import { $, escapeAttr, escapeHtml, setButtonBusy, setTextIfChanged } from "./dom.mjs";
import { formatBytes, formatNumber } from "./formatters.mjs";
import { chatDraftsKey, messageQueueKey, promptHistoryKey } from "./preferences-data.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs?v=goal-command-2-queue-command-1-reasoning-steps-1-reasoning-history-1-markdown-2";
import { isSupportedVideoFile, processVideoAttachment } from "./video-attachments.mjs";
import { createComposerAttachments } from "./composer-attachments.mjs";
import { createComposerPalettes } from "./composer-palettes.mjs";
import { clipboardFiles, fastModeSupportedForModel, interfaceLocale, isGoalCommandDraft, maxChatDraftCharacters, mentionTrigger, normalizeChatDrafts, normalizeMessageMode, normalizeMessageQueue, normalizePromptHistory, normalizeReasoningEffort, parseGoalCommandDraft, parseQueueCommandDraft, queueCollapseThreshold, reasoningEffortValuesForModel, resizeMessageInputElement, slashCommandsForContext, slashCommandsForEffectivePolicy, truncateChatDraft, unicodeCharacters } from "./composer-drafts.mjs";

// The stateless helpers live in composer-drafts.mjs. They are re-exported here so
// that importers, and the tests that cover them, keep a single entry point.
export { builtInSlashCommandsForContext, calculateMessageInputSize, clipboardFiles, defaultReasoningEffortValues, fastModeSupportedForModel, interfaceLocale, isGoalCommandDraft, knownReasoningEffortValues, maxChatDraftCharacters, maxQueuedMessages, mentionTrigger, normalizeChatDrafts, normalizeMessageMode, normalizeMessageQueue, normalizePromptHistory, normalizeQueuedAttachments, normalizeReasoningEffort, parseGoalCommandDraft, parseQueueCommandDraft, queueCollapseThreshold, reasoningEffortValuesForCapabilities, reasoningEffortValuesForModel, resizeMessageInputElement, slashCommandsForContext, slashCommandsForEffectivePolicy, truncateChatDraft, unicodeCharacters } from "./composer-drafts.mjs";

