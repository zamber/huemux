// Package music provides a synthetic audio frame generator for pipeline
// testing. It produces proper 0x02 wire-format frames with a configurable
// bass beat — no real audio hardware needed — so the full analysis→preset→
// output chain can be verified deterministically.
package music

import (
	"math"
	"time"
)

// TestTone generates a single analysis frame representing a bass tone at the
// given BPM and frequency. phase should be monotonically increasing across
// calls (e.g. frame index) to produce a beat that pulses over time.
//
// The returned Frame is identical to what would arrive from the browser's
// Web Audio analyser or the Android PCM→FFT path — the downstream preset
// engine cannot tell them apart.
func TestTone(bpm float64, freqHz float64, phase int) Frame {
	var f Frame

	// Beat envelope: a low-frequency oscillator at the given BPM.
	// One beat = one BPM cycle; 120 BPM = 2 Hz = period of 0.5 s.
	// At ~30 frames/s, one beat spans ~15 frames.
	beatHz := bpm / 60.0
	// 30 fps assumed
	beatPhase := float64(phase) * beatHz / 30.0
	envelope := 0.5 + 0.5*math.Sin(2*math.Pi*beatPhase)

	// Fill the 32 FFT bands. Concentrate energy in the bass region (bands
	// 0–7) since that is what the bass_pulse preset reacts to. A real bass
	// tone at ~60–130 Hz falls into bands 1–5 at 44.1 kHz / 2048-pt FFT
	// (~21.5 Hz per bin).
	loBand := int(freqHz/21.5) - 1
	if loBand < 0 {
		loBand = 0
	}
	hiBand := loBand + 3
	if hiBand >= Bands {
		hiBand = Bands - 1
	}
	for b := 0; b < Bands; b++ {
		if b >= loBand && b <= hiBand {
			// Energy peaks at the center of the activated range and tapers off.
			center := float64(loBand+hiBand) / 2.0
			dist := math.Abs(float64(b)-center) / float64(hiBand-loBand+1)
			shape := math.Max(0, 1.0-2.0*dist)
			f.FFT[b] = float32(envelope * shape * 0.8)
		}
	}

	// Fill the 256 waveform samples with a sine at freqHz, modulated by
	// the beat envelope. Sample rate: 44.1 kHz / waveStride (8) ≈ 5512.5 Hz
	// effective for the downsampled waveform.
	waveHz := freqHz
	for s := 0; s < Samples; s++ {
		t := float64(s) / Samples
		f.Wave[s] = float32(envelope * math.Sin(2*math.Pi*waveHz*t/5512.5) * 0.6)
	}
	return f
}

// TestToneSource returns a function suitable for engine.SetMusicFrameSource.
// It produces a new frame on each call, simulating a continuous audio stream
// at the given BPM and bass frequency. The returned closure tracks its own
// phase counter.
//
// Usage:
//
//	src := music.TestToneSource(100, 80) // 100 BPM, 80 Hz bass
//	eng.SetMusicFrameSource(func() (music.Frame, bool) {
//	    return src(), true
//	})
func TestToneSource(bpm float64, freqHz float64) func() (Frame, bool) {
	phase := 0
	ticker := time.NewTicker(time.Second / 30) // ~30 fps
	_ = ticker                                 // for future rate-limiting; currently caller-driven
	return func() (Frame, bool) {
		f := TestTone(bpm, freqHz, phase)
		phase++
		return f, true
	}
}
