import { t } from "./i18n.mjs";

const navigationModes = new Set(["all", "projects"]);

export const navigationRefreshDefaults = Object.freeze({
  intervalMs: 2000,
  minIntervalMs: 250,
});

function navigationRefreshTimerFunctions(timers) {
  const source = timers || globalThis;
  return {
    setTimeout: (typeof source?.setTimeout === "function" ? source.setTimeout : globalThis.setTimeout).bind(source),
    clearTimeout: (typeof source?.clearTimeout === "function" ? source.clearTimeout : globalThis.clearTimeout).bind(source),
  };
}

export function createNavigationRefreshController({
  refresh,
  shouldRefresh = () => true,
  onError,
  timers = globalThis,
  intervalMs = navigationRefreshDefaults.intervalMs,
  autoStart = false,
} = {}) {
  if (typeof refresh !== "function") throw new Error("createNavigationRefreshController requires refresh");
  const timer = navigationRefreshTimerFunctions(timers);
  const interval = Math.max(navigationRefreshDefaults.minIntervalMs, Number(intervalMs) || navigationRefreshDefaults.intervalMs);
  let started = false;
  let timerId = null;
  let scheduledReason = "interval";
  let inFlight = null;
  let pendingReason = "";

  function clearScheduled() {
    if (timerId === null) return;
    timer.clearTimeout(timerId);
    timerId = null;
  }

  function schedule(delay = interval, reason = "interval") {
    if (!started) return false;
    clearScheduled();
    scheduledReason = String(reason || "interval");
    timerId = timer.setTimeout(() => {
      timerId = null;
      run(scheduledReason);
    }, Math.max(0, Number(delay) || 0));
    return true;
  }

  function finish(operation) {
    if (inFlight !== operation) return;
    inFlight = null;
    if (!started) return;
    const nextReason = pendingReason;
    pendingReason = "";
    schedule(nextReason ? 0 : interval, nextReason || "interval");
  }

  function run(reason = "interval") {
    if (!started) return Promise.resolve(null);
    if (inFlight) {
      pendingReason = String(reason || "pending");
      return inFlight;
    }
    let allowed = false;
    try {
      allowed = shouldRefresh() !== false;
    } catch (error) {
      onError?.(error);
    }
    if (!allowed) {
      schedule(interval, "interval");
      return Promise.resolve(null);
    }
    const operation = Promise.resolve()
      .then(() => refresh({ reason: String(reason || "interval") }))
      .catch((error) => {
        onError?.(error);
        return null;
      })
      .finally(() => finish(operation));
    inFlight = operation;
    return operation;
  }

  function start({ immediate = false } = {}) {
    if (started) return false;
    started = true;
    schedule(immediate ? 0 : interval, immediate ? "start" : "interval");
    return true;
  }

  function request(reason = "manual") {
    if (!started) return false;
    if (inFlight) {
      pendingReason = String(reason || "manual");
      return true;
    }
    return schedule(0, reason);
  }

  function stop() {
    if (!started) return false;
    started = false;
    clearScheduled();
    pendingReason = "";
    return true;
  }

  async function flush() {
    if (!started) return null;
    clearScheduled();
    await run("flush");
    while (inFlight) await inFlight;
    return null;
  }

  if (autoStart) start();

  return {
    start,
    stop,
    dispose: stop,
    request,
    refreshNow: request,
    flush,
    isStarted: () => started,
    isRefreshing: () => inFlight !== null,
    intervalMs: interval,
  };
}

function text(value) {
  return String(value ?? "").trim();
}

function booleanValue(value) {
  return value === true || value === 1 || value === "1" || value === "true";
}

function compactDisplayPath(value) {
  return text(value)
    .replace(/^\/Users\/[^/]+(?=\/)/, "~")
    .replace(/^\/home\/[^/]+(?=\/)/, "~");
}

function timestamp(value) {
  const normalized = text(value);
  return normalized && !Number.isNaN(Date.parse(normalized)) ? normalized : "";
}

export function escapeNavigationHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

export function createNavigationTargetId(value = {}) {
  return [value.projectId, value.worklineId, value.agentId]
    .map((part) => encodeURIComponent(text(part)))
    .join("::");
}

