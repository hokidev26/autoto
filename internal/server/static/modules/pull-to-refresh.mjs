// Pull down on the top bar to reload.
//
// An installed PWA has no browser chrome, so the reload button that a tab would
// provide is simply absent. This restores that one affordance with the gesture
// people already expect from native apps.
//
// The gesture is bound to the top bar rather than the message list on purpose:
// the list's top edge already loads older history when it is scrolled into view,
// and two behaviours competing for the same downward drag would make both feel
// unreliable.

const PULL_THRESHOLD_PX = 70;
// Finger travel is damped so the indicator trails the thumb instead of tracking
// it 1:1, which is what makes the gesture feel resisted rather than loose.
const PULL_DAMPING = 0.4;
// Past this the drag is a horizontal swipe, not a pull, and is abandoned.
const HORIZONTAL_TOLERANCE_PX = 24;
const MAX_INDICATOR_TRAVEL_PX = 96;

function touchPoint(event) {
  const touch = event?.touches?.[0] || event?.changedTouches?.[0];
  return touch ? { x: touch.clientX, y: touch.clientY } : null;
}

export function isPullToRefreshSupported({
  view = globalThis,
  maxWidth = 767,
} = {}) {
  if (typeof view?.matchMedia !== "function") return false;
  // Touch capability alone is not enough: a touchscreen laptop still has a
  // reload button, so this stays a small-viewport affordance.
  const touchCapable = Number(view.navigator?.maxTouchPoints || 0) > 0 || "ontouchstart" in view;
  if (!touchCapable) return false;
  return view.matchMedia(`(max-width: ${maxWidth}px)`).matches;
}

export function installPullToRefresh({
  target,
  onRefresh,
  view = globalThis,
  threshold = PULL_THRESHOLD_PX,
  labels = {},
} = {}) {
  if (!target?.addEventListener || typeof onRefresh !== "function") return () => {};

  const text = {
    pull: labels.pull ?? "下拉刷新",
    release: labels.release ?? "松手刷新",
    refreshing: labels.refreshing ?? "正在刷新…",
  };

  const doc = view.document || globalThis.document;
  const indicator = doc?.createElement?.("div");
  if (indicator) {
    indicator.className = "pull-to-refresh-indicator";
    indicator.setAttribute("aria-hidden", "true");
    indicator.textContent = text.pull;
    target.appendChild(indicator);
  }

  // Honouring reduced motion means dropping the elastic follow, not the gesture:
  // the indicator still reports state, it just does not slide.
  const reducedMotion = typeof view.matchMedia === "function"
    ? view.matchMedia("(prefers-reduced-motion: reduce)").matches
    : false;

  let startPoint = null;
  let distance = 0;
  let armed = false;
  let refreshing = false;

  function paint(travel, label) {
    if (!indicator) return;
    indicator.textContent = label;
    indicator.style.transform = reducedMotion ? "" : `translateY(${travel}px)`;
    indicator.style.opacity = travel > 0 || refreshing ? "1" : "0";
  }

  function reset() {
    startPoint = null;
    distance = 0;
    armed = false;
    paint(0, text.pull);
  }

  function handleStart(event) {
    if (refreshing) return;
    startPoint = touchPoint(event);
    distance = 0;
    armed = false;
  }

  function handleMove(event) {
    if (!startPoint || refreshing) return;
    const point = touchPoint(event);
    if (!point) return;
    if (Math.abs(point.x - startPoint.x) > HORIZONTAL_TOLERANCE_PX) {
      reset();
      return;
    }
    const raw = point.y - startPoint.y;
    if (raw <= 0) {
      distance = 0;
      armed = false;
      paint(0, text.pull);
      return;
    }
    distance = raw * PULL_DAMPING;
    armed = distance >= threshold;
    // Only claimed once this is clearly a pull, so a tap on a top-bar button is
    // never swallowed by the gesture.
    if (distance > 4 && event.cancelable) event.preventDefault();
    paint(Math.min(distance, MAX_INDICATOR_TRAVEL_PX), armed ? text.release : text.pull);
  }

  function handleEnd() {
    if (refreshing) return;
    if (!armed) {
      reset();
      return;
    }
    refreshing = true;
    startPoint = null;
    paint(threshold, text.refreshing);
    // A reload replaces the page, so there is nothing to restore afterwards. If
    // the handler resolves without navigating, the indicator is cleared so the
    // gesture stays usable.
    Promise.resolve()
      .then(() => onRefresh())
      .catch((error) => console.error("[pull-to-refresh] refresh failed", error))
      .finally(() => {
        refreshing = false;
        reset();
      });
  }

  target.addEventListener("touchstart", handleStart, { passive: true });
  target.addEventListener("touchmove", handleMove, { passive: false });
  target.addEventListener("touchend", handleEnd, { passive: true });
  target.addEventListener("touchcancel", reset, { passive: true });
  paint(0, text.pull);

  return () => {
    target.removeEventListener("touchstart", handleStart);
    target.removeEventListener("touchmove", handleMove);
    target.removeEventListener("touchend", handleEnd);
    target.removeEventListener("touchcancel", reset);
    indicator?.remove?.();
  };
}
