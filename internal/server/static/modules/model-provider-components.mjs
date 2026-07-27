import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs?v=provider-draft-session-1";

const ct = (key, params) => t(`modelProvider.console.${key}`, params);

export const providerTypeTemplates = [
  {
    key: "codex",
    templateKey: "codexOAuth",
    type: "codex",
    name: "codex",
    model: "gpt-5.5",
    apiKeyOptional: true,
    category: "official",
  },
  {
    key: "openai",
    templateKey: "openaiOfficial",
    type: "openai",
    name: "openai",
    model: "gpt-4.1-mini",
    category: "official",
  },
  {
    key: "anthropic",
    templateKey: "anthropicOfficial",
    type: "anthropic",
    name: "anthropic",
    model: "claude-sonnet-4-5",
    maxTokens: 4096,
    category: "official",
  },
  {
    key: "openai-compatible",
    templateKey: "openaiCompatible",
    type: "openai-compatible",
    name: "custom-openai",
    baseUrl: "https://api.example.com/v1",
    model: "your-model",
    category: "custom",
  },
  {
    key: "anthropic-compatible",
    templateKey: "anthropicCompatible",
    type: "anthropic",
    name: "custom-anthropic",
    baseUrl: "https://api.example.com",
    model: "your-model",
    maxTokens: 4096,
    category: "custom",
  },
  {
    key: "ollama",
    templateKey: "ollama",
    type: "openai-compatible",
    name: "ollama",
    baseUrl: "http://127.0.0.1:11434/v1",
    model: "llama3.2",
    apiKeyOptional: true,
    category: "official",
  },
];

const builtinProviderNames = new Set([
  "codex",
  "openai",
  "anthropic",
  "openai-compatible",
  "cliproxyapi",
  "ollama",
  "gemini",
  "grok",
  "kimi",
]);
const categoryMeta = {
  all: { labelKey: "categories.all", titleKey: "categories.all" },
  official: { labelKey: "categories.officialPlatforms", titleKey: "officialPlatforms" },
  custom: { labelKey: "categories.custom", titleKey: "customProviders" },
};

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function stringValue(value) {
  return String(value ?? "").trim();
}

function contextTokenLimitValue(value) {
  const numeric = Number(value || 0);
  return Number.isFinite(numeric) && numeric > 0 ? Math.floor(numeric) : 0;
}

export function providerSupportsImageGeneration(provider = {}) {
  const capabilities = provider?.capabilities && typeof provider.capabilities === "object" && !Array.isArray(provider.capabilities)
    ? provider.capabilities
    : {};
  if (Object.hasOwn(capabilities, "imageGeneration")) return capabilities.imageGeneration === true;
  const type = stringValue(provider?.type || provider?.name).toLowerCase();
  const name = stringValue(provider?.name).toLowerCase();
  return type === "openai" || type === "codex" || name === "openai" || name === "codex";
}

export function normalizeProviderModelConfigs(source = {}, options = {}) {
  const provider = Array.isArray(source) ? { models: source } : (source && typeof source === "object" ? source : {});
  const hiddenModels = options.hiddenModels && typeof options.hiddenModels === "object" ? options.hiddenModels : {};
  const providerName = stringValue(options.providerName || provider.name);
  const capabilities = provider.modelCapabilities && typeof provider.modelCapabilities === "object" && !Array.isArray(provider.modelCapabilities)
    ? provider.modelCapabilities
    : {};
  const rows = [];
  const seen = new Set();
  const add = (raw, manual = false) => {
    const item = raw && typeof raw === "object" && !Array.isArray(raw) ? raw : { name: raw };
    const name = stringValue(item.name || item.model);
    if (!name || seen.has(name)) return;
    seen.add(name);
    const preferenceKey = providerName ? `${providerName}:${name}` : name;
    rows.push({
      name,
      contextTokenLimit: contextTokenLimitValue(firstDefined(item.contextTokenLimit, capabilities[name]?.contextTokenLimit)),
      imageGeneration: item.imageGeneration === true,
      hidden: item.hidden === undefined ? Boolean(hiddenModels[preferenceKey]) : Boolean(item.hidden),
      manual: item.manual === undefined ? Boolean(manual) : Boolean(item.manual),
    });
  };
  asArray(provider.modelConfigs).forEach((item) => add(item, Boolean(item?.manual)));
  asArray(provider.models).forEach((item) => add(item, Boolean(item?.manual)));
  const defaultModel = stringValue(provider.defaultModel || provider.model);
  if (defaultModel && !seen.has(defaultModel)) add({ name: defaultModel }, true);
  return rows;
}

export function mergeProviderModelDiscovery(currentConfigs = [], response = {}, defaultModel = "") {
  const existing = new Map(normalizeProviderModelConfigs({ modelConfigs: currentConfigs }).map((item) => [item.name, item]));
  const discovered = normalizeProviderModelConfigs({
    models: response?.models,
    modelCapabilities: response?.modelCapabilities,
  });
  const merged = discovered.map((item) => {
    const previous = existing.get(item.name);
    return previous
      ? { ...item, contextTokenLimit: previous.contextTokenLimit, imageGeneration: previous.imageGeneration, hidden: previous.hidden, manual: false }
      : { ...item, imageGeneration: false, manual: false };
  });
  const included = new Set(merged.map((item) => item.name));
  existing.forEach((item) => {
    if (item.manual && !included.has(item.name)) {
      merged.push({ ...item, manual: true });
      included.add(item.name);
    }
  });
  const selected = stringValue(defaultModel);
  if (selected && !included.has(selected)) merged.push({ name: selected, contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: true });
  return merged;
}

function providerDefaultModelForDraft(draft = {}, configs = normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs })) {
  const selected = stringValue(draft.model);
  return configs.some((item) => item.name === selected) ? selected : (configs[0]?.name || "");
}

export function providerModelDraftUsable(draft = {}) {
  const configs = normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs });
  return Boolean(draft.modelsReady && !draft.modelsStale && providerDefaultModelForDraft(draft, configs));
}

// Visibility only controls whether a model is offered in the composer's model
// picker. It is presentation, not configuration: the provider keeps working and
// keeps its configured default even when that model is hidden, so defaultModel
// is always returned untouched. allowEmpty lets every model be hidden.
export function setProviderModelHidden(modelConfigs = [], modelName = "", hidden = false, defaultModel = "", { allowEmpty = false } = {}) {
  const configs = normalizeProviderModelConfigs({ modelConfigs });
  const name = stringValue(modelName);
  const index = configs.findIndex((item) => item.name === name);
  const currentDefault = stringValue(defaultModel);
  if (index < 0) return { modelConfigs: configs, defaultModel: currentDefault, changed: false };
  if (!hidden) {
    configs[index] = { ...configs[index], hidden: false };
    return { modelConfigs: configs, defaultModel: currentDefault, changed: true };
  }
  const visibleAlternatives = configs.filter((item) => item.name !== name && !item.hidden);
  if (!visibleAlternatives.length && !allowEmpty) return { modelConfigs: configs, defaultModel: currentDefault, changed: false };
  configs[index] = { ...configs[index], hidden: true };
  return { modelConfigs: configs, defaultModel: currentDefault, changed: true };
}

// Show or hide every model at once. Like setProviderModelHidden this only moves
// entries in and out of the picker and never rewrites defaultModel. Without
// allowEmpty one entry stays visible (the configured default when it is one of
// them) so the provider's picker section is not left empty.
export function setProviderModelHiddenAll(modelConfigs = [], hideAll = false, defaultModel = "", { allowEmpty = false } = {}) {
  const configs = normalizeProviderModelConfigs({ modelConfigs });
  if (!configs.length) return { modelConfigs: configs, defaultModel: stringValue(defaultModel), changed: false };
  if (!hideAll) {
    let changed = false;
    const next = configs.map((item) => {
      if (!item.hidden) return item;
      changed = true;
      return { ...item, hidden: false };
    });
    return { modelConfigs: next, defaultModel: stringValue(defaultModel), changed };
  }
  if (allowEmpty) {
    let changedAll = false;
    const hiddenAll = configs.map((item) => {
      if (item.hidden) return item;
      changedAll = true;
      return { ...item, hidden: true };
    });
    return { modelConfigs: hiddenAll, defaultModel: stringValue(defaultModel), changed: changedAll };
  }
  const def = stringValue(defaultModel);
  const keeper = configs.some((item) => item.name === def) ? def : configs[0].name;
  let changed = false;
  const next = configs.map((item) => {
    const shouldHide = item.name !== keeper;
    if (Boolean(item.hidden) !== shouldHide) changed = true;
    return { ...item, hidden: shouldHide };
  });
  return { modelConfigs: next, defaultModel: stringValue(defaultModel), changed };
}

export function providerVisibilityPreferencesForDraft(preferences = {}, oldProviderName = "", newProviderName = "", modelConfigs = []) {
  const hiddenModels = { ...(preferences?.hiddenModels || {}) };
  const oldPrefix = `${stringValue(oldProviderName)}:`;
  const newPrefix = `${stringValue(newProviderName)}:`;
  if (oldPrefix !== ":") {
    Object.keys(hiddenModels).forEach((key) => {
      if (key.startsWith(oldPrefix)) delete hiddenModels[key];
    });
  }
  if (newPrefix !== ":") {
    Object.keys(hiddenModels).forEach((key) => {
      if (key.startsWith(newPrefix)) delete hiddenModels[key];
    });
    normalizeProviderModelConfigs({ modelConfigs }).forEach((item) => {
      if (item.hidden) hiddenModels[`${newPrefix}${item.name}`] = true;
    });
  }
  return { ...preferences, hiddenModels };
}

