import test from "node:test";
import assert from "node:assert/strict";

globalThis.window = { AUTOTO_LOCAL_TOKEN: "", CODEHARBOR_LOCAL_TOKEN: "" };
globalThis.location = { origin: "http://localhost", protocol: "http:", host: "localhost" };
globalThis.document = { getElementById: () => null, documentElement: { lang: "zh-TW" } };

const { createConversationTitleHelpers } = await import("./conversation-title-helpers.mjs");

function helpersFor(state) {
  return createConversationTitleHelpers({
    state,
    selectedModelValue: () => "",
    currentModelValue: () => "",
    projectOperationContextActive: () => true,
    effectivePermissionForDisplay: () => "",
    connectionModeSummary: () => ({}),
    permissionLabel: () => "",
    renderConversationHeaderIdentity() {},
    renderWorkbenchHeaderIdentity() {},
    renderRecentSidebarConversations() {},
    saveConversationTitle() {},
    showError() {},
  });
}

test("an untitled conversation is titled from the first user message, not the project path", () => {
  const { conversationHeaderTitle } = helpersFor({
    agent: {},
    project: { name: "C:\\Users\\Ray\\Desktop\\ai測試" },
    currentMessages: [
      { role: "assistant", content: "先招呼一下" },
      { role: "user", content: "幫我看一下這個專案的架構" },
      { role: "user", content: "再來一句" },
    ],
  });
  assert.equal(conversationHeaderTitle(), "幫我看一下這個專案的架構");
});

test("an explicit agent title always wins over the derived one", () => {
  const { conversationHeaderTitle } = helpersFor({
    agent: { title: "我自己命名的" },
    project: { name: "C:\\path" },
    currentMessages: [{ role: "user", content: "幫我看一下架構" }],
  });
  assert.equal(conversationHeaderTitle(), "我自己命名的");
});

test("a conversation with nothing to derive from reads as new, never as the path", () => {
  const base = { agent: {}, project: { name: "C:\\Users\\Ray\\Desktop\\ai測試" } };
  const empty = helpersFor({ ...base, currentMessages: [] }).conversationHeaderTitle();
  assert.doesNotMatch(empty, /Desktop/);
  // Assistant-only and blank user messages are not usable titles either.
  const unusable = helpersFor({
    ...base,
    currentMessages: [{ role: "assistant", content: "你好" }, { role: "user", content: "   " }],
  }).conversationHeaderTitle();
  assert.doesNotMatch(unusable, /Desktop/);
  assert.equal(empty, unusable);
});

test("derived titles collapse whitespace, drop code fences, and are length capped", () => {
  const { conversationHeaderTitle } = helpersFor({
    agent: {},
    project: { name: "C:\\path" },
    currentMessages: [{ role: "user", content: "  修這個\n\n bug ```js\nconst a = 1;\n``` 謝謝  " }],
  });
  assert.equal(conversationHeaderTitle(), "修這個 bug 謝謝");

  const long = helpersFor({
    agent: {},
    project: { name: "C:\\path" },
    currentMessages: [{ role: "user", content: "一".repeat(60) }],
  }).conversationHeaderTitle();
  // 28 characters plus the ellipsis.
  assert.equal(Array.from(long).length, 29);
  assert.equal(long.endsWith("…"), true);
});
