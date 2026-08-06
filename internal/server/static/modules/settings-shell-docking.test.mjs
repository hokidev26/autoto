import test from "node:test";
import assert from "node:assert/strict";

import { createSettingsShellHelpers } from "./settings-shell-helpers.mjs";

// The settings shell docks the settings modal into the app shell as a panel and
// must be able to put the DOM back exactly as it found it. These tests drive the
// real enter/exit pair against fake nodes and assert the restoration contract.

function makeStyle() {
  const props = new Map();
  const priorities = new Map();
  return {
    props,
    setProperty(name, value, priority = "") { props.set(name, value); priorities.set(name, priority); },
    removeProperty(name) { props.delete(name); priorities.delete(name); },
    getPropertyValue(name) { return props.get(name) ?? ""; },
    getPropertyPriority(name) { return priorities.get(name) ?? ""; },
  };
}

function makeNode(id = "") {
  const attrs = new Map();
  const node = {
    id,
    style: makeStyle(),
    parentNode: null,
    nextSibling: null,
    children: [],
    hidden: false,
    // A real record rather than a no-op: the rail's compact state is part of the
    // docking contract, so the tests have to be able to see it go on and come off.
    classes: new Set(),
    classList: {
      add(...names) { names.forEach((n) => node.classes.add(n)); },
      remove(...names) { names.forEach((n) => node.classes.delete(n)); },
      toggle(name, force) {
        const on = force === undefined ? !node.classes.has(name) : Boolean(force);
        if (on) node.classes.add(name); else node.classes.delete(name);
        return on;
      },
      contains: (name) => node.classes.has(name),
    },
    getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
    setAttribute: (name, value) => attrs.set(name, value),
    removeAttribute: (name) => attrs.delete(name),
    hasAttribute: (name) => attrs.has(name),
    querySelector: () => null,
    querySelectorAll: () => [],
    appendChild(child) { child.parentNode = node; node.children.push(child); return child; },
    insertBefore(child, ref) { child.parentNode = node; node.children.splice(node.children.indexOf(ref), 0, child); return child; },
    attrs,
  };
  return node;
}

function makeShellFixture() {
  const appShell = makeNode("appShell");
  const card = makeNode("settingsCard");
  const modal = makeNode("settingsModal");
  modal.querySelector = (selector) => (selector === ".settings-dialog-shell" ? card : null);
  modal.setAttribute("role", "dialog");
  modal.setAttribute("aria-modal", "true");

  const originalParent = makeNode("body");
  const sibling = makeNode("sibling");
  originalParent.appendChild(modal);
  originalParent.appendChild(sibling);
  modal.nextSibling = sibling;

  const hideable = {};
  for (const id of ["sessionSidebar", "sidebarResizeHandle", "overviewDashboard", "conversationPanel", "workbenchPanel", "schedulePanel", "terminalPanel", "conversationDetailsPanel", "backgroundTaskTray", "expandTerminalBtn"]) {
    hideable[id] = makeNode(id);
  }
  const nodes = { appShell, settingsModal: modal, ...hideable };
  return { appShell, modal, card, originalParent, sibling, nodes, hideable };
}

function makeHelpers(fixture, overrides = {}) {
  const calls = { saveCurrentChatDraft: 0, hideSlashCommandPalette: 0, closeMobileSidebar: 0, applyPrimaryWorkbench: [] };
  const state = { settingsShellOpen: false, activeWorkbench: "conversation", settingsMobileViewport: false, mobileSettingsView: "detail", ...overrides.state };
  const helpers = createSettingsShellHelpers({
    state,
    isMobileAppViewport: () => overrides.mobile ?? false,
    selectSettingsPanel: () => {},
    renderSettingsNav: () => {},
    renderMobileSettingsIndex: () => {},
    syncSettingsCloseControl: () => {},
    saveCurrentChatDraft: () => { calls.saveCurrentChatDraft += 1; },
    hideSlashCommandPalette: () => { calls.hideSlashCommandPalette += 1; },
    closeMobileSidebar: () => { calls.closeMobileSidebar += 1; },
    applyPrimaryWorkbench: (value) => calls.applyPrimaryWorkbench.push(value),
  });
  // The module resolves nodes through $(), which reads the global document.
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById: (id) => fixture.nodes[id] ?? null,
    body: { classList: { toggle() {}, add() {}, remove() {}, contains: () => false } },
    documentElement: makeNode("html"),
  };
  return { helpers, state, calls, restore: () => { globalThis.document = previousDocument; } };
}

