import assert from "node:assert/strict";
import test from "node:test";

import {
  defaultIMGatewayPrefs,
  defaultNotificationPrefs,
  defaultSearchPrefs,
  imGatewayPrefsKey,
  notificationPrefsKey,
  searchPrefsKey,
} from "./preferences-data.mjs";
import { createSettingsPreferencesController } from "./settings-preferences.mjs";

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

function controllerFor(state = {}, options = {}) {
  return createSettingsPreferencesController({
    state,
    loadChatDrafts: () => ({}),
    loadPromptHistory: () => [],
    loadTerminalPreferences: () => ({}),
    normalizeChatDrafts: (value) => value,
    normalizePromptHistory: (value) => value,
    normalizeRecentDirectories: (value) => value,
    normalizeTerminalPreferences: (value) => value,
    ...options,
  });
}

test("search preferences coerce unknown providers and result counts, and strip URLs from domain lists", () => {
  // A typo'd provider or a pasted https://host/path used to survive into the
  // request, so the search tool called an endpoint the backend would reject.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    const normalized = controller.normalizeSearchPreferences({
      provider: "bing",
      maxResults: 7,
      allowedDomains: "https://github.com/org/repo, example.com/path\n,foo.com",
      blockedDomains: ["not", "a", "string"],
      enabled: "yes",
    });
    assert.equal(normalized.provider, defaultSearchPrefs.provider);
    assert.equal(normalized.maxResults, defaultSearchPrefs.maxResults);
    assert.equal(normalized.allowedDomains, "github.com\nexample.com\nfoo.com");
    assert.equal(normalized.enabled, true);
    assert.equal(controller.normalizeSearchPreferences({ maxResults: 20 }).maxResults, 20);
    assert.equal(controller.searchProviderLabel("tavily"), "Tavily");
    assert.equal(controller.searchProviderLabel("nope"), "nope");
  });
});

test("IM gateway preferences reject unknown channels and payload sizes", () => {
  // An unrecognized channel left the form looking configured while the server
  // ignored the endpoint, so outbound events vanished with no error.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    const normalized = controller.normalizeIMGatewayPreferences({
      channel: "whatsapp",
      maxPayloadKB: 12,
      endpointUrl: `  https://hooks.example/a  `,
      allowedOrigins: "a.com, b.com\nc.com",
    });
    assert.equal(normalized.channel, defaultIMGatewayPrefs.channel);
    assert.equal(normalized.maxPayloadKB, defaultIMGatewayPrefs.maxPayloadKB);
    assert.equal(normalized.endpointUrl, "https://hooks.example/a");
    assert.equal(normalized.allowedOrigins, "a.com\nb.com\nc.com");
    assert.equal(controller.normalizeIMGatewayPreferences({ maxPayloadKB: 256 }).maxPayloadKB, 256);
    assert.equal(controller.imGatewayChannelLabel("discord"), "Discord");
    assert.equal(controller.imGatewayChannelLabel("missing"), "missing");
  });
});

test("notification duration and hold-until-dismissed keep errors on screen until closed", () => {
  // An error toast that expired with the info ones is how a failed run could
  // finish with nobody noticing. hold=0 is the "wait for dismiss" sentinel.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({ notifications: { ...defaultNotificationPrefs } });
    assert.equal(controller.notificationToastDuration("info"), 3800);
    assert.equal(controller.notificationToastDuration("error"), 7000);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, duration: "short" });
    assert.equal(controller.notificationToastDuration("info"), 2400);
    assert.equal(controller.notificationToastDuration("error"), 4500);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, duration: "long" });
    assert.equal(controller.notificationToastDuration("info"), 7000);
    assert.equal(controller.notificationToastDuration("error"), 11000);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, duration: "nope" });
    assert.equal(controller.currentNotificationPreferences().duration, defaultNotificationPrefs.duration);

    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, errorToastsPersist: true });
    assert.equal(controller.notificationToastHoldsUntilDismissed("error"), true);
    assert.equal(controller.notificationToastHoldsUntilDismissed("info"), false);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, errorToastsPersist: false });
    assert.equal(controller.notificationToastHoldsUntilDismissed("error"), false);
  });
});

test("toast variant gates honor the master switch and treat warn as warning", () => {
  // showToast("warn") used the info toggle, so turning off warnings still let
  // continuation-blocked cards through. The master switch must mute every variant.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    controller.saveNotificationPreferences({
      ...defaultNotificationPrefs,
      toastEnabled: true,
      infoToasts: false,
      successToasts: true,
      warningToasts: false,
      errorToasts: true,
    });
    assert.equal(controller.notificationVariantEnabled("info"), false);
    assert.equal(controller.notificationVariantEnabled("success"), true);
    assert.equal(controller.notificationVariantEnabled("warn"), false);
    assert.equal(controller.notificationVariantEnabled("warning"), false);
    assert.equal(controller.notificationVariantEnabled("error"), true);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, toastEnabled: false, errorToasts: true });
    assert.equal(controller.notificationVariantEnabled("error"), false);
  });
});

