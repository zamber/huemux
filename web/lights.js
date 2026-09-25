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
  chevronRight: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="9 6 15 12 9 18"/></svg>',
  roomHome: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 11l9-7 9 7"/><path d="M5 9.5V20h14V9.5"/></svg>',
  roomSofa: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 11V8a3 3 0 0 1 3-3h8a3 3 0 0 1 3 3v3"/><path d="M3 15a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v2a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M5 19v-2"/><path d="M19 19v-2"/></svg>',
  roomBed: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 7v11"/><path d="M3 15h18v3"/><path d="M21 15v-4a2 2 0 0 0-2-2h-8v6"/></svg>',
  roomKitchen: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 10h16v9a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z"/><path d="M8 7V5a1 1 0 0 1 1-1h2v3"/><path d="M16 7V5a1 1 0 0 1 1-1h2v3"/><path d="M12 13v3"/></svg>',
  roomDining: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 3v7a3 3 0 0 0 3 3v8"/><path d="M19 3v7a3 3 0 0 1-3 3v8"/><path d="M5 3c1.5 2 1.5 5 0 7"/><path d="M19 3c-1.5 2-1.5 5 0 7"/></svg>',
  roomBath: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 12h16v3a4 4 0 0 1-4 4H8a4 4 0 0 1-4-4z"/><path d="M6 12V5a2 2 0 0 1 2-2h1.5"/><path d="M7 21l-1 1.5"/><path d="M17 21l1 1.5"/></svg>',
  roomOffice: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="4" y="3" width="16" height="18" rx="1"/><path d="M9 3v4h6V3"/><path d="M9 11h6"/><path d="M9 15h6"/><path d="M12 3v18"/></svg>',
  roomGym: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6.5 6.5v11M17.5 6.5v11M3 9v6M21 9v6M6.5 12h11"/></svg>',
  roomPlant: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 21v-8"/><path d="M12 13c0-3-2-5-5-5 0 3 2 5 5 5z"/><path d="M12 11c0-3 2-5 5-5 0 3-2 5-5 5z"/><path d="M12 15c0-2-1.5-3.5-3.5-3.5 0 2 1.5 3.5 3.5 3.5z"/></svg>',
  roomDoor: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 21V4a1 1 0 0 1 1-1h12a1 1 0 0 1 1 1v17"/><path d="M15 12h.01"/></svg>',
  // Light-type icons, mapped from the light's own archetype (a different
  // field from the room archetype — a ceiling lamp and a bulb can share a
  // room). Default fallback is lightbulb.
  lightSpot: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="10" r="4"/><path d="M8.5 13.5 5 21"/><path d="M15.5 13.5 19 21"/><path d="M12 6V4"/></svg>',
  lightCeiling: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="4" y1="3" x2="20" y2="3"/><path d="M12 3v3"/><circle cx="12" cy="12" r="6"/></svg>',
  lightFloor: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 21v-8"/><path d="M8 21h8"/><path d="M12 13 8 7h8z"/></svg>',
  lightTable: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 21v-6"/><path d="M8 21h8"/><path d="M6 9h12l-1.5 6h-9z"/></svg>',
  lightStrip: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="3" y="9" width="18" height="6" rx="3"/></svg>',
  lightGlobe: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="10" r="6"/><path d="M12 16v5"/><path d="M9 21h6"/></svg>',
  lightWall: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M7 21V8"/><path d="M7 8h6v6H7z"/><path d="M13 11h3"/></svg>',
};

// Hue room archetypes (CLIP v2 metadata.archetype) mapped to the icon set
// above. Unknown or absent archetypes fall back to roomHome — never an empty
// icon, so a room header always has a shape to scan for.
const ARCHETYPE_ICONS = {
  home: 'roomHome', garage: 'roomHome', driveway: 'roomHome', carport: 'roomHome',
  storage: 'roomHome', other: 'roomHome', zone: 'roomHome',
  living_room: 'roomSofa', lounge: 'roomSofa', recreation: 'roomSofa',
  man_cave: 'roomSofa', tv: 'roomSofa', music: 'roomSofa',
  bedroom: 'roomBed', kids_bedroom: 'roomBed', guest_room: 'roomBed', nursery: 'roomBed',
  kitchen: 'roomKitchen', barbecue: 'roomKitchen',
  dining: 'roomDining',
  bathroom: 'roomBath', toilet: 'roomBath', laundry_room: 'roomBath',
  office: 'roomOffice', computer: 'roomOffice', studio: 'roomOffice',
  reading: 'roomOffice', closet: 'roomOffice',
  gym: 'roomGym',
  garden: 'roomPlant', terrace: 'roomPlant', balcony: 'roomPlant',
  porch: 'roomPlant', pool: 'roomPlant',
  hallway: 'roomDoor', front_door: 'roomDoor', staircase: 'roomDoor',
  downstairs: 'roomDoor', upstairs: 'roomDoor', top_floor: 'roomDoor', attic: 'roomDoor',
};

function iconForArchetype(archetype) {
  return ICONS[ARCHETYPE_ICONS[archetype] || 'roomHome'];
}

// Light archetypes (CLIP v2 metadata.archetype on the light resource — the
// physical lamp type, not the room it is in) grouped into the icon shapes a
// glance can tell apart. Unknown or absent → lightbulb.
const LIGHT_ARCHETYPE_ICONS = {
  spotlight_bulb: 'lightSpot', flood_bulb: 'lightSpot', single_spot: 'lightSpot',
  double_spot: 'lightSpot', wall_spot: 'lightSpot', ground_spot: 'lightSpot',
  ceiling_round: 'lightCeiling', ceiling_horizontal: 'lightCeiling',
  ceiling_square: 'lightCeiling', ceiling_tube: 'lightCeiling',
  ceiling_spot: 'lightCeiling', ceiling_lamp: 'lightCeiling',
  pendant_round: 'lightCeiling', pendant_long: 'lightCeiling',
  recessed_ceiling: 'lightCeiling', recessed_floor: 'lightCeiling',
  floor_shade: 'lightFloor', floor_lantern: 'lightFloor', bollard: 'lightFloor',
  table_shade: 'lightTable',
  hue_lightstrip: 'lightStrip', flexible_lamp: 'lightStrip',
  string_light: 'lightStrip', hue_play: 'lightStrip', hue_tube: 'lightStrip',
  hue_signe: 'lightStrip', wall_washer: 'lightStrip',
  hue_go: 'lightGlobe', hue_iris: 'lightGlobe', hue_bloom: 'lightGlobe',
  wall_shade: 'lightWall', wall_lantern: 'lightWall',
};

function iconForLight(l) {
  return ICONS[LIGHT_ARCHETYPE_ICONS[l.archetype] || 'lightbulb'];
}

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

// ---------- collapsible rooms + column count ----------
//
// Both are per-device presentation preferences, like the theme: localStorage,
// synced across the shell's iframes via the native `storage` event (the
// settings frame writes, this frame re-renders). Collapsed state is a JSON
// array of room ids; the column count is one of "1".."4".

