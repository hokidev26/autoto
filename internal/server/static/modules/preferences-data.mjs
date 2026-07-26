import { currentUILocale } from "./i18n.mjs";
import { defaultRegionalPreferences, normalizeRegionalPreferences } from "./locale-registry.mjs";
import { preferencesMessage } from "./messages-preferences.mjs";

const canonicalLocalPreferencePrefix = "autoto.";
const legacyLocalPreferencePrefix = "codeharbor.";

export const recentDirectoriesKey = "autoto.recentDirectories";
export const recentConversationsKey = "autoto.recentConversations";
export const preferredModelKey = "autoto.preferredModel";
export const modelVisibilityPrefsKey = "autoto.modelVisibility";
export const profilePrefsKey = "autoto.profile";
export const searchPrefsKey = "autoto.search";
export const imGatewayPrefsKey = "autoto.imGateway";
export const skillsPrefsKey = "autoto.skills";
export const notificationPrefsKey = "autoto.notifications";
export const appearancePrefsKey = "autoto.appearance";
export const terminalPrefsKey = "autoto.terminal";
export const regionalPrefsKey = "autoto.regional";
export const chatDraftsKey = "autoto.chatDrafts";
export const promptHistoryKey = "autoto.promptHistory";
export const primaryModePrefsKey = "autoto.ui.primaryMode";
export const defaultPrimaryModePreference = "conversation";
export const localPreferenceBackupKind = "autoto.local-preferences";
export const legacyLocalPreferenceBackupKind = "codeharbor.local-preferences";
export const localPreferenceBackupVersion = 2;
export const accountPreferenceStorageKeys = Object.freeze([
  profilePrefsKey,
  preferredModelKey,
  modelVisibilityPrefsKey,
]);

export const localPreferenceKeys = [
  recentDirectoriesKey,
  recentConversationsKey,
  preferredModelKey,
  modelVisibilityPrefsKey,
  profilePrefsKey,
  searchPrefsKey,
  imGatewayPrefsKey,
  skillsPrefsKey,
  notificationPrefsKey,
  appearancePrefsKey,
  terminalPrefsKey,
  regionalPrefsKey,
  chatDraftsKey,
  promptHistoryKey,
  primaryModePrefsKey,
];

export function legacyLocalPreferenceKey(key) {
  const canonicalKey = String(key || "");
  if (!canonicalKey.startsWith(canonicalLocalPreferencePrefix)) return "";
  return `${legacyLocalPreferencePrefix}${canonicalKey.slice(canonicalLocalPreferencePrefix.length)}`;
}

export function readLocalPreference(key, storage = globalThis.localStorage) {
  const canonicalKey = String(key || "");
  const currentValue = storage.getItem(canonicalKey);
  if (currentValue !== null) return currentValue;

  const legacyKey = legacyLocalPreferenceKey(canonicalKey);
  if (!legacyKey) return null;
  const legacyValue = storage.getItem(legacyKey);
  if (legacyValue === null) return null;

  try {
    storage.setItem(canonicalKey, legacyValue);
  } catch {}
  return legacyValue;
}

export function migrateLegacyLocalPreferences(storage = globalThis.localStorage) {
  localPreferenceKeys.forEach((key) => {
    try {
      readLocalPreference(key, storage);
    } catch {}
  });
}

export const localPreferenceBackupKeys = [
  { key: profilePrefsKey, labelKey: "profile", type: "json" },
  { key: searchPrefsKey, labelKey: "search", type: "json" },
  { key: imGatewayPrefsKey, labelKey: "imGateway", type: "json" },
  { key: skillsPrefsKey, labelKey: "skills", type: "json" },
  { key: notificationPrefsKey, labelKey: "notifications", type: "json" },
  { key: appearancePrefsKey, labelKey: "appearance", type: "json" },
  { key: terminalPrefsKey, labelKey: "terminal", type: "json" },
  { key: regionalPrefsKey, labelKey: "regional", type: "json" },
  { key: chatDraftsKey, labelKey: "chatDrafts", type: "json" },
  { key: promptHistoryKey, labelKey: "promptHistory", type: "json" },
  { key: recentDirectoriesKey, labelKey: "recentDirectories", type: "json" },
  { key: recentConversationsKey, labelKey: "recentConversations", type: "json" },
  { key: preferredModelKey, labelKey: "preferredModel", type: "string" },
  { key: modelVisibilityPrefsKey, labelKey: "modelVisibility", type: "json" },
  { key: primaryModePrefsKey, labelKey: "primaryMode", type: "string" },
].map((entry) => ({ ...entry, label: localPreferenceBackupLabel(entry) }));

