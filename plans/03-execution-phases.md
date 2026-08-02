# Plan 03 — Execution phases

The sequenced work breakdown across [Plan 01](01-config-profiles-and-access.md)
and [Plan 02](02-android-standalone.md). Design rationale lives in those two;
this is the running order, the acceptance criteria, and the status.

Update the status column as phases land. Each phase is intended to be a
reviewable commit or small series, and to leave `main` working.

| # | Phase | Status |
|---|---|---|
| 1 | `internal/appconfig` — schema, file, flags | **done** |
| 2 | Profiles, server side | **done** |
| 3 | Extract pairing UI into `web/shared/` | **done** |
| 4 | Profile-aware UI + settings screen | **done** |
| 5 | Listen address, Origin, auth | **done** |
| 6 | TLS modes | **done** |
| 7 | Android M1 — lights, standalone | **done, device-tested** |
| 8 | Android M2 — config + mobile polish | **done** |
| 9 | Android M3 — MediaProjection sync | **done, device-tested** |
| 10 | Android M4 — CI build, signing, distribution | **done (signing awaits a keystore secret)** |
| 11 | Device feedback — shell integrity, capture quality, recording | **device-tested; recording reworked in 12** |
| 12 | Recording without a second mirror, host diagnostics | **device-tested; file output reworked in 13** |
| 13 | File output — native save, share sheet, reported locations | **built, untested on a device** |
| 14 | Long-press suppression | **built, untested on a device** |
| 15 | Capture lifecycle; full-res recording removed | **built, untested on a device** |
| 16 | Split frame path — pipeline downscale, direct encode | **device-confirmed: snappy at 100% recording resolution** |
| 17 | Restricted-profile nav, sticky heading, encoder tail | **built, untested on a device** |
| 18 | `LICENSE` (GPL-3.0-or-later) + consistent Android signing | **done — first signed release pending** |
| 19 | Obtainium — README line, works with current CI | **documented; needs one signed release to verify** |
| 20 | Attribution + About screen | **not started** |
| 21 | IzzyOnDroid | **not started** |
| 22 | Scoop, Homebrew tap, AUR | **not started** |
| 23 | Flathub | **not started** |
| 24 | F-Droid main repo | **not started** |
| 25 | Release workflow split + `release` environment | **not started** |
| 26 | Privacy policy + store declarations | **not started** |
| 27 | Windows signing, then winget | **not started** |
| 28 | macOS notarization, then Homebrew cask | **not started** |
| 29 | Google Play | **not started** |

---

## Phase 1 — `internal/appconfig`

The schema everything else reads. Deliberately a no-op in behavior: defaults
reproduce today exactly, so this can land and be verified in isolation.

**Build**
- `internal/appconfig/config.go` — `Config`/`Listen`/`Auth`/`TLS` types, enum
  types with `Valid()`, `Default()`.
- `internal/appconfig/store.go` — load/save `app.json`, atomic write, tolerant
  of a missing file (returns defaults).
- `internal/appconfig/merge.go` — precedence: defaults → file → flags →
  runtime.
- `internal/appconfig/token.go` + `wordlist.go` — word-based token generation
  using `crypto/rand`.
- `internal/appconfig/flags.go` — flag registration bound to the same struct,
  so the CLI cannot drift from the file schema.
- Tests for precedence, round-trip, validation, token shape/entropy.

**Wire up (no behavior change)**
- `cmd/huemux/main.go` and `cmd/huemux-desktop/main.go` load the config and
  pass it to `server.New`, which ignores everything but the defaults for now.

**Done when**
- `go test ./internal/appconfig/...` passes.
- With no `app.json` and no flags, `huemux` behaves byte-identically to
  before: loopback, port 7654, both features, no auth.
- A written `app.json` round-trips; an unknown enum value is a clear startup
  error, not a silent fallback.

## Phase 2 — Profiles, server side

**Built**
- `server.BuildPaired` is the single place a profile decides what gets
  constructed. Both the startup path and `runPair` call it, so the two cannot
  disagree — this is the fix for `runPair` silently re-enabling a disabled
  half on pairing.
