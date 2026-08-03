package server

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/music"
)

// audioPayload assembles a valid 0x02 audio frame with the given FFT band
// values (rest zero) and a bass-only marker in the wave if bassOn.
func audioPayload(t *testing.T, fft [32]float32) []byte {
	t.Helper()
	p := make([]byte, 1+4*(music.Bands+music.Samples))
	p[0] = music.TypeByte
	for i, v := range fft {
		binary.LittleEndian.PutUint32(p[1+4*i:], math.Float32bits(v))
	}
	return p
}

func TestStatusMessageNoMusic(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("status carried music before any frame: %+v", msg.Music)
	}
}

func TestAudioFrameSourceAndStatus(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)

	c1, c2 := &Conn{}, &Conn{}
	var fft [32]float32
	fft[0] = 0.9 // loud bass

	s.handleAudioFrame(c1, audioPayload(t, fft))

	msg := s.statusMessage(nil)
	if msg.Music == nil || !msg.Music.Active || msg.Music.Frames != 1 {
		t.Fatalf("status after first frame: %+v", msg.Music)
	}
	if msg.Music.FFT[0] != 0.9 {
		t.Fatalf("bass band = %v, want 0.9", msg.Music.FFT[0])
	}

	// A second connection's frames are dropped while c1 owns the source.
	var other [32]float32
	other[31] = 1.0
	s.handleAudioFrame(c2, audioPayload(t, other))
	msg = s.statusMessage(nil)
	if msg.Music.FFT[0] != 0.9 || msg.Music.Frames != 1 {
		t.Fatalf("second source's frame leaked in: %+v", msg.Music)
	}

	// c2 taking over after c1 disconnects works.
	s.removeConn(c1)
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("status kept music after source disconnect: %+v", msg.Music)
	}
	s.handleAudioFrame(c2, audioPayload(t, other))
	msg = s.statusMessage(nil)
	if msg.Music == nil || msg.Music.FFT[31] != 1.0 {
		t.Fatalf("new source's frame not accepted after takeover: %+v", msg.Music)
	}

	// Reconnecting c1 must not resurrect the cleared state.
	s.removeConn(c2)
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("status kept music after second disconnect: %+v", msg.Music)
	}
}

// TestMusicStopClearsState is the regression guard for the stale-analysis
// trap: capture can stop on the page while the WS connection stays open
// (it carries the UI), so the disconnect path never fires and the status
// push would keep reporting the last frame as live forever.
// TestGridFramesDoNotTouchMusicState guards the 0x02/0x01 dispatch: a grid
// frame (type 0x01) must never be interpreted as audio, even when both
// arrive on the same connection.
func TestGridFramesDoNotTouchMusicState(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	c := &Conn{}

	// 3x1 grid of white pixels, exactly the shape handleGridFrame accepts
	// (it then no-ops on the nil engine).
	s.handleGridFrame(c, []byte{0x01, 3, 1, 255, 255, 255, 255, 255, 255})
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("grid frame claimed the music source: %+v", msg.Music)
	}

	// And the reverse: an audio frame must not be readable as a grid frame.
	s.handleAudioFrame(c, audioPayload(t, [32]float32{0.5}))
	if msg := s.statusMessage(nil); msg.Music == nil || !msg.Music.Active {
		t.Fatal("audio frame not accepted after a grid frame")
	}
}

func TestMusicStopClearsState(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	c := &Conn{}
	s.handleAudioFrame(c, audioPayload(t, [32]float32{0.5}))
	if msg := s.statusMessage(nil); msg.Music == nil {
		t.Fatal("expected music state after a frame")
	}

	s.handleControlMessage(c, []byte(`{"type": "music_stop"}`))
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("music block survived music_stop: %+v", msg.Music)
	}

	// A stopped source must not block a fresh one: the next frame from the
	// same connection re-claims the source.
	s.handleAudioFrame(c, audioPayload(t, [32]float32{0.5}))
	if msg := s.statusMessage(nil); msg.Music == nil || !msg.Music.Active {
		t.Fatal("new frames not accepted after music_stop")
	}
}

// TestMusicPresetControlMessage drives the activation path from the WS
// control message down to the engine. No area is selected, so the layout is
// empty — the activation still must succeed and report.
func TestMusicPresetControlMessage(t *testing.T) {
	eng := engine.New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	s := New(appconfig.Default(), nil, nil, eng, nil)
	c := &Conn{}

	s.handleControlMessage(c, []byte(`{"type": "music_preset", "preset": "bass_pulse"}`))
	if got := eng.MusicPreset(); got != "bass_pulse" {
		t.Fatalf("MusicPreset = %q after activation, want bass_pulse", got)
	}

	// Unknown slug: loud failure, preset stays untouched.
	s.handleControlMessage(c, []byte(`{"type": "music_preset", "preset": "nope"}`))
	if got := eng.MusicPreset(); got != "bass_pulse" {
		t.Fatalf("failed activation clobbered the preset: %q", got)
	}

	// Deactivate.
	s.handleControlMessage(c, []byte(`{"type": "music_preset", "preset": ""}`))
	if got := eng.MusicPreset(); got != "" {
		t.Fatalf("preset not cleared: %q", got)
	}
}

// TestPushAudioPCM is the Android internal-audio path in miniature: raw PCM
// over the mobile facade must land in the same music state the browser's
// 0x02 frames write, and show up in the status push with real energy.
func TestPushAudioPCM(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)

	// One second of a 440 Hz sine at 44.1 kHz, little-endian s16 (2 bytes
	// per sample — the buffer size is a common off-by-factor-two here).
	const rate = 44100
	pcm := make([]byte, rate*2) // zeros first: silence
	s.PushAudioPCM(pcm, rate)
	if msg := s.statusMessage(nil); msg.Music == nil || !msg.Music.Active {
		t.Fatalf("silence did not mark the source active: %+v", msg.Music)
	}

	var sum float32
	for i := 0; i < rate; i++ {
		v := int16(math.Sin(2*math.Pi*440*float64(i)/float64(rate)) * 30000)
		pcm[i*2] = byte(v)
		pcm[i*2+1] = byte(v >> 8)
	}
	s.PushAudioPCM(pcm, rate)
	msg := s.statusMessage(nil)
	if msg.Music == nil {
		t.Fatal("no music block after PCM")
	}
	for _, v := range msg.Music.FFT {
		sum += v
	}
	if sum < 0.1 {
		t.Fatalf("440 Hz tone produced no band energy: sum=%v", sum)
	}
	if msg.Music.Wave[0] == 0 {
		t.Fatal("waveform is silent")
	}
}

func TestAudioFrameRejectsMalformed(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	c := &Conn{}

	// Wrong type byte must not claim the source.
	s.handleAudioFrame(c, []byte{0x01, 0, 0, 0, 0})
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("malformed frame accepted: %+v", msg.Music)
	}
	// Truncated frame must not claim it either.
	p := audioPayload(t, [32]float32{})
	s.handleAudioFrame(c, p[:len(p)-1])
	if msg := s.statusMessage(nil); msg.Music != nil {
		t.Fatalf("truncated frame accepted: %+v", msg.Music)
	}
}