export function createChatComposerController({
  state,
  attachmentKind,
  currentProviderConfig,
  currentSkillsPreferences,
  getEffectiveSkillsPolicy,
  isComposingInput,
  isCurrentModelConfigured,
  awaitAgentSettingsSaved = async () => {},
  saveAgentSettings = async () => {},
  loadMessages,
  // Optional: existing callers that do not pass these keep the previous behaviour
  // of the transcript only appearing once the reload lands.
  echoPendingUserMessage = () => "",
  discardPendingUserMessage = () => false,
  notifyTerminal,
  openDirectoryChooser,
  request = api,
  scheduleMessageRefresh,
  scrollMessagesToBottom = () => {},
  showModelSetupNotice,
  showToast,
  onMessageAccepted,
  prepareVideoAttachment = processVideoAttachment,
  createAbortController = () => new AbortController(),
} = {}) {
  const pendingReasoningEfforts = new Map();
  const savingReasoningEfforts = new Set();
  const pendingFastModes = new Map();
  const savingFastModes = new Set();
  let attachmentGeneration = 0;
  let activeAttachmentJob = null;
  let queueDrainTimer = null;
  let queueDraining = false;
  let queueSequence = 0;
  // A long queue would push the composer down the screen, so past this many
  // entries the list collapses to the next few and the rest go behind a toggle.
  let queueExpanded = false;

  // Mirrors resolveComposerActivityStatus() in agent-workspace-helpers.mjs
  // rather than importing it: the composer must not take a dependency on the
  // workspace shell, and only the boolean matters here. Terminal live records
  // remain visible until the persisted run summary takes ownership, but they
  // must not keep the send button in Stop mode.
  const terminalLiveToolStatuses = new Set([
    "completed", "complete", "succeeded", "success", "done", "error", "failed",
    "rejected", "denied", "cancelled", "canceled", "interrupted", "aborted", "superseded",
  ]);

  function liveToolIsActive(item) {
    const status = String(item?.status || item?.state || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
    return !terminalLiveToolStatuses.has(status);
  }

  function agentTurnInFlight() {
    if (Object.keys(state.pendingToolApprovals || {}).length) return true;
    if (Object.values(state.liveToolOutputs || {}).some(liveToolIsActive)) return true;
    if (state.liveAssistantActive) return true;
    return String(state.agent?.status || "").trim().toLowerCase() === "running";
  }

  // The queue lives on the server so a follow-up parked on a phone is visible on
  // a desktop and gets sent even with every browser closed. localStorage is kept
  // only as a mirror, so a queue already on screen survives a reload offline.
  function loadMessageQueue() {
    try {
      return normalizeMessageQueue(JSON.parse(localStorage.getItem(messageQueueKey) || "[]"));
    } catch {
      return [];
    }
  }

  async function syncMessageQueueFromServer(agentId = state.agent?.id) {
    const id = String(agentId || "");
    if (!id) return;
    try {
      const response = await request(`/api/agents/${id}/queue`);
      // The agent id is stamped on before normalizing, not after: normalization
      // drops entries without one, so doing it the other way round threw away
      // the whole server queue and emptied the panel.
      const items = normalizeMessageQueue((Array.isArray(response?.queue) ? response.queue : [])
        .map((item) => ({ ...item, agentId: item?.agentId || id })));
      // Other agents' entries in the mirror are left alone: this response only
      // speaks for the agent that was asked about.
      const others = (Array.isArray(state.messageQueue) ? state.messageQueue : []).filter((item) => item?.agentId !== id);
      writeMessageQueue([...others, ...items]);
      renderMessageQueue();
    } catch {
      // An unreachable server must not wipe what is already on screen.
    }
  }

  function writeMessageQueue(queue) {
    const normalized = normalizeMessageQueue(queue);
    state.messageQueue = normalized;
    try {
      localStorage.setItem(messageQueueKey, JSON.stringify(normalized));
    } catch {
      // A full or blocked store must not cost the user the message they just
      // parked; the in-memory queue above still drains this session.
    }
    return normalized;
  }

  function queuedMessages(agentId = state.agent?.id) {
    const id = String(agentId || "");
    if (!Array.isArray(state.messageQueue)) state.messageQueue = loadMessageQueue();
    return state.messageQueue.filter((item) => item?.agentId === id);
  }

  // Attachments are metadata here on purpose: the bytes live on the server with
  // the parked row, so the chip only has to say what is coming along.
  function renderQueuedAttachmentsHTML(item) {
    const attachments = Array.isArray(item?.attachments) ? item.attachments : [];
    if (!attachments.length) return "";
    return `<span class="message-queue-attachments">${attachments.map((attachment) => `
      <span class="message-queue-attachment" title="${escapeAttr(attachment.filename)}">
        <svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">${attachment.kind === "image"
          ? `<rect x="3.5" y="5" width="17" height="14" rx="2.5"></rect><circle cx="9" cy="10.5" r="1.6"></circle><path d="m5 17 4.5-4 3.5 3 2.5-2.5 4 3.5"></path>`
          : `<path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8z"></path><path d="M14 3.5V8h4.5"></path>`}</svg>
        <span class="message-queue-attachment-name">${escapeHtml(attachment.filename)}</span>
      </span>`).join("")}</span>`;
  }

  function renderMessageQueue() {
    const host = $("messageQueue");
    if (!host) return;
    const pending = queuedMessages();
    // Parking a message is what makes the mirror worth polling, and the drain is
    // also what clears a row the backend has already sent. Starting it from the
    // render covers every path that can leave something parked -- enqueue, a
    // server sync, an agent switch, a fresh page load -- and scheduleQueueDrain
    // is a no-op once a timer is live.
    if (pending.length) scheduleQueueDrain();
    host.classList.toggle("hidden", pending.length === 0);
    if (!pending.length) {
      queueExpanded = false;
      host.innerHTML = "";
      return;
    }
    const collapsible = pending.length > queueCollapseThreshold;
    const visible = collapsible && !queueExpanded ? pending.slice(0, queueCollapseThreshold) : pending;
    const hiddenCount = pending.length - visible.length;
    const toggleLabel = queueExpanded
      ? t("workspace.chat.queueCollapse")
      : t("workspace.chat.queueExpand", { count: hiddenCount });
    // No heading: the row order and the text are the whole point, and a count
    // above a list the user can already see was just a line of chrome between the
    // backlog and the run it is waiting on.
    host.innerHTML = `
      <ol class="message-queue-list">
        ${visible.map((item, index) => `
          <li class="message-queue-item">
            <span class="message-queue-index">${index + 1}</span>
            <span class="message-queue-text">${item.text.trim() ? escapeHtml(item.text) : `<em class="message-queue-attachments-only">${escapeHtml(t("workspace.chat.queueAttachmentsOnly"))}</em>`}</span>
            ${renderQueuedAttachmentsHTML(item)}
            <span class="message-queue-actions">
              <button class="message-queue-edit" type="button" data-queue-edit="${escapeAttr(item.id)}" title="${escapeAttr(t("workspace.chat.queueEdit"))}" aria-label="${escapeAttr(t("workspace.chat.queueEdit"))}"><svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M4 20h4l10-10a2.5 2.5 0 0 0-3.5-3.5L4.5 16.5z"></path><path d="m13.5 7 3.5 3.5"></path></svg></button>
              <button class="message-queue-drop" type="button" data-queue-drop="${escapeAttr(item.id)}" title="${escapeAttr(t("workspace.chat.queueDrop"))}" aria-label="${escapeAttr(t("workspace.chat.queueDrop"))}"><svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg></button>
            </span>
          </li>
        `).join("")}
      </ol>
      ${collapsible ? `<button class="message-queue-toggle" type="button" data-queue-toggle aria-expanded="${queueExpanded ? "true" : "false"}">${escapeHtml(toggleLabel)}</button>` : ""}
    `;
    host.querySelectorAll("[data-queue-drop]").forEach((node) => {
      node.addEventListener("click", () => dropQueuedMessage(node.dataset.queueDrop));
    });
    host.querySelectorAll("[data-queue-edit]").forEach((node) => {
      node.addEventListener("click", () => editQueuedMessage(node.dataset.queueEdit));
    });
    host.querySelector?.("[data-queue-toggle]")?.addEventListener("click", () => {
      queueExpanded = !queueExpanded;
      renderMessageQueue();
    });
  }

  // Which parked row the composer is currently rewriting, when that row carries
  // attachments. Held here rather than in the queue entry so a server sync cannot
  // drop it mid-edit.
  let queueEditTarget = null;

  function editQueuedMessageTextInPlace(item) {
    const input = $("messageText");
    if (!input) return;
    // A half-typed draft would be lost by loading the parked text over it, and
    // unlike the plain path there is no free slot to park it in, since the row
    // being edited is staying put.
    if (input.value.trim() && input.value.trim() !== String(item.text || "").trim()) {
      showToast?.(t("workspace.chat.queueEditComposerBusy"), "warn", { force: true });
      return;
    }
    queueEditTarget = { id: item.id, agentId: item.agentId || state.agent?.id || "" };
    input.value = String(item.text || "");
    autoResizeMessageInput();
    syncMessageComposerBusy({ checkAttachmentContext: false });
    input.focus();
    input.setSelectionRange?.(input.value.length, input.value.length);
    showToast?.(t("workspace.chat.queueEditingAttachments", { count: item.attachments.length }), "info");
  }

  function clearQueueEditTarget() {
    queueEditTarget = null;
  }

  // Returns true when the send was consumed as an edit of a parked row.
  async function commitQueueEdit(agentId, text) {
    if (!queueEditTarget || queueEditTarget.agentId !== agentId) return false;
    const target = queueEditTarget;
    const row = (state.messageQueue || []).find((entry) => entry?.id === target.id);
    if (!row) {
      clearQueueEditTarget();
      return false;
    }
    clearQueueEditTarget();
    writeMessageQueue((state.messageQueue || []).map((entry) => (entry?.id === target.id ? { ...entry, text } : entry)));
    renderMessageQueue();
    try {
      await request(`/api/agents/${agentId}/queue/${encodeURIComponent(target.id)}`, {
        method: "PUT",
        body: JSON.stringify({ text }),
      });
      await syncMessageQueueFromServer(agentId);
    } catch (err) {
      await syncMessageQueueFromServer(agentId).catch(() => {});
      showToast?.(t("workspace.chat.queueEditFailed", { error: err?.message || String(err) }), "warn", { force: true });
    }
    return true;
  }

  function dropQueuedMessage(id) {
    const key = String(id || "");
    if (!key) return;
    if (queueEditTarget?.id === key) clearQueueEditTarget();
    const agentId = state.agent?.id;
    // Drop locally first so the row disappears under the finger that tapped it,
    // then confirm with the server and resync if it disagreed.
    writeMessageQueue((state.messageQueue || []).filter((item) => item?.id !== key));
    renderMessageQueue();
    if (agentId) {
      request(`/api/agents/${agentId}/queue/${encodeURIComponent(key)}`, { method: "DELETE" })
        .catch(() => syncMessageQueueFromServer(agentId));
    }
  }

  // Editing lifts the message back into the composer rather than opening an
  // inline field: the composer is where mode, context and history already live,
  // and re-submitting parks it again while the run is still going.
  function editQueuedMessage(id) {
    const key = String(id || "");
    if (!key) return;
    const item = (state.messageQueue || []).find((entry) => entry?.id === key);
    if (!item) return;
    const input = $("messageText");
    if (!input) return;
    // Attachments live on the server with the queue row; the composer only ever
    // held metadata, so pulling the row back cannot restore the files. Editing in
    // place keeps them, where delete-and-repost silently dropped them.
    if (Array.isArray(item.attachments) && item.attachments.length) {
      editQueuedMessageTextInPlace(item);
      return;
    }
    const pending = String(input.value || "");
    // Anything half-typed goes back to the front of the queue instead of being
    // overwritten by the message being edited.
    const rest = (state.messageQueue || []).filter((entry) => entry?.id !== key);
    writeMessageQueue(rest);
    input.value = item.text;
    renderMessageQueue();
    const agentId = state.agent?.id;
    if (agentId) {
      // The message is being edited here, so it leaves the shared queue; if the
      // composer already held text, that text takes its place rather than being
      // thrown away.
      request(`/api/agents/${agentId}/queue/${encodeURIComponent(key)}`, { method: "DELETE" })
        .then(() => (pending.trim()
          ? request(`/api/agents/${agentId}/queue`, {
            method: "POST",
            body: JSON.stringify({ text: pending, mode: item.mode, context: item.context }),
          })
          : null))
        .then(() => syncMessageQueueFromServer(agentId))
        .catch(() => syncMessageQueueFromServer(agentId));
    }
    autoResizeMessageInput();
    syncMessageComposerBusy({ checkAttachmentContext: false });
    input.focus();
    input.setSelectionRange?.(input.value.length, input.value.length);
    saveCurrentChatDraft();
  }

  function enqueueMessage(agentId, text, mode, context, attachments = []) {
    queueSequence += 1;
    const localId = `queued-${Date.now()}-${queueSequence}`;
    const files = Array.isArray(attachments) ? attachments : [];
    // Shown immediately, then reconciled: the server owns the real id and the
    // send, but the user should see their message parked without a round trip.
    writeMessageQueue([...(Array.isArray(state.messageQueue) ? state.messageQueue : []), {
      id: localId,
      agentId: String(agentId || ""),
      text,
      mode,
      context,
      attachments: files.map((item) => ({
        id: "",
        filename: item.file?.name || item.name || "attachment",
        kind: String(item.file?.type || item.mimeType || "").startsWith("image/") ? "image" : "file",
        mimeType: item.file?.type || item.mimeType || "",
        sizeBytes: Number(item.file?.size || item.sizeBytes || 0),
      })),
    }]);
    renderMessageQueue();
    if (!agentId) return;
    // Files have to go up as multipart; the JSON body stays for the common case
    // so parking plain text is still one small request.
    let body;
    if (files.length) {
      body = new FormData();
      body.append("text", text);
      body.append("mode", mode);
      body.append("context", context);
      files.forEach((item) => body.append("files", item.file, item.file?.name || "attachment"));
    } else {
      body = JSON.stringify({ text, mode, context });
    }
    request(`/api/agents/${agentId}/queue`, {
      method: "POST",
      body,
    }).then(() => syncMessageQueueFromServer(agentId)).catch((err) => {
      writeMessageQueue((state.messageQueue || []).filter((item) => item?.id !== localId));
      renderMessageQueue();
      showToast?.(t("workspace.chat.queueSendFailed", { error: err?.message || String(err) }), "warn", { force: true });
    });
  }

  function scheduleQueueDrain() {
    if (queueDrainTimer) return;
    if (!queuedMessages().length) return;
    // The server owns sending now, so this tick only mirrors its view of the
    // queue: it is what makes a message queued on another device disappear here
    // once the backend has sent it.
    queueDrainTimer = setInterval(() => {
      if (!queuedMessages().length) {
        clearInterval(queueDrainTimer);
        queueDrainTimer = null;
        return;
      }
      drainMessageQueue().catch(() => {});
    }, 1500);
    // Browsers hand back a plain number here. Under Node this is a Timeout that
    // would hold the event loop open on its own, so a test that leaves something
    // parked would never let the process exit.
    queueDrainTimer?.unref?.();
  }

  // Sending moved to the backend so a queue drains with every browser closed.
  // This now only reconciles against the server and pulls in whatever it sent,
  // which also keeps two open devices from both posting the same message.
  async function drainMessageQueue() {
    if (queueDraining) return;
    const agentId = state.agent?.id;
    if (!agentId) return;
    const before = queuedMessages(agentId).length;
    queueDraining = true;
    try {
      await syncMessageQueueFromServer(agentId);
      // Fewer entries means the backend started one of them, so bring the
      // conversation into line with the run that is now underway.
      if (queuedMessages(agentId).length < before) {
        await loadMessages(agentId);
        scheduleMessageRefresh(1200, agentId, { skipWhileActive: true });
      }
    } finally {
      queueDraining = false;
    }
  }

  function loadChatDrafts() {
    try {
      return normalizeChatDrafts(JSON.parse(localStorage.getItem(chatDraftsKey) || "{}"));
    } catch {
      return {};
    }
  }

  function currentChatDrafts() {
    if (!state.chatDrafts || typeof state.chatDrafts !== "object") state.chatDrafts = loadChatDrafts();
    return state.chatDrafts;
  }

  function currentChatDraftKey() {
    return state.agent?.id || state.workline?.id || state.project?.id || "global";
  }

  function serverDraftState(agentId = state.agent?.id) {
    if (!state.serverDrafts || typeof state.serverDrafts !== "object") state.serverDrafts = {};
    if (!agentId) return null;
    // epoch rises every time the draft is cleared. A save that was already in
    // flight when the message went out belongs to the previous epoch and must not
    // be allowed to write, or it restores the text the user just sent.
    if (!state.serverDrafts[agentId]) state.serverDrafts[agentId] = { enabled: false, version: 0, seq: 0, timer: null, epoch: 0 };
    return state.serverDrafts[agentId];
  }

  function writeChatDrafts(drafts) {
    state.chatDrafts = normalizeChatDrafts(drafts);
    try {
      localStorage.setItem(chatDraftsKey, JSON.stringify(state.chatDrafts));
    } catch {}
  }

  function saveChatDraftForKey(key, value) {
    const id = String(key || "").trim();
    if (!id) return;
    const drafts = { ...currentChatDrafts() };
    const { text } = truncateChatDraft(value);
    if (text.trim()) drafts[id] = text;
    else delete drafts[id];
    writeChatDrafts(drafts);
  }

  async function persistServerDraft(agentId, value, epoch) {
    const draftState = serverDraftState(agentId);
    if (!draftState?.enabled) return;
    // The send already deleted this draft, so writing now would put the sent text
    // back on the server and hand it to the composer on the next restore.
    if (epoch !== undefined && draftState.epoch !== epoch) return;
    const result = await api(`/api/agents/${agentId}/draft`, {
      method: "PUT",
      body: JSON.stringify({ text: truncateChatDraft(value).text, version: draftState.version }),
    });
    if (draftState.epoch !== epoch && epoch !== undefined) return;
    if (state.agent?.id === agentId) draftState.version = Number(result?.version || draftState.version + 1);
  }

  function scheduleServerDraftSave(agentId, value) {
    const draftState = serverDraftState(agentId);
    if (!draftState?.enabled) return;
    window.clearTimeout(draftState.timer);
    const epoch = draftState.epoch;
    draftState.timer = window.setTimeout(() => {
      persistServerDraft(agentId, value, epoch).catch(async (error) => {
        // A 409 means someone else moved the draft on. Re-reading the version and
        // writing again is right for a genuine conflict between two editors, but
        // not when the conflict is our own send having just deleted the draft:
        // that retry is what resurrected the sent message in the composer. The
        // epoch separates the two cases.
        if (error?.status === 409 && draftState.epoch === epoch) {
          try {
            const latest = await api(`/api/agents/${agentId}/draft`);
            if (draftState.epoch !== epoch) return;
            draftState.version = Number(latest?.version || 0);
            await persistServerDraft(agentId, value, epoch);
            return;
          } catch (retryError) {
            error = retryError;
          }
        } else if (error?.status === 409) {
          return;
        }
        notifyTerminal?.(`[warn] 私有草稿保存失败：${error?.message || error}\n`);
      });
    }, 400);
  }

  function saveCurrentChatDraft() {
    const input = $("messageText");
    if (!input) return;
    const agentId = state.agent?.id;
    const draftState = serverDraftState(agentId);
    if (agentId && draftState?.enabled) {
      scheduleServerDraftSave(agentId, input.value);
      return;
    }
    saveChatDraftForKey(currentChatDraftKey(), input.value);
  }

  async function restoreCurrentChatDraft() {
    const agentId = state.agent?.id;
    const localDraft = currentChatDrafts()[currentChatDraftKey()] || "";
    setMessageInputValue(localDraft, { saveDraft: false });
    if (agentId && state.authUser) {
      const draftState = serverDraftState(agentId);
      const seq = ++draftState.seq;
      try {
        const draft = await api(`/api/agents/${agentId}/draft`);
        if (state.agent?.id !== agentId || draftState.seq !== seq) return;
        draftState.enabled = true;
        draftState.version = Number(draft?.version || 0);
        saveChatDraftForKey(currentChatDraftKey(), "");
        setMessageInputValue(draft?.contentText || "", { saveDraft: false });
        return;
      } catch (error) {
        if (state.agent?.id !== agentId || draftState.seq !== seq) return;
        if (error?.status === 404) {
          draftState.enabled = true;
          draftState.version = 0;
          saveChatDraftForKey(currentChatDraftKey(), "");
          setMessageInputValue("", { saveDraft: false });
          return;
        }
        if (error?.status !== 401) {
          notifyTerminal?.(`[warn] ${t("workspace.chat.draftFallback", { message: error?.message || error })}\n`);
        }
      }
    }
  }

  function clearChatDraftForKey(key) {
    const agentId = state.agent?.id;
    const draftState = serverDraftState(agentId);
    if (agentId && draftState?.enabled) {
      window.clearTimeout(draftState.timer);
      // Retiring the epoch cancels a save that is already on the wire as well as
      // one still waiting on the debounce. Clearing the timer alone only covered
      // the second, which is why sending inside the 400ms debounce window left the
      // message sitting in the composer afterwards.
      draftState.epoch += 1;
      draftState.version = 0;
      api(`/api/agents/${agentId}/draft`, { method: "DELETE" }).catch(() => {});
      // The local fallback is also cleared: a draft saved before the server route
      // was known to work would otherwise survive the send.
      saveChatDraftForKey(key, "");
      return;
    }
    saveChatDraftForKey(key, "");
  }

  function reasoningEffortLabel(value) {
    return {
      auto: t("modelProvider.automatic"),
      low: t("modelProvider.low"),
      medium: t("modelProvider.medium"),
      high: t("modelProvider.high"),
      xhigh: t("staticExtra.chat.ultraHighEffort"),
      max: t("staticExtra.chat.maxEffort"),
      ultra: t("staticExtra.chat.ultraEffort"),
    }[value] || t("modelProvider.automatic");
  }

  // Mobile shows a single English initial: the compact composer row has no space
  // for the full word, and the localized label is what the desktop trigger uses.
  // 0 for auto, then one bar per step. xhigh, max and ultra all sit at the top
  // of a three-bar scale: they differ from each other by degree, not by
  // anything three bars can show, and the label beside the icon still names
  // which one is selected.
  function reasoningEffortIconLevel(value) {
    return { auto: 0, low: 1, medium: 2, high: 3, xhigh: 3, max: 3, ultra: 3 }[value] ?? 0;
  }

  // On a phone the level is the label: there is no room for words, so these have
  // to be distinguishable from each other on their own. "max" used to share "M"
  // with "medium", which made the strongest and the middle setting look alike --
  // the one pair where confusing them costs the most.
  function reasoningEffortMobileLabel(value) {
    return {
      auto: "A",
      low: "L",
      medium: "M",
      high: "H",
      xhigh: "X",
      max: "MX",
      ultra: "UX",
    }[value] || "A";
  }

  function reasoningEffortValues(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    const provider = currentProviderConfig?.(modelValue) || null;
    return reasoningEffortValuesForModel(provider, modelValue);
  }

  function currentReasoningEffort(modelValue) {
    const values = reasoningEffortValues(modelValue);
    return normalizeReasoningEffort(state.agent?.reasoningEffort, values);
  }

  function reasoningEffortSavingFor(agentId = state.agent?.id) {
    return Boolean(agentId && savingReasoningEfforts.has(agentId));
  }

  function syncReasoningEffortSavingState() {
    state.reasoningEffortSaving = reasoningEffortSavingFor();
  }

  function refreshReasoningEffortControl({ modelValue, requestedValue } = {}) {
    const select = $("reasoningEffort");
    if (!select) return "auto";
    const values = reasoningEffortValues(modelValue);
    const selected = requestedValue === undefined
      ? currentReasoningEffort(modelValue)
      : normalizeReasoningEffort(requestedValue, values);
    const saving = reasoningEffortSavingFor();
    syncReasoningEffortSavingState();
    select.innerHTML = values.map((value) => `<option value="${escapeAttr(value)}">${escapeHtml(reasoningEffortLabel(value))}</option>`).join("");
    select.value = selected;
    select.disabled = !state.agent || values.length <= 1 || saving;
    select.setAttribute("aria-busy", saving ? "true" : "false");
    select.dataset.supported = values.length > 1 ? "true" : "false";
    // The row reads as icons, so the level has to be visible in the icon and
    // not only in the label beside it: three bars, lit up to the selected
    // level. auto lights none, which is what "let the model decide" looks
    // like next to an explicit choice.
    const effortIcon = $("reasoningEffortIcon");
    if (effortIcon) effortIcon.dataset.effortLevel = String(reasoningEffortIconLevel(selected));
    const display = $("reasoningEffortDisplay");
    if (display) {
      setTextIfChanged(display, reasoningEffortLabel(selected));
      (display.dataset ||= {}).mobileLabel = reasoningEffortMobileLabel(selected);
    }
    // The visible control is the custom trigger, not the native select, so it
    // has to mirror the disabled state or it looks clickable while offering
    // nothing to choose (e.g. providers without reasoning-effort support).
    const trigger = select.parentElement?.querySelector?.('[data-composer-select="reasoningEffort"]');
    if (trigger) {
      const unsupported = values.length <= 1;
      trigger.disabled = Boolean(select.disabled);
      trigger.classList?.toggle?.("is-unsupported", unsupported);
      if (unsupported) trigger.title = t("chat.reasoningEffortUnsupported");
      else trigger.removeAttribute?.("title");
    }
    const pill = select.closest?.(".reasoning-effort-pill");
    pill?.classList.toggle("reasoning-effort-unsupported", values.length <= 1);
    pill?.classList.toggle("reasoning-effort-saving", saving);
    return selected;
  }

  function selectedReasoningEffort(modelValue) {
    const select = $("reasoningEffort");
    const values = reasoningEffortValues(modelValue);
    return normalizeReasoningEffort(select?.value ?? state.agent?.reasoningEffort, values);
  }

  function agentEntityGeneration(agent = state.agent) {
    const generation = Number(agent?.entityGeneration);
    return Number.isSafeInteger(generation) && generation >= 0 ? generation : 0;
  }

  async function saveReasoningEffort(value = $("reasoningEffort")?.value) {
    const agentId = state.agent?.id || "";
    if (!agentId) return null;
    const selected = normalizeReasoningEffort(value, reasoningEffortValues());
    const agentAtStart = state.agent;
    const persistedAtStart = agentAtStart.reasoningEffort;
    pendingReasoningEfforts.set(agentId, {
      effort: selected,
      model: String(agentAtStart.model || ""),
      entityGeneration: agentEntityGeneration(agentAtStart),
    });
    state.agent = { ...agentAtStart, reasoningEffort: selected };
    refreshReasoningEffortControl({ requestedValue: selected });
    if (reasoningEffortSavingFor(agentId)) return null;

    let updatedAgent = null;
    let persistedEffort = persistedAtStart;
    let lastRequest = null;
    savingReasoningEfforts.add(agentId);
    syncReasoningEffortSavingState();
    try {
      while (pendingReasoningEfforts.has(agentId)) {
        const next = pendingReasoningEfforts.get(agentId);
        pendingReasoningEfforts.delete(agentId);
        const current = state.agent;
        // A model change makes an already queued effort request stale. Do not
        // let it mutate the new model, and do not let its eventual response
        // replace the new model's authoritative state.
        if (!current || current.id !== agentId || current.model !== next.model) continue;
        if (agentEntityGeneration(current) !== next.entityGeneration) {
          next.entityGeneration = agentEntityGeneration(current);
        }
        lastRequest = { ...next };
        refreshReasoningEffortControl({ requestedValue: next.effort });
        updatedAgent = await request(`/api/agents/${agentId}/reasoning-effort`, {
          method: "PATCH",
          body: JSON.stringify({
            reasoningEffort: next.effort,
            model: next.model,
            entityGeneration: next.entityGeneration,
          }),
        });
        const stillCurrent = state.agent?.id === agentId
          && state.agent.model === next.model
          && agentEntityGeneration(state.agent) === next.entityGeneration;
        if (!stillCurrent) continue;
        if (updatedAgent?.id === agentId && updatedAgent.model === next.model) {
          state.agent = updatedAgent;
          persistedEffort = updatedAgent.reasoningEffort ?? next.effort;
        } else {
          state.agent = { ...state.agent, reasoningEffort: next.effort };
          persistedEffort = next.effort;
        }
      }
      return updatedAgent;
    } catch (error) {
      pendingReasoningEfforts.delete(agentId);
      const current = state.agent;
      const requestIsStillCurrent = current?.id === agentId
        && current.model === lastRequest?.model
        && agentEntityGeneration(current) === lastRequest?.entityGeneration;
      if (requestIsStillCurrent) {
        state.agent = { ...current, reasoningEffort: persistedEffort };
      }
      // A stale request may legitimately lose its race to a model change. It
      // is already obsolete, so preserve the new state without surfacing it as
      // a failed update for the current model.
      if (!requestIsStillCurrent) return null;
      throw error;
    } finally {
      savingReasoningEfforts.delete(agentId);
      syncReasoningEffortSavingState();
      if (state.agent?.id === agentId) refreshReasoningEffortControl();
    }
  }

  function fastModeSupported(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    return fastModeSupportedForModel(currentProviderConfig?.(modelValue) || null, modelValue);
  }

  function fastModeSavingFor(agentId = state.agent?.id) {
    return Boolean(agentId && savingFastModes.has(agentId));
  }

  function syncFastModeSavingState() {
    state.fastModeSaving = fastModeSavingFor();
  }

  function refreshFastModeControl({ modelValue, requestedValue } = {}) {
    const button = $("openProviderLoginBtn");
    if (!button) return false;
    const supported = Boolean(state.agent) && fastModeSupported(modelValue);
    const active = supported && Boolean(requestedValue === undefined ? state.agent?.fastMode : requestedValue);
    const saving = fastModeSavingFor();
    syncFastModeSavingState();
    button.classList.toggle("hidden", !supported);
    button.classList.toggle("fast-mode-active", active);
    button.disabled = !supported || saving;
    button.dataset.supported = supported ? "true" : "false";
    button.setAttribute("aria-pressed", active ? "true" : "false");
    button.setAttribute("aria-busy", saving ? "true" : "false");
    const label = active ? t("chat.fastModeEnabled") : t("chat.fastModeDisabled");
    button.title = label;
    button.setAttribute("aria-label", label);
    return active;
  }

  async function saveFastMode(value) {
    const agentId = state.agent?.id || "";
    if (!agentId) return null;
    const agentAtStart = state.agent;
    const selected = Boolean(value) && fastModeSupported(agentAtStart.model);
    const persistedAtStart = Boolean(agentAtStart.fastMode);
    pendingFastModes.set(agentId, {
      fastMode: selected,
      model: String(agentAtStart.model || ""),
      entityGeneration: agentEntityGeneration(agentAtStart),
    });
    state.agent = { ...agentAtStart, fastMode: selected };
    refreshFastModeControl({ requestedValue: selected });
    if (fastModeSavingFor(agentId)) return null;

    let updatedAgent = null;
    let persistedFastMode = persistedAtStart;
    let lastRequest = null;
    savingFastModes.add(agentId);
    syncFastModeSavingState();
    try {
      while (pendingFastModes.has(agentId)) {
        const next = pendingFastModes.get(agentId);
        pendingFastModes.delete(agentId);
        const current = state.agent;
        if (!current || current.id !== agentId || current.model !== next.model) continue;
        if (agentEntityGeneration(current) !== next.entityGeneration) {
          next.entityGeneration = agentEntityGeneration(current);
        }
        lastRequest = { ...next };
        refreshFastModeControl({ requestedValue: next.fastMode });
        updatedAgent = await request(`/api/agents/${agentId}/fast-mode`, {
          method: "PATCH",
          body: JSON.stringify({
            fastMode: next.fastMode,
            model: next.model,
            entityGeneration: next.entityGeneration,
          }),
        });
        const stillCurrent = state.agent?.id === agentId
          && state.agent.model === next.model
          && agentEntityGeneration(state.agent) === next.entityGeneration;
        if (!stillCurrent) continue;
        if (updatedAgent?.id === agentId && updatedAgent.model === next.model) {
          state.agent = updatedAgent;
          persistedFastMode = Boolean(updatedAgent.fastMode);
        } else {
          state.agent = { ...state.agent, fastMode: next.fastMode };
          persistedFastMode = next.fastMode;
        }
      }
      return updatedAgent;
    } catch (error) {
      pendingFastModes.delete(agentId);
      const current = state.agent;
      const requestIsStillCurrent = current?.id === agentId
        && current.model === lastRequest?.model
        && agentEntityGeneration(current) === lastRequest?.entityGeneration;
      if (requestIsStillCurrent) {
        state.agent = { ...current, fastMode: persistedFastMode };
      }
      if (!requestIsStillCurrent) return null;
      throw error;
    } finally {
      savingFastModes.delete(agentId);
      syncFastModeSavingState();
      if (state.agent?.id === agentId) refreshFastModeControl();
    }
  }

  function toggleFastMode() {
    if (!state.agent || !fastModeSupported()) return Promise.resolve(null);
    return saveFastMode(!Boolean(state.agent.fastMode));
  }

  function loadPromptHistory() {
    try {
      return normalizePromptHistory(JSON.parse(localStorage.getItem(promptHistoryKey) || "[]"));
    } catch {
      return [];
    }
  }

  function currentPromptHistory() {
    if (!Array.isArray(state.promptHistory)) state.promptHistory = loadPromptHistory();
    return state.promptHistory;
  }

  function savePromptHistory(history) {
    state.promptHistory = normalizePromptHistory(history);
    state.promptHistoryIndex = -1;
    state.promptHistoryDraft = "";
    try {
      localStorage.setItem(promptHistoryKey, JSON.stringify(state.promptHistory));
    } catch {}
    updatePromptHistoryHint();
  }

  function rememberPromptHistory(text) {
    const prompt = String(text || "").trim();
    if (!prompt) return;
    const next = [prompt, ...currentPromptHistory().filter((item) => item.toLowerCase() !== prompt.toLowerCase())];
    savePromptHistory(next);
  }

  function resetPromptHistoryNavigation() {
    state.promptHistoryIndex = -1;
    state.promptHistoryDraft = "";
  }

  function messageModeFor(agentId = state.agent?.id) {
    const saved = agentId ? state.messageModes?.[agentId] : "";
    return normalizeMessageMode(saved, state.agent?.planMode === true ? "plan" : "execute");
  }

  // The plan/execute toggle was removed from the composer; the permission menu
  // carries the message-mode options instead. Mode now lives purely in
  // state.messageModes, so this just resolves the effective value.
  function refreshMessageModeControl({ requestedMode } = {}) {
    return normalizeMessageMode(requestedMode ?? messageModeFor(), state.agent?.planMode === true ? "plan" : "execute");
  }

  function setMessageMode(value) {
    const agentId = state.agent?.id;
    const mode = normalizeMessageMode(value, state.agent?.planMode === true ? "plan" : "execute");
    if (agentId) state.messageModes = { ...(state.messageModes || {}), [agentId]: mode };
    refreshMessageModeControl({ requestedMode: mode });
    return mode;
  }

  function isMessageSendingFor(agentId = state.agent?.id) {
    return Boolean(agentId && state.messageSendingByAgent?.[agentId]);
  }

  function attachmentContextKey() {
    return JSON.stringify([
      String(state.agent?.id || ""),
      String(state.workline?.id || ""),
      String(state.project?.id || ""),
      String(state.navigationSelectionKind || "conversation"),
    ]);
  }

  function isAttachmentProcessing() {
    return Number(state.pendingAttachmentProcessing || 0) > 0;
  }

  function attachmentJobIsCurrent(job) {
    return Boolean(job
      && activeAttachmentJob?.generation === job.generation
      && attachmentGeneration === job.generation
      && attachmentContextKey() === job.contextKey
      && !job.controller?.signal?.aborted);
  }

  function invalidateAttachmentProcessing({ sync = true } = {}) {
    attachmentGeneration += 1;
    const job = activeAttachmentJob;
    activeAttachmentJob = null;
    state.pendingAttachmentProcessing = 0;
    job?.controller?.abort?.();
    if (sync) syncMessageComposerBusy({ checkAttachmentContext: false });
  }

  function beginAttachmentProcessing() {
    const job = {
      generation: ++attachmentGeneration,
      contextKey: attachmentContextKey(),
      controller: createAbortController(),
    };
    activeAttachmentJob = job;
    state.pendingAttachmentProcessing = 1;
    syncMessageComposerBusy({ checkAttachmentContext: false });
    return job;
  }

  function finishAttachmentProcessing(job) {
    if (activeAttachmentJob?.generation !== job?.generation) return;
    activeAttachmentJob = null;
    state.pendingAttachmentProcessing = 0;
    syncMessageComposerBusy({ checkAttachmentContext: false });
  }

  function isComposerBusy(agentId = state.agent?.id) {
    return isMessageSendingFor(agentId) || isAttachmentProcessing();
  }

  // Push the picker's model onto the agent before anything is submitted, and
  // report whether submitting may proceed. A retry needs this as much as a send
  // does: the server resolves the model from the agent record, so retrying
  // after switching away from a model that just failed would otherwise re-run
  // on the broken one and fail again identically.
  //
  // Returns false when the caller should abort quietly because the user has
  // been shown the model setup notice. Throws when the selection cannot be
  // reconciled at all.
  async function syncSelectedModelToAgent(agentId) {
    await awaitAgentSettingsSaved(agentId);
    if (state.agent?.id !== agentId) {
      throw new Error("The active conversation changed before the message was sent.");
    }
    let selectedModel = String($("modelSelect")?.value || state.agent.model || "").trim();
    let persistedModel = String(state.agent.model || "").trim();
    if (selectedModel && selectedModel !== persistedModel) {
      // The picker selection never reached the agent -- e.g. it was chosen
      // while this conversation had no agent yet, so the save only updated
      // the model preference and never issued a model PATCH. Force one save
      // pass now that the agent exists, then re-check before refusing.
      await saveAgentSettings();
      await awaitAgentSettingsSaved(agentId);
      if (state.agent?.id !== agentId) {
        throw new Error("The active conversation changed before the message was sent.");
      }
      selectedModel = String($("modelSelect")?.value || state.agent.model || "").trim();
      persistedModel = String(state.agent.model || "").trim();
      if (selectedModel && selectedModel !== persistedModel) {
        throw new Error("The selected model could not be synchronized. Please try again.");
      }
    }
    if (!isCurrentModelConfigured()) {
      showModelSetupNotice();
      return false;
    }
    return true;
  }

  function isRetryMode() {
    // The agent status is consulted alongside the run summary because the two
    // do not land together: agent.error sets state.agent.status synchronously
    // and syncs the composer straight away, while the run summary is fetched
    // afterwards. Reading only the summary left the button saying "send" -- and
    // an empty submit doing nothing -- until some later event happened to
    // re-sync.
    const runStatus = String(state.activeRunSummary?.run?.status || "").trim().toLowerCase();
    const agentStatus = String(state.agent?.status || "").trim().toLowerCase();
    const failed = ["error", "failed"].includes(runStatus) || ["error", "failed"].includes(agentStatus);
    if (!failed) return false;
    const input = $("messageText");
    const text = input ? input.value.trim() : "";
    const attachments = Array.isArray(state.pendingAttachments) ? state.pendingAttachments : [];
    return text === "" && attachments.length === 0;
  }

  function syncMessageComposerBusy({ checkAttachmentContext = true } = {}) {
    if (checkAttachmentContext && activeAttachmentJob && activeAttachmentJob.contextKey !== attachmentContextKey()) {
      invalidateAttachmentProcessing({ sync: false });
    }
    const sending = isMessageSendingFor();
    const processing = isAttachmentProcessing();
    const busy = sending || processing;
    const input = $("messageText");
    if (input) input.disabled = sending;
    const attachButton = $("attachFileBtn");
    if (attachButton) attachButton.disabled = busy;
    const attachInput = $("attachFileInput");
    if (attachInput) attachInput.disabled = busy;
    refreshMessageModeControl();
    bindStopButton();
    // One button, three jobs. While the agent is answering it stops the run —
    // there was no way to interrupt at all, and the button was sitting there
    // disabled with nothing else to do. The composer stays usable so the next
    // message can be typed while the current answer is still streaming.
    const sendBtn = $("sendMessageBtn");
    // Typing turns Stop back into a send affordance. Submitting mid-run already
    // parks the text as a follow-up, so a button still labelled Stop was
    // offering to kill the run the user was queueing behind.
    const queueMode = !busy && agentTurnInFlight() && Boolean(input?.value?.trim?.());
    const stopMode = !busy && !queueMode && agentTurnInFlight();
    const retryMode = !busy && !stopMode && !queueMode && isRetryMode();
    if (sendBtn) {
      sendBtn.classList?.toggle?.("is-stop", stopMode);
      // Let setButtonBusy restore its saved label before applying the current
      // action. Applying the action first would make a just-finished send
      // briefly revert from Stop back to its old Send label.
      setButtonBusy(sendBtn, busy, t(processing ? "workspace.chat.videoProcessing" : "workspace.chat.sending"));
      if (!busy) {
        const label = stopMode
          ? t("workspace.chat.stopRun")
          : queueMode ? t("workspace.chat.queueSend")
          : retryMode ? t("workspace.chat.retryRun") : t("chat.send");
        const title = stopMode ? t("workspace.chat.stopRunTitle") : label;
        const ariaLabel = stopMode ? title : (queueMode || retryMode) ? label : t("chat.sendMessage");
        setTextIfChanged(sendBtn, label);
        sendBtn.title = title;
        sendBtn.setAttribute?.("aria-label", ariaLabel);
        // Compact layouts render this attribute in a pseudo-element. Keep the
        // stop affordance a single square glyph instead of overflowing a 30px
        // circular button with its localized label.
        sendBtn.dataset.mobileLabel = stopMode ? "■" : "↑";
        sendBtn.classList?.toggle?.("is-queue", queueMode);
      }
    }
  }

  async function interruptRun() {
    const agentId = state.agent?.id;
    if (!agentId) return;
    // Interrupting is not a send, so it deliberately does not take the sending
    // lock: the run is already in flight and the button must stay responsive.
    await request(`/api/agents/${agentId}/interrupt`, { method: "POST" });
    showToast?.(t("workspace.chat.stopRequested"), "info");
    syncMessageComposerBusy();
  }

  // Stop is bound as a click rather than handled inside the submit path,
  // because Enter submits this form too: typing a follow-up while an answer is
  // still streaming must not kill the run the user is waiting for. Capture
  // phase so the form's submit handler never sees the event.
  function bindStopButton() {
    const sendBtn = $("sendMessageBtn");
    if (typeof sendBtn?.addEventListener !== "function") return;
    if (!sendBtn.dataset) return;
    if (sendBtn.dataset.stopBound === "true") return;
    sendBtn.dataset.stopBound = "true";
    sendBtn.addEventListener("click", (event) => {
      if (!agentTurnInFlight()) return;
      // With text waiting, this button is a send: let the submit handler park it
      // as a follow-up instead of interrupting the run behind it.
      if ($("messageText")?.value?.trim?.()) return;
      event.preventDefault();
      event.stopPropagation();
      interruptRun().catch((error) => showToast?.(error?.message || String(error), "error", { force: true }));
    }, true);
  }

  function setMessageSendingFor(agentId, sending) {
    if (!agentId) return;
    const next = { ...(state.messageSendingByAgent || {}) };
    if (sending) next[agentId] = true;
    else delete next[agentId];
    state.messageSendingByAgent = next;
    syncMessageComposerBusy();
  }

  // Runs the last still-live user message again. Shared by the composer's
  // empty-send retry and the failure banner's retry button so both go through
  // the model sync and the busy flag rather than posting a bare rerun.
  async function rerunLastUserMessage(agentId = state.agent?.id) {
    if (!agentId || isMessageSendingFor(agentId)) return false;
    // A retired message cannot be rerun -- the server refuses it -- so pick the
    // newest user message that is still part of the conversation.
    const lastUserMessage = [...(state.currentMessages || [])].reverse()
      .find((m) => m?.role === "user" && m?.id && !m?.supersededAt);
    if (!lastUserMessage) return false;
    const context = state.navigationSelectionKind === "project" ? "project" : "conversation";
    setMessageSendingFor(agentId, true);
    try {
      // The usual reason to retry is that the model was wrong, so the picker has
      // to reach the agent first -- the rerun uses whatever the agent record says.
      if (!(await syncSelectedModelToAgent(agentId))) return false;
      await request(`/api/agents/${agentId}/messages/${encodeURIComponent(lastUserMessage.id)}/rerun`, {
        method: "POST",
        body: JSON.stringify({ context }),
      });
      await loadMessages(agentId);
      scrollMessagesToBottom?.();
      scheduleMessageRefresh(1200, agentId, { skipWhileActive: true });
      return true;
    } finally {
      setMessageSendingFor(agentId, false);
      if (state.agent?.id === agentId) scrollMessagesToBottom?.();
    }
  }

  async function sendMessage(event) {
    event.preventDefault();
    if (!state.agent) {
      await openDirectoryChooser();
      return;
    }
    const agentId = state.agent.id;
    if (isMessageSendingFor(agentId)) return;
    if (isAttachmentProcessing()) {
      showToast?.(t("workspace.chat.videoSendBlocked"), "warn", { force: true });
      return;
    }
    const draftKey = currentChatDraftKey();
    const input = $("messageText");
    const text = input.value.trim();
    const context = state.navigationSelectionKind === "project" ? "project" : "conversation";
    const mode = context === "project" ? messageModeFor(agentId) : "execute";
    const attachments = [...(state.pendingAttachments || [])];
    if (!text && !attachments.length) {
      // If the last run failed and there is nothing new to send, run the last
      // user message again. This posts a rerun rather than a correction: a
      // correction writes a fresh copy of the prompt and retires the old one,
      // so retrying six times left six near-identical messages in the
      // transcript, each marked as superseding the last.
      if (isRetryMode()) {
        await rerunLastUserMessage(agentId);
        if (state.agent?.id === agentId) input?.focus?.({ preventScroll: true });
      }
      return;
    }
    // An edit of a parked row that carries attachments rewrites that row in place
    // and consumes the send, so the files stay with it on the server.
    if (queueEditTarget && !attachments.length && text) {
      if (await commitQueueEdit(agentId, text)) {
        input.value = "";
        autoResizeMessageInput();
        clearChatDraftForKey(draftKey);
        return;
      }
    }
    const goalCommand = parseGoalCommandDraft(text);
    if (goalCommand && context !== "project") {
      showToast?.(t("workspace.chat.goalProjectOnly"), "warn", { force: true });
      return;
    }
    if (goalCommand && !goalCommand.goalText) {
      showToast?.(t("workspace.chat.goalTextRequired"), "warn", { force: true });
      return;
    }
    if (goalCommand && attachments.length) {
      showToast?.(t("workspace.chat.goalAttachmentsUnsupported"), "warn", { force: true });
      return;
    }
    // /queue parks the message, and once anything is parked every later send
    // joins the back of the line -- otherwise a plain Enter would jump ahead of
    // the follow-ups the user already lined up.
    const queueCommand = parseQueueCommandDraft(text);
    // Sending while the turn is still in flight parks the message too. Stop is a
    // separate button, so an Enter here is a follow-up rather than a request to
    // cut the run short. /goal keeps its immediate path because the queue does
    // not carry it, and silently dropping it would be worse than sending now.
    // Attachments do park: they are stored with the queue row server-side.
    const autoQueue = agentTurnInFlight() && !isGoalCommandDraft(goalCommand);
    if (queueCommand || autoQueue || (queuedMessages(agentId).length && !isGoalCommandDraft(goalCommand))) {
      const queuedText = queueCommand ? queueCommand.queuedText : text;
      // Text is only required when nothing is attached, matching the immediate
      // send path, which accepts an image on its own.
      if (!queuedText && !attachments.length) {
        showToast?.(t("workspace.chat.queueTextRequired"), "warn", { force: true });
        return;
      }
      enqueueMessage(agentId, queuedText, mode, context, attachments);
      input.value = "";
      autoResizeMessageInput();
      // Parking took ownership of the pending files. Leaving them staged would
      // silently attach them again to whatever is sent next.
      if (attachments.length) clearPendingAttachments();
      clearChatDraftForKey(draftKey);
      showToast?.(t("workspace.chat.queued", { count: queuedMessages(agentId).length }), "info");
      return;
    }
    const isGoalCommand = Boolean(goalCommand);
    setMessageSendingFor(agentId, true);
    // Declared out here because both the success and the failure paths have to settle
    // it: whichever one runs, these files are either released or handed back.
    let staged = [];
    // Puts the composer back exactly as it was. Used by the two ways a send can fall
    // over after the box has been emptied: the model sync refusing, and the POST
    // failing. Both have to undo the same three things, and doing it in one place is
    // what keeps them from drifting apart.
    const restoreComposerAfterFailedSend = () => {
      discardPendingUserMessage?.(agentId);
      if (staged.length) restorePendingAttachments(staged);
      staged = [];
      if (!input.value.trim()) input.value = text;
      autoResizeMessageInput();
    };
    try {
      // Emptied in the same synchronous beat the button became "sending", before the
      // model sync below. That sync awaits any pending settings save, so it used to
      // split the send into stages: the button changed, then a round trip, then the
      // words went. One beat means the composer never disagrees with the button.
      input.value = "";
      autoResizeMessageInput();
      // The whole composer empties at once. The text used to go here while the
      // attachment cards stayed until the upload came back, so a send with a file
      // played out in three steps: the words vanished, the cards sat there for as
      // long as the transfer took, and only then did the button change. The files are
      // taken rather than cleared, because the failure path below has to put them
      // back; success releases their previews once the turn is safely delivered.
      staged = attachments.length ? detachPendingAttachments() : [];
      // Paint the user's turn in the same beat the composer is cleared. Waiting
      // for the POST and the reload after it left the text nowhere on screen for
      // 1-2 seconds, which read as the send being stuck. /goal is excluded
      // because it is a command rather than a transcript turn.
      if (!isGoalCommand) {
        echoPendingUserMessage?.(agentId, text, attachments);
        // Refusal is not an error: an unconfigured model shows its own notice. The
        // composer has already been emptied by this point, so it has to be put back or
        // the message would be gone with nothing sent and nothing said about it.
        if (!(await syncSelectedModelToAgent(agentId))) {
          if (state.agent?.id === agentId) restoreComposerAfterFailedSend();
          else if (staged.length) releasePendingAttachmentPreviews(staged);
          return;
        }
      }
      let accepted;
      if (attachments.length) {
        const form = new FormData();
        form.append("text", text);
        form.append("mode", mode);
        form.append("context", context);
        attachments.forEach((item) => form.append("files", item.file, item.file?.name || "attachment"));
        accepted = await request(`/api/agents/${agentId}/messages`, {
          method: "POST",
          body: form,
        });
      } else {
        accepted = await request(`/api/agents/${agentId}/messages`, {
          method: "POST",
          body: JSON.stringify({ text, mode, context }),
        });
      }
      await onMessageAccepted?.(accepted, agentId);
      // The mobile keyboard collapses asynchronously after send. Ask the
      // renderer to keep the newest user message visible across those layout
      // passes instead of relying only on the pre-send near-bottom check.
      scrollMessagesToBottom?.();
      if (text) rememberPromptHistory(text);
      clearChatDraftForKey(draftKey);
      // The cards left the composer before the upload started; this is the other half
      // of that handover, freeing the preview URLs now that the turn is delivered and
      // the files will not be handed back.
      if (staged.length) releasePendingAttachmentPreviews(staged);
      // The send is done here: the server has the turn, and the echo has been on
      // screen since the composer was cleared. Holding the busy flag through the
      // transcript reload below kept the textarea *disabled* for another round
      // trip, which is the part that hurt on a phone -- over a remote tunnel you
      // could not start typing the next message for a second or more, and the
      // button still read "sending" for a message that had already been accepted.
      //
      // Releasing here is safe against a double send rather than relying on the
      // flag: onMessageAccepted has just marked the run active, so agentTurnInFlight()
      // is true and a second Enter parks the text as a follow-up instead of posting
      // it again. The error path still releases in the finally below, because it
      // never reaches this line.
      setMessageSendingFor(agentId, false);
      if (!isGoalCommand) {
        // The reload gets its own failure handling because by this point the
        // message is already delivered. Letting it fall into the catch below made
        // a failed *reload* look like a failed *send*: the draft came back in the
        // composer -- and the optimistic echo was pulled off screen -- for a turn
        // the server had accepted, which invites sending the same thing twice.
        try {
          await loadMessages(agentId);
          scrollMessagesToBottom?.();
        } catch (reloadError) {
          notifyTerminal(`[warn] The transcript reload after send failed; the message was already delivered: ${reloadError?.message || reloadError}\n`);
        }
        scheduleMessageRefresh(1200, agentId, { skipWhileActive: true });
      }
    } catch (err) {
      const stillCurrent = state.agent?.id === agentId;
      // The send failed, so the echo would otherwise keep claiming a message was
      // delivered while the draft is being restored below.
      saveChatDraftForKey(draftKey, text);
      if (stillCurrent) {
        // Text and files go back together. Restoring only the words would leave the
        // message looking ready to resend while its attachments had been dropped.
        restoreComposerAfterFailedSend();
        throw err;
      }
      // The echo still has to go even when the reader has moved on, or that
      // conversation keeps a turn that was never delivered.
      discardPendingUserMessage?.(agentId);
      // The reader has moved to another conversation, so the cards cannot go back into
      // this composer; the draft keeps the text. Release the previews here or those
      // object URLs stay allocated for the rest of the session with nothing to show.
      if (staged.length) releasePendingAttachmentPreviews(staged);
      notifyTerminal(`[warn] Message delivery to the previous agent failed; the draft was kept: ${err.message || err}\n`);
    } finally {
      // A no-op on the success path, which already released above. Still required:
      // the error path never reaches that line.
      setMessageSendingFor(agentId, false);
      if (state.agent?.id === agentId) {
        // On mobile, a plain focus() after the request completes can make the
        // browser scroll the page toward the textarea while the keyboard is
        // reopening. Keep the caret without allowing that focus operation to
        // move the message viewport, then settle the transcript tail again.
        input.focus?.({ preventScroll: true });
        // Only when the user has not started composing the next message. The
        // composer is usable during the reload now, so this landed on people who
        // had already begun typing and scrolled up to re-read something: settling
        // the tail then yanked the transcript out from under them.
        if (!input?.value?.trim?.()) scrollMessagesToBottom?.();
      }
    }
  }

  function updateDraftLimitHint() {
    const input = $("messageText");
    const hint = $("chatDraftLimitHint");
    if (!input || !hint) return;
    const length = unicodeCharacters(input.value).length;
    const locale = interfaceLocale();
    const over = Math.max(0, length - maxChatDraftCharacters);
    hint.classList.toggle("warn", over > 0);
    hint.textContent = over > 0
      ? `已超出 ${formatNumber(over, locale)} 个字符；草稿只保存前 ${formatNumber(maxChatDraftCharacters, locale)} 个字符。`
      : `${formatNumber(length, locale)} / ${formatNumber(maxChatDraftCharacters, locale)} 个字符`;
  }

  // Typing deliberately does not move the transcript. An earlier version kept
  // the tail pinned across the composer's growth, which meant every new line of
  // a draft scrolled the conversation upward under the reader -- the thing this
  // was supposed to stop. Growing the composer simply covers a little more of
  // the transcript; the send itself is what re-anchors, and that is the only
  // point at which the reader asked for the view to move.
  function autoResizeMessageInput() {
    const size = resizeMessageInputElement($("messageText"));
    updatePromptHistoryHint();
    updateDraftLimitHint();
    return size;
  }

  function scheduleMessageInputResize() {
    const schedule = globalThis.requestAnimationFrame || ((callback) => globalThis.setTimeout(callback, 0));
    schedule(() => autoResizeMessageInput());
  }

  // Attachment handling lives in its own module; it shares this closure's
  // state by reference and calls back into the composer through these
  // parameters.
  const {
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
    setComposerDragging,
    eventHasFiles,
    handleAttachmentDragOver,
    handleAttachmentDragLeave,
    handleAttachmentDrop,
    handleConversationDragEnter,
    handleConversationDragOver,
    handleConversationDragLeave,
    handleConversationDrop,
    swallowStrayFileDrop,
  } = createComposerAttachments({
    attachmentJobIsCurrent,
    attachmentKind,
    beginAttachmentProcessing,
    clipboardFiles,
    finishAttachmentProcessing,
    invalidateAttachmentProcessing,
    isAttachmentProcessing,
    prepareVideoAttachment,
    showToast,
    state,
    syncMessageComposerBusy,
  });

  function setMessageInputValue(value, { saveDraft = true } = {}) {
    const input = $("messageText");
    input.value = value;
    autoResizeMessageInput();
    updateSlashCommandPalette();
    if (saveDraft) saveCurrentChatDraft();
    window.setTimeout(() => {
      input.selectionStart = input.value.length;
      input.selectionEnd = input.value.length;
    }, 0);
  }

  function updatePromptHistoryHint() {
    const hint = $("promptHistoryHint");
    if (!hint) return;
    const count = currentPromptHistory().length;
    const commandCount = enabledSlashCommands().length;
    const active = state.promptHistoryIndex >= 0;
    hint.textContent = active
      ? t("workspace.chat.historyActive", { index: state.promptHistoryIndex + 1, count })
      : commandCount
        ? t("workspace.chat.historyCommands", { count: commandCount })
        : count
          ? t("workspace.chat.historySaved", { count })
          : t("workspace.chat.historyEmpty");
  }

  // The mention and slash-command palettes live in their own module, wired
  // the same way as the attachment pipeline above.
  const {
    hideMentionPalette,
    insertMention,
    renderMentionPalette,
    updateMentionPalette,
    handleMentionKeydown,
    enabledSlashCommands,
    slashCommandTrigger,
    slashCommandMatches,
    slashCommandOptionId,
    updateSlashCommandPalette,
    hideSlashCommandPalette,
    applySlashCommand,
    handleSlashCommandKeydown,
  } = createComposerPalettes({
    currentSkillsPreferences,
    getEffectiveSkillsPolicy,
    handleMessageInput,
    isComposingInput,
    mentionTrigger,
    resetPromptHistoryNavigation,
    setMessageInputValue,
    showToast,
    slashCommandsForContext,
    slashCommandsForEffectivePolicy,
    state,
  });

  function handlePromptHistoryNavigation(event) {
    if (isComposingInput(event) || event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) return false;
    if (event.key !== "ArrowUp" && event.key !== "ArrowDown" && event.key !== "Escape") return false;
    const input = $("messageText");
    const history = currentPromptHistory();
    if (event.key === "Escape" && state.promptHistoryIndex >= 0) {
      setMessageInputValue(state.promptHistoryDraft || "");
      resetPromptHistoryNavigation();
      updatePromptHistoryHint();
      event.preventDefault();
      return true;
    }
    if (!history.length || (input.value.trim() && state.promptHistoryIndex < 0)) return false;
    if (event.key === "ArrowUp") {
      if (state.promptHistoryIndex < 0) state.promptHistoryDraft = input.value;
      state.promptHistoryIndex = Math.min(history.length - 1, state.promptHistoryIndex + 1);
      setMessageInputValue(history[state.promptHistoryIndex] || "");
      event.preventDefault();
      return true;
    }
    if (event.key === "ArrowDown" && state.promptHistoryIndex >= 0) {
      state.promptHistoryIndex -= 1;
      setMessageInputValue(state.promptHistoryIndex >= 0 ? history[state.promptHistoryIndex] : state.promptHistoryDraft || "");
      if (state.promptHistoryIndex < 0) resetPromptHistoryNavigation();
      updatePromptHistoryHint();
      event.preventDefault();
      return true;
    }
    return false;
  }

  function handleMessageInput() {
    resetPromptHistoryNavigation();
    autoResizeMessageInput();
    syncMessageComposerBusy({ checkAttachmentContext: false });
    updateSlashCommandPalette();
    updateMentionPalette();
    saveCurrentChatDraft();
  }

  function handleMessageKeydown(event) {
    if (handleMentionKeydown(event)) return;
    if (handleSlashCommandKeydown(event)) return;
    if (handlePromptHistoryNavigation(event)) return;
    if (isComposingInput(event) || event.key !== "Enter" || event.shiftKey || event.metaKey || event.ctrlKey || event.altKey) {
      return;
    }
    event.preventDefault();
    $("messageForm").requestSubmit();
  }

  return {
    addPendingAttachmentFiles,
    autoResizeMessageInput,
    clearPendingAttachments,
    handleAttachmentDragLeave,
    handleAttachmentDragOver,
    handleAttachmentDrop,
    handleConversationDragEnter,
    handleConversationDragOver,
    handleConversationDragLeave,
    handleConversationDrop,
    swallowStrayFileDrop,
    handleMessageInput,
    handleMessageKeydown,
    handleMessagePaste,
    hideMentionPalette,
    hideSlashCommandPalette,
    importAttachmentFiles,
    loadChatDrafts,
    loadPromptHistory,
    openAttachmentPicker,
    refreshFastModeControl,
    refreshMessageModeControl,
    refreshReasoningEffortControl,
    drainMessageQueue,
    editQueuedMessage,
    loadMessageQueue,
    syncMessageQueueFromServer,
    removePendingAttachment,
    renderMessageQueue,
    restoreCurrentChatDraft,
    saveCurrentChatDraft,
    saveFastMode,
    saveReasoningEffort,
    scheduleMessageInputResize,
    setMessageMode,
    selectedReasoningEffort,
    sendMessage,
    rerunLastUserMessage,
    setMessageInputValue,
    syncMessageComposerBusy,
    toggleFastMode,
    updateDraftLimitHint,
    updateMentionPalette,
    updatePromptHistoryHint,
    updateSlashCommandPalette,
  };
}