export function removeProviderVisibilityPreferences(preferences = {}, providerName = "") {
  const hiddenModels = { ...(preferences?.hiddenModels || {}) };
  const prefix = `${stringValue(providerName)}:`;
  if (prefix !== ":") Object.keys(hiddenModels).forEach((key) => { if (key.startsWith(prefix)) delete hiddenModels[key]; });
  return { ...preferences, hiddenModels };
}

function firstDefined(...values) {
  return values.find((value) => value !== undefined && value !== null);
}

function providerKey(provider) {
  return stringValue(provider?.name || provider?.type).toLowerCase();
}

function providerIdentityKeys(provider = {}) {
  return [provider.name, provider.type, provider.profile]
    .map((value) => stringValue(value).toLowerCase())
    .filter(Boolean);
}

const apiKeySources = new Set(["stored", "environment", "runtime", "optional", "none", "stored_unavailable"]);

function safeApiKeyMetadata(provider = {}) {
  const source = stringValue(provider.apiKeySource).toLowerCase();
  const lastFive = stringValue(provider.apiKeyLastFive);
  return {
    apiKeyConfigured: Boolean(provider.apiKeyConfigured),
    apiKeyPersisted: Boolean(provider.apiKeyPersisted),
    apiKeyLastFive: lastFive.slice(-5),
    apiKeySource: apiKeySources.has(source) ? source : "none",
  };
}

