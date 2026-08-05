import { createAccountPreferencesController } from "./account-preferences.mjs?v=profile-avatar-1-first-run-setup-1";
import { createAgentStreamController } from "./agent-stream.mjs";
import { createAutomationControlController } from "./automation-control.mjs?v=nav-schedules-1";
import { createAutomationToolCatalogController } from "./automation-tool-catalog.mjs?v=automation-tool-catalog-1";
import { createArchiveSettingsController } from "./archive-settings.mjs?v=archive-settings-1";
import { createConversationTitleHelpers } from "./conversation-title-helpers.mjs?v=standalone-removed-1";
import { createBackgroundTasksController } from "./background-tasks.mjs?v=subagent-cards-1-foreground-activity-1";
import { createExecutionNotifications } from "./execution-notifications.mjs";
import { createBackendRegistryController } from "./backend-registry.mjs?v=agent-admin-removed-1";
import { createChatComposerController, normalizeChatDrafts, normalizePromptHistory } from "./chat-composer.mjs?v=plan-mode-1-project-context-1-model-save-gate-1-goal-command-2-queue-command-1-reasoning-steps-1-reasoning-history-1-markdown-2";
import { createChatRenderingController, findToolActivityByIdentity, renderAgentTaskActivityCardHTML } from "./chat-rendering.mjs?v=protected-images-1-message-thread-1-plan-mode-2-user-message-left-1-switch-fix-3-hide-run-loading-1-i18n-shared-1-conversation-boundary-1-subagent-cards-1-message-lifecycle-1-subagent-incremental-1-profile-message-identity-1-profile-avatar-1-provider-errors-1-compact-run-error-1-first-token-task-status-1-tool-activity-lazy-1-tool-protocol-filter-1-live-assistant-last-1-tool-activity-svg-icons-1-reasoning-steps-1-reasoning-history-1-markdown-2-tool-inline-detail-1-md-table-1-tool-position-1-project-run-history-1-dup-activity-fix-1-reasoning-count-1-avatar-logo-fix-1-markdown-stream-1";
import { releaseProtectedImageURLs } from "./protected-images.mjs?v=protected-images-1";
import { createContextManagementController } from "./context-management.mjs?v=context-ring-3-scoped-memory-1";
import {
  addRecentConversation,
  buildNavigationView,
  createNavigationRefreshController,
  createRecentConversationSyncController,
  normalizeNavigationPayload,
  normalizeRecentConversations,
  parseNavigationTargetId,
  renderNavigationHTML,
  renderRecentConversationsHTML,
  resolveInitialNavigationTarget,
  resolveTopNavigationProjectId,
} from "./conversation-navigation.mjs?v=mode-boundaries-2-project-flat-1-task-workspace-1-navigation-state-1-project-context-1-recent-sync-1-dual-rail-collapse-1-compact-navigation-1-theme-icons-1-workline-fork-1-conversation-order-1-nav-status-color-1-top-project-1-standalone-removed-1";
import {
  basename,
  canonicalLocalPath,
  createDirectoryBrowserController,
  normalizePath,
  normalizeRecentDirectories,
  shortPath,
} from "./directory-browser.mjs?v=folder-picker-remote-2-root-card-1-root-shortcut-removed-1";
import { $, coalescePerFrame, escapeAttr, escapeHtml, setButtonBusy, setHTMLIfChanged, setTextIfChanged } from "./dom.mjs";
import { navigationCreateLabelKey, navigationCreateTarget } from "./navigation-create.mjs?v=standalone-removed-1";
import { createSubagentCardCoordinator } from "./subagent-cards.mjs?v=tool-activity-lazy-1";
import { createNavigationStartupGuard } from "./navigation-startup-guard.mjs?v=startup-navigation-guard-4";
import { formatNumber, formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-autoto-themes-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1-i18n-shared-1-overview-home-1-settings-cleanup-1-context-ring-3-global-background-1-theme-v2-1-background-upload-1-goal-command-2-queue-command-1-reasoning-steps-1-reasoning-history-1-markdown-2-first-run-setup-1-home-launcher-1-scoped-memory-1-standalone-removed-2";
import { appMainT as am } from "./messages-app-main-extra.mjs?v=workbench-title-edit-1-hidden-toggle-removed-1-settings-cleanup-1";
import { shellExtraT as sx } from "./messages-shell-extra.mjs";
import { createGitWorkflowController } from "./git-workflow.mjs?v=merge-review-1";
import { createLocalPreferencesSettingsController } from "./local-preferences-settings.mjs?v=settings-flat-1-apple-theme-1-autoto-themes-1-profile-avatar-1-global-background-1-background-upload-1";
import { createMCPRegistryUIController } from "./mcp-registry-ui.mjs";
import { createPluginRegistryUIController } from "./plugin-registry-ui.mjs";
import { createMemorySettingsController } from "./memory-settings.mjs";
import { agentModelSettingsPayload } from "./model-routing-settings.mjs";
import { createModelProviderSettingsController } from "./model-provider-settings.mjs?v=native-codex-3-provider-console-3-account-wide-1-model-compact-1-codex-export-1-settings-flat-1-aggregates-1-codex-import-open-1-provider-create-page-2-codex-browser-login-1-provider-secrets-1-model-picker-1-provider-full-page-2-provider-placeholders-1-usage-cost-1-codex-usage-clean-1-model-sections-hidden-1-model-configs-1-provider-reference-1-default-openai-responses-1-provider-draft-session-1-native-image-generation-1-provider-auto-name-1-provider-hidden-models-1-gemini-model-quota-1-safety-model-1-provider-quota-overview-1-codex-quota-exhausted-1";
import {
  createOverviewDashboardController,
  overviewNavigationRoute,
  overviewRailTarget,
  resolveOverviewStartup,
} from "./overview-dashboard.mjs?v=overview-home-3-nav-schedules-1-mobile-no-home-1-schedule-workspace-1-activity-heatmap-1-home-launcher-1-home-launcher-bottom-1-home-launcher-minimal-1-standalone-removed-2";
import { createPageLifecycleController } from "./page-lifecycle.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { createProjectKanbanController } from "./project-kanban.mjs?v=workbench-3-mode-boundaries-1";
import { createScheduleWorkspaceController } from "./schedule-workspace.mjs?v=schedule-workspace-2-browser-boundary-1";
import { createTaskWorkspaceController } from "./task-workspace.mjs?v=task-workspace-1";
import { createAppearanceBackgroundManager, createThemeManager, setThemePageContext } from "./theme-manager.mjs?v=autoto-themes-2-background-2-theme-v2-1-background-upload-1";
import { createThemeSettingsController } from "./theme-settings.mjs?v=autoto-themes-2-theme-v2-1-formal-assets-1";
import { readLocalPreference, recentConversationsKey } from "./preferences-data.mjs?v=autoto-themes-1-schedule-workspace-1-global-background-1";
import { applyRemoteAccessFailClosed, fullAccessAllowed, remoteAccessContext, terminalAccessAllowed } from "./remote-access-capabilities.mjs";
import { createRemoteAccessSettingsController } from "./remote-access-settings.mjs?v=remote-control-full-4-remote-full-toggle-3-tunnel-busy-1";
import { createSharedAPISettingsController } from "./shared-api-settings.mjs?v=shared-api-2-compact-layout-1-no-alias-safety-1-gateway-tunnel-1";
import { applyServerSkillsLoadResult, createSkillsPhaseBController, hydrateServerSkillSummaries, isOptimisticSkillConflict, loadServerSkillsWithFallback, normalizeSkillContext } from "./skills-bootstrap.mjs";
import { api, onAPIAuthorizationFailure, webSocketURL } from "./runtime.mjs";
import { firstSettingsItemForCategory, groupSettingsItemsByLegacyCategory, legacySettingsCategories, settingsCategoryByKey, settingsCategoryForItem } from "./settings-categories.mjs?v=users-panel-removed-1-shared-api-1-agent-admin-removed-1-archive-1-settings-cleanup-1";
import { settingsIconSVG, settingsItemByKey, settingsItems, settingsSections } from "./settings-data.mjs?v=users-panel-removed-1-shared-api-1-agent-admin-removed-1-archive-1-settings-icons-1-settings-cleanup-1";
import { createSettingsHelpController } from "./settings-help.mjs?v=settings-help-1";
import { createSettingsPanelRegistry } from "./settings-panel-registry.mjs";
import { createSecurityModeHelpers } from "./security-mode-helpers.mjs";
import { createSettingsNavigationHelpers } from "./settings-navigation-helpers.mjs";
import { createSettingsPreferencesController } from "./settings-preferences.mjs?v=apple-theme-1-autoto-themes-1-profile-avatar-1-dual-rail-collapse-1-global-background-1-settings-identity-removed-1";
import { createSettingsShellHelpers } from "./settings-shell-helpers.mjs";
import { createSkillsContext } from "./skills-context.mjs";
import { createServerResourceLoaders } from "./server-resource-loaders.mjs";
import { createSetupWizardController } from "./setup-wizard.mjs?v=first-run-readiness-1";
import { createSpecBoardController } from "./spec-board.mjs";
import { createSystemSettingsController } from "./system-settings.mjs?v=users-panel-removed-1-about-brand-license-1-desktop-shell-1-execution-budget-2-background-task-settings-1";
import { installDesktopDeepLinkRouter, isDesktopShell } from "./desktop-shell-ui.mjs";
import { createSkillsWorkbenchController } from "./skills-workbench.mjs?v=users-panel-removed-1-config-center-1-automation-tool-catalog-1-optional-tools-compact-1-skills-density-1";
import { createTerminalController } from "./terminal.mjs?v=terminal-actions-compact-2";
import { createUIShellController, elementVisible, isComposingInput } from "./ui-shell.mjs?v=permission-panel-2-plan-mode-panel-1-mobile-toolbar-right-3-icon-rail-1-mobile-viewport-1-sidebar-wheel-1-settings-cleanup-1-context-ring-3-dual-rail-collapse-1-compact-navigation-1-global-rail-2-model-menu-scroll-1-utility-resize-2-sheet-trim-1-model-provider-groups-1-danger-reflection-desc-1-danger-reflection-levels-1-model-icon-only-1-theme-icon-1";
import { createUsageHistoryController } from "./usage-history.mjs";
import { createAgentWorkspaceHelpers } from "./agent-workspace-helpers.mjs?v=task-summary-activity-1";
import { createNavigationContextMenu } from "./navigation-context-menu.mjs";
import { createOverviewNavHelpers } from "./overview-nav-helpers.mjs";
import { installPullToRefresh, isPullToRefreshSupported } from "./pull-to-refresh.mjs?v=pull-to-refresh-1";
import { createWorkbenchSidebarRender, primaryWorkbenchLayout } from "./workbench-sidebar-render.mjs?v=standalone-removed-1";
import { createWorkspaceContextHelpers } from "./workspace-context-helpers.mjs";
import { createWorkspaceExplorerController } from "./workspace-explorer.mjs?v=viewport-menu-1";
import { runPreviewScreenshot } from "./workspace-screenshot.mjs";
import { normalizeWorkStateSnapshot, renderWorkStateHTML } from "./work-state.mjs";

const CONV_ORDER_KEY_PREFIX = "autoto:conv_order:";
const SETTINGS_NAV_ORDER_PREFIX = "autoto:settings_nav_order:";
const PROJ_ORDER_KEY = "autoto:project_order";
const NAV_COLLAPSED_KEY = "autoto:nav_collapsed";

// Only collapsed nodes are stored, so a project or conversation the reader has
// never touched starts open and newly arriving ones are not hidden by a stale
// record. Entries are "scope:id".
function getCollapsedNavNodes() {
  try {
    const raw = localStorage.getItem(NAV_COLLAPSED_KEY);
    const parsed = raw ? JSON.parse(raw) : null;
    if (Array.isArray(parsed)) return new Set(parsed.map((entry) => String(entry || "")).filter(Boolean));
  } catch {}
  return new Set();
}

function saveCollapsedNavNodes(nodes) {
  try {
    localStorage.setItem(NAV_COLLAPSED_KEY, JSON.stringify([...nodes]));
  } catch {}
}

function toggleCollapsedNavNode(key) {
  const id = String(key || "").trim();
  if (!id) return;
  const nodes = getCollapsedNavNodes();
  if (nodes.has(id)) nodes.delete(id);
  else nodes.add(id);
  saveCollapsedNavNodes(nodes);
}

function getProjectOrder() {
  try {
    const val = localStorage.getItem(PROJ_ORDER_KEY);
    if (val) return JSON.parse(val);
  } catch {}
  return null;
}

function saveProjectOrder(projectIds) {
  try {
    localStorage.setItem(PROJ_ORDER_KEY, JSON.stringify(projectIds));
  } catch {}
}

function getConversationOrders() {
  const orders = {};
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (!key?.startsWith(CONV_ORDER_KEY_PREFIX)) continue;
      const projectId = key.slice(CONV_ORDER_KEY_PREFIX.length);
      const val = localStorage.getItem(key);
      if (val) orders[projectId] = JSON.parse(val);
    }
  } catch {}
  return orders;
}

function saveConversationOrder(projectId, agentIds) {
  try {
    localStorage.setItem(CONV_ORDER_KEY_PREFIX + projectId, JSON.stringify(agentIds));
  } catch {}
}

function getSettingsNavOrder(sectionId) {
  try {
    const val = localStorage.getItem(SETTINGS_NAV_ORDER_PREFIX + sectionId);
    if (val) return JSON.parse(val);
  } catch {}
  return null;
}

function saveSettingsNavOrder(sectionId, keys) {
  try {
    localStorage.setItem(SETTINGS_NAV_ORDER_PREFIX + sectionId, JSON.stringify(keys));
  } catch {}
}

function applySettingsNavOrder(items, order) {
  if (!Array.isArray(order) || !order.length) return items;
  const orderMap = new Map(order.map((k, i) => [String(k), i]));
  const copy = [...items];
  copy.sort((a, b) => {
    const ia = orderMap.has(a.key) ? orderMap.get(a.key) : Infinity;
    const ib = orderMap.has(b.key) ? orderMap.get(b.key) : Infinity;
    return ia - ib;
  });
  return copy;
}

let backendRegistry = null;
let settingsPreferences = null;
let resumeSetupWizardAfterSettings = null;

function closeBackendsModal() {
  backendRegistry.closeBackendsModal();
}

function currentSkillsPreferences() {
  return settingsPreferences.currentSkillsPreferences();
}

function notifyTerminal(message) {
  settingsPreferences.notifyTerminal(message);
}

function updateSidebarAccountSummary() {
  settingsPreferences.updateSidebarAccountSummary();
}

let skillsPhaseB = null;
let messageViewportBusyTimer = null;
const messageViewportBusyDelayMs = 140;

const state = {
  projects: [],
  navigationConversations: [],
  navigationLoadSeq: 0,
  navigationMode: "projects",
  navigationMenuTarget: null,
  navigationSelectionKind: "project",
  navigationTransitionTitle: "",
  startupWorkbenchIntent: "",
  sessionSidebarLayout: "expanded",
  recentConversations: [],
  project: null,
  workline: null,
  agent: null,
  agentContext: {},
  workState: null,
  healthSeq: 0,
  healthOK: null,
  healthLabel: "checking",
  agentStreamStatus: "idle",
  settings: null,
  settingsLoadSeq: 0,
  modelCatalog: null,
  modelCatalogSeq: 0,
  providerAuthFiles: null,
  providerAuthError: "",
  providerAuthLoading: false,
  providerAuthMutationWarning: "",
  providerAuthSeq: 0,
  codexAccountBusy: {},
  providerConfigStatus: "",
  providerConfigExpanded: {},
  providerConsole: {},
  storageSummary: null,
  storageError: "",
  storageSeq: 0,
  licenseSummary: null,
  licenseError: "",
  licenseSeq: 0,
  updateStatus: null,
  updateError: "",
  updateSeq: 0,
  usageHistory: null,
  runtimeSummary: null,
  remoteAccess: null,
  remoteAccessError: "",
  remoteAccessLoading: false,
  gatewayKeys: [],
  gatewayModels: [],
  gatewayUsage: { items: [], summary: {} },
  gatewayDataLoaded: false,
  gatewayDataLoading: false,
  gatewayAPIError: "",
  runtimeError: "",
  runtimeSeq: 0,
  authUser: undefined,
  profile: null,
  searchPrefs: null,
  skillsPrefs: null,
  serverSkills: [],
  serverSkillsStatus: "idle",
  serverSkillsHadServerData: false,
  serverSkillsLoadSeq: 0,
  serverSkillsSaving: false,
  serverSkillsError: "",
  activeSkillTab: "commands",
  skillContextScope: "global",
  skillsV2: { contexts: {}, effective: {} },
  workflowPreferences: null,
  workflowError: "",
  workflowLoading: false,
  workflowSaving: false,
  toolPermissionRules: [],
  toolPermissionRulesLoading: false,
  toolPermissionRulesSaving: false,
  toolPermissionRulesError: "",
  notifications: null,
  serverNotificationSettings: null,
  serverNotificationError: "",
  serverNotificationLoading: false,
  serverNotificationSaving: false,
  serverNotificationTesting: false,
  appearance: null,
  primaryModePreference: "conversation",
  activeWorkbench: "conversation",
  overviewActive: true,
  terminalPrefs: null,
  chatDrafts: null,
  pendingAttachments: [],
  // Messages the user parked with /queue while a run was in flight. Drained one
  // at a time by the composer once the agent goes idle.
  messageQueue: [],
  promptHistory: null,
  promptHistoryIndex: -1,
  promptHistoryDraft: "",
  slashCommandOpen: false,
  slashCommandIndex: 0,
  slashCommandQuery: "",
  terminalStatus: "idle",
  agentSaving: false,
  agentSavePending: false,
  agentSavePromise: null,
  agentSaveSnapshot: null,
  titleEditing: false,
  titleSaving: false,
  titleDraft: "",
  titleEditSurface: "conversation",
  reasoningEffortSaving: false,
  reasoningEffortPending: undefined,
  messageModes: {},
  messageSendingByAgent: {},
  messageRefreshTimersByAgent: {},
  activePlan: null,
  pendingPlanApproval: null,
  planActionBusy: {},
  chatHydrating: false,
  currentMessages: [],
  messageCopyTexts: [],
  messageHasMoreBefore: false,
  messageNextBefore: "",
  messageOlderLoading: false,
  activeRunSummary: null,
  activeRunSummaryRunId: "",
  activeRunToolCalls: [],
  activeRunToolCallsRunId: "",
  runSummaryLoading: false,
  runSummaryError: "",
  runRollbackBusy: false,
  runSummarySeq: 0,
  liveToolOutputs: {},
  // Model reasoning for the turn in flight: closed steps, plus the one still
  // streaming. Cleared with the rest of the live turn state on a switch.
  liveReasoningSteps: [],
  liveReasoningDraft: null,
  liveAssistantActive: false,
  liveAssistantText: "",
  liveAssistantRequestId: "",
  liveAssistantRunId: "",
  liveAssistantProvider: "",
  liveAssistantModel: "",
  liveAssistantStartedAt: "",
  liveAssistantPerformance: null,
  liveImageGenerations: {},
  pendingToolApprovals: {},
  pendingUserQuestions: {},
  gitStatus: null,
  gitDiff: null,
  gitLog: null,
  gitError: "",
  gitSeq: 0,
  gitScope: "all",
  gitSelectedPath: "",
  gitCommitMessage: "",
  gitCommitSelected: {},
  gitCommitBusy: false,
  gitOpen: false,
  modelRefreshing: false,
  modelApplying: false,
  modelApplySeq: 0,
  agentModelSettings: null,
  projectCreating: false,
  worklineForking: false,
  projectCreateSeq: 0,
  projectSelectSeq: 0,
  initializing: false,
  initRestartRequested: false,
  initSeq: 0,
  settingsWarmupStarted: false,
  settingsShellOpen: false,
  settingsMobileViewport: false,
  mobileSettingsView: "detail",
  activeSettingsPanel: "providers",
  activeSettingsCategory: "api",
  settingsSearchQuery: "",
  backendDeleteConfirmId: "",
  backends: [],
  backendHealth: null,
  backendLoadSeq: 0,
  backendHealthSeq: 0,
  backendActionBusy: {},
  automationToolCatalogItems: [],
  automationToolCatalogLoading: false,
  automationToolCatalogLoaded: false,
  automationToolCatalogError: "",
  automationToolCatalogSeq: 0,
  automationToolCatalogBusy: {},
  automationToolCatalogDiscovery: {},
  mcpRegistryServers: [],
  mcpRegistryTools: {},
  mcpRegistryError: "",
  mcpRegistrySeq: 0,
  mcpRegistryLoaded: false,
  mcpRegistryLoading: false,
  mcpRegistryActionBusy: {},
  mcpRegistryEditingId: "",
  projectWorklines: [],
  worklineAgents: [],
  directoryPath: "",
  directoryParent: "",
  directoryShortcuts: [],
  directoryBrowseSeq: 0,
  nativeDirectorySelecting: false,
  projectQuery: "",
  ws: null,
  terminalWS: null,
};

const navigationStartupGuard = createNavigationStartupGuard();

function startupTokenCurrent(token) {
  return navigationStartupGuard.isCurrent(token) && token?.initSeq === state.initSeq;
}

function startupSelectionCurrent(options) {
  return options?.source !== "startup" || startupTokenCurrent(options.startupToken);
}

const settingsShellHelpers = createSettingsShellHelpers({
  state,
  isMobileAppViewport,
  selectSettingsPanel,
  renderSettingsNav,
  renderMobileSettingsIndex,
  syncSettingsCloseControl,
  // These are bound later from chatComposer/uiShell; wrap to avoid TDZ at module eval.
  saveCurrentChatDraft: (...args) => saveCurrentChatDraft(...args),
  hideSlashCommandPalette: (...args) => hideSlashCommandPalette(...args),
  closeMobileSidebar: (...args) => closeMobileSidebar(...args),
  applyPrimaryWorkbench,
});

const {
  captureInlineProperties,
  restoreInlineProperties,
  setSettingsShellNodeHidden,
  restoreSettingsShellNode,
  isMobileSettingsViewport,
  settingsModalOpen,
  resolvedMobileSettingsSections,
  applyMobileSettingsViewClasses,
  syncSettingsViewportState,
  layoutSettingsShell,
  enterSettingsShell,
  exitSettingsShell,
} = settingsShellHelpers;

const workspaceContextHelpers = createWorkspaceContextHelpers({
  state,
  renderEmptyWorkspaceCard,
  syncThemePageContext: () => syncThemePageContext(),
  getBackgroundTasks: () => backgroundTasks,
  connectWS: (...args) => connectWS(...args),
  getConnectTerminal: () => connectTerminal,
});

const {
  projectOperationContextActive,
  setGlobalRailActive,
  terminalSocketUsable,
  restoreAuthorizedAgentTransports,
  showEmptyWorkspaceState,
} = workspaceContextHelpers;

