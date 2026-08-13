import { $ } from "./dom.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";

const menuActions = ["copy", "edit", "rollback", "fork", "compress", "delete"];

// Right-click / long-press context menu on chat messages. Copy and edit reuse
// the handlers behind the inline message buttons; rollback, fork, compress,
// and delete call their message-scoped APIs.
export function createMessageContextMenu({
  state,
  request,
  showToast,
  showError,
  confirmAction,
  copyToClipboard,
  openCorrectionEditor,
  loadMessages,
  onForkCreated = null,
  refreshContextStatus = null,
  onBeforeOpen = null,
}) {
  function closeMessageContextMenu({ restoreFocus = false } = {}) {
    const menu = $("messageContextMenu");
    const target = state.messageMenuTarget;
    state.messageMenuTarget = null;
    menu?.classList.add("hidden");
    menu?.setAttribute("aria-hidden", "true");
    if (restoreFocus && target?.id) {
      const escaped = globalThis.CSS?.escape?.(target.id) ?? target.id;
      $("messages")?.querySelector?.(`[data-message-id="${escaped}"]`)?.focus?.();
    }
  }

  function messageMenuTargetFromRow(row) {
    const id = String(row?.dataset?.messageId || "").trim();
    if (!id) return null;
    return {
      id,
      role: String(row.dataset.messageRole || "").trim(),
      superseded: row.classList.contains("message-superseded"),
      copyIndex: Number(row.querySelector?.("[data-copy-message]")?.dataset?.copyMessage ?? -1),
    };
  }

  function messageMenuActionsFor(target) {
    return {
      copy: true,
      edit: target.role === "user" && !target.superseded,
      rollback: !target.superseded,
      fork: !target.superseded,
      compress: !target.superseded,
      delete: true,
    };
  }

  function positionMessageContextMenu(menu, x, y) {
    const margin = 8;
    const width = menu.offsetWidth || 180;
    const height = menu.offsetHeight || 200;
    const maxX = Math.max(margin, (window.innerWidth || document.documentElement.clientWidth) - width - margin);
    const maxY = Math.max(margin, (window.innerHeight || document.documentElement.clientHeight) - height - margin);
    menu.style.left = `${Math.min(Math.max(margin, Number(x) || margin), maxX)}px`;
    menu.style.top = `${Math.min(Math.max(margin, Number(y) || margin), maxY)}px`;
  }

  function openMessageContextMenu(row, event) {
    const target = messageMenuTargetFromRow(row);
    const menu = $("messageContextMenu");
    if (!target || !menu || !state.agent?.id) return false;
    onBeforeOpen?.();
    state.messageMenuTarget = target;
    const enabled = messageMenuActionsFor(target);
    let firstVisible = null;
    menuActions.forEach((action) => {
      const item = menu.querySelector(`[data-message-menu-action="${action}"]`);
      if (!item) return;
      item.textContent = cr(`menu.${action}`);
      item.hidden = !enabled[action];
      if (!item.hidden && !firstVisible) firstVisible = item;
    });
    menu.classList.remove("hidden");
    menu.setAttribute("aria-hidden", "false");
    const rect = row.getBoundingClientRect?.();
    positionMessageContextMenu(menu, event?.clientX || rect?.left || 0, event?.clientY || rect?.bottom || 0);
    firstVisible?.focus?.();
    return true;
  }

  function handleMessageContextMenu(event) {
    // Inside an editor the native menu (paste, spellcheck) stays available.
    if (event.target.closest?.("textarea, input, select, [contenteditable='true']")) return;
    const row = event.target.closest?.(".chat-message[data-message-id]");
    if (!row || !$("messages")?.contains(row)) return;
    event.preventDefault();
    event.stopPropagation();
    openMessageContextMenu(row, event);
  }

  // iOS Safari never fires contextmenu for touch, so a timer-based long-press
  // fills the gap. Android fires both; the contextmenu listener cancels the
  // timer so the menu opens exactly once.
  function bindMessageLongPress() {
    const container = $("messages");
    if (!container || container.dataset.messageMenuLongPress === "bound") return;
    container.dataset.messageMenuLongPress = "bound";
    let timer = 0;
    let startX = 0;
    let startY = 0;
    const cancel = () => {
      if (timer) window.clearTimeout(timer);
      timer = 0;
    };
    container.addEventListener("pointerdown", (event) => {
      if (event.pointerType !== "touch") return;
      if (event.target.closest?.("textarea, input, select, button, a, [contenteditable='true']")) return;
      const row = event.target.closest?.(".chat-message[data-message-id]");
      if (!row || !container.contains(row)) return;
      cancel();
      startX = event.clientX;
      startY = event.clientY;
      timer = window.setTimeout(() => {
        timer = 0;
        if (openMessageContextMenu(row, event)) suppressNextClick();
      }, 550);
    });
    container.addEventListener("pointermove", (event) => {
      if (!timer) return;
      if (Math.abs(event.clientX - startX) > 10 || Math.abs(event.clientY - startY) > 10) cancel();
    });
    container.addEventListener("pointerup", cancel);
    container.addEventListener("pointercancel", cancel);
    container.addEventListener("contextmenu", cancel);
  }

  // The click synthesized when the finger lifts after a long-press lands
  // outside the menu and would close it in the same instant it opened.
  function suppressNextClick() {
    const handler = (event) => {
      event.stopPropagation();
      event.preventDefault();
    };
    window.addEventListener("click", handler, { capture: true, once: true });
    window.setTimeout(() => window.removeEventListener("click", handler, { capture: true }), 400);
  }

  function messageTextForTarget(target) {
    const indexed = state.messageCopyTexts?.[target.copyIndex];
    if (indexed) return indexed;
    const message = (state.currentMessages || []).find((item) => item.id === target.id);
    return String(message?.contentText || "");
  }

  async function applyMessageMenuAction(action) {
    const target = state.messageMenuTarget;
    const agentId = state.agent?.id || "";
    closeMessageContextMenu();
    if (!target || !agentId || !menuActions.includes(action)) return;
    const messagePath = `/api/agents/${encodeURIComponent(agentId)}/messages/${encodeURIComponent(target.id)}`;
    try {
      switch (action) {
        case "copy": {
          const text = messageTextForTarget(target);
          if (text && await copyToClipboard(text)) showToast(cr("message.copiedToast"), "success");
          else showToast(cr("message.copyFailedToast"), "warn", { force: true });
          return;
        }
        case "edit": {
          openCorrectionEditor?.(target.id);
          return;
        }
        case "rollback": {
          if (!await confirmAction(cr("menu.rollbackConfirm"))) return;
          await request(`${messagePath}/rollback`, { method: "POST", body: JSON.stringify({}) });
          await loadMessages(agentId);
          showToast(cr("menu.rollbackSuccess"), "success", { force: true });
          return;
        }
        case "fork": {
          if (!await confirmAction(cr("menu.forkConfirm"))) return;
          showToast(cr("menu.forkCreating"), "info");
          const created = await request(`${messagePath}/fork`, { method: "POST", body: JSON.stringify({}) });
          showToast(cr("menu.forkSuccess"), "success", { force: true });
          if (created?.agent) await onForkCreated?.(created.agent);
          return;
        }
        case "compress": {
          if (!await confirmAction(cr("menu.compressConfirm"))) return;
          const generation = Number(state.agent?.entityGeneration);
          const response = await request(`/api/agents/${encodeURIComponent(agentId)}/context/compact`, {
            method: "POST",
            body: JSON.stringify({
              ...(Number.isInteger(generation) ? { entityGeneration: generation } : {}),
              throughMessageId: target.id,
            }),
          });
          const noop = response?.compacted === false;
          showToast(cr(noop ? "menu.compressNoop" : "menu.compressSuccess"), noop ? "warn" : "success", { force: true });
          await refreshContextStatus?.();
          return;
        }
        case "delete": {
          if (!await confirmAction(cr("menu.deleteConfirm"))) return;
          await request(messagePath, { method: "DELETE" });
          await loadMessages(agentId);
          showToast(cr("menu.deleteSuccess"), "success", { force: true });
          return;
        }
      }
    } catch (error) {
      showError(error);
    }
  }

  return {
    closeMessageContextMenu,
    openMessageContextMenu,
    handleMessageContextMenu,
    bindMessageLongPress,
    applyMessageMenuAction,
  };
}
