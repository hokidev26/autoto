import { $ } from "./dom.mjs";
import { normalizeAvatarDataUrl } from "./profile-avatar.mjs";
import { applyDocumentLocale, applyStaticTranslations, currentUILocale, t } from "./i18n.mjs";
import { setRegionalPreferences } from "./locale-registry.mjs";
import {
  accountPreferenceStorageKeys,
  appearancePrefsKey,
  appearanceStyleVersion,
  appearanceThemeForRef,
  appearanceThemeToggleTarget,
  normalizeAppearanceBackground,
  normalizeAppearanceThemePreset,
  normalizeAppearanceThemeRef,
  normalizeAppearanceThemeSchemeRefs,
  chatDraftsKey,
  defaultAppearancePrefs,
  defaultIMGatewayPrefs,
  defaultNotificationPrefs,
  defaultProfilePrefs,
  defaultRegionalPrefs,
  defaultSearchPrefs,
  defaultSkillsPrefs,
  seedSkillCommandKeys,
  seedSkillCommandTexts,
  imGatewayPrefsKey,
  localPreferenceBackupKeys,
  localPreferenceBackupLabel,
  localPreferenceBackupKind,
  localPreferenceBackupVersion,
  modelVisibilityPrefsKey,
  notificationPrefsKey,
  normalizeImportedRegionalPreferences,
  normalizePrimaryModePreference,
  normalizeRegionalPreferences,
  preferredModelKey,
  primaryModePrefsKey,
  profilePrefsKey,
  promptHistoryKey,
  readLocalPreference,
  recentDirectoriesKey,
  regionalPrefsKey,
  searchPrefsKey,
  skillsPrefsKey,
  terminalPrefsKey,
} from "./preferences-data.mjs";
import { preferencesMessage } from "./messages-preferences.mjs";

