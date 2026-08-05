package preset

import (
	"encoding/json"
	"math"
	"time"
)

func init() {
	Register("brightness_energy", func() Primitive { return &brightnessEnergy{} })
	Register("color_map_energy", func() Primitive { return &colorMapEnergy{} })
	Register("color_map_frequency", func() Primitive { return &colorMapFrequency{} })
	Register("strobe_beat", func() Primitive { return &strobeBeat{} })
	Register("chase_trigger", func() Primitive { return &chaseTrigger{} })
	Register("pulse_energy", func() Primitive { return &pulseEnergy{} })
}

// brightnessEnergy maps energy (0..1) to a brightness value (0..1) through a
// curve, clipped to [min, max]. The plan's "per light" wording is achieved
// by feeding the output into a terminal routing node, which applies it to
// its light set.
type brightnessEnergy struct {
	params struct {
		Curve string  `json:"curve"` // lin | log | exp
		Min   float64 `json:"min"`
		Max   float64 `json:"max"`
	}
}

func (b *brightnessEnergy) Type() string { return "brightness_energy" }

func (b *brightnessEnergy) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "brightness_energy", Category: CategoryEffect,
		Label: "Brightness (Energy)", Description: "Maps an energy scalar to a brightness value through a curve.",
		Inputs:  []Port{{Name: "energy", Kind: PortScalar}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "curve", Label: "Curve", Type: "string", Default: "lin", Choices: []string{"lin", "log", "exp"}, Description: "Mapping curve."},
			{Name: "min", Label: "Min", Type: "number", Default: 0.0, Step: 0.01, Description: "Minimum output brightness."},
			{Name: "max", Label: "Max", Type: "number", Default: 1.0, Step: 0.01, Description: "Maximum output brightness."},
		},
	}
}

func (b *brightnessEnergy) Init(raw json.RawMessage) error {
	b.params.Curve = "lin"
	b.params.Min = 0
	b.params.Max = 1
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &b.params); err != nil {
			return err
		}
	}
	switch b.params.Curve {
	case "lin", "log", "exp":
	default:
		b.params.Curve = "lin"
	}
	if b.params.Min > b.params.Max {
		b.params.Min, b.params.Max = b.params.Max, b.params.Min
	}
	return nil
}

func (b *brightnessEnergy) Process(env *Env) {
	e := clamp01(env.In["energy"])
	var mapped float64
	switch b.params.Curve {
	case "log":
		// log(1+9e)/log(10): a steep rise out of silence, gentle at the top.
		mapped = math.Log(1+9*e) / math.Log(10)
	case "exp":
		mapped = e * e // quiet stays quiet, loud gets brighter fast
	default:
		mapped = e
	}
	env.Out["out"] = b.params.Min + (b.params.Max-b.params.Min)*mapped
}

// colorMapEnergy maps energy across a palette. The palette is interpolated
// piecewise-linearly in linear space, so neighbouring colors blend naturally.
type colorMapEnergy struct {
	palette []Color
}

func (c *colorMapEnergy) Type() string { return "color_map_energy" }

func (c *colorMapEnergy) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "color_map_energy", Category: CategoryEffect,
		Label: "Color Map (Energy)", Description: "Maps energy across a palette with piecewise-linear interpolation.",
		Inputs:  []Port{{Name: "energy", Kind: PortScalar}},
		Outputs: []Port{{Name: "r", Kind: PortScalar}, {Name: "g", Kind: PortScalar}, {Name: "b", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "palette", Label: "Palette", Type: "string", Default: []string{"#FFFFFF"}, Description: "List of hex colors to interpolate."},
			{Name: "hue_shift", Label: "Hue Shift (°)", Type: "number", Default: 0.0, Step: 1, Description: "Rotates every color's hue by this many degrees."},
		},
	}
}

func (c *colorMapEnergy) Init(raw json.RawMessage) error {
	p := struct {
		Palette  []string `json:"palette"`
		HueShift float64  `json:"hue_shift"` // degrees, applied to every color
	}{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
	}
	if len(p.Palette) == 0 {
		c.palette = []Color{{R: 1, G: 1, B: 1}}
		return nil
	}
	c.palette = make([]Color, 0, len(p.Palette))
	for _, hex := range p.Palette {
		col, err := hexToColor(hex)
		if err != nil {
			return err
		}
		if p.HueShift != 0 {
			col = shiftHue(col, p.HueShift)
		}
		c.palette = append(c.palette, col)
	}
	return nil
}

