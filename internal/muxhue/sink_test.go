package muxhue

import (
	"context"
	"sync"
	"testing"

	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/mux"
)

type fakeApplier struct {
	mu    sync.Mutex
	calls int
	cmds  map[string]lightctl.LightCommand
}

func (f *fakeApplier) ApplyLight(_ context.Context, rid string, cmd lightctl.LightCommand) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.cmds == nil {
		f.cmds = map[string]lightctl.LightCommand{}
	}
	f.cmds[rid] = cmd
	return nil
}

func (f *fakeApplier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestSinkSkipsUnchangedAndSendsOff(t *testing.T) {
	app := &fakeApplier{}
	s := New(app, 200, 2)
	out := map[string]mux.LightState{
		"a": {On: true, Brightness: 0.5, Color: mux.MustHex("#FF0000")},
	}
	if err := s.Apply(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if app.callCount() != 1 {
		t.Fatalf("first apply called %d times", app.callCount())
	}
	if got := app.cmds["a"]; !got.On || got.Brightness < 49 || got.R != 255 {
		t.Fatalf("unexpected command: %+v", got)
	}
	if err := s.Apply(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	if app.callCount() != 1 {
		t.Fatalf("identical state should be skipped, calls=%d", app.callCount())
	}
	off := map[string]mux.LightState{"a": {}}
	if err := s.Apply(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if app.callCount() != 2 {
		t.Fatalf("off should be sent, calls=%d", app.callCount())
	}
	if app.cmds["a"].On {
		t.Fatal("off command still has On=true")
	}
	s.Reset()
	if err := s.Apply(context.Background(), off); err != nil {
		t.Fatal(err)
	}
	if app.callCount() != 3 {
		t.Fatalf("reset should force a resend, calls=%d", app.callCount())
	}
}
