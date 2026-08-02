// Shared auth token helper. When auth.mode=token is configured, the token must
// be sent on every fetch and WebSocket upgrade.
//
// The token is stored in localStorage — it survives a browser restart (unlike
// sessionStorage), which matters because the token is the only way back into
// the app once auth is turned on.
//
// The server accepts two forms: Authorization: Bearer <token> (preferred for
// fetch because it keeps the token out of URLs) and ?token= (required for
// WebSocket because the browser WebSocket constructor cannot set headers).

const AUTH_KEY = 'huemux-auth-token';
const AUTH_PAGE = '/auth.html';

function getAuthToken() {
  try { return localStorage.getItem(AUTH_KEY) || ''; } catch (_) { return ''; }
}

function setAuthToken(t) {
  try { localStorage.setItem(AUTH_KEY, t); } catch (_) {}
}

function clearAuthToken() {
  try { localStorage.removeItem(AUTH_KEY); } catch (_) {}
}

function hasAuthToken() {
  return getAuthToken() !== '';
}

// redirectToAuth sends the user to the token-entry page, preserving the current
// URL so they come back to the right place after authenticating.
function redirectToAuth() {
  // Never redirect the auth page itself.
  if (window.location.pathname === AUTH_PAGE) return;
  var redir = encodeURIComponent(window.location.pathname + window.location.search);
  window.location.replace(AUTH_PAGE + '?redirect=' + redir);
}

// logout clears the token and sends the user to the auth page.
function logout() {
  clearAuthToken();
  window.location.replace(AUTH_PAGE);
}

// authFetch wraps fetch(), adding the Authorization header when a token is
// stored, and redirecting to the auth page on 401 responses so the user is
// never left staring at a dead UI with no way to recover.
function authFetch(url, opts) {
  var token = getAuthToken();

  opts = opts || {};
  opts.headers = opts.headers || {};
  if (!(opts.headers instanceof Headers)) {
    opts.headers = new Headers(opts.headers);
  }
  if (token && !opts.headers.has('Authorization')) {
    opts.headers.set('Authorization', 'Bearer ' + token);
  }

  return fetch(url, opts).then(function(resp) {
    if (resp.status === 401) redirectToAuth();
    return resp;
  });
}

// authWSURL returns the WebSocket URL with ?token= appended when a token is
// stored. Pass the path, e.g. '/ws'.
function authWSURL(path) {
  var proto = location.protocol === 'https:' ? 'wss' : 'ws';
  var base = proto + '://' + location.host + path;
  var token = getAuthToken();
  if (!token) return base;
  var sep = base.indexOf('?') === -1 ? '?' : '&';
  return base + sep + 'token=' + encodeURIComponent(token);
}