export function parseNavigationTargetId(targetId) {
  const parts = text(targetId).split("::");
  if (parts.length !== 3) return null;
  try {
    const [projectId, worklineId, agentId] = parts.map((part) => decodeURIComponent(part));
    if (!agentId || !projectId || !worklineId) return null;
    return { projectId, worklineId, agentId, targetId: createNavigationTargetId({ projectId, worklineId, agentId }) };
  } catch {
    return null;
  }
}

function normalizeProject(value = {}) {
  const id = text(value.id || value.projectId);
  if (!id) return null;
  const gitPath = text(value.gitPath || value.projectPath || value.path);
  return {
    ...value,
    id,
    name: text(value.name || value.projectName) || gitPath || id,
    gitPath,
    flowMode: text(value.flowMode || value.projectFlowMode).toLocaleLowerCase() || "workspace",
    updatedAt: timestamp(value.updatedAt || value.projectUpdatedAt),
    pinned: booleanValue(value.pinned || value.projectPinned),
    archivedAt: timestamp(value.archivedAt || value.projectArchivedAt),
  };
}

function conversationContext(value = {}) {
  const context = text(value.context).toLocaleLowerCase();
  if (["conversation", "standalone"].includes(context)) return "conversation";
  if (context === "project") return "project";
  if (Object.hasOwn(value, "projectFlowMode")) return booleanValue(value.projectFlowMode) ? "project" : "conversation";
  return "project";
}

function normalizeConversation(value = {}) {
  const projectId = text(value.projectId);
  const worklineId = text(value.worklineId);
  const agentId = text(value.agentId || value.id);
  const context = conversationContext(value);
  const legacyConversation = context === "conversation";
  if (!agentId || legacyConversation || !projectId || !worklineId) return null;
  const conversation = {
    projectId,
    projectName: text(value.projectName) || projectId,
    projectPath: text(value.projectPath),
    projectUpdatedAt: timestamp(value.projectUpdatedAt),
    projectPinned: booleanValue(value.projectPinned),
    projectArchivedAt: timestamp(value.projectArchivedAt),
    worklineId,
    worklineParentId: text(value.worklineParentId),
    worklineTitle: text(value.worklineTitle) || worklineId,
    worklineRole: text(value.worklineRole),
    worklineBranch: text(value.worklineBranch),
    worklineUpdatedAt: timestamp(value.worklineUpdatedAt),
    agentId,
    agentTitle: text(value.agentTitle || value.title) || agentId,
    agentType: text(value.agentType || value.type),
    agentStatus: text(value.agentStatus || value.status),
    agentPinned: booleanValue(value.agentPinned || value.pinned),
    agentArchivedAt: timestamp(value.agentArchivedAt || value.archivedAt),
    model: text(value.model),
    permissionMode: text(value.permissionMode),
    cwd: text(value.cwd),
    messageCount: Math.max(0, Number.isFinite(Number(value.messageCount)) ? Math.trunc(Number(value.messageCount)) : 0),
    lastActivityAt: timestamp(value.lastActivityAt || value.updatedAt),
    context: "project",
    projectFlowMode: true,
  };
  return { ...conversation, targetId: createNavigationTargetId(conversation) };
}

function conversationActivity(value) {
  return Date.parse(value.lastActivityAt || value.worklineUpdatedAt || value.projectUpdatedAt || "") || 0;
}

function compareRecent(left, right) {
  const leftArchived = left.projectArchivedAt || left.agentArchivedAt ? 1 : 0;
  const rightArchived = right.projectArchivedAt || right.agentArchivedAt ? 1 : 0;
  const leftProjectPinned = left.projectPinned ? 1 : 0;
  const rightProjectPinned = right.projectPinned ? 1 : 0;
  const leftAgentPinned = left.agentPinned ? 1 : 0;
  const rightAgentPinned = right.agentPinned ? 1 : 0;
  return leftArchived - rightArchived
    || rightProjectPinned - leftProjectPinned
    || rightAgentPinned - leftAgentPinned
    || conversationActivity(right) - conversationActivity(left)
    || left.agentTitle.localeCompare(right.agentTitle);
}

function compareConversationList(left, right) {
  const leftArchived = left.projectArchivedAt || left.agentArchivedAt ? 1 : 0;
  const rightArchived = right.projectArchivedAt || right.agentArchivedAt ? 1 : 0;
  const leftPinned = left.agentPinned ? 1 : 0;
  const rightPinned = right.agentPinned ? 1 : 0;
  return leftArchived - rightArchived
    || rightPinned - leftPinned
    || conversationActivity(right) - conversationActivity(left)
    || left.agentTitle.localeCompare(right.agentTitle);
}

