package preset

import (
	"encoding/json"
	"math"
	"time"
)

func init() {
	Register("lfo", func() Primitive { return &lfo{} })
	Register("smoother", func() Primitive { return &smoother{} })
	Register("adsr_envelope", func() Primitive { return &adsrEnvelope{} })
}

// lfo cycles 0..1 at a fixed frequency. The plan's phase_offset is a
// 0..1 fraction of the cycle, so two LFOs can be offset without degrees.
type lfo struct {
	params struct {
		Waveform    string  `json:"waveform"` // sin | tri | sqr
		Frequency   float64 `json:"frequency"`
		PhaseOffset float64 `json:"phase_offset"`
	}
}

func (l *lfo) Type() string { return "lfo" }

func (l *lfo) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "lfo", Category: CategoryModulation,
		Label: "LFO", Description: "Low-frequency oscillator, cycles 0–1 at a fixed frequency.",
		Outputs: []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "waveform", Label: "Waveform", Type: "string", Default: "sin", Choices: []string{"sin", "tri", "sqr"}, Description: "Oscillator waveform."},
			{Name: "frequency", Label: "Frequency (Hz)", Type: "number", Default: 0.1, Step: 0.01, Description: "Cycles per second."},
			{Name: "phase_offset", Label: "Phase Offset", Type: "number", Default: 0.0, Min: f64ptr(0), Max: f64ptr(1), Step: 0.01, Description: "Phase offset as 0–1 fraction of cycle."},
		},
	}
}

func (l *lfo) Init(raw json.RawMessage) error {
	l.params.Waveform = "sin"
	l.params.Frequency = 0.1
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &l.params); err != nil {
			return err
		}
	}
	switch l.params.Waveform {
	case "sin", "tri", "sqr":
	default:
		l.params.Waveform = "sin"
	}
	if l.params.Frequency <= 0 {
		l.params.Frequency = 0.1
	}
	return nil
}

func (l *lfo) Process(env *Env) {
	phase := 2 * math.Pi * (l.params.Frequency*float64(env.Now.UnixMilli())/1000 + math.Mod(l.params.PhaseOffset, 1))
	switch l.params.Waveform {
	case "tri":
		// asin(sin): triangle, then mapped to 0..1.
		env.Out["out"] = (2*math.Asin(math.Sin(phase))/math.Pi + 1) / 2
	case "sqr":
		if math.Sin(phase) >= 0 {
			env.Out["out"] = 1
		} else {
			env.Out["out"] = 0
		}
	default:
		env.Out["out"] = (math.Sin(phase) + 1) / 2
	}
}

// adsrEnvelope is the classic attack/decay/sustain/release envelope on a
// trigger: the output jumps up over attack_ms, settles to sustain over
// decay_ms, holds, and falls through release_ms when the trigger drops.
type adsrEnvelope struct {
	params struct {
		AttackMS  float64 `json:"attack_ms"`
		DecayMS   float64 `json:"decay_ms"`
		Sustain   float64 `json:"sustain"`
		ReleaseMS float64 `json:"release_ms"`
	}
	phase    int // 0 off, 1 attack, 2 decay, 3 sustain, 4 release
	prevTrig bool
	phaseAt  time.Time
}

const (
	adsrOff = iota
	adsrAttack
	adsrDecay
	adsrSustain
	adsrRelease
)

func (a *adsrEnvelope) Type() string { return "adsr_envelope" }

func (a *adsrEnvelope) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "adsr_envelope", Category: CategoryModulation,
		Label: "ADSR Envelope", Description: "Attack/decay/sustain/release envelope triggered by a trigger signal.",
		Inputs:  []Port{{Name: "trigger", Kind: PortTrigger}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "attack_ms", Label: "Attack (ms)", Type: "number", Default: 10.0, Step: 1, Description: "Rise time to peak."},
			{Name: "decay_ms", Label: "Decay (ms)", Type: "number", Default: 200.0, Step: 1, Description: "Fall time to sustain level."},
			{Name: "sustain", Label: "Sustain", Type: "number", Default: 0.6, Step: 0.01, Description: "Sustain level (0–1)."},
			{Name: "release_ms", Label: "Release (ms)", Type: "number", Default: 400.0, Step: 1, Description: "Fall time to zero after trigger ends."},
		},
	}
}

