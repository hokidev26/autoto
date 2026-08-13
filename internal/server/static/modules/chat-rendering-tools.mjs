import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatNumber } from "./formatters.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { chatMessagePresentation } from "./chat-rendering-messages.mjs";

const maxDurationMs = 7 * 24 * 60 * 60 * 1000;
export const maxToolActivityText = 12_000;
const maxToolActivityDiffLines = 800;
export const maxToolActivityCards = 40;
const maxToolActivityEventVersion = 100;
const maxToolFactCommandCount = 1_000;
const maxToolFactLabels = 8;
const maxToolSafetyReason = 600;

const toolDecisionValues = new Set(["allow", "ask", "deny", "allow_once", "allow_session"]);
const toolDecisionSources = new Set([
  "hard_danger_block", "command_review", "danger_reflection", "read_only_cap", "rule", "session_approval", "default_policy",
  "built_in_exec_whitelist", "builtin_exec_whitelist", "permission_mode", "workflow_preferences",
  "policy_unavailable", "workflow_unavailable", "human_approval", "generation_invalidation",
  "policy", "user", "system", "plan_mode",
]);
const toolDecisionScopes = new Set(["tool_call", "once", "session", "rule", "policy", "permission_mode", "workflow_preferences", "run", "plan", "global"]);
const toolFactEffects = new Set([
  "filesystem-delete", "privileged-execution", "disk-write", "filesystem-write", "file-destroy", "disk-format",
  "repository-state-discard", "repository-history-rewrite", "permission-change", "nested-shell", "network-access",
  "shell-execution", "process-kill", "process-spawn", "scheduled-task-change", "network-config", "network-share",
  "service-change", "container-delete", "package-install", "filesystem-mount", "system-shutdown", "backup-destroy",
  "registry-write", "boot-config", "account-change", "audit-clear", "obfuscation", "system-management", "policy-change",
]);
// Hard-blocked labels: catastrophic and effectively irreversible.
const toolFactDangerous = new Set([
  "file-delete", "file-destroy", "file-truncate", "find-delete", "privilege-escalation", "disk-write", "disk-format",
  "disk-partition", "network-pipe-shell", "decoded-pipe-shell", "permission-weaken", "permission-setuid", "git-clean",
  "git-reset-hard", "process-kill-all", "crontab-delete", "firewall-flush", "firewall-change", "container-delete",
  "system-shutdown", "shadow-copy-delete", "registry-delete", "boot-config", "service-delete", "account-change",
  "scheduled-task-change", "audit-clear", "encoded-command", "network-download-exec", "script-host-execution",
  "policy-weaken",
]);
// Approval-required labels: serious but recoverable, and legitimate in normal work.
const toolFactSensitive = new Set([
  "file-delete-scoped", "file-truncate-scoped", "file-overwrite", "permission-change", "process-kill", "service-change",
  "package-install", "git-force-push", "git-history-rewrite", "git-discard-changes", "registry-write", "network-config",
  "network-download", "network-share", "pipe-to-interpreter", "filesystem-mount", "scheduled-task-inspect",
  "account-inspect", "process-spawn", "obfuscation", "bulk-copy", "file-rename", "script-file-execution",
  "system-management", "disk-tooling", "backup-tooling",
]);
const toolFactSubcommands = new Set(["add", "branch", "checkout", "clean", "clone", "commit", "config", "diff", "fetch", "log", "merge", "pull", "push", "reset", "restore", "show", "status", "switch", "tag", "build", "env", "fmt", "generate", "get", "install", "list", "mod", "run", "test", "tool", "vet", "version", "work", "ci", "exec", "update", "lint", "other"]);

export function compactToolText(text, max = 140) {
  const value = String(text || "").replace(/\s+/g, " ").trim();
  if (!value) return "";
  return value.length > max ? `${value.slice(0, max - 1)}…` : value;
}

