import { fullAccessAllowed, remoteAccessContext } from "./remote-access-capabilities.mjs";
import { mergeProviderModelDiscovery } from "./model-provider-components.mjs?v=provider-card-clean-3-provider-create-page-2-provider-secrets-1-model-picker-1-provider-full-page-2-provider-placeholders-1-model-configs-1-provider-reference-1-default-openai-responses-1-provider-draft-session-1-native-image-generation-1";
import { t } from "./i18n.mjs?v=provider-draft-session-1";

const providerConsoleInteractiveSelector = "button, input, select, textarea, a, details, summary, [role=\"switch\"], [contenteditable=\"true\"]";
const providerConsoleFocusableSelector = "a[href], button, input, select, textarea, [tabindex]";
const codexBrowserLoginBasePath = "/api/providers/oauth/codex/login";

export function providerSensitiveDraftAccessAllowed(state = {}, locationLike = globalThis.location) {
  return !remoteAccessContext(state, locationLike) || fullAccessAllowed(state, locationLike);
}

export function validateProviderNameValue(value, { existingNames = [], mode = "create", originalName = "" } = {}) {
  const name = String(value || "").trim();
  if (!name) return { valid: false, code: "required", name };
  if (name.length > 64) return { valid: false, code: "too_long", name };
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name)) return { valid: false, code: "invalid", name };
  const original = String(originalName || "").trim();
  if (mode === "create" || name !== original) {
    const occupied = new Set((Array.isArray(existingNames) ? existingNames : []).map((item) => String(item || "").trim()).filter(Boolean));
    if (occupied.has(name)) return { valid: false, code: "conflict", name };
  }
  return { valid: true, code: "", name };
}

const commonCountryCodeSecondLevelDomains = new Set(["ac", "co", "com", "edu", "gov", "net", "org"]);

export function providerNameFromBaseURL(value) {
  let target;
  try {
    target = new URL(String(value || "").trim());
  } catch {
    return "";
  }
  if (!["http:", "https:"].includes(target.protocol)) return "";
  const hostname = target.hostname.toLowerCase().replace(/\.$/, "");
  if (!hostname || hostname === "localhost" || hostname.includes(":") || /^\d+(?:\.\d+){3}$/.test(hostname)) return "";
  const labels = hostname.split(".").filter(Boolean);
  if (labels.length < 2) return "";
  const countryCodeSuffix = labels.at(-1).length === 2 && commonCountryCodeSecondLevelDomains.has(labels.at(-2));
  const candidate = labels.at(countryCodeSuffix ? -3 : -2) || "";
  return /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/.test(candidate) ? candidate : "";
}

export function automaticProviderNameUpdate(baseURL, currentName = "", previousSuggestion = "", mode = "create") {
  const name = String(currentName || "").trim();
  const previous = String(previousSuggestion || "").trim();
  if (mode !== "create") return { name, suggestion: "", changed: false };
  const suggestion = providerNameFromBaseURL(baseURL);
  if (!suggestion) return { name, suggestion: previous, changed: false };
  if (name && name !== previous) return { name, suggestion: "", changed: false };
  return { name: suggestion, suggestion, changed: name !== suggestion };
}

export function codexBrowserLoginRequest(action, loginId = "") {
  const id = encodeURIComponent(String(loginId || "").trim());
  if (action === "start") return { path: `${codexBrowserLoginBasePath}/start`, options: { method: "POST" } };
  if (!id) throw new Error("Codex browser login ID is required");
  if (action === "status") return { path: `${codexBrowserLoginBasePath}/${id}`, options: {} };
  if (action === "cancel") return { path: `${codexBrowserLoginBasePath}/${id}`, options: { method: "DELETE" } };
  throw new Error(`unsupported Codex browser login action: ${action}`);
}

export function trustedCodexBrowserAuthURL(value) {
  try {
    const target = new URL(String(value || ""));
    return target.protocol === "https:"
      && target.hostname === "auth.openai.com"
      && target.port === ""
      && target.username === ""
      && target.password === ""
      && target.pathname === "/oauth/authorize";
  } catch {
    return false;
  }
}

