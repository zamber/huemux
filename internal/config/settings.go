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
	InvertDepth  bool    `json:"invert_depth"`
	Feather      float64 `json:"feather"`
	Letterbox    bool    `json:"letterbox"`

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
		InvertDepth:         false,
		Feather:             0.04,
		Letterbox:           true,
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
		return v
	}
	return DefaultAreaSettings(configurationType)
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
