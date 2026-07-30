package lightctl

import (
	"context"
	"math"
)

// srgbToLinear applies the sRGB inverse transfer function to one channel,
// 0..1. Averaging or converting gamma-encoded values directly is the
// classic mistake that produces washed-out color — see
// internal/pipeline/color.go's identical concern for the screen-sync color
// pipeline. Duplicated here (rather than exporting pipeline's version)
// because it's ten lines and lightctl has no other reason to depend on the
// screen-sync package.
func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// rgbToXY converts 8-bit sRGB to CIE 1931 xy chromaticity — CLIP v2's color
// resource has no direct RGB or HSV field, only xy, so any picker built on
// RGB/HSV (as the press-hold color picker is) must convert before calling
// SetColorXY.
func rgbToXY(r, g, b uint8) (x, y float64) {
	rl := srgbToLinear(float64(r) / 255)
	gl := srgbToLinear(float64(g) / 255)
	bl := srgbToLinear(float64(b) / 255)

	X := 0.4124*rl + 0.3576*gl + 0.1805*bl
	Y := 0.2126*rl + 0.7152*gl + 0.0722*bl
	Z := 0.0193*rl + 0.1192*gl + 0.9505*bl

	sum := X + Y + Z
	if sum <= 0 {
		return 0, 0
	}
	return X / sum, Y / sum
}

// SetLightColorRGB is the convenience path for a picker that works in RGB
// (or converts HSV to RGB itself, as the ported press-hold picker does):
// converts to CIE xy, then PUTs exactly as SetLightColorXY would.
func (s *Service) SetLightColorRGB(ctx context.Context, rid string, r, g, b uint8) error {
	x, y := rgbToXY(r, g, b)
	return s.SetLightColorXY(ctx, rid, x, y)
}
