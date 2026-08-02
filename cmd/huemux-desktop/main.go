// Command huemux-desktop wraps the same core (internal/config,
// internal/engine, internal/server — all completely unmodified) in an
// Electron shell via go-astilectron, to sidestep browser-variability
// concerns in Milestone 4 (getDisplayMedia() quirks, the
// MediaStreamTrackProcessor main-thread/worker-only split, capture source
// picker inconsistencies) by targeting one known, pinned Chromium instead
// of "whatever browser the user has."
//
// This is deliberately a separate binary/entry point rather than a change
// to cmd/huemux: the plain binary is unaffected in every way — same
// size, same zero-Electron-dependency story, same behavior — and this one
// adds a --headless flag that reproduces it exactly, for running the core
// alone (e.g. on a headless server) without paying for a GUI dependency at
// runtime.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asticode/go-astikit"
	"github.com/asticode/go-astilectron"

	"github.com/zamber/huemux/cmd/shared"
	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/server"
	"github.com/zamber/huemux/internal/ui"
)

var version = "dev"

// electronLog passes astilectron's logging through while keeping the most
// recent lines Electron wrote to its own stderr.
//
// It exists because of how this fails in practice: Chromium explains itself on
// stderr ("Missing X server or $DISPLAY", a sandbox complaint, a missing
// library), astilectron relays that as a log line, and then the Go side
// returns an error describing only its own symptom. Anyone reading the tail of
// the output — which is what gets pasted into a bug report — sees the symptom
// and none of the cause.
type electronLog struct {
	w     io.Writer
	mu    sync.Mutex
	lines []string
}

const electronLogKeep = 15

func (e *electronLog) Write(p []byte) (int, error) {
	const marker = "Stderr says: "
	if i := strings.Index(string(p), marker); i >= 0 {
		line := strings.TrimSpace(string(p)[i+len(marker):])
		if line != "" {
			e.mu.Lock()
			e.lines = append(e.lines, line)
			if len(e.lines) > electronLogKeep {
				e.lines = e.lines[len(e.lines)-electronLogKeep:]
			}
			e.mu.Unlock()
		}
	}
	return e.w.Write(p)
}

// tail renders the retained stderr for appending to an error, or "" when
// Electron said nothing — in which case the error stands on its own rather
// than gaining an empty section.
func (e *electronLog) tail() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.lines) == 0 {
		return ""
	}
	return "\n\nwhat Electron reported:\n  " + strings.Join(e.lines, "\n  ")
}

// electronVersion is pinned rather than left at astilectron's default
// (11.4.3, from 2020 — old enough that MediaStreamTrackProcessor support is
// shaky). A recent stable Electron release ships a Chromium new enough that
// none of the capture-path feature detection in web/app.js and
// web/capture-worker.js is even exercised: the worker-side
// MediaStreamTrackProcessor path is simply always available.
const electronVersion = "43.2.0"

