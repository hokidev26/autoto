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
  compactComposerModelLabel,
  createUIShellController,
  defaultSidebarWidth,
  globalRailCollapsedPreferenceKey,
  globalRailCollapsedWidth,
  globalRailExpandedWidth,
  groupModelSelectOptions,
  maxSidebarWidth,
  navigationDragColumnsEnterWidth,
  navigationDragColumnsExitWidth,
  navigationDragIconsEnterWidth,
  navigationDragIconsExitWidth,
  navigationLayoutModeFromDragWidth,
  navigationLayoutModePreferenceKey,
  navigationLayoutModes,
  nextNavigationLayoutMode,
  narrowSidebarMinWidth,
  sessionSidebarCollapsedPreferenceKey,
  minSidebarWidth,
  normalizeCollapsedPreference,
  normalizeNavigationLayoutMode,
  normalizeSidebarWidth,
  orderPermissionMenuOptions,
  permissionMenuPrimaryValues,
  permissionMenuSecondaryValues,
  sidebarWidthFromPointer,
  sidebarWidthPreferenceKey,
} from "./ui-shell.mjs";

const staticRoot = new URL("../", import.meta.url);
const indexURL = new URL("index.html", staticRoot);
const oauthAppURL = new URL("oauth-app.html", staticRoot);
const faviconICOURL = new URL("favicon.ico", staticRoot);
const favicon16URL = new URL("icons/autoto-tab-16.png", staticRoot);
const favicon32URL = new URL("icons/autoto-tab-32.png", staticRoot);
const appURL = new URL("app.js", staticRoot);
const appMainURL = new URL("modules/app-main.mjs", staticRoot);
const overviewDashboardURL = new URL("modules/overview-dashboard.mjs", staticRoot);
const i18nURL = new URL("modules/i18n.mjs", staticRoot);
const backgroundTasksURL = new URL("modules/background-tasks.mjs", staticRoot);
const agentWorkspaceHelpersURL = new URL("modules/agent-workspace-helpers.mjs", staticRoot);
const chatRenderingURL = new URL("modules/chat-rendering.mjs", staticRoot);
const chatRenderingMessagesURL = new URL("modules/messages-chat-rendering-extra.mjs", staticRoot);
const appMainStreamURL = new URL("modules/app-main-stream.mjs", staticRoot);
const navigationOrderURL = new URL("modules/navigation-order.mjs", staticRoot);
const chatRenderingSplitURLs = [
  chatRenderingURL,
  new URL("modules/chat-rendering-messages.mjs", staticRoot),
  new URL("modules/chat-rendering-tools.mjs", staticRoot),
  new URL("modules/chat-rendering-plan.mjs", staticRoot),
  new URL("modules/chat-rendering-correction.mjs", staticRoot),
  new URL("modules/chat-rendering-attachments.mjs", staticRoot),
  new URL("modules/chat-rendering-history.mjs", staticRoot),
];

async function readChatRenderingSource() {
  return (await Promise.all(chatRenderingSplitURLs.map((url) => readFile(url, "utf8")))).join("\n");
}

async function readAppMainSource() {
  const [main, stream, order] = await Promise.all([
    readFile(appMainURL, "utf8"),
    readFile(appMainStreamURL, "utf8"),
    readFile(navigationOrderURL, "utf8"),
  ]);
  return `${main}\n${stream}\n${order}`;
}
const directoryBrowserURL = new URL("modules/directory-browser.mjs", staticRoot);
const settingsPreferencesURL = new URL("modules/settings-preferences.mjs", staticRoot);
const stylesURL = new URL("styles.css", staticRoot);
const uiShellURL = new URL("modules/ui-shell.mjs", staticRoot);
const selectMenusURL = new URL("modules/composer-select-menus.mjs", staticRoot);

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

test("browser tabs use the blue smile favicon across entry points", async () => {
  const [html, oauthHTML, favicon16, favicon32, faviconICO] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(oauthAppURL, "utf8"),
    readFile(favicon16URL),
    readFile(favicon32URL),
    readFile(faviconICOURL),
  ]);

  for (const entryHTML of [html, oauthHTML]) {
    assert.match(entryHTML, /rel="icon" href="\/ui\/icons\/autoto-tab-32\.png" type="image\/png" sizes="32x32"/);
    assert.match(entryHTML, /rel="icon" href="\/ui\/icons\/autoto-tab-16\.png" type="image\/png" sizes="16x16"/);
    assert.match(entryHTML, /rel="shortcut icon" href="\/ui\/favicon\.ico"/);
  }
  assert.deepEqual([favicon16.readUInt32BE(16), favicon16.readUInt32BE(20)], [16, 16]);
  assert.deepEqual([favicon32.readUInt32BE(16), favicon32.readUInt32BE(20)], [32, 32]);
  assert.equal(faviconICO.readUInt16LE(0), 0);
  assert.equal(faviconICO.readUInt16LE(2), 1);
  assert.ok(faviconICO.readUInt16LE(4) >= 2);
});

// The browser keys ES module identity on the full URL, so a ?v= query string on
// any import forks that module into a second instance with its own state (the
// i18n locale split was the worst case). Freshness comes from the server's
// content ETag + Cache-Control: no-cache revalidation instead, so no source
// file may reference a ?v= stamp again.
test("static sources carry no ?v= cache-busting query strings", async () => {
  const { readdir } = await import("node:fs/promises");
  const { fileURLToPath } = await import("node:url");
  const root = fileURLToPath(staticRoot);
  const offenders = [];
  for (const relative of await readdir(root, { recursive: true })) {
    const name = String(relative).replaceAll("\\", "/");
    if (!/\.(mjs|js|html|css)$/.test(name) || name.endsWith(".test.mjs")) continue;
    const source = await readFile(new URL(name, staticRoot), "utf8");
    if (/\?v=[A-Za-z0-9-]/.test(source)) offenders.push(name);
  }
  assert.deepEqual(offenders, []);
});

