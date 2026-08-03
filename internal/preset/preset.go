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

// Primitive is one node in the preset graph.
type Primitive interface {
	// Type returns the catalog name, e.g. "beat_detector".
	Type() string
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

	type inRef struct {
		to, inPort string
	}
	fromCount := map[string]int{} // edges out of each node, for cycle detection
	for _, re := range raw.Edges {
		if _, ok := p.byID[re.From]; !ok {
			return nil, fmt.Errorf("preset %q: edge from unknown node %q", raw.Name, re.From)
		}
		to, ok := p.byID[re.To]
		if !ok {
			return nil, fmt.Errorf("preset %q: edge to unknown node %q", raw.Name, re.To)
		}
		fromCount[re.From]++
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
	positions map[string]Pos3

	env     Env
	all     []string
	scalars map[string]map[string]float64 // node id → port → value
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
	return r
}

// Name returns the preset's display name.
func (r *Runner) Name() string { return r.p.Name }

// Step runs the graph once and returns the per-channel linear colors. The
// latest audio frame is sampled at the start of the tick (DP-8: effects
// compute at the output rate; analysis is downsampled to it).
func (r *Runner) Step(now time.Time) map[uint8]pipeline.LinearColor {
	r.env.Now = now
	r.env.Lights = map[string]float64{}
	r.env.Colors = map[string]Color{}
	if r.frameFn != nil {
		r.env.Frame, r.env.HasFFT = r.frameFn()
	} else {
		r.env.HasFFT = false
	}

	for _, n := range r.p.nodes {
		clear(r.env.In)
		for _, e := range n.ins {
			// Missing upstream ports read as 0: a node that produced no
			// value this tick (no beat yet, silence, …) must drive its
			// downstream as quiet, not as its last value.
			r.env.In[e.inPort] = r.scalars[e.from][e.fromPort]
		}
		clear(r.env.Out)
		n.prim.Process(&r.env)
		// Copy the primitive's outputs into the tick's value store. The
		// copy is what makes the "0 when a node produced nothing" rule
		// work across ticks.
		if r.scalars[n.id] == nil {
			r.scalars[n.id] = map[string]float64{}
		} else {
			clear(r.scalars[n.id])
		}
		for port, v := range r.env.Out {
			r.scalars[n.id][port] = v
		}
	}

	out := make(map[uint8]pipeline.LinearColor, len(r.all))
	for _, rid := range r.all {
		value := r.env.Lights[rid]
		if value == 0 && r.env.Colors[rid] == (Color{}) {
			// Unselected light: never painted. Letting it fall through as
			// black would strobe off-lights between routing changes.
			continue
		}
		if value == 0 {
			value = 1 // colored but not value-driven
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
			continue // light not in this area's channels
		}
		out[ch] = pipeline.LinearColor{R: c.R * value, G: c.G * value, B: c.B * value}
	}
	return out
}
