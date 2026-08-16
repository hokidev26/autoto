import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatNumber } from "./formatters.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import {
  compactToolText,
  firstToolValue,
  isAgentToolActivity,
  isBashToolActivity,
  maxDurationMs,
  maxToolActivityDiffLines,
  maxToolActivityText,
  maxToolFactLabels,
  mcpCallArguments,
  mcpHostToolKind,
  mcpInnerToolName,
  normalizeToolActivity,
  safeToolText,
  toolActivityDisplayName,
  toolActivityOutputText,
  toolActivityTarget,
  toolStatusValue,
} from "./chat-rendering-tools-normalize.mjs";
import {
  friendlyToolName,
  toolActivityGlyph,
  toolActivityIconHTML,
  toolActivityIconKind,
  disclosureChevronMarkup,
} from "./chat-rendering-tools-glyphs.mjs";
import { renderReasoningStepHTML } from "./chat-rendering-tools-reasoning.mjs";

function toolActivityStatusClass(status) {
  const value = toolStatusValue(status);
  if (value === "completed") return "status-completed";
  if (value === "running") return "status-running";
  if (["pending_approval", "interrupted", "superseded", "cancelled", "canceled"].includes(value)) return "status-warn";
  return "status-error";
}

function toolActivityStatusLabel(status) {
  const value = toolStatusValue(status);
  if (value === "running") return cr("activity.running");
  if (value === "completed") return ""; // completed is the default; no label needed
  if (value === "pending_approval") return cr("run.toolStatus.pendingApproval");
  if (value === "denied") return cr("run.toolStatus.denied");
  if (value === "interrupted") return cr("run.status.interrupted");
  if (value === "superseded") return cr("run.status.superseded");
  return cr("activity.failed");
}

function toolActivityDeviceLabel(deviceId) {
  if (!deviceId) return "";
  return /^(local|localhost|local-service)$/i.test(String(deviceId)) ? cr("activity.localService") : String(deviceId);
}

function toolDecisionLabel(decision) {
  if (decision === "allow" || decision === "allow_once") return cr("activity.decisionAllow");
  if (decision === "allow_session") return cr("activity.decisionAllowSession");
  if (decision === "ask") return cr("activity.decisionAsk");
  if (decision === "deny") return cr("activity.decisionDeny");
  return "";
}

function toolDecisionSourceLabel(source) {
  const keys = {
    hard_danger_block: "hardDangerBlock",
    command_review: "commandReview",
    danger_reflection: "dangerReflection",
    read_only_cap: "readOnlyCap",
    rule: "rule",
    session_approval: "sessionApproval",
    default_policy: "defaultPolicy",
    built_in_exec_whitelist: "builtInExecWhitelist",
    permission_mode: "permissionMode",
    workflow_preferences: "workflowPreferences",
    policy_unavailable: "policyUnavailable",
    workflow_unavailable: "workflowUnavailable",
    human_approval: "humanApproval",
    generation_invalidation: "generationInvalidation",
    policy: "policy",
    user: "user",
    system: "system",
    plan_mode: "planMode",
  };
  return keys[source] ? cr(`activity.decisionSource.${keys[source]}`) : "";
}

function toolDecisionScopeLabel(scope) {
  const keys = {
    tool_call: "toolCall",
    once: "once",
    session: "session",
    rule: "rule",
    policy: "policy",
    permission_mode: "permissionMode",
    workflow_preferences: "workflowPreferences",
    run: "run",
    plan: "plan",
    global: "global",
  };
  return keys[scope] ? cr(`activity.decisionScope.${keys[scope]}`) : "";
}

export function toolActivitySafetyMetaParts(item) {
  const source = toolDecisionSourceLabel(item.decisionSource);
  const scope = toolDecisionScopeLabel(item.decisionScope);
  return [
    source ? cr("activity.decisionSourceLabel", { source }) : "",
    scope ? cr("activity.decisionScopeLabel", { scope }) : "",
  ].filter(Boolean);
}

function toolActivityFactLabels(item) {
  if (!isBashToolActivity(item) || !item.commandFacts) return [];
  const facts = item.commandFacts;
  const labels = [];
  if (facts.parseKnown === false) labels.push(cr("activity.factParseUnknown"));
  if (facts.parseKnown === true) {
    labels.push(facts.compound === true || (facts.commandCount !== null && facts.commandCount > 1) ? cr("activity.factCompound") : cr("activity.factSingle"));
  }
  if (facts.pipeline === true) labels.push(cr("activity.factPipeline"));
  if (facts.redirection === true) labels.push(cr("activity.factRedirection"));
  if (facts.substitution === true) labels.push(cr("activity.factSubstitution"));
  if (facts.background === true) labels.push(cr("activity.factBackground"));
  if (facts.program) labels.push(cr("activity.factProgram", { program: facts.program }));
  if (facts.subcommand) labels.push(cr("activity.factSubcommand", { subcommand: facts.subcommand }));
  facts.effects.forEach((effect) => labels.push(cr("activity.factEffect", { effect })));
  facts.dangerous.forEach((dangerous) => labels.push(cr("activity.factDanger", { dangerous })));
  (facts.sensitive || []).forEach((sensitive) => labels.push(cr("activity.factSensitive", { sensitive })));
  return labels.slice(0, maxToolFactLabels + 8);
}

export function renderToolActivityFactTags(item) {
  const labels = toolActivityFactLabels(item);
  if (!labels.length) return "";
  return `<div class="tool-activity-facts" aria-label="${escapeAttr(cr("activity.commandFacts"))}">${labels.map((label) => `<span class="tool-activity-fact">${escapeHtml(label)}</span>`).join("")}</div>`;
}

function renderToolActivityClassificationWarning(item) {
  const facts = item?.commandFacts;
  const dynamicProgram = String(facts?.program || "").trim().toLowerCase() === "dynamic";
  if (!isBashToolActivity(item) || (item?.shellSafe !== false && facts?.parseKnown !== false && !dynamicProgram)) return "";
  return `<div class="tool-activity-warning" role="alert">${escapeHtml(cr("activity.unclassifiedDynamicWarning"))}</div>`;
}

// The two model-facing danger layers are easy to conflate: both labels talk
// about danger, but one is a static rule floor that nothing can override and
// the other is a configurable LLM gate whose escalations a human can approve.
// The hint answers the reader's actual question -- "who decided this, and what
// can I do about it" -- right on the card.
function toolDecisionSourceHint(source) {
  if (source === "hard_danger_block") return cr("activity.sourceHintHardBlock");
  if (source === "danger_reflection") return cr("activity.sourceHintReflection");
  return "";
}

