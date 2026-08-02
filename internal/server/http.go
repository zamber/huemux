// Package server is huemux's loopback HTTP + WebSocket front end: the
// embedded UI, a small JSON API, and the /ws endpoint the browser's capture
// pipeline talks to.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zamber/huemux"
	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/pipeline"
)

// Server is the loopback HTTP server. It binds 127.0.0.1 only — never
// 0.0.0.0 — because there is no authentication and the WebSocket it serves
// drives real lights.
type Server struct {
	// cfgMu guards cfg, which stopped being immutable once /api/config could
	// PATCH it at runtime. Read through Config(), never directly.
	cfgMu     sync.RWMutex
	cfg       appconfig.Config
	store     *config.Store
	favorites *config.FavoritesStore
	mux       *http.ServeMux

	// p holds both paired-bridge services (screen-sync engine and light
	// control) behind one lock: they always become valid at the same
	// moment — pairing success — so one guarded struct is simpler than two
	// separate engMu/lightsMu pairs. Both are nil until paired; every
	// handler that needs either goes through engine()/lights() rather than
	// touching p directly, since pairing can complete at any point while
	// the server is already running and serving other tabs.
	pairedMu sync.RWMutex
	p        paired

	pairMu    sync.Mutex
	pairState pairingState

	authLimit *authLimiter

	lnMu sync.Mutex
	ln   net.Listener

	mu           sync.Mutex
	frameSource  *Conn
	uiConns      map[*Conn]struct{}
	lastFrameLog time.Time // throttles logFrameStats to at most once/second

	Addr string
}

type paired struct {
	eng    *engine.Engine
	lights *lightctl.Service
	cancel context.CancelFunc // stops the light-event broadcast goroutine below
}

// New builds a Server. eng may be nil if the bridge has not been paired
// yet — the server still starts and serves a web-driven pairing flow over
// the same /ws connection everything else uses, rather than requiring a
// separate CLI step before the UI is even reachable. eng is also nil under a
// profile that disables screen sync, which is why every handler that touches
// it already nil-checks rather than assuming a paired bridge implies an
// engine.
//
// cfg is retained because pairing happens *inside* the server: runPair
// constructs the paired services itself, long after startup, and has to build
// the same subset the profile asked for. Without it, pairing from the web UI
// would silently switch a disabled half of the app back on.
func New(cfg appconfig.Config, store *config.Store, favorites *config.FavoritesStore, eng *engine.Engine, lights *lightctl.Service) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		favorites: favorites,
		mux:       http.NewServeMux(),
		uiConns:   map[*Conn]struct{}{},
		authLimit: newAuthLimiter(),
	}
	if eng != nil || lights != nil {
		s.setPaired(eng, lights)
	}
	s.routes()
	return s
}

// Close releases the listener so the port is free again.
//
// Added because it was genuinely missing: without it a process that started a
// server could never give the port back, which showed up as the mobile
// facade's tests exhausting the port range after a handful of start/stop
// cycles. In-flight connections are not drained — there is no long-running
// work to lose, and every client reconnects on its own.
func (s *Server) Close() error {
	s.lnMu.Lock()
	ln := s.ln
	s.ln = nil
	s.lnMu.Unlock()
	if ln == nil {
		return nil
	}
	return ln.Close()
}

// guard wraps a handler with token authentication. Applied to every /api
// route and the WebSocket upgrade — not to the static UI, which is just an
// HTML shell that cannot do anything until one of these answers it.
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(w, r) {
			return
		}
		h(w, r)
	}
}

// Config returns the effective application configuration.
func (s *Server) Config() appconfig.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg
}

// setConfig replaces the effective configuration. Only /api/config calls
// this, and only from a loopback caller.
func (s *Server) setConfig(cfg appconfig.Config) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	s.cfg = cfg
}

