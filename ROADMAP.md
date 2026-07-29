# lightsync — Implementation Roadmap

A single-binary localhost service that serves a plain-JS web UI, captures the screen
in the browser, and streams the resulting colors to a Philips Hue Entertainment area
over DTLS.

This document is the build order. Each milestone has an **exit test** — a concrete
thing that must work before moving on. Do not skip the exit tests; almost every
failure mode in this project is silent, and the tests are what make them loud.

## 0. Why this shape

Two hard constraints decide the entire architecture:

1. **The browser cannot talk to the bridge.** Hue Entertainment is DTLS 1.2 over UDP
   port 2100 with a pre-shared key and exactly one permitted cipher suite,
   `TLS_PSK_WITH_AES_128_GCM_SHA256`. No browser API exposes raw UDP or DTLS-PSK.
   Something native must own the socket.
2. **`getDisplayMedia()` requires a secure context.** `http://lights.lan` is not one.
   `http://127.0.0.1` and `http://localhost` are — the spec grants loopback
   origins secure-context status without TLS.

Constraint 2 is why this is a separate localhost service instead of a page on the
main lights.lan app. We get screen capture with zero certificate work, and the same
process is the natural owner of the DTLS socket from constraint 1. The workaround and
the requirement collapse into one binary.

```
 ┌─ browser tab (http://127.0.0.1:7654) ──────────────┐
 │ getDisplayMedia ──▶ worker ──▶ 64×36 grid           │
 │                                zone averages (N×RGB)│
 └───────────────────────────────────┼────────────────┘
                                      │ WebSocket, binary, ~25 Hz
 ┌─ lightsync (Go, single binary) ────▼───────────────┐
 │ smoothing ▶ gains ▶ gamut ▶ HueStream encoder       │
 │ DTLS/PSK session ─────────────────┼──▶ bridge:2100/udp
 │ CLIP v2 REST (pair, list areas, start/stop)         │
 │ embedded web assets, CLI status readout             │
 └──────────────────────────────────────────────────────┘
```

**Where the work happens.** Smoothing, color correction and gamut mapping live in Go,
not JS. The browser's only job is: capture, reduce, send zone averages. This keeps the
tuning logic in the place that owns the output clock, and means a future non-browser
capture source (X11, PipeWire, HDMI grabber) plugs into the same pipeline unchanged.

**Non-goals for v1:** audio/music sync, multiple simultaneous entertainment areas,
non-Hue outputs (WLED/DDP), remote access from other machines, HDR tone mapping.

## Milestone 1 — Bridge discovery and pairing

**Build:** `internal/hue/discover.go`, `internal/hue/clip.go`, config persistence.

1. Discovery: try mDNS `_hue._tcp.local` first, fall back to the cloud discovery
   endpoint `https://discovery.meethue.com`, and always allow a manual IP entry.
   Manual entry is not a fallback nicety — it is the path that works on segmented
   VLANs, which is exactly where a self-hoster's IoT devices live.
2. Pairing: `POST /api` with `{"devicetype":"lightsync#<hostname>","generateclientkey":true}`.
   Poll every 2 s while the user presses the link button. Give up after 60 s.
3. Persist `{bridge_ip, bridge_id, username, clientkey}` to the config file with
   mode `0600`.

### Tricky parts

- **The bridge serves a self-signed certificate.** You must set
  `InsecureSkipVerify: true` on the HTTPS client, or pin the bridge's certificate
  after first contact. Do not use the system trust store; it will never work.
- **`clientkey` is returned exactly once, at registration.** There is no endpoint to
  read it back. Lose it and the user must re-press the link button and re-register.
  Write the config file *before* returning success to the UI, and fsync it.
- **`clientkey` is a hex string, not raw bytes.** It must be hex-decoded to 16 bytes
  before use as the PSK. Passing the ASCII string is the single most common cause of
  a handshake that fails with no useful error.
