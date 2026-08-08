import { objectValue } from "./value-coercion.mjs";
import { escapeHtml } from "./dom.mjs";

export const workStateLimits = Object.freeze({
  goal: 1200,
  role: 160,
  taskText: 500,
  tasks: 40,
  activeTasks: 3,
  verification: 800,
  declaredTest: 300,
  declaredTests: 8,
  count: 999999,
});

const taskStatuses = Object.freeze(["todo", "doing", "done", "blocked"]);
const taskStatusAliases = Object.freeze({
  pending: "todo",
  queued: "todo",
  active: "doing",
  in_progress: "doing",
  "in-progress": "doing",
  running: "doing",
  complete: "done",
  completed: "done",
  failed: "blocked",
  error: "blocked",
});

function boundedText(value, limit) {
  return String(value ?? "").trim().slice(0, limit);
}

function boundedCount(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) return 0;
  return Math.min(workStateLimits.count, Math.floor(number));
}

function firstText(values, limit) {
  for (const value of values) {
    if (value && typeof value === "object") continue;
    const normalized = boundedText(value, limit);
    if (normalized) return normalized;
  }
  return "";
}

function nestedText(value, keys, limit) {
  const source = objectValue(value);
  return firstText(keys.map((key) => source[key]), limit);
}

function normalizeTaskStatus(value) {
  const raw = boundedText(value, 32).toLowerCase();
  const status = taskStatusAliases[raw] || raw;
  return taskStatuses.includes(status) ? status : "todo";
}

function normalizeTask(value) {
  if (typeof value === "string" || typeof value === "number") {
    return { text: boundedText(value, workStateLimits.taskText), status: "todo" };
  }
  const source = objectValue(value);
  return {
    text: firstText([source.text, source.title, source.name, source.summary, source.description], workStateLimits.taskText),
    status: normalizeTaskStatus(source.status || source.state),
  };
}

function taskArray(source) {
  const tasks = source.tasks;
  if (Array.isArray(tasks)) return tasks;
  const taskSource = objectValue(tasks);
  for (const candidate of [source.activeTasks, source.taskItems, taskSource.active, taskSource.items, taskSource.list]) {
    if (Array.isArray(candidate)) return candidate;
  }
  return [];
}

function explicitCountSource(source) {
  const tasks = objectValue(source.tasks);
  return objectValue(source.taskCounts || source.task_counts || tasks.counts || source.counts);
}

function normalizeTaskCounts(source, tasks) {
  const counts = explicitCountSource(source);
  const derived = { todo: 0, doing: 0, done: 0, blocked: 0 };
  for (const task of tasks) derived[task.status] += 1;
  return Object.fromEntries(taskStatuses.map((status) => {
    const aliases = status === "doing" ? ["doing", "active", "inProgress", "in_progress", "running"]
      : status === "done" ? ["done", "complete", "completed"]
        : status === "blocked" ? ["blocked", "failed", "error"]
          : ["todo", "pending", "queued"];
    const explicit = aliases.find((key) => Object.prototype.hasOwnProperty.call(counts, key));
    return [status, explicit ? boundedCount(counts[explicit]) : derived[status]];
  }));
}

function normalizeDeclaredTest(value) {
  if (typeof value === "string" || typeof value === "number") return boundedText(value, workStateLimits.declaredTest);
  const source = objectValue(value);
  return firstText([source.name, source.command, source.text, source.title, source.description], workStateLimits.declaredTest);
}

function normalizeVerification(source) {
  const verification = objectValue(source.verification);
  const review = objectValue(source.review || verification.review || source.reviewer);
  const declared = [
    verification.declaredTests,
    verification.declared_tests,
    source.declaredTests,
    source.declared_tests,
    verification.tests,
  ].find(Array.isArray) || [];
  const declaredTests = declared.slice(0, workStateLimits.declaredTests).map(normalizeDeclaredTest).filter(Boolean);
  const findings = Array.isArray(verification.reviewFindings) ? verification.reviewFindings
    : Array.isArray(verification.review_findings) ? verification.review_findings
      : [];
  const reviewerVerdict = firstText([
    verification.reviewVerdict,
    verification.review_verdict,
    verification.reviewerVerdict,
    verification.reviewer_verdict,
    review.verdict,
    review.status,
    source.reviewerVerdict,
    source.reviewer_verdict,
  ], 40).toLowerCase();
  const planStatus = firstText([verification.planStatus, verification.plan_status], 40).toLowerCase();
  const explicitStatus = firstText([verification.status, verification.state, source.verificationStatus, source.verification_status], 40).toLowerCase();
  const status = explicitStatus || (planStatus === "stale" ? "stale" : declaredTests.length ? "declared" : reviewerVerdict ? "reviewed" : "");
  return {
    status,
    summary: firstText([verification.summary, verification.message, verification.detail, findings[0], source.verificationSummary], workStateLimits.verification),
    reviewerVerdict,
    declaredTests,
  };
}

