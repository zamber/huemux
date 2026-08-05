package music

// ApplyGainFloor applies the audio-pickup settings to one analysis frame,
// before it reaches the music state. Each FFT band is multiplied by gain,
// silenced when it falls below floor, and clamped to [0,1]. The waveform is
// untouched: the wave display is a raw time-domain trace and boosting it
// would not make the histogram read better — only the bands feed the
// histogram.
//
// Pure and stateless so it can be tested in isolation and called from both
// the browser-frame path and the Android PCM path with identical results.
func ApplyGainFloor(f Frame, gain, floor float64) Frame {
	for b := range f.FFT {
		v := float64(f.FFT[b]) * gain
		if v < floor {
			v = 0
		} else if v > 1 {
			v = 1
		}
		f.FFT[b] = float32(v)
	}
	return f
}