function shortToolRunId(runId) {
  const value = String(runId || "");
  return value.length <= 12 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`;
}

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

export function firstToolValue(source, ...keys) {
  for (const key of keys) {
    const value = source?.[key];
    if (value !== undefined && value !== null && value !== "") return value;
  }
  return undefined;
}

export function parseToolJSON(value) {
  if (!value || typeof value === "object") return value && typeof value === "object" ? value : {};
  try {
    const parsed = JSON.parse(String(value));
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return { value: String(value) };
  }
}

function safeToolText(value, maximum = maxToolActivityText) {
  let text;
  if (typeof value === "string") text = value;
  else {
    try {
      text = JSON.stringify(value ?? "");
    } catch {
      text = "";
    }
  }
  const normalized = String(text || "");
  return normalized.length > maximum ? `${normalized.slice(0, Math.max(0, maximum - 1))}…` : normalized;
}

export function toolStatusValue(status) {
  const value = String(status || "running").trim().toLowerCase();
  if (["completed", "success", "succeeded", "done"].includes(value)) return "completed";
  if (["error", "failed", "failure"].includes(value)) return "error";
  if (["denied", "pending_approval", "interrupted", "superseded", "cancelled", "canceled"].includes(value)) return value;
  return "running";
}

function isPlainToolRecord(value) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function safeToolEnum(value, allowed) {
  const text = typeof value === "string" ? value.trim().toLowerCase() : "";
  return allowed.has(text) ? text : "";
}

function safeToolFactProgram(value) {
  const text = typeof value === "string" ? value.trim() : "";
  return /^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$/.test(text) ? text : "";
}

function safeToolRuleId(value) {
  const text = typeof value === "string" ? value.trim() : "";
  return /^[A-Za-z0-9][A-Za-z0-9._:-]{0,95}$/.test(text) ? text : "";
}

function safeToolSafetyReason(value) {
  return typeof value === "string" ? compactToolText(value, maxToolSafetyReason) : "";
}

function safeToolFactLabels(value, allowed) {
  if (!Array.isArray(value)) return [];
  const labels = [];
  for (const item of value.slice(0, maxToolFactLabels * 4)) {
    const label = safeToolEnum(item, allowed);
    if (label && !labels.includes(label)) labels.push(label);
    if (labels.length >= maxToolFactLabels) break;
  }
  return labels;
}

function normalizeCommandFacts(value) {
  if (!isPlainToolRecord(value)) return null;
  const commandCount = value.commandCount;
  const parseKnown = typeof value.parseKnown === "boolean" ? value.parseKnown : null;
  const facts = {
    parseKnown,
    program: safeToolFactProgram(value.program),
    subcommand: safeToolEnum(value.subcommand, toolFactSubcommands),
    commandCount: Number.isSafeInteger(commandCount) && commandCount >= 0 && commandCount <= maxToolFactCommandCount ? commandCount : null,
    compound: typeof value.compound === "boolean" ? value.compound : null,
    pipeline: typeof value.pipeline === "boolean" ? value.pipeline : null,
    redirection: typeof value.redirection === "boolean" ? value.redirection : null,
    substitution: typeof value.substitution === "boolean" ? value.substitution : null,
    background: typeof value.background === "boolean" ? value.background : null,
    effects: safeToolFactLabels(value.effects, toolFactEffects),
    dangerous: safeToolFactLabels(value.dangerous, toolFactDangerous),
    sensitive: safeToolFactLabels(value.sensitive, toolFactSensitive),
  };
  return Object.values(facts).some((item) => item !== null && item !== "" && (!Array.isArray(item) || item.length)) ? facts : null;
}

function normalizedDecisionSource(value) {
  const source = safeToolEnum(value, toolDecisionSources);
  return source === "builtin_exec_whitelist" ? "built_in_exec_whitelist" : source;
}

// Line-art glyphs in the same idiom as the composer and workbench icons: a
// 24x24 box, no fill, 1.7px round-joined strokes inheriting currentColor. They
// replace the Unicode characters this used to emit (⌕ ◌ ▤ ± ✎ ›_ •), which
// rendered at whatever weight and baseline each platform's fallback font chose
// and read as punctuation rather than as icons.
const toolActivityGlyphPaths = Object.freeze({
  search: `<circle cx="10.5" cy="10.5" r="6.5"></circle><path d="m15.5 15.5 4.5 4.5"></path>`,
  files: `<path d="M8 3.5h5.5L18 8v9a1.5 1.5 0 0 1-1.5 1.5H8A1.5 1.5 0 0 1 6.5 17V5A1.5 1.5 0 0 1 8 3.5Z"></path><path d="M13 3.5V8h5"></path><path d="M4 7.5v13A1.5 1.5 0 0 0 5.5 22h9"></path>`,
  read: `<path d="M5.5 4.5h7L18.5 10v9.5a1.5 1.5 0 0 1-1.5 1.5H5.5A1.5 1.5 0 0 1 4 19.5v-13A1.5 1.5 0 0 1 5.5 4.5Z"></path><path d="M12 4.5V10h6"></path><path d="M7.5 13.5h7M7.5 17h4.5"></path>`,
  edit: `<path d="M12 20.5h8.5"></path><path d="M16.6 4.1a2.1 2.1 0 0 1 3 3L8.4 18.3 4 19.5l1.2-4.4Z"></path>`,
  write: `<path d="M5.5 3.5h7L18.5 9.5V13"></path><path d="M12 3.5V9.5h6"></path><path d="M4 6.5v13A1.5 1.5 0 0 0 5.5 21h5"></path><path d="M17.5 15.5v6M14.5 18.5h6"></path>`,
  command: `<rect x="3.5" y="4.5" width="17" height="15" rx="2.5"></rect><path d="m8 10 2.5 2.5L8 15"></path><path d="M13 15h3.5"></path>`,
  web: `<circle cx="12" cy="12" r="8.5"></circle><path d="M3.5 12h17"></path><path d="M12 3.5c2.4 2.3 3.6 5.2 3.6 8.5S14.4 18.2 12 20.5c-2.4-2.3-3.6-5.2-3.6-8.5S9.6 5.8 12 3.5Z"></path>`,
  task: `<circle cx="6.5" cy="6.5" r="2.5"></circle><circle cx="17.5" cy="6.5" r="2.5"></circle><circle cx="12" cy="18" r="2.5"></circle><path d="M6.5 9v2a2 2 0 0 0 2 2h7a2 2 0 0 0 2-2V9"></path><path d="M12 13v2.5"></path>`,
  todo: `<path d="M4 6.5 6 8.5 9.5 5"></path><path d="M4 17 6 19l3.5-3.5"></path><path d="M13 7h7M13 17.5h7"></path>`,
  thinking: `<path d="M12 3.5a5.5 5.5 0 0 0-3.4 9.8V16a1.5 1.5 0 0 0 1.5 1.5h3.8A1.5 1.5 0 0 0 15.4 16v-2.7A5.5 5.5 0 0 0 12 3.5Z"></path><path d="M10 20.5h4"></path><path d="M12 9v4"></path>`,
  // Deliberately the plainest mark in the set: it is the fallback for tools we
  // do not recognise (MCP servers, plugins), so it upgrades the old "•" bullet
  // rather than implying a capability the tool may not have.
  generic: `<circle cx="12" cy="12" r="8.5"></circle><circle cx="12" cy="12" r="2.3"></circle>`,
  // Subagent status markers. These used to be "↗", "✓" and "!" injected by a
  // ::before in workspace-tasks.css, which cannot share a box with an <svg>.
  dispatch: `<path d="M8 16 16 8"></path><path d="M9.5 8H16v6.5"></path>`,
  done: `<path d="m5.5 12.5 4.5 4.5 8.5-9.5"></path>`,
  alert: `<path d="M12 7v6.5"></path><circle cx="12" cy="17" r="1.1"></circle>`,
});

function toolActivityGlyph(kind) {
  const paths = toolActivityGlyphPaths[kind] || toolActivityGlyphPaths.generic;
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${paths}</svg>`;
}

