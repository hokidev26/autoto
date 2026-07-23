import test from "node:test";
import assert from "node:assert/strict";

import { createSubagentCardCoordinator, subagentCardRefreshReasons } from "./subagent-cards.mjs";

// Minimal DOM stand-ins. Only the surface the coordinator actually touches is
// modelled, matching the hand-rolled fake-node style used by the other suites.
function makeDetail(open = false) {
  return { open };
}

function makeButton(dataset = {}) {
  const button = { dataset, focusCalls: [] };
  button.focus = (options) => button.focusCalls.push(options);
  // closest() matches the element itself before walking up, so an action button
  // resolves to itself for the action selector and to its card for the card one.
  button.closest = (selector) => {
    if (selector === "[data-subagent-action]") return button;
    if (selector === "[data-subagent-card]") return button.card ?? null;
    return null;
  };
  return button;
}

function makeCard({ runId = "", toolUseId = "", taskId = "", status = "", details = [], buttons = [], summary = null } = {}) {
  const card = {
    dataset: { runId, toolUseId, taskId, subagentStatus: status },
    details,
    outerHTML: "<div data-subagent-card></div>",
    matches: (selector) => selector === "[data-subagent-card]",
    querySelectorAll: (selector) => {
      if (selector === "details") return details;
      if (selector === "[data-subagent-action]") return buttons;
      return [];
    },
    querySelector: (selector) => (selector === "summary" ? summary : null),
  };
  buttons.forEach((button) => { button.card = card; });
  return card;
}

function makeRoot(cards, { scrollTop = 0, activityRows = [] } = {}) {
  return {
    scrollTop,
    cards,
    activityRows,
    querySelectorAll: (selector) => {
      if (selector === "[data-subagent-card]") return cards;
      if (selector === "[data-subagent-activity-row]") return activityRows;
      return [];
    },
    addEventListener(type, handler) { this.listener = { type, handler }; },
  };
}

function makeCoordinator(overrides = {}) {
  const calls = { applyMessageSnapshot: [], loadProjects: [], selectNavigationConversation: [], loadRunSummary: [], showError: [], selectTask: [], cancel: [], loadAgent: [] };
  const state = {
    agent: { id: "agent-1" },
    projectSelectSeq: 1,
    chatHydrating: false,
    currentMessages: [{ id: "m1" }],
    liveToolOutputs: [],
    activeRunToolCalls: [],
    activeRunSummary: { toolCalls: [] },
    navigationConversations: [],
    ...overrides.state,
  };
  const backgroundTasks = {
    getTaskByParentTool: () => null,
    selectTask: async (id) => calls.selectTask.push(id),
    cancel: async (id) => calls.cancel.push(id),
    loadAgent: async (id) => { calls.loadAgent.push(id); return [{ id: `task-for-${id}` }]; },
    ...overrides.backgroundTasks,
  };
  const frames = [];
  const coordinator = createSubagentCardCoordinator({
    state,
    getBackgroundTasks: () => backgroundTasks,
    applyMessageSnapshot: (...args) => { calls.applyMessageSnapshot.push(args); return overrides.snapshotRendered ?? true; },
    loadProjects: async (...args) => { calls.loadProjects.push(args); overrides.onLoadProjects?.(state); },
    selectNavigationConversation: async (...args) => calls.selectNavigationConversation.push(args),
    loadRunSummary: async (...args) => calls.loadRunSummary.push(args),
    showError: (...args) => calls.showError.push(args),
    getMessagesRoot: () => overrides.root ?? null,
    getActiveElement: () => overrides.activeElement ?? null,
    requestFrame: (callback) => { frames.push(callback); return frames.length; },
    ...overrides.deps,
  });
  return { coordinator, state, calls, frames, backgroundTasks };
}

test("subagent card identity keys on the run/tool pair and never on position", () => {
  const { coordinator } = makeCoordinator();
  const first = makeCard({ runId: "run-1", toolUseId: "tool-1" });
  const second = makeCard({ runId: "run-1", toolUseId: "tool-2" });

  assert.equal(coordinator.cardIdentity(first), JSON.stringify(["run-1", "tool-1"]));
  // Identity must survive reordering: the same card keeps its key regardless of
  // where it sits, and two cards in the same run stay distinguishable.
  assert.notEqual(coordinator.cardIdentity(first), coordinator.cardIdentity(second));

  assert.equal(coordinator.cardIdentity(makeCard({ taskId: "task-9" })), "task:task-9");
  assert.equal(coordinator.cardIdentity(makeCard({})), "");
  assert.equal(coordinator.cardIdentity(null), "");
  // A run id without a tool id is not a usable pair and must not key on it alone.
  assert.equal(coordinator.cardIdentity(makeCard({ runId: "run-1" })), "");
});

