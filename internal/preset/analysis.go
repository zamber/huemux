package preset

import (
	"encoding/json"
	"math"
	"time"
)

func init() {
	Register("mic_capture", func() Primitive { return &micCapture{} })
	Register("beat_detector", func() Primitive { return &beatDetector{} })
	Register("freq_bands", func() Primitive { return &freqBands{} })
}

// micCapture is the graph's audio source. It produces nothing itself; the
// runner injects the latest analysis frame into every Env, and edges from a
// source node are dropped at parse time. It exists so presets read the way
// the plan describes them, and so a later source primitive (loopback,
// display audio) can replace it without touching the graph format.
type micCapture struct{}

func (m *micCapture) Type() string               { return "mic_capture" }
func (m *micCapture) Init(json.RawMessage) error { return nil }
func (m *micCapture) Process(*Env)               {}
func (m *micCapture) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "mic_capture", Category: CategorySource,
		Label:       "Microphone",
		Description: "Audio source from microphone or system audio capture. The runner injects the FFT frame into every node.",
		Outputs:     []Port{{Name: "out", Kind: PortTrigger}},
		Params:      nil,
	}
}

// beatParams and beatState --------------------------------------------------

const (
	// beatRingFrames is the energy history length. The plan says ~43 frames
	// (~1s); analysis arrives at ~30 Hz, so 43 frames is ~1.4s of history.
	beatRingFrames = 43
	// beatHoldTicks keeps a detected beat visible for three ticks (~100 ms
	// at the ~30 Hz analysis rate) so the slower effect clock (~25 Hz,
	// DP-8) reliably samples it — a beat lasts one analysis frame, which a
	// 40 ms sampler would otherwise miss almost half the time.
	beatHoldTicks = 3
	// bpmIntervals is how many inter-beat intervals feed the BPM estimate.
	bpmIntervals = 8
)

type beatParams struct {
	Threshold     float64 `json:"threshold"`
	MinIntervalMS float64 `json:"min_interval_ms"`
}

type beatDetector struct {
	params beatParams

	ring    [beatRingFrames]float64 // short-term energy history
	next    int                     // next ring slot
	filled  int                     // frames seen so far; < beatRingFrames while warming up
	beats   []float64               // recent inter-beat intervals, seconds
	lastHit time.Time
	hold    int
	bpm     float64
}

func (b *beatDetector) Type() string { return "beat_detector" }

func (b *beatDetector) Meta() PrimitiveMeta {
	minT, maxT := 1.1, 2.0
	minI, maxI := 100.0, 600.0
	return PrimitiveMeta{
		Type: "beat_detector", Category: CategoryAnalysis,
		Label:       "Beat Detector",
		Description: "Energy-onset beat detection. Outputs beat trigger, confidence, and BPM.",
		Outputs: []Port{
			{Name: "beat", Kind: PortTrigger},
			{Name: "confidence", Kind: PortScalar},
			{Name: "bpm", Kind: PortScalar},
		},
		Params: []ParamSpec{
			{Name: "threshold", Label: "Threshold", Type: "number", Default: 1.3, Min: &minT, Max: &maxT, Step: 0.05, Description: "Energy multiplier above variance to trigger beat."},
			{Name: "min_interval_ms", Label: "Min Interval (ms)", Type: "number", Default: 200.0, Min: &minI, Max: &maxI, Step: 10, Description: "Minimum time between beats."},
		},
	}
}

func (b *beatDetector) Init(raw json.RawMessage) error {
	b.params = beatParams{Threshold: 1.3, MinIntervalMS: 200}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &b.params); err != nil {
			return err
		}
	}
	if b.params.Threshold < 1.1 || b.params.Threshold > 2.0 {
		b.params.Threshold = 1.3
	}
	if b.params.MinIntervalMS < 100 || b.params.MinIntervalMS > 600 {
		b.params.MinIntervalMS = 200
	}
	b.beats = make([]float64, 0, bpmIntervals)
	return nil
}

