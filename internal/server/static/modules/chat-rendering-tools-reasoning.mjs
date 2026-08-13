import { escapeAttr, escapeHtml } from "./dom.mjs";
import { chatMessagePresentation } from "./chat-rendering-messages.mjs";
import { compactToolText, normalizeToolActivity } from "./chat-rendering-tools-normalize.mjs";
import { toolActivityGlyph } from "./chat-rendering-tools-glyphs.mjs";

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

export function renderReasoningStepHTML(step) {
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
