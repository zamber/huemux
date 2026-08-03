# HueMux — Implementation Roadmap

A single-binary localhost service that serves a plain-JS web UI, captures the screen
in the browser, and streams the resulting colors to a Philips Hue Entertainment area
over DTLS.

This document is the build order. Each milestone has an **exit test** — a concrete
thing that must work before moving on. Completed milestones are checked off; planned
ones describe what's next.

---

## 0. Why this shape

Two hard constraints decide the entire architecture:

1. **The browser cannot talk to the bridge.** Hue Entertainment is DTLS 1.2 over UDP
   port 2100 with a pre-shared key and exactly one permitted cipher suite,
   `TLS_PSK_WITH_AES_128_GCM_SHA256`. No browser API exposes raw UDP or DTLS-PSK.
   Something native must own the socket.
2. **`getDisplayMedia()` requires a secure context.** `http://lights.lan` is not one.
   `http://127.0.0.1` and `http://localhost` are — the spec grants loopback
   origins secure-context status without TLS.

Constraint 2 is why this is a separate localhost service instead of a page on a
remote server. We get screen capture with zero certificate work, and the same
process is the natural owner of the DTLS socket from constraint 1.

```
 ┌─ browser tab (http://127.0.0.1:7654) ──────────────┐
 │ getDisplayMedia ──▶ worker ──▶ 64×36 grid           │
 │                                zone averages (N×RGB)│
 └───────────────────────────────────┼────────────────┘
                                      │ WebSocket, binary, ~25 Hz
 ┌─ huemux (Go, single binary) ────▼───────────────┐
 │ smoothing ▶ gains ▶ gamut ▶ HueStream encoder       │
 │ DTLS/PSK session ─────────────────┼──▶ bridge:2100/udp
 │ CLIP v2 REST (pair, list areas, start/stop)         │
 │ embedded web assets, CLI status readout             │
 └──────────────────────────────────────────────────────┘
```

**Where the work happens.** Smoothing, color correction and gamut mapping live in Go,
not JS. The browser's only job is: capture, reduce, send zone averages. This keeps the
tuning logic in the place that owns the output clock.

---

## ✅ Completed

### Milestone 1 — Bridge discovery and pairing

- [x] mDNS discovery `_hue._tcp.local`
- [x] Cloud discovery fallback `https://discovery.meethue.com`
- [x] Manual IP entry
- [x] Pairing: `POST /api` with `generateclientkey: true`, poll every 2 s, 60 s timeout
- [x] Persist `{bridge_ip, bridge_id, username, clientkey, cert_sha256}` to config
- [x] Certificate SHA-256 pinning after first pairing (2026-08-02)

**Exit test:** `huemux pair` writes a config file, `huemux areas` lists entertainment
areas by name. ✅

### Milestone 2 — DTLS session and a static color

- [x] DTLS 1.2 PSK handshake (`TLS_PSK_WITH_AES_128_GCM_SHA256`)
- [x] HueStream v2 packet encoder (ASCII header + protocol version + area UUID + channels)
- [x] Send one static color for 5 seconds, fade out, close

**Exit test:** `huemux test <area-id>` lights up the room in one color, then fades
out and exits cleanly. ✅

### Milestone 3 — Localhost server and UI shell

- [x] HTTP server on 127.0.0.1:7654 (loopback-only by default)
- [x] Embedded web assets (`//go:embed web`)
- [x] WebSocket transport (RFC 6455, binary grid frames)
- [x] Origin check on WebSocket upgrade (loopback + configured listen host)
- [x] Shell with iframe tabs (lights, sync, settings)
- [x] Shared header with theme/language toggles
- [x] i18n support (26 locales, machine-translated)

**Exit test:** `./huemux` starts, browser shows the shell, WebSocket connects. ✅

### Milestone 4 — Capture in the browser

- [x] `getDisplayMedia()` screen/window/tab capture
- [x] Offscreen canvas worker for pixel reduction to 64×36 grid
- [x] Binary WebSocket frames at capture rate (typed array → WS → Go)
- [x] Handle source ending (user clicks Stop in browser chrome)
- [x] Claim mechanism: only one tab streams frames at a time

**Exit test:** Click "Start sync," pick a window, the preview shows downscaled
capture. ✅

### Milestone 5 — Zone mapping

- [x] Build zones from entertainment configuration channels
- [x] Support all 5 entertainment configuration types (screen, monitor, music,
      3D space, other)
