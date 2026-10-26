package mux

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Color is linear RGB in 0..1. Linear is the internal currency because
// averaging or blending gamma-encoded values makes mixed colors muddy; the
// conversion to sRGB happens once, at the edges (hex parsing and the UI
// snapshot).
type Color struct{ R, G, B float64 }

// Hex parses #RGB, #RRGGBB and #RRGGBBAA (alpha is ignored) into linear RGB.
func Hex(s string) (Color, error) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
	switch len(s) {
	case 3:
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	case 6, 8:
		s = s[:6]
	default:
		return Color{}, fmt.Errorf("mux: %q is not a hex color", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return Color{}, fmt.Errorf("mux: %q is not a hex color: %w", s, err)
	}
	r := float64((v>>16)&0xff) / 255
	g := float64((v>>8)&0xff) / 255
	b := float64(v&0xff) / 255
	return Color{R: srgbToLinear(r), G: srgbToLinear(g), B: srgbToLinear(b)}, nil
}

// MustHex is Hex for catalog defaults, where a bad literal is a programming
// error rather than user input.
func MustHex(s string) Color {
	c, err := Hex(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Hex renders the color as #RRGGBB in sRGB.
func (c Color) Hex() string {
	r, g, b := c.SRGB8()
	return fmt.Sprintf("#%02X%02X%02X", r, g, b)
}

// SRGB8 returns the display-ready 8-bit components.
func (c Color) SRGB8() (r, g, b uint8) {
	return linearToSRGB8(c.R), linearToSRGB8(c.G), linearToSRGB8(c.B)
}

// Scale multiplies every channel, clamped to 0..1.
func (c Color) Scale(v float64) Color {
	return Color{
		R: clamp01(c.R * v),
		G: clamp01(c.G * v),
		B: clamp01(c.B * v),
	}
}

// Add returns the per-channel sum, clamped.
func (c Color) Add(o Color) Color {
	return Color{R: clamp01(c.R + o.R), G: clamp01(c.G + o.G), B: clamp01(c.B + o.B)}
}

// Max returns the per-channel maximum.
func (c Color) Max(o Color) Color {
	return Color{R: math.Max(c.R, o.R), G: math.Max(c.G, o.G), B: math.Max(c.B, o.B)}
}

// Luma is the Rec. 709 relative luminance.
func (c Color) Luma() float64 { return 0.2126*c.R + 0.7152*c.G + 0.0722*c.B }

// Peak is the brightest channel, used as the output brightness.
func (c Color) Peak() float64 { return math.Max(c.R, math.Max(c.G, c.B)) }

// Normalized divides by the peak, leaving a hue/saturation-only color.
func (c Color) Normalized() Color {
	m := c.Peak()
	if m <= 0 {
		return Color{}
	}
	return Color{R: c.R / m, G: c.G / m, B: c.B / m}
}

// Mix interpolates in linear space.
func Mix(a, b Color, t float64) Color {
	t = clamp01(t)
	return Color{
		R: a.R + (b.R-a.R)*t,
		G: a.G + (b.G-a.G)*t,
		B: a.B + (b.B-a.B)*t,
	}
}

// Desaturate mixes toward the color's own luminance. saturation 1 keeps the
// color, 0 returns grey at the same luminance.
func (c Color) Desaturate(saturation float64) Color {
	saturation = clamp01(saturation)
	y := c.Luma()
	return Color{
		R: y + (c.R-y)*saturation,
		G: y + (c.G-y)*saturation,
		B: y + (c.B-y)*saturation,
	}
}

// HSV builds a linear-RGB color from hue (turns, wrapped), saturation and
// value (0..1). Hue is in turns rather than degrees because every caller
// has a phase in 0..1 already.
func HSV(h, s, v float64) Color {
	h = h - math.Floor(h)
	s, v = clamp01(s), clamp01(v)
	if s == 0 {
		g := srgbToLinear(v)
		return Color{R: g, G: g, B: g}
	}
	i := int(h * 6)
	f := h*6 - float64(i)
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	var r, g, b float64
	switch i % 6 {
	case 0:
		r, g, b = v, t, p
	case 1:
		r, g, b = q, v, p
	case 2:
		r, g, b = p, v, t
	case 3:
		r, g, b = p, q, v
	case 4:
		r, g, b = t, p, v
	default:
		r, g, b = v, p, q
	}
	return Color{R: srgbToLinear(r), G: srgbToLinear(g), B: srgbToLinear(b)}
}

func srgbToLinear(c float64) float64 {
	c = clamp01(c)
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSRGB8(c float64) uint8 {
	c = clamp01(c)
	var v float64
	if c <= 0.0031308 {
		v = c * 12.92
	} else {
		v = 1.055*math.Pow(c, 1/2.4) - 0.055
	}
	return uint8(math.Round(clamp01(v) * 255))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// frac returns the positive fractional part.
func frac(v float64) float64 {
	v -= math.Floor(v)
	if v < 0 {
		v++
	}
	return v
}
