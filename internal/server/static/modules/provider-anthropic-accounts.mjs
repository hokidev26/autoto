import { escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { confirm as platformConfirm, openExternal } from "./platform.mjs";
import { t } from "./i18n.mjs";
import {
  createProviderDraft,
  isAnthropicAccountProvider,
  normalizeConsoleProvider,
  normalizeProviderModelConfigs,
  renderProviderModelEditor,
  renderProviderClientPresentationFields,
} from "./model-provider-components.mjs";
import {
  anthropicAccountActionRequest,
  anthropicAccountsListRequest,
  anthropicProfileLoginCommand,
  consumeAnthropicAccountCreateRequest,
  normalizeAnthropicAccountList,
} from "./provider-settings-normalization.mjs";
import {
  anthropicAccountOverview,
  renderAnthropicAccountManagementTable,
} from "./provider-account-rendering.mjs";

// Creates the Anthropic account controller: profile/API-key account creation,
// per-account save/sync/toggle/delete, and the dedicated account console page.
// `ctx` supplies the shared provider-console primitives via dependency injection.
export function createAnthropicAccountsController(ctx) {
  const {
    state,
    requestAPI,
    notifyTerminal,
    refreshActiveSettingsPanel,
    refreshProviderConsole,
    providerConsoleState,
    setProviderConsoleResult,
    loadModelCatalog,
    providerByName,
    modelProvidersForUI,
  } = ctx;
  const mt = (key, params) => t(`modelProvider.${key}`, params);
  const ct = (key, params) => t(`modelProvider.console.${key}`, params);

  async function loadAnthropicAccounts({ silent = false } = {}) {
    const seq = (state.anthropicAccountSeq || 0) + 1;
    state.anthropicAccountSeq = seq;
    let loaded = false;
    state.anthropicAccountsLoading = true;
    if (providerConsoleState().view === "anthropic") refreshActiveSettingsPanel?.();
    try {
      const request = anthropicAccountsListRequest();
      const response = await requestAPI(request.path, request.options);
      if (seq !== state.anthropicAccountSeq) return false;
      state.anthropicAccounts = normalizeAnthropicAccountList(response);
      state.anthropicAccountsLoaded = true;
      state.anthropicAccountsError = "";
      loaded = true;
    } catch (error) {
      if (seq !== state.anthropicAccountSeq) return false;
      state.anthropicAccountsError = error?.message || mt("unknown");
      if (!silent) notifyTerminal?.(`[warn] ${mt("anthropic.accountsLoadFailed", { message: state.anthropicAccountsError })}\n`);
    } finally {
      if (seq === state.anthropicAccountSeq) {
        state.anthropicAccountsLoading = false;
        refreshActiveSettingsPanel?.();
      }
    }
    return loaded && seq === state.anthropicAccountSeq;
  }

  function anthropicAccountById(id) {
    return normalizeAnthropicAccountList(state.anthropicAccounts).find((account) => String(account?.id || "") === String(id || "")) || null;
  }

  function anthropicAccountIsManaged(id) {
    const account = anthropicAccountById(id);
    return Boolean(account && account.managed !== false);
  }

  async function runAnthropicAccountAction(id, button, busyLabel, action) {
    if (!anthropicAccountIsManaged(id)) return;
    state.anthropicAccountBusy ||= {};
    if (!id || state.anthropicAccountBusy[id]) return;
    state.anthropicAccountBusy[id] = true;
    setProviderConsoleResult("");
    setButtonBusy(button, true, busyLabel);
    refreshProviderConsole();
    try {
      await action();
      const refreshed = await loadAnthropicAccounts({ silent: true });
      if (!refreshed) setProviderConsoleResult(mt("anthropic.mutationRefreshFailed", { message: state.anthropicAccountsError || mt("unknown") }), "attention");
    } finally {
      delete state.anthropicAccountBusy[id];
      setButtonBusy(button, false, busyLabel);
      refreshProviderConsole();
    }
  }

  async function createAnthropicAccount(form) {
    if (!form || state.anthropicAccountCreating) return;
    const authType = form.elements?.authType?.value === "api_key" ? "api_key" : "profile";
    const profile = String(form.elements?.profile?.value || "").trim();
    const apiKey = String(form.elements?.apiKey?.value || "").trim();
    const alias = String(form.elements?.alias?.value || "").trim();
    const priority = Number(form.elements?.priority?.value || 100);
    if (authType === "profile" && !profile) throw new Error(mt("anthropic.profileRequired"));
    if (authType === "api_key" && !apiKey) throw new Error(mt("anthropic.apiKeyRequired"));
    if (!Number.isInteger(priority) || priority < 1 || priority > 1000000) throw new Error(mt("invalidPriority"));
    const request = consumeAnthropicAccountCreateRequest(form);
    state.anthropicAccountCreating = true;
    refreshProviderConsole();
    try {
      await requestAPI(request.path, request.options);
      const consoleState = providerConsoleState();
      consoleState.anthropicProfile = "";
      consoleState.anthropicAlias = "";
      consoleState.anthropicPriority = 100;
      setProviderConsoleResult(mt("anthropic.accountAdded"), "success");
      notifyTerminal?.(`[info] ${mt("anthropic.accountAdded")}\n`);
      await loadAnthropicAccounts({ silent: true });
      await loadModelCatalog();
    } finally {
      state.anthropicAccountCreating = false;
      refreshProviderConsole();
    }
  }

  async function saveAnthropicAccount(id, button) {
    const edit = providerConsoleState().anthropicEdit;
    if (!edit || edit.id !== id) return;
    const priority = Number(edit.priority);
    if (!Number.isInteger(priority) || priority < 1 || priority > 1000000) throw new Error(mt("invalidPriority"));
    return runAnthropicAccountAction(id, button, mt("saving"), async () => {
      const request = anthropicAccountActionRequest("save", id, { alias: String(edit.alias || "").trim(), priority });
      await requestAPI(request.path, request.options);
      providerConsoleState().anthropicEdit = null;
      setProviderConsoleResult(mt("anthropic.accountSaved"), "success");
    });
  }

  async function syncAnthropicAccount(id, button) {
    return runAnthropicAccountAction(id, button, mt("syncing"), async () => {
      const request = anthropicAccountActionRequest("sync", id);
      await requestAPI(request.path, request.options);
      notifyTerminal?.(`[info] ${mt("anthropic.accountSynced")}\n`);
    });
  }

  async function toggleAnthropicAccount(id, disabled, button) {
    return runAnthropicAccountAction(id, button, mt("saving"), async () => {
      const request = anthropicAccountActionRequest("toggle", id, { disabled });
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(mt(disabled ? "anthropic.accountEnabled" : "anthropic.accountDisabled"), "success");
    });
  }

  async function deleteAnthropicAccount(id, button) {
    if (state.anthropicAccountBusy?.[id] || !(await platformConfirm(mt("anthropic.deleteConfirm")))) return;
    return runAnthropicAccountAction(id, button, mt("deleting"), async () => {
      const request = anthropicAccountActionRequest("delete", id);
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(mt("anthropic.accountDeleted"), "success");
    });
  }

  async function startAnthropicLogin() {
    const consoleState = providerConsoleState();
    if (state.anthropicLoginBusy) return;
    state.anthropicLoginBusy = true;
    setProviderConsoleResult("");
    refreshProviderConsole();
    try {
      const data = await requestAPI("/api/providers/oauth/anthropic/login/start", { method: "POST", body: JSON.stringify({}) });
      consoleState.anthropicLogin = { loginId: data?.loginId || "", authUrl: data?.authUrl || "", status: data?.status || "pending", error: "" };
      if (consoleState.anthropicLogin.authUrl) await openExternal(consoleState.anthropicLogin.authUrl);
    } catch (error) {
      consoleState.anthropicLogin = { loginId: "", authUrl: "", status: "failed", error: error?.message || mt("unknown") };
    } finally {
      state.anthropicLoginBusy = false;
      refreshProviderConsole();
    }
  }

  async function submitAnthropicLogin(code) {
    const consoleState = providerConsoleState();
    const login = consoleState.anthropicLogin;
    const value = String(code || "").trim();
    if (!login?.loginId || state.anthropicLoginBusy) return;
    if (!value) {
      login.error = mt("anthropic.oauthCodeRequired");
      refreshProviderConsole();
      return;
    }
    state.anthropicLoginBusy = true;
    login.error = "";
    refreshProviderConsole();
    try {
      const data = await requestAPI(`/api/providers/oauth/anthropic/login/${encodeURIComponent(login.loginId)}/submit`, { method: "POST", body: JSON.stringify({ code: value }) });
      if (data?.status === "completed") {
        consoleState.anthropicLogin = null;
        consoleState.anthropicAddMode = "profile";
        setProviderConsoleResult(mt("anthropic.oauthLoginSuccess"), "success");
        notifyTerminal?.(`[info] ${mt("anthropic.oauthLoginSuccess")}\n`);
        await loadAnthropicAccounts({ silent: true });
        await loadModelCatalog();
      } else {
        login.status = data?.status || "failed";
        login.error = data?.error || mt("anthropic.oauthLoginFailed");
      }
    } catch (error) {
      login.error = error?.message || mt("anthropic.oauthLoginFailed");
    } finally {
      state.anthropicLoginBusy = false;
      refreshProviderConsole();
    }
  }

  async function pasteAnthropicLogin() {
    if (state.anthropicLoginBusy) return;
    let text = "";
    try {
      text = String((await globalThis.navigator?.clipboard?.readText?.()) || "").trim();
    } catch {
      text = "";
    }
    if (!text) {
      const login = providerConsoleState().anthropicLogin;
      if (login) {
        login.error = mt("anthropic.oauthClipboardEmpty");
        refreshProviderConsole();
      }
      return;
    }
    await submitAnthropicLogin(text);
  }

  async function reopenAnthropicLogin() {
    const login = providerConsoleState().anthropicLogin;
    if (!login?.authUrl) return;
    await openExternal(login.authUrl);
  }

  async function cancelAnthropicLogin() {
    const consoleState = providerConsoleState();
    const login = consoleState.anthropicLogin;
    consoleState.anthropicLogin = null;
    refreshProviderConsole();
    if (login?.loginId) {
      try {
        await requestAPI(`/api/providers/oauth/anthropic/login/${encodeURIComponent(login.loginId)}`, { method: "DELETE" });
      } catch {
        // Best effort: the session also expires on its own.
      }
    }
  }

  function renderAnthropicOAuthLogin(consoleState) {
    const login = consoleState.anthropicLogin;
    const busy = Boolean(state.anthropicLoginBusy);
    if (!login || !login.loginId) {
      const err = login?.error ? `<div class="settings-alert attention" role="alert">${escapeHtml(login.error)}</div>` : "";
      return `${err}<p class="anthropic-secret-note">${escapeHtml(mt("anthropic.oauthIntro"))}</p>
        <div class="settings-inline-actions"><button class="settings-action-btn primary" type="button" data-anthropic-login-start ${busy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(busy ? mt("anthropic.oauthStarting") : mt("anthropic.oauthLoginButton"))}</button></div>`;
    }
    const err = login.error ? `<div class="settings-alert attention" role="alert">${escapeHtml(login.error)}</div>` : "";
    const reopen = login.authUrl ? `<button class="anthropic-oauth-reopen" type="button" data-anthropic-login-reopen>${escapeHtml(mt("anthropic.oauthReopen"))}</button>` : "";
    return `${err}
      <ol class="anthropic-oauth-flow">
        <li class="anthropic-oauth-step">
          <span class="anthropic-oauth-step-num" aria-hidden="true">1</span>
          <div class="anthropic-oauth-step-main">
            <p class="anthropic-oauth-step-title">${escapeHtml(mt("anthropic.oauthStep1Title"))}</p>
            <p class="anthropic-oauth-step-hint">${escapeHtml(mt("anthropic.oauthStep1Hint"))}</p>
            ${reopen}
          </div>
        </li>
        <li class="anthropic-oauth-step">
          <span class="anthropic-oauth-step-num" aria-hidden="true">2</span>
          <div class="anthropic-oauth-step-main">
            <p class="anthropic-oauth-step-title">${escapeHtml(mt("anthropic.oauthStep2Title"))}</p>
            <p class="anthropic-oauth-step-hint">${escapeHtml(mt("anthropic.oauthStep2Hint"))}</p>
            <button class="settings-action-btn primary anthropic-oauth-paste" type="button" data-anthropic-login-paste ${busy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(busy ? mt("saving") : mt("anthropic.oauthPasteButton"))}</button>
            <details class="anthropic-oauth-manual"><summary>${escapeHtml(mt("anthropic.oauthManualToggle"))}</summary>
              <label class="settings-form-field"><span>${escapeHtml(mt("anthropic.oauthCodeLabel"))}</span><input type="text" data-anthropic-login-code autocomplete="off" spellcheck="false" placeholder="${escapeAttr(mt("anthropic.oauthCodePlaceholder"))}"></label>
              <div class="settings-inline-actions"><button class="settings-action-btn" type="button" data-anthropic-login-submit ${busy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(busy ? mt("saving") : mt("anthropic.oauthComplete"))}</button></div>
            </details>
          </div>
        </li>
      </ol>
      <div class="anthropic-oauth-foot"><button class="settings-action-btn subtle" type="button" data-anthropic-login-cancel>${escapeHtml(mt("anthropic.oauthCancel"))}</button></div>`;
  }

  function renderAnthropicConsolePage() {
    const consoleState = providerConsoleState();
    const accounts = normalizeAnthropicAccountList(state.anthropicAccounts);
    const overview = anthropicAccountOverview(accounts);
    const provider = providerByName(consoleState.providerName || "anthropic") || modelProvidersForUI().find(isAnthropicAccountProvider) || normalizeConsoleProvider(consoleState.draft || createProviderDraft("anthropic"));
    const draft = createProviderDraft("anthropic", consoleState.draft || provider);
    const providerTone = provider?.error || !provider?.configured ? "warn" : provider?.enabled === false ? "muted" : "ok";
    const providerState = provider?.error ? mt("needsAttention") : provider?.enabled === false ? mt("disabled") : provider?.configured ? mt("ready") : mt("unconfigured");
    const mode = consoleState.anthropicAddMode === "api_key" ? "api_key" : consoleState.anthropicAddMode === "oauth" ? "oauth" : "profile";
    const loginCommand = anthropicProfileLoginCommand(consoleState.anthropicProfile);
    const result = consoleState.result && typeof consoleState.result === "object"
      ? `<div class="codex-console-result settings-alert ${escapeAttr(consoleState.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(consoleState.result.message || "")}</div>`
      : "";
    const accountAlert = state.anthropicAccountsError ? `<div class="settings-alert attention" role="alert">${escapeHtml(mt("anthropic.accountsLoadFailed", { message: state.anthropicAccountsError }))}</div>` : "";
    const accountContent = state.anthropicAccountsLoading && !accounts.length
      ? `<div class="codex-console-loading settings-empty-card compact" role="status">${escapeHtml(mt("anthropic.loadingAccounts"))}</div>`
      : renderAnthropicAccountManagementTable(accounts, { translate: mt, editing: consoleState.anthropicEdit, busy: state.anthropicAccountBusy || {} });
    const creating = Boolean(state.anthropicAccountCreating);
    const modelBusy = Boolean(consoleState.busy?.["models:anthropic"]);
    const saveBusy = Boolean(consoleState.busy?.["save:anthropic"]);
    const refreshBusy = Boolean(consoleState.busy?.refresh);
    return `<div class="anthropic-account-console codex-account-console settings-page" tabindex="-1" aria-labelledby="anthropic-console-title">
      <button class="codex-console-back" type="button" data-mp-close-anthropic-page>← ${escapeHtml(mt("backToProviders"))}</button>
      <header class="codex-console-hero anthropic-console-hero settings-card">
        <div class="codex-console-heading"><div><p class="mp-provider-kicker">Anthropic</p><h1 id="anthropic-console-title" class="settings-card-title">${escapeHtml(mt("anthropic.title"))}</h1><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("anthropic.description"))}</p></div></div>
        <div class="codex-console-actions settings-inline-actions"><span class="settings-status-pill ${escapeAttr(providerTone)}">${escapeHtml(providerState)}</span><button class="settings-action-btn primary" type="button" data-anthropic-focus-add>${escapeHtml(mt("anthropic.addAccount"))}</button><button class="settings-action-btn" type="button" data-anthropic-refresh ${state.anthropicAccountsLoading ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(state.anthropicAccountsLoading ? mt("refreshing") : mt("refreshAccounts"))}</button></div>
      </header>
      <section class="codex-console-stats settings-stat-grid" aria-label="${escapeAttr(mt("anthropic.summary"))}">
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.total))}</strong><span>${escapeHtml(mt("totalAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.available))}</strong><span>${escapeHtml(mt("availableAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.rateLimited))}</strong><span>${escapeHtml(mt("limitedAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.disabled))}</strong><span>${escapeHtml(mt("disabledAccounts"))}</span></div>
      </section>
      ${result}
      <section class="anthropic-add-panel settings-card" id="anthropic-add-account" aria-labelledby="anthropic-add-title">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="anthropic-add-title" class="settings-card-title">${escapeHtml(mt("anthropic.addAccount"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("anthropic.addDescription"))}</p></div></div>
        <div class="anthropic-add-body settings-card-content">
          <div class="anthropic-auth-tabs settings-inline-actions" role="group" aria-label="${escapeAttr(mt("anthropic.authType"))}"><button class="settings-action-btn ${mode === "oauth" ? "primary" : "subtle"}" type="button" data-anthropic-add-mode="oauth" aria-pressed="${mode === "oauth" ? "true" : "false"}">${escapeHtml(mt("anthropic.oauthAuth"))}</button><button class="settings-action-btn ${mode === "profile" ? "primary" : "subtle"}" type="button" data-anthropic-add-mode="profile" aria-pressed="${mode === "profile" ? "true" : "false"}">${escapeHtml(mt("anthropic.profileAuth"))}</button><button class="settings-action-btn ${mode === "api_key" ? "primary" : "subtle"}" type="button" data-anthropic-add-mode="api_key" aria-pressed="${mode === "api_key" ? "true" : "false"}">${escapeHtml(mt("anthropic.apiKeyAuth"))}</button></div>
          ${mode === "oauth" ? `<div class="anthropic-oauth-login">${renderAnthropicOAuthLogin(consoleState)}</div>` : `<form class="anthropic-account-form" data-anthropic-account-form aria-busy="${creating ? "true" : "false"}">
            <input type="hidden" name="authType" value="${escapeAttr(mode)}">
            <div class="anthropic-add-grid">
              ${mode === "profile" ? `<label class="settings-form-field"><span>${escapeHtml(mt("anthropic.profileName"))}</span><input name="profile" value="${escapeAttr(consoleState.anthropicProfile || "")}" autocomplete="off" placeholder="${escapeAttr(mt("anthropic.profilePlaceholder"))}" required data-anthropic-profile data-select-on-focus="true"></label>` : `<label class="settings-form-field"><span>${escapeHtml(mt("apiKey"))}</span><input name="apiKey" type="password" value="" autocomplete="new-password" placeholder="${escapeAttr(mt("anthropic.apiKeyPlaceholder"))}" required></label>`}
              <label class="settings-form-field"><span>${escapeHtml(mt("anthropic.alias"))}</span><input name="alias" value="${escapeAttr(consoleState.anthropicAlias || "")}" autocomplete="off" placeholder="${escapeAttr(mt("anthropic.aliasPlaceholder"))}" data-anthropic-alias data-select-on-focus="true"></label>
              <label class="settings-form-field"><span>${escapeHtml(mt("priority"))}</span><input name="priority" type="number" min="1" max="1000000" step="1" value="${escapeAttr(consoleState.anthropicPriority || 100)}" data-anthropic-priority data-select-on-focus="true"></label>
            </div>
            ${mode === "profile" ? `<div class="anthropic-profile-command"><div><span>${escapeHtml(mt("anthropic.loginCommand"))}</span><code data-anthropic-command>${escapeHtml(loginCommand)}</code></div><button class="settings-action-btn subtle" type="button" data-anthropic-copy-command="${escapeAttr(loginCommand)}">${escapeHtml(mt("anthropic.copyCommand"))}</button></div>` : `<p class="anthropic-secret-note">${escapeHtml(mt("anthropic.apiKeySafety"))}</p>`}
            <div class="settings-inline-actions"><button class="settings-action-btn primary" type="submit" ${creating ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(creating ? mt("saving") : mt("anthropic.addAccount"))}</button></div>
          </form>`}
        </div>
      </section>
      <section class="codex-accounts-panel settings-card" aria-labelledby="anthropic-accounts-title" aria-busy="${state.anthropicAccountsLoading ? "true" : "false"}">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="anthropic-accounts-title" class="settings-card-title">${escapeHtml(mt("anthropic.accountsTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("anthropic.accountsDescription"))}</p></div><span class="settings-badge">${escapeHtml(mt("accountCount", { count: accounts.length }))}</span></div>
        ${accountAlert}${accountContent}
      </section>
      <section class="anthropic-config-panel settings-card" aria-labelledby="anthropic-config-title">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="anthropic-config-title" class="settings-card-title">${escapeHtml(mt("anthropic.configTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("anthropic.configDescription"))}</p></div><span class="settings-status-pill ${escapeAttr(providerTone)}">${escapeHtml(providerState)}</span></div>
        <form class="anthropic-config-form settings-card-content" data-mp-provider-form data-anthropic-provider-config>
          <input type="hidden" name="name" value="anthropic"><input type="hidden" name="type" value="anthropic"><input type="hidden" name="apiKey" value=""><input type="checkbox" name="apiKeyOptional" hidden>
          ${renderProviderClientPresentationFields(draft)}
          <div class="anthropic-model-manager">${renderProviderModelEditor(draft, modelBusy, true, { allowEmpty: true })}</div>
          <p class="anthropic-secret-note">${escapeHtml(mt("anthropic.configNote"))}</p>
          <div class="anthropic-config-actions settings-inline-actions"><button class="settings-action-btn" type="button" data-mp-refresh-models ${refreshBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(refreshBusy ? mt("refreshing") : mt("refreshModels"))}</button><button class="settings-action-btn primary" type="submit" ${saveBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(saveBusy ? mt("saving") : ct("actions.saveAndEnable"))}</button></div>
        </form>
      </section>
    </div>`;
  }

  return {
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
  };
}
