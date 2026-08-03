# POSSIBLE-FUTURE.md — Community Wishlist & Market Research

Features Hue users want that no client does well (or at all). Gathered from
app store reviews, Reddit, Home Assistant forums, and competitive analysis,
2025-08.

## Market landscape — what existing clients do well

| Client | Strengths | Weaknesses |
|---|---|---|
| **hueDynamic** (iOS/Android, $4.99) | Multi-bridge transparently, 50+ dynamic scenes, TV/camera sync, no IAP, photo ambience | Audio sync uses mic only, no internal-audio path |
| **Light DJ** (iOS, sub) | Ableton Link / MIDI Clock Sync, 100+ effects, DJ controller layout, Nanoleaf/LIFX | Subscription model, iOS only, complex for non-DJs |
| **iLightShow** (iOS/Android) | Spotify/Apple Music/Deezer/Tidal integration, precision beat detection, LIFX/Nanoleaf, iOS Shortcuts | Mic-only on Android, free limited to 1 light, paid tiers |
| **iConnect Hue** (iOS, $3.99 sub) | Apple Watch, broad music source support, multi-room, accessory integration | Subscription, iOS only |
| **SonicHue Sync** (iOS) | MIDI keyboard/controller input, ADSR envelope synth, DJ performance pad, low latency | "Coming soon" features (multi-bridge, gradient), iOS only |
| **Soundstorm** (Chrome ext) | Easy setup, reliable, great color combos, customizable | Inconsistent sync at times, Chrome-only |
| **Hue Sync desktop** (official) | Free, screen + audio, reliable | Closed source, Windows/macOS only, no preset editor |
| **Music Assistant** (OSS, HA addon) | True OSS, FFT analysis, beat detection, frequency-band mapping, Sendspin protocol | Requires Home Assistant, complex setup, experimental |
| **LedFx** (OSS, desktop) | Real-time audio visualization, effects engine, extensible | Separate process, not Hue-native, complex setup |

## What every client fails at

### 1. Multi-bridge support
/r/philipshue's most-requested feature (50+ posts). The Hue app itself
recently got this — third-party clients lag behind. A single bridge handles
~50 lights; power users with 100+ lights need two bridges but get two
separate apps.

### 2. Internal audio capture (no microphone)
Every mobile app uses the microphone for music sync. This means:
- Room noise bleeds into the effect
- Bluetooth headphones don't work (mic captures ambient, not the music)
- Quality degrades at parties (people talking over the mic)

Desktop apps (Hue Sync, LedFx) do internal capture. A web-based approach
using `getDisplayMedia({audio: true})` could do this in-browser, which no
mobile app currently does.

### 3. Preset builder — modular, composable
Current apps ship fixed effects (Strobe, Pulse, Chase, Wave). None let you
compose: "take bass energy → map to brightness on the left lights → color-
cycle the right lights on beat." The closest is Light DJ's "Super
SceneMaker" but it's still preset-selection, not a patch-bay.

### 4. Per-light effect routing
Every app sends the same effect to all lights in an entertainment area.
Users want: "this light does bass pulses, these two do melody color shifts,
that one strobes on snare hits." Requires a router primitive between
analysis and output.

### 5. Visualization of what's happening
When lights are syncing, no app shows you WHICH light is doing WHAT. The
user sees lights changing but can't tell if the effect is working as
intended. An interactive visualization showing per-light activation state
would make debugging and tuning possible.

### 6. Graceful release of entertainment control
When Hue Sync stops, lights stay in their last state. Other home automation
apps can't control them until the entertainment stream is explicitly
stopped. A "release on pause" or auto-release timeout would fix this.

### 7. Gradient product support
Hue Gradient lightstrips, Play gradient tubes, and Signe gradient lamps
support per-segment color. The Entertainment API exposes these as separate
channels, but most apps treat gradient products as single lights.

### 8. BPM-aware effect timing
Most apps do raw energy mapping (louder = brighter). Few do beat-timed
effects (flash exactly on the beat, hold through the bar). Light DJ and
SonicHue come closest with MIDI clock sync, but that requires manual BPM
tapping. Auto-BPM detection exists (iLightShow) but is rarely exposed as a
modulation source for other effects.

## Community pain points (from forums & reviews)

1. **"After a sync session, my automations are broken"** — lights stay in last
   sync state, motion sensors don't override, bedtime routines fail.
2. **"Why do I need the sync box if I have the Bridge Pro?"** — product
   confusion, users want software-only sync for music.
3. **"My gradient lightstrip only shows one color during sync"** — gradient
   products treated as single-channel.
4. **"Can't use Bluetooth headphones with music sync"** — mic-based capture
   means speakers only.
5. **"The app doesn't know where my lights are"** — no spatial awareness,
   effects can't flow left→right or match room geometry.
