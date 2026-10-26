// Package mux is the HueMux multiplexer: a small, pure effects engine that
// composes signal sources with light effects. It knows nothing about HTTP,
// Hue, or wall-clock scheduling; a caller supplies the time and the current
// audio/video inputs, and gets per-light linear RGB back. That is what makes
// the primitive catalogue testable without a bridge.
//
// The document model is a stack of lanes. Each lane is source -> effect ->
// blend, and the stack is what the "remix" UI edits. Internally this is
// still a tiny dataflow graph (the same idea as docs/MUSIC-REACTIVITY.md's
// DAG) with the wiring made implicit because a lane only ever has one of
// each. A future graph view can lower the same document without changing the
// primitives.
package mux

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Light is one addressable light and its normalized room position (0..1).
// Positions are only a preview/hint for spatial effects; index order is
// always available as a fallback for strips.
type Light struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
}

// AudioInput is a rate-independent slice of the latest analysis frame. The
// mux does not own capture or FFT; the runtime adapts music.Frame into this
// shape so the core stays free of the audio transport.
type AudioInput struct {
	Active bool
	Bands  []float64 // 0..1, low to high
	Wave   []float64 // -1..1
}

// VideoInput is the latest per-light screen-sync sample.
type VideoInput struct {
	Active bool
	Colors map[string]Color
}

// Inputs is everything a source may read in one tick.
type Inputs struct {
	Now   time.Time
	Audio AudioInput
	Video VideoInput
}

// SourceOutput is one source's result for a tick: named scalars, plus
// per-light colors for the video source.
type SourceOutput struct {
	Values map[string]float64 `json:"values,omitempty"`
	Colors map[string]Color   `json:"-"`
}

// Value returns a named scalar, or zero when the source does not provide it.
func (o SourceOutput) Value(name string) float64 {
	if name == "" {
		name = "value"
	}
	return o.Values[name]
}

// Source is one signal generator.
type Source interface {
	Type() string
	Sample(in Inputs) SourceOutput
}

// RenderInput is what an effect sees.
type RenderInput struct {
	Now    time.Time
	Source SourceOutput
	Lights []Light
}

// Effect turns one source output into premultiplied linear RGB per light.
// A pixel's brightness is already folded into its channels; the mixer
// converts peak channel to the output brightness after blending.
type Effect interface {
	Type() string
	Render(in RenderInput) map[string]Color
}

// Blend names how a lane combines with everything below it.
type Blend string

const (
	BlendReplace  Blend = "replace"
	BlendAdd      Blend = "add"
	BlendMax      Blend = "max"
	BlendScreen   Blend = "screen"
	BlendMultiply Blend = "multiply"
	BlendOver     Blend = "over"
)

// Blends is the stable, UI-facing list.
var Blends = []Blend{BlendReplace, BlendAdd, BlendMax, BlendScreen, BlendMultiply, BlendOver}

// BlendColor combines two premultiplied pixels. dstPresent distinguishes
// "nothing painted here yet" from black, which matters for multiply.
func BlendColor(dst Color, dstPresent bool, src Color, mode Blend) Color {
	if !dstPresent {
		return src
	}
	switch mode {
	case BlendAdd:
		return dst.Add(src)
	case BlendMax:
		return dst.Max(src)
	case BlendScreen:
		return Color{
			R: 1 - (1-dst.R)*(1-src.R),
			G: 1 - (1-dst.G)*(1-src.G),
			B: 1 - (1-dst.B)*(1-src.B),
		}
	case BlendMultiply:
		return Color{
			R: clamp01(dst.R * src.R),
			G: clamp01(dst.G * src.G),
			B: clamp01(dst.B * src.B),
		}
	case BlendOver:
		a := src.Peak()
		return Color{
			R: clamp01(src.R + dst.R*(1-a)),
			G: clamp01(src.G + dst.G*(1-a)),
			B: clamp01(src.B + dst.B*(1-a)),
		}
	default: // replace
		return src
	}
}

// Lane is one compiled layer.
type Lane struct {
	ID      string
	Name    string
	Enabled bool
	Source  Source
	Effect  Effect
	Blend   Blend
	Level   float64
	Targets []string // empty = all program lights
}

// OutputConfig controls the bridge-facing clock, not the preview clock.
type OutputConfig struct {
	Hz           int `json:"hz"`
	TransitionMS int `json:"transition_ms"`
}

