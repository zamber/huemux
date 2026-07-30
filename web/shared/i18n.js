// Minimal i18n: data-i18n="key.path" sets textContent, data-i18n-attr
// "attr:key.path,attr2:key2.path" sets attributes — same spirit as
// web/app.js's existing data-setting convention. Dictionaries are plain JSON
// fetched from /shared/i18n/<lang>.json (same-origin, so no build step and
// no CORS concerns). "system" mode follows navigator.language, same three-
// state idea as theme.js's system/light/dark cycle.

const LightsyncI18n = (() => {
  const SUPPORTED = ['en', 'pl'];
  let dict = {};
  let lang = 'en';

  function detectSystemLang() {
    const nav = (navigator.language || 'en').slice(0, 2).toLowerCase();
    return SUPPORTED.indexOf(nav) !== -1 ? nav : 'en';
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
    document.dispatchEvent(new CustomEvent('lightsync:langchange', { detail: { lang, choice: current() || 'system' } }));
  }

  function cycle() {
    const CYCLE = { system: 'en', en: 'pl', pl: 'system' };
    return setLang(CYCLE[current() || 'system']);
  }

  async function init() {
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