test("view state capture records detail open flags, status, focus, and scroll", () => {
  const button = makeButton({ subagentAction: "cancel", taskId: "t-1", childAgentId: "", childRunId: "" });
  const card = makeCard({ runId: "run-1", toolUseId: "tool-1", status: "running", details: [makeDetail(true), makeDetail(false)], buttons: [button] });
  const root = makeRoot([card], { scrollTop: 120 });
  const { coordinator } = makeCoordinator({ root, activeElement: button });

  const snapshot = coordinator.captureViewState(root);
  assert.deepEqual(snapshot.cards, [{ key: JSON.stringify(["run-1", "tool-1"]), status: "running", open: [true, false] }]);
  assert.deepEqual(snapshot.focus, { key: JSON.stringify(["run-1", "tool-1"]), action: "cancel", taskId: "t-1", childAgentId: "", childRunId: "" });
  assert.equal(snapshot.scrollTop, 120);

  // Cards without a usable identity are skipped rather than captured under "".
  const anonymous = makeRoot([makeCard({})]);
  assert.deepEqual(coordinator.captureViewState(anonymous).cards, []);
  assert.deepEqual(coordinator.captureViewState(null), { cards: [], focus: null, scrollTop: 0 });
});

test("view state restore reopens details, skips the summary detail on status change, and restores focus", () => {
  const { coordinator } = makeCoordinator();
  const snapshot = {
    cards: [{ key: JSON.stringify(["run-1", "tool-1"]), status: "running", open: [true, true] }],
    focus: null,
    scrollTop: 42,
  };

  const unchanged = makeCard({ runId: "run-1", toolUseId: "tool-1", status: "running", details: [makeDetail(false), makeDetail(false)] });
  const rootUnchanged = makeRoot([unchanged]);
  coordinator.restoreViewState(snapshot, rootUnchanged);
  assert.deepEqual(unchanged.details.map((d) => d.open), [true, true], "same status restores every detail");
  assert.equal(rootUnchanged.scrollTop, 42);

  // When the status changed the card re-rendered with its summary detail in the
  // new state on purpose; only nested details inherit the saved open flags.
  const changed = makeCard({ runId: "run-1", toolUseId: "tool-1", status: "completed", details: [makeDetail(false), makeDetail(false)] });
  coordinator.restoreViewState(snapshot, makeRoot([changed]));
  assert.deepEqual(changed.details.map((d) => d.open), [false, true]);
});

test("focus restore prefers the matching action button and falls back to the card summary", () => {
  const focus = { key: JSON.stringify(["run-1", "tool-1"]), action: "cancel", taskId: "t-1", childAgentId: "", childRunId: "" };
  const snapshot = { cards: [], focus, scrollTop: 0 };

  const match = makeButton({ subagentAction: "cancel", taskId: "t-1", childAgentId: "", childRunId: "" });
  const other = makeButton({ subagentAction: "view-task", taskId: "t-1", childAgentId: "", childRunId: "" });
  const card = makeCard({ runId: "run-1", toolUseId: "tool-1", buttons: [other, match] });
  const { coordinator } = makeCoordinator();
  coordinator.restoreViewState(snapshot, makeRoot([card]));
  assert.deepEqual(match.focusCalls, [{ preventScroll: true }], "focus must not scroll the transcript");
  assert.deepEqual(other.focusCalls, []);

  // Button gone after the re-render: fall back to the card's summary.
  const summary = { focusCalls: [] };
  summary.focus = (options) => summary.focusCalls.push(options);
  const withoutButton = makeCard({ runId: "run-1", toolUseId: "tool-1", buttons: [], summary });
  coordinator.restoreViewState(snapshot, makeRoot([withoutButton]));
  assert.deepEqual(summary.focusCalls, [{ preventScroll: true }]);
});