// Program is a validated document ready to tick.
type Program struct {
	Name   string
	Room   string
	Lights []Light
	Lanes  []Lane
	Master float64
	Output OutputConfig
}

// LightState is one light's final output: a normalized linear color plus the
// peak brightness it should be shown at.
type LightState struct {
	On         bool
	Brightness float64
	Color      Color
}

// TickResult is one program evaluation.
type TickResult struct {
	Output     map[string]LightState
	Pixels     map[string]Color
	LaneValues map[string]SourceOutput
}

// Step runs the whole stack at one instant.
func (p *Program) Step(in Inputs) TickResult {
	res := TickResult{
		Output:     make(map[string]LightState, len(p.Lights)),
		Pixels:     make(map[string]Color, len(p.Lights)),
		LaneValues: make(map[string]SourceOutput, len(p.Lanes)),
	}
	acc := make(map[string]Color, len(p.Lights))
	present := make(map[string]bool, len(p.Lights))

	for i := range p.Lanes {
		lane := &p.Lanes[i]
		if !lane.Enabled {
			continue
		}
		src := lane.Source.Sample(in)
		res.LaneValues[lane.ID] = src
		lights := p.targets(lane.Targets)
		px := lane.Effect.Render(RenderInput{Now: in.Now, Source: src, Lights: lights})
		for rid, c := range px {
			c = c.Scale(lane.Level)
			acc[rid] = BlendColor(acc[rid], present[rid], c, lane.Blend)
			present[rid] = true
		}
	}

	master := p.Master
	if master < 0 {
		master = 0
	}
	if master > 1 {
		master = 1
	}
	for rid, c := range acc {
		c = c.Scale(master)
		res.Pixels[rid] = c
		res.Output[rid] = stateFromPixel(c)
	}
	return res
}

func (p *Program) targets(ids []string) []Light {
	if len(ids) == 0 {
		return p.Lights
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]Light, 0, len(ids))
	for _, l := range p.Lights {
		if want[l.ID] {
			out = append(out, l)
		}
	}
	return out
}

func stateFromPixel(c Color) LightState {
	peak := c.Peak()
	if peak < 0.01 {
		return LightState{On: false}
	}
	return LightState{
		On:         true,
		Brightness: clamp01(peak),
		Color:      c.Normalized(),
	}
}

// Document is the on-disk / API shape. It is deliberately human-readable:
// a saved mux is a few kilobytes of JSON, and being able to paste it into a
// bug report is worth more than compactness.
type Document struct {
	Version int          `json:"version"`
	Name    string       `json:"name"`
	Room    string       `json:"room"`
	Lights  []string     `json:"lights"`
	Master  MasterDoc    `json:"master"`
	Lanes   []LaneDoc    `json:"lanes"`
	Output  OutputConfig `json:"output"`
}

// MasterDoc uses a pointer so an explicit 0 (all off) differs from an absent
// field (default 1).
type MasterDoc struct {
	Brightness *float64 `json:"brightness,omitempty"`
}

// NodeDoc names a registered primitive and its raw parameters.
type NodeDoc struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// LaneDoc is one editable lane.
type LaneDoc struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Enabled *bool    `json:"enabled,omitempty"`
	Source  NodeDoc  `json:"source"`
	Effect  NodeDoc  `json:"effect"`
	Blend   string   `json:"blend,omitempty"`
	Level   *float64 `json:"level,omitempty"`
	Targets []string `json:"targets,omitempty"`
}