- `/api/areas` is registered only when the Sync tab exists. The light-control
  routes stay registered under every profile, since the sync page's scene
  strip depends on them.
- The light-event broadcast is gated on `ShowsLightsTab()` rather than made
  lazy: under a sync-only profile there is no light-control UI for those
  events to reach, so gating achieves the same saving with no new machinery.
- `Server.Paired()` plus `ui.Printer.RenderNoEngine`, for a bug found while
  wiring this: a lights profile has a nil engine forever, and the render loop
  inferred "not paired" from that — telling an already-paired, correctly
  working server to go and pair itself on every tick.

**Not done, deliberately** — the plan called for `handleWS` to stop counting
lights-only clients as capture clients. Reading `engine.go:306` shows the
count is documented as deliberately tracking connected tabs "independent of
which one (if any) is the frame source", so the original item was a misreading
of the `CaptureClients` field name rather than a real defect. The field name
is the misleading part; the behavior is intended. Left alone.

**Done when**
- `--profile=lights` opens no DTLS socket (verify with `ss`), `/api/areas`
  returns `[]`, and light control works.
- `--profile=sync` keeps the scene strip working (it needs lightctl —
  see Plan 01).
- Pairing under `--profile=lights` still writes valid credentials.

## Phase 3 — Extract pairing UI

Prerequisite for a usable lights-only build, and the most delicate UI change
in the whole plan — hence its own phase.

Pairing currently exists **only** in `sync.html` + `app.js`
(`:104/:168/:178/:183`); `lights.html:31-35` just links to it. Under
`--profile=lights` there is no Sync page, so a fresh install cannot pair at
all.

**Done.** `<huemux-pairing>` (`web/shared/pairing.js` + `pairing.css`) is now
mounted by both pages. It is transport-agnostic — it emits a
`huemux:pair-send` event carrying the message to put on the wire, and each
page forwards that to its own WebSocket, since sync.html and lights.html each
open one and a callback property would have depended on mount ordering.

Verified in a browser against a real bridge with an unpaired
`--profile=lights` server: the lights page discovers the bridge, renders the
card, and the full event contract works (rescan, manual IP with whitespace
trimming, correctly *not* sending on a blank IP, and the per-bridge Pair
button). Sync page unchanged, zero console errors.

Two bugs caught by actually exercising it rather than eyeballing:

- Six pairing strings were hardcoded English in `app.js` and never
  translated — including one where a `pairing.searching` key already existed
  and simply wasn't used. All six now have `en`/`pl` entries.
- The component guarded on `window.HueMuxI18n`, which is **always undefined**:
  `i18n.js` declares `const HueMuxI18n` at the top level of a classic script,
  and a top-level `const` is not a property of `window`. Every dynamic string
  silently fell through to its English fallback. Invisible unless you actually
  switch language, which is exactly why it got switched.

## Phase 4 — Profile-aware UI + settings screen

- `/api/config` GET/PATCH (PATCH loopback-only), with the token redacted for
  non-loopback readers.
- `web/shared/header.js:78-86` — nav rendered from enabled features instead of
  a hardcoded two-link string.
- `web/shared/shell.js:9-13,35` — frame map and the `'sync'` default, which
  currently lands a lights-only build on a frame that does not exist.
- `web/app.html:30-33` — stop loading both iframes unconditionally.
- `web/settings.html` + `settings.js`.

**Done when** each profile shows only its own tabs, and settings changes made
from loopback persist across a restart.

## Phase 5 — Listen address, Origin, auth

The security-sensitive phase, deliberately last of the server work so it can
be reviewed alone.

- `ListenAndServe` takes host/port instead of hardcoding `127.0.0.1`
  (`http.go:195`, `:203`).
- `checkOrigin` (`ws.go:52`) gains the configured host **and nothing else** —
  not a wildcard, not "skip when a token is present". Fix the stale
  `localAddr` comment and the dead `[::1]` branch while there.
