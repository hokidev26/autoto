import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";

import {
  appearancePrefsKey,
  appearanceStyleVersion,
  appearanceThemePresets,
  defaultAppearancePrefs,
  defaultPrimaryModePreference,
  defaultRegionalPrefs,
  defaultSearchPrefs,
  legacyLocalPreferenceBackupKind,
  localPreferenceBackupKeys,
  localPreferenceBackupKind,
  localPreferenceBackupVersion,
  localPreferenceKeys,
  modelVisibilityPrefsKey,
  normalizeImportedRegionalPreferences,
  normalizePrimaryModePreference,
  normalizeRegionalPreferences,
  preferredModelKey,
  primaryModePrefsKey,
  profilePrefsKey,
  regionalPrefsKey,
} from "./preferences-data.mjs";
import { normalizeAccountProfile, normalizeModelVisibility } from "./account-preferences.mjs";
import { createSettingsPreferencesController } from "./settings-preferences.mjs";
import { createLocalPreferencesSettingsController } from "./local-preferences-settings.mjs";

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

function replaceGlobal(name, value) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, name);
  Object.defineProperty(globalThis, name, { configurable: true, writable: true, value });
  return () => {
    if (descriptor) Object.defineProperty(globalThis, name, descriptor);
    else delete globalThis[name];
  };
}

function withBrowserStorage(storage, callback) {
  const body = {
    dataset: {},
    classList: {
      values: new Set(),
      toggle(name, enabled) {
        if (enabled) this.values.add(name);
        else this.values.delete(name);
      },
      contains(name) {
        return this.values.has(name);
      },
    },
  };
  const restoreStorage = replaceGlobal("localStorage", storage);
  const restoreDocument = replaceGlobal("document", {
    title: "",
    body,
    getElementById() {
      return null;
    },
  });
  const restore = () => {
    restoreDocument();
    restoreStorage();
  };
  try {
    const result = callback(body);
    if (result && typeof result.then === "function") return result.finally(restore);
    restore();
    return result;
  } catch (error) {
    restore();
    throw error;
  }
}

function createController(state = {}, options = {}) {
  return createSettingsPreferencesController({
    state,
    ...options,
    loadChatDrafts: () => ({}),
    loadPromptHistory: () => [],
    loadTerminalPreferences: () => ({}),
    normalizeChatDrafts: (value) => value,
    normalizePromptHistory: (value) => value,
    normalizeRecentDirectories: (value) => value,
    normalizeTerminalPreferences: (value) => value,
  });
}

test("legacy relay protocol preference stays outside writable migration and backup sets", () => {
  assert.equal(localPreferenceKeys.includes("autoto.relayProtocol"), false);
  assert.equal(localPreferenceBackupKeys.some((entry) => entry.key === "autoto.relayProtocol"), false);
});

test("settings lazily migrates legacy CodeHarbor preferences without deleting them", () => {
  const legacyKey = "codeharbor.profile";
  const legacyValue = JSON.stringify({ displayName: "Legacy user" });
  const storage = new MemoryStorage([[legacyKey, legacyValue]]);

  withBrowserStorage(storage, () => {
    const profile = createController().loadProfilePreferences();

    assert.equal(profile.displayName, "Legacy user");
    assert.equal(profile.avatarInitials, "AT");
    assert.equal(storage.getItem(profilePrefsKey), legacyValue);
    assert.equal(storage.getItem(legacyKey), legacyValue);
  });
});

test("canonical Autoto preference takes priority over its legacy value", () => {
  const legacyKey = "codeharbor.profile";
  const canonicalValue = JSON.stringify({ displayName: "Autoto user" });
  const legacyValue = JSON.stringify({ displayName: "Legacy user" });
  const storage = new MemoryStorage([
    [profilePrefsKey, canonicalValue],
    [legacyKey, legacyValue],
  ]);

  withBrowserStorage(storage, () => {
    const profile = createController().loadProfilePreferences();

    assert.equal(profile.displayName, "Autoto user");
    assert.equal(storage.getItem(profilePrefsKey), canonicalValue);
    assert.equal(storage.getItem(legacyKey), legacyValue);
  });
});