function projectGroupActivity(group) {
  return Math.max(
    Date.parse(group?.project?.updatedAt || "") || 0,
    ...(group?.conversations || []).map((conversation) => conversationActivity(conversation)),
  );
}

function compareProjectGroups(left, right) {
  const leftArchived = left.project.archivedAt ? 1 : 0;
  const rightArchived = right.project.archivedAt ? 1 : 0;
  const leftPinned = left.project.pinned ? 1 : 0;
  const rightPinned = right.project.pinned ? 1 : 0;
  return leftArchived - rightArchived
    || rightPinned - leftPinned
    || projectGroupActivity(right) - projectGroupActivity(left)
    || left.project.name.localeCompare(right.project.name);
}

function applyProjectItemOrder(items, orderArr, projectId) {
  if (!Array.isArray(orderArr) || !orderArr.length) return items;
  const orderMap = new Map(orderArr.map((id, index) => [String(id), index]));
  return [...items].sort((left, right) => {
    const leftIndex = orderMap.has(projectId(left)) ? orderMap.get(projectId(left)) : Infinity;
    const rightIndex = orderMap.has(projectId(right)) ? orderMap.get(projectId(right)) : Infinity;
    return leftIndex - rightIndex;
  });
}

export function applyProjectOrder(projects, orderArr) {
  return applyProjectItemOrder(projects, orderArr, (project) => text(project?.id));
}

function applyProjectGroupOrder(groups, orderArr) {
  return applyProjectItemOrder(groups, orderArr, (group) => text(group?.project?.id));
}

export function normalizeNavigationPayload(payload = {}) {
  // The server already omits legacy conversation-flow containers. Repeat the
  // boundary here so stale caches or older proxies cannot reintroduce them into
  // the interactive project navigation.
  const projects = (Array.isArray(payload.projects) ? payload.projects : [])
    .map(normalizeProject)
    .filter((project) => project && project.flowMode !== "conversation");
  const projectIds = new Set(projects.map((project) => project.id));
  const seenConversationTargets = new Set();
  const conversations = (Array.isArray(payload.conversations) ? payload.conversations : [])
    .map(normalizeConversation)
    .filter(Boolean)
    .sort(compareRecent)
    .filter((conversation) => {
      if (seenConversationTargets.has(conversation.targetId)) return false;
      seenConversationTargets.add(conversation.targetId);
      return true;
    });

  conversations.forEach((conversation) => {
    if (projectIds.has(conversation.projectId)) return;
    projectIds.add(conversation.projectId);
    projects.push(normalizeProject({
      id: conversation.projectId,
      name: conversation.projectName,
      gitPath: conversation.projectPath,
      updatedAt: conversation.projectUpdatedAt,
      pinned: conversation.projectPinned,
      archivedAt: conversation.projectArchivedAt,
      flowMode: "workspace",
    }));
  });

  return { projects, conversations };
}

function normalizedQuery(query) {
  return text(query).toLocaleLowerCase();
}

function includesQuery(values, query) {
  if (!query) return true;
  return values.some((value) => text(value).toLocaleLowerCase().includes(query));
}

export function conversationMatchesSearch(conversation, query) {
  return includesQuery([
    conversation.projectName,
    conversation.projectPath,
    conversation.worklineTitle,
    conversation.worklineRole,
    conversation.worklineBranch,
    conversation.agentTitle,
    conversation.model,
  ], normalizedQuery(query));
}

export function projectMatchesSearch(project, conversations, query) {
  const normalized = normalizedQuery(query);
  if (!normalized) return true;
  return includesQuery([project.name, project.gitPath], normalized)
    || conversations.some((conversation) => conversation.projectId === project.id && conversationMatchesSearch(conversation, normalized));
}

