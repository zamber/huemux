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

### 0.1 The LICENSE file — GPL-3.0-or-later (done 2026-08-02)

The repository ships [LICENSE](LICENSE) under **GPL-3.0-or-later**, and that
closes the distribution blocker. The state that made it a blocker is worth
remembering: a public repo with no licence means **all rights reserved**
under copyright law — nobody may redistribute it, F-Droid, Flathub and any
downstream packager included — and several channels refuse such submissions
outright. With the file in place, that gate is closed.

#### Why GPL-3.0-or-later

For a project you want in the FOSS ecosystem and do not want quietly
repackaged, **GPL-3.0-or-later** is the right fit, and the dependency set
allows it.

| Option | Fit here |
|---|---|
| **GPL-3.0-or-later** | **Recommended.** A distributed fork must publish its source. Compatible with every dependency below. F-Droid's natural licence. |
| AGPL-3.0 | GPL plus the network clause. Worth considering only because HueMux *is* a server — it would bind someone who hosted it as a service. Scares off some contributors, and hosting this is an unlikely threat for a LAN app. |
| GPL-2.0-only | **Do not.** AndroidX is Apache-2.0, which is incompatible with GPLv2 (though fine with GPLv3). Choosing v2 would create a real conflict. |
| MIT / Apache-2.0 | Simplest, and what the dependencies use — but permits exactly the closed repackaging you want to prevent. |

Compatibility is fine: MIT, BSD-3 and Apache-2.0 dependencies can all be
combined into a GPLv3 work, and Electron's LGPL ffmpeg is compatible too.

#### What a licence does and does not protect against

Your instinct is right that a licence does not stop a rewrite. Copyright covers
the *expression*, not the idea, and a clean reimplementation has always been
legal — machine assistance changes the cost of that, not the law.

But that is not the threat that actually happens. What happens to popular FOSS
apps is **verbatim repackaging**: someone takes the APK, bolts on ads or
tracking, and publishes it. Against that, GPL is effective and easy to act
on — the copied code is identical, so infringement is trivial to demonstrate,
and a takedown needs no lawsuit.

Worth knowing: **the name protects against repackaging better than the licence
does.** A GPL fork is entitled to the code but never to the trademark, so it
cannot be called HueMux. That is the lever that removes a malicious clone from
a store fastest, and it is an argument for a distinctive name that is
unambiguously yours — see §0.2.

### 0.2 The name contains someone else's trademark

"Hue" is Signify's trademark for lighting products. `HueMux`, the domain
`huemux.com`, and any store listing all use it.

**In practice this is widely tolerated.** Play carries a long list of
third-party apps with "Hue" in the name — Hue Essentials, iConnectHue, All 4
Hue and others have shipped for years. Signify has not gone after the category,
and describing an app as working with Philips Hue is ordinary nominative use.
So the earlier framing here was too alarmed: this is a tail risk, not a
likelihood.

What actually gets apps pulled is narrower, and worth avoiding precisely:

- **Logos and badges.** Never use the Philips or Hue logos, or the "Works with
  Philips Hue" badge. That badge is a certification programme; using it
  uncertified is straightforward infringement and is the kind of thing that
  does draw a complaint.
- **Implying endorsement.** Keep the listing and the About screen clear:
  *"Not affiliated with, endorsed by, or sponsored by Signify N.V. Philips and
  Hue are trademarks of Signify Holding."*
- **Naming that reads as first-party.** "Hue Sync" is Signify's own product
  name. A name that could be mistaken for theirs is far riskier than one that
  merely contains the word.

`HueMux` is a coined compound and reads as third-party, which is the safer end
of this. Two practical notes rather than a warning:

- Publishing to FOSS channels first (§2A) carries essentially none of this
  risk — F-Droid and Flathub do not field trademark complaints the way Play
  does — so the question can be deferred until a Play listing is actually on
  the table.
- If HueMux is ever going to be the long-term name, registering it as a
  trademark in your jurisdiction is what makes §0.1's anti-repackaging argument
  real. That is a cheap filing compared to what it defends.

Read the Hue Developer Program terms before relying on the API commercially.
Local CLIP API use by third-party apps is widespread and tolerated; that is not
the same as licensed.

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

## 2A. FOSS-first channels — do these