export function localPreferenceBackupLabel(entry, locale = currentUILocale()) {
  return preferencesMessage(`backupLabels.${entry?.labelKey || ""}`, {}, locale);
}

export function normalizePrimaryModePreference(value) {
  const mode = String(value || "").trim();
  return ["workbench", "schedules"].includes(mode) ? mode : defaultPrimaryModePreference;
}

export const defaultProfilePrefs = {
  displayName: "",
  roleLabel: "Local developer",
  avatarInitials: "AT",
  avatarDataUrl: "",
  gitName: "",
  gitEmail: "",
  workspaceLabel: "Autoto Local",
};

export const defaultSearchPrefs = {
  enabled: false,
  provider: "duckduckgo",
  maxResults: 5,
  safeSearch: true,
  confirmBeforeSearch: true,
  preferGitHub: true,
  allowedDomains: "",
  blockedDomains: "",
  customEndpoint: "",
};

export const defaultIMGatewayPrefs = {
  enabled: false,
  channel: "webhook",
  inboundConfirm: true,
  requireSignature: true,
  redactSecrets: true,
  allowInboundMessages: true,
  notifyOnTaskDone: true,
  notifyOnErrors: true,
  notifyOnToolCalls: false,
  maxPayloadKB: 64,
  endpointUrl: "",
  allowedOrigins: "",
  blockedSenders: "",
};

export const defaultSkillsPrefs = {
  commands: [
    {
      id: "review-diff",
      name: "/review-diff",
      description: "审查当前工作区改动并给出风险提示。",
      prompt: "请审查当前工作区变更，重点关注正确性、测试覆盖、安全风险和用户可见行为。",
      enabled: true,
    },
    {
      id: "write-tests",
      name: "/write-tests",
      description: "为当前改动补充必要测试。",
      prompt: "请根据当前改动补充最小必要测试，并说明测试覆盖的行为。",
      enabled: true,
    },
  ],
  mcpServers: [],
  toolPolicy: {
    requireConfirmationForExec: true,
    requireConfirmationForWrites: false,
    allowReadOnlyByDefault: true,
    preferPlanForLargeTasks: true,
  },
};

export const defaultNotificationPrefs = {
  toastEnabled: true,
  infoToasts: true,
  successToasts: true,
  warningToasts: true,
  errorToasts: true,
  terminalNotices: true,
  duration: "normal",
};

export const appearanceStyleVersion = 5;
export const appearanceThemePresets = Object.freeze(["light", "dark", "cyber", "cream", "apple"]);
export const appearanceBackgroundModes = Object.freeze(["theme", "custom", "none"]);

export function normalizeAppearanceBackgroundMode(value) {
  const mode = String(value || "").trim().toLowerCase();
  return appearanceBackgroundModes.includes(mode) ? mode : "theme";
}

export function normalizeAppearanceBackgroundPosition(value, fallback = 50) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return Math.max(0, Math.min(100, Number(fallback) || 50));
  return Math.max(0, Math.min(100, Math.round(parsed)));
}

export function normalizeAppearanceBackgroundURL(value) {
  const url = String(value || "").trim();
  return /^\/appearance\/backgrounds\/[a-f0-9]{64}\/[A-Za-z0-9][A-Za-z0-9._-]{0,119}$/.test(url) ? url : "";
}

