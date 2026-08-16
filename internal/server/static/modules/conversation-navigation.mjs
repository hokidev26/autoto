import { t } from "./i18n.mjs";
import { conversationUnread } from "./conversation-seen.mjs";

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

// Conversations created before titles were derived from the opening message had
// their working directory persisted into the title column, so the row rendered
// the same path twice: once as the title and once as the meta line underneath.
// renderProject already avoids that for project rows; this is the conversation
// equivalent. Presentational only -- the stored title is left untouched, and the
// folder name is what a reader would call the directory anyway.
function displayFolderName(value) {
  const candidate = text(value);
  if (!looksLikeFilesystemPath(candidate)) return candidate;
  return pathBasename(candidate) || candidate;
}

export function conversationDisplayTitle(conversation) {
  return displayFolderName(conversation?.agentTitle);
}

// Folder rows keep the filesystem path on hover. Conversation rows show the
// git branch when the workline has one (forks look like autoto/fork-of-main-c39bc123),
// and fall back to the stored title when they are still on an unnamed root.
export function conversationHoverTitle(conversation) {
  return text(conversation?.worklineBranch) || text(conversation?.agentTitle);
}

function looksLikeFilesystemPath(value) {
  const candidate = text(value);
  if (!candidate) return false;
  // A Windows drive prefix, a POSIX absolute path, or a UNC share. A title that
  // merely mentions a slash (\"fix a/b handling\") is not treated as a path.
  return /^[A-Za-z]:[\\/]/.test(candidate) || /^\/[^/]/.test(candidate) || /^\\\\/.test(candidate);
}

function pathBasename(value) {
  const parts = text(value).split(/[\\/]+/).filter(Boolean);
  const last = parts.length ? parts[parts.length - 1] : "";
  // A drive root ("C:\") splits to just the drive letter, which is not a folder
  // name. Report nothing so the caller keeps the original string.
  return /^[A-Za-z]:$/.test(last) ? "" : last;
}

function timestamp(value) {
  const normalized = text(value);
  return normalized && !Number.isNaN(Date.parse(normalized)) ? normalized : "";
}

