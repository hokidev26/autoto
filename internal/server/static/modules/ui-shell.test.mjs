import test from "node:test";
import assert from "node:assert/strict";

import { createUIShellController } from "./ui-shell.mjs";

// Minimal hand-rolled DOM sufficient to drive bindComposerSelectMenus()'s
// desktop popover path: document.createElement, classList<->className sync,
// attribute get/set, parent/child tracking (for isConnected), and a tiny
// querySelector supporting tag/.class/#id/[attr]/[attr="value"] selectors.
function attributeSelectorParts(selector) {
  const match = selector.match(/^\[([\w-]+)(?:="([^"]*)")?\]$/);
  if (!match) return null;
  return { attr: match[1], value: match[2] };
}

function matchesSelector(node, selector) {
  const attrSelector = attributeSelectorParts(selector);
  if (attrSelector) {
    const actual = node.getAttribute(attrSelector.attr);
    if (attrSelector.value === undefined) return actual !== null;
    return actual === attrSelector.value;
  }
  if (selector.startsWith(".")) return node.classList.contains(selector.slice(1));
  if (selector.startsWith("#")) return node.id === selector.slice(1);
  return String(node.tagName || "").toLowerCase() === selector.toLowerCase();
}

function queryAll(root, selector) {
  const results = [];
  const walk = (node) => {
    for (const child of node.children) {
      if (matchesSelector(child, selector)) results.push(child);
      walk(child);
    }
  };
  walk(root);
  return results;
}

function makeNode(tag) {
  const attributes = new Map();
  const classSet = new Set();
  const listeners = new Map();
  const node = {
    tagName: String(tag || "").toUpperCase(),
    children: [],
    parentNode: null,
    id: "",
    textContent: "",
    innerHTML: "",
    value: "",
    checked: false,
    disabled: false,
    type: "",
    style: {},
    dataset: {},
    classList: {
      add: (...names) => names.forEach((name) => classSet.add(name)),
      remove: (...names) => names.forEach((name) => classSet.delete(name)),
      contains: (name) => classSet.has(name),
      toggle: (name, force) => {
        const next = force === undefined ? !classSet.has(name) : Boolean(force);
        if (next) classSet.add(name); else classSet.delete(name);
        return next;
      },
    },
    setAttribute(name, value) { attributes.set(name, String(value)); },
    getAttribute(name) { return attributes.has(name) ? attributes.get(name) : null; },
    removeAttribute(name) { attributes.delete(name); },
    addEventListener(name, handler) { listeners.set(name, handler); },
    removeEventListener(name) { listeners.delete(name); },
    dispatch(name, event = {}) { listeners.get(name)?.(event); },
    appendChild(child) {
      child.parentNode = node;
      node.children.push(child);
      return child;
    },
    append(...items) { items.forEach((item) => node.appendChild(item)); },
    replaceChildren(...items) {
      node.children.forEach((child) => { child.parentNode = null; });
      node.children = [];
      items.forEach((item) => node.appendChild(item));
    },
    remove() {
      if (node.parentNode) node.parentNode.children = node.parentNode.children.filter((child) => child !== node);
      node.parentNode = null;
    },
    contains(other) {
      let cursor = other;
      while (cursor) {
        if (cursor === node) return true;
        cursor = cursor.parentNode;
      }
      return false;
    },
    querySelector(selector) { return queryAll(node, selector)[0] || null; },
    querySelectorAll(selector) { return queryAll(node, selector); },
    closest() { return null; },
    focus() { node.focused = true; },
    getBoundingClientRect() { return { left: 40, top: 200, width: 40, height: 28 }; },
    get isConnected() {
      let cursor = node.parentNode;
      while (cursor) {
        if (cursor.__root) return true;
        cursor = cursor.parentNode;
      }
      return false;
    },
  };
  Object.defineProperty(node, "className", {
    get() { return [...classSet].join(" "); },
    set(value) {
      classSet.clear();
      String(value || "").split(/\s+/).filter(Boolean).forEach((name) => classSet.add(name));
    },
    enumerable: true,
    configurable: true,
  });
  return node;
}

function makeOption(value, textContent) {
  return { value, textContent, hidden: false, disabled: false };
}

// Builds a fake document/window pair with a single desktop "permissionMode"
// composer-select trigger wired up, enough to exercise open()/appendPermission-
// Options()/appendPermissionSafetyStatus() without a real browser.
function setupComposerSelectDOM() {
  const body = makeNode("body");
  body.__root = true;

  const trigger = makeNode("button");
  trigger.dataset.composerSelect = "permissionMode";

  const selectOptions = [
    makeOption("default", "自動"),
    makeOption("acceptEdits", "允許修改檔案"),
    makeOption("bypassPermissions", "允許全部"),
  ];
  const select = {
    id: "permissionMode",
    value: "default",
    disabled: false,
    options: selectOptions,
    addEventListener() {},
    removeEventListener() {},
  };

  const idMap = { permissionMode: select };
  const fakeDocument = {
    body,
    documentElement: { clientWidth: 1280, clientHeight: 900 },
    activeElement: null,
    createElement: (tag) => makeNode(tag),
    getElementById: (id) => idMap[id] || null,
    querySelector: () => null,
    querySelectorAll: (selector) => (selector === "[data-composer-select]" ? [trigger] : []),
    addEventListener() {},
    removeEventListener() {},
  };
  const fakeWindow = {
    matchMedia: () => ({ matches: false }),
    innerWidth: 1280,
    innerHeight: 900,
    addEventListener() {},
    removeEventListener() {},
  };

  return { fakeDocument, fakeWindow, trigger, select, body };
}

function withGlobals(fakeDocument, fakeWindow, fn) {
  const previousDocument = globalThis.document;
  const previousWindow = globalThis.window;
  globalThis.document = fakeDocument;
  globalThis.window = fakeWindow;
  return Promise.resolve()
    .then(fn)
    .finally(() => {
      globalThis.document = previousDocument;
      globalThis.window = previousWindow;
    });
}

function findMenu(body) {
  return body.children.find((child) => child.id === "composerSelectPopover");
}

function openPermissionMenu(trigger) {
  trigger.dispatch("click", { preventDefault() {}, stopPropagation() {} });
}

function flushMicrotasks() {
  return new Promise((resolve) => setImmediate(resolve));
}

test("danger reflection toggle is the only row in the safety section", async () => {
  const { fakeDocument, fakeWindow, trigger, body } = setupComposerSelectDOM();
  await withGlobals(fakeDocument, fakeWindow, async () => {
    const controller = createUIShellController({
      state: {},
      resizeTerminal() {},
      requestAPI: async () => ({}),
    });
    controller.bindComposerSelectMenus();
    openPermissionMenu(trigger);

    const menu = findMenu(body);
    const toggle = menu.querySelector(".composer-permission-danger-reflection-toggle");
    assert.ok(toggle, "expected a danger reflection toggle to be rendered");
    assert.equal(toggle.type, "checkbox");
    assert.equal(toggle.getAttribute("role"), "switch");

    // Only the toggle. The old "permission protection / enabled" note was
    // removed: it restated something always true and could not be acted on, so
    // it was pure noise above the one control that does something here.
    const safetyRows = menu.querySelectorAll(".composer-permission-safety-status");
    assert.equal(safetyRows.length, 1);
    assert.ok(safetyRows[0].classList.contains("composer-permission-danger-reflection"));
  });
});

test("danger reflection toggle reflects the value loaded from GET /api/workflow/preferences", async () => {
  const { fakeDocument, fakeWindow, trigger, body } = setupComposerSelectDOM();
  const calls = [];
  await withGlobals(fakeDocument, fakeWindow, async () => {
    const controller = createUIShellController({
      state: {},
      resizeTerminal() {},
      requestAPI: async (path, options = {}) => {
        calls.push({ path, options });
        return {
          requireConfirmationForExec: true,
          requireConfirmationForWrites: false,
          allowReadOnlyByDefault: true,
          dangerReflectionEnabled: true,
        };
      },
    });
    controller.bindComposerSelectMenus();
    openPermissionMenu(trigger);

    const menu = findMenu(body);
    const toggle = menu.querySelector(".composer-permission-danger-reflection-toggle");
    // Before the GET resolves the control shows its optimistic default.
    assert.equal(toggle.checked, false);

    await flushMicrotasks();

    assert.equal(toggle.checked, true);
    assert.equal(toggle.getAttribute("aria-checked"), "true");
    assert.equal(calls.length, 1);
    assert.equal(calls[0].path, "/api/workflow/preferences");
  });
});

test("toggling danger reflection sends the full preferences payload as a PUT", async () => {
  const { fakeDocument, fakeWindow, trigger, body } = setupComposerSelectDOM();
  const calls = [];
  await withGlobals(fakeDocument, fakeWindow, async () => {
    const controller = createUIShellController({
      state: {},
      resizeTerminal() {},
      requestAPI: async (path, options = {}) => {
        calls.push({ path, options });
        if (options.method === "PUT") return JSON.parse(options.body);
        return {
          requireConfirmationForExec: true,
          requireConfirmationForWrites: false,
          allowReadOnlyByDefault: true,
          dangerReflectionEnabled: false,
        };
      },
    });
    controller.bindComposerSelectMenus();
    openPermissionMenu(trigger);
    await flushMicrotasks();

    const menu = findMenu(body);
    const toggle = menu.querySelector(".composer-permission-danger-reflection-toggle");
    assert.equal(toggle.checked, false);

    toggle.checked = true;
    toggle.dispatch("change");
    await flushMicrotasks();

    const putCall = calls.find((call) => call.options.method === "PUT");
    assert.ok(putCall, "expected a PUT request");
    assert.equal(putCall.path, "/api/workflow/preferences");
    assert.deepEqual(JSON.parse(putCall.options.body), {
      requireConfirmationForExec: true,
      requireConfirmationForWrites: false,
      allowReadOnlyByDefault: true,
      dangerReflectionEnabled: true,
    });
    assert.equal(toggle.checked, true);
    assert.equal(toggle.getAttribute("aria-checked"), "true");
  });
});

test("a failed PUT reverts the toggle and surfaces the error", async () => {
  const { fakeDocument, fakeWindow, trigger, body } = setupComposerSelectDOM();
  const errors = [];
  await withGlobals(fakeDocument, fakeWindow, async () => {
    const controller = createUIShellController({
      state: {},
      resizeTerminal() {},
      showError: (error) => errors.push(error),
      requestAPI: async (path, options = {}) => {
        if (options.method === "PUT") throw new Error("boom");
        return {
          requireConfirmationForExec: false,
          requireConfirmationForWrites: false,
          allowReadOnlyByDefault: false,
          dangerReflectionEnabled: false,
        };
      },
    });
    controller.bindComposerSelectMenus();
    openPermissionMenu(trigger);
    await flushMicrotasks();

    const menu = findMenu(body);
    const toggle = menu.querySelector(".composer-permission-danger-reflection-toggle");
    assert.equal(toggle.checked, false);

    toggle.checked = true;
    toggle.dispatch("change");
    await flushMicrotasks();

    assert.equal(toggle.checked, false, "toggle should revert to its previous state on failure");
    assert.equal(toggle.getAttribute("aria-checked"), "false");
    assert.equal(errors.length, 1);
    assert.equal(errors[0].message, "boom");
  });
});

test("toggling without a requestAPI reverts instead of silently no-opping", async () => {
  const { fakeDocument, fakeWindow, trigger, body } = setupComposerSelectDOM();
  const errors = [];
  await withGlobals(fakeDocument, fakeWindow, async () => {
    const controller = createUIShellController({
      state: {},
      resizeTerminal() {},
      showError: (error) => errors.push(error),
      requestAPI: null,
    });
    controller.bindComposerSelectMenus();
    openPermissionMenu(trigger);
    await flushMicrotasks();

    const menu = findMenu(body);
    const toggle = menu.querySelector(".composer-permission-danger-reflection-toggle");
    assert.equal(toggle.checked, false);

    toggle.checked = true;
    toggle.dispatch("change");
    await flushMicrotasks();

    assert.equal(toggle.checked, false);
    assert.equal(errors.length, 1);
  });
});
