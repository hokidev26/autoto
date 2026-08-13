import { escapeHtml } from "./dom.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";

export function createChatRenderingHistory({
  state,
  loadOlderMessages,
  applyMessageSnapshot,
  showError,
} = {}) {
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

  function renderOlderMessagesControlHTML() {
    return state.messageHasMoreBefore ? `
      <div class="message-history-control" data-history-sentinel>
        <span class="message-history-status" role="status"${state.messageOlderLoading ? "" : " hidden"}>${escapeHtml(cr("history.loadingOlder"))}</span>
        <button class="ghost-btn mini" type="button" data-load-older-messages${historyAutoLoadAvailable() ? " hidden" : ""}>
          ${escapeHtml(cr("history.loadOlder"))}
        </button>
      </div>
    ` : "";
  }

  function resetHistoryFailures() {
    historyLoadFailures = 0;
  }

  function disconnectHistoryObserver() {
    historyObserver?.disconnect();
  }

  return {
    attachHistorySentinel,
    historyAutoLoadAvailable,
    renderOlderMessagesControlHTML,
    resetHistoryFailures,
    disconnectHistoryObserver,
    requestOlderMessages,
  };
}
