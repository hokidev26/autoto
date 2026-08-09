import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// A live reasoning step is stamped with the assistant turn that produced it, but
// liveAssistantToolOwnerId is only known once that turn has been persisted. Steps
// that close before then carry messageId "", and the per-message filter lets an
// unstamped step through for every message. One run that thought a lot before its
// first turn was saved therefore handed its whole backlog to any later turn that
// had no persisted reasoning of its own, producing a bare "N steps of reasoning"
// row beside the turn that actually held the tools.

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
    // The agent id is positional here, not an options object.
    assert.equal(controller.applyMessageSnapshot(messages, "agent-1"), true);
    return messagesElement.innerHTML;
  } finally {
    globalThis.document = previousDocument;
  }
}

// Two assistant turns in one run. The first holds the tools and its own saved
// reasoning; the second has none of either. The unstamped backlog must not be
// adopted by the second turn just because it has nothing of its own.
const messages = [
  { id: "u1", role: "user", contentText: "go" },
  {
    id: "a1",
    role: "assistant",
    runId: "run-1",
    contentText: "Working on it.",
    reasoningText: "Persisted thinking for the first turn.",
  },
  { id: "a2", role: "assistant", runId: "run-1", contentText: "Here is the answer." },
];

const unstampedSteps = Array.from({ length: 4 }, (_, index) => ({
  id: `reasoning-${index + 1}`,
  runId: "run-1",
  // Closed before any assistant turn was persisted, so there is no owner.
  messageId: "",
  text: `Unstamped step ${index + 1}.`,
  beforeToolUseId: "",
}));

const liveToolOutputs = {
  t1: { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t1", toolName: "Grep", status: "completed" },
  t2: { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t2", toolName: "Read", status: "completed" },
};

function stacks(html) {
  return [...html.matchAll(/<section class="[^"]*tool-activity-stack[^"]*"([^>]*)>([\s\S]*?)<\/section>/g)].map((match) => ({
    key: (match[1].match(/data-tool-activity-stack-key="([^"]*)"/) || [])[1] || "",
    title: (match[2].match(/<summary class="tool-activity-summary">([^<]*)</) || [])[1] || "",
    body: match[2],
  }));
}

test("a later turn of the same run does not adopt the unstamped backlog", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: unstampedSteps,
    liveToolOutputs,
  });
  const a2 = stacks(html).find((stack) => stack.key === "msg:a2");
  // The bug gave a2 a bare "N steps of reasoning" row built from thinking that
  // belonged to no turn, sitting beside the turn that held the tools.
  assert.equal(a2, undefined, "a2 produced no activity of its own, so it gets no row");
});

test("the unstamped backlog stays on the tail, which is the surface that has no owner", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: unstampedSteps,
    liveToolOutputs,
  });
  const tail = stacks(html).find((stack) => stack.key.startsWith("live:"));
  assert.ok(tail, "thinking that precedes every saved turn has to remain visible");
  for (const step of unstampedSteps) {
    assert.ok(tail.body.includes(step.text), `${step.text} must still be shown`);
  }
});

test("the turn that did the work keeps its own reasoning and tools in one row", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: unstampedSteps,
    liveToolOutputs,
  });
  const a1 = stacks(html).find((stack) => stack.key === "msg:a1");
  assert.ok(a1, "the turn holding the tools must have a row");
  assert.match(a1.title, /1 步推理 · 2 次工具|1 step.*2 tool/);
  // Its row must not also contain the ownerless backlog.
  for (const step of unstampedSteps) {
    assert.equal(a1.body.includes(step.text), false, `${step.text} does not belong to a1`);
  }
});

test("a step stamped with a turn still lands only on that turn", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveReasoningSteps: [{
      id: "reasoning-owned",
      runId: "run-1",
      messageId: "a2",
      text: "Thinking that belongs to the second turn.",
      beforeToolUseId: "",
    }],
    liveToolOutputs,
  });
  assert.equal((html.match(/Thinking that belongs to the second turn\./g) || []).length, 1);
});