function safeProxyURL(value) {
  const raw = stringValue(value);
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

function safeTransportMetadata(provider = {}) {
  const proxySource = stringValue(provider.proxyAuthSource).toLowerCase();
  const headerSource = stringValue(provider.requestHeadersSource).toLowerCase();
  return {
    proxyUrl: safeProxyURL(provider.proxyUrl),
    proxyAuthConfigured: Boolean(provider.proxyAuthConfigured),
    proxyAuthPersisted: Boolean(provider.proxyAuthPersisted),
    proxyAuthSource: apiKeySources.has(proxySource) ? proxySource : "none",
    userAgent: stringValue(provider.userAgent),
    requestHeaders: asArray(provider.requestHeaders).map((header) => ({
      name: stringValue(header?.name),
      configured: Boolean(header?.configured),
    })).filter((header) => header.name),
    requestHeadersPersisted: Boolean(provider.requestHeadersPersisted),
    requestHeadersSource: apiKeySources.has(headerSource) ? headerSource : "none",
    insecureSkipTLSVerify: Boolean(provider.insecureSkipTLSVerify),
  };
}

function settingsRecord(provider = {}) {
  const { apiKey: _apiKey, proxyUsername: _proxyUsername, proxyPassword: _proxyPassword, ...safeProvider } = provider || {};
  return {
    ...safeProvider,
    ...safeApiKeyMetadata(provider),
    ...safeTransportMetadata(provider),
    name: stringValue(provider.name || provider.type),
    type: stringValue(provider.type || provider.name || "openai-compatible"),
    defaultModel: stringValue(provider.defaultModel || provider.model),
    models: asArray(provider.models),
  };
}

export function isBuiltinProvider(provider = {}) {
  const origin = stringValue(provider.origin).toLowerCase();
  if (origin === "custom") return false;
  if (origin === "builtin") return true;
  // Unknown lifecycle metadata must never unlock deletion. The named set preserves
  // built-in handling for older server responses without an origin field.
  return !origin || providerIdentityKeys(provider).some((key) => builtinProviderNames.has(key));
}

export function isProviderDeletable(provider = {}) {
  return stringValue(provider.origin).toLowerCase() === "custom";
}

export function isAnthropicAccountProvider(provider = {}) {
  const normalized = normalizeConsoleProvider(provider);
  return normalized.name === "anthropic" && normalized.type === "anthropic" && isBuiltinProvider(normalized);
}

// The three subscription providers each keep a dedicated official card and a
// dedicated management page. loginKind drives the page layout: Gemini uses a
// browser OAuth callback, while Grok and Kimi use a device-code flow.
export const subscriptionProviderSpecs = Object.freeze({
  gemini: Object.freeze({ provider: "gemini", type: "gemini", view: "gemini", loginKind: "browser", className: "gemini-account-console" }),
  grok: Object.freeze({ provider: "grok", type: "grok", view: "grok", loginKind: "device", className: "grok-account-console" }),
  kimi: Object.freeze({ provider: "kimi", type: "kimi", view: "kimi", loginKind: "device", className: "kimi-account-console" }),
  kiro: Object.freeze({ provider: "kiro", type: "kiro", view: "kiro", loginKind: "token", className: "kiro-account-console" }),
});

export const subscriptionProviderKinds = Object.freeze(Object.keys(subscriptionProviderSpecs));

// subscriptionProviderKind identifies a native subscription provider strictly by
// its exact type. gemini-interactions, which is the API-key provider, must never
// be treated as an official Antigravity/Grok/Kimi account entry.
export function subscriptionProviderKind(provider = {}) {
  // Keyed on type alone, deliberately ignoring the configured name and origin.
  // These types cannot be pointed anywhere but their own pinned production
  // endpoint — the server rejects every other Base URL (see
  // validateGemini/Grok/KimiProductionBaseURL) and the credential store is keyed
  // by type, not by config name. So an instance saved under a different name
  // such as "gemini-oauth" is still the native subscription provider and must
  // get the OAuth login and account pages.
  const type = stringValue(provider.type).toLowerCase();
  return Object.hasOwn(subscriptionProviderSpecs, type) ? type : "";
}

export function isSubscriptionAccountProvider(provider = {}) {
  return Boolean(subscriptionProviderKind(provider));
}

export function subscriptionProviderSpec(kind) {
  return subscriptionProviderSpecs[String(kind || "").toLowerCase()] || null;
}

export function providerCategory(provider = {}) {
  const type = stringValue(provider.type).toLowerCase();
  const name = providerKey(provider);
  // Native subscription providers belong to the official section whatever name
  // they were saved under, for the same endpoint-pinning reason.
  if (subscriptionProviderKind(provider)) return "official";
  if (stringValue(provider.origin).toLowerCase() === "custom") return "custom";
  if (type === "openai-compatible" || type === "anthropic-compatible" || provider.profile === "cliproxyapi") return "custom";
  if (isBuiltinProvider(provider) || ["codex", "openai", "anthropic", "ollama"].includes(type) || name === "ollama") return "official";
  return "custom";
}

export function normalizeConsoleProvider(provider = {}) {
  const { apiKey: _apiKey, proxyUsername: _proxyUsername, proxyPassword: _proxyPassword, ...safeProvider } = provider || {};
  const type = stringValue(provider.type || provider.name || "openai-compatible");
  const name = stringValue(provider.name || type || "provider");
  const defaultModel = stringValue(provider.defaultModel || provider.model);
  const modelConfigs = normalizeProviderModelConfigs(provider);
  const models = modelConfigs.map((item) => item.name);
  const apiKeyMetadata = safeApiKeyMetadata(provider);
  const transportMetadata = safeTransportMetadata(provider);
  const configured = Boolean(provider.configured);
  const enabled = provider.enabled === undefined || provider.enabled === null
    ? configured
    : Boolean(provider.enabled);
  const suppliedOrigin = stringValue(provider.origin).toLowerCase();
  const origin = suppliedOrigin || (providerIdentityKeys({ ...provider, name, type }).some((key) => builtinProviderNames.has(key)) ? "builtin" : "unknown");
  return {
    ...safeProvider,
    ...apiKeyMetadata,
    ...transportMetadata,
    name,
    type,
    profile: stringValue(provider.profile),
    baseUrl: stringValue(provider.baseUrl),
    defaultModel,
    model: defaultModel,
    models: models.length ? models : (defaultModel ? [defaultModel] : []),
    modelConfigs: modelConfigs.length ? modelConfigs : (defaultModel ? [{ name: defaultModel, contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: true }] : []),
    modelsReady: provider.modelsReady === undefined ? Boolean(modelConfigs.length || defaultModel) : Boolean(provider.modelsReady),
    modelsStale: Boolean(provider.modelsStale),
    modelCapabilities: provider.modelCapabilities && typeof provider.modelCapabilities === "object" && !Array.isArray(provider.modelCapabilities) ? provider.modelCapabilities : {},
    modelsSource: stringValue(provider.modelsSource),
    discovered: Boolean(provider.discovered),
    available: Boolean(provider.available),
    runtimeAvailable: provider.runtimeAvailable === undefined || provider.runtimeAvailable === null ? undefined : Boolean(provider.runtimeAvailable),
    registered: provider.registered === undefined || provider.registered === null ? undefined : Boolean(provider.registered),
    maxTokens: Number(provider.maxTokens) > 0 ? Number(provider.maxTokens) : 0,
    configured,
    enabled,
    origin,
    apiKeyOptional: Boolean(provider.apiKeyOptional),
    error: stringValue(provider.error),
    capabilities: provider.capabilities && typeof provider.capabilities === "object" ? provider.capabilities : {},
  };
}

// Settings is authoritative for lifecycle fields while the catalog is authoritative
// for the currently discovered model list. Keeping the settings-first seed retains
// disabled providers that the model catalog deliberately excludes.
export function modelProvidersForUIUnion(settingsProviders, catalogProviders) {
  const records = new Map();
  for (const setting of asArray(settingsProviders)) {
    const normalized = settingsRecord(setting);
    if (normalized.name) records.set(normalized.name, normalized);
  }
  for (const catalog of asArray(catalogProviders)) {
    const catalogName = stringValue(catalog?.name || catalog?.type);
    if (!catalogName) continue;
    const { apiKey: _apiKey, ...safeCatalog } = catalog || {};
    const setting = records.get(catalogName) || {};
    records.set(catalogName, {
      ...setting,
      ...safeCatalog,
      name: catalogName,
      type: firstDefined(catalog.type, setting.type, catalogName),
      profile: firstDefined(setting.profile, catalog.profile, ""),
      baseUrl: firstDefined(setting.baseUrl, catalog.baseUrl, ""),
      defaultModel: firstDefined(catalog.defaultModel, setting.defaultModel, setting.model, catalog.model, ""),
      model: firstDefined(setting.model, catalog.defaultModel, catalog.model, ""),
      models: asArray(catalog.models).length ? catalog.models : (asArray(setting.models).length ? setting.models : []),
      modelsSource: firstDefined(catalog.modelsSource, setting.modelsSource, ""),
      discovered: firstDefined(catalog.discovered, setting.discovered, false),
      available: firstDefined(catalog.available, setting.available, false),
      runtimeAvailable: firstDefined(catalog.runtimeAvailable, setting.runtimeAvailable),
      registered: firstDefined(catalog.registered, setting.registered),
      maxTokens: firstDefined(setting.maxTokens, catalog.maxTokens, 0),
      configured: firstDefined(catalog.configured, setting.configured, false),
      enabled: firstDefined(setting.enabled, catalog.enabled, setting.configured, catalog.configured, false),
      origin: firstDefined(setting.origin, catalog.origin, ""),
      apiKeyOptional: firstDefined(setting.apiKeyOptional, catalog.apiKeyOptional, false),
      apiKeyConfigured: firstDefined(catalog.apiKeyConfigured, setting.apiKeyConfigured, false),
      apiKeyPersisted: firstDefined(catalog.apiKeyPersisted, setting.apiKeyPersisted, false),
      apiKeyLastFive: firstDefined(catalog.apiKeyLastFive, setting.apiKeyLastFive, ""),
      apiKeySource: firstDefined(catalog.apiKeySource, setting.apiKeySource, "none"),
    });
  }
  return [...records.values()].map(normalizeConsoleProvider);
}

export function providerConsoleStats(providers) {
  const list = asArray(providers);
  return {
    total: list.length,
    enabled: list.filter((provider) => provider.enabled).length,
    models: list.reduce((total, provider) => total + provider.models.length, 0),
    attention: list.filter((provider) => provider.error || (provider.enabled && !provider.configured)).length,
  };
}

export function filterConsoleProviders(providers, { search = "", category = "all" } = {}) {
  const needle = stringValue(search).toLocaleLowerCase();
  return asArray(providers).filter((provider) => {
    if (category !== "all" && providerCategory(provider) !== category) return false;
    if (!needle) return true;
    return [provider.name, provider.type, provider.baseUrl, provider.defaultModel, provider.origin]
      .some((value) => stringValue(value).toLocaleLowerCase().includes(needle));
  });
}

export function providerDisplayName(provider = {}) {
  if (provider.type === "codex" || provider.name === "codex") return "Codex OAuth";
  if (provider.name === "openai" && provider.type === "openai") return "OpenAI";
  if (provider.name === "anthropic" && provider.type === "anthropic") return "Anthropic";
  if (provider.name === "ollama") return "Ollama";
  if (stringValue(provider.type).toLowerCase() === "gemini-interactions") return "Gemini Interactions";
  switch (subscriptionProviderKind(provider)) {
    // The gemini provider authenticates through Google's Antigravity surface
    // (ideType ANTIGRAVITY), so the console labels it by the platform it talks
    // to rather than by the model family. The provider id stays "gemini".
    case "gemini": return "Antigravity";
    case "grok": return "Grok";
    case "kimi": return "Kimi";
  }
  return provider.name || provider.type || ct("labels.provider");
}

export function providerStatus(provider = {}) {
  if (provider.error) return { tone: "attention", label: ct("statusLabels.needsAttention") };
  if (!provider.enabled) return { tone: "muted", label: ct("statusLabels.disabled") };
  if (provider.configured) return { tone: "ready", label: ct("statusLabels.ready") };
  return { tone: "attention", label: ct("statusLabels.unconfigured") };
}

export function compactBaseUrl(value) {
  const raw = stringValue(value);
  if (!raw) return ct("labels.defaultEndpoint");
  try {
    const parsed = new URL(raw);
    return `${parsed.host}${parsed.pathname === "/" ? "" : parsed.pathname}`;
  } catch {
    return raw.length > 72 ? `${raw.slice(0, 69)}…` : raw;
  }
}

export function createProviderDraft(typeKey, provider = null) {
  const template = providerTypeTemplates.find((item) => item.key === typeKey)
    || providerTypeTemplates.find((item) => item.type === typeKey)
    || providerTypeTemplates[3];
  const source = provider ? normalizeConsoleProvider(provider) : null;
  const hasProviderName = Boolean(provider && Object.hasOwn(provider, "name"));
  const hasProviderBaseUrl = Boolean(provider && Object.hasOwn(provider, "baseUrl"));
  const localApiKey = provider?.apiKeyDraft === true ? stringValue(provider.apiKey) : "";
  const localProxyURL = provider?.proxyUrlDraft === true ? String(provider.proxyUrl || "") : safeProxyURL(source?.proxyUrl || provider?.proxyUrl);
  const localHeaders = asArray(provider?.requestHeaders).map((header) => ({
    name: stringValue(header?.name),
    value: provider?.requestHeadersDraft === true ? String(header?.value || "") : "",
    keepExisting: provider?.requestHeadersDraft === true ? Boolean(header?.keepExisting) : Boolean(header?.configured),
    configured: Boolean(header?.configured),
  })).filter((header) => header.name || header.value);
  return {
    name: hasProviderName ? stringValue(provider.name) : (source?.name || template.name),
    type: source?.type || template.type,
    profile: source?.profile || "",
    baseUrl: hasProviderBaseUrl ? stringValue(provider.baseUrl) : (source?.baseUrl || template.baseUrl || ""),
    // Saved provider responses never contribute a secret; only an explicitly marked
    // in-progress draft may retain its local key between redraws.
    apiKey: localApiKey,
    apiKeyDraft: provider?.apiKeyDraft === true,
    apiKeyConfigured: source?.apiKeyConfigured ?? false,
    apiKeyPersisted: source?.apiKeyPersisted ?? false,
    apiKeyLastFive: source?.apiKeyLastFive || "",
    apiKeySource: source?.apiKeySource || "none",
    clearApiKey: Boolean(provider?.clearApiKey),
    proxyUrl: localProxyURL,
    proxyUrlDraft: provider?.proxyUrlDraft === true,
    proxyAuthConfigured: source?.proxyAuthConfigured ?? false,
    proxyAuthPersisted: source?.proxyAuthPersisted ?? false,
    proxyAuthSource: source?.proxyAuthSource || "none",
    clearProxyAuth: Boolean(provider?.clearProxyAuth),
    userAgent: provider?.userAgentDraft === true ? String(provider.userAgent || "") : (source?.userAgent || ""),
    userAgentDraft: provider?.userAgentDraft === true,
    requestHeaders: localHeaders,
    requestHeadersDraft: provider?.requestHeadersDraft === true,
    requestHeadersPersisted: source?.requestHeadersPersisted ?? false,
    requestHeadersSource: source?.requestHeadersSource || "none",
    insecureSkipTLSVerify: provider?.insecureSkipTLSVerify ?? source?.insecureSkipTLSVerify ?? false,
    capabilities: source?.capabilities || { imageGeneration: providerSupportsImageGeneration(template) },
    model: source?.defaultModel || template.model || "",
    models: source?.models || [],
    modelConfigs: provider?.modelsReady === false && !asArray(provider?.modelConfigs).length && !asArray(provider?.models).length ? [] : (source?.modelConfigs || []),
    modelsReady: provider?.modelsReady === undefined ? Boolean(provider && (source?.modelConfigs?.length || source?.defaultModel)) : Boolean(provider.modelsReady),
    modelsStale: Boolean(provider?.modelsStale),
    maxTokens: source?.maxTokens || template.maxTokens || 0,
    apiKeyOptional: source?.apiKeyOptional ?? Boolean(template.apiKeyOptional),
    enabled: source?.enabled ?? false,
    origin: source?.origin || (template.category === "official" ? "builtin" : "custom"),
  };
}

export function providerConfigPayload(draft = {}) {
  const maxTokens = Number(draft.maxTokens || 0);
  return {
    name: stringValue(draft.name),
    type: stringValue(draft.type || "openai-compatible"),
    profile: stringValue(draft.profile),
    baseUrl: stringValue(draft.baseUrl),
    // Empty keeps the existing key; clearApiKey is the only explicit removal path.
    apiKey: draft.clearApiKey ? "" : stringValue(draft.apiKey),
    ...(draft.clearApiKey ? { clearApiKey: true } : {}),
    ...(draft.createOnly ? { createOnly: true } : {}),
    ...(stringValue(draft.originalName) ? { originalName: stringValue(draft.originalName) } : {}),
    model: stringValue(draft.model),
    models: normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs }).map((item) => ({
      name: item.name,
      contextTokenLimit: item.contextTokenLimit,
      imageGeneration: item.imageGeneration,
    })),
    maxTokens: Number.isFinite(maxTokens) && maxTokens > 0 ? Math.floor(maxTokens) : 0,
    apiKeyOptional: Boolean(draft.apiKeyOptional),
    proxyUrl: String(draft.proxyUrl || "").trim(),
    ...(draft.clearProxyAuth ? { clearProxyAuth: true } : {}),
    userAgent: String(draft.userAgent || "").trim(),
    requestHeaders: asArray(draft.requestHeaders).map((header) => ({
      name: stringValue(header?.name),
      value: String(header?.value || ""),
      keepExisting: Boolean(header?.keepExisting),
    })).filter((header) => header.name || header.value),
    insecureSkipTLSVerify: Boolean(draft.insecureSkipTLSVerify),
  };
}

