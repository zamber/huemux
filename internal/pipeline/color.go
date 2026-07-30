package pipeline

import (
	"math"

	"github.com/zamber/huemux/internal/hue"
)

// Grid is the reduced screen capture the browser sends: RGB, row-major,
// top-left origin, matching the wire format in PROTOCOL.md.
type Grid struct {
	W, H int
	Pix  []byte // len == W*H*3
}

func (g *Grid) at(x, y int) (r, gr, b byte) {
	if g == nil || x < 0 || y < 0 || x >= g.W || y >= g.H {
		return 0, 0, 0
	}
	i := (y*g.W + x) * 3
	return g.Pix[i], g.Pix[i+1], g.Pix[i+2]
}

// srgbToLinearLUT precomputes the sRGB electro-optical transfer function
// inverse for every possible 8-bit input. Averaging gamma-encoded samples is
// the single most common cause of muddy, washed-out output, so every sample
// goes through this before it is summed.
var srgbToLinearLUT = func() [256]float64 {
	var lut [256]float64
	for i := range lut {
		c := float64(i) / 255.0
		if c <= 0.04045 {
			lut[i] = c / 12.92
		} else {
			lut[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return lut
}()

func srgbToLinear(v byte) float64 { return srgbToLinearLUT[v] }

func linearToSRGB(c float64) byte {
	if c <= 0 {
		return 0
	}
	if c >= 1 {
		return 255
	}
	var s float64
	if c <= 0.0031308 {
		s = c * 12.92
	} else {
		s = 1.055*math.Pow(c, 1/2.4) - 0.055
	}
	v := int(math.Round(s * 255))
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(v)
}

// LetterboxMask records rows/columns at the edges of the grid that are near
// enough to black, contiguously from the edge, to be film letterboxing
// rather than content. Sampling treats these as excluded so black bars do
// not drag every zone's average toward black.
type LetterboxMask struct {
	Top, Bottom int // rows excluded, counted from each edge
	Left, Right int // columns excluded, counted from each edge
}

// letterboxLuminanceThreshold is how dark (in linear light) a row/column's
// average has to be, contiguously from an edge, to count as a bar.
const letterboxLuminanceThreshold = 0.02

// DetectLetterbox scans the tiny grid for near-black bars at each edge. It
// is a dozen comparisons on a 64×36 grid, not a real image analysis pass.
func DetectLetterbox(g *Grid) LetterboxMask {
	var m LetterboxMask
	if g == nil || g.W == 0 || g.H == 0 {
		return m
	}

	rowLum := func(y int) float64 {
		sum := 0.0
		for x := 0; x < g.W; x++ {
			r, gr, b := g.at(x, y)
			sum += luma(srgbToLinear(r), srgbToLinear(gr), srgbToLinear(b))
		}
		return sum / float64(g.W)
	}
	colLum := func(x int) float64 {
		sum := 0.0
		for y := 0; y < g.H; y++ {
			r, gr, b := g.at(x, y)
			sum += luma(srgbToLinear(r), srgbToLinear(gr), srgbToLinear(b))
		}
		return sum / float64(g.H)
	}

	for y := 0; y < g.H/3; y++ { // never treat more than a third of the frame as a bar
		if rowLum(y) > letterboxLuminanceThreshold {
			break
		}
		m.Top++
	}
	for y := g.H - 1; y >= g.H-g.H/3; y-- {
		if rowLum(y) > letterboxLuminanceThreshold {
			break
		}
		m.Bottom++
	}
	for x := 0; x < g.W/3; x++ {
		if colLum(x) > letterboxLuminanceThreshold {
			break
		}
		m.Left++
	}
	for x := g.W - 1; x >= g.W-g.W/3; x-- {
		if colLum(x) > letterboxLuminanceThreshold {
			break
		}
		m.Right++
	}
	return m
}

func luma(r, g, b float64) float64 { return 0.2126*r + 0.7152*g + 0.0722*b }

// SampleZoneLinear averages a zone's pixels in linear light, feathering the
// rect edges so a zone that falls between grid cells does not snap and
// judder as content moves, and skipping any letterboxed rows/columns.
func SampleZoneLinear(g *Grid, mask LetterboxMask, z Zone) (r, gr, b float64) {
	if g == nil || g.W == 0 || g.H == 0 {
		return 0, 0, 0
	}

	feather := z.Feather
	x0 := (z.U0 - feather) * float64(g.W)
	x1 := (z.U1 + feather) * float64(g.W)
	y0 := (z.V0 - feather) * float64(g.H)
	y1 := (z.V1 + feather) * float64(g.H)

	pxLo := clampInt(int(math.Floor(x0)), 0, g.W-1)
	pxHi := clampInt(int(math.Ceil(x1)), 0, g.W-1)
	pyLo := clampInt(int(math.Floor(y0)), 0, g.H-1)
	pyHi := clampInt(int(math.Ceil(y1)), 0, g.H-1)

	validXLo, validXHi := mask.Left, g.W-1-mask.Right
	validYLo, validYHi := mask.Top, g.H-1-mask.Bottom

	var sumR, sumG, sumB, sumW float64
	fw := feather * float64(g.W)
	fh := feather * float64(g.H)
	if fw <= 0 {
		fw = 1
	}
	if fh <= 0 {
		fh = 1
	}

	for py := pyLo; py <= pyHi; py++ {
		if py < validYLo || py > validYHi {
			continue
		}
		for px := pxLo; px <= pxHi; px++ {
			if px < validXLo || px > validXHi {
				continue
			}
			cx, cy := float64(px)+0.5, float64(py)+0.5
			wx := edgeWeight(cx, z.U0*float64(g.W), z.U1*float64(g.W), fw)
			wy := edgeWeight(cy, z.V0*float64(g.H), z.V1*float64(g.H), fh)
			w := wx * wy
			if w <= 0 {
				continue
			}
			pr, pg, pb := g.at(px, py)
			sumR += w * srgbToLinear(pr)
			sumG += w * srgbToLinear(pg)
			sumB += w * srgbToLinear(pb)
			sumW += w
		}
	}
	if sumW == 0 {
		return 0, 0, 0
	}
	return sumR / sumW, sumG / sumW, sumB / sumW
}

// edgeWeight is 1 inside [lo,hi], falling off linearly to 0 over a distance
// of feather outside either edge.
func edgeWeight(v, lo, hi, feather float64) float64 {
	if v >= lo && v <= hi {
		return 1
	}
	var d float64
	if v < lo {
		d = lo - v
	} else {
		d = v - hi
	}
	if d >= feather {
		return 0
	}
	return 1 - d/feather
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ColorParams are the per-tick knobs the color pipeline applies, on top of
// whatever is already baked into a zone's geometry.
type ColorParams struct {
	Saturation   float64 // 0-200, 100 = unchanged
	Brightness   float64 // 0-200, 100 = unchanged
	BlackCutoff  float64 // 0-1 linear; below this, output true black
	ChannelGain  float64 // per-channel brightness multiplier, 1 = unchanged
	ColorSpace   hue.ColorSpace
	ColorCapable bool // false for white-only bulbs: drive brightness only
}

// processLinear applies saturation, brightness/channel gain and black
// cutoff in linear light — the color-space-agnostic part of Process, shared
// with DisplayRGB below so the calibration preview can run the exact same
// adjustments without the xy-packing step.
func processLinear(rLin, gLin, bLin float64, p ColorParams) (float64, float64, float64) {
	// Saturation: push away from (or toward) the perceptual gray point.
	sat := p.Saturation / 100
	l := luma(rLin, gLin, bLin)
	rLin = l + sat*(rLin-l)
	gLin = l + sat*(gLin-l)
	bLin = l + sat*(bLin-l)

	// Brightness, including the per-channel multiplier, then black cutoff.
	gain := (p.Brightness / 100) * p.ChannelGain
	rLin *= gain
	gLin *= gain
	bLin *= gain

	if maxOf3(rLin, gLin, bLin) < p.BlackCutoff {
		rLin, gLin, bLin = 0, 0, 0
	}
	return clamp01f(rLin), clamp01f(gLin), clamp01f(bLin)
}

// Process runs one zone's linear-light average through gain, cutoff and
// gamut steps, in the order the roadmap specifies, and returns the 8-bit
// channel ready to encode onto the wire.
//
// Order matters: saturation and brightness gain happen in linear light,
// gamma re-encoding happens last, and it happens exactly once.
func Process(rLin, gLin, bLin float64, channelID uint8, p ColorParams) hue.Channel {
	rLin, gLin, bLin = processLinear(rLin, gLin, bLin, p)

	if !p.ColorCapable {
		// White-only bulbs only respond to brightness: collapse to a gray
		// value at the target brightness rather than sitting at whatever
		// arbitrary hue the last frame happened to compute.
		v := luma(rLin, gLin, bLin)
		g := linearToSRGB(v)
		return hue.Channel{ID: channelID, R: g, G: g, B: g}
	}

	switch p.ColorSpace {
	case hue.ColorSpaceXY:
		x, y, brightness := linearRGBtoXYBrightness(rLin, gLin, bLin)
		// Packed into the same three 8-bit-effective wire fields as RGB:
		// X, Y chromaticity and brightness, each scaled 0-255. The bridge's
		// low-byte-ignored behavior (see PROTOCOL.md) applies uniformly, so
		// there is no benefit to carrying more than 8 bits of precision
		// through this struct. NOTE: these bytes are NOT displayable RGB —
		// see DisplayRGB for the preview-safe equivalent.
		return hue.Channel{
			ID: channelID,
			R:  byte(clampf(x*255, 0, 255)),
			G:  byte(clampf(y*255, 0, 255)),
			B:  byte(clampf(brightness*255, 0, 255)),
		}
	default: // ColorSpaceRGB: bridge does the gamut conversion, we just gamma-encode
		return hue.Channel{
			ID: channelID,
			R:  linearToSRGB(rLin),
			G:  linearToSRGB(gLin),
			B:  linearToSRGB(bLin),
		}
	}
}

// DisplayRGB runs the same linear-light adjustments as Process but always
// gamma-encodes to real sRGB, regardless of p.ColorSpace — for callers that
// want "what color does this zone look like" (the calibration preview)
// rather than "what bytes go on the wire." Process's ColorSpaceXY branch
// packs x/y chromaticity and brightness into the Channel's R/G/B fields for
// the bridge; reusing those same bytes as a literal preview color (as the
// engine's status snapshot used to) renders every zone with a consistent
// greenish/yellowish cast, since y (landing in the G byte) tends to sit in
// a moderate-to-high range for most captured content regardless of what's
// actually on screen.
func DisplayRGB(rLin, gLin, bLin float64, p ColorParams) [3]byte {
	rLin, gLin, bLin = processLinear(rLin, gLin, bLin, p)
	if !p.ColorCapable {
		v := linearToSRGB(luma(rLin, gLin, bLin))
		return [3]byte{v, v, v}
	}
	return [3]byte{linearToSRGB(rLin), linearToSRGB(gLin), linearToSRGB(bLin)}
}

// linearRGBtoXYBrightness converts linear sRGB to CIE 1931 xy chromaticity
// plus relative brightness, using the sRGB/Rec.709 primaries matrix. It does
// not clamp into a specific light's gamut triangle (Gamut C etc.) — that
// requires per-light gamut data from the light service resource, which is a
// further refinement left for a later pass.
func linearRGBtoXYBrightness(r, g, b float64) (x, y, brightness float64) {
	X := 0.4124*r + 0.3576*g + 0.1805*b
	Y := 0.2126*r + 0.7152*g + 0.0722*b
	Z := 0.0193*r + 0.1192*g + 0.9505*b
	sum := X + Y + Z
	if sum <= 0 {
		return 0, 0, 0
	}
	return X / sum, Y / sum, clamp01f(Y)
}

func maxOf3(a, b, c float64) float64 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

func clamp01f(f float64) float64 { return clampf(f, 0, 1) }

func clampf(f, lo, hi float64) float64 {
	if f < lo {
		return lo
	}
	if f > hi {
		return hi
	}
	return f
}
