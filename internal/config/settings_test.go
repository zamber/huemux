package config

import "testing"

func TestAreaSettingsValidate_OutputHz(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero", 0, 20},
		{"negative", -1, 20},
		{"tooLarge", 2000000000, 20},
		{"justAboveMax", 26, 20},
		{"atMax", 25, 25},
		{"atMin", 1, 1},
		{"inRange", 20, 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := AreaSettings{OutputHz: tt.in}
			got := s.Validate().OutputHz
			if got != tt.want {
				t.Errorf("OutputHz = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAreaSettingsValidate_DebugHz(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"zero", 0, 10},
		{"negative", -5, 10},
		{"tooLarge", 31, 10},
		{"justAboveMax", 31, 10},
		{"atMax", 30, 30},
		{"atMin", 1, 1},
		{"inRange", 15, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := AreaSettings{DebugHz: tt.in}
			got := s.Validate().DebugHz
			if got != tt.want {
				t.Errorf("DebugHz = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAreaSettingsValidate_AudioGain(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero", 0, 2.0},
		{"negative", -1, 2.0},
		{"belowMin", 0.49, 2.0},
		{"aboveMax", 5.1, 2.0},
		{"atMin", 0.5, 0.5},
		{"atMax", 5, 5},
		{"inRange", 2.5, 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := AreaSettings{AudioGain: tt.in}
			got := s.Validate().AudioGain
			if got != tt.want {
				t.Errorf("AudioGain = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAreaSettingsValidate_AudioFloor(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"negative", -0.5, 0},
		{"tooLarge", 0.2, 0},
		{"justAboveMax", 0.101, 0},
		{"atMax", 0.1, 0.1},
		{"atMin", 0, 0},
		{"inRange", 0.05, 0.05},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := AreaSettings{AudioFloor: tt.in}
			got := s.Validate().AudioFloor
			if got != tt.want {
				t.Errorf("AudioFloor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBackfillDefaultsMigratesNewFields(t *testing.T) {
	// A record saved before the debug fields existed decodes with them at
	// zero. Backfill must fill them with the non-zero defaults (DebugHz,
	// AudioGain) and leave the zero-is-default fields alone.
	v := backfillDefaults(AreaSettings{AxisHorizontal: "x"}, "screen")
	d := DefaultAreaSettings("screen")
	if v.DebugHz != d.DebugHz {
		t.Errorf("DebugHz = %d after backfill, want %d", v.DebugHz, d.DebugHz)
	}
	if v.AudioGain != d.AudioGain {
		t.Errorf("AudioGain = %v after backfill, want %v", v.AudioGain, d.AudioGain)
	}
	if v.AudioFloor != 0 {
		t.Errorf("AudioFloor = %v after backfill, want 0", v.AudioFloor)
	}
	if v.DebugPreview {
		t.Error("DebugPreview = true after backfill, want false")
	}
	// Existing axis choices must survive the migration.
	if v.AxisHorizontal != "x" {
		t.Errorf("AxisHorizontal = %q after backfill, want existing x", v.AxisHorizontal)
	}
}

func TestBackfillDefaultsMigratesBlackCutoff(t *testing.T) {
	// 0.02 was the old default; it must become 0. A deliberate non-default
	// value survives.
	if got := backfillDefaults(AreaSettings{BlackCutoff: 0.02}, "screen").BlackCutoff; got != 0 {
		t.Errorf("BlackCutoff = %v after backfill, want 0", got)
	}
	if got := backfillDefaults(AreaSettings{BlackCutoff: 0.1}, "screen").BlackCutoff; got != 0.1 {
		t.Errorf("BlackCutoff = %v after backfill, want 0.1", got)
	}
	if got := backfillDefaults(AreaSettings{BlackCutoff: 0}, "screen").BlackCutoff; got != 0 {
		t.Errorf("BlackCutoff = %v after backfill, want 0", got)
	}
}

func TestBackfillDefaultsFillsLegacyAxisFields(t *testing.T) {
	// Records from before the axis-mapping schema still get the axis fill;
	// the debug fields get filled in the same pass.
	v := backfillDefaults(AreaSettings{}, "screen")
	d := DefaultAreaSettings("screen")
	if v.AxisHorizontal != d.AxisHorizontal || v.AxisVertical != d.AxisVertical || v.AxisDepth != d.AxisDepth {
		t.Errorf("axis fields not filled: %+v", v)
	}
	if v.DebugHz != d.DebugHz || v.AudioGain != d.AudioGain {
		t.Errorf("debug fields not filled: %+v", v)
	}
}
