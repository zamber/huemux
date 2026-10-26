package mux

import "embed"

//go:embed presets/*.json
var builtinFS embed.FS

var builtins = func() map[string][]byte {
	out := map[string][]byte{}
	entries, _ := builtinFS.ReadDir("presets")
	for _, e := range entries {
		name := e.Name()
		raw, err := builtinFS.ReadFile("presets/" + name)
		if err != nil {
			continue
		}
		slug := name[:len(name)-len(".json")]
		out[slug] = raw
	}
	return out
}()