test("primary mode migrates from the legacy CodeHarbor key", () => {
  const legacyKey = "codeharbor.ui.primaryMode";
  const storage = new MemoryStorage([[legacyKey, "workbench"]]);

  withBrowserStorage(storage, () => {
    const controller = createController();

    assert.equal(controller.loadPrimaryModePreference(), "workbench");
    assert.equal(controller.currentPrimaryModePreference(), "workbench");
    assert.equal(storage.getItem(primaryModePrefsKey), "workbench");
    assert.equal(storage.getItem(legacyKey), "workbench");
  });
});

test("primary mode backup normalizes invalid stored values", () => {
  const storage = new MemoryStorage([[primaryModePrefsKey, "kanban"]]);

  withBrowserStorage(storage, () => {
    const backup = createController().createLocalPreferencesBackup();

    assert.equal(backup.preferences[primaryModePrefsKey], defaultPrimaryModePreference);
  });
});

test("backup export uses Autoto format and import accepts legacy CodeHarbor format", () => {
  const storage = new MemoryStorage();

  withBrowserStorage(storage, () => {
    const controller = createController({ settings: { version: "1.2.3" } });
    const imported = controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: legacyLocalPreferenceBackupKind,
      version: 1,
      preferences: {
        "codeharbor.profile": { displayName: "Imported user", avatarInitials: "ch" },
      },
    }));

    assert.equal(imported, 1);
    assert.deepEqual(JSON.parse(storage.getItem(profilePrefsKey)), {
      displayName: "Imported user",
      roleLabel: "Local developer",
      avatarInitials: "CH",
      avatarDataUrl: "",
      gitName: "",
      gitEmail: "",
      workspaceLabel: "Autoto Local",
    });

    const backup = controller.createLocalPreferencesBackup();
    assert.equal(backup.kind, localPreferenceBackupKind);
    assert.ok(Object.keys(backup.preferences).every((key) => key.startsWith("autoto.")));
  });
});

test("profile settings expose compressed avatar upload controls and a safe preview", () => {
  const controller = createLocalPreferencesSettingsController({
    currentProfilePreferences: () => normalizeAccountProfile({
      displayName: "Photo user",
      avatarInitials: "PU",
      avatarDataUrl: "data:image/jpeg;base64,AAAA",
    }),
    profileDisplayName: () => "Photo user",
  });
  const html = controller.renderProfileSettingsContent();
  assert.match(html, /id="profileAvatarFile"[^>]*type="file"/);
  assert.match(html, /accept="image\/jpeg,image\/png,image\/webp,image\/gif"/);
  assert.match(html, /id="chooseProfileAvatarBtn"/);
  assert.match(html, /id="removeProfileAvatarBtn"/);
  assert.match(html, /class="profile-avatar-image" src="data:image\/jpeg;base64,AAAA"/);
});

