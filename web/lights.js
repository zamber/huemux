// lights.js — day-to-day Hue light control (rooms/lights browse+control,
// favorites, scenes). Talks to the same /ws connection and JSON
// control-message family as web/app.js (PROTOCOL.md §3), but is otherwise
// fully independent: this page never sends a capture grid frame, so it's
// always a plain "UI" connection, never the frame source.
//
// Design ported from lights-ui's Svelte components (LightCard.svelte,
// AllLightsTile.svelte, ColorPicker.svelte, Header.svelte's filter dropdown,
// stores.ts) — see that repo for the original — reimplemented here as
// plain DOM/template-string rendering, matching this repo's no-build-step
// philosophy. Notable deltas from the original, and why:
//   - Hue lights report color as CIE xy chromaticity, not hue/saturation, so
//     card tinting and scene swatches go through xyToRgb() below rather than
//     porting the HSL-based gradient math verbatim.
//   - The color picker's gesture handling is verbatim (canvas HSV render,
//     pointer events, coalesced "latest wins" updates) but the throttle is
//     requestAnimationFrame-based rather than an in-flight-request queue,
//     since sending over an already-open WebSocket has no fetch-style
//     round-trip latency to hide.
//   - There's no per-bridge "all lights" WS primitive — the all-lights tile
//     just fans out one light_toggle/light_brightness/light_color message
//     per light client-side, reusing the exact same per-light protocol.

const ICONS = {
  star: '<svg viewBox="0 0 24 24" fill="currentColor" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12,2 14.35,8.76 21.51,8.91 15.80,13.24 17.88,20.09 12,16 6.12,20.09 8.20,13.24 2.49,8.91 9.65,8.76"/></svg>',
  starOutline: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polygon points="12,2 14.35,8.76 21.51,8.91 15.80,13.24 17.88,20.09 12,16 6.12,20.09 8.20,13.24 2.49,8.91 9.65,8.76"/></svg>',
  palette: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><circle cx="8" cy="9" r="1.3" fill="currentColor" stroke="none"/><circle cx="12" cy="7" r="1.3" fill="currentColor" stroke="none"/><circle cx="16" cy="9" r="1.3" fill="currentColor" stroke="none"/><circle cx="9" cy="15" r="1.3" fill="currentColor" stroke="none"/></svg>',
  powerOn: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><line x1="12" y1="3" x2="12" y2="9"/><circle cx="12" cy="14" r="2.3" fill="currentColor" stroke="none"/></svg>',
  powerOff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><line x1="12" y1="3" x2="12" y2="9"/></svg>',
  lightbulb: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="9" r="6"/><line x1="12" y1="15" x2="12" y2="18"/><line x1="9" y1="18" x2="15" y2="18"/><line x1="10" y1="21" x2="14" y2="21"/></svg>',
};

const els = {
  connDot: document.getElementById('conn-dot'),
  unpaired: document.getElementById('unpaired-panel'),
  app: document.getElementById('app'),
  filterDetails: document.getElementById('filter-details'),
  filterSummary: document.getElementById('filter-summary'),
  filterList: document.getElementById('filter-list'),
  grid: document.getElementById('lights-grid'),
  scenesSection: document.getElementById('scenes-section'),
  scenesStrip: document.getElementById('scenes-strip'),
};

let ws = null;
let wsReady = false;
let ready = false; // becomes true once a paired status arrives and initial data is loaded

let lights = [];
let rooms = [];
let scenes = [];

let filter = 'favorites'; // 'favorites' | 'all' | 'room'
let filterRoomId = null;

// While a light (or the all-lights tile, keyed "__all__") is mid-drag on its
// brightness slider, external light_event merges still update the in-memory
// model but skip re-rendering the grid, so the slider the user is holding
// never gets yanked out from under their finger. Ported idea from
// LightCard.svelte's isUserEditing guard.
const editingIds = new Set();
const brightnessTimers = {};

// ---------- transport ----------

function connect() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.onopen = () => {
    wsReady = true;
    els.connDot.className = 'dot ok';
  };
  ws.onclose = () => {
    wsReady = false;
    els.connDot.className = 'dot';
    setTimeout(connect, 1500); // matches app.js's reconnect policy
  };
  ws.onerror = () => { els.connDot.className = 'dot warn'; };
  ws.onmessage = (ev) => {
    if (typeof ev.data !== 'string') return;
    handleMessage(JSON.parse(ev.data));
  };
}

function send(obj) {
  if (wsReady) ws.send(JSON.stringify(obj));
}

