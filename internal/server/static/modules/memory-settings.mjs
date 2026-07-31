import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

export function parseMemoryKeywords(value) {
  const values = Array.isArray(value) ? value : String(value ?? "").split(/[,\n\r]+/);
  const seen = new Set();
  return values.flatMap((entry) => String(entry ?? "").split(/[,\n\r]+/))
    .map((entry) => entry.trim())
    .filter((entry) => {
      if (!entry || seen.has(entry)) return false;
      seen.add(entry);
      return true;
    });
}

export function normalizeMemoryPayload(input = {}, { partial = false } = {}) {
  const source = input && typeof input === "object" ? input : {};
  const payload = {};
  const has = (key) => Object.prototype.hasOwnProperty.call(source, key);
  if (!partial || has("content")) payload.content = String(source.content ?? "").trim();
  if (!partial || has("keywords")) payload.keywords = parseMemoryKeywords(source.keywords);
  if (!partial || has("pinned")) payload.pinned = Boolean(source.pinned);
  if (partial && has("archived")) payload.archived = Boolean(source.archived);
  // Ownership is fixed at creation: the server ignores it on update, so the
  // payload only carries it when creating.
  if (!partial && has("agentId")) {
    const agentId = String(source.agentId ?? "").trim();
    if (agentId) payload.agentId = agentId;
  }
  return payload;
}

export const memoryScopes = Object.freeze(["all", "global", "agent"]);

export function normalizeMemoryScope(value) {
  const scope = String(value ?? "").trim().toLowerCase();
  return memoryScopes.includes(scope) ? scope : "all";
}

export function normalizeMemoryItem(item = {}) {
  const source = item && typeof item === "object" ? item : {};
  return {
    id: String(source.id ?? ""),
    // An empty agentId means the memory is global. A conversation-owned memory
    // is injected on every run of that conversation and never leaves it.
    agentId: String(source.agentId ?? source.agentID ?? ""),
    content: String(source.content ?? ""),
    keywords: parseMemoryKeywords(source.keywords),
    pinned: Boolean(source.pinned),
    archived: Boolean(source.archived ?? source.archivedAt),
    createdAt: String(source.createdAt ?? ""),
    updatedAt: String(source.updatedAt ?? ""),
  };
}

function memoryKeywordsText(keywords) {
  return parseMemoryKeywords(keywords).join(", ");
}

function renderMemoryStatus(item) {
  const badges = [];
  // Scope comes first: it decides whether the entry needs keywords at all.
  badges.push(item.agentId
    ? `<span class="settings-status-pill settings-badge accent">${escapeHtml(t("memory.status.conversation"))}</span>`
    : `<span class="settings-status-pill settings-badge">${escapeHtml(t("memory.status.global"))}</span>`);
  if (item.pinned) badges.push(`<span class="settings-status-pill settings-badge ok">${escapeHtml(t("memory.status.pinned"))}</span>`);
  if (item.archived) badges.push(`<span class="settings-status-pill settings-badge muted">${escapeHtml(t("memory.status.archived"))}</span>`);
  return badges.join(" ");
}

export function renderMemoryItem(value, { saving = false } = {}) {
  const item = normalizeMemoryItem(value);
  const disabled = saving ? " disabled" : "";
  const timestamp = item.updatedAt || item.createdAt;
  return `
    <section class="settings-provider-section settings-card settings-data-list-row${item.archived ? " collapsed" : ""}" data-memory-card="${escapeAttr(item.id)}" aria-busy="${saving ? "true" : "false"}">
      <div class="settings-provider-section-head settings-card-header">
        <div>
          <div class="settings-provider-title settings-card-title">${escapeHtml(item.content || t("memory.emptyItem"))}</div>
          <div class="settings-provider-meta settings-card-description">${escapeHtml(timestamp ? t("memory.updatedAt", { timestamp }) : t("memory.itemId", { id: item.id || t("memory.unknownId") }))}</div>
        </div>
        <div class="settings-action-row">${renderMemoryStatus(item)}</div>
      </div>
      <form class="settings-provider-config-form" data-memory-edit-form="${escapeAttr(item.id)}">
        <div class="settings-provider-form-grid settings-form-grid">
          <label class="settings-form-span-2">${escapeHtml(t("memory.contentLabel"))}
            <textarea class="settings-field settings-textarea" rows="3" data-memory-content required${disabled}>${escapeHtml(item.content)}</textarea>
          </label>
          <label class="settings-form-span-2">${escapeHtml(t("memory.keywordsLabel"))}
            <textarea class="settings-field settings-textarea" rows="2" data-memory-keywords placeholder="${escapeAttr(t("memory.keywordsPlaceholder"))}"${disabled}>${escapeHtml(memoryKeywordsText(item.keywords))}</textarea>
            <small>${escapeHtml(t(item.agentId ? "memory.keywordsOptionalForConversation" : "memory.keywordsRequiredForGlobal"))}</small>
          </label>
        </div>
        <div class="settings-action-row settings-form-actions settings-inline-actions">
          <button class="settings-action-btn primary" type="submit"${disabled}>${escapeHtml(t("memory.saveChanges"))}</button>
          <button class="settings-action-btn subtle" type="button" data-memory-pin="${escapeAttr(item.id)}"${disabled}>${escapeHtml(t(item.pinned ? "memory.unpin" : "memory.pin"))}</button>
          <button class="settings-action-btn subtle" type="button" data-memory-archive="${escapeAttr(item.id)}"${disabled}>${escapeHtml(t(item.archived ? "memory.restore" : "memory.archive"))}</button>
          <button class="settings-action-btn danger destructive" type="button" data-memory-delete="${escapeAttr(item.id)}"${disabled}>${escapeHtml(t("memory.delete"))}</button>
        </div>
      </form>
    </section>
  `;
}

