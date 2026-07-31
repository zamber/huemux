// Click-to-zoom for screenshots: no library, matches the rest of this
// project's plain-JS-no-build-step approach. Any <img class="zoomable">
// opens full-size in a fixed overlay; click anywhere, Escape, or clicking
// the image again closes it.
(function () {
  const overlay = document.createElement('div');
  overlay.id = 'lightbox';
  overlay.innerHTML = '<img alt=""><div class="lightbox-hint">Click or press Esc to close</div>';
  document.body.appendChild(overlay);
  const overlayImg = overlay.querySelector('img');

  function open(src, alt) {
    overlayImg.src = src;
    overlayImg.alt = alt || '';
    overlay.classList.add('open');
  }
  function close() {
    overlay.classList.remove('open');
    overlayImg.src = '';
  }

  document.addEventListener('click', (e) => {
    const img = e.target.closest('img.zoomable');
    if (img) { open(img.currentSrc || img.src, img.alt); return; }
    if (e.target === overlay || e.target === overlayImg) close();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') close();
  });
})();