function handleMessage(msg) {
  switch (msg.type) {
    case 'status':
      if (!msg.paired) {
        ready = false;
        els.unpaired.hidden = false;
        els.app.hidden = true;
      } else if (!ready) {
        ready = true;
        els.unpaired.hidden = true;
        els.app.hidden = false;
        fetchLights();
        fetchRooms();
        fetchScenes();
      }
      break;
    case 'light_event':
      mergeLightEvent(msg.event);
      break;
    case 'favorite_event':
      mergeFavorite(msg.id, msg.favorite);
      break;
  }
}

function mergeLightEvent(ev) {
  if (ev.type === 'light') {
    const l = lights.find((x) => x.id === ev.id);
    if (!l) return;
    if (ev.on !== undefined) l.on = ev.on;
    if (ev.brightness !== undefined) l.brightness = ev.brightness;
    if (ev.x !== undefined) l.x = ev.x;
    if (ev.y !== undefined) l.y = ev.y;
  } else if (ev.type === 'grouped_light') {
    const r = rooms.find((x) => x.grouped_light_id === ev.id);
    if (!r) return;
    if (ev.on !== undefined) r.on = ev.on;
    if (ev.brightness !== undefined) r.brightness = ev.brightness;
  }
  if (editingIds.size === 0) renderGrid();
}

function mergeFavorite(id, fav) {
  if (id.indexOf('room:') === 0) {
    const r = rooms.find((x) => x.id === id.slice(5));
    if (r) r.favorite = fav;
  } else {
    const l = lights.find((x) => x.id === id);
    if (l) l.favorite = fav;
  }
  if (editingIds.size === 0) renderGrid();
}

// ---------- data fetch ----------

async function fetchLights() {
  const res = await fetch('/api/lights');
  lights = await res.json();
  renderGrid();
}

async function fetchRooms() {
  const res = await fetch('/api/rooms');
  rooms = await res.json();
  renderFilterMenu();
}

async function fetchScenes() {
  const res = await fetch('/api/scenes');
  scenes = await res.json();
  renderScenes();
}

// ---------- color math ----------
// xyToRgb (xy chromaticity -> sRGB, for card tinting/scene swatches) lives
// in shared/color.js, loaded before this file — used by both this page and
// the sync page's scenes strip.

function hsvToRgb(h, s, v) {
  s /= 100; v /= 100;
  const c = v * s;
  const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
  const m = v - c;
  let r, g, b;
  if (h < 60) { r = c; g = x; b = 0; }
  else if (h < 120) { r = x; g = c; b = 0; }
  else if (h < 180) { r = 0; g = c; b = x; }
  else if (h < 240) { r = 0; g = x; b = c; }
  else if (h < 300) { r = x; g = 0; b = c; }
  else { r = c; g = 0; b = x; }
  return [Math.round((r + m) * 255), Math.round((g + m) * 255), Math.round((b + m) * 255)];
}

function gradientStyleFor(rgb, brightnessPct) {
  const t = Math.max(0, Math.min(1, brightnessPct / 100));
  const innerFactor = 0.3 + t * 0.7;
  const outerFactor = 0.15 + t * 0.35;
  const scale = (c, f) => Math.round(c * f);
  const [r, g, b] = rgb;
  const inner = `rgb(${scale(r, innerFactor)}, ${scale(g, innerFactor)}, ${scale(b, innerFactor)})`;
  const outer = `rgb(${scale(r, outerFactor)}, ${scale(g, outerFactor)}, ${scale(b, outerFactor)})`;
  return `background: radial-gradient(circle at 30% 30%, ${inner} 0%, ${outer} 60%, transparent 100%);`;
}

// ---------- rendering ----------

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function filteredLights() {
  if (filter === 'favorites') return lights.filter((l) => l.favorite);
  if (filter === 'room' && filterRoomId) return lights.filter((l) => l.room_id === filterRoomId);
  return lights;
}

function renderGrid() {
  const list = filteredLights();
  let html = '';
  if (filter === 'all') html += renderAllLightsTile();
  html += list.map(renderLightCard).join('');
  if (!list.length && filter !== 'all') {
    html += `<p class="hint lights-empty">${escapeHtml(LightsyncI18n.t('lights.empty'))}</p>`;
  }
  els.grid.innerHTML = html;
}

