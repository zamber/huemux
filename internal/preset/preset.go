// Package preset is the music-reactivity preset engine: the primitive
// catalog, the graph runner, and the primitives themselves. A preset is a
// JSON DAG of catalog entries (docs/MUSIC-REACTIVITY.md, "Preset format");
// the runner ticks the graph once per output clock tick, sampling the
// latest audio analysis frame, and produces per-channel linear colors that
// feed the same smoother/pipeline/stream path screen sync uses.
//
// The plan's Primitive interface (docs/MUSIC-REACTIVITY.md §"Primitive
// interface (Go)") maps scalars through name-port maps. Analysis and
// modulation primitives are exactly that. Routing and effect primitives
// additionally need the per-light bus, so Process takes an Env holding both:
// In/Out are the named scalar ports, Lights/Colors are the per-light value
// and color buses that terminal routing nodes write and the runner combines.
package preset

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/zamber/huemux/internal/music"
	"github.com/zamber/huemux/internal/pipeline"
)

// Color is a linear RGB color in 0..1, the internal currency of the effect
// layer (screen sync averages linear samples for the same reason: averaging
// gamma-encoded values muddies output).
type Color struct{ R, G, B float64 }

// Env is the per-tick state a primitive reads and writes. One Env is
// reused across all nodes in a tick; maps are wiped per node, not recreated.
type Env struct {
	// Audio: the latest analysis frame from the browser. Zero and HasFFT
	// false while no source is connected.
	Frame  music.Frame
	HasFFT bool

	// Video: per-light linear colors from the most recent grid sample,
	// populated by the engine when a frame source is active. HasVideo
	// is false when no grid frame is available (no capture, no source).
	VideoColors map[string]pipeline.LinearColor
	HasVideo    bool

	// AllLights is the sorted list of light RIDs known to the current
	// area (from the zone map at activation). Routing primitives use it.
	AllLights []string

	// Pos holds each light's physical room position (from the area's
	// channels), for spatial primitives like pulse_energy.
	Pos map[string]Pos3

	// In carries the values of this node's incoming edges by port name
	// (default "in" when the edge names no port).
	In map[string]float64
	// Out receives this node's scalar outputs by port name.
	Out map[string]float64

	// Lights is the per-light driving value bus (0..1, brightness). Written
	// by terminal routing nodes, read by the runner when assembling colors.
	Lights map[string]float64
	// Colors is the per-light base color bus. Written by routing nodes
	// receiving color ports; lights with a value but no color are white.
	Colors map[string]Color

	// Now is the wall-clock time of this tick, for time-based modulation.
	Now time.Time
}

// PrimitiveMeta is the editor-facing metadata for a primitive: ports,
// parameters, and category for the node palette.
type PrimitiveMeta struct {
	Type         string      `json:"type"`
	Category     Category    `json:"category"`
	Label        string      `json:"label"`
	Description  string      `json:"description"`
	Inputs       []Port      `json:"inputs"`
	Outputs      []Port      `json:"outputs"`
	Params       []ParamSpec `json:"params"`
	OutputEffect bool        `json:"output_effect,omitempty"`
}

// Category groups primitives for the editor palette.
type Category string

const (
	CategorySource       Category = "source"
	CategoryAnalysis     Category = "analysis"
	CategoryRouting      Category = "routing"
	CategoryEffect       Category = "effect"
	CategoryModulation   Category = "modulation"
	CategoryOutputEffect Category = "output_effect"
)

// PortKind describes what flows through a port.
type PortKind string

const (
	PortScalar  PortKind = "scalar"
	PortTrigger PortKind = "trigger"
	PortColor   PortKind = "color"
)

// Port is one named input or output on a primitive.
type Port struct {
	Name string   `json:"name"`
	Kind PortKind `json:"kind"`
}