test("v2 backup exports account snapshot and imports both v1 and v2 account fields through the service", async () => {
  const storage = new MemoryStorage([[appearancePrefsKey, JSON.stringify({ styleVersion: 3, themePreset: "dark" })]]);
  const imported = [];
  let accountSnapshot = {
    scopeKey: "user-a",
    profile: normalizeAccountProfile({ displayName: "Server user", avatarInitials: "su" }),
    preferredModel: "openai:gpt-server",
    modelVisibility: normalizeModelVisibility({ hiddenModels: { "openai:hidden": true } }),
    revision: 4,
    localStorageImportVersion: 1,
    updatedAt: "2026-07-18T00:00:00Z",
  };
  const accountPreferences = {
    getSnapshot: () => structuredClone(accountSnapshot),
    getProfile: () => structuredClone(accountSnapshot.profile),
    normalizeProfile: normalizeAccountProfile,
    normalizeModelVisibility,
    setProfile: async (profile) => { accountSnapshot.profile = normalizeAccountProfile(profile); },
    getStatus: () => "synced",
    importPreferences: async (patch) => {
      imported.push(structuredClone(patch));
      accountSnapshot = { ...accountSnapshot, ...patch };
      return Object.keys(patch).length;
    },
  };

  await withBrowserStorage(storage, async () => {
    const controller = createController({}, { accountPreferences });
    const backup = controller.createLocalPreferencesBackup();
    assert.equal(localPreferenceBackupVersion, 2);
    assert.equal(backup.version, 2);
    assert.deepEqual(backup.accountPreferences, {
      profile: accountSnapshot.profile,
      preferredModel: "openai:gpt-server",
      modelVisibility: { hiddenModels: { "openai:hidden": true }, showUnconfiguredProviders: false },
    });
    assert.equal(Object.hasOwn(backup.preferences, profilePrefsKey), false);
    assert.equal(Object.hasOwn(backup.preferences, preferredModelKey), false);
    assert.equal(Object.hasOwn(backup.preferences, modelVisibilityPrefsKey), false);

    const v1Count = await controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: legacyLocalPreferenceBackupKind,
      version: 1,
      preferences: {
        "codeharbor.profile": { displayName: "V1 user" },
        "codeharbor.preferredModel": "anthropic:v1",
        "codeharbor.modelVisibility": { showUnconfiguredProviders: true },
      },
    }));
    assert.equal(v1Count, 3);
    assert.equal(imported.at(-1).profile.displayName, "V1 user");
    assert.equal(imported.at(-1).preferredModel, "anthropic:v1");
    assert.equal(imported.at(-1).modelVisibility.showUnconfiguredProviders, true);

    const v2Count = await controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: localPreferenceBackupKind,
      version: 2,
      preferences: {},
      accountPreferences: {
        profile: { displayName: "V2 user" },
        preferredModel: "openai:v2",
        modelVisibility: { hiddenModels: { "openai:v2": true } },
      },
    }));
    assert.equal(v2Count, 3);
    assert.equal(imported.at(-1).profile.displayName, "V2 user");
    assert.equal(imported.at(-1).preferredModel, "openai:v2");
    assert.deepEqual(imported.at(-1).modelVisibility.hiddenModels, { "openai:v2": true });
  });
});

test("primary mode import reloads state and reapplies the injected UI callback", () => {
  const storage = new MemoryStorage();
  const appliedModes = [];

  withBrowserStorage(storage, () => {
    const state = {};
    const controller = createController(state, { applyPrimaryMode: (mode) => appliedModes.push(mode) });
    const imported = controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: legacyLocalPreferenceBackupKind,
      version: 1,
      preferences: {
        "codeharbor.ui.primaryMode": "workbench",
      },
    }));

    assert.equal(imported, 1);
    assert.equal(storage.getItem(primaryModePrefsKey), "workbench");
    assert.equal(state.primaryModePreference, "workbench");
    assert.equal(controller.currentPrimaryModePreference(), "workbench");
    assert.equal(appliedModes.at(-1), "workbench");
  });
});

test("primary mode rejects invalid values as conversation", () => {
  const storage = new MemoryStorage([[primaryModePrefsKey, "kanban"]]);
  const appliedModes = [];

  withBrowserStorage(storage, () => {
    const controller = createController({}, { applyPrimaryMode: (mode) => appliedModes.push(mode) });

    assert.equal(normalizePrimaryModePreference("workbench"), "workbench");
    assert.equal(normalizePrimaryModePreference("schedules"), "schedules");
    assert.equal(normalizePrimaryModePreference("CONVERSATION"), defaultPrimaryModePreference);
    assert.equal(controller.loadPrimaryModePreference(), defaultPrimaryModePreference);
    assert.equal(controller.setPrimaryModePreference("schedules"), "schedules");
    assert.equal(storage.getItem(primaryModePrefsKey), "schedules");
    assert.equal(controller.setPrimaryModePreference("invalid"), defaultPrimaryModePreference);
    assert.equal(storage.getItem(primaryModePrefsKey), defaultPrimaryModePreference);
    assert.equal(appliedModes.at(-1), defaultPrimaryModePreference);

    controller.restoreLocalPreferencesBackup(JSON.stringify({
      preferences: { [primaryModePrefsKey]: "invalid" },
    }));
    assert.equal(controller.currentPrimaryModePreference(), defaultPrimaryModePreference);
    assert.equal(storage.getItem(primaryModePrefsKey), defaultPrimaryModePreference);
  });
});