- The PSK identity is the `username` (application key) string, verbatim.
- Old bridges (v1, round) do not support Entertainment at all. Check the bridge's
  `/api/config` `modelid` and fail loudly with an explanation rather than timing out
  in the handshake.

**Exit test:** `lightsync pair` writes a config file containing a 32-hex-char
clientkey, and `lightsync areas` lists your entertainment areas by name.

## Milestone 2 — DTLS session and a static color

This is the highest-risk milestone. Do it before writing a single line of capture
code. If it does not work, nothing downstream matters.

**Build:** `internal/hue/stream.go`.

1. `PUT /clip/v2/resource/entertainment_configuration/{id}` with `{"action":"start"}`.
2. `dtls.Dial("udp", bridge:2100, cfg)` with PSK.
3. Encode and send a `HueStream` v2 packet.
4. Run a fixed-rate output loop; send even when nothing changed.
5. On shutdown: send one black frame, then `{"action":"stop"}`, then close.

### Packet layout (v2)

```
offset size  contents
 0      9    "HueStream"                    ASCII
 9      2    0x02 0x00                      protocol version 2.0
 11     1    sequence number                ignored by the bridge, increment anyway
 12     2    0x00 0x00                      reserved
 14     1    color space: 0x00 = RGB, 0x01 = xy+brightness
 15     1    0x00                           reserved
 16     36   entertainment configuration id ASCII UUID, no braces
 52     7×N  per channel: [id][R_hi][R_lo][G_hi][G_lo][B_hi][B_lo]  uint16 big-endian
```

### Tricky parts

- **Extended Master Secret.** Some bridge firmware does not negotiate RFC 7627. If the
  handshake fails, set `ExtendedMasterSecret: dtls.DisableExtendedMasterSecret`. Make
  this a config flag (`hue.disable_ems`) so it can be flipped without a rebuild, and
  try both automatically on first connect, remembering which one worked.
- **You must PUT `start` before the handshake, not after.** The bridge only opens the
  DTLS listener for an area in streaming state.
