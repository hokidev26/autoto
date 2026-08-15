import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { assistantAvatarSVG, profileAvatarHTML } from "./profile-avatar.mjs";
import { messageCopyGlyph } from "./chat-rendering-tools-glyphs.mjs";
import {
  groupToolActivityByMessage,
  persistedReasoningSteps,
  renderToolActivityStackHTML,
  transcriptMessageText,
} from "./chat-rendering.mjs";

function text(value) {
  return String(value ?? "").trim();
}

// The dispatcher appends a bracketed acceptance-criteria protocol block to the
// child's briefing. It addresses the child model, so the panel showing the
// briefing to a person renders the task itself without the contract boilerplate.
export function stripAcceptanceCriteriaBlock(value) {
  return String(value ?? "")
    .replace(/\n*\[BACKGROUND_ACCEPTANCE_CRITERIA\][\s\S]*?\[\/BACKGROUND_ACCEPTANCE_CRITERIA\]/g, "")
    .trim();
}

export function childTranscriptMessageText(message) {
  return stripAcceptanceCriteriaBlock(transcriptMessageText(message));
}

export const childBubbleBodyLineLimit = 12;
export const childBubbleBodyCharLimit = 900;

// Nothing is discarded: past the limit the head stays visible and the remainder
// goes into a disclosure, so the shape of the conversation survives while the
// whole text is still one click away.
export function renderChildBubbleBodyHTML(body, { escapeHtml: esc = escapeHtml, t: translate = t } = {}) {
  const lines = body.split("\n");
  const tooManyLines = lines.length > childBubbleBodyLineLimit;
  const tooLong = body.length > childBubbleBodyCharLimit;
  if (!tooManyLines && !tooLong) return `<p>${esc(body)}</p>`;
  // Whichever limit bites first decides the cut, so a single enormous line is
  // bounded too rather than only a tall one.
  const headByLines = tooManyLines ? lines.slice(0, childBubbleBodyLineLimit).join("\n") : body;
  const head = headByLines.length > childBubbleBodyCharLimit
    ? headByLines.slice(0, childBubbleBodyCharLimit)
    : headByLines;
  const rest = body.slice(head.length).replace(/^\n/, "");
  if (!rest) return `<p>${esc(body)}</p>`;
  const hiddenLines = rest.split("\n").length;
  return `<p>${esc(head)}</p>
      <details class="background-task-bubble-more">
        <summary>${esc(translate("backgroundTasks.moreLines", { count: String(hiddenLines) }))}</summary>
        <p>${esc(rest)}</p>
      </details>`;
}

export function renderChildConversationHTML({
  childAgentId,
  runId = "",
  messages = [],
  busy = false,
  callsByMessage = new Map(),
  toolCallsFlat = [],
  entityGeneration,
  editing = null,
  userIdentity,
  toolSelection = new Map(),
  renderMarkdown,
} = {}) {
  const agentId = text(childAgentId);
  if (!agentId) return `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.noChildAgent"))}</div>`;
  if (!messages.length) {
    return `<div class="background-task-empty">${escapeHtml(busy ? t("backgroundTasks.loading") : t("backgroundTasks.noConversation"))}</div>`;
  }
  const knownMessageIds = new Set(messages.map((message) => text(message?.id)).filter(Boolean));
  const generation = Number(entityGeneration);
  const bubbles = messages.map((message) => {
    const role = text(message?.role) === "user" ? "user" : "assistant";
    const body = childTranscriptMessageText(message);
    const reasoning = text(message?.reasoningText);
    const messageId = text(message?.id);
    const calls = callsByMessage.get(messageId) || [];
    if (!body && !reasoning && !calls.length) return "";
    const isEditing = Boolean(editing && editing.agentId === agentId && editing.messageId === messageId && role === "user");
    const bubbleClass = `background-task-bubble role-${role} chat-message${isEditing ? " message-editing" : ""}`;
    return `<article class="${bubbleClass}"${messageId ? ` data-message-id="${escapeAttr(messageId)}"` : ""} data-message-role="${escapeAttr(role)}" data-agent-id="${escapeAttr(agentId)}"${Number.isInteger(generation) ? ` data-entity-generation="${escapeAttr(String(generation))}"` : ""}>
        ${renderChildMessageHeadHTML(role, message, agentId, userIdentity, { editing: isEditing })}
        ${renderChildActivityHTML(agentId, `msg:${messageId}`, calls, persistedReasoningSteps(message, calls), runId, toolSelection)}
        ${isEditing ? renderChildCorrectionHTML(message, agentId) : (body ? renderChildBodyHTML(body, renderMarkdown) : "")}
      </article>`;
  }).join("");
  const { unowned } = groupToolActivityByMessage(toolCallsFlat, knownMessageIds);
  const earlier = renderChildActivityHTML(agentId, "run", unowned, [], runId, toolSelection);
  return `<div class="background-task-conversation">${earlier}${bubbles}</div>`;
}

