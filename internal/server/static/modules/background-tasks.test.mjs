import test from "node:test";
import assert from "node:assert/strict";

import {
  createBackgroundTasksController,
  normalizeBackgroundTask,
  normalizeContinuation,
  summarizeBackgroundTasks,
} from "./background-tasks.mjs";

test("background task normalization accepts API aliases and derives duration", () => {
  const task = normalizeBackgroundTask({
    taskId: "task-1",
    taskKind: "subagent",
    state: "completed",
    started_at: "2026-07-16T10:00:00Z",
    endedAt: "2026-07-16T10:00:03Z",
    childAgent: { id: "child-1" },
    childRun: { id: "run-1" },
  });
  assert.equal(task.id, "task-1");
  assert.equal(task.kind, "subagent");
  assert.equal(task.durationMs, 3000);
  assert.equal(task.childAgentId, "child-1");
  assert.equal(task.childRunId, "run-1");
});

test("Agent task list requests the maximum supported history window", async () => {
  const requests = [];
  const controller = createBackgroundTasksController({
    request: async (path, options) => {
      requests.push({ path, method: options.method });
      return { tasks: [
        { id: "task-newest", status: "running", createdAt: "2026-07-19T10:00:02Z" },
        { id: "task-older", status: "queued", createdAt: "2026-07-19T10:00:01Z" },
      ] };
    },
  });
  controller.setAgent("agent-history");
  await controller.loadAgent("agent-history");
  assert.deepEqual(requests, [{ path: "/api/agents/agent-history/background-tasks?limit=100", method: "GET" }]);
  assert.deepEqual(controller.state().order, ["task-newest", "task-older"]);
});

test("task summary derives readable titles and separates running from queued work", () => {
  const shell = normalizeBackgroundTask({ id: "shell-1", kind: "shell", status: "running", publicSummary: { program: "go", subcommand: "test" } });
  const child = normalizeBackgroundTask({ id: "agent-1", kind: "agent", status: "queued", publicSummary: { description: "Inspect auth flow", model: "codex:gpt" } });
  assert.equal(shell.title, "go test");
  assert.equal(child.title, "Inspect auth flow");
  const summary = summarizeBackgroundTasks([
    shell,
    child,
    { id: "approval-1", kind: "shell", status: "waiting_approval", publicSummary: JSON.stringify({ program: "npm" }) },
    { id: "done-1", kind: "shell", status: "succeeded", title: "Finished" },
  ]);
  assert.equal(summary.current.id, "shell-1");
  assert.equal(summary.current.title, "go test");
  assert.equal(summary.runningCount, 1);
  assert.equal(summary.queuedCount, 2);
  assert.equal(summary.activeCount, 3);
  assert.equal(summary.totalCount, 4);
});

test("foreground generation activity temporarily owns the composer task summary", () => {
  function element() {
    const classes = new Set();
    return {
      attributes: {},
      className: "",
      classList: {
        contains: (name) => classes.has(name),
        toggle(name, force) {
          if (force) classes.add(name);
          else classes.delete(name);
        },
      },
      setAttribute(name, value) { this.attributes[name] = String(value); },
    };
  }
  const elements = {
    headerTaskSummaryBtn: element(),
    headerCurrentTaskText: element(),
    headerTaskQueueBadge: element(),
    headerTaskStatusDot: element(),
  };
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    documentRef: { getElementById: (id) => elements[id] || null },
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{ id: "task-1", status: "running", title: "Run checks" }] }, { agentId: "agent-1" });

  assert.equal(controller.setForegroundActivity({ kind: "thinking", text: "思考中" }), true);
  assert.deepEqual(controller.state().foregroundActivity, { kind: "thinking", text: "思考中", tone: "running" });
  assert.equal(elements.headerCurrentTaskText.textContent, "思考中");
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot running");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-busy"], "true");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-label"], "思考中");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-task"), true);
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-foreground-activity"), true);

  controller.setForegroundActivity(null);
  assert.equal(controller.state().foregroundActivity, null);
  assert.equal(elements.headerCurrentTaskText.textContent, "Run checks");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-busy"], "false");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-foreground-activity"), false);
});

test("task panel reports open and close transitions for the shared chat utility column", async () => {
  const transitions = [];
  const controller = createBackgroundTasksController({
    request: async (path) => path.includes("/output?")
      ? { chunks: [] }
      : { id: "task-1", agentId: "agent-1", kind: "shell", status: "running", title: "Run checks" },
    onOpenChange: (open, detail) => transitions.push({ open, reason: detail.reason }),
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{ id: "task-1", agentId: "agent-1", status: "running", title: "Run checks" }] }, { agentId: "agent-1" });

  await controller.selectTask("task-1");
  assert.equal(controller.state().trayOpen, true);
  assert.deepEqual(transitions[0], { open: true, reason: "task-selected" });

  assert.equal(controller.closeTray("details-open"), true);
  assert.equal(controller.state().trayOpen, false);
  assert.deepEqual(transitions[1], { open: false, reason: "details-open" });
  assert.equal(controller.closeTray(), false);
});

test("selecting a historical task hydrates its exact task before opening details", async () => {
  const requests = [];
  const controller = createBackgroundTasksController({
    request: async (path, options) => {
      requests.push({ path, method: options.method });
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      return {
        id: "task-history",
        agentId: "agent-1",
        parentRunId: "run-history",
        parentToolUseId: "tool-history",
        kind: "agent",
        status: "succeeded",
        revision: 4,
        publicSummary: { description: "Historical task" },
      };
    },
  });
  controller.setAgent("agent-1");

  const task = await controller.selectTask("task-history");

  assert.equal(task.id, "task-history");
  assert.equal(controller.state().trayOpen, true);
  assert.equal(controller.state().selected, "task-history");
  assert.deepEqual(requests, [
    { path: "/api/background-tasks/task-history", method: "GET" },
    { path: "/api/background-tasks/task-history/output?afterSequence=0", method: "GET" },
  ]);
});

