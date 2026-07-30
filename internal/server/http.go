// Package server is lightsync's loopback HTTP + WebSocket front end: the
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
	"sync"
	"time"

	lightsync "lights.lan/lightsync"
	"lights.lan/lightsync/internal/config"
	"lights.lan/lightsync/internal/engine"
	"lights.lan/lightsync/internal/hue"
	"lights.lan/lightsync/internal/lightctl"
	"lights.lan/lightsync/internal/pipeline"
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
// paired yet. Exported so cmd/lightsync's CLI status readout and stdin
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
	webFS, err := fs.Sub(lightsync.WebFS, "web")
	if err != nil {
		panic("embedded web/ directory missing: " + err.Error()) // programmer error, not a runtime condition
	}
	s.mux.Handle("/", http.FileServerFS(webFS))
	s.mux.HandleFunc("/api/areas", s.handleAreas)
	s.mux.HandleFunc("/api/status", s.handleStatusAPI)
	s.mux.HandleFunc("/api/lights", s.handleLights)
	s.mux.HandleFunc("/api/rooms", s.handleRooms)
	s.mux.HandleFunc("/api/scenes", s.handleScenes)
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
			log.Printf("lightsync: http server stopped: %v", err)
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

func (s *Server) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.statusMessage())
}

type statusWire struct {
	Type     string               `json:"type"`
	Paired   bool                 `json:"paired"`
	Pairing  *pairingState        `json:"pairing,omitempty"`
	Snapshot *engine.Status       `json:"snapshot,omitempty"`
	Zones    []engine.ZoneStatus  `json:"zones,omitempty"`
	Settings *config.AreaSettings `json:"settings,omitempty"`
}

func (s *Server) statusMessage() statusWire {
	eng := s.engine()
	if eng == nil {
		s.pairMu.Lock()
		ps := s.pairState
		s.pairMu.Unlock()
		return statusWire{Type: "status", Paired: false, Pairing: &ps}
	}
	snap := eng.Snapshot()
	return statusWire{Type: "status", Paired: true, Snapshot: &snap, Zones: snap.Zones, Settings: &snap.Settings}
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
		log.Printf("lightsync: websocket upgrade: %v", err)
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
			s.handleControlMessage(payload)
		}
	}
}

// handleGridFrame accepts a binary grid frame only from whichever
// connection is currently designated the frame source, claiming that role
// for the first connection to send one. Two tabs both capturing produces a
// strobe that is very hard to trace back from the light end, so only one is
// ever honoured.
func (s *Server) handleGridFrame(conn *Conn, payload []byte) {
	eng := s.engine()
	if eng == nil {
		return
	}

	s.mu.Lock()
	if s.frameSource == nil {
		s.frameSource = conn
	}
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

func (s *Server) handleControlMessage(payload []byte) {
	var msg controlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Printf("lightsync: bad control message: %v", err)
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
				log.Printf("lightsync: light_toggle %s: %v", msg.RID, err)
			}
			return
		case "light_brightness":
			if err := lights.SetLightBrightness(ctx, msg.RID, msg.Brightness); err != nil {
				log.Printf("lightsync: light_brightness %s: %v", msg.RID, err)
			}
			return
		case "light_color":
			if err := lights.SetLightColorRGB(ctx, msg.RID, msg.R, msg.G, msg.B); err != nil {
				log.Printf("lightsync: light_color %s: %v", msg.RID, err)
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
				log.Printf("lightsync: room_toggle %s: %v", msg.RID, err)
			}
			return
		case "room_brightness":
			if err := lights.SetRoomBrightness(ctx, msg.RID, msg.Brightness); err != nil {
				log.Printf("lightsync: room_brightness %s: %v", msg.RID, err)
			}
			return
		case "scene_recall":
			if err := lights.RecallScene(ctx, msg.RID); err != nil {
				log.Printf("lightsync: scene_recall %s: %v", msg.RID, err)
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
		if err := eng.SelectArea(ctx, msg.AreaID); err != nil {
			log.Printf("lightsync: select_area %s: %v", msg.AreaID, err)
		}
	case "stop":
		eng.Stop(ctx)
	case "settings":
		var settings config.AreaSettings
		if err := json.Unmarshal(msg.Settings, &settings); err != nil {
			log.Printf("lightsync: bad settings payload: %v", err)
			return
		}
		eng.UpdateSettings(settings)
	case "identify":
		if msg.LightRID != "" {
			if err := eng.Identify(ctx, msg.LightRID); err != nil {
				log.Printf("lightsync: identify %s: %v", msg.LightRID, err)
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
	username, clientkey, err := hue.Pair(ctx, bridgeIP, "lightsync#"+hostname, 60*time.Second)
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
		raw, err := json.Marshal(s.statusMessage())
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
