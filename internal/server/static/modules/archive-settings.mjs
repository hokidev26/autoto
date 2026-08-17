import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

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

export function groupArchiveItems(payload = {}) {
  const projects = (Array.isArray(payload.projects) ? payload.projects : []).filter((project) => project.archivedAt);
  const projectIds = new Set(projects.map((project) => project.id));
  const nested = new Map(projects.map((project) => [project.id, []]));
  const conversations = [];
  for (const conversation of Array.isArray(payload.conversations) ? payload.conversations : []) {
    if (projectIds.has(conversation.projectId)) nested.get(conversation.projectId)?.push(conversation);
    else if (conversation.agentArchivedAt) conversations.push(conversation);
  }
  return { projects, nested, conversations };
}

export function normalizeArchiveSearchPayload(value = {}) {
  const results = (Array.isArray(value.results) ? value.results : []).slice(0, 20).map((item) => ({
    agentId: text(item?.agentId),
    agentTitle: text(item?.agentTitle),
    projectId: text(item?.projectId),
    projectName: text(item?.projectName),
    projectArchived: booleanValue(item?.projectArchived),
    agentArchived: booleanValue(item?.agentArchived),
    titleMatch: booleanValue(item?.titleMatch),
    matches: (Array.isArray(item?.matches) ? item.matches : []).slice(0, 3).map((match) => ({
      messageId: text(match?.messageId),
      role: text(match?.role),
      createdAt: timestamp(match?.createdAt),
      snippet: text(match?.snippet).slice(0, 240),
    })).filter((match) => match.snippet),
  })).filter((item) => item.agentId);
  return { query: text(value.query).slice(0, 80), results };
}

function displayPath(path) {
  const value = text(path);
  return value.replace(/^\/Users\/[^/]+(?=\/)/, "~").replace(/^\/home\/[^/]+(?=\/)/, "~") || "—";
}

function archiveItem(kind, item, { restoreLabel, deleteLabel, projectLabel, conversationLabel } = {}) {
  const isProject = kind === "project";
  const title = isProject ? item.name || item.id : item.agentTitle || item.agentId;
  const context = isProject
    ? displayPath(item.gitPath)
    : [item.projectName, item.worklineTitle].filter(Boolean).join(" / ") || displayPath(item.projectPath);
  const stateLabel = isProject ? projectLabel : conversationLabel;
  const id = escapeAttr(isProject ? item.id : item.agentId);
  return `
    <article class="archive-item">
      <div class="archive-item-icon" aria-hidden="true">${isProject ? "P" : "A"}</div>
      <div class="archive-item-main">
        <strong>${escapeHtml(title)}</strong>
        <small>${escapeHtml(context)}</small>
        <span class="archive-item-state">${escapeHtml(stateLabel)}</span>
      </div>
      <div class="archive-item-actions">
        <button class="settings-action-btn subtle archive-restore-btn" type="button" data-archive-restore="${escapeAttr(kind)}" data-archive-id="${id}">${escapeHtml(restoreLabel)}</button>
        <button class="settings-action-btn danger destructive archive-delete-btn" type="button" data-archive-delete="${escapeAttr(kind)}" data-archive-id="${id}">${escapeHtml(deleteLabel)}</button>
      </div>
    </article>`;
}

function nestedConversation(item, archivedLabel) {
  const extra = item.agentArchivedAt ? archivedLabel : (item.worklineTitle || "");
  return `<article class="archive-item archive-item-nested"><div class="archive-item-icon" aria-hidden="true">A</div><div class="archive-item-main"><strong>${escapeHtml(item.agentTitle || item.agentId)}</strong>${extra ? `<small>${escapeHtml(extra)}</small>` : ""}</div></article>`;
}

