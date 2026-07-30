// Package server is huemux's loopback HTTP + WebSocket front end: the
// embedded UI, a small JSON API, and the /ws endpoint the browser's capture
// pipeline talks to.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/zamber/huemux"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/pipeline"
)

// Server is the loopback HTTP server. It binds 127.0.0.1 only — never
// 0.0.0.0 — because there is no authentication and the WebSocket it serves
// drives real lights.
type Server struct {
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

	mu          sync.Mutex
	frameSource *Conn
	uiConns     map[*Conn]struct{}

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
// separate CLI step before the UI is even reachable.
func New(store *config.Store, favorites *config.FavoritesStore, eng *engine.Engine, lights *lightctl.Service) *Server {
	s := &Server{
		store:     store,
		favorites: favorites,
		mux:       http.NewServeMux(),
		uiConns:   map[*Conn]struct{}{},
	}
	if eng != nil || lights != nil {
		s.setPaired(eng, lights)
	}
	s.routes()
	return s
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

	if lights != nil {
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
	s.mux.HandleFunc("/api/areas", s.handleAreas)
	s.mux.HandleFunc("/api/status", s.handleStatusAPI)
	s.mux.HandleFunc("/api/lights", s.handleLights)
	s.mux.HandleFunc("/api/rooms", s.handleRooms)
	s.mux.HandleFunc("/api/scenes", s.handleScenes)
	s.mux.HandleFunc("/api/favorites", s.handleFavorites)
	s.mux.HandleFunc("/api/locale", s.handleLocale)
	s.mux.HandleFunc("/ws", s.handleWS)
}

// ListenAndServe binds the first free port starting at 7654, trying ten
// ports before giving up — a taken default port is not a reason to fail to
// start.
func (s *Server) ListenAndServe() (string, error) {
	const base = 7654
	var ln net.Listener
	var err error
	var port int
	for port = base; port < base+10; port++ {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			break
		}
	}
	if ln == nil {
		return "", fmt.Errorf("no free port in %d-%d: %w", base, base+9, err)
	}
	s.Addr = fmt.Sprintf("127.0.0.1:%d", port)
	go func() {
		if err := http.Serve(ln, s.mux); err != nil {
			log.Printf("huemux: http server stopped: %v", err)
		}
	}()
	return "http://" + s.Addr, nil
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
		s.pairMu.Lock()
		ps := s.pairState
		s.pairMu.Unlock()
		msg = statusWire{Type: "status", Paired: false, Pairing: &ps}
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
	conn, err := Upgrade(w, r)
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
	s.notifyStreamStopped(previous, caller)
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

	// No restart needed: swap both services in and every handler that was
	// checking for nil starts working on its very next call.
	s.setPaired(engine.New(bridge, s.store), lightctl.New(bridge, s.favorites))

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
