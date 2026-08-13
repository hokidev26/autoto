import assert from "node:assert/strict";
import test from "node:test";

import { t } from "./i18n.mjs";

// app-main.mjs is the live orchestrator: importing it evaluates the wiring.
// These tests only need the exported helpers, so the DOM/network stubs exist
// solely to let the module finish loading. They must be in place before import.

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

function fakeNode(tag = "div", id = "") {
  const classes = new Set();
  const attrs = new Map();
  const children = [];
  const dataset = {};
  const node = {
    id,
    tagName: String(tag).toUpperCase(),
    nodeType: 1,
    value: "",
    textContent: "",
    innerHTML: "",
    disabled: false,
    checked: false,
    hidden: false,
    type: "",
    name: "",
    href: "",
    download: "",
    title: "",
    placeholder: "",
    options: [],
    children,
    parentNode: null,
    parentElement: null,
    ownerDocument: null,
    isConnected: true,
    style: {},
    dataset,
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      toggle(name, force) {
        if (force === true) classes.add(name);
        else if (force === false) classes.delete(name);
        else if (classes.has(name)) classes.delete(name);
        else classes.add(name);
        return classes.has(name);
      },
      contains: (name) => classes.has(name),
    },
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return true; },
    setAttribute(name, value) {
      attrs.set(name, String(value));
      if (name.startsWith("data-")) {
        const key = name.slice(5).replace(/-([a-z])/g, (_, ch) => ch.toUpperCase());
        dataset[key] = String(value);
      }
    },
    getAttribute(name) { return attrs.has(name) ? attrs.get(name) : null; },
    hasAttribute(name) { return attrs.has(name); },
    removeAttribute(name) { attrs.delete(name); },
    querySelector() { return fakeNode("span"); },
    querySelectorAll() { return []; },
    closest() { return null; },
    matches() { return false; },
    contains() { return false; },
    focus() {},
    blur() {},
    click() {},
    select() {},
    remove() {},
    appendChild(child) {
      children.push(child);
      if (child) {
        child.parentNode = this;
        child.parentElement = this;
      }
      return child;
    },
    append(...nodes) { nodes.forEach((item) => this.appendChild(item)); },
    replaceChildren() { children.length = 0; },
    insertAdjacentHTML() {},
    insertAdjacentElement() { return null; },
    getBoundingClientRect() {
      return { top: 0, left: 0, right: 0, bottom: 0, width: 0, height: 0, x: 0, y: 0 };
    },
    scrollTo() {},
    setPointerCapture() {},
    releasePointerCapture() {},
    scrollHeight: 0,
    clientHeight: 0,
    clientWidth: 1024,
    scrollTop: 0,
    scrollLeft: 0,
    scrollWidth: 0,
  };
  Object.defineProperty(node, "className", {
    get() { return [...classes].join(" "); },
    set(value) {
      classes.clear();
      String(value || "").split(/\s+/).filter(Boolean).forEach((name) => classes.add(name));
    },
  });
  return node;
}

