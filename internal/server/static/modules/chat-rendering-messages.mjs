import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatNumber } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { visibleMessageText } from "./skills-commands.mjs";
import { normalizeAvatarDataUrl } from "./profile-avatar.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import {
  protectedDownloadAttribute,
  protectedImageAttribute,
} from "./protected-images.mjs";

export const userMessageRoles = new Set(["user", "human"]);
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

export function transcriptMessages(messages) {
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
