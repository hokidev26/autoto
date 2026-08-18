import { $, setTextIfChanged } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { classifyAttachmentKind } from "./attachment-glyphs.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import {
  mcpHostToolKind,
  mcpInnerToolName,
  normalizeToolActivity,
  toolActivityTarget,
} from "./chat-rendering-tools-normalize.mjs";

// Small, mostly independent helpers around the agent transport connection
// badge, chat attachment classification, the terminal dock toggle, and
// opening the task-workspace board for a given agent.
function compactActivityTarget(value, max = 28) {
  const text = String(value || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  if (text.length <= max) return text;
  return `${text.slice(0, Math.max(8, max - 1))}…`;
}

function toolActivityVerbLabel(item, translate = t) {
  const tool = item && typeof item === "object" ? normalizeToolActivity(item) : { toolName: item };
  const name = String(tool.toolName || "").toLowerCase();
  const host = mcpHostToolKind(tool.toolName);
  if (host === "list") return translate("chat.activity.listingMCP");
  if (host === "call") {
    const inner = mcpInnerToolName(tool);
    return inner
      ? translate("chat.activity.callingTool", { tool: compactActivityTarget(inner, 24) })
      : translate("chat.activity.genericStep");
  }
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
  const tool = item && typeof item === "object" ? normalizeToolActivity(item) : {};
  const target = toolActivityTarget(tool);
  if (!target) return "";
  if (/^https?:\/\//i.test(target)) {
    try {
      const url = new URL(target);
      const path = url.pathname === "/" ? "" : url.pathname;
      return compactActivityTarget(`${url.host}${path}`, 28);
    } catch {
      return compactActivityTarget(target, 28);
    }
  }
  const isCommand = /(?:^|\b)(bash|shell|terminal)(?:\b|$)/i.test(String(tool.toolName || ""));
  if (isCommand) return compactActivityTarget(target, 36);
  if (/[\\/]/.test(target) && !target.includes(" · ")) {
    return compactActivityTarget(String(target).split(/[\\/]/).pop() || target, 28);
  }
  return compactActivityTarget(target, 28);
}

// The bottom pill and the activity stack describe the same stream; a bare
// "thinking" beside a visible reasoning trail reads like an unexplained stall.
// The opening sentence of the current draft is the step's intent (the same
// slice the reasoning rows use as their title), so it stays stable while the
// step streams instead of flickering with every delta.
function liveReasoningStatusTitle(state) {
  const text = String(state?.liveReasoningDraft?.text || "").replace(/\s+/g, " ").trim();
  if (!text) return "";
  const boundary = text.search(/[。．.!！?？]/);
  return compactActivityTarget(boundary > 0 ? text.slice(0, boundary) : text, 42);
}

function thinkingActivityText(state, translate) {
  const title = liveReasoningStatusTitle(state);
  const base = translate("chat.activity.thinking");
  return title ? `${base} · ${title}` : base;
}

const terminalLiveToolStatuses = new Set([
  "completed", "complete", "succeeded", "success", "done", "error", "failed",
  "rejected", "denied", "cancelled", "canceled", "interrupted", "aborted", "superseded",
]);

function liveToolIsActive(item) {
  const status = String(item?.status || item?.state || "").trim().toLowerCase().replace(/[\s-]+/g, "_");
  return !terminalLiveToolStatuses.has(status);
}

function runningLiveTools(state) {
  const agentId = state?.agent?.id || "";
  return Object.values(state?.liveToolOutputs || {})
    .filter((item) => item && (!item.agentId || item.agentId === agentId))
    .filter(liveToolIsActive)
    .sort((a, b) => String(b.createdAt || "").localeCompare(String(a.createdAt || "")));
}

// Amber rather than the busy blue: the parent is not working, it is blocked on
// something else that is, and those read differently at a glance.
export function waitingOnBackgroundTasks(backgroundTasks, translate = t) {
  const summary = backgroundTasks?.getSummary?.();
  const running = Number(summary?.runningCount) || 0;
  const queued = Number(summary?.queuedCount) || 0;
  if (!running && !queued) return null;
  // Name the task when there is exactly one: "waiting for 1 sub-agent" says
  // nothing the count did not, while its title says which work is outstanding.
  const only = running === 1 && !queued ? activityTaskLabel(summary?.current) : "";
  return {
    kind: "waiting",
    tone: "waiting",
    text: only || (running
      ? translate("chat.activity.waitingSubagent", { count: running })
      : translate("chat.activity.waitingSubagentQueued", { count: queued })),
  };
}

function activityTaskLabel(task) {
  const title = String(task?.title || "").trim();
  return title ? compactActivityTarget(title, 32) : "";
}

// What the parent is doing locally and what it delegated are both true at once,
// and the bar had room for only the first. A turn that dispatched three
// sub-agents and then kept reading files reported just "reading foo.go", so the
// delegated work vanished from the one place the user looks for it.
export function withDelegatedActivitySuffix(activity, backgroundTasks, translate = t) {
  if (!activity) return activity;
  // Already the delegated state itself; adding its own count twice reads badly.
  if (activity.kind === "waiting") return activity;
  const summary = backgroundTasks?.getSummary?.();
  const running = Number(summary?.runningCount) || 0;
  if (running <= 0) return activity;
  const suffix = translate("chat.activity.alsoRunningSubagents", { count: running });
  if (!suffix) return activity;
  return { ...activity, text: `${activity.text} · ${suffix}` };
}

// contextCompactingForAgent reports whether the compaction notice belongs to the
// conversation on screen. A string value is an agent id and must match; anything
// else truthy is treated as unscoped.
export function contextCompactingForAgent(state) {
  const flag = state?.contextCompacting;
  if (!flag) return false;
  if (typeof flag !== "string") return true;
  const current = String(state?.agent?.id || "");
  return current !== "" && flag === current;
}

export function providerRetryFromSnapshot(value) {
  if (!value || typeof value !== "object") return null;
  const attempt = Number(value.attempt) || 0;
  const maxAttempts = Number(value.maxAttempts) || 0;
  if (attempt <= 0 && maxAttempts <= 0) return null;
  return {
    attempt: attempt > 0 ? attempt : 1,
    maxAttempts: maxAttempts > 0 ? maxAttempts : 0,
    at: Date.now(),
  };
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
  //
  // The flag carries the agent it belongs to, because a manual compaction has no
  // run behind it: nothing reaches a terminal state afterwards to clear a stray
  // value. An unscoped boolean therefore survived switching conversations and
  // left an unrelated composer claiming it was still compacting. A non-string
  // truthy value stays agent-agnostic so a caller that only knows "compacting"
  // still works.
  if (contextCompactingForAgent(state)) {
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
    const verb = toolActivityVerbLabel(tool, translate);
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
      text: hasText ? translate("chat.activity.generating") : thinkingActivityText(state, translate),
    };
  }

  const agentStatus = String(state?.agent?.status || "").trim().toLowerCase();
  if (agentStatus === "waiting") {
    return {
      kind: "waiting",
      tone: "waiting",
      text: translate("chat.activity.waitingSubagent", { count: 1 }),
    };
  }
  if (agentStatus === "running") {
    return { kind: "thinking", text: thinkingActivityText(state, translate) };
  }

  return null;
}

