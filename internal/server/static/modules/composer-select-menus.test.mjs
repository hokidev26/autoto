import test from "node:test";
import assert from "node:assert/strict";

import { composerSelectMenuLayout } from "./composer-select-menus.mjs";

test("model menu near the right edge stays inside the viewport", () => {
  // The model chip sits on the right of the composer. Layout used to size the
  // menu at 260px while CSS min-width forced 290px, so the checkmarks clipped.
  const layout = composerSelectMenuLayout({
    triggerRect: { left: 820, width: 96, top: 700, right: 916 },
    viewportWidth: 960,
    viewportHeight: 800,
    selectId: "modelSelect",
  });
  assert.equal(layout.width, 290);
  assert.equal(layout.left, 916 - 290);
  assert.ok(layout.left + layout.width <= 960 - 8);
  assert.equal(layout.bottom, 800 - 700 + 6);
});

test("a CSS min-width wider than the first guess still clamps after measure", () => {
  const layout = composerSelectMenuLayout({
    triggerRect: { left: 820, width: 96, top: 700 },
    viewportWidth: 960,
    viewportHeight: 800,
    selectId: "modelSelect",
    measuredWidth: 320,
  });
  assert.equal(layout.width, 320);
  assert.equal(layout.left, 916 - 320);
  assert.ok(layout.left + layout.width <= 960 - 8);
});

test("narrow viewports shrink the model menu instead of overflowing", () => {
  const layout = composerSelectMenuLayout({
    triggerRect: { left: 40, width: 80, top: 400 },
    viewportWidth: 200,
    viewportHeight: 500,
    selectId: "modelSelect",
  });
  assert.equal(layout.width, 184);
  assert.equal(layout.left, 8);
});

test("a right-side chip opens the menu from its right edge", () => {
  const layout = composerSelectMenuLayout({
    triggerRect: { left: 640, width: 96, top: 700, right: 736 },
    viewportWidth: 960,
    viewportHeight: 800,
    selectId: "modelSelect",
  });
  assert.equal(layout.width, 290);
  assert.equal(layout.left, 736 - 290);
});
