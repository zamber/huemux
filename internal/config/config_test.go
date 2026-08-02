package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultAreaSettings(t *testing.T) {
	tests := []struct {
		configurationType string
		wantMode          string
	}{
		{"screen", "edges"},
		{"monitor", "edges"},
		{"music", "spread"},
		{"3dspace", "spread"},
		{"", "spread"},
	}
	for _, tt := range tests {
		d := DefaultAreaSettings(tt.configurationType)
		if d.Mode != tt.wantMode {
			t.Errorf("DefaultAreaSettings(%q).Mode = %q, want %q", tt.configurationType, d.Mode, tt.wantMode)
		}
	}
}

func TestDefaultAreaSettingsFull(t *testing.T) {
	d := DefaultAreaSettings("screen")
	want := AreaSettings{
		Mode:                "edges",
		EdgeWidth:           0.15,
		QuadrantSize:        0.35,
		Feather:             0.04,
		Letterbox:           true,
		AxisHorizontal:      "x",
		AxisVertical:        "z",
		AxisDepth:           "y",
		InvertHorizontal:    false,
		InvertVertical:      true,
		InvertDepth:         false,
		DepthSizeGain:       0.3,
		Reactivity:          45,
		Brightness:          100,
		Saturation:          130,
		BlackCutoff:         0.02,
		ChannelBrightness:   map[uint8]float64{},
		SceneCutSensitivity: 0.35,
		CaptureWidth:        320,
		CaptureHeight:       180,
		CaptureFPS:          30,
		OutputHz:            20,
		ColorSpace:          "rgb",
		DisableEMS:          false,
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("DefaultAreaSettings(\"screen\") = %+v, want %+v", d, want)
	}
}

func TestBackfillDefaultsComplete(t *testing.T) {
	// A record that already carries the axis fields must pass through
	// untouched, even if its values differ from today's defaults.
	v := AreaSettings{
		Mode:              "spread",
		AxisHorizontal:    "y",
		AxisVertical:      "x",
		AxisDepth:         "z",
		InvertVertical:    false,
		DepthSizeGain:     0.0,
		Reactivity:        77,
		ChannelBrightness: map[uint8]float64{1: 0.5},
	}
	got := backfillDefaults(v, "screen")
	if !reflect.DeepEqual(got, v) {
		t.Errorf("complete record must pass through unchanged, got %+v", got)
	}
}

func TestBackfillDefaultsLegacy(t *testing.T) {
	// A pre-axis-fields record decodes with AxisHorizontal == ""; backfill
	// must fill the six axis fields from defaults while preserving everything
	// else, including the Mode.
	v := AreaSettings{
		Mode:       "spread",
		Reactivity: 77,
		Brightness: 50,
	}
	got := backfillDefaults(v, "screen")
	if got.AxisHorizontal != "x" || got.AxisVertical != "z" || got.AxisDepth != "y" {
		t.Errorf("axis fields = %q/%q/%q, want x/z/y", got.AxisHorizontal, got.AxisVertical, got.AxisDepth)
	}
	if !got.InvertVertical {
		t.Error("InvertVertical must backfill to true")
	}
	if got.DepthSizeGain != 0.3 {
		t.Errorf("DepthSizeGain = %v, want 0.3", got.DepthSizeGain)
	}
	if got.Mode != "spread" {
		t.Errorf("Mode must be preserved, got %q", got.Mode)
	}
	if got.Reactivity != 77 || got.Brightness != 50 {
		t.Errorf("user values must be preserved, got reactivity %v brightness %v", got.Reactivity, got.Brightness)
	}
}

func TestBackfillDefaultsLegacyIsIdempotent(t *testing.T) {
	v := AreaSettings{Mode: "edges"}
	once := backfillDefaults(v, "music")
	twice := backfillDefaults(once, "music")
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("backfill must be idempotent: %+v vs %+v", once, twice)
	}
}

func TestStoreGetUnknownAreaReturnsDefaults(t *testing.T) {
	s := &Store{byArea: map[string]AreaSettings{}}
	for _, ct := range []string{"screen", "music"} {
		got := s.Get("no-such-area", ct)
		want := DefaultAreaSettings(ct)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Get(%q) = %+v, want defaults %+v", ct, got, want)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	SetDir(t.TempDir())
	defer SetDir("")

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	areaID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	s := DefaultAreaSettings("screen")
	s.Reactivity = 77
	s.ColorSpace = "xy"
	s.ChannelBrightness = map[uint8]float64{3: 0.9, 5: 1.1}
	store.Set(areaID, s)
	store.Flush()

	reloaded, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore after flush: %v", err)
	}
	got := reloaded.Get(areaID, "screen")
	if !reflect.DeepEqual(got, s) {
		t.Errorf("round trip changed settings:\n got %+v\nwant %+v", got, s)
	}
}

func TestStoreCorruptFileStartsFresh(t *testing.T) {
	dir := t.TempDir()
	SetDir(dir)
	defer SetDir("")

	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore must tolerate a corrupt settings file, got %v", err)
	}
	got := store.Get("any", "screen")
	if !reflect.DeepEqual(got, DefaultAreaSettings("screen")) {
		t.Errorf("corrupt file: got %+v, want defaults", got)
	}
}

func TestSaveBridgeRoundTrip(t *testing.T) {
	SetDir(t.TempDir())
	defer SetDir("")

	b := Bridge{BridgeIP: "192.168.1.240", BridgeID: "abc123", Username: "user", ClientKey: "aabbccdd"}
	if err := SaveBridge(b); err != nil {
		t.Fatalf("SaveBridge: %v", err)
	}
	got, err := LoadBridge()
	if err != nil {
		t.Fatalf("LoadBridge: %v", err)
	}
	if got != b {
		t.Errorf("bridge round trip = %+v, want %+v", got, b)
	}
}
