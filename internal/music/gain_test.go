package music

import "testing"

func TestApplyGainFloorBoostsBands(t *testing.T) {
	var f Frame
	f.FFT[0] = 0.1
	got := ApplyGainFloor(f, 2.0, 0)
	if want := float32(0.2); got.FFT[0] != want {
		t.Fatalf("FFT[0] = %v after gain 2.0, want %v", got.FFT[0], want)
	}
}

func TestApplyGainFloorClampsAtOne(t *testing.T) {
	var f Frame
	f.FFT[5] = 0.9 // with default gain 2.0 this would exceed 1
	got := ApplyGainFloor(f, 2.0, 0)
	if got.FFT[5] != 1.0 {
		t.Fatalf("FFT[5] = %v, want clamp to 1.0", got.FFT[5])
	}
}

func TestApplyGainFloorSilencesBelowFloor(t *testing.T) {
	var f Frame
	f.FFT[0] = 0.04
	// floor 0.1: 0.04*gain is below the floor, so it must be silenced.
	got := ApplyGainFloor(f, 2.0, 0.1)
	if got.FFT[0] != 0 {
		t.Fatalf("FFT[0] = %v, want 0 (below floor)", got.FFT[0])
	}
	// A band above the floor survives.
	f.FFT[1] = 0.06
	got = ApplyGainFloor(f, 2.0, 0.1)
	if want := float32(0.12); got.FFT[1] != want {
		t.Fatalf("FFT[1] = %v, want %v (above floor)", got.FFT[1], want)
	}
}

func TestApplyGainFloorWaveUntouched(t *testing.T) {
	var f Frame
	for i := range f.Wave {
		f.Wave[i] = float32(i+1) / 256
	}
	got := ApplyGainFloor(f, 5.0, 0)
	for i := range f.Wave {
		if got.Wave[i] != f.Wave[i] {
			t.Fatalf("Wave[%d] = %v, want untouched %v", i, got.Wave[i], f.Wave[i])
		}
	}
}

func TestApplyGainFloorZeroGainSilencesAll(t *testing.T) {
	var f Frame
	f.FFT[0] = 0.5
	got := ApplyGainFloor(f, 0, 0)
	for i := range got.FFT {
		if got.FFT[i] != 0 {
			t.Fatalf("FFT[%d] = %v, want 0 with zero gain", i, got.FFT[i])
		}
	}
}
