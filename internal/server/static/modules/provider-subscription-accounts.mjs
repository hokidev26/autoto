import { escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { currentUILocale, t } from "./i18n.mjs";
import { remoteAccessContext } from "./remote-access-capabilities.mjs";
import {
  createProviderDraft,
  renderProviderModelEditor,
  subscriptionProviderKind,
  subscriptionProviderKinds,
  subscriptionProviderSpec,
} from "./model-provider-components.mjs";
import {
  normalizeSubscriptionAccountList,
  normalizeSubscriptionLoginStatus,
  normalizeSubscriptionProvider,
  subscriptionAccountActionRequest,
  subscriptionAccountsListRequest,
  subscriptionOAuthLoginRequest,
  subscriptionKiroSubmitRequest,
  subscriptionKiroSubmitAPIKeyRequest,
  trustedSubscriptionAuthURL,
} from "./provider-settings-normalization.mjs";
import {
  finiteNumber,
  renderSubscriptionAccountManagementTable,
  subscriptionAccountOverview,
} from "./provider-account-rendering.mjs";

const subscriptionLoginActiveStatuses = new Set(["starting", "pending", "exchanging"]);

// Creates the Gemini/Grok/Kimi subscription account controller. Each provider is
// an independent official entry with an independent management page; the three
// share code here but never a merged UI surface, and their runtime state is kept
// in per-provider maps so one login can never mutate another provider's page.
export function createSubscriptionAccountsController(ctx) {
  const {
    state,
    requestAPI,
    notifyTerminal,
    refreshActiveSettingsPanel,
    refreshProviderConsole,
    providerConsoleState,
    setProviderConsoleResult,
    loadModelCatalog,
    modelProvidersForUI,
    isModelHidden,
    modelOptionValue,
    copyText,
  } = ctx;
  const st = (key, params) => t(`modelProvider.subscription.common.${key}`, params);
  const pt = (provider, key, params) => t(`modelProvider.subscription.${provider}.${key}`, params);

  function ensureSubscriptionState() {
    const existingAccounts = state.subscriptionAccounts && typeof state.subscriptionAccounts === "object"
      ? state.subscriptionAccounts
      : null;
    state.subscriptionAccounts ||= {};
    state.subscriptionAccountsLoaded ||= Object.fromEntries(subscriptionProviderKinds.map((provider) => [provider, Boolean(existingAccounts && Object.hasOwn(existingAccounts, provider))]));
    state.subscriptionAccountsLoading ||= {};
    state.subscriptionAccountsError ||= {};
    state.subscriptionAccountsSeq ||= {};
    state.subscriptionAccountBusy ||= {};
    for (const provider of subscriptionProviderKinds) {
      if (!Object.hasOwn(state.subscriptionAccounts, provider)) state.subscriptionAccounts[provider] = [];
      if (!Object.hasOwn(state.subscriptionAccountsLoaded, provider)) state.subscriptionAccountsLoaded[provider] = false;
      if (!Object.hasOwn(state.subscriptionAccountsLoading, provider)) state.subscriptionAccountsLoading[provider] = false;
      if (!Object.hasOwn(state.subscriptionAccountsError, provider)) state.subscriptionAccountsError[provider] = "";
      if (!Object.hasOwn(state.subscriptionAccountsSeq, provider)) state.subscriptionAccountsSeq[provider] = 0;
      if (!state.subscriptionAccountBusy[provider] || typeof state.subscriptionAccountBusy[provider] !== "object") state.subscriptionAccountBusy[provider] = {};
    }
    return state;
  }

  function subscriptionPages() {
    const consoleState = providerConsoleState();
    const existing = consoleState.subscriptionPages && typeof consoleState.subscriptionPages === "object"
      ? consoleState.subscriptionPages
      : {};
    for (const provider of subscriptionProviderKinds) {
      const page = existing[provider] && typeof existing[provider] === "object" ? existing[provider] : {};
      if (!page.login || typeof page.login !== "object") page.login = idleLogin();
      if (!("edit" in page)) page.edit = null;
      existing[provider] = page;
    }
    consoleState.subscriptionPages = existing;
    return existing;
  }

  function subscriptionPage(provider) {
    return subscriptionPages()[provider];
  }

  function idleLogin() {
    return { seq: 0, loginId: "", status: "idle", authUrl: "", userCode: "", verificationUri: "", expiresAt: "", message: "", account: null, popupBlocked: false };
  }

  function subscriptionLoginActive(status) {
    return subscriptionLoginActiveStatuses.has(String(status || "").toLowerCase());
  }

  function accountsFor(provider) {
    ensureSubscriptionState();
    return normalizeSubscriptionAccountList(state.subscriptionAccounts[provider]);
  }

  function accountsLoaded(provider) {
    const p = normalizeSubscriptionProvider(provider);
    if (!p) return false;
    ensureSubscriptionState();
    return Boolean(state.subscriptionAccountsLoaded[p]);
  }

  function subscriptionAccountById(provider, id) {
    const target = String(id || "");
    return accountsFor(provider).find((account) => String(account?.id || account?.name || "") === target) || null;
  }

  async function loadSubscriptionAccounts(provider, { silent = false } = {}) {
    const p = normalizeSubscriptionProvider(provider);
    if (!p) return false;
    ensureSubscriptionState();
    const seq = (state.subscriptionAccountsSeq[p] || 0) + 1;
    state.subscriptionAccountsSeq[p] = seq;
    state.subscriptionAccountsLoading[p] = true;
    if (providerConsoleState().view === p) refreshActiveSettingsPanel?.();
    let loaded = false;
    try {
      const request = subscriptionAccountsListRequest(p);
      const response = await requestAPI(request.path, request.options);
      if (seq !== state.subscriptionAccountsSeq[p]) return false;
      state.subscriptionAccounts[p] = normalizeSubscriptionAccountList(response);
      state.subscriptionAccountsLoaded[p] = true;
      state.subscriptionAccountsError[p] = "";
      loaded = true;
    } catch (error) {
      if (seq !== state.subscriptionAccountsSeq[p]) return false;
      state.subscriptionAccountsError[p] = error?.message || st("unknown");
      if (!silent) notifyTerminal?.(`[warn] ${st("loadFailed", { message: state.subscriptionAccountsError[p] })}\n`);
    } finally {
      if (seq === state.subscriptionAccountsSeq[p]) {
        state.subscriptionAccountsLoading[p] = false;
        refreshActiveSettingsPanel?.();
      }
    }
    return loaded && seq === state.subscriptionAccountsSeq[p];
  }

  function refreshSubscriptionAccounts(provider) {
    return loadSubscriptionAccounts(provider, { silent: false });
  }

  async function runSubscriptionAccountAction(provider, id, button, busyLabel, action) {
    const p = normalizeSubscriptionProvider(provider);
    if (!p || !id) return;
    ensureSubscriptionState();
    const busyMap = state.subscriptionAccountBusy[p];
    if (busyMap[id]) return;
    busyMap[id] = true;
    setProviderConsoleResult("");
    setButtonBusy(button, true, busyLabel);
    refreshProviderConsole();
    try {
      await action();
      const refreshed = await loadSubscriptionAccounts(p, { silent: true });
      if (!refreshed) setProviderConsoleResult(st("mutationRefreshFailed", { message: state.subscriptionAccountsError[p] || st("unknown") }), "attention");
    } finally {
      delete busyMap[id];
      setButtonBusy(button, false, busyLabel);
      refreshProviderConsole();
    }
  }

  async function saveSubscriptionAccount(provider, id, button) {
    const page = subscriptionPage(provider);
    const edit = page?.edit;
    if (!edit || edit.id !== id) return;
    const priority = Number(edit.priority);
    if (!Number.isInteger(priority) || priority < 1 || priority > 1000000) throw new Error(st("invalidPriority"));
    return runSubscriptionAccountAction(provider, id, button, st("saving"), async () => {
      const request = subscriptionAccountActionRequest("save", provider, id, { alias: String(edit.alias || "").trim(), priority });
      await requestAPI(request.path, request.options);
      page.edit = null;
      setProviderConsoleResult(st("accountSaved"), "success");
    });
  }

  async function syncSubscriptionAccount(provider, id, button) {
    return runSubscriptionAccountAction(provider, id, button, st("syncing"), async () => {
      const request = subscriptionAccountActionRequest("sync", provider, id);
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(st("accountSynced"), "success");
    });
  }

  async function toggleSubscriptionAccount(provider, id, disabled, button) {
    return runSubscriptionAccountAction(provider, id, button, st("saving"), async () => {
      const request = subscriptionAccountActionRequest("toggle", provider, id, { disabled });
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(st(disabled ? "accountEnabled" : "accountDisabled"), "success");
    });
  }

  async function deleteSubscriptionAccount(provider, id, button) {
    const p = normalizeSubscriptionProvider(provider);
    if (!p) return;
    ensureSubscriptionState();
    if (state.subscriptionAccountBusy[p]?.[id] || !(await platformConfirm(pt(p, "deleteConfirm")))) return;
    return runSubscriptionAccountAction(p, id, button, st("deleting"), async () => {
      const request = subscriptionAccountActionRequest("delete", p, id);
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(st("accountDeleted"), "success");
    });
  }

  function subscriptionLoginState(provider) {
    return subscriptionPage(provider)?.login || idleLogin();
  }

  function subscriptionLoginAccountLabel(account) {
    if (!account || typeof account !== "object") return "";
    return String(account.alias || account.email || account.id || "").trim();
  }

  function openSubscriptionAuthURL(provider, authUrl) {
    if (!trustedSubscriptionAuthURL(provider, authUrl)) throw new Error(st("loginInvalidURL"));
    try {
      const opened = globalThis.open?.(authUrl, "_blank", "noopener,noreferrer");
      return Boolean(opened);
    } catch {
      return false;
    }
  }

  async function finishSubscriptionLogin(provider, status, seq) {
    const p = normalizeSubscriptionProvider(provider);
    const login = subscriptionLoginState(p);
    if (seq !== login.seq) return;
    const terminal = normalizeSubscriptionLoginStatus(status);
    Object.assign(login, terminal, { seq, popupBlocked: false });
    if (terminal.status === "completed") {
      const account = subscriptionLoginAccountLabel(terminal.account) || st("accountFallback");
      const message = st("loginCompleted", { account });
      setProviderConsoleResult(message, "success");
      notifyTerminal?.(`[info] ${message}\n`);
      const refreshFailures = [];
      const accountsLoaded = await loadSubscriptionAccounts(p, { silent: true });
      if (!accountsLoaded && state.subscriptionAccountsError[p]) refreshFailures.push(state.subscriptionAccountsError[p]);
      try {
        await loadModelCatalog?.();
        const quotaSnapshotLoaded = await loadSubscriptionAccounts(p, { silent: true });
        if (!quotaSnapshotLoaded && state.subscriptionAccountsError[p]) refreshFailures.push(state.subscriptionAccountsError[p]);
      } catch (error) {
        refreshFailures.push(error?.message || st("unknown"));
      }
      if (refreshFailures.length) {
        setProviderConsoleResult(st("loginRefreshWarning", { message: [...new Set(refreshFailures)].join("; ") }), "attention");
      }
    } else if (terminal.status === "cancelled") {
      setProviderConsoleResult(st("loginCancelled"), "info");
    } else if (terminal.status === "expired") {
      setProviderConsoleResult(st("loginExpired"), "attention");
    } else {
      setProviderConsoleResult(st("loginFailed", { message: terminal.message || st("unknown") }), "attention");
    }
    refreshProviderConsole();
  }

  async function pollSubscriptionLogin(provider, loginId, seq) {
    const p = normalizeSubscriptionProvider(provider);
    for (;;) {
      await new Promise((resolve) => globalThis.setTimeout(resolve, 1000));
      const login = subscriptionLoginState(p);
      if (seq !== login.seq || login.loginId !== loginId) return;
      const request = subscriptionOAuthLoginRequest("status", p, loginId);
      let status;
      try {
        status = normalizeSubscriptionLoginStatus(await requestAPI(request.path, request.options));
      } catch (error) {
        if (seq !== subscriptionLoginState(p).seq) return;
        await finishSubscriptionLogin(p, { loginId, status: "failed", message: error?.message || st("unknown") }, seq);
        return;
      }
      if (status.loginId && status.loginId !== loginId) return;
      Object.assign(login, status, { loginId, seq, authUrl: status.authUrl || login.authUrl });
      refreshProviderConsole();
      if (subscriptionLoginActive(status.status)) continue;
      await finishSubscriptionLogin(p, status, seq);
      return;
    }
  }

  async function startSubscriptionLogin(provider) {
    const p = normalizeSubscriptionProvider(provider);
    if (!p) return;
    const login = subscriptionLoginState(p);
    if (remoteAccessContext(state)) {
      setProviderConsoleResult(st("loginLocalOnly"), "attention");
      refreshProviderConsole();
      return;
    }
    if (subscriptionLoginActive(login.status) && login.authUrl) {
      openSubscriptionAuthURL(p, login.authUrl);
      return;
    }
    const seq = Number(login.seq || 0) + 1;
    Object.assign(login, idleLogin(), { seq, status: "starting" });
    setProviderConsoleResult("");
    refreshProviderConsole();
    try {
      const request = subscriptionOAuthLoginRequest("start", p, "", currentUILocale());
      const status = normalizeSubscriptionLoginStatus(await requestAPI(request.path, request.options));
      if (seq !== subscriptionLoginState(p).seq) return;
      if (!status.loginId) throw new Error(st("loginStartFailed", { message: st("unknown") }));
      const active = subscriptionLoginActive(status.status);
      const spec = subscriptionProviderSpec(p);
      let popupBlocked = false;
      if (active && spec?.loginKind === "browser" && status.authUrl) {
        popupBlocked = !openSubscriptionAuthURL(p, status.authUrl);
      }
      Object.assign(login, status, { seq, loginId: status.loginId, status: status.status || "pending", popupBlocked });
      refreshProviderConsole();
      if (!subscriptionLoginActive(login.status)) {
        await finishSubscriptionLogin(p, status, seq);
        return;
      }
      await pollSubscriptionLogin(p, login.loginId, seq);
    } catch (error) {
      if (seq !== subscriptionLoginState(p).seq) return;
      Object.assign(login, { status: "failed", message: error?.message || st("unknown"), popupBlocked: false });
      setProviderConsoleResult(error?.status === 403 ? st("loginLocalOnly") : st("loginStartFailed", { message: login.message }), "attention");
      refreshProviderConsole();
    }
  }

  async function cancelSubscriptionLogin(provider) {
    const p = normalizeSubscriptionProvider(provider);
    const login = subscriptionLoginState(p);
    if (!login.loginId || !subscriptionLoginActive(login.status)) return;
    const seq = Number(login.seq || 0) + 1;
    login.seq = seq;
    try {
      const request = subscriptionOAuthLoginRequest("cancel", p, login.loginId);
      const status = normalizeSubscriptionLoginStatus(await requestAPI(request.path, request.options));
      Object.assign(login, status, { seq, status: status.status || "cancelled", popupBlocked: false });
      setProviderConsoleResult(st("loginCancelled"), "info");
    } catch (error) {
      Object.assign(login, { seq, status: "failed", message: error?.message || st("unknown"), popupBlocked: false });
      setProviderConsoleResult(st("loginFailed", { message: login.message }), "attention");
    }
    refreshProviderConsole();
  }

  function reopenSubscriptionLogin(provider) {
    const p = normalizeSubscriptionProvider(provider);
    const login = subscriptionLoginState(p);
    if (!login.authUrl || !subscriptionLoginActive(login.status)) return;
    if (!openSubscriptionAuthURL(p, login.authUrl)) {
      login.popupBlocked = true;
      setProviderConsoleResult(st("loginPopupBlocked"), "attention");
      refreshProviderConsole();
    }
  }

  function copySubscriptionDeviceCode(provider) {
    const login = subscriptionLoginState(normalizeSubscriptionProvider(provider));
    if (!login.userCode) return;
    copyText?.(login.userCode);
    setProviderConsoleResult(st("deviceCodeCopied"), "success");
    refreshProviderConsole();
  }

  function renderSubscriptionLoginStatusText(status) {
    switch (String(status || "").toLowerCase()) {
      case "starting": return st("loginStarting");
      case "pending": return st("loginPending");
      case "exchanging": return st("loginExchanging");
      case "cancelled": return st("loginCancelled");
      case "expired": return st("loginExpired");
      case "failed": return st("loginFailed", { message: "" });
      default: return "";
    }
  }

  function renderSubscriptionLoginPanel(spec, login) {
    const provider = spec.provider;
    const active = subscriptionLoginActive(login.status);
    const statusText = renderSubscriptionLoginStatusText(login.status);
    const statusLine = active ? `<p class="subscription-login-status" role="status" aria-live="polite">${escapeHtml(statusText)}</p>` : "";
    if (spec.loginKind === "device") {
      if (active && login.userCode) {
        const verification = login.verificationUri || login.authUrl;
        return `<div class="subscription-login-device">
          <p class="anthropic-secret-note">${escapeHtml(st("verificationHint"))}</p>
          <div class="subscription-device-code"><span>${escapeHtml(st("deviceCodeLabel"))}</span><code data-subscription-device-code="${escapeAttr(provider)}">${escapeHtml(login.userCode)}</code><button class="settings-action-btn subtle" type="button" data-subscription-copy-code="${escapeAttr(provider)}">${escapeHtml(st("copyDeviceCode"))}</button></div>
          <div class="settings-inline-actions">${verification ? `<button class="settings-action-btn primary" type="button" data-subscription-login-reopen="${escapeAttr(provider)}">${escapeHtml(st("openVerification"))}</button>` : ""}<button class="settings-action-btn subtle" type="button" data-subscription-login-cancel="${escapeAttr(provider)}">${escapeHtml(st("loginCancelAction"))}</button></div>
          ${statusLine}
        </div>`;
      }
      return `<div class="subscription-login-idle"><p class="anthropic-secret-note">${escapeHtml(pt(provider, "loginIntro"))}</p><div class="settings-inline-actions"><button class="settings-action-btn primary" type="button" data-subscription-login-start="${escapeAttr(provider)}" ${active ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(active ? st("loginStarting") : pt(provider, "loginButton"))}</button></div>${statusLine}</div>`;
    }
    if (spec.loginKind === "token") {
      const page = subscriptionPage(spec.provider);
      const tokenInput = page?.kiroToken || "";
      const regionInput = page?.kiroRegion || "us-east-1";
      const apiKeyInput = page?.kiroApiKey || "";
      const submitBusy = Boolean(page?.kiroSubmitting) || active;
      // Always show the token form — no need to start a session first.
      return `<div class="subscription-login-token">
        <div class="subscription-token-fields">
          <label class="settings-label">${escapeHtml(pt(spec.provider, "tokenLabel"))}</label>
          <input class="settings-text-input" type="password" data-kiro-token-input placeholder="${escapeAttr(pt(spec.provider, "tokenPlaceholder"))}" value="${escapeAttr(tokenInput)}" autocomplete="off" />
          <label class="settings-label">${escapeHtml(pt(spec.provider, "regionLabel"))}</label>
          <select class="settings-select" data-kiro-region-input>
            ${["us-east-1","us-west-2","eu-central-1","ap-northeast-1","ap-southeast-1"].map(r => `<option value="${escapeAttr(r)}" ${r === regionInput ? "selected" : ""}>${escapeHtml(r)}</option>`).join("")}
          </select>
          <div class="settings-divider-label">${escapeHtml(pt(spec.provider, "orApiKey"))}</div>
          <label class="settings-label">${escapeHtml(pt(spec.provider, "apiKeyLabel"))}</label>
          <input class="settings-text-input" type="password" data-kiro-apikey-input placeholder="${escapeAttr(pt(spec.provider, "apiKeyPlaceholder"))}" value="${escapeAttr(apiKeyInput)}" autocomplete="off" />
        </div>
        <div class="settings-inline-actions">
          <button class="settings-action-btn primary" type="button" data-kiro-submit ${submitBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(submitBusy ? st("loginExchanging") : pt(spec.provider, "submitButton"))}</button>
        </div>
        ${statusLine}
      </div>`;
    }
    if (active) {
      const blocked = login.popupBlocked ? `<div class="settings-alert attention" role="alert">${escapeHtml(st("loginPopupBlocked"))}</div>` : "";
      return `<div class="subscription-login-browser">
        <p class="anthropic-secret-note">${escapeHtml(st("browserHint"))}</p>
        ${blocked}
        <div class="settings-inline-actions"><button class="settings-action-btn primary" type="button" data-subscription-login-reopen="${escapeAttr(provider)}">${escapeHtml(st("loginReopen"))}</button><button class="settings-action-btn subtle" type="button" data-subscription-login-cancel="${escapeAttr(provider)}">${escapeHtml(st("loginCancelAction"))}</button></div>
        ${statusLine}
      </div>`;
    }
    return `<div class="subscription-login-idle"><p class="anthropic-secret-note">${escapeHtml(pt(provider, "loginIntro"))}</p><div class="settings-inline-actions"><button class="settings-action-btn primary" type="button" data-subscription-login-start="${escapeAttr(provider)}" ${active ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(active ? st("loginStarting") : pt(provider, "loginButton"))}</button></div>${statusLine}</div>`;
  }

  // renderSubscriptionConsolePage renders exactly one provider page. It never
  // renders provider tabs, a switcher, or other providers' account lists.
  function renderSubscriptionConsolePage(kind) {
    const spec = subscriptionProviderSpec(kind);
    if (!spec) return "";
    const provider = spec.provider;
    ensureSubscriptionState();
    const page = subscriptionPage(provider);
    const accounts = accountsFor(provider);
    const overview = subscriptionAccountOverview(accounts);
    const loading = Boolean(state.subscriptionAccountsLoading[provider]);
    const error = state.subscriptionAccountsError[provider];
    const consoleState = providerConsoleState();
    const result = consoleState.result && typeof consoleState.result === "object"
      ? `<div class="codex-console-result settings-alert ${escapeAttr(consoleState.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(consoleState.result.message || "")}</div>`
      : "";
    const accountAlert = error ? `<div class="settings-alert attention" role="alert">${escapeHtml(st("loadFailed", { message: error }))}</div>` : "";
    const accountContent = loading && !accounts.length
      ? `<div class="codex-console-loading settings-empty-card compact" role="status">${escapeHtml(st("loadingAccounts"))}</div>`
      : renderSubscriptionAccountManagementTable(provider, accounts, { translate: st, emptyText: pt(provider, "empty"), editing: page.edit, busy: state.subscriptionAccountBusy[provider] || {} });
    const loginActive = subscriptionLoginActive(page.login.status);
    const loginButtonLabel = accounts.length ? pt(provider, "loginAnother") : pt(provider, "loginButton");
    return `<div class="subscription-account-console codex-account-console settings-page ${escapeAttr(spec.className)}" data-subscription-view="${escapeAttr(provider)}" tabindex="-1" aria-labelledby="subscription-${escapeAttr(provider)}-title">
      <button class="codex-console-back" type="button" data-mp-close-subscription-page="${escapeAttr(provider)}">← ${escapeHtml(st("back"))}</button>
      <header class="codex-console-hero settings-card">
        <div class="codex-console-heading"><div><p class="mp-provider-kicker">${escapeHtml(pt(provider, "kicker"))}</p><h1 id="subscription-${escapeAttr(provider)}-title" class="settings-card-title">${escapeHtml(pt(provider, "title"))}</h1><p class="settings-card-description" data-settings-help-copy>${escapeHtml(pt(provider, "description"))}</p></div></div>
        <div class="codex-console-actions settings-inline-actions"><button class="settings-action-btn primary" type="button" data-subscription-login-start="${escapeAttr(provider)}" ${loginActive ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(loginActive ? st("loginStarting") : loginButtonLabel)}</button><button class="settings-action-btn" type="button" data-subscription-refresh="${escapeAttr(provider)}" ${loading ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(loading ? st("refreshing") : st("refresh"))}</button></div>
      </header>
      <section class="codex-console-stats settings-stat-grid" aria-label="${escapeAttr(st("summary"))}">
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.total))}</strong><span>${escapeHtml(st("totalAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.available))}</strong><span>${escapeHtml(st("availableAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.expired))}</strong><span>${escapeHtml(st("limitedAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.disabled))}</strong><span>${escapeHtml(st("disabledAccounts"))}</span></div>
      </section>
      ${result}
      <section class="subscription-login-panel settings-card" aria-labelledby="subscription-${escapeAttr(provider)}-login-title">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="subscription-${escapeAttr(provider)}-login-title" class="settings-card-title">${escapeHtml(pt(provider, "loginButton"))}</h2></div></div>
        <div class="subscription-login-body settings-card-content">${renderSubscriptionLoginPanel(spec, page.login)}</div>
      </section>
      <section class="codex-accounts-panel settings-card" aria-labelledby="subscription-${escapeAttr(provider)}-accounts-title" aria-busy="${loading ? "true" : "false"}">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="subscription-${escapeAttr(provider)}-accounts-title" class="settings-card-title">${escapeHtml(pt(provider, "accountsTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(pt(provider, "accountsDescription"))}</p></div><span class="settings-badge">${escapeHtml(st("accountCount", { count: accounts.length }))}</span></div>
        ${accountAlert}${accountContent}
      </section>
      ${renderSubscriptionModelPanel(spec, accounts.length > 0)}
    </div>`;
  }

  // The configured provider instance for a subscription kind, found by type
  // rather than by name: the user may have saved it under any name (for example
  // "gemini-oauth") and it is still the same platform.
  function subscriptionProviderConfig(kind) {
    const providers = typeof modelProvidersForUI === "function" ? modelProvidersForUI() : [];
    return (Array.isArray(providers) ? providers : []).find((item) => subscriptionProviderKind(item) === kind) || null;
  }

  // Lets the user see which models an account can actually use, and re-fetch the
  // list from upstream after signing in. The form wrapper is what the console's
  // generic data-mp-fetch-models handler looks for.
  function renderSubscriptionModelPanel(spec, hasAccounts) {
    const kind = spec.provider;
    const config = subscriptionProviderConfig(kind);
    if (!config) return "";
    const consoleState = providerConsoleState();
    // Hidden state comes from the shared visibility preference, which is what
    // filters the composer's model picker. These pages have no draft/save cycle,
    // so the preference is authoritative and is applied explicitly: createProviderDraft
    // already normalizes every model to hidden:false, and normalizeProviderModelConfigs
    // only consults the preference when hidden is undefined, so layering the two would
    // silently keep every model visible.
    const baseDraft = createProviderDraft(config.name, config);
    const draft = typeof isModelHidden === "function" && typeof modelOptionValue === "function"
      ? {
        ...baseDraft,
        modelConfigs: (baseDraft.modelConfigs || []).map((item) => ({
          ...item,
          hidden: isModelHidden(modelOptionValue(config, item.name)),
        })),
      }
      : baseDraft;
    const modelBusy = Boolean(consoleState.busy?.[`models:${config.name}`]);
    const provider = spec.provider;
    const note = hasAccounts ? "" : `<p class="anthropic-secret-note">${escapeHtml(st("modelsNeedAccount"))}</p>`;
    return `<section class="subscription-model-panel settings-card" aria-labelledby="subscription-${escapeAttr(provider)}-models-title">
      <div class="codex-console-section-head settings-card-header"><div><h2 id="subscription-${escapeAttr(provider)}-models-title" class="settings-card-title">${escapeHtml(st("modelsTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(st("modelsDescription"))}</p></div></div>
      <form class="subscription-model-form settings-card-content" data-mp-provider-form data-subscription-provider-config="${escapeAttr(provider)}">
        <input type="hidden" name="name" value="${escapeAttr(config.name)}"><input type="hidden" name="type" value="${escapeAttr(config.type || kind)}"><input type="hidden" name="baseUrl" value="${escapeAttr(config.baseUrl || "")}"><input type="hidden" name="apiKey" value=""><input type="checkbox" name="apiKeyOptional" checked hidden>
        ${note}
        <div class="subscription-model-manager">${renderProviderModelEditor(draft, modelBusy, true, { allowEmpty: true })}</div>
      </form>
    </section>`;
  }

  async function submitKiroLogin(provider, button) {
    const page = subscriptionPage(provider);
    if (!page) return;
    const token = String(page.kiroToken || "").trim();
    const apiKey = String(page.kiroApiKey || "").trim();
    const region = String(page.kiroRegion || "us-east-1").trim();
    if (!token && !apiKey) {
      setProviderConsoleResult(pt(provider, "submitMissingToken"), "attention");
      refreshProviderConsole();
      return;
    }
    page.kiroSubmitting = true;
    setButtonBusy(button, true, st("loginExchanging"));
    refreshProviderConsole();
    try {
      // Start a fresh login session if none is active.
      let login = subscriptionLoginState(provider);
      if (!login.loginId || !subscriptionLoginActive(login.status)) {
        const startRequest = subscriptionOAuthLoginRequest("start", provider, "", currentUILocale());
        const startStatus = normalizeSubscriptionLoginStatus(await requestAPI(startRequest.path, startRequest.options));
        if (!startStatus.loginId) throw new Error(st("loginStartFailed", { message: st("unknown") }));
        const seq = Number(login.seq || 0) + 1;
        Object.assign(login, startStatus, { seq, loginId: startStatus.loginId, status: startStatus.status || "pending" });
        login = subscriptionLoginState(provider);
      }
      if (!login.loginId) throw new Error(st("loginStartFailed", { message: st("unknown") }));

      let request;
      if (apiKey && apiKey.startsWith("ksk_")) {
        request = subscriptionKiroSubmitAPIKeyRequest(login.loginId, apiKey);
      } else {
        request = subscriptionKiroSubmitRequest(login.loginId, token, region);
      }
      const status = normalizeSubscriptionLoginStatus(await requestAPI(request.path, request.options));
      const seq = login.seq;
      Object.assign(login, status, { loginId: login.loginId, seq });
      if (!subscriptionLoginActive(status.status)) {
        await finishSubscriptionLogin(provider, status, seq);
      }
    } catch (error) {
      const login = subscriptionLoginState(provider);
      Object.assign(login, { status: "failed", message: error?.message || st("unknown") });
      setProviderConsoleResult(st("loginFailed", { message: login.message }), "attention");
    } finally {
      page.kiroSubmitting = false;
      setButtonBusy(button, false, st("loginExchanging"));
      refreshProviderConsole();
    }
  }

  function handleKiroInputChange(e) {
    const target = e.target;
    if (!target) return;
    const page = subscriptionPage("kiro");
    if (!page) return;
    if (target.hasAttribute("data-kiro-token-input")) {
      page.kiroToken = target.value;
      return;
    }
    if (target.hasAttribute("data-kiro-region-input")) {
      page.kiroRegion = target.value;
      return;
    }
    if (target.hasAttribute("data-kiro-apikey-input")) {
      page.kiroApiKey = target.value;
      return;
    }
  }

  function handleKiroSubmitClick(e) {
    const target = e.target?.closest("[data-kiro-submit]");
    if (!target) return;
    submitKiroLogin("kiro", target);
  }

  return {
    ensureSubscriptionState,
    subscriptionPage,
    subscriptionPages,
    accountsFor,
    accountsLoaded,
    subscriptionAccountById,
    loadSubscriptionAccounts,
    refreshSubscriptionAccounts,
    saveSubscriptionAccount,
    syncSubscriptionAccount,
    toggleSubscriptionAccount,
    deleteSubscriptionAccount,
    startSubscriptionLogin,
    cancelSubscriptionLogin,
    reopenSubscriptionLogin,
    copySubscriptionDeviceCode,
    subscriptionLoginActive,
    submitKiroLogin,
    handleKiroInputChange,
    handleKiroSubmitClick,
    renderGeminiConsolePage: () => renderSubscriptionConsolePage("gemini"),
    renderGrokConsolePage: () => renderSubscriptionConsolePage("grok"),
    renderKimiConsolePage: () => renderSubscriptionConsolePage("kimi"),
    renderKiroConsolePage: () => renderSubscriptionConsolePage("kiro"),
  };
}

// finiteNumber is re-exported for parity with the Codex/Anthropic controllers'
// dependency surface in tests.
export { finiteNumber };
