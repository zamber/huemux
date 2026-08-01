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
