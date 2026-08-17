import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./messages-skills.mjs";

const catalogPath = "/api/optional-tools/automation";

function catalogState(state) {
  if (!Array.isArray(state.automationToolCatalogItems)) state.automationToolCatalogItems = [];
  if (typeof state.automationToolCatalogLoading !== "boolean") state.automationToolCatalogLoading = false;
  if (typeof state.automationToolCatalogLoaded !== "boolean") state.automationToolCatalogLoaded = false;
  if (typeof state.automationToolCatalogError !== "string") state.automationToolCatalogError = "";
  if (!Number.isSafeInteger(state.automationToolCatalogSeq)) state.automationToolCatalogSeq = 0;
  if (!state.automationToolCatalogBusy || typeof state.automationToolCatalogBusy !== "object") state.automationToolCatalogBusy = {};
  if (!state.automationToolCatalogDiscovery || typeof state.automationToolCatalogDiscovery !== "object") state.automationToolCatalogDiscovery = {};
  return state;
}

function itemName(item = {}) {
  return String(item.name || item.packageName || item.id || "").trim();
}

function isExternalItem(item = {}) {
  const kind = String(item.kind || "").toLowerCase();
  const installMode = String(item.installMode || "").toLowerCase();
  return kind === "external" || kind === "capability" || installMode === "external" || installMode === "capability";
}

export function safeOfficialHTTPSURL(item = {}) {
  for (const candidate of [item.installUrl, item.docsUrl, item.sourceUrl]) {
    try {
      const parsed = new URL(String(candidate || "").trim());
      if (parsed.protocol === "https:") return parsed.href;
    } catch {}
  }
  return "";
}

function busyKey(id, action) {
  return `${action}:${id}`;
}

function listText(value) {
  return Array.isArray(value) && value.length ? value.slice(0, 12).map((entry) => String(entry || "").slice(0, 80)).join(", ") : "—";
}

function catalogCodeText(prefix, value) {
  const raw = String(value || "").trim().slice(0, 64);
  const suffix = raw.replace(/[^a-zA-Z0-9]/g, "");
  if (!suffix) return "—";
  const key = `skillsWorkbench.automationCatalog.${prefix}${suffix[0].toUpperCase()}${suffix.slice(1)}`;
  const translated = t(key);
  return translated === key ? raw : translated;
}

function catalogCodeList(prefix, value) {
  if (!Array.isArray(value) || !value.length) return "—";
  return value.slice(0, 12).map((entry) => catalogCodeText(prefix, entry)).join(" · ");
}

function prerequisiteDetail(prerequisite = {}) {
  const id = String(prerequisite.id || "").trim().replace(/[^a-zA-Z0-9]/g, "").slice(0, 64);
  if (id) {
    const key = `skillsWorkbench.automationCatalog.prerequisite${id[0].toUpperCase()}${id.slice(1)}`;
    const translated = t(key);
    if (translated !== key) return translated;
  }
  return String(prerequisite.detail || "").slice(0, 240);
}

function confirmationValue(value, fallback = "—") {
  const text = String(value || "").trim().slice(0, 500);
  return text || fallback;
}

function yesNo(value) {
  return t(value ? "skillsWorkbench.automationCatalog.yes" : "skillsWorkbench.automationCatalog.no");
}

function discoveryTools(result) {
  if (Array.isArray(result)) return result;
  return Array.isArray(result?.tools) ? result.tools : [];
}

