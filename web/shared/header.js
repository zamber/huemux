// <huemux-header active="lights|sync"> — shared nav + theme/language
// toggles. Light DOM (no shadow root) on purpose: it needs theme.css's
// custom properties and the page's own stylesheet to cascade in, and this
// repo has no build step to inject styles into a shadow root per-component.
//
// Two contexts render this element:
//   - Standalone (lights.html/sync.html loaded directly): a normal <a href>
//     nav, full page navigation between the two.
//   - Inside the app.html shell, where each page lives in its own iframe so
//     switching tabs doesn't tear down the other one's WS connection (see
//     shared/shell.js): nav clicks are intercepted and delegate to
//     window.HueMuxShell.switchTab instead of navigating, and the
//     element skips rendering itself entirely on the *embedded* copies
//     (window.self !== window.top) since the shell's own top-level header
//     is the one actually shown.
class HueMuxHeader extends HTMLElement {
  static get observedAttributes() { return ['active']; }

  connectedCallback() {
    if (window.self !== window.top) {
      // Embedded in the shell's iframe — the shell's own top-level header
      // already renders nav/theme/lang for the whole app.
      this.hidden = true;
      return;
    }
    this._render();

    this._onClick = (e) => {
      const a = e.target.closest('a[data-tab]');
      if (!a) return;
      // Only hijack tabs the shell actually hosts as a frame. Settings is a
      // real page, not a shell tab, so preventing its navigation and then
      // calling switchTab('settings') — which no-ops on an unknown tab — left
      // the link completely dead. Anything the shell does not own falls
      // through to being an ordinary link.
      if (window.HueMuxShell && window.HueMuxShell.has && window.HueMuxShell.has(a.dataset.tab)) {
        e.preventDefault();
        window.HueMuxShell.switchTab(a.dataset.tab);
      }
    };
    this.addEventListener('click', this._onClick);

    this._onThemeChange = () => this._renderTheme();
    this._onLangChange = () => { this._renderTheme(); HueMuxI18n.applyTo(this); };
    document.addEventListener('huemux:themechange', this._onThemeChange);
    document.addEventListener('huemux:langchange', this._onLangChange);
    this._onFeatures = () => this._renderNav();
    document.addEventListener('huemux:features', this._onFeatures);
  }

  disconnectedCallback() {
    if (this._onClick) this.removeEventListener('click', this._onClick);
    document.removeEventListener('huemux:themechange', this._onThemeChange);
    document.removeEventListener('huemux:langchange', this._onLangChange);
    document.removeEventListener('huemux:features', this._onFeatures);
  }

  attributeChangedCallback(name) {
    if (name === 'active' && this.isConnected && window.self === window.top) {
      this._renderNav();
    }
  }

  _render() {
    this.innerHTML =
      '<div class="ls-header-inner">' +
        // href="/" rather than a page: the shell is what "home" means here,
        // and a direct link to lights.html would navigate the top window out
        // of the shell and tear down every frame in it. data-tab lets the
        // click be intercepted into a tab switch when that tab exists.
        '<a class="ls-brand" href="/" data-tab="lights">' +
          '<img class="ls-brand-mark" id="ls-brand-mark" alt="" width="22" height="22">' +
          '<span data-i18n="app.name">HueMux</span>' +
        '</a>' +
        '<nav class="ls-nav"></nav>' +
        '<div class="ls-header-actions">' +
          '<button type="button" id="ls-logout-btn" class="ls-icon-btn" hidden title="Log out" aria-label="Log out">🔒</button>' +
          '<button type="button" id="ls-theme-btn" class="ls-icon-btn"></button>' +
          '<button type="button" id="ls-simple-btn" class="ls-icon-btn">✨</button>' +
        '</div>' +
      '</div>';

    this._brandMark = this.querySelector('#ls-brand-mark');
    this._themeBtn = this.querySelector('#ls-theme-btn');
    this._simpleBtn = this.querySelector('#ls-simple-btn');
    this._logoutBtn = this.querySelector('#ls-logout-btn');
    this._themeBtn.addEventListener('click', () => HueMuxTheme.cycle());
    this._simpleBtn.addEventListener('click', () => HueMuxTheme.toggleSimple());

    // Logout button — only shown when a token is stored.
    if (this._logoutBtn) {
      this._logoutBtn.addEventListener('click', function () { logout(); });
      if (typeof hasAuthToken === 'function' && hasAuthToken()) {
        this._logoutBtn.hidden = false;
      }
    }

    this._renderNav();
    this._renderTheme();
    HueMuxI18n.applyTo(this);
  }