export function buildNavigationView(payload = {}, options = {}) {
  const normalized = normalizeNavigationPayload(payload);
  const mode = navigationModes.has(options.mode) ? options.mode : "projects";
  const query = normalizedQuery(options.query);
  const conversationsByProject = new Map();
  normalized.conversations.forEach((conversation) => {
    const items = conversationsByProject.get(conversation.projectId) || [];
    items.push(conversation);
    conversationsByProject.set(conversation.projectId, items);
  });

  const projects = normalized.projects.filter((project) => projectMatchesSearch(project, normalized.conversations, query));
  const groups = projects.map((project) => {
    const conversations = conversationsByProject.get(project.id) || [];
    const projectOwnMatch = includesQuery([project.name, project.gitPath], query);
    return {
      project,
      conversations: !query || projectOwnMatch
        ? conversations
        : conversations.filter((conversation) => conversationMatchesSearch(conversation, query)),
    };
  });

  // Sort groups so the project with the most recent message activity floats to
  // the top. Project pin/archive state still forms the outer boundary. A
  // user-defined drag order is applied later and takes precedence.
  groups.sort(compareProjectGroups);
  const orderedProjects = groups.map((group) => group.project);

  return {
    mode,
    query,
    totalProjectCount: normalized.projects.length,
    totalConversationCount: normalized.conversations.length,
    projects: orderedProjects,
    conversations: [],
    groups,
  };
}

export function resolveTopNavigationProjectId(payload = {}, options = {}) {
  const view = buildNavigationView(payload, { mode: "projects", query: options.query });
  return text(applyProjectOrder(view.projects, options.projectOrder)[0]?.id);
}

export function normalizeRecentConversations(value, limit = 8) {
  const items = Array.isArray(value) ? value : [];
  const seen = new Set();
  return items.flatMap((entry) => {
    const parsed = parseNavigationTargetId(typeof entry === "string" ? entry : entry?.targetId);
    // Projectless targets are legacy standalone conversations. Keep the raw
    // records in storage untouched, but never let them re-enter the app after a
    // refresh or cross-tab storage event.
    if (!parsed?.projectId || !parsed?.worklineId || seen.has(parsed.targetId)) return [];
    seen.add(parsed.targetId);
    const openedAt = timestamp(typeof entry === "object" ? (entry.openedAt || entry.lastOpenedAt) : "");
    return [{ targetId: parsed.targetId, openedAt }];
  }).slice(0, Math.max(0, limit));
}

export function createRecentConversationSyncController({
  key,
  onChange,
  window: windowImpl = globalThis.window,
  storage: storageImpl = globalThis.localStorage,
  limit = 8,
  autoStart = true,
} = {}) {
  const storageKey = String(key || "").trim();
  if (!storageKey) throw new Error("createRecentConversationSyncController requires key");
  if (typeof onChange !== "function") throw new Error("createRecentConversationSyncController requires onChange");
  let started = false;

  function handleStorage(event = {}) {
    if (String(event.key || "") !== storageKey) return false;
    if (event.storageArea && storageImpl && event.storageArea !== storageImpl) return false;
    let value = [];
    if (event.newValue !== null && event.newValue !== undefined && event.newValue !== "") {
      try {
        value = JSON.parse(event.newValue);
      } catch {
        return false;
      }
      if (!Array.isArray(value)) return false;
    }
    onChange(normalizeRecentConversations(value, limit), { reason: "storage", key: storageKey });
    return true;
  }

  function start() {
    if (started || typeof windowImpl?.addEventListener !== "function") return false;
    started = true;
    windowImpl.addEventListener("storage", handleStorage);
    return true;
  }

  function stop() {
    if (!started) return false;
    started = false;
    windowImpl?.removeEventListener?.("storage", handleStorage);
    return true;
  }

  if (autoStart) start();

  return {
    start,
    stop,
    dispose: stop,
    handleStorage,
    isStarted: () => started,
  };
}

export function resolveInitialNavigationTarget(recent, conversations) {
  const knownTargets = new Set((Array.isArray(conversations) ? conversations : [])
    .map((conversation) => text(conversation?.targetId))
    .filter((targetId) => {
      const parsed = parseNavigationTargetId(targetId);
      return Boolean(parsed?.projectId && parsed?.worklineId);
    }));
  const recentMatch = normalizeRecentConversations(recent)
    .find((entry) => knownTargets.has(entry.targetId));
  if (recentMatch) return recentMatch.targetId;
  return (Array.isArray(conversations) ? conversations : [])
    .map((conversation) => text(conversation?.targetId))
    .find((targetId) => knownTargets.has(targetId)) || "";
}

export function addRecentConversation(recent, target, openedAt = new Date().toISOString(), limit = 8) {
  const targetId = typeof target === "string" ? parseNavigationTargetId(target)?.targetId : createNavigationTargetId(target);
  const parsed = parseNavigationTargetId(targetId);
  if (!parsed?.projectId || !parsed?.worklineId) return normalizeRecentConversations(recent, limit);
  return normalizeRecentConversations([
    { targetId, openedAt: timestamp(openedAt) || new Date().toISOString() },
    ...normalizeRecentConversations(recent, Number.MAX_SAFE_INTEGER).filter((entry) => entry.targetId !== targetId),
  ], limit);
}

