// Theme state, ported from lights-ui's svelte/src/lib/theme.ts. Plain global
// script, no module/bundler, matching this repo's existing web/app.js
// convention.
//
// Two independent axes, not one five-state cycle:
//
//   palette  system | light | dark      (localStorage "theme")
//   simple   on | off                   (localStorage "themeSimple")
//
// They were briefly folded into a single list — system, light, dark,
// simple-light, simple-dark — cycled by one button. That is five taps to get
// back where you started, through icons that differ by a crescent, and it read
// as a random loop rather than a setting. They are genuinely orthogonal
// (simple-dark is the dark palette with decoration removed), the DOM already
// modelled them as two attributes, and now so do the storage and the UI.
//
// The FOUC-avoidance step (reading both keys and setting the attributes before
// first paint) is NOT here — it has to run before this file has even been
// requested, so it's inlined in each page's <head>. Keep those snippets in
// sync with PALETTES/THEME_COLOR/SIMPLE_KEY below.

const HueMuxTheme = (() => {
  const THEME_COLOR = { dark: '#000000', light: '#f7f3ea' };

  const PALETTES = ['system', 'light', 'dark'];
  const NEXT = { system: 'light', light: 'dark', dark: 'system' };

  const KEY = 'theme';
  const SIMPLE_KEY = 'themeSimple';

  // The old combined values still sit in localStorage on any device that ran
  // an earlier build. Split them on first read rather than silently falling
  // back to "system", which would look like the theme reset itself.
  function migrate() {
    const stored = localStorage.getItem(KEY);
    if (stored !== 'simple-light' && stored !== 'simple-dark') return;
    localStorage.setItem(KEY, stored === 'simple-light' ? 'light' : 'dark');
    localStorage.setItem(SIMPLE_KEY, '1');
  }
  migrate();

  /** 'system' | 'light' | 'dark' — the palette, ignoring decoration. */
  function palette() {
    const stored = localStorage.getItem(KEY);
    return PALETTES.indexOf(stored) > 0 ? stored : 'system';
  }

  /** Whether the decorative layers are stripped. Independent of palette. */
  function isSimple() {
    return localStorage.getItem(SIMPLE_KEY) === '1';
  }

  // resolved answers "light or dark?" for a palette choice, following the OS
  // when it is 'system'.
  function resolved(choice) {
    const p = choice || palette();
    if (p === 'system') {
      return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    return p;
  }

  function updateThemeColorMeta() {
    const meta = document.getElementById('theme-color-meta');
    if (!meta) return;
    meta.setAttribute('content', THEME_COLOR[resolved()]);
  }

  function applyToDOM() {
    const root = document.documentElement;
    // data-theme stays light|dark so every existing palette rule keeps
    // working untouched; the variant rides on a separate attribute.
    if (palette() === 'system') {
      root.removeAttribute('data-theme');
    } else {
      root.setAttribute('data-theme', resolved());
    }
    if (isSimple()) {
      root.setAttribute('data-simple', '');
    } else {
      root.removeAttribute('data-simple');
    }
    updateThemeColorMeta();
    document.dispatchEvent(new CustomEvent('huemux:themechange', {
      detail: { palette: palette(), simple: isSimple() },
    }));
  }

  function setPalette(choice) {
    if (choice === 'system') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, choice);
    applyToDOM();
  }

  function setSimple(on) {
    if (on) localStorage.setItem(SIMPLE_KEY, '1');
    else localStorage.removeItem(SIMPLE_KEY);
    applyToDOM();
  }

  /** system → light → dark → system. Three states, three distinct icons. */
  function cycle() {
    const next = NEXT[palette()];
    setPalette(next);
    return next;
  }

  function toggleSimple() {
    const next = !isSimple();
    setSimple(next);
    return next;
  }

  // While in "system" mode, keep the address-bar color live if the OS
  // preference changes without a page reload.
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
    if (palette() === 'system') updateThemeColorMeta();
  });

  // The app.html shell embeds each page in its own iframe — separate browsing
  // contexts that don't see each other's DOM events, but the native `storage`
  // event fires on every other same-origin window when localStorage changes,
  // which is exactly the cross-frame signal needed here (this listener never
  // fires in the frame that made the change itself, only in the others).
  window.addEventListener('storage', (e) => {
    if (e.key === KEY || e.key === SIMPLE_KEY) applyToDOM();
  });

  return { palette, isSimple, resolved, cycle, toggleSimple, setPalette, setSimple, PALETTES };
})();
