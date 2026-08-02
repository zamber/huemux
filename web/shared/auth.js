// Shared auth token helper. When auth.mode=token is configured, the token must
// be sent on every fetch and WebSocket upgrade. This module stores it in
// sessionStorage (survives a page reload but not a browser restart — that is
// deliberate: the token is a credential, not a preference).
//
// The server accepts two forms: Authorization: Bearer <token> (preferred for
// fetch because it keeps the token out of URLs) and ?token= (required for
// WebSocket because the browser WebSocket constructor cannot set headers).

const AUTH_KEY = 'huemux-auth-token';

// getToken returns the stored token or empty string.
function getAuthToken() {
  try { return sessionStorage.getItem(AUTH_KEY) || ''; } catch (_) { return ''; }
}

// setAuthToken stores the token. Pass '' to clear.
function setAuthToken(t) {
  try { sessionStorage.setItem(AUTH_KEY, t); } catch (_) {}
}

// hasAuthToken reports whether a token is stored.
function hasAuthToken() {
  return getAuthToken() !== '';
}

// authFetch wraps fetch(), adding the Authorization header when a token is stored.
// Use this instead of bare fetch() for every API call.
function authFetch(url, opts) {
  const token = getAuthToken();
  if (!token) return fetch(url, opts);

  opts = opts || {};
  opts.headers = opts.headers || {};
  if (!(opts.headers instanceof Headers)) {
    opts.headers = new Headers(opts.headers);
  }
  if (!opts.headers.has('Authorization')) {
    opts.headers.set('Authorization', 'Bearer ' + token);
  }
  return fetch(url, opts);
}

// authWSURL returns the WebSocket URL with ?token= appended when a token is stored.
// Pass the path, e.g. '/ws'.
function authWSURL(path) {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const base = proto + '://' + location.host + path;
  const token = getAuthToken();
  if (!token) return base;
  const sep = base.indexOf('?') === -1 ? '?' : '&';
  return base + sep + 'token=' + encodeURIComponent(token);
}
