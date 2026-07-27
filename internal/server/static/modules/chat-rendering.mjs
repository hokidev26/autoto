import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatBytes, formatNumber, formatTimestamp } from "./formatters.mjs?v=message-thread-1";
import { t } from "./i18n.mjs";
import { api } from "./runtime.mjs";
import { visibleMessageText } from "./skills-commands.mjs";
import { normalizeAvatarDataUrl } from "./profile-avatar.mjs?v=profile-avatar-1";
import { t as cr } from "./messages-chat-rendering-extra.mjs?v=plan-mode-1-i18n-shared-1-subagent-cards-1-provider-errors-1-tool-activity-lazy-1";
import {
  bindProtectedDownloads,
  hydrateProtectedImages,
  loadProtectedImageURL,
  protectedDownloadAttribute,
  protectedImageAttribute,
} from "./protected-images.mjs?v=protected-images-1";
import { openImageLightbox } from "./image-lightbox.mjs?v=protected-images-1";

const userMessageRoles = new Set(["user", "human"]);
const maxTokenCount = 1_000_000_000;
const maxDurationMs = 7 * 24 * 60 * 60 * 1000;
const maxTokensPerSecond = 1_000_000;

function normalizePositiveMetric(value, maximum, { integer = false, allowZero = false } = {}) {
  if (value === null || value === undefined || value === "") return null;
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0 || (!allowZero && number === 0) || number > maximum) return null;
  return integer ? Math.round(number) : number;
}

export function normalizeTurnUsage(turnUsage = {}) {
  const source = turnUsage && typeof turnUsage === "object" ? turnUsage : {};
  return {
    inputTokens: normalizePositiveMetric(source.inputTokens, maxTokenCount, { integer: true }),
    outputTokens: normalizePositiveMetric(source.outputTokens, maxTokenCount, { integer: true }),
    cachedInputTokens: normalizePositiveMetric(source.cachedInputTokens, maxTokenCount, { integer: true }),
    reasoningTokens: normalizePositiveMetric(source.reasoningTokens, maxTokenCount, { integer: true }),
    ttftMs: normalizePositiveMetric(source.ttftMs, maxDurationMs, { allowZero: true }),
    durationMs: normalizePositiveMetric(source.durationMs, maxDurationMs, { allowZero: true }),
    tokensPerSecond: normalizePositiveMetric(source.tokensPerSecond, maxTokensPerSecond),
    estimated: source.estimated === true,
  };
}

export function formatTurnUsagePerformance(turnUsage = {}, options = {}) {
  const usage = normalizeTurnUsage(turnUsage);
  const locale = options.locale;
  const parts = [];
  if (usage.tokensPerSecond !== null) {
    const value = formatNumber(usage.tokensPerSecond, { locale, minimumFractionDigits: 1, maximumFractionDigits: 1 });
    parts.push(`${usage.estimated ? "≈" : ""}${t("chat.throughput", {}, locale)} ${value} tok/s`);
  }
  if (usage.ttftMs !== null) {
    const seconds = formatNumber(usage.ttftMs / 1000, { locale, minimumFractionDigits: 1, maximumFractionDigits: 1 });
    parts.push(`${t("chat.ttft", {}, locale)} ${seconds}s`);
  }
  return parts.join(" | ");
}

export function chatMessagePresentation(message = {}) {
  const sourceRole = String(message.role || "message").trim() || "message";
  const parentToolUseId = String(message.parentToolUseId || message.parentTool_use_id || message.parentToolID || "").trim();
  const isToolResult = Boolean(parentToolUseId);
  const role = isToolResult ? "tool" : sourceRole;
  const normalizedRole = role.toLowerCase();
  const isUserMessage = !isToolResult && userMessageRoles.has(normalizedRole);
  const alignment = "left";
  const timestampValue = [message.createdAt, message.created_at, message.timestamp, message.sentAt, message.updatedAt]
    .map((value) => String(value || "").trim())
    .find((value) => value && !Number.isNaN(Date.parse(value))) || "";
  return {
    role,
    normalizedRole,
    roleClass: isUserMessage ? "user" : "assistant",
    alignment,
    timestampValue,
  };
}

export function normalizeMessageProfileIdentity(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const displayName = String(source.displayName || "").trim().slice(0, 80)
    || String(source.workspaceLabel || "").trim().slice(0, 80)
    || "Autoto User";
  const avatarInitials = String(source.avatarInitials || "AT").trim().slice(0, 4).toUpperCase() || "AT";
  const avatarDataUrl = normalizeAvatarDataUrl(source.avatarDataUrl);
  return { displayName, avatarInitials, avatarDataUrl };
}

function boundedImageText(value, maximum = 240) {
  return String(value ?? "").trim().slice(0, maximum);
}

function positiveImageDimension(value) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number > 0 && number <= 100_000 ? number : 0;
}

function imageOutputIndex(value) {
  const number = Number(value);
  return Number.isSafeInteger(number) && number >= 0 && number <= 10_000 ? number : null;
}

export function messageContentBlocks(message = {}) {
  let content = message?.contentJson ?? message?.content_json;
  if (typeof content === "string") {
    try { content = JSON.parse(content); } catch { return []; }
  }
  if (Array.isArray(content)) return content.filter((block) => block && typeof block === "object" && !Array.isArray(block));
  if (!content || typeof content !== "object") return [];
  for (const key of ["blocks", "content", "items"]) {
    if (Array.isArray(content[key])) return content[key].filter((block) => block && typeof block === "object" && !Array.isArray(block));
  }
  return content.type ? [content] : [];
}

export function normalizeGeneratedImageBlocks(message = {}) {
  return messageContentBlocks(message)
    .map((block, blockIndex) => ({ block, blockIndex }))
    .filter(({ block }) => String(block.type || "").trim().toLowerCase() === "image_generation")
    .map(({ block, blockIndex }) => ({
      type: "image_generation",
      assetId: boundedImageText(block.assetId ?? block.asset_id),
      generationId: boundedImageText(block.generationId ?? block.generation_id),
      status: boundedImageText(block.status, 64).toLowerCase(),
      mimeType: boundedImageText(block.mimeType ?? block.mime_type, 120),
      filename: boundedImageText(block.filename, 240),
      width: positiveImageDimension(block.width),
      height: positiveImageDimension(block.height),
      revisedPrompt: boundedImageText(block.revisedPrompt ?? block.revised_prompt, 4_000),
      outputIndex: imageOutputIndex(block.outputIndex ?? block.output_index),
      blockIndex,
    }))
    .sort((left, right) => (left.outputIndex ?? left.blockIndex) - (right.outputIndex ?? right.blockIndex) || left.blockIndex - right.blockIndex);
}

function contentBlockType(block = {}) {
  return String(block.type || "").trim().toLowerCase();
}

function contentBlockText(block = {}) {
  return String(block.text ?? block.content ?? "").trim();
}

function stripLegacyAssistantToolRequests(value) {
  return String(value || "")
    .split(/\r?\n/)
    .filter((line) => !/^\s*\[?Tool requested:\s+.+\s+\([^)]+\)\]?\s*$/i.test(line))
    .join("\n")
    .trim();
}

function messageIsToolResult(message = {}, blocks = messageContentBlocks(message)) {
  if (chatMessagePresentation(message).normalizedRole === "tool") return true;
  return blocks.some((block) => contentBlockType(block) === "tool_result");
}

export function transcriptMessageText(message = {}) {
  const blocks = messageContentBlocks(message);
  if (messageIsToolResult(message, blocks)) return "";
  const presentation = chatMessagePresentation(message);
  if (presentation.normalizedRole !== "assistant") return visibleMessageText(message);
  if (blocks.some((block) => contentBlockType(block) === "tool_use")) {
    return blocks
      .filter((block) => contentBlockType(block) === "text")
      .map(contentBlockText)
      .filter(Boolean)
      .join("\n\n");
  }
  return stripLegacyAssistantToolRequests(visibleMessageText(message));
}

export function isTranscriptMessageVisible(message = {}) {
  const blocks = messageContentBlocks(message);
  if (messageIsToolResult(message, blocks)) return false;
  if (transcriptMessageText(message).trim()) return true;
  if (chatMessagePresentation(message).normalizedRole === "assistant" && normalizeGeneratedImageBlocks(message).length) return true;
  return Array.isArray(message.attachments) && message.attachments.length > 0;
}

function transcriptMessages(messages) {
  return (Array.isArray(messages) ? messages : []).filter(isTranscriptMessageVisible);
}

export function generatedImageURL(agentId, messageId, assetId, { download = false } = {}) {
  const agent = boundedImageText(agentId);
  const message = boundedImageText(messageId);
  const asset = boundedImageText(assetId);
  if (!agent || !message || !asset) return "";
  const path = `/api/agents/${encodeURIComponent(agent)}/messages/${encodeURIComponent(message)}/generated-images/${encodeURIComponent(asset)}`;
  return download ? `${path}?download=1` : path;
}

export function renderGeneratedImageBlocksHTML(message = {}, fallbackAgentId = "") {
  const blocks = normalizeGeneratedImageBlocks(message);
  if (!blocks.length) return "";
  const agentId = message.agentId || message.agent_id || fallbackAgentId;
  const messageId = message.id || message.messageId || message.message_id;
  return `<div class="generated-image-grid" data-generated-images>${blocks.map((block) => {
    const imageURL = generatedImageURL(agentId, messageId, block.assetId);
    const downloadURL = generatedImageURL(agentId, messageId, block.assetId, { download: true });
    const alt = block.revisedPrompt || block.filename || cr("imageGeneration.alt");
    const filename = block.filename || cr("imageGeneration.filename", { index: (block.outputIndex ?? block.blockIndex) + 1 });
    const dimensions = block.width && block.height ? `${block.width} × ${block.height}` : "";
    const imageAttributes = `${block.width ? ` width="${block.width}"` : ""}${block.height ? ` height="${block.height}"` : ""}${block.width && block.height ? ` style="aspect-ratio: ${block.width} / ${block.height}"` : ""}`;
    // The asset lives behind the token-guarded API, which a plain <img src> or a
    // new-tab navigation cannot authenticate. Both the preview and the download
    // carry their path in a data attribute and are hydrated to a blob: URL.
    const preview = imageURL
      ? `<button type="button" class="generated-image-open" data-image-lightbox="${escapeAttr(imageURL)}" data-image-lightbox-name="${escapeAttr(filename)}" data-image-lightbox-caption="${escapeAttr(alt)}" aria-label="${escapeAttr(cr("imageGeneration.open", { filename }))}"><img class="generated-image-preview" ${protectedImageAttribute}="${escapeAttr(imageURL)}" alt="${escapeAttr(alt)}" loading="lazy" decoding="async"${imageAttributes}></button><div class="generated-image-missing" data-generated-image-missing hidden>${escapeHtml(cr("imageGeneration.missing"))}</div>`
      : `<div class="generated-image-missing" data-generated-image-missing>${escapeHtml(cr("imageGeneration.missing"))}</div>`;
    return `<figure class="generated-image-card" data-generated-image data-output-index="${escapeAttr(String(block.outputIndex ?? block.blockIndex))}" data-generation-id="${escapeAttr(block.generationId)}">${preview}<figcaption><div><strong title="${escapeAttr(filename)}">${escapeHtml(filename)}</strong>${dimensions ? `<span>${escapeHtml(dimensions)}</span>` : ""}</div>${downloadURL ? `<a class="generated-image-download" ${protectedDownloadAttribute}="${escapeAttr(downloadURL)}" download="${escapeAttr(filename)}">${escapeHtml(cr("imageGeneration.download"))}</a>` : ""}</figcaption>${block.revisedPrompt ? `<p title="${escapeAttr(block.revisedPrompt)}">${escapeHtml(block.revisedPrompt)}</p>` : ""}</figure>`;
  }).join("")}</div>`;
}

export function normalizeImageGenerationStatusEvent(event = {}) {
  if (String(event?.type || "") !== "image_generation.status") return null;
  const data = event?.data && typeof event.data === "object" && !Array.isArray(event.data) ? event.data : {};
  const normalized = {
    requestId: boundedImageText(data.requestId ?? data.request_id),
    runId: boundedImageText(data.runId ?? data.run_id),
    generationId: boundedImageText(data.generationId ?? data.generation_id),
    status: boundedImageText(data.status, 64).toLowerCase() || "running",
    outputIndex: imageOutputIndex(data.outputIndex ?? data.output_index),
    partialIndex: imageOutputIndex(data.partialIndex ?? data.partial_index),
  };
  return normalized.requestId || normalized.runId || normalized.generationId || normalized.outputIndex !== null ? normalized : null;
}

const messagePageLimit = 100;
const maxToolActivityText = 12_000;
const maxToolActivityDiffLines = 800;
const maxToolActivityCards = 40;
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

function planText(value, fallback = "") {
  if (value === null || value === undefined) return fallback;
  if (typeof value === "string" || typeof value === "number") return String(value).trim() || fallback;
  if (typeof value === "object") return planText(value.title ?? value.text ?? value.message ?? value.description ?? value.name, fallback);
  return fallback;
}

function planList(value) {
  if (Array.isArray(value)) return value.filter((item) => item !== null && item !== undefined);
  return value === null || value === undefined || value === "" ? [] : [value];
}

