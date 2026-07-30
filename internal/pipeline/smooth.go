package pipeline

import (
	"math"
	"sync"
	"time"
)

// LinearColor is a linear-light RGB triple, each component 0..1.
type LinearColor struct{ R, G, B float64 }

// deadband is the smallest per-component change worth recomputing. Below
// it, the previous smoothed value is kept as-is — the output loop still
// resends it (that is the keepalive), but there is no point spending cycles
// re-deriving a value that has not meaningfully moved.
const deadband = 1.0 / 512

// sceneCutThreshold: mean absolute change across all zones' luma, in a
// single tick, above which huemux treats it as a hard cut and snaps
// instead of easing. Without this a hard cut from dark to bright arrives
// visibly late, softened by the averaging window meant for ordinary motion.
const sceneCutThreshold = 0.35

// maxDeltaPerSecond caps how fast any channel may change, independent of
// the smoothing window. This is what kills strobing on flashing content —
// a different mechanism from the EMA, and both are needed.
const maxDeltaPerSecond = 4.0 // full 0..1 sweep in 250ms at most

// zoneState is the per-channel smoothing state carried between ticks.
type zoneState struct {
	current  LinearColor
	lastTick time.Time
	init     bool
}

// Smoother holds temporal state per channel and turns raw per-tick zone
// averages into the eased values that actually go out on the wire.
//
// Capture arrives at whatever rate the browser delivers; Smoother is driven
// once per output tick instead, on the output loop's own clock. It must
// never be skipped just because a new frame has not arrived — decoupling
// the two clocks is the point.
type Smoother struct {
	mu     sync.Mutex
	states map[uint8]*zoneState
}

// NewSmoother returns an empty Smoother; state is created lazily per
// channel id on first Step call.
func NewSmoother() *Smoother {
	return &Smoother{states: map[uint8]*zoneState{}}
}

// Step advances every channel's state by one output tick toward target,
// using reactivity (0-100, see ROADMAP for the tau mapping) to derive the
// EMA time constant. now should be the wall-clock time of this tick; alpha
// is computed from the *measured* interval since the channel's last tick,
// not the nominal tick rate, so that a ticker which slips does not silently
// change the smoothing behavior.
func (s *Smoother) Step(targets map[uint8]LinearColor, now time.Time, reactivity float64) map[uint8]LinearColor {
	s.mu.Lock()
	defer s.mu.Unlock()

	tauMs := lerp(60, 1200, (100-clampf(reactivity, 0, 100))/100)

	// Scene-cut detection: mean absolute luma change across all channels
	// that already have state.
	var sumDelta float64
	var n int
	for id, target := range targets {
		if st, ok := s.states[id]; ok && st.init {
			sumDelta += math.Abs(luma(target.R, target.G, target.B) - luma(st.current.R, st.current.G, st.current.B))
			n++
		}
	}
	sceneCut := n > 0 && sumDelta/float64(n) > sceneCutThreshold

	out := make(map[uint8]LinearColor, len(targets))
	for id, target := range targets {
		st, ok := s.states[id]
		if !ok {
			st = &zoneState{}
			s.states[id] = st
		}
		if !st.init {
			st.current = target
			st.init = true
			st.lastTick = now
			out[id] = target
			continue
		}

		dtMs := float64(now.Sub(st.lastTick).Milliseconds())
		if dtMs <= 0 {
			dtMs = 1
		}
		st.lastTick = now

		if sceneCut {
			st.current = target
			out[id] = clampColor(st.current)
			continue
		}

		st.current.R = step1D(st.current.R, target.R, tauMs, dtMs, maxDeltaPerSecond)
		st.current.G = step1D(st.current.G, target.G, tauMs, dtMs, maxDeltaPerSecond)
		st.current.B = step1D(st.current.B, target.B, tauMs, dtMs, maxDeltaPerSecond)
		out[id] = clampColor(st.current)
	}
	return out
}

// step1D advances one channel component with an exponential moving average,
// asymmetric between brightening and darkening (brightening reacts about
// twice as fast, matching how the eye handles onset versus decay), and rate
// limited independent of the EMA so flashing content cannot strobe the
// output no matter how the tau works out.
func step1D(current, target, tauMs, dtMs, maxPerSecond float64) float64 {
	if math.Abs(target-current) < deadband {
		return current
	}

	tau := tauMs
	if target < current {
		tau = tauMs * 2 // darkening: slower, so dark scenes don't feel like they're chasing every dip
	}
	alpha := 1 - math.Exp(-dtMs/tau)
	next := current + alpha*(target-current)

	maxDelta := maxPerSecond * (dtMs / 1000)
	if d := next - current; math.Abs(d) > maxDelta {
		if d > 0 {
			next = current + maxDelta
		} else {
			next = current - maxDelta
		}
	}
	return next
}

func clampColor(c LinearColor) LinearColor {
	return LinearColor{R: clamp01f(c.R), G: clamp01f(c.G), B: clamp01f(c.B)}
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// Reset drops all per-channel state, e.g. when the selected area changes.
func (s *Smoother) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states = map[uint8]*zoneState{}
}
