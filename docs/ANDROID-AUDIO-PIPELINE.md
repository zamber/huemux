# Android Audio Pipeline — Architecture & Fixes

## Current architecture

### Linux/Electron (working)

```
Browser JS                           Go server                          Bridge
─────────                           ──────────                          ──────
getDisplayMedia({audio:true})        handleAudioFrame()
    ↓                                     ↓
Web Audio API → AnalyserNode         music.State.Update()
    ↓                                     ↓
0x02 binary frames → WebSocket ──→  SetMusicFrameSource → engine
                                         ↓
                                    musicRunner.Step() → tick() → DTLS
```

Two independent capture paths can coexist: one `getDisplayMedia` for video
and another for audio. Each returns a separate MediaStream. The browser
handles them independently. The Electron wrapper's
`setDisplayMediaRequestHandler` returns `audio: 'loopback'` for system audio.

### Android (broken as of 2026-08-03)

```
Kotlin                              Go server                          Bridge
──────                              ─────────                          ──────
MediaProjection ──→ ImageReader     mobile.PushFrame() → engine.SetFrame() ✅
       │
       └────→ AudioRecord           mobile.PushAudioPCM() → Analyzer → music.State
                (PCM 44100 Hz)           ↓
                                     musicRunner = nil ❌
                                          ↓
                                     tick() → falls to grid path → grid=nil → return

startAudioCapture() NEVER calls Mobile.startSync(areaId) → no DTLS stream ❌
```

### Root causes

1. **Two separate capture paths compete for one MediaProjection.**
   Android allows exactly one MediaProjection session. `startCapture` (video)
   and `startAudioCapture` (audio-only) each try to create one, with no
   coordination. If video is running, `startAudioRecording()` can attach to
   the existing projection (works). If NOT running, a separate audio-only
   service starts but never calls `Mobile.startSync()` → no DTLS → dead end.

2. **The music preset is never auto-activated.**
   Audio frames arrive at `music.State` but `engine.musicRunner` is nil.
   The user must manually select a preset from the dropdown, which defaults
   to "Off." Until then, `tick()` falls through the music-runner path to the
   grid path, where a nil grid returns early → no colors sent.

3. **Audio read errors are silently dropped.**
   `AudioRecord.read()` can return `ERROR_INVALID_OPERATION` (-3),
   `ERROR_BAD_VALUE` (-2), `ERROR_DEAD_OBJECT` (-6). The audio loop in
   `ScreenCaptureService.audioLoop()` does `if (n <= 0) continue` — all
   errors silently ignored with no log.

4. **Audio frame logging is gated on `-debug`.**
   `logMusicStats()` only fires when `debuglog.Enabled` is true. On Android,
   `Capture()` enables the ring buffer but `Enabled` stays false, so audio
   stats never appear in the diagnostics report.

## How it differs from Linux

| Aspect | Linux | Android |
|--------|-------|---------|
| Capture API | `getDisplayMedia` (browser) | `MediaProjection` (platform) |
| Multiple sessions | Yes — browser permits independent audio+video streams | No — exactly one MediaProjection |
| Audio analysis | Web Audio API in browser (fast, native) | Pure-Go FFT (headless, DP-7) |
| DTLS start | `select_area` WS message after capture | `Mobile.startSync(areaId)` before consent |
| Audio-only mode | Works — separate `getUserMedia({audio})` | Broken — no `startSync` call |
| Log access | `-debug` flag, filesystem | Ring buffer only, via diagnostics report |

## Fixes applied (Go + web, 2026-08-03)

### Fix 1: CaptureMode routing (Go)

The engine now has a `CaptureMode` field (`video` / `audio` / `audiovideo`).
`tick()` routes based on mode:
- **video** (default): grid only, backwards-compatible
- **audio**: music preset only; grid ignored. Falls back to neutral keepalive
  frames if no preset is active → DTLS session stays alive.
- **audiovideo**: music preset if active, otherwise grid

Set via `capture_mode` WS message or `engine.SetCaptureMode()`.

### Fix 2: Auto-activate preset (Go)

`handleAudioFrame()` and `PushAudioPCM()` now call `autoActivateMusic()` on
the first frame. If capture mode is `audio` or `audiovideo` and no preset is
active, it activates `bass_pulse` automatically. No user interaction needed.

### Fix 3: Ungated audio logging (Go)

