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
  deviceCapture: document.getElementById('device-capture'),
  captureScale: document.getElementById('capture-scale'),
  captureScaleReadout: document.getElementById('capture-scale-readout'),
  recordQuality: document.getElementById('record-quality'),
  recordBtn: document.getElementById('record-btn'),
  recordStatus: document.getElementById('record-status'),
  areaWarning: document.getElementById('area-warning'),
  preview: document.getElementById('preview'),
  liveBadge: document.getElementById('live-badge'),
  sourceWarning: document.getElementById('source-warning'),
  statusGrid: document.getElementById('status-grid'),
  app: document.getElementById('app'),
  pairingPanel: document.getElementById('pairing-panel'),
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
  if (msg.type === 'stream_stopped') {
    // Another client either preempted us (started its own sync) or sent a
    // remote stop (e.g. the Lights page's own stop-streaming button) — our
    // stream is over whether we asked for it or not. Stop local capture and
    // reset the preview instead of leaving it frozen on the last frame,
    // which otherwise looks exactly like it's still streaming.
    stopCapture();
    resetPreview();
    syncing = false;
    els.startBtn.disabled = false;
    els.stopBtn.disabled = true;
    els.changeSourceBtn.disabled = true;
    renderSyncButtons();
    return;
  }
  if (msg.type !== 'status') return;

  if (!msg.paired) {
    els.pairingPanel.hidden = false;
    els.app.hidden = true;
    if (!discoveryStarted) {
      discoveryStarted = true;
      send({ type: 'discover_bridges' });
    }
    els.pairingPanel.update(msg.pairing || {});
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
  // Status is one of the two inputs to the button state, so re-render it here
  // rather than only where this page changes its own mind.
  renderSyncButtons();
  // Another connected client (a second tab, or the desktop app running
  // alongside a browser tab) can be the one actually holding the frame
  // source — this client would otherwise show a perfectly normal-looking
  // local capture preview with no indication it isn't reaching the bridge.
  els.sourceWarning.hidden = !(msg.source_held && !msg.you_are_source);
}

// --- Pairing ---------------------------------------------------------------

// The panel itself is <huemux-pairing> (shared/pairing.js), shared with
// lights.html so a lights-only profile can still pair. This page only has to
// forward the element's outbound messages onto its own WebSocket and hand it
// each status update.
els.pairingPanel.addEventListener('huemux:pair-send', (ev) => send(ev.detail));

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
    opt.textContent = HueMuxI18n.t('sync.noAreasFound');
    els.areaSelect.appendChild(opt);
    return;
  }
  for (const a of areas) {
    const opt = document.createElement('option');
    opt.value = a.id;
    const busy = a.active_streamer ? HueMuxI18n.t('sync.areaInUse') : '';
    const typeLabel = HueMuxI18n.t(AREA_TYPE_KEYS[a.configuration_type] || 'sync.configTypeOther');
    opt.textContent = `${a.metadata.name} — ${typeLabel} · ${HueMuxI18n.t('sync.status.channels', { n: a.channels.length })}${busy}`;
    els.areaSelect.appendChild(opt);
  }
  if (!syncing) els.startBtn.disabled = false;
}

document.addEventListener('huemux:langchange', loadAreas);

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

document.addEventListener('huemux:langchange', () => {
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

  // Skipped for native capture: Kotlin already called StartSync (which is
  // what selects the area and dials DTLS) before raising the consent dialog,
  // so sending it again would re-dial underneath a live stream.
  if (!nativeCapture) send({ type: 'select_area', area_id: areaId });

  syncing = true;
  els.startBtn.disabled = true;
  els.stopBtn.disabled = false;
  els.changeSourceBtn.disabled = false;
  renderSyncButtons();
});

els.stopBtn.addEventListener('click', () => stopSyncing());