export function normalizeAppearanceBackground(value = {}) {
  const source = value?.background && typeof value.background === "object" ? value.background : value;
  return {
    mode: normalizeAppearanceBackgroundMode(source?.backgroundMode || source?.mode),
    url: normalizeAppearanceBackgroundURL(source?.backgroundUrl || source?.url),
    dim: Math.max(0, Math.min(75, Number.isFinite(Number(source?.backgroundDim ?? source?.dim)) ? Math.round(Number(source?.backgroundDim ?? source?.dim)) : 18)),
    positionX: normalizeAppearanceBackgroundPosition(source?.backgroundPositionX ?? source?.positionX),
    positionY: normalizeAppearanceBackgroundPosition(source?.backgroundPositionY ?? source?.positionY),
  };
}
export const appearanceThemePresetTheme = Object.freeze({
  light: "light",
  dark: "dark",
  cyber: "dark",
  cream: "light",
  apple: "light",
});

export function normalizeAppearanceThemePreset(value) {
  const preset = String(value || "").trim().toLowerCase();
  return appearanceThemePresets.includes(preset) ? preset : "";
}

export function appearanceThemeForPreset(preset) {
  return appearanceThemePresetTheme[normalizeAppearanceThemePreset(preset)] || "light";
}

export function normalizeAppearanceThemeRef(value, fallbackPreset = "light") {
  const fallback = normalizeAppearanceThemePreset(fallbackPreset) || "light";
  const kind = String(value?.kind || "").trim().toLowerCase();
  const id = String(value?.id || "").trim().toLowerCase();
  if (kind === "preset") {
    const preset = normalizeAppearanceThemePreset(id);
    return { kind: "preset", id: preset || fallback };
  }
  if (kind === "package" && /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id) && id.length <= 63) {
    const revision = String(value?.revision || "").trim().slice(0, 128);
    const colorScheme = value?.colorScheme === "dark" ? "dark" : "light";
    return { kind: "package", id, revision, colorScheme };
  }
  return { kind: "preset", id: fallback };
}

export function appearanceThemeForRef(themeRef, fallbackPreset = "light") {
  const normalized = normalizeAppearanceThemeRef(themeRef, fallbackPreset);
  return normalized.kind === "package"
    ? normalized.colorScheme
    : appearanceThemeForPreset(normalized.id);
}

// A preset is a whole palette rather than a base plus a scheme, so there is no
// dark "cream": the global light/dark button has to move to a different theme
// outright. Remembering the last theme picked for each scheme lets it come back
// to that theme instead of dropping the selection and collapsing every round
// trip to the plain light/dark presets.
export function normalizeAppearanceThemeSchemeRefs(value, activeRef = null) {
  const refs = {};
  for (const scheme of ["light", "dark"]) {
    const candidate = value?.[scheme];
    if (!candidate) continue;
    const normalized = normalizeAppearanceThemeRef(candidate, scheme);
    // Drop a slot whose remembered theme does not actually render in that
    // scheme; restoring it would toggle the shell to the scheme it just left.
    if (appearanceThemeForRef(normalized, scheme) === scheme) refs[scheme] = normalized;
  }
  if (activeRef) {
    const normalized = normalizeAppearanceThemeRef(activeRef, "light");
    refs[appearanceThemeForRef(normalized, "light")] = normalized;
  }
  return refs;
}

// Resolves the themeRef/themePreset pair the global light/dark button should
// move to. Pure so the restore behaviour is testable without a DOM.
export function appearanceThemeToggleTarget(prefs = {}) {
  const activeRef = normalizeAppearanceThemeRef(prefs.themeRef, prefs.themePreset);
  const target = appearanceThemeForRef(activeRef, prefs.themePreset) === "dark" ? "light" : "dark";
  const remembered = normalizeAppearanceThemeSchemeRefs(prefs.themeSchemeRefs)[target];
  const themeRef = remembered || { kind: "preset", id: target };
  return { themeRef, themePreset: themeRef.kind === "preset" ? themeRef.id : target };
}

export const defaultAppearancePrefs = {
  styleVersion: appearanceStyleVersion,
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
};

export const defaultTerminalPrefs = {
  clearOnReconnect: true,
  focusOnConnect: true,
  maxLines: 5000,
};

export const defaultRegionalPrefs = defaultRegionalPreferences;
export { normalizeRegionalPreferences };

export function normalizeImportedRegionalPreferences(value = {}) {
  const source = value?.regionalPreferences ?? value?.regional ?? value;
  return normalizeRegionalPreferences(source);
}