export function normalizeCodexBrowserLoginStatus(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const account = source.account && typeof source.account === "object" && !Array.isArray(source.account) ? source.account : null;
  const rawStatus = String(source.status || "idle").trim().toLowerCase();
  return {
    loginId: String(source.loginId || source.login_id || "").trim(),
    status: rawStatus === "canceled" ? "cancelled" : rawStatus,
    authUrl: String(source.authUrl || source.auth_url || "").trim(),
    expiresAt: String(source.expiresAt || source.expires_at || "").trim(),
    message: String(source.message || source.error || "").trim(),
    account,
  };
}

export function redactedProviderProxyURL(value) {
  const raw = String(value || "").trim();
  if (!raw) return "";
  try {
    const parsed = new URL(raw);
    parsed.username = "";
    parsed.password = "";
    return parsed.toString().replace(/\/$/, "");
  } catch {
    return raw.replace(/\/\/[^/@\s]+@/, "//");
  }
}

export function providerConnectionFingerprint(draft = {}) {
  return JSON.stringify({
    type: String(draft.type || "").trim(),
    baseUrl: String(draft.baseUrl || "").trim(),
    apiKey: String(draft.apiKey || ""),
    clearApiKey: Boolean(draft.clearApiKey),
    apiKeyOptional: Boolean(draft.apiKeyOptional),
    proxyUrl: String(draft.proxyUrl || "").trim(),
    clearProxyAuth: Boolean(draft.clearProxyAuth),
    userAgent: String(draft.userAgent || "").trim(),
    requestHeaders: (Array.isArray(draft.requestHeaders) ? draft.requestHeaders : []).map((header) => ({
      name: String(header?.name || "").trim().toLowerCase(),
      value: String(header?.value || ""),
      keepExisting: Boolean(header?.keepExisting),
    })),
    insecureSkipTLSVerify: Boolean(draft.insecureSkipTLSVerify),
  });
}

export function markProviderModelsStale(previousDraft = {}, nextDraft = {}) {
  if (!previousDraft.modelsReady || previousDraft.modelsStale) return { ...nextDraft };
  return providerConnectionFingerprint(previousDraft) === providerConnectionFingerprint(nextDraft)
    ? { ...nextDraft }
    : { ...nextDraft, modelsStale: true };
}

export function providerConsoleDraftFromForm(currentDraft = {}, form, fallbackType = "openai-compatible") {
  const fields = form?.elements || {};
  const value = (name, fallback = "") => String(fields[name]?.value ?? fallback ?? "");
  const requestHeaders = [...(form?.querySelectorAll?.("[data-mp-request-header-row]") || [])].map((row) => {
    const name = String(row.querySelector?.("[data-mp-request-header-name]")?.value || "");
    const headerValue = String(row.querySelector?.("[data-mp-request-header-value]")?.value || "");
    const originalName = String(row.dataset?.originalName || "").trim().toLowerCase();
    const keepExisting = row.dataset?.keepExisting === "true"
      && !headerValue
      && String(name).trim().toLowerCase() === originalName;
    return {
      name,
      value: headerValue,
      keepExisting,
      configured: row.dataset?.configured === "true",
    };
  });
  const nextDraft = {
    ...currentDraft,
    name: value("name", currentDraft.name),
    type: value("type", currentDraft.type || fallbackType),
    profile: String(currentDraft.profile || ""),
    baseUrl: value("baseUrl", currentDraft.baseUrl),
    apiKey: fields.apiKey && String(fields.apiKey.value || "") === "" && currentDraft.apiKeyDraft
      ? String(currentDraft.apiKey || "")
      : value("apiKey", currentDraft.apiKey),
    apiKeyDraft: true,
    clearApiKey: Boolean(fields.clearApiKey?.checked),
    proxyUrl: value("proxyUrl", currentDraft.proxyUrl),
    proxyUrlDraft: true,
    clearProxyAuth: Boolean(fields.clearProxyAuth?.checked),
    userAgent: value("userAgent", currentDraft.userAgent),
    userAgentDraft: true,
    requestHeaders,
    requestHeadersDraft: true,
    insecureSkipTLSVerify: Boolean(fields.insecureSkipTLSVerify?.checked),
    model: value("model", currentDraft.model),
    maxTokens: Number(fields.maxTokens?.value || 0),
    apiKeyOptional: Boolean(fields.apiKeyOptional?.checked),
  };
  return markProviderModelsStale(currentDraft, nextDraft);
}

