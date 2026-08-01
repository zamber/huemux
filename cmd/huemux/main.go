// Command huemux is the entry point: pairing/areas/test subcommands and
// the default run mode that serves the UI and drives the output loop.
package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
	"github.com/zamber/huemux/internal/engine"
	"github.com/zamber/huemux/internal/hue"
	"github.com/zamber/huemux/internal/lightctl"
	"github.com/zamber/huemux/internal/server"
	"github.com/zamber/huemux/internal/ui"
)

var version = "dev"

func main() {
	// So /api/diagnostics reports the build, not "dev".
	server.Version = version
	// Always buffer recent log lines in memory, independent of -debug, so
	// /api/diagnostics can produce a report after the fact rather than
	// requiring a restart with logging already enabled.
	debuglog.Capture()

	args := os.Args[1:]

	// -debug is stripped out here rather than added as its own switch case
	// below, so it can be combined with any subcommand or with -v/--verbose
	// (e.g. `huemux -debug -v`) instead of being mutually exclusive with them.
	debug := false
	filtered := args[:0]
	for _, a := range args {
		if a == "-debug" || a == "--debug" {
			debug = true
			continue
		}
		filtered = append(filtered, a)
	}
	args = filtered

	if debug {
		if path, err := debuglog.Enable(); err != nil {
			fmt.Fprintf(os.Stderr, "huemux: could not enable debug log: %v\n", err)
		} else {
			fmt.Println("huemux: debug logging enabled, writing to " + path)
		}
	}

	if len(args) == 0 {
		runWithFlags(nil)
		return
	}

	switch args[0] {
	case "pair":
		cmdPair(args[1:])
	case "areas":
		cmdAreas(args[1:])
	case "test":
		cmdTest(args[1:])
	case "-h", "--help":
		usage()
	case "version", "--version":
		fmt.Println("huemux " + version)
	default:
		// Not a subcommand, so this is the run path with flags — including
		// the long-standing -v/--verbose, which is now a registered flag
		// rather than its own switch case so it can be combined with the
		// configuration flags below.
		runWithFlags(args)
	}
}

// runWithFlags parses the run-mode flags, resolves the effective
// configuration (defaults, then app.json, then whichever flags were actually
// passed), and starts the service.
func runWithFlags(args []string) {
	fs := flag.NewFlagSet("huemux", flag.ContinueOnError)
	fs.Usage = usage
	verbose := fs.Bool("verbose", false, "flat one-line status output instead of a repainting block")
	verboseShort := fs.Bool("v", false, "alias for --verbose")
	cfgFlags := appconfig.RegisterFlags(fs)

	if err := fs.Parse(args); err != nil {
		os.Exit(1) // fs already printed the error and usage
	}
	// A leftover positional argument means an unrecognised subcommand. Without
	// this the flag package would happily ignore it and start the server,
	// turning a typo like "huemux aeras" into a silent surprise.
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "huemux: unknown command %q\n\n", fs.Arg(0))
		usage()
		os.Exit(1)
	}

	dir, err := config.Dir()
	if err != nil {
		fatalf("resolving config dir: %v", err)
	}
	cfg, err := appconfig.Resolve(dir, cfgFlags)
	if err != nil {
		fatalf("%v", err)
	}
	for _, w := range cfg.Warnings() {
		fmt.Fprintln(os.Stderr, "huemux: warning: "+w)
	}

	cmdRun(cfg, *verbose || *verboseShort)
}

func usage() {
	fmt.Println(`huemux — screen colour sync for Philips Hue Entertainment areas

Usage:
  huemux pair <bridge-ip>        Register with the bridge (press the link button when prompted)
  huemux areas                   List entertainment areas on the paired bridge
  huemux test <area-id>          Prove the DTLS path works: cycles colors, then a 60s keepalive-only hold
  huemux [--verbose]             Run the service (default: http://127.0.0.1:7654)
  huemux -debug ...              Also write a detailed debug log to a file (path printed on startup;
                                  see KNOWN_ISSUES.md for the exact location per OS) — combine with any
                                  of the above, e.g. "huemux -debug -v"

Configuration flags (run mode; override ~/.config/huemux/app.json):
  --profile=full|lights|sync     Which half of the app to run (default full)
  --listen-host, --listen-port   Bind address (default 127.0.0.1:7654)
  --auth=none|token, --token     Authentication for non-loopback callers
  --tls=off|selfsigned|files     TLS, with --tls-cert / --tls-key for "files"`)
}

// --- pair ------------------------------------------------------------------

