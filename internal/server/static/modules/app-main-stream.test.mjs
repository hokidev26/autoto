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
  const { handleAgentStreamEvent, applyAgentLiveSnapshot } = createAppMainStreamWiring({
    t: (key) => key,
    am: (key) => key,
    state,
    $: () => null,
    normalizeWorkStateSnapshot: () => null,
    backgroundTasks: { handleEvent() {}, applySnapshot() {} },
    executionNotifications: { live: async () => {}, initial: async () => {}, snapshot: async () => {} },
    shouldLogAgentEvents: () => false,
    applyPlanEvent() {},
    renderConversationHeaderIdentity() {},
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
    clearRunSummary() {},
    replacePlanState() {},
    replacePendingApprovals() {},
    replacePendingUserQuestions() {},
    applyMessageSnapshot() {},
    normalizeStoredPermissionMode: (value) => value,
    enforcePermissionSelectCap() {},
    renderModelOptions() {},
    refreshReasoningEffortControl() {},
    refreshFastModeControl() {},
    refreshMessageModeControl() {},
    updateWorkspaceMetaPills() {},
    renderWorkbenchShell() {},
    syncMessageComposerBusy: () => { calls.composer += 1; },
    refreshComposerActivityStatus() {},
    loadRunSummary: async () => null,
    loadLatestRunSummary: async () => null,
    scheduleMessageRefresh() {},
    contextManagement: { load: async () => {}, applyStatus() {} },
    navigationRefresh: { request() {} },
    ...deps,
  });
  return { state, calls, handleAgentStreamEvent, applyAgentLiveSnapshot };
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

test("a live snapshot restores provider retry instead of falling back to thinking", async () => {
  const { state, applyAgentLiveSnapshot } = wiring({
    state: { providerRetry: null, liveAssistantActive: false },
  });
  await applyAgentLiveSnapshot({
    agent: { id: "agent-1", status: "running" },
    providerRetry: { attempt: 2, maxAttempts: 4 },
  });
  assert.equal(state.providerRetry.attempt, 2);
  assert.equal(state.providerRetry.maxAttempts, 4);
});

test("a live snapshot without retry clears a stale local retry flag", async () => {
  const { state, applyAgentLiveSnapshot } = wiring({
    state: { providerRetry: { attempt: 9, maxAttempts: 9 } },
  });
  await applyAgentLiveSnapshot({ agent: { id: "agent-1", status: "running" } });
  assert.equal(state.providerRetry, null);
});

test("a terminal latest run does not restore in-flight tools as still running", async () => {
  const { state, applyAgentLiveSnapshot } = wiring({
    state: {
      liveToolOutputs: { old: { status: "running", agentId: "other" } },
    },
  });
  await applyAgentLiveSnapshot({
    agent: { id: "agent-1", status: "interrupted" },
    latestRun: { id: "run-9", status: "interrupted" },
    toolActivity: [{ toolUseId: "t-live", status: "running" }],
  });
  assert.equal(state.liveToolOutputs["t-live"].status, "interrupted");
  assert.equal(state.liveToolOutputs.old.status, "running");
});
