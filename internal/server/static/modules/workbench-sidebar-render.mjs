import { $, coalescePerFrame, setTextIfChanged } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { overviewRailTarget } from "./overview-dashboard.mjs";
import { terminalAccessAllowed } from "./remote-access-capabilities.mjs";

// Which primary surface is visible for a given mode. Overview wins over every
// mode: it is a full-page surface, so entering it hides the conversation,
// workbench, and schedule panels rather than layering over them. Kept pure and
// separate from the DOM work so the visibility matrix can be checked directly.
export function primaryWorkbenchLayout(mode, { overviewActive = false } = {}) {
  const overview = overviewActive === true;
  const workbench = mode === "workbench" && !overview;
  const schedules = mode === "schedules" && !overview;
  const remote = mode === "remote" && !overview;
  return {
    overview,
    workbench,
    schedules,
    remote,
    hidden: {
      overviewDashboard: !overview,
      conversationPanel: overview || workbench || schedules,
      workbenchPanel: !workbench,
      schedulePanel: !schedules,
    },
    bodyClasses: {
      "overview-mode": overview,
      "workbench-mode": workbench,
      "schedule-mode": schedules,
      "remote-mode": remote,
    },
  };
}

export function appearanceAllowsPrimaryMode(mode, prefs = {}) {
  if (mode === "schedules") return prefs.showSchedulesNav !== false;
  if (mode === "remote") return prefs.showRemoteNav !== false;
  return true;
}

export function resolvedPrimaryWorkbench(value, prefs = {}) {
  const mode = ["workbench", "schedules", "remote"].includes(value) ? value : "conversation";
  return appearanceAllowsPrimaryMode(mode, prefs) ? mode : "conversation";
}