test("entering the settings shell reparents the modal, hides the surfaces, and drops modal semantics", () => {
  const fixture = makeShellFixture();
  const { helpers, state, calls, restore } = makeHelpers(fixture);
  try {
    helpers.enterSettingsShell();

    assert.equal(state.settingsShellOpen, true);
    assert.equal(fixture.modal.parentNode, fixture.appShell, "modal is docked into the app shell");
    // Docked it is a region inside the page, not a modal dialog over it.
    assert.equal(fixture.modal.getAttribute("role"), "region");
    assert.equal(fixture.modal.getAttribute("aria-modal"), null);
    // The conversation surfaces must be taken out of both layout and the
    // accessibility tree, not merely covered by the docked panel.
    for (const id of ["sessionSidebar", "sidebarResizeHandle", "conversationPanel", "workbenchPanel", "schedulePanel", "terminalPanel"]) {
      const node = fixture.hideable[id];
      assert.equal(node.style.getPropertyValue("display"), "none", `${id} should be display:none while docked`);
      assert.equal(node.style.getPropertyPriority("display"), "important", `${id} must win over stylesheet display rules`);
      assert.equal(node.getAttribute("aria-hidden"), "true", `${id} should leave the accessibility tree`);
    }
    // The in-progress draft is preserved before the composer goes away.
    assert.equal(calls.saveCurrentChatDraft, 1);
    assert.equal(calls.hideSlashCommandPalette, 1);
    assert.equal(calls.closeMobileSidebar, 1);
  } finally {
    restore();
  }
});

test("leaving the settings shell restores the original parent, sibling order, and modal semantics", () => {
  const fixture = makeShellFixture();
  const { helpers, state, calls, restore } = makeHelpers(fixture);
  try {
    helpers.enterSettingsShell();
    helpers.exitSettingsShell();

    assert.equal(state.settingsShellOpen, false);
    assert.equal(fixture.modal.parentNode, fixture.originalParent, "modal returns to its original parent");
    assert.ok(
      fixture.originalParent.children.indexOf(fixture.modal) < fixture.originalParent.children.indexOf(fixture.sibling),
      "modal is reinserted before the sibling it originally preceded",
    );
    assert.equal(fixture.modal.getAttribute("role"), "dialog", "dialog semantics are restored");
    assert.equal(fixture.modal.getAttribute("aria-modal"), "true");
    for (const [id, node] of Object.entries(fixture.hideable)) {
      assert.equal(node.style.getPropertyValue("display"), "", `${id} display override is removed`);
      assert.equal(node.getAttribute("aria-hidden"), null, `${id} returns to the accessibility tree`);
    }
    assert.deepEqual(calls.applyPrimaryWorkbench, ["conversation"], "the previous workbench is reapplied");
  } finally {
    restore();
  }
});

test("inline styles applied while docked are removed again on exit", () => {
  const fixture = makeShellFixture();
  const { helpers, restore } = makeHelpers(fixture);
  try {
    helpers.enterSettingsShell();
    assert.equal(fixture.modal.style.getPropertyValue("position"), "relative");
    assert.equal(fixture.card.style.getPropertyValue("height"), "100%");

    helpers.exitSettingsShell();
    // Nothing the docking added may survive: the modal must be styled by CSS
    // again, otherwise the next open inherits a half-docked geometry.
    assert.equal(fixture.modal.style.props.size, 0, "modal inline styles are cleared");
    assert.equal(fixture.card.style.props.size, 0, "card inline styles are cleared");
    assert.equal(fixture.appShell.style.props.size, 0, "app shell grid override is cleared");
  } finally {
    restore();
  }
});

// layoutSettingsShell pins the first grid column to the columns-mode width so
// the settings panel can use the stored width for its own category list. That
// leaves the rail narrow while it may still be in docked mode, whose CSS is
// written for 296px. CSS cannot read an inline grid value, so the narrowing is
// published as a class; without it the docked rail clipped "AUTOTO" and stacked
// each label one character per line inside 68px.
test("docking publishes the rail's narrowed state so CSS can compact it", () => {
  const fixture = makeShellFixture();
  const { helpers, restore } = makeHelpers(fixture);
  try {
    helpers.enterSettingsShell();
    assert.ok(
      fixture.appShell.classList.contains("settings-rail-compact"),
      "the rail is told it was narrowed",
    );
    // The flag has to agree with the inline width that caused it.
    assert.match(fixture.appShell.style.getPropertyValue("grid-template-columns"), /^(?:68|76)px /);

    helpers.exitSettingsShell();
    assert.equal(
      fixture.appShell.classList.contains("settings-rail-compact"),
      false,
      "the rail goes back to whatever its navigation stage says",
    );
  } finally {
    restore();
  }
});

