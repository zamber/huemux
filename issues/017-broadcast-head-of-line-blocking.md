# broadcast() blocks all WebSocket clients on the slowest reader

**Severity:** `medium`

**Category:** `performance` / `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`Server.broadcast()` holds `s.mu` while calling `WriteMessage` on every connected WS client sequentially. There is no write deadline. One client that stops reading (e.g., a backgrounded mobile browser tab) fills its TCP buffer, blocks the `WriteMessage` call, and stalls status pushes, frame-source claims, and grid-frame admission for every other client.

## Affected Files

- `internal/server/http.go:246-252` — `broadcast` method
- `internal/server/ws.go:252-280` — `writeFrame` with no write deadline
- `internal/server/http.go:233-241` — `runLightEventBroadcast` calls `broadcast`

## Evidence

```go
// http.go:246-252
func (s *Server) broadcast(raw []byte) {
    s.mu.Lock()                      // holds the lock
    for conn := range s.uiConns {
        _ = conn.WriteMessage(opText, raw)  // blocking TCP write
    }
    s.mu.Unlock()
}
```

```go
// ws.go:252-280 — no deadline
func (c *Conn) writeFrame(opcode byte, payload []byte) error {
    c.writeMu.Lock()
    defer c.writeMu.Unlock()
    // ... no SetWriteDeadline ...
    if _, err := c.bw.Write(head); err != nil { return err }
    if _, err := c.bw.Write(payload); err != nil { return err }
    return c.bw.Flush()
}
```

## Why It Matters

- The `s.mu` lock is also needed by `handleGridFrame` (frame admission), `claimFrameSource`, `stopStreamAndNotifySource`, and `pushStatusLoop`
- One stalled client freezes: frame source claims, light events, favorite events, and all UI status updates
- Under the shell, two iframes = two connections; one dead tab blocks both
- Backgrounded mobile tabs are the most likely cause (browser suspends the JS thread, TCP buffer fills)

## Suggested Fix

Option A: Per-connection buffered outbound channel with a single writer goroutine, drop-on-full:

```go
type Conn struct {
    // ...
    outbox chan []byte  // capacity 16 or so
}

func (c *Conn) writer() {
    for msg := range c.outbox {
        c.WriteMessage(opText, msg)
    }
}
```

Option B: Set a short write deadline before each broadcast write, and evict connections that fail:

```go
conn.rwc.SetWriteDeadline(time.Now().Add(2 * time.Second))
if err := conn.WriteMessage(opText, raw); err != nil {
    delete(s.uiConns, conn) // evict on timeout
}
```

## Related Issues

None directly.
