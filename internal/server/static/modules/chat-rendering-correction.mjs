import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { visibleMessageText } from "./skills-commands.mjs";

export function createChatRenderingCorrection({
  state,
  request,
  applyMessageSnapshot,
  loadMessages,
  showToast,
} = {}) {
  function renderCorrectionEditor(message) {
    const attachments = Array.isArray(message.attachments) ? message.attachments : [];
    const files = Array.isArray(state.correctionFiles) ? state.correctionFiles : [];
    // The file picker, the retire note, and the actions share one wrapping
    // footer row instead of stacking, so the editor stays roughly the height
    // of the message it replaces rather than growing into a tall card.
    return `
      <form class="message-correction-editor" data-correction-form="${escapeAttr(message.id || "")}">
        <textarea class="message-correction-text" data-correction-text rows="3">${escapeHtml(state.correctionText ?? visibleMessageText(message))}</textarea>
        ${attachments.length ? `<div class="message-correction-attachments">${attachments.map((attachment) => `
          <label><input type="checkbox" data-keep-correction-attachment value="${escapeAttr(attachment.id || "")}" checked /> ${escapeHtml(attachment.filename || cr("attachment.attachment"))}</label>
        `).join("")}</div>` : ""}
        ${files.length ? `<div class="message-correction-new-files">${files.map((file) => `<span>${escapeHtml(file.name || cr("attachment.attachment"))}</span>`).join("")}</div>` : ""}
        <div class="message-correction-footer">
          <label class="message-correction-file-label">${escapeHtml(cr("message.correctionAddFiles"))}<input type="file" data-correction-files multiple /></label>
          <p class="message-correction-note">${escapeHtml(cr("message.correctionNote"))}</p>
          <div class="message-correction-actions">
            <button class="ghost-btn mini" type="button" data-correction-cancel>${escapeHtml(cr("message.correctionCancel"))}</button>
            <button class="ghost-btn mini" type="submit" title="${escapeAttr(cr("message.correctTitle"))}">${escapeHtml(cr("message.correctionSubmit"))}</button>
          </div>
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
    showToast(cr("message.correctionCreated"), "success");
  }

  return {
    closeCorrectionEditor,
    correctionClipboardFiles,
    openCorrectionEditor,
    renderCorrectionEditor,
    submitCorrection,
  };
}
