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
// A short tick at the moment the gesture arms. Long enough to register as a
// deliberate confirmation, short enough not to feel like an error buzz.
const ARM_VIBRATION_MS = 12;

// The gesture used to report itself with three words and nothing else, so during
// the drag there was no sign of how much further the pull had to go. Progress is
// published as a 0..1 ratio and drawn as a ring, which is what makes the halfway
// point visible instead of implied.
function pullProgress(distance, threshold) {
  if (!(threshold > 0)) return 0;
  const ratio = distance / threshold;
  return ratio < 0 ? 0 : ratio > 1 ? 1 : ratio;
}

// Vibration is a courtesy, not a dependency: it is missing on desktop, refused
// without a user gesture in some engines, and can throw rather than return false.
// A failure here must never interrupt a refresh, so it is swallowed deliberately.
function vibrate(view, pattern) {
  const navigator = view?.navigator;
  if (typeof navigator?.vibrate !== "function") return false;
  try {
    return navigator.vibrate(pattern) === true;
  } catch {
    return false;
  }
}

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
  haptics = true,
} = {}) {
  if (!target?.addEventListener || typeof onRefresh !== "function") return () => {};

  // The defaults are a last resort for a caller that passes no labels. They are
  // deliberately Traditional here to match the shell's default script; the real
  // strings arrive translated from the caller.
  const text = {
    pull: labels.pull ?? "下拉重新載入",
    release: labels.release ?? "放開即重新載入",
    refreshing: labels.refreshing ?? "重新載入中…",
  };

  const doc = view.document || globalThis.document;
  const indicator = doc?.createElement?.("div");
  let ring = null;
  let caption = null;
  if (indicator) {
    indicator.className = "pull-to-refresh-indicator";
    indicator.setAttribute("aria-hidden", "true");
    ring = doc.createElement("span");
    ring.className = "pull-to-refresh-ring";
    caption = doc.createElement("span");
    caption.className = "pull-to-refresh-label";
    caption.textContent = text.pull;
    indicator.append(ring, caption);
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

  function paint(travel, label, progress = 0) {
    if (!indicator) return;
    if (caption) caption.textContent = label;
    // Centring is done here rather than with a fixed negative margin, because the
    // three labels have three different widths and a fixed margin made the pill
    // jump sideways as the text changed.
    const slide = reducedMotion ? 0 : travel;
    indicator.style.transform = `translate(-50%, ${slide}px)`;
    indicator.style.opacity = travel > 0 || refreshing ? "1" : "0";
    // Exposed as a custom property so the ring is drawn in CSS; the module stays
    // responsible for the gesture and not for how the arc looks.
    indicator.style.setProperty("--pull-progress", String(progress));
    indicator.classList.toggle("is-armed", armed && !refreshing);
    indicator.classList.toggle("is-refreshing", refreshing);
    if (ring) ring.style.setProperty("--pull-progress", String(progress));
  }

  function reset() {
    startPoint = null;
    distance = 0;
    armed = false;
    paint(0, text.pull, 0);
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
    const wasArmed = armed;
    armed = distance >= threshold;
    // The tick fires on the crossing, not on the armed state, so holding past the
    // threshold buzzes once instead of on every touchmove the browser delivers.
    // Dragging back above the line re-arms it, which is the confirmation people
    // expect when they change their mind and pull again.
    if (armed && !wasArmed && haptics) vibrate(view, ARM_VIBRATION_MS);
    // Only claimed once this is clearly a pull, so a tap on a top-bar button is
    // never swallowed by the gesture.
    if (distance > 4 && event.cancelable) event.preventDefault();
    paint(
      Math.min(distance, MAX_INDICATOR_TRAVEL_PX),
      armed ? text.release : text.pull,
      pullProgress(distance, threshold),
    );
  }

  function handleEnd() {
    if (refreshing) return;
    if (!armed) {
      reset();
      return;
    }
    refreshing = true;
    startPoint = null;
    paint(threshold, text.refreshing, 1);
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
