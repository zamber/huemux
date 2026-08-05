package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zamber/huemux/internal/preset"
)

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.presets == nil {
		http.Error(w, "preset store not available", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.presets.List())
}

func (s *Server) handlePresetCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(preset.CatalogMeta())
}

func (s *Server) handlePresetSlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug == "" {
		http.Error(w, "missing slug", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handlePresetGet(w, r, slug)
	case http.MethodPut:
		s.handlePresetPut(w, r, slug)
	case http.MethodDelete:
		s.handlePresetDelete(w, r, slug)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handlePresetGet(w http.ResponseWriter, r *http.Request, slug string) {
	if s.presets == nil {
		http.Error(w, "preset store not available", http.StatusServiceUnavailable)
		return
	}
	raw, ok := s.presets.Get(slug)
	if !ok {
		http.Error(w, "preset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) handlePresetPut(w http.ResponseWriter, r *http.Request, slug string) {
	if s.presets == nil {
		http.Error(w, "preset store not available", http.StatusServiceUnavailable)
		return
	}
	if !preset.ValidSlug(slug) {
		http.Error(w, "invalid preset slug", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10) // 256 KB
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large or unreadable", http.StatusBadRequest)
		return
	}
	if _, err := preset.Parse(raw); err != nil {
		http.Error(w, "invalid preset: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.presets.Put(slug, raw); err != nil {
		if errors.Is(err, preset.ErrBuiltinSlug) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePresetDelete(w http.ResponseWriter, r *http.Request, slug string) {
	if s.presets == nil {
		http.Error(w, "preset store not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.presets.Delete(slug); err != nil {
		if errors.Is(err, preset.ErrBuiltinSlug) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
