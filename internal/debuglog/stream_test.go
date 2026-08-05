package debuglog

import "testing"

// TestStreamRingSeparateFromAppRing is the regression guard for the
// "separate channels" ask: per-stream telemetry must land in StreamRecent and
// never leak into the app ring's Recent, and vice-versa — otherwise stream
// summaries swamp the one-off events a diagnostics report is read for.
func TestStreamRingSeparateFromAppRing(t *testing.T) {
	// The rings are package-global and shared across tests, so snapshot the
	// current sizes and assert on relative growth rather than exact contents.
	appBefore := len(Recent())
	streamBefore := len(StreamRecent())

	Streamf("pcm: %d bytes", 1234)
	StreamNote("capture started at 640x360")

	appAfter := len(Recent())
	streamAfter := len(StreamRecent())

	if streamAfter-streamBefore != 2 {
		t.Fatalf("stream ring grew by %d, want 2", streamAfter-streamBefore)
	}
	if appAfter != appBefore {
		t.Fatalf("app ring grew by %d after Streamf/StreamNote, want 0 (leak)", appAfter-appBefore)
	}
}

// TestAppRingNotInStreamRing is the mirror: app-event lines must not appear in
// the stream ring either.
func TestAppRingNotInStreamRing(t *testing.T) {
	appBefore := len(Recent())
	streamBefore := len(StreamRecent())

	Note("auto-activated bass_pulse")
	Infof("capture_mode set to %s", "edges")
	Audiof("some legacy audio line")

	if len(Recent())-appBefore != 3 {
		t.Fatalf("app ring did not grow by 3 (got %d)", len(Recent())-appBefore)
	}
	if len(StreamRecent()) != streamBefore {
		t.Fatalf("stream ring grew after app-only writes, want 0 (leak)")
	}
}

// TestStreamfFormatsArgs checks the format string actually expands.
func TestStreamfFormatsArgs(t *testing.T) {
	Streamf("chunk=%d bytes=%d", 42, 2048)
	recent := StreamRecent()
	if len(recent) == 0 {
		t.Fatal("StreamRecent returned nothing after Streamf")
	}
	got := recent[len(recent)-1]
	want := "stream/ chunk=42 bytes=2048"
	if len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Errorf("last stream line = %q, want it to end with %q", got, want)
	}
}

// TestStreamRingWraps checks the ring cap bounds memory: writing far more than
// the capacity keeps the buffer at capacity, oldest lines first.
func TestStreamRingWraps(t *testing.T) {
	for i := 0; i < streamRingCapacity+50; i++ {
		Streamf("line %d", i)
	}
	recent := StreamRecent()
	if len(recent) != streamRingCapacity {
		t.Fatalf("StreamRecent len = %d, want %d (cap)", len(recent), streamRingCapacity)
	}
	// Oldest surviving line must be the first one that survived the wrap.
	if got, want := recent[0], "stream/ line 50"; len(got) < len(want) || got[len(got)-len(want):] != want {
		t.Errorf("first line = %q, want it to end with %q", got, want)
	}
}
