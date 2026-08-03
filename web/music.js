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
    preset: document.getElementById('music-preset'),
    source: document.getElementById('music-source'),
  };

  // Built-in presets, keyed by the slug the server knows (engine
  // ActivateMusic). Slugs are stable; labels are translated.
  const PRESETS = [
    { slug: '', key: 'music.presetOff' },
    { slug: 'bass_pulse', key: 'music.presetBassPulse' },
    { slug: 'chill_ambient', key: 'music.presetChillAmbient' },
  ];

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
  // Native audio capture (Android internal audio): no MediaStream reaches
  // the page — Kotlin records the playback and pushes PCM into Go, and the
  // analysis comes back in the status push. The preview and frame count
  // are drawn from that instead of a local analyser.
  let nativeAudio = false;
  let statusFft = null;
  const audioWaiters = {};

  function nativeAudioAvailable() {
    return !!(window.HueMuxNative && typeof window.HueMuxNative.startAudioCapture === 'function');
  }

  // Resolvers keyed by request id, called from Kotlin. Same handshake shape
  // as app.js's screen-capture waiters, deliberately a separate registry
  // and entry point (window.__huemuxAudioResult).
  window.__huemuxAudioResult = function (id, ok, err) {
    const fn = audioWaiters[id];
    if (fn) fn(ok, err);
  };

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

  // acquireAudioTrack asks the OS for one audio track from the selected
  // source. "mic" is getUserMedia; "internal" is getDisplayMedia audio —
  // the OS picker chooses which tab/app makes the sound, which works with
  // headphones and does not hear the room (Phase 4's "system audio capture"
  // in its browser form). Android WebViews have no getDisplayMedia, so the
  // internal option is disabled there; its MediaProjection audio path is
  // future work.
  async function acquireAudioTrack(source) {
    if (source === 'internal') {
      let stream = null;
      try {
        stream = await navigator.mediaDevices.getDisplayMedia({ audio: true });
      } catch (err) {
        // Older Chromium insists on a video track; ask for a 2x2 one and
        // discard it — the audio is all this path ever uses.
        stream = await navigator.mediaDevices.getDisplayMedia({ audio: true, video: { width: 2, height: 2 } });
        stream.getVideoTracks().forEach((t) => t.stop());
      }
      const track = stream.getAudioTracks()[0];
      if (!track) throw new Error('shared source had no audio track');
      return track;
    }
    // Mic. Raw dynamics matter: AGC, echo cancellation and noise
    // suppression all compress or filter the signal, which blunts beat
    // detection. Not every device lets a page turn them off, so fall back
    // to defaults when the strict constraints are refused.
    try {
      return (await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false },
      })).getAudioTracks()[0];
    } catch (err) {
      return (await navigator.mediaDevices.getUserMedia({ audio: true })).getAudioTracks()[0];
    }
  }

  // startNativeAudioCapture asks Kotlin for internal-audio capture. The
  // consent dialog is the same MediaProjection one screen sync uses — the
  // OS treats screen and audio as one "record the screen" permission — and
  // resolution arrives through __huemuxAudioResult (see the waiter registry
  // above). The actual analysis happens in Go; the page just watches the
  // status push.
  function startNativeAudioCapture() {
    return new Promise((resolve, reject) => {
      const id = 'a' + Date.now() + Math.random().toString(36).slice(2, 8);
      audioWaiters[id] = (ok, err) => {
        delete audioWaiters[id];
        ok ? resolve() : reject(new Error(err || 'audio capture was not permitted'));
      };
      window.HueMuxNative.startAudioCapture(id);
    });
  }

  function stopNativeAudioCapture() {
    try {
      if (window.HueMuxNative && window.HueMuxNative.stopAudioCapture) {
        window.HueMuxNative.stopAudioCapture();
      }
    } catch (e) { /* the service may already be gone */ }
  }

  async function startCapture() {
    if (musicEls.source.value === 'internal' && nativeAudioAvailable()) {
      await startNativeAudioCapture();
      nativeAudio = true;
      running = true;
      frameCount = 0;
      statusFft = null;
      musicEls.spectrum.hidden = false;
      requestAnimationFrame(drawPreview);
      render();
      return;
    }
    const track = await acquireAudioTrack(musicEls.source.value);
    stream = new MediaStream([track]);

    // A source ending on its own (mic unplugged, permission revoked, the
    // user ending the share from the OS picker) unwinds the whole capture
    // state rather than leaving a dead timer claiming to capture.
    track.onended = () => stopCapture();

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
    if (nativeAudio) {
      stopNativeAudioCapture();
      nativeAudio = false;
      statusFft = null;
    }
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

  // Local preview: the same 32 bands, drawn from the analyser directly (or,
  // for native audio, from the status push's copy of what Go analysed — the
  // page has no local analyser for that path). The full spectrum UI is a
  // later Phase-1 item; this bar strip exists so the capture has an
  // immediate, honest "is there signal" answer. All bars in ink: contrast
  // carries meaning, colour does not (AGENTS.md UX rules).
  function drawPreview() {
    if (!running) return;
    const ctx = musicEls.spectrum.getContext('2d');
    const w = musicEls.spectrum.width, h = musicEls.spectrum.height;
    ctx.clearRect(0, 0, w, h);
    ctx.fillStyle = 'var(--ink)';
    const bw = w / FFT_BANDS;
    if (statusFft && statusFft.length === FFT_BANDS) {
      for (let b = 0; b < FFT_BANDS; b++) {
        const v = statusFft[b];
        if (v > 0.02) ctx.fillRect(b * bw + 1, h - v * h, bw - 2, Math.max(1, v * h));
      }
    } else if (analyser) {
      analyser.getByteFrequencyData(freqData);
      for (let b = 0; b < FFT_BANDS; b++) {
        let sum = 0, n = 0;
        for (let i = bandEdges[b]; i < bandEdges[b + 1]; i++) { sum += freqData[i]; n++; }
        const v = n ? sum / n / 255 : 0;
        if (v > 0.02) ctx.fillRect(b * bw + 1, h - v * h, bw - 2, Math.max(1, v * h));
      }
    }
    if (running) previewRAF = requestAnimationFrame(drawPreview);
  }

  function renderPresetOptions() {
    const value = musicEls.preset.value;
    musicEls.preset.innerHTML = '';
    for (const p of PRESETS) {
      const opt = document.createElement('option');
      opt.value = p.slug;
      opt.textContent = t(p.key);
      musicEls.preset.appendChild(opt);
    }
    musicEls.preset.value = value;
  }

  function render() {
    musicEls.toggleBtn.textContent = t(running ? 'music.stop' : 'music.start');
    musicEls.toggleBtn.classList.toggle('recording', running);
    // The source is fixed for the duration of a capture: swapping the
    // input under a live analyser would need a teardown/rebuild dance for
    // no benefit — stop and restart instead.
    musicEls.source.disabled = running;
    musicEls.status.textContent = running
      ? t('music.capturing', { n: frameCount })
      : t('music.idle');
  }

  musicEls.preset.addEventListener('change', () => {
    if (control) control({ type: 'music_preset', preset: musicEls.preset.value });
  });

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

  document.addEventListener('huemux:langchange', () => {
    renderPresetOptions();
    render();
  });

  window.HueMuxMusic = {
    // init wires the WebSocket senders and arms the controls. Takes an
    // options object (send/control) so future options do not change the
    // call shape. Called from the page's own script, which owns the
    // connection (AGENTS.md: each page opens its own WS).
    init(opts) {
      send = opts.send;
      control = opts.control || null;
      // Internal audio rides getDisplayMedia on browsers; on Android it
      // rides the native bridge's MediaProjection audio capture instead.
      // Only when neither exists is the option greyed out.
      const hasDisplay = !!(navigator.mediaDevices && navigator.mediaDevices.getDisplayMedia);
      if (!hasDisplay && !nativeAudioAvailable()) {
        const opt = musicEls.source.querySelector('option[value="internal"]');
        if (opt) {
          opt.disabled = true;
          opt.title = t('music.sourceUnavailable');
        }
      }
      renderPresetOptions();
      render();
    },
    // startCapture begins audio capture using the currently selected source
    // (mic or internal). Exposed so app.js can start music capture as part
    // of the unified Start flow instead of requiring a separate button click.
    startCapture() {
      if (running) return Promise.resolve();
      return startCapture();
    },
    stop() { stopCapture(); },
    isRunning: () => running,
    // onStatus reconciles the preset control with the server's view: the
    // engine is the authority on what runs (another tab, a restart, or a
    // rejected slug all leave the select wrong), and only a paired engine
    // can run presets at all. For native audio it also feeds the preview
    // and frame count from Go's analysis.
    onStatus(msg) {
      if (!msg) return;
      musicEls.preset.disabled = !msg.paired;
      const slug = msg.snapshot ? msg.snapshot.MusicPreset : '';
      if (musicEls.preset.value !== (slug || '')) musicEls.preset.value = slug || '';
      if (nativeAudio) {
        if (msg.music && msg.music.fft) statusFft = msg.music.fft;
        if (msg.music) frameCount = msg.music.frames;
        render();
      }
    },
    // onCaptureEnded fires when the OS ends the projection (user stops the
    // recording from the system UI, or Android reclaims it). The page has
    // to stop claiming to capture — same story as screen sync's handler.
    onCaptureEnded() {
      if (!nativeAudio) return;
      stopCapture();
    },
  };

  renderPresetOptions();
  render();
})();
