// Shell page: hosts sync.html and lights.html each in their own iframe so
// switching between them is a CSS visibility toggle, not a navigation —
// crucially, this keeps whichever page's WebSocket connection (and, for
// the sync page, an in-progress screen-capture stream) alive while you
// look at the other one. A plain <a href> nav between the two pages used
// to tear down whichever one you left, which meant you could never run
// screen sync and control lights at the same time.
window.HueMuxShell = (() => {
  // Only frames the page actually rendered. app.html omits the iframe for a
  // tab the profile disabled, so this map is built from what exists rather
  // than from a fixed pair — otherwise switchTab would target a null element
  // and the shell would land on a frame that was never created.
  const frames = {};
  for (const tab of ['sync', 'lights', 'settings']) {
    const el = document.getElementById('frame-' + tab);
    if (el) frames[tab] = el;
  }

  // Whichever frame exists, preferring sync to match the historical default.
  let active = frames.sync ? 'sync' : (frames.lights ? 'lights' : (frames.settings ? 'settings' : null));

  const NAV_KEY = { lights: 'nav.lights', sync: 'nav.sync', settings: 'nav.settings' };
  const PATH = { lights: '/lights.html', sync: '/sync.html', settings: '/settings.html' };

  function titleFor(tab) {
    return 'HueMux — ' + HueMuxI18n.t(NAV_KEY[tab] || NAV_KEY.sync);
  }

  function switchTab(tab) {
    if (!frames[tab] || !active) return;
    if (tab !== active) {
      frames[active].classList.remove('active');
      frames[tab].classList.add('active');
      active = tab;
      const header = document.querySelector('huemux-header');
      if (header) header.setAttribute('active', tab);
      history.replaceState(null, '', PATH[tab] || PATH.sync);
    }
    document.title = titleFor(tab);
  }

  document.addEventListener('huemux:langchange', () => { document.title = titleFor(active); });

  // Honour the URL only if that tab exists in this build; otherwise fall back
  // to whatever single frame there is. A lights-only deployment opened at
  // /sync.html used to land on a frame that does not exist and show nothing.
  const wanted = location.pathname.indexOf('lights') !== -1 ? 'lights' : 'sync';
  const initial = frames[wanted] ? wanted : active;
  if (initial) {
    frames[initial].classList.add('active');
    active = initial;
    const header = document.querySelector('huemux-header');
    if (header) header.setAttribute('active', initial);
    document.title = titleFor(initial);
  }

  // has() lets callers ask before preventing a link's default action, rather
  // than discovering afterwards that switchTab silently did nothing.
  return { switchTab, current: () => active, has: (tab) => !!frames[tab] };
})();
