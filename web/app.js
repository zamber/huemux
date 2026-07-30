// Main-thread glue: WebSocket transport, getDisplayMedia setup, the
// calibration preview, and the controls panel. See capture-worker.js for the
// actual pixel reduction and ROADMAP.md Milestone 4 for why capture is split
// the way it is.

const els = {
  connDot: document.getElementById('conn-dot'),
  areaSelect: document.getElementById('area-select'),
  startBtn: document.getElementById('start-btn'),
  stopBtn: document.getElementById('stop-btn'),
  changeSourceBtn: document.getElementById('change-source-btn'),
  areaWarning: document.getElementById('area-warning'),
  preview: document.getElementById('preview'),
  sourceWarning: document.getElementById('source-warning'),
  statusGrid: document.getElementById('status-grid'),
  app: document.getElementById('app'),
  pairingPanel: document.getElementById('pairing-panel'),
  pairingMessage: document.getElementById('pairing-message'),
  pairingError: document.getElementById('pairing-error'),
  pairingDiscovered: document.getElementById('pairing-discovered'),
  pairingRescanBtn: document.getElementById('pairing-rescan-btn'),
  pairingManualIP: document.getElementById('pairing-manual-ip'),
  pairingManualBtn: document.getElementById('pairing-manual-btn'),
  scenesDetails: document.getElementById('scenes-details'),
  scenesStrip: document.getElementById('sync-scenes-strip'),
};

const previewCtx = els.preview.getContext('2d');
previewCtx.imageSmoothingEnabled = false;
const gridCanvas = new OffscreenCanvas(64, 36);
const gridCtx = gridCanvas.getContext('2d');

let ws = null;
let wsReady = false;
let worker = null;
let stream = null;
let videoEl = null; // used only by the <video>+rVFC fallback
let latestStatus = null;
let suppressSettingsEcho = false;
// True from the moment capture succeeds until Stop (or the share ending on
// its own). loadAreas() re-runs on every WS reconnect — including the
// service simply outliving a brief network hiccup mid-session — and must
// not clobber the Start/Stop button state of a sync already in progress.
let syncing = false;
let wasPaired = false; // detects the unpaired->paired transition to trigger loadAreas() once
let discoveryStarted = false;

// --- WebSocket transport -----------------------------------------------

function connect() {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  ws = new WebSocket(`${proto}://${location.host}/ws`);
  ws.binaryType = 'arraybuffer';

  ws.onopen = () => {
    wsReady = true;
    els.connDot.className = 'dot ok';
    discoveryStarted = false; // a fresh connection gets a fresh scan if still unpaired
  };
  ws.onclose = () => {
    wsReady = false;
    els.connDot.className = 'dot';
    setTimeout(connect, 1500); // the service outlives any one tab; reconnect if it restarts
  };
  ws.onerror = () => { els.connDot.className = 'dot warn'; };
  ws.onmessage = (ev) => {
    if (typeof ev.data === 'string') {
      handleControlMessage(JSON.parse(ev.data));
    }
  };
}

function send(obj) {
  if (wsReady) ws.send(JSON.stringify(obj));
}

function sendGrid(buf) {
  if (wsReady) ws.send(buf);
}

function handleControlMessage(msg) {
  if (msg.type !== 'status') return;

  if (!msg.paired) {
    els.pairingPanel.hidden = false;
    els.app.hidden = true;
    if (!discoveryStarted) {
      discoveryStarted = true;
      send({ type: 'discover_bridges' });
    }
    renderPairing(msg.pairing || {});
    return;
  }

  els.pairingPanel.hidden = true;
  els.app.hidden = false;
  if (!wasPaired) {
    wasPaired = true;
    loadAreas();
    loadScenes();
  }

  latestStatus = msg;
  renderStatus(msg);
  // Another connected client (a second tab, or the desktop app running
  // alongside a browser tab) can be the one actually holding the frame
  // source — this client would otherwise show a perfectly normal-looking
  // local capture preview with no indication it isn't reaching the bridge.
  els.sourceWarning.hidden = !(msg.source_held && !msg.you_are_source);
}

