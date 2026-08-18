import test, { mock } from "node:test";
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

  // The kind carries the state to the dot: "thinking" gets its own tone rather
  // than collapsing into the generic working blue.
  assert.equal(controller.setForegroundActivity({ kind: "thinking", text: "思考中" }), true);
  assert.deepEqual(controller.state().foregroundActivity, { kind: "thinking", text: "思考中", tone: "thinking" });
  assert.equal(elements.headerCurrentTaskText.textContent, "思考中");
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot thinking");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-busy"], "true");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-label"], "思考中");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-task"), true);
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-foreground-activity"), true);

  // Every state the activity resolver produces reaches the dot with its own
  // tone: a provider retry, a pending approval and compaction each look
  // different from ordinary progress.
  controller.setForegroundActivity({ kind: "retrying", text: "重试中 2/3" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot retrying");
  controller.setForegroundActivity({ kind: "approval", text: "等待批准 · Bash" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot approval");
  controller.setForegroundActivity({ kind: "compacting", text: "压缩中" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot compacting");
  controller.setForegroundActivity({ kind: "generating", text: "正在生成" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot generating");
  // A tool step is ordinary progress and keeps the generic working tone.
  controller.setForegroundActivity({ kind: "tool", text: "正在读取 main.go" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot running");

  controller.setForegroundActivity(null);
  assert.equal(controller.state().foregroundActivity, null);
  assert.equal(elements.headerCurrentTaskText.textContent, "Run checks");
  assert.equal(elements.headerTaskSummaryBtn.attributes["aria-busy"], "false");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-foreground-activity"), false);
});

test("the composer header can show a live turn before the task list binds an agent", () => {
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
  assert.equal(controller.state().agentId, "");
  assert.equal(controller.setForegroundActivity({ kind: "thinking", text: "思考中" }), true);
  assert.equal(elements.headerCurrentTaskText.textContent, "思考中");
  assert.equal(elements.headerTaskSummaryBtn.classList.contains("has-foreground-activity"), true);
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

test("selecting an existing overview task keeps its list position", async () => {
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      const id = path.split("/").pop();
      return { id, agentId: "agent-1", kind: "shell", status: "completed" };
    },
  });
  controller.setAgent("agent-1");
  controller.applySnapshot({
    backgroundTasks: [
      { id: "task-a", agentId: "agent-1", status: "completed" },
      { id: "task-b", agentId: "agent-1", status: "completed" },
      { id: "task-c", agentId: "agent-1", status: "completed" },
    ],
  }, { agentId: "agent-1" });
  assert.deepEqual(controller.state().order, ["task-c", "task-b", "task-a"]);

  await controller.selectTask("task-a");
  assert.equal(controller.state().selected, "task-a");
  assert.deepEqual(controller.state().order, ["task-c", "task-b", "task-a"]);

  controller.handleEvent({ type: "task.created", agentId: "agent-1", data: { taskId: "task-new", status: "running" } });
  assert.deepEqual(controller.state().order, ["task-new", "task-c", "task-b", "task-a"]);
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

// The panel drops any event whose agentId is not its own, and a subagent's
// replies carry the subagent's id, so nothing it does arrives as an event. The
// single refresh after POST runs before the reply exists, so without polling a
// sent message looked like it went nowhere for as long as the panel stayed open.
test("a subagent's reply arrives without the user doing anything", async () => {
  mock.timers.enable({ apis: ["setTimeout"] });
  try {
    let messages = [{ role: "user", text: "keep waiting" }];
    let activeRunCalls = 0;
    const controller = createBackgroundTasksController({
      request: async (path, options = {}) => {
        const method = options.method || "GET";
        if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
        if (path.endsWith("/runs/active")) {
          activeRunCalls += 1;
          // Working for the first two polls, then idle.
          if (activeRunCalls > 2) throw new Error("active run not found");
          return { run: { id: "run-9", status: "running" } };
        }
        if (path.includes("/messages") && method === "GET") return messages;
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
    // Isolate the send path: whatever opening the task started must not be what
    // makes this pass, or the test would still pass with sending left unwired.
    controller.stopChildPollingForTest();
    assert.deepEqual(controller.state().childPolling, [], "precondition: nothing is being followed");

    await controller.sendChildAgentMessage("child-3", "keep waiting");
    assert.deepEqual(controller.state().childPolling, ["child-3"],
      "sending must start following the child, or its reply never arrives");

    // The reply lands on the provider's own schedule, with no further user input.
    messages = [{ role: "user", text: "keep waiting" }, { role: "assistant", text: "here is the answer" }];
    for (let tick = 0; tick < 4; tick += 1) {
      mock.timers.tick(1600);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    }

    const conversation = controller.state().childConversations?.["child-3"] || [];
    assert.ok(conversation.some((entry) => entry.role === "assistant"),
      "the reply must appear on its own, without the user reopening the task");
    assert.ok(activeRunCalls > 0, "the panel must follow the child's run to know when it is done");
  } finally {
    mock.timers.reset();
  }
});

// The thinking indicator lives on the composer's pill row -- the line above the
// message input, sharing it with the model select -- not under the send box
// where it read as a stray status line, and not inside the transcript where a
// scrolled-up reader never saw it.
test("the child thinking indicator renders on the pill row, not in the transcript", async () => {
  const controller = createBackgroundTasksController({
    request: async (path, options = {}) => {
      const method = options.method || "GET";
      if (path.includes("/messages") && method === "GET") return [{ id: "m1", role: "user", text: "question" }];
      if (path.includes("/messages")) return { id: "m1" };
      if (path.endsWith("/runs/active")) return { run: { id: "run-1", status: "running" } };
      if (path === "/api/agents/child-9") return { id: "child-9", model: "m" };
      return { id: "task-9", agentId: "agent-1", kind: "agent", status: "running", childAgentId: "child-9" };
    },
  });
  try {
    controller.setAgent("agent-1");
    await controller.sendChildAgentMessage("child-9", "question");
    assert.deepEqual(controller.state().childPolling, ["child-9"], "precondition: the child is being followed");
    await controller.loadChildConversation("child-9");

    const controls = controller.renderChildControlsHTMLForTest({ childAgentId: "child-9" }, { model: "m" });
    assert.match(controls, /background-task-child-controls">\s*<span class="background-task-child-working/,
      "a followed child shows the thinking indicator at the head of the pill row");
    assert.match(controls, /background-task-child-working[\s\S]*data-background-child-form/,
      "the indicator sits above the send box, not under it");
    const conversation = controller.renderChildConversationHTMLForTest({ childAgentId: "child-9", childRunId: "run-1" });
    assert.doesNotMatch(conversation, /background-task-child-working/,
      "the transcript must not carry its own copy of the indicator");
  } finally {
    controller.stopChildPollingForTest();
  }
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
  // Continuation grid lives in the resizable details panel, not the task tray.
  assert.match(css, /\.conversation-details-body \{[^}]*container-type:\s*inline-size/);
  assert.match(css, /@container conversation-details-panel \(max-width: 420px\)/);
  assert.doesNotMatch(css, /@media \(max-width: 760px\) \{[^}]*\.conversation-continuation-grid/);
  assert.doesNotMatch(css, /@media \(max-width: 480px\) \{[^}]*\.background-task-row/);
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

  assert.match(html, /<select data-background-child-model="child-1"/);
  assert.match(html, /value="anthropic:claude-opus-5" selected/);
  assert.match(html, /codex:gpt-5\.6/);
  // Duplicates collapse and blanks are dropped.
  assert.equal((html.match(/value="anthropic:claude-opus-5"/g) || []).length, 1);
  assert.ok(!html.includes("blank is dropped"));
});

// The pill row used to be framed chips over an invisible native select. It is
// now borderless triggers that open the same popover menu the main composer
// uses, while the hidden select keeps holding the options and the value so the
// tray's change delegation stays the single PATCH path.
test("subagent pills are borderless triggers that open the composer-style menu", async () => {
  const { readFile } = await import("node:fs/promises");
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    getModelOptions: () => [{ value: "codex:gpt-5.6", label: "gpt-5.6" }],
  });
  const html = controller.renderChildControlsHTMLForTest({ childAgentId: "child-1" }, { model: "codex:gpt-5.6" });

  // The select survives as the value store the change delegation listens to.
  assert.match(html, /<select data-background-child-model="child-1"/);
  // Each pill exposes a real button trigger for the popover.
  assert.match(html, /<button type="button" class="background-task-pill-trigger" data-background-child-pill[^>]*aria-haspopup="listbox"/);
  // The trigger names its control and current value for tooltip and reader.
  assert.match(html, /data-pill-title="[^"]+"/);

  // The menu itself is the composer's: same classes, so same look in both
  // themes, and choosing writes the select then fires a bubbling change.
  const source = await readFile(new URL("./background-tasks.mjs", import.meta.url), "utf8");
  assert.match(source, /composer-select-popover background-task-select-popover/, "the popover must reuse the composer menu classes");
  assert.match(source, /composer-select-option/, "options must reuse the composer option class");
  assert.match(source, /new EventCtor\("change", \{ bubbles: true \}\)/, "choosing must dispatch a bubbling change for the tray delegation");
  // The model menu groups by provider like the main chat's: a provider heading
  // above its models, rendered with the composer's own grouping helpers.
  assert.match(source, /groupModelSelectOptions\(visibleOptions\)/, "the model menu must group options by provider");
  assert.match(source, /composer-model-group-heading/, "provider headings must reuse the composer heading class");
  assert.match(source, /modelOptionPresentation\(option\.value, option\.textContent\)\.name/, "model rows show the model name; the provider lives in the heading");
  assert.match(source, /group\.provider \|\| t\("chat\.modelProviderFallback"\)/, "a provider-less model still gets a labelled group");

  // The frame is gone: the select is hidden (not stretched over the pill) and
  // the trigger is a quiet borderless button.
  const css = await readFile(new URL("../styles/workspace-tasks.css", import.meta.url), "utf8");
  assert.match(css, /\.background-task-child-pill > select \{ display: none; \}/);
  const trigger = css.match(/\.background-task-pill-trigger \{[^}]*\}/);
  assert.ok(trigger, ".background-task-pill-trigger rule must exist");
  assert.match(trigger[0], /border: 0/);
  assert.match(trigger[0], /background: transparent/);
});

// Every other control in this panel is translated; a raw "bypassPermissions"
// leaking into the select was the one exception.
test("subagent permission modes render translated labels, not raw enum values", () => {
  const controller = createBackgroundTasksController({ request: async () => ({}) });
  const html = controller.renderChildControlsHTMLForTest({ childAgentId: "child-2" }, { permissionMode: "bypassPermissions" });

  assert.match(html, /<option value="bypassPermissions" selected>/);
  // The option's visible text must not be the enum value itself.
  const options = [...html.matchAll(/<option value="(readOnly|acceptEdits|bypassPermissions|default)"[^>]*>([^<]*)</g)];
  assert.equal(options.length, 4, "all four permission modes must be offered");
  assert.deepEqual(options.map((match) => match[1]), ["readOnly", "default", "acceptEdits", "bypassPermissions"]);
  for (const [, value, label] of options) {
    assert.notEqual(label.trim(), value, `${value} must render a translated label`);
    assert.ok(label.trim().length > 0, `${value} must have a label`);
  }
});

test("subagent permission modes follow the composer remote allow-all cap", () => {
  const controller = createBackgroundTasksController({
    request: async () => ({}),
    bypassPermissionsAllowed: () => false,
  });
  const html = controller.renderChildControlsHTMLForTest({ childAgentId: "child-cap" }, { permissionMode: "bypassPermissions" });

  assert.match(html, /<option value="acceptEdits" selected>/);
  assert.match(html, /<option value="bypassPermissions" disabled>/);
  assert.doesNotMatch(html, /<option value="bypassPermissions" selected>/);
  const allowAll = html.match(/<option value="bypassPermissions"[^>]*>([^<]*)</);
  assert.ok(allowAll, "allow-all remains listed");
  assert.match(allowAll[1], /遠端停用|远程禁用|Remote disabled/);
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

  // The parent picking the work back up leaves the amber for its own state.
  controller.setForegroundActivity({ kind: "thinking", text: "thinking" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot thinking");

  // An unknown tone falls back to the kind's own tone rather than rendering an
  // unstyled dot.
  controller.setForegroundActivity({ kind: "thinking", tone: "nonsense", text: "still thinking" });
  assert.equal(elements.headerTaskStatusDot.className, "header-task-status-dot thinking");

  // Unknown tone and unknown kind together still land on the working blue.
  controller.setForegroundActivity({ kind: "mystery", tone: "nonsense", text: "doing something" });
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
  assert.match(html, /data-user-profile-avatar>AT<\/span>/);
  assert.match(html, /data-user-profile-name>Autoto User<\/span>/);
  // The reasoning-only turn has no answer text but must still render.
  assert.equal((html.match(/background-task-bubble/g) || []).length, 3, "every turn must render a bubble");
});

test("subagent user messages render the current profile avatar and display name", async () => {
  const tinyJPEGDataUrl = "data:image/jpeg;base64,AAAA";
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return {
          messages: [
            { id: "m1", role: "user", contentText: "brief the child", createdAt: "2026-08-14T02:29:39Z" },
            { id: "m2", role: "assistant", contentText: "on it", createdAt: "2026-08-14T02:30:00Z" },
          ],
        };
      }
      if (path.includes("/tool-calls")) return { toolCalls: [] };
      return {
        id: "task-id",
        agentId: "agent-1",
        kind: "agent",
        status: "succeeded",
        childAgentId: "child-1",
        childRunId: "run-1",
        revision: 1,
      };
    },
    getProfile: () => ({ displayName: "chang", avatarInitials: "RAY", avatarDataUrl: tinyJPEGDataUrl }),
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-id");

  const html = controller.renderChildConversationHTMLForTest({ childAgentId: "child-1", childRunId: "run-1" });
  assert.match(html, /data-message-role="user"/);
  assert.match(html, /class="message-head"/);
  assert.match(html, /class="message-avatar" aria-hidden="true" data-user-profile-avatar>/);
  assert.match(html, /<img class="message-avatar-image" data-user-profile-avatar-image src="data:image\/jpeg;base64,AAAA" alt="" aria-hidden="true" \/>/);
  assert.match(html, /data-user-profile-name>chang<\/span>/);
  assert.match(html, /<time class="message-time" datetime="2026-08-14T02:29:39Z"/);
  assert.doesNotMatch(html, />你<\/span>/);
  assert.doesNotMatch(html, />You<\/span>/);
  assert.match(html, /<article class="background-task-bubble role-assistant chat-message"/);
  assert.match(html, /data-agent-id="child-1"/);
  assert.match(html, /data-copy-child-message="m1"/);
  assert.match(html, /data-copy-child-message="m1"[^>]*>[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.match(html, /data-correct-child-message="m1"[^>]*aria-label="/);
  assert.match(html, /data-correct-child-message="m1"[^>]*>[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.doesNotMatch(html, /data-correct-child-message="[^"]*"[^>]*>[^<]+</);
  assert.equal(controller.ownsChildAgent("child-1"), true);
  assert.equal(controller.childMessageText("child-1", "m1"), "brief the child");
  controller.openChildCorrectionEditor("child-1", "m1");
  const editingHTML = controller.renderChildConversationHTMLForTest({ childAgentId: "child-1", childRunId: "run-1" });
  assert.match(editingHTML, /background-task-bubble role-user chat-message message-editing/);
  assert.match(editingHTML, /data-child-correction-form="m1"/);
  assert.match(editingHTML, /<textarea class="message-correction-text"[^>]*>brief the child<\/textarea>/);
  assert.doesNotMatch(editingHTML, /data-correct-child-message/);
  // The assistant turn mirrors the main transcript head as well: the Autoto
  // mark and name in place of the old bare "代理" label line.
  assert.match(html, /<span class="message-avatar message-avatar-logo" aria-hidden="true"><svg/);
  assert.match(html, /<div class="message-role">Autoto<\/div>/);
  assert.doesNotMatch(html, /<header><span>/);

  const identityOnly = createBackgroundTasksController({
    request: async (path) => {
      if (path.includes("/messages")) {
        return { messages: [{ id: "u1", role: "user", contentText: "hi", createdAt: "2026-08-14T02:29:39Z" }] };
      }
      if (path.includes("/tool-calls")) return { toolCalls: [] };
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      return { id: "task-esc", agentId: "agent-1", kind: "agent", status: "succeeded", childAgentId: "child-e", childRunId: "run-e", revision: 1 };
    },
    getProfile: () => ({ displayName: "er<erer", avatarInitials: "xy" }),
  });
  identityOnly.setAgent("agent-1");
  await identityOnly.selectTask("task-esc");
  const escapedHTML = identityOnly.renderChildConversationHTMLForTest({ childAgentId: "child-e", childRunId: "run-e" });
  assert.match(escapedHTML, /data-user-profile-avatar>XY<\/span>/);
  assert.match(escapedHTML, /data-user-profile-name>er&lt;erer<\/span>/);
  assert.doesNotMatch(escapedHTML, /er<erer/);
});

test("subagent user messages show each sender when the viewer is an account", async () => {
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return {
          messages: [
            { id: "m-ray", role: "user", contentText: "from ray", createdBy: "ray-id", author: { id: "ray-id", handle: "ray", displayName: "管理員雷", avatarInitials: "雷" } },
            { id: "m-feng", role: "user", contentText: "from feng", createdBy: "feng-id", author: { id: "feng-id", handle: "feng", displayName: "協作者風", avatarInitials: "風" } },
          ],
        };
      }
      if (path.includes("/tool-calls")) return { toolCalls: [] };
      return { id: "task-id", agentId: "agent-1", kind: "agent", status: "succeeded", childAgentId: "child-1", childRunId: "run-1", revision: 1 };
    },
    getProfile: () => ({ displayName: "管理員雷", avatarInitials: "雷" }),
    getAccount: () => ({ id: "ray-id", handle: "ray" }),
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-id");
  const html = controller.renderChildConversationHTMLForTest({ childAgentId: "child-1", childRunId: "run-1" });
  assert.match(html, /data-user-profile-name>管理員雷<\/span>/);
  assert.match(html, />協作者風 \(@feng\)<\/span>/);
  assert.doesNotMatch(html, /data-user-profile-name>協作者風/);
});

// The pane shares the main transcript's visibility rules. Raw protocol chatter
// -- "Tool X completed" result messages, legacy "Tool requested" lines, and the
// acceptance-criteria block appended to the briefing -- addresses the model,
// not the reader, and rendering it buried the conversation under boilerplate.
test("the subagent pane hides tool protocol chatter and the acceptance-criteria block", async () => {
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return {
          messages: [
            {
              id: "m1",
              role: "user",
              contentText: "查詢明天台中天氣\n\n[BACKGROUND_ACCEPTANCE_CRITERIA]\nThe JSON strings below are completion checks only.\n[\"回報最高最低溫\"]\n[/BACKGROUND_ACCEPTANCE_CRITERIA]",
              createdAt: "2026-08-12T14:30:00Z",
            },
            { id: "m2", role: "assistant", contentText: "我來查詢。\nTool requested: WebSearch (call_1)", createdAt: "2026-08-12T14:30:05Z" },
            { id: "m3", role: "user", parentToolUseId: "call_1", contentText: "Tool WebSearch (call_1) completed: Search results for 天氣", createdAt: "2026-08-12T14:30:09Z" },
            { id: "m4", role: "assistant", contentText: "明天多雲，最高 33°C。", createdAt: "2026-08-12T14:31:00Z" },
          ],
        };
      }
      if (path.includes("/tool-calls")) return { toolCalls: [] };
      return { id: "task-noise", agentId: "agent-1", kind: "agent", status: "succeeded", childAgentId: "child-n", childRunId: "run-n", revision: 1 };
    },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-noise");

  const html = controller.renderChildConversationHTMLForTest({ childAgentId: "child-n", childRunId: "run-n" });
  assert.ok(html.includes("查詢明天台中天氣"), "the briefing itself must stay visible");
  assert.ok(html.includes("我來查詢。"), "assistant text preceding a tool call must stay visible");
  assert.ok(html.includes("明天多雲"), "the final answer must stay visible");
  assert.doesNotMatch(html, /BACKGROUND_ACCEPTANCE_CRITERIA|completion checks only/, "the acceptance-criteria block addresses the model, not the reader");
  assert.doesNotMatch(html, /Tool requested:/, "legacy tool-request lines are protocol, not conversation");
  assert.doesNotMatch(html, /completed: Search results/, "tool results belong to the activity stack, not a bubble");
});

// The conversation transcript, not the detail <section>, is the element with
// overflow (.background-task-conversation has its own overflow: auto). Every
// content rewrite recreates that node at scrollTop 0, so restoring only the
// section's position yanked a reading user back to the oldest message whenever
// the child was polled or answered a follow-up message.
test("a content rewrite keeps the subagent transcript's scroll position", async () => {
  // Mimics the browser: an innerHTML write tears the subtree out and rebuilds
  // it, so the scrolling nodes come back fresh with scrollTop 0.
  function fakeDetail() {
    const detail = { scrollTop: 0, scrollHeight: 400, clientHeight: 400, conversation: null };
    detail.querySelector = (selector) => (selector === ".background-task-conversation" ? detail.conversation : null);
    Object.defineProperty(detail, "innerHTML", {
      set(markup) {
        detail.conversation = markup.includes("background-task-conversation")
          ? { scrollTop: 0, scrollHeight: 600, clientHeight: 200 }
          : null;
      },
    });
    return detail;
  }
  const tray = { classList: { toggle() {} }, detail: null };
  tray.querySelector = (selector) => (selector === ".background-task-detail" ? tray.detail : null);
  Object.defineProperty(tray, "innerHTML", {
    set(markup) {
      tray.detail = markup.includes("background-task-detail") ? fakeDetail() : null;
    },
  });

  const messages = [
    { id: "m1", role: "user", contentText: "briefing", createdAt: "2026-08-13T04:00:00Z" },
    { id: "m2", role: "assistant", contentText: "first answer", createdAt: "2026-08-13T04:01:00Z" },
  ];
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/runs/active")) return null;
      if (path.includes("/messages")) return { messages: [...messages] };
      if (path.includes("/tool-calls")) return { toolCalls: [] };
      return { id: "task-sub", agentId: "agent-1", kind: "agent", status: "running", childAgentId: "child-9", childRunId: "run-9", revision: 1 };
    },
    documentRef: { getElementById: (id) => (id === "backgroundTaskTray" ? tray : null) },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-sub");

  const opened = tray.detail?.conversation;
  assert.ok(opened, "the conversation must render");
  // A newly opened task lands at its latest content.
  assert.equal(opened.scrollTop, opened.scrollHeight, "opening a task must show the newest turn");

  // The reader scrolls back to an earlier turn, then the child answers, which
  // forces a content rewrite that replaces the transcript node.
  opened.scrollTop = 120;
  messages.push({ id: "m3", role: "assistant", contentText: "second answer", createdAt: "2026-08-13T04:02:00Z" });
  await controller.loadChildConversation("child-9", { force: true, runId: "run-9" });
  const rewritten = tray.detail?.conversation;
  assert.ok(rewritten && rewritten !== opened, "the rewrite must have replaced the transcript node");
  assert.equal(rewritten.scrollTop, 120, "a reader scrolled back must keep their place");

  // Pinned to the end instead: the next rewrite follows the new content.
  rewritten.scrollTop = rewritten.scrollHeight - rewritten.clientHeight;
  messages.push({ id: "m4", role: "assistant", contentText: "third answer", createdAt: "2026-08-13T04:03:00Z" });
  await controller.loadChildConversation("child-9", { force: true, runId: "run-9" });
  const pinned = tray.detail?.conversation;
  assert.ok(pinned, "the conversation must still render");
  assert.equal(pinned.scrollTop, pinned.scrollHeight, "a reader at the end keeps following the tail");
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

// The pane loads the newest slice of messages but fetches the task's whole run
// of tool calls, so a long-lived subagent always has calls whose owning turn
// has scrolled out. Emitting those after the bubbles parked an earlier task's
// entire tool history directly beneath the newest reply, where it read as work
// that reply had caused.
test("subagent activity older than the loaded window leads the transcript", async () => {
  const controller = createBackgroundTasksController({
    request: async (path) => {
      if (path.endsWith("/output?afterSequence=0")) return { chunks: [] };
      if (path.includes("/messages")) {
        return {
          messages: [
            { id: "m1", role: "user", contentText: "下沙確實好聽", createdAt: "2026-08-14T03:45:00Z" },
            { id: "m2", role: "assistant", contentText: "真的，很耐聽。", createdAt: "2026-08-14T03:45:58Z" },
          ],
        };
      }
      if (path.includes("/tool-calls")) {
        return {
          toolCalls: [
            // Owned by a turn from an earlier task, no longer in the window.
            { toolUseId: "t-old", toolName: "WebSearch", status: "completed", runId: "run-1", messageId: "scrolled-out", createdAt: "2026-08-12T16:29:31Z" },
          ],
        };
      }
      return {
        id: "task-id", agentId: "agent-1", kind: "agent", status: "succeeded",
        childAgentId: "child-1", childRunId: "run-1", revision: 1,
      };
    },
  });
  controller.setAgent("agent-1");
  await controller.selectTask("task-id");

  const html = controller.renderChildConversationHTMLForTest({ childAgentId: "child-1", childRunId: "run-1" });
  const activityAt = html.indexOf("tool-activity-stack");
  const firstBubbleAt = html.indexOf(`<article class="background-task-bubble`);
  assert.ok(activityAt >= 0, "the orphaned activity still has to be accounted for");
  assert.ok(firstBubbleAt >= 0, "the loaded turns still render");
  assert.ok(activityAt < firstBubbleAt, "activity older than every loaded turn leads the transcript instead of trailing the newest reply");
});
