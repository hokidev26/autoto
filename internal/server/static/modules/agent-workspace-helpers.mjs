import { $, setTextIfChanged } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";

// Small, mostly independent helpers around the agent transport connection
// badge, chat attachment classification, the terminal dock toggle, and
// opening the task-workspace board for a given agent.
function compactActivityTarget(value, max = 28) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  if (text.length <= max) return text;
  return `${text.slice(0, Math.max(8, max - 1))}…`;
}

function toolActivityVerbLabel(toolName, translate = t) {
  const name = String(toolName || "").toLowerCase();
  if (name.includes("grep") || name.includes("search") || name.includes("glob") || name.includes("web_search") || name.includes("websearch")) {
    return translate("chat.activity.searching");
  }
  if (name.includes("read") || name.includes("open_page") || name.includes("webfetch")) return translate("chat.activity.reading");
  if (name.includes("edit") || name.includes("apply_patch") || name.includes("strreplace")) return translate("chat.activity.editing");
  if (name.includes("write") || name.includes("create_file")) return translate("chat.activity.writing");
  if (name.includes("bash") || name.includes("shell") || name.includes("terminal") || name.includes("exec")) {
    return translate("chat.activity.runningCommand");
  }
  return translate("chat.activity.genericStep");
}

function toolActivityTargetLabel(item) {
  const input = item?.inputJson && typeof item.inputJson === "object"
    ? item.inputJson
    : (item?.input && typeof item.input === "object" ? item.input : {});
  const command = input.command || input.cmd;
  const filePath = input.file_path || input.filePath || input.path || input.cwd;
  const pattern = input.pattern || input.query;
  const value = input.value || input.url || input.ref_id;
  if (command) return compactActivityTarget(command, 36);
  if (filePath) return compactActivityTarget(String(filePath).split(/[\\/]/).pop() || filePath, 28);
  if (pattern) return compactActivityTarget(pattern, 28);
  if (value) return compactActivityTarget(value, 28);
  return "";
}

function runningLiveTools(state) {
  const agentId = state?.agent?.id || "";
  return Object.values(state?.liveToolOutputs || {})
    .filter((item) => item && (!item.agentId || item.agentId === agentId))
    .filter((item) => {
      const status = String(item.status || "").toLowerCase();
      return !status || ["running", "pending", "in_progress", "started", "active"].includes(status);
    })
    .sort((a, b) => String(b.createdAt || "").localeCompare(String(a.createdAt || "")));
}

export function resolveComposerActivityStatus(state, translate = t) {
  const approvals = Object.values(state?.pendingToolApprovals || {}).filter(Boolean);
  if (approvals.length) {
    const toolName = approvals[0]?.toolName || approvals[0]?.name || "";
    return {
      kind: "approval",
      text: toolName
        ? `${translate("chat.activity.awaitingApproval")} · ${compactActivityTarget(toolName, 18)}`
        : translate("chat.activity.awaitingApproval"),
    };
  }

  // Compaction calls a summary model and can take seconds. It outranks the tool
  // and thinking states because it happens *instead of* progress on the turn:
  // reporting "thinking" there is what made it look like an unexplained stall.
  if (state?.contextCompacting) {
    return { kind: "compacting", text: translate("chat.activity.compacting") };
  }

  // A provider fault being retried is not idle time and not a failure yet. Say
  // so, with the attempt count, so a 502 does not look like a hang.
  const retry = state?.providerRetry;
  if (retry) {
    const attempt = Number(retry.attempt) || 0;
    const maxAttempts = Number(retry.maxAttempts) || 0;
    return {
      kind: "retrying",
      text: attempt && maxAttempts
        ? `${translate("chat.activity.retrying")} ${attempt}/${maxAttempts}`
        : translate("chat.activity.retrying"),
    };
  }

  const [tool] = runningLiveTools(state);
  if (tool) {
    const verb = toolActivityVerbLabel(tool.toolName, translate);
    const target = toolActivityTargetLabel(tool);
    return {
      kind: "tool",
      text: target ? `${verb} ${target}` : verb,
    };
  }

  if (state?.liveAssistantActive) {
    const hasText = Boolean(String(state.liveAssistantText || "").trim());
    return {
      kind: hasText ? "generating" : "thinking",
      text: hasText ? translate("chat.activity.generating") : translate("chat.activity.thinking"),
    };
  }

  if (String(state?.agent?.status || "").toLowerCase() === "running") {
    return { kind: "thinking", text: translate("chat.activity.thinking") };
  }

  return null;
}

