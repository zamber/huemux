package preset

import (
	"encoding/json"
	"math"
	"time"
)

func init() {
	Register("splitter", func() Primitive { return &splitter{} })
	Register("math", func() Primitive { return &mathOp{} })
	Register("mapper", func() Primitive { return &mapper{} })
	Register("compressor", func() Primitive { return &compressor{} })
	Register("derivative", func() Primitive { return &derivative{} })
}

// splitter fans out one scalar input to N output ports.
type splitter struct {
	n int
}

func (s *splitter) Type() string { return "splitter" }

func (s *splitter) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "splitter", Category: CategoryRouting,
		Label:       "Splitter",
		Description: "Fans out one scalar to N downstream nodes.",
		Inputs:      []Port{{Name: "in", Kind: PortScalar}},
		Outputs:     nil, // dynamic, set during Init
		Params: []ParamSpec{
			{Name: "outputs", Label: "Outputs", Type: "number", Default: 2.0, Min: f64ptr(2), Max: f64ptr(8), Step: 1, Description: "Number of output ports (2–8)."},
		},
	}
}

func (s *splitter) Init(raw json.RawMessage) error {
	p := struct {
		Outputs int `json:"outputs"`
	}{Outputs: 2}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
	}
	if p.Outputs < 2 {
		p.Outputs = 2
	}
	if p.Outputs > 8 {
		p.Outputs = 8
	}
	s.n = p.Outputs
	return nil
}

func (s *splitter) Process(env *Env) {
	v := env.In["in"]
	for i := 1; i <= s.n; i++ {
		env.Out[portN(i)] = v
	}
}

func portN(n int) string {
	switch n {
	case 1:
		return "out_1"
	case 2:
		return "out_2"
	case 3:
		return "out_3"
	case 4:
		return "out_4"
	case 5:
		return "out_5"
	case 6:
		return "out_6"
	case 7:
		return "out_7"
	default:
		return "out_8"
	}
}

// mathOp applies a binary operation to one or two scalar inputs.
type mathOp struct {
	params struct {
		Op  string  `json:"op"`
		Min float64 `json:"min"`
		Max float64 `json:"max"`
	}
}

func (m *mathOp) Type() string { return "math" }

func (m *mathOp) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "math", Category: CategoryModulation,
		Label:       "Math",
		Description: "Binary scalar math: add, multiply, subtract, divide, clamp, invert.",
		Inputs:      []Port{{Name: "a", Kind: PortScalar}, {Name: "b", Kind: PortScalar}},
		Outputs:     []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "op", Label: "Operation", Type: "string", Default: "add", Choices: []string{"add", "mul", "sub", "div", "clamp", "invert"}, Description: "Math operation."},
			{Name: "min", Label: "Clamp Min", Type: "number", Default: 0.0, Step: 0.01, Description: "Minimum output (clamp mode)."},
			{Name: "max", Label: "Clamp Max", Type: "number", Default: 1.0, Step: 0.01, Description: "Maximum output (clamp mode)."},
		},
	}
}

func (m *mathOp) Init(raw json.RawMessage) error {
	m.params.Op = "add"
	m.params.Min = 0
	m.params.Max = 1
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m.params); err != nil {
			return err
		}
	}
	return nil
}

func (m *mathOp) Process(env *Env) {
	a := env.In["a"]
	b := env.In["b"]
	var v float64
	switch m.params.Op {
	case "mul":
		v = a * b
	case "sub":
		v = a - b
	case "div":
		if b != 0 {
			v = a / b
		}
	case "clamp":
		v = clamp(a, m.params.Min, m.params.Max)
	case "invert":
		v = 1 - a
	default: // add
		v = a + b
	}
	env.Out["out"] = v
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mapper remaps a scalar from one range to another with an optional curve.
type mapper struct {
	params struct {
		InMin  float64 `json:"in_min"`
		InMax  float64 `json:"in_max"`
		OutMin float64 `json:"out_min"`
		OutMax float64 `json:"out_max"`
		Curve  string  `json:"curve"`
	}
}

func (m *mapper) Type() string { return "mapper" }

func (m *mapper) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "mapper", Category: CategoryModulation,
		Label:       "Mapper",
		Description: "Remaps a scalar from [in_min, in_max] to [out_min, out_max] with an optional curve.",
		Inputs:      []Port{{Name: "in", Kind: PortScalar}},
		Outputs:     []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "in_min", Label: "In Min", Type: "number", Default: 0.0, Step: 0.01},
			{Name: "in_max", Label: "In Max", Type: "number", Default: 1.0, Step: 0.01},
			{Name: "out_min", Label: "Out Min", Type: "number", Default: 0.0, Step: 0.01},
			{Name: "out_max", Label: "Out Max", Type: "number", Default: 1.0, Step: 0.01},
			{Name: "curve", Label: "Curve", Type: "string", Default: "lin", Choices: []string{"lin", "exp", "log"}, Description: "Mapping curve."},
		},
	}
}