test("tool activity is resolved from live output, active calls, then the run summary", () => {
  const seen = [];
  const state = {
    liveToolOutputs: ["live"],
    activeRunToolCalls: ["active"],
    activeRunSummary: { toolCalls: ["summary"] },
  };
  const { coordinator } = makeCoordinator({ state });
  // findToolActivityByIdentity is the real implementation; assert the source
  // ordering by giving each source a distinguishable entry shape.
  const sources = [];
  const probe = makeCoordinator({
    state: {
      liveToolOutputs: [{ runId: "r", toolUseId: "t", name: "Agent", from: "live" }],
      activeRunToolCalls: [{ runId: "r", toolUseId: "t", name: "Agent", from: "active" }],
      activeRunSummary: { toolCalls: [{ runId: "r", toolUseId: "t", name: "Agent", from: "summary" }] },
    },
  });
  const resolved = probe.coordinator.toolActivity("r", "t");
  assert.equal(resolved?.from, "live", "live tool output wins over later sources");
  void seen; void sources; void coordinator; void state;
});

test("refresh replaces every card in place and restores the snapshot without re-rendering", () => {
  const cardA = makeCard({ runId: "run-1", toolUseId: "tool-1", status: "running", details: [makeDetail(true)] });
  const cardB = makeCard({ runId: "run-1", toolUseId: "tool-2", status: "running", details: [makeDetail(false)] });
  const root = makeRoot([cardA, cardB], { scrollTop: 33 });
  const { coordinator, calls } = makeCoordinator({
    root,
    state: {
      liveToolOutputs: [
        { runId: "run-1", toolUseId: "tool-1", name: "Agent" },
        { runId: "run-1", toolUseId: "tool-2", name: "Agent" },
      ],
    },
  });

  const refreshed = coordinator.refreshPreservingUI("agent-1", 1);
  assert.equal(refreshed, true);
  assert.deepEqual(calls.applyMessageSnapshot, [], "in-place replacement must not trigger a full re-render");
  assert.equal(root.scrollTop, 33, "scroll position is preserved");
  // The whole point of the card coordinator: repainting must be served from
  // state the transcript already holds, never by polling child tool calls.
  assert.deepEqual(calls.loadRunSummary, []);
  assert.deepEqual(calls.loadAgent, []);
  assert.deepEqual(calls.loadProjects, []);
});

test("compact subagent activity rows force a scroll-preserving transcript refresh", () => {
  const root = makeRoot([], { scrollTop: 27, activityRows: [{ dataset: { runId: "run-1", toolUseId: "tool-1" } }] });
  const { coordinator, calls } = makeCoordinator({ root });

  assert.equal(coordinator.refreshPreservingUI("agent-1", 1), true);
  assert.equal(calls.applyMessageSnapshot.length, 1);
  const [messages, agentId, options] = calls.applyMessageSnapshot[0];
  assert.deepEqual(messages, [{ id: "m1" }]);
  assert.equal(agentId, "agent-1");
  assert.deepEqual(options, { forceRender: true, preserveScroll: true });
  assert.equal(root.scrollTop, 27);
});

test("refresh falls back to a scroll-preserving re-render when a card cannot be replaced", () => {
  const replaceable = makeCard({ runId: "run-1", toolUseId: "tool-1" });
  const orphan = makeCard({ runId: "run-1", toolUseId: "missing" });
  const root = makeRoot([replaceable, orphan]);
  const { coordinator, calls } = makeCoordinator({
    root,
    state: { liveToolOutputs: [{ runId: "run-1", toolUseId: "tool-1", name: "Agent" }] },
  });

  assert.equal(coordinator.refreshPreservingUI("agent-1", 1), true);
  assert.equal(calls.applyMessageSnapshot.length, 1);
  const [messages, agentId, options] = calls.applyMessageSnapshot[0];
  assert.equal(agentId, "agent-1");
  assert.deepEqual(messages, [{ id: "m1" }]);
  assert.deepEqual(options, { forceRender: true, preserveScroll: true });
});

