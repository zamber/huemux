package engine

import (
	"testing"
	"time"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/music"
	"github.com/zamber/huemux/internal/pipeline"
)

// TestMusicPipelineEndToEnd exercises the full audio→preset→output chain
// with synthetic frames: TestTone → State → musicRunner → tick → colors.
// Verifies that the pipeline produces non-zero output without real hardware.
func TestMusicPipelineEndToEnd(t *testing.T) {
	e := New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)

	// Simulate what the server does at pairing time: wire the music state
	// reader (dropping the frame-count from Snapshot's 3-tuple).
	state := music.New()
	e.SetMusicFrameSource(func() (music.Frame, bool) {
		f, ok, _ := state.Snapshot()
		return f, ok
	})

	// Use the synthetic tone source to feed frames into state.
	src := music.TestToneSource(100, 80)
	for i := 0; i < 60; i++ {
		f, _ := src()
		state.Update(f)
	}

	// Select audio mode and activate a preset (what the server's
	// autoActivateMusic does on first audio frame).
	e.SetCaptureMode(CaptureAudio)
	channels := map[string]uint8{"light-a": 1, "light-b": 2}
	if err := e.ActivateMusic("bass_pulse", channels, nil); err != nil {
		t.Fatalf("activate bass_pulse: %v", err)
	}

	if got := e.MusicPreset(); got != "bass_pulse" {
		t.Fatalf("MusicPreset = %q, want bass_pulse", got)
	}
	if got := e.CaptureMode(); got != CaptureAudio {
		t.Fatalf("CaptureMode = %q, want audio", got)
	}

	// With no real stream, tick() returns early at the stream==nil check —
	// this test verifies the routing logic, not the DTLS output. The
	// preset runner's Step() is what produces the colors; that is tested
	// in the preset package.
	//
	// What we CAN verify: the musicRunner is non-nil and the capture mode
	// routes correctly.
	e.mu.Lock()
	runner := e.musicRunner
	mode := e.captureMode
	e.mu.Unlock()

	if runner == nil {
		t.Fatal("musicRunner is nil after ActivateMusic — preset not activated")
	}
	if mode != CaptureAudio {
		t.Fatalf("captureMode = %q, want audio", mode)
	}

	// Exercise Step() with the synthetic audio source — this is the
	// same call tick() makes.
	colors := runner.Step(time.Now())
	if len(colors) == 0 {
		t.Fatal("music runner Step() returned no colors — expected output for 2 channels")
	}
	// At least one channel should have non-zero output.
	hasOutput := false
	for _, c := range colors {
		if c.R > 0 || c.G > 0 || c.B > 0 {
			hasOutput = true
			break
		}
	}
	if !hasOutput {
		t.Error("all channel colors are zero — music pipeline produced no visible output")
	}
}

// TestEmptyGridInAudioMode verifies that when capture mode is audio and no
// grid exists, the engine routes to the music preset (or keepalive) rather
// than crashing on a nil grid dereference.
func TestEmptyGridInAudioMode(t *testing.T) {
	e := New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	e.SetCaptureMode(CaptureAudio)

	// No grid, no stream → tick returns early. This test proves the nil
	// grid doesn't panic in the capture-mode routing.
	e.mu.Lock()
	e.grid = nil
	e.mu.Unlock()

	// Call sampleGridLocked with nil grid — it returns nil safely.
	raw := e.sampleGridLocked(nil, pipeline.LetterboxMask{}, nil)
	if raw != nil {
		t.Errorf("sampleGridLocked(nil) = %v, want nil", raw)
	}

	// neutralFrameLocked should return nil for empty zones.
	neut := e.neutralFrameLocked(nil)
	if neut != nil {
		t.Errorf("neutralFrameLocked(nil) = %v, want nil", neut)
	}

	// neutralFrameLocked with zones should return dim values.
	zones := []pipeline.Zone{
		{ChannelID: 1},
		{ChannelID: 2},
	}
	neut2 := e.neutralFrameLocked(zones)
	if len(neut2) != 2 {
		t.Fatalf("neutralFrameLocked with 2 zones returned %d entries", len(neut2))
	}
	for _, c := range neut2 {
		if c.R <= 0 || c.G <= 0 || c.B <= 0 {
			t.Error("neutral frame has zero channel — keepalive requires non-zero output")
		}
	}
}