test("regional preferences default to auto and import legacy field names", () => {
  const storage = new MemoryStorage();

  withBrowserStorage(storage, () => {
    const controller = createController({ settings: { version: "1.2.3" } });
    assert.deepEqual(controller.currentRegionalPreferences(), defaultRegionalPrefs);
    assert.deepEqual(normalizeRegionalPreferences({ locale: "invalid", timezone: "Mars/Olympus" }), {
      locale: "auto",
      timezone: "auto",
    });
    assert.deepEqual(normalizeImportedRegionalPreferences({
      regionalPreferences: { language: "zh", timeZone: "Asia/Shanghai" },
    }), {
      locale: "zh-CN",
      timezone: "Asia/Shanghai",
    });
    controller.saveRegionalPreferences({ locale: "zh-Hant-HK", timezone: "Asia/Taipei" });
    assert.deepEqual(JSON.parse(storage.getItem(regionalPrefsKey)), {
      locale: "zh-TW",
      timezone: "Asia/Taipei",
    });

    const imported = controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: legacyLocalPreferenceBackupKind,
      version: 1,
      preferences: {
        "codeharbor.regional": { language: "en", timeZone: "UTC" },
      },
    }));

    assert.equal(imported, 1);
    assert.deepEqual(JSON.parse(storage.getItem(regionalPrefsKey)), {
      locale: "en-US",
      timezone: "UTC",
    });
    assert.deepEqual(controller.currentRegionalPreferences(), {
      locale: "en-US",
      timezone: "UTC",
    });
    assert.deepEqual(controller.createLocalPreferencesBackup().preferences[regionalPrefsKey], {
      locale: "en-US",
      timezone: "UTC",
    });
  });
});

test("appearance presets default to light and migrate version 2 and unversioned preferences", () => {
  assert.equal(appearanceStyleVersion, 5);
  assert.deepEqual(appearanceThemePresets, ["light", "dark", "cyber", "cream", "apple"]);
  assert.deepEqual(defaultAppearancePrefs, {
    styleVersion: 5,
    themeRef: { kind: "preset", id: "light" },
    themeSchemeRefs: { light: { kind: "preset", id: "light" } },
    themePreset: "light",
    theme: "light",
    density: "comfortable",
    backgroundMode: "theme",
    backgroundUrl: "",
    backgroundDim: 18,
    backgroundPositionX: 50,
    backgroundPositionY: 50,
    terminalDefaultOpen: false,
    showEventLog: true,
  });

  withBrowserStorage(new MemoryStorage([[appearancePrefsKey, JSON.stringify({
    styleVersion: 2,
    theme: "dark",
    density: "compact",
  })]]), () => {
    const appearance = createController().loadAppearancePreferences();
    assert.deepEqual(appearance.themeRef, { kind: "preset", id: "dark" });
    assert.equal(appearance.themePreset, "dark");
    assert.equal(appearance.theme, "dark");
  });

  withBrowserStorage(new MemoryStorage([[appearancePrefsKey, JSON.stringify({ theme: "dark" })]]), () => {
    const appearance = createController().loadAppearancePreferences();
    assert.deepEqual(appearance.themeRef, { kind: "preset", id: "light" });
    assert.equal(appearance.themePreset, "light");
    assert.equal(appearance.theme, "light");
  });
});

