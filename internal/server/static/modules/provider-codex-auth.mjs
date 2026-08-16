import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { apiDownload } from "./runtime.mjs";
import { formatNumber } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { remoteAccessContext } from "./remote-access-capabilities.mjs";
import { createProviderDraft, renderProviderModelEditor, renderProviderClientPresentationFields } from "./model-provider-components.mjs";
import {
  codexAccountActionRequest,
  codexAccountBatchRequest,
  codexAccountExportFilename,
  codexBrowserLoginRequest,
  codexDeleteResultWarning,
  codexImportBatchRequest,
  codexMutationRefreshWarning,
  normalizeCodexAccountList,
  normalizeCodexBatchResult,
  normalizeCodexBrowserLoginStatus,
  normalizeCodexImportBatchResult,
  normalizeCodexSelectedIds,
  trustedCodexBrowserAuthURL,
} from "./provider-settings-normalization.mjs";
import {
  codexAccountOverview,
  codexAccountStableID,
  finiteNumber,
  renderCodexAccountManagementTable,
} from "./provider-account-rendering.mjs";

const codexBrowserLoginActiveStatuses = new Set(["starting", "pending", "exchanging"]);
const maxCodexImportFiles = 50;
const maxCodexImportFileBytes = 2 << 20;
const maxCodexImportBatchBytes = 8 << 20;