export function renderToolActivitySafetySummary(item) {
  const decision = toolDecisionLabel(item.decision);
  const source = toolDecisionSourceLabel(item.decisionSource);
  const scope = toolDecisionScopeLabel(item.decisionScope);
  const chips = [
    decision ? cr("activity.decisionLabel", { decision }) : "",
    source ? cr("activity.decisionSourceLabel", { source }) : "",
    scope ? cr("activity.decisionScopeLabel", { scope }) : "",
  ].filter(Boolean);
  const audit = [
    item.ruleId ? cr("activity.ruleId", { ruleId: item.ruleId }) : "",
    item.permissionDecisionReason ? cr("activity.decisionReason", { reason: item.permissionDecisionReason }) : "",
  ].filter(Boolean);
  if (!chips.length && !audit.length) return "";
  const hint = toolDecisionSourceHint(item.decisionSource);
  const auditOpen = item.decision === "deny" || item.decision === "ask";
  const chipsHTML = chips.length
    ? `<div class="tool-activity-safety-chips">${chips.map((label) => `<span class="tool-activity-safety-chip">${escapeHtml(label)}</span>`).join("")}</div>`
    : "";
  const auditHTML = audit.length
    ? `<details class="tool-activity-safety-audit"${auditOpen ? " open" : ""}><summary>${escapeHtml(cr("activity.safetyAudit"))}</summary><div class="tool-activity-safety-summary">${escapeHtml(audit.join(" · "))}</div></details>`
    : "";
  return `<div class="tool-activity-safety"><div class="tool-activity-meta">${escapeHtml(cr("activity.safetyDecision"))}</div>${chipsHTML}${auditHTML}${hint ? `<div class="tool-activity-safety-hint">${escapeHtml(hint)}</div>` : ""}</div>`;
}

// Allow + default policy is the common successful path. Repeating those chips
// on every Edit/Read card drowned the diff. Unusual sources, denials, and
// audit identifiers still earn a row.
function toolActivitySafetyIsRoutine(item) {
  const decision = String(item?.decision || "");
  const source = String(item?.decisionSource || "");
  const scope = String(item?.decisionScope || "");
  if (decision && decision !== "allow" && decision !== "allow_once") return false;
  if (source && source !== "default_policy") return false;
  if (scope && scope !== "tool_call" && scope !== "once") return false;
  if (item?.ruleId || item?.permissionDecisionReason) return false;
  return true;
}

function toolActivityVisibleInputRecord(tool) {
  if (mcpHostToolKind(tool?.toolName) === "call") return mcpCallArguments(tool);
  return tool?.inputJson && typeof tool.inputJson === "object" && !Array.isArray(tool.inputJson)
    ? tool.inputJson
    : {};
}

const redundantDetailInputKeys = new Set([
  "file_path",
  "filePath",
  "path",
  "old_string",
  "oldString",
  "new_string",
  "newString",
  "oldStringBytes",
  "newStringBytes",
  "old_string_bytes",
  "new_string_bytes",
]);

function toolActivityDetailInputText(tool, { hasDiff = false } = {}) {
  const visible = toolActivityVisibleInputRecord(tool);
  const remaining = Object.fromEntries(Object.entries(visible).filter(([key]) => {
    if (redundantDetailInputKeys.has(key)) return false;
    if (hasDiff && (key === "content" || key === "contents")) return false;
    return true;
  }));
  if (!Object.keys(remaining).length) return "";
  try {
    return safeToolText(JSON.stringify(remaining, null, 2));
  } catch {
    return safeToolText(remaining);
  }
}

function toolActivityDiffText(item) {
  const output = item.outputJson && typeof item.outputJson === "object" ? item.outputJson : {};
  const candidates = [
    output?.result?.meta?.diff,
    output?.meta?.diff,
    output?.result?.diff,
    output?.diff,
  ];
  return candidates.find((value) => typeof value === "string" && value.trim()) || "";
}

function fallbackToolDiff(item) {
  const input = item.inputJson && typeof item.inputJson === "object" ? item.inputJson : {};
  const before = firstToolValue(input, "old_string", "oldString");
  const after = firstToolValue(input, "new_string", "newString");
  if (before === undefined && after === undefined) return "";
  return `--- before\n+++ after\n${String(before || "").split("\n").map((line) => `-${line}`).join("\n")}\n${String(after || "").split("\n").map((line) => `+${line}`).join("\n")}`;
}

function countUnifiedDiffStat(diff) {
  let added = 0;
  let deleted = 0;
  const lines = String(diff || "").split("\n");
  const limit = Math.min(lines.length, maxToolActivityDiffLines * 4);
  for (let index = 0; index < limit; index += 1) {
    const line = lines[index];
    if (!line || line.startsWith("+++") || line.startsWith("---") || line.startsWith("@@") || line.startsWith("\\")) continue;
    if (line.startsWith("+")) added += 1;
    else if (line.startsWith("-")) deleted += 1;
  }
  return added || deleted ? { added, deleted } : null;
}

function toolActivityRowDiffStat(tool) {
  const kind = toolActivityIconKind(toolActivityDisplayName(tool));
  if (kind !== "edit" && kind !== "write") return null;
  if (toolStatusValue(tool.status) !== "completed") return null;
  const fromDiff = countUnifiedDiffStat(toolActivityDiffText(tool) || fallbackToolDiff(tool));
  if (fromDiff) return fromDiff;
  if (kind !== "write") return null;
  const content = firstToolValue(tool.inputJson || {}, "content", "contents");
  if (typeof content !== "string" || !content) return null;
  const added = content.split("\n").length;
  return added > 0 ? { added, deleted: 0 } : null;
}

export function renderToolDiffHTML(item = {}) {
  const normalized = normalizeToolActivity(item);
  const diff = toolActivityDiffText(normalized) || fallbackToolDiff(normalized);
  if (!diff) return "";
  let oldLine = 0;
  let newLine = 0;
  const allLines = diff.split("\n");
  const lines = allLines.slice(0, maxToolActivityDiffLines);
  const rendered = lines.map((line) => {
    let type = "context";
    let number = "";
    const hunk = line.match(/^@@\s+-(\d+)(?:,\d+)?\s+\+(\d+)(?:,\d+)?\s+@@/);
    if (hunk) {
      type = "meta";
      oldLine = Number(hunk[1]);
      newLine = Number(hunk[2]);
    } else if (/^(---|\+\+\+|\\ No newline)/.test(line)) {
      type = "meta";
    } else if (line.startsWith("+") && !line.startsWith("+++")) {
      type = "add";
      if (newLine <= 0) newLine = 1;
      number = newLine++;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      type = "del";
      if (oldLine <= 0) oldLine = 1;
      number = oldLine++;
    } else {
      if (oldLine <= 0) oldLine = 1;
      if (newLine <= 0) newLine = 1;
      number = newLine;
      oldLine += 1;
      newLine += 1;
    }
    return `<div class="tool-diff-line ${type}"><span class="tool-diff-line-number" aria-hidden="true">${number === "" ? "" : escapeHtml(String(number))}</span><span>${escapeHtml(safeToolText(line, 4_000))}</span></div>`;
  }).join("");
  const note = allLines.length > lines.length || normalized.truncated ? `<div class="tool-activity-empty">${escapeHtml(cr("activity.truncated"))}</div>` : "";
  return `<div class="tool-diff" aria-label="${escapeAttr(cr("activity.diff"))}">${rendered}${note}</div>`;
}