// One place to unwind, because a stream can end four ways: this button, the
// system's own "stop sharing" affordance, the engine reporting stream_stopped
// because another client took over, or the capture service being reclaimed.
// Each used to unwind separately, and the native ones did not unwind at all —
// the UI went on claiming to stream after the frames had stopped.
function stopSyncing(opts) {
  const silent = opts && opts.silent;
  stopCapture();
  resetPreview();
  // Skip the outbound stop when the server already knows — it told us.
  if (!silent) send({ type: 'stop' });
  syncing = false;
  els.startBtn.disabled = false;
  els.stopBtn.disabled = true;
  els.changeSourceBtn.disabled = true;
  renderSyncButtons();
}

// Start and Stop were two buttons sitting side by side, one of them always
// disabled — which reads as broken rather than unavailable, and on a phone
// wastes a third of the control row. One button that says what it will do is
// clearer and matches the stop-streaming control the lights page already has.
//
// This must run at load, not only on a state change: `hidden` starts unset on
// all three buttons, so until something calls this every one of them is on
// screen at once — Start, Stop and Change source together, which is precisely
// the state the markup is designed never to show.
function renderSyncButtons() {
  // The server, not this page, is the authority on whether a stream is
  // running. They come apart whenever the page is newer than the stream: on
  // Android the capture service is a foreground service that outlives a
  // WebView reload, so a reloaded page would offer "Start" for a sync that is
  // already running and had no way to stop it. Trust `syncing` for what this
  // client is doing, and the status for what the machine is doing.
  const streaming = syncing || serverStreamIsOurs();
  els.startBtn.hidden = streaming;
  els.stopBtn.hidden = !streaming;
  els.stopBtn.disabled = false;
  els.changeSourceBtn.hidden = !streaming;
  // Changing source is meaningless where the OS picks it for us.
  if (nativeCapture) els.changeSourceBtn.hidden = true;
  // Starting or stopping sync is also what makes recording possible or not,
  // and what gives the effective capture size a value to show.
  refreshCaptureState();
}

// True when a stream is running and this client is the one feeding it — i.e.
// stopping it here is both possible and the right thing to offer. A stream fed
// by some *other* client is reported by els.sourceWarning instead; offering to
// stop that one from here would be a surprise.
function serverStreamIsOurs() {
  return !!(latestStatus && latestStatus.snapshot && latestStatus.snapshot.StreamActive &&
    (latestStatus.you_are_source || !latestStatus.source_held));
}

// Called from Kotlin when capture ends outside the page's control.
window.__huemuxCaptureEnded = function () {
  if (!syncing && !nativeCapture) return;
  nativeCapture = false;
  document.documentElement.removeAttribute('data-native-capture');
  stopSyncing({ silent: true });
};

els.changeSourceBtn.addEventListener('click', async () => {
  stopCapture();
  await startCapture();
});

// getDisplayMedia() unconditionally, in the browser and under the optional
// Electron wrapper (cmd/huemux-desktop) alike. This is the whole payoff
// of that wrapper: Electron's main process registers a
// setDisplayMediaRequestHandler (see cmd/huemux-desktop/provisioner.go)
// that intercepts this exact call and hands back the primary screen with no
// picker UI, no desktopCapturer plumbing needed here — desktopCapturer
// itself is main-process-only in modern Electron and simply isn't reachable
// from this side. The page has no idea which context it's running in.
// startNativeCapture hands off to Android's MediaProjection.
//
// Frames never touch JavaScript on this path: Kotlin downsamples and calls
// into the Go engine in-process, which is both faster and avoids shipping
// megabytes a second through a WebView bridge. The consequence is that there
// is no local video preview to draw — the zone swatches, which come from the
// server's status push, carry the useful half of that anyway.
async function startNativeCapture() {
  const areaId = els.areaSelect.value;
  if (!areaId) throw new Error('no entertainment area selected');

  nativeCapture = true;
  document.documentElement.setAttribute('data-native-capture', '');

  // A @JavascriptInterface method is synchronous and can only return
  // primitives, so it cannot hand back a promise. The consent dialog is
  // asynchronous and dismissable, and the page has to know which happened or
  // its Start button stays disabled forever — hence the callback handshake:
  // we register a resolver under an id, Kotlin calls it when the dialog
  // closes.
  try {
    await new Promise((resolve, reject) => {
      const id = 'c' + Date.now() + Math.random().toString(36).slice(2, 8);
      captureWaiters[id] = (ok, err) => {
        delete captureWaiters[id];
        ok ? resolve() : reject(new Error(err || 'capture was not permitted'));
      };
      window.HueMuxNative.startCapture(areaId, id);
    });
  } catch (e) {
    nativeCapture = false;
    document.documentElement.removeAttribute('data-native-capture');
    throw e;
  }
}