Free-software channels, in the order they cost you effort. None of them wants
money, an identity check, or a trademark opinion, and there is a genuine gap
here: the FOSS Hue app selection is thin.

§0.1's `LICENSE` file already exists, so the legal gate is closed.

### 2A.1 Obtainium — works today, zero effort

Obtainium installs Android apps straight from GitHub Releases and tracks
updates from them. Nothing to submit and nothing to package: `release.yml`
already attaches an APK to every tag, which is the entire requirement.

The only change worth making is dropping the debug-signed APK for a
consistently signed one (PACKAGING.md), because Android refuses to upgrade
across a signature change. Do that once and existing installs upgrade forever.

**Add an "Install with Obtainium" line to the README today.** It is the
cheapest distribution this project will ever get.

### 2A.2 IzzyOnDroid — the low-friction F-Droid repo

A well-known third-party repository that F-Droid clients can add, and by far
the easiest route into the F-Droid ecosystem: **it takes the APK you already
build**, rather than building from source on its own infrastructure.

- No reproducible build required, which is what makes the main F-Droid repo
  hard for a `gomobile` project.
- Requires a FOSS licence, a public source repo, and no proprietary
  dependencies — all true now.
- Reads Fastlane metadata from the repo, which is in `fastlane/` (see
  `fastlane/README.md`).

