import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatBytes } from "./formatters.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { protectedImageAttribute } from "./protected-images.mjs";

export function createChatRenderingAttachments({
  state,
  attachmentIcon,
  attachmentKind,
} = {}) {
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

  return {
    attachmentKindLabel,
    attachmentURL,
    messageAttachmentsMarkdown,
    renderMessageAttachments,
    renderSentAttachmentCard,
  };
}
