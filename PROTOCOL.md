# Protocol reference

Three protocols meet in this program: the Hue Entertainment stream going out
(§1), the browser-to-service link for screen sync (§2), and the day-to-day
light-control feature layered on the same WS connection (§3).

## 1. Hue Entertainment (outbound)

### Transport

- UDP, port 2100, on the bridge.
- DTLS 1.2, pre-shared key.
- Cipher suite: `TLS_PSK_WITH_AES_128_GCM_SHA256` only.
- PSK identity: the `username` (application key) from registration, as ASCII.
- PSK: the `clientkey` from registration, **hex-decoded to 16 bytes**.

### Setup order

This order is not negotiable:

1. `POST /api` with `{"devicetype":"...","generateclientkey":true}` while the
   link button is pressed → `username` + `clientkey`. The clientkey is issued
   once and cannot be read back.
2. `PUT /clip/v2/resource/entertainment_configuration/{id}` with
   `{"action":"start"}`.
3. DTLS handshake. The bridge only accepts one for an area already in streaming
   state.
4. Stream packets at a fixed rate.
5. On exit: fade to black, `{"action":"stop"}`, close the socket.

### Packet format (streaming API v2)

```
offset size field
 0      9   "HueStream"                    ASCII
 9      2   0x02 0x00                      protocol version 2.0
 11     1   sequence number                ignored by the bridge
 12     2   0x00 0x00                      reserved
 14     1   colour space                   0x00 = RGB, 0x01 = xy + brightness
 15     1   0x00                           reserved
 16     36  entertainment configuration id ASCII UUID, no braces
 52     7×N channel records
```

Each channel record:

```
 0  1  channel id
 1  2  R, big-endian uint16
 3  2  G, big-endian uint16
 5  2  B, big-endian uint16
```

Notes that cost time if you learn them the hard way:

- **Components are effectively 8-bit.** The low byte is ignored, and sending
  genuinely different high and low bytes makes lights behave erratically. Write
  `v` into both bytes.
- **Up to 10 channels per packet.** Larger areas need multiple packets per tick.
- **Roughly 25 packets/second is the ceiling, 20 is a sane default.** The bridge
  is rate-limited at the source; faster means dropped packets, not faster light.
- **Silence kills the stream.** About ten seconds without a packet and the
  bridge hands the lights back. Send the previous frame when idle.
- **One streamer per area.** `active_streamer` on the configuration says who
  holds it.

### Entertainment configuration (CLIP v2)

`GET /clip/v2/resource/entertainment_configuration` returns, per area:

| Field | Use |
|---|---|
| `id` | The 36-char UUID that goes in the packet header |
| `metadata.name` | Label for the picker |
| `configuration_type` | `screen`, `monitor`, `music`, `3dspace`, `other` |
| `status` | `active` / `inactive` |
| `active_streamer` | Present when another app holds the stream |
| `channels[]` | `channel_id`, `position {x,y,z}`, `members[]` |
| `locations.service_locations[]` | Per-service positions; several for a segmented strip |

**Iterate `channels[]`, never `light_services[]`.** A gradient lightstrip is one
device contributing several channels, each with its own position. Treating it as
one light throws away the entire point of a gradient strip.

Positions are normalised to roughly −1…+1. `x` runs left→right, `y` is the depth
axis of the room, `z` is height. Convention: a light at the back-left of the room
samples the top-left of the screen. The sign of the depth axis is not reliably
consistent across implementations, which is why `invert_depth` exists.

The bridge presents a **self-signed certificate** on HTTPS. Skip verification or
pin it; the system trust store will never accept it.

## 2. Browser ↔ service (inbound)

WebSocket at `/ws` on the loopback origin. Upgrades from any other origin are
rejected.

### Frames: browser → service (binary)

```
byte 0    0x01           message type: reduced grid
byte 1    width          pixels, typically 64
byte 2    height         pixels, typically 36
byte 3+   width×height×3 RGB, row major, top-left origin
```

