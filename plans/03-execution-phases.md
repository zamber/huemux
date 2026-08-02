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
| 18 | `LICENSE` (GPL-3.0-or-later) + Android signing | **done** |
| 19 | Obtainium — README line, works with current CI | **done — verify on alpha.20** |
| **R1** | **Release infrastructure — artifact signing, key custody** | **done (Windows/macOS gated on certificates)** |
| **R2** | **Release infrastructure — workflow split + gated environments** | **done** |
| **R3** | **Release infrastructure — automated test gate** | **deferred: revisit when leaving 0.0.x** |
| C1 | Attribution + About screen | **done** |
| C2 | IzzyOnDroid | **prepared — needs screenshots, then submit** |
| C3 | Scoop, Homebrew tap, AUR | **not started** |
| C4 | Flathub | **not started** |
| C5 | F-Droid main repo | **not started** |
| C6 | Privacy policy + store declarations | **not started** |
| C7 | Windows certificate, then winget | **blocked: needs a purchased certificate** |
| C8 | macOS notarization, then Homebrew cask | **blocked: needs Apple Developer membership** |
| C9 | Google Play | **not started** |

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

# Release infrastructure (R) and channels (C)

Renumbered from a single 18–29 chain into two tracks, because the chain implied
a false dependency: the channels barely depend on each other, and serialising
them means the slowest one blocks the rest.

- **R1–R3 come first** and are shared by everything. Once they are done, any
  channel can be worked on without touching release plumbing again.
- **C1–C9 are then parallel.** C1 is a soft prerequisite for the store
  channels (they want an About screen and attribution), and C7/C8 are blocked
  on purchases rather than on effort. Nothing else has an ordering constraint.

Full detail is in [PUBLISHING.md](../PUBLISHING.md).

## R1 — Artifact signing and key custody — **done**

Android releases are signed (phase 18). Release artifacts are covered by a
detached GPG signature over `SHA256SUMS`, which is the convention Linux
packagers and wary users already know how to check; the public key is committed
as `RELEASE-SIGNING-KEY.asc` and shipped with every release.

Windows and macOS signing steps are wired into the workflow but gated on
secrets that do not exist, because neither can be self-issued:

- **Authenticode** needs a CA-issued certificate — Azure Trusted Signing, or an
  OV/EV cert on a hardware token. A self-signed certificate satisfies nothing
  and is a key to manage for no benefit.
- **Developer ID** is issued by Apple to paying members only.

Both fail loudly if their secrets appear before the implementation does, rather
than silently producing unsigned output.

Key custody: keys live outside the repo at `~/.huemux-signing/` (0700, files
0600), with `backup-to-bitwarden.sh` for the vault copy and `OFFLINE-BACKUP.md`
for the copy that does not depend on an account. The Android keystore is
unrecoverable if lost, so one copy is not a backup.

## R2 — Workflow split and gated environments — **done**

Four jobs: `build` (no secrets) and `android` (keystore) run in parallel,
`sign` adds the GPG signature, `publish` creates the release.

Signing secrets moved from repository scope into a `signing` environment, so
only a job naming it can read them. That was not theoretical: `android.yml` has
a `pull_request` trigger, and a branch PR could previously have read the app
signing key.

The approval gate is keyed to the tag, because alphas are the development loop
here and must not need a human each time:

| Tag | Environment | Behaviour |
|---|---|---|
| `v0.1.0-alpha.3` | `release-prerelease` | publishes unattended |
| `v0.1.0` | `release-stable` | waits for a reviewer |

Two environments rather than one condition, because a required reviewer is a
property of the environment and cannot be switched on by an expression.

Token permissions are read-only by default; only `publish` gets
`contents: write`.

Proven on v0.0.2-alpha.21: the APK came out release-signed (so the
environment-scoped keystore was readable from the job that names it), the
published `SHA256SUMS.asc` verifies against the committed public key, and the
alpha published unattended. The duplicate repository-level secrets have since
been deleted, so the signing keys now exist in exactly one place on GitHub.

