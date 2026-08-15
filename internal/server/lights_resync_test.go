package server

// Lights-resync regression tests.
//
// The bug this guards: when the Android app is backgrounded, the WebSocket
// either drops or just stops receiving the bridge's eventstream deltas while
// lights change elsewhere (the official Hue app, a wall switch, a schedule).
// The bridge's eventstream has no replay — events are fire-and-forget — so a
// client that reconnects is stuck on whatever it last fetched until the *next*
// light change happens to arrive as a light_event. The server never informed
// a (re)connecting client of the current state, and the frontend never asked.
//
// The fix is two complementary paths, both tested here:
//   - pushLightsSnapshot, invoked from handleWS on every new connection, so a
//     reconnecting client is told the current truth without having to ask.
//   - the resync_lights control message, sent by the frontend as a foreground
//     handshake when the page becomes visible again, in the case where the WS
//     never actually dropped.
//
// Each test runs against a fake CLIP v2 bridge (httptest TLS server) whose
// REST state the test mutates to stand in for "the light changed while the
// client was away".

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/lightctl"
)

// ---------- fake CLIP v2 bridge ----------

// fakeBridge serves the REST resources lightctl reads and holds the
// eventstream open with no events. State is mutable so a test can simulate an
// external light change (the official app) simply by writing new values; the
// next REST read observes them, exactly like a real bridge would.
type fakeBridge struct {
	mu      sync.Mutex
	srv     *httptest.Server
	lights  []hue.Light
	rooms   []hue.Group
	grouped map[string]hue.GroupedLight
}

func newFakeBridge(t *testing.T, lights []hue.Light, rooms []hue.Group, grouped map[string]hue.GroupedLight) *fakeBridge {
	t.Helper()
	fb := &fakeBridge{lights: lights, rooms: rooms, grouped: grouped}
	fb.srv = httptest.NewTLSServer(http.HandlerFunc(fb.serveHTTP))
	t.Cleanup(fb.srv.Close)
	return fb
}

// addr returns host:port for hue.NewClient's BridgeIP. The client is created
// unpinned (empty certSHA256 -> InsecureSkipVerify), which is what accepts the
// httptest server's self-signed certificate.
func (fb *fakeBridge) addr() string {
	return fb.srv.Listener.Addr().String()
}

func (fb *fakeBridge) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/eventstream/clip/v2" {
		fb.serveEventStream(w, r)
		return
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()

	switch {
	case r.URL.Path == "/clip/v2/resource/light":
		writeV2(w, fb.lights)
	case r.URL.Path == "/clip/v2/resource/room":
		writeV2(w, fb.rooms)
	case r.URL.Path == "/clip/v2/resource/zone":
		writeV2(w, []hue.Group{})
	case strings.HasPrefix(r.URL.Path, "/clip/v2/resource/grouped_light/"):
		id := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/grouped_light/")
		gl, ok := fb.grouped[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeV2(w, []hue.GroupedLight{gl})
	default:
		http.NotFound(w, r)
	}
}

// serveEventStream holds the eventstream open with no events. It deliberately
// does not take fb.mu: the connection lives for the whole test, so holding the
// lock here would block every REST read the lightctl service makes — including
// the ones pushLightsSnapshot depends on.
func (fb *fakeBridge) serveEventStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	<-r.Context().Done()
}

func writeV2[T any](w http.ResponseWriter, data []T) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{}, "data": data})
}

// setLightOn flips a light's on/off state, simulating an external change.
func (fb *fakeBridge) setLightOn(id string, on bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for i := range fb.lights {
		if fb.lights[i].ID == id {
			fb.lights[i].On.On = on
			return
		}
	}
}

// setGroupedLightOn flips a room's aggregate grouped_light on/off state.
func (fb *fakeBridge) setGroupedLightOn(id string, on bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	gl, ok := fb.grouped[id]
	if !ok {
		return
	}
	gl.On.On = on
	fb.grouped[id] = gl
}

// ---------- fixtures ----------

func testLight(id, name, device string, on bool, brightness float64) hue.Light {
	// On/Dimming are anonymous struct types with json tags, which makes them
	// distinct from a bare struct{On bool}; set them field-by-field instead
	// of naming the type in a composite literal.
	l := hue.Light{
		ID:       id,
		Owner:    hue.ResourceIdentifier{RID: device},
		Metadata: hue.Metadata{Name: name},
	}
	l.On.On = on
	l.Dimming = &struct {
		Brightness float64 `json:"brightness"`
	}{Brightness: brightness}
	return l
}