test("appearance preset derives the palette, rejects unknown values, and updates body markers", () => {
  withBrowserStorage(new MemoryStorage([[appearancePrefsKey, JSON.stringify({
    styleVersion: 3,
    themePreset: "solar",
    theme: "dark",
  })]]), () => {
    const controller = createController();
    assert.deepEqual(controller.loadAppearancePreferences(), { ...defaultAppearancePrefs });
    assert.deepEqual(JSON.parse(localStorage.getItem(appearancePrefsKey)), { ...defaultAppearancePrefs });
  });

  withBrowserStorage(new MemoryStorage([[appearancePrefsKey, JSON.stringify({
    styleVersion: 3,
    themePreset: "cyber",
    theme: "light",
  })]]), (body) => {
    const controller = createController();
    assert.deepEqual(controller.normalizeAppearancePreferences({ styleVersion: 3, themePreset: "solar", theme: "dark" }), {
      ...defaultAppearancePrefs,
    });

    controller.applyAppearancePreferences();
    assert.equal(controller.currentAppearancePreferences().theme, "dark");
    assert.equal(body.dataset.themePreset, "cyber");
    assert.equal(body.classList.contains("theme-light"), true);
    assert.equal(body.classList.contains("theme-dark"), true);

    controller.setAppearancePreference("themePreset", "cream");
    assert.equal(body.dataset.themePreset, "cream");
    assert.equal(body.classList.contains("theme-dark"), false);
    assert.deepEqual(JSON.parse(localStorage.getItem(appearancePrefsKey)).themePreset, "cream");

    controller.setAppearancePreference("themePreset", "apple");
    assert.equal(body.dataset.themePreset, "apple");
    assert.equal(body.classList.contains("theme-dark"), false);
    assert.equal(JSON.parse(localStorage.getItem(appearancePrefsKey)).themePreset, "apple");
    assert.equal(JSON.parse(localStorage.getItem(appearancePrefsKey)).theme, "light");
  });
});

test("appearance package references retain revision and derive the color scheme", () => {
  withBrowserStorage(new MemoryStorage([[appearancePrefsKey, JSON.stringify({
    styleVersion: 4,
    themeRef: {
      kind: "package",
      id: "argentina-spain-final",
      revision: "rev-1",
      colorScheme: "dark",
    },
    themePreset: "dark",
  })]]), () => {
    const controller = createController();
    const appearance = controller.loadAppearancePreferences();
    assert.deepEqual(appearance.themeRef, {
      kind: "package",
      id: "argentina-spain-final",
      revision: "rev-1",
      colorScheme: "dark",
    });
    assert.equal(appearance.theme, "dark");
    assert.equal(appearance.themePreset, "dark");
  });
});

test("appearance backup retains the normalized theme preset", () => {
  withBrowserStorage(new MemoryStorage(), () => {
    const controller = createController();
    controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: localPreferenceBackupKind,
      version: 1,
      preferences: {
        [appearancePrefsKey]: { styleVersion: 3, themePreset: "apple", density: "compact" },
      },
    }));

    assert.deepEqual(JSON.parse(localStorage.getItem(appearancePrefsKey)), {
      styleVersion: 5,
      themeRef: { kind: "preset", id: "apple" },
      themeSchemeRefs: { light: { kind: "preset", id: "apple" } },
      themePreset: "apple",
      theme: "light",
      density: "compact",
      backgroundMode: "theme",
      backgroundUrl: "",
      backgroundDim: 18,
      backgroundPositionX: 50,
      backgroundPositionY: 50,
      terminalDefaultOpen: false,
      showEventLog: true,
    });
    assert.equal(controller.createLocalPreferencesBackup().preferences[appearancePrefsKey].themePreset, "apple");
  });
});