At 64×36 that is 6,912 bytes per frame. Only the connection designated as the
frame source is honoured; other tabs may connect for the UI but their frames are
ignored, because two capturing tabs produce a strobe that is very hard to trace
back from the light end.

```
byte 0    0x02           message type: audio frame (music reactivity)
byte 1+   32 FFT magnitude bands then 256 waveform samples, as one flat
          sequence of little-endian float32 (1152 bytes)
```

At ~30 Hz that is ~35 KB/s. Bands are 0..1 magnitude (0 = bass, 31 = treble);
wave samples are −1..1. Audio frames carry no claim handshake: the connection
that most recently sent a valid frame owns the audio source and all other
connections' frames are dropped, so two capturing tabs cannot interleave their
audio into one analysis stream. Unlike grid frames, audio capture needs
neither a paired bridge nor a selected area — the analysis side of the music
engine is entirely Go-side (see `docs/MUSIC-REACTIVITY.md`).

### Control: browser → service (JSON text)

```json
{"type": "select_area", "area_id": "<uuid>"}
{"type": "stop"}
{"type": "settings", "settings": { ... }}
{"type": "identify", "light_rid": "<light resource id>"}
```

`identify` blinks a physical light and is what makes click-to-blink work in the
calibration view.

`{"type": "music_stop"}` clears the music-reactivity audio source (see the
0x02 frame format below). Sent when the page stops mic capture while the WS
connection stays open — without it the status push would keep reporting the
last audio frame as live analysis forever. Any connection may send it, the
same rule as `stop` for the grid stream.

`{"type": "music_preset", "preset": "bass_pulse"}` activates a built-in
music-reactivity preset; `"preset": ""` deactivates it and hands the output
back to screen sync. Only one preset is active at a time (DP-9). While
active, the engine's output loop runs the preset's graph each tick, sampling
the latest 0x02 audio frame, and screen sync is paused — the music block
still carries the analysis frames, and the snapshot's `MusicPreset` field
carries the active preset slug. Built-in slugs: `bass_pulse`,
`chill_ambient`. An area switch re-applies the active preset to the new
area's channel layout.

### State: service → browser (JSON text)

Pushed once per second and on change:

```json
{
  "type": "status",
  "snapshot": { "BridgeState": "...", "Colors": [[r,g,b], ...], ... },
  "zones": [{ "ChannelID": 0, "U0": 0, "V0": 0.2, "U1": 0.15, "V1": 0.5, "LightRID": "..." }],
  "settings": { ... },
  "source_held": true,
  "you_are_source": false
}
```

Zone rects are in normalised screen space: `u` runs 0 (left) to 1 (right), `v`
runs 0 (top) to 1 (bottom). The page draws them over the preview without needing
to know how they were derived.

