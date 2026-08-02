# Publishing

How HueMux gets into app stores and package managers, what has to be true
legally before that can happen, and how the secrets for automated signing and
publishing are held.

[PACKAGING.md](PACKAGING.md) covers how artifacts are **built** and what each
packaging format looks like. This document covers **accounts, store
submission, legal obligations, and release automation** — the parts that need
a human, a payment method, or an identity check.

Ordered easiest to hardest. The early items need no account, no money and no
external approval, and several of them are prerequisites for the later ones —
so the order is also roughly the order to do them in.

> **Verify before you rely on it.** Store policies, fees, and API-level
> deadlines change often, and several of the specifics below (Play's testing
> requirements above all) have changed more than once. Every external
> requirement here should be re-read on the store's own site at the time of
> submission. Where a number is quoted it is a starting point for checking,
> not an authority.

---

## 0. Blockers — nothing ships until these are done

### 0.1 There is no LICENSE file

The repository is public with no licence, which under copyright law means
**all rights reserved**: nobody may redistribute it, and that includes
Flathub, Homebrew, F-Droid and anyone packaging it downstream. Several
channels will refuse the submission outright, and the ones that don't are
relying on a permission that has not been given.

Pick a licence and commit it as `LICENSE` at the repo root. For a project like
this the realistic choices are:

| Licence | Consequence |
|---|---|
| **MIT** / **BSD-2** | Anyone may do anything, including ship a closed fork. Simplest, and what the Go dependencies already use. |
| **Apache-2.0** | As MIT plus an explicit patent grant and a `NOTICE` mechanism. The safer default if patents ever matter. |
| **GPL-3.0** | Forks must stay open. Incompatible with the App Store; fine for Play, F-Droid and Flathub. |

Also add an SPDX identifier to the AppStream metadata (Flathub requires one)
and to the Play listing.

### 0.2 The name contains someone else's trademark

"Hue" is Signify's trademark for lighting products. `HueMux`, the domain
`huemux.com`, and the Play listing all use it.

What is normally fine: saying the app **works with** Philips Hue, in the
description, using the words descriptively. That is nominative use.

What carries real risk: the trademark inside the **product name**. Both Google
and Apple action trademark complaints on request from the holder, usually by
suspending the listing first and asking questions later — which is exactly the
outcome you are trying to avoid by using a separate account, and a separate
account does not protect against it.

Before submitting anywhere:

- Never use the Philips or Hue **logos**, or the "Works with Philips Hue"
  badge. That badge is a certification programme and using it uncertified is a
  straightforward infringement.
- Put a disclaimer in the store listing and the About screen: *"Not affiliated
  with, endorsed by, or sponsored by Signify N.V. Philips and Hue are
  trademarks of Signify Holding."*
- Read the Hue Developer Program terms before relying on the API commercially.
  Local CLIP API use by third-party apps is widespread and tolerated; that is
  not the same as licensed.
- **Decide now whether you are willing to rename.** If the answer is no, be
  aware you are accepting a takedown risk that no amount of account hygiene
  reduces. If yes, renaming is far cheaper before a store listing, an installed
  base and a package-manager entry exist than after.

---

## 1. Legal groundwork (no accounts needed)

### 1.1 Third-party licence attribution

Every dependency's licence must be reproduced in the distributed app. This is
a condition of MIT, BSD and Apache-2.0 alike — attribution is the one thing all
of them require.

What HueMux ships today:

| Component | Licence | Obligation |
|---|---|---|
| `pion/dtls`, `pion/logging`, `pion/transport` | MIT | Reproduce licence + copyright |
| `asticode/go-astilectron`, `go-astikit` | MIT | Reproduce licence + copyright |
| `golang.org/x/crypto`, `golang.org/x/sys` | BSD-3-Clause | Reproduce licence + copyright |
| Go standard library | BSD-3-Clause | Reproduce licence |
| **Fira Code** (`web/shared/fonts/`) | SIL OFL 1.1 | Ship `LICENSE-OFL.txt` (already present); reserved font name — a modified font may not be called Fira Code; the font may not be sold on its own |
| **Electron** (desktop build only) | MIT, plus bundled Chromium (BSD + many) and **ffmpeg (LGPL)** | Reproduce the bundled `LICENSES.chromium.html`; LGPL requires that ffmpeg remain replaceable, which Electron's dynamic linking already satisfies |
| **AndroidX** (Android build) | Apache-2.0 | Reproduce licence and any `NOTICE` |

