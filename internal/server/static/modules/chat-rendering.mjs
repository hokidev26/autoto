import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatTimestamp } from "./formatters.mjs";
import { api } from "./runtime.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";
import {
  bindProtectedDownloads,
  hydrateProtectedImages,
  loadProtectedImageURL,
} from "./protected-images.mjs";
import { openImageLightbox } from "./image-lightbox.mjs";
import { createStreamingMarkdown } from "./markdown-stream.mjs";
import {
  chatMessagePresentation,
  formatTurnUsagePerformance,
  generatedImageURL,
  isTranscriptMessageVisible,
  messageContentBlocks,
  normalizeGeneratedImageBlocks,
  normalizeImageGenerationStatusEvent,
  formatUserMessageAuthorLabel,
  normalizeMessageProfileIdentity,
  resolveUserMessageIdentity,
  normalizeTurnUsage,
  renderGeneratedImageBlocksHTML,
  clipboardPlainText,
  rewriteChatCopyEvent,
  transcriptMessageText,
  transcriptMessages,
  userMessageRoles,
} from "./chat-rendering-messages.mjs";
import {
  compactPlanStatus,
  createChatRenderingPlanCards,
  isApprovedPlanExecuteMessage,
  isPlanReflectionMessage,
  looksLikePlanDraft,
  normalizeAgentPlan,
  parsePlanDraftText,
  planCopyMarkdown,
  planForMessage,
  renderPlanSystemNoticeHTML,
} from "./chat-rendering-plan.mjs";
import { messageCopiedGlyph, messageCopyGlyph, messageCorrectGlyph } from "./chat-rendering-tools-glyphs.mjs";
import { createChatRenderingCorrection } from "./chat-rendering-correction.mjs";
import { createChatRenderingAttachments } from "./chat-rendering-attachments.mjs";
import { createChatRenderingHistory } from "./chat-rendering-history.mjs";
import {
  findToolActivityByIdentity,
  firstToolValue,
  groupToolActivityByMessage,
  isAgentToolActivity,
  maxLiveReasoningCharacters,
  maxLiveReasoningSteps,
  maxToolActivityCards,
  maxToolActivityText,
  mergeDuplicateToolActivity,
  nextToolActivitySelection,
  normalizeAgentTaskActivity,
  normalizeToolActivity,
  parseToolJSON,
  persistedReasoningSteps,
  reasoningStepTitle,
  renderAgentTaskActivityCardHTML,
  renderToolActivityCardHTML,
  renderToolActivityFactTags,
  renderToolActivitySafetySummary,
  renderToolActivityStackHTML,
  renderToolDiffHTML,
  streamedInputBlockHTML,
  toolActivityDedupeKey,
  toolActivitySafetyMetaParts,
  toolStatusValue,
} from "./chat-rendering-tools.mjs";

export {
  chatMessagePresentation,
  clipboardPlainText,
  rewriteChatCopyEvent,
  findToolActivityByIdentity,
  formatTurnUsagePerformance,
  generatedImageURL,
  groupToolActivityByMessage,
  isAgentToolActivity,
  isTranscriptMessageVisible,
  maxLiveReasoningCharacters,
  maxLiveReasoningSteps,
  messageContentBlocks,
  nextToolActivitySelection,
  looksLikePlanDraft,
  normalizeAgentPlan,
  parsePlanDraftText,
  planForMessage,
  normalizeAgentTaskActivity,
  normalizeGeneratedImageBlocks,
  normalizeImageGenerationStatusEvent,
  formatUserMessageAuthorLabel,
  normalizeMessageProfileIdentity,
  resolveUserMessageIdentity,
  normalizeToolActivity,
  normalizeTurnUsage,
  persistedReasoningSteps,
  reasoningStepTitle,
  renderAgentTaskActivityCardHTML,
  renderGeneratedImageBlocksHTML,
  renderToolActivityCardHTML,
  renderToolActivityStackHTML,
  renderToolDiffHTML,
  toolActivityDedupeKey,
  transcriptMessageText,
};

