import { accountPreferencesCurrentSetupVersion } from "./account-preferences.mjs";
import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { isDesktopShell, openExternal } from "./platform.mjs";
import { remoteAccessContext } from "./remote-access-capabilities.mjs";
import {
  codexAccountActionRequest,
  codexBrowserLoginRequest,
  normalizeCodexBrowserLoginStatus,
  trustedCodexBrowserAuthURL,
} from "./provider-settings-normalization.mjs";

export const setupWizardVersion = accountPreferencesCurrentSetupVersion;
export const setupWizardStepIds = Object.freeze(["welcome", "environment", "model", "complete"]);
export const setupQuickProviderTypes = Object.freeze(["openai", "openai-compatible", "anthropic", "gemini-interactions"]);
export const setupCodexLoginActiveStatuses = Object.freeze(["starting", "pending", "exchanging"]);

const setupProviderNamePattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const setupInstallPollInterval = 2000;
const setupEnvironmentPollInterval = 5000;

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value || {}, key);
}

function providerSupportsRuntime(provider = {}) {
  if (provider?.enabled === false || provider?.configured === false) return false;
  const runtimeSignals = [provider?.runtimeAvailable, provider?.registered]
    .filter((value) => value !== undefined && value !== null)
    .map(Boolean);
  if (runtimeSignals.length) return runtimeSignals.every(Boolean);
  if (hasOwn(provider, "available")) return provider.available === true;
  if (provider?.discovered === false || provider?.error) return false;
  return true;
}

export function discoverSetupModels(catalog = {}) {
  const providers = Array.isArray(catalog?.providers) ? catalog.providers : [];
  return providers.flatMap((provider) => {
    const providerName = String(provider?.name || "").trim();
    const models = Array.isArray(provider?.models) ? provider.models : [];
    if (!providerName || !providerSupportsRuntime(provider)) return [];
    return models
      .map((model) => String(model || "").trim())
      .filter(Boolean)
      .map((model) => ({
        provider: providerName,
        type: String(provider?.type || "").trim(),
        model,
        value: `${providerName}:${model}`,
        configured: provider?.configured !== false,
        available: true,
        capabilities: provider?.modelCapabilities?.[model] || provider?.capabilities || {},
        error: String(provider?.error || ""),
      }));
  });
}

export function filterSetupModels(models, filter = "") {
  const list = Array.isArray(models) ? models : [];
  const needle = String(filter || "").trim().toLowerCase();
  if (!needle) return list;
  return list.filter((item) => `${item?.model || ""} ${item?.provider || ""} ${item?.type || ""}`.toLowerCase().includes(needle));
}

export function groupSetupModels(models) {
  const groups = [];
  const byProvider = new Map();
  for (const item of Array.isArray(models) ? models : []) {
    const key = String(item?.provider || "");
    if (!byProvider.has(key)) {
      const group = { provider: key, type: String(item?.type || ""), models: [] };
      byProvider.set(key, group);
      groups.push(group);
    }
    byProvider.get(key).models.push(item);
  }
  return groups;
}