// Process implements the plan's algorithm (docs/MUSIC-REACTIVITY.md
// "Beat detection algorithm"): short-term energy against the variance of
// ~1s of history, with a minimum inter-beat interval and a BPM estimate
// from the median of recent intervals.
func (b *beatDetector) Process(env *Env) {
	env.Out["bpm"] = b.bpm
	env.Out["beat"] = 0
	env.Out["confidence"] = 0

	if b.hold > 0 {
		b.hold--
		env.Out["beat"] = 1
		env.Out["confidence"] = 1
		return
	}
	if !env.HasFFT {
		return
	}

	// E_short = mean(|fft|²) over the current frame.
	var sum float64
	for _, v := range env.Frame.FFT {
		sum += float64(v) * float64(v)
	}
	short := sum / float64(len(env.Frame.FFT))

	// E_long = mean(ring), C = var(ring).
	b.ring[b.next] = short
	b.next = (b.next + 1) % beatRingFrames
	if b.filled < beatRingFrames {
		b.filled++
	}
	var long float64
	for _, v := range b.ring {
		long += v
	}
	long /= beatRingFrames
	var variance float64
	for _, v := range b.ring {
		d := v - long
		variance += d * d
	}
	variance /= beatRingFrames

	// Warm-up and silence guards: before ~1s of history the variance of a
	// nearly-empty ring is positive-but-meaningless (one populated slot
	// against 42 zeros looks like a spike), and variance 0 with a dead mic
	// must not produce beats on 0 > 0. Both must not fire.
	if b.filled < beatRingFrames || variance <= 0 {
		env.Out["beat"] = 0
		return
	}

	now := env.Now
	sinceLast := now.Sub(b.lastHit).Seconds()
	if short > variance*b.params.Threshold && sinceLast*1000 >= b.params.MinIntervalMS {
		b.lastHit = now
		b.hold = beatHoldTicks
		env.Out["beat"] = 1
		// Confidence is how far past the threshold this hit was.
		env.Out["confidence"] = clamp01(short / (variance * b.params.Threshold))

		if sinceLast > 0 && sinceLast < 5 { // >5s gaps are pauses, not tempo
			b.beats = append(b.beats, sinceLast)
			if len(b.beats) > bpmIntervals {
				b.beats = b.beats[1:]
			}
			b.bpm = 60 / median(b.beats)
		}
	}
	env.Out["bpm"] = b.bpm
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	// Insertion sort into a copy: the slice is tiny (≤8) and this keeps the
	// median computation allocation-free on the hot path.
	cp := append([]float64(nil), v...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	mid := len(cp) / 2
	if len(cp)%2 == 1 {
		return cp[mid]
	}
	return (cp[mid-1] + cp[mid]) / 2
}

// freqBands splits the 32 log-spaced FFT bands into bass/mid/treble ranges.
// ---------------------------------------------------------------------------

type freqBandParams struct {
	BassCutoff   float64 `json:"bass_cutoff"`
	TrebleCutoff float64 `json:"treble_cutoff"`
}

// binHz is the nominal per-bin width the band mapping assumes (2048-pt FFT
// at 44.1 kHz, the Web Audio default). The browser's actual sample rate can
// differ (48 kHz is common), which shifts the boundaries by a fraction of a
// band — irrelevant for a bass/mid/treble split, and not worth a wire-format
// change to fix exactly.
const binHz = 44100.0 / 2048.0

// logBandCount is fixed by the wire format's 32 bands.
const logBandCount = 32

type freqBands struct {
	bassBand   int // first mid band
	trebleBand int // first treble band
}

func (f *freqBands) Type() string { return "freq_bands" }

func (f *freqBands) Meta() PrimitiveMeta {
	return PrimitiveMeta{
		Type: "freq_bands", Category: CategoryAnalysis,
		Label:       "Frequency Bands",
		Description: "Splits 32 FFT bands into bass, mid, treble, and RMS scalars.",
		Outputs: []Port{
			{Name: "bass", Kind: PortScalar},
			{Name: "mid", Kind: PortScalar},
			{Name: "treble", Kind: PortScalar},
			{Name: "rms", Kind: PortScalar},
		},
		Params: []ParamSpec{
			{Name: "bass_cutoff", Label: "Bass Cutoff (Hz)", Type: "number", Default: 200.0, Step: 10, Description: "Frequency boundary between bass and mid."},
			{Name: "treble_cutoff", Label: "Treble Cutoff (Hz)", Type: "number", Default: 4000.0, Step: 10, Description: "Frequency boundary between mid and treble."},
		},
	}
}

func (f *freqBands) Init(raw json.RawMessage) error {
	p := freqBandParams{BassCutoff: 200, TrebleCutoff: 4000}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
	}
	f.bassBand = bandOfHz(p.BassCutoff)
	f.trebleBand = bandOfHz(p.TrebleCutoff)
	if f.trebleBand <= f.bassBand {
		f.trebleBand = f.bassBand + 1
	}
	return nil
}

// bandOfHz maps a frequency to the log-spaced band index that contains it,
// mirroring the browser's bandEdgesFor() (web/music.js).
func bandOfHz(hz float64) int {
	if hz <= 0 {
		return 0
	}
	bin := hz / binHz
	if bin < 1 {
		return 0
	}
	b := int(logBandCount * math.Log(bin) / math.Log(1023))
	if b < 0 {
		return 0
	}
	if b >= logBandCount {
		return logBandCount - 1
	}
	return b
}

func (f *freqBands) Process(env *Env) {
	env.Out["bass"], env.Out["mid"], env.Out["treble"], env.Out["rms"] = 0, 0, 0, 0
	if !env.HasFFT {
		return
	}
	mean := func(lo, hi int) float64 { // mean magnitude over bands [lo, hi)
		if hi <= lo {
			return 0
		}
		var sum float64
		for i := lo; i < hi; i++ {
			sum += float64(env.Frame.FFT[i])
		}
		return sum / float64(hi-lo)
	}
	env.Out["bass"] = mean(0, f.bassBand)
	env.Out["mid"] = mean(f.bassBand, f.trebleBand)
	env.Out["treble"] = mean(f.trebleBand, logBandCount)
	env.Out["rms"] = mean(0, logBandCount)
}