export function providerTestPayload(draft = {}, provider = {}) {
  // Only the draft fields accepted by preflight are sent. In particular, lifecycle,
  // catalog, origin, and error fields are never client-controlled. An explicit empty
  // API key preserves the existing-key semantics for an already saved provider.
  return providerConfigPayload({ ...provider, ...draft });
}

export function providerMessageTestPayload(draft = {}, provider = {}, prompt = "") {
  return {
    ...providerTestPayload(draft, provider),
    prompt: stringValue(prompt),
  };
}

export function providerConsoleRequest(action, provider, values = {}) {
  const name = stringValue(values.pathName || values.name || provider?.name);
  const path = `/api/providers/${encodeURIComponent(name)}`;
  switch (action) {
    case "toggle": {
      const model = stringValue(values.model || provider?.defaultModel || provider?.model);
      const body = { enabled: Boolean(values.enabled) };
      if (model) body.model = model;
      return {
        path,
        options: { method: "PATCH", body: JSON.stringify(body) },
      };
    }
    case "test":
      return {
        path: "/api/providers/test",
        options: { method: "POST", body: JSON.stringify(providerTestPayload(values, provider)) },
      };
    case "message-test":
      return {
        path: "/api/providers/test-message",
        options: { method: "POST", body: JSON.stringify(providerMessageTestPayload(values, provider, values.prompt)) },
      };
    case "delete":
      return { path, options: { method: "DELETE" } };
    case "config":
      return {
        path: `${path}/config`,
        options: { method: "PUT", body: JSON.stringify(providerConfigPayload(values)) },
      };
    default:
      throw new Error(`unsupported provider console action: ${action}`);
  }
}

function normalizedDiscoveredModels(models) {
  return [...new Set(asArray(models).map(stringValue).filter(Boolean))];
}

function renderModelChoiceSelect(models, selectedModel, id) {
  const choices = normalizedDiscoveredModels(models);
  if (!choices.length) return "";
  const selected = stringValue(selectedModel);
  return `<div class="mp-discovered-models" data-mp-discovered-models>
    <label class="settings-form-field" for="${escapeAttr(id)}"><span>${escapeHtml(ct("fields.model"))} · ${escapeHtml(ct("fields.modelCount", { count: choices.length }))}</span><select id="${escapeAttr(id)}" data-mp-model-choice aria-label="${escapeAttr(ct("fields.defaultModel"))}"><option value="">${escapeHtml(ct("fields.defaultModel"))}</option>${choices.map((model) => `<option value="${escapeAttr(model)}" ${model === selected ? "selected" : ""}>${escapeHtml(model)}</option>`).join("")}</select></label>
  </div>`;
}

function renderProviderMessageTestDialog(state = {}, draft = {}) {
  if (!state.testOpen) return "";
  const test = state.test && typeof state.test === "object" ? state.test : {};
  const busy = Boolean(state.busy?.[`message-test:${draft.name}`]);
  const result = test.result && typeof test.result === "object" ? test.result : null;
  const resultHTML = result
    ? `<div class="mp-provider-test-result settings-alert ${escapeAttr(result.tone || (result.success ? "success" : "attention"))}" role="status" aria-live="polite"><strong>${escapeHtml(result.success ? ct("test.successTitle") : ct("test.failureTitle"))}</strong>${result.output ? `<pre>${escapeHtml(result.output)}</pre>` : `<span>${escapeHtml(result.message || ct("messages.requestFailed"))}</span>`}</div>`
    : "";
  return `<div class="mp-provider-test-backdrop" data-mp-backdrop="test" role="presentation"><section class="mp-provider-test-dialog settings-card" role="dialog" aria-modal="true" tabindex="-1" aria-labelledby="mp-provider-test-title" aria-describedby="mp-provider-test-description" aria-busy="${busy ? "true" : "false"}">
    <header class="mp-provider-test-head settings-card-header"><div><p class="mp-provider-kicker">${escapeHtml(ct("test.kicker"))}</p><h2 id="mp-provider-test-title" class="settings-card-title">${escapeHtml(ct("test.title"))}</h2><p id="mp-provider-test-description" class="settings-card-description" data-settings-help-copy>${escapeHtml(ct("test.description", { provider: draft.name || ct("labels.provider"), model: draft.model || ct("labels.notSet") }))}</p></div><button class="mp-icon-button" type="button" data-mp-close-test aria-label="${escapeAttr(ct("actions.closeModal"))}">×</button></header>
    <form class="mp-provider-test-form settings-card-content" data-mp-provider-test-form>
      <label class="settings-form-field"><span>${escapeHtml(ct("test.promptLabel"))}</span><textarea name="prompt" data-mp-test-prompt rows="4" maxlength="8192" required placeholder="${escapeAttr(ct("test.promptPlaceholder"))}">${escapeHtml(test.prompt || ct("test.defaultPrompt"))}</textarea><small data-settings-help-copy>${escapeHtml(ct("test.promptHelp"))}</small></label>
      ${resultHTML}
      <footer class="mp-provider-test-foot settings-card-footer settings-inline-actions"><button class="mp-action" type="button" data-mp-close-test>${escapeHtml(ct("actions.cancel"))}</button><button class="mp-action primary" type="submit" data-mp-send-test ${busy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(busy ? ct("test.sending") : ct("test.send"))}</button></footer>
    </form>
  </section></div>`;
}

function renderModelChips(models) {
  const choices = normalizedDiscoveredModels(models);
  if (!choices.length) return "";
  const visible = choices.slice(0, 12);
  const remainder = choices.length - visible.length;
  return `<div class="mp-provider-models" aria-label="${escapeAttr(ct("fields.modelCount", { count: choices.length }))}">${visible.map((model) => `<span class="mp-model-chip" title="${escapeAttr(model)}">${escapeHtml(model)}</span>`).join("")}${remainder > 0 ? `<span class="mp-model-chip" title="${escapeAttr(ct("fields.modelCount", { count: choices.length }))}">+${remainder}</span>` : ""}</div>`;
}

function renderProviderRequestHeaderRows(headers = []) {
  const rows = asArray(headers);
  if (!rows.length) return `<div class="mp-provider-header-empty" data-mp-request-header-empty>${escapeHtml(ct("createPage.headersEmpty"))}</div>`;
  return rows.map((header, index) => {
    const configured = Boolean(header?.configured || header?.keepExisting);
    const keepExisting = Boolean(header?.keepExisting);
    return `<div class="mp-provider-header-row" data-mp-request-header-row data-keep-existing="${keepExisting ? "true" : "false"}" data-configured="${configured ? "true" : "false"}" data-original-name="${escapeAttr(header?.name || "")}">
      <label><span>${escapeHtml(ct("fields.requestHeaderName"))}</span><input type="text" data-mp-request-header-name value="${escapeAttr(header?.name || "")}" maxlength="128" autocomplete="off" spellcheck="false" placeholder="X-Custom-Header"></label>
      <label><span>${escapeHtml(ct("fields.requestHeaderValue"))}</span><input type="password" data-mp-request-header-value value="${escapeAttr(header?.value || "")}" maxlength="8192" autocomplete="new-password" spellcheck="false" placeholder="${escapeAttr(configured ? ct("fields.requestHeaderBlankKeepsCurrent") : ct("fields.requestHeaderValuePlaceholder"))}"></label>
      <button class="mp-provider-header-remove" type="button" data-mp-remove-request-header="${index}" aria-label="${escapeAttr(ct("actions.removeHeader"))}">×</button>
    </div>`;
  }).join("");
}