function installAppMainHarness() {
  const nodes = new Map();
  const body = fakeNode("body");
  const root = fakeNode("html");
  root.lang = "zh-Hans-CN";
  const head = fakeNode("head");
  const storage = new MemoryStorage();
  const location = {
    protocol: "http:",
    host: "127.0.0.1:16888",
    hostname: "127.0.0.1",
    origin: "http://127.0.0.1:16888",
    href: "http://127.0.0.1:16888/",
    pathname: "/",
    search: "",
    hash: "",
  };
  const document = {
    nodeType: 9,
    title: "Autoto",
    body,
    documentElement: root,
    head,
    hidden: false,
    visibilityState: "visible",
    readyState: "complete",
    activeElement: null,
    getElementById(id) {
      const key = String(id || "");
      if (!nodes.has(key)) nodes.set(key, fakeNode("div", key));
      return nodes.get(key);
    },
    querySelector() { return null; },
    querySelectorAll() { return []; },
    createElement(tag) { return fakeNode(tag); },
    createElementNS(_ns, tag) { return fakeNode(tag); },
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return true; },
  };
  const window = {
    AUTOTO_DESKTOP_SHELL: "",
    innerWidth: 1280,
    innerHeight: 800,
    devicePixelRatio: 1,
    location,
    localStorage: storage,
    sessionStorage: new MemoryStorage(),
    document,
    navigator: { onLine: true, language: "zh-CN", userAgent: "node" },
    matchMedia(query) {
      return {
        matches: false,
        media: String(query || ""),
        addEventListener() {},
        removeEventListener() {},
        addListener() {},
        removeListener() {},
      };
    },
    addEventListener() {},
    removeEventListener() {},
    dispatchEvent() { return true; },
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
    setInterval: globalThis.setInterval.bind(globalThis),
    clearInterval: globalThis.clearInterval.bind(globalThis),
    requestAnimationFrame: (cb) => globalThis.setTimeout(cb, 0),
    cancelAnimationFrame: (id) => globalThis.clearTimeout(id),
    queueMicrotask: globalThis.queueMicrotask.bind(globalThis),
    getComputedStyle() {
      return { display: "block", visibility: "visible", getPropertyValue: () => "" };
    },
    confirm: () => true,
    alert() {},
    open: () => null,
    fetch: async () => ({
      ok: true,
      status: 200,
      statusText: "OK",
      json: async () => ({}),
      text: async () => "{}",
    }),
  };
  window.window = window;
  document.defaultView = window;
  body.ownerDocument = document;
  root.ownerDocument = document;

  const defineGlobal = (name, value) => {
    try {
      globalThis[name] = value;
    } catch {
      Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
    }
  };
  defineGlobal("window", window);
  defineGlobal("document", document);
  defineGlobal("localStorage", storage);
  defineGlobal("sessionStorage", window.sessionStorage);
  defineGlobal("location", location);
  defineGlobal("navigator", window.navigator);
  defineGlobal("matchMedia", window.matchMedia);
  defineGlobal("fetch", window.fetch);
  defineGlobal("alert", window.alert);
  defineGlobal("confirm", window.confirm);
  if (typeof globalThis.WebSocket !== "function") {
    globalThis.WebSocket = class {
      constructor() { this.readyState = 3; }
      addEventListener() {}
      removeEventListener() {}
      send() {}
      close() {}
    };
  }
  if (typeof URL.createObjectURL !== "function") {
    URL.createObjectURL = () => "blob:test";
    URL.revokeObjectURL = () => {};
  }
  return { document, nodes, storage };
}

const harness = installAppMainHarness();

const {
  applySettingsNavOrder,
  backgroundTaskNoticeLabel,
  connectionMobileLabel,
  continuationBlockedNoticeMessage,
  conversationDetailMetrics,
  conversationTitleForNotice,
  executionNoticeMessage,
  normalizeStoredPermissionMode,
  permissionLabel,
  permissionMobileLabel,
  renderEmptyWorkspaceCard,
  state,
} = await import("./app-main.mjs");

function resetNoticeState() {
  state.agent = null;
  state.recentConversations = [];
  state.currentMessages = [];
  state.activeRunSummary = null;
  state.terminalWS = null;
  state.workspacePreviewStatus = null;
}

test("settings nav order rearranges known keys and leaves unknown items at the end", () => {
  // A saved order used to be ignored, so a user who dragged Providers below Models
  // saw the original sequence again after reload.
  const items = [{ key: "a" }, { key: "b" }, { key: "c" }, { key: "d" }];
  const ordered = applySettingsNavOrder(items, ["c", "a"]);
  assert.deepEqual(ordered.map((item) => item.key), ["c", "a", "b", "d"]);
  assert.deepEqual(items.map((item) => item.key), ["a", "b", "c", "d"], "the source list must not be mutated");
  assert.equal(applySettingsNavOrder(items, null), items);
  assert.equal(applySettingsNavOrder(items, []), items);
});

