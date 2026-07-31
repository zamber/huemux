# HueMux

Lightweight, cross-platform frontend for Philips Hue: a Go server you drive
from a browser or an Electron desktop app. Two things in one binary —

- **Screen sync** — captures your screen and streams it to a Hue
  Entertainment area over DTLS, in real time.
- **Light control** — day-to-day control of every room, light, and scene on
  the bridge: on/off, brightness, colour, favorites.

Both talk to the bridge directly; no cloud, no Hue Sync app, no
platform-specific screen-capture hacks. Video sync works natively on
Wayland, X11, Windows, and macOS.

```
browser (127.0.0.1) ──grid──▶ huemux ──DTLS/UDP──▶ Hue bridge ──Zigbee──▶ lights
```

## Why it runs on localhost

`getDisplayMedia()` — the browser screen capture API — only works in a
secure context, and loopback origins are one without needing a certificate.
The same process also owns the DTLS socket to the bridge, which a browser
can never open itself: Hue Entertainment is DTLS 1.2 with a pre-shared key
over UDP, and no browser API exposes that. One binary solves both problems.

## Running

### Browser version

```bash
./huemux
```

Opens `http://127.0.0.1:7654` (prints the exact URL too — some other port if
7654 is taken). If you haven't paired yet, it walks you through that first:
searches for a bridge automatically (falling back to a manual IP field —
useful if your Hue gear is on a separate VLAN), then asks you to press the
bridge's link button. No terminal required for any of it.

Pairing, listing areas, and a DTLS smoke test are also available from the
command line, for scripting or diagnosing a stream that isn't reaching the
bridge:

```bash
./huemux pair 192.168.1.42                        # press the link button when prompted
./huemux areas                                     # list entertainment areas
./huemux test <entertainment-configuration-id>     # prove the DTLS path works before touching any video
```

For screen sync, you need at least one entertainment area, created in the
Hue app under **Settings → Entertainment areas**. Where you place each light
in that layout decides which part of the screen it samples.

### Desktop (Electron) version

Same core, wrapped in a real desktop window — real screen capture with no
picker dialog on X11/Windows/macOS (Wayland needs a one-time OS consent
dialog; see below), and a native app icon/window instead of a browser tab.

```bash
./huemux-desktop             # opens a window
./huemux-desktop --headless  # identical to plain huemux — no Electron, no GUI dependency paid at runtime
```

First non-headless launch downloads Electron (~150MB) into your OS cache
directory — needs internet access once, cached after that.

**Wayland users:** screen capture needs PipeWire + `xdg-desktop-portal`
installed and running (already the case on virtually every modern Wayland
desktop). The first time you start sync you'll see your compositor's own
screen-share consent dialog — that's Wayland's security model, not
something this app can bypass, unlike X11.

### macOS

macOS will ask for **Screen Recording** permission the first time either
version tries to capture your screen (System Settings → Privacy & Security
→ Screen Recording) — grant it to your terminal (browser version) or to
HueMux.app (desktop version), then retry.

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
an x/y-only mapping. The invert checkboxes under Sampling → Zone mapping are
the fix if zones come out upside down or mirrored. Depth doesn't position a
zone at all — it scales how large an area it samples (**Depth size
effect**), on the idea that a light physically further from the screen
suits a bigger, more averaged region better than a precise accent.

Settings are stored per entertainment area, so each room keeps its own tuning.

## Light control

Every room and light on the bridge, grouped by room, each with a
per-room bulk tile (toggle/brightness/colour for the whole room via the
bridge's own grouped-light resource) alongside the individual light cards.
Scenes show real colour-swatch previews and a dynamic-scene indicator, and
are grouped the same way — a room's own scenes sit right under that room's
lights; scenes tied to an entertainment zone rather than a room (most
commonly the same zone screen sync uses) get their own separate section.
Favorites (lights, rooms, scenes, or "all lights" as one unit) get quick
one-tap access without the star buttons that could accidentally undo them
sitting in the way.

Screen sync and light control run over the same connection and can run at
the same time — starting sync in one window/tab doesn't block controlling
lights from another, and vice versa; starting sync a second time explicitly
takes over from wherever it was already running, rather than silently
fighting over who's actually driving the stream.

## Building

```bash
make dev            # local binary: huemux
make dev-desktop     # local binary: huemux-desktop
make dist            # static linux/windows/macOS binaries, both architectures, into dist/
make dist-desktop    # same, for the desktop wrapper
```

One external dependency for the core: `pion/dtls`. The desktop wrapper adds
`go-astilectron`. The WebSocket server, colour pipeline, and UI are all
stdlib and plain JavaScript — no build step for the frontend.

See [PACKAGING.md](PACKAGING.md) for release signing, semver, and
Homebrew/Flatpak/AppImage packaging.

## Layout

```
cmd/huemux/             entry point, subcommands, output loop
cmd/huemux-desktop/     optional Electron shell around the same core
internal/hue/           CLIP v2 REST client, DTLS session, HueStream encoder
internal/pipeline/      zone mapping, colour, temporal smoothing
internal/lightctl/      day-to-day light control: rooms/lights/scenes, favorites
internal/server/        loopback HTTP server, minimal WebSocket
internal/config/        credentials, per-area settings, favorites
internal/ui/            CLI status readout
web/                    plain HTML/CSS/JS, embedded into the binary
web/shared/             theme tokens, i18n, header — shared by every page
web/sync.html           screen-sync UI
web/lights.html         light-control panel
web/app.html            shell that hosts both pages, each in its own iframe,
                        so switching between them doesn't interrupt either
```

## Known issues

See [KNOWN_ISSUES.md](KNOWN_ISSUES.md) — currently one open item (a
Wayland/Electron-specific green preview on first sync), including how to
capture a `-debug` log if you hit it.

## Security

The server binds `127.0.0.1` only and checks the `Origin` header on
WebSocket upgrades. Do not change either: there is no authentication, and a
socket that drives your lights should not be reachable from the rest of the
network.

## Licence

Code: MIT.

The bundled UI font, [Fira Code](https://github.com/tonsky/FiraCode)
(`web/shared/fonts/`), is under a separate license — the
[SIL Open Font License 1.1](web/shared/fonts/LICENSE-OFL.txt), not MIT. The
OFL permits embedding/bundling the font with software under any license
(including MIT) at no cost; it only restricts selling the font *by itself*
and requires renamed derivatives. Bundling it here doesn't relicense it —
the font files remain OFL, the app's own code remains MIT.
