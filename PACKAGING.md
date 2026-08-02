# Packaging

How HueMux's release binaries get built, signed (where relevant), and
published, plus how to package it further for OS-native distribution
channels (Homebrew, Flatpak, AppImage). Written for whoever cuts a release
or wants to add a new packaging target — most of this doesn't change often.

> **Store submission, developer accounts, and the legal work that has to
> happen first** (licensing, attribution, privacy policy, trademark) are in
> [PUBLISHING.md](PUBLISHING.md). This file is about producing artifacts;
> that one is about being allowed to distribute them.

## Current state vs. what's described below

Today, `.github/workflows/release.yml` builds and publishes **plain,
unsigned** binaries for every OS on a tag push. Nothing here is optional
extra polish except explicitly marked as such — but no code signing has
been set up yet (see below for exactly what's needed and why it hasn't
happened automatically). Treat the code-signing sections as "how to do
this when you're ready to," not "already done."

## Bumping the version (semver)

There's no hardcoded version anywhere to edit. `Makefile`'s `VERSION` is
`git describe --tags --always --dirty`, baked into the binary via
`-ldflags -X main.version=$(VERSION)`. So a release **is** a git tag:

```sh
git tag v0.3.0
git push origin v0.3.0
```

That push alone triggers `release.yml` (matches `v*`), which builds every
platform and publishes a GitHub Release with `generate_release_notes: true`
(auto-generated from merged PRs/commits since the last tag).