- Token middleware: `Authorization: Bearer` **and** `?token=` (browser
  `WebSocket` cannot set headers), `crypto/subtle` comparison, rate limiting
  on failures, loopback exempt by default.

**Done when** a non-loopback bind rejects unauthenticated `/api/*` and `/ws`,
accepts a valid token, still rejects a foreign `Origin`, and loopback is
unchanged.

## Phase 6 — TLS modes

`off` / `selfsigned` (generate and persist) / `files` (caller-supplied paths).
HueMux issues nothing itself — see Plan 01 for why.

**Done when** `selfsigned` serves HTTPS, and `files` works against a real
`tailscale cert` pair.

## Phase 7 — Android M1

Started out of order, because the Go half needs no Android SDK and plan 02
always said M1 could run in parallel.

**Done:**
- `internal/config.SetDir` — the three stores all resolve through `Dir()`, so
  one seam covers bridge credentials, area settings, and favorites.
  `os.UserConfigDir()` is meaningless on Android.
- `mobile/` — the gomobile facade (`Start`/`Stop`/`URL`/`IsPaired`/
  `ConfigJSON`/`SetConfigJSON`/`StartSync`/`StopSync`/`PushFrame`), flat enough
  for gomobile's restricted type rules, with tests including a race-detector
  concurrency case since gomobile calls in from arbitrary Java threads.
- `.github/workflows/android.yml` — a fast `CGO_ENABLED=0 GOOS=android` compile
  gate on every push, plus the actual `gomobile bind` AAR job on a runner.

**Confirmed rather than assumed:** the entire core, DTLS included, compiles to
a real `android/arm64` ELF binary locally with no NDK. That was plan 02's
central bet and it holds.

**Still to do:** the Kotlin app — `MainActivity` hosting a WebView on the URL
`Start` returns, with `setDomStorageEnabled(true)` since theme and language
live in `localStorage`.

**Done when**, on a real phone with no server running anywhere: pair from
fresh, control lights, kill and relaunch without re-pairing. **Verify bridge
discovery early** — it is the most likely Android-specific failure, and manual
IP entry is the existing fallback.

## Phase 8 — Android M2

Settings screen on device, plus the mobile CSS fixes catalogued in Plan 02
(filter list bottom-sheet, colour-picker height math, `blur(50px)` cost, sub-44px
touch targets, safe-area insets).

## Phase 9 — Android M3

`window.HueMuxNative` bridge, foreground service, MediaProjection,
`PushFrame`. Note the Android 14+ ordering constraint: `startForeground()`
must precede `getMediaProjection()`.

**Done when** frames reach the bridge from a real phone, and battery draw over
30 minutes has been measured rather than guessed.

## Phase 10 — Android M4

CI build (runners ship the Android SDK), keystore signing from a secret, APK
attached to releases. F-Droid is a natural fit; Play Store adds a
`MediaProjection` policy review, which is another reason sync is not in M1.

## Phase 11 — Device feedback

Not planned up front: this is the work that came out of actually using alpha.11
and alpha.12 on a phone, which is where every item below was found.

**Shell integrity.** The app is an iframe shell precisely so switching tabs does
not tear down a running capture, and two routes led out of it: Settings was a
plain link rather than a frame, and the shell's own `replaceState` meant any
reload resolved to a bare page outside the shell, one-way. Both closed; see
`web/shared/standalone-redirect.js`. This was the root cause of the "it re-inits
whenever I navigate" report, and of a stale sync page offering Start for a
stream that was already running.

**Two theme axes, not five states.** Palette and visual effects were one
five-state cycle behind one button. Split, with a migration for stored
`simple-light`/`simple-dark` values.

**Dropdowns.** The mobile bottom sheet is gone in favour of an anchored,
full-bleed panel; `shared/dropdown.{css,js}`, and the rule is written down in
AGENTS.md's UX section. It had been clipped by the fixed bottom nav.

**Lights cache.** Last-known lights/rooms/scenes/favourites in `localStorage`,
painted before the WebSocket connects, and no empty state until `/api/lights`
has actually answered.