test("network search settings render one compact strategy form without summary cards", () => {
  const settings = createLocalPreferencesSettingsController({
    currentSearchPreferences: () => ({ ...defaultSearchPrefs, provider: "custom", customEndpoint: "https://search.example.test/api" }),
  });
  const markup = settings.renderNetworkSearchSettingsContent();

  assert.match(markup, /compact-settings-page network-search-page/);
  assert.match(markup, /id="searchSettingsForm" class="compact-settings-section-controls"/);
  assert.match(markup, /compact-settings-switch-list/);
  assert.match(markup, /compact-settings-grid two-column/);
  assert.match(markup, /id="searchCustomEndpoint"/);
  assert.doesNotMatch(markup, /settings-stat-grid|settings-stat-card|settings-hero-card|appearance-choice/);
});

test("appearance settings render a flat compact form with five accessible preset previews", async () => {
  const settings = createLocalPreferencesSettingsController({
    currentAppearancePreferences: () => ({ ...defaultAppearancePrefs, themePreset: "apple" }),
    currentRegionalPreferences: () => ({ locale: "en-US", timezone: "auto" }),
    backgroundManager: { snapshot: () => ({ background: { mode: "theme", url: "", dim: 18, positionX: 50, positionY: 50 } }) },
  });
  const markup = settings.renderAppearanceSettingsContent();
  const styles = await readStylesSource(new URL("../styles.css", import.meta.url));

  assert.match(markup, /compact-settings-page appearance-page/);
  assert.match(markup, /compact-settings-section/);
  assert.match(markup, /compact-settings-choice-grid four-column/);
  assert.doesNotMatch(markup, /settings-stat-grid|settings-stat-card|settings-hero-card/);
  assert.match(markup, /role="radiogroup" aria-label=/);
  for (const preset of ["light", "dark", "cyber", "cream", "apple"]) {
    assert.match(markup, new RegExp(`data-appearance-field="themePreset" data-appearance-value="${preset}"`));
    assert.match(markup, new RegExp(`theme-preset-preview-${preset}`));
  }
  assert.match(markup, /theme-preset-preview-apple[\s\S]*?aria-checked="true"|aria-checked="true"[\s\S]*?theme-preset-preview-apple/);
  assert.match(markup, /id="appearanceBackgroundFile"[^>]*accept="image\/jpeg,image\/png,image\/webp"/);
  assert.match(markup, /id="appearanceBackgroundMode"/);
  assert.match(markup, /id="appearanceBackgroundDim"[^>]*type="range"/);
  assert.match(markup, /id="appearanceBackgroundPositionX"[^>]*type="range"/);
  assert.match(markup, /id="appearanceBackgroundPositionY"[^>]*type="range"/);
  assert.match(styles, /data-theme-preset="cyber"[\s\S]*?--ws-primary: #a7ff32/);
  assert.match(styles, /data-theme-preset="cream"[\s\S]*?--ws-canvas: #fff9ee/);
  assert.match(styles, /data-theme-preset="apple"[\s\S]*?--ws-primary: #007aff/);
  assert.match(styles, /\.theme-preset-preview-cyber/);
  assert.match(styles, /\.theme-preset-preview-cream/);
  assert.match(styles, /\.theme-preset-preview-apple/);
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\) \{[\s\S]*?data-theme-preset="apple"/);
  assert.match(styles, /@media \(prefers-reduced-transparency: reduce\) \{[\s\S]*?data-theme-preset="apple"/);
  assert.match(styles, /@media \(prefers-contrast: more\), \(forced-colors: active\) \{[\s\S]*?data-theme-preset="apple"/);
  assert.match(styles, /@media \(max-width: 760px\) \{[\s\S]*?#settingsContentBody \.appearance-theme-grid \{ grid-template-columns: 1fr; \}/);
});

test("appearance settings keeps the original Unicode background filename visible", () => {
  const originalFilename = "夏日 壁纸 (最终) 🌄.png";
  const settings = createLocalPreferencesSettingsController({
    currentAppearancePreferences: () => ({ ...defaultAppearancePrefs, backgroundMode: "custom", backgroundUrl: "/appearance/backgrounds/example/background-upload.png" }),
    currentRegionalPreferences: () => ({ locale: "zh-CN", timezone: "auto" }),
    backgroundManager: { snapshot: () => ({ background: { mode: "custom", url: "/appearance/backgrounds/example/background-upload.png", filename: originalFilename, dim: 18, positionX: 50, positionY: 50 } }) },
  });

  const markup = settings.renderAppearanceSettingsContent();
  assert.ok(markup.includes(originalFilename));
  assert.match(markup, /class="appearance-background-status" role="status" aria-live="polite"/);
});

test("apple theme cache stamps reach the static entry and updated modules", async () => {
  const [html, app, appMain, i18n, localPreferences, settingsPreferences] = await Promise.all([
    readFile(new URL("../index.html", import.meta.url), "utf8"),
    readFile(new URL("../app.js", import.meta.url), "utf8"),
    readFile(new URL("./app-main.mjs", import.meta.url), "utf8"),
    readFile(new URL("./i18n.mjs", import.meta.url), "utf8"),
    readFile(new URL("./local-preferences-settings.mjs", import.meta.url), "utf8"),
    readFile(new URL("./settings-preferences.mjs", import.meta.url), "utf8"),
  ]);

  assert.equal((html.match(/apple-theme-1/g) || []).length, 2);
  assert.equal((app.match(/apple-theme-1/g) || []).length, 1);
  assert.match(appMain, /local-preferences-settings\.mjs\?v=[^"\n]*apple-theme-1/);
  assert.match(appMain, /settings-preferences\.mjs\?v=apple-theme-1/);
  assert.match(localPreferences, /i18n\.mjs\?v=apple-theme-1/);
  assert.match(localPreferences, /preferences-data\.mjs\?v=apple-theme-1/);
  assert.match(settingsPreferences, /preferences-data\.mjs\?v=apple-theme-1/);
  assert.equal((i18n.match(/messages-(?:en|zh-CN|zh-TW)\.mjs\?v=[^"\n]*apple-theme-1/g) || []).length, 3);
});

test("background upload cache stamps reach the static entry, upload modules, and locale catalogs", async () => {
  const [html, app, appMain, i18n, localPreferences] = await Promise.all([
    readFile(new URL("../index.html", import.meta.url), "utf8"),
    readFile(new URL("../app.js", import.meta.url), "utf8"),
    readFile(new URL("./app-main.mjs", import.meta.url), "utf8"),
    readFile(new URL("./i18n.mjs", import.meta.url), "utf8"),
    readFile(new URL("./local-preferences-settings.mjs", import.meta.url), "utf8"),
  ]);

  assert.match(html, /app\.js\?v=[^"\n]*background-upload-1/);
  assert.match(app, /i18n\.mjs\?v=[^"\n]*background-upload-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*background-upload-1/);
  assert.match(appMain, /theme-manager\.mjs\?v=[^"\n]*background-upload-1/);
  assert.match(appMain, /local-preferences-settings\.mjs\?v=[^"\n]*background-upload-1/);
  assert.match(appMain, /i18n\.mjs\?v=[^"\n]*background-upload-1/);
  assert.match(localPreferences, /i18n\.mjs\?v=[^"\n]*background-upload-1/);
  assert.equal((i18n.match(/messages-(?:en|zh-CN|zh-TW)\.mjs\?v=[^"\n]*background-upload-1/g) || []).length, 3);
});

test("global theme toggle switches scheme and is wired to the shared helper", async () => {
  const appMain = await readFile(new URL("./app-main.mjs", import.meta.url), "utf8");

  assert.match(appMain, /updateGlobalThemeToggle,/);
  // The button used to inline a preset ternary and call
  // setAppearancePreference("themePreset", ...), which overwrote themeRef and
  // threw the selected theme away on the very first toggle.
  assert.match(appMain, /globalThemeToggleBtn"\)\?\.addEventListener\("click", \(\) => \{\s*toggleAppearanceColorScheme\(\);/);
  assert.doesNotMatch(appMain, /themePreset === "cream"/);
});

test("toggling the color scheme restores the previously chosen theme", () => {
  withBrowserStorage(new MemoryStorage(), () => {
    const controller = createController();
    const current = () => controller.currentAppearancePreferences();

    controller.setAppearancePreference("themePreset", "cream");
    assert.equal(current().theme, "light");

    controller.toggleAppearanceColorScheme();
    assert.equal(current().theme, "dark");
    assert.deepEqual(current().themeRef, { kind: "preset", id: "dark" });

    // Toggling back returns to cream instead of collapsing to the plain light
    // preset, which is the selection loss users hit flipping dark mode on/off.
    controller.toggleAppearanceColorScheme();
    assert.deepEqual(current().themeRef, { kind: "preset", id: "cream" });
    assert.equal(current().themePreset, "cream");

    // Each scheme keeps its own slot, so a dark preset and a light preset can
    // both be remembered and alternate cleanly.
    controller.setAppearancePreference("themePreset", "cyber");
    controller.toggleAppearanceColorScheme();
    assert.equal(current().themePreset, "cream");
    controller.toggleAppearanceColorScheme();
    assert.equal(current().themePreset, "cyber");
  });
});

test("toggling the color scheme restores a package theme", () => {
  withBrowserStorage(new MemoryStorage(), () => {
    const controller = createController();
    const current = () => controller.currentAppearancePreferences();
    const pkg = { kind: "package", id: "midnight-ink", revision: "r7", colorScheme: "dark" };

    controller.setAppearancePreference("themeRef", pkg);
    assert.equal(current().theme, "dark");

    controller.toggleAppearanceColorScheme();
    assert.equal(current().theme, "light");
    assert.equal(current().themeRef.kind, "preset");

    controller.toggleAppearanceColorScheme();
    assert.deepEqual(current().themeRef, pkg);
    assert.equal(current().theme, "dark");
  });
});

test("runtime prefers the Autoto token and falls back to the CodeHarbor token", async () => {
  const requests = [];
  const restores = [
    replaceGlobal("location", new URL("https://local.example.test/")),
    replaceGlobal("fetch", async (path, options) => {
      requests.push({ path, options });
      return { ok: true, text: async () => "" };
    }),
  ];
  if (!globalThis.FormData) restores.push(replaceGlobal("FormData", class FormData {}));

  const restoreCanonicalWindow = replaceGlobal("window", {
    AUTOTO_LOCAL_TOKEN: "autoto-token",
    CODEHARBOR_LOCAL_TOKEN: "legacy-token",
  });
  try {
    const canonicalRuntime = await import(new URL("./runtime.mjs?compat=canonical", import.meta.url).href);
    assert.equal(new URL(canonicalRuntime.withLocalToken("/api/status"), "https://local.example.test").searchParams.get("token"), "autoto-token");
    const socketURL = new URL(canonicalRuntime.webSocketURL("/ws/agent?id=agent-1"));
    assert.equal(socketURL.protocol, "wss:");
    assert.equal(socketURL.searchParams.get("token"), null);

    const restoreLegacyWindow = replaceGlobal("window", { CODEHARBOR_LOCAL_TOKEN: "legacy-token" });
    try {
      const legacyRuntime = await import(new URL("./runtime.mjs?compat=legacy", import.meta.url).href);
      assert.equal(new URL(legacyRuntime.withLocalToken("/api/status"), "https://local.example.test").searchParams.get("token"), "legacy-token");
      await legacyRuntime.api("/api/status");
      assert.equal(requests.at(-1).options.headers["X-Autoto-Token"], "legacy-token");
      assert.equal(requests.at(-1).options.headers["X-CodeHarbor-Token"], undefined);
    } finally {
      restoreLegacyWindow();
    }
  } finally {
    restoreCanonicalWindow();
    restores.reverse().forEach((restore) => restore());
  }
});
