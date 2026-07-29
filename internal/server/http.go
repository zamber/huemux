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
	"sync"
	"time"

	lightsync "lights.lan/lightsync"
	"lights.lan/lightsync/internal/config"
	"lights.lan/lightsync/internal/engine"
	"lights.lan/lightsync/internal/pipeline"
)

// Server is the loopback HTTP server. It binds 127.0.0.1 only — never
// 0.0.0.0 — because there is no authentication and the WebSocket it serves
// drives real lights.
type Server struct {
	eng *engine.Engine
	mux *http.ServeMux

	mu          sync.Mutex
	frameSource *Conn
	uiConns     map[*Conn]struct{}

	Addr string
}

// New builds a Server around an already-constructed Engine.
func New(eng *engine.Engine) *Server {
	s := &Server{
		eng:     eng,
		mux:     http.NewServeMux(),
		uiConns: map[*Conn]struct{}{},
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	webFS, err := fs.Sub(lightsync.WebFS, "web")
	if err != nil {
		panic("embedded web/ directory missing: " + err.Error()) // programmer error, not a runtime condition
	}
	s.mux.Handle("/", http.FileServerFS(webFS))
	s.mux.HandleFunc("/api/areas", s.handleAreas)
	s.mux.HandleFunc("/api/status", s.handleStatusAPI)
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	areas, err := s.eng.ListAreas(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(areas)
}

func (s *Server) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.statusMessage())
}

type statusWire struct {
	Type     string              `json:"type"`
	Snapshot engine.Status       `json:"snapshot"`
	Zones    []engine.ZoneStatus `json:"zones"`
	Settings config.AreaSettings `json:"settings"`
}

func (s *Server) statusMessage() statusWire {
	snap := s.eng.Snapshot()
	return statusWire{Type: "status", Snapshot: snap, Zones: snap.Zones, Settings: snap.Settings}
}

// controlMessage is every shape of JSON text frame the browser may send, per
// PROTOCOL.md §2.
type controlMessage struct {
	Type     string          `json:"type"`
	AreaID   string          `json:"area_id"`
	Settings json.RawMessage `json:"settings"`
	LightRID string          `json:"light_rid"`
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := Upgrade(w, r)
	if err != nil {
		log.Printf("lightsync: websocket upgrade: %v", err)
		return
	}
	defer conn.Close()

	s.eng.IncUIClient()
	s.mu.Lock()
	s.uiConns[conn] = struct{}{}
	s.mu.Unlock()

	statusDone := make(chan struct{})
	go s.pushStatusLoop(conn, statusDone)

	defer func() {
		close(statusDone)
		s.eng.DecUIClient()
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
	s.eng.SetFrame(grid)
}

func (s *Server) handleControlMessage(payload []byte) {
	var msg controlMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		log.Printf("lightsync: bad control message: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch msg.Type {
	case "select_area":
		if err := s.eng.SelectArea(ctx, msg.AreaID); err != nil {
			log.Printf("lightsync: select_area %s: %v", msg.AreaID, err)
		}
	case "stop":
		s.eng.Stop(ctx)
	case "settings":
		var settings config.AreaSettings
		if err := json.Unmarshal(msg.Settings, &settings); err != nil {
			log.Printf("lightsync: bad settings payload: %v", err)
			return
		}
		s.eng.UpdateSettings(settings)
	case "identify":
		if msg.LightRID != "" {
			if err := s.eng.Identify(ctx, msg.LightRID); err != nil {
				log.Printf("lightsync: identify %s: %v", msg.LightRID, err)
			}
		}
	}
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
