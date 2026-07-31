# Plan 02 — Standalone Android app (gomobile + WebView)

Status: **proposed**, not started. Depends on [Plan 01](01-config-profiles-and-access.md)
for the settings screen and config schema (M2 onward); M1 can start in parallel.

## Context

The goal is an Android app to drive the lights, with screen sync from the
phone as a bonus if it's achievable. The open question was whether the
Go/Electron architecture survives the move to Android, or whether the Hue
logic would have to be reimplemented in Kotlin — which is exactly the
duplication this project exists to avoid.

It survives, essentially intact. Two findings decide the shape:

**1. Web-based screen capture on a phone is impossible — verified, not
assumed.** `getDisplayMedia()` is unsupported on *every* mobile browser:
Chrome Android 150, Firefox Android 152, Safari iOS 26.5, Samsung Internet 30,
Android WebView. This is not a gap that closes with a newer WebView; there is
no web path to phone screen capture. Any streaming-from-Android must go
through the native `MediaProjection` API.

**2. The client/server seam is already clean.** `grep -rn "electron" internal/
cmd/huemux/` returns only *comments* — the core has zero knowledge of its
desktop wrapper. `huemux-desktop` is just a second `main` that starts the
ordinary server and points a window at it. An Android app is architecturally
the same move, and the desktop wrapper is the working proof that the pattern
holds.

## Architecture: run the Go core on the phone

```
┌─ APK ────────────────────────────────────────────┐
│                                                  │
│  MainActivity                                    │
│    └─ WebView → http://127.0.0.1:<port>          │
│                                                  │
│  huemux.aar  (gomobile bind of ./mobile)         │
│    └─ internal/server + lightctl + engine        │
│         + hue (CLIP v2 REST, DTLS)               │
│                                                  │
│  ScreenCaptureService  (foreground, sync only)   │
│    MediaProjection → VirtualDisplay              │
│      → ImageReader → pack RGB → PushFrame()      │
└──────────────────┬───────────────────────────────┘
                   │ LAN: HTTPS/CLIP v2 + DTLS/UDP
                   ▼
              Hue bridge
```

Why this shape wins over a thin LAN client:

- **The loopback security model transfers unchanged.** The WebView talks to
  `127.0.0.1`, so `checkOrigin` (`internal/server/ws.go:52`) passes as-is and
  the listener stays loopback-only. No auth, no TLS, no Origin relaxation
  needed for the app to work — all of Plan 01's network surface stays off by
  default on mobile.
- **Zero duplication.** Pairing, discovery, CLIP v2, DTLS, the colour
  pipeline, favorites, i18n, theming — all reused verbatim. `pion/dtls` is
  pure Go with `CGO_ENABLED=0`, so it cross-compiles to `android/arm64`
  without special handling.
- **It works with no server running.** Standalone, on the couch, with the
  homelab off.
- **The UI already mostly exists.** `lights.html` + `lights.js` + `lights.css`
  are self-contained, have no capture dependency, and are already
  mobile-first: the card grid is `flex-direction: column` by default
  (`lights.css:77-85`), the colour picker is genuinely touch-designed with
  `pointerType === 'touch'` handling that offsets the cursor above the finger
  (`lights.js:657-659`), and `touch-action` is set correctly throughout.

## The gomobile facade

`gomobile bind` only exports a restricted type set (signed ints, floats,
string, bool, `[]byte`, `error`, and types defined in the bound package). So
rather than binding `internal/server` directly, add a small purpose-built
package at `mobile/` (repo root, not under `internal/`) whose whole job is to
present a flat, bindable surface:

```go
package mobile

// Lifecycle. configDir comes from Kotlin — Android has no os.UserConfigDir().
func Start(configDir string) (string, error) // returns the base URL
func Stop()

// State
func IsPaired() bool
func ConfigJSON() string
func SetConfigJSON(json string) error

// Sync (M3)
func StartSync(areaID string) error
func StopSync()
func PushFrame(w, h int, rgb []byte) error
```

Note `PushFrame` bypasses the WebSocket entirely — Kotlin calls into Go
in-process, so the `[0x01][w][h][RGB…]` wire format
(`web/capture-worker.js:107`, parsed at `internal/server/http.go:442`) isn't
needed on this path. It stays the contract for *browser* clients only. The
frame goes straight to `engine.SetFrame` (`internal/engine/engine.go:251`).

`internal/config.Dir()` (`internal/config/config.go:22`) resolves via
`os.UserConfigDir()`, which is wrong on Android — it needs to accept an
injected base path. Small change, but it touches all three existing stores.

## Streaming from the phone (M3)

`MediaProjection` → `VirtualDisplay` → `ImageReader`, with the virtual display
created at low resolution directly (~320×180) so the compositor does the
scaling — the same trick the desktop path uses via track constraints
(`web/app.js:349-361`), and far cheaper than downsampling full frames.
`ImageReader` gives `RGBA_8888`; pack to RGB and hand off.

Platform requirements that will bite if not planned for:

- Android 10+ requires a foreground service with type `mediaProjection`.
- **Android 14+ requires `startForeground()` to be called *before*
  `getMediaProjection()`** — getting this order wrong throws at runtime.
- The system consent dialog is unavoidable and non-suppressible, same as the
  Wayland portal dialog already documented in `KNOWN_ISSUES.md`.

Worth being honest about the use case in the docs: syncing lights to *the
phone's own screen* is niche. The compelling version is casting/media playing
on the phone. This is correctly scoped as a bonus, after lights control is
solid.