test("stored dontAsk permission is coerced to default so the retired mode cannot reappear", () => {
  // Agents saved before dontAsk was removed still carry it; the backend treats it
  // as default, and showing a dead option made the picker look broken.
  assert.equal(normalizeStoredPermissionMode("dontAsk"), "default");
  assert.equal(normalizeStoredPermissionMode("acceptEdits"), "acceptEdits");
  assert.equal(normalizeStoredPermissionMode("  bypassPermissions "), "bypassPermissions");
  assert.equal(normalizeStoredPermissionMode(""), "");
});

test("permission labels keep the four live modes and map compact mobile codes", () => {
  // The composer pill used to show raw enum values on a phone, which overflowed
  // the toolbar and hid the model name next to it.
  assert.equal(permissionLabel("readOnly"), t("chat.permission.readOnly"));
  assert.equal(permissionLabel("acceptEdits"), t("chat.permission.editable"));
  assert.equal(permissionLabel("bypassPermissions"), t("chat.permission.allowAll"));
  assert.equal(permissionLabel("default"), t("chat.permission.automatic"));
  assert.equal(permissionLabel(""), t("chat.permission.automatic"));
  assert.equal(permissionLabel("mystery"), "mystery");
  assert.equal(permissionMobileLabel("readOnly"), "RO");
  assert.equal(permissionMobileLabel("acceptEdits"), "RW");
  assert.equal(permissionMobileLabel("bypassPermissions"), "ALL");
  assert.equal(permissionMobileLabel("default"), "AUTO");
  assert.equal(permissionMobileLabel("dontAsk"), "NA");
  assert.equal(permissionMobileLabel("nope"), "AUTO");
});

test("connection badge distinguishes LAN from restricted and open tunnels", () => {
  // The header used a single "remote" word for both a hardened tunnel and a
  // full-access one, so a restricted session looked identical to an open one.
  assert.equal(connectionMobileLabel(null), "LAN");
  assert.equal(connectionMobileLabel({ remote: false }), "LAN");
  assert.equal(connectionMobileLabel({ remote: true, restricted: true }), "T−");
  assert.equal(connectionMobileLabel({ remote: true, restricted: false }), "T+");
});

test("empty workspace card escapes untrusted copy and keeps the directory shortcut", () => {
  // Folder names and help copy come from the project; without escaping, a path
  // like `<img>` would execute inside the empty-state card.
  const html = renderEmptyWorkspaceCard({
    title: `<img src=x onerror=alert(1)>`,
    text: `a & b`,
    action: `"click"`,
    hint: `</div><script>x()</script>`,
    icon: `<svg>`,
  });
  assert.match(html, /class="empty-workspace-card"/);
  assert.match(html, /data-open-directory-shortcut="new"/);
  assert.match(html, /&lt;img src=x onerror=alert\(1\)&gt;/);
  assert.match(html, /a &amp; b/);
  assert.match(html, /&lt;\/div&gt;&lt;script&gt;x\(\)&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<img src=x/);
  assert.doesNotMatch(html, /<script>/);
});

test("conversation metrics prefer the run-summary cache field the API actually sends", () => {
  // cachedInputTokens is the live field. The two older names were never returned,
  // so the details panel always showed a cache of 0 even on a warm prompt.
  resetNoticeState();
  state.currentMessages = [{ id: "1" }, { id: "2" }];
  state.activeRunSummary = {
    costUsd: "1.25",
    inputTokens: "10",
    outputTokens: 4,
    cachedInputTokens: 7,
    cacheReadTokens: 99,
    cachedTokens: 88,
    toolCallCount: 3,
    toolCalls: [{}, {}],
    pendingApprovals: 1,
  };
  state.workspacePreviewStatus = { status: "ready" };
  const metrics = conversationDetailMetrics();
  assert.equal(metrics.messages, 2);
  assert.equal(metrics.cost, 1.25);
  assert.equal(metrics.inputTokens, 10);
  assert.equal(metrics.outputTokens, 4);
  assert.equal(metrics.cacheTokens, 7);
  assert.equal(metrics.tools, 3);
  assert.equal(metrics.browser, 1);
  assert.equal(metrics.approvals, 1);
});