// allowEmpty lets the last visible model be hidden too. Pages whose visibility is
// a display preference rather than provider configuration pass it; otherwise the
// final eye stays disabled so a provider is never left with nothing to offer.
export function renderProviderModelEditor(draft = {}, modelBusy = false, sensitiveAccessAllowed = true, { allowEmpty = false } = {}) {
  const configs = normalizeProviderModelConfigs({ modelConfigs: draft.modelConfigs });
  const imageGenerationSupported = providerSupportsImageGeneration(draft);
  const visibleCount = configs.filter((item) => !item.hidden).length;
  const eyeOffIcon = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m3 3 18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.2A10.8 10.8 0 0 1 12 4c5.2 0 9.2 4 10.5 8a11.7 11.7 0 0 1-3.1 4.8M6.2 6.2A11.8 11.8 0 0 0 1.5 12c1.3 4 5.3 8 10.5 8 1.3 0 2.5-.2 3.6-.7"/></svg>`;
  const eyeOnIcon = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 12s3.8-7 10-7 10 7 10 7-3.8 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/></svg>`;
  const allVisible = configs.length > 0 && configs.every((item) => !item.hidden);
  const rows = configs.map((item) => {
    const hideDisabled = !allowEmpty && !item.hidden && visibleCount <= 1;
    const visibilityIcon = item.hidden ? eyeOffIcon : eyeOnIcon;
    return `<div class="mp-provider-model-config-row${item.hidden ? " is-hidden" : ""}" data-mp-model-config="${escapeAttr(item.name)}">
      <div class="mp-provider-model-name"><strong title="${escapeAttr(item.name)}">${escapeHtml(item.name)}</strong>${item.manual ? `<span class="settings-badge">${escapeHtml(ct("statusLabels.manual"))}</span>` : ""}</div>
      <label class="mp-provider-model-limit"><span>${escapeHtml(ct("fields.contextTokenLimit"))}</span><input type="number" min="0" max="10000000" step="1" inputmode="numeric" value="${escapeAttr(item.contextTokenLimit || 272000)}" data-mp-model-token="${escapeAttr(item.name)}" aria-label="${escapeAttr(ct("fields.contextTokenLimitFor", { model: item.name }))}"></label>
      <label class="mp-provider-model-image-generation${imageGenerationSupported ? "" : " is-unsupported"}" title="${escapeAttr(imageGenerationSupported ? ct("fields.imageGenerationHelp") : ct("fields.imageGenerationUnsupported"))}"><input type="checkbox" data-mp-model-image-generation="${escapeAttr(item.name)}" ${item.imageGeneration ? "checked" : ""} ${imageGenerationSupported ? "" : "disabled"}><span class="mp-provider-model-image-track" aria-hidden="true"></span><span class="mp-provider-model-image-copy"><strong>${escapeHtml(ct("fields.imageGeneration"))}</strong>${imageGenerationSupported ? "" : `<small>${escapeHtml(ct("fields.protocolUnsupported"))}</small>`}</span></label>
      <button class="mp-provider-model-visibility" type="button" data-mp-model-visibility="${escapeAttr(item.name)}" data-hidden="${item.hidden ? "true" : "false"}" aria-pressed="${item.hidden ? "true" : "false"}" aria-label="${escapeAttr(ct(item.hidden ? "actions.showModel" : "actions.hideModel", { model: item.name }))}" ${hideDisabled ? "disabled" : ""}>${visibilityIcon}</button>
      ${item.manual ? `<button class="mp-provider-model-remove" type="button" data-mp-remove-manual-model="${escapeAttr(item.name)}" aria-label="${escapeAttr(ct("actions.removeManualModel", { model: item.name }))}">×</button>` : `<span class="mp-provider-model-remove-placeholder" aria-hidden="true"></span>`}
    </div>`;
  }).join("");
  // Visibility is only a picker preference. Keep a stable internal default even
  // when every model is hidden, and fall back to the first configured model when
  // a stale default no longer exists in the discovered catalog.
  const effectiveDefaultModel = providerDefaultModelForDraft(draft, configs);
  return `<div class="mp-provider-model-workspace" data-mp-model-workspace data-models-ready="${draft.modelsReady ? "true" : "false"}" data-models-stale="${draft.modelsStale ? "true" : "false"}">
    <input type="hidden" name="model" value="${escapeAttr(effectiveDefaultModel)}">
    <div class="mp-provider-model-toolbar"><button class="mp-action" type="button" data-mp-fetch-models ${(modelBusy || !sensitiveAccessAllowed) ? `disabled${modelBusy ? " aria-busy=\"true\"" : ""}` : ""}>${escapeHtml(modelBusy ? ct("actions.fetchingModels") : ct(draft.modelsReady ? "actions.refetchModels" : "actions.fetchModels"))}</button>${configs.length ? `<button class="mp-provider-model-visibility mp-provider-model-visibility-all" type="button" data-mp-model-visibility-all data-all-visible="${allVisible ? "true" : "false"}" aria-label="${escapeAttr(ct(allVisible ? "actions.hideAllModels" : "actions.showAllModels"))}" title="${escapeAttr(ct(allVisible ? "actions.hideAllModels" : "actions.showAllModels"))}">${allVisible ? eyeOnIcon : eyeOffIcon}</button>` : ""}</div>
    ${rows ? `<div class="mp-provider-model-config-list" role="group" aria-label="${escapeAttr(ct("createPage.modelListLabel"))}">${rows}</div>` : `<div class="mp-provider-model-empty settings-alert">${escapeHtml(ct("createPage.modelEmpty"))}</div>`}
    <div class="mp-provider-manual-model"><input type="text" data-mp-manual-model-input autocomplete="off" spellcheck="false" placeholder="${escapeAttr(ct("fields.manualModelPlaceholder"))}" aria-label="${escapeAttr(ct("fields.manualModel"))}"><button class="mp-action" type="button" data-mp-add-manual-model>${escapeHtml(ct("actions.addManualModel"))}</button></div>
    <small data-settings-help-copy>${escapeHtml(ct("createPage.manualModelHelp"))}</small>
  </div>`;
}

function renderCreateProtocolChoices(selected) {
  const types = [
    ["openai", "typeLabels.openai"],
    ["openai-compatible", "typeLabels.openaiCompatible"],
    ["anthropic", "typeLabels.anthropic"],
    ["gemini-interactions", "typeLabels.geminiInteractions"],
  ];
  return `<div class="mp-provider-create-protocol-options">${types.map(([value, labelKey]) => `<label><input type="radio" name="type" value="${escapeAttr(value)}" ${value === selected ? "checked" : ""}><span>${escapeHtml(ct(labelKey))}</span></label>`).join("")}</div>`;
}

function providerProtocolLabelKey(type) {
  return ({
    openai: "typeLabels.openai",
    "openai-compatible": "typeLabels.openaiCompatible",
    anthropic: "typeLabels.anthropic",
    "gemini-interactions": "typeLabels.geminiInteractions",
  })[stringValue(type)] || "typeLabels.openaiCompatible";
}

function providerCreateBaseURLPlaceholder(type, current = "") {
  const configured = stringValue(current);
  if (configured) return configured;
  return ({
    openai: "https://api.openai.com/v1",
    anthropic: "https://api.anthropic.com",
    "gemini-interactions": "https://generativelanguage.googleapis.com/v1beta/interactions",
  })[stringValue(type)] || "https://api.example.com/v1";
}

function renderProviderAPIKeyVisibilityButton() {
  return `<button class="mp-provider-secret-toggle" type="button" data-mp-toggle-api-key aria-pressed="false" aria-label="${escapeAttr(ct("actions.showApiKey"))}" title="${escapeAttr(ct("actions.showApiKey"))}">
    <svg class="mp-provider-secret-icon mp-provider-secret-icon-show" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M2 12s3.8-7 10-7 10 7 10 7-3.8 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="2.5"/></svg>
    <svg class="mp-provider-secret-icon mp-provider-secret-icon-hide" viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m3 3 18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.2A10.8 10.8 0 0 1 12 4c5.2 0 9.2 4 10.5 8a11.7 11.7 0 0 1-3.1 4.8M6.2 6.2A11.8 11.8 0 0 0 1.5 12c1.3 4 5.3 8 10.5 8 1.3 0 2.5-.2 3.6-.7"/></svg>
  </button>`;
}

export function renderProviderCreatePage(consoleState = {}) {
  const state = { mode: "create", type: "openai", draft: null, busy: {}, result: null, sensitiveAccessAllowed: true, ...consoleState };
  const draft = createProviderDraft(state.type, state.draft || null);
  const editing = state.mode === "edit";
  const sensitiveAccessAllowed = state.sensitiveAccessAllowed !== false;
  const createDraftPlaceholder = !editing && !state.dirty;
  const nameEditable = !editing || isProviderDeletable(draft);
  const deleteName = editing ? stringValue(state.providerName || draft.name) : draft.name;
  const messageTestBusy = Boolean(state.busy?.[`message-test:${draft.name}`]);
  const modelBusy = Boolean(state.busy?.[`models:${draft.name}`]);
  const saveBusy = Boolean(state.busy?.[`save:${draft.name}`]);
  const deleteBusy = Boolean(state.busy?.[`delete:${draft.name}`]);
  const busy = messageTestBusy || modelBusy || saveBusy || deleteBusy;
  const usable = providerModelDraftUsable(draft);
  const providerPrefix = draft.name || "provider";
  const modelReference = `${providerPrefix}:${draft.model || "your-model"}`;
  const nameError = stringValue(state.nameError);
  const apiKeyHelp = editing && draft.apiKeyPersisted ? ct("fields.apiKeyPersisted", { lastFive: draft.apiKeyLastFive }) : editing ? ct("fields.apiKeyBlankKeepsCurrent") : ct("createPage.apiKeyHelp");
  const apiKeyPlaceholder = editing ? ct("fields.apiKeyEditingPlaceholder") : ct("createPage.apiKeyPlaceholder");
  const proxyHelp = editing && draft.proxyAuthPersisted ? ct("fields.proxyAuthPersisted") : ct("createPage.proxyHelp");
  const baseURLPlaceholder = providerCreateBaseURLPlaceholder(draft.type, draft.baseUrl);
  const footerText = usable ? ct("createPage.footerHint") : draft.modelsStale ? ct("createPage.footerStale") : ct("createPage.footerNeedsModels");
  const result = state.result && typeof state.result === "object" ? `<div class="mp-provider-result settings-alert ${escapeAttr(state.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(state.result.message || "")}</div>` : "";
  return `<form class="mp-provider-page mp-provider-create-page mp-provider-create-form mp-provider-reference-layout settings-page-section${editing ? " mp-provider-edit-page" : ""}" data-mp-provider-form aria-labelledby="mp-provider-create-title" aria-describedby="mp-provider-create-description" aria-busy="${busy ? "true" : "false"}">
    <header class="mp-provider-reference-header">
      <p class="mp-provider-kicker">${escapeHtml(ct(editing ? "drawer.editProvider" : "createPage.kicker"))}</p>
      <div class="mp-provider-reference-title-row">
        <button class="mp-provider-reference-back" type="button" data-mp-close-drawer aria-label="${escapeAttr(ct("createPage.back"))}" title="${escapeAttr(ct("createPage.back"))}"><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m15 18-6-6 6-6"/></svg></button>
        <div class="mp-provider-reference-title-copy"><div class="mp-provider-reference-title-line"><h1 id="mp-provider-create-title">${escapeHtml(providerDisplayName(draft))}</h1><span class="mp-provider-reference-type">${escapeHtml(ct(providerProtocolLabelKey(draft.type)))}</span></div><p id="mp-provider-create-description" data-settings-help-copy>${escapeHtml(ct(editing ? "drawer.configurationDescription" : "createPage.description"))}</p></div>
        ${isProviderDeletable(draft) && editing ? `<button class="mp-provider-reference-delete" type="button" data-mp-delete-provider="${escapeAttr(deleteName)}" aria-label="${escapeAttr(ct("actions.delete"))}" title="${escapeAttr(ct("actions.delete"))}" ${deleteBusy ? "disabled aria-busy=\"true\"" : ""}><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13M10 11v5m4-5v5"/></svg></button>` : ""}
      </div>
    </header>

    <div class="mp-provider-reference-body">
      ${sensitiveAccessAllowed ? "" : `<div class="mp-provider-sensitive-access settings-alert attention" role="alert">${escapeHtml(ct("messages.sensitiveAccessRequiresFullSession"))}</div>`}
      <section class="mp-provider-reference-section mp-provider-reference-connection" aria-labelledby="mp-provider-create-connection-title">
        <div class="mp-provider-reference-section-heading mp-visually-hidden"><h2 id="mp-provider-create-connection-title">${escapeHtml(ct("createPage.connectionTitle"))}</h2><p data-settings-help-copy>${escapeHtml(ct("createPage.connectionDescription"))}</p></div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-name">${escapeHtml(ct("fields.providerName"))}</label><small id="mp-provider-create-name-help" data-settings-help-copy>${escapeHtml(ct("createPage.nameHelp", { example: modelReference }))}</small></div><input id="mp-provider-create-name" name="name" value="${escapeAttr(createDraftPlaceholder ? "" : draft.name)}" autocomplete="off" placeholder="${escapeAttr(createDraftPlaceholder ? draft.name : "")}" pattern="[A-Za-z0-9][A-Za-z0-9._-]*" maxlength="64" aria-invalid="${nameError ? "true" : "false"}" aria-describedby="mp-provider-create-name-help mp-provider-create-name-error" spellcheck="false" ${nameEditable ? "required" : "readonly"}><small id="mp-provider-create-name-error" class="mp-provider-field-error" data-mp-name-error role="alert" aria-live="polite" ${nameError ? "" : "hidden"}>${escapeHtml(nameError)}</small></div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-prefix">${escapeHtml(ct("fields.prefix"))}</label><small data-settings-help-copy>${escapeHtml(ct("createPage.prefixHelp"))}</small></div><input id="mp-provider-create-prefix" value="${escapeAttr(providerPrefix)}" readonly data-mp-provider-prefix-preview></div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-api-key">${escapeHtml(ct("fields.apiKey"))}</label><small data-settings-help-copy>${escapeHtml(apiKeyHelp)}</small></div><div class="mp-provider-secret-control"><input id="mp-provider-create-api-key" name="apiKey" type="password" value="${escapeAttr(draft.apiKeyDraft ? draft.apiKey : "")}" autocomplete="new-password" placeholder="${escapeAttr(apiKeyPlaceholder)}" spellcheck="false">${renderProviderAPIKeyVisibilityButton()}</div>${editing && draft.apiKeyPersisted ? `<label class="mp-provider-create-clear-key"><input name="clearApiKey" type="checkbox" ${draft.clearApiKey ? "checked" : ""} data-mp-clear-api-key><span>${escapeHtml(ct("fields.clearApiKey"))}</span></label>` : ""}</div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-base-url">${escapeHtml(ct("fields.baseUrl"))}</label><small data-settings-help-copy>${escapeHtml(ct("createPage.baseUrlHelp"))}</small></div><input id="mp-provider-create-base-url" name="baseUrl" type="url" value="${escapeAttr(createDraftPlaceholder ? "" : draft.baseUrl)}" autocomplete="url" placeholder="${escapeAttr(baseURLPlaceholder)}" spellcheck="false"></div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-proxy">${escapeHtml(ct("fields.proxyUrl"))}</label><small data-settings-help-copy>${escapeHtml(proxyHelp)}</small></div><input id="mp-provider-create-proxy" name="proxyUrl" type="text" inputmode="url" value="${escapeAttr(draft.proxyUrl || "")}" autocomplete="off" placeholder="http://user:password@127.0.0.1:7890" spellcheck="false">${editing && draft.proxyAuthConfigured ? `<label class="mp-provider-create-clear-key"><input name="clearProxyAuth" type="checkbox" ${draft.clearProxyAuth ? "checked" : ""} data-mp-clear-proxy-auth><span>${escapeHtml(ct("fields.clearProxyAuth"))}</span></label>` : ""}</div>
        <div class="mp-provider-reference-field mp-provider-create-field"><div class="mp-provider-reference-label"><label for="mp-provider-create-user-agent">${escapeHtml(ct("fields.userAgent"))}</label><small data-settings-help-copy>${escapeHtml(ct("createPage.userAgentHelp"))}</small></div><input id="mp-provider-create-user-agent" name="userAgent" type="text" value="${escapeAttr(draft.userAgent || "")}" maxlength="512" autocomplete="off" placeholder="Autoto" spellcheck="false"></div>
        <div class="mp-provider-reference-switch-list">
          <label class="mp-provider-flat-switch"><input name="apiKeyOptional" type="checkbox" ${draft.apiKeyOptional ? "checked" : ""}><span class="mp-provider-flat-switch-track" aria-hidden="true"></span><span class="mp-provider-flat-switch-copy"><strong>${escapeHtml(ct("fields.apiKeyOptional"))}</strong><small data-settings-help-copy>${escapeHtml(ct("createPage.apiKeyOptionalHelp"))}</small></span></label>
          <label class="mp-provider-flat-switch"><input name="insecureSkipTLSVerify" type="checkbox" ${draft.insecureSkipTLSVerify ? "checked" : ""}><span class="mp-provider-flat-switch-track" aria-hidden="true"></span><span class="mp-provider-flat-switch-copy"><strong>${escapeHtml(ct("fields.insecureSkipTLSVerify"))}</strong><small data-settings-help-copy>${escapeHtml(ct("createPage.tlsHelp"))}</small></span></label>
        </div>
        ${draft.insecureSkipTLSVerify ? `<div class="mp-provider-security-warning settings-alert attention" role="alert">${escapeHtml(ct("createPage.tlsWarning"))}</div>` : ""}
        <div class="mp-provider-reference-field mp-provider-reference-headers"><div class="mp-provider-reference-label"><span>${escapeHtml(ct("fields.requestHeaders"))}</span><small data-settings-help-copy>${escapeHtml(ct("createPage.headersHelp"))}</small></div><div class="mp-provider-header-list" data-mp-request-header-list>${renderProviderRequestHeaderRows(draft.requestHeaders)}</div><button class="mp-provider-header-add-bar" type="button" data-mp-add-request-header><span aria-hidden="true">＋</span>${escapeHtml(ct("actions.addHeader"))}</button></div>
      </section>

      <section class="mp-provider-reference-section mp-provider-reference-protocol" aria-labelledby="mp-provider-create-protocol-title"><div class="mp-provider-reference-section-heading"><h2 id="mp-provider-create-protocol-title">${escapeHtml(ct("fields.protocol"))}</h2><p data-settings-help-copy>${escapeHtml(ct("createPage.protocolHelp"))}</p></div><fieldset class="mp-provider-create-protocol"><legend class="mp-visually-hidden">${escapeHtml(ct("fields.protocol"))}</legend>${renderCreateProtocolChoices(draft.type)}</fieldset></section>

      <section class="mp-provider-reference-section mp-provider-reference-models" aria-labelledby="mp-provider-create-model-title"><div class="mp-provider-reference-section-heading"><h2 id="mp-provider-create-model-title">${escapeHtml(ct("createPage.modelTitle"))}</h2><p data-settings-help-copy>${escapeHtml(ct("createPage.modelDescription"))}</p></div>${renderProviderModelEditor(draft, modelBusy, sensitiveAccessAllowed, { allowEmpty: true })}<div class="mp-provider-model-test-actions settings-inline-actions"><button class="mp-action" type="button" data-mp-test-provider ${!usable || messageTestBusy || !sensitiveAccessAllowed ? "disabled" : ""} ${messageTestBusy ? "aria-busy=\"true\"" : ""}>${escapeHtml(messageTestBusy ? ct("test.sending") : ct("actions.sendTest"))}</button></div></section>
      ${result}
    </div>

    <footer class="mp-provider-reference-footer"><span>${escapeHtml(footerText)}</span><div class="mp-provider-create-footer-actions settings-inline-actions"><button class="mp-action" type="button" data-mp-close-drawer>${escapeHtml(ct("actions.discardChanges"))}</button><button class="mp-action primary" type="submit" data-mp-save-provider ${!usable || saveBusy || !sensitiveAccessAllowed ? "disabled" : ""} ${saveBusy ? "aria-busy=\"true\"" : ""}>${escapeHtml(saveBusy ? ct("actions.saving") : ct("actions.saveAndEnable"))}</button></div></footer>
  </form>${renderProviderMessageTestDialog(state, draft)}`;
}

export function renderProviderConsolePage({ providers = [], consoleState = {} } = {}) {
  const state = {
    search: "",
    category: "all",
    modal: "",
    drawer: "",
    mode: "",
    type: "",
    draft: null,
    busy: {},
    result: null,
    ...consoleState,
  };
  if (!Object.hasOwn(categoryMeta, state.category)) state.category = "all";
  if (state.drawer === "provider" && (state.mode === "create" || state.mode === "edit")) return renderProviderCreatePage(state);
  const stats = providerConsoleStats(providers);
  const filtered = filterConsoleProviders(providers, state);
  const categories = ["official", "custom"];
  const sectionHTML = categories
    .filter((category) => state.category === "all" || state.category === category)
    .map((category) => {
      const cards = filtered.filter((provider) => providerCategory(provider) === category);
      if (!cards.length) return "";
      const panelId = `mp-provider-panel-${category}`;
      const headingId = `mp-provider-section-title-${category}`;
      return `<section class="mp-provider-section settings-card" id="${panelId}" data-mp-category-section="${escapeAttr(category)}" aria-labelledby="${headingId}">
        <header class="mp-provider-section-head settings-card-header"><h2 id="${headingId}">${escapeHtml(ct(categoryMeta[category].titleKey))}</h2><span class="settings-badge">${escapeHtml(String(cards.length))}</span></header>
        <div class="mp-provider-grid settings-card-content">${cards.map((provider) => renderProviderCard(provider, state)).join("")}</div>
      </section>`;
    }).join("");
  const result = state.result && typeof state.result === "object"
    ? `<div class="mp-provider-result settings-alert ${escapeAttr(state.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(state.result.message || "")}</div>`
    : "";
  return `<div class="mp-provider-page settings-page-section" aria-labelledby="mp-provider-page-title">
    <header class="mp-provider-head settings-card settings-card-header">
      <div><p class="mp-provider-kicker">${escapeHtml(ct("kicker"))}</p><h1 id="mp-provider-page-title" class="settings-card-title">${escapeHtml(ct("title"))}</h1><p class="settings-card-description" data-settings-help-copy>${escapeHtml(ct("description"))}</p></div>
      <div class="mp-provider-head-actions settings-inline-actions"><button class="mp-action" type="button" data-mp-refresh-models>${escapeHtml(ct("actions.refreshModels"))}</button><button class="mp-action primary" type="button" data-mp-open-types>+ ${escapeHtml(ct("addProvider"))}</button></div>
    </header>
    <div class="mp-stat-grid settings-stat-grid" aria-label="${escapeAttr(ct("statistics"))}">
      ${renderStat(ct("stats.total"), stats.total)}${renderStat(ct("stats.enabled"), stats.enabled)}${renderStat(ct("stats.models"), stats.models)}${renderStat(ct("stats.attention"), stats.attention)}
    </div>
    <section class="mp-provider-toolbar settings-card settings-toolbar" aria-label="${escapeAttr(ct("category"))}">
      <label class="mp-provider-search settings-form-field"><span class="mp-visually-hidden">${escapeHtml(ct("searchProviders"))}</span><input type="search" value="${escapeAttr(state.search)}" placeholder="${escapeAttr(ct("searchPlaceholder"))}" data-mp-provider-search></label>
      <div class="mp-category-tabs" role="group" aria-label="${escapeAttr(ct("category"))}">
        ${Object.entries(categoryMeta).map(([key, item]) => `<button class="mp-category-tab ${state.category === key ? "active" : ""}" type="button" aria-pressed="${state.category === key ? "true" : "false"}" data-mp-category="${escapeAttr(key)}">${escapeHtml(ct(item.labelKey))}</button>`).join("")}
      </div>
    </section>
    ${result}
    <div class="mp-provider-sections">${sectionHTML || `<div class="mp-empty settings-card settings-alert" role="status">${escapeHtml(ct("messages.noResults"))}</div>`}</div>
  </div>`;
}

