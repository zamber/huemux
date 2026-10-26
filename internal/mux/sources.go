package mux

import (
	"math"
	"time"
)

func init() {
	RegisterSource(SourceSpec{
		Meta: NodeMeta{
			Type: "timer", Label: "Timer",
			Description: "A free-running clock. Drives pulses, chases and gradients without any capture.",
			Params: []ParamSpec{
				{Name: "bpm", Label: "BPM", Type: "number", Default: 60.0, Min: 1, Max: 300, Step: 1, Help: "Cycles per minute."},
				{Name: "waveform", Label: "Waveform", Type: "enum", Default: "sine", Choices: []Choice{
					{Value: "sine", Label: "Sine"}, {Value: "triangle", Label: "Triangle"},
					{Value: "saw", Label: "Saw"}, {Value: "square", Label: "Square"},
				}},
				{Name: "offset", Label: "Offset", Type: "number", Default: 0.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "min", Label: "Min", Type: "number", Default: 0.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "max", Label: "Max", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
			},
		},
		New: func(p map[string]any) (Source, error) {
			s := &timerSource{
				bpm:      pnum(p, "bpm", 60),
				waveform: pstr(p, "waveform", "sine"),
				offset:   pnum(p, "offset", 0),
				min:      pnum(p, "min", 0),
				max:      pnum(p, "max", 1),
			}
			if s.bpm <= 0 {
				s.bpm = 60
			}
			if s.min > s.max {
				s.min, s.max = s.max, s.min
			}
			switch s.waveform {
			case "sine", "triangle", "saw", "square":
			default:
				s.waveform = "sine"
			}
			return s, nil
		},
	})

	RegisterSource(SourceSpec{
		Meta: NodeMeta{
			Type: "sound", Label: "Sound",
			Description: "The microphone's 32-band analysis. Silent until a capture source is running.",
			Params: []ParamSpec{
				{Name: "band", Label: "Band", Type: "enum", Default: "bass", Choices: []Choice{
					{Value: "bass", Label: "Bass"}, {Value: "mid", Label: "Mid"},
					{Value: "treble", Label: "Treble"}, {Value: "energy", Label: "Energy"},
					{Value: "beat", Label: "Beat"},
				}},
				{Name: "gain", Label: "Gain", Type: "number", Default: 1.0, Min: 0.1, Max: 8, Step: 0.1},
				{Name: "floor", Label: "Noise floor", Type: "number", Default: 0.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "smoothing", Label: "Smoothing", Type: "number", Default: 0.45, Min: 0.01, Max: 1, Step: 0.01, Help: "1 follows the signal instantly; lower values glide."},
			},
		},
		New: func(p map[string]any) (Source, error) {
			s := &soundSource{
				band:      pstr(p, "band", "bass"),
				gain:      pnum(p, "gain", 1),
				floor:     pnum(p, "floor", 0),
				smoothing: pnum(p, "smoothing", 0.45),
				threshold: pnum(p, "threshold", 1.3),
			}
			switch s.band {
			case "bass", "mid", "treble", "energy", "beat":
			default:
				s.band = "bass"
			}
			if s.smoothing < 0.01 {
				s.smoothing = 0.01
			}
			if s.smoothing > 1 {
				s.smoothing = 1
			}
			return s, nil
		},
	})

	RegisterSource(SourceSpec{
		Meta: NodeMeta{
			Type: "video", Label: "Video area",
			Description: "Colors sampled from the screen-sync grid. Reports zero when no sync capture is running.",
			Params: []ParamSpec{
				{Name: "channel", Label: "Channel", Type: "enum", Default: "luma", Choices: []Choice{
					{Value: "luma", Label: "Brightness"}, {Value: "r", Label: "Red"},
					{Value: "g", Label: "Green"}, {Value: "b", Label: "Blue"},
				}},
				{Name: "gain", Label: "Gain", Type: "number", Default: 1.0, Min: 0.1, Max: 4, Step: 0.1},
			},
		},
		New: func(p map[string]any) (Source, error) {
			s := &videoSource{channel: pstr(p, "channel", "luma"), gain: pnum(p, "gain", 1)}
			switch s.channel {
			case "luma", "r", "g", "b":
			default:
				s.channel = "luma"
			}
			return s, nil
		},
	})

	RegisterSource(SourceSpec{
		Meta: NodeMeta{
			Type: "constant", Label: "Constant",
			Description: "A manual value. Useful for testing an effect chain without a clock.",
			Params: []ParamSpec{
				{Name: "value", Label: "Value", Type: "number", Default: 0.5, Min: 0, Max: 1, Step: 0.01},
			},
		},
		New: func(p map[string]any) (Source, error) {
			return &constantSource{value: clamp01(pnum(p, "value", 0.5))}, nil
		},
	})

	RegisterSource(SourceSpec{
		Meta: NodeMeta{
			Type: "random", Label: "Random",
			Description: "A seeded hold-and-step random value. Repeats exactly for the same seed and clock.",
			Params: []ParamSpec{
				{Name: "speed", Label: "Changes / sec", Type: "number", Default: 0.5, Min: 0.05, Max: 8, Step: 0.05},
				{Name: "min", Label: "Min", Type: "number", Default: 0.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "max", Label: "Max", Type: "number", Default: 1.0, Min: 0, Max: 1, Step: 0.01},
				{Name: "smooth", Label: "Smooth", Type: "bool", Default: true},
				{Name: "seed", Label: "Seed", Type: "number", Default: 1.0, Min: 1, Max: 9999, Step: 1},
			},
		},
		New: func(p map[string]any) (Source, error) {
			s := &randomSource{
				speed:  pnum(p, "speed", 0.5),
				min:    pnum(p, "min", 0),
				max:    pnum(p, "max", 1),
				smooth: pbool(p, "smooth", true),
				seed:   uint64(pnum(p, "seed", 1)),
			}
			if s.speed <= 0 {
				s.speed = 0.5
			}
			if s.min > s.max {
				s.min, s.max = s.max, s.min
			}
			return s, nil
		},
	})
}

// --- timer -----------------------------------------------------------

type timerSource struct {
	bpm      float64
	waveform string
	offset   float64
	min, max float64

	initialized bool
	lastPhase   float64
	beatUntil   time.Time
}

func (s *timerSource) Type() string { return "timer" }

func (s *timerSource) Sample(in Inputs) SourceOutput {
	nowSec := float64(in.Now.UnixNano()) / 1e9
	phase := frac(nowSec*s.bpm/60 + s.offset)
	if s.initialized && phase < s.lastPhase {
		s.beatUntil = in.Now.Add(80 * time.Millisecond)
	}
	s.lastPhase = phase
	s.initialized = true

	var w float64
	switch s.waveform {
	case "triangle":
		w = 1 - math.Abs(2*phase-1)
	case "saw":
		w = phase
	case "square":
		if phase < 0.5 {
			w = 1
		}
	default: // sine
		w = 0.5 - 0.5*math.Cos(2*math.Pi*phase)
	}
	beat := 0.0
	if in.Now.Before(s.beatUntil) {
		beat = 1
	}
	return SourceOutput{Values: map[string]float64{
		"value": s.min + (s.max-s.min)*w,
		"phase": phase,
		"beat":  beat,
	}}
}

// --- sound -----------------------------------------------------------

type soundSource struct {
	band        string
	gain, floor float64
	smoothing   float64
	threshold   float64
	minInterval time.Duration

	initialized bool
	value       float64
	average     float64
	lastBeat    time.Time
	beatUntil   time.Time
}

func (s *soundSource) Type() string { return "sound" }

func (s *soundSource) Sample(in Inputs) SourceOutput {
	out := SourceOutput{Values: map[string]float64{
		"value": 0, "bass": 0, "mid": 0, "treble": 0, "energy": 0, "beat": 0,
	}}
	if !in.Audio.Active {
		s.value += (0 - s.value) * s.smoothing
		out.Values["value"] = s.value
		return out
	}
	bass := meanBands(in.Audio.Bands, 0, 8)
	mid := meanBands(in.Audio.Bands, 8, 20)
	treble := meanBands(in.Audio.Bands, 20, len(in.Audio.Bands))
	energy := 0.5*bass + 0.35*mid + 0.15*treble

	if !s.initialized {
		s.average = energy
		s.initialized = true
	}
	s.average = s.average*0.92 + energy*0.08

	minInterval := s.minInterval
	if minInterval <= 0 {
		minInterval = 180 * time.Millisecond
	}
	if energy > 0.02 && energy > s.average*s.threshold && in.Now.Sub(s.lastBeat) > minInterval {
		s.lastBeat = in.Now
		s.beatUntil = in.Now.Add(80 * time.Millisecond)
	}
	beat := 0.0
	if in.Now.Before(s.beatUntil) {
		beat = 1
	}

	raw := energy
	switch s.band {
	case "bass":
		raw = bass
	case "mid":
		raw = mid
	case "treble":
		raw = treble
	case "beat":
		raw = beat
	}
	target := clamp01(raw*s.gain - s.floor)
	s.value += (target - s.value) * s.smoothing

	out.Values["value"] = s.value
	out.Values["bass"] = clamp01(bass*s.gain - s.floor)
	out.Values["mid"] = clamp01(mid*s.gain - s.floor)
	out.Values["treble"] = clamp01(treble*s.gain - s.floor)
	out.Values["energy"] = clamp01(energy*s.gain - s.floor)
	out.Values["beat"] = beat
	return out
}

func meanBands(bands []float64, lo, hi int) float64 {
	if len(bands) == 0 {
		return 0
	}
	if lo < 0 {
		lo = 0
	}
	if hi > len(bands) {
		hi = len(bands)
	}
	if hi <= lo {
		return 0
	}
	var sum float64
	for _, v := range bands[lo:hi] {
		sum += v
	}
	return sum / float64(hi-lo)
}

// --- video -----------------------------------------------------------

type videoSource struct {
	channel string
	gain    float64
}

func (s *videoSource) Type() string { return "video" }

func (s *videoSource) Sample(in Inputs) SourceOutput {
	out := SourceOutput{Values: map[string]float64{"value": 0, "r": 0, "g": 0, "b": 0}}
	if !in.Video.Active || len(in.Video.Colors) == 0 {
		return out
	}
	var r, g, b float64
	for _, c := range in.Video.Colors {
		r += c.R
		g += c.G
		b += c.B
	}
	n := float64(len(in.Video.Colors))
	r, g, b = r/n, g/n, b/n
	luma := 0.2126*r + 0.7152*g + 0.0722*b
	val := luma
	switch s.channel {
	case "r":
		val = r
	case "g":
		val = g
	case "b":
		val = b
	}
	out.Values["value"] = clamp01(val * s.gain)
	out.Values["r"] = clamp01(r * s.gain)
	out.Values["g"] = clamp01(g * s.gain)
	out.Values["b"] = clamp01(b * s.gain)
	out.Colors = make(map[string]Color, len(in.Video.Colors))
	for id, c := range in.Video.Colors {
		out.Colors[id] = c
	}
	return out
}

// --- constant --------------------------------------------------------

type constantSource struct{ value float64 }

func (s *constantSource) Type() string { return "constant" }

func (s *constantSource) Sample(Inputs) SourceOutput {
	return SourceOutput{Values: map[string]float64{"value": s.value}}
}

// --- random ----------------------------------------------------------

type randomSource struct {
	speed  float64
	min    float64
	max    float64
	smooth bool
	seed   uint64

	initialized bool
	lastSeg     int64
	current     float64
	next        float64
}

func (s *randomSource) Type() string { return "random" }

func (s *randomSource) Sample(in Inputs) SourceOutput {
	nowSec := float64(in.Now.UnixNano()) / 1e9
	period := 1 / s.speed
	seg := int64(math.Floor(nowSec / period))
	if !s.initialized {
		s.current = hashUnit(s.seed, seg)
		s.next = hashUnit(s.seed, seg+1)
		s.lastSeg = seg
		s.initialized = true
	}
	for s.lastSeg < seg {
		s.lastSeg++
		s.current = s.next
		s.next = hashUnit(s.seed, s.lastSeg+1)
	}
	v := s.current
	if s.smooth {
		t := nowSec/period - math.Floor(nowSec/period)
		v = s.current + (s.next-s.current)*smoothstep(t)
	}
	return SourceOutput{Values: map[string]float64{"value": s.min + (s.max-s.min)*v}}
}

func smoothstep(t float64) float64 {
	t = clamp01(t)
	return t * t * (3 - 2*t)
}

// hashUnit is splitmix64 reduced to 0..1. Deterministic across processes,
// which is all the random source promises.
func hashUnit(seed uint64, n int64) float64 {
	x := seed*0x9E3779B97F4A7C15 + uint64(n)*0xBF58476D1CE4E5B9
	x ^= x >> 30
	x *= 0xBF58476D1CE4E5B9
	x ^= x >> 27
	x *= 0x94D049BB133111EB
	x ^= x >> 31
	return float64(x>>11) / float64(1<<53)
}
