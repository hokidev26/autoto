import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

import { createChatComposerController } from "./chat-composer.mjs";

// Sending inside the 400ms draft debounce used to leave the sent message sitting
// in the composer afterwards. The debounced PUT was still pending when the send
// deleted the draft, so it recreated it; and once the delete had reset the
// version, that PUT came back 409 and the recovery path deliberately re-read the
// version and wrote the stale text again.
function createHarness({ modelSelectValue, saveAgentSettings } = {}) {
  let messageValue = "";
  const input = {
    disabled: false,
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
  if (modelSelectValue !== undefined) elements.modelSelect = { value: modelSelectValue };
  const timers = [];
  const previous = {
    document: globalThis.document,
    window: globalThis.window,
    getComputedStyle: globalThis.getComputedStyle,
    requestAnimationFrame: globalThis.requestAnimationFrame,
    localStorage: globalThis.localStorage,
    fetch: globalThis.fetch,
  };
  globalThis.document = { getElementById: (id) => elements[id] || null };
  globalThis.window = {
    ...previous.window,
    setTimeout(callback) { timers.push(callback); return timers.length; },
    clearTimeout(handle) { if (handle) timers[handle - 1] = null; },
  };
  globalThis.getComputedStyle = () => ({ minHeight: "46px", maxHeight: "128px", getPropertyValue: () => "" });
  globalThis.requestAnimationFrame = (callback) => callback();
  const stored = {};
  globalThis.localStorage = {
    getItem: (key) => (key in stored ? stored[key] : null),
    setItem: (key, value) => { stored[key] = String(value); },
    removeItem: (key) => { delete stored[key]; },
  };

  const draftCalls = [];
  // chat-composer imports api from runtime.mjs directly, so the seam is fetch.
  globalThis.fetch = async (path, options = {}) => {
    const method = String(options.method || "GET").toUpperCase();
    draftCalls.push(`${method} ${path}`);
    const reply = (status, payload) => ({
      ok: status < 400,
      status,
      statusText: String(status),
      text: async () => (payload === undefined ? "" : JSON.stringify(payload)),
    });
    if (String(path).endsWith("/draft") && method === "PUT") {
      return reply(200, { version: Number(JSON.parse(options.body || "{}").version || 0) + 1 });
    }
    if (String(path).endsWith("/draft") && method === "DELETE") return reply(204);
    if (String(path).endsWith("/draft")) return reply(200, { contentText: "", version: 9 });
    return reply(200, {});
  };

  const state = {
    agent: { id: "agent-1", model: "openai:model" },
    navigationSelectionKind: "conversation",
    chatDrafts: {},
    promptHistory: [],
    pendingAttachments: [],
    serverSkills: [],
    serverDrafts: { "agent-1": { enabled: true, version: 1, seq: 0, timer: null, epoch: 0 } },
  };

  // Handed to the saveAgentSettings hook so a test can reach the controller
  // from inside the save, the way app-main's settings pass re-enters the agent.
  const handle = {};
  const controller = createChatComposerController({
    state,
    awaitAgentSettingsSaved: async () => {},
    saveAgentSettings: async () => { await saveAgentSettings?.(handle); },
    currentSkillsPreferences: () => ({ commands: [] }),
    isCurrentModelConfigured: () => true,
    loadMessages: async () => {},
    onMessageAccepted: async () => {},
    request: async () => ({ accepted: true }),
    scheduleMessageRefresh() {},
    scrollMessagesToBottom() {},
  });

  Object.assign(handle, {
    controller,
    input,
    state,
    draftCalls,
    puts: () => draftCalls.filter((call) => call.startsWith("PUT")),
    runTimers() {
      const pending = timers.splice(0, timers.length).filter(Boolean);
      for (const callback of pending) callback();
    },
    restore() {
      globalThis.document = previous.document;
      globalThis.window = previous.window;
      globalThis.getComputedStyle = previous.getComputedStyle;
      globalThis.requestAnimationFrame = previous.requestAnimationFrame;
      globalThis.localStorage = previous.localStorage;
      globalThis.fetch = previous.fetch;
    },
  });
  return handle;
}

test("送出訊息後，還在等待的草稿儲存不會把文字寫回去", async () => {
  const harness = createHarness();
  try {
    const draftState = harness.state.serverDrafts["agent-1"];

    harness.input.value = "這行不該留在輸入框";
    harness.controller.handleMessageInput();
    const epochWhileTyping = draftState.epoch;

    await harness.controller.sendMessage({ preventDefault() {} });
    assert.equal(harness.input.value, "", "送出後輸入框要清空");
    assert.ok(draftState.epoch > epochWhileTyping, "送出必須讓草稿 epoch 前進");

    // The debounced save now fires, after the send already deleted the draft.
    harness.runTimers();
    await Promise.resolve();
    await Promise.resolve();

    assert.deepEqual(harness.puts(), [], "退役 epoch 的草稿儲存不該送出 PUT");
    assert.ok(
      harness.draftCalls.some((call) => call.startsWith("DELETE")),
      "送出時應該刪除伺服器草稿",
    );
  } finally {
    harness.restore();
  }
});

test("送出也會清掉本機草稿備份", async () => {
  const harness = createHarness();
  try {
    harness.input.value = "本機殘留";
    harness.controller.handleMessageInput();
    await harness.controller.sendMessage({ preventDefault() {} });
    assert.ok(!harness.state.chatDrafts?.["agent-1"], "本機備份要一起清掉");
  } finally {
    harness.restore();
  }
});

test("沒有送出時，草稿照常儲存", async () => {
  const harness = createHarness();
  try {
    harness.input.value = "正在打字";
    harness.controller.handleMessageInput();
    harness.runTimers();
    await Promise.resolve();
    await Promise.resolve();
    assert.deepEqual(harness.puts(), ["PUT /api/agents/agent-1/draft"], "一般輸入仍要存草稿");
  } finally {
    harness.restore();
  }
});

// A fresh session saves the model on the first send: the picker was chosen
// before the agent record had it, so the send's model sync runs a settings
// save. persistAgentSettingsPass used to re-enter the agent afterwards, whose
// draft restore ran while the send was still in flight. The stored draft is
// only cleared after delivery, so the restore found the just-sent text intact
// and wrote it back into the box. The save no longer re-enters, and the
// composer still has to ignore a restore that races the in-flight send.
test("送出途中觸發的草稿還原不得把剛送出的字寫回輸入框", async () => {
  const harness = createHarness({
    modelSelectValue: "openai:other",
    async saveAgentSettings(h) {
      // What app-main's persistAgentSettingsPass does: patch the model on the
      // agent, then re-enter it, which restores the draft.
      h.state.agent = { ...h.state.agent, model: "openai:other" };
      await h.controller.restoreCurrentChatDraft();
    },
  });
  try {
    // The cold-boot desktop shape: the server draft route is not confirmed yet,
    // so typing backs the text up locally and the restore reads it back
    // synchronously.
    harness.state.serverDrafts["agent-1"].enabled = false;
    harness.input.value = "開場第一句";
    harness.controller.handleMessageInput();
    await harness.controller.sendMessage({ preventDefault() {} });
    assert.equal(harness.input.value, "", "第一則訊息送出後輸入框要保持清空");
  } finally {
    harness.restore();
  }
});

// The 409 recovery exists for two editors racing on one draft. It must stay for
// that case, and must not fire for a conflict caused by our own send.
test("409 復原只在 epoch 未變時重試", () => {
  const source = readFileSync(new URL("./chat-composer.mjs", import.meta.url), "utf8");
  assert.match(source, /if \(error\?\.status === 409 && draftState\.epoch === epoch\) \{/);
  assert.match(source, /if \(draftState\.epoch !== epoch\) return;\s*\n\s*draftState\.version = Number\(latest\?\.version \|\| 0\);/);
  assert.match(source, /draftState\.epoch \+= 1;/);
});
