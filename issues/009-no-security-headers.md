# No Content-Security-Policy or security headers on HTTP responses

**Severity:** `medium`

**Category:** `security`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The HTTP server does not set Content-Security-Policy, X-Content-Type-Options, X-Frame-Options, or any other security headers. While the server binds loopback by default, when exposed to a LAN (`--listen-host=0.0.0.0`), the web UI is served without browser-side security controls.

## Affected Files

- `internal/server/http.go:275-303` — route handler setup, no middleware for security headers
- `internal/server/http.go:311-369` — `ListenAndServe`, no response header configuration

## Evidence

```go
// http.go:270-303 — routes are registered with no header middleware
func (s *Server) routes() {
    webFS, err := fs.Sub(huemux.WebFS, "web")
    // ...
    s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path == "/" {
            http.Redirect(w, r, "/app.html", http.StatusFound)
            return
        }
        fileServer.ServeHTTP(w, r)
    })
    // API routes registered with s.guard() only
}
```

No middleware wraps responses to add:
- `Content-Security-Policy`
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: no-referrer`
- `Permissions-Policy`

## Why It Matters

The loopback binding is the primary security control, but defense-in-depth matters:

1. If the server is exposed to LAN for multi-device use (phone + desktop), other LAN devices can reach the web UI
2. The WebView in the Android app is immune to most browser attacks, but the desktop build (`huemux-desktop`) loads the UI in a real Chromium
3. Without `X-Content-Type-Options: nosniff`, a MIME-type confusion attack is possible if an attacker can control any served content
4. Without CSP, an XSS in the frontend (unlikely but possible via innerHTML usage) has free rein

## Suggested Fix

Add a response header middleware that sets:

```
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; font-src 'self'
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
```

The CSP `connect-src` needs `ws:` and `wss:` for the WebSocket connection to the local server.

This middleware should be applied to all responses, including static assets. The `guard` middleware already wraps API routes — a `headers` middleware can compose the same way.

## Related Issues

- `001-websocket-frame-size-unbounded.md` — defense-in-depth

## References

- [MDN: Content-Security-Policy](https://developer.mozilla.org/en-US/docs/Web/HTTP/CSP)
- [Google Web Fundamentals: Security headers](https://web.dev/security-headers/)