func testRoom(id, name string, devices ...string) hue.Group {
	children := make([]hue.ResourceIdentifier, len(devices))
	for i, d := range devices {
		children[i] = hue.ResourceIdentifier{RID: d}
	}
	return hue.Group{
		ID:       id,
		Children: children,
		Services: []hue.ResourceIdentifier{{RID: "gl-" + id, RType: "grouped_light"}},
		Metadata: hue.Metadata{Name: name},
	}
}

func testGroupedLight(id string, on bool, brightness float64) hue.GroupedLight {
	gl := hue.GroupedLight{ID: id}
	gl.On.On = on
	gl.Dimming.Brightness = brightness
	return gl
}

// newLightsServer builds a server whose lightctl service talks to the fake
// bridge, under a full profile so the Lights tab is active. Cleanup cancels
// the eventstream broadcast goroutine (setPaired(nil, nil)) before the fake
// bridge closes, so the held-open eventstream connection does not wedge the
// httptest server's Close.
func newLightsServer(t *testing.T, fb *fakeBridge) *Server {
	t.Helper()
	dir := t.TempDir()
	withConfigDir(t, dir)
	favorites, err := config.NewFavoritesStore()
	if err != nil {
		t.Fatalf("favorites store: %v", err)
	}
	lights := lightctl.New(config.Bridge{
		BridgeIP:   fb.addr(),
		Username:   "testuser",
		CertSHA256: "",
	}, favorites)
	s := New(appconfig.Default(), nil, favorites, nil, lights)
	t.Cleanup(func() {
		s.setPaired(nil, nil) // cancel the eventstream broadcast goroutine
		_ = s.Close()
	})
	return s
}

// ---------- helpers ----------

func readLightsSnapshot(t *testing.T, frames <-chan []byte) lightsSnapshotWire {
	t.Helper()
	select {
	case raw := <-frames:
		var snap lightsSnapshotWire
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatalf("unmarshal lights_snapshot: %v (raw=%s)", err, raw)
		}
		if snap.Type != "lights_snapshot" {
			t.Fatalf("message type = %q, want lights_snapshot (raw=%s)", snap.Type, raw)
		}
		return snap
	case <-time.After(3 * time.Second):
		t.Fatal("no lights_snapshot frame arrived")
		return lightsSnapshotWire{}
	}
}

// ---------- the regression tests ----------

// TestPushLightsSnapshotOnReconnect is the reconnect half of the bug: a client
// connected, saw state A, dropped its connection (app backgrounded), and while
// it was away the light changed to state B elsewhere. On reconnect the server
// must inform the new connection of B — not leave it on the stale A it last
// fetched.
func TestPushLightsSnapshotOnReconnect(t *testing.T) {
	fb := newFakeBridge(t,
		[]hue.Light{testLight("l1", "Lamp", "dev1", true, 80)},
		[]hue.Group{testRoom("room1", "Living", "dev1")},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)

	// First connection: the server informs it of the current state (A: on).
	conn1, frames1 := testConn(t)
	s.pushLightsSnapshot(conn1)
	snap := readLightsSnapshot(t, frames1)
	if len(snap.Lights) != 1 || !snap.Lights[0].On {
		t.Fatalf("first snapshot: light must be on, got %+v", snap.Lights)
	}
	if len(snap.Rooms) != 1 || !snap.Rooms[0].On {
		t.Fatalf("first snapshot: room must be on, got %+v", snap.Rooms)
	}

	// The connection drops (app goes to the background).
	s.removeConn(conn1)

	// While away, the light is turned off by the official Hue app.
	fb.setLightOn("l1", false)
	fb.setGroupedLightOn("gl-room1", false)

	// Reconnect: the server must inform the new connection of the current
	// truth (B: off). Before the fix there was no such push, so this client
	// would keep showing the light on until the next light_event.
	conn2, frames2 := testConn(t)
	s.pushLightsSnapshot(conn2)
	snap = readLightsSnapshot(t, frames2)
	if len(snap.Lights) != 1 || snap.Lights[0].On {
		t.Fatalf("reconnected client still sees the light on; snapshot did not resync: %+v", snap.Lights)
	}
	if len(snap.Rooms) != 1 || snap.Rooms[0].On {
		t.Fatalf("reconnected client still sees the room on; snapshot did not resync: %+v", snap.Rooms)
	}
}

