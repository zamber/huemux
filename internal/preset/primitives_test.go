package preset

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/music"
)

func newPrim(t *testing.T, name string, params any) Primitive {
	t.Helper()
	p, err := New(name)
	if err != nil {
		t.Fatal(err)
	}
	var raw json.RawMessage
	if params != nil {
		raw, err = json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Init(raw); err != nil {
		t.Fatalf("%s init: %v", name, err)
	}
	return p
}

func testEnv(now time.Time) *Env {
	return &Env{
		In:        map[string]float64{},
		Out:       map[string]float64{},
		Lights:    map[string]float64{},
		Colors:    map[string]Color{},
		AllLights: []string{"light-a", "light-b"},
		Pos:       map[string]Pos3{"light-a": {X: -1}, "light-b": {X: 1}},
		Now:       now,
	}
}

func TestBeatDetectorFiresAndEstimatesBPM(t *testing.T) {
	bd := newPrim(t, "beat_detector", nil)
	now := time.Unix(1_700_000_000, 0)

	// Constant floor: variance ~0, no beats.
	floor := music.Frame{}
	for i := range floor.FFT {
		floor.FFT[i] = 0.02
	}
	for i := 0; i < 50; i++ {
		e := testEnv(now.Add(time.Duration(i) * 33 * time.Millisecond))
		e.Frame, e.HasFFT = floor, true
		bd.Process(e)
		if e.Out["beat"] != 0 {
			t.Fatalf("constant floor produced a beat at tick %d", i)
		}
	}

	// A 0.5 spike every 15 frames (~2 Hz → 30 BPM) must fire every time.
	var spike music.Frame
	for i := range spike.FFT {
		spike.FFT[i] = 0.5
	}
	beats := 0
	for i := 0; i < 60; i++ {
		e := testEnv(now.Add(time.Duration(i) * 33 * time.Millisecond))
		e.Frame, e.HasFFT = floor, true
		if i%15 == 0 {
			e.Frame = spike
		}
		bd.Process(e)
		if e.Out["beat"] == 1 {
			beats++
		}
	}
	// 4 spikes (i=0,15,30,45) + holds: at least 4 beat outputs.
	if beats < 4 {
		t.Fatalf("expected ≥4 beat outputs, got %d", beats)
	}
	// Interval 15 frames × 33ms ≈ 495ms → BPM ≈ 121.
	if bpm := bd.(*beatDetector).bpm; bpm < 100 || bpm > 140 {
		t.Fatalf("bpm = %v, want ~121", bpm)
	}
}

func TestFreqBandsSplit(t *testing.T) {
	fb := newPrim(t, "freq_bands", map[string]float64{"bass_cutoff": 200, "treble_cutoff": 4000}).(*freqBands)
	if fb.bassBand < 8 || fb.bassBand > 12 {
		t.Fatalf("bass band = %d, want ~10 for 200 Hz", fb.bassBand)
	}
	if fb.trebleBand < 22 || fb.trebleBand > 26 {
		t.Fatalf("treble band = %d, want ~24 for 4 kHz", fb.trebleBand)
	}

	e := testEnv(time.Now())
	e.Frame, e.HasFFT = music.Frame{}, true
	for i := range e.Frame.FFT {
		e.Frame.FFT[i] = 0
	}
	for i := 0; i < fb.bassBand; i++ {
		e.Frame.FFT[i] = 0.8 // pure bass
	}
	fb.Process(e)
	if e.Out["bass"] < 0.7 || e.Out["mid"] != 0 || e.Out["treble"] != 0 {
		t.Fatalf("bass-only frame split wrong: %+v", e.Out)
	}
}

func TestBrightnessCurves(t *testing.T) {
	for _, tc := range []struct {
		curve  string
		energy float64
		want   float64
	}{
		{"lin", 0.5, 0.5},    // min 0, max 1
		{"exp", 0.5, 0.25},   // exp(0.5) = e²
		{"log", 0.1, 0.2788}, // log(1+9·0.1)/log(10)
	} {
		b := newPrim(t, "brightness_energy", map[string]any{"curve": tc.curve, "min": 0.0, "max": 1.0})
		e := testEnv(time.Now())
		e.In["energy"] = tc.energy
		b.Process(e)
		if math.Abs(e.Out["out"]-tc.want) > 0.03 {
			t.Fatalf("%s(%v) = %v, want ~%v", tc.curve, tc.energy, e.Out["out"], tc.want)
		}
	}
	// Clipping to [min, max].
	b := newPrim(t, "brightness_energy", map[string]any{"curve": "lin", "min": 0.2, "max": 0.8})
	e := testEnv(time.Now())
	e.In["energy"] = 0
	b.Process(e)
	if e.Out["out"] != 0.2 {
		t.Fatalf("floor clip = %v, want 0.2", e.Out["out"])
	}
	e.In["energy"] = 1
	b.Process(e)
	if e.Out["out"] != 0.8 {
		t.Fatalf("ceiling clip = %v, want 0.8", e.Out["out"])
	}
}

func TestColorMapEnergyInterpolates(t *testing.T) {
	c := newPrim(t, "color_map_energy", map[string]any{"palette": []string{"#000000", "#FFFFFF"}})
	e := testEnv(time.Now())
	e.In["energy"] = 0.5
	c.Process(e)
	if e.Out["r"] < 0.49 || e.Out["r"] > 0.51 || e.Out["g"] != e.Out["r"] {
		t.Fatalf("mid-palette should be grey 0.5: %+v", e.Out)
	}
	// Hue shift moves a colour.
	c2 := newPrim(t, "color_map_energy", map[string]any{"palette": []string{"#FF0000"}, "hue_shift": 120})
	e2 := testEnv(time.Now())
	e2.In["energy"] = 0
	c2.Process(e2)
	// #FF0000 rotated 120° is green-ish: G well above R.
	if e2.Out["g"] <= e2.Out["r"] {
		t.Fatalf("hue shift did not rotate red: %+v", e2.Out)
	}
}

func TestThresholdGateHysteresis(t *testing.T) {
	g := newPrim(t, "threshold_gate", map[string]any{"min": 0.3, "max": 0.7, "hysteresis": 0.2})
	e := testEnv(time.Now())

	// Below min: closed.
	e.In["value"] = 0.2
	g.Process(e)
	if e.Out["out"] != 0 {
		t.Fatal("gate open below min")
	}
	// Enter the window: open, value passes.
	e.In["value"] = 0.4
	g.Process(e)
	if e.Out["out"] != 0.4 {
		t.Fatalf("open gate not passing: %v", e.Out["out"])
	}
	// Hover at the edge (0.2, inside widened window 0.1..0.9): stays open.
	e.In["value"] = 0.2
	g.Process(e)
	if e.Out["out"] != 0.2 {
		t.Fatalf("hysteresis closed the gate too early: %v", e.Out["out"])
	}
	// Below the widened floor: closes.
	e.In["value"] = 0.05
	g.Process(e)
	if e.Out["out"] != 0 {
		t.Fatal("gate stayed open below hysteresis floor")
	}
}

func TestLfoWaveforms(t *testing.T) {
	// 1 Hz sine: 0 at t=0, 1 at t=250ms, 0.5 at t=500ms.
	l := newPrim(t, "lfo", map[string]any{"waveform": "sin", "frequency": 1})
	base := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		ms   int64
		want float64
	}{
		{0, 0.5},
		{250, 1.0},
		{500, 0.5},
		{750, 0.0},
	} {
		e := testEnv(base.Add(time.Duration(tc.ms) * time.Millisecond))
		l.Process(e)
		if math.Abs(e.Out["out"]-tc.want) > 0.02 {
			t.Fatalf("sin at %dms = %v, want %v", tc.ms, e.Out["out"], tc.want)
		}
	}
	// Square at 1 Hz: 1 in the first half-cycle, 0 in the second.
	sq := newPrim(t, "lfo", map[string]any{"waveform": "sqr", "frequency": 1})
	e := testEnv(base)
	sq.Process(e)
	if e.Out["out"] != 1 {
		t.Fatalf("sqr at t=0 = %v, want 1", e.Out["out"])
	}
	e = testEnv(base.Add(600 * time.Millisecond))
	sq.Process(e)
	if e.Out["out"] != 0 {
		t.Fatalf("sqr at t=600ms = %v, want 0", e.Out["out"])
	}
}