- **Exactly one streamer per area.** The `active_streamer` field on the configuration
  tells you who holds it. If it is held, surface that in the UI ("in use by the Hue
  Sync app") instead of a generic connection error.
- **Keepalive is mandatory.** Silence for ~10 s and the bridge tears the stream down
  and hands the lights back to their previous state. The output loop must run at a
  fixed rate whether or not the browser is sending anything. When idle, send the last
  frame again.
- **16-bit is a lie.** The API documents 16 bits per component, but the low byte is
  ignored in practice, and passing genuinely different high/low bytes makes lights
  behave erratically. Write each 8-bit value into both bytes: `v<<8 | v`.
- **Rate ceiling.** Treat ~25 packets/second as the hard ceiling and ~20/s as the
  sane default. The bridge is rate-limited at the source; sending faster does not
  produce faster light changes, it produces dropped packets and a stalled feed.
- **MTU.** 10 channels is 70 bytes of payload plus a 52-byte header — comfortably
  inside one datagram. If you ever exceed 20 channels, check you are still under the
  path MTU rather than relying on IP fragmentation over Wi-Fi.
- **Reconnect.** UDP gives you no connection-closed signal. Detect death by watching
  for write errors and by re-polling the configuration's status every few seconds.
  Rebuild the session with exponential backoff, capped at 30 s.

**Exit test:** a `lightsync test` subcommand cycles every channel through red, green,
blue, white for two seconds each, holds the stream for 60 seconds with no traffic
other than keepalive, and the lights do not fall back. If they fall back, your
keepalive or your rate is wrong.

## Milestone 3 — Localhost server and UI shell

**Build:** `internal/server/http.go`, `internal/server/ws.go`, `web/*`, asset embedding.

1. Bind `127.0.0.1` only — never `0.0.0.0`. This service has no authentication
   and controls a WebSocket that drives lights; it must not be reachable from the LAN.
2. Embed `web/` with `//go:embed`. One binary, no asset paths, no install step.
3. Serve the UI, a small JSON API, and a WebSocket endpoint at `/ws`.
4. Open the browser automatically on start (`xdg-open` / `rundll32 url.dll`), and
   print the URL regardless in case that fails.

### Tricky parts

- **Port collisions.** Default to 7654; if it is taken, try the next 10 ports and
  print the one you got. Do not fail to start over a port.
- **Origin checking on the WebSocket.** Reject upgrades whose `Origin` is not the
  loopback origin. Without this, any website the user visits can open a WebSocket to
  your localhost port and drive their lights. This is a real attack, not a theoretical
  one, and it costs four lines.
- **Only one browser tab may drive output.** Accept multiple connections for the UI,
  but designate exactly one as the frame source and reject or demote the rest.
  Two tabs both capturing produces a hard-to-diagnose strobe.

**Exit test:** `lightsync` with no arguments opens a browser tab showing the UI, and
the CLI prints a live status line including the WebSocket client count.

## Milestone 4 — Capture in the browser

**Build:** `web/capture-worker.js`, `web/app.js`.

The whole performance argument: **never let a full-resolution frame reach
JavaScript.** There are three reduction stages, each performed by hardware that is
already doing the work. By the time your code touches pixels, there should be about
two thousand of them, and the per-frame cost should be microseconds.

**Stage 1 — capture-time downscale.** This is the single biggest win.

```js
const stream = await navigator.mediaDevices.getDisplayMedia({
  video: { frameRate: 30, resizeMode: 'crop-and-scale' },
  audio: false,
});
await stream.getVideoTracks()[0].applyConstraints({ width: 320, height: 180 });
```

The compositor scales before the frame is ever handed to the page. Chrome downscales
on bare values like this. Firefox needs `exact` or `max` to force it — apply both
forms and verify with `track.getSettings()` rather than assuming.

**Stage 2 — frames into a worker.** `MediaStreamTrackProcessor` turns the track into a
`ReadableStream<VideoFrame>`. It is push-based, which matters: it keeps delivering when
a timer-driven loop would be throttled.

**Stage 3 — reduce to the sampling grid.**

```js
const bmp = await createImageBitmap(frame, {
  resizeWidth: 64, resizeHeight: 36, resizeQuality: 'low',
});
frame.close();
```

64×36 RGBA is 9,216 bytes. Averaging that with typed arrays is below timer resolution.
There is nothing here for WebAssembly to accelerate — WASM only becomes interesting if
you insist on processing full-resolution frames on the CPU, which is the thing to
avoid.

### Tricky parts

- **`MediaStreamTrackProcessor` has an interop split.** Chrome shipped a
  non-standard main-thread version in 2021; the standardised version is worker-only
  and is what Firefox and Safari implement. Feature-detect both: construct in the
  worker if `self.MediaStreamTrackProcessor` exists, otherwise construct on the main
  thread and transfer the resulting `readable`.
- **Fallback path.** Where neither is available: a hidden `<video>` element plus
  `requestVideoFrameCallback`, drawing into a 64×36 `OffscreenCanvas`. Universal,
  slightly worse, and it does get throttled when the tab is hidden.
- **`frame.close()` on every single frame, including on every error path.** The frame
  pool is small. Leak a handful and capture stops with no error — it simply goes quiet.
  Wrap the body in `try/finally`.
- **Backpressure: drop, never queue.** If a reduction is still in flight when the next
  frame arrives, close the new frame and skip it. A queue turns a transient hiccup into
  permanently growing latency.
- **The user can stop the share from the browser's own UI.** Listen for the track's
  `ended` event and drive the UI back to a stopped state; otherwise the page lies
  about being active.
- **Background tab throttling.** Verify behaviour with the tab backgrounded — this is
  the normal case, since the user is watching something else. If throttling bites,
  the fallbacks in order are: keep the tab visible in a small window, or move capture
  to a native grabber behind the same WebSocket protocol.
- **Multi-monitor.** `getDisplayMedia` captures one surface. Show which one is being
  captured (`track.getSettings().displaySurface`) and offer a "change source" button
  that re-prompts.

**Exit test:** with the tab open and a video playing, the CLI status line shows a
steady inbound frame rate matching the configured capture rate, and browser CPU for
the tab stays in the low single digits. If it does not, one of the three reduction
stages is not actually engaging — check `track.getSettings()` first.

## Milestone 5 — Zone mapping (matching Hue's own model)

**Build:** `internal/pipeline/zones.go`, the calibration UI.

Hue already solved "where is this light relative to the screen", and the answer is in
the entertainment configuration. Read it; do not invent a parallel layout editor.

A `GET /clip/v2/resource/entertainment_configuration/{id}` returns:

- `metadata.name` — the user-facing area name, for the picker.
- `configuration_type` — one of `screen`, `monitor`, `music`, `3dspace`, `other`.
- `channels[]` — each with a `channel_id`, a `position {x,y,z}`, and `members[]`
  referencing light services and segments.
- `locations.service_locations[]` — per-service positions; a segmented device such as
  a gradient lightstrip contributes several positions.
- `status`, `active_streamer`, `stream_proxy`.

**The channel is the unit of control, not the light.** A gradient lightstrip is one
device but several channels, each with its own position, and each must sample a
different part of the screen or you have thrown away the entire point of a gradient
strip. Iterate `channels[]`, never `light_services[]`.

**Position → screen region.** Coordinates are normalised to roughly −1…+1: `x` is
left→right, `y` is the depth axis (toward and away from the screen), `z` is height.
The Hue app's layout screen is what populates these, and the convention users expect
is the one Hue's own sync products implement: a light placed back-left in the room
pulls color from the top-left of the screen.

```
u = (x + 1) / 2       // 0 = screen left,  1 = screen right
v = depth_to_vertical(y) // 0 = screen top, 1 = screen bottom
```

**Do not hard-code the sign of the depth axis.** Community implementations disagree
about which end of `y` is "back", and firmware and app versions have not been
consistent. Ship an `invert_depth` toggle, default it to the mapping above, and make
the calibration UI the thing that settles it in ten seconds rather than a forum
thread.

**Sampling modes**, matching how Hue's own products behave:

| Mode | Behaviour | Fits |
|---|---|---|
| `edges` | Each channel samples a band at the nearest screen edge, width = `edge_width` | Play bars, strips behind a screen; the default for `screen`/`monitor` |
| `quadrant` | Each channel samples a rectangle centred on its mapped position | Lamps spread around a room |
| `global` | Every channel gets the whole-screen average | Bulbs with no meaningful position, and a good "something is wrong" fallback |
| `spread` | Channels are distributed evenly left→right, ignoring positions | `music`, `3dspace`, `other` — where positions are not screen-relative |

Select the default mode from `configuration_type`: `screen`/`monitor` → `edges`,
everything else → `spread`. Let the user override.

Each zone is a rect in normalised screen space with a feather radius. Sample it from
the 64×36 grid with bilinear weighting so that a zone which falls between grid cells
does not snap and judder as content moves.

### Calibration UI — the part that makes this usable

The zone preview is the centrepiece of the interface, not a debug view:

- A live 64×36 thumbnail of the captured screen, drawn in a canvas.
- Each channel's sampling rect outlined on top of it, filled with the color that
  channel is currently being sent.
- Click a rect → the corresponding physical light blinks (`identify` on the light
  service), so the user can confirm the mapping without walking to the wall.
- One "flip depth axis" button. If the outlines are upside down relative to the room,
  the user presses it once and is done.

### Tricky parts

- **Channel ids need not be contiguous or zero-based.** Never assume
  `channel_id == index`.
- **10 channels per packet.** Areas can exceed that; split across packets in the same
  tick if so.
- **Positions can be identical.** Two bulbs placed on top of each other in the app
  produce two channels with the same rect. That is fine — do not deduplicate; the user
  may want that.
- **Fetch the configuration fresh when the picker opens.** Users rearrange areas in
  the Hue app and expect the change to appear.

**Exit test:** with an area selected, every channel's rect is visible in the preview,
clicking one blinks the right physical light, and a bright red object moved across the
screen lights the correct lights in the correct order.

## Milestone 6 — Color pipeline

**Build:** `internal/pipeline/color.go`.

Order matters. Run it exactly like this:

1. **sRGB → linear.** Apply the inverse transfer function to each 8-bit sample
   *before* averaging. Averaging gamma-encoded values is the number one cause of
   output that looks muddy and gray no matter what else you tune. This is the highest
   value-per-line change in the whole project.
2. **Average the zone** in linear light, with the feather weights.
3. **Letterbox rejection** (optional, on by default): detect near-black rows and
   columns on the tiny grid and exclude them, so black bars do not drag every zone
   toward black. A dozen comparisons on a 64×36 grid.
4. **Saturation gain.** Large sampling areas average toward white; a room-scale
   average is almost always less saturated than what the eye reads on screen. This is
   why every mature ambient-light project ships a saturation control.
5. **Brightness gain**, then **black cutoff** — below the threshold, output true
   black rather than a dim flicker, because bulbs are unstable at the bottom of
   their range.
6. **Linear → gamut.** Two options:
   - Color space byte `0x00`: send RGB, the bridge does the conversion. Start here.
   - Color space byte `0x01`: send CIE xy + brightness and clamp into the light's own
     gamut triangle yourself. Better saturated output, more work. Gamut C covers the
     modern color bulbs; the light's gamut is readable from its service resource.
7. **Encode** to `v<<8 | v` per component.

### Tricky parts

- **Per-channel brightness multiplier.** A strip behind a projector screen seen only
  by reflection needs to run hotter than a bar sitting on a sideboard. Hue's own app
  has this; expose it per channel and store it in the config.
- **White-only bulbs** in an area only respond to brightness. Detect them from the
  service's supported color features and drive brightness only, so they do not sit at
  a fixed useless value.
- **Do not apply gamma correction twice.** If you convert to linear at step 1,
  everything downstream is linear until step 6.

## Milestone 7 — Temporal behaviour and "reactivity"

**Build:** `internal/pipeline/smooth.go`.

**Decouple the clocks.** Capture runs at whatever rate the browser delivers.
Output runs on a fixed ticker at `output_hz`. The pipeline holds current state; the
ticker samples it. Never send a packet because a frame arrived.

Reactivity is the user-facing name for the length of the temporal averaging
window. One slider, 0–100, mapped to an exponential moving average time constant:

```
tau_ms = lerp(60, 1200, (100 - reactivity) / 100)
alpha  = 1 - exp(-dt_ms / tau_ms)
```

High reactivity = short window = the lights snap with the content, good for games.
Low reactivity = long window = a slow wash, good for films and for anyone who finds
the snappy version exhausting. Present it with the same four presets Hue uses, plus
the raw slider:

| Preset | Reactivity | Feel |
|---|---|---|
| Subtle | 20 | Slow ambient wash |
| Moderate | 45 | Default |
| High | 70 | Follows cuts |
| Intense | 90 | Games, near-instant |

**Asymmetric smoothing.** Use a shorter time constant when a zone brightens than when
it darkens (roughly 1:2). Matches how the eye handles onset versus decay, and stops
dark scenes from feeling dead.

**Scene-cut bypass.** If the mean absolute change across all zones exceeds a
threshold, snap to the new value instead of easing. Without this, a hard cut from a
dark to a bright scene arrives visibly late.

**Rate limiting.** Cap the per-tick delta. This is what kills strobing on flashing
content, and it is a different mechanism from smoothing — keep both.

**Deadband.** If nothing changed materially, keep sending the previous frame (the
keepalive requires it) but skip recomputation.

### Tricky parts

- **Compute `alpha` from the measured `dt`, not the nominal tick.** A ticker that
  slips turns a fixed alpha into inconsistent smoothing.
- **Do all of this in linear light, before the gamut conversion.** Smoothing
  gamma-encoded values produces visible non-uniform ramps.
- **When the browser stops sending** (tab closed, share ended), do not freeze on the last
  frame forever. Fade to black over ~2 s, then stop the stream and release the area.

## Milestone 8 — Controls and persistence

Everything below is a config field, a UI control, and a WebSocket message. Ship all of
it; each one exists because its absence is a complaint.

**Primary**
- Entertainment area picker — name, type, channel count, and a clear warning when
  `active_streamer` is set.
- Start / stop sync.
- Reactivity (preset buttons + slider).
- Brightness — output scale, 0–200 %.
- Saturation — 0–200 %.

**Sampling**
- Sampling mode (`edges` / `quadrant` / `global` / `spread`).
- Edge width — how far in from the edge the bands sit.
- Invert depth axis.
- Letterbox detection on/off.

**Output shaping**
- Black cutoff threshold.
- Per-channel brightness multipliers.
- Scene-cut sensitivity.

**Advanced** (collapsed by default)
- Capture resolution and frame rate.
- Output Hz.
- Color space: RGB or xy.
- Disable extended master secret.

**Persistence:** a single JSON file next to the config, saved on change with a 500 ms
debounce. Store per-area settings keyed by configuration id, so switching areas
restores that area's tuning rather than bleeding settings across rooms.

## Milestone 9 — CLI readout

The binary is started by a person who wants to see that it works. A single
repainting status block, no scrollback spam:

```
lightsync 0.1.0                                    http://127.0.0.1:7654
 bridge     192.168.1.42        connected           handshake 41 ms
 area       Living room         screen · 5 channels
 stream     active              20.0 Hz out          seq 148213
 capture    1 client            29.7 fps in          320×180 → 64×36
 pipeline   reactivity 45       bright 100%          sat 130%

 ch0 ██ #3a1f0e  ch1 ██ #7c4820  ch2 ██ #12305a
 ch3 ██ #0e2244  ch4 ██ #6b3a1c

 q quit  r reconnect  b blackout
```

Repaint at 4 Hz with ANSI cursor-up, not a full clear, so it does not flicker. Fall
back to plain appended lines when stdout is not a TTY, so logs stay readable under a
service manager. `--verbose` switches to a scrolling log with handshake details and
packet counters. The truecolor swatches are the fastest way to see that the pipeline
is alive and that zone mapping is sane, without opening the browser.

## Milestone 10 — Packaging

- `CGO_ENABLED=0` for static binaries: `lightsync-linux-amd64`, `lightsync-windows-amd64.exe`,
  plus `linux-arm64` since a home server is a plausible host.
- Build with `-ldflags "-s -w -X main.version=..."`.
- Windows: build with `-H=windowsgui` **only** if you drop the CLI readout — otherwise
  keep the console subsystem so the status block is visible when double-clicked.
- Linux: ship a `systemd --user` unit as an example, not as an installer.
- Tag, build in CI, attach binaries to the GitHub release. No installer, no package
  repos; download and run is the whole story.

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
| Capture silently stops after a few seconds | `VideoFrame.close()` not called | `try/finally` around frame handling |
| Latency grows over time | Frames queued instead of dropped | Drop when busy |
| `getDisplayMedia` is undefined | Page not a secure context | Use `127.0.0.1`, not a hostname |
| Everything works, then stops when tab is hidden | Background throttling | Verify with tab backgrounded; keep it visible |

## Latency budget

| Stage | Cost |
|---|---|
| Compositor capture | 16–33 ms |
| Reduce + zone average | 1–3 ms |
| WebSocket over loopback | < 1 ms |
| Smoothing (by design) | 60–1200 ms |
| DTLS + UDP to bridge | ~2 ms |
| Bridge → Zigbee → bulb | 50–100 ms |

Zigbee dominates and is also the main source of jitter. There is no point chasing 60 fps
capture. Keep the default reactivity in a range where the smoothing window is
comparable to the transport delay; anything faster is spent on a bus that cannot
deliver it.
