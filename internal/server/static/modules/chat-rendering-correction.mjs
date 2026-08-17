import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { visibleMessageText } from "./skills-commands.mjs";
import { attachmentGlyph, attachmentPaperclipGlyph, classifyAttachmentKind } from "./attachment-glyphs.mjs";

export function createChatRenderingCorrection({
  state,
  request,
  applyMessageSnapshot,
  loadMessages,
  showToast,
} = {}) {
  function attachmentDisplayKind(fileLike = {}) {
    return fileLike.kind || classifyAttachmentKind({
      name: fileLike.filename || fileLike.name || "",
      type: fileLike.mimeType || fileLike.type || "",
    });
  }

  function correctionChipHTML({ keepId = "", name = "", kind = "binary", removeIndex = -1 } = {}) {
    const icon = `<span class="pending-file-icon" data-kind="${escapeAttr(kind)}" aria-hidden="true">${attachmentGlyph(kind)}</span>`;
    const label = `<span class="pending-file-name">${escapeHtml(name || cr("attachment.attachment"))}</span>`;
    if (keepId) {
      return `<label class="pending-file-chip message-correction-chip" title="${escapeAttr(name)}">
        <input class="sr-only" type="checkbox" data-keep-correction-attachment value="${escapeAttr(keepId)}" checked />
        ${icon}${label}
      </label>`;
    }
    return `<span class="pending-file-chip message-correction-chip" title="${escapeAttr(name)}">
      ${icon}${label}
      <button class="pending-attachment-remove" type="button" data-remove-correction-file="${escapeAttr(String(removeIndex))}" title="${escapeAttr(t("workspace.chat.removeAttachment"))}" aria-label="${escapeAttr(t("workspace.chat.removeAttachment"))}">×</button>
    </span>`;
  }

  function renderCorrectionEditor(message) {
    const attachments = Array.isArray(message.attachments) ? message.attachments : [];
    const files = Array.isArray(state.correctionFiles) ? state.correctionFiles : [];
    const chips = [
      ...attachments.map((attachment) => correctionChipHTML({
        keepId: attachment.id || "",
        name: attachment.filename || cr("attachment.attachment"),
        kind: attachmentDisplayKind(attachment),
      })),
      ...files.map((file, index) => correctionChipHTML({
        name: file.name || cr("attachment.attachment"),
        kind: classifyAttachmentKind(file),
        removeIndex: index,
      })),
    ];
    return `
      <form class="message-correction-editor" data-correction-form="${escapeAttr(message.id || "")}">
        <textarea class="message-correction-text" data-correction-text rows="3">${escapeHtml(state.correctionText ?? visibleMessageText(message))}</textarea>
        ${chips.length ? `<div class="message-correction-attachments">${chips.join("")}</div>` : ""}
        <div class="message-correction-footer">
          <button class="message-correction-attach" type="button" data-correction-pick-files>
            ${attachmentPaperclipGlyph()}
            <span>${escapeHtml(cr("message.correctionAddFiles"))}</span>
          </button>
          <input class="sr-only" type="file" data-correction-files multiple />
          <div class="message-correction-actions">
            <button class="ghost-btn mini" type="button" data-correction-cancel>${escapeHtml(cr("message.correctionCancel"))}</button>
            <button class="message-correction-submit" type="submit" title="${escapeAttr(cr("message.correctTitle"))}">${escapeHtml(cr("message.correctionSubmit"))}</button>
          </div>
        </div>
        <p class="message-correction-note">${escapeHtml(cr("message.correctionNote"))}</p>
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
    state.correctionEditorNeedsFocus = true;
    applyMessageSnapshot(state.currentMessages, state.agent?.id);
  }

  function closeCorrectionEditor() {
    state.editingMessageId = "";
    state.correctionText = "";
    state.correctionFiles = [];
    state.correctionEditorNeedsFocus = false;
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
    state.correctionEditorNeedsFocus = false;
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