async function focusTaskCreation() {
  if (state.activeWorkbench !== "workbench") return false;
  closeMobileSidebar();
  if (taskWorkspace.getState().scope !== "agent") {
    if (taskWorkspace.focusCreate()) return true;
    await taskWorkspace.load();
    if (taskWorkspace.focusCreate()) return true;
    showToast(t("taskWorkspace.noAgents"), "info", { force: true });
    return false;
  }
  if (!state.agent?.id) {
    showToast(t("workbench.selectAgentToCreate"), "info", { force: true });
    return false;
  }
  if (projectKanban.focusCreate()) return true;
  await specBoard.load();
  if (projectKanban.focusCreate()) return true;
  showToast(t("projectKanban.unavailable"), "info", { force: true });
  return false;
}

const securityModeHelpers = createSecurityModeHelpers({
  state,
  showToast,
  connectionMobileLabel,
  updatePermissionModeDisplay,
  projectOperationContextActive,
  updateWorkspaceMetaPills: () => updateWorkspaceMetaPills(),
  closeSidebarSettingsMenu: () => closeSidebarSettingsMenu(),
  enforceTerminalAccessPolicy: () => enforceTerminalAccessPolicy(),
  renderTerminalButtonState: () => renderTerminalButtonState(),
  updateRuntimeStatusButton: () => updateRuntimeStatusButton(),
});

const {
  currentSecuritySummary,
  remoteSecurityHardeningActive,
  connectionModeSummary,
  bypassDisabledBySecurity,
  effectivePermissionForDisplay,
  enforcePermissionSelectCap,
  logoutRemoteAccess,
  updateSecurityModeUI,
} = securityModeHelpers;

const conversationTitleHelpers = createConversationTitleHelpers({
  state,
  selectedModelValue: () => selectedModelValue(),
  currentModelValue: () => currentModelValue(),
  projectOperationContextActive,
  effectivePermissionForDisplay,
  connectionModeSummary,
  permissionLabel,
  renderConversationHeaderIdentity,
  renderWorkbenchHeaderIdentity,
  renderRecentSidebarConversations,
  saveConversationTitle,
  showError,
});

const {
  currentWorkspaceModel,
  conversationHeaderTitle,
  titleEditorElements,
  titleForSurface,
  renderAllTitleEditors,
  normalizedTitleEditSurface,
  cancelConversationTitleEdit,
  updateTitleDraft,
  handleTitleEditorKeydown,
  updateWorkspaceMetaPills,
  loadRecentConversations,
  rememberCurrentConversation,
} = conversationTitleHelpers;

const workbenchSidebarRender = createWorkbenchSidebarRender({
  state,
  getTaskWorkspace: () => taskWorkspace,
  getScheduleWorkspace: () => scheduleWorkspace,
  getOverviewDashboard: () => overviewDashboard,
  getSpecBoard: () => specBoard,
  currentSecuritySummary,
  syncNavigationCreateButton,
  renderWorkbenchHeaderIdentity,
  syncMobilePageTitle,
  renderProjects,
  openOverviewConversation,
  showError,
  init: () => init(),
});

const {
  setTranslatedText,
  setTranslatedAttribute,
  normalizedPrimaryWorkbench,
  currentShellRailTarget,
  renderPrimaryModeSidebar,
  renderWorkbenchShell,
  scheduleWorkspaceViewOptions,
  renderScheduleSurface,
  refreshPrimaryMode,
} = workbenchSidebarRender;

const overviewNavHelpers = createOverviewNavHelpers({
  state,
  getOverviewDashboard: () => overviewDashboard,
});

const {
  overviewEntity,
  deferOverviewDOM,
  focusOverviewDataElement,
  overviewApprovalAgentIds,
} = overviewNavHelpers;

const agentWorkspaceHelpers = createAgentWorkspaceHelpers({
  state,
  getAgentStream: () => agentStream,
  getBackgroundTasks: () => backgroundTasks,
  getTaskWorkspace: () => taskWorkspace,
  getSpecBoard: () => specBoard,
  getProjectKanban: () => projectKanban,
  closeWorkspace: () => closeWorkspace(),
  toggleTerminal: (collapsed) => toggleTerminal(collapsed),
  notifyTerminal,
  projectOperationContextActive,
  isMobileAppViewport,
  closeConversationDetails,
  renderConversationDetails,
  updateRuntimeStatusButton: () => updateRuntimeStatusButton(),
  renderWorkbenchShell,
  loadProjects: (...args) => loadProjects(...args),
  selectNavigationConversation: (...args) => selectNavigationConversation(...args),
});

const {
  setComposerConnectionStatus,
  refreshComposerActivityStatus,
  connectWS,
  updateAgentStreamStatus,
  attachmentKind,
  attachmentIcon,
  toggleTerminalDock,
  openTaskWorkspaceAgent,
} = agentWorkspaceHelpers;

const navigationContextMenu = createNavigationContextMenu({
  state,
  showToast,
  setTranslatedText,
  loadProjects: (...args) => loadProjects(...args),
});

const {
  closeNavigationContextMenu,
  navigationMenuRecord,
  positionNavigationContextMenu,
  openNavigationContextMenu,
  handleNavigationContextMenu,
  bindNavigationMenuTriggers,
  applyNavigationMenuAction,
  bindNavigationActivation,
} = navigationContextMenu;

const settingsNavigationHelpers = createSettingsNavigationHelpers({
  state,
  showToast,
  notifyTerminal,
  isMobileSettingsViewport,
  renderMobileSettingsIndex,
  renderSettingsNav,
  selectSettingsPanel,
});

const {
  normalizedSettingsSearchQuery,
  settingsSearchText,
  filteredSettingsSections,
  nextFilteredSettingsKey,
  firstFilteredSettingsItem,
  filteredSettingsIncludesKey,
  syncSettingsSearchInput,
  bindSettingsArrowNavigation,
  renderSettingsCategoryNav,
  selectSettingsCategory,
  clearSettingsSearchQuery,
  focusSettingsSearchInput,
  refreshActiveSettingsPanel,
  copyToClipboard,
  copyText,
  syncThemePageContext,
} = settingsNavigationHelpers;

const skillsContext = createSkillsContext({
  state,
  showToast,
  notifyTerminal,
  getSkillsPhaseB: () => skillsPhaseB,
  getEffectiveSkillContext,
});

const {
  getSkillContext,
  setSkillContext,
  refreshEffectiveSkillsPolicy,
  invalidateAndRefreshEffectiveSkillsPolicy,
} = skillsContext;

const serverResourceLoaders = createServerResourceLoaders({
  state,
  showToast,
  showError,
  notifyTerminal,
  refreshActiveSettingsPanel,
  updateSecurityModeUI,
  invalidateAndRefreshEffectiveSkillsPolicy,
  renderModelOptions: () => renderModelOptions(),
  refreshReasoningEffortControl: () => refreshReasoningEffortControl(),
  refreshFastModeControl: () => refreshFastModeControl(),
  updateSlashCommandPalette: () => updateSlashCommandPalette(),
  updatePromptHistoryHint: () => updatePromptHistoryHint(),
  loadProviderAuthFiles: (options) => loadProviderAuthFiles(options),
  loadRemoteAccess: () => remoteAccessSettings.load(),
});

const {
  updateRuntimeStatusButton,
  setHealth,
  loadHealth,
  loadServerNotificationSettings,
  saveServerNotificationSettings,
  sortServerSkills,
  refreshServerSkillsUI,
  loadServerSkills,
  loadServerSkillDetail,
  createServerSkill,
  updateServerSkill,
  deleteServerSkill,
  previewServerSkillImport,
  importServerSkill,
  loadWorkflowPreferences,
  saveWorkflowPreferences,
  loadToolPermissionRules,
  createToolPermissionRule,
  updateToolPermissionRule,
  deleteToolPermissionRule,
  toolPermissionRuleSort,
  loadWorkflowPolicy,
  loadModelCatalog,
  loadStorageSummary,
  loadLicenseSummary,
  loadUpdateStatus,
  loadRuntimeSummary,
  warmSettingsData,
} = serverResourceLoaders;

const terminal = createTerminalController({
  state,
  copyToClipboard,
  formatNumber,
  notifyTerminal,
  refreshActiveSettingsPanel,
  showError,
  showToast,
});

const gitWorkflow = createGitWorkflowController({
  state,
  showError,
  showToast,
});

const workspaceExplorer = createWorkspaceExplorerController({
  state,
  request: api,
  showError,
  showToast,
  onPreviewOpen: () => {
    backgroundTasks?.closeTray?.("preview-open");
    closeConversationDetails();
    toggleTerminal(true);
    $("appShell")?.classList.add("preview-open");
  },
  onPreviewClose: () => {
    $("appShell")?.classList.remove("preview-open");
  },
  // chatComposer is declared further below; this callback is only ever
  // invoked from a later click event, well after module evaluation
  // finishes, so referencing it here (instead of at call-construction
  // time) avoids the TDZ the same way the helpers above defer to
  // saveCurrentChatDraft/hideSlashCommandPalette/closeMobileSidebar.
  onScreenshot: () => {
    runPreviewScreenshot({
      iframeEl: document.querySelector("#workspacePreviewFrameHost .workspace-preview-iframe"),
      // chat-composer.mjs's importAttachmentFiles reads event.target.files
      // (it's wired to a <input type="file"> change event), so we hand it a
      // minimal fake event carrying our screenshot File instead of an array.
      onFile: (file) => chatComposer.importAttachmentFiles({ target: { files: [file] } }).catch(showError),
      t,
      onError: () => showToast?.(t("workspace.explorer.screenshotFailed"), "warn", { force: true }),
    });
  },
});

function getSelectedModelValue() {
  return selectedModelValue();
}

let backgroundTasks = null;

const chatRendering = createChatRenderingController({
  state,
  attachmentIcon,
  attachmentKind,
  copyToClipboard,
  notifyTerminal,
  openGitModal: () => gitWorkflow.openGitModal?.(),
  refreshGitWorkflow: (options) => gitWorkflow.refreshGitWorkflow?.(options),
  resolveBackgroundTask: (tool) => backgroundTasks?.getTaskByParentTool?.(tool?.runId, tool?.toolUseId) || null,
  selectedModelValue: getSelectedModelValue,
  shortPath,
  showError,
  showToast,
});

const {
  appendTerminal,
  bindTerminalSettingsActions,
  clearTerminalOutput,
  connectTerminal,
  copyTerminalOutput,
  currentTerminalPreferences,
  enforceAccessPolicy: enforceTerminalAccessPolicy,
  focusTerminalPanel,
  handleTerminalKeydown,
  loadTerminalPreferences,
  normalizeTerminalPreferences,
  reconnectTerminalFromSettings,
  renderTerminalButtonState,
  renderTerminalSettingsContent,
  resizeTerminal,
  saveTerminalPreferences,
  sendTerminalInput,
  setTerminalPreference,
  setTerminalStatus,
  terminalOutputStats,
  terminalOutputText,
  toggleTerminal,
  trimTerminalOutput,
} = terminal;

onAPIAuthorizationFailure(({ status, path, error }) => {
  if (!remoteAccessContext(state)) return;
  remoteAccessSettings.invalidatePendingLoads({ status });
  applyRemoteAccessFailClosed(state, { status });
  state.remoteAccessError = error?.message || String(error || "remote authorization failed");
  disconnectAgentTransports();
  updateSecurityModeUI();
  if (state.activeSettingsPanel === "remote-access") refreshActiveSettingsPanel();
  if (state.remoteAccessAuthRefreshPending) return;
  // The controller handling its own failed authority request will commit the
  // fail-closed state in catch/finally. Other authorization failures start a
  // fresh authority read even if an older settings request is still in flight.
  if (state.remoteAccessLoading && path === "/api/security/remote-access") return;
  state.remoteAccessAuthRefreshPending = true;
  Promise.resolve()
    .then(() => remoteAccessSettings.load())
    .catch(() => {})
    .finally(() => {
      state.remoteAccessAuthRefreshPending = false;
    });
});

const {
  closeGitModal,
  loadGitStatus,
  openGitModal,
  resetGitWorkflowState,
} = gitWorkflow;

const {
  bind: bindWorkspaceExplorer,
  closeWorkspace,
  openWorkspace,
  setAgent: setWorkspaceExplorerAgent,
} = workspaceExplorer;

const {
  appendLiveAssistantText,
  appendToolOutput,
  applyMessageSnapshot,
  applyPlanEvent,
  beginLiveAssistantGeneration,
  clearCurrentAgentApprovals,
  clearLiveImageGenerations,
  clearLiveToolOutputs,
  clearPlanState,
  clearLiveAssistantText,
  clearMessageRefreshTimer,
  clearRunSummary,
  clearToolApproval,
  clearUserQuestion,
  copyCurrentConversationMarkdown,
  finishToolOutput,
  invalidateMessageLifecycle,
  loadLatestRunSummary,
  loadMessages,
  loadOlderMessages,
  loadRunSummary,
  rememberImageGenerationStatus,
  rememberToolApproval,
  appendLiveReasoning,
  clearLiveReasoning,
  closeLiveReasoningStep,
  rememberAssistantToolOwner,
  rememberToolStarted,
  rememberUserQuestion,
  refreshUserMessageIdentity,
  replacePendingApprovals,
  replacePendingUserQuestions,
  replacePlanState,
  scheduleMessageRefresh,
  scrollMessagesToBottom,
  updateConversationCopyButton,
  updateLiveAssistantPerformance,
} = chatRendering;

function executionNoticeMessage(notice) {
  const raw = notice?.raw || {};
  const data = raw.data && typeof raw.data === "object" ? raw.data : {};
  if (notice?.family === "task_terminal") return t("backgroundTasks.notifications.taskCompleted", { task: raw.title || data.title || notice.taskId || t("backgroundTasks.task") });
  if (notice?.family === "continuation_blocked") return t("backgroundTasks.continuation.blocked", { reason: notice.reason || data.reason || "—" });
  if (notice?.family === "budget_exhausted") return t("backgroundTasks.continuation.budgetExhausted", { reason: notice.reason || data.reason || notice.budget || "—" });
  if (notice?.family === "approval_required") return t("backgroundTasks.notifications.approvalRequired");
  if (notice?.family === "completed") return t("backgroundTasks.notifications.completed");
  if (notice?.family === "error") return t("backgroundTasks.notifications.error");
  if (notice?.family === "interrupted") return t("backgroundTasks.notifications.interrupted");
  return t("backgroundTasks.notifications.truncated");
}

const executionNotifications = createExecutionNotifications({
  notifier: (notice) => {
    const taskStatus = String(notice?.raw?.status || notice?.raw?.data?.status || "").toLowerCase();
    const taskFailed = notice.family === "task_terminal" && ["failed", "error", "interrupted", "cancelled", "canceled"].includes(taskStatus);
    const variant = taskFailed || ["error", "budget_exhausted"].includes(notice.family) ? "error" : ["approval_required", "continuation_blocked", "interrupted", "truncated"].includes(notice.family) ? "warn" : "success";
    showToast(executionNoticeMessage(notice), variant);
  },
  onError: (error) => notifyTerminal(`[warn] ${error?.message || error}\n`),
});

const subagentCards = createSubagentCardCoordinator({
  state,
  getBackgroundTasks: () => backgroundTasks,
  applyMessageSnapshot,
  loadProjects,
  selectNavigationConversation,
  loadRunSummary,
  showError,
});

backgroundTasks = createBackgroundTasksController({
  request: api,
  onChange: (change) => {
    subagentCards.scheduleRefresh(change);
    if ($("appShell")?.classList.contains("details-open")) renderConversationDetails();
  },
  onError: (error) => notifyTerminal(`[warn] ${error?.message || error}\n`),
  onOpenChange: (open) => {
    const shell = $("appShell");
    shell?.classList.toggle("background-tasks-open", open);
    if (!open) return;
    closeConversationDetails();
    closeWorkspace();
    toggleTerminal(true);
  },
  onNavigateAgent: (childAgentId) => {
    subagentCards.navigateToAgent(childAgentId).catch(showError);
  },
  onNavigateRun: (childAgentId, childRunId) => {
    subagentCards.navigateToRun(childAgentId, childRunId).catch(showError);
  },
});
backgroundTasks.bind();
backgroundTasks.subscribe?.(subagentCards.scheduleRefresh);
subagentCards.bindCardActions();

const agentStream = createAgentStreamController({
  api,
  webSocketURL,
  onEvent: handleAgentStreamEvent,
  onSnapshot: applyAgentLiveSnapshot,
  onStatus: updateAgentStreamStatus,
  onError: (error) => notifyTerminal(`[warn] ${am("agentStreamRestoreFailed", { message: error?.message || error })}\n`),
  getExecutionCheckpoint: (agentId) => executionNotifications.checkpoint(agentId),
});

const navigationRefresh = createNavigationRefreshController({
  refresh: () => loadProjects(),
  shouldRefresh: () => globalThis.navigator?.onLine !== false && globalThis.document?.visibilityState !== "hidden",
});

const recentConversationSync = createRecentConversationSyncController({
  key: recentConversationsKey,
  onChange: (recent) => {
    state.recentConversations = recent;
    renderRecentSidebarConversations();
  },
});

const pageLifecycle = createPageLifecycleController({
  onResume: (detail) => {
    navigationRefresh.request(detail?.reason || "lifecycle_resume");
    return agentStream.resume(detail);
  },
  onOffline: (detail) => agentStream.pause(detail?.reason || "browser_offline"),
  onError: (error) => notifyTerminal(`[warn] ${am("agentStreamRestoreFailed", { message: error?.message || error })}\n`),
});

const directoryBrowser = createDirectoryBrowserController({
  state,
  createProjectFromDirectory,
  elementVisible,
  notifyTerminal,
  showError,
  showToast,
});

const {
  browseDirectories,
  browseHomeDirectory,
  browseParentDirectory,
  closeDirectoryModal,
  createFolderInCurrentDirectory,
  favoriteCurrentDirectory,
  hideNewFolderInline,
  openDirectoryChooser,
  refreshDirectory,
  rememberDirectory,
  renderRecentModalDirectories,
  renderRecentSidebarDirectories,
  selectNativeDirectory,
  setDirectoryStatus,
  showNewFolderInline,
  updateDirectoryPathDisplay,
} = directoryBrowser;

backendRegistry = createBackendRegistryController({
  state,
  showError,
  showToast,
  updateSidebarAccountSummary,
});

const {
  loadBackends,
  openBackendsModal,
  renderBackendPanel,
  resetBackendForm,
  saveBackend,
} = backendRegistry;

const contextManagement = createContextManagementController({
  request: api,
  getAgent: () => state.agent,
  onStatusChange: (contextStatus) => {
    state.agentContext = contextStatus;
    if (state.agent?.id && (!contextStatus.agentId || contextStatus.agentId === state.agent.id)) {
      state.agent = { ...state.agent, prunedPercent: contextStatus.prunedPercent, pruneEnabled: contextStatus.autoPrune };
    }
  },
  onAgentChange: (agent) => {
    if (!agent?.id || state.agent?.id !== agent.id) return;
    state.agent = agent;
    state.worklineAgents = (state.worklineAgents || []).map((item) => item.id === agent.id ? agent : item);
  },
  showToast,
  showError,
  canManage: () => fullAccessAllowed(state),
  translate: t,
});
contextManagement.bind();

const messageModeBridge = {
  get: () => "execute",
  set: () => "execute",
};

const uiShell = createUIShellController({
  state,
  clearSettingsSearchQuery,
  closeBackendsModal,
  closeDirectoryModal,
  closeSettingsModal: requestCloseSettingsModal,
  focusSettingsSearchInput,
  normalizedSettingsSearchQuery,
  openDirectoryChooser,
  getMessageMode: () => messageModeBridge.get(),
  setMessageMode: (mode) => messageModeBridge.set(mode),
  getSummaryModel: () => String(state.settings?.agent?.summaryModel || state.settings?.agent?.defaultModel || ""),
  // The summary model is global runtime configuration, so the whole agent model
  // payload is round-tripped: sending summaryModel alone would drop the default
  // model and every subagent assignment.
  setSummaryModel: async (model) => {
    const payload = agentModelSettingsPayload({ ...(state.settings?.agent || {}), summaryModel: model });
    const response = await api("/api/runtime/agent-model-settings", { method: "PATCH", body: JSON.stringify(payload) });
    const savedAgent = response?.agent || payload;
    state.settings = { ...(state.settings || {}), agent: { ...(state.settings?.agent || {}), ...savedAgent } };
    return String(savedAgent.summaryModel || model);
  },
  renderProjects,
  onLayoutChange: ({ sessionSidebarMode = "expanded" } = {}) => {
    const changed = state.sessionSidebarLayout !== sessionSidebarMode;
    state.sessionSidebarLayout = sessionSidebarMode;
    if (!changed) return;
    const render = () => renderProjects();
    if (typeof globalThis.requestAnimationFrame === "function") globalThis.requestAnimationFrame(render);
    else globalThis.setTimeout?.(render, 0);
  },
  resizeTerminal,
  showError,
  translate: t,
});

const {
  beginSettingsDialogFocus,
  bindComposerSelectMenus,
  bindSidebarResizer,
  bindUtilityPanelResizer,
  closeMobileSidebar,
  closeProjectSearch,
  closeSidebarSettingsMenu,
  focusMobileSearch,
  handleDirectoryShortcutClick,
  handleGlobalEscape,
  handleSettingsDialogKeydown,
  handleSettingsSearchShortcut,
  handleSidebarSettingsMenuDocumentClick,
  openMobileSidebar,
  restoreSettingsDialogFocus,
  toggleProjectSearch,
  toggleSidebarSettingsMenu,
} = uiShell;

bindSidebarResizer();
bindUtilityPanelResizer();
bindComposerSelectMenus();
closeMobileSidebar({ restoreFocus: false });

const accountPreferences = createAccountPreferencesController({
  request: api,
  onChange: ({ snapshot }) => {
    state.profile = snapshot.profile;
    settingsPreferences?.applyProfilePreferences?.();
    refreshUserMessageIdentity();
    renderModelOptions?.();
  },
});

const themeManager = createThemeManager({
  api,
  showToast,
  translate: t,
});
const appearanceBackgroundManager = createAppearanceBackgroundManager({ api, showToast, translate: t });
themeManager.subscribe(() => {
  if (state.activeSettingsPanel === "appearance") refreshActiveSettingsPanel();
});

const modelProviderSettings = createModelProviderSettingsController({
  state,
  copyText,
  getModelVisibilityPreference: accountPreferences.getModelVisibility,
  getPreferredModelPreference: accountPreferences.getPreferredModel,
  loadModelCatalog,
  loadSettings,
  notifyTerminal,
  openSettingsModal,
  refreshActiveSettingsPanel,
  setModelVisibilityPreference: accountPreferences.setModelVisibility,
  setPreferredModelPreference: accountPreferences.setPreferredModel,
  showError,
  updateWorkspaceMetaPills,
});