// Compile validates a document and instantiates its primitives. resolved is
// the light inventory the caller owns (the server resolves the Salon room);
// a document that pins its own ids narrows that inventory.
func Compile(doc Document, resolved []Light) (*Program, error) {
	if doc.Version != 1 {
		return nil, fmt.Errorf("mux: unsupported document version %d", doc.Version)
	}
	if doc.Name == "" {
		doc.Name = "Untitled mux"
	}
	if doc.Room == "" {
		doc.Room = "Salon"
	}
	if len(doc.Lanes) > 16 {
		return nil, fmt.Errorf("mux: %d lanes is more than the 16-lane limit", len(doc.Lanes))
	}

	lights := resolved
	if len(doc.Lights) > 0 {
		want := make(map[string]bool, len(doc.Lights))
		for _, id := range doc.Lights {
			want[id] = true
		}
		lights = nil
		for _, l := range resolved {
			if want[l.ID] {
				lights = append(lights, l)
			}
		}
		if len(lights) != len(doc.Lights) {
			return nil, fmt.Errorf("mux: document names a light that is not in the room")
		}
	}
	if len(lights) == 0 {
		return nil, fmt.Errorf("mux: no lights resolved")
	}

	master := 1.0
	if doc.Master.Brightness != nil {
		master = clamp01(*doc.Master.Brightness)
	}

	program := &Program{
		Name:   doc.Name,
		Room:   doc.Room,
		Lights: lights,
		Master: master,
		Output: doc.Output,
	}
	if program.Output.Hz <= 0 {
		program.Output.Hz = 4
	}
	if program.Output.Hz > 10 {
		program.Output.Hz = 10
	}
	if program.Output.TransitionMS <= 0 {
		program.Output.TransitionMS = 220
	}

	seen := map[string]bool{}
	known := map[string]bool{}
	for _, l := range lights {
		known[l.ID] = true
	}
	for i, ld := range doc.Lanes {
		if ld.Source.Type == "" {
			return nil, fmt.Errorf("mux: lane %d has no source", i+1)
		}
		if ld.Effect.Type == "" {
			return nil, fmt.Errorf("mux: lane %d has no effect", i+1)
		}
		src, err := NewSource(ld.Source.Type, ld.Source.Params)
		if err != nil {
			return nil, fmt.Errorf("mux: lane %q source: %w", laneLabel(ld, i), err)
		}
		eff, err := NewEffect(ld.Effect.Type, ld.Effect.Params)
		if err != nil {
			return nil, fmt.Errorf("mux: lane %q effect: %w", laneLabel(ld, i), err)
		}
		id := ld.ID
		if id == "" {
			id = fmt.Sprintf("lane-%d", i+1)
		}
		if seen[id] {
			return nil, fmt.Errorf("mux: duplicate lane id %q", id)
		}
		seen[id] = true

		enabled := true
		if ld.Enabled != nil {
			enabled = *ld.Enabled
		}
		level := 1.0
		if ld.Level != nil {
			level = clamp01(*ld.Level)
		}
		blend := Blend(ld.Blend)
		if blend == "" {
			if i == 0 {
				blend = BlendReplace
			} else {
				blend = BlendAdd
			}
		}
		if !validBlend(blend) {
			return nil, fmt.Errorf("mux: lane %q has unknown blend %q", id, ld.Blend)
		}
		for _, t := range ld.Targets {
			if !known[t] {
				return nil, fmt.Errorf("mux: lane %q targets unknown light %q", id, t)
			}
		}
		program.Lanes = append(program.Lanes, Lane{
			ID:      id,
			Name:    laneLabel(ld, i),
			Enabled: enabled,
			Source:  src,
			Effect:  eff,
			Blend:   blend,
			Level:   level,
			Targets: ld.Targets,
		})
	}
	return program, nil
}

func laneLabel(ld LaneDoc, i int) string {
	if ld.Name != "" {
		return ld.Name
	}
	return fmt.Sprintf("Layer %d", i+1)
}

func validBlend(b Blend) bool {
	for _, v := range Blends {
		if b == v {
			return true
		}
	}
	return false
}

// DefaultDocument is the first thing the UI shows: one warm breathing layer
// and one restrained sparkle. It is intentionally quiet rather than a
// demo reel; the point is to make the controls obvious.
func DefaultDocument(lights []Light) Document {
	warm, _ := Hex("#FF7A18")
	white, _ := Hex("#FFFFFF")
	one := 1.0
	enabled := true
	_ = white
	return Document{
		Version: 1,
		Name:    "Salon warm pulse",
		Room:    "Salon",
		Lights:  lightIDs(lights),
		Master:  MasterDoc{Brightness: &one},
		Lanes: []LaneDoc{
			{
				ID: "warm", Name: "Warm pulse", Enabled: &enabled,
				Source: NodeDoc{Type: "timer", Params: map[string]any{"bpm": 48.0, "waveform": "sine"}},
				Effect: NodeDoc{Type: "pulse", Params: map[string]any{
					"color": warm.Hex(), "min": 0.08, "max": 0.85, "curve": "sine",
				}},
				Blend: string(BlendReplace), Level: &one,
			},
			{
				ID: "sparkle", Name: "Sparkle", Enabled: &enabled,
				Source: NodeDoc{Type: "timer", Params: map[string]any{"bpm": 72.0, "waveform": "saw"}},
				Effect: NodeDoc{Type: "sparkle", Params: map[string]any{
					"color": "#FFFFFF", "density": 0.22, "decay": 1.4, "speed": 1.0,
				}},
				Blend: string(BlendAdd), Level: floatPtr(0.45),
			},
		},
		Output: OutputConfig{Hz: 4, TransitionMS: 240},
	}
}