// --- Pairing ---------------------------------------------------------------

function renderPairing(p) {
  if (p.error) {
    els.pairingError.textContent = p.error;
    els.pairingError.hidden = false;
  } else {
    els.pairingError.hidden = true;
  }

  if (p.pairing) {
    els.pairingMessage.textContent = p.message || 'Pairing…';
  } else if (p.discovering) {
    els.pairingMessage.textContent = 'Searching for a bridge on your network…';
  } else if (p.discovered && p.discovered.length > 0) {
    els.pairingMessage.textContent = 'Found a bridge:';
  } else {
    els.pairingMessage.textContent = 'No bridge found automatically — enter its IP manually below.';
  }

  els.pairingDiscovered.innerHTML = '';
  for (const b of p.discovered || []) {
    const card = document.createElement('div');
    card.className = 'bridge-card' + (b.supported ? '' : ' unsupported');

    const info = document.createElement('div');
    info.className = 'bridge-info';
    const name = document.createElement('span');
    name.className = 'bridge-name';
    name.textContent = b.name || 'Hue Bridge';
    const ip = document.createElement('span');
    ip.className = 'bridge-ip';
    ip.textContent = b.ip + (b.supported ? '' : ' — too old for Entertainment areas');
    info.appendChild(name);
    info.appendChild(ip);
    card.appendChild(info);

    if (b.supported) {
      const btn = document.createElement('button');
      btn.textContent = p.pairing ? 'Pairing…' : 'Pair';
      btn.disabled = !!p.pairing;
      btn.addEventListener('click', () => send({ type: 'pair', bridge_ip: b.ip }));
      card.appendChild(btn);
    }
    els.pairingDiscovered.appendChild(card);
  }

  els.pairingRescanBtn.disabled = !!p.discovering || !!p.pairing;
  els.pairingManualBtn.disabled = !!p.pairing;
}

els.pairingRescanBtn.addEventListener('click', () => send({ type: 'discover_bridges' }));

els.pairingManualBtn.addEventListener('click', () => {
  const ip = els.pairingManualIP.value.trim();
  if (!ip) return;
  send({ type: 'pair', bridge_ip: ip });
});
els.pairingManualIP.addEventListener('keydown', (ev) => {
  if (ev.key === 'Enter') els.pairingManualBtn.click();
});

// --- Areas ---------------------------------------------------------------

// configuration_type is a raw CLIP v2 enum value (screen/monitor/music/
// 3dspace/other) — translated via this map rather than displayed as-is.
const AREA_TYPE_KEYS = {
  screen: 'sync.configTypeScreen',
  monitor: 'sync.configTypeMonitor',
  music: 'sync.configTypeMusic',
  '3dspace': 'sync.configTypeSpace3d',
  other: 'sync.configTypeOther',
};

async function loadAreas() {
  const resp = await fetch('/api/areas');
  const areas = await resp.json();
  els.areaSelect.innerHTML = '';
  if (!areas || areas.length === 0) {
    const opt = document.createElement('option');
    opt.value = '';
    opt.textContent = LightsyncI18n.t('sync.noAreasFound');
    els.areaSelect.appendChild(opt);
    return;
  }
  for (const a of areas) {
    const opt = document.createElement('option');
    opt.value = a.id;
    const busy = a.active_streamer ? LightsyncI18n.t('sync.areaInUse') : '';
    const typeLabel = LightsyncI18n.t(AREA_TYPE_KEYS[a.configuration_type] || 'sync.configTypeOther');
    opt.textContent = `${a.metadata.name} — ${typeLabel} · ${LightsyncI18n.t('sync.status.channels', { n: a.channels.length })}${busy}`;
    els.areaSelect.appendChild(opt);
  }
  if (!syncing) els.startBtn.disabled = false;
}

document.addEventListener('lightsync:langchange', loadAreas);