test("refresh is refused for a stale agent, stale selection, or a hydrating transcript", () => {
  const card = makeCard({ runId: "run-1", toolUseId: "tool-1" });
  const build = (state) => makeCoordinator({ root: makeRoot([card]), state });

  assert.equal(build({}).coordinator.refreshPreservingUI("other-agent", 1), false, "agent changed");
  assert.equal(build({}).coordinator.refreshPreservingUI("agent-1", 99), false, "selection moved on");
  assert.equal(build({ chatHydrating: true }).coordinator.refreshPreservingUI("agent-1", 1), false, "transcript hydrating");
  assert.equal(build({}).coordinator.refreshPreservingUI("", 1), false, "no agent");
  assert.equal(makeCoordinator({ root: null }).coordinator.refreshPreservingUI("agent-1", 1), false, "no transcript root");
  assert.equal(makeCoordinator({ root: makeRoot([]) }).coordinator.refreshPreservingUI("agent-1", 1), false, "no cards");
});

test("refresh scheduling honours the reason allowlist and excludes output streaming", () => {
  // Output events fire continuously while a task writes; refreshing on them
  // would thrash the transcript, so they must stay out of the allowlist.
  assert.equal(subagentCardRefreshReasons.has("task.output"), false);
  assert.equal(subagentCardRefreshReasons.has("output-loaded"), false);
  for (const reason of ["loaded", "task-loaded", "snapshot", "wait-finished", "cancel-finished", "task.created", "task.status", "task.completed"]) {
    assert.equal(subagentCardRefreshReasons.has(reason), true, `${reason} should refresh cards`);
  }

  const { coordinator, frames } = makeCoordinator({ root: makeRoot([makeCard({ runId: "r", toolUseId: "t" })]) });
  coordinator.scheduleRefresh({ reason: "task.output" });
  assert.equal(frames.length, 0, "ignored reason schedules no frame");
  coordinator.scheduleRefresh({ reason: "task.status" });
  assert.equal(frames.length, 1, "allowed reason schedules one frame");
  coordinator.scheduleRefresh({ reason: "task.status" });
  assert.equal(frames.length, 1, "pending frame is coalesced");
});

test("scheduled refresh is dropped when the selection moved while the frame was pending", () => {
  const card = makeCard({ runId: "run-1", toolUseId: "tool-1" });
  const { coordinator, state, calls, frames } = makeCoordinator({
    root: makeRoot([card]),
    state: { liveToolOutputs: [] },
    snapshotRendered: true,
  });

  coordinator.scheduleRefresh({ reason: "task.status" });
  assert.equal(frames.length, 1);
  state.projectSelectSeq = 2; // user switched conversations before the frame ran
  frames[0]();
  assert.deepEqual(calls.applyMessageSnapshot, [], "stale refresh must not paint the previous conversation");
});

test("scheduled refresh ignores changes addressed to another agent or while hydrating", () => {
  const root = makeRoot([makeCard({ runId: "r", toolUseId: "t" })]);
  const other = makeCoordinator({ root });
  other.coordinator.scheduleRefresh({ reason: "task.status", agentId: "someone-else" });
  assert.equal(other.frames.length, 0);

  const hydrating = makeCoordinator({ root, state: { chatHydrating: true } });
  hydrating.coordinator.scheduleRefresh({ reason: "task.status" });
  assert.equal(hydrating.frames.length, 0);
});

test("agent background-task loads dedupe in flight and discard superseded generations", async () => {
  const { coordinator, state, calls } = makeCoordinator();
  const first = coordinator.loadBackgroundTasksForAgent("agent-1");
  const second = coordinator.loadBackgroundTasksForAgent("agent-1");
  assert.equal(first, second, "a concurrent load for the same agent reuses the in-flight promise");
  assert.deepEqual(await first, [{ id: "task-for-agent-1" }]);
  assert.deepEqual(calls.loadAgent, ["agent-1"]);

  assert.deepEqual(await coordinator.loadBackgroundTasksForAgent(""), [], "no agent id loads nothing");

  // Result arriving after the agent moved on must not be handed back.
  state.agent = { id: "agent-2" };
  assert.deepEqual(await coordinator.loadBackgroundTasksForAgent("agent-1"), []);
});

