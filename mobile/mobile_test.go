package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
)

// The facade holds process-global state, so tests must not run in parallel
// and each must clean up after itself.
func reset(t *testing.T) string {
	t.Helper()
	Stop()
	dir := t.TempDir()
	t.Cleanup(func() {
		Stop()
		config.SetDir("")
	})
	return dir
}

func TestStartReturnsLoopbackURL(t *testing.T) {
	dir := reset(t)

	url, err := Start(dir)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The entire Android security model rests on this being loopback: the
	// WebView's Origin has to satisfy the same checkOrigin the desktop build
	// uses, with no relaxation.
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Errorf("Start returned %q, want a http://127.0.0.1: URL", url)
	}
	if URL() != url {
		t.Errorf("URL() = %q, want %q", URL(), url)
	}
}

func TestStartIsIdempotent(t *testing.T) {
	dir := reset(t)

	first, err := Start(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Android calls this again after an activity restart; that is routine,
	// not an error, and it must not bind a second port.
	second, err := Start(dir)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if first != second {
		t.Errorf("second Start returned %q, want the existing %q", second, first)
	}
}

func TestStartRequiresConfigDir(t *testing.T) {
	reset(t)
	// os.UserConfigDir() is meaningless on Android, so an empty path is a
	// caller bug worth refusing rather than silently writing somewhere odd.
	if _, err := Start(""); err == nil {
		t.Fatal("Start(\"\") must fail — Android has no default config location")
	}
}

func TestStartUsesInjectedConfigDir(t *testing.T) {
	dir := reset(t)

	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	// Persisting through the facade must land in the injected directory, not
	// in the host's real ~/.config — the whole point of SetDir.
	cfg := appconfig.Default()
	cfg.Profile = appconfig.ProfileSync
	raw, _ := json.Marshal(cfg)
	if err := SetConfigJSON(string(raw)); err != nil {
		t.Fatalf("SetConfigJSON: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, appconfig.FileName)); err != nil {
		t.Errorf("config was not written into the injected dir: %v", err)
	}
}

func TestDefaultsToFullProfile(t *testing.T) {
	dir := reset(t)

	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := ConfigJSON()
	if err != nil {
		t.Fatal(err)
	}
	var cfg appconfig.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	// Was lights-only while Android had no way to capture. MediaProjection
	// now feeds the engine, so the phone gets both halves.
	if cfg.Profile != appconfig.ProfileFull {
		t.Errorf("profile = %q, want %q by default on mobile", cfg.Profile, appconfig.ProfileFull)
	}
}

func TestExistingConfigFileWins(t *testing.T) {
	dir := reset(t)

	// An explicit choice must survive the mobile default above.
	if err := os.WriteFile(filepath.Join(dir, appconfig.FileName),
		[]byte(`{"profile":"full"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := ConfigJSON()
	if !strings.Contains(raw, `"profile":"full"`) {
		t.Errorf("config = %s, want the on-disk profile to win over the mobile default", raw)
	}
}

func TestStartRejectsInvalidStoredConfig(t *testing.T) {
	dir := reset(t)

	if err := os.WriteFile(filepath.Join(dir, appconfig.FileName),
		[]byte(`{"profile":"lites"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(dir); err == nil {
		t.Fatal("Start must refuse an invalid stored config rather than silently defaulting")
	}
}

func TestCallsBeforeStart(t *testing.T) {
	reset(t)

	if IsPaired() {
		t.Error("IsPaired() must be false before Start")
	}
	if URL() != "" {
		t.Error("URL() must be empty before Start")
	}
	if _, err := ConfigJSON(); err == nil {
		t.Error("ConfigJSON must fail before Start")
	}
	if err := SetConfigJSON(`{}`); err == nil {
		t.Error("SetConfigJSON must fail before Start")
	}
	if err := StartSync("area"); err == nil {
		t.Error("StartSync must fail before Start")
	}
	if err := PushFrame(2, 2, make([]byte, 12)); err == nil {
		t.Error("PushFrame must fail before Start")
	}
	// These must be no-ops rather than panics — Android will call them from
	// lifecycle callbacks regardless of what state we are in.
	Stop()
	StopSync()
}

func TestSetConfigJSONRejectsBadInput(t *testing.T) {
	dir := reset(t)
	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, raw string }{
		{"malformed", `{not json`},
		{"invalid profile", `{"profile":"lites"}`},
		{"invalid auth", `{"profile":"full","auth":{"mode":"password"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := SetConfigJSON(tc.raw); err == nil {
				t.Errorf("SetConfigJSON(%s) must fail", tc.raw)
			}
		})
	}
}

func TestPushFrameValidatesDimensions(t *testing.T) {
	dir := reset(t)
	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	// Lights profile has no engine, so every call is ErrNotStarted; the size
	// checks are still worth pinning because MediaProjection is the one caller
	// and a mismatched stride would otherwise corrupt the pipeline silently.
	for _, tc := range []struct {
		name string
		w, h int
		n    int
	}{
		{"zero width", 0, 4, 0},
		{"negative height", 4, -1, 0},
		{"short buffer", 4, 4, 10},
		{"long buffer", 4, 4, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := PushFrame(tc.w, tc.h, make([]byte, tc.n)); err == nil {
				t.Errorf("PushFrame(%d,%d,%d bytes) must fail", tc.w, tc.h, tc.n)
			}
		})
	}
}

func TestStartSyncNeedsSyncProfile(t *testing.T) {
	dir := reset(t)
	if _, err := Start(dir); err != nil { // defaults to lights
		t.Fatal(err)
	}
	err := StartSync("some-area")
	if err == nil {
		t.Fatal("StartSync must fail under a lights-only profile")
	}
	// The message has to say *why*, or the Kotlin side surfaces a mystery.
	if !strings.Contains(err.Error(), "profile") {
		t.Errorf("error %q should explain that the profile disables sync", err)
	}
}

// TestConcurrentCalls is a race-detector target: gomobile invokes these from
// whichever Java thread the caller happens to be on, so the facade has to
// tolerate genuine concurrency rather than assuming a single caller.
func TestConcurrentCalls(t *testing.T) {
	dir := reset(t)
	if _, err := Start(dir); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 5 {
			case 0:
				IsPaired()
			case 1:
				URL()
			case 2:
				_, _ = ConfigJSON()
			case 3:
				_ = PushFrame(2, 2, make([]byte, 12))
			case 4:
				StopSync()
			}
		}(i)
	}
	wg.Wait()
}