Generate the file rather than maintaining it by hand — a hand-written list goes
stale the first time a dependency is added:

```sh
go install github.com/google/go-licenses@latest
go-licenses report ./cmd/huemux --template licenses.tpl > THIRD_PARTY_LICENSES.md
# and check nothing copyleft crept in:
go-licenses check ./... --disallowed_types=forbidden,restricted
```

Wire that check into CI so a future dependency with an incompatible licence
fails the build rather than being discovered by a store reviewer.

### 1.2 An About screen with the licences in it

Needed for the attribution above to actually reach users, and expected by
reviewers on every store.

Add to Settings, below Diagnostics:

- App name, version (`main.version`, already baked in via ldflags), and the
  commit it was built from.
- Licence of HueMux itself, with a link to the source.
- **Third-party licences**, in full, on screen. Not a link — a device without
  internet must still be able to show them. Serve `THIRD_PARTY_LICENSES.md`
  from the embedded `web/` directory the same way every other asset is served.
- The trademark disclaimer from §0.2.
- A link to the privacy policy.

### 1.3 Privacy policy

Google Play requires a privacy policy URL **even for an app that collects
nothing**, and the Data safety form must agree with it. `docs/` is already a
GitHub Pages site on `huemux.com`, so this is a new page there.

It has to state honestly what HueMux does, which is unusually easy here:

- No analytics, no telemetry, no crash reporting, no advertising ID.
- No account, no personal data leaves the device.
- All traffic is local: the phone talks to the Hue bridge on the LAN.
- **Screen capture** happens only while the user starts it, is shown by a
  persistent system notification, and the captured pixels are reduced to
  colour averages and sent to the bridge — never to any server.
- **Recordings** are written to the device's own Movies folder at the user's
  request and are never uploaded.
- Diagnostics reports are generated on demand and shared only if the user
  chooses to share them.

Be exact about the screen capture. Understating it is the kind of mismatch
between policy and behaviour that gets an app pulled.

### 1.4 Export compliance (encryption)

The app uses DTLS and TLS. Both Google and Apple ask about encryption on every
submission. Using standard cryptography for a non-cryptographic purpose is
normally exempt from US EAR reporting, but **the declaration is still
mandatory** — answer it, do not skip it. If in doubt about the exemption,
this is the one item on the list where a lawyer is cheap relative to the
downside.

---

## 2. Channels, easiest to hardest

### 2.1 GitHub Releases — done

Already automated. `release.yml` builds every platform on a `v*` tag and
attaches the binaries. Everything below is a layer on top of this.

### 2.2 F-Droid — easiest real store, and no Google involvement

The best first store for this app, and the one that sidesteps the entire
account-ban question.

- No account, no fee, no identity check.
- **F-Droid signs the APK itself**, so no keystore secret is needed and no
  signing key can be lost.
- It builds from source, which HueMux already supports.

What it needs:
- The `LICENSE` file from §0.1. F-Droid will not accept the app without it.
- A metadata file submitted to `fdroiddata` describing the build.
- No proprietary dependencies. HueMux qualifies — there is no Firebase, no
  Play Services, no closed SDK.
- Fastlane-structured listing text and screenshots in the repo.

The build is the awkward part: F-Droid's builders must run `gomobile bind`,
which needs the NDK. This is solvable but is the main effort here.

### 2.3 Scoop (Windows) — no signing, no account

A manifest JSON in a bucket repo, pointing at the release `.exe` and its
SHA256. Users get `scoop install huemux`. Publishable today, needs nothing
that does not already exist. Scoop does not require code signing.

### 2.4 Homebrew tap (macOS + Linux) — no approval needed

A `homebrew-huemux` repo with a formula. See PACKAGING.md for the formula
itself. Users add the tap explicitly, so there is no review and no notability
requirement.

**Homebrew core** is a different matter and belongs in the hard section —
it has notability thresholds and requires the cask to be signed and notarized.

### 2.5 AppImage — no store at all

Covered in PACKAGING.md. Publish alongside the GitHub Release, optionally with
zsync for delta updates. No signing required, though a detached GPG signature
is good manners.

### 2.6 winget (Windows) — wants signing

A manifest PR to `microsoft/winget-pkgs`, validated automatically. It will
accept unsigned installers, but SmartScreen will warn users on first run until
the binary builds reputation. Better done after §2.9.

### 2.7 Flathub — moderate, no money

Needs, beyond PACKAGING.md's manifest:

- A reverse-DNS app ID. `com.huemux.HueMux` if the domain is yours, which it
  is.