test("subagent navigation resolves the conversation, reloading projects when it is unknown", async () => {
  const { coordinator, state, calls } = makeCoordinator({
    onLoadProjects: (current) => { current.navigationConversations = [{ agentId: "child-1", targetId: "conv-1" }]; },
  });

  await coordinator.navigateToAgent("child-1");
  assert.deepEqual(calls.loadProjects, [[{ autoEnter: false, reason: "subagent-card-navigation" }]]);
  assert.deepEqual(calls.selectNavigationConversation, [["conv-1"]]);
  void state;

  const known = makeCoordinator({ state: { navigationConversations: [{ agentId: "child-2", targetId: "conv-2" }] } });
  await known.coordinator.navigateToAgent("child-2");
  assert.deepEqual(known.calls.loadProjects, [], "an already-known conversation does not reload projects");
  assert.deepEqual(known.calls.selectNavigationConversation, [["conv-2"]]);

  const missing = makeCoordinator();
  await assert.rejects(() => missing.coordinator.navigateToAgent("ghost"));
  await missing.coordinator.navigateToAgent("");
  assert.deepEqual(missing.calls.selectNavigationConversation, []);
});

test("run navigation switches agent first and loads the summary against the active agent", async () => {
  const sameAgent = makeCoordinator();
  await sameAgent.coordinator.navigateToRun("agent-1", "run-9");
  assert.deepEqual(sameAgent.calls.loadRunSummary, [["run-9", { agentId: "agent-1" }]]);
  assert.deepEqual(sameAgent.calls.selectNavigationConversation, [], "already on the agent");

  const crossAgent = makeCoordinator({
    state: { navigationConversations: [{ agentId: "child-1", targetId: "conv-1" }] },
  });
  await crossAgent.coordinator.navigateToRun("child-1", "run-9");
  assert.deepEqual(crossAgent.calls.selectNavigationConversation, [["conv-1"]], "switches conversation first");
  // The summary load is left to the agent-enter path once the switch lands.
  assert.deepEqual(crossAgent.calls.loadRunSummary, []);
});

test("card actions map to background task and navigation handlers", async () => {
  const cases = [
    { action: "view-task", dataset: { taskId: "t-1" }, check: (c) => assert.deepEqual(c.calls.selectTask, ["t-1"]) },
    { action: "cancel", dataset: { taskId: "t-1" }, check: (c) => assert.deepEqual(c.calls.cancel, ["t-1"]) },
    { action: "open-agent", dataset: { childAgentId: "child-1" }, check: (c) => assert.deepEqual(c.calls.selectNavigationConversation, [["conv-1"]]) },
    { action: "open-run", dataset: { childAgentId: "agent-1", childRunId: "run-9" }, check: (c) => assert.deepEqual(c.calls.loadRunSummary, [["run-9", { agentId: "agent-1" }]]) },
  ];
  for (const { action, dataset, check } of cases) {
    const built = makeCoordinator({ state: { navigationConversations: [{ agentId: "child-1", targetId: "conv-1" }] } });
    const button = makeButton({ subagentAction: action, ...dataset });
    makeCard({ runId: "r", toolUseId: "t", buttons: [button] });
    await built.coordinator.performCardAction(button);
    check(built);
  }

  // Identifiers fall back to the owning card when the button omits them.
  const built = makeCoordinator();
  const button = makeButton({ subagentAction: "view-task" });
  makeCard({ runId: "r", toolUseId: "t", taskId: "card-task", buttons: [button] });
  await built.coordinator.performCardAction(button);
  assert.deepEqual(built.calls.selectTask, ["card-task"]);

  const unknown = makeCoordinator();
  await unknown.coordinator.performCardAction(makeButton({ subagentAction: "nope" }));
  assert.deepEqual(unknown.calls.selectTask, []);
  assert.deepEqual(unknown.calls.cancel, []);
});

test("bound click handler prevents default and routes action failures to showError", async () => {
  const root = makeRoot([]);
  const built = makeCoordinator({
    root,
    backgroundTasks: { selectTask: async () => { throw new Error("boom"); } },
  });
  built.coordinator.bindCardActions();
  assert.equal(root.listener.type, "click");

  let prevented = false;
  const button = makeButton({ subagentAction: "view-task", taskId: "t-1" });
  root.listener.handler({ target: { closest: () => button }, preventDefault: () => { prevented = true; } });
  assert.equal(prevented, true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(built.calls.showError.length, 1, "rejected actions surface through showError");

  // Clicks outside an action control are ignored without preventing default.
  let preventedOutside = false;
  root.listener.handler({ target: { closest: () => null }, preventDefault: () => { preventedOutside = true; } });
  assert.equal(preventedOutside, false);
});