const {
  bindModelSettingsActions,
  bindProviderSettingsActions,
  codexProviderSummary,
  currentModelValue,
  currentProviderConfig,
  discardProviderConsoleDraft,
  getPreferredModel,
  isCurrentModelConfigured,
  loadProviderAuthFiles,
  modelSetupMessage,
  providerLabel,
  providerStatusText,
  refreshModelCatalog,
  renderAgentModelOptions,
  renderModelOptions,
  renderModelSettingsContent,
  renderProviderSettingsContent,
  resetProviderConsoleToProviderList,
  selectedModelValue,
  setPreferredModel,
} = modelProviderSettings;

const setupWizard = createSetupWizardController({
  state,
  loadModelCatalog,
  loadSettings,
  loadSetupStatus: ({ force = false } = {}) => api(force ? "/api/setup/status?refresh=1" : "/api/setup/status"),
  openSettingsModal,
  renderModelOptions,
  getPreferredModel,
  getSetupVersion: accountPreferences.getSetupVersion,
  completeSetup: async (model, version) => {
    await accountPreferences.setPreferences({ preferredModel: model, setupVersion: version });
  },
  preferencesPending: accountPreferences.hasPendingPatch,
  copyText: copyToClipboard,
  showToast,
});
const { bindSetupWizardActions, maybeOpenSetupWizard, openSetupWizard, resumeAfterProviderSettings } = setupWizard;
resumeSetupWizardAfterSettings = resumeAfterProviderSettings;

const specBoard = createSpecBoardController({ request: api, showError, showToast });
specBoard.bind();

const projectKanban = createProjectKanbanController({
  specBoard,
  host: "#projectKanbanBody",
  translate: projectKanbanTranslation,
  showError,
  showToast,
});
projectKanban.bind();

const chatComposer = createChatComposerController({
  state,
  attachmentKind,
  currentProviderConfig,
  currentSkillsPreferences,
  getEffectiveSkillsPolicy: () => skillsPhaseB?.getEffectivePolicy(state.agent?.id, getEffectiveSkillContext()) || {
    items: [], status: "idle", error: "", hasAuthoritativeData: false,
  },
  isComposingInput,
  isCurrentModelConfigured,
  awaitAgentSettingsSaved: (agentId) => waitForAgentSettingsSave(agentId),
  saveAgentSettings: () => saveAgentSettings(),
  loadMessages,
  notifyTerminal,
  openDirectoryChooser,
  scheduleMessageRefresh,
  scrollMessagesToBottom,
  showModelSetupNotice,
  showToast,
  onMessageAccepted: async (result, agentId) => {
    // The POST is acknowledged before the runner necessarily publishes its
    // first WebSocket event. Mark the run as active now so the activity affordance
    // gets a real paint instead of depending on a later agent.started/model.started
    // event that may arrive in the same frame as the response.
    if (state.agent?.id === agentId) {
      state.agent = { ...state.agent, status: "running" };
      syncMessageComposerBusy();
      refreshComposerActivityStatus();
    }
    return specBoard.handleGoalConfirmation(result, agentId);
  },
});

const {
  autoResizeMessageInput,
  handleAttachmentDragLeave,
  handleAttachmentDragOver,
  handleAttachmentDrop,
  handleMessageInput,
  handleMessageKeydown,
  handleMessagePaste,
  hideSlashCommandPalette,
  importAttachmentFiles,
  loadChatDrafts,
  loadPromptHistory,
  openAttachmentPicker,
  refreshFastModeControl,
  refreshMessageModeControl,
  refreshReasoningEffortControl,
  loadMessageQueue,
  renderMessageQueue,
  restoreCurrentChatDraft,
  saveCurrentChatDraft,
  saveReasoningEffort,
  scheduleMessageInputResize,
  selectedReasoningEffort,
  setMessageMode,
  sendMessage,
  setMessageInputValue,
  syncMessageComposerBusy,
  toggleFastMode,
  updateDraftLimitHint,
  updatePromptHistoryHint,
  updateSlashCommandPalette,
} = chatComposer;

messageModeBridge.get = () => {
  const agentId = state.agent?.id;
  const saved = agentId ? state.messageModes?.[agentId] : "";
  if (saved === "plan" || saved === "execute") return saved;
  return state.agent?.planMode === true ? "plan" : "execute";
};
messageModeBridge.set = (mode) => setMessageMode(mode);

settingsPreferences = createSettingsPreferencesController({
  state,
  accountPreferences,
  appendTerminal,
  applyPrimaryMode: applyPrimaryWorkbench,
  applyThemePreference: (prefs) => themeManager.applyPreference(prefs),
  applyBackgroundPreference: (prefs) => appearanceBackgroundManager.saveOptions({
    mode: prefs.backgroundMode,
    url: prefs.backgroundUrl,
    dim: prefs.backgroundDim,
    positionX: prefs.backgroundPositionX,
    positionY: prefs.backgroundPositionY,
  }),
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
  renderRecentSidebarDirectories,
  showToast,
  toggleTerminal,
  trimTerminalOutput,
  updatePromptHistoryHint,
  updateSlashCommandPalette,
  updateGlobalThemeToggle,
});

const mcpRegistryUI = createMCPRegistryUIController({
  state,
  copyText,
  currentSkillsPreferences,
  refreshActiveSettingsPanel,
  showError,
  showToast,
});

const {
  bindMCPRegistryActions,
  isMCPRegistryActionBusy,
  loadMCPRegistryServers,
  renderMCPRegistryList,
} = mcpRegistryUI;

const pluginRegistryUI = createPluginRegistryUIController({
  state,
  refreshActiveSettingsPanel,
  showError,
  showToast,
});

const {
  bindPluginRegistryActions,
  renderPluginRegistryPanel,
} = pluginRegistryUI;

const {
  applyAppearancePreferences,
  applyProfilePreferences,
  currentAppearancePreferences,
  currentNotificationPreferences,
  currentPrimaryModePreference,
  currentProfilePreferences,
  currentRegionalPreferences,
  currentSearchPreferences,
  loadAppearancePreferences,
  loadNotificationPreferences,
  loadPrimaryModePreference,
  loadProfilePreferences,
  loadSearchPreferences,
  loadSkillsPreferences,
  localPreferencesBackupSummary,
  localPreferencesBackupText,
  localSkillID,
  normalizeMCPServer,
  normalizeSkillCommand,
  notificationToastDuration,
  notificationVariantEnabled,
  profileDisplayName,
  profileGitEnvExample,
  resetNotificationPreferences,
  resetProfilePreferences,
  resetSearchPreferences,
  resetSkillsPreferences,
  restoreLocalPreferencesBackup,
  saveAppearancePreferences,
  saveNotificationPreferences,
  saveProfilePreferences,
  saveRegionalPreferences,
  saveSearchPreferences,
  saveSkillsPreferences,
  searchPrefsExport,
  searchProviderLabel,
  setAppearancePreference,
  setNotificationPreference,
  setPrimaryModePreference,
  toggleAppearanceColorScheme,
  shouldLogAgentEvents,
  skillsPrefsExport,
} = settingsPreferences;

themeManager.setPreferenceAdapter({
  currentAppearancePreferences,
  saveAppearancePreferences,
});
appearanceBackgroundManager.setPreferenceAdapter({
  currentAppearancePreferences,
  saveAppearancePreferences,
});

const themeSettings = createThemeSettingsController({
  themeManager,
  currentAppearancePreferences,
  setAppearancePreference,
  refreshActiveSettingsPanel,
  showError,
  showToast,
});
const { bindThemeLibraryActions, renderThemeLibrarySection } = themeSettings;

const localPreferencesSettings = createLocalPreferencesSettingsController({
  state,
  copyText,
  currentAppearancePreferences,
  backgroundManager: appearanceBackgroundManager,
  currentNotificationPreferences,
  currentProfilePreferences,
  currentRegionalPreferences,
  currentSearchPreferences,
  notifyTerminal,
  profileDisplayName,
  profileGitEnvExample,
  resetNotificationPreferences,
  resetProfilePreferences,
  resetSearchPreferences,
  loadServerNotificationSettings,
  saveServerNotificationSettings,
  testServerNotification,
  saveProfilePreferences,
  saveRegionalPreferences,
  saveSearchPreferences,
  saveAppearancePreferences,
  searchPrefsExport,
  searchProviderLabel,
  renderThemeLibrarySection,
  bindThemeLibraryActions,
  setAppearancePreference,
  setNotificationPreference,
  showError,
  showToast,
});

const {
  bindAppearanceSettingsActions,
  bindNetworkSearchSettingsActions,
  bindNotificationSettingsActions,
  bindProfileSettingsActions,
  renderAppearanceSettingsContent,
  renderNetworkSearchSettingsContent,
  renderNotificationSettingsContent,
  renderProfileSettingsContent,
} = localPreferencesSettings;

const systemSettings = createSystemSettingsController({
  state,
  copyText,
  loadLicenseSummary,
  loadRuntimeSummary,
  loadStorageSummary,
  loadUpdateStatus,
  localPreferencesBackupSummary,
  localPreferencesBackupText,
  notifyTerminal,
  refreshActiveSettingsPanel,
  restoreLocalPreferencesBackup,
  showError,
  showToast,
});

const {
  bindAboutSettingsActions,
  bindRuntimeSettingsActions,
  bindStorageSettingsActions,
  renderAboutSettingsContent,
  renderRuntimeSettingsContent,
  renderServerSystemSettingsContent,
  renderStorageSettingsContent,
  renderUsageMetricCard,
} = systemSettings;

function getEffectiveSkillContext() {
  if (state.project?.id && state.workline?.id) {
    return normalizeSkillContext({
      scope: "workspace",
      projectId: state.project.id,
      worklineId: state.workline.id,
    });
  }
  if (state.project?.id) return normalizeSkillContext({ scope: "project", projectId: state.project.id });
  return normalizeSkillContext({ scope: "global" });
}

let skillsPhaseBRenderQueued = false;
skillsPhaseB = createSkillsPhaseBController({
  state,
  api,
  getContext: getSkillContext,
  onEffectiveInvalidated: () => refreshEffectiveSkillsPolicy(),
  onChange: () => {
    updateSlashCommandPalette();
    updatePromptHistoryHint();
    if (skillsPhaseBRenderQueued || state.activeSettingsPanel !== "skills") return;
    skillsPhaseBRenderQueued = true;
    queueMicrotask(() => {
      skillsPhaseBRenderQueued = false;
      if (state.activeSettingsPanel === "skills") refreshActiveSettingsPanel();
    });
  },
});

const automationToolCatalog = createAutomationToolCatalogController({
  state,
  request: api,
  confirmAction: (message) => platformConfirm(message),
  openExternal: (url) => {
    const opened = globalThis.window?.open?.(url, "_blank", "noopener,noreferrer");
    if (opened) opened.opener = null;
  },
  onRegistryChanged: async () => {
    state.mcpRegistryLoaded = false;
    await loadMCPRegistryServers({ force: true });
  },
  showError,
  showToast,
  refresh: () => {
    if (state.activeSettingsPanel === "skills" && state.activeSkillTab === "mcp-tools") refreshActiveSettingsPanel();
  },
});

const {
  bind: bindAutomationToolCatalog,
  render: renderAutomationToolCatalog,
} = automationToolCatalog;

const skillsWorkbench = createSkillsWorkbenchController({
  state,
  request: api,
  bindAutomationToolCatalog,
  bindMCPRegistryActions,
  bindPluginRegistryActions,
  copyText,
  createServerSkill,
  createToolPermissionRule,
  currentSkillsPreferences,
  deleteServerSkill,
  deleteToolPermissionRule,
  isMCPRegistryActionBusy,
  importServerSkill,
  loadServerSkills,
  loadServerSkillDetail,
  loadWorkflowPolicy,
  localSkillID,
  normalizeMCPServer,
  normalizeSkillCommand,
  notifyTerminal,
  previewServerSkillImport,
  renderAutomationToolCatalog,
  renderMCPRegistryList,
  renderPluginRegistryPanel,
  resetSkillsPreferences,
  saveSkillsPreferences,
  saveWorkflowPreferences,
  showError,
  skillsPhaseB,
  skillsPrefsExport,
  getSkillContext,
  setSkillContext,
  getProject: () => state.project,
  getWorkline: () => state.workline,
  getAgent: () => state.agent,
  openSettingsPanel: selectSettingsPanel,
  updateServerSkill,
  updateToolPermissionRule,
});

const {
  bindSkillTabs,
  renderSkillSettingsContent,
} = skillsWorkbench;

const memorySettings = createMemorySettingsController({
  request: api,
  onChange: () => {
    if (state.activeSettingsPanel === "memory") refreshActiveSettingsPanel();
  },
  showError,
  showToast,
});

const archiveSettings = createArchiveSettingsController({
  request: api,
  refresh: () => {
    if (state.activeSettingsPanel === "archive") refreshActiveSettingsPanel();
  },
  // A permanent delete removes rows the sidebar and active selection may still
  // reference, so resync navigation from the server afterwards.
  onDeleted: () => loadProjects(),
  showError,
  showToast,
});

const automationControl = createAutomationControlController({
  request: api,
  onChange: () => {
    if (state.activeSettingsPanel === "im-gateway") refreshActiveSettingsPanel();
  },
  showError,
  showToast,
});

const scheduleWorkspace = createScheduleWorkspaceController({
  request: api,
  onChange: () => {
    if (state.activeWorkbench !== "schedules") return;
    renderScheduleSurface();
    renderProjects();
  },
  showError,
  showToast,
  confirmAction: async (message) => platformConfirm(message),
  formatTimestamp: formatDateTime,
});

const remoteAccessSettings = createRemoteAccessSettingsController({
  state,
  request: api,
  copyText: copyToClipboard,
  onChange: () => {
    updateSecurityModeUI();
    restoreAuthorizedAgentTransports();
    if (state.activeSettingsPanel === "remote-access") refreshActiveSettingsPanel();
  },
  showError,
  showToast,
});

const sharedAPISettings = createSharedAPISettingsController({
  state,
  request: api,
  reloadSettings: loadSettings,
  copyText: copyToClipboard,
  onChange: () => {
    if (state.activeSettingsPanel === "shared-api") refreshActiveSettingsPanel();
  },
  showError,
  showToast,
});

const usageHistory = createUsageHistoryController({
  state,
  request: api,
  onChange: () => {
    if (state.activeSettingsPanel === "usage") refreshActiveSettingsPanel();
  },
});

const settingsPanelRegistry = createSettingsPanelRegistry();
[
  ["profile", { render: renderProfileSettingsContent, bind: bindProfileSettingsActions }],
  ["archive", { render: archiveSettings.render, bind: archiveSettings.bind }],
  ["memory", { render: memorySettings.render, bind: memorySettings.bind }],
  ["skills", { render: () => renderSkillSettingsContent(state.activeSkillTab || "commands"), bind: () => bindSkillTabs(state.activeSkillTab || "commands") }],
  ["models", { render: renderModelSettingsContent, bind: bindModelSettingsActions }],
  ["providers", { render: renderProviderSettingsContent, bind: bindProviderSettingsActions }],
  ["shared-api", { render: sharedAPISettings.render, bind: sharedAPISettings.bind }],
  ["network-search", { render: renderNetworkSearchSettingsContent, bind: bindNetworkSearchSettingsActions }],
  ["im-gateway", { render: automationControl.render, bind: automationControl.bind }],
  ["notifications", { render: renderNotificationSettingsContent, bind: bindNotificationSettingsActions }],
  ["appearance", { render: renderAppearanceSettingsContent, bind: bindAppearanceSettingsActions }],
  ["storage", { render: renderStorageSettingsContent, bind: bindStorageSettingsActions }],
  ["usage", { render: usageHistory.render, bind: usageHistory.bind }],
  ["servers-system", { render: renderServerSystemSettingsContent, bind: bindRuntimeSettingsActions }],
  ["runtime", { render: renderRuntimeSettingsContent, bind: bindRuntimeSettingsActions }],
  ["remote-access", { render: remoteAccessSettings.render, bind: remoteAccessSettings.bind }],
  ["terminals", { render: renderTerminalSettingsContent, bind: bindTerminalSettingsActions }],
  ["about", { render: renderAboutSettingsContent, bind: bindAboutSettingsActions, layout: "about" }],
].forEach(([key, panel]) => settingsPanelRegistry.register(key, panel));

const taskWorkspace = createTaskWorkspaceController({
  request: api,
  host: "#taskWorkspaceOverview",
  kanbanHost: "#projectKanbanBody",
  scopeHost: "#taskWorkspaceScopes",
  translate: (key, params) => t(key, params),
  showError,
  showToast,
  confirmAction: async (message) => platformConfirm(message),
  onChange: () => {
    if (state.activeWorkbench !== "workbench") return;
    renderWorkbenchHeaderIdentity();
    renderProjects();
  },
  onOpenAgent: (agent, project) => openTaskWorkspaceAgent(agent, project).catch(showError),
});
taskWorkspace.bind();
$("taskWorkspaceScopes")?.querySelector('[data-task-workspace-scope="agent"]')?.addEventListener("click", () => {
  if (state.agent?.id) specBoard.load().catch(showError);
});

function formatDateTime(value) {
  return formatTimestamp(value);
}

function overviewLauncherContext() {
  const modelSelect = $("modelSelect");
  const models = Array.from(modelSelect?.options || [])
    .map((option) => ({
      value: String(option?.value || "").trim(),
      label: String(option?.textContent || option?.label || option?.value || "").trim(),
      group: String(option?.parentElement?.label || "").trim(),
    }))
    .filter((option) => option.value && option.label);
  const selectedModel = selectedModelValue() || currentModelValue();
  return {
    displayName: accountPreferences.getProfile?.().displayName || "",
    projects: (Array.isArray(state.projects) ? state.projects : []).map((project) => ({
      id: String(project?.id || ""),
      name: String(project?.name || project?.id || ""),
      path: String(project?.gitPath || ""),
    })),
    selectedProjectId: String(state.project?.id || resolveTopNavigationProjectId({ projects: state.projects, conversations: state.navigationConversations }, { projectOrder: getProjectOrder() }) || ""),
    models,
    selectedModel,
    selectedEffort: selectedReasoningEffort(selectedModel),
    hour: new Date().getHours(),
  };
}

const overviewDashboard = createOverviewDashboardController({
  request: api,
  host: "#overviewDashboard",
  translate: t,
  formatDateTime,
  getLauncherContext: overviewLauncherContext,
  onLaunch: launchOverviewPrompt,
  onChooseDirectory: () => openDirectoryChooser(state.project?.gitPath || state.agent?.cwd || ""),
  onNavigate: handleOverviewNavigation,
  onError: showError,
});

const settingsHelp = createSettingsHelpController({

  getRoot: () => $("settingsContentBody"),
  trigger: $("settingsHelpBtn"),
  panel: $("settingsHelpPanel"),
  title: $("settingsHelpTitle"),
  body: $("settingsHelpBody"),
  closeButton: $("closeSettingsHelpBtn"),
  backdrop: $("settingsHelpBackdrop"),
  translate: t,
});
settingsHelp.bind();

function conversationDetailMetrics() {
  const messages = Array.isArray(state.currentMessages) ? state.currentMessages : [];
  const summary = state.activeRunSummary || {};
  const terminal = terminalOutputStats();
  const previewRunning = Boolean(state.workspacePreviewStatus?.running || ["running", "started", "ready"].includes(String(state.workspacePreviewStatus?.status || "").toLowerCase()));
  return {
    messages: messages.length,
    cost: Number(summary.costUsd || 0),
    inputTokens: Number(summary.inputTokens || 0),
    outputTokens: Number(summary.outputTokens || 0),
    cacheTokens: Number(summary.cacheReadTokens || summary.cachedTokens || 0),
    tools: Number(summary.toolCallCount || (Array.isArray(summary.toolCalls) ? summary.toolCalls.length : 0)),
    terminal: state.terminalWS ? 1 : terminal.lines > 1 ? 1 : 0,
    browser: previewRunning ? 1 : 0,
    approvals: Number(summary.pendingApprovals || 0),
  };
}

function renderConversationDetails() {
  const body = $("conversationDetailsBody");
  if (!body) return;
  const metrics = conversationDetailMetrics();
  const rows = [
    [sx("app.sessionId"), state.agent?.id || "—", true],
    [sx("app.type"), state.agent?.type || sx("app.programWorkspace")],
    [sx("app.projectPath"), state.agent?.cwd || state.project?.gitPath || "—", true],
    [sx("app.projectName"), state.project?.name || "—"],
    [sx("app.workline"), state.workline?.title || state.workline?.id || "—"],
    [sx("app.currentModel"), state.agent?.model || currentWorkspaceModel()],
    [sx("app.permissionMode"), state.agent?.permissionMode || "—"],
  ];
  const workStateHTML = renderWorkStateHTML(state.workState, {
    title: t("workspace.workState.title"), goal: t("workspace.workState.goal"), role: t("workspace.workState.role"),
    taskCounts: t("workspace.workState.taskCounts"), activeTask: t("workspace.workState.activeTask"),
    verification: t("workspace.workState.verification"), reviewer: t("workspace.workState.reviewer"), declaredTest: t("workspace.workState.declaredTest"),
    taskStatuses: { todo: t("workspace.workState.todo"), doing: t("workspace.workState.doing"), done: t("workspace.workState.done"), blocked: t("workspace.workState.blocked") },
    verificationStatuses: { not_configured: t("workspace.workState.notConfigured"), declared: t("workspace.workState.declared"), reviewed: t("workspace.workState.reviewed"), stale: t("workspace.workState.stale"), pending: t("workspace.workState.pending"), running: t("workspace.workState.running"), passed: t("workspace.workState.passed"), pass: t("workspace.workState.passed"), failed: t("workspace.workState.failed"), blocked: t("workspace.workState.blocked"), skipped: t("workspace.workState.skipped") },
    reviewerStatuses: { pass: t("workspace.workState.reviewPass"), needs_human: t("workspace.workState.reviewNeedsHuman"), block_recommended: t("workspace.workState.reviewBlockRecommended"), unavailable: t("workspace.workState.reviewUnavailable") },
  });
  setHTMLIfChanged(body, `
    <section class="conversation-detail-hero"><div><h2>${escapeHtml(state.project?.name || state.agent?.title || sx("app.noConversationSelected"))}</h2><p>${escapeHtml(state.agent?.title || sx("app.selectConversationHint"))}</p></div><span class="conversation-detail-status">${escapeHtml(state.agent?.status || t("chat.idle"))}</span></section>
    ${backgroundTasks.renderContinuationStatusHTML()}
    ${workStateHTML}
    <section class="conversation-metric-grid">
      ${[["Messages", metrics.messages], ["Cost", `$${metrics.cost.toFixed(4)}`], [sx("app.inputTokens"), metrics.inputTokens], [sx("app.outputTokens"), metrics.outputTokens], [sx("app.cacheTokens"), metrics.cacheTokens], [sx("app.tools"), metrics.tools], [t("terminal.title"), metrics.terminal], [sx("app.browser"), metrics.browser], [sx("app.pendingApprovals"), metrics.approvals]].map(([label, value]) => `<div class="conversation-metric-card"><span>${escapeHtml(label)}</span><strong>${escapeHtml(typeof value === "number" ? formatNumber(value) : value)}</strong></div>`).join("")}
    </section>
    <section class="conversation-detail-table">${rows.map(([label, value, copy]) => `<div class="conversation-detail-row"><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong>${copy && value !== "—" ? `<button type="button" data-copy-detail="${escapeAttr(value)}">${escapeHtml(t("workspace.chat.copy"))}</button>` : ""}</div>`).join("")}</section>
    <button class="legacy-secondary-btn conversation-runtime-link" type="button" data-details-runtime>${escapeHtml(sx("app.viewRuntime"))}</button>
  `);
  body.querySelectorAll("[data-copy-detail]").forEach((node) => node.addEventListener("click", () => copyText(node.dataset.copyDetail)));
  body.querySelector("[data-details-runtime]")?.addEventListener("click", () => {
    closeConversationDetails();
    openSettingsModal("runtime");
  });
}