func cmdPair(args []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if len(args) == 0 {
		fmt.Println("no bridge IP given; searching for one on the local network...")
		candidates := hue.Discover(ctx)
		if len(candidates) == 0 {
			fmt.Println("no bridge found automatically. Find its IP in the Hue app (Settings → My Bridge) and run:")
			fmt.Println("  huemux pair <bridge-ip>")
			os.Exit(1)
		}
		fmt.Println("found candidate bridge(s):")
		for _, ip := range candidates {
			fmt.Println("  " + ip)
		}
		fmt.Println("run: huemux pair <bridge-ip>")
		os.Exit(1)
	}
	bridgeIP := args[0]

	info, err := hue.BridgeConfig(ctx, bridgeIP)
	if err != nil {
		fatalf("could not contact a bridge at %s: %v", bridgeIP, err)
	}
	if !hue.SupportsEntertainment(info) {
		fatalf("the bridge at %s (model %s) is too old to support Entertainment areas — this needs a square (v2) bridge", bridgeIP, info.ModelID)
	}

	fmt.Printf("found bridge %q (id %s). Press the link button on the bridge now — waiting up to 60s...\n", info.Name, info.BridgeID)

	username, clientkey, err := hue.Pair(ctx, bridgeIP, fmt.Sprintf("huemux#%s", hostname()), 60*time.Second)
	if err != nil {
		fatalf("pairing failed: %v", err)
	}
	if _, err := hex.DecodeString(clientkey); err != nil || len(clientkey) != 32 {
		fatalf("bridge returned an unexpected clientkey (expected 32 hex chars, got %d): this is a bridge-side surprise, not a bug in the hex decode below", len(clientkey))
	}

	if err := config.SaveBridge(config.Bridge{
		BridgeIP:  bridgeIP,
		BridgeID:  info.BridgeID,
		Username:  username,
		ClientKey: clientkey,
	}); err != nil {
		fatalf("saving config: %v", err)
	}

	fmt.Println("paired. Run `huemux areas` to see your entertainment areas.")
}

// --- areas -------------------------------------------------------------------

func cmdAreas(args []string) {
	bridge := mustLoadBridge()
	client := hue.NewClient(bridge.BridgeIP, bridge.Username)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	areas, err := client.ListEntertainmentConfigurations(ctx)
	if err != nil {
		fatalf("listing entertainment areas: %v", err)
	}
	if len(areas) == 0 {
		fmt.Println("no entertainment areas found. Create one in the Hue app: Settings → Entertainment areas.")
		return
	}
	for _, a := range areas {
		busy := ""
		if a.IsActive() {
			busy = "  [in use]"
		}
		fmt.Printf("%s  %-24s %-8s %d channels%s\n", a.ID, a.Metadata.Name, a.ConfigurationType, len(a.Channels), busy)
	}
}

// --- test --------------------------------------------------------------------

func cmdTest(args []string) {
	if len(args) == 0 {
		fatalf("usage: huemux test <area-id>")
	}
	areaID := args[0]
	bridge := mustLoadBridge()
	client := hue.NewClient(bridge.BridgeIP, bridge.Username)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := client.GetEntertainmentConfiguration(ctx, areaID)
	if err != nil {
		fatalf("fetching area: %v", err)
	}
	if cfg.IsActive() {
		fatalf("area is already being streamed to by another application")
	}
	if len(cfg.Channels) == 0 {
		fatalf("area %s has no channels", areaID)
	}
	fmt.Printf("area %q: %d channels\n", cfg.Metadata.Name, len(cfg.Channels))

	turnOnChannelLights(ctx, client, cfg.Channels)

	if err := client.StartStreaming(ctx, areaID); err != nil {
		fatalf("PUT action=start: %v", err)
	}
	fmt.Println("streaming started on the bridge, dialing DTLS...")

	stream, err := hue.Dial(ctx, hue.Config{
		BridgeIP:  bridge.BridgeIP,
		Username:  bridge.Username,
		ClientKey: bridge.ClientKey,
		AreaID:    areaID,
		OutputHz:  20,
	})
	if err != nil {
		_ = client.StopStreaming(ctx, areaID)
		fatalf("dtls handshake: %v", err)
	}
	fmt.Println("handshake OK. Running output loop...")

	runDone := make(chan error, 1)
	go func() { runDone <- stream.Run(ctx) }()

	// stream.Set() only updates in-memory state; it does not itself detect
	// or report a dead output loop. Racing every sleep against runDone is
	// what makes an early failure (e.g. a write error right after the
	// handshake) show up immediately instead of being silently swallowed
	// while the rest of the test script sails on regardless.
	failed := false
	sleepOrFail := func(d time.Duration) bool {
		select {
		case err := <-runDone:
			fmt.Printf("\nERROR: output loop ended early: %v\n", err)
			failed = true
			return false
		case <-time.After(d):
			return true
		}
	}

	colors := []struct {
		name    string
		r, g, b uint8
	}{
		{"red", 255, 0, 0},
		{"green", 0, 255, 0},
		{"blue", 0, 0, 255},
		{"white", 255, 255, 255},
	}
	for _, c := range colors {
		if failed {
			break
		}
		fmt.Printf("-> %s\n", c.name)
		chans := make([]hue.Channel, len(cfg.Channels))
		for i, ch := range cfg.Channels {
			chans[i] = hue.Channel{ID: ch.ChannelID, R: c.r, G: c.g, B: c.b}
		}
		stream.Set(chans)
		sleepOrFail(2 * time.Second)
	}

	if !failed {
		fmt.Println("holding for 60s with NO further color commands — only the output loop's keepalive.")
		fmt.Println("watch the lights: if they revert to their previous state before this ends, the keepalive or rate is wrong.")
		for i := 60; i > 0 && !failed; i-- {
			fmt.Printf("\r  %2ds remaining ", i)
			sleepOrFail(time.Second)
		}
		fmt.Println()
	}

	if !failed {
		cancel() // triggers Stream.Run's fade-to-black shutdown path
		<-runDone
	}
	_ = stream.Close()
	_ = client.StopStreaming(context.Background(), areaID)

	if failed {
		fatalf("milestone 2 test failed: output loop exited early (see error above)")
	}
	fmt.Println("done. If the lights held color for the full 60s and then faded out cleanly, milestone 2 passes.")
}

