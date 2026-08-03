# CSRF on `/api/config` — any website can rewrite the persisted configuration

**Severity:** `critical`

**Category:** `security`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

`/api/config` accepts `POST` (a simple CORS method with no preflight) alongside `PATCH`. A malicious web page can `fetch('http://127.0.0.1:7654/api/config', {method:'POST', body: '...', headers:{'Content-Type':'text/plain'}})` — the browser sends this as a "simple request" with no CORS preflight. `isLoopbackRequest` sees `RemoteAddr` = `127.0.0.1` (genuinely) and allows the write. The response is unreadable cross-origin, but the side effect persists. A malicious page can disable auth, switch to LAN binding, or set a known token.

## Affected Files

- `internal/server/config_api.go:51-53` — `case http.MethodPatch, http.MethodPost:` accepts POST
- `internal/server/config_api.go:86-131` — `patchConfig` checks loopback but not Origin
- `internal/server/http.go:300` — route registration: `s.mux.HandleFunc("/api/config", s.guard(s.handleConfig))`
- `internal/server/ws.go:62-108` — `checkOrigin` exists but only protects `/ws`

## Evidence

```go
// config_api.go:51-53
case http.MethodPatch, http.MethodPost:  // POST is a simple method — no CORS preflight
    s.patchConfig(w, r)
```

```go
// config_api.go:86-90
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
    if !isLoopbackRequest(r) {  // 127.0.0.1 passes because the browser really is on loopback
        http.Error(w, "configuration can only be changed from the local machine", http.StatusForbidden)
        return
    }
```

```javascript
// Attacker page on any origin:
fetch('http://127.0.0.1:7654/api/config', {
    method: 'POST',
    body: JSON.stringify({
        listen: { host: "0.0.0.0" },
        auth: { mode: "none" }
    }),
    headers: { 'Content-Type': 'text/plain' }  // NOT application/json — simple CORS
});
// Response unreadable cross-origin, but write already persisted to disk.
```

## Why It Matters

- The attack requires the user to be running HueMux and visit a malicious page. 
- On next restart, the server binds all interfaces with no authentication
- `POST` is the simple method here — `PATCH` would trigger a CORS preflight which the server would reject (no CORS headers set)
- `json.Decoder` doesn't care about Content-Type, so `text/plain` works fine

## Reproduction

1. Start HueMux with default config (loopback, no auth)
2. Open a browser page served from `http://attacker.example` containing the fetch above
3. Check `~/.config/huemux/app.json` — the malicious config was written

## Suggested Fix

1. **Drop POST support** — only accept PATCH (which triggers CORS preflight)
2. **Require a custom header** — `X-Requested-With: XMLHttpRequest` or enforce `Content-Type: application/json` so the browser sends a preflight
3. **Add Origin check** — reuse `checkOrigin` logic in `patchConfig` before applying changes
4. Add a regression test simulating a cross-origin request from `evil.example`

## Related Issues

- `009-no-security-headers.md` — CSP would add defense-in-depth
