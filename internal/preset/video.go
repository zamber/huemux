package preset

import (
	"encoding/json"
	"math"

	"github.com/zamber/huemux/internal/pipeline"
)

func init() {
	Register("entertainment_area", func() Primitive { return &entertainmentArea{} })
	Register("hue_shift", func() Primitive { return &hueShift{} })
	Register("color_modulate", func() Primitive { return &colorModulate{} })
}

// entertainmentArea reads per-light colors from the video capture grid and
// writes them to the color bus. No-op when no video source is connected
// (env.HasVideo is false). This is what lets a preset consume screen-sync
// colors and route them through effects.
type entertainmentArea struct {
	params struct {
		LightIDs []string `json:"light_ids"`
	}
}

func (e *entertainmentArea) Type() string { return "entertainment_area" }

func (e *entertainmentArea) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "entertainment_area", Category: CategorySource,
		Label:       "Entertainment Area",
		Description: "Video source: reads per-light colors from the screen-sync grid. No-op when no video capture is active.",
		Inputs:      nil,
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to read from the grid. Empty = all."},
		},
	}
}

func (e *entertainmentArea) Init(raw json.RawMessage) error {
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &e.params); err != nil {
			return err
		}
	}
	return nil
}

func (e *entertainmentArea) Process(env *Env) {
	if !env.HasVideo || len(env.VideoColors) == 0 {
		return
	}
	ids := e.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	for _, rid := range ids {
		if c, ok := env.VideoColors[rid]; ok {
			env.Colors[rid] = Color{R: c.R, G: c.G, B: c.B}
		}
	}
}

// hueShift rotates the hue of each color in the bus by a scalar amount (degrees).
type hueShift struct {
	params struct {
		LightIDs []string `json:"light_ids"`
	}
}

func (h *hueShift) Type() string { return "hue_shift" }

func (h *hueShift) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "hue_shift", Category: CategoryEffect,
		Label:       "Hue Shift",
		Description: "Rotates hue of color bus entries by a scalar input (degrees, 0–360).",
		Inputs:      []Port{{Name: "amount", Kind: PortScalar}},
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to affect. Empty = all."},
		},
	}
}

func (h *hueShift) Init(raw json.RawMessage) error {
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &h.params); err != nil {
			return err
		}
	}
	return nil
}

func (h *hueShift) Process(env *Env) {
	deg := math.Mod(env.In["amount"], 360)
	if deg == 0 {
		return
	}
	ids := h.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	for _, rid := range ids {
		c, ok := env.Colors[rid]
		if !ok || c == (Color{}) {
			continue
		}
		env.Colors[rid] = shiftHue(c, deg)
	}
}

// colorModulate modulates saturation and/or brightness of color bus entries
// with an input scalar (0–1). 0 = no change, 1 = full modulation.
type colorModulate struct {
	params struct {
		LightIDs []string `json:"light_ids"`
		Target   string   `json:"target"` // saturation | brightness | both
	}
}

func (cm *colorModulate) Type() string { return "color_modulate" }

func (cm *colorModulate) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "color_modulate", Category: CategoryEffect,
		Label:       "Color Modulate",
		Description: "Modulates saturation and/or brightness of color bus with a scalar input.",
		Inputs:      []Port{{Name: "amount", Kind: PortScalar}},
		Outputs:     nil,
		Params: []ParamSpec{
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Lights to affect. Empty = all."},
			{Name: "target", Label: "Target", Type: "string", Default: "both", Choices: []string{"saturation", "brightness", "both"}, Description: "What to modulate."},
		},
	}
}

func (cm *colorModulate) Init(raw json.RawMessage) error {
	cm.params.Target = "both"
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cm.params); err != nil {
			return err
		}
	}
	switch cm.params.Target {
	case "saturation", "brightness", "both":
	default:
		cm.params.Target = "both"
	}
	return nil
}

func (cm *colorModulate) Process(env *Env) {
	amount := clamp01(env.In["amount"])
	ids := cm.params.LightIDs
	if len(ids) == 0 {
		ids = env.AllLights
	}
	for _, rid := range ids {
		c, ok := env.Colors[rid]
		if !ok || c == (Color{}) {
			continue
		}
		l := luma(c)
		switch cm.params.Target {
		case "saturation":
			// amount 0 → unchanged, amount 1 → fully desaturated
			sat := 1 - amount
			env.Colors[rid] = Color{
				R: l + sat*(c.R-l),
				G: l + sat*(c.G-l),
				B: l + sat*(c.B-l),
			}
		case "brightness":
			// amount 0 → unchanged, amount 1 → dark
			gain := 1 - amount*0.8 // max 80% dim
			env.Colors[rid] = Color{R: c.R * gain, G: c.G * gain, B: c.B * gain}
		default: // both
			sat := 1 - amount
			gain := 1 - amount*0.8
			env.Colors[rid] = Color{
				R: (l + sat*(c.R-l)) * gain,
				G: (l + sat*(c.G-l)) * gain,
				B: (l + sat*(c.B-l)) * gain,
			}
		}
	}
}

// Ensure pipeline import is used (entertainment_area reads pipeline.LinearColor).
var _ = pipeline.LinearColor{}