const agentTaskStatuses = new Set(["queued", "waiting_approval", "validating", "running", "cancel_requested", "succeeded", "failed", "canceled", "interrupted"]);
const agentTaskExpandedStatuses = new Set(["waiting_approval", "failed", "canceled", "interrupted"]);
const agentTaskCancellableStatuses = new Set(["queued", "waiting_approval", "validating", "running"]);
const maxAgentTaskID = 160;
const maxAgentTaskDescription = 240;
const maxAgentTaskRole = 80;
const maxAgentTaskModel = 256;
const maxAgentTaskWorkdir = 1024;
const maxAgentTaskErrorCode = 96;
const maxAgentTaskErrorMessage = 1024;
const maxAgentTaskAcceptanceCount = 16;

function safeAgentTaskObject(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;
  if (value.backgroundTask && typeof value.backgroundTask === "object" && !Array.isArray(value.backgroundTask)) return value.backgroundTask;
  if (value.task && typeof value.task === "object" && !Array.isArray(value.task)) return value.task;
  if (value.data?.backgroundTask && typeof value.data.backgroundTask === "object" && !Array.isArray(value.data.backgroundTask)) return value.data.backgroundTask;
  if (value.data?.task && typeof value.data.task === "object" && !Array.isArray(value.data.task)) return value.data.task;
  return value;
}