function renderLightCard(l) {
  const off = !l.on;
  const brightnessPct = l.on ? Math.round(l.brightness) : 0;
  const gradient = l.colorable ? gradientStyleFor(xyToRgb(l.x, l.y, brightnessPct || 40), brightnessPct || 40) : '';
  return `
    <div class="light-card ${off ? 'off' : ''}" data-id="${escapeHtml(l.id)}">
      ${gradient ? `<div class="light-card-gradient" style="${gradient}"></div>` : ''}
      <div class="light-card-head">
        <h3 title="${escapeHtml(l.name)}">${escapeHtml(l.name)}</h3>
        <div class="light-card-actions">
          <button type="button" class="icon-btn ${l.favorite ? 'active' : ''}" data-action="favorite" data-id="${escapeHtml(l.id)}" title="${escapeHtml(LightsyncI18n.t('lights.toggleFavorite'))}">${l.favorite ? ICONS.star : ICONS.starOutline}</button>
          ${l.colorable ? `<button type="button" class="icon-btn" data-action="color" data-id="${escapeHtml(l.id)}" title="${escapeHtml(LightsyncI18n.t('lights.chooseColor'))}">${ICONS.palette}</button>` : ''}
          <button type="button" class="icon-btn ${l.on ? 'active' : ''}" data-action="toggle" data-id="${escapeHtml(l.id)}" title="${escapeHtml(LightsyncI18n.t(l.on ? 'lights.turnOff' : 'lights.turnOn'))}">${l.on ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${l.dimmable ? `<input type="range" class="brightness-slider" min="0" max="100" value="${brightnessPct}" data-action="brightness" data-id="${escapeHtml(l.id)}">` : ''}
    </div>`;
}

function renderAllLightsTile() {
  const anyOn = lights.some((l) => l.on);
  const hasBrightness = lights.some((l) => l.dimmable);
  const hasColor = lights.some((l) => l.colorable);
  const onLights = lights.filter((l) => l.on && l.dimmable);
  const avgBrightness = onLights.length
    ? Math.round(onLights.reduce((sum, l) => sum + l.brightness, 0) / onLights.length)
    : 50;
  return `
    <div class="light-card all-lights-tile" data-id="__all__">
      <div class="light-card-head">
        <h3>${ICONS.lightbulb}<span>${escapeHtml(LightsyncI18n.t('lights.allLights'))}</span></h3>
        <div class="light-card-actions">
          ${hasColor ? `<button type="button" class="icon-btn" data-action="color-all" title="${escapeHtml(LightsyncI18n.t('lights.chooseColorAll'))}">${ICONS.palette}</button>` : ''}
          <button type="button" class="icon-btn ${anyOn ? 'active' : ''}" data-action="toggle-all" title="${escapeHtml(LightsyncI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn'))}">${anyOn ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${hasBrightness ? `<input type="range" class="brightness-slider" min="0" max="100" value="${avgBrightness}" data-action="brightness-all">` : ''}
    </div>`;
}

