package muxrun

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zamber/huemux/internal/mux"
)

type fakeSink struct {
	mu     sync.Mutex
	frames int
	last   map[string]mux.LightState
}

func (f *fakeSink) Apply(_ context.Context, out map[string]mux.LightState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames++
	f.last = out
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.frames
}

func TestRuntimePreviewAndOutput(t *testing.T) {
	lights := []mux.Light{{ID: "a", Name: "A", X: 0.5, Y: 0.5}}
	doc := mux.DefaultDocument(lights)
	prog, err := mux.Compile(doc, lights)
	if err != nil {
		t.Fatal(err)
	}
	sink := &fakeSink{}
	rt := New(lights, sink, Providers{})
	defer rt.Close()
	rt.SetProgram(prog)

	rt.RenderNow(time.Unix(100, 0))
	snap := rt.Snapshot()
	if snap.Tick == 0 || len(snap.Lights) != 1 {
		t.Fatalf("preview snapshot not populated: %+v", snap)
	}
	if rt.Running() {
		t.Fatal("new runtime should be stopped")
	}
	if sink.count() != 0 {
		t.Fatal("stopped runtime wrote to the sink")
	}

	if err := rt.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.count() == 0 {
		t.Fatal("started runtime never wrote a frame")
	}
	rt.Stop()
	if rt.Running() {
		t.Fatal("stop did not clear running")
	}
}

func TestRuntimeStartWithoutProgram(t *testing.T) {
	rt := New(nil, nil, Providers{})
	defer rt.Close()
	if err := rt.Start(); err == nil {
		t.Fatal("expected an error without a program")
	}
}
