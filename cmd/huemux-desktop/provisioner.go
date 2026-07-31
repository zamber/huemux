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
        console.log('huemux debug: platform=' + process.platform + ' session_type=' + (process.env.XDG_SESSION_TYPE || '') + ' commandLine has pipewire switch=' + app.commandLine.hasSwitch('enable-features'))
        electron.session.defaultSession.setDisplayMediaRequestHandler(function (request, callback) {
            electron.desktopCapturer.getSources({ types: ['screen'] }).then(function (sources) {
                // Logged every call, not just on failure: this is the exact
                // point that silently returns a placeholder frame instead of
                // real capture under Wayland without the PipeWire switch (see
                // pipeWireCapturePatch below) — sources.length/name/size here
                // is what tells that apart from a real capture failure.
                console.log('huemux debug: getSources returned ' + sources.length + ' source(s): ' + sources.map(function (s) { return s.name + '@' + s.id + ' thumbnail=' + (s.thumbnail ? s.thumbnail.getSize().width + 'x' + s.thumbnail.getSize().height : 'none') }).join(', '))
                callback(sources.length ? { video: sources[0], audio: 'loopback' } : {})
            }).catch(function (e) {
                console.log('huemux debug: getSources failed: ' + e)
                callback({})
            })
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
// HUEMUX_OZONE_PLATFORM / HUEMUX_DISABLE_VULKAN are undocumented escape
// hatches for testing a specific real-world symptom: PipeWire capture
// enumerates a real source (desktopCapturer.getSources() returns it,
// confirmed against real hardware) but the frames actually delivered are a
// fixed placeholder color, and every affected log shows Chromium's own
// warning that '--ozone-platform=wayland' is incompatible with Vulkan on
// that GPU/compositor combo, suggesting the same GPU buffer-import path
// used for hardware-accelerated capture frames is what's silently failing.
// Set to compare against the default without a rebuild each time:
//   HUEMUX_OZONE_PLATFORM=x11 huemux-desktop -debug   (routes via XWayland,
//     a completely different, PipeWire-independent capture code path)
//   HUEMUX_DISABLE_VULKAN=1 huemux-desktop -debug     (stays on native
//     Wayland/PipeWire, but forces the GL ANGLE backend instead of the
//     Vulkan-via-Wayland-ozone path the warning flags as broken)
//
// First attempt used '--ozone-platform-hint' for the former and
// '--disable-features=Vulkan' for the latter — confirmed by a real user's
// -debug log to do nothing (Chromium's own "not compatible with Vulkan"
// warning, which names the platform it actually picked, was identical with
// either set or not): -hint only informs ozone's own auto-detection, which
// still won given XDG_SESSION_TYPE=wayland, and Vulkan-vs-GL is decided by
// ANGLE's own backend selection, not the Vulkan feature flag. Switched to
// the unconditional '--ozone-platform' switch and '--use-angle=gl'.
console.log('huemux debug: ozone_platform=' + (process.env.HUEMUX_OZONE_PLATFORM || '(unset, default auto-detect)') + ' disable_vulkan=' + !!process.env.HUEMUX_DISABLE_VULKAN)
if (process.env.HUEMUX_OZONE_PLATFORM) {
    app.commandLine.appendSwitch('ozone-platform', process.env.HUEMUX_OZONE_PLATFORM)
}
if (process.env.HUEMUX_DISABLE_VULKAN) {
    app.commandLine.appendSwitch('use-angle', 'gl')
}
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
	var applied []string
	if !bytes.Contains(patched, []byte(pipeWireCapturePatchMarker)) {
		next := bytes.Replace(patched, []byte(electronDestructureMarker), []byte(pipeWireCapturePatch), 1)
		if bytes.Equal(next, patched) {
			return fmt.Errorf("marker %q not found in %s — astilectron's bundled index.js has likely changed shape upstream", electronDestructureMarker, indexJSPath)
		}
		patched = next
		applied = append(applied, "pipewire-capture")
	}
	if !bytes.Contains(patched, []byte("lightsync: patched in by")) {
		next := bytes.Replace(patched, []byte(onReadyMarker), []byte(displayMediaPatch), 1)
		if bytes.Equal(next, patched) {
			return fmt.Errorf("marker %q not found in %s — astilectron's bundled index.js has likely changed shape upstream", onReadyMarker, indexJSPath)
		}
		patched = next
		applied = append(applied, "display-media")
	}
	if bytes.Equal(patched, raw) {
		log.Printf("[electron] index.js already fully patched (pipewire-capture, display-media) — nothing to do")
		return nil
	}
	log.Printf("[electron] patching index.js: applying %v (previously missing)", applied)
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
// plain-default-provisioner run is read the same way, and vice versa. The
// one addition, HuemuxPatch, is namespaced under a different key so it's
// simply ignored by (and never confuses) astilectron's own code.
type provisionStatus struct {
	Astilectron *astilectron.ProvisionStatusPackage            `json:"astilectron,omitempty"`
	Electron    map[string]*astilectron.ProvisionStatusPackage `json:"electron,omitempty"`
	HuemuxPatch string                                         `json:"huemux_patch,omitempty"`
}

// patchSetVersion identifies the *content* of the patches below, not just
// their presence. Bump it any time displayMediaPatch or pipeWireCapturePatch
// changes — see the real bug this exists to prevent, in Provision() below.
const patchSetVersion = "4"

func (p *patchingProvisioner) Provision(ctx context.Context, appName, osName, arch, versionAstilectron, versionElectron string, paths astilectron.Paths) error {
	status := p.readStatus(paths)

	// Real bug found via a live user debug log: patchIndexJS only checks
	// whether ITS OWN marker COMMENT is present, not whether the patch body
	// behind it matches what this binary would insert today. That made it
	// correctly idempotent across repeat launches of the *same* build, but
	// silently inert across an upgrade that changes patch *content* (e.g.
	// adding the diagnostic console.log calls below) on an
	// already-patched install — status.Astilectron.Version was unchanged, so
	// the block below never re-ran, and patchIndexJS's own marker check saw
	// its old marker already present and skipped re-inserting, permanently
	// freezing that install's index.js on whatever patch body it first got.
	// Fix: track patchSetVersion in status.json too, and force astilectron's
	// own (small — this is not the ~300MB Electron download, just its JS
	// runtime) directory to be deleted and re-fetched whenever it's stale,
	// so patchIndexJS always runs against a guaranteed-pristine, unpatched
	// file rather than one it may have already (incompletely) touched.
	needsAstilectron := status.Astilectron == nil || status.Astilectron.Version != versionAstilectron
	needsPatchRefresh := status.HuemuxPatch != patchSetVersion
	if needsAstilectron || needsPatchRefresh {
		log.Printf("[electron] Provisioning Astilectron... (astilectron stale=%v, patch content stale=%v)", needsAstilectron, needsPatchRefresh)
		if err := p.fetch(ctx, paths.AstilectronDownloadSrc(), paths.AstilectronDownloadDst(), paths.AstilectronUnzipSrc(), paths.AstilectronDirectory()); err != nil {
			return fmt.Errorf("provisioning astilectron: %w", err)
		}
		status.Astilectron = &astilectron.ProvisionStatusPackage{Version: versionAstilectron}
	}
	// Deliberately outside the block above: still safe (and still useful as
	// a belt-and-braces check) to run unconditionally, since patchIndexJS is
	// idempotent per patch within a single binary's own content — the fix
	// above is what guarantees the file it operates on is never stale
	// relative to *this* binary's patch content in the first place.
	if err := patchIndexJS(paths.AstilectronDirectory() + "/index.js"); err != nil {
		return fmt.Errorf("patching astilectron index.js: %w", err)
	}
	status.HuemuxPatch = patchSetVersion

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
