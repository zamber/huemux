package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/mux"
	"github.com/zamber/huemux/internal/muxhue"
	"github.com/zamber/huemux/internal/muxrun"
)

// muxRoom is the only room the multiplexer drives. The user asked for the
// Salon lamps specifically; keeping the default here rather than in the UI
// means the API cannot accidentally be pointed at the bedroom by a stale
// document.
const muxRoom = "Salon"

// attachMux resolves the Salon inventory and builds the runtime. It runs in
// the background after pairing because /api/lights talks to the bridge and
// startup should not wait for it.
func (s *Server) attachMux(lights *lightctl.Service) {
	if lights == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	inv, err := s.resolveMuxLights(ctx, lights)
	cancel()
	if err != nil {
		log.Printf("huemux: mux: resolving %s lights: %v", muxRoom, err)
		return
	}
	if len(inv) == 0 {
		log.Printf("huemux: mux: no colorable lights in room %q", muxRoom)
		return
	}

	doc := mux.DefaultDocument(inv)
	if s.muxStore != nil {
		if raw, ok := s.muxStore.LoadCurrent(); ok {
			if d, err := mux.ParseDocument(raw); err == nil {
				if _, err := mux.Compile(d, inv); err == nil {
					doc = d
				}
			}
		}
	}
	prog, err := mux.Compile(doc, inv)
	if err != nil {
		doc = mux.DefaultDocument(inv)
		prog, err = mux.Compile(doc, inv)
		if err != nil {
			log.Printf("huemux: mux: default document does not compile: %v", err)
			return
		}
	}

	sink := muxhue.New(lights, doc.Output.TransitionMS, 4)
	rt := muxrun.New(inv, sink, muxrun.Providers{Audio: s.muxAudioProvider()})
	rt.SetProgram(prog)

	s.muxMu.Lock()
	old := s.muxRT
	s.muxRT = rt
	s.muxSink = sink
	s.muxDoc = doc
	s.muxInv = inv
	s.muxMu.Unlock()
	if old != nil {
		old.Close()
	}
}

// muxAudioProvider adapts the server's existing music.State into the mux's
// transport-free AudioInput. The mux never opens a capture path of its own.
func (s *Server) muxAudioProvider() func() mux.AudioInput {
	return func() mux.AudioInput {
		f, active, _ := s.music.Snapshot()
		if !active {
			return mux.AudioInput{}
		}
		bands := make([]float64, len(f.FFT))
		for i, v := range f.FFT {
			bands[i] = float64(v)
		}
		wave := make([]float64, len(f.Wave))
		for i, v := range f.Wave {
			wave[i] = float64(v)
		}
		return mux.AudioInput{Active: true, Bands: bands, Wave: wave}
	}
}

