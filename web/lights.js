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
  stopStreamingBtn: document.getElementById('stop-streaming-btn'),
};

let ws = null;
let wsReady = false;
let ready = false; // becomes true once a paired status arrives and initial data is loaded
let loaded = false; // becomes true once /api/lights has actually answered at least once

let lights = [];
let rooms = [];
let scenes = [];
let favoritesRaw = {}; // id -> unix-seconds; covers ids /api/lights and /api/rooms don't carry a favorite flag for (scenes, and the synthetic "all" pseudo-id)

// ---------- offline cache ----------
//
// Opening this page used to show nothing at all, then "No lights found on
// this bridge.", then the lights — because the grid is gated behind a
// WebSocket connecting, reporting `paired`, and four fetches resolving, and
// renders an empty state in the meantime. Every one of those steps is fast on
// its own and visibly slow in sequence on a phone.
//
// The last known-good payload is small, changes rarely, and is a strictly
// better first paint than an empty grid, so it goes to localStorage and comes
// straight back on load. It is presentation only: the live WebSocket
// overwrites every value it covers within a moment, and the fetches replace
// the arrays wholesale. Nothing is ever *sent* to the bridge from cache.
const CACHE_KEY = 'lightsCache.v1';

function saveCache() {
  try {
    localStorage.setItem(CACHE_KEY, JSON.stringify({ lights, rooms, scenes, favoritesRaw }));
  } catch (e) {
    // A full or disabled localStorage is not worth failing a render over.
  }
}

// Returns whether anything was restored, so the caller can decide to reveal
// the grid before the server has confirmed we are still paired.
function loadCache() {
  let raw;
  try {
    raw = localStorage.getItem(CACHE_KEY);
  } catch (e) {
    return false;
  }
  if (!raw) return false;
  try {
    const c = JSON.parse(raw);
    // Shape-check rather than trust: this survives across versions, and a
    // half-understood payload rendering as a broken grid would be worse than
    // the empty state it replaces.
    if (!Array.isArray(c.lights) || !Array.isArray(c.rooms) || !Array.isArray(c.scenes)) return false;
    lights = c.lights;
    rooms = c.rooms;
    scenes = c.scenes;
    favoritesRaw = (c.favoritesRaw && typeof c.favoritesRaw === 'object') ? c.favoritesRaw : {};
    return lights.length > 0;
  } catch (e) {
    return false;
  }
}

let filter = 'favorites'; // 'favorites' | 'all' | 'room'
let filterRoomId = null;
let filterExplicitFromURL = false; // true if the URL named a filter — otherwise the empty-favorites fallback below may override the 'favorites' default

// While a light (or the all-lights tile, keyed "__all__") is mid-drag on its
// brightness slider, external light_event merges still update the in-memory
// model but skip re-rendering the grid, so the slider the user is holding
// never gets yanked out from under their finger. Ported idea from
// LightCard.svelte's isUserEditing guard.
const editingIds = new Set();
const brightnessTimers = {};

// ---------- transport ----------