function renderChildMessageHeadHTML(role, message, childAgentId, userIdentity, { editing = false } = {}) {
  const timestampValue = text(message?.createdAt);
  const timeHTML = timestampValue
    ? `<time class="message-time" datetime="${escapeAttr(timestampValue)}" title="${escapeAttr(formatTimestamp(timestampValue))}">${escapeHtml(formatTimestamp(timestampValue, { timeOnly: true }))}</time>`
    : "";
  const messageId = text(message?.id);
  const copyTitle = cr("message.copyTitle");
  const copyBtn = messageId
    ? `<button class="message-copy-btn" type="button" data-copy-child-message="${escapeAttr(messageId)}" data-agent-id="${escapeAttr(childAgentId)}" title="${escapeAttr(copyTitle)}" aria-label="${escapeAttr(copyTitle)}">${messageCopyGlyph()}</button>`
    : "";
  const correctTitle = cr("message.correctTitle");
  // Icon-only: the visible "更正" label is hidden by CSS on the main transcript,
  // but those rules do not match this panel, so the two characters wrapped
  // vertically in the 20px hit target. Keep the label in aria/title only.
  const correctBtn = role === "user" && messageId && !editing
    ? `<button class="message-copy-btn" type="button" data-correct-child-message="${escapeAttr(messageId)}" data-agent-id="${escapeAttr(childAgentId)}" title="${escapeAttr(correctTitle)}" aria-label="${escapeAttr(correctTitle)}"></button>`
    : "";
  const actions = (correctBtn || copyBtn) ? `<div class="message-head-actions">${correctBtn}${copyBtn}</div>` : "";
  if (role !== "user") {
    return `<div class="message-head">
        <div class="message-meta"><span class="message-avatar message-avatar-logo" aria-hidden="true">${assistantAvatarSVG}</span><div class="message-role">Autoto</div></div>
        ${actions}
        ${timeHTML}
      </div>`;
  }
  const identity = userIdentity || { displayName: "", avatarInitials: "" };
  return `<div class="message-head">
      <div class="message-meta"><span class="message-avatar" aria-hidden="true" data-user-profile-avatar>${profileAvatarHTML(identity)}</span><div class="message-role"><span data-user-profile-name>${escapeHtml(identity.displayName)}</span></div></div>
      ${actions}
      ${timeHTML}
    </div>`;
}

function renderChildCorrectionHTML(message, childAgentId) {
  const body = childTranscriptMessageText(message);
  return `<form class="message-correction-editor" data-child-correction-form="${escapeAttr(text(message?.id))}" data-agent-id="${escapeAttr(childAgentId)}">
      <textarea class="message-correction-text" data-correction-text rows="3">${escapeHtml(body)}</textarea>
      <div class="message-correction-actions">
        <button class="ghost-btn mini" type="button" data-child-correction-cancel>${escapeHtml(cr("message.correctionCancel"))}</button>
        <button class="ghost-btn mini" type="submit">${escapeHtml(cr("message.correctionSubmit"))}</button>
      </div>
    </form>`;
}

function renderChildActivityHTML(childAgentId, scope, calls, reasoningSteps, runId, toolSelection) {
  const stackKey = `child:${text(childAgentId)}:${scope}`;
  return renderToolActivityStackHTML(calls, {
    compact: true,
    runId,
    stackKey,
    reasoningSteps,
    selectedToolUseId: toolSelection.get(stackKey) || "",
  });
}

export function renderChildBodyHTML(body, renderMarkdown) {
  if (typeof renderMarkdown !== "function") return renderChildBubbleBodyHTML(body);
  return `<div class="message-content background-task-bubble-body">${renderMarkdown(body)}</div>`;
}