func (s *Server) resolveMuxLights(ctx context.Context, lights *lightctl.Service) ([]mux.Light, error) {
	list, err := lights.ListLights(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]mux.Light, 0, len(list))
	for _, l := range list {
		if l.RoomName != muxRoom || !l.Colorable {
			continue
		}
		out = append(out, mux.Light{ID: l.ID, Name: l.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// The preview needs two dimensions and no bridge round-trip for a room
	// layout. A circle is stable and makes radial effects meaningful even
	// before the entertainment-area positions are wired in.
	n := len(out)
	for i := range out {
		if n == 0 {
			break
		}
		a := 2*math.Pi*float64(i)/float64(n) - math.Pi/2
		out[i].X = 0.5 + 0.38*math.Cos(a)
		out[i].Y = 0.5 + 0.38*math.Sin(a)
	}
	return out, nil
}

type muxView struct {
	Catalog mux.CatalogData  `json:"catalog"`
	Doc     mux.Document     `json:"doc"`
	Lights  []mux.Light      `json:"lights"`
	Status  muxrun.Snapshot  `json:"status"`
	Presets []mux.PresetInfo `json:"presets"`
	Ready   bool             `json:"ready"`
	Error   string           `json:"error,omitempty"`
}

type muxPutRequest struct {
	Doc  mux.Document `json:"doc"`
	Slug string       `json:"slug,omitempty"`
}

func (s *Server) handleMux(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleMuxGet(w, r)
	case http.MethodPut:
		s.handleMuxPut(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMuxGet(w http.ResponseWriter, r *http.Request) {
	s.muxMu.Lock()
	rt, doc, inv, store := s.muxRT, s.muxDoc, s.muxInv, s.muxStore
	s.muxMu.Unlock()

	if doc.Name == "" && len(inv) > 0 {
		doc = mux.DefaultDocument(inv)
	}
	view := muxView{
		Catalog: mux.Catalog(),
		Doc:     doc,
		Lights:  inv,
		Ready:   rt != nil,
	}
	if rt != nil {
		view.Status = rt.Snapshot()
	} else {
		view.Error = "bridge not paired yet"
	}
	if store != nil {
		view.Presets = store.List()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(view)
}

func (s *Server) handleMuxPut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusBadRequest)
		return
	}
	var req muxPutRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.muxMu.Lock()
	rt, inv, store := s.muxRT, append([]mux.Light(nil), s.muxInv...), s.muxStore
	s.muxMu.Unlock()
	if rt == nil || len(inv) == 0 {
		http.Error(w, "bridge not paired yet", http.StatusServiceUnavailable)
		return
	}
	prog, err := mux.Compile(req.Doc, inv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rt.SetProgram(prog)

	docRaw, err := json.Marshal(req.Doc)
	if err != nil {
		http.Error(w, "could not encode document", http.StatusInternalServerError)
		return
	}
	if store != nil {
		_ = store.SaveCurrent(docRaw)
		if req.Slug != "" {
			if err := store.Put(req.Slug, docRaw); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	s.muxMu.Lock()
	s.muxDoc = req.Doc
	s.muxMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleMuxState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.muxMu.Lock()
	rt := s.muxRT
	s.muxMu.Unlock()
	if rt == nil {
		http.Error(w, "bridge not paired yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rt.Snapshot())
}

func (s *Server) handleMuxStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.muxMu.Lock()
	rt, sink := s.muxRT, s.muxSink
	s.muxMu.Unlock()
	if rt == nil {
		http.Error(w, "bridge not paired yet", http.StatusServiceUnavailable)
		return
	}
	if sink != nil {
		sink.Reset()
	}
	if err := rt.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMuxStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.muxMu.Lock()
	rt := s.muxRT
	s.muxMu.Unlock()
	if rt == nil {
		http.Error(w, "bridge not paired yet", http.StatusServiceUnavailable)
		return
	}
	rt.Stop()
	w.WriteHeader(http.StatusNoContent)
}

// handleMuxOff is the "leave it dark" action: stop the clock, forget the
// duplicate-suppression cache, and send one all-off frame. It is separate
// from Stop because stopping a running effect should leave the last frame up,
// not black the room.
func (s *Server) handleMuxOff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.muxMu.Lock()
	rt, sink, inv := s.muxRT, s.muxSink, s.muxInv
	s.muxMu.Unlock()
	if rt == nil {
		http.Error(w, "bridge not paired yet", http.StatusServiceUnavailable)
		return
	}
	rt.Stop()
	if sink != nil {
		sink.Reset()
		out := make(map[string]mux.LightState, len(inv))
		for _, l := range inv {
			out[l.ID] = mux.LightState{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := sink.Apply(ctx, out); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMuxPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.muxMu.Lock()
	store := s.muxStore
	s.muxMu.Unlock()
	var list []mux.PresetInfo
	if store != nil {
		list = store.List()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func (s *Server) handleMuxPresetSlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "missing slug", http.StatusBadRequest)
		return
	}
	s.muxMu.Lock()
	store := s.muxStore
	s.muxMu.Unlock()
	if store == nil {
		http.Error(w, "preset store not available", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		raw, ok := store.Get(slug)
		if !ok {
			http.Error(w, "preset not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	case http.MethodPut:
		r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "body too large or unreadable", http.StatusBadRequest)
			return
		}
		if err := store.Put(slug, raw); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, mux.ErrBuiltinSlug) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		if err := store.Delete(slug); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, mux.ErrBuiltinSlug) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