test("controller reconciles snapshots and live task events without duplicating order", () => {
  const controller = createBackgroundTasksController({ request: async () => [] });
  controller.setAgent("agent-1");
  controller.applySnapshot({
    agent: { id: "agent-1" },
    backgroundTasks: [{ id: "task-1", kind: "bash", status: "running" }],
    recentBackgroundTasks: [{ id: "task-2", kind: "subagent", status: "completed" }],
    continuation: { mode: "safe", continuationCount: 2, totalTurns: 8, maxTotalTurns: 20 },
  });
  controller.handleEvent({ type: "task.status", agentId: "agent-1", data: { taskId: "task-1", status: "waiting" } });
  controller.handleEvent({ type: "task.completed", agentId: "agent-1", data: { taskId: "task-1", status: "completed" } });

  const state = controller.state();
  assert.deepEqual(state.order, ["task-1", "task-2"]);
  assert.equal(state.tasksById["task-1"].status, "completed");
  assert.equal(state.continuation.mode, "safe");
  assert.equal(state.continuation.count, 2);
  assert.equal(state.continuation.turnsUsed, 8);
});

test("output pagination tracks cursors, deduplicates chunks, and marks truncation", async () => {
  const requests = [];
  const pages = [
    { chunks: [{ sequence: 1, text: "one\n" }, { sequence: 2, text: "two\n" }], nextSequence: 2, hasMore: true },
    { chunks: [{ sequence: 2, text: "two\n" }, { sequence: 3, text: "three\n" }], nextSequence: 3, truncated: true, hasMore: false },
  ];
  const controller = createBackgroundTasksController({
    request: async (path) => {
      requests.push(path);
      return pages.shift();
    },
  });
  await controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{ id: "task-1", agentId: "agent-1", status: "running" }] }, { agentId: "agent-1" });
  await controller.loadOutput("task-1", { afterSequence: 0 });
  await controller.loadOutput("task-1");

  assert.deepEqual(requests, [
    "/api/background-tasks/task-1/output?afterSequence=0",
    "/api/background-tasks/task-1/output?afterSequence=2",
  ]);
  assert.deepEqual(controller.state().outputs["task-1"].map((chunk) => chunk.text), ["one\n", "two\n", "three\n"]);
  assert.equal(controller.state().outputCursors["task-1"], 3);
  assert.equal(controller.getTask("task-1").truncated, true);
});

test("wait and cancel use agreed endpoints and expose busy state during requests", async () => {
  const calls = [];
  let releaseCancel;
  const controller = createBackgroundTasksController({
    request: async (path, options) => {
      calls.push({ path, method: options.method });
      if (path.endsWith("/wait")) return { id: "task-1", status: "completed" };
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.endsWith("/cancel")) return new Promise((resolve) => { releaseCancel = () => resolve({ id: "task-2", status: "cancelled" }); });
      return {};
    },
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [
    { id: "task-1", status: "running" },
    { id: "task-2", status: "queued" },
  ] }, { agentId: "agent-1" });

  await controller.wait("task-1");
  const cancellation = controller.cancel("task-2");
  assert.deepEqual(controller.state().cancelBusy, ["task-2"]);
  releaseCancel();
  await cancellation;

  assert.deepEqual(calls.map((call) => call.path), [
    "/api/background-tasks/task-1/wait",
    "/api/background-tasks/task-1/output?afterSequence=0",
    "/api/background-tasks/task-2/cancel",
  ]);
  assert.equal(controller.getTask("task-2").status, "cancelled");
  assert.deepEqual(controller.state().cancelBusy, []);
});

test("in-flight task requests cannot repopulate state after an Agent change", async () => {
  const pending = new Map();
  const controller = createBackgroundTasksController({
    request: (path) => new Promise((resolve) => pending.set(path, resolve)),
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{ id: "task-1", agentId: "agent-1", status: "running" }] }, { agentId: "agent-1" });

  const taskRequest = controller.loadTask("task-1");
  const outputRequest = controller.loadOutput("task-1", { afterSequence: 0 });
  const waitRequest = controller.wait("task-1");
  const cancelRequest = controller.cancel("task-1");
  controller.setAgent("agent-2");

  pending.get("/api/background-tasks/task-1")?.({ id: "task-1", agentId: "agent-1", status: "completed" });
  pending.get("/api/background-tasks/task-1/output?afterSequence=0")?.({ chunks: [{ sequence: 1, text: "stale" }] });
  pending.get("/api/background-tasks/task-1/wait")?.({ id: "task-1", agentId: "agent-1", status: "completed" });
  pending.get("/api/background-tasks/task-1/cancel")?.({ id: "task-1", agentId: "agent-1", status: "cancelled" });
  await Promise.all([taskRequest, outputRequest, waitRequest, cancelRequest]);

  const state = controller.state();
  assert.equal(state.agentId, "agent-2");
  assert.deepEqual(state.tasksById, {});
  assert.deepEqual(state.outputs, {});
  assert.deepEqual(state.waitBusy, []);
  assert.deepEqual(state.cancelBusy, []);
});

