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
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/asticode/go-astikit"
	"github.com/asticode/go-astilectron"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/server"
	"github.com/zamber/huemux/internal/ui"
)

var version = "dev"

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
		fatalf("resolving config dir: %v", err)
	}
	cfg, err := appconfig.Resolve(configDir, cfgFlags)
	if err != nil {
		fatalf("%v", err)
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
		fatalf("loading settings: %v", err)
	}
	favorites, err := config.NewFavoritesStore()
	if err != nil {
		fatalf("loading favorites: %v", err)
	}

	var eng *engine.Engine
	var lights *lightctl.Service
	if bridge, err := config.LoadBridge(); err == nil {
		eng, lights = server.BuildPaired(cfg, bridge, store, favorites)
	}
	srv := server.New(cfg, store, favorites, eng, lights)
	url, err := srv.ListenAndServe()
	if err != nil {
		fatalf("starting server: %v", err)
	}
	fmt.Println("huemux-desktop " + version + "  " + url)

	if *headless {
		runHeadless(srv, store, url, *verbose)
		return
	}

	if err := runDesktop(url); err != nil {
		fatalf("desktop window: %v", err)
	}
	shutdown(srv.Engine(), store)
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

	// Logged unconditionally (cheap, one line) rather than gated behind
	// -debug: session-type/display-server mismatches are exactly the class
	// of bug (see provisioner.go's pipeWireCapturePatch) that's otherwise
	// invisible until someone thinks to ask for `env | grep -i session`.
	log.Printf("huemux-desktop: platform=%s session_type=%q wayland_display=%q x11_display=%q electron=%s cache_dir=%s",
		runtime.GOOS, os.Getenv("XDG_SESSION_TYPE"), os.Getenv("WAYLAND_DISPLAY"), os.Getenv("DISPLAY"), electronVersion, dataDir)

	l := log.New(os.Stdout, "[electron] ", log.LstdFlags)
	a, err := astilectron.New(l, astilectron.Options{
		AppName:           "huemux",
		BaseDirectoryPath: dataDir,
		VersionElectron:   electronVersion,
		SingleInstance:    true,
	})
	if err != nil {
		return fmt.Errorf("init astilectron: %w", err)
	}
	defer a.Close()
	a.HandleSignals()
	a.SetProvisioner(newPatchingProvisioner(l))

	if err := a.Start(); err != nil {
		return fmt.Errorf("start astilectron (first run downloads Electron %s — needs internet access): %w", electronVersion, err)
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
		return fmt.Errorf("open window: %w", err)
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
	openBrowser(url)

	printer := ui.NewPrinter(url)
	printer.Verbose = verbose

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	stdinCh := make(chan string, 1)
	go readStdinCommands(stdinCh)

	renderTick := time.NewTicker(250 * time.Millisecond)
	defer renderTick.Stop()

	var lastAreaID string
	for {
		select {
		case <-sigCh:
			shutdown(srv.Engine(), store)
			return
		case cmd := <-stdinCh:
			if cmd == "q" {
				shutdown(srv.Engine(), store)
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

func shutdown(eng *engine.Engine, store *config.Store) {
	if eng != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		eng.Stop(ctx)
	}
	store.Flush()
}

func readStdinCommands(out chan<- string) {
	// Line-buffered "q"/"r"/"b" rather than raw single-keypress input — see
	// the identical comment in cmd/huemux/main.go.
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 {
			out <- line[:1]
		}
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "huemux-desktop: "+format+"\n", args...)
	os.Exit(1)
}
