// Theme cycling (system -> light -> dark -> system), ported from lights-ui's
// svelte/src/lib/theme.ts. Plain global script, no module/bundler, matching
// this repo's existing web/app.js convention.
//
// The FOUC-avoidance step (reading localStorage and setting data-theme
// before first paint) is NOT here — it has to run before this file has even
// been requested, so it's inlined directly in each page's <head>. See the
// inline snippet each of lights.html/sync.html carries; keep it in sync with
// THEME_COLOR below if either changes.

const LightsyncTheme = (() => {
  const THEME_COLOR = { dark: '#000000', light: '#f7f3ea' };
  const CYCLE = { system: 'light', light: 'dark', dark: 'system' };

  function current() {
    const stored = localStorage.getItem('theme');
    return stored === 'light' || stored === 'dark' ? stored : 'system';
  }

  function resolved(choice) {
    if (choice === 'system') {
      return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }
    return choice;
  }

  function updateThemeColorMeta(choice) {
    const meta = document.getElementById('theme-color-meta');
    if (!meta) return;
    meta.setAttribute('content', THEME_COLOR[resolved(choice)]);
  }

  function apply(choice) {
    if (choice === 'system') {
      localStorage.removeItem('theme');
      document.documentElement.removeAttribute('data-theme');
    } else {
      localStorage.setItem('theme', choice);
      document.documentElement.setAttribute('data-theme', choice);
    }
    updateThemeColorMeta(choice);
    document.dispatchEvent(new CustomEvent('lightsync:themechange', { detail: { choice } }));
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

  return { current, resolved, cycle };
})();
