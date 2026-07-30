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
    // lightsync: patched in by cmd/lightsync-desktop's custom provisioner
    // (see provisioner.go) so getDisplayMedia() works with no picker UI.
    // desktopCapturer is main-process-only; this is the only place it can
    // be wired up to what a page's getDisplayMedia() call receives.
    try {
        electron.session.defaultSession.setDisplayMediaRequestHandler(function (request, callback) {
            electron.desktopCapturer.getSources({ types: ['screen'] }).then(function (sources) {
                callback(sources.length ? { video: sources[0], audio: 'loopback' } : {})
            }).catch(function () { callback({}) })
        }, { useSystemPicker: false })
    } catch (e) {
        console.error('lightsync: setDisplayMediaRequestHandler failed:', e)
    }
`

const onReadyMarker = "function onReady () {"

// patchIndexJS splices displayMediaPatch into the astilectron index.js at
// dir. Idempotent: if the marker comment is already present (e.g. a status
// file got lost but the directory didn't), it's a no-op rather than
// double-patching.
func patchIndexJS(indexJSPath string) error {
	raw, err := os.ReadFile(indexJSPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", indexJSPath, err)
	}
	if bytes.Contains(raw, []byte("lightsync: patched in by")) {
		return nil
	}
	patched := bytes.Replace(raw, []byte(onReadyMarker), []byte(displayMediaPatch), 1)
	if bytes.Equal(patched, raw) {
		return fmt.Errorf("marker %q not found in %s — astilectron's bundled index.js has likely changed shape upstream", onReadyMarker, indexJSPath)
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
		if err := patchIndexJS(paths.AstilectronDirectory() + "/index.js"); err != nil {
			return fmt.Errorf("patching astilectron index.js: %w", err)
		}
		status.Astilectron = &astilectron.ProvisionStatusPackage{Version: versionAstilectron}
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