const COLLAPSED_KEY = 'huemux.collapsedRooms';
const collapsedRooms = new Set();

function loadCollapsedRooms() {
  collapsedRooms.clear();
  try {
    const raw = localStorage.getItem(COLLAPSED_KEY);
    if (!raw) return;
    const list = JSON.parse(raw);
    if (!Array.isArray(list)) return;
    for (const id of list) if (typeof id === 'string') collapsedRooms.add(id);
  } catch (e) {
    // Same policy as the offline cache: bad storage is not worth failing over.
  }
}

function persistCollapsedRooms() {
  try {
    localStorage.setItem(COLLAPSED_KEY, JSON.stringify([...collapsedRooms]));
  } catch (e) {}
}

const COLUMNS_KEY = 'huemux.lightsColumns';
let lightsColumns = 'auto'; // 'auto' = responsive default; else "1".."4" user override

function loadColumnsPref() {
  try {
    const v = localStorage.getItem(COLUMNS_KEY);
    lightsColumns = ['1', '2', '3', '4', 'auto'].indexOf(v) >= 0 ? v : 'auto';
  } catch (e) {
    lightsColumns = 'auto';
  }
}

// Inline on each .lights-cards-grid when the user has overridden the count;
// absent otherwise (and for 'auto'), so the responsive defaults in
// lights.css apply.
function gridStyleAttr() {
  return lightsColumns && lightsColumns !== 'auto' ? ` style="--light-cols:${lightsColumns}"` : '';
}

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
      // The button also appears when another instance holds the area (the
      // snapshot's busy state): this local instance's "stop" then releases
      // that stream on the bridge — the only way to take over an area
      // someone else is streaming.
      els.stopStreamingBtn.hidden = !(msg.snapshot &&
        (msg.snapshot.StreamActive || msg.snapshot.AreaBusyBy));
      break;
    case 'light_event':
      mergeLightEvent(msg.event);
      break;
    case 'lights_snapshot':
      // Full-state resync pushed on connect (the server informs a
      // reconnecting client of the current truth) and in answer to the
      // resync_lights handshake sent when the page comes back to the
      // foreground. The eventstream only carries *future* deltas, so a
      // backgrounded app that missed events while it was away is otherwise
      // stuck on stale state until the next light change happens to arrive.
      lights = Array.isArray(msg.lights) ? msg.lights : [];
      rooms = Array.isArray(msg.rooms) ? msg.rooms : [];
      loaded = true;
      renderFilterMenu();
      renderGrid();
      renderZoneScenes();
      saveCache();
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
  const rgb = cardRgbFor(l, brightnessPct);

  card.classList.toggle('off', !l.on);
  if (rgb) {
    card.style.setProperty('--card-accent', `rgb(${rgb[0]},${rgb[1]},${rgb[2]})`);
    card.style.setProperty('--card-accent-soft', `rgba(${rgb[0]},${rgb[1]},${rgb[2]},0.35)`);
  }

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
    if (rgb) {
      slider.style.setProperty('--slider-fill', `rgb(${rgb[0]},${rgb[1]},${rgb[2]})`);
      slider.style.setProperty('--slider-pct', brightnessPct + '%');
    }
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
    const pct = Math.round(room.brightness || 0);
    slider.value = String(pct);
    slider.setAttribute('style', multiSliderFillStyle(lights.filter((x) => x.room_id === room.id), pct));
  }
  // The room's color-summary dots derive from its lights' states; a
  // room-wide change (toggle-room, toggle-all) dims or brightens them.
  const card = tile.closest('.room-card');
  if (card) {
    const dots = card.querySelector('.room-dots');
    if (dots) dots.innerHTML = roomDotsFor(lights.filter((x) => x.room_id === room.id));
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

  // The tile's tinted chrome (bulb icon, colour button, active buttons) all
  // read --card-accent — refresh it so colour changes made from another
  // app re-tint the tile without a rebuild.
  const rep = representativeRgb(lights);
  if (rep) tile.style.setProperty('--card-accent', `rgb(${rep[0]},${rep[1]},${rep[2]})`);
  else tile.style.removeProperty('--card-accent');

  const slider = tile.querySelector('.brightness-slider');
  if (slider && !editingIds.has('__all__') && document.activeElement !== slider) {
    const on = lights.filter((l) => l.on && l.dimmable);
    const avg = on.length
      ? Math.round(on.reduce((t, l) => t + l.brightness, 0) / on.length)
      : 0;
    slider.value = String(avg);
    slider.setAttribute('style', multiSliderFillStyle(lights, avg));
  }

  const dots = tile.querySelector('.room-dots');
  if (dots) dots.innerHTML = roomDotsFor(lights);
  return true;
}

