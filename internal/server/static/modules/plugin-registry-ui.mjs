import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./messages-skills.mjs";
import { api as runtimeAPI } from "./runtime.mjs";
import { buildPluginInstallPayload, pluginEnvironmentStatuses, pluginTools } from "./plugin-registry.mjs";

export function createPluginRegistryUIController({
  state,
  api = runtimeAPI,
  refreshActiveSettingsPanel,
  showError,
  showToast,
} = {}) {
  const registry = {
    items: [],
    details: {},
    discoveries: {},
    loaded: false,
    loading: false,
    error: "",
    seq: 0,
    busy: {},
  };

  function shouldRefresh() {
    return state?.activeSettingsPanel === "skills" && state?.activeSkillTab === "plugins";
  }

  function refresh() {
    if (shouldRefresh()) refreshActiveSettingsPanel?.();
  }

  function actionKey(id, action) {
    return `${action}:${id}`;
  }

  function isPluginActionBusy(id, action) {
    return Boolean(registry.busy[actionKey(id, action)]);
  }

  function setPluginActionBusy(id, action, busy) {
    const key = actionKey(id, action);
    if (busy) registry.busy = { ...registry.busy, [key]: true };
    else {
      const next = { ...registry.busy };
      delete next[key];
      registry.busy = next;
    }
    refresh();
  }

  async function loadPlugins({ force = false } = {}) {
    if (!force && registry.loaded) return registry.items;
    const seq = ++registry.seq;
    registry.loading = true;
    registry.error = "";
    refresh();
    try {
      const result = await api("/api/plugins");
      if (seq !== registry.seq) return registry.items;
      registry.items = Array.isArray(result) ? result : [];
      registry.loaded = true;
      return registry.items;
    } catch (error) {
      if (seq === registry.seq) registry.error = error?.message || String(error);
      throw error;
    } finally {
      if (seq === registry.seq) {
        registry.loading = false;
        refresh();
      }
    }
  }

  async function installPlugin(rootPath) {
    const payload = buildPluginInstallPayload(rootPath);
    setPluginActionBusy("new", "install", true);
    try {
      const installed = await api("/api/plugins/install", { method: "POST", body: JSON.stringify(payload) });
      registry.loaded = false;
      await loadPlugins({ force: true });
      showToast?.(t("pluginRegistry.installedDisabled", { name: installed?.name || installed?.id || "" }), "success");
      return installed;
    } finally {
      setPluginActionBusy("new", "install", false);
    }
  }

  async function loadPluginDetail(id, { force = false } = {}) {
    if (!id) return null;
    if (!force && registry.details[id]) return registry.details[id];
    setPluginActionBusy(id, "detail", true);
    try {
      const detail = await api(`/api/plugins/${encodeURIComponent(id)}`);
      registry.details = { ...registry.details, [id]: detail };
      refresh();
      return detail;
    } finally {
      setPluginActionBusy(id, "detail", false);
    }
  }

  async function setPluginEnabled(id, enabled, { confirm = globalThis.window?.confirm } = {}) {
    const action = enabled ? "enable" : "disable";
    if (enabled && confirm) {
      const plugin = registry.items.find((item) => item.id === id) || registry.details[id] || {};
      if (!confirm(t("pluginRegistry.enableConfirm", { name: plugin.name || id }))) return false;
    }
    setPluginActionBusy(id, action, true);
    try {
      const options = { method: "POST" };
      if (enabled) options.body = JSON.stringify({ confirmExecuteLocalCode: true });
      const result = await api(`/api/plugins/${encodeURIComponent(id)}/${action}`, options);
      registry.details = { ...registry.details, [id]: result };
      await loadPlugins({ force: true });
      return result;
    } finally {
      setPluginActionBusy(id, action, false);
    }
  }

  async function discoverPluginTools(id) {
    setPluginActionBusy(id, "discover", true);
    try {
      const result = await api(`/api/plugins/${encodeURIComponent(id)}/discover`, { method: "POST" });
      registry.discoveries = { ...registry.discoveries, [id]: result };
      refresh();
      showToast?.(t("pluginRegistry.discovered", { count: pluginTools(result).length }), "success");
      return result;
    } finally {
      setPluginActionBusy(id, "discover", false);
    }
  }

  async function uninstallPlugin(id, { confirm = globalThis.window?.confirm } = {}) {
    const plugin = registry.items.find((item) => item.id === id) || registry.details[id];
    if (!plugin) throw new Error(t("pluginRegistry.notFound"));
    if (confirm && !confirm(t("pluginRegistry.uninstallConfirm", { name: plugin.name || id }))) return false;
    setPluginActionBusy(id, "uninstall", true);
    try {
      await api(`/api/plugins/${encodeURIComponent(id)}`, { method: "DELETE" });
      const details = { ...registry.details };
      const discoveries = { ...registry.discoveries };
      delete details[id];
      delete discoveries[id];
      registry.details = details;
      registry.discoveries = discoveries;
      await loadPlugins({ force: true });
      showToast?.(t("pluginRegistry.uninstalled"), "success");
      return true;
    } finally {
      setPluginActionBusy(id, "uninstall", false);
    }
  }

  function renderEnvironment(plugin) {
    const statuses = pluginEnvironmentStatuses(plugin);
    if (!statuses.length) return `<div class="settings-provider-meta settings-card-description skill-card-meta">${escapeHtml(t("pluginRegistry.noEnvironment"))}</div>`;
    return `<div class="plugin-env-list settings-inline-actions skill-card-meta">${statuses.map((entry) => `<span class="settings-status-pill settings-badge ${entry.configured ? "ok" : "warn"}">${escapeHtml(entry.key)} · ${escapeHtml(t(entry.configured ? "pluginRegistry.configured" : "pluginRegistry.notConfigured"))}</span>`).join("")}</div>`;
  }

  function renderDiscovery(result) {
    const tools = pluginTools(result);
    if (!result) return "";
    if (!tools.length) return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-discovery" aria-live="polite">${escapeHtml(t("pluginRegistry.noTools"))}</div>`;
    return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-discovery" aria-live="polite"><strong>${escapeHtml(t("pluginRegistry.toolsCount", { count: tools.length }))}</strong> · ${tools.map((tool) => escapeHtml(tool?.exposedName || tool?.remoteName || t("pluginRegistry.unnamedTool"))).join(" · ")}</div>`;
  }

  function renderPluginCard(plugin) {
    const id = String(plugin?.id || "");
    const detail = registry.details[id];
    const view = detail || plugin;
    const enabling = isPluginActionBusy(id, "enable");
    const disabling = isPluginActionBusy(id, "disable");
    const discovering = isPluginActionBusy(id, "discover");
    const uninstalling = isPluginActionBusy(id, "uninstall");
    const loadingDetail = isPluginActionBusy(id, "detail");
    const anyBusy = enabling || disabling || discovering || uninstalling || loadingDetail;
    const metaParts = [
      `${escapeHtml(t("pluginRegistry.id"))}: <code>${escapeHtml(id)}</code>`,
      view.version ? escapeHtml(view.version) : "",
      view.rootPath ? escapeHtml(t("pluginRegistry.sourcePath", { path: view.rootPath })) : "",
    ].filter(Boolean).join(" · ");
    return `<div class="skill-command-card settings-card settings-data-list-row ${view.enabled ? "" : "disabled"}" aria-busy="${anyBusy ? "true" : "false"}">
      <div>
        <div class="skill-command-title settings-card-title">${escapeHtml(view.name || id || t("pluginRegistry.unnamed"))} <span class="settings-status-pill settings-badge ${view.enabled ? "ok" : "muted"}">${escapeHtml(t(view.enabled ? "pluginRegistry.enabled" : "pluginRegistry.disabled"))}</span></div>
        <div class="settings-provider-meta settings-card-description skill-card-meta">${metaParts}</div>
        ${renderEnvironment(view)}
        ${renderDiscovery(registry.discoveries[id])}
      </div>
      <div class="settings-action-row settings-inline-actions">
        <button class="settings-action-btn subtle" type="button" data-plugin-detail="${escapeAttr(id)}" ${anyBusy ? "disabled" : ""}>${escapeHtml(t(loadingDetail ? "pluginRegistry.loadingDetail" : "pluginRegistry.detail"))}</button>
        <button class="settings-action-btn subtle" type="button" data-plugin-discover="${escapeAttr(id)}" ${anyBusy || !view.enabled ? "disabled" : ""}>${escapeHtml(t(discovering ? "pluginRegistry.discovering" : "pluginRegistry.discover"))}</button>
        <button class="settings-action-btn subtle" type="button" data-plugin-toggle="${escapeAttr(id)}" data-plugin-enable="${view.enabled ? "false" : "true"}" ${anyBusy ? "disabled" : ""}>${escapeHtml(t(enabling || disabling ? "pluginRegistry.changing" : view.enabled ? "pluginRegistry.disable" : "pluginRegistry.enable"))}</button>
        <button class="settings-action-btn danger destructive" type="button" data-plugin-uninstall="${escapeAttr(id)}" ${anyBusy ? "disabled" : ""}>${escapeHtml(t(uninstalling ? "pluginRegistry.uninstalling" : "pluginRegistry.uninstall"))}</button>
      </div>
    </div>`;
  }


  function renderPluginRegistryPanel(active) {
    const installing = isPluginActionBusy("new", "install");
    const list = registry.loading && !registry.loaded
      ? `<div class="settings-empty-card settings-empty-state settings-skeleton compact" aria-busy="true">${escapeHtml(t("pluginRegistry.loading"))}</div>`
      : registry.error
        ? `<div class="settings-empty-card settings-empty-state settings-alert compact danger" role="alert">${escapeHtml(t("pluginRegistry.loadFailed", { message: registry.error }))}</div>`
        : registry.items.length
          ? `<div class="skill-command-list settings-data-list" aria-live="polite" aria-busy="${registry.loading ? "true" : "false"}">${registry.items.map(renderPluginCard).join("")}</div>`
          : `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("pluginRegistry.empty"))}</div>`;
    return `<p class="skills-description settings-card-description" data-settings-help-copy>${escapeHtml(active.description)}</p>
      <section class="settings-provider-section settings-card settings-page-section highlighted" aria-busy="${registry.loading ? "true" : "false"}">
        <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(t("pluginRegistry.title"))}</div><div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("pluginRegistry.description"))}</div></div><button id="refreshPluginsBtn" class="settings-action-btn subtle" type="button" ${registry.loading ? "disabled" : ""}>${escapeHtml(t(registry.loading ? "pluginRegistry.refreshing" : "pluginRegistry.refresh"))}</button></div>
        <div class="settings-card-content">${list}</div>
      </section>
      <section class="settings-provider-section settings-card settings-page-section" aria-busy="${installing ? "true" : "false"}">
        <div class="settings-provider-title settings-card-title">${escapeHtml(t("pluginRegistry.installTitle"))}</div>
        <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("pluginRegistry.installDescription"))}</div>
        <form id="pluginInstallForm" class="skill-command-form"><div class="settings-provider-form-grid settings-form-grid"><label class="settings-form-span-2 settings-form-field">${escapeHtml(t("pluginRegistry.pathLabel"))}<input id="pluginRootPath" class="settings-field" placeholder="${escapeAttr(t("pluginRegistry.pathPlaceholder"))}" /></label></div><div class="settings-action-row settings-form-actions settings-inline-actions"><button class="settings-action-btn primary" type="submit" ${installing ? "disabled" : ""}>${escapeHtml(t(installing ? "pluginRegistry.installing" : "pluginRegistry.install"))}</button></div></form>
        <div class="settings-provider-note settings-alert" role="note">${escapeHtml(t("pluginRegistry.uninstallNote"))}</div>
      </section>`;
  }

  function bindPluginRegistryActions(body = $("settingsContentBody")) {
    if (!registry.loaded && !registry.loading) loadPlugins().catch(showError);
    $("refreshPluginsBtn")?.addEventListener("click", () => loadPlugins({ force: true }).catch(showError));
    $("pluginInstallForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      installPlugin($("pluginRootPath")?.value || "").then(() => {
        const input = $("pluginRootPath");
        if (input) input.value = "";
      }).catch(showError);
    });
    body?.querySelectorAll("[data-plugin-detail]").forEach((node) => node.addEventListener("click", () => loadPluginDetail(node.dataset.pluginDetail, { force: true }).catch(showError)));
    body?.querySelectorAll("[data-plugin-discover]").forEach((node) => node.addEventListener("click", () => discoverPluginTools(node.dataset.pluginDiscover).catch(showError)));
    body?.querySelectorAll("[data-plugin-toggle]").forEach((node) => node.addEventListener("click", () => setPluginEnabled(node.dataset.pluginToggle, node.dataset.pluginEnable === "true").catch(showError)));
    body?.querySelectorAll("[data-plugin-uninstall]").forEach((node) => node.addEventListener("click", () => uninstallPlugin(node.dataset.pluginUninstall).catch(showError)));
  }

  return {
    bindPluginRegistryActions,
    discoverPluginTools,
    installPlugin,
    isPluginActionBusy,
    loadPluginDetail,
    loadPlugins,
    registry,
    renderPluginRegistryPanel,
    setPluginEnabled,
    uninstallPlugin,
  };
}