- [x] Configurable axis mapping (which physical axis = screen horizontal/vertical/depth)
- [x] Four sampling modes: edges, quadrant, global, spread
- [x] Letterbox detection and masking

**Exit test:** Zone preview in the calibration UI shows the correct number of
rectangles with live color updating. ✅

### Milestone 6 — Color pipeline

- [x] Linear-light averaging (sRGB → linear → average → sRGB)
- [x] Per-channel gain (brightness, saturation)
- [x] Gamut mapping (xy + brightness → RGB per light's color features)
- [x] Black cutoff

**Exit test:** Colors on the lights visually match the calibration preview. ✅

### Milestone 7 — Temporal behaviour

- [x] Fixed-rate output loop independent of capture rate
- [x] EMA temporal smoothing (asymmetric: darker-faster, brighter-slower)
- [x] Scene-cut detection (snap to new color on large luma delta)
- [x] Fade-out on stream stop (ramp to black, not hard cut)
- [x] Keepalive ticks when no new frames arrive

**Exit test:** Screen sync feels smooth, no flicker, lights don't revert mid-session. ✅

### Milestone 8 — Controls and persistence

- [x] Per-area settings persistence (debounced JSON write)
- [x] Reactivity slider (0-100%)
- [x] Brightness and saturation controls
- [x] Backfill defaults for settings files written before new fields existed
- [x] `output_hz` clamping to [1, 25]

**Exit test:** Drag a slider, reload the page — the value is still there. ✅

### Milestone 9 — Lights tab

- [x] Light/room/scene/favorite listing via CLIP v2
- [x] Light toggle (on/off), brightness, color picker
- [x] Room grouping with all-lights tile
- [x] Scene recall (static + dynamic/entertainment scenes)
- [x] Favorites toggle and filter
- [x] Live light event push over WebSocket (SSE → Go → WS → UI)

**Exit test:** Turn a light on/off from the UI, change its color, recall a scene. ✅

### Milestone 10 — Packaging & release

- [x] `CGO_ENABLED=0` static binaries (Linux amd64/arm64, Windows amd64, macOS amd64/arm64)
- [x] GitHub Actions release workflow (vet → test → lint → build → attach)
- [x] Android AAR via gomobile + CI with signing
- [x] Android APK with WebView host activity
- [x] Desktop Electron wrapper (`cmd/huemux-desktop`, astilectron)
- [x] AppImage for Linux desktop
- [x] Homebrew tap
- [x] IzzyOnDroid / Obtainium distribution
- [x] GPG-signed release artifacts

**Exit test:** Tag `v0.0.2-alpha.N`, CI produces binaries + AAR + APK, all
platforms boot and show the UI. ✅

### Milestone 11 — Security hardening (2026-08-02 audit)

- [x] WebSocket frame size limit (16 MiB, prevents OOM from malicious frames)
- [x] `output_hz` validation on ingest (prevents `time.NewTicker(0)` panic)
- [x] CSRF protection on `/api/config` (POST dropped, PATCH-only with CORS preflight)
- [x] Frontend auth flow (`/auth.html`, `localStorage` token persistence, logout)
- [x] Data race fix: `Stream.Stats()` accessor under `s.mu`
- [x] Concurrent `SelectArea` serialization (`selectMu`)
- [x] Token wordlist replaced with server's 382-word list (15→34 bits entropy)
- [x] Android WebView JS bridge restricted to app's own host
- [x] Bridge TLS certificate pinning (SHA-256 fingerprint stored at pairing)
- [x] Content-Security-Policy + security headers on all HTTP responses
- [x] Broadcast write deadline (prevents head-of-line blocking from slow clients)
- [x] `.golangci.yml` CI integration (govet, staticcheck, errcheck, gosec, unused, misspell)
- [x] Test coverage: hue, engine, config, pipeline packages (0% → 17-78%)

**Exit test:** Zero lint findings, all tests pass, `go vet` clean. ✅

---

## 🔲 Planned

### Milestone 12 — Music reactivity (see docs/MUSIC-REACTIVITY.md)

Four-phase plan for a modular audio-reactive light engine:

- **Phase 1:** Audio capture (mic + internal), FFT analysis, beat detection,
  frequency band splitting. Browser sends feature values to Go over WebSocket.
  Exit: beat indicator flashes on clap, BPM stabilizes within 5 s of music.
- **Phase 2:** Effect primitives (brightness, color, strobe, chase, pulse) +
  routing + modulation. Exit: "Bass Pulse" preset drives lights from music.
- **Phase 3:** Preset builder UI (desktop node editor + phone card browser).
  Exit: build a preset without editing JSON.
- **Phase 4:** System audio capture (no mic), interactive visualization, spatial
  routing, MIDI/OSC control. Exit: Spotify → lights react, headphones on.

Full design, primitive catalog, UI sketches, decision points, and extension
architecture in [docs/MUSIC-REACTIVITY.md](docs/MUSIC-REACTIVITY.md).

### Milestone 13 — Multi-bridge support

Users with >50 lights need two bridges. The Hue app itself recently added this;
third-party clients lag behind.

- Bridge registry in config (array, not single object)
- Aggregate lights/rooms/scenes across bridges
- Per-bridge entertainment area selection
- "Which bridge is this light on?" indicator in UI
- Transparent switching (no "switch bridge" button — just show everything)

### Milestone 14 — Gradient product support

Hue Gradient lightstrips, Play gradient tubes, and Signe lamps expose
per-segment color channels through the Entertainment API. Current behavior
treats them as single lights.

- Multi-channel light grouping (N channels = 1 gradient device)
- Per-segment color interpolation
- Gradient-aware effects (color sweep along strip, not per-light)

### Milestone 15 — Profile/device management

- Per-device settings sync (export/import config + presets)
- Multiple entertainment area profiles (living room vs. bedroom tuning)
- Config migration between machines
- Backup/restore of bridge pairing (avoid re-pressing link button)

### Milestone 16 — Standalone headless mode

- System audio capture without a browser (PipeWire/PulseAudio/ALSA)
- Screen capture without a browser (X11 SHM, PipeWire screencast, Windows DXGI)
- Run as a background service with no UI
- Control via CLI or a companion web app on another device

### Milestone 17 — Accessibility & platform maturity

- Keyboard navigation for all controls (color picker, scene chips, sliders)
- Screen reader support (ARIA labels, live regions for status changes)
- Reduced-motion mode
- Flathub / F-Droid submission
- Windows MSIX package
- macOS code signing + notarization

---

## Non-goals (explicitly out of scope for v1.x)

These are not accidents — they are deliberate scope boundaries:

- **Multiple simultaneous entertainment areas.** One area at a time. The
  bridge hardware can only stream to one area per bridge.
- **Non-Hue outputs (WLED/DDP/DMX).** The pipeline is HueStream-specific.
  A `Target` interface could abstract this, but it's not planned.
- **Cloud relay / remote access service.** LAN-only. Use Tailscale or
  a VPN to reach your home network.
- **iOS app.** The web UI works in Safari. A native iOS app needs Apple
  Developer Program enrollment and a different UI framework.

---

## Failure modes, and what they actually mean

| Symptom | Cause | Fix |
|---|---|---|
| Handshake times out, no error detail | PSK passed as ASCII, not hex-decoded | Decode the clientkey to 16 bytes |
| Handshake fails after ClientHello | Extended master secret not negotiated | `DisableExtendedMasterSecret` |
| Handshake refused | `action: start` never sent, or area held by another app | Check `active_streamer` |
| Lights revert after ~10 s | Keepalive gap | Output loop must run on its own ticker |
| Lights flicker / lag in bursts | Sending faster than ~25 Hz | Clamp `output_hz` |
| Colors look washed out and gray | Averaging in gamma space | Convert to linear before averaging |
| Only one light in a gradient strip responds | Iterating light services, not channels | Iterate `channels[]` |
| Zones mirrored top/bottom | Depth axis sign | Flip `invert_depth` |

## Latency budget

| Stage | Budget | Notes |
|---|---|---|
| Capture (getDisplayMedia) | 16-33 ms | 30-60 fps native capture |
| Worker grid reduction | <1 ms | 64×36 on OffscreenCanvas |
| WS frame to Go | <1 ms | Loopback, no network |
| Smoothing + pipeline | <1 ms | Pure math, 64-200 channels |
| DTLS encode + send | 1-2 ms | AES-128-GCM hardware-accelerated |
| Bridge processing | 10-20 ms | Bridge firmware decode + PWM update |
| **Total** | **~30-57 ms** | Well under the ~80ms human flicker threshold |