// patchRoomTileFor refreshes the tile of whichever room a light belongs to,
// since a room's aggregate state and colour wash both derive from its lights.
function patchRoomTileFor(l) {
  if (!l || !l.room_id) return;
  const room = rooms.find((r) => r.id === l.room_id);
  if (!room) return;
  const roomLights = lights.filter((x) => x.room_id === room.id);
  // The room card's color dots derive from its lights, so every light event
  // refreshes them — including in the Favorites view, where the bulk tile is
  // not rendered and only the card chrome exists to update.
  const card = els.grid.querySelector(`.room-card[data-room-id="${cssEscape(room.id)}"]`);
  if (card) {
    const dots = card.querySelector('.room-dots');
    if (dots) dots.innerHTML = roomDotsFor(roomLights);
  }
  const tile = els.grid.querySelector(`.all-lights-tile[data-room-id="${cssEscape(room.id)}"]`);
  if (!tile) return;
  // Same as patchAllLightsTile: external colour changes must re-tint the
  // room tile's bulb icon, colour button and active state.
  const rep = representativeRgb(roomLights);
  if (rep) tile.style.setProperty('--card-accent', `rgb(${rep[0]},${rep[1]},${rep[2]})`);
  else tile.style.removeProperty('--card-accent');
  // Use the room's grouped_light state when available — same logic as
  // renderRoomTile and patchRoomTile, so a grouped_light event reporting
  // off is not overridden by a subsequent per-light event's aggregate.
  const anyOn = room.on !== undefined ? room.on : roomLights.some((x) => x.on);
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
    if (ev.x !== undefined) {
      l.x = ev.x;
      l.y = ev.y;
      // An xy delta is an authoritative switch back to color mode; the
      // server drops the mirek_valid:false companion events, so this is
      // what actually clears a stale mirek on the client.
      l.mirek = 0;
      noteColorEvent(ev.id);
    }
    if (ev.mirek !== undefined) {
      l.mirek = ev.mirek;
      noteColorEvent(ev.id);
    }

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

// rgbToHsv is hsvToRgb's inverse — used to derive the hover variant of a
// scene chip's tint by boosting saturation.
function rgbToHsv(r, g, b) {
  r /= 255; g /= 255; b /= 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const d = max - min;
  let h = 0;
  if (d > 0) {
    if (max === r) h = 60 * (((g - b) / d) % 6);
    else if (max === g) h = 60 * ((b - r) / d + 2);
    else h = 60 * ((r - g) / d + 4);
  }
  if (h < 0) h += 360;
  const s = max === 0 ? 0 : (d / max) * 100;
  return [h, s, max * 100];
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

// The one color a light card renders: mirek-preferred — a light in CT mode
// reports its white point in mirek, and deriving a white from its (possibly
// stale) xy would tint the card wrong. Falls back to xy for color lights and
// to null for plain white bulbs.
function cardRgbFor(l, brightnessPct) {
  if (l.mirek) return kelvinToRgb(1e6 / l.mirek);
  if (l.colorable) return xyToRgb(l.x, l.y, brightnessPct || 40);
  return null;
}

// Inline custom properties that paint a light card's brightness slider with
// the light's own color up to the current level (theme.css turns them into
// the track gradient; accent-color alone never showed because the custom
// track background overrides it).
function sliderFillStyle(rgb, pct) {
  if (!rgb) return '';
  return `--slider-fill:rgb(${rgb[0]},${rgb[1]},${rgb[2]});--slider-pct:${pct}%;`;
}

// One representative color for an aggregate tile's palette button — the
// first light that has a usable color (xy or mirek), whether it is on or
// off, so the button keeps showing the tile's selected colour.
function representativeRgb(list) {
  for (const l of list) {
    const rgb = cardRgbFor(l, 60);
    if (rgb) return rgb;
  }
  return null;
}

// The tile sliders' fill: a gradient of every active light's color across
// the filled portion, trailing into the track — the slider edition of the
// tiles' multiGradientStyle wash. Falls back to '' (plain track) when no
// active light has a color.
function multiSliderFillStyle(list, pct) {
  const colors = [];
  for (const l of list) {
    if (!l.on) continue;
    const rgb = cardRgbFor(l, Math.max(20, Math.round(l.brightness) || 40));
    if (rgb) colors.push(rgb);
    if (colors.length >= 5) break;
  }
  if (!colors.length) return '';
  const stops = colors.map((c, i) => {
    const pos = colors.length === 1 ? pct : Math.round((i / (colors.length - 1)) * pct);
    return `rgb(${c[0]},${c[1]},${c[2]}) ${pos}%`;
  });
  return `--slider-grad:linear-gradient(to right, ${stops.join(',')}, var(--surface-alt) ${pct}%, var(--surface-alt) 100%);`;
}

// The room header's color-summary dots: up to 4 representative colors from
// the room's lights, deduped by xy (or mirek for CT-mode lights). Off lights
// keep their hue but render dimmed — the dot answers "what does this room
// look like", and an off light is still part of the answer.
function roomDotsFor(roomLights) {
  const seen = new Set();
  const dots = [];
  for (const l of roomLights) {
    if (dots.length >= 4) break;
    let rgb = null;
    let key = null;
    if (l.colorable && (l.x || l.y)) {
      rgb = xyToRgb(l.x, l.y, 60);
      key = 'xy' + l.x.toFixed(3) + ',' + l.y.toFixed(3);
    } else if (l.mirek) {
      rgb = kelvinToRgb(1e6 / l.mirek);
      key = 'ct' + l.mirek;
    }
    if (!rgb || seen.has(key)) continue;
    seen.add(key);
    dots.push(`<span class="room-dot ${l.on ? '' : 'room-dot-off'}" style="background: rgb(${rgb[0]},${rgb[1]},${rgb[2]})"></span>`);
  }
  return dots.join('');
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
  // The all-lights tile sits above the room cards, outside any of them —
  // the global control is deliberately not a room.
  if (showAllTile) html += `<div class="lights-cards-grid"${gridStyleAttr()}>${renderAllLightsTile()}</div>`;

  if (filter === 'room') {
    // Already scoped to one room by the filter itself. The card's header
    // repeats the room name, but that is the point: this filter is an
    // explicit navigation into the room, so it always renders expanded and
    // ignores the persisted collapsed state.
    const room = rooms.find((r) => r.id === filterRoomId);
    if (room || list.length) {
      const sceneList = sortScenesFavoriteFirst(filteredScenes());
      html += renderRoomCard(room, list, sceneList, { forceExpanded: true });
    }
  } else {
    // 'all' / 'favorites': one card per room — the room's bulk tile, its
    // lights, and its own scenes, all inside one collapsible surface
    // (entertainment-zone scenes aren't tied to one room and get their own
    // strip at the bottom instead — see renderZoneScenes).
    const byRoomId = new Map();
    for (const l of list) {
      const key = l.room_id || '';
      if (!byRoomId.has(key)) byRoomId.set(key, []);
      byRoomId.get(key).push(l);
    }
    // A room favourited via its bulk tile has to appear in the Favorites view
    // even when none of its individual lights are favourited — otherwise
    // favouriting a room is a control that does nothing observable. Seed an
    // empty group so the loop below emits its card.
    if (filter === 'favorites') {
      for (const r of favoritedRooms()) {
        if (!byRoomId.has(r.id)) byRoomId.set(r.id, []);
      }
    }
    const { roomScenes } = splitScenesByRoomVsZone(filteredScenes());
    const scenesByRoomId = new Map();
    for (const sc of roomScenes) {
      if (!scenesByRoomId.has(sc.group_id)) scenesByRoomId.set(sc.group_id, []);
      scenesByRoomId.get(sc.group_id).push(sc);
    }

    html += [...byRoomId.entries()].map(([roomId, roomLights]) => {
      const room = rooms.find((r) => r.id === roomId);
      const sceneList = sortScenesFavoriteFirst(scenesByRoomId.get(roomId) || []);
      return renderRoomCard(room, roomLights, sceneList);
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
  const rgb = cardRgbFor(l, brightnessPct);
  const gradient = rgb ? gradientStyleFor(rgb, brightnessPct || 40) : '';
  // The light's own colour, exposed as a custom property on the card. The
  // blurred gradient layer conveys it in the full themes; the simple themes
  // drop that layer and use this for a tinted border instead, so the colour
  // survives as information rather than being lost with the decoration.
  // xyToRgb returns [r,g,b]. Indexing it as .r/.g/.b produced
  // "rgb(undefined,undefined,undefined)", which browsers drop as invalid — so
  // the simple theme's tinted border silently never appeared.
  const accent = rgb
    ? `--card-accent:rgb(${rgb[0]},${rgb[1]},${rgb[2]});--card-accent-soft:rgba(${rgb[0]},${rgb[1]},${rgb[2]},0.35);`
    : '';
  // Hidden, not just disabled, in the Favorites view — lights-ui's own rule
  // (showFavoriteButton={currentFilter !== 'favorites'}): favorites are for
  // quick access, and a star sitting right there invites an accidental
  // unfavorite when all you meant to do was tap the power button.
  const showFavBtn = filter !== 'favorites';
  return `
    <div class="light-card ${off ? 'off' : ''}" data-id="${escapeHtml(l.id)}" style="${accent}">
      ${gradient ? `<div class="light-card-gradient" style="${gradient}"></div>` : ''}
      <div class="light-card-head">
        <h3 title="${escapeHtml(l.name)}">${iconForLight(l)}<span>${escapeHtml(l.name)}</span></h3>
        <div class="light-card-actions">
          ${l.colorable || l.ct_capable ? `<button type="button" class="icon-btn" data-action="color" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t(l.colorable ? 'lights.chooseColor' : 'lights.chooseColorTemp'))}">${ICONS.palette}</button>` : ''}
          ${showFavBtn ? `<button type="button" class="icon-btn ${l.favorite ? 'active' : ''}" data-action="favorite" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${l.favorite ? ICONS.star : ICONS.starOutline}</button>` : ''}
          <button type="button" class="icon-btn ${l.on ? 'active' : ''}" data-action="toggle" data-id="${escapeHtml(l.id)}" title="${escapeHtml(HueMuxI18n.t(l.on ? 'lights.turnOff' : 'lights.turnOn'))}">${l.on ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${l.dimmable ? `<input type="range" class="brightness-slider" min="0" max="100" value="${brightnessPct}" data-action="brightness" data-id="${escapeHtml(l.id)}" style="${sliderFillStyle(rgb, brightnessPct)}">` : ''}
    </div>`;
}

function renderAllLightsTile() {
  const anyOn = lights.some((l) => l.on);
  const hasBrightness = lights.some((l) => l.dimmable);
  const hasColor = lights.some((l) => l.colorable || l.ct_capable);
  const onLights = lights.filter((l) => l.on && l.dimmable);
  const avgBrightness = onLights.length
    ? Math.round(onLights.reduce((sum, l) => sum + l.brightness, 0) / onLights.length)
    : 50;
  const allFav = !!favoritesRaw.all;
  const showFavBtn = filter !== 'favorites';
  const allGradient = multiGradientStyle(lights);
  const repRgb = representativeRgb(lights);
  const repAccent = repRgb ? `--card-accent:rgb(${repRgb[0]},${repRgb[1]},${repRgb[2]});` : '';
  return `
    <div class="light-card all-lights-tile" data-id="__all__" style="${repAccent}">
      ${allGradient ? `<div class="light-card-gradient" style="${allGradient}"></div>` : ''}
      <div class="light-card-head">
        <h3 title="${escapeHtml(HueMuxI18n.t('lights.allLights'))}">${ICONS.lightbulb}<span>${escapeHtml(HueMuxI18n.t('lights.allLights'))}</span></h3>
        <span class="room-dots" title="${escapeHtml(HueMuxI18n.t('lights.roomColors'))}">${roomDotsFor(lights)}</span>
        <div class="light-card-actions">
          ${hasColor ? `<button type="button" class="icon-btn" data-action="color-all" title="${escapeHtml(HueMuxI18n.t('lights.chooseColorAll'))}">${ICONS.palette}</button>` : ''}
          ${showFavBtn ? `<button type="button" class="icon-btn ${allFav ? 'active' : ''}" data-action="favorite" data-id="all" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${allFav ? ICONS.star : ICONS.starOutline}</button>` : ''}
          <button type="button" class="icon-btn ${anyOn ? 'active' : ''}" data-action="toggle-all" title="${escapeHtml(HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn'))}">${anyOn ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${hasBrightness ? `<input type="range" class="brightness-slider" min="0" max="100" value="${avgBrightness}" data-action="brightness-all" style="${multiSliderFillStyle(lights, avgBrightness)}">` : ''}
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
  // The room's grouped_light state (room.on, set from the /api/rooms
  // response and kept current by mergeLightEvent) is authoritative —
  // a grouped_light reporting off while individual lights still flag
  // on=true must not render an active power button and colour wash.
  // Fall back to the per-light aggregate only when room.on has never
  // been seen (stale localStorage cache from an older version).
  const anyOn = room.on !== undefined ? room.on : roomLights.some((l) => l.on);
  const roomFav = !!favoritesRaw['room:' + room.id];
  const roomGradient = multiGradientStyle(roomLights);
  // Same rule as light cards and scene chips: no star in the Favorites view,
  // where it would sit under the thumb inviting an accidental unfavourite.
  const showFavBtn = filter !== 'favorites';
  const hasBrightness = roomLights.some((l) => l.dimmable);
  const hasColor = roomLights.some((l) => l.colorable || l.ct_capable);
  // Prefer the grouped_light's own brightness; fall back to per-light
  // average only when the field is absent (stale cache).
  const roomBrightness = room.brightness !== undefined
    ? Math.round(room.brightness)
    : (roomLights.filter((l) => l.on && l.dimmable).length
        ? Math.round(roomLights.filter((l) => l.on && l.dimmable).reduce((sum, l) => sum + l.brightness, 0) / roomLights.filter((l) => l.on && l.dimmable).length)
        : 50);
  const repRgb = representativeRgb(roomLights);
  const repAccent = repRgb ? `--card-accent:rgb(${repRgb[0]},${repRgb[1]},${repRgb[2]});` : '';
  return `
    <div class="light-card all-lights-tile" data-room-id="${escapeHtml(room.id)}" style="${repAccent}">
      ${roomGradient ? `<div class="light-card-gradient" style="${roomGradient}"></div>` : ''}
      <div class="light-card-head">
        <h3 title="${escapeHtml(HueMuxI18n.t('lights.allInRoom'))}">${ICONS.lightbulb}<span>${escapeHtml(HueMuxI18n.t('lights.allInRoom'))}</span></h3>
        <span class="room-dots" title="${escapeHtml(HueMuxI18n.t('lights.roomColors'))}">${roomDotsFor(roomLights)}</span>
        <div class="light-card-actions">
          ${hasColor ? `<button type="button" class="icon-btn" data-action="color-room" data-room-id="${escapeHtml(room.id)}" title="${escapeHtml(HueMuxI18n.t('lights.chooseColorAll'))}">${ICONS.palette}</button>` : ''}
          ${showFavBtn ? `<button type="button" class="icon-btn ${roomFav ? 'active' : ''}" data-action="favorite" data-id="room:${escapeHtml(room.id)}" title="${escapeHtml(HueMuxI18n.t('lights.toggleFavorite'))}">${roomFav ? ICONS.star : ICONS.starOutline}</button>` : ''}
          <button type="button" class="icon-btn ${anyOn ? 'active' : ''}" data-action="toggle-room" data-room-id="${escapeHtml(room.id)}" data-id="${escapeHtml(room.grouped_light_id)}" title="${escapeHtml(HueMuxI18n.t(anyOn ? 'lights.turnAllOff' : 'lights.turnAllOn'))}">${anyOn ? ICONS.powerOn : ICONS.powerOff}</button>
        </div>
      </div>
      ${hasBrightness ? `<input type="range" class="brightness-slider" min="0" max="100" value="${roomBrightness}" data-action="brightness-room" data-id="room:${escapeHtml(room.grouped_light_id)}" style="${multiSliderFillStyle(roomLights, roomBrightness)}">` : ''}
    </div>`;
}

// The collapsible card every room renders as: a header of pure chrome (icon,
// name, count, color dots, chevron) around a body holding the room's bulk
// tile, its light cards, and its own scenes. Bulk controls deliberately stay
// in the tile inside the body rather than moving to the header — that keeps
// patchRoomTile/patchRoomTileFor working unchanged, which is what keeps
// per-light events O(1) instead of full-grid rebuilds.
function renderRoomCard(room, roomLights, sceneList, opts) {
  const roomId = room ? room.id : '';
  const collapsed = !(opts && opts.forceExpanded) && collapsedRooms.has(roomId);
  const name = room ? room.name : ((roomLights[0] && roomLights[0].room_name) || '—');
  const collapseLabel = HueMuxI18n.t(collapsed ? 'lights.expandRoom' : 'lights.collapseRoom');
  // Normally no bulk tile in Favorites — "quick access to what I favourited"
  // is not the same as room-wide control. The exception is a room whose tile
  // is itself the favourite, which is the whole point of having favourited it.
  const roomIsFav = room && !!favoritesRaw['room:' + room.id];
  const tile = (room && (filter !== 'favorites' || roomIsFav))
    ? renderRoomTile(room, roomLights) : '';
  return `
    <section class="room-card ${collapsed ? 'collapsed' : ''}" data-room-id="${escapeHtml(roomId)}">
      <button type="button" class="room-card-head" data-action="room-collapse"
              data-room-id="${escapeHtml(roomId)}" aria-expanded="${collapsed ? 'false' : 'true'}"
              title="${escapeHtml(collapseLabel)}">
        <span class="room-icon">${iconForArchetype(room ? room.archetype : '')}</span>
        <span class="room-title">${escapeHtml(name)}</span>
        <span class="room-count">${roomLights.length}</span>
        <span class="room-dots" title="${escapeHtml(HueMuxI18n.t('lights.roomColors'))}">${roomDotsFor(roomLights)}</span>
        <span class="room-card-chevron">${ICONS.chevronRight}</span>
      </button>
      <div class="room-card-body"><div class="room-card-body-inner">
        <div class="lights-cards-grid"${gridStyleAttr()}>${tile}${roomLights.map(renderLightCard).join('')}</div>
        ${sceneList.length ? `<div class="scenes-strip">${sceneList.map(renderSceneChip).join('')}</div>` : ''}
      </div></div>
    </section>`;
}

// Max-height accordion — the collapse animation Chromium 83 can actually run
// (grid-template-rows transitions need Chromium 107+). Under data-simple the
// class toggles instantly with no transition at all.
function toggleRoomCollapsed(roomId) {
  const willCollapse = !collapsedRooms.has(roomId);
  if (willCollapse) collapsedRooms.add(roomId);
  else collapsedRooms.delete(roomId);
  // The pseudo-room (lights with no room_id) collapses visually but is not
  // persisted — it has no id to remember it by.
  if (roomId) persistCollapsedRooms();

  const card = els.grid.querySelector(`.room-card[data-room-id="${cssEscape(roomId)}"]`);
  if (!card) return;
  card.classList.toggle('collapsed', willCollapse);
  const head = card.querySelector('.room-card-head');
  head.setAttribute('aria-expanded', String(!willCollapse));
  head.title = HueMuxI18n.t(willCollapse ? 'lights.expandRoom' : 'lights.collapseRoom');

  const body = card.querySelector('.room-card-body');
  if (document.documentElement.hasAttribute('data-simple')) return; // instant, class-driven
  if (willCollapse) {
    body.style.maxHeight = body.scrollHeight + 'px';
    void body.offsetHeight; // force reflow so the transition runs
    body.style.maxHeight = '0px';
  } else {
    body.style.maxHeight = body.scrollHeight + 'px';
    const done = () => {
      body.style.maxHeight = ''; // back to auto so later content growth isn't clipped
      body.removeEventListener('transitionend', done);
    };
    body.addEventListener('transitionend', done);
  }
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
  // The first swatch doubles as the chip's identity tint — a faint wash of
  // it on the border and background so a preset reads as "the warm one"
  // before its name does. The hover variant is the same hue with boosted
  // saturation (HSV), which makes the hover pop on the background and the
  // border instead of cancelling the tint. Inline rgba rather than
  // color-mix(): the wall panel's Chromium 83 predates it.
  const first = sc.swatches[0] ? xyToRgb(sc.swatches[0].x, sc.swatches[0].y) : null;
  let tint = '';
  if (first) {
    const [h, s, v] = rgbToHsv(first[0], first[1], first[2]);
    const [hr, hg, hb] = hsvToRgb(h, Math.min(100, Math.round(s * 1.3 + 20)), v);
    tint = `--chip-color:rgb(${first[0]},${first[1]},${first[2]});` +
      `--chip-tint:rgba(${first[0]},${first[1]},${first[2]},0.10);--chip-tint-strong:rgba(${first[0]},${first[1]},${first[2]},0.35);` +
      `--chip-tint-hover:rgba(${hr},${hg},${hb},0.16);--chip-tint-strong-hover:rgba(${hr},${hg},${hb},0.55);`;
  }
  const title = sc.group_name ? `${sc.name} — ${sc.group_name}` : sc.name;
  const fav = !!favoritesRaw[sc.id];
  const showFavBtn = filter !== 'favorites';
  return `
    <div class="scene-chip" title="${escapeHtml(title)}" style="${tint}">
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
  // Room color dots derive from light state; refresh them per room too
  // (patchRoomTile covers the tile-present case; this covers tiles absent).
  for (const r of rooms) {
    const first = lights.find((l) => l.room_id === r.id);
    if (first) patchRoomTileFor(first);
  }
  patchAllLightsTile();
}

function actionColorAll(r, g, b) {
  lights.filter((l) => l.colorable).forEach((l) => send({ type: 'light_color', rid: l.id, r, g, b }));
}

// White-temperature fan-out, mirroring actionColorAll: mirek goes straight
// to the bridge (CLIP v2 accepts it natively — no xy conversion).
function sendColorTemp(targetId, mirek) {
  if (targetId && targetId.indexOf('room:') === 0) {
    const roomId = targetId.slice(5);
    lights.filter((l) => l.room_id === roomId && l.ct_capable).forEach((l) => send({ type: 'light_color_temp', rid: l.id, mirek }));
  } else if (targetId) {
    send({ type: 'light_color_temp', rid: targetId, mirek });
  } else {
    lights.filter((l) => l.ct_capable).forEach((l) => send({ type: 'light_color_temp', rid: l.id, mirek }));
  }
}

// The light ids a temperature probe would address — mirror of the fan-out in
// sendColorTemp, used to know which light_events confirm a probe.
function colorTempTargetIds(targetId) {
  if (targetId && targetId.indexOf('room:') === 0) {
    return lights.filter((l) => l.room_id === targetId.slice(5) && l.ct_capable).map((l) => l.id);
  }
  if (targetId) return [targetId];
  return lights.filter((l) => l.ct_capable).map((l) => l.id);
}

// Same pair for color probes (sendColorRgb is the picker's RGB send path,
// replacing the old flush() fan-out).
function colorTargetIds(targetId) {
  if (targetId && targetId.indexOf('room:') === 0) {
    return lights.filter((l) => l.room_id === targetId.slice(5) && l.colorable).map((l) => l.id);
  }
  if (targetId) return [targetId];
  return lights.filter((l) => l.colorable).map((l) => l.id);
}

function sendColorRgb(targetId, rgb) {
  const [r, g, b] = rgb;
  if (targetId && targetId.indexOf('room:') === 0) {
    const roomId = targetId.slice(5);
    lights.filter((l) => l.room_id === roomId && l.colorable).forEach((l) => send({ type: 'light_color', rid: l.id, r, g, b }));
  } else if (targetId) {
    send({ type: 'light_color', rid: targetId, r, g, b });
  } else {
    actionColorAll(r, g, b);
  }
}

// ---------- color-probe send pacing ----------
//
// The bridge, not the network, is the slow hop: an unthrottled drag sends a
// probe per animation frame, the bridge applies each change in ~200ms+ and
// its eventstream reports them all back — so after the finger lifts, a tail
// of queued color updates keeps arriving and re-colouring the lamps.
//
// The pacer sends at most one probe at a time and holds the latest pick as
// "pending" until the in-flight probe is confirmed by a matching light_event
// (x/y or mirek). The probe→confirm time feeds an EWMA, which self-corrects
// the pace to whatever the bridge actually delivers. A timeout at
// max(rtt×3, 1s), capped at 4s, releases the budget when an event is lost —
// a missed delta can never wedge the pipeline, it only slows one probe.
let probeInFlight = false;
let probeTargets = new Set(); // light ids the in-flight probe addressed
let probeSentAt = 0;
let probeTimeout = null;
let probeRttMs = 200; // EWMA of probe→confirm roundtrip
let pendingProbe = null; // () => void — latest unsent pick (latest wins)

function maybeSendProbe() {
  if (!pendingProbe || probeInFlight) return;
  probeInFlight = true;
  probeSentAt = performance.now();
  const sendFn = pendingProbe;
  pendingProbe = null;
  sendFn();
  const budgetMs = Math.min(Math.max(probeRttMs * 3, 1000), 4000);
  probeTimeout = setTimeout(() => confirmProbe(null), budgetMs);
}

// lightId is the id from a confirming light_event, or null for the timeout
// path (which releases the budget unconditionally).
function confirmProbe(lightId) {
  if (!probeInFlight) return;
  if (lightId && !probeTargets.has(lightId)) return;
  clearTimeout(probeTimeout);
  probeInFlight = false;
  if (probeSentAt) {
    const sample = performance.now() - probeSentAt;
    if (sample < 4000) probeRttMs = probeRttMs * 0.7 + sample * 0.3;
  }
  probeSentAt = 0;
  probeTargets = new Set();
  if (pendingProbe) maybeSendProbe();
}

// Called from mergeLightEvent for every color/mirek delta: if a probe is in
// flight for that light, its roundtrip is complete.
function noteColorEvent(lightId) {
  if (probeInFlight && probeTargets.has(lightId)) confirmProbe(lightId);
}

// id is either a light id, the sentinel "__all__", or "room:<grouped_light_id>"
// (the room tile's slider — prefixed since, unlike a light id, it isn't
// self-describing on its own).
//
// Brightness moves a light in both directions:
//   - dragging from 0 (or off) to a positive value turns it ON — a bare
//     light_brightness only dims, so a physically-off bulb silently stayed
//     dark while the UI claimed it was on;
//   - dragging from a positive value to 0 turns it OFF. It used to dim to the
//     floor and stay on, which left a bulb burning at its minimum for no
//     reason and needed a second touch in the same spot to finish the job.
// The "previous" value is what the user last committed via a slider
// (lastBrightnessPct), falling back to the live model on first touch, so a
// light the bridge reports at its dimming floor still reads as 0 here.
const lastBrightnessPct = {};
const brightnessGestures = {};

// A light that is on at the dimming floor reads as 0 even when the bridge
// reports the clamped minimum as 1, so the next move to 0 still turns it off
// rather than dimming it to the same place again.
function lightUserPrev(l) {
  if (!l) return 0;
  const model = l.on ? l.brightness : 0;
  if (lastBrightnessPct.hasOwnProperty(l.id) && lastBrightnessPct[l.id] === 0 && model <= 1) return 0;
  return model;
}

function roomUserPrev(room) {
  if (!room) return 0;
  const model = room.on ? room.brightness : 0;
  const key = 'room:' + room.grouped_light_id;
  if (lastBrightnessPct.hasOwnProperty(key) && lastBrightnessPct[key] === 0 && model <= 1) return 0;
  return model;
}

// Pre-gesture state for a slider id. For "__all__" this is a per-light map;
// for a light or a room it is a single number.
function userPrevFor(id) {
  if (id === '__all__') {
    const prevs = {};
    for (const l of lights) {
      if (l.dimmable) prevs[l.id] = lightUserPrev(l);
    }
    return prevs;
  }
  if (id.indexOf('room:') === 0) {
    return roomUserPrev(rooms.find((r) => r.grouped_light_id === id.slice(5)));
  }
  return lightUserPrev(lights.find((x) => x.id === id));
}

// Three outcomes for a slider: turn on at this level, set this level, or turn
// off. Which one it is depends only on where the level started and where it
// ended, never on how the finger got there.
function brightnessTransition(prev, pct) {
  if (pct === 0) return 'off';
  return prev <= 0 ? 'on' : 'set';
}

function scheduleBrightness(id, pct) {
  // Capture the pre-gesture state on the first input event of a drag. The
  // optimistic updates below mutate the model immediately, so a transition
  // computed later would see the state mid-gesture rather than where the
  // user actually started from.
  if (!brightnessGestures.hasOwnProperty(id)) {
    brightnessGestures[id] = userPrevFor(id);
  }
  editingIds.add(id);
  clearTimeout(brightnessTimers[id]);
  brightnessTimers[id] = setTimeout(() => {
    const prev = brightnessGestures[id];
    delete brightnessGestures[id];
    lastBrightnessPct[id] = pct;

    if (id === '__all__') {
      for (const l of lights) {
        if (!l.dimmable) continue;
        const p = prev && prev.hasOwnProperty(l.id) ? prev[l.id] : (l.on ? l.brightness : 0);
        const t = brightnessTransition(p, pct);
        if (t === 'on') {
          send({ type: 'light_toggle', rid: l.id, on: true });
          send({ type: 'light_brightness', rid: l.id, brightness: pct });
        } else if (t === 'off') {
          send({ type: 'light_toggle', rid: l.id, on: false });
        } else {
          send({ type: 'light_brightness', rid: l.id, brightness: pct });
        }
      }
    } else if (id.indexOf('room:') === 0) {
      const rid = id.slice(5);
      const t = brightnessTransition(prev, pct);
      if (t === 'on') {
        send({ type: 'room_toggle', rid, on: true });
        send({ type: 'room_brightness', rid, brightness: pct });
      } else if (t === 'off') {
        send({ type: 'room_toggle', rid, on: false });
      } else {
        send({ type: 'room_brightness', rid, brightness: pct });
      }
    } else {
      const t = brightnessTransition(prev, pct);
      if (t === 'on') {
        send({ type: 'light_toggle', rid: id, on: true });
        send({ type: 'light_brightness', rid: id, brightness: pct });
      } else if (t === 'off') {
        send({ type: 'light_toggle', rid: id, on: false });
      } else {
        send({ type: 'light_brightness', rid: id, brightness: pct });
      }
    }

    setTimeout(() => {
      editingIds.delete(id);
      renderGrid();
    }, 500);
  }, 300);

  // Optimistic local update (no re-render — keeps the slider under the
  // user's finger instead of getting replaced mid-drag).
  if (id === '__all__') {
    const prevs = brightnessGestures[id] || {};
    for (const l of lights) {
      if (!l.dimmable) continue;
      const p = prevs.hasOwnProperty(l.id) ? prevs[l.id] : (l.on ? l.brightness : 0);
      const t = brightnessTransition(p, pct);
      l.brightness = pct;
      l.on = t !== 'off';
    }
  } else if (id.indexOf('room:') === 0) {
    // Room sliders carry "room:<grouped_light_id>", so find the room by its
    // grouped_light_id, then update that room's lights by room.id (the two
    // are different resources on the bridge — see Room in lightctl/service.go).
    const roomId = id.slice(5);
    const room = rooms.find((r) => r.grouped_light_id === roomId);
    const t = brightnessTransition(brightnessGestures[id], pct);
    if (room) {
      room.brightness = pct;
      room.on = t !== 'off';
      for (const l of lights) {
        if (l.room_id === room.id && l.dimmable) {
          l.brightness = pct;
          l.on = t !== 'off';
        }
      }
    }
  } else {
    const l = lights.find((x) => x.id === id);
    if (l) {
      const t = brightnessTransition(brightnessGestures[id], pct);
      l.brightness = pct;
      l.on = t !== 'off';
    }
  }
}

// ---------- color picker ----------

// targetId is a light id, "room:<roomId>" (fans out to that room's lights —
// CLIP v2 has no room-level color or temperature PUT), or null (every light
// on the bridge). Color-capable targets get the full HSV surface plus a
// white-temperature strip on its right edge; white-ambiance-only targets get
// a full-canvas temperature picker.
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
    // The close button is top-left, and the whole overlay hijacks pointer
    // events to pick colors — so the header has to be excluded from that
    // (see onDown) or the button would be unusable and the Android back
    // gesture, which also swipes from the left edge, would pick a colour.
    `<div class="color-picker-head">` +
      `<button type="button" class="color-picker-close" aria-label="${escapeHtml(HueMuxI18n.t('lights.closeColorPicker'))}">✕</button>` +
      `<h2>${escapeHtml(name)}</h2>` +
    `</div>` +
    `<canvas class="color-picker-canvas"></canvas>` +
    `<div class="color-picker-foot"><div class="color-picker-swatch"></div><span class="color-picker-readout"></span></div>`;
  document.body.appendChild(overlay);

  const canvas = overlay.querySelector('.color-picker-canvas');
  const ctx = canvas.getContext('2d');
  const swatch = overlay.querySelector('.color-picker-swatch');
  const readout = overlay.querySelector('.color-picker-readout');
  const closeBtn = overlay.querySelector('.color-picker-close');

  let hue = 0;
  let sat = 0;
  let cursorEl = null;
  let active = false;

  // The lights this picker will actually touch, to decide whether the HSV
  // surface exists at all: a white-ambiance-only bulb gets temperature only.
  const relevantLights = targetId && targetId.indexOf('room:') === 0
    ? lights.filter((l) => l.room_id === targetId.slice(5))
    : (targetId ? lights.filter((l) => l.id === targetId) : lights);
  const split = relevantLights.some((l) => l.colorable) ? 0.78 : 0;

  // split is the fraction of canvas width that stays HSV; the remainder is
  // the white-temperature strip (top 6500 K, bottom 2000 K — mirek 154..500).
  // split 0 means the whole canvas is temperature.
  function renderGradient() {
    const w = canvas.width;
    const h = canvas.height;
    if (w <= 0 || h <= 0) return;
    const img = ctx.createImageData(w, h);
    const data = img.data;
    const hsvW = split > 0 ? Math.max(0, Math.floor(split * w)) : 0;
    for (let py = 0; py < h; py++) {
      const s = 100 - (py / h) * 100;
      for (let px = 0; px < hsvW; px++) {
        const hh = (px / hsvW) * 360;
        const [r, g, b] = hsvToRgb(hh, s, 100);
        const i = (py * w + px) * 4;
        data[i] = r; data[i + 1] = g; data[i + 2] = b; data[i + 3] = 255;
      }
      // One Kelvin color per row — the strip varies with height only.
      const [r, g, b] = kelvinToRgb(6500 - (py / h) * 4500);
      for (let px = hsvW; px < w; px++) {
        const i = (py * w + px) * 4;
        data[i] = r; data[i + 1] = g; data[i + 2] = b; data[i + 3] = 255;
      }
    }
    ctx.putImageData(img, 0, 0);
    if (split > 0 && split < 1) {
      ctx.fillStyle = 'rgba(255,255,255,0.25)';
      ctx.fillRect(hsvW - 1, 0, 2, h);
    }
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

  function pick(e) {
    const rect = canvas.getBoundingClientRect();
    const x = Math.max(0, Math.min(e.clientX - rect.left, rect.width));
    const y = Math.max(0, Math.min(e.clientY - rect.top, rect.height));
    let pickedRgb = null; // the picked colour, hoisted for the cursor below

    if (split > 0 && x >= split * rect.width) {
      // White-temperature strip: height maps to Kelvin, which maps to mirek.
      const kelvin = 6500 - (y / rect.height) * 4500;
      const mirek = Math.max(153, Math.min(500, Math.round(1e6 / kelvin)));
      pickedRgb = kelvinToRgb(kelvin);
      const [r, g, b] = pickedRgb;
      swatch.style.backgroundColor = `rgb(${r}, ${g}, ${b})`;
      // Unit notation, untranslated — same policy as the H/S readout below.
      readout.textContent = `${Math.round(kelvin)} K`;
      pendingProbe = () => {
        probeTargets = new Set(colorTempTargetIds(targetId));
        sendColorTemp(targetId, mirek);
      };
    } else {
      const w = split > 0 ? split * rect.width : rect.width;
      hue = Math.round((x / w) * 360);
      sat = Math.round(100 - (y / rect.height) * 100);

      pickedRgb = hsvToRgb(hue, sat, 100);
      const [r, g, b] = pickedRgb;
      swatch.style.backgroundColor = `rgb(${r}, ${g}, ${b})`;
      readout.textContent = `H: ${hue}° S: ${sat}%`;

      const rgb = [r, g, b];
      pendingProbe = () => {
        probeTargets = new Set(colorTargetIds(targetId));
        sendColorRgb(targetId, rgb);
      };
    }
    maybeSendProbe();

    if (!cursorEl) {
      cursorEl = document.createElement('div');
      cursorEl.className = 'color-picker-cursor';
      overlay.appendChild(cursorEl);
    }
    const isTouch = e.pointerType === 'touch';
    cursorEl.style.left = e.clientX + 'px';
    cursorEl.style.top = (isTouch ? e.clientY - 50 : e.clientY) + 'px';
    if (pickedRgb) cursorEl.style.backgroundColor = `rgb(${pickedRgb[0]}, ${pickedRgb[1]}, ${pickedRgb[2]})`;
    cursorEl.hidden = false;
  }

  function onDown(e) {
    // The header (and its close button) must never start a colour pick: the
    // whole overlay captures pointerdown to drive the canvas, but this area is
    // where the close button lives and where Android's back swipe is expected
    // to work. Pointerdown on it just does nothing.
    if (e.target.closest('.color-picker-head')) return;
    active = true;
    pick(e);
  }
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

  if (closeBtn) closeBtn.addEventListener('click', (e) => {
    e.stopPropagation();
    closePicker();
  });

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
      // Refresh the card's color dots too — patchRoomTile covers the
      // tile-present case; this covers a Favorites view with no tile.
      const firstRoomLight = lights.find((l) => l.room_id === roomId);
      if (firstRoomLight) patchRoomTileFor(firstRoomLight);
      patchAllLightsTile();
      break;
    }
    case 'room-collapse':
      toggleRoomCollapsed(btn.dataset.roomId);
      break;
    case 'color-room':
      openColorPicker('room:' + btn.dataset.roomId);
      break;
  }
});

els.grid.addEventListener('input', (e) => {
  const el = e.target;
  const pct = parseInt(el.value, 10);
  if (el.dataset.action === 'brightness' || el.dataset.action === 'brightness-room') {
    if (el.dataset.action === 'brightness') {
      // The fill tracks the finger, not the model: this is the optimistic
      // update that keeps the slider colored mid-drag.
      const l = lights.find((x) => x.id === el.dataset.id);
      const rgb = l ? cardRgbFor(l, pct) : null;
      if (rgb) {
        el.style.setProperty('--slider-fill', `rgb(${rgb[0]},${rgb[1]},${rgb[2]})`);
        el.style.setProperty('--slider-pct', pct + '%');
      }
    } else {
      // Room tile: the gradient fill follows the finger the same way.
      const roomId = el.dataset.id.slice('room:'.length);
      el.setAttribute('style', multiSliderFillStyle(lights.filter((x) => x.room_id === roomId), pct));
    }
    // el.dataset.id already carries the "room:" prefix for brightness-room.
    scheduleBrightness(el.dataset.id, pct);
  } else if (el.dataset.action === 'brightness-all') {
    el.setAttribute('style', multiSliderFillStyle(lights, pct));
    scheduleBrightness('__all__', pct);
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
  persistFilterToStorage();
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

// ---------- filter <-> URL / storage ----------

// The filter survives in two places: the URL (for direct bookmarks — the
// standalone redirect forwards ?filter=... onto the shell URL, and this page
// reads the parent's URL when running inside the shell) and localStorage
// (for cold starts through the shell, whose iframe src never carries a
// query). URL wins when both exist: it is the more recent, explicit choice.
const FILTER_KEY = 'huemux.lightsFilter';

function persistFilterToURL() {
  const params = new URLSearchParams();
  params.set('filter', filter);
  if (filter === 'room' && filterRoomId) params.set('room', filterRoomId);
  history.replaceState(null, '', '?' + params.toString());
}

function persistFilterToStorage() {
  try {
    localStorage.setItem(FILTER_KEY, filter === 'room' && filterRoomId ? 'room:' + filterRoomId : filter);
  } catch (e) {}
}

function applyFilterChoice(f, r) {
  if (f === 'favorites' || f === 'all') { filter = f; filterRoomId = null; filterExplicitFromURL = true; }
  else if (f === 'room' && r) { filter = 'room'; filterRoomId = r; filterExplicitFromURL = true; }
}

function restoreFilterFromURL() {
  let params = new URLSearchParams(location.search);
  // Inside the shell the iframe's own src has no query — the standalone
  // redirect put it on the shell URL instead. Same origin, so reading it
  // is allowed; a cross-origin parent just stays a no-op.
  if (!params.get('filter') && window.self !== window.top) {
    try { params = new URLSearchParams(window.parent.location.search); } catch (e) {}
  }
  applyFilterChoice(params.get('filter'), params.get('room'));
}

function restoreFilterFromStorage() {
  try {
    const v = localStorage.getItem(FILTER_KEY);
    if (v && v.indexOf('room:') === 0) applyFilterChoice('room', v.slice(5));
    else applyFilterChoice(v, null);
  } catch (e) {}
}

// ---------- init ----------

restoreFilterFromURL();
// The URL names the filter explicitly (bookmark) — otherwise fall back to
// the last choice, stored per device like the theme.
if (!filterExplicitFromURL) restoreFilterFromStorage();
loadCollapsedRooms();
loadColumnsPref();
HueMuxFeatures.load();

// The shell embeds this page in its own iframe — separate browsing contexts
// that don't see each other's DOM events. The native `storage` event fires on
// every other same-origin window when localStorage changes, which is how a
// collapsed-room or column-count change made in another frame (Settings)
// reaches this one. It never fires in the frame that wrote the value.
window.addEventListener('storage', (e) => {
  if (e.key === COLLAPSED_KEY) {
    loadCollapsedRooms();
    scheduleRender();
  } else if (e.key === COLUMNS_KEY) {
    loadColumnsPref();
    scheduleRender();
  }
});

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

// ---------- foreground resync ----------
//
// Android backgrounds the app without tearing the WS down (the WebView's
// timers get throttled, the socket usually stays up), so lights changed
// elsewhere while the app was away never arrive as light_events — and the
// eventstream only carries *future* deltas, so there is nothing to replay.
// When the page becomes visible again, ask the server for a fresh full-state
// snapshot. The server also pushes one on connect, so the case where the WS
// really did drop is covered by the reconnect path; this handshake covers the
// far more common case where it didn't. The server reads lights+rooms fresh
// from the bridge, so the resync is a hard re-sync, not "whatever the
// server cached."
document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && wsReady) {
    send({ type: 'resync_lights' });
  }
});

connect();
