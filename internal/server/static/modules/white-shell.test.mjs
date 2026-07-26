import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";

import {
  appearancePrefsKey,
  appearanceStyleVersion,
  appearanceThemePresets,
  defaultAppearancePrefs,
  localPreferenceBackupKind,
} from "./preferences-data.mjs";
import { nativeDirectoryPickerAllowed } from "./directory-browser.mjs";
import { createSettingsPreferencesController } from "./settings-preferences.mjs";
import { createSystemSettingsController } from "./system-settings.mjs";
import {
  collapsedSidebarWidth,
  compactComposerModelLabel,
  createUIShellController,
  defaultSidebarWidth,
  globalRailCollapsedPreferenceKey,
  globalRailCollapsedWidth,
  globalRailExpandedWidth,
  groupModelSelectOptions,
  maxSidebarWidth,
  sessionSidebarCollapsedPreferenceKey,
  minSidebarWidth,
  normalizeCollapsedPreference,
  normalizeSidebarWidth,
  orderPermissionMenuOptions,
  permissionMenuPrimaryValues,
  permissionMenuSecondaryValues,
  sidebarWidthFromPointer,
  sidebarWidthPreferenceKey,
} from "./ui-shell.mjs";

const staticRoot = new URL("../", import.meta.url);
const indexURL = new URL("index.html", staticRoot);
const appURL = new URL("app.js", staticRoot);
const appMainURL = new URL("modules/app-main.mjs", staticRoot);
const overviewDashboardURL = new URL("modules/overview-dashboard.mjs", staticRoot);
const i18nURL = new URL("modules/i18n.mjs", staticRoot);
const backgroundTasksURL = new URL("modules/background-tasks.mjs", staticRoot);
const agentWorkspaceHelpersURL = new URL("modules/agent-workspace-helpers.mjs", staticRoot);
const chatRenderingURL = new URL("modules/chat-rendering.mjs", staticRoot);
const chatRenderingMessagesURL = new URL("modules/messages-chat-rendering-extra.mjs", staticRoot);
const directoryBrowserURL = new URL("modules/directory-browser.mjs", staticRoot);
const settingsPreferencesURL = new URL("modules/settings-preferences.mjs", staticRoot);
const stylesURL = new URL("styles.css", staticRoot);
const uiShellURL = new URL("modules/ui-shell.mjs", staticRoot);

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
  const restoreStorage = replaceGlobal("localStorage", storage);
  const restoreDocument = replaceGlobal("document", {
    title: "",
    body: { classList: { toggle() {} } },
    getElementById() {
      return null;
    },
  });
  try {
    return callback();
  } finally {
    restoreDocument();
    restoreStorage();
  }
}

function createController(state = {}) {
  return createSettingsPreferencesController({
    state,
    loadChatDrafts: () => ({}),
    loadPromptHistory: () => [],
    loadTerminalPreferences: () => ({}),
    normalizeChatDrafts: (value) => value,
    normalizePromptHistory: (value) => value,
    normalizeRecentDirectories: (value) => value,
    normalizeTerminalPreferences: (value) => value,
  });
}

test("native directory picker requires capability, loopback, and macOS", () => {
  const previous = globalThis.window;
  globalThis.window = {};
  try {
    const options = { state: { remoteAccess: { capabilities: { nativePickerAllowed: true } } }, platformLike: "MacIntel" };
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "localhost" }, options), true);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "127.0.0.1" }, options), true);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "::1" }, options), true);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "192.168.0.146" }, options), false);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "appliance-tires-empire-partner.trycloudflare.com" }, options), false);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "localhost" }, { ...options, platformLike: "Win32" }), false);
    assert.equal(nativeDirectoryPickerAllowed({ hostname: "localhost" }, { state: {}, platformLike: "MacIntel" }), false);
  } finally {
    globalThis.window = previous;
  }
});

test("white shell adds the global rail before the conversation sidebar with the expected targets", async () => {
  const [html, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appMainURL, "utf8"),
  ]);

  assert.ok(html.indexOf('class="global-rail"') < html.indexOf('class="sidebar"'));
  const buttons = [...html.matchAll(/<button class="global-rail-button[^\"]*"[^>]*data-global-rail-target="([^"]+)"[^>]*>([\s\S]*?)<\/button>/g)]
    .map((match) => ({
      target: match[1],
      label: match[2].match(/class="global-rail-label"[^>]*>([^<]+)</)?.[1],
      markup: match[0],
    }));
  assert.deepEqual(buttons.map(({ target, label }) => ({ target, label })), [
    { target: "home", label: "首页" },
    { target: "conversation", label: "对话" },
    { target: "schedules", label: "排程" },
    { target: "profile", label: "设置" },
  ]);
  assert.ok(html.indexOf('data-global-rail-target="home"') < html.indexOf('data-global-rail-target="conversation"'));
  assert.ok(html.indexOf('data-global-rail-target="profile"') < html.indexOf('id="globalThemeToggleBtn"'));
  assert.doesNotMatch(html, /data-global-rail-target="(?:tasks|skills|runtime|im-gateway|agents)"/);
  assert.match(buttons[0].markup, /class="global-rail-button active"/);
  assert.match(buttons[0].markup, /aria-pressed="true"/);
  assert.match(buttons[0].markup, /data-i18n-title="shell\.nav\.home"/);
  assert.match(buttons[0].markup, /m3\.5 10\.5 8\.5-7 8\.5 7/);
  const scheduleButton = buttons.find(({ target }) => target === "schedules");
  assert.match(scheduleButton?.markup || "", /<rect x="4" y="5" width="16" height="15" rx="2\.5"><\/rect>/);
  assert.match(scheduleButton?.markup || "", /M8 3v4M16 3v4M4 9h16/);
  assert.match(appMain, /querySelectorAll\("\[data-global-rail-target\]"\)/);
  assert.match(appMain, /activateGlobalRailTarget\(node\.dataset\.globalRailTarget\)/);
  assert.match(html, /id="globalRailCollapseBtn"[^>]*aria-expanded="true"[^>]*data-i18n-title="shell\.collapseGlobalNavigation"/);
  assert.doesNotMatch(html, /id="globalRailAvatar"/);
  assert.match(html, /class="global-rail-button global-rail-settings-button"[^>]*data-global-rail-target="profile"/);
  assert.match(html, /id="sessionSidebarCollapseBtn"[^>]*aria-expanded="true"[^>]*data-i18n-title="shell\.collapseSessionSidebar"/);

  const ids = [...html.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
  assert.equal(new Set(ids).size, ids.length, "white shell must not introduce duplicate IDs");
});

test("desktop home overview stays available while mobile starts in conversation", async () => {
  const [html, appMain, overviewDashboard, styles, messagesCN, messagesTW, messagesEN] = await Promise.all([
    readFile(indexURL, "utf8"), readFile(appMainURL, "utf8"), readFile(overviewDashboardURL, "utf8"), readStylesSource(stylesURL),
    readFile(new URL("./messages-zh-CN.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-zh-TW.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-en.mjs", import.meta.url), "utf8"),
  ]);
  assert.match(html, /<main id="appShell"[^>]*>[\s\S]*?<section id="overviewDashboard" class="overview-dashboard-page"/);
  assert.doesNotMatch(html, /<main id="overviewDashboard"/);
  assert.doesNotMatch(overviewDashboard, /<main\b/);
  assert.match(overviewDashboard, /role="status" aria-live="polite" aria-atomic="true"/);
  assert.ok(html.indexOf('id="overviewDashboard"') < html.indexOf('id="schedulePanel"'));
  assert.ok(html.indexOf('id="schedulePanel"') < html.indexOf('id="conversationPanel"'));
  assert.doesNotMatch(html, /employeeOverviewModal|employeeOverviewBody/);
  assert.match(appMain, /createOverviewDashboardController\(\{[\s\S]*?request: api,[\s\S]*?host: "#overviewDashboard",[\s\S]*?translate: t,[\s\S]*?formatDateTime,[\s\S]*?onNavigate: handleOverviewNavigation/);
  assert.match(appMain, /key === "home"[\s\S]*?openOverviewDashboard\(\)\.catch\(showError\)/);
  assert.match(appMain, /overviewDashboard\.load\(\)/);
  assert.match(appMain, /function openOverviewTask\(id = ""\)[\s\S]*?taskWorkspace\.selectTask/);
  // Action -> surface routing is a table in overview-dashboard.mjs, checked
  // exhaustively in overview-navigation.test.mjs; app-main only dispatches it.
  assert.match(appMain, /const route = overviewNavigationRoute\(action\)/);
  assert.match(appMain, /route\.handler === "approvals"\) return openOverviewApprovals\(\)/);
  assert.match(appMain, /route\.handler === "runs"\) return openOverviewRuns\(target\)/);
  assert.match(appMain, /route\.handler === "schedules"\) return openOverviewSchedules\(target\)/);
  assert.match(appMain, /tool-calls\/pending/);
  assert.match(appMain, /loadRunSummary\(run\.id, \{ agentId: run\.agentId \}\)/);
  assert.match(appMain, /setGlobalRailActive\(currentShellRailTarget\(\)\)/);
  assert.equal((appMain.match(/state\.overviewActive && options\.preserveOverview !== true\) switchPrimaryWorkbench\("conversation"\)/g) || []).length, 2);
  assert.match(appMain, /preserveOverview: startup\.overviewActive/);
  assert.match(appMain, /mobile:\s*isMobileAppViewport\(\)/);
  assert.match(appMain, /function leaveOverviewForMobile\(\)[\s\S]*?state\.overviewActive = false;[\s\S]*?applyPrimaryWorkbench\("conversation"\)/);
  assert.match(appMain, /async function openOverviewDashboard\(\)[\s\S]*?if \(isMobileAppViewport\(\)\)[\s\S]*?state\.overviewActive = false;[\s\S]*?return;/);
  assert.match(html, /id="schedulePanel" class="schedule-workspace-panel hidden"/);
  assert.match(appMain, /function openOverviewSchedules\(id = ""\)[\s\S]*?scheduleWorkspace\.load[\s\S]*?switchPrimaryWorkbench\("schedules"\)[\s\S]*?scheduleWorkspace\.select/);
  assert.doesNotMatch(appMain, /function openOverviewSchedules\(id = ""\)[\s\S]*?openSettingsModal\("im-gateway"\)/);
  assert.match(styles, /Desktop home overview dashboard: a first-class white-shell surface, never a modal/);
  assert.match(styles, /\.overview-dashboard-page\s*\{[\s\S]*?overflow:\s*auto/);
  assert.match(styles, /overview-mode :is\(#conversationPanel, #workbenchPanel, #schedulePanel\)[\s\S]*?display:\s*none !important/);
  assert.match(styles, /not\(\.overview-mode\) #overviewDashboard[\s\S]*?display:\s*none !important/);
  assert.match(styles, /@media \(max-width:\s*767px\)\s*\{\s*body\.white-shell\.theme-light \.overview-dashboard-page\s*\{\s*display:\s*none !important;/);
  assert.match(styles, /theme-dark \.overview-dashboard-page/);
  for (const messages of [messagesCN, messagesTW, messagesEN]) {
    assert.match(messages, /nav:\s*\{\s*home:/);
    assert.match(messages, /overview:\s*\{/);
    assert.match(messages, /runningAgents:/);
    assert.match(messages, /pendingApprovalsCount:/);
  }
});

test("dual workbench shell keeps conversation and Kanban views in one runtime", async () => {
  const [html, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(html, /id="conversationPanel" class="chat-panel hidden"/);
  assert.match(html, /id="workbenchPanel" class="workbench-panel hidden"/);
  assert.match(html, /id="projectKanbanBody"/);
  const workbenchHeaderStart = html.indexOf('<header class="workbench-header">');
  const workbenchHeader = html.slice(workbenchHeaderStart, html.indexOf("</header>", workbenchHeaderStart));
  assert.match(workbenchHeader, /class="workbench-kicker sr-only"/);
  assert.match(workbenchHeader, /id="workbenchTitleEditor" class="chat-title-editor workbench-title-editor"/);
  assert.match(workbenchHeader, /id="workbenchTitle" class="workbench-title chat-title-display"[^>]*disabled>任务工作台<\/button>/);
  assert.match(workbenchHeader, /id="workbenchTitleInput" class="chat-title-input workbench-title-input hidden"/);
  assert.match(workbenchHeader, /id="editWorkbenchTitleBtn"[\s\S]*?id="saveWorkbenchTitleBtn"[\s\S]*?id="cancelWorkbenchTitleBtn"/);
  assert.match(workbenchHeader, /id="workbenchMeta" class="workbench-meta sr-only"/);
  assert.doesNotMatch(workbenchHeader, /id="workbenchTitle"[^>]*data-i18n/);
  assert.match(appMain, /function renderWorkbenchHeaderIdentity\(\)[\s\S]*?renderAgentTitleEditor\("workbench"\)/);
  assert.match(appMain, /\$\("workbenchTitle"\)\?\.addEventListener\("click", \(\) => beginConversationTitleEdit\("workbench"\)\)/);
  assert.match(appMain, /saveConversationTitle\("workbench"\)\.catch\(showError\)/);
  assert.match(appMain, /workbenchTitleRequired[\s\S]*?workbenchTitleInvalid[\s\S]*?workbenchTitleSaved/);
  assert.match(styles, /\.workbench-header\s*\{[^}]*height:\s*64px[^}]*min-height:\s*64px[^}]*flex:\s*0 0 64px[^}]*padding:\s*0 18px/);
  assert.match(styles, /\.workbench-heading\s*\{[^}]*flex:\s*1 1 auto[^}]*display:\s*flex[^}]*align-items:\s*center/);
  assert.match(styles, /\.workbench-title\s*\{[^}]*max-width:\s*min\(42vw, 520px\)[^}]*font-size:\s*16px[^}]*font-weight:\s*700/);
  assert.match(styles, /\.workbench-title-editor\s*\{[^}]*max-width:\s*min\(46vw, 620px\)[^}]*flex:\s*1 1 auto/);
  assert.match(styles, /\.workbench-title-input\s*\{[^}]*width:\s*min\(34vw, 420px\)[^}]*min-width:\s*140px/);
  assert.match(styles, /@media \(max-width:\s*767px\)\s*\{[\s\S]*?\.workbench-header\s*\{[^}]*height:\s*54px[^}]*flex:\s*0 0 54px[^}]*padding:\s*0 14px[\s\S]*?\.workbench-title\s*\{[^}]*font-size:\s*16px[^}]*font-weight:\s*500/);
  assert.match(styles, /\.workbench-header:has\(\.workbench-title-input:not\(\.hidden\)\) \.workbench-header-actions\s*\{[^}]*display:\s*none/);
  // The workbench and terminal shortcuts were removed from the mobile header.
  assert.doesNotMatch(html, /id="mobileWorkbenchBtn"|id="mobileTerminalBtn"/);
  const taskGlyph = /<rect x="4" y="3\.5" width="16" height="17" rx="3"><\/rect>[\s\S]*?m7\.5 8 1\.4 1\.4 2\.6-2\.6/;
  const workbenchBoardMarkup = workbenchHeader.match(/<button id="workbenchBoardBtn"[\s\S]*?<\/button>/)?.[0] || "";
  assert.match(workbenchBoardMarkup, taskGlyph);
  assert.match(appMain, /function applyPrimaryWorkbench\(value\)/);
  assert.match(appMain, /const mode = normalizedPrimaryWorkbench\(value\)[\s\S]*?setPrimaryModePreference\(mode\)/);
  assert.doesNotMatch(appMain, /function primaryWorkbenchRailTarget/);
  assert.match(appMain, /key === "conversation"[\s\S]*?switchPrimaryWorkbench\("conversation"\)/);
  assert.match(appMain, /key === "schedules"[\s\S]*?switchPrimaryWorkbench\("schedules"\)/);
  assert.doesNotMatch(appMain, /key === "tasks"/);
  // Which panel each mode shows is decided by primaryWorkbenchLayout and
  // checked exhaustively in workbench-layout.test.mjs; app-main only applies
  // the resulting matrix to the DOM.
  assert.match(appMain, /primaryWorkbenchLayout\(mode, \{ overviewActive: state\.overviewActive \}\)/);
  assert.match(appMain, /Object\.entries\(layout\.hidden\)\) \$\(id\)\?\.classList\.toggle\("hidden", hidden\)/);
  assert.match(appMain, /Object\.entries\(layout\.bodyClasses\)\) document\.body\.classList\.toggle\(name, active\)/);
  const applyStart = appMain.indexOf("function applyPrimaryWorkbench");
  const applyEnd = appMain.indexOf("function switchPrimaryWorkbench", applyStart);
  const applyBody = appMain.slice(applyStart, applyEnd);
  assert.doesNotMatch(applyBody, /disconnectAgentTransports|selectProject|selectNavigationConversation|beginNavigationSelection/);
  assert.match(appMain, /createPageLifecycleController\(\{[\s\S]*?agentStream\.resume\(detail\)[\s\S]*?agentStream\.pause/);
  assert.match(appMain, /autoto:auth-changed[\s\S]*?disconnectAgentTransports\(\)[\s\S]*?projectKanban\.setAgent\(null\)[\s\S]*?init\(\)\.catch\(showError\)/);
});