  _renderNav() {
    const active = this.getAttribute('active') || '';
    const nav = this.querySelector('.ls-nav');
    if (!nav) return;

    // Which tabs exist comes from the server, because the profile decides it
    // and the server is the only thing that knows. HueMuxFeatures defaults to
    // both-enabled until /api/config answers, so the nav renders immediately
    // and settles rather than flickering in from empty.
    const f = (window.HueMuxFeatures && window.HueMuxFeatures.current()) || { lights: true, sync: true };

    let html = '';
    if (f.lights) {
      html += '<a href="/lights.html" data-tab="lights" data-i18n="nav.lights"' +
        (active === 'lights' ? ' class="active"' : '') + '>Lights</a>';
    }
    if (f.sync) {
      html += '<a href="/sync.html" data-tab="sync" data-i18n="nav.sync"' +
        (active === 'sync' ? ' class="active"' : '') + '>Sync</a>';
    }
    // Every tab that exists gets a link, including when there is only one.
    //
    // A single feature tab used to render no link at all, on the reasoning
    // that a permanently-active link is noise. It is worse than noise: with
    // the nav showing only Settings, opening Settings under a lights-only or
    // sync-only profile left nothing on screen that could go back, and the
    // profile control is on that very page — so the way out was to change the
    // profile you had just come to change.
    html += '<a href="/settings.html" data-tab="settings" data-i18n="nav.settings"' +
      (active === 'settings' ? ' class="active"' : '') + '>Settings</a>';
    nav.innerHTML = html;
    HueMuxI18n.applyTo(nav);
  }

  _renderTheme() {
    // Reads through HueMuxTheme rather than localStorage directly, so an
    // unrecognised stored value falls back to 'system' in one place.
    const has = typeof HueMuxTheme !== 'undefined';
    const choice = has ? HueMuxTheme.palette() : 'system';
    // Three palettes, three shapes that cannot be confused at 16px: a sun, a
    // moon, and a half-lit moon for "whatever the system says". The previous
    // five-icon cycle distinguished states by a crescent's thickness, which is
    // not a distinction anyone can make on a phone.
    const icons = { system: '🌓', light: '☀️', dark: '🌙' };
    const titleKeys = {
      system: 'theme.titleSystem', light: 'theme.titleLight', dark: 'theme.titleDark',
    };
    // The mark's convergence node is white on dark and near-black on light,
    // so it needs the resolved palette rather than the chosen one — "system"
    // has to become an actual colour before an image can be picked.
    if (this._brandMark) {
      const dark = !has || HueMuxTheme.resolved() === 'dark';
      this._brandMark.src = dark ? '/shared/icon-mark.svg' : '/shared/icon-mark-light.svg';
    }

    this._themeBtn.textContent = icons[choice] || icons.system;
    const title = HueMuxI18n.t(titleKeys[choice] || titleKeys.system);
    this._themeBtn.setAttribute('title', title);
    this._themeBtn.setAttribute('aria-label', title);

    // Decoration is a separate axis, so it gets a separate control. One glyph
    // in two states — lit and dimmed — rather than a second near-identical
    // symbol; aria-pressed carries the same fact for screen readers.
    if (!this._simpleBtn) return;
    const simple = has && HueMuxTheme.isSimple();
    this._simpleBtn.classList.toggle('off', simple);
    this._simpleBtn.setAttribute('aria-pressed', simple ? 'true' : 'false');
    const st = HueMuxI18n.t(simple ? 'theme.titleEffectsOff' : 'theme.titleEffectsOn');
    this._simpleBtn.setAttribute('title', st);
    this._simpleBtn.setAttribute('aria-label', st);
  }

}

customElements.define('huemux-header', HueMuxHeader);

// Any other in-page link to another tab's page (e.g. lights.html's
// unpaired-panel "Go to Sync" link) needs the same shell-aware handling as
// the nav links above, or clicking it navigates only the iframe itself —
// leaving the shell's own top-level header/URL pointing at the tab you
// clicked away from while the iframe you're looking at shows the other
// page's content. Same-origin iframe, so a direct window.top reference
// works without postMessage.
if (window.self !== window.top) {
  const TAB_FOR_PATH = {
    '/lights.html': 'lights',
    '/sync.html': 'sync',
    '/settings.html': 'settings',
  };
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[href]');
    if (!a) return;
    const tab = TAB_FOR_PATH[a.getAttribute('href')];
    const shell = window.top.HueMuxShell;
    // Only intercept what the shell can actually show. Otherwise the link
    // keeps its normal behaviour rather than being silently swallowed.
    if (!tab || !shell || !shell.has(tab)) return;
    e.preventDefault();
    shell.switchTab(tab);
  });
}
