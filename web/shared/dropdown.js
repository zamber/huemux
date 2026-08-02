// Behaviour for .hm-dropdown (see shared/dropdown.css for the markup contract
// and the design rationale).
//
// A bare <details> stays open until its own summary is clicked again, which is
// not what a menu does: tapping the page elsewhere, pressing Escape, or
// choosing an item should all close it. None of that is worth a component
// framework, but it does need to exist, and it needs to exist in one place —
// this is the third time the pattern has come up in this UI.
//
// Delegated from the document, so dropdowns rendered after this script runs
// (every one of them, in practice — the room list is rebuilt whenever the
// bridge reports new rooms) are covered without re-registering anything.
(function () {
  function openDropdowns() {
    return document.querySelectorAll('details.hm-dropdown[open]');
  }

  // Pointerdown rather than click: it fires before focus moves and before any
  // click handler inside the page runs, so the menu is already closed by the
  // time the thing underneath reacts. With click, tapping a light card behind
  // an open menu left the menu open over the card it had just toggled.
  document.addEventListener('pointerdown', (e) => {
    for (const d of openDropdowns()) {
      if (!d.contains(e.target)) d.open = false;
    }
  });

  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    for (const d of openDropdowns()) {
      d.open = false;
      // Return focus to the control that opened it, or a keyboard user is
      // dropped at the top of the document with no idea where they were.
      const s = d.querySelector('summary');
      if (s) s.focus();
    }
  });

  // Choosing an item closes the menu. The item's own click handler still runs
  // — this only decides the disclosure state, never what the item does.
  document.addEventListener('click', (e) => {
    const item = e.target.closest('.hm-dropdown-item');
    if (!item) return;
    const d = item.closest('details.hm-dropdown');
    if (d) d.open = false;
  });
})();
