import assert from "node:assert/strict";
import test from "node:test";

import { createAppMainStreamWiring } from "./app-main-stream.mjs";

function wiring({ state: stateOverrides = {}, ...deps } = {}) {
  const calls = { marked: [], toasts: [], composer: 0, nav: [] };
  const state = {
    agent: { id: "agent-1", status: "running" },
    liveToolOutputs: { t1: { status: "running", agentId: "agent-1" } },
    pendingToolApprovals: { t1: {} },
    localInterruptRequestedAt: 0,
    ...stateOverrides,
  };
  const { handleAgentStreamEvent } = createAppMainStreamWiring({
    t: (key) => key,
    am: (key) => key,
    state,
    backgroundTasks: { handleEvent() {} },
    executionNotifications: { live: async () => {} },
    shouldLogAgentEvents: () => false,
    applyPlanEvent() {},
    syncNavigationConversationFromAgent: (...args) => calls.nav.push(args),
    clearCurrentAgentApprovals: () => { state.pendingToolApprovals = {}; },
    clearBlockedToolNotices() {},
    markLiveToolOutputsInterrupted: (id) => {
      calls.marked.push(id);
      const live = state.liveToolOutputs || {};
      for (const [toolUseId, item] of Object.entries(live)) {
        if (item && (!item.agentId || item.agentId === id)) {
          live[toolUseId] = { ...item, status: "interrupted" };
        }
      }
    },
    showToast: (message, tone) => calls.toasts.push({ message, tone }),
    clearLiveAssistantText: () => { state.liveAssistantActive = false; },
    clearLiveImageGenerations() {},
    syncMessageComposerBusy: () => { calls.composer += 1; },
    refreshComposerActivityStatus() {},
    loadRunSummary: async () => null,
    loadLatestRunSummary: async () => null,
    scheduleMessageRefresh() {},
    contextManagement: { load: async () => {} },
    navigationRefresh: { request() {} },
    ...deps,
  });
  return { state, calls, handleAgentStreamEvent };
}

test("a remote interrupt marks live tools, sets interrupted, and toasts other viewers", async () => {
  const { state, calls, handleAgentStreamEvent } = wiring();
  await handleAgentStreamEvent({
    type: "agent.interrupted",
    agentId: "agent-1",
    data: { runId: "run-1" },
  });

  assert.equal(state.agent.status, "interrupted");
  assert.deepEqual(calls.marked, ["agent-1"]);
  assert.equal(state.liveToolOutputs.t1.status, "interrupted");
  assert.deepEqual(calls.toasts, [{ message: "workspace.chat.stoppedByOther", tone: "warn" }]);
  assert.equal(calls.composer > 0, true);
  assert.equal(Object.keys(state.pendingToolApprovals).length, 0);
});

test("the client that clicked Stop does not get a second someone-else toast", async () => {
  const { calls, handleAgentStreamEvent } = wiring({
    state: { localInterruptRequestedAt: Date.now() },
  });
  await handleAgentStreamEvent({
    type: "agent.interrupted",
    agentId: "agent-1",
    data: { runId: "run-1" },
  });

  assert.deepEqual(calls.toasts, []);
  assert.deepEqual(calls.marked, ["agent-1"]);
});

test("agent.done still settles the composer to idle", async () => {
  const { state, handleAgentStreamEvent } = wiring();
  await handleAgentStreamEvent({ type: "agent.done", agentId: "agent-1", data: { runId: "run-1" } });
  assert.equal(state.agent.status, "idle");
});
