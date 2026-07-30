// <lightsync-header active="lights|sync"> — shared nav + theme/language
// toggles. Light DOM (no shadow root) on purpose: it needs theme.css's
// custom properties and the page's own stylesheet to cascade in, and this
// repo has no build step to inject styles into a shadow root per-component.
class LightsyncHeader extends HTMLElement {
  connectedCallback() {
    const active = this.getAttribute('active') || '';
    this.innerHTML =
      '<div class="ls-header-inner">' +
        '<a class="ls-brand" href="/lights.html" data-i18n="app.name">lightsync</a>' +
        '<nav class="ls-nav">' +
          '<a href="/lights.html" data-i18n="nav.lights"' + (active === 'lights' ? ' class="active"' : '') + '>Lights</a>' +
          '<a href="/sync.html" data-i18n="nav.sync"' + (active === 'sync' ? ' class="active"' : '') + '>Sync</a>' +
        '</nav>' +
        '<div class="ls-header-actions">' +
          '<button type="button" id="ls-theme-btn" class="ls-icon-btn"></button>' +
          '<button type="button" id="ls-lang-btn" class="ls-icon-btn"></button>' +
        '</div>' +
      '</div>';

    this._themeBtn = this.querySelector('#ls-theme-btn');
    this._langBtn = this.querySelector('#ls-lang-btn');

    this._themeBtn.addEventListener('click', () => LightsyncTheme.cycle());
    this._langBtn.addEventListener('click', () => LightsyncI18n.cycle());

    this._onThemeChange = () => this._renderTheme();
    this._onLangChange = () => { this._renderLang(); LightsyncI18n.applyTo(this); };
    document.addEventListener('lightsync:themechange', this._onThemeChange);
    document.addEventListener('lightsync:langchange', this._onLangChange);

    this._renderTheme();
    this._renderLang();
    LightsyncI18n.applyTo(this);
  }

  disconnectedCallback() {
    document.removeEventListener('lightsync:themechange', this._onThemeChange);
    document.removeEventListener('lightsync:langchange', this._onLangChange);
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
