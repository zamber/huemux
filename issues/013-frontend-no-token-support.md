# Frontend has no way to send auth token — token mode renders the web UI unusable

**Severity:** `critical`

**Category:** `correctness`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The web UI constructs WebSocket connections and API fetch calls without any mechanism to include an auth token. If a user configures `auth.mode=token` (as the Settings page and README recommend for LAN deployments), every API call returns 401, every WebSocket upgrade is rejected, and the UI dead-ends — no login screen, no token field, no recovery path.

## Affected Files

- `web/app.js:53` — `new WebSocket(...)` with no `?token=`
- `web/lights.js:121` — same
- `web/app.js:152,186` — `fetch('/api/areas')` with no `Authorization` header
- `web/lights.js:388,395,401,408` — `fetch('/api/lights')`, etc., no header
- `web/settings.js:71,129,200,320` — same
- `internal/server/auth.go:117-127` — the server supports both `Authorization: Bearer` and `?token=`, but the frontend uses neither

## Evidence

```javascript
// app.js:53 — WS upgrade with no token
new WebSocket(`${proto}://${location.host}/ws`)
```

```javascript
// app.js:152 — API call with no auth header
const resp = await fetch('/api/areas');
```

```go
// auth.go:117-127 — the server expects one of:
func requestToken(r *http.Request) string {
    if h := r.Header.Get("Authorization"); len(h) > 7 && h[:7] == "Bearer " {
        return h[7:]
    }
    return r.URL.Query().Get("token")
}
```

## Why It Matters

README.md:126-130 and the Settings page both steer users toward token authentication for LAN deployments. But the generated path (Settings → switch auth to token → restart) bricks the web UI silently: static assets load (they're unguarded), but the app is dead with no explanation and no way back.

## Suggested Fix

Option A (minimal): Add a token input field to the Settings page. Persist in `sessionStorage`. When set, append `?token=` to the WS URL and send `Authorization: Bearer <token>` on every fetch.

Option B (safe): Block `auth.mode=token` in the Settings UI when `listen.host` is non-loopback until a token is explicitly set, and show a warning that the web UI cannot work under token auth without the fix above.

## Related Issues

- `010-csrf-config-api-post-method.md` — combined with CSRF, an attacker can set a known token then use it
