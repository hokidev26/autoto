import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// Two behaviours are locked in here, both about the same rule: a stopped run must
// say why it stopped, and a running one must not be interrupted with a step that
// it may still be routing around.
//
// The failure reason matters because agent.error carries the text but the
// transcript used to render it only via the persisted run summary, which the
// server never writes when registerRun fails before a run row exists. The run
// then stopped with the reason in hand and the screen stayed empty.
// Recording a notice re-renders the stack, which reaches for #messages, so the
// controller needs a document the same way the other rendering tests give it one.
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

function harness({ status = "running", summary = null } = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", status, cwd: "/work/project" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveImageGenerations: {},
    pendingToolApprovals: {},
    pendingUserQuestions: {},
    blockedToolNotices: {},
    liveAssistantActive: false,
    activeRunSummary: summary,
    lastAgentError: null,
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
  return { controller, state, restore: () => { globalThis.document = previousDocument; } };
}

function errorEvent(overrides = {}) {
  return {
    type: "agent.error",
    agentId: "agent-1",
    text: "provider handshake rejected the client version",
    data: { runId: "run-1" },
    ...overrides,
  };
}

test("a failed run shows its reason even when no run summary exists", () => {
  // The registerRun path: no persisted run, so the event text is the only copy.
  const { controller, state, restore } = harness({ status: "running" });
  try {
    controller.rememberAgentRunError(errorEvent({ data: { runId: "" } }));
    state.agent = { ...state.agent, status: "error" };

    const html = controller.renderApprovalCardsHTML();
    assert.match(html, /conversation-run-notice error/);
    assert.match(html, /provider handshake rejected the client version/);
  } finally {
    restore();
  }
});

test("the reason stays hidden while the agent is still working", () => {
  const { controller, state, restore } = harness({ status: "running" });
  try {
    controller.rememberAgentRunError(errorEvent());
    assert.equal(state.agent.status, "running");
    assert.equal(controller.renderApprovalCardsHTML(), "");
  } finally {
    restore();
  }
});

test("a new turn drops the previous failure reason", () => {
  const { controller, state, restore } = harness({ status: "error" });
  try {
    controller.rememberAgentRunError(errorEvent());
    assert.match(controller.renderApprovalCardsHTML(), /conversation-run-notice error/);

    controller.clearAgentRunError("agent-1");
    state.agent = { ...state.agent, status: "running" };
    assert.equal(controller.renderApprovalCardsHTML(), "");
  } finally {
    restore();
  }
});

test("the run summary owns the failure when it has one, so the reason is not doubled", () => {
  // Both would otherwise render, leaving two rows saying the same thing.
  const summary = { run: { id: "run-1", status: "error", errorMessage: "same failure" } };
  const { controller, restore } = harness({ status: "error", summary });
  try {
    controller.rememberAgentRunError(errorEvent());
    assert.equal(controller.renderApprovalCardsHTML(), "", "the summary card already carries this failure, with a retry button");
  } finally {
    restore();
  }
});

const blockEvent = {
  type: "tool.approval_required",
  agentId: "agent-1",
  data: { toolUseId: "tool-1", risk: "danger", toolName: "Bash", warning: "this truncates an existing file" },
};

test("a blocked tool call is not reported while the agent is still working", () => {
  // A refusal is usually not the end: the agent takes another route and finishes.
  const { controller, restore } = harness({ status: "running" });
  try {
    controller.rememberToolApproval(blockEvent);
    assert.equal(controller.renderApprovalCardsHTML(), "");
  } finally {
    restore();
  }
});

test("a blocked tool call is reported once the agent stops", () => {
  const { controller, state, restore } = harness({ status: "running" });
  try {
    controller.rememberToolApproval(blockEvent);
    state.agent = { ...state.agent, status: "idle" };
    assert.match(controller.renderApprovalCardsHTML(), /this truncates an existing file/);
  } finally {
    restore();
  }
});

test("a hard block never becomes a pending approval", () => {
  // It would otherwise count as a decision the reader has to make and leave the
  // composer waiting forever on one nobody can give.
  const { controller, state, restore } = harness({ status: "running" });
  try {
    controller.rememberToolApproval(blockEvent);
    assert.deepEqual(Object.keys(state.pendingToolApprovals || {}), []);
    assert.deepEqual(Object.keys(state.blockedToolNotices || {}), ["tool-1"]);
  } finally {
    restore();
  }
});

test("a pending approval counts as mid-turn, so the reason waits", () => {
  const { controller, state, restore } = harness({ status: "idle" });
  try {
    controller.rememberAgentRunError(errorEvent());
    state.pendingToolApprovals = { "tool-9": { toolUseId: "tool-9", agentId: "agent-1", toolName: "Bash" } };
    // The stack is not empty here -- the approval card itself renders. What must
    // not appear is the failure reason, because the turn is still going.
    const html = controller.renderApprovalCardsHTML();
    assert.doesNotMatch(html, /provider handshake rejected the client version/);
  } finally {
    restore();
  }
});
