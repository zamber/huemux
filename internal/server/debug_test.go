package server

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/pipeline"
)

// testConn returns a *Conn backed by a real net.Pipe pair and a channel the
// drain goroutine feeds every binary WS frame it reads. WriteMessage blocks
// until the reader consumes the bytes, so the drain goroutine is what keeps
// the test from deadlocking on a socket nobody reads.
func testConn(t *testing.T) (*Conn, <-chan []byte) {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()
	frames := make(chan []byte, 16)
	go func() {
		defer close(frames)
		br := bufio.NewReader(clientEnd)
		for {
			head := make([]byte, 2)
			if _, err := io.ReadFull(br, head); err != nil {
				return
			}
			n := int(head[1] & 0x7F)
			switch n {
			case 126:
				ext := make([]byte, 2)
				if _, err := io.ReadFull(br, ext); err != nil {
					return
				}
				n = int(binary.BigEndian.Uint16(ext))
			case 127:
				ext := make([]byte, 8)
				if _, err := io.ReadFull(br, ext); err != nil {
					return
				}
				n = int(binary.BigEndian.Uint64(ext))
			}
			payload := make([]byte, n)
			if _, err := io.ReadFull(br, payload); err != nil {
				return
			}
			frames <- payload
		}
	}()
	t.Cleanup(func() {
		serverEnd.Close()
		clientEnd.Close()
	})
	return &Conn{rwc: serverEnd, br: bufio.NewReader(serverEnd), bw: bufio.NewWriter(serverEnd)}, frames
}

// TestPushFrameIncrementsCounters verifies the Android frame path reaches the
// stream counters and the engine — the whole point of routing it through the
// server instead of calling eng.SetFrame directly.
func TestPushFrameIncrementsCounters(t *testing.T) {
	eng := engine.New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	s := New(appconfig.Default(), nil, nil, eng, nil)

	rgb := make([]byte, 4*2*3)
	if err := s.PushFrame(4, 2, rgb); err != nil {
		t.Fatalf("PushFrame: %v", err)
	}
	s.mu.Lock()
	frames := s.framesAccepted
	s.mu.Unlock()
	if frames != 1 {
		t.Fatalf("framesAccepted = %d, want 1", frames)
	}
	cw, ch, gw, gh := eng.CaptureStats()
	if cw != 4 || ch != 2 || gw != 4 || gh != 2 {
		t.Fatalf("capture stats = %dx%d grid %dx%d, want 4x2 grid 4x2", cw, ch, gw, gh)
	}
}

func TestPushFrameGatedOnEngine(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	if err := s.PushFrame(4, 2, make([]byte, 4*2*3)); err == nil {
		t.Fatal("PushFrame without an engine must fail")
	}
	// Bad dims fail even with an engine.
	eng := engine.New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	s = New(appconfig.Default(), nil, nil, eng, nil)
	if err := s.PushFrame(0, 2, nil); err == nil {
		t.Fatal("PushFrame(0,2) must fail")
	}
	if err := s.PushFrame(4, 4, make([]byte, 10)); err == nil {
		t.Fatal("PushFrame with a short buffer must fail")
	}
}

func TestDebugInterval(t *testing.T) {
	cases := []struct {
		hz   int
		want time.Duration
	}{
		{0, time.Second / 10},  // below range → default 10
		{-5, time.Second / 10}, // below range → default 10
		{1, time.Second},
		{10, time.Second / 10},
		{30, time.Second / 30},
		{40, time.Second / 30}, // above range → clamped to 30
	}
	for _, tc := range cases {
		if got := debugInterval(tc.hz); got != tc.want {
			t.Errorf("debugInterval(%d) = %v, want %v", tc.hz, got, tc.want)
		}
	}
}

