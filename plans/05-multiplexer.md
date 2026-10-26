# 05 — Multiplexer (Mux): remixing effects

Status: Phase 1 implemented in this branch.
Supersedes the UI and runtime assumptions in
[docs/MUSIC-REACTIVITY.md](../docs/MUSIC-REACTIVITY.md); the primitive
catalogue and the "the preset is the graph" decision there are the starting
point, not a separate feature. Related market notes are in
[POSSIBLE-FUTURE.md](../POSSIBLE-FUTURE.md).

## Goal

A UI for *remixing* light effects: stack a few lanes, give each one a signal
source and an effect, blend them, and watch the result on the Salon lamps.
Sources should be interchangeable — a timer, the microphone, a video area, a
constant — so the same effect can be driven by anything.

The old node editor answered "can we represent a DAG?" It did not answer "can
a person build something on a phone in under a minute?" This plan separates
the two questions. The DAG remains the execution model; the UI is a lane
mixer, not a canvas.

## Why the old graph UI failed

- The canvas is the only editing surface. Node drag, port drag and pan all
  compete for the same touch gesture, and the palette and inspector are
  off-canvas drawers that hide the thing being edited.
- `/api/areas` only exists under the `full`/`sync` profile. Under `lights`
  the page asks for an area it cannot have, then renders an empty canvas
  with no explanation.
- The empty state is a blank grid. Nothing says what to do next.
- Loading the page also loads `music.js`, whose DOM assumptions do not hold
  on the preset page, and the resulting exception aborts later setup.
- The graph model leaks into the UI: every primitive is a box with typed
  ports, so the user has to understand the whole plumbing before hearing a
  single effect.

The replacement keeps the primitive model but presents a small number of
*layers* whose wiring is implicit: `source → effect → blend → lights`.
Advanced graph editing can return later as a second view over the same
document; it is not the on-ramp.

## Layers

The important part is what each layer is allowed to know.

```
web/mux.js ──HTTP──▶ internal/server/mux_api.go ──▶ internal/muxrun
                                                        │
                                             internal/mux (pure)
                                                        │
                                                  mux.Sink interface
                                                        │
                                             internal/muxhue adapter
                                                        │
                                             internal/lightctl ──▶ bridge
```

- `internal/mux` — pure multiplexer. Colors, sources, effects, lanes, blend,
  document parse/validate, catalog metadata. No HTTP, no timers, no Hue, no
  `time.Now()` inside `Tick` (time is an input). Every primitive is
  deterministic given `(now, inputs, params)`, which is what makes it
  testable without a bridge.
- `internal/muxrun` — runtime. Owns the preview clock, the output clock, the
  latest snapshot, the start/stop state, and a `mux.Sink`. Providers for
  audio and video are injected functions, not imports.
- `internal/muxhue` — the only place that knows how a `mux.Output` becomes a
  Hue request. It talks to `lightctl`, never to `internal/hue`.
- `internal/server` — transport only: JSON in, JSON out, auth, lifecycle.
- `web/mux.*` — UI only: it composes a document and reads a preview
  snapshot. It never computes an effect.

This is deliberately more packages than a single `internal/mux` would be.
The payoff is that `go test ./internal/mux/...` runs with no bridge, no
network and no clock, and a future Go or non-Go client can drive the same
document through the same API.

## Document

```json
{
  "version": 1,
  "name": "Salon warm pulse",
  "room": "Salon",
  "lights": ["<rid>", "..."],
  "master": { "brightness": 0.9 },
  "lanes": [
    {
      "id": "lane-1",
      "name": "Warm pulse",
      "enabled": true,
      "source": { "type": "timer", "params": { "bpm": 48, "waveform": "sine" } },
      "effect": { "type": "pulse", "params": { "color": "#FF7A18", "min": 0.05, "max": 1.0 } },
      "blend": "replace",
      "level": 1.0,
      "targets": []
    }
  ],
  "output": { "hz": 4, "transition_ms": 220 }
}
```

`targets: []` means every light in `lights`. `lights: []` means "resolve the
document's `room` at start time". A document is valid without a running
output: the editor previews it.

The document is the preset format. It is intentionally flat enough that a
person can read it, but each lane is a `source`/`effect` pair, so the same
JSON can be lowered to a DAG later without changing saved files.

## Primitive model

A source produces named scalars plus, for video, per-light colors:

```go
type SourceOutput struct {
    Values map[string]float64
    Colors map[string]Color
}
```

An effect receives one source output and the target light list, and writes
premultiplied linear RGB per light:

```go
type Pixel struct{ R, G, B float64 } // 0..1, already scaled by brightness
```

`Pixel` rather than `(color, brightness)` keeps blending honest: two lanes
at 40% brightness add to 80%, they do not compete over a separate brightness
channel. The output conversion takes `max(r,g,b)` as brightness and the
normalized channels as the color.

### Sources (Phase 1)

