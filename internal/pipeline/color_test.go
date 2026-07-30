package pipeline

import (
	"testing"

	"github.com/zamber/huemux/internal/hue"
)

func solidGrid(w, h int, r, g, b byte) *Grid {
	pix := make([]byte, w*h*3)
	for i := 0; i < w*h; i++ {
		pix[i*3+0] = r
		pix[i*3+1] = g
		pix[i*3+2] = b
	}
	return &Grid{W: w, H: h, Pix: pix}
}

func TestSampleZoneLinearSolidRed(t *testing.T) {
	grid := solidGrid(64, 36, 255, 0, 0)
	mask := DetectLetterbox(grid)
	t.Logf("mask: %+v", mask)

	z := Zone{ChannelID: 0, U0: 0.295, V0: 0.85, U1: 0.595, V1: 1, Feather: 0.04}
	r, g, b := SampleZoneLinear(grid, mask, z)
	t.Logf("raw linear sample: r=%.4f g=%.4f b=%.4f", r, g, b)
	if r < 0.9 {
		t.Fatalf("expected r close to 1.0 for solid red input, got r=%.4f g=%.4f b=%.4f", r, g, b)
	}
	if g > 0.01 || b > 0.01 {
		t.Fatalf("expected g,b near 0 for solid red input, got g=%.4f b=%.4f", g, b)
	}

	ch := Process(r, g, b, 0, ColorParams{
		Saturation: 130, Brightness: 150, BlackCutoff: 0.02, ChannelGain: 1,
		ColorSpace: hue.ColorSpaceRGB, ColorCapable: true,
	})
	t.Logf("processed channel: R=%d G=%d B=%d", ch.R, ch.G, ch.B)
	if ch.R < 100 {
		t.Fatalf("expected a strong red channel, got R=%d G=%d B=%d", ch.R, ch.G, ch.B)
	}
}
