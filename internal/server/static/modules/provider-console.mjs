import { $, escapeAttr, escapeHtml, setButtonBusy, setHTMLIfChanged } from "./dom.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs";
import {
  createProviderDraft,
  isAnthropicAccountProvider,
  isBuiltinProvider,
  isProviderDeletable,
  modelProvidersForUIUnion,
  normalizeConsoleProvider,
  normalizeProviderModelConfigs,
  applyProviderUserAgentPresetToForm,
  providerConfigPayload,
  providerConsoleRequest,
  providerModelDraftUsable,
  providerVisibilityPreferencesForDraft,
  removeProviderVisibilityPreferences,
  renderProviderConsolePage,
  setProviderModelHidden,
  setProviderModelHiddenAll,
} from "./model-provider-components.mjs";
import {
  automaticProviderNameUpdate,
  markProviderModelsStale,
  normalizeAnthropicAccountList,
  normalizeCodexAccountList,
  normalizeCodexSelectedIds,
  providerConsoleDraftFromForm,
  providerConsoleFocusableElements,
  providerModelDiscovery,
  providerPreflightResult,
  providerSensitiveDraftAccessAllowed,
  redactedProviderProxyURL,
  restoreProviderConsoleFocus,
  selectProviderConsoleFieldOnFocus,
  shouldOpenProviderCardFromKeyboard,
  syncProviderConsoleDraft,
  trapProviderConsoleFocus,
  validateProviderNameValue,
} from "./provider-settings-normalization.mjs";
import { codexAccountStableID, finiteNumber, providerAccountQuotaSummary } from "./provider-account-rendering.mjs";
import { providerCategory, subscriptionProviderKind, subscriptionProviderKinds, subscriptionProviderSpec } from "./model-provider-components.mjs";
import { normalizeSubscriptionProvider } from "./provider-settings-normalization.mjs";
import { createCodexAuthController } from "./provider-codex-auth.mjs";
import { createAnthropicAccountsController } from "./provider-anthropic-accounts.mjs";
import { createSubscriptionAccountsController } from "./provider-subscription-accounts.mjs";
import { createModelRoutingController } from "./model-routing-settings.mjs";

export const providerConsoleSuccessBannerMs = 3600;