// ParamSpec describes one editable parameter for the editor inspector.
type ParamSpec struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // number | string | bool | color | light_ids
	Default     any      `json:"default"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Step        float64  `json:"step,omitempty"`
	Choices     []string `json:"choices,omitempty"`
	Description string   `json:"description,omitempty"`
}

// NodeSnapshot is a per-node state capture from one tick, streamed to the
// editor at debug_hz for live node inspection.
type NodeSnapshot struct {
	ID     string             `json:"id"`
	Type   string             `json:"type"`
	Out    map[string]float64 `json:"out,omitempty"`
	Lights map[string]float64 `json:"lights,omitempty"`
	Colors []sRGBColor        `json:"colors,omitempty"`
}

// sRGBColor is a display-ready 8-bit color for the streaming path.
type sRGBColor struct {
	RID string `json:"rid"`
	R   uint8  `json:"r"`
	G   uint8  `json:"g"`
	B   uint8  `json:"b"`
}

// Primitive is one node in the preset graph.
type Primitive interface {
	// Type returns the catalog name, e.g. "beat_detector".
	Type() string
	// Meta returns the editor-facing metadata: ports, parameters, category.
	Meta() PrimitiveMeta
	// Init applies the node's params from the preset JSON.
	Init(params json.RawMessage) error
	// Process runs one tick. Upstream scalar values are in env.In; the
	// primitive writes outputs to env.Out and, for routing/effect nodes,
	// to env.Lights / env.Colors.
	Process(env *Env)
}

// Registry ------------------------------------------------------------

var (
	registryMu sync.RWMutex
	registry   = map[string]func() Primitive{}
)

// Register adds a primitive constructor to the catalog. Called from init()
// in the primitive files.
func Register(name string, newFn func() Primitive) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("preset: duplicate primitive registration: " + name)
	}
	registry[name] = newFn
}

// Catalog returns the registered primitive names, sorted.
func Catalog() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// CatalogMeta returns editor metadata for every registered primitive, sorted
// by category then type.
func CatalogMeta() []PrimitiveMeta {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]PrimitiveMeta, 0, len(registry))
	for _, newFn := range registry {
		out = append(out, newFn().Meta())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// New instantiates a registered primitive by name.
func New(name string) (Primitive, error) {
	registryMu.RLock()
	newFn, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown primitive type %q", name)
	}
	return newFn(), nil
}

// Graph ---------------------------------------------------------------

// edge wires one upstream port to one downstream port. A missing fromPort
// means "out"; a missing inPort means "in".
type edge struct {
	from, fromPort, inPort string
}

// node is one instantiated graph node plus its incoming scalar wiring.
type node struct {
	id   string
	prim Primitive
	ins  []edge
}

// Preset is a parsed, validated preset graph.
type Preset struct {
	Name        string
	Description string
	nodes       []*node
	byID        map[string]*node
	// sources are mic_capture-type node ids, whose edges carry no scalar
	// (the frame is injected into every Env instead).
	sources map[string]bool
}

// RawPreset is the on-disk JSON shape, per docs/MUSIC-REACTIVITY.md.
type rawPreset struct {
	Version     int       `json:"version"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Nodes       []rawNode `json:"nodes"`
	Edges       []rawEdge `json:"edges"`
}

type rawNode struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Params json.RawMessage `json:"params"`
}

type rawEdge struct {
	From    string `json:"from"`
	OutPort string `json:"out_port"`
	To      string `json:"to"`
	InPort  string `json:"in_port"`
}

// Parse decodes and validates a preset document, instantiating every
// primitive. Cycles are rejected; unknown node ids on edges are rejected.
func Parse(data []byte) (*Preset, error) {
	var raw rawPreset
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse preset: %w", err)
	}
	if raw.Version != 1 {
		return nil, fmt.Errorf("unsupported preset version %d", raw.Version)
	}
	if raw.Name == "" {
		return nil, fmt.Errorf("preset has no name")
	}
	if len(raw.Nodes) == 0 {
		return nil, fmt.Errorf("preset %q has no nodes", raw.Name)
	}

	p := &Preset{
		Name:        raw.Name,
		Description: raw.Description,
		byID:        map[string]*node{},
		sources:     map[string]bool{},
	}
	for _, rn := range raw.Nodes {
		prim, err := New(rn.Type)
		if err != nil {
			return nil, fmt.Errorf("preset %q node %q: %w", raw.Name, rn.ID, err)
		}
		if err := prim.Init(rn.Params); err != nil {
			return nil, fmt.Errorf("preset %q node %q (%s): %w", raw.Name, rn.ID, rn.Type, err)
		}
		n := &node{id: rn.ID, prim: prim}
		if _, dup := p.byID[rn.ID]; dup {
			return nil, fmt.Errorf("preset %q has duplicate node id %q", raw.Name, rn.ID)
		}
		p.byID[rn.ID] = n
		p.nodes = append(p.nodes, n)
		if prim.Type() == "mic_capture" {
			p.sources[rn.ID] = true
		}
	}

	for _, re := range raw.Edges {
		if _, ok := p.byID[re.From]; !ok {
			return nil, fmt.Errorf("preset %q: edge from unknown node %q", raw.Name, re.From)
		}
		to, ok := p.byID[re.To]
		if !ok {
			return nil, fmt.Errorf("preset %q: edge to unknown node %q", raw.Name, re.To)
		}
		// Edges out of a source node carry no scalar; the audio frame is
		// injected into every Env directly.
		if p.sources[re.From] {
			continue
		}
		inPort := re.InPort
		if inPort == "" {
			inPort = "in"
		}
		outPort := re.OutPort
		if outPort == "" {
			outPort = "out"
		}
		to.ins = append(to.ins, edge{from: re.From, fromPort: outPort, inPort: inPort})
	}

	// Topological sort (Kahn). The graph is acyclic by design (Phase 3 adds
	// user-facing validation); a cycle here is a preset bug and must fail
	// loudly at load, not silently stall the lights on a dead node.
	dependents := map[string][]*node{}
	for _, n := range p.nodes {
		for _, e := range n.ins {
			dependents[e.from] = append(dependents[e.from], n)
		}
	}
	remaining := map[string]int{}
	for _, n := range p.nodes {
		remaining[n.id] = len(n.ins)
	}
	var queue []*node
	for _, n := range p.nodes {
		if remaining[n.id] == 0 {
			queue = append(queue, n)
		}
	}
	order := make([]*node, 0, len(p.nodes))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		order = append(order, cur)
		for _, dep := range dependents[cur.id] {
			remaining[dep.id]--
			if remaining[dep.id] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if len(order) != len(p.nodes) {
		return nil, fmt.Errorf("preset %q has a cycle", raw.Name)
	}
	p.nodes = order
	return p, nil
}

