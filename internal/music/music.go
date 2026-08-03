// Package music holds the inbound audio side of the music-reactivity engine:
// the wire format for browser audio frames and the state of the most recent
// one. The analysis primitives that consume this state (beat detection, band
// splits) build on it; the plan is docs/MUSIC-REACTIVITY.md.
//
// Only the transport interface lives here. The browser does the analysis
// work with the Web Audio API (DP-7), so what crosses the socket is a small
// frame of raw features, not PCM.
package music

import (
	"encoding/binary"
	"math"
	"sync"
)

// TypeByte is the binary-frame message type for audio frames
// (PROTOCOL.md §2). It is distinct from 0x01 (reduced grid) so the two
// captures can share one WebSocket without a dispatcher having to guess.
const TypeByte = 0x02

// Bands and Samples are the fixed sizes of one frame; they are part of the
// wire format and must not change without a protocol version bump.
const (
	Bands   = 32  // FFT magnitude bands, 0 = bass, 31 = treble, each 0..1
	Samples = 256 // downsampled time-domain samples, -1..1
)

// Frame is one audio analysis frame produced by the browser: 32 FFT
// magnitude bands followed by 256 downsampled waveform samples.
type Frame struct {
	FFT  [Bands]float32
	Wave [Samples]float32
}

// frameBytes is the payload size of a valid audio frame: the type byte plus
// one little-endian float32 per value.
const frameBytes = 1 + 4*(Bands+Samples)

// ParseFrame decodes a binary audio frame: type byte 0x02, then the 32 FFT
// bands and 256 wave samples as a flat sequence of little-endian float32
// values. Anything else — wrong type byte, wrong length — reports ok=false
// rather than returning a partial frame: a truncated frame fed to a beat
// detector would silently read as "quiet" data.
func ParseFrame(payload []byte) (Frame, bool) {
	var f Frame
	if len(payload) != frameBytes || payload[0] != TypeByte {
		return f, false
	}
	for i := range f.FFT {
		f.FFT[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[1+4*i:]))
	}
	for i := range f.Wave {
		f.Wave[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[1+4*Bands+4*i:]))
	}
	return f, true
}

// State holds the most recent audio frame. Written by the server's WS read
// loop at ~30 Hz, read by the status push at 1 Hz and — once analysis lands
// — by the preset engine's primitives. The mutex is the entire cost of the
// holder; a channel would just be a queue nobody wants (frames are
// replace-latest by design: the output clock samples the newest analysis,
// it never needs every intermediate frame).
type State struct {
	mu     sync.RWMutex
	frame  Frame
	active bool
	frames uint64 // frames accepted since the source started
}

// New returns an empty State: no source, no frame.
func New() *State { return &State{} }

// Update stores a frame and marks the source active.
func (s *State) Update(f Frame) {
	s.mu.Lock()
	s.frame = f
	s.active = true
	s.frames++
	s.mu.Unlock()
}

// Clear drops the source: no more frames are coming, and whatever was
// stored is stale. Called when the capturing connection disconnects so a
// dead capture cannot keep reporting as live analysis.
func (s *State) Clear() {
	s.mu.Lock()
	s.frame = Frame{}
	s.active = false
	s.frames = 0
	s.mu.Unlock()
}

// Snapshot returns the latest frame, whether a source is active, and the
// frame count in one atomic read, so a consumer cannot observe a frame and
// a count from different instants.
func (s *State) Snapshot() (Frame, bool, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.frame, s.active, s.frames
}