function safeAgentTaskJSON(value) {
  if (!value) return {};
  if (typeof value === "object" && !Array.isArray(value)) return value;
  if (typeof value !== "string" || value.length > maxToolActivityText) return {};
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function agentTaskStatus(value) {
  const status = String(value || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
  if (["pending", "waiting"].includes(status)) return "queued";
  if (["started", "in_progress"].includes(status)) return "running";
  if (["completed", "complete", "success", "done"].includes(status)) return "succeeded";
  if (["error", "failure"].includes(status)) return "failed";
  if (status === "cancelled") return "canceled";
  return agentTaskStatuses.has(status) ? status : "";
}

function agentTaskNumber(value, maximum) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 0 && number <= maximum ? number : null;
}

function agentTaskDuration(task) {
  const explicit = Number(firstToolValue(task, "durationMs", "duration_ms"));
  if (Number.isFinite(explicit) && explicit >= 0 && explicit <= maxDurationMs) return explicit;
  const startedAt = firstToolValue(task, "startedAt", "started_at");
  const completedAt = firstToolValue(task, "completedAt", "completed_at", "finishedAt", "finished_at");
  if (!startedAt || !completedAt) return 0;
  const elapsed = Date.parse(completedAt) - Date.parse(startedAt);
  return Number.isFinite(elapsed) && elapsed >= 0 && elapsed <= maxDurationMs ? elapsed : 0;
}

function formatAgentTaskDuration(durationMs) {
  if (!(durationMs > 0)) return "";
  if (durationMs < 1000) return `${formatNumber(Math.round(durationMs))} ms`;
  if (durationMs < 60_000) return `${formatNumber(durationMs / 1000, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} s`;
  return `${formatNumber(durationMs / 60_000, { minimumFractionDigits: 1, maximumFractionDigits: 1 })} min`;
}

function embeddedAgentBackgroundTask(item, tool) {
  const source = item?.data && typeof item.data === "object" ? { ...item, ...item.data } : item;
  const output = safeAgentTaskJSON(tool.output || tool.resultPreview);
  const outputJSON = safeAgentTaskObject(tool.outputJson) || {};
  const candidate = safeAgentTaskObject(output) || safeAgentTaskObject(outputJSON);
  const taskId = firstToolValue(source, "backgroundTaskId", "background_task_id", "taskId", "task_id")
    || firstToolValue(output, "backgroundTaskId", "background_task_id", "taskId", "task_id", "id")
    || firstToolValue(outputJSON, "backgroundTaskId", "background_task_id", "taskId", "task_id", "id");
  if (candidate && Object.keys(candidate).length) return taskId ? { ...candidate, id: firstToolValue(candidate, "id", "taskId", "task_id") || taskId } : candidate;
  return taskId ? { id: taskId } : null;
}

function embeddedAgentBackgroundTaskHandle(item, tool) {
  const task = embeddedAgentBackgroundTask(item, tool);
  const taskId = firstToolValue(task || {}, "id", "taskId", "task_id", "backgroundTaskId", "background_task_id");
  return taskId ? { id: taskId } : null;
}

function resolveAgentBackgroundTask(item, tool, options) {
  if (Object.prototype.hasOwnProperty.call(options, "backgroundTask")) {
    const task = options.backgroundTask;
    return task === null || task === undefined || safeAgentTaskObject(task) ? { ok: true, task: safeAgentTaskObject(task) } : { ok: false, task: null };
  }
  if (typeof options.resolveBackgroundTask === "function") {
    try {
      const task = options.resolveBackgroundTask(tool);
      if (task === null || task === undefined) return { ok: true, task: embeddedAgentBackgroundTaskHandle(item, tool) };
      return safeAgentTaskObject(task) ? { ok: true, task: safeAgentTaskObject(task) } : { ok: false, task: null };
    } catch {
      return { ok: false, task: null };
    }
  }
  return { ok: true, task: embeddedAgentBackgroundTask(item, tool) };
}

export function normalizeAgentTaskActivity(item = {}, backgroundTask = null) {
  const tool = normalizeToolActivity(item);
  if (!isAgentToolActivity(tool)) return null;
  const task = safeAgentTaskObject(backgroundTask) || {};
  const input = tool.inputJson && typeof tool.inputJson === "object" ? tool.inputJson : {};
  const summary = safeAgentTaskJSON(firstToolValue(task, "publicSummary", "public_summary", "summary"));
  const result = safeAgentTaskJSON(firstToolValue(task, "result", "resultJson", "result_json"));
  const taskId = compactToolText(firstToolValue(task, "id", "taskId", "task_id", "backgroundTaskId", "background_task_id"), maxAgentTaskID);
  const backgroundStatus = agentTaskStatus(firstToolValue(task, "status", "state"));
  const childAgentId = compactToolText(firstToolValue(task, "childAgentId", "child_agent_id") || firstToolValue(result, "childAgentId", "child_agent_id"), maxAgentTaskID);
  const childRunId = compactToolText(firstToolValue(task, "childRunId", "child_run_id") || firstToolValue(result, "childRunId", "child_run_id"), maxAgentTaskID);
  const toolDispatched = tool.status === "completed";
  const status = backgroundStatus === "running" && !childAgentId
    ? "validating"
    : (backgroundStatus || (tool.status === "error" ? "failed" : (toolDispatched ? "dispatched" : "dispatching")));
  const acceptanceCriteria = firstToolValue(input, "acceptance_criteria", "acceptanceCriteria");
  const acceptanceCount = agentTaskNumber(firstToolValue(summary, "acceptanceCount", "acceptance_count"), maxAgentTaskAcceptanceCount)
    ?? agentTaskNumber(firstToolValue(result, "acceptanceCount", "acceptance_count"), maxAgentTaskAcceptanceCount)
    ?? (Array.isArray(acceptanceCriteria) ? Math.min(acceptanceCriteria.length, maxAgentTaskAcceptanceCount) : 0);
  const requestedRole = compactToolText(firstToolValue(summary, "requestedSubagentType", "requested_subagent_type") || firstToolValue(input, "subagent_type", "subagentType", "role"), maxAgentTaskRole);
  const resolvedRole = compactToolText(firstToolValue(result, "role", "subagentType", "subagent_type") || firstToolValue(summary, "subagentType", "subagent_type", "role") || requestedRole || cr("subagent.roleAuto"), maxAgentTaskRole);
  const requestedModel = compactToolText(firstToolValue(summary, "requestedModel") || firstToolValue(input, "model"), maxAgentTaskModel);
  const actualModel = compactToolText(firstToolValue(result, "model") || firstToolValue(summary, "model"), maxAgentTaskModel);
  const workdir = compactToolText(firstToolValue(summary, "workdir", "workingDirectory", "working_directory") || firstToolValue(result, "workdir", "workingDirectory", "working_directory") || firstToolValue(input, "workdir"), maxAgentTaskWorkdir);
  const errorCode = compactToolText(firstToolValue(task, "errorCode", "error_code") || firstToolValue(result, "errorCode", "error_code"), maxAgentTaskErrorCode);
  const errorMessage = compactToolText(firstToolValue(task, "errorMessage", "error_message", "error") || firstToolValue(result, "errorMessage", "error_message", "error"), maxAgentTaskErrorMessage);
  return {
    tool,
    taskPresent: Boolean(backgroundStatus),
    taskId,
    description: compactToolText(firstToolValue(summary, "description") || firstToolValue(input, "description") || cr("subagent.descriptionFallback"), maxAgentTaskDescription),
    requestedRole,
    role: resolvedRole,
    requestedModel,
    actualModel,
    workdir,
    acceptanceCount,
    status,
    toolDispatched,
    durationMs: agentTaskDuration(task) || tool.durationMs,
    childAgentId,
    childRunId,
    ownerAgentId: compactToolText(firstToolValue(task, "ownerAgentId", "owner_agent_id") || tool.agentId, maxAgentTaskID),
    errorCode,
    errorMessage,
    expanded: agentTaskExpandedStatuses.has(status),
    cancellable: Boolean(taskId && agentTaskCancellableStatuses.has(status)),
  };
}

function agentTaskStatusLabel(activity) {
  if (activity.status === "dispatched") return activity.taskId ? cr("subagent.status.dispatched") : cr("subagent.status.dispatchedWaiting");
  if (activity.status === "dispatching") return cr("subagent.status.dispatching");
  return cr(`subagent.status.${activity.status || "unknown"}`);
}

function agentTaskStatusClass(status) {
  if (status === "succeeded") return "status-completed";
  if (["queued", "validating", "running", "dispatching", "dispatched"].includes(status)) return "status-running";
  if (["waiting_approval", "cancel_requested", "canceled", "interrupted"].includes(status)) return "status-warn";
  return "status-error";
}

function agentTaskStatusGlyphKind(status) {
  const statusClass = agentTaskStatusClass(status);
  if (statusClass === "status-completed") return "done";
  if (statusClass === "status-running") return "dispatch";
  return "alert";
}

function agentTaskFailureNotice(activity) {
  if (activity.status !== "failed") return "";
  const code = activity.errorCode.toLowerCase();
  if (code.includes("rejected") || code === "invalid_payload" || code === "scope_rejected") return cr("subagent.failure.requestRejected");
  if (code.includes("unavailable")) return cr("subagent.failure.unavailable");
  if (code.startsWith("child_") || code.includes("child_run")) return cr("subagent.failure.childRun");
  return cr("subagent.failure.generic");
}

function agentTaskErrorDetails(activity) {
  if (activity.status !== "failed" || (!activity.errorCode && !activity.errorMessage)) return "";
  const parts = [
    activity.errorCode ? cr("subagent.errorCode", { code: activity.errorCode }) : "",
    activity.errorMessage ? cr("subagent.errorMessage", { message: activity.errorMessage }) : "",
  ].filter(Boolean);
  return parts.join(" · ");
}

function renderAgentTaskActionsHTML(activity) {
  const actions = [];
  if (activity.taskId) actions.push(`<button class="ghost-btn mini" type="button" data-subagent-action="view-task" data-task-id="${escapeAttr(activity.taskId)}">${escapeHtml(cr("subagent.action.viewTask"))}</button>`);
  if (activity.cancellable) actions.push(`<button class="ghost-btn mini danger" type="button" data-subagent-action="cancel" data-task-id="${escapeAttr(activity.taskId)}">${escapeHtml(cr("subagent.action.cancel"))}</button>`);
  if (activity.childAgentId) actions.push(`<button class="ghost-btn mini" type="button" data-subagent-action="open-agent" data-child-agent-id="${escapeAttr(activity.childAgentId)}">${escapeHtml(cr("subagent.action.openAgent"))}</button>`);
  if (activity.childAgentId && activity.childRunId) actions.push(`<button class="ghost-btn mini" type="button" data-subagent-action="open-run" data-child-run-id="${escapeAttr(activity.childRunId)}" data-child-agent-id="${escapeAttr(activity.childAgentId)}">${escapeHtml(cr("subagent.action.openRun"))}</button>`);
  return actions.length ? `<div class="approval-actions subagent-task-actions">${actions.join("")}</div>` : "";
}

export function renderAgentTaskActivityCardHTML(item = {}, backgroundTask = null, options = {}) {
  const activity = normalizeAgentTaskActivity(item, backgroundTask);
  if (!activity) return "";
  const { tool } = activity;
  const statusLabel = agentTaskStatusLabel(activity);
  const duration = formatAgentTaskDuration(activity.durationMs);
  const requestedModel = activity.requestedModel || cr("subagent.modelAuto");
  const actualModel = activity.actualModel || cr("subagent.modelPending");
  const meta = [
    cr("subagent.requestedRole", { role: activity.requestedRole || cr("subagent.roleAuto") }),
    cr("subagent.resolvedRole", { role: activity.role }),
    cr("subagent.requestedModel", { model: requestedModel }),
    cr("subagent.actualModel", { model: actualModel }),
    activity.workdir ? cr("subagent.workdir", { path: activity.workdir }) : "",
    cr("subagent.acceptanceCount", { count: activity.acceptanceCount }),
    duration ? cr("subagent.duration", { duration }) : "",
  ].filter(Boolean);
  const taskState = !activity.taskPresent && activity.toolDispatched ? cr("subagent.waitingTaskInfo") : "";
  const failure = agentTaskFailureNotice(activity);
  const errorDetails = agentTaskErrorDetails(activity);
  const safeAudit = [
    activity.requestedRole ? cr("subagent.requestedRole", { role: activity.requestedRole }) : "",
    cr("subagent.resolvedRole", { role: activity.role }),
    requestedModel ? cr("subagent.requestedModel", { model: requestedModel }) : "",
    actualModel ? cr("subagent.actualModel", { model: actualModel }) : "",
    activity.workdir ? cr("subagent.workdir", { path: activity.workdir }) : "",
  ].filter(Boolean).join(" · ");
  const label = [cr("subagent.title"), activity.description, statusLabel].filter(Boolean).join(" · ");
  return `
    <article class="tool-activity-card live-tool-output-card subagent-task-card chat-flow-item chat-flow-left chat-report-card ${escapeAttr(agentTaskStatusClass(activity.status))}" aria-label="${escapeAttr(label)}" data-chat-alignment="left" data-chat-report="subagent-task" data-subagent-card data-subagent-status="${escapeAttr(activity.status)}" data-live-tool-output-card="${escapeAttr(tool.toolUseId)}" data-tool-use-id="${escapeAttr(tool.toolUseId)}" data-run-id="${escapeAttr(tool.runId)}"${activity.taskId ? ` data-task-id="${escapeAttr(activity.taskId)}"` : ""}>
      <details class="subagent-task-summary"${activity.expanded ? " open" : ""}>
        <summary class="tool-activity-head live-tool-output-head">
          <span class="tool-activity-icon tool-activity-icon-task" aria-hidden="true">${toolActivityGlyph(agentTaskStatusGlyphKind(activity.status))}</span>
          <span class="tool-activity-main">
            <span class="tool-activity-title live-tool-output-title">${escapeHtml(cr("subagent.title"))}</span>
            <span class="tool-activity-target">${escapeHtml(activity.description)}</span>
          </span>
          <span class="tool-activity-status live-tool-output-dot" role="status" aria-live="polite">${escapeHtml(statusLabel)}</span>
        </summary>
        <div class="tool-activity-meta live-tool-output-meta">${escapeHtml(meta.join(" · "))}</div>
        ${taskState ? `<div class="tool-activity-empty subagent-task-notice">${escapeHtml(taskState)}</div>` : ""}
        ${failure ? `<div class="tool-activity-warning subagent-task-notice" role="alert">${escapeHtml(failure)}</div>` : ""}
        ${errorDetails ? `<div class="tool-activity-warning subagent-task-notice" role="alert">${escapeHtml(errorDetails)}</div>` : ""}
        ${renderAgentTaskActionsHTML(activity)}
      </details>
      <details class="tool-activity-details subagent-task-audit"${options.detailsExpanded ? " open" : ""}>
        <summary>${disclosureChevronMarkup()}${escapeHtml(cr("subagent.auditDetails"))}</summary>
        <div class="tool-activity-meta">${escapeHtml(cr("subagent.safeDetails"))}</div>
        <pre class="tool-activity-command">${escapeHtml(safeAudit || cr("activity.noOutput"))}</pre>
        ${streamedInputBlockHTML(tool)}
        ${tool.truncated ? `<div class="tool-activity-truncated">${escapeHtml(cr("activity.truncated"))}</div>` : ""}
      </details>
    </article>
  `;
}

// Copy always earns its place on a non-empty block; the expand toggle only
// when the text is likely to overflow the capped height (~12 monospace lines
// is what 220px fits).
function toolActivityBlockControlsHTML(text) {
  const value = String(text || "");
  const long = value.length > 1200 || value.split("\n").length > 12;
  const copy = `<button class="ghost-btn mini" type="button" data-tool-block-copy>${escapeHtml(cr("code.copy"))}</button>`;
  const toggle = long ? `<button class="ghost-btn mini" type="button" data-tool-block-toggle>${escapeHtml(cr("activity.expandBlock"))}</button>` : "";
  return `<span class="tool-activity-block-actions">${copy}${toggle}</span>`;
}

// The streamed-argument block names what is being streamed: file content for
// Write/Edit, the task brief for a subagent, the (redacted) command for Bash.
function streamedInputLabel(field) {
  if (field === "prompt") return cr("activity.streamedPrompt");
  if (field === "command") return cr("activity.streamedCommand");
  return cr("activity.streamedInput");
}

export function streamedInputBlockHTML(tool) {
  const preview = String(tool.inputPreview || "");
  if (!preview) return "";
  return `
        <div class="tool-activity-block">
          <div class="tool-activity-block-bar">
            <div class="tool-activity-meta">${escapeHtml(streamedInputLabel(tool.inputPreviewField))}</div>
            ${toolActivityBlockControlsHTML(preview)}
          </div>
          <pre class="tool-activity-output live-tool-output-body" data-tool-input-preview>${escapeHtml(preview)}</pre>
        </div>`;
}

function renderToolActivityBodyBlock(kind, label, text) {
  if (!text) return "";
  return `<div class="tool-activity-block">
          <div class="tool-activity-block-bar">
            <div class="tool-activity-meta">${escapeHtml(label)}</div>
            ${toolActivityBlockControlsHTML(text)}
          </div>
          <pre class="${kind}">${escapeHtml(text)}</pre>
        </div>`;
}

function isBoilerplateFileMutationOutput(text) {
  const value = String(text || "").trim();
  if (!value) return false;
  return /^Edited .+ \(\d+ replacement\(s\)\)$/.test(value)
    || /^Edited .+ \(\d+ edit\(s\), \d+ replacement\(s\)\)$/.test(value)
    || /^Wrote \d+ bytes to /.test(value);
}

function renderGenericToolActivityCardHTML(item = {}, options = {}) {
  const tool = normalizeToolActivity(item);
  const status = tool.status;
  const target = toolActivityTarget(tool);
  const kind = toolActivityIconKind(toolActivityDisplayName(tool));
  const diffHTML = kind === "edit" || kind === "write" ? renderToolDiffHTML(tool) : "";
  const input = toolActivityDetailInputText(tool, { hasDiff: Boolean(diffHTML) });
  const rawOutput = toolActivityOutputText(tool);
  const output = (kind === "edit" || kind === "write") && isBoilerplateFileMutationOutput(rawOutput)
    ? ""
    : rawOutput;
  const streamed = streamedInputBlockHTML(tool);
  const factTags = renderToolActivityFactTags(tool);
  const classificationWarning = renderToolActivityClassificationWarning(tool);
  const safetySummary = toolActivitySafetyIsRoutine(tool) ? "" : renderToolActivitySafetySummary(tool);
  const device = compactToolText(toolActivityDeviceLabel(tool.executionDeviceId), 80);
  const meta = [
    compactToolText(tool.risk, 40),
    !options.inlineDetail && tool.durationMs > 0 ? `${formatNumber(tool.durationMs)} ms` : "",
    options.inlineDetail ? "" : device,
  ].filter(Boolean).join(" · ");
  const cardLabel = [toolActivityDisplayName(tool), target, toolActivityStatusLabel(status)].filter(Boolean).join(" · ");
  const icon = toolActivityIconHTML(toolActivityDisplayName(tool), "tool-activity-icon");
  const inputBlock = renderToolActivityBodyBlock("tool-activity-command", cr("activity.input"), input);
  const outputBlock = renderToolActivityBodyBlock("tool-activity-output live-tool-output-body", cr("activity.output"), output);
  const emptyBlock = inputBlock || outputBlock || diffHTML || streamed
    ? ""
    : `<div class="tool-activity-empty is-compact">${escapeHtml(cr("activity.noInput"))}</div>`;
  const detailsBody = `${safetySummary}${inputBlock}${streamed}${diffHTML}${outputBlock}${emptyBlock}${tool.truncated ? `<div class="tool-activity-truncated">${escapeHtml(cr("activity.truncated"))}</div>` : ""}`;
  // Clicking the row already opens this card, so an extra "details" disclosure
  // is a second click for the same information. Full cards keep the summary
  // because they can land without a row above them.
  const detailsHTML = options.inlineDetail
    ? `<div class="tool-activity-details is-inline">${detailsBody}</div>`
    : `
      <details class="tool-activity-details"${options.detailsExpanded ? " open" : ""}>
        <summary>${disclosureChevronMarkup()}${escapeHtml(cr("activity.details"))}</summary>
        ${detailsBody}
      </details>`;
  // Inline variant: the row button this detail expands under already names the
  // tool, its target, and its status, so repeating that head read as a
  // duplicate. Keep only what the row does not show -- fact tags, warnings,
  // the meta line -- above the detail sections.
  if (options.inlineDetail) {
    const inlineMeta = factTags || classificationWarning || meta
      ? `<div class="tool-activity-inline-meta">${factTags}${classificationWarning}${meta ? `<div class="tool-activity-meta">${escapeHtml(meta)}</div>` : ""}</div>`
      : "";
    return `
    <article class="tool-activity-card tool-activity-inline-card chat-report-card ${escapeAttr(toolActivityStatusClass(status))}" aria-label="${escapeAttr(cardLabel)}" data-chat-report="tool-activity" data-tool-use-id="${escapeAttr(tool.toolUseId)}" data-run-id="${escapeAttr(tool.runId)}">
      ${inlineMeta}
      ${detailsHTML}
    </article>
  `;
  }
  return `
    <article class="tool-activity-card live-tool-output-card chat-flow-item chat-flow-left chat-report-card ${escapeAttr(toolActivityStatusClass(status))}" aria-label="${escapeAttr(cardLabel)}" data-chat-alignment="left" data-chat-report="tool-activity" data-live-tool-output-card="${escapeAttr(tool.toolUseId)}" data-tool-use-id="${escapeAttr(tool.toolUseId)}" data-run-id="${escapeAttr(tool.runId)}">
      <div class="tool-activity-head live-tool-output-head">
        <span class="${escapeAttr(icon.classes)}" aria-hidden="true">${icon.svg}</span>
        <div class="tool-activity-main">
          <div class="tool-activity-title live-tool-output-title">${escapeHtml(friendlyToolName(toolActivityDisplayName(tool)))}</div>
          ${target ? `<div class="tool-activity-target">${escapeHtml(target)}</div>` : ""}
          ${factTags}
          ${classificationWarning}
          ${meta ? `<div class="tool-activity-meta live-tool-output-meta">${escapeHtml(meta)}</div>` : ""}
        </div>
        <span class="tool-activity-status live-tool-output-dot">${escapeHtml(toolActivityStatusLabel(status))}</span>
      </div>
      ${detailsHTML}
    </article>
  `;
}

export function renderToolActivityCardHTML(item = {}, options = {}) {
  const tool = normalizeToolActivity(item);
  if (!isAgentToolActivity(tool)) return renderGenericToolActivityCardHTML(tool, options);
  const resolved = resolveAgentBackgroundTask(item, tool, options || {});
  if (!resolved.ok) return renderGenericToolActivityCardHTML(tool, options);
  return renderAgentTaskActivityCardHTML(tool, resolved.task, options);
}

function toolActivityRecordNeedsExpansion({ item, tool }, options = {}) {
  if (tool.status !== "completed") return true;
  if (!isAgentToolActivity(tool)) return false;
  const resolved = resolveAgentBackgroundTask(item, tool, options);
  if (!resolved.ok) return true;
  const activity = normalizeAgentTaskActivity(item, resolved.task);
  return !activity || activity.status !== "succeeded";
}

function toolActivityGroupExpanded(records, options = {}) {
  // Stay collapsed by default. Auto-expanding live or attention-needed work
  // made every turn open a long tool list; the user can open the summary when
  // they want the trail. Selection still forces open so a clicked tool's
  // detail is visible immediately.
  if (typeof options.expanded === "boolean") return options.expanded;
  if (String(options.selectedToolUseId || "")) return true;
  return false;
}

function toolActivityStackKey(records, options = {}) {
  const explicit = String(options.stackKey || "").trim();
  if (explicit) return explicit;
  const runId = records.map(({ tool }) => String(tool.runId || "").trim()).find(Boolean) || "current";
  return `${options.live ? "live" : "run"}:${runId}`;
}

function formatToolActivityDuration(durationMs) {
  const ms = Number(durationMs);
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 60_000) {
    const tenths = Math.round(ms / 100) / 10;
    if (Number.isInteger(tenths) || tenths >= 10) {
      return `${formatNumber(Math.round(tenths), { maximumFractionDigits: 0 })}s`;
    }
    return `${formatNumber(tenths, { minimumFractionDigits: 1, maximumFractionDigits: 1 })}s`;
  }
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1000);
  return seconds > 0 ? `${minutes}m${seconds}s` : `${minutes}m`;
}