// Compact age for the sidebar's right edge, the way Cursor writes 31m / 3h.
// Units stay Latin so a narrow column stays even across locales.
export function formatCompactRelativeTime(value, now = Date.now()) {
  const then = Date.parse(timestamp(value));
  if (Number.isNaN(then)) return "";
  const delta = Math.max(0, Number(now) - then);
  const minute = 60 * 1000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (delta < minute) return "now";
  if (delta < hour) return `${Math.floor(delta / minute)}m`;
  if (delta < day) return `${Math.floor(delta / hour)}h`;
  if (delta < 7 * day) return `${Math.floor(delta / day)}d`;
  try {
    return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric" }).format(new Date(then));
  } catch {
    return "";
  }
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

// Pinned stays above normal, archived stays below both. A stored drag order
// must reorder rows *within* those tiers only: sorting purely by stored index
// let one past drag outrank pinning, so a pinned project stopped sitting at the
// top once the user had ever reordered the sidebar.
function projectOrderTier(pinned, archived) {
  if (archived) return 2;
  return pinned ? 0 : 1;
}

// "Fresh activity" = the agent is working right now, or it left a reply the
// user has not looked at yet -- the same signal as the unread mark. Rows with
// fresh activity float above the stored drag order inside their tier, ordered
// by the recency the caller already sorted them in; once the run ends and the
// reply is read, the row settles back into its dragged position. This is the
// compromise between chat-style "latest bubbles up" and a fully manual order.
export function conversationHasFreshActivity(conversation, seenMap = {}) {
  const status = text(conversation?.agentStatus).toLocaleLowerCase();
  // "waiting" counts: a conversation parked on a subagent is mid-task, not done.
  if (status === "running" || status === "pending" || status === "queued" || status === "waiting") return true;
  return conversationUnread(conversation, seenMap);
}

function applyProjectItemOrder(items, orderArr, projectId, tierOf, isFresh) {
  if (!Array.isArray(orderArr) || !orderArr.length) return items;
  const orderMap = new Map(orderArr.map((id, index) => [String(id), index]));
  const indexOf = (item) => (orderMap.has(projectId(item)) ? orderMap.get(projectId(item)) : Infinity);
  const freshOf = typeof isFresh === "function" ? (item) => (isFresh(item) ? 1 : 0) : () => 0;
  // Array.prototype.sort is stable, so items absent from the stored order --
  // and fresh items among themselves -- keep the recency order they arrived in
  // instead of being shuffled arbitrarily.
  return [...items].sort((left, right) => {
    const tier = tierOf(left) - tierOf(right);
    if (tier) return tier;
    const leftFresh = freshOf(left);
    const rightFresh = freshOf(right);
    if (leftFresh || rightFresh) return rightFresh - leftFresh;
    return indexOf(left) - indexOf(right);
  });
}

export function applyProjectOrder(projects, orderArr, isFresh) {
  return applyProjectItemOrder(
    projects,
    orderArr,
    (project) => text(project?.id),
    (project) => projectOrderTier(project?.pinned, project?.archivedAt),
    isFresh,
  );
}

function groupFreshness(seenMap) {
  if (!seenMap) return undefined;
  return (group) => (group?.conversations || []).some((conversation) => conversationHasFreshActivity(conversation, seenMap));
}

function applyProjectGroupOrder(groups, orderArr, seenMap) {
  return applyProjectItemOrder(
    groups,
    orderArr,
    (group) => text(group?.project?.id),
    (group) => projectOrderTier(group?.project?.pinned, group?.project?.archivedAt),
    groupFreshness(seenMap),
  );
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
  // Subagents belong to the background-task panel, not the sidebar. A parent
  // turn can dispatch several at once, and they reuse the parent's workline, so
  // the tree cannot express them as anything but plain sibling rows -- which is
  // what made the sidebar look like it was opening branches. Git worklines are
  // first-level rows under the project; extra conversations on the same
  // workline nest underneath that row.
  //
  // Filtered at view-build time rather than in /api/navigation on purpose:
  // navigateToAgent resolves a child's targetId out of the unfiltered
  // state.navigationConversations projection, so removing subagents upstream
  // would break the panel's own "open subagent" button.
  const visibleConversations = normalized.conversations.filter((conversation) => conversation.agentType !== "subagent");
  const conversationsByProject = new Map();
  visibleConversations.forEach((conversation) => {
    const items = conversationsByProject.get(conversation.projectId) || [];
    items.push(conversation);
    conversationsByProject.set(conversation.projectId, items);
  });

  const projects = normalized.projects.filter((project) => projectMatchesSearch(project, visibleConversations, query));
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
    totalConversationCount: visibleConversations.length,
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

// The meta line spells out what the conversation is doing right now, in the
// reader's language. Raw backend statuses ("idle", "running") used to leak
// into the sidebar untranslated and said nothing about a parked parent.
const navigationStatusLabelKeys = Object.freeze({
  running: "workspace.navigation.statusRunning",
  pending: "workspace.navigation.statusRunning",
  queued: "workspace.navigation.statusRunning",
  waiting: "workspace.navigation.statusWaiting",
  error: "workspace.navigation.statusError",
  failed: "workspace.navigation.statusError",
  interrupted: "workspace.navigation.statusInterrupted",
  idle: "workspace.navigation.statusIdle",
  completed: "workspace.navigation.statusIdle",
});

export function navigationStatusLabel(value) {
  const key = navigationStatusLabelKeys[navigationAgentStatusClass(value)];
  return key ? t(key) : text(value);
}

// A collapsed group hides its rows, so the project row has to carry the unread
// mark for the conversations underneath it or a finished reply would be
// invisible until the user expanded the group.
export function aggregateNavigationUnread(conversations = [], seenMap = {}, activeAgentId = "") {
  const list = Array.isArray(conversations) ? conversations : [];
  return list.some((conversation) => conversation?.agentId !== activeAgentId && conversationUnread(conversation, seenMap));
}

// Project rows inherit the liveliest agent under them so the sidebar icon can
// still signal "running / done" when the list is in projects-only mode.
export function aggregateNavigationAgentStatus(conversations = []) {
  const list = Array.isArray(conversations) ? conversations : [];
  if (!list.length) return "";
  const statuses = list.map((item) => navigationAgentStatusClass(item?.agentStatus || item?.status));
  if (statuses.some((status) => status === "running" || status === "pending" || status === "queued")) return "running";
  if (statuses.some((status) => status === "waiting")) return "waiting";
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

function isGitWorklineFork(conversation) {
  return conversation.worklineRole !== "root" && Boolean(conversation.worklineBranch) && Boolean(conversation.worklineParentId);
}

function pickWorklineHead(members) {
  if (members.length === 1) return members[0];
  const named = members.find((item) => text(item.agentTitle) === text(item.worklineTitle));
  if (named) return named;
  let head = members[0];
  let best = Number(head.messageCount) || 0;
  members.forEach((item) => {
    const count = Number(item.messageCount) || 0;
    if (count > best) {
      head = item;
      best = count;
    }
  });
  if (best > 0) return head;
  return members[members.length - 1];
}

// One first-level row per workline. Extra primary agents on the same workline
// nest underneath, which is how a git branch can hold more than one conversation.
function clusterConversationsByWorkline(conversations) {
  const membersByWorkline = new Map();
  (Array.isArray(conversations) ? conversations : []).forEach((conversation) => {
    const id = text(conversation?.worklineId);
    if (!id) return;
    const list = membersByWorkline.get(id) || [];
    list.push(conversation);
    membersByWorkline.set(id, list);
  });
  const seen = new Set();
  const clusters = [];
  (Array.isArray(conversations) ? conversations : []).forEach((conversation) => {
    const id = text(conversation?.worklineId);
    if (!id || seen.has(id)) return;
    seen.add(id);
    const members = membersByWorkline.get(id) || [conversation];
    const head = pickWorklineHead(members);
    clusters.push({
      head,
      children: members.filter((item) => item.agentId !== head.agentId),
    });
  });
  return clusters;
}

function navigationWorklineConversationTrigger(worklineId) {
  const label = t("shell.newConversation");
  return `<button class="navigation-row-fork" type="button" data-workline-conversation-trigger data-workline-id-conversation="${escapeNavigationHtml(worklineId)}" aria-label="${escapeNavigationHtml(label)}" title="${escapeNavigationHtml(label)}">+</button>`;
}

// Distinct from the sidebar-header "+", which opens the directory flow, and from
// the conversation-row "+" which adds another chat on the same workline.
function navigationForkTrigger(projectId) {
  const label = t("shell.newWorkline");
  return `<button class="navigation-row-fork" type="button" data-project-fork-trigger data-project-id-fork="${escapeNavigationHtml(projectId)}" aria-label="${escapeNavigationHtml(label)}" title="${escapeNavigationHtml(label)}">+</button>`;
}

// A disclosure triangle for a branch of the tree. Rendered as a real button
// rather than a <details> so the open state can be persisted per node: a details
// element resets on every re-render, and the sidebar re-renders on any
// navigation change, which would spring every group open again.
export function navigationDisclosure(scope, id, expanded, label) {
  const key = `${scope}:${id}`;
  return `<button class="navigation-disclosure${expanded ? " expanded" : ""}" type="button" draggable="false" data-navigation-disclosure="${escapeNavigationHtml(key)}" aria-expanded="${expanded ? "true" : "false"}" aria-label="${escapeNavigationHtml(label)}" title="${escapeNavigationHtml(label)}"><svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m9 6 6 6-6 6"></path></svg></button>`;
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
  // Visible label is the folder name. A stored name that is actually the working
  // directory is shortened the same way conversation titles are. The full path
  // stays on the native tooltip, so it appears only while the pointer rests on
  // the row rather than as a second line under the name.
  const headline = displayFolderName(text(options.headline) || project.name);
  const hoverPath = displayPath || headline;
  const counts = options.taskCounts?.[project.id] || {};
  const activeTasks = Number(counts.todo || 0) + Number(counts.doing || 0) + Number(counts.blocked || 0);
  const taskMeta = options.taskContext
    ? `<span class="project-task-counts"><span>${escapeNavigationHtml(String(activeTasks))}</span>${Number(counts.blocked || 0) ? `<span class="blocked">${escapeNavigationHtml(String(counts.blocked))}</span>` : ""}</span>`
    : "";
  const statusClass = text(options.agentStatus) ? navigationAgentStatusClass(options.agentStatus) : "";
  const unread = options.unread === true;
  const stateClass = `${project.pinned ? "pinned " : ""}${project.archivedAt ? "archived " : ""}`;
  const stateMeta = navigationStateMarkup({ pinned: project.pinned, archivedAt: project.archivedAt });
  const folderClosed = `<span class="navigation-agent-icon navigation-folder-icon navigation-folder-closed theme-icon-slot" data-theme-icon-slot="sidebar-project" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"></path></svg></span>`;
  const folderOpen = `<span class="navigation-agent-icon navigation-folder-icon navigation-folder-open" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="m6 14 1.45-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.54 6a2 2 0 0 1-1.95 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"></path></svg></span>`;
  // Resting row is folder + name. Hovering anywhere on the row swaps the
  // folder for a chevron and paints the pill, so expand is not limited to the
  // 16px icon. Expanded groups swap the closed glyph for an open folder.
  // The row "+" is a trailing grid cell so it sits on the far right of a wide
  // column instead of overlapping the folder name.
  const twist = `<span class="navigation-project-twist">${options.disclosure || ""}${folderClosed}${folderOpen}</span>`;
  return `
    <div class="navigation-conversation-row navigation-project-row ${options.taskContext ? "task-context " : ""}${active ? "active " : ""}${statusClass ? `status-${statusClass} ` : ""}${unread ? "unread " : ""}project-card ${stateClass}" role="button" tabindex="0" draggable="true"       title="${escapeNavigationHtml(hoverPath)}" data-project-id="${escapeNavigationHtml(project.id)}" data-navigation-kind="project" data-navigation-id="${escapeNavigationHtml(project.id)}"${statusClass ? ` data-agent-status="${escapeNavigationHtml(statusClass)}"` : ""}${unread ? " data-agent-unread=\"true\"" : ""} data-navigation-context="${options.taskContext ? "tasks" : "project"}">
      ${twist}
      <span class="navigation-conversation-main">
        <span class="navigation-conversation-title navigation-project-title"><span class="project-name">${escapeNavigationHtml(headline)}</span>${stateMeta}</span>
        <span class="navigation-conversation-meta project-path" title="${escapeNavigationHtml(path)}">${escapeNavigationHtml(displayPath)}</span>
      </span>
      ${options.taskContext ? "" : navigationForkTrigger(project.id)}
      ${taskMeta}
    </div>`;
}

function renderConversation(conversation, activeAgentId, nested = false, options = {}) {
  const active = options.activeSelectionKind !== "project" && conversation.agentId === activeAgentId;
  const taskContext = options.taskContext === true;
  const nestedFork = options.nestedFork === true;
  const statusClass = navigationAgentStatusClass(conversation.agentStatus);
  // The row a reader is currently looking at is read by definition, so it must
  // never render as unread even before the seen mark is written.
  //
  // hiddenUnread lets a row that has collapsed children carry their mark, the same
  // way a collapsed project row carries its conversations'. The active guard still
  // wins: an open conversation whose own forks are unread is reporting on them, not
  // on itself, so the mark is legitimate there -- but a row can never be unread on
  // its own behalf while it is the one being read.
  const unread = !active && (conversationUnread(conversation, options.seenMap) || options.hiddenUnread === true);
  const worklineContext = conversation.worklineBranch || conversation.worklineTitle;
  const projectContext = compactDisplayPath(conversation.projectPath) || conversation.projectName;
  const context = nested
    ? worklineContext
    : [projectContext, worklineContext].filter((value, index, items) => value && items.indexOf(value) === index).join(" / ");
  const metaParts = [context, conversation.model, navigationStatusLabel(conversation.agentStatus)];
  if (!taskContext) metaParts.push(t("workspace.navigation.messageCount", { count: conversation.messageCount }));
  const meta = metaParts.filter(Boolean).join(" · ");
  const stateClass = `${conversation.agentPinned ? "pinned " : ""}${conversation.agentArchivedAt ? "archived " : ""}`;
  const stateMeta = navigationStateMarkup({ pinned: conversation.agentPinned, archivedAt: conversation.agentArchivedAt });
  const orderScope = text(options.orderScope);
  // The full stored value stays in the row's tooltip, so a path-titled
  // conversation is still identifiable on hover.
  const displayTitle = conversationDisplayTitle(conversation);
  const relativeTime = taskContext ? "" : formatCompactRelativeTime(conversation.lastActivityAt, options.now);
  const worklineCreate = options.worklineCreate && conversation.worklineId
    ? navigationWorklineConversationTrigger(conversation.worklineId)
    : "";
  const trailing = relativeTime || worklineCreate
    ? `<span class="navigation-conversation-trailing">${relativeTime ? `<span class="navigation-conversation-time">${escapeNavigationHtml(relativeTime)}</span>` : ""}${worklineCreate}</span>`
    : "";
  // Fork conversations use a branch icon instead of the default conversation bubble.
  const icon = taskContext
    ? `<svg viewBox="0 0 20 20"><circle cx="10" cy="6.5" r="3"></circle><path d="M4.5 17c.7-3.5 2.5-5.2 5.5-5.2s4.8 1.7 5.5 5.2"></path></svg>`
    : nestedFork
      ? `<svg viewBox="0 0 20 20"><circle cx="7" cy="5" r="1.8"></circle><circle cx="7" cy="15" r="1.8"></circle><circle cx="14" cy="8" r="1.8"></circle><path d="M7 6.8v6.4M7 6.8c2 0 7 .5 7 1.2" stroke-linecap="round"></path></svg>`
      : `<svg viewBox="0 0 20 20"><path d="M5 4.5h10a2 2 0 0 1 2 2V12a2 2 0 0 1-2 2H9l-4 2.5V14a2 2 0 0 1-2-2V6.5a2 2 0 0 1 2-2Z"></path></svg>`;
  return `
    <div class="navigation-conversation-row ${nested ? "nested " : ""}${nestedFork ? "fork-conversation " : ""}${taskContext ? "task-context " : ""}${active ? "active " : ""}status-${statusClass} ${unread ? "unread " : ""}${stateClass}" role="button" tabindex="0" draggable="true"${active ? ` aria-current="true"` : ""} title="${escapeNavigationHtml(conversationHoverTitle(conversation))}" data-navigation-target="${escapeNavigationHtml(conversation.targetId)}" data-navigation-kind="conversation" data-navigation-id="${escapeNavigationHtml(conversation.agentId)}" data-agent-status="${escapeNavigationHtml(conversation.agentStatus || "idle")}"${unread ? " data-agent-unread=\"true\"" : ""} data-navigation-context="${taskContext ? "tasks" : "project"}"${orderScope ? ` data-conversation-order-scope="${escapeNavigationHtml(orderScope)}"` : ""}>
      ${options.disclosure || ""}
      <span class="navigation-agent-icon theme-icon-slot" data-theme-icon-slot="${nestedFork ? "sidebar-fork" : "sidebar-conversation"}" aria-hidden="true">${icon}</span>
      <span class="navigation-conversation-main">
        <span class="navigation-conversation-title"><span class="navigation-title-text">${escapeNavigationHtml(displayTitle)}</span>${stateMeta}</span>
        <span class="navigation-conversation-meta" title="${escapeNavigationHtml(meta)}">${escapeNavigationHtml(meta)}</span>
      </span>
      ${trailing}
    </div>`;
}

// Same tier rule as the project order: a stored drag order reorders rows inside
// the pinned/normal/archived tiers, and never lifts a normal conversation above
// a pinned one. When a seenMap is provided, rows with fresh activity (running,
// or unread) additionally float above the dragged rows of their tier.
export function applyConversationOrder(conversations, orderArr, seenMap) {
  return applyProjectItemOrder(
    conversations,
    orderArr,
    (conversation) => text(conversation?.agentId),
    (conversation) => projectOrderTier(
      conversation?.agentPinned,
      conversation?.agentArchivedAt || conversation?.projectArchivedAt,
    ),
    seenMap ? (conversation) => conversationHasFreshActivity(conversation, seenMap) : undefined,
  );
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
  // Nodes are addressed as "scope:id". For "project" and "workline" the entry
  // means collapsed, so an untouched group shows its children.
  const collapsed = options.collapsedNodes instanceof Set
    ? options.collapsedNodes
    : new Set(Array.isArray(options.collapsedNodes) ? options.collapsedNodes.map(text).filter(Boolean) : []);
  // Passed in rather than read here so the renderer stays pure and testable.
  const seenMap = options.seenMap && typeof options.seenMap === "object" ? options.seenMap : {};
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
    // Apply user-defined project order (drag-to-reorder, persisted in
    // localStorage). Rows with fresh activity outrank the drag order; see
    // conversationHasFreshActivity for the compromise this implements.
    const groups = applyProjectGroupOrder(view.groups || [], options.projectOrder, seenMap);
    const groupsHTML = groups.map((group) => {
      const orderedConvs = options.conversationOrders?.[group.project.id]
        ? applyConversationOrder(group.conversations, options.conversationOrders[group.project.id], seenMap)
        : group.conversations;

      const convsHTML = clusterConversationsByWorkline(orderedConvs).map(({ head, children }) => {
        const gitFork = isGitWorklineFork(head);
        const childrenOpen = !collapsed.has(`workline:${head.worklineId}`);
        const headHTML = renderConversation(head, activeAgentId, true, {
          activeSelectionKind,
          orderScope: group.project.id,
          seenMap,
          now: options.now,
          nestedFork: gitFork,
          worklineCreate: true,
          hiddenUnread: Boolean(children.length) && !childrenOpen && aggregateNavigationUnread(children, seenMap, activeAgentId),
          disclosure: children.length
            ? navigationDisclosure("workline", head.worklineId, childrenOpen, t("workspace.navigation.toggleConversations"))
            : "",
        });
        if (!children.length) return headHTML;
        const childrenHTML = children.map((child) => renderConversation(child, activeAgentId, true, {
          activeSelectionKind,
          orderScope: group.project.id,
          seenMap,
          now: options.now,
        })).join("");
        return `${headHTML}<div class="navigation-workline-forks"${childrenOpen ? "" : " hidden"}>${childrenHTML}</div>`;
      }).join("");

      const projectStatus = aggregateNavigationAgentStatus(group.conversations);
      const groupOpen = !collapsed.has(`project:${group.project.id}`);
      return `
      <section class="navigation-project-group" draggable="true" data-navigation-project-group="${escapeNavigationHtml(group.project.id)}" data-conversation-count="${escapeNavigationHtml(String(group.conversations.length))}" data-navigation-context="project">
        ${renderProject(group.project, activeProjectId, {
          activeSelectionKind,
          agentStatus: projectStatus,
          // Only while collapsed: an expanded group shows the unread row itself.
          unread: !groupOpen && aggregateNavigationUnread(group.conversations, seenMap, activeAgentId),
          // The open conversation names the row when it belongs to this project;
          // otherwise the most recent one does. Falls back to the project name
          // inside renderProject when the group has no conversations yet.
          // Folder name stays on the group row. Conversation titles live on the
          // nested rows, with relative time on the right, so the tree reads like
          // a file list rather than repeating the open chat twice.
          headline: group.project.name,
          // A group with nothing under it has nothing to disclose.
          disclosure: group.conversations.length
            ? navigationDisclosure("project", group.project.id, groupOpen, t("workspace.navigation.toggleConversations"))
            : "",
        })}
        <div class="navigation-project-conversations" data-project-conversations="${escapeNavigationHtml(group.project.id)}"${groupOpen ? "" : " hidden"}>
          ${convsHTML || `<div class="navigation-empty-conversations">${escapeNavigationHtml(t("workspace.navigation.noConversations"))}</div>`}
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
    const statusByProject = new Map((view.groups || []).map((group) => [group.project.id, aggregateNavigationAgentStatus(group.conversations)]));
    // Flat rows carry no nested conversation underneath, so without this the row
    // is named by the project -- normally its directory -- and reads as the same
    // path twice, once as the title and once as the meta line below it.
    const conversationsByProject = new Map((view.groups || []).map((group) => [group.project.id, group.conversations]));
    const projects = applyProjectOrder(view.projects || [], options.projectOrder, (project) =>
      (conversationsByProject.get(project.id) || []).some((conversation) => conversationHasFreshActivity(conversation, seenMap)));
    html = projects.map((project) => renderProject(project, activeProjectId, {
      activeSelectionKind,
      agentStatus: statusByProject.get(project.id) || "",
      unread: aggregateNavigationUnread(conversationsByProject.get(project.id), seenMap, activeAgentId),
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
