package music

import (
	"encoding/binary"
	"math"
	"sync"
	"testing"
)

// encode builds a raw 0x02 frame payload from the given values, so tests
// exercise the exact byte layout ParseFrame must decode.
func encode(t *testing.T, fft [Bands]float32, wave [Samples]float32) []byte {
	t.Helper()
	p := make([]byte, frameBytes)
	p[0] = TypeByte
	for i, v := range fft {
		binary.LittleEndian.PutUint32(p[1+4*i:], math.Float32bits(v))
	}
	for i, v := range wave {
		binary.LittleEndian.PutUint32(p[1+4*Bands+4*i:], math.Float32bits(v))
	}
	return p
}

func TestParseFrameRoundTrip(t *testing.T) {
	var fft [Bands]float32
	var wave [Samples]float32
	for i := range fft {
		fft[i] = float32(i) / Bands // 0..~1
	}
	for i := range wave {
		wave[i] = float32(i%7)/3 - 1 // covers negative and positive, incl. -1..1 edges
	}
	got, ok := ParseFrame(encode(t, fft, wave))
	if !ok {
		t.Fatal("valid frame rejected")
	}
	if got.FFT != fft {
		t.Fatalf("fft mismatch: got %v want %v", got.FFT, fft)
	}
	if got.Wave != wave {
		t.Fatalf("wave mismatch: got %v want %v", got.Wave, wave)
	}
}

func TestParseFrameRejects(t *testing.T) {
	var fft [Bands]float32
	fft[0] = 0.5
	valid := encode(t, fft, [Samples]float32{})

	cases := []struct {
		name    string
		payload []byte
	}{
		{"wrong type byte", append([]byte{0x01}, valid[1:]...)},
		{"empty", nil},
		{"type byte only", []byte{TypeByte}},
		{"truncated", valid[:len(valid)-1]},
		{"one byte too long", append(valid, 0)},
	}
	for _, c := range cases {
		if _, ok := ParseFrame(c.payload); ok {
			t.Errorf("%s: malformed frame accepted", c.name)
		}
	}
}

func TestStateLifecycle(t *testing.T) {
	s := New()
	if _, active, frames := s.Snapshot(); active || frames != 0 {
		t.Fatalf("fresh state: active=%v frames=%d, want false/0", active, frames)
	}

	f := Frame{FFT: [Bands]float32{0.5}}
	s.Update(f)
	got, active, frames := s.Snapshot()
	if !active || frames != 1 || got.FFT[0] != 0.5 {
		t.Fatalf("after update: active=%v frames=%d fft0=%v", active, frames, got.FFT[0])
	}

	s.Update(Frame{})
	_, _, frames = s.Snapshot()
	if frames != 2 {
		t.Fatalf("frames after second update = %d, want 2", frames)
	}

	s.Clear()
	if _, active, frames := s.Snapshot(); active || frames != 0 {
		t.Fatalf("after clear: active=%v frames=%d, want false/0", active, frames)
	}
}

// Update and Snapshot run on the WS read loop and the status push
// concurrently in production; a data race here would surface under -race.
func TestStateConcurrent(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			s.Update(Frame{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			s.Snapshot()
		}
	}()
	wg.Wait()
	if _, _, frames := s.Snapshot(); frames != 2000 {
		t.Fatalf("frames = %d, want 2000", frames)
	}
}