// The kind doubles as a CSS hook (.tool-activity-icon-<kind>), so the palette in
// workspace-tasks.css can tint reads, writes and commands apart at a glance.
function toolActivityIconKind(toolName) {
  const name = String(toolName || "").toLowerCase();
  if (name.includes("grep") || name.includes("search")) return name.includes("web") ? "web" : "search";
  if (name.includes("glob")) return "files";
  if (name.includes("fetch") || name.includes("browser") || name.includes("navigate") || name.includes("http")) return "web";
  if (name.includes("todo")) return "todo";
  if (name.includes("task") || name.includes("agent")) return "task";
  if (name.includes("notebook") || name.includes("edit")) return "edit";
  if (name.includes("write") || name.includes("create")) return "write";
  if (name.includes("read") || name.includes("view") || name.includes("cat")) return "read";
  if (name.includes("bash") || name.includes("shell") || name.includes("terminal") || name.includes("powershell") || name.includes("exec")) return "command";
  return "generic";
}

// Maps a raw tool name to a human-readable display title. Keeps the original
// as fallback so unknown tools still surface their actual name.
function friendlyToolName(toolName) {
  const name = String(toolName || "").toLowerCase().trim();
  if (name === "ls" || name === "listdirectory" || name === "list_directory") return cr("toolNames.listDirectory");
  if (name === "pwd") return cr("toolNames.currentDirectory");
  if (name === "cat") return cr("toolNames.readFile");
  if (name === "mkdir") return cr("toolNames.createDirectory");
  if (name === "cp") return cr("toolNames.copyFile");
  if (name === "mv") return cr("toolNames.moveFile");
  if (name === "rm") return cr("toolNames.deleteFile");
  if (name === "touch") return cr("toolNames.createFile");
  if (name === "find") return cr("toolNames.findFiles");
  if (name === "which") return cr("toolNames.findCommand");
  if (name === "echo") return cr("toolNames.echoText");
  if (name === "curl" || name === "wget") return cr("toolNames.networkRequest");
  return toolName;
}

