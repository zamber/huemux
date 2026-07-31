# Known Issues

## Solid green (or black) preview on the first sync attempt

**Symptom:** starting screen sync for the first time in a session shows a
flat green (sometimes black) preview instead of real screen content, at 0
fps. A second attempt sometimes works.

**Status:** confirmed, via live debugging with a real affected user, to be a
**system-level PipeWire/portal (or GPU/DRI) issue on their machine, not a
HueMux or Electron bug.** The desktop-build-specific fixes below are real
and worth keeping, but they turned out not to be this particular user's
root cause — see "Ruled out" below for how that was established.

What's already fixed:

- The calibration/status-preview snapshot used to reuse the exact bytes
  `Process()` produces for the wire, which are x/y chromaticity + brightness
  (not RGB) whenever `ColorSpaceXY` is selected — this made the *preview*
  wrong even when the real capture and the real bridge output were both
  fine. Fixed by `internal/pipeline/color.go`'s `DisplayRGB()`, which always
  gamma-encodes to real sRGB for the preview regardless of wire color space.
- Under Wayland specifically, Chromium's `desktopCapturer` does not error
  when it can't do real PipeWire-backed capture — it silently hands back a
  fixed placeholder frame (observed: solid green, unconditionally). The
  Electron desktop build (`cmd/huemux-desktop`) needed
  `app.commandLine.appendSwitch('enable-features', 'WebRTCPipeWireCapturer')`
  set before Electron's `ready` event, which is now patched in via
  `cmd/huemux-desktop/provisioner.go`'s `pipeWireCapturePatch`. A related bug
  in the patcher itself — the patch would never reach an *already-provisioned*
  install on a version bump, only fresh ones — was also found and fixed
  (`patchIndexJS` now runs unconditionally on every launch, not just when
  astilectron itself needs a fresh download).

### Ruled out: Electron/HueMux-specific causes

A real-hardware debugging session (Fedora-family, GNOME, Wayland, ultrawide
monitor) worked through every Electron-side lever before finding the actual
layer:

1. Confirmed via `-debug` that `desktopCapturer.getSources()` genuinely
   returns a real source (`"Cały ekran"`/"Entire screen", a real thumbnail)
   — not an empty list, so it isn't the "no source available" case.
2. Confirmed the `WebRTCPipeWireCapturer` command-line switch was actually
   present (`commandLine has pipewire switch=true`) and, separately, that
   the astilectron patch delivering it was actually up to date (see the
   patch-staleness bug below — a real, distinct bug found along the way).
3. Tried forcing `--ozone-platform=x11` (bypasses PipeWire/the Wayland
   portal entirely, routes through XWayland) — this measurably changed
   Chromium's behavior (the Vulkan-incompatibility warning below disappeared)
   but surfaced a *different*, machine-specific failure: the GPU process
   segfaulted repeatedly on `/usr/lib64/gbm/dri_gbm.so: Permission denied`.
   Abandoned as a dead end, but the permission error itself is a real,
   independently-worth-chasing lead — see "What to check on your system"
   below.
4. Tried forcing the ANGLE GL backend (`--use-angle=gl`) instead of Vulkan,
   staying on native Wayland — no effect; Chromium's own
   `wayland_surface_factory.cc` warning ("`--ozone-platform=wayland` is not
   compatible with Vulkan") kept appearing regardless, meaning Wayland
   ozone's GPU buffer/surface allocation picks its backend independently of
   ANGLE's rendering backend switch.
5. **The decisive test:** ran the *plain browser build* (`huemux`, no
   Electron at all) and opened it in a real, independently-installed
   Firefox. Same solid-green result. Since Firefox has its own mature,
   long-shipped native PipeWire/portal screen-capture support with zero
   HueMux or Electron code involved, this rules out HueMux's code, the
   Electron wrapper, and every patch/switch above as the cause — whatever
   is producing the green frame is happening beneath all of that, on this
   machine specifically.

One more data point worth noting: the average color logged by
`logFrameStats` was the *exact same* `rgb(0,136,0)` across every single
attempt, on every backend combination tried. Real captured content
averaging to precisely zero red and zero blue on every attempt is far more
consistent with a fixed synthetic placeholder frame than with genuine
screen content (which would essentially never average to exactly `0,0`
on two full channels).

### What to check on your system (not yet done)

Since this points at PipeWire/the desktop portal/GPU access rather than
HueMux, the next diagnostic steps are system-level, not app-level:

- The GBM/DRI permission error from step 3 above
  (`/usr/lib64/gbm/dri_gbm.so: Permission denied`) is a concrete, standalone
  lead: check the file's actual permissions/ownership, and whether your
  user is in the `video`/`render` groups PipeWire's DMA-BUF capture path
  typically needs. A broken GBM/DRI path could plausibly break PipeWire's
  own zero-copy capture buffer negotiation too, independent of Chromium.
- `systemctl --user status pipewire pipewire-pulse wireplumber
  xdg-desktop-portal xdg-desktop-portal-gnome` (substitute the portal
  backend for your actual DE if not GNOME) — look for crash-looping or
  failed units.
- Test screen capture completely outside the browser/Chromium family —
  e.g. GNOME's own screen recording (Settings → Screenshots, or
  <kbd>Ctrl+Shift+Alt+R</kbd>), OBS Studio's Wayland portal source, or
  `wf-recorder`. If those *also* produce a blank/wrong-color result, the
  bug is confirmed compositor/portal-side, nothing app-specific can fix it.
  If those work fine, the bug is scoped to Chromium's specific PipeWire
  capturer implementation on this system — worth searching/filing upstream
  against Chromium rather than HueMux.

### Reproducing with `-debug`

Both binaries accept a `-debug` flag:

```
huemux -debug
huemux-desktop -debug
```

This writes a timestamped log file (in addition to the normal stdout
output) with:

- On the desktop build: the detected session type, `WAYLAND_DISPLAY`,
  `DISPLAY`, and Electron version at startup; plus Electron's own
  main-process console output, including — on every capture attempt — how
  many screen sources `desktopCapturer.getSources()` returned and their
  names/thumbnail sizes (an empty or single-fixed-size result across
  repeated attempts is a strong signal of the placeholder-frame case above).
- On both builds: a throttled (max once/second) average-RGB summary of
  incoming grid frames once a sync session is active — an average that
  never changes despite real on-screen motion means the *server* is
  receiving placeholder/static data, which narrows the bug to capture,
  not to color processing or the bridge output path.

Log file location (created automatically, path is also printed to stdout
on startup):

| OS | Path |
|---|---|
| Linux/BSD | `$XDG_STATE_HOME/huemux/debug-<timestamp>.log`, or `~/.local/state/huemux/` if `$XDG_STATE_HOME` is unset |
| macOS | `~/Library/Logs/HueMux/debug-<timestamp>.log` |
| Windows | `%LOCALAPPDATA%\HueMux\logs\debug-<timestamp>.log` |

Please attach the full log from a reproduction (not just the tail) along
with: OS + desktop environment/compositor (e.g. "Fedora 42, GNOME 47,
Wayland"), and whether you're on the browser or desktop build.
