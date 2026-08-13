import test from "node:test";
import assert from "node:assert/strict";

import {
  firstSettingsItemForCategory,
  groupSettingsItemsByLegacyCategory,
  legacySettingsCategories,
  resolveSettingsCategorySelection,
  settingsCategoryByKey,
  settingsCategoryForItem,
  settingsItemsForCategory,
} from "./settings-categories.mjs";
import { t } from "./i18n.mjs";
import { settingsIconSVG, settingsItemByKey, settingsItems, skillTabs } from "./settings-data.mjs";

test("legacy settings expose the eight category shortcuts in order", () => {
  assert.deepEqual(legacySettingsCategories.map((category) => category.label), [
    "API 设置",
    "聊天平台",
    "记忆",
    "诊断",
    "网络代理",
    "市场",
    "日志",
    "关于",
  ]);
});

test("every existing settings page remains reachable from a legacy category", () => {
  const mapped = new Set(legacySettingsCategories.flatMap((category) => category.items));
  assert.deepEqual([...new Set(settingsItems.map((item) => item.key).filter((key) => !mapped.has(key)))], []);
  assert.equal(settingsCategoryForItem("runtime"), "diagnostics");
  assert.equal(settingsCategoryForItem("providers"), "api");
  assert.equal(settingsCategoryForItem("shared-api"), "api");
  assert.equal(firstSettingsItemForCategory("market"), "skills");
  assert.equal(settingsItemByKey("providers")?.key, "providers");
  assert.equal(settingsItemByKey("shared-api")?.key, "shared-api");
  assert.equal(settingsItemByKey("agent-admin"), null);
  assert.equal(settingsItemByKey("agents"), null);
  assert.equal(settingsItemByKey("worklines-containers"), null);
  assert.deepEqual(legacySettingsCategories.find((category) => category.key === "network")?.items, ["network-search", "remote-access", "peer-collaboration"]);
  assert.equal(legacySettingsCategories.some((category) => category.key === "permissions"), false);
  assert.equal(settingsItemByKey("users"), null);
  assert.equal(legacySettingsCategories.some((category) => category.items.includes("users")), false);
  assert.equal(settingsItemByKey("missing"), null);
});

test("legacy category grouping preserves page order and filters empty groups", () => {
  const groups = groupSettingsItemsByLegacyCategory(settingsItems);
  assert.deepEqual(groups.map((group) => group.key), legacySettingsCategories.map((group) => group.key));
  assert.deepEqual(groups.find((group) => group.key === "api").items.map((item) => item.key), ["providers", "shared-api", "models", "profile", "appearance"]);

  const modelsOnly = groupSettingsItemsByLegacyCategory(settingsItems, (item) => item.key === "models");
  assert.deepEqual(modelsOnly.map((group) => ({ key: group.key, items: group.items.map((item) => item.key) })), [
    { key: "api", items: ["models"] },
  ]);
});

test("category selection exposes only the selected category and marks the active item", () => {
  assert.deepEqual(settingsItemsForCategory(settingsItems, "diagnostics").map((item) => item.key), [
    "runtime",
    "servers-system",
    "storage",
    "terminals",
  ]);

  const selection = resolveSettingsCategorySelection(settingsItems, {
    categoryKey: "diagnostics",
    activeKey: "storage",
  });
  assert.equal(selection.categoryKey, "diagnostics");
  assert.equal(selection.activeKey, "storage");
  assert.equal(selection.activeItem, settingsItemByKey("storage"));
  assert.deepEqual(selection.items.map((item) => [item.key, item.active]), [
    ["runtime", false],
    ["servers-system", false],
    ["storage", true],
    ["terminals", false],
  ]);
});

test("category selection falls back to its first visible item", () => {
  const selection = resolveSettingsCategorySelection(settingsItems, {
    categoryKey: "api",
    activeKey: "runtime",
    predicate: (item) => item.key !== "providers",
  });
  assert.equal(selection.activeKey, "shared-api");
  assert.equal(selection.activeItem, settingsItemByKey("shared-api"));
  assert.deepEqual(selection.items.filter((item) => item.active).map((item) => item.key), ["shared-api"]);

  const empty = resolveSettingsCategorySelection([], { categoryKey: "chat", activeKey: "im-gateway" });
  assert.equal(empty.activeKey, "");
  assert.equal(empty.activeItem, null);
  assert.deepEqual(empty.items, []);
});

test("unknown categories consistently fall back to the api category", () => {
  assert.equal(settingsCategoryByKey("missing").key, "api");
  assert.equal(settingsCategoryForItem("missing", "also-missing"), "api");
  assert.equal(firstSettingsItemForCategory("missing"), "providers");

  const selection = resolveSettingsCategorySelection(settingsItems, {
    categoryKey: "missing",
    activeKey: "models",
  });
  assert.equal(selection.categoryKey, "api");
  assert.equal(selection.activeKey, "models");
  assert.equal(selection.items.find((item) => item.active)?.key, "models");
});

test("settings pages expose a consistent SVG icon set", () => {
  const icons = settingsItems.map((item) => settingsIconSVG(item.icon));
  assert.equal(icons.length, settingsItems.length);
  icons.forEach((icon) => {
    assert.match(icon, /^<svg class="settings-icon-svg"[^>]*viewBox="0 0 24 24"/);
    assert.match(icon, /stroke="currentColor"/);
    assert.doesNotMatch(icon, /[♙▣◈⚙♧✦♢◉⌂☁⇄⌕◇▤▻▭▷▧ⓘ]/);
  });
  assert.match(settingsIconSVG("missing"), /<circle cx="16" cy="7" r="2"/);
});

test("skill tab metadata is sourced from translated messages", () => {
  const mcpTools = skillTabs.find((tab) => tab.key === "mcp-tools");
  assert.deepEqual(mcpTools, {
    key: "mcp-tools",
    label: t("skills.tabs.mcpTools.label"),
    description: t("skills.tabs.mcpTools.description"),
    empty: t("skills.tabs.mcpTools.empty"),
    action: t("skills.tabs.mcpTools.action"),
  });
});
