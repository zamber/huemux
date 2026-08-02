package pipeline

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func colorApprox(a, b LinearColor) bool {
	return approx(a.R, b.R) && approx(a.G, b.G) && approx(a.B, b.B)
}

func t0() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC) }

func TestSmootherFirstStepReturnsTarget(t *testing.T) {
	// No history: the first tick must pass the raw value through untouched.
	s := NewSmoother()
	targets := map[uint8]LinearColor{1: {R: 0.9, G: 0.5, B: 0.1}, 2: {R: 0.0, G: 1.0, B: 0.5}}
	got := s.Step(targets, t0(), 45)
	if len(got) != 2 {
		t.Fatalf("output has %d channels, want 2", len(got))
	}
	for id, want := range targets {
		if !colorApprox(got[id], want) {
			t.Errorf("channel %d first step = %+v, want raw target %+v", id, got[id], want)
		}
	}
}

func TestSmootherEasesTowardTarget(t *testing.T) {
	// With low reactivity the EMA time constant is long: one tick at 1s must
	// land strictly between current and target, not snap to either.
	s := NewSmoother()
	base := map[uint8]LinearColor{1: {R: 0.5, G: 0.5, B: 0.5}}
	s.Step(base, t0(), 0) // init: current = 0.5

	target := map[uint8]LinearColor{1: {R: 0.3, G: 0.3, B: 0.3}}
	got := s.Step(target, t0().Add(time.Second), 0)
	c := got[1]
	if c.R <= 0.3 || c.R >= 0.5 {
		t.Errorf("eased value = %v, want strictly between 0.3 and 0.5", c)
	}
	// All three components behave identically.
	if !colorApprox(c, LinearColor{R: c.R, G: c.R, B: c.R}) {
		t.Errorf("components diverged: %+v", c)
	}
}

func TestSmootherTracksHistory(t *testing.T) {
	// Repeated ticks must approach the target monotonically and never
	// overshoot it.
	s := NewSmoother()
	base := map[uint8]LinearColor{1: {R: 0.5, G: 0.5, B: 0.5}}
	s.Step(base, t0(), 0)
	target := map[uint8]LinearColor{1: {R: 0.3, G: 0.3, B: 0.3}}

	prev := 0.5
	for i := 0; i < 10; i++ {
		got := s.Step(target, t0().Add(time.Duration(i+1)*time.Second), 0)
		v := got[1].R
		if v >= prev || v < 0.3 {
			t.Fatalf("tick %d: value %f, previous %f — must approach 0.3 monotonically without overshoot", i+1, v, prev)
		}
		prev = v
	}
}

func TestSmootherReducesPeakValue(t *testing.T) {
	// A 0.2 -> 0.5 jump is below the scene-cut threshold, so it must be
	// eased — the output never reaches the raw target on the first tick.
	s := NewSmoother()
	s.Step(map[uint8]LinearColor{1: {R: 0.2, G: 0.2, B: 0.2}}, t0(), 0)
	peak := map[uint8]LinearColor{1: {R: 0.5, G: 0.5, B: 0.5}} // luma delta 0.3 < 0.35

	got := s.Step(peak, t0().Add(time.Second), 0)
	if c := got[1]; c.R >= 0.5 || c.R <= 0.2 {
		t.Errorf("peak value = %v, want strictly between 0.2 and 0.5", c)
	}
}

func TestSmootherSceneCutSnaps(t *testing.T) {
	s := NewSmoother()
	s.Step(map[uint8]LinearColor{1: {R: 0.0, G: 0.0, B: 0.0}}, t0(), 0)
	bright := map[uint8]LinearColor{1: {R: 1.0, G: 1.0, B: 1.0}} // luma delta 1.0 > 0.35
	got := s.Step(bright, t0().Add(time.Second), 0)
	if !colorApprox(got[1], LinearColor{R: 1.0, G: 1.0, B: 1.0}) {
		t.Errorf("scene cut must snap to target, got %+v", got[1])
	}
}

