// Music reactivity — the audio-source half of the multiplexer (see
// docs/MUSIC-REACTIVITY.md, Phase 1). Microphone audio goes through the Web
// Audio API's AnalyserNode and comes out as binary audio frames — type 0x02,
// 32 FFT magnitude bands + 256 waveform samples as little-endian float32 —
// pushed to the Go side at ~30 Hz over the page's existing WebSocket. All
// analysis and routing happens Go-side; this file is only the source.
//
// Wrapped in an IIFE and exposed explicitly on window (the HueMuxShell
// precedent): app.js already owns top-level names like `els`, and this file
// must not collide with them in the shared script scope.
(function () {
  'use strict';

  const FFT_BANDS = 32;   // wire format: must match internal/music
  const WAVE_SAMPLES = 256;
  const FFT_SIZE = 2048;  // analyser FFT size; frequencyBinCount = 1024
  const TICK_MS = 33;     // ~30 frames per second, per the roadmap
  const WAVE_DIV = FFT_SIZE / WAVE_SAMPLES; // 8 samples averaged per output

  const musicEls = {
    toggleBtn: document.getElementById('music-toggle'),
    status: document.getElementById('music-status'),
    spectrum: document.getElementById('music-spectrum'),
  };

  let send = null;    // binary frames — wired by init() to the page's WS send
  let control = null; // JSON control messages, e.g. music_stop
  let running = false;
  let stream = null;
  let audioCtx = null;
  let analyser = null;
  let freqData = null;  // Uint8Array(1024), analyser's byte bins
  let waveData = null;  // Float32Array(2048), time-domain samples
  let bandEdges = null; // log-spaced bin ranges for the 32-band reduction
  let tickTimer = null;
  let previewRAF = null;
  let frameCount = 0;

  // i18n's init() fetches the locale file asynchronously, so the very first
  // render of this module can land before the strings exist and t() returns
  // the raw key path. Self-heal: re-render briefly until the file is loaded.
  // (i18n.js only dispatches huemux:langchange on an actual change, never on
  // the initial load.)
  let retryTimer = null;
  let retryCount = 0;
  const t = (key, vars) => {
    const out = (typeof HueMuxI18n !== 'undefined' ? HueMuxI18n.t(key, vars) : key);
    if (out === key && retryCount < 20) {
      retryCount++;
      clearTimeout(retryTimer);
      retryTimer = setTimeout(render, 100);
    }
    return out;
  };

  function bandEdgesFor(nBins) {
    // Geometric spacing: ~constant octave width. Linear bands would give
    // the bass region — where beat detection and the bass/mid split live —
    // one or two of 32 bins while ten high bands cover empty air above
    // 10 kHz. The first bands stay coarse (a band can be a single bin) but
    // low bins are ~21 Hz wide, so that is still sub-bass resolution.
    const edges = new Array(FFT_BANDS + 1);
    const logN = Math.log(nBins - 1);
    for (let i = 0; i <= FFT_BANDS; i++) {
      edges[i] = Math.max(1, Math.round(Math.exp((i / FFT_BANDS) * logN)));
    }
    return edges;
  }

  async function startCapture() {
    // Raw dynamics matter here: AGC, echo cancellation and noise
    // suppression all compress or filter the signal, which blunts beat
    // detection. Not every device lets a page turn them off, so fall back
    // to defaults when the strict constraints are refused.
    try {
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false },
      });
    } catch (err) {
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      } catch (err2) {
        stream = null;
        throw err2;
      }
    }

    // A mic unplugged (or permission revoked) ends the track on its own;
    // unwind the whole capture state rather than leaving a dead timer
    // claiming to capture.
    stream.getAudioTracks().forEach((track) => {
      track.onended = () => stopCapture();
    });

    audioCtx = new AudioContext();
    const src = audioCtx.createMediaStreamSource(stream);
    analyser = audioCtx.createAnalyser();
    analyser.fftSize = FFT_SIZE;
    // Raw per-frame values: the analyser's smoothing would smear onsets
    // across frames, and the Go side owns any smoothing it wants.
    analyser.smoothingTimeConstant = 0;
    src.connect(analyser);

    freqData = new Uint8Array(analyser.frequencyBinCount);
    waveData = new Float32Array(FFT_SIZE);
    bandEdges = bandEdgesFor(analyser.frequencyBinCount);
    frameCount = 0;

    tickTimer = setInterval(tick, TICK_MS);
    running = true;
    musicEls.spectrum.hidden = false;
    requestAnimationFrame(drawPreview);
    render();
  }

  function stopCapture() {
    running = false;
    // Tell the server the source is gone so its music block clears: the
    // frames stop either way, but without this the status push would keep
    // reporting stale analysis as live. The page's WS stays open, so the
    // disconnect path never fires.
    if (control) control({ type: 'music_stop' });
    if (tickTimer) { clearInterval(tickTimer); tickTimer = null; }
    if (previewRAF) { cancelAnimationFrame(previewRAF); previewRAF = null; }
    if (audioCtx) { audioCtx.close().catch(() => {}); audioCtx = null; }
    if (stream) { stream.getTracks().forEach((tr) => tr.stop()); stream = null; }
    analyser = null;
    musicEls.spectrum.hidden = true;
    render();
  }

  // One analysis frame: read the analyser, reduce to the wire format, send.
  // The byte layout must stay in lockstep with internal/music.ParseFrame —
  // type byte 0x02, then Bands+Samples little-endian float32s.
  function tick() {
    if (!running || !analyser || !send) return;
    analyser.getByteFrequencyData(freqData);
    analyser.getFloatTimeDomainData(waveData);

    const out = new ArrayBuffer(1 + (FFT_BANDS + WAVE_SAMPLES) * 4);
    const dv = new DataView(out);
    dv.setUint8(0, 0x02);
    let o = 1;
    for (let b = 0; b < FFT_BANDS; b++) {
      let sum = 0, n = 0;
      for (let i = bandEdges[b]; i < bandEdges[b + 1]; i++) { sum += freqData[i]; n++; }
      // Analyser byte bins map [-100,-30] dB to 0..255; /255 normalises to
      // the 0..1 magnitude the Go side expects.
      dv.setFloat32(o, n ? sum / n / 255 : 0, true);
      o += 4;
    }
    for (let w = 0; w < WAVE_SAMPLES; w++) {
      let sum = 0;
      for (let i = 0; i < WAVE_DIV; i++) sum += waveData[w * WAVE_DIV + i];
      dv.setFloat32(o, sum / WAVE_DIV, true);
      o += 4;
    }
    frameCount++;
    send(dv.buffer);
    if (frameCount % 15 === 0) render(); // keep the count fresh without thrashing
  }

  // Local preview: the same 32 bands, drawn from the analyser directly. The
  // full spectrum UI is a later Phase-1 item; this bar strip exists so the
  // capture has an immediate, honest "is there signal" answer. All bars in
  // ink: contrast carries meaning, colour does not (AGENTS.md UX rules).
  function drawPreview() {
    if (!running || !analyser) return;
    analyser.getByteFrequencyData(freqData);
    const ctx = musicEls.spectrum.getContext('2d');
    const w = musicEls.spectrum.width, h = musicEls.spectrum.height;
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = 'var(--ink)';
    const bw = w / FFT_BANDS;
    for (let b = 0; b < FFT_BANDS; b++) {
      let sum = 0, n = 0;
      for (let i = bandEdges[b]; i < bandEdges[b + 1]; i++) { sum += freqData[i]; n++; }
      const v = n ? sum / n / 255 : 0;
      if (v > 0.02) ctx.fillRect(b * bw + 1, h - v * h, bw - 2, Math.max(1, v * h));
    }
    if (running) previewRAF = requestAnimationFrame(drawPreview);
  }

  function render() {
    musicEls.toggleBtn.textContent = t(running ? 'music.stop' : 'music.start');
    musicEls.toggleBtn.classList.toggle('recording', running);
    musicEls.status.textContent = running
      ? t('music.capturing', { n: frameCount })
      : t('music.idle');
  }

  musicEls.toggleBtn.addEventListener('click', async () => {
    if (running) { stopCapture(); return; }
    try {
      await startCapture();
    } catch (err) {
      // The permission denial and the missing-device case both land here;
      // err.name tells them apart on the status line.
      console.error('music capture:', err);
      musicEls.status.textContent = t('music.error') + ` (${err.name})`;
    }
  });

  document.addEventListener('huemux:langchange', render);

  window.HueMuxMusic = {
    // init wires the WebSocket send function and arms the toggle. Takes an
    // options object (send) so future options do not change the call shape.
    // Called from the page's own script, which owns the connection
    // (AGENTS.md: each page opens its own WS).
    init(opts) { send = opts.send; control = opts.control || null; render(); },
    stop() { stopCapture(); },
    isRunning: () => running,
  };

  render();
})();