test("continuation events retain budgets while updating lifecycle status", () => {
  const normalized = normalizeContinuation({
    autoContinuationMode: "safe",
    count: 1,
    segmentTurns: 4,
    budgets: { maxTotalTurns: 24, maxTokens: 8000, durationMs: 120000 },
  });
  assert.equal(normalized.mode, "safe");
  assert.equal(normalized.maxTotalTurns, 24);
  assert.equal(normalized.tokenBudget, 8000);

  const controller = createBackgroundTasksController({ request: async () => ({}) });
  controller.setAgent("agent-1");
  controller.applySnapshot({ continuation: normalized }, { agentId: "agent-1" });
  controller.handleEvent({ type: "agent.continuation_blocked", agentId: "agent-1", data: { reason: "waiting approval", waitingTaskId: "task-3" } });
  controller.handleEvent({ type: "agent.budget_exhausted", agentId: "agent-1", data: { reason: "token budget", tokensUsed: 8000 } });
  const continuation = controller.getContinuation();
  assert.equal(continuation.status, "budget_exhausted");
  assert.equal(continuation.reason, "token budget");
  assert.equal(continuation.tokenBudget, 8000);
  assert.equal(continuation.tokensUsed, 8000);
});

test("continuation budgets of -1 render as unlimited instead of a raw sentinel", () => {
  const normalized = normalizeContinuation({
    autoContinuationMode: "safe",
    segmentTurns: 40,
    turnsUsed: 12,
    tokensUsed: 4096,
    elapsedMs: 65000,
    maxTotalTurns: -1,
    tokenBudget: -1,
    durationBudgetMs: -1,
  });
  // -1 is the durable config value and must survive normalization untouched.
  assert.equal(normalized.maxTotalTurns, -1);
  assert.equal(normalized.tokenBudget, -1);
  assert.equal(normalized.durationBudgetMs, -1);

  const controller = createBackgroundTasksController({ request: async () => ({}) });
  controller.setAgent("agent-1");
  controller.applySnapshot({ continuation: normalized }, { agentId: "agent-1" });
  const html = controller.renderContinuationStatusHTML();
  assert.match(html, /12 \/ 不限制/);
  assert.match(html, /4096 \/ 不限制/);
  assert.doesNotMatch(html, /\/ -1/);
  assert.equal(html.match(/不限制/g).length, 3);
});

test("normalization explicitly preserves task ownership, association, revision, public fields, and timestamps", () => {
  const task = normalizeBackgroundTask({
    taskId: "task-explicit",
    owner_agent_id: "agent-owner",
    parent_run_id: "run-parent",
    parent_tool_use_id: "tool-parent",
    task_revision: "12",
    taskKind: "agent",
    state: "failed",
    public_summary: JSON.stringify({ description: "Inspect safely" }),
    child_agent_id: "agent-child",
    child_run_id: "run-child",
    error_code: "child_failed",
    error_message: "Child failed",
    created_at: "2026-07-19T10:00:00Z",
    started_at: "2026-07-19T10:00:01Z",
    cancel_requested_at: "2026-07-19T10:00:02Z",
    completed_at: "2026-07-19T10:00:03Z",
    updated_at: "2026-07-19T10:00:04Z",
  });

  assert.equal(task.ownerAgentId, "agent-owner");
  assert.equal(task.agentId, "agent-owner");
  assert.equal(task.parentRunId, "run-parent");
  assert.equal(task.parentToolUseId, "tool-parent");
  assert.equal(task.revision, 12);
  assert.deepEqual(task.publicSummary, { description: "Inspect safely" });
  assert.deepEqual(task.summary, { description: "Inspect safely" });
  assert.equal(task.childAgentId, "agent-child");
  assert.equal(task.childRunId, "run-child");
  assert.equal(task.errorCode, "child_failed");
  assert.equal(task.createdAt, "2026-07-19T10:00:00Z");
  assert.equal(task.startedAt, "2026-07-19T10:00:01Z");
  assert.equal(task.cancelRequestedAt, "2026-07-19T10:00:02Z");
  assert.equal(task.completedAt, "2026-07-19T10:00:03Z");
  assert.equal(task.updatedAt, "2026-07-19T10:00:04Z");
});

test("parent tool lookup is isolated by the parent run composite key", () => {
  const controller = createBackgroundTasksController({ request: async () => ({}) });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [
    { id: "task-run-a", parentRunId: "run-a", parentToolUseId: "tool-shared", status: "running", publicSummary: { description: "A" } },
    { id: "task-run-b", parentRunId: "run-b", parentToolUseId: "tool-shared", status: "queued", publicSummary: { description: "B" } },
  ] }, { agentId: "agent-1" });

  assert.equal(controller.getTaskByParentTool("run-a", "tool-shared").id, "task-run-a");
  assert.equal(controller.getTaskByParentTool("run-b", "tool-shared").id, "task-run-b");
  assert.equal(controller.getTaskByParentTool("", "tool-shared"), null);
  assert.equal(controller.getTaskByParentTool("run-c", "tool-shared"), null);
});