**Ready (as of 2026-08-02):** licence, signed releases with a stable key,
an incrementing `versionCode`, a real launcher icon, the 512x512
`icon.png`, listing text in English and Polish, and screenshots in
`fastlane/metadata/android/en-US/images/phoneScreenshots/` (taken by the
user on-device; the earlier candidates showed the bridge's LAN address).

**Submission location (updated 2026-08-02):** the old
<https://gitlab.com/IzzyOnDroid/repo> is **archived**; app inclusion
requests now go to the issue tracker at
<https://codeberg.org/IzzyOnDroid/repodata/issues> — template "App
Inclusion Request" (`app-inclusion-request.yaml`). Fields: source code
URL, licence, categories, summary/description, CLI build instructions,
and an AI-assistance declaration (the project is Claude-Code-built —
disclose it, as the app's own tests and release history attest).

**Reviewer checklist** (what they check against the repo):
- `short_description.txt` under 80 chars — currently 73 ✓
- sensible `full_description.txt`, properly proportioned listing ✓
- releases tagged with names matching versionName or versionCode ✓
- a developer-attached `.apk` on the most recent release, under 30 MB —
  currently 15.7 MB ✓

Their scanner also reports non-free trackers and libraries; HueMux has
none, and it is worth keeping that true — an analytics SDK would be
flagged publicly on the listing.

Once accepted, users add the IzzyOnDroid repo in their F-Droid client and
HueMux updates like any other app.

### 2A.3 Flathub — the Linux desktop answer

The de-facto Linux app store, and FOSS-friendly by construction. See §2C.3 for
the build detail; the effort is Electron's offline sources, not the policy.

### 2A.4 F-Droid main repo

The flagship, and the one with the strongest guarantees: builds from source on
their infrastructure, **signs the APK itself** (so there is no key to lose),
and needs no account or fee.

The cost is that their builders must run `gomobile bind`, which needs the NDK
and a build recipe that works unattended. That is the real work, and it is why
IzzyOnDroid comes first — it gets the app to F-Droid users while this is
sorted out.

### 2A.5 AUR (Arch)

A `PKGBUILD` in the Arch User Repository. Community-maintained, no review, no
account beyond an AUR login. Cheap, and Arch users are a meaningful share of
the audience for a self-hosted LAN tool.

### 2A.6 Accrescent

A newer, security-focused Android store built around modern signing. Small
audience today, low submission cost, and philosophically aligned. Worth doing
after IzzyOnDroid, not before.

---

## 2B. No-gatekeeper channels

Not FOSS institutions, but nobody reviews or approves — a manifest in a repo
and you are done. Cheap wins that also prove the release artifacts are
consumable.

### 2B.1 Scoop (Windows)

A manifest JSON pointing at the release `.exe` and its SHA256. No signing
required. Publishable today.

### 2B.2 Homebrew tap (macOS + Linux) — done 2026-08-02

**<https://github.com/zamber/homebrew-huemux>** — live. Two formulas:
`huemux` (plain server) and `huemux-desktop` (GUI), each installing the
signed release binary for the right OS/arch with `using: :nounzip`.
Users: `brew tap zamber/huemux && brew install huemux-desktop`.

**Auto-bump:** the tap's `.github/workflows/auto-bump.yml` runs every six
hours and on demand. `scripts/bump.py` reads the newest release from the
GitHub API, and rewrites version, asset URLs and SHA256s from the
release's `SHA256SUMS` (no binary downloads). Hashes are reconciled even
when the version is current, so a force-pushed release cannot leave the
tap serving a stale checksum. Uses only the workflow's own
`GITHUB_TOKEN` — no cross-repo secrets.

Not to be confused with Homebrew core (2C.2), which has review and a
notability bar.

### 2B.3 AppImage

Covered in PACKAGING.md. Published alongside the GitHub Release, optionally
with zsync for delta updates. No signing required, though a detached GPG
signature is good manners.

---

## 2C. Channels with real gates

Everything below wants money, an identity check, or a human reviewer.

### 2C.1 winget (Windows)

A manifest PR to `microsoft/winget-pkgs`, validated automatically. Accepts
unsigned installers, but SmartScreen warns users until the binary builds
reputation — so this is better after §2C.4.

### 2C.2 Homebrew core

Notability thresholds (stars, forks, active maintenance) plus, for a cask,
signed and notarized macOS builds. The self-hosted tap in §2B.2 remains the
fallback indefinitely.

### 2C.3 Flathub build detail

Beyond PACKAGING.md's manifest:

- A reverse-DNS app ID. `com.huemux.HueMux`, since the domain is yours.
- **AppStream metainfo XML** with summary, description, screenshots,
  `<content_rating>` and the SPDX licence from §0.1. Flathub validates it.
- **Offline builds.** Flatpak builds have no network, so Go modules must be
  vendored (`go mod vendor`, committed) and Electron pre-fetched as declared
  sources. The Electron half is the real work; the plain Go build is easy.

Reviewed by hand. Expect a round of comments.

### 2C.4 Windows code signing — money and identity

SmartScreen warns on unsigned executables. Cheapest first:

- **Azure Trusted Signing** — pay-as-you-go, Microsoft-managed certificate, no
  hardware token, works from CI cleanly. The only sane option for this project.
- **OV certificate** — must live on a hardware token since the 2023 key-storage
  rules, which makes CI signing painful.
- **EV certificate** — immediate SmartScreen reputation, most expensive, also
  hardware-bound.

All require verifying a legal identity or business.

### 2C.5 macOS notarization — $99/year

Apple Developer Program, a Developer ID Application certificate, `codesign` on
the app *and* the embedded Electron framework, then `notarytool submit --wait`
and `xcrun stapler staple`. Hardened runtime, with a screen-recording
entitlement if the desktop build ever captures on macOS.

### 2C.6 Google Play

The most gated, and the only channel where §0.2's trademark question has teeth.
See §3.

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
nothing about. If avoiding Google entirely is acceptable, **the FOSS channels in §2A are
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

FOSS channels first, which is also the cheapest-first order. Nothing before
step 6 costs money or involves anyone's approval.

1. **`LICENSE` — GPL-3.0-or-later** (§0.1) — done 2026-08-02. No longer blocks any FOSS channel.
2. **Sign the Android release consistently** (PACKAGING.md). Do it before
   anyone installs, because Android will not upgrade across a signature change.
3. **Obtainium** (§2A.1) — a README line. Works with what CI already builds.
4. `THIRD_PARTY_LICENSES` + CI licence check (§1.1), and the About screen
   (§1.2). Required by the stores below and honest regardless.
5. **IzzyOnDroid** (§2A.2) — first real Android store. Takes the existing APK.
6. Scoop manifest and Homebrew tap (§2B.1, §2B.2), plus AUR (§2A.5). No
   gatekeepers.
7. **Flathub** (§2A.3, §2C.3) — the Linux desktop answer.
8. **F-Droid main repo** (§2A.4) — the flagship, once `gomobile bind` builds
   unattended on their infrastructure.
9. Split the release workflow and add the gated `release` environment (§4).
10. Privacy policy (§1.3) — needed from here on, not before.
11. Windows signing (§2C.4), then winget (§2C.1).
12. macOS notarization (§2C.5), then a Homebrew cask (§2C.2).
13. Google Play (§3) — last, most gated, and the only place §0.2 has teeth.
