# Working on HueMux

Development and verification notes for whoever (or whatever) is changing this
codebase. User-facing docs are in [README.md](README.md); release mechanics are
in [PACKAGING.md](PACKAGING.md); store submission, accounts and the legal
prerequisites are in [PUBLISHING.md](PUBLISHING.md); designs for unstarted work
are in [plans/](plans/).

Two things are unresolved and block distribution: there is **no `LICENSE`
file** (a public repo without one is all rights reserved, so no packager may
redistribute it), and the project name contains **Signify's "Hue"
trademark**. Both are cheaper to settle before there are store listings and an
installed base. See PUBLISHING.md §0.

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

**And the browser has its own stale copy.** `curl` proves what the *server*
holds, which is a different question from what the *page* is running. A tab
that was open before the rebuild will happily keep executing the previous
version of a script from its HTTP cache, and a plain reload does not always
evict it — an iframe reloaded from within the shell reliably does not.

This is the same trap as the embedded-assets one, one layer further out, and it
is worse: it produces a *passing* result for a fix that is not there. It has
already done so once, on a slider change that was then reported working and was
not. When a frontend change appears to work, confirm the page is running it:

```js
// In the frame under test, not the shell.
await fetch('/shared/slider-touch.js', { cache: 'force-cache' }).then(r => r.text())
// vs { cache: 'reload' } — if they differ, the test was against the old file.
```

A `{ cache: 'reload' }` fetch followed by reloading the frame is enough to
recover. Prefer checking for a string only the *new* version contains, since
that fails loudly rather than silently agreeing with you.

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

## UX rules

These are settled decisions, not preferences to relitigate. They come from the
person who uses this on a phone every day; where a rule breaks a platform
convention, that is deliberate and the reason is given.

**Contrast carries meaning, colour does not.** State is shown with weight,
fill, outline and opacity — a selected row is full-strength ink and 600 weight
against muted ink, an active toggle is filled rather than tinted, a disabled
control is dimmed. `--accent` exists but is decoration, never the only thing
distinguishing two states. Anything that reads as "the blue one is on" fails
in the simple theme, in bright sun, and for anyone colour-blind.

**Outlines over fills.** Controls are defined by their border. `.hm-dropdown
> summary` is a 2px ink outline; buttons are outlined until they represent a
running state, at which point they invert to a solid ink fill. This is what
keeps the UI legible against the near-black surface.

**Fira Code everywhere, including form controls.** It is self-hosted in
`web/shared/fonts/` — no CDN, since this runs on a LAN with no guaranteed
internet. Browsers do not inherit `font-family` into `button`/`input`/`select`,
so `shared/theme.css` sets `font-family: inherit` on them explicitly; a new
control that looks subtly wrong is almost always missing that.

**Dropdowns anchor to their trigger — never a bottom sheet.** Use
`shared/dropdown.{css,js}` (`.hm-dropdown` / `.hm-dropdown-panel` /
`.hm-dropdown-item`). On a phone the panel is full-bleed with 44px+ rows like a
sheet, but it opens directly beneath the control that owns it and dismisses on
an outside tap, Escape, or picking an item.

This is a deliberate break with the mobile convention, and the bottom sheet it
replaced is why: it appeared at the opposite end of the screen from the finger
that opened it, so it was easy to miss entirely; it implied a modality it never
had; and it sat beneath the fixed bottom nav, which silently ate the last rows
of a long list. Phones are large enough that the top of the screen is
reachable, and keeping a menu next to its control is worth more than the
shorter thumb travel.

Native `<select>` elements are the exception and stay native — the OS picker is
centred, unobscured, and accessible for free. The rule is about menus this code
draws itself.

**The bottom nav is fixed and everything must clear it.** `shared/header.css`
puts `padding-bottom` on `body` rather than on each page's container, precisely
so a new page cannot forget. Anything positioned near the bottom of the
viewport needs to sit above `z-index: 40` or account for the bar's height.

**Two independent settings get two controls.** Palette (system/light/dark) and
visual effects (on/off) were once a single five-state cycle behind one button;
it read as a random loop and took five taps to undo one. If two things are
orthogonal, model them orthogonally in storage, in the DOM, and in the UI.

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