- **AppStream metainfo XML** with a summary, description, screenshots,
  `<content_rating>`, and the SPDX licence from §0.1. Flathub validates it.
- **Offline builds.** Flatpak builds have no network, so Go modules must be
  vendored (`go mod vendor`, committed) and Electron's download must be
  pre-fetched as declared sources. The Electron half is the real work; the
  plain Go build is straightforward.

Flathub reviews submissions by hand. Expect a round of comments.

### 2.8 macOS notarization — $99/year

Required before a Homebrew cask is pleasant to install, and before anyone can
run the desktop app without right-click-Open gymnastics.

- Apple Developer Program membership.
- A Developer ID Application certificate.
- `codesign` the app and the embedded Electron framework, then `notarytool
  submit --wait`, then `xcrun stapler staple`.
- Hardened runtime, with an entitlement for screen recording if the desktop
  build ever captures on macOS.

### 2.9 Windows code signing — money and identity

SmartScreen shows a scary warning on unsigned executables. Options, cheapest
first:

- **Azure Trusted Signing** — pay-as-you-go, certificate managed by Microsoft,
  no hardware token, and it works from CI cleanly. The best fit for this
  project by a distance.
- **OV certificate** — must now live on a hardware token (since the 2023 key
  storage rules), which makes CI signing painful.
- **EV certificate** — immediate SmartScreen reputation, most expensive, also
  hardware-bound.

All of them require verifying a legal identity or business.

### 2.10 Homebrew core — notability gate

Requires the project to be reasonably well known (stars/forks/watchers
thresholds, actively maintained, versioned releases) and, for a cask, signed
and notarized macOS builds. Come back to this once §2.8 is done and the
project has users.

### 2.11 Google Play — the hardest, and the one you asked about

See §3.

---

## 3. Google Play, and the account question

### 3.1 A new account does not reliably protect the old one

This needs saying plainly, because the plan depends on it.

Google's developer terms allow them to terminate **associated** accounts, not
just the offending one. Association is inferred from payment instruments,
device fingerprints, IP addresses, recovery phone numbers and email addresses.
So:

- A separate email alone associates immediately if it shares a payment card, a
  phone number for verification, or a recovery address with the personal one.
- Deliberately creating accounts to evade an enforcement action is itself a
  terms violation, and is treated more harshly than whatever caused the
  original problem.

What genuinely reduces linkage — and it reduces, it does not eliminate:

- A distinct Google account created fresh, never signed into alongside the
  personal one.
- A distinct payment method not tied to the personal account.
- A distinct phone number for verification.
- Ideally a **company** rather than a person as the account holder, which also
  puts a legal entity between you and a dispute. This requires a D-U-N-S
  number and takes weeks.

The honest summary: the risk you are trying to avoid is mostly avoided by not
tripping policy in the first place, and the highest-probability trip for this
app is the **trademark in the name** (§0.2), which a separate account does
nothing about. If avoiding Google entirely is acceptable, **F-Droid (§2.2) is
the answer** and costs nothing.

### 3.2 What Play requires, specifically

Re-verify each of these at submission time.

**Account setup**
- One-time registration fee (US$25 at the time of writing).
- Identity verification: government ID and address, for a personal account.
  D-U-N-S number and more for an organisation account.
- A public developer name, email and address shown on the listing. For a
  personal account **this is your home address unless you use an
  organisation** — a real reason to consider a company.

**Testing gate**
- Personal accounts created after roughly late 2023 must run a closed test
  with a minimum number of opted-in testers (12 has been the figure) for a
  continuous period (14 days has been the figure) before production access is
  granted. Plan for this: it needs a dozen real Google accounts willing to
  install the app and leave it installed.

**Technical**
- Target API level within Google's supported window — currently the app
  targets 35, which is aligned, but the deadline moves annually.
- **App Bundle (.aab)**, not APK, for new submissions. `release.yml` currently
  produces an APK; it needs a `bundleRelease` variant.
- **Play App Signing.** Google holds the app signing key; you keep an upload
  key. This is good news for §4 — losing the upload key is recoverable, unlike
  losing a self-managed signing key.
- 64-bit only is already satisfied (arm64).

**Declarations, all of which must match reality**
- **Foreground service types**: `FOREGROUND_SERVICE_MEDIA_PROJECTION` requires
  a written justification in Console, with a video demonstrating the feature.
  This is the single most likely place for a rejection.
