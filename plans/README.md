# Plans

Design documents for work that hasn't started yet. Each is a proposal to be
argued with, not a commitment — they exist so the thinking survives between
sessions and so the tricky parts are found before the code is written, not
during.

Kept out of `docs/` on purpose: that directory is the GitHub Pages source for
huemux.com, and anything with a `.md` extension there becomes a published
page.

| Plan | Status | Summary |
|---|---|---|
| [01 — Deployment profiles, config, and access](01-config-profiles-and-access.md) | proposed | Run only the half you need (`--profile=lights\|sync\|full`), a centralized config schema shared by CLI/file/runtime API/settings UI, and the listen-address, auth, and TLS work needed to expose HueMux beyond loopback. |
| [02 — Standalone Android app](02-android-standalone.md) | proposed | Ship the Go core on the phone via `gomobile bind` with a WebView front end — the same trick `huemux-desktop` plays with Electron. Lights control first, `MediaProjection` screen sync as a later milestone. |
| [03 — Execution phases](03-execution-phases.md) | **phase 1 in progress** | The sequenced work breakdown across 01 and 02, with acceptance criteria and running status. Start here to see what's actually being worked on. |

## Order

Plan 01 first. Plan 02's M1 can start in parallel, but its settings screen
(M2) depends on 01's config schema, and 01's profile work is what makes a
lights-only build — which is what the phone actually wants — coherent.
[Plan 03](03-execution-phases.md) is the authoritative running order.

## Two findings worth not re-deriving

**No mobile browser supports `getDisplayMedia()`.** Verified against current
compatibility data: Chrome Android, Firefox Android, Safari iOS, Samsung
Internet, and Android WebView are all unsupported. There is no web path to
screen capture on a phone, so any streaming-from-Android has to go through
native `MediaProjection`. This is a permanent constraint, not a gap waiting on
a newer WebView.

**The core has zero knowledge of its wrappers.** `grep -rn "electron"
internal/ cmd/huemux/` returns only comments. `huemux-desktop` is a second
`main` that starts the ordinary loopback server and points a window at it —
which is why the Android app can be the same shape, and why neither plan
requires reimplementing any Hue logic.