// BuildPaired constructs the services the configured profile calls for from a
// paired bridge. Exported so the process entry points build exactly the same
// subset at startup that runPair builds after a web-driven pairing — one
// function, so the two paths cannot disagree about what a profile means.
//
// A nil return for either service is normal and expected downstream: every
// handler already nil-checks, since "not paired yet" produced the same shape
// long before profiles existed.
func BuildPaired(cfg appconfig.Config, bridge config.Bridge, store *config.Store, favorites *config.FavoritesStore) (*engine.Engine, *lightctl.Service) {
	var eng *engine.Engine
	if cfg.NeedsEngine() {
		eng = engine.New(bridge, store)
	}
	var lights *lightctl.Service
	if cfg.NeedsLightctl() {
		lights = lightctl.New(bridge, favorites)
	}
	return eng, lights
}

func (s *Server) buildPaired(bridge config.Bridge) (*engine.Engine, *lightctl.Service) {
	return BuildPaired(s.Config(), bridge, s.store, s.favorites)
}

func (s *Server) engine() *engine.Engine {
	s.pairedMu.RLock()
	defer s.pairedMu.RUnlock()
	return s.p.eng
}

func (s *Server) lights() *lightctl.Service {
	s.pairedMu.RLock()
	defer s.pairedMu.RUnlock()
	return s.p.lights
}

// Engine returns the current engine, or nil if the bridge has not been
// paired yet. Exported so cmd/huemux's CLI status readout and stdin
// commands can pick up an engine constructed later by a web-driven pairing
// flow, since that happens inside the server, not the process's original
// startup path.
func (s *Server) Engine() *engine.Engine { return s.engine() }

// Paired reports whether the bridge has been paired, independent of which
// services the profile actually constructed. Callers need this to tell "no
// engine because nothing is paired" apart from "no engine because the profile
// disabled screen sync" — under a lights-only profile the engine is nil
// forever, and treating that as unpaired would tell a working server to go
// and pair itself.
func (s *Server) Paired() bool {
	s.pairedMu.RLock()
	defer s.pairedMu.RUnlock()
	return s.p.eng != nil || s.p.lights != nil
}

// setPaired swaps in the pairing-derived services and (re)starts the
// light-event broadcast goroutine. Safe to call more than once — a second
// pairing (shouldn't normally happen, but no reason to leak if it does)
// cancels the previous broadcast goroutine before starting a new one.
func (s *Server) setPaired(eng *engine.Engine, lights *lightctl.Service) {
	ctx, cancel := context.WithCancel(context.Background())

	s.pairedMu.Lock()
	if s.p.cancel != nil {
		s.p.cancel()
	}
	s.p = paired{eng: eng, lights: lights, cancel: cancel}
	s.pairedMu.Unlock()

	// Gated on the Lights tab existing, not merely on lights being non-nil.
	// Subscribe opens a long-lived eventstream connection to the bridge, and
	// under a sync-only profile lightctl exists solely to answer /api/scenes
	// for the sync page's scene strip — there is no light-control UI for
	// those events to reach, so the subscription would be pure background
	// traffic nobody reads.
	if lights != nil && s.Config().ShowsLightsTab() {
		go s.runLightEventBroadcast(ctx, lights)
	}
}

// runLightEventBroadcast relays every event from the bridge's eventstream
// (translated into UI-ready lightctl.LightEvents) to every currently
// connected WS client — this is what gets the light-control panel
// SSE-like responsiveness without a second transport (see PROTOCOL.md §3).
func (s *Server) runLightEventBroadcast(ctx context.Context, lights *lightctl.Service) {
	for le := range lights.Subscribe(ctx) {
		raw, err := json.Marshal(lightEventWire{Type: "light_event", Event: le})
		if err != nil {
			continue
		}
		s.broadcast(raw)
	}
}

// broadcast sends raw to every currently connected WS client (frame source
// and UI tabs alike — favorite/light-event pushes are relevant to any open
// tab, not just the one that triggered them).
func (s *Server) broadcast(raw []byte) {
	s.mu.Lock()
	for conn := range s.uiConns {
		_ = conn.WriteMessage(opText, raw)
	}
	s.mu.Unlock()
}