function connect() {
  ws = new WebSocket(authWSURL('/ws'));
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

// <huemux-pairing> is transport-agnostic — it emits the message it wants sent
// and each host page puts it on its own WebSocket. See shared/pairing.js.
let discoveryStarted = false;
els.unpaired.addEventListener('huemux:pair-send', (ev) => send(ev.detail));

function handleMessage(msg) {
  switch (msg.type) {
    case 'status':
      if (!msg.paired) {
        ready = false;
        els.unpaired.hidden = false;
        els.app.hidden = true;
        // Kick off discovery once, the same way sync.html does. Without this
        // the panel would sit on "no bridge found" until the user pressed
        // Search again, since discovery is client-initiated.
        if (!discoveryStarted) {
          discoveryStarted = true;
          send({ type: 'discover_bridges' });
        }
        els.unpaired.update(msg.pairing || {});
        break;
      }
      if (!ready) {
        ready = true;
        els.unpaired.hidden = true;
        els.app.hidden = false;
        initialLoad();
      }
      // Entertainment streaming (from any client) holds exclusive control
      // of its lights' color/brightness, same as the real Hue app — this
      // is how you get manual control back without switching to Sync.
      els.stopStreamingBtn.hidden = !(msg.snapshot && msg.snapshot.StreamActive);
      break;
    case 'light_event':
      mergeLightEvent(msg.event);
      break;
    case 'favorite_event':
      mergeFavorite(msg.id, msg.favorite);
      break;
    case 'config_changed':
      // set(), not load(): the push is fresher than load()'s cached fetch,
      // and the header re-renders tabs and the logout button from this.
      if (typeof HueMuxFeatures !== 'undefined') HueMuxFeatures.set(msg);
      break;
  }
}

// ---------- targeted DOM updates ----------
//
// A light_event names exactly one light, but the original code responded by
// rebuilding the whole grid with innerHTML. Measured on a wall panel that was
// ~745ms through to paint, twice per action, and consecutive taps queued
// behind each other until latency reached seconds. See ARCHITECTURE.md.
//
// patchLightCard updates the handful of attributes that can actually change,
// leaving the DOM structure alone. Returns false when the card is not on
// screen, so the caller can fall back to a full render.

function patchLightCard(l) {
  const card = els.grid.querySelector(`.light-card[data-id="${cssEscape(l.id)}"]`);
  if (!card) return false;

  const brightnessPct = l.on ? Math.round(l.brightness) : 0;
  const rgb = l.colorable ? xyToRgb(l.x, l.y, brightnessPct || 40) : null;

  card.classList.toggle('off', !l.on);
  if (rgb) card.style.setProperty('--card-accent', `rgb(${rgb[0]},${rgb[1]},${rgb[2]})`);

  const grad = card.querySelector('.light-card-gradient');
  if (grad && rgb) grad.setAttribute('style', gradientStyleFor(rgb, brightnessPct || 40));

  const power = card.querySelector('[data-action="toggle"]');
  if (power) {
    power.classList.toggle('active', !!l.on);
    power.innerHTML = l.on ? ICONS.powerOn : ICONS.powerOff;
    power.title = HueMuxI18n.t(l.on ? 'lights.turnOff' : 'lights.turnOn');
  }

  const fav = card.querySelector('[data-action="favorite"]');
  if (fav) {
    fav.classList.toggle('active', !!l.favorite);
    fav.innerHTML = l.favorite ? ICONS.star : ICONS.starOutline;
  }

  // Never fight a finger that is mid-drag: editingIds already guards the
  // render path, and this is the same rule at element level.
  const slider = card.querySelector('.brightness-slider');
  if (slider && !editingIds.has(l.id) && document.activeElement !== slider) {
    slider.value = String(brightnessPct);
  }
  return true;
}

// patchRoomTile is the same idea for a room's bulk tile. Its power state
// tracks "any light in the room is on", which the tile renders from the room's
// grouped_light — so a grouped_light event can update it in place too.
function patchRoomTile(room) {
  const tile = els.grid.querySelector(`.all-lights-tile[data-room-id="${cssEscape(room.id)}"]`);
  if (!tile) return false;
  const power = tile.querySelector('[data-action="toggle-room"]');
  if (power) {
    power.classList.toggle('active', !!room.on);
    power.innerHTML = room.on ? ICONS.powerOn : ICONS.powerOff;
    power.title = HueMuxI18n.t(room.on ? 'lights.turnAllOff' : 'lights.turnAllOn');
  }
  const slider = tile.querySelector('.brightness-slider');
  if (slider && !editingIds.has(room.id) && document.activeElement !== slider) {
    slider.value = String(Math.round(room.brightness || 0));
  }
  return true;
}

// patchAllLightsTile updates the group tile in place. Without this it was the
// only card that waited for a full re-render, which is why every real lamp
// reacted instantly and the group one lagged by a second or two.
function patchAllLightsTile() {
  const tile = els.grid.querySelector('.all-lights-tile[data-id="__all__"]');
  if (!tile) return false;
  const anyOn = lights.some((l) => l.on);

  const power = tile.querySelector('[data-action="toggle-all"]');
  if (power) {
    power.classList.toggle('active', anyOn);
    power.innerHTML = anyOn ? ICONS.powerOn : ICONS.powerOff;
    power.title = HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn');
  }

  const grad = tile.querySelector('.light-card-gradient');
  const style = multiGradientStyle(lights);
  if (grad) {
    grad.setAttribute('style', style);
  } else if (style) {
    // The tile renders no gradient element while everything is off, so one
    // has to be created the first time a light comes on.
    const el = document.createElement('div');
    el.className = 'light-card-gradient';
    el.setAttribute('style', style);
    tile.insertBefore(el, tile.firstChild);
  }

  const slider = tile.querySelector('.brightness-slider');
  if (slider && !editingIds.has('__all__') && document.activeElement !== slider) {
    const on = lights.filter((l) => l.on && l.dimmable);
    if (on.length) {
      slider.value = String(Math.round(on.reduce((t, l) => t + l.brightness, 0) / on.length));
    }
  }
  return true;
}

// patchRoomTileFor refreshes the tile of whichever room a light belongs to,
// since a room's aggregate state and colour wash both derive from its lights.
function patchRoomTileFor(l) {
  if (!l || !l.room_id) return;
  const room = rooms.find((r) => r.id === l.room_id);
  if (!room) return;
  const tile = els.grid.querySelector(`.all-lights-tile[data-room-id="${cssEscape(room.id)}"]`);
  if (!tile) return;
  const roomLights = lights.filter((x) => x.room_id === room.id);
  const anyOn = roomLights.some((x) => x.on);
  const power = tile.querySelector('[data-action="toggle-room"]');
  if (power) {
    power.classList.toggle('active', anyOn);
    power.innerHTML = anyOn ? ICONS.powerOn : ICONS.powerOff;
    power.title = HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn');
  }
  const grad = tile.querySelector('.light-card-gradient');
  const style = multiGradientStyle(roomLights);
  if (grad) grad.setAttribute('style', style);
  else if (style) {
    const el = document.createElement('div');
    el.className = 'light-card-gradient';
    el.setAttribute('style', style);
    tile.insertBefore(el, tile.firstChild);
  }
}

// cssEscape — CSS.escape is absent on older WebViews, and light ids are
// bridge-generated UUIDs, so a conservative fallback is enough.
function cssEscape(v) {
  if (window.CSS && CSS.escape) return CSS.escape(v);
  return String(v).replace(/["\\]/g, '\\$&');
}

// scheduleRender coalesces full rebuilds into one per animation frame. A
// room-wide change emits an event per light; without this each one paid for
// its own full rebuild.
let renderQueued = false;
function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  requestAnimationFrame(() => {
    renderQueued = false;
    renderGrid();
  });
}

function mergeLightEvent(ev) {
  if (ev.type === 'light') {
    const l = lights.find((x) => x.id === ev.id);
    if (!l) return;
    if (ev.on !== undefined) l.on = ev.on;
    if (ev.brightness !== undefined) l.brightness = ev.brightness;
    if (ev.x !== undefined) l.x = ev.x;
    if (ev.y !== undefined) l.y = ev.y;

    // Patch just this card. A light_event cannot change the grid's structure
    // — no card appears, disappears or moves — so a full rebuild was always
    // doing ~745ms of work to change a class and an icon.
    if (editingIds.size === 0 && patchLightCard(l)) {
      // The group tile aggregates every light, so a single-light event
      // changes it too — its power state and its colour wash.
      patchAllLightsTile();
      patchRoomTileFor(l);
      return;
    }
  } else if (ev.type === 'grouped_light') {
    const r = rooms.find((x) => x.grouped_light_id === ev.id);
    if (!r) return;
    if (ev.on !== undefined) r.on = ev.on;
    if (ev.brightness !== undefined) r.brightness = ev.brightness;
    if (editingIds.size === 0 && patchRoomTile(r)) return;
  }
  // Fall through only when the affected element is not on screen — the
  // Favorites view, a room filter, or a light that has genuinely appeared.
  if (editingIds.size === 0) scheduleRender();
}

function mergeFavorite(id, fav) {
  if (fav) favoritesRaw[id] = Math.floor(Date.now() / 1000);
  else delete favoritesRaw[id];

  if (id.indexOf('room:') === 0) {
    const r = rooms.find((x) => x.id === id.slice(5));
    if (r) r.favorite = fav;
  } else {
    const l = lights.find((x) => x.id === id);
    if (l) l.favorite = fav;
  }
  // A favourite change can add or remove cards in the Favorites view, so this
  // one genuinely is structural. Coalesced rather than immediate.
  if (editingIds.size === 0) {
    scheduleRender();
    renderZoneScenes();
    renderFilterMenu();
  }
}

// ---------- data fetch ----------

async function fetchLights() {
  const res = await authFetch('/api/lights');
  lights = await res.json();
  loaded = true;
  renderGrid();
}

async function fetchRooms() {
  const res = await authFetch('/api/rooms');
  rooms = await res.json();
  renderFilterMenu();
}

async function fetchScenes() {
  const res = await authFetch('/api/scenes');
  scenes = await res.json();
  renderZoneScenes();
  renderGrid(); // room-scoped scenes now render inline as part of the grid too
}

async function fetchFavorites() {
  const res = await authFetch('/api/favorites');
  favoritesRaw = await res.json();
}

// Runs once, when the WS first reports a paired bridge. Waits for every
// fetch so the empty-favorites fallback below sees complete data rather
// than deciding based on whatever happened to resolve first.
async function initialLoad() {
  await Promise.all([fetchLights(), fetchRooms(), fetchScenes(), fetchFavorites()]);
  if (!filterExplicitFromURL && filter === 'favorites' && !hasAnyFavorites()) {
    // Landing on an empty Favorites view is a dead end for a first-time
    // user — All is the more useful default until they've favorited
    // something.
    filter = 'all';
  }
  renderFilterMenu();
  renderGrid();
  renderZoneScenes();
  // Written once everything has landed rather than per-fetch, so the cache is
  // always a coherent set — a half-written one would render lights with room
  // names and scenes that no longer match them.
  saveCache();
}

function hasAnyFavorites() {
  return lights.some((l) => l.favorite) ||
    scenes.some((sc) => !!favoritesRaw[sc.id]) ||
    rooms.some((r) => !!favoritesRaw['room:' + r.id]) ||
    !!favoritesRaw.all;
}

// Rooms whose bulk tile has been favourited. The Favorites view renders these
// as tiles even though none of their individual lights may be favourited —
// favouriting a room means "give me the whole-room control", not "give me
// every light in it".
function favoritedRooms() {
  return rooms.filter((r) => !!favoritesRaw['room:' + r.id]);
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

// multiGradientStyle blends the colours of several lights into one wash, so
// the all-lights and per-room tiles show what the room actually looks like
// rather than being the only blank cards on screen. Falls back to nothing
// when no colour-capable light is on, which reads correctly as "off".
function multiGradientStyle(list) {
  const on = list.filter((l) => l.on && l.colorable);
  if (!on.length) return '';
  // Cap the number of stops: past a handful they stop being distinguishable
  // and every extra one costs gradient interpolation on a weak GPU.
  const picked = on.slice(0, 5);
  const stops = picked.map((l, i) => {
    const c = xyToRgb(l.x, l.y, Math.max(20, Math.round(l.brightness) || 40));
    const pct = picked.length === 1 ? 100 : Math.round((i / (picked.length - 1)) * 100);
    return `rgb(${c[0]},${c[1]},${c[2]}) ${pct}%`;
  });
  if (stops.length === 1) {
    return `background: radial-gradient(circle at 30% 30%, ${stops[0].split(' ')[0]} 0%, transparent 75%);`;
  }
  return `background: linear-gradient(120deg, ${stops.join(', ')});`;
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

// Groups an already-filtered list by room_name, preserving first-seen room
// order (Map iteration order). Shared shape for both lights and scenes.
function groupByRoomName(list, roomNameOf) {
  const byRoom = new Map();
  for (const item of list) {
    const key = roomNameOf(item) || '—';
    if (!byRoom.has(key)) byRoom.set(key, []);
    byRoom.get(key).push(item);
  }
  return byRoom;
}

function renderGrid() {
  const list = filteredLights();
  // The all-lights tile also participates in the Favorites view once it's
  // been favorited itself — the whole point of favoriting it is quick
  // access from that tab.
  const showAllTile = filter === 'all' || (filter === 'favorites' && !!favoritesRaw.all);
  let html = '';
  // Wrapped in .room-group (not just .lights-cards-grid) so its bottom
  // margin matches every room block that follows — previously this and the
  // first room-group butted up against each other with no gap.
  if (showAllTile) html += `<div class="room-group"><div class="lights-cards-grid">${renderAllLightsTile()}</div></div>`;

  if (filter === 'room') {
    // Already scoped to one room by the filter itself — a header repeating
    // that room's name would be redundant. Still gets its own room-scoped
    // all-lights tile and its own (room, not zone) scenes, merged into the
    // same block, same as each room gets in the 'all'/'favorites' case
    // below.
    const room = rooms.find((r) => r.id === filterRoomId);
    const tile = (room && list.length) ? renderRoomTile(room, list) : '';
    const sceneList = sortScenesFavoriteFirst(filteredScenes());
    html += `
      <div class="room-group">
        <div class="lights-cards-grid">${tile}${list.map(renderLightCard).join('')}</div>
        ${sceneList.length ? `<div class="scenes-strip">${sceneList.map(renderSceneChip).join('')}</div>` : ''}
      </div>`;
  } else {
    // 'all' / 'favorites': one merged block per room — header, a
    // room-scoped all-lights tile, that room's lights, and that room's own
    // scenes, all together (entertainment-zone scenes aren't tied to one
    // room and get their own separate section instead — see
    // renderZoneScenes). No room tile in the Favorites view: bulk
    // room-wide control doesn't fit "quick access to what I favorited."
    const byRoomId = new Map();
    for (const l of list) {
      const key = l.room_id || '';
      if (!byRoomId.has(key)) byRoomId.set(key, { name: l.room_name || '—', lights: [] });
      byRoomId.get(key).lights.push(l);
    }
    // A room favourited via its bulk tile has to appear in the Favorites view
    // even when none of its individual lights are favourited — otherwise
    // favouriting a room is a control that does nothing observable. Seed an
    // empty group so the loop below emits its tile.
    if (filter === 'favorites') {
      for (const r of favoritedRooms()) {
        if (!byRoomId.has(r.id)) byRoomId.set(r.id, { name: r.name || '—', lights: [] });
      }
    }
    const { roomScenes } = splitScenesByRoomVsZone(filteredScenes());
    const scenesByRoomId = new Map();
    for (const sc of roomScenes) {
      if (!scenesByRoomId.has(sc.group_id)) scenesByRoomId.set(sc.group_id, []);
      scenesByRoomId.get(sc.group_id).push(sc);
    }

    html += [...byRoomId.entries()].map(([roomId, entry]) => {
      const room = rooms.find((r) => r.id === roomId);
      // Normally no bulk tile in Favorites — "quick access to what I
      // favourited" is not the same as room-wide control. The exception is a
      // room whose tile is itself the favourite, which is the whole point of
      // having favourited it.
      const roomIsFav = room && !!favoritesRaw['room:' + room.id];
      const tile = (room && (filter !== 'favorites' || roomIsFav))
        ? renderRoomTile(room, entry.lights) : '';
      const sceneList = sortScenesFavoriteFirst(scenesByRoomId.get(roomId) || []);
      return `
        <div class="room-group">
          <h3 class="room-header">${escapeHtml(entry.name)}</h3>
          <div class="lights-cards-grid">${tile}${entry.lights.map(renderLightCard).join('')}</div>
          ${sceneList.length ? `<div class="scenes-strip">${sceneList.map(renderSceneChip).join('')}</div>` : ''}
        </div>`;
    }).join('');
  }

  // "No lights found" is a conclusion, and until /api/lights has answered we
  // have not got one — an empty `lights` array before the first fetch just
  // means the fetch is still in flight. Saying so anyway is what produced the
  // flash of "no lights" on every open. An empty *filter* result is different:
  // that is a real answer about a real room, so it still shows.
  if (!list.length && !showAllTile && (loaded || lights.length)) {
    html += `<p class="hint lights-empty">${escapeHtml(HueMuxI18n.t('lights.empty'))}</p>`;
  }
  els.grid.innerHTML = html;
}

// Scenes tied to an actual room (sc.group_id matches something in the
// /api/rooms list) vs. scenes tied to a zone that isn't a room at all —
// most commonly the entertainment zone screen-sync uses, which spans
// multiple rooms and so doesn't belong under any single room's header.
function splitScenesByRoomVsZone(list) {
  const roomIds = new Set(rooms.map((r) => r.id));
  const roomScenes = [];
  const zoneScenes = [];
  for (const sc of list) {
    (roomIds.has(sc.group_id) ? roomScenes : zoneScenes).push(sc);
  }
  return { roomScenes, zoneScenes };
}

function renderLightCard(l) {
  const off = !l.on;
  const brightnessPct = l.on ? Math.round(l.brightness) : 0;
  const rgb = l.colorable ? xyToRgb(l.x, l.y, brightnessPct || 40) : null;
  const gradient = rgb ? gradientStyleFor(rgb, brightnessPct || 40) : '';
  // The light's own colour, exposed as a custom property on the card. The
  // blurred gradient layer conveys it in the full themes; the simple themes
  // drop that layer and use this for a tinted border instead, so the colour
  // survives as information rather than being lost with the decoration.
  // xyToRgb returns [r,g,b]. Indexing it as .r/.g/.b produced
  // "rgb(undefined,undefined,undefined)", which browsers drop as invalid — so
  // the simple theme's tinted border silently never appeared.
  const accent = rgb ? `--card-accent:rgb(${rgb[0]},${rgb[1]},${rgb[2]});` : '';
  // Hidden, not just disabled, in the Favorites view — lights-ui's own rule
  // (showFavoriteButton={currentFilter !== 'favorites'}): favorites are for
  // quick access, and a star sitting right there invites an accidental
  // unfavorite when all you meant to do was tap the power button.
  const showFavBtn = filter !== 'favorites';
  return `
    <div class="light-card ${off ? 'off' : ''}" data-id="${escapeHtml(l.id)}" style="${accent}">
      ${gradient ? `<div class="light-card-gradient" style="${gradient}"></div>` : ''}
      <div class="light-card-head">
        <h3 title="${escapeHtml(l.name)}"><span>${escapeHtml(l.name)}</span></h3>
        <div class="light-card-actions">
          ${showFavBtn ? `<button type="button" class="icon-btn ${l.favorite ? 'active' : ''}" data-action="favorite" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${l.favorite ? ICONS.star : ICONS.starOutline}</button>` : ''}
          ${l.colorable ? `<button type="button" class="icon-btn" data-action="color" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t('lights.chooseColor'))}">${ICONS.palette}</button>` : ''}
          <button type="button" class="icon-btn ${l.on ? 'active' : ''}" data-action="toggle" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t(l.on ? 'lights.turnOff' : 'lights.turnOn'))}">${l.on ? ICONS.powerOn : ICONS.powerOff}</button>
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
  const allFav = !!favoritesRaw.all;
  const showFavBtn = filter !== 'favorites';
  const allGradient = multiGradientStyle(lights);
  return `
    <div class="light-card all-lights-tile" data-id="__all__">
      ${allGradient ? `<div class="light-card-gradient" style="${allGradient}"></div>` : ''}
      <div class="light-card-head">
        <h3>${ICONS.lightbulb}<span>${escapeHtml(HueMuxI18n.t('lights.allLights'))}</span></h3>
        <div class="light-card-actions">
          ${showFavBtn ? `<button type="button" class="icon-btn ${allFav ? 'active' : ''}" data-action="favorite" data-id="all" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${allFav ? ICONS.star : ICONS.starOutline}</button>` : ''}
          ${hasColor ? `<button type="button" class="icon-btn" data-action="color-all" title="${escapeHtml(HueMuxI18n.t('lights.chooseColorAll'))}">${ICONS.palette}</button>` : ''}
          <button type="button" class="icon-btn ${anyOn ? 'active' : ''}" data-action="toggle-all" title="${escapeHtml(HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn'))}">${anyOn ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${hasBrightness ? `<input type="range" class="brightness-slider" min="0" max="100" value="${avgBrightness}" data-action="brightness-all">` : ''}
    </div>`;
}

