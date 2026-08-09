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

// The indicator is a pill containing a progress ring and a label, so the fake has
// to model real nodes: a shared stub returned three times would let a bug that
// writes to the wrong node pass unnoticed.
function fakeNode() {
  const node = {
    className: "",
    textContent: "",
    children: [],
    attributes: {},
    classes: new Set(),
    removed: false,
    properties: {},
    style: {
      setProperty(name, value) { node.properties[name] = value; },
      removeProperty(name) { delete node.properties[name]; },
    },
    classList: {
      toggle(name, on) {
        if (on) node.classes.add(name);
        else node.classes.delete(name);
        return on;
      },
      contains: (name) => node.classes.has(name),
    },
    setAttribute(name, value) { node.attributes[name] = value; },
    append(...nodes) { node.children.push(...nodes); },
    appendChild(child) { node.children.push(child); return child; },
    remove() { node.removed = true; },
  };
  return node;
}

function fakeView({ matches = true, reducedMotion = false, maxTouchPoints = 1, vibrate } = {}) {
  const created = [];
  const navigator = { maxTouchPoints };
  // Only defined when a test asks for it, so the absent-hardware path is exercised
  // by every other test rather than being a special case nobody runs.
  if (vibrate) navigator.vibrate = vibrate;
  const view = {
    created,
    navigator,
    matchMedia: (query) => ({
      matches: query.includes("prefers-reduced-motion") ? reducedMotion : matches,
    }),
    document: {
      createElement: () => {
        const node = fakeNode();
        created.push(node);
        return node;
      },
    },
  };
  // The pill is created first, then the ring, then the label.
  Object.defineProperty(view, "indicator", { get: () => created[0] });
  Object.defineProperty(view, "ring", { get: () => created[1] });
  Object.defineProperty(view, "label", { get: () => created[2] });
  return view;
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
  // The transform carries the horizontal centring as well as the vertical follow,
  // because the three labels have three widths and a fixed margin made the pill
  // slide sideways whenever the text changed.
  assert.equal(view.indicator.style.transform, "translate(-50%, 40px)");
});

test("the label reports pull, release, and refreshing in turn", async () => {
  const target = fakeElement();
  const view = fakeView();
  // Supplied by the caller from the active locale. The module's own defaults are a
  // fallback, not the shipped copy, so the test states its own strings.
  const labels = { pull: "pull", release: "release", refreshing: "refreshing" };
  installPullToRefresh({ target, view, labels, onRefresh: () => new Promise(() => {}) });
  assert.equal(view.label.textContent, "pull");
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 50 }], cancelable: true, preventDefault() {} });
  assert.equal(view.label.textContent, "pull");
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(view.label.textContent, "release");
  target.emit("touchend", { changedTouches: [{ clientX: 100, clientY: 220 }] });
  assert.equal(view.label.textContent, "refreshing");
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
  // The slide is dropped but the centring is not, or the pill would sit off to one
  // side for exactly the users who asked for less movement.
  assert.equal(view.indicator.style.transform, "translate(-50%, 0px)");
  assert.equal(view.label.textContent, "放開即重新載入");
});

test("the indicator is inserted into the target and marked decorative", () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => {} });
  assert.equal(target.children.length, 1);
  assert.equal(view.indicator.className, "pull-to-refresh-indicator");
  assert.equal(view.indicator.attributes["aria-hidden"], "true");
  // The ring and label live inside the pill, not alongside it in the top bar.
  assert.deepEqual(view.indicator.children, [view.ring, view.label]);
  assert.equal(view.ring.className, "pull-to-refresh-ring");
  assert.equal(view.label.className, "pull-to-refresh-label");
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