function renderScenes() {
  if (!scenes.length) { els.scenesSection.hidden = true; return; }
  els.scenesSection.hidden = false;
  els.scenesStrip.innerHTML = scenes.map((sc) => {
    const swatches = sc.swatches.slice(0, 4).map((sw) => {
      const [r, g, b] = xyToRgb(sw.x, sw.y);
      return `<span class="scene-swatch" style="background: rgb(${r},${g},${b})"></span>`;
    }).join('');
    const title = sc.group_name ? `${sc.name} — ${sc.group_name}` : sc.name;
    return `
      <button type="button" class="scene-chip" data-scene-id="${escapeHtml(sc.id)}" title="${escapeHtml(title)}">
        <span class="scene-swatches">${swatches}</span>
        <span class="scene-name">${escapeHtml(sc.name)}</span>
        ${sc.auto_dynamic ? `<span class="scene-dynamic-badge" title="${escapeHtml(LightsyncI18n.t('lights.sceneDynamic'))}">&#10022;</span>` : ''}
      </button>`;
  }).join('');
}

function renderFilterMenu() {
  const items = [
    { key: 'favorites', label: LightsyncI18n.t('lights.filterFavorites') },
    { key: 'all', label: LightsyncI18n.t('lights.filterAll') },
  ].concat(rooms.map((r) => ({ key: 'room:' + r.id, label: r.name })));

  els.filterList.innerHTML = items.map((it) => {
    const active =
      (filter === 'favorites' && it.key === 'favorites') ||
      (filter === 'all' && it.key === 'all') ||
      (filter === 'room' && it.key === 'room:' + filterRoomId);
    return `<button type="button" class="filter-item ${active ? 'active' : ''}" data-key="${escapeHtml(it.key)}">${escapeHtml(it.label)}</button>`;
  }).join('');

  updateFilterSummary();
}

function updateFilterSummary() {
  let label = LightsyncI18n.t('lights.filterCategories');
  if (filter === 'favorites') label = LightsyncI18n.t('lights.filterFavorites');
  else if (filter === 'all') label = LightsyncI18n.t('lights.filterAll');
  else if (filter === 'room' && filterRoomId) {
    const r = rooms.find((x) => x.id === filterRoomId);
    if (r) label = r.name;
  }
  els.filterSummary.textContent = label;
}

// ---------- actions ----------

function actionToggleAll() {
  const target = !lights.some((l) => l.on);
  lights.forEach((l) => send({ type: 'light_toggle', rid: l.id, on: target }));
}

function actionColorAll(r, g, b) {
  lights.filter((l) => l.colorable).forEach((l) => send({ type: 'light_color', rid: l.id, r, g, b }));
}

function scheduleBrightness(id, pct) {
  editingIds.add(id);
  clearTimeout(brightnessTimers[id]);
  brightnessTimers[id] = setTimeout(() => {
    if (id === '__all__') {
      lights.filter((l) => l.dimmable).forEach((l) => send({ type: 'light_brightness', rid: l.id, brightness: pct }));
    } else {
      send({ type: 'light_brightness', rid: id, brightness: pct });
    }
    setTimeout(() => {
      editingIds.delete(id);
      renderGrid();
    }, 500);
  }, 300);

  // Optimistic local update (no re-render — keeps the slider under the
  // user's finger instead of getting replaced mid-drag).
  if (id === '__all__') {
    lights.filter((l) => l.dimmable).forEach((l) => { l.brightness = pct; l.on = pct > 0; });
  } else {
    const l = lights.find((x) => x.id === id);
    if (l) { l.brightness = pct; l.on = pct > 0; }
  }
}

// ---------- color picker ----------

function openColorPicker(targetId) {
  const overlay = document.createElement('div');
  overlay.className = 'color-picker-overlay';
  overlay.setAttribute('role', 'application');
  const name = targetId ? ((lights.find((l) => l.id === targetId) || {}).name || '') : LightsyncI18n.t('lights.allLights');
  overlay.setAttribute('aria-label', LightsyncI18n.t('lights.colorPickerTitle', { name }));
  overlay.innerHTML =
    `<div class="color-picker-head"><h2>${escapeHtml(name)}</h2></div>` +
    `<canvas class="color-picker-canvas"></canvas>` +
    `<div class="color-picker-foot"><div class="color-picker-swatch"></div><span class="color-picker-readout"></span></div>`;
  document.body.appendChild(overlay);

  const canvas = overlay.querySelector('.color-picker-canvas');
  const ctx = canvas.getContext('2d');
  const swatch = overlay.querySelector('.color-picker-swatch');
  const readout = overlay.querySelector('.color-picker-readout');

  let hue = 0;
  let sat = 0;
  let cursorEl = null;
  let pendingColor = null;
  let rafScheduled = false;
  let active = false;

  function renderGradient() {
    const w = canvas.width;
    const h = canvas.height;
    if (w <= 0 || h <= 0) return;
    const img = ctx.createImageData(w, h);
    const data = img.data;
    for (let py = 0; py < h; py++) {
      const s = 100 - (py / h) * 100;
      for (let px = 0; px < w; px++) {
        const hh = (px / w) * 360;
        const [r, g, b] = hsvToRgb(hh, s, 100);
        const i = (py * w + px) * 4;
        data[i] = r; data[i + 1] = g; data[i + 2] = b; data[i + 3] = 255;
      }
    }
    ctx.putImageData(img, 0, 0);
  }

  function resize() {
    canvas.width = overlay.clientWidth;
    canvas.height = overlay.clientHeight - 180; // header+footer, matches lights.css
    renderGradient();
  }

  function flush() {
    rafScheduled = false;
    if (!pendingColor) return;
    const { r, g, b } = pendingColor;
    pendingColor = null;
    if (targetId) send({ type: 'light_color', rid: targetId, r, g, b });
    else actionColorAll(r, g, b);
  }

  function pick(e) {
    const rect = canvas.getBoundingClientRect();
    const x = Math.max(0, Math.min(e.clientX - rect.left, rect.width));
    const y = Math.max(0, Math.min(e.clientY - rect.top, rect.height));
    hue = Math.round((x / rect.width) * 360);
    sat = Math.round(100 - (y / rect.height) * 100);

    const [r, g, b] = hsvToRgb(hue, sat, 100);
    swatch.style.backgroundColor = `rgb(${r}, ${g}, ${b})`;
    readout.textContent = `H: ${hue}° S: ${sat}%`;

    pendingColor = { r, g, b };
    if (!rafScheduled) {
      rafScheduled = true;
      requestAnimationFrame(flush);
    }

    if (!cursorEl) {
      cursorEl = document.createElement('div');
      cursorEl.className = 'color-picker-cursor';
      overlay.appendChild(cursorEl);
    }
    const isTouch = e.pointerType === 'touch';
    cursorEl.style.left = e.clientX + 'px';
    cursorEl.style.top = (isTouch ? e.clientY - 50 : e.clientY) + 'px';
    cursorEl.style.backgroundColor = `rgb(${r}, ${g}, ${b})`;
    cursorEl.hidden = false;
  }

  function onDown(e) { active = true; pick(e); }
  function onMove(e) { if (!active) return; e.preventDefault(); pick(e); }
  function onUp(e) {
    if (!active) return;
    pick(e);
    active = false;
    if (cursorEl) cursorEl.hidden = true;
    closePicker();
  }

  function closePicker() {
    document.removeEventListener('pointermove', onMove);
    document.removeEventListener('pointerup', onUp);
    document.removeEventListener('pointercancel', onUp);
    window.removeEventListener('resize', resize);
    overlay.remove();
  }

  overlay.addEventListener('pointerdown', onDown);
  document.addEventListener('pointermove', onMove);
  document.addEventListener('pointerup', onUp);
  document.addEventListener('pointercancel', onUp);
  window.addEventListener('resize', resize);

  resize();
}

// ---------- event delegation ----------

els.grid.addEventListener('click', (e) => {
  const btn = e.target.closest('button[data-action]');
  if (!btn) return;
  const action = btn.dataset.action;
  const id = btn.dataset.id;
  switch (action) {
    case 'favorite':
      send({ type: 'light_favorite', rid: id });
      break;
    case 'toggle': {
      const l = lights.find((x) => x.id === id);
      send({ type: 'light_toggle', rid: id, on: !(l && l.on) });
      break;
    }
    case 'color':
      openColorPicker(id);
      break;
    case 'toggle-all':
      actionToggleAll();
      break;
    case 'color-all':
      openColorPicker(null);
      break;
  }
});

els.grid.addEventListener('input', (e) => {
  const el = e.target;
  if (el.dataset.action === 'brightness') {
    scheduleBrightness(el.dataset.id, parseInt(el.value, 10));
  } else if (el.dataset.action === 'brightness-all') {
    scheduleBrightness('__all__', parseInt(el.value, 10));
  }
});

els.filterList.addEventListener('click', (e) => {
  const btn = e.target.closest('.filter-item');
  if (!btn) return;
  const key = btn.dataset.key;
  if (key === 'favorites') { filter = 'favorites'; filterRoomId = null; }
  else if (key === 'all') { filter = 'all'; filterRoomId = null; }
  else if (key.indexOf('room:') === 0) { filter = 'room'; filterRoomId = key.slice(5); }
  persistFilterToURL();
  els.filterDetails.open = false;
  renderFilterMenu();
  renderGrid();
});

els.scenesStrip.addEventListener('click', (e) => {
  const chip = e.target.closest('.scene-chip');
  if (!chip) return;
  send({ type: 'scene_recall', rid: chip.dataset.sceneId });
});

document.addEventListener('lightsync:langchange', () => {
  renderFilterMenu();
  renderGrid();
  renderScenes();
});

// ---------- filter <-> URL ----------

function persistFilterToURL() {
  const params = new URLSearchParams();
  params.set('filter', filter);
  if (filter === 'room' && filterRoomId) params.set('room', filterRoomId);
  history.replaceState(null, '', '?' + params.toString());
}

function restoreFilterFromURL() {
  const params = new URLSearchParams(location.search);
  const f = params.get('filter');
  const r = params.get('room');
  if (f === 'favorites' || f === 'all') { filter = f; filterRoomId = null; }
  else if (f === 'room' && r) { filter = 'room'; filterRoomId = r; }
}

// ---------- init ----------

restoreFilterFromURL();
LightsyncI18n.init().then(() => {
  renderFilterMenu();
  renderGrid();
});
connect();