**Capture resolution and recording** — the two things alpha.12 deliberately
left. `captureScale` is now a control rather than a constant, and the long-edge
cap moved from 480 to 1920, since a cap below the slider's range made the
setting look broken by silently ignoring most of it. Recording is
`ScreenRecorder.kt`, at either the capture size or the display's own.

**Done when** a device confirms: recording produces a playable file in
Movies/HueMux at both qualities, the resolution slider changes the reported
capture size, and a lap through all three tabs leaves a running sync running.

Note on the recording design: mirroring to a MediaRecorder *and* to the colour
pipeline from one VirtualDisplay is not possible — `createVirtualDisplay` binds
exactly one Surface for the display's lifetime, and the capture display's is the
ImageReader that feeds Go. Recording therefore always costs a second virtual
display; the quality setting chooses that display's resolution, not whether it
exists. Devices that refuse a second display lose recording and keep syncing.

## Phase 12 — Recording without a second mirror, and host diagnostics

From testing alpha.13. Two of these are corrections to Phase 11.

**Recording no longer needs a second VirtualDisplay.** Phase 11 recorded by
mirroring the screen again, on the reasoning that one display cannot feed two
consumers. True of a Surface, and beside the point: the frames are already in
memory on their way to the colour engine. `FrameRecorder` encodes that buffer
(RGB to YUV, MediaCodec, MediaMuxer). The device that refused a second display
has nothing left to refuse. `ScreenRecorder` stays for full-resolution
recording, the one case that does need its own mirror.

**Capture rebuild is single-threaded.** Changing resolution mid-capture crashed:
the reader was closed from one thread while a frame callback ran on another.
A HandlerThread owns frame delivery, rebuild and teardown.

**Diagnostics reach across the language boundary.** `server.SetHostInfo` and
`Mobile.LogHost` let the Kotlin half write into the report the Go half
generates. Without this a failed recording produced a report with no mention of
recording, which is how Phase 11 shipped a bug that could not be diagnosed from
its own bug report.

**Done when** a device confirms: recording at "match capture" produces a
playable file, the resolution slider does not crash and is locked while
recording, and a failed recording leaves a specific reason in the host section.

## Phase 13 — File output

From testing alpha.14. Both items are about a file existing but being
unreachable.

**Files do not travel through navigations.** The diagnostics download broke
when its navigation moved into an iframe in Phase 11, because a WebView's
DownloadListener does not fire there. Rather than choosing between a navigation
that can blank the page and an iframe that cannot download, the page now
fetches its own text over loopback and `MainActivity.saveTextFile` writes it to
Downloads via MediaStore. Non-Android browsers use an ordinary `<a download>`.

**A saved file reports where it went.** Both recorders expose the resolved
location and a share URI; `shareLastFile` opens the system share sheet for the
most recent recording or saved report. Phase 12 reported only a filename, which
left "did it save?" unanswerable from the UI.

**Not done:** a save dialog (ACTION_CREATE_DOCUMENT) before recording. It makes
recording start asynchronous — the same activity-result handshake `startCapture`
needs — and the share sheet covers the actual need. Revisit if choosing the
destination up front turns out to matter.

**Done when** a device confirms: the diagnostics button writes a file to
Downloads and Share sends it, and a finished recording names its location.

## Phase 14 — Long-press suppression

Long press has no meaning anywhere in this UI; every control acts on release.
The platform did not know that, so holding a control produced a tap highlight,
then a text selection that stuck after the finger lifted.

Disabled, not delayed: a threshold would only postpone a gesture that does
nothing. Three mechanisms, since disabling one leaves the others —
`-webkit-tap-highlight-color`, `user-select`/`-webkit-touch-callout`, and the
Activity consuming long clicks (the only route to the haptic tick and the
context menu).

The CSS is scoped to controls. Text that exists to be read stays selectable,
the diagnostics dump above all: hand-selecting it is the documented fallback
for devices where the download and the clipboard both fail.

**Done when** a device confirms that holding a button or slider produces no
highlight, no haptic tick, and no stuck state, and that the diagnostics text
can still be selected by hand.

