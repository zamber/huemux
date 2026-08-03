# Missing test coverage for critical packages

**Severity:** `high`

**Category:** `test-coverage`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

Four core packages have zero or near-zero test coverage. The Hue protocol, the engine, light control, and config packages — together comprising the majority of the Go source — have no tests.

## Affected Files

| Package | Source files | Test files | Coverage estimate |
|---|---|---|---|
| `internal/hue/` | 6 files (clip, discover, eventstream, light, stream, types) | 0 | 0% |
| `internal/engine/` | 1 file (engine.go) | 0 | 0% |
| `internal/lightctl/` | 1 file (service.go) | 0 | 0% |
| `internal/config/` | 3 files (config, favorites, settings) | 0 | 0% |
| `internal/pipeline/` | 3 files (color, smooth, zones) | 2 test files | ~60% (smooth.go untested) |
| `internal/debuglog/` | 1 file (debuglog.go) | 0 | 0% |

Packages with good tests: `internal/server/` (5 test files), `internal/appconfig/` (1 test file), `mobile/` (1 test file).

## Evidence

```bash
$ find internal -name '*_test.go' | sort
internal/appconfig/appconfig_test.go
internal/pipeline/color_test.go
internal/pipeline/zones_test.go
internal/server/auth_test.go
internal/server/config_api_test.go
internal/server/diagnostics_test.go
internal/server/profile_test.go
internal/server/testhelp_test.go
```

## Why It Matters

The untested packages handle:
- **`internal/hue/stream.go`** — DTLS handshake, packet encoding, the output loop, fade-out logic. A regression here produces silent stream failures (no error, just black lights).
- **`internal/hue/clip.go`** — All bridge REST API calls. This is the single most exercised code path during normal use.
- **`internal/engine/engine.go`** — Stream lifecycle, zone building, settings management. The orchestrator for the entire sync feature.
- **`internal/hue/eventstream.go`** — SSE parsing with exponential backoff reconnect. An off-by-one in SSE parsing (single `\n` split vs `\n\n` event boundaries) could silently corrupt event data.
- **`internal/pipeline/smooth.go`** — Temporal smoothing. Untested math in the color pipeline means a smoothing regression would be caught only by visual inspection.

The existing tests are excellent — `appconfig_test.go` has 440 lines of thorough tests, `server/auth_test.go` covers edge cases well. The gap is not test quality but test existence.

## Suggested Fix

Prioritize tests by risk:

1. **`internal/hue/stream_test.go`** — packet encoding/decoding (pure function, no network needed). The `encode()` method is entirely deterministic.
2. **`internal/hue/clip_test.go`** — needs a mock HTTP server (see `005-mock-hue-bridge-for-testing.md`)
3. **`internal/pipeline/smooth_test.go`** — pure math, no dependencies
4. **`internal/hue/discover_test.go`** — mDNS packet parsing is pure function; `extractARecords` and `buildPTRQuery` are testable
5. **`internal/engine/engine_test.go`** — zone building, settings update logic

## Related Issues

- `005-mock-hue-bridge-for-testing.md` — prerequisite for testing `clip.go` and `eventstream.go`
