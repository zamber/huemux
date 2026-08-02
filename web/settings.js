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

// ---------- applying changes ----------
//
// Every field applies as it changes; there is no Save button.
//
// A Save button in a settings screen with four fields is a second step that
// exists only to be forgotten. It also lies about failure: with one submit for
// the whole form, a rejected port makes it look as though nothing was saved,
// when in fact nothing was *sent*. Applying per field means the error lands on
// the field that caused it and everything else is already stored.
//
// Validation stays entirely server-side — appconfig.Validate is the single
// source of truth and re-implementing its rules here is how the two drift.

let applyTimer = null;
let inFlight = false;
let pendingAgain = false;

function currentBody() {
  const body = {
    profile: els.profile.value,
    listen: { host: els.host.value.trim(), port: Number(els.port.value) },
    auth: { mode: els.auth.value },
    tls: { mode: els.tls.value },
  };
  // Only send a token when one was typed, so changing an unrelated setting
  // does not wipe the existing credential.
  const typed = els.token.value.trim();
  if (typed) body.auth.token = typed;
  return body;
}

function say(msg, bad) {
  els.result.textContent = msg;
  els.result.className = bad ? 'hint warning' : 'hint';
}

function apply() {
  // One request at a time. Two PATCHes racing on the same document can commit
  // out of order, and the loser silently wins — the field the user changed
  // last would be the one that did not stick.
  if (inFlight) { pendingAgain = true; return; }
  inFlight = true;
  say(t('settings.saving', 'Saving…'));

  fetch('/api/config', {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(currentBody()),
  })
    .then((r) => r.text().then((text) => ({ ok: r.ok, text })))
    .then(({ ok, text }) => {
      if (!ok) {
        // The server's message names the offending field; anything this page
        // invented would be vaguer.
        say(text.trim(), true);
        return;
      }
      const res = JSON.parse(text);
      say(res.restart_required
        ? t('settings.savedRestart', 'Saved — restart HueMux for this to take effect.')
        : t('settings.saved', 'Saved.'));
      els.token.value = '';
      load();
    })
    .catch((e) => say(String(e), true))
    .finally(() => {
      inFlight = false;
      if (pendingAgain) { pendingAgain = false; apply(); }
    });
}

// Selects commit on change and go immediately. Text and number fields debounce,
// because applying on every keystroke would PATCH a half-typed address and show
// its rejection while the user is still typing it.
function scheduleApply(immediate) {
  clearTimeout(applyTimer);
  if (immediate) { apply(); return; }
  applyTimer = setTimeout(apply, 600);
}

[els.profile, els.auth, els.tls].forEach((el) =>
  el.addEventListener('change', () => scheduleApply(true)));
[els.host, els.port, els.token].forEach((el) => {
  el.addEventListener('input', () => scheduleApply(false));
  // Leaving a field is an unambiguous "I am done with this one".
  el.addEventListener('blur', () => scheduleApply(true));
});

els.form.addEventListener('submit', (ev) => {
  // Nothing submits, but a form still does on Enter, and letting it navigate
  // would blank the page.
  ev.preventDefault();
  scheduleApply(true);
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

// ---------- about ----------

const about = {
  version: document.getElementById('about-version'),
  licence: document.getElementById('about-licence'),
  source: document.getElementById('about-source'),
  btn: document.getElementById('about-licences'),
  text: document.getElementById('about-licences-text'),
};

fetch('/api/about')
  .then((r) => (r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status))))
  .then((a) => {
    about.version.textContent = a.version;
    about.licence.textContent = a.license;
    about.source.textContent = a.source_url;
    about.source.href = a.source_url;
  })
  .catch(() => {
    // The section still says something useful without the server: the licence
    // is a property of the build, not of the connection.
    about.version.textContent = t('about.unknown', 'unknown');
    about.licence.textContent = 'GPL-3.0-or-later';
  });

// Loaded on demand rather than with the page: it is several hundred lines of
// licence text that most visits will never open, and Settings should not carry
// that on every load.
let licencesLoaded = false;
about.btn.addEventListener('click', () => {
  if (licencesLoaded) {
    about.text.hidden = !about.text.hidden;
    return;
  }
  // Served from the embedded web/ directory, so this works on a phone with no
  // internet — which is the whole point of shipping the text rather than a link.
  fetch('/THIRD_PARTY_LICENSES.md')
    .then((r) => (r.ok ? r.text() : Promise.reject(new Error('HTTP ' + r.status))))
    .then((txt) => {
      about.text.textContent = txt;
      about.text.hidden = false;
      licencesLoaded = true;
    })
    .catch((e) => {
      about.text.textContent = t('about.licencesFailed', 'Could not load licences: ') + e.message;
      about.text.hidden = false;
    });
});

// ---------- language and direction ----------

const langEls = {
  lang: document.getElementById('set-lang'),
  dir: document.getElementById('set-dir'),
};

function renderLangOptions() {
  const sys = document.createElement('option');
  sys.value = '';
  sys.textContent = t('settings.languageSystem', 'System');
  langEls.lang.replaceChildren(sys);
  for (const loc of HueMuxI18n.LOCALES) {
    const o = document.createElement('option');
    o.value = loc.tag;
    // Its own name, not an English one — see the LOCALES comment in i18n.js.
    o.textContent = loc.name;
    langEls.lang.appendChild(o);
  }
  langEls.lang.value = HueMuxI18n.current() || '';
  langEls.dir.value = HueMuxI18n.dirOverride() || '';
}

langEls.lang.addEventListener('change', () => {
  HueMuxI18n.setLang(langEls.lang.value || 'system');
});

langEls.dir.addEventListener('change', () => {
  HueMuxI18n.setDirOverride(langEls.dir.value || null);
});

// Re-rendered on language change so "System" is itself translated, and so the
// selection survives a change made from another frame of the shell.
document.addEventListener('huemux:langchange', renderLangOptions);
renderLangOptions();
