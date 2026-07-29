// Capture worker.
//
// The whole performance argument of this project lives here: a full-resolution
// frame must never reach JavaScript. Three reductions happen before this code
// touches a pixel —
//
//   1. the capture track is constrained to ~320x180, so the compositor scales
//      before the page ever sees the frame;
//   2. MediaStreamTrackProcessor delivers frames off the main thread;
//   3. createImageBitmap resizes to the sampling grid on the GPU.
//
// What arrives at getImageData is about 2,000 pixels. Packing them is
// microseconds of work, which is why there is nothing here for WebAssembly to
// speed up.

let canvas = null;
let ctx = null;
let gridW = 64;
let gridH = 36;
let busy = false;
let running = false;

self.onmessage = async (e) => {
  const msg = e.data;

  switch (msg.type) {
    case 'start':
      gridW = msg.gridW || 64;
      gridH = msg.gridH || 36;
      canvas = new OffscreenCanvas(gridW, gridH);
      // willReadFrequently keeps the surface on the CPU side, which is what we
      // want: we read every frame and never composite.
      ctx = canvas.getContext('2d', { willReadFrequently: true });
      running = true;
      pump(msg.readable || readableFromTrack(msg.track));
      break;

    case 'stop':
      running = false;
      break;

    case 'grid':
      gridW = msg.gridW;
      gridH = msg.gridH;
      canvas = new OffscreenCanvas(gridW, gridH);
      ctx = canvas.getContext('2d', { willReadFrequently: true });
      break;
  }
};

function readableFromTrack(track) {
  // The standardised MediaStreamTrackProcessor is worker-only. Chrome also
  // ships a non-standard main-thread version, in which case app.js constructs
  // it there and transfers us the readable instead of the track.
  if (typeof self.MediaStreamTrackProcessor === 'undefined') {
    throw new Error('MediaStreamTrackProcessor unavailable in worker');
  }
  return new self.MediaStreamTrackProcessor({ track }).readable;
}

async function pump(readable) {
  const reader = readable.getReader();
  while (running) {
    const { value: frame, done } = await reader.read();
    if (done) break;
    if (!frame) continue;

    // Drop, never queue. If a reduction is still in flight, this frame is
    // already stale by the time we could get to it, and queueing turns a
    // transient hiccup into permanently growing latency.
    if (busy) {
      frame.close();
      continue;
    }

    busy = true;
    try {
      await reduce(frame);
    } catch (err) {
      self.postMessage({ type: 'error', message: String(err) });
    } finally {
      // Non-negotiable, on every path. The frame pool is small; leak a handful
      // and capture stops with no error at all — it simply goes quiet.
      frame.close();
      busy = false;
    }
  }
  try {
    reader.releaseLock();
  } catch (_) {
    /* already released */
  }
  self.postMessage({ type: 'ended' });
}

async function reduce(frame) {
  const bmp = await createImageBitmap(frame, {
    resizeWidth: gridW,
    resizeHeight: gridH,
    resizeQuality: 'low',
  });
  ctx.drawImage(bmp, 0, 0, gridW, gridH);
  bmp.close();

  const rgba = ctx.getImageData(0, 0, gridW, gridH).data;

  // Wire format: [0x01][width][height][R,G,B ...] — see internal/server/ws.go.
  // Sending the whole grid rather than pre-averaged zones keeps every tuning
  // decision on the Go side and gives the calibration view something to draw.
  const out = new Uint8Array(3 + gridW * gridH * 3);
  out[0] = 0x01;
  out[1] = gridW;
  out[2] = gridH;
  for (let i = 0, o = 3; i < rgba.length; i += 4, o += 3) {
    out[o] = rgba[i];
    out[o + 1] = rgba[i + 1];
    out[o + 2] = rgba[i + 2];
  }

  self.postMessage({ type: 'grid', buf: out.buffer }, [out.buffer]);
}