export function navigationAgentStatusClass(value) {
  return text(value).toLocaleLowerCase().replace(/[^a-z0-9_-]+/g, "-").replace(/^-+|-+$/g, "") || "idle";
}

// Project rows inherit the liveliest agent under them so the sidebar icon can
// still signal "running / done" when the list is in projects-only mode.
export function aggregateNavigationAgentStatus(conversations = []) {
  const list = Array.isArray(conversations) ? conversations : [];
  if (!list.length) return "";
  const statuses = list.map((item) => navigationAgentStatusClass(item?.agentStatus || item?.status));
  if (statuses.some((status) => status === "running" || status === "pending" || status === "queued")) return "running";
  if (statuses.some((status) => status === "error" || status === "failed")) return "error";
  return "idle";
}

function navigationStateMarkup({ pinned = false, archivedAt = "" } = {}) {
  const marks = [];
  if (pinned) {
    marks.push(`<span class="navigation-state-badge pinned" title="${escapeNavigationHtml(t("shell.pinned"))}" aria-label="${escapeNavigationHtml(t("shell.pinned"))}">P</span>`);
  }
  if (archivedAt) {
    marks.push(`<span class="navigation-state-badge archived" title="${escapeNavigationHtml(t("shell.archived"))}" aria-label="${escapeNavigationHtml(t("shell.archived"))}">A</span>`);
  }
  return marks.join("");
}

function navigationMoreTrigger(kind, id) {
  const label = t("shell.navigationActions");
  return `<button class="navigation-row-actions" type="button" data-navigation-menu-trigger data-navigation-kind="${escapeNavigationHtml(kind)}" data-navigation-id="${escapeNavigationHtml(id)}" aria-haspopup="menu" aria-label="${escapeNavigationHtml(label)}" title="${escapeNavigationHtml(label)}">…</button>`;
}

// Deliberately distinct from the sidebar-header "+", which opens the project
// directory flow. This one only ever forks the project it sits on into a new
// git branch + worktree, so its label says so rather than saying "new".
function navigationForkTrigger(projectId) {
  const label = t("shell.newWorkline");
  return `<button class="navigation-row-fork" type="button" data-project-fork-trigger data-project-id-fork="${escapeNavigationHtml(projectId)}" aria-label="${escapeNavigationHtml(label)}" title="${escapeNavigationHtml(label)}">+</button>`;
}

// Which conversation gives a project row its name. The open one wins so the row
// matches the header the reader is looking at; otherwise the list is already in
// recency order, so the first entry is the most recent.
function navigationProjectHeadline(conversations, activeAgentId) {
  const items = Array.isArray(conversations) ? conversations : [];
  if (!items.length) return "";
  const activeId = text(activeAgentId);
  const open = activeId ? items.find((item) => item?.agentId === activeId) : null;
  return text((open || items[0])?.agentTitle);
}

