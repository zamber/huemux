package music

import (
	"testing"
)

// TestTestToneShape verifies that the synthetic frame generator produces
// non-zero bass energy and a recognizable beat envelope.
func TestTestToneShape(t *testing.T) {
	// Collect 60 frames (2 seconds at 30 fps).
	var frames []Frame
	for i := 0; i < 60; i++ {
		frames = append(frames, TestTone(120, 80, i))
	}

	// Bass energy should be non-zero in every frame.
	for i, f := range frames {
		sum := float64(0)
		for b := 0; b < 8; b++ {
			sum += float64(f.FFT[b])
		}
		if sum <= 0 {
			t.Fatalf("frame %d: bass sum is zero, want > 0", i)
		}
	}

	// At 120 BPM (2 Hz), the beat envelope is: early frames (phase 0-4)
	// have higher energy than trough frames (phase ~11-15) because the sine
	// envelope starts at 0, peaks at quarter-cycle, and dips at three-quarter cycle.
	// We compare a peak region against a trough region.
	peak := bassSum(frames, 3, 7)
	trough := bassSum(frames, 11, 15)
	if peak <= trough {
		t.Errorf("peak bass sum %.3f <= trough %.3f — beat envelope may be flat", peak, trough)
	}

	// Waveform samples should also be non-zero.
	nonZero := 0
	for _, f := range frames {
		for _, w := range f.Wave {
			if w != 0 {
				nonZero++
			}
		}
	}
	if nonZero == 0 {
		t.Fatal("all waveform samples are zero")
	}
}

func bassSum(frames []Frame, start, end int) float64 {
	if end > len(frames) {
		end = len(frames)
	}
	var sum float64
	for i := start; i < end; i++ {
		for b := 0; b < 8; b++ {
			sum += float64(frames[i].FFT[b])
		}
	}
	return sum
}

// TestTestToneSource exercises the source closure API.
func TestTestToneSource(t *testing.T) {
	src := TestToneSource(100, 80)
	f1, ok1 := src()
	f2, ok2 := src()
	if !ok1 || !ok2 {
		t.Fatal("source returned ok=false")
	}
	// Two consecutive frames at different phases should differ in at least
	// one value somewhere (beat envelope or waveform).
	differ := false
	for b := 0; b < Bands; b++ {
		if f1.FFT[b] != f2.FFT[b] {
			differ = true
			break
		}
	}
	if !differ {
		for s := 0; s < Samples; s++ {
			if f1.Wave[s] != f2.Wave[s] {
				differ = true
				break
			}
		}
	}
	if !differ {
		t.Error("consecutive frames are identical — beat envelope not advancing")
	}
}