func TestDownscaleGrid(t *testing.T) {
	landscape := &pipeline.Grid{W: 640, H: 360, Pix: make([]byte, 640*360*3)}
	w, h, rgb := downscaleGrid(landscape, previewMaxEdge)
	if w != 180 || h != 101 {
		t.Fatalf("landscape downscale = %dx%d, want 180x101", w, h)
	}
	if len(rgb) != 180*101*3 {
		t.Fatalf("landscape rgb len = %d, want %d", len(rgb), 180*101*3)
	}

	portrait := &pipeline.Grid{W: 360, H: 640, Pix: make([]byte, 360*640*3)}
	w, h, _ = downscaleGrid(portrait, previewMaxEdge)
	if w != 101 || h != 180 {
		t.Fatalf("portrait downscale = %dx%d, want 101x180", w, h)
	}

	small := &pipeline.Grid{W: 10, H: 10, Pix: make([]byte, 10*10*3)}
	w, h, rgb = downscaleGrid(small, previewMaxEdge)
	if w != 10 || h != 10 || len(rgb) != 10*10*3 {
		t.Fatalf("small downscale = %dx%d len %d, want 10x10 len 300", w, h, len(rgb))
	}

	if w, h, rgb := downscaleGrid(nil, previewMaxEdge); w != 0 || h != 0 || rgb != nil {
		t.Fatalf("nil downscale = %dx%d %v, want 0x0 nil", w, h, rgb)
	}

	// Nearest-neighbour: a 2x1 grid capped to 1 wide picks the first pixel.
	two := &pipeline.Grid{W: 2, H: 1, Pix: []byte{10, 20, 30, 200, 200, 200}}
	w, h, rgb = downscaleGrid(two, 1)
	if w != 1 || h != 1 || len(rgb) != 3 {
		t.Fatalf("2x1→1x1 = %dx%d len %d, want 1x1 len 3", w, h, len(rgb))
	}
	if rgb[0] != 10 || rgb[1] != 20 || rgb[2] != 30 {
		t.Fatalf("nearest-neighbour picked rgb=(%d,%d,%d), want (10,20,30)", rgb[0], rgb[1], rgb[2])
	}
}

// TestMaybeBroadcastPreviewEchoAndRateLimit drives the grid echo end to end:
// preview on → one 180x101 frame; a second call inside the 100 ms window is
// dropped; preview off → nothing ever sent.
func TestMaybeBroadcastPreviewEchoAndRateLimit(t *testing.T) {
	eng := engine.New(config.Bridge{BridgeIP: "192.0.2.10"}, nil)
	eng.UpdateSettings(config.AreaSettings{DebugPreview: true})
	s := New(appconfig.Default(), nil, nil, eng, nil)
	conn, frames := testConn(t)
	s.mu.Lock()
	s.uiConns[conn] = struct{}{}
	s.mu.Unlock()

	grid := &pipeline.Grid{W: 640, H: 360, Pix: make([]byte, 640*360*3)}
	for i := range grid.Pix {
		grid.Pix[i] = byte(i % 251)
	}

	s.maybeBroadcastPreview(grid)
	select {
	case payload := <-frames:
		if payload[0] != previewTypeByte {
			t.Fatalf("echo type = %#x, want %#x", payload[0], previewTypeByte)
		}
		if w, h := int(payload[1]), int(payload[2]); w != 180 || h != 101 {
			t.Fatalf("echo dims = %dx%d, want 180x101", w, h)
		}
		if len(payload) != 3+180*101*3 {
			t.Fatalf("echo len = %d, want %d", len(payload), 3+180*101*3)
		}
	case <-time.After(time.Second):
		t.Fatal("no echo frame arrived")
	}

	// Second call inside the rate-limit window must be dropped.
	s.maybeBroadcastPreview(grid)
	select {
	case payload := <-frames:
		t.Fatalf("rate-limited echo leaked a second frame: len=%d", len(payload))
	case <-time.After(50 * time.Millisecond):
	}

	// Preview off: reset the throttle and confirm nothing is ever sent.
	s.mu.Lock()
	s.lastPreviewAt = time.Time{}
	s.mu.Unlock()
	eng.UpdateSettings(config.AreaSettings{})
	s.maybeBroadcastPreview(grid)
	select {
	case payload := <-frames:
		t.Fatalf("preview-off echo leaked a frame: len=%d", len(payload))
	case <-time.After(50 * time.Millisecond):
	}
}