export function renderMemorySettingsContent(value = {}) {
  const state = {
    loading: Boolean(value.loading),
    error: String(value.error ?? ""),
    items: Array.isArray(value.items) ? value.items.map(normalizeMemoryItem) : [],
    query: String(value.query ?? ""),
    includeArchived: Boolean(value.includeArchived),
    saving: Boolean(value.saving),
    scope: normalizeMemoryScope(value.scope),
    agentId: String(value.agentId ?? ""),
    agentTitle: String(value.agentTitle ?? ""),
  };
  const pinnedCount = state.items.filter((item) => item.pinned).length;
  const conversationCount = state.items.filter((item) => item.agentId).length;
  const disabled = state.saving ? " disabled" : "";
  let list = "";
  if (state.loading) {
    list = `<div class="settings-empty-card settings-empty-state settings-skeleton" aria-busy="true">${escapeHtml(t("memory.loading"))}</div>`;
  } else if (!state.items.length) {
    list = `<div class="settings-empty-card settings-empty-state">${escapeHtml(state.query ? t("memory.noMatches", { query: state.query }) : t("memory.emptyList"))}</div>`;
  } else {
    list = state.items.map((item) => renderMemoryItem(item, { saving: state.saving })).join("");
  }

  return `
    <div class="settings-live-page memory-settings-page settings-page-section" aria-live="polite" aria-busy="${state.loading || state.saving ? "true" : "false"}">
      <section class="settings-hero-card settings-card settings-card-header">
        <div>
          <div class="settings-hero-kicker">${escapeHtml(t("memory.heroKicker"))}</div>
          <div class="settings-hero-title settings-card-title">${escapeHtml(t("memory.heroTitle"))}</div>
          <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("memory.heroDescription"))}</p>
        </div>
        <div class="settings-action-row settings-toolbar settings-inline-actions">
          <button id="refreshMemoriesBtn" class="settings-action-btn subtle" type="button"${disabled}>${escapeHtml(t("common.refresh"))}</button>
        </div>
      </section>
      <div class="settings-status-strip settings-stat-grid" aria-label="${escapeAttr(t("memory.currentResults"))}">
        <div class="settings-stat-card"><strong>${escapeHtml(String(state.items.length))}</strong><span>${escapeHtml(t("memory.currentResults"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(conversationCount))}</strong><span>${escapeHtml(t("memory.status.conversation"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(pinnedCount))}</strong><span>${escapeHtml(t("memory.status.pinned"))}</span></div>
      </div>
      ${state.error ? `<div class="settings-inline-alert settings-alert" role="alert" aria-live="assertive">${escapeHtml(state.error)}</div>` : ""}
      <section class="settings-provider-section settings-card settings-page-section highlighted">
        <div class="settings-provider-section-head settings-card-header">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(t("memory.searchTitle"))}</div>
            <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("memory.searchDescription"))}</div>
          </div>
        </div>
        <form id="memorySearchForm" class="settings-provider-config-form">
          <div class="settings-provider-form-grid settings-form-grid">
            <label>${escapeHtml(t("memory.searchLabel"))}
              <input id="memorySearchInput" class="settings-field" value="${escapeAttr(state.query)}" placeholder="${escapeAttr(t("memory.searchPlaceholder"))}"${disabled} />
            </label>
            <label>${escapeHtml(t("memory.scopeLabel"))}
              <select id="memoryScopeFilter" class="settings-field"${disabled}>
                <option value="all" ${state.scope === "all" ? "selected" : ""}>${escapeHtml(t("memory.scopeAll"))}</option>
                <option value="global" ${state.scope === "global" ? "selected" : ""}>${escapeHtml(t("memory.scopeGlobal"))}</option>
                <option value="agent" ${state.scope === "agent" ? "selected" : ""}${state.agentId ? "" : " disabled"}>${escapeHtml(state.agentTitle ? t("memory.scopeNamedConversation", { title: state.agentTitle }) : t("memory.scopeConversation"))}</option>
              </select>
            </label>
            <label class="settings-checkbox-field settings-switch-row">
              <input id="memoryIncludeArchived" type="checkbox" ${state.includeArchived ? "checked" : ""}${disabled} />
              <span>${escapeHtml(t("memory.includeArchived"))}</span>
            </label>
          </div>
          ${state.scope === "agent" && !state.agentId ? `<p class="settings-card-description">${escapeHtml(t("memory.scopeNeedsConversation"))}</p>` : ""}
          <div class="settings-action-row settings-form-actions settings-inline-actions">
            <button class="settings-action-btn primary" type="submit"${disabled}>${escapeHtml(t("common.search"))}</button>
            <button id="clearMemorySearchBtn" class="settings-action-btn subtle" type="button"${disabled}>${escapeHtml(t("memory.clearSearch"))}</button>
          </div>
        </form>
      </section>
      <section class="settings-provider-section settings-card settings-page-section">
        <div class="settings-provider-section-head settings-card-header">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(t("memory.createTitle"))}</div>
            <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("memory.createDescription"))}</div>
          </div>
        </div>
        <form id="createMemoryForm" class="settings-provider-config-form">
          <div class="settings-provider-form-grid settings-form-grid">
            <label class="settings-form-span-2">${escapeHtml(t("memory.contentLabel"))}
              <textarea id="newMemoryContent" class="settings-field settings-textarea" rows="3" required placeholder="${escapeAttr(t("memory.contentPlaceholder"))}"${disabled}></textarea>
            </label>
            <label>${escapeHtml(t("memory.keywordsLabel"))}
              <textarea id="newMemoryKeywords" class="settings-field settings-textarea" rows="2" placeholder="${escapeAttr(t("memory.newKeywordsPlaceholder"))}"${disabled}></textarea>
            </label>
            <label class="settings-checkbox-field settings-switch-row">
              <input id="newMemoryPinned" type="checkbox"${disabled} />
              <span>${escapeHtml(t("memory.pinAfterCreate"))}</span>
            </label>
            ${state.agentId ? `<label class="settings-checkbox-field settings-switch-row settings-form-span-2">
              <input id="newMemoryOwned" type="checkbox"${disabled} />
              <span>${escapeHtml(state.agentTitle ? t("memory.ownByNamedConversation", { title: state.agentTitle }) : t("memory.ownByConversation"))}</span>
            </label>` : ""}
          </div>
          <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("memory.scopeHelp"))}</p>
          <div class="settings-action-row settings-form-actions settings-inline-actions">
            <button class="settings-action-btn primary" type="submit"${disabled}>${escapeHtml(t(state.saving ? "memory.saving" : "memory.create"))}</button>
          </div>
        </form>
      </section>
      <div class="settings-provider-cards settings-data-list" aria-live="polite">${list}</div>
    </div>
  `;
}

