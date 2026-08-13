import { resolveLocale } from "./locale-registry.mjs";
import messagesEN from "./messages-en.mjs";
import backgroundTaskMessages from "./messages-background-tasks.mjs";
import peerCollaborationMessages from "./messages-peer-collaboration.mjs";
import remoteAccessMessages from "./messages-remote-access.mjs";
import preferencesMessages from "./messages-preferences.mjs";
import providerSubscriptionAccountsMessages from "./messages-provider-subscription-accounts.mjs";
import setupWizardMessages from "./messages-setup-wizard.mjs";
import staticExtraMessages from "./messages-static-extra.mjs";
import systemSettingsMessages from "./messages-system-settings.mjs";
import usageHistoryMessages from "./messages-usage-history.mjs";
import messagesZhCN from "./messages-zh-CN.mjs";
import messagesZhTW from "./messages-zh-TW.mjs";

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
    peerCollaborationMessages,
    remoteAccessMessages,
    preferencesMessages,
    providerSubscriptionAccountsMessages,
    setupWizardMessages,
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
  for (const key of ["autoto.regional"]) {
    try {
      const raw = globalThis.localStorage?.getItem?.(key);
      if (!raw) continue;
      const value = JSON.parse(raw);
      return value?.locale ?? value?.language ?? value?.lang ?? "auto";
    } catch {}
  }
  return "auto";
}

// Module-local state is safe because every import of this module resolves to
// the same URL (no ?v= query strings anywhere), so the browser and Node both
// evaluate it exactly once. A globalThis-keyed runtime used to bridge the
// duplicate instances created by divergent ?v= imports; that split no longer
// exists, and the source guard test keeps it that way.
let activeLocale = resolveUILocale(initialLocalePreference());

function lookup(catalog, key) {
  return String(key || "").split(".").reduce((value, part) => value && typeof value === "object" ? value[part] : undefined, catalog);
}

// Exported because the messages-*.mjs modules that layer extra catalogues on top
// of this one each need it. They already import from here, and this module does
// not import them, so there is no cycle -- they had private copies only because
// this was not exported.
export function interpolate(message, params = {}) {
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
  return activeLocale;
}

export function t(key, params = {}, locale = activeLocale) {
  const resolved = resolveUILocale(locale);
  const message = lookup(messageCatalogs[resolved], key) ?? lookup(messageCatalogs["zh-CN"], key) ?? key;
  return interpolate(message, params);
}

export function applyDocumentLocale(locale = activeLocale, root = globalThis.document) {
  activeLocale = resolveUILocale(locale);
  const element = root?.documentElement;
  if (element) {
    element.lang = activeLocale === "zh-TW" ? "zh-Hant-TW" : activeLocale === "zh-CN" ? "zh-Hans-CN" : "en";
    element.dataset.uiLocale = activeLocale;
  }
  if (root && "title" in root) root.title = t("app.title");
  return activeLocale;
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
  if (!root) return activeLocale;
  nodesWithAttribute(root, "data-i18n").forEach((node) => {
    const key = node.getAttribute("data-i18n");
    if (!key) return;
    const translated = t(key);
    if (translated !== key) node.textContent = translated;
  });
  translateAttribute(root, "data-i18n-title", "title");
  translateAttribute(root, "data-i18n-placeholder", "placeholder");
  translateAttribute(root, "data-i18n-aria-label", "aria-label");
  return activeLocale;
}

export function setUILocale(locale, root = globalThis.document) {
  applyDocumentLocale(locale, root);
  applyStaticTranslations(root);
  return activeLocale;
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
