// Minimal i18n: data-i18n="key.path" sets textContent, data-i18n-html sets
// innerHTML, data-i18n-attr "attr:key.path" sets attributes. Dictionaries are
// plain JSON fetched from /shared/i18n/<tag>.json — same-origin, so no build
// step and no CORS. "system" mode follows the platform, the same three-state
// idea as theme.js's system/light/dark.

const HueMuxI18n = (() => {
  // Every locale, with the name written in that locale. A language list in
  // English is useless to the person who needs it: someone who has ended up in
  // a language they cannot read has to find their own, and "Japanese" does not
  // help them — 日本語 does.
  //
  // Order is deliberate: English first as the source, then alphabetical by tag
  // so the list is predictable to scan rather than ranked by some judgement
  // about which languages matter more.
  const LOCALES = [
    { tag: 'en', name: 'English' },
    { tag: 'ar-SA', name: 'العربية', dir: 'rtl' },
    { tag: 'cs-CZ', name: 'Čeština' },
    { tag: 'da-DK', name: 'Dansk' },
    { tag: 'de-DE', name: 'Deutsch' },
    { tag: 'es-419', name: 'Español (Latinoamérica)' },
    { tag: 'es-ES', name: 'Español (España)' },
    { tag: 'fi-FI', name: 'Suomi' },
    { tag: 'fr-CA', name: 'Français (Canada)' },
    { tag: 'fr-FR', name: 'Français' },
    { tag: 'hi-IN', name: 'हिन्दी' },
    { tag: 'id-ID', name: 'Bahasa Indonesia' },
    { tag: 'it-IT', name: 'Italiano' },
    { tag: 'ja-JP', name: '日本語' },
    { tag: 'ko-KR', name: '한국어' },
    { tag: 'nb-NO', name: 'Norsk bokmål' },
    { tag: 'nl-NL', name: 'Nederlands' },
    { tag: 'pl-PL', name: 'Polski' },
    { tag: 'pt-BR', name: 'Português (Brasil)' },
    { tag: 'pt-PT', name: 'Português (Portugal)' },
    { tag: 'ro-RO', name: 'Română' },
    { tag: 'ru-RU', name: 'Русский' },
    { tag: 'sv-SE', name: 'Svenska' },
    { tag: 'tr-TR', name: 'Türkçe' },
    { tag: 'zh-Hans-CN', name: '简体中文' },
    { tag: 'zh-Hant-TW', name: '繁體中文' },
  ];

  const TAGS = LOCALES.map((l) => l.tag);
  const BASE = 'en';

  let dict = {};
  let baseDict = null;
  let lang = BASE;
  let serverLangHint = null;

  // /api/locale reflects the *server process's* environment (LANG etc.), not
  // the browser's — needed because under the Electron wrapper the bundled
  // Chromium's navigator.language often does not track the host OS locale,
  // while the Go process's environment usually does.
  async function fetchServerLangHint() {
    try {
      const res = await fetch('/api/locale');
      const data = await res.json();
      const m = match(data.lang);
      if (m) serverLangHint = m;
    } catch (_) {
      // Endpoint unreachable — the browser's own preferences still apply.
    }
  }

  // Best available locale for a platform tag, tried most specific first.
  //
  // "pt-BR" should find pt-BR exactly; "pt" should find *a* Portuguese rather
  // than falling to English; "en-GB" should find en. Matching only on exact
  // tags would send most of the world to English despite a usable translation
  // being present, which is the failure mode worth avoiding.
  function match(tag) {
    if (!tag) return null;
    const want = String(tag).toLowerCase();
    const exact = TAGS.find((t) => t.toLowerCase() === want);
    if (exact) return exact;
    const primary = want.split('-')[0];
    const sameLang = TAGS.find((t) => t.toLowerCase() === primary);
    if (sameLang) return sameLang;
    return TAGS.find((t) => t.toLowerCase().split('-')[0] === primary) || null;
  }

  function detectSystemLang() {
    if (serverLangHint) return serverLangHint;
    const candidates = (navigator.languages && navigator.languages.length)
      ? navigator.languages
      : [navigator.language || BASE];
    for (const c of candidates) {
      const m = match(c);
      if (m) return m;
    }
    return BASE;
  }

  /** The explicit user choice, or null when following the system. */
  function current() {
    const stored = localStorage.getItem('lang');
    return TAGS.indexOf(stored) !== -1 ? stored : null;
  }

  function resolved() {
    return current() || detectSystemLang();
  }

  function localeFor(tag) {
    return LOCALES.find((l) => l.tag === tag) || LOCALES[0];
  }

  // --- direction ------------------------------------------------------------

  /**
   * Writing direction, and an override that is deliberately independent of the
   * language.
   *
   * Arabic needs the layout mirrored, and mirroring is the kind of thing that
   * looks fine until a real layout meets it. Tying `dir` to the language would
   * mean the only way to check it is to switch to a language you cannot read,
   * where a broken layout and a correct one look much the same. The override
   * lets the mirror be tested against English.
   */
  function dirOverride() {
    const v = localStorage.getItem('dirOverride');
    return (v === 'ltr' || v === 'rtl') ? v : null;
  }

  function setDirOverride(v) {
    if (v === 'ltr' || v === 'rtl') localStorage.setItem('dirOverride', v);
    else localStorage.removeItem('dirOverride');
    applyDir();
    document.dispatchEvent(new CustomEvent('huemux:dirchange', { detail: { dir: dir() } }));
  }

  function dir() {
    return dirOverride() || localeFor(lang).dir || 'ltr';
  }

  function applyDir() {
    document.documentElement.setAttribute('dir', dir());
  }

  // --- dictionary -----------------------------------------------------------

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
    // For strings that need embedded markup (a link mid-sentence). The value
    // is trusted static HTML from our own JSON, not user input.
    root.querySelectorAll('[data-i18n-html]').forEach((el) => {
      el.innerHTML = t(el.getAttribute('data-i18n-html'));
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

  async function fetchDict(tag) {
    const res = await fetch('/shared/i18n/' + tag + '.json');
    if (!res.ok) throw new Error('no dictionary for ' + tag);
    return res.json();
  }

  // Later object wins, but only where it actually has a string. Missing keys
  // therefore fall through to English rather than rendering as a raw key path
  // — which matters with two dozen translations that will lag the source every
  // time a string is added.
  function mergeOver(base, over) {
    const out = Array.isArray(base) ? base.slice() : Object.assign({}, base);
    for (const k of Object.keys(over || {})) {
      const b = out[k];
      const o = over[k];
      out[k] = (o && typeof o === 'object' && b && typeof b === 'object') ? mergeOver(b, o) : o;
    }
    return out;
  }

  async function load(tag) {
    if (!baseDict) baseDict = await fetchDict(BASE);
    if (tag === BASE) { dict = baseDict; return; }
    try {
      dict = mergeOver(baseDict, await fetchDict(tag));
    } catch (_) {
      // A missing or malformed file must not leave the UI blank.
      dict = baseDict;
    }
  }

  async function setLang(choice) {
    if (choice === 'system' || !choice) localStorage.removeItem('lang');
    else localStorage.setItem('lang', choice);
    lang = resolved();
    await load(lang);
    document.documentElement.setAttribute('lang', lang);
    applyDir();
    applyTo(document);
    document.dispatchEvent(new CustomEvent('huemux:langchange', {
      detail: { lang, choice: current() || 'system' },
    }));
  }

  async function init() {
    await fetchServerLangHint();
    lang = resolved();
    await load(lang);
    document.documentElement.setAttribute('lang', lang);
    applyDir();
    applyTo(document);
  }

  // Cross-frame sync for the app.html shell — each page runs in its own iframe
  // and they do not see each other's DOM events, but `storage` fires on every
  // other same-origin window.
  window.addEventListener('storage', (e) => {
    if (e.key === 'lang') setLang(current() || 'system');
    if (e.key === 'dirOverride') applyDir();
  });

  return {
    t, applyTo, setLang, init, current, resolved, dir,
    dirOverride, setDirOverride, localeFor,
    LOCALES, TAGS,
  };
})();
