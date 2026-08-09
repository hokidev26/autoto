import assert from "node:assert/strict";
import test from "node:test";

import { createWorkspaceExplorerController } from "./workspace-explorer.mjs";

// Preview docks into the shared right-hand utility column, the same one the
// conversation details and background task panels use. It was briefly a centred
// dialog instead; these tests pin the docked contract so it stays put, and pin the
// toggle, because the header button reports aria-expanded and used to only ever
// open -- pressing it on an open panel did nothing, which reads as a dead button.
function harness() {
  const classes = new Map();
  const elements = new Map();
  const fakeElement = (id) => {
    const set = new Set();
    classes.set(id, set);
    return {
      classList: {
        add: (...names) => names.forEach((name) => set.add(name)),
        remove: (...names) => names.forEach((name) => set.delete(name)),
        toggle: (name, on) => (on ? set.add(name) : set.delete(name)),
        contains: (name) => set.has(name),
      },
      setAttribute() {},
      removeAttribute() {},
      addEventListener() {},
      querySelector: () => null,
      querySelectorAll: () => [],
      innerHTML: "",
      dataset: {},
      style: {},
      focus() {},
      title: "",
      disabled: false,
    };
  };
  for (const id of ["workspaceModal", "workspacePreviewBtn", "workspaceExplorerBtn", "workspacePreviewIndicator"]) {
    elements.set(id, fakeElement(id));
  }

  const previewEvents = [];
  const state = { agent: { id: "agent-1" } };
  const controller = createWorkspaceExplorerController({
    state,
    request: async () => ({}),
    getElementById: (id) => elements.get(id) || null,
    onPreviewOpen: () => previewEvents.push("open"),
    onPreviewClose: () => previewEvents.push("close"),
    showError: () => {},
    showToast: () => {},
  });
  controller.setAgent(state.agent);
  return { controller, modalClasses: classes.get("workspaceModal"), previewEvents, state };
}

test("preview docks into the utility column", () => {
  const { controller, modalClasses } = harness();
  controller.openWorkspace("preview");
  assert.equal(modalClasses.has("hidden"), false);
  // The class is the whole mechanism: it sizes the card to the utility column.
  assert.equal(modalClasses.has("workspace-preview-dock-mode"), true);
});

test("both tabs dock, and only the preview trims its chrome", () => {
  // shell-dock-mode carries the geometry every docked panel shares. The second
  // class is preview-only because it also hides the title and tab strip, which the
  // files browser needs in order to say what it is.
  const { controller, modalClasses } = harness();
  controller.openWorkspace("files");
  assert.equal(modalClasses.has("shell-dock-mode"), true);
  assert.equal(modalClasses.has("workspace-preview-dock-mode"), false);
  controller.selectTab("preview");
  assert.equal(modalClasses.has("shell-dock-mode"), true);
  assert.equal(modalClasses.has("workspace-preview-dock-mode"), true);
});

test("switching tabs moves in and out of dock mode", () => {
  const { controller, modalClasses } = harness();
  controller.openWorkspace("files");
  assert.equal(typeof controller.selectTab, "function", "selectTab must exist for this test to mean anything");
  controller.selectTab("preview");
  assert.equal(modalClasses.has("workspace-preview-dock-mode"), true);
  controller.selectTab("files");
  assert.equal(modalClasses.has("workspace-preview-dock-mode"), false);
});

test("closing the panel releases the column", () => {
  // The callbacks are tied to the modal, not to the preview tab: both tabs live in
  // the column, so the files tab has to claim and release it too. Without the
  // release the grid keeps a column reserved for a panel that is no longer there.
  const { controller, previewEvents, state } = harness();
  controller.openWorkspace("preview");
  assert.deepEqual(previewEvents, ["open"]);
  controller.closeWorkspace();
  assert.deepEqual(previewEvents, ["open", "close"]);
  assert.equal(state.workspaceOpen, false);

  previewEvents.length = 0;
  controller.openWorkspace("files");
  assert.deepEqual(previewEvents, ["open"], "the files tab docks as well, so it claims the column");
  controller.closeWorkspace();
  assert.deepEqual(previewEvents, ["open", "close"]);
});

test("switching tabs keeps the column the panel is already using", () => {
  // Releasing on a tab switch would collapse the panel the reader is still in.
  const { controller, previewEvents } = harness();
  controller.openWorkspace("files");
  previewEvents.length = 0;
  controller.selectTab("preview");
  controller.selectTab("files");
  assert.deepEqual(previewEvents, [], "a tab switch is not an open or a close");
});

test("the header button closes the panel it opened", () => {
  // bind() owns the toggle, so drive it the way a click does rather than trusting
  // openWorkspace alone: the button used to call openWorkspace unconditionally.
  const { controller, state, previewEvents } = harness();
  const clicks = [];
  const button = {
    addEventListener: (type, handler) => { if (type === "click") clicks.push(handler); },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    setAttribute() {}, removeAttribute() {}, title: "", disabled: false,
  };
  const stub = (id) => (id === "workspacePreviewBtn" ? button : {
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    setAttribute() {}, removeAttribute() {}, addEventListener() {},
    querySelector: () => null, querySelectorAll: () => [], innerHTML: "",
    dataset: {}, style: {}, focus() {}, title: "", disabled: false,
  });
  const toggling = createWorkspaceExplorerController({
    state,
    request: async () => ({}),
    getElementById: stub,
    onPreviewOpen: () => previewEvents.push("open"),
    onPreviewClose: () => previewEvents.push("close"),
    showError: () => {},
    showToast: () => {},
  });
  toggling.setAgent(state.agent);
  toggling.bind();
  assert.ok(clicks.length > 0, "bind() must attach a click handler for this test to mean anything");

  clicks[0]();
  assert.equal(state.workspaceOpen, true);
  clicks[0]();
  assert.equal(state.workspaceOpen, false, "a second press must collapse the panel");
  void controller;
});
