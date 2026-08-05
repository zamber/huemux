package server

import (
	"time"

	"github.com/zamber/huemux/internal/pipeline"
)

// previewTypeByte is the binary WebSocket message type for the downscaled
// grid echo (PROTOCOL.md §2). Distinct from 0x01 (grid in) and 0x02 (audio
// frame in) so a client can tell the echo apart without a second dispatch
// guess.
const previewTypeByte = 0x03

// previewMaxEdge is the longest dimension of the downscaled echo. The full
// grid can be 640x360; the echo is capped far smaller because it exists to
// drive a preview and a histogram, not to be a faithful stream.
const previewMaxEdge = 180

// previewMinInterval is the minimum time between echo broadcasts: a 10 fps
// cap. The echo is a resource-burn feature behind an explicit checkbox, and
// 10 fps keeps that burn bounded even when the capture pipeline runs at 30.
const previewMinInterval = 100 * time.Millisecond

// maybeBroadcastPreview sends a downscaled copy of the current grid to every
// connected UI when the debug-preview setting is enabled. It is the Android
// stream echo: the WebView is a plain uiConns member (not the frame source),
// so it receives the same echo a desktop browser tab does.
//
// Cheap on the hot path when the preview is off: one engine lookup, one
// settings read, and nothing else.
func (s *Server) maybeBroadcastPreview(grid *pipeline.Grid) {
	eng := s.engine()
	if eng == nil {
		return
	}
	_, preview, _, _ := eng.DebugSettings()
	if !preview {
		return
	}

	s.mu.Lock()
	if time.Since(s.lastPreviewAt) < previewMinInterval {
		s.mu.Unlock()
		return
	}
	s.lastPreviewAt = time.Now()
	conns := make([]*Conn, 0, len(s.uiConns))
	for conn := range s.uiConns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	w, h, rgb := downscaleGrid(grid, previewMaxEdge)
	if w == 0 || h == 0 {
		return
	}
	payload := make([]byte, 3+len(rgb))
	payload[0] = previewTypeByte
	payload[1] = byte(w)
	payload[2] = byte(h)
	copy(payload[3:], rgb)

	// Written outside s.mu: writes block on the socket, and the echo is a
	// high-rate path — holding the connection lock across socket writes
	// would stall every frame-accepting connection. Conn.WriteMessage is
	// writeMu-guarded, so the debug loop, status loop and preview writer
	// never interleave frames on one connection.
	for _, conn := range conns {
		_ = conn.WriteMessage(opBinary, payload)
	}
}

// downscaleGrid shrinks a grid to at most maxEdge on its longest side,
// preserving aspect ratio, via nearest-neighbour sampling. Returns the
// resulting width, height and tightly packed RGB bytes. Returns (0,0,nil)
// for a nil or malformed grid.
func downscaleGrid(grid *pipeline.Grid, maxEdge int) (w, h int, rgb []byte) {
	if grid == nil || grid.W <= 0 || grid.H <= 0 || len(grid.Pix) < grid.W*grid.H*3 {
		return 0, 0, nil
	}
	srcW, srcH := grid.W, grid.H
	w, h = srcW, srcH
	if srcW > srcH {
		if srcW > maxEdge {
			w = maxEdge
			h = srcH * maxEdge / srcW
		}
	} else {
		if srcH > maxEdge {
			h = maxEdge
			w = srcW * maxEdge / srcH
		}
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	rgb = make([]byte, w*h*3)
	for y := 0; y < h; y++ {
		srcY := y * srcH / h
		for x := 0; x < w; x++ {
			srcX := x * srcW / w
			src := (srcY*srcW + srcX) * 3
			dst := (y*w + x) * 3
			rgb[dst] = grid.Pix[src]
			rgb[dst+1] = grid.Pix[src+1]
			rgb[dst+2] = grid.Pix[src+2]
		}
	}
	return w, h, rgb
}
