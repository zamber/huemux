package preset

import (
	"encoding/json"
	"math"
	"time"
)

func init() {
	Register("reactivity_effect", func() Primitive { return &reactivityEffect{} })
	Register("brightness_effect", func() Primitive { return &brightnessEffect{} })
	Register("saturation_effect", func() Primitive { return &saturationEffect{} })
}

// reactivityEffect smooths the per-light value bus with an asymmetric one-pole
// EMA, per-light state. reactivity 100 → tau 0 → pass-through. This is the
// graph-level counterpart of the pipeline.Smoother, moved out of the global
// settings so it lives in the preset where it belongs.
type reactivityEffect struct {
	params struct {
		Reactivity float64  `json:"reactivity"`
		LightIDs   []string `json:"light_ids"`
	}
	state map[string]onePole
}

type onePole struct {
	current float64
	last    time.Time
	init    bool
}

func (r *reactivityEffect) Type() string { return "reactivity_effect" }

func (r *reactivityEffect) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "reactivity_effect", Category: CategoryOutputEffect, OutputEffect: true,
		Label:       "Reactivity",
		Description: "Per-light EMA smooth on the value bus. 100 = pass-through, lower = smoother.",
		Inputs:      nil,
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "reactivity", Label: "Reactivity", Type: "number", Default: 100.0, Min: f64ptr(0), Max: f64ptr(100), Step: 1, Description: "100 = pass-through, 0 = maximum smoothing."},
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to affect. Empty = all."},
		},
	}
}

func (r *reactivityEffect) Init(raw json.RawMessage) error {
	r.params.Reactivity = 100
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r.params); err != nil {
			return err
		}
	}
	if r.params.Reactivity < 0 || r.params.Reactivity > 100 {
		r.params.Reactivity = 100
	}
	r.state = map[string]onePole{}
	return nil
}

func (r *reactivityEffect) Process(env *Env) {
	ids := r.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	if r.params.Reactivity >= 100 {
		return // pass-through
	}
	// Same tau mapping as pipeline.Smoother: tau = lerp(0, 1200ms, (100−R)/100).
	tauMs := (100 - r.params.Reactivity) / 100 * 1200
	now := env.Now
	for _, rid := range ids {
		v, ok := env.Lights[rid]
		if !ok {
			continue
		}
		s := r.state[rid]
		if !s.init {
			s.current = v
			s.last = now
			s.init = true
			r.state[rid] = s
			continue
		}
		dt := now.Sub(s.last).Seconds() * 1000
		if dt <= 0 {
			dt = 40
		}
		alpha := 1 - math.Exp(-dt/max(tauMs, 0.01))
		s.current += alpha * (v - s.current)
		s.last = now
		r.state[rid] = s
		env.Lights[rid] = s.current
	}
}

// brightnessEffect multiplies per-light bus values by a static gain.
type brightnessEffect struct {
	params struct {
		Gain     float64  `json:"gain"`
		LightIDs []string `json:"light_ids"`
	}
}

func (b *brightnessEffect) Type() string { return "brightness_effect" }

func (b *brightnessEffect) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "brightness_effect", Category: CategoryOutputEffect, OutputEffect: true,
		Label:       "Brightness",
		Description: "Multiplies per-light value bus by gain. 1 = unchanged.",
		Inputs:      nil,
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "gain", Label: "Gain", Type: "number", Default: 1.0, Min: f64ptr(0), Max: f64ptr(2), Step: 0.01, Description: "Brightness multiplier. 1 = unchanged."},
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to affect. Empty = all."},
		},
	}
}

func (b *brightnessEffect) Init(raw json.RawMessage) error {
	b.params.Gain = 1
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &b.params); err != nil {
			return err
		}
	}
	if b.params.Gain < 0 {
		b.params.Gain = 0
	}
	if b.params.Gain > 2 {
		b.params.Gain = 2
	}
	return nil
}

func (b *brightnessEffect) Process(env *Env) {
	g := b.params.Gain
	if g == 1 {
		return
	}
	ids := b.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	for _, rid := range ids {
		if v, ok := env.Lights[rid]; ok {
			env.Lights[rid] = v * g
		}
	}
}

// saturationEffect pushes per-light colors toward or away from their luma gray
// point. 100 = unchanged, 0 = grayscale, 200 = double saturation. Same formula
// as pipeline.Process: c' = luma + (sat/100)*(c − luma).
type saturationEffect struct {
	params struct {
		Saturation float64  `json:"saturation"`
		LightIDs   []string `json:"light_ids"`
	}
}

func (s *saturationEffect) Type() string { return "saturation_effect" }

func (s *saturationEffect) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "saturation_effect", Category: CategoryOutputEffect, OutputEffect: true,
		Label:       "Saturation",
		Description: "Pushes colors toward or away from gray. 100 = unchanged, 0 = grayscale.",
		Inputs:      nil,
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "saturation", Label: "Saturation", Type: "number", Default: 100.0, Min: f64ptr(0), Max: f64ptr(200), Step: 1, Description: "100 = unchanged, 0 = grayscale, 200 = double saturation."},
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to affect. Empty = all."},
		},
	}
}

func (s *saturationEffect) Init(raw json.RawMessage) error {
	s.params.Saturation = 100
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.params); err != nil {
			return err
		}
	}
	if s.params.Saturation < 0 || s.params.Saturation > 200 {
		s.params.Saturation = 100
	}
	return nil
}

func (s *saturationEffect) Process(env *Env) {
	sat := s.params.Saturation
	if sat == 100 {
		return
	}
	ids := s.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	factor := sat / 100
	for _, rid := range ids {
		c, ok := env.Colors[rid]
		if !ok || c == (Color{}) {
			continue
		}
		l := luma(c)
		env.Colors[rid] = Color{
			R: clamp01(l + factor*(c.R-l)),
			G: clamp01(l + factor*(c.G-l)),
			B: clamp01(l + factor*(c.B-l)),
		}
	}
}

// luma returns the perceived brightness of a linear color (Rec. 709 coefficients).
func luma(c Color) float64 { return 0.2126*c.R + 0.7152*c.G + 0.0722*c.B }
