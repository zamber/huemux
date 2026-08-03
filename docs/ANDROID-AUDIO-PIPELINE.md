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

## Kotlin changes needed (can't compile on this host)

These changes are specified here for the Android CI build. No Android SDK or
NDK is available on this host (see `AGENTS.md`).

### 1. `ScreenCaptureService.kt` — Remove `audioOnly` mode

The service should always capture both video and audio when possible:

```kotlin
// In onStartCommand, after creating VirtualDisplay for video:
if (!audioOnly) {
    val t = HandlerThread("huemux-capture").also { it.start() }
    pipelineThread = t
    pipeline = Handler(t.looper)
    pipeline?.post { startCapture() }
    // ALSO start audio capture on the same projection.
    // startAudioRecording() is a no-op if already running.
    val audioErr = startAudioRecording()
    if (audioErr != null) {
        Mobile.logHost("audio: auto-start failed: $audioErr")
    }
} else {
    // Keep audio-only for now, but it MUST call Mobile.startSync first.
    // See MainActivity changes below.
    Mobile.logHost("audio: audio-only capture service up")
    val err = startAudioRecording()
    if (err != null) {
        Mobile.logHost("audio: auto-start failed: $err")
    }
}
```

### 2. `ScreenCaptureService.kt` — Log audio read errors

```kotlin
private fun audioLoop(record: AudioRecord) {
    val buf = ByteArray(AUDIO_CHUNK_BYTES)
    var errorCount = 0
    while (audioRunning) {
        val n = record.read(buf, 0, buf.size, AudioRecord.READ_BLOCKING)
        if (n <= 0) {
            errorCount++
            if (errorCount == 1 || errorCount % 100 == 0) {
                Mobile.logHost("audio: read error $n (count=$errorCount)")
            }
            // Avoid a busy-spin on persistent errors.
            if (n == AudioRecord.ERROR_INVALID_OPERATION ||
                n == AudioRecord.ERROR_DEAD_OBJECT) {
                Mobile.logHost("audio: fatal read error $n, stopping")
                break
            }
            Thread.sleep(10)
            continue
        }
        errorCount = 0
        try {
            Mobile.pushAudioPCM(buf.copyOf(n), AUDIO_SAMPLE_RATE.toLong())
        } catch (e: Exception) {
            Log.w(TAG, "pushAudioPcm failed", e)
        }
    }
}
```

### 3. `MainActivity.kt` — `startAudioCapture` requires `areaId`

```kotlin
@JavascriptInterface
fun startAudioCapture(areaId: String, callbackId: String) {
    runOnUiThread {
        val svc = ScreenCaptureService.instance
        if (svc != null) {
            // Screen capture already running — attach audio to its projection.
            val err = svc.startAudioRecording()
            resolveAudio(callbackId, err == null, err ?: "")
            return@runOnUiThread
        }
        // No service running: need a fresh MediaProjection AND a DTLS stream.
        try {
            Mobile.startSync(areaId)
        } catch (e: Exception) {
            Log.e(TAG, "startSync for audio failed", e)
            resolveAudio(callbackId, false, e.message ?: "could not select area")
            return@runOnUiThread
        }
        pendingCaptureCallback = callbackId
        pendingAudioOnly = true
        val mgr = getSystemService(MEDIA_PROJECTION_SERVICE) as MediaProjectionManager
        projectionLauncher.launch(mgr.createScreenCaptureIntent())
    }
}
```

### 4. `web/music.js` — Pass `areaId` to native audio capture

```javascript
function startNativeAudioCapture() {
    return new Promise((resolve, reject) => {
      const id = 'a' + Date.now() + Math.random().toString(36).slice(2, 8);
      const areaId = document.getElementById('area-select').value;
      if (!areaId) {
        reject(new Error('no entertainment area selected'));
        return;
      }
      audioWaiters[id] = (ok, err) => {
        delete audioWaiters[id];
        ok ? resolve() : reject(new Error(err || 'audio capture was not permitted'));
      };
      window.HueMuxNative.startAudioCapture(areaId, id);
    });
}
```

## Verification after Kotlin changes

1. Start Huemux on Android, pair with bridge
2. Select area, set mode to "Audio"
3. Press Start → MediaProjection consent dialog appears
4. After consent: check diagnostics report for "audio: internal capture started"
5. Verify spectrum appears in preview canvas
6. Verify lights respond to device audio playback (play music on the phone)
7. Switch mode to "Video" → lights follow screen content instead
8. Switch mode to "Audio+Video" → lights follow music preset while screen
   recording also runs