function openConversationDetails() {
  if (!state.agent) showToast(am("selectConversationFirst"), "warn");
  backgroundTasks.closeTray("details-open");
  closeWorkspace();
  toggleTerminal(true);
  $("appShell")?.classList.add("details-open");
  $("conversationDetailsPanel")?.classList.remove("hidden");
  $("runtimeStatusBtn")?.classList.add("active");
  $("runtimeStatusBtn")?.setAttribute("aria-expanded", "true");
  renderConversationDetails();
}

function closeConversationDetails() {
  $("appShell")?.classList.remove("details-open");
  $("conversationDetailsPanel")?.classList.add("hidden");
  $("runtimeStatusBtn")?.classList.remove("active");
  $("runtimeStatusBtn")?.setAttribute("aria-expanded", "false");
}

// One drawn mark for both entries to the scheme toggle. The rail used the text
// glyphs ☀/☾, which render at whatever the font decides and sat next to stroked
// SVG icons everywhere else; the phone drawer had a moon that never changed at
// all. The icon shows the scheme you are about to switch to.
const themeToggleIconMarkup = Object.freeze({
  moon: '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 14.5A8.5 8.5 0 0 1 9.5 4a8.5 8.5 0 1 0 10.5 10.5z"></path></svg>',
  sun: '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"></circle><path d="M12 2.5v2M12 19.5v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M2.5 12h2M19.5 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4"></path></svg>',
});

function updateGlobalThemeToggle() {
  const dark = currentAppearancePreferences().theme === "dark";
  const markup = dark ? themeToggleIconMarkup.sun : themeToggleIconMarkup.moon;
  for (const [buttonId, iconId] of [
    ["globalThemeToggleBtn", "globalThemeToggleIcon"],
    ["mobileSidebarThemeBtn", "mobileSidebarThemeIcon"],
  ]) {
    const button = $(buttonId);
    const icon = $(iconId);
    if (!button || !icon) continue;
    button.setAttribute("aria-pressed", dark ? "true" : "false");
    icon.innerHTML = markup;
  }
}

async function loadSettings() {
  const seq = ++state.settingsLoadSeq;
  try {
    const settings = await api("/api/settings");
    if (seq !== state.settingsLoadSeq) return;
    state.settings = settings;
    updateSidebarAccountSummary();
    renderModelOptions();
    refreshReasoningEffortControl();
    refreshFastModeControl();
  } catch (err) {
    if (seq === state.settingsLoadSeq) throw err;
  }
}

async function testServerNotification() {
  state.serverNotificationTesting = true;
  state.serverNotificationError = "";
  try {
    await api("/api/notifications/test", { method: "POST", body: JSON.stringify({}) });
    showToast(am("notificationTestSent"), "success", { force: true });
    notifyTerminal(`[info] ${am("notificationTestSent")}\n`);
  } catch (err) {
    state.serverNotificationError = err.message || String(err);
    showError(err);
  } finally {
    state.serverNotificationTesting = false;
    if (state.activeSettingsPanel === "notifications") refreshActiveSettingsPanel();
  }
}

function projectKanbanTranslation(key, params = {}, fallback = "") {
  const translationKey = String(key || "").startsWith("projectKanban.") ? String(key) : `projectKanban.${key}`;
  const translated = t(translationKey, params);
  return translated === translationKey ? fallback : translated;
}

function currentNavigationCreateTarget() {
  return navigationCreateTarget(state);
}

function syncNavigationCreateButton(button) {
  if (!button) return;
  const target = currentNavigationCreateTarget();
  const labelKey = navigationCreateLabelKey(target);
  button.dataset.createTarget = target;
  setTranslatedAttribute(button, "title", labelKey);
  setTranslatedAttribute(button, "aria-label", labelKey);
}

function applyPrimaryWorkbench(value) {
  const mode = normalizedPrimaryWorkbench(value);
  const previousMode = state.activeWorkbench;
  state.primaryModePreference = mode;
  state.activeWorkbench = mode;
  const layout = primaryWorkbenchLayout(mode, { overviewActive: state.overviewActive });
  const { overview, workbench, schedules } = layout;
  if (previousMode !== mode) {
    state.projectQuery = "";
    if ($("projectSearch")) $("projectSearch").value = "";
    $("projectSearchWrap")?.classList.add("hidden");
    $("projectSearchToggleBtn")?.classList.remove("active");
    if (mode === "schedules" && scheduleWorkspace.getState().query) scheduleWorkspace.setQuery("");
  }
  for (const [id, hidden] of Object.entries(layout.hidden)) $(id)?.classList.toggle("hidden", hidden);
  for (const [name, active] of Object.entries(layout.bodyClasses)) document.body.classList.toggle(name, active);
  const modalOpen = elementVisible("settingsModal");
  if (!modalOpen) setGlobalRailActive(overviewRailTarget({ overviewActive: overview, activeWorkbench: mode }));
  if (workbench) {
    taskWorkspace.setContext({ projectId: state.project?.id || "", agentId: state.agent?.id || "" });
    if (previousMode !== mode) taskWorkspace.setScope("dispatch");
    taskWorkspace.load({ silent: taskWorkspace.getState().loaded }).catch(showError);
  }
  if (schedules) {
    renderScheduleSurface();
    const scheduleState = scheduleWorkspace.getState();
    if (!scheduleState.loaded && !scheduleState.loading) scheduleWorkspace.load().catch(showError);
  }
  renderWorkbenchShell();
  renderProjects();
  syncMobilePageTitle();
  syncThemePageContext();
  if (workbench && taskWorkspace.getState().scope === "agent" && state.agent?.id) specBoard.load().catch(showError);
  return mode;
}

function switchPrimaryWorkbench(value) {
  const mode = normalizedPrimaryWorkbench(value);
  state.overviewActive = false;
  backgroundTasks.closeTray("workbench-switch");
  closeConversationDetails();
  closeSettingsModal({ restoreWorkbench: false, restoreFocus: false });
  if (mode === "schedules") {
    closeWorkspace();
    closeGitModal();
    toggleTerminal(true);
  }
  return setPrimaryModePreference(mode);
}

const globalRailSettingsTargets = new Set(["profile"]);

const MOBILE_SETTINGS_MEDIA_QUERY = "(max-width: 767px)";

function isMobileAppViewport() {
  const mediaMatch = globalThis.matchMedia?.(MOBILE_SETTINGS_MEDIA_QUERY)?.matches;
  if (typeof mediaMatch === "boolean") return mediaMatch;
  return Number(globalThis.innerWidth || 0) <= 767;
}

function leaveOverviewForMobile() {
  if (!isMobileAppViewport() || !state.overviewActive) return false;
  state.overviewActive = false;
  applyPrimaryWorkbench("conversation");
  return true;
}

function syncSettingsCloseControl() {
  const button = $("closeSettingsModalBtn");
  if (!button) return;
  const mobile = state.settingsMobileViewport && isMobileSettingsViewport();
  const detail = mobile && state.mobileSettingsView === "detail";
  const messageKey = detail
    ? "settings.mobile.backToIndex"
    : mobile
      ? "settings.mobile.close"
      : "settings.backToChat";
  const label = t(messageKey);
  button.textContent = detail ? "←" : "×";
  button.title = label;
  button.setAttribute("aria-label", label);
  button.setAttribute("data-i18n-title", messageKey);
  button.setAttribute("data-i18n-aria-label", messageKey);
}

function renderMobileSettingsIndex() {
  const nav = $("settingsNav");
  if (!nav) return;
  nav.setAttribute("aria-label", t("settings.mobile.indexTitle"));
  nav.innerHTML = resolvedMobileSettingsSections().map((section) => `
    <section class="settings-mobile-index-group" data-mobile-settings-section="${escapeAttr(section.key)}" aria-labelledby="mobile-settings-section-${escapeAttr(section.key)}">
      <div id="mobile-settings-section-${escapeAttr(section.key)}" class="settings-mobile-index-heading">${escapeHtml(section.label)}</div>
      <div class="settings-mobile-index-list">
        ${section.items.map((item) => `
          <button class="settings-nav-item settings-mobile-index-row" type="button" data-settings-key="${escapeAttr(item.key)}" aria-label="${escapeAttr(item.label)}">
            <span class="settings-nav-icon" aria-hidden="true">${settingsIconSVG(item.icon)}</span>
            <span class="settings-nav-label settings-mobile-index-copy"><strong>${escapeHtml(item.label)}</strong><small>${escapeHtml(item.subtitle)}</small></span>
            <span class="settings-mobile-index-chevron" aria-hidden="true">›</span>
          </button>
        `).join("")}
      </div>
    </section>
  `).join("");
  nav.querySelectorAll("[data-settings-key]").forEach((node) => {
    node.addEventListener("click", () => selectSettingsPanel(node.dataset.settingsKey));
  });
  bindSettingsArrowNavigation(nav, "[data-settings-key]", { ArrowUp: -1, ArrowDown: 1, Home: "first", End: "last" });
}

function showMobileSettingsIndex({ focus = false } = {}) {
  if (!isMobileSettingsViewport() || !settingsModalOpen()) return false;
  settingsHelp.close({ restoreFocus: false });
  state.settingsMobileViewport = true;
  state.mobileSettingsView = "index";
  if ($("settingsModalTitle")) $("settingsModalTitle").textContent = t("settings.mobile.indexTitle");
  state.settingsSearchQuery = "";
  syncSettingsSearchInput();
  applyMobileSettingsViewClasses();
  renderMobileSettingsIndex();
  if (focus) globalThis.queueMicrotask?.(() => $("settingsNav")?.querySelector?.(".settings-mobile-index-row")?.focus?.());
  return true;
}

function requestCloseSettingsModal(options) {
  if (state.settingsMobileViewport && state.mobileSettingsView === "detail" && showMobileSettingsIndex({ focus: true })) return;
  closeSettingsModal(options);
}

function openSettingsModal(key = "providers", { trigger = document.activeElement, showMobileIndex = false } = {}) {
  backgroundTasks.closeTray("settings-open");
  closeConversationDetails();
  if (state.workspaceOpen && state.workspaceTab === "preview") closeWorkspace();
  const itemKey = settingsItemByKey(key)?.key || "providers";
  // Opening Settings lands on the category itself. The provider account
  // pages are drill-downs reached from the provider list, so resuming one
  // made "Settings" appear to open on some account screen instead of the
  // list the sidebar says is selected.
  resetProviderConsoleToProviderList();
  const modal = $("settingsModal");
  const wasOpen = !modal?.classList.contains("hidden");
  state.settingsSearchQuery = "";
  state.activeSettingsCategory = settingsCategoryForItem(itemKey, "api");
  state.settingsMobileViewport = isMobileSettingsViewport();
  state.mobileSettingsView = "detail";
  modal?.classList.remove("hidden");
  setThemePageContext("");
  if (state.settingsMobileViewport) exitSettingsShell();
  else enterSettingsShell();
  if (!wasOpen) beginSettingsDialogFocus(trigger);
  setGlobalRailActive("profile");
  syncSettingsSearchInput();
  warmSettingsData();
  applyMobileSettingsViewClasses();
  selectSettingsPanel(itemKey);
  if (itemKey === "appearance") themeManager.loadCatalog({ force: true }).catch(() => {});
  if (state.settingsMobileViewport && showMobileIndex) showMobileSettingsIndex();
}

function closeSettingsModal({ restoreWorkbench = true, restoreFocus = true } = {}) {
  const modal = $("settingsModal");
  const wasOpen = Boolean(modal && !modal.classList.contains("hidden"));
  if (wasOpen) {
    settingsHelp.close({ restoreFocus: false });
    remoteAccessSettings.consumeGeneratedPassword();
    sharedAPISettings.consumeOneTimeToken();
    discardProviderConsoleDraft();
    $("settingsContentBody").textContent = "";
  }
  modal?.classList.add("hidden");
  state.mobileSettingsView = "detail";
  applyMobileSettingsViewClasses();
  exitSettingsShell();
  syncThemePageContext();
  if (wasOpen && restoreFocus) restoreSettingsDialogFocus();
  if (restoreWorkbench) setGlobalRailActive(currentShellRailTarget());
  if (wasOpen && typeof resumeSetupWizardAfterSettings === "function") {
    Promise.resolve().then(() => resumeSetupWizardAfterSettings()).catch(showError);
  }
}

async function openOverviewConversation(id = "") {
  const entity = overviewEntity("recentConversations", id) || overviewEntity("activeRuns", id) || overviewEntity("activeTasks", id);
  const agentId = entity?.agentId || id;
  let target = state.navigationConversations.find((item) => item.targetId === id || item.agentId === agentId || item.id === id) || null;
  if (!target && id) {
    await loadProjects();
    target = state.navigationConversations.find((item) => item.targetId === id || item.agentId === agentId || item.id === id) || null;
  }
  switchPrimaryWorkbench("conversation");
  if (!id) return;
  if (!target) throw new Error(t("overview.conversationUnavailable"));
  await selectNavigationConversation(target.targetId || target);
}

async function openOverviewTask(id = "") {
  switchPrimaryWorkbench("workbench");
  taskWorkspace.setScope("dispatch");
  await taskWorkspace.load({ silent: taskWorkspace.getState().loaded });
  if (!id) return;
  const entity = overviewEntity("activeTasks", id);
  let agentId = entity?.agentId || "";
  if (!agentId) {
    for (const project of taskWorkspace.getState().workspace.projects) {
      const agent = project.agents.find((item) => item.tasks.some((task) => task.id === id));
      if (agent) { agentId = agent.id; break; }
    }
  }
  if (!agentId || !taskWorkspace.selectTask(agentId, id)) throw new Error(t("overview.taskUnavailable"));
  focusOverviewDataElement("[data-task-workspace-task]", "taskWorkspaceTask", `${agentId}::${id}`);
}

async function openOverviewRuns(id = "") {
  const runs = overviewDashboard.getState().payload.activeRuns || [];
  const run = id ? overviewEntity("activeRuns", id) : runs[0] || null;
  if (!run) {
    switchPrimaryWorkbench("conversation");
    if (id) throw new Error(t("overview.runUnavailable"));
    return;
  }
  await openOverviewConversation(run.agentId);
  if (state.agent?.id !== run.agentId) throw new Error(t("overview.runUnavailable"));
  const summary = await loadRunSummary(run.id, { agentId: run.agentId });
  if (!summary) throw new Error(t("overview.runUnavailable"));
  focusOverviewDataElement("[data-run-id]", "runId", run.id, { focusSelector: "button:not(:disabled)" });
}

async function openOverviewSchedules(id = "") {
  if (!id) {
    switchPrimaryWorkbench("schedules");
    return;
  }
  const loaded = await scheduleWorkspace.load({ preferredId: id, autoHistory: false });
  switchPrimaryWorkbench("schedules");
  if (!loaded) throw new Error(t("overview.scheduleUnavailable"));
  const selected = await scheduleWorkspace.select(id, { loadHistory: false });
  if (!selected) throw new Error(t("overview.scheduleUnavailable"));
  void scheduleWorkspace.loadHistory(id);
  focusOverviewDataElement("[data-schedule-workspace]", "scheduleWorkspace", id, { focusSelector: "input, select, textarea, button:not(:disabled)" });
}

async function locateOverviewApprovals() {
  const agentIds = overviewApprovalAgentIds();
  const batchSize = 8;
  for (let offset = 0; offset < agentIds.length; offset += batchSize) {
    const batch = agentIds.slice(offset, offset + batchSize);
    const results = await Promise.allSettled(batch.map(async (agentId) => ({
      agentId,
      approvals: await api(`/api/agents/${encodeURIComponent(agentId)}/tool-calls/pending`),
    })));
    for (const result of results) {
      if (result.status === "fulfilled" && Array.isArray(result.value.approvals) && result.value.approvals.length) return result.value;
    }
  }
  return null;
}

async function openOverviewApprovals() {
  const located = await locateOverviewApprovals();
  if (!located) {
    switchPrimaryWorkbench("conversation");
    showToast(t("overview.approvalsUnavailable"), "info", { force: true });
    return;
  }
  await openOverviewConversation(located.agentId);
  if (state.agent?.id !== located.agentId) throw new Error(t("overview.approvalsUnavailable"));
  replacePendingApprovals(located.approvals, located.agentId);
  const firstApprovalId = String(located.approvals[0]?.toolUseId || located.approvals[0]?.tool_use_id || "").trim();
  if (firstApprovalId) focusOverviewDataElement("[data-approval-card]", "approvalCard", firstApprovalId, { focusSelector: "button:not(:disabled)" });
}

async function openOverviewDashboard() {
  closeSidebarSettingsMenu();
  closeMobileSidebar();
  backgroundTasks.closeTray("overview-open");
  closeConversationDetails();
  closeSettingsModal({ restoreWorkbench: false, restoreFocus: false });
  closeWorkspace();
  closeGitModal();
  toggleTerminal(true);
  if (isMobileAppViewport()) {
    state.overviewActive = false;
    applyPrimaryWorkbench("conversation");
    return;
  }
  state.overviewActive = true;
  applyPrimaryWorkbench("conversation");
  await overviewDashboard.load();
}

function handleOverviewNavigation(action, id = "") {
  const route = overviewNavigationRoute(action);
  if (!route) return undefined;
  const target = route.usesId ? id : "";
  if (route.handler === "rail-conversation") return activateGlobalRailTarget("conversation");
  if (route.handler === "task") return openOverviewTask(target);
  if (route.handler === "schedules") return openOverviewSchedules(target);
  if (route.handler === "approvals") return openOverviewApprovals();
  if (route.handler === "runs") return openOverviewRuns(target);
  if (route.handler === "conversation") return openOverviewConversation(target);
  return undefined;
}

function activateGlobalRailTarget(target) {
  // The global rail is immediately interactive after the shell is revealed,
  // while startup data can still be hydrating. Claim navigation before any UI
  // write so a late startup restore cannot send the user back to Home.
  navigationStartupGuard.beginUserNavigation();
  const key = String(target || "conversation");
  const openingConversationFromOverview = key === "conversation" && state.overviewActive;
  state.startupWorkbenchIntent = state.initializing ? key : "";
  closeSidebarSettingsMenu();
  closeMobileSidebar();
  if (key === "home") {
    openOverviewDashboard().catch(showError);
    return;
  }
  if (key === "conversation") {
    switchPrimaryWorkbench("conversation");
    if (!state.initializing && (openingConversationFromOverview || !state.agent)) {
      openDefaultConversationTarget({ preserveMessageState: true }).catch(showError);
    }
    return;
  }
  if (key === "schedules") {
    switchPrimaryWorkbench("schedules");
    return;
  }
  if (globalRailSettingsTargets.has(key)) openSettingsModal(key === "profile" ? "providers" : key);
}

function renderSettingsNav(activeKey = "providers") {
  const nav = $("settingsNav");
  if (!nav) return;
  if (state.settingsMobileViewport && state.mobileSettingsView === "index" && isMobileSettingsViewport()) {
    renderMobileSettingsIndex();
    return;
  }
  nav.setAttribute("aria-label", t("settings.directory"));
  syncSettingsSearchInput();
  const categoryKey = settingsCategoryForItem(activeKey, state.activeSettingsCategory || "api");
  state.activeSettingsCategory = categoryKey;
  renderSettingsCategoryNav(categoryKey);
  const groups = filteredSettingsSections();
  if (!groups.length) {
    nav.innerHTML = `<div class="settings-nav-empty"><strong>${escapeHtml(am("noMatchingSettings"))}</strong><span>${escapeHtml(am("matchingSettingsHint"))}</span></div>`;
    return;
  }
  nav.innerHTML = groups.map((category) => {
    const sectionId = category.items[0]?.key || category.label;
    const order = getSettingsNavOrder(sectionId);
    const orderedItems = applySettingsNavOrder(category.items, order);
    return `
    <section class="settings-nav-group" aria-label="${escapeAttr(category.label)}" data-settings-nav-section="${escapeAttr(sectionId)}">
      <div class="settings-nav-group-label">${escapeHtml(category.label)}</div>
      ${orderedItems.map((item) => `
        <button class="settings-nav-item ${item.key === activeKey ? "active" : ""}" type="button" ${item.key === activeKey ? 'aria-current="page"' : ""} draggable="true" data-settings-key="${escapeAttr(item.key)}" title="${escapeAttr(item.label)}">
          <span class="settings-nav-icon" aria-hidden="true">${settingsIconSVG(item.icon)}</span>
          <span class="settings-nav-label"><strong>${escapeHtml(item.label)}</strong></span>
        </button>
      `).join("")}
    </section>
  `;
  }).join("");
  nav.querySelectorAll("[data-settings-key]").forEach((node) => {
    node.addEventListener("click", () => selectSettingsPanel(node.dataset.settingsKey));
  });
  bindSettingsArrowNavigation(nav, "[data-settings-key]", { ArrowUp: -1, ArrowDown: 1, Home: "first", End: "last" });
  bindSettingsNavDrag(nav);
}

function updateSettingsSearchQuery(value) {
  state.settingsSearchQuery = String(value || "").slice(0, 80);
  const sections = filteredSettingsSections();
  const activeKey = state.activeSettingsPanel || "profile";
  const nextKey = nextFilteredSettingsKey(activeKey, sections);
  renderSettingsNav(nextKey);
  if (nextKey !== activeKey) selectSettingsPanel(nextKey);
}

