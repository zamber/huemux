package engine

import (
	"testing"

	"github.com/zamber/huemux/internal/config"
)

// TestActivateMusicLifecycle covers the integration seams between the
// server layer and the output loop: preset activation by slug, the
// deactivate path, and error handling for unknown slugs. The tick itself is
// exercised by the preset package's runner tests; driving a real tick needs
// a bridge stream, which is what the E2E check does.
func TestActivateMusicLifecycle(t *testing.T) {
	e := New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	if got := e.MusicPreset(); got != "" {
		t.Fatalf("fresh engine has music preset %q", got)
	}

	// Unknown slug is a loud error, not a silent no-op.
	if err := e.ActivateMusic("no_such_preset", nil, nil); err == nil {
		t.Fatal("unknown preset slug accepted")
	}
	if got := e.MusicPreset(); got != "" {
		t.Fatalf("failed activation set a preset: %q", got)
	}

	channels := map[string]uint8{"light-a": 1}
	if err := e.ActivateMusic("bass_pulse", channels, nil); err != nil {
		t.Fatalf("activate bass_pulse: %v", err)
	}
	if got := e.MusicPreset(); got != "bass_pulse" {
		t.Fatalf("MusicPreset = %q, want %q", got, "bass_pulse")
	}

	if err := e.ActivateMusic("", nil, nil); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got := e.MusicPreset(); got != "" {
		t.Fatalf("preset survived deactivation: %q", got)
	}
}

// TestSetMusicFrameSourceNilSafe: wiring a nil source (or none at all) must
// not panic — presets run on silence, painting the brightness floor.
func TestSetMusicFrameSourceNilSafe(t *testing.T) {
	e := New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	e.SetMusicFrameSource(nil) // must not panic
	if err := e.ActivateMusic("bass_pulse", nil, nil); err != nil {
		t.Fatalf("activate with nil source: %v", err)
	}
}
