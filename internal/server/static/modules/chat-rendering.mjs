import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatBytes, formatNumber, formatTimestamp } from "./formatters.mjs?v=message-thread-1";
import { t } from "./i18n.mjs";
import { api } from "./runtime.mjs";
import { visibleMessageText } from "./skills-commands.mjs";
import { normalizeAvatarDataUrl } from "./profile-avatar.mjs?v=profile-avatar-1";
import { t as cr } from "./messages-chat-rendering-extra.mjs?v=plan-mode-1-i18n-shared-1-subagent-cards-1-provider-errors-1-tool-activity-lazy-1-reasoning-count-1-per-message-activity-1";
import {
  bindProtectedDownloads,
  hydrateProtectedImages,
  loadProtectedImageURL,
  protectedDownloadAttribute,
  protectedImageAttribute,
} from "./protected-images.mjs?v=protected-images-1";
import { openImageLightbox } from "./image-lightbox.mjs?v=protected-images-1";
import { createStreamingMarkdown } from "./markdown-stream.mjs?v=markdown-stream-1";

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
  if (value === "completed") return ""; // completed is the default; no label needed
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

// Maps a raw tool name to a human-readable display title. Keeps the original
// as fallback so unknown tools still surface their actual name.
function friendlyToolName(toolName) {
  const name = String(toolName || "").toLowerCase().trim();
  if (name === "ls" || name === "listdirectory" || name === "list_directory") return "列出目录";
  if (name === "pwd") return "当前目录";
  if (name === "cat") return "读取文件";
  if (name === "mkdir") return "创建目录";
  if (name === "cp") return "复制文件";
  if (name === "mv") return "移动文件";
  if (name === "rm") return "删除文件";
  if (name === "touch") return "新建文件";
  if (name === "find") return "查找文件";
  if (name === "which") return "查找命令";
  if (name === "echo") return "输出文本";
  if (name === "curl" || name === "wget") return "网络请求";
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
function mergeDuplicateToolActivity(kept, duplicate) {
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

export function createChatRenderingController({
  state,
  attachmentIcon,
  attachmentKind,
  apiRequest = api,
  AbortControllerImpl = globalThis.AbortController,
  copyToClipboard,
  notifyTerminal,
  resolveBackgroundTask,
  retryLastRun,
  selectedModelValue,
  shortPath,
  showError,
  showToast,
} = {}) {
  const request = apiRequest || api;
  let messageLifecycleGeneration = 0;
  let messageLoadRequest = null;
  let olderMessagesRequest = null;
  // Tool activity of runs older than the newest one, fetched at most once per
  // run. The cap bounds a single sweep; scrolling further back requests the
  // next batch on the following snapshot.
  const historyRunActivityLimit = 8;
  const historyRunActivityRequests = new Set();
  let historyRunActivityInFlight = false;
  // Failure banners the user closed, by run id. Kept in memory only: dismissing
  // is about the current view, and a reload asking again is better than
  // permanently hiding why a run failed.
  const dismissedRunNotices = new Set();

  // -- Scroll-intent tracking --------------------------------------------------
  // We only scroll to the bottom when the user was already following the tail.
  // "Near bottom" means within 120px of the bottom edge so a partial line does
  // not break the follow behaviour.
  const NEAR_BOTTOM_PX = 120;

  function isNearBottom(el) {
    if (!el) return true; // no element → assume following
    return el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX;
  }

  // Whether the reader is following the newest content. This is deliberately a
  // remembered intent rather than something re-derived from the geometry after
  // each render.
  //
  // Deriving it after the fact is what made the transcript stop mid-answer: the
  // live card appends its text and *then* asks isNearBottom, by which point a
  // chunk larger than NEAR_BOTTOM_PX has already pushed the view off the
  // bottom. One 228px arrival was enough. The answer was then judged
  // "not following" for every render that followed, so the view froze while the
  // reply kept growing beneath it -- landing the reader partway up a long
  // answer with no way back except scrolling.
  //
  // Only the reader's own scrolling changes it. Programmatic scrolls land at
  // the bottom and so re-affirm it, which is harmless; the one path that
  // restores a saved offset runs solely when following is already off.
  let followingTail = true;
  let followTracked = null;
  let scrollSettleGeneration = 0;
  let jumpToLatestBound = null;

  // The floating "jump to latest" pill mirrors the follow intent: it appears
  // the moment the reader scrolls away from the tail and disappears once they
  // are following again, whether they scrolled back themselves or clicked it.
  function syncJumpToLatestButton() {
    const button = $("jumpToLatestBtn");
    if (!button?.classList) return;
    if (jumpToLatestBound !== button && typeof button.addEventListener === "function") {
      jumpToLatestBound = button;
      button.addEventListener("click", () => { scrollMessagesToBottom(); });
    }
    button.classList.toggle("hidden", followingTail);
  }

  function trackTranscriptFollow(el) {
    const target = el || $("messages");
    if (!target || followTracked === target || !target.addEventListener) return;
    followTracked = target;
    target.addEventListener("scroll", () => {
      followingTail = isNearBottom(target);
      syncJumpToLatestButton();
    }, { passive: true });
  }

  function setFollowingTail(value) {
    followingTail = Boolean(value);
    syncJumpToLatestButton();
  }

  function scrollToBottomIfFollowing(el) {
    trackTranscriptFollow(el);
    if (followingTail && el) el.scrollTop = el.scrollHeight;
  }

  function clampTranscriptScrollTop(el, value) {
    const top = Number(value);
    const scrollHeight = Number(el?.scrollHeight);
    const clientHeight = Number(el?.clientHeight);
    const max = Number.isFinite(scrollHeight) && Number.isFinite(clientHeight)
      ? Math.max(0, scrollHeight - Math.max(0, clientHeight))
      : Math.max(0, scrollHeight || 0);
    return Math.min(Math.max(0, Number.isFinite(top) ? top : 0), max);
  }

  function captureTranscriptView(el) {
    const target = el || $("messages");
    if (!target) return { scrollTop: 0, followingTail: true };
    trackTranscriptFollow(target);
    return {
      scrollTop: Number(target.scrollTop) || 0,
      followingTail,
    };
  }

  function restoreTranscriptView(view, el = $("messages")) {
    if (!el || !view) return;
    if (view.followingTail) {
      el.scrollTop = el.scrollHeight;
      return;
    }
    el.scrollTop = clampTranscriptScrollTop(el, view.scrollTop);
  }

  // Sending on mobile often changes two things in separate browser frames: the
  // textarea collapses and the on-screen keyboard starts closing. A single
  // synchronous scroll therefore gets overwritten by the viewport resize. Keep
  // this explicit helper for send flows and settle the tail after those layout
  // passes as well. It is intentionally unconditional because a new message is
  // an explicit request to follow the latest content.
  function scrollMessagesToBottom() {
    const el = $("messages");
    if (!el) return false;
    // An explicit jump to the newest content also restores following: the
    // reader asked to be at the end, so the next reply should keep them there.
    trackTranscriptFollow(el);
    setFollowingTail(true);
    const settleGeneration = ++scrollSettleGeneration;
    const scroll = () => {
      // The last of these runs 320ms out, so it can outlive the document that
      // scheduled it. Re-reading the node each time is deliberate -- a render
      // in between replaces it -- but the lookup itself has to be safe.
      if (!globalThis.document || settleGeneration !== scrollSettleGeneration) return;
      // A reader who scrolls away during the settle has overruled it. Without
      // this the queued passes would drag them back to the end up to 320ms
      // after they moved.
      if (!followingTail) return;
      const current = $("messages");
      if (current) current.scrollTop = current.scrollHeight;
    };
    scroll();
    const frame = globalThis.requestAnimationFrame;
    if (typeof frame === "function") {
      frame(() => {
        scroll();
        frame(scroll);
      });
    } else {
      globalThis.setTimeout?.(scroll, 0);
      globalThis.setTimeout?.(scroll, 120);
    }
    globalThis.setTimeout?.(scroll, 320);
    return true;
  }
  // ---------------------------------------------------------------------------

  // -- Scroll-to-load history --------------------------------------------------
  // Reading further back is a scroll, not a button press. The sentinel sits above
  // the oldest rendered message; bringing it into view loads the page before it.
  //
  // Loading starts before the sentinel is actually visible so the next page is
  // usually already in place by the time the user reaches it.
  const HISTORY_PRELOAD_PX = 320;
  // After this many consecutive failures, stop reloading on every scroll and let
  // the user retry deliberately via the fallback button.
  const HISTORY_FAILURE_LIMIT = 2;

  let historyObserver = null;
  let historyLoadFailures = 0;

  function historyAutoLoadAvailable() {
    return typeof globalThis.IntersectionObserver === "function" && historyLoadFailures < HISTORY_FAILURE_LIMIT;
  }

  function attachHistorySentinel(el, agentId) {
    historyObserver?.disconnect();
    if (!el || !historyAutoLoadAvailable()) return;
    // A load in flight already re-renders when it settles, and that render
    // re-attaches. Observing now would only fire against the request underway.
    if (state.messageOlderLoading) return;
    const sentinel = el.querySelector("[data-history-sentinel]");
    if (!sentinel) return;
    historyObserver = new globalThis.IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      // A page of history lands above the sentinel and pushes it back out of
      // view, but the callback can fire again before that render happens. Only
      // unobserving stops one scroll from requesting the same page twice.
      historyObserver?.disconnect();
      requestOlderMessages(agentId);
    }, { root: el, rootMargin: `${HISTORY_PRELOAD_PX}px 0px 0px 0px` });
    historyObserver.observe(sentinel);
  }

  function requestOlderMessages(agentId) {
    loadOlderMessages(agentId)
      .then((loaded) => {
        if (loaded) historyLoadFailures = 0;
      })
      .catch((err) => {
        historyLoadFailures += 1;
        // The fallback button only appears once the render below re-evaluates
        // historyAutoLoadAvailable(), so surface the failure and re-render.
        showError(err);
        if (historyLoadFailures >= HISTORY_FAILURE_LIMIT) applyMessageSnapshot(state.currentMessages, agentId, { preserveScroll: true });
      });
  }
  // ---------------------------------------------------------------------------

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
    // Failures belong to the conversation that produced them: a new one starts
    // with scroll-to-load enabled rather than inheriting the old fallback.
    historyLoadFailures = 0;
    historyObserver?.disconnect();
    return messageLifecycleGeneration;
  }

  // Sending used to clear the composer and then show nothing until two round
  // trips had finished: the POST, and the full transcript reload after it. For
  // 1-2 seconds the text existed nowhere on screen, so the send read as a stall.
  // This paints the user's own turn immediately from local data.
  //
  // Nothing here needs to reconcile with the server copy. applyMessageSnapshot
  // assigns state.currentMessages wholesale, so the authoritative snapshot
  // replaces this entry rather than sitting beside it, and the optimistic id is
  // never sent anywhere. Attachments are described only well enough to render a
  // count, because the real rows arrive with the snapshot moments later.
  function echoPendingUserMessage(agentId, text, attachments = []) {
    if (!agentId || state.agent?.id !== agentId) return "";
    const trimmed = String(text || "").trim();
    const files = Array.isArray(attachments) ? attachments : [];
    if (!trimmed && !files.length) return "";
    const id = `optimistic-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
    state.optimisticUserMessageId = id;
    const echo = {
      id,
      role: "user",
      // contentText, not content: visibleMessageText reads contentText, and
      // isTranscriptMessageVisible drops a message whose text is empty, so the
      // wrong field here renders nothing at all.
      contentText: trimmed,
      createdAt: new Date().toISOString(),
      optimistic: true,
      attachments: files.map((item, index) => ({
        id: `${id}-attachment-${index}`,
        name: String(item?.file?.name || ""),
        mimeType: String(item?.file?.type || ""),
        size: Number(item?.file?.size || 0),
      })),
    };
    // The previous turn's outcome card goes now, not when the next run starts.
    // state.activeRunSummary still described the finished run at this point, so the
    // snapshot below used to paint that card -- tool activity, omitted-count note
    // and "load earlier tool calls" button -- one more time, and it stayed on
    // screen until agent.started cleared it. The card was already destined to be
    // replaced, so showing it again for that gap only read as a flicker.
    //
    // preserveView because applyMessageSnapshot repaints the whole transcript on
    // the next line; asking clearRunSummary to render as well would paint twice.
    clearRunSummary({ preserveView: true });
    applyMessageSnapshot([...(state.currentMessages || []), echo], agentId, { forceRender: true });
    scrollMessagesToBottom();
    return id;
  }

  // Called when the POST failed. The turn never reached the server, so leaving the
  // echo on screen would claim a message was sent that was not.
  function discardPendingUserMessage(agentId = state.agent?.id, id = state.optimisticUserMessageId) {
    if (!id) return false;
    state.optimisticUserMessageId = "";
    if (!agentId || state.agent?.id !== agentId) return false;
    const remaining = (state.currentMessages || []).filter((message) => message?.id !== id);
    if (remaining.length === (state.currentMessages || []).length) return false;
    applyMessageSnapshot(remaining, agentId, { forceRender: true });
    return true;
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
    // Mirrors the backend transition rule: replan is a 409 from these states,
    // so the button and the feedback box should not be offered at all.
    const replannable = !["executing", "executed", "cancelled"].includes(status);
    const feedbackDraft = plan.id ? String(state.planFeedbackDrafts?.[plan.id] || "") : "";
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
        ${replannable ? `
        <div class="plan-card-feedback">
          <label for="plan-feedback-${escapeAttr(plan.id)}">${escapeHtml(cr("plan.feedbackLabel"))}</label>
          <textarea id="plan-feedback-${escapeAttr(plan.id)}" data-plan-feedback="${escapeAttr(plan.id)}" rows="2" placeholder="${escapeAttr(cr("plan.feedbackPlaceholder"))}" ${busy ? "disabled" : ""}>${escapeHtml(feedbackDraft)}</textarea>
        </div>` : ""}
        <div class="plan-card-actions">
          ${pending ? `<button class="ghost-btn mini" type="button" data-plan-action="approve" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.approve"))}</button>` : ""}
          ${executable ? `<button class="ghost-btn mini primary" type="button" data-plan-action="execute" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(busy ? cr("plan.working") : cr("plan.execute"))}</button>` : ""}
          ${cancellable ? `<button class="ghost-btn mini danger" type="button" data-plan-action="cancel" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.cancel"))}</button>` : ""}
          ${replannable ? `<button class="ghost-btn mini" type="button" data-plan-action="replan" data-plan-id="${escapeAttr(plan.id)}" ${busy ? "disabled" : ""}>${escapeHtml(cr("plan.replan"))}</button>` : ""}
        </div>
      </section>
    `;
  }

  function renderPlanCards() {
    if (state.chatHydrating || !state.agent?.id) return;
    // Plan state can change at the end of a run while the reader is looking at
    // history. Let the normal follow intent decide whether to stay at the tail;
    // forceRender here used to pull that reader back to the newest message.
    applyMessageSnapshot(state.currentMessages, state.agent.id);
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
    // Replanning carries the reviewer's notes so the next plan revision can
    // address them instead of guessing why the previous one was rejected.
    const feedback = action === "replan" ? String(state.planFeedbackDrafts?.[planId] || "").trim() : "";
    renderPlanCards();
    try {
      const result = await request(`/api/agents/${encodeURIComponent(agentId)}/plans/${encodeURIComponent(planId)}/${encodeURIComponent(action)}`, {
        method: "POST",
        body: JSON.stringify(feedback ? { revision: plan.revision, comment: feedback } : { revision: plan.revision }),
      });
      if (action === "replan" && state.planFeedbackDrafts?.[planId] !== undefined) {
        const drafts = { ...state.planFeedbackDrafts };
        delete drafts[planId];
        state.planFeedbackDrafts = drafts;
      }
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
    // The card is re-rendered on every plan event, which would wipe whatever
    // the reviewer has typed. The draft therefore lives in state, keyed by
    // plan id, and is written back into the textarea on each render.
    root.querySelectorAll("[data-plan-feedback]").forEach((input) => {
      input.addEventListener("input", () => {
        const planId = input.dataset.planFeedback || "";
        if (!planId) return;
        state.planFeedbackDrafts = { ...(state.planFeedbackDrafts || {}), [planId]: input.value };
      });
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
    if (html) scrollToBottomIfFollowing(el);
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
    // Earlier runs in this window may still be missing their tool history. The
    // fetch repaints the stacks in place when it lands, so it never blocks the
    // paint below.
    ensureHistoryRunActivity(agentId).catch(() => {});
    state.messageCopyTexts = visibleMessages.map(transcriptMessageText);
    updateConversationCopyButton();
    if (state.chatHydrating && options.forceRender !== true) return true;
    if (!el) return true;
    // A full snapshot replaces the container's children and the browser resets
    // scrollTop as a side effect. Capture both the reader's explicit offset and
    // follow intent before doing any DOM work, and retire older delayed tail
    // settles so they cannot run against this newer snapshot.
    const savedScrollTop = Number(el.scrollTop) || 0;
    trackTranscriptFollow(el);
    const wasFollowing = followingTail;
    scrollSettleGeneration += 1;
    el.removeAttribute?.("aria-busy");
    if (el.dataset) delete el.dataset.initialChatState;
    const liveAssistantCard = renderLiveAssistantCardHTML();
    const liveImageGenerationCards = renderLiveImageGenerationCardsHTML();
    const planCards = renderPlanCardsHTML();
    // Per-message stacks are computed first: whatever finds a home under its own
    // assistant turn is subtracted from both the live tail and the run-level card,
    // so a call is never shown in more than one place.
    const { stacks: messageActivityStacks, ownedToolUseIds } = messageToolActivityStacks(visibleMessages);
    const liveToolCards = renderLiveToolOutputCardsHTML(ownedToolUseIds);
    const runSummaryCard = renderRunSummaryCardHTML(ownedToolUseIds);
    const approvalCards = renderApprovalCardsHTML();
    if (!visibleMessages.length && !liveAssistantCard && !liveImageGenerationCards && !planCards && !liveToolCards && !runSummaryCard && !approvalCards && !messageActivityStacks.size) {
      el.classList.add("empty");
      el.innerHTML = `<div class="empty-conversation-state">${escapeHtml(cr("message.empty"))}</div>`;
      el.scrollTop = 0;
      return true;
    }
    el.classList.remove("empty");
    // History loads by scrolling into it rather than by pressing a button. The
    // button remains as the fallback for two cases that cannot rely on scroll:
    // no IntersectionObserver, and repeated load failures (where retrying on
    // every scroll would hammer a failing endpoint).
    const olderMessagesControl = state.messageHasMoreBefore ? `
      <div class="message-history-control" data-history-sentinel>
        <span class="message-history-status" role="status"${state.messageOlderLoading ? "" : " hidden"}>正在加载更早消息…</span>
        <button class="ghost-btn mini" type="button" data-load-older-messages${historyAutoLoadAvailable() ? " hidden" : ""}>
          加载更早消息
        </button>
      </div>
    ` : "";
    // Tail order is a contract shared with the incremental inserters below: the
    // run scaffolding (image generation, plan, live tool output, run outcome)
    // sits above the assistant's own words, so the newest thing the assistant
    // said is the last message in the thread. Only approval prompts, which the
    // user has to act on, render below it.
    //
    // The run summary card (tool activity) is an exception: it belongs *after*
    // the user message that triggered the run and *before* the assistant reply
    // that closed it, not at the tail of the thread.
    //
    // Once a run is terminal, anchor its persisted outcome immediately after
    // the triggering user message. Active runs: anchor the live tool stack at
    // the same position so the order is always: user msg → tools → assistant.
    const anchorRunSummary = isTerminalRunStatus(state.activeRunSummary?.run?.status);
    let messagesHTML = "";
    let runCardInserted = false;
    let liveToolsInserted = false;
    // Track the last user-message index as a fallback insertion point.
    let lastUserMessageIndex = -1;
    const needsRunAnchor = anchorRunSummary && runSummaryCard;
    const needsLiveAnchor = !anchorRunSummary && liveToolCards;
    // The outcome anchor resolves the trigger message, the newest user turn, and
    // the newest assistant turn in one pass, so it is computed whenever the card
    // needs a home rather than only when the trigger id is missing.
    const runOutcomeTarget = needsRunAnchor ? runOutcomeAnchor(visibleMessages) : null;
    if (needsLiveAnchor) {
      visibleMessages.forEach((message, index) => {
        if (userMessageRoles.has(chatMessagePresentation(message).normalizedRole)) {
          lastUserMessageIndex = index;
        }
      });
    }
    visibleMessages.forEach((message, index) => {
      // Activity is emitted outside renderChatMessageCached because the message
      // HTML cache is keyed on the message alone and would go stale as activity
      // arrives.
      //
      // Either way round the transcript must read user message → activity → AI
      // answer, and which side of its anchor the stack goes on depends on what
      // that anchor is. An assistant turn owns the reasoning and tool calls that
      // produced its words, so its stack leads it; appending made the answer
      // look like the cause of the work behind it. A stack anchored to a user
      // row instead belongs to an earlier run that row triggered, so it trails.
      const activityHTML = messageActivityStacks.get(String(message.id || "")) || "";
      // The activity timeline describes the work that leads to an assistant
      // answer, so keep it immediately before that answer on every responsive
      // layout. This matches the desktop transcript order on mobile as well.
      const activityLeadsMessage = chatMessagePresentation(message).normalizedRole === "assistant";
      // The outcome card leads an assistant anchor for the same reason the
      // per-message stack does: the work came before the words. Emitting it here,
      // ahead of both, keeps question → work → answer intact even when the anchor
      // is the reply itself.
      if (runSummaryCard && !runCardInserted && runOutcomeTarget?.position === "beforebegin" && message.id === runOutcomeTarget.messageId) {
        messagesHTML += runSummaryCard;
        runCardInserted = true;
      }
      if (activityLeadsMessage) messagesHTML += activityHTML;
      messagesHTML += renderChatMessageCached(message, index);
      if (!activityLeadsMessage) messagesHTML += activityHTML;
      if (runSummaryCard && !runCardInserted && runOutcomeTarget?.position === "afterend" && message.id === runOutcomeTarget.messageId) {
        messagesHTML += runSummaryCard;
        runCardInserted = true;
      }
      // For active runs anchor live tool cards after the last user message so
      // the order is: user message → tool activity → assistant reply.
      if (liveToolCards && !liveToolsInserted && !anchorRunSummary) {
        if (index === lastUserMessageIndex) {
          messagesHTML += liveToolCards;
          liveToolsInserted = true;
        }
      }
    });
    // Active runs and any outcome whose anchor was not found go in the tail.
    // Live records owned by a terminal run summary are already filtered out in
    // currentLiveToolOutputList, so whatever is left here still needs a home;
    // dropping it would lose tool activity instead of de-duplicating it.
    // ...with one exception: a run whose own turn is off screen must not take the
    // tail, because the tail now belongs to a newer question. Placing it there put
    // a finished run's tool activity under a message it never ran for, which is the
    // stale "load earlier tool calls" strip that appeared under a freshly sent
    // message and vanished when the next summary arrived.
    const tailRunSummaryCard = runCardInserted || (needsRunAnchor && !runOutcomeTarget && runOutcomeHomeIsOffScreen(visibleMessages))
      ? ""
      : runSummaryCard;
    const tailLiveToolCards = liveToolsInserted ? "" : liveToolCards;
    // Preserve the user's manual open/collapsed state for the live tool
    // activity group across the full innerHTML replacement. renderLiveToolOutputCards
    // already does this on incremental updates; applyMessageSnapshot must too,
    // otherwise a subagent refresh that calls applyMessageSnapshot collapses a
    // panel the user explicitly expanded.
    const savedLiveStackOpen = el.querySelector("[data-live-tool-output-stack] details.tool-activity-group")?.open ?? null;
    el.innerHTML = `${olderMessagesControl}${messagesHTML}${liveImageGenerationCards}${planCards}${tailLiveToolCards}${tailRunSummaryCard}${liveAssistantCard}${approvalCards}`;
    if (savedLiveStackOpen !== null) {
      const restoredDetails = el.querySelector("[data-live-tool-output-stack] details.tool-activity-group");
      if (restoredDetails && restoredDetails.open !== savedLiveStackOpen) restoredDetails.open = savedLiveStackOpen;
    }
    const liveMessageIds = new Set(visibleMessages.map((message) => message.id).filter(Boolean));
    for (const cachedId of messageHtmlCache.keys()) {
      if (!liveMessageIds.has(cachedId)) messageHtmlCache.delete(cachedId);
    }
    bindToolActivityControls(el);
    bindMessageActionButtons(el);
    el.querySelector("[data-load-older-messages]")?.addEventListener("click", () => {
      historyLoadFailures = 0;
      loadOlderMessages(agentId).catch(showError);
    });
    // Every render replaces this container's children, so the sentinel observed
    // a moment ago is now a detached node. Re-observing here is what keeps
    // scroll-to-load working after a tool finishes, a run summary updates, or
    // any other mid-conversation rebuild.
    attachHistorySentinel(el, agentId);
    bindRunSummaryButtons(el);
    bindPlanButtons(el);
    bindApprovalButtons(el);
    bindCopyCodeButtons(el);
    // forceRender means a fresh conversation was just opened — always scroll to
    // the tail so the user lands at the latest message, not the top of history.
    // Otherwise, only a reader who was already following the tail should move;
    // a reader who deliberately left it keeps the same visible offset even
    // though the full snapshot rebuilt every child node.
    //
    // preserveScroll outranks forceRender. The two answer different questions:
    // forceRender is about rebuilding the DOM even when the markup is unchanged,
    // preserveScroll is about leaving the viewport alone. Testing forceRender
    // first meant a caller asking for both had its scroll request silently
    // dropped, and the one caller that does that -- the subagent refresh -- runs
    // repeatedly while the answer streams. Every background-task update then
    // yanked the transcript to the tail, pulling the conversation upward under a
    // reader who had deliberately scrolled back.
    const shouldFollowTail = !options.preserveScroll && (options.forceRender === true || wasFollowing);
    if (shouldFollowTail) {
      // One synchronous assignment only reaches the tail if the transcript has
      // finished growing, and after a full rebuild it has not: activity stacks,
      // code blocks and images settle over the next few frames, and every pixel
      // they add lands below the viewport.
      //
      // This applies to the rebuild at the end of a turn just as much as to
      // opening a conversation. The live card is swapped for the persisted
      // message, the single scroll lands against the pre-layout height, and the
      // content then grows past it -- leaving the reader above the end, looking
      // at an earlier part of the conversation. Settle across those passes in
      // both cases; the deferred passes stop early if the reader scrolls away.
      scrollMessagesToBottom();
    } else if (options.preserveScroll || !wasFollowing) {
      el.scrollTop = clampTranscriptScrollTop(el, savedScrollTop);
    }
    return true;
  }

  function renderPerformanceHTML(turnUsage, { live = false } = {}) {
    const text = formatTurnUsagePerformance(turnUsage);
    if (!text) return "";
    const usage = normalizeTurnUsage(turnUsage);
    return `<div class="message-performance${live ? " message-performance-live" : ""}${usage.estimated ? " is-estimated" : ""}" aria-label="${escapeAttr(text)}">${escapeHtml(text)}</div>`;
  }

  // Streaming answers are split into a settled prefix and a volatile tail so a
  // long answer stops re-parsing everything it has already said on every chunk.
  const streamingMarkdown = createStreamingMarkdown({
    renderMarkdown,
    renderOpenFence: ({ lang, code }) => codeBlockHTML(code, lang),
  });

  function renderLiveAssistantCardHTML() {
    const text = String(state.liveAssistantText || "");
    if (!text) return "";
    const logoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" style="width:14px;height:14px;display:block;"><circle cx="32" cy="32" r="25"/><path d="M21 35c3.2 4 6.8 6 11 6s7.8-2 11-6"/><path d="M23 25h.02M41 25h.02"/></svg>`;
    return `
      <div class="message assistant live-assistant-message chat-message chat-flow-item chat-flow-left" data-chat-alignment="left" data-live-assistant data-run-id="${escapeAttr(state.liveAssistantRunId || "")}" data-request-id="${escapeAttr(state.liveAssistantRequestId || "")}" data-started-at="${escapeAttr(state.liveAssistantStartedAt || "")}">
        <div class="message-head">
          <div class="message-meta"><span class="message-avatar message-avatar-logo" aria-hidden="true">${logoSVG}</span><div class="message-role">Autoto</div></div>
          ${renderPerformanceHTML(state.liveAssistantPerformance, { live: true })}
        </div>
        <div class="message-content">${liveAssistantContentHTML(text)}</div>
      </div>
    `;
  }

  // The two containers are what let a chunk touch only the tail: everything in
  // the settled half is already in the DOM and is never rewritten.
  function liveAssistantContentHTML(text) {
    const { stableHTML, tailHTML } = streamingMarkdown.update(text);
    return `<div data-md-stable>${stableHTML}</div><div data-md-tail>${tailHTML}</div>`;
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
      ? `${state.correctionText ?? ""}\u0000${(Array.isArray(state.correctionFiles) ? state.correctionFiles : []).map((file) => file?.name || "").join(",")}`
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
    const isAssistant = presentation.normalizedRole === "assistant";
    const logoSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false" style="width:14px;height:14px;display:block;"><circle cx="32" cy="32" r="25"/><path d="M21 35c3.2 4 6.8 6 11 6s7.8-2 11-6"/><path d="M23 25h.02M41 25h.02"/></svg>`;
    const avatarLabel = isAssistant ? "" : (presentation.role.slice(0, 1).toUpperCase() || "•");
    const avatarHTML = usesProfileIdentity ? profileAvatarHTML(profileIdentity) : (isAssistant ? logoSVG : escapeHtml(avatarLabel));
    const roleLabel = isAssistant ? "Autoto" : (profileIdentity?.displayName || presentation.role);
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
      <div class="message ${presentation.roleClass}${editing ? " message-editing" : ""}${superseded ? " message-superseded" : ""} chat-message chat-flow-item chat-flow-${presentation.alignment}" data-chat-alignment="${presentation.alignment}" data-message-role="${escapeAttr(presentation.normalizedRole)}" data-message-id="${escapeAttr(message.id || "")}">
        <div class="message-head">
          <div class="message-meta"><span class="message-avatar" aria-hidden="true"${profileAvatarAttr}>${avatarHTML}</span><div class="message-role">${roleHTML}</div></div>
          <div class="message-head-actions">${actions}</div>
          ${timeHTML}
        </div>
        ${editing ? renderCorrectionEditor(message) : `<div class="message-content">${renderMarkdown(friendlyMessageText(transcriptMessageText(message)))}</div>${presentation.normalizedRole === "assistant" ? renderGeneratedImageBlocksHTML(message, state.agent?.id || "") : ""}${renderMessageAttachments(message)}`}
        ${presentation.normalizedRole === "assistant" ? renderPerformanceHTML(message.turnUsage) : ""}
      </div>
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
    if (!String(state.liveAssistantText || "")) {
      existing?.remove();
      if (!transcriptMessages(state.currentMessages).length && !renderLiveImageGenerationCardsHTML() && !renderPlanCardsHTML() && !renderLiveToolOutputCardsHTML() && !renderRunSummaryCardHTML() && !renderApprovalCardsHTML()) {
        el.classList.add("empty");
        el.innerHTML = `<div class="empty-conversation-state">${escapeHtml(cr("message.empty"))}</div>`;
      }
      return;
    }
    el.classList.remove("empty");
    // Growing text is the common case by far, and rebuilding the whole card for
    // it discards and re-parses DOM that has not changed. When the card is
    // already on screen for this same run, update the tail in place instead.
    //
    // This is attempted before any markup is built: both paths advance the same
    // streaming renderer, so building the full card first would consume the
    // settled fragment this path needs to append.
    if (existing && updateLiveAssistantContentInPlace(existing)) {
      scrollToBottomIfFollowing(el);
      return;
    }
    const html = renderLiveAssistantCardHTML();
    if (!html) {
      existing?.remove();
      return;
    }
    if (existing) existing.outerHTML = html;
    else {
      const approvalStack = el.querySelector("[data-approval-stack]");
      if (approvalStack) approvalStack.insertAdjacentHTML("beforebegin", html);
      else el.insertAdjacentHTML("beforeend", html);
    }
    bindCopyCodeButtons(el);
    scrollToBottomIfFollowing(el);
  }

  // Returns false when the card on screen cannot be updated incrementally (a
  // different run, or markup from before this path existed), leaving the caller
  // to fall back to a full rebuild.
  function updateLiveAssistantContentInPlace(card) {
    if (card.dataset.runId !== String(state.liveAssistantRunId || "")) return false;
    if (card.dataset.requestId !== String(state.liveAssistantRequestId || "")) return false;
    const stable = card.querySelector("[data-md-stable]");
    const tail = card.querySelector("[data-md-tail]");
    if (!stable || !tail) return false;

    const { tailHTML, stableDeltaHTML, recomputed } = streamingMarkdown.update(String(state.liveAssistantText || ""));
    // A replacement invalidates the settled DOM; anything else only ever adds to
    // it, so the newly settled fragment is appended rather than re-serialising a
    // prefix that is already on screen and already correct.
    if (recomputed) stable.innerHTML = stableDeltaHTML;
    else if (stableDeltaHTML) stable.insertAdjacentHTML("beforeend", stableDeltaHTML);
    if (stableDeltaHTML) bindCopyCodeButtons(stable);
    tail.innerHTML = tailHTML;
    bindCopyCodeButtons(tail);
    return true;
  }

  function liveAssistantEventMatches(detail = {}) {
    const requestId = String(detail.requestId || "");
    const runId = String(detail.runId || "");
    if (requestId && state.liveAssistantRequestId && requestId !== state.liveAssistantRequestId) return false;
    if (runId && state.liveAssistantRunId && runId !== state.liveAssistantRunId) return false;
    return true;
  }

  function beginLiveAssistantGeneration(detail = {}) {
    streamingMarkdown.reset();
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
    // An empty delta cannot start an answer. Treating one as the first chunk of
    // a new generation resets the live text to "" and repaints, and the repaint's
    // empty-text branch removes the streamed card that a finishing turn is still
    // deliberately showing -- the transcript loses that card's height and the
    // reader is dropped up the page at the exact moment the reply lands.
    //
    // A trailing empty delta after completion is the common way in: the turn end
    // sets liveAssistantActive to false, so the next event looks like the start
    // of something new.
    if (!delta && !state.liveAssistantActive) return false;
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
    streamingMarkdown.reset();
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
    clearHistoryRunActivity();
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
    retainActiveRunActivityAsHistory();
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
    retainActiveRunActivityAsHistory(runId);
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
      // Terminal summaries take ownership of the activity list. Drop the live
      // copy *before* painting so the first paint after restore is not a pair
      // of identical stacks (live + persisted).
      if (isTerminalRunStatus(summary?.run?.status)) {
        clearLiveToolOutputs({ agentId, preserveView: true });
      }
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

  // Repaints the per-message activity stacks in place, anchored after the
  // message each one belongs to. Called by both incremental paths (live tool
  // events and run-summary loads) because a single tool call can move between
  // the two lists as a run finishes, which changes who owns it.
  function syncMessageToolActivityStacks(el) {
    const root = el || $("messages");
    if (!root?.querySelectorAll) return new Set();
    const { stacks, ownedToolUseIds } = messageToolActivityStacks(transcriptMessages(state.currentMessages));
    root.querySelectorAll("[data-message-activity]").forEach((node) => {
      const messageId = String(node.dataset?.messageActivity || "");
      const html = stacks.get(messageId);
      if (html) {
        // Preserve <details> open/collapsed state so that a history-stack sync
        // does not re-expand a card the user manually collapsed.
        const detailsOpen = node.querySelector("details.tool-activity-group")?.open ?? null;
        node.outerHTML = html;
        stacks.delete(messageId);
        if (detailsOpen !== null) {
          const replaced = root.querySelector(`[data-message-activity="${cssIdentifierEscape(messageId)}"] details.tool-activity-group`);
          if (replaced && replaced.open !== detailsOpen) replaced.open = detailsOpen;
        }
        return;
      }
      node.remove();
    });
    for (const [messageId, html] of stacks) {
      const anchor = root.querySelector(`[data-message-id="${cssIdentifierEscape(messageId)}"]`);
      // Same placement rule as the full rebuild, so a repaint never moves a
      // stack: an assistant turn's own work leads its words, while a stack
      // anchored to a user row belongs to an earlier run that row triggered and
      // trails it. Both read user message → activity → AI answer.
      if (!anchor) continue;
      // Keep incremental updates aligned with the full transcript renderer:
      // an assistant turn's activity leads its answer, while activity anchored
      // to a user message trails that user message.
      const anchorIsAssistant = String(anchor.dataset?.messageRole || "") === "assistant";
      anchor.insertAdjacentHTML(anchorIsAssistant ? "beforebegin" : "afterend", html);
    }
    return ownedToolUseIds;
  }

  // The run-level outcome card describes the work between a question and its
  // answer, so it has exactly one correct home: after the message that triggered
  // the run, and before the reply that closed it. Both paint paths resolve that
  // home here so neither can drift from the other.
  //
  // The trigger id is only usable when that message is actually on screen. A
  // conversation opened at its tail may not have loaded the trigger yet, and the
  // previous code had no fallback for that case: the card fell through to the
  // container tail, i.e. below the assistant's answer. Hence the ladder --
  // trigger, then the newest user turn, then the newest assistant turn (which
  // the card leads rather than trails).
  // True when the transcript holds messages but the newest user turn belongs to a
  // different run than the card being placed. That is the superseded case, and it
  // is what separates "no anchor yet" from "no anchor ever".
  function runOutcomeHomeIsOffScreen(visibleMessages) {
    const messages = Array.isArray(visibleMessages) ? visibleMessages : [];
    if (!messages.length) return false;
    const run = state.activeRunSummary?.run;
    const cardRunId = String(state.activeRunSummaryRunId || run?.id || "");
    if (!cardRunId) return false;
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];
      if (!userMessageRoles.has(chatMessagePresentation(message).normalizedRole)) continue;
      const runId = String(message?.runId || message?.run_id || "");
      return Boolean(runId) && runId !== cardRunId;
    }
    return false;
  }

  function runOutcomeAnchor(visibleMessages) {
    const messages = Array.isArray(visibleMessages) ? visibleMessages : [];
    if (!messages.length) return null;
    const run = state.activeRunSummary?.run;
    const triggerId = String(run?.triggerMessageId || run?.trigger_message_id || "");
    const cardRunId = String(state.activeRunSummaryRunId || run?.id || "");
    let lastUser = null;
    let lastUserRunId = "";
    let lastAssistant = null;
    for (const message of messages) {
      const messageId = String(message?.id || "");
      if (!messageId) continue;
      const role = chatMessagePresentation(message).normalizedRole;
      if (triggerId && messageId === triggerId) {
        return { messageId, position: userMessageRoles.has(role) ? "afterend" : "beforebegin" };
      }
      if (userMessageRoles.has(role)) {
        lastUser = messageId;
        lastUserRunId = String(message?.runId || message?.run_id || "");
      } else if (role === "assistant") lastAssistant = messageId;
    }
    // The fallbacks below assume this run is the newest turn, which is what makes
    // "the last user message" a sane home for its card. In a long conversation the
    // trigger can sit outside the loaded window, and then the assumption breaks:
    // the newest user message is the one just submitted for the *next* run, so a
    // finished run's activity was attached under a question it had never seen.
    // That is the flash of "load earlier tool calls" under a fresh message, which
    // cleared itself once the new run's summary replaced the card.
    //
    // A user turn that already belongs to another run is therefore disqualifying:
    // this card's real home is off screen, and showing nothing is right until the
    // trigger loads.
    if (lastUser && cardRunId && lastUserRunId && lastUserRunId !== cardRunId) return null;
    if (lastUser) return { messageId: lastUser, position: "afterend" };
    if (lastAssistant) return { messageId: lastAssistant, position: "beforebegin" };
    return null;
  }

  // Places a freshly built outcome card in the live DOM using the same anchor the
  // full repaint uses. Before this existed the incremental path had no anchor at
  // all: it looked for the streaming assistant card, and once a run finished that
  // card was already gone, so every terminal run appended its activity to the
  // very bottom of the transcript -- underneath the answer it belonged above.
  function insertRunOutcomeCard(el, html) {
    const messages = transcriptMessages(state.currentMessages);
    const target = runOutcomeAnchor(messages);
    // A card whose run owns no message on screen has no home. The tail fallback
    // below is for a transcript that has not painted yet, not for a run that has
    // been superseded, so taking it would drop this run's activity under whatever
    // happens to be last -- typically the question that started the next run.
    if (!target && runOutcomeHomeIsOffScreen(messages)) return;
    const anchor = target ? el.querySelector(`[data-message-id="${cssIdentifierEscape(target.messageId)}"]`) : null;
    if (anchor) {
      // An assistant anchor already has its own per-message activity stack
      // rendered immediately above it. Going in before that stack keeps the run
      // card above both, rather than wedged between a turn's work and its words.
      if (target.position === "beforebegin") {
        const stack = el.querySelector(`[data-message-activity="${cssIdentifierEscape(target.messageId)}"]`);
        (stack || anchor).insertAdjacentHTML("beforebegin", html);
        return;
      }
      anchor.insertAdjacentHTML("afterend", html);
      return;
    }
    // No message anchor at all (an empty or not-yet-painted transcript): keep the
    // previous behaviour of sitting above the streaming reply and approvals.
    const fallback = el.querySelector("[data-live-assistant], [data-approval-stack]");
    if (fallback) fallback.insertAdjacentHTML("beforebegin", html);
    else el.insertAdjacentHTML("beforeend", html);
  }

  // Message ids are server-generated, but they still reach a selector here, so
  // they go through CSS.escape when the platform provides it.
  function cssIdentifierEscape(value) {
    const text = String(value || "");
    if (typeof globalThis.CSS?.escape === "function") return globalThis.CSS.escape(text);
    return text.replace(/["\\]/g, "\\$&");
  }

  function renderRunSummaryCard() {
    if (state.chatHydrating) return;
    const el = $("messages");
    if (!el) return;
    // Keep the current review card stable while a refresh is in flight. Rendering
    // the transient loading status here makes context switches visibly flash.
    if (state.runSummaryLoading) return;
    const view = captureTranscriptView(el);
    const owned = syncMessageToolActivityStacks(el);
    const existing = el.querySelector("[data-run-summary-card], [data-run-outcome-card]");
    const html = renderRunSummaryCardHTML(owned);
    if (existing) {
      if (html) existing.outerHTML = html;
      else existing.remove();
    } else if (html) {
      if (el.classList.contains("empty")) {
        el.classList.remove("empty");
        el.innerHTML = html;
      } else {
        insertRunOutcomeCard(el, html);
      }
    }
    restoreTranscriptView(view, el);
    bindToolActivityControls(el);
    if (!html) return;
    bindRunSummaryButtons(el);
  }

  function renderRunSummaryCardHTML(ownedToolUseIds = null) {
    const summary = state.activeRunSummary;
    const run = summary?.run;
    const runId = state.activeRunSummaryRunId || run?.id || "";
    if (!run) {
      if (!state.runSummaryError || state.runSummaryLoading) return "";
      return renderRunSummaryLoadErrorHTML();
    }
    // Project runs use the same compact outcome as conversation runs so their
    // tool history and failure notice survive navigation. The old metrics,
    // message preview, and git controls remain absent; those live in dedicated
    // project surfaces instead.
    const toolCalls = activeRunToolCallList(summary, runId);
    return renderConversationRunOutcomeHTML(summary, run, runId, toolCalls, ownedToolUseIds);
  }

  function activeRunToolCallList(summary, runId) {
    return state.activeRunToolCallsRunId === runId && Array.isArray(state.activeRunToolCalls)
      ? state.activeRunToolCalls
      : (Array.isArray(summary?.toolCalls) ? summary.toolCalls : []);
  }

  // Only the newest run arrives with the run summary, so every earlier turn in
  // the transcript used to lose its tool history the moment the next run
  // started. Earlier runs are terminal and immutable, so their activity is
  // fetched once per run and kept for as long as the conversation stays open.
  function historyRunActivityMap() {
    if (!state.historyRunToolCalls || typeof state.historyRunToolCalls !== "object" || Array.isArray(state.historyRunToolCalls)) {
      state.historyRunToolCalls = {};
    }
    return state.historyRunToolCalls;
  }

  function clearHistoryRunActivity() {
    state.historyRunToolCalls = {};
    historyRunActivityRequests.clear();
  }

  function historyRunActivityList(activeRunId = "") {
    const skip = String(activeRunId || "");
    return Object.entries(historyRunActivityMap())
      .filter(([runId]) => runId && runId !== skip)
      .flatMap(([, calls]) => (Array.isArray(calls) ? calls : []));
  }

  // Newest first: the transcript runs oldest-to-newest, so its tail is the part
  // the reader is actually looking at, and the cap keeps a long conversation
  // from opening one request per turn.
  function transcriptHistoryRunIds(activeRunId = "") {
    const skip = String(activeRunId || "");
    const messages = Array.isArray(state.currentMessages) ? state.currentMessages : [];
    const runIds = [];
    const seen = new Set();
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const runId = String(messages[index]?.runId || messages[index]?.run_id || "");
      if (!runId || runId === skip || seen.has(runId)) continue;
      seen.add(runId);
      runIds.push(runId);
      if (runIds.length >= historyRunActivityLimit) break;
    }
    return runIds;
  }

  async function ensureHistoryRunActivity(agentId = state.agent?.id) {
    if (!agentId || historyRunActivityInFlight) return false;
    const activeRunId = String(state.activeRunSummaryRunId || "");
    const pending = transcriptHistoryRunIds(activeRunId).filter((runId) => !historyRunActivityRequests.has(runId));
    if (!pending.length) return false;
    const generation = messageLifecycleGeneration;
    historyRunActivityInFlight = true;
    let loaded = false;
    try {
      for (const runId of pending) {
        historyRunActivityRequests.add(runId);
        try {
          const page = await request(
            `/api/agents/${encodeURIComponent(agentId)}/runs/${encodeURIComponent(runId)}/tool-calls?view=activity&limit=${maxToolActivityCards}`,
          );
          if (!messageLifecycleIsCurrent(agentId, generation)) return loaded;
          const calls = Array.isArray(page) ? page : (Array.isArray(page?.toolCalls) ? page.toolCalls : []);
          if (!calls.length) continue;
          historyRunActivityMap()[runId] = calls;
          loaded = true;
        } catch (err) {
          // One unreachable run must not stop the rest of the transcript from
          // recovering its history, and dropping the mark keeps it retryable.
          historyRunActivityRequests.delete(runId);
          notifyTerminal?.(`[warn] ${cr("run.refreshFailed", { message: err?.message || err })}\n`);
        }
      }
    } finally {
      historyRunActivityInFlight = false;
    }
    if (!loaded || state.chatHydrating || !messageLifecycleIsCurrent(agentId, generation)) return loaded;
    const el = $("messages");
    // Inserting a strip above the tail grows the transcript under the reader.
    // Follow the same rule every other paint uses, or the newest message slides
    // out of view and the next render yanks it back.
    const view = captureTranscriptView(el);
    syncMessageToolActivityStacks(el);
    restoreTranscriptView(view, el);
    return loaded;
  }

  // A run that stops being the active one keeps the activity it already loaded:
  // handing those calls to the history map means the strip stays on screen
  // instead of blinking out when the next run starts and back in when the
  // refetch lands.
  function retainActiveRunActivityAsHistory(nextRunId = "") {
    const previousRunId = String(state.activeRunToolCallsRunId || state.activeRunSummaryRunId || "");
    if (!previousRunId || previousRunId === String(nextRunId || "")) return;
    const calls = Array.isArray(state.activeRunToolCalls) ? state.activeRunToolCalls : [];
    if (!calls.length) return;
    historyRunActivityMap()[previousRunId] = calls;
    historyRunActivityRequests.add(previousRunId);
  }

  // A run whose assistant turns carried no narration has no visible message to
  // file its calls under: the bare "Tool requested: X" rows are stripped from
  // the transcript, so every call in that run comes back unowned. The active run
  // parks those in its outcome card, which is anchored on the triggering user
  // message; an earlier run has no such card, so its calls are anchored on the
  // first message of the run instead -- the same user turn, and the same
  // resulting order: user message → tools → reply.
  function anchorHistoryRunActivity(unowned, messages, activeRunId) {
    const anchored = new Map();
    const byRun = new Map();
    for (const call of Array.isArray(unowned) ? unowned : []) {
      const callRunId = normalizeToolActivity(call).runId;
      if (!callRunId || callRunId === String(activeRunId || "")) continue;
      if (!byRun.has(callRunId)) byRun.set(callRunId, []);
      byRun.get(callRunId).push(call);
    }
    if (!byRun.size) return anchored;
    // Prefer the run's assistant turn. That turn is the one that called the
    // tools, and it is also where its reasoning lives -- reasoning is read off
    // the assistant message, so anchoring the tools anywhere else splits one
    // run into two rows: the tools under the user's question, the reasoning
    // alone under the reply.
    //
    // The run's first message stays the fallback, which is what a run whose
    // assistant turn produced no words needs: that turn never became a visible
    // message, so its tools would otherwise have nowhere to go.
    const firstMessageByRun = new Map();
    const assistantMessageByRun = new Map();
    for (const message of messages) {
      const messageRunId = String(message?.runId || message?.run_id || "");
      const messageId = String(message?.id || "");
      if (!messageRunId || !messageId) continue;
      if (!firstMessageByRun.has(messageRunId)) firstMessageByRun.set(messageRunId, messageId);
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      if (!assistantMessageByRun.has(messageRunId)) assistantMessageByRun.set(messageRunId, messageId);
    }
    const visibleTurnsByRun = visibleAssistantTurnsByRun(messages);
    for (const [callRunId, calls] of byRun) {
      const fallbackId = assistantMessageByRun.get(callRunId) || firstMessageByRun.get(callRunId);
      // No visible anchor: keep the previous behaviour of leaving the calls to
      // the run-level fallback rather than moving them under an unrelated turn.
      if (!fallbackId) continue;
      calls.sort((left, right) => String(normalizeToolActivity(left).createdAt || "")
        .localeCompare(String(normalizeToolActivity(right).createdAt || "")));
      // Split by the turn each call actually belongs under, for the same reason
      // as the unowned merge above. Anchoring a whole run onto its first
      // assistant turn put every later round's tools on the earliest row, so the
      // counts described the run rather than the round the reader was looking at.
      const turns = visibleTurnsByRun.get(callRunId) || [];
      const byOwner = new Map();
      for (const call of calls) {
        const ownerId = adoptingTurnForActivity(turns, normalizeToolActivity(call).createdAt) || fallbackId;
        if (!byOwner.has(ownerId)) byOwner.set(ownerId, []);
        byOwner.get(ownerId).push(call);
      }
      for (const [ownerId, ownedCalls] of byOwner) {
        if (!anchored.has(ownerId)) anchored.set(ownerId, []);
        anchored.get(ownerId).push([callRunId, ownedCalls]);
      }
    }
    return anchored;
  }

  // Tool records from older/partial payloads may not carry messageId even though
  // they do carry runId. In that case use the assistant turn for that run as the
  // owner before falling back to the run-level anchor. This keeps reasoning and
  // tools in one activity stack instead of producing one stack on each side of
  // the answer.
  // Every visible assistant turn of a run, in transcript order, with the time it
  // was saved. A run makes one assistant turn per tool round and most of those
  // turns are invisible -- their only content is a tool_use block, whose text is
  // stripped, so they never reach the transcript. Their tool calls therefore
  // arrive unowned and have to be adopted by a turn that is on screen.
  function visibleAssistantTurnsByRun(messages) {
    const byRun = new Map();
    for (const message of Array.isArray(messages) ? messages : []) {
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const runId = String(message?.runId || message?.run_id || "").trim();
      const messageId = String(message?.id || "").trim();
      if (!runId || !messageId) continue;
      if (!byRun.has(runId)) byRun.set(runId, []);
      byRun.get(runId).push({ id: messageId, createdAt: String(message?.createdAt || message?.created_at || "") });
    }
    return byRun;
  }

  // The visible turn a piece of unowned activity belongs under: the last one
  // saved at or before the activity happened. Anything older than the first
  // visible turn goes to that first turn, since there is nothing earlier to hold
  // it.
  function adoptingTurnForActivity(turns, createdAt) {
    if (!Array.isArray(turns) || !turns.length) return "";
    const at = String(createdAt || "");
    if (!at) return turns[turns.length - 1].id;
    let chosen = turns[0].id;
    for (const turn of turns) {
      if (turn.createdAt && turn.createdAt > at) break;
      chosen = turn.id;
    }
    return chosen;
  }

  function mergeUnownedActivityIntoAssistantTurns(grouped, messages) {
    // Spreading the calls across the run's visible turns by time, rather than
    // giving every one of them to a single turn. Keeping only the last turn per
    // run is what produced one row carrying a whole run's tools -- "9 次工具" --
    // while the rows around it showed nothing, even though each of those rows is
    // a real round that ran its own tools.
    const byRun = visibleAssistantTurnsByRun(messages);
    if (!byRun.size || !grouped?.unowned?.length) return grouped;
    const remaining = [];
    for (const call of grouped.unowned) {
      const tool = normalizeToolActivity(call);
      // Only repair incomplete records. An explicit but no-longer-visible
      // messageId is historical ownership and must continue through the
      // existing run-level anchoring path.
      if (tool.messageId) {
        remaining.push(call);
        continue;
      }
      const ownerId = adoptingTurnForActivity(byRun.get(String(tool.runId || "").trim()), tool.createdAt);
      if (!ownerId) {
        remaining.push(call);
        continue;
      }
      if (!grouped.byMessage.has(ownerId)) grouped.byMessage.set(ownerId, []);
      grouped.byMessage.get(ownerId).push(call);
    }
    grouped.unowned = remaining;
    return grouped;
  }

  // One activity stack per assistant turn, built from that turn's reasoning plus
  // the tool calls it emitted. Persisted and live records are merged into the
  // same stack because they are two views of one timeline: currentLiveToolOutputList
  // already drops live copies a terminal summary owns, so a call appears in one
  // of the two lists, never both.
  //
  // Returns the markup keyed by message id plus the set of tool-use ids that
  // found a home, which is what the run-level card subtracts to avoid showing
  // the same call twice.
  function messageToolActivityStacks(visibleMessages) {
    const stacks = new Map();
    const ownedToolUseIds = new Set();
    // Live reasoning steps this pass moved under a message. The tail subtracts
    // them so one turn's thinking is not rendered in two places.
    const messages = Array.isArray(visibleMessages) ? visibleMessages : [];
    const knownIds = new Set(messages.map((message) => String(message?.id || "")).filter(Boolean));
    if (!knownIds.size) return { stacks, ownedToolUseIds };
    const runId = state.activeRunSummaryRunId || state.activeRunSummary?.run?.id || "";
    const persisted = mergeUnownedActivityIntoAssistantTurns(groupToolActivityByMessage([
      ...activeRunToolCallList(state.activeRunSummary, runId),
      ...historyRunActivityList(runId),
    ], knownIds), messages);
    const live = mergeUnownedActivityIntoAssistantTurns(groupToolActivityByMessage(currentLiveToolOutputList(), knownIds), messages);
    // While a run is active its unowned calls belong to the run outcome card,
    // which is the only surface that exists before the closing turn is saved.
    // Once the run is terminal that turn is on screen, and parking the calls on
    // the card while the turn's stack holds the reasoning split one round into
    // two rows: tools in the card, thinking beside the answer. A terminal run
    // is therefore anchored like a history run -- everything on the turn.
    const activeRunTerminal = isTerminalRunStatus(state.activeRunSummary?.run?.status);
    const anchored = anchorHistoryRunActivity([...persisted.unowned, ...live.unowned], messages, activeRunTerminal ? "" : runId);
    const runActive = String(state.agent?.status || "").trim().toLowerCase() === "running";
    // One call, one row. A tool call reaches this function from several lists at
    // once -- the active run summary, the retained history map, and the live
    // stream -- and the same call carries the same toolUseId in all of them.
    // Whenever two of those copies land on different surfaces (say, one merged
    // onto its assistant turn and one still unowned and anchored), the turn
    // renders the same tools twice: once bare, once beside that turn's
    // reasoning. Rather than depend on every producer agreeing about ownership,
    // the invariant is enforced here: the first surface to claim a toolUseId
    // keeps it, and later surfaces drop it.
    // A live reasoning step is stamped with the turn that produced it, but the
    // owner id is only known once that turn has been persisted, so steps that
    // close before then carry no messageId. Such a step precedes the run's first
    // assistant turn, and the per-message filter lets an unstamped step through
    // for every message -- so a later turn with no reasoning of its own adopted
    // the whole backlog and rendered it as a bare "N steps of reasoning" row
    // beside the turn that actually did the work. Only the run's first assistant
    // turn may claim them.
    // When a step names a turn that is not on screen, the run it belongs to is
    // known and so is when that turn was saved, so it can be placed the same way
    // an unowned tool call is: under the last visible turn at or before it. Those
    // invisible turns are the tool rounds, and they hold most of a run's thinking,
    // so handing all of them to the run's first visible turn is what made one row
    // read "3 步推理" while the rounds beside it showed one step each.
    //
    // Read from state.currentMessages rather than the visible subset, because the
    // turns being located are precisely the ones filtered out of it.
    const invisibleTurnCreatedAt = new Map();
    const invisibleReasoningTurns = [];
    for (const message of Array.isArray(state.currentMessages) ? state.currentMessages : []) {
      const id = String(message?.id || "");
      if (!id || knownIds.has(id)) continue;
      const createdAt = String(message?.createdAt || message?.created_at || "");
      invisibleTurnCreatedAt.set(id, createdAt);
      // The tool rounds persist their reasoning too (one reasoningText per
      // assistant turn), but the turns themselves never reach the transcript,
      // so that thinking used to vanish the moment the live steps were cleared.
      // Collect it here so each round's reasoning can be filed under the
      // visible turn that adopts the round's tool calls.
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const text = String(message?.reasoningText || message?.reasoning_text || "").trim();
      const turnRunId = String(message?.runId || message?.run_id || "").trim();
      if (!text || !turnRunId) continue;
      invisibleReasoningTurns.push({ id, runId: turnRunId, createdAt, text });
    }
    const visibleTurnsByRun = visibleAssistantTurnsByRun(messages);
    // Persisted reasoning of invisible turns, grouped by the visible turn that
    // adopts it -- the same placement rule unowned tool calls use, so a round's
    // thinking and its tools always land on the same row.
    const adoptedReasoningByOwner = new Map();
    for (const turn of invisibleReasoningTurns) {
      const owner = adoptingTurnForActivity(visibleTurnsByRun.get(turn.runId) || [], turn.createdAt);
      if (!owner) continue;
      if (!adoptedReasoningByOwner.has(owner)) adoptedReasoningByOwner.set(owner, []);
      adoptedReasoningByOwner.get(owner).push(turn);
    }
    for (const turns of adoptedReasoningByOwner.values()) {
      turns.sort((left, right) => left.createdAt.localeCompare(right.createdAt));
    }
    // The tool call a turn's reasoning renders above: the turn's own first call
    // when the records carry its messageId, otherwise the first call at or
    // after the turn was saved. Records are already in createdAt order.
    const turnAnchorToolUseId = (records, turnId, createdAt) => {
      let byTime = "";
      for (const record of records) {
        const tool = normalizeToolActivity(record);
        if (!tool.toolUseId) continue;
        if (turnId && tool.messageId === turnId) return tool.toolUseId;
        if (!byTime && createdAt && String(tool.createdAt || "") >= createdAt) byTime = tool.toolUseId;
      }
      return byTime;
    };
    // The visible turn a step stamped to an invisible turn belongs under, or ""
    // when the step is not that case and the existing orphan rules apply.
    const adoptStampedStep = (step, messageRunId) => {
      const stepOwner = String(step?.messageId || "");
      if (!stepOwner || knownIds.has(stepOwner)) return "";
      const createdAt = invisibleTurnCreatedAt.get(stepOwner);
      if (createdAt === undefined) return "";
      return adoptingTurnForActivity(visibleTurnsByRun.get(messageRunId) || [], createdAt);
    };
    const firstAssistantTurnByRun = new Map();
    for (const message of messages) {
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const messageRun = String(message?.runId || message?.run_id || "").trim();
      const id = String(message?.id || "");
      if (!messageRun || !id || firstAssistantTurnByRun.has(messageRun)) continue;
      firstAssistantTurnByRun.set(messageRun, id);
    }
    const claimedToolKeys = new Set();
    const claimUnseen = (records) => {
      const kept = [];
      // Key -> index into kept, for this invocation only: a duplicate arriving
      // in the same turn's list can still donate its streamed output to the
      // copy that won the slot. Cross-turn duplicates just drop, as before.
      const keptIndexByKey = new Map();
      for (const record of Array.isArray(records) ? records : []) {
        // toolUseId when present, a content fingerprint otherwise. A record
        // with neither cannot be de-duplicated; keeping it is the safer side
        // of the trade, since dropping it would lose the row entirely.
        const key = toolActivityDedupeKey(record);
        if (key && claimedToolKeys.has(key)) {
          const keptIndex = keptIndexByKey.get(key);
          if (keptIndex !== undefined) kept[keptIndex] = mergeDuplicateToolActivity(kept[keptIndex], record);
          continue;
        }
        if (key) {
          claimedToolKeys.add(key);
          keptIndexByKey.set(key, kept.length);
        }
        kept.push(record);
      }
      return kept;
    };
    for (const message of messages) {
      const messageId = String(message?.id || "");
      if (!messageId) continue;
      const liveRecords = live.byMessage.get(messageId) || [];
      // anchorHistoryRunActivity resolves each group's anchor by that group's own
      // runId, so a group always lands on the first message of the very run it
      // came from. Its calls are therefore that turn's own activity arriving by
      // the history route rather than a foreign run needing its own row, and
      // they belong in the one stack alongside the turn's reasoning.
      //
      // Rendering them separately is what split a single run into two adjacent
      // "activity" lines -- one bare, one with reasoning -- whenever a call
      // reached the view through both the live stream and the persisted summary.
      const anchoredRecords = (anchored.get(messageId) || []).flatMap(([, calls]) => calls);
      const records = claimUnseen([...(persisted.byMessage.get(messageId) || []), ...liveRecords, ...anchoredRecords]
        .sort((left, right) => String(normalizeToolActivity(left).createdAt || "").localeCompare(String(normalizeToolActivity(right).createdAt || ""))));
      const messageRunId = String(message?.runId || message?.run_id || "").trim();
      // The full persisted trail: each adopted tool round's reasoning anchored
      // before that round's own first call, so the row reads
      // reasoning -> tools -> reasoning -> tools even after a reload -- the
      // same shape the live path streams.
      const adoptedSteps = (adoptedReasoningByOwner.get(messageId) || []).map((turn) => ({
        id: `reasoning:${turn.id}`,
        runId: turn.runId,
        messageId: turn.id,
        text: turn.text,
        beforeToolUseId: turnAnchorToolUseId(records, turn.id, turn.createdAt),
      }));
      let ownSteps = persistedReasoningSteps(message, records);
      if (ownSteps.length && adoptedSteps.length) {
        // With earlier rounds' thinking on the row, this turn's own reasoning
        // is the *last* thought, not the first: anchor it to the turn's own
        // first tool call, or let it trail when the turn only spoke.
        const ownAnchor = turnAnchorToolUseId(records, messageId, "");
        ownSteps = ownSteps.map((step) => ({ ...step, beforeToolUseId: ownAnchor }));
      }
      const persistedSteps = [...adoptedSteps, ...ownSteps];
      // Live steps still cover whatever has not persisted yet -- the round that
      // is streaming right now. They are only consulted while this turn's own
      // reasoning is unsaved: a saved turn trusts its row as complete, and the
      // ownerless backlog stays on the tail as before. Content matching drops
      // the live copy of anything the adopted rows above already carry, so a
      // round is never rendered twice while the run is still going.
      //
      // Only with a runId: currentLiveReasoningSteps("") matches every step and
      // would pull another run's thinking under this turn. Scoped to this
      // message, not just its run: a run persists one assistant turn per model
      // round, so a run-scoped fallback handed every turn the same full list.
      const liveSteps = ownSteps.length || !messageRunId ? [] : currentLiveReasoningSteps(messageRunId, messageId, {
        claimOrphans: firstAssistantTurnByRun.get(messageRunId) === messageId,
        visibleIds: knownIds,
        adoptStampedStep,
      });
      const reasoningSteps = [...persistedSteps, ...reasoningStepsNotCoveredByPersisted(liveSteps, persistedSteps)];
      // This turn's own stack first, then any earlier run this message anchors.
      const rendered = [];
      if (records.length || reasoningSteps.length) {
        const stackKey = `msg:${messageId}`;
        rendered.push([records, renderToolActivityStackHTML(records, {
          compact: true,
          live: liveRecords.length > 0,
          runActive,
          reasoningSteps,
          resolveBackgroundTask,
          runId: String(normalizeToolActivity(records[0] || {}).runId || message?.runId || runId || ""),
          stackKey,
          selectedToolUseId: selectedToolActivity(stackKey),
          totalCount: records.length,
        })]);
      }
      const html = rendered.map(([, markup]) => markup).filter(Boolean).join("");
      if (!html) continue;
      for (const [group, markup] of rendered) {
        if (!markup) continue;
        for (const record of group) {
          const key = toolActivityDedupeKey(record);
          if (key) ownedToolUseIds.add(key);
        }
      }
      stacks.set(messageId, `<div class="message-tool-activity" data-message-activity="${escapeAttr(messageId)}">${html}</div>`);
    }
    return { stacks, ownedToolUseIds };
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
    } else if (haystack.includes("must be a git repository")) {
      // Continuation anchors its safety snapshot in Git. The raw text names an
      // internal precondition and never says which directory is wrong or what
      // to do, so a long task can stop on something the user could fix in
      // seconds if anyone told them.
      message = cr("run.continuationWorkspaceNotGitRepo");
    } else if (haystack.includes("load continuation safety snapshot")) {
      message = cr("run.continuationSnapshotUnavailable", { detail: compactText(normalized, 200) });
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

  // Calls already shown under their own assistant message are excluded here, so
  // the run-level card keeps only what has no narration to sit under: rows from
  // before messageId was recorded, or an owner that never became a visible
  // message. The omitted-count note is computed against everything fetched, not
  // against the leftovers, so it stays truthful once the split moved most rows
  // out of this card.
  function renderConversationRunOutcomeHTML(summary, run, runId, toolCalls, ownedToolUseIds = null) {
    const status = String(run?.status || "unknown").trim().toLowerCase();
    if (status === "superseded") return "";
    const owned = ownedToolUseIds instanceof Set ? ownedToolUseIds : new Set();
    const leftover = owned.size
      ? toolCalls.filter((call) => {
        const key = toolActivityDedupeKey(call);
        return !key || !owned.has(key);
      })
      : toolCalls;
    const stackKey = runToolActivityStackKey(runId);
    const toolActivity = renderToolActivityStackHTML(leftover, {
      compact: true,
      resolveBackgroundTask,
      runId,
      stackKey,
      selectedToolUseId: selectedToolActivity(stackKey),
      totalCount: leftover.length,
    });
    // Counted against everything fetched for the run, not against this card's
    // leftovers, and deliberately so. The sentence describes the run across the whole
    // transcript: calls filed under their own assistant message are on screen and
    // therefore not missing. Counting only the leftovers would tell a run whose calls
    // all found a home that it is showing none of them.
    //
    // It does mean this note and the summary above it count different things, which
    // looks like a contradiction when the split moved most rows elsewhere: "showing
    // the latest 40" above a summary reading 24. The alternative states something
    // false, so the note stays and the disclosure below keeps the two apart.
    const omitted = Math.max(0, Number(summary?.toolCallCount || 0) - toolCalls.length);
    const omittedNote = omitted > 0
      ? `<div class="tool-activity-more conversation-run-omitted">${escapeHtml(cr("activity.recentOnly", { visible: toolCalls.length, count: omitted }))}</div>`
      : "";
    const loadEarlier = renderEarlierRunToolCallsButton(runId, { compact: true });
    const notice = renderConversationRunNoticeHTML(run, status);
    if (!toolActivity && !loadEarlier && !notice && !omittedNote) return "";
    // The paging note and the load button belong to the disclosure, not beside it.
    // Left as siblings of the collapsed <details> they stayed on screen while the
    // activity itself was folded away, so a run the reader had deliberately collapsed
    // still occupied three lines: a summary, a sentence pointing at the run history,
    // and a button offering to load the same calls inline. Inside the group they
    // appear only once the reader opens it and the offer is relevant.
    const paging = omittedNote || loadEarlier
      ? `<div class="conversation-run-activity-paging">${omittedNote}${loadEarlier}</div>`
      : "";
    return `
      <section class="conversation-run-outcome chat-flow-item chat-flow-left ${escapeAttr(runStatusClass(status))}" data-chat-alignment="left" data-chat-report="conversation-run" data-run-outcome-card>
        ${notice}
        ${toolActivity ? injectRunActivityPaging(toolActivity, paging) : paging}
      </section>
    `;
  }

  function renderConversationRunNoticeHTML(run, status) {
    if (status === "error" || status === "failed") {
      const message = runFailureMessage(run);
      const runId = String(run?.id || "");
      if (dismissedRunNotices.has(runId)) return "";
      // One line: the message is the whole point, and the title restated the
      // colour while the history hint said the same thing under every failure.
      // Retry and dismiss sit at the end, where the eye lands after reading.
      return `
        <div class="conversation-run-notice error" role="status">
          <span class="conversation-run-notice-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4.5 21 19.5H3z"></path><path d="M12 10v4.2"></path><path d="M12 17.2h.01"></path></svg></span>
          <span class="conversation-run-notice-message" title="${escapeAttr(message)}">${escapeHtml(message)}</span>
          <span class="conversation-run-notice-actions">
            <button type="button" class="conversation-run-notice-btn" data-run-retry="${escapeAttr(runId)}" title="${escapeAttr(cr("run.retry"))}" aria-label="${escapeAttr(cr("run.retry"))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M20 11a8 8 0 1 0-2.3 5.7"></path><path d="M20 5.5V11h-5.5"></path></svg></button>
            <button type="button" class="conversation-run-notice-btn" data-run-notice-dismiss="${escapeAttr(runId)}" title="${escapeAttr(cr("run.dismissError"))}" aria-label="${escapeAttr(cr("run.dismissError"))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg></button>
          </span>
        </div>
      `;
    }
    if (status === "interrupted") {
      // An interrupted run can still carry a reason, and it is usually the only
      // place that reason is visible. Dropping it left the generic sentence as
      // the whole story, so a run blocked by something fixable looked identical
      // to one the user stopped on purpose.
      const reason = String(run?.errorMessage || "").trim()
        ? runFailureMessage(run)
        : "";
      const detail = reason
        ? `<span class="conversation-run-notice-message" title="${escapeAttr(reason)}">${escapeHtml(reason)}</span>`
        : "";
      return `<div class="conversation-run-notice interrupted" role="status"><span>${escapeHtml(cr("run.conversationInterrupted"))}</span>${detail}</div>`;
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

  // Places the paging controls inside the activity disclosure so they fold with it.
  // The stack is built by renderToolActivityStackHTML, which has no notion of the run
  // card's paging, and threading an extra slot through it would put a run-level
  // concern into the shared renderer used by every per-message stack. Splicing at the
  // group's closing tag keeps that boundary intact.
  //
  // If the expected shape is not found the paging is appended after the stack instead
  // of being dropped: worse placement is recoverable, a load button that silently
  // stops rendering is not.
  function injectRunActivityPaging(stackHTML, paging) {
    if (!paging) return stackHTML;
    const closeAt = stackHTML.lastIndexOf("</details>");
    if (closeAt === -1) return `${stackHTML}${paging}`;
    return `${stackHTML.slice(0, closeAt)}${paging}${stackHTML.slice(closeAt)}`;
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
    // Key through the normalizer so a payload that spells the id differently
    // (toolUseId / tool_use_id / id) still matches its earlier copy; comparing
    // raw fields let the same call re-enter the list on every page load.
    const known = new Set((state.activeRunToolCalls || []).map((call) => normalizeToolActivity(call).toolUseId).filter(Boolean));
    state.activeRunToolCalls = [...calls.filter((call) => !known.has(normalizeToolActivity(call).toolUseId)), ...(state.activeRunToolCalls || [])];
    state.activeRunToolCallsHasMore = Boolean(page?.hasMore);
    state.activeRunToolCallsNextOffset = Number(page?.nextOffset || offset + calls.length);
    renderRunSummaryCard();
  }

  function bindRunSummaryButtons(root) {
    root.querySelectorAll("[data-run-tool-activity-more]").forEach((button) => {
      button.addEventListener("click", () => loadEarlierRunToolCalls(button.dataset.runToolActivityMore || "").catch(showError));
    });
    root.querySelectorAll("[data-run-notice-dismiss]").forEach((button) => {
      button.addEventListener("click", () => {
        dismissedRunNotices.add(String(button.dataset.runNoticeDismiss || ""));
        button.closest(".conversation-run-notice")?.remove();
      });
    });
    root.querySelectorAll("[data-run-retry]").forEach((button) => {
      button.addEventListener("click", () => retryFailedRun(button).catch(showError));
    });
  }

  // The composer owns the retry: it syncs the model picker to the agent and
  // holds the busy flag, so the banner delegates rather than posting its own
  // rerun and racing a send already in flight.
  async function retryFailedRun(button) {
    if (button.disabled) return;
    button.disabled = true;
    const runId = String(button.dataset.runRetry || "");
    try {
      if (await retryLastRun?.()) dismissedRunNotices.add(runId);
      else button.disabled = false;
    } catch (cause) {
      button.disabled = false;
      throw cause;
    }
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
      // Stamped with the turn that produced it, the same way tool records are.
      // runId alone is too coarse: one run persists several assistant turns, so
      // every turn of that run claimed the whole run's thinking and each of them
      // rendered the identical full list.
      messageId: String(state.liveAssistantToolOwnerId || ""),
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

  // messageId narrows the result to one assistant turn's own thinking. Without it
  // the caller gets the whole run, which is right for the tail stack and wrong for
  // a per-message stack.
  // A step is an orphan when no turn on screen can own it: either nothing stamped
  // it (it closed before the run's first turn was saved) or it was stamped to a
  // turn the transcript does not show. A turn whose only content is a tool_use
  // block is invisible, and a run makes one of those per tool round, so most of a
  // run's thinking is stamped to turns that never reach the per-message loop.
  // Orphans have to be claimed by the run's anchor turn -- the same turn the run's
  // tool calls are anchored onto -- or they strand on the tail as a bare
  // "N steps of reasoning" row beside the row holding the tools they explain.
  function liveReasoningStepIsOrphan(step, visibleIds) {
    const stepOwner = String(step?.messageId || "");
    if (!stepOwner) return true;
    return visibleIds instanceof Set ? !visibleIds.has(stepOwner) : false;
  }

  function currentLiveReasoningSteps(runId = "", messageId = "", options = {}) {
    const expected = String(runId || "");
    const owner = String(messageId || "");
    // The anchor turn takes the orphans on top of its own stamped steps. Every
    // other turn takes only what is stamped to it, so no two rows show the same
    // thinking. The tail passes no owner and keeps everything, which is what
    // covers a run with no visible turn yet.
    const claimOrphans = options.claimOrphans === true;
    const visibleIds = options.visibleIds instanceof Set ? options.visibleIds : null;
    // Placement for a step stamped to a turn that is not on screen. Those turns
    // are the run's tool rounds and they carry most of its thinking, so spreading
    // them over the visible turns by time is what keeps each row's step count
    // describing its own round. Returns "" when the step is some other case, which
    // leaves the rules below in charge.
    const adoptStampedStep = typeof options.adoptStampedStep === "function" ? options.adoptStampedStep : null;
    const steps = (Array.isArray(state.liveReasoningSteps) ? state.liveReasoningSteps : [])
      .filter((step) => !expected || !step.runId || step.runId === expected)
      .filter((step) => {
        if (!owner) return true;
        const stepOwner = String(step?.messageId || "");
        if (stepOwner === owner) return true;
        if (adoptStampedStep) {
          const adopter = adoptStampedStep(step, expected);
          // Only decides steps it recognises. An unstamped step returns "" and
          // stays with the run-anchor rule, which is what keeps a backlog that
          // precedes every saved turn off the later turns entirely.
          if (adopter) return adopter === owner;
        }
        return claimOrphans && liveReasoningStepIsOrphan(step, visibleIds);
      });
    const draft = state.liveReasoningDraft;
    if (draft && String(draft.text || "").trim() && (!expected || !draft.runId || draft.runId === expected)) {
      // The open draft belongs to whichever turn is currently streaming, so it
      // only joins a per-message stack when that turn is the current owner.
      const draftOwner = String(state.liveAssistantToolOwnerId || "");
      if (!owner || !draftOwner || draftOwner === owner) {
        steps.push({ id: "reasoning-open", runId: draft.runId, messageId: draftOwner, text: String(draft.text).trim(), beforeToolUseId: "", open: true });
      }
    }
    return steps;
  }

  // A closed live reasoning step and its assistant turn's persisted
  // reasoningText are the same thinking seen twice: the stream wrote the step,
  // then the runner saved the turn. Once that turn is on screen its own activity
  // stack renders the reasoning, so the tail must let go of its copy or the run
  // shows two identical "activity" rows.
  //
  // The handover is decided by the thinking itself, because neither of the two
  // things that look like cheaper keys survives contact with real runs:
  //
  //   - runId. A persisted row that reaches the client without one counts under
  //     a different key than the live step it owns, so nothing is handed over
  //     and every step renders twice.
  //   - turn count. One saved turn does not imply one closed live step. A step
  //     closes at each tool call, so a turn that thought, acted, and thought
  //     again streams more steps than it saves rows, and the surplus is stranded
  //     in a second reasoning-only stack.
  //
  // One saved row can own several steps. The runner concatenates every reasoning
  // delta of a model turn into a single ReasoningText, while the stream closes a
  // step at each tool call and at the switch to answering, so a turn that thought
  // and acted repeatedly saves one row covering all of it. Matching only the end
  // of that row retired the last step and stranded the earlier ones in a
  // reasoning-only stack that grew with every turn. Each row is therefore scanned
  // forward with a cursor: several steps may be retired, in streaming order, and
  // each occurrence is spent once so a thought repeated verbatim across turns
  // still keeps one home each.
  //
  // The two size caps do not line up, either. The runner trims to the last
  // maxPersistedReasoningBytes and the stream to the last
  // maxLiveReasoningCharacters, so for CJK -- three bytes per character against a
  // smaller byte budget -- the saved row can be the shorter of the two and is a
  // tail of the live step rather than containing it.
  //
  // Only closed steps hand over; the open draft is still streaming and exists
  // nowhere else yet.
  function reasoningHandoverKey(value) {
    return String(value || "").trim().replace(/\s+/g, " ");
  }

  // Same handover rule as the tail, scoped to one turn's stack: a live step
  // whose text the persisted rows already carry is the same thinking seen
  // twice, so only the copy the transcript will keep survives. The open draft
  // always stays -- it exists nowhere else yet.
  function reasoningStepsNotCoveredByPersisted(liveSteps, persistedSteps) {
    const list = Array.isArray(liveSteps) ? liveSteps : [];
    const rows = (Array.isArray(persistedSteps) ? persistedSteps : [])
      .map((step) => ({ text: reasoningHandoverKey(step?.text), cursor: 0 }))
      .filter((row) => row.text);
    if (!list.length || !rows.length) return list;
    return list.filter((step) => {
      if (step?.open) return true;
      const key = reasoningHandoverKey(step?.text);
      if (!key) return true;
      return !rows.some((saved) => {
        const at = saved.text.indexOf(key, saved.cursor);
        if (at >= 0) {
          saved.cursor = at + key.length;
          return true;
        }
        // The saved row is byte-bounded while the live copy is character
        // bounded, so for CJK the saved text can be a tail of the step.
        if (saved.cursor === 0 && key.endsWith(saved.text)) {
          saved.cursor = saved.text.length;
          return true;
        }
        return false;
      });
    });
  }

  function liveReasoningStepsWithoutPersisted(steps, runId = "") {
    const list = Array.isArray(steps) ? steps : [];
    if (!list.length) return list;
    const unclaimed = [];
    // Assistant turns on screen that have not persisted their reasoning yet.
    // messageToolActivityStacks falls back to the live steps for exactly these, so
    // the tail has to let go of what they take -- including the open draft, which
    // that fallback also picks up. Content matching cannot see these turns: they
    // have no saved reasoning to match against.
    //
    // Both the message id and its run are recorded because the fallback narrows by
    // message where a step carries one and by run where it does not, and the two
    // sides have to release exactly the same steps.
    const claimedMessages = new Set();
    const claimedRuns = new Set();
    // The anchor turn of each run claims that run's orphan steps, so the tail has
    // to release them on exactly the same condition. Releasing on any unsaved turn
    // of the run left them unrendered; releasing on none of them stranded a bare
    // reasoning row beside the row holding the tools.
    const visible = transcriptMessages(state.currentMessages);
    const visibleIds = new Set(visible.map((message) => String(message?.id || "")).filter(Boolean));
    const firstAssistantTurnByRun = new Map();
    for (const message of visible) {
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const owner = String(message?.runId || message?.run_id || "").trim();
      const id = String(message?.id || "").trim();
      if (owner && id && !firstAssistantTurnByRun.has(owner)) firstAssistantTurnByRun.set(owner, id);
    }
    // Runs whose anchor turn is on screen and has no saved reasoning of its own:
    // that turn is taking the orphans, so the tail must not also show them.
    const runsClaimingOrphans = new Set();
    for (const message of visible) {
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const owner = String(message?.runId || message?.run_id || "").trim();
      const id = String(message?.id || "").trim();
      if (!owner || firstAssistantTurnByRun.get(owner) !== id) continue;
      if (!persistedReasoningSteps(message).length) runsClaimingOrphans.add(owner);
    }
    for (const message of visible) {
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const persisted = persistedReasoningSteps(message);
      if (!persisted.length) {
        const owner = String(message?.runId || message?.run_id || "").trim();
        const ownerMessage = String(message?.id || "").trim();
        if (owner && firstAssistantTurnByRun.get(owner) === ownerMessage) claimedRuns.add(owner);
        if (ownerMessage) claimedMessages.add(ownerMessage);
        continue;
      }
      for (const entry of persisted) {
        const key = reasoningHandoverKey(entry.text);
        // A cursor per row, not a bare string: one row can own several steps, and
        // each occurrence inside it may only be spent once.
        if (key) unclaimed.push({ text: key, cursor: 0 });
      }
    }
    // Invisible tool-round turns persist their reasoning too, and once their
    // run has a visible turn the per-message stacks surface those rows. The
    // tail must then hand over the live copies the same way it does for
    // visible turns' rows, or the run shows the same thinking twice. A run
    // with no visible turn keeps its live copies: the tail is still their only
    // home.
    for (const message of Array.isArray(state.currentMessages) ? state.currentMessages : []) {
      const id = String(message?.id || "");
      if (!id || visibleIds.has(id)) continue;
      if (chatMessagePresentation(message).normalizedRole !== "assistant") continue;
      const owner = String(message?.runId || message?.run_id || "").trim();
      if (!owner || !firstAssistantTurnByRun.has(owner)) continue;
      for (const entry of persistedReasoningSteps(message)) {
        const key = reasoningHandoverKey(entry.text);
        if (key) unclaimed.push({ text: key, cursor: 0 });
      }
    }
    if (!unclaimed.length && !claimedRuns.size && !claimedMessages.size) return list;
    const fallbackRunId = String(runId || "").trim();
    return list.filter((step) => {
      // A stamped step is claimed only by the turn that produced it. Falling back
      // to the run for these is what let one unsaved turn hide the thinking of
      // every other turn in the same run.
      const stepMessage = String(step?.messageId || "").trim();
      // A step stamped to a turn the transcript shows is claimed by that turn.
      // Anything else is an orphan, and the run's anchor turn takes it.
      if (stepMessage && visibleIds.has(stepMessage)) {
        if (claimedMessages.has(stepMessage)) return false;
      } else {
        const owner = String(step?.runId || "").trim() || fallbackRunId;
        if (owner && (claimedRuns.has(owner) || runsClaimingOrphans.has(owner))) return false;
      }
      if (step?.open) return true;
      const key = reasoningHandoverKey(step?.text);
      if (!key) return true;
      // Search forward from the cursor so a row spends each occurrence once, in
      // the order the steps streamed. `indexOf` rather than `endsWith` is the
      // whole point: the runner concatenates a turn's reasoning into one row, so
      // every step except the last sits in the middle of it.
      const claimed = unclaimed.some((saved) => {
        const at = saved.text.indexOf(key, saved.cursor);
        if (at >= 0) {
          saved.cursor = at + key.length;
          return true;
        }
        // The saved row is trimmed to a byte budget while the live copy is
        // trimmed to a character budget, so for CJK the saved text can be the
        // shorter of the two. Then the row is a tail of the step, not the other
        // way round, and the row is spent whole.
        if (saved.cursor === 0 && saved.text && key.endsWith(saved.text)) {
          saved.cursor = saved.text.length;
          return true;
        }
        return false;
      });
      return !claimed;
    });
  }

  // Tool lifecycle events carry no messageId of their own, but the assistant
  // message that emitted them is always persisted and announced first (the
  // runner writes the turn, publishes message.created, then executes the calls).
  // Remembering that id here is what lets live activity group by turn without
  // widening the tool event payload.
  function rememberAssistantToolOwner(messageId) {
    const id = String(messageId || "").trim();
    if (!id) return false;
    state.liveAssistantToolOwnerId = id;
    return true;
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
    // Stamped once, at start: appendToolOutput and finishToolOutput both merge
    // onto the existing record, so the owner rides along for the whole lifecycle.
    const messageId = String(started.messageId || current.messageId || state.liveAssistantToolOwnerId || "");
    next[toolUseId] = { ...started, toolUseId: String(toolUseId), messageId, status: "running" };
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
    const reviewedRunId = String(reviewedRun?.id || state.activeRunToolCallsRunId || state.activeRunSummaryRunId || "");
    const runToolsHaveVisibleHome = Boolean(reviewedRun) && reviewedStatus !== "superseded";
    const terminalHome = runToolsHaveVisibleHome && isTerminalRunStatus(reviewedStatus);
    const reviewedIds = runToolsHaveVisibleHome && state.activeRunToolCallsRunId && Array.isArray(state.activeRunToolCalls)
      ? new Set(state.activeRunToolCalls.map((call) => normalizeToolActivity(call).toolUseId).filter(Boolean))
      : new Set();
    // A terminal summary owns this run's records for good. Delete the live
    // copies instead of re-filtering them on every paint, so a late event
    // replay or a paint racing the summary load cannot resurrect them as a
    // second activity stack.
    if (terminalHome && reviewedRunId) {
      const entries = Object.entries(state.liveToolOutputs || {});
      const kept = entries.filter(([, item]) => String(item?.runId || "") !== reviewedRunId);
      if (kept.length !== entries.length) state.liveToolOutputs = Object.fromEntries(kept);
    }
    return Object.values(state.liveToolOutputs || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .filter((item) => {
        const itemRunId = String(item.runId || "");
        if (!itemRunId || !reviewedRunId || itemRunId !== reviewedRunId) return true;
        // Terminal run summary already owns this run — drop every live copy,
        // even when tool-call ids have not finished hydrating yet.
        if (terminalHome) return false;
        return !reviewedIds.has(item.toolUseId);
      })
      .sort((a, b) => String(a.createdAt || "").localeCompare(String(b.createdAt || "")));
  }

  // Live records whose assistant message is already on screen render under that
  // message instead, so the tail stack keeps only what has nowhere else to go:
  // the current turn, whose message is not persisted yet.
  //
  // Ownership has to be decided the exact same way here as in
  // messageToolActivityStacks. That function repairs records that carry a runId
  // but no messageId by adopting the run's assistant turn; skipping the repair
  // here left those records looking unowned to the tail while the turn had
  // already claimed them, so both surfaces rendered the same calls. That is the
  // second activity card, and it appears whenever a client misses
  // message.created (reconnect, reload) and therefore has no
  // liveAssistantToolOwnerId to stamp on tool events.
  function unownedLiveToolOutputList() {
    const messages = transcriptMessages(state.currentMessages);
    const knownIds = new Set(messages.map((message) => String(message?.id || "")).filter(Boolean));
    return mergeUnownedActivityIntoAssistantTurns(
      groupToolActivityByMessage(currentLiveToolOutputList(), knownIds),
      messages,
    ).unowned;
  }

  // ownedToolUseIds is the set produced by messageToolActivityStacks so the
  // tail can exclude any call that already found a home under its own message.
  // Without this, a call whose runId maps to a visible assistant turn is
  // claimed by anchorHistoryRunActivity inside messageToolActivityStacks but
  // still returned by unownedLiveToolOutputList — rendering it twice.
  function renderLiveToolOutputCardsHTML(ownedToolUseIds = null) {
    const allRecords = unownedLiveToolOutputList();
    const records = ownedToolUseIds instanceof Set
      ? allRecords.filter((r) => {
        const key = toolActivityDedupeKey(r);
        return !key || !ownedToolUseIds.has(key);
      })
      : allRecords;
    const runActive = String(state.agent?.status || "").trim().toLowerCase() === "running";
    const runId = records.length ? normalizeToolActivity(records.at(-1)).runId : String(state.liveAssistantRunId || "");
    const summaryRun = state.activeRunSummary?.run;
    const summaryRunId = String(summaryRun?.id || state.activeRunSummaryRunId || "");
    // Records owned by a terminal run summary are already dropped one-by-one in
    // currentLiveToolOutputList. Suppressing the whole stack here instead would
    // also hide live records that carry no runId and therefore cannot belong to
    // the finished run.
    const reasoningSteps = liveReasoningStepsWithoutPersisted(currentLiveReasoningSteps(runId).filter((step) => {
      if (!summaryRunId || !isTerminalRunStatus(summaryRun?.status)) return true;
      return String(step?.runId || "") !== summaryRunId;
    }), runId);
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
    const view = captureTranscriptView(el);
    // Ownership can change on any tool event: the call's assistant turn may have
    // just been persisted, which moves it out of the tail stack and under that
    // turn. Sync first so the tail markup below is computed against the result.
    // The returned set of claimed tool-use ids is passed to the tail renderer so
    // records already anchored under a message are not rendered a second time.
    const ownedToolUseIds = syncMessageToolActivityStacks(el);
    const existing = el.querySelector("[data-live-tool-output-stack]");
    const html = renderLiveToolOutputCardsHTML(ownedToolUseIds);
    if (existing) {
      if (html) {
        // Preserve the <details> open/collapsed state across the outerHTML
        // replacement. Without this, every tool-count increment resets the card
        // to its template default (expanded), undoing a user collapse and causing
        // a height change that jumps the scroll position and flashes the layout.
        const detailsOpen = existing.querySelector("details.tool-activity-group")?.open ?? null;
        existing.outerHTML = html;
        // outerHTML replaces the node reference; re-query to restore state.
        const updated = el.querySelector("[data-live-tool-output-stack]");
        if (updated && detailsOpen !== null) {
          const details = updated.querySelector("details.tool-activity-group");
          if (details && details.open !== detailsOpen) details.open = detailsOpen;
        }
      } else {
        existing.remove();
      }
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
    restoreTranscriptView(view, el);
    bindToolActivityControls(el);
  }

  function clearLiveToolOutputs({ agentId = state.agent?.id, preserveView = false } = {}) {
    const id = String(agentId || "").trim();
    if (!id) return false;
    const previous = state.liveToolOutputs || {};
    const next = Object.fromEntries(
      Object.entries(previous).filter(([, item]) => item?.agentId && item.agentId !== id),
    );
    const removedToolOutput = Object.keys(previous).some((key) => !Object.prototype.hasOwnProperty.call(next, key));
    state.liveToolOutputs = next;
    const totals = { ...liveToolOutputTotals() };
    const removedTotals = Object.keys(totals).filter((key) => key.startsWith(`${id}:`));
    removedTotals.forEach((key) => delete totals[key]);
    state.liveToolOutputTotals = totals;
    // Reasoning is part of the same live activity surface; leaving it behind
    // after tools are cleared recreates a second "活动" card of pure thinking.
    const hadReasoning = Boolean(
      (Array.isArray(state.liveReasoningSteps) && state.liveReasoningSteps.length)
      || String(state.liveReasoningDraft?.text || "").trim(),
    );
    clearLiveReasoning();
    const changed = removedToolOutput || removedTotals.length > 0 || hadReasoning;
    if (!changed) return false;
    if (!preserveView) renderLiveToolOutputCards();
    return true;
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

  // Deliberately the same markup and classes as the run failure notice. A blocked
  // call and a failed turn are the same thing to the reader -- work stopped, here is
  // why -- so they should not look like two different features. Retry is omitted:
  // re-running a hard-blocked command reaches the same block every time.
  function renderBlockedToolNoticeHTML(notice) {
    const toolUseId = String(notice?.toolUseId || "");
    const reason = String(notice?.warning || "").trim() || cr("approval.blockedWarning");
    const toolName = String(notice?.toolName || "").trim();
    const message = toolName ? `${toolName}: ${reason}` : reason;
    return `
      <div class="conversation-run-notice error" role="status" data-blocked-tool-notice="${escapeAttr(toolUseId)}">
        <span class="conversation-run-notice-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4.5 21 19.5H3z"></path><path d="M12 10v4.2"></path><path d="M12 17.2h.01"></path></svg></span>
        <span class="conversation-run-notice-message" title="${escapeAttr(message)}">${escapeHtml(message)}</span>
        <span class="conversation-run-notice-actions">
          <button type="button" class="conversation-run-notice-btn" data-blocked-notice-dismiss="${escapeAttr(toolUseId)}" title="${escapeAttr(cr("run.dismissError"))}" aria-label="${escapeAttr(cr("run.dismissError"))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg></button>
        </span>
      </div>
    `;
  }

  function renderApprovalCardsHTML() {
    const approvals = currentApprovalList();
    const questions = currentUserQuestionList();
    const blocked = currentBlockedToolNoticeList();
    // The stack renders from live state without waiting on a fetch, which is why
    // the failure reason belongs here: it is on screen the moment the run stops.
    const runError = currentAgentRunError();
    if (!approvals.length && !questions.length && !blocked.length && !runError) return "";
    return `
      <div class="approval-stack chat-flow-stack chat-flow-left" data-chat-alignment="left" data-approval-stack>
        ${runError ? renderAgentRunErrorNoticeHTML(runError) : ""}
        ${blocked.map(renderBlockedToolNoticeHTML).join("")}
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
    // Capture the reader's intent before approval state changes the DOM: removing
    // the card shortens the transcript, while inserting a question grows it.
    const view = captureTranscriptView(el);
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
    restoreTranscriptView(view, el);
    if (!html) return;
    bindApprovalButtons(el);
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
    root.querySelectorAll("[data-blocked-notice-dismiss]").forEach((button) => {
      button.addEventListener("click", () => {
        // Drop the record first, then the row: a re-render triggered by the next
        // tool event would otherwise bring the dismissed notice straight back.
        dismissBlockedToolNotice(String(button.dataset.blockedNoticeDismiss || ""));
        button.closest(".conversation-run-notice")?.remove();
      });
    });
    root.querySelectorAll("[data-agent-error-dismiss]").forEach((button) => {
      button.addEventListener("click", () => {
        // Recorded in the same dismissed set the run notices use, so closing it
        // survives the re-render that the next snapshot triggers.
        dismissedRunNotices.add(String(button.dataset.agentErrorDismiss || ""));
        button.closest(".conversation-run-notice")?.remove();
      });
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

  // A hard block is not a decision anyone can make, so it is recorded as a notice
  // instead of an approval. Filing it under pendingToolApprovals used to make the
  // card appear and then vanish a moment later, when tool.finished cleared it: the
  // only lasting trace was a toast, and the reason was gone before it could be read.
  function rememberBlockedToolNotice(event) {
    const data = event.data || {};
    const toolUseId = String(data.toolUseId || data.tool_use_id || "");
    const agentId = event.agentId || state.agent?.id;
    if (!toolUseId || !agentId) return;
    state.blockedToolNotices = {
      ...(state.blockedToolNotices || {}),
      [toolUseId]: {
        toolUseId,
        agentId,
        toolName: String(data.toolName || data.tool_name || ""),
        // The warning is the whole point of the notice: it names what was refused
        // and why. Without it the row would only repeat that something was blocked.
        warning: String(data.warning || "").trim(),
        createdAt: event.createdAt || new Date().toISOString(),
      },
    };
    renderApprovalCards();
  }

  function dismissBlockedToolNotice(toolUseId) {
    if (!toolUseId || !state.blockedToolNotices?.[toolUseId]) return;
    const next = { ...(state.blockedToolNotices || {}) };
    delete next[toolUseId];
    state.blockedToolNotices = next;
    renderApprovalCards();
  }

  // A refusal belongs to the turn that hit it. Kept past that turn, it waits for
  // the next stop and lands as if it were the reason for that one.
  function clearBlockedToolNotices(agentId = state.agent?.id) {
    if (!Object.keys(state.blockedToolNotices || {}).length) return;
    const next = { ...(state.blockedToolNotices || {}) };
    for (const [key, value] of Object.entries(next)) {
      if (!agentId || !value?.agentId || value.agentId === agentId) delete next[key];
    }
    state.blockedToolNotices = next;
    renderApprovalCards();
  }

  function currentBlockedToolNoticeList() {
    const agentId = state.agent?.id || "";
    // Held back while the agent works: a refusal it routes around is a step, not
    // an outcome, and would otherwise sit above a run that went on to succeed.
    if (!agentHasStopped()) return [];
    return Object.values(state.blockedToolNotices || {})
      .filter((item) => item && (!item.agentId || item.agentId === agentId))
      .sort((a, b) => String(a.createdAt || "").localeCompare(String(b.createdAt || "")));
  }

  // "Has the agent stopped?" -- deliberately narrower than the composer's
  // agentTurnInFlight(). That one also counts live tool records that have not
  // reached a terminal status, which is right for the send button but wrong here:
  // those records are kept on screen on purpose after a run ends, until the
  // persisted summary takes them over. Reusing it would let one leftover record
  // hide a failure reason indefinitely -- the same class of silent omission this
  // notice exists to fix. Status and streaming are what actually say the agent is
  // working; a pending approval means it is mid-turn waiting on the reader.
  function agentHasStopped() {
    if (state.liveAssistantActive) return false;
    if (Object.keys(state.pendingToolApprovals || {}).length) return false;
    return String(state.agent?.status || "").trim().toLowerCase() !== "running";
  }

  // Why a stopped run needs its own notice: agent.error already carries the
  // reason in event.text, but the transcript used to show it only by way of the
  // persisted run summary. That summary is fetched afterwards and can legitimately
  // not exist -- when registerRun fails before a run row is created, the server
  // publishes agent.error with runId "" and never calls CompleteRun. The fetch
  // then falls back to the latest run, finds one that is not terminal, and returns
  // nothing, so the run stopped with the reason in hand and the screen stayed
  // empty. Keeping the text from the event makes the reason independent of that
  // fetch succeeding.
  function rememberAgentRunError(event) {
    const agentId = event?.agentId || state.agent?.id || "";
    if (!agentId) return;
    const message = String(event?.text || event?.data?.message || event?.data?.error || "").trim();
    if (!message) return;
    state.lastAgentError = {
      agentId,
      runId: String(event?.data?.runId || event?.data?.run_id || ""),
      message,
      createdAt: event?.createdAt || new Date().toISOString(),
    };
    renderApprovalCards();
  }

  function clearAgentRunError(agentId = state.agent?.id) {
    const current = state.lastAgentError;
    if (!current) return;
    if (agentId && current.agentId && current.agentId !== agentId) return;
    state.lastAgentError = null;
  }

  function currentAgentRunError() {
    const current = state.lastAgentError;
    if (!current || !agentHasStopped()) return null;
    if (current.agentId && current.agentId !== (state.agent?.id || "")) return null;
    if (dismissedRunNotices.has(agentRunErrorNoticeKey(current))) return null;
    // The run summary carries the richer version of the same failure, complete
    // with a retry button, so defer to it whenever it is on screen for this run
    // rather than stacking two rows that say the same thing.
    const run = state.activeRunSummary?.run;
    const summaryStatus = String(run?.status || "").trim().toLowerCase();
    const summaryShowsError = Boolean(run) && (summaryStatus === "error" || summaryStatus === "failed");
    if (summaryShowsError && (!current.runId || current.runId === String(run?.id || ""))) return null;
    return current;
  }

  function agentRunErrorNoticeKey(item) {
    return `agent-error:${item?.runId || item?.createdAt || ""}`;
  }

  function renderAgentRunErrorNoticeHTML(item) {
    const message = runFailureMessage({ errorMessage: item?.message });
    const key = agentRunErrorNoticeKey(item);
    return `
      <div class="conversation-run-notice error" role="status" data-agent-error-notice="${escapeAttr(key)}">
        <span class="conversation-run-notice-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4.5 21 19.5H3z"></path><path d="M12 10v4.2"></path><path d="M12 17.2h.01"></path></svg></span>
        <span class="conversation-run-notice-message" title="${escapeAttr(message)}">${escapeHtml(message)}</span>
        <span class="conversation-run-notice-actions">
          <button type="button" class="conversation-run-notice-btn" data-agent-error-dismiss="${escapeAttr(key)}" title="${escapeAttr(cr("run.dismissError"))}" aria-label="${escapeAttr(cr("run.dismissError"))}"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg></button>
        </span>
      </div>
    `;
  }

  function rememberToolApproval(event) {
    const data = event.data || {};
    const toolUseId = data.toolUseId || data.tool_use_id;
    const agentId = event.agentId || state.agent?.id;
    if (!toolUseId || !agentId) return;
    if (data.risk === "danger") {
      rememberBlockedToolNotice(event);
      return;
    }
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

  const terminalLiveToolStatuses = new Set([
    "completed", "complete", "succeeded", "success", "done", "error", "failed",
    "rejected", "denied", "cancelled", "canceled", "interrupted", "aborted", "superseded",
  ]);

  function liveToolStatusIsActive(item) {
    const status = String(item?.status || item?.state || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
    return !terminalLiveToolStatuses.has(status);
  }

  function agentTurnIsActive(agentId) {
    if (state.agent?.id !== agentId) return false;
    if (String(state.agent?.status || "").trim().toLowerCase() === "running") return true;
    if (state.liveAssistantActive) return true;
    if (Object.keys(state.pendingToolApprovals || {}).length) return true;
    return Object.values(state.liveToolOutputs || {}).some((item) => (
      item && (!item.agentId || item.agentId === agentId) && liveToolStatusIsActive(item)
    ));
  }

  function clearMessageRefreshTimer(agentId) {
    const timer = state.messageRefreshTimersByAgent?.[agentId];
    if (!timer) return;
    window.clearTimeout(timer);
    const next = { ...(state.messageRefreshTimersByAgent || {}) };
    delete next[agentId];
    state.messageRefreshTimersByAgent = next;
  }

  function scheduleMessageRefresh(delay = 0, agentId = state.agent?.id, options = {}) {
    if (!agentId) return;
    clearMessageRefreshTimer(agentId);
    const skipWhileActive = options?.skipWhileActive === true;
    const timer = window.setTimeout(() => {
      clearMessageRefreshTimer(agentId);
      if (skipWhileActive && agentTurnIsActive(agentId)) return;
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

  // Shared by the finished-fence path and the streaming open-fence path so a
  // code block does not change shape at the moment its closing ``` arrives.
  function codeBlockHTML(code, lang) {
    return `<div class="code-block"><div class="code-head"><span>${escapeHtml(lang)}</span><button class="copy-code" type="button" data-code="${escapeAttr(code)}">${escapeHtml(cr("code.copy"))}</button></div><pre><code>${highlightCode(code, lang)}</code></pre></div>`;
  }

  function renderMarkdown(text) {
    const blocks = [];
    const pattern = /```([^\n`]*)\n([\s\S]*?)```/g;
    let lastIndex = 0;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      if (match.index > lastIndex) blocks.push(renderMarkdownText(text.slice(lastIndex, match.index)));
      blocks.push(codeBlockHTML(match[2] || "", (match[1] || "text").trim() || "text"));
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
    // Table accumulator: rows collected until a non-table line closes the block.
    let tableRows = [];

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
    // Flush a collected table: first row is <thead>, rest are <tbody>.
    const closeTable = () => {
      if (!tableRows.length) return;
      const [headerRow, , ...bodyRows] = tableRows; // row[1] is the separator
      const thCells = (headerRow || []).map((cell) => `<th>${renderInlineMarkdown(cell)}</th>`).join("");
      const thead = `<thead><tr>${thCells}</tr></thead>`;
      const tbody = bodyRows.length
        ? `<tbody>${bodyRows.map((row) => `<tr>${row.map((cell) => `<td>${renderInlineMarkdown(cell)}</td>`).join("")}</tr>`).join("")}</tbody>`
        : "";
      html.push(`<div class="md-table-wrap"><table class="md-table">${thead}${tbody}</table></div>`);
      tableRows = [];
    };
    // Split a pipe-delimited row into trimmed cells, ignoring leading/trailing |.
    const parseTableRow = (line) => line.replace(/^\s*\|/, "").replace(/\|\s*$/, "").split("|").map((cell) => cell.trim());
    const isSeparatorRow = (row) => row.every((cell) => /^:?-+:?$/.test(cell));
    const pushItem = (markup) => {
      if (lists.length) lists[lists.length - 1].items.push(markup);
      else html.push(markup);
    };

    for (const raw of lines) {
      const line = raw.replace(/\s+$/, "");
      if (!line.trim()) {
        closeLists();
        closeQuote();
        closeTable();
        continue;
      }

      const heading = line.match(/^(#{1,6})\s+(.+)$/);
      if (heading) {
        closeLists();
        closeQuote();
        closeTable();
        const level = heading[1].length;
        html.push(`<h${level}>${renderInlineMarkdown(heading[2].replace(/\s+#+\s*$/, ""))}</h${level}>`);
        continue;
      }

      if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
        closeLists();
        closeQuote();
        closeTable();
        html.push("<hr>");
        continue;
      }

      const quoted = line.match(/^\s{0,3}>\s?(.*)$/);
      if (quoted) {
        closeLists();
        closeTable();
        quote.push(quoted[1]);
        continue;
      }
      closeQuote();

      // Table rows: a line with at least one | is a candidate. Accumulate header +
      // separator + body rows together, then flush when the block ends.
      if (line.includes("|")) {
        const cells = parseTableRow(line);
        if (cells.length >= 1) {
          closeLists();
          if (tableRows.length === 1 && isSeparatorRow(cells)) {
            // Separator row: store it but don't display as a real row.
            tableRows.push(cells);
          } else {
            tableRows.push(cells);
          }
          continue;
        }
      } else if (tableRows.length) {
        // Non-pipe line while a table is open closes it.
        closeTable();
      }

      const bullet = line.match(/^(\s*)[-*+]\s+(.+)$/);
      const ordered = line.match(/^(\s*)\d{1,9}[.)]\s+(.+)$/);
      const item = bullet || ordered;
      if (item) {
        closeTable();
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
    closeTable();
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
    // A per-message stack may hold records from any of the three lists (active
    // run, retained history, live stream), so the detail lookup must search
    // them all: searching only the active run made clicking a history row
    // answer "detail unavailable".
    return [
      ...activeRunToolCallList(state.activeRunSummary, runId),
      ...(historyRunActivityMap()[runId] || []),
      ...currentLiveToolOutputList(),
    ];
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
    // Update every row's inline-detail slot: fill the selected one, clear all others.
    stack.querySelectorAll?.("[data-tool-activity-inline-detail]").forEach((slot) => {
      const slotId = String(slot.dataset?.toolActivityInlineDetail || "");
      if (slotId !== selected) {
        slot.innerHTML = "";
        return;
      }
      const record = toolActivityRecordsForStack(stack).find((item) => normalizeToolActivity(item).toolUseId === selected);
      slot.innerHTML = record
        ? renderToolActivityCardHTML(record, { resolveBackgroundTask, detailsExpanded: true, inlineDetail: true })
        : `<div class="tool-activity-empty">${escapeHtml(cr("activity.detailUnavailable"))}</div>`;
    });
    // Keep the legacy shared slot empty (it is still in the DOM for older renders).
    const legacySlot = stack.querySelector?.("[data-tool-activity-selected-detail]");
    if (legacySlot) legacySlot.innerHTML = "";
  }

  function bindToolActivityControls(root) {
    if (!root?.addEventListener || !root.dataset || root.dataset.toolActivityControlsBound === "true") return;
    root.dataset.toolActivityControlsBound = "true";
    root.addEventListener("click", (event) => {
      const copyButton = event.target?.closest?.("[data-tool-block-copy]");
      if (copyButton) {
        // The block's own <pre> is the source of truth; embedding the text in a
        // data attribute would double every large output in the DOM.
        const pre = copyButton.closest?.(".tool-activity-block")?.querySelector?.("pre");
        const original = copyButton.textContent;
        copyToClipboard(pre?.textContent || "").then((ok) => {
          copyButton.textContent = ok ? cr("code.copied") : cr("code.copyFailed");
          setTimeout(() => { copyButton.textContent = original; }, 1200);
        });
        return;
      }
      const toggleButton = event.target?.closest?.("[data-tool-block-toggle]");
      if (toggleButton) {
        const pre = toggleButton.closest?.(".tool-activity-block")?.querySelector?.("pre");
        if (!pre) return;
        const expanded = pre.classList.toggle("is-expanded");
        toggleButton.textContent = cr(expanded ? "activity.collapseBlock" : "activity.expandBlock");
        return;
      }
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
        // Ignore the browser's synthetic error from an empty src before
        // hydration. Only reveal the placeholder when the protected-image
        // fetch itself reported a failure (protectedImageState === "error").
        if (image.dataset?.protectedImageState !== "error") return;
        const placeholder = image.closest?.(".attachment-image-card")?.querySelector?.("[data-attachment-image-failed]");
        if (placeholder) placeholder.hidden = false;
        image.closest?.(".attachment-image-card")?.classList?.add?.("is-missing");
      });
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
          labels: {
            close: cr("attachment.closeViewer"),
            download: cr("imageGeneration.download"),
            copy: cr("attachment.copyImage"),
          },
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
        if (image.dataset?.protectedImageState !== "error") return;
        const card = image.closest?.("[data-generated-image]");
        image.closest?.(".generated-image-open")?.setAttribute?.("hidden", "");
        const placeholder = card?.querySelector?.("[data-generated-image-missing]");
        if (placeholder) placeholder.hidden = false;
        card?.classList?.add?.("is-missing");
      });
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
    // Incremental streaming re-scans a container that already holds bound
    // buttons, so binding is marked rather than repeated: a second listener on
    // the same button would fire the copy twice and fight over the label.
    root.querySelectorAll(".copy-code:not([data-copy-bound])").forEach((button) => {
      button.dataset.copyBound = "1";
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
    clearLiveToolOutputs,
    clearPlanState,
    clearLiveAssistantText,
    clearMessageRefreshTimer,
    clearRunSummary,
    clearToolApproval,
    clearAgentRunError,
    clearBlockedToolNotices,
    dismissBlockedToolNotice,
    rememberAgentRunError,
    // Exported for tests: the approval stack is the surface that reports why a run
    // stopped, so asserting on its markup is more direct than driving a DOM.
    renderApprovalCardsHTML,
    // Also for tests. Where the run outcome card lands is a placement decision made
    // from state alone, so asserting on the decision is steadier than asserting on
    // the position of a node inside a rebuilt transcript.
    runOutcomeAnchorForTest: runOutcomeAnchor,
    runOutcomeHomeIsOffScreenForTest: runOutcomeHomeIsOffScreen,
    echoPendingUserMessage,
    discardPendingUserMessage,
    clearUserQuestion,
    copyCurrentConversationMarkdown,
    ensureHistoryRunActivity,
    finishToolOutput,
    invalidateMessageLifecycle,
    loadLatestRunSummary,
    loadMessages,
    loadOlderMessages,
    loadRunSummary,
    performPlanAction,
    rememberImageGenerationStatus,
    rememberAssistantToolOwner,
    rememberToolApproval,
    rememberToolStarted,
    rememberUserQuestion,
    refreshUserMessageIdentity,
    // Handed to the background task panel so a subagent's answer is rendered by
    // the same markdown pipeline as the main thread instead of a second one that
    // would drift from it.
    renderMarkdown,
    replacePendingApprovals,
    replacePendingUserQuestions,
    replacePlanState,
    scheduleMessageRefresh,
    updateConversationCopyButton,
    updateLiveAssistantPerformance,
    scrollMessagesToBottom,
  };
}
