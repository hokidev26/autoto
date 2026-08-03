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
  assert.deepEqual(controller.state().foregroundActivity, { kind: "thinking", text: "思考中" });
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