What to bump, per [semver](https://semver.org/) (`MAJOR.MINOR.PATCH`):

- **PATCH** (`v0.3.0` → `v0.3.1`): bug fixes, no behavior/protocol change a
  user would need to react to.
- **MINOR** (`v0.3.0` → `v0.4.0`): new features, backward-compatible. This
  covers almost everything so far (new UI features, new WS message types
  that old clients simply ignore).
- **MAJOR**: breaking changes. The one that's already happened in this repo
  without a version bump (there was no tagged release yet) is the config
  directory rename (`~/.config/lightsync` → `~/.config/huemux`, forces
  re-pairing) — that class of change (anything that breaks an existing
  install/config on upgrade) is what MAJOR is for. Pre-1.0 (`v0.x.y`), minor
  bumps are conventionally allowed to include breaking changes too (per
  semver's own spec, "anything may change" before 1.0.0) — use judgement,
  but a changelog note either way.

No formal changelog file is kept beyond git tags/GitHub's auto-generated
release notes; if that stops being enough, revisit.

## Code signing: does it matter, and how

**Short answer: yes for Windows and macOS, no for Linux.** Neither is set
up yet — both need a paid certificate/account that doesn't exist yet, so
this is a "when you decide it's worth it" task, not something automatable
without that first manual step.

### macOS

Unsigned apps get Gatekeeper's "Apple could not verify... is free of
malware" dialog, which most users won't push through (the reliable manual
bypass is right-click → Open, or `xattr -d com.apple.quarantine
huemux-darwin-*`, but that's real friction to ask of anyone downloading a
binary).

To fix it properly:

1. Apple Developer Program membership ($99/year) — the one recurring cost.
2. A **Developer ID Application** certificate (created in Xcode or the
   Apple Developer portal, lives in your Keychain).
3. Sign the binary: `codesign --sign "Developer ID Application: Your Name
   (TEAMID)" --options runtime dist/huemux-darwin-arm64`.
4. **Notarize** it (Apple's automated scan, separate from signing and
   mandatory since macOS 10.15 for anything downloaded outside the App
   Store): `xcrun notarytool submit dist/huemux-darwin-arm64.zip
   --apple-id you@example.com --team-id TEAMID --password
   <app-specific-password> --wait`, then `xcrun stapler staple` the result
   so the notarization ticket travels with the binary (works offline).
5. Both steps **require running on real macOS** (Xcode command line tools,
   `codesign`/`notarytool`) — there is no cross-signing story from Linux.
   This means: either a `macos-latest` GitHub Actions runner (works fine,
   GitHub-hosted macOS runners have Xcode preinstalled) with the
   certificate + app-specific password stored as encrypted repo secrets, or
   a manual signing step on an actual Mac before uploading. Automating it
   in `release.yml` is the better long-term option once the certificate
   exists.

### Windows

Unsigned `.exe`s trigger SmartScreen's "Windows protected your PC" —
bypassable via "More info" → "Run anyway," friction but not a hard block.

1. Buy an Authenticode code-signing certificate (OV, ~$100–400/year, from
   e.g. DigiCert/Sectigo/SSL.com) or an EV certificate (pricier, needs a
   hardware token/HSM, but gets **instant** SmartScreen reputation instead
   of building it up over time/downloads with an OV cert).
2. Sign with `signtool sign /f cert.pfx /p <password> /fd sha256 /tr
   http://timestamp.digicert.com /td sha256 huemux-windows-amd64.exe`
   (Windows), or `osslsigncode sign` (works on Linux, if the private key
   isn't locked to a hardware token — EV certs usually are, which is the
   main practical reason OV is easier to automate in CI).
3. Can run in Actions on `windows-latest`, or via `osslsigncode` on the
   existing `ubuntu-latest` job if using an OV cert with an exportable key.

### Linux

No equivalent gatekeeping mechanism for a standalone binary — distros trust
their own package repos, not arbitrary downloaded executables, so there's
no dialog to suppress. The closest thing to "signing" that's worth doing
regardless of platform: publish `SHA256SUMS` (already done by `make dist`)
and optionally GPG-sign it (`gpg --detach-sign SHA256SUMS`) or use
[Sigstore/cosign](https://www.sigstore.dev/) (the modern, keyless
alternative) so a download can be verified against the GitHub release —
nice-to-have, not blocking anything.

## Should packaging live in GitHub Actions or be done manually?

**GitHub Actions, same as the current plain-binary build.** The one thing
that can't be automated *before it exists* is buying the certificates
above — that's a manual, one-time (well, annual) step outside git entirely.
Once a cert exists, encrypt it into a repo secret and every packaging step
below (signing, Flatpak, AppImage, Homebrew formula bump) can run
unattended on tag push exactly like `release.yml` already does. Doing it
by hand instead only makes sense if you'd rather not put a code-signing
private key in GitHub's secret store at all, which is a reasonable but
separate trust call — not a packaging-mechanics one.

## Homebrew (macOS + Linuxbrew)

Homebrew unified its Linux support years ago ("Linuxbrew" now just means
"Homebrew on Linux") — one formula, if it only needs to install a binary
with no OS-specific dependencies, works for both.

**Recommended: your own tap**, not `homebrew-core`. `homebrew-core`
requires the formula to build from source (not fetch a prebuilt binary),
imposes notability/stability bars, and goes through PR review by
Homebrew's maintainers — appropriate for an established project, overkill
for getting a first release out. A tap is just a git repo named
`homebrew-<name>`:

1. Create `github.com/zamber/homebrew-huemux`.
2. Add `Formula/huemux.rb`:

   ```ruby
   class Huemux < Formula
     desc "Screen-color sync and light control for Philips Hue"
     homepage "https://github.com/zamber/huemux"
     version "0.3.0"

     on_macos do
       on_arm do
         url "https://github.com/zamber/huemux/releases/download/v0.3.0/huemux-darwin-arm64"
         sha256 "..."
       end
       on_intel do
         url "https://github.com/zamber/huemux/releases/download/v0.3.0/huemux-darwin-amd64"
         sha256 "..."
       end
     end
     on_linux do
       on_arm do
         url "https://github.com/zamber/huemux/releases/download/v0.3.0/huemux-linux-arm64"
         sha256 "..."
       end
       on_intel do
         url "https://github.com/zamber/huemux/releases/download/v0.3.0/huemux-linux-amd64"
         sha256 "..."
       end
     end

     def install
       bin.install Dir["huemux-*"].first => "huemux"
     end
   end
   ```

3. Users install via `brew tap zamber/huemux && brew install huemux`.
4. Bumping the formula's `url`/`sha256`/`version` on every release is the
   one recurring chore — script it (a small step in `release.yml` that
   checks out the tap repo, sed-replaces those three fields per platform
   block, commits, and pushes with a token that has write access to the tap
   repo) rather than doing it by hand each time.

`huemux-desktop` (the Electron GUI) could get its own formula the same way,
or — more idiomatically for a GUI app on macOS — a **Cask** instead of a
Formula in the same tap, if you later ship a `.app` bundle/`.dmg` rather
than a bare binary. Not needed yet since it's currently just another static
binary with no bundle structure.

## Flatpak

Needs a manifest (YAML or JSON) describing runtime, permissions, and how to
build/install the app, keyed to a reverse-DNS app ID, e.g.
`io.github.zamber.HueMux`.

Minimal manifest, given the Go binary already has zero dynamic
dependencies (`CGO_ENABLED=0`) so there's nothing to actually build inside
the sandbox — just install the prebuilt release binary:

```yaml
app-id: io.github.zamber.HueMux
runtime: org.freedesktop.Platform
runtime-version: "23.08"
sdk: org.freedesktop.Sdk
command: huemux-desktop
finish-args:
  - --share=network        # bridge is on the local LAN, over UDP/DTLS
  - --socket=wayland
  - --socket=fallback-x11
  - --socket=pulseaudio     # if audio loopback capture is ever wired up
  - --device=dri            # GPU accel for the Electron window
modules:
  - name: huemux-desktop
    buildsystem: simple
    build-commands:
      - install -Dm755 huemux-desktop-linux-* /app/bin/huemux-desktop
      - install -Dm644 packaging/io.github.zamber.HueMux.desktop /app/share/applications/io.github.zamber.HueMux.desktop
      - install -Dm644 packaging/icon.svg /app/share/icons/hicolor/scalable/apps/io.github.zamber.HueMux.svg
    sources:
      - type: file
        url: https://github.com/zamber/huemux/releases/download/v0.3.0/huemux-desktop-linux-amd64
        sha256: "..."
        dest-filename: huemux-desktop-linux-amd64
```

Two real gaps to fill in before this works as-is: a `.desktop` file and an
icon (neither exists in `packaging/` yet — needed for any Linux desktop
packaging, Flatpak or otherwise). `--filesystem` is deliberately **not**
requested: the config directory should live inside Flatpak's own per-app
data dir (`~/.var/app/io.github.zamber.HueMux/config`), which
`config.Dir()`'s `os.UserConfigDir()` call already resolves to correctly
under Flatpak's environment overrides — no code change needed, just don't
punch a hole through the sandbox for it.

**Distribution:** self-host (run `flatpak-builder` yourself, serve the
resulting repo from anywhere, users `flatpak remote-add`) for full control
and no review wait, or submit to
[Flathub](https://github.com/flathub/flathub) (the de facto standard,
huge discoverability, but goes through their submission review and
requires meeting their [quality guidelines](https://docs.flathub.org/docs/for-app-authors/requirements/)).
Recommend starting self-hosted, moving to Flathub once the app/manifest
has stabilized — resubmitting there after guideline-driven rework is
wasted review-queue time otherwise.

## AppImage

A portable, self-mounting executable — no sandbox/permission model, no
install step, and cheap to produce because the binary is fully static
(the only thing it fetches at first run is Electron itself, into the user's
cache, exactly as the tarball does).

**Built and published automatically** by `.github/workflows/appimage.yml`
on every push to `main`. The AppDir layout lives in the repo at
`packaging/appimage/` (AppRun shim + `huemux.desktop`; the icon is the
same 512px render used by the store listing). The workflow:

1. builds `dist/huemux-desktop-linux-amd64` via `make dist-desktop`;
2. runs `appimagetool` (downloaded, run with `--appimage-extract-and-run`
   since runners lack FUSE) with the update-information string and
   `--file-url` baked in, which also emits the `.zsync` delta file;
3. uploads both to the **`continuous` release** (created on first run).

### The continuous channel

The AppImage community's convention for "always the latest, always the
same URL":

```
https://github.com/zamber/huemux/releases/download/continuous/huemux-x86_64.AppImage
```

The asset under that URL is replaced on every build (`gh release upload
--clobber`), so the URL never changes while its contents track `main`.
The embedded update information (`gh-releases-zsync|zamber|huemux|
continuous|huemux-x86_64.AppImage`) plus the uploaded `.zsync` file means
the image can update itself in place:

```sh
./huemux-x86_64.AppImage --appimage-update   # or the AppImageUpdate GUI
```

Triggered on `main` pushes rather than tags deliberately: every release is
a tag on a main commit, and the branch push precedes the tag, so gating on
tags would only add duplicate runs — while building on main keeps the
channel fresh even between releases.

The desktop shell needs a display at runtime; the AppImage itself does not
need FUSE (the runtime falls back to `--appimage-extract-and-run`), so it
runs on any modern distro and even in containers with a display.

Not on the versioned releases by design — the versioned release already
carries the plain `huemux-desktop-linux-amd64` tarball-style binary, and
the channel is what Linux users should use for the desktop build. If a
versioned AppImage is ever wanted (e.g. for AppImageHub ingestion), it is
a two-line addition to `release.yml`.

## Release artifact signing (GPG)

Every release carries `SHA256SUMS.asc`, a detached signature over the checksum
file. One signature covers every artifact, since the checksums already bind
each file's contents.

The public key is committed as `RELEASE-SIGNING-KEY.asc` and copied into each
release. To verify a download:

```sh
gpg --import RELEASE-SIGNING-KEY.asc
gpg --verify SHA256SUMS.asc SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

Key: `06AD E5FB 119C D5D9 EF49  2FA0 8C1D 68E7 C991 0D5E`, expires 2031.
Secrets `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` on the repo; private material at
`~/.huemux-signing/`, never in the repo. A revocation certificate is stored
with it, for the case where the key leaks.

## Windows and macOS signing — not set up, and not self-serve

Both steps exist in `release.yml` and are gated on secrets that do not exist.
Neither certificate can be generated locally, which is why this is a purchase
decision rather than a task:

- **Windows (Authenticode)** needs a CA-issued certificate — Azure Trusted
  Signing (pay-as-you-go, no hardware token, works from CI) or an OV/EV cert,
  which since 2023 must live on a hardware token and is painful to use from CI.
- **macOS (Developer ID)** is issued by Apple to Developer Program members
  only, at $99/year.

A self-signed certificate is not a cheaper version of either. SmartScreen and
Gatekeeper both ignore it, so it buys nothing and adds a key to look after.
Until then, Windows and macOS artifacts ship unsigned and users see the
platform's warning — which is the honest state, and no worse than it was.

Both steps **fail the build** if their secrets appear without the
implementation, rather than silently producing unsigned artifacts.

## Key custody

Signing material lives at `~/.huemux-signing/` (0700, files 0600) on the
maintainer's machine, outside the repo:

| File | If lost |
|---|---|
| `huemux.jks` + `password.txt` | **Unrecoverable.** No published install can ever be updated again; recovery means a new application ID and every user reinstalling. |
| `gpg-private.asc` + `gpg-passphrase.txt` | Recoverable but disruptive — rotate, republish the public key, re-announce. |
| `revocation-certificate.rev` | Needed to revoke the GPG key if it leaks. |

That directory also holds `backup-to-bitwarden.sh` (vault copy; run it after
unlocking the vault yourself) and `OFFLINE-BACKUP.md` (the copy that does not
depend on an account). Both copies matter: a vault is a single point of failure
for a key that cannot be regenerated.

## Android

The APK is built by `.github/workflows/release.yml` and attached to each
release. It cannot be built on a machine without the Android SDK and NDK
(8–12 GB), which is why it lives in CI — GitHub's runners ship both.

### Signing

Without a keystore the workflow produces a **debug-signed** APK, named
`…-arm64-debug.apk`. That installs and runs fine, and is what alpha releases
have shipped so far. Its two limits: Android refuses to upgrade between
differently-signed builds (so a later signed release needs an uninstall
first), and no store will accept it.

**This is now set up.** The keystore lives at `~/.huemux-signing/huemux.jks`
on the maintainer's machine, with its password beside it, and the four secrets
below are configured on the repository. Key fingerprint (SHA-256), for
verifying a downloaded APK is genuinely ours:

```
43:44:0F:03:F8:DF:CF:E2:23:C9:20:8F:5A:98:14:E2:4C:69:1C:84:DD:41:4A:DF:F7:C6:C4:ED:8E:E9:D0:60
```

> **Back that directory up, offline, now.** Android identifies an app by its
> signing key. Losing this file means no existing install can ever be updated
> again — not by you, not by anyone — and the only recovery is publishing under
> a new application ID and asking every user to reinstall. It is valid until
> 2053; treat it like the only copy of a house key.

To create one from scratch (already done — kept for reference and for anyone
forking this):

```sh
keytool -genkeypair -v \
  -keystore huemux.jks -alias huemux \
  -keyalg RSA -keysize 4096 -validity 10000
```

Then add four repository secrets:

| Secret | Value |
|---|---|
| `ANDROID_KEYSTORE_B64` | `base64 -w0 huemux.jks` |
| `ANDROID_KEYSTORE_PASSWORD` | the store password |
| `ANDROID_KEY_ALIAS` | `huemux` |
| `ANDROID_KEY_PASSWORD` | the key password |

The workflow detects `ANDROID_KEYSTORE_B64` and switches to `assembleRelease`
automatically. Nothing else changes, and a fork without the secrets keeps
building debug APKs rather than failing.

**Do not commit the keystore**, and note that a `.jks` in a public repo is
equivalent to publishing the private key — the same reasoning that keeps
HueMux out of the certificate business in the TLS section of the README.

### Distribution

F-Droid is the natural fit: the build is reproducible from source, has no
proprietary dependencies, and needs no Google account. Play Store is possible
but adds a `MediaProjection` policy review once screen sync lands, which is
one reason that feature is scoped after lights control.