test("revision guards discard stale events and protect hydrated fields on equal revisions", () => {
  const controller = createBackgroundTasksController({ request: async () => ({}) });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{
    id: "task-revision",
    ownerAgentId: "agent-1",
    parentRunId: "run-1",
    parentToolUseId: "tool-1",
    kind: "agent",
    status: "running",
    revision: 8,
    title: "Hydrated title",
    publicSummary: { description: "Hydrated summary" },
    childAgentId: "child-1",
    childRunId: "child-run-1",
    createdAt: "2026-07-19T10:00:00Z",
    updatedAt: "2026-07-19T10:00:01Z",
  }] }, { agentId: "agent-1" });

  controller.handleEvent({ type: "task.status", agentId: "agent-1", data: { taskId: "task-revision", kind: "agent", status: "failed", revision: 7 } });
  assert.equal(controller.getTask("task-revision").status, "running");
  assert.equal(controller.getTask("task-revision").revision, 8);

  controller.handleEvent({ type: "task.status", agentId: "agent-1", data: { taskId: "task-revision", kind: "agent", status: "waiting", revision: 8, outputBytes: 42 } });
  const task = controller.getTask("task-revision");
  assert.equal(task.status, "waiting");
  assert.equal(task.title, "Hydrated title");
  assert.deepEqual(task.publicSummary, { description: "Hydrated summary" });
  assert.equal(task.parentRunId, "run-1");
  assert.equal(task.parentToolUseId, "tool-1");
  assert.equal(task.childAgentId, "child-1");
  assert.equal(task.childRunId, "child-run-1");
  assert.equal(task.outputBytes, 42);

  controller.applySnapshot({ backgroundTasks: [{ id: "task-revision", status: "queued", title: "Unversioned stale title", childRunId: "newly-known-run" }] });
  assert.equal(controller.getTask("task-revision").status, "waiting");
  assert.equal(controller.getTask("task-revision").title, "Hydrated title");
});

test("unknown task.created events trigger exact asynchronous task hydration", async () => {
  const requests = [];
  const controller = createBackgroundTasksController({
    request: async (path, options) => {
      requests.push({ path, method: options.method });
      return {
        id: "task-created",
        ownerAgentId: "agent-1",
        parentRunId: "run-parent",
        parentToolUseId: "tool-parent",
        kind: "agent",
        status: "running",
        revision: 3,
        publicSummary: { description: "Hydrated child task" },
        createdAt: "2026-07-19T10:00:00Z",
        updatedAt: "2026-07-19T10:00:01Z",
      };
    },
  });
  controller.setAgent("agent-1");

  const handled = controller.handleEvent({ type: "task.created", agentId: "agent-1", data: { taskId: "task-created", kind: "agent", status: "running", revision: 3 } });
  assert.equal(handled, true);
  assert.equal(typeof handled?.then, "undefined");
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(requests, [{ path: "/api/background-tasks/task-created", method: "GET" }]);
  assert.equal(controller.getTask("task-created").title, "Hydrated child task");
  assert.equal(controller.getTaskByParentTool("run-parent", "tool-parent").id, "task-created");
});