func (a *adsrEnvelope) Init(raw json.RawMessage) error {
	a.params.AttackMS = 10
	a.params.DecayMS = 200
	a.params.Sustain = 0.6
	a.params.ReleaseMS = 400
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a.params); err != nil {
			return err
		}
	}
	if a.params.AttackMS <= 0 {
		a.params.AttackMS = 10
	}
	if a.params.DecayMS <= 0 {
		a.params.DecayMS = 200
	}
	if a.params.ReleaseMS <= 0 {
		a.params.ReleaseMS = 400
	}
	if a.params.Sustain < 0 || a.params.Sustain > 1 {
		a.params.Sustain = 0.6
	}
	return nil
}

func (a *adsrEnvelope) Process(env *Env) {
	trig := env.In["trigger"] > 0.5
	now := env.Now
	if trig && !a.prevTrig {
		a.phase = adsrAttack
		a.phaseAt = now
	}
	if !trig && a.prevTrig {
		a.phase = adsrRelease
		a.phaseAt = now
	}
	a.prevTrig = trig

	t := now.Sub(a.phaseAt).Seconds() * 1000
	var level float64
	switch a.phase {
	case adsrOff:
		level = 0
	case adsrAttack:
		level = t / a.params.AttackMS
		if t >= a.params.AttackMS {
			level = 1
			a.phase = adsrDecay
			a.phaseAt = now
		}
	case adsrDecay:
		level = 1 - (1-a.params.Sustain)*t/a.params.DecayMS
		if t >= a.params.DecayMS {
			level = a.params.Sustain
			a.phase = adsrSustain
			a.phaseAt = now
		}
	case adsrSustain:
		level = a.params.Sustain
	case adsrRelease:
		level = a.params.Sustain * (1 - t/a.params.ReleaseMS)
		if t >= a.params.ReleaseMS {
			level = 0
			a.phase = adsrOff
		}
	}
	env.Out["out"] = clamp01(level)
}

// smoother is an asymmetric one-pole low-pass: attack_ms governs rises,
// release_ms falls. This is the scalar counterpart of the pipeline's color
// smoother (the plan's "reuse internal/pipeline/smooth.go" — the pipeline
// one works per-channel on colors, which is the wrong shape for a scalar
// port, so the same time-constant math is applied here).
type smoother struct {
	params struct {
		AttackMS  float64 `json:"attack_ms"`
		ReleaseMS float64 `json:"release_ms"`
	}
	current float64
	last    time.Time
	init    bool
}

func (s *smoother) Type() string { return "smoother" }

func (s *smoother) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "smoother", Category: CategoryModulation,
		Label: "Smoother", Description: "Asymmetric one-pole low-pass on a scalar. Attack governs rises, release governs falls.",
		Inputs:  []Port{{Name: "in", Kind: PortScalar}},
		Outputs: []Port{{Name: "out", Kind: PortScalar}},
		Params: []ParamSpec{
			{Name: "attack_ms", Label: "Attack (ms)", Type: "number", Default: 100.0, Step: 1, Description: "Rise time constant."},
			{Name: "release_ms", Label: "Release (ms)", Type: "number", Default: 400.0, Step: 1, Description: "Fall time constant."},
		},
	}
}

func (s *smoother) Init(raw json.RawMessage) error {
	s.params.AttackMS = 100
	s.params.ReleaseMS = 400
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &s.params); err != nil {
			return err
		}
	}
	if s.params.AttackMS <= 0 {
		s.params.AttackMS = 100
	}
	if s.params.ReleaseMS <= 0 {
		s.params.ReleaseMS = 400
	}
	return nil
}

func (s *smoother) Process(env *Env) {
	in := env.In["in"]
	if !s.init {
		s.current = in
		s.last = env.Now
		s.init = true
		env.Out["out"] = in
		return
	}
	ms := s.params.ReleaseMS
	if in > s.current {
		ms = s.params.AttackMS
	}
	// alpha from the measured interval since the last tick, so a slipped
	// ticker does not silently change the time constant (same reasoning as
	// pipeline.Smoother).
	dt := env.Now.Sub(s.last).Seconds() * 1000
	if s.last.IsZero() || dt <= 0 {
		dt = 40 // nominal 25 Hz tick
	}
	alpha := 1 - math.Exp(-dt/ms)
	s.current += alpha * (in - s.current)
	s.last = env.Now
	env.Out["out"] = s.current
}