export function setupQuickProviderIssues(draft = {}) {
  const issues = [];
  const name = String(draft.name || "").trim();
  const type = String(draft.type || "").trim();
  const baseUrl = String(draft.baseUrl || "").trim();
  if (!setupProviderNamePattern.test(name)) issues.push("invalidName");
  if (!setupQuickProviderTypes.includes(type)) issues.push("invalidType");
  if (type === "openai-compatible" && !baseUrl) issues.push("baseUrlRequired");
  if (baseUrl && !/^https?:\/\//i.test(baseUrl)) issues.push("baseUrlInvalid");
  return issues;
}

export function setupQuickProviderResolvedModel(draft = {}, discovered = []) {
  const typed = String(draft?.model || "").trim();
  if (typed) return typed;
  const list = Array.isArray(discovered) ? discovered : [];
  for (const item of list) {
    const model = String(item || "").trim();
    if (model) return model;
  }
  return "";
}

export function setupQuickProviderTestSucceeded(response) {
  return Boolean(response?.reachable) && response?.configured !== false && !response?.errorCode;
}

export function setupCodexLoginActive(status) {
  return setupCodexLoginActiveStatuses.includes(String(status || "").trim().toLowerCase());
}

export function firstSetupCodexModel(models = []) {
  const list = Array.isArray(models) ? models : [];
  return list.find((item) => String(item?.type || "").trim().toLowerCase() === "codex") || null;
}

export function normalizeSetupTool(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const id = String(source.id || source.name || "").trim().toLowerCase().slice(0, 40);
  return {
    id,
    available: Boolean(source.available ?? source.installed ?? source.ready),
    required: Boolean(source.required),
    recommended: Boolean(source.recommended),
    builtIn: Boolean(source.builtIn ?? source.builtin),
    version: String(source.version || "").trim().replace(/[\r\n]+/g, " ").slice(0, 160),
    installCommand: String(source.installCommand || "").trim().replace(/[\r\n]+/g, " ").slice(0, 500),
  };
}

export function normalizeSetupStatus(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const rawTools = Array.isArray(source.tools) ? source.tools : Array.isArray(source.requirements) ? source.requirements : [];
  const packageManagerSource = source.packageManager;
  const packageManager = typeof packageManagerSource === "string"
    ? { name: packageManagerSource.trim(), available: Boolean(packageManagerSource.trim()) }
    : {
        name: String(packageManagerSource?.name || "").trim().slice(0, 40),
        available: Boolean(packageManagerSource?.available ?? packageManagerSource?.name),
      };
  const tools = rawTools.map(normalizeSetupTool).filter((tool) => tool.id && tool.id !== "database");
  const databaseSource = source.database && typeof source.database === "object" ? source.database : {};
  const database = {
    available: Boolean(databaseSource.available ?? databaseSource.ready ?? source.databaseReady ?? source.databaseAvailable),
    version: String(databaseSource.version || databaseSource.detail || "").trim().replace(/[\r\n]+/g, " ").slice(0, 160),
  };
  return {
    loaded: Boolean(source.loaded),
    error: String(source.error || "").trim().slice(0, 240),
    platform: {
      os: String(source.platform?.os || source.os || "").trim().slice(0, 40),
      arch: String(source.platform?.arch || source.arch || "").trim().slice(0, 40),
    },
    database,
    packageManager,
    tools,
  };
}

export function setupToolById(status, id) {
  return normalizeSetupStatus(status).tools.find((tool) => tool.id === id) || null;
}

export function setupEnvironmentReady(status) {
  const normalized = normalizeSetupStatus(status);
  if (!normalized.loaded || normalized.error || !normalized.database.available) return false;
  const required = normalized.tools.filter((tool) => tool.required);
  return required.length > 0 && required.every((tool) => tool.available);
}

export function setupSelectedModel(models, preferredModel = "") {
  const values = Array.isArray(models) ? models.map((item) => String(item?.value || "").trim()).filter(Boolean) : [];
  const preferred = String(preferredModel || "").trim();
  return values.includes(preferred) ? preferred : values[0] || "";
}

export function setupWizardStartupDecision({
  setupVersion = 0,
  preferredModel = "",
  catalog = {},
  currentVersion = setupWizardVersion,
  remote = false,
} = {}) {
  const models = discoverSetupModels(catalog);
  const preferred = String(preferredModel || "").trim();
  const completed = Math.max(0, Number(setupVersion) || 0) >= currentVersion;
  const preferredReady = Boolean(preferred) && models.some((item) => item.value === preferred);
  // Official login, tool installs, and first-run checks are host work. A phone
  // session must not be sent through that flow just because it is a new browser
  // or the catalog has not arrived yet. The sidebar still opens the wizard.
  if (remote) return { open: false, step: "welcome", reason: "remote-skip", models, preferredReady };
  if (!completed) return { open: true, step: "welcome", reason: "first-run", models, preferredReady };
  if (preferredReady) return { open: false, step: "welcome", reason: "complete", models, preferredReady };
  return { open: true, step: "model", reason: "model-unavailable", models, preferredReady };
}

export function setupCanFinish({ status, catalog, selectedModel } = {}) {
  const models = discoverSetupModels(catalog);
  return setupEnvironmentReady(status) && models.some((item) => item.value === String(selectedModel || "").trim());
}

function setupStepIndex(step) {
  const index = setupWizardStepIds.indexOf(String(step || ""));
  return index >= 0 ? index : 0;
}

function setupToolLabel(id) {
  return t(`setupWizard.tools.${id}.title`);
}

function setupToolDescription(id) {
  return t(`setupWizard.tools.${id}.description`);
}

export function createSetupWizardController({
  state,
  request,
  loadModelCatalog,
  loadSettings,
  loadSetupStatus,
  openSettingsModal,
  renderModelOptions,
  getPreferredModel,
  getSetupVersion,
  completeSetup,
  preferencesPending,
  copyText,
  showToast,
} = {}) {
  let activeStep = 0;
  let selectedModel = "";
  let setupStatus = normalizeSetupStatus();
  let automaticFlow = false;
  let openReason = "manual";
  let suspendedForProviderSettings = false;
  let previousFocus = null;
  let refreshInFlight = null;
  let finishing = false;
  let wizardOpen = false;
  let modelFilter = "";
  let installJobs = {};
  let installPollTimer = null;
  let environmentPollTimer = null;
  let pendingFocusSelector = "";
  const quickProvider = {
    open: false,
    type: "openai-compatible",
    name: "",
    baseUrl: "",
    apiKey: "",
    model: "",
    busy: "",
    notice: null,
    discoveredModels: [],
  };
  const codexLogin = {
    seq: 0,
    loginId: "",
    status: "idle",
    authUrl: "",
    message: "",
    account: null,
    popupBlocked: false,
  };
  let verify = { status: "idle", model: "", message: "" };

  function models() {
    return discoverSetupModels(state?.modelCatalog);
  }

  function currentStepID() {
    return setupWizardStepIds[activeStep] || setupWizardStepIds[0];
  }

  function preferredModel() {
    return String(getPreferredModel?.() || "").trim();
  }

  function wizardDismissible() {
    return !automaticFlow || openReason !== "first-run";
  }

  function setWizardOwnedInert(node, active) {
    if (!node) return;
    if (active) {
      if (!node.hasAttribute("inert")) {
        node.setAttribute("inert", "");
        node.dataset.setupWizardInert = "true";
      }
      return;
    }
    if (node.dataset.setupWizardInert === "true") {
      node.removeAttribute("inert");
      delete node.dataset.setupWizardInert;
    }
  }

  function setWizardIsolation(active, { allowSettings = false, restoreFocus = true } = {}) {
    if (active && !previousFocus) previousFocus = globalThis.document?.activeElement || null;
    setWizardOwnedInert($("appShell"), active);
    setWizardOwnedInert($("settingsModal"), active && !allowSettings);
    if (active) return;
    const target = previousFocus;
    previousFocus = null;
    if (restoreFocus) globalThis.queueMicrotask?.(() => target?.isConnected && target?.focus?.());
  }

  function focusSetupWizard() {
    globalThis.queueMicrotask?.(() => {
      const modal = $("setupWizardModal");
      if (!modal || modal.classList.contains("hidden")) return;
      modal.querySelector?.(".setup-wizard-card")?.focus?.();
    });
  }

  function setupWizardFocusableElements() {
    const modal = $("setupWizardModal");
    if (!modal) return [];
    return [...modal.querySelectorAll("button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
      .filter((node) => !node.closest?.(".hidden"));
  }

  function handleSetupWizardKeydown(event) {
    event.stopPropagation();
    if (event.key === "Escape") {
      event.preventDefault();
      closeSetupWizard();
      return;
    }
    if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key) && event.target?.closest?.("[data-setup-model]")) {
      event.preventDefault();
      moveModelSelection(event.key);
      return;
    }
    if (event.key !== "Tab") return;
    const focusable = setupWizardFocusableElements();
    if (!focusable.length) {
      event.preventDefault();
      focusSetupWizard();
      return;
    }
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = globalThis.document?.activeElement;
    if (event.shiftKey && (active === first || !$("setupWizardModal")?.contains?.(active))) {
      event.preventDefault();
      last.focus?.();
    } else if (!event.shiftKey && (active === last || !$("setupWizardModal")?.contains?.(active))) {
      event.preventDefault();
      first.focus?.();
    }
  }

  function moveModelSelection(key) {
    const visible = filterSetupModels(models(), modelFilter);
    if (!visible.length) return;
    const values = visible.map((item) => item.value);
    let index = values.indexOf(selectedModel);
    if (key === "Home") index = 0;
    else if (key === "End") index = values.length - 1;
    else if (key === "ArrowDown") index = index < 0 ? 0 : Math.min(values.length - 1, index + 1);
    else if (key === "ArrowUp") index = index < 0 ? 0 : Math.max(0, index - 1);
    if (values[index] === selectedModel) return;
    selectedModel = values[index];
    pendingFocusSelector = "@model-selected";
    renderSetupWizard();
  }

  function closeSetupWizard({ force = false } = {}) {
    if (automaticFlow && !wizardDismissible() && !force) return false;
    $("setupWizardModal")?.classList.add("hidden");
    setWizardIsolation(false);
    automaticFlow = false;
    suspendedForProviderSettings = false;
    wizardOpen = false;
    if (setupCodexLoginActive(codexLogin.status)) {
      const seq = Number(codexLogin.seq || 0) + 1;
      const loginId = codexLogin.loginId;
      Object.assign(codexLogin, { seq, loginId: "", status: "cancelled", authUrl: "", popupBlocked: false });
      if (loginId) {
        const requestSpec = codexBrowserLoginRequest("cancel", loginId);
        Promise.resolve(request?.(requestSpec.path, requestSpec.options)).catch(() => {});
      }
    }
    syncBackgroundPolling();
    return true;
  }

  function renderProgress() {
    const progress = $("setupWizardProgress");
    if (progress) {
      progress.innerHTML = setupWizardStepIds.map((step, index) => `
        <button class="setup-wizard-progress-dot ${index < activeStep ? "complete" : ""} ${index === activeStep ? "active" : ""}"
          type="button" data-setup-step-jump="${index}" ${index < activeStep && !finishing ? "" : "disabled"}
          aria-label="${escapeAttr(t(`setupWizard.steps.${setupWizardStepIds[index]}`))}"></button>
      `).join("");
    }
    const label = $("setupWizardStepLabel");
    if (label) label.textContent = `${t("setupWizard.stepProgress", { current: activeStep + 1, total: setupWizardStepIds.length })} · ${t(`setupWizard.steps.${currentStepID()}`)}`;
  }

  function featureCard(icon, title, description) {
    return `
      <article class="setup-wizard-feature-card">
        <span class="setup-wizard-feature-icon" aria-hidden="true">${icon}</span>
        <span><strong>${escapeHtml(title)}</strong><small>${escapeHtml(description)}</small></span>
      </article>
    `;
  }

  function renderWelcome() {
    return `
      <section class="setup-wizard-step setup-wizard-welcome">
        <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.welcome.eyebrow"))}</span>
        <h2>${escapeHtml(t("setupWizard.welcome.title"))}</h2>
        <p>${escapeHtml(t("setupWizard.welcome.description"))}</p>
        <div class="setup-wizard-feature-grid">
          ${featureCard("✦", t("setupWizard.welcome.modelTitle"), t("setupWizard.welcome.modelDescription"))}
          ${featureCard("⌘", t("setupWizard.welcome.toolsTitle"), t("setupWizard.welcome.toolsDescription"))}
          ${featureCard("✓", t("setupWizard.welcome.onceTitle"), t("setupWizard.welcome.onceDescription"))}
        </div>
      </section>
    `;
  }

  function toolImportance(tool) {
    if (tool.required) return { className: "required", label: t("setupWizard.environment.required") };
    if (tool.builtIn) return { className: "built-in", label: t("setupWizard.environment.builtIn") };
    if (tool.recommended) return { className: "recommended", label: t("setupWizard.environment.recommended") };
    return { className: "optional", label: t("setupWizard.environment.optional") };
  }

  function installJobFor(toolID) {
    const job = installJobs[toolID];
    return job && typeof job === "object" ? job : null;
  }

  function anyInstallRunning() {
    return Object.values(installJobs).some((job) => job?.status === "running");
  }

  function renderToolInstall(tool) {
    if (tool.available || !tool.installCommand) return "";
    const job = installJobFor(tool.id);
    const running = job?.status === "running";
    const failed = job?.status === "failed";
    const succeeded = job?.status === "succeeded";
    const installDisabled = running || anyInstallRunning();
    const installLabel = running
      ? t("setupWizard.actions.installing")
      : failed
        ? t("setupWizard.actions.retryInstall")
        : t("setupWizard.actions.installNow");
    let stateNote = "";
    if (running) {
      stateNote = `<span class="setup-wizard-install-note running">${escapeHtml(t("setupWizard.environment.installRunning"))}</span>`;
    } else if (succeeded) {
      stateNote = `<span class="setup-wizard-install-note ok">${escapeHtml(t("setupWizard.environment.installSucceeded", { tool: setupToolLabel(tool.id) }))}</span>`;
    } else if (failed) {
      const detail = String(job?.output || job?.error || "").trim().slice(-400);
      stateNote = `
        <span class="setup-wizard-install-note error">${escapeHtml(t("setupWizard.environment.installFailed", { tool: setupToolLabel(tool.id) }))}</span>
        ${detail ? `<code class="setup-wizard-command error">${escapeHtml(detail)}</code>` : ""}
      `;
    }
    return `
      <span class="setup-wizard-tool-install">
        <code class="setup-wizard-command">${escapeHtml(tool.installCommand)}</code>
        <span class="setup-wizard-install-actions">
          <button class="setup-wizard-mini-btn primary" type="button" data-setup-install="${escapeAttr(tool.id)}" ${installDisabled ? "disabled" : ""}>${escapeHtml(installLabel)}</button>
          <button class="setup-wizard-mini-btn" type="button" data-setup-copy-install="${escapeAttr(tool.id)}">${escapeHtml(t("setupWizard.actions.copyInstall"))}</button>
        </span>
        ${stateNote}
      </span>
    `;
  }

  function renderTool(tool) {
    const importance = toolImportance(tool);
    const statusLabel = tool.available
      ? tool.version || t("setupWizard.environment.available")
      : tool.recommended && !tool.required
        ? t("setupWizard.environment.limited")
        : t("setupWizard.environment.missing");
    return `
      <article class="setup-wizard-tool ${tool.available ? "available" : "missing"}">
        <span class="setup-wizard-tool-state" aria-hidden="true">${tool.available ? "✓" : "!"}</span>
        <span class="setup-wizard-tool-copy">
          <span class="setup-wizard-tool-heading">
            <strong>${escapeHtml(setupToolLabel(tool.id))}</strong>
            <span class="setup-wizard-requirement ${importance.className}">${escapeHtml(importance.label)}</span>
          </span>
          <small>${escapeHtml(setupToolDescription(tool.id))}</small>
        </span>
        <span class="setup-wizard-tool-meta">
          <span>${escapeHtml(statusLabel)}</span>
        </span>
        ${renderToolInstall(tool)}
      </article>
    `;
  }

  function environmentHasGaps() {
    const normalized = normalizeSetupStatus(setupStatus);
    if (!normalized.loaded || normalized.error || !normalized.database.available) return true;
    return normalized.tools.some((tool) => !tool.available);
  }

  function renderEnvironment() {
    const normalized = normalizeSetupStatus(setupStatus);
    if (normalized.error || !normalized.loaded) {
      return `
        <section class="setup-wizard-step">
          <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.environment.eyebrow"))}</span>
          <h2>${escapeHtml(t("setupWizard.environment.title"))}</h2>
          <p>${escapeHtml(t("setupWizard.environment.description"))}</p>
          <div class="setup-wizard-state-card error" role="alert">${escapeHtml(normalized.error || t("setupWizard.environment.checkFailed"))}</div>
        </section>
      `;
    }
    const manager = normalized.packageManager.available && normalized.packageManager.name
      ? t("setupWizard.environment.packageManager", { manager: normalized.packageManager.name })
      : `${t("setupWizard.environment.packageManagerMissing")} · ${t("setupWizard.environment.installUnavailable")}`;
    const blocked = !setupEnvironmentReady(normalized)
      ? `<div class="setup-wizard-state-card error" role="alert">${escapeHtml(t("setupWizard.environment.blocked"))}</div>`
      : "";
    const autoRefreshHint = environmentHasGaps()
      ? `<div class="setup-wizard-state-card">${escapeHtml(t("setupWizard.environment.autoRefreshHint"))}</div>`
      : "";
    const environmentTools = [
      normalizeSetupTool({
        id: "database",
        available: normalized.database.available,
        required: true,
        builtIn: true,
        version: normalized.database.version,
      }),
      ...normalized.tools,
    ];
    return `
      <section class="setup-wizard-step">
        <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.environment.eyebrow"))}</span>
        <h2>${escapeHtml(t("setupWizard.environment.title"))}</h2>
        <p>${escapeHtml(t("setupWizard.environment.description"))}</p>
        <div class="setup-wizard-package-manager">${escapeHtml(manager)}</div>
        <div class="setup-wizard-tool-list">${environmentTools.map(renderTool).join("")}</div>
        ${blocked}
        ${autoRefreshHint}
      </section>
    `;
  }

  function quickProviderNoticeCard() {
    if (!quickProvider.notice) return "";
    const kind = quickProvider.notice.kind === "ok" ? "ok" : "error";
    return `<div class="setup-wizard-state-card ${kind}" role="status">${escapeHtml(String(quickProvider.notice.text || ""))}</div>`;
  }

  function renderCodexLoginCard() {
    const remoteOnly = remoteAccessContext(state);
    const active = setupCodexLoginActive(codexLogin.status);
    const connected = Boolean(firstSetupCodexModel(models()));
    const busy = active || finishing;
    let notice = "";
    if (remoteOnly) notice = t("setupWizard.codexLogin.localOnly");
    else if (codexLogin.popupBlocked) notice = t("setupWizard.codexLogin.popupBlocked");
    else if (codexLogin.status === "failed") notice = t("setupWizard.codexLogin.failed", { message: codexLogin.message || t("setupWizard.codexLogin.unknown") });
    else if (codexLogin.status === "expired") notice = t("setupWizard.codexLogin.expired");
    else if (codexLogin.status === "cancelled") notice = t("setupWizard.codexLogin.cancelled");
    else if (codexLogin.status === "completed" && connected) notice = t("setupWizard.codexLogin.success");
    else if (codexLogin.status === "completed") notice = t("setupWizard.codexLogin.modelsPending");
    else if (active) notice = t(`setupWizard.codexLogin.${codexLogin.status === "exchanging" ? "exchanging" : "pending"}`);
    const actionLabel = active
      ? t("setupWizard.codexLogin.waiting")
      : t(connected ? "setupWizard.codexLogin.addAnother" : "setupWizard.codexLogin.action");
    return `
      <section class="setup-wizard-codex-login" aria-labelledby="setup-wizard-codex-title" aria-busy="${active ? "true" : "false"}">
        <span class="setup-wizard-quick-form-heading">
          <strong id="setup-wizard-codex-title">${escapeHtml(t("setupWizard.codexLogin.title"))}</strong>
          <small>${escapeHtml(t("setupWizard.codexLogin.description"))}</small>
        </span>
        <span class="setup-wizard-quick-actions">
          <button class="settings-action-btn primary" type="button" data-setup-codex-login ${busy || remoteOnly ? "disabled" : ""}>${escapeHtml(actionLabel)}</button>
          ${active ? `<button class="ghost-btn" type="button" data-setup-codex-reopen>${escapeHtml(t("setupWizard.codexLogin.reopen"))}</button>
            <button class="ghost-btn" type="button" data-setup-codex-cancel>${escapeHtml(t("setupWizard.codexLogin.cancel"))}</button>` : ""}
        </span>
        ${notice ? `<div class="setup-wizard-state-card ${codexLogin.status === "failed" || remoteOnly || (codexLogin.status === "completed" && !connected) ? "error" : connected && codexLogin.status === "completed" ? "ok" : ""}" role="status">${escapeHtml(notice)}</div>` : ""}
      </section>
    `;
  }

  function renderQuickProviderForm() {
    const busy = quickProvider.busy;
    const typeOptions = setupQuickProviderTypes.map((type) => `
      <option value="${escapeAttr(type)}" ${quickProvider.type === type ? "selected" : ""}>${escapeHtml(t(`setupWizard.quickProvider.types.${type}`))}</option>
    `).join("");
    const baseUrlRequired = quickProvider.type === "openai-compatible";
    return `
      <form class="setup-wizard-quick-form" data-setup-qp-form novalidate>
        <span class="setup-wizard-quick-form-heading">
          <strong>${escapeHtml(t("setupWizard.quickProvider.title"))}</strong>
          <small>${escapeHtml(t("setupWizard.quickProvider.description"))}</small>
        </span>
        <div class="setup-wizard-quick-grid">
          <label>
            <span>${escapeHtml(t("setupWizard.quickProvider.typeLabel"))}</span>
            <select data-setup-qp-field="type" ${busy ? "disabled" : ""}>${typeOptions}</select>
          </label>
          <label>
            <span>${escapeHtml(t("setupWizard.quickProvider.nameLabel"))}</span>
            <input type="text" data-setup-qp-field="name" value="${escapeAttr(quickProvider.name)}" placeholder="my-provider" autocomplete="off" spellcheck="false" ${busy ? "disabled" : ""}>
          </label>
          <label class="setup-wizard-quick-wide">
            <span>${escapeHtml(t("setupWizard.quickProvider.baseUrlLabel"))}${baseUrlRequired ? " *" : ""}</span>
            <input type="text" data-setup-qp-field="baseUrl" value="${escapeAttr(quickProvider.baseUrl)}" placeholder="https://api.example.com/v1" autocomplete="off" spellcheck="false" ${busy ? "disabled" : ""}>
          </label>
          <label class="setup-wizard-quick-wide">
            <span>${escapeHtml(t("setupWizard.quickProvider.apiKeyLabel"))}</span>
            <input type="password" data-setup-qp-field="apiKey" value="${escapeAttr(quickProvider.apiKey)}" autocomplete="new-password" ${busy ? "disabled" : ""}>
          </label>
          <label class="setup-wizard-quick-wide">
            <span>${escapeHtml(t("setupWizard.quickProvider.modelLabel"))}</span>
            <input type="text" data-setup-qp-field="model" value="${escapeAttr(quickProvider.model)}" placeholder="${escapeAttr(t("setupWizard.quickProvider.modelPlaceholder"))}" autocomplete="off" spellcheck="false" ${busy ? "disabled" : ""}>
          </label>
        </div>
        ${quickProviderNoticeCard()}
        <span class="setup-wizard-quick-actions">
          <button class="settings-action-btn primary" type="submit" data-setup-qp-save ${busy ? "disabled" : ""}>${escapeHtml(busy === "save" ? t("setupWizard.quickProvider.saving") : t("setupWizard.quickProvider.save"))}</button>
          <button class="ghost-btn" type="button" data-setup-qp-test ${busy ? "disabled" : ""}>${escapeHtml(busy === "test" ? t("setupWizard.quickProvider.testing") : t("setupWizard.quickProvider.test"))}</button>
          <button class="ghost-btn" type="button" data-setup-open-providers>${escapeHtml(t("setupWizard.quickProvider.openFull"))}</button>
        </span>
      </form>
    `;
  }

  function renderModelGroups(visibleModels) {
    return groupSetupModels(visibleModels).map((group) => `
      <div class="setup-wizard-model-group-label">${escapeHtml(group.provider)}${group.type ? ` · ${escapeHtml(group.type)}` : ""} · ${escapeHtml(t("setupWizard.model.groupModels", { count: group.models.length }))}</div>
      ${group.models.map((item) => {
        const checked = item.value === selectedModel;
        return `
          <button class="setup-wizard-model ${checked ? "selected" : ""}" type="button" role="radio" aria-checked="${checked ? "true" : "false"}" tabindex="${checked ? "0" : "-1"}" data-setup-model="${escapeAttr(item.value)}">
            <span class="setup-wizard-model-indicator" aria-hidden="true">${checked ? "✓" : ""}</span>
            <span class="setup-wizard-model-copy"><strong>${escapeHtml(item.model)}</strong><small>${escapeHtml(item.provider)}${item.type ? ` · ${escapeHtml(item.type)}` : ""}</small></span>
            <span class="settings-status-pill ok">${escapeHtml(checked ? t("setupWizard.model.selected") : t("setupWizard.model.usable"))}</span>
          </button>
        `;
      }).join("")}
    `).join("");
  }

  function renderModel() {
    const availableModels = models();
    if (!availableModels.length) {
      return `
        <section class="setup-wizard-step">
          <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.model.eyebrow"))}</span>
          <h2>${escapeHtml(t("setupWizard.model.title"))}</h2>
          <p>${escapeHtml(t("setupWizard.model.description"))}</p>
          <div class="setup-wizard-empty setup-wizard-empty-with-form">
            <span class="setup-wizard-empty-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><rect x="7" y="7" width="10" height="10" rx="2"></rect><path d="M9.5 1.5v3M14.5 1.5v3M9.5 19.5v3M14.5 19.5v3M1.5 9.5h3M1.5 14.5h3M19.5 9.5h3M19.5 14.5h3"></path><circle cx="12" cy="12" r="2.25"></circle></svg></span>
            <strong>${escapeHtml(t("setupWizard.model.noModels"))}</strong>
            <span>${escapeHtml(t(remoteAccessContext(state) ? "setupWizard.model.noModelsRemote" : "setupWizard.model.noModelsDescription"))}</span>
            <button class="ghost-btn setup-wizard-inline-refresh" type="button" data-setup-refresh>${escapeHtml(t("setupWizard.model.refreshModels"))}</button>
          </div>
          ${renderCodexLoginCard()}
          ${renderQuickProviderForm()}
        </section>
      `;
    }
    const visibleModels = filterSetupModels(availableModels, modelFilter);
    // Roving tabindex needs at least one stop even when the selection is
    // filtered out of view.
    const selectionVisible = visibleModels.some((item) => item.value === selectedModel);
    const list = visibleModels.length
      ? renderModelGroups(visibleModels)
      : `<div class="setup-wizard-state-card">${escapeHtml(t("setupWizard.model.filterEmpty", { filter: modelFilter }))}</div>`;
    return `
      <section class="setup-wizard-step">
        <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.model.eyebrow"))}</span>
        <h2>${escapeHtml(t("setupWizard.model.title"))}</h2>
        <p>${escapeHtml(t("setupWizard.model.description"))}</p>
        ${availableModels.length > 6 ? `
          <input class="setup-wizard-model-filter" type="search" data-setup-model-filter value="${escapeAttr(modelFilter)}"
            placeholder="${escapeAttr(t("setupWizard.model.searchPlaceholder"))}" aria-label="${escapeAttr(t("setupWizard.model.searchPlaceholder"))}"
            autocomplete="off" spellcheck="false">
        ` : ""}
        <div class="setup-wizard-models" role="radiogroup" aria-label="${escapeAttr(t("setupWizard.model.title"))}" ${selectionVisible ? "" : 'data-setup-selection-hidden="true"'}>
          ${list}
        </div>
        ${selectedModel ? "" : `<div class="setup-wizard-state-card error" role="alert">${escapeHtml(t("setupWizard.model.required"))}</div>`}
        ${renderCodexLoginCard()}
        ${quickProvider.open ? renderQuickProviderForm() : `
          <span class="setup-wizard-quick-actions">
            <button class="ghost-btn" type="button" data-setup-qp-toggle>${escapeHtml(t("setupWizard.model.quickAddToggle"))}</button>
          </span>
        `}
      </section>
    `;
  }

  function renderVerifyCard() {
    if (verify.status === "running") {
      return `<div class="setup-wizard-state-card" role="status">${escapeHtml(t("setupWizard.complete.verifying"))}</div>`;
    }
    if (verify.status === "ok") {
      return `<div class="setup-wizard-state-card ok" role="status">${escapeHtml(t("setupWizard.complete.verifyOk"))}</div>`;
    }
    if (verify.status === "fail") {
      return `
        <div class="setup-wizard-state-card error" role="alert">
          ${escapeHtml(t("setupWizard.complete.verifyFail", { message: verify.message || "—" }))}
          <button class="setup-wizard-inline-action" type="button" data-setup-verify>${escapeHtml(t("setupWizard.actions.verify"))}</button>
        </div>
      `;
    }
    return "";
  }

  function renderComplete() {
    const gitReady = Boolean(setupToolById(setupStatus, "git")?.available);
    return `
      <section class="setup-wizard-step setup-wizard-complete">
        <span class="setup-wizard-complete-icon" aria-hidden="true">✓</span>
        <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.complete.eyebrow"))}</span>
        <h2>${escapeHtml(t("setupWizard.complete.title"))}</h2>
        <p>${escapeHtml(t("setupWizard.complete.description"))}</p>
        <div class="setup-wizard-summary-list">
          <div><span aria-hidden="true">✓</span><strong>${escapeHtml(t("setupWizard.complete.environmentReady"))}</strong></div>
          <div class="${gitReady ? "" : "warn"}"><span aria-hidden="true">${gitReady ? "✓" : "!"}</span><strong>${escapeHtml(t(gitReady ? "setupWizard.complete.gitReady" : "setupWizard.complete.gitLimited"))}</strong></div>
          <div><span aria-hidden="true">✓</span><strong>${escapeHtml(t("setupWizard.complete.modelReady", { model: selectedModel }))}</strong></div>
        </div>
        ${renderVerifyCard()}
      </section>
    `;
  }

  function nextAllowed() {
    const step = currentStepID();
    if (step === "environment") return setupEnvironmentReady(setupStatus);
    if (step === "model") return models().some((item) => item.value === selectedModel);
    if (step === "complete") return setupCanFinish({ status: setupStatus, catalog: state?.modelCatalog, selectedModel });
    return true;
  }

  function updateActions() {
    const back = $("setupWizardBackBtn");
    const next = $("setupWizardNextBtn");
    const refresh = $("setupWizardRefreshBtn");
    const close = $("setupWizardCloseBtn");
    const skip = $("setupWizardSkipBtn");
    if (back) {
      back.classList.toggle("hidden", activeStep === 0);
      back.disabled = finishing;
      back.textContent = t("setupWizard.actions.back");
    }
    if (next) {
      next.disabled = finishing || !nextAllowed();
      if (!next.hasAttribute("aria-busy")) next.textContent = currentStepID() === "complete" ? t("setupWizard.actions.finish") : t("setupWizard.actions.next");
    }
    if (refresh) {
      const show = ["environment", "model"].includes(currentStepID());
      refresh.classList.toggle("hidden", !show);
      refresh.disabled = Boolean(refreshInFlight) || finishing;
      if (!refresh.hasAttribute("aria-busy")) refresh.textContent = t("setupWizard.actions.refresh");
    }
    if (close) {
      close.classList.toggle("hidden", automaticFlow && !wizardDismissible());
      close.setAttribute("aria-label", t("setupWizard.close"));
    }
    if (skip) {
      skip.classList.toggle("hidden", !(automaticFlow && wizardDismissible()));
      skip.disabled = finishing;
      skip.textContent = t("setupWizard.actions.later");
    }
  }

  function restorePendingFocus() {
    if (!pendingFocusSelector) return;
    const selector = pendingFocusSelector;
    pendingFocusSelector = "";
    globalThis.queueMicrotask?.(() => {
      const modal = $("setupWizardModal");
      if (!modal || modal.classList.contains("hidden")) return;
      if (selector === "@model-selected") {
        [...modal.querySelectorAll("[data-setup-model]")]
          .find((node) => node.dataset.setupModel === selectedModel)
          ?.focus?.();
        return;
      }
      const node = modal.querySelector(selector);
      if (!node) return;
      node.focus?.();
      if (typeof node.setSelectionRange === "function" && typeof node.value === "string") {
        try {
          node.setSelectionRange(node.value.length, node.value.length);
        } catch {}
      }
    });
  }

  function renderSetupWizard() {
    const body = $("setupWizardBody");
    if (!body) return;
    const renderers = [renderWelcome, renderEnvironment, renderModel, renderComplete];
    body.innerHTML = (renderers[activeStep] || renderWelcome)();
    const title = $("setupWizardTitle");
    const subtitle = $("setupWizardSubtitle");
    if (title) title.textContent = t("setupWizard.title");
    if (subtitle) subtitle.textContent = t("setupWizard.subtitle");
    renderProgress();
    updateActions();
    restorePendingFocus();
    if (currentStepID() === "complete") maybeVerifySelectedModel();
    syncBackgroundPolling();
  }

  async function refreshSetupWizard({ reloadStatus = true, reloadModels = true, forceStatus = false, skipRenderIfUnchanged = false } = {}) {
    if (refreshInFlight) return refreshInFlight;
    const button = $("setupWizardRefreshBtn");
    setButtonBusy(button, true, t("setupWizard.actions.refreshing"));
    refreshInFlight = (async () => {
      const before = skipRenderIfUnchanged ? JSON.stringify([setupStatus, models(), selectedModel]) : "";
      const environmentPromise = reloadStatus
        ? Promise.resolve(loadSetupStatus?.({ force: forceStatus }))
          .then((value) => ({ ok: true, value }))
          .catch((error) => ({ ok: false, error }))
        : Promise.resolve({ ok: true, value: setupStatus });
      const modelRefreshes = reloadModels
        ? [Promise.resolve(loadSettings?.()), Promise.resolve(loadModelCatalog?.())]
        : [];
      const [environmentResult] = await Promise.all([
        environmentPromise,
        Promise.allSettled(modelRefreshes),
      ]);
      setupStatus = environmentResult.ok
        ? normalizeSetupStatus({ ...environmentResult.value, loaded: true })
        : normalizeSetupStatus({ loaded: true, error: environmentResult.error?.message || t("setupWizard.errors.loadStatus") });
      selectedModel = setupSelectedModel(models(), selectedModel || preferredModel());
      if (skipRenderIfUnchanged && before === JSON.stringify([setupStatus, models(), selectedModel])) return setupStatus;
      renderSetupWizard();
      return setupStatus;
    })().finally(() => {
      refreshInFlight = null;
      setButtonBusy(button, false, t("setupWizard.actions.refreshing"));
      updateActions();
      syncBackgroundPolling();
    });
    return refreshInFlight;
  }

  function syncBackgroundPolling() {
    const pollInstalls = wizardOpen && anyInstallRunning();
    if (pollInstalls && installPollTimer == null) {
      installPollTimer = globalThis.setTimeout?.(() => {
        installPollTimer = null;
        pollInstallJobs().catch(() => {});
      }, setupInstallPollInterval) ?? null;
    } else if (!pollInstalls && installPollTimer != null) {
      globalThis.clearTimeout?.(installPollTimer);
      installPollTimer = null;
    }

    const pollEnvironment = wizardOpen && currentStepID() === "environment" && environmentHasGaps() && !anyInstallRunning();
    if (pollEnvironment && environmentPollTimer == null) {
      environmentPollTimer = globalThis.setTimeout?.(() => {
        environmentPollTimer = null;
        refreshSetupWizard({ reloadModels: false, forceStatus: true, skipRenderIfUnchanged: true }).catch(() => {}).finally(() => syncBackgroundPolling());
      }, setupEnvironmentPollInterval) ?? null;
    } else if (!pollEnvironment && environmentPollTimer != null) {
      globalThis.clearTimeout?.(environmentPollTimer);
      environmentPollTimer = null;
    }
  }

  async function pollInstallJobs() {
    if (!wizardOpen) return;
    try {
      const response = await request?.("/api/setup/install/status");
      const jobs = Array.isArray(response?.jobs) ? response.jobs : [];
      const previous = installJobs;
      installJobs = Object.fromEntries(jobs.map((job) => [String(job?.tool || ""), job]));
      let refreshNeeded = false;
      for (const job of jobs) {
        const before = previous[job.tool];
        if (before?.status !== "running" || job.status === "running") continue;
        if (job.status === "succeeded") {
          showToast?.(t("setupWizard.environment.installSucceeded", { tool: setupToolLabel(job.tool) }), "success", { force: true });
          refreshNeeded = true;
        } else {
          showToast?.(t("setupWizard.environment.installFailed", { tool: setupToolLabel(job.tool) }), "error", { force: true });
        }
      }
      if (refreshNeeded) {
        await refreshSetupWizard({ reloadModels: false, forceStatus: true });
      } else if (currentStepID() === "environment") {
        renderSetupWizard();
      }
    } finally {
      syncBackgroundPolling();
    }
  }

  async function startToolInstall(toolID) {
    if (anyInstallRunning()) return;
    try {
      const response = await request?.("/api/setup/install", {
        method: "POST",
        body: JSON.stringify({ tool: toolID }),
      });
      if (response?.job?.tool) installJobs = { ...installJobs, [response.job.tool]: response.job };
    } catch (error) {
      showToast?.(error?.message || String(error), "error", { force: true });
    }
    if (currentStepID() === "environment") renderSetupWizard();
    else syncBackgroundPolling();
  }

  function maybeVerifySelectedModel() {
    if (!selectedModel) return;
    if (verify.model === selectedModel && verify.status !== "idle") return;
    verifySelectedModel().catch(() => {});
  }

  async function verifySelectedModel() {
    const model = selectedModel;
    const providerName = model.split(":")[0] || "";
    if (!model || !providerName || !request) return;
    verify = { status: "running", model, message: "" };
    if (currentStepID() === "complete") renderSetupWizard();
    let next;
    try {
      const response = await request(`/api/providers/${encodeURIComponent(providerName)}/test`, { method: "POST" });
      const ok = Boolean(response?.reachable) && response?.configured !== false;
      next = { status: ok ? "ok" : "fail", model, message: String(response?.message || "") };
    } catch (error) {
      next = { status: "fail", model, message: error?.message || String(error) };
    }
    if (verify.model !== model || verify.status !== "running") return;
    verify = next;
    if (currentStepID() === "complete") renderSetupWizard();
  }

  function quickProviderIssueText(issues) {
    const key = issues[0];
    return t(`setupWizard.quickProvider.issues.${key}`);
  }

  function quickProviderDraft() {
    return {
      name: quickProvider.name.trim(),
      type: quickProvider.type,
      baseUrl: quickProvider.baseUrl.trim(),
      apiKey: quickProvider.apiKey,
      model: quickProvider.model.trim(),
    };
  }

  function preopenCodexLoginWindow() {
    if (isDesktopShell()) return null;
    try {
      const popup = globalThis.open?.("about:blank", "autoto-codex-login", "popup,width=720,height=820");
      if (popup) popup.opener = null;
      return popup || null;
    } catch {
      return null;
    }
  }

  async function openCodexLoginAuthURL(authUrl, popup = null) {
    if (!trustedCodexBrowserAuthURL(authUrl)) throw new Error(t("setupWizard.codexLogin.invalidURL"));
    try {
      if (popup && !popup.closed) {
        popup.location.replace(authUrl);
        return true;
      }
      return await openExternal(authUrl);
    } catch {
      return false;
    }
  }

  async function finishCodexSetupLogin(status, seq) {
    const terminal = normalizeCodexBrowserLoginStatus(status);
    if (seq !== codexLogin.seq) return;
    Object.assign(codexLogin, terminal, { seq, popupBlocked: false });
    if (terminal.status === "completed") {
      const accountID = String(terminal.account?.id || "").trim();
      if (accountID) {
        try {
          const requestSpec = codexAccountActionRequest("sync", accountID);
          await request?.(requestSpec.path, requestSpec.options);
        } catch {}
      }
      await refreshSetupWizard({ reloadStatus: false });
      const codexModel = firstSetupCodexModel(models());
      if (codexModel) {
        selectedModel = codexModel.value;
        showToast?.(t("setupWizard.codexLogin.success"), "success", { force: true });
      } else {
        showToast?.(t("setupWizard.codexLogin.modelsPending"), "error", { force: true });
      }
    }
    if (currentStepID() === "model") renderSetupWizard();
    else updateActions();
  }

  async function pollCodexSetupLogin(loginId, seq) {
    for (;;) {
      await new Promise((resolve) => globalThis.setTimeout(resolve, 1000));
      if (seq !== codexLogin.seq || codexLogin.loginId !== loginId) return;
      const requestSpec = codexBrowserLoginRequest("status", loginId);
      let status;
      try {
        status = normalizeCodexBrowserLoginStatus(await request?.(requestSpec.path, requestSpec.options));
      } catch (error) {
        if (seq !== codexLogin.seq) return;
        await finishCodexSetupLogin({ loginId, status: "failed", message: error?.message || t("setupWizard.codexLogin.unknown") }, seq);
        return;
      }
      if (status.loginId && status.loginId !== loginId) return;
      Object.assign(codexLogin, status, { loginId, seq, authUrl: status.authUrl || codexLogin.authUrl });
      if (currentStepID() === "model") renderSetupWizard();
      if (setupCodexLoginActive(status.status)) continue;
      await finishCodexSetupLogin(status, seq);
      return;
    }
  }

  async function startCodexSetupLogin() {
    if (remoteAccessContext(state) || finishing) return;
    if (setupCodexLoginActive(codexLogin.status) && codexLogin.authUrl) {
      try {
        if (!await openCodexLoginAuthURL(codexLogin.authUrl)) {
          codexLogin.popupBlocked = true;
          renderSetupWizard();
        }
      } catch (error) {
        Object.assign(codexLogin, { status: "failed", message: error?.message || t("setupWizard.codexLogin.invalidURL"), popupBlocked: false });
        renderSetupWizard();
      }
      return;
    }
    const popup = preopenCodexLoginWindow();
    const seq = Number(codexLogin.seq || 0) + 1;
    Object.assign(codexLogin, {
      seq,
      loginId: "",
      status: "starting",
      authUrl: "",
      message: "",
      account: null,
      popupBlocked: false,
    });
    renderSetupWizard();
    try {
      const requestSpec = codexBrowserLoginRequest("start");
      const status = normalizeCodexBrowserLoginStatus(await request?.(requestSpec.path, requestSpec.options));
      if (seq !== codexLogin.seq) {
        popup?.close?.();
        if (status.loginId) {
          const cancelSpec = codexBrowserLoginRequest("cancel", status.loginId);
          Promise.resolve(request?.(cancelSpec.path, cancelSpec.options)).catch(() => {});
        }
        return;
      }
      if (!status.loginId) throw new Error(t("setupWizard.codexLogin.startFailed"));
      const active = setupCodexLoginActive(status.status);
      if (active && !trustedCodexBrowserAuthURL(status.authUrl)) throw new Error(t("setupWizard.codexLogin.invalidURL"));
      const opened = active ? await openCodexLoginAuthURL(status.authUrl, popup) : true;
      if (!active) popup?.close?.();
      Object.assign(codexLogin, status, {
        seq,
        loginId: status.loginId,
        status: status.status || "pending",
        popupBlocked: active && !opened,
      });
      renderSetupWizard();
      if (!setupCodexLoginActive(codexLogin.status)) {
        await finishCodexSetupLogin(codexLogin, seq);
        return;
      }
      await pollCodexSetupLogin(codexLogin.loginId, seq);
    } catch (error) {
      popup?.close?.();
      if (seq !== codexLogin.seq) return;
      Object.assign(codexLogin, {
        status: "failed",
        message: error?.status === 403 ? t("setupWizard.codexLogin.localOnly") : (error?.message || t("setupWizard.codexLogin.unknown")),
        popupBlocked: false,
      });
      renderSetupWizard();
    }
  }

  async function cancelCodexSetupLogin() {
    if (!codexLogin.loginId || !setupCodexLoginActive(codexLogin.status)) return;
    const seq = Number(codexLogin.seq || 0) + 1;
    const loginId = codexLogin.loginId;
    codexLogin.seq = seq;
    try {
      const requestSpec = codexBrowserLoginRequest("cancel", loginId);
      const status = normalizeCodexBrowserLoginStatus(await request?.(requestSpec.path, requestSpec.options));
      Object.assign(codexLogin, status, { seq, status: status.status || "cancelled", popupBlocked: false });
    } catch (error) {
      Object.assign(codexLogin, { seq, status: "failed", message: error?.message || t("setupWizard.codexLogin.unknown"), popupBlocked: false });
    }
    renderSetupWizard();
  }

  async function reopenCodexSetupLogin() {
    if (!codexLogin.authUrl || !setupCodexLoginActive(codexLogin.status)) return;
    try {
      if (!await openCodexLoginAuthURL(codexLogin.authUrl)) {
        codexLogin.popupBlocked = true;
        renderSetupWizard();
      }
    } catch (error) {
      Object.assign(codexLogin, { status: "failed", message: error?.message || t("setupWizard.codexLogin.invalidURL"), popupBlocked: false });
      renderSetupWizard();
    }
  }

  async function discoverQuickProviderModels(draft) {
    const response = await request?.("/api/providers/test", {
      method: "POST",
      body: JSON.stringify({
        name: draft.name,
        type: draft.type,
        baseUrl: draft.baseUrl,
        apiKey: draft.apiKey,
        model: draft.model,
        createOnly: true,
      }),
    });
    const models = Array.isArray(response?.models) ? response.models.map(String).filter(Boolean) : [];
    return { response, models };
  }

  async function testQuickProvider() {
    const draft = quickProviderDraft();
    const issues = setupQuickProviderIssues(draft);
    if (issues.length) {
      quickProvider.notice = { kind: "error", text: quickProviderIssueText(issues) };
      renderSetupWizard();
      return;
    }
    quickProvider.busy = "test";
    quickProvider.notice = null;
    renderSetupWizard();
    try {
      const { response, models: discovered } = await discoverQuickProviderModels(draft);
      quickProvider.discoveredModels = discovered;
      if (!quickProvider.model && discovered[0]) quickProvider.model = discovered[0];
      if (setupQuickProviderTestSucceeded(response)) {
        quickProvider.notice = {
          kind: "ok",
          text: t("setupWizard.quickProvider.testOk", { count: discovered.length || Number(response?.modelCount) || 0 }),
        };
      } else {
        quickProvider.notice = { kind: "error", text: String(response?.message || t("setupWizard.quickProvider.testFail")) };
      }
    } catch (error) {
      quickProvider.notice = { kind: "error", text: error?.message || String(error) };
    } finally {
      quickProvider.busy = "";
      renderSetupWizard();
    }
  }

  async function saveQuickProvider() {
    const draft = quickProviderDraft();
    const issues = setupQuickProviderIssues(draft);
    if (issues.length) {
      quickProvider.notice = { kind: "error", text: quickProviderIssueText(issues) };
      renderSetupWizard();
      return;
    }
    quickProvider.busy = "save";
    quickProvider.notice = null;
    renderSetupWizard();
    try {
      let discovered = quickProvider.discoveredModels;
      if (!draft.model && !discovered.length) {
        const probed = await discoverQuickProviderModels(draft);
        discovered = probed.models;
        quickProvider.discoveredModels = discovered;
        if (!setupQuickProviderTestSucceeded(probed.response)) {
          quickProvider.busy = "";
          quickProvider.notice = { kind: "error", text: String(probed.response?.message || t("setupWizard.quickProvider.testFail")) };
          renderSetupWizard();
          return;
        }
      }
      const model = setupQuickProviderResolvedModel(draft, discovered);
      if (model && !quickProvider.model) quickProvider.model = model;
      if (!model) {
        quickProvider.busy = "";
        quickProvider.notice = { kind: "error", text: t("setupWizard.quickProvider.noModels") };
        renderSetupWizard();
        return;
      }
      await request?.(`/api/providers/${encodeURIComponent(draft.name)}/config`, {
        method: "PUT",
        body: JSON.stringify({
          name: draft.name,
          type: draft.type,
          baseUrl: draft.baseUrl,
          apiKey: draft.apiKey,
          model,
          createOnly: true,
        }),
      });
      await request?.(`/api/providers/${encodeURIComponent(draft.name)}`, {
        method: "PATCH",
        body: JSON.stringify({ enabled: true }),
      });
      quickProvider.apiKey = "";
      quickProvider.busy = "";
      await refreshSetupWizard({ reloadStatus: false });
      const created = models().find((item) => item.provider === draft.name);
      if (created) {
        selectedModel = created.value;
        quickProvider.open = false;
        quickProvider.notice = null;
        showToast?.(t("setupWizard.quickProvider.created", { name: draft.name }), "success", { force: true });
      } else {
        quickProvider.notice = { kind: "error", text: t("setupWizard.quickProvider.createdNoModels", { name: draft.name }) };
      }
      renderSetupWizard();
    } catch (error) {
      quickProvider.busy = "";
      quickProvider.notice = { kind: "error", text: error?.message || String(error) };
      renderSetupWizard();
    }
  }

  async function finishSetupWizard() {
    if (finishing || !setupCanFinish({ status: setupStatus, catalog: state?.modelCatalog, selectedModel })) return false;
    finishing = true;
    const next = $("setupWizardNextBtn");
    setButtonBusy(next, true, t("setupWizard.actions.saving"));
    updateActions();
    try {
      await completeSetup?.(selectedModel, setupWizardVersion);
      if (preferencesPending?.()) throw new Error(t("setupWizard.errors.pendingSave"));
      renderModelOptions?.();
      automaticFlow = false;
      closeSetupWizard({ force: true });
      showToast?.(t("setupWizard.completedToast", { model: selectedModel }), "success", { force: true });
      return true;
    } catch (error) {
      showToast?.(error?.message || String(error), "error", { force: true });
      return false;
    } finally {
      finishing = false;
      setButtonBusy(next, false, t("setupWizard.actions.saving"));
      updateActions();
    }
  }

  async function openSetupWizard({ automatic = false, reason = "", startStep = "welcome", reloadStatus = true, reloadModels = true } = {}) {
    automaticFlow = Boolean(automatic);
    openReason = reason || (automatic ? "automatic" : "manual");
    suspendedForProviderSettings = false;
    activeStep = setupStepIndex(startStep);
    selectedModel = setupSelectedModel(models(), preferredModel());
    verify = { status: "idle", model: "", message: "" };
    wizardOpen = true;
    $("setupWizardModal")?.classList.remove("hidden");
    setWizardIsolation(true);
    renderSetupWizard();
    focusSetupWizard();
    if (reloadStatus || reloadModels) await refreshSetupWizard({ reloadStatus, reloadModels });
    return true;
  }

  async function resumeAfterProviderSettings() {
    if (!suspendedForProviderSettings) return false;
    suspendedForProviderSettings = false;
    await openSetupWizard({ automatic: true, reason: openReason, startStep: "model" });
    return true;
  }

  async function maybeOpenSetupWizard() {
    const remote = remoteAccessContext(state);
    if (remote) return false;
    const decision = setupWizardStartupDecision({
      setupVersion: getSetupVersion?.(),
      preferredModel: preferredModel(),
      catalog: state?.modelCatalog,
      remote,
    });
    if (!decision.open) {
      try {
        setupStatus = normalizeSetupStatus({ ...(await loadSetupStatus?.()), loaded: true });
        if (setupEnvironmentReady(setupStatus)) return false;
      } catch (error) {
        setupStatus = normalizeSetupStatus({ loaded: true, error: error?.message || t("setupWizard.errors.loadStatus") });
      }
      await openSetupWizard({ automatic: true, reason: "environment", startStep: "environment", reloadStatus: false, reloadModels: false });
      return true;
    }
    await openSetupWizard({ automatic: true, reason: decision.reason, startStep: decision.step, reloadModels: false });
    return true;
  }

  async function copyInstallCommand(toolID) {
    const tool = setupToolById(setupStatus, toolID);
    if (!tool?.installCommand) return false;
    let copied = false;
    try {
      copied = Boolean(await copyText?.(tool.installCommand));
    } catch {}
    if (!copied && globalThis.navigator?.clipboard?.writeText) {
      try {
        await globalThis.navigator.clipboard.writeText(tool.installCommand);
        copied = true;
      } catch {}
    }
    if (copied) showToast?.(t("setupWizard.environment.installCopied", { tool: setupToolLabel(tool.id) }), "success");
    return copied;
  }

  function bindSetupWizardActions() {
    $("setupWizardCloseBtn")?.addEventListener("click", () => closeSetupWizard());
    $("setupWizardSkipBtn")?.addEventListener("click", () => closeSetupWizard());
    $("setupWizardBackBtn")?.addEventListener("click", () => {
      if (activeStep <= 0 || finishing) return;
      activeStep--;
      renderSetupWizard();
    });
    $("setupWizardNextBtn")?.addEventListener("click", () => {
      if (!nextAllowed() || finishing) return;
      if (currentStepID() === "complete") {
        finishSetupWizard().catch((error) => showToast?.(error?.message || String(error), "error", { force: true }));
        return;
      }
      activeStep = Math.min(setupWizardStepIds.length - 1, activeStep + 1);
      renderSetupWizard();
    });
    $("setupWizardRefreshBtn")?.addEventListener("click", () => refreshSetupWizard({ forceStatus: true }).catch((error) => showToast?.(error?.message || String(error), "error", { force: true })));
    $("setupWizardProgress")?.addEventListener("click", (event) => {
      const dot = event.target?.closest?.("[data-setup-step-jump]");
      if (!dot || finishing) return;
      const index = Number(dot.dataset.setupStepJump);
      if (!Number.isInteger(index) || index >= activeStep) return;
      activeStep = index;
      renderSetupWizard();
    });
    $("setupWizardBody")?.addEventListener("click", (event) => {
      const modelButton = event.target?.closest?.("[data-setup-model]");
      if (modelButton) {
        selectedModel = String(modelButton.dataset.setupModel || "").trim();
        pendingFocusSelector = "@model-selected";
        renderSetupWizard();
        return;
      }
      if (event.target?.closest?.("[data-setup-open-providers]")) {
        suspendedForProviderSettings = automaticFlow;
        $("setupWizardModal")?.classList.add("hidden");
        wizardOpen = false;
        syncBackgroundPolling();
        if (suspendedForProviderSettings) setWizardIsolation(true, { allowSettings: true });
        else {
          automaticFlow = false;
          setWizardIsolation(false, { restoreFocus: false });
        }
        openSettingsModal?.("providers");
        return;
      }
      if (event.target?.closest?.("[data-setup-refresh]")) {
        refreshSetupWizard({ forceStatus: true }).catch((error) => showToast?.(error?.message || String(error), "error", { force: true }));
        return;
      }
      if (event.target?.closest?.("[data-setup-qp-toggle]")) {
        quickProvider.open = true;
        renderSetupWizard();
        return;
      }
      if (event.target?.closest?.("[data-setup-qp-test]")) {
        testQuickProvider().catch(() => {});
        return;
      }
      if (event.target?.closest?.("[data-setup-codex-login]")) {
        startCodexSetupLogin().catch(() => {});
        return;
      }
      if (event.target?.closest?.("[data-setup-codex-cancel]")) {
        cancelCodexSetupLogin().catch(() => {});
        return;
      }
      if (event.target?.closest?.("[data-setup-codex-reopen]")) {
        reopenCodexSetupLogin().catch(() => {});
        return;
      }
      if (event.target?.closest?.("[data-setup-verify]")) {
        verifySelectedModel().catch(() => {});
        return;
      }
      const installButton = event.target?.closest?.("[data-setup-install]");
      if (installButton) {
        startToolInstall(installButton.dataset.setupInstall).catch(() => {});
        return;
      }
      const copyButton = event.target?.closest?.("[data-setup-copy-install]");
      if (copyButton) copyInstallCommand(copyButton.dataset.setupCopyInstall).catch(() => {});
    });
    $("setupWizardBody")?.addEventListener("input", (event) => {
      const filterInput = event.target?.closest?.("[data-setup-model-filter]");
      if (filterInput) {
        modelFilter = String(filterInput.value || "");
        pendingFocusSelector = "[data-setup-model-filter]";
        renderSetupWizard();
        return;
      }
      const field = event.target?.closest?.("[data-setup-qp-field]");
      if (field) {
        const key = String(field.dataset.setupQpField || "");
        if (key in quickProvider) quickProvider[key] = String(field.value || "");
        if (key === "apiKey" || key === "baseUrl" || key === "type") quickProvider.discoveredModels = [];
        if (key === "type") {
          pendingFocusSelector = '[data-setup-qp-field="type"]';
          renderSetupWizard();
        }
      }
    });
    $("setupWizardBody")?.addEventListener("submit", (event) => {
      if (!event.target?.closest?.("[data-setup-qp-form]")) return;
      event.preventDefault();
      saveQuickProvider().catch(() => {});
    });
    $("setupWizardModal")?.addEventListener("click", (event) => {
      if (event.target?.id === "setupWizardModal") closeSetupWizard();
    });
    $("setupWizardModal")?.addEventListener("keydown", handleSetupWizardKeydown);
  }

  return {
    bindSetupWizardActions,
    closeSetupWizard,
    finishSetupWizard,
    maybeOpenSetupWizard,
    openSetupWizard,
    refreshSetupWizard,
    renderSetupWizard,
    resumeAfterProviderSettings,
  };
}
