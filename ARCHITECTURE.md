# Architecture

How a tap on a light card becomes a light changing, and where the time goes.
Written after measuring a real deployment (an Android wall panel driving a
LAN-hosted HueMux) because the perceived latency there — up to several seconds
— turned out not to be where it looked.

## The parts

```
┌─ browser ────────────────┐        ┌─ huemux (Go) ──────────┐      ┌─ Hue bridge ─┐
│                          │        │                        │      │              │
│  lights.js ──────────────┼──WS───▶│ handleControlMessage   │─HTTP▶│  CLIP v2     │
│    ▲                     │  :7654 │   └─ lightctl.Service  │      │              │
│    │  light_event        │◀──WS───┤ runLightEventBroadcast │◀SSE──┤  eventstream │
│    │                     │        │                        │      │              │
│  renderGrid()            │        │ engine.Engine ─────────┼DTLS─▶│  entertainment│
│  (full innerHTML)        │        │   (screen sync only)   │ UDP  │              │
└──────────────────────────┘        └────────────────────────┘      └──────────────┘
```

**One WebSocket per page.** `sync.html` and `lights.html` each open their own
to `/ws`; the shell (`app.html`) hosts both in iframes, so a full deployment
holds two. Every connected socket also receives a status push once per second.

**Two independent halves.** `internal/engine` owns screen sync (DTLS, capture
intake, the output clock). `internal/lightctl` owns day-to-day light control
(CLIP v2 REST plus the bridge's eventstream). They share nothing but the paired
bridge credentials, which is what makes `--profile=lights` able to drop the
engine entirely.

## Key systems

- **`internal/server`** — the loopback HTTP + WebSocket front end. Serves the
  embedded UI (`//go:embed web` in `assets.go`), a small JSON API
  (`/api/lights`, `/api/rooms`, `/api/scenes`, `/api/favorites`,
  `/api/config`, `/api/diagnostics`, `/api/presets`…), and `/ws`. UI and
  capture pipeline share that one socket: JSON control messages
  (`light_toggle`, `scene_recall`, `resync_lights`…) go through
  `handleControlMessage`; the capture pipeline sends binary frames (0x01
  reduced grid, 0x02 audio). The server also owns auth/origin checks, the 1 Hz
  status push, and the `lights_snapshot` full-state push on (re)connect.
- **`internal/hue`** — the CLIP v2 client: bridge discovery, REST calls, the
  eventstream (SSE) subscription, and the DTLS Entertainment stream
  (`stream.go`, UDP :2100). The eventstream is fire-and-forget with no replay —
  the reason reconnect/foreground resync exists.
- **`internal/lightctl`** — the light-control service layer over `hue`. It
  translates eventstream JSON into `lightctl.LightEvent`s for the broadcast and
  exposes `Snapshot()` for full-state resync. `server` talks to this, never to
  `hue` directly.
- **`internal/engine`** — the screen-sync runtime. Owns the output loop: it
  consumes the grid and audio frames the browser sends, runs them through
  `internal/pipeline` (sampling → smoothing → gamut → encoding) and
  `internal/preset` (music presets), and writes per-light values to the bridge
  over the DTLS stream. Nil under `--profile=lights`.
- **`internal/pipeline`** — stateless transforms: reduced `Grid` → per-channel
  colour values. Shared by screen sync and music reactivity.
- **`internal/music` + `internal/preset`** — the music-reactivity path. The
  browser does the audio analysis with the Web Audio API and sends small
  feature frames (32 bands, 0x02); `preset` runs a JSON DAG of primitives whose
  outputs feed the same per-light bus the sync pipeline uses.
- **`internal/config` vs `internal/appconfig`** — `config` owns bridge
  credentials and feature data (favourites); `config.SaveBridge` fsyncs the
  one-time clientkey. `appconfig` owns application configuration
  (defaults → file → flags, profiles, auth tokens). Kept separate so runtime
  settings never share the credential write path.
- **`web/`** — the embedded frontend. `app.html` is the shell; the tab pages
  (`lights.html`, `sync.html`, `settings.html`…) live in iframes, each opening
  its own `/ws`. `shared/` holds the cross-page pieces (theme, i18n, dropdowns,
  auth, slider-touch, header).
- **`mobile/` + `android/`** — the gomobile facade and Android wrapper: a
  WebView around the same embedded UI, pointed at the loopback server.

The wire protocol for all of the above — every JSON message type, the binary
frame types, and the state pushes — is specified in PROTOCOL.md; the
build/test loop is in AGENTS.md.

## What happens when you tap a light

1. `lights.js` delegates the click from `#lights-grid`, reads `data-action`,
   and sends `{"type":"light_toggle","rid":…,"on":…}` over the WebSocket.
2. `handleControlMessage` calls `lightctl.SetLightOn`, an HTTPS PUT to the
   bridge.
3. The bridge applies it and — separately — emits an event on its eventstream.
4. `runLightEventBroadcast` translates that into a `light_event` and broadcasts
   it to **every** connected socket.
5. `mergeLightEvent` updates the in-memory model and calls `renderGrid()`.

**The UI never updates from its own action.** It updates only when the change
comes back from the bridge. There is no optimistic local update, so the
round-trip latency is fully visible as dead time.

## Where the time actually goes

Measured on the wall panel (Chromium 83, 1280×800, 12 light cards, 9 gradient
layers) via the DevTools protocol over adb:

| Stage | Time |
|---|---|
| Touch → click handler → `ws.send` | **5 ms** |
| `ws.send` → first DOM change (server + bridge + eventstream) | **~460 ms** |
| One full `#lights-grid` rebuild, to forced layout | **~325 ms** |
| One full `#lights-grid` rebuild, **through to paint** | **~745 ms** |
| Full-grid rebuilds per single action | **2** |
| Single tap, idle page: tap → visible update | **420–480 ms** |
| Five taps 250 ms apart: first two taps | **1435 ms, 1321 ms** |

Three conclusions, one of which contradicts the obvious guess:

**The WebSocket is not slow.** Five milliseconds from touch to bytes on the
wire. The transport is not the problem, and neither is the Go server's
handling of the message.

**The bridge round trip is the floor.** ~460 ms covers the HTTPS PUT, the
bridge acting on it, and the eventstream reporting it back. That is the
Hue bridge's own latency and no amount of local optimisation removes it —
but an optimistic UI update would hide it completely.

**The client re-renders everything, and that is what compounds.**
`mergeLightEvent` ends in `renderGrid()`, which rebuilds the entire grid via
`innerHTML` — every card, every scene chip, every gradient. 325 ms of blocking
main-thread work, twice per action. Idle, that is merely wasteful. Under rapid
input the rebuilds queue behind each other and latency climbs to 1.4 s after
two taps, and keeps climbing from there. This is the mechanism behind the
multi-second delays.

**The gradients are not the cause — measured, not assumed.** The obvious
suspect was the nine `filter: blur()` layers, so they were A/B tested by
toggling the simple theme on the same page and re-running the rebuild
benchmark:

| | full theme | simple theme |
|---|---|---|
| Rebuild to layout | 318 ms | 325 ms |
| Rebuild through paint | 745 ms | 749 ms |
| Scroll, average frame | 46 ms | 56 ms |

No improvement anywhere, inside noise or slightly worse. The cost is DOM
reconstruction itself — parsing the markup, creating ~12 cards' worth of
buttons, inline SVG icons and `<input type=range>` controls, then recalculating
style and layout for all of it. Blur affects compositing, which is not where
the time goes.

This matters for what to fix: simplifying the theme is a legitimate preference
and may help weaker GPUs, but it will not make the UI feel faster. Only doing
less DOM work will.

## Why it compounds rather than just being slow

`innerHTML` on the grid destroys and recreates every node. That means:

- Style recalculation and layout for the whole subtree, not the one card that
  changed.
- Every `.light-card-gradient` blur layer is re-rasterised.
- The main thread is blocked for the duration, so queued taps cannot be
  dispatched and the next event's rebuild starts late.

Two taps in quick succession therefore cost more than twice one tap.

## Results after fixing

Items 1–3 below are implemented. Re-measured on the same wall panel:

| | before | after |
|---|---|---|
| Single tap → visible update | 420–480 ms | **6–17 ms** |
| Five taps 250 ms apart | 1435, 1321, 602, 396, 369 ms | **12, 45, 53, 29, 16 ms** |
| All-lights toggle | worst case, one event per light | **53 ms** |
| Full grid rebuilds per action | 2 | **0** |

The compounding is gone entirely — rapid taps no longer queue, because nothing
blocks the main thread for hundreds of milliseconds. Correctness was checked by
comparing every card's rendered state against `/api/lights` after a burst of
toggles: no mismatches, so the optimistic path still reconciles with whatever
the bridge actually did.

## The fix, in priority order

1. **Optimistic local update.** Apply the toggle to the local model and the
   single affected element immediately, before the server confirms. Removes
   the entire ~460 ms of visible dead time. The eventstream then reconciles.
2. **Targeted DOM updates instead of a full rebuild.** A `light_event` names
   exactly one light; update that card's power state, brightness and colour in
   place. Reserve the full `renderGrid()` for changes that genuinely alter
   structure (filter change, room grouping, initial load).
3. **Coalesce bursts.** If a full rebuild is unavoidable, batch events arriving
   within an animation frame so a room-wide change costs one rebuild instead
   of one per light.
4. **Cheaper themes — for preference, not for speed.** The simple variants
   drop the blurred layers and hover elevation while keeping every accent
   colour. Measured above, this does **not** reduce rebuild cost on the test
   device, so it is offered as a look-and-feel choice rather than a fix. It
   may still help GPU-bound hardware weaker than the one measured.

Items 1–3 are implemented and client-side only; nothing in the Go server
changed. Item 4 shipped as a preference, not a performance feature.

`patchLightCard` and `patchRoomTile` return `false` when their element is not
on screen, so anything genuinely structural — the Favorites view gaining a
card, a filter change — still falls through to a full (now coalesced) render.
That fallback is what keeps the optimisation from becoming a correctness bug.

## Theme complexity

`web/shared/theme.css` drives everything from CSS custom properties, and the
per-card ambience is a separate absolutely-positioned `.light-card-gradient`
element carrying `filter: blur(50px)` (24 px on coarse pointers) plus an
opacity transition on hover.

That is the most expensive piece of the UI to *composite* — though, per the
measurements above, compositing is not what makes interaction slow. The
**Simple Light** and **Simple Dark** variants keep the
palette and the accent colours — including the per-light colour, rendered as a
plain tinted border rather than a blurred wash — and drop the blur layers and
the hover elevation. Stored client-side in `localStorage` under `theme`,
alongside the existing system/light/dark choice, so it costs the server
nothing and can differ per device: a phone can keep the full look while the
wall panel runs simple.

## Notes for anyone changing this

- The frontend is compiled into the binary with `go:embed`. Editing `web/`
  changes nothing until you rebuild — see [AGENTS.md](AGENTS.md).
- Hover is not a reliable proxy for "has a mouse". The wall panel reports
  `hover: hover` and `any-hover: hover` despite being a touchscreen, so
  `:hover` styles fire on tap and stick. Gate touch behaviour on
  `pointer: coarse`, which that device reports correctly.
- Status is pushed at 1 Hz to every socket regardless of activity. It is small,
  but it is per-socket, and the shell holds two.
- Origin checking is the load-bearing security control for `/ws`. It accepts
  loopback, the configured listen host, and — when bound to a wildcard — any
  address this machine actually holds. Never widen it further; see
  `internal/server/ws.go`.
