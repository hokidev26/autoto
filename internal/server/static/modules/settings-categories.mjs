import { t } from "./i18n.mjs";

export const legacySettingsCategories = Object.freeze([
  { key: "api", label: t("settings.category.api"), items: ["providers", "shared-api", "models", "profile", "users", "appearance"] },
  { key: "chat", label: t("settings.category.chat"), items: ["im-gateway"] },
  { key: "memory", label: t("settings.category.memory"), items: ["memory"] },
  { key: "diagnostics", label: t("settings.category.diagnostics"), items: ["runtime", "servers-system", "storage"] },
  { key: "network", label: t("settings.category.network"), items: ["network-search", "remote-access", "peer-collaboration"] },
  { key: "market", label: t("settings.category.market"), items: ["skills"] },
  { key: "logs", label: t("settings.category.logs"), items: ["notifications", "usage", "archive"] },
  { key: "about", label: t("settings.category.about"), items: ["about"] },
]);

const categoryByKey = new Map(legacySettingsCategories.map((category) => [category.key, category]));
const categoryByItem = new Map();
legacySettingsCategories.forEach((category) => category.items.forEach((item) => categoryByItem.set(item, category.key)));

export function settingsCategoryByKey(key) {
  return categoryByKey.get(String(key || "")) || legacySettingsCategories[0];
}

export function settingsCategoryForItem(key, fallback = "api") {
  return categoryByItem.get(String(key || "")) || settingsCategoryByKey(fallback).key;
}

export function firstSettingsItemForCategory(key) {
  return settingsCategoryByKey(key)?.items?.[0] || "providers";
}

export function settingsItemsForCategory(items = [], categoryKey = "api", predicate = () => true) {
  const category = settingsCategoryByKey(categoryKey);
  const itemByKey = new Map((Array.isArray(items) ? items : []).map((item) => [item?.key, item]));
  return category.items
    .map((key) => itemByKey.get(key))
    .filter((item) => item && predicate(item, category));
}

export function resolveSettingsCategorySelection(items = [], options = {}) {
  const { categoryKey = "api", activeKey = "", predicate = () => true } = options || {};
  const category = settingsCategoryByKey(categoryKey);
  const categoryItems = settingsItemsForCategory(items, category.key, predicate);
  const requestedActiveKey = String(activeKey || "");
  const activeItem = categoryItems.find((item) => item.key === requestedActiveKey) || categoryItems[0] || null;
  const resolvedActiveKey = activeItem?.key || "";

  return {
    category,
    categoryKey: category.key,
    activeItem,
    activeKey: resolvedActiveKey,
    items: categoryItems.map((item) => ({ ...item, active: item.key === resolvedActiveKey })),
  };
}

export function groupSettingsItemsByLegacyCategory(items = [], predicate = () => true) {
  return legacySettingsCategories.map((category) => ({
    ...category,
    items: settingsItemsForCategory(items, category.key, predicate),
  })).filter((category) => category.items.length);
}