## R3 — Automated test gate — **deferred**

Deliberately parked while the project is 0.0.x and shaped by fast user
feedback: the loop is a device in the maintainer's hand, and a test suite
written against a UI that is still changing weekly would mostly measure churn.
Revisit on leaving 0.0.x, when the shape has settled and regressions start
costing more than the tests would.

The analysis below stands and is what to pick up then.

### What it should cover

Currently the release runs `go vet` and `go test`, which covers the Go core and
nothing else. The parts that have broken most often in this project — the
frontend and the Android half — have no automated coverage at all, and every
regression so far was found by hand on a device.

What is missing, in the order it pays off:

1. **A server smoke test in CI.** Start the built binary, assert it serves
   `/app.html`, `/api/config` and the embedded assets, and exits cleanly. Would
   have caught nothing so far, but it is the floor.
2. **Frontend unit tests.** There is no build step and no test runner. `node
   --test` against the pure-logic modules (`theme.js`'s palette/effects split,
   `slider-touch.js`'s gesture rules, `dropdown.js`'s dismissal) needs no
   bundler and would have caught the slider bug that shipped in alpha.14.
3. **A headless browser check.** The shell's frame integrity, the standalone
   redirect, and the lights cache are all exactly what Playwright is for, and
   all three have regressed at least once.
4. **Kotlin compilation in CI on every push**, not only on a tag. The Android
   half is currently compiled for the first time by a release build, which is
   the worst possible moment to discover a syntax error.
5. **A device checklist** in the repo for what genuinely cannot be automated:
   MediaProjection consent, recording playback, upgrade-over-install.

**Done when** items 1, 2 and 4 run on every push and a release cannot be cut
with any of them failing.

## C1 — Attribution and About screen

`THIRD_PARTY_LICENSES` generated by `go-licenses`, with `go-licenses check` in
CI. An About section showing version, licence, the full third-party text served
from embedded `web/` (offline devices must still show it), and the trademark
disclaimer.

**Done when** the About screen shows every licence with no network, and CI
fails on a deliberately-added copyleft dependency.

## C2 — IzzyOnDroid — **prepared**

Metadata is in `fastlane/` in both English and Polish, and the app now has a
real launcher icon: it had been shipping `@android:drawable/ic_menu_compass`, a
stock system drawable, which no store accepts and which is not stable API. The
replacement is an adaptive vector — no density buckets, about a kilobyte, and
it doubles as the Android 13+ themed icon.

**Blocking submission:** screenshots and a 512x512 `icon.png`. The screenshots
are deliberately not taken from the existing ones, which show the bridge's LAN
address that this repo does not carry.

**Then:** open an issue at gitlab.com/IzzyOnDroid/repo with the repo URL and
the APK naming pattern; they track releases from there. See PUBLISHING.md
§2A.2.

## C3 — Scoop, Homebrew tap, AUR

No review, no accounts beyond a repo. Also the first proof the release
artifacts are consumable by something other than a browser.

## C4 — Flathub

Manifest, AppStream metainfo with the SPDX licence, and offline sources — Go
modules vendored, Electron pre-fetched. The Electron half is the work.

## C5 — F-Droid main repo

Built from source on their infrastructure and signed by them. The cost is a
`gomobile bind` recipe that runs unattended with the NDK.

## C6 — Privacy policy and store declarations

Not needed by C2–C5. Required from C9, and cheap to do alongside C1.

## C7 — Windows certificate, then winget — **blocked**

Azure Trusted Signing is the only option that works cleanly from CI. Requires
verifying a legal identity and paying. The workflow step is already in place.

## C8 — macOS notarization, then Homebrew cask — **blocked**

Apple Developer Program, Developer ID certificate, `notarytool`, stapling, and
signing the embedded Electron framework as well as the app.

## C9 — Google Play

Most gated. Account and identity verification, a closed test held for a
continuous period before production unlocks, an App Bundle rather than an APK,
Play App Signing, and C6's declarations. A separate Google account does not
reliably insulate a personal one — see PUBLISHING.md §3.1.
