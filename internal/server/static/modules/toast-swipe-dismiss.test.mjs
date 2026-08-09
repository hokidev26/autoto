import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The swipe handler is a closure inside app-main.mjs, which pulls in the whole app on
// import. Rather than stand that up, the function is read out of the source and
// evaluated on its own against fake nodes. That keeps this a test of the gesture logic
// -- the axis decision and the threshold -- instead of a test of module wiring.
const source = readFileSync(new URL("app-main.mjs", import.meta.url), "utf8");

function loadSwipeBinder() {
  const thresholdMatch = /const toastSwipeDismissPx = (\d+);/.exec(source);
  assert.ok(thresholdMatch, "the dismiss threshold must stay a named constant");
  const startAt = source.indexOf("function bindToastSwipeDismiss(node, close) {");
  assert.notEqual(startAt, -1, "bindToastSwipeDismiss must exist");
  const endAt = source.indexOf("\nfunction showToast(", startAt);
  assert.notEqual(endAt, -1);
  const factory = new Function(
    `const toastSwipeDismissPx = ${thresholdMatch[1]};`
      + source.slice(startAt, endAt)
      + "return bindToastSwipeDismiss;",
  );
  return { bind: factory(), threshold: Number(thresholdMatch[1]) };
}

function fakeToast() {
  const listeners = new Map();
  const classes = new Set();
  return {
    style: {},
    classList: {
      add: (name) => classes.add(name),
      remove: (name) => classes.delete(name),
      contains: (name) => classes.has(name),
    },
    addEventListener: (name, handler) => listeners.set(name, handler),
    fire(name, x, y, { cancelable = true } = {}) {
      const event = { touches: [{ clientX: x, clientY: y }], cancelable, prevented: false };
      event.preventDefault = () => { event.prevented = true; };
      listeners.get(name)?.(event);
      return event;
    },
    has: (name) => listeners.has(name),
    classes,
  };
}

test("a decisive horizontal swipe dismisses the notice", () => {
  const { bind, threshold } = loadSwipeBinder();
  const node = fakeToast();
  let closed = 0;
  bind(node, () => { closed += 1; });

  node.fire("touchstart", 200, 100);
  node.fire("touchmove", 200 - 20, 100);
  assert.equal(node.classes.has("swiping"), true, "the card follows the finger");
  node.fire("touchmove", 200 - (threshold + 12), 100);
  node.fire("touchend", 200 - (threshold + 12), 100);

  assert.equal(closed, 1, "past the threshold, release dismisses");
  assert.equal(node.classes.has("swiped-out"), true);
  assert.match(node.style.transform, /translateX\(-120%\)/, "and it leaves the way it was pushed");
});

test("a short drag springs back and keeps the notice", () => {
  const { bind, threshold } = loadSwipeBinder();
  const node = fakeToast();
  let closed = 0;
  bind(node, () => { closed += 1; });

  node.fire("touchstart", 200, 100);
  node.fire("touchmove", 200 + Math.round(threshold / 2), 100);
  node.fire("touchend", 200 + Math.round(threshold / 2), 100);

  assert.equal(closed, 0, "an indecisive drag must not dismiss");
  assert.equal(node.classes.has("swiping"), false, "and the card settles back");
  assert.equal(node.style.transform, "", "with its inline offset cleared");
});

test("a vertical drag is left to whatever is underneath", () => {
  const { bind, threshold } = loadSwipeBinder();
  const node = fakeToast();
  let closed = 0;
  bind(node, () => { closed += 1; });

  node.fire("touchstart", 200, 300);
  // Mostly vertical, but far enough horizontally to pass the threshold on its own.
  // The axis is claimed once at the start of the gesture, so this stays a scroll.
  const move = node.fire("touchmove", 200 + threshold + 20, 300 - 200);
  node.fire("touchend", 200 + threshold + 20, 300 - 200);

  assert.equal(closed, 0, "scrolling the transcript must never dismiss a notice");
  assert.equal(node.classes.has("swiping"), false);
  assert.equal(move.prevented, false, "and the scroll is not swallowed");
});

test("the gesture is ignored until it clears the noise floor", () => {
  const { bind } = loadSwipeBinder();
  const node = fakeToast();
  bind(node, () => { throw new Error("must not close"); });

  node.fire("touchstart", 200, 100);
  const move = node.fire("touchmove", 203, 102);
  assert.equal(node.classes.has("swiping"), false, "3px is noise, not a direction");
  assert.equal(move.prevented, false);
});

test("a cancelled touch puts the card back", () => {
  const { bind, threshold } = loadSwipeBinder();
  const node = fakeToast();
  let closed = 0;
  bind(node, () => { closed += 1; });

  node.fire("touchstart", 200, 100);
  node.fire("touchmove", 200 - (threshold + 30), 100);
  node.fire("touchcancel", 0, 0);

  assert.equal(closed, 0, "an interrupted gesture is not a dismissal");
  assert.equal(node.style.transform, "", "and the card does not stay pushed aside");
  assert.equal(node.classes.has("swiping"), false);
});

test("every toast gets the gesture, not just the persistent ones", () => {
  assert.match(
    source,
    /node\.querySelector\("button"\)\.addEventListener\("click", close\);\s*\n\s*bindToastSwipeDismiss\(node, close\);/,
    "bound next to the close button so the two ways out stay together",
  );
});
