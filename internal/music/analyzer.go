package music

import (
	"math"
)

// The PCM analyzer is the headless mode DP-7 anticipated: analysis normally
// runs in the browser's Web Audio API, but Android's internal-audio capture
// happens in Kotlin, where there is no AnalyserNode. The phone pushes raw
// PCM over the mobile facade and this produces the same 32-band + 256-wave
// frame the browser's 0x02 path sends — same geometric band mapping
// (web/music.js bandEdgesFor), so every primitive downstream is agnostic to
// where the analysis came from.

// fftSize matches the browser's 2048-pt FFT, so per-bin widths and the
// geometric band edges line up between the two sources.
const (
	fftSize    = 2048
	fftHop     = 1024 // 50% overlap: ~43 frames/s at 44.1 kHz (DP-8 wants 30-60)
	waveStride = fftSize / Samples
)

// Analyzer buffers raw PCM and emits analysis frames on every hop. One
// instance per server; stateful, so chunks of any size can be fed.
type Analyzer struct {
	buf  []float64 // raw samples, waiting for a full window
	rate int       // last sample rate seen; 0 until the first Feed
}

// Feed consumes little-endian signed-16 PCM (the format Android's
// AudioRecord produces with ENCODING_PCM_16BIT) and returns any complete
// analysis frames. A change of sample rate resets the buffer — the two
// rates cannot interleave meaningfully.
func (a *Analyzer) Feed(pcm []byte, sampleRate int) []Frame {
	if sampleRate <= 0 {
		return nil
	}
	if a.rate != sampleRate {
		a.buf = a.buf[:0]
		a.rate = sampleRate
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(pcm[i]) | int16(pcm[i+1])<<8
		a.buf = append(a.buf, float64(s)/32768)
	}

	var frames []Frame
	for len(a.buf) >= fftSize {
		frames = append(frames, analyzeWindow(a.buf[:fftSize]))
		a.buf = a.buf[fftHop:]
	}
	return frames
}

// analyzeWindow turns one 2048-sample window into a frame: Hann-windowed
// FFT reduced to the 32 geometric bands plus the 256-sample waveform.
func analyzeWindow(window []float64) Frame {
	// Hann window, in place on a copy.
	w := make([]float64, fftSize)
	for i := 0; i < fftSize; i++ {
		w[i] = window[i] * (0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize)))
	}

	// Iterative radix-2 FFT (Cooley-Tukey, bit-reversed permutation).
	re, im := make([]float64, fftSize), make([]float64, fftSize)
	copy(re, w)
	for i, j := 1, 0; i < fftSize; i++ {
		bit := fftSize >> 1
		for ; j&bit != 0; bit >>= 1 {
			j &^= bit
		}
		j |= bit
		if i < j {
			re[i], re[j] = re[j], re[i]
		}
	}
	for length := 2; length <= fftSize; length <<= 1 {
		ang := -2 * math.Pi / float64(length)
		wr, wi := math.Cos(ang), math.Sin(ang)
		for i := 0; i < fftSize; i += length {
			curR, curI := 1.0, 0.0
			for j := i; j < i+length/2; j++ {
				uR, uI := re[j], im[j]
				vR := re[j+length/2]*curR - im[j+length/2]*curI
				vI := re[j+length/2]*curI + im[j+length/2]*curR
				re[j], im[j] = uR+vR, uI+vI
				re[j+length/2], im[j+length/2] = uR-vR, uI-vI
				curR, curI = curR*wr-curI*wi, curR*wi+curI*wr
			}
		}
	}

	var f Frame
	// 32 geometric bands over bins 1..1023 — the browser's bandEdgesFor.
	nBins := fftSize / 2
	for b := 0; b < Bands; b++ {
		lo := bandEdge(b, nBins)
		hi := bandEdge(b+1, nBins)
		var sum float64
		for i := lo; i < hi; i++ {
			// |X|/(N/2): a full-scale sine lands at ~0.5 per bin with the
			// Hann window; a band mean of several such bins still peaks
			// near 1 for a concentrated tone.
			sum += math.Hypot(re[i], im[i]) / (fftSize / 2)
		}
		if hi > lo {
			f.FFT[b] = float32(sum / float64(hi-lo))
		}
	}
	// Waveform: 256 samples, each the mean of 8 windowed samples.
	for s := 0; s < Samples; s++ {
		var sum float64
		for i := 0; i < waveStride; i++ {
			sum += w[s*waveStride+i]
		}
		f.Wave[s] = float32(sum / waveStride)
	}
	return f
}

// bandEdge mirrors web/music.js's bandEdgesFor: geometric spacing so the
// bass region keeps resolution.
func bandEdge(b, nBins int) int {
	if b <= 0 {
		return 1
	}
	if b >= Bands {
		return nBins
	}
	e := int(math.Round(math.Exp(float64(b) / Bands * math.Log(float64(nBins-1)))))
	if e < 1 {
		return 1
	}
	return e
}
