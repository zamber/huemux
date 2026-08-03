package preset

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed presets
var presetFS embed.FS

// Builtin returns a parsed built-in preset by slug ("bass_pulse").
func Builtin(slug string) (*Preset, error) {
	data, err := presetFS.ReadFile("presets/" + slug + ".json")
	if err != nil {
		return nil, fmt.Errorf("unknown built-in preset %q", slug)
	}
	return Parse(data)
}

// BuiltinSlugs lists the available built-in preset slugs, sorted.
func BuiltinSlugs() []string {
	entries, err := presetFS.ReadDir("presets")
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		name := e.Name()
		if len(name) > 5 && name[len(name)-5:] == ".json" {
			slugs = append(slugs, name[:len(name)-5])
		}
	}
	sort.Strings(slugs)
	return slugs
}
