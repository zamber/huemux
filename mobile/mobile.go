// Package mobile is the gomobile-bound surface of HueMux: the whole Go core,
// presented as a handful of functions Kotlin can call.
//
// # Why this package exists
//
// `gomobile bind` only exports a restricted set of types — signed integers,
// floats, string, bool, []byte, error, and types defined in the bound package
// itself. It cannot export internal/server.Server, appconfig.Config, or
// anything else with a real Go shape. So rather than contorting those packages
// into something bindable, this one presents a deliberately flat facade and
// keeps the awkwardness in a single file that exists only for this purpose.
//
// # What runs on the phone
//
// All of it. The Android app starts this server in-process and points a
// WebView at the loopback URL Start returns, which is the same move
// cmd/huemux-desktop makes with an Electron window. That is what lets the
// phone reuse pairing, CLIP v2, DTLS, the colour pipeline, favorites, and the
// entire UI without reimplementing any of it in Kotlin — and it means the
// loopback-only security model carries over untouched, since the WebView's
// Origin really is 127.0.0.1.
//
// # Threading
//
// Every exported function is safe to call from any thread; gomobile invokes
// them from whichever Java/Kotlin thread the caller is on. State is guarded by
// mu below.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/pipeline"
	"github.com/zamber/huemux/internal/server"
)

var (
	mu      sync.Mutex
	srv     *server.Server
	baseURL string
)

// ErrNotStarted is returned by everything that needs a running server.
var ErrNotStarted = errors.New("huemux: not started")

// Start boots the HueMux server against configDir and returns the base URL to
// point a WebView at, e.g. "http://127.0.0.1:7654".
//
// configDir must be an app-private directory supplied by the Android
// framework (Context.getFilesDir().getAbsolutePath()) — os.UserConfigDir() is
// meaningless on Android and an app cannot write outside its sandbox.
//
// Calling Start on an already-started server returns the existing URL rather
// than erroring: Android will happily call this again after an activity
// restart, and that is not a mistake worth failing.
func Start(configDir string) (string, error) {
	mu.Lock()
	defer mu.Unlock()

	if srv != nil {
		return baseURL, nil
	}
	if configDir == "" {
		return "", errors.New("huemux: configDir is required on Android")
	}
	config.SetDir(configDir)
	// On Android this is the only way logs are ever obtainable: no command
	// line for -debug, no reachable filesystem. The settings screen's
	// diagnostics button reads this buffer.
	debuglog.Capture()
	server.Version = "android"

	// Whether a config file exists has to be checked before Load, not
	// inferred from what it returns: Load merges over appconfig.Default(), so
	// a fresh install comes back fully populated with profile "full" and is
	// indistinguishable from a file that says so. Testing cfg.Profile == ""
	// therefore never fired, and the mobile default below silently did
	// nothing.
	_, statErr := os.Stat(filepath.Join(configDir, appconfig.FileName))
	firstRun := errors.Is(statErr, os.ErrNotExist)

	cfg, err := appconfig.Load(configDir)
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	// Full profile on first run, now that MediaProjection feeds the engine.
	//
	// This defaulted to lights-only while there was no way to capture on
	// Android: a Sync tab that could not sync was worse than no tab. That is
	// no longer true — the Kotlin side captures the screen and calls PushFrame
	// directly — so the phone gets both halves like every other platform. An
	// explicit choice on disk still wins.
	if firstRun {
		cfg.Profile = appconfig.ProfileFull
	}
	if err := cfg.Validate(); err != nil {
		return "", fmt.Errorf("invalid config: %w", err)
	}

	store, err := config.NewStore()
	if err != nil {
		return "", fmt.Errorf("open settings: %w", err)
	}
	favorites, err := config.NewFavoritesStore()
	if err != nil {
		return "", fmt.Errorf("open favorites: %w", err)
	}

	var eng *engine.Engine
	var lights *lightctl.Service
	if bridge, err := config.LoadBridge(); err == nil {
		eng, lights = server.BuildPaired(cfg, bridge, store, favorites)
	}

	s := server.New(cfg, store, favorites, eng, lights)
	url, err := s.ListenAndServe()
	if err != nil {
		return "", fmt.Errorf("start server: %w", err)
	}
	srv, baseURL = s, url
	return url, nil
}

// Stop releases what Start acquired: the DTLS stream first, so the bridge is
// not left holding an entertainment area (which would block the real Hue app
// until it timed out), then the HTTP listener so the port is free for the
// next Start. Safe to call when not started.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return
	}
	stopSyncLocked()
	_ = srv.Close()
	srv, baseURL = nil, ""
}

// URL returns the base URL, or "" if not started.
func URL() string {
	mu.Lock()
	defer mu.Unlock()
	return baseURL
}

