// A WebSocket that dies without saying so.
//
// Closing a laptop lid, dropping Wi-Fi, or moving between networks can leave
// the page holding a socket that is dead but not closed. `readyState` stays 1,
// `onclose` never runs, `ws.send()` succeeds into a kernel buffer that nobody
// will ever read, and every message the server pushes is lost. The page then
// goes on rendering whatever it last knew: the light panel stops tracking the
// lamps, and it looks like "the app is broken" rather than "the socket is
// dead". This is the worst kind of failure to leave in place, because the page
// reports nothing — its connection dot stays green.
//
// No browser API answers "is this socket still working?", so the page infers
// it. It can, because the server pushes a status message to every connection
// once a second (see pushStatusLoop in internal/server/http.go): a healthy
// channel is never quiet. Silence for many seconds is therefore proof of a
// dead socket, and the only useful answer is a real reconnect — the server
// pushes a fresh snapshot to every client the moment it connects.
//
// The same silence test covers the page coming back from the background: a tab
// whose timers were frozen has no idea whether its socket survived the sleep,
// and `wsReady` says "yes" either way.
window.HueMuxWS = (function () {
  'use strict';

  // The server's 1 Hz status push is the heartbeat. 15 s is fifteen missed
  // pushes: long enough that no plausible stall trips it — a busy main thread,
  // a backgrounded tab whose timers are throttled, a brief Wi-Fi roam — and
  // short enough that a woken laptop is live again within seconds of the
  // screen coming on.
  const STALE_MS = 15000;
  const CHECK_MS = 5000;

  // watch(socket, { onStale, staleAfterMs, checkEveryMs }) -> {
  //   stop(), isStale()
  // }
  //
  // onStale runs once, when the socket has been silent long enough to call it
  // dead. The caller does the reconnecting: it is the only place that knows how
  // to build its own socket. stop() must be called whenever the caller throws
  // the socket away, or its timer keeps running against a socket nobody has.
  function watch(ws, opts) {
    const o = opts || {};
    const staleMs = o.staleAfterMs || STALE_MS;
    const checkMs = o.checkEveryMs || CHECK_MS;
    let last = Date.now();

    const onMessage = function () { last = Date.now(); };
    ws.addEventListener('message', onMessage);

    const timer = setInterval(function () {
      // Closing and closed sockets have their own path (onclose), and there is
      // nothing to rescue: a CONNECTING socket has simply not opened yet.
      if (ws.readyState !== 1) return;
      if (Date.now() - last < staleMs) return;
      stop();
      o.onStale();
    }, checkMs);

    function stop() {
      clearInterval(timer);
      ws.removeEventListener('message', onMessage);
    }

    return {
      stop: stop,
      isStale: function () {
        return ws.readyState === 1 && Date.now() - last >= staleMs;
      },
    };
  }

  return { watch: watch, STALE_MS: STALE_MS };
})();