export function syncProviderConsoleDraft(consoleState, form) {
  if (!consoleState || !form) return null;
  const draft = providerConsoleDraftFromForm(consoleState.draft || {}, form, consoleState.type);
  consoleState.draft = draft;
  consoleState.dirty = true;
  return draft;
}

export function isProviderConsoleInteractiveTarget(target, card = null) {
  const interactive = target?.closest?.(providerConsoleInteractiveSelector);
  return Boolean(interactive && (!card || card.contains?.(interactive)));
}

export function selectProviderConsoleFieldOnFocus(target) {
  const marker = target?.getAttribute?.("data-select-on-focus") || target?.dataset?.selectOnFocus;
  if (marker !== "true") return false;
  target.removeAttribute?.("data-select-on-focus");
  if (target.dataset) delete target.dataset.selectOnFocus;
  if (target.disabled || target.readOnly || target.type === "password") return false;
  if (!String(target.value ?? "")) return false;
  target.select?.();
  return true;
}

export function shouldOpenProviderCardFromKeyboard(event, card) {
  if (!card || !["Enter", " ", "Spacebar"].includes(event?.key)) return false;
  return !isProviderConsoleInteractiveTarget(event.target, card);
}

export function providerConsoleFocusableElements(layer) {
  return [...(layer?.querySelectorAll?.(providerConsoleFocusableSelector) || [])]
    .filter((node) => !node.disabled && node.getAttribute?.("aria-hidden") !== "true" && node.tabIndex !== -1);
}

export function trapProviderConsoleFocus(event, layer) {
  if (event?.key !== "Tab" || !layer) return false;
  const focusable = providerConsoleFocusableElements(layer);
  if (!focusable.length) {
    event.preventDefault?.();
    layer.focus?.();
    return true;
  }
  const current = event.target;
  const index = focusable.indexOf(current);
  if (event.shiftKey ? index <= 0 : index === -1 || index === focusable.length - 1) {
    event.preventDefault?.();
    focusable[event.shiftKey ? focusable.length - 1 : 0].focus?.();
    return true;
  }
  return false;
}

export function restoreProviderConsoleFocus(target) {
  target?.focus?.();
}

export function providerPreflightResult(response, translate) {
  if (response?.errorCode === "not_configured") {
    return {
      message: translate("messages.currentDraftTestNeedsApiKey"),
      tone: "warning",
      terminalLevel: "warn",
    };
  }
  if (response?.reachable === true && response?.configured !== false) {
    return {
      message: translate("messages.currentDraftTestSucceeded"),
      tone: "success",
      terminalLevel: "info",
    };
  }
  return {
    message: translate("messages.currentDraftTestFailed", { message: response?.errorCode || translate("messages.requestFailed") }),
    tone: "attention",
    terminalLevel: "warn",
  };
}

export function providerModelDiscovery(response, currentModel = "", currentConfigs = []) {
  const modelConfigs = mergeProviderModelDiscovery(currentConfigs, response, currentModel);
  const models = modelConfigs.map((item) => item.name);
  const current = String(currentModel || "").trim();
  const visible = modelConfigs.filter((item) => !item.hidden);
  return {
    models,
    modelConfigs,
    selectedModel: visible.some((item) => item.name === current) ? current : (visible[0]?.name || current),
  };
}

export function normalizeCodexAccountList(value) {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== "object") return [];
  for (const key of ["accounts", "files", "authFiles", "data", "items"]) {
    if (Array.isArray(value[key])) return value[key];
  }
  return [];
}

