# WebSocket frame size is unbounded — local OOM risk

**Severity:** `critical`

**Category:** `security` / `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`Conn.readFrame` allocates `make([]byte, length)` without any maximum size check. A 64-bit length field on the wire means a malicious local client can instruct the server to allocate up to 2^64 bytes, crashing the process with OOM.

## Affected Files

- `internal/server/ws.go:225` — `length = binary.BigEndian.Uint64(ext)` for 127-extended-length frames
- `internal/server/ws.go:235` — `payload = make([]byte, length)` — no bound check before allocation

## Evidence

```go
// ws.go:212-236
switch length {
case 126:
    ext := make([]byte, 2)
    if _, err = io.ReadFull(c.br, ext); err != nil {
        return false, 0, nil, err
    }
    length = uint64(binary.BigEndian.Uint16(ext))
case 127:
    ext := make([]byte, 8)
    if _, err = io.ReadFull(c.br, ext); err != nil {
        return false, 0, nil, err
    }
    length = binary.BigEndian.Uint64(ext)    // <-- up to 2^64-1
}

// ...

payload = make([]byte, length)               // <-- panic: runtime error: makeslice: len out of range
```

## Why It Matters

The WebSocket server is loopback-only by default, so the attacker must already be on the machine. But:

- The Android build runs in-process, where another app (or a compromised WebView) could connect
- The desktop build (`--headless` or LAN-bound `--listen-host=0.0.0.0`) exposes the WS endpoint
- Even on loopback, a malicious local process or browser tab can crash HueMux trivially

A frame length of 2^31 or larger triggers `runtime: makeslice: len out of range` panic, which kills the whole process. The `recover()` in `debuglog` would catch it only if it propagated through `SetCrashOutput`, which panics in the WS read goroutine bypass.

## Reproduction

```go
// Write a WebSocket frame header claiming 4GiB payload
conn, _ := net.Dial("tcp", "127.0.0.1:7654")
// ... perform WS handshake ...
// Send: FIN=1, opcode=binary, MASK=1, length=127, extended=0x100000000
// The server will OOM or panic.
```

## Suggested Fix

Add a maximum frame size constant and enforce it before allocation:

```go
const maxFrameSize = 16 << 20 // 16 MiB — a grid frame is at most ~12 KiB at current grid sizes

// In readFrame, before make([]byte, length):
if length > maxFrameSize {
    return false, 0, nil, fmt.Errorf("frame too large: %d bytes (max %d)", length, maxFrameSize)
}
```

For grid frames specifically, the size is bounded by `3 + gridW*gridH*3` where `gridW` and `gridH` are the 2 header bytes. Add a secondary check in `handleGridFrame` validating `w*h*3` against a reasonable maximum grid resolution (e.g., 640×360).

## Related Issues

- `002-no-csp-or-security-headers.md` — defense-in-depth would help if the WS endpoint is exposed

## References

- RFC 6455 §5.2 — frame length encoding (7-bit, 7+16-bit, 7+64-bit)
- The current grid at defaults is 320×180 = 57,600 pixels × 3 bytes = 172,800 bytes per frame