test("conversation metrics fall back to toolCalls length and treat a live terminal as present", () => {
  // A summary without toolCallCount still ran tools; dropping them made a busy
  // turn look idle. A connected socket is enough to mark the terminal live.
  resetNoticeState();
  state.currentMessages = "not-an-array";
  state.activeRunSummary = { toolCalls: [{}, {}, {}] };
  state.terminalWS = { readyState: 1 };
  harness.nodes.get("terminalOutput").textContent = "";
  const metrics = conversationDetailMetrics();
  assert.equal(metrics.messages, 0);
  assert.equal(metrics.tools, 3);
  assert.equal(metrics.terminal, 1);
  assert.equal(metrics.cacheTokens, 0);
  assert.equal(metrics.browser, 0);
});

test("a finished background task is named by title or conversation, never by its id", () => {
  // The toast used to fall back to a hex task id, which wrapped onto a second
  // line and named nothing a person could recognise.
  resetNoticeState();
  state.agent = { id: "agent-1", title: "Release plan" };
  assert.equal(
    backgroundTaskNoticeLabel({ agentId: "agent-1" }, { title: "Compile docs" }, {}),
    "Compile docs",
  );
  assert.equal(
    backgroundTaskNoticeLabel({ agentId: "agent-1" }, {}, { title: "From data" }),
    "From data",
  );
  assert.equal(
    backgroundTaskNoticeLabel({ agentId: "agent-1" }, {}, {}),
    "Release plan",
  );
  state.agent = { id: "other", title: "Other" };
  state.recentConversations = [{ agentId: "agent-2", title: "Sidebar chat" }];
  assert.equal(
    backgroundTaskNoticeLabel({ agentId: "agent-2" }, {}, {}),
    "Sidebar chat",
  );
  assert.equal(
    backgroundTaskNoticeLabel({ agentId: "missing" }, { taskId: "abc123def" }, {}),
    t("backgroundTasks.task"),
  );
  assert.equal(conversationTitleForNotice({ agentId: "" }), "");
});

test("continuation-blocked copy depends on the reason code, not the raw English reason", () => {
  // "you sent another message" used to share the failure sentence, so the most
  // common (and expected) case read as an error report.
  assert.equal(
    continuationBlockedNoticeMessage({ reasonCode: "preempted" }, {}),
    t("backgroundTasks.continuation.preempted"),
  );
  assert.equal(
    continuationBlockedNoticeMessage({ reasonCode: "interrupted" }, {}),
    t("backgroundTasks.continuation.stopped"),
  );
  assert.equal(
    continuationBlockedNoticeMessage({ reason: "provider 429" }, { reasonCode: "" }),
    t("backgroundTasks.continuation.blocked", { reason: "provider 429" }),
  );
});

test("execution notices pick family-specific copy and keep task titles out of the id", () => {
  // A task_terminal toast that interpolated the id again undid the label fix.
  resetNoticeState();
  state.agent = { id: "agent-1", title: "Main chat" };
  assert.equal(
    executionNoticeMessage({ family: "task_terminal", agentId: "agent-1", raw: { title: "Nightly" } }),
    t("backgroundTasks.notifications.taskCompleted", { task: "Nightly" }),
  );
  assert.equal(
    executionNoticeMessage({ family: "continuation_blocked", reasonCode: "preempted", raw: {} }),
    t("backgroundTasks.continuation.preempted"),
  );
  assert.equal(
    executionNoticeMessage({ family: "budget_exhausted", reason: "max turns", raw: {} }),
    t("backgroundTasks.continuation.budgetExhausted", { reason: "max turns" }),
  );
  assert.equal(executionNoticeMessage({ family: "approval_required", raw: {} }), t("backgroundTasks.notifications.approvalRequired"));
  assert.equal(executionNoticeMessage({ family: "completed", raw: {} }), t("backgroundTasks.notifications.completed"));
  assert.equal(executionNoticeMessage({ family: "error", raw: {} }), t("backgroundTasks.notifications.error"));
  assert.equal(executionNoticeMessage({ family: "interrupted", raw: {} }), t("backgroundTasks.notifications.interrupted"));
  assert.equal(executionNoticeMessage({ family: "unknown", raw: {} }), t("backgroundTasks.notifications.truncated"));
});