// Only the desktop branch narrows the rail, so the flag has to follow the
// viewport across a resize. Otherwise a phone-width layout that never squeezed
// the rail would keep the compact styling.
test("the compact flag tracks the viewport, since only desktop narrows the rail", () => {
  const fixture = makeShellFixture();
  const { helpers, restore } = makeHelpers(fixture);
  const previousMatchMedia = globalThis.matchMedia;
  // The module asks for (min-width: 768px) to pick a branch and (min-width:
  // 1280px) to pick between 76px and 68px.
  const setViewport = (width) => {
    globalThis.matchMedia = (query) => {
      const min = Number(/min-width:\s*(\d+)px/.exec(query)?.[1] ?? 0);
      return { matches: width >= min };
    };
  };
  try {
    setViewport(1440);
    helpers.enterSettingsShell();
    assert.ok(fixture.appShell.classList.contains("settings-rail-compact"), "desktop narrows the rail");
    assert.match(fixture.appShell.style.getPropertyValue("grid-template-columns"), /^76px /, "and uses the wide rail width");

    setViewport(1024);
    helpers.layoutSettingsShell();
    assert.match(fixture.appShell.style.getPropertyValue("grid-template-columns"), /^68px /, "narrower desktop still narrows");
    assert.ok(fixture.appShell.classList.contains("settings-rail-compact"));

    setViewport(500);
    helpers.layoutSettingsShell();
    assert.equal(fixture.appShell.style.getPropertyValue("grid-template-columns"), "", "mobile drops the override");
    assert.equal(
      fixture.appShell.classList.contains("settings-rail-compact"),
      false,
      "and drops the flag with it",
    );

    setViewport(1440);
    helpers.layoutSettingsShell();
    assert.ok(fixture.appShell.classList.contains("settings-rail-compact"), "returning to desktop restores both");
  } finally {
    if (previousMatchMedia === undefined) delete globalThis.matchMedia;
    else globalThis.matchMedia = previousMatchMedia;
    restore();
  }
});

test("entering twice only relayouts and exiting without a session is inert", () => {
  const fixture = makeShellFixture();
  const { helpers, state, calls, restore } = makeHelpers(fixture);
  try {
    helpers.enterSettingsShell();
    const parentAfterFirst = fixture.modal.parentNode;
    helpers.enterSettingsShell();
    assert.equal(fixture.modal.parentNode, parentAfterFirst, "a second enter must not re-dock");
    assert.equal(calls.saveCurrentChatDraft, 1, "the draft is only saved on the real transition");

    helpers.exitSettingsShell();
    calls.applyPrimaryWorkbench.length = 0;
    helpers.exitSettingsShell();
    assert.equal(state.settingsShellOpen, false);
    assert.deepEqual(calls.applyPrimaryWorkbench, [], "exiting without a session does nothing");
  } finally {
    restore();
  }
});

test("docking never touches agent transports or conversation selection", () => {
  // The shell is a layout change. Historically this was guarded by asserting the
  // source text contained no transport calls; assert it behaviourally instead by
  // giving the helpers no such collaborators and driving a full enter/exit.
  const fixture = makeShellFixture();
  const { helpers, restore } = makeHelpers(fixture);
  try {
    assert.doesNotThrow(() => {
      helpers.enterSettingsShell();
      helpers.exitSettingsShell();
    });
  } finally {
    restore();
  }
});

test("narrowing to mobile drops a settings search query the user can no longer clear", () => {
  // The search field is desktop-only. A query typed before the viewport
  // narrowed used to survive into mobile and keep filtering the nav, and a
  // query matching nothing left the settings page blank with no field on
  // screen to empty. Entering mobile has to reset it.
  const fixture = makeShellFixture();
  const { helpers, state, restore } = makeHelpers(fixture, { mobile: true });
  try {
    // The mobile view switch stamps the modal through dataset; the shared
    // fixture only needs it for this path, so it is set here rather than
    // widening makeNode for every other test.
    fixture.modal.dataset = {};
    state.settingsSearchQuery = "zzzz-no-match";
    state.mobileSettingsView = "index";

    helpers.syncSettingsViewportState();

    assert.equal(state.settingsMobileViewport, true);
    assert.equal(state.settingsSearchQuery, "", "the stale query is cleared on the way into mobile");
  } finally {
    restore();
  }
});
