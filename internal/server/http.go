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
	"github.com/zamber/huemux/internal/music"
	"github.com/zamber/huemux/internal/pipeline"
	"github.com/zamber/huemux/internal/preset"
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
	presets   *preset.Store
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

	mu            sync.Mutex
	frameSource   *Conn
	musicSource   *Conn // connection owning audio capture (music reactivity)
	uiConns       map[*Conn]struct{}
	lastFrameLog  time.Time // throttles logFrameStats to at most once/second
	lastStreamLog time.Time // throttles stream-telemetry summaries to at most once/5s
	lastPreviewAt time.Time // throttles the grid echo to at most 10 fps

	// Stream counters feed the debug push and the diagnostics report. All
	// guarded by mu like the rest of the connection state.
	framesAccepted uint64 // grids handed to the engine
	framesDropped  uint64 // grids from a non-source connection
	pcmChunks      uint64 // PCM buffers fed to the analyzer
	pcmBytes       uint64 // PCM bytes fed to the analyzer
	audioFrames    uint64 // analysis frames written to the music state

	// musicOffExplicit is true when the user explicitly set the music
	// preset to "" (Off). When set, autoActivateMusic() stays quiet so
	// the user's choice is not silently overridden on the next PCM chunk.
	musicOffExplicit bool

	// music holds the latest audio analysis frame. Immutable pointer after
	// construction, so it needs no lock of its own on top of State's.
	music *music.State

	// audioAna turns raw PCM (Android internal audio, pushed over the mobile
	// facade) into the same frames the browser's 0x02 path sends. Guarded by
	// audioMu: gomobile invokes from arbitrary threads.
	audioMu  sync.Mutex
	audioAna *music.Analyzer

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
		music:     music.New(),
		audioAna:  &music.Analyzer{},
	}
	if dir, err := config.Dir(); err == nil {
		s.presets, _ = preset.NewStore(dir + "/presets")
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

// RestartListener closes the current listener and starts a new one on the
// address in cfg. It validates the new bind before closing the old one, so a
// bad address never tears down a working server. Returns the new display URL.
func (s *Server) RestartListener(cfg appconfig.Config) (string, error) {
	host := cfg.Listen.Host
	if host == "" {
		host = appconfig.DefaultHost
	}
	port := cfg.Listen.Port
	if port == 0 {
		port = appconfig.DefaultPort
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	newLn, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("listen on %s: %w", addr, err)
	}

	scheme := "http"
	if cfg.TLS.Mode != appconfig.TLSOff {
		tlsCfg, tlsErr := tlsConfigFor(cfg)
		if tlsErr != nil {
			_ = newLn.Close()
			return "", tlsErr
		}
		newLn = tls.NewListener(newLn, tlsCfg)
		scheme = "https"
	}

	// Close old listener — drops all WS connections, but every client
	// reconnects on its own (1500ms backoff in app.js and lights.js).
	_ = s.Close()

	s.lnMu.Lock()
	s.ln = newLn
	s.lnMu.Unlock()

	displayAddr := net.JoinHostPort(displayHost(host), strconv.Itoa(port))
	s.Addr = displayAddr

	go func() {
		if err := http.Serve(newLn, s.mux); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("huemux: http server stopped: %v", err)
			}
		}
	}()

	return scheme + "://" + displayAddr, nil
}

