# Working on HueMux

Development and verification notes for whoever (or whatever) is changing this
codebase. User-facing docs are in [README.md](README.md); release mechanics are
in [PACKAGING.md](PACKAGING.md); designs for unstarted work are in
[plans/](plans/).

## Commands

```bash
make dev            # build ./huemux
make dev-desktop    # build ./huemux-desktop (Electron wrapper)
go test ./...       # tests
go vet ./...        # vet
gofmt -l ./cmd ./internal   # must print nothing
```

The release workflow runs `go vet` and `go test` before publishing, so a
failure there blocks a release rather than shipping a broken artifact.

`go test -race` works but takes ~45s here, nearly all of it compiling the
instrumented binary on two cores. That is slow enough to look like a hung test
if you run it under a two-minute timeout and assume the worst — it already has
once. Give it room, or run it in the background.

## Android

The whole core targets Android, and you can prove it without any SDK:

```bash
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...
```

That is worth running after touching anything in `internal/` — it is what
catches a dependency that turns out not to be pure Go, which is the realistic
way Android support breaks. `mobile/` is the gomobile facade and is ordinary
Go, so `go test ./mobile/...` covers it.

What you **cannot** do locally is `gomobile bind`: it needs CGO and the NDK,
and the SDK+NDK is 8–12 GB against a host with well under a gigabyte free.
The AAR is built by `.github/workflows/android.yml` on a runner, where both
are preinstalled. Do not try to install the SDK here.

## The frontend is embedded — rebuild after every `web/` edit

`assets.go` does `//go:embed web`, so the whole frontend is baked into the
binary at build time. A running server serves the `web/` that existed when it
was built.

**Editing a file under `web/` and reloading the browser does nothing.** The
failure mode is nasty because it is indistinguishable from a broken change: the
page loads fine, the old behavior persists, and the obvious conclusion is that
the fix is wrong. This has already cost one debugging session chasing a
"non-working" fix that was correct and simply not being served.

The loop for any HTML/CSS/JS change:

```bash
make dev && ./huemux            # rebuild, then restart
```

Before concluding a frontend change is broken, **verify the server is serving
it** — ask the server, not the filesystem, since `grep`ping `web/` only proves
what you wrote, not what is running:

```bash
curl -s localhost:7654/shared/pairing.js | grep -c _hasI18n
```

Zero means you are testing a stale binary. This check costs a second and
rules out the most likely explanation first.

Restarting also drops every WebSocket, so reload the page rather than trusting
a tab that was open across the restart.

## Verifying behavior

The Go side has unit tests; the parts most likely to break do not, so drive
them.

- **Profiles** (`--profile=lights|sync|full`) — the meaningful check is
  whether the sync engine really is absent, not whether a tab is hidden:
  `ss -unp | grep <pid>` should show no UDP socket under `--profile=lights`.
- **Config precedence** — defaults → `app.json` → explicitly-passed flags. The
  regression to watch for is an *unset* flag clobbering a file value with its
  zero value; `internal/appconfig` has a test for exactly that, mutation-checked
  by swapping `Visit` for `VisitAll`.
- **Frontend** — drive it in a browser. Two things that pass a visual check and
  fail in use: language switching (dynamic strings set from JS do not move when
  only `data-i18n` attributes are re-applied) and anything depending on the
  server's own state pushes.

When testing against a real bridge, prefer read-only operations and events that
never reach the socket. Intercepting a component's outbound events in the
capture phase with `stopPropagation` exercises the whole contract without
touching real hardware.

Run each server instance on a known port and confirm the URL it printed. Two
test servers will silently take 7654 and 7655, and curling the wrong one
produces confident, wrong conclusions.

## Conventions worth not rediscovering

- `internal/config` owns feature data (bridge credentials, per-area tuning,
  favorites). `internal/appconfig` owns application configuration. They are
  separate because `config.SaveBridge` fsyncs a clientkey the bridge issues
  exactly once — runtime-mutable settings must never share that write path.
- Frontend globals are declared `const` at the top level of classic scripts,
  which means **they are not properties of `window`**. Guard with
  `typeof HueMuxI18n !== 'undefined'`, never `window.HueMuxI18n`. The one
  genuine exception is `HueMuxShell`, which `shell.js` assigns to `window`
  explicitly.
- `server.New` tolerates a nil engine and/or nil lights service. That predates
  profiles — "not paired yet" always produced the same shape — which is why
  profile support is mostly wiring rather than surgery.
- Both `cmd/huemux` and `cmd/huemux-desktop` construct paired services through
  `server.BuildPaired`, as does web-driven pairing. Add a service in one place
  or the paths will disagree about what a profile means.
- The repo is public. Do not commit LAN addresses, tailnet names, internal
  hostnames, or paths to private infrastructure notes.