| Type | Values | Notes |
|---|---|---|
| `timer` | `value`, `phase`, `beat` | BPM + waveform, free-running. The default. |
| `sound` | `value`, `bass`, `mid`, `treble`, `energy`, `beat` | Reads the server's existing 32-band frame. Zero when no capture. |
| `video` | `value`, `r`, `g`, `b`, per-light colors | Reads the current grid colors when screen sync is feeding them. |
| `constant` | `value` | Manual fader; useful for testing a chain. |
| `random` | `value` | Seeded hold-and-step; deterministic for a given seed. |

### Effects (Phase 1)

| Type | What it does |
|---|---|
| `solid` | One color; optional source-driven brightness. |
| `pulse` | Source value → brightness through a curve. |
| `gradient` | Two-color gradient across X, Y, index or radius. |
| `chase` | A moving head with width/trail, index order. |
| `rainbow` | Rotating hue ramp across the lights. |
| `sparkle` | Per-light hashed twinkle, density/decay. |
| `video_map` | Passes the video source's per-light colors through. |

Every effect has `color`/palette params where that makes sense, and a
`source` param naming which scalar drives it (default `value`). That is the
composition seam: any source can drive any effect.

### Blends

`replace`, `add`, `max`, `screen`, `multiply`, `over`. The first enabled
lane defaults to `replace`; the UI defaults later lanes to `add`.

## Runtime

- Preview clock: 12 Hz while anyone is looking. It renders into an atomic
  snapshot and never touches the bridge.
- Output clock: `output.hz` (default 4 Hz, clamped 1–10). Only runs when the
  mux is started. The sink is called from one worker and a newer frame
  replaces an unsent one, so a slow bridge cannot back up the clock.
- Audio provider: `music.State.Snapshot` already lives in the server. The
  mux runtime does not own capture.
- Video provider: supplied by the engine when one exists. Under a
  lights-only profile it reports inactive; the `video` source then reads
  zero and `video_map` emits nothing. The UI says so instead of pretending.
- Lights: resolved once from `room` (`Salon`) through `lightctl` and cached
  in the runtime. `lights` in the document can pin a subset.

REST output is the Phase 1 sink. It sends one combined `on` + `dimming` +
`color.xy` PUT per changed light, with a transition, and skips lights whose
quantized value did not change. DTLS output (the existing entertainment
stream path) is Phase 2; it is the only way to get genuinely high-rate
effects without hammering the bridge.

## UI

Mobile first, and not a canvas:

- A sticky preview strip of the Salon lamps, drawn as circles at their
  normalized positions, with live colors. The preview is the feedback loop;
  it is always visible while editing.
- One card per lane. Each card has an enable switch, source select, effect
  select, the three most useful effect params, a color swatch, a blend
  select and a level slider. The rest of the params sit behind "More".
- Reordering is explicit up/down buttons, not a drag-only affordance.
  Desktop may add drag later; touch does not depend on it.
- Adding a lane opens a bottom sheet with two choices (source, effect) and
  sensible defaults, then closes. No empty node needs to be positioned.
- Presets are a sheet, not a route: load replaces the document, save asks
  for a name. Built-ins are read-only.
- Start/Stop is one prominent toggle. Editing while stopped still animates
  the preview.

The page is a normal shell tab (`Mux`), available under every profile that
has `lightctl` (which is all of them). It does not need an entertainment
area, an area selection, or the sync engine.

## Icon

The old mark is six gradients with a per-strand white fade: legible at
512 px, mush at the 22 px header size. The new mark is the same idea reduced
to what survives: three colored inputs (red, green, blue) converging into
one white output dot on a rounded square. No gradients, no per-strand
taper. The light-background and bare-mark variants use a near-black output
instead of white. `scripts/gen-icon.py` remains the single source of truth
and also emits the Android vector and the app-image PNG.

## Phases

1. **This branch.** Core + seven effects + five sources + lane mixer +
   runtime + REST sink + API + mobile UI + icon + unit tests. Salon only by
   default.
2. **Streaming output.** A `mux.Sink` over `internal/hue.Stream` for the
   `Living room all` area, with channel mapping from the area config.
   Removes the REST rate ceiling and enables strobe/chase at full rate.
3. **Video in the mux page.** Capture a screen region in the mux page and
   send reduced frames on the existing WS, independent of the sync engine.
4. **Graph view.** An optional desktop DAG editor over the same document
   (lower lanes to a graph, or edit the graph directly).
5. **Preset sharing.** Export/import and a small gallery.

## Verification

- `go test ./internal/mux/...` covers color math, every primitive's
  determinism, blend behavior, document validation, and the runtime's
  start/stop and latest-frame semantics with a fake sink.
- `go test ./internal/server/...` covers the API with a fake runtime.
- A browser pass at 390×844 and 1280×720: add a lane, change source and
  effect, reorder, save, load, start/stop, no console errors.
- One live pass against the Salon lamps, checking that only Salon light RIDs
  are addressed and that Stop leaves them in a sane state.