// Resolvers keyed by request id, called from Kotlin. Assigned to window
// explicitly — a top-level const is not a property of window, and the native
// side looks this up by name.
const captureWaiters = {};
window.__huemuxCaptureResult = function (id, ok, err) {
  const fn = captureWaiters[id];
  if (fn) fn(ok, err);
};

function stopNativeCapture() {
  nativeCapture = false;
  document.documentElement.removeAttribute('data-native-capture');
  try {
    if (window.HueMuxNative && window.HueMuxNative.stopCapture) {
      window.HueMuxNative.stopCapture();
    }
  } catch (e) { /* the service may already be gone; nothing to recover */ }
}

let nativeCapture = false;

async function startCapture() {
  const capW = Number(getControl('capture_width') || 320);
  const capH = Number(getControl('capture_height') || 180);
  const capFPS = Number(getControl('capture_fps') || 30);

  // Tier 0: native capture, when the page is hosted by the Android app.
  //
  // No mobile browser implements getDisplayMedia — not Chrome, Firefox,
  // Safari or any WebView — so on a phone the ladder below has nothing to
  // climb. Android's MediaProjection is the only route, and it lives on the
  // Kotlin side, which injects HueMuxNative for exactly this.
  //
  // A capability check, not an environment check: asking whether the bridge
  // exists is deterministic, whereas getDisplayMedia fails differently on
  // every WebView version — undefined here, NotAllowedError there, a hang
  // waiting on a permission prompt that never appears elsewhere.
  if (window.HueMuxNative && typeof window.HueMuxNative.startCapture === 'function') {
    return startNativeCapture();
  }

  stream = await navigator.mediaDevices.getDisplayMedia({
    video: { frameRate: capFPS, resizeMode: 'crop-and-scale' },
    audio: false,
  });
  const track = stream.getVideoTracks()[0];
  track.onended = () => {
    stopCapture();
    resetPreview();
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
  // Native capture holds an Android foreground service and a MediaProjection
  // session; leaving those running would keep the screen-capture notification
  // up and the DTLS stream alive after the user pressed Stop.
  if (nativeCapture) stopNativeCapture();
  if (worker) { worker.postMessage({ type: 'stop' }); worker.terminate(); worker = null; }
  if (videoEl) { videoEl.pause(); videoEl.srcObject = null; videoEl = null; }
  if (stream) { stream.getTracks().forEach((t) => t.stop()); stream = null; }
}

// --- Calibration preview ---------------------------------------------------

// Clears the preview back to empty (the black CSS background shows through)
// instead of leaving it frozen on whatever the last captured frame was —
// which otherwise looks exactly like sync is still running when it isn't,
// whether that's because the user pressed Stop, the browser's share ended
// on its own, or another client's stream_stopped preempted this one.
function resetPreview() {
  previewCtx.clearRect(0, 0, els.preview.width, els.preview.height);
}

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
  const t = HueMuxI18n.t;
  els.liveBadge.hidden = !s.snapshot.StreamActive;
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

// --- device capture: resolution and recording -----------------------------
//
// Android only. Both controls talk straight to MainActivity's bridge rather
// than through the Go server, because both are properties of *this device's*
// screen mirror — a second client connected to the same server has its own,
// and the server has no business holding one device's capture preferences.
//
// Every method here returns a JSON string synchronously (see NativeBridge):
// none of them needs a consent dialog or an activity result, unlike
// startCapture, so none of them needs the callback handshake that one has.

function nativeBridge() {
  const n = window.HueMuxNative;
  return (n && typeof n.captureState === 'function') ? n : null;
}

function readCaptureState() {
  const n = nativeBridge();
  if (!n) return null;
  try {
    return JSON.parse(n.captureState());
  } catch (e) {
    console.warn('captureState failed', e);
    return null;
  }
}

// The effective size is worth showing rather than just the percentage: the
// long edge is capped, so past a certain point dragging the slider changes the
// number and nothing else, and without the size on screen that looks like the
// control is broken.
function renderCaptureState(st) {
  if (!st) return;
  els.captureScale.value = String(Math.round(st.scale * 100));
  const pct = Math.round(st.scale * 100) + '%';
  const size = st.capturing && st.captureW
    ? `${pct} · ${st.captureW}x${st.captureH}`
    : `${pct} · ${HueMuxI18n.t('sync.captureWhenRunning')}`;
  els.captureScaleReadout.textContent = size;

  els.recordQuality.value = st.quality === 'native' ? 'native' : 'capture';
  els.recordBtn.classList.toggle('recording', !!st.recording);
  els.recordBtn.textContent = HueMuxI18n.t(st.recording ? 'sync.recordStop' : 'sync.recordStart');
  // Recording mirrors the capture projection, so there is nothing to record
  // until sync is running. Saying so on the disabled control beats letting
  // someone tap it and read an error.
  els.recordBtn.disabled = !st.capturing && !st.recording;
  // Only ever *replaces* the status line, never blanks it. This runs on every
  // status push, and an error the user is still reading ("this device would
  // not give a second screen mirror") must not be wiped a second later by a
  // routine refresh. The click handler clears it deliberately instead.
  if (!st.recording && !st.capturing) {
    els.recordStatus.textContent = HueMuxI18n.t('sync.recordNeedsSync');
  } else if (st.recording) {
    els.recordStatus.textContent = HueMuxI18n.t('sync.recordInProgress');
  }
}

function refreshCaptureState() {
  const st = readCaptureState();
  if (st) renderCaptureState(st);
}

if (nativeBridge()) {
  els.deviceCapture.hidden = false;
  refreshCaptureState();

  els.captureScale.addEventListener('input', () => {
    // Live, not on release: this rebuilds the capture display each time, which
    // is cheap enough (one ImageReader plus one VirtualDisplay) and makes the
    // effective-size readout follow the finger.
    const n = nativeBridge();
    if (!n) return;
    try {
      n.setCaptureScale(Number(els.captureScale.value) / 100);
    } catch (e) {
      console.warn('setCaptureScale failed', e);
    }
    // The service resizes asynchronously; ask again once it has.
    setTimeout(refreshCaptureState, 150);
    refreshCaptureState();
  });

  els.recordQuality.addEventListener('change', () => {
    const n = nativeBridge();
    if (!n) return;
    try {
      n.setRecordingQuality(els.recordQuality.value);
    } catch (e) {
      console.warn('setRecordingQuality failed', e);
    }
  });

  els.recordBtn.addEventListener('click', () => {
    const n = nativeBridge();
    if (!n) return;
    const st = readCaptureState();
    els.recordStatus.textContent = '';
    let res;
    try {
      res = JSON.parse(st && st.recording ? n.stopRecording() : n.startRecording(els.recordQuality.value));
    } catch (e) {
      els.recordStatus.textContent = String(e);
      return;
    }
    if (!res.ok) {
      // The native side already phrases these for a person — a device that
      // will not give a second mirror, an encode size it will not accept.
      // Repeating it verbatim beats inventing a vaguer one here.
      els.recordStatus.textContent = res.error;
    } else if (res.name) {
      els.recordStatus.textContent = HueMuxI18n.t('sync.recordSaved', { name: res.name });
    } else {
      els.recordStatus.textContent = '';
    }
    refreshCaptureState();
  });

  // Capture can start or stop without this block being involved (the Start
  // button, the system's own stop, the service being reclaimed), and each
  // changes what these controls should say.
  document.addEventListener('huemux:langchange', refreshCaptureState);
}

// Before anything asynchronous: the three sync buttons all start visible
// because none of them has `hidden` set in the markup, and the first thing
// that would otherwise correct that is a status message.
renderSyncButtons();

connect();
