import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";

import {
  createUIShellController,
  maxUtilityPanelWidth,
  minUtilityPanelWidth,
  normalizeUtilityPanelWidth,
  utilityPanelChatMinWidth,
  utilityPanelDesktopBreakpoint,
  utilityPanelMaxAvailable,
  utilityPanelWidthFromPointer,
  utilityPanelWidthPreferenceKey,
} from "./ui-shell.mjs";
import { createSettingsShellHelpers } from "./settings-shell-helpers.mjs";

const staticRoot = new URL("../", import.meta.url);
const indexURL = new URL("index.html", staticRoot);
const appMainURL = new URL("modules/app-main.mjs", staticRoot);
const stylesURL = new URL("styles.css", staticRoot);

class MemoryStorage {
  constructor(entries = []) {
    this.values = new Map(entries);
  }

  getItem(key) {
    return this.values.has(key) ? this.values.get(key) : null;
  }

  setItem(key, value) {
    this.values.set(key, String(value));
  }

  removeItem(key) {
    this.values.delete(key);
  }
}

function replaceGlobal(name, value) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, name);
  Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  return () => {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor);
    else delete globalThis[name];
  };
}

test("utility panel width helpers clamp values, respect available space, and keep a stable preference key", () => {
  assert.equal(utilityPanelWidthPreferenceKey, "autoto.ui.utilityPanelWidth");
  assert.equal(utilityPanelDesktopBreakpoint, 1280);
  assert.equal(utilityPanelChatMinWidth, 420);
  // Low enough that the panel can reach its narrow, phone-shaped tier; the old
  // 320 floor ended the drag before that layout could apply.
  assert.equal(minUtilityPanelWidth, 260);
  assert.equal(maxUtilityPanelWidth, 620);

  // Flat clamp, no viewport info supplied.
  assert.equal(normalizeUtilityPanelWidth(undefined), maxUtilityPanelWidth);
  assert.equal(normalizeUtilityPanelWidth(100), minUtilityPanelWidth);
  assert.equal(normalizeUtilityPanelWidth(900), maxUtilityPanelWidth);
  assert.equal(normalizeUtilityPanelWidth("450.6"), 451);

  // Pointer-driven width: anchored to the viewport's right edge, mirroring
  // sidebarWidthFromPointer's left-anchored sibling.
  assert.equal(utilityPanelWidthFromPointer(1000, 1400), 400);
  assert.equal(utilityPanelWidthFromPointer(1350, 1400), minUtilityPanelWidth);

  // Viewport-aware cap: never allowed to squeeze the chat column or overflow
  // the viewport, even if that is narrower than maxUtilityPanelWidth.
  const tightAvailable = utilityPanelMaxAvailable({ viewportWidth: 1280, railWidth: 76, sidebarWidth: 296 });
  assert.equal(tightAvailable, 1280 - 76 - 296 - 420);
  assert.equal(normalizeUtilityPanelWidth(620, undefined, { maxAvailable: tightAvailable }), Math.max(minUtilityPanelWidth, tightAvailable));
  const roomyAvailable = utilityPanelMaxAvailable({ viewportWidth: 2000, railWidth: 76, sidebarWidth: 296 });
  assert.equal(normalizeUtilityPanelWidth(620, undefined, { maxAvailable: roomyAvailable }), maxUtilityPanelWidth);
});

test("utility panel resize handle markup mirrors the sidebar separator's accessible shape", async () => {
  const html = await readFile(indexURL, "utf8");
  assert.match(
    html,
    /<div id="utilityPanelResizeHandle" class="utility-panel-resize-handle" role="separator" aria-orientation="vertical"[^>]*aria-valuemin="320"[^>]*aria-valuemax="620"[^>]*aria-valuenow="480"[^>]*tabindex="0">/,
  );
  // Placed as a sibling of the panels it resizes, not nested inside any one
  // of them, just like #sidebarResizeHandle sits beside (not inside) .sidebar.
  const handleIndex = html.indexOf('id="utilityPanelResizeHandle"');
  const detailsIndex = html.indexOf('id="conversationDetailsPanel"');
  const backgroundIndex = html.indexOf('id="backgroundTaskTray"');
  assert.ok(handleIndex > -1 && detailsIndex > handleIndex && backgroundIndex > detailsIndex);
});

