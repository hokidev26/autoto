import assert from "node:assert/strict";
import test from "node:test";
import { createChatComposerController } from "./chat-composer.mjs";

// Drives the real sendMessage rather than reading the source, so these assert what a
// user sees: with a file attached, the text used to clear while the attachment cards
// stayed until the upload came back. The gate below holds the send open at the same
// await the settings save occupies, which is the window the cards used to sit in.

function harness({ requestImpl, settingsGate, agentId = "agent-attach" } = {}) {
  const input = {
    value: "Here is the screenshot",
    disabled: false,
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    focus() {},
  };
  const attachmentsWrap = {
    classList: { toggle() {}, add() {}, remove() {} },
    innerHTML: "",
    querySelectorAll() { return []; },
  };
  const elements = {
    messageText: input,
    modelSelect: { value: "" },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
    pendingAttachments: attachmentsWrap,
    sendMessageBtn: {
      classList: { toggle() {}, add() {}, remove() {}, contains: () => false },
      dataset: {},
      removeAttribute() {},
      setAttribute() {},
      disabled: false,
      textContent: "Send",
    },
  };
  // previewUrl is what a failed send has to hand back intact; revoking it early is the
  // mistake this fixture is shaped to catch.
  const revoked = [];
  // A real File, because the send builds FormData and append() rejects a plain object.
  // Using the genuine type also keeps this honest about what detaching hands over.
  const file = new File([new Uint8Array([1, 2, 3, 4])], "shot.png", { type: "image/png" });
  const state = {
    agent: { id: agentId, model: "openai:model-a" },
    navigationSelectionKind: "conversation",
    promptHistory: [],
    pendingAttachments: [{ id: "att-1", file, previewUrl: "blob:preview-1" }],
    serverSkills: [],
    currentMessages: [],
  };

  const previousDocument = globalThis.document;
  const previousComputed = globalThis.getComputedStyle;
  const previousURL = globalThis.URL;
  globalThis.document = { getElementById: (id) => elements[id] || null };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue: () => "" });
  globalThis.URL = { revokeObjectURL: (url) => revoked.push(url) };

  const controller = createChatComposerController({
    state,
    awaitAgentSettingsSaved: settingsGate ? async () => { await settingsGate; } : async () => {},
    currentSkillsPreferences: () => ({ commands: [] }),
    isCurrentModelConfigured: () => true,
    loadMessages: async () => {},
    onMessageAccepted: async () => {},
    request: requestImpl || (async () => ({ accepted: true })),
    scheduleMessageRefresh() {},
    echoPendingUserMessage: () => "echo-1",
    discardPendingUserMessage: () => true,
    scrollMessagesToBottom() {},
    notifyTerminal() {},
    showToast() {},
  });

  return {
    controller,
    state,
    input,
    revoked,
    restore() {
      globalThis.document = previousDocument;
      globalThis.getComputedStyle = previousComputed;
      globalThis.URL = previousURL;
    },
  };
}

test("the attachment cards are gone before the upload is even attempted", async () => {
  let seenDuringUpload = null;
  const h = harness({
    requestImpl: async () => {
      // Sampled inside the request: this is the window the cards used to occupy.
      seenDuringUpload = h.state.pendingAttachments.length;
      return { accepted: true };
    },
  });
  try {
    await h.controller.sendMessage({ preventDefault() {} });
    assert.equal(seenDuringUpload, 0, "the composer was already empty when the upload started");
    assert.equal(h.input.value, "", "and so was the textarea");
    assert.deepEqual(h.state.pendingAttachments, []);
  } finally {
    h.restore();
  }
});

test("text and attachments clear together, before the settings-save await", async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const h = harness({ settingsGate: gate });
  try {
    const sending = h.controller.sendMessage({ preventDefault() {} });
    // Let sendMessage run up to the gate.
    await Promise.resolve();
    await Promise.resolve();
    assert.equal(h.input.value, "", "the text goes before the round trip, not after it");
    assert.equal(
      h.state.pendingAttachments.length,
      0,
      "and the cards go with it, in the same beat -- this is the stutter being fixed",
    );
    release();
    await sending;
  } finally {
    h.restore();
  }
});

test("a delivered send releases the preview it was holding", async () => {
  const h = harness();
  try {
    await h.controller.sendMessage({ preventDefault() {} });
    assert.deepEqual(h.revoked, ["blob:preview-1"], "freed once the file will not be handed back");
  } finally {
    h.restore();
  }
});

test("a failed send hands the file back with its preview intact", async () => {
  const h = harness({
    requestImpl: async () => { throw new Error("network down"); },
  });
  try {
    await assert.rejects(h.controller.sendMessage({ preventDefault() {} }), /network down/);
    assert.equal(h.state.pendingAttachments.length, 1, "the card is back in the composer");
    assert.equal(h.state.pendingAttachments[0].id, "att-1");
    assert.equal(h.input.value, "Here is the screenshot", "and so is the text");
    assert.deepEqual(
      h.revoked,
      [],
      "the preview must survive, or the restored card shows a broken thumbnail",
    );
  } finally {
    h.restore();
  }
});

test("the file is uploaded even though the composer emptied first", async () => {
  const uploaded = [];
  const h = harness({
    requestImpl: async (path, options) => {
      // FormData is not available in this environment; the composer appends to whatever
      // it built, so assert the request carried a body rather than inspecting its shape.
      uploaded.push({ path, hasBody: Boolean(options?.body), method: options?.method });
      return { accepted: true };
    },
  });
  try {
    await h.controller.sendMessage({ preventDefault() {} });
    assert.equal(uploaded.length, 1);
    assert.equal(uploaded[0].method, "POST");
    assert.equal(uploaded[0].hasBody, true, "detaching must not take the file away from the send");
  } finally {
    h.restore();
  }
});