function toolActivityRowMeta(statusLabel, durationMs, running) {
  if (running) return "";
  return [statusLabel, formatToolActivityDuration(durationMs)].filter(Boolean).join(" ");
}

function toolActivityVerb(kind, toolName, running, item) {
  const host = mcpHostToolKind(toolName);
  if (host === "list") {
    return running ? cr("activity.listingMCP") : cr("activity.verbListMCP");
  }
  if (host === "call") {
    const inner = mcpInnerToolName(item) || "";
    if (running) {
      return inner ? cr("activity.callingTool", { tool: inner }) : cr("activity.genericStep");
    }
    return inner || friendlyToolName(toolName);
  }
  if (running) {
    const live = {
      search: "searching",
      files: "searching",
      web: "searching",
      read: "reading",
      edit: "editing",
      write: "writing",
      command: "runningCommand",
    };
    return cr(`activity.${live[kind] || "genericStep"}`);
  }
  const verbs = {
    search: "verbSearch",
    read: "verbRead",
    edit: "verbEdit",
    write: "verbWrite",
    command: "verbCommand",
    files: "verbGlob",
    web: "verbWeb",
    task: "verbTask",
    todo: "verbTodo",
  };
  return verbs[kind] ? cr(`activity.${verbs[kind]}`) : friendlyToolName(toolName);
}

