package pipeline

import (
	"testing"

	"lights.lan/lightsync/internal/hue"
)

// Real channel positions from a live "Living room pc" screen entertainment
// area (3 Play bars: one above the monitor, one either side). y (room depth)
// is identical across all three — they're all mounted right at the screen —
// which is exactly the case that broke the old x/y-only projection: mapping
// depth (y) to screen-vertical collapsed every channel into the same
// horizontal band regardless of the "Flip depth axis" setting, because y
// never varied in the first place. z (height) is what actually distinguishes
// the top bar from the two side bars.
func realPCChannels() []hue.EntertainmentChannel {
	return []hue.EntertainmentChannel{
		{ChannelID: 0, Position: hue.Position{X: -0.1100, Y: 1.0, Z: 0.5100}},  // top-middle
		{ChannelID: 1, Position: hue.Position{X: -0.3854, Y: 1.0, Z: -0.0300}}, // left-middle
		{ChannelID: 2, Position: hue.Position{X: 0.2198, Y: 1.0, Z: -0.0700}},  // right-middle
	}
}

func zoneByChannel(zones []Zone, id uint8) Zone {
	for _, z := range zones {
		if z.ChannelID == id {
			return z
		}
	}
	panic("channel not found")
}

func TestBuildZonesUsesHeightNotDepthByDefault(t *testing.T) {
	opts := DefaultZoneOpts("screen")
	if opts.AxisVertical != AxisZ {
		t.Fatalf("expected default vertical axis to be z (height), got %s", opts.AxisVertical)
	}
	zones := BuildZones(realPCChannels(), opts)
	if len(zones) != 3 {
		t.Fatalf("expected 3 zones, got %d", len(zones))
	}

	top := zoneByChannel(zones, 0)
	left := zoneByChannel(zones, 1)
	right := zoneByChannel(zones, 2)

	// ch0 (highest z) must land on the top edge.
	if top.V0 != 0 {
		t.Errorf("ch0 (top-middle, z=0.51) expected top edge (V0=0), got zone %+v", top)
	}
	// ch1/ch2 sit at similar, much lower z, roughly equidistant from top/bottom
	// given their x is not near 0.5 depth-wise — they should land on the
	// left/right edges (nearest-edge logic), not both stacked on the same
	// horizontal band the way the old y-as-vertical mapping produced.
	if left.U0 != 0 {
		t.Errorf("ch1 (left-middle, x=-0.39) expected left edge (U0=0), got zone %+v", left)
	}
	if right.U1 != 1 {
		t.Errorf("ch2 (right-middle, x=0.22) expected right edge (U1=1), got zone %+v", right)
	}

	// The old bug: mapping depth (constant y=1.0 for all three) to vertical
	// meant every zone got an identical V range regardless of channel.
	// Confirm that's no longer true between the top channel and the sides.
	if top.V0 == left.V0 && top.V1 == left.V1 {
		t.Errorf("ch0 and ch1 have identical vertical range %v/%v — depth-collapse bug is back", top.V0, top.V1)
	}
}

func TestBuildZonesRespectsConfiguredAxisRoles(t *testing.T) {
	// Forcing vertical back onto y (the old, buggy default) should reproduce
	// the collapse: every channel here has y=1.0, so every zone should land
	// on the same edge with the same V range.
	opts := DefaultZoneOpts("screen")
	opts.AxisVertical = AxisY
	opts.DepthSizeGain = 0 // isolate the vertical-axis collapse from depth-based size scaling
	zones := BuildZones(realPCChannels(), opts)

	first := zones[0]
	for _, z := range zones[1:] {
		if z.V0 != first.V0 || z.V1 != first.V1 {
			t.Errorf("with vertical=y all channels share y=1.0 and should collapse to the same V range; got %+v vs %+v", first, z)
		}
	}
}

func TestDepthSizeGainScalesRectSize(t *testing.T) {
	channels := []hue.EntertainmentChannel{
		{ChannelID: 0, Position: hue.Position{X: 0, Y: -1, Z: 0}}, // near (depth=-1 on y axis)
		{ChannelID: 1, Position: hue.Position{X: 0, Y: 1, Z: 0}},  // far (depth=+1 on y axis)
	}
	opts := ZoneOpts{
		Mode: ModeQuadrant, QuadrantSize: 0.2, DepthSizeGain: 1.0,
		AxisHorizontal: AxisX, AxisVertical: AxisZ, AxisDepth: AxisY,
	}
	zones := BuildZones(channels, opts)
	near := zoneByChannel(zones, 0)
	far := zoneByChannel(zones, 1)

	nearSize := near.U1 - near.U0
	farSize := far.U1 - far.U0
	if farSize <= nearSize {
		t.Fatalf("expected the channel further along the depth axis to sample a larger region: near=%.4f far=%.4f", nearSize, farSize)
	}
}