// Per-room equivalent of renderAllLightsTile — same idea (aggregate
// toggle/brightness/color), scoped to one room instead of every light on
// the bridge. Toggle/brightness go through the real room_toggle/
// room_brightness WS messages (Room.GroupedLightID, already wired up
// server-side since M2) rather than a client-side fan-out; color has no
// room-level CLIP v2 primitive, so that one *does* fan out to just this
// room's colorable lights, same technique as the global tile's color-all.
// No favorite star here — favoriting is per-light/per-scene/the global
// "all", not per-room, at least for now.
function renderRoomTile(room, roomLights) {
  const anyOn = roomLights.some((l) => l.on);
  const roomFav = !!favoritesRaw['room:' + room.id];
  const roomGradient = multiGradientStyle(roomLights);
  // Same rule as light cards and scene chips: no star in the Favorites view,
  // where it would sit under the thumb inviting an accidental unfavourite.
  const showFavBtn = filter !== 'favorites';
  const hasBrightness = roomLights.some((l) => l.dimmable);
  const hasColor = roomLights.some((l) => l.colorable);
  const onLights = roomLights.filter((l) => l.on && l.dimmable);
  const avgBrightness = onLights.length
    ? Math.round(onLights.reduce((sum, l) => sum + l.brightness, 0) / onLights.length)
    : 50;
  return `
    <div class="light-card all-lights-tile" data-room-id="${escapeHtml(room.id)}">
      ${roomGradient ? `<div class="light-card-gradient" style="${roomGradient}"></div>` : ''}
      <div class="light-card-head">
        <h3>${ICONS.lightbulb}<span>${escapeHtml(HueMuxI18n.t('lights.allInRoom'))}</span></h3>
        <div class="light-card-actions">
          ${showFavBtn ? `<button type="button" class="icon-btn ${roomFav ? 'active' : ''}" data-action="favorite" data-id="room:${escapeHtml(room.id)}" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${roomFav ? ICONS.star : ICONS.starOutline}</button>` : ''}
          ${hasColor ? `<button type="button" class="icon-btn" data-action="color-room" data-room-id="${escapeHtml(room.id)}" title="${escapeHtml(HueMuxI18n.t('lights.chooseColorAll'))}">${ICONS.palette}</button>` : ''}
          <button type="button" class="icon-btn ${anyOn ? 'active' : ''}" data-action="toggle-room" data-room-id="${escapeHtml(room.id)}" data-id="${escapeHtml(room.grouped_light_id)}" title="${escapeHtml(HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn'))}">${anyOn ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${hasBrightness ? `<input type="range" class="brightness-slider" min="0" max="100" value="${avgBrightness}" data-action="brightness-room" data-id="room:${escapeHtml(room.grouped_light_id)}">` : ''}
    </div>`;
}

