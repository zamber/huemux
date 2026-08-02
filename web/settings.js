// Settings page: reads and writes /api/config.
//
// Everything here is a thin shell over that endpoint. Validation is the
// server's job — appconfig.Validate is the single source of truth and this
// page shows whatever it says rather than re-implementing the rules in
// JavaScript, which is how the two would drift.

const els = {
  status: document.getElementById('settings-status'),
  readonly: document.getElementById('settings-readonly'),
  form: document.getElementById('settings-form'),
  profile: document.getElementById('set-profile'),
  host: document.getElementById('set-host'),
  port: document.getElementById('set-port'),
  auth: document.getElementById('set-auth'),
  token: document.getElementById('set-token'),
  tokenGen: document.getElementById('set-token-gen'),
  tls: document.getElementById('set-tls'),
  save: document.getElementById('set-save'),
  result: document.getElementById('set-result'),
};

// Same wordlist idea as the Go side, kept short and readable. Only used to
// *suggest* a token in the UI; the authoritative generator and the entropy
// discussion live in internal/appconfig/token.go.
const WORDS = [
  'otter', 'badger', 'falcon', 'walrus', 'gecko', 'puffin', 'marten', 'lemur',
  'amber', 'birch', 'canyon', 'delta', 'ember', 'fjord', 'harbor', 'island',
  'copper', 'indigo', 'ivory', 'jade', 'olive', 'pearl', 'silver', 'topaz',
  'anchor', 'beacon', 'compass', 'lantern', 'mirror', 'ribbon', 'saddle', 'violin',
];

function t(key, fallback) {
  if (typeof HueMuxI18n === 'undefined') return fallback;
  const s = HueMuxI18n.t(key);
  return s && s !== key ? s : fallback;
}

function suggestToken() {
  // crypto.getRandomValues, not Math.random: this is a credential, even if
  // the user is free to replace it with anything they like.
  const out = [];
  const buf = new Uint32Array(3);
  crypto.getRandomValues(buf);
  for (let i = 0; i < 3; i++) out.push(WORDS[buf[i] % WORDS.length]);
  return out.join('.');
}

function render(cfg) {
  els.profile.value = cfg.profile || 'full';
  els.host.value = cfg.listen.host;
  els.port.value = cfg.listen.port;
  els.auth.value = cfg.auth.mode || 'none';
  els.tls.value = cfg.tls.mode || 'off';

  // The server never sends the token back — only whether one exists. Leaving
  // the field blank means "unchanged"; typing replaces it.
  els.token.placeholder = cfg.auth.has_token
    ? t('settings.tokenSet', '(unchanged — type to replace)')
    : t('settings.tokenPlaceholder', 'e.g. otter.beacon.willow');

  els.status.hidden = true;
  els.form.hidden = false;

  if (!cfg.editable) {
    els.readonly.hidden = false;
    els.form.querySelectorAll('input, select, button').forEach((el) => { el.disabled = true; });
  }
}

