import { resolveLocale } from "./locale-registry.mjs";
import messagesEN from "./messages-en.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-autoto-themes-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1-settings-cleanup-1-schedule-workspace-1-provider-draft-session-1-context-ring-3-global-background-1-theme-v2-1-formal-theme-assets-1-background-upload-1-goal-command-2";
import backgroundTaskMessages from "./messages-background-tasks.mjs";
import remoteAccessMessages from "./messages-remote-access.mjs?v=remote-control-full-4-remote-full-toggle-3-cloudflared-install-1";
import preferencesMessages from "./messages-preferences.mjs";
import staticExtraMessages from "./messages-static-extra.mjs";
import systemSettingsMessages from "./messages-system-settings.mjs";
import usageHistoryMessages from "./messages-usage-history.mjs";
import messagesZhCN from "./messages-zh-CN.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-autoto-themes-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1-settings-cleanup-1-schedule-workspace-1-provider-draft-session-1-context-ring-3-global-background-1-theme-v2-1-formal-theme-assets-1-background-upload-1-goal-command-2";
import messagesZhTW from "./messages-zh-TW.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-autoto-themes-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1-settings-cleanup-1-schedule-workspace-1-provider-draft-session-1-context-ring-3-global-background-1-theme-v2-1-formal-theme-assets-1-background-upload-1-goal-command-2";

export const uiLocales = Object.freeze(["zh-TW", "zh-CN", "en"]);

function mergeMessageTree(target, source) {
  Object.entries(source || {}).forEach(([key, value]) => {
    if (value && typeof value === "object" && !Array.isArray(value)) {
      const child = target[key] && typeof target[key] === "object" && !Array.isArray(target[key]) ? target[key] : {};
      target[key] = mergeMessageTree(child, value);
    } else {
      target[key] = value;
    }
  });
  return target;
}

function createMergedCatalog(locale, base) {
  return [
    backgroundTaskMessages,
    remoteAccessMessages,
    preferencesMessages,
    staticExtraMessages,
    systemSettingsMessages,
    usageHistoryMessages,
  ].reduce((catalog, pack) => mergeMessageTree(catalog, pack?.[locale]), mergeMessageTree({}, base));
}

export const messageCatalogs = Object.freeze({
  "zh-TW": createMergedCatalog("zh-TW", messagesZhTW),
  "zh-CN": createMergedCatalog("zh-CN", messagesZhCN),
  en: createMergedCatalog("en", messagesEN),
});

function initialLocalePreference() {
  if (!globalThis.localStorage?.getItem) return "zh-CN";
  for (const key of ["autoto.regional", "codeharbor.regional"]) {
    try {
      const raw = globalThis.localStorage?.getItem?.(key);
      if (!raw) continue;
      const value = JSON.parse(raw);
      return value?.locale ?? value?.language ?? value?.lang ?? "auto";
    } catch {}
  }
  return "auto";
}

const localeRuntimeKey = Symbol.for("autoto.i18n.runtime");
const existingLocaleRuntime = globalThis[localeRuntimeKey];
const localeRuntime = existingLocaleRuntime && typeof existingLocaleRuntime === "object" ? existingLocaleRuntime : {};
if (!uiLocales.includes(localeRuntime.activeLocale)) localeRuntime.activeLocale = resolveUILocale(initialLocalePreference());
globalThis[localeRuntimeKey] = localeRuntime;

function lookup(catalog, key) {
  return String(key || "").split(".").reduce((value, part) => value && typeof value === "object" ? value[part] : undefined, catalog);
}

function interpolate(message, params = {}) {
  return String(message).replace(/\{([A-Za-z0-9_]+)\}/g, (match, name) => (
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name] ?? "") : match
  ));
}

export function resolveUILocale(value = "auto") {
  const requested = String(value || "auto").trim();
  const resolved = requested.toLowerCase() === "auto" ? resolveLocale("auto") : requested;
  const normalized = resolved.toLowerCase();
  if (normalized === "zh-tw" || normalized === "zh-hant" || normalized.startsWith("zh-hant-") || normalized === "zh-hk" || normalized === "zh-mo") return "zh-TW";
  if (normalized === "zh" || normalized === "zh-cn" || normalized === "zh-hans" || normalized.startsWith("zh-hans-") || normalized === "zh-sg") return "zh-CN";
  return "en";
}

export function currentUILocale() {
  return localeRuntime.activeLocale;
}

export function t(key, params = {}, locale = localeRuntime.activeLocale) {
  const resolved = resolveUILocale(locale);
  const message = lookup(messageCatalogs[resolved], key) ?? lookup(messageCatalogs["zh-CN"], key) ?? key;
  return interpolate(message, params);
}

export function applyDocumentLocale(locale = localeRuntime.activeLocale, root = globalThis.document) {
  localeRuntime.activeLocale = resolveUILocale(locale);
  const element = root?.documentElement;
  if (element) {
    element.lang = localeRuntime.activeLocale === "zh-TW" ? "zh-Hant-TW" : localeRuntime.activeLocale === "zh-CN" ? "zh-Hans-CN" : "en";
    element.dataset.uiLocale = localeRuntime.activeLocale;
  }
  if (root && "title" in root) root.title = t("app.title");
  return localeRuntime.activeLocale;
}

function nodesWithAttribute(root, attribute) {
  const nodes = [];
  if (root?.nodeType === 1 && root.hasAttribute?.(attribute)) nodes.push(root);
  root?.querySelectorAll?.(`[${attribute}]`)?.forEach((node) => nodes.push(node));
  return nodes;
}

function translateAttribute(root, marker, attribute) {
  nodesWithAttribute(root, marker).forEach((node) => {
    const key = node.getAttribute(marker);
    if (!key) return;
    const translated = t(key);
    if (translated !== key) node.setAttribute(attribute, translated);
  });
}

export function applyStaticTranslations(root = globalThis.document) {
  if (!root) return localeRuntime.activeLocale;
  nodesWithAttribute(root, "data-i18n").forEach((node) => {
    const key = node.getAttribute("data-i18n");
    if (!key) return;
    const translated = t(key);
    if (translated !== key) node.textContent = translated;
  });
  translateAttribute(root, "data-i18n-title", "title");
  translateAttribute(root, "data-i18n-placeholder", "placeholder");
  translateAttribute(root, "data-i18n-aria-label", "aria-label");
  return localeRuntime.activeLocale;
}

export function setUILocale(locale, root = globalThis.document) {
  applyDocumentLocale(locale, root);
  applyStaticTranslations(root);
  return localeRuntime.activeLocale;
}

export function flattenMessageKeys(catalog) {
  const keys = [];
  function visit(value, prefix) {
    Object.entries(value || {}).forEach(([key, child]) => {
      const path = prefix ? `${prefix}.${key}` : key;
      if (child && typeof child === "object" && !Array.isArray(child)) visit(child, path);
      else keys.push(path);
    });
  }
  visit(catalog, "");
  return keys.sort();
}
