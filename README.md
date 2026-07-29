# lightsync

Screen colour sync for Philips Hue Entertainment areas. A single binary that
serves a small web UI on `127.0.0.1`, captures your screen through the
browser, and streams the result to your Hue bridge over DTLS.

Part of the `lights.lan` self-hosted lighting setup.

```
browser (127.0.0.1) ──grid──▶ lightsync ──DTLS/UDP──▶ Hue bridge ──Zigbee──▶ lights
```

## Why it runs on localhost

`getDisplayMedia()` — the browser screen capture API — only works in a
secure context. A LAN hostname like `http://lights.lan` is not one, so it
would need a real certificate. Loopback origins **are** secure contexts
without TLS, so serving the page from `http://127.0.0.1` sidesteps
certificates entirely.

The same process then owns the DTLS socket to the bridge, which the browser
could never open itself: Hue Entertainment is DTLS 1.2 with a pre-shared key
over UDP, and no browser API exposes that. One binary solves both problems.

## Quick start

```bash
# 1. pair (press the link button on the bridge when prompted)
./lightsync pair 192.168.1.42

# 2. check it sees your areas
./lightsync areas

# 3. prove the DTLS path works before touching any video
./lightsync test <entertainment-configuration-id>

# 4. run it
./lightsync
```

Step 4 opens `http://127.0.0.1:7654`. Pick an area, press **Start sync**, and
choose a screen or window in the browser's share dialogue.

You need at least one entertainment area, created in the Hue app under
**Settings → Entertainment areas**. Where you place each light in that layout
is what decides which part of the screen it samples, so it is worth getting
roughly right.

## Controls

| Control | What it does |
|---|---|
| Reactivity | Length of the averaging window. Subtle → slow wash, Intense → snaps with the content |
| Brightness | Output scale, 0–200 % |
| Saturation | A room-scale average drifts toward white; above 100 % pulls it back |
| Black cutoff | Below this, lights go fully off instead of flickering at the bottom of their range |
| Mode | `edges` / `quadrant` / `global` / `spread` — see ROADMAP |
| Edge width | How far in from the screen edge the sampled bands sit |
| Flip depth axis | One press if the zones come out upside down relative to your room |
| Ignore black bars | Letterbox detection, so bars do not drag every zone toward black |

Settings are stored per entertainment area, so each room keeps its own tuning.

## Building

```bash
make dev   # local binary
make dist  # static linux-amd64, linux-arm64, windows-amd64
```

One external dependency: `pion/dtls`. The WebSocket server, colour pipeline
and UI are all stdlib and plain JavaScript, with no build step for the
frontend.

## Layout

```
cmd/lightsync/     entry point, subcommands, output loop
internal/hue/       CLIP v2 REST client, DTLS session, HueStream encoder
internal/pipeline/  zone mapping, colour, temporal smoothing
internal/server/    loopback HTTP server, minimal WebSocket
internal/config/    credentials and per-area settings
internal/ui/        CLI status readout
web/                plain HTML/CSS/JS, embedded into the binary
```

## Security

The server binds `127.0.0.1` only and checks the `Origin` header on
WebSocket upgrades. Do not change either: there is no authentication, and a
socket that drives your lights should not be reachable from the rest of the
network.

## Licence

MIT.