test("white shell adds the global rail before the conversation sidebar with the expected targets", async () => {
  const [html, appMain] = await Promise.all([
    readFile(indexURL, "utf8"),
    readAppMainSource(),
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
  // The session column's own collapse arrow is retired: the rail's three-way
  // cycle is the only navigation collapse control.
  assert.doesNotMatch(html, /id="sessionSidebarCollapseBtn"/);

  const ids = [...html.matchAll(/\sid="([^"]+)"/g)].map((match) => match[1]);
  assert.equal(new Set(ids).size, ids.length, "white shell must not introduce duplicate IDs");
});

test("desktop home overview stays available while mobile starts in conversation", async () => {
  const [html, appMain, overviewDashboard, styles, themeRuntime, messagesCN, messagesTW, messagesEN] = await Promise.all([
    readFile(indexURL, "utf8"), readAppMainSource(), readFile(overviewDashboardURL, "utf8"), readStylesSource(stylesURL),
    readFile(new URL("../theme-runtime.css", import.meta.url), "utf8"),
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
  assert.match(appMain, /createOverviewDashboardController\(\{[\s\S]*?request: api,[\s\S]*?host: "#overviewDashboard",[\s\S]*?translate: t,[\s\S]*?formatDateTime,[\s\S]*?getLauncherContext: overviewLauncherContext,[\s\S]*?onLaunch: launchOverviewPrompt,[\s\S]*?onChooseDirectory:[\s\S]*?onNavigate: handleOverviewNavigation/);
  const initStart = appMain.indexOf("async function init()");
  const initEnd = appMain.indexOf("function openRequestedInitialView", initStart);
  const initBody = appMain.slice(initStart, initEnd);
  assert.match(initBody, /await Promise\.all\([\s\S]*?\);\s*if \(seq !== state\.initSeq\) return;[\s\S]*?state\.profile = loadProfilePreferences\(\);[\s\S]*?renderModelOptions\(\);[\s\S]*?navigationRefresh\.start\(\);[\s\S]*?if \(!state\.agent && startupTokenCurrent\(startupToken\)/);
  assert.doesNotMatch(initBody, /if \(!startupTokenCurrent\(startupToken\)\) return;/);
  assert.match(initBody, /if \(seq === state\.initSeq\) \{\s*installDesktopDeepLinkRouter\(/);
  assert.match(appMain, /autoto:auth-changed[\s\S]*?if \(state\.initializing\) state\.initRestartRequested = true[\s\S]*?init\(\)\.catch\(showError\)/);
  assert.match(initBody, /const restartRequested = seq === state\.initSeq && state\.initRestartRequested[\s\S]*?queueMicrotask\(restart\)/);
  assert.match(appMain, /async function launchOverviewPrompt\([\s\S]*?resolveTopNavigationProjectId\([\s\S]*?selectProject\(selectedProjectId, \{ preserveMessageState: true \}\)[\s\S]*?saveReasoningEffort\([\s\S]*?setMessageInputValue\(prompt, \{ saveDraft: false \}\)[\s\S]*?sendMessage\(\{ preventDefault\(\) \{\} \}\)/);
  assert.doesNotMatch(appMain, /createStandaloneConversation|\/api\/conversations/);
  assert.match(appMain, /key === "home"[\s\S]*?openOverviewDashboard\(\)\.catch\(showError\)/);
  assert.match(appMain, /overviewDashboard\.load\(\)/);
  assert.match(appMain, /createNavigationStartupGuard/);
  assert.match(appMain, /const startupToken = navigationStartupGuard\.beginInit\(seq\)/);
  assert.match(appMain, /function beginNavigationSelection\(project, options = \{\}\)[\s\S]*?navigationStartupGuard\.beginUserNavigation\(\)/);
  assert.match(appMain, /source: "startup",\s*startupToken/);
  assert.match(appMain, /if \(startupTokenCurrent\(startupToken\)\)/);
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
  // Both selection entry points leave the overview unless the caller asked to
  // keep it. selectProject names the decision because it also has to know, a
  // few lines later, whether this call is the exact no-op of re-selecting the
  // project already open -- returning from the overview is real work and must
  // not be skipped.
  assert.equal((appMain.match(/state\.overviewActive && options\.preserveOverview !== true/g) || []).length, 2);
  assert.match(appMain, /const leavingOverview = state\.overviewActive && options\.preserveOverview !== true;\s*\r?\n\s*if \(leavingOverview\) switchPrimaryWorkbench\("conversation"\);/);
  assert.match(appMain, /if \(state\.overviewActive && options\.preserveOverview !== true\) switchPrimaryWorkbench\("conversation"\);/);
  assert.match(appMain, /preserveOverview: startup\.overviewActive/);
  assert.match(appMain, /mobile:\s*isMobileAppViewport\(\)/);
  assert.match(appMain, /function leaveOverviewForMobile\(\)[\s\S]*?state\.overviewActive = false;[\s\S]*?applyPrimaryWorkbench\("conversation"\)/);
  assert.match(appMain, /async function openOverviewDashboard\(\)[\s\S]*?if \(isMobileAppViewport\(\)\)[\s\S]*?state\.overviewActive = false;[\s\S]*?return;/);
  assert.match(html, /id="schedulePanel" class="schedule-workspace-panel hidden"/);
  assert.match(appMain, /function openOverviewSchedules\(id = ""\)[\s\S]*?scheduleWorkspace\.load[\s\S]*?switchPrimaryWorkbench\("schedules"\)[\s\S]*?scheduleWorkspace\.select/);
  assert.doesNotMatch(appMain, /function openOverviewSchedules\(id = ""\)[\s\S]*?openSettingsModal\("im-gateway"\)/);
  assert.match(styles, /Desktop home: a centred greeting and composer card, then the usage heatmap/);
  assert.match(styles, /\.overview-dashboard-page\s*\{[\s\S]*?overflow:\s*auto/);
  assert.match(styles, /\.overview-hero-root\s*\{[\s\S]*?width:\s*min\(900px, 100%\)/);
  // The home composer is one rounded card: the frame and the focus ring belong
  // to the card, the textarea inside it is borderless, and the toolbar shares
  // the card's bottom edge. Matched against the rule body only ([^}]*), because
  // a lazy [\s\S]*? happily runs past the closing brace and finds the value in
  // some unrelated rule.
  const launcherRule = (selector) => new RegExp(`\\${selector}\\s*\\{[^}]*`);
  const launcherBody = (selector) => styles.match(launcherRule(selector))?.[0] || "";
  const launcherFormBody = launcherBody(".overview-launcher-form");
  assert.match(launcherFormBody, /box-shadow:\s*none/);
  assert.match(launcherFormBody, /border:\s*0/);
  const launcherCardBody = launcherBody(".overview-launcher-card");
  assert.match(launcherCardBody, /border-radius:\s*18px/);
  assert.match(launcherCardBody, /background:\s*var\(--ws-card/);
  assert.match(styles, /\.overview-launcher-card:focus-within\s*\{[^}]*border-color/);
  const launcherInputBody = launcherBody(".overview-launcher-input");
  assert.match(launcherInputBody, /border:\s*0/);
  assert.match(launcherInputBody, /background:\s*transparent/);
  assert.match(launcherInputBody, /max-height:\s*200px/);
  // The toolbar splits into a workspace group left and a model group right.
  assert.match(launcherBody(".overview-launcher-toolbar"), /justify-content:\s*space-between/);
  // The send button is a round icon control inside the card's toolbar.
  const launcherSendBody = launcherBody(".overview-launcher-send");
  assert.match(launcherSendBody, /width:\s*34px/);
  assert.match(launcherSendBody, /height:\s*34px/);
  assert.match(launcherSendBody, /border-radius:\s*999px/);
  assert.doesNotMatch(launcherSendBody, /position:\s*absolute/);
  // The suggestion chips were removed with the bottom-aligned composer pass.
  assert.doesNotMatch(styles, /\.overview-launcher-suggestion/);
  // Themed installs must re-point both send buttons together, and the themed
  // input treatment lands on the card (the textarea inside stays transparent).
  assert.match(themeRuntime, /\.composer-send-btn,\s*\.overview-launcher-send/);
  assert.match(themeRuntime, /\.message-input,\s*\.overview-launcher-card/);
  assert.doesNotMatch(overviewDashboard, /overview-launcher-project-row/);
  assert.doesNotMatch(styles, /\.overview-launcher-mode(?:-group)?/);
  assert.doesNotMatch(overviewDashboard, /data-overview-launcher-(?:action="mode"|mode=)/);
  // The stat columns and resume lists left with the redesign.
  assert.doesNotMatch(styles, /\.overview-columns|\.overview-side-column|\.overview-stats-rows/);
  assert.match(styles, /overview-mode :is\(#conversationPanel, #workbenchPanel, #schedulePanel\)[\s\S]*?display:\s*none !important/);
  assert.match(styles, /not\(\.overview-mode\) #overviewDashboard[\s\S]*?display:\s*none !important/);
  assert.match(styles, /@media \(max-width:\s*767px\)\s*\{\s*body\.white-shell\.theme-light \.overview-dashboard-page\s*\{\s*display:\s*none !important;/);
  assert.match(styles, /theme-dark \.overview-dashboard-page/);
  for (const messages of [messagesCN, messagesTW, messagesEN]) {
    assert.match(messages, /nav:\s*\{\s*home:/);
    assert.match(messages, /overview:\s*\{/);
    assert.match(messages, /runningAgents:/);
    assert.match(messages, /greetingFallback:/);
    assert.match(messages, /projectRequired:/);
    assert.doesNotMatch(messages, /continueWorking:|inProgress:|upcoming:|pendingHint:/);
  }
});

test("dual workbench shell keeps conversation and Kanban views in one runtime", async () => {
  const [html, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readAppMainSource(),
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
  assert.match(appMain, /function renderWorkbenchHeaderIdentityNow\(\)[\s\S]*?renderAgentTitleEditor\("workbench"\)/);
  // The header renders are asked for from four independent places while a
  // switch is in flight, so they are coalesced to one leading run plus at most
  // one trailing run per frame. The public names stay hoisted functions: they
  // are passed to the helper factories during module evaluation, above the
  // coalesced consts, where a const would still be in its dead zone.
  assert.match(appMain, /const coalescedWorkbenchHeaderIdentity = coalescePerFrame\(renderWorkbenchHeaderIdentityNow\)/);
  assert.match(appMain, /const coalescedConversationHeaderIdentity = coalescePerFrame\(renderConversationHeaderIdentityNow\)/);
  assert.match(appMain, /function renderWorkbenchHeaderIdentity\(\) \{ return coalescedWorkbenchHeaderIdentity\(\); \}/);
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
    readAppMainSource(),
    readStylesSource(stylesURL),
  ]);
  const selectStart = appMain.indexOf("async function selectProject");
  const selectEnd = appMain.indexOf("async function selectNavigationConversation", selectStart);
  const selectBody = appMain.slice(selectStart, selectEnd);

  assert.match(selectBody, /const preserveConversationView = Boolean\([\s\S]*?state\.agent\?\.id/);
  assert.match(selectBody, /beginNavigationSelection\(project, \{[\s\S]*?preserveConversationView,[\s\S]*?selectionKind: "project",/);
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
    readAppMainSource(),
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
});

test("storage settings cards keep visible vertical spacing", async () => {
  const [html, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
  ]);

  assert.match(styles, /body\.white-shell\.theme-light \.legacy-settings-content-body \.storage-entry-list\s*\{[\s\S]*?gap:\s*12px/);
});

test("network proxy settings remove duplicate agent management while keeping the backend modal", async () => {
  const [html, app, appMain, settingsData, settingsCategories, backendRegistry] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readAppMainSource(),
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
});

test("folder picker uses stable SVG actions and directory icons instead of font glyphs", async () => {
  const [html, directoryBrowser, styles, appMain, app] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(directoryBrowserURL, "utf8"),
    readStylesSource(stylesURL),
    readAppMainSource(),
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
});

test("conversation sidebar exposes one project navigation without a standalone filter", async () => {
  const [html, app, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readAppMainSource(),
    readStylesSource(stylesURL),
  ]);
  const header = html.slice(html.indexOf('<header class="session-sidebar-header">'), html.indexOf("</header>", html.indexOf('<header class="session-sidebar-header">')));

  assert.match(header, /class="session-sidebar-title"[^>]*>会话</);
  for (const id of ["projectSearchToggleBtn", "newProjectBtn", "refreshBtn"]) {
    assert.match(header, new RegExp(`id="${id}"`));
  }
  assert.doesNotMatch(html, /class="nav-stack"/);
  assert.doesNotMatch(html, /navigationFilters|data-navigation-mode="conversations"|data-create-conversation|mobileNewConversationBtn/);
  assert.doesNotMatch(html, /navigationListHeading|navigation-list-heading/);
  assert.doesNotMatch(styles, /\.navigation-list-heading/);
  assert.doesNotMatch(appMain, /navigationListHeading|createStandaloneConversation|\/api\/conversations/);
  assert.match(appMain, /navigationMode:\s*"projects"/);
  assert.match(html, /id="recentSidebarConversations"/);
  // The recent-directories sidebar block is gone from the shell. It used to be
  // hidden only inside @media (max-width: 767px), so the desktop window kept
  // rendering an always-empty "recent folders" section.
  assert.doesNotMatch(html, /id="recentSidebarDirectories"/);
  assert.match(html, /id="globalThemeToggleBtn"/);
  assert.match(html, /id="globalHealthText"/);
  assert.match(styles, /\.navigation-conversation-row\.conv-drag-over,\s*\.proj-drag-over\s*\{[\s\S]*?outline:\s*2px solid var\(--accent\)/);
});

test("all interactive creation routes use project selection and never call the removed API", async () => {
  const [html, app, appMain] = await Promise.all([readFile(indexURL, "utf8"), readFile(appURL, "utf8"), readAppMainSource()]);
  const desktop = html.match(/<button id="newProjectBtn"[^>]*>/)?.[0] || "";
  const mobile = html.match(/<button id="mobileChooseDirectoryBtn"[^>]*>/)?.[0] || "";
  assert.match(desktop, /data-create-navigation-item/);
  assert.match(desktop, /data-i18n-title="shell\.chooseFolder"/);
  assert.match(mobile, /data-open-directory-shortcut="current"/);
  assert.doesNotMatch(html, /data-create-conversation|mobileNewConversationBtn|data-navigation-mode="conversations"/);
  assert.match(appMain, /return navigationCreateTarget\(state\)/);
  assert.match(appMain, /const labelKey = navigationCreateLabelKey\(target\)/);
  assert.match(appMain, /async function createNavigationItem\(trigger = null\)/);
  assert.match(appMain, /const target = currentNavigationCreateTarget\(\)[\s\S]*?if \(target === "schedule"\) return startScheduleCreation\(\)[\s\S]*?openDirectoryChooser\(state\.project\?\.gitPath \|\| state\.agent\?\.cwd \|\| "", \{ trigger \}\)/);
  assert.match(appMain, /\[data-create-navigation-item\][\s\S]*?createNavigationItem\(button\)/);
  assert.doesNotMatch(`${app}${appMain}`, /createStandaloneConversation|\/api\/conversations|standaloneConversationCreating/);
});

test("conversation navigation exposes archive, pin, and accessible context-menu controls", async () => {
  const [html, appMain, navigation, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readAppMainSource(),
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

test("project, task, and schedule modes expose separate creation boundaries", async () => {
  const [html, appMain, styles] = await Promise.all([
    readFile(indexURL, "utf8"),
    readAppMainSource(),
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
  assert.doesNotMatch(html, /id="navigationFilters"|data-navigation-mode="conversations"/);
  assert.doesNotMatch(html, /recent-directories-sidebar/);
  // The boundary this test guards is the task sidebar staying flat: there the
  // project is the unit of work, not the conversations under it. The
  // conversation sidebar shows the tree at every width -- it used to depend on
  // the compact/expanded split, which meant collapsing the sidebar revealed more
  // structure than widening it, and forks had nowhere to render at normal width.
  assert.match(appMain, /const effectiveNavigationMode = taskContext \? "projects" : "all"/);
  assert.doesNotMatch(appMain, /compactSessionSidebar \? "all"/);
  assert.match(appMain, /state\.navigationMode = "projects"/);
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
    readAppMainSource(),
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
  // The composer directory button is gone: the sidebar + and the mobile
  // top-bar button reach the same openDirectoryChooser, so a third entry on
  // the toolbar only spent 28px repeating them.
  assert.doesNotMatch(composer, /class="composer-actions"/);
  assert.doesNotMatch(composer, /id="composerFolderBtn"/);
  assert.doesNotMatch(composer, /id="composerTerminalBtn"/);
  assert.match(styles, /body\.white-shell\.theme-light:not\(\.project-operation-context\) \[data-project-context-only\]\s*\{[^}]*display:\s*none !important/);
  assert.match(styles, /body\.white-shell\.theme-light:not\(\.project-operation-context\) :is\(\.composer-actions, \.composer-message-mode-field, \.composer-permission-field\) \{ display: none !important; \}/);
  assert.doesNotMatch(appMain, /messageModeToggle/);
  assert.doesNotMatch(styles, /\.composer-field-label,\s*\.composer-actions,[\s\S]{0,180}\.composer-permission-field\s*\{ display: none !important; \}/);
  assert.match(appMain, /navigationSelectionKind:\s*"project"/);
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
    readAppMainSource(),
  ]);
  assert.match(html, /id="openProviderLoginBtn"[^>]*class="[^"]*toolbar-lightning-btn[^"]*hidden[^"]*"[^>]*aria-pressed="false"[^>]*data-i18n-title="chat\.fastModeDisabled"/);
  assert.match(appMain, /openProviderLoginBtn"\)\?\.addEventListener\("click", \(\) => toggleFastMode\(\)\.catch\(showError\)\)/);
  assert.doesNotMatch(appMain, /openProviderLoginBtn"\)\.addEventListener\("click", \(\) => openSettingsModal\("providers"\)\)/);
  assert.match(styles, /\.toolbar-lightning-btn:not\(\.hidden\)\s*\{[\s\S]*?border:\s*0;[\s\S]*?background:\s*transparent;[\s\S]*?box-shadow:\s*none/);
  assert.match(styles, /\.toolbar-lightning-btn\.fast-mode-active\s*\{[\s\S]*?border:\s*0;[\s\S]*?background:\s*transparent;[\s\S]*?box-shadow:\s*none/);
  assert.match(styles, /\.toolbar-lightning-btn\.fast-mode-active svg\s*\{[\s\S]*?fill:\s*currentColor/);
});

test("permission mode display targets only the permission toolbar pill", async () => {
  const appMain = await readAppMainSource();
  assert.match(appMain, /querySelector\("\.permission-toolbar-pill \.mode-display"\)/);
  assert.doesNotMatch(appMain, /querySelector\("\.mode-display"\)/);
});

test("chat header exposes the legacy six-tool order with real SVG icons", async () => {
  const [html, appMain] = await Promise.all([readFile(indexURL, "utf8"), readAppMainSource()]);
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
    readAppMainSource(),
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
    readAppMainSource(),
    readChatRenderingSource(),
    readFile(new URL("./conversation-navigation.mjs", import.meta.url), "utf8"),
  ]);
  const finalDesktopComposer = styles.slice(styles.indexOf("/* Final desktop full-width composer override. */"));
  assert.match(styles, /grid-template-columns:\s*76px var\(--session-sidebar-width\) minmax\(360px, 1fr\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.sidebar-resize-handle\s*\{[\s\S]*?position:\s*fixed[\s\S]*?left:\s*calc\(68px \+ var\(--session-sidebar-width\) - 3px\)/);
  assert.match(styles, /body\.white-shell\.theme-light \.chat-panel\s*\{[\s\S]*?grid-column:\s*3/);
  assert.match(styles, /body\.white-shell\.theme-light \.terminal-panel\s*\{[\s\S]*?grid-column:\s*4/);
  assert.match(styles, /body\.white-shell\.theme-light \.session-sidebar-header\s*\{[^}]*flex:\s*0 0 64px[^}]*height:\s*64px[^}]*min-height:\s*64px/);
  assert.match(styles, /body\.white-shell\.theme-light \.composer-wrap\s*\{[\s\S]*?padding:\s*6px 12px 8px/);
  assert.match(styles, /body\.white-shell\.theme-light \.message-input\s*\{[\s\S]*?min-height:\s*40px/);
  assert.match(styles, /body\.white-shell\.theme-light \.composer-send-btn\s*\{[\s\S]*?width:\s*34px/);
  assert.match(styles, /body\.white-shell\.theme-light \.navigation-project-row \.navigation-project-title\s*\{[\s\S]*?display:\s*flex[\s\S]*?gap:\s*6px/);
  // The PROJECT badge is no longer rendered: it existed to tell project rows
  // apart from standalone conversation rows, and those are one thing now. Its
  // rules are left in place as inert styling rather than asserted here.
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
  // The session column's compact stage is retired: no compact header title and
  // no collapsed-column grid variable are left in the shell.
  assert.doesNotMatch(html, /session-sidebar-compact-title/);
  assert.doesNotMatch(styles, /\.app-shell\.session-sidebar-collapsed/);
  assert.match(styles, /--global-rail-layout-width:\s*68px/);
  assert.match(styles, /\.app-shell\.global-rail-collapsed\s*\{[\s\S]*?--global-rail-layout-width:\s*48px/);
  // Icon rail: the conversation column is removed from the grid, not narrowed.
  assert.match(styles, /\.app-shell\.nav-mode-icons\s*\{[\s\S]*?--global-rail-layout-width:\s*48px[\s\S]*?--session-sidebar-layout-width:\s*0px/);
  assert.match(styles, /\.app-shell\.nav-mode-icons \.sidebar\s*\{[\s\S]*?display:\s*none/);
  // The divider survives the icon layout, because dragging is a way back out of it
  // and not only a way into it.
  assert.match(styles, /\.app-shell\.nav-mode-icons \.sidebar-resize-handle\s*\{[\s\S]*?display:\s*block[\s\S]*?left:\s*calc\(var\(--global-rail-layout-width\) - 3px\)/);
  // Docked mode lifts the search and create controls onto the conversation row,
  // and the wrapper is layout-neutral everywhere else.
  assert.match(styles, /\.global-rail-nav-row\s*\{\s*display:\s*contents/);
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-nav-row\s*\{[\s\S]*?display:\s*flex/);
  // Overlaid on the conversation entry, not laid out beside it: as flex siblings
  // they sat past the entry's background and read as loose floating icons.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-nav-row\s*\{[\s\S]*?position:\s*relative/);
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-nav-row \.session-sidebar-actions\s*\{[\s\S]*?position:\s*absolute[\s\S]*?right:\s*6px/);
  // The entry reserves room on its right so a long label cannot run underneath.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-nav-row \.global-rail-button\s*\{[\s\S]*?padding-right:\s*66px/);
  // The >=1280px rules hide the magnifier because the field is pinned open there.
  // Docked mode un-pins the field, so the magnifier must come back or the search
  // box has nothing to open it.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-nav-row #projectSearchToggleBtn\s*\{[\s\S]*?display:\s*inline-flex/);
  // The docked list is a real descendant of .global-rail, so the rail's own nav
  // entry rules have to exclude it. Without this its buttons took a 58px tall
  // full-width nav entry's shape and held every card open.
  assert.match(styles, /\.global-rail button:not\(\.global-rail-dock \*\)/);
  assert.match(styles, /\.global-rail button:not\(\.global-rail-dock \*\),[\s\S]*?\{[\s\S]*?min-height:\s*58px/);
  // The divider survives the overview page: the rail it resizes is on screen there,
  // so the layout must be changeable without opening a conversation first.
  assert.doesNotMatch(styles, /\.overview-mode :is\(#sessionSidebar, #sidebarResizeHandle/);
  assert.match(styles, /\.overview-mode #sidebarResizeHandle\s*\{[\s\S]*?display:\s*block[\s\S]*?left:\s*calc\(var\(--global-rail-layout-width\) - 3px\)/);
  assert.match(styles, /\.overview-mode \.app-shell\.nav-mode-docked #sessionSidebar\s*\{\s*display:\s*flex/);
  // Docked rows are tightened: the >=1280px padding plus a two-line title/path
  // stack made each card taller than the nav entries above it.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.navigation-conversation-row,[\s\S]*?\{[\s\S]*?min-height:\s*0[\s\S]*?padding:\s*2px 9px/);
  // The disclosure stretches to the row and carries its own floor, so it is what
  // actually holds these cards open; padding alone could not shorten them.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.navigation-disclosure\s*\{[\s\S]*?min-height:\s*26px/);
  // Morphicons-style disclosure: hairline stroke, and a curve that overshoots and
  // settles rather than sliding linearly.
  assert.match(styles, /\.navigation-disclosure svg\s*\{[\s\S]*?stroke-width:\s*1\.5/);
  assert.match(styles, /\.navigation-disclosure svg\s*\{[\s\S]*?transition:\s*transform\s+\.26s\s+cubic-bezier\(\.34,\s*1\.56,\s*\.64,\s*1\)/);
  assert.match(styles, /@media \(prefers-reduced-motion: reduce\)\s*\{[\s\S]*?\.navigation-disclosure svg\s*\{\s*transition:\s*none/);
  // The >=1280px rule pins the search field open; docked mode hands it back to the
  // magnifier so it costs no row until asked for.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.sidebar-search-wrap\.hidden\s*\{[\s\S]*?display:\s*none/);
  // Docked: the rail takes over the stored width and the second column collapses,
  // because the list is now parented inside the rail.
  assert.match(styles, /\.app-shell\.nav-mode-docked\s*\{[\s\S]*?--global-rail-layout-width:\s*var\(--session-sidebar-width[\s\S]*?--session-sidebar-layout-width:\s*0px/);
  // Dragging still works when docked, so the handle follows the rail's edge.
  assert.match(styles, /\.app-shell\.nav-mode-docked \.sidebar-resize-handle\s*\{[\s\S]*?left:\s*calc\(var\(--global-rail-layout-width\) - 3px\)/);
  // Icon-above-label becomes icon-beside-label once there is room for words.
  assert.match(styles, /\.app-shell\.nav-mode-docked[\s\S]*?\.global-rail-button[\s\S]*?flex-direction:\s*row/);
  assert.match(styles, /\.app-shell\.nav-mode-docked \.global-rail-dock\s*\{[\s\S]*?flex:\s*1 1 auto/);
  // Settings docked while the rail is in docked mode. layoutSettingsShell pins
  // the first column to the columns-mode width, so the rail is narrow while its
  // stage styles are still written for 296px. These assertions lock the compact
  // presentation that restores, matched against the rule body only ([^}]*)
  // because a lazy [\s\S]*? runs past the closing brace into unrelated rules.
  const compactRule = (selector) => styles.match(
    new RegExp(`\\.app-shell\\.nav-mode-docked\\.settings-rail-compact ${selector}\\s*\\{[^}]*`),
  )?.[0] || "";
  const compactBrand = compactRule("\\.global-rail-brand");
  assert.match(compactBrand, /flex-direction:\s*column/, "brand stacks again at rail width");
  assert.match(compactRule("\\.global-rail-brand-text"), /font-size:\s*10px/, "brand text returns to the size that fits");
  const compactLabel = compactRule("\\.global-rail-label");
  assert.match(compactLabel, /font-size:\s*10px/);
  // Without nowrap a CJK label stacked one character per line inside 68px.
  assert.match(compactLabel, /white-space:\s*nowrap/);
  // That rule is a selector group (nav entries plus the footer entry), so the
  // selector is followed by a comma rather than by the block.
  const compactButtons = styles.match(
    /\.app-shell\.nav-mode-docked\.settings-rail-compact \.global-rail-nav \.global-rail-button,[^{]*\{[^}]*/,
  )?.[0] || "";
  assert.match(compactButtons, /flex-direction:\s*column/, "entries stack icon over label");
  // The search/create pair is absolutely positioned over the conversation entry
  // in docked mode, which does not fit once the rail is 68px wide. The list it
  // acts on is hidden for the whole time settings is docked.
  assert.match(
    compactRule("\\.global-rail-nav-row \\.session-sidebar-actions"),
    /display:\s*none/,
    "the overlaid actions step aside",
  );
  // The docked values above must stay intact: they are still right at 296px.
  assert.match(
    styles,
    /\.app-shell\.nav-mode-docked \.global-rail-brand\s*\{[^}]*flex-direction:\s*row/,
    "docked keeps its horizontal brand when the rail really is wide",
  );
  // Search and create survive docking; the sidebar's own heading does not.
  assert.match(styles, /\.app-shell\.nav-mode-docked :is\([\s\S]*?\.session-sidebar-heading[\s\S]*?\)\s*\{[\s\S]*?display:\s*none/);
  // The dock sits between the conversation entry and the entries below it, which
  // is what makes the list read as belonging to that entry.
  const conversationRailEntry = html.indexOf('data-global-rail-target="conversation"');
  const conversationDock = html.indexOf('id="railConversationDock"');
  const schedulesRailEntry = html.indexOf('data-global-rail-target="schedules"');
  assert.ok(conversationDock > conversationRailEntry, "the dock follows the conversation rail entry");
  assert.ok(conversationDock < schedulesRailEntry, "the dock precedes the entries below it");
  assert.match(html, /id="railConversationDock" class="global-rail-dock" hidden/);
  assert.match(navigation, /data-theme-icon-slot="sidebar-project"/);
  // The slot name became a template expression when forks gained their own
  // icon, so it is no longer contiguous source text. Assert the row still emits
  // the attribute and that this slot is one of the values it can carry.
  assert.match(navigation, /data-theme-icon-slot="\$\{[^}]*sidebar-conversation[^}]*\}"/);
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
  // Finishing a turn must not repaint the cleared live state on its own. The
  // persisted message is 80ms behind it, so rendering the gap tears the answer
  // out of the transcript and puts an equivalent block back a frame later --
  // one visible jolt at the end of every turn. This pair only occurs in that
  // handler.
  assert.match(appMain, /clearLiveAssistantText\(\{ preserveView: true \}\);[\s\S]{0,160}?clearLiveImageGenerations\(\{ agentId, preserveView: true \}\)/);
  // Opening or switching a conversation runs applyAgentLiveSnapshot, which ends
  // in applyMessageSnapshot rebuilding the whole transcript. Every clear before
  // that paints a state about to be overwritten, so an unguarded one empties the
  // view and fills it again -- the flash on switch. The trailing
  // applyMessageSnapshot is part of the assertion because it is what makes
  // deferring safe.
  assert.match(
    appMain,
    /async function applyAgentLiveSnapshot\([\s\S]*?clearLiveAssistantText\(\{ preserveView: true \}\)[\s\S]*?clearRunSummary\(\{ preserveView: true \}\)[\s\S]*?applyMessageSnapshot\(/,
  );
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
  const [html, styles, uiShell, appMain, selectMenus] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(uiShellURL, "utf8"),
    readAppMainSource(),
    readFile(selectMenusURL, "utf8"),
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
  assert.match(selectMenus, /heading\.textContent = binding\.label\?\.textContent/);
  assert.match(selectMenus, /menu\.classList\.toggle\("composer-permission-popover", isPermissionMenu\)/);
  assert.match(selectMenus, /appendPermissionSafetyStatus\(/);
  assert.match(selectMenus, /appendMessageModeSection\(/);
  assert.match(selectMenus, /usesMobileSheet[\s\S]*permissionMode/);
  assert.doesNotMatch(uiShell, /messageModeToggle/);
  assert.doesNotMatch(selectMenus, /messageModeToggle/);
  assert.match(selectMenus, /chat\.enterPlanMode/);
  assert.match(selectMenus, /chat\.executeMode/);
  assert.match(selectMenus, /menu\.style\.bottom = `\$\{Math\.max\(8,[\s\S]*?- rect\.top \+ 6\)\}px`/);
  assert.match(selectMenus, /binding\.select\.dispatchEvent\(new EventConstructor\("change"/);
  assert.match(selectMenus, /appendModelOptionGroups\(binding, menu\)/);
  assert.match(selectMenus, /presentation\?\.provider \? `\$\{presentation\.provider\}:\$\{presentation\.name\}`/);
  assert.match(selectMenus, /!active\.mobile && \(event\.target === menu \|\| menu\.contains\(event\.target\)\)/);
  assert.match(styles, /\.composer-select-popover\s*\{[\s\S]*?overscroll-behavior:\s*contain/);
  assert.match(styles, /\.composer-model-group-heading\s*\{/);
  assert.match(appMain, /agentSavePromise:\s*null/);
  assert.match(appMain, /state\.agentSaveSnapshot = captureAgentSettingsSnapshot\(\);[\s\S]*?while \(state\.agentSavePending\)/);
  assert.match(appMain, /awaitAgentSettingsSaved:\s*\(agentId\) => waitForAgentSettingsSave\(agentId\)/);
});

test("permission menu lists every mode in the primary group, least permissive first", () => {
  const options = [
    { value: "readOnly" },
    { value: "acceptEdits" },
    { value: "bypassPermissions" },
    { value: "default" },
  ];
  assert.deepEqual(permissionMenuPrimaryValues, ["readOnly", "default", "acceptEdits", "bypassPermissions"]);
  assert.deepEqual(permissionMenuSecondaryValues, []);
  assert.deepEqual(orderPermissionMenuOptions(options).map((option) => option.value), [
    "readOnly",
    "default",
    "acceptEdits",
    "bypassPermissions",
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
    readChatRenderingSource(),
    readAppMainSource(),
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
  assert.match(backgroundTasks, /const currentText = foregroundActivity\?\.text\s*\|\|\s*summary\.current\?\.title/);
  // A lifecycle event has no title, so the label has to fall back to the counts
  // rather than to the idle string while something is still running.
  assert.match(backgroundTasks, /summary\.current\?\.title\s*\|\|\s*\(hasCurrentActivity[\s\S]*?headerTitle[\s\S]*?headerIdle/);
  assert.match(backgroundTasks, /headerButton\.classList\.toggle\("has-task", hasCurrentActivity\)/);
  assert.match(backgroundTasks, /headerQueue\.classList\.toggle\("hidden", summary\.queuedCount <= 0\)/);
  // Mobile is the only view that keeps the composer pill as the fallback. The
  // project context used to gate this too, which left an ordinary conversation
  // reporting no running task for a whole turn.
  assert.match(agentWorkspaceHelpers, /routeActivityToTaskSummary[\s\S]*?!isMobileAppViewport\?\.\(\)[\s\S]*?backgroundTasks\.setForegroundActivity\(activity\)/);
  assert.doesNotMatch(agentWorkspaceHelpers, /routeActivityToTaskSummary = Boolean\(projectOperationContextActive/);
  // Blocked on a child is a distinct state, and it has to survive as far as the dot.
  assert.match(agentWorkspaceHelpers, /waitingOnBackgroundTasks[\s\S]*?tone: "waiting"/);
  assert.match(backgroundTasks, /foregroundActivity\.tone \|\| "running"/);
  assert.match(appMain, /createAgentWorkspaceHelpers\(\{[\s\S]*?projectOperationContextActive,[\s\S]*?isMobileAppViewport,/);
  assert.match(appMain, /onMessageAccepted:\s*async[\s\S]*?state\.agent = \{ \.\.\.state\.agent, status: "running" \}[\s\S]*?refreshComposerActivityStatus\(\)/);
  assert.match(appMain, /window\.addEventListener\("resize"[\s\S]*?refreshComposerActivityStatus\(\)/);
  assert.match(chatRendering, /function renderLiveAssistantCardHTML\(\)[\s\S]*?if \(!text\) return ""/);
  assert.doesNotMatch(chatRendering, /class="live-assistant-(?:waiting|status)"/);
});

test("Subagent compact cards integrate background tasks without polling child tool calls", async () => {
  const [html, app, appMain, chatRendering, messages, styles, backgroundTasks] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readAppMainSource(),
    readChatRenderingSource(),
    readFile(chatRenderingMessagesURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(backgroundTasksURL, "utf8"),
  ]);


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
  // The status marker moved from a ::before character to an <svg> picked in
  // chat-rendering.mjs, so the card must size that svg and must not reintroduce
  // a generated-content glyph beside it.
  assert.match(cardStyles, /\.subagent-task-card \.tool-activity-icon > svg \{[\s\S]*?width: 14px/);
  assert.doesNotMatch(cardStyles, /\.tool-activity-icon::before/);
  assert.match(chatRendering, /agentTaskStatusGlyphKind\(activity\.status\)/);
  assert.match(cardStyles, /\.subagent-task-card :is\(summary, button\):focus-visible/);
  // The narrow tier follows the transcript column's own width, not the
  // window's: a docked utility panel squeezes the column while the viewport
  // stays desktop-wide, which a media query cannot see.
  assert.match(cardStyles, /@container chat-transcript \(max-width: 760px\)[\s\S]*?\.subagent-task-actions \.ghost-btn\.mini/);
  assert.doesNotMatch(cardStyles, /(?:^|\n)\s*\.tool-activity-card\s*\{/);
});

test("composer responds to its actual width before the mobile breakpoint", async () => {
  const [html, styles] = await Promise.all([readFile(indexURL, "utf8"), readStylesSource(stylesURL)]);
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
  // Hugs the pill instead of a fixed basis: the old 96px/92px bases were sized
  // for a pill that no longer exists, and the leftover ~22px read as a hole
  // between the permission control and the directory button beside it.
  assert.match(responsiveStyles, /\.composer-permission-field\s*\{[^}]*flex:\s*0 0 auto/);
  assert.doesNotMatch(responsiveStyles, /\.composer-permission-field\s*\{[^}]*flex:\s*0 0 9\dpx/);
  // The pill sizes to its longest label instead of a fixed width, so 全部允許
  // is never ellipsised, and carries no padding of its own so the trigger
  // fills it and the whole control is clickable.
  assert.match(responsiveStyles, /\.permission-toolbar-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*max-content[^}]*padding:\s*0/);
  assert.match(responsiveStyles, /\.permission-toolbar-pill \.composer-select-trigger\s*\{[^}]*width:\s*100%/);
  assert.match(responsiveStyles, /\.permission-toolbar-pill \.composer-select-value\s*\{[^}]*text-overflow:\s*clip/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*1320px\)[\s\S]*?\.composer-task-summary\s*\{[^}]*width:\s*180px[^}]*max-width:\s*180px[^}]*flex:\s*0 1 180px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-task-summary\s*\{[^}]*width:\s*30px[^}]*flex:\s*0 0 30px/);
  // Fixed rather than elastic at this tier, which is the one that governs at a
  // normal desktop width. The context ring and these controls are pushed right
  // as one group, so a model box that flexed with the length of the model id
  // moved everything to its left by up to 60px whenever the id changed.
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-model-field\s*\{[^}]*width:\s*200px[^}]*min-width:\s*200px[^}]*max-width:\s*200px[^}]*flex:\s*0 0 200px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-model-field \.composer-select-value\s*\{[^}]*text-overflow:\s*ellipsis/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-effort-field\s*\{[^}]*min-width:\s*34px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-effort-field\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.effort-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field\s*\{[^}]*width:\s*auto[^}]*min-width:\s*0[^}]*max-width:\s*none[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.effort-pill\s*\{[^}]*width:\s*auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.composer-select-value\s*\{[^}]*font-size:\s*11px[^}]*text-overflow:\s*clip/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-effort-field \.composer-select-value::before\s*\{[^}]*content:\s*none/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.composer-permission-field\s*\{[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.permission-toolbar-pill\s*\{[^}]*width:\s*auto[^}]*min-width:\s*max-content/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.message-mode-option::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*900px\)[\s\S]*?\.permission-safety-indicator\s*\{[^}]*display:\s*none/);
  // Mid-squeeze (620px) the pills keep their identity and only get tighter.
  // The model pill shows the compact family label (gpt-5.6, sonnet) from
  // data-mobile-label at natural width, instead of ellipsizing the full
  // provider:model id inside a flexible 42-88px box.
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-model-field\s*\{[^}]*width:\s*auto[^}]*flex:\s*0 0 auto/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-model-field \.composer-select-value::before\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  // The permission pill keeps its localized label at this tier; the English
  // initials (RO/ALL/...) stay exclusive to the icon rail and the phone
  // toolbar. Mid-squeeze they made one pill switch language while its
  // neighbours stayed localized.
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-permission-field \.composer-select-value\s*\{[^}]*font-size:\s*11px/);
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-permission-field \.composer-select-value::before\s*\{[^}]*content:\s*none/);
  // Every pill drops its caret together at 620px; permission had already
  // dropped its own, which left the trio inconsistent.
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.composer-toolbar \.composer-select-chevron\s*\{[^}]*display:\s*none/);
});

test("mobile header and composer use compact icon-first layouts", async () => {
  const [html, styles, appMain, app] = await Promise.all([readFile(indexURL, "utf8"), readStylesSource(stylesURL), readAppMainSource(), readFile(appURL, "utf8")]);
  const marker = "/* Compact mobile composer: one utility row plus one message row. */";
  const mobileComposerStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Model provider settings.", styles.indexOf(marker)));
  assert.ok(mobileComposerStyles.startsWith(marker));
  assert.match(html, /id="mobileSearchBtn"[\s\S]*?<svg viewBox="0 0 24 24"/);
  assert.doesNotMatch(html, /id="composerFolderBtn"/);
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
  const [styles, uiShell, selectMenus] = await Promise.all([readStylesSource(stylesURL), readFile(uiShellURL, "utf8"), readFile(selectMenusURL, "utf8")]);
  const marker = "/* Narrow composer icon rail: preserve every control at one fixed size. */";
  const iconRail = styles.slice(styles.indexOf(marker), styles.indexOf("/* Flat, single-pass settings layout", styles.indexOf(marker)));
  assert.ok(iconRail.startsWith(marker));
  assert.match(iconRail, /@container composer-shell \(max-width:\s*480px\)/);
  // The connection pill is gone from this tier rather than resized into it. On
  // desktop paintComposerStatus hands the running step to the task summary, so the
  // pill held only the connection text and still spun a ring beside it: two
  // indicators for one fact, the spinning one describing nothing. See
  // narrow-toolbar-icons.test.mjs for the rest of that rule.
  assert.match(iconRail, /\.composer-status \{\s*display:\s*none;\s*\}/);
  assert.doesNotMatch(iconRail, /\.composer-status[^{}]*\{[^}]*display:\s*(?:inline-)?flex/);
  assert.match(iconRail, /\.composer-controls\s*\{[^}]*min-width:\s*max-content[^}]*flex:\s*0 0 auto[^}]*overflow:\s*visible/);
  assert.match(iconRail, /:is\(\.composer-model-field, \.composer-effort-field, \.composer-permission-field\)\s*\{[^}]*width:\s*28px[^}]*min-width:\s*28px[^}]*max-width:\s*28px[^}]*flex:\s*0 0 28px/);
  assert.match(iconRail, /:is\(\.toolbar-model-pill, \.effort-pill, \.permission-toolbar-pill\)\s*\{[^}]*width:\s*28px[^}]*flex:\s*0 0 28px[^}]*border:\s*0[^}]*background:\s*transparent/);
  assert.match(iconRail, /\.composer-select-value\s*\{[^}]*position:\s*absolute[^}]*clip-path:\s*inset\(50%\)/);
  assert.match(iconRail, /\.composer-select-chevron\s*\{[^}]*display:\s*none/);
  assert.match(iconRail, /\.toolbar-lightning-btn:not\(\.hidden\),[\s\S]*?\.composer-toolbar-icon\s*\{[^}]*width:\s*28px[^}]*display:\s*inline-flex[^}]*border:\s*0[^}]*background:\s*transparent/);
  assert.match(iconRail, /\.model-tool-btn\.icon-only\.composer-toolbar-icon\s*\{[^}]*width:\s*28px[^}]*height:\s*30px[^}]*min-height:\s*30px/);
  // No .composer-actions sizing any more: the directory button that lived
  // in it was removed from the composer toolbar.
  assert.doesNotMatch(iconRail, /\.composer-actions\s*\{/);
  // The model trigger renders icon-only, so the composed "field：value" label is
  // the only place the selected model is stated; it must reach both assistive
  // tech and a sighted hover.
  assert.match(selectMenus, /const triggerLabel = fieldLabel \? `\$\{fieldLabel\}：\$\{displayText\}` : displayText;/);
  assert.match(selectMenus, /trigger\.setAttribute\("aria-label", triggerLabel\);/);
  assert.match(selectMenus, /trigger\.title = triggerLabel;/);
});

test("composer toolbar controls share one vertical centre line", async () => {
  const styles = await readStylesSource(stylesURL);
  const marker = "/* Responsive composer tiers follow the editor's real width, not the full viewport. */";
  const responsiveStyles = styles.slice(styles.indexOf(marker), styles.indexOf("/* Flat, single-pass settings layout", styles.indexOf(marker)));

  // workbench top-aligns the toolbar row; with 30px icon buttons beside taller
  // pills that left every glyph on its own line. The desktop block re-centres it.
  assert.match(responsiveStyles, /\.composer-toolbar\s*\{[^}]*align-items:\s*center/);

  // The 620px tier used to size pills at 32px inside the 30px row, so the
  // model/effort/permission group hung two pixels below the directory button.
  assert.match(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.permission-toolbar-pill\s*\{[^}]*height:\s*30px[^}]*min-height:\s*30px/);
  assert.doesNotMatch(responsiveStyles, /@container composer-shell \(max-width:\s*620px\)[\s\S]*?\.permission-toolbar-pill\s*\{[^}]*height:\s*32px/);

  // Block-level icon buttons render their svg on the text baseline, two pixels
  // above centre; they must be flex-centred like the pill glyphs beside them.
  assert.match(responsiveStyles, /\.composer-toolbar \.composer-toolbar-icon,[\s\S]*?\.composer-toolbar \.model-tool-btn\.icon-only,[\s\S]*?\{[^}]*display:\s*inline-flex[^}]*align-items:\s*center[^}]*justify-content:\s*center/);

  // In the <=480px icon rail the caret hide needs the .composer-toolbar hop:
  // the 620px tier re-shows the effort caret with a two-class selector that
  // outranked the old one-hop rule and squeezed a caret into the 28px slot.
  const railMarker = "/* Narrow composer icon rail: preserve every control at one fixed size. */";
  const iconRail = styles.slice(styles.indexOf(railMarker), styles.indexOf("/* Flat, single-pass settings layout", styles.indexOf(railMarker)));
  assert.match(iconRail, /\.composer-toolbar \.composer-select-chevron\s*\{\s*display:\s*none/);
  // And the percent readout leaves no room beside the ring at that width.
  assert.match(iconRail, /\.context-usage-btn \.context-usage-label\s*\{[^}]*position:\s*absolute[^}]*clip-path:\s*inset\(50%\)/);
});

test("mobile sidebar closes safely during desktop startup and cache updates propagate", async () => {
  const [html, app, appMain, uiShell] = await Promise.all([
    readFile(indexURL, "utf8"),
    readFile(appURL, "utf8"),
    readAppMainSource(),
    readFile(uiShellURL, "utf8"),
  ]);
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

test("navigation collapses through columns, docked and icons, and stays desktop-only", () => {
  assert.equal(globalRailExpandedWidth, 68);
  assert.equal(globalRailCollapsedWidth, 48);
  assert.equal(normalizeCollapsedPreference("true"), true);
  assert.equal(normalizeCollapsedPreference("false"), false);
  assert.equal(normalizeCollapsedPreference("unexpected", true), true);
  assert.deepEqual([...navigationLayoutModes], ["columns", "docked", "icons"]);
  assert.equal(nextNavigationLayoutMode("columns"), "docked");
  assert.equal(nextNavigationLayoutMode("docked"), "icons");
  assert.equal(nextNavigationLayoutMode("icons"), "columns");
  assert.equal(normalizeNavigationLayoutMode("DOCKED"), "docked");
  assert.equal(normalizeNavigationLayoutMode("nonsense"), "columns");
  assert.equal(normalizeNavigationLayoutMode(null, "icons"), "icons");

  // Dragging reaches every layout, so the button is a shortcut rather than the
  // only route. Shrinking leaves a layout later than expanding re-enters it; that
  // gap is what stops the rail reflowing back and forth under a resting pointer.
  const drag = navigationLayoutModeFromDragWidth;
  assert.equal(drag(500, "columns"), "columns");
  assert.equal(drag(navigationDragColumnsExitWidth, "columns"), "columns");
  assert.equal(drag(navigationDragColumnsExitWidth - 1, "columns"), "docked");
  assert.equal(drag(navigationDragIconsEnterWidth + 1, "columns"), "docked");
  assert.equal(drag(navigationDragIconsEnterWidth, "columns"), "icons");
  assert.equal(drag(navigationDragIconsEnterWidth, "docked"), "icons");
  assert.equal(drag(navigationDragIconsEnterWidth + 1, "docked"), "docked");
  assert.equal(drag(navigationDragColumnsEnterWidth - 1, "docked"), "docked");
  assert.equal(drag(navigationDragColumnsEnterWidth, "docked"), "columns");
  // Leaving the icon rail needs a wider clearance than entering it did.
  assert.equal(drag(navigationDragIconsExitWidth, "icons"), "icons");
  assert.equal(drag(navigationDragIconsExitWidth + 1, "icons"), "docked");
  assert.equal(drag(navigationDragColumnsEnterWidth, "icons"), "columns");
  assert.ok(navigationDragIconsExitWidth > navigationDragIconsEnterWidth, "the icon band is stickier than its entry point");
  assert.ok(navigationDragColumnsEnterWidth > navigationDragColumnsExitWidth, "the column band is stickier than its entry point");
  // The narrow column rests its divider at rail + narrow width, which sat below
  // the old 300px exit, so grabbing it flipped the layout to docked on the
  // first pixel of travel. The exit must leave deliberate travel below the
  // narrowest resting point.
  const narrowRestingTotal = globalRailExpandedWidth + narrowSidebarMinWidth;
  assert.equal(drag(narrowRestingTotal, "columns"), "columns");
  assert.equal(drag(narrowRestingTotal - 1, "columns"), "columns");
  assert.ok(
    navigationDragColumnsExitWidth <= narrowRestingTotal - 20,
    "a narrow column can be grabbed and moved without instantly docking",
  );
  // A garbage measurement must not move the layout.
  assert.equal(drag(Number.NaN, "docked"), "docked");

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
  // No stored mode: this is the migration case. The legacy collapsed rail is what
  // the icon layout replaced, so it has to land on "icons".
  const storage = new MemoryStorage([
    [sidebarWidthPreferenceKey, "342"],
    [globalRailCollapsedPreferenceKey, "true"],
    [sessionSidebarCollapsedPreferenceKey, "true"],
  ]);
  const shell = makeNode();
  const globalRail = makeNode();
  const globalCollapseButton = makeNode();
  const conversationRailButton = makeNode();
  const conversationRailRow = makeNode();
  conversationRailRow.appendChild = (node) => { node.parent = conversationRailRow; return node; };
  const sidebarHeaderParent = {
    insertBefore(node) { node.parent = sidebarHeaderParent; return node; },
  };
  const sidebarActions = makeNode();
  sidebarActions.parent = sidebarHeaderParent;
  sidebarActions.parentNode = sidebarHeaderParent;
  sidebarActions.nextSibling = null;
  // Docking is a DOM move, so the sidebar needs a real parent to leave and a dock
  // to arrive in. These stubs record the move so the test can assert on it.
  const sidebarShellParent = {
    insertBefore(node) { node.parent = sidebarShellParent; return node; },
  };
  const sidebar = makeNode();
  sidebar.parent = sidebarShellParent;
  sidebar.parentNode = sidebarShellParent;
  sidebar.nextSibling = null;
  const conversationDock = makeNode();
  conversationDock.appendChild = (node) => { node.parent = conversationDock; return node; };
  const separator = makeNode({ attributes: { tabindex: "0" } });
  separator.getBoundingClientRect = () => ({ left: 398, width: 6 });
  separator.setPointerCapture = () => {};
  separator.releasePointerCapture = () => {};
  // The rail meets the viewport edge; a docked sidebar sits inside the rail's
  // padding. The drag has to measure whichever element the stored width sizes.
  globalRail.getBoundingClientRect = () => ({ left: 0, width: 296 });
  sidebar.getBoundingClientRect = () => ({ left: 68, width: 296 });
  const projects = { classList: makeClassList(), clientHeight: 200, clientWidth: 240, scrollHeight: 200, scrollWidth: 240, scrollLeft: 0, scrollTop: 0 };
  const messages = { classList: makeClassList(), clientHeight: 200, clientWidth: 240, scrollHeight: 200, scrollWidth: 240, scrollLeft: 0, scrollTop: 0 };
  const bodyClasses = makeClassList();
  const nodes = {
    appShell: shell,
    globalRailCollapseBtn: globalCollapseButton,
    messages,
    projects,
    railConversationDock: conversationDock,
    sessionSidebarActions: sidebarActions,
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
      if (selector === '[data-global-rail-target="conversation"]') return conversationRailButton;
      if (selector === "[data-rail-conversation-row]") return conversationRailRow;
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
    const click = { preventDefault() {}, stopPropagation() {} };
    const controller = createUIShellController({ state: {}, resizeTerminal() {} });
    const cleanup = controller.bindSidebarResizer({ storage });
    // Migrated to icons: the rail is collapsed, the list is out of the layout, and
    // compact is cleared because it only describes the standalone column.
    assert.equal(shell.classList.contains("nav-mode-icons"), true);
    assert.equal(shell.classList.contains("global-rail-collapsed"), true);
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    assert.equal(globalCollapseButton.getAttribute("aria-expanded"), "false");
    assert.equal(separator.getAttribute("tabindex"), "0");
    assert.equal(storage.getItem(navigationLayoutModePreferenceKey), "icons");
    assert.equal(storage.getItem(globalRailCollapsedPreferenceKey), "true");
    assert.equal(storage.getItem(sessionSidebarCollapsedPreferenceKey), "false");
    assert.equal(sidebar.parent, sidebarShellParent);

    // icons -> columns: back to the two-column shell at the stored width.
    globalCollapseButton.dispatch("click", click);
    assert.equal(shell.classList.contains("nav-mode-columns"), true);
    assert.equal(shell.classList.contains("nav-mode-icons"), false);
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(globalCollapseButton.getAttribute("aria-expanded"), "true");
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px");
    assert.equal(storage.getItem(navigationLayoutModePreferenceKey), "columns");
    assert.equal(sidebar.parent, sidebarShellParent);

    // The column's own compact stage is retired: the stale stored "true" above
    // was rewritten to "false" on first paint, and the column keeps its full
    // stored width -- the rail's three-way cycle is the only collapse control.
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false);
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px");

    // columns -> docked: the list moves inside the rail and the width carries over.
    globalCollapseButton.dispatch("click", click);
    assert.equal(shell.classList.contains("nav-mode-docked"), true);
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(globalCollapseButton.getAttribute("aria-expanded"), "true");
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px");
    assert.equal(storage.getItem(navigationLayoutModePreferenceKey), "docked");
    assert.equal(sidebar.parent, conversationDock, "docked mode parents the sidebar to the rail dock");
    assert.equal(conversationDock.getAttribute("hidden"), null);
    // Search and create ride along onto the conversation row: the sidebar's own
    // header is hidden here, so leaving them behind would lose both controls.
    assert.equal(sidebarActions.parent, conversationRailRow, "docked mode moves search and create onto the conversation row");

    // Dragging still resizes the list when docked, and it tracks the pointer
    // exactly: measuring the nested sidebar instead of the rail would subtract
    // the rail's padding and leave the edge trailing the cursor.
    // Pressing alone must not resize or switch layout: the divider does not always
    // rest at the stored boundary, so a press that applied its own position would
    // change the layout the moment it was touched.
    separator.dispatch("pointerdown", { button: 0, clientX: 360, pointerId: 1, preventDefault() {} });
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "342px", "a press with no movement leaves the width alone");
    assert.equal(shell.classList.contains("nav-mode-docked"), true, "a press with no movement leaves the layout alone");
    windowListeners.get("pointermove")?.({ clientX: 360, preventDefault() {} });
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), "360px");
    assert.equal(shell.classList.contains("session-sidebar-collapsed"), false, "docked mode has no compact form to fall into");

    // Dragging is the second route through the cycle. Widening past the column
    // threshold hands the list its own column again, mid-drag.
    windowListeners.get("pointermove")?.({ clientX: navigationDragColumnsEnterWidth, preventDefault() {} });
    assert.equal(shell.classList.contains("nav-mode-columns"), true, "dragging wide enough restores the column layout");
    assert.equal(sidebar.parent, sidebarShellParent);
    assert.equal(sidebarActions.parent, sidebarHeaderParent);
    // The divider is what the user holds, so the width behind it is the total
    // minus the icon rail the column layout still shows.
    assert.equal(shell.styleValues.get("style:--session-sidebar-width"), `${navigationDragColumnsEnterWidth - globalRailExpandedWidth}px`);

    // Shrinking back past the exit threshold docks it again without letting go.
    windowListeners.get("pointermove")?.({ clientX: navigationDragColumnsExitWidth - 1, preventDefault() {} });
    assert.equal(shell.classList.contains("nav-mode-docked"), true, "dragging narrow enough docks the list");
    assert.equal(sidebar.parent, conversationDock);

    // All the way in: the icon rail, reached by drag rather than by the button.
    windowListeners.get("pointermove")?.({ clientX: navigationDragIconsEnterWidth, preventDefault() {} });
    assert.equal(shell.classList.contains("nav-mode-icons"), true, "dragging to the far left reaches the icon rail");
    assert.equal(sidebar.parent, sidebarShellParent);
    // And back out again, which is why the divider stays put in the icon layout.
    windowListeners.get("pointermove")?.({ clientX: navigationDragIconsExitWidth + 1, preventDefault() {} });
    assert.equal(shell.classList.contains("nav-mode-docked"), true, "dragging out of the icon rail is possible");

    windowListeners.get("pointermove")?.({ clientX: 360, preventDefault() {} });
    windowListeners.get("pointerup")?.({ pointerId: 1 });
    // One stored width across layouts, so whichever column holds the list next
    // inherits the size just dragged.
    assert.equal(storage.getItem(sidebarWidthPreferenceKey), "360");
    assert.equal(storage.getItem(navigationLayoutModePreferenceKey), "docked");

    // docked -> icons: the sidebar returns to the shell so the hidden rail never
    // owns it, which is what keeps the phone drawer reachable.
    globalCollapseButton.dispatch("click", click);
    assert.equal(shell.classList.contains("nav-mode-icons"), true);
    assert.equal(sidebar.parent, sidebarShellParent, "leaving docked mode returns the sidebar to the shell");
    assert.equal(sidebarActions.parent, sidebarHeaderParent, "the action cluster goes back to the sidebar header");
    assert.equal(conversationDock.getAttribute("hidden"), "");

    // The icon rail hides the list, so its nav entry has to restore a layout that
    // shows one -- the last one actually in use, which was docked.
    conversationRailButton.dispatch("click", click);
    assert.equal(shell.classList.contains("nav-mode-docked"), true);
    assert.equal(sidebar.parent, conversationDock);
    // Already visible: pressing it again must not cycle the layout away.
    conversationRailButton.dispatch("click", click);
    assert.equal(shell.classList.contains("nav-mode-docked"), true);

    mobile = true;
    windowListeners.get("resize")?.();
    assert.equal(shell.classList.contains("nav-mode-docked"), false);
    assert.equal(shell.classList.contains("nav-mode-icons"), false);
    assert.equal(shell.classList.contains("global-rail-collapsed"), false);
    assert.equal(sidebar.parent, sidebarShellParent, "a phone viewport always undocks");
    globalCollapseButton.dispatch("click", click);
    assert.equal(storage.getItem(navigationLayoutModePreferenceKey), "docked");
    assert.equal(storage.getItem(globalRailCollapsedPreferenceKey), "false");

    // Back on desktop the stored layout resumes.
    mobile = false;
    windowListeners.get("resize")?.();
    assert.equal(shell.classList.contains("nav-mode-docked"), true);
    assert.equal(sidebar.parent, conversationDock);
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
    readChatRenderingSource(),
    readAppMainSource(),
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
    readAppMainSource(),
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
    readAppMainSource(),
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
  assert.match(settingsStyles, /\.settings-nav-icon \.settings-icon-svg\s*\{[^}]*width:\s*18px;[^}]*height:\s*18px;/);
  assert.match(settingsStyles, /\[aria-current="page"\] \.settings-nav-icon\s*\{[^}]*background:\s*transparent;[^}]*color:\s*var\(--settings-primary\);[^}]*box-shadow:\s*none;/);

  const mobile = settingsStyles.slice(settingsStyles.indexOf("@media (max-width: 767px)"));
  assert.match(mobile, /\.settings-sidebar\s*\{[\s\S]*?display:\s*grid;/);
  assert.doesNotMatch(mobile, /\.settings-sidebar\s*\{[^}]*display:\s*none;/);
  assert.match(mobile, /\.settings-mobile-category-nav,[\s\S]*?\.settings-nav-groups\s*\{[\s\S]*?display:\s*flex;/);
  assert.match(mobile, /\.settings-nav-group\s*\{[\s\S]*?display:\s*contents;/);
  assert.match(mobile, /\.settings-help-panel\s*\{\s*width:\s*100%;\s*border-left:\s*0;/);
});

test("mobile shell skips home and keeps the drawer, settings index, and model sheet wired", async () => {
  const [html, styles, app, appMain, uiShell, settingsPreferences, messagesCN, messagesTW, messagesEN, selectMenus] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appURL, "utf8"),
    readAppMainSource(),
    readFile(uiShellURL, "utf8"),
    readFile(new URL("./settings-preferences.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-zh-CN.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-zh-TW.mjs", import.meta.url), "utf8"),
    readFile(new URL("./messages-en.mjs", import.meta.url), "utf8"),
    readFile(selectMenusURL, "utf8"),
  ]);

  for (const id of [
    "mobilePageTitle",
    "overviewDashboard",
    "mobileChooseDirectoryBtn",
    "mobileConversationWelcome",
    "mobileSidebarBackdrop",
    "mobileSidebarCloseBtn",
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

  const marker = "/* Mobile shell refresh: conversation, drawer, settings, and composer selection sheets. */";
  const refreshedStyles = styles.slice(styles.indexOf(marker));
  assert.ok(refreshedStyles.startsWith(marker));
  // .mobile-sidebar-metrics joins this list: the drawer's CPU / memory cell is
  // mobile-only for the same reason as the rest of the drawer furniture, since the
  // desktop already shows those numbers on the home dashboard.
  assert.match(styles, /\.mobile-topbar,\s*\.mobile-backdrop,\s*\.mobile-drawer-header,\s*\.mobile-drawer-primary-actions,\s*\.mobile-drawer-schedule-btn,\s*\.mobile-sidebar-account-summary,\s*\.mobile-sidebar-quick-actions,\s*\.mobile-sidebar-metrics,\s*\.mobile-drawer-footer,\s*\.mobile-conversation-welcome,\s*\.composer-status\s*\{\s*display:\s*none;/);
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

  assert.match(appMain, /mobileSettingsView:\s*"detail"/);
  assert.match(appMain, /function showMobileSettingsIndex[\s\S]*?settings\.mobile\.indexTitle/);
  assert.match(appMain, /showMobileSettingsIndex[\s\S]*?querySelector\?\.\("\.settings-mobile-index-row"\)\?\.focus/);
  assert.match(appMain, /function requestCloseSettingsModal[\s\S]*?mobileSettingsView === "detail"/);
  assert.match(appMain, /function syncMobilePageTitle[\s\S]*?\(!state\.project && !state\.agent\) \? t\("shell\.nav\.conversation"\)/);
  assert.doesNotMatch(appMain, /\(!state\.project && !state\.agent\) \? t\("shell\.home"\)/);
  assert.match(selectMenus, /mobile-select-sheet-backdrop hidden/);
  assert.match(uiShell, /mobileSidebarBackdrop/);
  assert.match(uiShell, /mobileSidebarCloseBtn/);
  assert.equal((uiShell.match(/function closeMobileSidebar/g) || []).length, 1);
  assert.match(selectMenus, /translate\("chat\.selectModel"\)/);
  // 管理模型 / 思考强度 / 压缩上下文 were removed from the model menu: each already has
  // its own control beside the composer.
  // The model menu instead offers the summary model, which is applied by
  // round-tripping the whole agent payload so the default model and subagent
  // assignments are not dropped.
  assert.match(selectMenus, /translate\("chat\.summaryModel"\)/);
  assert.match(selectMenus, /openSummaryModelPicker/);
  assert.match(appMain, /agentModelSettingsPayload\(\{ \.\.\.\(state\.settings\?\.agent \|\| \{\}\), summaryModel: model \}\)/);
  assert.match(appMain, /api\("\/api\/runtime\/agent-model-settings", \{ method: "PATCH"/);
  assert.doesNotMatch(uiShell, /translate\("chat\.manageModels"\)/);
  assert.doesNotMatch(selectMenus, /translate\("chat\.manageModels"\)/);
  assert.doesNotMatch(uiShell, /translate\("chat\.compactContext"\)/);
  assert.doesNotMatch(selectMenus, /translate\("chat\.compactContext"\)/);
  assert.doesNotMatch(uiShell, /openModelSettings/);
  assert.doesNotMatch(selectMenus, /openModelSettings/);
  assert.doesNotMatch(uiShell, /mobileComposerSelectStyles/);
  assert.doesNotMatch(selectMenus, /mobileComposerSelectStyles/);
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
    readAppMainSource(),
    readChatRenderingSource(),
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
});

test("static shell controls localize without marking runtime-owned content", async () => {
  const html = await readFile(indexURL, "utf8");
  const tag = (id) => html.match(new RegExp(`<[^>]*id="${id}"[^>]*>`))?.[0] || "";

  for (const [id, marker] of [
    ["workspaceExplorerBtn", 'data-i18n-aria-label="chat.openWorkspace"'],
    ["gitWorkflowBtn", 'data-i18n-aria-label="chat.gitChanges"'],
    ["specBoardBtn", 'data-i18n-aria-label="chat.taskList"'],
    ["runtimeStatusBtn", 'data-i18n-aria-label="chat.conversationDetails"'],
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
  assert.equal(defaultAppearancePrefs.themePreset, "light");
  assert.equal(defaultAppearancePrefs.theme, "light");
  assert.equal(defaultAppearancePrefs.styleVersion, appearanceStyleVersion);
  assert.equal(appearanceStyleVersion, 5);
  assert.deepEqual(appearanceThemePresets, ["light", "dark", "cyber", "cream"]);
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
      themeSchemeRefs: { light: { kind: "preset", id: "light" } },
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
      showThroughput: false,
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
      showThroughput: false,
    });
    assert.deepEqual(controller.createLocalPreferencesBackup().preferences[appearancePrefsKey], {
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
      showThroughput: false,
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
    /\.app-shell\.terminal-collapsed\.details-open,[\s\S]*?\.app-shell\.terminal-collapsed\.background-tasks-open,[\s\S]*?\.app-shell\.terminal-collapsed\.preview-open[\s\S]*?\{[\s\S]*?grid-template-columns:[\s\S]*?clamp\(260px, calc\(50vw - 186px\), 1200px\)/,
  );
});

test("finishing a run no longer auto-opens the run summary review card", async () => {
  const appMain = await readAppMainSource();
  const handlerStart = appMain.indexOf("async function handleAgentStreamEvent");
  assert.ok(handlerStart >= 0);
  const handlerEnd = appMain.indexOf("return { applyAgentLiveSnapshot, handleAgentStreamEvent }", handlerStart);
  assert.ok(handlerEnd > handlerStart);
  const handler = appMain.slice(handlerStart, handlerEnd);
  // A finished run does load its summary now, but only to take ownership of the
  // tool activity the live view was holding -- never to open the review card.
  // The card's own emptiness rule is covered behaviourally in
  // chat-rendering.test.mjs ("ordinary completed conversations without tools
  // render no Run review"), so what matters here is the handoff shape: every
  // load in this handler is paired with clearing the live copy.
  for (const call of handler.matchAll(/loadRunSummary\(runId[\s\S]{0,600}?\n  \}/g)) {
    assert.match(call[0], /clearLiveToolOutputs\(\{ agentId \}\)/);
  }
  assert.doesNotMatch(handler, /openRunReview|showRunSummaryPanel/);
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

  const [uiShell, styles, selectMenus] = await Promise.all([
    readFile(uiShellURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(selectMenusURL, "utf8"),
  ]);
  assert.match(selectMenus, /groupModelSelectOptions\(options\)\.forEach/);
  assert.match(selectMenus, /appendModelOptionGroups\(binding, options, \{ mobile: true \}\)/);
  assert.match(selectMenus, /appendModelOptionGroups\(binding, menu\)/);
  assert.doesNotMatch(uiShell, /composer-model-option-provider/);
  assert.doesNotMatch(selectMenus, /composer-model-option-provider/);
  // The summary model list is provider-grouped too: the same model name can come
  // from several providers, so a flat list would be ambiguous.
  assert.match(selectMenus, /groupModelSelectOptions\(summaryModelOptions\(binding\)\)\.forEach/);
  assert.match(
    selectMenus,
    /const openSummaryModelPicker[\s\S]*?groupHeading\.textContent = group\.provider \|\| translate\("chat\.modelProviderFallback"\)/,
  );
  // The collapsed row names the provider next to the current summary model.
  assert.match(selectMenus, /const summaryModelDescriptor = \(binding, value\)/);
  assert.match(
    selectMenus,
    /descriptor\?\.provider\s*\n\s*\? `\$\{descriptor\.provider\} · \$\{descriptor\.name\}`\s*\n\s*: \(descriptor\?\.name \|\| current\)/,
  );
  assert.match(styles, /\.composer-model-group-heading\.composer-model-group-start\s*\{[\s\S]*?border-top:\s*1px solid/);
  assert.match(styles, /\.mobile-model-group-heading\.composer-model-group-start/);
});