export function createAgentWorkspaceHelpers({
  state,
  getAgentStream,
  getBackgroundTasks,
  getTaskWorkspace,
  getSpecBoard,
  getProjectKanban,
  closeWorkspace,
  toggleTerminal,
  notifyTerminal,
  projectOperationContextActive,
  isMobileAppViewport,
  closeConversationDetails,
  renderConversationDetails,
  updateRuntimeStatusButton,
  renderWorkbenchShell,
  loadProjects,
  selectNavigationConversation,
}) {
  let lastConnectionStatus = { text: t("chat.idle"), ok: false };

  function paintComposerStatus() {
    const label = $("composerStatusText");
    const dot = $("composerStatusDot");
    const wrap = label?.closest?.(".composer-status") || document.querySelector?.(".composer-status");
    const activity = resolveComposerActivityStatus(state, t);
    const backgroundTasks = getBackgroundTasks?.();
    const routeActivityToTaskSummary = Boolean(projectOperationContextActive?.() && !isMobileAppViewport?.() && backgroundTasks?.setForegroundActivity);
    if (routeActivityToTaskSummary) backgroundTasks.setForegroundActivity(activity);
    else backgroundTasks?.setForegroundActivity?.(null);
    const composerActivity = routeActivityToTaskSummary ? null : activity;
    const text = composerActivity?.text || lastConnectionStatus.text || t("chat.idle");
    // Handing the activity to the task summary must not let this pill claim the
    // workspace is idle. Both sit on the same toolbar, so a grey "idle" dot
    // beside a task summary still animating a running step reads as a bug.
    const busy = Boolean(composerActivity) || (routeActivityToTaskSummary && Boolean(activity));
    const ok = !busy && Boolean(lastConnectionStatus.ok);
    if (label) setTextIfChanged(label, text);
    if (dot) {
      dot.classList.toggle("ok", ok);
      dot.classList.toggle("busy", busy);
    }
    if (wrap) {
      wrap.classList.toggle("is-busy", busy);
      wrap.classList.toggle("is-ok", ok);
      wrap.title = text;
      wrap.setAttribute("aria-label", text);
    }
  }

  function setComposerConnectionStatus(text, ok = false) {
    lastConnectionStatus = { text: text || t("chat.idle"), ok: Boolean(ok) };
    paintComposerStatus();
  }

  function refreshComposerActivityStatus() {
    paintComposerStatus();
  }

  function connectWS() {
    if (!state.agent?.id || state.remoteAccessFailClosed) return;
    getAgentStream().connect(state.agent.id).catch((error) => {
      if (state.agent?.id) notifyTerminal(`[warn] ${am("agentLiveSnapshotFailed", { message: error?.message || error })}\n`);
    });
  }

  function updateAgentStreamStatus(detail = {}) {
    const badge = $("wsBadge");
    const streamStatus = detail.status || "idle";
    state.agentStreamStatus = streamStatus;
    if (streamStatus === "resyncing") {
      state.workState = null;
      if ($("appShell")?.classList.contains("details-open")) renderConversationDetails();
    }
    const labels = {
      idle: ["ws idle", t("workspace.main.idle"), false],
      syncing: ["ws syncing", t("workspace.main.syncing"), false],
      resyncing: ["ws resync", t("workspace.main.recovering"), false],
      connecting: ["ws connecting", t("workspace.main.connecting"), false],
      reconnecting: ["ws reconnecting", t("workspace.main.reconnecting"), false],
      connected: [detail.resume === "replayed" ? "ws replayed" : "ws connected", t("workspace.main.connected"), true],
      offline: ["ws offline", t("workspace.main.offline"), false],
    };
    const [badgeText, composerText, ok] = labels[streamStatus] || labels.offline;
    if (badge) {
      setTextIfChanged(badge, badgeText);
      badge.classList.toggle("ok", ok);
    }
    setComposerConnectionStatus(composerText, ok);
    updateRuntimeStatusButton();
    renderWorkbenchShell();
  }

  function attachmentKind(file) {
    const type = String(file?.type || "").toLowerCase();
    const name = String(file?.name || "").toLowerCase();
    if (type.startsWith("image/")) return "image";
    if (type === "video/mp4" || type === "video/webm") return "video";
    if (type === "application/pdf" || name.endsWith(".pdf")) return "pdf";
    if (name.endsWith(".docx") || type.includes("wordprocessingml.document")) return "docx";
    if (type.startsWith("text/") || /\.(txt|md|markdown|json|jsonl|csv|tsv|log|xml|ya?ml|toml|ini|env|go|js|jsx|ts|tsx|css|html?|py|rb|rs|java|c|h|cpp|hpp|cs|php|sh|zsh|bash|sql|swift|kt|kts|dart|vue|svelte)$/i.test(name)) return "text";
    return "binary";
  }

  function attachmentIcon(kind) {
    if (kind === "image") return "🖼";
    if (kind === "video") return "VIDEO";
    if (kind === "pdf") return "PDF";
    if (kind === "docx") return "DOC";
    if (kind === "text") return "TXT";
    return "FILE";
  }

  function toggleTerminalDock(collapsed) {
    if (!projectOperationContextActive()) return false;
    if (collapsed !== true) {
      getBackgroundTasks().closeTray("terminal-open");
      closeConversationDetails();
      if (state.workspaceOpen && state.workspaceTab === "preview") closeWorkspace();
    }
    toggleTerminal(collapsed);
  }

  async function openTaskWorkspaceAgent(agent, project) {
    const agentId = String(agent?.id || "").trim();
    const projectId = String(project?.id || agent?.projectId || "").trim();
    if (!agentId || !projectId) return;
    let target = state.navigationConversations.find((conversation) => conversation.projectId === projectId && conversation.agentId === agentId);
    if (!target) {
      await loadProjects({ autoEnter: false, reason: "task-workspace-agent" });
      target = state.navigationConversations.find((conversation) => conversation.projectId === projectId && conversation.agentId === agentId);
    }
    if (!target) throw new Error(t("taskWorkspace.selectAgentFirst"));
    await selectNavigationConversation(target.targetId, { preserveSidebar: true, selectionKind: "project" });
    getTaskWorkspace().setContext({ projectId, agentId, scope: "agent" });
    await getSpecBoard().load();
    getProjectKanban().render();
  }

  return {
    setComposerConnectionStatus,
    refreshComposerActivityStatus,
    connectWS,
    updateAgentStreamStatus,
    attachmentKind,
    attachmentIcon,
    toggleTerminalDock,
    openTaskWorkspaceAgent,
  };
}
