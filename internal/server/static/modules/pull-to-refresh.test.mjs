import test from "node:test";
import assert from "node:assert/strict";

import { installPullToRefresh, isPullToRefreshSupported } from "./pull-to-refresh.mjs";

function fakeElement() {
  const listeners = new Map();
  const element = {
    listeners,
    children: [],
    addEventListener: (type, handler) => listeners.set(type, handler),
    removeEventListener: (type) => listeners.delete(type),
    appendChild: (child) => { element.children.push(child); return child; },
    emit: (type, event) => listeners.get(type)?.(event),
  };
  return element;
}

function fakeIndicator() {
  return {
    className: "",
    textContent: "",
    style: {},
    attributes: {},
    removed: false,
    setAttribute(name, value) { this.attributes[name] = value; },
    remove() { this.removed = true; },
  };
}

function fakeView({ matches = true, reducedMotion = false, maxTouchPoints = 1 } = {}) {
  const indicator = fakeIndicator();
  return {
    indicator,
    navigator: { maxTouchPoints },
    matchMedia: (query) => ({
      matches: query.includes("prefers-reduced-motion") ? reducedMotion : matches,
    }),
    document: { createElement: () => indicator },
  };
}

// A drag is a start, some number of moves, then an end. `cancelable` mirrors the
// browser so preventDefault assertions are meaningful.
function drag(target, { from = { x: 100, y: 10 }, steps = [], end = true } = {}) {
  const prevented = [];
  target.emit("touchstart", { touches: [{ clientX: from.x, clientY: from.y }] });
  for (const point of steps) {
    target.emit("touchmove", {
      touches: [{ clientX: point.x ?? from.x, clientY: point.y }],
      cancelable: true,
      preventDefault: () => prevented.push(point.y),
    });
  }
  if (end) target.emit("touchend", { changedTouches: [{ clientX: from.x, clientY: steps.at(-1)?.y ?? from.y }] });
  return { prevented };
}

test("the gesture is offered only on touch-capable small viewports", () => {
  assert.equal(isPullToRefreshSupported({ view: fakeView() }), true);
  // A touchscreen laptop still has a reload button of its own.
  assert.equal(isPullToRefreshSupported({ view: fakeView({ matches: false }) }), false);
  assert.equal(isPullToRefreshSupported({ view: fakeView({ maxTouchPoints: 0 }) }), false);
  assert.equal(isPullToRefreshSupported({ view: {} }), false);
});

test("pulling past the threshold refreshes once", async () => {
  const target = fakeElement();
  let refreshes = 0;
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => { refreshes += 1; } });
  drag(target, { steps: [{ y: 60 }, { y: 140 }, { y: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 1);
});

test("a short pull springs back without refreshing", async () => {
  const target = fakeElement();
  let refreshes = 0;
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => { refreshes += 1; } });
  // 100px of travel is 40px after damping, below the 70px threshold.
  drag(target, { steps: [{ y: 60 }, { y: 110 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 0);
  assert.equal(view.indicator.style.opacity, "0");
});

test("travel is damped rather than tracking the finger one to one", () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => {} });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 100 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.style.transform, "translateY(40px)");
});

test("the label reports pull, release, and refreshing in turn", async () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => new Promise(() => {}) });
  assert.equal(view.indicator.textContent, "下拉刷新");
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 50 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.textContent, "下拉刷新");
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.textContent, "松手刷新");
  target.emit("touchend", { changedTouches: [{ clientX: 100, clientY: 220 }] });
  assert.equal(view.indicator.textContent, "正在刷新…");
});

// A tap on a top-bar button must not be swallowed, so the gesture only claims
// the event once the drag is unambiguous.
test("a negligible drag leaves the event to the top bar's own controls", () => {
  const target = fakeElement();
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => {} });
  const { prevented } = drag(target, { steps: [{ y: 12 }], end: false });
  assert.equal(prevented.length, 0);
});

test("a clear pull claims the event so the page does not scroll under it", () => {
  const target = fakeElement();
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => {} });
  const { prevented } = drag(target, { steps: [{ y: 120 }], end: false });
  assert.deepEqual(prevented, [120]);
});

test("a sideways swipe is abandoned instead of counting as a pull", async () => {
  const target = fakeElement();
  let refreshes = 0;
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => { refreshes += 1; } });
  drag(target, { from: { x: 100, y: 10 }, steps: [{ x: 220, y: 230 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 0);
});

test("an upward drag never arms the gesture", async () => {
  const target = fakeElement();
  let refreshes = 0;
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => { refreshes += 1; } });
  drag(target, { from: { x: 100, y: 200 }, steps: [{ y: 40 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 0);
});

test("a refresh in flight ignores further pulls", async () => {
  const target = fakeElement();
  let refreshes = 0;
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => { refreshes += 1; return new Promise(() => {}); } });
  drag(target, { steps: [{ y: 220 }] });
  drag(target, { steps: [{ y: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 1);
});

test("a failing refresh is reported and the gesture stays usable", async () => {
  const target = fakeElement();
  const previous = console.error;
  const logged = [];
  console.error = (...args) => logged.push(args);
  try {
    let attempts = 0;
    installPullToRefresh({
      target,
      view: fakeView(),
      onRefresh: () => {
        attempts += 1;
        if (attempts === 1) throw new Error("reload blocked");
      },
    });
    drag(target, { steps: [{ y: 220 }] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(logged.length, 1);
    drag(target, { steps: [{ y: 220 }] });
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(attempts, 2, "expected the gesture to still work after a failure");
  } finally {
    console.error = previous;
  }
});

test("touchcancel resets the gesture", async () => {
  const target = fakeElement();
  let refreshes = 0;
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => { refreshes += 1; } });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  target.emit("touchcancel", {});
  target.emit("touchend", { changedTouches: [{ clientX: 100, clientY: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 0);
  assert.equal(view.indicator.style.opacity, "0");
});

test("reduced motion keeps the state text but drops the slide", () => {
  const target = fakeElement();
  const view = fakeView({ reducedMotion: true });
  installPullToRefresh({ target, view, onRefresh: () => {} });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.style.transform, "");
  assert.equal(view.indicator.textContent, "松手刷新");
});

test("the indicator is inserted into the target and marked decorative", () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => {} });
  assert.equal(target.children.length, 1);
  assert.equal(view.indicator.className, "pull-to-refresh-indicator");
  assert.equal(view.indicator.attributes["aria-hidden"], "true");
});

test("the disposer unbinds the gesture and removes the indicator", () => {
  const target = fakeElement();
  const view = fakeView();
  const dispose = installPullToRefresh({ target, view, onRefresh: () => {} });
  assert.equal(target.listeners.size, 4);
  dispose();
  assert.equal(target.listeners.size, 0);
  assert.equal(view.indicator.removed, true);
});

test("a missing target or handler installs nothing", () => {
  assert.equal(typeof installPullToRefresh({}), "function");
  assert.equal(typeof installPullToRefresh({ target: fakeElement() }), "function");
});

test("the threshold is configurable", async () => {
  const target = fakeElement();
  let refreshes = 0;
  installPullToRefresh({ target, view: fakeView(), threshold: 10, onRefresh: () => { refreshes += 1; } });
  // 40px of travel is 16px damped: past a 10px threshold, short of the default.
  drag(target, { steps: [{ y: 40 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 1);
});