6. **"Too many apps, each does one thing"** — fragmentation. Users want one
   app that does lights + scenes + sync + effects.

## Features that would differentiate HueMux

Derived from the gaps above, things HueMux could do that others don't:

### Near-term (in scope for music reactivity MVP)
- **Web-native internal audio capture** — `getDisplayMedia({audio: true})`,
  no mic needed, works with headphones
- **Modular preset builder** — chain audio analysis → routing → effects as
  composable primitives
- **Per-light effect routing** — different lights react to different audio
  features
- **Auto-BPM detection** driving effect timing (not just energy mapping)

### Medium-term
- **Interactive visualization** — live 2D map of lights showing activation
  state, frequency assignment, and effect propagation
- **Spatial-aware routing** — effects that flow across the room using the
  Hue app's zone geometry
- **Beat-synchronized effects** — flash/chase/pulse locked to detected BPM
  with phase offset per light

### Long-term / speculative
- **Multi-bridge support** — aggregate lights from 2+ bridges into one view
- **Gradient segment mapping** — per-segment color for gradient products
- **MIDI/OSC control surface** — map faders/knobs/pads to effect parameters
- **Streaming service integration** — Spotify/Apple Music metadata for
  mood-aware effect switching
- **3D spatial audio routing** — move virtual sound sources through the
  room using light positions

## Decision points for review

These are architectural choices that affect the entire music reactivity
design. Each is documented with the chosen path and the rationale.

### DP-1: Web Audio API vs. external audio library
**Decision:** Web Audio API (`AudioContext`, `AnalyserNode`).
**Rationale:** Zero dependencies, already shipped in every browser, fast
FFT via native code. The `AnalyserNode` gives us 32-2048 frequency bins
at whatever resolution we need. No reason to bundle a JS FFT library.
**Trade-off:** Limited to browser audio sources (mic, tab capture,
`getDisplayMedia`). Can't capture system audio without user consent.
**Alternative rejected:** Meyda, essentia.js — heavier, slower, no
benefit over native `AnalyserNode` for our use case.

### DP-2: Beat detection algorithm
**Decision:** Onset-detection with adaptive threshold (energy-based).
**Rationale:** Simple, fast, well-understood. Compute short-term energy
over ~43ms windows (2048 samples @ 48kHz), compare to longer-term
average, detect onset when ratio exceeds 1.3×. Minimum 300ms between
beats (~200 BPM cap). Same approach used by LedFx and Music Assistant.
**Trade-off:** Less accurate on complex material (jazz, classical) than
spectral-flux methods. Adequate for pop/electronic/rock.
**Alternative rejected:** Spectral flux (more complex, same practical
results for the genres people play at parties), ML-based (overkill).

### DP-3: Effect engine architecture
**Decision:** Directed acyclic graph (DAG) of processing nodes.
**Rationale:** Let users chain: AudioSource → BeatDetector → Router →
ColorMapper → LightOutput. Each node has typed inputs/outputs. The
preset is the graph. This is the "multiplexer" the user envisioned.
**Trade-off:** More complex to build than a flat preset list. But a flat
list would need rewriting to add routing/modulation later.
**Alternative rejected:** Flat preset system with hardcoded parameter
knobs (like every existing app). Not extensible.

### DP-4: Preset storage format
**Decision:** JSON objects, same structure as the DAG.
**Rationale:** Human-readable, debuggable, trivially shareable. Same
format for file storage and WebSocket transport. Versioned schema.
**Trade-off:** Verbose compared to binary. Acceptable for presets that
are at most a few KB.
**Alternative rejected:** Protobuf, MessagePack — not debuggable in
devtools, harder for users to inspect/edit.

### DP-5: Phone UI approach
**Decision:** Card-based preset browser + tap-to-activate, NOT a
miniaturized node editor.
**Rationale:** Touch-dragging nodes on a phone screen is frustrating.
Cards with large touch targets (≥44px), swipe to switch presets, tap
to toggle. The full node editor lives on desktop/tablet only.
**Trade-off:** Phone users can't build presets from scratch on-device.
They can activate, tweak parameters, and reorder — but composing a new
preset needs a larger screen or a companion desktop session.
**Alternative rejected:** Responsive node editor (too cramped below
~768px width), bottom-sheet parameter panels (works for tweaking,
not for understanding signal flow).

### DP-6: Visualization rendering
**Decision:** Canvas 2D, not WebGL.
**Rationale:** Our visualization needs are modest: 2D light map,
frequency bars, activation trails. Canvas 2D handles this at 60fps
without the GPU context overhead. Same tech as the existing calibration
preview.
**Trade-off:** Can't do particle effects or 3D. If we later want 3D
spatial visualization, we'd need WebGL — but that's Phase 4+.
**Alternative rejected:** WebGL (overkill for Phase 1-3), DOM-based
(too slow for real-time updates at >30Hz).