// Scenes are tied to a room/zone (group_id) — filtered to match whatever
// the light grid is currently showing, rather than always listing every
// scene from every room regardless of context.
function filteredScenes() {
  if (filter === 'room' && filterRoomId) return scenes.filter((sc) => sc.group_id === filterRoomId);
  if (filter === 'favorites') return scenes.filter((sc) => !!favoritesRaw[sc.id]);
  return scenes; // 'all': unfiltered, matching the light grid's own "everything" view
}

// Favorited scenes first — same idea as the light grid, just scoped to
// whatever room (or "all") is currently in view rather than global.
function sortScenesFavoriteFirst(list) {
  return [...list].sort((a, b) => (favoritesRaw[b.id] ? 1 : 0) - (favoritesRaw[a.id] ? 1 : 0));
}

// One pill, not two adjacent buttons: the left/main zone recalls the scene,
// the right zone (present only outside the Favorites view) toggles its
// favorite star — both live inside the same .scene-chip element so there's
// a single visual tag, not a chip plus a separate button bolted on next to
// it. When the star zone is absent (Favorites view), .scene-chip-main
// naturally fills the whole chip, so a click anywhere just recalls —
// there's no dead zone that could be mistaken for an unfavorite control.
function renderSceneChip(sc) {
  const swatches = sc.swatches.slice(0, 4).map((sw) => {
    const [r, g, b] = xyToRgb(sw.x, sw.y);
    return `<span class="scene-swatch" style="background: rgb(${r},${g},${b})"></span>`;
  }).join('');
  const title = sc.group_name ? `${sc.name} — ${sc.group_name}` : sc.name;
  const fav = !!favoritesRaw[sc.id];
  const showFavBtn = filter !== 'favorites';
  return `
    <div class="scene-chip" title="${escapeHtml(title)}">
      <span class="scene-chip-main" data-action="recall" data-scene-id="${escapeHtml(sc.id)}">
        <span class="scene-swatches">${swatches}</span>
        <span class="scene-name">${escapeHtml(sc.name)}</span>
        ${sc.auto_dynamic ? `<span class="scene-dynamic-badge" title="${escapeHtml(HueMuxI18n.t('lights.sceneDynamic'))}">&#10022;</span>` : ''}
      </span>
      ${showFavBtn ? `<span class="scene-chip-star ${fav ? 'active' : ''}" data-action="favorite" data-id="${escapeHtml(sc.id)}" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${fav ? ICONS.star : ICONS.starOutline}</span>` : ''}
    </div>`;
}

// Entertainment-zone-scoped scenes only (see splitScenesByRoomVsZone) — a
// room's own scenes are rendered inline as part of its block in
// renderGrid() instead, alongside that room's lights and its all-lights
// tile.
function renderZoneScenes() {
  if (filter === 'room') {
    // A specific room has no entertainment-zone scenes of its own by
    // definition — its own (room-scoped) scenes are already shown inline
    // in renderGrid().
    els.scenesSection.hidden = true;
    return;
  }

  const { zoneScenes } = splitScenesByRoomVsZone(filteredScenes());
  if (!zoneScenes.length) { els.scenesSection.hidden = true; return; }
  els.scenesSection.hidden = false;

  if (filter === 'favorites') {
    // Grouped by zone with its own header, in case favorited scenes span
    // more than one entertainment zone.
    els.scenesStrip.classList.add('grouped');
    const byZone = groupByRoomName(zoneScenes, (sc) => sc.group_name);
    els.scenesStrip.innerHTML = [...byZone.entries()].map(([zoneName, scs]) => `
      <div class="scenes-room-group">
        <h3 class="scenes-room-header">${escapeHtml(zoneName)}</h3>
        <div class="scenes-strip">${sortScenesFavoriteFirst(scs).map(renderSceneChip).join('')}</div>
      </div>`).join('');
  } else {
    els.scenesStrip.classList.remove('grouped');
    els.scenesStrip.innerHTML = sortScenesFavoriteFirst(zoneScenes).map(renderSceneChip).join('');
  }
}

function renderFilterMenu() {
  const items = [
    { key: 'favorites', label: HueMuxI18n.t('lights.filterFavorites') },
    { key: 'all', label: HueMuxI18n.t('lights.filterAll') },
  ].concat(rooms.map((r) => ({ key: 'room:' + r.id, label: r.name })));

  els.filterList.innerHTML = items.map((it) => {
    const active =
      (filter === 'favorites' && it.key === 'favorites') ||
      (filter === 'all' && it.key === 'all') ||
      (filter === 'room' && it.key === 'room:' + filterRoomId);
    return `<button type="button" class="hm-dropdown-item ${active ? 'active' : ''}" data-key="${escapeHtml(it.key)}">${escapeHtml(it.label)}</button>`;
  }).join('');

  updateFilterSummary();
}

function updateFilterSummary() {
  let label = HueMuxI18n.t('lights.filterCategories');
  if (filter === 'favorites') label = HueMuxI18n.t('lights.filterFavorites');
  else if (filter === 'all') label = HueMuxI18n.t('lights.filterAll');
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
  // The worst case for round-trip latency: one message and one returning
  // event per light. Reflect all of them immediately.
  for (const l of lights) { l.on = target; patchLightCard(l); }
  for (const r of rooms) { r.on = target; patchRoomTile(r); }
  patchAllLightsTile();
}

function actionColorAll(r, g, b) {
  lights.filter((l) => l.colorable).forEach((l) => send({ type: 'light_color', rid: l.id, r, g, b }));
}

// id is either a light id, the sentinel "__all__", or "room:<grouped_light_id>"
// (the room tile's slider — prefixed since, unlike a light id, it isn't
// self-describing on its own).
function scheduleBrightness(id, pct) {
  editingIds.add(id);
  clearTimeout(brightnessTimers[id]);
  brightnessTimers[id] = setTimeout(() => {
    if (id === '__all__') {
      lights.filter((l) => l.dimmable).forEach((l) => send({ type: 'light_brightness', rid: l.id, brightness: pct }));
    } else if (id.indexOf('room:') === 0) {
      send({ type: 'room_brightness', rid: id.slice(5), brightness: pct });
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
  } else if (id.indexOf('room:') === 0) {
    const room = rooms.find((r) => r.grouped_light_id === id.slice(5));
    if (room) {
      lights.filter((l) => l.room_id === room.id && l.dimmable).forEach((l) => { l.brightness = pct; l.on = pct > 0; });
    }
  } else {
    const l = lights.find((x) => x.id === id);
    if (l) { l.brightness = pct; l.on = pct > 0; }
  }
}

// ---------- color picker ----------

// targetId is a light id, "room:<roomId>" (fans out to that room's
// colorable lights — CLIP v2 has no room-level color PUT), or null (every
// colorable light on the bridge).
function openColorPicker(targetId) {
  const overlay = document.createElement('div');
  overlay.className = 'color-picker-overlay';
  overlay.setAttribute('role', 'application');
  let name = HueMuxI18n.t('lights.allLights');
  if (targetId && targetId.indexOf('room:') === 0) {
    name = (rooms.find((r) => r.id === targetId.slice(5)) || {}).name || '';
  } else if (targetId) {
    name = (lights.find((l) => l.id === targetId) || {}).name || '';
  }
  overlay.setAttribute('aria-label', HueMuxI18n.t('lights.colorPickerTitle', { name }));
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
    // Measure the canvas rather than subtracting a constant for the header
    // and footer. The old `overlay.clientHeight - 180` hardcoded their
    // combined height into JS, so it silently drifted whenever lights.css
    // changed and left almost no canvas on a short screen in landscape. The
    // canvas is `flex: 1` in a column, so its own laid-out box is already the
    // right answer.
    canvas.width = canvas.clientWidth;
    canvas.height = canvas.clientHeight;
    renderGradient();
  }

  function flush() {
    rafScheduled = false;
    if (!pendingColor) return;
    const { r, g, b } = pendingColor;
    pendingColor = null;
    if (targetId && targetId.indexOf('room:') === 0) {
      const roomId = targetId.slice(5);
      lights.filter((l) => l.room_id === roomId && l.colorable).forEach((l) => send({ type: 'light_color', rid: l.id, r, g, b }));
    } else if (targetId) {
      send({ type: 'light_color', rid: targetId, r, g, b });
    } else {
      actionColorAll(r, g, b);
    }
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
  // Room-embedded scenes (see renderGrid) are <span>s, not <button>s, same
  // as the entertainment-zone scenes strip — checked first since they'd
  // never match the button[data-action] selector below.
  const star = e.target.closest('.scene-chip-star');
  if (star) {
    send({ type: 'light_favorite', rid: star.dataset.id });
    return;
  }
  const sceneMain = e.target.closest('.scene-chip-main');
  if (sceneMain) {
    send({ type: 'scene_recall', rid: sceneMain.dataset.sceneId });
    return;
  }

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
      const next = !(l && l.on);
      send({ type: 'light_toggle', rid: id, on: next });
      // Optimistic: show the new state now rather than after the round trip.
      // Measured at ~460ms on a LAN deployment — the HTTPS PUT to the bridge,
      // the bridge acting on it, and the eventstream reporting back — all of
      // which was visible as dead time where a tap appeared to do nothing.
      // The event still arrives and reconciles; if the bridge rejects the
      // change or the light is unreachable, the next event corrects this.
      if (l) {
        l.on = next;
        patchLightCard(l);
        patchAllLightsTile();
        patchRoomTileFor(l);
      }
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
    case 'toggle-room': {
      const roomId = btn.dataset.roomId;
      const anyOn = lights.filter((l) => l.room_id === roomId).some((l) => l.on);
      const next = !anyOn;
      send({ type: 'room_toggle', rid: id, on: next });
      // Optimistic, and worth more here than for a single light: a room-wide
      // change produces one event per light, so without this the room appears
      // to change one card at a time over a second or more.
      const room = rooms.find((r) => r.id === roomId);
      if (room) { room.on = next; patchRoomTile(room); }
      for (const l of lights) {
        if (l.room_id === roomId) { l.on = next; patchLightCard(l); }
      }
      patchAllLightsTile();
      break;
    }
    case 'color-room':
      openColorPicker('room:' + btn.dataset.roomId);
      break;
  }
});

els.grid.addEventListener('input', (e) => {
  const el = e.target;
  if (el.dataset.action === 'brightness' || el.dataset.action === 'brightness-room') {
    // el.dataset.id already carries the "room:" prefix for brightness-room.
    scheduleBrightness(el.dataset.id, parseInt(el.value, 10));
  } else if (el.dataset.action === 'brightness-all') {
    scheduleBrightness('__all__', parseInt(el.value, 10));
  }
});

els.stopStreamingBtn.addEventListener('click', () => {
  send({ type: 'stop' });
  // Stopping the stream is not the same as stopping the capture, and on
  // Android this button used to do only the first. The screen-capture service
  // kept running with no stream to feed: the notification stayed up, the
  // screen was still being mirrored, and the Sync tab — seeing no stream —
  // showed "Start", so there was nothing left in the app that could stop it.
  // The user had to go to Android's own recording indicator.
  //
  // Called directly rather than routed through the Sync page, which may not
  // even be loaded under a lights-only profile. The bridge is reachable from
  // any frame of the app; on desktop it simply is not there.
  try {
    const n = window.HueMuxNative || (window.top && window.top.HueMuxNative);
    if (n && typeof n.stopCapture === 'function') n.stopCapture();
  } catch (e) {
    // Nothing to recover: the stream stop above has already been sent.
  }
});

els.filterList.addEventListener('click', (e) => {
  const btn = e.target.closest('.hm-dropdown-item');
  if (!btn) return;
  const key = btn.dataset.key;
  if (key === 'favorites') { filter = 'favorites'; filterRoomId = null; }
  else if (key === 'all') { filter = 'all'; filterRoomId = null; }
  else if (key.indexOf('room:') === 0) { filter = 'room'; filterRoomId = key.slice(5); }
  filterExplicitFromURL = true;
  persistFilterToURL();
  // shared/dropdown.js already closes it on any item click; this stays so the
  // page does not depend on that script having loaded to remain usable.
  els.filterDetails.open = false;
  renderFilterMenu();
  renderGrid();
  renderZoneScenes();
});

els.scenesStrip.addEventListener('click', (e) => {
  const star = e.target.closest('.scene-chip-star');
  if (star) {
    send({ type: 'light_favorite', rid: star.dataset.id });
    return;
  }
  const main = e.target.closest('.scene-chip-main');
  if (!main) return;
  send({ type: 'scene_recall', rid: main.dataset.sceneId });
});

document.addEventListener('huemux:langchange', () => {
  renderFilterMenu();
  renderGrid();
  renderZoneScenes();
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
  if (f === 'favorites' || f === 'all') { filter = f; filterRoomId = null; filterExplicitFromURL = true; }
  else if (f === 'room' && r) { filter = 'room'; filterRoomId = r; filterExplicitFromURL = true; }
}

// ---------- init ----------

restoreFilterFromURL();
HueMuxFeatures.load();

// Paint the last known state before anything asynchronous starts. Revealing
// #app here is deliberate: it is normally gated on the server confirming a
// paired bridge, but a cache can only exist if we were paired when it was
// written, and the alternative is a blank screen for as long as the WebSocket
// takes to connect. If the bridge really has been unpaired since, the first
// status message hides it again — a brief wrong guess in the rare case, in
// exchange for an instant open in the common one.
const hydrated = loadCache();
if (hydrated) {
  // Same fallback initialLoad applies, applied to the cached data too —
  // otherwise a user with no favourites gets an empty Favorites view painted
  // instantly and then swapped for All a moment later, which is a worse first
  // impression than the blank screen this is meant to remove.
  if (!filterExplicitFromURL && filter === 'favorites' && !hasAnyFavorites()) filter = 'all';
  els.unpaired.hidden = true;
  els.app.hidden = false;
  renderFilterMenu();
  renderGrid();
  renderZoneScenes();
}

HueMuxI18n.init().then(() => {
  renderFilterMenu();
  renderGrid();
});
connect();