type lightEventWire struct {
	Type  string              `json:"type"`
	Event lightctl.LightEvent `json:"event"`
}

// favoriteEventWire is pushed after a light_favorite toggle — ToggleFavorite
// has no eventstream counterpart (favorites are local state, not a bridge
// resource), so without this the requesting tab has no way to learn the new
// state short of re-fetching /api/lights.
type favoriteEventWire struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Favorite bool   `json:"favorite"`
}

func (s *Server) routes() {
	webFS, err := fs.Sub(huemux.WebFS, "web")
	if err != nil {
		panic("embedded web/ directory missing: " + err.Error()) // programmer error, not a runtime condition
	}
	fileServer := http.FileServerFS(webFS)
	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// There's no web/index.html for http.FileServerFS to fall back to —
		// root goes to the app.html shell, which hosts sync.html and
		// lights.html each in their own iframe (see shared/shell.js) so
		// switching between them doesn't tear down whichever one you leave.
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/app.html", http.StatusFound)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
	// Entertainment areas are meaningless without the sync engine, so this
	// one is profile-gated. The light-control routes below are not: the sync
	// page renders its own scene strip from /api/scenes, so they stay
	// reachable under every profile (see appconfig.Config.NeedsLightctl).
	if s.cfg.ShowsSyncTab() {
		s.mux.HandleFunc("/api/areas", s.guard(s.handleAreas))
	}
	s.mux.HandleFunc("/api/about", s.guard(s.handleAbout))
	s.mux.HandleFunc("/api/status", s.guard(s.handleStatusAPI))
	s.mux.HandleFunc("/api/lights", s.guard(s.handleLights))
	s.mux.HandleFunc("/api/rooms", s.guard(s.handleRooms))
	s.mux.HandleFunc("/api/scenes", s.guard(s.handleScenes))
	s.mux.HandleFunc("/api/favorites", s.guard(s.handleFavorites))
	s.mux.HandleFunc("/api/locale", s.guard(s.handleLocale))
	s.mux.HandleFunc("/api/config", s.guard(s.handleConfig))
	s.mux.HandleFunc("/api/diagnostics", s.guard(s.handleDiagnostics))
	s.mux.HandleFunc("/ws", s.guard(s.handleWS))
}

// ListenAndServe binds the configured address and starts serving.
//
// A port of 0 keeps the long-standing behaviour of scanning upward from the
// default: a port already in use is not a reason to refuse to start. An
// explicitly configured port is not scanned past, because silently landing on
// a different port than the one someone wrote down is worse than failing.
func (s *Server) ListenAndServe() (string, error) {
	cfg := s.Config()
	host := cfg.Listen.Host
	if host == "" {
		host = appconfig.DefaultHost
	}

	var ln net.Listener
	var err error
	var port int

	if cfg.Listen.Port == 0 {
		const span = 10
		for port = appconfig.DefaultPort; port < appconfig.DefaultPort+span; port++ {
			ln, err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
			if err == nil {
				break
			}
			ln = nil
		}
		if ln == nil {
			return "", fmt.Errorf("no free port in %d-%d on %s: %w",
				appconfig.DefaultPort, appconfig.DefaultPort+span-1, host, err)
		}
	} else {
		port = cfg.Listen.Port
		ln, err = net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			return "", fmt.Errorf("listen on %s:%d: %w", host, port, err)
		}
	}

	s.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	scheme := "http"

	if cfg.TLS.Mode != appconfig.TLSOff {
		tlsCfg, err := tlsConfigFor(cfg)
		if err != nil {
			_ = ln.Close()
			return "", err
		}
		ln = tls.NewListener(ln, tlsCfg)
		scheme = "https"
	}

	s.lnMu.Lock()
	s.ln = ln
	s.lnMu.Unlock()

	go func() {
		// securityHeaders wraps the mux for both plain HTTP and the
		// tls.NewListener path above — the TLS layer only wraps ln, so this
		// single serve call is the one place every response passes through.
		if err := http.Serve(ln, securityHeaders(s.mux)); err != nil {
			// A closed listener is the expected result of Close(), not a fault.
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("huemux: http server stopped: %v", err)
			}
		}
	}()
	return scheme + "://" + s.Addr, nil
}

