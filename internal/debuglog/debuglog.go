// Package debuglog wires up huemux's optional -debug flag. When enabled, it
// captures everything the process would otherwise scatter across the
// terminal and the OS's own crash reporter — the standard logger, any other
// *log.Logger built against os.Stdout/os.Stderr after Enable runs (notably
// cmd/huemux-desktop's astilectron logger, which carries Electron's own
// main-process console output), plain fmt.Println/Fprintf output, and
// unrecovered panics/fatal runtime errors — into one timestamped file. The
// goal is a single attachment for a bug report instead of a multi-step log
// gather (terminal scrollback plus a separately-copied crash trace, etc).
package debuglog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Enabled is true once Enable has run successfully. Checked by callers that
// want to skip building/logging high-frequency diagnostic messages (e.g. a
// per-frame color summary) unless a debug log was actually requested.
var Enabled bool

// Dir returns the OS-appropriate directory for huemux's debug log, creating
// it if necessary. Deliberately distinct from config.Dir() (settings and
// bridge credentials): the XDG Base Directory spec keeps state/log data
// ($XDG_STATE_HOME) separate from config, and macOS/Windows draw the same
// distinction with their own dedicated per-user log locations.
//
//   - Linux/BSD: $XDG_STATE_HOME/huemux, falling back to ~/.local/state/huemux
//   - macOS:     ~/Library/Logs/HueMux
//   - Windows:   %LOCALAPPDATA%\HueMux\logs
func Dir() (string, error) {
	var dir string
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		dir = filepath.Join(home, "Library", "Logs", "HueMux")
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home dir: %w", err)
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		dir = filepath.Join(base, "HueMux", "logs")
	default: // linux and other unix
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			dir = filepath.Join(xdg, "huemux")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home dir: %w", err)
			}
			dir = filepath.Join(home, ".local", "state", "huemux")
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create log dir %s: %w", dir, err)
	}
	return dir, nil
}

// Enable creates a fresh, timestamped log file under Dir() and arranges for
// the whole process's output to reach it, not just calls that already went
// through the standard logger. A new file per launch rather than one
// append-forever log keeps a single bug reproduction's output easy to find
// and hand over without scrolling past unrelated runs.
func Enable() (path string, err error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	path = filepath.Join(dir, "debug-"+time.Now().Format("20060102-150405")+".log")
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("creating %s: %w", path, err)
	}

	// Reassign the package-level os.Stdout/os.Stderr *variables* to the
	// write end of an in-process pipe, tee'd back to the real terminal plus
	// f. Everything that resolves os.Stdout/os.Stderr dynamically at call
	// time — fmt.Println, fmt.Fprintf(os.Stderr, ...), and any *log.Logger
	// built with log.New(os.Stdout, ...) after this point — picks up the
	// swap for free, with no per-call-site changes needed. This has to
	// happen before log.SetOutput below, using the *original* file handles,
	// so the default logger's target isn't the pipe itself (that would tee
	// its own output back through the pipe a second time).
	if err := teeStd(&os.Stdout, f); err != nil {
		return "", fmt.Errorf("redirecting stdout: %w", err)
	}
	if err := teeStd(&os.Stderr, f); err != nil {
		return "", fmt.Errorf("redirecting stderr: %w", err)
	}

	// os.Stdout is now the tee'd pipe, so pointing the default logger at it
	// (rather than building a second io.MultiWriter directly against f)
	// avoids writing every log line into f twice.
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// Unrecovered panics and other fatal runtime errors bypass the
	// os.Stdout/os.Stderr variables entirely (the runtime writes straight to
	// the real fd 2), so they need this separate hook to also land in f.
	// Per the stdlib doc this is "in addition to standard error" — the
	// terminal still gets the crash trace too.
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "huemux: could not enable crash output to debug log: %v\n", err)
	}

	Enabled = true
	return path, nil
}

// teeStd swaps *target (a pointer to the os.Stdout or os.Stderr package
// variable) for the write end of a new pipe, and starts a goroutine copying
// everything written to it into both the file *target pointed at before the
// swap (so terminal output is unaffected) and f (the debug log). The
// goroutine exits on its own when the process does, since the pipe closes
// with it.
func teeStd(target **os.File, f *os.File) error {
	real := *target
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	*target = w
	go func() {
		_, _ = io.Copy(io.MultiWriter(real, f), r)
	}()
	return nil
}

// --- in-memory ring buffer ---------------------------------------------
//
// Everything above writes to a file, and only when -debug was passed. That is
// no use on a phone: there is no command line to pass a flag on, no filesystem
// the user can reach, and by the time something misbehaves it is too late to
// restart with logging enabled.
//
// So the last N lines are always kept in memory, regardless of -debug. The
// cost is a few hundred kilobytes and a mutex on a path that already holds
// one; the benefit is that diagnostics can be produced on demand, after the
// fact, from a button in the UI.

const ringCapacity = 800

var ring = struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}{lines: make([]string, ringCapacity)}

// ringWriter captures whatever is written to the standard logger.
type ringWriter struct{}

func (ringWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line != "" {
			appendLine(line)
		}
	}
	return len(p), nil
}

func appendLine(s string) {
	// Bound the line: a single enormous log entry should not be able to
	// consume the whole buffer and push out everything useful around it.
	if len(s) > 2000 {
		s = s[:2000] + "…(truncated)"
	}
	ring.mu.Lock()
	ring.lines[ring.next] = s
	ring.next = (ring.next + 1) % ringCapacity
	if ring.next == 0 {
		ring.full = true
	}
	ring.mu.Unlock()
}

// Recent returns the buffered lines, oldest first.
func Recent() []string {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if !ring.full {
		out := make([]string, ring.next)
		copy(out, ring.lines[:ring.next])
		return out
	}
	out := make([]string, 0, ringCapacity)
	out = append(out, ring.lines[ring.next:]...)
	out = append(out, ring.lines[:ring.next]...)
	return out
}

// Capture starts recording log output into the ring buffer. Idempotent, and
// safe to call alongside Enable — the two compose, since Enable's file writer
// is added to whatever output is already configured.
func Capture() {
	captureOnce.Do(func() {
		log.SetOutput(io.MultiWriter(log.Writer(), ringWriter{}))
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	})
}

var captureOnce sync.Once
