import test from "node:test";
import assert from "node:assert/strict";

globalThis.window = { AUTOTO_LOCAL_TOKEN: "" };
globalThis.location = { origin: "http://localhost", protocol: "http:", host: "localhost" };

const { createChatComposerController } = await import("./chat-composer.mjs");

// Editing a parked message used to DELETE its queue row and repost the text. The
// attachment bytes only exist server-side with that row, and the composer holds
// metadata rather than File objects, so the repost could not carry them: the
// files were destroyed by the act of editing. A row with attachments is now
// rewritten in place through PUT, which leaves them attached.

function harness({ attachments = [], composerValue = "" } = {}) {
  const previousDocument = globalThis.document;
  const previousLocalStorage = globalThis.localStorage;
  const store = new Map();
  globalThis.localStorage = {
    getItem: (key) => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => store.set(key, String(value)),
    removeItem: (key) => store.delete(key),
  };
  const input = {
    value: composerValue,
    focus() {},
    setSelectionRange() {},
    setAttribute() {},
    removeAttribute() {},
    style: {},
  };
  const queueHost = {
    innerHTML: "",
    classList: { toggle() {}, add() {}, remove() {} },
    querySelectorAll: () => [],
    querySelector: () => null,
  };
  globalThis.document = {
    getElementById: (id) => ({ messageText: input, messageQueue: queueHost }[id] || null),
  };
  const state = {
    agent: { id: "agent-1" },
    navigationSelectionKind: "conversation",
    promptHistory: [],
    pendingAttachments: [],
    messageQueue: [{
      id: "queued-1",
      agentId: "agent-1",
      text: "original text",
      mode: "execute",
      context: "conversation",
      attachments,
    }],
  };
  const calls = [];
  const toasts = [];
  let serverQueue = state.messageQueue.map((row) => ({ ...row }));
  const controller = createChatComposerController({
    state,
    // The resync after an edit reads the queue back. Returning an empty list here
    // would wipe the row locally and hide whether the edit preserved it, so this
    // mirrors the server: the row survives a PUT, carrying its attachments, and
    // reflects the new text.
    request: async (path, options = {}) => {
      const method = options.method || "GET";
      calls.push({ path, method, body: options.body });
      if (method === "PUT" && path.includes("/queue/")) {
        const body = JSON.parse(String(options.body || "{}"));
        serverQueue = serverQueue.map((row) => (path.endsWith(`/${row.id}`) ? { ...row, text: body.text } : row));
        return serverQueue.find((row) => path.endsWith(`/${row.id}`)) || {};
      }
      if (method === "DELETE" && path.includes("/queue/")) {
        serverQueue = serverQueue.filter((row) => !path.endsWith(`/${row.id}`));
        return {};
      }
      // The endpoint returns an object with a queue property, not a bare array.
      if (path.includes("/queue") && method === "GET") return { queue: serverQueue };
      return {};
    },
    showToast: (message, tone) => toasts.push({ message, tone }),
  });
  return {
    state, controller, input, calls, toasts,
    restore: () => {
      globalThis.document = previousDocument;
      globalThis.localStorage = previousLocalStorage;
    },
  };
}

const oneAttachment = [{ id: "", filename: "diagram.png", kind: "image", mimeType: "image/png", sizeBytes: 2048 }];

test("editing a parked message with attachments never deletes its queue row", async () => {
  const h = harness({ attachments: oneAttachment });
  try {
    h.controller.editQueuedMessage("queued-1");
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(
      h.calls.some((call) => call.method === "DELETE"),
      false,
      "the row holds the only copy of the files, so deleting it destroys them",
    );
    // The row stays parked so the files stay with it.
    assert.equal(h.state.messageQueue.length, 1);
    assert.equal(h.state.messageQueue[0].attachments.length, 1);
    // The text is loaded for rewriting.
    assert.equal(h.input.value, "original text");
  } finally {
    h.restore();
  }
});

test("sending the edit rewrites the parked row in place and keeps the attachments", async () => {
  const h = harness({ attachments: oneAttachment });
  try {
    h.controller.editQueuedMessage("queued-1");
    h.input.value = "corrected text";
    await h.controller.sendMessage({ preventDefault() {} });
    const put = h.calls.find((call) => call.method === "PUT");
    assert.ok(put, `expected a PUT to update the parked row, got ${JSON.stringify(h.calls)}`);
    assert.match(put.path, /\/queue\/queued-1$/);
    assert.deepEqual(JSON.parse(put.body), { text: "corrected text" });
    assert.equal(h.calls.some((call) => call.method === "DELETE"), false);
    const row = h.state.messageQueue.find((entry) => entry.id === "queued-1");
    assert.equal(row.text, "corrected text");
    assert.equal(row.attachments.length, 1, "attachments must survive the edit");
    assert.equal(h.input.value, "", "the composer is emptied by a committed edit");
  } finally {
    h.restore();
  }
});

test("a parked message with no attachments keeps the original edit behaviour", async () => {
  const h = harness({ attachments: [] });
  try {
    h.controller.editQueuedMessage("queued-1");
    await new Promise((resolve) => setImmediate(resolve));
    // Nothing is lost by pulling this row back, so it still leaves the queue and
    // the text returns to the composer for a fresh send.
    assert.equal(h.calls.some((call) => call.method === "DELETE"), true);
    assert.equal(h.state.messageQueue.length, 0);
    assert.equal(h.input.value, "original text");
  } finally {
    h.restore();
  }
});

test("an edit is refused while the composer holds an unrelated draft", async () => {
  const h = harness({ attachments: oneAttachment, composerValue: "half-typed thought" });
  try {
    h.controller.editQueuedMessage("queued-1");
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(h.input.value, "half-typed thought", "the draft must not be overwritten");
    assert.equal(h.state.messageQueue.length, 1);
    assert.ok(h.toasts.some((toast) => toast.tone === "warn"), "the refusal has to be explained");
  } finally {
    h.restore();
  }
});
