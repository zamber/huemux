package preset

import (
	"fmt"
	"math"
	"strconv"
)

// hexToColor parses "#RRGGBB" into a linear-space Color. Preset palettes are
// authored in sRGB hex because that is what a human writes; the effect layer
// works in linear space (averaging gamma-encoded values is the classic cause
// of muddy output), so the conversion happens here, once, at Init.
func hexToColor(s string) (Color, error) {
	if len(s) != 7 || s[0] != '#' {
		return Color{}, fmt.Errorf("color %q: want #RRGGBB", s)
	}
	var c Color
	for i, comp := range []*float64{&c.R, &c.G, &c.B} {
		v, err := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		if err != nil {
			return Color{}, fmt.Errorf("color %q: %w", s, err)
		}
		*comp = srgbToLinear(float64(v) / 255)
	}
	return c, nil
}

// srgbToLinear converts one 0..1 sRGB component to linear light.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// lerpColor interpolates between two linear colors.
func lerpColor(a, b Color, t float64) Color {
	return Color{
		R: a.R + (b.R-a.R)*t,
		G: a.G + (b.G-a.G)*t,
		B: a.B + (b.B-a.B)*t,
	}
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