function shortActivityTarget(target, kind) {
  const value = String(target || "").trim();
  if (!value) return "";
  if (kind === "command") return compactToolText(value, 100);
  if (kind === "edit" || kind === "write") {
    const normalized = value.replace(/\\/g, "/");
    const pathPart = normalized.split(" · ")[0] || normalized;
    const base = pathPart.split("/").filter(Boolean).pop() || pathPart;
    return compactToolText(base, 88);
  }
  if (/^https?:\/\//i.test(value)) {
    try {
      const url = new URL(value);
      const path = url.pathname === "/" ? "" : url.pathname;
      const query = url.search ? url.search.slice(0, 28) : "";
      return compactToolText(`${url.host}${path}${query}`, 88);
    } catch {
      return compactToolText(value, 88);
    }
  }
  const normalized = value.replace(/\\/g, "/");
  const pathPart = normalized.split(" · ")[0] || normalized;
  if (!pathPart.includes("/")) return compactToolText(value, 88);
  const parts = pathPart.split("/").filter(Boolean);
  const shortPath = parts.length <= 2 ? parts.join("/") : parts.slice(-2).join("/");
  const rest = normalized.slice(pathPart.length).trim();
  return compactToolText(rest ? `${shortPath} ${rest}` : shortPath, 88);
}

