// Package ui is the CLI status readout: a repainting block showing that the
// pipeline is alive without needing to open the browser.
package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/zamber/huemux/internal/engine"
)

// Printer renders engine.Status to stdout, repainting in place on a real
// terminal and falling back to plain appended lines under a service
// manager or when piped, so logs stay readable either way.
type Printer struct {
	Verbose   bool
	isTTY     bool
	lastLines int
	url       string
}

// NewPrinter detects whether stdout is a terminal. url is printed once
// alongside every status block, since it's the thing a person watching this
// actually needs.
func NewPrinter(url string) *Printer {
	isTTY := false
	if fi, err := os.Stdout.Stat(); err == nil {
		isTTY = fi.Mode()&os.ModeCharDevice != 0
	}
	return &Printer{isTTY: isTTY, url: url}
}

// Render prints one status block. On a TTY it repaints in place at whatever
// rate the caller ticks it (the roadmap suggests 4 Hz); otherwise it prints
// a single flat line so it stays grep-able in a log file.
func (p *Printer) Render(s engine.Status) {
	if !p.isTTY || p.Verbose {
		fmt.Println(p.flatLine(s))
		return
	}
	p.repaint(p.block(s))
}

// RenderUnpaired is what's shown before the bridge has been paired — there
// is no engine.Status yet, so it's a distinct, much shorter block rather
// than Render with a zero-valued Status (which would print a bridge IP of
// "" and other nonsense fields with nothing to explain them).
func (p *Printer) RenderUnpaired(url string) {
	line := "huemux " + url + " — not paired yet. Open the URL above in a browser to pair with your bridge."
	if !p.isTTY || p.Verbose {
		fmt.Println(line)
		return
	}
	p.repaint(line)
}

// RenderNoEngine is the readout for a profile that deliberately runs without
// the screen-sync engine. Distinct from RenderUnpaired because the absence of
// an engine means two completely different things: on a sync-capable profile
// it means "pair your bridge", but here it means "working as configured".
// Reusing RenderUnpaired would have told an already-paired lights-only server
// to go and pair itself, forever.
func (p *Printer) RenderNoEngine(url string, profile string, paired bool) {
	line := "huemux " + url + "  profile=" + profile
	if paired {
		line += " — light control ready (screen sync disabled by profile)"
	} else {
		line += " — not paired yet. Open the URL above in a browser to pair with your bridge."
	}
	if !p.isTTY || p.Verbose {
		fmt.Println(line)
		return
	}
	p.repaint(line)
}

func (p *Printer) repaint(block string) {
	if p.lastLines > 0 {
		fmt.Printf("\x1b[%dA", p.lastLines) // cursor up, so we repaint instead of scrolling
	}
	lines := strings.Split(block, "\n")
	for _, l := range lines {
		fmt.Printf("\x1b[2K%s\n", l) // clear line, then print — avoids flicker from a full clear
	}
	p.lastLines = len(lines)
}

func (p *Printer) block(s engine.Status) string {
	var b strings.Builder
	fmt.Fprintf(&b, "huemux                                          %s\n", p.url)

	bridgeState := "disconnected"
	if s.BridgeConnected {
		bridgeState = fmt.Sprintf("connected           handshake %dms", s.HandshakeMS)
	}
	fmt.Fprintf(&b, " bridge     %-20s%s\n", s.BridgeIP, bridgeState)

	area := "—"
	if s.AreaName != "" {
		area = fmt.Sprintf("%s        %s · %d channels", s.AreaName, s.AreaType, s.ChannelCount)
	}
	if s.AreaBusyBy != "" {
		area += fmt.Sprintf("  [in use by %s]", s.AreaBusyBy)
	}
	fmt.Fprintf(&b, " area       %s\n", area)

	streamState := "stopped"
	if s.StreamActive {
		streamState = "active"
	}
	fmt.Fprintf(&b, " stream     %-20s%.1f Hz out          seq %d\n", streamState, float64(s.OutputHz), s.Sent)
	fmt.Fprintf(&b, " capture    %d client(s)         %.1f fps in         %dx%d → %dx%d\n",
		s.CaptureClients, s.InboundFPS, s.CaptureW, s.CaptureH, s.GridW, s.GridH)
	fmt.Fprintf(&b, " pipeline   reactivity %-9.0fbright %.0f%%            sat %.0f%%\n",
		s.Settings.Reactivity, s.Settings.Brightness, s.Settings.Saturation)

	if s.LastError != "" {
		fmt.Fprintf(&b, " error      %s\n", s.LastError)
	}

	b.WriteString("\n")
	for i, z := range s.Zones {
		fmt.Fprintf(&b, " ch%-2d \x1b[48;2;%d;%d;%dm  \x1b[0m #%02x%02x%02x", z.ChannelID, z.R, z.G, z.B, z.R, z.G, z.B)
		if (i+1)%3 == 0 || i == len(s.Zones)-1 {
			b.WriteString("\n")
		} else {
			b.WriteString("   ")
		}
	}
	if len(s.Zones) == 0 {
		b.WriteString(" (no channels yet — select an area)\n")
	}

	b.WriteString("\n q quit  r reconnect  b blackout\n")
	return strings.TrimRight(b.String(), "\n")
}

func (p *Printer) flatLine(s engine.Status) string {
	return fmt.Sprintf("huemux bridge=%s area=%q stream=%v out_hz=%d sent=%d capture_fps=%.1f clients=%d err=%q",
		s.BridgeIP, s.AreaName, s.StreamActive, s.OutputHz, s.Sent, s.InboundFPS, s.CaptureClients, s.LastError)
}