test("first Agent output hydrates child identifiers for running navigation", async () => {
  const requests = [];
  const controller = createBackgroundTasksController({
    request: async (path, options) => {
      requests.push({ path, method: options.method });
      if (path.includes("/output?")) return { chunks: [] };
      return {
        id: "task-output-child",
        ownerAgentId: "agent-1",
        parentRunId: "run-parent",
        parentToolUseId: "tool-parent",
        kind: "agent",
        status: "running",
        revision: 2,
        childAgentId: "child-agent",
        childRunId: "child-run",
        publicSummary: { description: "Running child" },
        createdAt: "2026-08-03T05:00:00Z",
        startedAt: "2026-08-03T05:00:01Z",
        updatedAt: "2026-08-03T05:00:01Z",
      };
    },
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({ backgroundTasks: [{
    id: "task-output-child",
    ownerAgentId: "agent-1",
    kind: "agent",
    status: "running",
    revision: 1,
    publicSummary: { description: "Starting child" },
  }] }, { agentId: "agent-1" });

  controller.handleEvent({ type: "task.output", agentId: "agent-1", data: { taskId: "task-output-child", kind: "agent", outputBytes: 20 } });
  await new Promise((resolve) => setImmediate(resolve));

  assert.deepEqual(requests, [
    { path: "/api/background-tasks/task-output-child", method: "GET" },
    { path: "/api/background-tasks/task-output-child/output?afterSequence=0", method: "GET" },
  ]);
  assert.equal(controller.getTask("task-output-child").childAgentId, "child-agent");
  assert.equal(controller.getTask("task-output-child").childRunId, "child-run");
});

test("newer lifecycle events force exact hydration while an older request is in flight", async () => {
  const requests = [];
  const resolvers = [];
  const controller = createBackgroundTasksController({
    request: (path, options) => {
      requests.push({ path, method: options.method });
      return new Promise((resolve) => resolvers.push(resolve));
    },
  });
  controller.setAgent("agent-1");

  controller.handleEvent({ type: "task.created", agentId: "agent-1", data: { taskId: "task-racing", kind: "agent", status: "queued", revision: 1 } });
  controller.handleEvent({ type: "task.status", agentId: "agent-1", data: { taskId: "task-racing", kind: "agent", status: "running", revision: 2 } });
  assert.deepEqual(requests, [
    { path: "/api/background-tasks/task-racing", method: "GET" },
    { path: "/api/background-tasks/task-racing", method: "GET" },
  ]);

  resolvers[0]({
    id: "task-racing",
    ownerAgentId: "agent-1",
    parentRunId: "run-parent",
    parentToolUseId: "tool-parent",
    kind: "agent",
    status: "queued",
    revision: 1,
    publicSummary: { description: "Queued snapshot" },
    createdAt: "2026-07-19T10:00:00Z",
    updatedAt: "2026-07-19T10:00:00Z",
  });
  resolvers[1]({
    id: "task-racing",
    ownerAgentId: "agent-1",
    parentRunId: "run-parent",
    parentToolUseId: "tool-parent",
    kind: "agent",
    status: "running",
    revision: 2,
    publicSummary: { description: "Running snapshot" },
    childAgentId: "child-agent",
    childRunId: "child-run",
    createdAt: "2026-07-19T10:00:00Z",
    startedAt: "2026-07-19T10:00:01Z",
    updatedAt: "2026-07-19T10:00:01Z",
  });
  await new Promise((resolve) => setImmediate(resolve));

  const task = controller.getTask("task-racing");
  assert.equal(task.status, "running");
  assert.equal(task.revision, 2);
  assert.equal(task.childAgentId, "child-agent");
  assert.equal(task.childRunId, "child-run");
});

test("automatic hydration responses are discarded after an Agent switch", async () => {
  let resolveHydration;
  const controller = createBackgroundTasksController({
    request: () => new Promise((resolve) => { resolveHydration = resolve; }),
  });
  controller.setAgent("agent-1");
  controller.handleEvent({ type: "task.created", agentId: "agent-1", data: { taskId: "task-stale", kind: "agent", status: "queued", revision: 1 } });
  controller.setAgent("agent-2");
  resolveHydration({
    id: "task-stale",
    ownerAgentId: "agent-1",
    parentRunId: "run-old",
    parentToolUseId: "tool-old",
    kind: "agent",
    status: "running",
    revision: 2,
  });
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(controller.getTask("task-stale"), null);
  assert.equal(controller.getTaskByParentTool("run-old", "tool-old"), null);
  assert.deepEqual(controller.state().tasksById, {});
});

test("subscriptions receive read-only public snapshots and unsubscribe cleanly", () => {
  const onChangeSnapshots = [];
  const subscribedSnapshots = [];
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    onChange: (snapshot) => onChangeSnapshots.push(snapshot),
  });
  controller.setAgent("agent-1");
  const unsubscribe = controller.subscribe((snapshot) => subscribedSnapshots.push(snapshot));

  controller.applySnapshot({ recentBackgroundTasks: [{
    id: "task-public",
    parentRunId: "run-public",
    parentToolUseId: "tool-public",
    kind: "agent",
    status: "running",
    revision: 1,
    publicSummary: { description: "Safe", prompt: "TOP SECRET" },
  }] }, { agentId: "agent-1" });

  assert.equal(subscribedSnapshots.length, 1);
  assert.equal(onChangeSnapshots.length, 2);
  assert.equal(Object.isFrozen(subscribedSnapshots[0]), true);
  assert.equal(Object.isFrozen(subscribedSnapshots[0].tasks), true);
  assert.deepEqual(subscribedSnapshots[0].tasks[0].publicSummary, { description: "Safe" });
  assert.equal(controller.getTaskByParentTool("run-public", "tool-public").publicSummary.prompt, undefined);
  assert.equal(unsubscribe(), true);

  controller.applySnapshot({ continuation: { mode: "safe" } }, { agentId: "agent-1" });
  assert.equal(subscribedSnapshots.length, 1);
  assert.equal(onChangeSnapshots.length, 3);
  assert.equal(typeof controller.selectTask, "function");
  assert.equal(typeof controller.getTask, "function");
  assert.equal(typeof controller.getTaskByParentTool, "function");
  assert.equal(typeof controller.subscribe, "function");
});

// Status alone could not answer "what did the subagent actually say", which is
// the reason to open this panel. Selecting an agent task therefore has to fetch
// that subagent's own conversation and settings.
test("selecting a subagent task loads its conversation and agent settings", async () => {
  const requests = [];
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      requests.push(path);
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return [
          { role: "user", text: "stay on standby", createdAt: "2026-01-01T00:00:00Z" },
          { role: "assistant", text: "standing by", createdAt: "2026-01-01T00:00:05Z" },
        ];
      }
      if (path === "/api/agents/child-7") return { id: "child-7", model: "codex:gpt-5.6", reasoningEffort: "low", permissionMode: "bypassPermissions" };
      return {
        id: "task-child",
        agentId: "agent-1",
        kind: "agent",
        status: "succeeded",
        childAgentId: "child-7",
        publicSummary: { description: "Subagent task" },
      };
    },
  });
  controller.setAgent("agent-1");

  await controller.selectTask("task-child");

  assert.ok(requests.some((path) => path.startsWith("/api/agents/child-7/messages")),
    "the subagent's conversation must be fetched");
  assert.ok(requests.includes("/api/agents/child-7"),
    "the subagent's model and effort must be fetched so the controls show real values");
});

// The panel exists to steer a task mid-flight, so a model or effort change is
// written straight to the subagent rather than only to local state.
test("changing a subagent's effort patches that agent", async () => {
  const calls = [];
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      calls.push({ path, method: options.method || "GET", body: options.body });
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) return [];
      if (path === "/api/agents/child-9") return { id: "child-9", model: "m", reasoningEffort: "auto" };
      return { id: "task-9", agentId: "agent-1", kind: "agent", status: "running", childAgentId: "child-9" };
    },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-9");
  calls.length = 0;

  await controller.updateChildAgentSetting("child-9", "reasoning-effort", { reasoningEffort: "high" });

  const patch = calls.find((call) => call.method === "PATCH");
  assert.ok(patch, "a settings change must reach the API");
  assert.equal(patch.path, "/api/agents/child-9/reasoning-effort");
  assert.deepEqual(JSON.parse(patch.body), { reasoningEffort: "high" });
});