test("utility panel resize handle is wired up next to the sidebar resizer in app-main", async () => {
  const appMain = await readFile(appMainURL, "utf8");
  assert.match(appMain, /bindSidebarResizer\(\);\s*\n\s*bindUtilityPanelResizer\(\);/);
});

test("utility panel resize handle styles stay hidden until a panel opens at the wide breakpoint", async () => {
  const styles = (await readStylesSource(stylesURL)).replace(/\r\n/g, "\n");
  assert.match(styles, /body\.white-shell\.theme-light \.utility-panel-resize-handle\s*\{[\s\S]*?display:\s*none;[\s\S]*?position:\s*fixed;[\s\S]*?cursor:\s*col-resize/);

  const wideBreakpointStart = styles.indexOf("@media (min-width: 1280px) {\n  body.white-shell.theme-light .app-shell.details-open,");
  assert.ok(wideBreakpointStart > -1, "expected the >=1280px details/background-tasks/preview open block");
  const wideBreakpointBlock = styles.slice(wideBreakpointStart, styles.indexOf("\n}\n", wideBreakpointStart) + 3);
  assert.match(wideBreakpointBlock, /grid-template-columns:\s*76px var\(--session-sidebar-width\) minmax\(420px, 1fr\) var\(--utility-panel-width, clamp\(260px, calc\(50vw - 186px\), 620px\)\)/);
  assert.match(wideBreakpointBlock, /\.app-shell\.details-open \.utility-panel-resize-handle,[\s\S]*?\.app-shell\.preview-open \.utility-panel-resize-handle\s*\{[\s\S]*?display:\s*block;[\s\S]*?right:\s*calc\(var\(--utility-panel-width, clamp\(260px, calc\(50vw - 186px\), 620px\)\) - 3px\)/);

  // The higher-specificity rule that actually wins once the terminal auto-
  // collapses behind an open panel must honour the same custom property.
  assert.match(styles, /\.app-shell\.terminal-collapsed\.preview-open\s*\{\s*\n\s*grid-template-columns:\s*var\(--global-rail-layout-width\) var\(--session-sidebar-layout-width\) minmax\(0, 1fr\) var\(--utility-panel-width, clamp\(260px, calc\(50vw - 186px\), 620px\)\)/);

  // The docked workspace preview card is not a grid child, so its own width
  // must track the same variable to stay visually aligned with the divider.
  assert.match(styles, /\.workspace-preview-dock-mode \.workspace-modal-card\s*\{\s*\n\s*width:\s*var\(--utility-panel-width, clamp\(420px, calc\(50vw - 186px\), 620px\)\)/);
});

test("utility panel resizer drags, keys, resets, persists, and cleans up", () => {
  const elementListeners = new Map();
  const windowListeners = new Map();
  const classes = new Set();
  const bodyClasses = new Set();
  const styleValues = new Map();
  const attributes = new Map();
  const storage = new MemoryStorage([[utilityPanelWidthPreferenceKey, "500"]]);
  const shellClasses = new Set(["details-open"]);
  const separator = {
    classList: {
      add(name) { classes.add(name); },
      remove(name) { classes.delete(name); },
    },
    addEventListener(name, handler) { elementListeners.set(name, handler); },
    removeEventListener(name) { elementListeners.delete(name); },
    setAttribute(name, value) { attributes.set(name, value); },
    setPointerCapture() {},
    releasePointerCapture() {},
  };
  const detailsPanel = { getBoundingClientRect() { return { width: 500 }; } };
  const backgroundPanel = { getBoundingClientRect() { return { width: 0 }; } };
  const globalRail = { getBoundingClientRect() { return { width: 76 }; } };
  const sidebar = { getBoundingClientRect() { return { width: 296 }; } };
  const shell = {
    classList: {
      contains(name) { return shellClasses.has(name); },
    },
    style: {
      setProperty(name, value) { styleValues.set(name, value); },
      removeProperty(name) { styleValues.delete(name); },
    },
  };
  const fakeDocument = {
    body: {
      classList: {
        add(name) { bodyClasses.add(name); },
        remove(name) { bodyClasses.delete(name); },
        contains(name) { return bodyClasses.has(name); },
      },
    },
    documentElement: {
      clientWidth: 1440,
      // --utility-panel-width is set on the document root (not #appShell) so the
      // fixed-position browser preview modal, a sibling of #appShell, inherits it.
      style: {
        setProperty(name, value) { styleValues.set(name, value); },
        removeProperty(name) { styleValues.delete(name); },
      },
    },
    getElementById(id) {
      return {
        appShell: shell,
        utilityPanelResizeHandle: separator,
        conversationDetailsPanel: detailsPanel,
        backgroundTaskTray: backgroundPanel,
      }[id] || null;
    },
    querySelector(selector) {
      if (selector === ".global-rail") return globalRail;
      if (selector === ".sidebar") return sidebar;
      if (selector === ".workspace-preview-dock-mode .workspace-modal-card") return null;
      return null;
    },
  };
  const fakeWindow = {
    matchMedia(query) { return { matches: query.includes("1280") }; },
    addEventListener(name, handler) { windowListeners.set(name, handler); },
    removeEventListener(name) { windowListeners.delete(name); },
    innerWidth: 1440,
  };
  const restoreDocument = replaceGlobal("document", fakeDocument);
  const restoreWindow = replaceGlobal("window", fakeWindow);
  try {
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    const cleanup = controller.bindUtilityPanelResizer({ storage });

    // Persisted preference (500px) restored on bind, ahead of any interaction.
    assert.equal(styleValues.get("--utility-panel-width"), "500px");
    assert.equal(attributes.get("aria-valuenow"), "500");

    let keyPrevented = false;
    elementListeners.get("keydown")({ key: "ArrowLeft", shiftKey: false, preventDefault() { keyPrevented = true; } });
    assert.equal(keyPrevented, true);
    assert.equal(styleValues.get("--utility-panel-width"), "508px");
    assert.equal(storage.getItem(utilityPanelWidthPreferenceKey), "508");

    elementListeners.get("keydown")({ key: "ArrowRight", shiftKey: true, preventDefault() {} });
    assert.equal(styleValues.get("--utility-panel-width"), "484px");

    elementListeners.get("keydown")({ key: "End", preventDefault() {} });
    assert.equal(styleValues.get("--utility-panel-width"), `${maxUtilityPanelWidth}px`);

    elementListeners.get("keydown")({ key: "Home", preventDefault() {} });
    assert.equal(styleValues.get("--utility-panel-width"), `${minUtilityPanelWidth}px`);

    elementListeners.get("pointerdown")({ button: 0, pointerId: 7, clientX: 1000, preventDefault() {} });
    assert.equal(classes.has("is-dragging"), true);
    assert.equal(bodyClasses.has("utility-panel-resizing"), true);
    windowListeners.get("pointermove")({ clientX: 900, preventDefault() {} });
    windowListeners.get("pointerup")({ pointerId: 7 });
    assert.equal(styleValues.get("--utility-panel-width"), "540px");
    assert.equal(storage.getItem(utilityPanelWidthPreferenceKey), "540");
    assert.equal(classes.has("is-dragging"), false);
    assert.equal(bodyClasses.has("utility-panel-resizing"), false);

    elementListeners.get("dblclick")();
    assert.equal(styleValues.has("--utility-panel-width"), false);
    assert.equal(storage.getItem(utilityPanelWidthPreferenceKey), null);

    cleanup();
    assert.equal(elementListeners.size, 0);
    assert.equal(windowListeners.size, 0);
  } finally {
    restoreWindow();
    restoreDocument();
  }
});

test("utility panel resizer ignores drag/key input once the panel closes or the viewport narrows", () => {
  const elementListeners = new Map();
  const windowListeners = new Map();
  const styleValues = new Map();
  const storage = new MemoryStorage();
  const shellClasses = new Set(); // no panel open
  const separator = {
    classList: { add() {}, remove() {} },
    addEventListener(name, handler) { elementListeners.set(name, handler); },
    removeEventListener(name) { elementListeners.delete(name); },
    setAttribute() {},
    setPointerCapture() {},
    releasePointerCapture() {},
  };
  const shell = {
    classList: { contains(name) { return shellClasses.has(name); } },
    style: { setProperty(name, value) { styleValues.set(name, value); }, removeProperty(name) { styleValues.delete(name); } },
  };
  const fakeDocument = {
    body: { classList: { add() {}, remove() {}, contains: () => false } },
    documentElement: { clientWidth: 900 },
    getElementById(id) {
      return { appShell: shell, utilityPanelResizeHandle: separator }[id] || null;
    },
    querySelector() { return null; },
  };
  const fakeWindow = {
    matchMedia() { return { matches: false }; }, // narrower than 1280px
    addEventListener(name, handler) { windowListeners.set(name, handler); },
    removeEventListener(name) { windowListeners.delete(name); },
    innerWidth: 900,
  };
  const restoreDocument = replaceGlobal("document", fakeDocument);
  const restoreWindow = replaceGlobal("window", fakeWindow);
  try {
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    const cleanup = controller.bindUtilityPanelResizer({ storage });
    assert.equal(styleValues.has("--utility-panel-width"), false);

    elementListeners.get("keydown")({ key: "ArrowLeft", shiftKey: false, preventDefault() { throw new Error("should not preventDefault while closed/narrow"); } });
    elementListeners.get("pointerdown")({ button: 0, pointerId: 1, clientX: 500, preventDefault() {} });
    assert.equal(styleValues.has("--utility-panel-width"), false);
    cleanup();
  } finally {
    restoreWindow();
    restoreDocument();
  }
});

test("entering the settings shell also hides the utility panel resize handle", () => {
  const makeNode = (id) => {
    const attrs = new Map();
    return {
      id,
      style: {
        props: new Map(),
        setProperty(name, value) { this.props.set(name, value); },
        removeProperty(name) { this.props.delete(name); },
        getPropertyValue(name) { return this.props.get(name) ?? ""; },
        getPropertyPriority() { return ""; },
      },
      classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
      getAttribute: (name) => (attrs.has(name) ? attrs.get(name) : null),
      setAttribute: (name, value) => attrs.set(name, value),
      removeAttribute: (name) => attrs.delete(name),
      hasAttribute: (name) => attrs.has(name),
      querySelector: () => null,
      querySelectorAll: () => [],
      children: [],
      appendChild(child) { child.parentNode = this; this.children.push(child); return child; },
      insertBefore(child, ref) { child.parentNode = this; this.children.splice(this.children.indexOf(ref), 0, child); return child; },
    };
  };
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

  const ids = ["sessionSidebar", "sidebarResizeHandle", "overviewDashboard", "conversationPanel", "workbenchPanel", "schedulePanel", "terminalPanel", "conversationDetailsPanel", "backgroundTaskTray", "expandTerminalBtn", "utilityPanelResizeHandle"];
  const nodes = { appShell, settingsModal: modal };
  for (const id of ids) nodes[id] = makeNode(id);

  const state = { settingsShellOpen: false, activeWorkbench: "conversation", settingsMobileViewport: false, mobileSettingsView: "detail" };
  const helpers = createSettingsShellHelpers({
    state,
    isMobileAppViewport: () => false,
    selectSettingsPanel: () => {},
    renderSettingsNav: () => {},
    renderMobileSettingsIndex: () => {},
    syncSettingsCloseControl: () => {},
    saveCurrentChatDraft: () => {},
    hideSlashCommandPalette: () => {},
    closeMobileSidebar: () => {},
    applyPrimaryWorkbench: () => {},
  });
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById: (id) => nodes[id] ?? null,
    body: { classList: { toggle() {}, add() {}, remove() {}, contains: () => false } },
    documentElement: makeNode("html"),
  };
  try {
    helpers.enterSettingsShell();
    const handle = nodes.utilityPanelResizeHandle;
    assert.equal(handle.style.getPropertyValue("display"), "none");
    assert.equal(handle.getAttribute("aria-hidden"), "true");
    helpers.exitSettingsShell();
    assert.equal(handle.style.getPropertyValue("display"), "");
    assert.equal(handle.getAttribute("aria-hidden"), null);
  } finally {
    globalThis.document = previousDocument;
  }
});