function renderProject(project, activeProjectId, options = {}) {
  const active = options.activeSelectionKind !== "conversation" && project.id === activeProjectId;
  const path = project.gitPath || project.id;
  const displayPath = compactDisplayPath(path);
  // A project's name is normally its directory, so the row used to read as the
  // same path twice: once as the title and once as the meta line underneath.
  // Prefer the conversation the row actually represents, which is what the
  // header shows and what the reader recognises the row by.
  const headline = text(options.headline) || project.name;
  const counts = options.taskCounts?.[project.id] || {};
  const activeTasks = Number(counts.todo || 0) + Number(counts.doing || 0) + Number(counts.blocked || 0);
  const taskMeta = options.taskContext
    ? `<span class="project-task-counts"><span>${escapeNavigationHtml(String(activeTasks))}</span>${Number(counts.blocked || 0) ? `<span class="blocked">${escapeNavigationHtml(String(counts.blocked))}</span>` : ""}</span>`
    : "";
  const statusClass = text(options.agentStatus) ? navigationAgentStatusClass(options.agentStatus) : "";
  const stateClass = `${project.pinned ? "pinned " : ""}${project.archivedAt ? "archived " : ""}`;
  const stateMeta = navigationStateMarkup({ pinned: project.pinned, archivedAt: project.archivedAt });
  const icon = `<svg viewBox="0 0 20 20"><path d="M5 4.5h10a2 2 0 0 1 2 2V12a2 2 0 0 1-2 2H9l-4 2.5V14a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2Z"></path></svg>`;
  return `
    <div class="navigation-conversation-row navigation-project-row ${options.taskContext ? "task-context " : ""}${active ? "active " : ""}${statusClass ? `status-${statusClass} ` : ""}project-card ${stateClass}" role="button" tabindex="0" draggable="true"       title="${escapeNavigationHtml(headline)}" data-project-id="${escapeNavigationHtml(project.id)}" data-navigation-kind="project" data-navigation-id="${escapeNavigationHtml(project.id)}"${statusClass ? ` data-agent-status="${escapeNavigationHtml(statusClass)}"` : ""} data-navigation-context="${options.taskContext ? "tasks" : "project"}">
      <span class="navigation-agent-icon theme-icon-slot" data-theme-icon-slot="sidebar-project" aria-hidden="true">${icon}</span>
      <span class="navigation-conversation-main">
        <span class="navigation-conversation-title navigation-project-title"><span class="project-kind-badge">PROJECT</span><span class="project-name">${escapeNavigationHtml(headline)}</span>${stateMeta}</span>
        <span class="navigation-conversation-meta project-path" title="${escapeNavigationHtml(path)}">${escapeNavigationHtml(displayPath)}</span>
      </span>
      ${taskMeta}
      ${options.taskContext ? "" : navigationForkTrigger(project.id)}
    </div>`;
}

function renderConversation(conversation, activeAgentId, nested = false, options = {}) {
  const active = options.activeSelectionKind !== "project" && conversation.agentId === activeAgentId;
  const taskContext = options.taskContext === true;
  const nestedFork = options.nestedFork === true;
  const statusClass = navigationAgentStatusClass(conversation.agentStatus);
  const worklineContext = conversation.worklineBranch || conversation.worklineTitle;
  const projectContext = compactDisplayPath(conversation.projectPath) || conversation.projectName;
  const context = nested
    ? worklineContext
    : [projectContext, worklineContext].filter((value, index, items) => value && items.indexOf(value) === index).join(" / ");
  const metaParts = [context, conversation.model, conversation.agentStatus];
  if (!taskContext) metaParts.push(t("workspace.navigation.messageCount", { count: conversation.messageCount }));
  const meta = metaParts.filter(Boolean).join(" · ");
  const stateClass = `${conversation.agentPinned ? "pinned " : ""}${conversation.agentArchivedAt ? "archived " : ""}`;
  const stateMeta = navigationStateMarkup({ pinned: conversation.agentPinned, archivedAt: conversation.agentArchivedAt });
  const orderScope = text(options.orderScope);
  // Fork conversations use a branch icon instead of the default conversation bubble.
  const icon = taskContext
    ? `<svg viewBox="0 0 20 20"><circle cx="10" cy="6.5" r="3"></circle><path d="M4.5 17c.7-3.5 2.5-5.2 5.5-5.2s4.8 1.7 5.5 5.2"></path></svg>`
    : nestedFork
      ? `<svg viewBox="0 0 20 20"><circle cx="7" cy="5" r="1.8"></circle><circle cx="7" cy="15" r="1.8"></circle><circle cx="14" cy="8" r="1.8"></circle><path d="M7 6.8v6.4M7 6.8c2 0 7 .5 7 1.2" stroke-linecap="round"></path></svg>`
      : `<svg viewBox="0 0 20 20"><path d="M5 4.5h10a2 2 0 0 1 2 2V12a2 2 0 0 1-2 2H9l-4 2.5V14a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2Z"></path></svg>`;
  return `
    <div class="navigation-conversation-row ${nested ? "nested " : ""}${nestedFork ? "fork-conversation " : ""}${taskContext ? "task-context " : ""}${active ? "active " : ""}status-${statusClass} ${stateClass}" role="button" tabindex="0" draggable="true" title="${escapeNavigationHtml(conversation.agentTitle)}" data-navigation-target="${escapeNavigationHtml(conversation.targetId)}" data-navigation-kind="conversation" data-navigation-id="${escapeNavigationHtml(conversation.agentId)}" data-agent-status="${escapeNavigationHtml(conversation.agentStatus || "idle")}" data-navigation-context="${taskContext ? "tasks" : "project"}"${orderScope ? ` data-conversation-order-scope="${escapeNavigationHtml(orderScope)}"` : ""}>
      <span class="navigation-agent-icon theme-icon-slot" data-theme-icon-slot="${nestedFork ? "sidebar-fork" : "sidebar-conversation"}" aria-hidden="true">${icon}</span>
      <span class="navigation-conversation-main">
        <span class="navigation-conversation-title"><span class="navigation-title-text">${escapeNavigationHtml(conversation.agentTitle)}</span>${stateMeta}</span>
        <span class="navigation-conversation-meta" title="${escapeNavigationHtml(meta)}">${escapeNavigationHtml(meta)}</span>
      </span>
      ${navigationMoreTrigger("conversation", conversation.agentId)}
    </div>`;
}