## Files to add

**New (Go):**
- `mobile/mobile.go` — the facade above.
- `mobile/doc.go` — build instructions, gomobile version pinning.

**New (Android, under `android/`):**
- Standard Gradle project.
- `MainActivity.kt` — WebView host; `Start()` on resume, `Stop()` on
  destroy. `WebSettings.setJavaScriptEnabled(true)`,
  `setDomStorageEnabled(true)` (required — theme and language preferences use
  `localStorage`, `web/shared/theme.js:12`).
- `ScreenCaptureService.kt` — foreground service, MediaProjection (M3).
- Icons, `AndroidManifest.xml`, signing config.

**Modified:**
- `internal/config/config.go:22` — accept an injected config dir.
- `Makefile` — `make android` (gomobile bind) and `make android-apk`.
- `.github/workflows/release.yml` — Android SDK/NDK setup, gomobile install,
  AAR + APK build, signing from a keystore secret, attach APK to the release.

## Mobile UI polish (M2)

Concrete issues found reading the current CSS, all in `lights.*`:

- `.filter-list` is `position: absolute; min-width: 170px; max-height: 300px`
  (`lights.css:50-55`) anchored under a `<details>` in a flex toolbar — with
  several rooms this is a cramped scroller on a phone. Wants a bottom-sheet
  treatment.
- Colour picker canvas height is computed as `overlay.clientHeight - 180`
  (`lights.js:616`) with a hardcoded constant commented "matches lights.css".
  On a short screen in landscape this leaves almost no canvas. Should derive
  from measured header/footer.
- `.light-card-gradient` applies `filter: blur(50px)` per card
  (`web/shared/theme.css:152`) — expensive to composite on mid-range Android
  GPUs once many cards are on screen.
- Touch targets below the 44px minimum: `.ls-icon-btn` is 36×36
  (`header.css:41-42`), `.scene-chip-star` is 32×32 (`theme.css:256`).
- No safe-area-inset handling for notches and gesture bars.
- Fira Code monospace at `line-height: 1.6` (`theme.css:122`) is wide; long
  room names hit `text-overflow: ellipsis` aggressively at 360px.

Also worth revisiting on mobile: `app.html` loads **both** iframes eagerly
(`web/app.html:30-33`), so two independent WebSocket connections open on
launch, each receiving a 1 Hz status push. On a phone, in a `lights` profile,
only one is needed — Plan 01's profile-aware shell handles this.

## Phasing

- **M1 — Lights, standalone.** `mobile/` facade, gomobile build in the
  Makefile, minimal Kotlin app, WebView, config dir injection. Success: pair
  with the bridge from a fresh install and control lights, with no server
  running anywhere. No foreground service needed — the Go server only has to
  live as long as the app is in the foreground.
- **M2 — Config + polish.** Settings screen from Plan 01, the mobile CSS
  fixes above, app icon, launch screen.
- **M3 — Screen sync.** MediaProjection, foreground service, `PushFrame`.
- **M4 — Distribution.** CI build and signing, APK on GitHub releases.
  F-Droid is a natural fit (reproducible, no proprietary deps); Play Store
  brings a `MediaProjection` policy review, which is another reason to keep
  sync out of M1.

## Risks

| Risk | Assessment |
|---|---|
| **Bridge discovery on Android** | `hue.Discover` needs checking — mDNS/SSDP on Android requires a `MulticastLock`, and some OEM ROMs filter multicast on battery saver. Manual IP entry already exists as a fallback (`web/app.js` manual pairing panel), so this degrades rather than blocks. **Verify early in M1.** |
| **gomobile maintenance** | Still maintained (docs updated July 2026), but it is a `x/mobile` tool with a long tail of open issues and is not a fast-moving project. Pin the version. The facade being tiny is the mitigation — if gomobile ever becomes untenable, the fallback is shipping the Go binary in `jniLibs` as `lib*.so` and exec'ing it, which is uglier but well-trodden. |
| **APK size** | Go runtime pushes ~10–15 MB per ABI. Ship per-ABI splits (`arm64-v8a` realistically covers everything current) rather than a universal APK. |
| **Battery** | Lights-only is on-demand and cheap. Streaming holds a foreground service, MediaProjection, and a 20–50 Hz DTLS output loop — genuinely heavy. Document it; don't hide it. |
| **Doze / background kill** | Only affects M3. Lights control doesn't need to survive backgrounding. |
| **WebView version skew** | The app depends on the system WebView (Android System WebView / Chrome). `lights.js` is plain ES2017-era JS with no build step, so the floor is low, but `MediaStreamTrackProcessor` feature detection in `app.js` must not throw on a WebView that lacks it — the sync page should degrade rather than error. |

## Verification

- **M1 on real hardware, not just an emulator** — the emulator can't reach a
  real bridge on the LAN, and multicast discovery behaves differently.
- Fresh install → discovery finds the bridge (or manual IP works) → link-button
  pairing succeeds → lights, rooms, scenes, and favorites all function.
- Kill and relaunch: confirm credentials persisted to the injected config dir
  and no re-pair is needed.
- Airplane mode / bridge unreachable: confirm the UI degrades rather than
  hanging (the WS reconnect is a flat 1500 ms retry forever with no backoff,
  `web/lights.js:69-86` — worth revisiting for battery on mobile).
- M3: confirm capture consent flow, confirm frames reach the bridge, and
  measure battery draw over 30 minutes of sync before calling it shippable.
