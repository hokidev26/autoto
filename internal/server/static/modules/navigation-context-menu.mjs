import { $ } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { api } from "./runtime.mjs";

// Right-click context menu for navigation rows (projects and conversations):
// open/close/position it, and apply the pin/archive action it exposes.
// Rows do not render a "…" button; the same menu still opens from a leftover
// trigger if one is present.
export function createNavigationContextMenu({
  state,
  showToast,
  setTranslatedText,
  loadProjects,
}) {
  function closeNavigationContextMenu({ restoreFocus = false } = {}) {
    const menu = $("navigationContextMenu");
    const target = state.navigationMenuTarget;
    const trigger = target?.trigger?.isConnected
      ? target.trigger
      : [...document.querySelectorAll("[data-navigation-kind][data-navigation-id]:not([data-navigation-menu-trigger])")]
        .find((node) => node.dataset.navigationKind === target?.kind && node.dataset.navigationId === target?.id);
    state.navigationMenuTarget = null;
    menu?.classList.add("hidden");
    menu?.setAttribute("aria-hidden", "true");
    if (restoreFocus) trigger?.focus?.();
  }

  function navigationMenuRecord(kind, id) {
    if (kind === "project") {
      const project = state.projects.find((item) => item.id === id);
      return project ? { kind, id, pinned: Boolean(project.pinned), archived: Boolean(project.archivedAt) } : null;
    }
    const conversation = state.navigationConversations.find((item) => item.agentId === id);
    return conversation
      ? { kind, id, pinned: Boolean(conversation.agentPinned), archived: Boolean(conversation.agentArchivedAt) }
      : null;
  }

  function positionNavigationContextMenu(menu, x, y) {
    const margin = 8;
    const width = menu.offsetWidth || 180;
    const height = menu.offsetHeight || 88;
    const maxX = Math.max(margin, (window.innerWidth || document.documentElement.clientWidth) - width - margin);
    const maxY = Math.max(margin, (window.innerHeight || document.documentElement.clientHeight) - height - margin);
    menu.style.left = `${Math.min(Math.max(margin, Number(x) || margin), maxX)}px`;
    menu.style.top = `${Math.min(Math.max(margin, Number(y) || margin), maxY)}px`;
  }

  function openNavigationContextMenu(kind, id, event, trigger = null) {
    const record = navigationMenuRecord(kind, id);
    const menu = $("navigationContextMenu");
    if (!record || !menu) return false;
    const pinItem = menu.querySelector('[data-navigation-menu-action="pin"]');
    const archiveItem = menu.querySelector('[data-navigation-menu-action="archive"]');
    state.navigationMenuTarget = { ...record, trigger };
    setTranslatedText(pinItem, record.pinned ? "shell.unpin" : "shell.pin");
    setTranslatedText(archiveItem, record.archived ? "shell.restore" : "shell.archive");
    menu.dataset.navigationMenuKind = kind;
    menu.dataset.navigationMenuId = id;
    menu.classList.remove("hidden");
    menu.setAttribute("aria-hidden", "false");
    const rect = trigger?.getBoundingClientRect?.();
    const x = event?.clientX || (rect ? rect.right : 0);
    const y = event?.clientY || (rect ? rect.bottom : 0);
    positionNavigationContextMenu(menu, x, y);
    (pinItem || archiveItem)?.focus?.();
    return true;
  }

  function handleNavigationContextMenu(event) {
    const row = event.target.closest?.("[data-navigation-kind][data-navigation-id]");
    if (!row || !$("projects")?.contains(row)) return;
    const kind = String(row.dataset.navigationKind || "").trim();
    const id = String(row.dataset.navigationId || "").trim();
    if (!kind || !id) return;
    event.preventDefault();
    event.stopPropagation();
    openNavigationContextMenu(kind, id, event, row);
  }

  function bindNavigationMenuTriggers() {
    $("projects")?.querySelectorAll("[data-navigation-menu-trigger]").forEach((trigger) => {
      const open = (event) => {
        event.preventDefault();
        event.stopPropagation();
        openNavigationContextMenu(trigger.dataset.navigationKind, trigger.dataset.navigationId, event, trigger);
      };
      trigger.addEventListener("click", open);
      trigger.addEventListener("keydown", (event) => {
        if (event.key !== "Enter" && event.key !== " ") return;
        open(event);
      });
    });
  }

  async function applyNavigationMenuAction(action) {
    const target = state.navigationMenuTarget;
    if (!target || !["pin", "archive"].includes(action)) return;
    const patch = action === "pin"
      ? { pinned: !target.pinned }
      : { archived: !target.archived };
    closeNavigationContextMenu({ restoreFocus: true });
    const path = target.kind === "project"
      ? `/api/projects/${encodeURIComponent(target.id)}/navigation-state`
      : `/api/agents/${encodeURIComponent(target.id)}/navigation-state`;
    try {
      await api(path, { method: "PATCH", body: JSON.stringify(patch) });
      await loadProjects();
      const messageKey = action === "pin"
        ? (patch.pinned ? "shell.pinSuccess" : "shell.unpinSuccess")
        : (patch.archived ? "shell.archiveSuccess" : "shell.restoreSuccess");
      showToast(t(messageKey), "success", { force: true });
    } catch (error) {
      showToast(error?.message || t("shell.navigationStateFailed"), "error", { force: true });
    }
  }

  function bindNavigationActivation(node, activate) {
    node.addEventListener("click", activate);
    node.addEventListener("keydown", (event) => {
      if (event.target !== node || (event.key !== "Enter" && event.key !== " ")) return;
      event.preventDefault();
      activate();
    });
  }

  return {
    closeNavigationContextMenu,
    navigationMenuRecord,
    positionNavigationContextMenu,
    openNavigationContextMenu,
    handleNavigationContextMenu,
    bindNavigationMenuTriggers,
    applyNavigationMenuAction,
    bindNavigationActivation,
  };
}
