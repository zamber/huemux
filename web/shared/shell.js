// Shell page: hosts sync.html and lights.html each in their own iframe so
// switching between them is a CSS visibility toggle, not a navigation —
// crucially, this keeps whichever page's WebSocket connection (and, for
// the sync page, an in-progress screen-capture stream) alive while you
// look at the other one. A plain <a href> nav between the two pages used
// to tear down whichever one you left, which meant you could never run
// screen sync and control lights at the same time.
window.HueMuxShell = (() => {
  const frames = {
    sync: document.getElementById('frame-sync'),
    lights: document.getElementById('frame-lights'),
  };
  let active = 'sync';

  function titleFor(tab) {
    const key = tab === 'lights' ? 'nav.lights' : 'nav.sync';
    return 'HueMux — ' + HueMuxI18n.t(key);
  }

  function switchTab(tab) {
    if (!frames[tab]) return;
    if (tab !== active) {
      frames[active].classList.remove('active');
      frames[tab].classList.add('active');
      active = tab;
      const header = document.querySelector('huemux-header');
      if (header) header.setAttribute('active', tab);
      history.replaceState(null, '', tab === 'lights' ? '/lights.html' : '/sync.html');
    }
    document.title = titleFor(tab);
  }

  document.addEventListener('huemux:langchange', () => { document.title = titleFor(active); });

  const initial = location.pathname.indexOf('lights') !== -1 ? 'lights' : 'sync';
  switchTab(initial);

  return { switchTab, current: () => active };
})();