export function applyConversationOrder(conversations, orderArr) {
  if (!Array.isArray(orderArr) || !orderArr.length) return conversations;
  const orderMap = new Map(orderArr.map((id, i) => [String(id), i]));
  const copy = [...conversations];
  copy.sort((a, b) => {
    const ia = orderMap.has(a.agentId) ? orderMap.get(a.agentId) : Infinity;
    const ib = orderMap.has(b.agentId) ? orderMap.get(b.agentId) : Infinity;
    return ia - ib;
  });
  return copy;
}

export function renderNavigationHTML(view = {}, options = {}) {
  const mode = navigationModes.has(view.mode) ? view.mode : "projects";
  const activeProjectId = text(options.activeProjectId);
  const activeAgentId = text(options.activeAgentId);
  const taskContext = options.taskContext === true;
  const activeSelectionKind = taskContext
    ? "project"
    : options.activeSelectionKind === "project" || options.activeSelectionKind === "conversation"
      ? options.activeSelectionKind
      : activeAgentId ? "conversation" : "project";
  let html = "";
  if (taskContext) {
    const taskConversations = new Map((view.groups || []).map((group) => [group.project.id, group.conversations]));
    html = (view.projects || []).map((project) => renderProject(project, activeProjectId, {
      taskContext: true,
      taskCounts: options.taskCounts,
      activeSelectionKind,
      headline: navigationProjectHeadline(taskConversations.get(project.id), activeAgentId),
    })).join("");
  } else if (mode === "all") {
    // Apply user-defined project order (drag-to-reorder, persisted in localStorage).
    const groups = applyProjectGroupOrder(view.groups || [], options.projectOrder);
    const groupsHTML = groups.map((group) => {
      const orderedConvs = options.conversationOrders?.[group.project.id]
        ? applyConversationOrder(group.conversations, options.conversationOrders[group.project.id])
        : group.conversations;

      // Split into root workline conversations and fork conversations. Root
      // conversations keep their existing position; fork conversations are
      // rendered as nested children immediately below their root sibling.
      // When a project only has one workline (no forks), this is a no-op.
      //
      // "Fork child" is the narrow case: it must point at a parent workline.
      // Everything else is a root row, so a conversation with an unexpected
      // worklineRole can never silently vanish from the sidebar.
      const isForkChild = (c) => c.worklineRole !== "root" && Boolean(c.worklineBranch) && Boolean(c.worklineParentId);
      const rootConvs = orderedConvs.filter((c) => !isForkChild(c));
      const forksByWorklineId = new Map();
      orderedConvs.forEach((c) => {
        if (!isForkChild(c)) return;
        const list = forksByWorklineId.get(c.worklineParentId) || [];
        list.push(c);
        forksByWorklineId.set(c.worklineParentId, list);
      });
      const hasForks = forksByWorklineId.size > 0;
      // A fork whose parent is missing from this project would otherwise be
      // dropped; render it as a plain row so nothing is lost.
      const rootWorklineIds = new Set(rootConvs.map((c) => c.worklineId).filter(Boolean));
      const orphanForks = [...forksByWorklineId.entries()]
        .filter(([parentId]) => !rootWorklineIds.has(parentId))
        .flatMap(([, list]) => list);

      const convsHTML = [
        ...rootConvs.map((conversation) => {
          const rootHTML = renderConversation(conversation, activeAgentId, true, { activeSelectionKind, orderScope: group.project.id });
          if (!hasForks) return rootHTML;
          const forks = forksByWorklineId.get(conversation.worklineId) || [];
          if (!forks.length) return rootHTML;
          const forksHTML = forks.map((fork) =>
            renderConversation(fork, activeAgentId, true, { activeSelectionKind, orderScope: group.project.id, nestedFork: true }),
          ).join("");
          return `${rootHTML}<div class="navigation-workline-forks">${forksHTML}</div>`;
        }),
        ...orphanForks.map((fork) =>
          renderConversation(fork, activeAgentId, true, { activeSelectionKind, orderScope: group.project.id, nestedFork: true }),
        ),
      ].join("");

      const projectStatus = aggregateNavigationAgentStatus(group.conversations);
      return `
      <section class="navigation-project-group" draggable="true" data-navigation-project-group="${escapeNavigationHtml(group.project.id)}" data-conversation-count="${escapeNavigationHtml(String(group.conversations.length))}" data-navigation-context="project">
        ${renderProject(group.project, activeProjectId, {
          activeSelectionKind,
          agentStatus: projectStatus,
          // The open conversation names the row when it belongs to this project;
          // otherwise the most recent one does. Falls back to the project name
          // inside renderProject when the group has no conversations yet.
          headline: navigationProjectHeadline(group.conversations, activeAgentId),
        })}
        <div class="navigation-project-conversations" data-project-conversations="${escapeNavigationHtml(group.project.id)}">
          ${convsHTML}
        </div>
      </section>`;
    }).join("");
    html = groupsHTML;
  } else if (mode === "projects") {
    // Projects-only mode stays a flat list of project rows: no group sections
    // and no conversation structure, which is what the task sidebar relies on.
    // The drag handlers therefore resolve a project from the row itself here,
    // not from a wrapping group. Agent status is still rolled up from groups so
    // the bubble icon can turn blue while a project agent is running.
    const projects = applyProjectOrder(view.projects || [], options.projectOrder);
    const statusByProject = new Map((view.groups || []).map((group) => [group.project.id, aggregateNavigationAgentStatus(group.conversations)]));
    // Flat rows carry no nested conversation underneath, so without this the row
    // is named by the project -- normally its directory -- and reads as the same
    // path twice, once as the title and once as the meta line below it.
    const conversationsByProject = new Map((view.groups || []).map((group) => [group.project.id, group.conversations]));
    html = projects.map((project) => renderProject(project, activeProjectId, {
      activeSelectionKind,
      agentStatus: statusByProject.get(project.id) || "",
      headline: navigationProjectHeadline(conversationsByProject.get(project.id), activeAgentId),
    })).join("");
  }
  if (html) return html;
  if (view.query) return `<div class="empty-list">${escapeNavigationHtml(t("workspace.navigation.noResults", { query: view.query }))}</div>`;
  if (!view.totalProjectCount && taskContext) {
    return `<div class="navigation-boundary-empty" data-task-project-boundary="true">
      <strong>${escapeNavigationHtml(t("workbench.noProjectsTitle"))}</strong>
      <span>${escapeNavigationHtml(t("workbench.noProjectsDescription"))}</span>
      <button type="button" data-primary-workbench-target="conversation">${escapeNavigationHtml(t("workbench.goToConversation"))}</button>
    </div>`;
  }
  if (!view.totalProjectCount) {
    return `
      <button class="project-card project-card-empty" type="button" data-open-directory-shortcut="new">
        <span class="project-card-main">
          <span class="project-name">${escapeNavigationHtml(t("workspace.navigation.chooseFolder"))}</span>
          <span class="project-path">${escapeNavigationHtml(t("workspace.navigation.chooseFolderHint"))}</span>
        </span>
      </button>`;
  }
  return `<div class="empty-list">${escapeNavigationHtml(t("workspace.navigation.noProjects"))}</div>`;
}

export function renderRecentConversationsHTML(recent, conversations, activeAgentId = "") {
  const groupedTargets = new Set((Array.isArray(conversations) ? conversations : [])
    .map((conversation) => text(conversation?.targetId))
    .filter(Boolean));
  const duplicateCount = normalizeRecentConversations(recent)
    .filter((entry) => groupedTargets.has(entry.targetId)).length;
  // Project groups are the canonical location for every known conversation.
  // The legacy recent container stays present for app-main compatibility, but it
  // no longer repeats rows that are already available beneath their projects.
  return `<div class="recent-empty recent-conversations-deduplicated" data-recent-conversations-deduplicated="true" data-deduplicated-count="${escapeNavigationHtml(String(duplicateCount))}">${escapeNavigationHtml(t("workspace.navigation.noRecentConversations"))}</div>`;
}
