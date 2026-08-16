import test from "node:test";
import assert from "node:assert/strict";

import automationMessages from "./messages-automation.mjs";
import appMainExtraMessages from "./messages-app-main-extra.mjs";
import chatRenderingExtraMessages, { t as chatRenderingExtraT } from "./messages-chat-rendering-extra.mjs";
import shellExtraMessages from "./messages-shell-extra.mjs";
import skillsMessages from "./messages-skills.mjs";
import userAccountsMessages from "./messages-user-accounts.mjs";
import {
  applyDocumentLocale,
  applyStaticTranslations,
  currentUILocale,
  flattenMessageKeys,
  messageCatalogs,
  resolveUILocale,
  setUILocale,
  t,
  uiLocales,
} from "./i18n.mjs";

test("all supported UI locales expose the same message keys", () => {
  const expected = flattenMessageKeys(messageCatalogs["zh-CN"]);
  assert.deepEqual(uiLocales, ["zh-TW", "zh-CN", "en"]);
  for (const locale of uiLocales) {
    assert.deepEqual(flattenMessageKeys(messageCatalogs[locale]), expected, locale);
  }
});

test("domain message packs expose matching keys for all locales", () => {
  for (const [name, pack] of Object.entries({ appMainExtra: appMainExtraMessages, automation: automationMessages, chatRenderingExtra: chatRenderingExtraMessages, shellExtra: shellExtraMessages, skills: skillsMessages, userAccounts: userAccountsMessages })) {
    const expected = flattenMessageKeys(pack["zh-CN"]);
    for (const locale of uiLocales) assert.deepEqual(flattenMessageKeys(pack[locale]), expected, `${name}:${locale}`);
  }
});

test("main and automation catalogs align schedule enum translation keys", () => {
  const required = [
    "permissionModes.readOnly", "permissionModes.acceptEdits",
    "environmentModes.workline", "environmentModes.standalone",
    "narratorModes.reuse", "narratorModes.new", "unknownValue",
  ];
  const mainExpected = flattenMessageKeys(messageCatalogs["zh-CN"].schedule);
  const automationExpected = flattenMessageKeys(automationMessages["zh-CN"].automation.schedule);
  for (const key of required) {
    assert.ok(mainExpected.includes(key), `main:${key}`);
    assert.ok(automationExpected.includes(key), `automation:${key}`);
  }
  for (const locale of uiLocales) {
    assert.deepEqual(flattenMessageKeys(messageCatalogs[locale].schedule), mainExpected, `main:${locale}`);
    assert.deepEqual(flattenMessageKeys(automationMessages[locale].automation.schedule), automationExpected, `automation:${locale}`);
  }
  assert.equal(t("schedule.permissionModes.readOnly", {}, "zh-CN"), "只读（推荐）");
  assert.equal(t("schedule.permissionModes.readOnly", {}, "zh-TW"), "唯讀（建議）");
  assert.equal(t("schedule.permissionModes.readOnly", {}, "en"), "Read only (recommended)");
});

test("UI locale resolution supports traditional, simplified, English, and safe fallback", () => {
  assert.equal(resolveUILocale("zh-TW"), "zh-TW");
  assert.equal(resolveUILocale("zh-Hant-HK"), "zh-TW");
  assert.equal(resolveUILocale("zh-CN"), "zh-CN");
  assert.equal(resolveUILocale("zh-Hans-SG"), "zh-CN");
  assert.equal(resolveUILocale("en-US"), "en");
  assert.equal(resolveUILocale("fr-FR"), "en");
});

test("translations interpolate values and fall back to keys", () => {
  assert.equal(t("common.itemCount", { count: 3 }, "zh-TW"), "共 3 項");
  assert.equal(t("common.itemCount", { count: 3 }, "en"), "3 items");
  assert.equal(t("memory.noMatches", { query: "demo" }, "en"), "No memories match “demo”.");
  assert.equal(t("mcp.discoveredTools", { count: 3 }, "zh-TW"), "已發現 3 個 MCP 工具。");
  assert.equal(t("shell.nav.conversation", {}, "zh-TW"), "對話");
  assert.equal(t("shell.nav.schedules", {}, "zh-CN"), "排程");
  assert.equal(t("common.enabled", {}, "en"), "Enabled");
  assert.equal(t("shell.nav.settings", {}, "en"), "Settings");
  assert.equal(t("missing.translation.key", {}, "en"), "missing.translation.key");
});

