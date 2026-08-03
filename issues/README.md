# HueMux Codebase Audit — Issue Index

Exploratory proofing run, 2026-08-02. All findings, no fixes applied.

## Summary

| Severity | Count | Description |
|---|---|---|
| Critical | 4 | Process-crashing bugs, CSRF, silent token dead-end |
| High | 11 | Data races, resource leaks, weak security defaults, missing test coverage |
| Medium | 16 | Performance, maintenance debt, documentation, edge cases |
| Low | 20+ | Dead code, style issues, minor hardening |

## Issue files

| # | Title | Severity |
|---|---|---|
| [001](001-websocket-frame-size-unbounded.md) | WebSocket frame size unbounded — local OOM risk | critical |
| [002](002-agents-md-outdated-license-claim.md) | AGENTS.md says "no LICENSE" but LICENSE exists | medium |
| [003](003-bridge-tls-no-certificate-pinning.md) | Bridge HTTPS uses InsecureSkipVerify without certificate pinning | high |
| [004](004-no-linter-configuration.md) | No linter configuration — only go vet and gofmt | high |
| [005](005-duplicate-code-in-cmd-packages.md) | Duplicate code between cmd/huemux and cmd/huemux-desktop | medium |
| [006](006-missing-test-coverage.md) | Missing test coverage for critical packages | high |
| [007](007-release-signing-key-permissions.md) | RELEASE-SIGNING-KEY.asc has restrictive permissions | medium |
| [008](008-ci-android-path-triggers-asymmetric.md) | CI Android verify job has incomplete path triggers | medium |
| [009](009-no-security-headers.md) | No Content-Security-Policy or security headers | medium |
| [010](010-csrf-config-api-post-method.md) | CSRF on /api/config — any website can rewrite config | critical |
| [011](011-output-hz-panics-server.md) | Unvalidated output_hz panics the server | critical |
| [012](012-data-race-stream-stats.md) | Data race on Stream.Sent and Stream.LastError | high |
| [013](013-frontend-no-token-support.md) | Frontend has no way to send auth token | critical |
| [014](014-android-webview-js-bridge-external.md) | Android WebView JS bridge reachable from third-party pages | high |
| [015](015-settings-ui-weak-token-generator.md) | Settings UI token generator has ~15 bits of entropy | high |
| [016](016-concurrent-selectarea-leaks-stream.md) | Concurrent SelectArea leaks DTLS stream and goroutines | high |
| [017](017-broadcast-head-of-line-blocking.md) | broadcast() blocks all clients on slowest reader | medium |

## Additional findings (not yet in individual issue files)

These are documented in the subagent reports but may not warrant full issue files:

### Dead code and orphaned assets
- `lightctl.Service.Identify` — never called, `identify` WS message routes to `eng.Identify`
- `hue.Client.ListDevices` / `hue.Device` — never called
- `hue.Light.ColorTemperature` field — never read
- `config.AreaSettings.Letterbox`, `.SceneCutSensitivity`, `.CaptureWidth/Height/FPS` — stored and exposed in UI but never consumed
- `web/shared/icon.svg` and `icon-light.svg` — referenced nowhere
- `web/capture-worker.js:42-47` — `case 'grid'` branch never used
- `web/shared/i18n.js:163-165` — `data-i18n-html` feature unused, potential XSS sink

### Configuration and documentation
- `appconfig.GenerateToken` never called outside tests — docs promise auto-generation on first non-loopback start but code has no such feature
- `cmd/huemux-desktop/main.go` not gofmt-clean (one alignment issue)
- Stale comment in `mobile_test.go:215` says "defaults to lights" but defaults to `ProfileFull`
- `detectSystemLang` only supports `pl` and `en` despite 26 i18n locales

### Hardening opportunities
- `SaveBridge` not atomic (write+truncate, not temp+rename) — clientkey is issued once and unrecoverable
- Per-IP auth rate limiter map never pruned — grows unboundedly
- Self-signed cert marked as CA with `KeyUsageCertSign` — unusual for leaf cert
- AppImage continuous channel unsigned with `contents: write` on push to main — no approval gate
- `go install gomobile@latest` in CI — supply-chain risk from unpinned tool version
- Android: any app on the device can reach loopback-server (no per-app loopback isolation)

### Frontend UX issues
- Fetch failures leave pages stuck with no error and no retry
- No data refresh on WS reconnect — stale state after server restart
- Pinch-zoom disabled on every page (`user-scalable=no`)
- Color picker is not keyboard-accessible
- Scene chips and room filter not keyboard-operable
- No Cache-Control strategy for embedded frontend (server serves with no caching headers)

## Test coverage summary

| Package | Tests? | Status |
|---|---|---|
| `internal/hue/` | None | 0% — all bridge communication untested |
| `internal/engine/` | None | 0% — orchestrator untested |
| `internal/lightctl/` | None | 0% — light control untested |
| `internal/config/` | None | 0% — config persistence untested |
| `internal/pipeline/` | Partial | ~60% — smoother.go untested |
| `internal/debuglog/` | None | 0% |
| `internal/server/` | Good | ~70% — WS, TLS, about untested |
| `internal/appconfig/` | Excellent | ~95% |
| `mobile/` | Good | ~80% |

## Commands to reproduce audit environment

```bash
cd /home/luna/projects/huemux
go vet ./...           # clean
gofmt -l ./cmd ./internal ./mobile  # cmd/huemux-desktop/main.go flagged
go test ./...          # all pass
go build ./...         # clean
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...  # clean
```