// Creates the Codex OAuth account controller: import/export, browser login polling,
// and batch account operations. `ctx` supplies the shared provider-console primitives
// (state, network, and refresh/result helpers) via explicit dependency injection --
// no module-level singleton is introduced here.
export function createCodexAuthController(ctx) {
  const {
    state,
    requestAPI,
    notifyTerminal,
    refreshActiveSettingsPanel,
    refreshProviderConsole,
    providerConsoleState,
    setProviderConsoleResult,
    loadModelCatalog,
    providerLabel,
    providerStatusText,
    providerModelList,
    codexProvider,
    isModelHidden,
    modelOptionValue,
  } = ctx;
  const mt = (key, params) => t(`modelProvider.${key}`, params);

  async function loadProviderAuthFiles({ silent = false } = {}) {
    const seq = ++state.providerAuthSeq;
    const button = silent ? null : $("codexRefreshAuthBtn");
    let loaded = false;
    state.providerAuthLoading = true;
    setButtonBusy(button, true, mt("refreshing"));
    if (silent && providerConsoleState().view === "codex" && !extractAuthFiles(state.providerAuthFiles).length) refreshActiveSettingsPanel?.();
    try {
      const files = await requestAPI("/api/providers/oauth/codex/accounts");
      if (seq !== state.providerAuthSeq) return;
      state.providerAuthFiles = files;
      state.providerAuthLoaded = true;
      providerConsoleState().codexSelectedIds = normalizeCodexSelectedIds(providerConsoleState().codexSelectedIds, extractAuthFiles(files));
      state.providerAuthError = "";
      state.providerAuthMutationWarning = "";
      loaded = true;
    } catch (err) {
      if (seq !== state.providerAuthSeq) return;
      state.providerAuthError = err.message;
      if (!silent) notifyTerminal?.(`[warn] ${mt("authAccountsLoadFailed", { message: err.message })}\n`);
    } finally {
      if (seq === state.providerAuthSeq) {
        state.providerAuthLoading = false;
        setButtonBusy(button, false, mt("refreshing"));
      }
    }
    if (seq === state.providerAuthSeq) refreshActiveSettingsPanel?.();
    return loaded && seq === state.providerAuthSeq;
  }

  function codexImportFileAccepted(file) {
    return String(file?.name || "").toLowerCase().endsWith(".json");
  }

  function setCodexImportFiles(files) {
    const incoming = Array.from(files || []);
    if (!incoming.length) {
      providerConsoleState().codexImportFiles = [];
      providerConsoleState().codexImportResult = null;
      return true;
    }
    if (incoming.length > maxCodexImportFiles) {
      setProviderConsoleResult(mt("importTooManyFiles", { count: maxCodexImportFiles }), "attention");
      return false;
    }
    const totalBytes = incoming.reduce((sum, file) => {
      const size = Math.max(0, Number(file?.size || 0));
      return sum + (codexImportFileAccepted(file) && size <= maxCodexImportFileBytes ? size : 0);
    }, 0);
    if (totalBytes > maxCodexImportBatchBytes) {
      setProviderConsoleResult(mt("importBatchTooLarge"), "attention");
      return false;
    }
    providerConsoleState().codexImportFiles = incoming;
    providerConsoleState().codexImportResult = null;
    setProviderConsoleResult("");
    return true;
  }

  async function readCodexImportFile(file) {
    if (typeof file?.text === "function") return String(await file.text());
    if (typeof file?.arrayBuffer === "function") return new TextDecoder().decode(await file.arrayBuffer());
    return await new Promise((resolve, reject) => {
      const Reader = globalThis.FileReader;
      if (typeof Reader !== "function") {
        reject(new Error(mt("importFileReadFailed", { name: String(file?.name || mt("unknown")) })));
        return;
      }
      const reader = new Reader();
      reader.onload = () => resolve(String(reader.result || ""));
      reader.onerror = () => reject(reader.error || new Error(mt("importFileReadFailed", { name: String(file?.name || mt("unknown")) })));
      reader.readAsText(file);
    });
  }

  async function importCodexAuthFile() {
    const button = $("codexImportAuthBtn");
    if (button?.disabled) return;
    const textarea = $("codexAuthImportText");
    const fileInput = $("codexAuthImportFiles");
    const consoleState = providerConsoleState();
    const selectedFiles = Array.isArray(consoleState.codexImportFiles) ? consoleState.codexImportFiles : [];
    const draft = (textarea?.value || consoleState.codexImportDraft || "").trim();
    if (!selectedFiles.length && !draft) throw new Error(mt("importContentRequired"));

    setProviderConsoleResult("");
    consoleState.codexImportResult = null;
    setButtonBusy(button, true, mt("importing"));
    if (textarea) textarea.disabled = true;
    if (fileInput) fileInput.disabled = true;
    let imported = 0;
    let skipped = 0;
    let results = [];
    try {
      if (selectedFiles.length) {
        const prepared = [];
        const resultByFile = new Map();
        for (const file of selectedFiles) {
          const filename = String(file?.name || mt("unknown"));
          if (!codexImportFileAccepted(file)) {
            resultByFile.set(file, { filename, status: "failed", imported: 0, skipped: 0, error: mt("importInvalidFileType", { name: filename }) });
            continue;
          }
          if (Math.max(0, Number(file?.size || 0)) > maxCodexImportFileBytes) {
            resultByFile.set(file, { filename, status: "failed", imported: 0, skipped: 0, error: mt("importFileTooLarge", { name: filename }) });
            continue;
          }
          try {
            prepared.push({ filename, content: await readCodexImportFile(file), file });
          } catch (error) {
            resultByFile.set(file, { filename, status: "failed", imported: 0, skipped: 0, error: error?.message || mt("unknown") });
          }
        }
        if (prepared.length) {
          try {
            const request = codexImportBatchRequest(prepared);
            const response = normalizeCodexImportBatchResult(await requestAPI(request.path, request.options));
            imported = response.imported;
            skipped = response.skipped;
            prepared.forEach((item, index) => {
              const serverResult = response.results[index] || { filename: item.filename, status: "failed", imported: 0, skipped: 0, error: mt("unknown") };
              resultByFile.set(item.file, { ...serverResult, filename: serverResult.filename || item.filename });
            });
          } catch (error) {
            prepared.forEach((item) => resultByFile.set(item.file, {
              filename: item.filename,
              status: "failed",
              imported: 0,
              skipped: 0,
              error: error?.message || mt("unknown"),
            }));
          }
        }
        results = selectedFiles.map((file) => resultByFile.get(file) || {
          filename: String(file?.name || mt("unknown")),
          status: "failed",
          imported: 0,
          skipped: 0,
          error: mt("unknown"),
        });
        consoleState.codexImportFiles = selectedFiles.filter((file, index) => results[index]?.status === "failed");
      } else {
        consoleState.codexImportDraft = draft;
        try {
          const response = await requestAPI("/api/providers/oauth/codex/import", {
            method: "POST",
            body: JSON.stringify({ filename: "autoto-codex-auth.json", content: draft }),
          });
          imported = Math.max(0, Number(response?.imported || 0));
          skipped = Math.max(0, Number(response?.skipped || 0));
          const errors = Array.isArray(response?.errors) ? response.errors.map((error) => String(error || "")).filter(Boolean) : [];
          results = [{
            filename: "autoto-codex-auth.json",
            status: errors.length ? "failed" : imported > 0 ? "success" : "skipped",
            imported,
            skipped,
            error: errors.join("; "),
          }];
        } catch (error) {
          results = [{ filename: "autoto-codex-auth.json", status: "failed", imported: 0, skipped: 0, error: error?.message || mt("unknown") }];
        }
      }

      const failures = results.filter((item) => item.status === "failed");
      consoleState.codexImportResult = {
        imported,
        skipped,
        failed: failures.length,
        results,
        errors: failures.map((item) => ({ filename: item.filename, message: item.error })),
      };
      if (!selectedFiles.length && !failures.length) consoleState.codexImportDraft = "";
      const message = failures.length
        ? mt("importedCredentialsPartial", { count: imported, failed: failures.length })
        : mt("importedCredentialsCount", { count: imported, skipped });
      setProviderConsoleResult(message, failures.length ? "attention" : "success");
      notifyTerminal?.(`[${failures.length ? "warn" : "info"}] ${message}\n`);
      if (imported || skipped) await loadProviderAuthFiles({ silent: true });
      if (imported) await loadModelCatalog();
    } finally {
      setButtonBusy(button, false, mt("importing"));
      if (textarea?.isConnected) textarea.disabled = false;
      if (fileInput?.isConnected) fileInput.disabled = false;
      refreshProviderConsole();
    }
  }

  function codexBrowserLoginState() {
    return providerConsoleState().codexBrowserLogin;
  }

  function codexBrowserLoginActive(status = codexBrowserLoginState().status) {
    return codexBrowserLoginActiveStatuses.has(String(status || "").toLowerCase());
  }

  function codexBrowserLoginAccountLabel(account) {
    if (!account || typeof account !== "object") return "";
    return String(account.alias || account.email || account.name || account.account_id || account.accountId || "").trim();
  }

  function preopenCodexBrowserLoginWindow() {
    try {
      const popup = globalThis.open?.("about:blank", "autoto-codex-login", "popup,width=720,height=820");
      if (popup) popup.opener = null;
      return popup || null;
    } catch {
      return null;
    }
  }

  function openCodexBrowserAuthURL(authUrl, popup = null) {
    if (!trustedCodexBrowserAuthURL(authUrl)) throw new Error(mt("browserLoginInvalidURL"));
    try {
      if (popup && !popup.closed) {
        popup.location.replace(authUrl);
        return true;
      }
      const opened = globalThis.open?.(authUrl, "_blank", "noopener,noreferrer");
      return Boolean(opened);
    } catch {
      return false;
    }
  }

  async function finishCodexBrowserLogin(status, seq) {
    const login = codexBrowserLoginState();
    if (seq !== login.seq) return;
    const terminal = normalizeCodexBrowserLoginStatus(status);
    Object.assign(login, terminal, { seq, popupBlocked: false });
    const account = codexBrowserLoginAccountLabel(terminal.account) || mt("browserLoginAccountFallback");
    if (terminal.status === "completed") {
      const message = mt("browserLoginSuccess", { account });
      const refreshFailures = [];
      setProviderConsoleResult(message, "success");
      notifyTerminal?.(`[info] ${message}\n`);
      const accountID = String(terminal.account?.id || "").trim();
      if (accountID) {
        try {
          const request = codexAccountActionRequest("sync", accountID);
          await requestAPI(request.path, request.options);
        } catch (error) {
          refreshFailures.push(error?.message || mt("unknown"));
        }
      }
      const accountsLoaded = await loadProviderAuthFiles({ silent: true });
      if (!accountsLoaded && state.providerAuthError) refreshFailures.push(state.providerAuthError);
      try {
        await loadModelCatalog();
      } catch (error) {
        refreshFailures.push(error?.message || mt("unknown"));
      }
      if (refreshFailures.length) {
        const warning = mt("browserLoginRefreshWarning", { message: [...new Set(refreshFailures)].join("; ") });
        state.providerAuthMutationWarning = warning;
        notifyTerminal?.(`[warn] ${warning}\n`);
      }
    } else if (terminal.status === "cancelled") {
      setProviderConsoleResult(mt("browserLoginCancelled"), "info");
    } else if (terminal.status === "expired") {
      setProviderConsoleResult(mt("browserLoginExpired"), "attention");
    } else {
      setProviderConsoleResult(mt("browserLoginFailed", { message: terminal.message || mt("unknown") }), "attention");
    }
    refreshProviderConsole();
  }

  async function pollCodexBrowserLogin(loginId, seq) {
    for (;;) {
      await new Promise((resolve) => globalThis.setTimeout(resolve, 1000));
      const login = codexBrowserLoginState();
      if (seq !== login.seq || login.loginId !== loginId) return;
      const request = codexBrowserLoginRequest("status", loginId);
      let status;
      try {
        status = normalizeCodexBrowserLoginStatus(await requestAPI(request.path, request.options));
      } catch (error) {
        if (seq !== codexBrowserLoginState().seq) return;
        await finishCodexBrowserLogin({ loginId, status: "failed", message: error?.message || mt("unknown") }, seq);
        return;
      }
      if (status.loginId && status.loginId !== loginId) return;
      Object.assign(login, status, { loginId, seq, authUrl: status.authUrl || login.authUrl });
      refreshProviderConsole();
      if (codexBrowserLoginActive(status.status)) continue;
      await finishCodexBrowserLogin(status, seq);
      return;
    }
  }

  async function startCodexBrowserLogin() {
    const login = codexBrowserLoginState();
    if (remoteAccessContext(state)) {
      setProviderConsoleResult(mt("browserLoginLocalOnly"), "attention");
      refreshProviderConsole();
      return;
    }
    if (codexBrowserLoginActive(login.status) && login.authUrl) {
      openCodexBrowserAuthURL(login.authUrl);
      return;
    }
    const popup = preopenCodexBrowserLoginWindow();
    const seq = Number(login.seq || 0) + 1;
    Object.assign(login, {
      seq,
      loginId: "",
      status: "starting",
      authUrl: "",
      expiresAt: "",
      message: "",
      account: null,
      popupBlocked: !popup,
    });
    setProviderConsoleResult("");
    refreshProviderConsole();
    try {
      const request = codexBrowserLoginRequest("start");
      const status = normalizeCodexBrowserLoginStatus(await requestAPI(request.path, request.options));
      if (seq !== codexBrowserLoginState().seq) {
        popup?.close?.();
        return;
      }
      if (!status.loginId) throw new Error(mt("browserLoginStartFailed"));
      const active = codexBrowserLoginActive(status.status);
      if (active && !trustedCodexBrowserAuthURL(status.authUrl)) throw new Error(mt("browserLoginInvalidURL"));
      const opened = active ? openCodexBrowserAuthURL(status.authUrl, popup) : true;
      if (!active) popup?.close?.();
      Object.assign(login, status, {
        seq,
        loginId: status.loginId,
        status: status.status || "pending",
        popupBlocked: active && !opened,
      });
      refreshProviderConsole();
      if (!codexBrowserLoginActive(login.status)) {
        await finishCodexBrowserLogin(login, seq);
        return;
      }
      await pollCodexBrowserLogin(login.loginId, seq);
    } catch (error) {
      popup?.close?.();
      if (seq !== codexBrowserLoginState().seq) return;
      Object.assign(login, { status: "failed", message: error?.message || mt("unknown"), popupBlocked: false });
      setProviderConsoleResult(error?.status === 403 ? mt("browserLoginLocalOnly") : mt("browserLoginFailed", { message: login.message }), "attention");
      refreshProviderConsole();
    }
  }

  async function cancelCodexBrowserLogin() {
    const login = codexBrowserLoginState();
    if (!login.loginId || !codexBrowserLoginActive(login.status)) return;
    const seq = Number(login.seq || 0) + 1;
    login.seq = seq;
    try {
      const request = codexBrowserLoginRequest("cancel", login.loginId);
      const status = normalizeCodexBrowserLoginStatus(await requestAPI(request.path, request.options));
      Object.assign(login, status, { seq, status: status.status || "cancelled", popupBlocked: false });
      setProviderConsoleResult(mt("browserLoginCancelled"), "info");
    } catch (error) {
      Object.assign(login, { seq, status: "failed", message: error?.message || mt("unknown"), popupBlocked: false });
      setProviderConsoleResult(mt("browserLoginFailed", { message: login.message }), "attention");
    }
    refreshProviderConsole();
  }

  function reopenCodexBrowserLogin() {
    const login = codexBrowserLoginState();
    if (!login.authUrl || !codexBrowserLoginActive(login.status)) return;
    if (!openCodexBrowserAuthURL(login.authUrl)) {
      login.popupBlocked = true;
      setProviderConsoleResult(mt("browserLoginPopupBlocked"), "attention");
      refreshProviderConsole();
    }
  }

  async function runCodexAccountAction(id, button, busyLabel, action) {
    state.codexAccountBusy ||= {};
    if (!id || state.codexAccountBusy[id]) return;
    state.codexAccountBusy[id] = true;
    state.providerAuthMutationWarning = "";
    setProviderConsoleResult("");
    setButtonBusy(button, true, busyLabel);
    refreshProviderConsole();
    try {
      const actionResult = await action();
      const refreshed = await loadProviderAuthFiles({ silent: true });
      const warnings = [
        actionResult?.warning || "",
        codexMutationRefreshWarning(refreshed, state.providerAuthError, mt),
      ].filter(Boolean);
      state.providerAuthMutationWarning = warnings.join(" ");
      warnings.forEach((warning) => notifyTerminal?.(`[warn] ${warning}\n`));
    } finally {
      delete state.codexAccountBusy[id];
      setButtonBusy(button, false, busyLabel);
      refreshProviderConsole();
    }
  }

  async function saveCodexAccount(id, button) {
    const consoleState = providerConsoleState();
    const edit = consoleState.codexEdit;
    if (!edit || edit.id !== id) return;
    const alias = String(edit.alias || "").trim();
    const priority = Number(edit.priority);
    if (!Number.isInteger(priority) || priority < 1 || priority > 1000000) throw new Error(mt("invalidPriority"));
    return runCodexAccountAction(id, button, mt("saving"), async () => {
      const request = codexAccountActionRequest("save", id, { alias, priority });
      await requestAPI(request.path, request.options);
      consoleState.codexEdit = null;
      setProviderConsoleResult(mt("accountSaved"), "success");
      notifyTerminal?.(`[info] ${mt("accountSaved")}\n`);
    });
  }

  async function syncCodexAccount(id, button) {
    return runCodexAccountAction(id, button, mt("syncing"), async () => {
      const request = codexAccountActionRequest("sync", id);
      await requestAPI(request.path, request.options);
      notifyTerminal?.(`[info] ${mt("accountSynced")}\n`);
    });
  }

  async function resetCodexQuota(id, button) {
    if (state.codexAccountBusy?.[id] || !(await platformConfirm(mt("resetQuotaConfirm")))) return;
    return runCodexAccountAction(id, button, mt("resettingQuota"), async () => {
      const request = codexAccountActionRequest("resetQuota", id);
      await requestAPI(request.path, request.options);
      setProviderConsoleResult(mt("quotaReset"), "success");
      notifyTerminal?.(`[info] ${mt("quotaReset")}\n`);
    });
  }

  async function toggleCodexAccount(id, disabled, button) {
    return runCodexAccountAction(id, button, mt("saving"), async () => {
      const request = codexAccountActionRequest("toggle", id, { disabled });
      await requestAPI(request.path, request.options);
      notifyTerminal?.(`[info] ${mt(disabled ? "accountEnabled" : "accountDisabled")}\n`);
    });
  }

  function codexAccountById(id) {
    const target = String(id || "");
    return normalizeCodexAccountList(state.providerAuthFiles).find((account) => {
      const accountId = String(account?.id || account?.auth_index || account?.authIndex || account?.name || "");
      return accountId === target;
    }) || null;
  }

  async function exportCodexAccount(id, button) {
    state.codexAccountBusy ||= {};
    if (!id || state.codexAccountBusy[id] || !(await platformConfirm(mt("exportAccountConfirm")))) return;
    state.codexAccountBusy[id] = true;
    state.providerAuthMutationWarning = "";
    setProviderConsoleResult("");
    setButtonBusy(button, true, mt("exporting"));
    refreshProviderConsole();
    try {
      const request = codexAccountActionRequest("export", id);
      const response = await apiDownload(request.path, request.options);
      const blob = await response.blob();
      const objectURL = globalThis.URL?.createObjectURL?.(blob);
      if (!objectURL || !globalThis.document?.createElement) throw new Error(mt("accountExportFailed"));
      const link = globalThis.document.createElement("a");
      link.href = objectURL;
      link.download = codexAccountExportFilename(codexAccountById(id), id);
      link.hidden = true;
      try {
        globalThis.document.body?.appendChild(link);
        link.click();
      } finally {
        link.remove();
        const revoke = () => globalThis.URL?.revokeObjectURL?.(objectURL);
        if (typeof globalThis.setTimeout === "function") globalThis.setTimeout(revoke, 0);
        else revoke();
      }
      setProviderConsoleResult(mt("accountExported"), "success");
    } finally {
      delete state.codexAccountBusy[id];
      setButtonBusy(button, false, mt("exporting"));
      refreshProviderConsole();
    }
  }

  async function deleteCodexAccount(id, button) {
    if (state.codexAccountBusy?.[id] || !(await platformConfirm(mt("deleteAccountConfirm")))) return;
    return runCodexAccountAction(id, button, mt("deleting"), async () => {
      const request = codexAccountActionRequest("delete", id);
      const result = await requestAPI(request.path, request.options);
      const warning = codexDeleteResultWarning(result, mt);
      if (!warning) {
        setProviderConsoleResult(mt("accountDeleted"), "success");
        notifyTerminal?.(`[info] ${mt("accountDeleted")}\n`);
      }
      return { warning };
    });
  }

  async function runCodexBatchOperation(operation) {
    const consoleState = providerConsoleState();
    const accounts = extractAuthFiles(state.providerAuthFiles);
    const ids = normalizeCodexSelectedIds(consoleState.codexSelectedIds, accounts);
    if (!ids.length || consoleState.codexBatchBusy) return;
    const priority = Number(consoleState.codexBatchPriority);
    if (operation === "set_priority" && (!Number.isInteger(priority) || priority < 1 || priority > 1000000)) throw new Error(mt("invalidPriority"));
    if (operation === "sync" && !(await platformConfirm(mt("batchSyncConfirm", { count: ids.length })))) return;
    if (operation === "delete" && !(await platformConfirm(mt("batchDeleteConfirm", { count: ids.length })))) return;
    consoleState.codexBatchBusy = true;
    consoleState.codexEdit = null;
    state.providerAuthMutationWarning = "";
    state.codexAccountBusy ||= {};
    ids.forEach((id) => { state.codexAccountBusy[id] = true; });
    setProviderConsoleResult("");
    refreshProviderConsole();
    try {
      const request = codexAccountBatchRequest(operation, ids, { priority });
      const response = await requestAPI(request.path, request.options);
      const result = normalizeCodexBatchResult(response, ids);
      const message = result.failed
        ? mt("batchPartial", { success: result.success, failed: result.failed })
        : mt("batchSuccess", { count: result.success });
      if (result.failed) setProviderConsoleResult(message, "attention");
      notifyTerminal?.(`[${result.failed ? "warn" : "info"}] ${message}\n`);
      state.providerAuthMutationWarning = [...new Set(result.warnings)].join(" ");
      await loadProviderAuthFiles({ silent: true });
      consoleState.codexSelectedIds = normalizeCodexSelectedIds(result.failedIds, extractAuthFiles(state.providerAuthFiles));
    } finally {
      ids.forEach((id) => { delete state.codexAccountBusy[id]; });
      consoleState.codexBatchBusy = false;
      refreshProviderConsole();
    }
  }

  function renderCodexModelPanel(provider, hasAccounts) {
    const providerName = String(provider?.name || "codex").trim() || "codex";
    const header = `<div class="codex-console-section-head settings-card-header"><div><h2 id="codex-models-title" class="settings-card-title">${escapeHtml(mt("codexModels"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("codexModelsDescription"))}</p></div></div>`;
    if (!provider) {
      return `<section class="codex-model-panel settings-card" aria-labelledby="codex-models-title">${header}<div class="settings-card-content"><div class="settings-alert attention" role="alert">${escapeHtml(mt("codexProviderNotFound"))}</div></div></section>`;
    }
    const baseDraft = createProviderDraft(providerName, provider);
    const draft = typeof isModelHidden === "function" && typeof modelOptionValue === "function"
      ? {
        ...baseDraft,
        modelConfigs: (baseDraft.modelConfigs || []).map((item) => ({
          ...item,
          hidden: isModelHidden(modelOptionValue(provider, item.name)),
        })),
      }
      : baseDraft;
    const modelBusy = Boolean(providerConsoleState().busy?.refresh);
    const saveBusy = Boolean(providerConsoleState().busy?.[`save:${providerName}`]);
    const note = hasAccounts ? "" : `<p class="anthropic-secret-note">${escapeHtml(mt("codexModelsNeedAccount"))}</p>`;
    return `<section class="codex-model-panel settings-card" aria-labelledby="codex-models-title">
      ${header}
      <form class="codex-model-form settings-card-content" data-mp-provider-form data-codex-provider-config="${escapeAttr(providerName)}">
        <input type="hidden" name="name" value="${escapeAttr(providerName)}"><input type="hidden" name="type" value="${escapeAttr(provider.type || "codex")}"><input type="hidden" name="baseUrl" value="${escapeAttr(provider.baseUrl || "")}"><input type="hidden" name="apiKey" value="">
        ${note}
        ${renderProviderClientPresentationFields(draft)}
        <div class="codex-model-manager">${renderProviderModelEditor(draft, modelBusy, true, { allowEmpty: true, refreshModels: true })}</div>
        <div class="anthropic-config-actions settings-inline-actions"><button class="settings-action-btn primary" type="submit" ${saveBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(t("modelProvider.console.actions.saveAndEnable"))}</button></div>
      </form>
    </section>`;
  }

  function renderCodexConsolePage() {
    const consoleState = providerConsoleState();
    const authFiles = extractAuthFiles(state.providerAuthFiles);
    const overview = codexAccountOverview(authFiles);
    const provider = codexProvider();
    const modelRefreshBusy = Boolean(consoleState.busy?.refresh);
    const providerTone = provider?.error || !provider?.configured ? "warn" : provider?.enabled === false ? "muted" : "ok";
    const providerState = provider?.error
      ? mt("needsAttention")
      : provider?.enabled === false
        ? mt("disabled")
        : provider?.configured
          ? mt("ready")
          : mt("unconfigured");
    const result = consoleState.result && typeof consoleState.result === "object"
      ? `<div class="codex-console-result settings-alert ${escapeAttr(consoleState.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(consoleState.result.message || "")}</div>`
      : "";
    const accountAlert = state.providerAuthMutationWarning
      ? `<div class="settings-alert attention" role="status" aria-live="polite">${escapeHtml(state.providerAuthMutationWarning)}</div>`
      : state.providerAuthError
        ? `<div class="settings-alert attention" role="alert">${escapeHtml(state.providerAuthError)}</div>`
        : "";
    const browserLogin = consoleState.codexBrowserLogin;
    const browserLoginActive = codexBrowserLoginActive(browserLogin.status);
    const browserLoginLocalOnly = remoteAccessContext(state);
    const browserLoginStatusKey = {
      starting: "browserLoginStatusStarting",
      pending: "browserLoginStatusWaiting",
      exchanging: "browserLoginStatusExchanging",
      completed: "browserLoginStatusCompleted",
      failed: "browserLoginStatusFailed",
      cancelled: "browserLoginStatusCancelled",
      expired: "browserLoginStatusExpired",
    }[browserLogin.status] || "";
    const browserLoginPanel = `
      <section class="codex-browser-login-panel settings-card" aria-labelledby="codex-browser-login-title" aria-busy="${browserLoginActive ? "true" : "false"}">
        <div class="codex-console-section-head settings-card-header">
          <div><h2 id="codex-browser-login-title" class="settings-card-title">${escapeHtml(mt("browserLoginTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("browserLoginDescription"))}</p></div>
          <span class="settings-badge codex-browser-login-recommended">${escapeHtml(mt("browserLoginRecommended"))}</span>
        </div>
        <div class="codex-browser-login-body settings-card-content">
          <div class="codex-browser-login-copy"><strong>${escapeHtml(mt("browserLoginAccountOnly"))}</strong><p>${escapeHtml(browserLoginLocalOnly ? mt("browserLoginLocalOnly") : mt("browserLoginSafety"))}</p></div>
          <div class="codex-browser-login-actions settings-inline-actions">
            ${browserLoginStatusKey ? `<span class="settings-status-pill ${browserLogin.status === "completed" ? "ok" : browserLogin.status === "failed" || browserLogin.status === "expired" ? "warn" : "muted"}" role="status" aria-live="polite">${escapeHtml(mt(browserLoginStatusKey))}</span>` : ""}
            ${browserLoginActive && browserLogin.authUrl ? `<button class="settings-action-btn" type="button" data-mp-codex-browser-open>${escapeHtml(mt("browserLoginOpen"))}</button>` : ""}
            ${browserLoginActive ? `<button class="settings-action-btn subtle" type="button" data-mp-codex-browser-cancel>${escapeHtml(mt("browserLoginCancel"))}</button>` : `<button class="settings-action-btn primary" type="button" data-mp-codex-browser-login ${browserLoginLocalOnly ? "disabled" : ""}>${escapeHtml(mt(browserLogin.status === "completed" ? "browserLoginAddAnother" : "browserLoginAction"))}</button>`}
          </div>
          ${browserLogin.popupBlocked ? `<div class="settings-alert attention codex-browser-login-alert" role="alert">${escapeHtml(mt("browserLoginPopupBlocked"))}</div>` : ""}
        </div>
      </section>`;
    const importFiles = Array.isArray(consoleState.codexImportFiles) ? consoleState.codexImportFiles : [];
    const importResult = consoleState.codexImportResult && typeof consoleState.codexImportResult === "object" ? consoleState.codexImportResult : null;
    const importFileList = importFiles.length
      ? `<ul class="codex-import-file-list">${importFiles.map((file) => `<li class="codex-import-file-row"><span>${escapeHtml(String(file?.name || mt("unknown")))}</span><small>${escapeHtml(formatNumber(Math.max(0, Number(file?.size || 0))))} B</small></li>`).join("")}</ul>`
      : "";
    const importResultRows = Array.isArray(importResult?.results) ? importResult.results : [];
    const importResultPanel = importResult
      ? `<div class="codex-import-result ${importResult.failed ? "has-errors" : "is-success"}" role="status"><strong>${escapeHtml(mt("importResultSummary", importResult))}</strong>${importResultRows.length ? `<ul class="codex-import-file-list">${importResultRows.map((item) => {
        const status = item?.status === "success" || item?.status === "skipped" ? item.status : "failed";
        const tone = status === "success" ? "success" : status === "skipped" ? "warning" : "danger";
        const detail = status === "success"
          ? mt("importFileImported", { count: Math.max(0, Number(item?.imported || 0)), skipped: Math.max(0, Number(item?.skipped || 0)) })
          : status === "skipped"
            ? mt("importFileSkipped", { count: Math.max(0, Number(item?.skipped || 0)) })
            : String(item?.error || mt("unknown"));
        return `<li class="codex-import-file-row"><span>${escapeHtml(item?.filename || mt("unknown"))}</span><span class="codex-import-file-status ${tone}">${escapeHtml(mt(`importFileStatus${status[0].toUpperCase()}${status.slice(1)}`))}</span><div class="codex-import-file-detail">${escapeHtml(detail)}</div></li>`;
      }).join("")}</ul>` : ""}</div>`
      : "";
    const importOpen = Boolean(consoleState.codexImportDraft || importFiles.length || importResult);
    const importPanel = `
      <details class="codex-import-panel settings-card" id="codexCredentialImportSection"${importOpen ? " open" : ""}>
        <summary class="codex-console-section-head settings-card-header codex-import-summary">
          <div><h2 id="codex-import-title" class="settings-card-title">${escapeHtml(mt("codexImportTitle"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("codexImportDescription"))}</p></div>
        </summary>
        <div class="codex-import-body settings-card-content">
          <div class="codex-import-dropzone" data-codex-import-drop>
            <input id="codexAuthImportFiles" class="hidden" type="file" accept=".json" multiple data-codex-import-files>
            <div><strong>${escapeHtml(importFiles.length ? mt("selectedJsonFiles", { count: importFiles.length }) : mt("chooseJsonFiles"))}</strong><p>${escapeHtml(mt("chooseJsonFilesHint"))}</p></div>
            <div class="settings-inline-actions"><button class="settings-action-btn" type="button" data-codex-choose-import-files>${escapeHtml(mt("chooseFile"))}</button>${importFiles.length ? `<button class="settings-action-btn subtle" type="button" data-codex-clear-import-files>${escapeHtml(mt("clearFiles"))}</button>` : ""}</div>
          </div>
          ${importFileList}
          <div class="codex-import-divider"><span>${escapeHtml(mt("importOrPaste"))}</span></div>
          <label class="settings-form-field"><span class="mp-visually-hidden">${escapeHtml(mt("codexImportTitle"))}</span><textarea id="codexAuthImportText" class="codex-import-textarea settings-text-input" data-codex-import-draft placeholder="${escapeAttr(mt("codexImportPlaceholder"))}">${escapeHtml(consoleState.codexImportDraft || "")}</textarea></label>
          ${importResultPanel}
          <div class="codex-import-footer"><p data-settings-help-copy>${escapeHtml(mt("codexImportSuccess"))}</p><button id="codexImportAuthBtn" class="settings-action-btn primary" type="button" data-mp-codex-import>${escapeHtml(mt("import"))}</button></div>
        </div>
      </details>`;
    const accountContent = state.providerAuthLoading && !authFiles.length
      ? `<div class="codex-console-loading settings-empty-card compact" role="status">${escapeHtml(mt("loadingAccounts"))}</div>`
      : renderCodexAccountManagementTable(authFiles, {
        translate: mt,
        editing: consoleState.codexEdit,
        busy: state.codexAccountBusy || {},
        selectedIds: consoleState.codexSelectedIds,
        batchBusy: consoleState.codexBatchBusy,
        batchPriority: consoleState.codexBatchPriority,
      });
    return `<div class="codex-account-console settings-page" tabindex="-1" aria-labelledby="codex-console-title">
      <button class="codex-console-back" type="button" data-mp-close-codex-page>← ${escapeHtml(mt("backToProviders"))}</button>
      <header class="codex-console-hero settings-card">
        <div class="codex-console-heading"><div><p class="mp-provider-kicker">Codex OAuth</p><h1 id="codex-console-title" class="settings-card-title">${escapeHtml(mt("accountConsoleTitle"))}</h1><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("accountConsoleDescription"))}</p></div></div>
        <div class="codex-console-actions settings-inline-actions">
          <span class="settings-status-pill ${escapeAttr(providerTone)}">${escapeHtml(providerState)}</span>
          <button id="codexRefreshAuthBtn" class="settings-action-btn" type="button" data-mp-codex-refresh ${state.providerAuthLoading ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(state.providerAuthLoading ? mt("refreshing") : mt("refreshAccounts"))}</button>
          <button class="settings-action-btn" type="button" data-mp-refresh-models ${modelRefreshBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(modelRefreshBusy ? mt("refreshing") : mt("refreshModels"))}</button>
        </div>
      </header>
      <section class="codex-console-stats settings-stat-grid" aria-label="${escapeAttr(mt("accountSummary"))}">
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.total))}</strong><span>${escapeHtml(mt("totalAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.available))}</strong><span>${escapeHtml(mt("availableAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.exhausted))}</strong><span>${escapeHtml(mt("exhaustedAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.rateLimited))}</strong><span>${escapeHtml(mt("limitedAccounts"))}</span></div>
        <div class="settings-stat-card"><strong>${escapeHtml(String(overview.disabled))}</strong><span>${escapeHtml(mt("disabledAccounts"))}</span></div>
      </section>
      ${result}${browserLoginPanel}${importPanel}
      <section class="codex-accounts-panel settings-card" aria-labelledby="codex-accounts-title" aria-busy="${state.providerAuthLoading ? "true" : "false"}">
        <div class="codex-console-section-head settings-card-header"><div><h2 id="codex-accounts-title" class="settings-card-title">${escapeHtml(mt("importedCredentials"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(mt("importedCredentialsDescription"))}</p></div><span class="settings-badge">${escapeHtml(mt("accountCount", { count: authFiles.length }))}</span></div>
        ${accountAlert}
        ${accountContent}
      </section>
      ${renderCodexModelPanel(provider, authFiles.length > 0)}
    </div>`;
  }

  function renderCodexImportCard() {
    return `
    <section class="settings-provider-section" id="codexCredentialImportSection">
      <div class="settings-provider-section-head">
        <div>
          <div class="settings-provider-title">${escapeHtml(mt("codexImportTitle"))}</div>
          <div class="settings-provider-meta" data-settings-help-copy>${escapeHtml(mt("codexImportDescription"))}</div>
        </div>
        <button id="codexImportAuthBtn" class="settings-action-btn primary" type="button">${escapeHtml(mt("import"))}</button>
      </div>
      <textarea id="codexAuthImportText" class="settings-token-input" placeholder="${escapeAttr(mt("codexImportPlaceholder"))}"></textarea>
      <div class="settings-inline-success" data-settings-help-copy>${escapeHtml(mt("codexImportSuccess"))}</div>
    </section>
  `;
  }

  function renderCodexAccountCard(authFiles) {
    return `
    <section class="settings-provider-section">
      <div class="settings-provider-section-head">
        <div>
          <div class="settings-provider-title">${escapeHtml(mt("importedCredentials"))}</div>
          <div class="settings-provider-meta" data-settings-help-copy>${escapeHtml(mt("importedCredentialsDescription"))}</div>
        </div>
        <button id="codexRefreshAuthBtn" class="settings-action-btn" type="button">${escapeHtml(mt("refreshAccounts"))}</button>
      </div>
      ${state.providerAuthMutationWarning ? `<div class="settings-inline-alert">${escapeHtml(state.providerAuthMutationWarning)}</div>` : state.providerAuthError ? `<div class="settings-inline-alert">${escapeHtml(state.providerAuthError)}</div>` : ""}
      <div class="settings-auth-list">
        ${renderCodexAccountManagementTable(authFiles, { translate: mt })}
      </div>
    </section>
  `;
  }

  function renderCodexAuthItem(item) {
    const name = authFileName(item);
    const provider = authFileProvider(item);
    const alias = typeof item === "object" && item ? (item.alias || item.email || item.account || item.account_id || item.accountID || "") : "";
    const disabled = Boolean(typeof item === "object" && item && item.disabled);
    return `
    <div class="settings-auth-item">
      <div>
        <div class="settings-auth-title">${escapeHtml(name)}</div>
        <div class="settings-auth-meta">${escapeHtml(provider)}${alias ? ` · ${escapeHtml(alias)}` : ""}</div>
      </div>
      <span class="settings-status-pill ${disabled ? "muted" : "ok"}">${escapeHtml(disabled ? mt("disabled") : mt("available"))}</span>
    </div>
  `;
  }

  function extractAuthFiles(value) {
    return normalizeCodexAccountList(value);
  }

  function authFileName(item) {
    if (typeof item === "string") return item;
    if (!item || typeof item !== "object") return mt("unknown");
    return item.name || item.filename || item.file || item.path || item.auth_index || item.authIndex || mt("unknown");
  }

  function authFileProvider(item) {
    if (!item || typeof item !== "object") return "Codex";
    return item.provider || item.type || item.channel || "Codex";
  }

  function renderCodexStatusCard(provider) {
    const models = providerModelList(provider);
    const endpoint = provider.baseUrl || "https://chatgpt.com/backend-api/codex";
    return `
    <section class="settings-provider-section highlighted">
      <div class="settings-provider-section-head">
        <div>
          <div class="settings-provider-title">${escapeHtml(providerLabel(provider))}</div>
          <div class="settings-provider-meta">${escapeHtml(endpoint)}</div>
        </div>
        <span class="settings-status-pill ${provider.error ? "warn" : (provider.configured ? "ok" : "muted")}">${escapeHtml(providerStatusText(provider))}</span>
      </div>
      ${provider.error ? `<div class="settings-inline-alert">${escapeHtml(provider.error)}</div>` : provider.configured ? `<div class="settings-inline-success">${escapeHtml(mt("nativeCodexReady", { count: models.length }))}</div>` : `<div class="settings-inline-alert">${escapeHtml(mt("nativeCodexNeedsCredentials"))}</div>`}
      <div class="settings-copy-row">
        <button class="settings-action-btn subtle" type="button" data-copy-text="${escapeAttr(endpoint)}">${escapeHtml(mt("copyBaseUrl"))}</button>
      </div>
    </section>
  `;
  }

  return {
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
  };
}
