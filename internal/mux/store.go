package mux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ValidSlug reports whether s is usable as a preset filename.
func ValidSlug(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	return slugRE.MatchString(s)
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// PresetInfo is one entry in the preset list.
type PresetInfo struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
}

// ErrBuiltinSlug is returned when a caller tries to write or delete a
// built-in preset.
var ErrBuiltinSlug = errors.New("mux: built-in presets are read-only")

// Store persists user presets in one directory and serves a small embedded
// built-in set. The current editing document is stored separately so it can
// be restored on restart without appearing as a named preset.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates dir if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// List returns built-ins first, then user presets sorted by slug.
func (s *Store) List() []PresetInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]PresetInfo, 0, 8)
	for _, slug := range builtinSlugs() {
		if b, ok := builtins[slug]; ok {
			var d Document
			if json.Unmarshal(b, &d) == nil {
				out = append(out, PresetInfo{Slug: slug, Name: d.Name, Builtin: true})
			}
		}
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	var user []PresetInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".json")
		if slug == "current" || !ValidSlug(slug) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var d Document
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		name := d.Name
		if name == "" {
			name = slug
		}
		user = append(user, PresetInfo{Slug: slug, Name: name})
	}
	sort.Slice(user, func(i, j int) bool { return user[i].Slug < user[j].Slug })
	return append(out, user...)
}

// Get returns a preset by slug, user files first.
func (s *Store) Get(slug string) ([]byte, bool) {
	if !ValidSlug(slug) {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if raw, err := os.ReadFile(filepath.Join(s.dir, slug+".json")); err == nil {
		return raw, true
	}
	if raw, ok := builtins[slug]; ok {
		return append([]byte(nil), raw...), true
	}
	return nil, false
}

// Put writes a user preset atomically.
func (s *Store) Put(slug string, raw []byte) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("mux: invalid preset slug %q", slug)
	}
	if _, ok := builtins[slug]; ok {
		return ErrBuiltinSlug
	}
	if _, err := ParseDocument(raw); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(filepath.Join(s.dir, slug+".json"), raw)
}

// Delete removes a user preset.
func (s *Store) Delete(slug string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("mux: invalid preset slug %q", slug)
	}
	if _, ok := builtins[slug]; ok {
		return ErrBuiltinSlug
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.dir, slug+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// LoadCurrent returns the last editing document, if one was saved.
func (s *Store) LoadCurrent() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.dir, "current.json"))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// SaveCurrent writes the editing document.
func (s *Store) SaveCurrent(raw []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeAtomic(filepath.Join(s.dir, "current.json"), raw)
}

func writeAtomic(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// ParseDocument unmarshals the document envelope without compiling it.
// Compile is the validating step; this is for listing and save-time checks.
func ParseDocument(raw []byte) (Document, error) {
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, fmt.Errorf("mux: invalid document JSON: %w", err)
	}
	if d.Version != 1 {
		return d, fmt.Errorf("mux: unsupported document version %d", d.Version)
	}
	return d, nil
}

func builtinSlugs() []string {
	out := make([]string, 0, len(builtins))
	for k := range builtins {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
