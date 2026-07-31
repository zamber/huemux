// <huemux-pairing> — the bridge pairing panel, shared by every page that
// might be the first one a user opens.
//
// This lived only in sync.html until the deployment profiles landed, which
// broke a real case: under --profile=lights there is no Sync page to send
// people to, so a fresh install had nowhere to pair from. Pairing is not a
// screen-sync feature, it is a precondition for the whole app, so it belongs
// somewhere both pages can mount it.
//
// Light DOM (no shadow root), matching <huemux-header>: the panel is styled by
// shared/pairing.css using the same theme tokens as everything else, and a
// shadow root would cut it off from them for no benefit.
//
// Communication is by event rather than a callback property, because each page
// owns its own WebSocket (sync.html and lights.html each open one) and events
// do not care about mount ordering: the element emits `huemux:pair-send` with
// the message to put on the wire, and whichever page hosts it forwards that to
// its own send().
class HueMuxPairing extends HTMLElement {
  connectedCallback() {
    if (this._built) return;
    this._built = true;

    this.innerHTML =
      '<h2 data-i18n="pairing.heading">Pair with your Hue bridge</h2>' +
      '<p class="hint" data-pair="message"></p>' +
      '<p class="warning" data-pair="error" hidden></p>' +
      '<div data-pair="discovered"></div>' +
      '<button type="button" data-pair="rescan" data-i18n="pairing.rescan">Search again</button>' +
      '<details data-pair="manual">' +
        '<summary data-i18n="pairing.manualSummary">Enter bridge IP manually</summary>' +
        '<p class="hint" data-i18n="pairing.manualHint"></p>' +
        '<div class="manual-ip-row">' +
          '<input type="text" data-pair="ip" data-i18n-attr="placeholder:pairing.manualPlaceholder">' +
          '<button type="button" data-pair="manual-btn" data-i18n="pairing.manualButton">Pair with this IP</button>' +
        '</div>' +
      '</details>';

    this._message = this.querySelector('[data-pair="message"]');
    this._error = this.querySelector('[data-pair="error"]');
    this._discovered = this.querySelector('[data-pair="discovered"]');
    this._rescan = this.querySelector('[data-pair="rescan"]');
    this._ip = this.querySelector('[data-pair="ip"]');
    this._manualBtn = this.querySelector('[data-pair="manual-btn"]');

    this._rescan.addEventListener('click', () => this._send({ type: 'discover_bridges' }));
    this._manualBtn.addEventListener('click', () => {
      const ip = this._ip.value.trim();
      if (ip) this._send({ type: 'pair', bridge_ip: ip });
    });
    this._ip.addEventListener('keydown', (ev) => {
      if (ev.key === 'Enter') this._manualBtn.click();
    });

    // Re-render on a language switch: the status-driven strings below are set
    // from JS, so applyTo() alone would leave them in the previous language
    // until the next status push happened to arrive.
    this._onLang = () => {
      this._applyI18n();
      this.update(this._last || {});
    };
    document.addEventListener('huemux:langchange', this._onLang);

    this._applyI18n();
    this.update({});
  }

  disconnectedCallback() {
    if (this._onLang) document.removeEventListener('huemux:langchange', this._onLang);
  }

  // i18n.js declares `const HueMuxI18n = ...` at the top level of a classic
  // script, and a top-level const is *not* a property of window — only var and
  // function declarations are. Guarding on `window.HueMuxI18n` therefore always
  // saw undefined and silently fell through to the English fallbacks, so the
  // status-driven strings below never translated. Caught only because a
  // language switch was actually exercised in a browser; it looks correct on
  // the page as long as English is the language. Use the bare identifier with
  // a typeof guard, which reads the global lexical scope where it really lives.
  _hasI18n() {
    return typeof HueMuxI18n !== 'undefined';
  }

  _applyI18n() {
    if (this._hasI18n()) HueMuxI18n.applyTo(this);
  }

  _t(key, fallback) {
    if (!this._hasI18n()) return fallback;
    const s = HueMuxI18n.t(key);
    // t() returns the key itself when a translation is missing; falling back
    // keeps a half-translated dictionary from printing "pairing.searching" at
    // someone instead of a sentence.
    return s && s !== key ? s : fallback;
  }

  _send(msg) {
    this.dispatchEvent(new CustomEvent('huemux:pair-send', { detail: msg, bubbles: true }));
  }

  // update renders the server's pairing state. Safe to call with {} — that is
  // the pre-first-status case, which should look like "searching" rather than
  // an empty panel.
  update(p) {
    if (!this._built) return;
    this._last = p;

    if (p.error) {
      this._error.textContent = p.error;
      this._error.hidden = false;
    } else {
      this._error.hidden = true;
    }

    if (p.pairing) {
      this._message.textContent = p.message || this._t('pairing.inProgress', 'Pairing…');
    } else if (p.discovering) {
      this._message.textContent = this._t('pairing.searching', 'Searching for a bridge on your network…');
    } else if (p.discovered && p.discovered.length > 0) {
      this._message.textContent = this._t('pairing.found', 'Found a bridge:');
    } else {
      this._message.textContent = this._t('pairing.notFound', 'No bridge found automatically — enter its IP manually below.');
    }

    this._discovered.innerHTML = '';
    for (const b of p.discovered || []) {
      this._discovered.appendChild(this._bridgeCard(b, p));
    }

    this._rescan.disabled = !!p.discovering || !!p.pairing;
    this._manualBtn.disabled = !!p.pairing;
  }

  _bridgeCard(b, p) {
    const card = document.createElement('div');
    card.className = 'bridge-card' + (b.supported ? '' : ' unsupported');

    const info = document.createElement('div');
    info.className = 'bridge-info';

    const name = document.createElement('span');
    name.className = 'bridge-name';
    name.textContent = b.name || this._t('pairing.bridgeFallbackName', 'Hue Bridge');

    const ip = document.createElement('span');
    ip.className = 'bridge-ip';
    ip.textContent = b.supported
      ? b.ip
      : b.ip + ' — ' + this._t('pairing.tooOld', 'too old for Entertainment areas');

    info.appendChild(name);
    info.appendChild(ip);
    card.appendChild(info);

    // An unsupported (round, v1) bridge gets no button at all rather than a
    // disabled one: pairing would succeed and then fail confusingly later at
    // the DTLS stage, so the honest answer is to not offer it.
    if (b.supported) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.textContent = p.pairing
        ? this._t('pairing.inProgress', 'Pairing…')
        : this._t('pairing.pairAction', 'Pair');
      btn.disabled = !!p.pairing;
      btn.addEventListener('click', () => this._send({ type: 'pair', bridge_ip: b.ip }));
      card.appendChild(btn);
    }
    return card;
  }
}

customElements.define('huemux-pairing', HueMuxPairing);