test("skill commands gain a slash, keep user wording, and recover seed keys from the id", () => {
  // A command stored as "review-diff" never appeared in the slash palette. User
  // edits must not be overwritten just because the id still matches a seed.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    const missingSlash = controller.normalizeSkillCommand({
      id: "custom-1",
      name: "ship",
      description: "Ship it",
      prompt: "please ship",
    });
    assert.equal(missingSlash.name, "/ship");
    assert.equal(missingSlash.description, "Ship it");

    const customSeedId = controller.normalizeSkillCommand({
      id: "review-diff",
      name: "/review-diff",
      description: "My own review notes",
      prompt: "Look at this specifically",
    });
    assert.equal(customSeedId.description, "My own review notes");
    assert.equal(customSeedId.prompt, "Look at this specifically");
    assert.equal(customSeedId.descriptionKey, "workspace.chat.seedReviewDiffDescription");
    assert.equal(customSeedId.promptKey, "workspace.chat.seedReviewDiffPrompt");

    const longName = controller.normalizeSkillCommand({ name: `/${"n".repeat(80)}`, description: "d", prompt: "p" });
    assert.equal(longName.name.length, 40);
  });
});

test("MCP server transport falls back to stdio and stays disabled until opted in", () => {
  // An unknown transport used to be stored as-is and then fail at connect. New
  // rows that defaulted to enabled started a process the user had not reviewed.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    const normalized = controller.normalizeMCPServer({
      id: "mcp-1",
      name: "  files  ",
      command: "npx demo",
      transport: "udp",
    });
    assert.equal(normalized.name, "files");
    assert.equal(normalized.transport, "stdio");
    assert.equal(normalized.enabled, false);
    assert.equal(controller.normalizeMCPServer({ transport: "sse", enabled: true }).transport, "sse");
  });
});

test("Git env example quotes names safely and reports when identity is missing", () => {
  // A display name containing `"` used to break the copied shell snippet, and an
  // empty profile copied a blank command that looked like it had succeeded.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    const snippet = controller.profileGitEnvExample({ gitName: `Ada "Coder"`, gitEmail: "ada@example.com" });
    assert.match(snippet, /git config --global user\.name "Ada \\"Coder\\""/);
    assert.match(snippet, /git config --global user\.email "ada@example.com"/);
    const missing = controller.profileGitEnvExample({ gitName: "", gitEmail: "" });
    assert.match(missing, /^# /);
    assert.doesNotMatch(missing, /git config/);
  });
});

test("sound and system-notification gates stay off unless the matching preference is on", () => {
  // play("error") used to fire whenever soundEnabled was true, even with
  // soundOnError unchecked. System notifications must not prompt until opted in.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    controller.saveNotificationPreferences({
      ...defaultNotificationPrefs,
      soundEnabled: true,
      soundOnDone: false,
      soundOnError: true,
      systemNotifications: false,
    });
    assert.equal(controller.notificationSoundEnabled("success"), false);
    assert.equal(controller.notificationSoundEnabled("error"), true);
    assert.equal(controller.systemNotificationsEnabled(), false);
    controller.saveNotificationPreferences({ ...defaultNotificationPrefs, soundEnabled: false, soundOnError: true });
    assert.equal(controller.notificationSoundEnabled("error"), false);
  });
});

test("saving search preferences writes the normalized shape, not the raw form values", () => {
  // The panel used to persist maxResults="7" as a string and a provider the
  // enum does not know, which then rendered as a blank <select>.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    controller.saveSearchPreferences({ provider: "bing", maxResults: "7", allowedDomains: "https://a.com/x" });
    const stored = JSON.parse(globalThis.localStorage.getItem(searchPrefsKey));
    assert.equal(stored.provider, "duckduckgo");
    assert.equal(stored.maxResults, 5);
    assert.equal(stored.allowedDomains, "a.com");
    assert.equal(controller.currentSearchPreferences().provider, "duckduckgo");
  });
});

test("event-log preference is the switch shouldLogAgentEvents actually reads", () => {
  // The terminal dumped every agent event regardless of the Appearance toggle,
  // which is how a long run buried the few lines the user had wanted to keep.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    controller.saveAppearancePreferences({ ...controller.currentAppearancePreferences(), showEventLog: false });
    assert.equal(controller.shouldLogAgentEvents(), false);
    controller.saveAppearancePreferences({ ...controller.currentAppearancePreferences(), showEventLog: true });
    assert.equal(controller.shouldLogAgentEvents(), true);
  });
});

test("IM and notification saves round-trip through localStorage under the Autoto keys", () => {
  // A key typo here would silently create a second store the backup exporter
  // does not know about, so a restore would look successful and drop the values.
  withBrowser(new MemoryStorage(), () => {
    const controller = controllerFor({});
    controller.saveIMGatewayPreferences({ channel: "slack", enabled: true });
    controller.saveNotificationPreferences({ duration: "long", toastEnabled: false });
    assert.equal(JSON.parse(globalThis.localStorage.getItem(imGatewayPrefsKey)).channel, "slack");
    assert.equal(JSON.parse(globalThis.localStorage.getItem(notificationPrefsKey)).duration, "long");
    assert.equal(JSON.parse(globalThis.localStorage.getItem(notificationPrefsKey)).toastEnabled, false);
  });
});