test("sending a message to a subagent posts to that agent and refreshes its conversation", async () => {
  const calls = [];
  let messages = [];
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      calls.push({ path, method: options.method || "GET", body: options.body });
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages") && (options.method || "GET") === "GET") return messages;
      if (path.includes("/messages")) {
        messages = [{ role: "user", text: JSON.parse(options.body).text }];
        return { id: "m1" };
      }
      if (path === "/api/agents/child-3") return { id: "child-3", model: "m" };
      return { id: "task-3", agentId: "agent-1", kind: "agent", status: "running", childAgentId: "child-3" };
    },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-3");
  calls.length = 0;

  await controller.sendChildAgentMessage("child-3", "keep waiting");

  const post = calls.find((call) => call.method === "POST");
  assert.ok(post, "the message must be posted to the subagent");
  assert.equal(post.path, "/api/agents/child-3/messages");
  assert.equal(JSON.parse(post.body).text, "keep waiting");
  assert.ok(calls.filter((call) => call.path.includes("/messages") && call.method === "GET").length > 0,
    "the conversation is refetched so the sent message appears");
});

// A blank message must not start a run on the subagent.
test("an empty subagent message is not sent", async () => {
  const calls = [];
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      calls.push({ path, method: options.method || "GET" });
      return {};
    },
  });
  controller.setAgent("agent-1");
  await controller.sendChildAgentMessage("child-1", "   ");
  assert.deepEqual(calls, []);
});

// Two subagent cards used to fill the pane, burying the list this panel exists
// to browse, and the detail pane had to be reachable without scrolling past it.
test("the task list is a one-row tab strip and the detail pane owns the rest", async () => {
  const { readFile } = await import("node:fs/promises");
  const css = await readFile(new URL("../styles/workspace-tasks.css", import.meta.url), "utf8");

  // One fixed row for the tabs, everything else for the detail pane. Stacked
  // cards used to push the detail pane down the panel as tasks accumulated.
  const grid = css.match(/\.background-task-tray-grid \{[^}]*\}/);
  assert.ok(grid, "the tray grid rule must exist");
  assert.match(grid[0], /grid-template-rows:\s*auto minmax\(0, 1fr\)/);

  // Tabs scroll sideways rather than wrapping into more rows.
  const strip = css.match(/\.background-task-tabs \{[^}]*\}/);
  assert.ok(strip, ".background-task-tabs must exist");
  assert.match(strip[0], /display:\s*flex/);
  assert.match(strip[0], /overflow-x:\s*auto/);
  assert.match(strip[0], /overflow-y:\s*hidden/);
  // The narrow-width override must not reintroduce a row split: the strip is one
  // auto row at every width, and a 40% share of the panel would be absurd for it.
  assert.doesNotMatch(css, /@media \(max-width: 760px\) \{[^}]*\.background-task-tray-grid \{[^}]*grid-template-rows/);

  // Status is a dot on the tab; a word per tab would crowd out the title.
  assert.match(css, /\.background-task-tab-dot \{[^}]*border-radius:\s*50%/);
  assert.match(css, /\.background-task-tab-dot\.status-failed/);

  // Sized against the panel, not the window, so a docked panel collapses too.
  assert.match(css, /\.background-task-tray-grid \{[^}]*container-type:\s*inline-size/);
  assert.match(css, /@container background-task-panel \(max-width: 420px\)/);
  assert.match(css, /@container background-task-panel \(max-width: 300px\)/);
});

// Selecting a tab now navigates too, which is what allowed the separate "open
// subagent" and "open run" buttons below the pane to be removed.
test("clicking a task tab opens it in the panel without hijacking the main conversation", async () => {
  const navigated = [];
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) return [];
      if (path === "/api/agents/child-42") return { id: "child-42", model: "m" };
      return { id: "task-42", agentId: "agent-1", kind: "agent", status: "running", childAgentId: "child-42" };
    },
    onNavigateAgent: (childAgentId) => navigated.push(childAgentId),
  });
  controller.setAgent("agent-1");

  const task = await controller.selectTask("task-42");
  assert.equal(controller.state().selected, "task-42");
  assert.equal(task.childAgentId, "child-42");
  // Selecting must never navigate: the user was reading one thread and a glance
  // at a subagent used to replace it.
  assert.deepEqual(navigated, []);

  // The click handler itself must not call the navigate hook for a tab either.
  const source = await (await import("node:fs/promises")).readFile(new URL("./background-tasks.mjs", import.meta.url), "utf8");
  const branch = source.slice(source.indexOf("} else if (target.dataset.backgroundTask) {"));
  const branchBody = branch.slice(0, branch.indexOf("} else if (target.dataset.backgroundOutputMore)"));
  assert.ok(!branchBody.includes("onNavigateAgent"), "a tab click must not navigate the main conversation");
  assert.match(branchBody, /selectTask\(target\.dataset\.backgroundTask\)/);
});

// The model control has to offer the configured provider models rather than ask
// for a hand-typed id, and it must not invent a catalogue of its own.
test("subagent model options come from the injected provider list", () => {
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    getModelOptions: () => [
      { value: "anthropic:claude-opus-5", label: "claude-opus-5" },
      { value: "anthropic:claude-opus-5", label: "duplicate" },
      { value: "", label: "blank is dropped" },
      "codex:gpt-5.6",
    ],
  });
  const html = controller.renderChildControlsHTMLForTest({ childAgentId: "child-1" }, { model: "anthropic:claude-opus-5" });

  assert.match(html, /<select data-background-child-model="child-1">/);
  assert.match(html, /value="anthropic:claude-opus-5" selected/);
  assert.match(html, /codex:gpt-5\.6/);
  // Duplicates collapse and blanks are dropped.
  assert.equal((html.match(/value="anthropic:claude-opus-5"/g) || []).length, 1);
  assert.ok(!html.includes("blank is dropped"));
});