function selectSettingsPanel(key) {
  const item = settingsItemByKey(key) || settingsItems[0];
  if (state.activeSettingsPanel === "providers" && item.key !== "providers") discardProviderConsoleDraft();
  if ($("settingsModalTitle")) $("settingsModalTitle").textContent = isMobileSettingsViewport() ? item.label : t("settings.dialogTitle");
  if (isMobileSettingsViewport() && settingsModalOpen()) {
    state.settingsMobileViewport = true;
    state.mobileSettingsView = "detail";
    applyMobileSettingsViewClasses();
  }
  const panel = settingsPanelRegistry.resolve(item.key);
  const categoryKey = settingsCategoryForItem(item.key, state.activeSettingsCategory || "api");
  settingsHelp.close({ restoreFocus: false });
  if (state.activeSettingsPanel === "shared-api" && item.key !== "shared-api") sharedAPISettings.consumeOneTimeToken();
  state.activeSettingsCategory = categoryKey;
  state.activeSettingsPanel = item.key;
  if (settingsModalOpen()) setGlobalRailActive(item.key === "im-gateway" ? "schedules" : "profile");
  renderSettingsNav(item.key);
  const isAboutPanel = item.key === "about";
  $("settingsContentTitle")?.closest(".settings-content-head")?.classList.remove("hidden");
  $("settingsContentBody")?.closest(".settings-content")?.classList.toggle("about-panel-active", isAboutPanel);
  $("settingsContentTitle").textContent = item.label;
  $("settingsContentSubtitle").textContent = item.subtitle;
  settingsHelp.sync({ key: item.key, label: item.label, overview: item.subtitle });
  const layout = panel?.layout || (isAboutPanel ? "about" : "");
  const content = panel ? panel.render(item) : renderGenericSettingsContent(item);
  $("settingsContentBody").innerHTML = `<div class="settings-page-frame" data-settings-page="${escapeAttr(item.key)}"${layout ? ` data-panel-layout="${escapeAttr(layout)}"` : ""}>${content}</div>`;
  panel?.bind?.(item);
}

function renderGenericSettingsContent(item) {
  const details = settingsPanelDetails(item.key);
  return `
    <div class="settings-panel-card">
      <div class="settings-panel-icon">${settingsIconSVG(item.icon)}</div>
      <div>
        <div class="settings-panel-title">${escapeHtml(item.label)}</div>
        <p data-settings-help-copy>${escapeHtml(item.subtitle)}</p>
      </div>
    </div>
    <div class="settings-panel-grid">
      ${details.map((detail) => `
        <div class="settings-info-card">
          <div class="settings-info-title">${escapeHtml(detail.title)}</div>
          <div class="settings-info-text">${escapeHtml(detail.text)}</div>
        </div>
      `).join("")}
    </div>
  `;
}

function settingsPanelDetails(key) {
  const base = {
    profile: [
      { title: am("currentStatus"), text: am("profileStatusDescription") },
      { title: am("quickActions"), text: am("profileQuickActionsDescription") },
    ],
    models: [
      { title: am("defaultModel"), text: state.settings?.agent?.defaultModel || am("defaultModelNotLoaded") },
      { title: am("configuredProviders"), text: am("providerProfiles", { count: state.settings?.providers?.length || 0 }) },
    ],
    providers: [
      { title: "Codex OAuth", text: codexProviderSummary() },
      { title: "Secret", text: am("secretDescription") },
    ],
    "servers-system": [
      { title: am("serverPort"), text: `${state.settings?.server?.host || "localhost"}:${state.settings?.server?.port || "16888"}` },
      { title: am("version"), text: state.settings?.version || "0.1.0-dev" },
    ],
    about: [
      { title: "Autoto", text: "Local-first Go AI coding Autoto server MVP." },
      { title: "License", text: am("licenseDescription") },
    ],
  };
  return base[key] || [
    { title: am("reservedPage"), text: am("reservedPageDescription") },
    { title: am("nextStep"), text: am("nextStepDescription") },
  ];
}

function renderEmptyWorkspaceCard({ title = t("chat.emptyTitle"), text = t("chat.emptyDescription"), action = t("chat.chooseFolderAction"), hint = t("chat.emptyHint"), icon = "☻" } = {}) {
  return `
    <div class="empty-workspace-card">
      <div class="empty-workspace-icon">${escapeHtml(icon)}</div>
      <div class="empty-workspace-title">${escapeHtml(title)}</div>
      <div class="empty-workspace-text">${escapeHtml(text)}</div>
      <button class="empty-workspace-action" type="button" data-open-directory-shortcut="new">${escapeHtml(action)}</button>
      <div class="empty-workspace-hint">${escapeHtml(hint)}</div>
    </div>
  `;
}

function clearMessageViewportBusyTimer() {
  if (messageViewportBusyTimer === null) return;
  window.clearTimeout(messageViewportBusyTimer);
  messageViewportBusyTimer = null;
}

function markMessageViewportBusy(options = {}) {
  const el = $("messages");
  if (!el) return;
  clearMessageViewportBusyTimer();
  el.setAttribute("aria-busy", "true");
  el.dataset.initialChatState = "loading";
  delete el.dataset.contextSwitching;
  delete el.dataset.switchingLabel;
  if (!options.contextSwitch) return;

  const label = String(options.label || am("projectLoadingTitle"));
  messageViewportBusyTimer = window.setTimeout(() => {
    messageViewportBusyTimer = null;
    const current = $("messages");
    if (!current || current.getAttribute("aria-busy") !== "true") return;
    current.dataset.contextSwitching = "true";
    current.dataset.switchingLabel = label;
  }, messageViewportBusyDelayMs);
}

function clearMessageViewportBusy() {
  clearMessageViewportBusyTimer();
  const el = $("messages");
  if (!el) return;
  el.removeAttribute("aria-busy");
  delete el.dataset.initialChatState;
  delete el.dataset.contextSwitching;
  delete el.dataset.switchingLabel;
}

function syncProjectOperationContext() {
  const active = projectOperationContextActive();
  const body = document.body;
  const wasActive = body?.classList.contains("project-operation-context") || false;
  body?.classList.toggle("project-operation-context", active);
  if (body) body.dataset.navigationContext = active ? "project" : "conversation";
  (document.querySelectorAll?.("[data-project-context-only]") || []).forEach((node) => {
    node.setAttribute("aria-hidden", active ? "false" : "true");
  });
  const permissionMode = $("permissionMode");
  if (permissionMode) permissionMode.disabled = !active;
  if (wasActive && !active) {
    toggleTerminal(true);
    closeWorkspace();
    closeGitModal();
    backgroundTasks.closeTray("conversation-context");
  }
  renderTerminalButtonState();
  return active;
}

function permissionLabel(value) {
  const labels = {
    readOnly: t("chat.permission.readOnly"),
    acceptEdits: t("chat.permission.editable"),
    bypassPermissions: t("chat.permission.allowAll"),
    dontAsk: t("chat.permission.dontAsk"),
    default: t("chat.permission.automatic"),
  };
  return labels[value] || value || t("chat.permission.automatic");
}

function permissionMobileLabel(value) {
  return {
    readOnly: "RO",
    acceptEdits: "RW",
    bypassPermissions: "ALL",
    dontAsk: "NA",
    default: "AUTO",
  }[value] || "AUTO";
}

function updatePermissionModeDisplay() {
  const select = $("permissionMode");
  const display = document.querySelector(".permission-toolbar-pill .mode-display");
  if (!select || !display) return;
  const permission = effectivePermissionForDisplay(select.value);
  const label = permissionLabel(permission);
  display.textContent = label;
  (display.dataset ||= {}).mobileLabel = permissionMobileLabel(permission);
  const badge = $("permissionRiskBadge");
  if (badge) {
    badge.textContent = label;
    badge.classList.toggle("danger", permission === "bypassPermissions");
  }
}

function connectionMobileLabel(connection) {
  if (!connection?.remote) return "LAN";
  return connection.restricted ? "T−" : "T+";
}

function renderAgentTitleEditor(surface) {
  const { display, input, edit, save, cancel, editLabel, fieldLabel } = titleEditorElements(surface);
  const editable = Boolean(state.agent?.id);
  if (!editable) {
    state.titleEditing = false;
    state.titleSaving = false;
    state.titleDraft = "";
  }
  const editing = editable && state.titleEditing;
  const title = titleForSurface(surface);
  if (display) {
    setTextIfChanged(display, title);
    display.disabled = !editable || state.titleSaving;
    display.title = editable ? editLabel : title;
    display.setAttribute("aria-label", editable ? editLabel : title);
    display.classList.toggle("hidden", editing);
  }
  if (input) {
    input.classList.toggle("hidden", !editing);
    input.disabled = state.titleSaving;
    input.setAttribute("aria-label", fieldLabel);
    if (editing && input.value !== state.titleDraft) input.value = state.titleDraft;
  }
  if (edit) {
    edit.disabled = !editable || state.titleSaving;
    edit.classList.toggle("hidden", editing);
    edit.title = editLabel;
    edit.setAttribute("aria-label", editLabel);
  }
  if (save) {
    save.disabled = state.titleSaving;
    save.classList.toggle("hidden", !editing);
    save.toggleAttribute("aria-busy", state.titleSaving);
    save.title = am("saveConversationTitle");
    save.setAttribute("aria-label", am("saveConversationTitle"));
  }
  if (cancel) {
    cancel.disabled = state.titleSaving;
    cancel.classList.toggle("hidden", !editing);
    cancel.title = am("cancelConversationTitle");
    cancel.setAttribute("aria-label", am("cancelConversationTitle"));
  }
}

function syncMobilePageTitle() {
  const node = $("mobilePageTitle");
  if (!node) return;
  if (state.overviewActive) {
    setTextIfChanged(node, t("shell.nav.home"));
    return;
  }
  if (state.activeWorkbench === "workbench") {
    setTextIfChanged(node, titleForSurface("workbench") || t("workbench.title"));
    return;
  }
  if (state.activeWorkbench === "schedules") {
    const scheduleState = scheduleWorkspace.getState();
    const selected = scheduleState.schedules.find((item) => item.id === scheduleState.selectedScheduleId);
    setTextIfChanged(node, selected?.name || t("shell.nav.schedules"));
    return;
  }
  setTextIfChanged(node, (!state.project && !state.agent) ? t("shell.nav.conversation") : titleForSurface("conversation"));
}

function renderConversationHeaderIdentityNow() {
  renderAgentTitleEditor("conversation");
  syncMobilePageTitle();
}

function renderWorkbenchHeaderIdentityNow() {
  syncMobilePageTitle();
  const workspaceState = taskWorkspace.getState();
  if (workspaceState.scope === "agent") {
    renderAgentTitleEditor("workbench");
    return;
  }
  if (state.titleEditSurface === "workbench") {
    state.titleEditing = false;
    state.titleSaving = false;
    state.titleDraft = "";
  }
  const { display, input, edit, save, cancel } = titleEditorElements("workbench");
  const project = workspaceState.workspace.projects.find((item) => item.id === workspaceState.projectId);
  const title = workspaceState.scope === "project" && project ? project.name : t("taskWorkspace.dispatchTitle");
  if (display) {
    display.textContent = title;
    display.disabled = true;
    display.title = title;
    display.setAttribute("aria-label", title);
    display.classList.remove("hidden");
  }
  input?.classList.add("hidden");
  edit?.classList.add("hidden");
  save?.classList.add("hidden");
  cancel?.classList.add("hidden");
}

// The header renders are asked for from four independent places while a
// switch is in flight. See coalescePerFrame.
//
// Declared as hoisted functions rather than as the coalesced consts directly:
// these names are handed to the helper factories during module evaluation,
// well above this line, and a const would still be in its temporal dead zone
// at that point. The wrappers are only ever called after evaluation finishes.
const coalescedConversationHeaderIdentity = coalescePerFrame(renderConversationHeaderIdentityNow);
const coalescedWorkbenchHeaderIdentity = coalescePerFrame(renderWorkbenchHeaderIdentityNow);
function renderConversationHeaderIdentity() { return coalescedConversationHeaderIdentity(); }
function renderWorkbenchHeaderIdentity() { return coalescedWorkbenchHeaderIdentity(); }

function beginConversationTitleEdit(surface = "conversation") {
  if (!state.agent?.id || state.titleSaving) return;
  state.titleEditSurface = normalizedTitleEditSurface(surface);
  state.titleDraft = String(state.agent.title || state.project?.name || "");
  state.titleEditing = true;
  renderAllTitleEditors();
  queueMicrotask(() => {
    const input = titleEditorElements(state.titleEditSurface).input;
    input?.focus();
    input?.select();
  });
}

async function saveConversationTitle(surface = state.titleEditSurface) {
  const agentId = state.agent?.id || "";
  if (!agentId || state.titleSaving) return;
  const target = normalizedTitleEditSurface(surface);
  const workbench = target === "workbench";
  const input = titleEditorElements(target).input;
  const title = String(input ? input.value : state.titleDraft || "").trim();
  if (!title) throw new Error(am(workbench ? "workbenchTitleRequired" : "conversationTitleRequired"));
  const byteLength = globalThis.TextEncoder ? new TextEncoder().encode(title).length : title.length;
  if (byteLength > 200 || /[\0\r\n]/.test(title)) throw new Error(am(workbench ? "workbenchTitleInvalid" : "conversationTitleInvalid"));
  if (title === String(state.agent?.title || "").trim()) {
    cancelConversationTitleEdit();
    return;
  }
  const generation = Number(state.agent?.entityGeneration);
  state.titleEditSurface = target;
  state.titleDraft = title;
  state.titleSaving = true;
  renderAllTitleEditors();
  try {
    const updated = await api(`/api/agents/${encodeURIComponent(agentId)}/title`, {
      method: "PATCH",
      body: JSON.stringify({ title, ...(Number.isInteger(generation) ? { entityGeneration: generation } : {}) }),
    });
    if (state.agent?.id !== agentId) return;
    state.agent = updated;
    state.worklineAgents = (state.worklineAgents || []).map((agent) => agent.id === agentId ? updated : agent);
    state.titleEditing = false;
    state.titleDraft = "";
    syncNavigationConversationFromAgent(updated, { reason: "agent-title" });
    navigationRefresh.request("agent-title");
    renderConversationHeaderIdentity();
    renderWorkbenchShell();
    rememberCurrentConversation();
    showToast(am(workbench ? "workbenchTitleSaved" : "conversationTitleSaved"), "success");
    notifyTerminal(`[info] ${am(workbench ? "workbenchTitleSavedTerminal" : "conversationTitleSavedTerminal", { title })}\n`);
  } finally {
    if (state.agent?.id === agentId) {
      state.titleSaving = false;
      renderAllTitleEditors();
    }
  }
}

function renderRecentSidebarConversations() {
  const el = $("recentSidebarConversations");
  if (!el) return;
  el.innerHTML = renderRecentConversationsHTML(state.recentConversations, state.navigationConversations, state.agent?.id || "");
  el.querySelectorAll("[data-navigation-target]").forEach((node) => {
    node.addEventListener("click", () => selectNavigationConversation(node.dataset.navigationTarget).catch(showError));
  });
}

async function loadProjects() {
  const seq = ++state.navigationLoadSeq;
  try {
    const payload = await api("/api/navigation");
    if (seq !== state.navigationLoadSeq) return;
    const navigation = normalizeNavigationPayload(payload);
    state.projects = navigation.projects;
    state.navigationConversations = navigation.conversations;
    renderProjects();
    return navigation;
  } catch (err) {
    if (seq === state.navigationLoadSeq) throw err;
  }
}

async function openDefaultConversationTarget(options = {}) {
  const projectId = resolveTopNavigationProjectId({
    projects: state.projects,
    conversations: state.navigationConversations,
  }, {
    projectOrder: getProjectOrder(),
  });
  if (projectId) {
    state.navigationMode = "projects";
    await selectProject(projectId, options);
    return { kind: "project", id: projectId };
  }

  state.chatHydrating = false;
  renderProjects();
  showEmptyWorkspaceState();
  return null;
}

// Forks a project into a new git branch + worktree and opens its conversation.
// The branch name is left to the server, which derives it from the title and
// appends a short id: naming it here would need a dialog this shell does not
// have, and the conversation title stays renameable afterward while the branch
// does not.
async function createProjectWorkline(projectId, trigger = null, options = {}) {
  const id = String(projectId || "").trim();
  if (!id || state.worklineForking) return null;
  const requestBody = {};
  const title = String(options?.title || "").trim();
  const model = String(options?.model || "").trim();
  const permissionMode = String(options?.permissionMode || "").trim();
  if (title) requestBody.title = title;
  if (model) requestBody.model = model;
  if (permissionMode) requestBody.permissionMode = permissionMode;
  state.worklineForking = true;
  if (trigger) {
    trigger.disabled = true;
    trigger.setAttribute("aria-busy", "true");
  }
  showToast(t("shell.newWorklineCreating"), "info");
  let gitSetupAttempted = false;
  try {
    const worklines = await api(`/api/projects/${encodeURIComponent(id)}/worklines`);
    const list = Array.isArray(worklines) ? worklines : [];
    // Forking the root keeps every branch a sibling off the mainline rather
    // than chaining forks onto whichever workline happens to be first.
    const parent = list.find((item) => item?.isRoot) || list[0];
    if (!parent?.id) throw new Error(t("shell.newWorklineFailed"));
    const fork = () => api(`/api/worklines/${encodeURIComponent(parent.id)}/fork`, {
      method: "POST",
      body: JSON.stringify(requestBody),
    });

    let created;
    try {
      created = await fork();
    } catch (error) {
      const setupCode = String(error?.body?.code || "");
      if (!new Set(["no_git_repo", "git_no_commits"]).has(setupCode)) throw error;
      const path = error?.body?.path || id;
      const confirmKey = setupCode === "git_no_commits" ? "shell.gitInitialCommitConfirm" : "shell.gitInitConfirm";
      if (!await platformConfirm(t(confirmKey, { path }))) return null;
      gitSetupAttempted = true;
      await api(`/api/projects/${encodeURIComponent(id)}/init-git`, { method: "POST" });
      showToast(t("shell.gitInitSuccess"), "success", { force: true });
      // Keep this retry inside the same busy lifecycle. Recursive re-entry used
      // to let the outer finally clear the busy flag while the retry was active.
      created = await fork();
    }

    const agentId = String(created?.agent?.id || "").trim();
    await loadProjects();
    if (agentId) {
      const conversation = state.navigationConversations.find((item) => item.agentId === agentId) || {
        agentId,
        agentTitle: created?.agent?.title || title || agentId,
        projectId: id,
        worklineId: created?.workline?.id || "",
        context: "project",
      };
      await selectNavigationConversation(conversation);
    }
    showToast(t("shell.newWorklineSuccess", { branch: created?.workline?.branch || "" }), "success", { force: true });
    return created;
  } catch (error) {
    showToast(error?.message || t(gitSetupAttempted ? "shell.gitInitFailed" : "shell.newWorklineFailed"), "error", { force: true });
    return null;
  } finally {
    state.worklineForking = false;
    if (trigger) {
      trigger.disabled = false;
      trigger.removeAttribute("aria-busy");
    }
  }
}

async function launchOverviewPrompt({ text, projectId = "", model = "", reasoningEffort = "auto" } = {}) {
  const prompt = String(text || "").trim();
  if (!prompt) throw new Error(t("overview.promptRequired"));
  const selectedProjectId = String(projectId || state.project?.id || resolveTopNavigationProjectId({
    projects: state.projects,
    conversations: state.navigationConversations,
  }, { projectOrder: getProjectOrder() }) || "").trim();
  if (!selectedProjectId) {
    await openDirectoryChooser(state.project?.gitPath || state.agent?.cwd || "");
    throw new Error(t("overview.projectRequired"));
  }
  if (state.project?.id !== selectedProjectId || !state.agent?.id) {
    await selectProject(selectedProjectId, { preserveMessageState: true });
  }
  if (!state.agent?.id || state.project?.id !== selectedProjectId) throw new Error(t("shell.newWorklineFailed"));

  await saveReasoningEffort(reasoningEffort || "auto");
  setMessageInputValue(prompt, { saveDraft: false });
  await sendMessage({ preventDefault() {} });
  return true;
}

function startScheduleCreation() {
  if (state.activeWorkbench !== "schedules") switchPrimaryWorkbench("schedules");
  closeMobileSidebar();
  scheduleWorkspace.startCreate();
  renderScheduleSurface();
  renderProjects();
  deferOverviewDOM(() => $("scheduleWorkspaceBody")?.querySelector?.("[data-schedule-form] input")?.focus?.());
  return true;
}

async function createNavigationItem(trigger = null) {
  const target = currentNavigationCreateTarget();
  if (target === "schedule") return startScheduleCreation();
  closeMobileSidebar();
  await openDirectoryChooser(state.project?.gitPath || state.agent?.cwd || "", { trigger });
  return null;
}

function syncNavigationConversationFromAgent(agent, options = {}) {
  const agentId = String(agent?.id || "").trim();
  if (!agentId) return false;
  const index = state.navigationConversations.findIndex((item) => item.agentId === agentId);
  if (index < 0) {
    navigationRefresh.request(options.reason || "agent-discovered");
    return false;
  }
  const current = state.navigationConversations[index];
  const messageCount = Number(agent?.messageCount);
  const updated = {
    ...current,
    agentTitle: String(agent?.title || current.agentTitle),
    agentType: String(agent?.type || current.agentType),
    agentStatus: String(options.status || agent?.status || current.agentStatus || "idle"),
    model: String(agent?.model || current.model),
    permissionMode: String(agent?.permissionMode || current.permissionMode),
    cwd: String(agent?.cwd || current.cwd),
    messageCount: Number.isFinite(messageCount) ? Math.max(0, Math.trunc(messageCount)) : current.messageCount,
    lastActivityAt: String(agent?.lastMessageAt || agent?.updatedAt || current.lastActivityAt),
  };
  state.navigationConversations = [
    ...state.navigationConversations.slice(0, index),
    updated,
    ...state.navigationConversations.slice(index + 1),
  ];
  renderProjects();
  return true;
}