// shiftHue rotates a linear color's hue by deg without touching its
// perceived lightness (a naive rotation in RGB space changes brightness).
func shiftHue(c Color, deg float64) Color {
	// To HSL, shift, back.
	maxV, minV := c.R, c.R
	for _, v := range []float64{c.G, c.B} {
		if v > maxV {
			maxV = v
		}
		if v < minV {
			minV = v
		}
	}
	l := (maxV + minV) / 2
	d := maxV - minV
	if d == 0 {
		return c // grey: hue is meaningless, shift changes nothing
	}
	var h float64
	switch maxV {
	case c.R:
		h = math.Mod((c.G-c.B)/d, 6)
	case c.G:
		h = (c.B-c.R)/d + 2
	default:
		h = (c.R-c.G)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	h = math.Mod(h+deg, 360)

	s := 0.0
	if l > 0 && l < 1 {
		s = d / (1 - math.Abs(2*l-1))
	}
	cc := (1 - math.Abs(2*l-1)) * s
	x := cc * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - cc/2
	var r, g, bl float64
	switch {
	case h < 60:
		r, g, bl = cc, x, 0
	case h < 120:
		r, g, bl = x, cc, 0
	case h < 180:
		r, g, bl = 0, cc, x
	case h < 240:
		r, g, bl = 0, x, cc
	case h < 300:
		r, g, bl = x, 0, cc
	default:
		r, g, bl = cc, 0, x
	}
	return Color{R: r + m, G: g + m, B: bl + m}
}

func (c *colorMapEnergy) Process(env *Env) {
	e := clamp01(env.In["energy"])
	pos := e * float64(len(c.palette)-1)
	i := int(pos)
	t := pos - float64(i)
	if i >= len(c.palette)-1 {
		col := c.palette[len(c.palette)-1]
		env.Out["r"], env.Out["g"], env.Out["b"] = col.R, col.G, col.B
		return
	}
	col := lerpColor(c.palette[i], c.palette[i+1], t)
	env.Out["r"], env.Out["g"], env.Out["b"] = col.R, col.G, col.B
}

// colorMapFrequency mixes three fixed colors by the band energies: the
// result is bass×bassColor + mid×midColor + treble×trebleColor, so loud bass
// paints its color and a full-spectrum track blends them.
type colorMapFrequency struct {
	bass, mid, treble Color
}

func (c *colorMapFrequency) Type() string { return "color_map_frequency" }

func (c *colorMapFrequency) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "color_map_frequency", Category: CategoryEffect,
		Label: "Color Map (Frequency)", Description: "Mixes three colors by bass/mid/treble band energies.",
		Inputs:  []Port{{Name: "bass", Kind: PortScalar}, {Name: "mid", Kind: PortScalar}, {Name: "treble", Kind: PortScalar}},
		Outputs: []Port{{Name: "r", Kind: PortScalar}, {Name: "g", Kind: PortScalar}, {Name: "b", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "bass_color", Label: "Bass Color", Type: "color", Default: "#FFFFFF", Description: "Hex color for bass frequencies."},
			{Name: "mid_color", Label: "Mid Color", Type: "color", Default: "#FFFFFF", Description: "Hex color for mid frequencies."},
			{Name: "treble_color", Label: "Treble Color", Type: "color", Default: "#FFFFFF", Description: "Hex color for treble frequencies."},
		},
	}
}

func (c *colorMapFrequency) Init(raw json.RawMessage) error {
	p := struct {
		Bass   string `json:"bass_color"`
		Mid    string `json:"mid_color"`
		Treble string `json:"treble_color"`
	}{Bass: "#FFFFFF", Mid: "#FFFFFF", Treble: "#FFFFFF"}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
	}
	var err error
	if c.bass, err = hexToColor(p.Bass); err != nil {
		return err
	}
	if c.mid, err = hexToColor(p.Mid); err != nil {
		return err
	}
	if c.treble, err = hexToColor(p.Treble); err != nil {
		return err
	}
	return nil
}

func (c *colorMapFrequency) Process(env *Env) {
	r := clamp01(env.In["bass"])*c.bass.R + clamp01(env.In["mid"])*c.mid.R + clamp01(env.In["treble"])*c.treble.R
	g := clamp01(env.In["bass"])*c.bass.G + clamp01(env.In["mid"])*c.mid.G + clamp01(env.In["treble"])*c.treble.G
	b := clamp01(env.In["bass"])*c.bass.B + clamp01(env.In["mid"])*c.mid.B + clamp01(env.In["treble"])*c.treble.B
	env.Out["r"], env.Out["g"], env.Out["b"] = clamp01(r), clamp01(g), clamp01(b)
}