func TestSmootherAsymmetric(t *testing.T) {
	s := newPrim(t, "smoother", map[string]any{"attack_ms": 50, "release_ms": 2000})
	base := time.Unix(1_700_000_000, 0)
	e := testEnv(base)
	e.In["in"] = 0
	s.Process(e)
	if e.Out["out"] != 0 {
		t.Fatal("smoother not initialised to input")
	}
	// Fast attack: after 200ms at release 2000ms the rise is mostly done.
	e = testEnv(base.Add(200 * time.Millisecond))
	e.In["in"] = 1
	s.Process(e)
	if e.Out["out"] < 0.9 {
		t.Fatalf("attack too slow: %v after 200ms", e.Out["out"])
	}
	// Slow release: 200ms after the drop, still mostly high.
	e = testEnv(base.Add(400 * time.Millisecond))
	e.In["in"] = 0
	s.Process(e)
	if e.Out["out"] < 0.85 {
		t.Fatalf("release too fast: %v", e.Out["out"])
	}
}

func TestAdsrEnvelope(t *testing.T) {
	a := newPrim(t, "adsr_envelope", map[string]any{"attack_ms": 50, "decay_ms": 100, "sustain": 0.5, "release_ms": 200})
	base := time.Unix(1_700_000_000, 0)

	e := testEnv(base)
	a.Process(e)
	if e.Out["out"] != 0 {
		t.Fatal("envelope not 0 before trigger")
	}
	// Trigger on: attack begins (level 0 at the instant of the edge).
	e = testEnv(base.Add(10 * time.Millisecond))
	e.In["trigger"] = 1
	a.Process(e)
	if e.Out["out"] != 0 {
		t.Fatalf("envelope jumped at the trigger edge: %v", e.Out["out"])
	}
	// 20ms into a 50ms attack: ~40% up.
	e = testEnv(base.Add(30 * time.Millisecond))
	e.In["trigger"] = 1
	a.Process(e)
	if e.Out["out"] < 0.1 {
		t.Fatalf("attack not rising: %v", e.Out["out"])
	}
	// Attack completes at +60ms (the tick it crosses 50ms; output 1 at the
	// transition), then decay runs +60..+160ms down to sustain 0.5.
	e = testEnv(base.Add(60 * time.Millisecond))
	e.In["trigger"] = 1
	a.Process(e)
	if e.Out["out"] != 1 {
		t.Fatalf("attack peak = %v, want 1", e.Out["out"])
	}
	e = testEnv(base.Add(160 * time.Millisecond))
	e.In["trigger"] = 1
	a.Process(e)
	if math.Abs(e.Out["out"]-0.5) > 0.02 {
		t.Fatalf("sustain = %v, want 0.5", e.Out["out"])
	}
	// Held: stays at sustain.
	e = testEnv(base.Add(250 * time.Millisecond))
	e.In["trigger"] = 1
	a.Process(e)
	if math.Abs(e.Out["out"]-0.5) > 0.02 {
		t.Fatalf("sustain not held: %v", e.Out["out"])
	}
	// Release starts on the trigger drop (output still 0.5 at the edge)…
	e = testEnv(base.Add(260 * time.Millisecond))
	a.Process(e)
	if e.Out["out"] > 0.5 {
		t.Fatal("release not falling")
	}
	// …and is done 200ms later.
	e = testEnv(base.Add(460 * time.Millisecond))
	a.Process(e)
	if e.Out["out"] != 0 {
		t.Fatalf("envelope not zero after release: %v", e.Out["out"])
	}
}

