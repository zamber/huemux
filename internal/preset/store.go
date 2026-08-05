package preset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Summary is a lightweight preset listing entry.
type Summary struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidSlug reports whether s is a safe preset filename.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

// Store manages user presets on disk. Built-in presets are resolved from
// embed.FS via Builtin; user presets live as individual JSON files.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir, creating it if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("preset store: %w", err)
	}
	return &Store{dir: dir}, nil
}

// List returns all available presets: builtins first, then user presets.
func (s *Store) List() []Summary {
	var out []Summary
	for _, slug := range BuiltinSlugs() {
		b, _ := Builtin(slug)
		name, desc := slug, ""
		if b != nil {
			name, desc = b.Name, b.Description
		}
		out = append(out, Summary{Slug: slug, Name: name, Description: desc, Builtin: true})
	}
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		slug := e.Name()[:len(e.Name())-5]
		if !ValidSlug(slug) {
			continue
		}
		if _, ok := builtinSlug(slug); ok {
			continue // don't shadow builtins in listings
		}
		name := slug
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err == nil {
			p, perr := Parse(raw)
			if perr == nil {
				name = p.Name
			}
		}
		out = append(out, Summary{Slug: slug, Name: name, Builtin: false})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// Get returns the raw JSON for a preset. User presets take priority over
// builtins. ok is false when the slug is unknown.
func (s *Store) Get(slug string) ([]byte, bool) {
	if !ValidSlug(slug) {
		return nil, false
	}
	path := filepath.Join(s.dir, slug+".json")
	if raw, err := os.ReadFile(path); err == nil {
		return raw, true
	}
	return BuiltinRaw(slug)
}

// Put writes a preset document to disk atomically. Returns ErrBuiltinSlug
// when the slug shadows a builtin.
func (s *Store) Put(slug string, doc []byte) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid preset slug %q", slug)
	}
	if _, ok := builtinSlug(slug); ok {
		return fmt.Errorf("%w: %s", ErrBuiltinSlug, slug)
	}
	path := filepath.Join(s.dir, slug+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, doc, 0o600); err != nil {
		return fmt.Errorf("write preset %q: %w", slug, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic save preset %q: %w", slug, err)
	}
	return nil
}

// Delete removes a user preset. Returns ErrBuiltinSlug for builtins.
func (s *Store) Delete(slug string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid preset slug %q", slug)
	}
	if _, ok := builtinSlug(slug); ok {
		return fmt.Errorf("%w: %s", ErrBuiltinSlug, slug)
	}
	path := filepath.Join(s.dir, slug+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete preset %q: %w", slug, err)
	}
	return nil
}

// Load resolves a preset for the engine: user file first, then builtin.
func (s *Store) Load(slug string) (*Preset, error) {
	raw, ok := s.Get(slug)
	if !ok {
		return nil, fmt.Errorf("preset %q not found", slug)
	}
	return Parse(raw)
}

// ErrBuiltinSlug is returned when an operation would shadow a built-in preset.
var ErrBuiltinSlug = errors.New("cannot overwrite built-in preset")

// builtinSlug reports whether slug matches a built-in.
func builtinSlug(slug string) (string, bool) {
	for _, b := range BuiltinSlugs() {
		if b == slug {
			return b, true
		}
	}
	return "", false
}
