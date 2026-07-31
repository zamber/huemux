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

## Screenshots

HueMux itself follows the system theme (or a manual override) — but this
page, `jekyll-theme-minimal`, doesn't have a dark mode of its own, so both
are shown explicitly below rather than swapped in based on your browser's
preference (which would otherwise pair a dark screenshot with this page's
permanently white background).

### Light control

<p align="center">
  <img src="screenshots/huemux-lights-light.png" alt="Light control panel, grouped by room, with entertainment-area scenes and favorites — light theme" width="700">
  <br><sub>Light theme</sub>
</p>

<p align="center">
  <img src="screenshots/huemux-lights-dark.png" alt="Light control panel, grouped by room, with entertainment-area scenes and favorites — dark theme" width="700">
  <br><sub>Dark theme</sub>
</p>

### Screen sync

<p align="center">
  <img src="screenshots/huemux-sync-light.png" alt="Screen sync panel, showing entertainment zone selection and the reactivity/sampling controls — light theme" width="700">
  <br><sub>Light theme</sub>
</p>

<p align="center">
  <img src="screenshots/huemux-sync-dark.png" alt="Screen sync panel, showing entertainment zone selection and the reactivity/sampling controls — dark theme" width="700">
  <br><sub>Dark theme</sub>
</p>

## Downloads

<div id="releases">Loading releases…</div>

<script>
// Tiny Markdown -> HTML converter for release bodies. Not a general GFM
// implementation — just enough for what these release notes actually use
// (##/### headers, **bold**, `code`, [links](url), - lists, --- rules, and
// | pipe | tables |), so a release written as plain Markdown on GitHub
// doesn't show up here as literal asterisks and pipe characters. Escapes
// the whole body first, then substitutes in a fixed allowlist of real tags
// — same trust model as the single `<` escape this replaced, just actually
// rendered instead of dumped as text.
function mdInline(s) {
  return s
    .replace(/`([^`]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
    .replace(/\*([^*]+)\*/g, '<em>$1</em>')
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
}

function mdToHtml(md) {
  const escaped = md.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  const lines = escaped.split('\n');
  const out = [];
  let para = [];
  const flushPara = () => {
    if (para.length) {
      out.push(`<p>${mdInline(para.join(' '))}</p>`);
      para = [];
    }
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim() === '') { flushPara(); continue; }
    if (/^###\s+/.test(line)) { flushPara(); out.push(`<h3>${mdInline(line.replace(/^###\s+/, ''))}</h3>`); continue; }
    if (/^##\s+/.test(line)) { flushPara(); out.push(`<h2>${mdInline(line.replace(/^##\s+/, ''))}</h2>`); continue; }
    if (/^---+$/.test(line.trim())) { flushPara(); out.push('<hr>'); continue; }
    if (/^\s*[-*]\s+/.test(line)) {
      flushPara();
      const items = [];
      while (i < lines.length && lines[i].trim() !== '') {
        if (/^\s*[-*]\s+/.test(lines[i])) items.push(lines[i].replace(/^\s*[-*]\s+/, ''));
        else items[items.length - 1] += ' ' + lines[i].trim();
        i++;
      }
      i--;
      out.push('<ul>' + items.map((it) => `<li>${mdInline(it)}</li>`).join('') + '</ul>');
      continue;
    }
    if (line.trim().startsWith('|')) {
      flushPara();
      const rows = [];
      while (i < lines.length && lines[i].trim().startsWith('|')) { rows.push(lines[i]); i++; }
      i--;
      const cells = (row) => row.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());
      const header = cells(rows[0]);
      const bodyRows = rows.slice(2).map(cells);
      const thead = '<tr>' + header.map((c) => `<th>${mdInline(c)}</th>`).join('') + '</tr>';
      const tbody = bodyRows.map((r) => '<tr>' + r.map((c) => `<td>${mdInline(c)}</td>`).join('') + '</tr>').join('');
      out.push(`<table><thead>${thead}</thead><tbody>${tbody}</tbody></table>`);
      continue;
    }
    para.push(line.trim());
  }
  flushPara();
  return out.join('\n');
}

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
      const notes = rel.body ? `<div class="release-notes">${mdToHtml(rel.body)}</div>` : '';
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
  #releases .release-notes h2 { font-size: 1.1em; margin: 0.8em 0 0.4em; }
  #releases .release-notes h3 { font-size: 1em; margin: 0.7em 0 0.3em; }
  #releases .release-notes table { font-size: 0.95em; margin: 0 0 0.8em; }
  #releases .release-notes ul { margin: 0 0 0.8em; padding-left: 1.2em; }
  #releases .release-notes hr { margin: 1em 0; }
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
