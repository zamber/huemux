package server

import (
	"strings"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/hue"
)

// The keepalive's job: tell a connection that stopped answering apart from one
// that is merely idle, and drop the first. Without it a peer that slept,
// changed network or was killed leaves a socket the server still writes to and
// the client still believes in — the read loop never returns, the conn stays in
// uiConns, and the client shows stale state with no error anywhere. That was
// the reported symptom: a scene recalled in the Hue app never re-tinting the
// lamp cards on a laptop that had been asleep.

// shortenKeepalive drives a whole ping/idle cycle in milliseconds and restores
// the production values afterwards. No test in this package runs in parallel,
// so the package-level knobs are safe to move.
func shortenKeepalive(t *testing.T) {
	t.Helper()
	status, ping, idle := wsStatusInterval, wsPingInterval, wsIdleTimeout
	wsStatusInterval, wsPingInterval, wsIdleTimeout = 10*time.Millisecond, 30*time.Millisecond, 120*time.Millisecond
	t.Cleanup(func() {
		wsStatusInterval, wsPingInterval, wsIdleTimeout = status, ping, idle
	})
}

func keepaliveTestServer(t *testing.T) string {
	t.Helper()
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
	return strings.TrimPrefix(base, "http://")
}

// A peer that stops answering — reads nothing, so it never sends the pong a
// browser would — is dropped once the idle timeout passes.
func TestWSKeepaliveDropsSilentPeer(t *testing.T) {
	shortenKeepalive(t)
	addr := keepaliveTestServer(t)

	raw, conn, err := dialWS(t, addr)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer raw.Close()

	// Read nothing for several idle timeouts. The server's writes to this
	// socket keep succeeding (they fit in the socket buffer), which is exactly
	// why a write error alone is not a sufficient death certificate.
	time.Sleep(6 * wsIdleTimeout)

	// The peer is gone from the server's side: the next read sees the close.
	raw.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return // closed, as it must be
		}
	}
}

// A peer that keeps answering — like a browser, which replies to a ping frame
// on its own, with no page code involved — stays connected indefinitely.
func TestWSKeepaliveKeepsAnsweringPeer(t *testing.T) {
	shortenKeepalive(t)
	addr := keepaliveTestServer(t)

	raw, conn, err := dialWS(t, addr)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer raw.Close()

	// conn.ReadMessage answers ping frames with pongs before it returns, so
	// merely reading here is what a browser does for an idle tab: it never
	// sends a message of its own, and it is never dropped. Compare with
	// TestWSKeepaliveDropsSilentPeer, whose peer reads nothing and therefore
	// never pongs — the pong is the only difference between the two.
	statuses := 0
	deadline := time.Now().Add(6 * wsIdleTimeout)
	for time.Now().Before(deadline) {
		raw.SetReadDeadline(time.Now().Add(2 * time.Second))
		op, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("idle-but-answering peer was dropped after %d status pushes: %v", statuses, err)
		}
		if op == opText && strings.Contains(string(payload), `"status"`) {
			statuses++
		}
	}
	if statuses == 0 {
		t.Fatal("no status pushes arrived, so the connection was not actually up")
	}
}

// A peer that sends its own frames, without waiting to be asked, also counts as
// alive: any frame updates the receive clock.
func TestWSKeepaliveKeepsChattyPeer(t *testing.T) {
	shortenKeepalive(t)
	addr := keepaliveTestServer(t)

	raw, conn, err := dialWS(t, addr)
	if err != nil {
		t.Fatalf("dial ws: %v", err)
	}
	defer raw.Close()

	// Never read: this peer proves liveness only by writing. An unknown
	// control message is enough — handleControlMessage ignores what it does
	// not know, so nothing else happens.
	deadline := time.Now().Add(6 * wsIdleTimeout)
	for time.Now().Before(deadline) {
		if err := conn.WriteMessage(opText, []byte(`{"type":"not-a-real-message"}`)); err != nil {
			t.Fatalf("write: %v", err)
		}
		time.Sleep(wsIdleTimeout / 4)
	}
}
