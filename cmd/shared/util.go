// Package shared holds utility functions shared between the huemux and
// huemux-desktop entry points without pulling the desktop binary's
// Electron/astilectron dependencies into the plain build.
package shared

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/zamber/huemux/internal/config"
	"github.com/zamber/huemux/internal/engine"
)

// Shutdown stops the engine (if any) and flushes the settings store.
func Shutdown(eng *engine.Engine, store *config.Store) {
	if eng != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		eng.Stop(ctx)
	}
	store.Flush()
}

// ReadStdinCommands reads line-buffered commands from stdin and sends the
// first character of each non-empty line to out.
func ReadStdinCommands(out chan<- string) {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 {
			out <- line[:1]
		}
	}
}

// OpenBrowser tries to open url in the system browser.
func OpenBrowser(url string) {
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

// Fatalf prints "binary: format" to stderr and exits with code 1.
func Fatalf(binary, format string, args ...any) {
	fmt.Fprintf(os.Stderr, binary+": "+format+"\n", args...)
	os.Exit(1)
}
