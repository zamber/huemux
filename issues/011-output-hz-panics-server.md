# Unvalidated `output_hz` from WebSocket settings panics the server

**Severity:** `critical`

**Category:** `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`Engine.outputLoop` reads `e.settings.OutputHz` (from user WebSocket `settings` message) and passes it to `time.NewTicker(time.Second / time.Duration(hz))`. A large value like `2000000000` produces `time.Second / 2000000000 = 0`, and `time.NewTicker(0)` **panics** with "non-positive interval for NewTicker". This panics the entire process because it happens in the output loop goroutine, not under `net/http`'s recover.

## Affected Files

- `internal/engine/engine.go:315-324` — `outputLoop` reads `OutputHz` with only `<= 0` guard
- `internal/server/http.go:807-813` — `settings` message handler, no validation
- `internal/hue/stream.go:153-160` — `Dial` caps OutputHz at 25, but the engine's ticker bypasses this

## Evidence

```go
// engine.go:315-324
func (e *Engine) outputLoop(ctx context.Context) {
    defer close(e.loopDone)
    
    e.mu.Lock()
    hz := e.settings.OutputHz    // user-controlled via WS "settings" message
    e.mu.Unlock()
    if hz <= 0 {
        hz = 20
    }
    t := time.NewTicker(time.Second / time.Duration(hz))  // PANICS if hz = 2000000000
```

The `AreaSettings` validation path:
1. Browser sends `{"type":"settings","settings":{"output_hz":2000000000}}` over WS
2. `handleControlMessage` → `json.Unmarshal` into `config.AreaSettings` (no validation)
3. `eng.UpdateSettings(settings)` → stores it
4. On next `SelectArea` or engine restart, `outputLoop` reads the stored value → panic

## Why It Matters

- The panic kills the entire process (not just one goroutine)
- `e.loopDone` is never closed, so `Engine.Stop()` blocks forever on `<-done`
- Even non-panicking values in the 1e9 range (1ns interval) peg a CPU core at 100%
- Reachable by any authenticated WS client, which with default config means any local process

## Reproduction

1. Start HueMux paired with a bridge and entertainment area
2. Send `{"type":"settings","settings":{"output_hz":2000000000}}` over the WebSocket
3. Server panics: "panic: non-positive interval for NewTicker"

## Suggested Fix

Clamp `OutputHz` on ingest, not just on use:

```go
// In handleControlMessage "settings" case, after json.Unmarshal:
if settings.OutputHz < 1 {
    settings.OutputHz = 20
} else if settings.OutputHz > 25 {
    settings.OutputHz = 25
}
```

Better: add a `Validate()` method on `AreaSettings` and call it in `handleControlMessage` and `UpdateSettings`. Also clamp in `outputLoop` as defense-in-depth:

```go
hz := e.settings.OutputHz
if hz < 1 || hz > 25 {
    hz = 20
}
```

## Related Issues

None directly, but the broader pattern of trusting WS JSON without validation affects other settings fields too (see `AreaSettings` struct — most numeric fields lack bounds).
