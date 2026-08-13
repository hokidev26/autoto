import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// A dispatched subagent reaches the transcript as an `agent` tool call. Each
// dispatch belongs to the assistant turn that made it, so its card has to stay
// with that turn: a reader scrolling back through several dispatches needs to
// see which question produced which subagent. Letting the cards collect at the
// tail instead puts every subagent under the newest question, which is the
// shape a reader reads as "the last turn did all of this work".
function fakeMessagesElement() {
  const classes = new Set(["empty"]);
  return {
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      contains: (name) => classes.has(name),
    },
    innerHTML: "",
    querySelector: () => null,
    querySelectorAll: () => [],
    insertAdjacentHTML(_position, html) { this.innerHTML += html; },
    addEventListener() {},
    scrollHeight: 100,
    clientHeight: 0,
    scrollTop: 0,
  };
}

function render(messages, stateOverrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project", status: "idle" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveImageGenerations: {},
    pendingToolApprovals: {},
    blockedToolNotices: {},
    pendingUserQuestions: {},
    activeRunSummary: null,
    activeRunSummaryRunId: "",
    ...stateOverrides,
  };
  try {
    const controller = createChatRenderingController({
      state,
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });
    assert.equal(controller.applyMessageSnapshot(messages, "agent-1"), true);
    return messagesElement.innerHTML;
  } finally {
    globalThis.document = previousDocument;
  }
}

// Two dispatches an hour apart, each its own run and its own visible turn.
const messages = [
  { id: "u1", role: "user", contentText: "幫我派一個代理查台中天氣", createdAt: "2026-08-13T15:00:00Z" },
  { id: "a1", role: "assistant", runId: "run-1", contentText: "已派出子代理。", createdAt: "2026-08-13T15:00:10Z" },
  { id: "u2", role: "user", contentText: "幫我派一個代理查高雄活動", createdAt: "2026-08-13T16:00:00Z" },
  { id: "a2", role: "assistant", runId: "run-2", contentText: "已派出第二個子代理。", createdAt: "2026-08-13T16:00:10Z" },
];

function agentCall(toolUseId, extra = {}) {
  return { agentId: "agent-1", toolName: "Agent", toolUseId, status: "completed", ...extra };
}

function positions(html) {
  return {
    a1: html.indexOf('data-message-id="a1"'),
    a2: html.indexOf('data-message-id="a2"'),
    card1: html.indexOf('data-tool-use-id="task-1"'),
    card2: html.indexOf('data-tool-use-id="task-2"'),
  };
}

// An assistant turn's activity leads its words ("work before words"), so a
// card owned by a turn renders immediately *before* that turn's message.
test("標記了所有者的子代理卡片留在派出它的回合", () => {
  const html = render(messages, {
    liveToolOutputs: {
      "task-1": agentCall("task-1", { runId: "run-1", messageId: "a1", createdAt: "2026-08-13T15:00:12Z" }),
      "task-2": agentCall("task-2", { runId: "run-2", messageId: "a2", createdAt: "2026-08-13T16:00:12Z" }),
    },
  });
  const at = positions(html);
  assert.ok(at.a1 >= 0 && at.a2 >= 0, "兩個回合都要出現在 transcript");
  assert.ok(at.card1 >= 0 && at.card2 >= 0, "兩張子代理卡片都要渲染");
  assert.ok(at.card1 < at.a1, "第一次派出的子代理要領在第一回合的答案之前");
  assert.ok(at.card2 > at.a1 && at.card2 < at.a2, "第二次派出的子代理要落在兩個回合之間，跟著第二回合");
});

// Even with neither an owner nor a timestamp, adoption is scoped to the call's
// own run, so a dispatch still lands on that run's turn instead of collecting
// under the newest one. This is the case that reads as "every subagent piled
// onto the last question" when it goes wrong, so it is pinned here.
test("缺少所有者與時間戳時，子代理活動仍依所屬 run 回到自己的回合", () => {
  const html = render(messages, {
    liveToolOutputs: {
      "task-1": agentCall("task-1", { runId: "run-1" }),
      "task-2": agentCall("task-2", { runId: "run-2" }),
    },
  });
  const at = positions(html);
  assert.ok(at.card1 >= 0 && at.card2 >= 0, "兩張卡片仍然要渲染，不能消失");
  assert.ok(at.card1 < at.a1, "第一次派出的子代理要留在第一回合");
  assert.ok(at.card2 > at.a1 && at.card2 < at.a2, "第二次派出的子代理要跟著第二回合");
});

// The real-world shape: a subagent dispatched in the first turn is still
// running when the reader asks the next question, so a newer run is active
// while the older dispatch's card is still live. The older card must stay with
// the turn that dispatched it rather than following the active run to the tail.
test("較早派出、仍在執行的子代理不會跟著新的 active run 跑到最尾端", () => {
  const html = render(messages, {
    agent: { id: "agent-1", cwd: "/work/project", status: "running" },
    activeRunSummaryRunId: "run-2",
    activeRunSummary: { run: { id: "run-2", status: "running" }, toolCalls: [] },
    liveToolOutputs: {
      "task-1": agentCall("task-1", { runId: "run-1", messageId: "a1", createdAt: "2026-08-13T15:00:12Z", status: "running" }),
      "task-2": agentCall("task-2", { runId: "run-2", messageId: "a2", createdAt: "2026-08-13T16:00:12Z", status: "running" }),
    },
  });
  const at = positions(html);
  assert.ok(at.card1 >= 0 && at.card2 >= 0, "兩張卡片都要渲染");
  assert.ok(at.card1 < at.a1, "還在跑的第一個子代理仍要留在第一回合");
  assert.ok(at.card2 < at.a2, "第二個子代理要跟著第二回合，不能被推到 transcript 最尾端");
});

test("未標記所有者的子代理活動依時間回到各自的回合", () => {
  const html = render(messages, {
    liveToolOutputs: {
      "task-1": agentCall("task-1", { runId: "run-1", createdAt: "2026-08-13T15:00:12Z" }),
      "task-2": agentCall("task-2", { runId: "run-2", createdAt: "2026-08-13T16:00:12Z" }),
    },
  });
  const at = positions(html);
  assert.ok(at.card1 >= 0 && at.card2 >= 0, "兩張子代理卡片都要渲染");
  assert.ok(at.card1 < at.a2, "較早那次派出的子代理不該堆到最後一回合");
});