// Every other control in this panel is translated; a raw "bypassPermissions"
// leaking into the select was the one exception.
test("subagent permission modes render translated labels, not raw enum values", () => {
  const controller = createBackgroundTasksController({ request: async () => ({}) });
  const html = controller.renderChildControlsHTMLForTest({ childAgentId: "child-2" }, { permissionMode: "bypassPermissions" });

  assert.match(html, /<option value="bypassPermissions" selected>/);
  // The option's visible text must not be the enum value itself.
  const options = [...html.matchAll(/<option value="(readOnly|acceptEdits|bypassPermissions|default|dontAsk)"[^>]*>([^<]*)</g)];
  assert.equal(options.length, 5, "all five permission modes must be offered");
  for (const [, value, label] of options) {
    assert.notEqual(label.trim(), value, `${value} must render a translated label`);
    assert.ok(label.trim().length > 0, `${value} must have a label`);
  }
});

// A lifecycle event carries identifiers and state but no title; the title only
// arrives with the follow-up hydration request. The header used to fall through
// to the idle string in that window, so a dispatched subagent read as "no
// running task" while the dot beside it was already animating.
test("the task summary never reads idle while a task is running", () => {
  function element() {
    const classes = new Set();
    return {
      attributes: {},
      className: "",
      textContent: "",
      classList: {
        contains: (name) => classes.has(name),
        toggle(name, force) { if (force) classes.add(name); else classes.delete(name); },
      },
      setAttribute(name, value) { this.attributes[name] = String(value); },
    };
  }
  const elements = {
    headerTaskSummaryBtn: element(),
    headerCurrentTaskText: element(),
    headerTaskQueueBadge: element(),
    headerTaskStatusDot: element(),
  };
  const controller = createBackgroundTasksController({
    // Hydration is what would supply the title; keep it pending so the render
    // under test is the one that only has the event payload.
    request: () => new Promise(() => {}),
    documentRef: { getElementById: (id) => elements[id] || null },
  });
  controller.setAgent("agent-1");

  const idleText = elements.headerCurrentTaskText.textContent;

  // Exactly what the server publishes for a dispatched subagent.
  controller.handleEvent({ type: "task.created", agentId: "agent-1", data: { taskId: "task-1", kind: "agent", status: "queued", revision: 1 } });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot queued");
  assert.notEqual(elements.headerCurrentTaskText.textContent, idleText, "a queued task must not read as idle");

  controller.handleEvent({ type: "task.status", agentId: "agent-1", data: { taskId: "task-1", kind: "agent", status: "running", revision: 2 } });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot running");
  assert.notEqual(elements.headerCurrentTaskText.textContent, idleText, "a running task must not read as idle");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-task"), true);

  // Terminal again: the idle label is correct now.
  controller.handleEvent({ type: "task.completed", agentId: "agent-1", data: { taskId: "task-1", kind: "agent", status: "completed", revision: 3 } });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot idle");
  assert.equal(elements.headerCurrentTaskText.textContent, idleText);
});

// Every task used to get a tab whether or not it had been looked at, so five
// background tasks meant five truncated titles competing for one row. The strip
// now starts from the overview and grows only when the user opens something.
test("the tab strip starts at the overview and grows only for opened tasks", async () => {
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.includes("/background-tasks?limit=")) {
        return { tasks: [
          { id: "task-1", status: "running", title: "First", createdAt: "2026-08-08T10:00:02Z", updatedAt: "2026-08-08T10:00:02Z" },
          { id: "task-2", status: "succeeded", title: "Second", createdAt: "2026-08-08T10:00:01Z", updatedAt: "2026-08-08T10:00:01Z" },
        ] };
      }
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/output")) return { chunks: [] };
      const id = path.split("/").pop();
      return { id, status: "running", title: id === "task-1" ? "First" : "Second", createdAt: "2026-08-08T10:00:02Z", updatedAt: "2026-08-08T10:00:02Z" };
    },
  });
  controller.setAgent("agent-1");
  await controller.loadAgent("agent-1");

  assert.equal(controller.state().selected, "", "the overview is the default view");
  assert.deepEqual(controller.state().openTabs, [], "no task has a tab until it is opened");

  await controller.selectTask("task-1");
  assert.equal(controller.state().selected, "task-1");
  assert.deepEqual(controller.state().openTabs, ["task-1"], "opening a task gives it a tab");

  await controller.selectTask("task-2");
  assert.deepEqual(controller.state().openTabs, ["task-1", "task-2"], "tabs accumulate in the order they were opened");

  // Closing the active tab falls back to the overview rather than to a task the
  // user did not ask for.
  assert.equal(controller.closeTab("task-2"), true);
  assert.deepEqual(controller.state().openTabs, ["task-1"]);
  assert.equal(controller.state().selected, "", "closing the active tab returns to the overview");

  // Reopening goes back through the overview, which still lists everything.
  await controller.selectTask("task-2");
  assert.deepEqual(controller.state().openTabs, ["task-1", "task-2"]);

  controller.showOverview();
  assert.equal(controller.state().selected, "");
  assert.deepEqual(controller.state().openTabs, ["task-1", "task-2"], "the overview does not discard open tabs");
});