function renderStat(label, value) {
  return `<div class="mp-stat settings-stat-card"><strong>${escapeHtml(String(value))}</strong><span>${escapeHtml(label)}</span></div>`;
}

// Compact model preview for a provider card: the first few visible model names,
// a "+N more models" line, a total-count badge, and a hidden-count badge.
function renderModelPreview(provider, state = {}) {
  const configs = Array.isArray(provider.modelConfigs) && provider.modelConfigs.length
    ? provider.modelConfigs.map((item) => ({ name: stringValue(item?.name), hidden: Boolean(item?.hidden) }))
    : normalizedDiscoveredModels(provider.models).map((name) => ({ name, hidden: false }));
  const named = configs.filter((item) => item.name);
  if (!named.length) return "";
  const hiddenMap = (state?.modelVisibility?.hiddenModels || state?.hiddenModels || {});
  const providerName = stringValue(provider.name);
  const isHidden = (item) => item.hidden || Boolean(hiddenMap[`${providerName}:${item.name}`]);
  const visible = named.filter((item) => !isHidden(item)).map((item) => item.name);
  const hiddenCount = named.length - visible.length;
  const shown = visible.slice(0, 3);
  const moreVisible = visible.length - shown.length;
  // The "+N more" line and the count badge share one row so the card spends a
  // row on model names instead of on two separate summary lines.
  return `<div class="mp-provider-model-preview">
    <div class="mp-provider-model-lines">${shown.map((name) => `<div class="mp-model-line" title="${escapeAttr(name)}">${escapeHtml(name)}</div>`).join("")}</div>
    <div class="mp-provider-model-meta">${moreVisible > 0 ? `<div class="mp-model-more">+${moreVisible} ${escapeHtml(ct("fields.moreModels"))}</div>` : ""}<div class="mp-provider-model-counts"><span class="mp-model-count-badge settings-badge">${escapeHtml(ct("fields.modelsBadge", { count: visible.length }))}</span>${hiddenCount > 0 ? `<span class="mp-model-hidden-badge settings-badge" title="${escapeAttr(ct("fields.hiddenCount", { count: hiddenCount }))}">+${hiddenCount}</span>` : ""}</div></div>
  </div>`;
}

