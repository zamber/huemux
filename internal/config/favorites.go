package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FavoritesStore persists favourited light/room ids to a JSON file next to
// config.json/settings.json, same debounced-flush idiom as Store in
// settings.go. Room favourites use the "room:<rid>" prefix convention
// already established by lights-ui, so importing its existing favourites
// data (a handful of rows, hand-copied — see cmd/lightsync-desktop/README.md-
// style notes for the migration story) is a straight copy, not a transform.
//
// JSON, not SQLite: CGO_ENABLED=0 static builds rule out cgo-based SQLite
// drivers, and at this scale (a handful of ids) a pure-Go driver would buy
// nothing over a file Store already knows how to debounce-flush.
type FavoritesStore struct {
	path string

	mu       sync.Mutex
	byID     map[string]int64 // entity id ("light rid" or "room:<rid>") -> created_at unix
	timer    *time.Timer
	debounce time.Duration
}

// NewFavoritesStore loads (or initializes) favorites.json.
func NewFavoritesStore() (*FavoritesStore, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	s := &FavoritesStore{
		path:     filepath.Join(dir, "favorites.json"),
		byID:     map[string]int64{},
		debounce: 500 * time.Millisecond,
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.byID) // corrupt file: start fresh rather than fail to start
	}
	return s, nil
}

// IsFavorite reports whether id is currently favourited.
func (s *FavoritesStore) IsFavorite(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.byID[id]
	return ok
}

// Toggle flips id's favourite state and returns the new state.
func (s *FavoritesStore) Toggle(id string) bool {
	s.mu.Lock()
	_, was := s.byID[id]
	if was {
		delete(s.byID, id)
	} else {
		s.byID[id] = time.Now().Unix()
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.debounce, s.flush)
	s.mu.Unlock()
	return !was
}

// All returns a snapshot of every favourited id and when it was favourited.
func (s *FavoritesStore) All() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.byID))
	for k, v := range s.byID {
		out[k] = v
	}
	return out
}

// Flush forces an immediate write, e.g. on clean shutdown.
func (s *FavoritesStore) Flush() { s.flush() }

func (s *FavoritesStore) flush() {
	s.mu.Lock()
	raw, err := json.MarshalIndent(s.byID, "", "  ")
	path := s.path
	s.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