export function createMemorySettingsController({
  request,
  onChange,
  showError,
  showToast,
  confirmDelete,
  // Supplies the conversation the panel is opened next to, so a memory can be
  // scoped to it without the user pasting an id.
  getAgent,
} = {}) {
  if (typeof request !== "function") throw new TypeError("Memory settings request must be a function");
  const state = {
    loading: false,
    error: "",
    items: [],
    query: "",
    includeArchived: false,
    saving: false,
    scope: "all",
    requestSeq: 0,
  };
  let loaded = false;

  function currentAgent() {
    const agent = getAgent?.();
    return {
      id: String(agent?.id ?? ""),
      title: String(agent?.title ?? ""),
    };
  }

  function emit() {
    onChange?.(getState());
  }

  function getState() {
    const agent = currentAgent();
    return {
      loading: state.loading,
      error: state.error,
      items: state.items.map((item) => ({ ...item, keywords: [...item.keywords] })),
      query: state.query,
      includeArchived: state.includeArchived,
      saving: state.saving,
      scope: state.scope,
      agentId: agent.id,
      agentTitle: agent.title,
      requestSeq: state.requestSeq,
    };
  }

  function setError(error) {
    state.error = error?.message || String(error || t("memory.requestFailed"));
    showError?.(error instanceof Error ? error : new Error(state.error));
  }

  async function load({ query = state.query, includeArchived = state.includeArchived, scope = state.scope } = {}) {
    state.query = String(query ?? "").slice(0, 200);
    state.includeArchived = Boolean(includeArchived);
    state.scope = normalizeMemoryScope(scope);
    const agent = currentAgent();
    // Without a conversation to scope to, "this conversation" cannot be queried;
    // fall back to everything rather than sending a request the server rejects.
    if (state.scope === "agent" && !agent.id) state.scope = "all";
    const seq = ++state.requestSeq;
    state.loading = true;
    state.error = "";
    emit();
    const params = new URLSearchParams({
      q: state.query,
      includeArchived: String(state.includeArchived),
    });
    if (state.scope !== "all") params.set("scope", state.scope);
    if (state.scope === "agent") params.set("agentId", agent.id);
    try {
      const result = await request(`/api/memories?${params.toString()}`);
      if (seq !== state.requestSeq) return false;
      state.items = Array.isArray(result) ? result.map(normalizeMemoryItem) : [];
      state.error = "";
      loaded = true;
      return true;
    } catch (error) {
      if (seq !== state.requestSeq) return false;
      state.items = [];
      loaded = true;
      setError(error);
      return false;
    } finally {
      if (seq === state.requestSeq) {
        state.loading = false;
        emit();
      }
    }
  }

  async function runMutation(path, options, successMessage) {
    if (state.saving) return false;
    state.saving = true;
    state.error = "";
    state.requestSeq += 1;
    emit();
    try {
      await request(path, options);
      showToast?.(successMessage, "success");
      await load();
      return true;
    } catch (error) {
      setError(error);
      return false;
    } finally {
      state.saving = false;
      emit();
    }
  }

  async function create(input) {
    const payload = normalizeMemoryPayload(input);
    if (!payload.content) {
      const error = new Error(t("memory.contentRequired"));
      setError(error);
      emit();
      return false;
    }
    return runMutation("/api/memories", {
      method: "POST",
      body: JSON.stringify(payload),
    }, t("memory.created"));
  }

  async function update(id, input) {
    const memoryID = String(id ?? "");
    const payload = normalizeMemoryPayload(input, { partial: true });
    if (Object.prototype.hasOwnProperty.call(payload, "content") && !payload.content) {
      const error = new Error(t("memory.contentRequired"));
      setError(error);
      emit();
      return false;
    }
    return runMutation(`/api/memories/${encodeURIComponent(memoryID)}`, {
      method: "PATCH",
      body: JSON.stringify(payload),
    }, t("memory.updated"));
  }

  async function remove(id) {
    const memoryID = String(id ?? "");
    const confirm = confirmDelete || platformConfirm;
    if (!await confirm(t("memory.deleteConfirm"))) return false;
    return runMutation(`/api/memories/${encodeURIComponent(memoryID)}`, {
      method: "DELETE",
    }, t("memory.deleted"));
  }

  function itemByID(id) {
    const memoryID = String(id ?? "");
    return state.items.find((item) => item.id === memoryID);
  }

  function bind() {
    $("memorySearchForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      load({ query: $("memorySearchInput")?.value || "" });
    });
    $("clearMemorySearchBtn")?.addEventListener("click", () => load({ query: "" }));
    $("memoryIncludeArchived")?.addEventListener("change", (event) => load({ includeArchived: event.currentTarget.checked }));
    $("memoryScopeFilter")?.addEventListener("change", (event) => load({ scope: event.currentTarget.value }));
    $("refreshMemoriesBtn")?.addEventListener("click", () => load());
    $("createMemoryForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      create({
        content: $("newMemoryContent")?.value || "",
        keywords: $("newMemoryKeywords")?.value || "",
        pinned: Boolean($("newMemoryPinned")?.checked),
        agentId: $("newMemoryOwned")?.checked ? currentAgent().id : "",
      });
    });
    document.querySelectorAll("[data-memory-edit-form]").forEach((form) => {
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        update(form.dataset.memoryEditForm, {
          content: form.querySelector("[data-memory-content]")?.value || "",
          keywords: form.querySelector("[data-memory-keywords]")?.value || "",
        });
      });
    });
    document.querySelectorAll("[data-memory-pin]").forEach((button) => {
      button.addEventListener("click", () => {
        const item = itemByID(button.dataset.memoryPin);
        if (item) update(item.id, { pinned: !item.pinned });
      });
    });
    document.querySelectorAll("[data-memory-archive]").forEach((button) => {
      button.addEventListener("click", () => {
        const item = itemByID(button.dataset.memoryArchive);
        if (item) update(item.id, { archived: !item.archived });
      });
    });
    document.querySelectorAll("[data-memory-delete]").forEach((button) => {
      button.addEventListener("click", () => remove(button.dataset.memoryDelete));
    });
    if (!loaded && !state.loading) load();
  }

  return {
    bind,
    create,
    getState,
    load,
    remove,
    render: () => renderMemorySettingsContent(state),
    update,
  };
}