// Project-level drag — reorders project groups in the sidebar.
function bindProjectDrag(el) {
  if (!el || el.dataset?.projDragBound === "true") return;
  if (el.dataset) el.dataset.projDragBound = "true";

  let dragProjectId = "";

  // The sidebar renders projects two ways: grouped mode wraps each project in a
  // <section data-navigation-project-group> alongside its conversations, while
  // projects-only mode (the task sidebar) renders a flat list of bare project
  // rows. Both have to be draggable, so every handler resolves a project
  // through here instead of assuming a group ancestor exists.
  function projectDragTarget(node) {
    const group = node?.closest?.("[data-navigation-project-group]");
    if (group) return { id: group.dataset.navigationProjectGroup || "", element: group };
    const row = node?.closest?.("[data-project-id]");
    if (row && row.dataset.navigationKind === "project") {
      return { id: row.dataset.projectId || row.dataset.navigationId || "", element: row };
    }
    return { id: "", element: null };
  }

  el.addEventListener("dragstart", (event) => {
    // A conversation row inside a group owns its own drag; ignore those here.
    if (event.target?.closest?.("[data-navigation-target]")) return;
    const { id, element } = projectDragTarget(event.target);
    if (!id) return;
    dragProjectId = id;
    event.dataTransfer.setData("text/plain", dragProjectId);
    event.dataTransfer.effectAllowed = "move";
    element?.classList.add("proj-dragging");
  });

  el.addEventListener("dragover", (event) => {
    if (!dragProjectId) return;
    // Anywhere inside a project group is a valid drop target, so hovering a
    // conversation row still reorders the group it belongs to. Restricting
    // this to the project row alone left most of the group's height inert.
    const { id: targetId, element } = projectDragTarget(event.target);
    if (!targetId || targetId === dragProjectId) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    el.querySelectorAll(".proj-drag-over").forEach((n) => n.classList.remove("proj-drag-over"));
    element?.classList.add("proj-drag-over");
  });

  el.addEventListener("dragleave", (event) => {
    if (!dragProjectId) return;
    if (!el.contains(event.relatedTarget)) {
      el.querySelectorAll(".proj-drag-over").forEach((n) => n.classList.remove("proj-drag-over"));
    }
  });

  el.addEventListener("drop", (event) => {
    if (!dragProjectId) return;
    const { id: targetId } = projectDragTarget(event.target);
    if (!targetId || targetId === dragProjectId) return;
    event.preventDefault();

    // Read the on-screen order from whichever structure is rendered. Querying
    // only for group sections returned nothing in projects-only mode, so the
    // reorder silently did nothing there.
    const groups = Array.from(el.querySelectorAll("[data-navigation-project-group]"));
    const ids = groups.length
      ? groups.map((node) => node.dataset.navigationProjectGroup || "")
      : Array.from(el.querySelectorAll('[data-project-id][data-navigation-kind="project"]'))
        .map((node) => node.dataset.projectId || node.dataset.navigationId || "");
    const fromIdx = ids.indexOf(dragProjectId);
    const toIdx = ids.indexOf(targetId);
    if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;
    ids.splice(fromIdx, 1);
    ids.splice(toIdx, 0, dragProjectId);
    saveProjectOrder(ids);
    renderProjects();
  });

  el.addEventListener("dragend", () => {
    el.querySelectorAll(".proj-dragging, .proj-drag-over").forEach((n) => {
      n.classList.remove("proj-dragging", "proj-drag-over");
    });
    dragProjectId = "";
  });
}

function bindConversationDrag(el) {
  if (!el || el.dataset?.convDragBound === "true") return;
  if (el.dataset) el.dataset.convDragBound = "true";

  let dragAgentId = "";
  let dragOrderScope = "";

  function conversationOrderScope(row) {
    const explicit = String(row?.dataset?.conversationOrderScope || "").trim();
    if (explicit) return explicit;
    return String(row?.closest?.("[data-navigation-project-group]")?.dataset?.navigationProjectGroup || "").trim();
  }

  function conversationRows(scope) {
    if (!scope) return [];
    const escaped = CSS.escape(scope);
    const scoped = Array.from(el.querySelectorAll(`[data-navigation-target][data-conversation-order-scope="${escaped}"]`));
    if (scoped.length) return scoped;
    return Array.from(el.querySelectorAll(`[data-project-conversations="${escaped}"] [data-navigation-target]`));
  }

  el.addEventListener("dragstart", (event) => {
    const row = event.target?.closest?.("[data-navigation-target][draggable]");
    if (!row) return;
    const orderScope = conversationOrderScope(row);
    if (!orderScope) return;
    dragAgentId = row.dataset.navigationId || "";
    dragOrderScope = orderScope;
    event.dataTransfer.setData("text/plain", dragAgentId);
    event.dataTransfer.effectAllowed = "move";
    row.classList.add("conv-dragging");
  });

  el.addEventListener("dragover", (event) => {
    // Only claim rows in the same project ordering scope. Project
    // conversations stay inside their project.
    if (!dragAgentId || !dragOrderScope) return;
    const row = event.target?.closest?.("[data-navigation-target]");
    if (!row || row.dataset.navigationId === dragAgentId || conversationOrderScope(row) !== dragOrderScope) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    el.querySelectorAll(".conv-drag-over").forEach((n) => n.classList.remove("conv-drag-over"));
    row.classList.add("conv-drag-over");
  });

  el.addEventListener("dragleave", (event) => {
    if (!el.contains(event.relatedTarget)) {
      el.querySelectorAll(".conv-drag-over").forEach((n) => n.classList.remove("conv-drag-over"));
    }
  });

  el.addEventListener("drop", (event) => {
    // Bail out before preventDefault when this is not a conversation drag so a
    // project drag can continue to the project-level handler registered later.
    if (!dragAgentId || !dragOrderScope) return;
    const targetRow = event.target?.closest?.("[data-navigation-target]");
    if (!targetRow) return;
    const targetAgentId = targetRow.dataset.navigationId || "";
    if (targetAgentId === dragAgentId || conversationOrderScope(targetRow) !== dragOrderScope) return;
    event.preventDefault();

    const ids = conversationRows(dragOrderScope).map((row) => row.dataset.navigationId || "").filter(Boolean);
    const fromIdx = ids.indexOf(dragAgentId);
    const toIdx = ids.indexOf(targetAgentId);
    if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;
    ids.splice(fromIdx, 1);
    ids.splice(toIdx, 0, dragAgentId);
    saveConversationOrder(dragOrderScope, ids);
    renderProjects();
  });

  el.addEventListener("dragend", () => {
    el.querySelectorAll(".conv-dragging, .conv-drag-over").forEach((n) => {
      n.classList.remove("conv-dragging", "conv-drag-over");
    });
    dragAgentId = "";
    dragOrderScope = "";
  });
}

// Settings-nav drag — items can be reordered within a section, never across.
function bindSettingsNavDrag(nav) {
  if (!nav || nav.dataset?.settingsNavDragBound === "true") return;
  if (nav.dataset) nav.dataset.settingsNavDragBound = "true";

  let dragKey = "";
  let dragSection = null;

  nav.addEventListener("dragstart", (event) => {
    const btn = event.target?.closest?.("[data-settings-key][draggable]");
    if (!btn) return;
    dragKey = btn.dataset.settingsKey || "";
    dragSection = btn.closest("[data-settings-nav-section]");
    event.dataTransfer.setData("text/plain", dragKey);
    event.dataTransfer.effectAllowed = "move";
    btn.classList.add("settings-nav-dragging");
  });

  nav.addEventListener("dragover", (event) => {
    const btn = event.target?.closest?.("[data-settings-key]");
    if (!btn || btn.dataset.settingsKey === dragKey) return;
    const sec = btn.closest("[data-settings-nav-section]");
    // Block cross-section moves
    if (!dragSection || sec !== dragSection) return;
    event.preventDefault();
    event.dataTransfer.dropEffect = "move";
    nav.querySelectorAll(".settings-nav-drag-over").forEach((n) => n.classList.remove("settings-nav-drag-over"));
    btn.classList.add("settings-nav-drag-over");
  });

  nav.addEventListener("dragleave", (event) => {
    if (!nav.contains(event.relatedTarget)) {
      nav.querySelectorAll(".settings-nav-drag-over").forEach((n) => n.classList.remove("settings-nav-drag-over"));
    }
  });

  nav.addEventListener("drop", (event) => {
    event.preventDefault();
    const targetBtn = event.target?.closest?.("[data-settings-key]");
    if (!targetBtn || !dragKey) return;
    const targetKey = targetBtn.dataset.settingsKey || "";
    const targetSection = targetBtn.closest("[data-settings-nav-section]");
    if (targetKey === dragKey || !dragSection || targetSection !== dragSection) return;

    const sectionId = dragSection.dataset.settingsNavSection || "";
    const allBtns = Array.from(dragSection.querySelectorAll("[data-settings-key]"));
    const keys = allBtns.map((b) => b.dataset.settingsKey || "");
    const fromIdx = keys.indexOf(dragKey);
    const toIdx = keys.indexOf(targetKey);
    if (fromIdx < 0 || toIdx < 0 || fromIdx === toIdx) return;
    keys.splice(fromIdx, 1);
    keys.splice(toIdx, 0, dragKey);
    saveSettingsNavOrder(sectionId, keys);
    renderSettingsNav(state.activeSettingsPanel || "");
  });

  nav.addEventListener("dragend", () => {
    nav.querySelectorAll(".settings-nav-dragging, .settings-nav-drag-over").forEach((n) => {
      n.classList.remove("settings-nav-dragging", "settings-nav-drag-over");
    });
    dragKey = "";
    dragSection = null;
  });
}

function renderProjects() {
  const el = $("projects");
  if (!el) return;
  const scheduleContext = state.activeWorkbench === "schedules";
  const taskContext = state.activeWorkbench === "workbench";
  // Every sidebar width shows the tree. The compact/expanded split used to
  // decide this, which meant collapsing the sidebar revealed more structure than
  // widening it: forks render only in "all", so at normal width a forked
  // conversation had nowhere to appear at all. The task sidebar stays flat --
  // there the project is the unit of work, not the conversation under it.
  const effectiveNavigationMode = taskContext ? "projects" : "all";
  state.navigationMode = "projects";
  renderPrimaryModeSidebar();
  if (scheduleContext) {
    el.innerHTML = scheduleWorkspace.renderNavigation(scheduleWorkspaceViewOptions());
    scheduleWorkspace.bind(el, scheduleWorkspaceViewOptions());
    renderRecentSidebarConversations();
    renderRecentSidebarDirectories();
    return;
  }
  const view = buildNavigationView({ projects: state.projects, conversations: state.navigationConversations }, {
    mode: effectiveNavigationMode,
    query: state.projectQuery,
  });
  const taskCounts = Object.fromEntries(taskWorkspace.getState().workspace.projects.map((project) => [project.id, project.counts]));
  el.innerHTML = renderNavigationHTML(view, {
    activeProjectId: state.project?.id || "",
    activeAgentId: state.agent?.id || "",
    activeSelectionKind: state.navigationSelectionKind,
    taskContext,
    taskCounts,
    conversationOrders: getConversationOrders(),
    collapsedNodes: getCollapsedNavNodes(),
    projectOrder: getProjectOrder(),
  });
  bindConversationDrag(el);
  bindProjectDrag(el);
  el.querySelectorAll("[data-project-id]").forEach((node) => {
    bindNavigationActivation(node, () => selectProject(node.dataset.projectId).then(() => {
      if (state.activeWorkbench === "workbench") {
        taskWorkspace.setContext({ projectId: node.dataset.projectId, agentId: state.agent?.id || "", scope: "project" });
      }
    }).catch(showError));
  });
  el.querySelectorAll("[data-navigation-target]").forEach((node) => {
    bindNavigationActivation(node, () => selectNavigationConversation(node.dataset.navigationTarget).catch(showError));
  });
  bindNavigationMenuTriggers();
  el.querySelectorAll("[data-navigation-disclosure]").forEach((node) => {
    node.addEventListener("click", (event) => {
      // The row behind the triangle is itself a button, so without this the
      // toggle would also navigate to whatever it sits on.
      event.preventDefault();
      event.stopPropagation();
      toggleCollapsedNavNode(node.dataset.navigationDisclosure);
      renderProjects();
    });
  });
  el.querySelectorAll("[data-project-fork-trigger]").forEach((node) => {
    node.addEventListener("click", (event) => {
      // The row itself is a button; without this the fork would also switch
      // the selection out from under the request.
      event.preventDefault();
      event.stopPropagation();
      createProjectWorkline(node.dataset.projectIdFork, node);
    });
  });
  el.querySelectorAll("[data-primary-workbench-target]").forEach((node) => {
    node.addEventListener("click", () => switchPrimaryWorkbench(node.dataset.primaryWorkbenchTarget));
  });
  renderRecentSidebarConversations();
  renderRecentSidebarDirectories();
}

async function createProjectFromDirectory(path, options = {}) {
  saveCurrentChatDraft();
  hideSlashCommandPalette();
  if (state.projectCreating) return;
  const normalizedPath = canonicalLocalPath(path);
  if (!normalizedPath) throw new Error(t("workspace.main.selectDirectory"));
  const modalOpen = elementVisible("folderModal");
  const button = options.button || (modalOpen ? $("chooseDirectoryBtn") : null);
  const projects = Array.isArray(state.projects) ? state.projects : [];
  const existing = projects.find((project) => normalizePath(project.gitPath) === normalizePath(normalizedPath));
  const seq = ++state.projectCreateSeq;
  state.projectCreating = true;
  const busyText = t("workspace.main.creating");
  const idempotencyKey = options.idempotencyKey || globalThis.crypto?.randomUUID?.() || `project-conversation-${Date.now()}-${seq}`;
  setButtonBusy(button, true, busyText);
  setDirectoryStatus(`${busyText}：${normalizedPath}`, "busy");
  showToast(`${busyText}：${shortPath(normalizedPath)}`, "info", { force: true });
  try {
    rememberDirectory(normalizedPath);
    const name = basename(normalizedPath) || "Project";
    const model = currentModelValue();
    const created = existing
      ? await api(`/api/projects/${encodeURIComponent(existing.id)}/conversations`, {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ title: name, ...(model ? { model } : {}), idempotencyKey }),
      })
      : await api("/api/projects", {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: JSON.stringify({ name, gitPath: normalizedPath, forceNewConversation: true, idempotencyKey, ...(model ? { model } : {}) }),
      });
    if (seq !== state.projectCreateSeq) return;
    if (modalOpen) closeDirectoryModal();
    await loadProjects();
    if (seq !== state.projectCreateSeq) return;
    if (created.project?.id && !state.projects.some((project) => project.id === created.project.id)) {
      state.projects = [created.project, ...state.projects];
    }
    state.navigationSelectionKind = "project";
    state.project = created.project;
    state.workline = created.workline;
    state.agent = created.agent;
    state.projectWorklines = created.workline ? [created.workline] : [];
    state.worklineAgents = created.agent ? [created.agent] : [];
    renderProjects();
    await enterAgent();
    showToast(t("workspace.main.selectedDirectory", { path: shortPath(created.project?.gitPath || normalizedPath) }), "success", { force: true });
    appendTerminal(`Created project: ${created.project.name}\nPath: ${created.project.gitPath}\n`);
  } catch (err) {
    if (seq === state.projectCreateSeq) {
      const message = err.message || String(err);
      setDirectoryStatus(am("openingFailed", { message }), "error");
      throw err;
    }
  } finally {
    state.projectCreating = false;
    setButtonBusy(button, false, busyText);
  }
}

function beginNavigationSelection(project, options = {}) {
  const startupSelection = options.source === "startup";
  if (startupSelection) {
    if (!startupTokenCurrent(options.startupToken)) return null;
  } else {
    // This must remain the first operation: a click during any pending startup
    // request immediately owns navigation, before even draft/UI cleanup runs.
    navigationStartupGuard.beginUserNavigation();
    state.startupWorkbenchIntent = "";
  }
  saveCurrentChatDraft();
  hideSlashCommandPalette();
  closeMobileSidebar();
  state.projectCreateSeq++;
  const seq = ++state.projectSelectSeq;
  const previousTitle = conversationHeaderTitle();
  state.navigationSelectionKind = "project";
  state.navigationTransitionTitle = options.preserveConversationView ? previousTitle : "";
  disconnectAgentTransports();
  state.project = project || null;
  state.workline = null;
  state.agent = null;
  contextManagement.reset(null);
  syncProjectOperationContext();
  state.workState = null;
  state.titleEditing = false;
  state.titleSaving = false;
  state.titleDraft = "";
  if (!options.preserveConversationView) renderConversationHeaderIdentity();
  state.chatHydrating = true;
  clearLiveAssistantText({ preserveView: true });
  setWorkspaceExplorerAgent(null);
  projectKanban.setAgent(null);
  taskWorkspace.setContext({ projectId: project?.id || "", agentId: "" });
  renderWorkbenchShell();
  syncMessageComposerBusy();
  refreshReasoningEffortControl();
  refreshFastModeControl();
  state.currentMessages = [];
  state.messageCopyTexts = [];
  state.messageHasMoreBefore = false;
  state.messageNextBefore = "";
  state.messageOlderLoading = false;
  resetGitWorkflowState();
  updateConversationCopyButton();
  setMessageInputValue("", { saveDraft: false });
  state.projectWorklines = [];
  state.worklineAgents = [];
  renderProjects();
  return seq;
}

async function selectProject(id, options = {}) {
  const leavingOverview = state.overviewActive && options.preserveOverview !== true;
  if (leavingOverview) switchPrimaryWorkbench("conversation");
  const project = state.projects.find((item) => item.id === id) || null;
  // Clicking the project that is already open, while already looking at it,
  // asks for nothing that is not on screen. It used to re-fetch the worklines
  // and the agents and re-enter the agent every time, which is why holding the
  // mouse on a project row made the workspace load over and over.
  //
  // Deliberately narrow. Coming back from the overview has real work to do, and
  // so does clicking the project row while a conversation inside it is open --
  // that moves the selection back to project scope. Only the exact no-op is
  // skipped.
  if (
    project
    && !leavingOverview
    && state.project?.id === id
    && state.agent?.id
    && state.navigationSelectionKind === "project"
    && !options.preserveMessageState
  ) {
    return;
  }
  // Preserve whenever there is a conversation on screen, including when this is
  // the project already open. The `!== id` clause that used to be here made
  // re-selecting the current project the one case that did NOT preserve, so
  // clicking an already-open project tore the workspace down to the "loading
  // project" placeholder and rebuilt it -- a full teardown to load something
  // that was already loaded. selectNavigationConversation has always used the
  // plain form.
  const preserveConversationView = Boolean(state.project?.id && state.agent?.id);
  // Re-selecting the project that is already open loads nothing that is not
  // already on screen, so neither the placeholder nor the busy overlay has
  // anything to cover for. This has to be read before beginNavigationSelection,
  // which is what moves state.project onto the new selection.
  const reselectingCurrentProject = Boolean(state.project?.id) && state.project.id === id;
  const seq = beginNavigationSelection(project, {
    preserveConversationView,
    selectionKind: "project",
    source: options.source,
    startupToken: options.startupToken,
  });
  if (seq == null) return;
  if (!state.project) {
    state.chatHydrating = false;
    updateWorkspaceMetaPills();
    showEmptyWorkspaceState();
    return;
  }
  if (!preserveConversationView) {
    $("currentTitle").textContent = state.project.name;
    updateWorkspaceMetaPills();
    if (!options.preserveMessageState && !reselectingCurrentProject) {
      showEmptyWorkspaceState({
        title: am("projectLoadingTitle"),
        text: am("projectLoadingDescription"),
        action: am("chooseAnotherFolder"),
        hint: state.project.gitPath || "",
        icon: "…",
        busy: true,
      });
    }
  } else if (!reselectingCurrentProject) {
    markMessageViewportBusy({ contextSwitch: true, label: am("projectLoadingTitle") });
  }
  try {
    const worklines = await api(`/api/projects/${id}/worklines`);
    if (!startupSelectionCurrent(options) || seq !== state.projectSelectSeq || state.project?.id !== id) return;
    state.projectWorklines = Array.isArray(worklines) ? worklines : [];
    state.workline = state.projectWorklines[0] || null;
    if (!state.workline) {
      state.chatHydrating = false;
      state.navigationTransitionTitle = "";
      $("currentTitle").textContent = state.project.name;
      updateWorkspaceMetaPills();
      clearMessageViewportBusy();
      showEmptyWorkspaceState({ title: am("noWorklines"), text: am("noWorklinesDescription"), action: am("chooseAnotherFolder"), icon: "◇" });
      return;
    }
    const worklineId = state.workline.id;
    const agents = await api(`/api/worklines/${worklineId}/agents`);
    if (!startupSelectionCurrent(options) || seq !== state.projectSelectSeq || state.project?.id !== id || state.workline?.id !== worklineId) return;
    state.worklineAgents = Array.isArray(agents) ? agents : [];
    state.agent = state.worklineAgents.find((agent) => agent.type === "primary") || state.worklineAgents[0] || null;
    if (!state.agent) {
      state.chatHydrating = false;
      state.navigationTransitionTitle = "";
      $("currentTitle").textContent = state.project.name;
      updateWorkspaceMetaPills();
      clearMessageViewportBusy();
      showEmptyWorkspaceState({ title: am("noAgents"), text: am("noAgentsDescription"), action: am("chooseAnotherFolder"), icon: "♧" });
      return;
    }
    await enterAgent();
    if (!startupSelectionCurrent(options) || seq !== state.projectSelectSeq) return;
    clearMessageViewportBusy();
  } catch (err) {
    if (startupSelectionCurrent(options) && seq === state.projectSelectSeq && state.project?.id === id) {
      state.chatHydrating = false;
      clearMessageViewportBusy();
      throw err;
    }
  }
}

async function selectNavigationConversation(target, options = {}) {
  if (state.overviewActive && options.preserveOverview !== true) switchPrimaryWorkbench("conversation");
  const supplied = target && typeof target === "object" ? target : null;
  const parsed = typeof target === "string" ? parseNavigationTargetId(target) : parseNavigationTargetId(target?.targetId || "");
  const navigationConversation = supplied?.agentId
    ? supplied
    : parsed ? state.navigationConversations.find((item) => item.targetId === parsed.targetId) || null : null;
  const agentId = String(navigationConversation?.agentId || parsed?.agentId || "").trim();
  const projectId = String(navigationConversation?.projectId || parsed?.projectId || "").trim();
  const worklineId = String(navigationConversation?.worklineId || parsed?.worklineId || "").trim();
  if (!agentId || !projectId || !worklineId || navigationConversation?.context === "conversation" || navigationConversation?.projectFlowMode === false) {
    throw new Error(am("invalidConversationTarget"));
  }
  const project = state.projects.find((item) => item.id === projectId) || (navigationConversation ? {
    id: navigationConversation.projectId,
    name: navigationConversation.projectName,
    gitPath: navigationConversation.projectPath,
    updatedAt: navigationConversation.projectUpdatedAt,
    flowMode: "workspace",
  } : null);
  const preserveConversationView = Boolean(state.agent?.id);
  const seq = beginNavigationSelection(project, {
    preserveConversationView,
    selectionKind: "project",
    source: options.source,
    startupToken: options.startupToken,
  });
  if (seq == null) return;
  if (!state.project) {
    state.chatHydrating = false;
    showEmptyWorkspaceState();
    throw new Error(am("projectNoLongerExists"));
  }

  if (!preserveConversationView) $("currentTitle").textContent = navigationConversation?.projectName || state.project.name;
  updateWorkspaceMetaPills();
  // Keep the previous title and conversation in place while the next one hydrates.
  // Replacing either with an intermediate project/loading state causes a distracting flash.
  markMessageViewportBusy();

  try {
    const [worklines, agents] = await Promise.all([
      api(`/api/projects/${encodeURIComponent(projectId)}/worklines`),
      api(`/api/worklines/${encodeURIComponent(worklineId)}/agents`),
    ]);
    if (!startupSelectionCurrent(options) || seq !== state.projectSelectSeq || state.project?.id !== projectId) return;
    state.projectWorklines = Array.isArray(worklines) ? worklines : [];
    state.workline = state.projectWorklines.find((item) => item.id === worklineId) || null;
    state.worklineAgents = Array.isArray(agents) ? agents : [];
    state.agent = state.worklineAgents.find((item) => item.id === agentId) || null;
    if (!state.agent || !state.workline) {
      state.chatHydrating = false;
      clearMessageViewportBusy();
      $("currentTitle").textContent = state.project.name;
      updateWorkspaceMetaPills();
      showEmptyWorkspaceState({
        title: am("conversationUnavailable"),
        text: am("conversationUnavailableDescription"),
        action: am("chooseAnotherFolder"),
        icon: "◇",
      });
      throw new Error(am("worklineOrAgentMissing"));
    }
    await enterAgent();
    if (!startupSelectionCurrent(options) || seq !== state.projectSelectSeq) return;
    clearMessageViewportBusy();
    renderProjects();
  } catch (err) {
    const stillSelected = startupSelectionCurrent(options)
      && seq === state.projectSelectSeq
      && state.project?.id === projectId;
    if (stillSelected) {
      state.chatHydrating = false;
      clearMessageViewportBusy();
      throw err;
    }
  }
}

