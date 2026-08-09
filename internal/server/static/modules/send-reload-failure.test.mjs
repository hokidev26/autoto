import assert from "node:assert/strict";
import test from "node:test";

import { createChatComposerController } from "./chat-composer.mjs";

// The transcript reload used to sit inside the same try as the POST, so a failed
// reload was handled as a failed send: the draft was restored into the composer
// and the optimistic echo was pulled back off screen, for a message the server
// had already accepted. The user then had every reason to send it again.
function harness({ loadMessages, request } = {}) {
  const previous = {
    document: globalThis.document,
    window: globalThis.window,
    getComputedStyle: globalThis.getComputedStyle,
    requestAnimationFrame: globalThis.requestAnimationFrame,
  };
  let messageValue = "";
  const input = {
    scrollHeight: 46,
    style: {},
    classList: { toggle() {} },
    get value() { return messageValue; },
    set value(value) { messageValue = String(value || ""); },
    setAttribute() {},
    removeAttribute() {},
    focus() {},
  };
  const elements = {
    messageText: input,
    messageForm: { requestSubmit() {} },
    promptHistoryHint: { textContent: "" },
    slashCommandPalette: { classList: { add() {}, remove() {} }, innerHTML: "" },
  };
  globalThis.document = { getElementById(id) { return elements[id] || null; } };
  globalThis.window = { ...previous.window, setTimeout(callback) { callback(); } };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue: () => "" });
  globalThis.requestAnimationFrame = (callback) => callback();

  const state = {
    agent: { id: "agent-1", model: "openai:model" },
    chatDrafts: {},
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
  };
  const terminalLines = [];
  let discarded = 0;
  const controller = createChatComposerController({
    state,
    currentSkillsPreferences: () => ({ commands: [] }),
    isCurrentModelConfigured: () => true,
    loadMessages: loadMessages || (async () => {}),
    onMessageAccepted: async () => {},
    request: request || (async () => ({})),
    scheduleMessageRefresh() {},
    notifyTerminal: (line) => terminalLines.push(String(line)),
    echoPendingUserMessage: () => "optimistic-1",
    discardPendingUserMessage: () => { discarded += 1; return true; },
  });
  return {
    controller,
    input,
    state,
    terminalLines,
    discardCount: () => discarded,
    restore: () => {
      globalThis.document = previous.document;
      globalThis.window = previous.window;
      globalThis.getComputedStyle = previous.getComputedStyle;
      globalThis.requestAnimationFrame = previous.requestAnimationFrame;
    },
  };
}

test("a failed transcript reload does not undo a delivered send", async () => {
  const { controller, input, state, terminalLines, discardCount, restore } = harness({
    loadMessages: async () => { throw new Error("network dropped"); },
  });
  try {
    input.value = "已經送到伺服器的訊息";
    await controller.sendMessage({ preventDefault() {} });

    // The POST succeeded, so none of the send-failure recovery may run.
    assert.equal(input.value, "", "a delivered message must not reappear in the composer");
    assert.equal(state.chatDrafts["agent-1"], undefined, "no draft should be restored for a delivered message");
    assert.equal(discardCount(), 0, "the echo belongs to a message that was accepted");
    // The failure is still reported rather than silently swallowed.
    assert.equal(terminalLines.some((line) => line.includes("transcript reload after send failed")), true);
  } finally {
    restore();
  }
});

test("a failed POST still restores the draft and drops the echo", async () => {
  const { controller, input, state, discardCount, restore } = harness({
    request: async () => { throw new Error("server refused"); },
  });
  try {
    input.value = "從未送出的訊息";
    await assert.rejects(() => controller.sendMessage({ preventDefault() {} }));

    // The opposite case: nothing reached the server, so the recovery must run.
    assert.equal(input.value, "從未送出的訊息", "an undelivered message must come back");
    assert.equal(state.chatDrafts["agent-1"], "從未送出的訊息");
    assert.equal(discardCount(), 1, "the echo must not claim an undelivered message was sent");
  } finally {
    restore();
  }
});