const messagePageLimit = 100;
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
  const {
    attachHistorySentinel,
    renderOlderMessagesControlHTML,
    resetHistoryFailures,
    disconnectHistoryObserver,
  } = createChatRenderingHistory({
    state,
    loadOlderMessages,
    applyMessageSnapshot,
    showError,
  });

  const {
    applyPlanEvent,
    bindPlanButtons,
    clearPlanState,
    performPlanAction,
    renderPlanCardHTML,
    renderPlanCards,
    renderPlanCardsHTML,
    replacePlanState,
  } = createChatRenderingPlanCards({
    state,
    request,
    applyMessageSnapshot,
    scheduleMessageRefresh,
    showError,
    showToast,
  });

  const {
    closeCorrectionEditor,
    correctionClipboardFiles,
    openCorrectionEditor,
    renderCorrectionEditor,
    submitCorrection,
  } = createChatRenderingCorrection({
    state,
    request,
    applyMessageSnapshot,
    loadMessages,
    showToast,
  });

  const {
    messageAttachmentsMarkdown,
    renderMessageAttachments,
  } = createChatRenderingAttachments({
    state,
    attachmentIcon,
    attachmentKind,
  });

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
    resetHistoryFailures();
    disconnectHistoryObserver();
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
      createdBy: String(state.account?.id || ""),
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

  function captureToolActivityOpenState(root) {
    const states = new Map();
    root?.querySelectorAll?.("[data-tool-activity-stack]")?.forEach((stack) => {
      const key = String(stack.dataset?.toolActivityStackKey || "");
      const details = stack.querySelector?.("details.tool-activity-group");
      if (key && details) states.set(key, Boolean(details.open));
    });
    return states;
  }

  function restoreToolActivityOpenState(root, states) {
    if (!(states instanceof Map) || !states.size) return;
    root?.querySelectorAll?.("[data-tool-activity-stack]")?.forEach((stack) => {
      const key = String(stack.dataset?.toolActivityStackKey || "");
      if (!states.has(key)) return;
      const details = stack.querySelector?.("details.tool-activity-group");
      if (details && details.open !== states.get(key)) details.open = states.get(key);
    });
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
    state.messageCopyTexts = visibleMessages.map(messageCopyText);
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
    const olderMessagesControl = renderOlderMessagesControlHTML();
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
    // Preserve the user's manual open/collapsed state for every activity group
    // across the full innerHTML replacement. A snapshot can be triggered by a
    // new background-task event after the activity moved under its assistant
    // turn, so preserving only the live tail stack lets that group revert to its
    // template default (and re-open after the user collapsed it).
    const savedToolActivityOpenStates = captureToolActivityOpenState(el);
    el.innerHTML = `${olderMessagesControl}${messagesHTML}${liveImageGenerationCards}${planCards}${tailLiveToolCards}${tailRunSummaryCard}${liveAssistantCard}${approvalCards}`;
    restoreToolActivityOpenState(el, savedToolActivityOpenStates);
    const liveMessageIds = new Set(visibleMessages.map((message) => message.id).filter(Boolean));
    for (const cachedId of messageHtmlCache.keys()) {
      if (!liveMessageIds.has(cachedId)) messageHtmlCache.delete(cachedId);
    }
    bindToolActivityControls(el);
    bindMessageActionButtons(el);
    bindMessageClipboard(el);
    el.querySelector("[data-load-older-messages]")?.addEventListener("click", () => {
      resetHistoryFailures();
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
    const drafting = looksLikePlanDraft(text);
    if (drafting) streamingMarkdown.reset();
    return `
      <div class="message assistant live-assistant-message chat-message chat-flow-item chat-flow-left" data-chat-alignment="left" data-live-assistant${drafting ? " data-plan-drafting" : ""} data-run-id="${escapeAttr(state.liveAssistantRunId || "")}" data-request-id="${escapeAttr(state.liveAssistantRequestId || "")}" data-started-at="${escapeAttr(state.liveAssistantStartedAt || "")}">
        <div class="message-head">
          <div class="message-role sr-only">Autoto</div>
          ${renderPerformanceHTML(state.liveAssistantPerformance, { live: true })}
        </div>
        <div class="message-content">${drafting ? escapeHtml(cr("plan.drafting")) : liveAssistantContentHTML(text)}</div>
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

  function messageCopyText(message) {
    const text = transcriptMessageText(message);
    if (isApprovedPlanExecuteMessage(text)) return cr("plan.executeNotice");
    if (isPlanReflectionMessage(text)) return cr("plan.reflectNotice");
    const plan = planForMessage(message, state);
    if (plan) return planCopyMarkdown(plan);
    return clipboardPlainText(text);
  }

  function planMessageRenderSignature(message) {
    const text = transcriptMessageText(message);
    if (isApprovedPlanExecuteMessage(text)) return "plan-notice:execute";
    if (isPlanReflectionMessage(text)) return "plan-notice:reflect";
    const plan = planForMessage(message, state);
    if (!plan) return "";
    return [
      plan.id || "",
      compactPlanStatus(plan.status),
      plan.revision || 0,
      plan.reviewVerdict || "",
      plan.id && state.planActionBusy?.[plan.id] ? "1" : "0",
      plan.id ? String(state.planFeedbackDrafts?.[plan.id] || "") : "",
      plan.id && state.planCardCollapsed?.[plan.id] ? "0" : "1",
    ].join("\u0001");
  }

  // Captures every input `renderChatMessageHTML` reads so a cached render can
  // be safely reused only when none of them have changed. `JSON.stringify(message)`
  // is deliberately used wholesale (rather than picking individual fields) so no
  // message field renderChatMessageHTML might read is ever missed.
  function messageRenderSignature(message, index) {
    const editing = Boolean(message.id && state.editingMessageId === message.id);
    const usesProfileIdentity = userMessageRoles.has(chatMessagePresentation(message).normalizedRole);
    const identity = usesProfileIdentity
      ? JSON.stringify(resolveUserMessageIdentity(message, {
        profile: state.profile,
        account: state.account,
        unknownAuthor: cr("message.unknownAuthor"),
      }))
      : "";
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
      planMessageRenderSignature(message),
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

  function renderPlanNoticeMessageHTML(message, kind) {
    return `
      <div class="plan-system-notice chat-flow-item chat-flow-left" data-chat-alignment="left" data-plan-notice="${escapeAttr(kind)}" data-message-id="${escapeAttr(message.id || "")}">
        ${renderPlanSystemNoticeHTML({ kind })}
      </div>
    `;
  }

  function renderChatMessageHTML(message, index) {
    const presentation = chatMessagePresentation(message);
    const editing = Boolean(message.id && state.editingMessageId === message.id);
    const usesProfileIdentity = userMessageRoles.has(presentation.normalizedRole);
    const profileIdentity = usesProfileIdentity
      ? resolveUserMessageIdentity(message, {
        profile: state.profile,
        account: state.account,
        unknownAuthor: cr("message.unknownAuthor"),
      })
      : null;
    const isAssistant = presentation.normalizedRole === "assistant";
    const transcriptText = transcriptMessageText(message);
    if (!editing && usesProfileIdentity && isApprovedPlanExecuteMessage(transcriptText)) {
      return renderPlanNoticeMessageHTML(message, "execute");
    }
    if (!editing && usesProfileIdentity && isPlanReflectionMessage(transcriptText)) {
      return renderPlanNoticeMessageHTML(message, "reflect");
    }
    const plan = isAssistant && !editing ? planForMessage(message, state) : null;
    const liveIdentity = Boolean(profileIdentity?.live);
    const avatarHTML = usesProfileIdentity
      ? profileAvatarHTML(profileIdentity)
      : escapeHtml(presentation.role.slice(0, 1).toUpperCase() || "•");
    const roleLabel = isAssistant
      ? "Autoto"
      : (profileIdentity
        ? formatUserMessageAuthorLabel(profileIdentity, cr("message.unknownAuthor"))
        : presentation.role);
    const profileAvatarAttr = liveIdentity ? " data-user-profile-avatar" : "";
    const roleHTML = usesProfileIdentity
      ? `<span${liveIdentity ? " data-user-profile-name" : ""}>${escapeHtml(roleLabel)}</span>`
      : escapeHtml(roleLabel);
    const timeHTML = presentation.timestampValue
      ? `<time class="message-time" datetime="${escapeAttr(presentation.timestampValue)}" title="${escapeAttr(formatTimestamp(presentation.timestampValue))}">${escapeHtml(formatTimestamp(presentation.timestampValue, { timeOnly: true }))}</time>`
      : "";
    // While this message is being corrected the head keeps only the copy
    // action: offering "correct" for a message whose correction editor is
    // already open re-renders the same editor and reads as a second control.
    const actions = `${message.role === "user" && !editing ? `<button class="message-copy-btn" type="button" data-correct-message="${escapeAttr(message.id || "")}" title="${escapeAttr(cr("message.correctTitle"))}" aria-label="${escapeAttr(cr("message.correctTitle"))}">${messageCorrectGlyph()}</button>` : ""}<button class="message-copy-btn" type="button" data-copy-message="${escapeAttr(String(index))}" title="${escapeAttr(cr("message.copyTitle"))}" aria-label="${escapeAttr(cr("message.copyTitle"))}">${messageCopyGlyph()}</button>`;
    const body = editing
      ? renderCorrectionEditor(message)
      : plan
        ? renderPlanCardHTML(plan)
        : `<div class="message-content">${renderMarkdown(friendlyMessageText(transcriptText))}</div>${isAssistant ? renderGeneratedImageBlocksHTML(message, state.agent?.id || "") : ""}${renderMessageAttachments(message)}`;
    const senderHTML = isAssistant
      ? `<div class="message-role sr-only">Autoto</div>`
      : `<div class="message-meta"><span class="message-avatar" aria-hidden="true"${profileAvatarAttr}>${avatarHTML}</span><div class="message-role">${roleHTML}</div></div>`;
    const head = plan ? "" : `
        <div class="message-head">
          ${senderHTML}
          <div class="message-head-actions">${actions}</div>
          ${timeHTML}
        </div>`;
    return `
      <div class="message ${presentation.roleClass}${editing ? " message-editing" : ""}${plan ? " plan-message" : ""} chat-message chat-flow-item chat-flow-${presentation.alignment}" data-chat-alignment="${presentation.alignment}" data-message-role="${escapeAttr(presentation.normalizedRole)}" data-message-id="${escapeAttr(message.id || "")}">
        ${head}
        ${body}
        ${isAssistant && !plan ? renderPerformanceHTML(message.turnUsage) : ""}
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
    if (looksLikePlanDraft(state.liveAssistantText || "")) {
      streamingMarkdown.reset();
      return Object.prototype.hasOwnProperty.call(card.dataset || {}, "planDrafting") || card.hasAttribute?.("data-plan-drafting") === true;
    }
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
    const savedToolActivityOpenStates = captureToolActivityOpenState(el);
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
    restoreToolActivityOpenState(el, savedToolActivityOpenStates);
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

  function isUserInterruptedReason(value) {
    const reason = String(value || "").trim().toLowerCase();
    return reason === "interrupted by user" || reason === "interrupted";
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
      // A stored reason that is not a user Stop is usually the only place the
      // cause is visible. A user Stop used to omit the divider because the
      // clicker already knew; a collaborator watching the same turn then saw
      // the reply freeze and the send button stay on Stop with no explanation.
      const raw = String(run?.errorMessage || "").trim();
      const reason = !raw || isUserInterruptedReason(raw) ? cr("run.stoppedNotice") : runFailureMessage(run);
      return `<div class="conversation-run-notice interrupted" role="status"><span class="conversation-run-notice-message" title="${escapeAttr(reason)}">${escapeHtml(reason)}</span></div>`;
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

  // Streams the argument text (Write content, Edit replacement) while the
  // model is still composing the call, so the expanded card follows the write
  // in real time. The record usually does not exist yet: input deltas arrive
  // before tool.started, so this creates it in a running state and
  // tool.started later merges onto it.
  function appendToolInputDelta(event) {
    const data = event?.data || {};
    const toolUseId = firstToolValue(data, "toolUseId", "tool_use_id");
    if (!toolUseId) return;
    const delta = String(event?.text ?? data.text ?? "");
    const inputJson = data.inputJson && typeof data.inputJson === "object" ? data.inputJson : null;
    if (!delta && !inputJson) return;
    const known = Object.prototype.hasOwnProperty.call(state.liveToolOutputs || {}, toolUseId);
    const current = state.liveToolOutputs?.[toolUseId] || {};
    const normalized = normalizeToolActivity({
      ...current,
      agentId: event?.agentId || current.agentId || state.agent?.id,
      runId: current.runId || data.runId || data.run_id || "",
      toolName: current.toolName || data.toolName || data.tool_name || "",
      createdAt: current.createdAt || event?.createdAt || new Date().toISOString(),
      status: current.status || "running",
      inputJson: inputJson || current.inputJson || null,
      inputPreviewField: String(data.field || current.inputPreviewField || ""),
    });
    // Snapshot previews (Bash commands) resend the whole redacted text, so a
    // replace event supersedes the accumulated preview instead of extending it.
    const replace = data.replace === true;
    const inputPreview = !delta
      ? String(current.inputPreview || "")
      : replace
        ? trimLiveToolOutput(delta)
        : trimLiveToolOutput(`${current.inputPreview || ""}${delta}`);
    const updated = {
      ...normalized,
      toolUseId: String(toolUseId),
      messageId: String(current.messageId || state.liveAssistantToolOwnerId || ""),
      inputPreview,
    };
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
    const savedToolActivityOpenStates = captureToolActivityOpenState(el);
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
        // outerHTML replaces the node and rebuilds the stack from its template
        // default. The keyed state captured above restores a user's manual
        // choice even when the activity moved to another stack during sync.
        existing.outerHTML = html;
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
    restoreToolActivityOpenState(el, savedToolActivityOpenStates);
    restoreTranscriptView(view, el);
    bindToolActivityControls(el);
    // Streamed input blocks follow their tail like a terminal: each repaint
    // rebuilds the <pre>, which resets its scroll to the top, so pin it back
    // to the newest content while the call is still being composed.
    if (typeof el.querySelectorAll === "function") {
      el.querySelectorAll("[data-tool-input-preview]").forEach((block) => {
        if (typeof block.scrollHeight === "number") block.scrollTop = block.scrollHeight;
      });
    }
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
    // The content the model streamed while composing this call (Write body,
    // Edit replacement, subagent brief) is exactly what the human is being
    // asked to approve, so surface it on the card instead of just byte counts.
    const liveRecord = state.liveToolOutputs?.[String(approval.toolUseId || "")] || {};
    const streamedTool = {
      inputPreview: String(tool.inputPreview || liveRecord.inputPreview || ""),
      inputPreviewField: String(tool.inputPreviewField || liveRecord.inputPreviewField || ""),
    };
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
        ${streamedInputBlockHTML(streamedTool)}
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

  function bindMessageClipboard(root) {
    if (!root?.addEventListener) return;
    const dataset = root.dataset || (root.dataset = {});
    if (dataset.chatCopyBound === "1") return;
    dataset.chatCopyBound = "1";
    root.addEventListener("copy", (event) => rewriteChatCopyEvent(event));
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
    root.querySelector("[data-correction-pick-files]")?.addEventListener("click", () => {
      root.querySelector("[data-correction-files]")?.click();
    });
    root.querySelectorAll("[data-remove-correction-file]").forEach((button) => {
      button.addEventListener("click", () => {
        const index = Number(button.dataset.removeCorrectionFile);
        state.correctionText = root.querySelector("[data-correction-text]")?.value ?? state.correctionText ?? "";
        state.correctionFiles = (Array.isArray(state.correctionFiles) ? state.correctionFiles : []).filter((_, itemIndex) => itemIndex !== index);
        applyMessageSnapshot(state.currentMessages, state.agent?.id);
      });
    });
    root.querySelector("[data-correction-files]")?.addEventListener("change", (event) => {
      state.correctionText = root.querySelector("[data-correction-text]")?.value ?? state.correctionText ?? "";
      const picked = Array.from(event.target?.files || []).filter(Boolean);
      state.correctionFiles = [...(Array.isArray(state.correctionFiles) ? state.correctionFiles : []), ...picked];
      if (event.target) event.target.value = "";
      applyMessageSnapshot(state.currentMessages, state.agent?.id);
    });
    const correctionText = root.querySelector("[data-correction-text]");
    if (state.correctionEditorNeedsFocus && correctionText) {
      state.correctionEditorNeedsFocus = false;
      correctionText.focus?.();
      const cursor = String(correctionText.value || "").length;
      correctionText.setSelectionRange?.(cursor, cursor);
    }
    root.querySelector("[data-correction-text]")?.addEventListener("paste", (event) => {
      const files = correctionClipboardFiles(event);
      if (!files.length) return;
      state.correctionFiles = [...(state.correctionFiles || []), ...files];
      window.setTimeout(() => applyMessageSnapshot(state.currentMessages, state.agent?.id), 0);
    });
    root.querySelectorAll("[data-copy-message]:not([data-copy-bound])").forEach((button) => {
      button.dataset.copyBound = "1";
      button.addEventListener("click", async () => {
        const index = Number(button.dataset.copyMessage || -1);
        const text = state.messageCopyTexts[index] || "";
        const restore = () => {
          button.classList.remove("is-copied");
          button.innerHTML = messageCopyGlyph();
          button.setAttribute("title", cr("message.copyTitle"));
          button.setAttribute("aria-label", cr("message.copyTitle"));
        };
        if (text && await copyToClipboard(text)) {
          button.classList.add("is-copied");
          button.innerHTML = messageCopiedGlyph();
          button.setAttribute("title", cr("message.copied"));
          button.setAttribute("aria-label", cr("message.copied"));
        } else {
          showToast(cr("message.copyFailedToast"), "warn");
        }
        window.setTimeout(restore, 1200);
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
    appendToolInputDelta,
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
    // The message context menu's "edit" entry drives the same in-place
    // correction editor as the inline button.
    openCorrectionEditor,
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