func lightIDs(lights []Light) []string {
	out := make([]string, 0, len(lights))
	for _, l := range lights {
		out = append(out, l.ID)
	}
	sort.Strings(out)
	return out
}

func floatPtr(v float64) *float64 { return &v }

// --- registries ------------------------------------------------------

// ParamSpec describes one editable primitive parameter. The UI builds its
// controls from this, so adding a primitive needs no frontend changes.
type ParamSpec struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // number | color | bool | enum | text
	Default any      `json:"default,omitempty"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	Help    string   `json:"help,omitempty"`
}

// Choice is one enum option.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// NodeMeta is the editor-facing description of a primitive.
type NodeMeta struct {
	Type        string      `json:"type"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Params      []ParamSpec `json:"params"`
}

// SourceSpec is a registered source constructor + metadata.
type SourceSpec struct {
	Meta NodeMeta
	New  func(map[string]any) (Source, error)
}

// EffectSpec is a registered effect constructor + metadata.
type EffectSpec struct {
	Meta NodeMeta
	New  func(map[string]any) (Effect, error)
}

var (
	sources = map[string]SourceSpec{}
	effects = map[string]EffectSpec{}
)

// RegisterSource adds a source to the catalogue. Duplicate registration is a
// programming error, not a user error.
func RegisterSource(spec SourceSpec) {
	if _, dup := sources[spec.Meta.Type]; dup {
		panic("mux: duplicate source " + spec.Meta.Type)
	}
	sources[spec.Meta.Type] = spec
}

// RegisterEffect adds an effect to the catalogue.
func RegisterEffect(spec EffectSpec) {
	if _, dup := effects[spec.Meta.Type]; dup {
		panic("mux: duplicate effect " + spec.Meta.Type)
	}
	effects[spec.Meta.Type] = spec
}

// NewSource instantiates a source by type.
func NewSource(typ string, params map[string]any) (Source, error) {
	spec, ok := sources[typ]
	if !ok {
		return nil, fmt.Errorf("unknown source %q", typ)
	}
	return spec.New(params)
}

// NewEffect instantiates an effect by type.
func NewEffect(typ string, params map[string]any) (Effect, error) {
	spec, ok := effects[typ]
	if !ok {
		return nil, fmt.Errorf("unknown effect %q", typ)
	}
	return spec.New(params)
}

// CatalogData is the complete editor metadata.
type CatalogData struct {
	Sources []NodeMeta `json:"sources"`
	Effects []NodeMeta `json:"effects"`
	Blends  []string   `json:"blends"`
}

// Catalog returns every registered primitive, sorted by type.
func Catalog() CatalogData {
	srcs := make([]NodeMeta, 0, len(sources))
	for _, s := range sources {
		srcs = append(srcs, s.Meta)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].Type < srcs[j].Type })
	effs := make([]NodeMeta, 0, len(effects))
	for _, e := range effects {
		effs = append(effs, e.Meta)
	}
	sort.Slice(effs, func(i, j int) bool { return effs[i].Type < effs[j].Type })
	blends := make([]string, len(Blends))
	for i, b := range Blends {
		blends[i] = string(b)
	}
	return CatalogData{Sources: srcs, Effects: effs, Blends: blends}
}

// --- parameter helpers ----------------------------------------------

func pnum(p map[string]any, key string, def float64) float64 {
	if p == nil {
		return def
	}
	v, ok := p[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, err := n.Float64()
		if err == nil {
			return f
		}
	}
	return def
}

func pstr(p map[string]any, key, def string) string {
	if p == nil {
		return def
	}
	if v, ok := p[key].(string); ok && v != "" {
		return v
	}
	return def
}

func pbool(p map[string]any, key string, def bool) bool {
	if p == nil {
		return def
	}
	if v, ok := p[key].(bool); ok {
		return v
	}
	return def
}

func pcolor(p map[string]any, key, def string) Color {
	c, err := Hex(pstr(p, key, def))
	if err != nil {
		return MustHex(def)
	}
	return c
}