## Phase 15 — Capture lifecycle, and the end of full-resolution recording

From the first device log that could describe the Android half (Phase 12's
host diagnostics earning their keep immediately).

**One MediaProjection permits one VirtualDisplay.** From Android 14, asking for
a second ends the projection rather than failing. The log is unambiguous:
`record: start requested quality=native` at 08:29:04.811, `capture: stopped` at
08:29:05.176. Phase 11's second-display recorder was never going to work on a
current device; `ScreenRecorder.kt` is deleted. Full-resolution recording is
now done by raising the capture scale, which reaches the same encoder legally —
confirmed in the same log at 942x1920 for 864 frames.

**Capture lifetime is not stream lifetime.** Stopping the stream left the
capture service running, and the Sync page keyed its Stop button off stream
state, so a capture with no stream had no control anywhere in the app. The stop
path now asks the service rather than trusting per-page state, and the button
appears whenever capture is running.

**Done when** a device confirms: stopping from either tab ends both the stream
and the capture, and no state leaves the capture running with no way to stop it.

## Phase 16 — Splitting the frame path

Reported as sluggishness at a raised capture resolution; the log showed inbound
fps at 4.1.

One full-resolution RGB buffer served both the colour engine and the recorder.
The engine reduces to 64x36 and is indifferent to frame size, so at 942x1920
every frame cost 5.4M bounds-checked ByteBuffer.get(int) calls, a 5.4MB JNI
transfer, and a second copy in Go — to produce 2304 cells. Raising the
resolution for a video slowed the lights, which inverts the intent.

Now read once per consumer, in bulk rows: the engine gets a 2x2-averaged
downscale capped at `PIPELINE_LONG_EDGE` (480, ~7 samples per grid cell per
axis), and `FrameRecorder` converts the capture surface straight to YUV with no
RGB intermediate. Both are capped at ~30/s against a ~60/s compositor and a
20Hz output.

Downscaling is unconditional. It is invisible to the engine, so an option would
only be a way to choose the slow path.

Verified by modelling the index arithmetic in Python against padded and packed
strides and odd dimensions — the only way to check bounds here without an SDK.
It caught a stale row averaged into the bottom edge and an undeclared buffer.

**Done when** a device shows inbound fps holding up with the recording
resolution at 100%, and recordings still look right.

## Phase 17 — Restricted profiles, sticky heading, encoder tail

Phase 16 confirmed working on device (~20fps, reported as snappy, 2122 frames
recorded at full resolution with sync running). These are the defects visible
alongside it.

**A single-feature profile could not leave Settings.** `_renderNav` emitted no
feature link when only one of lights/sync existed, so the nav was Settings
alone — and the profile control is on that page. Every existing tab now gets a
link.

**The sticky room heading was content-width**, so cards scrolling under it
showed beside it. It now cancels `--page-pad` and restores it as padding.

**The encoder tail was dropped.** `drain(endOfStream = true)` returned at the
first `INFO_TRY_AGAIN_LATER`, discarding frames already queued — `in=2122
out=2121` in the device log. It now polls to `DRAIN_TIMEOUT_MS`, which also
governs whether the end-of-stream marker is ever seen.

**The pipeline size is logged on change**, since the live capture block is
absent from any report taken after capture stops, which is all of them.

**Done when** a device confirms both restricted profiles are navigable, the
heading covers what scrolls under it, and `in == out` on a recording.

---

# Publishing phases (18–29)

Full detail — every legal obligation, every channel's requirements, and the
secrets each one needs — is in [PUBLISHING.md](../PUBLISHING.md). This is the
running order and the acceptance criteria.

**FOSS channels first**, which is also cheapest-first. Nothing before phase 27
costs money or needs anyone's approval, and there is a real gap to fill: the
free-software Hue app selection is thin.

One thing blocks all of it and is not code: **there is no `LICENSE` file**. A
public repo without one is all rights reserved, so no packager may
redistribute it.

## Phase 18 — `LICENSE`, and consistent Android signing

