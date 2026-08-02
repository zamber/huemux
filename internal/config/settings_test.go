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
