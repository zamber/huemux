// Drives the `data-scrolling` attribute that shared/scrollbars.css keys its
// scroll indicators off. See that file for why the thumb fades rather than
// being removed.
//
// The listener is on `document` with capture: true, not on window. Scroll
// events do not bubble from an element, so a window-level listener would see
// the page scrolling and nothing else — every nested scroller in this UI (the
// room dropdown, the scene strips, the diagnostics textarea) would keep an
// invisible thumb while being scrolled, which is the one moment it needs to be
// visible. Capture phase catches all of them with one registration, including
// scrollers added long after this runs.
(function () {
  const HIDE_AFTER_MS = 900;
  const root = document.documentElement;
  let timer = null;

  function onScroll() {
    root.setAttribute('data-scrolling', '');
    clearTimeout(timer);
    timer = setTimeout(() => root.removeAttribute('data-scrolling'), HIDE_AFTER_MS);
  }

  // passive: the handler never calls preventDefault, and saying so keeps it off
  // the critical path of the scroll it is reacting to.
  document.addEventListener('scroll', onScroll, { capture: true, passive: true });
})();
