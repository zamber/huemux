package server

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/debuglog"
)

// Version is the build version, set by the entry points so diagnostics can
// report it. Left as "dev" when unset rather than empty, so a report from an
// unversioned local build says so explicitly.
var Version = "dev"

// handleDiagnostics returns a single plain-text report intended to be sent to
// whoever is debugging the problem.
//
// This exists because the alternative did not work in practice. The -debug
// flag writes a file, which assumes a command line to pass the flag on, a
// filesystem the user can reach, and enough foresight to have enabled it
// before the problem happened. On a phone none of those hold. A button that
// produces one shareable file, after the fact, is the only workable shape.
//
// Loopback-only, for the same reason /api/config's PATCH is: the report
// necessarily contains the bridge address, the local network's view of this
// machine, and recent log lines. That is fine to hand to a maintainer
// deliberately and not something any device on the network should be able to
// pull.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "diagnostics can only be read on the machine HueMux is running on",
			http.StatusForbidden)
		return
	}

	cfg := s.Config()
	var b strings.Builder

	line := func(k string, v any) { fmt.Fprintf(&b, "%-22s %v\n", k+":", v) }

	b.WriteString("HueMux diagnostics\n")
	b.WriteString("==================\n\n")
	line("generated", time.Now().Format(time.RFC3339))
	line("version", Version)
	line("go", runtime.Version())
	line("os/arch", runtime.GOOS+"/"+runtime.GOARCH)
	line("cpus", runtime.NumCPU())

	b.WriteString("\nconfiguration\n-------------\n")
	line("profile", cfg.Profile)
	line("listen", fmt.Sprintf("%s:%d", cfg.Listen.Host, cfg.Listen.Port))
	line("bound address", s.Addr)
	line("auth mode", cfg.Auth.Mode)
	// Never the token itself. Whether one is set is diagnostic; the value is a
	// credential, and this report is meant to be pasted into an issue.
	line("auth token set", cfg.Auth.Token != "")
	line("loopback exempt", cfg.Auth.AllowLoopbackUnauthenticated)
	line("tls mode", cfg.TLS.Mode)

	if dir, err := config.Dir(); err == nil {
		line("config dir", dir)
	}

	b.WriteString("\nstate\n-----\n")
	line("paired", s.Paired())
	line("sync engine", s.Engine() != nil)
	line("light control", s.lights() != nil)
	s.mu.Lock()
	line("ws clients", len(s.uiConns))
	line("frame source held", s.frameSource != nil)
	s.mu.Unlock()

	if eng := s.Engine(); eng != nil {
		snap := eng.Snapshot()
		line("bridge", snap.BridgeIP)
		line("bridge connected", snap.BridgeConnected)
		line("area", snap.AreaName)
		line("streaming", snap.StreamActive)
		line("inbound fps", fmt.Sprintf("%.1f", snap.InboundFPS))
		if snap.LastError != "" {
			line("last error", snap.LastError)
		}
	}

	b.WriteString("\nrecent log\n----------\n")
	lines := debuglog.Recent()
	if len(lines) == 0 {
		b.WriteString("(nothing logged yet)\n")
	} else {
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}

	b.WriteString("\n-- end of report --\n")

	name := fmt.Sprintf("huemux-diagnostics-%s.txt", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	// Offered as a download so the Android WebView's download listener can
	// write it somewhere the user can actually attach it from.
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write([]byte(b.String()))
}
