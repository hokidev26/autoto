import test from "node:test";
import assert from "node:assert/strict";

import { createMessageContextMenu } from "./message-context-menu.mjs";

function fakeMenuElement() {
  const classes = new Set(["hidden"]);
  const attributes = new Map();
  const items = new Map(["copy", "edit", "rollback", "fork", "compress", "delete"].map((action) => [action, {
    action,
    hidden: false,
    textContent: "",
    focused: false,
    focus() { this.focused = true; },
  }]));
  return {
    items,
    style: {},
    offsetWidth: 180,
    offsetHeight: 200,
    classList: {
      add: (name) => classes.add(name),
      remove: (name) => classes.delete(name),
      contains: (name) => classes.has(name),
    },
    setAttribute: (name, value) => attributes.set(name, value),
    getAttribute: (name) => attributes.get(name),
    querySelector(selector) {
      const match = /data-message-menu-action="([a-z]+)"/.exec(selector);
      return match ? items.get(match[1]) || null : null;
    },
  };
}

function fakeMessageRow({ id = "msg-1", role = "user", superseded = false, copyIndex = 0 } = {}) {
  return {
    dataset: { messageId: id, messageRole: role },
    classList: { contains: (name) => name === "message-superseded" && superseded },
    querySelector: (selector) => (selector === "[data-copy-message]" ? { dataset: { copyMessage: String(copyIndex) } } : null),
    getBoundingClientRect: () => ({ left: 40, bottom: 60 }),
  };
}

function withMenuEnvironment(run) {
  const previousDocument = globalThis.document;
  const previousWindow = globalThis.window;
  const menu = fakeMenuElement();
  const messagesContainer = {
    dataset: {},
    contains: () => true,
    querySelector: () => null,
    addEventListener: () => {},
  };
  globalThis.document = {
    getElementById: (id) => (id === "messageContextMenu" ? menu : id === "messages" ? messagesContainer : null),
    documentElement: { clientWidth: 1024, clientHeight: 768 },
  };
  globalThis.window = {
    innerWidth: 1024,
    innerHeight: 768,
    setTimeout: () => 0,
    clearTimeout: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
  };
  const restore = () => {
    globalThis.document = previousDocument;
    globalThis.window = previousWindow;
  };
  try {
    const result = run({ menu, messagesContainer });
    // Async runs must keep the stubbed globals alive until they settle.
    if (result && typeof result.then === "function") return result.finally(restore);
    restore();
    return result;
  } catch (error) {
    restore();
    throw error;
  }
}

function makeController(overrides = {}) {
  const calls = { requests: [], toasts: [], errors: [], confirms: [], copied: [], edited: [], loadedMessages: [], forked: [] };
  const state = {
    agent: { id: "agent-1", entityGeneration: 3 },
    currentMessages: [{ id: "msg-1", contentText: "fallback text" }],
    messageCopyTexts: ["copy text"],
    ...overrides.state,
  };
  const controller = createMessageContextMenu({
    state,
    request: overrides.request || (async (path, options = {}) => {
      calls.requests.push({ path, method: options.method || "GET", body: options.body });
      return overrides.response ?? {};
    }),
    showToast: (message, variant) => calls.toasts.push({ message, variant }),
    showError: (error) => calls.errors.push(error),
    confirmAction: async (message) => {
      calls.confirms.push(message);
      return overrides.confirmResult ?? true;
    },
    copyToClipboard: async (text) => {
      calls.copied.push(text);
      return true;
    },
    openCorrectionEditor: (id) => calls.edited.push(id),
    loadMessages: async (agentId) => calls.loadedMessages.push(agentId),
    onForkCreated: async (agent) => calls.forked.push(agent),
  });
  return { controller, calls, state };
}

test("opening the menu on a user message shows every action and fills labels", () => {
  withMenuEnvironment(({ menu }) => {
    const { controller, state } = makeController();
    const opened = controller.openMessageContextMenu(fakeMessageRow(), { clientX: 100, clientY: 120 });
    assert.equal(opened, true);
    assert.equal(menu.classList.contains("hidden"), false);
    assert.deepEqual(state.messageMenuTarget, { id: "msg-1", role: "user", superseded: false, copyIndex: 0 });
    for (const item of menu.items.values()) {
      assert.equal(item.hidden, false);
      assert.notEqual(item.textContent, "");
    }
  });
});