func TestSmootherHighReactivitySnaps(t *testing.T) {
	// Reactivity 100 gives a zero time constant: alpha = 1, so the next tick
	// lands exactly on target (for a sub-cut change, so the scene-cut branch
	// is not what does it).
	s := NewSmoother()
	base := map[uint8]LinearColor{1: {R: 0.5, G: 0.5, B: 0.5}}
	s.Step(base, t0(), 0)
	target := map[uint8]LinearColor{1: {R: 0.45, G: 0.45, B: 0.45}} // delta 0.05 < 0.35
	got := s.Step(target, t0().Add(time.Second), 100)
	if !colorApprox(got[1], target[1]) {
		t.Errorf("reactivity 100: got %+v, want %+v", got[1], target[1])
	}
}

func TestSmootherResetDropsHistory(t *testing.T) {
	s := NewSmoother()
	s.Step(map[uint8]LinearColor{1: {R: 0.9, G: 0.9, B: 0.9}}, t0(), 0)
	s.Reset()
	// After Reset the smoother behaves like a fresh one: raw passthrough.
	target := map[uint8]LinearColor{1: {R: 0.1, G: 0.9, B: 0.3}}
	got := s.Step(target, t0().Add(time.Second), 0)
	if !colorApprox(got[1], target[1]) {
		t.Errorf("after Reset, got %+v, want raw target %+v", got[1], target[1])
	}
}

func TestSmootherEmptyTargets(t *testing.T) {
	s := NewSmoother()
	if got := s.Step(nil, t0(), 45); len(got) != 0 {
		t.Errorf("empty targets: got %v, want empty map", got)
	}
}

func TestSmootherClampsOutput(t *testing.T) {
	// A channel initialized out of range (hand-built LinearColors) must not
	// emit out-of-range eased values; the sub-cut change below takes the EMA
	// path, whose output goes through clampColor.
	s := NewSmoother()
	s.Step(map[uint8]LinearColor{1: {R: 2.0, G: 2.0, B: 2.0}}, t0(), 0)
	got := s.Step(map[uint8]LinearColor{1: {R: 1.8, G: 1.8, B: 1.8}}, t0().Add(time.Second), 0)
	c := got[1]
	if c.R > 1.0 || c.R < 0 || c.G > 1.0 || c.G < 0 || c.B > 1.0 || c.B < 0 {
		t.Errorf("clamped value out of [0,1]: %+v", c)
	}
	if !approx(c.R, 1.0) {
		t.Errorf("value = %v, want exactly 1.0 after clamping", c)
	}
}

func TestStep1DDeadband(t *testing.T) {
	// Changes below the deadband keep the previous value verbatim.
	if got := step1D(0.5, 0.5+deadband/2, 100, 1000, 4.0); !approx(got, 0.5) {
		t.Errorf("sub-deadband step = %f, want 0.5", got)
	}
	if got := step1D(0.5, 0.5, 100, 1000, 4.0); !approx(got, 0.5) {
		t.Errorf("no-op step = %f, want 0.5", got)
	}
}

func TestStep1DRateLimit(t *testing.T) {
	// With tau far below dt, alpha ~ 1 and the EMA alone would jump nearly
	// the whole range in one tick; the rate limiter must cap the movement to
	// maxPerSecond * dt = 0.1.
	got := step1D(0, 1, 1, 100, 1.0)
	if got > 0.1+1e-9 {
		t.Errorf("rate-limited step = %f, want <= 0.1", got)
	}
	if got <= 0 {
		t.Errorf("rate-limited step = %f, want > 0", got)
	}
}

func TestStep1DDarkeningSlower(t *testing.T) {
	// Darkening doubles tau, so a 0.5 -> 0.0 move covers less distance in one
	// tick than a 0.0 -> 0.5 move does.
	bright := step1D(0.0, 0.5, 1000, 1000, 4.0)
	dark := step1D(0.5, 0.0, 1000, 1000, 4.0)
	if bright <= 0.5-dark {
		t.Errorf("brightening distance %f not greater than darkening distance %f", bright, 0.5-dark)
	}
}
