# Plan: Android audio sync — Kotlin diagnostics, bridge fix, pipeline tests, alpha.37

## Context

Commit `9629c20` ("fix: unify Android capture, fix audio sync, integrate music
UI", tagged `v0.0.2-alpha.36`) removed the separate `startAudioCapture`
bridge method and made one MediaProjection capture both video and internal
audio. The diagnostics report the user pasted (alpha.36, Nothing A024) shows:

- `recent log` contains only Go-side lines. No Kotlin failures.
- The user reports a "no method startCapture" error when starting sync.

Both are real defects that shipped in alpha.36.

## Root causes

### 1. "no method startCapture" — JS bridge argument-count mismatch

- `web/app.js` calls `window.HueMuxNative.startCapture(areaId, id, withAudio)`
  — three arguments.
- `MainActivity.kt`'s `@JavascriptInterface fun startCapture(areaId: String,
  callbackId: String)` — two parameters.

Android's `addJavascriptInterface` bridge matches methods by **name and
argument count** ([chromium java-bridge.md](https://chromium.googlesource.com/chromium/src/+/refs/heads/main/android_webview/docs/java-bridge.md)).
A three-argument call against a two-parameter method finds no match, and the
bridge throws "no method startCapture". This breaks every native capture
attempt on Android.

The `withAudio` third argument was added in `9629c20`, but the Kotlin
signature was never changed to accept it. The unified design always captures
both streams, so the flag is meaningless — the native side has no use for it.

### 2. Kotlin failures never reach the diagnostics report

The report's `recent log` section is the Go ring buffer (`debuglog.Note`).
Kotlin code only reaches it when it explicitly calls `Mobile.logHost(...)`.
Everywhere else — `Log.e`/`Log.w` in `MainActivity`, `ScreenCaptureService`,
`FrameRecorder` — the failure goes to logcat, which is unreachable on a phone
without adb. A diagnostics report from a device therefore never mentions the
layer where screen sync actually lives.

### 3. No Kotlin tests run anywhere in CI

`.github/workflows/android.yml`:
- `verify` job: Go vet/test only; its push paths do not include `android/**`.
- `aar` job: builds the AAR + debug APK, runs **zero** Gradle tests.

There is no `src/test` source set and no JUnit dependency in
`android/app/build.gradle.kts`. The bridge-signature regression and the
stale-`startAudioCapture`-style errors are exactly what a native-API
consistency test would have caught.

## Implementation

### Step 1 — Fix the bridge mismatch (the actual bug)

`web/app.js`, `startNativeCapture()`:

- Change `window.HueMuxNative.startCapture(areaId, id, withAudio);` back to
  `window.HueMuxNative.startCapture(areaId, id);`.
- Delete the now-misleading `withAudio` computation and its comment. The
  native service always captures video + internal audio from the one
  projection; the `capture_mode` WebSocket message already tells the Go engine
  how to route.

No Kotlin change needed: the two-parameter `startCapture` is the intended API.

### Step 2 — Global Kotlin error grab

**New file `android/app/src/main/java/com/huemux/app/HueLog.kt`.** A wrapper
over `android.util.Log` that also pushes every error/warning into the Go
diagnostics ring via `Mobile.logHost(...)`, so Kotlin failures appear in the
report's `recent log` section.

- `e(tag, msg, t)` / `w(tag, msg, t)` mirror `Log.e`/`Log.w` and call a
  `sink` (default `{ Mobile.logHost("android: $it") }`).
- Rate limiter (max ~50 lines / 10 s) so a failing hot loop cannot flood the
  800-line ring.
- `Log.e`/`Log.w` calls are wrapped in try/catch — a logging failure must
  never crash the app.
- Internal `var sink: (String) -> Unit` so unit tests can count/drop lines
  without the Go library.

**New file `android/app/src/main/java/com/huemux/app/CrashCatcher.kt`.**
Global uncaught-exception handler installed once from `MainActivity.onCreate`:

- On crash: writes `crash-last.txt` into `filesDir` (survives the restart)
  and calls `Mobile.logHost` with the exception class/message.
- Chains to the previous handler so the OS still gets the crash.

**Replace `Log.e`/`Log.w`/`Log.i` calls** in `MainActivity.kt`,
`ScreenCaptureService.kt`, `FrameRecorder.kt` with the `HueLog` equivalents
(mechanical: `Log.` → `HueLog.` + import). Where a `Mobile.logHost` call
already exists next to a `Log.e`, keep both but let `HueLog` own the Log side.

**Include the last crash in the host block.** In `MainActivity`'s
`publishHostInfo()`, append `CrashCatcher.latest()` (a short `crash: …` line)
so a report taken after a crash still names it.

### Step 3 — Kotlin unit tests + CI wiring

**`android/app/build.gradle.kts`:**
- `testImplementation("junit:junit:4.13.2")`.
- `testOptions { unitTests.isReturnDefaultValues = true }` (lets pure-logic
  tests touch `android.util.Log` stubs without Robolectric).

**New test source set `android/app/src/test/java/com/huemux/app/`:**

1. `NativeBridgeApiTest.kt` — **the key test.** Parses `MainActivity.kt` for
   `@JavascriptInterface fun name(params)` and parses `web/app.js` +
   `web/music.js` for `HueMuxNative.<name>(args)`. Asserts:
   - every JS call names an existing `@JavascriptInterface` method, and
   - the JS argument count equals the native parameter count.
   This catches the `startCapture` regression (3 args vs 2) and any leftover
   call to a removed method (e.g. `startAudioCapture`). A path resolver walks
   up from the test working dir to find the repo root.

2. `ScreenCaptureServiceTest.kt` — pure helpers: `pipelineStep(w,h)` (long
   edge ≤ 480 budget, ceiling) and `avg4(a,b,c,d)` (unsigned-byte mean,
   signed-byte input handling).

3. `FrameRecorderTest.kt` — `bitrateFor(w,h)` clamp bounds.

4. `HueLogTest.kt` — throttle logic via the injectable sink.

**`.github/workflows/android.yml`:**

- Add `android/**` to the workflow's push `paths` so Kotlin-only changes
  actually run the pipeline (today they do not).
- Add a `kotlin-tests` job (PR + push to main):
  checkout → setup-go stable → setup-java 17 → install gomobile/gobind →
  `gomobile init` → `go get -tool …/gobind` → `gomobile bind` to
  `android/app/libs/huemux.aar` → `gradle wrapper` → `./gradlew --no-daemon
  testDebugUnitTest` → upload `android/app/build/reports/tests/**` on failure.
  This is the PR-time gate that catches Kotlin-side regressions.
- Add the same `testDebugUnitTest` step to the `aar` job after `assembleDebug`
  so main also runs the suite.

### Step 4 — Coverage / exercise-the-code summary

| Class | What is now pinned |
|---|---|
| `NativeBridgeApiTest` | JS↔Kotlin bridge surface: names + arity (the regression) |
| `ScreenCaptureServiceTest` | downsample step + unsigned-byte averaging |
| `FrameRecorderTest` | bitrate clamp |
| `HueLogTest` | error-routing throttle |

### Step 5 — Release alpha.37

1. Commit to `main`: the two fixes + tests + workflow changes.
2. Let CI pass (`verify`, `kotlin-tests`, `aar`).
3. Tag `v0.0.2-alpha.37`; push the tag. `release.yml` builds + publishes the
   pre-release (tag contains a hyphen).
4. Add curated release notes `.github/release-notes/v0.0.2-alpha.37.md`
   (narrative style, matching alpha.24's format) describing: the bridge fix,
   Kotlin errors now visible in diagnostics, and the new CI Kotlin tests.

## Files to change

- `web/app.js` — drop the `withAudio` argument (the bug fix).
- `android/app/src/main/java/com/huemux/app/HueLog.kt` — new.
- `android/app/src/main/java/com/huemux/app/CrashCatcher.kt` — new.
- `android/app/src/main/java/com/huemux/app/MainActivity.kt` — `HueLog`,
  `CrashCatcher.install`, host-block crash line.
- `android/app/src/main/java/com/huemux/app/ScreenCaptureService.kt` — `HueLog`.
- `android/app/src/main/java/com/huemux/app/FrameRecorder.kt` — `HueLog`.
- `android/app/build.gradle.kts` — JUnit + `returnDefaultValues`.
- `android/app/src/test/java/com/huemux/app/*Test.kt` — new tests.
- `.github/workflows/android.yml` — `android/**` paths, `kotlin-tests` job,
  unit-test step in `aar`.
- `.github/release-notes/v0.0.2-alpha.37.md` — new.

## Verification

- Local (host has Go, no Android SDK): `go test ./...`,
  `CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...`.
- CI: `verify` + `kotlin-tests` on push/PR; `aar` on main; `release` on tag.
- The Kotlin side cannot be built locally (no SDK/NDK on this host,
  AGENTS.md) — CI is the gate.

## Risks / notes

- `HueLog`'s rate limiter is the only guard against flooding the 800-line
  ring from an erroring hot loop; keep the window generous but bounded.
- The API-surface test reads `web/` from the android module by walking up the
  directory tree. It will fail loudly with a clear message if the layout is
  unexpected, rather than silently passing.
- No behavior change beyond the bug fix and the added diagnostics: `withAudio`
  had no functional effect (the service always captures both streams).