// Renders the primary-mode session sidebar (conversations/workbench/
// schedules header, search, and create buttons) and the workbench shell
// meta/status strip, plus the schedule surface and the small i18n text/
// attribute-setting helpers they share.
export function createWorkbenchSidebarRender({
  state,
  getTaskWorkspace,
  getScheduleWorkspace,
  getOverviewDashboard,
  getSpecBoard,
  currentSecuritySummary,
  syncNavigationCreateButton,
  renderWorkbenchHeaderIdentity,
  syncMobilePageTitle,
  renderProjects,
  openOverviewConversation,
  showError,
  init,
}) {
  // Writes to the element's own label span when it has one. Assigning
  // textContent to a button replaces ALL of its children, so calling this on a
  // button that also holds an icon silently deletes the icon on every render —
  // which is how the mobile schedule entry lost its glyph while its siblings
  // kept theirs.
  // Guarded because this renders several times per switch with the same key:
  // the sidebar titles, the mobile page title and the mobile action labels all
  // go through here, and each unguarded write replaced their text node.
  function setTranslatedText(element, key) {
    if (!element) return;
    const label = element.querySelector?.("[data-i18n-label]") || element;
    if (label.dataset.i18n !== key) label.dataset.i18n = key;
    setTextIfChanged(label, t(key));
  }

  function setTranslatedAttribute(element, attribute, key) {
    if (!element) return;
    element.setAttribute(`data-i18n-${attribute}`, key);
    element.setAttribute(attribute, t(key));
  }

  function normalizedPrimaryWorkbench(value) {
    return ["workbench", "schedules", "remote"].includes(value) ? value : "conversation";
  }

  function currentShellRailTarget() {
    return overviewRailTarget(state);
  }

  function renderPrimaryModeSidebar() {
    const taskWorkspace = getTaskWorkspace();
    const taskMode = state.activeWorkbench === "workbench";
    const scheduleMode = state.activeWorkbench === "schedules";
    const remoteMode = state.activeWorkbench === "remote";
    const sidebar = $("sessionSidebar");
    const title = $("sessionSidebarTitle");
    const actions = $("sessionSidebarActions");
    const resizeHandle = $("sidebarResizeHandle");
    const searchToggle = $("projectSearchToggleBtn");
    const searchInput = $("projectSearch");
    const refreshButton = $("refreshBtn");
    const newProjectButton = $("newProjectBtn");
    const newTaskButton = $("newTaskBtn");
    const mobileNewScheduleButton = $("mobileNewScheduleBtn");
    const mobileChooseDirectoryButton = $("mobileChooseDirectoryBtn");
    const mobileScheduleModeButton = $("mobileScheduleModeBtn");
    const mobileRemoteModeButton = $("mobileRemoteModeBtn");
    const sidebarLabelKey = scheduleMode ? "shell.scheduleSidebar" : remoteMode ? "shell.remoteSidebar" : taskMode ? "workbench.sidebarLabel" : "shell.sessionSidebar";
    const sidebarTitleKey = scheduleMode ? "shell.nav.schedules" : remoteMode ? "shell.nav.remote" : taskMode ? "workbench.sidebarTitle" : "shell.sessionTitle";
    const sidebarActionsKey = scheduleMode ? "shell.scheduleActions" : remoteMode ? "shell.remoteActions" : taskMode ? "workbench.sidebarActions" : "shell.sessionActions";
    const searchLabelKey = scheduleMode ? "shell.searchSchedulesLabel" : remoteMode ? "shell.searchRemoteLabel" : taskMode ? "workbench.searchContextLabel" : "shell.searchProjectsLabel";
    const searchPlaceholderKey = scheduleMode ? "shell.searchSchedules" : remoteMode ? "shell.searchRemote" : taskMode ? "workbench.searchContext" : "shell.searchProjects";
    const refreshKey = scheduleMode ? "shell.refreshSchedules" : remoteMode ? "shell.refreshRemote" : taskMode ? "workbench.refreshTasks" : "shell.refreshSessions";

    setTranslatedAttribute(sidebar, "aria-label", sidebarLabelKey);
    setTranslatedText(title, sidebarTitleKey);
    setTranslatedAttribute(actions, "aria-label", sidebarActionsKey);
    setTranslatedAttribute(resizeHandle, "aria-label", scheduleMode ? "shell.resizeScheduleSidebar" : remoteMode ? "shell.resizeRemoteSidebar" : taskMode ? "workbench.resizeSidebar" : "shell.resizeSidebar");
    [searchToggle].forEach((button) => {
      setTranslatedAttribute(button, "title", searchLabelKey);
      setTranslatedAttribute(button, "aria-label", searchLabelKey);
    });
    setTranslatedAttribute(searchInput, "placeholder", searchPlaceholderKey);
    setTranslatedAttribute(searchInput, "aria-label", searchLabelKey);
    if (refreshButton?.getAttribute("aria-busy") !== "true") {
      setTranslatedAttribute(refreshButton, "title", refreshKey);
      setTranslatedAttribute(refreshButton, "aria-label", refreshKey);
    }
    newProjectButton?.classList.toggle("hidden", taskMode || remoteMode);
    if (!taskMode && !remoteMode) syncNavigationCreateButton(newProjectButton);
    newTaskButton?.classList.toggle("hidden", !taskMode);
    mobileChooseDirectoryButton?.classList.toggle("hidden", scheduleMode || remoteMode);
    mobileNewScheduleButton?.classList.toggle("hidden", !scheduleMode);
    if (mobileNewScheduleButton) {
      setTranslatedText(mobileNewScheduleButton, "shell.newSchedule");
      setTranslatedAttribute(mobileNewScheduleButton, "title", "shell.newSchedule");
      setTranslatedAttribute(mobileNewScheduleButton, "aria-label", "shell.newSchedule");
    }
    if (mobileScheduleModeButton) {
      const mobileModeKey = scheduleMode ? "shell.nav.conversation" : "shell.nav.schedules";
      setTranslatedText(mobileScheduleModeButton, mobileModeKey);
      setTranslatedAttribute(mobileScheduleModeButton, "title", mobileModeKey);
      setTranslatedAttribute(mobileScheduleModeButton, "aria-label", mobileModeKey);
    }
    if (mobileRemoteModeButton) {
      const mobileRemoteKey = remoteMode ? "shell.nav.conversation" : "shell.nav.remote";
      setTranslatedText(mobileRemoteModeButton, mobileRemoteKey);
      setTranslatedAttribute(mobileRemoteModeButton, "title", mobileRemoteKey);
      setTranslatedAttribute(mobileRemoteModeButton, "aria-label", mobileRemoteKey);
    }
    if (newTaskButton) {
      const workspaceState = taskWorkspace.getState();
      const enabled = workspaceState.scope === "agent"
        ? Boolean(state.agent?.id)
        : workspaceState.workspace.summary.agentCount > 0;
      const taskActionKey = enabled ? "workbench.createTask" : "workbench.selectAgentToCreate";
      newTaskButton.disabled = !enabled;
      setTranslatedAttribute(newTaskButton, "title", taskActionKey);
      setTranslatedAttribute(newTaskButton, "aria-label", taskActionKey);
    }
  }

  function renderWorkbenchShellNow() {
    const taskWorkspace = getTaskWorkspace();
    const agent = state.agent;
    const project = state.project;
    const workspaceState = taskWorkspace.getState();
    const scope = workspaceState.scope;
    const selectedProject = workspaceState.workspace.projects.find((item) => item.id === workspaceState.projectId) || null;
    const summary = scope === "project" && selectedProject ? selectedProject.counts : workspaceState.workspace.summary;
    const meta = $("workbenchMeta");
    const status = $("workbenchAgentStatus");
    const agentTitle = String(agent?.title || agent?.id || "").trim();
    const projectTitle = String(project?.name || "").trim();
    renderWorkbenchHeaderIdentity();
    if (meta) {
      if (scope === "agent") {
        meta.textContent = agent
          ? `${t("workbench.currentAgent", { agent: agentTitle })} · ${t("workbench.currentProject", { project: projectTitle || "—" })}`
          : t("workbench.selectAgent");
      } else if (scope === "project" && selectedProject) {
        setTextIfChanged(meta, `${selectedProject.agents.length} ${t("taskWorkspace.agents")} · ${selectedProject.counts.total} ${t("taskWorkspace.tasks")}`);
      } else {
        setTextIfChanged(meta, `${workspaceState.workspace.summary.projectCount} ${t("taskWorkspace.projects")} · ${workspaceState.workspace.summary.agentCount} ${t("taskWorkspace.agents")}`);
      }
    }
    if (status) {
      if (scope === "agent") {
        setTextIfChanged(status, agent?.status || "idle");
        status.classList.toggle("ok", Boolean(agent && agent.status === "idle"));
        status.classList.toggle("warn", Boolean(agent && ["running", "interrupted"].includes(agent.status)));
      } else {
        setTextIfChanged(status, `${Number(summary?.blocked || 0)} ${t("taskWorkspace.blocked")}`);
        status.classList.toggle("ok", Number(summary?.blocked || 0) === 0);
        status.classList.toggle("warn", Number(summary?.blocked || 0) > 0);
      }
    }
    const enabled = scope === "agent" && Boolean(agent?.id);
    const boardButton = $("workbenchBoardBtn");
    if (boardButton) {
      boardButton.disabled = !state.agent?.id;
      boardButton.classList.toggle("active", scope === "agent");
    }
    ["workbenchFilesBtn", "workbenchGitBtn", "workbenchRunBtn", "workbenchPreviewBtn"].forEach((id) => {
      const button = $(id);
      if (button) button.disabled = !enabled;
    });
    const terminalButton = $("workbenchTerminalBtn");
    const security = currentSecuritySummary();
    const terminalLocked = !terminalAccessAllowed(state);
    if (terminalButton) terminalButton.disabled = !enabled || terminalLocked;
    const gitCount = Array.isArray(state.gitStatus?.files) ? state.gitStatus.files.length : 0;
    const gitBadge = document.querySelector("[data-workbench-git-badge]");
    if (gitBadge) {
      setTextIfChanged(gitBadge, gitCount > 99 ? "99+" : String(gitCount));
      gitBadge.classList.toggle("hidden", !enabled || gitCount === 0);
    }
    renderPrimaryModeSidebar();
  }

  // updateAgentStreamStatus alone drives this four times per switch as the
  // connection moves through its states. See coalescePerFrame.
  const renderWorkbenchShell = coalescePerFrame(renderWorkbenchShellNow);

  function scheduleWorkspaceViewOptions() {
    return {
      conversations: state.navigationConversations,
      activeAgentId: state.agent?.id || "",
      onOpenConversation: (agentId) => openOverviewConversation(agentId).catch(showError),
    };
  }

  function renderScheduleSurface() {
    const scheduleWorkspace = getScheduleWorkspace();
    const panel = $("schedulePanel");
    const body = $("scheduleWorkspaceBody");
    if (!panel || !body) return;
    const snapshot = scheduleWorkspace.getState();
    panel.setAttribute("aria-busy", snapshot.loading || Boolean(snapshot.busy?.save) ? "true" : "false");
    body.innerHTML = scheduleWorkspace.render(scheduleWorkspaceViewOptions());
    scheduleWorkspace.bind(body, scheduleWorkspaceViewOptions());
    syncMobilePageTitle();
  }

  async function refreshPrimaryMode() {
    const overviewDashboard = getOverviewDashboard();
    const scheduleWorkspace = getScheduleWorkspace();
    const taskWorkspace = getTaskWorkspace();
    const specBoard = getSpecBoard();
    if (state.overviewActive) {
      await overviewDashboard.load({ force: true });
      return;
    }
    if (state.activeWorkbench === "schedules") {
      await scheduleWorkspace.load();
      renderScheduleSurface();
      renderProjects();
      return;
    }
    await init();
    if (state.activeWorkbench === "workbench") {
      await taskWorkspace.load();
      if (taskWorkspace.getState().scope === "agent" && state.agent?.id) await specBoard.load();
    }
    renderProjects();
  }

  return {
    setTranslatedText,
    setTranslatedAttribute,
    normalizedPrimaryWorkbench,
    currentShellRailTarget,
    renderPrimaryModeSidebar,
    renderWorkbenchShell,
    scheduleWorkspaceViewOptions,
    renderScheduleSurface,
    refreshPrimaryMode,
  };
}
