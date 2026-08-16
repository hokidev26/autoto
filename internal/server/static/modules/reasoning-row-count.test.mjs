import test from "node:test";
import assert from "node:assert/strict";

import { createChatRenderingController } from "./chat-rendering.mjs";

// One run, two assistant turns. The first has saved its reasoning; the second has
// not. Live steps closed before the first turn was saved carry no messageId.
//
// Narrowing the unstamped claim to the run's first turn fixed a turn adopting a
// backlog it never produced, but it left a hole: when that first turn HAS saved
// reasoning it uses the saved block only and never looks at live steps, while the
// tail released them solely on an unsaved first turn. Nobody claimed them, so the
// tail kept them as a third bare row -- showing the same thinking that the first
// turn's row was already showing in its saved form.

function fakeMessagesElement() {
  const classes = new Set(["empty"]);
  return {
    classList: {
      add: (...n) => n.forEach((x) => classes.add(x)),
      remove: (...n) => n.forEach((x) => classes.delete(x)),
      contains: (x) => classes.has(x),
    },
    innerHTML: "",
    querySelector: () => null,
    querySelectorAll: () => [],
    insertAdjacentHTML(_p, html) { this.innerHTML += html; },
    addEventListener() {},
    scrollHeight: 100, clientHeight: 0, scrollTop: 0,
  };
}

function render(messages, stateOverrides = {}) {
  const el = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? el : null) };
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
      shortPath: (v) => v,
      showError: () => {},
      showToast: () => {},
    });
    assert.equal(controller.applyMessageSnapshot(messages, "agent-1"), true);
    return el.innerHTML;
  } finally {
    globalThis.document = previousDocument;
  }
}

function stacks(html) {
  return [...html.matchAll(/<section class="[^"]*tool-activity-stack[^"]*"([^>]*)>([\s\S]*?)<\/section>/g)].map((m) => ({
    key: (m[1].match(/data-tool-activity-stack-key="([^"]*)"/) || [])[1] || "",
    title: (m[2].match(/<summary class="tool-activity-summary">(?:<svg[\s\S]*?<\/svg>)?([^<]*)/) || [])[1] || "",
    body: m[2],
  }));
}

// The thinking the first turn saved. The live stream produced it as one step
// before that turn existed, so the live copy is unstamped.
const savedThinking = "There is a regression where read conversations show as unread again.";

const messages = [
  { id: "u1", role: "user", contentText: "three things" },
  {
    id: "a1",
    role: "assistant",
    runId: "run-1",
    contentText: "Checking the first one.",
    reasoningText: savedThinking,
  },
  { id: "a2", role: "assistant", runId: "run-1", contentText: "And the second." },
];