test("a superseded assistant message only offers copy and delete", () => {
  withMenuEnvironment(({ menu }) => {
    const { controller } = makeController();
    controller.openMessageContextMenu(fakeMessageRow({ role: "assistant", superseded: true }), {});
    assert.equal(menu.items.get("copy").hidden, false);
    assert.equal(menu.items.get("delete").hidden, false);
    for (const action of ["edit", "rollback", "fork", "compress"]) {
      assert.equal(menu.items.get(action).hidden, true, `${action} should be hidden`);
    }
  });
});

test("edit stays hidden on assistant messages that are still current", () => {
  withMenuEnvironment(({ menu }) => {
    const { controller } = makeController();
    controller.openMessageContextMenu(fakeMessageRow({ role: "assistant" }), {});
    assert.equal(menu.items.get("edit").hidden, true);
    assert.equal(menu.items.get("rollback").hidden, false);
  });
});

test("copy prefers the indexed transcript text and edit opens the correction editor", async () => {
  await withMenuEnvironment(async ({ menu }) => {
    const { controller, calls } = makeController();
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("copy");
    assert.deepEqual(calls.copied, ["copy text"]);
    assert.equal(menu.classList.contains("hidden"), true);

    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("edit");
    assert.deepEqual(calls.edited, ["msg-1"]);
    assert.deepEqual(calls.requests, []);
  });
});

test("rollback confirms, posts to the rollback endpoint, and reloads messages", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController();
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("rollback");
    assert.equal(calls.confirms.length, 1);
    assert.deepEqual(calls.requests, [{ path: "/api/agents/agent-1/messages/msg-1/rollback", method: "POST", body: "{}" }]);
    assert.deepEqual(calls.loadedMessages, ["agent-1"]);
  });
});

test("a declined confirmation leaves the conversation untouched", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController({ confirmResult: false });
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("delete");
    assert.deepEqual(calls.requests, []);
    assert.deepEqual(calls.loadedMessages, []);
  });
});

test("fork confirms first, posts to the fork endpoint, and hands the new agent to the navigator", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController({ response: { agent: { id: "fork-1", title: "Fork" } } });
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("fork");
    assert.equal(calls.confirms.length, 1, "fork shows a reminder before creating anything");
    assert.equal(calls.requests[0].path, "/api/agents/agent-1/messages/msg-1/fork");
    assert.deepEqual(calls.forked, [{ id: "fork-1", title: "Fork" }]);
  });
});

test("a declined fork reminder creates nothing", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController({ confirmResult: false });
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("fork");
    assert.deepEqual(calls.requests, []);
    assert.deepEqual(calls.forked, []);
  });
});

test("compress sends the entity generation and the through-message id", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController({ response: { compacted: true } });
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("compress");
    assert.equal(calls.requests[0].path, "/api/agents/agent-1/context/compact");
    assert.deepEqual(JSON.parse(calls.requests[0].body), { entityGeneration: 3, throughMessageId: "msg-1" });
  });
});

test("delete issues a DELETE on the message and reloads", async () => {
  await withMenuEnvironment(async () => {
    const { controller, calls } = makeController();
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("delete");
    assert.deepEqual(calls.requests, [{ path: "/api/agents/agent-1/messages/msg-1", method: "DELETE", body: undefined }]);
    assert.deepEqual(calls.loadedMessages, ["agent-1"]);
  });
});

test("a failed action surfaces through showError instead of throwing", async () => {
  await withMenuEnvironment(async () => {
    const failure = new Error("boom");
    const { controller, calls } = makeController({
      request: async () => { throw failure; },
    });
    controller.openMessageContextMenu(fakeMessageRow(), {});
    await controller.applyMessageMenuAction("rollback");
    assert.deepEqual(calls.errors, [failure]);
  });
});

test("the context menu handler ignores clicks outside message rows and editors", () => {
  withMenuEnvironment(({ menu }) => {
    const { controller } = makeController();
    let prevented = false;
    controller.handleMessageContextMenu({
      target: { closest: () => null },
      preventDefault: () => { prevented = true; },
      stopPropagation: () => {},
    });
    assert.equal(prevented, false);
    assert.equal(menu.classList.contains("hidden"), true);

    // Inside a textarea the native menu must stay usable.
    const row = fakeMessageRow();
    controller.handleMessageContextMenu({
      target: { closest: (selector) => (selector.includes("textarea") ? {} : row) },
      preventDefault: () => { prevented = true; },
      stopPropagation: () => {},
    });
    assert.equal(prevented, false);
    assert.equal(menu.classList.contains("hidden"), true);
  });
});
