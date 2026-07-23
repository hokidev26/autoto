import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1";

function text(value) {
  return String(value ?? "").trim();
}

function timestamp(value) {
  const normalized = text(value);
  return normalized && !Number.isNaN(Date.parse(normalized)) ? normalized : "";
}

function booleanValue(value) {
  return value === true || value === 1 || value === "1" || value === "true";
}

export function normalizeArchivePayload(value = {}) {
  const projects = (Array.isArray(value.projects) ? value.projects : []).map((project) => ({
    id: text(project?.id || project?.projectId),
    name: text(project?.name || project?.projectName),
    gitPath: text(project?.gitPath || project?.projectPath),
    archivedAt: timestamp(project?.archivedAt || project?.projectArchivedAt),
    pinned: booleanValue(project?.pinned || project?.projectPinned),
  })).filter((project) => project.id);
  const conversations = (Array.isArray(value.conversations) ? value.conversations : []).map((conversation) => ({
    projectId: text(conversation?.projectId),
    projectName: text(conversation?.projectName),
    projectPath: text(conversation?.projectPath),
    worklineId: text(conversation?.worklineId),
    worklineTitle: text(conversation?.worklineTitle),
    agentId: text(conversation?.agentId),
    agentTitle: text(conversation?.agentTitle),
    agentArchivedAt: timestamp(conversation?.agentArchivedAt),
    projectArchivedAt: timestamp(conversation?.projectArchivedAt),
    agentPinned: booleanValue(conversation?.agentPinned),
  })).filter((conversation) => conversation.projectId && conversation.agentId);
  return { projects, conversations };
}

function displayPath(path) {
  const value = text(path);
  return value.replace(/^\/Users\/[^/]+(?=\/)/, "~").replace(/^\/home\/[^/]+(?=\/)/, "~") || "—";
}

function archiveItem(kind, item, { restoreLabel, projectLabel, conversationLabel } = {}) {
  const isProject = kind === "project";
  const title = isProject ? item.name || item.id : item.agentTitle || item.agentId;
  const context = isProject
    ? displayPath(item.gitPath)
    : [item.projectName, item.worklineTitle].filter(Boolean).join(" / ") || displayPath(item.projectPath);
  const stateLabel = isProject ? projectLabel : conversationLabel;
  return `
    <article class="archive-item">
      <div class="archive-item-icon" aria-hidden="true">${isProject ? "P" : "A"}</div>
      <div class="archive-item-main">
        <strong>${escapeHtml(title)}</strong>
        <small>${escapeHtml(context)}</small>
        <span class="archive-item-state">${escapeHtml(stateLabel)}</span>
      </div>
      <button class="settings-action-btn subtle archive-restore-btn" type="button" data-archive-restore="${escapeAttr(kind)}" data-archive-id="${escapeAttr(isProject ? item.id : item.agentId)}">${escapeHtml(restoreLabel)}</button>
    </article>`;
}

export function createArchiveSettingsController({
  request,
  refresh,
  showError,
  showToast,
} = {}) {
  let payload = { projects: [], conversations: [] };
  let loading = false;
  let loaded = false;
  let error = "";
  let sequence = 0;

  const archiveText = (key, params) => t(`archive.${key}`, params);

  async function load() {
    if (loading) return;
    const currentSequence = ++sequence;
    loading = true;
    error = "";
    refresh?.();
    try {
      const result = await request("/api/navigation?includeArchived=true");
      if (currentSequence !== sequence) return;
      payload = normalizeArchivePayload(result);
      loaded = true;
    } catch (cause) {
      if (currentSequence !== sequence) return;
      error = cause?.message || String(cause);
      loaded = false;
      showError?.(cause);
    } finally {
      if (currentSequence === sequence) {
        loading = false;
        refresh?.();
      }
    }
  }

  async function restore(kind, id, button) {
    const path = kind === "project"
      ? `/api/projects/${encodeURIComponent(id)}/navigation-state`
      : `/api/agents/${encodeURIComponent(id)}/navigation-state`;
    setButtonBusy(button, true, archiveText("restoring"));
    try {
      await request(path, { method: "PATCH", body: JSON.stringify({ archived: false }) });
      showToast?.(archiveText("restored"), "success", { force: true });
      await load();
    } catch (cause) {
      showError?.(cause);
    } finally {
      if (button) setButtonBusy(button, false);
    }
  }

  function render() {
    // Only auto-load while idle with no prior failure. A failed load must not
    // re-enter load() via refresh→render, or 401 toasts loop forever.
    if (!loaded && !loading && !error) load().catch(showError);
    if (loading && !loaded) return `<div class="settings-empty-card settings-empty-state">${escapeHtml(archiveText("loading"))}</div>`;
    if (error) {
      return `<div class="settings-live-page archive-page"><div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(error)}</div><button id="archiveRefreshBtn" class="settings-action-btn subtle" type="button">${escapeHtml(archiveText("refresh"))}</button></div>`;
    }

    const archivedProjects = payload.projects.filter((project) => project.archivedAt);
    const archivedConversations = payload.conversations.filter((conversation) => conversation.agentArchivedAt);
    const total = archivedProjects.length + archivedConversations.length;
    return `
      <div class="settings-live-page archive-page">
        <section class="settings-hero-card settings-page-section settings-card">
          <div class="settings-card-header">
            <div>
              <div class="settings-hero-kicker">${escapeHtml(archiveText("kicker"))}</div>
              <div class="settings-hero-title settings-card-title">${escapeHtml(archiveText("title"))}</div>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(archiveText("description"))}</p>
            </div>
            <button id="archiveRefreshBtn" class="settings-action-btn subtle" type="button">${escapeHtml(archiveText("refresh"))}</button>
          </div>
        </section>
        <div class="settings-status-strip settings-stat-grid archive-summary-grid">
          <div class="settings-stat-card"><strong>${escapeHtml(String(total))}</strong><span>${escapeHtml(archiveText("total"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(String(archivedProjects.length))}</strong><span>${escapeHtml(archiveText("projects"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(String(archivedConversations.length))}</strong><span>${escapeHtml(archiveText("conversations"))}</span></div>
        </div>
        ${total ? `
          ${archivedProjects.length ? `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(archiveText("projectsTitle"))}</div></div></div><div class="archive-item-list">${archivedProjects.map((project) => archiveItem("project", project, { restoreLabel: archiveText("restore"), projectLabel: archiveText("projectArchived"), conversationLabel: "" })).join("")}</div></section>` : ""}
          ${archivedConversations.length ? `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(archiveText("conversationsTitle"))}</div></div></div><div class="archive-item-list">${archivedConversations.map((conversation) => archiveItem("conversation", conversation, { restoreLabel: archiveText("restore"), projectLabel: "", conversationLabel: archiveText("conversationArchived") })).join("")}</div></section>` : ""}
        ` : `<section class="settings-provider-section settings-page-section settings-card"><div class="archive-empty-state">${escapeHtml(archiveText("empty"))}</div></section>`}
      </div>`;
  }

  function bind() {
    $("archiveRefreshBtn")?.addEventListener("click", () => load().catch(showError));
    document.querySelectorAll("[data-archive-restore]").forEach((button) => {
      button.addEventListener("click", () => restore(button.dataset.archiveRestore, button.dataset.archiveId, button));
    });
  }

  return { bind, load, normalize: () => payload, render, restore };
}