`source_held`/`you_are_source` answer "is anyone streaming, and is it me?" —
computed per connection (unlike every other field here, which is identical
for every recipient), since only one WS connection is ever the frame source
(`internal/server/http.go`'s `frameSource`). Every other connected client's
captured frames are silently dropped, which without this is invisible: a
second tab or the desktop app both keep showing their own local capture
preview with no indication that it's not actually reaching the bridge.

While a browser is capturing audio (music reactivity, Phase 1), the status
push also carries the latest analysis frame so the UI and the Go-side
analysis primitives can see exactly what the engine received:

```json
"music": { "active": true, "frames": 87, "fft": [ ...32 floats, 0-1... ], "wave": [ ...256 floats... ] }
```

`fft` is magnitude per band, 0 = bass, 31 = treble; `wave` is the downsampled
time-domain signal. The block is absent while no audio source is connected.
The browser already has its own copy of this data — the arrays exist to prove
the pipe and to feed analysis, not to power the preview.

The frame source is claimed explicitly by `select_area` (`claimFrameSource`),
not by whichever connection happens to send a grid frame first — starting
sync from a second client preempts the first rather than just having its
frames silently dropped from then on. `stop` releases the frame source too,
from *any* connection, not just the one holding it — sent from the Lights
page this is how you get manual light control back without switching to
Sync and stopping it there, mirroring the real Hue app's own "turn off
entertainment sync to control lights directly" behavior.

Whenever a connection stops being the frame source for a reason *other than
its own explicit stop* — preempted by another client's `select_area`, or
told to stop by a `stop` sent from somewhere else — it receives:

```json
{"type": "stream_stopped"}
```

so its UI can stop local capture and reset the preview instead of leaving
it frozen on the last frame, which otherwise looks exactly like it's still
streaming when it no longer is.

### HTTP

| Route | Purpose |
|---|---|
| `GET /` | Embedded UI |
| `GET /api/areas` | Entertainment areas, fetched fresh from the bridge |
| `GET /api/status` | Current screen-sync snapshot |
| `GET /api/lights` | Every light, fresh from the bridge (`internal/lightctl.Light`) |
| `GET /api/rooms` | Every room, fresh from the bridge (`internal/lightctl.Room`) |
| `GET /api/scenes` | Every scene, fresh from the bridge (`internal/lightctl.Scene`) |
| `GET /api/favorites` | Every favourited id (light, `room:<id>`, scene, or the synthetic `all`), id → unix-seconds favourited-at |
| `GET /api/locale` | `{"lang": "pl"}` (or `""`) — locale hint from this *process's* environment, preferred over the browser's own `navigator.language` since Electron's often doesn't reflect the host OS locale |
| `GET /ws` | WebSocket upgrade |

## 3. Light control (day-to-day Hue control, over the same `/ws`)

Mutations and live updates for the light-control panel share the same `/ws`
connection as everything else in PROTOCOL.md §2 — a second message family on
the same socket, not a second transport. Rationale: the connection is already
origin-checked and already has a push loop; a second SSE endpoint (the
approach lights-ui uses, connecting directly to the bridge's own
`/eventstream/clip/v2`) would just be more to keep alive for no benefit.

### Control: browser → service (JSON text)

```json
{"type": "light_toggle",     "rid": "<light id>",         "on": true}
{"type": "light_brightness", "rid": "<light id>",         "brightness": 42}
{"type": "light_color",      "rid": "<light id>",         "r": 255, "g": 128, "b": 0}
{"type": "light_favorite",   "rid": "<light id or room:<room id>>"}
{"type": "room_toggle",      "rid": "<grouped_light id>", "on": true}
{"type": "room_brightness",  "rid": "<grouped_light id>", "brightness": 42}
{"type": "scene_recall",     "rid": "<scene id>"}
```

`light_color`'s `rid` always addresses an individual light — CLIP v2 has no
room/zone-level color PUT. `r`/`g`/`b` (0-255) are converted to CIE xy
chromaticity server-side (`internal/lightctl/color.go`) before the PUT, since
CLIP v2's `color` resource only ever accepts `xy`, never RGB or HSV directly.
`room_toggle`/`room_brightness` target a room or zone's `grouped_light`
resource id specifically (`Room.GroupedLightID` in the `/api/rooms` response)
— not the room's own id, which only names the group.

### State: service → browser (JSON text)

Pushed the instant an eventstream event arrives — event-driven, not the 1Hz
poll `status` uses, since that's what gets this feature SSE-like
responsiveness without a second transport:

```json
{
  "type": "light_event",
  "event": {
    "type": "light",
    "id": "<light or grouped_light id>",
    "on": true,
    "brightness": 42.0,
    "x": 0.45,
    "y": 0.41
  }
}
```

Every field on `event` except `type`/`id` is a pointer server-side and only
present when that field actually changed in this update — the bridge's
eventstream sends partial resource deltas, never full resources, so treat a
missing field as "unchanged," never as "reset to zero."

`light_favorite` has no eventstream counterpart — favorites are local state
(`internal/config/favorites.go`), not a bridge resource — so the new state is
pushed explicitly, broadcast to every connected tab (not just the one that
sent the toggle, so multiple open windows stay in sync):

```json
{"type": "favorite_event", "id": "<light id or room:<room id>>", "favorite": true}
```
