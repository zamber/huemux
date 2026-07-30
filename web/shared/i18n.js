// Minimal i18n: data-i18n="key.path" sets textContent, data-i18n-attr
// "attr:key.path,attr2:key2.path" sets attributes — same spirit as
// web/app.js's existing data-setting convention. Dictionaries are plain JSON
// fetched from /shared/i18n/<lang>.json (same-origin, so no build step and
// no CORS concerns). "system" mode follows navigator.language, same three-
// state idea as theme.js's system/light/dark cycle.

const HueMuxI18n = (() => {
  const SUPPORTED = ['en', 'pl'];
  let dict = {};
  let lang = 'en';
  let serverLangHint = null;

  // /api/locale reflects the *server process's* environment (LANG etc.),
  // not the browser's — needed because under the Electron wrapper
  // (cmd/huemux-desktop), the bundled Chromium's navigator.language
  // often doesn't track the host OS locale at all, while the Go process's
  // environment (inherited from whatever launched it) usually does.
  // Preferred over navigator.language/navigator.languages when present.
  async function fetchServerLangHint() {
    try {
      const res = await fetch('/api/locale');
      const data = await res.json();
      if (SUPPORTED.indexOf(data.lang) !== -1) serverLangHint = data.lang;
    } catch (_) {
      // Endpoint unreachable — navigator.language/.languages still apply.
    }
  }

  function detectSystemLang() {
    if (serverLangHint) return serverLangHint;
    // navigator.languages (full preference list) over the single
    // navigator.language, which is sometimes less reliable in embedded
    // contexts than the browser's complete list.
    const candidates = (navigator.languages && navigator.languages.length) ? navigator.languages : [navigator.language || 'en'];
    for (const c of candidates) {
      const code = (c || '').slice(0, 2).toLowerCase();
      if (SUPPORTED.indexOf(code) !== -1) return code;
    }
    return 'en';
  }

  // The explicit user choice, or null if following system.
  function current() {
    const stored = localStorage.getItem('lang');
    return SUPPORTED.indexOf(stored) !== -1 ? stored : null;
  }

  function resolved() {
    return current() || detectSystemLang();
  }

  function lookup(key) {
    return key.split('.').reduce((o, k) => (o && typeof o === 'object' ? o[k] : undefined), dict);
  }

  function t(key, vars) {
    const v = lookup(key);
    if (typeof v !== 'string') return key;
    if (!vars) return v;
    return Object.keys(vars).reduce((s, k) => s.split('{' + k + '}').join(vars[k]), v);
  }

  function applyTo(root) {
    root = root || document;
    root.querySelectorAll('[data-i18n]').forEach((el) => {
      el.textContent = t(el.getAttribute('data-i18n'));
    });
    root.querySelectorAll('[data-i18n-attr]').forEach((el) => {
      el.getAttribute('data-i18n-attr').split(',').forEach((pair) => {
        const parts = pair.split(':');
        const attr = parts[0] && parts[0].trim();
        const key = parts[1] && parts[1].trim();
        if (attr && key) el.setAttribute(attr, t(key));
      });
    });
  }

  async function load(lng) {
    const res = await fetch('/shared/i18n/' + lng + '.json');
    dict = await res.json();
  }

  async function setLang(choice) {
    if (choice === 'system') localStorage.removeItem('lang');
    else localStorage.setItem('lang', choice);
    lang = resolved();
    await load(lang);
    document.documentElement.setAttribute('lang', lang);
    applyTo(document);
    document.dispatchEvent(new CustomEvent('huemux:langchange', { detail: { lang, choice: current() || 'system' } }));
  }

  function cycle() {
    const CYCLE = { system: 'en', en: 'pl', pl: 'system' };
    return setLang(CYCLE[current() || 'system']);
  }

  async function init() {
    await fetchServerLangHint();
    lang = resolved();
    await load(lang);
    document.documentElement.setAttribute('lang', lang);
    applyTo(document);
  }

  // Cross-frame sync for the app.html shell (sync.html/lights.html each run
  // in their own iframe — see the identical comment in theme.js). setLang
  // re-writing the same value to localStorage here is a harmless no-op.
  window.addEventListener('storage', (e) => {
    if (e.key === 'lang') setLang(current() || 'system');
  });

  return { t, applyTo, setLang, cycle, init, current, resolved };
})();