// securityHeaders sets browser-side security headers on every HTTP response.
// The loopback binding is the primary security control, but when the server is
// exposed to the LAN (adb reverse, port forward, the desktop wrapper's bundled
// Chromium) the UI must not be served without them.
//
// Two frontend constraints shape the policy:
//   - script-src and style-src carry 'unsafe-inline' because every HTML page
//     in web/ has inline <script> blocks and the UI sets styles from JS
//     (element.style). Hardening those to hashes/nonces would mean rewriting
//     the frontend's script structure, which is out of scope for a header
//     middleware.
//   - X-Frame-Options is SAMEORIGIN, not DENY: app.html hosts sync.html,
//     lights.html and settings.html in same-origin iframes (see web/shell.js),
//     and DENY blocks same-origin framing too. SAMEORIGIN still blocks the
//     clickjacking case the header exists for — an external page framing this
//     UI.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; font-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAreas(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	eng := s.engine()
	if eng == nil {
		_, _ = w.Write([]byte("[]"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	areas, err := eng.ListAreas(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(areas)
}

func (s *Server) handleLights(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lights := s.lights()
	if lights == nil {
		_, _ = w.Write([]byte("[]"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := lights.ListLights(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lights := s.lights()
	if lights == nil {
		_, _ = w.Write([]byte("[]"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := lights.ListRooms(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) handleScenes(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	lights := s.lights()
	if lights == nil {
		_, _ = w.Write([]byte("[]"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := lights.ListScenes(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	_ = json.NewEncoder(w).Encode(list)
}

// handleFavorites exposes every favourited id (light, "room:<id>", scene,
// or the synthetic "all" pseudo-id for the all-lights tile) — the /api/
// lights and /api/rooms responses only carry a light's/room's own favorite
// flag, with no way to learn about ids that aren't lights or rooms.
func (s *Server) handleFavorites(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.favorites == nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(s.favorites.All())
}

// handleLocale exposes a locale hint derived from this *process's*
// environment, which i18n.js prefers over navigator.language when present.
// That's not redundant with the browser's own locale: under the Electron
// wrapper (cmd/huemux-desktop), the bundled Chromium's reported
// navigator.language often doesn't reflect the host OS locale at all (it
// depends on how Electron itself was launched/packaged), while this
// process's environment — inherited from whatever shell, systemd unit, or
// desktop launcher started it — usually does.
func (s *Server) handleLocale(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"lang": detectSystemLang()})
}

// detectSystemLang reads the standard POSIX locale environment variables in
// their usual precedence order. LANGUAGE is a GNU extension and may be a
// colon-separated priority list; the others are single locale strings like
// "pl_PL.UTF-8". Returns "" (no opinion) rather than guessing if none of
// them name a language this app actually supports.
func detectSystemLang() string {
	supported := map[string]bool{"pl": true, "en": true}
	for _, key := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		v := os.Getenv(key)
		if v == "" || v == "C" || v == "POSIX" {
			continue
		}
		first := strings.SplitN(v, ":", 2)[0]
		code := first
		if i := strings.IndexAny(code, "_.@"); i >= 0 {
			code = code[:i]
		}
		code = strings.ToLower(code)
		if supported[code] {
			return code
		}
	}
	return ""
}

func (s *Server) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.statusMessage(nil))
}

type statusWire struct {
	Type     string               `json:"type"`
	Paired   bool                 `json:"paired"`
	Pairing  *pairingState        `json:"pairing,omitempty"`
	Snapshot *engine.Status       `json:"snapshot,omitempty"`
	Zones    []engine.ZoneStatus  `json:"zones,omitempty"`
	Settings *config.AreaSettings `json:"settings,omitempty"`

	// Multiple WS clients (a browser tab and the desktop app, say) can be
	// connected at once, but only one is ever s.frameSource — claimed
	// explicitly by select_area (see claimFrameSource), which evicts and
	// notifies whoever held it before. Without these fields, a second
	// client has no way to know its own "sync" is a local-only preview
	// that isn't actually reaching the bridge. SourceHeld/YouAreSource are
	// per-connection, computed fresh for whoever asked, unlike the rest of
	// this struct which is identical for every recipient.
	SourceHeld   bool `json:"source_held"`
	YouAreSource bool `json:"you_are_source"`
}

func (s *Server) statusMessage(conn *Conn) statusWire {
	var msg statusWire
	eng := s.engine()
	if eng == nil {
		// Paired comes from the bridge, not from whether a sync engine
		// exists. Under a lights-only profile the engine is nil forever by
		// design, and reporting Paired:false there told the browser to show
		// the pairing panel on a fully working, already-paired server — every
		// status push, so it could never get past it. Exactly the bug fixed
		// for the CLI readout in Server.Paired(); this path was missed.
		//
		// The pairing state still rides along: harmless when paired, and it
		// is what drives the panel when genuinely unpaired.
		s.pairMu.Lock()
		ps := s.pairState
		s.pairMu.Unlock()
		msg = statusWire{Type: "status", Paired: s.Paired(), Pairing: &ps}
	} else {
		snap := eng.Snapshot()
		msg = statusWire{Type: "status", Paired: true, Snapshot: &snap, Zones: snap.Zones, Settings: &snap.Settings}
	}
	s.mu.Lock()
	msg.SourceHeld = s.frameSource != nil
	msg.YouAreSource = conn != nil && s.frameSource == conn
	s.mu.Unlock()
	return msg
}

// controlMessage is every shape of JSON text frame the browser may send, per
// PROTOCOL.md §2 (screen-sync), §3 (light control) and the pairing extension.
type controlMessage struct {
	Type     string          `json:"type"`
	AreaID   string          `json:"area_id"`
	Settings json.RawMessage `json:"settings"`
	LightRID string          `json:"light_rid"`
	BridgeIP string          `json:"bridge_ip"`

	// Light control (internal/lightctl) — RID is a light id for
	// light_*/scene_recall messages, a grouped_light id for room_*.
	RID        string  `json:"rid"`
	On         bool    `json:"on"`
	Brightness float64 `json:"brightness"`
	R          uint8   `json:"r"`
	G          uint8   `json:"g"`
	B          uint8   `json:"b"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := Upgrade(w, r, s.Config().Listen.Host)
	if err != nil {
		log.Printf("huemux: websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	if eng := s.engine(); eng != nil {
		eng.IncUIClient()
		defer eng.DecUIClient()
	}
	s.mu.Lock()
	s.uiConns[conn] = struct{}{}
	s.mu.Unlock()

	statusDone := make(chan struct{})
	go s.pushStatusLoop(conn, statusDone)

	defer func() {
		close(statusDone)
		s.mu.Lock()
		delete(s.uiConns, conn)
		if s.frameSource == conn {
			s.frameSource = nil
		}
		s.mu.Unlock()
	}()

	for {
		opcode, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch opcode {
		case opBinary:
			s.handleGridFrame(conn, payload)
		case opText:
			s.handleControlMessage(conn, payload)
		}
	}
}

// handleGridFrame accepts a binary grid frame only from whichever
// connection currently holds the frame source, claimed explicitly via
// select_area (see claimFrameSource) rather than by whoever happens to send
// a frame first. Two tabs both capturing produces a strobe that is very
// hard to trace back from the light end, so only one is ever honoured.
func (s *Server) handleGridFrame(conn *Conn, payload []byte) {
	eng := s.engine()
	if eng == nil {
		return
	}

	s.mu.Lock()
	isSource := s.frameSource == conn
	s.mu.Unlock()
	if !isSource {
		return
	}

	if len(payload) < 3 || payload[0] != 0x01 {
		return
	}
	w, h := int(payload[1]), int(payload[2])
	want := 3 + w*h*3
	if len(payload) < want {
		return
	}
	grid := &pipeline.Grid{W: w, H: h, Pix: append([]byte(nil), payload[3:want]...)}
	eng.SetFrame(grid)

	if debuglog.Enabled {
		s.logFrameStats(conn, grid)
	}
}

// logFrameStats logs a throttled (at most once/second) average-color summary
// of incoming grid frames when -debug is on. This is the one signal this
// server ever had zero visibility into: whether "grid frames are arriving"
// actually means real, varying capture data, or a capture path silently
// handing back a fixed placeholder (e.g. the solid-green frame observed when
// Wayland screen capture runs without the PipeWire feature switch — see
// cmd/huemux-desktop/provisioner.go's pipeWireCapturePatch). An unchanging
// average across many logged frames is the tell.
func (s *Server) logFrameStats(conn *Conn, grid *pipeline.Grid) {
	s.mu.Lock()
	skip := time.Since(s.lastFrameLog) < time.Second
	if !skip {
		s.lastFrameLog = time.Now()
	}
	s.mu.Unlock()
	if skip {
		return
	}

	var sumR, sumG, sumB int
	n := len(grid.Pix) / 3
	for i := 0; i < n; i++ {
		sumR += int(grid.Pix[i*3])
		sumG += int(grid.Pix[i*3+1])
		sumB += int(grid.Pix[i*3+2])
	}
	if n == 0 {
		n = 1
	}
	log.Printf("huemux debug: frame from %s: %dx%d grid, avg rgb=(%d,%d,%d)",
		connAddr(conn), grid.W, grid.H, sumR/n, sumG/n, sumB/n)
}

// claimFrameSource makes conn the frame source, evicting whoever held it
// before. Called from select_area, i.e. whenever a client actually starts
// (or restarts) a sync session — explicit, rather than the old "first
// connection to send a grid frame wins" implicit claim, so that starting
// sync from a second client visibly preempts the first instead of just
// having its own frames silently dropped forever with no feedback.
func (s *Server) claimFrameSource(conn *Conn) {
	s.mu.Lock()
	previous := s.frameSource
	s.frameSource = conn
	s.mu.Unlock()
	if debuglog.Enabled {
		log.Printf("huemux debug: claimFrameSource: %s becomes source (was %s)", connAddr(conn), connAddr(previous))
	}
	s.notifyStreamStopped(previous, conn)
}

// stopStreamAndNotifySource stops being any connection's frame source.
// Called whenever anyone sends "stop" — including from the Lights page,
// which has no frame of its own but can still cut a sync session started
// elsewhere, mirroring the real Hue app's "turn off entertainment sync to
// control lights directly" behavior.
func (s *Server) stopStreamAndNotifySource(caller *Conn) {
	s.mu.Lock()
	previous := s.frameSource
	s.frameSource = nil
	s.mu.Unlock()
	if debuglog.Enabled {
		log.Printf("huemux debug: stopStreamAndNotifySource: called by %s, was source %s", connAddr(caller), connAddr(previous))
	}
	s.notifyStreamStopped(previous, caller)
}

// connAddr is a nil-safe, human-readable identifier for a *Conn in debug
// logs — remote port is enough to tell two loopback WS clients (e.g. a
// browser tab and the desktop app) apart from each other.
func connAddr(c *Conn) string {
	if c == nil {
		return "<none>"
	}
	return c.rwc.RemoteAddr().String()
}

// notifyStreamStopped tells previous (if it exists and isn't exceptConn,
// which already knows) that its stream was stopped out from under it — by
// a preemption or a remote stop — so its UI can stop local capture and
// reset its preview instead of leaving it frozen on the last frame,
// looking like it's still streaming when it no longer is.
func (s *Server) notifyStreamStopped(previous, exceptConn *Conn) {
	if previous == nil || previous == exceptConn {
		return
	}
	raw, err := json.Marshal(map[string]string{"type": "stream_stopped"})
	if err != nil {
		return
	}
	_ = previous.WriteMessage(opText, raw)
}

func (s *Server) handleControlMessage(conn *Conn, payload []byte) {
	var msg controlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Printf("huemux: bad control message: %v", err)
		return
	}

	switch msg.Type {
	case "discover_bridges":
		go s.runDiscovery()
		return
	case "pair":
		go s.runPair(msg.BridgeIP)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Light-control messages (PROTOCOL.md §3), routed here first since
	// their types never overlap with the screen-sync ones below.
	if lights := s.lights(); lights != nil {
		switch msg.Type {
		case "light_toggle":
			if err := lights.SetLightOn(ctx, msg.RID, msg.On); err != nil {
				log.Printf("huemux: light_toggle %s: %v", msg.RID, err)
			}
			return
		case "light_brightness":
			if err := lights.SetLightBrightness(ctx, msg.RID, msg.Brightness); err != nil {
				log.Printf("huemux: light_brightness %s: %v", msg.RID, err)
			}
			return
		case "light_color":
			if err := lights.SetLightColorRGB(ctx, msg.RID, msg.R, msg.G, msg.B); err != nil {
				log.Printf("huemux: light_color %s: %v", msg.RID, err)
			}
			return
		case "light_favorite":
			fav := lights.ToggleFavorite(msg.RID)
			if raw, err := json.Marshal(favoriteEventWire{Type: "favorite_event", ID: msg.RID, Favorite: fav}); err == nil {
				s.broadcast(raw)
			}
			return
		case "room_toggle":
			if err := lights.SetRoomOn(ctx, msg.RID, msg.On); err != nil {
				log.Printf("huemux: room_toggle %s: %v", msg.RID, err)
			}
			return
		case "room_brightness":
			if err := lights.SetRoomBrightness(ctx, msg.RID, msg.Brightness); err != nil {
				log.Printf("huemux: room_brightness %s: %v", msg.RID, err)
			}
			return
		case "scene_recall":
			if err := lights.RecallScene(ctx, msg.RID); err != nil {
				log.Printf("huemux: scene_recall %s: %v", msg.RID, err)
			}
			return
		}
	}

	eng := s.engine()
	if eng == nil {
		return // everything below drives an active screen-sync session; nothing to do until paired
	}

	switch msg.Type {
	case "select_area":
		s.claimFrameSource(conn)
		if err := eng.SelectArea(ctx, msg.AreaID); err != nil {
			log.Printf("huemux: select_area %s: %v", msg.AreaID, err)
		}
	case "stop":
		eng.Stop(ctx)
		s.stopStreamAndNotifySource(conn)
	case "settings":
		var settings config.AreaSettings
		if err := json.Unmarshal(msg.Settings, &settings); err != nil {
			log.Printf("huemux: bad settings payload: %v", err)
			return
		}
		eng.UpdateSettings(settings)
	case "identify":
		if msg.LightRID != "" {
			if err := eng.Identify(ctx, msg.LightRID); err != nil {
				log.Printf("huemux: identify %s: %v", msg.LightRID, err)
			}
		}
	}
}

// pairingState is pushed as part of every status message while unpaired, so
// the pairing UI works the same way everything else in this app does: watch
// the WS status stream, don't poll a separate endpoint.
type pairingState struct {
	Discovering bool               `json:"discovering"`
	Discovered  []discoveredBridge `json:"discovered,omitempty"`
	Pairing     bool               `json:"pairing"`
	Message     string             `json:"message,omitempty"`
	Error       string             `json:"error,omitempty"`
}

type discoveredBridge struct {
	IP        string `json:"ip"`
	Name      string `json:"name"`
	ID        string `json:"id"`
	Supported bool   `json:"supported"`
}

// runDiscovery tries mDNS then cloud discovery (see internal/hue/discover.go)
// and probes each candidate for its name/model, so the UI can show something
// more useful than a bare IP list. Manual IP entry, handled entirely
// client-side, remains the path of record for segmented VLANs where neither
// discovery method reaches the bridge.
func (s *Server) runDiscovery() {
	s.pairMu.Lock()
	s.pairState.Discovering = true
	s.pairState.Discovered = nil
	s.pairState.Error = ""
	s.pairMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	ips := hue.Discover(ctx)
	found := make([]discoveredBridge, 0, len(ips))
	for _, ip := range ips {
		info, err := hue.BridgeConfig(ctx, ip)
		if err != nil {
			continue
		}
		found = append(found, discoveredBridge{
			IP: ip, Name: info.Name, ID: info.BridgeID,
			Supported: hue.SupportsEntertainment(info),
		})
	}

	s.pairMu.Lock()
	s.pairState.Discovering = false
	s.pairState.Discovered = found
	s.pairMu.Unlock()
}

// runPair drives the full pairing flow against a chosen bridge IP,
// publishing progress into pairState for the status push to pick up. It
// must not block the WS read loop that triggered it — pairing waits up to
// 60s for a physical button press — so callers run it via `go`.
func (s *Server) runPair(bridgeIP string) {
	s.pairMu.Lock()
	if s.pairState.Pairing {
		s.pairMu.Unlock()
		return // a pairing attempt is already in flight; ignore the duplicate
	}
	s.pairState.Pairing = true
	s.pairState.Error = ""
	s.pairState.Message = "Contacting bridge..."
	s.pairMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	info, err := hue.BridgeConfig(ctx, bridgeIP)
	if err != nil {
		s.pairFail(fmt.Sprintf("could not contact a bridge at %s: %v", bridgeIP, err))
		return
	}
	if !hue.SupportsEntertainment(info) {
		s.pairFail(fmt.Sprintf("the bridge at %s (model %s) is too old to support Entertainment areas", bridgeIP, info.ModelID))
		return
	}

	s.pairMu.Lock()
	s.pairState.Message = "Press the link button on the bridge now..."
	s.pairMu.Unlock()

	hostname, _ := os.Hostname()
	username, clientkey, err := hue.Pair(ctx, bridgeIP, "huemux#"+hostname, 60*time.Second)
	if err != nil {
		s.pairFail(err.Error())
		return
	}

	bridge := config.Bridge{BridgeIP: bridgeIP, BridgeID: info.BridgeID, Username: username, ClientKey: clientkey}
	if err := config.SaveBridge(bridge); err != nil {
		s.pairFail(fmt.Sprintf("saving config: %v", err))
		return
	}

	// No restart needed: swap the services in and every handler that was
	// checking for nil starts working on its very next call.
	//
	// Built through the profile rather than unconditionally: this used to
	// construct both services regardless, which meant pairing from the web UI
	// silently re-enabled whichever half the profile had disabled — a
	// --profile=lights server would quietly acquire a screen-sync engine the
	// moment someone completed pairing, and keep it until restart.
	s.setPaired(s.buildPaired(bridge))

	s.pairMu.Lock()
	s.pairState = pairingState{Message: "Paired"}
	s.pairMu.Unlock()
}

func (s *Server) pairFail(msg string) {
	s.pairMu.Lock()
	s.pairState.Pairing = false
	s.pairState.Error = msg
	s.pairMu.Unlock()
}

// pushStatusLoop sends a status snapshot at least once a second, per
// PROTOCOL.md. Faster on-change pushes are left as a future refinement;
// 1 Hz is frequent enough that the UI never looks stale.
func (s *Server) pushStatusLoop(conn *Conn, done chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()

	send := func() {
		raw, err := json.Marshal(s.statusMessage(conn))
		if err != nil {
			return
		}
		if err := conn.WriteMessage(opText, raw); err != nil {
			return
		}
	}
	send() // immediately, so a newly connected tab isn't waiting a full second
	for {
		select {
		case <-done:
			return
		case <-t.C:
			send()
		}
	}
}
