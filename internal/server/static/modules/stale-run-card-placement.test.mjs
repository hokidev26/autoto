import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// The run outcome card describes the work between one question and its answer, so
// it belongs after the message that triggered its run. In a long conversation that
// trigger can sit outside the loaded window, and the fallback ladder then reached
// for "the newest user message" -- which, right after a submit, is the question for
// the *next* run. A finished run's activity, its omitted-count note and its "load
// earlier tool calls" button were rendered under a message that run had never seen,
// then disappeared once the new run's summary replaced the card.
function harness(overrides = {}) {
  const state = {
    agent: { id: "agent-1", status: "idle" },
    currentMessages: [],
    activeRunSummary: null,
    activeRunSummaryRunId: "",
    activeRunToolCalls: [],
    activeRunToolCallsRunId: "",
    activeRunToolCallsHasMore: false,
    activeRunToolCallsNextOffset: 0,
    historyRunToolCalls: {},
    liveToolOutputs: {},
    chatHydrating: false,
    runSummaryLoading: false,
    runSummaryError: "",
    ...overrides,
  };
  const controller = createChatRenderingController({
    state,
    request: async () => ({}),
    showError: () => {},
    showToast: () => {},
    getElementById: () => null,
  });
  return { controller, state };
}

function finishedRunSummary(runId, triggerMessageId) {
  return {
    run: { id: runId, status: "completed", triggerMessageId },
    toolCalls: [
      { toolUseId: "t-1", tool: "Read", status: "completed" },
      { toolUseId: "t-2", tool: "Bash", status: "completed" },
    ],
    toolCallCount: 68,
  };
}

test("a finished run's card is not attached to the next run's question", () => {
  // The trigger is deliberately absent from currentMessages, which is what a long
  // conversation looks like once the window has scrolled past it.
  const { controller, state } = harness({
    activeRunSummary: finishedRunSummary("run-a", "msg-trigger-a"),
    activeRunSummaryRunId: "run-a",
    activeRunToolCallsRunId: "run-a",
    activeRunToolCallsHasMore: true,
    currentMessages: [
      { id: "msg-new", role: "user", contentText: "the next question", runId: "run-b" },
    ],
  });

  const anchor = controller.runOutcomeAnchorForTest(state.currentMessages);
  assert.equal(anchor, null, "a user turn belonging to another run is not a home for this card");
  assert.equal(
    controller.runOutcomeHomeIsOffScreenForTest(state.currentMessages),
    true,
    "and the placement must know the difference between 'not yet painted' and 'superseded'",
  );
});

test("the card still anchors to its own trigger when that message is loaded", () => {
  const { controller, state } = harness({
    activeRunSummary: finishedRunSummary("run-a", "msg-trigger-a"),
    activeRunSummaryRunId: "run-a",
    currentMessages: [
      { id: "msg-trigger-a", role: "user", contentText: "the question", runId: "run-a" },
      { id: "msg-reply-a", role: "assistant", contentText: "the answer", runId: "run-a" },
    ],
  });

  const anchor = controller.runOutcomeAnchorForTest(state.currentMessages);
  assert.deepEqual(anchor, { messageId: "msg-trigger-a", position: "afterend" });
  assert.equal(controller.runOutcomeHomeIsOffScreenForTest(state.currentMessages), false);
});

test("a newest user turn on the same run is still a valid fallback", () => {
  // Trigger off screen, but the newest question belongs to this very run, so the
  // card is describing work the reader can see. That fallback has to keep working.
  const { controller, state } = harness({
    activeRunSummary: finishedRunSummary("run-a", "msg-trigger-missing"),
    activeRunSummaryRunId: "run-a",
    currentMessages: [
      { id: "msg-user-a", role: "user", contentText: "same run question", runId: "run-a" },
    ],
  });

  const anchor = controller.runOutcomeAnchorForTest(state.currentMessages);
  assert.deepEqual(anchor, { messageId: "msg-user-a", position: "afterend" });
  assert.equal(controller.runOutcomeHomeIsOffScreenForTest(state.currentMessages), false);
});

test("a user turn with no run id yet is not treated as superseding", () => {
  // An optimistic echo has no run id until the server answers. Treating that as a
  // different run would hide the previous card for the wrong reason.
  const { controller, state } = harness({
    activeRunSummary: finishedRunSummary("run-a", "msg-trigger-missing"),
    activeRunSummaryRunId: "run-a",
    currentMessages: [
      { id: "msg-optimistic", role: "user", contentText: "just typed", runId: "" },
    ],
  });

  assert.equal(controller.runOutcomeHomeIsOffScreenForTest(state.currentMessages), false);
  assert.deepEqual(
    controller.runOutcomeAnchorForTest(state.currentMessages),
    { messageId: "msg-optimistic", position: "afterend" },
  );
});

test("an empty transcript is 'not painted yet', never 'superseded'", () => {
  const { controller } = harness({
    activeRunSummary: finishedRunSummary("run-a", "msg-trigger-a"),
    activeRunSummaryRunId: "run-a",
  });
  assert.equal(controller.runOutcomeHomeIsOffScreenForTest([]), false);
});
