package hue

import (
	"bytes"
	"testing"
)

// testAreaID is exactly 36 ASCII chars, as Dial requires.
const testAreaID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func streamForTest(areaID string, cs ColorSpace) *Stream {
	return &Stream{cfg: Config{AreaID: areaID, ColorSpace: cs}}
}

func TestEncodePacketHeader(t *testing.T) {
	pkt := streamForTest(testAreaID, ColorSpaceRGB).encode(7, nil)

	if len(pkt) != headerLen {
		t.Fatalf("empty packet length = %d, want headerLen %d", len(pkt), headerLen)
	}
	if !bytes.Equal(pkt[:9], []byte("HueStream")) {
		t.Errorf("magic = %q, want %q", pkt[:9], "HueStream")
	}
	if pkt[9] != 0x02 || pkt[10] != 0x00 {
		t.Errorf("protocol version = [%02x %02x], want [02 00]", pkt[9], pkt[10])
	}
	if pkt[11] != 7 {
		t.Errorf("sequence byte = %d, want 7", pkt[11])
	}
	if pkt[12] != 0x00 || pkt[13] != 0x00 {
		t.Errorf("reserved bytes = [%02x %02x], want [00 00]", pkt[12], pkt[13])
	}
	if pkt[14] != byte(ColorSpaceRGB) {
		t.Errorf("color space byte = %02x, want %02x", pkt[14], ColorSpaceRGB)
	}
	if pkt[15] != 0x00 {
		t.Errorf("reserved byte 15 = %02x, want 00", pkt[15])
	}
	if !bytes.Equal(pkt[16:52], []byte(testAreaID)) {
		t.Errorf("area id field = %q, want %q", pkt[16:52], testAreaID)
	}
}

func TestEncodeLengthScalesWithChannels(t *testing.T) {
	s := streamForTest(testAreaID, ColorSpaceRGB)
	for _, n := range []int{0, 1, 2, 7, maxChannelsPerPacket, 21} {
		chans := make([]Channel, n)
		for i := range chans {
			chans[i] = Channel{ID: uint8(i)}
		}
		pkt := s.encode(0, chans)
		if want := headerLen + channelLen*n; len(pkt) != want {
			t.Errorf("len(%d channels) = %d, want %d", n, len(pkt), want)
		}
	}
}

func TestEncodeSequenceNumber(t *testing.T) {
	s := streamForTest(testAreaID, ColorSpaceRGB)
	for _, seq := range []uint8{0, 1, 42, 127, 255} {
		if pkt := s.encode(seq, nil); pkt[11] != seq {
			t.Errorf("seq %d encoded as %d", seq, pkt[11])
		}
	}
}

func TestEncodeChannelLayout(t *testing.T) {
	s := streamForTest(testAreaID, ColorSpaceRGB)
	chans := []Channel{{ID: 3, R: 0xFF, G: 0x80, B: 0x00}}
	pkt := s.encode(0, chans)

	want := []byte{
		0x03,       // id
		0xFF, 0xFF, // R, high and low byte duplicated
		0x80, 0x80, // G
		0x00, 0x00, // B
	}
	if !bytes.Equal(pkt[headerLen:], want) {
		t.Errorf("channel field = %x, want %x", pkt[headerLen:], want)
	}
}

func TestEncodeMultipleChannels(t *testing.T) {
	s := streamForTest(testAreaID, ColorSpaceRGB)
	chans := []Channel{
		{ID: 1, R: 0x11, G: 0x22, B: 0x33},
		{ID: 2, R: 0xAA, G: 0xBB, B: 0xCC},
	}
	pkt := s.encode(0, chans)
	for i, c := range chans {
		off := headerLen + i*channelLen
		if pkt[off] != c.ID {
			t.Errorf("channel %d id = %d, want %d", i, pkt[off], c.ID)
		}
		if !bytes.Equal(pkt[off+1:off+7], []byte{c.R, c.R, c.G, c.G, c.B, c.B}) {
			t.Errorf("channel %d components = %x", i, pkt[off+1:off+7])
		}
	}
}

func TestEncodeColorSpaceByte(t *testing.T) {
	if pkt := streamForTest(testAreaID, ColorSpaceRGB).encode(0, nil); pkt[14] != byte(ColorSpaceRGB) {
		t.Errorf("RGB color space byte = %02x, want %02x", pkt[14], ColorSpaceRGB)
	}
	if pkt := streamForTest(testAreaID, ColorSpaceXY).encode(0, nil); pkt[14] != byte(ColorSpaceXY) {
		t.Errorf("XY color space byte = %02x, want %02x", pkt[14], ColorSpaceXY)
	}
}

func TestWriteComponentDuplicatesByte(t *testing.T) {
	var b []byte
	b = writeComponent(b, 0xAB)
	if !bytes.Equal(b, []byte{0xAB, 0xAB}) {
		t.Errorf("writeComponent(0xAB) = %x, want [ab ab]", b)
	}
	b = writeComponent(b, 0x00)
	if !bytes.Equal(b, []byte{0xAB, 0xAB, 0x00, 0x00}) {
		t.Errorf("second writeComponent = %x, want [ab ab 00 00]", b)
	}
}