// turnOnChannelLights makes sure every light behind an area's channels is
// switched on before streaming starts. Entertainment streaming only
// controls color/brightness, never the on/off state, so a physically-off
// bulb would otherwise sit dark through an entire perfect test run with no
// error to explain why. Best-effort: a light that fails to resolve or
// switch on is logged and skipped rather than aborting the test.
func turnOnChannelLights(ctx context.Context, client *hue.Client, channels []hue.EntertainmentChannel) {
	seen := map[string]bool{}
	for _, ch := range channels {
		for _, m := range ch.Members {
			lightRID, err := client.ResolveLightForService(ctx, m.Service.RID)
			if err != nil {
				fmt.Printf("warning: could not resolve light for channel %d: %v\n", ch.ChannelID, err)
				continue
			}
			if seen[lightRID] {
				continue
			}
			seen[lightRID] = true
			if err := client.SetOn(ctx, lightRID, true); err != nil {
				fmt.Printf("warning: could not turn on light %s: %v\n", lightRID, err)
			}
		}
	}
}

// --- run -----------------------------------------------------------------

func cmdRun(cfg appconfig.Config, verbose bool) {
	// Resolved but not yet acted on: profile gating and the non-loopback
	// listen/auth/TLS paths land in later phases (see plans/03). Logged under
	// -debug so a bug report shows what the process actually resolved, rather
	// than what the reporter believes their config file says.
	if debuglog.Enabled {
		log.Printf("huemux: config profile=%s listen=%s:%d auth=%s tls=%s",
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

	// No pairing required up front: if config.LoadBridge fails (not paired
	// yet, or the file is missing/unreadable), the server still starts and
	// serves a web-driven pairing flow over the same /ws connection
	// everything else uses. `huemux pair <ip>` on the command line still
	// works too, for scripting.
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

	fmt.Println("huemux " + version + "  " + url)
	if cfg.Profile != appconfig.ProfileFull {
		fmt.Printf("profile %s — %s\n", cfg.Profile, profileSummary(cfg))
	}
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

	renderTick := time.NewTicker(250 * time.Millisecond) // 4 Hz per ROADMAP Milestone 9
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
				continue // not paired yet; r/b have nothing to act on
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
				// A nil engine means "go and pair" only when the profile
				// wanted one; otherwise it is the configured steady state.
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

// profileSummary describes a non-default profile in one line, so the startup
// output says what was actually switched off rather than leaving the operator
// to infer it from a missing tab.
func profileSummary(cfg appconfig.Config) string {
	switch {
	case !cfg.NeedsEngine():
		return "light control only, screen sync disabled"
	case !cfg.ShowsLightsTab():
		return "screen sync only, light-control UI hidden"
	default:
		return "screen sync and light control"
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
	// A line-buffered "q"/"r"/"b" rather than raw single-keypress input:
	// avoids a terminal-raw-mode dependency for a control surface that has
	// no exit test riding on it (the browser UI is the primary control
	// path). Press Enter after the letter.
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
	_ = cmd.Start() // best-effort; the URL is printed regardless
}

func mustLoadBridge() config.Bridge {
	b, err := config.LoadBridge()
	if err != nil {
		fatalf("not paired yet (or config unreadable: %v). Run: huemux pair <bridge-ip>", err)
	}
	return b
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "huemux: "+format+"\n", args...)
	os.Exit(1)
}