`logMusicStats()` now writes to the ring buffer via `debuglog.Audiof()`
regardless of `-debug`. Throttled to once per 5 seconds. `PushAudioPCM` also
logs PCM chunk stats. The engine logs a once-per-session diagnostic on the
first tick showing mode/grid/preset/zone state.

### Fix 4: Zone preview tinting (JS)

`drawZoneOverlays()` now falls back to a visible neutral gray when zone colors
are (0,0,0), so calibration rectangles are always visible even before
streaming starts.

### Fix 5: Mode selector UI (JS/HTML)

A `capture-mode` dropdown was added next to the area selector with options
Video / Audio / Audio+Video. The music panel is shown/hidden based on mode.
In audio mode, the preview canvas draws a 32-band FFT histogram.

### Fix 6: Synthetic audio test harness (Go)

`internal/music/testharness.go` generates proper 0x02-wire-format frames with
a configurable BPM and bass frequency. Used by `music_pipeline_test.go` to
verify the full analysis→preset→output chain end-to-end with zero hardware.

## Fixes applied (2026-08-04)

### Fix 1: Always capture both video and audio from one projection ✅

`ScreenCaptureService.onStartCommand` no longer has an `audioOnly` branch.
The service always creates the VirtualDisplay for video AND auto-starts
`AudioPlaybackCapture` when API >= 29. One consent, both streams.

### Fix 2: Log audio read errors ✅

`ScreenCaptureService.audioLoop` now logs read errors (first and every
100th), breaks on `ERROR_INVALID_OPERATION`/`ERROR_DEAD_OBJECT`, and
sleeps 10ms on transient errors to avoid a busy-spin.

### Superseded: separate `startAudioCapture` bridge method

Fixes 3 and 4 from the original spec (give `startAudioCapture` an `areaId`,
pass `areaId` from `music.js`) are **superseded by removal**. The audio-only
service mode, the `startAudioCapture`/`stopAudioCapture` bridge methods, and
the `__huemuxAudioResult` callback registry are all gone. The unified UI has
one Start button that triggers one native `startCapture` call, which creates
one projection that captures both screen and audio.

The `HueMuxMusic` JS module adopts the native audio stream from the status
push (`msg.music.active` → auto-adopt). No separate bridge call, no second
consent dialog, no orphaned DTLS-less projection.

### Additional: "Off" preset sticks ✅

`internal/server/http.go`: `musicOffExplicit` flag. Choosing Off from the
preset dropdown sets the flag; `autoActivateMusic()` respects it.
`music_stop` clears the flag so the next capture session may auto-activate.

## Updated architecture (post-fix)

```
Kotlin                              Go server                          Bridge
──────                              ─────────                          ──────
MediaProjection ──→ VirtualDisplay   mobile.PushFrame() → engine.SetFrame() ✅
       │
       └────→ AudioRecord            mobile.PushAudioPCM() → Analyzer → music.State ✅
                (PCM 44100 Hz)            ↓
                                     musicRunner.Step() → tick() → DTLS ✅
                                         ↑
                                     autoActivateMusic() activates bass_pulse
                                     on first audio frame (unless Off explicit)
```

One consent dialog, one projection, one foreground service. Both streams
flow from the same session.

## Android 16 research (2026-08-04)

No new Android 14–16 API unifies screen + audio capture into a single call.
`MediaProjection` + `AudioPlaybackCapture` + `AudioRecord` remains the only
way to get live PCM audio alongside screen frames. Android 16's
`ScreenRecordingController` can produce system-muxed recordings but does not
deliver live frames — it cannot replace the `ImageReader` → `PushFrame` path
the colour pipeline depends on.

## Silent-audio checklist

Audio capture reports "recording" but the spectrum stays flat? These are
the three known causes, ordered by likelihood:

1. **Uncapturable app.** Apps can opt out via
   `AudioManager.setAllowedCapturePolicy`. Netflix, Spotify, and banking
   apps typically do. Play audio from a source that allows capture (YouTube,
   a local media player, the device's own ringtone).
2. **WebView 147 regression** (April 2026). A bug in Android System WebView
   v147 caused `AudioPlaybackCapture` to return silence. Fixed in WebView
   148+. Check the device's WebView version; if on 147, update or install
   WebView Beta.
3. **Bluetooth audio routing.** Bluetooth headphones (A2DP) route audio
   directly to the headset, bypassing the system mixer. The recorder hears
   nothing. Disconnect Bluetooth and play through the device speaker.
