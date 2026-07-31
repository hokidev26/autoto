import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { buildMCPRegistryPayload, parseMCPCommandLine } from "./mcp-registry.mjs";
import { t } from "./i18n.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { api } from "./runtime.mjs";

export function createMCPRegistryUIController({
  state,
  copyText,
  currentSkillsPreferences,
  refreshActiveSettingsPanel,
  showError,
  showToast,
} = {}) {
  function shouldRefreshMCPRegistryPanel() {
    return state.activeSettingsPanel === "skills" && state.activeSkillTab === "mcp-tools";
  }

  async function loadMCPRegistryServers({ force = false } = {}) {
    if (!force && state.mcpRegistryLoaded) return;
    const seq = ++state.mcpRegistrySeq;
    state.mcpRegistryLoading = true;
    state.mcpRegistryError = "";
    if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
    try {
      const servers = await api("/api/mcp/servers");
      if (seq !== state.mcpRegistrySeq) return;
      state.mcpRegistryServers = Array.isArray(servers) ? servers : [];
      state.mcpRegistryLoaded = true;
    } catch (err) {
      if (seq !== state.mcpRegistrySeq) return;
      state.mcpRegistryError = err.message || String(err);
    } finally {
      if (seq === state.mcpRegistrySeq) {
        state.mcpRegistryLoading = false;
        if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
      }
    }
  }

  function mcpRegistryActionKey(id, action) {
    return `${action}:${id}`;
  }

  function isMCPRegistryActionBusy(id, action) {
    return Boolean(state.mcpRegistryActionBusy?.[mcpRegistryActionKey(id, action)]);
  }

  function setMCPRegistryActionBusy(id, action, busy) {
    const key = mcpRegistryActionKey(id, action);
    if (busy) state.mcpRegistryActionBusy = { ...(state.mcpRegistryActionBusy || {}), [key]: true };
    else {
      const next = { ...(state.mcpRegistryActionBusy || {}) };
      delete next[key];
      state.mcpRegistryActionBusy = next;
    }
    if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
  }

  async function discoverMCPRegistryTools(id) {
    if (!id) return;
    setMCPRegistryActionBusy(id, "tools", true);
    try {
      const result = await api(`/api/mcp/servers/${id}/tools`);
      state.mcpRegistryTools = { ...(state.mcpRegistryTools || {}), [id]: result };
      if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
      showToast?.(t("mcp.discoveredTools", { count: result.count || 0 }), "success");
    } finally {
      setMCPRegistryActionBusy(id, "tools", false);
    }
  }

  async function saveMCPRegistryServerFromPanel(event) {
    event.preventDefault();
    const editingId = state.mcpRegistryEditingId || "";
    const payload = readMCPRegistryFormPayload({ omitEmptyEnv: Boolean(editingId) });
    const actionId = editingId || "new";
    const action = editingId ? "update" : "create";
    setMCPRegistryActionBusy(actionId, action, true);
    try {
      if (editingId) {
        await api(`/api/mcp/servers/${editingId}`, { method: "PATCH", body: JSON.stringify(payload) });
        showToast?.(t("mcp.updated"), "success");
      } else {
        await api("/api/mcp/servers", { method: "POST", body: JSON.stringify(payload) });
        showToast?.(t("mcp.created"), "success");
      }
      clearMCPRegistryForm();
      state.mcpRegistryLoaded = false;
      await loadMCPRegistryServers({ force: true });
    } finally {
      setMCPRegistryActionBusy(actionId, action, false);
    }
  }

  function readMCPRegistryFormPayload({ omitEmptyEnv = false } = {}) {
    const envText = $("mcpRegistryEnv")?.value || "";
    const payload = buildMCPRegistryPayload({
      name: $("mcpRegistryName")?.value || "",
      command: $("mcpRegistryCommand")?.value || "",
      argsText: $("mcpRegistryArgs")?.value || "",
      cwd: $("mcpRegistryCWD")?.value || "",
      envText,
      enabled: String($("mcpRegistryEnabled")?.value || "true") !== "false",
    });
    if (omitEmptyEnv && !String(envText || "").trim()) delete payload.env;
    return payload;
  }

  function clearMCPRegistryForm() {
    state.mcpRegistryEditingId = "";
    ["mcpRegistryName", "mcpRegistryCommand", "mcpRegistryArgs", "mcpRegistryCWD", "mcpRegistryEnv"].forEach((id) => {
      const node = $(id);
      if (node) node.value = "";
    });
    const enabled = $("mcpRegistryEnabled");
    if (enabled) enabled.value = "true";
  }

  function editMCPRegistryServer(id) {
    const server = state.mcpRegistryServers.find((item) => item.id === id);
    if (!server) throw new Error(t("mcp.serverNotFound"));
    state.mcpRegistryEditingId = id;
    if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
  }

  function cancelMCPRegistryEdit() {
    clearMCPRegistryForm();
    if (shouldRefreshMCPRegistryPanel()) refreshActiveSettingsPanel?.();
  }

  async function toggleMCPRegistryServer(id) {
    const server = state.mcpRegistryServers.find((item) => item.id === id);
    if (!server) throw new Error(t("mcp.serverNotFound"));
    setMCPRegistryActionBusy(id, "toggle", true);
    try {
      await api(`/api/mcp/servers/${id}`, { method: "PATCH", body: JSON.stringify({ enabled: !server.enabled }) });
      await loadMCPRegistryServers({ force: true });
    } finally {
      setMCPRegistryActionBusy(id, "toggle", false);
    }
  }

  async function deleteMCPRegistryServer(id) {
    const server = state.mcpRegistryServers.find((item) => item.id === id);
    if (!server) throw new Error(t("mcp.serverNotFound"));
    if (!await platformConfirm(t("mcp.deleteConfirm", { name: server.name || id }))) return;
    setMCPRegistryActionBusy(id, "delete", true);
    try {
      await api(`/api/mcp/servers/${id}`, { method: "DELETE" });
      const nextTools = { ...(state.mcpRegistryTools || {}) };
      delete nextTools[id];
      state.mcpRegistryTools = nextTools;
      if (state.mcpRegistryEditingId === id) state.mcpRegistryEditingId = "";
      await loadMCPRegistryServers({ force: true });
      showToast?.(t("mcp.deleted"), "success");
    } finally {
      setMCPRegistryActionBusy(id, "delete", false);
    }
  }

  async function registerMCPDraftServer(id) {
    const draft = currentSkillsPreferences().mcpServers.find((server) => server.id === id);
    if (!draft) throw new Error(t("mcp.draftNotFound"));
    const parsed = parseMCPCommandLine(draft.command);
    if (!parsed.command) throw new Error(t("mcp.draftCommandRequired"));
    setMCPRegistryActionBusy(id, "register", true);
    try {
      await api("/api/mcp/servers", {
        method: "POST",
        body: JSON.stringify({
          name: draft.name || parsed.command,
          transport: "stdio",
          command: parsed.command,
          args: parsed.args,
          enabled: Boolean(draft.enabled),
        }),
      });
      state.mcpRegistryLoaded = false;
      await loadMCPRegistryServers({ force: true });
      showToast?.(t("mcp.registered"), "success");
    } finally {
      setMCPRegistryActionBusy(id, "register", false);
    }
  }

  function renderMCPRegistryList() {
    if (state.mcpRegistryLoading && !state.mcpRegistryLoaded) return `<div class="settings-empty-card settings-empty-state settings-skeleton compact" aria-busy="true">${escapeHtml(t("mcp.loading"))}</div>`;
    if (state.mcpRegistryError) return `<div class="settings-empty-card settings-empty-state settings-alert compact danger" role="alert">${escapeHtml(t("mcp.loadFailed", { message: state.mcpRegistryError }))}</div>`;
    if (!state.mcpRegistryServers.length) return `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("mcp.emptyList"))}</div>`;
    return `<div class="skill-command-list settings-data-list" aria-live="polite" aria-busy="${state.mcpRegistryLoading ? "true" : "false"}">${state.mcpRegistryServers.map(renderMCPRegistryServerCard).join("")}</div>`;
  }

  function renderMCPRegistryServerCard(server) {
    const tools = state.mcpRegistryTools?.[server.id];
    const discovering = isMCPRegistryActionBusy(server.id, "tools");
    const toggling = isMCPRegistryActionBusy(server.id, "toggle");
    const deleting = isMCPRegistryActionBusy(server.id, "delete");
    const status = t(server.enabled ? "mcp.enabled" : "mcp.disabled");
    const args = Array.isArray(server.args) && server.args.length ? ` ${server.args.join(" ")}` : "";
    const envText = Array.isArray(server.envKeys) && server.envKeys.length ? t("mcp.environmentWithKeys", { keys: server.envKeys.join(", ") }) : t("mcp.noEnvironment");
    return `
    <div class="skill-command-card skill-card settings-card settings-data-list-row ${server.enabled ? "" : "disabled"}" aria-busy="${discovering || toggling || deleting ? "true" : "false"}">
      <div class="skill-card-main">
        <div class="skill-command-title settings-card-title">${escapeHtml(server.name || t("mcp.defaultServerName"))} <span class="settings-status-pill settings-badge ${server.enabled ? "ok" : "muted"}">${escapeHtml(status)}</span></div>
        <div class="settings-provider-meta settings-card-description skill-card-meta"><span>${escapeHtml(t("mcp.serverId"))}: <code>${escapeHtml(server.id)}</code></span> · <span>${escapeHtml(server.transport || "stdio")} · ${escapeHtml((server.command || "") + args)}</span> · <span>${escapeHtml(envText)}${server.cwd ? ` · ${escapeHtml(t("mcp.cwd", { cwd: server.cwd }))}` : ""}</span></div>
        ${tools ? renderMCPRegistryToolsResult(tools) : ""}
      </div>
      <div class="settings-action-row settings-inline-actions skill-card-actions">
        <button class="settings-action-btn subtle" type="button" data-mcp-registry-tools="${escapeAttr(server.id)}" ${discovering || !server.enabled ? "disabled" : ""}>${escapeHtml(t(discovering ? "mcp.discovering" : "mcp.discoverTools"))}</button>
        <button class="settings-action-btn subtle" type="button" data-mcp-registry-edit="${escapeAttr(server.id)}" ${discovering || toggling || deleting ? "disabled" : ""}>${escapeHtml(t("mcp.edit"))}</button>
        <button class="settings-action-btn subtle" type="button" data-mcp-registry-toggle="${escapeAttr(server.id)}" ${toggling || deleting ? "disabled" : ""}>${escapeHtml(t(toggling ? "mcp.toggling" : (server.enabled ? "mcp.disable" : "mcp.enable")))}</button>
        <button class="settings-action-btn subtle" type="button" data-mcp-registry-copy="${escapeAttr(server.id)}">${escapeHtml(t("mcp.copyServerId"))}</button>
        <button class="settings-action-btn danger destructive" type="button" data-mcp-registry-delete="${escapeAttr(server.id)}" ${deleting ? "disabled" : ""}>${escapeHtml(t(deleting ? "mcp.deleting" : "mcp.delete"))}</button>
      </div>
    </div>
  `;
  }

  function renderMCPRegistryToolsResult(result) {
    const tools = Array.isArray(result.tools) ? result.tools : [];
    if (!tools.length) return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-discovery" aria-live="polite">${escapeHtml(t("mcp.noToolsReturned"))}</div>`;
    return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-discovery" aria-live="polite"><strong>${escapeHtml(t("mcp.toolsListCount", { count: tools.length }))}</strong> · ${tools.map((tool) => escapeHtml(tool.name || t("mcp.unnamedTool"))).join(" · ")}</div>`;
  }

  function bindMCPRegistryActions(body = $("settingsContentBody")) {
    if (state.activeSkillTab === "mcp-tools" && !state.mcpRegistryLoaded && !state.mcpRegistryLoading) loadMCPRegistryServers().catch(showError);
    $("mcpRegistryForm")?.addEventListener("submit", (event) => saveMCPRegistryServerFromPanel(event).catch(showError));
    $("cancelMCPRegistryEditBtn")?.addEventListener("click", cancelMCPRegistryEdit);
    $("refreshMCPRegistryBtn")?.addEventListener("click", () => loadMCPRegistryServers({ force: true }).catch(showError));
    body?.querySelectorAll("[data-mcp-register]").forEach((node) => node.addEventListener("click", () => registerMCPDraftServer(node.dataset.mcpRegister).catch(showError)));
    body?.querySelectorAll("[data-mcp-registry-tools]").forEach((node) => node.addEventListener("click", () => discoverMCPRegistryTools(node.dataset.mcpRegistryTools).catch(showError)));
    body?.querySelectorAll("[data-mcp-registry-edit]").forEach((node) => node.addEventListener("click", () => editMCPRegistryServer(node.dataset.mcpRegistryEdit)));
    body?.querySelectorAll("[data-mcp-registry-toggle]").forEach((node) => node.addEventListener("click", () => toggleMCPRegistryServer(node.dataset.mcpRegistryToggle).catch(showError)));
    body?.querySelectorAll("[data-mcp-registry-copy]").forEach((node) => node.addEventListener("click", () => copyText(node.dataset.mcpRegistryCopy)));
    body?.querySelectorAll("[data-mcp-registry-delete]").forEach((node) => node.addEventListener("click", () => deleteMCPRegistryServer(node.dataset.mcpRegistryDelete).catch(showError)));
  }

  return {
    bindMCPRegistryActions,
    isMCPRegistryActionBusy,
    loadMCPRegistryServers,
    renderMCPRegistryList,
  };
}
