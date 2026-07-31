# Plan 01 — Deployment profiles, centralized config, and network access

Status: **proposed**, not started. Prerequisite for [Plan 02 (Android)](02-android-standalone.md).

## Context

Today HueMux is a single shape: one binary, both features, bound to
`127.0.0.1`, no authentication, no configuration beyond per-area sync tuning.
That is exactly right for the desktop use case it was built for, and wrong for
two others that have since come up:

- **Headless light control on the LAN.** Run HueMux on a server as the single
  place light control lives (`lights.lan`), instead of duplicating Hue logic
  across shell scripts, Home Assistant automations, and one-off tools. This
  needs: no screen-sync machinery running at all, a listen address that isn't
  loopback, and — because that removes the "nothing else can reach it"
  property — an authentication story.
- **Sync-only installs.** Someone who only wants the screen-sync half
  shouldn't get a Lights tab, and shouldn't pay for a bridge eventstream
  subscription they never look at.

Both are the same underlying feature: **the two halves of the app should be
independently switchable**, and there needs to be somewhere to say so.

There is currently **no application-level config mechanism at all** — no flags
beyond `-debug`/`-v`/`--headless`, no config struct, no feature toggles. The
three existing files under `~/.config/huemux` (`config.json` bridge
credentials, `settings.json` per-area sync tuning, `favorites.json`) are all
feature data, not app configuration. So this plan adds that layer.

The good news from exploration: **the Go core is already almost cleanly
split.** `server.New(store, favorites, eng, lights)` at
`internal/server/http.go:67` already accepts `nil` for either the engine or
the lights service, and every handler already degrades to `[]`/`{}`. Most of
this plan is wiring and UI, not surgery.

## Design

### One schema, three layers

A new package `internal/appconfig`, deliberately separate from
`internal/config` (which stays what it is: bridge credentials and feature
data). This is the single source of truth referenced by the CLI, the config
file, the runtime API, the web settings screen, and the Android settings
screen — "centralized and shared by all".

```go
package appconfig

type Config struct {
    Profile Profile `json:"profile"` // full | lights | sync
    Listen  Listen  `json:"listen"`
    Auth    Auth    `json:"auth"`
    TLS     TLS     `json:"tls"`
}

type Listen struct {
    Host string `json:"host"` // default "127.0.0.1"
    Port int    `json:"port"` // default 7654; 0 = scan upward for a free port
}

type Auth struct {
    Mode  AuthMode `json:"mode"`  // none | token
    Token string   `json:"token"` // 3 dot-separated words
    // Loopback is exempt by default so desktop use never sees a login.
    AllowLoopbackUnauthenticated bool `json:"allow_loopback_unauthenticated"`
}

type TLS struct {
    Mode     TLSMode `json:"mode"` // off | selfsigned | files
    CertFile string  `json:"cert_file"`
    KeyFile  string  `json:"key_file"`
}
```

Stored as a fourth file, `~/.config/huemux/app.json` — deliberately *not* a
new key inside `config.json`, so the runtime-mutable settings never share a
write path with the fsync'd bridge credentials (`config.SaveBridge`,
`internal/config/config.go:67`).

Precedence, lowest to highest:

1. Built-in defaults (loopback, port 7654, profile `full`, auth `none`, TLS `off`)
2. `app.json`
3. CLI flags
4. Runtime overrides via the API — **loopback callers only** (see below)

Runtime changes persist back to `app.json`. Changes that require a rebind
(`listen`, `tls`) report `restart_required: true` rather than trying to
re-listen underneath live connections; profile and auth changes apply live.

### Profiles

| Profile | Engine (sync) | lightctl | Notes |
|---|---|---|---|
| `full` (default) | yes | yes | Today's behavior, unchanged |
| `lights` | **no** | yes | Headless server case. No DTLS, no capture, no Sync tab |
| `sync` | yes | yes* | No Lights tab |

\* **`sync` still constructs lightctl**, because the sync page's own scene
strip calls `/api/scenes` and sends `scene_recall` (`web/app.js:236`,
`web/app.js:266`). Dropping lightctl there would silently break a feature on
the page the profile exists to serve. It is a UI-level profile, not a
dependency-level one. To avoid the idle cost, make the bridge eventstream
subscription (`lightctl.Service.Subscribe`,
`internal/lightctl/service.go:258`) lazy — started on first light/room
consumer, not in `setPaired` (`internal/server/http.go:114`).