test("project switching preserves the current view until the next Agent is ready", async () => {
  const [appMain, styles] = await Promise.all([
    readFile(appMainURL, "utf8"),
    readStylesSource(stylesURL),
  ]);
  const selectStart = appMain.indexOf("async function selectProject");
  const selectEnd = appMain.indexOf("async function selectNavigationConversation", selectStart);
  const selectBody = appMain.slice(selectStart, selectEnd);

  assert.match(selectBody, /const preserveConversationView = Boolean\([\s\S]*?state\.agent\?\.id/);
  assert.match(selectBody, /beginNavigationSelection\(project, \{ preserveConversationView, selectionKind: "project" \}\)/);
  assert.match(selectBody, /markMessageViewportBusy\(\{ contextSwitch: true, label: am\("projectLoadingTitle"\) \}\)/);
  assert.match(selectBody, /await enterAgent\(\);[\s\S]*?clearMessageViewportBusy\(\);/);
  assert.match(appMain, /function beginNavigationSelection\(project, options = \{\}\)[\s\S]*?if \(!options\.preserveConversationView\) renderConversationHeaderIdentity\(\);/);
  assert.match(appMain, /function markMessageViewportBusy\(options = \{\}\)[\s\S]*?dataset\.contextSwitching = "true"/);
  assert.match(styles, /\.messages\[data-context-switching="true"\]::before/);
  assert.match(styles, /\.messages\[data-context-switching="true"\]::after/);
});

test("boot transition waits for app readiness and cross-fades the localized shell", async () => {
  const [html, app, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(app, /function normalizeBootLocale|const normalizeBootLocale/);
  assert.match(app, /navigator\?\.languages/);
  assert.match(app, /new Intl\.DateTimeFormat\(\)\.resolvedOptions\(\)\.locale/);
  assert.match(app, /const activeBootLocale = applyBootLocale\(\)/);
  assert.match(app, /querySelectorAll\?\.\('\[data-i18n="workspace\.main\.loadingProjectTitle"\]'/);
  assert.match(app, /正在載入專案/);
  assert.doesNotMatch(app, /loadingProjectDescription|Autoto 正在準備工作線和 AI 代理/);
  assert.match(html, /<html[^>]*data-ui-locale-pending="true"/);
  assert.match(html, /id="bootTransition" class="boot-transition"[^>]*role="status"[^>]*aria-labelledby="bootTransitionLabel"/);
  assert.equal((html.match(/class="boot-transition-dot"/g) || []).length, 3);
  assert.match(html, /id="bootTransitionLabel" class="sr-only"[^>]*data-i18n="workspace\.main\.loadingProjectTitle"/);
  assert.doesNotMatch(html, /boot-transition-(?:card|title|description|progress)|bootTransition(?:Title|Description)/);
  assert.match(styles, /#appShell\s*\{[^}]*opacity:\s*1;[^}]*transition:\s*opacity 280ms ease/);
  assert.match(styles, /html\[data-ui-locale-pending="true"\] #appShell\s*\{[^}]*visibility:\s*hidden;[^}]*opacity:\s*0;[^}]*transform:\s*translateY\(6px\)/);
  assert.match(styles, /\.boot-transition\s*\{[^}]*position:\s*fixed;[^}]*opacity:\s*0;[^}]*visibility:\s*hidden/);
  assert.match(styles, /html\[data-ui-locale-pending="true"\] \.boot-transition\s*\{[^}]*opacity:\s*1;[^}]*visibility:\s*visible/);
  assert.match(styles, /\.boot-transition\s*\{[^}]*background:\s*#f6f7fb/);
  assert.match(styles, /\.boot-transition-mark\s*\{[^}]*width:\s*auto;[^}]*margin:\s*0;[^}]*border:\s*0;[^}]*background:\s*transparent;[^}]*box-shadow:\s*none/);
  assert.doesNotMatch(styles, /\.boot-transition-(?:card|title|description|progress)/);
  assert.doesNotMatch(styles, /@keyframes boot-transition-(?:card-enter|mark-float|progress)/);
  assert.match(styles, /@keyframes boot-transition-dot-jump/);
  assert.match(styles, /@media \(prefers-reduced-motion:\s*reduce\)[\s\S]*?\.boot-transition-dot/);
  assert.match(app, /const appReadyEventName = "autoto:app-ready"/);
  assert.match(app, /const waitForAppReady = \(\{ timeout = 12000 \} = \{\}\) =>/);
  assert.match(app, /const appReady = waitForAppReady\(\);[\s\S]*?await import\("\.\/modules\/app-main\.mjs[\s\S]*?await appReady;[\s\S]*?revealLocalizedUI\(\)/);
  assert.match(appMain, /function signalAppReady\(\)[\s\S]*?new EventConstructor\("autoto:app-ready"\)/);
  assert.match(appMain, /init\(\)\.then\(\(\) => \{[\s\S]*?openRequestedInitialView\(\);[\s\S]*?const setupStartup = maybeOpenSetupWizard\(\);[\s\S]*?signalAppReady\(\);[\s\S]*?return setupStartup;[\s\S]*?\}\)\.catch\(\(error\) => \{[\s\S]*?signalAppReady\(\);[\s\S]*?showError\(error\);/);
  assert.match(html, /app\.js\?v=[^"\n]*boot-dots-only-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*boot-dots-only-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*boot-ready-transition-1/);
});

test("static entry points share throughput and usage-history cache stamps", async () => {
  const [html, app] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
  ]);
  assert.equal((html.match(/-fast-mode-1-throughput-1-usage-history-1/g) || []).length, 2);
  assert.equal((app.match(/-fast-mode-1-throughput-1-usage-history-1/g) || []).length, 1);
  assert.doesNotMatch(html, /-throughput-1["']/);
  assert.doesNotMatch(app, /-throughput-1["']/);
});

test("Codex browser login cache stamps reach the static entry and locale catalogs", async () => {
  const [html, app, appMain, i18n] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(i18nURL, "utf8"),
  ]);
  assert.equal((html.match(/codex-browser-login-1/g) || []).length, 2);
  assert.equal((app.match(/codex-browser-login-1/g) || []).length, 1);
  assert.match(appMain, /model-provider-settings\.mjs\?v=[^"\n]*codex-browser-login-1/);
  assert.match(appMain, /i18n\.mjs\?v=[^"\n]*codex-browser-login-1/);
  assert.equal((i18n.match(/messages-(?:en|zh-CN|zh-TW)\.mjs\?v=[^"\n]*codex-browser-login-1/g) || []).length, 3);
});

test("settings help cache stamps reach the shell and locale catalogs", async () => {
  const [html, app, appMain, i18n] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(i18nURL, "utf8"),
  ]);
  assert.equal((html.match(/settings-help-1/g) || []).length, 2);
  assert.equal((app.match(/settings-help-1/g) || []).length, 1);
  assert.match(appMain, /settings-help\.mjs\?v=settings-help-1/);
  assert.match(appMain, /i18n\.mjs\?v=[^"\n]*settings-help-1/);
  assert.equal((i18n.match(/messages-(?:en|zh-CN|zh-TW)\.mjs\?v=[^"\n]*settings-help-1/g) || []).length, 3);
});

test("storage settings cards keep visible vertical spacing", async () => {
  const [html, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(styles, /body\.white-shell\.theme-light \.legacy-settings-content-body \.storage-entry-list\s*\{[\s\S]*?gap:\s*12px/);
  assert.match(html, /styles\.css\?v=[^"\n]*storage-card-gap-1/);
});

test("network proxy settings remove duplicate agent management while keeping the backend modal", async () => {
  const [html, app, appMain, settingsData, settingsCategories, backendRegistry] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(new URL("./settings-data.mjs", import.meta.url), "utf8"),
    readFile(new URL("./settings-categories.mjs", import.meta.url), "utf8"),
    readFile(new URL("./backend-registry.mjs", import.meta.url), "utf8"),
  ]);

  assert.match(html, /id="manageBackendsBtn"/);
  assert.match(html, /id="backendsModal"[^>]*role="dialog"/);
  assert.doesNotMatch(settingsData, /settingItem\("agent-admin"/);
  assert.doesNotMatch(settingsCategories, /items:\s*\["network-search",\s*"agent-admin"\]/);
  assert.doesNotMatch(appMain, /\["agent-admin",\s*\{\s*render:/);
  assert.doesNotMatch(backendRegistry, /renderAgentAdminSettingsContent|bindAgentAdminSettingsActions|settingsBackendForm/);
  assert.match(appMain, /backend-registry\.mjs\?v=agent-admin-removed-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*agent-admin-removed-1/);
  assert.match(html, /app\.js\?v=[^"\n]*agent-admin-removed-1/);
});

test("folder picker uses stable SVG actions and directory icons instead of font glyphs", async () => {
  const [html, directoryBrowser, styles, appMain, app] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(directoryBrowserURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appMainURL, "utf8"),
    readFile(appURL, "utf8"),
  ]);
  const toolbar = html.slice(html.indexOf('<div class="folder-toolbar"'), html.indexOf('<div id="newFolderInline"'));
  const newFolderButton = toolbar.slice(toolbar.indexOf('id="newFolderBtn"'), toolbar.indexOf('</button>', toolbar.indexOf('id="newFolderBtn"')));

  assert.match(newFolderButton, /class="folder-tool-btn folder-tool-btn-labeled"/);
  assert.match(newFolderButton, /aria-controls="newFolderInline" aria-expanded="false"/);
  assert.match(newFolderButton, /<svg[^>]*viewBox="0 0 24 24"/);
  assert.match(newFolderButton, /data-i18n="folder\.newFolder">新建文件夹<\/span>/);
  assert.doesNotMatch(toolbar, /▱＋/);
  assert.doesNotMatch(toolbar, /toggleHiddenFoldersBtn|folder\.hiddenUnavailable/);
  assert.doesNotMatch(appMain, /toggleHiddenFoldersBtn|hiddenFoldersNotShown/);
  assert.match(directoryBrowser, /const directoryFolderIcon = `[\s\S]*class="directory-folder-svg"/);
  assert.match(directoryBrowser, /class="directory-icon" aria-hidden="true">\$\{directoryFolderIcon\}/);
  assert.doesNotMatch(directoryBrowser, /class="directory-icon">▱/);
  assert.match(directoryBrowser, /filter\(\(shortcut\) => shortcut\.name !== "Root"\)/);
  assert.doesNotMatch(directoryBrowser, /folder-root-section|folder-root-card|rootHTML/);
  assert.doesNotMatch(styles, /\.folder-root-card\s*\{/);
  assert.match(directoryBrowser, /trigger\?\.setAttribute\("aria-expanded", "true"\)/);
  assert.match(directoryBrowser, /trigger\?\.setAttribute\("aria-expanded", "false"\)/);
  assert.match(styles, /\.folder-tool-btn-labeled \{/);
  assert.match(styles, /\.directory-folder-stroke \{/);
  assert.match(appMain, /directory-browser\.mjs\?v=[^"\n]*root-shortcut-removed-1/);
  assert.match(appMain, /messages-app-main-extra\.mjs\?v=[^"\n]*hidden-toggle-removed-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*hidden-toggle-removed-1/);
  assert.match(html, /app\.js\?v=[^"\n]*hidden-toggle-removed-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*root-shortcut-removed-1/);
  assert.equal((html.match(/folder-picker-remote-2/g) || []).length, 2);
});

test("conversation sidebar separates projects and standalone conversations without a redundant list heading", async () => {
  const [html, app, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readStylesSource(stylesURL),
  ]);
  const header = html.slice(html.indexOf('<header class="session-sidebar-header">'), html.indexOf("</header>", html.indexOf('<header class="session-sidebar-header">')));
  const filters = html.slice(html.indexOf('id="navigationFilters"'), html.indexOf("</div>", html.indexOf('id="navigationFilters"')));

  assert.match(header, /class="session-sidebar-title"[^>]*>会话</);
  for (const id of ["projectSearchToggleBtn", "newProjectBtn", "refreshBtn"]) {
    assert.match(header, new RegExp(`id="${id}"`));
  }
  assert.doesNotMatch(html, /class="nav-stack"/);
  assert.equal((filters.match(/data-navigation-mode=/g) || []).length, 2);
  assert.match(filters, /class="navigation-filter active"[^>]*data-navigation-mode="projects"[^>]*aria-pressed="true"/);
  assert.match(filters, /data-navigation-mode="conversations"[^>]*aria-pressed="false"/);
  assert.doesNotMatch(filters, /data-navigation-mode="all"|shell\.filters\.all|>全部</);
  assert.doesNotMatch(html, /navigationListHeading|navigation-list-heading/);
  assert.doesNotMatch(styles, /\.navigation-list-heading/);
  assert.doesNotMatch(appMain, /navigationListHeading/);
  assert.match(appMain, /navigationMode:\s*"projects"/);
  assert.match(appMain, /node\.dataset\.navigationMode \|\| "projects"/);
  assert.match(html, /id="recentSidebarConversations"/);
  assert.match(html, /id="recentSidebarDirectories"/);
  assert.match(html, /id="globalThemeToggleBtn"/);
  assert.match(html, /id="globalHealthText"/);
  assert.match(html, /styles\.css\?v=[^"\n]*navigation-split-1/);
  assert.match(html, /app\.js\?v=[^"\n]*navigation-split-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*navigation-split-1/);
});

test("conversation creation routes project contexts through folder selection and keeps standalone API", async () => {
  const [html, app, appMain] = await Promise.all([readFile(indexURL, "utf8"), readFile(appURL, "utf8"), readFile(appMainURL, "utf8")]);
  const desktop = html.match(/<button id="newProjectBtn"[^>]*>/)?.[0] || "";
  const mobile = html.match(/<button id="mobileNewConversationBtn"[^>]*>/)?.[0] || "";
  const folder = html.match(/<button id="mobileChooseDirectoryBtn"[^>]*>/)?.[0] || "";
  assert.match(desktop, /data-create-navigation-item/);
  assert.doesNotMatch(desktop, /data-create-conversation/);
  assert.match(mobile, /data-create-conversation/);
  assert.doesNotMatch(`${desktop}${mobile}`, /data-open-directory-shortcut/);
  assert.match(folder, /data-open-directory-shortcut="current"/);
  // Which target the create button acts on, and the label that must agree with
  // it, are decided by navigation-create.mjs and covered in
  // navigation-create.test.mjs across every workbench/navigation-mode pair.
  assert.match(appMain, /return navigationCreateTarget\(state\)/);
  assert.match(appMain, /const labelKey = navigationCreateLabelKey\(target\)/);
  assert.match(appMain, /async function createNavigationItem\(trigger = null\)/);
  assert.match(appMain, /const target = currentNavigationCreateTarget\(\)[\s\S]*?if \(target === "schedule"\) return startScheduleCreation\(\)[\s\S]*?if \(target === "conversation"\) return createStandaloneConversation\(\)/);
  assert.match(appMain, /openDirectoryChooser\(state\.project\?\.gitPath \|\| state\.agent\?\.cwd \|\| "", \{ trigger \}\)/);
  assert.match(appMain, /\[data-create-navigation-item\][\s\S]*?createNavigationItem\(button\)/);
  assert.match(appMain, /async function createStandaloneConversation/);
  assert.match(appMain, /if \(state\.standaloneConversationCreating\) return null/);
  assert.match(appMain, /api\("\/api\/conversations", \{[\s\S]*?method: "POST"/);
  assert.match(appMain, /title: t\("shell\.newConversation"\)/);
  assert.match(appMain, /const model = selectedModelValue\(\)/);
  assert.match(appMain, /await loadProjects\(\)[\s\S]*?await selectNavigationConversation\(conversation\)/);
  assert.match(appMain, /api\(`\/api\/agents\/\$\{encodeURIComponent\(agentId\)\}`\)/);
  assert.match(appMain, /modelPatchInFlight = true[\s\S]*?applyAgentPatch\("model", \{ model \}\)[\s\S]*?modelPatchInFlight = false/);
  assert.match(appMain, /if \(modelPatchInFlight && previousAgent[\s\S]*?state\.agent = previousAgent[\s\S]*?renderModelOptions\(\)[\s\S]*?refreshReasoningEffortControl\(\)/);
  assert.match(html, /app\.js\?v=[^"\n]*standalone-conversation-1[^"\n]*contextual-create-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*contextual-create-1/);
});

test("conversation navigation exposes archive, pin, and accessible context-menu controls", async () => {
  const [html, appMain, navigation, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(new URL("./conversation-navigation.mjs", import.meta.url), "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.doesNotMatch(html, /id="navigationArchiveToggleBtn"/);
  assert.match(html, /id="navigationContextMenu"[^>]*role="menu"/);
  assert.match(html, /data-navigation-menu-action="pin"/);
  assert.match(html, /data-navigation-menu-action="archive"/);
  assert.match(navigation, /data-navigation-menu-trigger/);
  assert.match(navigation, /navigation-state-badge pinned/);
  assert.match(navigation, /navigation-state-badge archived/);
  assert.match(appMain, /createArchiveSettingsController/);
  assert.match(appMain, /archiveSettings/);
  assert.match(appMain, /document\.addEventListener\("contextmenu", handleNavigationContextMenu\)/);
  assert.match(appMain, /applyNavigationMenuAction\(action\)/);
  assert.match(styles, /\.navigation-context-menu\s*\{[\s\S]*?position:\s*fixed/);
  assert.match(styles, /\.navigation-row-actions\s*\{[\s\S]*?opacity:\s*0/);
});

test("conversation, task, and schedule modes expose separate creation boundaries", async () => {
  const [html, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(html, /id="sessionSidebar" class="sidebar"/);
  assert.match(html, /id="sessionSidebarTitle" class="session-sidebar-title"/);
  assert.match(html, /id="newProjectBtn" class="[^"]*session-sidebar-action[^"]*"[^>]*data-create-navigation-item/);
  assert.match(html, /id="mobileNewScheduleBtn" class="mobile-drawer-primary-action hidden"/);
  const scheduleModeButton = html.match(/<button\b(?=[^>]*\bid="mobileScheduleModeBtn")[^>]*>/)?.[0] || "";
  assert.match(scheduleModeButton, /\btype="button"/);
  assert.match(scheduleModeButton, /\bdata-i18n-aria-label="shell\.nav\.schedules"/);
  const scheduleModeClasses = (scheduleModeButton.match(/\bclass="([^"]*)"/)?.[1] || "").split(/\s+/).filter(Boolean);
  assert.equal(scheduleModeClasses.includes("hidden"), false);
  assert.doesNotMatch(scheduleModeButton, /(?:^|\s)hidden(?:\s|=|>)/);
  // Product contract: schedules remains directly above the mobile settings actions.
  assert.ok(html.indexOf('id="mobileScheduleModeBtn"') < html.indexOf('id="mobileSidebarSettingsBtn"'));
  assert.match(html, /id="schedulePanel" class="schedule-workspace-panel hidden"/);
  assert.match(html, /id="newTaskBtn" class="[^"]*task-mode-action hidden"[^>]*disabled/);
  assert.match(html, /id="specBoardBtn" class="icon-btn header-tool-btn"[^>]*data-project-context-only/);
  assert.doesNotMatch(html, /id="specBoardBtn" class="[^"]*\bhidden\b/);
  assert.match(html, /id="navigationFilters" class="[^"]*conversation-mode-only/);
  assert.match(html, /recent-directories-sidebar conversation-mode-only/);
  assert.match(appMain, /const scheduleContext = state\.activeWorkbench === "schedules"/);
  assert.match(appMain, /const baseNavigationMode = taskContext \? "projects" : state\.navigationMode/);
  assert.match(appMain, /const effectiveNavigationMode = !taskContext && compactSessionSidebar \? "all" : baseNavigationMode/);
  assert.match(appMain, /if \(scheduleContext\)[\s\S]*?scheduleWorkspace\.renderNavigation/);
  assert.match(appMain, /renderNavigationHTML\(view, \{[\s\S]*?taskContext,/);
  assert.match(appMain, /newTaskBtn"\)\?\.addEventListener\("click", \(\) => focusTaskCreation\(\)\.catch\(showError\)\)/);
  assert.match(appMain, /\$\("mobileScheduleModeBtn"\)\?\.addEventListener\("click", \(\) => \{[\s\S]*?closeMobileSidebar\(\);[\s\S]*?switchPrimaryWorkbench\(state\.activeWorkbench === "schedules" \? "conversation" : "schedules"\)/);
  assert.match(appMain, /projectKanban\.focusCreate\(\)/);
  assert.match(appMain, /data-primary-workbench-target/);
  assert.match(styles, /body\.white-shell\.theme-light\.workbench-mode \.conversation-mode-only\s*\{[\s\S]*?display:\s*none !important/);
  assert.match(styles, /body\.white-shell\.theme-light\.schedule-mode \.conversation-mode-only\s*\{[\s\S]*?display:\s*none !important/);
  assert.match(styles, /First-class schedule workspace/);
  assert.match(styles, /body\.white-shell\.theme-light\.workbench-mode #newTaskBtn\s*\{[\s\S]*?background:\s*var\(--task-accent-soft\)/);
  assert.match(styles, /\.navigation-boundary-empty\s*\{/);
});

test("composer operation controls are exposed only in project context", async () => {
  const [html, styles, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appMainURL, "utf8"),
  ]);
  const formStart = html.indexOf('<form id="messageForm"');
  const formEnd = html.indexOf("</form>", formStart);
  const composer = html.slice(formStart, formEnd);

  const toolbarIndex = composer.indexOf('class="composer-toolbar"');
  const inputIndex = composer.indexOf('id="composerInputShell"');
  assert.ok(toolbarIndex >= 0 && toolbarIndex < inputIndex);
  assert.ok(composer.indexOf('id="headerTaskSummaryBtn"') > toolbarIndex && composer.indexOf('id="headerTaskSummaryBtn"') < inputIndex);
  assert.ok(composer.indexOf('id="modelSelect"') < composer.indexOf('id="reasoningEffort"'));
  assert.ok(composer.indexOf('id="reasoningEffort"') < composer.indexOf('id="openProviderLoginBtn"'));
  assert.ok(composer.indexOf('id="openProviderLoginBtn"') < composer.indexOf('id="permissionMode"'));
  assert.ok(composer.indexOf('id="permissionMode"') < inputIndex);
  assert.match(composer, /class="composer-field composer-model-field"/);
  assert.match(composer, /class="composer-field composer-effort-field"/);
  // The plan/execute toggle is gone: the permission menu carries message mode.
  assert.doesNotMatch(composer, /composer-message-mode-field|id="messageModeToggle"/);
  assert.match(composer, /class="composer-field composer-permission-field" data-project-context-only aria-hidden="true"/);
  assert.match(composer, /class="permission-safety-indicator hidden"[^>]*aria-hidden="true"/);
  assert.match(composer, /id="permissionRiskBadge" class="permission-risk-badge hidden" aria-hidden="true"/);
  assert.match(composer, /class="composer-actions" data-project-context-only aria-hidden="true"/);
  assert.match(composer, /id="composerFolderBtn"/);
  assert.doesNotMatch(composer, /id="composerTerminalBtn"/);
  assert.match(styles, /body\.white-shell\.theme-light:not\(\.project-operation-context\) \[data-project-context-only\]\s*\{[^}]*display:\s*none !important/);
  assert.match(styles, /body\.white-shell\.theme-light:not\(\.project-operation-context\) :is\(\.composer-actions, \.composer-message-mode-field, \.composer-permission-field\) \{ display: none !important; \}/);
  assert.doesNotMatch(appMain, /messageModeToggle/);
  assert.doesNotMatch(styles, /\.composer-field-label,\s*\.composer-actions,[\s\S]{0,180}\.composer-permission-field\s*\{ display: none !important; \}/);
  assert.match(appMain, /navigationSelectionKind:\s*"conversation"/);
  assert.match(appMain, /function syncProjectOperationContext\(\)/);
  assert.match(appMain, /selectionKind:\s*"project"/);
  assert.doesNotMatch(html, /id="currentMeta"/);
  assert.doesNotMatch(html, /id="wsBadge"/);
  assert.ok(composer.indexOf('id="messageText"') > toolbarIndex);
  assert.ok(composer.indexOf('id="sendMessageBtn"') > toolbarIndex);
  assert.match(composer, /<label class="composer-field-label" for="reasoningEffort" data-i18n="chat\.reasoningEffort">/);
  assert.match(composer, /id="reasoningEffort"[^>]*data-i18n-title="chat\.reasoningEffort"[^>]*data-i18n-aria-label="chat\.reasoningEffort"/);
  assert.match(composer, /id="reasoningEffortDisplay"[^>]*aria-hidden="true"/);
  assert.match(composer, /<label class="composer-field-label" for="modelSelect" data-i18n="chat\.model">/);
  assert.match(composer, /<label class="composer-field-label" for="permissionMode" data-i18n="chat\.permissionMode">/);
  assert.doesNotMatch(composer, /id="saveAgentBtn"/);
  assert.match(composer, /id="messageText"[^>]*class="[^"]*autosize-message-input/);
  assert.match(composer, /id="permissionRiskBadge"/);
  assert.match(composer, /id="sendMessageBtn"[^>]*data-i18n="chat\.send"[^>]*>发送<\/button>/);
});

test("lightning control is a capability-gated Fast mode toggle", async () => {
  const [html, styles, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appMainURL, "utf8"),
  ]);
  assert.match(html, /styles\.css\?v=[^"]*lightning-icon-1/);
  assert.match(html, /id="openProviderLoginBtn"[^>]*class="[^"]*toolbar-lightning-btn[^"]*hidden[^"]*"[^>]*aria-pressed="false"[^>]*data-i18n-title="chat\.fastModeDisabled"/);
  assert.match(appMain, /openProviderLoginBtn"\)\?\.addEventListener\("click", \(\) => toggleFastMode\(\)\.catch\(showError\)\)/);
  assert.doesNotMatch(appMain, /openProviderLoginBtn"\)\.addEventListener\("click", \(\) => openSettingsModal\("providers"\)\)/);
  assert.match(styles, /\.toolbar-lightning-btn:not\(\.hidden\)\s*\{[\s\S]*?border:\s*0;[\s\S]*?background:\s*transparent;[\s\S]*?box-shadow:\s*none/);
  assert.match(styles, /\.toolbar-lightning-btn\.fast-mode-active\s*\{[\s\S]*?border:\s*0;[\s\S]*?background:\s*transparent;[\s\S]*?box-shadow:\s*none/);
  assert.match(styles, /\.toolbar-lightning-btn\.fast-mode-active svg\s*\{[\s\S]*?fill:\s*currentColor/);
});

test("permission mode display targets only the permission toolbar pill", async () => {
  const appMain = await readFile(appMainURL, "utf8");
  assert.match(appMain, /querySelector\("\.permission-toolbar-pill \.mode-display"\)/);
  assert.doesNotMatch(appMain, /querySelector\("\.mode-display"\)/);
});

test("chat header exposes the legacy six-tool order with real SVG icons", async () => {
  const [html, appMain] = await Promise.all([readFile(indexURL, "utf8"), readFile(appMainURL, "utf8")]);
  const headerStart = html.indexOf('<header class="chat-header">');
  const headerEnd = html.indexOf("</header>", headerStart);
  const header = html.slice(headerStart, headerEnd);
  const expected = [
    "workspaceExplorerBtn",
    "gitWorkflowBtn",
    "specBoardBtn",
    "runtimeStatusBtn",
    "toggleTerminalBtn",
    "workspacePreviewBtn",
  ];
  const positions = expected.map((id) => header.indexOf(`id="${id}"`));
  assert.ok(positions.every((position) => position >= 0));
  assert.deepEqual([...positions].sort((a, b) => a - b), positions);
  assert.equal((header.match(/<svg viewBox="0 0 24 24"/g) || []).length >= expected.length, true);
  assert.match(appMain, /runtimeStatusBtn[\s\S]*openConversationDetails\(\)/);
  assert.match(html, /id="terminalCommandForm"/);
  assert.match(html, /id="terminalCommandInput"/);
});

test("background tasks share the right utility column instead of overlaying chat", async () => {
  const [html, styles, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appMainURL, "utf8"),
  ]);
  const detailsPanel = html.indexOf('id="conversationDetailsPanel"');
  const taskPanel = html.indexOf('id="backgroundTaskTray"');
  assert.ok(detailsPanel >= 0 && taskPanel > detailsPanel);
  assert.match(html, /id="backgroundTaskTray" class="utility-panel background-task-panel hidden"/);
  assert.doesNotMatch(html, /id="backgroundTaskTray" class="background-task-tray/);
  assert.match(styles, /\.app-shell\.background-tasks-open/);
  assert.match(styles, /\.background-task-panel-body\s*\{[\s\S]*?flex:\s*1;[\s\S]*?overflow:\s*hidden/);
  assert.match(styles, /\.background-task-tray-grid\s*\{[\s\S]*?grid-template-rows:/);
  assert.match(appMain, /onOpenChange:\s*\(open\)\s*=>\s*\{[\s\S]*?classList\.toggle\("background-tasks-open", open\)/);
  assert.match(appMain, /function openConversationDetails\(\)\s*\{[\s\S]*?backgroundTasks\.closeTray\("details-open"\)/);
});

test("browser preview dock compacts both control rows to preserve page space", async () => {
  const [html, styles] = await Promise.all([readFile(indexURL, "utf8"), readStylesSource(stylesURL)]);
  const dockStart = styles.indexOf("body.white-shell.theme-light .workspace-preview-dock-mode {");
  const dockEnd = styles.indexOf("@media (min-width: 1280px)", dockStart);
  const dock = styles.slice(dockStart, dockEnd);
  assert.match(dock, /\.workspace-preview-dock-mode \.workspace-modal-head\s*\{[^}]*position:\s*absolute;[^}]*right:\s*[^;]+;[^}]*display:\s*block;[^}]*border:\s*0;/);
  const hiddenChromeRule = dock.match(/\.workspace-preview-dock-mode \.workspace-modal-title,[^{}]*\{[^}]*\}/)?.[0] || "";
  assert.match(hiddenChromeRule, /\.workspace-preview-dock-mode \.workspace-modal-title/);
  assert.match(hiddenChromeRule, /\.workspace-preview-dock-mode #workspaceFilesTab/);
  assert.match(hiddenChromeRule, /\.workspace-preview-dock-mode \.workspace-tabs/);
  assert.match(hiddenChromeRule, /display:\s*none/);
  assert.match(dock, /\.workspace-preview-dock-mode \.workspace-modal-heading\s*\{\s*display:\s*none;/);
  assert.match(dock, /\.workspace-preview-dock-mode \.workspace-preview-toolbar\s*\{[^}]*flex:\s*0 0 auto;[^}]*flex-wrap:\s*wrap;/);
  assert.match(dock, /\.workspace-preview-dock-mode \.workspace-preview-status\s*\{\s*display:\s*none;/);
  assert.match(dock, /\.workspace-preview-dock-mode \.workspace-preview-field\s*\{\s*display:\s*none;/);
  assert.match(dock, /\.workspace-browser-address\s*\{[^}]*flex:\s*1 1 [^;]+;[^}]*min-width:/);
  const viewportMenu = html.match(/<details\b(?=[^>]*\bclass="[^"]*\bworkspace-viewport-menu\b)[^>]*>[\s\S]*?<\/details>/)?.[0] || "";
  assert.match(viewportMenu, /<summary\b[^>]*role="button"/);
  assert.match(viewportMenu, /data-preview-viewport="adaptive"/);
});

test("desktop conversation layout follows the compact resizable geometry", async () => {
  const [html, styles, appMain, chatRendering, navigation] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appMainURL, "utf8"),
    readFile(chatRenderingURL, "utf8"),
    readFile(new URL("./conversation-navigation.mjs", import.meta.url), "utf8"),
  ]);
  const finalDesktopComposer = styles.slice(styles.indexOf("/* Final desktop full-width composer override. */"));
  assert.match(styles, /grid-template-columns:\s*76px var\(--session-sidebar-width\) minmax\(420px, 1fr\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.sidebar-resize-handle\s*\{[\s\S]*?position:\s*fixed[\s\S]*?left:\s*calc\(68px \+ var\(--session-sidebar-width\) - 3px\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.chat-panel\s*\{[\s\S]*?grid-column:\s*3/);
  assert.match(styles, /body\.white-shell\.theme-light \.terminal-panel\s*\{[\s\S]*?grid-column:\s*4/);
  assert.match(styles, /body\.white-shell\.theme-light \.session-sidebar-header\s*\{[^}]*flex:\s*0 0 64px[^}]*height:\s*64px[^}]*min-height:\s*64px/);
  assert.match(html, /styles\.css\?v=[^"\n]*header-divider-align-1/);
  assert.match(styles, /body\.white-shell\.theme-light \.composer-wrap\s*\{[\s\S]*?padding:\s*6px 12px 8px/);
  assert.match(styles, /body\.white-shell\.theme-light \.message-input\s*\{[\s\S]*?min-height:\s*40px/);
  assert.match(styles, /body\.white-shell\.theme-light \.composer-send-btn\s*\{[\s\S]*?width:\s*34px/);
  assert.match(styles, /body\.white-shell\.theme-light \.navigation-project-row \.navigation-project-title\s*\{[\s\S]*?display:\s*flex[\s\S]*?gap:\s*6px/);
  assert.match(styles, /body\.white-shell\.theme-light \.navigation-project-row \.project-kind-badge\s*\{[\s\S]*?background:\s*var\(--ws-primary-soft\)[\s\S]*?font-size:\s*8px/);
  assert.match(styles, /body\.white-shell\.theme-light \.navigation-conversation-row\s*\{[\s\S]*?min-height:\s*42px[\s\S]*?grid-template-columns:\s*14px minmax\(0, 1fr\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.navigation-project-group \+ \.navigation-project-group\s*\{[\s\S]*?margin-top:\s*2px/);
  assert.match(styles, /body\.white-shell\.theme-light \.messages:not\(\.empty\)\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\)[\s\S]*?grid-auto-rows:\s*max-content[\s\S]*?justify-content:\s*start[\s\S]*?row-gap:\s*14px[\s\S]*?padding:\s*14px 16px 14px/);
  assert.match(styles, /body\.white-shell\.theme-light \.messages:not\(\.empty\) > \[class~="message"\]\s*\{[^}]*justify-self:\s*stretch[^}]*width:\s*100%[^}]*max-width:\s*100%[^}]*white-space:\s*normal/);
  assert.match(styles, /body\.white-shell\.theme-light \.messages:not\(\.empty\) > \[class~="message"\]\[class~="user"\]\[class~="chat-flow-left"\]\s*\{[^}]*justify-self:\s*start[^}]*align-self:\s*start[^}]*width:\s*100%[^}]*min-width:\s*126px[^}]*max-width:\s*100%[^}]*height:\s*fit-content[^}]*margin:\s*0;[^}]*background:\s*var\(--ws-primary-soft\)[^}]*color:\s*var\(--ws-text\)/);
  assert.match(styles, /\[class~="message"\]\[class~="user"\]\[class~="chat-flow-left"\] \.message-head-actions\s*\{[^}]*position:\s*absolute[^}]*display:\s*flex/);
  assert.match(styles, /\[class~="message"\]\[class~="user"\]\[class~="chat-flow-left"\] \.message-copy-btn\s*\{[^}]*width:\s*20px[^}]*font-size:\s*0/);
  assert.match(styles, /@media \(max-width:\s*760px\)\s*\{[\s\S]*?\[class~="message"\]\[class~="user"\]\[class~="chat-flow-left"\]\s*\{[^}]*width:\s*fit-content[^}]*max-width:\s*92%[^}]*margin-left:\s*0/);
  assert.match(styles, /\[class~="message"\]\[class~="user"\]\[class~="chat-flow-left"\]\[class~="message-editing"\]\s*\{[^}]*justify-self:\s*stretch[^}]*width:\s*100%[^}]*max-width:\s*100%[^}]*background:\s*var\(--ws-card\)/);
  assert.match(styles, /\[class~="message"\]:not\(\[class~="live-assistant-message"\]\) \.message-head\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\) auto max-content/);
  assert.match(styles, /\[class~="message"\]:not\(\[class~="live-assistant-message"\]\) \.message-time\s*\{[^}]*grid-column:\s*3[^}]*justify-self:\s*end/);
  assert.match(styles, /\.message-editing \.message-correction-text\s*\{[\s\S]*?border-radius:\s*7px[\s\S]*?background:\s*var\(--ws-input\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.messages:not\(\.empty\) > \[class~="run-summary-card"\]\s*\{[\s\S]*?justify-self:\s*stretch[\s\S]*?width:\s*100%/);
  assert.match(styles, /@media \(max-width:\s*767px\)\s*\{[\s\S]*?body\.white-shell\.theme-light \.messages \[class~="run-summary-card"\]\s*\{[^}]*display:\s*none/);
  assert.match(styles, /\.project-run-failure\s*\{[^}]*width:\s*100%[^}]*max-width:\s*100%/);
  assert.match(styles, /\.run-summary-failure-alert\s*\{[^}]*font-size:\s*12px/);
  assert.match(finalDesktopComposer, /\[class~="toolbar-model-pill"\],[\s\S]*?\[class~="model-tool-btn"\]\[class~="icon-only"\]\s*\{[\s\S]*?border-radius:\s*6px/);
  assert.match(finalDesktopComposer, /textarea#messageText\s*\{[\s\S]*?border-radius:\s*7px/);
  assert.match(finalDesktopComposer, /#sendMessageBtn\s*\{[\s\S]*?border-radius:\s*7px/);
  assert.match(styles, /\.sidebar-resize-handle\s*\{[\s\S]*?cursor:\s*col-resize/);
  assert.match(html, /id="sidebarResizeHandle"[^>]*role="separator"[^>]*aria-valuemin="184"[^>]*aria-valuemax="420"/);
  assert.match(html, /id="sessionSidebarCompactTitle" class="session-sidebar-compact-title"/);
  assert.match(styles, /--global-rail-layout-width:\s*68px/);
  assert.match(styles, /\.app-shell\.global-rail-collapsed\s*\{[\s\S]*?--global-rail-layout-width:\s*48px/);
  assert.match(styles, /\.app-shell\.session-sidebar-collapsed\s*\{[\s\S]*?--session-sidebar-layout-width:\s*184px/);
  assert.match(styles, /\.app-shell\.session-sidebar-collapsed \.navigation-conversation-row[\s\S]*?min-height:\s*48px/);
  assert.match(navigation, /data-theme-icon-slot="sidebar-project"/);
  assert.match(navigation, /data-theme-icon-slot="sidebar-conversation"/);
  assert.match(appMain, /bindSidebarResizer\(\)/);
  assert.match(styles, /\.sidebar-search-wrap\.hidden\s*\{[\s\S]*?display:\s*block !important/);
  assert.match(html, /id="messages" class="messages empty" data-initial-chat-state="loading" aria-busy="true"/);
  assert.match(html, /workspace\.main\.loadingProjectTitle/);
  assert.doesNotMatch(html, /id="messages"[\s\S]{0,500}data-i18n="chat\.emptyTitle"/);
  assert.match(appMain, /resolveInitialNavigationTarget\(state\.recentConversations, state\.navigationConversations\)/);
  assert.match(appMain, /createNavigationRefreshController\(\{[\s\S]*?refresh:\s*\(\) => loadProjects\(\)[\s\S]*?visibilityState !== "hidden"/);
  assert.match(appMain, /createRecentConversationSyncController\(\{[\s\S]*?key:\s*recentConversationsKey[\s\S]*?state\.recentConversations = recent[\s\S]*?renderRecentSidebarConversations\(\)/);
  assert.match(navigation, /addEventListener\("storage", handleStorage\)/);
  assert.match(appMain, /beforeunload[\s\S]*?recentConversationSync\.stop\(\)/);
  assert.match(appMain, /navigationRefresh\.request\(event\.type\)/);
  assert.match(appMain, /navigationRefresh\.start\(\)/);
  assert.match(appMain, /syncNavigationConversationFromAgent\(state\.agent/);
  assert.match(appMain, /preserveMessageState:\s*true/);
  assert.match(appMain, /navigationTransitionTitle/);
  assert.match(appMain, /const preserveConversationView = Boolean\(state\.agent\?\.id\)/);
  assert.match(appMain, /clearLiveAssistantText\(\{ preserveView: true \}\)/);
  assert.match(appMain, /clearRunSummary\(\{ preserveView: true \}\)/);
  assert.match(appMain, /function markMessageViewportBusy\(options = \{\}\)[\s\S]*?dataset\.initialChatState = "loading"/);
  assert.match(appMain, /messageViewportBusyDelayMs = 140/);
  assert.match(appMain, /clearMessageViewportBusyTimer\(\)/);
  assert.match(appMain, /selectNavigationConversation[\s\S]*?markMessageViewportBusy\(\)/);
  assert.doesNotMatch(appMain, /conversationOpeningTitle/);
  assert.match(chatRendering, /state\.chatHydrating && options\.forceRender !== true/);
  assert.match(chatRendering, /function clearRunSummary\(\{ preserveView = false \} = \{\}\)/);
  assert.match(chatRendering, /if \(html\) existing\.outerHTML = html/);
  assert.match(chatRendering, /if \(state\.runSummaryLoading\) return;/);
  assert.match(chatRendering, /if \(!state\.runSummaryError \|\| state\.runSummaryLoading\) return ""/);
  assert.doesNotMatch(chatRendering, /runStatusLabel\(status\)\}\)\}\$\{state\.runSummaryLoading/);
  assert.match(chatRendering, /function renderLiveAssistantCard\(\{ preserveView = false \} = \{\}\)/);
  assert.match(styles, /\.messages\[data-context-switching="true"\]::before[\s\S]*?background: color-mix\(in srgb, var\(--bg\) 34%, transparent\)/);
  assert.match(styles, /content: attr\(data-switching-label\) " …"/);
  assert.match(styles, /@keyframes message-context-pulse/);
  assert.match(chatRendering, /class="empty-conversation-state"/);
});

test("composer selects hide external labels and open titled menus upward", async () => {
  const [html, styles, uiShell, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(uiShellURL, "utf8"),
    readFile(appMainURL, "utf8"),
  ]);
  for (const id of ["modelSelect", "reasoningEffort", "permissionMode"]) {
    assert.match(html, new RegExp(`data-composer-select="${id}"`));
  }
  assert.match(styles, /\.composer-native-select\s*\{[\s\S]*?clip-path:\s*inset\(50%\)/);
  assert.match(styles, /\.composer-select-popover\s*\{[\s\S]*?position:\s*fixed/);
  assert.match(styles, /\.composer-select-popover-title\s*\{/);
  assert.match(styles, /\.composer-select-popover\.composer-permission-popover\s*\{/);
  assert.match(styles, /\.composer-permission-option-icon svg\s*\{/);
  assert.match(styles, /\.composer-permission-safety-status\s*\{/);
  assert.match(uiShell, /heading\.textContent = binding\.label\?\.textContent/);
  assert.match(uiShell, /menu\.classList\.toggle\("composer-permission-popover", isPermissionMenu\)/);
  assert.match(uiShell, /appendPermissionSafetyStatus\(/);
  assert.match(uiShell, /appendMessageModeSection\(/);
  assert.match(uiShell, /usesMobileSheet[\s\S]*permissionMode/);
  assert.doesNotMatch(uiShell, /messageModeToggle/);
  assert.match(uiShell, /chat\.enterPlanMode/);
  assert.match(uiShell, /chat\.executeMode/);
  assert.match(uiShell, /menu\.style\.bottom = `\$\{Math\.max\(8,[\s\S]*?- rect\.top \+ 6\)\}px`/);
  assert.match(uiShell, /binding\.select\.dispatchEvent\(new EventConstructor\("change"/);
  assert.match(uiShell, /appendModelOptionGroups\(binding, menu\)/);
  assert.match(uiShell, /presentation\?\.provider \? `\$\{presentation\.provider\}:\$\{presentation\.name\}`/);
  assert.match(uiShell, /!active\.mobile && \(event\.target === menu \|\| menu\.contains\(event\.target\)\)/);
  assert.match(styles, /\.composer-select-popover\s*\{[\s\S]*?overscroll-behavior:\s*contain/);
  assert.match(styles, /\.composer-model-group-heading\s*\{/);
  assert.match(appMain, /agentSavePromise:\s*null/);
  assert.match(appMain, /state\.agentSaveSnapshot = captureAgentSettingsSnapshot\(\);[\s\S]*?while \(state\.agentSavePending\)/);
  assert.match(appMain, /awaitAgentSettingsSaved:\s*\(agentId\) => waitForAgentSettingsSave\(agentId\)/);
});

test("permission menu groups the real modes in figure-two order", () => {
  const options = [
    { value: "readOnly" },
    { value: "acceptEdits" },
    { value: "bypassPermissions" },
    { value: "default" },
    { value: "dontAsk" },
  ];
  assert.deepEqual(permissionMenuPrimaryValues, ["default", "acceptEdits", "bypassPermissions"]);
  assert.deepEqual(permissionMenuSecondaryValues, ["readOnly", "dontAsk"]);
  assert.deepEqual(orderPermissionMenuOptions(options).map((option) => option.value), [
    "default",
    "acceptEdits",
    "bypassPermissions",
    "readOnly",
    "dontAsk",
  ]);
});

test("desktop composer uses the full chat width without centered side gutters", async () => {
  const styles = await readStylesSource(stylesURL);
  const marker = "/* Final desktop full-width composer override. */";
  const desktopComposerStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Compact mobile composer", styles.indexOf(marker)));
  assert.ok(desktopComposerStyles.startsWith(marker));
  assert.match(desktopComposerStyles, /\[class~="composer-card"\]\s*\{[^}]*width:\s*100%;[^}]*max-width:\s*none;[^}]*margin:\s*0;/);
  assert.doesNotMatch(desktopComposerStyles, /\[class~="composer-card"\]\s*\{[^}]*margin:\s*0 auto/);
  assert.match(desktopComposerStyles, /\[class~="composer-toolbar"\]\s*\{[^}]*justify-content:\s*flex-start;/);
  const taskSummaryRule = desktopComposerStyles.match(/\[class~="composer-task-summary"\]\s*\{[^}]*\}/)?.[0] || "";
  assert.match(taskSummaryRule, /flex:\s*0 1 [^;]+;/);
  assert.match(taskSummaryRule, /grid-template-columns:/);
  assert.doesNotMatch(taskSummaryRule, /margin-right:\s*auto/);
  assert.match(desktopComposerStyles, /\[class~="composer-controls"\]\s*\{[^}]*flex:\s*0 1 auto;[^}]*flex-wrap:\s*nowrap;/);
  const visibleActionsRule = desktopComposerStyles.match(/\[class~="toolbar-lightning-btn"\],[^{}]*\[class~="composer-actions"\]\s*\{[^}]*\}/)?.[0] || "";
  assert.match(visibleActionsRule, /display:\s*flex;/);
  assert.match(desktopComposerStyles, /textarea#messageText\s*\{[^}]*min-height:\s*var\(--composer-input-min-height\);[^}]*max-height:\s*var\(--composer-input-max-height\)/);
  assert.match(desktopComposerStyles, /#sendMessageBtn\s*\{[^}]*flex:\s*0 0 [^;]+;/);
});

test("composer task activity is borderless, left aligned, and spins blue while active", async () => {
  const [html, styles, backgroundTasks, agentWorkspaceHelpers, chatRendering, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(backgroundTasksURL, "utf8"),
    readFile(agentWorkspaceHelpersURL, "utf8"),
    readFile(chatRenderingURL, "utf8"),
    readFile(appMainURL, "utf8"),
  ]);
  const marker = "/* Minimal left-aligned task activity, matching the inline running indicator. */";
  const indicatorStyles = styles.slice(styles.indexOf(marker));
  assert.match(html, /id="headerCurrentTaskText"[^>]*aria-live="polite"[^>]*aria-atomic="true"/);
  assert.ok(indicatorStyles.startsWith(marker));
  const taskSummaryRule = indicatorStyles.match(/\.composer-task-summary\s*\{[^}]*\}/)?.[0] || "";
  assert.match(taskSummaryRule, /flex:\s*0 1 [^;]+;/);
  assert.match(taskSummaryRule, /padding:\s*0;/);
  assert.match(taskSummaryRule, /border:\s*0;/);
  assert.match(taskSummaryRule, /background:\s*transparent;/);
  assert.doesNotMatch(taskSummaryRule, /margin-right:\s*auto/);
  assert.match(indicatorStyles, /\.composer-task-summary\.has-task[^\{]*\{[^}]*color:\s*var\(--ws-primary/);
  assert.match(indicatorStyles, /\.header-task-status-dot\.running,[\s\S]*?\.header-task-status-dot\.queued[\s\S]*?border-top-color:\s*var\(--ws-primary[\s\S]*?animation:\s*composer-task-indicator-spin/);
  assert.match(indicatorStyles, /@keyframes composer-task-indicator-spin[\s\S]*?rotate\(360deg\)/);
  assert.match(backgroundTasks, /function setForegroundActivity[\s\S]*?foregroundActivity = next;[\s\S]*?render\(\)/);
  assert.match(backgroundTasks, /const currentText = foregroundActivity\?\.text \|\| summary\.current\?\.title/);
  assert.match(backgroundTasks, /headerButton\.classList\.toggle\("has-task", hasCurrentActivity\)/);
  assert.match(backgroundTasks, /headerQueue\.classList\.toggle\("hidden", summary\.queuedCount <= 0\)/);
  assert.match(agentWorkspaceHelpers, /routeActivityToTaskSummary[\s\S]*?projectOperationContextActive\?\.\(\) && !isMobileAppViewport\?\.\(\)[\s\S]*?backgroundTasks\.setForegroundActivity\(activity\)/);
  assert.match(appMain, /createAgentWorkspaceHelpers\(\{[\s\S]*?projectOperationContextActive,[\s\S]*?isMobileAppViewport,/);
  assert.match(appMain, /window\.addEventListener\("resize"[\s\S]*?refreshComposerActivityStatus\(\)/);
  assert.match(chatRendering, /function renderLiveAssistantCardHTML\(\)[\s\S]*?if \(!text\) return ""/);
  assert.doesNotMatch(chatRendering, /class="live-assistant-(?:waiting|status)"/);
});

test("Subagent compact cards integrate background tasks without polling child tool calls", async () => {
  const [html, app, appMain, chatRendering, messages, styles, backgroundTasks] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(chatRenderingURL, "utf8"),
    readFile(chatRenderingMessagesURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(backgroundTasksURL, "utf8"),
  ]);

  assert.match(html, /styles\.css\?v=[^"\n]*subagent-cards-1/);
  assert.match(html, /app\.js\?v=[^"\n]*subagent-cards-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*subagent-cards-1/);
  assert.match(appMain, /background-tasks\.mjs\?v=subagent-cards-1/);
  assert.match(appMain, /chat-rendering\.mjs\?v=[^"\n]*subagent-cards-1/);
  assert.match(chatRendering, /messages-chat-rendering-extra\.mjs\?v=[^"\n]*subagent-cards-1/);

  assert.match(appMain, /resolveBackgroundTask:\s*\(tool\)\s*=>\s*backgroundTasks\?\.getTaskByParentTool\?\.\(tool\?\.runId, tool\?\.toolUseId\)/);
  assert.match(chatRendering, /options\.resolveBackgroundTask\(tool\)/);
  assert.match(chatRendering, /task === null \|\| task === undefined\) return \{ ok: true, task: embeddedAgentBackgroundTaskHandle\(item, tool\) \}/);
  assert.match(backgroundTasks, /function associationKey\(parentRunId, parentToolUseId\)[\s\S]*?return runId && toolUseId \? JSON\.stringify\(\[runId, toolUseId\]\)/);
  assert.match(backgroundTasks, /function getTaskByParentTool\(parentRunId, parentToolUseId\)/);

  const enterAgent = appMain.slice(appMain.indexOf("async function enterAgent"), appMain.indexOf("function showModelSetupNotice"));
  assert.match(enterAgent, /state\.chatHydrating = true;[\s\S]*?backgroundTasks\.setAgent\(agentId\)/);
  assert.match(enterAgent, /loadBackgroundTasksForAgent\(agentId\)/);
  assert.match(backgroundTasks, /background-tasks\?limit=100/);
  assert.match(backgroundTasks, /if \(!alreadyLoaded\) await loadTask\(normalized\)/);
  assert.match(backgroundTasks, /lifecycleRefresh[\s\S]*?hydrateTask\(current\.id, \{ force: lifecycleRefresh \}\)/);

  // The card coordinator itself lives in subagent-cards.mjs and is covered by
  // behaviour tests in subagent-cards.test.mjs (identity keyed on run/tool
  // rather than position, in-place replacement without re-rendering, the
  // refresh-reason allowlist excluding output events, the selection-sequence
  // staleness guard, view-state and focus restoration, and action routing).
  // What stays asserted here is only the entry point's side of the contract.
  assert.match(appMain, /createSubagentCardCoordinator\(\{[\s\S]*?getBackgroundTasks: \(\) => backgroundTasks,/);
  assert.match(appMain, /backgroundTasks\.subscribe\?\.\(subagentCards\.scheduleRefresh\)/);
  assert.match(appMain, /subagentCards\.bindCardActions\(\)/);
  assert.match(appMain, /onNavigateAgent: \(childAgentId\) => \{[\s\S]*?subagentCards\.navigateToAgent\(childAgentId\)\.catch\(showError\)/);
  assert.match(appMain, /onNavigateRun: \(childAgentId, childRunId\) => \{[\s\S]*?subagentCards\.navigateToRun\(childAgentId, childRunId\)\.catch\(showError\)/);
  for (const action of ["view-task", "cancel", "open-agent", "open-run"]) {
    assert.match(chatRendering, new RegExp(`data-subagent-action="${action}"`));
  }
  assert.match(chatRendering, /activity\.childAgentId && activity\.childRunId/);
  assert.match(chatRendering, /data-subagent-status=/);
  assert.match(chatRendering, /role="status" aria-live="polite"/);

  assert.equal((messages.match(/subagent:\s*Object\.freeze/g) || []).length, 3);
  for (const key of ["title", "roleAuto", "modelAuto", "requestedModel", "waitingTaskInfo", "auditDetails", "failure", "action", "status"]) {
    assert.match(messages, new RegExp(`${key}:`));
  }
  assert.match(messages, /dispatched:\s*"已派发"/);
  assert.match(messages, /succeeded:\s*"子任务已完成"/);
  assert.match(messages, /requestedModel:\s*"请求模型：\{model\}"/);
  assert.match(messages, /modelAuto:\s*"自动选择"/);
  assert.match(messages, /queued:\s*"子任务已排队"/);
  assert.match(messages, /waiting_approval:\s*"后台任务等待审批"/);
  assert.match(messages, /waiting_approval:\s*"背景任務等待核准"/);
  assert.match(messages, /waiting_approval:\s*"Background task awaiting approval"/);

  const marker = "/* Compact Subagent task cards: semantic, responsive, and isolated from generic tools. */";
  const cardStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Background tasks and auto-continuation */", styles.indexOf(marker)));
  assert.ok(cardStyles.startsWith(marker));
  assert.match(chatRendering, /class="[^"]*subagent-task-card/);
  assert.match(cardStyles, /\.subagent-task-card\.status-completed \.tool-activity-icon::before \{ content: "✓"; \}/);
  assert.match(cardStyles, /\.subagent-task-card\.status-warn \.tool-activity-icon::before,[\s\S]*?content: "!"/);
  assert.match(cardStyles, /\.subagent-task-card :is\(summary, button\):focus-visible/);
  assert.match(cardStyles, /@media \(max-width: 760px\)[\s\S]*?\.subagent-task-actions \.ghost-btn\.mini/);
  assert.doesNotMatch(cardStyles, /(?:^|\n)\s*\.tool-activity-card\s*\{/);
});

test("composer responds to its actual width before the mobile breakpoint", async () => {
  const [html, styles] = await Promise.all([readFile(indexURL, "utf8"), readStylesSource(stylesURL)]);
  assert.match(html, /styles\.css\?v=[^"]*composer-responsive-2/);
  assert.match(html, /styles\.css\?v=[^"]*composer-effort-compact-2/);
  const marker = "/* Responsive composer tiers follow the editor's real width, not the full viewport. */";
  const responsiveStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Flat, single-pass settings layout", styles.indexOf(marker)));
  assert.ok(responsiveStyles.startsWith(marker));
  assert.match(responsiveStyles, /\.composer-wrap\s*\{[^}]*container-name:\s*composer-shell[^}]*container-type:\s*inline-size/);
  assert.match(responsiveStyles, /\.composer-controls\s*\{[^}]*align-items:\s*center/);
  assert.match(responsiveStyles, /\.composer-select-value\s*\{[^}]*flex:\s*1 1 auto/);
  assert.match(responsiveStyles, /\.composer-effort-field\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /\.effort-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*padding:\s*0/);
  assert.match(responsiveStyles, /\.composer-effort-field \.composer-select-trigger\s*\{[^}]*width:\s*auto[^}]*justify-content:\s*center[^}]*gap:\s*4px[^}]*padding:\s*0 4px/);
  assert.match(responsiveStyles, /\.composer-effort-field \.composer-select-value\s*\{[^}]*flex:\s*0 1 auto/);
  assert.match(responsiveStyles, /\.toolbar-lightning-btn:not\(\.hidden\)\s*\{[^}]*align-self:\s*center[^}]*align-items:\s*center[^}]*justify-content:\s*center/);
  assert.match(responsiveStyles, /\.composer-permission-field\s*\{[^}]*flex:\s*0 0 96px/);
  // The pill sizes to its longest label instead of a fixed width, so 全部允許
  // is never ellipsised, and carries no padding of its own so the trigger
  // fills it and the whole control is clickable.
  assert.match(responsiveStyles, /\.permission-toolbar-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*max-content[^}]*padding:\s*0/);
  assert.match(responsiveStyles, /\.permission-toolbar-pill \.composer-select-trigger\s*\{[^}]*width:\s*100%/);
  assert.match(responsiveStyles, /\.permission-toolbar-pill \.composer-select-value\s*\{[^}]*text-overflow:\s*clip/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*1320px\)[\s\S]*?\.composer-task-summary\s*\{[^}]*width:\s*180px[^}]*max-width:\s*180px[^}]*flex:\s*0 1 180px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-task-summary\s*\{[^}]*width:\s*30px[^}]*flex:\s*0 0 30px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-model-field\s*\{[^}]*min-width:\s*140px[^}]*max-width:\s*200px[^}]*flex:\s*1 1 160px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-effort-field\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.effort-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.effort-pill\s*\{[^}]*width:\s*auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.composer-select-value\s*\{[^}]*font-size:\s*11px[^}]*text-overflow:\s*clip/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.composer-select-value::before\s*\{[^}]*content:\s*none/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-permission-field\s*\{[^}]*flex:\s*0 0 92px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.permission-toolbar-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*max-content/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.message-mode-option::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.permission-safety-indicator\s*\{[^}]*display:\s*none/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-model-field\s*\{[^}]*max-width:\s*88px[^}]*flex:\s*1 1 62px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-permission-field \.composer-select-value::before\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
});

test("mobile header and composer use compact icon-first layouts", async () => {
  const [html, styles, appMain, app] = await Promise.all([readFile(indexURL, "utf8"), readStylesSource(stylesURL), readFile(appMainURL, "utf8"), readFile(appURL, "utf8")]);
  const marker = "/* Compact mobile composer: one utility row plus one message row. */";
  const mobileComposerStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Model provider settings.", styles.indexOf(marker)));
  assert.ok(mobileComposerStyles.startsWith(marker));
  assert.match(html, /styles\.css\?v=[^"]*mobile-short-labels-1/);
  assert.match(html, /app\.js\?v=[^"]*mobile-short-labels-1/);
  assert.match(app, /app-main\.mjs\?v=[^"]*mobile-short-labels-1/);
  assert.match(html, /id="mobileSearchBtn"[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.match(html, /id="composerFolderBtn"[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.doesNotMatch(html, /id="composerTerminalBtn"/);
  assert.match(html, /data-composer-select="modelSelect"[\s\S]*?class="composer-select-icon"[\s\S]*?id="modelSelectDisplay"[^>]*data-mobile-label="模型"/);
  // The effort trigger has no icon any more: the sparkle was removed, and mobile
  // shows a single-letter label instead.
  assert.match(html, /data-composer-select="reasoningEffort"[\s\S]*?id="reasoningEffortDisplay"[^>]*data-mobile-label="A"/);
  assert.doesNotMatch(html, /data-composer-select="reasoningEffort"[^>]*>\s*<span class="composer-select-icon"/);
  assert.match(styles, /\.composer-effort-field \.composer-select-value::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(html, /data-composer-select="permissionMode"[\s\S]*?class="composer-select-icon"[\s\S]*?data-mobile-label="RW"/);
  assert.match(html, /id="securityModeBadge"[^>]*data-mobile-label="LAN"/);
  assert.doesNotMatch(html, /id="(?:remoteSecurityBanner|workbenchRemoteSecurityBanner)"/);
  assert.doesNotMatch(appMain, /\$\("(?:remoteSecurityBanner|workbenchRemoteSecurityBanner)"\)/);
  assert.doesNotMatch(html, /data-message-mode=/);
  // Mobile keeps the permission control as a bare shield; its menu also carries
  // the message-mode options.
  assert.match(styles, /\.composer-permission-field\s*\{[^}]*display:\s*flex !important/);
  assert.match(styles, /\.composer-permission-field \.composer-select-icon\s*\{[^}]*display:\s*inline-flex !important/);
  assert.match(styles, /\.mobile-permission-sheet \.composer-permission-option/);
  assert.match(styles, /\.composer-message-mode-section-title/);
  assert.match(html, /id="sendMessageBtn"[^>]*data-mobile-label="↑"/);
  assert.equal(compactComposerModelLabel("cliproxyapi:claude-sonnet-4-6"), "sonnet");
  assert.equal(compactComposerModelLabel("codex:gpt-5.5"), "gpt-5.5");
  assert.equal(compactComposerModelLabel("openai:gpt-4-1-mini"), "gpt-4.1");
  assert.match(appMain, /readOnly:\s*"RO"[\s\S]*?acceptEdits:\s*"RW"[\s\S]*?bypassPermissions:\s*"ALL"/);
  assert.match(appMain, /connection\.restricted \? "T−" : "T\+"/);
  assert.match(mobileComposerStyles, /\[class~="mobile-update-pill"\][\s\S]*?display:\s*none !important/);
  assert.match(mobileComposerStyles, /\[class~="mobile-topbar"\][\s\S]*?height:\s*56px/);
  assert.match(mobileComposerStyles, /\[class~="composer-card"\][\s\S]*?gap:\s*6px[\s\S]*?border:\s*0/);
  assert.match(mobileComposerStyles, /\[class~="composer-toolbar"\][\s\S]*?justify-content:\s*flex-end/);
  assert.match(mobileComposerStyles, /\[class~="composer-controls"\][\s\S]*?flex:\s*0 1 auto[\s\S]*?justify-content:\s*flex-end[\s\S]*?margin-left:\s*auto/);
  assert.match(mobileComposerStyles, /\[class~="composer-model-field"\][\s\S]*?width:\s*96px[\s\S]*?flex:\s*0 1 96px/);
  assert.match(mobileComposerStyles, /\[class~="composer-select-icon"\]\s*\{[^}]*display:\s*inline-flex/);
  assert.match(mobileComposerStyles, /\[class~="composer-model-field"\] \[class~="composer-select-value"\]::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(mobileComposerStyles, /\[class~="message-mode-option"\]::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(mobileComposerStyles, /\[class~="composer-permission-field"\][\s\S]*?\[class~="composer-select-value"\]::before\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(mobileComposerStyles, /\[class~="permission-safety-indicator"\],[\s\S]*?display:\s*none !important/);
  // 36px composer row, matching the reference input metrics.
  assert.match(mobileComposerStyles, /textarea#messageText[\s\S]*?--composer-input-min-height:\s*36px/);
  assert.match(mobileComposerStyles, /#sendMessageBtn[\s\S]*?width:\s*44px[\s\S]*?height:\s*36px/);
  assert.match(mobileComposerStyles, /#sendMessageBtn::before\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(styles, /\.composer-task-summary:disabled,[\s\S]*?\.composer-task-summary:not\(\.has-task\)\s*\{[^}]*display:\s*none/);
  assert.match(styles, /\.composer-task-summary\s*\{[^}]*width:\s*28px[^}]*margin-right:\s*auto/);
  assert.match(styles, /\.security-mode-pill::before\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(styles, /#backgroundTasksBtn\s*\{\s*display:\s*none/);
  assert.match(mobileComposerStyles, /\[class~="composer-hints"\][\s\S]*?display:\s*none/);
});

test("narrow composer switches atomically to a fixed unframed icon rail", async () => {
  const [styles, uiShell] = await Promise.all([readStylesSource(stylesURL), readFile(uiShellURL, "utf8")]);
  const marker = "/* Narrow composer icon rail: preserve every control at one fixed size. */";
  const iconRail = styles.slice(styles.indexOf(marker), styles.indexOf("/* Flat, single-pass settings layout", styles.indexOf(marker)));
  assert.ok(iconRail.startsWith(marker));
  assert.match(iconRail, /@container composer-shell \(max-width:\s*480px\)/);
  assert.match(iconRail, /\.composer-status\s*\{[^}]*width:\s*28px[^}]*display:\s*inline-flex[^}]*flex:\s*0 0 28px/);
  assert.match(iconRail, /\.composer-status\.is-busy\s*\{[^}]*width:\s*auto[^}]*flex:\s*0 1 auto/);
  assert.match(iconRail, /\.composer-status-dot\.busy,[\s\S]*?animation:\s*composer-task-indicator-spin/);
  assert.match(iconRail, /\.composer-status\.is-busy \.composer-status-text\s*\{[^}]*display:\s*inline/);
  assert.match(iconRail, /\.composer-status-dot,[\s\S]*?\.composer-status-dot\.ok\s*\{[^}]*width:\s*14px[^}]*border:\s*2px solid[^}]*background:\s*transparent/);
  assert.match(iconRail, /\.composer-controls\s*\{[^}]*min-width:\s*max-content[^}]*flex:\s*0 0 auto[^}]*overflow:\s*visible/);
  assert.match(iconRail, /:is\(\.composer-model-field, \.composer-effort-field, \.composer-permission-field\)\s*\{[^}]*width:\s*28px[^}]*min-width:\s*28px[^}]*max-width:\s*28px[^}]*flex:\s*0 0 28px/);
  assert.match(iconRail, /:is\(\.toolbar-model-pill, \.effort-pill, \.permission-toolbar-pill\)\s*\{[^}]*width:\s*28px[^}]*flex:\s*0 0 28px[^}]*border:\s*0[^}]*background:\s*transparent/);
  assert.match(iconRail, /\.composer-select-value\s*\{[^}]*position:\s*absolute[^}]*clip-path:\s*inset\(50%\)/);
  assert.match(iconRail, /\.composer-select-chevron\s*\{[^}]*display:\s*none/);
  assert.match(iconRail, /\.toolbar-lightning-btn:not\(\.hidden\),[\s\S]*?\.composer-toolbar-icon\s*\{[^}]*width:\s*28px[^}]*display:\s*inline-flex[^}]*border:\s*0[^}]*background:\s*transparent/);
  assert.match(iconRail, /\.model-tool-btn\.icon-only\.composer-toolbar-icon\s*\{[^}]*width:\s*28px[^}]*height:\s*30px[^}]*min-height:\s*30px/);
  assert.match(iconRail, /\.composer-actions\s*\{[^}]*flex:\s*0 0 auto[^}]*gap:\s*4px/);
  assert.match(uiShell, /trigger\.setAttribute\("aria-label", fieldLabel \? `\$\{fieldLabel\}：\$\{displayText\}` : displayText\)/);
});

test("mobile sidebar closes safely during desktop startup and cache updates propagate", async () => {
  const [html, app, appMain, uiShell] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(uiShellURL, "utf8"),
  ]);
  assert.match(html, /app\.js\?v=[^"\n]*mobile-viewport-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*mobile-viewport-1/);
  assert.match(appMain, /ui-shell\.mjs\?v=[^"\n]*mobile-viewport-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*model-save-scroll-1/);
  assert.match(html, /app\.js\?v=[^"\n]*model-save-scroll-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*model-save-scroll-1/);
  assert.match(appMain, /chat-composer\.mjs\?v=[^"\n]*model-save-gate-1/);
  assert.match(appMain, /ui-shell\.mjs\?v=[^"\n]*model-menu-scroll-1/);
  assert.equal((uiShell.match(/const mobileViewport/g) || []).length, 1);

  const bodyClasses = new Set(["mobile-sidebar-open"]);
  const fakeDocument = {
    body: {
      classList: {
        contains(name) { return bodyClasses.has(name); },
        remove(name) { bodyClasses.delete(name); },
      },
    },
    getElementById() { return null; },
  };
  const fakeWindow = { matchMedia() { return { matches: false }; } };
  const restoreDocument = replaceGlobal("document", fakeDocument);
  const restoreWindow = replaceGlobal("window", fakeWindow);
  try {
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    assert.doesNotThrow(() => controller.closeMobileSidebar({ restoreFocus: false }));
    assert.equal(bodyClasses.has("mobile-sidebar-open"), false);
  } finally {
    restoreWindow();
    restoreDocument();
  }
});

test("sidebar resize width clamps pointer values and keeps a stable preference key", () => {
  assert.equal(sidebarWidthPreferenceKey, "autoto.ui.sessionSidebarWidth");
  assert.equal(normalizeSidebarWidth(undefined), defaultSidebarWidth);
  assert.equal(normalizeSidebarWidth(100), minSidebarWidth);
  assert.equal(normalizeSidebarWidth(900), maxSidebarWidth);
  assert.equal(normalizeSidebarWidth("333.6"), 334);
  assert.equal(sidebarWidthFromPointer(510, 180), 330);
  assert.equal(sidebarWidthFromPointer(120, 180), minSidebarWidth);
});

test("sidebar resizer restores, drags, keys, persists, and cleans up", () => {
  const elementListeners = new Map();
  const windowListeners = new Map();
  const classes = new Set();
  const bodyClasses = new Set();
  const styleValues = new Map();
  const attributes = new Map();
  const storage = new MemoryStorage([[sidebarWidthPreferenceKey, "340"]]);
  const scrollNode = ({ hidden = false } = {}) => ({
    classList: { contains(name) { return hidden && name === "hidden"; } },
    clientHeight: 200,
    clientWidth: 240,
    scrollHeight: 800,
    scrollWidth: 240,
    scrollLeft: 0,
    scrollTop: 0,
  });
  const projects = scrollNode();
  const taskWorkspaceOverview = scrollNode();
  const projectKanbanBody = scrollNode({ hidden: true });
  const messages = scrollNode();
  const separator = {
    classList: {
      add(name) { classes.add(name); },
      remove(name) { classes.delete(name); },
    },
    addEventListener(name, handler) { elementListeners.set(name, handler); },
    removeEventListener(name) { elementListeners.delete(name); },
    getBoundingClientRect() { return { left: 397, width: 6 }; },
    setAttribute(name, value) { attributes.set(name, value); },
    setPointerCapture() {},
    releasePointerCapture() {},
  };
  const shell = { style: { setProperty(name, value) { styleValues.set(name, value); } } };
  const sidebar = { getBoundingClientRect() { return { left: 100 }; } };
  const fakeDocument = {
    body: {
      classList: {
        add(name) { bodyClasses.add(name); },
        remove(name) { bodyClasses.delete(name); },
        contains(name) { return bodyClasses.has(name); },
      },
    },
    getElementById(id) {
      return {
        appShell: shell,
        messages,
        projectKanbanBody,
        projects,
        sidebarResizeHandle: separator,
        taskWorkspaceOverview,
      }[id] || null;
    },
    querySelector(selector) { return selector === ".sidebar" ? sidebar : null; },
  };
  const fakeWindow = {
    matchMedia() { return { matches: false }; },
    addEventListener(name, handler) { windowListeners.set(name, handler); },
    removeEventListener(name) { windowListeners.delete(name); },
  };
  const restoreDocument = replaceGlobal("document", fakeDocument);
  const restoreWindow = replaceGlobal("window", fakeWindow);
  const restoreRAF = replaceGlobal("requestAnimationFrame", (callback) => callback());
  try {
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    const cleanup = controller.bindSidebarResizer({ storage });
    assert.equal(styleValues.get("--session-sidebar-width"), "340px");
    assert.equal(attributes.get("aria-valuenow"), "340");

    let prevented = false;
    elementListeners.get("keydown")({ key: "ArrowRight", shiftKey: false, preventDefault() { prevented = true; } });
    assert.equal(prevented, true);
    assert.equal(styleValues.get("--session-sidebar-width"), "348px");
    assert.equal(storage.getItem(sidebarWidthPreferenceKey), "348");

    bodyClasses.add("workbench-mode");
    let mainWheelPrevented = false;
    elementListeners.get("wheel")({
      clientX: 402,
      deltaMode: 0,
      deltaX: 0,
      deltaY: 120,
      preventDefault() { mainWheelPrevented = true; },
    });
    assert.equal(mainWheelPrevented, true);
    assert.equal(taskWorkspaceOverview.scrollTop, 120);

    let sidebarWheelPrevented = false;
    elementListeners.get("wheel")({
      clientX: 398,
      deltaMode: 0,
      deltaX: 0,
      deltaY: 90,
      preventDefault() { sidebarWheelPrevented = true; },
    });
    assert.equal(sidebarWheelPrevented, true);
    assert.equal(projects.scrollTop, 90);

    elementListeners.get("pointerdown")({ button: 0, pointerId: 1, clientX: 450, preventDefault() {} });
    assert.equal(classes.has("is-dragging"), true);
    assert.equal(bodyClasses.has("sidebar-resizing"), true);
    windowListeners.get("pointermove")({ clientX: 500, preventDefault() {} });
    windowListeners.get("pointerup")({ pointerId: 1 });
    assert.equal(styleValues.get("--session-sidebar-width"), "400px");
    assert.equal(storage.getItem(sidebarWidthPreferenceKey), "400");
    assert.equal(classes.has("is-dragging"), false);

    elementListeners.get("dblclick")();
    assert.equal(styleValues.get("--session-sidebar-width"), `${defaultSidebarWidth}px`);
    cleanup();
    assert.equal(elementListeners.size, 0);
    assert.equal(windowListeners.size, 0);
  } finally {
    restoreRAF();
    restoreWindow();
    restoreDocument();
  }
});

test("dual rail collapse compacts both rails, preserves independent sidebar restore, and stays desktop-only", () => {
  assert.equal(globalRailExpandedWidth, 68);
  assert.equal(globalRailCollapsedWidth, 48);
  assert.equal(collapsedSidebarWidth, 184);
  assert.equal(normalizeCollapsedPreference("true"), true);
  assert.equal(normalizeCollapsedPreference("false"), false);
  assert.equal(normalizeCollapsedPreference("unexpected", true), true);

  const makeClassList = (initial = []) => {
    const names = new Set(initial);
    return {
      add(...values) { values.forEach((value) => names.add(value)); },
      remove(...values) { values.forEach((value) => names.delete(value)); },
      contains(value) { return names.has(value); },
      toggle(value, force) {
        const next = force === undefined ? !names.has(value) : Boolean(force);
        if (next) names.add(value); else names.delete(value);
        return next;
      },
    };
  };
  const makeNode = ({ classes = [], attributes = {} } = {}) => {
    const listeners = new Map();
    const values = new Map(Object.entries(attributes));
    const node = {
      classList: makeClassList(classes),
      style: { setProperty(name, value) { values.set(`style:${name}`, value); } },
      addEventListener(name, handler) { listeners.set(name, handler); },
      removeEventListener(name) { listeners.delete(name); },
      dispatch(name, event = {}) { listeners.get(name)?.(event); },
      setAttribute(name, value) { values.set(name, String(value)); },
      getAttribute(name) { return values.has(name) ? values.get(name) : null; },
      removeAttribute(name) { values.delete(name); },
      get styleValues() { return values; },
    };
    return node;
  };
  const storage = new MemoryStorage([
    [sidebarWidthPreferenceKey, "342"],
    [globalRailCollapsedPreferenceKey, "true"],
    [sessionSidebarCollapsedPreferenceKey, "true"],
  ]);
  const shell = makeNode();
  const globalRail = makeNode();
  const sidebar = makeNode();
  const globalCollapseButton = makeNode();
  const sessionCollapseButton = makeNode();
  const separator = makeNode({ attributes: { tabindex: "0" } });
  separator.getBoundingClientRect = () => ({ left: 398, width: 6 });
  separator.setPointerCapture = () => {};
  separator.releasePointerCapture = () => {};
  const projects = { classList: makeClassList(), clientHeight: 200, clientWidth: 240, scrollHeight: 200, scrollWidth: 240, scrollLeft: 0, scrollTop: 0 };
  const messages = { classList: makeClassList(), clientHeight: 200, clientWidth: 240, scrollHeight: 200, scrollWidth: 240, scrollLeft: 0, scrollTop: 0 };
  const bodyClasses = makeClassList();
  const nodes = {
    appShell: shell,
    globalRailCollapseBtn: globalCollapseButton,
    messages,
    projects,
    sessionSidebarCollapseBtn: sessionCollapseButton,
    sidebarResizeHandle: separator,
  };
  const fakeDocument = {
    body: { classList: bodyClasses },
    documentElement: { clientWidth: 1280 },
    getElementById(id) { return nodes[id] || null; },
    querySelector(selector) {
      if (selector === ".sidebar") return sidebar;
      if (selector === ".global-rail") return globalRail;
      if (selector === ".agent-list-section") return projects;
      return null;
    },
  };
  let mobile = false;
  const windowListeners = new Map();
  const fakeWindow = {
    matchMedia() { return { matches: mobile }; },
    addEventListener(name, handler) { windowListeners.set(name, handler); },
    removeEventListener(name) { windowListeners.delete(name); },
  };
  const restoreDocument = replaceGlobal("document", fakeDocument);
  const restoreWindow = replaceGlobal("window", fakeWindow);
  const restoreRAF = replaceGlobal("requestAnimationFrame", (callback) => callback());
  try {
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    const cleanup = controller.bindSidebarResizer({ storage });
    assert.equal(shell.classList.contains("global-rail-collapsed"), true);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), true);
    assert.equal(globalCollapseButton.getAttribute("aria-expanded"), "false");
    assert.equal(sessionCollapseButton.getAttribute("aria-expanded"), "false");
    assert.equal(separator.getAttribute("tabindex"), "0");
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "184px");
    assert.equal(storage.getItem(globalRailCollapsedPreferenceKey), "true");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "true");

    globalCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px");
    assert.equal(storage.getItem(globalRailCollapsedPreferenceKey), "false");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "false");

    sessionCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), true);
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "184px");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "true");
    sessionCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "false");

    globalCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(shell.classList.contains("global-rail-collapsed"), true);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), true);
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "184px");
    globalCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);

    mobile = true;
    windowListeners.get("resize")?.();
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    globalCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    sessionCollapseButton.dispatch("click", { preventDefault() {}, stopPropagation() {} });
    assert.equal(storage.getItem(globalRailCollapsedPreferenceKey), "false");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "false");

    mobile = false;
    windowListeners.get("resize")?.();
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    cleanup();
    assert.equal(windowListeners.size, 0);
  } finally {
    restoreRAF();
    restoreWindow();
    restoreDocument();
  }
});

test("legacy chat alignment keeps the composer untouched and flattens the transcript", async () => {
  const [styles, chatRendering, appMain] = await Promise.all([
    readStylesSource(stylesURL),
    readFile(chatRenderingURL, "utf8"),
    readFile(appMainURL, "utf8"),
  ]);
  const marker = "/* Legacy chat transcript alignment. Intentionally excludes every composer/input selector. */";
  const legacyStart = styles.indexOf(marker);
  const legacyEnd = styles.indexOf("/* Codex account management", legacyStart);
  const legacyChatStyles = styles.slice(legacyStart, legacyEnd);

  assert.ok(legacyChatStyles.startsWith(marker));
  assert.match(legacyChatStyles, /\.chat-header\s*\{[\s\S]*?height:\s*64px/);
  assert.match(legacyChatStyles, /\.message\.user,[\s\S]*?background:\s*transparent/);
  assert.match(legacyChatStyles, /\.message\.assistant\s*\{[\s\S]*?margin-right:\s*auto/);
  assert.doesNotMatch(legacyChatStyles, /\.composer-/);
  assert.doesNotMatch(legacyChatStyles, /\.message-input/);
  assert.match(chatRendering, /empty-conversation-state[^\n]*message\.empty/);
  assert.match(chatRendering, /normalizeMessageProfileIdentity\(state\.profile\)/);
  assert.match(chatRendering, /data-user-profile-avatar/);
  assert.match(chatRendering, /data-user-profile-name/);
  assert.match(appMain, /state\.profile = snapshot\.profile;[\s\S]*?refreshUserMessageIdentity\(\);/);
  assert.match(styles, /\[class~="user"\]\[class~="chat-flow-left"\] \.message-avatar\s*\{[\s\S]*?width:\s*24px;/);
  assert.match(styles, /\[class~="user"\]\[class~="chat-flow-left"\] \.message-role\s*\{[\s\S]*?font-size:\s*13px;/);
  assert.match(chatRendering, /normalizeAvatarDataUrl\(source\.avatarDataUrl\)/);
  assert.match(chatRendering, /message-avatar-image/);
  assert.match(styles, /\.profile-avatar-image\s*\{[\s\S]*?object-fit:\s*cover;/);
});

test("settings dialog mounts the shadcn shell without dropping legacy entry points", async () => {
  const [html, appMain, uiShell] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(uiShellURL, "utf8"),
  ]);
  for (const id of [
    "settingsModal",
    "settingsModalTitle",
    "settingsCategoryNav",
    "settingsSearchInput",
    "clearSettingsSearchBtn",
    "closeSettingsModalBtn",
    "settingsNav",
    "settingsContentTitle",
    "settingsContentSubtitle",
    "settingsContentBody",
    "settingsHelpBtn",
    "settingsHelpBackdrop",
    "settingsHelpPanel",
    "settingsHelpTitle",
    "settingsHelpBody",
    "closeSettingsHelpBtn",
    "conversationDetailsPanel",
    "conversationDetailsBody",
    "workspacePreviewNavigateForm",
    "workspacePreviewAddress",
  ]) assert.match(html, new RegExp(`id="${id}"`));
  for (const className of [
    "settings-dialog-shell",
    "settings-sidebar",
    "settings-sidebar-header",
    "settings-sidebar-search",
    "settings-mobile-category-nav",
    "settings-nav-groups",
    "settings-main",
    "settings-main-header",
    "settings-main-heading",
    "settings-page-scroll",
    "settings-help-trigger",
    "settings-help-backdrop",
    "settings-help-panel",
    "settings-help-body",
  ]) assert.match(html, new RegExp(`class="[^"]*${className}`));
  assert.doesNotMatch(html, /settingsIdentity(?:Btn|Avatar|Name|Meta)|settings-identity-card/);
  assert.match(html, /class="sidebar-footer hidden"/);
  assert.match(html, /id="settingsContentSubtitle"[^>]*class="[^"]*hidden[^"]*"[^>]*aria-hidden="true"/);
  assert.match(html, /id="settingsHelpBtn"[^>]*aria-controls="settingsHelpPanel"[^>]*aria-expanded="false"/);
  assert.match(html, /id="settingsHelpPanel"[^>]*role="dialog"[^>]*aria-modal="false"[^>]*aria-hidden="true"/);
  assert.match(appMain, /createSettingsHelpController/);
  assert.match(appMain, /settingsHelp\.sync\(\{ key: item\.key, label: item\.label, overview: item\.subtitle \}\)/);
  assert.match(appMain, /settingsHelp\.handleKeydown\(event\)/);
  const desktopNav = appMain.slice(appMain.indexOf("function renderSettingsNav"), appMain.indexOf("function updateSettingsSearchQuery"));
  assert.doesNotMatch(desktopNav, /settings-nav-label[^\n]*<small>\$\{escapeHtml\(item\.subtitle\)\}/);
  assert.match(appMain, /settings-mobile-index-copy[^\n]*<strong>\$\{escapeHtml\(item\.label\)\}<\/strong><small>\$\{escapeHtml\(item\.subtitle\)\}<\/small>/);
  assert.match(appMain, /groupSettingsItemsByLegacyCategory/);
  assert.match(appMain, /class="settings-nav-group"/);
  assert.match(appMain, /aria-current="page"/);
  assert.match(appMain, /class="settings-page-frame" data-settings-page=/);
  assert.match(appMain, /data-panel-layout=/);
  assert.match(appMain, /handleSettingsDialogKeydown/);
  assert.match(appMain, /beginSettingsDialogFocus/);
  assert.match(uiShell, /settingsDialogHasNestedModal/);
  assert.match(uiShell, /restoreSettingsDialogFocus/);
  assert.match(uiShell, /event\.defaultPrevented/);
  assert.doesNotMatch(appMain, /openEmployeeOverview|employeeOverview/);
  assert.doesNotMatch(appMain, /\["(?:agents|worklines-containers)",\s*\{\s*render:/);
  assert.match(appMain, /renderConversationDetails\(\)/);
  assert.match(appMain, /settingsCategoryForItem/);
  assert.match(appMain, /classList\.toggle\("about-panel-active", isAboutPanel\)/);
  assert.match(appMain, /bindSkillTabs\(state\.activeSkillTab \|\| "commands"\)/);
  assert.doesNotMatch(appMain, /\["users",\s*\{\s*render:/);
});

test("settings navigation uses a filled selection rail without a full outline", async () => {
  const styles = await readStylesSource(stylesURL);
  assert.match(styles, /#settingsModal :is\(\.settings-nav-item, \.settings-nav-group button\)\.active,[\s\S]*?\[aria-current="page"\]\s*\{[\s\S]*?border-color:\s*transparent;[\s\S]*?background:\s*var\(--settings-primary-soft\)/);
  assert.match(styles, /#settingsModal :is\(\.settings-nav-item, \.settings-nav-group button\)\.active::before,[\s\S]*?width:\s*3px;[\s\S]*?background:\s*var\(--settings-primary\)/);
  assert.match(styles, /:focus-visible\s*\{[\s\S]*?box-shadow:\s*0 0 0 3px var\(--settings-ring\)/);
});

test("settings shell docks beside the global rail and keeps complete mobile navigation", async () => {
  const [html, styles, app, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
  ]);
  const settingsMarker = "Settings shadcn system — scoped integration.";
  const dockMarker = "/* Settings mode occupies the workbench beside the persistent global rail. */";
  const providerMarker = "/* Model provider settings. Scoped after legacy settings overrides by design. */";
  const settingsIndex = styles.indexOf(settingsMarker);
  const dockIndex = styles.indexOf(dockMarker, settingsIndex);
  const providerIndex = styles.indexOf(providerMarker);
  assert.ok(settingsIndex > 0 && dockIndex > settingsIndex && providerIndex > dockIndex);
  const settingsStyles = styles.slice(settingsIndex, providerIndex);
  const dockStyles = settingsStyles.slice(settingsStyles.indexOf(dockMarker));
  assert.match(dockStyles, /@media \(min-width:\s*768px\)[\s\S]*?#settingsModal\s*\{[\s\S]*?align-items:\s*stretch;[\s\S]*?justify-content:\s*stretch;[\s\S]*?backdrop-filter:\s*none;/);
  assert.match(dockStyles, /#settingsModal \.settings-dialog-shell\s*\{[\s\S]*?width:\s*100%;[\s\S]*?height:\s*100%;[\s\S]*?grid-template-columns:\s*var\(--session-sidebar-width, 296px\) minmax\(0, 1fr\);[\s\S]*?border-radius:\s*0;/);
  assert.doesNotMatch(settingsStyles, /\.settings-dialog-shell:has\(\.codex-account-console\)/);
  assert.match(settingsStyles, /\.settings-main\.legacy-settings-content\s*\{[\s\S]*?overflow:\s*hidden !important;/);
  assert.match(settingsStyles, /#settingsModal \.settings-page-frame\s*\{[^}]*width:\s*min\(1120px, 100%\);[^}]*margin:\s*0;/);
  assert.doesNotMatch(settingsStyles, /#settingsModal \.settings-page-frame\s*\{[^}]*margin:\s*0 auto;/);
  assert.match(settingsStyles, /#settingsContentBody \[data-settings-help-copy\]\s*\{\s*display:\s*none !important;/);
  assert.match(settingsStyles, /#settingsModal \.settings-help-panel\s*\{[\s\S]*?width:\s*min\(440px, 88%\);/);
  assert.match(settingsStyles, /#settingsModal \.settings-help-body\s*\{[\s\S]*?overflow:\s*auto;/);
  assert.match(settingsStyles, /\.automation-hero p/);

  // Docking itself lives in settings-shell-helpers.mjs and is covered by
  // settings-shell-docking.test.mjs, which drives the real enter/exit pair and
  // asserts reparenting, the display:none !important + aria-hidden treatment of
  // the conversation surfaces, dialog-semantics swap and restoration, full
  // inline-style cleanup, and idempotent enter/exit.
  // \r?\n, not \n: git checks these files out with CRLF on Windows, so a bare
  // \n makes this the only assertion in the suite that fails on a Windows
  // working tree while passing in CI.
  assert.match(appMain, /enterSettingsShell,\r?\n\s*exitSettingsShell,/);
  assert.match(appMain, /if \(state\.settingsMobileViewport\) exitSettingsShell\(\);\s*\n\s*else enterSettingsShell\(\);/);
  assert.match(appMain, /closeSettingsModal\(\{ restoreWorkbench: false, restoreFocus: false \}\)/);
  assert.match(appMain, /function closeSettingsModal[\s\S]*?discardProviderConsoleDraft\(\);/);
  assert.match(html, /class="settings-sidebar legacy-settings-topbar"[^>]*data-i18n-aria-label="settings\.directory"/);
  assert.match(html, /id="closeSettingsModalBtn"[^>]*data-i18n-title="settings\.backToChat"[^>]*data-i18n-aria-label="settings\.backToChat"/);
  assert.match(html, /class="settings-page-scroll"[^>]*role="region"[^>]*data-i18n-aria-label="settings\.details"/);
  assert.match(appMain, /settingsIconSVG\(item\.icon\)/);
  assert.doesNotMatch(appMain, /escapeHtml\(item\.icon\)/);
  assert.match(html, /styles\.css\?v=[^"\n]*settings-icons-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*settings-content-left-1/);
  assert.match(html, /app\.js\?v=[^"\n]*settings-icons-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*settings-icons-1/);
  assert.match(appMain, /settings-data\.mjs\?v=[^"\n]*settings-icons-1/);
  assert.match(html, /app\.js\?v=[^"\n]*default-openai-responses-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*default-openai-responses-1/);
  assert.match(appMain, /model-provider-settings\.mjs\?v=[^"\n]*default-openai-responses-1/);
  assert.match(html, /app\.js\?v=[^"\n]*provider-draft-session-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*provider-draft-session-1/);
  assert.match(appMain, /model-provider-settings\.mjs\?v=[^"\n]*provider-draft-session-1/);
  assert.match(html, /app\.js\?v=[^"\n]*remote-full-toggle-3/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*remote-full-toggle-3/);
  assert.match(appMain, /remote-access-settings\.mjs\?v=[^"\n]*remote-full-toggle-3/);
  assert.match(html, /styles\.css\?v=[^"\n]*profile-message-identity-1/);
  assert.match(html, /app\.js\?v=[^"\n]*profile-message-identity-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*profile-message-identity-1/);
  assert.match(appMain, /chat-rendering\.mjs\?v=[^"\n]*profile-message-identity-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*profile-avatar-1/);
  assert.match(html, /app\.js\?v=[^"\n]*profile-avatar-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*profile-avatar-1/);
  assert.match(appMain, /account-preferences\.mjs\?v=profile-avatar-1/);
  assert.match(appMain, /local-preferences-settings\.mjs\?v=[^"\n]*profile-avatar-1/);
  assert.match(settingsStyles, /\.settings-nav-icon \.settings-icon-svg\s*\{[^}]*width:\s*18px;[^}]*height:\s*18px;/);
  assert.match(settingsStyles, /\[aria-current="page"\] \.settings-nav-icon\s*\{[^}]*background:\s*transparent;[^}]*color:\s*var\(--settings-primary\);[^}]*box-shadow:\s*none;/);
  assert.match(html, /styles\.css\?v=[^"\n]*settings-active-icon-frame-removed-1/);

  const mobile = settingsStyles.slice(settingsStyles.indexOf("@media (max-width: 767px)"));
  assert.match(mobile, /\.settings-sidebar\s*\{[\s\S]*?display:\s*grid;/);
  assert.doesNotMatch(mobile, /\.settings-sidebar\s*\{[^}]*display:\s*none;/);
  assert.match(mobile, /\.settings-mobile-category-nav,[\s\S]*?\.settings-nav-groups\s*\{[\s\S]*?display:\s*flex;/);
  assert.match(mobile, /\.settings-nav-group\s*\{[\s\S]*?display:\s*contents;/);
  assert.match(mobile, /\.settings-help-panel\s*\{\s*width:\s*100%;\s*border-left:\s*0;/);
});

test("mobile shell skips home and keeps the drawer, settings index, and model sheet wired", async () => {
  const [html, styles, app, appMain, uiShell, settingsPreferences, messagesCN, messagesTW, messagesEN] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(uiShellURL, "utf8"),
    readFile(new URL("./settings-preferences.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-zh-CN.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-zh-TW.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-en.mjs", import.meta.url), "utf8"),
  ]);

  for (const id of [
    "mobilePageTitle",
    "overviewDashboard",
    "mobileNewConversationBtn",
    "mobileChooseDirectoryBtn",
    "mobileConversationWelcome",
    "mobileSidebarBackdrop",
    "mobileSidebarCloseBtn",
    "mobileDrawerSearchBtn",
    "mobileSidebarAccountSummary",
    "mobileSidebarSettingsBtn",
    "mobileSidebarLogoutBtn",
  ]) assert.match(html, new RegExp(`id="${id}"`));
  assert.match(html, /id="mobilePageTitle"[^>]*data-i18n="shell\.nav\.conversation">对话<\/div>/);
  assert.doesNotMatch(html, /id="mobilePageTitle"[^>]*data-i18n="shell\.nav\.home"/);
  assert.match(html, /class="[^"]*mobile-drawer-header/);
  assert.match(html, /class="mobile-drawer-brand-mark"[^>]*>[\s\S]*?<svg viewBox="0 0 32 32">/);
  assert.match(html, /class="[^"]*mobile-drawer-footer/);
  assert.match(html, /class="settings-sidebar-header settings-modal-header"/);
  assert.doesNotMatch(html, /id="mobileSettingsIndex"/);
  assert.doesNotMatch(html, /id="mobileModelPanel"/);
  assert.match(html, /id="attachFileBtn"[^>]*data-i18n-aria-label="chat\.attachFile"/);
  assert.match(html, /styles\.css\?v=[^"\n]*mobile-sidebar-list-compact-1/);

  const marker = "/* Mobile shell refresh: conversation, drawer, settings, and composer selection sheets. */";
  const refreshedStyles = styles.slice(styles.indexOf(marker));
  assert.ok(refreshedStyles.startsWith(marker));
  assert.match(styles, /\.mobile-topbar,\s*\.mobile-backdrop,\s*\.mobile-drawer-header,\s*\.mobile-drawer-primary-actions,\s*\.mobile-drawer-schedule-btn,\s*\.mobile-sidebar-account-summary,\s*\.mobile-sidebar-quick-actions,\s*\.mobile-drawer-footer,\s*\.mobile-conversation-welcome,\s*\.composer-status\s*\{\s*display:\s*none;/);
  assert.match(refreshedStyles, /:is\(\.composer-task-summary, \.permission-safety-indicator, \.permission-risk-badge, \.toolbar-lightning-btn\)/);
  assert.match(refreshedStyles, /\.composer-status:not\(\.is-busy\)/);
  assert.match(refreshedStyles, /\.composer-status\.is-busy\s*\{[^}]*display:\s*inline-flex !important/);
  assert.match(refreshedStyles, /\.mobile-select-sheet-backdrop\s*\{[\s\S]*?align-items:\s*flex-end/);
  assert.match(refreshedStyles, /@media \(max-width:\s*767px\)[\s\S]*?\.mobile-page-title\s*\{[\s\S]*?text-align:\s*center/);
  assert.match(refreshedStyles, /body\.white-shell\.theme-light \.mobile-backdrop\s*\{[^}]*z-index:\s*78[^}]*backdrop-filter:\s*blur\(2px\)/);
  assert.match(refreshedStyles, /body\.white-shell\.theme-light \.sidebar\s*\{[^}]*z-index:\s*80/);
  assert.match(refreshedStyles, /body\.white-shell\.theme-light \.overview-dashboard-page\s*\{\s*display:\s*none !important/);
  assert.match(refreshedStyles, /\.mobile-drawer-brand-mark svg\s*\{[^}]*fill:\s*none;[^}]*stroke:\s*currentColor;[^}]*stroke-linecap:\s*round/);
  assert.match(refreshedStyles, /body\.white-shell\.theme-light\.mobile-sidebar-open \.sidebar\s*\{[^}]*transform:\s*translateX\(0\)/);
  assert.match(refreshedStyles, /\.chat-panel:has\(\.messages\.empty:not\(\[data-initial-chat-state="loading"\]\)\) \.mobile-conversation-welcome\s*\{\s*display:\s*flex/);
  assert.match(refreshedStyles, /#settingsModal\.mobile-settings-index \.settings-main\s*\{\s*display:\s*none/);
  assert.match(refreshedStyles, /#settingsModal\.mobile-settings-detail \.settings-sidebar\s*\{[\s\S]*?flex:\s*0 0 auto/);
  assert.match(refreshedStyles, /#settingsModal \.settings-modal-header \.settings-title\s*\{[\s\S]*?grid-column:\s*2;[\s\S]*?text-align:\s*center/);
  assert.match(refreshedStyles, /#settingsModal\.mobile-settings-detail \.settings-close-btn\s*\{[^}]*grid-column:\s*1/);
  assert.match(refreshedStyles, /\.mobile-sidebar-account-summary\s*\{[^}]*display:\s*none/);
  assert.doesNotMatch(refreshedStyles, /\.settings-identity-(?:card|copy|avatar|chevron)/);
  assert.match(refreshedStyles, /\.sidebar \.project-list\s*\{[^}]*flex:\s*0 1 auto;[^}]*align-content:\s*start;[^}]*overflow-y:\s*auto/);
  assert.doesNotMatch(refreshedStyles, /\.sidebar \.project-list\s*\{[^}]*flex:\s*1 1 auto/);
  assert.match(refreshedStyles, /\.mobile-sidebar-quick-actions\s*\{[\s\S]*?grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/);
  const guardMarker = "/* Mobile shell final cascade guard: keep the redesigned composer above legacy responsive tiers. */";
  const guardIndex = styles.lastIndexOf(guardMarker);
  assert.ok(guardIndex > styles.indexOf("/* Narrow composer icon rail"));
  const cascadeGuard = styles.slice(guardIndex);
  assert.match(html, /class="mobile-brand">Autoto<\/div>/);
  assert.match(html, /id="attachFileBtn"[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.match(cascadeGuard, /\.mobile-brand\s*\{[\s\S]*?display:\s*block/);
  assert.match(cascadeGuard, /\.chat-header\s*\{[\s\S]*?height:\s*50px;[\s\S]*?display:\s*flex/);
  assert.match(cascadeGuard, /\.composer-toolbar\s*\{[\s\S]*?order:\s*1/);
  assert.match(cascadeGuard, /\.composer-input-shell\s*\{[\s\S]*?order:\s*2/);
  assert.match(cascadeGuard, /#sendMessageBtn\s*\{[\s\S]*?width:\s*60px[\s\S]*?height:\s*36px/);
  assert.match(cascadeGuard, /\.composer-task-summary,[\s\S]*?display:\s*none !important/);
  assert.match(cascadeGuard, /\.composer-select-value,[\s\S]*?position:\s*static;[\s\S]*?clip-path:\s*none/);
  assert.match(cascadeGuard, /\.composer-model-field \.composer-select-trigger\s*\{\s*max-width:\s*min\(58vw, 246px\)/);

  assert.match(html, /styles\.css\?v=[^"\n]*mobile-settings-compact-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*mobile-logo-1/);
  assert.match(html, /styles\.css\?v=[^"\n]*mobile-no-home-1/);
  assert.match(html, /app\.js\?v=[^"\n]*mobile-settings-compact-1/);
  assert.match(html, /app\.js\?v=[^"\n]*mobile-no-home-1/);
  assert.match(html, /app\.js\?v=[^"\n]*mobile-title-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*mobile-settings-compact-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*mobile-no-home-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*mobile-title-1/);
  assert.match(appMain, /mobileSettingsView:\s*"detail"/);
  assert.match(appMain, /function showMobileSettingsIndex[\s\S]*?settings\.mobile\.indexTitle/);
  assert.match(appMain, /showMobileSettingsIndex[\s\S]*?querySelector\?\.\("\.settings-mobile-index-row"\)\?\.focus/);
  assert.match(appMain, /function requestCloseSettingsModal[\s\S]*?mobileSettingsView === "detail"/);
  assert.match(appMain, /function syncMobilePageTitle[\s\S]*?\(!state\.project && !state\.agent\) \? t\("shell\.nav\.conversation"\)/);
  assert.doesNotMatch(appMain, /\(!state\.project && !state\.agent\) \? t\("shell\.home"\)/);
  assert.match(uiShell, /mobile-select-sheet-backdrop hidden/);
  assert.match(uiShell, /mobileSidebarBackdrop/);
  assert.match(uiShell, /mobileSidebarCloseBtn/);
  assert.equal((uiShell.match(/function closeMobileSidebar/g) || []).length, 1);
  assert.match(uiShell, /translate\("chat\.selectModel"\)/);
  // 管理模型 / 思考强度 / 压缩上下文 were removed from the model menu: each already has
  // its own control beside the composer.
  // The model menu instead offers the summary model, which is applied by
  // round-tripping the whole agent payload so the default model and subagent
  // assignments are not dropped.
  assert.match(uiShell, /translate\("chat\.summaryModel"\)/);
  assert.match(uiShell, /openSummaryModelPicker/);
  assert.match(appMain, /agentModelSettingsPayload\(\{ \.\.\.\(state\.settings\?\.agent \|\| \{\}\), summaryModel: model \}\)/);
  assert.match(appMain, /api\("\/api\/runtime\/agent-model-settings", \{ method: "PATCH"/);
  assert.doesNotMatch(uiShell, /translate\("chat\.manageModels"\)/);
  assert.doesNotMatch(uiShell, /translate\("chat\.compactContext"\)/);
  assert.doesNotMatch(uiShell, /openModelSettings/);
  assert.doesNotMatch(uiShell, /mobileComposerSelectStyles/);
  assert.match(settingsPreferences, /mobileSidebarAvatar/);
  assert.match(settingsPreferences, /mobileSidebarAccountName/);
  assert.match(settingsPreferences, /mobileSidebarAccountMeta/);

  for (const messages of [messagesCN, messagesTW, messagesEN]) {
    assert.match(messages, /mobileWelcomeTitle:/);
    assert.match(messages, /selectModel:/);
    assert.match(messages, /backToIndex:/);
    assert.match(messages, /personalInterface:/);
  }
});

test("about settings use the legacy version layout and real update status", () => {
  const state = {
    settings: { version: "0.1.0-dev" },
    updateStatus: null,
    updateError: "",
    licenseSummary: null,
    licenseError: "",
  };
  const controller = createSystemSettingsController({
    state,
    localPreferencesBackupSummary: () => ({ count: 0, bytes: 0, labels: [] }),
  });

  const initial = controller.renderAboutSettingsContent();
  assert.match(initial, /class="legacy-about-logo"[\s\S]*?\/ui\/autoto-logo\.svg/);
  assert.doesNotMatch(initial, /legacy-about-logo-spark/);
  assert.match(initial, /class="legacy-about-overview"/);
  assert.doesNotMatch(initial, /legacy-about-overview settings-page-section settings-card/);
  assert.match(initial, /id="legacyAboutProductName">Autoto</);
  assert.match(initial, /当前版本[\s\S]*0\.1\.0-dev/);
  assert.match(initial, /最新版本[\s\S]*—/);
  assert.match(initial, /更新状态[\s\S]*尚未检查/);
  assert.match(initial, /id="checkForUpdatesBtn"/);
  assert.match(initial, /<details class="legacy-about-more">/);

  state.updateStatus = {
    status: "update_available",
    currentVersion: "1.0.0",
    targetVersion: "1.1.0",
  };
  const available = controller.renderAboutSettingsContent();
  assert.match(available, /当前版本[\s\S]*1\.0\.0/);
  assert.match(available, /最新版本[\s\S]*1\.1\.0/);
  assert.match(available, /发现可用更新/);

  state.licenseSummary = {
    notice: "Development aid only; verify before distribution. Not legal advice.",
    modules: [
      { path: "example.com/unknown", version: "v1.0.0", license: "unknown", relation: "indirect" },
      { path: "example.com/direct", version: "v2.0.0", license: "MIT", relation: "direct" },
    ],
  };
  const licenses = controller.renderAboutSettingsContent();
  assert.match(licenses, /class="legacy-about-license-metrics/);
  assert.match(licenses, /class="license-accordion warn" open/);
  assert.match(licenses, /未知许可证/);
  assert.match(licenses, /MIT/);
  assert.match(licenses, /直接依赖/);
  assert.doesNotMatch(licenses, /Development aid only/);
  assert.doesNotMatch(licenses, /license-group-grid/);
});

test("legacy font stack and static shell translations are wired", async () => {
  const [html, styles, app, appMain, chatRendering] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(chatRenderingURL, "utf8"),
  ]);
  assert.match(styles, /--ui-font:\s*-apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft JhengHei", sans-serif/);
  assert.match(styles, /font:\s*14px\/1\.45 var\(--ui-font\)/);
  assert.match(styles, /\.legacy-settings-category\s*\{[\s\S]*?font-weight:\s*600/);
  assert.match(styles, /\.legacy-settings-content-head\s*\{[\s\S]*?margin:\s*0 36px/);
  assert.match(styles, /#settingsContentBody \* \{ font-weight:\s*400; \}/);
  assert.match(styles, /\.legacy-settings-content-head \{ display:\s*none; \}/);
  assert.match(styles, /\.legacy-settings-content-body \.settings-provider-section,[\s\S]*?border-radius:\s*0/);
  assert.match(styles, /\.legacy-settings-content-body \.settings-action-btn \{[\s\S]*?border-radius:\s*7px/);
  assert.match(styles, /\.legacy-settings-content \{[\s\S]*?overflow-y:\s*auto\s*!important/);
  assert.match(styles, /\.legacy-settings-content-body \{[\s\S]*?width:\s*auto;[\s\S]*?margin:\s*0;[\s\S]*?padding:\s*24px 24px 56px 36px;[\s\S]*?overflow:\s*visible\s*!important/);
  assert.match(styles, /\.legacy-settings-content-body \.skills-tabs \{[\s\S]*?display:\s*flex;[\s\S]*?flex-wrap:\s*wrap/);
  assert.match(html, /data-i18n="shell\.nav\.conversation"/);
  assert.match(html, /data-i18n-placeholder="chat\.messagePlaceholder"/);
  assert.match(html, /data-i18n-aria-label="settings\.categories"/);
  assert.match(html, /app\.js\?v=[^"\n]*i18n-shared-1/);
  assert.match(app, /app-main\.mjs\?v=[^"\n]*i18n-shared-1/);
  assert.match(appMain, /i18n\.mjs\?v=[^"\n]*i18n-shared-1/);
  assert.match(appMain, /chat-rendering\.mjs\?v=[^"\n]*i18n-shared-1/);
  assert.match(chatRendering, /messages-chat-rendering-extra\.mjs\?v=[^"\n]*i18n-shared-1/);
});

test("static shell controls localize without marking runtime-owned content", async () => {
  const html = await readFile(indexURL, "utf8");
  const tag = (id) => html.match(new RegExp(`<[^>]*id="${id}"[^>]*>`))?.[0] || "";

  for (const [id, marker] of [
    ["workspaceExplorerBtn", 'data-i18n-aria-label="chat.openWorkspace"'],
    ["gitWorkflowBtn", 'data-i18n-aria-label="chat.gitChanges"'],
    ["specBoardBtn", 'data-i18n-aria-label="chat.taskList"'],
    ["runtimeStatusBtn", 'data-i18n-aria-label="chat.conversationDetails"'],
    ["composerFolderBtn", 'data-i18n-title="chat.switchDirectory"'],
    ["reconnectTerminalBtn", 'data-i18n="common.reconnect"'],
    ["conversationDetailsPanel", 'data-i18n-aria-label="staticExtra.workspace.main.conversationDetails"'],
    ["backendsModalTitle", 'data-i18n="staticExtra.backend.modalTitle"'],
    ["closeGitModalBtn", 'data-i18n-aria-label="staticExtra.workspace.git.closeModal"'],
    ["workspaceModalTitle", 'data-i18n="staticExtra.workspace.explorer.modalTitle"'],
    ["workspacePreviewAddress", 'data-i18n-placeholder="staticExtra.workspace.explorer.addressPlaceholder"'],
    ["closeSpecBoardBtn", 'data-i18n-aria-label="staticExtra.workspace.spec.close"'],
  ]) assert.match(tag(id), new RegExp(marker));

  assert.doesNotMatch(html, /employeeOverview|staticExtra\.workspace\.main\.employeeOverview/);
  assert.match(html, /<span data-i18n="backend\.nameLabel">名称<\/span>/);
  assert.match(html, /<span data-i18n="staticExtra\.workspace\.explorer\.optionalPort">端口（可选）<\/span>/);
  assert.doesNotMatch(tag("terminalCommandInput"), /data-i18n-placeholder/, "terminal input placeholder is runtime-owned");
  for (const id of ["currentTitle", "directoryStatus", "workspaceEditorPath", "workspacePreviewStatus", "workspacePreviewLogs"]) {
    assert.doesNotMatch(tag(id), /data-i18n(?:-title|-placeholder|-aria-label)?=/, `${id} is runtime-owned`);
  }
});

test("initial shell and default appearance use the versioned light theme", async () => {
  const html = await readFile(indexURL, "utf8");

  assert.match(html, /<body class="theme-light white-shell ui-density-comfortable">/);
  assert.match(html, /styles\.css\?v=white-shell-2/);
  assert.match(html, /app\.js\?v=white-shell-2/);
  assert.equal(defaultAppearancePrefs.themePreset, "light");
  assert.equal(defaultAppearancePrefs.theme, "light");
  assert.equal(defaultAppearancePrefs.styleVersion, appearanceStyleVersion);
  assert.equal(appearanceStyleVersion, 5);
  assert.deepEqual(appearanceThemePresets, ["light", "dark", "cyber", "cream", "apple"]);
});

test("dark appearance keeps the legacy white-shell geometry and layers colors only", async () => {
  const [preferences, styles] = await Promise.all([
    readFile(settingsPreferencesURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(preferences, /classList\.toggle\("theme-light", true\)/);
  assert.match(preferences, /classList\.toggle\("theme-dark", prefs\.theme === "dark"\)/);
  assert.match(styles, /body\.white-shell\.theme-light\.theme-dark\s*\{[\s\S]*?--ws-canvas:/);
  assert.match(styles, /body\.white-shell\.theme-light\.theme-dark \.workbench-panel\s*\{[\s\S]*?background:\s*var\(--ws-canvas\)/);
  assert.match(styles, /moon button changes only the palette/);
});

test("chat-platform settings use the shared flat settings layout", async () => {
  const styles = await readStylesSource(stylesURL);

  assert.match(styles, /legacy-settings-content-body \.automation-section\.span-2,[\s\S]*?grid-column:\s*1/);
  assert.match(styles, /legacy-settings-content-body \.automation-list\s*\{[\s\S]*?max-height:\s*none/);
  assert.match(styles, /legacy-settings-content-body \.automation-form[\s\S]*?border-radius:\s*0/);
});

test("unversioned dark appearance migrates once to light and explicit versioned dark remains valid", () => {
  const storage = new MemoryStorage([[appearancePrefsKey, JSON.stringify({
    theme: "dark",
    density: "compact",
    terminalDefaultOpen: true,
    showEventLog: false,
  })]]);

  withBrowserStorage(storage, () => {
    const controller = createController({ activeSettingsPanel: "" });
    const migrated = controller.loadAppearancePreferences();
    assert.deepEqual(migrated, {
      styleVersion: 5,
      themeRef: { kind: "preset", id: "light" },
      themePreset: "light",
      theme: "light",
      density: "compact",
      backgroundMode: "theme",
      backgroundUrl: "",
      backgroundDim: 18,
      backgroundPositionX: 50,
      backgroundPositionY: 50,
      terminalDefaultOpen: true,
      showEventLog: false,
    });
    assert.deepEqual(JSON.parse(storage.getItem(appearancePrefsKey)), migrated);

    controller.saveAppearancePreferences({ ...migrated, themePreset: "dark" });
    assert.equal(JSON.parse(storage.getItem(appearancePrefsKey)).themePreset, "dark");
    assert.equal(JSON.parse(storage.getItem(appearancePrefsKey)).theme, "dark");
    assert.equal(JSON.parse(storage.getItem(appearancePrefsKey)).styleVersion, 5);
  });
});

test("appearance backup import and export normalize the new schema without rejecting old backups", () => {
  const storage = new MemoryStorage();

  withBrowserStorage(storage, () => {
    const controller = createController({ settings: { version: "test" } });
    const imported = controller.restoreLocalPreferencesBackup(JSON.stringify({
      kind: localPreferenceBackupKind,
      version: 1,
      preferences: {
        [appearancePrefsKey]: { theme: "dark", density: "comfortable" },
      },
    }));

    assert.equal(imported, 1);
    assert.deepEqual(JSON.parse(storage.getItem(appearancePrefsKey)), {
      styleVersion: 5,
      themeRef: { kind: "preset", id: "light" },
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
    assert.deepEqual(controller.createLocalPreferencesBackup().preferences[appearancePrefsKey], {
      styleVersion: 5,
      themeRef: { kind: "preset", id: "light" },
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
  });
});

test("Codex and Anthropic account consoles use the available desktop width for account actions", async () => {
  const styles = await readStylesSource(stylesURL);

  assert.match(styles, /#settingsModal \.settings-page-frame:has\(\.codex-account-console\)\s*\{[\s\S]*?width:\s*100%;[\s\S]*?max-width:\s*none;/);
  assert.match(styles, /#settingsContentBody \.codex-oauth-account-table th:nth-child\(7\)\s*\{\s*width:\s*22%;\s*\}/);
  assert.match(styles, /#settingsContentBody \.codex-oauth-account-table th:nth-child\(9\)\s*\{\s*width:\s*19%;\s*\}/);
  assert.match(styles, /#settingsContentBody \.codex-account-actions\s*\{[\s\S]*?border-radius:\s*9px;[\s\S]*?background:\s*var\(--settings-muted\)/);
  assert.match(styles, /#settingsContentBody \.anthropic-account-table th:nth-child\(8\)\s*\{\s*width:\s*22%;\s*\}/);
  assert.match(styles, /#settingsContentBody \.codex-browser-login-body\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\) auto;/);
  assert.match(styles, /#settingsContentBody \.codex-browser-login-actions\s*\{[\s\S]*?justify-content:\s*flex-end;/);
  assert.match(styles, /@media \(max-width: 767px\)[\s\S]*?#settingsContentBody \.codex-browser-login-body \{ grid-template-columns:\s*minmax\(0, 1fr\);/);
  assert.match(styles, /@media \(max-width: 767px\)[\s\S]*?#settingsContentBody \.codex-accounts-panel \.codex-account-table-wrap \{ overflow:\s*visible;/);
});

test("agent model pools stay compact after redundant lower model sections are removed", async () => {
  const styles = await readStylesSource(stylesURL);

  assert.match(styles, /#settingsContentBody \.agent-model-pool-details\s*\{[\s\S]*?overflow:\s*hidden;/);
  assert.match(styles, /#settingsContentBody \.agent-model-pool-summary\s*\{[\s\S]*?min-height:\s*40px;/);
  assert.match(styles, /#settingsContentBody \.agent-model-pool-options\s*\{[\s\S]*?max-height:\s*150px;/);
});

test("model provider settings styles remain scoped, responsive, and independent from legacy cards", async () => {
  const styles = await readStylesSource(stylesURL);
  const marker = "/* Model provider settings. Scoped after legacy settings overrides by design. */";
  const blockIndex = styles.lastIndexOf(marker);
  const providerStyles = styles.slice(blockIndex);

  assert.ok(blockIndex > styles.lastIndexOf(".legacy-settings-content-body .settings-provider-card"));
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-page\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-stat-grid\s*\{[\s\S]*?grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-toolbar,/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-head\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-visually-hidden\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-search\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-add-provider-btn,/);
  assert.match(providerStyles, /#settingsContentBody \.mp-action,/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-grid\s*\{[\s\S]*?repeat\(3, minmax\(0, 1fr\)\)/);
  assert.match(providerStyles, /@media \(min-width: 1360px\)\s*\{[\s\S]*?\.mp-provider-grid\s*\{[\s\S]*?repeat\(4, minmax\(0, 1fr\)\)/);
  assert.match(providerStyles, /@media \(max-width: 1120px\)\s*\{[\s\S]*?\.mp-provider-grid\s*\{[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(providerStyles, /@media \(max-width: 767px\)\s*\{[\s\S]*?\.mp-provider-grid\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\)/);
  assert.match(providerStyles, /\.mp-provider-card\.is-disabled[\s\S]*?opacity:\s*\.68/);
  assert.match(providerStyles, /\.mp-status-badge\s*\{/);
  assert.match(providerStyles, /\.mp-model-chip\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-switch\s*\{/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-delete\s*\{/);
  assert.doesNotMatch(providerStyles, /(?:^|\n)\s*\.mp-provider-(?:switch|delete)\b/m);
  assert.match(providerStyles, /\.mp-provider-type-modal\s*\{[\s\S]*?z-index:\s*90/);
  assert.match(providerStyles, /\.mp-modal-panel\s*\{[\s\S]*?width:\s*min\(680px, 100%\)/);
  assert.match(providerStyles, /\.mp-provider-type-grid\s*\{[\s\S]*?repeat\(2, minmax\(0, 1fr\)\)/);
  assert.match(providerStyles, /\.mp-provider-drawer-backdrop\s*\{[\s\S]*?z-index:\s*80/);
  assert.match(providerStyles, /\.mp-provider-drawer\s*\{[\s\S]*?width:\s*min\(540px, 100vw\)[\s\S]*?grid-template-rows:\s*auto minmax\(0, 1fr\) auto/);
  assert.match(providerStyles, /\.mp-provider-drawer-body\s*\{[\s\S]*?overflow:\s*auto/);
  assert.match(providerStyles, /\.mp-provider-drawer \.mp-drawer-body\s*\{[\s\S]*?overflow:\s*auto/);
  assert.match(providerStyles, /\.mp-provider-drawer-footer\s*\{[\s\S]*?position:\s*sticky/);
  assert.match(providerStyles, /\.mp-provider-drawer \.mp-drawer-foot\s*\{[\s\S]*?position:\s*sticky/);
  assert.match(providerStyles, /\.mp-provider-drawer \.mp-config-section\s*\{/);
  assert.match(providerStyles, /\.mp-provider-drawer \.codex-account-table-wrap,[\s\S]*?overflow-x:\s*auto/);
  assert.match(providerStyles, /#settingsContentBody:has\(\.mp-provider-reference-layout\)\s*\{[\s\S]*?max-width:\s*none;/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-reference-layout\s*\{[\s\S]*?width:\s*100%;/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-flat-switch-track\s*\{[\s\S]*?border-radius:\s*999px;/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-reference-protocol \.mp-provider-create-protocol-options\s*\{[\s\S]*?display:\s*flex;/);
  assert.match(providerStyles, /#settingsContentBody \.mp-provider-secret-toggle\[aria-pressed="true"\] \.mp-provider-secret-icon-hide/);
  assert.match(providerStyles, /@media \(max-width: 767px\)[\s\S]*?#settingsContentBody:has\(\.mp-provider-reference-layout\)/);
  assert.doesNotMatch(providerStyles, /(?:^|\n)\s*width:\s*1120px;/m);
  assert.match(providerStyles, /body\.white-shell\.theme-light\.theme-dark #settingsContentBody \.mp-provider-page,/);
  assert.match(providerStyles, /body\.ui-density-compact #settingsContentBody \.mp-provider-page/);
  assert.match(providerStyles, /:focus-visible\s*\{/);
  assert.match(providerStyles, /@media \(prefers-reduced-motion: reduce\)/);
  assert.doesNotMatch(providerStyles, /\.settings-provider-card|\.settings-status-strip|\.settings-hero-card/);
  assert.doesNotMatch(providerStyles, /settingsCategoryNav|specBoardBtn|taskList|legacy-settings-category/);
  assert.ok(styles.trimEnd().endsWith(providerStyles.trimEnd()), "provider CSS must remain the final stylesheet block");
});

test("opening a utility panel does not collapse the app shell's 4th grid column to zero width", async () => {
  const styles = await readStylesSource(stylesURL);
  // The dual-rail compact navigation block unconditionally zeroes the 4th
  // grid column whenever .terminal-collapsed is present. Every "open panel"
  // flow (details / background tasks / preview) also collapses the terminal
  // first, so without this override the newly-opened panel would sit in a
  // zero-width column and never become visible.
  assert.match(
    styles,
    /\.app-shell\.terminal-collapsed\.details-open,[\s\S]*?\.app-shell\.terminal-collapsed\.background-tasks-open,[\s\S]*?\.app-shell\.terminal-collapsed\.preview-open[\s\S]*?\{[\s\S]*?grid-template-columns:[\s\S]*?clamp\(380px, calc\(50vw - 186px\), 620px\)/,
  );
});

test("finishing a run no longer auto-opens the run summary review card", async () => {
  const appMain = await readFile(appMainURL, "utf8");
  const handlerStart = appMain.indexOf("async function handleAgentStreamEvent");
  assert.ok(handlerStart >= 0);
  const handlerEnd = appMain.indexOf("\nfunction captureAgentSettingsSnapshot", handlerStart);
  assert.ok(handlerEnd > handlerStart);
  const handler = appMain.slice(handlerStart, handlerEnd);
  assert.doesNotMatch(handler, /loadRunSummary\(runId/);
  assert.match(handler, /terminalAgentEvents\.includes\(event\.type\)/);
  // The card must stay reachable on demand elsewhere (e.g. the overview "runs" link).
  assert.match(appMain, /const summary = await loadRunSummary\(run\.id, \{ agentId: run\.agentId \}\);/);
});

test("model picker groups every provider once and lists all of its models underneath", async () => {
  const qionggemeGroup = { label: "qionggeme" };
  const lanyangyangGroup = { label: "lanyangyang" };
  const options = [
    { value: "qionggeme:gpt-5.6-luna", textContent: "gpt-5.6-luna", dataset: { provider: "qionggeme" }, parentElement: qionggemeGroup },
    { value: "lanyangyang:gpt-5.6-sol", textContent: "gpt-5.6-sol", dataset: { provider: "lanyangyang" }, parentElement: lanyangyangGroup },
    { value: "qionggeme:gpt-5.6-terra", textContent: "gpt-5.6-terra", dataset: { provider: "qionggeme" }, parentElement: qionggemeGroup },
  ];
  assert.deepEqual(groupModelSelectOptions(options).map((group) => ({
    provider: group.provider,
    models: group.options.map((option) => option.textContent),
  })), [
    { provider: "qionggeme", models: ["gpt-5.6-luna", "gpt-5.6-terra"] },
    { provider: "lanyangyang", models: ["gpt-5.6-sol"] },
  ]);

  const [uiShell, styles] = await Promise.all([
    readFile(uiShellURL, "utf8"),
    readStylesSource(stylesURL),
  ]);
  assert.match(uiShell, /groupModelSelectOptions\(options\)\.forEach/);
  assert.match(uiShell, /appendModelOptionGroups\(binding, options, \{ mobile: true \}\)/);
  assert.match(uiShell, /appendModelOptionGroups\(binding, menu\)/);
  assert.doesNotMatch(uiShell, /composer-model-option-provider/);
  assert.match(styles, /\.composer-model-group-heading\.composer-model-group-start\s*\{[\s\S]*?border-top:\s*1px solid/);
  assert.match(styles, /\.mobile-model-group-heading\.composer-model-group-start/);
});