func (m *mapper) Init(raw json.RawMessage) error {
	m.params.InMax = 1
	m.params.OutMax = 1
	m.params.Curve = "lin"
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m.params); err != nil {
			return err
		}
	}
	return nil
}

func (m *mapper) Process(env *Env) {
	v := env.In["in"]
	// Normalise to 0..1.
	inRange := m.params.InMax - m.params.InMin
	if inRange == 0 {
		env.Out["out"] = m.params.OutMin
		return
	}
	t := (v - m.params.InMin) / inRange
	t = clamp01(t)
	// Apply curve.
	switch m.params.Curve {
	case "exp":
		t = t * t
	case "log":
		if t > 0 {
			t = math.Log(1+9*t) / math.Log(10)
		}
	}
	// Scale to output range.
	env.Out["out"] = m.params.OutMin + t*(m.params.OutMax-m.params.OutMin)
}

// compressor is a downward compressor on a scalar time series.
type compressor struct {
	params struct {
		Threshold float64 `json:"threshold"`
		Ratio     float64 `json:"ratio"`
		AttackMS  float64 `json:"attack_ms"`
		ReleaseMS float64 `json:"release_ms"`
	}
	// reserved for stateful envelope follower (future)
}

func (c *compressor) Type() string { return "compressor" }

func (c *compressor) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "compressor", Category: CategoryModulation,
		Label:       "Compressor",
		Description: "Downward compressor on a scalar time series. Reduces signal above threshold by ratio.",
		Inputs:      []Port{{Name: "in", Kind: PortScalar}},
		Outputs:     []Port{{Name: "out", Kind: PortScalar}, {Name: "reduction", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "threshold", Label: "Threshold", Type: "number", Default: 0.5, Min: f64ptr(0), Max: f64ptr(1), Step: 0.01, Description: "Level above which compression applies."},
			{Name: "ratio", Label: "Ratio", Type: "number", Default: 4.0, Min: f64ptr(1), Max: f64ptr(20), Step: 0.5, Description: "Compression ratio. 4 = 4:1."},
			{Name: "attack_ms", Label: "Attack (ms)", Type: "number", Default: 5.0, Step: 1, Description: "Envelope attack time."},
			{Name: "release_ms", Label: "Release (ms)", Type: "number", Default: 100.0, Step: 1, Description: "Envelope release time."},
		},
	}
}

func (c *compressor) Init(raw json.RawMessage) error {
	c.params.Threshold = 0.5
	c.params.Ratio = 4
	c.params.AttackMS = 5
	c.params.ReleaseMS = 100
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c.params); err != nil {
			return err
		}
	}
	if c.params.Threshold < 0 || c.params.Threshold > 1 {
		c.params.Threshold = 0.5
	}
	if c.params.Ratio < 1 {
		c.params.Ratio = 1
	}
	return nil
}

func (c *compressor) Process(env *Env) {
	in := env.In["in"]
	// Envelope follower: peak-detecting one-pole.
	now := env.Now
	// We track the envelope from the last Process call via state.
	// For the MVP we use a simple instantaneous compressor — the scalar
	// is already at 25 Hz, so a full envelope follower adds marginal
	// benefit and the smoother primitive covers the filtering case.
	above := in - c.params.Threshold
	if above <= 0 {
		env.Out["out"] = in
		env.Out["reduction"] = 0
		return
	}
	gainReduction := above * (1 - 1/c.params.Ratio)
	env.Out["out"] = in - gainReduction
	env.Out["reduction"] = gainReduction
	_ = now // reserved for stateful envelope follower in future iteration
}

// derivative computes the rate of change of a scalar (Δ/dt), clamped to ≥0.
// Useful for turning energy into "intensity."
type derivative struct {
	prev float64
	last time.Time
	init bool
}

func (d *derivative) Type() string { return "derivative" }

func (d *derivative) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "derivative", Category: CategoryModulation,
		Label:       "Derivative",
		Description: "Rate of change of a scalar (Δ/dt), clamped to ≥0. Turns energy into intensity.",
		Inputs:      []Port{{Name: "in", Kind: PortScalar}},
		Outputs:     []Port{{Name: "out", Kind: PortScalar}},
		Params:      nil,
	}
}

func (d *derivative) Init(raw json.RawMessage) error { return nil }

func (d *derivative) Process(env *Env) {
	in := env.In["in"]
	if !d.init {
		d.prev = in
		d.last = env.Now
		d.init = true
		env.Out["out"] = 0
		return
	}
	dt := env.Now.Sub(d.last).Seconds()
	d.last = env.Now
	if dt <= 0 {
		dt = 0.04
	}
	delta := (in - d.prev) / dt
	d.prev = in
	if delta < 0 {
		delta = 0
	}
	env.Out["out"] = delta
}
