import assert from "node:assert/strict";
import test from "node:test";

import { createSpecBoardController } from "./spec-board.mjs";

// The task-list board reports aria-expanded and lights its header button while
// it is up, but the button was wired straight to open(), so pressing a lit,
// expanded button did nothing and the × was the only way out. It was also the one
// dialog in the app with no Escape handler.
function harness(hooks = {}) {
  const listeners = { click: new Map(), keydown: [] };
  const classes = new Map();
  const attrs = new Map();

  const fakeElement = (id) => {
    const set = new Set();
    classes.set(id, set);
    return {
      id,
      classList: {
        add: (...names) => names.forEach((n) => set.add(n)),
        remove: (...names) => names.forEach((n) => set.delete(n)),
        toggle: (name, on) => (on ? set.add(name) : set.delete(name)),
        contains: (name) => set.has(name),
      },
      setAttribute: (name, value) => attrs.set(`${id}:${name}`, value),
      removeAttribute: (name) => attrs.delete(`${id}:${name}`),
      addEventListener: (type, handler) => {
        if (type === "click") listeners.click.set(id, handler);
      },
      querySelector: () => null,
      querySelectorAll: () => [],
      innerHTML: "",
      title: "",
      disabled: false,
    };
  };

  const elements = new Map();
  for (const id of ["specBoardBtn", "specBoardModal", "specBoardBody", "closeSpecBoardBtn"]) {
    elements.set(id, fakeElement(id));
  }
  elements.get("specBoardModal").classList.add("hidden");

  const documentImpl = {
    getElementById: (id) => elements.get(id) || null,
    addEventListener: (type, handler) => {
      if (type === "keydown") listeners.keydown.push(handler);
    },
  };

  const controller = createSpecBoardController({
    request: async (path) => (String(path).includes("/spec") ? { tasks: [] } : { agents: [] }),
    // The option is named "document"; the controller aliases it internally.
    document: documentImpl,
    showError: () => {},
    showToast: () => {},
    ...hooks,
  });
  // setAgent is what populates rootAgent, and open() refuses without one.
  controller.setAgent({ id: "agent-1" });
  controller.bind();

  return { controller, listeners, classes, attrs };
}

test("the header button closes the board it opened", () => {
  const { controller, listeners } = harness();
  const click = listeners.click.get("specBoardBtn");
  assert.equal(typeof click, "function", "bind() must attach a click handler for this test to mean anything");

  click();
  assert.equal(controller.getState().open, true);
  click();
  assert.equal(controller.getState().open, false, "a second press must collapse the board");
});

test("the button state agrees with whether the board is open", () => {
  // The lie this fixes: aria-expanded said true while the button did nothing.
  const { controller, listeners, classes, attrs } = harness();
  const click = listeners.click.get("specBoardBtn");
  click();
  assert.equal(attrs.get("specBoardBtn:aria-expanded"), "true");
  assert.equal(classes.get("specBoardBtn").has("active"), true);
  click();
  assert.equal(attrs.get("specBoardBtn:aria-expanded"), "false");
  assert.equal(classes.get("specBoardBtn").has("active"), false);
  assert.equal(controller.getState().open, false);
});

test("Escape closes the board, like every other dialog", () => {
  const { controller, listeners } = harness();
  listeners.click.get("specBoardBtn")();
  assert.equal(controller.getState().open, true);
  assert.ok(listeners.keydown.length > 0, "bind() must attach a keydown handler");

  let prevented = false;
  for (const handler of listeners.keydown) {
    handler({ key: "Escape", preventDefault: () => { prevented = true; } });
  }
  assert.equal(controller.getState().open, false);
  assert.equal(prevented, true, "the key must be consumed so it does not also act elsewhere");
});

test("the board docks into the utility column and claims it", () => {
  // It used to open as a centred dialog over the conversation. Docking means the
  // shared geometry class plus a claim on the column, because the workspace panel,
  // the details panel and the task tray all occupy that same grid cell.
  const claims = [];
  const { controller, listeners, classes } = harness({
    onDockOpen: () => claims.push("open"),
    onDockClose: () => claims.push("close"),
  });
  listeners.click.get("specBoardBtn")();
  assert.equal(classes.get("specBoardModal").has("shell-dock-mode"), true);
  assert.deepEqual(claims, ["open"]);

  listeners.click.get("specBoardBtn")();
  assert.equal(classes.get("specBoardModal").has("shell-dock-mode"), false, "closing must release the geometry");
  assert.equal(classes.get("specBoardModal").has("hidden"), true);
  assert.deepEqual(claims, ["open", "close"], "closing must release the column too");
  void controller;
});

test("Escape is ignored while the board is closed", () => {
  // Otherwise it would consume a key the rest of the app still wants.
  const { listeners } = harness();
  let prevented = false;
  for (const handler of listeners.keydown) {
    handler({ key: "Escape", preventDefault: () => { prevented = true; } });
  }
  assert.equal(prevented, false);
});
