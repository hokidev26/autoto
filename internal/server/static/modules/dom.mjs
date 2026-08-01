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