function workStateSource(value) {
  const root = objectValue(value);
  if (Object.prototype.hasOwnProperty.call(root, "workState")) return objectValue(root.workState);
  if (Object.prototype.hasOwnProperty.call(root, "work_state")) return objectValue(root.work_state);
  const directKeys = ["goal", "role", "tasks", "taskCounts", "task_counts", "activeTasks", "verification", "declaredTests", "declared_tests"];
  return directKeys.some((key) => Object.prototype.hasOwnProperty.call(root, key)) ? root : null;
}

export function normalizeWorkState(value, options = {}) {
  const source = workStateSource(value);
  if (!source) return null;
  const goalValue = source.goal;
  const executionRoles = Array.isArray(source.executionRoles) ? source.executionRoles : [];
  const projectedAgentId = boundedText(source.agentId || source.agent_id || options.agentId, 160);
  const childRole = executionRoles.find((item) => {
    const projected = objectValue(item);
    const itemAgentId = boundedText(projected.agentId || projected.agent_id, 160);
    return itemAgentId && projectedAgentId && itemAgentId !== projectedAgentId;
  });
  const roleValue = source.role || source.executionRole || childRole || executionRoles[0];
  const tasks = taskArray(source).slice(0, workStateLimits.tasks).map(normalizeTask).filter((task) => task.text);
  const counts = normalizeTaskCounts(source, tasks);
  const verification = normalizeVerification(source);
  const goal = firstText([
    goalValue,
    nestedText(goalValue, ["text", "summary", "title", "description"], workStateLimits.goal),
    source.objective,
  ], workStateLimits.goal);
  const role = firstText([
    roleValue,
    nestedText(roleValue, ["role", "subagentType", "subagent_type", "worklineRole", "workline_role", "name", "title", "type", "label"], workStateLimits.role),
    source.agentRole,
    source.agent_role,
  ], workStateLimits.role);
  const hasTaskData = ["tasks", "taskCounts", "task_counts", "activeTasks", "taskItems", "counts"]
    .some((key) => Object.prototype.hasOwnProperty.call(source, key));
  const agentId = projectedAgentId;
  const activeTasks = tasks.filter((task) => task.status !== "done").slice(0, workStateLimits.activeTasks);
  const normalized = { agentId, goal, role, counts, activeTasks, verification, hasTaskData };
  const hasContent = goal || role || hasTaskData || verification.status || verification.summary
    || verification.reviewerVerdict || verification.declaredTests.length;
  return hasContent ? normalized : null;
}

export function normalizeWorkStateSnapshot(snapshot) {
  const source = objectValue(snapshot);
  return normalizeWorkState(source, { agentId: source.agent?.id });
}

function label(labels, key, fallback) {
  return boundedText(labels?.[key] || fallback, 160);
}

function statusLabel(status, labels) {
  const normalized = boundedText(status, 40).toLowerCase();
  return label(labels, normalized, normalized || label(labels, "unknown", "Unknown"));
}

function detailRow(title, value, className = "") {
  return `<div class="conversation-detail-row${className ? ` ${className}` : ""}"><span>${escapeHtml(title)}</span><strong>${escapeHtml(value)}</strong></div>`;
}

export function renderWorkStateHTML(value, labels = {}) {
  const state = normalizeWorkState(value);
  if (!state) return "";
  const countText = ["todo", "doing", "done", "blocked"]
    .map((status) => `${label(labels?.taskStatuses, status, status)} ${state.counts[status]}`)
    .join(" · ");
  const rows = [];
  if (state.goal) rows.push(detailRow(label(labels, "goal", "Goal"), state.goal));
  if (state.role) rows.push(detailRow(label(labels, "role", "Role"), state.role));
  if (state.hasTaskData) rows.push(detailRow(label(labels, "taskCounts", "Task counts"), countText));
  for (const task of state.activeTasks) {
    rows.push(detailRow(label(labels, "activeTask", "Active task"), `${statusLabel(task.status, labels?.taskStatuses)} · ${task.text}`));
  }
  if (state.verification.status) {
    rows.push(detailRow(label(labels, "verification", "Verification"), statusLabel(state.verification.status, labels?.verificationStatuses)));
  }
  if (state.verification.summary) rows.push(detailRow(label(labels, "verification", "Verification"), state.verification.summary));
  if (state.verification.reviewerVerdict) {
    rows.push(detailRow(label(labels, "reviewer", "Reviewer"), statusLabel(state.verification.reviewerVerdict, labels?.reviewerStatuses)));
  }
  for (const declaredTest of state.verification.declaredTests) {
    rows.push(detailRow(label(labels, "declaredTest", "Declared test"), declaredTest));
  }
  if (!rows.length) return "";
  return `<section class="conversation-detail-table work-state-details"><h3>${escapeHtml(label(labels, "title", "Work state"))}</h3>${rows.join("")}</section>`;
}