function toolActivityIconHTML(toolName, extraClass = "") {
  const kind = toolActivityIconKind(toolName);
  const classes = `${extraClass} tool-activity-icon-${kind}`.trim();
  return { kind, classes, svg: toolActivityGlyph(kind) };
}

export function normalizeToolActivity(call = {}, fallback = {}) {
  const eventData = call?.data && typeof call.data === "object" ? call.data : {};
  const source = { ...fallback, ...call, ...eventData };
  const inputValue = firstToolValue(source, "inputJson", "input_json", "input");
  const outputValue = firstToolValue(source, "outputJson", "output_json", "result");
  const toolUseId = firstToolValue(source, "toolUseId", "tool_use_id", "id");
  const durationMs = Number(firstToolValue(source, "durationMs", "duration_ms") || 0);
  const eventVersionValue = firstToolValue(source, "eventVersion", "event_version");
  const shellSafeValue = firstToolValue(source, "shellSafe", "shell_safe");
  const permissionDecidedBy = normalizedDecisionSource(firstToolValue(source, "permissionDecidedBy", "permission_decided_by"));
  const decisionSource = normalizedDecisionSource(firstToolValue(source, "decisionSource", "decision_source")) || permissionDecidedBy;
  return {
    agentId: firstToolValue(source, "agentId", "agent_id") || "",
    runId: firstToolValue(source, "runId", "run_id") || "",
    // The assistant message that emitted this call. It is what lets a run's
    // activity be filed under the narration that caused it instead of being
    // flattened into one stack for the whole run.
    messageId: String(firstToolValue(source, "messageId", "message_id") || ""),
    toolUseId: toolUseId ? String(toolUseId) : "",
    toolName: String(firstToolValue(source, "toolName", "tool_name", "name") || cr("defaults.tool")),
    risk: String(firstToolValue(source, "risk") || ""),
    status: toolStatusValue(firstToolValue(source, "status", "state")),
    createdAt: firstToolValue(source, "createdAt", "created_at", "startedAt", "started_at") || "",
    durationMs: Number.isFinite(durationMs) && durationMs > 0 ? Math.min(durationMs, maxDurationMs) : 0,
    executionDeviceId: String(firstToolValue(source, "executionDeviceId", "execution_device_id", "deviceId", "device_id") || ""),
    inputJson: parseToolJSON(inputValue),
    outputJson: parseToolJSON(outputValue),
    resultPreview: firstToolValue(source, "resultPreview", "result_preview", "outputPreview", "output_preview") || "",
    output: firstToolValue(source, "output") || "",
    // Argument text streamed while the model was still composing the call
    // (Write content, Edit replacement). Carried on the record so later
    // lifecycle events do not wipe what was already shown.
    inputPreview: String(firstToolValue(source, "inputPreview", "input_preview") || ""),
    inputPreviewField: String(firstToolValue(source, "inputPreviewField", "input_preview_field") || ""),
    errorMessage: firstToolValue(source, "errorMessage", "error_message", "error") || "",
    truncated: Boolean(firstToolValue(source, "truncated", "inputTruncated", "input_truncated", "outputTruncated", "output_truncated", "resultTruncated", "result_truncated", "diffTruncated", "diff_truncated", "inputPreviewTruncated", "input_preview_truncated")),
    eventVersion: Number.isSafeInteger(eventVersionValue) && eventVersionValue > 0 && eventVersionValue <= maxToolActivityEventVersion ? eventVersionValue : null,
    decision: safeToolEnum(firstToolValue(source, "decision", "permissionDecision", "permission_decision"), toolDecisionValues),
    decisionSource,
    ruleId: safeToolRuleId(firstToolValue(source, "ruleId", "rule_id")),
    decisionScope: safeToolEnum(firstToolValue(source, "decisionScope", "decision_scope"), toolDecisionScopes),
    commandFacts: normalizeCommandFacts(firstToolValue(source, "commandFacts", "command_facts")),
    shellSafe: typeof shellSafeValue === "boolean" ? shellSafeValue : null,
    permissionDecidedBy,
    permissionDecisionReason: safeToolSafetyReason(firstToolValue(source, "permissionDecisionReason", "permission_decision_reason", "reason")),
  };
}

