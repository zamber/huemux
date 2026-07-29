package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// AreaSettings is every tunable in Milestone 8, kept per entertainment area
// so that switching rooms restores that room's tuning instead of bleeding
// settings across them.
type AreaSettings struct {
	// Sampling
	Mode         string  `json:"mode"` // edges | quadrant | global | spread
	EdgeWidth    float64 `json:"edge_width"`
	QuadrantSize float64 `json:"quadrant_size"`
	Feather      float64 `json:"feather"`
	Letterbox    bool    `json:"letterbox"`

	// Zone mapping: which physical axis (x/y/z) plays which role. See
	// pipeline.ZoneOpts for why this is configurable rather than assumed —
	// real areas disagree about which axis carries useful information.
	AxisHorizontal   string  `json:"axis_horizontal"` // x | y | z -> screen U
	AxisVertical     string  `json:"axis_vertical"`   // x | y | z -> screen V
	AxisDepth        string  `json:"axis_depth"`      // x | y | z -> sample-size modifier
	InvertHorizontal bool    `json:"invert_horizontal"`
	InvertVertical   bool    `json:"invert_vertical"`
	InvertDepth      bool    `json:"invert_depth"`
	DepthSizeGain    float64 `json:"depth_size_gain"` // 0 = disabled

	// Primary
	Reactivity float64 `json:"reactivity"` // 0-100
	Brightness float64 `json:"brightness"` // 0-200 (%)
	Saturation float64 `json:"saturation"` // 0-200 (%)

	// Output shaping
	BlackCutoff         float64           `json:"black_cutoff"` // 0-1, linear
	ChannelBrightness   map[uint8]float64 `json:"channel_brightness,omitempty"`
	SceneCutSensitivity float64           `json:"scene_cut_sensitivity"`

	// Advanced
	CaptureWidth  int    `json:"capture_width"`
	CaptureHeight int    `json:"capture_height"`
	CaptureFPS    int    `json:"capture_fps"`
	OutputHz      int    `json:"output_hz"`
	ColorSpace    string `json:"color_space"` // rgb | xy
	DisableEMS    bool   `json:"disable_ems"`
}

// DefaultAreaSettings returns the roadmap's stated defaults for a freshly
// selected area of the given configuration_type.
func DefaultAreaSettings(configurationType string) AreaSettings {
	mode := "spread"
	if configurationType == "screen" || configurationType == "monitor" {
		mode = "edges"
	}
	return AreaSettings{
		Mode:                mode,
		EdgeWidth:           0.15,
		QuadrantSize:        0.35,
		Feather:             0.04,
		Letterbox:           true,
		AxisHorizontal:      "x",
		AxisVertical:        "z",
		AxisDepth:           "y",
		InvertHorizontal:    false,
		InvertVertical:      true, // see pipeline.DefaultZoneOpts: z is physical-up, screen-V is image-down
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
}

// Store persists per-area settings to a single JSON file, debounced so a
// slider being dragged doesn't turn into a filesystem write per frame.
type Store struct {
	path string

	mu       sync.Mutex
	byArea   map[string]AreaSettings
	timer    *time.Timer
	debounce time.Duration
}

// NewStore loads (or initializes) the settings file next to the bridge
// config.
func NewStore() (*Store, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	s := &Store{
		path:     filepath.Join(dir, "settings.json"),
		byArea:   map[string]AreaSettings{},
		debounce: 500 * time.Millisecond,
	}
	if raw, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(raw, &s.byArea) // corrupt settings file: start fresh rather than fail to start
	}
	return s, nil
}

// Get returns the stored settings for an area, or sensible defaults if none
// have been saved yet.
func (s *Store) Get(areaID, configurationType string) AreaSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.byArea[areaID]; ok {
		return backfillDefaults(v, configurationType)
	}
	return DefaultAreaSettings(configurationType)
}

// backfillDefaults fills in fields that are still at their Go zero value,
// which for settings saved before those fields existed means "missing," not
// "deliberately zero." Without this, a settings.json written before the
// axis-mapping fields existed would decode with AxisVertical == "" and
// silently degrade zone mapping instead of falling back to the same
// defaults a fresh area gets — an on-disk schema evolving out from under a
// still-valid file is exactly the kind of thing that fails silently.
func backfillDefaults(v AreaSettings, configurationType string) AreaSettings {
	if v.AxisHorizontal != "" {
		return v // has axis fields already; assume the rest of the record is current too
	}
	d := DefaultAreaSettings(configurationType)
	v.AxisHorizontal = d.AxisHorizontal
	v.AxisVertical = d.AxisVertical
	v.AxisDepth = d.AxisDepth
	v.InvertVertical = d.InvertVertical
	v.DepthSizeGain = d.DepthSizeGain
	return v
}

// Set updates the in-memory settings for an area and schedules a debounced
// write to disk.
func (s *Store) Set(areaID string, settings AreaSettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byArea[areaID] = settings
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(s.debounce, s.flush)
}

// Flush forces an immediate write, e.g. on clean shutdown.
func (s *Store) Flush() {
	s.flush()
}

func (s *Store) flush() {
	s.mu.Lock()
	raw, err := json.MarshalIndent(s.byArea, "", "  ")
	path := s.path
	s.mu.Unlock()
	if err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o600)
}