export function codexAccountExportFilename(account = {}, id = "") {
  const raw = String(account?.name || account?.alias || `codex-${id || "account"}`).trim();
  const safe = raw
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .replace(/[\\/:*?"<>|]+/g, "-")
    .replace(/\s+/g, " ")
    .replace(/^\.+/, "")
    .slice(0, 120)
    .trim();
  const base = safe || "codex-auth";
  return /\.json$/i.test(base) ? base : `${base}.json`;
}

export function normalizeAnthropicAccountList(value) {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== "object") return [];
  for (const key of ["accounts", "data", "items"]) {
    if (Array.isArray(value[key])) return value[key];
  }
  return [];
}

export function codexAccountActionRequest(action, id, values = {}) {
  const path = `/api/providers/oauth/codex/accounts/${encodeURIComponent(String(id || ""))}`;
  switch (action) {
    case "save": return { path, options: { method: "PATCH", body: JSON.stringify({ alias: String(values.alias || ""), priority: Number(values.priority) }) } };
    case "toggle": return { path, options: { method: "PATCH", body: JSON.stringify({ disabled: !Boolean(values.disabled) }) } };
    case "sync": return { path: `${path}/refresh`, options: { method: "POST" } };
    case "export": return { path: `${path}/export`, options: { method: "GET", headers: { "X-Autoto-Confirm": "export-codex-account" } } };
    case "delete": return { path, options: { method: "DELETE" } };
    default: throw new Error(`unsupported Codex account action: ${action}`);
  }
}

export function codexAccountBatchRequest(operation, ids, values = {}) {
  const normalizedIds = [...new Set((Array.isArray(ids) ? ids : [])
    .map((id) => String(id || "").trim())
    .filter(Boolean))];
  const body = { ids: normalizedIds, operation: String(operation || "").trim() };
  if (body.operation === "set_priority") body.priority = Number(values.priority);
  return {
    path: "/api/providers/oauth/codex/accounts/batch",
    options: { method: "POST", body: JSON.stringify(body) },
  };
}

const subscriptionProviderSet = new Set(["gemini", "grok", "kimi"]);

// normalizeSubscriptionProvider rejects anything but the three native subscription
// providers, so gemini-interactions or custom providers can never reach the
// account routes.
export function normalizeSubscriptionProvider(value) {
  const provider = String(value || "").trim().toLowerCase();
  return subscriptionProviderSet.has(provider) ? provider : "";
}

export function subscriptionAccountsListRequest(provider) {
  const p = normalizeSubscriptionProvider(provider);
  if (!p) throw new Error(`unsupported subscription provider: ${provider}`);
  return { path: `/api/providers/auth/${p}/accounts`, options: {} };
}

export function subscriptionAccountActionRequest(action, provider, id, values = {}) {
  const p = normalizeSubscriptionProvider(provider);
  if (!p) throw new Error(`unsupported subscription provider: ${provider}`);
  const accountId = encodeURIComponent(String(id || "").trim());
  const path = `/api/providers/auth/${p}/accounts/${accountId}`;
  switch (action) {
    case "save": return { path, options: { method: "PATCH", body: JSON.stringify({ alias: String(values.alias || ""), priority: Number(values.priority) }) } };
    case "toggle": return { path, options: { method: "PATCH", body: JSON.stringify({ disabled: !values.disabled }) } };
    case "sync": return { path: `${path}/sync`, options: { method: "POST" } };
    case "delete": return { path, options: { method: "DELETE" } };
    default: throw new Error(`unsupported subscription account action: ${action}`);
  }
}

export function subscriptionOAuthLoginRequest(action, provider, loginId = "") {
  const p = normalizeSubscriptionProvider(provider);
  if (!p) throw new Error(`unsupported subscription provider: ${provider}`);
  const base = `/api/providers/oauth/${p}/login`;
  if (action === "start") return { path: `${base}/start`, options: { method: "POST" } };
  const id = encodeURIComponent(String(loginId || "").trim());
  if (!id) throw new Error("subscription login ID is required");
  if (action === "status") return { path: `${base}/${id}`, options: {} };
  if (action === "cancel") return { path: `${base}/${id}`, options: { method: "DELETE" } };
  throw new Error(`unsupported subscription login action: ${action}`);
}

export function normalizeSubscriptionAccountList(value) {
  if (Array.isArray(value)) return value;
  if (!value || typeof value !== "object") return [];
  for (const key of ["accounts", "data", "items"]) {
    if (Array.isArray(value[key])) return value[key];
  }
  return [];
}

export function normalizeSubscriptionLoginStatus(value = {}) {
  const source = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const account = source.account && typeof source.account === "object" && !Array.isArray(source.account) ? source.account : null;
  const rawStatus = String(source.status || "idle").trim().toLowerCase();
  return {
    loginId: String(source.loginId || source.login_id || "").trim(),
    provider: normalizeSubscriptionProvider(source.provider),
    status: rawStatus === "canceled" ? "cancelled" : rawStatus,
    authUrl: String(source.authUrl || source.auth_url || "").trim(),
    userCode: String(source.userCode || source.user_code || "").trim(),
    verificationUri: String(source.verificationUri || source.verification_uri || "").trim(),
    expiresAt: String(source.expiresAt || source.expires_at || "").trim(),
    message: String(source.message || source.error || "").trim(),
    account,
  };
}

// trustedSubscriptionAuthURL enforces a per-provider official-domain allowlist so
// a compromised or spoofed response can never open an arbitrary URL.
export function trustedSubscriptionAuthURL(provider, value) {
  const p = normalizeSubscriptionProvider(provider);
  if (!p) return false;
  let target;
  try {
    target = new URL(String(value || ""));
  } catch {
    return false;
  }
  if (target.protocol !== "https:" || target.username || target.password) return false;
  const host = target.hostname.toLowerCase().replace(/\.$/, "");
  if (p === "gemini") return host === "accounts.google.com";
  if (p === "grok") return host === "x.ai" || host.endsWith(".x.ai");
  if (p === "kimi") return host === "kimi.com" || host.endsWith(".kimi.com");
  return false;
}

export function codexImportBatchRequest(files) {
  const normalizedFiles = (Array.isArray(files) ? files : []).map((file) => ({
    filename: String(file?.filename || file?.name || "").trim(),
    content: String(file?.content || ""),
  }));
  return {
    path: "/api/providers/oauth/codex/import/batch",
    options: { method: "POST", body: JSON.stringify({ files: normalizedFiles }) },
  };
}

export function normalizeCodexBatchResult(value = {}, requestedIds = []) {
  const requested = normalizeCodexSelectedIds(requestedIds);
  const items = Array.isArray(value?.results) ? value.results : [];
  const normalizedItems = items.map((item) => {
    const id = String(item?.id || item?.account_id || item?.accountId || "").trim();
    const success = item?.success === true || String(item?.status || "").toLowerCase() === "ok";
    return {
      id,
      success,
      error: String(item?.error || "").trim(),
      warning: String(item?.warning || "").trim(),
      retryable: Boolean(item?.retryable),
    };
  }).filter((item) => item.id);
  const byID = new Map(normalizedItems.map((item) => [item.id, item]));
  const failedIds = requested.filter((id) => !byID.get(id)?.success);
  const warnings = normalizedItems.map((item) => item.warning).filter(Boolean);
  const success = Math.max(0, Number(value?.success ?? requested.length - failedIds.length) || 0);
  const failed = Math.max(0, Number(value?.failed ?? failedIds.length) || 0);
  return { total: Math.max(0, Number(value?.total ?? requested.length) || 0), success, failed, failedIds, warnings, results: normalizedItems };
}

export function normalizeCodexSelectedIds(ids, accounts = null) {
  const restrictToAccounts = Array.isArray(accounts);
  const allowed = new Set((restrictToAccounts ? accounts : []).map((account) => String(account?.id || account?.auth_index || account?.authIndex || account?.name || "").trim()).filter(Boolean));
  return [...new Set((Array.isArray(ids) ? ids : []).map((id) => String(id || "").trim()).filter((id) => id && (!restrictToAccounts || allowed.has(id))))];
}

export function validateCodexImportJSON(filename, content) {
  const name = String(filename || "").trim() || "codex-auth.json";
  const text = String(content || "");
  if (!name.toLowerCase().endsWith(".json")) return { valid: false, filename: name, code: "type" };
  if (!text.trim()) return { valid: false, filename: name, code: "empty" };
  try {
    const value = JSON.parse(text);
    const objectLike = value && typeof value === "object";
    return objectLike ? { valid: true, filename: name, value } : { valid: false, filename: name, code: "shape" };
  } catch {
    return { valid: false, filename: name, code: "parse" };
  }
}

export function normalizeCodexImportBatchResult(value = {}) {
  const results = (Array.isArray(value?.results) ? value.results : []).map((item) => {
    const rawStatus = String(item?.status || "failed").toLowerCase();
    const status = rawStatus === "success" || rawStatus === "skipped" ? rawStatus : "failed";
    return {
      filename: String(item?.filename || "").trim(),
      status,
      format: String(item?.format || "").trim(),
      imported: Math.max(0, Number(item?.imported || 0) || 0),
      skipped: Math.max(0, Number(item?.skipped || 0) || 0),
      files: Array.isArray(item?.files) ? item.files.map((name) => String(name || "").trim()).filter(Boolean) : [],
      error: String(item?.error || "").trim(),
    };
  });
  const imported = results.reduce((sum, item) => sum + item.imported, 0);
  const skipped = results.reduce((sum, item) => sum + item.skipped, 0);
  return {
    status: String(value?.status || "").trim(),
    total: Math.max(0, Number(value?.total ?? results.length) || 0),
    successFiles: Math.max(0, Number(value?.success ?? results.filter((item) => item.status === "success").length) || 0),
    skippedFiles: Math.max(0, Number(value?.skipped ?? results.filter((item) => item.status === "skipped").length) || 0),
    failedFiles: Math.max(0, Number(value?.failed ?? results.filter((item) => item.status === "failed").length) || 0),
    imported,
    skipped,
    results,
  };
}

export function anthropicProfileLoginCommand(profile) {
  const value = String(profile || "").trim();
  if (!value) return "ant auth login --profile <name>";
  if (/^[A-Za-z0-9._-]+$/.test(value)) return `ant auth login --profile ${value}`;
  return `ant auth login --profile '${value.replace(/'/g, `'"'"'`)}'`;
}

export function anthropicAccountsListRequest() {
  return { path: "/api/providers/auth/anthropic/accounts", options: {} };
}

export function anthropicAccountCreateRequest(values = {}) {
  const authType = values.authType === "api_key" ? "api_key" : "profile";
  const priority = Number(values.priority);
  const body = {
    authType,
    ...(authType === "api_key"
      ? { apiKey: String(values.apiKey || "").trim() }
      : { profile: String(values.profile || "").trim() }),
  };
  const alias = String(values.alias || "").trim();
  if (alias) body.alias = alias;
  if (Number.isInteger(priority)) body.priority = priority;
  return { path: "/api/providers/auth/anthropic/accounts", options: { method: "POST", body: JSON.stringify(body) } };
}

export function consumeAnthropicAccountCreateRequest(form) {
  const request = anthropicAccountCreateRequest({
    authType: form?.elements?.authType?.value,
    profile: form?.elements?.profile?.value,
    apiKey: form?.elements?.apiKey?.value,
    alias: form?.elements?.alias?.value,
    priority: form?.elements?.priority?.value,
  });
  if (form?.elements?.apiKey) form.elements.apiKey.value = "";
  return request;
}

export function anthropicAccountActionRequest(action, id, values = {}) {
  const path = `/api/providers/auth/anthropic/accounts/${encodeURIComponent(String(id || ""))}`;
  switch (action) {
    case "save": return { path, options: { method: "PATCH", body: JSON.stringify({ alias: String(values.alias || ""), priority: Number(values.priority) }) } };
    case "toggle": return { path, options: { method: "PATCH", body: JSON.stringify({ disabled: !Boolean(values.disabled) }) } };
    case "sync": return { path: `${path}/sync`, options: { method: "POST" } };
    case "delete": return { path, options: { method: "DELETE" } };
    default: throw new Error(`unsupported Anthropic account action: ${action}`);
  }
}

export function codexMutationRefreshWarning(refreshed, message, translate = (key, params) => t(`modelProvider.${key}`, params)) {
  return refreshed ? "" : translate("accountMutationRefreshFailed", { message: message || translate("unknown") });
}

export function codexDeleteResultWarning(result, translate = (key, params) => t(`modelProvider.${key}`, params)) {
  return result?.status === "partial" || result?.stats_deleted === false
    ? translate("accountDeletePartial")
    : "";
}