function toolActivityRowPresentation(item, tool, options = {}) {
  const running = toolStatusValue(tool.status) === "running";
  if (isAgentToolActivity(tool)) {
    const resolved = resolveAgentBackgroundTask(item, tool, options);
    if (resolved.ok) {
      const activity = normalizeAgentTaskActivity(item, resolved.task);
      if (activity) {
        const verb = toolActivityVerb("task", tool.toolName, running, tool);
        const target = shortActivityTarget(activity.description, "task");
        return {
          iconKind: "task",
          verb,
          target,
          statusClass: agentTaskStatusClass(activity.status),
          statusLabel: toolActivityRowMeta(agentTaskStatusLabel(activity), activity.durationMs, running),
        };
      }
    }
  }
  const displayName = toolActivityDisplayName(tool);
  const iconKind = toolActivityIconKind(displayName);
  const verb = toolActivityVerb(iconKind, tool.toolName, running, tool);
  const target = shortActivityTarget(toolActivityTarget(tool), iconKind);
  const statusClass = toolActivityStatusClass(tool.status);
  const attention = statusClass === "status-error" || statusClass === "status-warn";
  return {
    iconKind,
    verb,
    target,
    statusClass,
    statusLabel: toolActivityRowMeta(toolActivityStatusLabel(tool.status), tool.durationMs, running),
    diffStat: !running && !attention ? toolActivityRowDiffStat(tool) : null,
  };
}

function renderToolActivityStepStatusHTML(presentation) {
  const stat = presentation.diffStat;
  if (stat && (stat.added > 0 || stat.deleted > 0)) {
    const parts = [];
    if (stat.added > 0) {
      parts.push(`<span class="tool-activity-step-add">+${escapeHtml(formatNumber(stat.added))}</span>`);
    }
    if (stat.deleted > 0) {
      parts.push(`<span class="tool-activity-step-del">-${escapeHtml(formatNumber(stat.deleted))}</span>`);
    }
    return parts.join("");
  }
  return escapeHtml(presentation.statusLabel);
}