// Runner ticks a parsed preset graph at the output rate. Safe for one
// concurrent writer (the engine's output loop) plus one SetLightTargets
// before starting; the engine swaps the whole Runner under its own lock.
type Runner struct {
	p         *Preset
	channel   map[string]uint8 // light RID → area channel id
	frameFn   func() (music.Frame, bool)
	videoFn   func() (map[string]pipeline.LinearColor, bool)
	positions map[string]Pos3

	env     Env
	all     []string
	scalars map[string]map[string]float64 // node id → port → value

	snapMu    sync.Mutex
	snaps     []NodeSnapshot
	hasOutput bool // true when the preset contains output-effect nodes
}

// NewRunner builds a Runner for an area. channels maps light RIDs to area
// channel ids (derived from the engine's zones); positions maps light RIDs
// to their physical room position (from the area's channel layout) and may
// be nil; frameFn returns the latest audio analysis frame and may be nil.
func NewRunner(p *Preset, channels map[string]uint8, positions map[string]Pos3, frameFn func() (music.Frame, bool)) *Runner {
	r := &Runner{
		p:         p,
		channel:   channels,
		positions: positions,
		frameFn:   frameFn,
		env: Env{
			In:     map[string]float64{},
			Out:    map[string]float64{},
			Lights: map[string]float64{},
			Colors: map[string]Color{},
		},
		scalars: map[string]map[string]float64{},
	}
	for rid := range channels {
		r.all = append(r.all, rid)
	}
	sort.Strings(r.all)
	r.env.AllLights = r.all
	r.env.Pos = positions
	for _, n := range p.nodes {
		if n.prim.Meta().OutputEffect {
			r.hasOutput = true
			break
		}
	}
	return r
}

// SetVideoFrameFn sets the function that supplies per-light video-sampled
// colors to the entertainment_area source node. Pass nil to clear.
func (r *Runner) SetVideoFrameFn(fn func() (map[string]pipeline.LinearColor, bool)) {
	r.videoFn = fn
}

// Name returns the preset's display name.
func (r *Runner) Name() string { return r.p.Name }

