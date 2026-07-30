package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/asticode/go-astikit"
	"github.com/asticode/go-astilectron"
)

// displayMediaPatch is spliced into astilectron's vendored Electron
// main-process index.js, right at the top of its onReady() function.
//
// Why this exists at all: Electron removed renderer-process access to
// desktopCapturer around v20 (it's main-process-only now — confirmed by
// hand: `require('electron').desktopCapturer` is `undefined` in a window's
// devtools console even with nodeIntegration on). That means a page calling
// the ordinary web-standard getDisplayMedia() has nothing to talk to unless
// the *main* process registers a session-level handler for it first.
// Registering one here means the existing browser-facing code path in
// web/app.js's startBrowserCapture (plain getDisplayMedia(), no
// Electron-specific branch) works completely unchanged: Electron intercepts
// the call before any picker UI would show, and just hands back the
// primary screen.
//
// v1 always returns the primary screen — see the matching note in
// web/app.js. A real source picker is a natural follow-up.
const displayMediaPatch = `function onReady () {
    // lightsync: patched in by cmd/huemux-desktop's custom provisioner
    // (see provisioner.go) so getDisplayMedia() works with no picker UI.
    // desktopCapturer is main-process-only; this is the only place it can
    // be wired up to what a page's getDisplayMedia() call receives.
    //
    // NOTE the "lightsync:" prefix on this comment (and the pipeWireCapturePatch
    // one below) is an idempotency marker patchIndexJS checks for literally —
    // kept as-is across the HueMux rename specifically so it keeps matching
    // installs already patched before the rename, rather than double-patching
    // them. Not a leftover, not a bug: see pipeWireCapturePatchMarker's comment.
    try {
        electron.session.defaultSession.setDisplayMediaRequestHandler(function (request, callback) {
            electron.desktopCapturer.getSources({ types: ['screen'] }).then(function (sources) {
                callback(sources.length ? { video: sources[0], audio: 'loopback' } : {})
            }).catch(function () { callback({}) })
        }, { useSystemPicker: false })
    } catch (e) {
        console.error('huemux: setDisplayMediaRequestHandler failed:', e)
    }
`

const onReadyMarker = "function onReady () {"

// electronDestructureMarker is the line right after `require('electron')`
// that pulls app out of it — the earliest point in the file where `app` is
// available, and (critically) still well before app.whenReady()/the "ready"
// event fires later in this same file. Command-line switches — including
// the Ozone/PipeWire one below — are read by Chromium at startup and mostly
// have no effect if set any later than this, so they cannot go in the
// onReady patch above.
const electronDestructureMarker = "const {app, BrowserWindow, ipcMain, Menu, MenuItem, Tray, dialog, Notification} = electron"

// pipeWireCapturePatch enables real screen capture on a Wayland session.
// Without WebRTCPipeWireCapturer, desktopCapturer.getSources() under
// Wayland doesn't error — it just doesn't return real screen content, and
// what actually reaches the page's <video> is a fixed placeholder frame
// (observed on a real Wayland desktop: solid green, unconditionally,
// regardless of what's on screen — much harder to diagnose than an
// outright failure would have been, since nothing throws or logs).
//
// This does not restore the "no picker at all" UX the onReady patch above
// gets on X11: Wayland's own security model requires a one-time compositor
// consent dialog (via xdg-desktop-portal) before any app can capture the
// screen, which — unlike X11 — cannot be bypassed by picking a source
// programmatically. That's a platform constraint, not something fixable
// from this side.
const pipeWireCapturePatch = `const {app, BrowserWindow, ipcMain, Menu, MenuItem, Tray, dialog, Notification} = electron
// lightsync: pipewire capture switch, patched in by cmd/huemux-desktop's
// custom provisioner (see provisioner.go's pipeWireCapturePatch).
app.commandLine.appendSwitch('enable-features', 'WebRTCPipeWireCapturer')
app.commandLine.appendSwitch('ozone-platform-hint', 'auto')
`

// pipeWireCapturePatchMarker is deliberately distinct from the onReady
// patch's own "lightsync: patched in by" marker text below — reusing the
// same substring in both would make patchIndexJS unable to tell the two
// patches apart (caught by a throwaway idempotency test against a real,
// already-once-patched cache file before this constant existed). Both
// markers keep their pre-rename "lightsync:" prefix on purpose — renaming
// either to "huemux:" would make this program unable to recognize an
// install already patched under the old name, and re-patch (not
// double-patch, thanks to the bytes.Contains checks below, but there's no
// upside to forcing that) on every HueMux user's very first post-rename
// launch for zero visible benefit, since nothing here is ever user-facing.
const pipeWireCapturePatchMarker = "lightsync: pipewire capture switch"