export function findToolActivityByIdentity(collections, runId, toolUseId) {
  const expectedRunId = String(runId || "").trim();
  const expectedToolUseId = String(toolUseId || "").trim();
  if (!expectedRunId || !expectedToolUseId) return null;
  for (const collection of Array.isArray(collections) ? collections : [collections]) {
    const records = Array.isArray(collection)
      ? collection
      : (collection && typeof collection === "object" ? Object.values(collection) : []);
    for (const record of records) {
      const tool = normalizeToolActivity(record);
      if (tool.runId === expectedRunId && tool.toolUseId === expectedToolUseId) return record;
    }
  }
  return null;
}

function toolActivityInputText(item) {
  const input = item.inputJson && typeof item.inputJson === "object" ? item.inputJson : {};
  // An empty object is no input; serialising it to "{}" made the card show two
  // braces where the "no input" empty state should speak instead.
  if (!Object.keys(input).length) return "";
  try {
    return safeToolText(JSON.stringify(input, null, 2));
  } catch {
    return safeToolText(input);
  }
}

function toolActivityOutputText(item) {
  if (item.errorMessage) return safeToolText(item.errorMessage);
  if (item.output) return safeToolText(item.output);
  if (item.resultPreview) return safeToolText(item.resultPreview);
  const raw = item.outputJson && typeof item.outputJson === "object" ? item.outputJson : {};
  const result = raw.result && typeof raw.result === "object" ? raw.result : raw;
  const output = firstToolValue(result, "output", "text", "message");
  if (output !== undefined) return safeToolText(output);
  const visible = Object.fromEntries(Object.entries(result).filter(([key]) => !["meta", "diff"].includes(key)));
  if (Object.keys(visible).length) {
    try {
      return safeToolText(JSON.stringify(visible, null, 2));
    } catch {
      return safeToolText(visible);
    }
  }
  return "";
}

// Identity for de-duplicating one tool call seen through several lists (active
// run summary, retained history, live stream). toolUseId is authoritative when
// present. Records that never got one can still collide -- the same persisted
// row reaching the view twice keeps its server-side createdAt in both copies --
// so those fall back to a fingerprint of the fields such copies share. A live
// and a persisted view of one call do not share createdAt, but they always
// carry a toolUseId, so the fingerprint never has to bridge that pair.
export function toolActivityDedupeKey(record) {
  const tool = normalizeToolActivity(record);
  if (tool.toolUseId) return tool.toolUseId;
  const createdAt = String(tool.createdAt || "");
  if (!createdAt) return "";
  return `fp:${tool.runId}|${tool.toolName}|${createdAt}`;
}

// The first copy of a call wins its slot, but a live duplicate may hold
// streamed output the persisted row has not hydrated yet. Borrow the missing
// output so the expanded card is never emptier than what the user already saw
// streaming.
export function mergeDuplicateToolActivity(kept, duplicate) {
  if (toolActivityOutputText(normalizeToolActivity(kept))) return kept;
  const dupTool = normalizeToolActivity(duplicate);
  const output = dupTool.output || dupTool.resultPreview;
  if (!output) return kept;
  return { ...kept, output };
}

function toolActivityTarget(item) {
  const input = item.inputJson && typeof item.inputJson === "object" ? item.inputJson : {};
  const command = firstToolValue(input, "command");
  const filePath = firstToolValue(input, "file_path", "filePath", "path", "cwd");
  const pattern = firstToolValue(input, "pattern", "query");
  const pages = firstToolValue(input, "pages");
  const offset = firstToolValue(input, "offset");
  const limit = firstToolValue(input, "limit");
  const value = firstToolValue(input, "value", "url", "ref_id");
  const parts = [];
  if (command) parts.push(compactToolText(command, 180));
  else if (filePath) parts.push(compactToolText(filePath, 180));
  else if (value) parts.push(compactToolText(value, 180));
  if (pattern) parts.push(`pattern: ${compactToolText(pattern, 100)}`);
  if (pages !== undefined) parts.push(`pages: ${compactToolText(pages, 60)}`);
  if (offset !== undefined || limit !== undefined) parts.push([offset !== undefined ? `offset ${offset}` : "", limit !== undefined ? `limit ${limit}` : ""].filter(Boolean).join(" / "));
  return parts.join(" · ");
}