// IsPaired reports whether a bridge has been paired. The Android UI uses this
// to decide whether to show a first-run hint; the web UI discovers the same
// thing over its own WebSocket.
func IsPaired() bool {
	mu.Lock()
	defer mu.Unlock()
	return srv != nil && srv.Paired()
}

// ConfigJSON returns the effective configuration as JSON.
//
// JSON rather than a bound struct because gomobile cannot export
// appconfig.Config, and hand-writing a getter per field would mean touching
// this file every time the schema gains one. The settings UI on Android is a
// WebView over the same page a desktop browser gets, so this is mainly for a
// native settings screen or diagnostics.
func ConfigJSON() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return "", ErrNotStarted
	}
	raw, err := json.Marshal(srv.Config())
	if err != nil {
		return "", fmt.Errorf("encode config: %w", err)
	}
	return string(raw), nil
}

// SetConfigJSON persists a new configuration to configDir. It does not apply
// to the running server: listen address and TLS are fixed at bind time, and
// silently accepting a change that only takes effect on next launch would be
// worse than saying so. Restart the app after calling this.
func SetConfigJSON(raw string) error {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return ErrNotStarted
	}
	var cfg appconfig.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	return appconfig.Save(dir, cfg)
}

// --- screen sync (M3) ------------------------------------------------------

// StartSync selects an entertainment area and opens the DTLS stream, so that
// PushFrame has somewhere to send frames.
//
// Deliberately bypasses the WebSocket frame-source arbitration in
// internal/server: that machinery exists to arbitrate between competing
// browser clients, and on a phone there is exactly one capture source by
// construction. See plans/02-android-standalone.md.
func StartSync(areaID string) error {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return ErrNotStarted
	}
	eng := srv.Engine()
	if eng == nil {
		return errors.New("huemux: screen sync is disabled by the current profile")
	}
	if areaID == "" {
		return errors.New("huemux: areaID is required")
	}
	// Bounded rather than context.Background(): SelectArea dials DTLS and
	// waits on the bridge, and a hung call here would wedge the mutex that
	// every other exported function needs.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return eng.SelectArea(ctx, areaID)
}

// StopSync ends the stream and fades the lights out, without stopping the
// server.
func StopSync() {
	mu.Lock()
	defer mu.Unlock()
	stopSyncLocked()
}

// PushFrame hands one captured frame to the colour pipeline. w*h*3 bytes of
// tightly packed RGB, row-major — the same grid the browser capture worker
// produces, minus its 3-byte header, since Kotlin calls straight into Go here
// and there is no wire to frame.
//
// Cheap by design: MediaProjection delivers frames continuously and this is
// called from that hot path, so it validates, copies, and returns. The copy is
// not optional — the caller owns the backing array and will reuse it for the
// next frame, while the pipeline holds onto what it is given.
func PushFrame(w, h int, rgb []byte) error {
	mu.Lock()
	eng := (*engine.Engine)(nil)
	if srv != nil {
		eng = srv.Engine()
	}
	mu.Unlock()

	if eng == nil {
		return ErrNotStarted
	}
	if w <= 0 || h <= 0 {
		return fmt.Errorf("huemux: bad frame size %dx%d", w, h)
	}
	if want := w * h * 3; len(rgb) != want {
		return fmt.Errorf("huemux: frame is %d bytes, want %d for %dx%d RGB", len(rgb), want, w, h)
	}
	eng.SetFrame(&pipeline.Grid{W: w, H: h, Pix: append([]byte(nil), rgb...)})
	return nil
}

func stopSyncLocked() {
	if srv == nil {
		return
	}
	if eng := srv.Engine(); eng != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		eng.Stop(ctx)
	}
}

// SetHostInfo hands the Go side a block of text describing state only the host
// application can see, for inclusion in the diagnostics report. On Android
// that is the capture and recording state: the MediaProjection, the virtual
// display and the encoder all live in Kotlin, so when recording fails, it
// fails somewhere this process cannot observe. Before this existed, a
// diagnostics report from a phone with broken recording said nothing at all
// about recording.
//
// Safe before Start: the storage is package-level and independent of the
// server, so the host can report a failure that happened during startup.
func SetHostInfo(text string) {
	server.SetHostInfo(text)
}

// LogHost records one line in the same in-memory ring the diagnostics report
// prints, so host-side events appear in order alongside the Go ones.
//
// Meant for state changes and failures — capture started at this size, the
// encoder refused that one — not per-frame chatter. The ring holds a few
// hundred lines and this shares it with everything else.
func LogHost(line string) {
	debuglog.Note("huemux/host: " + line)
}