async function enterAgent() {
  if (!state.agent) return;
  syncThemePageContext();
  closeConversationDetails();
  const agentId = state.agent.id;
  contextManagement.setAgent(state.agent).catch(showError);
  state.navigationTransitionTitle = "";
  state.chatHydrating = true;
  const projectContext = syncProjectOperationContext();
  backgroundTasks.setAgent(agentId);
  setWorkspaceExplorerAgent(projectContext ? state.agent : null);
  projectKanban.setAgent(state.agent);
  taskWorkspace.setContext({ projectId: state.project?.id || "", agentId });
  renderWorkbenchShell();
  if (state.activeWorkbench === "workbench" && taskWorkspace.getState().scope === "agent") specBoard.load().catch(showError);
  renderConversationHeaderIdentity();
  $("permissionMode").value = state.agent.permissionMode || "acceptEdits";
  enforcePermissionSelectCap();
  updateWorkspaceMetaPills();
  renderModelOptions();
  refreshReasoningEffortControl();
  refreshFastModeControl();
  refreshMessageModeControl();
  await restoreCurrentChatDraft();
  renderMessageQueue();
  syncMessageComposerBusy();
  refreshComposerActivityStatus();
  clearLiveReasoning();
  clearRunSummary({ preserveView: true });
  clearPlanState(agentId);
  if (projectContext) {
    connectTerminal();
    loadGitStatus({ silent: true }).then(renderWorkbenchShell).catch(() => {});
  } else {
    resetGitWorkflowState();
  }
  let effectiveSkillsError = null;
  const effectiveSkillsPromise = refreshEffectiveSkillsPolicy().catch((error) => {
    effectiveSkillsError = error;
  });
  let messagesLoaded = false;
  try {
    [messagesLoaded] = await Promise.all([
      loadMessages(agentId),
      loadLatestRunSummary(agentId),
      subagentCards.loadBackgroundTasksForAgent(agentId),
    ]);
    if (state.agent?.id !== agentId) return;
    state.chatHydrating = false;
    if (messagesLoaded) applyMessageSnapshot(state.currentMessages, agentId, { forceRender: true });
  } finally {
    if (state.agent?.id === agentId) {
      state.chatHydrating = false;
      connectWS();
    }
  }
  // The transcript and navigation are ready at this point. Do not hold the
  // first visible page behind the optional skills-policy request: over a remote
  // tunnel it can be noticeably slower than the chat data it does not affect.
  signalAppReady();
  await effectiveSkillsPromise;
  if (state.agent?.id !== agentId) return;
  if (effectiveSkillsError) throw effectiveSkillsError;
  rememberCurrentConversation();
  renderProjects();
}

function showModelSetupNotice() {
  const el = $("messages");
  el.classList.remove("empty");
  el.innerHTML = `
    <div class="setup-notice-card">
      <div class="setup-notice-title">${escapeHtml(t("workspace.main.modelKeyMissing"))}</div>
      <p>${escapeHtml(modelSetupMessage())}</p>
      <div class="setup-notice-actions">
        <button class="ghost-btn mini" type="button" id="openModelSettingsNoticeBtn">${escapeHtml(t("workspace.main.openModelSettings"))}</button>
        <button class="ghost-btn mini" type="button" id="openProviderSettingsNoticeBtn">${escapeHtml(t("workspace.main.openProviderSettings"))}</button>
      </div>
    </div>
  `;
  $("openModelSettingsNoticeBtn").addEventListener("click", () => openSettingsModal("models"));
  $("openProviderSettingsNoticeBtn").addEventListener("click", () => openSettingsModal("providers"));
}

function disconnectAgentTransports() {
  clearMessageRefreshTimer(state.agent?.id);
  invalidateMessageLifecycle();
  // The transcript we are leaving owns blob: URLs for its image assets. Nothing
  // else references them once its DOM is replaced, so revoke them here rather
  // than letting them accumulate across conversation switches.
  releaseProtectedImageURLs();
  agentStream.disconnect();
  subagentCards.resetAgentLoad();
  backgroundTasks.setAgent("");
  state.ws = null;
  if (state.terminalWS) {
    const socket = state.terminalWS;
    state.terminalWS = null;
    try { socket.close(); } catch {}
  }
  const badge = $("wsBadge");
  if (badge) {
    badge.textContent = "ws idle";
    badge.classList.remove("ok");
  }
  state.agentStreamStatus = "idle";
  setComposerConnectionStatus(t("workspace.main.idle"));
  setTerminalStatus("idle");
  updateRuntimeStatusButton();
}

async function applyAgentLiveSnapshot(snapshot, detail = {}) {
  const agentId = snapshot?.agent?.id || "";
  if (!agentId || state.agent?.id !== agentId) return;
  const nextAgent = snapshot.agent;
  const nextWorkState = normalizeWorkStateSnapshot(snapshot);
  state.agent = nextAgent;
  contextManagement.applyStatus(snapshot.context || {}, { agentId });
  state.workState = nextWorkState;
  backgroundTasks.applySnapshot(snapshot, { agentId });
  if (detail.source === "initial") await executionNotifications.initial(snapshot, { agentId });
  else await executionNotifications.snapshot(snapshot, { agentId });
  renderConversationHeaderIdentity();
  syncNavigationConversationFromAgent(state.agent, { reason: "agent-snapshot" });
  navigationRefresh.request("agent-snapshot");
  // applyMessageSnapshot at the end of this function rebuilds the whole
  // transcript -- live cards, run summary, plans and approvals included -- so
  // every clear here is painting an intermediate state that is about to be
  // overwritten. Doing it unguarded is what makes opening or switching a
  // conversation flash: the view empties, then fills.
  clearLiveAssistantText({ preserveView: true });
  clearLiveImageGenerations({ agentId, preserveView: true });
  const recoveredToolOutputs = Object.fromEntries(Object.entries(state.liveToolOutputs || {}).filter(([, value]) => value?.agentId && value.agentId !== agentId));
  for (const call of Array.isArray(snapshot.toolActivity) ? snapshot.toolActivity : []) {
    const toolUseId = String(call?.toolUseId || call?.tool_use_id || "").trim();
    if (toolUseId) recoveredToolOutputs[toolUseId] = { ...call, agentId, toolUseId };
  }
  state.liveToolOutputs = recoveredToolOutputs;
  clearRunSummary({ preserveView: true });
  replacePlanState(snapshot.activePlan, snapshot.pendingPlanApproval ?? snapshot.pendingPlan, agentId);
  replacePendingApprovals(snapshot.pendingApprovals, agentId);
  replacePendingUserQuestions(snapshot.pendingUserQuestions, agentId);
  applyMessageSnapshot(snapshot.messages, agentId, {
    hasMoreBefore: snapshot.messageHasMoreBefore,
    nextBefore: snapshot.messageNextBefore,
  });
  // Re-render the heading now that the messages exist: an untitled conversation
  // takes its title from the first user message, which was not yet loaded when
  // the header rendered above.
  renderConversationHeaderIdentity();
  const permissionMode = $("permissionMode");
  if (permissionMode) permissionMode.value = state.agent.permissionMode || "acceptEdits";
  enforcePermissionSelectCap();
  renderModelOptions();
  refreshReasoningEffortControl();
  refreshFastModeControl();
  refreshMessageModeControl();
  updateWorkspaceMetaPills();
  renderWorkbenchShell();
  syncMessageComposerBusy();
  refreshComposerActivityStatus();
  const latestRun = snapshot.latestRun;
  if (latestRun?.id && ["completed", "error", "failed", "interrupted", "superseded"].includes(latestRun.status)) {
    loadRunSummary(latestRun.id, { agentId }).catch((error) => notifyTerminal(`[warn] ${am("runSummaryRestoreFailed", { message: error?.message || error })}\n`));
  }
}