function toolActivityRowAriaMeta(presentation) {
  const stat = presentation.diffStat;
  if (stat && (stat.added > 0 || stat.deleted > 0)) {
    return [`${stat.added > 0 ? `+${stat.added}` : ""}`, `${stat.deleted > 0 ? `-${stat.deleted}` : ""}`].filter(Boolean).join(" ");
  }
  return presentation.statusLabel;
}
function renderToolActivityRowHTML(record, options = {}) {
  const { item, tool } = record;
  const presentation = toolActivityRowPresentation(item, tool, options);
  const selected = String(options.selectedToolUseId || "") === tool.toolUseId;
  const label = [presentation.verb, presentation.target, toolActivityRowAriaMeta(presentation)].filter(Boolean).join(" · ");
  const subagentAttrs = isAgentToolActivity(tool)
    ? ` data-subagent-activity-row data-run-id="${escapeAttr(tool.runId)}" data-tool-use-id="${escapeAttr(tool.toolUseId)}"`
    : "";
  // Inline detail: pre-render when selected so static HTML is correct; runtime
  // clicks update this slot directly rather than a shared bottom slot so the
  // detail always appears immediately below the row that was clicked.
  const inlineDetail = selected
    ? renderToolActivityCardHTML(item, { ...options, detailsExpanded: true, inlineDetail: true })
    : "";
  const targetClass = presentation.iconKind === "command"
    ? "tool-activity-step-target is-command"
    : (presentation.iconKind === "edit" || presentation.iconKind === "write" ? "tool-activity-step-target is-file" : "tool-activity-step-target");
  return `
    <li class="tool-activity-step ${escapeAttr(presentation.statusClass)}${selected ? " selected" : ""}"${subagentAttrs}>
      <button class="tool-activity-step-button" type="button" data-tool-activity-select="${escapeAttr(tool.toolUseId)}" data-tool-name="${escapeAttr(tool.toolName)}" data-tool-activity-label="${escapeAttr(label)}" aria-expanded="${selected ? "true" : "false"}" aria-label="${escapeAttr(cr(selected ? "activity.closeDetails" : "activity.openDetails", { tool: label }))}">
        <span class="tool-activity-step-icon tool-activity-icon-${escapeAttr(presentation.iconKind)}" aria-hidden="true">${toolActivityGlyph(presentation.iconKind)}</span>
        <span class="tool-activity-step-copy">
          <span class="tool-activity-step-verb">${escapeHtml(presentation.verb)}</span>
          ${presentation.target ? `<strong class="${targetClass}">${escapeHtml(presentation.target)}</strong>` : ""}
          <span class="tool-activity-step-status">${renderToolActivityStepStatusHTML(presentation)}</span>
        </span>
      </button>
      <div class="tool-activity-inline-detail" data-tool-activity-inline-detail="${escapeAttr(tool.toolUseId)}">${inlineDetail}</div>
    </li>
  `;
}

// Each reasoning step is filed under the tool that ended it, so it renders
// immediately above that tool's row; steps that name no tool (the trailing one,
// and anything whose tool never made it into this page) close the list.
function renderToolActivityRowsHTML(records, reasoningSteps, options = {}) {
  const steps = Array.isArray(reasoningSteps) ? reasoningSteps : [];
  const rendered = new Set();
  const rows = records.map((record) => {
    const toolUseId = String(record.tool.toolUseId || "");
    const leading = steps
      .filter((step) => toolUseId && String(step?.beforeToolUseId || "") === toolUseId)
      .map((step) => {
        rendered.add(step);
        return renderReasoningStepHTML(step);
      })
      .join("");
    return `${leading}${renderToolActivityRowHTML(record, options)}`;
  }).join("");
  const trailing = steps.filter((step) => !rendered.has(step)).map(renderReasoningStepHTML).join("");
  return `${rows}${trailing}`;
}

export function renderToolActivityStackHTML(toolCalls = [], options = {}) {
  const reasoningSteps = Array.isArray(options.reasoningSteps) ? options.reasoningSteps : [];
  const records = (Array.isArray(toolCalls) ? toolCalls : [])
    .map((item) => ({ item, tool: normalizeToolActivity(item) }))
    .filter(({ tool }) => tool.toolUseId || tool.toolName);
  // Reasoning alone is worth a stack: it is what fills the gap before the first
  // tool call, which is exactly when the user is staring at an empty thread.
  if (!records.length && !reasoningSteps.length) return "";
  const expanded = toolActivityGroupExpanded(records, options);
  const stackKey = toolActivityStackKey(records, options);
  const source = options.live ? "live" : "run";
  const runId = String(options.runId || records.map(({ tool }) => tool.runId).find(Boolean) || "");
  const requestedSelection = String(options.selectedToolUseId || "");
  const selectedRecord = records.find(({ tool }) => tool.toolUseId === requestedSelection) || null;
  const selectedToolUseId = selectedRecord?.tool.toolUseId || "";
  const requestedTotal = Number(options.totalCount);
  const totalCount = Number.isFinite(requestedTotal) && requestedTotal > records.length ? Math.floor(requestedTotal) : records.length;
  const omitted = Math.max(0, totalCount - records.length);
  const modeClass = options.compact ? "conversation-tool-activity " : "";
  // data-live-tool-output-stack is how the incremental renderer finds the one
  // tail card it owns, so only the tail may carry it. A per-message stack is
  // also "live" whenever it holds streaming records, and letting it publish the
  // same marker made querySelector return whichever came first in the document:
  // the tail update then rewrote or removed an assistant turn's own stack.
  const tail = options.tail === undefined ? Boolean(options.live) && !options.compact : Boolean(options.tail);
  const reasoningCount = reasoningSteps.length;
  const runningRecords = records.filter(({ tool }) => toolStatusValue(tool.status) === "running");
  const running = runningRecords.length ? runningRecords[runningRecords.length - 1] : null;
  let summaryTitle;
  if (running) {
    const presentation = toolActivityRowPresentation(running.item, running.tool, options);
    summaryTitle = [presentation.verb, presentation.target].filter(Boolean).join(" ");
  } else if (reasoningCount > 0) {
    summaryTitle = totalCount > 0
      ? cr("activity.processTitleWithReasoning", { reasoning: reasoningCount, count: totalCount })
      : cr("activity.processTitleOnlyReasoning", { reasoning: reasoningCount });
  } else {
    summaryTitle = cr("activity.processTitle", { count: totalCount });
  }
  return `
    <section class="${options.live ? "live-tool-output-stack " : ""}${modeClass}tool-activity-stack chat-flow-stack chat-flow-left" data-chat-alignment="left" data-tool-activity-stack data-tool-activity-stack-key="${escapeAttr(stackKey)}" data-tool-activity-source="${escapeAttr(source)}" data-tool-activity-count="${escapeAttr(String(totalCount))}" data-tool-activity-visible-count="${escapeAttr(String(records.length))}" data-tool-activity-default="${expanded ? "expanded" : "collapsed"}"${runId ? ` data-run-id="${escapeAttr(runId)}"` : ""}${tail ? " data-live-tool-output-stack" : ""}${options.compact ? " data-conversation-run-tool-activity" : ""}>
      <details class="tool-activity-group"${expanded ? " open" : ""}>
        <summary class="tool-activity-summary">${disclosureChevronMarkup()}${escapeHtml(summaryTitle)}</summary>
        <ul class="tool-activity-steps">${renderToolActivityRowsHTML(records, reasoningSteps, { ...options, selectedToolUseId })}</ul>
        ${omitted > 0 ? `<div class="tool-activity-more">${escapeHtml(cr("activity.recentOnly", { visible: records.length, count: omitted }))}</div>` : ""}
        <div class="tool-activity-selected-detail" data-tool-activity-selected-detail></div>
      </details>
    </section>
  `;
}
