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
	// No mobile browser can capture a screen, and MediaProjection is not
	// wired up yet, so screen sync is opt-in on the phone rather than the
	// default — it would otherwise open a DTLS stream nothing can feed. An
	// explicit choice on disk always wins.
	if firstRun {
		cfg.Profile = appconfig.ProfileLights
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

// Stop releases what Start acquired. Safe to call when not started.
//
// Note the HTTP listener itself is not closed — Server has no Shutdown, and
// on Android the process is torn down wholesale rather than being expected to
// keep running cleanly afterwards. What matters here is stopping the DTLS
// stream so the bridge is released promptly instead of waiting for its own
// timeout; leaving an entertainment area held would block the real Hue app.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if srv == nil {
		return
	}
	stopSyncLocked()
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
