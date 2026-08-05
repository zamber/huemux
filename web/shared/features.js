// HueMuxFeatures — one fetch of /api/config, shared by every page.
//
// Which tabs exist is a server-side decision (the deployment profile), so the
// UI has to ask rather than assume. The rule for what a profile means lives in
// internal/appconfig and is sent here already resolved — `lights` and `sync`
// booleans, not a profile string for JavaScript to re-interpret. Duplicating
// that mapping on the client is how the two drift apart.
//
// Deliberately assigned to `window` rather than declared `const` at top level:
// a top-level const in a classic script is NOT a property of window, so
// `window.HueMuxFeatures` would be undefined everywhere and every caller would
// silently fall back to its defaults. That exact bug already shipped once, in
// the pairing panel's i18n guard.
window.HueMuxFeatures = (() => {
  // Both enabled until told otherwise. A page that renders its nav before the
  // fetch resolves should show the normal full UI and then narrow, rather than
  // flashing an empty header — and if /api/config is unreachable the sensible
  // failure is a complete UI, not a crippled one.
  let features = {
    lights: true, sync: true, presets: true, profile: 'full', loaded: false,
    auth: { mode: 'none', has_token: false },
  };
  let pending = null;

  function current() {
    return features;
  }

  function announce() {
    document.dispatchEvent(new CustomEvent('huemux:features', { detail: features }));
  }

  // load fetches once and caches. Repeated calls share the same in-flight
  // promise, so the shell plus two iframes do not make three requests for a
  // value that cannot differ between them.
  //
  // The request authenticates when a token is stored. This file loads before
  // shared/auth.js and cannot call its helpers, so the key name is repeated
  // here — keep it in sync with AUTH_KEY there. Without this, /api/config
  // 401s once a passphrase gates loopback, and the header would never learn
  // that auth is on (no logout button, wrong nav).
  function load() {
    if (pending) return pending;
    var token = '';
    try { token = localStorage.getItem('huemux-auth-token') || ''; } catch (_) {}
    pending = fetch('/api/config', token ? { headers: { 'Authorization': 'Bearer ' + token } } : undefined)
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
      .then((cfg) => {
        features = {
          lights: cfg.lights !== false,
          sync: cfg.sync !== false,
          presets: cfg.presets !== false,
          profile: cfg.profile || 'full',
          editable: !!cfg.editable,
          auth: cfg.auth || { mode: 'none', has_token: false },
          loaded: true,
        };
        announce();
        return features;
      })
      .catch(() => {
        // Keep the permissive defaults. An unreachable config endpoint should
        // not be able to hide half the app.
        features = { ...features, loaded: true };
        announce();
        return features;
      });
    return pending;
  }

  // set applies a config_changed push from the WebSocket. The server is the
  // authority on profile and auth, and the push is fresher than any cached
  // fetch — a PATCH applied a moment ago would otherwise stay hidden behind
  // load()'s cache until the next full reload.
  function set(cfg) {
    features = {
      lights: cfg.lights !== false,
      sync: cfg.sync !== false,
      presets: cfg.presets !== false,
      profile: cfg.profile || features.profile,
      editable: !!cfg.editable,
      auth: cfg.auth || features.auth,
      loaded: true,
    };
    announce();
    return features;
  }

  return { current, load, set };
})();