// strobeBeat flashes on a beat: the envelope jumps to 1 and decays over
// duration_ms (exponential or linear). The color ports carry the flash color
// scaled by the envelope — black while idle, which the runner treats as "no
// color" — so a strobe terminal can overlay a brightness-driven light set
// without owning the value bus: off-beat the lights keep their brightness
// chain's color, on-beat they flash.
type strobeBeat struct {
	params struct {
		Color      string  `json:"color"`
		DurationMS float64 `json:"duration_ms"`
		Decay      string  `json:"decay"` // exp | lin
	}
	color Color
	env   float64
	last  time.Time
}

func (s *strobeBeat) Type() string { return "strobe_beat" }

func (s *strobeBeat) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "strobe_beat", Category: CategoryEffect,
		Label: "Strobe (Beat)", Description: "Flashes on beat with configurable color and decay.",
		Inputs:  []Port{{Name: "beat", Kind: PortTrigger}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}, {Name: "r", Kind: PortScalar}, {Name: "g", Kind: PortScalar}, {Name: "b", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "color", Label: "Color", Type: "color", Default: "#FF6600", Description: "Flash color (hex)."},
			{Name: "duration_ms", Label: "Duration (ms)", Type: "number", Default: 250.0, Step: 10, Description: "Flash duration."},
			{Name: "decay", Label: "Decay", Type: "string", Default: "exp", Choices: []string{"exp", "lin"}, Description: "Decay curve shape."},
		},
	}
}

func (s *strobeBeat) Init(raw json.RawMessage) error {
	s.params.Color = "#FF6600"
	s.params.DurationMS = 250
	s.params.Decay = "exp"
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.params); err != nil {
			return err
		}
	}
	col, err := hexToColor(s.params.Color)
	if err != nil {
		return err
	}
	s.color = col
	if s.params.DurationMS <= 0 {
		s.params.DurationMS = 250
	}
	switch s.params.Decay {
	case "lin", "exp":
	default:
		s.params.Decay = "exp"
	}
	return nil
}

func (s *strobeBeat) Process(env *Env) {
	if env.In["beat"] > 0.5 {
		s.env = 1
		s.last = env.Now
	} else if s.env > 0 {
		elapsed := env.Now.Sub(s.last).Seconds() * 1000
		d := s.params.DurationMS
		switch s.params.Decay {
		case "lin":
			s.env = math.Max(0, 1-elapsed/d)
		default:
			// exp: tau = duration/3, so the flash is ~95% gone in one
			// duration and invisible by two.
			s.env = math.Exp(-3 * elapsed / d)
			// Snap to black: the exponential never reaches zero, and the
			// runner's "black means uncolored" rule is exact — without the
			// snap, a dying flash would tint the brightness chain's white
			// forever.
			if s.env < 0.02 {
				s.env = 0
			}
		}
	}
	env.Out["out"] = s.env
	env.Out["r"] = s.color.R * s.env
	env.Out["g"] = s.color.G * s.env
	env.Out["b"] = s.color.B * s.env
}

// chaseTrigger advances a lit head through the target lights. Without a
// "trigger" edge it advances on a timer (speed = seconds per step); with
// one, it advances once per trigger. width lights trail the head, each step
// behind multiplied by trail_decay.
type chaseTrigger struct {
	params struct {
		LightIDs   []string `json:"light_ids"` // empty → all lights
		Speed      float64  `json:"speed"`     // seconds per step
		Width      float64  `json:"width"`     // lit lights behind the head
		TrailDecay float64  `json:"trail_decay"`
	}
	ids     []string
	head    int
	started bool
	last    time.Time
}

func (c *chaseTrigger) Type() string { return "chase_trigger" }

func (c *chaseTrigger) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "chase_trigger", Category: CategoryEffect,
		Label: "Chase (Trigger)", Description: "Advances a lit head through target lights on trigger or timer.",
		Inputs:  []Port{{Name: "trigger", Kind: PortTrigger}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}, {Name: "position", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "light_ids", Label: "Light IDs", Type: "light_ids", Default: []string{}, Description: "Target lights. Empty = all."},
			{Name: "speed", Label: "Speed (s)", Type: "number", Default: 0.3, Step: 0.05, Description: "Seconds per step when no trigger wired."},
			{Name: "width", Label: "Width", Type: "number", Default: 1.0, Step: 1, Description: "Lit lights behind the head."},
			{Name: "trail_decay", Label: "Trail Decay", Type: "number", Default: 0.5, Step: 0.05, Description: "Decay multiplier per step behind head."},
		},
	}
}