export function createAutomationToolCatalogController({
  state = {},
  request,
  confirmAction = async () => false,
  openExternal,
  onRegistryChanged,
  showError,
  showToast,
  refresh,
} = {}) {
  catalogState(state);

  function emit() {
    refresh?.();
  }

  function findItem(id) {
    return state.automationToolCatalogItems.find((item) => String(item?.id || "") === String(id || ""));
  }

  function isBusy(id, action) {
    return Boolean(state.automationToolCatalogBusy[busyKey(id, action)]);
  }

  function setBusy(id, action, value) {
    const key = busyKey(id, action);
    if (value) state.automationToolCatalogBusy = { ...state.automationToolCatalogBusy, [key]: true };
    else {
      const next = { ...state.automationToolCatalogBusy };
      delete next[key];
      state.automationToolCatalogBusy = next;
    }
    emit();
  }

  async function load({ force = false } = {}) {
    if (!force && (state.automationToolCatalogLoaded || state.automationToolCatalogLoading)) return true;
    const sequence = ++state.automationToolCatalogSeq;
    state.automationToolCatalogLoading = true;
    state.automationToolCatalogError = "";
    emit();
    try {
      const payload = await request(catalogPath);
      if (sequence !== state.automationToolCatalogSeq) return false;
      state.automationToolCatalogItems = Array.isArray(payload) ? payload : [];
      state.automationToolCatalogLoaded = true;
      return true;
    } catch (error) {
      if (sequence !== state.automationToolCatalogSeq) return false;
      state.automationToolCatalogError = error?.message || String(error);
      state.automationToolCatalogLoaded = false;
      showError?.(error);
      return false;
    } finally {
      if (sequence === state.automationToolCatalogSeq) {
        state.automationToolCatalogLoading = false;
        emit();
      }
    }
  }

  async function mutate(id, action, callback) {
    if (isBusy(id, action)) return false;
    setBusy(id, action, true);
    try {
      await callback();
      return true;
    } catch (error) {
      showError?.(error);
      return false;
    } finally {
      setBusy(id, action, false);
    }
  }

  async function install(id) {
    const item = findItem(id);
    if (!item || isExternalItem(item) || !item.canInstall) return false;
    const packageReference = item.packageName && item.version ? `${item.packageName}@${item.version}` : item.packageName || item.version;
    if (!await confirmAction(t("skillsWorkbench.automationCatalog.confirmInstall", {
      name: itemName(item),
      source: confirmationValue(item.sourceUrl),
      packageReference: confirmationValue(packageReference),
      installUrl: confirmationValue(item.installUrl),
      managedPath: confirmationValue(item.managedPath),
    }))) return false;
    return mutate(id, "install", async () => {
      await request(`${catalogPath}/${encodeURIComponent(id)}/install`, { method: "POST" });
      showToast?.(t("skillsWorkbench.automationCatalog.installedToast", { name: itemName(item) }), "success");
      await load({ force: true });
    });
  }

  async function configure(id) {
    const item = findItem(id);
    if (!item || isExternalItem(item) || !item.canConfigure) return false;
    if (!await confirmAction(t("skillsWorkbench.automationCatalog.confirmConfigure", {
      name: itemName(item),
      serverId: confirmationValue(item.mcpServerId || `optional-automation-${id}`),
    }))) return false;
    return mutate(id, "configure", async () => {
      await request(`${catalogPath}/${encodeURIComponent(id)}/configure`, { method: "POST" });
      showToast?.(t("skillsWorkbench.automationCatalog.configuredToast", { name: itemName(item) }), "success");
      await load({ force: true });
      await onRegistryChanged?.();
    });
  }

  async function setEnabled(id, enabled) {
    const item = findItem(id);
    const serverId = String(item?.mcpServerId || "").trim();
    if (!item || isExternalItem(item) || !serverId || !item.canEnable || Boolean(item.enabled) === Boolean(enabled)) return false;
    if (enabled && !await confirmAction(t("skillsWorkbench.automationCatalog.confirmEnable", { name: itemName(item) }))) return false;
    return mutate(id, "enable", async () => {
      await request(`/api/mcp/servers/${encodeURIComponent(serverId)}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: Boolean(enabled) }),
      });
      showToast?.(t(enabled ? "skillsWorkbench.automationCatalog.enabledToast" : "skillsWorkbench.automationCatalog.disabledToast", { name: itemName(item) }), "success");
      await load({ force: true });
      await onRegistryChanged?.();
    });
  }

  async function discover(id) {
    const item = findItem(id);
    const serverId = String(item?.mcpServerId || "").trim();
    if (!item?.enabled || !serverId) return false;
    return mutate(id, "discover", async () => {
      try {
        const result = await request(`/api/mcp/servers/${encodeURIComponent(serverId)}/tools`);
        state.automationToolCatalogDiscovery = { ...state.automationToolCatalogDiscovery, [id]: result };
        emit();
        showToast?.(t("skillsWorkbench.automationCatalog.discoveredToast", { count: discoveryTools(result).length }), "success");
      } catch (error) {
        state.automationToolCatalogDiscovery = {
          ...state.automationToolCatalogDiscovery,
          [id]: { error: error?.message || String(error) },
        };
        emit();
        throw error;
      }
    });
  }

  function openOfficial(id) {
    const item = findItem(id);
    const url = safeOfficialHTTPSURL(item);
    if (!url) {
      showToast?.(t("skillsWorkbench.automationCatalog.unsafeOfficialUrl"), "warn", { force: true });
      return false;
    }
    Promise.resolve(openExternal?.(url)).catch(() => {});
    return true;
  }

  function renderMetadata(item) {
    const fields = [
      ["toolId", item.id],
      ["kind", item.kind],
      ["installMode", item.installMode],
      ["publisher", item.publisher],
      ["license", item.license],
      ["purpose", catalogCodeText("purpose", item.purpose)],
      ["riskBoundary", catalogCodeText("risk", item.riskBoundary)],
      ["dataAccess", catalogCodeList("data", item.dataAccess)],
      ["safetyDefaults", catalogCodeList("safety", item.safetyDefaults)],
      ["packageName", item.packageName],
      ["version", item.version],
      ["sourceUrl", item.sourceUrl],
      ["docsUrl", item.docsUrl],
      ["installUrl", item.installUrl],
      ["platforms", listText(item.platforms)],
      ["supported", yesNo(item.supported)],
      ["managedPath", item.managedPath || "—"],
      ["mcpServerId", item.mcpServerId || "—"],
    ];
    return `<div class="settings-provider-meta settings-card-description skill-card-meta">${fields.map(([key, value]) => `${escapeHtml(t(`skillsWorkbench.automationCatalog.${key}`))}: <code>${escapeHtml(value || "—")}</code>`).join(" · ")}</div>`;
  }

  function renderIdentity(item) {
    return `<div class="settings-provider-meta settings-card-description skill-card-meta"><strong>${escapeHtml(catalogCodeText("purpose", item.purpose))}</strong> · ${escapeHtml(catalogCodeText("risk", item.riskBoundary))}</div>`;
  }

  function renderDetails(item) {
    return `<details class="skill-card-details"><summary>${escapeHtml(t("skillsWorkbench.automationCatalog.detailsSummary"))}</summary>${renderMetadata(item)}</details>`;
  }

  function renderPrerequisites(item) {
    const unavailable = (Array.isArray(item.prerequisites) ? item.prerequisites : []).filter((prerequisite) => !prerequisite?.available).slice(0, 12);
    if (!unavailable.length) return "";
    return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-prerequisites"><strong>${escapeHtml(t("skillsWorkbench.automationCatalog.prerequisites"))}</strong><ul class="skill-findings">${unavailable.map((prerequisite) => {
      const detail = prerequisiteDetail(prerequisite);
      return `<li><strong>${escapeHtml(String(prerequisite?.id || "—").slice(0, 80))}</strong> · ${escapeHtml(t("skillsWorkbench.automationCatalog.prerequisiteUnavailable"))}${detail ? `：${escapeHtml(detail)}` : ""}</li>`;
    }).join("")}</ul></div>`;
  }

  function renderDiscovery(item) {
    const result = state.automationToolCatalogDiscovery[item.id];
    if (!result) return "";
    if (result.error) return `<div class="settings-inline-alert settings-alert skill-card-discovery" role="alert">${escapeHtml(t("skillsWorkbench.automationCatalog.discoveryFailed", { message: result.error }))}</div>`;
    const tools = discoveryTools(result);
    if (!tools.length) return "";
    return `<div class="settings-provider-meta settings-card-description skill-card-meta skill-card-discovery"><strong>${escapeHtml(t("skillsWorkbench.automationCatalog.discoveredTools", { count: tools.length }))}</strong> · ${tools.map((tool) => escapeHtml(tool?.name || t("skillsWorkbench.automationCatalog.unnamedTool"))).join(" · ")}</div>`;
  }

  function renderCard(item) {
    const id = String(item?.id || "");
    const external = isExternalItem(item);
    const installing = isBusy(id, "install");
    const configuring = isBusy(id, "configure");
    const enabling = isBusy(id, "enable");
    const discovering = isBusy(id, "discover");
    const anyBusy = installing || configuring || enabling || discovering;
    const officialURL = safeOfficialHTTPSURL(item);
    return `
      <article class="skill-command-card skill-card settings-card settings-data-list-row ${item.enabled ? "" : "disabled"}" data-automation-tool-id="${escapeAttr(id)}" aria-busy="${anyBusy ? "true" : "false"}">
        <div class="skill-card-main">
          <div class="skill-command-title settings-card-title">${escapeHtml(itemName(item) || t("skillsWorkbench.automationCatalog.unnamed"))}</div>
          ${renderIdentity(item)}
          <div class="settings-action-row settings-inline-actions skill-card-meta">
            <span class="settings-status-pill settings-badge ${item.installed ? "ok" : "muted"}">${escapeHtml(t("skillsWorkbench.automationCatalog.stepInstalled"))}: ${escapeHtml(yesNo(item.installed))}${item.installedVersion ? ` · ${escapeHtml(item.installedVersion)}` : ""}</span>
            <span class="settings-status-pill settings-badge ${item.configured ? "ok" : "muted"}">${escapeHtml(t("skillsWorkbench.automationCatalog.stepConfigured"))}: ${escapeHtml(yesNo(item.configured))}</span>
            <span class="settings-status-pill settings-badge ${item.enabled ? "ok" : "muted"}">${escapeHtml(t("skillsWorkbench.automationCatalog.stepEnabled"))}: ${escapeHtml(yesNo(item.enabled))}</span>
          </div>
          ${renderPrerequisites(item)}
          ${external ? `<div class="settings-inline-alert settings-alert skill-card-boundary" role="note">${escapeHtml(t("skillsWorkbench.automationCatalog.externalBoundary"))}</div>` : ""}
          ${renderDiscovery(item)}
          ${renderDetails(item)}
        </div>
        <div class="settings-action-row settings-inline-actions skill-card-actions">
          ${external ? "" : `<button class="settings-action-btn primary" type="button" data-automation-install="${escapeAttr(id)}" ${!item.canInstall || anyBusy ? "disabled" : ""}>${escapeHtml(t(installing ? "skillsWorkbench.automationCatalog.installing" : "skillsWorkbench.automationCatalog.install"))}</button>
          <button class="settings-action-btn subtle" type="button" data-automation-configure="${escapeAttr(id)}" ${!item.canConfigure || anyBusy ? "disabled" : ""}>${escapeHtml(t(configuring ? "skillsWorkbench.automationCatalog.configuring" : "skillsWorkbench.automationCatalog.configure"))}</button>
          <button class="settings-action-btn subtle" type="button" data-automation-enable="${escapeAttr(id)}" data-automation-enabled="${item.enabled ? "true" : "false"}" ${!item.canEnable || !item.mcpServerId || anyBusy ? "disabled" : ""}>${escapeHtml(t(enabling ? "skillsWorkbench.automationCatalog.changing" : item.enabled ? "skillsWorkbench.automationCatalog.disable" : "skillsWorkbench.automationCatalog.enable"))}</button>
          <button class="settings-action-btn subtle" type="button" data-automation-discover="${escapeAttr(id)}" ${!item.enabled || !item.mcpServerId || anyBusy ? "disabled" : ""}>${escapeHtml(t(discovering ? "skillsWorkbench.automationCatalog.discovering" : "skillsWorkbench.automationCatalog.discover"))}</button>`}
          <button class="settings-action-btn subtle" type="button" data-automation-official="${escapeAttr(id)}" ${!officialURL || anyBusy ? "disabled" : ""}>${escapeHtml(t("skillsWorkbench.automationCatalog.openOfficial"))}</button>
        </div>
      </article>`;
  }

  function render() {
    const items = state.automationToolCatalogItems;
    const loading = state.automationToolCatalogLoading;
    return `
      <section class="settings-provider-section settings-card settings-page-section automation-tool-catalog" aria-live="polite" aria-busy="${loading ? "true" : "false"}">
        <div class="settings-provider-section-head settings-card-header">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(t("skillsWorkbench.automationCatalog.title"))}</div>
            <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(t("skillsWorkbench.automationCatalog.description"))}</div>
          </div>
          <button class="settings-action-btn subtle" type="button" data-automation-refresh ${loading ? "disabled" : ""}>${escapeHtml(t(loading ? "skillsWorkbench.automationCatalog.loading" : "skillsWorkbench.automationCatalog.refresh"))}</button>
        </div>
        ${state.automationToolCatalogError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(t("skillsWorkbench.automationCatalog.loadFailed", { message: state.automationToolCatalogError }))}</div>` : ""}
        <div class="skill-command-list settings-data-list">
          ${loading && !items.length ? `<div class="settings-empty-card settings-empty-state settings-skeleton compact">${escapeHtml(t("skillsWorkbench.automationCatalog.loading"))}</div>` : items.length ? items.map(renderCard).join("") : `<div class="settings-empty-card settings-empty-state compact">${escapeHtml(t("skillsWorkbench.automationCatalog.empty"))}</div>`}
        </div>
      </section>`;
  }

  function bind(root = globalThis.document) {
    if (!state.automationToolCatalogLoaded && !state.automationToolCatalogLoading) load().catch(showError);
    root?.querySelector?.("[data-automation-refresh]")?.addEventListener("click", () => load({ force: true }).catch(showError));
    root?.querySelectorAll?.("[data-automation-install]")?.forEach((node) => node.addEventListener("click", () => install(node.dataset.automationInstall).catch(showError)));
    root?.querySelectorAll?.("[data-automation-configure]")?.forEach((node) => node.addEventListener("click", () => configure(node.dataset.automationConfigure).catch(showError)));
    root?.querySelectorAll?.("[data-automation-enable]")?.forEach((node) => node.addEventListener("click", () => setEnabled(node.dataset.automationEnable, node.dataset.automationEnabled !== "true").catch(showError)));
    root?.querySelectorAll?.("[data-automation-discover]")?.forEach((node) => node.addEventListener("click", () => discover(node.dataset.automationDiscover).catch(showError)));
    root?.querySelectorAll?.("[data-automation-official]")?.forEach((node) => node.addEventListener("click", () => openOfficial(node.dataset.automationOfficial)));
  }

  function snapshot() {
    return {
      items: state.automationToolCatalogItems.map((item) => ({ ...item })),
      loading: state.automationToolCatalogLoading,
      loaded: state.automationToolCatalogLoaded,
      error: state.automationToolCatalogError,
      seq: state.automationToolCatalogSeq,
      busy: { ...state.automationToolCatalogBusy },
      discovery: { ...state.automationToolCatalogDiscovery },
    };
  }

  return { bind, configure, discover, install, load, openOfficial, render, setEnabled, snapshot };
}