export function createSettingsPreferencesController({
  state,
  accountPreferences,
  activeBackend,
  appendTerminal,
  applyPrimaryMode,
  applyThemePreference,
  applyBackgroundPreference,
  loadChatDrafts,
  loadPromptHistory,
  loadTerminalPreferences,
  normalizeChatDrafts,
  normalizePromptHistory,
  normalizeRecentDirectories,
  normalizeTerminalPreferences,
  refreshActiveSettingsPanel,
  renderModelOptions,
  renderRecentModalDirectories,
  showToast,
  toggleTerminal,
  trimTerminalOutput,
  updatePromptHistoryHint,
  updateSlashCommandPalette,
  updateGlobalThemeToggle,
} = {}) {
  const accountPreferenceStorageKeySet = new Set(accountPreferenceStorageKeys);

  function pt(key, params = {}) {
    return preferencesMessage(key, params, currentUILocale());
  }

  function loadProfilePreferences() {
    if (accountPreferences) return normalizeProfilePreferences(accountPreferences.getProfile());
    try {
      return normalizeProfilePreferences(JSON.parse(safeReadLocalPreference(profilePrefsKey) || "{}"));
    } catch {
      return normalizeProfilePreferences({});
    }
  }

  function normalizeProfilePreferences(value = {}) {
    if (accountPreferences?.normalizeProfile) return accountPreferences.normalizeProfile(value);
    const displayName = String(value.displayName || "").trim().slice(0, 80);
    const roleLabel = String(value.roleLabel || defaultProfilePrefs.roleLabel).trim().slice(0, 80) || defaultProfilePrefs.roleLabel;
    const avatarInitials = String(value.avatarInitials || defaultProfilePrefs.avatarInitials).trim().slice(0, 4).toUpperCase() || defaultProfilePrefs.avatarInitials;
    const avatarDataUrl = normalizeAvatarDataUrl(value.avatarDataUrl);
    const gitName = String(value.gitName || "").trim().slice(0, 120);
    const gitEmail = String(value.gitEmail || "").trim().slice(0, 160);
    const workspaceLabel = String(value.workspaceLabel || defaultProfilePrefs.workspaceLabel).trim().slice(0, 80) || defaultProfilePrefs.workspaceLabel;
    return { displayName, roleLabel, avatarInitials, avatarDataUrl, gitName, gitEmail, workspaceLabel };
  }

  function currentProfilePreferences() {
    if (accountPreferences) state.profile = normalizeProfilePreferences(accountPreferences.getProfile());
    else if (!state.profile) state.profile = loadProfilePreferences();
    return state.profile;
  }

  function saveProfilePreferences(next, { notify = false } = {}) {
    state.profile = normalizeProfilePreferences(next);
    const saving = accountPreferences
      ? accountPreferences.setProfile(state.profile)
      : Promise.resolve().then(() => {
        try {
          localStorage.setItem(profilePrefsKey, JSON.stringify(state.profile));
        } catch {}
      });
    applyProfilePreferences();
    if (state.activeSettingsPanel === "profile") refreshActiveSettingsPanel?.();
    if (notify) saving.then(() => {
      const syncStatus = accountPreferences?.getStatus?.();
      const key = syncStatus === "synced"
        ? "settings.profileSynced"
        : (syncStatus === "offline" || syncStatus === "unauthorized" ? "settings.profileOffline" : "settings.profilePending");
      showToast?.(pt(key), syncStatus === "synced" ? "success" : "warn", { force: true });
    });
    return saving;
  }

  function resetProfilePreferences() {
    return saveProfilePreferences({ ...defaultProfilePrefs }, { notify: true });
  }

  function applyProfilePreferences() {
    const profile = currentProfilePreferences();
    document.title = profile.workspaceLabel && profile.workspaceLabel !== "Autoto Local"
      ? `${profile.workspaceLabel} · Autoto`
      : "Autoto";
    const displayName = profileDisplayName();
    const avatar = $("sidebarAvatar");
    const accountName = $("sidebarAccountName");
    const menuName = $("sidebarMenuProfileName");
    const menuMeta = $("sidebarMenuProfileMeta");
    const mobileAvatar = $("mobileSidebarAvatar");
    const mobileName = $("mobileSidebarAccountName");
    [avatar, mobileAvatar].forEach((node) => applyAvatarNode(node, profile));
    if (accountName) accountName.textContent = displayName;
    if (menuName) menuName.textContent = displayName;
    if (menuMeta) menuMeta.textContent = profile.roleLabel || pt("settings.localWorkspace");
    if (mobileName) mobileName.textContent = displayName;
    updateSidebarAccountSummary();
  }

  function applyAvatarNode(node, profile) {
    if (!node) return;
    const dataUrl = normalizeAvatarDataUrl(profile?.avatarDataUrl);
    if (dataUrl && typeof globalThis.document?.createElement === "function") {
      const image = globalThis.document.createElement("img");
      image.src = dataUrl;
      image.alt = "";
      image.className = "profile-avatar-image";
      image.setAttribute("aria-hidden", "true");
      node.replaceChildren?.(image);
      if (!node.replaceChildren) node.innerHTML = "";
      if (!node.replaceChildren) node.appendChild?.(image);
      return;
    }
    node.textContent = profile?.avatarInitials || defaultProfilePrefs.avatarInitials;
  }

  function profileDisplayName() {
    const profile = currentProfilePreferences();
    return profile.displayName || profile.workspaceLabel || "Autoto User";
  }

  function updateSidebarAccountSummary() {
    const version = String(state.settings?.version || "0.1.0-dev").replace(/^v/i, "");
    const backend = activeBackend?.();
    const meta = $("sidebarAccountMeta");
    if (meta) meta.textContent = `v${version} · ${backend?.name || pt("settings.localBackend")}`;
    const mobileVersionChip = $("mobileVersionChip");
    if (mobileVersionChip) mobileVersionChip.textContent = pt("settings.updateVersion", { version });
    const mobileDrawerVersionChip = $("mobileDrawerVersionChip");
    if (mobileDrawerVersionChip) mobileDrawerVersionChip.textContent = pt("settings.updateVersion", { version });
    const mobileDrawerVersionText = $("mobileDrawerVersionText");
    if (mobileDrawerVersionText) mobileDrawerVersionText.textContent = `v${version}`;
    const mobileSidebarMeta = $("mobileSidebarAccountMeta");
    if (mobileSidebarMeta) mobileSidebarMeta.textContent = `v${version} · ${backend?.name || pt("settings.localBackend")}`;
  }

  function profileGitEnvExample(profile = currentProfilePreferences()) {
    const rows = [];
    if (profile.gitName) rows.push(`git config --global user.name "${profile.gitName.replace(/"/g, "\\\"")}"`);
    if (profile.gitEmail) rows.push(`git config --global user.email "${profile.gitEmail.replace(/"/g, "\\\"")}"`);
    return rows.join("\n") || pt("settings.gitIdentityMissing");
  }

  function loadRegionalPreferences() {
    try {
      return normalizeRegionalPreferences(JSON.parse(safeReadLocalPreference(regionalPrefsKey) || "{}"));
    } catch {
      return normalizeRegionalPreferences(defaultRegionalPrefs);
    }
  }

  function currentRegionalPreferences() {
    if (!state.regionalPrefs) state.regionalPrefs = loadRegionalPreferences();
    return state.regionalPrefs;
  }

  function applyRegionalPreferences() {
    state.regionalPrefs = setRegionalPreferences(currentRegionalPreferences());
    applyDocumentLocale(state.regionalPrefs.locale);
    applyStaticTranslations();
    // Seed command text is resolved when preferences are normalised, so the cached
    // copy still holds the previous language. Dropping it makes the next read
    // re-resolve; without this the slash palette keeps the old wording until reload.
    state.skillsPrefs = null;
    return state.regionalPrefs;
  }

  function saveRegionalPreferences(next, { notify = false } = {}) {
    state.regionalPrefs = normalizeRegionalPreferences(next);
    try {
      localStorage.setItem(regionalPrefsKey, JSON.stringify(state.regionalPrefs));
    } catch {}
    applyRegionalPreferences();
    if (["regional", "appearance"].includes(state.activeSettingsPanel)) refreshActiveSettingsPanel?.();
    if (notify) showToast?.(pt("settings.appearanceSaved"), "success", { force: true });
  }

  function resetRegionalPreferences() {
    saveRegionalPreferences(defaultRegionalPrefs, { notify: true });
  }

  function loadSearchPreferences() {
    try {
      return normalizeSearchPreferences(JSON.parse(safeReadLocalPreference(searchPrefsKey) || "{}"));
    } catch {
      return normalizeSearchPreferences({});
    }
  }

  function normalizeSearchPreferences(value = {}) {
    const provider = ["duckduckgo", "brave", "tavily", "searxng", "custom"].includes(value.provider)
      ? value.provider
      : defaultSearchPrefs.provider;
    const maxResults = Number(value.maxResults || defaultSearchPrefs.maxResults);
    return {
      enabled: value.enabled !== undefined ? Boolean(value.enabled) : defaultSearchPrefs.enabled,
      provider,
      maxResults: [3, 5, 10, 20].includes(maxResults) ? maxResults : defaultSearchPrefs.maxResults,
      safeSearch: value.safeSearch !== undefined ? Boolean(value.safeSearch) : defaultSearchPrefs.safeSearch,
      confirmBeforeSearch: value.confirmBeforeSearch !== undefined ? Boolean(value.confirmBeforeSearch) : defaultSearchPrefs.confirmBeforeSearch,
      preferGitHub: value.preferGitHub !== undefined ? Boolean(value.preferGitHub) : defaultSearchPrefs.preferGitHub,
      allowedDomains: normalizeDomainList(value.allowedDomains || ""),
      blockedDomains: normalizeDomainList(value.blockedDomains || ""),
      customEndpoint: String(value.customEndpoint || "").trim().slice(0, 300),
    };
  }

  function normalizeDomainList(value) {
    return String(value || "")
      .split(/[\n,]+/)
      .map((item) => item.trim().replace(/^https?:\/\//, "").replace(/\/.*$/, ""))
      .filter(Boolean)
      .slice(0, 30)
      .join("\n");
  }

  function currentSearchPreferences() {
    if (!state.searchPrefs) state.searchPrefs = loadSearchPreferences();
    return state.searchPrefs;
  }

  function saveSearchPreferences(next, { notify = false } = {}) {
    state.searchPrefs = normalizeSearchPreferences(next);
    try {
      localStorage.setItem(searchPrefsKey, JSON.stringify(state.searchPrefs));
    } catch {}
    if (state.activeSettingsPanel === "network-search") refreshActiveSettingsPanel?.();
    if (notify) showToast?.(pt("settings.searchSaved"), "success", { force: true });
  }

  function resetSearchPreferences() {
    saveSearchPreferences({ ...defaultSearchPrefs }, { notify: true });
  }

  function searchProviderLabel(provider) {
    return {
      duckduckgo: "DuckDuckGo",
      brave: "Brave Search",
      tavily: "Tavily",
      searxng: "SearXNG",
      custom: pt("settings.customEndpoint"),
    }[provider] || provider || pt("settings.noSelection");
  }

  function searchPrefsExport() {
    return JSON.stringify(currentSearchPreferences(), null, 2);
  }

  function localSkillID(prefix = "item") {
    return `${prefix}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`;
  }

  function loadSkillsPreferences() {
    try {
      return normalizeSkillsPreferences(JSON.parse(safeReadLocalPreference(skillsPrefsKey) || "{}"));
    } catch {
      return normalizeSkillsPreferences({});
    }
  }

  function normalizeSkillsPreferences(value = {}) {
    const commands = Array.isArray(value.commands) ? value.commands : defaultSkillsPrefs.commands;
    const mcpServers = Array.isArray(value.mcpServers) ? value.mcpServers : defaultSkillsPrefs.mcpServers;
    const policy = value.toolPolicy || {};
    return {
      commands: commands.map(normalizeSkillCommand).filter((item) => item.name && item.prompt).slice(0, 50),
      mcpServers: mcpServers.map(normalizeMCPServer).filter((item) => item.name || item.command).slice(0, 30),
      toolPolicy: {
        requireConfirmationForExec: policy.requireConfirmationForExec !== undefined ? Boolean(policy.requireConfirmationForExec) : defaultSkillsPrefs.toolPolicy.requireConfirmationForExec,
        requireConfirmationForWrites: policy.requireConfirmationForWrites !== undefined ? Boolean(policy.requireConfirmationForWrites) : defaultSkillsPrefs.toolPolicy.requireConfirmationForWrites,
        allowReadOnlyByDefault: policy.allowReadOnlyByDefault !== undefined ? Boolean(policy.allowReadOnlyByDefault) : defaultSkillsPrefs.toolPolicy.allowReadOnlyByDefault,
        preferPlanForLargeTasks: policy.preferPlanForLargeTasks !== undefined ? Boolean(policy.preferPlanForLargeTasks) : defaultSkillsPrefs.toolPolicy.preferPlanForLargeTasks,
      },
    };
  }

  // Seed text is resolved here rather than stored, so the shipped commands follow
  // the active language instead of the language of whoever first opened the app.
  // A stored literal that still matches something the seed once shipped counts as
  // untouched and is re-resolved; anything else is the user's own wording and is
  // returned verbatim. The key is kept on the object so a later locale change can
  // resolve it again.
  function resolveSeedText(stored, key, limit) {
    const text = String(stored || "").trim();
    if (!key) return text.slice(0, limit);
    if (text && !seedSkillCommandTexts.includes(text)) return text.slice(0, limit);
    // t, not pt: pt reads the preferences catalog, while these keys live in the
    // shared workspace catalog alongside the built-in /goal and /queue text.
    const translated = String(t(key, {}, currentUILocale()) || "").trim();
    // t falls back to the key itself when a catalog entry is missing. Showing a
    // dotted key in the palette would be worse than showing the stored text.
    if (!translated || translated === key) return text.slice(0, limit);
    return translated.slice(0, limit);
  }

  function normalizeSkillCommand(command = {}) {
    const rawName = String(command.name || "").trim();
    const name = rawName ? (rawName.startsWith("/") ? rawName : `/${rawName}`) : "";
    // Profiles written before the seed carried keys hold only the literal text, so
    // the key is recovered from the seed id. Matching on id alone is not enough:
    // a user may have renamed or rewritten that command, and resolveSeedText is what
    // decides, by comparing the stored text against what the seed once shipped.
    const seedKeys = seedSkillCommandKeys[String(command.id || "")] || {};
    const descriptionKey = String(command.descriptionKey || seedKeys.descriptionKey || "").trim();
    const promptKey = String(command.promptKey || seedKeys.promptKey || "").trim();
    const normalized = {
      id: String(command.id || localSkillID("cmd")),
      name: name.slice(0, 40),
      description: resolveSeedText(command.description, descriptionKey, 160),
      prompt: resolveSeedText(command.prompt, promptKey, 4000),
      enabled: command.enabled !== undefined ? Boolean(command.enabled) : true,
    };
    if (descriptionKey) normalized.descriptionKey = descriptionKey;
    if (promptKey) normalized.promptKey = promptKey;
    return normalized;
  }

  function normalizeMCPServer(server = {}) {
    return {
      id: String(server.id || localSkillID("mcp")),
      name: String(server.name || "").trim().slice(0, 80),
      command: String(server.command || "").trim().slice(0, 500),
      transport: ["stdio", "sse", "http"].includes(server.transport) ? server.transport : "stdio",
      enabled: server.enabled !== undefined ? Boolean(server.enabled) : false,
    };
  }

  function currentSkillsPreferences() {
    if (!state.skillsPrefs) state.skillsPrefs = loadSkillsPreferences();
    return state.skillsPrefs;
  }

  function saveSkillsPreferences(next, { notify = false } = {}) {
    state.skillsPrefs = normalizeSkillsPreferences(next);
    try {
      localStorage.setItem(skillsPrefsKey, JSON.stringify(state.skillsPrefs));
    } catch {}
    if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel?.();
    updatePromptHistoryHint?.();
    updateSlashCommandPalette?.();
    if (notify) showToast?.(pt("settings.skillsSaved"), "success", { force: true });
  }

  function resetSkillsPreferences() {
    saveSkillsPreferences({ ...defaultSkillsPrefs }, { notify: true });
  }

  function skillsPrefsExport() {
    return JSON.stringify(currentSkillsPreferences(), null, 2);
  }

  function loadIMGatewayPreferences() {
    try {
      return normalizeIMGatewayPreferences(JSON.parse(safeReadLocalPreference(imGatewayPrefsKey) || "{}"));
    } catch {
      return normalizeIMGatewayPreferences({});
    }
  }

  function normalizeIMGatewayPreferences(value = {}) {
    const channel = ["webhook", "discord", "slack", "telegram", "lark", "wecom", "custom"].includes(value.channel)
      ? value.channel
      : defaultIMGatewayPrefs.channel;
    const maxPayloadKB = Number(value.maxPayloadKB || defaultIMGatewayPrefs.maxPayloadKB);
    return {
      enabled: value.enabled !== undefined ? Boolean(value.enabled) : defaultIMGatewayPrefs.enabled,
      channel,
      inboundConfirm: value.inboundConfirm !== undefined ? Boolean(value.inboundConfirm) : defaultIMGatewayPrefs.inboundConfirm,
      requireSignature: value.requireSignature !== undefined ? Boolean(value.requireSignature) : defaultIMGatewayPrefs.requireSignature,
      redactSecrets: value.redactSecrets !== undefined ? Boolean(value.redactSecrets) : defaultIMGatewayPrefs.redactSecrets,
      allowInboundMessages: value.allowInboundMessages !== undefined ? Boolean(value.allowInboundMessages) : defaultIMGatewayPrefs.allowInboundMessages,
      notifyOnTaskDone: value.notifyOnTaskDone !== undefined ? Boolean(value.notifyOnTaskDone) : defaultIMGatewayPrefs.notifyOnTaskDone,
      notifyOnErrors: value.notifyOnErrors !== undefined ? Boolean(value.notifyOnErrors) : defaultIMGatewayPrefs.notifyOnErrors,
      notifyOnToolCalls: value.notifyOnToolCalls !== undefined ? Boolean(value.notifyOnToolCalls) : defaultIMGatewayPrefs.notifyOnToolCalls,
      maxPayloadKB: [32, 64, 128, 256].includes(maxPayloadKB) ? maxPayloadKB : defaultIMGatewayPrefs.maxPayloadKB,
      endpointUrl: String(value.endpointUrl || "").trim().slice(0, 400),
      allowedOrigins: normalizeLineList(value.allowedOrigins || "", 30),
      blockedSenders: normalizeLineList(value.blockedSenders || "", 50),
    };
  }

  function normalizeLineList(value, limit = 30) {
    return String(value || "")
      .split(/[\n,]+/)
      .map((item) => item.trim())
      .filter(Boolean)
      .slice(0, limit)
      .join("\n");
  }

  function currentIMGatewayPreferences() {
    if (!state.imGatewayPrefs) state.imGatewayPrefs = loadIMGatewayPreferences();
    return state.imGatewayPrefs;
  }

  function saveIMGatewayPreferences(next, { notify = false } = {}) {
    state.imGatewayPrefs = normalizeIMGatewayPreferences(next);
    try {
      localStorage.setItem(imGatewayPrefsKey, JSON.stringify(state.imGatewayPrefs));
    } catch {}
    if (state.activeSettingsPanel === "im-gateway") refreshActiveSettingsPanel?.();
    if (notify) showToast?.(pt("settings.imGatewaySaved"), "success", { force: true });
  }

  function resetIMGatewayPreferences() {
    saveIMGatewayPreferences({ ...defaultIMGatewayPrefs }, { notify: true });
  }

  function imGatewayChannelLabel(channel) {
    return {
      webhook: pt("settings.genericWebhook"),
      discord: "Discord",
      slack: "Slack",
      telegram: "Telegram",
      lark: pt("settings.lark"),
      wecom: pt("settings.wecom"),
      custom: pt("settings.customGateway"),
    }[channel] || channel || pt("settings.noSelection");
  }

  function imGatewayPrefsExport() {
    return JSON.stringify(currentIMGatewayPreferences(), null, 2);
  }

  function loadNotificationPreferences() {
    try {
      return normalizeNotificationPreferences(JSON.parse(safeReadLocalPreference(notificationPrefsKey) || "{}"));
    } catch {
      return normalizeNotificationPreferences({});
    }
  }

  function normalizeNotificationPreferences(value = {}) {
    const duration = ["short", "normal", "long"].includes(value.duration) ? value.duration : defaultNotificationPrefs.duration;
    return {
      toastEnabled: value.toastEnabled !== undefined ? Boolean(value.toastEnabled) : defaultNotificationPrefs.toastEnabled,
      infoToasts: value.infoToasts !== undefined ? Boolean(value.infoToasts) : defaultNotificationPrefs.infoToasts,
      successToasts: value.successToasts !== undefined ? Boolean(value.successToasts) : defaultNotificationPrefs.successToasts,
      warningToasts: value.warningToasts !== undefined ? Boolean(value.warningToasts) : defaultNotificationPrefs.warningToasts,
      errorToasts: value.errorToasts !== undefined ? Boolean(value.errorToasts) : defaultNotificationPrefs.errorToasts,
      terminalNotices: value.terminalNotices !== undefined ? Boolean(value.terminalNotices) : defaultNotificationPrefs.terminalNotices,
      duration,
      soundEnabled: value.soundEnabled !== undefined ? Boolean(value.soundEnabled) : defaultNotificationPrefs.soundEnabled,
      soundOnDone: value.soundOnDone !== undefined ? Boolean(value.soundOnDone) : defaultNotificationPrefs.soundOnDone,
      soundOnError: value.soundOnError !== undefined ? Boolean(value.soundOnError) : defaultNotificationPrefs.soundOnError,
      systemNotifications: value.systemNotifications !== undefined ? Boolean(value.systemNotifications) : defaultNotificationPrefs.systemNotifications,
      errorToastsPersist: value.errorToastsPersist !== undefined ? Boolean(value.errorToastsPersist) : defaultNotificationPrefs.errorToastsPersist,
      hapticFeedback: value.hapticFeedback !== undefined ? Boolean(value.hapticFeedback) : defaultNotificationPrefs.hapticFeedback,
    };
  }

  function currentNotificationPreferences() {
    if (!state.notifications) state.notifications = loadNotificationPreferences();
    return state.notifications;
  }

  function saveNotificationPreferences(next, { notify = false } = {}) {
    state.notifications = normalizeNotificationPreferences(next);
    try {
      localStorage.setItem(notificationPrefsKey, JSON.stringify(state.notifications));
    } catch {}
    if (state.activeSettingsPanel === "notifications") refreshActiveSettingsPanel?.();
    if (notify) showToast?.(pt("settings.notificationSaved"), "success", { force: true });
  }

  function setNotificationPreference(field, value) {
    const prefs = { ...currentNotificationPreferences() };
    if (field === "duration") {
      prefs.duration = value;
    } else {
      prefs[field] = value === true || value === "true";
    }
    saveNotificationPreferences(prefs, { notify: true });
  }

  function resetNotificationPreferences() {
    saveNotificationPreferences({ ...defaultNotificationPrefs }, { notify: true });
  }

  function notificationVariantEnabled(variant) {
    const prefs = currentNotificationPreferences();
    if (!prefs.toastEnabled) return false;
    if (variant === "success") return prefs.successToasts;
    if (variant === "warn" || variant === "warning") return prefs.warningToasts;
    if (variant === "error") return prefs.errorToasts;
    return prefs.infoToasts;
  }

  function notificationToastDuration(variant) {
    const prefs = currentNotificationPreferences();
    const base = variant === "error" ? 7000 : 3800;
    if (prefs.duration === "short") return variant === "error" ? 4500 : 2400;
    if (prefs.duration === "long") return variant === "error" ? 11000 : 7000;
    return base;
  }

  // 0 means "wait for the user to dismiss it". An error that expired on its own
  // was the reason a failed run could go unnoticed entirely.
  function notificationToastHoldsUntilDismissed(variant) {
    if (variant !== "error") return false;
    return currentNotificationPreferences().errorToastsPersist !== false;
  }

  function notificationSoundEnabled(tone) {
    const prefs = currentNotificationPreferences();
    if (!prefs.soundEnabled) return false;
    if (tone === "error") return prefs.soundOnError !== false;
    if (tone === "success") return prefs.soundOnDone !== false;
    return false;
  }

  function systemNotificationsEnabled() {
    return currentNotificationPreferences().systemNotifications === true;
  }

  // Whether touch gestures may vibrate. Absent hardware support is handled at the
  // call site, so this answers the preference question only.
  function hapticFeedbackEnabled() {
    return currentNotificationPreferences().hapticFeedback !== false;
  }

  function notifyTerminal(message) {
    if (currentNotificationPreferences().terminalNotices) appendTerminal?.(message);
  }

  function loadAppearancePreferences() {
    try {
      const raw = safeReadLocalPreference(appearancePrefsKey);
      const value = JSON.parse(raw || "{}");
      const normalized = normalizeAppearancePreferences(value);
      const normalizedText = JSON.stringify(normalized);
      if (raw !== null && (Number(value?.styleVersion || 0) < appearanceStyleVersion || JSON.stringify(value) !== normalizedText)) {
        try {
          localStorage.setItem(appearancePrefsKey, normalizedText);
        } catch {}
      }
      return normalized;
    } catch {
      return normalizeAppearancePreferences({});
    }
  }

  function normalizeAppearancePreferences(value = {}) {
    const sourceStyleVersion = Number(value.styleVersion || 0);
    const hasThemePreset = Object.prototype.hasOwnProperty.call(value, "themePreset");
    const requestedPreset = normalizeAppearanceThemePreset(value.themePreset);
    const legacyTheme = ["dark", "light"].includes(value.theme) ? value.theme : defaultAppearancePrefs.theme;
    // Version 2 stored only a light/dark value. Older, unversioned preferences
    // retain the previous safe migration to light instead of reviving a stale dark shell.
    const migratedPreset = hasThemePreset
      ? (requestedPreset || defaultAppearancePrefs.themePreset)
      : (sourceStyleVersion >= 2 ? legacyTheme : defaultAppearancePrefs.themePreset);
    let themeRef = (sourceStyleVersion >= appearanceStyleVersion || value.themeRef)
      ? normalizeAppearanceThemeRef(value.themeRef, migratedPreset)
      : { kind: "preset", id: migratedPreset };
    // Preserve callers that update only themePreset through saveAppearancePreferences.
    if (themeRef.kind === "preset" && requestedPreset && requestedPreset !== themeRef.id) {
      themeRef = { kind: "preset", id: requestedPreset };
    }
    const themePreset = themeRef.kind === "preset"
      ? themeRef.id
      : (requestedPreset || themeRef.colorScheme || defaultAppearancePrefs.themePreset);
    const density = ["comfortable", "compact"].includes(value.density) ? value.density : defaultAppearancePrefs.density;
    const background = normalizeAppearanceBackground({ ...defaultAppearancePrefs, ...value });
    return {
      styleVersion: appearanceStyleVersion,
      themeRef,
      themeSchemeRefs: normalizeAppearanceThemeSchemeRefs(value.themeSchemeRefs, themeRef),
      themePreset,
      theme: appearanceThemeForRef(themeRef, themePreset),
      density,
      backgroundMode: background.mode,
      backgroundUrl: background.url,
      backgroundDim: background.dim,
      backgroundPositionX: background.positionX,
      backgroundPositionY: background.positionY,
      terminalDefaultOpen: value.terminalDefaultOpen !== undefined ? Boolean(value.terminalDefaultOpen) : defaultAppearancePrefs.terminalDefaultOpen,
      showEventLog: value.showEventLog !== undefined ? Boolean(value.showEventLog) : defaultAppearancePrefs.showEventLog,
      showThroughput: value.showThroughput !== undefined ? Boolean(value.showThroughput) : defaultAppearancePrefs.showThroughput,
    };
  }

  function currentAppearancePreferences() {
    if (!state.appearance) state.appearance = loadAppearancePreferences();
    return state.appearance;
  }

  function saveAppearancePreferences(next, { applyTerminalDefault = false, notify = false } = {}) {
    state.appearance = normalizeAppearancePreferences({ ...next, styleVersion: appearanceStyleVersion });
    try {
      localStorage.setItem(appearancePrefsKey, JSON.stringify(state.appearance));
    } catch {}
    applyAppearancePreferences({ applyTerminalDefault });
    if (state.activeSettingsPanel === "appearance") refreshActiveSettingsPanel?.();
    if (notify) showToast?.(pt("settings.appearanceSaved"), "success");
    return state.appearance;
  }

  function applyAppearancePreferences({ applyTerminalDefault = false } = {}) {
    const prefs = currentAppearancePreferences();
    // The white-shell layout is anchored to the legacy light selector. Keep that
    // structural marker in both themes and layer night colors without swapping
    // out any grid, sizing, positioning, or responsive rules.
    document.body.classList.toggle("theme-light", true);
    document.body.classList.toggle("theme-dark", prefs.theme === "dark");
    if (document.body.dataset) document.body.dataset.themePreset = prefs.themePreset;
    document.body.classList.toggle("ui-density-compact", prefs.density === "compact");
    document.body.classList.toggle("ui-density-comfortable", prefs.density !== "compact");
    // A class rather than a render flag: the pill is markup the transcript
    // already produced, so toggling this must not depend on re-rendering every
    // message that is currently on screen.
    document.body.classList.toggle("show-throughput", prefs.showThroughput === true);
    const themeResult = applyThemePreference?.(prefs);
    themeResult?.catch?.(() => {});
    const backgroundResult = applyBackgroundPreference?.(prefs);
    backgroundResult?.catch?.(() => {});
    updateGlobalThemeToggle?.();
    if (applyTerminalDefault && $("appShell")) {
      toggleTerminal?.(!prefs.terminalDefaultOpen);
    }
  }

  function setAppearancePreference(field, value) {
    const prefs = { ...currentAppearancePreferences() };
    if (field === "theme" || field === "themePreset") {
      const preset = normalizeAppearanceThemePreset(value) || defaultAppearancePrefs.themePreset;
      prefs.themePreset = preset;
      prefs.themeRef = { kind: "preset", id: preset };
    } else if (field === "themeRef") {
      prefs.themeRef = normalizeAppearanceThemeRef(value, prefs.themePreset);
    } else if (field === "terminalDefaultOpen" || field === "showEventLog" || field === "showThroughput") {
      prefs[field] = value === true || value === "true";
    } else {
      prefs[field] = value;
    }
    return saveAppearancePreferences(prefs, { notify: true });
  }

  // The global light/dark button is a scheme switch, not a theme picker: it must
  // not overwrite the chosen theme the way setAppearancePreference("themePreset")
  // deliberately does for the picker itself.
  function toggleAppearanceColorScheme() {
    const prefs = currentAppearancePreferences();
    return saveAppearancePreferences({ ...prefs, ...appearanceThemeToggleTarget(prefs) });
  }

  function shouldLogAgentEvents() {
    return currentAppearancePreferences().showEventLog;
  }

  function loadPrimaryModePreference() {
    return normalizePrimaryModePreference(safeReadLocalPreference(primaryModePrefsKey));
  }

  function currentPrimaryModePreference() {
    if (!state.primaryModePreference) state.primaryModePreference = loadPrimaryModePreference();
    return state.primaryModePreference;
  }

  function applyPrimaryModePreference() {
    const primaryMode = currentPrimaryModePreference();
    applyPrimaryMode?.(primaryMode);
    return primaryMode;
  }

  function savePrimaryModePreference(next) {
    state.primaryModePreference = normalizePrimaryModePreference(next);
    try {
      localStorage.setItem(primaryModePrefsKey, state.primaryModePreference);
    } catch {}
    applyPrimaryModePreference();
    return state.primaryModePreference;
  }

  function setPrimaryModePreference(value) {
    return savePrimaryModePreference(value);
  }

  function safeReadLocalPreference(key) {
    try {
      return readLocalPreference(key);
    } catch {
      return null;
    }
  }

  function localPreferenceValueForBackup(entry, raw) {
    if (entry.key === primaryModePrefsKey) return normalizePrimaryModePreference(raw);
    if (entry.type !== "json") return raw;
    try {
      return normalizeImportedJSONPreference(entry.key, JSON.parse(raw || "null"));
    } catch {
      return normalizeImportedJSONPreference(entry.key, {});
    }
  }

  function localPreferencesBackupSummary() {
    const summary = localPreferenceBackupKeys.reduce((acc, entry) => {
      if (accountPreferences && accountPreferenceStorageKeySet.has(entry.key)) return acc;
      const raw = safeReadLocalPreference(entry.key);
      if (raw === null) return acc;
      acc.count += 1;
      acc.bytes += raw.length;
      acc.labels.push(entry.label);
      return acc;
    }, { count: 0, bytes: 0, labels: [] });
    if (!accountPreferences) return summary;
    const accountSnapshot = accountPreferences.getSnapshot();
    const accountEntries = [
      [profilePrefsKey, accountSnapshot.profile],
      [preferredModelKey, accountSnapshot.preferredModel],
      [modelVisibilityPrefsKey, accountSnapshot.modelVisibility],
    ];
    accountEntries.forEach(([key, value]) => {
      const entry = localPreferenceBackupKeys.find((item) => item.key === key);
      const raw = typeof value === "string" ? value : JSON.stringify(value);
      summary.count += 1;
      summary.bytes += raw.length;
      if (entry) summary.labels.push(entry.label);
    });
    return summary;
  }

  function createLocalPreferencesBackup() {
    const preferences = {};
    localPreferenceBackupKeys.forEach((entry) => {
      if (accountPreferences && accountPreferenceStorageKeySet.has(entry.key)) return;
      const raw = safeReadLocalPreference(entry.key);
      if (raw !== null) preferences[entry.key] = localPreferenceValueForBackup(entry, raw);
    });
    const backup = {
      kind: localPreferenceBackupKind,
      version: localPreferenceBackupVersion,
      app: "Autoto",
      appVersion: state.settings?.version || "0.1.0-dev",
      exportedAt: new Date().toISOString(),
      preferences,
    };
    if (accountPreferences) {
      const accountSnapshot = accountPreferences.getSnapshot();
      backup.accountPreferences = {
        profile: accountSnapshot.profile,
        preferredModel: accountSnapshot.preferredModel,
        modelVisibility: accountSnapshot.modelVisibility,
      };
    }
    return backup;
  }

  function localPreferencesBackupText() {
    return JSON.stringify(createLocalPreferencesBackup(), null, 2);
  }

  function normalizeImportedJSONPreference(key, value) {
    if (key === profilePrefsKey) return normalizeProfilePreferences(value || {});
    if (key === modelVisibilityPrefsKey) return accountPreferences?.normalizeModelVisibility?.(value || {}) || {
      hiddenModels: value?.hiddenModels && typeof value.hiddenModels === "object" ? value.hiddenModels : {},
      showUnconfiguredProviders: Boolean(value?.showUnconfiguredProviders),
    };
    if (key === searchPrefsKey) return normalizeSearchPreferences(value || {});
    if (key === imGatewayPrefsKey) return normalizeIMGatewayPreferences(value || {});
    if (key === skillsPrefsKey) return normalizeSkillsPreferences(value || {});
    if (key === notificationPrefsKey) return normalizeNotificationPreferences(value || {});
    if (key === appearancePrefsKey) return normalizeAppearancePreferences(value || {});
    if (key === terminalPrefsKey) return normalizeTerminalPreferences(value || {});
    if (key === regionalPrefsKey) return normalizeImportedRegionalPreferences(value || {});
    if (key === chatDraftsKey) return normalizeChatDrafts(value || {});
    if (key === promptHistoryKey) return normalizePromptHistory(value);
    if (key === recentDirectoriesKey) return normalizeRecentDirectories(value);
    return value || {};
  }

  function normalizeImportedLocalPreference(entry, value) {
    if (entry.type === "json") {
      let parsed = value;
      if (typeof parsed === "string") {
        try {
          parsed = JSON.parse(parsed || "null");
        } catch {
          throw new Error(pt("backup.invalidJSON", { label: entry.label }));
        }
      }
      return JSON.stringify(normalizeImportedJSONPreference(entry.key, parsed));
    }
    const text = String(value ?? "").trim();
    if (entry.key === primaryModePrefsKey) return normalizePrimaryModePreference(text);
    if (entry.key === preferredModelKey) return text.slice(0, 240);
    return text.slice(0, 1000);
  }

  function restoreLocalPreferencesBackup(text) {
    let payload = null;
    try {
      payload = JSON.parse(text);
    } catch {
      throw new Error(pt("backup.invalidFormat"));
    }
    const kind = String(payload?.kind || "").trim();
    if (kind && kind !== localPreferenceBackupKind) {
      throw new Error(pt("backup.unsupportedFormat"));
    }
    const preferences = payload?.preferences || payload?.settings || payload?.values || {};
    const accountPayload = payload?.accountPreferences;
    if ((!preferences || typeof preferences !== "object" || Array.isArray(preferences))
      || (accountPayload !== undefined && (!accountPayload || typeof accountPayload !== "object" || Array.isArray(accountPayload)))) {
      throw new Error(pt("backup.preferencesMissing"));
    }
    const hasPreference = (key) => Object.prototype.hasOwnProperty.call(preferences, key);
    const updates = localPreferenceBackupKeys
      .filter((entry) => (!accountPreferences || !accountPreferenceStorageKeySet.has(entry.key))
        && hasPreference(entry.key))
      .map((entry) => ({ entry, raw: normalizeImportedLocalPreference(entry, preferences[entry.key]) }));
    const accountPatch = {};
    if (accountPreferences) {
      const accountSource = accountPayload && typeof accountPayload === "object" ? accountPayload : {};
      const accountValue = (field, key) => {
        if (Object.prototype.hasOwnProperty.call(accountSource, field)) return accountSource[field];
        return hasPreference(key) ? preferences[key] : undefined;
      };
      const profile = accountValue("profile", profilePrefsKey);
      const preferredModel = accountValue("preferredModel", preferredModelKey);
      const modelVisibility = accountValue("modelVisibility", modelVisibilityPrefsKey);
      if (profile !== undefined) {
        const parsed = typeof profile === "string" ? safeParseImportedJSON(profile, localPreferenceBackupLabel({ labelKey: "profile" })) : profile;
        accountPatch.profile = normalizeProfilePreferences(parsed || {});
      }
      if (preferredModel !== undefined) accountPatch.preferredModel = String(preferredModel || "").trim().slice(0, 240);
      if (modelVisibility !== undefined) {
        const parsed = typeof modelVisibility === "string" ? safeParseImportedJSON(modelVisibility, localPreferenceBackupLabel({ labelKey: "modelVisibility" })) : modelVisibility;
        accountPatch.modelVisibility = accountPreferences.normalizeModelVisibility(parsed || {});
      }
    }
    const accountCount = Object.keys(accountPatch).length;
    if (!updates.length && !accountCount) throw new Error(pt("backup.noImportablePreferences"));
    try {
      updates.forEach(({ entry, raw }) => {
        if (!raw) localStorage.removeItem(entry.key);
        else localStorage.setItem(entry.key, raw);
      });
    } catch {
      throw new Error(pt("backup.storageWriteFailed"));
    }
    reloadLocalPreferencesFromStorage();
    if (!accountCount) return updates.length;
    return accountPreferences.importPreferences(accountPatch).then((imported) => {
      state.profile = loadProfilePreferences();
      applyProfilePreferences();
      renderModelOptions?.();
      return updates.length + imported;
    });
  }

  function safeParseImportedJSON(value, label) {
    try {
      return JSON.parse(value || "null");
    } catch {
      throw new Error(pt("backup.invalidJSON", { label }));
    }
  }

  function reloadLocalPreferencesFromStorage() {
    state.primaryModePreference = loadPrimaryModePreference();
    state.profile = loadProfilePreferences();
    state.searchPrefs = loadSearchPreferences();
    state.imGatewayPrefs = loadIMGatewayPreferences();
    state.skillsPrefs = loadSkillsPreferences();
    state.notifications = loadNotificationPreferences();
    state.terminalPrefs = loadTerminalPreferences();
    state.regionalPrefs = loadRegionalPreferences();
    state.chatDrafts = loadChatDrafts();
    state.promptHistory = loadPromptHistory();
    state.appearance = loadAppearancePreferences();
    applyPrimaryModePreference();
    applyProfilePreferences();
    applyAppearancePreferences();
    applyRegionalPreferences();
    trimTerminalOutput?.();
    updatePromptHistoryHint?.();
    renderRecentModalDirectories?.();
    renderModelOptions?.();
  }

  applyRegionalPreferences();

  return {
    applyAppearancePreferences,
    applyPrimaryModePreference,
    applyProfilePreferences,
    applyRegionalPreferences,
    createLocalPreferencesBackup,
    currentAppearancePreferences,
    currentIMGatewayPreferences,
    currentNotificationPreferences,
    currentPrimaryModePreference,
    currentProfilePreferences,
    currentRegionalPreferences,
    currentSearchPreferences,
    currentSkillsPreferences,
    imGatewayChannelLabel,
    imGatewayPrefsExport,
    loadAppearancePreferences,
    loadIMGatewayPreferences,
    loadNotificationPreferences,
    loadPrimaryModePreference,
    loadProfilePreferences,
    loadRegionalPreferences,
    loadSearchPreferences,
    loadSkillsPreferences,
    localPreferencesBackupSummary,
    localPreferencesBackupText,
    localSkillID,
    normalizeAppearancePreferences,
    normalizeDomainList,
    normalizeIMGatewayPreferences,
    normalizeImportedRegionalPreferences,
    normalizeLineList,
    normalizeMCPServer,
    normalizeNotificationPreferences,
    normalizeProfilePreferences,
    normalizeRegionalPreferences,
    normalizeSearchPreferences,
    normalizeSkillCommand,
    normalizeSkillsPreferences,
    notificationToastDuration,
    notificationToastHoldsUntilDismissed,
    hapticFeedbackEnabled,
    notificationSoundEnabled,
    systemNotificationsEnabled,
    notificationVariantEnabled,
    notifyTerminal,
    profileDisplayName,
    profileGitEnvExample,
    resetAppearancePreferences: () => saveAppearancePreferences({ ...defaultAppearancePrefs }, { applyTerminalDefault: true, notify: true }),
    resetIMGatewayPreferences,
    resetNotificationPreferences,
    resetProfilePreferences,
    resetRegionalPreferences,
    resetSearchPreferences,
    resetSkillsPreferences,
    restoreLocalPreferencesBackup,
    saveAppearancePreferences,
    saveIMGatewayPreferences,
    saveNotificationPreferences,
    savePrimaryModePreference,
    saveProfilePreferences,
    saveRegionalPreferences,
    saveSearchPreferences,
    saveSkillsPreferences,
    searchPrefsExport,
    searchProviderLabel,
    setAppearancePreference,
    setNotificationPreference,
    setPrimaryModePreference,
    shouldLogAgentEvents,
    skillsPrefsExport,
    toggleAppearanceColorScheme,
    updateSidebarAccountSummary,
  };
}