func (c *chaseTrigger) Init(raw json.RawMessage) error {
	c.params.Speed = 0.3
	c.params.Width = 1
	c.params.TrailDecay = 0.5
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c.params); err != nil {
			return err
		}
	}
	c.ids = append([]string(nil), c.params.LightIDs...)
	if c.params.Speed <= 0 {
		c.params.Speed = 0.3
	}
	if c.params.Width < 1 {
		c.params.Width = 1
	}
	if c.params.TrailDecay <= 0 || c.params.TrailDecay > 1 {
		c.params.TrailDecay = 0.5
	}
	return nil
}

func (c *chaseTrigger) Process(env *Env) {
	ids := c.ids
	if len(ids) == 0 {
		ids = env.AllLights
	}
	env.Out["position"] = float64(c.head)

	if len(ids) == 0 {
		return
	}
	advance := false
	if _, wired := env.In["trigger"]; wired {
		advance = env.In["trigger"] > 0.5
	} else if !c.started || env.Now.Sub(c.last).Seconds() >= c.params.Speed {
		advance = true
	}
	if advance {
		c.head = (c.head + 1) % len(ids)
		c.last = env.Now
		c.started = true
	}
	if !c.started {
		return
	}
	width := int(c.params.Width)
	for i, rid := range ids {
		d := (c.head - i + len(ids)) % len(ids) // steps behind the head
		if d >= width {
			continue
		}
		env.Lights[rid] = math.Pow(c.params.TrailDecay, float64(d))
	}
	env.Out["out"] = env.Lights[ids[c.head]]
}

// pulseEnergy drives brightness as a pulse centred on one light: lights
// within `radius × energy` of the centre get energy scaled by their
// distance, so louder music pushes the pulse further out (expand/contract
// with the signal) and decay sharpens the falloff at the edge.
type pulseEnergy struct {
	params struct {
		CenterLight string  `json:"center_light"`
		Radius      float64 `json:"radius"`
		Decay       float64 `json:"decay"`
	}
	center string
}

func (p *pulseEnergy) Type() string { return "pulse_energy" }

func (p *pulseEnergy) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "pulse_energy", Category: CategoryEffect,
		Label: "Pulse (Energy)", Description: "Drives brightness as a spatial pulse from a center light, expanding with energy.",
		Inputs:  []Port{{Name: "energy", Kind: PortScalar}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "center_light", Label: "Center Light", Type: "string", Default: "", Description: "RID of the center light."},
			{Name: "radius", Label: "Radius", Type: "number", Default: 0.5, Step: 0.05, Description: "Base pulse radius."},
			{Name: "decay", Label: "Decay", Type: "number", Default: 0.5, Step: 0.05, Description: "Distance falloff sharpness."},
		},
	}
}

func (p *pulseEnergy) Init(raw json.RawMessage) error {
	p.params.Radius = 0.5
	p.params.Decay = 0.5
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p.params); err != nil {
			return err
		}
	}
	if p.params.Radius <= 0 {
		p.params.Radius = 0.5
	}
	if p.params.Decay <= 0 {
		p.params.Decay = 0.5
	}
	p.center = p.params.CenterLight
	return nil
}

func (p *pulseEnergy) Process(env *Env) {
	energy := clamp01(env.In["energy"])
	env.Out["out"] = energy
	// No positions for the centre means we cannot measure distance — stay
	// quiet rather than guess.
	if len(env.Pos) == 0 {
		return
	}
	center, ok := env.Pos[p.center]
	if !ok {
		for _, rid := range env.AllLights {
			if _, ok := env.Pos[rid]; ok {
				center = env.Pos[rid]
				break
			}
		}
	}
	if !ok {
		return
	}
	radius := p.params.Radius * energy
	if radius <= 0 {
		return
	}
	for _, rid := range env.AllLights {
		pos, ok := env.Pos[rid]
		if !ok {
			continue
		}
		d := distance3(pos, center)
		if d > radius {
			continue
		}
		falloff := 1 - d/radius
		env.Lights[rid] = energy * math.Pow(falloff, p.params.Decay)
	}
}

// Pos3 is a light's physical position in the room (hue.Position's shape,
// kept local so the preset package need not import hue).
type Pos3 struct{ X, Y, Z float64 }

func distance3(a, b Pos3) float64 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
