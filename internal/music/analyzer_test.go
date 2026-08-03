package music

import (
	"math"
	"testing"
)

// sinePCM builds little-endian s16 PCM of a sine at the given frequency.
func sinePCM(rate int, hz, seconds float64) []byte {
	n := int(float64(rate) * seconds)
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := int16(math.Sin(2*math.Pi*hz*float64(i)/float64(rate)) * 30000)
		out[i*2] = byte(v)
		out[i*2+1] = byte(v >> 8)
	}
	return out
}

func TestAnalyzerToneLandsInItsBand(t *testing.T) {
	const rate = 44100
	a := &Analyzer{}
	frames := a.Feed(sinePCM(rate, 440, 0.5), rate)
	if len(frames) == 0 {
		t.Fatal("no frames produced from 0.5s of audio")
	}

	// 440 Hz at 44.1 kHz with a 2048-pt FFT sits in bin ~20, band ~13.
	var peakBand int
	for i, v := range frames[len(frames)-1].FFT {
		if v > frames[len(frames)-1].FFT[peakBand] {
			peakBand = i
		}
	}
	if peakBand < 10 || peakBand > 17 {
		t.Fatalf("440 Hz tone peaked in band %d, want ~13-14", peakBand)
	}
	// The tone's band must be clearly louder than the top octave.
	last := frames[len(frames)-1]
	if last.FFT[peakBand] < last.FFT[28]*5 {
		t.Fatalf("tone band %v not dominant over high bands %v", last.FFT[peakBand], last.FFT[28])
	}
}

func TestAnalyzerSilenceIsZero(t *testing.T) {
	const rate = 44100
	a := &Analyzer{}
	frames := a.Feed(make([]byte, rate), rate) // a full second of zeros
	if len(frames) == 0 {
		t.Fatal("silence produced no frames")
	}
	for _, f := range frames {
		for _, v := range f.FFT {
			if v > 1e-6 {
				t.Fatalf("silence produced band energy %v", v)
			}
		}
		for _, v := range f.Wave {
			if math.Abs(float64(v)) > 1e-6 {
				t.Fatalf("silence produced wave sample %v", v)
			}
		}
	}
}

func TestAnalyzerFrameShape(t *testing.T) {
	const rate = 44100
	a := &Analyzer{}
	frames := a.Feed(sinePCM(rate, 1000, 0.2), rate)
	f := frames[len(frames)-1]
	if len(f.FFT) != Bands || len(f.Wave) != Samples {
		t.Fatalf("frame shape %dx%d, want %dx%d", len(f.FFT), len(f.Wave), Bands, Samples)
	}
	for _, v := range f.Wave {
		if math.Abs(float64(v)) > 1 {
			t.Fatalf("wave sample out of range: %v", v)
		}
	}
}

// TestAnalyzerFrameRate: 50% overlap at 2048/1024 gives ~43 frames/s at
// 44.1 kHz; a 1-second feed must produce at least 30 (DP-8's analysis-rate
// floor).
func TestAnalyzerFrameRate(t *testing.T) {
	const rate = 44100
	a := &Analyzer{}
	frames := a.Feed(sinePCM(rate, 440, 1.0), rate)
	if len(frames) < 30 {
		t.Fatalf("1s of audio produced %d frames, want >= 30", len(frames))
	}
}

// Sample-rate changes reset the buffer rather than analysing mixed-rate
// audio.
func TestAnalyzerRateChangeResets(t *testing.T) {
	a := &Analyzer{}
	a.Feed(sinePCM(44100, 440, 0.01), 44100) // partial window
	frames := a.Feed(sinePCM(48000, 440, 0.1), 48000)
	if len(frames) == 0 {
		t.Fatal("no frames after rate change")
	}
	// A full 48000-sample feed at window 2048 / hop 1024 yields
	// floor((4800-2048)/1024)+1 = 3 frames; if the old 44100 samples had
	// been mixed in, the first window would span both rates. The reset is
	// observable as the count starting fresh.
	if len(frames) != 3 {
		t.Fatalf("after rate change: %d frames from 0.1s, want 3", len(frames))
	}
}
