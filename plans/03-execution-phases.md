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
| 7 | Android M1 — lights, standalone | **built, untested on a device** |
| 8 | Android M2 — config + mobile polish | **done** |
| 9 | Android M3 — MediaProjection sync | not started |
| 10 | Android M4 — CI build, signing, distribution | **done (signing awaits a keystore secret)** |

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
