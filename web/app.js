// Main-thread glue: WebSocket transport, getDisplayMedia setup, the
// calibration preview, and the controls panel. See capture-worker.js for the
// actual pixel reduction and ROADMAP.md Milestone 4 for why capture is split
// the way it is.

const els = {
  connDot: document.getElementById('conn-dot'),
  areaSelect: document.getElementById('area-select'),
  captureMode: document.getElementById('capture-mode'),
  startBtn: document.getElementById('start-btn'),
  stopBtn: document.getElementById('stop-btn'),
  changeSourceBtn: document.getElementById('change-source-btn'),
  deviceCapture: document.getElementById('device-capture'),
  captureScale: document.getElementById('capture-scale'),
  captureScaleReadout: document.getElementById('capture-scale-readout'),
  recordBtn: document.getElementById('record-btn'),
  recordStatus: document.getElementById('record-status'),
  recordShare: document.getElementById('record-share'),
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

// The server's binary echo frame type (PROTOCOL.md §2): a downscaled copy of
// the grid the engine is analysing, broadcast when the debug-preview setting
// is on. Must match previewTypeByte in internal/server/grid_broadcast.go.
const previewTypeByte = 0x03;

let ws = null;
let wsReady = false;
let worker = null;
let stream = null;
let videoEl = null; // used only by the <video>+rVFC fallback
let latestStatus = null;
// The latest {"type":"debug"} push — drives the capture readouts and the
// audio histogram at up to debug_hz instead of the 1 Hz status cadence.
let latestDebug = null;
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
  ws = new WebSocket(authWSURL('/ws'));
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
    } else if (ev.data instanceof ArrayBuffer) {
      // Binary frames from the server: the 0x03 grid echo the debug-preview
      // feature broadcasts. The outbound 0x01/0x02 frames are the opposite
      // direction and never arrive here.
      handleBinaryMessage(ev.data);
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
  if (msg.type === 'config_changed') {
    // Server-side config was updated (e.g. profile changed, auth toggled).
    // set(), not load(): the push is fresher than load()'s cached fetch,
    // and the header re-renders tabs and the logout button from this.
    if (typeof HueMuxFeatures !== 'undefined') HueMuxFeatures.set(msg);
    return;
  }
  if (msg.type === 'debug') {
    // The debug push (up to debug_hz, see PROTOCOL.md): drives the capture
    // readouts and the audio histogram faster than the 1 Hz status push.
    // It is not a full status — no zones, no pairing — so it deliberately
    // falls through to nothing else here.
    latestDebug = msg;
    const mode = els.captureMode ? els.captureMode.value : 'video';
    if (mode === 'audio') drawAudioSpectrum(msg);
    const fpsEl = document.getElementById('debug-fps');
    if (fpsEl) {
      fpsEl.hidden = false;
      fpsEl.textContent = HueMuxI18n.t('sync.debugFps', { fps: msg.fps_in.toFixed(1) });
    }
    if (typeof HueMuxMusic !== 'undefined' && HueMuxMusic.onDebug) HueMuxMusic.onDebug(msg);
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
  // The music panel's preset control follows the server's view of what is
  // actually running (see HueMuxMusic.onStatus).
  if (typeof HueMuxMusic !== 'undefined') HueMuxMusic.onStatus(msg);
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
  const resp = await authFetch('/api/areas');
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
  const resp = await authFetch('/api/scenes');
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

  const mode = els.captureMode ? els.captureMode.value : 'video';
  // Always send the capture mode to the server before starting capture,
  // so the engine's output loop routes correctly from the first tick.
  send({ type: 'capture_mode', preset: mode });

  // Capture first, select_area (which dials the real DTLS stream) only once
  // it succeeds. Reversing this order would mean the bridge starts
  // streaming black/keepalive frames to real lights for however long the
  // browser's share picker sits open, which is a needless real-world side
  // effect of a UI interaction that hasn't finished yet.
  //
  // On Android, internal audio is recorded by the same MediaProjection as
  // the screen — one consent dialog, both streams. Asking the music module
  // to capture separately would open a second dialog and a second projection,
  // which the OS forbids from coexisting. The native startCapture handles
  // both; the music module adopts the native stream from the status push.
  var wantAudio = mode === 'audio' || mode === 'audiovideo';
  var wantVideo = mode === 'video' || mode === 'audiovideo';
  var srcSel = document.getElementById('music-source');
  var nativeInternal = wantAudio && srcSel && srcSel.value === 'internal' && nativeCaptureAvailable();
  try {
    if (nativeInternal) {
      // One native call captures both video and internal audio.
      await startCapture();
    } else {
      if (wantAudio && typeof HueMuxMusic !== 'undefined') {
        await HueMuxMusic.startCapture();
      }
      if (wantVideo) {
        await startCapture();
      }
    }
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
  if (typeof HueMuxMusic !== 'undefined') HueMuxMusic.stop();
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
  // Any of the three counts. A capture running with no stream is exactly the
  // state that stranded the user: the page showed Start, and the thing that
  // needed stopping was the capture, not the stream.
  const streaming = syncing || serverStreamIsOurs() || nativeCaptureRunning();
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
// Whether the Android capture service is running right now, according to the
// service itself. Separate from `syncing` and from the server's stream state:
// all three can disagree, and the one that decides whether a Stop button is
// worth offering is this one.
function nativeCaptureRunning() {
  const st = readCaptureState();
  return !!(st && st.capturing);
}

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

  // On Android, internal audio rides the same MediaProjection as the screen.
  // One consent dialog yields both streams; the service always starts
  // AudioPlaybackCapture alongside the VirtualDisplay, so there is no flag to
  // pass here. (Adding one would also break the bridge: JavascriptInterface
  // methods are matched by argument count, and startCapture takes two.)
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

function nativeCaptureAvailable() {
  return !!(window.HueMuxNative && typeof window.HueMuxNative.startCapture === 'function');
}

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

// Server-originated binary frames. There is exactly one today — the 0x03 grid
// echo from the debug-preview feature — dispatched on the first byte so future
// types (a 0x04 audio echo, say) slot in without touching this body.
function handleBinaryMessage(buf) {
  const u8 = new Uint8Array(buf);
  if (u8.length < 3) return;
  if (u8[0] === previewTypeByte) {
    const w = u8[1], h = u8[2];
    if (w < 1 || h < 1 || u8.length < 3 + w * h * 3) return;
    handleGridEcho(w, h, u8.subarray(3));
  }
}

// Draw the server's downscaled grid echo. Unlike the local capture path this
// has no source stream of its own — the echo *is* the picture, so it goes
// straight through the same preview renderer the local path uses (which is
// what keeps the zone overlays drawing on top).
function handleGridEcho(w, h, rgb) {
  const rgba = new Uint8ClampedArray(w * h * 4);
  for (let i = 0, o = 0; o < rgba.length; i += 3, o += 4) {
    rgba[o] = rgb[i]; rgba[o + 1] = rgb[i + 1]; rgba[o + 2] = rgb[i + 2]; rgba[o + 3] = 255;
  }
  drawPreviewFromGrid(w, h, rgba);
  drawLuminanceHistogram(rgb);
}

function stopCapture() {
  // Native capture holds an Android foreground service and a MediaProjection
  // session; leaving those running would keep the screen-capture notification
  // up and the DTLS stream alive after the user pressed Stop.
  //
  // Asked of the native side, not of `nativeCapture`. That flag is this page's
  // memory of having started capture, and the capture service outlives the
  // page: after a reload — or when the stream was stopped from the Lights tab,
  // which is a different document — the flag is false while the service is
  // very much running. Stop then did nothing, and since the page also believed
  // nothing was running it offered no other way out; the only remaining
  // control was Android's own recording indicator.
  if (nativeCapture || nativeCaptureRunning()) stopNativeCapture();
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
  // Still draw zone overlays so the calibration view is never blank —
  // black-on-black rectangles were the pre-stream state and looked broken.
  drawZoneOverlays();
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

// Peak-hold for the FFT strip: a drum hit decays across a few pushes rather
// than vanishing between them. Without it a bar that resets to its live value
// each draw reads as the signal dying — the exact "levels are low" complaint
// this whole feature exists to fix. Decay per draw, so the same 0.92 factor
// works at the 1 Hz status cadence and the 30 Hz debug cadence alike.
const fftPeaks = new Array(32).fill(0);

// Draw a 32-band frequency histogram in the preview canvas when audio
// mode is active and FFT data is available. Replaces the blank video
// preview with an honest "is there signal" visual. All bars in ink:
// contrast carries meaning, colour does not (AGENTS.md UX rules).
function drawAudioSpectrum(msg) {
  if (!msg || !msg.music || !msg.music.fft) return;
  const fft = msg.music.fft;
  if (fft.length < 32) return;
  const ctx = previewCtx;
  const w = els.preview.width, h = els.preview.height;
  const bw = Math.floor(w / 32);
  // Semi-transparent background so zone overlays draw on top.
  ctx.clearRect(0, 0, w, h);
  ctx.fillStyle = 'rgba(0,0,0,0.85)';
  ctx.fillRect(0, 0, w, h);
  for (let b = 0; b < 32; b++) {
    fftPeaks[b] = Math.max(fft[b], fftPeaks[b] * 0.92);
    const v = fftPeaks[b];
    if (v < 0.005) continue;
    const barH = Math.max(2, v * h);
    const x = b * bw;
    ctx.fillStyle = 'var(--ink)';
    ctx.fillRect(x + 1, h - barH, bw - 2, barH);
  }
  drawZoneOverlays();
}

// A 32-bin luminance histogram of the grid echo, drawn as a strip across the
// bottom of the preview canvas. Same bar vocabulary as the audio spectrum:
// height is proportional, ink-only. It answers "is the captured picture too
// dark / blown out / stuck on black" at a glance, which is the whole reason
// the echo exists on Android (where the WebView has no local capture preview).
function drawLuminanceHistogram(rgb) {
  const ctx = previewCtx;
  const w = els.preview.width, h = els.preview.height;
  const stripH = Math.max(24, Math.floor(h * 0.18));
  const bins = new Array(32).fill(0);
  for (let i = 0; i < rgb.length; i += 3) {
    // Rec.709 luma — green dominates human brightness perception.
    const y = 0.2126 * rgb[i] + 0.7152 * rgb[i + 1] + 0.0722 * rgb[i + 2];
    bins[Math.min(31, (y * 32 / 256) | 0)]++;
  }
  let max = 0;
  for (const n of bins) if (n > max) max = n;
  if (max === 0) return;
  ctx.fillStyle = 'rgba(0,0,0,0.85)';
  ctx.fillRect(0, h - stripH, w, stripH);
  ctx.fillStyle = 'var(--ink)';
  const bw = w / 32;
  for (let b = 0; b < 32; b++) {
    const barH = Math.max(1, (bins[b] / max) * (stripH - 4));
    ctx.fillRect(b * bw + 1, h - barH - 2, bw - 2, barH);
  }
}

function drawZoneOverlays() {
  if (!latestStatus || !latestStatus.zones) return;
  const w = els.preview.width, h = els.preview.height;
  for (const z of latestStatus.zones) {
    const x = z.U0 * w, y = z.V0 * h, rw = (z.U1 - z.U0) * w, rh = (z.V1 - z.V0) * h;
    // If the zone color is black (no frames yet), use a visible neutral gray
    // so the rectangles are not invisible on the black background.
    const isBlack = z.R === 0 && z.G === 0 && z.B === 0;
    const fill = isBlack
      ? 'rgba(60,60,60,0.45)'
      : `rgba(${z.R},${z.G},${z.B},0.55)`;
    previewCtx.fillStyle = fill;
    previewCtx.fillRect(x, y, rw, rh);
    previewCtx.strokeStyle = isBlack ? 'rgba(255,255,255,0.6)' : 'rgba(255,255,255,0.8)';
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

  // In audio mode, draw the FFT spectrum in the preview canvas since there
  // are no video frames to show. Zone overlays still render on top.
  const mode = els.captureMode ? els.captureMode.value : 'video';
  if (mode === 'audio' && s.music && s.music.fft) {
    drawAudioSpectrum(s);
  }
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
  debug_hz: 'int', debug_preview: 'bool', video_sync: 'bool', audio_gain: 'float', audio_floor: 'float',
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

// Capture mode selector — tells the server which source to route to the
// output loop. On Android this is a post-capture routing decision; the
// single MediaProjection always captures both video and audio when able.
if (els.captureMode) {
  els.captureMode.addEventListener('change', () => {
    send({ type: 'capture_mode', preset: els.captureMode.value });
    updateMusicPanelVisibility();
    // Leaving audio mode stops the spectrum draws; a stale peak would be
    // drawn again on re-entry after an arbitrary silence.
    if (els.captureMode.value !== 'audio') fftPeaks.fill(0);
  });
}

function updateMusicPanelVisibility() {
  var mode = els.captureMode ? els.captureMode.value : 'video';
  var opts = document.getElementById('music-options');
  var spec = document.getElementById('music-spectrum');
  var want = mode !== 'video';
  if (opts) opts.hidden = !want;
  if (spec) spec.hidden = !want;
}

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

  // Changing the capture size rebuilds the capture display, and the encoder
  // was configured for the size it had when recording started — so the control
  // is locked for the duration rather than allowed to corrupt the rest of the
  // file. The native side refuses it too; this is so the UI says why.
  els.captureScale.disabled = !!st.scaleLocked;

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
  } else if (st.lastLocation) {
    // The location the file actually went to, reported by the native side
    // rather than assumed from a folder convention here. "It recorded and I
    // have no idea where the file is" is not a saved file.
    els.recordStatus.textContent = HueMuxI18n.t('sync.recordSaved', { name: st.lastLocation });
  }
  if (els.recordShare) els.recordShare.hidden = !st.canShare;
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

  if (els.recordShare) {
    els.recordShare.addEventListener('click', () => {
      const n = nativeBridge();
      if (!n || typeof n.shareLastFile !== 'function') return;
      try {
        const res = JSON.parse(n.shareLastFile());
        if (!res.ok) els.recordStatus.textContent = res.error;
      } catch (e) {
        els.recordStatus.textContent = String(e);
      }
    });
  }

  els.recordBtn.addEventListener('click', () => {
    const n = nativeBridge();
    if (!n) return;
    const st = readCaptureState();
    els.recordStatus.textContent = '';
    let res;
    try {
      res = JSON.parse(st && st.recording ? n.stopRecording() : n.startRecording());
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

// Music reactivity (Phase 1): hand the audio-source module our WebSocket
// senders so its 0x02 frames ride the same connection as everything else —
// binary frames through sendGrid, JSON control messages (music_stop) through
// send.
if (typeof HueMuxMusic !== 'undefined') HueMuxMusic.init({ send: sendGrid, control: send });

connect();
