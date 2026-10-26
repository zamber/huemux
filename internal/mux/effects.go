package mux

import "math"

func init() {
	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "solid", Label: "Solid",
			Description: "One color across the target lights, optionally breathing with a source.",
			Params: []ParamSpec{
				{Name: "color", Label: "Color", Type: "color", Default: "#FF7A18"},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 0.8, Min: 0, Max: 1, Step: 0.01},
				{Name: "source", Label: "Drive from", Type: "text", Default: "", Help: "Source value name, e.g. value or bass. Empty keeps a steady brightness."},
				{Name: "min", Label: "Min", Type: "number", Default: 0.1, Min: 0, Max: 1, Step: 0.01},
				{Name: "max", Label: "Max", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &solidEffect{
				color: pcolor(p, "color", "#FF7A18"), brightness: pnum(p, "brightness", 0.8),
				source: pstr(p, "source", ""), min: pnum(p, "min", 0.1), max: pnum(p, "max", 1),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "pulse", Label: "Pulse",
			Description: "Source value mapped to brightness through a curve. The workhorse for timers and sound.",
			Params: []ParamSpec{
				{Name: "color", Label: "Color", Type: "color", Default: "#FF7A18"},
				{Name: "source", Label: "Drive from", Type: "text", Default: "value"},
				{Name: "min", Label: "Min", Type: "number", Default: 0.05, Min: 0, Max: 1, Step: 0.01},
				{Name: "max", Label: "Max", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "curve", Label: "Curve", Type: "enum", Default: "lin", Choices: []Choice{
					{Value: "lin", Label: "Linear"}, {Value: "exp", Label: "Exponential"},
					{Value: "sqrt", Label: "Square root"}, {Value: "sine", Label: "Sine"},
				}},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &pulseEffect{
				color: pcolor(p, "color", "#FF7A18"), source: pstr(p, "source", "value"),
				min: pnum(p, "min", 0.05), max: pnum(p, "max", 1), curve: pstr(p, "curve", "lin"),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "gradient", Label: "Gradient",
			Description: "A two-color gradient across the room, optionally scrolling with a source.",
			Params: []ParamSpec{
				{Name: "from", Label: "From", Type: "color", Default: "#FF3B30"},
				{Name: "to", Label: "To", Type: "color", Default: "#3A7BFF"},
				{Name: "axis", Label: "Axis", Type: "enum", Default: "x", Choices: []Choice{
					{Value: "x", Label: "Left → right"}, {Value: "y", Label: "Bottom → top"},
					{Value: "index", Label: "Light order"}, {Value: "radial", Label: "Radial"},
					{Value: "angle", Label: "Angle around center"},
				}},
				{Name: "offset", Label: "Offset", Type: "number", Default: 0.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "spread", Label: "Scroll", Type: "number", Default: 0.0, Min: -2, Max: 2, Step: 0.01, Help: "How much the source value shifts the gradient."},
				{Name: "source", Label: "Scroll from", Type: "text", Default: "value"},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 0.8, Min: 0, Max: 1, Step: 0.01},
				{Name: "reverse", Label: "Reverse", Type: "bool", Default: false},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &gradientEffect{
				from: pcolor(p, "from", "#FF3B30"), to: pcolor(p, "to", "#3A7BFF"),
				axis: pstr(p, "axis", "x"), offset: pnum(p, "offset", 0),
				spread: pnum(p, "spread", 0), source: pstr(p, "source", "value"),
				brightness: pnum(p, "brightness", 0.8), reverse: pbool(p, "reverse", false),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "chase", Label: "Chase",
			Description: "A moving head with a width and trail. Index order by default, or use the room axes.",
			Params: []ParamSpec{
				{Name: "color", Label: "Head", Type: "color", Default: "#FFFFFF"},
				{Name: "background", Label: "Background", Type: "color", Default: "#000000"},
				{Name: "axis", Label: "Axis", Type: "enum", Default: "index", Choices: []Choice{
					{Value: "index", Label: "Light order"}, {Value: "x", Label: "Left → right"},
					{Value: "y", Label: "Bottom → top"}, {Value: "radial", Label: "Radial"},
				}},
				{Name: "width", Label: "Head width", Type: "number", Default: 0.18, Min: 0.01, Max: 1, Step: 0.01},
				{Name: "trail", Label: "Trail", Type: "number", Default: 0.35, Min: 0, Max: 1, Step: 0.01},
				{Name: "speed", Label: "Speed", Type: "number", Default: 0.35, Min: 0.01, Max: 3, Step: 0.01, Help: "Laps per second."},
				{Name: "direction", Label: "Direction", Type: "enum", Default: "forward", Choices: []Choice{
					{Value: "forward", Label: "Forward"}, {Value: "reverse", Label: "Reverse"},
				}},
				{Name: "source", Label: "Phase from", Type: "text", Default: ""},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 0.9, Min: 0, Max: 1, Step: 0.01},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &chaseEffect{
				color: pcolor(p, "color", "#FFFFFF"), background: pcolor(p, "background", "#000000"),
				axis: pstr(p, "axis", "index"), width: pnum(p, "width", 0.18),
				trail: pnum(p, "trail", 0.35), speed: pnum(p, "speed", 0.35),
				direction: pstr(p, "direction", "forward"), source: pstr(p, "source", ""),
				brightness: pnum(p, "brightness", 0.9),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "rainbow", Label: "Rainbow",
			Description: "A rotating hue ramp across the lights.",
			Params: []ParamSpec{
				{Name: "speed", Label: "Rotation", Type: "number", Default: 0.04, Min: -1, Max: 1, Step: 0.01, Help: "Turns per second."},
				{Name: "spread", Label: "Spread", Type: "number", Default: 1.0, Min: 0, Max: 4, Step: 0.01},
				{Name: "saturation", Label: "Saturation", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 0.8, Min: 0, Max: 1, Step: 0.01},
				{Name: "source", Label: "Phase from", Type: "text", Default: ""},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &rainbowEffect{
				speed: pnum(p, "speed", 0.04), spread: pnum(p, "spread", 1),
				saturation: pnum(p, "saturation", 1), brightness: pnum(p, "brightness", 0.8),
				source: pstr(p, "source", ""),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "sparkle", Label: "Sparkle",
			Description: "Per-light twinkles. Deterministic for a given seed and clock.",
			Params: []ParamSpec{
				{Name: "color", Label: "Color", Type: "color", Default: "#FFFFFF"},
				{Name: "density", Label: "Density", Type: "number", Default: 0.22, Min: 0.01, Max: 1, Step: 0.01},
				{Name: "decay", Label: "Decay", Type: "number", Default: 1.4, Min: 0.1, Max: 5, Step: 0.1},
				{Name: "speed", Label: "Speed", Type: "number", Default: 1.0, Min: 0.05, Max: 5, Step: 0.05},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "seed", Label: "Seed", Type: "number", Default: 7.0, Min: 1, Max: 9999, Step: 1},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &sparkleEffect{
				color: pcolor(p, "color", "#FFFFFF"), density: pnum(p, "density", 0.22),
				decay: pnum(p, "decay", 1.4), speed: pnum(p, "speed", 1),
				brightness: pnum(p, "brightness", 1), seed: uint64(pnum(p, "seed", 7)),
			}, nil
		},
	})

	RegisterEffect(EffectSpec{
		Meta: NodeMeta{
			Type: "video_map", Label: "Video map",
			Description: "Passes the video area's per-light colors through. Emits nothing while no capture is running.",
			Params: []ParamSpec{
				{Name: "gain", Label: "Gain", Type: "number", Default: 1.0, Min: 0.1, Max: 3, Step: 0.05},
				{Name: "brightness", Label: "Brightness", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "saturation", Label: "Saturation", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
			},
		},
		New: func(p map[string]any) (Effect, error) {
			return &videoMapEffect{
				gain: pnum(p, "gain", 1), brightness: pnum(p, "brightness", 1),
				saturation: pnum(p, "saturation", 1),
			}, nil
		},
	})
}

// --- solid -----------------------------------------------------------

type solidEffect struct {
	color      Color
	brightness float64
	source     string
	min, max   float64
}

func (e *solidEffect) Type() string { return "solid" }

func (e *solidEffect) Render(in RenderInput) map[string]Color {
	factor := 1.0
	if e.source != "" {
		v := clamp01(in.Source.Value(e.source))
		factor = e.min + (e.max-e.min)*v
	}
	b := clamp01(e.brightness * factor)
	return solidPixels(in.Lights, e.color.Scale(b))
}

// --- pulse -----------------------------------------------------------

type pulseEffect struct {
	color  Color
	source string
	min    float64
	max    float64
	curve  string
}

func (e *pulseEffect) Type() string { return "pulse" }

func (e *pulseEffect) Render(in RenderInput) map[string]Color {
	v := clamp01(in.Source.Value(e.source))
	switch e.curve {
	case "exp":
		v *= v
	case "sqrt":
		v = math.Sqrt(v)
	case "sine":
		v = 0.5 - 0.5*math.Cos(math.Pi*v)
	}
	b := clamp01(e.min + (e.max-e.min)*v)
	return solidPixels(in.Lights, e.color.Scale(b))
}

// --- gradient --------------------------------------------------------

type gradientEffect struct {
	from, to   Color
	axis       string
	offset     float64
	spread     float64
	source     string
	brightness float64
	reverse    bool
}

func (e *gradientEffect) Type() string { return "gradient" }

func (e *gradientEffect) Render(in RenderInput) map[string]Color {
	scroll := in.Source.Value(e.source) * e.spread
	out := make(map[string]Color, len(in.Lights))
	n := len(in.Lights)
	for i, l := range in.Lights {
		t := axisValue(e.axis, l, i, n) + e.offset + scroll
		if e.reverse {
			t = 1 - t
		}
		t = frac(t)
		out[l.ID] = Mix(e.from, e.to, t).Scale(e.brightness)
	}
	return out
}

// --- chase -----------------------------------------------------------

type chaseEffect struct {
	color, background Color
	axis              string
	width, trail      float64
	speed             float64
	direction         string
	source            string
	brightness        float64
}

func (e *chaseEffect) Type() string { return "chase" }

func (e *chaseEffect) Render(in RenderInput) map[string]Color {
	head := frac(float64(in.Now.UnixNano())/1e9*e.speed + in.Source.Value(e.source))
	if e.direction == "reverse" {
		head = 1 - head
	}
	width := clamp01(e.width)
	trail := clamp01(e.trail)
	bg := e.background.Scale(0.06)
	out := make(map[string]Color, len(in.Lights))
	n := len(in.Lights)
	for i, l := range in.Lights {
		pos := axisValue(e.axis, l, i, n)
		d := math.Abs(head - pos)
		if d > 0.5 {
			d = 1 - d
		}
		var intensity float64
		if d <= width {
			intensity = 1
		} else if trail > 0 && d <= width+trail {
			intensity = 1 - (d-width)/trail
			intensity = smoothstep(intensity)
		}
		c := Mix(bg, e.color, intensity).Scale(e.brightness)
		out[l.ID] = c
	}
	return out
}

// --- rainbow ---------------------------------------------------------

type rainbowEffect struct {
	speed      float64
	spread     float64
	saturation float64
	brightness float64
	source     string
}

func (e *rainbowEffect) Type() string { return "rainbow" }

func (e *rainbowEffect) Render(in RenderInput) map[string]Color {
	now := float64(in.Now.UnixNano()) / 1e9
	phase := in.Source.Value(e.source)
	out := make(map[string]Color, len(in.Lights))
	n := len(in.Lights)
	for i, l := range in.Lights {
		pos := 0.0
		if n > 1 {
			pos = float64(i) / float64(n)
		}
		h := frac(now*e.speed + pos*e.spread + phase)
		out[l.ID] = HSV(h, e.saturation, 1).Scale(e.brightness)
	}
	return out
}

// --- sparkle ---------------------------------------------------------

type sparkleEffect struct {
	color      Color
	density    float64
	decay      float64
	speed      float64
	brightness float64
	seed       uint64
}

func (e *sparkleEffect) Type() string { return "sparkle" }

func (e *sparkleEffect) Render(in RenderInput) map[string]Color {
	rate := e.speed * 4
	nowSec := float64(in.Now.UnixNano()) / 1e9
	seg := int64(math.Floor(nowSec * rate))
	t := frac(nowSec * rate)
	out := make(map[string]Color, len(in.Lights))
	for i, l := range in.Lights {
		if hashUnit(e.seed, int64(i)*100003+seg) >= e.density {
			continue
		}
		b := math.Pow(1-t, e.decay) * e.brightness
		if b <= 0.01 {
			continue
		}
		out[l.ID] = e.color.Scale(clamp01(b))
	}
	return out
}

// --- video map -------------------------------------------------------

type videoMapEffect struct {
	gain       float64
	brightness float64
	saturation float64
}

func (e *videoMapEffect) Type() string { return "video_map" }

func (e *videoMapEffect) Render(in RenderInput) map[string]Color {
	if len(in.Source.Colors) == 0 {
		return nil
	}
	out := make(map[string]Color, len(in.Lights))
	for _, l := range in.Lights {
		c, ok := in.Source.Colors[l.ID]
		if !ok {
			continue
		}
		c = c.Desaturate(e.saturation).Scale(e.gain * e.brightness)
		if c.Peak() > 0.005 {
			out[l.ID] = c
		}
	}
	return out
}

// --- helpers ---------------------------------------------------------

func solidPixels(lights []Light, c Color) map[string]Color {
	out := make(map[string]Color, len(lights))
	for _, l := range lights {
		out[l.ID] = c
	}
	return out
}

func axisValue(axis string, l Light, i, n int) float64 {
	switch axis {
	case "x":
		return clamp01(l.X)
	case "y":
		return clamp01(l.Y)
	case "radial":
		dx, dy := l.X-0.5, l.Y-0.5
		return clamp01(math.Hypot(dx, dy) * 2)
	case "angle":
		a := math.Atan2(l.Y-0.5, l.X-0.5)/(2*math.Pi) + 0.5
		return frac(a)
	default: // index
		if n <= 1 {
			return 0
		}
		return float64(i) / float64(n-1)
	}
}
