// Makes a touch on a range slider do what it does on every other engine: tap
// the track and the value goes there, drag it and the value follows, swipe it
// and nothing happens at all.
//
// Two problems meet here, and the second is why the first is handled this way.
//
// The one this file exists for: a slider spans the full width of a card, so a
// lot of vertical swipes begin on one. `touch-action: pan-y` (shared/theme.css)
// already hands the vertical gesture back to the browser so the page scrolls —
// but not before the native control has processed the touch-down and jumped
// its thumb to wherever the finger landed. The page scrolls *and* a light
// changes brightness, some 300ms later, from lights.js:scheduleBrightness. The
// scroll was fixed; this is the residue of it.
//
// The one that fix introduced: on Chromium the control jumps the thumb at
// touch-down and fires `input` immediately, and that first `input` is the only
// one a tap produces. Withholding it therefore withheld the tap as well — on
// Chrome and in the Android app, tapping a slider's track did nothing. Firefox
// reports the change elsewhere, which is why it kept working there.
//
// So the rules are:
//
//   - Nothing a touch does reaches the page until the finger has travelled.
//     Under DRIFT_PX in either axis the gesture is a tap, and the control's own
//     jump to the finger is replayed to the page as a single `input` event on
//     release — the same event, with the same value, that the other engines
//     deliver for the same tap. No value that the control did not already
//     choose is ever invented here.
//   - Past DRIFT_PX horizontally, tracking goes live and the page sees the
//     values as they arrive, so a deliberate adjustment still follows the
//     finger rather than jumping into place only at the end. By then there is
//     nothing left to disambiguate.
//   - Past DRIFT_PX vertically the gesture is a scroll: the value goes back and
//     the page is never told, so nothing is sent.
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
      // The last value the control chose for itself, kept in case the gesture
      // turns out to be a tap. Null until it has chosen one.
      tapped: null,
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
      st.tapped = null;
      if (e.target.value !== st.startValue) e.target.value = st.startValue;
    }
  }, true);

  // The suppression itself. Until a gesture is engaged, every input event it
  // produces — starting with the jump-to-finger on touch-down — is undone and
  // withheld from the rest of the page, and the value it carried is remembered
  // in case this turns out to be a tap rather than a scroll.
  document.addEventListener('input', (e) => {
    const st = pending.get(e.target);
    if (!st || st.engaged) return;
    if (!st.aborted) st.tapped = e.target.value;
    if (e.target.value !== st.startValue) e.target.value = st.startValue;
    e.stopImmediatePropagation();
  }, true);

  // `commit` is false when the browser took the gesture for scrolling
  // (pointercancel) — the exact case this whole file exists for.
  function finish(el, commit) {
    const st = pending.get(el);
    if (!st) return;
    pending.delete(el);
    if (!commit || st.engaged || st.aborted) return;
    // A tap, and the control has already decided where its thumb belongs: it
    // put it under the finger. That value was withheld from the page along
    // with everything else, so it is delivered now, once, exactly as the other
    // engines deliver it. A tap that lands on the thumb where it already is
    // changes nothing, and is not reported as if it had.
    //
    // `input` only, and not `change`: the engines that fire `change` for a
    // slider fire it after this point and it is not suppressed, so it arrives
    // on its own, with the value just committed here.
    if (st.tapped === null || st.tapped === st.startValue) return;
    el.value = st.tapped;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }

  document.addEventListener('pointerup', (e) => finish(e.target, true), true);
  document.addEventListener('pointercancel', (e) => finish(e.target, false), true);
})();