const liveState = {
  liveAssistantRunId: "run-1",
  liveReasoningSteps: [
    // Closed before a1 was saved, so unstamped. Same words a1 later saved.
    { id: "reasoning-1", runId: "run-1", messageId: "", text: savedThinking, beforeToolUseId: "t1" },
    // a2's own thinking, correctly stamped.
    { id: "reasoning-2", runId: "run-1", messageId: "a2", text: "Now the model settings.", beforeToolUseId: "" },
  ],
  liveToolOutputs: {
    t1: { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t1", toolName: "Grep", status: "completed" },
    t2: { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t2", toolName: "Read", status: "completed" },
  },
};

// The shape a real run actually has, read off the database rather than guessed:
// one run persists an assistant turn per tool round, and nearly all of them carry
// no reasoning_text at all. Tool calls are anchored onto the run's first turn, so
// that turn holds the whole run's tools. Reasoning must end up on the same row as
// the tools it explains, and each step must appear exactly once.
// A turn whose only content is a tool_use block is not shown in the transcript,
// so it never reaches the per-message loop and can own no row. Steps stamped to
// such a turn therefore have no possible claimant.
const toolOnlyTurn = (id, toolUseId, toolName) => ({
  id,
  role: "assistant",
  runId: "run-9",
  contentText: "",
  contentJson: JSON.stringify([{ type: "tool_use", id: toolUseId, name: toolName, input: {} }]),
});

const manyTurnRun = [
  { id: "u1", role: "user", contentText: "three things" },
  // The run's one visible turn: it anchors the run's tool activity.
  { id: "m1", role: "assistant", runId: "run-9", contentText: "Reproduced. Checking which rows." },
  toolOnlyTurn("m2", "k2", "Read"),
  // Invisible, and it saved reasoning.
  { ...toolOnlyTurn("m3", "k3", "Bash"), reasoningText: "Both sides keep the tail." },
  toolOnlyTurn("m4", "k4", "Edit"),
];

const manyTurnState = {
  liveAssistantRunId: "run-9",
  liveReasoningSteps: [
    { id: "r1", runId: "run-9", messageId: "", text: "First, find where it is read.", beforeToolUseId: "k1" },
    { id: "r2", runId: "run-9", messageId: "m2", text: "Now read the helper.", beforeToolUseId: "k2" },
    { id: "r3", runId: "run-9", messageId: "m4", text: "Then check the bound.", beforeToolUseId: "k3" },
  ],
  liveToolOutputs: {
    // Anchored on the run's first turn, which is what the real renderer does.
    k1: { agentId: "agent-1", runId: "run-9", messageId: "m1", toolUseId: "k1", toolName: "Grep", status: "completed" },
    k2: { agentId: "agent-1", runId: "run-9", messageId: "m1", toolUseId: "k2", toolName: "Read", status: "completed" },
    k3: { agentId: "agent-1", runId: "run-9", messageId: "m1", toolUseId: "k3", toolName: "Bash", status: "completed" },
  },
};

test("every reasoning step of a many-turn run is rendered exactly once", () => {
  const html = render(manyTurnRun, manyTurnState);
  for (const step of manyTurnState.liveReasoningSteps) {
    const count = (html.match(new RegExp(step.text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g")) || []).length;
    assert.equal(count, 1, `${step.text} rendered ${count} times`);
  }
});

test("a many-turn run does not scatter its thinking across extra bare rows", () => {
  const rows = stacks(render(manyTurnRun, manyTurnState));
  const bare = rows.filter((s) => /思考|Thought|reasoning/.test(s.title) && !/步骤|步驟|steps|tool/.test(s.title));
  assert.deepEqual(
    bare.map((s) => `${s.key} :: ${s.title}`),
    [],
    "a row of thinking with no tools beside it is the split this is meant to prevent",
  );
});

test("thinking a turn already saved is not also shown as a bare tail row", () => {
  const html = render(messages, liveState);
  const occurrences = (html.match(new RegExp(savedThinking.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "g")) || []).length;
  assert.equal(occurrences, 1, "the saved block and its unstamped live copy are the same thinking seen twice");
});

test("no orphan stack survives on the tail once the run's turns are on screen", () => {
  const tail = stacks(render(messages, liveState)).filter((s) => s.key.startsWith("live:"));
  assert.deepEqual(tail.map((s) => s.title), [], "every step belongs to a turn that is visible");
});

test("one row per turn that has activity, and no more", () => {
  const rows = stacks(render(messages, liveState));
  assert.deepEqual(rows.map((s) => s.key).sort(), ["msg:a1", "msg:a2"]);
});

test("each turn's row carries its own thinking, not the other's", () => {
  const rows = stacks(render(messages, liveState));
  const a1 = rows.find((s) => s.key === "msg:a1");
  const a2 = rows.find((s) => s.key === "msg:a2");
  assert.ok(a1.body.includes(savedThinking), "a1 saved this thinking, so a1 shows it");
  assert.equal(a2.body.includes(savedThinking), false, "a2 never produced it");
  assert.ok(a2.body.includes("Now the model settings."), "a2's stamped step stays with a2");
  assert.match(a1.title, /2 个步骤|2 個步驟|2 steps/, "a1 owns the tools");
});