// patchIndexJS splices both the PipeWire-capture and display-media patches
// into the astilectron index.js at indexJSPath. Idempotent per-patch: each
// checks its own marker comment independently, so a partially-patched file
// (e.g. from an interrupted previous run) still gets whichever half it's
// missing rather than being skipped or double-patched.
func patchIndexJS(indexJSPath string) error {
	raw, err := os.ReadFile(indexJSPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", indexJSPath, err)
	}

	patched := raw
	if !bytes.Contains(patched, []byte(pipeWireCapturePatchMarker)) {
		next := bytes.Replace(patched, []byte(electronDestructureMarker), []byte(pipeWireCapturePatch), 1)
		if bytes.Equal(next, patched) {
			return fmt.Errorf("marker %q not found in %s — astilectron's bundled index.js has likely changed shape upstream", electronDestructureMarker, indexJSPath)
		}
		patched = next
	}
	if !bytes.Contains(patched, []byte("lightsync: patched in by")) {
		next := bytes.Replace(patched, []byte(onReadyMarker), []byte(displayMediaPatch), 1)
		if bytes.Equal(next, patched) {
			return fmt.Errorf("marker %q not found in %s — astilectron's bundled index.js has likely changed shape upstream", onReadyMarker, indexJSPath)
		}
		patched = next
	}
	if bytes.Equal(patched, raw) {
		return nil // both patches already present
	}
	return os.WriteFile(indexJSPath, patched, 0o644)
}

// patchingProvisioner reimplements astilectron's default provisioning
// (download astilectron + Electron, unzip, track versions so repeat runs
// don't re-download) using only its exported building blocks — Download,
// Unzip and the Paths accessors — because the default provisioner's own
// type is unexported and can't be wrapped or delegated to directly. The one
// thing it adds on top: patchIndexJS, applied once right after astilectron
// itself is freshly unzipped.
type patchingProvisioner struct {
	l  astikit.SeverityLogger
	dl *astikit.HTTPDownloader
}

func newPatchingProvisioner(l astikit.StdLogger) *patchingProvisioner {
	return &patchingProvisioner{
		l:  astikit.AdaptStdLogger(l),
		dl: astikit.NewHTTPDownloader(astikit.HTTPDownloaderOptions{Sender: astikit.HTTPSenderOptions{Logger: l}}),
	}
}

// provisionStatus mirrors astilectron.ProvisionStatus's on-disk JSON shape
// exactly (same field names/tags), so a status file written by a prior
// plain-default-provisioner run is read the same way, and vice versa.
type provisionStatus struct {
	Astilectron *astilectron.ProvisionStatusPackage            `json:"astilectron,omitempty"`
	Electron    map[string]*astilectron.ProvisionStatusPackage `json:"electron,omitempty"`
}

func (p *patchingProvisioner) Provision(ctx context.Context, appName, osName, arch, versionAstilectron, versionElectron string, paths astilectron.Paths) error {
	status := p.readStatus(paths)

	if status.Astilectron == nil || status.Astilectron.Version != versionAstilectron {
		log.Printf("[electron] Provisioning Astilectron...")
		if err := p.fetch(ctx, paths.AstilectronDownloadSrc(), paths.AstilectronDownloadDst(), paths.AstilectronUnzipSrc(), paths.AstilectronDirectory()); err != nil {
			return fmt.Errorf("provisioning astilectron: %w", err)
		}
		status.Astilectron = &astilectron.ProvisionStatusPackage{Version: versionAstilectron}
	}
	// Deliberately outside the block above: patchIndexJS is idempotent per
	// patch (checks its own marker comments), so it's safe — and necessary —
	// to run on every launch, not just when astilectron itself needed a
	// fresh download. Real bug this fixes: an install that already had
	// astilectron provisioned under an *older* version of this program (with
	// only, say, the onReady patch) would otherwise never receive a
	// *newly added* patch (like the PipeWire one) on a later version bump,
	// since the version check above would see astilectron's own version is
	// unchanged and skip this block entirely, silently leaving the install
	// permanently on the old, incomplete patch set.
	if err := patchIndexJS(paths.AstilectronDirectory() + "/index.js"); err != nil {
		return fmt.Errorf("patching astilectron index.js: %w", err)
	}

	key := osName + "-" + arch
	if status.Electron == nil {
		status.Electron = map[string]*astilectron.ProvisionStatusPackage{}
	}
	if status.Electron[key] == nil || status.Electron[key].Version != versionElectron {
		log.Printf("[electron] Provisioning Electron...")
		if err := p.fetch(ctx, paths.ElectronDownloadSrc(), paths.ElectronDownloadDst(), paths.ElectronUnzipSrc(), paths.ElectronDirectory()); err != nil {
			return fmt.Errorf("provisioning electron: %w", err)
		}
		status.Electron[key] = &astilectron.ProvisionStatusPackage{Version: versionElectron}
	}

	return p.writeStatus(paths, status)
}

func (p *patchingProvisioner) fetch(ctx context.Context, src, dst, unzipSrc, dir string) error {
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	if err := astilectron.Download(ctx, p.l, p.dl, src, dst); err != nil {
		return fmt.Errorf("downloading %s: %w", src, err)
	}
	defer os.Remove(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return astilectron.Unzip(ctx, p.l, unzipSrc, dir)
}

func (p *patchingProvisioner) readStatus(paths astilectron.Paths) provisionStatus {
	var s provisionStatus
	f, err := os.Open(paths.ProvisionStatus())
	if err != nil {
		return s // missing/unreadable status file just means "provision everything"
	}
	defer f.Close()
	_ = json.NewDecoder(f).Decode(&s)
	return s
}

func (p *patchingProvisioner) writeStatus(paths astilectron.Paths, s provisionStatus) error {
	f, err := os.Create(paths.ProvisionStatus())
	if err != nil {
		return fmt.Errorf("creating %s: %w", paths.ProvisionStatus(), err)
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(s)
}
