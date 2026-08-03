# Data race on Stream.Sent and Stream.LastError

**Severity:** `high`

**Category:** `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`Engine.snapshotLocked` reads `e.stream.Sent` and `e.stream.LastError` on the status timer goroutine while the stream's output loop writes the same fields under `s.mu`. The two locks are unrelated (`e.mu` vs `s.mu`), creating a concurrent read/write race.

## Affected Files

- `internal/engine/engine.go:408-415` — reads `e.stream.Sent` and `e.stream.LastError` under `e.mu`
- `internal/hue/stream.go:189-192` — writes `s.LastError` under `s.mu`
- `internal/hue/stream.go:226-228` — writes `s.Sent++` under `s.mu`

## Evidence

```go
// engine.go:408-415 — in snapshotLocked, holding e.mu
if e.stream != nil {
    sent = e.stream.Sent          // race: stream.flush writes this under s.mu
    if e.stream.LastError != nil {
        lastErr = e.stream.LastError.Error()  // race: stream.Run writes this under s.mu
    }
}

// stream.go:189-192 — in Run, holding s.mu
if err := s.flush(); err != nil {
    s.mu.Lock()
    s.LastError = err             // write under s.mu, read under e.mu
    s.mu.Unlock()
}

// stream.go:226-228 — in flush, holding s.mu
s.mu.Lock()
s.Sent++                          // write under s.mu, read under e.mu
s.mu.Unlock()
```

## Why It Matters

- `Sent` is a plain `uint64` — on 32-bit platforms this is a torn read
- `LastError` is an `error` (interface value) — a torn interface read can produce a corrupt type/value pair
- `Snapshot()` is called once per second per connected WS client (status pushes) and from `/api/status`
- The race is benign today on 64-bit platforms for `Sent` (aligned uint64), but `LastError` races are never benign
- `go test -race` won't catch it because no test exercises a live stream

## Reproduction

1. Start HueMux paired with a bridge and active entertainment area
2. Connect two browser tabs (triggers two status-push loops, doubling Snapshot() frequency)
3. Run `go test -race` on a test that exercises this path — no test exists

## Suggested Fix

Add a thread-safe accessor to `Stream`:

```go
func (s *Stream) Stats() (sent uint64, lastErr error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.Sent, s.LastError
}
```

Call this from `snapshotLocked` instead of reading fields directly.

## Related Issues

- `006-missing-test-coverage.md` — this race exists because the stream package has no tests
