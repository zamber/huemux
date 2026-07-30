# huemux-desktop (experimental, `feature/astilectron-wrapper` branch)

Wraps the exact same core (`internal/config`, `internal/engine`,
`internal/server` — genuinely unmodified, `git diff main -- internal/` is
empty) in an Electron shell via [go-astilectron](https://github.com/asticode/go-astilectron),
to answer a concrete question: is it viable to drop browser-variability
concerns (`getDisplayMedia()` quirks across browsers, the
`MediaStreamTrackProcessor` main-thread/worker-only split, capture source
picker inconsistencies) by targeting one known, pinned Chromium instead of
whatever browser the user happens to have?

**Answer: yes**, verified end-to-end against a real Hue bridge — real
Electron window, real screen capture with **zero** picker dialog, real DTLS
stream to real lights, at a steady 60fps inbound (better than a typical
browser's `getDisplayMedia()` delivery).

```bash
make dev-desktop        # local binary
./huemux-desktop             # opens an Electron window
./huemux-desktop --headless  # identical to plain `huemux` — no Electron, no GUI dependency paid at runtime
```

First non-headless launch downloads Electron (~150MB) into
`$XDG_CACHE_HOME/huemux/astilectron` (or the OS equivalent) — needs
internet access once; cached after that.

## The one non-obvious part: `desktopCapturer` is main-process-only

Older Electron tutorials show `require('electron').desktopCapturer` called
directly from a renderer. That stopped working around Electron 20 — verified
by hand in this window's own devtools console:

```
> require('electron').desktopCapturer
undefined
```

`require('electron')` itself works fine (astilectron hardcodes
`nodeIntegration: true` for every window it creates — not something this
app configures, it's baked into the library), so this isn't a
node-integration problem. `desktopCapturer` specifically was moved
main-process-only.

The fix that actually worked, and the reason `web/app.js` needed **zero**
Electron-specific branching in the end: Electron's main process can
intercept the ordinary web-standard `getDisplayMedia()` call via
[`session.setDisplayMediaRequestHandler`](https://www.electronjs.org/docs/latest/api/session#sessetdisplaymediarequesthandlerhandler-opts),
handing back a source with no picker UI at all. The page has no idea it's
running under Electron — `startCapture()` in app.js is the same function,
same `getDisplayMedia()` call, in a browser tab or in this wrapper.

Getting that handler registered was the actual work: astilectron's bundled
Electron main-process script (`index.js`, downloaded fresh into the cache
directory on each version bump) has no extension point for arbitrary
main-process code, and its own `defaultProvisioner` type is unexported, so
it can't be wrapped. `provisioner.go` reimplements provisioning from
astilectron's exported building blocks (`Download`, `Unzip`, the `Paths`
accessors) and splices `displayMediaPatch` into `index.js` right after
downloading it, once per version — see the comments there for the exact
mechanism and why each piece exists.

## What this pass did not attempt

- **Source picker.** `desktopCapturer.getSources()` (called from the
  patched main process) always returns the primary screen. A real
  multi-monitor/window picker is a natural follow-up, not attempted here —
  this pass was about proving the wrapper is viable at all.
- **Packaging.** Electron ships unpackaged (downloaded at runtime, not
  bundled into the Go binary). `go-astilectron-bundler` or a similar
  asset-embedding pass is the natural next step if this graduates beyond
  "does it work."
- **macOS/Windows.** Only verified on Linux (under Xvfb, headless-VM
  testing — see below). The mechanism (patch a plain-text `index.js` after
  download, before Electron starts reading it) is platform-agnostic, but
  hasn't been run on the other two.

## How this was actually tested

This host has no real display. Verified for real anyway, not just
compiled-and-assumed-to-work: `Xvfb` (already present on this box from an
unrelated Playwright/Chrome setup) provided a virtual X11 display, `xdotool`
drove real clicks against the real rendered window, `import` (ImageMagick)
screenshotted it, and Electron's own devtools (`HUEMUX_DEVTOOLS=1` env
var, see `runDesktop`) confirmed the `desktopCapturer`-undefined finding
directly rather than guessing from an error message.

The capture test briefly drove real hardware (the actual paired bridge) —
disclosed and confirmed with the user at the time, not something to repeat
casually in future testing of this branch.