test("model provider console exposes aligned nested keys for every locale", () => {
  const expected = flattenMessageKeys(messageCatalogs["zh-CN"].modelProvider.console);
  for (const required of [
    "actions.refreshModels",
    "actions.enableProvider",
    "codex.footer",
    "drawer.configurationDescription",
    "fields.apiKeyEditingPlaceholder",
    "messages.currentDraftTestSucceeded",
    "messages.currentDraftTestNeedsApiKey",
    "messages.currentDraftTestFailed",
    "origins.unknown",
    "createPage.networkTitle",
    "fields.proxyUrl",
    "fields.requestHeaders",
    "fields.clientIdentity",
    "fields.insecureSkipTLSVerify",
    "createPage.clientIdentityHelp",
  ]) assert.ok(expected.includes(required), required);

  for (const locale of uiLocales) {
    assert.deepEqual(flattenMessageKeys(messageCatalogs[locale].modelProvider.console), expected, locale);
  }
});

test("model provider console interpolates model, count, and failure details", () => {
  assert.equal(
    t("modelProvider.console.fields.finalModelExample", { provider: "openai", model: "gpt-4.1-mini" }, "zh-CN"),
    "最终模型示例：openai:gpt-4.1-mini",
  );
  assert.equal(t("modelProvider.console.messages.modelCount", { count: 3 }, "zh-TW"), "3 個模型");
  assert.equal(
    t("modelProvider.console.messages.mutationRefreshWarning", { message: "offline" }, "en"),
    "The change succeeded, but refreshing the provider list failed: offline",
  );
});

test("chat activity timeline has concise, safe copy in every locale", () => {
  const keys = [
    "processTitle", "input", "output", "noOutput", "localService",
    "details", "diff", "running", "completed", "failed", "searching", "reading",
    "editing", "writing", "runningCommand", "genericStep", "truncated",
    "verbRead", "verbEdit", "verbWrite", "verbSearch", "verbGlob", "verbCommand",
    "verbWeb", "verbTask", "verbTodo",
    "callingTool", "verbListMCP", "listingMCP", "safetyAudit",
  ];

  for (const locale of uiLocales) {
    const activity = chatRenderingExtraMessages[locale].chatRenderingExtra.activity;
    for (const key of keys) assert.equal(typeof activity[key], "string", `${locale}:${key}`);
    assert.equal(chatRenderingExtraT("activity.processTitle", { count: 3 }, locale).includes("3"), true, locale);

    const copy = Object.values(activity).join(" ").toLowerCase();
    assert.doesNotMatch(copy, /chain of thought|思维链已加密/);
  }

  assert.equal(chatRenderingExtraT("activity.input", {}, "fr-FR"), "输入");
});

// Imports carry no query strings anywhere in the frontend, so every module sees
// the same i18n instance; dynamic message packs must follow its locale directly.
test("dynamic message packs follow the shared i18n locale state", () => {
  const previous = currentUILocale();
  const root = { title: "", documentElement: { lang: "", dataset: {} }, querySelectorAll() { return []; } };
  try {
    setUILocale("zh-TW", root);
    assert.equal(currentUILocale(), "zh-TW");
    assert.equal(chatRenderingExtraT("run.review"), "任務回顧");
    assert.equal(chatRenderingExtraT("run.rollback"), "回復到開始前");
  } finally {
    setUILocale(previous, root);
  }
});

test("static translation keeps existing copy when a key is missing", () => {
  const previous = currentUILocale();
  const missingText = {
    textContent: "保留原文",
    getAttribute(name) { return name === "data-i18n" ? "missing.static.copy" : null; },
  };
  const attributes = new Map([["data-i18n-title", "missing.static.title"], ["title", "保留標題"]]);
  const missingTitle = {
    getAttribute(name) { return attributes.get(name) || null; },
    setAttribute(name, value) { attributes.set(name, value); },
  };
  const root = {
    title: "",
    documentElement: { lang: "", dataset: {} },
    querySelectorAll(selector) {
      if (selector === "[data-i18n]") return [missingText];
      if (selector === "[data-i18n-title]") return [missingTitle];
      return [];
    },
  };
  try {
    applyDocumentLocale("zh-TW", root);
    applyStaticTranslations(root);
    assert.equal(missingText.textContent, "保留原文");
    assert.equal(attributes.get("title"), "保留標題");
  } finally {
    applyDocumentLocale(previous, root);
  }
});

test("document locale updates lang and data-ui-locale", () => {
  const root = { title: "", documentElement: { lang: "", dataset: {} }, querySelectorAll() { return []; } };
  assert.equal(applyDocumentLocale("zh-TW", root), "zh-TW");
  assert.equal(root.documentElement.lang, "zh-Hant-TW");
  assert.equal(root.documentElement.dataset.uiLocale, "zh-TW");
  assert.equal(root.title, "Autoto");

  setUILocale("en", root);
  assert.equal(currentUILocale(), "en");
  assert.equal(root.documentElement.lang, "en");
});