func TestChaseTriggerAdvances(t *testing.T) {
	c := newPrim(t, "chase_trigger", map[string]any{"light_ids": []string{"light-a", "light-b"}, "speed": 0.5, "width": 1})
	base := time.Unix(1_700_000_000, 0)

	// The chase advances on its first tick (activation), then once per
	// speed interval: head goes light-b → light-a → light-b…
	e := testEnv(base)
	c.Process(e)
	if e.Lights["light-b"] != 1 || e.Lights["light-a"] != 0 {
		t.Fatalf("first advance wrong: %+v", e.Lights)
	}
	e2 := testEnv(base.Add(500 * time.Millisecond))
	c.Process(e2)
	if e2.Lights["light-a"] != 1 || e2.Lights["light-b"] != 0 {
		t.Fatalf("second advance wrong: %+v", e2.Lights)
	}
	// Before the interval elapses, nothing moves.
	e3 := testEnv(base.Add(600 * time.Millisecond))
	c.Process(e3)
	if e3.Lights["light-a"] != 1 || e3.Lights["light-b"] != 0 {
		t.Fatalf("chase advanced early: %+v", e3.Lights)
	}
}

func TestPulseEnergyDistance(t *testing.T) {
	p := newPrim(t, "pulse_energy", map[string]any{"center_light": "light-a", "radius": 1.5, "decay": 1})
	e := testEnv(time.Now())
	e.In["energy"] = 1
	p.Process(e)
	// light-a is the centre (dist 0): full energy. light-b is 2 units away
	// (> radius × energy): dark.
	if e.Lights["light-a"] != 1 {
		t.Fatalf("centre light = %v, want 1", e.Lights["light-a"])
	}
	if e.Lights["light-b"] != 0 {
		t.Fatalf("light 2 units away lit: %v", e.Lights["light-b"])
	}
	// Quiet energy shrinks the radius: light-a only.
	e2 := testEnv(time.Now())
	e2.In["energy"] = 0.5
	p.Process(e2)
	if e2.Lights["light-a"] != 0.5 {
		t.Fatalf("quiet pulse centre = %v, want 0.5", e2.Lights["light-a"])
	}
}

func TestColorMapFrequencyMixes(t *testing.T) {
	c := newPrim(t, "color_map_frequency", map[string]any{
		"bass_color": "#FF0000", "mid_color": "#00FF00", "treble_color": "#0000FF",
	})
	e := testEnv(time.Now())
	e.In["bass"], e.In["mid"], e.In["treble"] = 0.5, 0.25, 0
	c.Process(e)
	if math.Abs(e.Out["r"]-0.5) > 0.01 || math.Abs(e.Out["g"]-0.25) > 0.01 || e.Out["b"] != 0 {
		t.Fatalf("frequency mix wrong: %+v", e.Out)
	}
}