// Step runs the graph once and returns the per-channel linear colors. The
// latest audio frame is sampled at the start of the tick (DP-8: effects
// compute at the output rate; analysis is downsampled to it).
// Video colors are sampled before the main loop and injected into env.
// Output-effect nodes (OutputEffect: true) run in a second pass after
// routing nodes have written the per-light buses.
func (r *Runner) Step(now time.Time) map[uint8]pipeline.LinearColor {
	r.env.Now = now
	r.env.Lights = map[string]float64{}
	r.env.Colors = map[string]Color{}
	if r.frameFn != nil {
		r.env.Frame, r.env.HasFFT = r.frameFn()
	} else {
		r.env.HasFFT = false
	}
	if r.videoFn != nil {
		r.env.VideoColors, r.env.HasVideo = r.videoFn()
	} else {
		r.env.HasVideo = false
	}

	var snaps []NodeSnapshot

	// Main pass: skip output-effect nodes; they run after routing.
	for _, n := range r.p.nodes {
		if n.prim.Meta().OutputEffect {
			continue
		}
		beforeLights, beforeColors := snapshotBuses(r.env.Lights, r.env.Colors)
		clear(r.env.In)
		for _, e := range n.ins {
			r.env.In[e.inPort] = r.scalars[e.from][e.fromPort]
		}
		clear(r.env.Out)
		n.prim.Process(&r.env)
		if r.scalars[n.id] == nil {
			r.scalars[n.id] = map[string]float64{}
		} else {
			clear(r.scalars[n.id])
		}
		for port, v := range r.env.Out {
			r.scalars[n.id][port] = v
		}
		snap := diffSnapshot(n.id, n.prim.Type(), r.env.Out, beforeLights, beforeColors, r.env.Lights, r.env.Colors)
		if snap != nil {
			snaps = append(snaps, *snap)
		}
	}

	// Output-effect post-pass: run effect nodes in topo order, mutating
	// the per-light buses that routing nodes wrote.
	if r.hasOutput {
		for _, n := range r.p.nodes {
			if !n.prim.Meta().OutputEffect {
				continue
			}
			beforeLights, beforeColors := snapshotBuses(r.env.Lights, r.env.Colors)
			clear(r.env.In)
			for _, e := range n.ins {
				r.env.In[e.inPort] = r.scalars[e.from][e.fromPort]
			}
			clear(r.env.Out)
			n.prim.Process(&r.env)
			if r.scalars[n.id] == nil {
				r.scalars[n.id] = map[string]float64{}
			} else {
				clear(r.scalars[n.id])
			}
			for port, v := range r.env.Out {
				r.scalars[n.id][port] = v
			}
			snap := diffSnapshot(n.id, n.prim.Type(), r.env.Out, beforeLights, beforeColors, r.env.Lights, r.env.Colors)
			if snap != nil {
				snaps = append(snaps, *snap)
			}
		}
	}

	r.snapMu.Lock()
	r.snaps = snaps
	r.snapMu.Unlock()

	out := make(map[uint8]pipeline.LinearColor, len(r.all))
	for _, rid := range r.all {
		value := r.env.Lights[rid]
		if value == 0 && r.env.Colors[rid] == (Color{}) {
			continue
		}
		if value == 0 {
			value = 1
		}
		if value < 0 {
			value = 0
		} else if value > 1 {
			value = 1
		}
		c := r.env.Colors[rid]
		if c == (Color{}) {
			c = Color{R: 1, G: 1, B: 1}
		}
		ch, ok := r.channel[rid]
		if !ok {
			continue
		}
		out[ch] = pipeline.LinearColor{R: c.R * value, G: c.G * value, B: c.B * value}
	}
	return out
}

// NodeSnapshots returns the per-node state from the most recent tick.
func (r *Runner) NodeSnapshots() []NodeSnapshot {
	r.snapMu.Lock()
	defer r.snapMu.Unlock()
	out := make([]NodeSnapshot, len(r.snaps))
	copy(out, r.snaps)
	return out
}

// snapshotBuses copies the Lights and Colors buses for before/after diffing.
func snapshotBuses(lights map[string]float64, colors map[string]Color) (map[string]float64, map[string]Color) {
	lc := make(map[string]float64, len(lights))
	for k, v := range lights {
		lc[k] = v
	}
	cc := make(map[string]Color, len(colors))
	for k, v := range colors {
		cc[k] = v
	}
	return lc, cc
}

// diffSnapshot returns a NodeSnapshot recording what this node changed, or
// nil when nothing changed (sparse streaming).
func diffSnapshot(id, typ string, out map[string]float64, beforeLights map[string]float64, beforeColors map[string]Color, afterLights map[string]float64, afterColors map[string]Color) *NodeSnapshot {
	var changed bool
	s := NodeSnapshot{ID: id, Type: typ}

	if len(out) > 0 {
		s.Out = make(map[string]float64, len(out))
		for k, v := range out {
			s.Out[k] = v
		}
		changed = true
	}

	for rid, v := range afterLights {
		if beforeLights[rid] != v {
			if s.Lights == nil {
				s.Lights = map[string]float64{}
			}
			s.Lights[rid] = v
			changed = true
		}
	}

	for rid, c := range afterColors {
		if beforeColors[rid] != c {
			s.Colors = append(s.Colors, sRGBColor{RID: rid, R: linearTo8(c.R), G: linearTo8(c.G), B: linearTo8(c.B)})
			changed = true
		}
	}

	if !changed {
		return nil
	}
	return &s
}

// f64ptr returns a pointer to v, for ParamSpec Min/Max fields.
func f64ptr(v float64) *float64 { return &v }

// linearTo8 converts a linear 0..1 value to an 8-bit sRGB byte (simple gamma).
func linearTo8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	// Approximate sRGB gamma: linear → ~2.2 gamma
	g := v
	if g <= 0.0031308 {
		g *= 12.92
	} else {
		g = 1.055*math.Pow(g, 1.0/2.4) - 0.055
	}
	if g < 0 {
		g = 0
	}
	if g > 1 {
		g = 1
	}
	return uint8(g*255 + 0.5)
}
