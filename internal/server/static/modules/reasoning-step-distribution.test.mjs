import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// A run makes one assistant turn per tool round. Those turns are invisible: their
// only content is a tool_use block, whose rendered text is stripped, so the
// message is empty and never reaches the transcript. They still hold most of the
// run's thinking, stamped with their own message id.
//
// Every one of those steps used to go to the run's first visible turn, the only
// turn allowed to claim an orphan. One row then read "3 步推理" while the rounds
// beside it showed a single step each. A step whose owner is a known invisible
// turn now lands on the last visible turn saved at or before that owner, so each
// row's count describes its own round and the order is preserved.
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
    agent: { id: "agent-1", cwd: "/work/project", status: "running" },
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

function stacks(html) {
  return [...html.matchAll(/<section class="[^"]*tool-activity-stack[^"]*"([^>]*)>([\s\S]*?)<\/section>/g)].map((match) => ({
    key: (match[1].match(/data-tool-activity-stack-key="([^"]*)"/) || [])[1] || "",
    body: match[2],
  }));
}

function rowFor(html, messageId) {
  return stacks(html).find((row) => row.key === `msg:${messageId}`);
}

// v1 and v2 are visible rounds; h1 and h2 are tool-only rounds between them, so
// they are filtered out of the transcript but still own their thinking.
const messages = [
  { id: "u1", role: "user", contentText: "go", createdAt: "2026-08-10T10:00:00Z" },
  { id: "v1", role: "assistant", runId: "run-1", contentText: "First visible round.", createdAt: "2026-08-10T10:00:10Z" },
  { id: "h1", role: "assistant", runId: "run-1", contentText: "", createdAt: "2026-08-10T10:00:15Z" },
  { id: "v2", role: "assistant", runId: "run-1", contentText: "Second visible round.", createdAt: "2026-08-10T10:00:20Z" },
  { id: "h2", role: "assistant", runId: "run-1", contentText: "", createdAt: "2026-08-10T10:00:25Z" },
];

const step = (id, owner, text) => ({ id, runId: "run-1", messageId: owner, text, beforeToolUseId: "" });

test("隱形回合的推理依時間落在各自的可見列", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [
      step("s1", "h1", "Thinking from the round after the first row."),
      step("s2", "h2", "Thinking from the round after the second row."),
    ],
  });
  const first = rowFor(html, "v1");
  const second = rowFor(html, "v2");
  assert.ok(first, "第一個可見回合要有活動列");
  assert.ok(second, "第二個可見回合要有活動列");

  assert.ok(first.body.includes("Thinking from the round after the first row."));
  assert.equal(first.body.includes("Thinking from the round after the second row."), false,
    "後面回合的思考不該倒回前一列");

  assert.ok(second.body.includes("Thinking from the round after the second row."));
  assert.equal(second.body.includes("Thinking from the round after the first row."), false,
    "前面回合的思考不該重複出現在後一列");
});

test("一列不會獨吞整個 run 的推理", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [
      step("s1", "h1", "Step A."),
      step("s2", "h2", "Step B."),
      step("s3", "h2", "Step C."),
    ],
  });
  const first = rowFor(html, "v1");
  const texts = ["Step A.", "Step B.", "Step C."];
  const inFirst = texts.filter((text) => first?.body.includes(text)).length;
  assert.equal(inFirst, 1, `第一列只該拿到屬於它那段時間的步驟，實際 ${inFirst}`);
});

test("每個步驟只出現一次", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [step("s1", "h1", "Only once please."), step("s2", "h2", "Also only once.")],
  });
  assert.equal((html.match(/Only once please\./g) || []).length, 1);
  assert.equal((html.match(/Also only once\./g) || []).length, 1);
});

// The unstamped backlog keeps its existing rule. It closed before any turn of the
// run was saved, so it names no owner and there is no time to place it by: only
// the run's first visible turn may claim it, and a later turn must not. That rule
// predates this change and has to survive it. Where such a backlog goes when the
// tail is on screen is covered by unstamped-reasoning-owner.test.mjs.
test("沒有標記所有者的推理仍然維持原本規則", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [step("s1", "", "Unstamped backlog.")],
  });
  assert.equal(rowFor(html, "v2")?.body.includes("Unstamped backlog.") ?? false, false,
    "未標記的積壓不該被後面的回合認領");
  assert.ok(rowFor(html, "v1")?.body.includes("Unstamped backlog."),
    "未標記的積壓仍由該 run 的第一個可見回合承接，不能消失");
});

// A step stamped to a visible turn is already owned and must not move.
test("標記到可見回合的推理不受影響", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [step("s1", "v2", "Belongs to the second row.")],
  });
  assert.ok(rowFor(html, "v2")?.body.includes("Belongs to the second row."));
  assert.equal(rowFor(html, "v1")?.body.includes("Belongs to the second row.") ?? false, false);
});