function renderProviderCard(provider, state = {}) {
  const disabled = !provider.enabled;
  const deletable = isProviderDeletable(provider);
  const toggleBusy = Boolean(state.busy?.[`toggle:${provider.name}`]);
  const deleteBusy = Boolean(state.busy?.[`delete:${provider.name}`]);
  const busy = toggleBusy || deleteBusy;
  const displayName = providerDisplayName(provider);
  const toggleLabel = ct(provider.enabled ? "actions.disableProvider" : "actions.enableProvider");
  // Minimal card: name, type badge, toggle, and the edit/delete footer. Status
  // and errors live in the provider's edit page to keep the list compact.
  return `<article class="mp-provider-card settings-card${disabled ? " is-disabled" : ""}${deletable ? " is-custom" : ""}" data-mp-provider-card="${escapeAttr(provider.name)}" data-disabled="${disabled ? "true" : "false"}" data-origin="${escapeAttr(provider.origin || "unknown")}" aria-busy="${busy ? "true" : "false"}">
    <header class="mp-provider-card-head settings-card-header"><div class="mp-provider-card-identity"><div><h3 class="settings-card-title">${escapeHtml(displayName)}</h3></div></div><div class="mp-provider-card-controls"><button class="mp-provider-switch ${provider.enabled ? "is-on" : "is-off"}" type="button" role="switch" aria-checked="${provider.enabled ? "true" : "false"}" aria-label="${escapeAttr(`${toggleLabel}: ${displayName}`)}" title="${escapeAttr(toggleLabel)}" data-mp-provider-toggle="${escapeAttr(provider.name)}" ${busy ? "disabled" : ""}><span class="mp-provider-switch-thumb" aria-hidden="true"></span></button></div></header>
    ${renderModelPreview(provider, state)}
    <footer class="mp-provider-card-actions"><button class="mp-provider-card-open" type="button" data-mp-provider-open="${escapeAttr(provider.name)}" aria-label="${escapeAttr(ct("aria.configureProvider", { provider: displayName }))}">${escapeHtml(ct("drawer.editProvider"))}</button>${deletable ? `<button class="mp-provider-delete" type="button" data-mp-delete-provider="${escapeAttr(provider.name)}" aria-label="${escapeAttr(`${ct("actions.delete")}: ${displayName}`)}" title="${escapeAttr(ct("actions.delete"))}" ${busy ? "disabled" : ""}><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13M10 11v5m4-5v5"/></svg><span>${escapeHtml(ct("actions.delete"))}</span></button>` : ""}</footer>
  </article>`;
}

function renderProviderTypeModal() {
  return `<div class="mp-provider-type-modal" data-mp-backdrop="modal" role="presentation"><section class="mp-modal-panel settings-card" role="dialog" aria-modal="true" tabindex="-1" aria-labelledby="mp-provider-type-title" aria-describedby="mp-provider-type-description">
    <header class="mp-modal-head settings-card-header"><div><p class="mp-provider-kicker">${escapeHtml(ct("addProvider"))}</p><h2 id="mp-provider-type-title" class="settings-card-title">${escapeHtml(ct("typeModalTitle"))}</h2><p id="mp-provider-type-description" class="settings-card-description" data-settings-help-copy>${escapeHtml(ct("typeSelectDescription"))}</p></div></header>
    <div class="mp-provider-type-grid settings-card-content">${providerTypeTemplates.map((template) => `<button class="mp-provider-type-option" type="button" data-mp-select-type="${escapeAttr(template.key)}"><strong>${escapeHtml(ct(`templates.${template.templateKey}.title`))}</strong><span data-settings-help-copy>${escapeHtml(ct(`templates.${template.templateKey}.description`))}</span></button>`).join("")}</div>
    <footer class="mp-modal-foot settings-card-footer"><button class="mp-icon-button" type="button" data-mp-close-modal aria-label="${escapeAttr(ct("actions.closeModal"))}">×</button></footer>
  </section></div>`;
}

function renderProviderDrawer(state) {
  const content = state.mode === "create" || state.mode === "edit" ? renderProviderCreatePage(state) : renderConfigDrawerForm(state);
  const busy = Object.values(state.busy || {}).some(Boolean);
  return `<div class="mp-provider-drawer-backdrop" data-mp-backdrop="drawer" role="presentation"><aside class="mp-provider-drawer settings-card" role="dialog" aria-modal="true" tabindex="-1" aria-busy="${busy ? "true" : "false"}" aria-labelledby="mp-drawer-title" aria-describedby="mp-drawer-description">${content}</aside></div>`;
}

function renderConfigDrawerForm(state) {
  const draft = createProviderDraft(state.type, state.draft || null);
  const editing = state.mode === "edit";
  const createDraftPlaceholder = !editing && !state.dirty;
  const nameEditable = !editing || isProviderDeletable(draft);
  const deleteName = editing ? stringValue(state.providerName || draft.name) : draft.name;
  const messageTestBusy = Boolean(state.busy?.[`message-test:${draft.name}`]);
  const modelBusy = Boolean(state.busy?.[`models:${draft.name}`]);
  const saveBusy = Boolean(state.busy?.[`save:${draft.name}`]);
  const deleteBusy = Boolean(state.busy?.[`delete:${draft.name}`]);
  const discoveredModels = normalizedDiscoveredModels(draft.models);
  const modelList = discoveredModels.length
    ? `<datalist id="mp-provider-model-options">${discoveredModels.map((model) => `<option value="${escapeAttr(model)}"></option>`).join("")}</datalist>`
    : "";
  const modelChoices = renderModelChoiceSelect(discoveredModels, draft.model, "mp-provider-model-choice");
  return `<form data-mp-provider-form>
    <header class="mp-drawer-head settings-card-header"><div><p class="mp-provider-kicker">${escapeHtml(editing ? ct("drawer.editProvider") : ct("drawer.createProvider"))}</p><h2 id="mp-drawer-title" class="settings-card-title">${escapeHtml(providerDisplayName(draft))}</h2><p id="mp-drawer-description" class="settings-card-description" data-settings-help-copy>${escapeHtml(ct("drawer.configurationDescription"))}</p></div><button class="mp-icon-button" type="button" data-mp-close-drawer aria-label="${escapeAttr(ct("actions.closeDrawer"))}">×</button></header>
    <div class="mp-drawer-body settings-card-content">
      <section class="mp-config-section settings-page-section"><h3>${escapeHtml(ct("steps.basic"))}</h3>
        <label class="settings-form-field">${escapeHtml(ct("fields.name"))}<input name="name" value="${escapeAttr(createDraftPlaceholder ? "" : draft.name)}" autocomplete="off" placeholder="${escapeAttr(createDraftPlaceholder ? draft.name : "")}" ${nameEditable ? "required" : "readonly"}></label>
        <label class="settings-form-field">${escapeHtml(ct("fields.protocol"))}<select name="type">${renderTypeOptions(draft.type)}</select></label>
        <label class="settings-form-field">${escapeHtml(ct("fields.finalModelExample", { provider: draft.name || "provider", model: draft.model || "your-model" }))}<input value="${escapeAttr(`${draft.name || "provider"}:${draft.model || "your-model"}`)}" readonly data-mp-model-example></label>
        <label class="settings-form-field">${escapeHtml(ct("fields.apiKey"))}<input name="apiKey" type="password" value="" autocomplete="off" placeholder="${escapeAttr(ct(editing ? "fields.apiKeyEditingPlaceholder" : "createPage.apiKeyPlaceholder"))}"></label><small data-settings-help-copy>${escapeHtml(editing && draft.apiKeyPersisted ? ct("fields.apiKeyPersisted", { lastFive: draft.apiKeyLastFive }) : ct("fields.apiKeyBlankKeepsCurrent"))}</small>
        ${editing && draft.apiKeyPersisted ? `<label class="mp-check settings-form-field"><input name="clearApiKey" type="checkbox" ${draft.clearApiKey ? "checked" : ""} data-mp-clear-api-key> ${escapeHtml(ct("fields.clearApiKey"))}</label>` : ""}
        <label class="settings-form-field">${escapeHtml(ct("fields.baseUrl"))}<input name="baseUrl" type="url" value="${escapeAttr(createDraftPlaceholder ? "" : draft.baseUrl)}" autocomplete="url" placeholder="${escapeAttr(createDraftPlaceholder ? (draft.baseUrl || "https://api.example.com/v1") : "https://api.example.com/v1")}"></label>
        <label class="settings-form-field">${escapeHtml(ct("fields.defaultModel"))}<input name="model" data-select-on-focus="true" value="${escapeAttr(draft.model)}" autocomplete="off" placeholder="${escapeAttr(ct("fields.defaultModelPlaceholder"))}" ${discoveredModels.length ? "list=\"mp-provider-model-options\"" : ""}>${modelList}</label>${modelChoices}
      </section>
      <details class="mp-config-section settings-page-section"><summary>${escapeHtml(ct("steps.advanced"))}</summary>
        <label class="settings-form-field">${escapeHtml(ct("fields.maxTokens"))}<input name="maxTokens" data-select-on-focus="true" type="number" min="0" step="1" value="${escapeAttr(draft.maxTokens || "")}"></label>
        <label class="mp-check settings-form-field"><input name="apiKeyOptional" type="checkbox" ${draft.apiKeyOptional ? "checked" : ""}> ${escapeHtml(ct("fields.apiKeyOptional"))}</label>
      </details>
      ${state.result && typeof state.result === "object" ? `<div class="mp-provider-result settings-alert ${escapeAttr(state.result.tone || "info")}" role="status" aria-live="polite">${escapeHtml(state.result.message || "")}</div>` : ""}
    </div>
    <footer class="mp-drawer-foot settings-card-footer settings-inline-actions"><div class="settings-inline-actions"><button class="mp-action" type="button" data-mp-fetch-models ${modelBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(modelBusy ? ct("actions.fetchingModels") : ct("actions.fetchModels"))}</button><button class="mp-action" type="button" data-mp-test-provider ${messageTestBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(messageTestBusy ? ct("test.sending") : ct("actions.sendTest"))}</button></div><div class="settings-inline-actions">${isProviderDeletable(draft) && editing ? `<button class="mp-action danger" type="button" data-mp-delete-provider="${escapeAttr(deleteName)}" ${deleteBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(ct("actions.delete"))}</button>` : ""}<button class="mp-action primary" type="submit" data-mp-save-provider ${saveBusy ? "disabled aria-busy=\"true\"" : ""}>${escapeHtml(saveBusy ? ct("actions.saving") : ct("actions.saveAndEnable"))}</button></div></footer>
  </form>`;
}

function renderTypeOptions(selected) {
  const types = [
    ["openai", "typeLabels.openai"],
    ["openai-compatible", "typeLabels.openaiCompatible"],
    ["anthropic", "typeLabels.anthropic"],
    ["gemini-interactions", "typeLabels.geminiInteractions"],
  ];
  return types.map(([value, labelKey]) => `<option value="${escapeAttr(value)}" ${value === selected ? "selected" : ""}>${escapeHtml(ct(labelKey))}</option>`).join("");
}
