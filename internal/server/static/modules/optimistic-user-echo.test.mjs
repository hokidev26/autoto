import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// Sending cleared the composer and then showed nothing until the POST and the
// transcript reload after it had both finished, so the user's own text was absent
// from the screen for 1-2 seconds. These tests pin the echo that closes that gap
// and, just as importantly, that it disappears again when the send fails.
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
    removeAttribute() {},
    dataset: {},
    scrollHeight: 100,
    clientHeight: 0,
    scrollTop: 0,
  };
}

function harness(overrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveImageGenerations: {},
    pendingToolApprovals: {},
    blockedToolNotices: {},
    pendingUserQuestions: {},
    ...overrides,
  };
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
  return { state, controller, messagesElement, restore: () => { globalThis.document = previousDocument; } };
}

test("the sent text is painted before any server round trip", () => {
  const { state, controller, messagesElement, restore } = harness();
  try {
    const id = controller.echoPendingUserMessage("agent-1", "請幫我看這段程式");
    assert.ok(id, "an echo id is needed so the failure path can remove it again");
    assert.equal(state.currentMessages.length, 1);
    // The paint is the point: asserting only on state would pass even if the
    // message never reached the DOM.
    assert.match(messagesElement.innerHTML, /請幫我看這段程式/);
  } finally {
    restore();
  }
});

test("the server snapshot replaces the echo instead of doubling it", () => {
  const { state, controller, restore } = harness();
  try {
    controller.echoPendingUserMessage("agent-1", "hello");
    controller.applyMessageSnapshot(
      [{ id: "server-1", role: "user", contentText: "hello", createdAt: "2026-08-09T06:00:00.000Z" }],
      "agent-1",
      { forceRender: true },
    );
    // applyMessageSnapshot assigns state.currentMessages wholesale, which is what
    // makes the echo safe: there is no id reconciliation to get wrong.
    assert.deepEqual(state.currentMessages.map((message) => message.id), ["server-1"]);
  } finally {
    restore();
  }
});

test("a failed send takes the echo back off the screen", () => {
  const { state, controller, messagesElement, restore } = harness();
  try {
    controller.echoPendingUserMessage("agent-1", "this never leaves the browser");
    assert.equal(controller.discardPendingUserMessage("agent-1"), true);
    assert.deepEqual(state.currentMessages, []);
    assert.doesNotMatch(messagesElement.innerHTML, /this never leaves the browser/);
  } finally {
    restore();
  }
});

test("an empty send paints nothing", () => {
  const { state, controller, restore } = harness();
  try {
    assert.equal(controller.echoPendingUserMessage("agent-1", "   "), "");
    assert.deepEqual(state.currentMessages, []);
  } finally {
    restore();
  }
});

test("an attachment-only send still paints a turn", () => {
  const { state, controller, restore } = harness();
  try {
    const id = controller.echoPendingUserMessage("agent-1", "", [{ file: { name: "shot.png", type: "image/png", size: 2048 } }]);
    assert.ok(id, "an image on its own is a valid send, so it must be visible too");
    assert.equal(state.currentMessages[0].attachments[0].name, "shot.png");
  } finally {
    restore();
  }
});

// Submitting used to bring the finished turn's outcome card back for one paint.
// state.activeRunSummary still held the previous run when the echo was inserted,
// so the snapshot rendered that card -- tool activity and its "load earlier tool
// calls" button -- and it sat there until agent.started fired and cleared it.
// The card was on its way out either way, so the only thing that paint added was
// a flicker. Nothing about it is stale-card *placement*: it is about the card
// existing at all between the submit and the next run.
function finishedRunState() {
  return {
    activeRunSummary: {
      run: { id: "run-a", source: "conversation", status: "completed", triggerMessageId: "u1" },
      toolCallCount: 68,
    },
    activeRunSummaryRunId: "run-a",
    activeRunToolCalls: [{ toolUseId: "t-1", toolName: "Read", status: "completed" }],
    activeRunToolCallsRunId: "run-a",
    // What produces the "load earlier tool calls" button.
    activeRunToolCallsHasMore: true,
    activeRunToolCallsNextOffset: 1,
    historyRunToolCalls: {},
    currentMessages: [
      { id: "u1", role: "user", contentText: "第一個問題", runId: "run-a" },
      { id: "a1", role: "assistant", contentText: "做完了。", runId: "run-a" },
    ],
    runSummaryLoading: false,
    runSummaryError: "",
  };
}

test("送出下一則訊息時，上一輪的 run summary 卡片不會再閃現一次", () => {
  const { state, controller, messagesElement, restore } = harness(finishedRunState());
  try {
    // Establish that this fixture really does paint the card, so the assertions
    // after the echo are about the send clearing it rather than about a fixture
    // that never rendered it in the first place.
    controller.applyMessageSnapshot(state.currentMessages, "agent-1", { forceRender: true });
    assert.match(messagesElement.innerHTML, /data-run-outcome-card/, "前一輪的摘要卡本來就在畫面上");
    assert.match(messagesElement.innerHTML, /data-run-tool-activity-more/, "含「載入更早工具呼叫」按鈕");

    const id = controller.echoPendingUserMessage("agent-1", "第二個問題");
    assert.ok(id);

    // The echo's own paint is the one that used to show the card again.
    assert.doesNotMatch(messagesElement.innerHTML, /data-run-outcome-card/, "送出時就不該再畫出上一輪的摘要卡");
    assert.doesNotMatch(messagesElement.innerHTML, /data-run-tool-activity-more/);
    assert.match(messagesElement.innerHTML, /第二個問題/, "使用者自己的新訊息仍然要立刻出現");

    // Cleared at the source, not hidden by the renderer: agent.started no longer
    // has a stale summary left to clean up.
    assert.equal(state.activeRunSummary, null);
    assert.equal(state.activeRunSummaryRunId, "");
    assert.deepEqual(state.activeRunToolCalls, []);
    assert.equal(state.activeRunToolCallsHasMore, false);
  } finally {
    restore();
  }
});

test("the echo never lands in a conversation the user switched away from", () => {
  const { state, controller, restore } = harness();
  try {
    assert.equal(controller.echoPendingUserMessage("agent-2", "meant for another chat"), "");
    assert.deepEqual(state.currentMessages, []);
  } finally {
    restore();
  }
});
