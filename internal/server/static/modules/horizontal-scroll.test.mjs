import test from "node:test";
import assert from "node:assert/strict";

import {
  canScrollHorizontally,
  focusHorizontalRegionFromPointer,
  nearestHorizontalScroller,
  scrollHorizontalRegionFromKeyboard,
} from "./horizontal-scroll.mjs";

function scroller(overrides = {}) {
  return {
    nodeType: 1,
    overflowX: "auto",
    scrollWidth: 1400,
    clientWidth: 800,
    scrollLeft: 0,
    parentElement: null,
    contains(node) { return node === this || node?.parentElement === this; },
    hasAttribute(name) { return Object.hasOwn(this, `attr_${name}`); },
    getAttribute(name) { return this[`attr_${name}`]; },
    setAttribute(name, value) { this[`attr_${name}`] = String(value); },
    focus() { this.focusCalls = (this.focusCalls || 0) + 1; },
    closest(selector) { return selector === ".settings-h-scroll" ? this : null; },
    ...overrides,
  };
}

test("horizontal overflow regions are detected from auto/scroll and ignored when clipped", () => {
  assert.equal(canScrollHorizontally(scroller()), true);
  assert.equal(canScrollHorizontally(scroller({ overflowX: "scroll" })), true);
  assert.equal(canScrollHorizontally(scroller({ overflowX: "hidden" })), false);
  assert.equal(canScrollHorizontally(scroller({ scrollWidth: 800 })), false);
});

test("nearest horizontal scroller walks unmarked overflow ancestors", () => {
  const wrap = scroller({ closest() { return null; } });
  const cell = { nodeType: 1, parentElement: wrap, closest() { return null; } };
  assert.equal(nearestHorizontalScroller(cell), wrap);
});

test("arrow keys pan an overflowing region unless the region itself is focused", () => {
  const wrap = scroller();
  const button = {
    nodeType: 1,
    parentElement: wrap,
    closest(selector) {
      if (selector === ".settings-h-scroll") return wrap;
      return null;
    },
  };
  let prevented = false;
  assert.equal(scrollHorizontalRegionFromKeyboard({
    key: "ArrowRight",
    target: button,
    preventDefault() { prevented = true; },
  }), true);
  assert.equal(wrap.scrollLeft, 280);
  assert.equal(prevented, true);

  assert.equal(scrollHorizontalRegionFromKeyboard({
    key: "ArrowRight",
    target: wrap,
    preventDefault() { throw new Error("native overflow scrolling should handle the focused wrap"); },
  }), false);
});

test("arrow keys keep moving the caret inside fields", () => {
  const wrap = scroller();
  const input = {
    nodeType: 1,
    parentElement: wrap,
    closest(selector) {
      if (selector === ".settings-h-scroll") return wrap;
      if (selector === "input, textarea, select, [contenteditable='true']") return input;
      return null;
    },
  };
  assert.equal(scrollHorizontalRegionFromKeyboard({
    key: "ArrowRight",
    target: input,
    preventDefault() { throw new Error("typing caret movement must keep the arrows"); },
  }), false);
  assert.equal(wrap.scrollLeft, 0);
});

test("arrow keys skip regions that already handled the event", () => {
  const wrap = scroller();
  const button = {
    nodeType: 1,
    parentElement: wrap,
    closest(selector) { return selector === ".settings-h-scroll" ? wrap : null; },
  };
  assert.equal(scrollHorizontalRegionFromKeyboard({
    key: "ArrowRight",
    defaultPrevented: true,
    target: button,
    preventDefault() { throw new Error("tab strips that already consumed arrows must keep them"); },
  }), false);
  assert.equal(wrap.scrollLeft, 0);
});

test("unmarked overflow ancestors still pan from arrow keys", () => {
  const wrap = scroller({ closest() { return null; } });
  const cell = {
    nodeType: 1,
    parentElement: wrap,
    closest() { return null; },
  };
  assert.equal(scrollHorizontalRegionFromKeyboard({
    key: "ArrowRight",
    target: cell,
    preventDefault() {},
  }), true);
  assert.equal(wrap.scrollLeft, 280);
});

test("clicking a non-interactive cell focuses the overflowing region", () => {
  const wrap = scroller({ closest(selector) { return selector === ".settings-h-scroll" ? this : null; } });
  const cell = {
    nodeType: 1,
    parentElement: wrap,
    closest(selector) {
      if (selector === ".settings-h-scroll") return wrap;
      return null;
    },
  };
  const button = {
    nodeType: 1,
    parentElement: wrap,
    closest(selector) {
      if (selector === ".settings-h-scroll") return wrap;
      if (selector === "a, button, input, select, textarea, label") return button;
      return null;
    },
  };
  assert.equal(focusHorizontalRegionFromPointer({ target: cell }), true);
  assert.equal(wrap.focusCalls, 1);
  assert.equal(wrap.attr_tabindex, "0");
  assert.equal(focusHorizontalRegionFromPointer({ target: button }), false);
  assert.equal(wrap.focusCalls, 1);
});