function load() {
  fetch('/api/config')
    .then((r) => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
    .then(render)
    .catch((e) => {
      els.status.textContent = t('settings.loadFailed', 'Could not load settings: ') + e.message;
    });
}

els.tokenGen.addEventListener('click', () => {
  els.token.value = suggestToken();
  // Choosing a token is an unambiguous request to use one.
  if (els.auth.value === 'none') els.auth.value = 'token';
});

els.form.addEventListener('submit', (ev) => {
  ev.preventDefault();
  els.save.disabled = true;
  els.result.textContent = '';

  const body = {
    profile: els.profile.value,
    listen: { host: els.host.value.trim(), port: Number(els.port.value) },
    auth: { mode: els.auth.value },
    tls: { mode: els.tls.value },
  };
  // Only send a token when one was typed, so saving an unrelated setting
  // does not wipe the existing credential.
  const typed = els.token.value.trim();
  if (typed) body.auth.token = typed;

  fetch('/api/config', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
    .then((r) => r.text().then((text) => ({ ok: r.ok, text })))
    .then(({ ok, text }) => {
      if (!ok) {
        // The server's validation message is more specific than anything
        // this page could invent.
        els.result.textContent = text.trim();
        els.result.className = 'hint warning';
        return;
      }
      const res = JSON.parse(text);
      els.result.textContent = res.restart_required
        ? t('settings.savedRestart', 'Saved — restart HueMux for this to take effect.')
        : t('settings.saved', 'Saved.');
      els.result.className = 'hint';
      els.token.value = '';
      load();
    })
    .catch((e) => {
      els.result.textContent = String(e);
      els.result.className = 'hint warning';
    })
    .finally(() => { els.save.disabled = false; });
});

load();

// ---------- diagnostics ----------
//
// Three ways out, because each fails somewhere that matters. A WebView may
// swallow a download; navigator.clipboard is undefined on an insecure origin,
// which every plain-HTTP LAN deployment is; and text on screen always works
// but needs manual selection. Offering all three means there is never a device
// where the answer is "you cannot get the log off it".

const diag = {
  download: document.getElementById('diag-download'),
  copy: document.getElementById('diag-copy'),
  view: document.getElementById('diag-view'),
  result: document.getElementById('diag-result'),
  text: document.getElementById('diag-text'),
  share: document.getElementById('diag-share'),
};

function fetchDiagnostics() {
  return fetch('/api/diagnostics')
    .then((r) => (r.ok ? r.text() : r.text().then((t) => Promise.reject(new Error(t.trim() || ('HTTP ' + r.status))))));
}

function diagSay(msg, bad) {
  diag.result.textContent = msg;
  diag.result.className = bad ? 'hint warning' : 'hint';
}

diag.view.addEventListener('click', () => {
  fetchDiagnostics().then((text) => {
    diag.text.value = text;
    diag.text.hidden = false;
    diag.text.focus();
    diag.text.setSelectionRange(0, 0);
    diagSay(t('settings.diagnosticsShown', 'Shown below — select all to copy manually.'));
  }).catch((e) => diagSay(String(e.message || e), true));
});

diag.copy.addEventListener('click', () => {
  fetchDiagnostics().then((text) => {
    // Only available in a secure context, which a plain-HTTP LAN page is not.
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text)
        .then(() => diagSay(t('settings.diagnosticsCopied', 'Copied to clipboard.')));
    }
    // Fall back to showing it rather than failing: the user can still select
    // and copy, which is the whole point.
    diag.text.value = text;
    diag.text.hidden = false;
    diag.text.select();
    diagSay(t('settings.diagnosticsSelectManually',
      'Clipboard unavailable on an insecure page — the text is selected below, copy it manually.'));
  }).catch((e) => diagSay(String(e.message || e), true));
});

// The Android bridge, reachable from this frame or from the shell that hosts
// it. Declared here rather than assumed, because settings.html is also served
// standalone and in a desktop browser, where none of this exists.
function nativeBridge() {
  try {
    const n = window.HueMuxNative || (window.top && window.top.HueMuxNative);
    return (n && typeof n.saveTextFile === 'function') ? n : null;
  } catch (e) {
    return null;
  }
}

function offerShare() {
  const n = nativeBridge();
  if (!n || typeof n.shareLastFile !== 'function') return;
  diag.share.hidden = false;
}

if (diag.share) {
  diag.share.addEventListener('click', () => {
    const n = nativeBridge();
    if (!n) return;
    try {
      const res = JSON.parse(n.shareLastFile());
      if (!res.ok) diagSay(res.error, true);
    } catch (e) {
      diagSay(String(e), true);
    }
  });
}

diag.download.addEventListener('click', () => {
  diagSay(t('settings.diagnosticsDownloading', 'Downloading…'));

  // On Android, fetch the text here and let Kotlin write the file.
  //
  // A WebView ignores a download unless a DownloadListener handles it, and
  // that listener does not fire for a navigation started inside an iframe.
  // This button previously navigated the window, which worked; moving it into
  // a throwaway iframe — to stop a failed download replacing the Settings page
  // with plain text — removed the only mechanism that made it work at all. The
  // fix is not to pick between those two, but to stop routing a file through a
  // navigation: fetching over loopback always works, and the native side can
  // write to Downloads directly.
  const n = nativeBridge();
  if (n) {
    fetchDiagnostics().then((text) => {
      const name = 'huemux-diagnostics-' +
        new Date().toISOString().replace(/[-:T]/g, '').slice(0, 15) + '.txt';
      const res = JSON.parse(n.saveTextFile(name, text));
      if (!res.ok) {
        diagSay(res.error || t('settings.diagnosticsDownloadFailed', 'Could not save the file.'), true);
        return;
      }
      diagSay(t('settings.diagnosticsSaved', 'Saved to ') + (res.name || name));
      offerShare();
    }).catch((e) => diagSay(String(e.message || e), true));
    return;
  }

  // Everywhere else the server's Content-Disposition and the browser's own
  // download handling are enough.
  const a = document.createElement('a');
  a.href = '/api/diagnostics';
  a.download = '';
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => {
    diagSay(t('settings.diagnosticsDownloadHint',
      'If nothing downloaded, use View or Copy instead.'));
  }, 2500);
});
