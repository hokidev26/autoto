import { $ } from "./dom.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import { findToolActivityByIdentity, renderAgentTaskActivityCardHTML } from "./chat-rendering.mjs?v=message-thread-1-plan-mode-2-user-message-left-1-switch-fix-3-hide-run-loading-1-i18n-shared-1-conversation-boundary-1-subagent-cards-1-message-lifecycle-1-subagent-incremental-1-profile-message-identity-1-profile-avatar-1-provider-errors-1-compact-run-error-1-first-token-task-status-1-tool-activity-lazy-1-tool-protocol-filter-1";

// Background-task changes that should refresh the visible subagent cards.
// Output-streaming events are deliberately excluded: they fire continuously
// while a task writes, and refreshing on them would thrash the transcript.
export const subagentCardRefreshReasons = new Set([
  "loaded",
  "task-loaded",
  "snapshot",
  "wait-finished",
  "cancel-finished",
  "task.created",
  "task.status",
  "task.completed",
]);

// Subagent cards are re-rendered in place from already-loaded run/tool state.
// The coordinator never fetches child tool calls: everything it needs comes
// from state the transcript already holds plus the background-task controller.
//
// getBackgroundTasks is a getter rather than a value because the background
// task controller is constructed with an onChange that calls back into
// scheduleRefresh, so the two are mutually dependent at wiring time.
export function createSubagentCardCoordinator({
  state,
  getBackgroundTasks,
  applyMessageSnapshot,
  loadProjects,
  selectNavigationConversation,
  loadRunSummary,
  showError,
  getMessagesRoot = () => $("messages"),
  getActiveElement = () => globalThis.document?.activeElement,
  requestFrame = (callback) => (globalThis.requestAnimationFrame || ((cb) => globalThis.setTimeout(cb, 0)))(callback),
}) {
  let backgroundTaskAgentLoadGeneration = 0;
  let backgroundTaskAgentLoadInFlight = null;
  let subagentCardRefreshHandle = 0;
  let subagentCardRefreshAgentId = "";
  let subagentCardRefreshSelectionSeq = 0;

  // Identity is the (runId, toolUseId) pair, never the card's position: cards
  // are replaced and reordered, and an index-based key would follow the slot
  // instead of the task.
  function cardIdentity(card) {
    const dataset = card?.dataset || {};
    const runId = String(dataset.runId || "").trim();
    const toolUseId = String(dataset.toolUseId || "").trim();
    if (runId && toolUseId) return JSON.stringify([runId, toolUseId]);
    const taskId = String(dataset.taskId || "").trim();
    return taskId ? `task:${taskId}` : "";
  }

  function captureViewState(root = getMessagesRoot()) {
    if (!root) return { cards: [], focus: null, scrollTop: 0 };
    const active = getActiveElement();
    const cards = [...root.querySelectorAll("[data-subagent-card]")].flatMap((card) => {
      const key = cardIdentity(card);
      if (!key) return [];
      const details = card.matches?.("details") ? [card] : [...card.querySelectorAll("details")];
      return [{
        key,
        status: String(card.dataset?.subagentStatus || ""),
        open: details.map((detail) => Boolean(detail.open)),
      }];
    });
    const focusButton = active?.closest?.("[data-subagent-action]");
    const focusCard = focusButton?.closest?.("[data-subagent-card]");
    const focusKey = cardIdentity(focusCard);
    return {
      cards,
      focus: focusButton && focusKey ? {
        key: focusKey,
        action: focusButton.dataset.subagentAction || "",
        taskId: focusButton.dataset.taskId || "",
        childAgentId: focusButton.dataset.childAgentId || "",
        childRunId: focusButton.dataset.childRunId || "",
      } : null,
      scrollTop: root.scrollTop || 0,
    };
  }

  function restoreViewState(snapshot, root = getMessagesRoot()) {
    if (!root || !snapshot) return;
    const cards = [...root.querySelectorAll("[data-subagent-card]")];
    for (const card of cards) {
      const saved = snapshot.cards?.find((item) => item.key === cardIdentity(card));
      if (!saved) continue;
      const details = card.matches?.("details") ? [card] : [...card.querySelectorAll("details")];
      const statusChanged = saved.status !== String(card.dataset?.subagentStatus || "");
      details.forEach((detail, detailIndex) => {
        // A status change re-renders the summary detail expanded on purpose;
        // only the nested details inherit their previous open state.
        if (detailIndex === 0 && statusChanged) return;
        detail.open = Boolean(saved.open?.[detailIndex]);
      });
    }
    if (snapshot.focus) {
      const card = cards.find((item) => cardIdentity(item) === snapshot.focus.key);
      const button = [...(card?.querySelectorAll?.("[data-subagent-action]") || [])].find((candidate) => (
        (candidate.dataset.subagentAction || "") === snapshot.focus.action
        && (candidate.dataset.taskId || "") === snapshot.focus.taskId
        && (candidate.dataset.childAgentId || "") === snapshot.focus.childAgentId
        && (candidate.dataset.childRunId || "") === snapshot.focus.childRunId
      ));
      if (button) button.focus?.({ preventScroll: true });
      else card?.querySelector?.("summary")?.focus?.({ preventScroll: true });
    }
    root.scrollTop = snapshot.scrollTop || 0;
  }

  function toolActivity(runId, toolUseId) {
    return findToolActivityByIdentity([
      state.liveToolOutputs,
      state.activeRunToolCalls,
      state.activeRunSummary?.toolCalls,
    ], runId, toolUseId);
  }

  function replaceCard(card) {
    const runId = String(card?.dataset?.runId || "").trim();
    const toolUseId = String(card?.dataset?.toolUseId || "").trim();
    if (!runId || !toolUseId || !("outerHTML" in card)) return false;
    const tool = toolActivity(runId, toolUseId);
    if (!tool) return false;
    const task = getBackgroundTasks()?.getTaskByParentTool?.(runId, toolUseId) || null;
    const html = renderAgentTaskActivityCardHTML(tool, task);
    if (!html) return false;
    card.outerHTML = html;
    return true;
  }

  function refreshPreservingUI(agentId = state.agent?.id, selectionSeq = state.projectSelectSeq) {
    if (!agentId || state.agent?.id !== agentId || selectionSeq !== state.projectSelectSeq || state.chatHydrating) return false;
    const root = getMessagesRoot();
    if (!root) return false;
    const cards = [...root.querySelectorAll("[data-subagent-card]")];
    const activityRows = [...root.querySelectorAll("[data-subagent-activity-row]")];
    if (!cards.length && !activityRows.length) return false;
    const snapshot = captureViewState(root);
    const replaced = cards.reduce((count, card) => count + (replaceCard(card) ? 1 : 0), 0);
    // Every selected detail card swapped in place and no compact row depends on
    // the new task state: keep the cheap path and skip the transcript re-render.
    if (!activityRows.length && replaced === cards.length) {
      restoreViewState(snapshot, root);
      return true;
    }
    const rendered = applyMessageSnapshot(state.currentMessages, agentId, { forceRender: true, preserveScroll: true });
    if (rendered) restoreViewState(snapshot, root);
    return rendered;
  }

  function scheduleRefresh(change = {}) {
    const agentId = state.agent?.id || "";
    const reason = String(change.reason || "");
    if (!subagentCardRefreshReasons.has(reason)) return;
    if (!agentId || state.chatHydrating || (change.agentId && change.agentId !== agentId)) return;
    subagentCardRefreshAgentId = agentId;
    subagentCardRefreshSelectionSeq = state.projectSelectSeq;
    if (subagentCardRefreshHandle) return;
    subagentCardRefreshHandle = requestFrame(() => {
      subagentCardRefreshHandle = 0;
      const expectedAgentId = subagentCardRefreshAgentId;
      const expectedSelectionSeq = subagentCardRefreshSelectionSeq;
      subagentCardRefreshAgentId = "";
      subagentCardRefreshSelectionSeq = 0;
      // The selection moved on while the frame was pending; this refresh would
      // paint the previous conversation's cards.
      if (expectedSelectionSeq !== state.projectSelectSeq) return;
      refreshPreservingUI(expectedAgentId, expectedSelectionSeq);
    });
  }

  // Invalidate any in-flight background-task load so a stale result cannot land
  // after the agent is torn down. Called from disconnect/reset paths that must
  // not reach these closure-scoped counters directly.
  function resetAgentLoad() {
    backgroundTaskAgentLoadGeneration += 1;
    backgroundTaskAgentLoadInFlight = null;
  }

  function loadBackgroundTasksForAgent(agentId) {
    const normalizedAgentId = String(agentId || "").trim();
    const backgroundTasks = getBackgroundTasks();
    if (!normalizedAgentId || !backgroundTasks) return Promise.resolve([]);
    if (backgroundTaskAgentLoadInFlight?.agentId === normalizedAgentId) return backgroundTaskAgentLoadInFlight.promise;
    const generation = ++backgroundTaskAgentLoadGeneration;
    const promise = Promise.resolve(backgroundTasks.loadAgent(normalizedAgentId)).then((tasks) => {
      if (generation !== backgroundTaskAgentLoadGeneration || state.agent?.id !== normalizedAgentId) return [];
      return tasks;
    }).finally(() => {
      if (backgroundTaskAgentLoadInFlight?.generation === generation) backgroundTaskAgentLoadInFlight = null;
    });
    backgroundTaskAgentLoadInFlight = { agentId: normalizedAgentId, generation, promise };
    return promise;
  }

  async function navigateToAgent(childAgentId) {
    const agentId = String(childAgentId || "").trim();
    if (!agentId) return;
    let conversation = state.navigationConversations.find((item) => item.agentId === agentId);
    if (!conversation?.targetId) {
      await loadProjects({ autoEnter: false, reason: "subagent-card-navigation" });
      conversation = state.navigationConversations.find((item) => item.agentId === agentId);
    }
    if (!conversation?.targetId) throw new Error(am("conversationUnavailable"));
    await selectNavigationConversation(conversation.targetId);
  }

  async function navigateToRun(childAgentId, childRunId) {
    const agentId = String(childAgentId || "").trim();
    const runId = String(childRunId || "").trim();
    if (agentId && agentId !== state.agent?.id) await navigateToAgent(agentId);
    if (runId && (!agentId || agentId === state.agent?.id)) await loadRunSummary(runId, { agentId: state.agent?.id });
  }

  async function performCardAction(button) {
    const action = button?.dataset?.subagentAction || "";
    const card = button?.closest?.("[data-subagent-card]");
    const taskId = button?.dataset?.taskId || card?.dataset?.taskId || "";
    const childAgentId = button?.dataset?.childAgentId || card?.dataset?.childAgentId || "";
    const childRunId = button?.dataset?.childRunId || card?.dataset?.childRunId || "";
    const backgroundTasks = getBackgroundTasks();
    if (action === "view-task") await backgroundTasks.selectTask(taskId);
    else if (action === "cancel") await backgroundTasks.cancel(taskId);
    else if (action === "open-agent") await navigateToAgent(childAgentId);
    else if (action === "open-run") await navigateToRun(childAgentId, childRunId);
  }

  function bindCardActions() {
    getMessagesRoot()?.addEventListener("click", (event) => {
      const button = event.target?.closest?.("[data-subagent-action]");
      if (!button) return;
      event.preventDefault();
      Promise.resolve(performCardAction(button)).catch(showError);
    });
  }

  return {
    cardIdentity,
    captureViewState,
    restoreViewState,
    toolActivity,
    replaceCard,
    refreshPreservingUI,
    scheduleRefresh,
    loadBackgroundTasksForAgent,
    resetAgentLoad,
    navigateToAgent,
    navigateToRun,
    performCardAction,
    bindCardActions,
  };
}