The asymmetry is intentional and worth stating in the docs: `lights` is a real
resource saving (no DTLS socket, no output loop, no capture plumbing);
`sync` is mostly a UI simplification.

### Auth

Configurable per the three modes, with loopback exempt by default so nothing
about the desktop experience changes.

**Token format:** three dot-separated words from an embedded wordlist, e.g.
`amber.tiger.moon` — readable over the phone, typeable on a TV remote,
memorable. Generated on first start in a non-loopback configuration, printed
once to the log, and settable by the user via flag/config/API.

Entropy note to record in the docs rather than discover later: 3 words from a
2048-word list is ~33 bits. That is fine for a LAN with rate limiting and
**not** fine for direct internet exposure. So: make the word count
configurable (default 3), add fixed-window rate limiting on failed auth, and
use `crypto/subtle.ConstantTimeCompare` for the check. If someone exposes this
to the internet, the docs should point them at a longer token — or better, at
Tailscale (below).

Applies to `/api/*` and the `/ws` upgrade. Accepted as either an
`Authorization: Bearer` header or a `?token=` query parameter — the query
parameter is not optional to support, because browser `WebSocket` cannot set
headers.

### The Origin check has to change, carefully

`checkOrigin` (`internal/server/ws.go:52`) currently hardcodes an allowlist of
loopback hostnames. That is load-bearing security for the desktop case — it is
what stops any website you happen to have open from driving your lights — and
it must stay exactly as strict when bound to loopback.

The change: when a non-loopback listen host is configured, the allowlist gains
that configured host (and nothing else). Not a wildcard, not "any Origin",
not "skip the check when a token is present". Also fix the two existing
defects found while reading it: the stale doc comment referencing a
`localAddr` parameter that does not exist, and the dead `"[::1]"` case at
`ws.go:62` (`u.Hostname()` has already stripped the brackets).

### TLS, and the huemux.com subdomain idea

Answering the question directly, because the intuitive version of it is a trap:

**Shipping a real cert inside the app does not work.** A public DNS record for
`local.huemux.com` → `127.0.0.1` plus a Let's Encrypt cert is technically
functional, but it requires shipping the *private key* in the binary. A
publicly disclosed key must be revoked under CA/Browser Forum rules, it puts
you on a 90-day re-release treadmill, and it only ever covers loopback — which
is already a secure context, so it buys nothing where it works and doesn't
work where you actually need it (the LAN).

**Your own subdomain with DNS-01 is legitimate**, and different from the
above, because you own `huemux.com` and the key never leaves your machine:
point `lights.huemux.com` at the server's LAN IP (a private IP in public DNS
is fine and common for self-hosting), issue via DNS-01 (no inbound
reachability needed), renew with certbot on that box. Costs: DNS provider API
access, a renewal cron, and publishing a little network topology.

**For most self-hosters the best option is Tailscale.**
`tailscale cert <host>.<your-tailnet>.ts.net` issues a real, auto-renewing
Let's Encrypt cert with no DNS plumbing, no port forwarding, and no cert
distribution — and it works from a phone from anywhere, not just at home.
Known caveat: `tailscaled` is unreliable inside LXC containers without
`/dev/net/tun` and needs `userspace-networking` mode there.

**Design conclusion: HueMux stays out of the certificate business.** It
accepts `cert_file`/`key_file` paths, plus a `selfsigned` mode that generates
a long-lived self-signed cert for zero-config LAN use. All three strategies
above then work without HueMux knowing anything about them. Documented
recommendation order: Tailscale → own-domain DNS-01 → self-signed → plain HTTP
on a trusted LAN.

## Files to change

**New:**
- `internal/appconfig/` — the schema above, load/save/merge, defaults, validation, wordlist token generation.
- `internal/appconfig/wordlist.go` — embedded ~2048-word list (short, unambiguous, no near-homophones).
- `internal/server/auth.go` — token middleware, rate limiter, loopback detection.
- `web/settings.html` + `web/settings.js` — the settings screen. Reads/writes `/api/config`; the Android app gets it for free by virtue of being a WebView on the same server.

