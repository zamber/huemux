// <lightsync-header active="lights|sync"> — shared nav + theme/language
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
//     window.LightsyncShell.switchTab instead of navigating, and the
//     element skips rendering itself entirely on the *embedded* copies
//     (window.self !== window.top) since the shell's own top-level header
//     is the one actually shown.
class LightsyncHeader extends HTMLElement {
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
      if (window.LightsyncShell) {
        e.preventDefault();
        window.LightsyncShell.switchTab(a.dataset.tab);
      }
    };
    this.addEventListener('click', this._onClick);

    this._onThemeChange = () => this._renderTheme();
    this._onLangChange = () => { this._renderLang(); LightsyncI18n.applyTo(this); };
    document.addEventListener('lightsync:themechange', this._onThemeChange);
    document.addEventListener('lightsync:langchange', this._onLangChange);
  }

  disconnectedCallback() {
    if (this._onClick) this.removeEventListener('click', this._onClick);
    document.removeEventListener('lightsync:themechange', this._onThemeChange);
    document.removeEventListener('lightsync:langchange', this._onLangChange);
  }

  attributeChangedCallback(name) {
    if (name === 'active' && this.isConnected && window.self === window.top) {
      this._renderNav();
    }
  }

  _render() {
    this.innerHTML =
      '<div class="ls-header-inner">' +
        '<a class="ls-brand" href="/lights.html" data-i18n="app.name">lightsync</a>' +
        '<nav class="ls-nav"></nav>' +
        '<div class="ls-header-actions">' +
          '<button type="button" id="ls-theme-btn" class="ls-icon-btn"></button>' +
          '<button type="button" id="ls-lang-btn" class="ls-icon-btn"></button>' +
        '</div>' +
      '</div>';

    this._themeBtn = this.querySelector('#ls-theme-btn');
    this._langBtn = this.querySelector('#ls-lang-btn');
    this._themeBtn.addEventListener('click', () => LightsyncTheme.cycle());
    this._langBtn.addEventListener('click', () => LightsyncI18n.cycle());

    this._renderNav();
    this._renderTheme();
    this._renderLang();
    LightsyncI18n.applyTo(this);
  }

  _renderNav() {
    const active = this.getAttribute('active') || '';
    const nav = this.querySelector('.ls-nav');
    if (!nav) return;
    nav.innerHTML =
      '<a href="/lights.html" data-tab="lights" data-i18n="nav.lights"' + (active === 'lights' ? ' class="active"' : '') + '>Lights</a>' +
      '<a href="/sync.html" data-tab="sync" data-i18n="nav.sync"' + (active === 'sync' ? ' class="active"' : '') + '>Sync</a>';
    LightsyncI18n.applyTo(nav);
  }

  _renderTheme() {
    const choice = localStorage.getItem('theme') || 'system';
    const icons = { system: '🌗', light: '☀️', dark: '🌙' };
    const titleKeys = { system: 'theme.titleSystem', light: 'theme.titleLight', dark: 'theme.titleDark' };
    this._themeBtn.textContent = icons[choice] || icons.system;
    const title = LightsyncI18n.t(titleKeys[choice] || titleKeys.system);
    this._themeBtn.setAttribute('title', title);
    this._themeBtn.setAttribute('aria-label', title);
  }

  _renderLang() {
    const choice = LightsyncI18n.current() || 'system';
    const labels = { system: 'A', en: 'EN', pl: 'PL' };
    const titleKeys = { system: 'lang.titleSystem', en: 'lang.titleEn', pl: 'lang.titlePl' };
    this._langBtn.textContent = labels[choice] || labels.system;
    const title = LightsyncI18n.t(titleKeys[choice] || titleKeys.system);
    this._langBtn.setAttribute('title', title);
    this._langBtn.setAttribute('aria-label', title);
  }
}

customElements.define('lightsync-header', LightsyncHeader);
