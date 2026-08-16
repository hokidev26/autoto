import { t as cr } from "./messages-chat-rendering-extra.mjs";

export const maxDurationMs = 7 * 24 * 60 * 60 * 1000;
export const maxToolActivityText = 12_000;
export const maxToolActivityDiffLines = 800;
export const maxToolActivityCards = 40;
const maxToolActivityEventVersion = 100;
const maxToolFactCommandCount = 1_000;
export const maxToolFactLabels = 8;
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

export function safeToolText(value, maximum = maxToolActivityText) {
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

const mcpHostToolNames = new Set(["mcpcalltool", "mcplisttools"]);

export function mcpHostToolKind(toolName) {
  const name = String(toolName || "").trim().toLowerCase();
  if (name === "mcpcalltool") return "call";
  if (name === "mcplisttools") return "list";
  return "";
}

function mcpInputRecord(item) {
  return item?.inputJson && typeof item.inputJson === "object" ? item.inputJson : {};
}

export function mcpCallArguments(item) {
  if (mcpHostToolKind(item?.toolName) !== "call") return {};
  const value = firstToolValue(mcpInputRecord(item), "arguments", "args");
  const parsed = parseToolJSON(value);
  return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
}

export function mcpInnerToolName(item) {
  if (mcpHostToolKind(item?.toolName) !== "call") return "";
  const inner = String(firstToolValue(mcpInputRecord(item), "toolName", "tool_name") || "").trim();
  if (!inner || mcpHostToolNames.has(inner.toLowerCase())) return "";
  return inner;
}

export function toolActivityDisplayName(item) {
  return mcpInnerToolName(item) || String(item?.toolName || "");
}

export function toolActivityInputText(item) {
  const input = mcpInputRecord(item);
  const visible = mcpHostToolKind(item?.toolName) === "call" ? mcpCallArguments(item) : input;
  // An empty object is no input; serialising it to "{}" made the card show two
  // braces where the "no input" empty state should speak instead. MCPCallTool's
  // host wrapper (serverId / toolName) is not the argument the user asked about.
  if (!Object.keys(visible).length) return "";
  try {
    return safeToolText(JSON.stringify(visible, null, 2));
  } catch {
    return safeToolText(visible);
  }
}

export function toolActivityOutputText(item) {
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

export function toolActivityTarget(item) {
  const input = mcpInputRecord(item);
  const nested = mcpCallArguments(item);
  const lookup = Object.keys(nested).length ? nested : input;
  const command = firstToolValue(lookup, "command");
  const filePath = firstToolValue(lookup, "file_path", "filePath", "path", "cwd");
  const pattern = firstToolValue(lookup, "pattern", "query");
  const pages = firstToolValue(lookup, "pages");
  const offset = firstToolValue(lookup, "offset");
  const limit = firstToolValue(lookup, "limit");
  const value = firstToolValue(lookup, "value", "url", "ref_id", "uid", "selector", "expression", "text");
  const parts = [];
  if (command) parts.push(compactToolText(command, 180));
  else if (filePath) parts.push(compactToolText(filePath, 180));
  else if (value) parts.push(compactToolText(value, 180));
  if (pattern) parts.push(`pattern: ${compactToolText(pattern, 100)}`);
  if (pages !== undefined) parts.push(`pages: ${compactToolText(pages, 60)}`);
  if (offset !== undefined || limit !== undefined) parts.push([offset !== undefined ? `offset ${offset}` : "", limit !== undefined ? `limit ${limit}` : ""].filter(Boolean).join(" / "));
  return parts.join(" · ");
}

export function isBashToolActivity(item) {
  return /(?:^|\b)(bash|shell|terminal)(?:\b|$)/i.test(String(item?.toolName || ""));
}

export function isAgentToolActivity(item = {}) {
  const source = item?.data && typeof item.data === "object" ? { ...item, ...item.data } : item;
  const name = firstToolValue(source, "toolName", "tool_name", "name");
  return String(name || "").trim().toLowerCase() === "agent";
}

export function nextToolActivitySelection(currentToolUseId, requestedToolUseId) {
  const current = String(currentToolUseId || "");
  const requested = String(requestedToolUseId || "");
  return requested && requested !== current ? requested : "";
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
