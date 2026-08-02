package engine

import (
	"testing"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/pipeline"
)

func TestZoneOptsFromSettings(t *testing.T) {
	s := config.AreaSettings{
		Mode:             "quadrant",
		EdgeWidth:        0.21,
		QuadrantSize:     0.42,
		AxisHorizontal:   "y",
		AxisVertical:     "x",
		AxisDepth:        "z",
		InvertHorizontal: true,
		InvertVertical:   false,
		InvertDepth:      true,
		DepthSizeGain:    0.5,
		Feather:          0.07,
	}
	got := zoneOptsFromSettings(s)
	want := pipeline.ZoneOpts{
		Mode:             pipeline.SampleMode("quadrant"),
		EdgeWidth:        0.21,
		QuadrantSize:     0.42,
		AxisHorizontal:   pipeline.AxisY,
		AxisVertical:     pipeline.AxisX,
		AxisDepth:        pipeline.AxisZ,
		InvertHorizontal: true,
		InvertVertical:   false,
		InvertDepth:      true,
		DepthSizeGain:    0.5,
		Feather:          0.07,
	}
	if got != want {
		t.Errorf("zoneOptsFromSettings = %+v, want %+v", got, want)
	}
}

func TestZoneOptsFromSettingsDefaults(t *testing.T) {
	// Zero-valued settings must map to zero-valued opts, never to the
	// pipeline defaults — the defaults come from config, not from here.
	got := zoneOptsFromSettings(config.AreaSettings{})
	if got.Mode != "" || got.AxisHorizontal != "" || got.AxisVertical != "" || got.AxisDepth != "" {
		t.Errorf("zero settings must map to zero opts, got %+v", got)
	}
}

func TestColorSpaceFromString(t *testing.T) {
	tests := []struct {
		in   string
		want hue.ColorSpace
	}{
		{"xy", hue.ColorSpaceXY},
		{"rgb", hue.ColorSpaceRGB},
		{"", hue.ColorSpaceRGB},
		{"XY", hue.ColorSpaceRGB},  // exact match only
		{"xyz", hue.ColorSpaceRGB}, // not a prefix match
		{"anything", hue.ColorSpaceRGB},
	}
	for _, tt := range tests {
		if got := colorSpaceFromString(tt.in); got != tt.want {
			t.Errorf("colorSpaceFromString(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestNewEngine(t *testing.T) {
	bridge := config.Bridge{BridgeIP: "192.0.2.10", Username: "user", ClientKey: "aabb"}
	e := New(bridge, nil) // nil store must be tolerated (pre-pairing state)
	if e == nil {
		t.Fatal("New returned nil")
	}
	if e.client == nil {
		t.Fatal("client is nil")
	}
	if e.client.BridgeIP != bridge.BridgeIP {
		t.Errorf("client BridgeIP = %q, want %q", e.client.BridgeIP, bridge.BridgeIP)
	}
	if e.client.Username != bridge.Username {
		t.Errorf("client Username = %q, want %q", e.client.Username, bridge.Username)
	}
	if e.store != nil {
		t.Error("store must stay nil when nil was passed")
	}
	if e.smoother == nil {
		t.Error("smoother must always be created")
	}
}

func TestSnapshotOnFreshEngine(t *testing.T) {
	e := New(config.Bridge{}, nil)
	s := e.Snapshot()
	if s.BridgeConnected || s.StreamActive {
		t.Error("fresh engine must report disconnected and inactive")
	}
	if s.AreaID != "" || s.ChannelCount != 0 {
		t.Errorf("fresh engine area = %q, channels = %d, want empty/0", s.AreaID, s.ChannelCount)
	}
	if s.GridW != 0 || s.GridH != 0 {
		t.Errorf("fresh engine grid = %dx%d, want 0x0", s.GridW, s.GridH)
	}
	if s.Zones == nil {
		t.Error("Zones must be an empty slice, not nil (JSON consumers expect an array)")
	}
}

func TestUpdateSettingsWithoutActiveArea(t *testing.T) {
	// With no area selected, UpdateSettings must be a no-op — and with a nil
	// store it must not touch the store at all.
	e := New(config.Bridge{}, nil)
	e.UpdateSettings(config.DefaultAreaSettings("screen")) // must not panic
}

func TestSetFrameTracksGrid(t *testing.T) {
	e := New(config.Bridge{}, nil)
	g := &pipeline.Grid{W: 64, H: 36, Pix: make([]byte, 64*36*3)}
	e.SetFrame(g)
	s := e.Snapshot()
	if s.GridW != 64 || s.GridH != 36 {
		t.Errorf("snapshot grid = %dx%d, want 64x36", s.GridW, s.GridH)
	}
	if s.CaptureW != 64 || s.CaptureH != 36 {
		t.Errorf("capture size = %dx%d, want 64x36", s.CaptureW, s.CaptureH)
	}
}

func TestGridHelpers(t *testing.T) {
	if gridW(nil) != 0 || gridH(nil) != 0 {
		t.Error("nil grid must report 0x0")
	}
	g := &pipeline.Grid{W: 12, H: 7}
	if gridW(g) != 12 || gridH(g) != 7 {
		t.Errorf("gridW/gridH = %d/%d, want 12/7", gridW(g), gridH(g))
	}
}
