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
./lightsync
```

This opens `http://127.0.0.1:7654`. If you haven't paired yet, it walks you
through that first: it searches for a bridge automatically (falling back to
a manual IP field — useful if your Hue gear lives on a separate VLAN, which
automatic discovery won't cross), then asks you to press the link button.
Once paired, pick an area, press **Start sync**, and choose a screen or
window in the browser's share dialogue. No terminal required for any of it.

Pairing, listing areas and a DTLS smoke test are also available from the
command line, for scripting or for diagnosing a stream that isn't reaching
the bridge:

```bash
./lightsync pair 192.168.1.42                        # press the link button when prompted
./lightsync areas                                     # list entertainment areas
./lightsync test <entertainment-configuration-id>      # prove the DTLS path works before touching any video
```

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
| Ignore black bars | Letterbox detection, so bars do not drag every zone toward black |

### Zone mapping

Which physical axis from the Hue app's room layout (x/y/z) becomes
screen-horizontal, screen-vertical, and "depth" is configurable per area,
each independently invertible. Areas don't agree on which axis carries
useful information — a pair of Play bars either side of a monitor plus a
strip above it has meaningful x (left/right) and z (height) but a y (room
depth) that barely varies, since everything's mounted right at the screen,
which is why the defaults are horizontal=x, vertical=z, depth=y rather than
the x/y-only mapping an early version of this shipped with. **Flip
vertical** is the one-press fix if zones come out upside down; the full axis
selects live under Sampling → Zone mapping. Depth doesn't position a zone at
all — it scales how large an area it samples (**Depth size effect**), on
the idea that a light physically further from the screen suits a bigger,
more averaged region better than a precise accent.

Settings are stored per entertainment area, so each room keeps its own tuning.

## Building

```bash
make dev   # local binary
make dist  # static linux-amd64, linux-arm64, windows-amd64
```

One external dependency: `pion/dtls`. The WebSocket server, colour pipeline
and UI are all stdlib and plain JavaScript, with no build step for the
frontend.

Prebuilt binaries (linux-amd64, linux-arm64, windows-amd64) are hosted at
http://lightsync.lan/ alongside `lights.lan` — download and run, no install
step. `packaging/lightsync.service` is an example systemd `--user` unit for
running it as a persistent background service instead of a terminal session.

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