func main() {
	// So /api/diagnostics reports the build, not "dev".
	server.Version = version
	// Always buffer recent log lines in memory, independent of -debug, so
	// /api/diagnostics can produce a report after the fact rather than
	// requiring a restart with logging already enabled.
	debuglog.Capture()

	headless := flag.Bool("headless", false, "run the core service only, no desktop window — identical to plain `huemux`")
	verbose := flag.Bool("verbose", false, "verbose CLI status log (headless mode only)")
	debug := flag.Bool("debug", false, "write a detailed debug log to a file, including Electron's own main-process output — see KNOWN_ISSUES.md for the exact path per OS")
	cfgFlags := appconfig.RegisterFlags(flag.CommandLine)
	flag.Parse()

	if *debug {
		if path, err := debuglog.Enable(); err != nil {
			fmt.Fprintf(os.Stderr, "huemux-desktop: could not enable debug log: %v\n", err)
		} else {
			fmt.Println("huemux-desktop: debug logging enabled, writing to " + path)
		}
	}

	configDir, err := config.Dir()
	if err != nil {
		shared.Fatalf("huemux-desktop", "resolving config dir: %v", err)
	}
	cfg, err := appconfig.Resolve(configDir, cfgFlags)
	if err != nil {
		shared.Fatalf("huemux-desktop", "%v", err)
	}
	for _, w := range cfg.Warnings() {
		fmt.Fprintln(os.Stderr, "huemux-desktop: warning: "+w)
	}
	// Resolved but not yet acted on — see the matching note in cmd/huemux.
	if debuglog.Enabled {
		log.Printf("huemux-desktop: config profile=%s listen=%s:%d auth=%s tls=%s",
			cfg.Profile, cfg.Listen.Host, cfg.Listen.Port, cfg.Auth.Mode, cfg.TLS.Mode)
	}

	store, err := config.NewStore()
	if err != nil {
		shared.Fatalf("huemux-desktop", "loading settings: %v", err)
	}
	favorites, err := config.NewFavoritesStore()
	if err != nil {
		shared.Fatalf("huemux-desktop", "loading favorites: %v", err)
	}

	var eng *engine.Engine
	var lights *lightctl.Service
	if bridge, err := config.LoadBridge(); err == nil {
		eng, lights = server.BuildPaired(cfg, bridge, store, favorites)
	}
	srv := server.New(cfg, store, favorites, eng, lights)
	url, err := srv.ListenAndServe()
	if err != nil {
		shared.Fatalf("huemux-desktop", "starting server: %v", err)
	}
	fmt.Println("huemux-desktop " + version + "  " + url)

	if *headless {
		runHeadless(srv, store, url, *verbose)
		return
	}

	// If the port scan moved us past the default, something is on 7654.
	// Starting a second Electron would collide with the first instance's
	// userData lock (~/.config/Electron SingletonLock) and crash with an
	// opaque "App has crashed" message — see the desktop-build section of
	// KNOWN_ISSUES.md. Probe the default before launching: if anything
	// answers, assume the first instance is alive and exit cleanly rather
	// than starting a second Electron that cannot survive.
	if cfg.Listen.Port == 0 {
		host := cfg.Listen.Host
		if host == "" {
			host = appconfig.DefaultHost
		}
		defaultAddr := net.JoinHostPort(host, strconv.Itoa(appconfig.DefaultPort))
		if srv.Addr != defaultAddr {
			probe := "http://" + defaultAddr + "/api/about"
			if resp, err := httpGet(probe); err == nil {
				resp.Body.Close()
				fmt.Println("huemux-desktop: already running at http://" + defaultAddr)
				os.Exit(0)
			}
		}
	}

	if err := runDesktop(url); err != nil {
		shared.Fatalf("huemux-desktop", "desktop window: %v", err)
	}
	shared.Shutdown(srv.Engine(), store)
}

