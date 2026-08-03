# Music Reactivity — Feature Roadmap

A modular, composable audio-reactive light engine built into HueMux. The
goal: let users build presets by chaining analysis, routing, and effect
primitives — a "multiplexer" that turns sound into light behavior through
pluggable signal chains.

This document is the plan. It defines the primitives, the UI, the phases, and
the extension points. Implementation decisions are documented with their
rationale under [Decision Points](#decision-points).

See [POSSIBLE-FUTURE.md](../POSSIBLE-FUTURE.md) for market research on why
these features were chosen and what existing clients fail to deliver.

---

## Architecture overview

```
┌─ Audio Source ─────────────────────────────────────────────────────────┐
│                                                                         │
│  ┌───────────┐   ┌──────────────┐   ┌─────────────────┐               │
│  │ Microphone │   │ getDisplay-  │   │ System audio    │  (future)     │
│  │ (existing) │   │ Media(audio) │   │ loopback/pipe   │               │
│  └─────┬─────┘   └──────┬───────┘   └────────┬────────┘               │
│        │                │                     │                         │
│        └────────────────┼─────────────────────┘                         │
│                         │ Web Audio API                                 │
│                         ▼ AudioContext.createAnalyser()                 │
│                                                                         │
└─────────────────────────┬───────────────────────────────────────────────┘
                          │ time-domain samples, frequency bins
                          ▼
┌─ Analysis Layer (Go, via WebSocket from browser) ──────────────────────┐
│                                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐ │
│  │ BeatDetector │  │ FreqBands    │  │ Loudness     │  │ Spectral-  │ │
│  │ onset, BPM   │  │ bass/mid/trb │  │ RMS, peak    │  │ Flux       │ │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └─────┬──────┘ │
│         │                 │                 │                 │        │
│         └─────────────────┼─────────────────┼─────────────────┘        │
│                           │ feature values (0-1, Hz, bool)             │
│                           ▼                                             │
└───────────────────────────┬─────────────────────────────────────────────┘
                            │
                            ▼
┌─ Routing Layer ────────────────────────────────────────────────────────┐
│                                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐ │
│  │ LightSelector│  │ ZoneMapper   │  │ CyclePattern │  │ Threshold- │ │
│  │ which lights │  │ spatial map  │  │ seq/rand/p-p │  │ Gate       │ │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └─────┬──────┘ │
│         │                 │                 │                 │        │
│         └─────────────────┼─────────────────┼─────────────────┘        │
│                           │ (light_id, feature_vector) pairs            │
│                           ▼                                             │
└───────────────────────────┬─────────────────────────────────────────────┘
                            │
                            ▼
┌─ Effect Layer ─────────────────────────────────────────────────────────┐
│                                                                         │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐  ┌────────────┐ │
│  │ ColorMapper  │  │ Brightness-  │  │ StrobePattern│  │ ChaseEffect│ │
│  │              │  │ Curve        │  │              │  │            │ │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘  └─────┬──────┘ │
│         │                 │                 │                 │        │
│         └─────────────────┼─────────────────┼─────────────────┘        │
│                           │ color vector (R,G,B per channel)            │
│                           ▼                                             │
└───────────────────────────┬─────────────────────────────────────────────┘
                            │
                            ▼
                    ┌───────────────┐
                    │ HueStream v2  │──▶ bridge:2100/udp
                    │ encoder (extg)│
                    └───────────────┘
```

**Key design choice:** Analysis runs in the browser (Web Audio API is native
and fast), but all routing, effect computation, and modulation run in Go
alongside the existing output pipeline. The browser sends FFT bins and raw
samples at ~30-60 Hz over the existing WebSocket. Go applies the preset graph
and feeds the stream encoder. This keeps the output clock in Go where it
belongs, and reuses the same smoothing/gamut/encoder already built.

---

## Primitive types

Each primitive is a self-contained module with typed inputs, typed outputs,
and serialisable parameters. New primitives are registered in a catalog;
presets are graphs of catalog entries.

### 1. Audio Source Primitives

| Name | Input | Output | Parameters |
|---|---|---|---|
| `mic_capture` | `getUserMedia({audio})` | 2048-sample f32 buffer, 32 FFT bins | device_id, sample_rate |
| `display_audio` | `getDisplayMedia({audio})` | same | — |
| `loopback` (future) | system audio device | same | device_name |

The browser sends two arrays every frame:
- `fft`: `float32[32]` — magnitude per frequency band (0 = bass, 31 = treble)
- `wave`: `float32[256]` — downsampled time-domain waveform

### 2. Analysis Primitives

| Name | Input | Output | Parameters |
|---|---|---|---|
| `beat_detector` | `fft` array | `beat: bool, bpm: float, confidence: 0-1` | `threshold` (1.1-2.0), `min_interval_ms` (100-600) |
| `freq_bands` | `fft` array | `bass: 0-1, mid: 0-1, treble: 0-1` | `bass_cutoff` (default 200Hz), `treble_cutoff` (default 4000Hz) |
| `loudness` | `fft` array | `rms: 0-1, peak: 0-1, dynamic_range: 0-1` | `window_ms` (smoothing) |
| `spectral_flux` | `fft` array | `flux: 0-1` (how fast spectrum changes) | `window_ms` |
| `onset_detector` | `fft` array | `onset: bool, strength: 0-1` | `threshold`, `min_interval_ms` |
| `silence_detector` | `fft` array | `silent: bool` | `threshold_db` |

#### Beat detection algorithm

```
1. Compute short-term energy over current frame:
     E_short = mean(|fft|²)

2. Maintain a ring buffer of ~43 frames (~1s history).
     E_long = mean(ring_buffer)

3. Compute variance over the buffer.
     C = var(ring_buffer)  // "how varied is the energy right now"

4. Beat condition:
     beat = (E_short > C * threshold) AND
            (time_since_last_beat > min_interval_ms)

5. BPM estimation:
     Collect last N inter-beat intervals.
     BPM = 60 / median(intervals)
     Weight by recency (exponentially decaying).
```

This is the same approach used by LedFx and Music Assistant. It's not
perfect on complex material (jazz, classical) but accurate on
pop/electronic/rock — the genres people actually play at parties.

### 3. Routing Primitives

| Name | Input | Output | Parameters |
|---|---|---|---|
| `all_lights` | — | `[light_id]` for every light in area | — |
| `light_group` | — | `[light_id]` for selected lights | `light_ids: [string]` |
| `zone_split` | — | groups of lights by spatial zone | `axis: x|y|z`, `split_count: int` |
| `cycle_sequential` | `trigger: bool` | `[light_id]` changing on each trigger | `direction: fwd|rev|pingpong` |
| `cycle_random` | `trigger: bool` | `[light_id]` random on each trigger | `avoid_repeat: bool` |
| `threshold_gate` | `value: 0-1` | same light_ids, or empty | `min: 0-1`, `max: 0-1`, `hysteresis: 0-1` |
| `split_by_frequency` | `bass,mid,treble` | each frequency band → different light group | `bass_ids, mid_ids, treble_ids` |

### 4. Effect Primitives

| Name | Input | Output | Parameters |
|---|---|---|---|
| `color_map_energy` | `energy: 0-1` per light | R,G,B per light | `palette: [color]`, `hue_shift: 0-360` |
| `color_map_frequency` | `bass,mid,treble` | R,G,B per light | bass/mid/treble → color assignments |
| `brightness_energy` | `energy: 0-1` | brightness 0-255 | `curve: lin|log|exp`, `min: 0-1`, `max: 0-1` |
| `strobe_beat` | `beat: bool` | R,G,B pulse on beat | `color`, `duration_ms`, `decay: exp|lin` |
| `chase_trigger` | `trigger: bool` | advancing light activation | `speed: s`, `width: 1-N lights`, `trail_decay` |
| `pulse_energy` | `energy: 0-1` | brightness expand/contract | `center_light`, `radius`, `decay` |
| `wave_frequency` | `bass,mid,treble` | color gradient across lights | `waveform: sin|tri|sqr`, `speed` |
| `static_scene` | — | fixed R,G,B per light (fallback) | `scene: {light_id: color}` |

### 5. Modulation Primitives

| Name | Input | Output | Parameters |
|---|---|---|---|
| `adsr_envelope` | `trigger: bool` | `0-1` envelope value | `attack_ms, decay_ms, sustain, release_ms` |
| `lfo` | — | `0-1` cyclic value | `waveform, frequency, phase_offset` |
| `smoother` | any `0-1` | smoothed `0-1` | `attack_ms, release_ms` (asymmetric) |
| `randomizer` | `trigger: bool` | random `0-1` | `range: min-max`, `distribution: uni|norm` |

### Future primitive types (Phase 4+)

- `spatial_panner` — virtual 3D sound source position mapped to nearest lights
- `track_separator` — isolate vocals/drums/bass (needs stem separation, heavy)
- `genre_detector` — ML classifier switching presets based on music style
- `lyric_sync` — timing events from streaming service metadata
- `midi_cc_input` — map MIDI control change messages to any parameter

---

## Preset format

A preset is a JSON document describing a DAG of primitive instances:

```json
{
  "version": 1,
  "name": "Bass Pulse",
  "description": "Bass drives brightness, beat triggers strobe on left lights",
  "nodes": [
    {
      "id": "src",
      "type": "mic_capture",
      "params": {}
    },
    {
      "id": "beat",
      "type": "beat_detector",
      "params": { "threshold": 1.3, "min_interval_ms": 200 }
    },
    {
      "id": "bands",
      "type": "freq_bands",
      "params": { "bass_cutoff": 200, "treble_cutoff": 4000 }
    },
    {
      "id": "bass_lights",
      "type": "light_group",
      "params": { "light_ids": ["dc5bc7fd-...", "4ba90295-..."] }
    },
    {
      "id": "left_lights",
      "type": "light_group",
      "params": { "light_ids": ["c1a73134-..."] }
    },
    {
      "id": "bass_brightness",
      "type": "brightness_energy",
      "params": { "curve": "exp", "min": 0.1, "max": 1.0 }
    },
    {
      "id": "beat_strobe",
      "type": "strobe_beat",
      "params": { "color": "#FF6600", "duration_ms": 150 }
    }
  ],
  "edges": [
    { "from": "src", "to": "beat" },
    { "from": "src", "to": "bands" },
    { "from": "bands", "out_port": "bass", "to": "bass_brightness", "in_port": "energy" },
    { "from": "bass_brightness", "to": "bass_lights" },
    { "from": "beat", "out_port": "beat", "to": "beat_strobe", "in_port": "beat" },
    { "from": "beat_strobe", "to": "left_lights" }
  ]
}
```

**Why JSON:** debuggable in browser devtools, human-readable, trivially
shareable as a file or URL. The preset is the graph. No hidden state.

**Versioned:** The `version` field lets us evolve the format without
breaking saved presets. A migration function upgrades old presets on load.

---

## UI Design

### Desktop / tablet (≥768px width) — Node Editor

```
┌──────────────────────────────────────────────────────────────┐
│  Presets: [Bass Pulse ▼] [+New] [Save] [Export] [Import]    │
├──────────────────────────────────────────────────────────────┤
│                    ┌──────────────┐                          │
│  ┌────────┐       │ beat_detector│       ┌──────────────┐  │
│  │ mic    │──────▶│ threshold:1.3│──────▶│ strobe_beat  │  │
│  │_capture│       │ min_int:200  │       │ color:#F60   │  │
│  └────────┘       └──────┬───────┘       │ dur:150ms    │  │
│                          │               └──────┬───────┘  │
│                          │                      │           │
│                    ┌─────▼──────┐         ┌─────▼───────┐  │
│                    │freq_bands  │         │ left_lights │  │
│                    │bass→ mid→  │         │ c1a73134... │  │
│                    │      treble│         └─────────────┘  │
│                    └─────┬──────┘                          │
│                          │ bass                             │
│                    ┌─────▼──────┐                          │
│                    │brightness_ │                          │
│                    │energy      │                          │
│                    │curve:exp   │                          │
│                    └─────┬──────┘                          │
│                          │                                  │
│                    ┌─────▼──────┐                          │
│                    │bass_lights │                          │
│                    │dc5b,4ba9...│                          │
│                    └────────────┘                          │
│                                                              │
├──────────────────────────────────────────────────────────────┤
│  ◀ Preview ──────────────────────────────────────────────▶  │
│  ┌─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─●─┐  │
│  │ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○   │  │
│  │ ○ ○ ○ ● ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○   │  │
│  │ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○ ○   │  │
│  └──────────────────────────────────────────────────────┘  │
│  ▁▂▃▄▅▆▇█▇▆▅▄▃▂▁  BPM: 128 ●  Bass: ████████░ 72%       │
└──────────────────────────────────────────────────────────────┘
```

Features:
- Drag nodes from a palette on the left
- Connect output ports to input ports by dragging
- Click a node to show its parameter inspector in the sidebar
- Preview pane shows live light activity + audio waveform
- Nodes color-code: source=blue, analysis=green, routing=yellow, effect=red, modulation=purple

### Phone (<768px width) — Card Browser

```
┌─────────────────────┐
│  Music Sync         │
├─────────────────────┤
│                     │
│  ┌───────────────┐  │
│  │ Bass Pulse    │  │  ◀ swipe between presets
│  │ ████████████  │  │
│  │ ● Beat ●●●●● │  │  ◀ mini visualization
│  │ [ACTIVE]      │  │
│  └───────────────┘  │
│                     │
│  ┌───────────────┐  │
│  │ Chill Ambient │  │
│  │ ░░░░░░░░░░░░  │  │
│  │ ░░ Beat ░░░░ │  │
│  │ [Tap to start]│  │
│  └───────────────┘  │
│                     │
│  ┌───────────────┐  │
│  │ Party Mode    │  │
│  │ ▓▓▓▓▓▓▓▓▓▓▓▓  │  │
│  │ ● Beat ●●●●● │  │
│  │ [Tap to start]│  │
│  └───────────────┘  │
│                     │
├─────────────────────┤
│  [◀◀] [▶▶] [⚙ Tweak] │  ◀ transport controls
└─────────────────────┘
```

Features:
- Cards with large touch targets (≥44px height)
- Each card shows a live mini-visualization of what the preset does
- Swipe left/right to switch presets
- Tap a card to activate
- "Tweak" button opens a bottom sheet with the most important parameters
  (brightness range, color palette, beat sensitivity)

### Tweak panel (phone bottom sheet)

```
┌─────────────────────┐
│  Bass Pulse    [✕]  │
├─────────────────────┤
│  Brightness         │
│  [────────●────] 75%│
│                     │
│  Color Palette      │
│  [Fire ▾]           │
│                     │
│  Beat Sensitivity   │
│  [─────●───────] 1.3│
│                     │
│  Strobe Speed       │
│  [────────●────] 150│
│                     │
│  Active Lights      │
│  ☑ Lampka salon     │
│  ☑ Lampa ciemniejsza│
│  ☐ Ekran góra       │
│  ☐ Ekran prawo      │
│  ☐ Ekran lewo       │
└─────────────────────┘
```

The tweak panel exposes the "top 5" most useful parameters derived from
the preset graph. Full editing needs the desktop node editor.

### Interactive visualization mode

When enabled (toggle in header), the sync page shows:

1. **Light grid overlay** — each light's position rendered as a circle,
   intensity = current brightness, color = current color. Activation
   trails show the last ~2 seconds of changes as fading arcs.

2. **Frequency spectrum** — live FFT bars below the light grid, colored
   by frequency band (red=bass, green=mid, blue=treble).

3. **Beat markers** — vertical lines or pulse rings that appear on beat
   detection, showing which lights responded.

4. **Per-light inspector** — tap a light circle to see which primitives
   are currently driving it and with what values.

5. **Graph overlay** — toggle to overlay the preset graph on the light
   grid, showing data flow as animated particles traveling along edges.

---

## Phase plan

### Phase 1 — Audio Capture & Basic Analysis (Milestone: "beat detector drives a counter on screen")

**Goal:** Get audio into the pipeline and extract meaningful features.
No lights change yet. The analysis values appear in the browser UI.

- [x] `AudioContext` capture from microphone — `web/music.js`, capture
      toggle on the sync page (Phase 1 step 1, 2026-08-03)
- [x] `AnalyserNode` → FFT bins (32 bands) + waveform (256 samples) —
      2048-pt FFT reduced to 32 geometrically-spaced bands + 256 samples
- [x] FFT data sent over WebSocket to Go at ~30 Hz (binary frame, `0x02`
      type byte, interleaved `float32`) — `internal/music.ParseFrame`,
      surfaced in the status push as `music.{active,frames,fft,wave}`
- [x] Go-side `BeatDetector` primitive (energy-based onset detection) —
      `internal/preset/analysis.go`, plan's algorithm + beat hold so the
      25 Hz effect clock reliably samples ~30 Hz onsets
- [x] Go-side `FreqBands` primitive (bass/mid/treble split)
- [ ] Browser UI: live FFT spectrum, beat indicator, BPM display
      (the mini FFT strip ships; the full visualization is later)
- [x] Go-side preset engine: load a preset JSON, instantiate primitives,
      run the graph each tick — `internal/preset`, wired into the engine's
      output loop (`music_preset` WS message, `MusicPreset` in the status)

**Exit test:** Clap your hands near the mic → beat indicator flashes.
Play a song → BPM estimate stabilizes within 5 seconds. Frequency bars
move in sync with the music.

### Phase 2 — Effect Primitives & Light Output (Milestone: "bass makes lights brighter")

**Goal:** Turn analysis features into actual light changes. First
presets ship.

- [x] `brightness_energy` — map energy to brightness per light
- [x] `color_map_energy` — map energy to hue in a palette
- [x] `color_map_frequency` — map bass/mid/treble to assigned colors
- [x] `strobe_beat` — flash a light on beat detection
- [x] `chase_trigger` — advance activation through lights on trigger
- [x] `pulse_energy` — brightness pulse expanding from a center light
- [x] Routing primitives: `all_lights`, `light_group`, `threshold_gate`
- [x] Modulation: `smoother` (scalar counterpart of
      `internal/pipeline/smooth.go`'s time-constant math), `adsr_envelope`,
      plus `lfo` (Chill Ambient's cycling needs it) — all Phase 2
      primitives landed 2026-08-03 in `internal/preset/`

**Exit test:** Load "Bass Pulse" preset → bass frequencies drive
brightness on selected lights, beat triggers a strobe flash. Load
"Chill Ambient" preset → slow color cycling, no strobe, smooth
transitions. (Presets ship with `light_ids: []` = all lights, since
RIDs are bridge-specific; a per-user light picker arrives with Phase 3.)

### Phase 3 — Preset Builder & Gallery (Milestone: "build a preset without editing JSON")

**Goal:** Users can create, edit, save, and share presets through the UI.

- [ ] Desktop node editor (canvas-based, drag-and-drop)
- [ ] Node palette (categorized by primitive type)
- [ ] Parameter inspector (click node → show its params)
- [ ] Preset save/load (localStorage + file export/import)
- [ ] Preset gallery (phone card browser)
- [ ] Tweak panel (phone bottom sheet with top-5 params)
- [ ] Preset validation (cycles? missing edges? type mismatches?)
- [ ] Preset versioning + migration

**Exit test:** On desktop: drag `mic_capture` → `beat_detector` →
`strobe_beat` → `light_group`, configure each, save as "My Beat
Strobe", activate it, lights strobe on beat. On phone: browse presets,
tap to activate, tweak brightness range from bottom sheet.

### Phase 4 — Advanced Features (Milestone: "system audio capture without a microphone")

**Goal:** Internal audio, spatial routing, visualization mode,
external control.

- [x] System audio capture via `getDisplayMedia({audio: true})`
      (tab/share audio, not mic — works with headphones) — browser form
      landed with the source selector (mic | internal audio) on
      2026-08-03; Android's MediaProjection audio path is still pending,
      and the internal option is disabled there until it lands
- [ ] Interactive visualization mode (light grid + spectrum + trails)
- [ ] Spatial routing: `zone_split` using existing zone geometry,
      `spatial_panner` mapping virtual position to nearest lights
- [ ] MIDI/OSC control surface (map CC/notes to primitive params)
- [ ] Gradient product segment mapping (multiple channels per device)
- [ ] Preset sharing (export as URL, import from clipboard)
- [ ] Streaming service metadata (Spotify Web Playback SDK for
      mood/genre-aware effect switching)

**Exit test:** Play Spotify through desktop speakers → lights react
without hearing room noise. Open visualization → see activation
trails moving across the light grid in time with the music. Connect
a MIDI controller → fader changes brightness range of active preset.

---

## Extension points

The primitive system is designed to be extended without touching the
engine core:

1. **New primitive types** — Implement the `Primitive` interface (below),
   register in the catalog. The preset engine, UI, and serializer all
   discover it through the catalog.

2. **New audio sources** — Implement `AudioSource`, feed FFT frames into
   the same WebSocket path. Could be: network audio stream, file playback,
   voice activity detector.

3. **New output targets** — The effect layer currently outputs HueStream
   channel colors. A `Target` interface could also drive: WLED over DDP,
   Nanoleaf over UDP, DMX over serial, MIDI note output.

4. **Community presets** — A preset is a JSON file. Share as gist, import
   from URL. A preset registry (GitHub repo) could host curated presets.

### Primitive interface (Go)

```go
// Primitive is one node in the preset graph. It receives typed feature
// values from upstream nodes, processes them, and produces typed outputs
// for downstream nodes.
type Primitive interface {
    // Type returns the catalog name, e.g. "beat_detector".
    Type() string

    // Ports describes the inputs and outputs.
    Ports() PortSpec

    // SetParams applies user-configurable parameters. Called on preset
    // load and when the user edits them in the UI.
    SetParams(p json.RawMessage) error

    // Process runs one tick. Inputs carries the values from upstream
    // edges; the primitive writes its outputs to the provided map.
    Process(inputs map[string]float64, outputs map[string]float64)
}

type PortSpec struct {
    Inputs  []Port  // what this primitive consumes
    Outputs []Port  // what this primitive produces
}

type Port struct {
    Name string    // e.g. "energy", "beat", "bass"
    Kind PortKind  // scalar, vector, trigger, spectrum
}
```

New primitives are registered at init time:

```go
func init() {
    preset.Register("beat_detector", func() Primitive { return &BeatDetector{} })
}
```

The UI discovers available primitives via a `/api/presets/catalog`
endpoint that returns the registered catalog with port specs and
parameter schemas.

---

## Integration with existing pipeline

The music reactivity engine feeds into the existing output pipeline
at the same point screen sync does:

```
Screen capture ──▶ pipeline.BuildZones ──▶ colors
                                              │
Music reactivity ──▶ effect layer ────────────▶ colors
                                              │
                                    ┌─────────▼──────────┐
                                    │ pipeline.Smoother   │
                                    │ pipeline.Gains      │
                                    │ pipeline.GamutMap   │
                                    │ hue.Stream.encode   │
                                    └─────────┬──────────┘
                                              ▼
                                          bridge:2100/udp
```

The music engine produces `[]hue.Channel` (same type as screen sync),
so the smoother, gains, gamut mapper, and encoder apply identically.
No new output path needed.

When music sync is active, screen capture is paused (or both can run
with a blend mode — Phase 4).

---

## Decision points

See [POSSIBLE-FUTURE.md](../POSSIBLE-FUTURE.md) "Decision points for
review" for DP-1 through DP-6 covering: Web Audio API vs libraries,
beat detection algorithm, effect engine architecture, preset storage
format, phone UI approach, and visualization rendering.

### DP-7: Analysis location — browser vs. Go
**Decision:** Analysis runs in the browser (Web Audio API), feature values
sent to Go over WebSocket.
**Rationale:** Web Audio API's `AnalyserNode` gives us hardware-accelerated
FFT with zero CPU cost on the Go side. The browser already has the audio
context; sending raw PCM to Go for FFT would add latency and Go CPU load
for a solved problem. Feature values (a few dozen floats per frame) are
negligible bandwidth (~1 KB/s at 30Hz).
**Trade-off:** Go can't do analysis without a browser providing audio. A
future headless mode (no browser, system audio capture) would need Go-side
FFT — but that's Phase 4+ and can use a pure-Go FFT library when needed.

### DP-8: Effect frame rate
**Decision:** Effects compute at the stream output rate (~25 Hz, matching
`output_hz`). Analysis frames arrive at ~30-60 Hz and are downsampled by
the engine to the output rate.
**Rationale:** The bridge stream runs at a fixed rate; computing effects
faster than the output clock wastes CPU. The existing `outputLoop` ticker
becomes the master clock for both screen sync and music sync.
**Trade-off:** 25 Hz is below the ~43ms window used for beat detection.
The beat detector runs at the analysis rate (30-60 Hz) and its output
is sampled at the effect rate. A beat happening between output ticks is
still captured; the effect just applies it on the next output tick
(~40ms latency, imperceptible for lights).

### DP-9: Multi-preset blending
**Decision:** One active preset at a time in Phase 1-3. Blending
(multiple presets active, per-light priority) is Phase 4.
**Rationale:** A single preset already produces rich behavior through
the graph. Blending adds complexity (priority arbitration, per-light
mixing, UI for blend weights) without enabling new use cases that a
well-designed single preset can't cover.
**Trade-off:** Can't have "bass drives these lights, melody drives those"
as two independently-toggleable presets. That specific case is handled
by routing primitives within a single preset (`split_by_frequency`).