function searchHit(item, { openLabel, userLabel, assistantLabel }) {
  const snippets = (item.matches || []).map((match) => {
    const role = match.role === "assistant" ? assistantLabel : userLabel;
    return `<p class="archive-hit-snippet"><span>${escapeHtml(role)}</span>${escapeHtml(match.snippet)}</p>`;
  }).join("");
  return `<article class="archive-item archive-hit">
    <div class="archive-item-icon" aria-hidden="true">A</div>
    <div class="archive-item-main">
      <strong>${escapeHtml(item.agentTitle || item.agentId)}</strong>
      <small>${escapeHtml(item.projectName || item.projectId || "—")}</small>
      ${snippets}
    </div>
    <div class="archive-item-actions">
      <button class="settings-action-btn subtle" type="button" data-archive-open="${escapeAttr(item.agentId)}" data-archive-project="${escapeAttr(item.projectId)}" data-archive-agent-archived="${item.agentArchived ? "true" : "false"}" data-archive-project-archived="${item.projectArchived ? "true" : "false"}">${escapeHtml(openLabel)}</button>
    </div>
  </article>`;
}

export function createArchiveSettingsController({
  request,
  refresh,
  showError,
  showToast,
  confirmDelete,
  onDeleted,
  onOpen,
} = {}) {
  let payload = { projects: [], conversations: [] };
  let loading = false;
  let loaded = false;
  let error = "";
  let sequence = 0;
  let query = "";
  let searchSequence = 0;
  let searchTimer = 0;
  let searching = false;
  let searchError = "";
  let searchHits = { query: "", results: [] };
  let restoreSearchFocus = false;
  let searchCaret = 0;
  let composing = false;
  let nestedOpen = Object.create(null);

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

  async function search(nextQuery = query) {
    const needle = text(nextQuery).slice(0, 80);
    query = needle;
    if (!needle) {
      searchSequence += 1;
      searching = false;
      searchError = "";
      searchHits = { query: "", results: [] };
      paintResults();
      return;
    }
    const currentSequence = ++searchSequence;
    searching = true;
    searchError = "";
    if (!searchHits.results.length) paintResults();
    try {
      const result = await request(`/api/archive/search?q=${encodeURIComponent(needle)}`);
      if (currentSequence !== searchSequence) return;
      searchHits = normalizeArchiveSearchPayload(result);
      searching = false;
    } catch (cause) {
      if (currentSequence !== searchSequence) return;
      searching = false;
      searchError = cause?.message || String(cause);
      showError?.(cause);
    } finally {
      if (currentSequence === searchSequence) paintResults();
    }
  }

  // Keep the search field in the DOM. Replacing the whole settings page on
  // every keystroke drops the caret and kills IME composition, which is why
  // typing in this box felt broken.
  function resultsHost() {
    try {
      return globalThis.document?.getElementById?.("archiveResultsHost") || null;
    } catch {
      return null;
    }
  }

  function paintResults() {
    const host = resultsHost();
    if (!host) {
      restoreSearchFocus = true;
      refresh?.();
      return;
    }
    host.innerHTML = renderResults();
    bindResults(host);
  }

  function scheduleSearch(nextQuery) {
    const raw = String(nextQuery ?? "").slice(0, 80);
    query = raw;
    if (searchTimer) clearTimeout(searchTimer);
    if (!text(raw)) {
      void search("");
      return;
    }
    searchTimer = setTimeout(() => {
      searchTimer = 0;
      void search(raw);
    }, 280);
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
      if (query) await search(query);
    } catch (cause) {
      showError?.(cause);
    } finally {
      if (button) setButtonBusy(button, false);
    }
  }

  // Permanent removal. The server refuses anything that is not archived or is
  // still running, and it never touches git worktrees on disk.
  async function remove(kind, id, button) {
    const confirmFn = confirmDelete || platformConfirm;
    const prompt = archiveText(kind === "project" ? "deleteProjectConfirm" : "deleteConversationConfirm");
    if (!await confirmFn(prompt)) return false;
    const path = kind === "project"
      ? `/api/projects/${encodeURIComponent(id)}`
      : `/api/agents/${encodeURIComponent(id)}`;
    setButtonBusy(button, true, archiveText("deleting"));
    try {
      await request(path, { method: "DELETE" });
      showToast?.(archiveText("deleted"), "success", { force: true });
      await load();
      if (query) await search(query);
      await onDeleted?.(kind, id);
      return true;
    } catch (cause) {
      showError?.(cause);
      return false;
    } finally {
      if (button) setButtonBusy(button, false);
    }
  }

  async function openConversation(agentId, meta, button) {
    setButtonBusy(button, true, archiveText("opening"));
    try {
      await onOpen?.(agentId, meta);
    } catch (cause) {
      showError?.(cause);
    } finally {
      if (button) setButtonBusy(button, false);
    }
  }

  function renderBrowse(groups) {
    const projectCards = groups.projects.map((project) => {
      const nested = groups.nested.get(project.id) || [];
      const nestedMarkup = nested.length
        ? `<details class="archive-nested" data-archive-nested="${escapeAttr(project.id)}"${nestedOpen[project.id] ? " open" : ""}><summary class="archive-nested-summary">${escapeHtml(archiveText("conversationsInside", { count: nested.length }))}</summary><div class="archive-nested-list">${nested.map((item) => nestedConversation(item, archiveText("conversationArchived"))).join("")}</div></details>`
        : "";
      return `${archiveItem("project", project, { restoreLabel: archiveText("restore"), deleteLabel: archiveText("delete"), projectLabel: archiveText("projectArchived"), conversationLabel: "" })}${nestedMarkup}`;
    }).join("");
    const conversationCards = groups.conversations.map((conversation) => archiveItem("conversation", conversation, {
      restoreLabel: archiveText("restore"),
      deleteLabel: archiveText("delete"),
      projectLabel: "",
      conversationLabel: archiveText("conversationArchived"),
    })).join("");
    return `
      ${groups.projects.length ? `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(archiveText("projectsTitle"))}</div><p class="settings-card-description" data-settings-help-copy>${escapeHtml(archiveText("projectsHint"))}</p></div></div><div class="archive-item-list">${projectCards}</div></section>` : ""}
      ${groups.conversations.length ? `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(archiveText("conversationsTitle"))}</div><p class="settings-card-description" data-settings-help-copy>${escapeHtml(archiveText("conversationsHint"))}</p></div></div><div class="archive-item-list">${conversationCards}</div></section>` : ""}`;
  }

  function renderSearch() {
    if (searching && !searchHits.results.length) {
      return `<section class="settings-provider-section settings-page-section settings-card"><div class="archive-empty-state">${escapeHtml(archiveText("searching"))}</div></section>`;
    }
    if (searchError) {
      return `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(searchError)}</div></section>`;
    }
    if (!searchHits.results.length) {
      return `<section class="settings-provider-section settings-page-section settings-card"><div class="archive-empty-state">${escapeHtml(archiveText("noSearchResults"))}</div></section>`;
    }
    const hits = searchHits.results.map((item) => searchHit(item, {
      openLabel: archiveText("open"),
      userLabel: archiveText("snippetUser"),
      assistantLabel: archiveText("snippetAssistant"),
    })).join("");
    return `<section class="settings-provider-section settings-page-section settings-card"><div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(archiveText("mentionsTitle"))}</div></div></div><div class="archive-item-list">${hits}</div></section>`;
  }

  function renderResults() {
    const groups = groupArchiveItems(payload);
    const total = groups.projects.length + groups.conversations.length;
    if (query) return renderSearch();
    if (total) return renderBrowse(groups);
    return `<section class="settings-provider-section settings-page-section settings-card"><div class="archive-empty-state">${escapeHtml(archiveText("empty"))}</div></section>`;
  }

  function render() {
    if (!loaded && !loading && !error) load().catch(showError);
    if (loading && !loaded) return `<div class="settings-empty-card settings-empty-state">${escapeHtml(archiveText("loading"))}</div>`;
    if (error) {
      return `<div class="settings-live-page archive-page"><div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(error)}</div><button id="archiveRefreshBtn" class="settings-action-btn subtle" type="button">${escapeHtml(archiveText("refresh"))}</button></div>`;
    }

    const groups = groupArchiveItems(payload);
    const total = groups.projects.length + groups.conversations.length;
    return `
      <div class="settings-live-page archive-page">
        <section class="settings-hero-card">
          <div class="settings-card-header">
            <div>
              <div class="settings-hero-kicker">${escapeHtml(archiveText("kicker"))}</div>
              <div class="settings-hero-title settings-card-title">${escapeHtml(archiveText("title"))}</div>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(archiveText("description"))}</p>
            </div>
            <button id="archiveRefreshBtn" class="settings-action-btn subtle" type="button">${escapeHtml(archiveText("refresh"))}</button>
          </div>
          <label class="archive-search-field"><span class="sr-only">${escapeHtml(archiveText("searchLabel"))}</span><input id="archiveSearchInput" type="search" maxlength="80" value="${escapeAttr(query)}" placeholder="${escapeAttr(archiveText("searchPlaceholder"))}" autocomplete="off" spellcheck="false" /></label>
        </section>
        <div class="settings-status-strip settings-stat-grid archive-summary-grid">
          <div class="settings-stat-card"><strong>${escapeHtml(String(total))}</strong><span>${escapeHtml(archiveText("total"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(String(groups.projects.length))}</strong><span>${escapeHtml(archiveText("projects"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(String(groups.conversations.length))}</strong><span>${escapeHtml(archiveText("conversations"))}</span></div>
        </div>
        <div id="archiveResultsHost">${renderResults()}</div>
      </div>`;
  }

  function bindResults(root) {
    const scope = root && typeof root.querySelectorAll === "function" ? root : document;
    scope.querySelectorAll("[data-archive-restore]").forEach((button) => {
      button.addEventListener("click", () => restore(button.dataset.archiveRestore, button.dataset.archiveId, button));
    });
    scope.querySelectorAll("[data-archive-delete]").forEach((button) => {
      button.addEventListener("click", () => remove(button.dataset.archiveDelete, button.dataset.archiveId, button).catch(showError));
    });
    scope.querySelectorAll("[data-archive-open]").forEach((button) => {
      button.addEventListener("click", () => openConversation(button.dataset.archiveOpen, {
        projectId: button.dataset.archiveProject || "",
        agentArchived: button.dataset.archiveAgentArchived === "true",
        projectArchived: button.dataset.archiveProjectArchived === "true",
      }, button).catch(showError));
    });
    scope.querySelectorAll("[data-archive-nested]").forEach((panel) => {
      panel.addEventListener("toggle", () => {
        const id = panel.dataset.archiveNested;
        if (id) nestedOpen[id] = Boolean(panel.open);
      });
    });
  }

  function bind() {
    $("archiveRefreshBtn")?.addEventListener("click", () => {
      load().then(() => query ? search(query) : undefined).catch(showError);
    });
    const input = $("archiveSearchInput");
    input?.addEventListener("compositionstart", () => { composing = true; });
    input?.addEventListener("compositionend", (event) => {
      composing = false;
      searchCaret = Number(event.currentTarget.selectionStart ?? event.currentTarget.value.length);
      scheduleSearch(event.currentTarget.value);
    });
    input?.addEventListener("input", (event) => {
      if (composing || event.isComposing) return;
      searchCaret = Number(event.currentTarget.selectionStart ?? event.currentTarget.value.length);
      scheduleSearch(event.currentTarget.value);
    });
    if (restoreSearchFocus && input) {
      const caret = Math.min(Math.max(0, searchCaret), input.value.length);
      input.focus();
      try { input.setSelectionRange(caret, caret); } catch { /* not all input types expose a caret */ }
      restoreSearchFocus = false;
    }
    bindResults(document);
  }

  return { bind, load, normalize: () => payload, remove, render, restore, scheduleSearch, search };
}