**Modified — Go:**
- `internal/server/http.go` — construct from `appconfig`; `ListenAndServe` (`:189`) takes host/port and TLS instead of hardcoding `127.0.0.1` at `:195`/`:203`; new `/api/config` GET+PATCH; gate route registration by profile.
- `internal/server/http.go:754` (`runPair`) — **the one real trap.** It currently constructs *both* `engine.New` and `lightctl.New` unconditionally, so pairing from the web UI would silently re-enable a feature the profile disabled. Must respect the profile.
- `internal/server/http.go:402` — `IncUIClient` is called for every WS client, so a lights-only client inflates `Status.CaptureClients`. Gate on the connection actually being a sync client.
- `internal/server/ws.go:52` — Origin allowlist as described; fix stale comment and dead `[::1]` branch.
- `internal/lightctl/service.go:258` — lazy eventstream subscription.
- `cmd/huemux/main.go:322-328` and `cmd/huemux-desktop/main.go:75-81` — parse config, conditionally construct engine/lights. Both already share `server.New` identically, so this is the same small change twice.
- `internal/ui/status.go` — takes `engine.Status` today; needs a lights-only readout. `RenderUnpaired` (`:49`) is the closest existing "nothing to show" precedent.

**Modified — web:**
- `web/shared/header.js:78-86` — nav is a hardcoded two-link string. Fetch `/api/config` (or a slim `/api/features`) and render only enabled tabs, plus a settings link.
- `web/shared/shell.js:9-13,35` — hardcoded frame map, `active = 'sync'` default, and `initial` defaulting to sync. A lights-only build currently lands on a frame that doesn't exist.
- `web/app.html:30-33` — both iframes are hardcoded and both load eagerly.
- **Pairing UI must be extracted.** It lives *only* in `sync.html` + `app.js` (`:104/:168/:178/:183`); `lights.html:31-35` just links to the Sync page. In a `lights` profile there is no Sync page, so **a fresh lights-only install cannot pair at all.** This is the single biggest UI item in the plan — move the pairing panel into `web/shared/` and mount it on whichever page is enabled.

## Phasing

1. **`internal/appconfig` + CLI flags + `app.json`.** No behavior change; `full`/loopback/no-auth defaults reproduce today exactly. Ship this alone and verify nothing moved.
2. **Profiles, server side.** Conditional construction, the `runPair` fix, lazy eventstream, the CLI readout. Verify with `--profile=lights` that no DTLS socket is opened.
3. **Extract pairing into `web/shared/`.** Prerequisite for a usable lights-only build; worth its own commit since it touches the most delicate existing UI.
4. **Profile-aware UI.** `/api/config`, dynamic nav, shell frame handling, settings screen.
5. **Listen address + Origin + auth.** The security-sensitive commit, deliberately separate and last so it can be reviewed on its own.
6. **TLS modes.** `selfsigned` generation and `files` passthrough.

## Verification

- **No regression, default config:** `huemux` with no `app.json` and no flags — pair, sync, control lights, confirm byte-identical behavior to today.
- **Profile isolation:** `huemux --profile=lights` → `ss -ltnp` shows the HTTP port and no DTLS/UDP socket to the bridge; `/api/areas` returns `[]`; the Sync tab is absent; **pairing from a fresh config still works** (the regression this plan is most likely to introduce).
- **Precedence:** set profile in `app.json`, override with a flag, override again via `PATCH /api/config` from loopback, confirm each wins in order and that the last persists across restart.
- **Auth:** with `mode=token` and a non-loopback bind, confirm `/api/lights` and the `/ws` upgrade both 401 without a token and succeed with one; confirm loopback still needs none; confirm a wrong token is rate limited.
- **Origin:** from a non-loopback bind, confirm a WS upgrade carrying an unrelated `Origin` is still rejected — the check must not have been loosened into uselessness.
- **TLS:** `selfsigned` serves HTTPS and the app works past the browser warning; `files` mode works against a real `tailscale cert` pair.
- Existing `go test ./...` (currently only `internal/pipeline` has tests) plus new table tests for `appconfig` precedence/merge and the token check.
