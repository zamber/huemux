package server

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// frame assembles one RFC 6455 frame: header, optional 4-byte mask key, and
// masked payload. Enough for readFrame's tests, which need no real socket.
func frame(fin, masked bool, opcode byte, length uint64, payload []byte) []byte {
	var b []byte
	h0 := opcode
	if fin {
		h0 |= 0x80
	}
	b = append(b, h0)

	h1 := byte(0)
	if masked {
		h1 |= 0x80
	}
	switch {
	case length <= 125:
		b = append(b, h1|byte(length))
	case length <= 65535:
		b = append(b, h1|126, byte(length>>8), byte(length))
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, length)
		b = append(b, h1|127)
		b = append(b, ext...)
	}

	maskKey := [4]byte{0x12, 0x34, 0x56, 0x78}
	if masked {
		b = append(b, maskKey[:]...)
		for i := range payload {
			b = append(b, payload[i]^maskKey[i%4])
		}
		return b
	}
	return append(b, payload...)
}

func readFrame(t *testing.T, input []byte) (fin bool, opcode byte, payload []byte, err error) {
	t.Helper()
	c := &Conn{br: bufio.NewReader(bytes.NewReader(input))}
	return c.readFrame()
}

// A hostile length field must be rejected before any allocation, so no
// payload bytes need to be on the wire. 2^64-1 is the exact DoS case from
// the issue: previously it panicked make() with "len out of range".
func TestReadFrameRejectsOversized(t *testing.T) {
	cases := []struct {
		name   string
		length uint64
	}{
		{"one past the limit", maxFrameSize + 1},
		{"2^40", 1 << 40},
		{"2^64-1", ^uint64(0)},
	}
	for _, tc := range cases {
		input := frame(true, false, opBinary, tc.length, nil)
		_, _, _, err := readFrame(t, input)
		if err == nil {
			t.Errorf("%s: oversized frame accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "frame too large") {
			t.Errorf("%s: err = %q, want frame-too-large", tc.name, err)
		}
	}

	// The check must hold for masked frames too, after the mask key is read.
	input := frame(true, true, opBinary, maxFrameSize+1, nil)
	if _, _, _, err := readFrame(t, input); err == nil {
		t.Error("masked oversized frame accepted")
	}
}

// Exactly the limit is still allowed: the check is strict greater-than.
func TestReadFrameAcceptsMaxSize(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5A}, maxFrameSize)
	input := frame(true, false, opBinary, uint64(maxFrameSize), payload)
	_, _, got, err := readFrame(t, input)
	if err != nil {
		t.Fatalf("frame at the limit rejected: %v", err)
	}
	if len(got) != maxFrameSize {
		t.Fatalf("payload len = %d, want %d", len(got), maxFrameSize)
	}
}

// Legitimate frame sizes and the masking path keep working.
func TestReadFrameAllowsNormalSizes(t *testing.T) {
	cases := []struct {
		name    string
		masked  bool
		length  uint64
		payload []byte
	}{
		{"7-bit length", false, 5, []byte("hello")},
		{"16-bit length", false, 300, bytes.Repeat([]byte{0x11}, 300)},
		{"masked small", true, 4, []byte("pong")},
	}
	for _, tc := range cases {
		input := frame(true, tc.masked, opText, tc.length, tc.payload)
		fin, opcode, got, err := readFrame(t, input)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !fin || opcode != opText {
			t.Errorf("%s: fin = %v, opcode = %#x, want fin + text", tc.name, fin, opcode)
		}
		if !bytes.Equal(got, tc.payload) {
			t.Errorf("%s: payload = %q, want %q", tc.name, got, tc.payload)
		}
	}
}
