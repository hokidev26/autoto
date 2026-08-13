import { accountPreferencesCurrentSetupVersion } from "./account-preferences.mjs";
import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";

export const setupWizardVersion = accountPreferencesCurrentSetupVersion;
export const setupWizardStepIds = Object.freeze(["welcome", "environment", "model", "complete"]);

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
} = {}) {
  const models = discoverSetupModels(catalog);
  const preferred = String(preferredModel || "").trim();
  const completed = Math.max(0, Number(setupVersion) || 0) >= currentVersion;
  const preferredReady = Boolean(preferred) && models.some((item) => item.value === preferred);
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
  let suspendedForProviderSettings = false;
  let previousFocus = null;
  let refreshInFlight = null;
  let finishing = false;

  function models() {
    return discoverSetupModels(state?.modelCatalog);
  }

  function currentStepID() {
    return setupWizardStepIds[activeStep] || setupWizardStepIds[0];
  }

  function preferredModel() {
    return String(getPreferredModel?.() || "").trim();
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

  function closeSetupWizard({ force = false } = {}) {
    if (automaticFlow && !force) return false;
    $("setupWizardModal")?.classList.add("hidden");
    setWizardIsolation(false);
    automaticFlow = false;
    suspendedForProviderSettings = false;
    return true;
  }

  function renderProgress() {
    const progress = $("setupWizardProgress");
    if (progress) {
      progress.innerHTML = setupWizardStepIds.map((step, index) => `
        <span class="setup-wizard-progress-dot ${index < activeStep ? "complete" : ""} ${index === activeStep ? "active" : ""}" aria-hidden="true"></span>
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

  function renderTool(tool) {
    const importance = toolImportance(tool);
    const statusLabel = tool.available
      ? tool.version || t("setupWizard.environment.available")
      : tool.recommended && !tool.required
        ? t("setupWizard.environment.limited")
        : t("setupWizard.environment.missing");
    const action = !tool.available && tool.installCommand
      ? `<button class="setup-wizard-inline-action" type="button" data-setup-copy-install="${escapeAttr(tool.id)}">${escapeHtml(t("setupWizard.actions.copyInstall"))}</button>`
      : "";
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
          ${action}
        </span>
      </article>
    `;
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
      : t("setupWizard.environment.packageManagerMissing");
    const blocked = !setupEnvironmentReady(normalized)
      ? `<div class="setup-wizard-state-card error" role="alert">${escapeHtml(t("setupWizard.environment.blocked"))}</div>`
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
      </section>
    `;
  }

  function renderModel() {
    const availableModels = models();
    if (!availableModels.length) {
      return `
        <section class="setup-wizard-step">
          <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.model.eyebrow"))}</span>
          <h2>${escapeHtml(t("setupWizard.model.title"))}</h2>
          <p>${escapeHtml(t("setupWizard.model.description"))}</p>
          <div class="setup-wizard-empty">
            <span class="setup-wizard-empty-icon" aria-hidden="true">◇</span>
            <strong>${escapeHtml(t("setupWizard.model.noModels"))}</strong>
            <span>${escapeHtml(t("setupWizard.model.noModelsDescription"))}</span>
            <div class="setup-wizard-empty-actions">
              <button class="settings-action-btn primary" type="button" data-setup-open-providers>${escapeHtml(t("setupWizard.model.openProviders"))}</button>
              <button class="ghost-btn" type="button" data-setup-refresh>${escapeHtml(t("setupWizard.model.refreshModels"))}</button>
            </div>
          </div>
        </section>
      `;
    }
    return `
      <section class="setup-wizard-step">
        <span class="setup-wizard-eyebrow">${escapeHtml(t("setupWizard.model.eyebrow"))}</span>
        <h2>${escapeHtml(t("setupWizard.model.title"))}</h2>
        <p>${escapeHtml(t("setupWizard.model.description"))}</p>
        <div class="setup-wizard-models" role="radiogroup" aria-label="${escapeAttr(t("setupWizard.model.title"))}">
          ${availableModels.map((item) => {
            const checked = item.value === selectedModel;
            return `
              <button class="setup-wizard-model ${checked ? "selected" : ""}" type="button" role="radio" aria-checked="${checked ? "true" : "false"}" data-setup-model="${escapeAttr(item.value)}">
                <span class="setup-wizard-model-indicator" aria-hidden="true">${checked ? "✓" : ""}</span>
                <span class="setup-wizard-model-copy"><strong>${escapeHtml(item.model)}</strong><small>${escapeHtml(item.provider)}${item.type ? ` · ${escapeHtml(item.type)}` : ""}</small></span>
                <span class="settings-status-pill ok">${escapeHtml(checked ? t("setupWizard.model.selected") : t("setupWizard.model.usable"))}</span>
              </button>
            `;
          }).join("")}
        </div>
        ${selectedModel ? "" : `<div class="setup-wizard-state-card error" role="alert">${escapeHtml(t("setupWizard.model.required"))}</div>`}
      </section>
    `;
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
      close.classList.toggle("hidden", automaticFlow);
      close.setAttribute("aria-label", t("setupWizard.close"));
    }
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
  }

  async function refreshSetupWizard({ reloadStatus = true, reloadModels = true, forceStatus = false } = {}) {
    if (refreshInFlight) return refreshInFlight;
    const button = $("setupWizardRefreshBtn");
    setButtonBusy(button, true, t("setupWizard.actions.refreshing"));
    refreshInFlight = (async () => {
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
      renderSetupWizard();
      return setupStatus;
    })().finally(() => {
      refreshInFlight = null;
      setButtonBusy(button, false, t("setupWizard.actions.refreshing"));
      updateActions();
    });
    return refreshInFlight;
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

  async function openSetupWizard({ automatic = false, startStep = "welcome", reloadStatus = true, reloadModels = true } = {}) {
    automaticFlow = Boolean(automatic);
    suspendedForProviderSettings = false;
    activeStep = setupStepIndex(startStep);
    selectedModel = setupSelectedModel(models(), preferredModel());
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
    await openSetupWizard({ automatic: true, startStep: "model" });
    return true;
  }

  async function maybeOpenSetupWizard() {
    const decision = setupWizardStartupDecision({
      setupVersion: getSetupVersion?.(),
      preferredModel: preferredModel(),
      catalog: state?.modelCatalog,
    });
    if (!decision.open) {
      try {
        setupStatus = normalizeSetupStatus({ ...(await loadSetupStatus?.()), loaded: true });
        if (setupEnvironmentReady(setupStatus)) return false;
      } catch (error) {
        setupStatus = normalizeSetupStatus({ loaded: true, error: error?.message || t("setupWizard.errors.loadStatus") });
      }
      await openSetupWizard({ automatic: true, startStep: "environment", reloadStatus: false, reloadModels: false });
      return true;
    }
    await openSetupWizard({ automatic: true, startStep: decision.step, reloadModels: false });
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
    $("setupWizardBody")?.addEventListener("click", (event) => {
      const modelButton = event.target?.closest?.("[data-setup-model]");
      if (modelButton) {
        selectedModel = String(modelButton.dataset.setupModel || "").trim();
        renderSetupWizard();
        return;
      }
      if (event.target?.closest?.("[data-setup-open-providers]")) {
        suspendedForProviderSettings = automaticFlow;
        $("setupWizardModal")?.classList.add("hidden");
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
      const installButton = event.target?.closest?.("[data-setup-copy-install]");
      if (installButton) copyInstallCommand(installButton.dataset.setupCopyInstall).catch(() => {});
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