GPL-3.0-or-later. It permits the FOSS channels, makes verbatim repackaging
straightforward to act on, and is compatible with every dependency — note that
GPL-2.0 would *not* be, since AndroidX is Apache-2.0.

Ship a real signing key at the same time (PACKAGING.md). Android refuses to
upgrade across a signature change, so every install made before this is a
reinstall later.

**Done when** `LICENSE` is committed and a tagged release produces a
consistently signed APK.

## Phase 19 — Obtainium

A README line. Obtainium installs from GitHub Releases and tracks updates from
them, which `release.yml` already produces. Needs phase 18's signing to be
useful, and nothing else.

**Done when** the app installs and self-updates through Obtainium.

## Phase 20 — Attribution and About screen

`THIRD_PARTY_LICENSES` generated by `go-licenses`, with `go-licenses check` in
CI so an incompatible dependency fails the build rather than a store review.
An About section in Settings showing version, licence, the full third-party
text served from embedded `web/` (offline devices must still show it), and the
trademark disclaimer.

**Done when** the About screen shows every licence with no network, and CI
fails on a deliberately-added copyleft dependency.

## Phase 21 — IzzyOnDroid

The first real Android store, and much lower friction than f-droid.org: it
accepts the APK already built rather than building from source. Needs phase 18
and Fastlane-structured metadata in the repo.

**Done when** the app is in the IzzyOnDroid repo and updates track releases.

## Phase 22 — Scoop, Homebrew tap, AUR

No review, no accounts beyond a repo. Also the first proof the release
artifacts are consumable by something other than a browser.

**Done when** `scoop install`, `brew install` and an AUR helper each work from
a clean machine.

## Phase 23 — Flathub

Manifest, AppStream metainfo with the SPDX licence, and offline sources —
Flatpak builds have no network, so Go modules must be vendored and Electron
pre-fetched. The Electron half is the work.

**Done when** the Flathub build passes and the app installs from Flathub.

## Phase 24 — F-Droid main repo

The flagship: built from source on their infrastructure and signed by them, so
there is no key to lose. The cost is a `gomobile bind` recipe that runs
unattended with the NDK.

**Done when** the app is in the F-Droid repo and updates land on a tag.

## Phase 25 — Release workflow split

`build` (no secrets) → `sign` → `publish`, with publish gated on a `release`
GitHub Environment carrying a manual approval, so no credential is reachable
from a pull request and no mistyped tag publishes by itself.

**Done when** a fork's PR cannot read any signing secret, and a tag push waits
for approval.

## Phase 26 — Privacy policy and store declarations

Needed from here on, not before — the FOSS channels above do not require one. A
page on the existing `docs/` site, exact about screen capture and recording,
plus the Data safety answers, the encryption export declaration and the
`FOREGROUND_SERVICE_MEDIA_PROJECTION` justification, all consistent with each
other.

**Done when** huemux.com/privacy is live and the four declarations agree.

## Phase 27 — Windows signing, then winget

Azure Trusted Signing is the only option that works cleanly from CI; OV and EV
certificates now live on hardware tokens. Requires verifying a legal identity.

**Done when** a downloaded release `.exe` runs with no SmartScreen warning.

## Phase 28 — macOS notarization, then Homebrew cask

Apple Developer Program, Developer ID certificate, `notarytool`, stapling, and
signing the embedded Electron framework as well as the app.

**Done when** the desktop app opens on a clean Mac without right-click-Open.

## Phase 29 — Google Play

Last, most gated, and the only channel where the trademark question has teeth —
Play carries plenty of third-party apps with "Hue" in the name, but it is also
the only place a complaint gets actioned quickly. Needs an account and identity
verification, a closed test held for a continuous period before production
unlocks, an App Bundle rather than the current APK, Play App Signing, and phase
26's declarations.

A separate Google account does not reliably insulate a personal one: Google
terminates associated accounts, inferring association from payment methods,
phone numbers and devices. See PUBLISHING.md §3.1.

**Done when** the app is on a production track with a staged rollout, and a
tagged release reaches the internal track without manual steps.