async function handleAgentStreamEvent(event) {
  const agentId = state.agent?.id || "";
  if (!agentId || (event.agentId && event.agentId !== agentId)) return;
  backgroundTasks.handleEvent(event);
  await executionNotifications.live(event, { agentId });
  if (shouldLogAgentEvents()) appendTerminal(`[event] ${event.type}${event.text ? `: ${event.text}` : ""}\n`);
  applyPlanEvent(event);
  if (event.type === "context.updated") {
    const contextUpdate = event.data?.context || event.data?.status || event.data || {};
    if (Number.isInteger(Number(contextUpdate.entityGeneration))) {
      state.agent = {
        ...state.agent,
        entityGeneration: Number(contextUpdate.entityGeneration),
        prunedPercent: Number(contextUpdate.prunedPercent) || state.agent?.prunedPercent || 0,
        pruneEnabled: contextUpdate.pruneEnabled ?? state.agent?.pruneEnabled,
      };
    }
    contextManagement.applyStatus(contextUpdate, { agentId, partial: true });
  }
  if (event.type === "agent.title_updated") {
    const generatedTitle = String(event.data?.title || "").trim();
    const generation = Number(event.data?.entityGeneration);
    // An open title editor is the user's in-flight intent, so the server's
    // generated name must not overwrite what they are currently typing.
    if (generatedTitle && !state.titleEditing && !state.titleSaving) {
      state.agent = { ...state.agent, title: generatedTitle, entityGeneration: Number.isInteger(generation) ? generation : state.agent?.entityGeneration };
      state.worklineAgents = (state.worklineAgents || []).map((agent) => agent.id === agentId ? { ...agent, title: generatedTitle } : agent);
      syncNavigationConversationFromAgent(state.agent, { reason: "agent-title" });
      renderConversationHeaderIdentity();
      renderWorkbenchShell();
    }
  }
  const runId = event.data?.runId || "";
  const requestId = event.data?.requestId || "";
  // The assistant turn is persisted and announced before its tools run, so this
  // is the last message id seen when tool.started arrives -- i.e. the owner of
  // whatever activity follows.
  if (event.type === "message.created") rememberAssistantToolOwner(event.messageId || event.data?.messageId);
  const completedMessageEvents = ["message.created", "message.completed"];
  const terminalAgentEvents = ["agent.done", "agent.error", "agent.interrupted"];
  const navigationRefreshEvents = ["agent.started", ...completedMessageEvents, ...terminalAgentEvents];
  if (event.type === "agent.started") {
    state.agent = { ...state.agent, status: "running" };
    syncNavigationConversationFromAgent(state.agent, { status: "running", reason: "agent-started" });
    clearRunSummary();
    clearLiveAssistantText();
    syncMessageComposerBusy();
    refreshComposerActivityStatus();
  }
  if (event.type === "model.started") {
    beginLiveAssistantGeneration({
      requestId,
      runId,
      provider: event.data?.provider,
      model: event.data?.model,
      startedAt: event.data?.startedAt,
    });
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.reasoning") {
    appendLiveReasoning({ ...event, data: { ...(event.data || {}), runId } });
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.text") {
    // The model has stopped planning and started answering, so whatever it
    // was reasoning about is a finished step.
    closeLiveReasoningStep();
    appendLiveAssistantText(event.text || event.data?.text || "", { requestId, runId });
    refreshComposerActivityStatus();
  }
  if (event.type === "model.streaming") {
    updateLiveAssistantPerformance(event.data?.pendingThroughput, { requestId, runId });
  }
  if (event.type === "model.completed") {
    const throughput = event.data?.throughput && typeof event.data.throughput === "object" ? { ...event.data.throughput } : {};
    if (throughput.ttftMs == null && event.data?.ttftMs != null) throughput.ttftMs = event.data.ttftMs;
    updateLiveAssistantPerformance(throughput, { requestId, runId, replace: true });
  }
  if (event.type === "image_generation.status") {
    rememberImageGenerationStatus(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.started") {
    rememberToolStarted(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.output") appendToolOutput(event);
  if (event.type === "tool.approval_required") {
    rememberToolApproval(event);
    showToast(event.data?.risk === "danger" ? t("workspace.chat.dangerousToolBlocked") : t("workspace.chat.toolApproval"), event.data?.risk === "danger" ? "error" : "warn");
    refreshComposerActivityStatus();
  }
  if (event.type === "user.question_required") {
    rememberUserQuestion(event);
    showToast(t("workspace.chat.userQuestionRequired"), "warn");
    refreshComposerActivityStatus();
  }
  if (event.type === "tool.finished") {
    clearToolApproval(event.data?.toolUseId);
    clearUserQuestion(event.data?.toolUseId);
    finishToolOutput(event);
    refreshComposerActivityStatus();
  }
  if (event.type === "agent.interrupted") clearCurrentAgentApprovals();
  if (terminalAgentEvents.includes(event.type)) {
    const status = event.type === "agent.error" ? "error" : "idle";
    state.agent = { ...state.agent, status };
    syncNavigationConversationFromAgent(state.agent, { status, reason: event.type });
  }
  if ([...completedMessageEvents, ...terminalAgentEvents].includes(event.type)) {
    // preserveView because the persisted message has not arrived yet. Rendering
    // the cleared state now tears the streamed answer out of the transcript,
    // the page collapses by its height, and 80ms later loadMessages puts an
    // equivalent block back -- a visible jolt at the end of every single turn.
    // The state is cleared either way; only the repaint waits for the refresh
    // that is about to replace this content anyway.
    clearLiveAssistantText({ preserveView: true });
    clearLiveImageGenerations({ agentId, preserveView: true });
    // Keep live tool and reasoning records visible until the persisted run
    // summary successfully takes ownership of them. The composer separately
    // treats terminal live records as display-only, so retaining them here no
    // longer leaves the send button stuck in Stop mode.
    syncMessageComposerBusy();
    refreshComposerActivityStatus();
    // The context readout and its panel are fed by context.updated, and every
    // place that publishes it does so only when the context was compacted or
    // explicitly managed -- never for an ordinary turn. So the numbers stayed
    // frozen at whatever they were when the conversation was opened, and only a
    // reload appeared to fix them. A finished turn is exactly when the token
    // count has changed, so re-read it here. Guarded on the agent still being
    // the open one so a background conversation cannot overwrite the panel.
    if (state.agent?.id === agentId) contextManagement.load().catch(() => {});
  }
  if ([...completedMessageEvents, ...terminalAgentEvents].includes(event.type)) scheduleMessageRefresh(80, agentId);
  if (navigationRefreshEvents.includes(event.type)) navigationRefresh.request(event.type);
  if (["agent.error", "agent.interrupted"].includes(event.type)) {
    const restore = runId ? loadRunSummary(runId, { agentId }) : loadLatestRunSummary(agentId);
    void restore.then((summary) => {
      if (!summary || state.agent?.id !== agentId) return;
      // The persisted outcome now owns these records; remove the live copy so
      // the same tool calls are not shown twice.
      clearLiveToolOutputs({ agentId });
      // Records can arrive between the completion event and this callback, and
      // clearLiveToolOutputs does not touch the composer, so the button state
      // has to be recomputed against what is left.
      syncMessageComposerBusy();
    }).catch((error) => notifyTerminal(`[warn] ${am("runSummaryRestoreFailed", { message: error?.message || error })}\n`));
  }
  // Successful runs: also eagerly persist tool history so it survives before
  // the next user message triggers pruneLiveToolOutputs and wipes the live view.
  if (event.type === "agent.done" && runId) {
    const restore = loadRunSummary(runId, { agentId });
    void restore.then((summary) => {
      if (!summary || state.agent?.id !== agentId) return;
      clearLiveToolOutputs({ agentId });
      syncMessageComposerBusy();
    }).catch(() => {});
  }
}

function captureAgentSettingsSnapshot() {
  const selectableModel = selectedModelValue();
  const rawModel = String($("modelSelect")?.value || "").trim();
  const model = state.agent && rawModel === state.agent.model ? rawModel : selectableModel;
  return {
    agentId: state.agent?.id || "",
    model,
    selectableModel,
    permissionMode: String($("permissionMode")?.value || "").trim(),
    reasoningEffort: selectedReasoningEffort(model),
  };
}

async function persistAgentSettingsPass(snapshot) {
  let agentId = "";
  let previousAgent = null;
  let modelPatchInFlight = false;
  try {
    const { model, selectableModel, permissionMode, reasoningEffort } = snapshot || {};
    if (!snapshot?.agentId) {
      if (selectableModel) setPreferredModel(selectableModel);
      renderModelOptions();
      refreshActiveSettingsPanel();
      notifyTerminal(model ? `[info] ${am("modelPreferenceSaved", { model })}\n` : `[info] ${am("noModelSelectedTerminal")}\n`);
      return;
    }
    if (state.agent?.id !== snapshot.agentId) return;
    agentId = snapshot.agentId;
    previousAgent = state.agent;
    const id = agentId;
    const applyAgentPatch = async (path, payload) => {
      const updated = await api(`/api/agents/${id}/${path}`, { method: "PATCH", body: JSON.stringify(payload) });
      if (state.agent?.id !== id) return false;
      if (path === "model" && String(updated?.model || "").trim() !== String(payload.model || "").trim()) {
        throw new Error(`Model update did not persist ${payload.model}`);
      }
      state.agent = updated;
      return true;
    };
    if (model && model !== state.agent.model) {
      modelPatchInFlight = true;
      if (!await applyAgentPatch("model", { model })) return;
      modelPatchInFlight = false;
    }
    const storedReasoningEffort = String(state.agent.reasoningEffort || "").trim().toLowerCase();
    if ((storedReasoningEffort && storedReasoningEffort !== reasoningEffort) || (!storedReasoningEffort && reasoningEffort !== "auto")) {
      if (!await applyAgentPatch("reasoning-effort", { reasoningEffort })) return;
    }
    if (permissionMode && permissionMode !== state.agent.permissionMode) {
      if (!await applyAgentPatch("permission-mode", { permissionMode })) return;
    }
    if (state.agent?.id !== id) return;
    if (selectableModel && model === selectableModel) setPreferredModel(selectableModel);
    if (state.agentSavePending) return;
    await enterAgent();
    if (state.agent?.id !== id) return;
    notifyTerminal(`Saved settings: ${state.agent.model}, ${state.agent.permissionMode}\n`);
  } catch (err) {
    if (modelPatchInFlight && previousAgent && state.agent?.id === previousAgent.id) {
      state.agent = previousAgent;
      renderModelOptions();
      refreshReasoningEffortControl();
      refreshFastModeControl();
      updateWorkspaceMetaPills();
    }
    if (!agentId || state.agent?.id === agentId) throw err;
  }
}

function saveAgentSettings() {
  state.agentSaveSnapshot = captureAgentSettingsSnapshot();
  state.agentSavePending = true;
  if (state.agentSavePromise) return state.agentSavePromise;
  state.agentSaving = true;
  state.agentSavePromise = (async () => {
    while (state.agentSavePending) {
      const snapshot = state.agentSaveSnapshot;
      state.agentSavePending = false;
      await persistAgentSettingsPass(snapshot);
    }
  })().finally(() => {
    state.agentSaving = false;
    state.agentSavePromise = null;
  });
  return state.agentSavePromise;
}

async function waitForAgentSettingsSave(agentId = state.agent?.id) {
  while (state.agentSavePromise || state.agentSavePending) {
    await (state.agentSavePromise || saveAgentSettings());
  }
  if (agentId && state.agent?.id !== agentId) {
    throw new Error("The active conversation changed while model settings were saving.");
  }
}

function showToast(message, variant = "info", options = {}) {
  if (!options.force && !notificationVariantEnabled(variant)) return;
  const stack = $("toastStack");
  if (!stack) return;
  const node = document.createElement("div");
  node.className = `toast toast-${variant}`;
  node.innerHTML = `<span>${escapeHtml(message)}</span><button type="button" aria-label="${escapeAttr(am("closeNotification"))}">×</button>`;
  const close = () => {
    node.classList.add("leaving");
    window.setTimeout(() => node.remove(), 180);
  };
  node.querySelector("button").addEventListener("click", close);
  stack.appendChild(node);
  window.setTimeout(close, notificationToastDuration(variant));
}

function showError(err) {
  const message = err.message || String(err);
  showToast(message, "error", { force: true });
  notifyTerminal(`[error] ${message}\n`);
}

bindSetupWizardActions();
bindWorkspaceExplorer();
document.addEventListener("keydown", handleGlobalEscape);
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if ($("appShell")?.classList.contains("background-tasks-open")) backgroundTasks.closeTray("escape");
  if ($("appShell")?.classList.contains("details-open")) closeConversationDetails();
});
document.addEventListener("keydown", handleSettingsSearchShortcut);
document.addEventListener("click", handleDirectoryShortcutClick);
document.addEventListener("click", handleSidebarSettingsMenuDocumentClick);
document.querySelectorAll("[data-global-rail-target]").forEach((node) => {
  node.addEventListener("click", () => activateGlobalRailTarget(node.dataset.globalRailTarget));
});
$("globalThemeToggleBtn")?.addEventListener("click", () => {
  toggleAppearanceColorScheme();
});
// The phone drawer has no global rail, so it carries its own entry to the same
// scheme toggle rather than leaving light/dark unreachable on a phone.
$("mobileSidebarThemeBtn")?.addEventListener("click", () => {
  toggleAppearanceColorScheme();
});
$("refreshBtn").addEventListener("click", () => refreshPrimaryMode().catch(showError));
document.querySelectorAll("[data-create-navigation-item]").forEach((button) => {
  button.addEventListener("click", () => createNavigationItem(button).catch(showError));
});
$("mobileNewScheduleBtn")?.addEventListener("click", startScheduleCreation);
$("mobileScheduleModeBtn")?.addEventListener("click", () => {
  closeMobileSidebar();
  switchPrimaryWorkbench(state.activeWorkbench === "schedules" ? "conversation" : "schedules");
});
$("newTaskBtn")?.addEventListener("click", () => focusTaskCreation().catch(showError));
$("currentTitle")?.addEventListener("click", () => beginConversationTitleEdit("conversation"));
$("editConversationTitleBtn")?.addEventListener("click", () => beginConversationTitleEdit("conversation"));
$("saveConversationTitleBtn")?.addEventListener("click", () => saveConversationTitle("conversation").catch(showError));
$("cancelConversationTitleBtn")?.addEventListener("click", cancelConversationTitleEdit);
$("currentTitleInput")?.addEventListener("input", (event) => updateTitleDraft("conversation", event));
$("currentTitleInput")?.addEventListener("keydown", (event) => handleTitleEditorKeydown("conversation", event));
$("workbenchTitle")?.addEventListener("click", () => beginConversationTitleEdit("workbench"));
$("editWorkbenchTitleBtn")?.addEventListener("click", () => beginConversationTitleEdit("workbench"));
$("saveWorkbenchTitleBtn")?.addEventListener("click", () => saveConversationTitle("workbench").catch(showError));
$("cancelWorkbenchTitleBtn")?.addEventListener("click", cancelConversationTitleEdit);
$("workbenchTitleInput")?.addEventListener("input", (event) => updateTitleDraft("workbench", event));
$("workbenchTitleInput")?.addEventListener("keydown", (event) => handleTitleEditorKeydown("workbench", event));
$("sidebarAccountBtn")?.addEventListener("click", (event) => {
  event.stopPropagation();
  toggleSidebarSettingsMenu();
});
$("settingsBtn").addEventListener("click", () => { closeSidebarSettingsMenu(); openSettingsModal("providers"); });
$("providerSettingsBtn")?.addEventListener("click", () => { closeSidebarSettingsMenu(); openSettingsModal("providers"); });
$("modelSettingsBtn")?.addEventListener("click", () => { closeSidebarSettingsMenu(); openSettingsModal("models"); });
$("runtimeSettingsBtn")?.addEventListener("click", () => { closeSidebarSettingsMenu(); openSettingsModal("servers-system"); });
$("aboutSettingsBtn")?.addEventListener("click", () => { closeSidebarSettingsMenu(); openSettingsModal("about"); });
$("logoutBtn")?.addEventListener("click", () => logoutRemoteAccess().catch(showError));
$("settingsSearchInput")?.addEventListener("input", (event) => updateSettingsSearchQuery(event.target.value));
$("settingsSearchInput")?.addEventListener("keydown", (event) => {
  if (isComposingInput(event)) return;
  if (event.key === "Enter") {
    const first = firstFilteredSettingsItem();
    if (first) selectSettingsPanel(first.key);
    event.preventDefault();
  }
});
$("clearSettingsSearchBtn")?.addEventListener("click", () => clearSettingsSearchQuery({ focus: true }));
$("closeSettingsModalBtn").addEventListener("click", () => requestCloseSettingsModal());
$("settingsModal").addEventListener("keydown", (event) => {
  settingsHelp.handleKeydown(event);
  if (!state.settingsShellOpen) handleSettingsDialogKeydown(event);
});
$("settingsModal").addEventListener("click", (event) => { if (event.target.id === "settingsModal") closeSettingsModal(); });
$("closeConversationDetailsBtn")?.addEventListener("click", closeConversationDetails);
$("settingsWizardBtn").addEventListener("click", () => {
  closeSettingsModal();
  openSetupWizard().catch(showError);
});
$("manageBackendsBtn").addEventListener("click", () => { closeSidebarSettingsMenu(); openBackendsModal(); });
$("closeBackendsModalBtn").addEventListener("click", closeBackendsModal);
$("backendsModal").addEventListener("click", (event) => { if (event.target.id === "backendsModal") closeBackendsModal(); });
$("backendForm").addEventListener("submit", (event) => saveBackend(event).catch(showError));
$("resetBackendFormBtn").addEventListener("click", resetBackendForm);
$("mobileMenuBtn").addEventListener("click", () => {
  // The drawer always opens on the conversation/project list, never the
  // last-used "schedules/任務" view. Reach schedules via the 排程 button.
  switchPrimaryWorkbench("conversation");
  openMobileSidebar();
});
$("mobileSidebarCloseBtn")?.addEventListener("click", closeMobileSidebar);
$("mobileSidebarBackdrop").addEventListener("click", closeMobileSidebar);
$("mobileSearchBtn").addEventListener("click", focusMobileSearch);
// An installed PWA has no browser reload button, so the top bar carries the
// gesture instead. Bound to the top bar rather than the transcript because the
// transcript's top edge already loads older history.
if (isPullToRefreshSupported()) {
  const mobileTopbar = document.querySelector(".mobile-topbar");
  if (mobileTopbar) {
    installPullToRefresh({
      target: mobileTopbar,
      onRefresh: () => globalThis.location?.reload?.(),
    });
  }
}
$("mobileSidebarSettingsBtn")?.addEventListener("click", () => {
  closeMobileSidebar();
  closeSidebarSettingsMenu();
  openSettingsModal("providers", { showMobileIndex: true });
});
$("mobileSidebarLogoutBtn")?.addEventListener("click", () => {
  closeMobileSidebar();
  closeSidebarSettingsMenu();
  logoutRemoteAccess().catch(showError);
});
$("navigationContextMenu")?.addEventListener("click", (event) => {
  const action = event.target.closest?.("[data-navigation-menu-action]")?.dataset.navigationMenuAction;
  if (!action) return;
  event.preventDefault();
  event.stopPropagation();
  applyNavigationMenuAction(action).catch(showError);
});
document.addEventListener("contextmenu", handleNavigationContextMenu);
document.addEventListener("click", (event) => {
  const menu = $("navigationContextMenu");
  if (!menu || menu.classList.contains("hidden")) return;
  if (menu.contains(event.target) || event.target.closest?.("[data-navigation-menu-trigger]")) return;
  closeNavigationContextMenu();
});
document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (!(state.navigationMenuTarget && !$("navigationContextMenu")?.classList.contains("hidden"))) return;
  closeNavigationContextMenu({ restoreFocus: true });
  event.preventDefault();
});
$("projectSearchToggleBtn")?.addEventListener("click", (event) => {
  event.preventDefault();
  event.stopPropagation();
  toggleProjectSearch();
});
$("projectSearchClearBtn")?.addEventListener("click", () => {
  closeProjectSearch({ clear: true });
  if (state.activeWorkbench === "schedules") scheduleWorkspace.setQuery("");
});
$("projectSearch").addEventListener("input", (event) => {
  state.projectQuery = event.target.value;
  if (state.activeWorkbench === "schedules") scheduleWorkspace.setQuery(event.target.value);
  else renderProjects();
});
$("projectSearch").addEventListener("keydown", (event) => {
  if (isComposingInput(event)) return;
  if (event.key === "Escape") {
    closeProjectSearch({ clear: true });
    if (state.activeWorkbench === "schedules") scheduleWorkspace.setQuery("");
    event.preventDefault();
  }
});
$("copyConversationBtn")?.addEventListener("click", () => copyCurrentConversationMarkdown().catch(showError));
$("runtimeStatusBtn")?.addEventListener("click", () => {
  if ($("appShell")?.classList.contains("details-open")) closeConversationDetails();
  else openConversationDetails();
});
$("gitWorkflowBtn")?.addEventListener("click", openGitModal);
$("workbenchBoardBtn")?.addEventListener("click", () => {
  if (!state.agent?.id) return;
  taskWorkspace.setContext({ projectId: state.project?.id || "", agentId: state.agent.id, scope: "agent" });
  specBoard.load().catch(showError);
  projectKanban.render();
  $("projectKanbanBody")?.scrollTo?.({ top: 0, behavior: "smooth" });
});
$("workbenchFilesBtn")?.addEventListener("click", () => openWorkspace("files"));
$("workbenchGitBtn")?.addEventListener("click", openGitModal);
$("workbenchRunBtn")?.addEventListener("click", openConversationDetails);
$("workbenchTerminalBtn")?.addEventListener("click", () => toggleTerminalDock());
$("workbenchPreviewBtn")?.addEventListener("click", () => openWorkspace("preview"));
$("closeGitModalBtn")?.addEventListener("click", closeGitModal);
$("gitModal")?.addEventListener("click", (event) => { if (event.target.id === "gitModal") closeGitModal(); });
$("closeFolderModalBtn").addEventListener("click", closeDirectoryModal);
$("cancelDirectoryBtn").addEventListener("click", closeDirectoryModal);
$("folderModal").addEventListener("click", (event) => { if (event.target.id === "folderModal") closeDirectoryModal(); });
$("folderHomeBtn").addEventListener("click", () => browseHomeDirectory().catch(showError));
$("folderParentBtn").addEventListener("click", () => browseParentDirectory().catch(showError));
$("folderRefreshBtn").addEventListener("click", () => refreshDirectory().catch(showError));
$("nativeDirectoryBtn")?.addEventListener("click", (event) => {
  selectNativeDirectory(state.directoryPath, { trigger: event.currentTarget }).then(async (picked) => {
    if (!picked?.path) return;
    updateDirectoryPathDisplay(picked.path);
    setDirectoryStatus(am("finderSelectionOpening", { path: picked.path }), "busy");
    await createProjectFromDirectory(picked.path, { button: $("chooseDirectoryBtn") });
  }).catch(showError);
});
$("folderLocationPill")?.addEventListener("click", () => {
  const input = $("manualDirectoryPath");
  input?.focus();
  input?.select();
});
$("newFolderBtn").addEventListener("click", showNewFolderInline);
$("confirmNewFolderBtn").addEventListener("click", () => createFolderInCurrentDirectory().catch(showError));
$("cancelNewFolderBtn").addEventListener("click", hideNewFolderInline);
$("newFolderNameInput").addEventListener("keydown", (event) => {
  if (isComposingInput(event)) return;
  if (event.key === "Enter") createFolderInCurrentDirectory().catch(showError);
  if (event.key === "Escape") {
    hideNewFolderInline();
    event.preventDefault();
    event.stopPropagation();
  }
});
$("favoriteDirectoryBtn").addEventListener("click", favoriteCurrentDirectory);
$("goDirectoryBtn").addEventListener("click", () => browseDirectories($("manualDirectoryPath").value.trim()).catch(showError));
$("manualDirectoryPath").addEventListener("keydown", (event) => {
  if (isComposingInput(event)) return;
  if (event.key === "Enter") browseDirectories($("manualDirectoryPath").value.trim()).catch(showError);
});
$("chooseDirectoryBtn").addEventListener("click", () => createProjectFromDirectory(state.directoryPath).catch(showError));
$("messageForm").addEventListener("submit", (event) => sendMessage(event).catch(showError));
// Message mode buttons open the shared permission/mode panel (bound in ui-shell).
$("attachFileBtn")?.addEventListener("click", openAttachmentPicker);
$("attachFileInput")?.addEventListener("change", (event) => importAttachmentFiles(event).catch(showError));
$("composerInputShell")?.addEventListener("dragover", handleAttachmentDragOver);
$("composerInputShell")?.addEventListener("dragleave", handleAttachmentDragLeave);
$("composerInputShell")?.addEventListener("drop", handleAttachmentDrop);
$("messageText").addEventListener("input", handleMessageInput);
$("messageText").addEventListener("paste", scheduleMessageInputResize);
$("messageText").addEventListener("keydown", handleMessageKeydown);
$("messageText").addEventListener("paste", handleMessagePaste);
$("messageText").addEventListener("compositionstart", () => { state.mentionComposing = true; });
$("messageText").addEventListener("compositionend", () => { state.mentionComposing = false; handleMessageInput(); });
$("messageText").addEventListener("focus", updateSlashCommandPalette);
$("messageText").addEventListener("blur", () => window.setTimeout(hideSlashCommandPalette, 120));
$("terminalOutput").addEventListener("keydown", handleTerminalKeydown);
$("terminalOutput").addEventListener("click", () => $("terminalOutput").focus());
$("terminalOutput").addEventListener("paste", (event) => {
  event.preventDefault();
  sendTerminalInput(event.clipboardData?.getData("text") || "");
});
$("reconnectTerminalBtn").addEventListener("click", connectTerminal);
window.addEventListener("resize", () => {
  closeMobileSidebar({ restoreFocus: false });
  leaveOverviewForMobile();
  syncSettingsViewportState();
  layoutSettingsShell();
  resizeTerminal();
  autoResizeMessageInput();
  refreshComposerActivityStatus();
});
window.addEventListener("autoto:auth-changed", () => {
  // Authentication changes start a new data lifecycle; never let the prior
  // startup restore into the newly authorized/unauthorized workspace.
  navigationStartupGuard.invalidate();
  if (state.initializing) state.initRestartRequested = true;
  saveCurrentChatDraft();
  navigationRefresh.stop();
  state.serverDrafts = {};
  disconnectAgentTransports();
  closeWorkspace();
  closeConversationDetails();
  state.project = null;
  state.workline = null;
  state.agent = null;
  contextManagement.reset(null);
  state.navigationSelectionKind = "project";
  state.workState = null;
  syncProjectOperationContext();
  state.projectWorklines = [];
  state.worklineAgents = [];
  state.chatHydrating = false;
  state.currentMessages = [];
  state.messageCopyTexts = [];
  clearRunSummary();
  clearPlanState();
  clearLiveAssistantText();
  setWorkspaceExplorerAgent(null);
  projectKanban.setAgent(null);
  taskWorkspace.setContext({ projectId: "", agentId: "", scope: "dispatch" });
  resetGitWorkflowState();
  renderWorkbenchShell();
  renderProjects();
  showEmptyWorkspaceState();
  init().catch(showError);
});
window.addEventListener("beforeunload", () => {
  navigationRefresh.stop();
  recentConversationSync.stop();
  saveCurrentChatDraft();
});
$("refreshModelsBtn")?.addEventListener("click", () => refreshModelCatalog().catch(showError));
$("openProviderLoginBtn")?.addEventListener("click", () => toggleFastMode().catch(showError));
$("modelSelect").addEventListener("change", () => {
  updateModelConfiguredState();
  refreshReasoningEffortControl({ modelValue: $("modelSelect").value });
  refreshFastModeControl({ modelValue: $("modelSelect").value });
  saveAgentSettings().catch(showError);
});
$("reasoningEffort")?.addEventListener("change", (event) => {
  refreshReasoningEffortControl({ requestedValue: event.target.value });
  saveReasoningEffort(event.target.value).catch(showError);
});
$("permissionMode").addEventListener("change", () => {
  if (!projectOperationContextActive()) return;
  updatePermissionModeDisplay();
  saveAgentSettings().catch(showError);
});
$("toggleTerminalBtn").addEventListener("click", () => toggleTerminalDock());
$("collapseTerminalBtn").addEventListener("click", () => toggleTerminalDock(true));
$("expandTerminalBtn").addEventListener("click", () => toggleTerminalDock(false));
$("terminalCommandForm")?.addEventListener("submit", (event) => {
  event.preventDefault();
  const input = $("terminalCommandInput");
  const command = String(input?.value || "").trim();
  if (!command) return;
  if (!sendTerminalInput(`${command}\r`)) {
    showToast(state.agent ? am("terminalNotConnected") : am("selectAgentFirst"), "warn", { force: true });
    return;
  }
  input.value = "";
  $("terminalOutput")?.focus();
});
renderTerminalButtonState();
updateRuntimeStatusButton();

async function init() {
  if (state.initializing) return;
  const seq = ++state.initSeq;
  const startupToken = navigationStartupGuard.beginInit(seq);
  state.initializing = true;
  const refreshButton = $("refreshBtn");
  if (refreshButton) {
    refreshButton.disabled = true;
    refreshButton.classList.add("loading");
    refreshButton.setAttribute("aria-busy", "true");
    refreshButton.title = t("common.refreshing");
  }
  try {
    state.profile = loadProfilePreferences();
    applyProfilePreferences();
    state.searchPrefs = loadSearchPreferences();
    state.skillsPrefs = loadSkillsPreferences();
    state.notifications = loadNotificationPreferences();
    state.appearance = loadAppearancePreferences();
    state.primaryModePreference = loadPrimaryModePreference();
    state.terminalPrefs = loadTerminalPreferences();
    state.chatDrafts = loadChatDrafts();
    state.messageQueue = loadMessageQueue();
    state.promptHistory = loadPromptHistory();
    state.recentConversations = loadRecentConversations();
    applyAppearancePreferences({ applyTerminalDefault: true });
    appearanceBackgroundManager.load().catch(() => appearanceBackgroundManager.apply({
      mode: currentAppearancePreferences().backgroundMode,
      url: currentAppearancePreferences().backgroundUrl,
      dim: currentAppearancePreferences().backgroundDim,
      positionX: currentAppearancePreferences().backgroundPositionX,
      positionY: currentAppearancePreferences().backgroundPositionY,
    }));
    themeManager.loadCatalog()
      .then(() => themeManager.applyPreference(currentAppearancePreferences(), { notifyMissing: false }))
      .catch(() => {});
    applyPrimaryWorkbench(currentPrimaryModePreference());
    updateGlobalThemeToggle();
    const accountPreferencesHydration = accountPreferences.hydrate();
    if (!state.agent) {
      $("currentTitle").textContent = t("chat.noAgent");
      $("composerStatusText").textContent = t("chat.idle");
      const terminalOutput = $("terminalOutput");
      if (terminalOutput && terminalOutput.textContent.startsWith("Terminal ready.")) terminalOutput.textContent = t("terminal.ready");
    }
    renderConversationHeaderIdentity();
    updatePermissionModeDisplay();
    autoResizeMessageInput();
    renderRecentSidebarConversations();
    renderRecentSidebarDirectories();
    // Health is informative rather than a prerequisite for rendering the last
    // conversation. Start it with the other requests so an extra tunnel round
    // trip never delays the initial screen.
    const healthPromise = loadHealth();
    // The static shell already has the selected locale and local preferences.
    // Reveal it as soon as remote requests are underway rather than holding a
    // full-screen loading indicator until every settings/catalog request has
    // completed. The navigation and current transcript continue to hydrate
    // below without blocking the first paint.
    signalAppReady();
    await Promise.all([accountPreferencesHydration, loadSettings(), loadRuntimeSummary(), remoteAccessSettings.load().catch(() => {}), loadModelCatalog(), loadProjects(), loadBackends(), loadServerSkills(), healthPromise]);
    if (seq !== state.initSeq) return;
    state.profile = loadProfilePreferences();
    applyProfilePreferences();
    renderModelOptions();
    navigationRefresh.start();
    if (!state.agent && state.startupWorkbenchIntent === "conversation" && !state.initRestartRequested) {
      state.startupWorkbenchIntent = "";
      if (state.activeWorkbench === "conversation" && !state.overviewActive) {
        await openDefaultConversationTarget({ preserveMessageState: true });
      }
    } else if (!state.agent && startupTokenCurrent(startupToken)) {
      const initialTarget = resolveInitialNavigationTarget(state.recentConversations, state.navigationConversations);
      const requestedView = new URLSearchParams(globalThis.location?.search || "").get("view") || "";
      const startup = resolveOverviewStartup({
        requestedView,
        hasConversation: Boolean(initialTarget),
        hasProject: state.projects.length > 0,
        mobile: isMobileAppViewport(),
      });
      if (startupTokenCurrent(startupToken)) {
        state.overviewActive = startup.overviewActive;
        applyPrimaryWorkbench(startup.workbench);
      }
      if (startup.restoreConversation && initialTarget && startupTokenCurrent(startupToken)) {
        await selectNavigationConversation(initialTarget, {
          preserveMessageState: true,
          preserveOverview: startup.overviewActive,
          source: "startup",
          startupToken,
        });
      } else if (startup.selectFallbackProject && startupTokenCurrent(startupToken)) {
        await selectProject(state.projects[0].id, {
          preserveMessageState: true,
          source: "startup",
          startupToken,
        });
      } else if (startupTokenCurrent(startupToken)) {
        state.chatHydrating = false;
      }
      if (startup.overviewActive && startupTokenCurrent(startupToken)) {
        await overviewDashboard.load();
        // A user click may invalidate this startup while the dashboard request
        // is pending. No startup navigation follows this await; the current init
        // still installs its global router and runs its normal finally cleanup.
      }
    }
    if (seq === state.initSeq) {
      installDesktopDeepLinkRouter({
        openSettings: (panel) => {
          openSettingsModal(panel || "providers");
        },
        openAgent: (id) => {
          const agentId = String(id || "").trim();
          if (!agentId) return;
          const target = state.navigationConversations.find((item) => item.agentId === agentId)
            || state.recentConversations.find((item) => item.agentId === agentId);
          if (target) {
            selectNavigationConversation(target).catch(showError);
            return;
          }
          // Fallback: open settings if agent not in list yet.
          showToast?.(t("chat.noAgent"), "info");
        },
        openProject: (id) => {
          if (!id) return;
          selectProject(id).catch(showError);
        },
        openConversation: (id) => {
          const agentId = String(id || "").trim();
          if (!agentId) return;
          const target = state.navigationConversations.find((item) => item.agentId === agentId || item.targetId === agentId)
            || state.recentConversations.find((item) => item.agentId === agentId || item.targetId === agentId);
          if (target) selectNavigationConversation(target).catch(showError);
        },
        openView: (view) => {
          const name = String(view || "").trim().toLowerCase();
          if (!name) return;
          if (name === "settings") {
            openSettingsModal("providers");
            return;
          }
          if (name === "details") {
            openConversationDetails();
            return;
          }
          if (name === "browser") {
            openWorkspace("preview");
            return;
          }
          if (name === "terminal") {
            toggleTerminalDock(false);
            return;
          }
          if (name === "chat" || name === "conversation") {
            applyPrimaryWorkbench("conversation");
          }
        },
      });
      if (isDesktopShell()) {
        // Soft-refresh desktop shell status when About is opened later.
        state.desktopShellReady = true;
      }
    }
  } finally {
    const restartRequested = seq === state.initSeq && state.initRestartRequested;
    if (seq === state.initSeq) {
      state.initializing = false;
      if (refreshButton) {
        refreshButton.disabled = false;
        refreshButton.classList.remove("loading");
        refreshButton.removeAttribute("aria-busy");
        renderPrimaryModeSidebar();
      }
      if (restartRequested) {
        // Auth changes can arrive during the single-flight init. The old
        // startup is already invalidated; restart after its cleanup so the new
        // auth lifecycle is not silently dropped or left permanently busy.
        state.initRestartRequested = false;
        const restart = () => init().catch(showError);
        if (typeof globalThis.queueMicrotask === "function") globalThis.queueMicrotask(restart);
        else Promise.resolve().then(restart);
      }
    }
  }
}

function openRequestedInitialView() {
  const params = new URLSearchParams(globalThis.location?.search || "");
  const view = params.get("view") || "";
  if (view === "settings") openSettingsModal(params.get("panel") || "providers");
  if (view === "details") openConversationDetails();
  if (view === "browser") openWorkspace("preview");
  if (view === "terminal") toggleTerminalDock(false);
}

function signalAppReady() {
  const root = globalThis.document?.documentElement;
  if (root?.dataset.autotoAppReady === "true") return;
  if (root) root.dataset.autotoAppReady = "true";
  const EventConstructor = globalThis.Event;
  if (typeof EventConstructor === "function") {
    globalThis.dispatchEvent?.(new EventConstructor("autoto:app-ready"));
  }
}

init().then(() => {
  openRequestedInitialView();
  const setupStartup = maybeOpenSetupWizard();
  signalAppReady();
  return setupStartup;
}).catch((error) => {
  signalAppReady();
  showError(error);
});