// Closing a tab is housekeeping; stopping the work is not. They were the same
// button, so tidying the strip cancelled whatever was running behind it.
test("closing a tab leaves the task running", async () => {
  const cancelled = [];
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      if (String(options.method || "").toUpperCase() === "POST" && path.includes("/cancel")) {
        cancelled.push(path);
        return { id: "task-1", status: "cancelled" };
      }
      if (path.includes("/output")) return { chunks: [] };
      return { id: "task-1", status: "running", title: "First", createdAt: "2026-08-08T10:00:02Z", updatedAt: "2026-08-08T10:00:02Z" };
    },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-1");
  assert.deepEqual(controller.state().openTabs, ["task-1"]);

  controller.closeTab("task-1");
  assert.deepEqual(cancelled, [], "closing a tab must not cancel the task");
  assert.equal(controller.state().tasksById["task-1"].status, "running", "the task keeps running");
  assert.deepEqual(controller.state().openTabs, []);

  // Cancelling is still available, from the overview row.
  await controller.cancel("task-1");
  assert.equal(cancelled.length, 1, "cancel is still reachable");
});

// End to end across the two layers: the helper decides the parent is blocked on a
// child, and the controller renders that as an amber dot rather than the working
// blue. The tone has to survive the hand-off for the distinction to reach anyone.
test("waiting on a child reaches the task summary as an amber dot", () => {
  function element() {
    const classes = new Set();
    return {
      attributes: {},
      className: "",
      textContent: "",
      classList: {
        contains: (name) => classes.has(name),
        toggle(name, force) { if (force) classes.add(name); else classes.delete(name); },
      },
      setAttribute(name, value) { this.attributes[name] = String(value); },
    };
  }
  const elements = {
    headerTaskSummaryBtn: element(),
    headerCurrentTaskText: element(),
    headerTaskQueueBadge: element(),
    headerTaskStatusDot: element(),
  };
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    documentRef: { getElementById: (id) => elements[id] || null },
  });
  controller.setAgent("agent-1");

  controller.setForegroundActivity({ kind: "waiting", tone: "waiting", text: "waiting on 1 subagent" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot waiting");
  assert.equal(elements.headerCurrentTaskText.textContent, "waiting on 1 subagent");

  // The parent picking the work back up returns to the working blue.
  controller.setForegroundActivity({ kind: "thinking", text: "thinking" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot running");

  // An unknown tone must not render an unstyled dot.
  controller.setForegroundActivity({ kind: "thinking", tone: "nonsense", text: "still thinking" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot running");
});

// The panel read message.text and message.content. The messages endpoint returns
// neither: it returns contentText. Every bubble was therefore dropped as empty and
// a task that had run for twenty minutes showed a blank pane. The reasoning and
// the tool calls, which are the part of a subagent's work that is otherwise
// invisible, were not requested at all.
test("opening a subagent task reads contentText and fetches its tool calls", async () => {
  const requested = [];
  const controller = createBackgroundTasksController({
    request: async (path) => {
      requested.push(path);
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return {
          messages: [
            { id: "m1", role: "user", contentText: "do the thing", createdAt: "2026-08-08T15:34:28Z" },
            // A turn that only reasoned and called a tool, with no answer text.
            { id: "m2", role: "assistant", contentText: "", reasoningText: "weighing options", createdAt: "2026-08-08T15:35:00Z" },
            { id: "m3", role: "assistant", contentText: "done", createdAt: "2026-08-08T15:56:00Z" },
          ],
        };
      }
      if (path.includes("/tool-calls")) {
        return { toolCalls: [
          { messageId: "m2", toolName: "Grep", status: "succeeded", durationMs: 120, inputJson: { query: "x" } },
          { messageId: "m2", toolName: "Read", status: "error", errorMessage: "missing file" },
        ] };
      }
      return {
        id: "task-sub",
        agentId: "agent-1",
        kind: "agent",
        status: "succeeded",
        childAgentId: "child-9",
        childRunId: "run-9",
        revision: 1,
        publicSummary: { description: "Queued message attachments" },
      };
    },
  });
  controller.setAgent("agent-1");

  await controller.selectTask("task-sub");

  // The run is what owns tool calls, so the child run id has to reach the request.
  assert.ok(
    requested.some((path) => path === "/api/agents/child-9/runs/run-9/tool-calls?view=activity"),
    `tool calls were not requested: ${JSON.stringify(requested)}`,
  );
  assert.ok(requested.some((path) => path.startsWith("/api/agents/child-9/messages")), "child messages were not requested");

  // Assert the rendered pane, not the stored messages: the data was always present
  // and only the field read was wrong, so a payload assertion passes either way.
  const html = controller.renderChildConversationHTMLForTest({ childAgentId: "child-9", childRunId: "run-9" });
  assert.ok(html.includes("do the thing"), `the user turn did not reach the pane: ${html}`);
  assert.ok(html.includes("done"), "the final answer did not reach the pane");
  assert.ok(html.includes("weighing options"), "reasoning was not rendered");
  assert.ok(html.includes("Grep"), "the tool call was not rendered");
  assert.ok(html.includes("missing file"), "a failed call must show why");
  // The reasoning-only turn has no answer text but must still render.
  assert.equal((html.match(/background-task-bubble/g) || []).length, 3, "every turn must render a bubble");
});

// A shell task has no child agent, so there is no run to ask about and asking
// anyway would 404 on every open.
test("a shell task does not request subagent tool calls", async () => {
  const requested = [];
  const controller = createBackgroundTasksController({
    request: async (path) => {
      requested.push(path);
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      return { id: "task-shell", agentId: "agent-1", kind: "shell", status: "succeeded", revision: 1 };
    },
  });
  controller.setAgent("agent-1");

  await controller.selectTask("task-shell");

  assert.equal(requested.some((path) => path.includes("/tool-calls")), false, "a shell task has no subagent run");
});
