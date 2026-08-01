export const $ = (id) => document.getElementById(id);

export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[ch]));
}

export function escapeAttr(value) {
  return escapeHtml(value).replace(/'/g, "&#39;");
}

export function setButtonBusy(button, busy, busyLabel) {
  if (!button) return;
  if (busy) {
    if (!button.dataset.originalLabel) button.dataset.originalLabel = button.textContent;
    button.textContent = busyLabel;
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
  } else {
    if (button.dataset.originalLabel) button.textContent = button.dataset.originalLabel;
    delete button.dataset.originalLabel;
    button.disabled = false;
    button.removeAttribute("aria-busy");
  }
}

// Writing innerHTML tears the subtree out and rebuilds it even when the markup
// is byte-identical, which reads as a flicker. Several panels here render more
// than once per conversation switch -- enterAgent paints from the agent already
// in the list, then applyAgentLiveSnapshot paints the same thing again from the
// live snapshot -- so the repeat renders were pure flash.
//
// The comparison is against what was last written, not against
// element.innerHTML: the browser reserializes markup, so reading it back would
// never match and the guard would never hold.
//
// The cache is keyed on the element too. A replaced container is empty and must
// be written even when the markup is unchanged; a stale hit there would leave
// the panel permanently blank, which is far worse than the flicker.
const lastWrittenMarkup = new WeakMap();

export function setHTMLIfChanged(element, markup) {
  if (!element) return false;
  if (lastWrittenMarkup.get(element) === markup) return false;
  lastWrittenMarkup.set(element, markup);
  element.innerHTML = markup;
  return true;
}

// Assigning textContent replaces the text node even when the string is
// identical, which is a real mutation and a real repaint. The measured cost on
// one project switch: the header tool badges rewrote 14 times, the composer
// status 7, the reasoning pill 6, the background-task labels 5 each -- all with
// unchanged text, all visible as a flicker across the toolbar and composer.
//
// Only for elements whose entire content is that one string. Do not use it to
// clear a container: an element holding child nodes with no text reads back as
// "", so writing "" would look unchanged and the children would survive.
export function setTextIfChanged(element, text) {
  if (!element) return false;
  const value = String(text);
  if (element.textContent === value) return false;
  element.textContent = value;
  return true;
}

// Collapses a burst of calls into at most two runs per frame: the first call
// runs synchronously, and everything else in the same frame collapses into one
// trailing run.
//
// A conversation switch calls the header renders eleven times, because the
// stream status, the navigation selection, enterAgent and the live snapshot all
// legitimately ask for a repaint while the switch is in flight. Nothing is
// waiting on data -- the same content is simply drawn over and over.
//
// The leading run is what makes this safe to retrofit: a pure trailing
// scheduler would make rendering asynchronous, and any caller that renders and
// then measures the DOM in the same tick would silently start reading stale
// layout. Here the first render still happens before the call returns, and the
// trailing run only catches up on whatever changed afterwards -- which, with
// the setTextIfChanged and setHTMLIfChanged guards, usually writes nothing.
//
// Only for no-argument renders that read their inputs from shared state: the
// trailing run takes no arguments, because by then the arguments of whichever
// call scheduled it are no longer the current truth.
const scheduleFrame = typeof requestAnimationFrame === "function"
  ? requestAnimationFrame
  : (callback) => setTimeout(callback, 0);

export function coalescePerFrame(render) {
  let ranThisFrame = false;
  let pending = false;
  return function coalesced() {
    if (ranThisFrame) {
      pending = true;
      return undefined;
    }
    ranThisFrame = true;
    scheduleFrame(() => {
      ranThisFrame = false;
      if (!pending) return;
      pending = false;
      render();
    });
    return render();
  };
}