// --- Scenes ------------------------------------------------------------
// Compact, collapsed-by-default disclosure near the calibration preview —
// present for convenience (recall a scene without switching to the Lights
// page) but deliberately not competing for space with the sync controls.
// Shows every scene rather than trying to filter to "this entertainment
// area's room," since entertainment configurations and the room/zone groups
// scenes belong to are separate CLIP v2 resources with no reliable shared id.

let scenesData = [];

async function loadScenes() {
  const resp = await fetch('/api/scenes');
  scenesData = await resp.json();
  renderScenesStrip();
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function renderScenesStrip() {
  if (!scenesData.length) { els.scenesDetails.hidden = true; return; }
  els.scenesDetails.hidden = false;
  els.scenesStrip.innerHTML = scenesData.map((sc) => {
    const swatches = sc.swatches.slice(0, 4).map((sw) => {
      const [r, g, b] = xyToRgb(sw.x, sw.y);
      return `<span class="scene-swatch" style="background: rgb(${r},${g},${b})"></span>`;
    }).join('');
    const title = sc.group_name ? `${sc.name} — ${sc.group_name}` : sc.name;
    return `
      <button type="button" class="scene-chip" data-scene-id="${escapeHtml(sc.id)}" title="${escapeHtml(title)}">
        <span class="scene-swatches">${swatches}</span>
        <span class="scene-name">${escapeHtml(sc.name)}</span>
        ${sc.auto_dynamic ? `<span class="scene-dynamic-badge">&#10022;</span>` : ''}
      </button>`;
  }).join('');
}

els.scenesStrip.addEventListener('click', (e) => {
  const chip = e.target.closest('.scene-chip');
  if (!chip) return;
  send({ type: 'scene_recall', rid: chip.dataset.sceneId });
});

document.addEventListener('lightsync:langchange', () => {
  renderScenesStrip();
  if (latestStatus) renderStatus(latestStatus);
});

// --- Start / stop ----------------------------------------------------------

els.startBtn.addEventListener('click', async () => {
  const areaId = els.areaSelect.value;
  if (!areaId) return;
  els.areaWarning.hidden = true;

  // Capture first, select_area (which dials the real DTLS stream) only once
  // it succeeds. Reversing this order would mean the bridge starts
  // streaming black/keepalive frames to real lights for however long the
  // browser's share picker sits open, which is a needless real-world side
  // effect of a UI interaction that hasn't finished yet.
  try {
    await startCapture();
  } catch (err) {
    els.areaWarning.textContent = `Capture failed: ${err.message || err}`;
    els.areaWarning.hidden = false;
    return;
  }

  send({ type: 'select_area', area_id: areaId });

  syncing = true;
  els.startBtn.disabled = true;
  els.stopBtn.disabled = false;
  els.changeSourceBtn.disabled = false;
});

els.stopBtn.addEventListener('click', () => {
  stopCapture();
  send({ type: 'stop' });
  syncing = false;
  els.startBtn.disabled = false;
  els.stopBtn.disabled = true;
  els.changeSourceBtn.disabled = true;
});

els.changeSourceBtn.addEventListener('click', async () => {
  stopCapture();
  await startCapture();
});

// getDisplayMedia() unconditionally, in the browser and under the optional
// Electron wrapper (cmd/lightsync-desktop) alike. This is the whole payoff
// of that wrapper: Electron's main process registers a
// setDisplayMediaRequestHandler (see cmd/lightsync-desktop/provisioner.go)
// that intercepts this exact call and hands back the primary screen with no
// picker UI, no desktopCapturer plumbing needed here — desktopCapturer
// itself is main-process-only in modern Electron and simply isn't reachable
// from this side. The page has no idea which context it's running in.
async function startCapture() {
  const capW = Number(getControl('capture_width') || 320);
  const capH = Number(getControl('capture_height') || 180);
  const capFPS = Number(getControl('capture_fps') || 30);

  stream = await navigator.mediaDevices.getDisplayMedia({
    video: { frameRate: capFPS, resizeMode: 'crop-and-scale' },
    audio: false,
  });
  const track = stream.getVideoTracks()[0];
  track.onended = () => {
    stopCapture();
    send({ type: 'stop' });
    syncing = false;
    els.startBtn.disabled = false;
    els.stopBtn.disabled = true;
    els.changeSourceBtn.disabled = true;
  };

  // Chrome and Firefox disagree on whether bare values or exact/max force the
  // downscale; apply both forms and verify with getSettings() rather than
  // assuming either took effect. Electron's intercepted stream accepts the
  // same constraints, so this isn't browser-only either.
  try {
    await track.applyConstraints({ width: capW, height: capH });
  } catch (_) { /* fall through to the exact/max form below */ }
  const settled = track.getSettings();
  if (!settled.width || settled.width > capW * 1.5) {
    try {
      await track.applyConstraints({ width: { exact: capW }, height: { exact: capH } });
    } catch (_) {
      try {
        await track.applyConstraints({ width: { max: capW }, height: { max: capH } });
      } catch (_) { /* whatever the compositor gives us is still far smaller than native */ }
    }
  }

  const gridW = 64, gridH = 36;
  gridCanvas.width = gridW;
  gridCanvas.height = gridH;

  if (typeof MediaStreamTrackProcessor !== 'undefined') {
    // The non-standard main-thread constructor Chrome shipped in 2021.
    // Constructing it here works regardless of whether the worker also has
    // it, so prefer this path whenever it's available.
    worker = new Worker('/capture-worker.js');
    worker.onmessage = onWorkerMessage;
    const processor = new MediaStreamTrackProcessor({ track });
    const readable = processor.readable;
    worker.postMessage({ type: 'start', gridW, gridH, readable }, [readable]);
  } else {
    // Try handing the raw track to the worker in case it has the
    // standardised, worker-only implementation.
    worker = new Worker('/capture-worker.js');
    worker.onmessage = onWorkerMessage;
    try {
      worker.postMessage({ type: 'start', gridW, gridH, track }, [track]);
    } catch (err) {
      // Neither side has MediaStreamTrackProcessor. Universal fallback:
      // a hidden <video> + requestVideoFrameCallback, drawn straight into
      // the grid canvas on the main thread. Slightly worse (it does get
      // throttled when the tab is hidden) but works everywhere.
      worker.terminate();
      worker = null;
      startVideoFallback(track, gridW, gridH);
    }
  }
}

function startVideoFallback(track, gridW, gridH) {
  videoEl = document.createElement('video');
  videoEl.srcObject = new MediaStream([track]);
  videoEl.muted = true;
  videoEl.playsInline = true;
  videoEl.play();

  const step = () => {
    if (!videoEl) return;
    gridCtx.drawImage(videoEl, 0, 0, gridW, gridH);
    const rgba = gridCtx.getImageData(0, 0, gridW, gridH).data;
    const out = new Uint8Array(3 + gridW * gridH * 3);
    out[0] = 0x01;
    out[1] = gridW;
    out[2] = gridH;
    for (let i = 0, o = 3; i < rgba.length; i += 4, o += 3) {
      out[o] = rgba[i]; out[o + 1] = rgba[i + 1]; out[o + 2] = rgba[i + 2];
    }
    sendGrid(out.buffer);
    drawPreviewFromGrid(gridW, gridH, rgba);
    if (videoEl && 'requestVideoFrameCallback' in videoEl) {
      videoEl.requestVideoFrameCallback(step);
    }
  };
  if ('requestVideoFrameCallback' in videoEl) {
    videoEl.requestVideoFrameCallback(step);
  } else {
    // Extremely old engines: plain rAF polling.
    const raf = () => { step(); if (videoEl) requestAnimationFrame(raf); };
    requestAnimationFrame(raf);
  }
}

function onWorkerMessage(e) {
  const msg = e.data;
  if (msg.type === 'grid') {
    sendGrid(msg.buf);
    const u8 = new Uint8Array(msg.buf);
    const gridW = u8[1], gridH = u8[2];
    // Re-expand RGB triples to RGBA for the preview canvas.
    const rgba = new Uint8ClampedArray(gridW * gridH * 4);
    for (let i = 3, o = 0; o < rgba.length; i += 3, o += 4) {
      rgba[o] = u8[i]; rgba[o + 1] = u8[i + 1]; rgba[o + 2] = u8[i + 2]; rgba[o + 3] = 255;
    }
    drawPreviewFromGrid(gridW, gridH, rgba);
  } else if (msg.type === 'error') {
    console.error('capture worker:', msg.message);
  }
}

function stopCapture() {
  if (worker) { worker.postMessage({ type: 'stop' }); worker.terminate(); worker = null; }
  if (videoEl) { videoEl.pause(); videoEl.srcObject = null; videoEl = null; }
  if (stream) { stream.getTracks().forEach((t) => t.stop()); stream = null; }
}

// --- Calibration preview ---------------------------------------------------

function drawPreviewFromGrid(gridW, gridH, rgba) {
  gridCanvas.width = gridW;
  gridCanvas.height = gridH;
  const imgData = new ImageData(new Uint8ClampedArray(rgba.buffer ? rgba : rgba), gridW, gridH);
  gridCtx.putImageData(imgData, 0, 0);

  previewCtx.clearRect(0, 0, els.preview.width, els.preview.height);
  previewCtx.drawImage(gridCanvas, 0, 0, els.preview.width, els.preview.height);
  drawZoneOverlays();
}

function drawZoneOverlays() {
  if (!latestStatus || !latestStatus.zones) return;
  const w = els.preview.width, h = els.preview.height;
  for (const z of latestStatus.zones) {
    const x = z.U0 * w, y = z.V0 * h, rw = (z.U1 - z.U0) * w, rh = (z.V1 - z.V0) * h;
    previewCtx.fillStyle = `rgba(${z.R},${z.G},${z.B},0.55)`;
    previewCtx.fillRect(x, y, rw, rh);
    previewCtx.strokeStyle = 'rgba(255,255,255,0.8)';
    previewCtx.lineWidth = 1;
    previewCtx.strokeRect(x, y, rw, rh);
    previewCtx.fillStyle = '#fff';
    previewCtx.font = '10px monospace';
    previewCtx.fillText(`ch${z.ChannelID}`, x + 3, y + 11);
  }
}

els.preview.addEventListener('click', (ev) => {
  if (!latestStatus || !latestStatus.zones) return;
  const rect = els.preview.getBoundingClientRect();
  const u = (ev.clientX - rect.left) / rect.width;
  const v = (ev.clientY - rect.top) / rect.height;
  for (const z of latestStatus.zones) {
    if (u >= z.U0 && u <= z.U1 && v >= z.V0 && v <= z.V1) {
      if (z.LightRID) send({ type: 'identify', light_rid: z.LightRID });
      return;
    }
  }
});

// --- Status readout ---------------------------------------------------------

function renderStatus(s) {
  const t = LightsyncI18n.t;
  const rows = [
    [t('sync.status.bridge'), `${s.snapshot.BridgeIP} ${t(s.snapshot.BridgeConnected ? 'sync.status.connected' : 'sync.status.disconnected')} · ${t('sync.status.handshake', { ms: s.snapshot.HandshakeMS })}`],
    [t('sync.status.area'), `${s.snapshot.AreaName || '—'} ${s.snapshot.AreaType || ''} · ${t('sync.status.channels', { n: s.snapshot.ChannelCount })}`],
    [t('sync.status.stream'), `${t(s.snapshot.StreamActive ? 'sync.status.streamActive' : 'sync.status.streamStopped')} · ${t('sync.status.hzOut', { hz: s.snapshot.OutputHz })} · ${t('sync.status.sent', { n: s.snapshot.Sent })}`],
    [t('sync.status.capture'), `${t('sync.status.clients', { n: s.snapshot.CaptureClients })} · ${t('sync.status.fpsIn', { fps: s.snapshot.InboundFPS.toFixed(1) })} · ${s.snapshot.CaptureW}x${s.snapshot.CaptureH} → ${s.snapshot.GridW}x${s.snapshot.GridH}`],
  ];
  if (s.snapshot.AreaBusyBy) rows.push([t('sync.status.busy'), t('sync.status.busyHeldBy', { app: s.snapshot.AreaBusyBy })]);
  if (s.snapshot.LastError) rows.push([t('sync.status.error'), s.snapshot.LastError]);

  els.statusGrid.innerHTML = '';
  for (const [k, v] of rows) {
    const kEl = document.createElement('span'); kEl.className = 'k'; kEl.textContent = k;
    const vEl = document.createElement('span'); vEl.textContent = v;
    els.statusGrid.appendChild(kEl); els.statusGrid.appendChild(vEl);
  }
  const swatches = document.createElement('div');
  swatches.className = 'swatches';
  for (const z of s.zones || []) {
    const sw = document.createElement('div');
    sw.className = 'swatch';
    sw.style.background = `rgb(${z.R},${z.G},${z.B})`;
    sw.title = `ch${z.ChannelID}`;
    swatches.appendChild(sw);
  }
  els.statusGrid.appendChild(swatches);

  if (!suppressSettingsEcho) applySettingsToControls(s.settings);
}

// --- Controls ----------------------------------------------------------------

const settingKeys = {
  reactivity: 'float', brightness: 'float', saturation: 'float',
  mode: 'string', edge_width: 'float', letterbox: 'bool',
  axis_horizontal: 'string', axis_vertical: 'string', axis_depth: 'string',
  invert_horizontal: 'bool', invert_vertical: 'bool', invert_depth: 'bool',
  depth_size_gain: 'float',
  black_cutoff: 'float', scene_cut_sensitivity: 'float',
  capture_width: 'int', capture_height: 'int', capture_fps: 'int',
  output_hz: 'int', color_space: 'string', disable_ems: 'bool',
};

function getControl(key) {
  const el = document.querySelector(`[data-setting="${key}"]`);
  if (!el) return undefined;
  if (el.type === 'checkbox') return el.checked;
  return el.value;
}

function collectSettings() {
  const out = {};
  for (const [key, type] of Object.entries(settingKeys)) {
    const el = document.querySelector(`[data-setting="${key}"]`);
    if (!el) continue;
    if (type === 'bool') out[key] = el.checked;
    else if (type === 'float') out[key] = parseFloat(el.value);
    else if (type === 'int') out[key] = parseInt(el.value, 10);
    else out[key] = el.value;
  }
  return out;
}

function applySettingsToControls(settings) {
  if (!settings) return;
  suppressSettingsEcho = true;
  for (const [key, type] of Object.entries(settingKeys)) {
    const el = document.querySelector(`[data-setting="${key}"]`);
    if (!el || settings[key] === undefined) continue;
    if (type === 'bool') el.checked = settings[key];
    else el.value = settings[key];
  }
  updateReadouts();
  suppressSettingsEcho = false;
}

function updateReadouts() {
  document.querySelectorAll('[data-readout-target]').forEach((el) => {
    const target = el.dataset.readoutTarget;
    const readout = document.querySelector(`[data-readout="${target}"]`);
    if (readout) readout.textContent = el.value;
  });
}

let settingsDebounce = null;
document.querySelectorAll('[data-setting]').forEach((el) => {
  const evt = el.tagName === 'SELECT' || el.type === 'checkbox' ? 'change' : 'input';
  el.addEventListener(evt, () => {
    updateReadouts();
    if (suppressSettingsEcho) return;
    clearTimeout(settingsDebounce);
    settingsDebounce = setTimeout(() => {
      send({ type: 'settings', settings: collectSettings() });
    }, 120);
  });
});

document.querySelectorAll('[data-preset-reactivity]').forEach((btn) => {
  btn.addEventListener('click', () => {
    const slider = document.querySelector('[data-setting="reactivity"]');
    slider.value = btn.dataset.presetReactivity;
    slider.dispatchEvent(new Event('input'));
  });
});

connect();