export function createModelProviderSettingsController({
  state,
  copyText,
  getModelVisibilityPreference,
  getPreferredModelPreference,
  loadModelCatalog,
  loadSettings,
  notifyTerminal,
  openSettingsModal,
  refreshActiveSettingsPanel,
  requestAPI = api,
  setModelVisibilityPreference,
  setPreferredModelPreference,
  showError,
  updateWorkspaceMetaPills,
} = {}) {
  let preferredModelFallback = "";
  let modelVisibilityFallback = { hiddenModels: {}, showUnconfiguredProviders: false };
  let providerQuotaDeferredHandle = null;
  let providerQuotaDeferredScheduled = false;
  const mt = (key, params) => t(`modelProvider.${key}`, params);
  const ct = (key, params) => t(`modelProvider.console.${key}`, params);

  function setModelRefreshButtonsBusy(busy) {
    ["refreshModelsBtn", "settingsRefreshModelsBtn", "providerRefreshModelsBtn"].forEach((id) => {
      setButtonBusy($(id), busy, mt("refreshing"));
    });
  }

  function setModelApplyButtonsBusy(busy) {
    setButtonBusy($("settingsClearPreferredModelBtn"), busy, mt("processing"));
    document.querySelectorAll("[data-apply-model]").forEach((button) => {
      button.disabled = busy;
      if (busy) button.setAttribute("aria-busy", "true");
      else button.removeAttribute("aria-busy");
    });
  }

  async function refreshModelCatalog() {
    if (state.modelRefreshing) return;
    state.modelRefreshing = true;
    setModelRefreshButtonsBusy(true);
    try {
      await loadModelCatalog();
      await reloadLoadedOfficialAccountOverview();
      notifyTerminal?.(`[info] ${mt("modelListRefreshed")}\n`);
    } finally {
      state.modelRefreshing = false;
      setModelRefreshButtonsBusy(false);
    }
  }

  // Shared provider-console primitives, handed to the codex/anthropic/model-routing
  // sub-controllers via explicit dependency injection (no module-level singleton).
  const ctx = {
    state,
    requestAPI,
    notifyTerminal,
    refreshActiveSettingsPanel,
    loadModelCatalog,
    loadSettings,
    showError,
    openSettingsModal,
    refreshProviderConsole,
    providerConsoleState,
    setProviderConsoleResult,
    providerByName,
    modelProvidersForUI,
    providerModelList,
    providerRuntimeSelectable,
    providerLabel,
    providerStatusText,
    codexProvider,
    refreshModelCatalog,
    applyPreferredModel,
    clearVisibleConfiguredModelHides,
    isModelHidden,
    setModelHidden,
    renderModelOptions,
    modelOptionValue,
  };

  const codexAuth = createCodexAuthController(ctx);
  const {
    loadProviderAuthFiles,
    setCodexImportFiles,
    importCodexAuthFile,
    startCodexBrowserLogin,
    cancelCodexBrowserLogin,
    reopenCodexBrowserLogin,
    saveCodexAccount,
    syncCodexAccount,
    resetCodexQuota,
    toggleCodexAccount,
    exportCodexAccount,
    deleteCodexAccount,
    runCodexBatchOperation,
    renderCodexConsolePage,
  } = codexAuth;

  const anthropicAccounts = createAnthropicAccountsController(ctx);
  const {
    loadAnthropicAccounts,
    anthropicAccountById,
    createAnthropicAccount,
    saveAnthropicAccount,
    syncAnthropicAccount,
    toggleAnthropicAccount,
    deleteAnthropicAccount,
    startAnthropicLogin,
    reopenAnthropicLogin,
    submitAnthropicLogin,
    pasteAnthropicLogin,
    cancelAnthropicLogin,
    renderAnthropicConsolePage,
  } = anthropicAccounts;

  const subscriptionAccounts = createSubscriptionAccountsController({ ...ctx, isModelHidden, modelOptionValue });
  const {
    ensureSubscriptionState,
    accountsFor,
    accountsLoaded,
    loadSubscriptionAccounts,
    refreshSubscriptionAccounts,
    subscriptionAccountById,
    subscriptionPage,
    saveSubscriptionAccount,
    syncSubscriptionAccount,
    toggleSubscriptionAccount,
    deleteSubscriptionAccount,
    startSubscriptionLogin,
    cancelSubscriptionLogin,
    reopenSubscriptionLogin,
    copySubscriptionDeviceCode,
    submitKiroLogin,
    handleKiroInputChange,
    handleKiroSubmitClick,
    renderGeminiConsolePage,
    renderGrokConsolePage,
    renderKimiConsolePage,
    renderKiroConsolePage,
  } = subscriptionAccounts;

  const modelRouting = createModelRoutingController(ctx);
  const { renderModelSettingsContent, bindModelSettingsActions } = modelRouting;


  function relayProtocolSpec(key) {
    return relayProtocolSpecs().find((item) => item.key === key) || relayProtocolSpecs().find((item) => item.key === "completions");
  }

  function relayProtocolSpecs() {
    return [
      { key: "anthropic", label: mt("relayProtocols.anthropic.label"), providerName: "anthropic", providerType: "anthropic", help: mt("relayProtocols.anthropic.help") },
      { key: "codex", label: "Codex Relay", providerName: "cliproxyapi", providerType: "openai-compatible", providerProfile: "cliproxyapi", help: mt("relayProtocols.responses.help") },
      { key: "responses", label: mt("relayProtocols.responses.label"), providerName: "openai", providerType: "openai", help: mt("relayProtocols.responses.help") },
      { key: "gemini-interactions", label: "Gemini Interactions", providerName: "gemini", providerType: "gemini-interactions", help: "Gemini Interactions API" },
      { key: "claude-code", label: mt("relayProtocols.claudeCode.label"), providerName: "anthropic", providerType: "anthropic", help: mt("relayProtocols.claudeCode.help") },
      { key: "completions", label: mt("relayProtocols.completions.label"), providerName: "openai-compatible", providerType: "openai-compatible", help: mt("relayProtocols.completions.help") },
    ];
  }

  function providerConfigExpanded(key) {
    return Boolean(state.providerConfigExpanded?.[key]);
  }

  function renderProviderConfigToggle(key, expanded, label = mt("config")) {
    const buttonLabel = expanded ? mt("collapse") : mt("expand", { label });
    return `<button class="settings-action-btn subtle" type="button" data-toggle-provider-config="${escapeAttr(key)}" aria-expanded="${expanded ? "true" : "false"}">${escapeHtml(buttonLabel)}</button>`;
  }

  function toggleProviderConfig(key) {
    state.providerConfigExpanded = { ...(state.providerConfigExpanded || {}), [key]: !providerConfigExpanded(key) };
    refreshActiveSettingsPanel?.();
  }

  function expandProviderConfig(key) {
    if (providerConfigExpanded(key)) return;
    state.providerConfigExpanded = { ...(state.providerConfigExpanded || {}), [key]: true };
    refreshActiveSettingsPanel?.();
  }

  function providerByName(name) {
    return modelProvidersForUI().find((provider) => provider.name === name)
      || (state.settings?.providers || []).find((provider) => provider.name === name)
      || null;
  }

  function renderModelProviderSection(provider) {
    const models = providerModelList(provider);
    return `
    <section class="agent-model-catalog-provider settings-provider-section settings-card">
      <header class="settings-provider-section-head settings-card-header">
        <div>
          <h2 class="settings-provider-title settings-card-title">${escapeHtml(providerLabel(provider))}</h2>
          <div class="settings-provider-meta settings-card-description">${escapeHtml(provider.baseUrl || provider.type || "provider")}</div>
        </div>
        <span class="settings-status-pill settings-badge ${provider.error ? "warn" : (provider.configured ? "ok" : "muted")}">${escapeHtml(providerStatusText(provider))}</span>
      </header>
      <div class="settings-card-content">
      ${provider.error ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(provider.error)}</div>` : ""}
      <div class="agent-model-catalog-grid settings-model-grid">
        ${models.map((model) => renderModelChoice(provider, model)).join("")}
      </div>
      </div>
    </section>
  `;
  }

  function renderModelChoice(provider, model) {
    const value = modelOptionValue(provider, model);
    const active = value === currentModelValue();
    const preferred = value === getPreferredModel();
    const hidden = isModelHidden(value);
    const selectable = isModelSelectable(provider, model);
    const disabled = !provider.configured;
    const title = hidden ? mt("showModel") : mt("hideModel");
    const icon = hidden
      ? `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m3 3 18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.2A10.8 10.8 0 0 1 12 4c5.2 0 9.2 4 10.5 8a11.7 11.7 0 0 1-3.1 4.8M6.2 6.2A11.8 11.8 0 0 0 1.5 12c1.3 4 5.3 8 10.5 8 1.3 0 2.5-.2 3.6-.7"/></svg>`
      : `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.8-7 10-7 10 7 10 7-3.8 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/></svg>`;
    const status = disabled ? mt("unconfigured") : hidden ? mt("hidden") : preferred ? mt("preferred") : active ? mt("currentModel") : "";
    return `
    <div class="agent-model-catalog-item settings-model-row settings-card ${active ? "active" : ""} ${hidden || disabled ? "muted" : ""}">
      <button class="settings-model-choice ${active ? "active" : ""}" type="button" data-apply-model="${escapeAttr(value)}" title="${escapeAttr(value)}" ${selectable ? "" : "disabled"}>
        <span class="settings-model-name">${escapeHtml(value)}</span>
      </button>
      <div class="settings-inline-actions">${status ? `<span class="settings-badge ${disabled ? "muted" : hidden ? "warn" : "ok"}">${escapeHtml(status)}</span>` : ""}<button class="settings-model-icon-btn ${hidden ? "is-hidden" : "is-visible"}" type="button" data-toggle-model-visibility="${escapeAttr(value)}" data-model-visibility-state="${hidden ? "hidden" : "visible"}" title="${escapeAttr(title)}" aria-label="${escapeAttr(title)}" aria-pressed="${hidden ? "false" : "true"}" ${disabled ? "disabled" : ""}>${icon}</button></div>
    </div>
  `;
  }

  function providerConsoleState() {
    const fallback = {
      view: "providers",
      search: "",
      category: "all",
      modal: "",
      drawer: "",
      mode: "",
      type: "",
      providerName: "",
      draft: null,
      dirty: false,
      nameTouched: false,
      nameError: "",
      busy: {},
      result: null,
      testOpen: false,
      test: { prompt: "", result: null },
      codexImportDraft: "",
      codexImportFiles: [],
      codexImportResult: null,
      codexSelectedIds: [],
      codexBatchBusy: false,
      codexBatchPriority: 100,
      codexEdit: null,
      codexBrowserLogin: {
        seq: 0,
        loginId: "",
        status: "idle",
        authUrl: "",
        expiresAt: "",
        message: "",
        account: null,
        popupBlocked: false,
      },
      anthropicAddMode: "profile",
      anthropicProfile: "",
      anthropicAlias: "",
      anthropicPriority: 100,
      anthropicEdit: null,
    };
    if (!state.providerConsole || typeof state.providerConsole !== "object") {
      state.providerConsole = fallback;
    } else {
      // Preserve object identity so async work that retained this state keeps writing
      // to the live console state after a render-triggered normalization.
      const consoleState = state.providerConsole;
      const previous = { ...consoleState };
      const testState = previous.test && typeof previous.test === "object" ? previous.test : {};
      const previousTest = { ...testState };
      const browserLoginState = previous.codexBrowserLogin && typeof previous.codexBrowserLogin === "object"
        ? previous.codexBrowserLogin
        : {};
      const previousBrowserLogin = { ...browserLoginState };
      Object.assign(testState, fallback.test, previousTest);
      Object.assign(browserLoginState, fallback.codexBrowserLogin, previousBrowserLogin);
      Object.assign(consoleState, fallback, previous, {
        busy: previous.busy || {},
        test: testState,
        codexImportFiles: Array.isArray(previous.codexImportFiles) ? previous.codexImportFiles : [],
        codexSelectedIds: Array.isArray(previous.codexSelectedIds) ? previous.codexSelectedIds : [],
        codexBrowserLogin: browserLoginState,
      });
    }
    return state.providerConsole;
  }

  let providerResultClearTimer = 0;

  function setProviderConsoleResult(message, tone = "info") {
    if (providerResultClearTimer) {
      globalThis.clearTimeout?.(providerResultClearTimer);
      providerResultClearTimer = 0;
    }
    const text = String(message || "");
    providerConsoleState().result = text ? { message: text, tone } : null;
    if (!text || tone !== "success" || typeof globalThis.setTimeout !== "function") return;
    providerResultClearTimer = globalThis.setTimeout(() => {
      providerResultClearTimer = 0;
      if (providerConsoleState().result?.tone !== "success") return;
      providerConsoleState().result = null;
      refreshProviderConsole();
    }, providerConsoleSuccessBannerMs);
  }

  function requireProviderSensitiveDraftAccess() {
    if (providerSensitiveDraftAccessAllowed(state)) return true;
    const message = ct("messages.sensitiveAccessRequiresFullSession");
    setProviderConsoleResult(message, "attention");
    notifyTerminal?.(`[warn] ${message}\n`);
    refreshProviderConsole();
    return false;
  }

  function configuredOfficialAccountKinds() {
    const kinds = new Set();
    for (const provider of modelProvidersForUI()) {
      if (providerCategory(provider) !== "official") continue;
      if (provider.type === "codex" || provider.name === "codex") kinds.add("codex");
      else if (isAnthropicAccountProvider(provider)) kinds.add("anthropic");
      else {
        const kind = subscriptionProviderKind(provider);
        if (kind) kinds.add(kind);
      }
    }
    return kinds;
  }

  function officialAccountKindsForCurrentList() {
    const consoleState = providerConsoleState();
    if (consoleState.view !== "providers" || consoleState.category === "custom") return new Set();
    return configuredOfficialAccountKinds();
  }

  function officialProviderAccountSummaries() {
    ensureSubscriptionState();
    const codexLoaded = state.providerAuthLoaded === true || (state.providerAuthFiles !== null && state.providerAuthFiles !== undefined);
    const anthropicLoaded = state.anthropicAccountsLoaded === true || Object.hasOwn(state, "anthropicAccounts");
    const summaries = {
      codex: providerAccountQuotaSummary("codex", normalizeCodexAccountList(state.providerAuthFiles), {
        loaded: codexLoaded,
        loading: Boolean(state.providerAuthLoading),
        error: state.providerAuthError,
      }),
      anthropic: providerAccountQuotaSummary("anthropic", normalizeAnthropicAccountList(state.anthropicAccounts), {
        loaded: anthropicLoaded,
        loading: Boolean(state.anthropicAccountsLoading),
        error: state.anthropicAccountsError,
      }),
    };
    for (const provider of subscriptionProviderKinds) {
      summaries[provider] = providerAccountQuotaSummary(provider, accountsFor(provider), {
        loaded: accountsLoaded(provider),
        loading: Boolean(state.subscriptionAccountsLoading?.[provider]),
        error: state.subscriptionAccountsError?.[provider],
      });
    }
    return summaries;
  }

  function officialAccountKindNeedsLoad(kind) {
    if (kind === "codex") {
      const loaded = state.providerAuthLoaded === true || (state.providerAuthFiles !== null && state.providerAuthFiles !== undefined);
      return !loaded && !state.providerAuthLoading && !state.providerAuthError;
    }
    if (kind === "anthropic") {
      const loaded = state.anthropicAccountsLoaded === true || Object.hasOwn(state, "anthropicAccounts");
      return !loaded && !state.anthropicAccountsLoading && !state.anthropicAccountsError;
    }
    ensureSubscriptionState();
    return !accountsLoaded(kind) && !state.subscriptionAccountsLoading[kind] && !state.subscriptionAccountsError[kind];
  }

  function loadOfficialAccountKind(kind) {
    if (!officialAccountKindNeedsLoad(kind)) return Promise.resolve(false);
    if (kind === "codex") return loadProviderAuthFiles({ silent: true });
    if (kind === "anthropic") return loadAnthropicAccounts({ silent: true });
    return loadSubscriptionAccounts(kind, { silent: true });
  }

  function scheduleDeferredOfficialAccountLoads(kinds) {
    const deferredKinds = [...kinds].filter((kind) => !["codex", "gemini"].includes(kind) && officialAccountKindNeedsLoad(kind));
    if (!deferredKinds.length || providerQuotaDeferredScheduled) return;
    providerQuotaDeferredScheduled = true;
    const run = () => {
      providerQuotaDeferredScheduled = false;
      providerQuotaDeferredHandle = null;
      if (providerConsoleState().view !== "providers") return;
      Promise.allSettled(deferredKinds.map(loadOfficialAccountKind));
    };
    if (typeof globalThis.requestIdleCallback === "function") {
      providerQuotaDeferredHandle = globalThis.requestIdleCallback(run, { timeout: 800 });
    } else {
      providerQuotaDeferredHandle = globalThis.setTimeout(run, 120);
    }
  }

  async function loadOfficialProviderAccountOverview({ defer = true } = {}) {
    const kinds = officialAccountKindsForCurrentList();
    if (!kinds.size) return [];
    const immediateKinds = ["codex", "gemini"].filter((kind) => kinds.has(kind));
    const jobs = immediateKinds.filter(officialAccountKindNeedsLoad).map(loadOfficialAccountKind);
    if (defer) scheduleDeferredOfficialAccountLoads(kinds);
    else jobs.push(...[...kinds].filter((kind) => !immediateKinds.includes(kind) && officialAccountKindNeedsLoad(kind)).map(loadOfficialAccountKind));
    return Promise.allSettled(jobs);
  }

  async function reloadLoadedOfficialAccountOverview() {
    ensureSubscriptionState();
    const kinds = configuredOfficialAccountKinds();
    const jobs = [];
    const codexLoaded = state.providerAuthLoaded === true || (state.providerAuthFiles !== null && state.providerAuthFiles !== undefined);
    const anthropicLoaded = state.anthropicAccountsLoaded === true || Object.hasOwn(state, "anthropicAccounts");
    if (kinds.has("codex") && codexLoaded) jobs.push(loadProviderAuthFiles({ silent: true }));
    if (kinds.has("anthropic") && anthropicLoaded) jobs.push(loadAnthropicAccounts({ silent: true }));
    for (const provider of subscriptionProviderKinds) {
      if (kinds.has(provider) && accountsLoaded(provider)) jobs.push(loadSubscriptionAccounts(provider, { silent: true }));
    }
    return Promise.allSettled(jobs);
  }

  function renderProviderSettingsContent() {
    const consoleState = providerConsoleState();
    if (consoleState.view === "codex") return renderCodexConsolePage();
    if (consoleState.view === "anthropic") return renderAnthropicConsolePage();
    if (consoleState.view === "gemini") return renderGeminiConsolePage();
    if (consoleState.view === "grok") return renderGrokConsolePage();
    if (consoleState.view === "kimi") return renderKimiConsolePage();
    if (consoleState.view === "kiro") return renderKiroConsolePage();
    return renderProviderConsolePage({
      providers: modelProvidersForUI(),
      accountSummaries: officialProviderAccountSummaries(),
      consoleState: {
        ...consoleState,
        sensitiveAccessAllowed: providerSensitiveDraftAccessAllowed(state),
      },
    });
  }


  function renderCustomProviderConfigCard() {
    const expanded = providerConfigExpanded("custom-provider");
    return `
    <section class="settings-provider-section custom-provider-section ${expanded ? "expanded" : "collapsed"}">
      <div class="settings-provider-section-head">
        <div>
          <div class="settings-provider-title">${escapeHtml(mt("customProviderTitle"))}</div>
          <div class="settings-provider-meta" data-settings-help-copy>${escapeHtml(mt("customProviderDescription"))}</div>
        </div>
        <div class="settings-action-row compact-actions">
          <button id="fillGroqProviderExampleBtn" class="settings-action-btn subtle" type="button">${escapeHtml(mt("fillGroqExample"))}</button>
          ${renderProviderConfigToggle("custom-provider", expanded, mt("customConfig"))}
        </div>
      </div>
      ${expanded ? `
        <form id="customProviderConfigForm" class="settings-provider-config-form custom-provider-config-form settings-collapsible-body">
          <div class="settings-provider-form-grid">
            <label>${escapeHtml(mt("providerNamePrefix"))}
              <input id="customProviderName" class="settings-field" name="name" value="" placeholder="groq" autocomplete="off" />
            </label>
            <label>${escapeHtml(mt("protocol"))}
              <select id="customProviderType" class="settings-field" name="type">
                ${renderProviderTypeOptions("openai-compatible")}
              </select>
            </label>
            <label class="settings-form-span-2">${escapeHtml(mt("baseUrl"))}
              <input id="customProviderBaseUrl" class="settings-field" name="baseUrl" value="" placeholder="https://api.groq.com/openai/v1" autocomplete="off" />
            </label>
            <label class="settings-form-span-2">${escapeHtml(mt("apiKey"))}
              <input id="customProviderApiKey" class="settings-field" name="apiKey" type="password" autocomplete="off" placeholder="${escapeAttr(mt("apiKeyUserPlaceholder"))}" />
            </label>
            <label>${escapeHtml(mt("defaultModel"))}
              <input id="customProviderModel" class="settings-field" name="model" data-select-on-focus="true" value="" placeholder="openai/gpt-oss-20b" autocomplete="off" />
            </label>
            <label>${escapeHtml(mt("maxTokens"))}
              <input class="settings-field" name="maxTokens" data-select-on-focus="true" type="number" min="0" step="1" value="" placeholder="${escapeAttr(mt("maxTokensOptional"))}" />
            </label>
            <label class="settings-checkbox-field settings-form-span-2">
              <input name="apiKeyOptional" type="checkbox" />
              <span>${escapeHtml(mt("apiKeyOptional"))}</span>
            </label>
          </div>
          <div class="settings-provider-actions">
            <button class="settings-action-btn primary" type="submit" data-provider-save>${escapeHtml(mt("saveCustomProvider"))}</button>
          </div>
          <div class="settings-provider-note">${escapeHtml(mt("groqNote"))}</div>
        </form>
      ` : ""}
    </section>
  `;
  }

  function extractAuthFiles(value) {
    return normalizeCodexAccountList(value);
  }

  function renderProviderCard(provider) {
    const setting = settingProviderByName(provider.name) || {};
    const type = provider.type || setting.type || "openai-compatible";
    const baseUrl = provider.baseUrl || setting.baseUrl || "";
    const model = provider.defaultModel || setting.model || "";
    const maxTokens = provider.maxTokens || setting.maxTokens || 0;
    const models = providerModelList(provider);
    const apiKeyOptional = Boolean(provider.apiKeyOptional || setting.apiKeyOptional);
    const envExample = providerEnvExample({ ...provider, type, baseUrl, defaultModel: model });
    const expanded = providerConfigExpanded(`provider:${provider.name}`);
    return `
    <section class="settings-provider-card ${expanded ? "expanded" : "collapsed"}">
      <div class="settings-provider-card-head">
        <div>
          <div class="settings-provider-title">${escapeHtml(providerLabel(provider))}</div>
          <div class="settings-provider-meta">${escapeHtml(type)} · ${escapeHtml(providerCapabilitiesLabel(provider))} · ${escapeHtml(mt("modelCount", { count: models.length }))}</div>
        </div>
        <div class="settings-action-row compact-actions">
          <span class="settings-status-pill ${provider.error ? "warn" : (provider.configured ? "ok" : "muted")}">${escapeHtml(providerStatusText(provider))}</span>
          ${renderProviderConfigToggle(`provider:${provider.name}`, expanded, mt("config"))}
        </div>
      </div>
      <div class="settings-provider-meta path">${escapeHtml(baseUrl || mt("defaultOfficialEndpoint"))}</div>
      ${provider.configured ? `<div class="settings-inline-success compact">${escapeHtml(mt("configuredVisible"))}</div>` : `<div class="settings-inline-alert compact">${escapeHtml(mt("unconfiguredHidden"))}</div>`}
      ${provider.error ? `<div class="settings-inline-alert compact">${escapeHtml(provider.error)}</div>` : ""}
      ${expanded ? `
        <form class="settings-provider-config-form settings-collapsible-body" data-provider-name="${escapeAttr(provider.name)}">
          <div class="settings-provider-form-grid">
            <label>${escapeHtml(mt("protocol"))}
              <select class="settings-field" name="type">
                ${renderProviderTypeOptions(type)}
              </select>
            </label>
            <label>${escapeHtml(mt("defaultModel"))}
              <input class="settings-field" name="model" data-select-on-focus="true" value="${escapeAttr(model)}" placeholder="${escapeAttr(mt("defaultModelPlaceholder"))}" />
            </label>
            <label class="settings-form-span-2">${escapeHtml(mt("baseUrl"))}
              <input class="settings-field" name="baseUrl" value="${escapeAttr(baseUrl)}" placeholder="${escapeAttr(providerBaseURLPlaceholder(type, provider.profile))}" />
            </label>
            <label class="settings-form-span-2">${escapeHtml(mt("apiKey"))}
              <input class="settings-field" name="apiKey" type="password" value="" autocomplete="off" placeholder="${escapeAttr(provider.configured ? mt("apiKeyPreservePlaceholder") : mt("apiKeyPastePlaceholder"))}" />
              ${provider.apiKeyPersisted ? `<small data-settings-help-copy>${escapeHtml(mt("apiKeyPersisted", { lastFive: provider.apiKeyLastFive }))}</small>` : ""}
            </label>
            ${provider.apiKeyPersisted ? `<label class="settings-checkbox-field"><input name="clearApiKey" type="checkbox" data-provider-clear-api-key /> <span>${escapeHtml(mt("clearApiKey"))}</span></label>` : ""}
            <label>${escapeHtml(mt("maxTokens"))}
              <input class="settings-field" name="maxTokens" data-select-on-focus="true" type="number" min="0" step="1" value="${escapeAttr(maxTokens || "")}" placeholder="${escapeAttr(mt("maxTokensAnthropic"))}" />
            </label>
            <label class="settings-checkbox-field">
              <input name="apiKeyOptional" type="checkbox" ${apiKeyOptional ? "checked" : ""} />
              <span>${escapeHtml(mt("apiKeyOptional"))}</span>
            </label>
          </div>
          <div class="settings-provider-actions">
            <button class="settings-action-btn primary" type="submit" data-provider-save>${escapeHtml(mt("saveConfig"))}</button>
            <button class="settings-action-btn subtle" type="button" data-copy-text="${escapeAttr(envExample)}">${escapeHtml(mt("copyEnvExample"))}</button>
          </div>
          <div class="settings-provider-note">${escapeHtml(mt("providerConfigNote"))}</div>
        </form>
      ` : ""}
    </section>
  `;
  }

  function renderProviderTypeOptions(selected) {
    return [
      { value: "codex", label: mt("typeCodexOAuth") },
      { value: "openai-compatible", label: mt("typeOpenAICompatible") },
      { value: "openai", label: mt("typeOpenAIOfficial") },
      { value: "anthropic", label: mt("typeAnthropicOfficial") },
      { value: "gemini-interactions", label: "Gemini Interactions" },
    ].map((item) => `<option value="${escapeAttr(item.value)}" ${item.value === selected ? "selected" : ""}>${escapeHtml(item.label)}</option>`).join("");
  }

  function providerBaseURLPlaceholder(type, profile) {
    if (type === "codex") return "https://chatgpt.com/backend-api/codex";
    if (profile === "cliproxyapi") return "http://127.0.0.1:8317/v1";
    if (type === "openai-compatible") return "https://api.example.com/v1";
    if (type === "anthropic") return mt("anthropicOfficialEndpointPlaceholder");
    if (type === "gemini-interactions") return "https://generativelanguage.googleapis.com/v1beta/interactions";
    return mt("openaiOfficialEndpointPlaceholder");
  }

  function providerEnvExample(provider) {
    const model = provider.defaultModel || provider.model || "your-model";
    const baseURL = provider.baseUrl || providerBaseURLPlaceholder(provider.type, provider.profile);
    const rowsByProvider = {
      openai: [`export OPENAI_API_KEY="sk-..."`, `export OPENAI_MODEL="${model}"`],
      anthropic: [`export ANTHROPIC_API_KEY="sk-ant-..."`, `export ANTHROPIC_MODEL="${model}"`],
      "gemini-interactions": [`export GEMINI_API_KEY="..."`, `export GEMINI_MODEL="${model}"`, `export GEMINI_BASE_URL="${baseURL}"`],
      gemini: [`export GEMINI_API_KEY="..."`, `export GEMINI_MODEL="${model}"`, `export GEMINI_BASE_URL="${baseURL}"`],
      groq: [`export GROQ_API_KEY="gsk_..."`, `export GROQ_MODEL="${model}"`],
      cliproxyapi: [`export CLIPROXYAPI_BASE_URL="${baseURL}"`, `export CLIPROXYAPI_MODEL="${model}"`, `# ${mt("cliproxyEnvComment")}`, `export CLIPROXYAPI_API_KEY="..."`],
      "openai-compatible": [`export OPENAI_COMPATIBLE_BASE_URL="${baseURL}"`, `export OPENAI_COMPATIBLE_API_KEY="sk-..."`, `export OPENAI_COMPATIBLE_MODEL="${model}"`],
    };
    return (rowsByProvider[provider.profile] || rowsByProvider[provider.name] || rowsByProvider[provider.type] || rowsByProvider["openai-compatible"]).join("\n");
  }

  function fillGroqProviderExample() {
    if (!providerConfigExpanded("custom-provider")) {
      expandProviderConfig("custom-provider");
    }
    const form = $("customProviderConfigForm");
    if (!form) return;
    form.elements.name.value = "groq";
    form.elements.type.value = "openai-compatible";
    form.elements.baseUrl.value = "https://api.groq.com/openai/v1";
    form.elements.model.value = "openai/gpt-oss-20b";
    for (const name of ["name", "baseUrl", "model"]) {
      form.elements[name]?.setAttribute?.("data-select-on-focus", "true");
    }
    form.elements.maxTokens.value = "";
    form.elements.apiKeyOptional.checked = false;
    form.elements.apiKey.value = "";
    form.elements.apiKey.focus();
  }

  async function saveProviderConfig(event) {
    event.preventDefault();
    const form = event.currentTarget;
    if (form.dataset.submitting === "true") return;
    const providerName = String(form.dataset.providerName || form.elements.name?.value || "").trim();
    const saveButton = form.querySelector("[data-provider-save]");
    const maxTokens = Number(form.elements.maxTokens?.value || 0);
    if (!providerName) throw new Error(mt("selectProviderName"));
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(providerName)) throw new Error(mt("invalidProviderName"));
    const payload = {
      name: providerName,
      type: form.elements.type?.value || "openai-compatible",
      baseUrl: form.elements.baseUrl?.value.trim() || "",
      apiKey: form.elements.clearApiKey?.checked ? "" : (form.elements.apiKey?.value.trim() || ""),
      ...(form.elements.clearApiKey?.checked ? { clearApiKey: true } : {}),
      model: form.elements.model?.value.trim() || "",
      maxTokens: Number.isFinite(maxTokens) ? maxTokens : 0,
      apiKeyOptional: Boolean(form.elements.apiKeyOptional?.checked),
    };
    if (!payload.model) throw new Error(mt("selectDefaultModel"));
    if (payload.type === "openai-compatible" && !payload.baseUrl) throw new Error(mt("missingBaseUrl"));
    form.dataset.submitting = "true";
    setButtonBusy(saveButton, true, mt("saving"));
    try {
      const response = await requestAPI(`/api/providers/${encodeURIComponent(providerName)}/config`, { method: "PUT", body: JSON.stringify(payload) });
      if (form.elements.apiKey) form.elements.apiKey.value = "";
      if (form.elements.clearApiKey) form.elements.clearApiKey.checked = false;
      state.providerConfigStatus = response.message || mt("providerConfigSaved");
      await loadSettings();
      await loadModelCatalog();
      renderModelOptions();
      refreshActiveSettingsPanel?.();
      notifyTerminal?.(`[info] ${mt("providerConfigSavedTerminal", { provider: providerLabel({ name: providerName }), message: response.message || mt("effective") })}\n`);
    } finally {
      delete form.dataset.submitting;
      setButtonBusy(saveButton, false, mt("saving"));
    }
  }

  let providerConsoleEventRoot = null;
  let providerConsoleFocusReturn = null;

  function scheduleProviderConsoleFocus(callback) {
    const schedule = globalThis.queueMicrotask || ((work) => Promise.resolve().then(work));
    schedule(callback);
  }

  function providerConsoleLayer() {
    return providerConsoleEventRoot?.querySelector?.('[role="dialog"][aria-modal="true"]') || null;
  }

  function focusProviderConsoleLayer() {
    const layer = providerConsoleLayer();
    if (!layer) return;
    const [first] = providerConsoleFocusableElements(layer);
    (first || layer).focus?.();
  }

  function rememberProviderConsoleFocus(trigger) {
    const card = trigger?.closest?.("[data-mp-provider-card]");
    providerConsoleFocusReturn = {
      node: trigger || null,
      cardName: card?.dataset?.mpProviderCard || "",
      opensTypes: Boolean(trigger?.closest?.("[data-mp-open-types]")),
    };
  }

  function resolveProviderConsoleFocusReturn() {
    const saved = providerConsoleFocusReturn;
    if (!saved) return null;
    if (saved.node?.isConnected !== false) return saved.node;
    if (!providerConsoleEventRoot) return null;
    if (saved.cardName) {
      return [...providerConsoleEventRoot.querySelectorAll("[data-mp-provider-card]")]
        .find((node) => node.dataset?.mpProviderCard === saved.cardName) || null;
    }
    if (saved.opensTypes) return providerConsoleEventRoot.querySelector("[data-mp-open-types]");
    return null;
  }

  function restoreProviderConsoleLayerFocus() {
    const target = resolveProviderConsoleFocusReturn();
    providerConsoleFocusReturn = null;
    if (target) scheduleProviderConsoleFocus(() => restoreProviderConsoleFocus(target));
  }

  function focusCodexConsolePage() {
    const page = providerConsoleEventRoot?.querySelector?.(".codex-account-console");
    const backButton = page?.querySelector?.("[data-mp-close-codex-page]");
    (backButton || page)?.focus?.();
  }

  function focusAnthropicConsolePage() {
    const page = providerConsoleEventRoot?.querySelector?.(".anthropic-account-console");
    const backButton = page?.querySelector?.("[data-mp-close-anthropic-page]");
    (backButton || page)?.focus?.();
  }

  function focusSubscriptionConsolePage(provider) {
    const page = providerConsoleEventRoot?.querySelector?.(`.subscription-account-console[data-subscription-view="${provider}"]`);
    const backButton = page?.querySelector?.("[data-mp-close-subscription-page]");
    (backButton || page)?.focus?.();
  }

  function focusProviderCreatePage() {
    const page = providerConsoleEventRoot?.querySelector?.(".mp-provider-create-page");
    const backButton = page?.querySelector?.("[data-mp-close-drawer]");
    (backButton || page)?.focus?.();
  }

  function focusProviderTestDialog() {
    const dialog = providerConsoleEventRoot?.querySelector?.('[role="dialog"][aria-modal="true"].mp-provider-test-dialog');
    const prompt = dialog?.querySelector?.("[data-mp-test-prompt]");
    (prompt || dialog)?.focus?.();
  }

  function refreshProviderConsole({ focusLayer = false, focusCodex = false, focusAnthropic = false, focusSubscription = "", focusCreate = false, focusTest = false, restoreFocus = false } = {}) {
    refreshActiveSettingsPanel?.();
    if (focusLayer) scheduleProviderConsoleFocus(focusProviderConsoleLayer);
    if (focusCodex) scheduleProviderConsoleFocus(focusCodexConsolePage);
    if (focusAnthropic) scheduleProviderConsoleFocus(focusAnthropicConsolePage);
    if (focusSubscription) scheduleProviderConsoleFocus(() => focusSubscriptionConsolePage(focusSubscription));
    if (focusCreate) scheduleProviderConsoleFocus(focusProviderCreatePage);
    if (focusTest) scheduleProviderConsoleFocus(focusProviderTestDialog);
    if (restoreFocus) restoreProviderConsoleLayerFocus();
  }

  function discardProviderConsoleDraft() {
    const consoleState = providerConsoleState();
    consoleState.testOpen = false;
    consoleState.test = { prompt: "", result: null };
    consoleState.modal = "";
    consoleState.drawer = "";
    consoleState.mode = "";
    consoleState.type = "";
    consoleState.providerName = "";
    consoleState.draft = null;
    consoleState.dirty = false;
    consoleState.nameTouched = false;
    consoleState.nameError = "";
  }

  function closeProviderConsoleLayer() {
    const consoleState = providerConsoleState();
    if (consoleState.testOpen) {
      consoleState.testOpen = false;
      consoleState.test = { prompt: "", result: null };
      refreshProviderConsole({ restoreFocus: true });
      return true;
    }
    if (consoleState.modal) {
      consoleState.modal = "";
      refreshProviderConsole({ restoreFocus: true });
      return true;
    }
    if (consoleState.drawer) {
      discardProviderConsoleDraft();
      refreshProviderConsole({ restoreFocus: true });
      return true;
    }
    if (consoleState.view === "codex" || consoleState.view === "anthropic" || normalizeSubscriptionProvider(consoleState.view)) {
      const subscriptionKind = normalizeSubscriptionProvider(consoleState.view);
      consoleState.view = "providers";
      consoleState.mode = "";
      consoleState.type = "";
      consoleState.providerName = "";
      consoleState.codexEdit = null;
      consoleState.codexSelectedIds = [];
      consoleState.anthropicEdit = null;
      if (subscriptionKind) {
        const page = subscriptionPage(subscriptionKind);
        if (page) page.edit = null;
      }
      setProviderConsoleResult("");
      refreshProviderConsole({ restoreFocus: true });
      return true;
    }
    return false;
  }

  // resetProviderConsoleToProviderList returns the console to its top level.
  // The account pages are drill-downs — they carry their own "back to
  // providers" control — so reopening Settings from scratch should land on the
  // provider list rather than resume whichever account page was last open.
  // Unlike closeProviderConsoleLayer this never refreshes or moves focus: the
  // caller is about to render the panel itself.
  function resetProviderConsoleToProviderList() {
    const consoleState = providerConsoleState();
    consoleState.view = "providers";
    consoleState.mode = "";
    consoleState.type = "";
    consoleState.providerName = "";
    consoleState.drawer = "";
    consoleState.modal = "";
    consoleState.testOpen = false;
    consoleState.codexEdit = null;
    consoleState.codexSelectedIds = [];
    consoleState.anthropicEdit = null;
    for (const kind of ["gemini", "grok", "kimi", "kiro"]) {
      const page = subscriptionPage(kind);
      if (page) page.edit = null;
    }
  }
  function openSubscriptionConsolePage(kind, provider = {}) {
    const spec = subscriptionProviderSpec(kind);
    if (!spec) return;
    const consoleState = providerConsoleState();
    const normalized = normalizeConsoleProvider(provider || createProviderDraft(spec.type));
    consoleState.view = spec.view;
    consoleState.modal = "";
    consoleState.drawer = "";
    consoleState.mode = spec.provider;
    consoleState.type = spec.type;
    consoleState.providerName = normalized.name || spec.provider;
    consoleState.draft = null;
    consoleState.dirty = false;
    const page = subscriptionPage(spec.provider);
    if (page) page.edit = null;
    setProviderConsoleResult("");
    refreshProviderConsole({ focusSubscription: spec.provider });
    if (!state.subscriptionAccountsLoading?.[spec.provider]) loadSubscriptionAccounts(spec.provider, { silent: true }).catch(showError);
  }

  function openGeminiConsolePage(provider = {}) {
    openSubscriptionConsolePage("gemini", provider);
  }

  function openGrokConsolePage(provider = {}) {
    openSubscriptionConsolePage("grok", provider);
  }

  function openKimiConsolePage(provider = {}) {
    openSubscriptionConsolePage("kimi", provider);
  }

  function openKiroConsolePage(provider = {}) {
    openSubscriptionConsolePage("kiro", provider);
  }

  function openCodexConsolePage(provider = {}) {
    const consoleState = providerConsoleState();
    const normalized = normalizeConsoleProvider(provider || createProviderDraft("codex"));
    consoleState.view = "codex";
    consoleState.modal = "";
    consoleState.drawer = "";
    consoleState.mode = "codex";
    consoleState.type = "codex";
    consoleState.providerName = normalized.name || "codex";
    consoleState.draft = createProviderDraft("codex", normalized);
    consoleState.dirty = false;
    consoleState.codexEdit = null;
    consoleState.codexSelectedIds = [];
    setProviderConsoleResult("");
    refreshProviderConsole({ focusCodex: true });
    if (!state.providerAuthLoading) loadProviderAuthFiles({ silent: true }).catch(showError);
  }

  function openAnthropicConsolePage(provider = {}) {
    const consoleState = providerConsoleState();
    const normalized = normalizeConsoleProvider(provider || createProviderDraft("anthropic"));
    consoleState.view = "anthropic";
    consoleState.modal = "";
    consoleState.drawer = "";
    consoleState.mode = "anthropic";
    consoleState.type = "anthropic";
    consoleState.providerName = normalized.name || "anthropic";
    consoleState.draft = providerDraftWithVisibility(createProviderDraft("anthropic", normalized), normalized.name || "anthropic");
    consoleState.dirty = false;
    consoleState.anthropicEdit = null;
    setProviderConsoleResult("");
    refreshProviderConsole({ focusAnthropic: true });
    if (!state.anthropicAccountsLoading) loadAnthropicAccounts({ silent: true }).catch(showError);
  }

  // syncAccountDraftModelHidden mirrors a visibility change made on an account
  // page into the open edit draft. Those pages write the shared preference
  // directly and need no save, but the panel's save button rebuilds the
  // provider's hidden set from the draft, so the two must not drift apart.
  // A null model name applies to every model, for the show/hide-all control.
  function syncAccountDraftModelHidden(modelName, hidden) {
    const consoleState = providerConsoleState();
    const draft = consoleState.draft;
    if (!draft || !Array.isArray(draft.modelConfigs)) return;
    const name = modelName === null ? null : String(modelName || "").trim();
    consoleState.draft = {
      ...draft,
      modelConfigs: draft.modelConfigs.map((item) => (
        name === null || item?.name === name ? { ...item, hidden } : item
      )),
    };
  }

  function providerDraftWithVisibility(draft, providerName = draft?.name) {
    const prefs = loadModelVisibilityPreferences();
    const modelConfigs = normalizeProviderModelConfigs(draft, { hiddenModels: prefs.hiddenModels, providerName });
    const selected = String(draft?.model || "").trim();
    const visibleDefault = modelConfigs.find((item) => item.name === selected && !item.hidden)?.name
      || modelConfigs.find((item) => !item.hidden)?.name
      || selected;
    return { ...draft, model: visibleDefault, models: modelConfigs.map((item) => item.name), modelConfigs };
  }

  // Model visibility is a shared preference, not provider configuration, so an
  // eye toggled on an already-saved provider's edit page applies immediately —
  // exactly like the account pages — instead of silently dying with a closed,
  // unsaved draft. The preference stays keyed by the saved provider name; a
  // rename in the open draft is migrated by the save path. A provider still
  // being created has no saved name, so its flags keep persisting on save and
  // the draft stays marked dirty.
  function persistDraftModelVisibilityForSavedProvider(consoleState, modelConfigs) {
    const savedName = consoleState.mode === "edit" ? String(consoleState.providerName || "").trim() : "";
    if (!savedName) {
      consoleState.dirty = true;
      return;
    }
    saveModelVisibilityPreferences(providerVisibilityPreferencesForDraft(loadModelVisibilityPreferences(), savedName, savedName, modelConfigs));
    renderModelOptions();
  }

  function openProviderConsoleDrawer(provider) {
    const normalized = normalizeConsoleProvider(provider || {});
    if (normalized.type === "codex" || normalized.name === "codex") {
      openCodexConsolePage(normalized);
      return;
    }
    if (isAnthropicAccountProvider(normalized)) {
      openAnthropicConsolePage(normalized);
      return;
    }
    const subscriptionKind = subscriptionProviderKind(normalized);
    if (subscriptionKind) {
      openSubscriptionConsolePage(subscriptionKind, normalized);
      return;
    }
    const consoleState = providerConsoleState();
    consoleState.view = "providers";
    consoleState.modal = "";
    consoleState.drawer = "provider";
    consoleState.mode = "edit";
    consoleState.type = normalized.type;
    consoleState.providerName = normalized.name;
    consoleState.draft = providerDraftWithVisibility(createProviderDraft(normalized.type, normalized), normalized.name);
    consoleState.autoProviderName = "";
    consoleState.dirty = false;
    setProviderConsoleResult("");
    refreshProviderConsole({ focusCreate: true });
  }

  function openProviderConsoleType(type = "openai") {
    const draft = createProviderDraft(type);
    if (type === "codex") {
      openCodexConsolePage(draft);
      return;
    }
    if (type === "anthropic") {
      openAnthropicConsolePage(draft);
      return;
    }
    const subscriptionKind = subscriptionProviderKind({ type });
    if (subscriptionKind) {
      openSubscriptionConsolePage(subscriptionKind, providerByName(subscriptionKind) || draft);
      return;
    }
    const emptyModelDraft = {
      ...draft,
      model: "",
      models: [],
      modelConfigs: [],
      modelsReady: false,
      modelsStale: false,
    };
    const consoleState = providerConsoleState();
    consoleState.view = "providers";
    consoleState.modal = "";
    consoleState.drawer = "provider";
    consoleState.mode = "create";
    consoleState.type = type;
    consoleState.providerName = emptyModelDraft.name;
    consoleState.draft = providerDraftWithVisibility(emptyModelDraft, emptyModelDraft.name);
    consoleState.autoProviderName = "";
    consoleState.dirty = false;
    setProviderConsoleResult("");
    refreshProviderConsole({ focusCreate: true });
  }

  function providerConsoleBusy(key) {
    return Boolean(providerConsoleState().busy?.[key]);
  }

  async function runProviderConsoleBusy(key, work) {
    const consoleState = providerConsoleState();
    if (consoleState.busy?.[key]) return;
    consoleState.busy = { ...(consoleState.busy || {}), [key]: true };
    refreshProviderConsole();
    try {
      return await work();
    } finally {
      const nextBusy = { ...(providerConsoleState().busy || {}) };
      delete nextBusy[key];
      providerConsoleState().busy = nextBusy;
      refreshProviderConsole();
    }
  }

  async function refreshProviderDataAfterMutation(successMessage) {
    // Settings and the model catalog are independent, and each re-walks every
    // provider server-side. Awaiting them one after the other doubled the pause
    // after a save, toggle or delete.
    const [settingsResult, catalogResult] = await Promise.allSettled([
      loadSettings(),
      loadModelCatalog(),
    ]);
    let refreshFailed = settingsResult.status === "rejected" || catalogResult.status === "rejected";
    if (state.modelCatalog?.error) refreshFailed = true;
    renderModelOptions();
    if (refreshFailed) {
      const warning = ct("messages.mutationRefreshWarning", { message: ct("messages.requestFailed") });
      setProviderConsoleResult(warning, "attention");
      notifyTerminal?.(`[warn] ${warning}\n`);
    } else {
      setProviderConsoleResult(successMessage, "success");
      notifyTerminal?.(`[info] ${successMessage}\n`);
    }
    refreshProviderConsole();
  }

  function consoleDraftFromForm(form) {
    const current = providerConsoleState();
    return providerConsoleDraftFromForm(current.draft || {}, form, current.type);
  }

  function providerNameValidationMessage(code) {
    if (code === "required") return mt("selectProviderName");
    if (code === "too_long") return mt("providerNameTooLong");
    if (code === "conflict") return mt("providerNameConflict");
    return code ? mt("invalidProviderName") : "";
  }

  function currentProviderNameValidation(value) {
    const consoleState = providerConsoleState();
    return validateProviderNameValue(value, {
      mode: consoleState.mode,
      originalName: consoleState.providerName,
      existingNames: selectableModelProviders().map((provider) => provider?.name),
    });
  }

  function updateProviderNameValidation(form, { touched = true } = {}) {
    const consoleState = providerConsoleState();
    const input = form?.elements?.name;
    if (!input) return { valid: true, code: "", name: "" };
    if (touched) consoleState.nameTouched = true;
    const validation = currentProviderNameValidation(input.value);
    const message = consoleState.nameTouched ? providerNameValidationMessage(validation.code) : "";
    consoleState.nameError = message;
    input.setCustomValidity?.(message);
    input.setAttribute?.("aria-invalid", message ? "true" : "false");
    const errorNode = form.querySelector?.("[data-mp-name-error]");
    if (errorNode) {
      errorNode.textContent = message;
      errorNode.hidden = !message;
    }
    return validation;
  }

  function validateConsoleDraft(draft, { requireModel = true, requireModelsReady = requireModel } = {}) {
    const nameValidation = currentProviderNameValidation(draft.name);
    if (!nameValidation.valid) throw new Error(providerNameValidationMessage(nameValidation.code));
    if (requireModel && !draft.model) throw new Error(mt("selectDefaultModel"));
    if (requireModelsReady && !providerModelDraftUsable(draft)) throw new Error(ct(draft.modelsStale ? "messages.modelsStaleSaveBlocked" : "messages.modelsRequired"));
    if (draft.type === "openai-compatible" && !draft.baseUrl) throw new Error(mt("missingBaseUrl"));
    const headerNames = new Set();
    for (const header of Array.isArray(draft.requestHeaders) ? draft.requestHeaders : []) {
      const name = String(header?.name || "").trim();
      const value = String(header?.value || "");
      if (!name && !value) continue;
      if (!name) throw new Error(ct("messages.requestHeaderNameRequired"));
      if (!value && !header?.keepExisting) throw new Error(ct("messages.requestHeaderValueRequired", { name }));
      const key = name.toLowerCase();
      if (headerNames.has(key)) throw new Error(ct("messages.requestHeaderDuplicate", { name }));
      headerNames.add(key);
    }
  }

  function consoleDraftRequestValues(draft, extra = {}) {
    const consoleState = providerConsoleState();
    return {
      ...draft,
      ...extra,
      ...(consoleState.mode === "create"
        ? { createOnly: true }
        : { originalName: String(consoleState.providerName || draft.name || "").trim() }),
    };
  }

  function consoleDraftCanDiscoverModels(draft) {
    if (!draft || draft.type === "codex") return false;
    if (draft.type === "openai-compatible" && !draft.baseUrl) return false;
    const current = providerConsoleState();
    const existing = providerByName(current.providerName || draft.name);
    return Boolean(draft.apiKey || draft.apiKeyOptional || existing?.configured);
  }

  async function discoverConsoleProviderModels(form) {
    const consoleState = providerConsoleState();
    const rawDraft = consoleDraftFromForm(form);
    consoleState.draft = rawDraft;
    consoleState.dirty = true;
    const draft = { ...rawDraft, ...providerConfigPayload(rawDraft) };
    updateProviderNameValidation(form);
    validateConsoleDraft(draft, { requireModel: false });
    if (!requireProviderSensitiveDraftAccess()) return false;
    if (!consoleDraftCanDiscoverModels(draft)) {
      const message = ct("messages.currentDraftTestNeedsApiKey");
      setProviderConsoleResult(message, "attention");
      notifyTerminal?.(`[warn] ${message}\n`);
      return false;
    }
    if (providerConsoleBusy(`models:${draft.name}`)) return false;
    consoleState.draft = draft;
    consoleState.dirty = true;
    await runProviderConsoleBusy(`models:${draft.name}`, async () => {
      try {
        const request = providerConsoleRequest("test", null, consoleDraftRequestValues(draft));
        const response = await requestAPI(request.path, request.options);
        const preflight = providerPreflightResult(response, ct);
        if (preflight.tone !== "success") {
          setProviderConsoleResult(preflight.message, preflight.tone);
          notifyTerminal?.(`[${preflight.terminalLevel}] ${preflight.message}\n`);
          return;
        }
        const discovery = providerModelDiscovery(response, draft.model, draft.modelConfigs);
        if (!discovery.models.length) {
          const message = ct("messages.noModelsDiscovered");
          setProviderConsoleResult(message, "attention");
          notifyTerminal?.(`[warn] ${message}\n`);
          return;
        }
        consoleState.draft = {
          ...draft,
          models: discovery.models,
          modelConfigs: discovery.modelConfigs,
          model: discovery.selectedModel,
          modelsReady: true,
          modelsStale: false,
        };
        const message = ct("messages.modelsDiscovered", {
          count: discovery.models.length,
          model: discovery.selectedModel,
        });
        setProviderConsoleResult(message, "success");
        notifyTerminal?.(`[info] ${message}\n`);
      } catch (error) {
        const message = ct("messages.currentDraftTestFailed", { message: error?.message || ct("messages.requestFailed") });
        setProviderConsoleResult(message, "attention");
        notifyTerminal?.(`[warn] ${message}\n`);
      }
    });
    return true;
  }

  function openProviderMessageTest(form) {
    const consoleState = providerConsoleState();
    const rawDraft = consoleDraftFromForm(form);
    const draft = { ...rawDraft, ...providerConfigPayload(rawDraft) };
    updateProviderNameValidation(form);
    consoleState.draft = draft;
    consoleState.testOpen = true;
    consoleState.test = {
      prompt: consoleState.test?.prompt || ct("test.defaultPrompt"),
      result: null,
    };
    refreshProviderConsole({ focusTest: true });
  }

  async function sendProviderMessageTest(form) {
    const consoleState = providerConsoleState();
    const providerForm = providerConsoleEventRoot?.querySelector?.("[data-mp-provider-form]");
    const rawDraft = providerForm ? consoleDraftFromForm(providerForm) : (consoleState.draft || {});
    const draft = { ...rawDraft, ...providerConfigPayload(rawDraft) };
    const prompt = String(form?.elements?.prompt?.value || "").trim();
    try {
      if (providerForm) updateProviderNameValidation(providerForm);
      validateConsoleDraft(draft);
      if (!prompt) throw new Error(ct("test.promptRequired"));
    } catch (error) {
      consoleState.test = {
        ...(consoleState.test || {}),
        prompt,
        result: { success: false, tone: "attention", message: error?.message || ct("test.failureMessage") },
      };
      refreshProviderConsole({ focusTest: true });
      return;
    }
    consoleState.draft = draft;
    consoleState.dirty = true;
    if (!requireProviderSensitiveDraftAccess()) return false;
    if (!draft.name || providerConsoleBusy(`message-test:${draft.name}`)) return;
    consoleState.test = { ...(consoleState.test || {}), prompt, result: null };
    await runProviderConsoleBusy(`message-test:${draft.name}`, async () => {
      try {
        const request = providerConsoleRequest("message-test", null, consoleDraftRequestValues(draft, { prompt }));
        const response = await requestAPI(request.path, request.options);
        const success = response?.success === true;
        const result = {
          success,
          tone: success ? "success" : "attention",
          output: success ? String(response?.output || "") : "",
          message: success
            ? ct("test.successMessage", { model: response?.model || draft.model })
            : String(response?.message || ct("test.failureMessage")),
        };
        consoleState.test = { ...(consoleState.test || {}), prompt, result };
        notifyTerminal?.(`[${success ? "info" : "warn"}] ${result.message}\n`);
      } catch (error) {
        const message = error?.message || ct("test.failureMessage");
        consoleState.test = {
          ...(consoleState.test || {}),
          prompt,
          result: { success: false, tone: "attention", message },
        };
        notifyTerminal?.(`[warn] ${message}\n`);
      }
    });
  }

  async function testConsoleProvider(form) {
    const consoleState = providerConsoleState();
    const rawDraft = consoleDraftFromForm(form);
    consoleState.draft = rawDraft;
    consoleState.dirty = true;
    const draft = { ...rawDraft, ...providerConfigPayload(rawDraft) };
    updateProviderNameValidation(form);
    validateConsoleDraft(draft);
    if (!requireProviderSensitiveDraftAccess()) return false;
    if (!draft.name || providerConsoleBusy(`test:${draft.name}`)) return;
    consoleState.draft = draft;
    await runProviderConsoleBusy(`test:${draft.name}`, async () => {
      try {
        const request = providerConsoleRequest("test", null, consoleDraftRequestValues(draft));
        const response = await requestAPI(request.path, request.options);
        const preflight = providerPreflightResult(response, ct);
        setProviderConsoleResult(preflight.message, preflight.tone);
        notifyTerminal?.(`[${preflight.terminalLevel}] ${preflight.message}\n`);
      } catch (error) {
        const message = ct("messages.currentDraftTestFailed", { message: error?.message || ct("messages.requestFailed") });
        setProviderConsoleResult(message, "attention");
        notifyTerminal?.(`[warn] ${message}\n`);
      }
    });
  }

  async function saveConsoleProvider(form) {
    const consoleState = providerConsoleState();
    const rawDraft = consoleDraftFromForm(form);
    consoleState.draft = rawDraft;
    consoleState.dirty = true;
    const draft = { ...rawDraft, ...providerConfigPayload(rawDraft) };
    updateProviderNameValidation(form);
    validateConsoleDraft(draft);
    if (!requireProviderSensitiveDraftAccess()) return false;
    if (providerConsoleBusy(`save:${draft.name}`)) return;
    consoleState.draft = draft;
    await runProviderConsoleBusy(`save:${draft.name}`, async () => {
      let saved = false;
      try {
        const originalName = consoleState.mode === "edit"
          ? String(consoleState.providerName || draft.name).trim()
          : String(draft.name).trim();
        const configRequest = providerConsoleRequest("config", { name: originalName }, consoleDraftRequestValues({ ...providerConfigPayload(draft), pathName: originalName }));
        await requestAPI(configRequest.path, configRequest.options);
        saved = true;
        const retainedAPIKey = !draft.clearApiKey && draft.apiKeyDraft ? String(draft.apiKey || "") : "";
        const apiKeyConfigured = !draft.clearApiKey && Boolean(retainedAPIKey || draft.apiKeyConfigured);
        const apiKeyPersisted = !draft.clearApiKey && Boolean(retainedAPIKey || draft.apiKeyPersisted);
        consoleState.mode = "edit";
        consoleState.type = draft.type;
        consoleState.providerName = draft.name;
        consoleState.draft = {
          ...draft,
          apiKey: retainedAPIKey,
          apiKeyDraft: Boolean(retainedAPIKey),
          apiKeyConfigured,
          apiKeyPersisted,
          apiKeyLastFive: retainedAPIKey ? retainedAPIKey.slice(-5) : (draft.clearApiKey ? "" : draft.apiKeyLastFive),
          apiKeySource: retainedAPIKey ? "stored" : (draft.clearApiKey ? "none" : draft.apiKeySource),
          clearApiKey: false,
          proxyUrl: redactedProviderProxyURL(draft.proxyUrl),
          proxyUrlDraft: false,
          requestHeaders: (Array.isArray(draft.requestHeaders) ? draft.requestHeaders : []).map((header) => ({
            name: String(header?.name || "").trim(),
            value: "",
            keepExisting: true,
            configured: true,
          })).filter((header) => header.name),
          requestHeadersDraft: false,
          clearProxyAuth: false,
        };
        consoleState.dirty = false;
        const enableRequest = providerConsoleRequest("toggle", { name: draft.name, defaultModel: draft.model }, { enabled: true, model: draft.model });
        await requestAPI(enableRequest.path, enableRequest.options);
        const nextVisibility = providerVisibilityPreferencesForDraft(loadModelVisibilityPreferences(), originalName, draft.name, draft.modelConfigs);
        await persistModelVisibilityPreferences(nextVisibility);
        await refreshProviderDataAfterMutation(ct("messages.providerSavedAndEnabled", { provider: providerDisplayName(draft) }));
      } catch (error) {
        const message = saved
          ? ct("messages.providerSavedEnableFailed")
          : (error?.message || ct("messages.providerSaveFailed"));
        if (!saved && error?.status === 409) {
          consoleState.nameTouched = true;
          consoleState.nameError = message;
        }
        setProviderConsoleResult(message, "attention");
        notifyTerminal?.(`[warn] ${message}\n`);
        refreshProviderConsole();
      }
    });
  }

  async function toggleConsoleProvider(name) {
    const provider = providerByName(name);
    if (!provider || providerConsoleBusy(`toggle:${name}`) || providerConsoleBusy(`delete:${name}`)) return;
    const enabled = !Boolean(provider.enabled);
    const model = String(provider.defaultModel || provider.model || "").trim();
    await runProviderConsoleBusy(`toggle:${name}`, async () => {
      const request = providerConsoleRequest("toggle", provider, { enabled, model });
      await requestAPI(request.path, request.options);
      // No banner on toggle — the switch itself is the feedback.
      await refreshProviderDataAfterMutation("");
    });
  }

  async function deleteConsoleProvider(name) {
    const provider = providerByName(name);
    if (!provider || !isProviderDeletable(provider) || providerConsoleBusy(`delete:${name}`) || providerConsoleBusy(`toggle:${name}`)) return;
    if (!(await platformConfirm(ct("messages.deleteProviderConfirm", { provider: providerDisplayName(provider) })))) return;
    await runProviderConsoleBusy(`delete:${name}`, async () => {
      const request = providerConsoleRequest("delete", provider);
      await requestAPI(request.path, request.options);
      await persistModelVisibilityPreferences(removeProviderVisibilityPreferences(loadModelVisibilityPreferences(), name));
      const consoleState = providerConsoleState();
      consoleState.drawer = "";
      consoleState.mode = "";
      consoleState.type = "";
      consoleState.providerName = "";
      consoleState.draft = null;
      consoleState.dirty = false;
      await refreshProviderDataAfterMutation(ct("messages.providerDeleted", { provider: providerDisplayName(provider) }));
      restoreProviderConsoleLayerFocus();
    });
  }

  function providerDisplayName(provider) {
    if (provider?.type === "codex" || provider?.name === "codex") return "Codex OAuth";
    if (provider?.name === "ollama") return "Ollama";
    return provider?.name || provider?.type || ct("labels.provider");
  }

  function updateAutomaticProviderName(consoleState, form, target) {
    if (consoleState.mode !== "create") return false;
    if (target?.name === "name") {
      if (String(target.value || "").trim() !== String(consoleState.autoProviderName || "").trim()) consoleState.autoProviderName = "";
      return false;
    }
    if (target?.name !== "baseUrl") return false;
    const nameInput = form?.elements?.name;
    if (!nameInput) return false;
    const update = automaticProviderNameUpdate(target.value, nameInput.value, consoleState.autoProviderName, consoleState.mode);
    consoleState.autoProviderName = update.suggestion;
    if (update.changed) nameInput.value = update.name;
    return update.changed;
  }

  function updateProviderConsoleDraftFromEvent(event) {
    const target = event.target;
    const form = target?.closest?.("[data-mp-provider-form]");
    if (!form || (!target?.name && !target?.matches?.("[data-mp-model-choice]"))) return false;
    if (target?.name === "userAgentPreset") applyProviderUserAgentPresetToForm(form, target.value);
    const consoleState = providerConsoleState();
    const automaticNameChanged = updateAutomaticProviderName(consoleState, form, target);
    if (target?.name === "apiKey") {
      consoleState.draft = { ...(consoleState.draft || {}), apiKey: String(target.value || ""), apiKeyDraft: true };
    }
    const draft = syncProviderConsoleDraft(consoleState, form);
    if (!draft) return false;
    const prefixPreview = form.querySelector?.("[data-mp-provider-prefix-preview]");
    if (prefixPreview) {
      const value = draft.name || "provider";
      if ("value" in prefixPreview) prefixPreview.value = value;
      else prefixPreview.textContent = value;
    }
    const example = form.querySelector?.("[data-mp-model-example]");
    if (example) {
      const value = `${draft.name || "provider"}:${draft.model || "your-model"}`;
      if ("value" in example) example.value = value;
      else example.textContent = value;
    }
    if (automaticNameChanged) updateProviderNameValidation(form);
    return true;
  }

  function handleProviderConsoleFocus(event) {
    selectProviderConsoleFieldOnFocus(event.target);
  }

  function handleProviderConsoleFocusOut(event) {
    if (event.target?.name !== "name") return;
    const form = event.target.closest?.("[data-mp-provider-form]");
    if (form) updateProviderNameValidation(form);
  }

  function handleProviderConsoleInput(event) {
    const rawTarget = event.target;
    if (rawTarget?.hasAttribute?.("data-kiro-token-input") || rawTarget?.hasAttribute?.("data-kiro-region-input") || rawTarget?.hasAttribute?.("data-kiro-apikey-input")) {
      handleKiroInputChange(event);
      return;
    }
    if (rawTarget?.matches?.("[data-mp-test-prompt]")) {
      providerConsoleState().test = {
        ...(providerConsoleState().test || {}),
        prompt: rawTarget.value || "",
      };
      return;
    }
    if (rawTarget?.matches?.("[data-anthropic-profile]")) {
      const consoleState = providerConsoleState();
      consoleState.anthropicProfile = rawTarget.value || "";
      const form = rawTarget.closest?.("[data-anthropic-account-form]");
      const command = anthropicProfileLoginCommand(consoleState.anthropicProfile);
      const commandNode = form?.querySelector?.("[data-anthropic-command]");
      const copyButton = form?.querySelector?.("[data-anthropic-copy-command]");
      if (commandNode) commandNode.textContent = command;
      if (copyButton) copyButton.dataset.anthropicCopyCommand = command;
      return;
    }
    if (rawTarget?.matches?.("[data-anthropic-alias]")) {
      providerConsoleState().anthropicAlias = rawTarget.value || "";
      return;
    }
    if (rawTarget?.matches?.("[data-anthropic-priority]")) {
      providerConsoleState().anthropicPriority = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.anthropicEditAlias) {
      const edit = providerConsoleState().anthropicEdit;
      if (edit?.id === rawTarget.dataset.anthropicEditAlias) edit.alias = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.anthropicEditPriority) {
      const edit = providerConsoleState().anthropicEdit;
      if (edit?.id === rawTarget.dataset.anthropicEditPriority) edit.priority = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.subscriptionEditAlias) {
      const provider = normalizeSubscriptionProvider(rawTarget.dataset.subscriptionProvider);
      const edit = provider ? subscriptionPage(provider)?.edit : null;
      if (edit?.id === rawTarget.dataset.subscriptionEditAlias) edit.alias = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.subscriptionEditPriority) {
      const provider = normalizeSubscriptionProvider(rawTarget.dataset.subscriptionProvider);
      const edit = provider ? subscriptionPage(provider)?.edit : null;
      if (edit?.id === rawTarget.dataset.subscriptionEditPriority) edit.priority = rawTarget.value || "";
      return;
    }
    if (rawTarget?.matches?.("[data-codex-import-draft]")) {
      providerConsoleState().codexImportDraft = rawTarget.value || "";
      return;
    }
    if (rawTarget?.matches?.("[data-codex-batch-priority]")) {
      providerConsoleState().codexBatchPriority = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.codexEditAlias) {
      const edit = providerConsoleState().codexEdit;
      if (edit?.id === rawTarget.dataset.codexEditAlias) edit.alias = rawTarget.value || "";
      return;
    }
    if (rawTarget?.dataset?.codexEditPriority) {
      const edit = providerConsoleState().codexEdit;
      if (edit?.id === rawTarget.dataset.codexEditPriority) edit.priority = rawTarget.value || "";
      return;
    }
    if (rawTarget?.matches?.("[data-mp-model-token]")) {
      const consoleState = providerConsoleState();
      const name = String(rawTarget.dataset.mpModelToken || "").trim();
      const value = Math.min(10_000_000, Math.max(0, Math.floor(Number(rawTarget.value || 0) || 0)));
      consoleState.draft = {
        ...(consoleState.draft || {}),
        // The whole draft: a provider whose list lives under `models` would
        // otherwise rebuild from nothing and lose every row but the edited one.
        modelConfigs: normalizeProviderModelConfigs(consoleState.draft || {}).map((item) => item.name === name ? { ...item, contextTokenLimit: value } : item),
      };
      consoleState.dirty = true;
      return;
    }
    if (rawTarget?.matches?.("[data-mp-request-header-name], [data-mp-request-header-value]")) {
      const row = rawTarget.closest?.("[data-mp-request-header-row]");
      if (rawTarget.matches?.("[data-mp-request-header-value]") && rawTarget.value) row?.setAttribute?.("data-keep-existing", "false");
      const form = rawTarget.closest?.("[data-mp-provider-form]");
      if (form) syncProviderConsoleDraft(providerConsoleState(), form);
      return;
    }
    if (updateProviderConsoleDraftFromEvent(event)) {
      if (rawTarget?.name === "name") {
        const form = rawTarget.closest?.("[data-mp-provider-form]");
        if (form) updateProviderNameValidation(form);
      }
      return;
    }
    const target = rawTarget?.closest?.("[data-mp-provider-search]");
    if (!target) return;
    providerConsoleState().search = target.value || "";
    refreshProviderConsole();
  }

  function handleProviderConsoleChange(event) {
    const target = event.target;
    if (target?.hasAttribute?.("data-kiro-token-input") || target?.hasAttribute?.("data-kiro-region-input") || target?.hasAttribute?.("data-kiro-apikey-input")) {
      handleKiroInputChange(event);
      return;
    }
    if (target?.matches?.("[data-codex-import-files]")) {
      setCodexImportFiles(target.files);
      target.value = "";
      refreshProviderConsole();
      return;
    }
    if (target?.matches?.("[data-codex-select]")) {
      const consoleState = providerConsoleState();
      if (consoleState.codexBatchBusy) return;
      const id = String(target.dataset.codexSelect || "").trim();
      const selected = new Set(normalizeCodexSelectedIds(consoleState.codexSelectedIds));
      if (target.checked) selected.add(id);
      else selected.delete(id);
      consoleState.codexSelectedIds = normalizeCodexSelectedIds([...selected], extractAuthFiles(state.providerAuthFiles));
      refreshProviderConsole();
      return;
    }
    if (target?.matches?.("[data-codex-select-all]")) {
      const consoleState = providerConsoleState();
      if (consoleState.codexBatchBusy) return;
      consoleState.codexSelectedIds = target.checked
        ? extractAuthFiles(state.providerAuthFiles).map(codexAccountStableID).filter(Boolean)
        : [];
      refreshProviderConsole();
      return;
    }
    if (target?.matches?.("[data-mp-model-image-generation]")) {
      if (target.disabled) return;
      const consoleState = providerConsoleState();
      const name = String(target.dataset.mpModelImageGeneration || "").trim();
      consoleState.draft = {
        ...(consoleState.draft || {}),
        modelConfigs: normalizeProviderModelConfigs(consoleState.draft || {}).map((item) => item.name === name ? { ...item, imageGeneration: Boolean(target.checked) } : item),
      };
      consoleState.dirty = true;
      return;
    }
    const form = target?.closest?.("[data-mp-provider-form]");
    if (target?.matches?.("[data-mp-model-choice]") && form?.elements?.model) {
      form.elements.model.value = target.value || "";
    } else if (target?.name === "model" && form) {
      const choice = form.querySelector?.("[data-mp-model-choice]");
      if (choice && choice.value !== target.value) choice.value = "";
    }
    if (target?.matches?.("[data-mp-clear-api-key]") && target.checked) {
      void platformConfirm(ct("messages.clearApiKeyConfirm")).then((ok) => {
        if (!ok) target.checked = false;
      });
    }
    if (target?.matches?.("[data-mp-clear-proxy-auth]") && target.checked) {
      void platformConfirm(ct("messages.clearProxyAuthConfirm")).then((ok) => {
        if (!ok) target.checked = false;
      });
    }
    const updated = updateProviderConsoleDraftFromEvent(event);
    if (updated && (target?.name === "insecureSkipTLSVerify" || target?.name === "allowPlaintextHTTP")) {
      refreshProviderConsole();
      return;
    }
    if (!updated) return;
    if (providerConsoleState().draft?.modelsStale) refreshProviderConsole();
  }

  function handleProviderConsoleDragOver(event) {
    const zone = event.target?.closest?.("[data-codex-import-drop]");
    if (!zone) return;
    event.preventDefault();
    zone.classList.add("is-dragging");
    if (event.dataTransfer) event.dataTransfer.dropEffect = "copy";
  }

  function handleProviderConsoleDragLeave(event) {
    const zone = event.target?.closest?.("[data-codex-import-drop]");
    if (!zone || zone.contains?.(event.relatedTarget)) return;
    zone.classList.remove("is-dragging");
  }

  function handleProviderConsoleDrop(event) {
    const zone = event.target?.closest?.("[data-codex-import-drop]");
    if (!zone) return;
    event.preventDefault();
    zone.classList.remove("is-dragging");
    setCodexImportFiles(event.dataTransfer?.files);
    refreshProviderConsole();
  }

  function handleProviderConsoleKeydown(event) {
    const layer = event.target?.closest?.('[role="dialog"][aria-modal="true"]');
    if (layer && trapProviderConsoleFocus(event, layer)) return;
    if (event.key === "Escape" && closeProviderConsoleLayer()) {
      event.preventDefault();
      return;
    }
    const card = event.target?.closest?.("[data-mp-provider-card]");
    if (shouldOpenProviderCardFromKeyboard(event, card)) {
      event.preventDefault();
      rememberProviderConsoleFocus(card);
      openProviderConsoleDrawer(providerByName(card.dataset.mpProviderCard));
    }
  }

  function handleProviderConsoleSubmit(event) {
    const testForm = event.target?.closest?.("[data-mp-provider-test-form]");
    if (testForm) {
      event.preventDefault();
      sendProviderMessageTest(testForm).catch(showError);
      return;
    }
    const anthropicAccountForm = event.target?.closest?.("[data-anthropic-account-form]");
    if (anthropicAccountForm) {
      event.preventDefault();
      createAnthropicAccount(anthropicAccountForm).catch(showError);
      return;
    }
    const form = event.target?.closest?.("[data-mp-provider-form]");
    if (!form) return;
    event.preventDefault();
    saveConsoleProvider(form).catch(showError);
  }

  function handleProviderConsoleClick(event) {
    const target = event.target?.closest?.("button, [data-mp-provider-card], [data-mp-backdrop]");
    if (!target) return;
    const consoleState = providerConsoleState();
    if (target.dataset.mpBackdrop && event.target === target) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.mpToggleApiKey !== undefined) {
      const input = target.closest?.(".mp-provider-secret-control")?.querySelector?.('input[name="apiKey"]');
      if (!input) return;
      const revealing = input.type === "password";
      input.type = revealing ? "text" : "password";
      const label = ct(revealing ? "actions.hideApiKey" : "actions.showApiKey");
      target.setAttribute?.("aria-pressed", revealing ? "true" : "false");
      target.setAttribute?.("aria-label", label);
      target.setAttribute?.("title", label);
      return;
    }
    if (target.dataset.mpCloseCodexPage !== undefined || target.dataset.mpCloseAnthropicPage !== undefined) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.codexChooseImportFiles !== undefined) {
      $("codexAuthImportFiles")?.click?.();
      return;
    }
    if (target.dataset.codexClearImportFiles !== undefined) {
      setCodexImportFiles([]);
      refreshProviderConsole();
      return;
    }
    if (target.dataset.codexSelectAllAccounts !== undefined) {
      consoleState.codexSelectedIds = extractAuthFiles(state.providerAuthFiles).map(codexAccountStableID).filter(Boolean);
      refreshProviderConsole();
      return;
    }
    if (target.dataset.codexClearSelection !== undefined) {
      consoleState.codexSelectedIds = [];
      refreshProviderConsole();
      return;
    }
    if (target.dataset.codexBatchSync !== undefined) {
      runCodexBatchOperation("sync").catch(showError);
      return;
    }
    if (target.dataset.codexBatchEnable !== undefined) {
      runCodexBatchOperation("enable").catch(showError);
      return;
    }
    if (target.dataset.codexBatchDisable !== undefined) {
      runCodexBatchOperation("disable").catch(showError);
      return;
    }
    if (target.dataset.codexBatchPriorityApply !== undefined) {
      runCodexBatchOperation("set_priority").catch(showError);
      return;
    }
    if (target.dataset.codexBatchDelete !== undefined) {
      runCodexBatchOperation("delete").catch(showError);
      return;
    }
    if (target.dataset.anthropicAddMode) {
      const requestedMode = target.dataset.anthropicAddMode;
      consoleState.anthropicAddMode = requestedMode === "api_key" ? "api_key" : requestedMode === "oauth" ? "oauth" : "profile";
      refreshProviderConsole();
      if (consoleState.anthropicAddMode !== "oauth") {
        scheduleProviderConsoleFocus(() => providerConsoleEventRoot?.querySelector?.("[data-anthropic-account-form] input:not([type=hidden])")?.focus?.());
      }
      return;
    }
    if (target.dataset.anthropicLoginStart !== undefined) {
      startAnthropicLogin().catch(showError);
      return;
    }
    if (target.dataset.anthropicLoginReopen !== undefined) {
      reopenAnthropicLogin().catch(showError);
      return;
    }
    if (target.dataset.anthropicLoginSubmit !== undefined) {
      const code = providerConsoleEventRoot?.querySelector?.("[data-anthropic-login-code]")?.value || "";
      submitAnthropicLogin(code).catch(showError);
      return;
    }
    if (target.dataset.anthropicLoginPaste !== undefined) {
      pasteAnthropicLogin().catch(showError);
      return;
    }
    if (target.dataset.anthropicLoginCancel !== undefined) {
      cancelAnthropicLogin().catch(showError);
      return;
    }
    if (target.dataset.anthropicFocusAdd !== undefined) {
      providerConsoleEventRoot?.querySelector?.("#anthropic-add-account")?.scrollIntoView?.({ block: "start", behavior: "smooth" });
      scheduleProviderConsoleFocus(() => providerConsoleEventRoot?.querySelector?.("[data-anthropic-account-form] input:not([type=hidden])")?.focus?.());
      return;
    }
    if (target.dataset.anthropicCopyCommand !== undefined) {
      copyText?.(target.dataset.anthropicCopyCommand || "");
      setProviderConsoleResult(mt("anthropic.commandCopied"), "success");
      refreshProviderConsole();
      return;
    }
    if (target.dataset.anthropicRefresh !== undefined) {
      loadAnthropicAccounts().catch(showError);
      return;
    }
    if (target.dataset.anthropicEdit) {
      const id = target.dataset.anthropicEdit;
      const account = anthropicAccountById(id);
      if (!account || account.managed === false) return;
      consoleState.anthropicEdit = { id, alias: String(account.alias || ""), priority: finiteNumber(account.priority, 100) };
      refreshProviderConsole();
      scheduleProviderConsoleFocus(() => [...(providerConsoleEventRoot?.querySelectorAll?.("[data-anthropic-edit-alias]") || [])].find((node) => node.dataset?.anthropicEditAlias === id)?.focus?.());
      return;
    }
    if (target.dataset.anthropicEditCancel) {
      if (consoleState.anthropicEdit?.id === target.dataset.anthropicEditCancel) consoleState.anthropicEdit = null;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.anthropicSave) {
      saveAnthropicAccount(target.dataset.anthropicSave, target).catch(showError);
      return;
    }
    if (target.dataset.anthropicSync) {
      syncAnthropicAccount(target.dataset.anthropicSync, target).catch(showError);
      return;
    }
    if (target.dataset.anthropicToggle) {
      toggleAnthropicAccount(target.dataset.anthropicToggle, target.dataset.disabled === "true", target).catch(showError);
      return;
    }
    if (target.dataset.anthropicDelete) {
      deleteAnthropicAccount(target.dataset.anthropicDelete, target).catch(showError);
      return;
    }
    if (target.dataset.mpCloseTest !== undefined) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.mpCloseModal !== undefined) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.mpCloseDrawer !== undefined) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.mpAddManualModel !== undefined) {
      const form = target.closest?.("[data-mp-provider-form]");
      const input = form?.querySelector?.("[data-mp-manual-model-input]");
      const name = String(input?.value || "").trim();
      if (!name) return;
      const draft = form ? providerConsoleDraftFromForm(consoleState.draft || {}, form, consoleState.type) : { ...(consoleState.draft || {}) };
      const configs = normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs });
      if (!configs.some((item) => item.name === name)) configs.push({ name, contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: true });
      consoleState.draft = {
        ...draft,
        model: draft.model && configs.some((item) => item.name === draft.model && !item.hidden) ? draft.model : name,
        models: configs.map((item) => item.name),
        modelConfigs: configs,
        modelsReady: true,
        modelsStale: false,
      };
      consoleState.dirty = true;
      setProviderConsoleResult(ct("messages.manualModelAdded", { model: name }), "info");
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpModelVisibility) {
      const name = String(target.dataset.mpModelVisibility || "").trim();
      // Official account pages render the live provider, not an edit draft, so
      // their eye toggles the shared visibility preference directly. That
      // preference filters the composer's picker and applies without a save step.
      const accountProviderName = accountModelFormProvider(target);
      if (accountProviderName) {
        const hidden = target.dataset.hidden !== "true";
        setModelHidden(`${accountProviderName}:${name}`, hidden);
        // The same panel has a save button, and saving rebuilds this provider's
        // hidden set from the draft. Leaving the draft behind meant the save
        // replayed whatever was hidden when the panel opened and silently
        // un-hid everything toggled since.
        syncAccountDraftModelHidden(name, hidden);
        return;
      }
      const draft = { ...(consoleState.draft || {}) };
      const result = setProviderModelHidden(draft.modelConfigs, name, target.dataset.hidden !== "true", draft.model, { allowEmpty: true });
      if (!result.changed) {
        setProviderConsoleResult(ct("messages.lastVisibleModel"), "attention");
        refreshProviderConsole();
        return;
      }
      consoleState.draft = { ...draft, model: result.defaultModel, modelConfigs: result.modelConfigs, models: result.modelConfigs.map((item) => item.name) };
      persistDraftModelVisibilityForSavedProvider(consoleState, result.modelConfigs);
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpModelVisibilityAll !== undefined) {
      const accountProviderName = accountModelFormProvider(target);
      if (accountProviderName) {
        const hidden = target.dataset.allVisible === "true";
        setProviderModelsHidden(accountProviderName, hidden);
        syncAccountDraftModelHidden(null, hidden);
        return;
      }
      const draft = { ...(consoleState.draft || {}) };
      const result = setProviderModelHiddenAll(draft.modelConfigs, target.dataset.allVisible === "true", draft.model, { allowEmpty: true });
      if (!result.changed) {
        refreshProviderConsole();
        return;
      }
      consoleState.draft = { ...draft, model: result.defaultModel, modelConfigs: result.modelConfigs, models: result.modelConfigs.map((item) => item.name) };
      persistDraftModelVisibilityForSavedProvider(consoleState, result.modelConfigs);
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpRemoveManualModel) {
      const name = String(target.dataset.mpRemoveManualModel || "").trim();
      const draft = { ...(consoleState.draft || {}) };
      const configs = normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs }).filter((item) => item.name !== name || !item.manual);
      const visible = configs.filter((item) => !item.hidden);
      consoleState.draft = {
        ...draft,
        model: draft.model === name ? (visible[0]?.name || "") : draft.model,
        models: configs.map((item) => item.name),
        modelConfigs: configs,
        modelsReady: Boolean(visible.length),
      };
      consoleState.dirty = true;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpAddRequestHeader !== undefined) {
      const form = target.closest?.("[data-mp-provider-form]");
      const draft = form ? providerConsoleDraftFromForm(consoleState.draft || {}, form, consoleState.type) : { ...(consoleState.draft || {}) };
      draft.requestHeaders = [...(Array.isArray(draft.requestHeaders) ? draft.requestHeaders : []), { name: "", value: "", keepExisting: false, configured: false }];
      draft.requestHeadersDraft = true;
      consoleState.draft = draft;
      consoleState.dirty = true;
      refreshProviderConsole();
      scheduleProviderConsoleFocus(() => {
        const fields = providerConsoleEventRoot?.querySelectorAll?.("[data-mp-request-header-name]") || [];
        fields[fields.length - 1]?.focus?.();
      });
      return;
    }
    if (target.dataset.mpRemoveRequestHeader !== undefined) {
      const form = target.closest?.("[data-mp-provider-form]");
      const draft = form ? providerConsoleDraftFromForm(consoleState.draft || {}, form, consoleState.type) : { ...(consoleState.draft || {}) };
      const index = Number(target.dataset.mpRemoveRequestHeader);
      const previousDraft = consoleState.draft || {};
      draft.requestHeaders = (Array.isArray(draft.requestHeaders) ? draft.requestHeaders : []).filter((_, itemIndex) => itemIndex !== index);
      draft.requestHeadersDraft = true;
      consoleState.draft = markProviderModelsStale(previousDraft, draft);
      consoleState.dirty = true;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.codexEdit) {
      const id = target.dataset.codexEdit;
      const account = extractAuthFiles(state.providerAuthFiles).find((item) => String(item?.id || item?.auth_index || item?.authIndex || item?.name || "") === id);
      if (!account) return;
      consoleState.codexEdit = {
        id,
        alias: String(account.alias || ""),
        priority: finiteNumber(account.priority, 100),
      };
      refreshProviderConsole();
      scheduleProviderConsoleFocus(() => {
        const field = [...(providerConsoleEventRoot?.querySelectorAll?.("[data-codex-edit-alias]") || [])]
          .find((node) => node.dataset?.codexEditAlias === id);
        field?.focus?.();
        field?.select?.();
      });
      return;
    }
    if (target.dataset.codexEditCancel) {
      if (consoleState.codexEdit?.id === target.dataset.codexEditCancel) consoleState.codexEdit = null;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpOpenTypes !== undefined) {
      rememberProviderConsoleFocus(target);
      openProviderConsoleType();
      return;
    }
    if (target.dataset.mpSelectType) {
      openProviderConsoleType(target.dataset.mpSelectType);
      return;
    }
    if (target.dataset.mpCategory) {
      consoleState.category = target.dataset.mpCategory;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.mpProviderToggle) {
      toggleConsoleProvider(target.dataset.mpProviderToggle).catch(showError);
      return;
    }
    if (target.dataset.mpProviderOpen) {
      rememberProviderConsoleFocus(target);
      openProviderConsoleDrawer(providerByName(target.dataset.mpProviderOpen));
      return;
    }
    if (target.dataset.mpProviderCard) {
      rememberProviderConsoleFocus(target);
      openProviderConsoleDrawer(providerByName(target.dataset.mpProviderCard));
      return;
    }
    if (target.dataset.mpFetchModels !== undefined) {
      const form = target.closest?.("[data-mp-provider-form]");
      if (form) discoverConsoleProviderModels(form).catch(showError);
      else {
        setProviderConsoleResult("");
        runProviderConsoleBusy("refresh", async () => {
          await refreshModelCatalog();
        }).catch(showError);
      }
      return;
    }
    if (target.dataset.mpRefreshModels !== undefined) {
      setProviderConsoleResult("");
      runProviderConsoleBusy("refresh", async () => {
        await refreshModelCatalog();
      }).catch(showError);
      return;
    }
    if (target.dataset.mpTestProvider !== undefined) {
      const form = target.closest("[data-mp-provider-form]");
      if (form) {
        rememberProviderConsoleFocus(target);
        openProviderMessageTest(form);
      }
      return;
    }
    if (target.dataset.mpDeleteProvider) {
      deleteConsoleProvider(target.dataset.mpDeleteProvider).catch(showError);
      return;
    }
    if (target.dataset.mpCodexBrowserLogin !== undefined) {
      startCodexBrowserLogin().catch(showError);
      return;
    }
    if (target.dataset.mpCodexBrowserOpen !== undefined) {
      reopenCodexBrowserLogin().catch(showError);
      return;
    }
    if (target.dataset.mpCodexBrowserCancel !== undefined) {
      cancelCodexBrowserLogin().catch(showError);
      return;
    }
    if (target.dataset.mpCodexImport !== undefined) {
      importCodexAuthFile().catch(showError);
      return;
    }
    if (target.dataset.mpCodexRefresh !== undefined) {
      loadProviderAuthFiles().catch(showError);
      return;
    }
    if (target.dataset.codexSave) {
      saveCodexAccount(target.dataset.codexSave, target).catch(showError);
      return;
    }
    if (target.dataset.codexSync) {
      syncCodexAccount(target.dataset.codexSync, target).catch(showError);
      return;
    }
    if (target.dataset.codexResetQuota) {
      resetCodexQuota(target.dataset.codexResetQuota, target).catch(showError);
      return;
    }
    if (target.dataset.codexExport) {
      exportCodexAccount(target.dataset.codexExport, target).catch(showError);
      return;
    }
    if (target.dataset.codexToggle) {
      toggleCodexAccount(target.dataset.codexToggle, target.dataset.disabled === "true", target).catch(showError);
      return;
    }
    if (target.dataset.codexDelete) {
      deleteCodexAccount(target.dataset.codexDelete, target).catch(showError);
      return;
    }
    if (target.dataset.mpCloseSubscriptionPage !== undefined) {
      closeProviderConsoleLayer();
      return;
    }
    if (target.dataset.subscriptionLoginStart) {
      startSubscriptionLogin(target.dataset.subscriptionLoginStart).catch(showError);
      return;
    }
    if (target.dataset.subscriptionLoginReopen) {
      reopenSubscriptionLogin(target.dataset.subscriptionLoginReopen).catch(showError);
      return;
    }
    if (target.dataset.subscriptionLoginCancel) {
      cancelSubscriptionLogin(target.dataset.subscriptionLoginCancel).catch(showError);
      return;
    }
    if (target.dataset.subscriptionCopyCode) {
      copySubscriptionDeviceCode(target.dataset.subscriptionCopyCode);
      return;
    }
    if (target.dataset.subscriptionRefresh) {
      refreshSubscriptionAccounts(target.dataset.subscriptionRefresh).catch(showError);
      return;
    }
    if (target.dataset.subscriptionEdit) {
      const provider = normalizeSubscriptionProvider(target.dataset.subscriptionProvider);
      const id = target.dataset.subscriptionEdit;
      const account = subscriptionAccountById(provider, id);
      if (!provider || !account) return;
      const page = subscriptionPage(provider);
      if (page) page.edit = { id, alias: String(account.alias || ""), priority: finiteNumber(account.priority, 100) };
      refreshProviderConsole();
      scheduleProviderConsoleFocus(() => [...(providerConsoleEventRoot?.querySelectorAll?.("[data-subscription-edit-alias]") || [])].find((node) => node.dataset?.subscriptionEditAlias === id)?.focus?.());
      return;
    }
    if (target.dataset.subscriptionEditCancel) {
      const provider = normalizeSubscriptionProvider(target.dataset.subscriptionProvider);
      const page = provider ? subscriptionPage(provider) : null;
      if (page?.edit?.id === target.dataset.subscriptionEditCancel) page.edit = null;
      refreshProviderConsole();
      return;
    }
    if (target.dataset.subscriptionSave) {
      saveSubscriptionAccount(normalizeSubscriptionProvider(target.dataset.subscriptionProvider), target.dataset.subscriptionSave, target).catch(showError);
      return;
    }
    if (target.dataset.subscriptionSync) {
      syncSubscriptionAccount(normalizeSubscriptionProvider(target.dataset.subscriptionProvider), target.dataset.subscriptionSync, target).catch(showError);
      return;
    }
    if (target.dataset.subscriptionToggle) {
      toggleSubscriptionAccount(normalizeSubscriptionProvider(target.dataset.subscriptionProvider), target.dataset.subscriptionToggle, target.dataset.disabled === "true", target).catch(showError);
      return;
    }
    if (target.dataset.subscriptionDelete) {
      deleteSubscriptionAccount(normalizeSubscriptionProvider(target.dataset.subscriptionProvider), target.dataset.subscriptionDelete, target).catch(showError);
      return;
    }
    if (target.dataset.kiroSubmit !== undefined) {
      submitKiroLogin("kiro", target).catch(showError);
      return;
    }
  }

  function bindProviderSettingsActions() {
    const root = $("settingsContentBody");
    if (!root) return;
    if (providerConsoleEventRoot !== root) {
      if (providerConsoleEventRoot) {
        providerConsoleEventRoot.removeEventListener("click", handleProviderConsoleClick);
        providerConsoleEventRoot.removeEventListener("focusin", handleProviderConsoleFocus);
        providerConsoleEventRoot.removeEventListener("focusout", handleProviderConsoleFocusOut);
        providerConsoleEventRoot.removeEventListener("input", handleProviderConsoleInput);
        providerConsoleEventRoot.removeEventListener("change", handleProviderConsoleChange);
        providerConsoleEventRoot.removeEventListener("dragover", handleProviderConsoleDragOver);
        providerConsoleEventRoot.removeEventListener("dragleave", handleProviderConsoleDragLeave);
        providerConsoleEventRoot.removeEventListener("drop", handleProviderConsoleDrop);
        providerConsoleEventRoot.removeEventListener("keydown", handleProviderConsoleKeydown);
        providerConsoleEventRoot.removeEventListener("submit", handleProviderConsoleSubmit);
      }
      providerConsoleEventRoot = root;
      root.addEventListener("click", handleProviderConsoleClick);
      root.addEventListener("focusin", handleProviderConsoleFocus);
      root.addEventListener("focusout", handleProviderConsoleFocusOut);
      root.addEventListener("input", handleProviderConsoleInput);
      root.addEventListener("change", handleProviderConsoleChange);
      root.addEventListener("dragover", handleProviderConsoleDragOver);
      root.addEventListener("dragleave", handleProviderConsoleDragLeave);
      root.addEventListener("drop", handleProviderConsoleDrop);
      root.addEventListener("keydown", handleProviderConsoleKeydown);
      root.addEventListener("submit", handleProviderConsoleSubmit);
    }
    if (providerConsoleState().view === "providers") {
      loadOfficialProviderAccountOverview().catch(showError);
    }
  }

  function allModelOptions() {
    return selectableModelProviders().flatMap((provider) => providerModelList(provider).map((model) => ({ provider, model, value: modelOptionValue(provider, model) })));
  }

  function providerLabel(provider) {
    if (provider.type === "codex" || provider.name === "codex") return "Codex OAuth";
    if (provider.profile === "cliproxyapi") return "CLIProxyAPI";
    if (provider.type === "openai-compatible" && provider.profile === "") return mt("relay");
    return provider.name;
  }

  function providerStatusText(provider) {
    if (provider.error) return mt("needsConfiguration");
    if (provider.configured) return mt("ready");
    return mt("unconfigured");
  }

  function providerCapabilitiesLabel(provider) {
    const capabilities = provider.capabilities || {};
    const labels = [];
    if (capabilities.streaming) labels.push(mt("streaming"));
    if (capabilities.tools) labels.push(mt("tools"));
    if (capabilities.imageInput) labels.push(mt("image"));
    return labels.length ? labels.join(" / ") : mt("basic");
  }

  async function applyPreferredModel(model) {
    if (state.modelApplying) return;
    const seq = ++state.modelApplySeq;
    const value = String(model || "").trim();
    const previousAgent = state.agent;
    let agentId = "";
    state.modelApplying = true;
    setModelApplyButtonsBusy(true);
    try {
      agentId = state.agent?.id || "";
      if (agentId && value && value !== state.agent.model) {
        const updated = await requestAPI(`/api/agents/${agentId}/model`, { method: "PATCH", body: JSON.stringify({ model: value }) });
        if (seq !== state.modelApplySeq || state.agent?.id !== agentId) return;
        state.agent = updated;
      }
      if (seq !== state.modelApplySeq) return;
      setPreferredModel(value);
      renderModelOptions();
      refreshActiveSettingsPanel?.();
      notifyTerminal?.(value ? `[info] ${mt("usingModel", { model: value })}\n` : `[info] ${mt("clearedPreferredModel")}\n`);
    } catch (err) {
      if (agentId && state.agent?.id === agentId && previousAgent?.id === agentId) state.agent = previousAgent;
      renderModelOptions();
      if (!agentId || state.agent?.id === agentId) throw err;
    } finally {
      if (seq === state.modelApplySeq) state.modelApplying = false;
      setModelApplyButtonsBusy(false);
    }
  }

  function getPreferredModel() {
    const value = getPreferredModelPreference?.();
    return String(value ?? preferredModelFallback ?? "").trim();
  }

  function setPreferredModel(model) {
    const value = String(model || "").trim();
    preferredModelFallback = value;
    setPreferredModelPreference?.(value);
    return value;
  }

  function loadModelVisibilityPreferences() {
    const raw = getModelVisibilityPreference?.() || modelVisibilityFallback;
    return {
      hiddenModels: raw?.hiddenModels && typeof raw.hiddenModels === "object" ? { ...raw.hiddenModels } : {},
      showUnconfiguredProviders: Boolean(raw?.showUnconfiguredProviders),
    };
  }

  function saveModelVisibilityPreferences(prefs) {
    modelVisibilityFallback = {
      hiddenModels: { ...(prefs?.hiddenModels || {}) },
      showUnconfiguredProviders: Boolean(prefs?.showUnconfiguredProviders),
    };
    setModelVisibilityPreference?.(modelVisibilityFallback);
    return modelVisibilityFallback;
  }

  async function persistModelVisibilityPreferences(prefs) {
    modelVisibilityFallback = {
      hiddenModels: { ...(prefs?.hiddenModels || {}) },
      showUnconfiguredProviders: Boolean(prefs?.showUnconfiguredProviders),
    };
    await Promise.resolve(setModelVisibilityPreference?.(modelVisibilityFallback));
    return modelVisibilityFallback;
  }

  function modelVisibilityPreferences() {
    return loadModelVisibilityPreferences();
  }

  function modelOptionValue(provider, model) {
    return `${provider.name}:${model}`;
  }

  function isModelHidden(value) {
    return Boolean(modelVisibilityPreferences().hiddenModels?.[value]);
  }

  function providerRuntimeSelectable(provider) {
    const runtimeProvider = provider && typeof provider === "object" ? provider : {};
    const signals = [runtimeProvider.runtimeAvailable, runtimeProvider.registered]
      .filter((value) => value !== undefined && value !== null)
      .map(Boolean);
    if (signals.length) return signals.every(Boolean);
    return Boolean(runtimeProvider.enabled && runtimeProvider.configured);
  }

  function isModelSelectable(provider, model) {
    const prefs = modelVisibilityPreferences();
    if (!provider.enabled || !providerRuntimeSelectable(provider)) return false;
    return !prefs.hiddenModels?.[modelOptionValue(provider, model)];
  }

  function setModelHidden(value, hidden) {
    const prefs = modelVisibilityPreferences();
    const hiddenModels = { ...(prefs.hiddenModels || {}) };
    if (hidden) hiddenModels[value] = true;
    else delete hiddenModels[value];
    saveModelVisibilityPreferences({ ...prefs, hiddenModels });
    renderModelOptions();
    refreshActiveSettingsPanel?.();
  }

  // Resolves the provider an official account page's live model panel belongs
  // to, or "" when the click came from an editable provider draft instead.
  function accountModelFormProvider(target) {
    const form = target?.closest?.("[data-subscription-provider-config]")
      || target?.closest?.("[data-codex-provider-config]");
    return form ? String(form.elements?.name?.value || "").trim() : "";
  }

  function setProviderModelsHidden(providerName, hidden) {
    const provider = providerByName(providerName);
    if (!provider) return;
    const prefs = modelVisibilityPreferences();
    const hiddenModels = { ...(prefs.hiddenModels || {}) };
    providerModelList(provider).forEach((model) => {
      const key = modelOptionValue(provider, model);
      if (hidden) hiddenModels[key] = true;
      else delete hiddenModels[key];
    });
    saveModelVisibilityPreferences({ ...prefs, hiddenModels });
    renderModelOptions();
    refreshActiveSettingsPanel?.();
  }

  function clearVisibleConfiguredModelHides() {
    const prefs = modelVisibilityPreferences();
    const hiddenModels = { ...(prefs.hiddenModels || {}) };
    modelProvidersForUI().forEach((provider) => {
      if (!provider.configured) return;
      providerModelList(provider).forEach((model) => delete hiddenModels[modelOptionValue(provider, model)]);
    });
    saveModelVisibilityPreferences({ ...prefs, hiddenModels });
    renderModelOptions();
    refreshActiveSettingsPanel?.();
  }

  function selectableModelValues() {
    return allModelOptions().map((item) => item.value);
  }

  function selectedModelValue() {
    const values = selectableModelValues();
    const candidates = [
      $("modelSelect")?.value,
      state.agent?.model,
      getPreferredModel(),
      state.settings?.agent?.defaultModel,
      values[0],
    ].map((value) => String(value || "").trim()).filter(Boolean);
    return candidates.find((value) => values.includes(value)) || "";
  }

  function currentModelValue() {
    return state.agent?.model || selectedModelValue();
  }

  // The model the user has chosen but the server has not confirmed yet. Changing
  // the picker starts a PATCH and returns immediately, so for as long as that save
  // is in flight state.agent.model is still the previous model. Any repaint in
  // that window -- and an interrupt forces one, because it resyncs the live
  // snapshot -- wrote the old value straight back into the select, which read as
  // the picker undoing the user's choice on its own.
  //
  // Scoped to the agent the save belongs to, so a repaint after switching
  // conversations cannot apply a pending pick to the wrong one.
  function pendingModelSelection() {
    if (!state.agentSavePending && !state.agentSaving) return "";
    const snapshot = state.agentSaveSnapshot;
    if (!snapshot || !state.agent?.id || snapshot.agentId !== state.agent.id) return "";
    return String(snapshot.model || "").trim();
  }

  function renderModelOptions() {
    const select = $("modelSelect");
    if (!select) return;
    const providers = selectableModelProviders();
    const optionValues = [];
    const groups = providers.map((provider) => {
      const models = providerModelList(provider);
      const groupLabel = `${provider.name}${provider.error ? mt("groupNeedsRefresh") : ""}`;
      const options = models.map((model) => {
        const value = `${provider.name}:${model}`;
        optionValues.push(value);
        const suffix = provider.configured ? "" : mt("optionUnconfigured");
        return `<option value="${escapeAttr(value)}" data-provider="${escapeAttr(provider.name)}" data-configured="${provider.configured ? "true" : "false"}">${escapeHtml(model + suffix)}</option>`;
      }).join("");
      return `<optgroup label="${escapeAttr(groupLabel)}">${options}</optgroup>`;
    }).join("");
    const currentModel = pendingModelSelection() || currentModelValue();
    const currentOption = currentModel && !optionValues.includes(currentModel)
      ? `<option value="${escapeAttr(currentModel)}" data-configured="false" data-runtime-available="false" disabled>${escapeHtml(currentModel + mt("currentHidden"))}</option>`
      : "";
    // Rebuilding the select tears every option out and puts it back, which
    // reads as a flicker on the composer. Switching a conversation renders this
    // twice -- once from the list agent in enterAgent, once from the live
    // snapshot -- and the provider list is identical both times, so the second
    // pass was nothing but the flash. Compare against what was last written
    // rather than against select.innerHTML, which the browser reserializes.
    const markup = currentOption + (groups || `<option value="" data-configured="false">${escapeHtml(mt("modelsNotLoaded"))}</option>`);
    setHTMLIfChanged(select, markup);
    if (currentModel) {
      select.value = currentModel;
    }
    updateModelConfiguredState();
    updateWorkspaceMetaPills?.();
  }

  function settingProviderByName(name) {
    return (state.settings?.providers || []).find((provider) => provider.name === name) || null;
  }

  function modelProvidersForUI() {
    return modelProvidersForUIUnion(
      state.settings?.providers || [],
      state.modelCatalog?.providers || [],
    ).map(normalizeModelProvider);
  }

  function selectableModelProviders() {
    return modelProvidersForUI()
      .map((provider) => ({
        ...provider,
        models: providerModelList(provider).filter((model) => isModelSelectable(provider, model)),
      }))
      .filter((provider) => provider.models.length);
  }

  function normalizeModelProvider(provider) {
    const capabilities = provider.capabilities && typeof provider.capabilities === "object" ? provider.capabilities : {};
    const reasoningEfforts = [
      capabilities.reasoningEfforts,
      capabilities.reasoningEffortValues,
      capabilities.effortValues,
      Array.isArray(capabilities.reasoningEffort) ? capabilities.reasoningEffort : undefined,
      capabilities.reasoningEffort?.values,
      capabilities.reasoningEffort?.supportedValues,
    ].find(Array.isArray);
    const management = provider.management && typeof provider.management === "object" ? provider.management : {};
    const rawModelCapabilities = provider.modelCapabilities && typeof provider.modelCapabilities === "object" && !Array.isArray(provider.modelCapabilities)
      ? provider.modelCapabilities
      : {};
    const modelCapabilities = Object.fromEntries(Object.entries(rawModelCapabilities)
      .map(([model, value]) => [String(model || "").trim(), {
        ...(value && typeof value === "object" ? value : {}),
        fastMode: Boolean(value?.fastMode),
      }])
      .filter(([model]) => Boolean(model)));
    return {
      name: provider.name || provider.type || "provider",
      type: provider.type || provider.name || "provider",
      profile: provider.profile || "",
      baseUrl: provider.baseUrl || "",
      defaultModel: provider.defaultModel || provider.model || "",
      maxTokens: Number(provider.maxTokens || 0),
      models: Array.isArray(provider.models) ? provider.models.filter(Boolean) : [],
      modelsSource: String(provider.modelsSource || ""),
      discovered: Boolean(provider.discovered),
      available: Boolean(provider.available),
      runtimeAvailable: provider.runtimeAvailable === undefined || provider.runtimeAvailable === null ? undefined : Boolean(provider.runtimeAvailable),
      registered: provider.registered === undefined || provider.registered === null ? undefined : Boolean(provider.registered),
      configured: Boolean(provider.configured),
      enabled: provider.enabled === undefined ? Boolean(provider.configured) : Boolean(provider.enabled),
      origin: String(provider.origin || (isBuiltinProvider(provider) ? "builtin" : "custom")),
      apiKeyOptional: Boolean(provider.apiKeyOptional),
      apiKeyConfigured: Boolean(provider.apiKeyConfigured),
      apiKeyPersisted: Boolean(provider.apiKeyPersisted),
      apiKeyLastFive: String(provider.apiKeyLastFive || "").slice(-5),
      apiKeySource: ["stored", "environment", "runtime", "optional", "none", "stored_unavailable"].includes(String(provider.apiKeySource || "").toLowerCase())
        ? String(provider.apiKeySource).toLowerCase()
        : "none",
      capabilities: {
        tools: Boolean(capabilities.tools),
        streaming: Boolean(capabilities.streaming),
        imageInput: Boolean(capabilities.imageInput),
        reasoningEffort: capabilities.reasoningEffort === true || Array.isArray(reasoningEfforts),
        reasoningEfforts: Array.isArray(reasoningEfforts)
          ? reasoningEfforts.filter(Boolean)
          : undefined,
      },
      modelCapabilities,
      management: {
        url: management.url || provider.managementUrl || "",
        authFiles: Boolean(management.authFiles),
      },
      managementUrl: provider.managementUrl || management.url || "",
      error: provider.error || "",
    };
  }

  function providerModelList(provider) {
    if (provider.models.length) return provider.models;
    return provider.defaultModel ? [provider.defaultModel] : [];
  }

  function currentProviderConfig(modelValue = selectedModelValue()) {
    const [providerName] = String(modelValue || "").split(":");
    return modelProvidersForUI().find((provider) => provider.name === providerName)
      || (state.settings?.providers || []).find((provider) => provider.name === providerName)
      || null;
  }

  function isCurrentModelConfigured(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    const provider = currentProviderConfig(modelValue);
    if (provider) return Boolean(provider.configured && providerRuntimeSelectable(provider));
    // Collaborators never load /api/settings, so until /api/models returns there is
    // no client-side configured flag. Blocking send in that window replaced the
    // transcript with a setup card they cannot open.
    const catalogReady = state.modelCatalog != null;
    const settingsReady = Array.isArray(state.settings?.providers);
    return !catalogReady && !settingsReady;
  }

  function updateModelConfiguredState() {
    const select = $("modelSelect");
    if (!select) return;
    const provider = currentProviderConfig(select.value);
    const configured = Boolean(provider?.configured && providerRuntimeSelectable(provider));
    select.classList.toggle("model-unconfigured", !configured);
    select.title = provider?.error || (configured
      ? mt("modelConfigured")
      : provider && !providerRuntimeSelectable(provider)
        ? mt("runtimeUnavailable")
        : modelSetupMessage(select.value));
  }

  function modelSetupMessage(modelValue = $("modelSelect")?.value || state.agent?.model || "") {
    const provider = currentProviderConfig(modelValue);
    const providerName = provider?.name || String(modelValue || "openai").split(":")[0] || "openai";
    if (providerName === "codex") {
      return mt("codexModelSetupMessage", { model: modelValue || mt("noneSelected") });
    }
    if (provider?.error) {
      return `${provider.error} ${mt("configurationRefresh")}`;
    }
    const envByProvider = {
      openai: "OPENAI_API_KEY",
      anthropic: "ANTHROPIC_API_KEY",
      groq: "GROQ_API_KEY",
      cliproxyapi: mt("envCliproxyApiKey"),
      "openai-compatible": mt("envOpenAICompatibleApiKey"),
    };
    const envName = envByProvider[providerName] || mt("envFallback");
    return mt("modelSetupMessage", {
      model: modelValue || mt("noneSelected"),
      provider: providerName,
      envName,
    });
  }

  function codexProvider() {
    return modelProvidersForUI().find((provider) => provider.type === "codex" || provider.name === "codex")
      || null;
  }

  function codexProviderSummary() {
    const provider = codexProvider();
    if (!provider) return mt("codexProviderMissing");
    const count = providerModelList(normalizeModelProvider(provider)).length;
    if (provider.error) return mt("codexProviderError", { error: provider.error, count });
    if (!provider.configured) return mt("codexProviderNeedsCredentials", { count });
    return mt("codexProviderConnected", { count });
  }

  function renderAgentModelOptions(currentModel) {
    const options = allModelOptions();
    const values = new Set(options.map((item) => item.value));
    const currentOption = currentModel && !values.has(currentModel)
      ? `<option value="${escapeAttr(currentModel)}" disabled>${escapeHtml(currentModel + mt("currentHidden"))}</option>`
      : "";
    const grouped = selectableModelProviders().map((provider) => {
      const models = providerModelList(provider);
      if (!models.length) return "";
      return `<optgroup label="${escapeAttr(providerLabel(provider))}">${models.map((model) => {
        const value = `${provider.name}:${model}`;
        const suffix = provider.configured ? "" : mt("optionUnconfigured");
        return `<option value="${escapeAttr(value)}" ${value === currentModel ? "selected" : ""}>${escapeHtml(model + suffix)}</option>`;
      }).join("")}</optgroup>`;
    }).join("");
    return currentOption + (grouped || `<option value="${escapeAttr(currentModel || "")}">${escapeHtml(currentModel || mt("modelsNotLoaded"))}</option>`);
  }


  return {
    bindModelSettingsActions,
    bindProviderSettingsActions,
    codexProviderSummary,
    currentModelValue,
    currentProviderConfig,
    getPreferredModel,
    isCurrentModelConfigured,
    loadProviderAuthFiles,
    loadOfficialProviderAccountOverview,
    modelSetupMessage,
    openProviderConsoleType,
    resetProviderConsoleToProviderList,
    openGeminiConsolePage,
    openGrokConsolePage,
    openKimiConsolePage,
    openKiroConsolePage,
    renderGeminiConsolePage,
    renderGrokConsolePage,
    renderKimiConsolePage,
    renderKiroConsolePage,
    loadSubscriptionAccounts,
    refreshSubscriptionAccounts,
    startSubscriptionLogin,
    cancelSubscriptionLogin,
    reopenSubscriptionLogin,
    saveSubscriptionAccount,
    syncSubscriptionAccount,
    toggleSubscriptionAccount,
    deleteSubscriptionAccount,
    providerLabel,
    providerStatusText,
    refreshModelCatalog,
    relayProtocolSpec,
    renderAgentModelOptions,
    renderModelOptions,
    renderModelSettingsContent,
    renderProviderSettingsContent,
    selectedModelValue,
    setPreferredModel,
    saveConsoleProvider,
    setProviderConsoleResult,
    deleteConsoleProvider,
    discoverConsoleProviderModels,
    discardProviderConsoleDraft,
  };
}
