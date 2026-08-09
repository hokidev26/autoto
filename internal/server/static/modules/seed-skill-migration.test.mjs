import assert from "node:assert/strict";
import test from "node:test";

import { regionalPrefsKey, skillsPrefsKey } from "./preferences-data.mjs";
import { createSettingsPreferencesController } from "./settings-preferences.mjs";

// Profiles created before the seed carried translation keys hold literal zh-CN
// text in localStorage. Changing the shipped default cannot reach them, because
// normalizeSkillsPreferences only falls back to the default when the stored array
// is missing. These tests cover the migration that does reach them, and the line
// it must not cross: text the user wrote is theirs and stays untouched.
class MemoryStorage {
  constructor(entries = []) {
    this.values = new Map(entries);
  }

  getItem(key) {
    return this.values.has(key) ? this.values.get(key) : null;
  }

  setItem(key, value) {
    this.values.set(key, String(value));
  }

  removeItem(key) {
    this.values.delete(key);
  }
}

function withBrowser(storage, callback) {
  const previousStorage = globalThis.localStorage;
  const previousDocument = globalThis.document;
  globalThis.localStorage = storage;
  globalThis.document = {
    title: "",
    documentElement: { lang: "", dataset: {}, classList: { toggle() {}, contains: () => false } },
    body: { dataset: {}, classList: { toggle() {}, contains: () => false } },
    getElementById: () => null,
    querySelectorAll: () => [],
  };
  try {
    return callback();
  } finally {
    globalThis.document = previousDocument;
    globalThis.localStorage = previousStorage;
  }
}

// Locale is driven through regional preferences rather than setUILocale, because
// that is the path the language selector uses. Calling setUILocale directly is not
// equivalent: reading preferences re-applies the stored locale, and a stored "auto"
// resolves from document.documentElement.lang, which would silently put the
// previous language back and make this test measure nothing.
function controllerFor(state = {}) {
  const controller = createSettingsPreferencesController({
    state,
    loadChatDrafts: () => ({}),
    loadPromptHistory: () => [],
    loadTerminalPreferences: () => ({}),
    normalizeChatDrafts: (value) => value,
    normalizePromptHistory: (value) => value,
    normalizeRecentDirectories: (value) => value,
    normalizeTerminalPreferences: (value) => value,
  });
  return controller;
}

function storageWith(locale, skills) {
  return new MemoryStorage([
    [regionalPrefsKey, JSON.stringify({ locale, timezone: "auto" })],
    [skillsPrefsKey, skills],
  ]);
}

const legacyStored = JSON.stringify({
  commands: [
    {
      id: "review-diff",
      name: "/review-diff",
      description: "审查当前工作区改动并给出风险提示。",
      prompt: "请审查当前工作区变更，重点关注正确性、测试覆盖、安全风险和用户可见行为。",
      enabled: true,
    },
  ],
  mcpServers: [],
});

test("a stored Simplified seed is re-resolved to the active locale", () => {
  withBrowser(storageWith("zh-TW", legacyStored), () => {
    const controller = controllerFor();
    controller.applyRegionalPreferences();
    const commands = controller.currentSkillsPreferences().commands;
    const reviewDiff = commands.find((command) => command.name === "/review-diff");
    assert.ok(reviewDiff, "the stored command must survive");
    assert.match(reviewDiff.description, /審查/, "a zh-TW profile must read Traditional text");
    assert.doesNotMatch(reviewDiff.description, /审查/);
  });
});

test("the same stored value reads Simplified under a zh-CN profile", () => {
  withBrowser(storageWith("zh-CN", legacyStored), () => {
    const controller = controllerFor();
    controller.applyRegionalPreferences();
    assert.match(controller.currentSkillsPreferences().commands[0].description, /审查/);
  });
});

test("wording the user wrote is never overwritten", () => {
  const mine = "我自己改的說明，不要動它。";
  const stored = JSON.stringify({
    commands: [{ id: "review-diff", name: "/review-diff", description: mine, prompt: "我自己的提示。", enabled: true }],
    mcpServers: [],
  });

  withBrowser(storageWith("zh-TW", stored), () => {
    const controller = controllerFor();
    controller.applyRegionalPreferences();
    const commands = controller.currentSkillsPreferences().commands;
    assert.equal(commands[0].description, mine, "an edited description is user data, not a seed");
    assert.equal(commands[0].prompt, "我自己的提示。");
  });
});

test("switching language re-resolves the text instead of waiting for a reload", () => {
  withBrowser(storageWith("zh-CN", legacyStored), () => {
    const state = {};
    const controller = controllerFor(state);
    controller.applyRegionalPreferences();
    assert.match(controller.currentSkillsPreferences().commands[0].description, /审查/);

    // This is the whole user-visible fix: picking a language in the selector must
    // change the slash palette now. Without dropping the cached copy the previous
    // wording survives until a full reload, which is how the bug was reported.
    controller.saveRegionalPreferences({ locale: "zh-TW", timezone: "auto" });
    assert.match(controller.currentSkillsPreferences().commands[0].description, /審查/);
  });
});