// TestResyncLightsHandshake is the foreground half of the bug: the WS stayed
// up the whole time (the common Android case — the socket survives
// backgrounding; only the deltas stop arriving), so there is no reconnect to
// hang a push on. The frontend's foreground handshake (resync_lights) must
// make the server read the current state fresh from the bridge and send it.
func TestResyncLightsHandshake(t *testing.T) {
	fb := newFakeBridge(t,
		[]hue.Light{testLight("l1", "Lamp", "dev1", true, 80)},
		[]hue.Group{testRoom("room1", "Living", "dev1")},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)

	conn, frames := testConn(t)

	// First foreground handshake: the server answers with the current state.
	s.handleControlMessage(conn, []byte(`{"type":"resync_lights"}`))
	snap := readLightsSnapshot(t, frames)
	if len(snap.Lights) != 1 || !snap.Lights[0].On {
		t.Fatalf("handshake snapshot: light must be on, got %+v", snap.Lights)
	}

	// Light changes elsewhere while the WS stayed up.
	fb.setLightOn("l1", false)
	fb.setGroupedLightOn("gl-room1", false)

	// Resume: the frontend sends the handshake again. The server must answer
	// with the current state, not whatever the client last fetched.
	s.handleControlMessage(conn, []byte(`{"type":"resync_lights"}`))
	snap = readLightsSnapshot(t, frames)
	if len(snap.Lights) != 1 || snap.Lights[0].On {
		t.Fatalf("handshake returned stale light state; resync did not fetch fresh: %+v", snap.Lights)
	}
	if len(snap.Rooms) != 1 || snap.Rooms[0].On {
		t.Fatalf("handshake returned stale room state; resync did not fetch fresh: %+v", snap.Rooms)
	}
}

// TestHandleWSPushesSnapshotOnConnect drives a real WebSocket upgrade through
// handleWS and asserts the server pushes a lights_snapshot to a brand-new
// connection without the client asking. This is the exact reconnect scenario
// from the field — the endpoint that reconnects with stale data must be told
// the current truth.
func TestHandleWSPushesSnapshotOnConnect(t *testing.T) {
	fb := newFakeBridge(t,
		[]hue.Light{testLight("l1", "Lamp", "dev1", true, 80)},
		[]hue.Group{testRoom("room1", "Living", "dev1")},
		map[string]hue.GroupedLight{"gl-room1": testGroupedLight("gl-room1", true, 70)},
	)
	s := newLightsServer(t, fb)
	base, err := s.ListenAndServe()
	if err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	addr := strings.TrimPrefix(base, "http://")

	raw, conn, err := dialWS(t, addr)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer raw.Close()

	type readResult struct {
		op      byte
		payload []byte
		err     error
	}
	resCh := make(chan readResult, 8)
	go func() {
		for {
			op, payload, err := conn.ReadMessage()
			resCh <- readResult{op, append([]byte(nil), payload...), err}
			if err != nil {
				return
			}
		}
	}()

	// handleWS also starts a 1 Hz status push, so skip frames until the
	// snapshot (or a 3 s timeout).
	deadline := time.After(3 * time.Second)
	for {
		select {
		case r := <-resCh:
			if r.err != nil {
				t.Fatalf("ws read: %v", r.err)
			}
			if r.op != opText {
				continue
			}
			var kind struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(r.payload, &kind) != nil || kind.Type != "lights_snapshot" {
				continue
			}
			var snap lightsSnapshotWire
			if err := json.Unmarshal(r.payload, &snap); err != nil {
				t.Fatalf("unmarshal snapshot: %v", err)
			}
			if len(snap.Lights) != 1 || !snap.Lights[0].On {
				t.Fatalf("on-connect snapshot: light must be on, got %+v", snap.Lights)
			}
			if len(snap.Rooms) != 1 || !snap.Rooms[0].On {
				t.Fatalf("on-connect snapshot: room must be on, got %+v", snap.Rooms)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for lights_snapshot pushed on connect")
		}
	}
}

// dialWS performs the RFC 6455 client handshake against addr and returns the
// raw connection (for closing) plus a *Conn that reads server frames.
func dialWS(t *testing.T, addr string) (net.Conn, *Conn, error) {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")) // any 16-byte nonce
	req := "GET /ws HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Origin: http://" + addr + "\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := raw.Write([]byte(req)); err != nil {
		raw.Close()
		return nil, nil, err
	}

	br := bufio.NewReader(raw)
	status, err := br.ReadString('\n')
	if err != nil {
		raw.Close()
		return nil, nil, err
	}
	if !strings.Contains(status, "101") {
		raw.Close()
		return nil, nil, fmt.Errorf("upgrade failed: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			raw.Close()
			return nil, nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	return raw, &Conn{rwc: raw, br: br, bw: bufio.NewWriter(raw)}, nil
}
