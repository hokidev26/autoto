const TYPING_SELECTOR = "input, textarea, select, [contenteditable='true']";
const INTERACTIVE_SELECTOR = "a, button, input, select, textarea, label";
const MARKED_SCROLLER_SELECTOR = ".settings-h-scroll";

function overflowX(node) {
  if (!node) return "";
  if (typeof node.overflowX === "string" && node.overflowX) return String(node.overflowX).toLowerCase();
  const view = node.ownerDocument?.defaultView;
  try {
    return String(view?.getComputedStyle?.(node)?.overflowX || "").toLowerCase();
  } catch {
    return "";
  }
}

function maxScrollLeft(node) {
  return Math.max(0, (Number(node?.scrollWidth) || 0) - (Number(node?.clientWidth) || 0));
}

export function canScrollHorizontally(node) {
  if (!node) return false;
  if (maxScrollLeft(node) <= 1) return false;
  const ox = overflowX(node);
  return ox === "auto" || ox === "scroll";
}

export function nearestHorizontalScroller(node, root = null) {
  let current = node?.nodeType === 1 ? node : node?.parentElement;
  while (current && current !== root) {
    if (canScrollHorizontally(current)) return current;
    current = current.parentElement || null;
  }
  return null;
}

function scrollerForTarget(target, root) {
  const marked = target?.closest?.(MARKED_SCROLLER_SELECTOR);
  if (marked && (root ? root.contains?.(marked) !== false : true) && canScrollHorizontally(marked)) return marked;
  return nearestHorizontalScroller(target, root);
}

export function scrollHorizontalRegionFromKeyboard(event, { root } = {}) {
  if (!event || event.defaultPrevented) return false;
  if (event.altKey || event.ctrlKey || event.metaKey) return false;
  if (event.key !== "ArrowLeft" && event.key !== "ArrowRight") return false;
  const target = event.target;
  if (target?.closest?.(TYPING_SELECTOR)) return false;
  const wrap = scrollerForTarget(target, root);
  if (!wrap) return false;
  if (target === wrap) return false;
  const maxLeft = maxScrollLeft(wrap);
  const step = Math.max(48, Math.round((Number(wrap.clientWidth) || 0) * 0.35));
  const current = Number(wrap.scrollLeft) || 0;
  const next = Math.min(maxLeft, Math.max(0, current + (event.key === "ArrowLeft" ? -step : step)));
  if (next === current) return false;
  wrap.scrollLeft = next;
  event.preventDefault?.();
  return true;
}

export function focusHorizontalRegionFromPointer(event, { root } = {}) {
  const target = event?.target;
  const interactive = target?.closest?.(INTERACTIVE_SELECTOR);
  if (interactive && interactive !== target?.closest?.(MARKED_SCROLLER_SELECTOR)) return false;
  const wrap = scrollerForTarget(target, root);
  if (!wrap || typeof wrap.focus !== "function") return false;
  if (interactive && interactive !== wrap) return false;
  if (!wrap.hasAttribute?.("tabindex")) wrap.setAttribute?.("tabindex", "0");
  wrap.focus({ preventScroll: true });
  return true;
}
