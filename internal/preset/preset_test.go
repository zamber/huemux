package preset

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/music"
	"github.com/zamber/huemux/internal/pipeline"
)

// testChannels is a two-light area for runner tests.
func testChannels() (map[string]uint8, map[string]Pos3) {
	return map[string]uint8{"light-a": 1, "light-b": 2},
		map[string]Pos3{"light-a": {X: -1, Y: 0, Z: 0}, "light-b": {X: 1, Y: 0, Z: 0}}
}

// frame builds an audio frame with the given FFT band magnitudes.
func frame(bands ...float32) music.Frame {
	var f music.Frame
	for i, v := range bands {
		if i >= len(f.FFT) {
			break
		}
		f.FFT[i] = v
	}
	return f
}

func TestParseRejectsMalformed(t *testing.T) {
	cases := []struct {
		name, json, wantErr string
	}{
		{"version", `{"version": 2, "name": "x", "nodes": []}`, "version"},
		{"no name", `{"version": 1, "nodes": []}`, "name"},
		{"no nodes", `{"version": 1, "name": "x", "nodes": []}`, "no nodes"},
		{"unknown type", `{"version": 1, "name": "x", "nodes": [{"id": "a", "type": "nope"}]}`, "unknown primitive"},
		{"edge from unknown", `{"version": 1, "name": "x", "nodes": [{"id": "a", "type": "all_lights"}], "edges": [{"from": "z", "to": "a"}]}`, "unknown node"},
		{"edge to unknown", `{"version": 1, "name": "x", "nodes": [{"id": "a", "type": "all_lights"}], "edges": [{"from": "a", "to": "z"}]}`, "unknown node"},
		{"duplicate id", `{"version": 1, "name": "x", "nodes": [{"id": "a", "type": "all_lights"}, {"id": "a", "type": "all_lights"}]}`, "duplicate"},
	}
	for _, c := range cases {
		if _, err := Parse([]byte(c.json)); err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want contains %q", c.name, err, c.wantErr)
		}
	}
}

func TestParseRejectsCycle(t *testing.T) {
	// a -> b -> a is a cycle.
	data := `{"version": 1, "name": "x", "nodes": [
		{"id": "a", "type": "all_lights"}, {"id": "b", "type": "all_lights"}],
		"edges": [{"from": "a", "to": "b"}, {"from": "b", "to": "a"}]}`
	if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle not rejected: %v", err)
	}
}

func TestBuiltinPresetsParse(t *testing.T) {
	slugs := BuiltinSlugs()
	if len(slugs) == 0 {
		t.Fatal("no built-in presets embedded")
	}
	for _, slug := range slugs {
		p, err := Builtin(slug)
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if p.Name == "" {
			t.Fatalf("%s: empty name", slug)
		}
	}
}

