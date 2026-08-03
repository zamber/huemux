# Concurrent SelectArea from two WebSocket clients leaks a DTLS stream and goroutines

**Severity:** `high`

**Category:** `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`Engine.SelectArea` is not serialized. Two concurrent `select_area` WebSocket messages from two browser tabs both pass through `e.Stop(ctx)` (the second finds `cancel == nil` and returns immediately), both call `StartStreaming`, the second overwrites `e.stream`/`e.cancel`/`e.loopDone` — orphaning the first stream's DTLS socket and both goroutines (`outputLoop` + `stream.Run`) until process exit.

## Affected Files

- `internal/engine/engine.go:110-196` — `SelectArea` with no serialization
- `internal/server/http.go:738-745` — `discover_bridges`/`pair` run in goroutines, making the race window large
- `internal/server/http.go:798-803` — `select_area` message handler, per-connection

## Evidence

```go
// engine.go:110-196
func (e *Engine) SelectArea(ctx context.Context, areaID string) error {
    e.Stop(ctx)  // T1: goroutine from tab A calls Stop, gets cancel, waits on <-done
    // T2: goroutine from tab B calls Stop while A's Stop is mid-wait
    // T2 finds cancel == nil (already cleared by T1) — returns immediately
    
    cfg, err := e.client.GetEntertainmentConfiguration(ctx, areaID) // T2 fetches
    // ...
    e.client.StartStreaming(ctx, areaID)   // T2 starts streaming
    // T1's old stream is still running — its goroutine and DTLS socket are now orphaned
    
    e.mu.Lock()
    e.stream = stream    // T2 overwrites
    e.cancel = cancel    // T2 overwrites
    e.loopDone = make(chan struct{})  // T2 overwrites
    e.mu.Unlock()
    
    go func() { stream.Run(runCtx) }()    // T2 starts new goroutines
    go e.outputLoop(runCtx)
    // T1's stream, outputLoop goroutine, and DTLS socket are leaked
}
```

## Why It Matters

- The orphaned DTLS socket keeps sending data to the bridge forever (until bridge timeout, ~10s of silence)
- The orphaned `outputLoop` goroutine keeps running forever (blocked on `<-ctx.Done()` which never fires)
- Memory leak: each orphan is ~a goroutine + a DTLS socket + buffer
- The shell (`app.html`) hosts two iframes (lights + sync), making two concurrent connections the default case

## Reproduction

1. Pair HueMux with a bridge
2. Open two browser tabs to the sync page
3. Rapidly click two different entertainment areas in the two tabs
4. Observe via `ss -unp | grep huemux` that extra UDP sockets accumulate

## Suggested Fix

Add a `selectMu sync.Mutex` to `Engine` that serializes the entire `SelectArea`/`Stop` sequence:

```go
type Engine struct {
    // ...
    selectMu sync.Mutex
}

func (e *Engine) SelectArea(ctx context.Context, areaID string) error {
    e.selectMu.Lock()
    defer e.selectMu.Unlock()
    // ... existing implementation
}
```

Also make `Stop` take the same lock.

## Related Issues

- `012-data-race-stream-stats.md` — another concurrency issue in the engine/stream boundary
