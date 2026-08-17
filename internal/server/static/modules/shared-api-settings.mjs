import { objectValue } from "./value-coercion.mjs";
import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { currentUILocale, t } from "./i18n.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

const endpoints = Object.freeze({
  status: "/api/gateway/status",
  config: "/api/gateway/config",
  accounts: "/api/gateway/accounts",
  keys: "/api/gateway/keys",
  models: "/api/gateway/models",
  usage: "/api/gateway/usage",
  requests: "/api/gateway/requests?limit=50",
  tunnel: "/api/gateway/tunnel",
});

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(objectValue(value), key);
}

function textValue(value) {
  return String(value ?? "").trim();
}

function integerValue(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? Math.floor(number) : fallback;
}

function listValue(value) {
  const list = Array.isArray(value) ? value : textValue(value).split(/[\n,]/);
  return [...new Set(list.map(textValue).filter(Boolean))];
}

function encoded(value) {
  return encodeURIComponent(textValue(value));
}

function normalizedDateTime(value) {
  const text = textValue(value);
  if (!text) return "";
  const date = new Date(text);
  return Number.isNaN(date.getTime()) ? text : date.toISOString();
}

function dateTimeInputValue(value) {
  const date = new Date(value);
  if (!value || Number.isNaN(date.getTime())) return "";
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function directGatewayValue(settings = {}) {
  const source = objectValue(settings);
  const nested = objectValue(source.gateway);
  return Object.keys(nested).length ? nested : source;
}

export function normalizeGatewaySettings(settings = {}) {
  const gateway = directGatewayValue(settings);
  return {
    enabled: Boolean(gateway.enabled),
    host: textValue(gateway.host),
    port: integerValue(gateway.port),
    allowRemote: Boolean(gateway.allowRemote),
    maxGlobalConcurrency: integerValue(gateway.maxGlobalConcurrency),
    maxRequestBytes: integerValue(gateway.maxRequestBytes),
  };
}

export function normalizeApiTunnel(value = {}) {
  const source = objectValue(value);
  const validStatuses = ["idle", "installing", "starting", "running", "stopping", "unavailable", "error"];
  return {
    available: Boolean(source.available),
    installable: !Boolean(source.available) && Boolean(source.installable),
    status: validStatuses.includes(textValue(source.status)) ? textValue(source.status) : "unavailable",
    publicUrl: textValue(source.publicApiBaseUrl ?? source.publicUrl),
    activeKeys: integerValue(source.activeKeys),
    gatewayRunning: Boolean(source.gatewayRunning),
    error: textValue(source.error),
  };
}

export function normalizeGatewayRuntime(value = {}, fallback = {}) {
  const runtime = objectValue(value);
  const defaults = objectValue(fallback);
  return {
    desiredEnabled: hasOwn(runtime, "desiredEnabled") ? Boolean(runtime.desiredEnabled) : Boolean(defaults.desiredEnabled),
    running: Boolean(runtime.running),
    address: textValue(runtime.address),
    allowRemote: hasOwn(runtime, "allowRemote") ? Boolean(runtime.allowRemote) : Boolean(defaults.allowRemote),
    ephemeralIsolation: Boolean(runtime.ephemeralIsolation),
    lastError: textValue(runtime.lastError),
    updatedAt: textValue(runtime.updatedAt),
  };
}

export function normalizeGatewayStatus(payload = {}) {
  const root = objectValue(payload);
  const nestedStatus = objectValue(root.status);
  const statusSource = Object.keys(nestedStatus).length ? nestedStatus : root;
  const gatewaySource = Object.keys(objectValue(root.gateway)).length ? root.gateway : {};
  const gateway = normalizeGatewaySettings({ gateway: gatewaySource });
  return {
    status: normalizeGatewayRuntime(statusSource, {
      desiredEnabled: gateway.enabled,
      allowRemote: gateway.allowRemote,
    }),
    gateway,
    protocols: listValue(root.protocols),
  };
}

export function normalizeGatewayKey(value = {}) {
  const key = objectValue(value);
  return {
    id: textValue(key.id),
    name: textValue(key.name),
    keyPrefix: textValue(key.keyPrefix),
    enabled: key.enabled !== false,
    allowedModels: listValue(key.allowedModels),
    requestsPerMinute: integerValue(key.requestsPerMinute),
    monthlyTokenLimit: integerValue(key.monthlyTokenLimit),
    maxConcurrency: integerValue(key.maxConcurrency),
    expiresAt: textValue(key.expiresAt),
    lastUsedAt: textValue(key.lastUsedAt),
    revokedAt: textValue(key.revokedAt),
    createdAt: textValue(key.createdAt),
    updatedAt: textValue(key.updatedAt),
    usage: objectValue(key.usage),
  };
}

export function normalizeGatewayKeys(payload = {}) {
  const keys = Array.isArray(payload) ? payload : objectValue(payload).keys;
  return (Array.isArray(keys) ? keys : []).map(normalizeGatewayKey).filter((key) => key.id);
}

export function normalizeGatewayAccount(value = {}) {
  const account = objectValue(value);
  return {
    provider: textValue(account.provider),
    accountId: textValue(account.accountId),
    label: textValue(account.label),
    authType: textValue(account.authType),
    source: textValue(account.source),
    priority: integerValue(account.priority),
    disabled: Boolean(account.disabled),
    eligible: account.eligible !== false,
    shared: Boolean(account.shared),
    effective: Boolean(account.effective),
    reason: textValue(account.reason),
  };
}

export function normalizeGatewayAccounts(payload = {}) {
  const accounts = Array.isArray(payload) ? payload : objectValue(payload).accounts;
  return (Array.isArray(accounts) ? accounts : []).map(normalizeGatewayAccount).filter((account) => account.provider && account.accountId);
}

export function normalizeGatewayRequest(value = {}) {
  const record = objectValue(value);
  const key = objectValue(record.key);
  const account = objectValue(record.account);
  const actual = objectValue(record.actual ?? record.resolved);
  const tokens = objectValue(record.tokens ?? record.usage);
  const timing = objectValue(record.timing);
  const error = objectValue(record.error);
  const inputTokens = integerValue(record.inputTokens ?? record.promptTokens ?? tokens.inputTokens ?? tokens.input ?? tokens.prompt);
  const outputTokens = integerValue(record.outputTokens ?? record.completionTokens ?? tokens.outputTokens ?? tokens.output ?? tokens.completion);
  return {
    id: textValue(record.id ?? record.requestId),
    createdAt: textValue(record.createdAt ?? record.timestamp ?? record.startedAt ?? record.time),
    key: textValue(record.keyLabel ?? record.keyName ?? record.gatewayKeyName ?? key.label ?? key.name ?? key.safeId ?? record.keyPrefix ?? record.gatewayKeyPrefix ?? record.keyId ?? record.gatewayKeyId ?? key.id ?? (typeof record.key === "string" ? record.key : "")),
    protocol: textValue(record.protocol ?? record.apiProtocol),
    kind: textValue(record.kind ?? record.requestKind),
    alias: textValue(record.alias ?? record.modelAlias ?? record.requestedAlias ?? record.requestedModel),
    provider: textValue(record.provider ?? record.actualProvider ?? record.resolvedProvider ?? actual.provider),
    model: textValue(record.model ?? record.actualModel ?? record.resolvedModel ?? actual.model),
    accountId: textValue(record.accountId ?? record.safeAccountId ?? record.accountSafeId ?? record.credentialId ?? account.accountId ?? account.safeId ?? account.id),
    accountLabel: textValue(record.accountLabel ?? account.label),
    inputTokens,
    outputTokens,
    totalTokens: integerValue(record.totalTokens ?? tokens.totalTokens ?? tokens.total, inputTokens + outputTokens),
    durationMs: integerValue(record.durationMs ?? record.duration ?? timing.durationMs ?? timing.duration),
    ttftMs: integerValue(record.ttftMs ?? record.timeToFirstTokenMs ?? timing.ttftMs ?? timing.timeToFirstTokenMs),
    error: textValue(record.errorMessage ?? error.message ?? (typeof record.error === "string" ? record.error : "")),
  };
}

export function normalizeGatewayRequests(payload = {}) {
  const requests = Array.isArray(payload) ? payload : objectValue(payload).requests;
  return (Array.isArray(requests) ? requests : []).map(normalizeGatewayRequest);
}

export function isCodexGatewayProvider(provider = {}) {
  const name = textValue(provider.name).toLowerCase();
  const type = textValue(provider.type).toLowerCase();
  return name === "codex" || type === "codex";
}

export function gatewayProviderRestriction(provider = {}) {
  const profile = textValue(provider.profile).toLowerCase();
  if (profile === "cliproxyapi") return "oauthProxy";
  return "";
}

export function gatewayProviderRequest(name, gatewayEnabled) {
  const providerName = textValue(name);
  if (!providerName) throw new TypeError("Provider name is required");
  return {
    path: `/api/providers/${encoded(providerName)}`,
    options: { method: "PATCH", body: JSON.stringify({ gatewayEnabled: Boolean(gatewayEnabled) }) },
  };
}

export function gatewayAccountRequest(provider, accountId, shared) {
  const providerName = textValue(provider);
  const id = textValue(accountId);
  if (!providerName || !id) throw new TypeError("Gateway account provider and account id are required");
  return {
    path: `${endpoints.accounts}/${encoded(providerName)}/${encoded(id)}`,
    options: { method: "PATCH", body: JSON.stringify({ shared: Boolean(shared) }) },
  };
}

export function gatewayConfigPayload(draft = {}) {
  const source = objectValue(draft);
  const payload = {};
  if (hasOwn(source, "enabled")) payload.enabled = Boolean(source.enabled);
  if (hasOwn(source, "host")) payload.host = textValue(source.host);
  if (hasOwn(source, "port")) payload.port = integerValue(source.port);
  if (hasOwn(source, "allowRemote")) payload.allowRemote = Boolean(source.allowRemote);
  if (hasOwn(source, "maxGlobalConcurrency")) payload.maxGlobalConcurrency = integerValue(source.maxGlobalConcurrency);
  if (hasOwn(source, "maxRequestBytes")) payload.maxRequestBytes = integerValue(source.maxRequestBytes);
  if (source.confirmRemoteRisk === true) payload.confirmRemoteRisk = true;
  return payload;
}

export function gatewayConfigRequest(draft = {}, confirmRemoteRisk = false) {
  const payload = gatewayConfigPayload(draft);
  if (confirmRemoteRisk) payload.confirmRemoteRisk = true;
  return { path: endpoints.config, options: { method: "PATCH", body: JSON.stringify(payload) } };
}

export function isLoopbackGatewayHost(value) {
  let host = textValue(value).toLowerCase();
  if (!host) return true;
  if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
  if (host.endsWith(".")) host = host.slice(0, -1);
  if (host === "localhost" || host === "::1" || host === "0:0:0:0:0:0:0:1") return true;
  return /^127(?:\.\d{1,3}){3}$/.test(host);
}

export function gatewayConfigNeedsRemoteConfirmation(current = {}, draft = {}, runtime = {}) {
  const before = normalizeGatewaySettings(current);
  const payload = gatewayConfigPayload(draft);
  const nextHost = hasOwn(payload, "host") ? payload.host : before.host;
  const nextAllowRemote = hasOwn(payload, "allowRemote") ? payload.allowRemote : before.allowRemote;
  const opensRemote = nextAllowRemote && !before.allowRemote;
  const changesToNonLoopback = hasOwn(payload, "host") && !isLoopbackGatewayHost(nextHost) && textValue(nextHost).toLowerCase() !== textValue(before.host).toLowerCase();
  const startsRiskyListener = payload.enabled === true && !Boolean(objectValue(runtime).desiredEnabled) && (nextAllowRemote || !isLoopbackGatewayHost(nextHost));
  return opensRemote || changesToNonLoopback || startsRiskyListener;
}

export function gatewayKeyPolicyPayload(draft = {}) {
  const source = objectValue(draft);
  const payload = {
    name: textValue(source.name),
    enabled: source.enabled !== false,
    allowedModels: listValue(source.allowedModels),
    requestsPerMinute: integerValue(source.requestsPerMinute),
    monthlyTokenLimit: integerValue(source.monthlyTokenLimit),
    maxConcurrency: integerValue(source.maxConcurrency),
    expiresAt: normalizedDateTime(source.expiresAt),
  };
  if (!payload.name) delete payload.name;
  if (!payload.expiresAt) payload.expiresAt = "";
  return payload;
}

export function gatewayKeyRequest(action, keyOrID = {}, draft = {}) {
  const key = typeof keyOrID === "string" ? { id: keyOrID } : objectValue(keyOrID);
  const id = textValue(key.id);
  if (action === "create") return { path: endpoints.keys, options: { method: "POST", cache: "no-store", body: JSON.stringify(gatewayKeyPolicyPayload(draft)) } };
  if (!id) throw new TypeError("Gateway key id is required");
  const base = `${endpoints.keys}/${encoded(id)}`;
  const expectedUpdatedAt = textValue(draft.expectedUpdatedAt ?? key.updatedAt);
  if (action === "update") return { path: base, options: { method: "PATCH", body: JSON.stringify({ ...gatewayKeyPolicyPayload(draft), expectedUpdatedAt }) } };
  if (action === "toggle") return { path: base, options: { method: "PATCH", body: JSON.stringify({ enabled: !Boolean(key.enabled), expectedUpdatedAt }) } };
  if (action === "rotate") return { path: `${base}/rotate`, options: { method: "POST", cache: "no-store" } };
  if (action === "revoke") return { path: `${base}/revoke`, options: { method: "POST" } };
  if (action === "delete") return { path: base, options: { method: "DELETE" } };
  throw new TypeError(`Unknown gateway key action: ${action}`);
}

function replaceBy(items, item, field) {
  const index = items.findIndex((candidate) => candidate[field] === item[field]);
  if (index < 0) return [item, ...items];
  return items.map((candidate, candidateIndex) => candidateIndex === index ? item : candidate);
}

function replaceAccount(items, account) {
  const index = items.findIndex((candidate) => candidate.provider === account.provider && candidate.accountId === account.accountId);
  if (index < 0) return [account, ...items];
  return items.map((candidate, candidateIndex) => candidateIndex === index ? account : candidate);
}

function formatDate(value) {
  if (!value) return t("sharedAPI.never");
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(currentUILocale(), { dateStyle: "medium", timeStyle: "short" }).format(date);
}

function formatNumber(value) {
  return new Intl.NumberFormat(currentUILocale()).format(integerValue(value));
}

function formatMilliseconds(value) {
  const milliseconds = integerValue(value);
  if (!milliseconds) return "—";
  if (milliseconds < 1000) return `${formatNumber(milliseconds)} ms`;
  return `${new Intl.NumberFormat(currentUILocale(), { maximumFractionDigits: 1 }).format(milliseconds / 1000)} s`;
}

function keyStatus(key) {
  if (key.revokedAt) return { key: "revoked", tone: "danger" };
  if (!key.enabled) return { key: "paused", tone: "warn" };
  if (key.expiresAt && new Date(key.expiresAt).getTime() <= Date.now()) return { key: "expired", tone: "warn" };
  return { key: "active", tone: "ok" };
}

function providerLabel(provider) {
  return textValue(provider.name) || textValue(provider.type) || t("sharedAPI.unknownProvider");
}

function displayHost(host) {
  const value = textValue(host);
  if (value.includes(":") && !value.startsWith("[")) return `[${value}]`;
  return value;
}

function gatewayAddress(gateway) {
  if (!gateway.host && !gateway.port) return t("sharedAPI.notConfigured");
  const host = displayHost(gateway.host || "127.0.0.1");
  return gateway.port ? `${host}:${gateway.port}` : host;
}

function keyUsageValue(key) {
  return integerValue(key.usage.monthlyTokens ?? key.usage.tokens ?? key.usage.tokenCount);
}

function keyEditorValues(key = {}) {
  const normalized = normalizeGatewayKey(key);
  return {
    name: normalized.name,
    enabled: normalized.enabled,
    allowedModels: normalized.allowedModels.join("\n"),
    requestsPerMinute: normalized.requestsPerMinute,
    monthlyTokenLimit: normalized.monthlyTokenLimit,
    maxConcurrency: normalized.maxConcurrency,
    expiresAt: dateTimeInputValue(normalized.expiresAt),
  };
}

function accountIsProfile(account) {
  const description = `${account.authType} ${account.source}`.toLowerCase();
  return description.includes("profile") || description.includes("cliproxy");
}

function accountRestriction(account) {
  if (account.disabled) return account.reason || t("sharedAPI.accountDisabled");
  if (accountIsProfile(account)) return account.reason || t("sharedAPI.accountProfileUnavailable");
  if (!account.eligible) return account.reason || t("sharedAPI.accountNotEligible");
  return "";
}

export function createSharedAPISettingsController({
  state,
  request,
  reloadSettings,
  copyText,
  onChange,
  showError,
  showToast,
  confirmAction = platformConfirm,
} = {}) {
  const confirm = confirmAction;
  let oneTimeToken = "";
  let oneTimeTokenContext = "";
  let editingKeyID = "";
  let keyEditorOpen = false;
  let requestsOpen = false;
  let editingModelAlias = "";
  let modelEditorOpen = false;
  let loadSequence = 0;

  function ensureState() {
    if (!state.gatewayConfig || typeof state.gatewayConfig !== "object") state.gatewayConfig = normalizeGatewaySettings(state.settings || {});
    if (!state.gatewayStatus || typeof state.gatewayStatus !== "object") {
      const config = normalizeGatewaySettings({ gateway: state.gatewayConfig });
      state.gatewayStatus = normalizeGatewayRuntime({}, { desiredEnabled: config.enabled, allowRemote: config.allowRemote });
    }
    if (!Array.isArray(state.gatewayProtocols)) state.gatewayProtocols = [];
    if (!Array.isArray(state.gatewayAccounts)) state.gatewayAccounts = [];
    if (!Array.isArray(state.gatewayKeys)) state.gatewayKeys = [];
    if (!Array.isArray(state.gatewayModels)) state.gatewayModels = [];
    if (!Array.isArray(state.gatewayRequests)) state.gatewayRequests = [];
    if (!state.gatewayUsage || typeof state.gatewayUsage !== "object") state.gatewayUsage = { items: [], summary: {} };
    if (typeof state.gatewayDataLoaded !== "boolean") state.gatewayDataLoaded = false;
    if (typeof state.gatewayDataLoading !== "boolean") state.gatewayDataLoading = false;
    if (typeof state.gatewayAPIError !== "string") state.gatewayAPIError = "";
    if (!state.apiTunnel || typeof state.apiTunnel !== "object") state.apiTunnel = normalizeApiTunnel({});
  }

  function gateway() {
    return normalizeGatewaySettings({ gateway: state.gatewayConfig });
  }

  function runtime() {
    const config = gateway();
    return normalizeGatewayRuntime(state.gatewayStatus, { desiredEnabled: config.enabled, allowRemote: config.allowRemote });
  }

  function changed() {
    onChange?.();
  }

  function setError(error) {
    state.gatewayAPIError = error?.message || String(error || t("sharedAPI.requestFailed"));
    changed();
  }

  async function perform(path, options) {
    try {
      const result = await request(path, options);
      state.gatewayAPIError = "";
      return result;
    } catch (error) {
      setError(error);
      throw error;
    }
  }

  function applyGatewayPayload(payload = {}, fallbackConfig = {}) {
    const root = objectValue(payload);
    const currentConfig = gateway();
    const currentRuntime = runtime();
    const nestedStatus = objectValue(root.status);
    const statusSource = Object.keys(nestedStatus).length ? nestedStatus : root;
    const statusFields = ["desiredEnabled", "running", "address", "allowRemote", "ephemeralIsolation", "lastError", "updatedAt"];
    const mergedStatus = { ...currentRuntime };
    statusFields.forEach((field) => {
      if (hasOwn(statusSource, field)) mergedStatus[field] = statusSource[field];
    });
    if (hasOwn(fallbackConfig, "enabled") && !hasOwn(statusSource, "desiredEnabled")) mergedStatus.desiredEnabled = Boolean(fallbackConfig.enabled);
    if (hasOwn(fallbackConfig, "allowRemote") && !hasOwn(statusSource, "allowRemote")) mergedStatus.allowRemote = Boolean(fallbackConfig.allowRemote);

    const directConfig = {};
    ["enabled", "host", "port", "allowRemote", "maxGlobalConcurrency", "maxRequestBytes"].forEach((field) => {
      if (hasOwn(root, field)) directConfig[field] = root[field];
    });
    const responseConfig = objectValue(root.gateway);
    state.gatewayConfig = normalizeGatewaySettings({ gateway: { ...currentConfig, ...fallbackConfig, ...directConfig, ...responseConfig } });
    state.gatewayStatus = normalizeGatewayRuntime(mergedStatus, {
      desiredEnabled: state.gatewayConfig.enabled,
      allowRemote: state.gatewayConfig.allowRemote,
    });
    if (Array.isArray(root.protocols)) state.gatewayProtocols = listValue(root.protocols);
    if (state.settings && typeof state.settings === "object") {
      state.settings.gateway = { ...objectValue(state.settings.gateway), ...state.gatewayConfig };
    }
  }

  async function load({ refreshSettings = false } = {}) {
    ensureState();
    const sequence = ++loadSequence;
    state.gatewayDataLoading = true;
    state.gatewayAPIError = "";
    try {
      if (refreshSettings) {
        await reloadSettings?.();
        state.gatewayConfig = normalizeGatewaySettings(state.settings || {});
      }
      if (sequence !== loadSequence) return false;
      const results = await Promise.allSettled([
        request(endpoints.status),
        request(endpoints.accounts),
        request(endpoints.keys),
        request(endpoints.usage),
        request(endpoints.requests),
        request(endpoints.tunnel),
        request(endpoints.models),
      ]);
      if (sequence !== loadSequence) return false;
      if (results[0].status === "fulfilled") applyGatewayPayload(results[0].value);
      if (results[1].status === "fulfilled") state.gatewayAccounts = normalizeGatewayAccounts(results[1].value);
      if (results[2].status === "fulfilled") state.gatewayKeys = normalizeGatewayKeys(results[2].value);
      if (results[3].status === "fulfilled") {
        const usage = objectValue(results[3].value);
        state.gatewayUsage = { items: Array.isArray(usage.items) ? usage.items : [], summary: objectValue(usage.summary) };
      }
      if (results[4].status === "fulfilled") state.gatewayRequests = normalizeGatewayRequests(results[4].value);
      if (results[5].status === "fulfilled") state.apiTunnel = normalizeApiTunnel(results[5].value);
      if (results[6].status === "fulfilled") {
        const raw = Array.isArray(results[6].value) ? results[6].value : (results[6].value?.models ?? []);
        state.gatewayModels = raw.map(normalizeGatewayModelItem).filter((m) => m.alias);
      }
      const failure = results.find((result) => result.status === "rejected");
      if (failure) throw failure.reason;
      state.gatewayDataLoaded = true;
      return true;
    } catch (error) {
      if (sequence !== loadSequence) return false;
      state.gatewayAPIError = error?.message || String(error || t("sharedAPI.requestFailed"));
      throw error;
    } finally {
      if (sequence === loadSequence) {
        state.gatewayDataLoaded = true;
        state.gatewayDataLoading = false;
        changed();
      }
    }
  }

  async function refreshAfterConflict(error) {
    if (error?.status !== 409) throw error;
    let refreshed = false;
    try {
      refreshed = await load({ refreshSettings: true });
    } catch {}
    const message = t(refreshed ? "sharedAPI.conflict" : "sharedAPI.conflictRefreshFailed");
    state.gatewayAPIError = message;
    changed();
    throw new Error(message);
  }

  async function updateGatewayConfig(draft = {}) {
    const payload = gatewayConfigPayload(draft);
    const needsConfirmation = gatewayConfigNeedsRemoteConfirmation(gateway(), payload, runtime());
    if (needsConfirmation && !await confirm(t("sharedAPI.remoteRiskConfirm"))) return null;
    const call = gatewayConfigRequest(payload, needsConfirmation);
    const result = await perform(call.path, call.options);
    applyGatewayPayload(result, payload);
    if (hasOwn(payload, "enabled") && Object.keys(payload).length === 1) {
      showToast?.(t(payload.enabled ? "sharedAPI.gatewayStarted" : "sharedAPI.gatewayStopped"));
    } else {
      showToast?.(t("sharedAPI.gatewayConfigSaved"));
    }
    changed();
    return result;
  }

  function setGatewayEnabled(enabled) {
    return updateGatewayConfig({ enabled: Boolean(enabled) });
  }

  async function controlApiTunnel(method, suffix = "") {
    const result = await perform(`${endpoints.tunnel}${suffix}`, { method });
    state.apiTunnel = normalizeApiTunnel(result);
    changed();
    return result;
  }

  async function startApiTunnel() {
    if (!await confirm(t("sharedAPI.apiTunnelStartConfirm"))) return null;
    const savedTunnel = state.apiTunnel;
    state.apiTunnel = normalizeApiTunnel({ ...state.apiTunnel, status: "starting" });
    changed();
    try {
      return await controlApiTunnel("POST");
    } catch (error) {
      state.apiTunnel = savedTunnel;
      changed();
      throw error;
    }
  }

  async function stopApiTunnel() {
    const savedTunnel = state.apiTunnel;
    state.apiTunnel = normalizeApiTunnel({ ...state.apiTunnel, status: "stopping" });
    changed();
    try {
      return await controlApiTunnel("DELETE");
    } catch (error) {
      state.apiTunnel = savedTunnel;
      changed();
      throw error;
    }
  }

  async function installCloudflaredForApiTunnel() {
    return controlApiTunnel("POST", "/install");
  }

  async function copyApiTunnelUrl() {
    const url = normalizeApiTunnel(state.apiTunnel || {}).publicUrl;
    if (!url) return false;
    try {
      if (await copyText?.(url) === true) {
        showToast?.(t("sharedAPI.apiTunnelUrlCopied"));
        return true;
      }
    } catch {}
    return false;
  }

  function revealToken(result, context) {
    oneTimeToken = textValue(result?.token);
    oneTimeTokenContext = textValue(context);
  }

  function dismissToken() {
    oneTimeToken = "";
    oneTimeTokenContext = "";
    changed();
  }

  async function copyOneTimeToken() {
    if (!oneTimeToken) return false;
    try {
      if (await copyText?.(oneTimeToken) === true) {
        showToast?.(t("sharedAPI.tokenCopied"));
        return true;
      }
    } catch {}
    showToast?.(t("sharedAPI.tokenCopyFailed"), "warn");
    return false;
  }

  async function createKey(draft) {
    const call = gatewayKeyRequest("create", {}, draft);
    const result = await perform(call.path, call.options);
    if (result?.key) state.gatewayKeys = replaceBy(state.gatewayKeys, normalizeGatewayKey(result.key), "id");
    revealToken(result, result?.key?.name || draft?.name || t("sharedAPI.newKey"));
    keyEditorOpen = false;
    editingKeyID = "";
    showToast?.(t("sharedAPI.keyCreated"));
    changed();
    return result;
  }

  async function updateKey(id, draft) {
    const key = state.gatewayKeys.find((item) => item.id === id) || { id };
    const call = gatewayKeyRequest("update", key, draft);
    let result;
    try {
      result = await perform(call.path, call.options);
    } catch (error) {
      return refreshAfterConflict(error);
    }
    if (result?.key) state.gatewayKeys = replaceBy(state.gatewayKeys, normalizeGatewayKey(result.key), "id");
    editingKeyID = "";
    showToast?.(t("sharedAPI.keyUpdated"));
    changed();
    return result;
  }

  async function toggleKey(id) {
    const key = state.gatewayKeys.find((item) => item.id === id);
    if (!key || key.revokedAt) return null;
    const call = gatewayKeyRequest("toggle", key);
    let result;
    try {
      result = await perform(call.path, call.options);
    } catch (error) {
      return refreshAfterConflict(error);
    }
    state.gatewayKeys = replaceBy(state.gatewayKeys, normalizeGatewayKey(result?.key || { ...key, enabled: !key.enabled }), "id");
    showToast?.(t(key.enabled ? "sharedAPI.keyPaused" : "sharedAPI.keyResumed"));
    changed();
    return result;
  }

  async function rotateKey(id) {
    const key = state.gatewayKeys.find((item) => item.id === id);
    if (!key || key.revokedAt || !await confirm(t("sharedAPI.rotateConfirm", { name: key.name || key.keyPrefix }))) return null;
    const call = gatewayKeyRequest("rotate", key);
    const result = await perform(call.path, call.options);
    if (result?.key) state.gatewayKeys = replaceBy(state.gatewayKeys, normalizeGatewayKey(result.key), "id");
    revealToken(result, result?.key?.name || key.name || key.keyPrefix);
    showToast?.(t("sharedAPI.keyRotated"));
    changed();
    return result;
  }

  async function revokeKey(id) {
    const key = state.gatewayKeys.find((item) => item.id === id);
    if (!key || key.revokedAt || !await confirm(t("sharedAPI.revokeConfirm", { name: key.name || key.keyPrefix }))) return null;
    const call = gatewayKeyRequest("revoke", key);
    const result = await perform(call.path, call.options);
    state.gatewayKeys = replaceBy(state.gatewayKeys, normalizeGatewayKey(result?.key || { ...key, enabled: false, revokedAt: new Date().toISOString() }), "id");
    if (editingKeyID === id) editingKeyID = "";
    showToast?.(t("sharedAPI.keyRevoked"));
    changed();
    return result;
  }

  async function deleteKey(id) {
    const key = state.gatewayKeys.find((item) => item.id === id);
    if (!key || !await confirm(t("sharedAPI.deleteConfirm", { name: key.name || key.keyPrefix }))) return null;
    const call = gatewayKeyRequest("delete", key);
    await perform(call.path, call.options);
    state.gatewayKeys = state.gatewayKeys.filter((item) => item.id !== id);
    if (editingKeyID === id) editingKeyID = "";
    showToast?.(t("sharedAPI.keyDeleted"));
    changed();
    return true;
  }

  async function toggleProvider(name, enabled) {
    const provider = (state.settings?.providers || []).find((item) => item.name === name);
    if (!provider || gatewayProviderRestriction(provider)) return null;
    const call = gatewayProviderRequest(name, enabled);
    const result = await perform(call.path, call.options);
    provider.gatewayEnabled = Boolean(enabled);
    showToast?.(t(enabled ? "sharedAPI.providerShared" : "sharedAPI.providerUnshared", { name: providerLabel(provider) }));
    changed();
    return result;
  }

  async function toggleAccount(provider, accountId, shared) {
    const account = state.gatewayAccounts.find((item) => item.provider === provider && item.accountId === accountId);
    if (!account || accountRestriction(account)) return null;
    const call = gatewayAccountRequest(provider, accountId, shared);
    const result = await perform(call.path, call.options);
    const responseAccount = objectValue(result?.account);
    const directAccount = Object.keys(responseAccount).length ? responseAccount : objectValue(result);
    const merged = normalizeGatewayAccount({ ...account, ...directAccount, provider, accountId, shared: Boolean(shared) });
    if (!hasOwn(directAccount, "effective")) merged.effective = merged.shared && merged.eligible && !merged.disabled && !accountIsProfile(merged);
    state.gatewayAccounts = replaceAccount(state.gatewayAccounts, merged);
    showToast?.(t(shared ? "sharedAPI.accountShared" : "sharedAPI.accountUnshared", { label: merged.label || merged.accountId }));
    changed();
    return result;
  }

  function normalizeGatewayModelItem(m = {}) {
    return { alias: textValue(m.alias), targetModel: textValue(m.targetModel), enabled: m.enabled !== false, createdAt: textValue(m.createdAt), updatedAt: textValue(m.updatedAt) };
  }

  function replaceByAlias(items, item) {
    const index = items.findIndex((m) => m.alias === item.alias);
    if (index < 0) return [item, ...items];
    return items.map((m, i) => (i === index ? item : m));
  }

  async function createModel(draft) {
    const result = await perform(endpoints.models, { method: "POST", body: JSON.stringify({ alias: draft.alias, targetModel: draft.targetModel, enabled: draft.enabled !== false }) });
    const m = result?.model || result;
    if (m?.alias) state.gatewayModels = replaceByAlias(state.gatewayModels, normalizeGatewayModelItem(m));
    modelEditorOpen = false;
    editingModelAlias = "";
    showToast?.(t("sharedAPI.modelCreated"));
    changed();
    return result;
  }

  async function updateModel(alias, draft) {
    const result = await perform(`${endpoints.models}?alias=${encoded(alias)}`, { method: "PATCH", body: JSON.stringify({ alias: draft.alias, targetModel: draft.targetModel, enabled: draft.enabled !== false, expectedUpdatedAt: draft.expectedUpdatedAt }) });
    const m = result?.model || result;
    if (m?.alias) state.gatewayModels = replaceByAlias(state.gatewayModels, normalizeGatewayModelItem(m));
    editingModelAlias = "";
    showToast?.(t("sharedAPI.modelUpdated"));
    changed();
    return result;
  }

  async function deleteModel(alias) {
    if (!await confirm(t("sharedAPI.modelDeleteConfirm", { alias }))) return null;
    await perform(`${endpoints.models}?alias=${encoded(alias)}`, { method: "DELETE" });
    state.gatewayModels = state.gatewayModels.filter((m) => m.alias !== alias);
    if (editingModelAlias === alias) editingModelAlias = "";
    showToast?.(t("sharedAPI.modelDeleted"));
    changed();
    return true;
  }

  async function toggleModel(alias) {
    const model = state.gatewayModels.find((m) => m.alias === alias);
    if (!model) return null;
    const result = await perform(`${endpoints.models}?alias=${encoded(alias)}`, { method: "PATCH", body: JSON.stringify({ enabled: !model.enabled, expectedUpdatedAt: model.updatedAt }) });
    const updated = result?.model || result;
    state.gatewayModels = replaceByAlias(state.gatewayModels, normalizeGatewayModelItem({ ...model, enabled: !model.enabled, ...(updated?.alias ? updated : {}) }));
    changed();
    return result;
  }

  function renderOpenSection({ sectionClass, title, description, actions = "", body = "" }) {
    return `
      <section class="compact-settings-section ${sectionClass}">
        <div class="shared-api-section-head">
          <div class="compact-settings-section-copy"><h2>${escapeHtml(title)}</h2><p data-settings-help-copy>${escapeHtml(description)}</p></div>
          ${actions ? `<div class="shared-api-section-actions">${actions}</div>` : ""}
        </div>
        ${body ? `<div class="compact-settings-section-controls">${body}</div>` : ""}
      </section>`;
  }

  function renderGateway() {
    const value = gateway();
    const status = runtime();
    const desiredEnabled = status.desiredEnabled;
    const actualAddress = status.address || gatewayAddress(value);
    return renderOpenSection({
      sectionClass: "shared-api-gateway-section",
      title: t("sharedAPI.gatewayTitle"),
      description: t("sharedAPI.gatewayDescription"),
      actions: `<span class="settings-badge ${status.running ? "ok" : "warn"}">${escapeHtml(t(status.running ? "sharedAPI.runtimeRunning" : "sharedAPI.runtimeStopped"))}</span>`,
      body: `
          <dl class="shared-api-gateway-meta">
            <div><dt>${escapeHtml(t("sharedAPI.actualStatus"))}</dt><dd>${escapeHtml(t(status.running ? "sharedAPI.running" : "sharedAPI.stopped"))}</dd></div>
            <div><dt>${escapeHtml(t("sharedAPI.desiredState"))}</dt><dd>${escapeHtml(t(desiredEnabled ? "sharedAPI.desiredEnabled" : "sharedAPI.desiredDisabled"))}</dd></div>
            <div><dt>${escapeHtml(t("sharedAPI.actualAddress"))}</dt><dd>${escapeHtml(actualAddress)}</dd></div>
            <div><dt>${escapeHtml(t("sharedAPI.protocols"))}</dt><dd>${escapeHtml(state.gatewayProtocols.length ? state.gatewayProtocols.join(", ") : t("sharedAPI.none"))}</dd></div>
            <div><dt>${escapeHtml(t("sharedAPI.ephemeralIsolation"))}</dt><dd>${escapeHtml(t(status.ephemeralIsolation ? "sharedAPI.yes" : "sharedAPI.no"))}</dd></div>
            <div><dt>${escapeHtml(t("sharedAPI.lastUpdated"))}</dt><dd>${escapeHtml(formatDate(status.updatedAt))}</dd></div>
          </dl>
          ${status.lastError ? `<div class="settings-inline-alert settings-alert shared-api-runtime-error" role="alert">${escapeHtml(t("sharedAPI.lastError", { message: status.lastError }))}</div>` : ""}
          <form class="compact-settings-editor shared-api-gateway-form" data-gateway-config-form>
            <div class="compact-settings-grid two-column">
              <label class="settings-form-field">${escapeHtml(t("sharedAPI.host"))}<input class="settings-field" name="host" value="${escapeAttr(value.host)}" autocomplete="off" /></label>
              <label class="settings-form-field">${escapeHtml(t("sharedAPI.port"))}<input class="settings-field" name="port" type="number" min="0" max="65535" step="1" value="${escapeAttr(value.port)}" /></label>
              <label class="settings-form-field">${escapeHtml(t("sharedAPI.globalConcurrency"))}<input class="settings-field" name="maxGlobalConcurrency" type="number" min="0" step="1" value="${escapeAttr(value.maxGlobalConcurrency)}" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.zeroUnlimited"))}</small></label>
              <label class="settings-form-field">${escapeHtml(t("sharedAPI.maxRequestBytes"))}<input class="settings-field" name="maxRequestBytes" type="number" min="0" step="1" value="${escapeAttr(value.maxRequestBytes)}" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.zeroUnlimited"))}</small></label>
            </div>
            <label class="compact-settings-switch-row"><span><strong>${escapeHtml(t("sharedAPI.allowRemote"))}</strong><small data-settings-help-copy>${escapeHtml(t("sharedAPI.allowRemoteHint"))}</small></span><input name="allowRemote" type="checkbox" ${value.allowRemote ? "checked" : ""} /></label>
            <div class="settings-inline-actions compact-settings-editor-actions shared-api-gateway-actions"><button class="settings-action-btn ${desiredEnabled ? "danger" : "primary"}" type="button" data-gateway-toggle="${desiredEnabled ? "false" : "true"}">${escapeHtml(t(desiredEnabled ? "sharedAPI.stopGateway" : "sharedAPI.startGateway"))}</button><button class="settings-action-btn subtle" type="submit">${escapeHtml(t("sharedAPI.saveGatewayConfig"))}</button></div>
          </form>`,
    });
  }

  function apiTunnelStatusLabel(status) {
    const labels = {
      idle: t("sharedAPI.apiTunnelStop") ? "" : "",
      running: t("sharedAPI.runtimeRunning"),
      starting: "…",
      stopping: "…",
      installing: "…",
      error: t("sharedAPI.requestFailed"),
      unavailable: t("sharedAPI.runtimeStopped"),
    };
    // Prefer dedicated per-state label from i18n; fall through to raw status.
    return textValue(labels[status] ?? status);
  }

  function renderApiTunnel() {
    const tunnel = normalizeApiTunnel(state.apiTunnel || {});
    const isRunning = tunnel.status === "running";
    const isBusy = ["starting", "stopping", "installing"].includes(tunnel.status);
    const gatewayDown = !tunnel.gatewayRunning;
    const noKeys = isRunning && tunnel.activeKeys === 0;
    let badgeClass, badgeLabel;
    if (isRunning) { badgeClass = "ok"; badgeLabel = t("sharedAPI.apiTunnelRunning"); }
    else if (tunnel.status === "starting") { badgeClass = "muted"; badgeLabel = t("sharedAPI.apiTunnelStarting"); }
    else if (tunnel.status === "stopping") { badgeClass = "muted"; badgeLabel = t("sharedAPI.apiTunnelStopping"); }
    else if (tunnel.status === "installing") { badgeClass = "muted"; badgeLabel = t("sharedAPI.apiTunnelInstalling"); }
    else if (tunnel.status === "error") { badgeClass = "warn"; badgeLabel = t("sharedAPI.requestFailed"); }
    else { badgeClass = "muted"; badgeLabel = t("sharedAPI.apiTunnelStopped"); }
    const actions = isBusy
      ? `<button class="settings-action-btn subtle" type="button" disabled aria-busy="true">${escapeHtml(badgeLabel)}</button>`
      : tunnel.installable
        ? `<button class="settings-action-btn subtle" type="button" data-api-tunnel-install>${escapeHtml(t("sharedAPI.apiTunnelInstall"))}</button>`
        : tunnel.available && !isRunning
          ? `<button class="settings-action-btn ${gatewayDown ? "subtle" : "primary"}" type="button" data-api-tunnel-start ${gatewayDown ? "disabled" : ""}>${escapeHtml(t("sharedAPI.apiTunnelStart"))}</button>`
          : isRunning
            ? `<button class="settings-action-btn danger" type="button" data-api-tunnel-stop>${escapeHtml(t("sharedAPI.apiTunnelStop"))}</button>`
            : "";
    const extras = [
      isRunning && tunnel.publicUrl ? `<div class="shared-api-tunnel-url-row"><code>${escapeHtml(tunnel.publicUrl)}</code><button class="settings-action-btn subtle" type="button" data-api-tunnel-copy-url>${escapeHtml(t("sharedAPI.apiTunnelCopyUrl"))}</button></div>` : "",
      noKeys ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(t("sharedAPI.apiTunnelNoKeys"))}</div>` : "",
      gatewayDown && !isRunning && !isBusy ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(t("sharedAPI.apiTunnelGatewayDown"))}</div>` : "",
      tunnel.error && tunnel.status === "error" ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(tunnel.error)}</div>` : "",
    ].filter(Boolean).join("");
    return `
      <section class="compact-settings-section shared-api-tunnel-section">
        <div class="shared-api-section-head">
          <div class="compact-settings-section-copy"><h2>${escapeHtml(t("sharedAPI.apiTunnelTitle"))}</h2><p data-settings-help-copy>${escapeHtml(t("sharedAPI.apiTunnelDescription"))}</p></div>
          <div class="shared-api-section-actions"><span class="settings-status-pill ${badgeClass}">${escapeHtml(badgeLabel)}</span>${actions}</div>
        </div>
        ${extras ? `<div class="compact-settings-section-controls">${extras}</div>` : ""}
      </section>`;
  }

  function renderProviders() {
    const providers = Array.isArray(state.settings?.providers) ? state.settings.providers : [];
    return renderOpenSection({
      sectionClass: "shared-api-providers-section",
      title: t("sharedAPI.providersTitle"),
      description: t("sharedAPI.providersDescription"),
      body: `<div class="shared-api-list">
            ${providers.length ? providers.map((provider) => {
              const restriction = gatewayProviderRestriction(provider);
              return `<div class="shared-api-row ${restriction ? "is-disabled" : ""}"><span><strong>${escapeHtml(providerLabel(provider))}</strong><small>${escapeHtml(restriction ? t("sharedAPI.oauthProxyUnavailable") : t(provider.gatewayEnabled ? "sharedAPI.providerEligible" : "sharedAPI.providerPrivate"))}</small></span>${restriction ? `<span class="settings-badge">${escapeHtml(t("sharedAPI.notShareable"))}</span>` : `<label class="shared-api-switch"><input type="checkbox" data-gateway-provider="${escapeAttr(provider.name)}" ${provider.gatewayEnabled ? "checked" : ""} /><span>${escapeHtml(t("sharedAPI.shareProvider"))}</span></label>`}</div>`;
            }).join("") : `<div class="settings-empty-state">${escapeHtml(t("sharedAPI.noProviders"))}</div>`}
          </div>`,
    });
  }

  function renderAccounts() {
    const groups = new Map();
    state.gatewayAccounts.forEach((account) => {
      if (!groups.has(account.provider)) groups.set(account.provider, []);
      groups.get(account.provider).push(account);
    });
    return renderOpenSection({
      sectionClass: "shared-api-accounts-section",
      title: t("sharedAPI.accountsTitle"),
      description: t("sharedAPI.accountsDescription"),
      actions: `<span class="settings-badge">${escapeHtml(t("sharedAPI.accountCount", { count: state.gatewayAccounts.length }))}</span>`,
      body: `<div class="shared-api-account-groups">${groups.size ? [...groups.entries()].map(([provider, accounts]) => `<div class="shared-api-account-group"><div class="shared-api-account-group-heading"><strong>${escapeHtml(provider)}</strong><span>${escapeHtml(t("sharedAPI.accountCount", { count: accounts.length }))}</span></div><div class="shared-api-list">${accounts.map((account) => {
            const restriction = accountRestriction(account);
            const statusKey = restriction ? "accountUnavailable" : account.effective ? "accountEffective" : account.shared ? "accountPending" : "accountPrivate";
            const auth = account.authType || t("sharedAPI.none");
            const source = account.source || t("sharedAPI.none");
            return `<div class="shared-api-row shared-api-account-row ${restriction ? "is-disabled" : ""}"><span><strong>${escapeHtml(account.label || account.accountId)} <code>${escapeHtml(account.accountId)}</code></strong><small>${escapeHtml(t("sharedAPI.accountMeta", { auth, source, priority: formatNumber(account.priority) }))}</small>${restriction ? `<small class="shared-api-account-reason">${escapeHtml(restriction)}</small>` : ""}</span><div class="shared-api-account-actions"><span class="settings-badge ${account.effective ? "ok" : restriction ? "warn" : ""}">${escapeHtml(t(`sharedAPI.${statusKey}`))}</span><label class="shared-api-switch"><input type="checkbox" data-gateway-account-provider="${escapeAttr(account.provider)}" data-gateway-account-id="${escapeAttr(account.accountId)}" ${account.shared ? "checked" : ""} ${restriction ? "disabled" : ""} /><span>${escapeHtml(t("sharedAPI.shareAccount"))}</span></label></div></div>`;
          }).join("")}</div></div>`).join("") : `<div class="settings-empty-state shared-api-compact-empty">${escapeHtml(t("sharedAPI.noAccounts"))}</div>`}</div>`,
    });
  }

  function renderKeyForm(key = {}) {
    const value = keyEditorValues(key);
    const editing = Boolean(key.id);
    return `<form class="compact-settings-editor shared-api-key-form" data-gateway-key-form="${escapeAttr(editing ? key.id : "new")}">
      <div class="compact-settings-grid two-column">
        <label class="settings-form-field">${escapeHtml(t("sharedAPI.keyName"))}<input class="settings-field" name="name" value="${escapeAttr(value.name)}" required autocomplete="off" /></label>
        <label class="settings-form-field">${escapeHtml(t("sharedAPI.expiresAt"))}<input class="settings-field" name="expiresAt" type="datetime-local" value="${escapeAttr(value.expiresAt)}" /></label>
        <label class="settings-form-field">${escapeHtml(t("sharedAPI.requestsPerMinute"))}<input class="settings-field" name="requestsPerMinute" type="number" min="0" step="1" value="${escapeAttr(value.requestsPerMinute)}" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.zeroUnlimited"))}</small></label>
        <label class="settings-form-field">${escapeHtml(t("sharedAPI.monthlyTokenLimit"))}<input class="settings-field" name="monthlyTokenLimit" type="number" min="0" step="1" value="${escapeAttr(value.monthlyTokenLimit)}" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.zeroUnlimited"))}</small></label>
        <label class="settings-form-field">${escapeHtml(t("sharedAPI.maxConcurrency"))}<input class="settings-field" name="maxConcurrency" type="number" min="0" step="1" value="${escapeAttr(value.maxConcurrency)}" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.zeroUnlimited"))}</small></label>
        <label class="settings-form-field full-width">${escapeHtml(t("sharedAPI.allowedModels"))}<textarea class="settings-field" name="allowedModels" placeholder="public-chat\npublic-code">${escapeHtml(value.allowedModels)}</textarea><small data-settings-help-copy>${escapeHtml(t("sharedAPI.allowedModelsHint"))}</small></label>
      </div>
      <label class="compact-settings-switch-row"><span><strong>${escapeHtml(t("sharedAPI.keyEnabled"))}</strong><small data-settings-help-copy>${escapeHtml(t("sharedAPI.keyEnabledHint"))}</small></span><input name="enabled" type="checkbox" ${value.enabled ? "checked" : ""} /></label>
      <div class="settings-inline-actions compact-settings-editor-actions"><button class="settings-action-btn subtle" type="button" data-gateway-key-cancel>${escapeHtml(t("sharedAPI.cancel"))}</button><button class="settings-action-btn primary" type="submit">${escapeHtml(t(editing ? "sharedAPI.save" : "sharedAPI.createKey"))}</button></div>
    </form>`;
  }

  function renderOneTimeToken() {
    if (!oneTimeToken) return "";
    return `<div class="shared-api-token settings-inline-alert" role="status"><div><strong>${escapeHtml(t("sharedAPI.tokenTitle", { name: oneTimeTokenContext }))}</strong><span>${escapeHtml(t("sharedAPI.tokenNotice"))}</span></div><code>${escapeHtml(oneTimeToken)}</code><div class="settings-inline-actions"><button class="settings-action-btn primary" type="button" data-gateway-token-copy>${escapeHtml(t("sharedAPI.copyToken"))}</button><button class="settings-action-btn subtle" type="button" data-gateway-token-dismiss>${escapeHtml(t("sharedAPI.closeToken"))}</button></div></div>`;
  }

  function renderKey(key) {
    const status = keyStatus(key);
    const usage = keyUsageValue(key);
    const modelText = key.allowedModels.length ? key.allowedModels.join(", ") : t("sharedAPI.allModels");
    const quota = key.monthlyTokenLimit ? `${formatNumber(usage)} / ${formatNumber(key.monthlyTokenLimit)}` : t("sharedAPI.unlimited");
    return `<article class="shared-api-key-card ${key.revokedAt ? "is-revoked" : ""}">
      <div class="shared-api-key-head"><span><strong>${escapeHtml(key.name || t("sharedAPI.unnamedKey"))}</strong><code>${escapeHtml(key.keyPrefix || "—")}</code></span><span class="settings-badge ${status.tone}">${escapeHtml(t(`sharedAPI.status.${status.key}`))}</span></div>
      <dl class="shared-api-key-meta"><div><dt>${escapeHtml(t("sharedAPI.lastUsed"))}</dt><dd>${escapeHtml(formatDate(key.lastUsedAt))}</dd></div><div><dt>${escapeHtml(t("sharedAPI.monthlyQuota"))}</dt><dd>${escapeHtml(quota)}</dd></div><div><dt>${escapeHtml(t("sharedAPI.allowedModels"))}</dt><dd title="${escapeAttr(modelText)}">${escapeHtml(modelText)}</dd></div><div><dt>${escapeHtml(t("sharedAPI.expiresAt"))}</dt><dd>${escapeHtml(formatDate(key.expiresAt))}</dd></div></dl>
      <div class="settings-inline-actions shared-api-key-actions">${key.revokedAt ? `<button class="settings-action-btn danger" type="button" data-gateway-key-delete="${escapeAttr(key.id)}">${escapeHtml(t("sharedAPI.delete"))}</button>` : `<button class="settings-action-btn subtle" type="button" data-gateway-key-edit="${escapeAttr(key.id)}">${escapeHtml(t("sharedAPI.edit"))}</button><button class="settings-action-btn subtle" type="button" data-gateway-key-toggle="${escapeAttr(key.id)}">${escapeHtml(t(key.enabled ? "sharedAPI.pause" : "sharedAPI.resume"))}</button><button class="settings-action-btn subtle" type="button" data-gateway-key-rotate="${escapeAttr(key.id)}">${escapeHtml(t("sharedAPI.rotate"))}</button><button class="settings-action-btn danger" type="button" data-gateway-key-revoke="${escapeAttr(key.id)}">${escapeHtml(t("sharedAPI.revoke"))}</button>`}</div>
    </article>`;
  }

  function renderKeys() {
    return renderOpenSection({
      sectionClass: "shared-api-keys-section",
      title: t("sharedAPI.keysTitle"),
      description: t("sharedAPI.keysDescription"),
      actions: `<span class="settings-badge">${escapeHtml(t("sharedAPI.keyCount", { count: state.gatewayKeys.length }))}</span><button class="settings-action-btn primary" type="button" data-gateway-key-add>${escapeHtml(t("sharedAPI.addKey"))}</button>`,
      body: `
          ${renderOneTimeToken()}
          ${keyEditorOpen && !editingKeyID ? renderKeyForm() : ""}
          <div class="shared-api-key-list">${state.gatewayKeys.length ? state.gatewayKeys.map((key) => editingKeyID === key.id ? renderKeyForm(key) : renderKey(key)).join("") : `<div class="settings-empty-state shared-api-compact-empty">${escapeHtml(t("sharedAPI.noKeys"))}</div>`}</div>`,
    });
  }

  function renderModelForm(model = {}) {
    const editing = Boolean(model.alias);
    const providers = Array.isArray(state.settings?.providers) ? state.settings.providers : [];
    // The target is picked from the models the user has opened to the Gateway
    // instead of typed as free text; the alias is then filled in after it.
    const available = [...new Set(providers.filter((p) => p.gatewayEnabled && p.configured).flatMap((p) => {
      const configs = Array.isArray(p.modelConfigs) ? p.modelConfigs : [];
      return configs.map((c) => c.name || "").filter(Boolean).map((m) => `${p.name}:${m}`);
    }))];
    // An alias saved before its provider was closed off must keep showing its
    // real target while being edited.
    if (model.targetModel && !available.includes(model.targetModel)) available.unshift(model.targetModel);
    const targetField = available.length
      ? `<label class="settings-form-field">${escapeHtml(t("sharedAPI.modelTarget"))}<select class="settings-field" name="targetModel" required>${model.targetModel ? "" : `<option value="" disabled selected>${escapeHtml(t("sharedAPI.modelTargetPlaceholder"))}</option>`}${available.map((s) => `<option value="${escapeAttr(s)}" ${s === model.targetModel ? "selected" : ""}>${escapeHtml(s)}</option>`).join("")}</select><small data-settings-help-copy>${escapeHtml(t("sharedAPI.modelTargetSelectHint"))}</small></label>`
      : `<label class="settings-form-field">${escapeHtml(t("sharedAPI.modelTarget"))}<input class="settings-field" name="targetModel" value="${escapeAttr(model.targetModel || "")}" required autocomplete="off" /><small data-settings-help-copy>${escapeHtml(t("sharedAPI.modelTargetHint"))}</small></label>`;
    return `<form class="compact-settings-editor shared-api-model-form" data-gateway-model-form="${escapeAttr(editing ? model.alias : "new")}"><div class="compact-settings-grid two-column">${targetField}<label class="settings-form-field">${escapeHtml(t("sharedAPI.modelAlias"))}<input class="settings-field" name="alias" value="${escapeAttr(model.alias || "")}" required autocomplete="off" /></label></div><label class="compact-settings-switch-row"><span><strong>${escapeHtml(t("sharedAPI.modelEnabled"))}</strong></span><input name="enabled" type="checkbox" ${model.enabled !== false ? "checked" : ""} /></label><div class="settings-inline-actions compact-settings-editor-actions"><button class="settings-action-btn subtle" type="button" data-gateway-model-cancel>${escapeHtml(t("sharedAPI.cancel"))}</button><button class="settings-action-btn primary" type="submit">${escapeHtml(t(editing ? "sharedAPI.save" : "sharedAPI.addModel"))}</button></div></form>`;
  }

  function renderModel(model) {
    return `<div class="shared-api-row"><span><strong>${escapeHtml(model.alias)}</strong><small>${escapeHtml(model.targetModel)}</small></span><div class="settings-inline-actions"><span class="settings-status-pill ${model.enabled ? "ok" : "muted"}">${escapeHtml(t(model.enabled ? "sharedAPI.modelEnabled" : "sharedAPI.paused"))}</span><button class="settings-action-btn subtle" type="button" data-gateway-model-toggle="${escapeAttr(model.alias)}">${escapeHtml(t(model.enabled ? "sharedAPI.pause" : "sharedAPI.resume"))}</button><button class="settings-action-btn subtle" type="button" data-gateway-model-edit="${escapeAttr(model.alias)}">${escapeHtml(t("sharedAPI.edit"))}</button><button class="settings-action-btn danger" type="button" data-gateway-model-delete="${escapeAttr(model.alias)}">${escapeHtml(t("sharedAPI.delete"))}</button></div></div>`;
  }

  function renderModels() {
    // An alias publishes one provider model under a public name, so it can only
    // resolve once the Gateway is up and has an account to route to. Offering
    // the editor before that is a control that cannot do anything yet, and the
    // aliases it would create would point nowhere.
    if (!runtime().running || !state.gatewayAccounts.length) return "";
    return `
      <section class="compact-settings-section shared-api-models-section">
        <div class="shared-api-section-head">
          <div class="compact-settings-section-copy"><h2>${escapeHtml(t("sharedAPI.modelsTitle"))}</h2><p data-settings-help-copy>${escapeHtml(t("sharedAPI.modelsDescription"))}</p></div>
          <div class="shared-api-section-actions"><span class="settings-badge">${escapeHtml(t("sharedAPI.modelCount", { count: state.gatewayModels.length }))}</span><button class="settings-action-btn primary" type="button" data-gateway-model-add>${escapeHtml(t("sharedAPI.addModel"))}</button></div>
        </div>
        <div class="compact-settings-section-controls">
          ${modelEditorOpen && !editingModelAlias ? renderModelForm() : ""}
          <div class="shared-api-list">${state.gatewayModels.length ? state.gatewayModels.map((m) => editingModelAlias === m.alias ? renderModelForm(m) : renderModel(m)).join("") : `<div class="settings-empty-state shared-api-compact-empty">${escapeHtml(t("sharedAPI.noModels"))}</div>`}</div>
        </div>
      </section>`;
  }

  function renderUsage() {
    const summary = objectValue(state.gatewayUsage.summary);
    const values = [
      ["requests", summary.requests ?? summary.totalRequests],
      ["tokens", summary.tokens ?? summary.totalTokens],
      ["activeKeys", summary.activeKeys],
      ["errors", summary.errors ?? summary.errorCount],
    ].filter(([, value]) => value !== undefined && value !== null);
    return `<section class="compact-settings-section shared-api-usage-section"><div class="compact-settings-section-copy"><h2>${escapeHtml(t("sharedAPI.usageTitle"))}</h2><p data-settings-help-copy>${escapeHtml(t("sharedAPI.usageDescription"))}</p></div><div class="compact-settings-section-controls">${values.length ? `<div class="shared-api-usage-grid">${values.map(([key, value]) => `<div><strong>${escapeHtml(formatNumber(value))}</strong><span>${escapeHtml(t(`sharedAPI.usage.${key}`))}</span></div>`).join("")}</div>` : `<div class="settings-empty-state shared-api-compact-empty">${escapeHtml(t("sharedAPI.noUsage"))}</div>`}</div></section>`;
  }

  function renderRequest(record) {
    const protocol = [record.protocol, record.kind].filter(Boolean).join(" / ") || t("sharedAPI.none");
    const actualModel = [record.provider, record.model].filter(Boolean).join(":") || t("sharedAPI.none");
    const model = record.alias ? `${record.alias} → ${actualModel}` : actualModel;
    const account = [record.accountLabel, record.accountId].filter(Boolean).join(" · ") || t("sharedAPI.unknownAccount");
    const tokens = t("sharedAPI.tokenSummary", { input: formatNumber(record.inputTokens), output: formatNumber(record.outputTokens), total: formatNumber(record.totalTokens) });
    const timing = t("sharedAPI.timingSummary", { duration: formatMilliseconds(record.durationMs), ttft: formatMilliseconds(record.ttftMs) });
    return `<article class="shared-api-request-row ${record.error ? "has-error" : ""}"><div class="shared-api-request-head"><time>${escapeHtml(formatDate(record.createdAt))}</time><span class="settings-badge ${record.error ? "danger" : "ok"}">${escapeHtml(t(record.error ? "sharedAPI.requestErrorStatus" : "sharedAPI.requestSuccess"))}</span></div><dl class="shared-api-request-meta"><div><dt>${escapeHtml(t("sharedAPI.requestKey"))}</dt><dd>${escapeHtml(record.key || t("sharedAPI.unknownKey"))}</dd></div><div><dt>${escapeHtml(t("sharedAPI.requestProtocol"))}</dt><dd>${escapeHtml(protocol)}</dd></div><div><dt>${escapeHtml(t("sharedAPI.requestModel"))}</dt><dd><code>${escapeHtml(model)}</code></dd></div><div><dt>${escapeHtml(t("sharedAPI.requestAccount"))}</dt><dd>${escapeHtml(account)}</dd></div><div><dt>${escapeHtml(t("sharedAPI.requestTokens"))}</dt><dd>${escapeHtml(tokens)}</dd></div><div><dt>${escapeHtml(t("sharedAPI.requestTiming"))}</dt><dd>${escapeHtml(timing)}</dd></div></dl>${record.error ? `<div class="settings-inline-alert settings-alert shared-api-request-error" role="status">${escapeHtml(record.error)}</div>` : ""}</article>`;
  }

  function renderRequests() {
    return `<section class="compact-settings-section shared-api-requests-section"><details class="shared-api-requests-details"${requestsOpen ? " open" : ""}><summary class="compact-settings-section-summary"><div class="compact-settings-section-copy"><h2>${escapeHtml(t("sharedAPI.requestsTitle"))}</h2><p data-settings-help-copy>${escapeHtml(t("sharedAPI.requestsDescription"))}</p></div><span class="settings-badge">${escapeHtml(t("sharedAPI.requestCount", { count: state.gatewayRequests.length }))}</span></summary><div class="compact-settings-section-controls"><div class="shared-api-request-list">${state.gatewayRequests.length ? state.gatewayRequests.map(renderRequest).join("") : `<div class="settings-empty-state shared-api-compact-empty">${escapeHtml(t("sharedAPI.noRequests"))}</div>`}</div></div></details></section>`;
  }

  function render() {
    ensureState();
    const status = runtime();
    return `<div class="compact-settings-page shared-api-page">
      <header class="compact-settings-header"><div class="compact-settings-heading"><h1>${escapeHtml(t("sharedAPI.title"))}</h1><p data-settings-help-copy>${escapeHtml(t("sharedAPI.description"))}</p></div><div class="compact-settings-header-actions"><span class="settings-badge ${status.running ? "ok" : "warn"}">${escapeHtml(t(status.running ? "sharedAPI.runtimeRunning" : "sharedAPI.runtimeStopped"))}</span><button class="settings-action-btn subtle" type="button" data-gateway-refresh ${state.gatewayDataLoading ? "disabled" : ""}>${escapeHtml(state.gatewayDataLoading ? t("sharedAPI.loading") : t("sharedAPI.refresh"))}</button></div></header>
      ${state.gatewayAPIError ? `<div class="settings-inline-alert settings-alert shared-api-error" role="alert">${escapeHtml(t("sharedAPI.error", { message: state.gatewayAPIError }))}</div>` : ""}
      ${renderGateway()}${renderApiTunnel()}${renderProviders()}${renderAccounts()}${renderKeys()}${renderModels()}${renderUsage()}${renderRequests()}
    </div>`;
  }

  function gatewayDraftFromForm(form) {
    return {
      host: form.elements.host?.value,
      port: form.elements.port?.value,
      allowRemote: Boolean(form.elements.allowRemote?.checked),
      maxGlobalConcurrency: form.elements.maxGlobalConcurrency?.value,
      maxRequestBytes: form.elements.maxRequestBytes?.value,
    };
  }

  function keyDraftFromForm(form) {
    return {
      name: form.elements.name?.value,
      enabled: Boolean(form.elements.enabled?.checked),
      allowedModels: form.elements.allowedModels?.value,
      requestsPerMinute: form.elements.requestsPerMinute?.value,
      monthlyTokenLimit: form.elements.monthlyTokenLimit?.value,
      maxConcurrency: form.elements.maxConcurrency?.value,
      expiresAt: form.elements.expiresAt?.value,
    };
  }

  function modelDraftFromForm(form) {
    return {
      alias: form.elements.alias?.value,
      targetModel: form.elements.targetModel?.value,
      enabled: Boolean(form.elements.enabled?.checked),
    };
  }

  function runButton(button, work) {
    setButtonBusy(button, true);
    Promise.resolve().then(work).catch(showError).finally(() => setButtonBusy(button, false));
  }

  function bind() {
    ensureState();
    if (!state.gatewayDataLoaded && !state.gatewayDataLoading) load().catch(() => {});
    const root = $("settingsContentBody");
    root?.querySelector?.("[data-gateway-refresh]")?.addEventListener("click", (event) => runButton(event.currentTarget, () => load({ refreshSettings: true })));
    root?.querySelector?.("[data-gateway-config-form]")?.addEventListener("submit", (event) => { event.preventDefault(); const form = event.currentTarget; runButton(form.querySelector("[type=submit]"), () => updateGatewayConfig(gatewayDraftFromForm(form))); });
    root?.querySelector?.("[data-gateway-toggle]")?.addEventListener("click", (event) => runButton(event.currentTarget, () => setGatewayEnabled(event.currentTarget.dataset.gatewayToggle === "true")));
    root?.querySelector?.(".shared-api-requests-details")?.addEventListener("toggle", (event) => {
      requestsOpen = Boolean(event.currentTarget.open);
    });
    root?.querySelectorAll?.("[data-gateway-provider]").forEach((input) => input.addEventListener("change", (event) => runButton(event.currentTarget, () => toggleProvider(event.currentTarget.dataset.gatewayProvider, event.currentTarget.checked))));
    root?.querySelectorAll?.("[data-gateway-account-provider]").forEach((input) => input.addEventListener("change", (event) => runButton(event.currentTarget, () => toggleAccount(event.currentTarget.dataset.gatewayAccountProvider, event.currentTarget.dataset.gatewayAccountId, event.currentTarget.checked))));
    root?.querySelector?.("[data-gateway-key-add]")?.addEventListener("click", () => { keyEditorOpen = true; editingKeyID = ""; changed(); });
    root?.querySelectorAll?.("[data-gateway-key-edit]").forEach((button) => button.addEventListener("click", () => { editingKeyID = button.dataset.gatewayKeyEdit; keyEditorOpen = false; changed(); }));
    root?.querySelectorAll?.("[data-gateway-key-toggle]").forEach((button) => button.addEventListener("click", () => runButton(button, () => toggleKey(button.dataset.gatewayKeyToggle))));
    root?.querySelectorAll?.("[data-gateway-key-rotate]").forEach((button) => button.addEventListener("click", () => runButton(button, () => rotateKey(button.dataset.gatewayKeyRotate))));
    root?.querySelectorAll?.("[data-gateway-key-revoke]").forEach((button) => button.addEventListener("click", () => runButton(button, () => revokeKey(button.dataset.gatewayKeyRevoke))));
    root?.querySelectorAll?.("[data-gateway-key-delete]").forEach((button) => button.addEventListener("click", () => runButton(button, () => deleteKey(button.dataset.gatewayKeyDelete))));
    root?.querySelectorAll?.("[data-gateway-key-cancel]").forEach((button) => button.addEventListener("click", () => { keyEditorOpen = false; editingKeyID = ""; changed(); }));
    root?.querySelectorAll?.("[data-gateway-key-form]").forEach((form) => form.addEventListener("submit", (event) => { event.preventDefault(); const id = form.dataset.gatewayKeyForm; runButton(form.querySelector("[type=submit]"), () => id === "new" ? createKey(keyDraftFromForm(form)) : updateKey(id, keyDraftFromForm(form))); }));
    root?.querySelector?.("[data-gateway-model-add]")?.addEventListener("click", () => { modelEditorOpen = true; editingModelAlias = ""; changed(); });
    root?.querySelectorAll?.("[data-gateway-model-edit]").forEach((button) => button.addEventListener("click", () => { editingModelAlias = button.dataset.gatewayModelEdit; modelEditorOpen = false; changed(); }));
    root?.querySelectorAll?.("[data-gateway-model-toggle]").forEach((button) => button.addEventListener("click", () => runButton(button, () => toggleModel(button.dataset.gatewayModelToggle))));
    root?.querySelectorAll?.("[data-gateway-model-delete]").forEach((button) => button.addEventListener("click", () => runButton(button, () => deleteModel(button.dataset.gatewayModelDelete))));
    root?.querySelectorAll?.("[data-gateway-model-cancel]").forEach((button) => button.addEventListener("click", () => { modelEditorOpen = false; editingModelAlias = ""; changed(); }));
    root?.querySelectorAll?.("[data-gateway-model-form]").forEach((form) => form.addEventListener("submit", (event) => { event.preventDefault(); const alias = form.dataset.gatewayModelForm; runButton(form.querySelector("[type=submit]"), () => alias === "new" ? createModel(modelDraftFromForm(form)) : updateModel(alias, { ...modelDraftFromForm(form), expectedUpdatedAt: state.gatewayModels.find((m) => m.alias === alias)?.updatedAt })); }));
    root?.querySelector?.("[data-gateway-token-copy]")?.addEventListener("click", (event) => runButton(event.currentTarget, copyOneTimeToken));
    root?.querySelector?.("[data-gateway-token-dismiss]")?.addEventListener("click", dismissToken);
    root?.querySelector?.("[data-api-tunnel-copy-url]")?.addEventListener("click", (event) => runButton(event.currentTarget, copyApiTunnelUrl));
    root?.querySelector?.("[data-api-tunnel-install]")?.addEventListener("click", (event) => runButton(event.currentTarget, installCloudflaredForApiTunnel));
    root?.querySelector?.("[data-api-tunnel-start]")?.addEventListener("click", (event) => runButton(event.currentTarget, startApiTunnel));
    root?.querySelector?.("[data-api-tunnel-stop]")?.addEventListener("click", (event) => runButton(event.currentTarget, stopApiTunnel));
  }

  function consumeOneTimeToken() {
    const token = oneTimeToken;
    oneTimeToken = "";
    oneTimeTokenContext = "";
    return token;
  }

  ensureState();
  return {
    bind,
    consumeOneTimeToken,
    copyApiTunnelUrl,
    copyOneTimeToken,
    createKey,
    dismissToken,
    installCloudflaredForApiTunnel,
    load,
    oneTimeTokenValue: () => oneTimeToken,
    render,
    revokeKey,
    deleteKey,
    rotateKey,
    createModel,
    updateModel,
    deleteModel,
    toggleModel,
    setGatewayEnabled,
    startApiTunnel,
    stopApiTunnel,
    toggleAccount,
    toggleKey,
    toggleProvider,
    updateGatewayConfig,
    updateKey,
  };
}
