// Theme cycling (system -> light -> dark -> system), ported from lights-ui's
// svelte/src/lib/theme.ts. Plain global script, no module/bundler, matching
// this repo's existing web/app.js convention.
//
// The FOUC-avoidance step (reading localStorage and setting data-theme
// before first paint) is NOT here — it has to run before this file has even
// been requested, so it's inlined directly in each page's <head>. See the
// inline snippet each of lights.html/sync.html carries; keep it in sync with
// THEME_COLOR below if either changes.

const HueMuxTheme = (() => {
  const THEME_COLOR = { dark: '#000000', light: '#f7f3ea' };

  // Five choices, not three. "simple-light"/"simple-dark" keep the palette and
  // the accent colours but drop the per-card blurred gradient layers and the
  // hover elevation — the most expensive things the UI composites, and purely
  // decorative. On a weak panel that is the difference between a rebuild
  // costing ~325ms and something far cheaper; on a desktop the full themes
  // cost nothing noticeable, so both stay available.
  //
  // Stored client-side, so a phone can keep the full look while a wall panel
  // runs simple. The server neither knows nor cares.
  const CHOICES = ['system', 'light', 'dark', 'simple-light', 'simple-dark'];
  const CYCLE = {
    system: 'light',
    light: 'dark',
    dark: 'simple-light',
    'simple-light': 'simple-dark',
    'simple-dark': 'system',
  };

  // Which base palette a choice resolves to, for the address-bar colour.
  const BASE = {
    light: 'light', 'simple-light': 'light',
    dark: 'dark', 'simple-dark': 'dark',
  };

  function current() {
    const stored = localStorage.getItem('theme');
    return CHOICES.indexOf(stored) > 0 ? stored : 'system';
  }

  // resolved answers "light or dark?" — the base palette, ignoring whether the
  // simple variant is in play. Callers that need the variant use current().
  function resolved(choice) {
    if (choice === 'system') {
      return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    return BASE[choice] || choice;
  }

  // simple() reports whether decoration should be stripped. Kept separate from
  // the palette so the two are independently addressable in CSS.
  function simple(choice) {
    return choice === 'simple-light' || choice === 'simple-dark';
  }

  function updateThemeColorMeta(choice) {
    const meta = document.getElementById('theme-color-meta');
    if (!meta) return;
    meta.setAttribute('content', THEME_COLOR[resolved(choice)]);
  }

  function applyToDOM(choice) {
    const root = document.documentElement;
    // data-theme stays light|dark so every existing palette rule keeps
    // working untouched; the variant rides on a separate attribute.
    if (choice === 'system') {
      root.removeAttribute('data-theme');
    } else {
      root.setAttribute('data-theme', resolved(choice));
    }
    if (simple(choice)) {
      root.setAttribute('data-simple', '');
    } else {
      root.removeAttribute('data-simple');
    }
    updateThemeColorMeta(choice);
    document.dispatchEvent(new CustomEvent('huemux:themechange', { detail: { choice } }));
  }

  function apply(choice) {
    if (choice === 'system') localStorage.removeItem('theme');
    else localStorage.setItem('theme', choice);
    applyToDOM(choice);
  }

  function cycle() {
    const next = CYCLE[current()];
    apply(next);
    return next;
  }

  // While in "system" mode, keep the address-bar color live if the OS
  // preference changes without a page reload.
  window.matchMedia('(prefers-color-scheme: light)').addEventListener('change', () => {
    if (current() === 'system') updateThemeColorMeta('system');
  });

  // The app.html shell embeds sync.html/lights.html each in their own
  // iframe — separate browsing contexts that don't see each other's DOM
  // events, but the native `storage` event fires on every other same-origin
  // window when localStorage changes, which is exactly the cross-frame
  // signal needed here (this listener never fires in the frame that made
  // the change itself, only in the others).
  window.addEventListener('storage', (e) => {
    if (e.key === 'theme') applyToDOM(current());
  });

  return { current, resolved, cycle, simple, CHOICES };
})();