// runDesktop bootstraps Electron via astilectron and opens a window pointed
// at the already-running local server — the exact same HTTP+WS server the
// plain binary serves to an OS browser, unmodified. astilectron downloads
// Electron + its own bootstrap into a cache directory on first run, which
// needs internet access and ~300MB of disk; both are one-time costs, not
// paid again on subsequent launches.
func runDesktop(url string) error {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	dataDir := filepath.Join(cacheDir, "huemux", "astilectron")

	// Checked before Electron is even started, because the failure it produces
	// otherwise is a wall of Chromium logging ending in "context canceled".
	// This is the single most common way the desktop build fails on Linux —
	// over SSH, in a container, on a headless server — and the fix is a
	// different command, not a bug report.
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return fmt.Errorf("no graphical display: both DISPLAY and WAYLAND_DISPLAY are unset.\n" +
			"  This build opens a desktop window and needs one. Either:\n" +
			"    huemux-desktop --headless    same server, no window\n" +
			"    huemux                       the plain server binary\n" +
			"  and open the printed URL in a browser")
	}

	// Logged unconditionally (cheap, one line) rather than gated behind
	// -debug: session-type/display-server mismatches are exactly the class
	// of bug (see provisioner.go's pipeWireCapturePatch) that's otherwise
	// invisible until someone thinks to ask for `env | grep -i session`.
	log.Printf("huemux-desktop: platform=%s session_type=%q wayland_display=%q x11_display=%q electron=%s cache_dir=%s",
		runtime.GOOS, os.Getenv("XDG_SESSION_TYPE"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY"), electronVersion, dataDir)

	// Electron reports its real failures on its own stderr, which astilectron
	// relays as ordinary log lines. Those lines scroll past and the error the
	// program actually returns is the Go-side symptom — "open window: context
	// canceled" — which says nothing about why. Retaining the stderr tail lets
	// the failure be reported with the cause attached.
	cap := &electronLog{w: os.Stdout}
	l := log.New(cap, "[electron] ", log.LstdFlags)
	a, err := astilectron.New(l, astilectron.Options{
		AppName:           "huemux",
		BaseDirectoryPath: dataDir,
		VersionElectron:   electronVersion,
		SingleInstance:    true,
		ElectronSwitches:  []string{"--user-data-dir=" + filepath.Join(dataDir, "electron-profile")},
	})
	if err != nil {
		return fmt.Errorf("init astilectron: %w", err)
	}
	defer a.Close()
	a.HandleSignals()
	a.SetProvisioner(newPatchingProvisioner(l))

	if err := a.Start(); err != nil {
		return fmt.Errorf("start astilectron (first run downloads Electron %s — needs internet access): %w%s",
			electronVersion, err, cap.tail())
	}

	w, err := a.NewWindow(url, &astilectron.WindowOptions{
		Title:  astikit.StrPtr("HueMux"),
		Width:  astikit.IntPtr(1000),
		Height: astikit.IntPtr(860),
		Center: astikit.BoolPtr(true),
	})
	if err != nil {
		return fmt.Errorf("create window: %w", err)
	}
	if err := w.Create(); err != nil {
		return fmt.Errorf("open window: %w%s", err, cap.tail())
	}
	if os.Getenv("HUEMUX_DEVTOOLS") != "" {
		_ = w.OpenDevTools()
	}

	a.Wait() // blocks until the window/app is closed or a.Quit() is called
	return nil
}

// --- headless mode: byte-for-byte the same loop cmd/huemux/main.go runs,
// duplicated rather than shared so that binary is never touched by this one
// existing. ---

func runHeadless(srv *server.Server, store *config.Store, url string, verbose bool) {
	if !srv.Paired() {
		fmt.Println("not paired yet — open the URL above to pair with your bridge")
	}
	shared.OpenBrowser(url)

	printer := ui.NewPrinter(url)
	printer.Verbose = verbose

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	stdinCh := make(chan string, 1)
	go shared.ReadStdinCommands(stdinCh)

	renderTick := time.NewTicker(250 * time.Millisecond)
	defer renderTick.Stop()

	var lastAreaID string
	for {
		select {
		case <-sigCh:
			shared.Shutdown(srv.Engine(), store)
			return
		case cmd := <-stdinCh:
			if cmd == "q" {
				shared.Shutdown(srv.Engine(), store)
				return
			}
			eng := srv.Engine()
			if eng == nil {
				continue
			}
			switch cmd {
			case "b":
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				eng.Stop(ctx)
				cancel()
			case "r":
				if lastAreaID != "" {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					if err := eng.SelectArea(ctx, lastAreaID); err != nil {
						fmt.Println("reconnect failed:", err)
					}
					cancel()
				}
			}
		case <-renderTick.C:
			eng := srv.Engine()
			if eng == nil {
				cfg := srv.Config()
				if cfg.NeedsEngine() {
					printer.RenderUnpaired(url)
				} else {
					printer.RenderNoEngine(url, string(cfg.Profile), srv.Paired())
				}
				continue
			}
			st := eng.Snapshot()
			if st.AreaID != "" {
				lastAreaID = st.AreaID
			}
			printer.Render(st)
		}
	}
}

// httpGet fetches a URL and returns the response body on 200, or an error.
// 2s timeout — this is a local probe, not a remote fetch.
func httpGet(url string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
