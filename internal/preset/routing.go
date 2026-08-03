package preset

import (
	"encoding/json"
	"sort"
)

func init() {
	Register("all_lights", func() Primitive { return &allLights{} })
	Register("light_group", func() Primitive { return &lightGroup{} })
	Register("threshold_gate", func() Primitive { return &thresholdGate{} })
}

// applyToLights writes the incoming driving value and, when color ports are
// wired, the incoming color to every light in ids. This is the shared body
// of the terminal routing nodes: the effect chain's scalar output (brightness
// 0..1, or color components) meets the light set here.
//
// Each bus is written only when its port is wired, so two terminals can
// drive the same lights without clobbering each other: a brightness chain
// owns the value bus, a color chain owns the color bus, and the runner
// multiplies them. A terminal with no inputs at all selects nothing.
func applyToLights(env *Env, ids []string) {
	in := 1.0
	hasIn := false
	if v, ok := env.In["in"]; ok {
		in = v
		hasIn = true
	}
	var c Color
	hasColor := false
	if _, ok := env.In["r"]; ok {
		hasColor = true
		c = Color{R: env.In["r"], G: env.In["g"], B: env.In["b"]}
	}
	for _, rid := range ids {
		if hasIn {
			env.Lights[rid] = in
		}
		if hasColor {
			env.Colors[rid] = c
		}
	}
	if hasIn {
		env.Out["out"] = in
	}
}

// allLights selects every light in the current area.
type allLights struct{}

func (a *allLights) Type() string               { return "all_lights" }
func (a *allLights) Init(json.RawMessage) error { return nil }
func (a *allLights) Process(env *Env)           { applyToLights(env, env.AllLights) }

// lightGroup selects the configured lights. An empty light_ids list selects
// every light — shipped presets cannot know a bridge's RIDs, and "the same
// lights the last preset used" would be surprising state, so empty means all.
type lightGroup struct {
	ids []string
}

func (l *lightGroup) Type() string { return "light_group" }

func (l *lightGroup) Init(raw json.RawMessage) error {
	p := struct {
		LightIDs []string `json:"light_ids"`
	}{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
	}
	l.ids = append([]string(nil), p.LightIDs...)
	sort.Strings(l.ids)
	return nil
}

func (l *lightGroup) Process(env *Env) {
	if len(l.ids) == 0 {
		applyToLights(env, env.AllLights)
		return
	}
	applyToLights(env, l.ids)
}

// thresholdGate is a window comparator with hysteresis on a scalar: "out"
// passes "value" through while value is inside [min, max], and 0 outside.
// Once open, the window widens by hysteresis before closing again, so a
// signal hovering at the boundary does not flutter. Used to make a strobe
// only fire above a loudness floor, a chase only advance in a mid-range
// energy band, etc. (The plan lists it under routing; gating the value a
// routing node applies produces the same "empty light set" behavior, since
// a 0 driving value paints nothing.)
type thresholdGate struct {
	params struct {
		Min        float64 `json:"min"`
		Max        float64 `json:"max"`
		Hysteresis float64 `json:"hysteresis"`
	}
	open bool
}

func (t *thresholdGate) Type() string { return "threshold_gate" }

func (t *thresholdGate) Init(raw json.RawMessage) error {
	t.params.Min = 0
	t.params.Max = 1
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &t.params); err != nil {
			return err
		}
	}
	if t.params.Hysteresis < 0 {
		t.params.Hysteresis = 0
	}
	return nil
}

func (t *thresholdGate) Process(env *Env) {
	v := env.In["value"]
	if t.open {
		if v < t.params.Min-t.params.Hysteresis || v > t.params.Max+t.params.Hysteresis {
			t.open = false
		}
	} else if v >= t.params.Min && v <= t.params.Max {
		t.open = true
	}
	if !t.open {
		v = 0
	}
	env.Out["out"] = v
}