export function openAgentTurnIsLive(state) {
  const status = String(state?.agent?.status || "").trim().toLowerCase();
  return liveAgentStatuses.has(status) || Boolean(state?.liveAssistantActive);
}

const liveAgentStatuses = new Set(["running", "waiting"]);
const terminalAgentStatuses = new Set(["idle", "interrupted", "error", "failed", "completed"]);

// Navigation already polls agentStatus every couple of seconds. Live websocket
// events can stall on a remote tunnel, so the open conversation has to take
// that durable status or Stop/thinking stay frozen on the viewer who did not
// click.
export function reconcileOpenAgentFromNavigation({
  agent,
  conversations,
  liveAssistantActive = false,
  localTurnStartedAt = 0,
  localInterruptRequestedAt = 0,
  now = Date.now(),
  turnGraceMs = 8000,
  interruptToastMs = 4000,
} = {}) {
  const agentId = String(agent?.id || "").trim();
  const row = (conversations || []).find((item) => String(item?.agentId || "") === agentId);
  if (!agentId || !row) return { changed: false, agent, settle: null, catchUp: false };
  const nextStatus = String(row.agentStatus || "").trim().toLowerCase();
  const prevStatus = String(agent?.status || "").trim().toLowerCase();
  const catchUp = liveAgentStatuses.has(nextStatus) || liveAgentStatuses.has(prevStatus);
  if (!nextStatus || nextStatus === prevStatus) {
    return { changed: false, agent, settle: null, catchUp };
  }
  const inLocalTurnGrace = Number(localTurnStartedAt) > 0 && (now - Number(localTurnStartedAt)) < turnGraceMs;
  const localStopRecent = Number(localInterruptRequestedAt) > 0 && (now - Number(localInterruptRequestedAt)) < interruptToastMs;
  const nextIsTerminal = terminalAgentStatuses.has(nextStatus);
  const prevIsLive = liveAgentStatuses.has(prevStatus) || Boolean(liveAssistantActive);
  if (nextIsTerminal && inLocalTurnGrace) {
    return { changed: false, agent, settle: null, catchUp: true };
  }
  return {
    changed: true,
    agent: { ...agent, status: row.agentStatus },
    settle: nextIsTerminal && prevIsLive
      ? { toastOther: nextStatus === "interrupted" && !localStopRecent }
      : null,
    catchUp: liveAgentStatuses.has(String(row.agentStatus || "").trim().toLowerCase()) || catchUp,
  };
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
    const localActivity = resolveComposerActivityStatus(state, t);
    const backgroundTasks = getBackgroundTasks?.();
    // Waiting on a child is the parent's own state, but nothing local reports it:
    // dispatching returns a handle and the parent run ends, so every local signal
    // reads idle while the work is still going. The task counts are the only place
    // that knows, which is why this is resolved here rather than in
    // resolveComposerActivityStatus.
    const activity = withDelegatedActivitySuffix(localActivity, backgroundTasks, t)
      || waitingOnBackgroundTasks(backgroundTasks, t);
    // The summary sits where the user looks for "what is it doing", so it takes
    // the activity whenever it is on screen. It used to require the project
    // context, which left a plain conversation reporting no running task through
    // an entire turn. Phones used to null it as well, so Continue left the
    // header on "no running task" while Stop was already showing.
    const openId = String(state?.agent?.id || "").trim();
    if (openId && backgroundTasks?.setAgent && !backgroundTasks.state?.()?.agentId) {
      backgroundTasks.setAgent(openId);
    }
    const canRouteToSummary = Boolean(backgroundTasks?.setForegroundActivity);
    if (canRouteToSummary) backgroundTasks.setForegroundActivity(activity);
    const composerActivity = (!canRouteToSummary || isMobileAppViewport?.()) ? activity : null;
    const text = composerActivity?.text || lastConnectionStatus.text || t("chat.idle");
    // Handing the activity to the task summary must not let this pill claim the
    // workspace is idle. Both sit on the same toolbar, so a grey "idle" dot
    // beside a task summary still animating a running step reads as a bug.
    const busy = Boolean(activity);
    const ok = !busy && Boolean(lastConnectionStatus.ok);
    if (label) setTextIfChanged(label, text);
    if (dot) {
      dot.classList.toggle("ok", ok);
      dot.classList.toggle("busy", busy);
    }
    if (wrap) {
      wrap.classList.toggle("is-busy", busy);
      wrap.classList.toggle("is-ok", ok);
      if (wrap.dataset) wrap.dataset.activityTone = busy ? String(activity?.tone || activity?.kind || "") : "";
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
    return classifyAttachmentKind(file);
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