function toolActivityDeviceLabel(deviceId) {
  if (!deviceId) return "";
  return /^(local|localhost|local-service)$/i.test(String(deviceId)) ? cr("activity.localService") : String(deviceId);
}

function isBashToolActivity(item) {
  return /(?:^|\b)(bash|shell|terminal)(?:\b|$)/i.test(String(item?.toolName || ""));
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
  const parts = [
    decision ? cr("activity.decisionLabel", { decision }) : "",
    source ? cr("activity.decisionSourceLabel", { source }) : "",
    scope ? cr("activity.decisionScopeLabel", { scope }) : "",
    item.ruleId ? cr("activity.ruleId", { ruleId: item.ruleId }) : "",
    item.permissionDecisionReason ? cr("activity.decisionReason", { reason: item.permissionDecisionReason }) : "",
  ].filter(Boolean);
  if (!parts.length) return "";
  const hint = toolDecisionSourceHint(item.decisionSource);
  return `<div class="tool-activity-safety"><div class="tool-activity-meta">${escapeHtml(cr("activity.safetyDecision"))}</div><div class="tool-activity-safety-summary">${escapeHtml(parts.join(" · "))}</div>${hint ? `<div class="tool-activity-safety-hint">${escapeHtml(hint)}</div>` : ""}</div>`;
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

export function isAgentToolActivity(item = {}) {
  const source = item?.data && typeof item.data === "object" ? { ...item, ...item.data } : item;
  const name = firstToolValue(source, "toolName", "tool_name", "name");
  return String(name || "").trim().toLowerCase() === "agent";
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
        <summary>${escapeHtml(cr("subagent.auditDetails"))}</summary>
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

function renderGenericToolActivityCardHTML(item = {}, options = {}) {
  const tool = normalizeToolActivity(item);
  const status = tool.status;
  const target = toolActivityTarget(tool);
  const input = toolActivityInputText(tool);
  const output = toolActivityOutputText(tool);
  const device = compactToolText(toolActivityDeviceLabel(tool.executionDeviceId), 80);
  const diff = String(tool.toolName).toLowerCase().includes("edit") ? renderToolDiffHTML(tool) : "";
  const factTags = renderToolActivityFactTags(tool);
  const classificationWarning = renderToolActivityClassificationWarning(tool);
  const safetySummary = renderToolActivitySafetySummary(tool);
  const meta = [
    compactToolText(tool.risk, 40),
    ...toolActivitySafetyMetaParts(tool),
    tool.durationMs > 0 ? `${formatNumber(tool.durationMs)} ms` : "",
    device,
    tool.runId ? shortToolRunId(tool.runId) : "",
  ].filter(Boolean).join(" · ");
  const cardLabel = [tool.toolName, target, toolActivityStatusLabel(status)].filter(Boolean).join(" · ");
  const icon = toolActivityIconHTML(tool.toolName, "tool-activity-icon");
  const detailsHTML = `
      <details class="tool-activity-details"${options.detailsExpanded ? " open" : ""}>
        <summary>${escapeHtml(cr("activity.details"))}</summary>
        ${safetySummary}
        <div class="tool-activity-block">
          <div class="tool-activity-block-bar">
            <div class="tool-activity-meta">${escapeHtml(cr("activity.input"))}</div>
            ${input ? toolActivityBlockControlsHTML(input) : ""}
          </div>
          <pre class="tool-activity-command">${escapeHtml(input || cr("activity.noInput"))}</pre>
        </div>
        ${streamedInputBlockHTML(tool)}
        ${diff ? `<div class="tool-activity-meta">${escapeHtml(cr("activity.diff"))}</div>${diff}` : ""}
        <div class="tool-activity-block">
          <div class="tool-activity-block-bar">
            <div class="tool-activity-meta">${escapeHtml(cr("activity.output"))}</div>
            ${output ? toolActivityBlockControlsHTML(output) : ""}
          </div>
          ${output ? `<pre class="tool-activity-output live-tool-output-body">${escapeHtml(output)}</pre>` : `<div class="tool-activity-empty">${escapeHtml(cr("activity.noOutput"))}</div>`}
        </div>
        ${tool.truncated ? `<div class="tool-activity-truncated">${escapeHtml(cr("activity.truncated"))}</div>` : ""}
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
          <div class="tool-activity-title live-tool-output-title">${escapeHtml(friendlyToolName(tool.toolName))}</div>
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

function toolActivityRowPresentation(item, tool, options = {}) {
  if (isAgentToolActivity(tool)) {
    const resolved = resolveAgentBackgroundTask(item, tool, options);
    if (resolved.ok) {
      const activity = normalizeAgentTaskActivity(item, resolved.task);
      if (activity) {
        return {
          iconKind: "task",
          title: cr("subagent.title"),
          target: activity.description,
          statusClass: agentTaskStatusClass(activity.status),
          statusLabel: agentTaskStatusLabel(activity),
        };
      }
    }
  }
  return {
    iconKind: toolActivityIconKind(tool.toolName),
    title: friendlyToolName(tool.toolName),
    target: toolActivityTarget(tool),
    statusClass: toolActivityStatusClass(tool.status),
    statusLabel: toolActivityStatusLabel(tool.status),
  };
}

function renderToolActivityRowHTML(record, options = {}) {
  const { item, tool } = record;
  const presentation = toolActivityRowPresentation(item, tool, options);
  const selected = String(options.selectedToolUseId || "") === tool.toolUseId;
  const label = [presentation.title, presentation.target, presentation.statusLabel].filter(Boolean).join(" · ");
  const subagentAttrs = isAgentToolActivity(tool)
    ? ` data-subagent-activity-row data-run-id="${escapeAttr(tool.runId)}" data-tool-use-id="${escapeAttr(tool.toolUseId)}"`
    : "";
  // Inline detail: pre-render when selected so static HTML is correct; runtime
  // clicks update this slot directly rather than a shared bottom slot so the
  // detail always appears immediately below the row that was clicked.
  const inlineDetail = selected
    ? renderToolActivityCardHTML(item, { ...options, detailsExpanded: true, inlineDetail: true })
    : "";
  return `
    <li class="tool-activity-step ${escapeAttr(presentation.statusClass)}${selected ? " selected" : ""}"${subagentAttrs}>
      <button class="tool-activity-step-button" type="button" data-tool-activity-select="${escapeAttr(tool.toolUseId)}" data-tool-activity-label="${escapeAttr(label)}" aria-expanded="${selected ? "true" : "false"}" aria-label="${escapeAttr(cr(selected ? "activity.closeDetails" : "activity.openDetails", { tool: label }))}">
        <span class="tool-activity-step-icon tool-activity-icon-${escapeAttr(presentation.iconKind)}" aria-hidden="true">${toolActivityGlyph(presentation.iconKind)}</span>
        <span class="tool-activity-step-copy">
          <strong>${escapeHtml(presentation.title)}</strong>
          ${presentation.target ? `<span>${escapeHtml(compactToolText(presentation.target, 220))}</span>` : ""}
        </span>
        <span class="tool-activity-step-status">${escapeHtml(presentation.statusLabel)}</span>
      </button>
      <div class="tool-activity-inline-detail" data-tool-activity-inline-detail="${escapeAttr(tool.toolUseId)}">${inlineDetail}</div>
    </li>
  `;
}

export function nextToolActivitySelection(currentToolUseId, requestedToolUseId) {
  const current = String(currentToolUseId || "");
  const requested = String(requestedToolUseId || "");
  return requested && requested !== current ? requested : "";
}

export const maxLiveReasoningCharacters = 20000;
export const maxLiveReasoningSteps = 60;

// The first sentence carries the intent ("Planning the requirement inference");
// the rest is supporting detail that belongs in the expanded body.
export function reasoningStepTitle(text) {
  const value = String(text || "").replace(/\s+/g, " ").trim();
  if (!value) return "";
  const boundary = value.search(/[。．.!！?？\n]/);
  const head = boundary > 0 ? value.slice(0, boundary) : value;
  return compactToolText(head, 120);
}

function renderReasoningStepHTML(step) {
  const title = reasoningStepTitle(step?.text);
  if (!title) return "";
  const body = String(step?.text || "").trim();
  const detail = body.length > title.length ? body : "";
  return `
    <li class="tool-activity-step tool-activity-reasoning-step${step?.open ? " is-open" : ""}" data-reasoning-step="${escapeAttr(String(step?.id || ""))}">
      <details class="tool-activity-reasoning">
        <summary class="tool-activity-step-button">
          <span class="tool-activity-step-icon tool-activity-icon-thinking" aria-hidden="true">${toolActivityGlyph("thinking")}</span>
          <span class="tool-activity-step-copy"><strong>${escapeHtml(title)}</strong></span>
        </summary>
        ${detail ? `<div class="tool-activity-reasoning-body">${escapeHtml(detail)}</div>` : ""}
      </details>
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
  // Prefer the combined title when reasoning steps are present so the summary
  // reflects both the thinking trail and the tool calls under it. A turn that
  // only thought needs its own wording: the combined title would read
  // "3 steps of reasoning · 0 tool calls".
  const summaryTitle = reasoningCount > 0
    ? (totalCount > 0
      ? cr("activity.processTitleWithReasoning", { reasoning: reasoningCount, count: totalCount })
      : cr("activity.processTitleOnlyReasoning", { reasoning: reasoningCount }))
    : cr("activity.processTitle", { count: totalCount });
  return `
    <section class="${options.live ? "live-tool-output-stack " : ""}${modeClass}tool-activity-stack chat-flow-stack chat-flow-left" data-chat-alignment="left" data-tool-activity-stack data-tool-activity-stack-key="${escapeAttr(stackKey)}" data-tool-activity-source="${escapeAttr(source)}" data-tool-activity-count="${escapeAttr(String(totalCount))}" data-tool-activity-visible-count="${escapeAttr(String(records.length))}" data-tool-activity-default="${expanded ? "expanded" : "collapsed"}"${runId ? ` data-run-id="${escapeAttr(runId)}"` : ""}${tail ? " data-live-tool-output-stack" : ""}${options.compact ? " data-conversation-run-tool-activity" : ""}>
      <details class="tool-activity-group"${expanded ? " open" : ""}>
        <summary class="tool-activity-summary">${escapeHtml(summaryTitle)}</summary>
        <ul class="tool-activity-steps">${renderToolActivityRowsHTML(records, reasoningSteps, { ...options, selectedToolUseId })}</ul>
        ${omitted > 0 ? `<div class="tool-activity-more">${escapeHtml(cr("activity.recentOnly", { visible: records.length, count: omitted }))}</div>` : ""}
        <div class="tool-activity-selected-detail" data-tool-activity-selected-detail></div>
      </details>
    </section>
  `;
}

// A run's tool calls belong to the assistant turn that emitted them, not to the
// run as a whole. Grouping by that owner is what lets the transcript keep its
// real order: narration, the tools it caused, the next narration. Calls whose
// owner is unknown (older rows without messageId, or an owner that never became
// a visible message) stay together in `unowned` and keep the previous
// behaviour of hanging off the run itself.
export function groupToolActivityByMessage(toolCalls = [], knownMessageIds = null) {
  const byMessage = new Map();
  const unowned = [];
  const known = knownMessageIds instanceof Set ? knownMessageIds : null;
  for (const call of Array.isArray(toolCalls) ? toolCalls : []) {
    const messageId = normalizeToolActivity(call).messageId;
    if (!messageId || (known && !known.has(messageId))) {
      unowned.push(call);
      continue;
    }
    if (!byMessage.has(messageId)) byMessage.set(messageId, []);
    byMessage.get(messageId).push(call);
  }
  return { byMessage, unowned };
}

// Persisted reasoning is one block of text per assistant turn, so it becomes a
// single step filed before that turn's first tool call -- the same slot the live
// path uses, which is what keeps thinking on the activity surface after a run
// ends instead of moving it into the message bubble.
export function persistedReasoningSteps(message = {}, toolCalls = []) {
  // Only the assistant reasons. A user row carrying this field is either a
  // client bug or hostile input, and must never grow a thinking step.
  if (chatMessagePresentation(message).normalizedRole !== "assistant") return [];
  const text = String(message?.reasoningText || message?.reasoning_text || "").trim();
  if (!text) return [];
  const firstToolUseId = (Array.isArray(toolCalls) ? toolCalls : [])
    .map((call) => normalizeToolActivity(call).toolUseId)
    .find(Boolean) || "";
  return [{
    id: `reasoning:${String(message?.id || "")}`,
    runId: String(message?.runId || message?.run_id || ""),
    text,
    beforeToolUseId: firstToolUseId,
  }];
}
