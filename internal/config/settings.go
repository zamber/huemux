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

	// Debug
	DebugHz      int     `json:"debug_hz"`      // debug-data push rate (fps/capture/histogram)
	DebugPreview bool    `json:"debug_preview"` // stream echo to connected UIs (resource burn)
	VideoSync    bool    `json:"video_sync"`    // when false, skip video sampling (grid is ignored)
	AudioGain    float64 `json:"audio_gain"`    // FFT band boost for the analysis frame
	AudioFloor   float64 `json:"audio_floor"`   // FFT bands below this are silenced
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
		Reactivity:          100, // pass-through: artistic smoothing moved into preset graph
		Brightness:          100, // pass-through
		Saturation:          100, // pass-through
		BlackCutoff:         0,
		ChannelBrightness:   map[uint8]float64{},
		SceneCutSensitivity: 0.35,
		CaptureWidth:        320,
		CaptureHeight:       180,
		CaptureFPS:          30,
		OutputHz:            20,
		ColorSpace:          "rgb",
		DisableEMS:          false,
		DebugHz:             20,
		DebugPreview:        false,
		VideoSync:           true,
		AudioGain:           10.0,
		AudioFloor:          0,
	}
}

// Validate clamps fields to safe ranges and returns the (possibly modified)
// settings. WebSocket settings arrive from the browser without any client-side
// authority, so anything that would feed a hardware rate or allocation must be
// bounded here.
func (s AreaSettings) Validate() AreaSettings {
	if s.OutputHz < 1 || s.OutputHz > 25 {
		s.OutputHz = 20
	}
	if s.DebugHz < 1 || s.DebugHz > 30 {
		s.DebugHz = 20
	}
	if s.AudioGain < 0.5 || s.AudioGain > 10 {
		s.AudioGain = 10.0
	}
	if s.AudioFloor < 0 || s.AudioFloor > 0.1 {
		s.AudioFloor = 0
	}
	return s
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
	d := DefaultAreaSettings(configurationType)
	// Axis fields: only filled for records saved before they existed (the
	// legacy pre-mapping schema). A record that already carries axis choices
	// keeps them — a user's inverted axes are not something to overwrite.
	if v.AxisHorizontal == "" {
		v.AxisHorizontal = d.AxisHorizontal
		v.AxisVertical = d.AxisVertical
		v.AxisDepth = d.AxisDepth
		v.InvertVertical = d.InvertVertical
		v.DepthSizeGain = d.DepthSizeGain
	}
	// BlackCutoff's old default was 0.02; the new default is 0. A saved 0.02
	// is the pre-migration default, not a deliberate choice, so move it to 0.
	// Anything else the user actually set survives.
	if v.BlackCutoff == 0.02 {
		v.BlackCutoff = 0
	}
	// Debug fields added after the early return above existed: a zero here
	// means the record predates them, not that the user chose zero (both
	// DebugHz and AudioGain clamp to a non-zero default in Validate, so a
	// genuine user choice of zero is indistinguishable from missing — the
	// non-zero defaults are the safe fill). AudioFloor == 0 and
	// DebugPreview == false are already the defaults, so zero-filling them is
	// idempotent and needs no sentinel.
	if v.DebugHz == 0 {
		v.DebugHz = d.DebugHz
		v.VideoSync = d.VideoSync // field predates the record
	}
	if v.AudioGain == 0 {
		v.AudioGain = d.AudioGain
	}
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
