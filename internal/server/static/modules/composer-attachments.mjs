import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatBytes } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import { isSupportedVideoFile, processVideoAttachment } from "./video-attachments.mjs";
import { attachmentGlyph } from "./attachment-glyphs.mjs";
import { openImageLightbox } from "./image-lightbox.mjs";

// The composer's attachment pipeline: picker, paste, drag-and-drop, preview
// cards, and video pre-processing. Split out of chat-composer.mjs to keep
// that file inside the source size budget. State is shared by reference with
// the controller, so the caller passes its own `state` object in.
export function createComposerAttachments({
  attachmentJobIsCurrent,
  attachmentKind,
  beginAttachmentProcessing,
  clipboardFiles,
  finishAttachmentProcessing,
  invalidateAttachmentProcessing,
  isAttachmentProcessing,
  prepareVideoAttachment,
  previewPendingImage,
  showToast,
  state,
  syncMessageComposerBusy,
} = {}) {
  function openAttachmentPicker() {
    const input = $("attachFileInput");
    if (!input || input.disabled) return;
    input.value = "";
    input.click();
  }

  function attachmentId() {
    return `att-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
  }

  function videoAttachmentCandidate(file) {
    const type = String(file?.type || "").toLowerCase();
    const name = String(file?.name || "").toLowerCase();
    return type.startsWith("video/") || /\.(mp4|webm)$/.test(name);
  }

  function videoAttachmentFailureLabel(error) {
    const key = {
      "unsupported-type": "workspace.chat.videoUnsupported",
      "source-too-large": "workspace.chat.videoSourceTooLarge",
      "duration-too-long": "workspace.chat.videoDurationTooLong",
      "derived-budget-exceeded": "workspace.chat.videoDerivedTooLarge",
      "message-budget-exceeded": "workspace.chat.videoMessageTooLarge",
    }[error?.code] || "workspace.chat.videoProcessingFailed";
    return t(key);
  }

  function pendingAttachmentForFile(file, groupId = "") {
    const kind = attachmentKind(file);
    return {
      id: attachmentId(),
      file,
      kind,
      groupId,
      previewUrl: kind === "image" ? URL.createObjectURL(file) : "",
    };
  }

  function releasePendingAttachmentPreviews(attachments) {
    (Array.isArray(attachments) ? attachments : []).forEach((item) => {
      if (item?.previewUrl) URL.revokeObjectURL(item.previewUrl);
    });
  }

  function attachmentCancellationError() {
    return Object.assign(new Error("Attachment processing became stale."), { code: "cancelled" });
  }

  async function addPendingAttachmentFiles(files) {
    const pickedFiles = Array.from(files || []).filter(Boolean);
    if (!pickedFiles.length) return { added: [], skipped: [] };
    if (isAttachmentProcessing()) {
      showToast?.(t("workspace.chat.videoProcessing"), "warn", { force: true });
      return { added: [], skipped: pickedFiles };
    }
    const maxFileBytes = 10 * 1024 * 1024;
    const maxTotalBytes = 25 * 1024 * 1024;
    const currentAttachments = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    const currentTotal = currentAttachments.reduce((sum, item) => sum + (item.file?.size || 0), 0);
    let nextTotal = currentTotal;
    const skipped = [];
    const added = [];
    const videoNotices = [];
    const hasVideo = pickedFiles.some(videoAttachmentCandidate);
    const job = hasVideo ? beginAttachmentProcessing() : null;
    try {
      for (const file of pickedFiles) {
        if (job && !attachmentJobIsCurrent(job)) throw attachmentCancellationError();
        const name = file.name || t("workspace.chat.unnamedFile");
        if (videoAttachmentCandidate(file)) {
          if (!isSupportedVideoFile(file)) {
            skipped.push(`${name} (${t("workspace.chat.videoUnsupported")})`);
            continue;
          }
          try {
            const result = await prepareVideoAttachment(file, {
              currentMessageBytes: nextTotal,
              signal: job?.controller?.signal,
            });
            if (!attachmentJobIsCurrent(job)) throw attachmentCancellationError();
            const prepared = [];
            const groupId = `video-${job.generation}-${added.length}`;
            try {
              for (const outputFile of result.files || []) {
                if (!attachmentJobIsCurrent(job)) throw attachmentCancellationError();
                prepared.push(pendingAttachmentForFile(outputFile, groupId));
                if (!attachmentJobIsCurrent(job)) throw attachmentCancellationError();
              }
            } catch (error) {
              releasePendingAttachmentPreviews(prepared);
              throw error;
            }
            if (!prepared.length) throw new Error("Video processing produced no readable attachments.");
            added.push(...prepared);
            nextTotal += Number(result.totalBytes || prepared.reduce((sum, item) => sum + (item.file?.size || 0), 0));
            videoNotices.push({
              key: result.originalIncluded ? "workspace.chat.videoOriginalIncluded" : "workspace.chat.videoFramesOnly",
              tone: result.originalIncluded ? "success" : "warn",
              name,
              count: Number(result.frameFiles?.length || 0),
            });
          } catch (error) {
            if (error?.code === "cancelled" || !attachmentJobIsCurrent(job)) throw attachmentCancellationError();
            skipped.push(`${name} (${videoAttachmentFailureLabel(error)})`);
          }
          continue;
        }
        if (file.size > maxFileBytes) {
          skipped.push(`${name}（${formatBytes(file.size)}）`);
          continue;
        }
        if (nextTotal + file.size > maxTotalBytes) {
          skipped.push(`${name} (${formatBytes(maxTotalBytes)})`);
          continue;
        }
        try {
          added.push(pendingAttachmentForFile(file));
          nextTotal += file.size;
        } catch {
          skipped.push(name);
        }
      }
      if (job && !attachmentJobIsCurrent(job)) throw attachmentCancellationError();
      if (added.length) {
        state.pendingAttachments = [...(Array.isArray(state.pendingAttachments) ? state.pendingAttachments : []), ...added];
        renderPendingAttachments();
        videoNotices.forEach((notice) => showToast?.(t(notice.key, {
          name: notice.name,
          count: notice.count,
        }), notice.tone, { force: true }));
        showToast?.(t("workspace.chat.attachmentsAdded", { count: added.length }), "success", { force: true });
      }
      if (skipped.length) {
        const preview = skipped.slice(0, 3).join("、");
        showToast?.(t("workspace.chat.attachmentsSkipped", { count: skipped.length, files: preview, suffix: skipped.length > 3 ? t("workspace.chat.more") : "" }), "warn", { force: true });
      }
      return { added, skipped };
    } catch (error) {
      if (error?.code === "cancelled" || (job && !attachmentJobIsCurrent(job))) {
        releasePendingAttachmentPreviews(added);
        return { added: [], skipped: [], cancelled: true };
      }
      releasePendingAttachmentPreviews(added);
      throw error;
    } finally {
      if (job) finishAttachmentProcessing(job);
    }
  }

  async function importAttachmentFiles(event) {
    const picker = event?.target;
    try {
      await addPendingAttachmentFiles(picker?.files || []);
    } finally {
      if (picker) picker.value = "";
    }
  }

  function handleMessagePaste(event) {
    const files = clipboardFiles(event);
    if (!files.length) return false;
    void addPendingAttachmentFiles(files);
    // Keep the browser's normal text paste and undo stack intact when the
    // clipboard contains both text and files.
    return true;
  }

  function removePendingAttachment(id) {
    invalidateAttachmentProcessing({ sync: false });
    const attachments = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    const removed = attachments.find((item) => item.id === id);
    if (removed?.previewUrl) URL.revokeObjectURL(removed.previewUrl);
    state.pendingAttachments = attachments.filter((item) => item.id !== id);
    renderPendingAttachments();
    syncMessageComposerBusy({ checkAttachmentContext: false });
  }

  function clearPendingAttachments() {
    invalidateAttachmentProcessing({ sync: false });
    const attachments = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    releasePendingAttachmentPreviews(attachments);
    state.pendingAttachments = [];
    renderPendingAttachments();
    syncMessageComposerBusy({ checkAttachmentContext: false });
  }

  // Hands the staged files to a send without destroying them, so the composer can
  // empty in the same beat as the textarea instead of waiting for the upload.
  //
  // Deliberately not clearPendingAttachments: that revokes the preview object URLs,
  // and a failed send has to put the cards back with their thumbnails intact. The
  // send owns them from here and decides which way it goes -- releasePending on
  // success, restore on failure -- so a caller that takes them must do one or the
  // other or the previews leak.
  function detachPendingAttachments() {
    const attachments = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    if (!attachments.length) return [];
    state.pendingAttachments = [];
    renderPendingAttachments();
    syncMessageComposerBusy({ checkAttachmentContext: false });
    return attachments;
  }

  // Puts detached files back after a send failed. Prepended so they keep their place
  // ahead of anything staged while the request was in flight.
  function restorePendingAttachments(attachments) {
    const restored = Array.isArray(attachments) ? attachments.filter(Boolean) : [];
    if (!restored.length) return;
    const current = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    const known = new Set(current.map((item) => item?.id).filter(Boolean));
    state.pendingAttachments = [...restored.filter((item) => !known.has(item?.id)), ...current];
    renderPendingAttachments();
    syncMessageComposerBusy({ checkAttachmentContext: false });
  }

  function renderPendingAttachments() {
    const wrap = $("pendingAttachments");
    if (!wrap) return;
    const attachments = state.pendingAttachments || [];
    wrap.classList.toggle("hidden", attachments.length === 0);
    $("composerInputShell")?.classList.toggle("has-pending-attachments", attachments.length > 0);
    const images = attachments.filter(isPendingImagePreview);
    const files = attachments.filter((item) => !isPendingImagePreview(item));
    const imageStrip = images.length
      ? `<div class="pending-image-strip">${images.map((item) => pendingAttachmentCardHTML(item)).join("")}</div>`
      : "";
    const fileStrip = files.length
      ? `<div class="pending-file-strip">${files.map((item) => pendingAttachmentCardHTML(item)).join("")}</div>`
      : "";
    wrap.innerHTML = `${imageStrip}${fileStrip}`;
    wrap.querySelectorAll("[data-remove-attachment]").forEach((button) => {
      button.addEventListener("click", (event) => {
        event.stopPropagation?.();
        removePendingAttachment(button.dataset.removeAttachment);
      });
    });
    wrap.querySelectorAll("[data-pending-image-preview]").forEach((button) => {
      button.addEventListener("click", () => {
        const item = attachments.find((attachment) => attachment.id === button.dataset.pendingImagePreview);
        if (item) void openPendingImagePreview(item);
      });
    });
  }

  function isPendingImagePreview(item) {
    return item?.kind === "image" && Boolean(item.previewUrl);
  }

  async function openPendingImagePreview(item) {
    const name = item?.file?.name || t("workspace.chat.unnamedFile");
    const opener = previewPendingImage || openImageLightbox;
    const opened = await opener({
      url: item.previewUrl,
      caption: name,
      downloadName: name,
      labels: {
        close: cr("attachment.closeViewer"),
        download: cr("imageGeneration.download"),
        copy: cr("attachment.copyImage"),
      },
    });
    if (!opened) showToast?.(cr("attachment.unavailable"), "warn");
  }

  function pendingAttachmentCardHTML(item) {
    const file = item.file || {};
    const name = file.name || t("workspace.chat.unnamedFile");
    if (item.kind === "image" && item.previewUrl) {
      return `
        <div class="pending-image-card" title="${escapeAttr(name)}">
          <button class="pending-image-open" type="button" data-pending-image-preview="${escapeAttr(item.id)}" aria-label="${escapeAttr(t("workspace.chat.previewAttachment", { name }))}">
            <img class="pending-image-thumb" src="${escapeAttr(item.previewUrl)}" alt="${escapeAttr(name)}" />
          </button>
          <button class="pending-attachment-remove" type="button" title="${escapeAttr(t("workspace.chat.removeAttachment"))}" aria-label="${escapeAttr(t("workspace.chat.removeAttachment"))}" data-remove-attachment="${escapeAttr(item.id)}">×</button>
        </div>
      `;
    }
    const subtitle = formatBytes(file.size || 0);
    const kind = item.kind || "binary";
    const title = subtitle ? `${name} · ${subtitle}` : name;
    return `
      <div class="pending-file-chip" title="${escapeAttr(title)}" data-kind="${escapeAttr(kind)}">
        <span class="pending-file-icon" data-kind="${escapeAttr(kind)}" aria-hidden="true">${attachmentGlyph(kind)}</span>
        <span class="pending-file-name">${escapeHtml(name)}</span>
        <button class="pending-attachment-remove" type="button" title="${escapeAttr(t("workspace.chat.removeAttachment"))}" aria-label="${escapeAttr(t("workspace.chat.removeAttachment"))}" data-remove-attachment="${escapeAttr(item.id)}">×</button>
      </div>
    `;
  }

  function setComposerDragging(active) {
    $("composerInputShell")?.classList.toggle("dragging", Boolean(active));
  }

  function eventHasFiles(event) {
    return Array.from(event?.dataTransfer?.types || []).includes("Files");
  }

  function handleAttachmentDragOver(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    setComposerDragging(true);
  }

  function handleAttachmentDragLeave(event) {
    const shell = $("composerInputShell");
    if (!shell || shell.contains(event.relatedTarget)) return;
    setComposerDragging(false);
  }

  function handleAttachmentDrop(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    setComposerDragging(false);
    void addPendingAttachmentFiles(event.dataTransfer?.files || []);
  }

  // Dropping onto the transcript means the same thing as dropping onto the
  // composer: the file becomes an attachment on the next message. The transcript
  // is the far bigger target and the one people actually aim at, so it takes the
  // drop as well rather than letting the browser navigate away from the app.
  //
  // Depth is counted rather than testing relatedTarget: the transcript is full of
  // nested cards and every boundary between them fires dragleave, which made the
  // hint flicker while the pointer was still well inside.
  let conversationDragDepth = 0;

  function setConversationDragging(active) {
    const host = $("messages");
    if (!host) return;
    if (active) host.dataset.dropHint = t("workspace.chat.dropHint");
    else delete host.dataset.dropHint;
    host.classList.toggle("dropping-files", Boolean(active));
  }

  function handleConversationDragEnter(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    conversationDragDepth += 1;
    setConversationDragging(true);
  }

  function handleConversationDragOver(event) {
    if (!eventHasFiles(event)) return;
    // Without preventDefault the browser handles the drop itself and opens the
    // file, discarding the whole workspace.
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
    if (!conversationDragDepth) conversationDragDepth = 1;
    setConversationDragging(true);
  }

  function handleConversationDragLeave(event) {
    if (!eventHasFiles(event)) return;
    conversationDragDepth = Math.max(0, conversationDragDepth - 1);
    if (!conversationDragDepth) setConversationDragging(false);
  }

  function handleConversationDrop(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    conversationDragDepth = 0;
    setConversationDragging(false);
    void addPendingAttachmentFiles(event.dataTransfer?.files || []);
  }

  // A file dropped just off-target would otherwise be opened by the browser,
  // navigating away and losing everything unsent. Only file drags are swallowed;
  // the sidebar's own project and conversation drags carry text/plain and still
  // reach their handlers.
  function swallowStrayFileDrop(event) {
    if (!eventHasFiles(event)) return;
    event.preventDefault();
    conversationDragDepth = 0;
    setConversationDragging(false);
  }

  return {
    openAttachmentPicker,
    attachmentId,
    videoAttachmentCandidate,
    videoAttachmentFailureLabel,
    pendingAttachmentForFile,
    releasePendingAttachmentPreviews,
    attachmentCancellationError,
    addPendingAttachmentFiles,
    importAttachmentFiles,
    handleMessagePaste,
    removePendingAttachment,
    clearPendingAttachments,
    detachPendingAttachments,
    restorePendingAttachments,
    renderPendingAttachments,
    pendingAttachmentCardHTML,
    openPendingImagePreview,
    setComposerDragging,
    eventHasFiles,
    handleAttachmentDragOver,
    handleAttachmentDragLeave,
    handleAttachmentDrop,
    setConversationDragging,
    handleConversationDragEnter,
    handleConversationDragOver,
    handleConversationDragLeave,
    handleConversationDrop,
    swallowStrayFileDrop,
  };
}