function planStatus(value, fallback = "draft") {
  const status = String(value || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
  return status || fallback;
}

export function normalizeAgentPlan(value, agentId = "") {
  const wrapper = value && typeof value === "object" ? value : {};
  const source = wrapper.plan && typeof wrapper.plan === "object" ? wrapper.plan : wrapper;
  const review = source.review && typeof source.review === "object" ? source.review : {};
  const steps = planList(source.steps ?? source.planSteps ?? source.plan_steps).map((step, index) => ({
    title: planText(step, cr("plan.stepFallback", { index: index + 1 })),
    detail: typeof step === "object" ? planText(step.detail ?? step.description ?? step.reason) : "",
    status: typeof step === "object" ? planStatus(step.status, "") : "",
  }));
  const risks = planList(source.risks ?? source.riskItems ?? source.risk_items).map((risk) => planText(risk)).filter(Boolean);
  const reviewFindings = planList(source.reviewFindings ?? source.review_findings ?? review.findings ?? review.items)
    .map((finding) => planText(finding))
    .filter(Boolean);
  const rawRevision = Number(source.revision ?? source.planRevision ?? source.plan_revision);
  const plan = {
    id: planText(source.id ?? source.planId ?? source.plan_id),
    agentId: planText(source.agentId ?? source.agent_id, agentId),
    revision: Number.isSafeInteger(rawRevision) && rawRevision > 0 ? rawRevision : 0,
    goal: planText(source.goal ?? source.objective ?? source.title ?? source.summary),
    status: planStatus(source.status ?? source.state, wrapper.pendingApproval === true || wrapper.pendingPlanApproval === true ? "pending_approval" : "draft"),
    steps,
    risks,
    reviewVerdict: planText(source.reviewVerdict ?? source.review_verdict ?? review.verdict ?? review.status),
    reviewFindings,
    staleReason: planText(source.staleReason ?? source.stale_reason ?? source.invalidReason ?? source.invalid_reason),
    createdAt: planText(source.createdAt ?? source.created_at),
    updatedAt: planText(source.updatedAt ?? source.updated_at),
  };
  return plan.id || plan.goal || plan.steps.length || plan.risks.length || plan.reviewVerdict || plan.staleReason ? plan : null;
}

function compactPlanStatus(status) {
  const value = planStatus(status);
  if (["in_review", "pending_approval", "awaiting_approval", "approval_required"].includes(value)) return "pending_approval";
  if (["approved", "ready", "accepted"].includes(value)) return "approved";
  if (["executing", "running", "in_progress"].includes(value)) return "executing";
  if (["executed", "completed", "done"].includes(value)) return "executed";
  if (["cancelled", "canceled", "rejected"].includes(value)) return "cancelled";
  if (["stale", "invalid", "outdated"].includes(value)) return "stale";
  if (value === "draft" || value === "planning") return "draft";
  return "unknown";
}

function compactToolText(text, max = 140) {
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
  if (value === "completed") return cr("activity.completed");
  if (value === "pending_approval") return cr("run.toolStatus.pendingApproval");
  if (value === "denied") return cr("run.toolStatus.denied");
  if (value === "interrupted") return cr("run.status.interrupted");
  if (value === "superseded") return cr("run.status.superseded");
  return cr("activity.failed");
}

function firstToolValue(source, ...keys) {
  for (const key of keys) {
    const value = source?.[key];
    if (value !== undefined && value !== null && value !== "") return value;
  }
  return undefined;
}

function parseToolJSON(value) {
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

function toolStatusValue(status) {
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
    errorMessage: firstToolValue(source, "errorMessage", "error_message", "error") || "",
    truncated: Boolean(firstToolValue(source, "truncated", "inputTruncated", "input_truncated", "outputTruncated", "output_truncated", "resultTruncated", "result_truncated", "diffTruncated", "diff_truncated")),
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

function toolActivitySafetyMetaParts(item) {
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

function renderToolActivityFactTags(item) {
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

function renderToolActivitySafetySummary(item) {
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
  return `<div class="tool-activity-safety"><div class="tool-activity-meta">${escapeHtml(cr("activity.safetyDecision"))}</div><div class="tool-activity-safety-summary">${escapeHtml(parts.join(" · "))}</div></div>`;
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

const agentTaskStatuses = new Set(["queued", "waiting_approval", "running", "cancel_requested", "succeeded", "failed", "canceled", "interrupted"]);
const agentTaskExpandedStatuses = new Set(["waiting_approval", "failed", "canceled", "interrupted"]);
const agentTaskCancellableStatuses = new Set(["queued", "waiting_approval", "running"]);
const maxAgentTaskID = 160;
const maxAgentTaskDescription = 240;
const maxAgentTaskRole = 80;
const maxAgentTaskModel = 256;
const maxAgentTaskErrorCode = 96;
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
  const toolDispatched = tool.status === "completed";
  const status = backgroundStatus || (tool.status === "error" ? "failed" : (toolDispatched ? "dispatched" : "dispatching"));
  const acceptanceCriteria = firstToolValue(input, "acceptance_criteria", "acceptanceCriteria");
  const acceptanceCount = agentTaskNumber(firstToolValue(summary, "acceptanceCount", "acceptance_count"), maxAgentTaskAcceptanceCount)
    ?? agentTaskNumber(firstToolValue(result, "acceptanceCount", "acceptance_count"), maxAgentTaskAcceptanceCount)
    ?? (Array.isArray(acceptanceCriteria) ? Math.min(acceptanceCriteria.length, maxAgentTaskAcceptanceCount) : 0);
  return {
    tool,
    taskPresent: Boolean(backgroundStatus),
    taskId,
    description: compactToolText(firstToolValue(summary, "description") || firstToolValue(input, "description") || cr("subagent.descriptionFallback"), maxAgentTaskDescription),
    role: compactToolText(firstToolValue(summary, "subagentType", "subagent_type", "role") || firstToolValue(result, "role", "subagentType", "subagent_type") || firstToolValue(input, "subagent_type", "subagentType", "role") || cr("subagent.roleAuto"), maxAgentTaskRole),
    requestedModel: compactToolText(firstToolValue(summary, "model") || firstToolValue(input, "model"), maxAgentTaskModel),
    acceptanceCount,
    status,
    toolDispatched,
    durationMs: agentTaskDuration(task) || tool.durationMs,
    childAgentId: compactToolText(firstToolValue(task, "childAgentId", "child_agent_id") || firstToolValue(result, "childAgentId", "child_agent_id"), maxAgentTaskID),
    childRunId: compactToolText(firstToolValue(task, "childRunId", "child_run_id") || firstToolValue(result, "childRunId", "child_run_id"), maxAgentTaskID),
    ownerAgentId: compactToolText(firstToolValue(task, "ownerAgentId", "owner_agent_id") || tool.agentId, maxAgentTaskID),
    errorCode: compactToolText(firstToolValue(task, "errorCode", "error_code"), maxAgentTaskErrorCode),
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
  if (["queued", "running", "dispatching", "dispatched"].includes(status)) return "status-running";
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
  const model = activity.requestedModel || cr("subagent.modelAuto");
  const meta = [
    cr("subagent.role", { role: activity.role }),
    cr("subagent.requestedModel", { model }),
    cr("subagent.acceptanceCount", { count: activity.acceptanceCount }),
    duration ? cr("subagent.duration", { duration }) : "",
  ].filter(Boolean);
  const taskState = !activity.taskPresent && activity.toolDispatched ? cr("subagent.waitingTaskInfo") : "";
  const failure = agentTaskFailureNotice(activity);
  const input = toolActivityInputText(tool);
  const output = toolActivityOutputText(tool);
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
        ${renderAgentTaskActionsHTML(activity)}
      </details>
      <details class="tool-activity-details subagent-task-audit"${options.detailsExpanded ? " open" : ""}>
        <summary>${escapeHtml(cr("subagent.auditDetails"))}</summary>
        <div class="tool-activity-meta">${escapeHtml(cr("activity.input"))}</div>
        <pre class="tool-activity-command">${escapeHtml(input || cr("activity.noOutput"))}</pre>
        <div class="tool-activity-meta">${escapeHtml(cr("activity.output"))}</div>
        ${output ? `<pre class="tool-activity-output live-tool-output-body">${escapeHtml(output)}</pre>` : `<div class="tool-activity-empty">${escapeHtml(cr("activity.noOutput"))}</div>`}
        ${tool.truncated ? `<div class="tool-activity-truncated">${escapeHtml(cr("activity.truncated"))}</div>` : ""}
      </details>
    </article>
  `;
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
  return `
    <article class="tool-activity-card live-tool-output-card chat-flow-item chat-flow-left chat-report-card ${escapeAttr(toolActivityStatusClass(status))}" aria-label="${escapeAttr(cardLabel)}" data-chat-alignment="left" data-chat-report="tool-activity" data-live-tool-output-card="${escapeAttr(tool.toolUseId)}" data-tool-use-id="${escapeAttr(tool.toolUseId)}" data-run-id="${escapeAttr(tool.runId)}">
      <div class="tool-activity-head live-tool-output-head">
        <span class="${escapeAttr(icon.classes)}" aria-hidden="true">${icon.svg}</span>
        <div class="tool-activity-main">
          <div class="tool-activity-title live-tool-output-title">${escapeHtml(tool.toolName)}</div>
          ${target ? `<div class="tool-activity-target">${escapeHtml(target)}</div>` : ""}
          ${factTags}
          ${classificationWarning}
          ${meta ? `<div class="tool-activity-meta live-tool-output-meta">${escapeHtml(meta)}</div>` : ""}
        </div>
        <span class="tool-activity-status live-tool-output-dot">${escapeHtml(toolActivityStatusLabel(status))}</span>
      </div>
      <details class="tool-activity-details"${options.detailsExpanded ? " open" : ""}>
        <summary>${escapeHtml(cr("activity.details"))}</summary>
        ${safetySummary}
        <div class="tool-activity-meta">${escapeHtml(cr("activity.input"))}</div>
        <pre class="tool-activity-command">${escapeHtml(input || cr("activity.noOutput"))}</pre>
        ${diff ? `<div class="tool-activity-meta">${escapeHtml(cr("activity.diff"))}</div>${diff}` : ""}
        <div class="tool-activity-meta">${escapeHtml(cr("activity.output"))}</div>
        ${output ? `<pre class="tool-activity-output live-tool-output-body">${escapeHtml(output)}</pre>` : `<div class="tool-activity-empty">${escapeHtml(cr("activity.noOutput"))}</div>`}
        ${tool.truncated ? `<div class="tool-activity-truncated">${escapeHtml(cr("activity.truncated"))}</div>` : ""}
      </details>
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
  if (typeof options.expanded === "boolean") return options.expanded;
  if (String(options.selectedToolUseId || "")) return true;
  if (options.live === true && options.runActive !== false) return true;
  return records.some((record) => toolActivityRecordNeedsExpansion(record, options));
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
    title: tool.toolName,
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
  return `
    <section class="${options.live ? "live-tool-output-stack " : ""}${modeClass}tool-activity-stack chat-flow-stack chat-flow-left" data-chat-alignment="left" data-tool-activity-stack data-tool-activity-stack-key="${escapeAttr(stackKey)}" data-tool-activity-source="${escapeAttr(source)}" data-tool-activity-count="${escapeAttr(String(totalCount))}" data-tool-activity-visible-count="${escapeAttr(String(records.length))}" data-tool-activity-default="${expanded ? "expanded" : "collapsed"}"${runId ? ` data-run-id="${escapeAttr(runId)}"` : ""}${options.live ? " data-live-tool-output-stack" : ""}${options.compact ? " data-conversation-run-tool-activity" : ""}>
      <details class="tool-activity-group"${expanded ? " open" : ""}>
        <summary class="tool-activity-summary">${escapeHtml(cr("activity.processTitle", { count: totalCount }))}</summary>
        <div class="tool-activity-protected">${escapeHtml(cr("activity.processProtected"))}</div>
        <ul class="tool-activity-steps">${renderToolActivityRowsHTML(records, reasoningSteps, { ...options, selectedToolUseId })}</ul>
        ${omitted > 0 ? `<div class="tool-activity-more">${escapeHtml(cr("activity.recentOnly", { visible: records.length, count: omitted }))}</div>` : ""}
        <div class="tool-activity-selected-detail" data-tool-activity-selected-detail>${selectedRecord ? renderToolActivityCardHTML(selectedRecord.item, { ...options, detailsExpanded: true }) : ""}</div>
      </details>
    </section>
  `;
}

export function createChatRenderingController({
  state,
  attachmentIcon,
  attachmentKind,
  apiRequest = api,
  AbortControllerImpl = globalThis.AbortController,
  copyToClipboard,
  notifyTerminal,
  resolveBackgroundTask,
  selectedModelValue,
  shortPath,
  showError,
  showToast,
} = {}) {
  const request = apiRequest || api;
  let messageLifecycleGeneration = 0;
  let messageLoadRequest = null;
  let olderMessagesRequest = null;

  const currentUserMessageIdentity = () => normalizeMessageProfileIdentity(state.profile);

  function toolActivitySelections() {
    if (!state.toolActivitySelections || typeof state.toolActivitySelections !== "object" || Array.isArray(state.toolActivitySelections)) {
      state.toolActivitySelections = {};
    }
    return state.toolActivitySelections;
  }

  function selectedToolActivity(stackKey) {
    return String(toolActivitySelections()[String(stackKey || "")] || "");
  }

  function setSelectedToolActivity(stackKey, toolUseId) {
    const key = String(stackKey || "");
    if (!key) return "";
    const next = { ...toolActivitySelections() };
    if (toolUseId) next[key] = String(toolUseId);
    else delete next[key];
    state.toolActivitySelections = next;
    return String(toolUseId || "");
  }

  function runToolActivityStackKey(runId) {
    return `run:${String(runId || "current")}`;
  }

  function liveToolActivityStackKey(records = []) {
    const agentId = String(state.agent?.id || "current");
    const runId = records.map((item) => normalizeToolActivity(item).runId).find(Boolean) || state.liveAssistantRunId || "current";
    return `live:${agentId}:${runId}`;
  }

  const profileAvatarHTML = (identity) => identity?.avatarDataUrl
    ? `<img class="message-avatar-image" data-user-profile-avatar-image src="${escapeAttr(identity.avatarDataUrl)}" alt="" aria-hidden="true" />`
    : escapeHtml(identity?.avatarInitials || "AT");

  const messageLifecycleIsCurrent = (agentId, generation) => (
    messageLifecycleGeneration === generation && state.agent?.id === agentId
  );

  function abortMessageRequest(operation) {
    try { operation?.controller?.abort?.(); } catch {}
  }

  function messageRequest(agentId, generation) {
    return {
      agentId,
      generation,
      controller: typeof AbortControllerImpl === "function" ? new AbortControllerImpl() : null,
    };
  }

  function isAbortedMessageRequest(error, operation) {
    return Boolean(operation?.controller?.signal?.aborted)
      || error?.name === "AbortError"
      || error?.code === 20;
  }

  function invalidateMessageLifecycle() {
    messageLifecycleGeneration += 1;
    abortMessageRequest(messageLoadRequest);
    abortMessageRequest(olderMessagesRequest);
    messageLoadRequest = null;
    olderMessagesRequest = null;
    state.messageOlderLoading = false;
    return messageLifecycleGeneration;
  }

  async function loadMessages(agentId = state.agent?.id) {
    if (!agentId) return false;
    const generation = messageLifecycleGeneration;
    abortMessageRequest(messageLoadRequest);
    abortMessageRequest(olderMessagesRequest);
    olderMessagesRequest = null;
    state.messageOlderLoading = false;
    const operation = messageRequest(agentId, generation);
    messageLoadRequest = operation;
    try {
      const page = await request(
        `/api/agents/${encodeURIComponent(agentId)}/messages?limit=${messagePageLimit}`,
        operation.controller ? { signal: operation.controller.signal } : {},
      );
      if (!messageLifecycleIsCurrent(agentId, generation)) return false;
      return applyMessageSnapshot(page?.messages, agentId, {
        hasMoreBefore: page?.hasMoreBefore,
        nextBefore: page?.nextBefore,
        clearLiveImageGenerations: true,
      });
    } catch (err) {
      if (isAbortedMessageRequest(err, operation) || !messageLifecycleIsCurrent(agentId, generation)) return false;
      throw err;
    } finally {
      if (messageLoadRequest === operation) messageLoadRequest = null;
    }
  }

  async function loadOlderMessages(agentId = state.agent?.id) {
    if (!agentId || state.agent?.id !== agentId || !state.messageHasMoreBefore || !state.messageNextBefore || state.messageOlderLoading) return false;
    const generation = messageLifecycleGeneration;
    const cursor = state.messageNextBefore;
    const operation = messageRequest(agentId, generation);
    olderMessagesRequest = operation;
    state.messageOlderLoading = true;
    const el = $("messages");
    const previousHeight = el?.scrollHeight || 0;
    const previousTop = el?.scrollTop || 0;
    applyMessageSnapshot(state.currentMessages, agentId, { preserveScroll: true });
    try {
      const page = await request(
        `/api/agents/${encodeURIComponent(agentId)}/messages?before=${encodeURIComponent(cursor)}&limit=${messagePageLimit}`,
        operation.controller ? { signal: operation.controller.signal } : {},
      );
      if (!messageLifecycleIsCurrent(agentId, generation) || olderMessagesRequest !== operation) return false;
      const older = Array.isArray(page?.messages) ? page.messages : [];
      const existing = new Set((state.currentMessages || []).map((message) => message?.id).filter(Boolean));
      const merged = [...older.filter((message) => !message?.id || !existing.has(message.id)), ...(state.currentMessages || [])];
      applyMessageSnapshot(merged, agentId, {
        hasMoreBefore: page?.hasMoreBefore,
        nextBefore: page?.nextBefore,
        preserveScroll: true,
      });
      if (el) el.scrollTop = previousTop + Math.max(0, el.scrollHeight - previousHeight);
      return true;
    } catch (err) {
      if (isAbortedMessageRequest(err, operation) || !messageLifecycleIsCurrent(agentId, generation)) return false;
      throw err;
    } finally {
      if (olderMessagesRequest === operation) {
        olderMessagesRequest = null;
        if (messageLifecycleIsCurrent(agentId, generation)) {
          state.messageOlderLoading = false;
          applyMessageSnapshot(state.currentMessages, agentId, { preserveScroll: true });
        }
      }
    }
  }

  function currentPlanForAgent(agentId = state.agent?.id) {
    const active = normalizeAgentPlan(state.activePlan, agentId);
    const pending = normalizeAgentPlan(state.pendingPlanApproval, agentId);
    if (pending && (!pending.agentId || pending.agentId === agentId)) return pending;
    if (active && (!active.agentId || active.agentId === agentId)) return active;
    return null;
  }

  function planStatusLabel(status) {
    const value = compactPlanStatus(status);
    return cr(`plan.status.${value}`);
  }

  function planStatusClass(status) {
    const value = compactPlanStatus(status);
    if (["approved", "executed"].includes(value)) return "status-completed";
    if (["pending_approval", "stale"].includes(value)) return "status-warn";
    if (["cancelled"].includes(value)) return "status-error";
    return "status-neutral";
  }

  function renderPlanCardsHTML() {
    const plan = currentPlanForAgent();
    if (!plan) return "";
    const status = compactPlanStatus(plan.status);
    const pending = status === "pending_approval" || normalizeAgentPlan(state.pendingPlanApproval)?.id === plan.id;
    const busy = Boolean(plan.id && state.planActionBusy?.[plan.id]);
    const executable = ["approved", "ready", "accepted"].includes(status);
    const cancellable = !["executed", "cancelled"].includes(status);
    const title = plan.goal || cr("plan.untitled");
    const steps = plan.steps.length ? `
      <section class="plan-card-section">
        <h4>${escapeHtml(cr("plan.steps"))}</h4>
        <ol class="plan-card-steps">${plan.steps.map((step) => `<li class="${escapeAttr(step.status ? `is-${compactPlanStatus(step.status)}` : "")}"><strong>${escapeHtml(step.title)}</strong>${step.detail ? `<span>${escapeHtml(step.detail)}</span>` : ""}</li>`).join("")}</ol>
      </section>
    ` : "";
    const risks = plan.risks.length ? `
      <section class="plan-card-section">
        <h4>${escapeHtml(cr("plan.risks"))}</h4>
        <ul class="plan-card-list risk">${plan.risks.map((risk) => `<li>${escapeHtml(risk)}</li>`).join("")}</ul>
      </section>
    ` : "";
    const review = plan.reviewVerdict || plan.reviewFindings.length ? `
      <section class="plan-card-section plan-card-review">
        <h4>${escapeHtml(cr("plan.review"))}</h4>
        ${plan.reviewVerdict ? `<div class="plan-review-verdict">${escapeHtml(plan.reviewVerdict)}</div>` : ""}
        ${plan.reviewFindings.length ? `<ul class="plan-card-list">${plan.reviewFindings.map((finding) => `<li>${escapeHtml(finding)}</li>`).join("")}</ul>` : `<p>${escapeHtml(cr("plan.noFindings"))}</p>`}
      </section>
    ` : "";
    const stale = plan.staleReason ? `<div class="plan-card-stale" role="status"><strong>${escapeHtml(cr("plan.staleReason"))}</strong><span>${escapeHtml(plan.staleReason)}</span></div>` : "";
    return `
      <section class="plan-card chat-flow-item chat-flow-left chat-report-card ${escapeAttr(planStatusClass(status))}" data-chat-alignment="left" data-chat-report="agent-plan" data-plan-card="${escapeAttr(plan.id)}">
        <div class="plan-card-head">
          <div>
            <div class="plan-card-kicker">${escapeHtml(cr("plan.kicker"))}</div>
            <div class="plan-card-title">${escapeHtml(title)}</div>
          </div>
          <span class="plan-card-status">${escapeHtml(planStatusLabel(status))}</span>
        </div>
        <section class="plan-card-section plan-card-goal"><h4>${escapeHtml(cr("plan.goal"))}</h4><p>${escapeHtml(title)}</p></section>
        ${steps}${risks}${review}${stale}
        <div class="plan-card-actions">
          ${pending ? `<button class="ghost-btn mini" type="button" data-plan-action="approve" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.approve"))}</button>` : ""}
          ${executable ? `<button class="ghost-btn mini primary" type="button" data-plan-action="execute" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(busy ? cr("plan.working") : cr("plan.execute"))}</button>` : ""}
          ${cancellable ? `<button class="ghost-btn mini danger" type="button" data-plan-action="cancel" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.cancel"))}</button>` : ""}
          <button class="ghost-btn mini" type="button" data-plan-action="replan" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.replan"))}</button>
        </div>
      </section>
    `;
  }

  function renderPlanCards() {
    if (state.chatHydrating || !state.agent?.id) return;
    applyMessageSnapshot(state.currentMessages, state.agent.id, { forceRender: true });
  }

  function replacePlanState(activePlan, pendingPlanApproval, agentId = state.agent?.id) {
    if (!agentId || state.agent?.id !== agentId) return false;
    const active = normalizeAgentPlan(activePlan, agentId);
    const pending = normalizeAgentPlan(pendingPlanApproval, agentId);
    state.activePlan = active;
    state.pendingPlanApproval = pending || (compactPlanStatus(active?.status) === "pending_approval" ? active : null);
    renderPlanCards();
    return true;
  }

  function clearPlanState(agentId = state.agent?.id) {
    return replacePlanState(null, null, agentId);
  }

  function applyPlanEvent(event) {
    const type = String(event?.type || "").toLowerCase();
    if (!type.startsWith("plan.")) return false;
    const data = event?.data && typeof event.data === "object" ? event.data : {};
    const received = normalizeAgentPlan(data.activePlan ?? data.pendingPlanApproval ?? data.pendingPlan ?? data.plan ?? data, event?.agentId || state.agent?.id);
    const current = currentPlanForAgent(event?.agentId || state.agent?.id);
    if (!received && !current) return false;
    const eventStatus = {
      "plan.approval_required": "pending_approval",
      "plan.approved": "approved",
      "plan.executing": "executing",
      "plan.executed": "executed",
      "plan.cancelled": "cancelled",
      "plan.canceled": "cancelled",
      "plan.stale": "stale",
      "plan.replanned": "draft",
    }[type] || "";
    const plan = {
      ...(current || {}),
      ...(received || {}),
      status: eventStatus || received?.status || current?.status || "draft",
      staleReason: received?.staleReason || data.staleReason || data.stale_reason || (type === "plan.stale" ? event?.text || current?.staleReason : current?.staleReason || ""),
    };
    const pending = data.pendingPlanApproval ?? data.pendingPlan ?? (compactPlanStatus(plan.status) === "pending_approval" ? plan : null);
    return replacePlanState(plan, pending, event?.agentId || state.agent?.id);
  }

  async function performPlanAction(planId, action, button) {
    const agentId = state.agent?.id;
    const plan = currentPlanForAgent(agentId);
    if (!agentId || !plan?.id || plan.id !== planId || !action || state.planActionBusy?.[planId]) return;
    state.planActionBusy = { ...(state.planActionBusy || {}), [planId]: true };
    renderPlanCards();
    try {
      const result = await request(`/api/agents/${encodeURIComponent(agentId)}/plans/${encodeURIComponent(planId)}/${encodeURIComponent(action)}`, {
        method: "POST",
        body: JSON.stringify({ revision: plan.revision }),
      });
      if (state.agent?.id !== agentId) return;
      const next = normalizeAgentPlan(result?.activePlan ?? result?.pendingPlanApproval ?? result?.pendingPlan ?? result?.plan ?? result, agentId) || {
        ...plan,
        status: { approve: "approved", execute: "executing", cancel: "cancelled", replan: "draft" }[action] || plan.status,
      };
      const pending = result?.pendingPlanApproval ?? result?.pendingPlan ?? (compactPlanStatus(next.status) === "pending_approval" ? next : null);
      replacePlanState(next, pending, agentId);
      showToast(cr(`plan.toast.${action}`), action === "cancel" ? "warn" : "success");
      scheduleMessageRefresh(80, agentId);
    } catch (error) {
      showError(error);
    } finally {
      const busy = { ...(state.planActionBusy || {}) };
      delete busy[planId];
      state.planActionBusy = busy;
      if (state.agent?.id === agentId) renderPlanCards();
    }
  }

  function bindPlanButtons(root) {
    root.querySelectorAll("[data-plan-action]").forEach((button) => {
      button.addEventListener("click", () => performPlanAction(button.dataset.planId || "", button.dataset.planAction || "", button));
    });
  }

  function imageGenerationStatusKey(status = {}) {
    if (status.generationId) return `generation:${status.generationId}`;
    if (status.requestId) return `request:${status.requestId}:${status.outputIndex ?? 0}`;
    return `run:${status.runId || "unknown"}:${status.outputIndex ?? 0}`;
  }

  function currentLiveImageGenerations() {
    const agentId = state.agent?.id || "";
    return Object.values(state.liveImageGenerations || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .sort((left, right) => (left.outputIndex ?? 0) - (right.outputIndex ?? 0));
  }

  function renderLiveImageGenerationCardsHTML() {
    const items = currentLiveImageGenerations();
    if (!items.length) return "";
    return `<div class="live-image-generation-stack chat-flow-stack chat-flow-left" data-live-image-generation-stack aria-live="polite">${items.map((item) => `<section class="live-image-generation-card chat-flow-item chat-flow-left" data-live-image-generation="${escapeAttr(item.generationId || imageGenerationStatusKey(item))}" data-request-id="${escapeAttr(item.requestId)}" data-run-id="${escapeAttr(item.runId)}" data-output-index="${escapeAttr(String(item.outputIndex ?? ""))}"><div class="live-image-generation-skeleton" aria-hidden="true"><span></span></div><div><strong>${escapeHtml(cr("imageGeneration.generating"))}</strong><span>${escapeHtml(cr("imageGeneration.status", { status: item.status || "running" }))}</span></div></section>`).join("")}</div>`;
  }

  function renderLiveImageGenerationCards() {
    if (state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    const existing = el.querySelector("[data-live-image-generation-stack]");
    const html = renderLiveImageGenerationCardsHTML();
    if (existing) {
      if (html) existing.outerHTML = html;
      else existing.remove();
    } else if (html) {
      el.classList.remove("empty");
      const anchor = el.querySelector("[data-plan-stack], [data-live-tool-output-stack], [data-run-summary-card], [data-run-outcome-card], [data-live-assistant], [data-approval-stack]");
      if (anchor) anchor.insertAdjacentHTML("beforebegin", html);
      else el.insertAdjacentHTML("beforeend", html);
    }
    if (html) el.scrollTop = el.scrollHeight;
  }

  function rememberImageGenerationStatus(event) {
    const normalized = normalizeImageGenerationStatusEvent(event);
    const agentId = String(event?.agentId || event?.agent_id || state.agent?.id || "");
    if (!normalized || !agentId || state.agent?.id !== agentId) return false;
    const key = imageGenerationStatusKey(normalized);
    const current = state.liveImageGenerations?.[key] || {};
    state.liveImageGenerations = {
      ...(state.liveImageGenerations || {}),
      [key]: { ...current, ...normalized, agentId },
    };
    renderLiveImageGenerationCards();
    return true;
  }

  function clearLiveImageGenerations({ agentId = state.agent?.id, preserveView = false } = {}) {
    const next = { ...(state.liveImageGenerations || {}) };
    for (const [key, item] of Object.entries(next)) {
      if (!agentId || !item?.agentId || item.agentId === agentId) delete next[key];
    }
    state.liveImageGenerations = next;
    if (!preserveView) renderLiveImageGenerationCards();
  }

  function applyMessageSnapshot(messages, agentId = state.agent?.id, options = {}) {
    if (!agentId || state.agent?.id !== agentId) return false;
    const normalized = Array.isArray(messages) ? messages : [];
    const visibleMessages = transcriptMessages(normalized);
    if (options.hasMoreBefore !== undefined) state.messageHasMoreBefore = Boolean(options.hasMoreBefore);
    if (options.nextBefore !== undefined) state.messageNextBefore = String(options.nextBefore || "");
    if (options.clearLiveImageGenerations === true) clearLiveImageGenerations({ agentId, preserveView: true });
    const el = $("messages");
    state.currentMessages = normalized;
    state.messageCopyTexts = visibleMessages.map(transcriptMessageText);
    updateConversationCopyButton();
    if (state.chatHydrating && options.forceRender !== true) return true;
    if (!el) return true;
    el.removeAttribute?.("aria-busy");
    if (el.dataset) delete el.dataset.initialChatState;
    const liveAssistantCard = renderLiveAssistantCardHTML();
    const liveImageGenerationCards = renderLiveImageGenerationCardsHTML();
    const planCards = renderPlanCardsHTML();
    const liveToolCards = renderLiveToolOutputCardsHTML();
    const runSummaryCard = renderRunSummaryCardHTML();
    const approvalCards = renderApprovalCardsHTML();
    if (!visibleMessages.length && !liveAssistantCard && !liveImageGenerationCards && !planCards && !liveToolCards && !runSummaryCard && !approvalCards) {
      el.classList.add("empty");
      el.innerHTML = `<div class="empty-conversation-state">${escapeHtml(cr("message.empty"))}</div>`;
      return true;
    }
    el.classList.remove("empty");
    const olderMessagesControl = state.messageHasMoreBefore ? `
      <div class="message-history-control">
        <button class="ghost-btn mini" type="button" data-load-older-messages ${state.messageOlderLoading ? "disabled" : ""}>
          ${state.messageOlderLoading ? "正在加载…" : "加载更早消息"}
        </button>
      </div>
    ` : "";
    // Tail order is a contract shared with the incremental inserters below: the
    // run scaffolding (image generation, plan, live tool output, run outcome)
    // sits above the assistant's own words, so the newest thing the assistant
    // said is the last message in the thread. Only approval prompts, which the
    // user has to act on, render below it.
    el.innerHTML = `${olderMessagesControl}${visibleMessages.map(renderChatMessageCached).join("")}${liveImageGenerationCards}${planCards}${liveToolCards}${runSummaryCard}${liveAssistantCard}${approvalCards}`;
    const liveMessageIds = new Set(visibleMessages.map((message) => message.id).filter(Boolean));
    for (const cachedId of messageHtmlCache.keys()) {
      if (!liveMessageIds.has(cachedId)) messageHtmlCache.delete(cachedId);
    }
    bindToolActivityControls(el);
    bindMessageActionButtons(el);
    el.querySelector("[data-load-older-messages]")?.addEventListener("click", () => {
      loadOlderMessages(agentId).catch(showError);
    });
    bindRunSummaryButtons(el);
    bindPlanButtons(el);
    bindApprovalButtons(el);
    bindCopyCodeButtons(el);
    if (!options.preserveScroll) el.scrollTop = el.scrollHeight;
    return true;
  }

  function renderPerformanceHTML(turnUsage, { live = false } = {}) {
    const text = formatTurnUsagePerformance(turnUsage);
    if (!text) return "";
    const usage = normalizeTurnUsage(turnUsage);
    return `<div class="message-performance${live ? " message-performance-live" : ""}${usage.estimated ? " is-estimated" : ""}" aria-label="${escapeAttr(text)}">${escapeHtml(text)}</div>`;
  }

  function renderLiveAssistantCardHTML() {
    const text = String(state.liveAssistantText || "");
    if (!text) return "";
    return `
      <div class="message assistant live-assistant-message chat-message chat-flow-item chat-flow-left" data-chat-alignment="left" data-live-assistant data-run-id="${escapeAttr(state.liveAssistantRunId || "")}" data-request-id="${escapeAttr(state.liveAssistantRequestId || "")}" data-started-at="${escapeAttr(state.liveAssistantStartedAt || "")}">
        <div class="message-head">
          <div class="message-role">assistant</div>
          ${renderPerformanceHTML(state.liveAssistantPerformance, { live: true })}
        </div>
        <div class="message-content">${renderMarkdown(text)}</div>
      </div>
    `;
  }

  const messageHtmlCache = new Map();

  // Captures every input `renderChatMessageHTML` reads so a cached render can
  // be safely reused only when none of them have changed. `JSON.stringify(message)`
  // is deliberately used wholesale (rather than picking individual fields) so no
  // message field renderChatMessageHTML might read is ever missed.
  function messageRenderSignature(message, index) {
    const editing = Boolean(message.id && state.editingMessageId === message.id);
    const usesProfileIdentity = userMessageRoles.has(chatMessagePresentation(message).normalizedRole);
    const identity = usesProfileIdentity ? JSON.stringify(currentUserMessageIdentity()) : "";
    const agentId = state.agent?.id || "";
    // Proxy for the active UI locale: cr(...) labels change value whenever the
    // language changes, so this stands in for whatever localized strings the
    // real render would have produced.
    const localeProxy = cr("message.copy");
    // Proxy for the separate regional formatting preference (locale/timezone
    // used by formatTimestamp/formatNumber via locale-registry.mjs), which can
    // change independently of the UI language above but still affects the
    // rendered timestamp and byte-size text.
    const regionProxy = formatTimestamp("2020-01-01T00:00:00.000Z", { timeOnly: true });
    // While the correction editor is open for this message, its rendered
    // markup also depends on the in-progress draft text and attached files,
    // neither of which live on the message object itself.
    const correctionState = editing
      ? `${state.correctionText ?? ""} ${(Array.isArray(state.correctionFiles) ? state.correctionFiles : []).map((file) => file?.name || "").join(",")}`
      : "";
    return [
      JSON.stringify(message),
      index,
      editing,
      identity,
      agentId,
      localeProxy,
      regionProxy,
      correctionState,
    ].join(" ");
  }

  function renderChatMessageCached(message, index) {
    if (!message.id) return renderChatMessageHTML(message, index);
    const sig = messageRenderSignature(message, index);
    const cached = messageHtmlCache.get(message.id);
    if (cached?.sig === sig) return cached.html;
    const html = renderChatMessageHTML(message, index);
    messageHtmlCache.set(message.id, { sig, html });
    return html;
  }

  function renderChatMessageHTML(message, index) {
    const presentation = chatMessagePresentation(message);
    const editing = Boolean(message.id && state.editingMessageId === message.id);
    const usesProfileIdentity = userMessageRoles.has(presentation.normalizedRole);
    const profileIdentity = usesProfileIdentity ? currentUserMessageIdentity() : null;
    const avatarLabel = presentation.normalizedRole === "assistant" ? "A" : (presentation.role.slice(0, 1).toUpperCase() || "•");
    const avatarHTML = usesProfileIdentity ? profileAvatarHTML(profileIdentity) : escapeHtml(avatarLabel);
    const roleLabel = profileIdentity?.displayName || presentation.role;
    const profileAvatarAttr = usesProfileIdentity ? " data-user-profile-avatar" : "";
    const correctionLabel = message.correctionOfMessageId ? " · 更正" : "";
    // A correction retires the turns that followed it. They stay readable so the
    // history still makes sense, but are marked so nobody mistakes them for part
    // of what the model is currently working from.
    const superseded = Boolean(message.supersededAt);
    const supersededLabel = superseded ? ` · ${cr("message.superseded")}` : "";
    const roleHTML = usesProfileIdentity
      ? `<span data-user-profile-name>${escapeHtml(roleLabel)}</span>${correctionLabel}${supersededLabel}`
      : `${escapeHtml(roleLabel)}${correctionLabel}${supersededLabel}`;
    const timeHTML = presentation.timestampValue
      ? `<time class="message-time" datetime="${escapeAttr(presentation.timestampValue)}" title="${escapeAttr(formatTimestamp(presentation.timestampValue))}">${escapeHtml(formatTimestamp(presentation.timestampValue, { timeOnly: true }))}</time>`
      : "";
    const actions = `${message.role === "user" && !superseded ? `<button class="message-copy-btn" type="button" data-correct-message="${escapeAttr(message.id || "")}" title="${escapeAttr(cr("message.correctTitle"))}">${escapeHtml(cr("message.correct"))}</button>` : ""}<button class="message-copy-btn" type="button" data-copy-message="${escapeAttr(String(index))}" title="${escapeAttr(cr("message.copyTitle"))}">${escapeHtml(cr("message.copy"))}</button>`;
    return `
      <div class="message ${presentation.roleClass}${editing ? " message-editing" : ""}${superseded ? " message-superseded" : ""} chat-message chat-flow-item chat-flow-${presentation.alignment}" data-chat-alignment="${presentation.alignment}" data-message-role="${escapeAttr(presentation.normalizedRole)}">
        <div class="message-head">
          <div class="message-meta"><span class="message-avatar" aria-hidden="true"${profileAvatarAttr}>${avatarHTML}</span><div class="message-role">${roleHTML}</div></div>
          <div class="message-head-actions">${actions}</div>
          ${timeHTML}
        </div>
        ${editing ? renderCorrectionEditor(message) : `${renderPersistedReasoningHTML(message, presentation.normalizedRole)}<div class="message-content">${renderMarkdown(friendlyMessageText(transcriptMessageText(message)))}</div>${presentation.normalizedRole === "assistant" ? renderGeneratedImageBlocksHTML(message, state.agent?.id || "") : ""}${renderMessageAttachments(message)}`}
        ${presentation.normalizedRole === "assistant" ? renderPerformanceHTML(message.turnUsage) : ""}
      </div>
    `;
  }

  // History has no live step boundaries to hang reasoning off -- the tool calls
  // for a finished run live in their own card -- so a persisted turn shows its
  // reasoning as one collapsed block above the answer it produced.
  function renderPersistedReasoningHTML(message, normalizedRole) {
    if (normalizedRole !== "assistant") return "";
    const text = String(message?.reasoningText || "").trim();
    if (!text) return "";
    return `
      <details class="message-reasoning">
        <summary>${escapeHtml(cr("activity.reasoningSummary"))}</summary>
        <div class="message-reasoning-body">${escapeHtml(text)}</div>
      </details>
    `;
  }

  function refreshUserMessageIdentity() {
    const root = $("messages");
    if (!root) return false;
    const identity = currentUserMessageIdentity();
    let updated = false;
    root.querySelectorAll("[data-user-profile-avatar]").forEach((node) => {
      const image = node.querySelector?.("[data-user-profile-avatar-image]");
      if (identity.avatarDataUrl) {
        if (image?.getAttribute?.("src") === identity.avatarDataUrl) return;
        node.innerHTML = profileAvatarHTML(identity);
        updated = true;
        return;
      }
      if (!image && node.textContent === identity.avatarInitials) return;
      node.textContent = identity.avatarInitials;
      updated = true;
    });
    root.querySelectorAll("[data-user-profile-name]").forEach((node) => {
      if (node.textContent === identity.displayName) return;
      node.textContent = identity.displayName;
      updated = true;
    });
    return updated;
  }

  function renderLiveAssistantCard({ preserveView = false } = {}) {
    if (preserveView || state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    const existing = el.querySelector("[data-live-assistant]");
    const html = renderLiveAssistantCardHTML();
    if (!html) {
      existing?.remove();
      if (!transcriptMessages(state.currentMessages).length && !renderLiveImageGenerationCardsHTML() && !renderPlanCardsHTML() && !renderLiveToolOutputCardsHTML() && !renderRunSummaryCardHTML() && !renderApprovalCardsHTML()) {
        el.classList.add("empty");
        el.innerHTML = `<div class="empty-conversation-state">${escapeHtml(cr("message.empty"))}</div>`;
      }
      return;
    }
    el.classList.remove("empty");
    if (existing) existing.outerHTML = html;
    else {
      const approvalStack = el.querySelector("[data-approval-stack]");
      if (approvalStack) approvalStack.insertAdjacentHTML("beforebegin", html);
      else el.insertAdjacentHTML("beforeend", html);
    }
    bindCopyCodeButtons(el);
    el.scrollTop = el.scrollHeight;
  }

  function liveAssistantEventMatches(detail = {}) {
    const requestId = String(detail.requestId || "");
    const runId = String(detail.runId || "");
    if (requestId && state.liveAssistantRequestId && requestId !== state.liveAssistantRequestId) return false;
    if (runId && state.liveAssistantRunId && runId !== state.liveAssistantRunId) return false;
    return true;
  }

  function beginLiveAssistantGeneration(detail = {}) {
    state.liveAssistantActive = true;
    state.liveAssistantText = "";
    state.liveAssistantRequestId = String(detail.requestId || "");
    state.liveAssistantRunId = String(detail.runId || "");
    state.liveAssistantProvider = String(detail.provider || "");
    state.liveAssistantModel = String(detail.model || "");
    state.liveAssistantStartedAt = String(detail.startedAt || "");
    state.liveAssistantPerformance = normalizeTurnUsage(detail.performance);
    renderLiveAssistantCard();
  }

  function appendLiveAssistantText(text, detail = {}) {
    const delta = String(text || "");
    if (typeof detail === "string") detail = { runId: detail };
    if (!state.liveAssistantActive) {
      beginLiveAssistantGeneration(detail);
    } else if (!liveAssistantEventMatches(detail)) {
      return false;
    }
    if (detail.requestId && !state.liveAssistantRequestId) state.liveAssistantRequestId = String(detail.requestId);
    if (detail.runId && !state.liveAssistantRunId) state.liveAssistantRunId = String(detail.runId);
    if (detail.performance && typeof detail.performance === "object") {
      state.liveAssistantPerformance = normalizeTurnUsage({ ...(state.liveAssistantPerformance || {}), ...detail.performance });
    }
    if (delta) state.liveAssistantText = `${state.liveAssistantText || ""}${delta}`;
    renderLiveAssistantCard();
    return true;
  }

  function updateLiveAssistantPerformance(performance, detail = {}) {
    if (!state.liveAssistantActive || !liveAssistantEventMatches(detail)) return false;
    if (detail.requestId && !state.liveAssistantRequestId) state.liveAssistantRequestId = String(detail.requestId);
    if (detail.runId && !state.liveAssistantRunId) state.liveAssistantRunId = String(detail.runId);
    state.liveAssistantPerformance = normalizeTurnUsage({
      ...(detail.replace === true ? {} : state.liveAssistantPerformance || {}),
      ...(performance && typeof performance === "object" ? performance : {}),
    });
    renderLiveAssistantCard();
    return true;
  }

  function clearLiveAssistantText({ preserveView = false } = {}) {
    state.liveAssistantActive = false;
    state.liveAssistantText = "";
    state.liveAssistantRequestId = "";
    state.liveAssistantRunId = "";
    state.liveAssistantProvider = "";
    state.liveAssistantModel = "";
    state.liveAssistantStartedAt = "";
    state.liveAssistantPerformance = null;
    renderLiveAssistantCard({ preserveView });
  }

  function renderCorrectionEditor(message) {
    const attachments = Array.isArray(message.attachments) ? message.attachments : [];
    const files = Array.isArray(state.correctionFiles) ? state.correctionFiles : [];
    return `
      <form class="message-correction-editor" data-correction-form="${escapeAttr(message.id || "")}">
        <textarea class="message-correction-text" data-correction-text rows="4">${escapeHtml(state.correctionText ?? visibleMessageText(message))}</textarea>
        ${attachments.length ? `<div class="message-correction-attachments">${attachments.map((attachment) => `
          <label><input type="checkbox" data-keep-correction-attachment value="${escapeAttr(attachment.id || "")}" checked /> ${escapeHtml(attachment.filename || "附件")}</label>
        `).join("")}</div>` : ""}
        ${files.length ? `<div class="message-correction-new-files">${files.map((file) => `<span>${escapeHtml(file.name || "附件")}</span>`).join("")}</div>` : ""}
        <label class="message-correction-file-label">添加图片或文本文件<input type="file" data-correction-files multiple /></label>
        <p class="message-correction-note">${escapeHtml(cr("message.correctionNote"))}</p>
        <div class="message-correction-actions">
          <button class="ghost-btn mini" type="button" data-correction-cancel>${escapeHtml(cr("message.correctionCancel"))}</button>
          <button class="ghost-btn mini" type="submit" title="${escapeAttr(cr("message.correctTitle"))}">${escapeHtml(cr("message.correctionSubmit"))}</button>
        </div>
      </form>
    `;
  }

  function correctionClipboardFiles(event) {
    const direct = Array.from(event?.clipboardData?.files || []).filter(Boolean);
    if (direct.length) return direct;
    return Array.from(event?.clipboardData?.items || [])
      .filter((item) => item?.kind === "file")
      .map((item) => item.getAsFile?.())
      .filter(Boolean);
  }

  function openCorrectionEditor(messageId) {
    state.editingMessageId = messageId;
    state.correctionText = visibleMessageText(state.currentMessages.find((message) => message.id === messageId) || {});
    state.correctionFiles = [];
    applyMessageSnapshot(state.currentMessages, state.agent?.id);
  }

  function closeCorrectionEditor() {
    state.editingMessageId = "";
    state.correctionText = "";
    state.correctionFiles = [];
    applyMessageSnapshot(state.currentMessages, state.agent?.id);
  }

  async function submitCorrection(form) {
    const agentId = state.agent?.id;
    const messageId = form?.dataset?.correctionForm || "";
    if (!agentId || !messageId) return;
    const text = form.querySelector("[data-correction-text]")?.value ?? state.correctionText ?? "";
    const keepAttachmentIds = Array.from(form.querySelectorAll("[data-keep-correction-attachment]:checked")).map((input) => input.value).filter(Boolean);
    const files = Array.isArray(state.correctionFiles) ? state.correctionFiles : [];
    const payload = new FormData();
    payload.append("text", text);
    payload.append("keepAttachmentIds", JSON.stringify(keepAttachmentIds));
    payload.append("context", state.navigationSelectionKind === "project" ? "project" : "conversation");
    files.forEach((file) => payload.append("files", file, file.name || "attachment"));
    await request(`/api/agents/${agentId}/messages/${encodeURIComponent(messageId)}/corrections`, { method: "POST", body: payload });
    state.editingMessageId = "";
    state.correctionText = "";
    state.correctionFiles = [];
    await loadMessages(agentId);
    showToast("已创建更正消息并重新运行。", "success");
  }

  function clearRunSummary({ preserveView = false } = {}) {
    state.activeRunSummary = null;
    state.activeRunSummaryRunId = "";
    state.activeRunToolCalls = [];
    state.activeRunToolCallsRunId = "";
    state.activeRunToolCallsHasMore = false;
    state.activeRunToolCallsNextOffset = 0;
    state.runSummaryLoading = false;
    state.runSummaryError = "";
    state.runRollbackBusy = false;
    state.runSummarySeq = Number(state.runSummarySeq || 0) + 1;
    if (!preserveView) renderRunSummaryCard();
  }

  async function loadLatestRunSummary(agentId = state.agent?.id) {
    if (!agentId) return null;
    const seq = Number(state.runSummarySeq || 0) + 1;
    state.runSummarySeq = seq;
    state.activeRunSummary = null;
    state.activeRunSummaryRunId = "";
    state.activeRunToolCalls = [];
    state.activeRunToolCallsRunId = "";
    state.activeRunToolCallsHasMore = false;
    state.activeRunToolCallsNextOffset = 0;
    state.runSummaryLoading = false;
    state.runSummaryError = "";
    state.runRollbackBusy = false;
    try {
      const runs = await request(`/api/agents/${agentId}/runs?limit=1`);
      if (seq !== state.runSummarySeq || state.agent?.id !== agentId) return null;
      const run = Array.isArray(runs) ? runs[0] : null;
      if (!run || !isTerminalRunStatus(run.status)) {
        renderRunSummaryCard();
        return null;
      }
      return await loadRunSummary(run.id, { agentId });
    } catch (err) {
      if (seq === state.runSummarySeq && state.agent?.id === agentId) {
        state.runSummaryError = err.message || String(err);
        renderRunSummaryCard();
      }
      notifyTerminal?.(`[warn] ${cr("run.refreshFailed", { message: err.message || err })}\n`);
      return null;
    }
  }

  async function loadRunSummary(runId, options = {}) {
    const agentId = options.agentId || state.agent?.id;
    if (!agentId || !runId) return null;
    const seq = Number(state.runSummarySeq || 0) + 1;
    state.runSummarySeq = seq;
    state.activeRunSummaryRunId = runId;
    state.activeRunToolCalls = [];
    state.activeRunToolCallsRunId = runId;
    state.activeRunToolCallsHasMore = false;
    state.activeRunToolCallsNextOffset = 0;
    state.runSummaryLoading = true;
    state.runSummaryError = "";
    renderRunSummaryCard();
    try {
      const [summaryResult, toolCallsResult] = await Promise.allSettled([
        request(`/api/agents/${agentId}/runs/${encodeURIComponent(runId)}`),
        request(`/api/agents/${agentId}/runs/${encodeURIComponent(runId)}/tool-calls?view=activity&limit=${maxToolActivityCards}`),
      ]);
      if (summaryResult.status === "rejected") throw summaryResult.reason;
      if (seq !== state.runSummarySeq || state.agent?.id !== agentId) return null;
      const summary = summaryResult.value;
      const fallbackToolCalls = Array.isArray(summary?.toolCalls) ? summary.toolCalls : [];
      state.activeRunSummary = summary;
      state.activeRunSummaryRunId = summary?.run?.id || runId;
      const toolPage = toolCallsResult.status === "fulfilled" ? toolCallsResult.value : null;
      state.activeRunToolCalls = Array.isArray(toolPage)
        ? toolPage
        : (Array.isArray(toolPage?.toolCalls) ? toolPage.toolCalls : fallbackToolCalls);
      state.activeRunToolCallsHasMore = Boolean(toolPage?.hasMore);
      state.activeRunToolCallsNextOffset = Number(toolPage?.nextOffset || state.activeRunToolCalls.length || 0);
      state.activeRunToolCallsRunId = state.activeRunSummaryRunId;
      state.runSummaryLoading = false;
      state.runSummaryError = "";
      renderLiveToolOutputCards();
      renderRunSummaryCard();
      if (options.notify) showToast(cr("run.refreshed"), "success");
      return summary;
    } catch (err) {
      if (seq !== state.runSummarySeq || state.agent?.id !== agentId) return null;
      state.runSummaryLoading = false;
      state.runSummaryError = err.message || String(err);
      renderRunSummaryCard();
      throw err;
    }
  }

  function renderRunSummaryCard() {
    if (state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    const existing = el.querySelector("[data-run-summary-card], [data-run-outcome-card]");
    // Keep the current review card stable while a refresh is in flight. Rendering
    // the transient loading status here makes context switches visibly flash.
    if (state.runSummaryLoading) return;
    const html = renderRunSummaryCardHTML();
    if (existing) {
      if (html) existing.outerHTML = html;
      else existing.remove();
    } else if (html) {
      if (el.classList.contains("empty")) {
        el.classList.remove("empty");
        el.innerHTML = html;
      } else {
        const anchor = el.querySelector("[data-live-assistant], [data-approval-stack]");
        if (anchor) anchor.insertAdjacentHTML("beforebegin", html);
        else el.insertAdjacentHTML("beforeend", html);
      }
    }
    if (!html) return;
    bindToolActivityControls(el);
    bindRunSummaryButtons(el);
    el.scrollTop = el.scrollHeight;
  }

  function renderRunSummaryCardHTML() {
    const summary = state.activeRunSummary;
    const run = summary?.run;
    const runId = state.activeRunSummaryRunId || run?.id || "";
    if (!run) {
      if (!state.runSummaryError || state.runSummaryLoading) return "";
      return renderRunSummaryLoadErrorHTML();
    }
    // Project-domain runs no longer render a card in the chat flow: the stats
    // grid duplicated the conversation details panel and the recent-message
    // preview duplicated the messages immediately above it. The git
    // checkpoint/rollback controls that used to live here moved into the git
    // modal (see git-workflow.mjs), which reads state.activeRunSummary itself.
    if (isProjectRunReview(run)) return "";
    const toolCalls = activeRunToolCallList(summary, runId);
    return renderConversationRunOutcomeHTML(summary, run, runId, toolCalls);
  }

  function isProjectRunReview(run) {
    return state.navigationSelectionKind === "project" && String(run?.source || "").trim() !== "conversation";
  }

  function activeRunToolCallList(summary, runId) {
    return state.activeRunToolCallsRunId === runId && Array.isArray(state.activeRunToolCalls)
      ? state.activeRunToolCalls
      : (Array.isArray(summary?.toolCalls) ? summary.toolCalls : []);
  }

  function runFailureMessage(run) {
    const raw = String(run?.errorMessage || "").trim();
    if (!raw) return cr("run.conversationErrorFallback");
    const normalized = raw.replace(/\s+/g, " ");
    const detail = extractProviderErrorDetail(normalized);
    const haystack = `${normalized} ${detail}`.toLowerCase();
    let message = detail;
    if (haystack.includes("all available accounts exhausted") || haystack.includes("no available accounts")) {
      message = cr("run.providerErrorAccountsExhausted");
    } else if (
      haystack.includes("账户余额不足")
      || haystack.includes("帳戶餘額不足")
      || haystack.includes("余额不足")
      || haystack.includes("餘額不足")
      || /insufficient (?:account )?(?:balance|credits?)/.test(haystack)
    ) {
      message = cr("run.providerErrorInsufficientBalance");
    } else if (
      haystack.includes("rate limit")
      || haystack.includes("too many requests")
      || haystack.includes("请求过于频繁")
      || haystack.includes("請求過於頻繁")
    ) {
      message = cr("run.providerErrorRateLimited");
    } else if (
      /(?:model.{0,80}(?:not found|does not exist|unsupported|unavailable)|(?:unsupported|unknown) model|模型.{0,24}(?:不存在|不可用|無法使用|无法使用|不支持|不支援))/.test(haystack)
    ) {
      message = cr("run.providerErrorModelUnavailable");
    }
    const httpStatus = normalized.match(/\b([45]\d{2})\b/)?.[1] || "";
    const bounded = compactText(message || normalized, 600);
    return httpStatus ? cr("run.providerErrorWithStatus", { status: httpStatus, message: bounded }) : bounded;
  }

  function extractProviderErrorDetail(value) {
    const text = String(value || "").trim();
    const jsonStart = text.indexOf("{");
    if (jsonStart >= 0) {
      try {
        const payload = JSON.parse(text.slice(jsonStart));
        const nestedError = payload?.error;
        const message = typeof nestedError === "string"
          ? nestedError
          : (nestedError?.message || payload?.message || payload?.detail);
        if (typeof message === "string" && message.trim()) return message.trim();
      } catch {
        // Keep the original provider text when the trailing payload is not valid JSON.
      }
    }
    const prefixed = text.match(/\b(?:openai\s+)?api\s+error(?:\s+[45]\d{2})?\s*:\s*(.+)$/i);
    return prefixed?.[1]?.trim() || text;
  }

  function renderConversationRunOutcomeHTML(summary, run, runId, toolCalls) {
    const status = String(run?.status || "unknown").trim().toLowerCase();
    if (status === "superseded") return "";
    const stackKey = runToolActivityStackKey(runId);
    const toolActivity = renderToolActivityStackHTML(toolCalls, {
      compact: true,
      resolveBackgroundTask,
      runId,
      stackKey,
      selectedToolUseId: selectedToolActivity(stackKey),
      totalCount: Math.max(Number(summary?.toolCallCount || 0), toolCalls.length),
    });
    const loadEarlier = renderEarlierRunToolCallsButton(runId, { compact: true });
    const notice = renderConversationRunNoticeHTML(run, status);
    if (!toolActivity && !loadEarlier && !notice) return "";
    return `
      <section class="conversation-run-outcome chat-flow-item chat-flow-left ${escapeAttr(runStatusClass(status))}" data-chat-alignment="left" data-chat-report="conversation-run" data-run-outcome-card>
        ${notice}
        ${toolActivity}
        ${loadEarlier}
      </section>
    `;
  }

  function renderConversationRunNoticeHTML(run, status) {
    if (status === "error" || status === "failed") {
      const message = runFailureMessage(run);
      return `
        <div class="conversation-run-notice error" role="status">
          <strong>${escapeHtml(cr("run.conversationErrorTitle"))}</strong>
          <div class="conversation-run-error-message">${escapeHtml(message)}</div>
          <span>${escapeHtml(cr("run.conversationHistoryHint"))}</span>
        </div>
      `;
    }
    if (status === "interrupted") {
      return `<div class="conversation-run-notice interrupted" role="status"><span>${escapeHtml(cr("run.conversationInterrupted"))}</span></div>`;
    }
    return "";
  }

  function renderRunSummaryLoadErrorHTML() {
    return `
      <section class="conversation-run-outcome chat-flow-item chat-flow-left status-warn" data-chat-alignment="left" data-chat-report="run-load-error" data-run-outcome-card>
        <div class="conversation-run-notice load-error" role="status">
          <strong>${escapeHtml(cr("run.summaryUnavailableTitle"))}</strong>
          <span>${escapeHtml(cr("run.summaryUnavailableHint"))}</span>
        </div>
      </section>
    `;
  }

  function renderEarlierRunToolCallsButton(runId, { compact = false } = {}) {
    if (!state.activeRunToolCallsHasMore || !runId) return "";
    return `<button class="ghost-btn mini tool-activity-load-more${compact ? " conversation-tool-activity-more" : ""}" type="button" data-run-tool-activity-more="${escapeAttr(runId)}">${escapeHtml(cr("activity.loadEarlier"))}</button>`;
  }

  async function loadEarlierRunToolCalls(runId) {
    const agentId = state.agent?.id;
    if (!agentId || !runId || !state.activeRunToolCallsHasMore) return;
    const offset = Number(state.activeRunToolCallsNextOffset || state.activeRunToolCalls?.length || 0);
    const page = await request(`/api/agents/${agentId}/runs/${encodeURIComponent(runId)}/tool-calls?view=activity&limit=${maxToolActivityCards}&offset=${offset}`);
    if (state.agent?.id !== agentId || state.activeRunSummaryRunId !== runId) return;
    const calls = Array.isArray(page) ? page : (Array.isArray(page?.toolCalls) ? page.toolCalls : []);
    const known = new Set((state.activeRunToolCalls || []).map((call) => call?.toolUseId || call?.tool_use_id).filter(Boolean));
    state.activeRunToolCalls = [...calls.filter((call) => !known.has(call?.toolUseId || call?.tool_use_id)), ...(state.activeRunToolCalls || [])];
    state.activeRunToolCallsHasMore = Boolean(page?.hasMore);
    state.activeRunToolCallsNextOffset = Number(page?.nextOffset || offset + calls.length);
    renderRunSummaryCard();
  }

  function bindRunSummaryButtons(root) {
    root.querySelectorAll("[data-run-tool-activity-more]").forEach((button) => {
      button.addEventListener("click", () => loadEarlierRunToolCalls(button.dataset.runToolActivityMore || "").catch(showError));
    });
  }

  function isTerminalRunStatus(status) {
    return ["completed", "error", "failed", "interrupted", "superseded"].includes(String(status || ""));
  }

  function runStatusClass(status) {
    const value = String(status || "unknown");
    if (value === "completed") return "status-completed";
    if (value === "error" || value === "failed") return "status-error";
    if (value === "interrupted" || value === "superseded") return "status-warn";
    return "status-neutral";
  }

  function toolStatusLabel(status) {
    const value = String(status || "unknown");
    if (value === "completed") return cr("run.toolStatus.completed");
    if (value === "pending_approval") return cr("run.toolStatus.pendingApproval");
    if (value === "denied") return cr("run.toolStatus.denied");
    if (value === "error") return cr("run.toolStatus.error");
    return value;
  }

  function toolStatusClass(status) {
    const value = String(status || "unknown");
    if (value === "completed") return "status-completed";
    if (value === "pending_approval") return "status-warn";
    if (value === "denied" || value === "error") return "status-error";
    return "status-neutral";
  }

  function compactText(text, max = 140) {
    const value = String(text || "").replace(/\s+/g, " ").trim();
    if (!value) return cr("defaults.empty");
    return value.length > max ? `${value.slice(0, max - 1)}…` : value;
  }

  function toolCallPreview(call) {
    if (call.errorMessage) return compactText(call.errorMessage, 120);
    const input = call.inputJson;
    if (input && typeof input === "object") {
      if (input.command) return compactText(input.command, 120);
      if (input.file_path) return compactText(input.file_path, 120);
      if (input.pattern) return compactText(input.pattern, 120);
    }
    if (typeof input === "string") return compactText(input, 120);
    try {
      return compactText(JSON.stringify(input || {}), 120);
    } catch {
      return "";
    }
  }

  function toolItemFromEvent(event, current = {}) {
    const data = event?.data && typeof event.data === "object" ? event.data : {};
    const outputDelta = event?.text ?? data.text ?? (typeof data.output === "string" ? data.output : "");
    const resultPreview = firstToolValue(data, "resultPreview", "result_preview", "outputPreview", "output_preview") || "";
    const output = outputDelta
      ? trimLiveToolOutput(`${current.output || ""}${outputDelta}`)
      : (current.output || resultPreview || data.output || "");
    return normalizeToolActivity({
      ...current,
      ...data,
      data,
      agentId: event?.agentId || current.agentId || state.agent?.id,
      createdAt: current.createdAt || event?.createdAt || new Date().toISOString(),
      output,
      resultPreview,
      status: data.status || data.state || current.status || "running",
      truncated: Boolean(current.truncated || data.truncated || data.inputTruncated || data.outputTruncated || data.resultTruncated),
    });
  }

  function liveToolOutputCounterKey(item = {}) {
    const tool = normalizeToolActivity(item);
    return `${tool.agentId || state.agent?.id || "current"}:${tool.runId || "current"}`;
  }

  function liveToolOutputTotals() {
    if (!state.liveToolOutputTotals || typeof state.liveToolOutputTotals !== "object" || Array.isArray(state.liveToolOutputTotals)) {
      state.liveToolOutputTotals = {};
    }
    return state.liveToolOutputTotals;
  }

  function rememberLiveToolCount(item, known) {
    if (known) return;
    const key = liveToolOutputCounterKey(item);
    const totals = { ...liveToolOutputTotals() };
    const agentPrefix = `${normalizeToolActivity(item).agentId || state.agent?.id || "current"}:`;
    for (const existingKey of Object.keys(totals)) {
      if (existingKey.startsWith(agentPrefix) && existingKey !== key) delete totals[existingKey];
    }
    totals[key] = Math.max(0, Number(totals[key] || 0)) + 1;
    state.liveToolOutputTotals = totals;
  }

  function pruneLiveToolOutputs(items) {
    const entries = Object.entries(items || {});
    const groups = new Map();
    for (const entry of entries) {
      const key = liveToolOutputCounterKey(entry[1]);
      if (!groups.has(key)) groups.set(key, []);
      groups.get(key).push(entry);
    }
    const selectedIds = new Set(Object.values(toolActivitySelections()).map((value) => String(value || "")).filter(Boolean));
    const kept = new Set();
    for (const group of groups.values()) {
      const active = [];
      const terminal = [];
      for (const entry of group) {
        const status = toolStatusValue(entry[1]?.status);
        if (status === "running" || status === "pending_approval") active.push(entry);
        else terminal.push(entry);
      }
      terminal.sort((left, right) => String(left[1]?.createdAt || "").localeCompare(String(right[1]?.createdAt || "")));
      [...active, ...terminal.slice(-maxToolActivityCards)].forEach(([key]) => kept.add(key));
      group.forEach(([key, item]) => {
        if (selectedIds.has(String(item?.toolUseId || key))) kept.add(key);
      });
    }
    return Object.fromEntries(entries.filter(([key]) => kept.has(key)));
  }

  // Reasoning arrives as a delta stream with no step boundaries of its own. A
  // step is closed by the next tool call, and is filed under that tool's id so
  // the row renders immediately above the action it explains; whatever is still
  // open when the turn ends becomes the trailing step.
  function appendLiveReasoning(event) {
    const runId = String(event?.data?.runId || event?.data?.run_id || event?.runId || "");
    const delta = String(event?.text || event?.data?.text || "");
    if (!delta) return false;
    const draft = state.liveReasoningDraft && state.liveReasoningDraft.runId === runId
      ? state.liveReasoningDraft
      : { runId, text: "" };
    state.liveReasoningDraft = { runId, text: `${draft.text}${delta}`.slice(-maxLiveReasoningCharacters) };
    renderLiveToolOutputCards();
    return true;
  }

  function closeLiveReasoningStep(beforeToolUseId = "") {
    const draft = state.liveReasoningDraft;
    if (!draft || !String(draft.text || "").trim()) return false;
    const steps = Array.isArray(state.liveReasoningSteps) ? state.liveReasoningSteps : [];
    state.liveReasoningSteps = [...steps, {
      id: `reasoning-${steps.length + 1}`,
      runId: draft.runId,
      text: String(draft.text).trim(),
      beforeToolUseId: String(beforeToolUseId || ""),
    }].slice(-maxLiveReasoningSteps);
    state.liveReasoningDraft = null;
    return true;
  }

  function clearLiveReasoning() {
    state.liveReasoningSteps = [];
    state.liveReasoningDraft = null;
  }

  function currentLiveReasoningSteps(runId = "") {
    const expected = String(runId || "");
    const steps = (Array.isArray(state.liveReasoningSteps) ? state.liveReasoningSteps : [])
      .filter((step) => !expected || !step.runId || step.runId === expected);
    const draft = state.liveReasoningDraft;
    if (draft && String(draft.text || "").trim() && (!expected || !draft.runId || draft.runId === expected)) {
      steps.push({ id: "reasoning-open", runId: draft.runId, text: String(draft.text).trim(), beforeToolUseId: "", open: true });
    }
    return steps;
  }

  function rememberToolStarted(event) {
    const data = event?.data || {};
    const toolUseId = firstToolValue(data, "toolUseId", "tool_use_id");
    if (!toolUseId) return;
    closeLiveReasoningStep(toolUseId);
    const known = Object.prototype.hasOwnProperty.call(state.liveToolOutputs || {}, toolUseId);
    const current = state.liveToolOutputs?.[toolUseId] || {};
    const started = toolItemFromEvent(event, current);
    rememberLiveToolCount({ ...started, toolUseId: String(toolUseId) }, known);
    const next = { ...(state.liveToolOutputs || {}) };
    if (started.runId) {
      for (const [key, item] of Object.entries(next)) {
        if (key !== toolUseId && item?.agentId === started.agentId && item?.runId && item.runId !== started.runId) delete next[key];
      }
    }
    next[toolUseId] = { ...started, toolUseId: String(toolUseId), status: "running" };
    state.liveToolOutputs = pruneLiveToolOutputs(next, started.agentId || state.agent?.id || "");
    renderLiveToolOutputCards();
  }

  function appendToolOutput(event) {
    const data = event?.data || {};
    const toolUseId = firstToolValue(data, "toolUseId", "tool_use_id");
    if (!toolUseId) return;
    const known = Object.prototype.hasOwnProperty.call(state.liveToolOutputs || {}, toolUseId);
    const current = state.liveToolOutputs?.[toolUseId] || {};
    const updated = { ...toolItemFromEvent(event, current), toolUseId: String(toolUseId) };
    rememberLiveToolCount(updated, known);
    state.liveToolOutputs = pruneLiveToolOutputs({
      ...(state.liveToolOutputs || {}),
      [toolUseId]: updated,
    }, updated.agentId || state.agent?.id || "");
    renderLiveToolOutputCards();
  }

  function finishToolOutput(event) {
    const data = event?.data || {};
    const toolUseId = firstToolValue(data, "toolUseId", "tool_use_id");
    if (!toolUseId) return;
    const known = Object.prototype.hasOwnProperty.call(state.liveToolOutputs || {}, toolUseId);
    const current = state.liveToolOutputs?.[toolUseId] || {};
    const completed = toolItemFromEvent(event, current);
    const updated = {
      ...completed,
      toolUseId: String(toolUseId),
      status: toolStatusValue(data.status || data.state || "completed"),
      durationMs: Number(firstToolValue(data, "durationMs", "duration_ms") || current.durationMs || 0) || 0,
    };
    rememberLiveToolCount(updated, known);
    state.liveToolOutputs = pruneLiveToolOutputs({
      ...(state.liveToolOutputs || {}),
      [toolUseId]: updated,
    }, updated.agentId || state.agent?.id || "");
    renderLiveToolOutputCards();
  }

  function currentLiveToolOutputList() {
    const agentId = state.agent?.id || "";
    const reviewedRun = state.activeRunSummary?.run;
    const reviewedStatus = String(reviewedRun?.status || "").trim().toLowerCase();
    const runToolsHaveVisibleHome = Boolean(reviewedRun) && reviewedStatus !== "superseded";
    const reviewedIds = runToolsHaveVisibleHome && state.activeRunToolCallsRunId && Array.isArray(state.activeRunToolCalls)
      ? new Set(state.activeRunToolCalls.map((call) => normalizeToolActivity(call).toolUseId).filter(Boolean))
      : new Set();
    return Object.values(state.liveToolOutputs || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .filter((item) => !(item.runId && item.runId === state.activeRunToolCallsRunId && reviewedIds.has(item.toolUseId)))
      .sort((a, b) => String(a.createdAt || "").localeCompare(String(b.createdAt || "")));
  }

  function renderLiveToolOutputCardsHTML() {
    const records = currentLiveToolOutputList();
    const runActive = String(state.agent?.status || "").trim().toLowerCase() === "running";
    const runId = records.length ? normalizeToolActivity(records.at(-1)).runId : String(state.liveAssistantRunId || "");
    const reasoningSteps = currentLiveReasoningSteps(runId);
    if (!records.length && !reasoningSteps.length) return "";
    const stackKey = records.length ? liveToolActivityStackKey(records) : `live:${runId || "current"}`;
    const counterKey = liveToolOutputCounterKey(records.at(-1));
    const totalCount = Math.max(records.length, Number(liveToolOutputTotals()[counterKey] || 0));
    return renderToolActivityStackHTML(records, {
      live: true,
      runActive,
      reasoningSteps,
      resolveBackgroundTask,
      runId,
      stackKey,
      selectedToolUseId: selectedToolActivity(stackKey),
      totalCount,
    });
  }

  function renderLiveToolOutputCards() {
    if (state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    const existing = el.querySelector("[data-live-tool-output-stack]");
    const html = renderLiveToolOutputCardsHTML();
    if (existing) {
      if (html) existing.outerHTML = html;
      else existing.remove();
    } else if (html) {
      if (el.classList.contains("empty")) {
        el.classList.remove("empty");
        el.innerHTML = html;
      } else {
        const anchor = el.querySelector("[data-run-summary-card], [data-run-outcome-card], [data-live-assistant], [data-approval-stack]");
        if (anchor) anchor.insertAdjacentHTML("beforebegin", html);
        else el.insertAdjacentHTML("beforeend", html);
      }
    }
    if (!html) return;
    bindToolActivityControls(el);
    el.scrollTop = el.scrollHeight;
  }

  function trimLiveToolOutput(text) {
    const max = 40000;
    const value = String(text || "");
    if (value.length <= max) return value;
    return `${cr("liveOutput.earlierTruncated")}\n${value.slice(value.length - max)}`;
  }

  function currentApprovalList() {
    const agentId = state.agent?.id || "";
    return Object.values(state.pendingToolApprovals || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .sort((a, b) => String(a.createdAt || "").localeCompare(String(b.createdAt || "")));
  }

  function currentUserQuestionList() {
    const agentId = state.agent?.id || "";
    return Object.values(state.pendingUserQuestions || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .sort((a, b) => String(a.createdAt || "").localeCompare(String(b.createdAt || "")));
  }

  function renderApprovalCardsHTML() {
    const approvals = currentApprovalList();
    const questions = currentUserQuestionList();
    if (!approvals.length && !questions.length) return "";
    return `
      <div class="approval-stack chat-flow-stack chat-flow-left" data-chat-alignment="left" data-approval-stack>
        ${approvals.map(renderApprovalCard).join("")}
        ${questions.map(renderUserQuestionCard).join("")}
      </div>
    `;
  }

  function renderUserQuestionCard(item) {
    const toolUseId = item.toolUseId || "";
    const questions = Array.isArray(item.questions) ? item.questions : [];
    const body = questions.map((question, index) => {
      const key = String(question.question || `q${index}`);
      const multi = question.multiSelect === true;
      const inputType = multi ? "checkbox" : "radio";
      const options = Array.isArray(question.options) ? question.options : [];
      const optionHTML = options.map((option, optionIndex) => {
        const label = String(option.label || `option-${optionIndex}`);
        const description = String(option.description || "");
        const id = `uq-${toolUseId}-${index}-${optionIndex}`;
        return `
          <label class="user-question-option" for="${escapeAttr(id)}">
            <input id="${escapeAttr(id)}" type="${inputType}" name="uq-${escapeAttr(toolUseId)}-${escapeAttr(key)}" value="${escapeAttr(label)}" />
            <span>
              <strong>${escapeHtml(label)}</strong>
              ${description ? `<small>${escapeHtml(description)}</small>` : ""}
            </span>
          </label>
        `;
      }).join("");
      return `
        <div class="user-question-block" data-user-question-block="${escapeAttr(key)}" data-multi="${multi ? "1" : "0"}">
          <div class="user-question-header">${escapeHtml(question.header || key)}</div>
          <div class="user-question-options">${optionHTML}</div>
          <input class="user-question-other" type="text" data-question-other="${escapeAttr(key)}" placeholder="${escapeAttr(cr("userQuestion.otherPlaceholder"))}" maxlength="2000" />
        </div>
      `;
    }).join("");
    return `
      <section class="approval-card chat-flow-item chat-flow-left chat-report-card user-question-card" data-chat-alignment="left" data-chat-report="user-question" data-user-question-card="${escapeAttr(toolUseId)}">
        <div class="approval-card-head">
          <div>
            <div class="approval-title">${escapeHtml(cr("userQuestion.title"))}</div>
            <div class="approval-meta">AskUserQuestion${item.expiresAt ? ` · ${escapeHtml(cr("userQuestion.expires", { time: formatTimestamp(item.expiresAt) }))}` : ""}</div>
          </div>
        </div>
        ${body}
        <div class="approval-actions">
          <button class="ghost-btn mini" type="button" data-user-question-submit="${escapeAttr(toolUseId)}">${escapeHtml(cr("userQuestion.submit"))}</button>
          <button class="ghost-btn mini danger" type="button" data-user-question-skip="${escapeAttr(toolUseId)}">${escapeHtml(cr("userQuestion.skip"))}</button>
        </div>
      </section>
    `;
  }

  function renderApprovalCard(approval) {
    const tool = normalizeToolActivity(approval);
    const risk = tool.risk || "exec";
    const isDanger = risk === "danger";
    const commandOmitted = approval.commandOmitted === true;
    const commandLoadFailed = approval.commandLoadFailed === true;
    const commandUnclassified = tool.commandFacts?.parseKnown === false || tool.commandFacts?.program === "dynamic";
    const projectedCommand = approval.command || approval.input?.command || tool.inputJson?.command || JSON.stringify(approval.input || tool.inputJson || {});
    const command = commandLoadFailed
      ? cr("approval.commandLoadFailed")
      : commandOmitted
        ? cr(isDanger ? "approval.blockedCommandOmitted" : "approval.loadingCommand")
        : projectedCommand;
    const safetyMeta = toolActivitySafetyMetaParts(tool).join(" · ");
    const factTags = renderToolActivityFactTags(tool);
    const safetySummary = renderToolActivitySafetySummary(tool);
    const title = isDanger ? cr("approval.blockedTitle") : cr("approval.requiredTitle");
    const warning = commandLoadFailed
      ? cr("approval.commandLoadFailed")
      : commandOmitted && !isDanger
        ? cr("approval.loadingCommand")
        : commandUnclassified && !isDanger
          ? cr("approval.unclassifiedWarning")
          : approval.warning || (isDanger ? cr("approval.blockedWarning") : cr("approval.warning"));
    const allowDisabled = commandOmitted || commandLoadFailed;
    return `
      <section class="approval-card chat-flow-item chat-flow-left chat-report-card ${isDanger ? "danger" : ""}" data-chat-alignment="left" data-chat-report="tool-approval" data-approval-card="${escapeAttr(approval.toolUseId || "")}">
        <div class="approval-card-head">
          <div>
            <div class="approval-title">${escapeHtml(title)}</div>
            <div class="approval-meta">${escapeHtml(tool.toolName || cr("defaults.tool"))} · ${escapeHtml(risk)} · ${escapeHtml(shortPath(approval.cwd || state.agent?.cwd || ""))}${safetyMeta ? ` · ${escapeHtml(safetyMeta)}` : ""}</div>
          </div>
          <span class="approval-risk">${escapeHtml(risk)}</span>
        </div>
        <pre class="approval-command">${escapeHtml(command)}</pre>
        ${factTags}
        ${safetySummary}
        <div class="approval-warning">${escapeHtml(warning)}</div>
        ${approval.expiresAt ? `<div class="approval-meta">${escapeHtml(cr("approval.expires", { time: formatTimestamp(approval.expiresAt) }))}</div>` : ""}
        ${isDanger ? `<div class="approval-blocked-note">${escapeHtml(cr("approval.blockedNote"))}</div>` : `
          <div class="approval-actions">
            <button class="ghost-btn mini" type="button" data-approval-decision="allow_once" data-tool-use-id="${escapeAttr(approval.toolUseId || "")}" ${allowDisabled ? "disabled" : ""}>${escapeHtml(cr("approval.allowOnce"))}</button>
            <button class="ghost-btn mini" type="button" data-approval-decision="allow_session" data-tool-use-id="${escapeAttr(approval.toolUseId || "")}" ${allowDisabled ? "disabled" : ""}>${escapeHtml(cr("approval.allowSession"))}</button>
            <button class="ghost-btn mini danger" type="button" data-approval-decision="deny" data-tool-use-id="${escapeAttr(approval.toolUseId || "")}">${escapeHtml(cr("approval.deny"))}</button>
          </div>
        `}
      </section>
    `;
  }

  function renderApprovalCards() {
    if (state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    const existing = el.querySelector("[data-approval-stack]");
    const html = renderApprovalCardsHTML();
    if (existing) {
      if (html) existing.outerHTML = html;
      else existing.remove();
    } else if (html) {
      if (el.classList.contains("empty")) {
        el.classList.remove("empty");
        el.innerHTML = html;
      } else {
        el.insertAdjacentHTML("beforeend", html);
      }
    }
    if (!html) return;
    bindApprovalButtons(el);
    el.scrollTop = el.scrollHeight;
  }

  function bindApprovalButtons(root) {
    root.querySelectorAll("[data-approval-decision]").forEach((button) => {
      button.addEventListener("click", () => approveToolCall(button.dataset.toolUseId, button.dataset.approvalDecision, button));
    });
    root.querySelectorAll("[data-user-question-submit]").forEach((button) => {
      button.addEventListener("click", () => submitUserQuestion(button.dataset.userQuestionSubmit, false, button));
    });
    root.querySelectorAll("[data-user-question-skip]").forEach((button) => {
      button.addEventListener("click", () => submitUserQuestion(button.dataset.userQuestionSkip, true, button));
    });
  }

  async function submitUserQuestion(toolUseId, skipped, button) {
    if (!state.agent?.id || !toolUseId) return;
    const card = button?.closest("[data-user-question-card]") || document.querySelector(`[data-user-question-card="${CSS.escape(toolUseId)}"]`);
    const buttons = card?.querySelectorAll("button") || [];
    if (skipped) {
      buttons.forEach((node) => { node.disabled = true; });
      try {
        await request(`/api/agents/${state.agent.id}/tool-calls/${encodeURIComponent(toolUseId)}/user-answer`, {
          method: "POST",
          body: JSON.stringify({ skipped: true, reason: "skipped in UI" }),
        });
        clearUserQuestion(toolUseId);
        showToast(cr("userQuestion.skippedToast"), "warn");
        scheduleMessageRefresh(120, state.agent.id);
      } catch (err) {
        buttons.forEach((node) => { node.disabled = false; });
        showError(err);
      }
      return;
    }
    const answers = [];
    const blocks = card?.querySelectorAll("[data-user-question-block]") || [];
    for (const block of blocks) {
      const key = block.dataset.userQuestionBlock;
      if (!key) continue;
      const selected = [...block.querySelectorAll("input[type='checkbox']:checked, input[type='radio']:checked")].map((node) => node.value).filter(Boolean);
      const otherText = String(block.querySelector(`[data-question-other="${CSS.escape(key)}"]`)?.value || "").trim();
      if (!selected.length && !otherText) {
        showToast(cr("userQuestion.needSelection"), "warn");
        return;
      }
      answers.push({ question: key, selectedLabels: selected, otherText });
    }
    buttons.forEach((node) => { node.disabled = true; });
    try {
      await request(`/api/agents/${state.agent.id}/tool-calls/${encodeURIComponent(toolUseId)}/user-answer`, {
        method: "POST",
        body: JSON.stringify({ answers }),
      });
      clearUserQuestion(toolUseId);
      showToast(cr("userQuestion.submittedToast"), "success");
      scheduleMessageRefresh(120, state.agent.id);
    } catch (err) {
      buttons.forEach((node) => { node.disabled = false; });
      showError(err);
    }
  }

  async function approveToolCall(toolUseId, decision, button) {
    if (!state.agent?.id || !toolUseId || !decision) return;
    const approval = state.pendingToolApprovals?.[toolUseId];
    const buttons = button?.closest(".approval-card")?.querySelectorAll("button") || [];
    buttons.forEach((node) => { node.disabled = true; });
    try {
      await request(`/api/agents/${state.agent.id}/tool-calls/${encodeURIComponent(toolUseId)}/approval`, {
        method: "POST",
        body: JSON.stringify({ decision, reason: decision === "deny" ? "denied in UI" : "approved in UI" }),
      });
      const next = { ...(state.pendingToolApprovals || {}) };
      delete next[toolUseId];
      state.pendingToolApprovals = next;
      renderApprovalCards();
      showToast(decision === "deny" ? cr("approval.deniedToast") : cr("approval.approvedToast"), decision === "deny" ? "warn" : "success");
      notifyTerminal(`[tool] ${decision}: ${approval?.toolName || cr("defaults.tool")} ${toolUseId}\n`);
      scheduleMessageRefresh(120, state.agent.id);
    } catch (err) {
      buttons.forEach((node) => { node.disabled = false; });
      showError(err);
    }
  }

  function replacePendingApprovals(approvals, agentId = state.agent?.id) {
    if (!agentId || state.agent?.id !== agentId) return false;
    const next = { ...(state.pendingToolApprovals || {}) };
    for (const [key, value] of Object.entries(next)) {
      if (!value?.agentId || value.agentId === agentId) delete next[key];
    }
    for (const call of Array.isArray(approvals) ? approvals : []) {
      const toolUseId = call?.toolUseId || call?.tool_use_id;
      if (!toolUseId) continue;
      const input = call.inputJson && typeof call.inputJson === "object" ? call.inputJson : {};
      const toolName = call.toolName || cr("defaults.tool");
      const lowerToolName = String(toolName).toLowerCase();
      next[toolUseId] = {
        ...call,
        agentId,
        toolUseId,
        toolName,
        command: input.command || input.file_path || input.path || JSON.stringify(input),
        cwd: input.cwd || state.agent?.cwd || "",
        risk: lowerToolName === "bash" ? "exec" : (["write", "edit"].includes(lowerToolName) ? "write" : "read"),
        warning: call.permissionSuggestions || call.permissionDecisionReason || cr("approval.awaiting"),
        createdAt: call.createdAt || new Date().toISOString(),
      };
    }
    state.pendingToolApprovals = next;
    renderApprovalCards();
    return true;
  }

  function approvalCommandFromInput(input) {
    if (!input || typeof input !== "object") return "";
    const value = input.command || input.file_path || input.path;
    if (value !== undefined && value !== null) return String(value);
    try {
      return JSON.stringify(input);
    } catch {
      return "";
    }
  }

  async function hydrateToolApproval(agentId, toolUseId) {
    try {
      const call = await request(`/api/agents/${encodeURIComponent(agentId)}/tool-calls/${encodeURIComponent(toolUseId)}`);
      if (state.agent?.id !== agentId) return;
      const current = state.pendingToolApprovals?.[toolUseId];
      if (!current || current.commandOmitted !== true) return;
      if (call?.status && call.status !== "pending_approval") {
        clearToolApproval(toolUseId);
        return;
      }
      const input = parseToolJSON(call?.inputJson ?? call?.input_json ?? call?.input);
      const command = approvalCommandFromInput(input);
      if (!command.trim() || command.length > maxToolActivityText) throw new Error("approval command is unavailable or too large");
      state.pendingToolApprovals = {
        ...(state.pendingToolApprovals || {}),
        [toolUseId]: {
          ...current,
          input,
          inputJson: input,
          command,
          cwd: input.cwd || current.cwd || state.agent?.cwd || "",
          commandOmitted: false,
          commandLoadFailed: false,
        },
      };
      renderApprovalCards();
    } catch {
      if (state.agent?.id !== agentId) return;
      const current = state.pendingToolApprovals?.[toolUseId];
      if (!current || current.commandOmitted !== true) return;
      state.pendingToolApprovals = {
        ...(state.pendingToolApprovals || {}),
        [toolUseId]: { ...current, commandLoadFailed: true },
      };
      renderApprovalCards();
    }
  }

  function rememberToolApproval(event) {
    const data = event.data || {};
    const toolUseId = data.toolUseId || data.tool_use_id;
    const agentId = event.agentId || state.agent?.id;
    if (!toolUseId || !agentId) return;
    state.pendingToolApprovals = {
      ...(state.pendingToolApprovals || {}),
      [toolUseId]: {
        ...data,
        agentId,
        toolUseId,
        createdAt: event.createdAt || new Date().toISOString(),
      },
    };
    renderApprovalCards();
    if (data.commandOmitted === true && data.risk !== "danger") void hydrateToolApproval(agentId, toolUseId);
  }

  function clearToolApproval(toolUseId) {
    if (!toolUseId || !state.pendingToolApprovals?.[toolUseId]) return;
    const next = { ...(state.pendingToolApprovals || {}) };
    delete next[toolUseId];
    state.pendingToolApprovals = next;
    renderApprovalCards();
  }

  function clearCurrentAgentApprovals() {
    const agentId = state.agent?.id;
    if (!agentId) return;
    const next = { ...(state.pendingToolApprovals || {}) };
    for (const [key, value] of Object.entries(next)) {
      if (!value?.agentId || value.agentId === agentId) delete next[key];
    }
    state.pendingToolApprovals = next;
    const questions = { ...(state.pendingUserQuestions || {}) };
    for (const [key, value] of Object.entries(questions)) {
      if (!value?.agentId || value.agentId === agentId) delete questions[key];
    }
    state.pendingUserQuestions = questions;
    renderApprovalCards();
  }

  function rememberUserQuestion(event) {
    const data = event.data || {};
    const toolUseId = data.toolUseId || data.tool_use_id;
    const agentId = event.agentId || state.agent?.id;
    if (!toolUseId || !agentId) return;
    state.pendingUserQuestions = {
      ...(state.pendingUserQuestions || {}),
      [toolUseId]: {
        ...data,
        agentId,
        toolUseId,
        questions: Array.isArray(data.questions) ? data.questions : [],
        createdAt: event.createdAt || new Date().toISOString(),
      },
    };
    renderApprovalCards();
  }

  function replacePendingUserQuestions(items, agentId = state.agent?.id) {
    if (!agentId || state.agent?.id !== agentId) return false;
    const next = { ...(state.pendingUserQuestions || {}) };
    for (const [key, value] of Object.entries(next)) {
      if (!value?.agentId || value.agentId === agentId) delete next[key];
    }
    for (const item of Array.isArray(items) ? items : []) {
      const toolUseId = item?.toolUseId || item?.tool_use_id;
      if (!toolUseId) continue;
      next[toolUseId] = {
        ...item,
        agentId,
        toolUseId,
        questions: Array.isArray(item.questions) ? item.questions : [],
        createdAt: item.createdAt || new Date().toISOString(),
      };
    }
    state.pendingUserQuestions = next;
    renderApprovalCards();
    return true;
  }

  function clearUserQuestion(toolUseId) {
    if (!toolUseId || !state.pendingUserQuestions?.[toolUseId]) return;
    const next = { ...(state.pendingUserQuestions || {}) };
    delete next[toolUseId];
    state.pendingUserQuestions = next;
    renderApprovalCards();
  }

  function renderMessageAttachments(message) {
    const attachments = Array.isArray(message.attachments) ? message.attachments : [];
    if (!attachments.length) return "";
    return `
      <div class="message-attachments">
        ${attachments.map((attachment) => renderSentAttachmentCard(message, attachment)).join("")}
      </div>
    `;
  }

  function renderSentAttachmentCard(message, attachment) {
    const kind = attachment.kind || attachmentKind({ name: attachment.filename || "", type: attachment.mimeType || "" });
    const url = attachmentURL(message, attachment);
    const filename = attachment.filename || cr("attachment.attachment");
    const subtitle = [attachmentKindLabel(kind), formatBytes(attachment.sizeBytes || 0)].filter(Boolean).join(" · ");
    // Images get a real preview instead of a 38px file chip. The src is left
    // empty on purpose: /api/ rejects header-less <img> loads, so the path
    // travels in data-protected-image and is hydrated into a blob URL.
    if (kind === "image") {
      return `
        <figure class="attachment-image-card">
          <button class="attachment-image-open" type="button" data-attachment-lightbox="${escapeAttr(url)}" data-attachment-name="${escapeAttr(filename)}" aria-label="${escapeAttr(cr("attachment.openImage", { filename }))}">
            <img class="attachment-image-preview" ${protectedImageAttribute}="${escapeAttr(url)}" alt="${escapeAttr(filename)}" decoding="async" />
            <span class="attachment-image-failed" data-attachment-image-failed hidden>${escapeHtml(cr("attachment.unavailable"))}</span>
          </button>
          <figcaption class="attachment-image-caption">
            <span class="attachment-name" title="${escapeAttr(filename)}">${escapeHtml(filename)}</span>
            <span class="attachment-subtitle">${escapeHtml(subtitle)}</span>
          </figcaption>
        </figure>
      `;
    }
    return `
      <button class="attachment-card" type="button" data-attachment-download="${escapeAttr(url)}" data-attachment-name="${escapeAttr(filename)}">
        <span class="attachment-thumb">${escapeHtml(attachmentIcon(kind))}</span>
        <div class="attachment-meta">
          <div class="attachment-name" title="${escapeAttr(filename)}">${escapeHtml(filename)}</div>
          <div class="attachment-subtitle">${escapeHtml(subtitle)}</div>
        </div>
      </button>
    `;
  }

  function attachmentURL(message, attachment) {
    return `/api/agents/${encodeURIComponent(message.agentId || state.agent?.id || "")}/messages/${encodeURIComponent(message.id || attachment.messageId || "")}/attachments/${encodeURIComponent(attachment.id || "")}`;
  }

  function messageAttachmentsMarkdown(message) {
    const attachments = Array.isArray(message.attachments) ? message.attachments : [];
    if (!attachments.length) return "";
    const lines = attachments.map((attachment) => `- ${cr("attachment.line", { filename: attachment.filename || cr("attachment.attachment"), kind: attachmentKindLabel(attachment.kind), size: formatBytes(attachment.sizeBytes || 0) })}`);
    return `\n\n${cr("attachment.heading")}\n${lines.join("\n")}`;
  }

  function attachmentKindLabel(kind) {
    if (kind === "image") return cr("attachment.images");
    if (kind === "pdf") return cr("attachment.pdf");
    if (kind === "docx") return cr("attachment.docx");
    if (kind === "text") return cr("attachment.text");
    return cr("attachment.file");
  }

  function updateConversationCopyButton() {
    const button = $("copyConversationBtn");
    if (!button) return;
    const count = transcriptMessages(state.currentMessages).length;
    button.disabled = count === 0;
    button.title = count ? cr("conversation.copyTitle", { count }) : cr("conversation.noCopyTitle");
  }

  function conversationMarkdown() {
    const messages = transcriptMessages(state.currentMessages);
    const title = state.project?.name || state.agent?.title || "Autoto Conversation";
    const meta = [
      `# ${cr("conversation.exportTitle", { title })}`,
      "",
      `- ${cr("conversation.exportedAt", { time: formatTimestamp(new Date()) })}`,
      `- ${cr("conversation.project", { project: state.project?.name || cr("conversation.unselected") })}`,
      `- ${cr("conversation.path", { path: state.agent?.cwd || state.project?.gitPath || cr("conversation.unset") })}`,
      `- ${cr("conversation.agent", { agent: state.agent?.title || state.agent?.id || cr("conversation.unselected") })}`,
      `- ${cr("conversation.model", { model: state.agent?.model || selectedModelValue() || cr("conversation.unselected") })}`,
      "",
    ];
    const body = messages.map((message, index) => {
      const role = String(message.role || cr("defaults.message")).toUpperCase();
      const text = transcriptMessageText(message).trim() || cr("conversation.emptyMessage");
      return `## ${index + 1}. ${role}\n\n${text}${messageAttachmentsMarkdown(message)}`;
    });
    return [...meta, ...body].join("\n");
  }

  async function copyCurrentConversationMarkdown() {
    if (!transcriptMessages(state.currentMessages).length) {
      showToast(cr("conversation.none"), "warn");
      return;
    }
    if (await copyToClipboard(conversationMarkdown())) {
      showToast(cr("conversation.copied"), "success");
      notifyTerminal(`[info] ${cr("conversation.copiedTerminal")}\n`);
      return;
    }
    showToast(cr("conversation.copyFailed"), "warn");
  }

  function clearMessageRefreshTimer(agentId) {
    const timer = state.messageRefreshTimersByAgent?.[agentId];
    if (!timer) return;
    window.clearTimeout(timer);
    const next = { ...(state.messageRefreshTimersByAgent || {}) };
    delete next[agentId];
    state.messageRefreshTimersByAgent = next;
  }

  function scheduleMessageRefresh(delay = 0, agentId = state.agent?.id) {
    if (!agentId) return;
    clearMessageRefreshTimer(agentId);
    const timer = window.setTimeout(() => {
      clearMessageRefreshTimer(agentId);
      loadMessages(agentId).catch((err) => notifyTerminal(`[warn] ${cr("conversation.refreshFailed", { message: err.message || err })}\n`));
    }, Math.max(0, delay));
    state.messageRefreshTimersByAgent = { ...(state.messageRefreshTimersByAgent || {}), [agentId]: timer };
  }

  function friendlyMessageText(text) {
    const value = String(text || "");
    if (value.includes("OpenAI official provider is not configured")) {
      return cr("provider.openAI");
    }
    if (value.includes("Anthropic provider is not configured")) {
      return cr("provider.anthropic");
    }
    if (value.includes("OpenAI-compatible provider is not configured")) {
      return cr("provider.compatible");
    }
    if (value.includes("cliproxyapi provider request failed") && value.includes("127.0.0.1:8317")) {
      return cr("provider.cliProxyUnavailable");
    }
    if (value.includes("cliproxyapi model request failed: 401")) {
      return cr("provider.cliProxyUnauthorized");
    }
    return value;
  }

  function renderMarkdown(text) {
    const blocks = [];
    const pattern = /```([^\n`]*)\n([\s\S]*?)```/g;
    let lastIndex = 0;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      if (match.index > lastIndex) blocks.push(renderMarkdownText(text.slice(lastIndex, match.index)));
      const lang = (match[1] || "text").trim() || "text";
      const code = match[2] || "";
      blocks.push(`<div class="code-block"><div class="code-head"><span>${escapeHtml(lang)}</span><button class="copy-code" type="button" data-code="${escapeAttr(code)}">${escapeHtml(cr("code.copy"))}</button></div><pre><code>${highlightCode(code, lang)}</code></pre></div>`);
      lastIndex = pattern.lastIndex;
    }
    if (lastIndex < text.length) blocks.push(renderMarkdownText(text.slice(lastIndex)));
    return blocks.join("");
  }

  // Blank lines are kept as block separators rather than filtered away: they are
  // the only thing that tells one list from the next, or a heading from the
  // paragraph that follows it.
  function renderMarkdownText(text) {
    const lines = String(text || "").replace(/\r\n?/g, "\n").split("\n");
    const html = [];
    // A stack of open lists, deepest last, so indented bullets nest instead of
    // flattening into one long column.
    let lists = [];
    let quote = [];

    const closeLists = (toDepth = 0) => {
      while (lists.length > toDepth) {
        const closed = lists.pop();
        const markup = `<${closed.tag}>${closed.items.join("")}</${closed.tag}>`;
        if (lists.length) lists[lists.length - 1].items.push(markup);
        else html.push(markup);
      }
    };
    const closeQuote = () => {
      if (!quote.length) return;
      html.push(`<blockquote>${quote.map((line) => `<p>${renderInlineMarkdown(line)}</p>`).join("")}</blockquote>`);
      quote = [];
    };
    const pushItem = (markup) => {
      if (lists.length) lists[lists.length - 1].items.push(markup);
      else html.push(markup);
    };

    for (const raw of lines) {
      const line = raw.replace(/\s+$/, "");
      if (!line.trim()) {
        closeLists();
        closeQuote();
        continue;
      }

      const heading = line.match(/^(#{1,6})\s+(.+)$/);
      if (heading) {
        closeLists();
        closeQuote();
        const level = heading[1].length;
        html.push(`<h${level}>${renderInlineMarkdown(heading[2].replace(/\s+#+\s*$/, ""))}</h${level}>`);
        continue;
      }

      if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
        closeLists();
        closeQuote();
        html.push("<hr>");
        continue;
      }

      const quoted = line.match(/^\s{0,3}>\s?(.*)$/);
      if (quoted) {
        closeLists();
        quote.push(quoted[1]);
        continue;
      }
      closeQuote();

      const bullet = line.match(/^(\s*)[-*+]\s+(.+)$/);
      const ordered = line.match(/^(\s*)\d{1,9}[.)]\s+(.+)$/);
      const item = bullet || ordered;
      if (item) {
        const tag = bullet ? "ul" : "ol";
        // Two spaces is the shallowest indent editors and models agree on, so it
        // is what decides a nesting level here.
        const depth = Math.floor(item[1].replace(/\t/g, "  ").length / 2) + 1;
        while (lists.length > depth) closeLists(lists.length - 1);
        while (lists.length < depth) lists.push({ tag, items: [] });
        const current = lists[lists.length - 1];
        if (current.tag !== tag) {
          closeLists(lists.length - 1);
          lists.push({ tag, items: [] });
        }
        lists[lists.length - 1].items.push(`<li>${renderInlineMarkdown(item[2])}</li>`);
        continue;
      }

      // A plain line while a list is open is that item's continuation, not a new
      // paragraph beside the list.
      if (lists.length) {
        pushItem(`<p>${renderInlineMarkdown(line.trim())}</p>`);
        continue;
      }
      html.push(`<p>${renderInlineMarkdown(line)}</p>`);
    }
    closeLists();
    closeQuote();
    return html.join("");
  }

  // Only http(s) and mailto survive. The link text arrives already escaped, but
  // a scheme like javascript: contains nothing escapeHtml touches, so the scheme
  // has to be checked explicitly or a link becomes a script.
  function safeMarkdownLinkHref(url) {
    const value = String(url || "").trim().replace(/&amp;/g, "&");
    if (!/^(?:https?:\/\/|mailto:)/i.test(value)) return "";
    if (/[\s<>"'`\\]/.test(value)) return "";
    return escapeAttr(value);
  }

  function renderInlineMarkdown(text) {
    const held = [];
    // Code spans are lifted out before any emphasis runs so `**` inside a code
    // span stays literal, which is exactly why someone wrapped it in backticks.
    let out = escapeHtml(text).replace(/`([^`]+)`/g, (_, code) => {
      held.push(`<code class="inline-code">${code}</code>`);
      return `${held.length - 1}`;
    });
    out = out
      .replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, (whole, label, url) => {
        const href = safeMarkdownLinkHref(url);
        return href ? `<a href="${href}" target="_blank" rel="noopener noreferrer">${label}</a>` : whole;
      })
      .replace(/\*\*\*(?=\S)([\s\S]*?\S)\*\*\*/g, "<strong><em>$1</em></strong>")
      .replace(/\*\*(?=\S)([\s\S]*?\S)\*\*/g, "<strong>$1</strong>")
      .replace(/__(?=\S)([\s\S]*?\S)__/g, "<strong>$1</strong>")
      .replace(/~~(?=\S)([\s\S]*?\S)~~/g, "<del>$1</del>")
      // Single-character emphasis is matched last and only when the delimiter
      // hugs the text, so multiplication and snake_case survive intact.
      .replace(/(^|[^*\w])\*(?=\S)([^*\n]*?\S)\*(?![*\w])/g, "$1<em>$2</em>")
      .replace(/(^|[^_\w])_(?=\S)([^_\n]*?\S)_(?![_\w])/g, "$1<em>$2</em>");
    return out.replace(/(\d+)/g, (_, index) => held[Number(index)] ?? "");
  }

  function highlightCode(code, lang) {
    const tokens = [];
    const hold = (html) => {
      const key = `\uE000TOK${tokens.length}\uE001`;
      tokens.push(html);
      return key;
    };
    let html = escapeHtml(code);
    html = html.replace(/("[^"\n]*"|'[^'\n]*')/g, (value) => hold(`<span class="tok-string">${value}</span>`));
    html = html.replace(/(\/\/.*|#.*)$/gm, (value) => hold(`<span class="tok-comment">${value}</span>`));
    const keywordSet = "const|let|var|function|return|if|else|for|while|switch|case|break|class|type|struct|func|package|import|from|export|async|await|try|catch|defer|go|select|range";
    html = html.replace(new RegExp(`\\b(${keywordSet})\\b`, "g"), '<span class="tok-keyword">$1</span>');
    return html.replace(/\uE000TOK(\d+)\uE001/g, (_, index) => tokens[Number(index)] || "");
  }

  function toolActivityRecordsForStack(stack) {
    const source = String(stack?.dataset?.toolActivitySource || "run");
    if (source === "live") return currentLiveToolOutputList();
    const runId = String(stack?.dataset?.runId || state.activeRunSummaryRunId || state.activeRunSummary?.run?.id || "");
    return activeRunToolCallList(state.activeRunSummary, runId);
  }

  function renderToolActivitySelection(stack, toolUseId) {
    if (!stack) return;
    const selected = String(toolUseId || "");
    stack.querySelectorAll?.("[data-tool-activity-select]").forEach((button) => {
      const active = String(button.dataset?.toolActivitySelect || "") === selected;
      button.setAttribute?.("aria-expanded", active ? "true" : "false");
      const label = String(button.dataset?.toolActivityLabel || "");
      button.setAttribute?.("aria-label", cr(active ? "activity.closeDetails" : "activity.openDetails", { tool: label }));
      button.closest?.(".tool-activity-step")?.classList?.toggle?.("selected", active);
    });
    const slot = stack.querySelector?.("[data-tool-activity-selected-detail]");
    if (!slot) return;
    if (!selected) {
      slot.innerHTML = "";
      return;
    }
    const record = toolActivityRecordsForStack(stack).find((item) => normalizeToolActivity(item).toolUseId === selected);
    slot.innerHTML = record
      ? renderToolActivityCardHTML(record, { resolveBackgroundTask, detailsExpanded: true })
      : `<div class="tool-activity-empty">${escapeHtml(cr("activity.detailUnavailable"))}</div>`;
  }

  function bindToolActivityControls(root) {
    if (!root?.addEventListener || !root.dataset || root.dataset.toolActivityControlsBound === "true") return;
    root.dataset.toolActivityControlsBound = "true";
    root.addEventListener("click", (event) => {
      const button = event.target?.closest?.("[data-tool-activity-select]");
      if (!button) return;
      const stack = button.closest?.("[data-tool-activity-stack]");
      const stackKey = String(stack?.dataset?.toolActivityStackKey || "");
      if (!stack || !stackKey) return;
      const requested = String(button.dataset?.toolActivitySelect || "");
      const next = nextToolActivitySelection(selectedToolActivity(stackKey), requested);
      setSelectedToolActivity(stackKey, next);
      renderToolActivitySelection(stack, next);
    });
  }

  // Every protected asset surface in a freshly rendered subtree: thumbnails need
  // their bytes fetched with the API token, download links need arming, and the
  // open buttons need to route to the lightbox instead of a doomed navigation.
  function bindProtectedImageSurfaces(root) {
    if (!root?.querySelectorAll) return;
    hydrateProtectedImages(root);
    bindProtectedDownloads(root);
    root.querySelectorAll("img.attachment-image-preview").forEach((image) => {
      image.addEventListener("error", () => {
        const placeholder = image.closest?.(".attachment-image-card")?.querySelector?.("[data-attachment-image-failed]");
        if (placeholder) placeholder.hidden = false;
        image.closest?.(".attachment-image-card")?.classList?.add?.("is-missing");
      }, { once: true });
    });
    if (root.dataset?.protectedImageClicksBound === "true") return;
    if (root.dataset) root.dataset.protectedImageClicksBound = "true";
    root.addEventListener?.("click", (event) => {
      const opener = event.target?.closest?.("[data-attachment-lightbox], [data-image-lightbox]");
      if (opener) {
        event.preventDefault?.();
        const url = opener.dataset?.attachmentLightbox || opener.dataset?.imageLightbox || "";
        const name = opener.dataset?.attachmentName || opener.dataset?.imageLightboxName || "";
        openImageLightbox({
          url,
          caption: opener.dataset?.imageLightboxCaption || name,
          downloadName: name,
          labels: { close: cr("attachment.closeViewer"), download: cr("imageGeneration.download") },
        }).then((opened) => {
          if (!opened) showToast(cr("attachment.unavailable"), "warn");
        });
        return;
      }
      // Non-image attachments are buttons rather than links for the same reason:
      // a plain href cannot carry the API token.
      const download = event.target?.closest?.("[data-attachment-download]");
      if (!download) return;
      event.preventDefault?.();
      downloadProtectedAttachment(download.dataset?.attachmentDownload || "", download.dataset?.attachmentName || "");
    });
  }

  async function downloadProtectedAttachment(url, filename) {
    if (!url) return;
    try {
      const objectURL = await loadProtectedImageURL(url);
      const anchor = document.createElement("a");
      anchor.href = objectURL;
      anchor.download = filename || "attachment";
      anchor.click();
    } catch {
      showToast(cr("attachment.unavailable"), "warn");
    }
  }

  function bindMessageActionButtons(root) {
    root.querySelectorAll("[data-generated-image] img.generated-image-preview").forEach((image) => {
      image.addEventListener("error", () => {
        const card = image.closest?.("[data-generated-image]");
        image.closest?.(".generated-image-open")?.setAttribute?.("hidden", "");
        const placeholder = card?.querySelector?.("[data-generated-image-missing]");
        if (placeholder) placeholder.hidden = false;
        card?.classList?.add?.("is-missing");
      }, { once: true });
    });
    bindProtectedImageSurfaces(root);
    root.querySelectorAll("[data-correct-message]").forEach((button) => {
      button.addEventListener("click", () => openCorrectionEditor(button.dataset.correctMessage || ""));
    });
    root.querySelector("[data-correction-cancel]")?.addEventListener("click", closeCorrectionEditor);
    root.querySelector("[data-correction-form]")?.addEventListener("submit", (event) => {
      event.preventDefault();
      submitCorrection(event.currentTarget).catch(showError);
    });
    root.querySelector("[data-correction-text]")?.addEventListener("input", (event) => {
      state.correctionText = event.target.value;
    });
    root.querySelector("[data-correction-files]")?.addEventListener("change", (event) => {
      state.correctionText = root.querySelector("[data-correction-text]")?.value ?? state.correctionText ?? "";
      state.correctionFiles = Array.from(event.target?.files || []).filter(Boolean);
      applyMessageSnapshot(state.currentMessages, state.agent?.id);
    });
    root.querySelector("[data-correction-text]")?.addEventListener("paste", (event) => {
      const files = correctionClipboardFiles(event);
      if (!files.length) return;
      state.correctionFiles = [...(state.correctionFiles || []), ...files];
      window.setTimeout(() => applyMessageSnapshot(state.currentMessages, state.agent?.id), 0);
    });
    root.querySelectorAll("[data-copy-message]").forEach((button) => {
      button.addEventListener("click", async () => {
        const index = Number(button.dataset.copyMessage || -1);
        const text = state.messageCopyTexts[index] || "";
        const original = button.textContent;
        if (text && await copyToClipboard(text)) {
          button.textContent = cr("message.copied");
          showToast(cr("message.copiedToast"), "success");
          notifyTerminal(`[info] ${cr("message.copiedToast")}\n`);
        } else {
          button.textContent = cr("message.copyFailed");
          showToast(cr("message.copyFailedToast"), "warn");
        }
        window.setTimeout(() => { button.textContent = original; }, 1200);
      });
    });
  }

  function bindCopyCodeButtons(root) {
    root.querySelectorAll(".copy-code").forEach((button) => {
      button.addEventListener("click", async () => {
        const ok = await copyToClipboard(button.dataset.code || "");
        const original = button.textContent;
        button.textContent = ok ? cr("code.copied") : cr("code.copyFailed");
        if (!ok) showToast(cr("code.copyFailedToast"), "warn");
        setTimeout(() => { button.textContent = original; }, 1200);
      });
    });
  }

  return {
    appendLiveAssistantText,
    appendLiveReasoning,
    appendToolOutput,
    clearLiveReasoning,
    closeLiveReasoningStep,
    applyMessageSnapshot,
    applyPlanEvent,
    beginLiveAssistantGeneration,
    clearCurrentAgentApprovals,
    clearLiveImageGenerations,
    clearPlanState,
    clearLiveAssistantText,
    clearMessageRefreshTimer,
    clearRunSummary,
    clearToolApproval,
    clearUserQuestion,
    copyCurrentConversationMarkdown,
    finishToolOutput,
    invalidateMessageLifecycle,
    loadLatestRunSummary,
    loadMessages,
    loadOlderMessages,
    loadRunSummary,
    performPlanAction,
    rememberImageGenerationStatus,
    rememberToolApproval,
    rememberToolStarted,
    rememberUserQuestion,
    refreshUserMessageIdentity,
    replacePendingApprovals,
    replacePendingUserQuestions,
    replacePlanState,
    scheduleMessageRefresh,
    updateConversationCopyButton,
    updateLiveAssistantPerformance,
  };
}