- **Data safety form**: consistent with §1.3.
- **Privacy policy URL**: §1.3.
- **Screen capture disclosure**: the app must not capture covertly. HueMux is
  fine — the system consent dialog and the persistent notification do the work
  — but the listing must describe screen sync prominently.
- **Encryption declaration**: §1.4.
- **Content rating questionnaire**.

---

## 4. Secrets and automated publishing

### 4.1 Principles

- **Nothing that can publish is reachable from a pull request.** Store
  credentials go in a GitHub **Environment** (`release`) with a protection
  rule, not in plain repository secrets. Environment secrets are only readable
  by jobs that declare `environment: release`, and a fork PR cannot.
- **Require a manual approval** on that environment. A tag push then builds
  everything and *waits* for a human before anything is published to a store.
  This is the difference between a mistyped tag being an annoyance and being a
  release.
- **Split the workflow**: `build` (no secrets, runs on every tag and PR) →
  `sign` (signing secrets) → `publish` (store credentials). A failure in
  publishing must never mean rebuilding unsigned.
- **Never `echo` a secret**, and be careful with `set -x`. GitHub masks known
  secret values in logs, but not derived ones — a base64 decode of a keystore
  into a logged path defeats it.
- Prefer **OIDC** where the target supports it, so no long-lived credential
  exists at all. Google Cloud supports Workload Identity Federation, which
  covers Play publishing via a service account without storing a JSON key.

### 4.2 The secrets, by channel

| Secret | Channel | Notes |
|---|---|---|
| `ANDROID_KEYSTORE_B64` | Android | Already wired (PACKAGING.md). With Play App Signing this is the *upload* key. |
| `ANDROID_KEYSTORE_PASSWORD`, `ANDROID_KEY_ALIAS`, `ANDROID_KEY_PASSWORD` | Android | Already wired |
| `PLAY_SERVICE_ACCOUNT_JSON` | Play | Service account with "Release manager" only. Prefer OIDC instead. |
| `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_SUBSCRIPTION_ID` | Windows | Azure Trusted Signing; use OIDC, no client secret |
| `APPLE_DEVELOPER_ID_P12_B64`, `APPLE_P12_PASSWORD` | macOS | Imported into a temporary keychain, deleted in a cleanup step |
| `APPLE_API_KEY_ID`, `APPLE_API_ISSUER`, `APPLE_API_KEY_B64` | macOS | App Store Connect API key for `notarytool`, better than an app-specific password |
| `HOMEBREW_TAP_TOKEN` | Homebrew | Fine-grained PAT, write access to the tap repo **only** |
| `FLATHUB_TOKEN` | Flathub | Only if automating the manifest bump |

### 4.3 Publishing on a version bump

The trigger stays what it is today — pushing a tag. What changes is what
happens after the build:

```
tag v0.3.0 pushed
      │
      ├─ build      (no secrets)      binaries, .aab, .apk, AppImage
      ├─ sign       (signing secrets) Windows, macOS notarization, Android upload key
      │
      └─ publish    (environment: release, manual approval)
             ├─ GitHub Release          — always
             ├─ Play internal track     — pre-release tags (v*-alpha.*, v*-beta.*)
             ├─ Play production         — final tags only, staged rollout
             ├─ Homebrew tap bump       — PR to the tap repo
             ├─ winget / Scoop manifest — PR
             └─ Flathub manifest bump   — PR
```

Two rules worth encoding in the workflow:

- **Pre-release tags never reach a production track.** The tag pattern already
  distinguishes them (`release.yml` treats a hyphen as a pre-release), so key
  the Play track off the same test.
- **Play rollouts start staged**, not at 100%. A bad release reaching everyone
  at once is unrecoverable; a 10% rollout is not.

---

## 5. Suggested order

1. `LICENSE` file — §0.1. Blocks almost everything else.
2. Decide the trademark question — §0.2. Cheapest to act on now.
3. `THIRD_PARTY_LICENSES` generation + CI licence check — §1.1.
4. About screen — §1.2.
5. Privacy policy page on huemux.com — §1.3.
6. Scoop manifest + Homebrew tap — §2.3, §2.4. Quick wins, no gatekeepers.
7. F-Droid — §2.2. The first real store, and no Google account involved.
8. Split the release workflow and add the `release` environment — §4.
9. Flathub — §2.7.
10. Windows signing via Azure Trusted Signing — §2.9, then winget — §2.6.
11. macOS notarization — §2.8, then a Homebrew cask.
12. Google Play — §3. Last, because it is the most gated and the most exposed
    to the trademark question.