// broadcastConfigChanged pushes a config_changed message to every connected
// WS client so each frame can update its features/auth state live.
func (s *Server) broadcastConfigChanged() {
	cfg := s.Config()
	lanIPs := LocalAddresses()
	lanStrs := make([]string, len(lanIPs))
	for i, ip := range lanIPs {
		lanStrs[i] = ip.String()
	}
	w := configChangedWire{
		Type:    "config_changed",
		Profile: string(cfg.Profile),
		Lights:  cfg.ShowsLightsTab(),
		Sync:    cfg.ShowsSyncTab(),
		Auth: configAuthWire{
			Mode:     string(cfg.Auth.Mode),
			HasToken: strings.TrimSpace(cfg.Auth.Token) != "",
		},
		Listen: configListenWire{
			Host: displayHost(cfg.Listen.Host),
			Port: listenPort(s.Addr),
		},
		LanAddresses: lanStrs,
	}
	raw, err := json.Marshal(w)
	if err != nil {
		return
	}
	s.broadcast(raw)
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

	// The music capture lives in the server (it owns the WS), the output
	// clock in the engine — wire the two together whenever an engine comes
	// into existence, at startup and after a web-driven pairing alike. The
	// engine wants (frame, active); the state's Snapshot also carries a
	// frame counter, dropped here.
	if eng != nil {
		eng.SetMusicFrameSource(func() (music.Frame, bool) {
			f, ok, _ := s.music.Snapshot()
			return f, ok
		})
		if s.presets != nil {
			eng.SetPresetLoader(s.presets.Load)
		}
	}

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

// lightsSnapshotWire is the full light+room state pushed to a client that
// just connected or explicitly asked for a resync — the "catch up on anything
// the eventstream delivered while you were away" payload. Without it a
// reconnecting client is stuck with whatever it last fetched until the *next*
// light change happens to arrive as a light_event.
//
// The bridge's eventstream has no replay/history, so the server cannot
// reconstruct what the client missed; it reads the current truth fresh from
// the REST API instead. lights+rooms cover everything the panel renders from
// live bridge state — per-light and grouped_light (aggregate) on/off/
// brightness/color alike.
type lightsSnapshotWire struct {
	Type   string           `json:"type"`
	Lights []lightctl.Light `json:"lights"`
	Rooms  []lightctl.Room  `json:"rooms"`
}

// pushLightsSnapshot fetches the current light+room state fresh from the
// bridge and sends it to one client. Runs in its own goroutine because the
// bridge round-trips take network time; a write to a conn that closed
// meanwhile is simply dropped (WriteMessage errors, and the goroutine ends).
// Gated on the Lights tab existing — under a sync-only profile the lightctl
// service exists solely to answer /api/scenes for the sync page, and pushing
// light state to that page is pure waste.
func (s *Server) pushLightsSnapshot(conn *Conn) {
	lights := s.lights()
	if lights == nil || !s.Config().ShowsLightsTab() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snap, err := lights.Snapshot(ctx)
	if err != nil {
		log.Printf("huemux: lights_snapshot: %v", err)
		return
	}
	raw, err := json.Marshal(lightsSnapshotWire{Type: "lights_snapshot", Lights: snap.Lights, Rooms: snap.Rooms})
	if err != nil {
		return
	}
	_ = conn.WriteMessage(opText, raw)
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

// configChangedWire is broadcast to every WS client after /api/config changes,
// so each frame can update its features/auth state without a full reload.
type configChangedWire struct {
	Type         string           `json:"type"`
	Profile      string           `json:"profile"`
	Lights       bool             `json:"lights"`
	Sync         bool             `json:"sync"`
	Auth         configAuthWire   `json:"auth"`
	Listen       configListenWire `json:"listen"`
	LanAddresses []string         `json:"lan_addresses"`
}

type configAuthWire struct {
	Mode     string `json:"mode"`
	HasToken bool   `json:"has_token"`
}

type configListenWire struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// displayHost returns 127.0.0.1 when host is a wildcard, so the printed URL
// and Electron window URL are actually connectable. A browser cannot connect
// to 0.0.0.0 or :: — those are bind-only meta-addresses.
func displayHost(host string) string {
	if isWildcardHost(host) {
		return "127.0.0.1"
	}
	return host
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
	s.mux.HandleFunc("/api/presets", s.guard(s.handlePresets))
	s.mux.HandleFunc("/api/presets/catalog", s.guard(s.handlePresetCatalog))
	s.mux.HandleFunc("/api/presets/{slug}", s.guard(s.handlePresetSlug))
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

	s.Addr = net.JoinHostPort(displayHost(host), strconv.Itoa(port))
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
		if err := http.Serve(ln, s.mux); err != nil {
			// A closed listener is the expected result of Close(), not a fault.
			if !errors.Is(err, net.ErrClosed) {
				log.Printf("huemux: http server stopped: %v", err)
			}
		}
	}()
	return scheme + "://" + s.Addr, nil
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

	// Music carries the latest audio analysis frame while a browser is
	// capturing (music reactivity, Phase 1). Absent until then, so the
	// common case pays nothing.
	Music *musicStatusWire `json:"music,omitempty"`
}

// musicStatusWire is the music-reactivity block of the status push: whether
// a browser is sending audio frames, how many have arrived, and the latest
// 32 FFT bands plus 256 wave samples. The arrays let the UI and the Go
// analysis primitives see exactly what the engine received — the browser
// already has its own copy, so these exist to prove the pipe, not to feed
// the preview.
type musicStatusWire struct {
	Active bool      `json:"active"`
	Frames uint64    `json:"frames"`
	FFT    []float32 `json:"fft"`
	Wave   []float32 `json:"wave"`
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

	if f, active, frames := s.music.Snapshot(); active {
		// Copied, not aliased: the wire struct must not share backing
		// arrays with whatever the next frame will overwrite.
		msg.Music = &musicStatusWire{
			Active: true,
			Frames: frames,
			FFT:    append([]float32(nil), f.FFT[:]...),
			Wave:   append([]float32(nil), f.Wave[:]...),
		}
	}
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

	// Preset names a built-in music-reactivity preset for music_preset;
	// "" deactivates music and hands the output back to screen sync.
	Preset string `json:"preset"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := Upgrade(w, r, s.Config().Listen.Host)
	if err != nil {
		log.Printf("huemux: websocket upgrade: %v", err)
		return
	}
	defer conn.Close() //nolint:errcheck

	if eng := s.engine(); eng != nil {
		eng.IncUIClient()
		defer eng.DecUIClient()
	}
	s.mu.Lock()
	s.uiConns[conn] = struct{}{}
	s.mu.Unlock()

	statusDone := make(chan struct{})
	debugDone := make(chan struct{})
	go s.pushStatusLoop(conn, statusDone)
	go s.pushDebugLoop(conn, debugDone)

	// Inform a (re)connecting client of the current light+room state. A
	// connection that was dropped while the app was backgrounded comes back
	// with a stale in-memory grid — the bridge's eventstream can't replay what
	// it missed, so the server pushes the current truth fresh on connect. The
	// frontend's foreground handshake (resync_lights) covers the case where
	// the WS never actually dropped; this covers the one where it did.
	go s.pushLightsSnapshot(conn)

	defer func() {
		close(statusDone)
		close(debugDone)
		s.removeConn(conn)
	}()

	for {
		opcode, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch opcode {
		case opBinary:
			// 0x01 is a screen-sync grid frame, 0x02 an audio frame for
			// music reactivity. Dispatched here rather than inside the grid
			// handler so an audio frame can never be misread as the start
			// of a (different-sized) grid frame.
			if len(payload) > 0 && payload[0] == music.TypeByte {
				s.handleAudioFrame(conn, payload)
			} else {
				s.handleGridFrame(conn, payload)
			}
		case opText:
			s.handleControlMessage(conn, payload)
		}
	}
}

// PushAudioPCM feeds raw PCM captured on the host (Android internal audio
// via the mobile facade) into the same music state the browser's 0x02
// frames write. Analysis runs here with a pure-Go FFT — DP-7's headless
// case, arrived with Android's MediaProjection audio capture.
func (s *Server) PushAudioPCM(pcm []byte, sampleRate int) {
	if len(pcm) == 0 || sampleRate <= 0 {
		return
	}
	s.audioMu.Lock()
	frames := s.audioAna.Feed(pcm, sampleRate)
	s.audioMu.Unlock()

	gain, floor := s.audioGainFloor()
	s.mu.Lock()
	s.pcmChunks++
	s.pcmBytes += uint64(len(pcm))
	s.audioFrames += uint64(len(frames))
	s.mu.Unlock()
	for _, f := range frames {
		s.music.Update(music.ApplyGainFloor(f, gain, floor))
	}
	s.autoActivateMusic()
	// Stream-telemetry summary, at most once every 5 seconds. Lives in the
	// stream ring, never the app-event ring — a busy capture must not bury
	// one-off events in the diagnostics report.
	s.mu.Lock()
	skip := time.Since(s.lastStreamLog) < 5*time.Second
	if !skip {
		s.lastStreamLog = time.Now()
	}
	s.mu.Unlock()
	if !skip && len(frames) > 0 {
		debuglog.Streamf("pcm: %d bytes @ %d Hz → %d analysis frames, bass=%.3f",
			len(pcm), sampleRate, len(frames), frames[0].FFT[0])
	}
}

// audioGainFloor returns the audio-pickup settings to apply to the analysis
// frame, preferring the engine's per-area settings and falling back to the UI
// defaults when no engine exists — music-only capture and pre-pairing capture
// have no area to draw settings from, and the histogram must still benefit.
func (s *Server) audioGainFloor() (gain, floor float64) {
	eng := s.engine()
	if eng == nil {
		return 2.0, 0
	}
	_, _, gain, floor = eng.DebugSettings()
	return gain, floor
}

// removeConn forgets a disconnected connection: unregisters it as a UI
// client and releases whatever source roles it held. Extracted from the
// handleWS defer so tests can exercise the disconnect path directly.
func (s *Server) removeConn(conn *Conn) {
	s.mu.Lock()
	delete(s.uiConns, conn)
	if s.frameSource == conn {
		s.frameSource = nil
	}
	wasMusicSource := s.musicSource == conn
	if wasMusicSource {
		s.musicSource = nil
	}
	s.mu.Unlock()
	// The music source is gone; whatever state it built up is stale and must
	// not keep reporting as live analysis. Cleared outside the lock — the
	// state has its own, and Update (which takes only State's) never runs
	// under s.mu.
	if wasMusicSource {
		s.music.Clear()
	}
}

// handleAudioFrame accepts a binary audio frame (type 0x02, see
// internal/music) from whichever connection is the music source. Music
// capture deliberately has no claim handshake in Phase 1 — unlike grid
// frames it needs neither a paired bridge nor a selected area — so the most
// recent connection to send a valid frame owns the source and every other
// connection's frames are dropped. That keeps two capturing tabs from
// interleaving their audio into one incoherent analysis stream; the
// take-over is immediate and needs no message because the newest sender is
// whoever last pressed Start.
func (s *Server) handleAudioFrame(conn *Conn, payload []byte) {
	f, ok := music.ParseFrame(payload)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.musicSource != nil && s.musicSource != conn {
		s.mu.Unlock()
		return
	}
	s.musicSource = conn
	s.mu.Unlock()
	gain, floor := s.audioGainFloor()
	s.music.Update(music.ApplyGainFloor(f, gain, floor))

	// Auto-activate the default music preset on the first audio frame
	// when capture mode is audio or audiovideo. This is what turns audio
	// capture from "frames arriving but going nowhere" into actual light
	// output — the user should not have to manually select a preset on
	// top of starting capture.
	s.autoActivateMusic()

	s.logMusicStats()
}

// autoActivateMusic activates the default preset (bass_pulse) on first
// audio frame if the engine is in audio/audiovideo mode and no preset
// is active. Idempotent — the preset is only set once per capture session.
func (s *Server) autoActivateMusic() {
	eng := s.engine()
	if eng == nil {
		return
	}
	mode := eng.CaptureMode()
	if mode != engine.CaptureAudio && mode != engine.CaptureAudioVideo {
		return
	}
	if eng.MusicPreset() != "" {
		return // already active, user made an explicit choice
	}
	s.mu.Lock()
	off := s.musicOffExplicit
	s.mu.Unlock()
	if off {
		return
	}
	channels, positions := eng.MusicLayout()
	if channels == nil {
		// No area selected yet — harmless, the next audio frame will retry.
		return
	}
	if err := eng.ActivateMusic("bass_pulse", channels, positions); err != nil {
		log.Printf("huemux: auto-activate bass_pulse: %v", err)
	} else {
		debuglog.Audiof("auto-activated bass_pulse preset (mode=%s)", mode)
	}
}

// logMusicStats logs a throttled (at most once/5s) summary of incoming
// audio frames to the stream ring — always, regardless of -debug, so a
// diagnostics report from a phone has evidence that audio frames were (or
// were not) arriving. Stream telemetry, so it shares lastStreamLog with the
// PCM summary and writes to the stream ring, keeping the app-event ring
// readable.
func (s *Server) logMusicStats() {
	s.mu.Lock()
	skip := time.Since(s.lastStreamLog) < 5*time.Second
	if !skip {
		s.lastStreamLog = time.Now()
	}
	s.mu.Unlock()
	if skip {
		return
	}
	f, active, frames := s.music.Snapshot()
	if !active {
		return
	}
	debuglog.Streamf("frames=%d bass=%.3f wave0=%.3f", frames, f.FFT[0], f.Wave[0])
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
	if !isSource {
		s.framesDropped++
	}
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
	grid := s.acceptGrid(w, h, payload[3:want])

	if grid != nil && debuglog.Enabled {
		s.logFrameStats(conn, grid)
	}
}

// acceptGrid validates, copies and forwards one RGB grid to the engine and
// the debug echo. Shared by the browser frame path (handleGridFrame) and the
// Android path (PushFrame) so both reach the same stream counters and the
// preview echo — previously Android frames called eng.SetFrame directly and
// never touched any of this.
func (s *Server) acceptGrid(w, h int, pix []byte) *pipeline.Grid {
	const maxGridPixels = 640 * 360 // ~230k pixels
	if w < 0 || h < 0 || w*h > maxGridPixels {
		return nil
	}
	eng := s.engine()
	if eng == nil {
		return nil
	}
	grid := &pipeline.Grid{W: w, H: h, Pix: append([]byte(nil), pix...)}
	s.mu.Lock()
	s.framesAccepted++
	s.mu.Unlock()
	eng.SetFrame(grid)
	s.maybeBroadcastPreview(grid)
	return grid
}

// PushFrame hands one captured RGB frame (w*h*3 tightly packed bytes) to the
// colour pipeline, routing it through the same acceptance path as a browser
// grid frame so Android frames reach the stream counters and the debug echo.
// Exported for the mobile facade. The caller still checks the engine exists;
// this re-validates defensively.
func (s *Server) PushFrame(w, h int, rgb []byte) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("huemux: bad frame size %dx%d", w, h)
	}
	if want := w * h * 3; len(rgb) != want {
		return fmt.Errorf("huemux: frame is %d bytes, want %d for %dx%d RGB", len(rgb), want, w, h)
	}
	if s.engine() == nil {
		return errors.New("huemux: screen sync is disabled by the current profile")
	}
	s.acceptGrid(w, h, rgb)
	return nil
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
	case "music_stop":
		// Music capture stopped on the page while the WS connection stays
		// open (it carries the UI too), so the disconnect path never fires.
		// Without this the status push would keep reporting the last frame
		// as live analysis forever. Any connection may clear it — the same
		// rule as the grid stream's "stop".
		s.mu.Lock()
		s.musicSource = nil
		s.musicOffExplicit = false // next capture session may auto-activate
		s.mu.Unlock()
		s.music.Clear()
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
		case "resync_lights":
			// Foreground handshake / post-reconnect catch-up: fetch the full
			// light+room state fresh from the bridge and push it to this
			// client. The eventstream cannot replay what the client missed
			// while it was backgrounded, so a resync is a fresh REST read,
			// not a replay. Async: the bridge round-trips would stall the
			// connection's read loop.
			go s.pushLightsSnapshot(conn)
			return
		}
	}

	eng := s.engine()
	if eng == nil {
		return // everything below drives an active screen-sync session; nothing to do until paired
	}

	switch msg.Type {
	case "capture_mode":
		// Route the output loop to video, audio, or audiovideo input.
		// Reuses the Preset field for the mode string. Takes effect on
		// the next tick; no restart needed.
		mode := engine.CaptureMode(msg.Preset)
		eng.SetCaptureMode(mode)
		debuglog.Audiof("capture_mode set to %s", mode)
	case "music_preset":
		// Activate a built-in music-reactivity preset (or deactivate with
		// ""). Needs the engine for the area's light/channel layout; the
		// audio frames are already flowing through SetMusicFrameSource.
		// An unknown slug or an unselected area fails loudly in the log
		// rather than leaving the UI guessing.
		//
		// An explicit "" (Off) sets musicOffExplicit so the next PCM
		// chunk does not silently re-activate bass_pulse. Choosing a
		// named preset clears the flag.
		s.mu.Lock()
		s.musicOffExplicit = msg.Preset == ""
		s.mu.Unlock()
		channels, positions := eng.MusicLayout()
		if err := eng.ActivateMusic(msg.Preset, channels, positions); err != nil {
			log.Printf("huemux: music_preset %q: %v", msg.Preset, err)
		}
	case "select_area":
		s.claimFrameSource(conn)
		if err := eng.SelectArea(ctx, msg.AreaID); err != nil {
			log.Printf("huemux: select_area %s: %v", msg.AreaID, err)
		}
		// A music preset pins the light→channel layout of the area it was
		// activated on. Switching areas must re-apply it or the runner
		// keeps painting the old area's channel ids.
		if slug := eng.MusicPreset(); slug != "" {
			channels, positions := eng.MusicLayout()
			if err := eng.ActivateMusic(slug, channels, positions); err != nil {
				log.Printf("huemux: re-activate music preset after area switch: %v", err)
			}
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
		settings = settings.Validate()
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
	username, clientkey, certSHA256, err := hue.Pair(ctx, bridgeIP, "huemux#"+hostname, 60*time.Second)
	if err != nil {
		s.pairFail(err.Error())
		return
	}

	bridge := config.Bridge{BridgeIP: bridgeIP, BridgeID: info.BridgeID, Username: username, ClientKey: clientkey, CertSHA256: certSHA256}
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

// debugWire is the lightweight debug push the histogram and capture readouts
// are driven from. Unlike the 1 Hz status push it is emitted up to debug_hz
// times per second, so a histogram that needs fast updates to "bump high" is
// not stuck at once a second.
type debugWire struct {
	Type     string                `json:"type"`
	FPSIn    float64               `json:"fps_in"`
	Frames   uint64                `json:"frames"`
	CaptureW int                   `json:"capture_w"`
	CaptureH int                   `json:"capture_h"`
	GridW    int                   `json:"grid_w"`
	GridH    int                   `json:"grid_h"`
	Music    *musicStatusWire      `json:"music,omitempty"`
	Nodes    []preset.NodeSnapshot `json:"nodes,omitempty"`
}

// debugMessage assembles one debug push. The music block is included only
// while a source is active, mirroring the status push.
func (s *Server) debugMessage(eng *engine.Engine) debugWire {
	cw, ch, gw, gh := eng.CaptureStats()
	s.mu.Lock()
	frames := s.framesAccepted
	s.mu.Unlock()
	w := debugWire{
		Type:     "debug",
		FPSIn:    eng.InboundFPS(),
		Frames:   frames,
		CaptureW: cw,
		CaptureH: ch,
		GridW:    gw,
		GridH:    gh,
	}
	if f, active, frameCount := s.music.Snapshot(); active {
		w.Music = &musicStatusWire{
			Active: true,
			Frames: frameCount,
			FFT:    append([]float32(nil), f.FFT[:]...),
			Wave:   append([]float32(nil), f.Wave[:]...),
		}
	}
	if eng.MusicPreset() != "" {
		w.Nodes = eng.NodeSnapshots()
	}
	return w
}

// debugInterval returns how often the debug push should fire for a given
// debug_hz setting. Clamped to the same range Validate enforces, so a
// settings record that never passed Validate cannot feed a division by zero.
func debugInterval(hz int) time.Duration {
	if hz < 1 {
		hz = 10
	}
	if hz > 30 {
		hz = 30
	}
	return time.Second / time.Duration(hz)
}

// pushDebugLoop drives the debug push. It ticks at a fixed 33 ms cadence and
// sends a message only when the elapsed time since the last send reaches
// 1000/debug_hz — so raising debug_hz takes effect within one tick with no
// ticker rebuild, and the goroutine is identical regardless of the setting.
// Skipped entirely while no engine exists: the capture statistics it would
// carry are engine state.
func (s *Server) pushDebugLoop(conn *Conn, done chan struct{}) {
	t := time.NewTicker(33 * time.Millisecond)
	defer t.Stop()
	var last time.Time // zero: the first tick always sends
	for {
		select {
		case <-done:
			return
		case <-t.C:
			eng := s.engine()
			if eng == nil {
				continue
			}
			hz, _, _, _ := eng.DebugSettings()
			if time.Since(last) < debugInterval(hz) {
				continue
			}
			last = time.Now()
			raw, err := json.Marshal(s.debugMessage(eng))
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(opText, raw); err != nil {
				return
			}
		}
	}
}