// The gesture used to report itself with three words and nothing else, so mid-pull
// there was no sign of how much further to go. Progress is published as a 0..1 ratio
// and drawn as an arc.
test("the pull publishes its progress as it travels", () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => {} });
  assert.equal(view.indicator.properties["--pull-progress"], "0");
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  // 87.5px of travel is 35px damped, exactly half of the 70px threshold.
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 87.5 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.properties["--pull-progress"], "0.5");
  assert.equal(view.ring.properties["--pull-progress"], "0.5");
});

test("progress is clamped to one so the arc cannot overfill", () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => {} });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 900 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.properties["--pull-progress"], "1");
  assert.equal(view.indicator.classes.has("is-armed"), true);
});

test("the armed and refreshing states are legible on screen, not only by feel", async () => {
  const target = fakeElement();
  const view = fakeView();
  installPullToRefresh({ target, view, onRefresh: () => new Promise(() => {}) });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 40 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.classes.has("is-armed"), false, "not yet past the threshold");
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(view.indicator.classes.has("is-armed"), true);
  target.emit("touchend", { changedTouches: [{ clientX: 100, clientY: 220 }] });
  // Armed and refreshing are mutually exclusive: the ring stops reporting a
  // fraction once the reload is in flight, because the fraction is unknowable.
  assert.equal(view.indicator.classes.has("is-refreshing"), true);
  assert.equal(view.indicator.classes.has("is-armed"), false);
});

test("crossing the threshold ticks once, not on every touchmove", () => {
  const target = fakeElement();
  const pulses = [];
  const view = fakeView({ vibrate: (pattern) => { pulses.push(pattern); return true; } });
  installPullToRefresh({ target, view, onRefresh: () => {} });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 100 }], cancelable: true, preventDefault() {} });
  assert.deepEqual(pulses, [], "below the threshold there is nothing to confirm");
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(pulses.length, 1, "the crossing is the event, so it fires once");
  // Holding past the line delivers many more touchmoves; each one must not buzz.
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 240 }], cancelable: true, preventDefault() {} });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 260 }], cancelable: true, preventDefault() {} });
  assert.equal(pulses.length, 1, "a held pull must not buzz continuously");
});

test("dragging back below the line re-arms the tick", () => {
  const target = fakeElement();
  const pulses = [];
  const view = fakeView({ vibrate: (pattern) => { pulses.push(pattern); return true; } });
  installPullToRefresh({ target, view, onRefresh: () => {} });
  target.emit("touchstart", { touches: [{ clientX: 100, clientY: 0 }] });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 40 }], cancelable: true, preventDefault() {} });
  target.emit("touchmove", { touches: [{ clientX: 100, clientY: 220 }], cancelable: true, preventDefault() {} });
  assert.equal(pulses.length, 2, "changing your mind and pulling again is confirmed again");
});

test("haptics can be switched off without affecting the gesture", async () => {
  const target = fakeElement();
  const pulses = [];
  let refreshes = 0;
  const view = fakeView({ vibrate: (pattern) => { pulses.push(pattern); return true; } });
  installPullToRefresh({ target, view, haptics: false, onRefresh: () => { refreshes += 1; } });
  drag(target, { steps: [{ y: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(pulses, []);
  assert.equal(refreshes, 1, "the preference silences the buzz, not the refresh");
});

test("hardware without a vibration motor still refreshes", async () => {
  const target = fakeElement();
  let refreshes = 0;
  // No vibrate on the fake navigator at all, which is the desktop and iOS case.
  installPullToRefresh({ target, view: fakeView(), onRefresh: () => { refreshes += 1; } });
  drag(target, { steps: [{ y: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 1);
});

test("a vibrate that throws cannot break the gesture", async () => {
  const target = fakeElement();
  let refreshes = 0;
  // Some engines reject vibration outside a user-activation window by throwing
  // rather than returning false. A courtesy must not take the refresh down.
  const view = fakeView({ vibrate: () => { throw new Error("blocked by engine"); } });
  installPullToRefresh({ target, view, onRefresh: () => { refreshes += 1; } });
  drag(target, { steps: [{ y: 220 }] });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(refreshes, 1);
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
