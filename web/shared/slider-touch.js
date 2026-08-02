// Makes range sliders ignore a touch that turns out to be a scroll.
//
// The problem: a slider spans the full width of a card, so a lot of vertical
// swipes begin on one. `touch-action: pan-y` (shared/theme.css) already hands
// the vertical gesture back to the browser so the page scrolls — but not
// before the native control has processed the touch-down and jumped its thumb
// to wherever the finger landed. The page scrolls *and* a light changes
// brightness. The scroll was fixed; this is the residue of it.
//
// The rule implemented here:
//
//   - A touch on a slider changes nothing until the finger has travelled more
//     than DRIFT_PX horizontally. Below that it is a tap or the start of a
//     scroll, and neither should move a value. This is also what absorbs the
//     drift of a finger that is nominally holding still.
//   - Once it is unambiguously a horizontal drag, tracking goes live again, so
//     a deliberate adjustment still follows the finger rather than jumping
//     into place only at the end. By then there is nothing to disambiguate.
//   - A gesture that reads as vertical, or that the browser claims for
//     scrolling (pointercancel), puts the value back where it started.
//
// Mouse and pen are untouched: click-to-set on the track is correct there and
// there is no competing scroll gesture to protect.
//
// Everything is registered in the capture phase on the document, which matters
// twice: it catches sliders that did not exist when this ran (every light card
// is re-rendered from a template), and a suppressed `input` event can be
// stopped before the page's own listeners see it, so no bridge message, no
// optimistic re-render, no server round trip for a value that is about to be
// put back.
(function () {
  // Roughly 2mm on a typical phone. Small enough that a deliberate nudge still
  // registers, large enough to sit outside the jitter of a stationary finger.
  const DRIFT_PX = 8;

  // Element being touched -> gesture state. A single entry in practice; a map
  // because nothing guarantees that.
  const pending = new Map();

  function isRange(el) {
    return el && el.tagName === 'INPUT' && el.type === 'range';
  }

  document.addEventListener('pointerdown', (e) => {
    if (e.pointerType !== 'touch' || !isRange(e.target)) return;
    pending.set(e.target, {
      pointerId: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      // The value as it was before the control reacted to this touch. Read in
      // the capture phase, so it is still the pre-jump value.
      startValue: e.target.value,
      engaged: false,
      aborted: false,
    });
  }, true);

  document.addEventListener('pointermove', (e) => {
    const st = pending.get(e.target);
    if (!st || st.engaged || st.aborted || e.pointerId !== st.pointerId) return;
    const dx = Math.abs(e.clientX - st.x);
    const dy = Math.abs(e.clientY - st.y);
    if (dx > DRIFT_PX && dx > dy) {
      st.engaged = true;
    } else if (dy > DRIFT_PX) {
      // Reads as a scroll. Decide now rather than waiting for the browser:
      // pointercancel is not guaranteed to arrive on every engine.
      //
      // Marked, not forgotten. Dropping the gesture here was the first
      // attempt, and it undid the whole point: with the state gone nothing
      // suppressed the input events that kept arriving, so the native control
      // carried on following the finger up the screen and the value moved
      // anyway — exactly the behaviour this file exists to prevent, just
      // delayed by one event. The gesture stays suppressed until it ends.
      st.aborted = true;
      if (e.target.value !== st.startValue) e.target.value = st.startValue;
    }
  }, true);

  // The suppression itself. Until a gesture is engaged, every input event it
  // produces — starting with the jump-to-finger on touch-down — is undone and
  // withheld from the rest of the page.
  document.addEventListener('input', (e) => {
    const st = pending.get(e.target);
    if (!st || st.engaged) return;
    if (e.target.value !== st.startValue) e.target.value = st.startValue;
    e.stopImmediatePropagation();
  }, true);

  function finish(el) {
    const st = pending.get(el);
    if (!st) return;
    if (!st.engaged && el.value !== st.startValue) {
      el.value = st.startValue;
    }
    pending.delete(el);
  }

  document.addEventListener('pointerup', (e) => finish(e.target), true);
  // Fired when the browser takes the gesture over for scrolling — the exact
  // case this whole file exists for.
  document.addEventListener('pointercancel', (e) => finish(e.target), true);
})();
