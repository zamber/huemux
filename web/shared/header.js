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
    this._onLangChange = () => { this._renderLang(); HueMuxI18n.applyTo(this); };
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
        '<a class="ls-brand" href="/lights.html" data-i18n="app.name">HueMux</a>' +
        '<nav class="ls-nav"></nav>' +
        '<div class="ls-header-actions">' +
          '<button type="button" id="ls-theme-btn" class="ls-icon-btn"></button>' +
          '<button type="button" id="ls-lang-btn" class="ls-icon-btn"></button>' +
        '</div>' +
      '</div>';

    this._themeBtn = this.querySelector('#ls-theme-btn');
    this._langBtn = this.querySelector('#ls-lang-btn');
    this._themeBtn.addEventListener('click', () => HueMuxTheme.cycle());
    this._langBtn.addEventListener('click', () => HueMuxI18n.cycle());

    this._renderNav();
    this._renderTheme();
    this._renderLang();
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
    // With one feature tab there is nothing to navigate between, so a single
    // permanently-active link would just be noise — but Settings still needs
    // a way in, so it is always present.
    if (!(f.lights && f.sync)) html = '';
    html += '<a href="/settings.html" data-tab="settings" data-i18n="nav.settings"' +
      (active === 'settings' ? ' class="active"' : '') + '>Settings</a>';
    nav.innerHTML = html;
    HueMuxI18n.applyTo(nav);
  }

  _renderTheme() {
    // Reads through HueMuxTheme.current() rather than localStorage directly,
    // so an unrecognised stored value falls back to 'system' in one place.
    const choice = (typeof HueMuxTheme !== 'undefined') ? HueMuxTheme.current() : 'system';
    // The simple variants reuse their palette's icon with a dot, rather than
    // inventing a symbol: they are the same light/dark theme with the
    // decoration removed, and the title says which.
    const icons = {
      system: '🌗', light: '☀️', dark: '🌙',
      'simple-light': '☀', 'simple-dark': '🌒',
    };
    const titleKeys = {
      system: 'theme.titleSystem', light: 'theme.titleLight', dark: 'theme.titleDark',
      'simple-light': 'theme.titleSimpleLight', 'simple-dark': 'theme.titleSimpleDark',
    };
    this._themeBtn.textContent = icons[choice] || icons.system;
    const title = HueMuxI18n.t(titleKeys[choice] || titleKeys.system);
    this._themeBtn.setAttribute('title', title);
    this._themeBtn.setAttribute('aria-label', title);
  }

  _renderLang() {
    const choice = HueMuxI18n.current() || 'system';
    const labels = { system: 'A', en: 'EN', pl: 'PL' };
    const titleKeys = { system: 'lang.titleSystem', en: 'lang.titleEn', pl: 'lang.titlePl' };
    this._langBtn.textContent = labels[choice] || labels.system;
    const title = HueMuxI18n.t(titleKeys[choice] || titleKeys.system);
    this._langBtn.setAttribute('title', title);
    this._langBtn.setAttribute('aria-label', title);
  }
}

customElements.define('huemux-header', HueMuxHeader);

// Any other in-page link to /sync.html or /lights.html (e.g. lights.html's
// unpaired-panel "Go to Sync" links) needs the same shell-aware handling as
// the nav links above, or clicking it navigates only the iframe itself —
// leaving the shell's own top-level header/URL pointing at the tab you
// clicked away from while the iframe you're looking at shows the other
// page's content. Same-origin iframe, so a direct window.top reference
// works without postMessage.
if (window.self !== window.top) {
  document.addEventListener('click', (e) => {
    const a = e.target.closest('a[href="/lights.html"], a[href="/sync.html"]');
    if (!a || !window.top.HueMuxShell) return;
    e.preventDefault();
    window.top.HueMuxShell.switchTab(a.getAttribute('href').indexOf('lights') !== -1 ? 'lights' : 'sync');
  });
}