// TestBassPulseLightsFromBass is the phase-2 exit test in miniature: bass
// energy drives brightness through the whole graph to per-channel colors.
func TestBassPulseLightsFromBass(t *testing.T) {
	channels, pos := testChannels()
	p, err := Builtin("bass_pulse")
	if err != nil {
		t.Fatal(err)
	}
	var current music.Frame
	r := NewRunner(p, channels, pos, func() (music.Frame, bool) {
		return current, true
	})

	// Loud bass, quiet treble: band 8 (~110 Hz) full-scale, others near 0.
	current = frame(0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	out := r.Step(time.Unix(1_700_000_000, 0))

	if len(out) != 2 {
		t.Fatalf("expected both channels lit, got %v", out)
	}
	for id := range out {
		c := out[id]
		if c.R != c.G || c.G != c.B {
			t.Fatalf("ch %d: bass brightness must be white, got %+v", id, c)
		}
		if c.R < 0.03 || c.R > 1.01 {
			t.Fatalf("ch %d: brightness %v, want ~0.06 (exp curve, min 0.05)", id, c.R)
		}
	}
}

// TestRunnerRespectsChannelMap drops lights not in the area.
func TestRunnerRespectsChannelMap(t *testing.T) {
	channels := map[string]uint8{"light-a": 1}
	p, err := Builtin("bass_pulse")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(p, channels, nil, func() (music.Frame, bool) {
		return frame(0, 0, 0, 0, 0, 0, 0, 0, 1), true
	})
	out := r.Step(time.Unix(1_700_000_000, 0))
	if len(out) != 1 {
		t.Fatalf("expected only channel 1, got %v", out)
	}
	if _, ok := out[2]; ok {
		t.Fatal("light-b has no channel but was painted")
	}
}

// TestStrobeFlashComposesOverBrightness: the strobe's color × value must
// multiply with the routing node's brightness.
func TestStrobeFlashComposesOverBrightness(t *testing.T) {
	channels, pos := testChannels()
	p, err := Builtin("bass_pulse")
	if err != nil {
		t.Fatal(err)
	}
	var current music.Frame
	r := NewRunner(p, channels, pos, func() (music.Frame, bool) {
		return current, true
	})

	// Pre-warm the beat detector with a constant floor so the variance is
	// low, then a full-spectrum spike that must register as a beat.
	current = frame(0.05)
	for i := 0; i < 45; i++ {
		r.Step(time.Unix(1_700_000_000, 0).Add(time.Duration(i) * 40 * time.Millisecond))
	}
	current = music.Frame{}
	for i := range current.FFT {
		current.FFT[i] = 0.5
	}
	now := time.Unix(1_700_000_000, 0).Add(45 * 40 * time.Millisecond)
	var out map[uint8]pipeline.LinearColor
	out = r.Step(now) // tick to trigger beat detection

	// On the beat, strobe envelope = 1; the flash color #FF6600 (linear
	// space) × brightness. The reactivity_effect output node smooths the
	// brightness bus with reactivity=45, so the first tick after a spike
	// is attenuated — check several ticks for the peak.
	var peak pipeline.LinearColor
	for i := 0; i < 8; i++ {
		out = r.Step(now.Add(time.Duration(i) * 40 * time.Millisecond))
		c := out[1]
		if c.R > peak.R {
			peak = c
		}
	}
	if peak.R <= peak.G || peak.G <= peak.B {
		t.Fatalf("strobe color not orange on beat: %+v", peak)
	}
	if peak.R < 0.1 {
		t.Fatalf("strobe too dim on beat: %+v", peak)
	}

	// After the flash decays (duration 250ms, exp decay), the strobe lights
	// must fall back to the bass brightness chain — white (equal components),
	// not orange and not black: the strobe owns only the color bus. The
	// audio returns to the floor after the spike, so the detector goes quiet
	// and the strobe envelope decays.
	current = frame(0.05)
	decayed := false
	var last pipeline.LinearColor
	for i := 1; i <= 15; i++ {
		out = r.Step(now.Add(time.Duration(i) * 40 * time.Millisecond))
		last = out[1]
		if last.R <= last.G*1.01 && last.G <= last.B*1.01 {
			decayed = true
			break
		}
	}
	if !decayed {
		t.Fatalf("strobe never returned to white: %+v", last)
	}
}

// TestChillAmbientCyclesWithoutStrobe: LFO drives palette cycling; the color
// must change over time while staying smooth (no hard steps in the channel
// bus, no lights off).
func TestChillAmbientCycles(t *testing.T) {
	channels, pos := testChannels()
	p, err := Builtin("chill_ambient")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(p, channels, pos, nil) // no audio source: works from LFO alone

	now := time.Unix(1_700_000_000, 0)
	first := r.Step(now)
	if len(first) != 2 {
		t.Fatalf("ambient must light every channel, got %v", first)
	}
	// 2 s later the LFO (0.08 Hz) has moved a third of a cycle: the color
	// must differ.
	later := r.Step(now.Add(2 * time.Second))
	if first[1] == later[1] {
		t.Fatalf("ambient color did not cycle: %v", first[1])
	}
	// Everything is always lit and never clipped.
	for id := range first {
		c := later[id]
		if c.R < 0 || c.R > 1 || c.G < 0 || c.G > 1 || c.B < 0 || c.B > 1 {
			t.Fatalf("ch %d out of range: %+v", id, c)
		}
		if c.R == 0 && c.G == 0 && c.B == 0 {
			t.Fatalf("ch %d went black mid-cycle", id)
		}
	}
}

// TestNoAudioMeansDark: with no frame source, analysis outputs are 0 and
// the brightness floor (0.05 by design) is the only thing painted — a
// barely-visible dark, never a lit room.
func TestNoAudioMeansDark(t *testing.T) {
	channels, _ := testChannels()
	p, err := Builtin("bass_pulse")
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(p, channels, nil, func() (music.Frame, bool) {
		return music.Frame{}, false
	})
	out := r.Step(time.Unix(1_700_000_000, 0))
	if len(out) == 0 {
		t.Fatalf("preset painted nothing at all: %v", out)
	}
	for _, c := range out {
		if c.R > 0.1 {
			t.Fatalf("silence painted a lit room: %+v", c)
		}
	}
}

func TestRunnerColorTimesValue(t *testing.T) {
	// light-a: color (1, 0.5, 0) from a mid-band drive, no value edge → the
	// runner treats color-only selection as full brightness. light-b: a
	// terminal with no inputs at all selects nothing.
	p, err := Parse([]byte(`{"version": 1, "name": "mix", "nodes": [
		{"id": "src", "type": "mic_capture", "params": {}},
		{"id": "bands", "type": "freq_bands", "params": {}},
		{"id": "c", "type": "color_map_frequency", "params": {"bass_color": "#FFFFFF", "mid_color": "#FF8000", "treble_color": "#000000"}},
		{"id": "g1", "type": "light_group", "params": {"light_ids": ["light-a"]}},
		{"id": "g2", "type": "light_group", "params": {"light_ids": ["light-b"]}}],
		"edges": [
			{"from": "src", "to": "bands"},
			{"from": "bands", "out_port": "mid", "to": "c", "in_port": "mid"},
			{"from": "c", "out_port": "r", "to": "g1", "in_port": "r"},
			{"from": "c", "out_port": "g", "to": "g1", "in_port": "g"},
			{"from": "c", "out_port": "b", "to": "g1", "in_port": "b"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	channels := map[string]uint8{"light-a": 1, "light-b": 2}
	// Full mid band only (bands 10-23): the mid color must win.
	var mid music.Frame
	for i := 10; i < 24; i++ {
		mid.FFT[i] = 1
	}

	r := NewRunner(p, channels, nil, func() (music.Frame, bool) { return mid, true })
	out := r.Step(time.Unix(1_700_000_000, 0))
	// #FF8000 in linear space: G = srgbToLinear(0x80) ≈ 0.216.
	if out[1].R != 1 || math.Abs(out[1].G-0.216) > 0.01 || out[1].B != 0 {
		t.Fatalf("light-a color wrong: %+v", out[1])
	}
	if _, ok := out[2]; ok {
		t.Fatalf("unwired terminal selected light-b: %+v", out[2])
	}
}
