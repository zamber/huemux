---
layout: default
title: HueMux
---

# HueMux

Lightweight, cross-platform frontend for Philips Hue: a Go server you drive
from a browser or an Electron desktop app.

- **Screen sync** — captures your screen and streams it to a Hue
  Entertainment area over DTLS, in real time.
- **Light control** — day-to-day control of every room, light, and scene:
  on/off, brightness, colour, favorites.

Both talk to the bridge directly — no cloud, no Hue Sync app, no
platform-specific screen-capture hacks. Video sync works natively on
Wayland, X11, Windows, and macOS.

[View source on GitHub](https://github.com/zamber/huemux) ·
[Read the docs](https://github.com/zamber/huemux#readme)

## Downloads

<div id="releases">Loading releases…</div>

<script>
fetch('https://api.github.com/repos/zamber/huemux/releases')
  .then((r) => r.json())
  .then((releases) => {
    const el = document.getElementById('releases');
    if (!Array.isArray(releases) || releases.length === 0) {
      el.textContent = 'No releases published yet — check back soon, or build from source in the meantime.';
      return;
    }
    el.innerHTML = releases.map((rel) => {
      const date = new Date(rel.published_at).toLocaleDateString(undefined, { year: 'numeric', month: 'long', day: 'numeric' });
      const assets = (rel.assets || []).map((a) => {
        const mb = (a.size / 1024 / 1024).toFixed(1);
        return `<li><a href="${a.browser_download_url}">${a.name}</a> <span class="asset-size">(${mb} MB)</span></li>`;
      }).join('');
      const notes = rel.body ? `<div class="release-notes">${rel.body.replace(/</g, '&lt;')}</div>` : '';
      return `
        <div class="release">
          <h3>${rel.name || rel.tag_name} <span class="release-date">${date}</span></h3>
          <ul class="assets">${assets}</ul>
          ${notes}
        </div>`;
    }).join('<hr>');
  })
  .catch(() => {
    document.getElementById('releases').innerHTML =
      'Could not load releases from the GitHub API — see the <a href="https://github.com/zamber/huemux/releases">releases page</a> directly.';
  });
</script>

<style>
  #releases .release h3 { margin-bottom: 0.2em; }
  #releases .release-date { font-weight: normal; font-size: 0.75em; color: #666; }
  #releases .assets { list-style: none; padding-left: 0; }
  #releases .asset-size { color: #666; font-size: 0.85em; }
  #releases .release-notes { font-size: 0.9em; color: #444; }
</style>

## Why it runs on localhost

`getDisplayMedia()` — the browser screen capture API — only works in a
secure context, and loopback origins are one without needing a
certificate. The same process also owns the DTLS socket to the bridge,
which a browser can never open itself: Hue Entertainment is DTLS 1.2 with
a pre-shared key over UDP, and no browser API exposes that. One binary
solves both problems.

## Building from source

```bash
git clone https://github.com/zamber/huemux.git
cd huemux
make dev            # local binary: huemux
make dev-desktop    # local binary: huemux-desktop (Electron wrapper)
```

Full build/run docs are in the [README](https://github.com/zamber/huemux#readme).
