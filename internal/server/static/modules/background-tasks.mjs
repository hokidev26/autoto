import { escapeAttr, escapeHtml, setHTMLIfChanged, setTextIfChanged } from "./dom.mjs";
import { formatDuration, formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs";
// The child transcript is a conversation, so it is rendered with the same
// components the main thread uses rather than a second, thinner imitation of
// them. The version string matches app-main's import so the browser resolves
// both to one module instance.
import {
  groupToolActivityByMessage,
  nextToolActivitySelection,
  normalizeToolActivity,
  persistedReasoningSteps,
  renderToolActivityCardHTML,
  renderToolActivityStackHTML,
} from "./chat-rendering.mjs?v=protected-images-1-message-thread-1-plan-mode-2-user-message-left-1-switch-fix-3-hide-run-loading-1-i18n-shared-1-conversation-boundary-1-subagent-cards-1-message-lifecycle-1-subagent-incremental-1-profile-message-identity-1-profile-avatar-1-provider-errors-1-compact-run-error-1-first-token-task-status-1-tool-activity-lazy-1-tool-protocol-filter-1-live-assistant-last-1-tool-activity-svg-icons-1-reasoning-steps-1-reasoning-history-1-markdown-2-tool-inline-detail-1-md-table-1-tool-position-1-project-run-history-1-dup-activity-fix-1-reasoning-count-1-avatar-logo-fix-1-markdown-stream-1-reasoning-handover-1";

const terminalStatuses = new Set(["completed", "complete", "succeeded", "success", "failed", "error", "cancelled", "canceled", "interrupted"]);
const runningStatuses = new Set(["running", "started", "cancel_requested"]);
const queuedStatuses = new Set(["queued", "pending", "waiting", "waiting_approval"]);
const cancellableStatuses = new Set(["queued", "pending", "waiting", "waiting_approval", "running", "started", "cancel_requested"]);
const defaultOutputLimit = 24000;
// How often to re-read a subagent's conversation while its run is active, and how
// many times before giving up. 1.5s keeps a reply feeling prompt without hammering
// the API; the cap is a safety net for a run that never reports itself finished,
// and at this interval it covers roughly ten minutes of work.
const childPollIntervalMs = 1500;
const childPollMaxAttempts = 400;

function text(value) {
  return String(value ?? "").trim();
}

// Only the tones the status dot has a style for; anything else falls back to
// the activity's kind, and finally to the working blue, rather than rendering
// an unstyled dot.
const activityTones = new Set(["running", "queued", "waiting", "thinking", "generating", "retrying", "approval", "compacting"]);
// The activity resolver names its states by kind and only sets an explicit
// tone for "waiting". Every kind with a styled glyph maps here, so thinking,
// retrying and the rest reach the dot instead of collapsing into "running".
const activityKindTones = Object.freeze({
  thinking: "thinking",
  generating: "generating",
  retrying: "retrying",
  approval: "approval",
  compacting: "compacting",
  waiting: "waiting",
  tool: "running",
});

function normalizeForegroundActivity(value) {
  const label = text(value?.text || value?.label);
  if (!label) return null;
  const kind = text(value?.kind) || "activity";
  const tone = text(value?.tone);
  return {
    kind,
    text: label,
    tone: activityTones.has(tone) ? tone : (activityKindTones[kind] || "running"),
  };
}

function number(value, fallback = null) {
  if (value === null || value === undefined || value === "") return fallback;
  const normalized = Number(value);
  return Number.isFinite(normalized) ? normalized : fallback;
}

function object(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function publicSummary(value, fallback = {}) {
  const candidate = value?.publicSummary ?? value?.public_summary ?? value?.summary;
  if (candidate === undefined || candidate === null || candidate === "") return object(fallback);
  if (typeof candidate === "string") {
    try { return object(JSON.parse(candidate)); } catch { return object(fallback); }
  }
  return object(candidate);
}

function taskRevision(value) {
  const source = taskPayload(value);
  return number(source.revision ?? source.taskRevision ?? source.task_revision ?? value?.revision);
}

function associationKey(parentRunId, parentToolUseId) {
  const runId = text(parentRunId);
  const toolUseId = text(parentToolUseId);
  return runId && toolUseId ? JSON.stringify([runId, toolUseId]) : "";
}

function knownValue(value) {
  if (value === undefined || value === null || value === "") return false;
  if (Array.isArray(value)) return value.length > 0;
  if (typeof value === "object") return Object.keys(value).length > 0;
  return true;
}

function preserveKnownTask(previous, candidate) {
  const merged = { ...candidate };
  for (const [key, value] of Object.entries(previous || {})) {
    if (knownValue(value)) merged[key] = value;
  }
  return merged;
}

function clonePublicValue(value) {
  if (Array.isArray(value)) return value.map(clonePublicValue);
  if (!value || typeof value !== "object") return value;
  return Object.fromEntries(Object.entries(value)
    .filter(([key]) => !key.toLowerCase().includes("prompt"))
    .map(([key, item]) => [key, clonePublicValue(item)]));
}

function readonlySnapshot(value) {
  if (!value || typeof value !== "object" || Object.isFrozen(value)) return value;
  for (const item of Object.values(value)) readonlySnapshot(item);
  return Object.freeze(value);
}

function publicTaskTitle(source, kind) {
  const summary = publicSummary(source);
  const explicit = text(source.title || source.name || source.label || summary.title || summary.label || summary.description);
  if (explicit) return explicit;
  const program = text(summary.program);
  const subcommand = text(summary.subcommand);
  if (program) return [program, subcommand].filter(Boolean).join(" ");
  const model = text(summary.model);
  const subagentType = text(summary.subagentType || summary.subagent_type);
  if (kind === "agent" && (subagentType || model)) return [subagentType || "agent", model].filter(Boolean).join(" · ");
  return "";
}

function taskId(value) {
  return text(value?.taskId || value?.backgroundTaskId || value?.id || value?.data?.taskId || value?.data?.backgroundTaskId);
}

function taskPayload(value) {
  if (!value || typeof value !== "object") return {};
  if (value.task && typeof value.task === "object") return value.task;
  if (value.backgroundTask && typeof value.backgroundTask === "object") return value.backgroundTask;
  if (value.data?.task && typeof value.data.task === "object") return value.data.task;
  return value.data && typeof value.data === "object" ? value.data : value;
}

export function normalizeBackgroundTask(value, fallback = {}) {
  const source = taskPayload(value);
  const id = taskId(source) || taskId(value) || taskId(fallback);
  if (!id) return null;
  const ownerAgentId = text(
    source.ownerAgentId || source.owner_agent_id || source.agentId || source.agent_id
    || value?.ownerAgentId || value?.agentId || fallback.ownerAgentId || fallback.agentId,
  );
  const status = text(source.status || source.state || value?.status || fallback.status || "pending").toLowerCase();
  const createdAt = text(source.createdAt || source.created_at || value?.createdAt || fallback.createdAt);
  const updatedAt = text(source.updatedAt || source.updated_at || value?.updatedAt || fallback.updatedAt);
  const cancelRequestedAt = text(source.cancelRequestedAt || source.cancel_requested_at || fallback.cancelRequestedAt);
  const startedAt = text(source.startedAt || source.started_at || fallback.startedAt);
  const completedAt = text(source.completedAt || source.finishedAt || source.endedAt || source.completed_at || fallback.completedAt);
  let durationMs = number(source.durationMs ?? source.duration_ms ?? fallback.durationMs);
  if (durationMs === null && startedAt && completedAt) {
    const elapsed = Date.parse(completedAt) - Date.parse(startedAt);
    if (Number.isFinite(elapsed) && elapsed >= 0) durationMs = elapsed;
  }
  const kind = text(source.kind || source.type || source.taskKind || fallback.kind || "task");
  const summary = publicSummary(source, fallback.publicSummary || fallback.summary);
  const revision = number(source.revision ?? source.taskRevision ?? source.task_revision ?? value?.revision ?? fallback.revision);
  return {
    ...fallback,
    ...source,
    id,
    taskId: id,
    ownerAgentId,
    agentId: ownerAgentId,
    parentRunId: text(source.parentRunId || source.parentRunID || source.parent_run_id || fallback.parentRunId),
    parentToolUseId: text(source.parentToolUseId || source.parentToolUseID || source.parentToolId || source.parentToolID || source.parent_tool_use_id || fallback.parentToolUseId),
    kind,
    status,
    revision,
    title: text(source.title || source.name || source.label) || text(fallback.title) || publicTaskTitle({ ...source, publicSummary: summary }, kind),
    publicSummary: summary,
    summary,
    createdAt,
    updatedAt,
    cancelRequestedAt,
    startedAt,
    completedAt,
    durationMs,
    childAgentId: text(source.childAgentId || source.childAgent?.id || source.child_agent_id || fallback.childAgentId),
    childRunId: text(source.childRunId || source.childRun?.id || source.runId || source.child_run_id || fallback.childRunId),
    result: source.result ?? source.resultJson ?? source.result_json ?? fallback.result ?? null,
    errorCode: text(source.errorCode || source.error_code || fallback.errorCode),
    errorMessage: text(source.errorMessage || source.error_message || source.error || (terminalStatuses.has(status) ? source.message : "") || fallback.errorMessage || fallback.error),
    error: text(source.errorMessage || source.error_message || source.error || (terminalStatuses.has(status) ? source.message : "") || fallback.errorMessage || fallback.error),
    lastOutputSequence: number(source.lastOutputSequence ?? source.last_output_sequence ?? fallback.lastOutputSequence),
    outputBytes: number(source.outputBytes ?? source.output_bytes ?? fallback.outputBytes),
    truncated: Boolean(source.truncated ?? source.outputTruncated ?? source.output_truncated ?? fallback.truncated),
  };
}

export function summarizeBackgroundTasks(values = []) {
  const tasks = (Array.isArray(values) ? values : [])
    .map((value) => normalizeBackgroundTask(value))
    .filter(Boolean);
  const running = tasks.filter((task) => runningStatuses.has(task.status));
  const queued = tasks.filter((task) => queuedStatuses.has(task.status));
  const current = running[0] || queued[0] || null;
  return {
    current,
    runningCount: running.length,
    queuedCount: queued.length,
    activeCount: running.length + queued.length,
    totalCount: tasks.length,
  };
}

function taskCollection(value) {
  if (Array.isArray(value)) return value;
  for (const candidate of [value?.tasks, value?.backgroundTasks, value?.recentBackgroundTasks, value?.items, value?.data]) {
    if (Array.isArray(candidate)) return candidate;
  }
  return [];
}

function outputCollection(value) {
  if (Array.isArray(value)) return value;
  for (const candidate of [value?.output, value?.chunks, value?.items, value?.events, value?.data]) {
    if (Array.isArray(candidate)) return candidate;
  }
  if (typeof value?.output === "string" || typeof value?.text === "string") return [value];
  return [];
}

function normalizeOutputChunk(value, fallbackSequence = 0) {
  if (typeof value === "string") return { sequence: fallbackSequence, text: value };
  if (!value || typeof value !== "object") return null;
  const source = value.data && typeof value.data === "object" ? value.data : value;
  const content = source.text ?? source.output ?? source.chunk ?? source.content ?? source.message ?? value.text ?? value.output;
  if (content === null || content === undefined) return null;
  return {
    sequence: Math.max(0, Math.trunc(number(source.sequence ?? source.seq ?? source.outputSequence ?? value.sequence, fallbackSequence))),
    text: String(content),
    stream: text(source.stream || source.channel || source.kind || value.stream),
    createdAt: text(source.createdAt || source.timestamp || value.createdAt),
  };
}

export function normalizeContinuation(value = {}, fallback = {}) {
  const source = value?.continuation && typeof value.continuation === "object" ? value.continuation : value;
  if (!source || typeof source !== "object") return { ...fallback };
  const budgets = source.budgets && typeof source.budgets === "object" ? source.budgets : {};
  return {
    ...fallback,
    ...source,
    mode: text(source.mode || source.autoContinuationMode || source.auto_continuation_mode || fallback.mode || "off").toLowerCase(),
    count: number(source.count ?? source.continuationCount ?? source.continuations ?? fallback.count, 0),
    segmentTurns: number(source.segmentTurns ?? source.segment_turns ?? fallback.segmentTurns),
    turnsUsed: number(source.turnsUsed ?? source.totalTurns ?? source.turns ?? fallback.turnsUsed),
    maxTotalTurns: number(source.maxTotalTurns ?? budgets.maxTotalTurns ?? fallback.maxTotalTurns),
    tokensUsed: number(source.tokensUsed ?? source.totalTokens ?? source.tokenCount ?? fallback.tokensUsed),
    tokenBudget: number(source.tokenBudget ?? budgets.tokenBudget ?? budgets.maxTokens ?? fallback.tokenBudget),
    elapsedMs: number(source.elapsedMs ?? source.durationMs ?? source.totalDurationMs ?? fallback.elapsedMs),
    durationBudgetMs: number(source.durationBudgetMs ?? source.maxDurationMs ?? budgets.durationMs ?? fallback.durationBudgetMs),
    waitingTaskId: text(source.waitingTaskId || source.waitingBackgroundTaskId || source.waitingOnTaskId || fallback.waitingTaskId),
    lastStop: text(source.lastStop || source.lastStopType || source.stop || fallback.lastStop),
    reason: text(source.reason || source.lastStopReason || source.blockedReason || fallback.reason),
    status: text(source.status || fallback.status),
    scheduledAt: text(source.scheduledAt || fallback.scheduledAt),
    startedAt: text(source.startedAt || fallback.startedAt),
  };
}

// -1 is the durable "no ceiling" value carried straight through from config, so
// it must read as unlimited rather than printing a bare -1 next to real usage.
function unlimitedBudget(limit) {
  return limit === -1 || limit === "-1";
}

// The duration row formats its own values, so -1 has to bypass formatDuration
// and reach ratioText intact to take the unlimited branch.
function durationBudgetText(limit) {
  if (limit == null) return null;
  return unlimitedBudget(limit) ? -1 : formatDuration(limit);
}

function ratioText(used, limit) {
  if (used === null && limit === null) return "—";
  const limitText = unlimitedBudget(limit) ? t("backgroundTasks.continuation.unlimited") : (limit ?? "—");
  return `${used ?? "—"} / ${limitText}`;
}

export function createBackgroundTasksController({
  request,
  documentRef = globalThis.document,
  onChange,
  onError,
  onOpenChange,
  onNavigateAgent,
  onNavigateRun,
  // Supplies the configured provider models so the subagent model control offers
  // the same list as the main composer. Injected rather than fetched here to
  // keep one owner of the model catalogue.
  getModelOptions,
  // The main transcript's markdown renderer. It lives inside the chat rendering
  // controller because it shares that closure's code-block helpers, so it is
  // handed in rather than imported. Without it the pane falls back to the
  // bounded plain-text body, which is what a headless test gets.
  renderMarkdown,
  maxOutputChars = defaultOutputLimit,
} = {}) {
  if (typeof request !== "function") throw new Error("createBackgroundTasksController requires request");

  const tasksById = new Map();
  const tasksByParentTool = new Map();
  const order = [];
  const outputs = new Map();
  const outputCursors = new Map();
  const cancelBusy = new Set();
  const waitBusy = new Set();
  // A subagent's own conversation and settings, keyed by its agent id. Status
  // alone could not answer "what did it actually say", which is the reason to
  // open this panel at all.
  const childConversations = new Map();
  const childAgents = new Map();
  const childBusy = new Set();
  // childAgentId -> Map(messageId -> tool calls), so a turn can show what it ran.
  const childToolCalls = new Map();
  // childAgentId -> the same calls unsplit. Grouping by message drops anything
  // whose owning turn is outside the loaded window, and those calls are still
  // part of the run: the flat list is what lets them close the transcript the
  // way the main thread's run outcome does.
  const childToolCallList = new Map();
  // Which tool row is open, keyed by activity stack. The main thread keeps the
  // same map for the same reason: a re-render must not close a detail the
  // reader just opened.
  const childToolSelection = new Map();
  // childAgentId -> poll timer. handleEvent drops any event whose agentId is not
  // this panel's agent, and a child's replies carry the child's id, so nothing
  // the child does ever reaches us as an event. Without polling, a message sent
  // from this panel looked like it went nowhere: the POST succeeded, the one
  // refresh after it ran before the child had answered, and no later refresh
  // existed. Poll the child directly while it works.
  const childPolls = new Map();
  const hydrationRequests = new Map();
  const listeners = new Set();
  // Tabs the user opened, in the order they opened them. Every task used to get
  // a tab whether or not it had been looked at, so five background tasks meant
  // five truncated titles competing for one row. The strip now starts from the
  // overview and grows only on demand.
  const openTabs = [];
  // "" is the overview: the list of everything, and where a closed tab returns to.
  let selected = "";
  let agentId = "";
  let agentGeneration = 0;
  let trayOpen = false;
  let continuation = normalizeContinuation();
  let bound = false;
  let loading = false;
  let error = "";
  let foregroundActivity = null;

  const host = (id) => documentRef?.getElementById?.(id) || null;
  const operationIsCurrent = (expectedAgentId, expectedGeneration) => agentId === expectedAgentId && agentGeneration === expectedGeneration;

  function openTabIds() {
    return openTabs.filter((id) => tasksById.has(id));
  }

  function rememberOpenTab(id) {
    const normalized = text(id);
    if (!normalized || openTabs.includes(normalized)) return;
    openTabs.push(normalized);
  }

  // Closes the tab only. Cancelling is a separate, destructive action that lives
  // on the overview row and in the detail pane, so tidying the strip cannot stop
  // work by accident.
  function closeTab(id) {
    const normalized = text(id);
    const at = openTabs.indexOf(normalized);
    if (at < 0) return false;
    openTabs.splice(at, 1);
    if (selected === normalized) selected = "";
    emit("tab-closed");
    return true;
  }

  function setTrayOpen(nextOpen, reason = "tray-change") {
    const open = Boolean(nextOpen && agentId);
    if (trayOpen === open) return false;
    trayOpen = open;
    // Nothing is on screen to update once the tray is closed, so stop polling
    // rather than keep requesting on the user's behalf.
    if (!open) stopAllChildPolls();
    onOpenChange?.(open, { reason, agentId, selected });
    return true;
  }

  function orderedTasks() {
    return order.map((id) => tasksById.get(id)).filter(Boolean);
  }

  function availableModels() {
    const raw = typeof getModelOptions === "function" ? getModelOptions() : [];
    if (!Array.isArray(raw)) return [];
    const seen = new Set();
    const options = [];
    for (const entry of raw) {
      const value = text(typeof entry === "string" ? entry : entry?.value ?? entry?.id ?? entry?.name);
      if (!value || seen.has(value)) continue;
      seen.add(value);
      options.push({ value, label: text(typeof entry === "string" ? entry : entry?.label ?? entry?.title) || value });
    }
    return options;
  }

  function childMessages(childAgentId) {
    const id = text(childAgentId);
    return id ? (childConversations.get(id) || []) : [];
  }

  function childToolCallsByMessage(childAgentId) {
    const id = text(childAgentId);
    return (id && childToolCalls.get(id)) || new Map();
  }

  function childToolCallsFlat(childAgentId) {
    const id = text(childAgentId);
    return (id && childToolCallList.get(id)) || [];
  }

  // A reload triggered from the child composer or its settings knows the agent but
  // not the run, so recover the run from whichever task owns that agent.
  function childRunIdFor(childAgentId) {
    const id = text(childAgentId);
    if (!id) return "";
    for (const task of tasksById.values()) {
      if (text(task?.childAgentId) === id) return text(task?.childRunId);
    }
    return "";
  }

  // Loaded on demand rather than with the task list: most tasks are never
  // opened, and each one costs a separate conversation fetch.
  async function loadChildConversation(childAgentId, { force = false, runId = "" } = {}) {
    const id = text(childAgentId);
    if (!id || childBusy.has(id)) return;
    if (!force && childConversations.has(id)) return;
    childBusy.add(id);
    try {
      const run = text(runId);
      const [messages, agent, calls] = await Promise.all([
        request(`/api/agents/${encodeURIComponent(id)}/messages?limit=40`).catch(() => null),
        request(`/api/agents/${encodeURIComponent(id)}`).catch(() => null),
        // Tool calls hang off the run, not the agent, so a task with no recorded
        // child run simply has none to show. Failing this must not cost the
        // messages: the conversation is still readable without it.
        run
          ? request(`/api/agents/${encodeURIComponent(id)}/runs/${encodeURIComponent(run)}/tool-calls?view=activity`).catch(() => null)
          : Promise.resolve(null),
      ]);
      const list = Array.isArray(messages) ? messages : (Array.isArray(messages?.messages) ? messages.messages : []);
      childConversations.set(id, list);
      if (agent && typeof agent === "object") childAgents.set(id, agent);
      childToolCalls.set(id, groupToolCallsByMessage(calls));
      childToolCallList.set(id, toolCallList(calls));
      render();
    } finally {
      childBusy.delete(id);
    }
  }

  function toolCallList(payload) {
    return Array.isArray(payload) ? payload : (Array.isArray(payload?.toolCalls) ? payload.toolCalls : []);
  }

  // Grouped by message so each call renders under the turn that made it, in the
  // order the run recorded them.
  function groupToolCallsByMessage(payload) {
    const list = toolCallList(payload);
    const grouped = new Map();
    for (const call of list) {
      const messageId = text(call?.messageId);
      if (!messageId) continue;
      const existing = grouped.get(messageId);
      if (existing) existing.push(call);
      else grouped.set(messageId, [call]);
    }
    return grouped;
  }

  // Model, effort and permission changes are written straight to the subagent so
  // a task can be steered without leaving this panel.
  async function updateChildAgent(childAgentId, path, body) {
    const id = text(childAgentId);
    if (!id) return;
    try {
      await request(`/api/agents/${encodeURIComponent(id)}/${path}`, {
        method: "PATCH",
        body: JSON.stringify(body),
      });
      await loadChildConversation(id, { force: true, runId: childRunIdFor(id) });
    } catch (err) {
      onError?.(err);
    }
  }

  async function sendToChildAgent(childAgentId, message) {
    const id = text(childAgentId);
    const body = text(message);
    if (!id || !body) return;
    try {
      await request(`/api/agents/${encodeURIComponent(id)}/messages`, {
        method: "POST",
        // Project context, the same one the main composer sends. The
        // conversation context caps the run at readOnly, which turned every
        // follow-up here into a research-only reply: the subagent could be
        // asked to fix what it just did and had no way to touch a file.
        body: JSON.stringify({ text: body, mode: "execute", context: "project" }),
      });
      await loadChildConversation(id, { force: true, runId: childRunIdFor(id) });
      // The reply does not exist yet: the refresh above only shows the message
      // just sent. Follow the run until it finishes so the answer appears on its
      // own, the way it does in the main conversation.
      startChildPoll(id);
    } catch (err) {
      onError?.(err);
    }
  }

  // Returns the child's active run, or null once it has none.
  async function childActiveRun(childAgentId) {
    const id = text(childAgentId);
    if (!id) return null;
    // 404 is the documented idle answer here, not a failure, so a rejection of
    // any kind means "not working" rather than something worth reporting.
    const summary = await request(`/api/agents/${encodeURIComponent(id)}/runs/active`).catch(() => null);
    const run = summary?.run || summary?.Run || null;
    return run && text(run.id || run.ID) ? run : null;
  }

  function stopChildPoll(childAgentId) {
    const id = text(childAgentId);
    const timer = childPolls.get(id);
    if (timer == null) return;
    clearTimeout(timer);
    childPolls.delete(id);
  }

  // Refreshes the child's conversation on a timer until its run goes idle, then
  // refreshes once more so the final turn is not left off, and stops. Bounded by
  // childPollMaxAttempts so a wedged run cannot poll forever.
  function startChildPoll(childAgentId, attempt = 0) {
    const id = text(childAgentId);
    if (!id) return;
    stopChildPoll(id);
    if (attempt >= childPollMaxAttempts) {
      render();
      return;
    }
    const timer = setTimeout(async () => {
      childPolls.delete(id);
      // The panel may have moved on while this was pending; a background refresh
      // for a child nobody is looking at is wasted work.
      if (!isChildVisible(id)) {
        render();
        return;
      }
      try {
        const run = await childActiveRun(id);
        await loadChildConversation(id, { force: true, runId: text(run?.id || run?.ID) || childRunIdFor(id) });
        if (run) startChildPoll(id, attempt + 1);
        else render();
      } catch (err) {
        onError?.(err);
        render();
      }
    }, childPollIntervalMs);
    childPolls.set(id, timer);
    render();
  }

  // Whether this child's conversation is on screen right now.
  function isChildVisible(childAgentId) {
    const id = text(childAgentId);
    if (!id || !trayOpen) return false;
    const task = selected ? tasksById.get(selected) : null;
    return Boolean(task && text(task.childAgentId) === id);
  }

  function childIsWorking(childAgentId) {
    return childPolls.has(text(childAgentId));
  }

  function stopAllChildPolls() {
    for (const timer of childPolls.values()) clearTimeout(timer);
    childPolls.clear();
  }

  function taskSummary() {
    return summarizeBackgroundTasks(orderedTasks());
  }

  function publicTaskSnapshot(task) {
    if (!task) return null;
    const summary = clonePublicValue(task.publicSummary || task.summary || {});
    return {
      id: task.id,
      taskId: task.taskId,
      ownerAgentId: task.ownerAgentId,
      agentId: task.agentId,
      parentRunId: task.parentRunId,
      parentToolUseId: task.parentToolUseId,
      kind: task.kind,
      status: task.status,
      revision: task.revision,
      title: task.title,
      publicSummary: summary,
      summary: clonePublicValue(summary),
      createdAt: task.createdAt,
      updatedAt: task.updatedAt,
      cancelRequestedAt: task.cancelRequestedAt,
      startedAt: task.startedAt,
      completedAt: task.completedAt,
      durationMs: task.durationMs,
      childAgentId: task.childAgentId,
      childRunId: task.childRunId,
      result: clonePublicValue(task.result),
      errorCode: task.errorCode,
      errorMessage: task.errorMessage || task.error,
      error: task.errorMessage || task.error,
      lastOutputSequence: task.lastOutputSequence,
      outputBytes: task.outputBytes,
      truncated: task.truncated,
      outputHasMore: Boolean(task.outputHasMore),
    };
  }

  function publicSummarySnapshot() {
    const summary = taskSummary();
    return {
      ...summary,
      current: publicTaskSnapshot(summary.current),
    };
  }

  function publicStateSnapshot(reason = "change") {
    return readonlySnapshot({
      reason,
      agentId,
      selected,
      continuation: clonePublicValue(continuation),
      summary: publicSummarySnapshot(),
      tasks: orderedTasks().map(publicTaskSnapshot),
    });
  }

  function emit(reason = "change") {
    render();
    const snapshot = publicStateSnapshot(reason);
    onChange?.(snapshot);
    for (const listener of [...listeners]) {
      try { listener(snapshot); } catch (cause) { onError?.(cause); }
    }
  }

  function rememberOrder(id, recent = false) {
    const index = order.indexOf(id);
    if (index >= 0) order.splice(index, 1);
    if (recent) order.push(id);
    else order.unshift(id);
  }

  function updateParentToolIndex(previous, task) {
    const previousKey = associationKey(previous?.parentRunId, previous?.parentToolUseId);
    const nextKey = associationKey(task?.parentRunId, task?.parentToolUseId);
    if (previousKey && previousKey !== nextKey && tasksByParentTool.get(previousKey) === previous?.id) {
      tasksByParentTool.delete(previousKey);
    }
    if (nextKey) tasksByParentTool.set(nextKey, task.id);
  }

  function upsertTask(value, { recent = false } = {}) {
    const id = taskId(value);
    const previous = id ? tasksById.get(id) : null;
    const incomingRevision = taskRevision(value);
    const previousRevision = number(previous?.revision);
    if (previous && incomingRevision !== null && previousRevision !== null && incomingRevision < previousRevision) return null;
    let task = normalizeBackgroundTask(value, previous || { ownerAgentId: agentId, agentId });
    if (!task || (agentId && task.agentId && task.agentId !== agentId)) return null;
    if (previous && previousRevision !== null && incomingRevision === null) {
      task = preserveKnownTask(previous, task);
    }
    tasksById.set(task.id, task);
    updateParentToolIndex(previous, task);
    rememberOrder(task.id, recent);
    return task;
  }

  function ingestTasks(values, options = {}) {
    for (const value of taskCollection(values)) upsertTask(value, options);
  }

  function appendOutput(id, values, response = {}) {
    if (!id) return [];
    const current = outputs.get(id) || [];
    const bySequence = new Map(current.map((chunk) => [`${chunk.sequence}:${chunk.text}`, chunk]));
    let fallbackSequence = outputCursors.get(id) || 0;
    for (const value of outputCollection(values)) {
      const chunk = normalizeOutputChunk(value, fallbackSequence + 1);
      if (!chunk) continue;
      fallbackSequence = Math.max(fallbackSequence, chunk.sequence);
      bySequence.set(`${chunk.sequence}:${chunk.text}`, chunk);
    }
    let next = [...bySequence.values()].sort((a, b) => a.sequence - b.sequence);
    let total = next.reduce((sum, chunk) => sum + chunk.text.length, 0);
    while (next.length > 1 && total > maxOutputChars) total -= next.shift().text.length;
    outputs.set(id, next);
    const responseCursor = number(response.nextSequence ?? response.lastSequence ?? response.cursor);
    const cursor = Math.max(outputCursors.get(id) || 0, responseCursor ?? fallbackSequence);
    outputCursors.set(id, cursor);
    const task = tasksById.get(id);
    if (task && (response.truncated !== undefined || response.hasMore !== undefined)) {
      tasksById.set(id, {
        ...task,
        truncated: Boolean(response.truncated ?? task.truncated),
        outputHasMore: Boolean(response.hasMore ?? response.more),
      });
    }
    return next;
  }

  async function loadAgent(nextAgentId = agentId) {
    const requestedAgentId = text(nextAgentId);
    if (!requestedAgentId) return [];
    const requestedGeneration = agentGeneration;
    loading = true;
    error = "";
    emit("loading");
    try {
      const response = await request(`/api/agents/${encodeURIComponent(requestedAgentId)}/background-tasks?limit=100`, { method: "GET" });
      if (!operationIsCurrent(requestedAgentId, requestedGeneration)) return [];
      ingestTasks(response, { recent: true });
      if (response?.continuation) continuation = normalizeContinuation(response.continuation, continuation);
      return taskCollection(response);
    } catch (cause) {
      if (operationIsCurrent(requestedAgentId, requestedGeneration)) {
        error = cause?.message || String(cause);
        onError?.(cause);
      }
      return [];
    } finally {
      if (operationIsCurrent(requestedAgentId, requestedGeneration)) {
        loading = false;
        emit("loaded");
      }
    }
  }

  async function loadTask(id) {
    const normalized = text(id);
    if (!normalized) return null;
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    const response = await request(`/api/background-tasks/${encodeURIComponent(normalized)}`, { method: "GET" });
    if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
    const task = upsertTask(response);
    if (task) emit("task-loaded");
    return task;
  }

  function taskNeedsHydration(task) {
    if (!task) return true;
    return !task.parentRunId
      || !task.parentToolUseId
      || !task.createdAt
      || !task.updatedAt
      || task.revision === null
      || task.kind === "task"
      || !Object.keys(task.publicSummary || task.summary || {}).length;
  }

  function hydrateTask(id, { force = false } = {}) {
    const normalized = text(id);
    if (!normalized) return Promise.resolve(null);
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    const pending = hydrationRequests.get(normalized);
    if (!force && pending?.agentId === expectedAgentId && pending?.generation === expectedGeneration) return pending.promise;
    const entry = { agentId: expectedAgentId, generation: expectedGeneration, promise: null };
    entry.promise = loadTask(normalized)
      .catch((cause) => {
        if (operationIsCurrent(expectedAgentId, expectedGeneration)) onError?.(cause);
        return null;
      })
      .finally(() => {
        if (hydrationRequests.get(normalized) === entry) hydrationRequests.delete(normalized);
      });
    hydrationRequests.set(normalized, entry);
    return entry.promise;
  }

  async function loadOutput(id, { afterSequence } = {}) {
    const normalized = text(id);
    if (!normalized) return [];
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    const cursor = number(afterSequence, outputCursors.get(normalized) || 0) ?? 0;
    const query = new URLSearchParams({ afterSequence: String(Math.max(0, Math.trunc(cursor))) });
    const response = await request(`/api/background-tasks/${encodeURIComponent(normalized)}/output?${query}`, { method: "GET" });
    if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return [];
    const result = appendOutput(normalized, response, response || {});
    emit("output-loaded");
    return result;
  }

  async function selectTask(id) {
    const normalized = text(id);
    if (!normalized) return null;
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    const alreadyLoaded = tasksById.has(normalized);
    try {
      if (!alreadyLoaded) await loadTask(normalized);
      if (!operationIsCurrent(expectedAgentId, expectedGeneration) || !tasksById.has(normalized)) return null;
      selected = normalized;
      rememberOpenTab(normalized);
      setTrayOpen(true, "task-selected");
      emit("selected");
      if (alreadyLoaded) await loadTask(normalized);
      if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
      if (!(outputs.get(normalized) || []).length) await loadOutput(normalized, { afterSequence: 0 });
      // Opening a subagent task is a request to read its conversation, so fetch it
      // here rather than waiting for the first render to notice it is missing.
      const openedTask = tasksById.get(normalized);
      const childAgentId = text(openedTask?.childAgentId);
      if (childAgentId) {
        await loadChildConversation(childAgentId, { runId: text(openedTask?.childRunId) });
        // A child that is still working would otherwise render as a frozen
        // snapshot for as long as the panel stayed open.
        if (await childActiveRun(childAgentId)) startChildPoll(childAgentId);
      }
    } catch (cause) {
      if (operationIsCurrent(expectedAgentId, expectedGeneration)) onError?.(cause);
    }
    if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
    return tasksById.get(normalized) || null;
  }

  async function wait(id = selected) {
    const normalized = text(id);
    if (!normalized || waitBusy.has(normalized)) return null;
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    waitBusy.add(normalized);
    emit("wait-started");
    try {
      const response = await request(`/api/background-tasks/${encodeURIComponent(normalized)}/wait`, { method: "POST" });
      if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
      const task = upsertTask(response);
      await loadOutput(normalized).catch((cause) => {
        if (operationIsCurrent(expectedAgentId, expectedGeneration)) onError?.(cause);
      });
      return operationIsCurrent(expectedAgentId, expectedGeneration) ? task : null;
    } finally {
      if (operationIsCurrent(expectedAgentId, expectedGeneration)) {
        waitBusy.delete(normalized);
        emit("wait-finished");
      }
    }
  }

  async function cancel(id = selected) {
    const normalized = text(id);
    if (!normalized || cancelBusy.has(normalized)) return null;
    const expectedAgentId = agentId;
    const expectedGeneration = agentGeneration;
    cancelBusy.add(normalized);
    emit("cancel-started");
    try {
      const response = await request(`/api/background-tasks/${encodeURIComponent(normalized)}/cancel`, { method: "POST" });
      if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
      return upsertTask(response || { id: normalized, status: "cancelled" });
    } finally {
      if (operationIsCurrent(expectedAgentId, expectedGeneration)) {
        cancelBusy.delete(normalized);
        emit("cancel-finished");
      }
    }
  }

  function applySnapshot(snapshot = {}, options = {}) {
    const snapshotAgentId = text(options.agentId || snapshot?.agent?.id || snapshot?.agentId);
    if (agentId && snapshotAgentId && snapshotAgentId !== agentId) return false;
    if (snapshot && typeof snapshot === "object") {
      if (snapshot.backgroundTasks !== undefined) ingestTasks(snapshot.backgroundTasks);
      if (snapshot.recentBackgroundTasks !== undefined) ingestTasks(snapshot.recentBackgroundTasks, { recent: true });
      if (snapshot.continuation) continuation = normalizeContinuation(snapshot.continuation, continuation);
    }
    emit("snapshot");
    return true;
  }

  function handleEvent(event = {}) {
    const type = text(event.type);
    const eventAgentId = text(event.agentId || event.data?.agentId);
    if (agentId && eventAgentId && eventAgentId !== agentId) return false;
    if (["task.created", "task.status", "task.completed"].includes(type)) {
      const task = upsertTask(event);
      if (task && type === "task.completed" && !terminalStatuses.has(task.status)) {
        const completedTask = { ...task, status: "completed" };
        tasksById.set(task.id, completedTask);
        updateParentToolIndex(task, completedTask);
      }
      const current = task ? tasksById.get(task.id) : null;
      const lifecycleRefresh = Boolean(current && type !== "task.created");
      if (current && (lifecycleRefresh || taskNeedsHydration(current))) void hydrateTask(current.id, { force: lifecycleRefresh });
    } else if (type === "task.output") {
      const id = taskId(event);
      if (id) {
        if (!tasksById.has(id)) upsertTask({ id, agentId: eventAgentId, status: "running", kind: event.data?.kind });
        const current = tasksById.get(id);
        // AgentExecutor attaches child identifiers immediately before its first
        // output chunk. Refresh the durable task once here so running cards can
        // expose child navigation without waiting for the terminal event.
        if (current?.kind === "agent" && (!current.childAgentId || !current.childRunId)) void hydrateTask(id);
        const inlineText = text(event.text || event.output || event.chunk || event.data?.text || event.data?.output || event.data?.chunk);
        if (inlineText) appendOutput(id, [event], event.data || {});
        else loadOutput(id).catch(onError);
      }
    } else if (["agent.continuation_scheduled", "agent.continuation_started", "agent.continuation_blocked", "agent.budget_exhausted"].includes(type)) {
      const status = type.replace("agent.continuation_", "").replace("agent.", "");
      continuation = normalizeContinuation({ ...(event.data || {}), status }, continuation);
      if (type === "agent.budget_exhausted") continuation.status = "budget_exhausted";
    } else {
      return false;
    }
    emit(type);
    return true;
  }

  function setForegroundActivity(value) {
    const next = agentId ? normalizeForegroundActivity(value) : null;
    if (next?.kind === foregroundActivity?.kind && next?.text === foregroundActivity?.text && next?.tone === foregroundActivity?.tone) return true;
    foregroundActivity = next;
    render();
    return true;
  }

  function setAgent(nextAgentId, { load = false } = {}) {
    const normalized = text(nextAgentId);
    if (normalized === agentId) return load ? loadAgent(normalized) : Promise.resolve([]);
    setTrayOpen(false, "agent-changed");
    agentId = normalized;
    agentGeneration += 1;
    tasksById.clear();
    tasksByParentTool.clear();
    hydrationRequests.clear();
    order.splice(0);
    openTabs.splice(0);
    outputs.clear();
    outputCursors.clear();
    cancelBusy.clear();
    waitBusy.clear();
    // Timers outlive a state reset, so a poll for the previous agent's child would
    // keep firing against a panel that has moved on.
    stopAllChildPolls();
    selected = "";
    continuation = normalizeContinuation();
    loading = false;
    error = "";
    foregroundActivity = null;
    emit("agent-changed");
    return load && normalized ? loadAgent(normalized) : Promise.resolve([]);
  }

  function taskStatusLabel(status) {
    const key = text(status || "unknown").toLowerCase().replaceAll("-", "_");
    return t(`backgroundTasks.status.${key}`);
  }

  function taskOutputText(id) {
    return (outputs.get(id) || []).map((chunk) => chunk.text).join("");
  }

  // Browser-style tabs, but only for tasks the user opened. The leading tab is
  // the overview and cannot be closed, so there is always somewhere to go back
  // to and somewhere to reopen a task from.
  function renderTabStripHTML() {
    const overviewActive = !selected;
    const tabs = openTabIds().map((id) => {
      const task = tasksById.get(id);
      const active = id === selected;
      const label = task.title || task.id;
      return `<span class="background-task-tab ${active ? "active" : ""}" data-background-tab-wrap>
        <button class="background-task-tab-main" type="button" data-background-task="${escapeAttr(id)}" aria-pressed="${active ? "true" : "false"}" title="${escapeAttr(label)}">
          <span class="background-task-tab-dot status-${escapeAttr(task.status)}" aria-hidden="true"></span>
          <span class="background-task-tab-label">${escapeHtml(label)}</span>
        </button>
        <button class="background-task-tab-close" type="button" data-background-tab-close="${escapeAttr(id)}" title="${escapeAttr(t("backgroundTasks.closeTab"))}" aria-label="${escapeAttr(t("backgroundTasks.closeTab"))}">×</button>
      </span>`;
    }).join("");
    return `<div class="background-task-tabs" role="tablist">
      <span class="background-task-tab overview ${overviewActive ? "active" : ""}" data-background-tab-wrap>
        <button class="background-task-tab-main" type="button" data-background-overview aria-pressed="${overviewActive ? "true" : "false"}" title="${escapeAttr(t("backgroundTasks.overview"))}">
          <span class="background-task-tab-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h10"></path></svg></span>
          <span class="background-task-tab-label">${escapeHtml(t("backgroundTasks.overview"))}</span>
        </button>
      </span>
      ${tabs}
    </div>`;
  }

  // The overview row carries the cancel, because the tab's × now only closes the
  // tab and stopping a task still has to be reachable.
  function renderOverviewRowHTML(task) {
    const canCancel = cancellableStatuses.has(task.status);
    const label = task.title || task.id;
    return `<div class="background-task-overview-row">
      <button class="background-task-overview-open" type="button" data-background-task="${escapeAttr(task.id)}" title="${escapeAttr(label)}">
        <span class="background-task-tab-dot status-${escapeAttr(task.status)}" aria-hidden="true"></span>
        <span class="background-task-overview-title">${escapeHtml(label)}</span>
        <span class="background-task-state status-${escapeAttr(task.status)}">${escapeHtml(taskStatusLabel(task.status))}</span>
        <span class="background-task-overview-time">${escapeHtml(task.durationMs == null ? (task.createdAt ? formatTimestamp(task.createdAt) : "—") : formatDuration(task.durationMs))}</span>
      </button>
      ${canCancel
        ? `<button class="background-task-overview-cancel" type="button" data-background-cancel="${escapeAttr(task.id)}" ${cancelBusy.has(task.id) ? "disabled" : ""} title="${escapeAttr(t("backgroundTasks.cancel"))}" aria-label="${escapeAttr(t("backgroundTasks.cancel"))}"><svg viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="m6 6 12 12"></path><path d="m18 6-12 12"></path></svg></button>`
        : ""}
    </div>`;
  }

  const effortOptions = ["auto", "low", "medium", "high", "xhigh", "max", "ultra"];
  // Same keys the main composer's permission select uses, so a raw
  // "bypassPermissions" is never shown where every other control is translated.
  const permissionOptions = [
    ["readOnly", "chat.permission.readOnly"],
    ["acceptEdits", "chat.permission.editable"],
    ["bypassPermissions", "chat.permission.allowAll"],
    ["default", "chat.permission.automatic"],
    ["dontAsk", "chat.permission.dontAsk"],
  ];

  function renderChildConversationHTML(task) {
    const childAgentId = text(task.childAgentId);
    if (!childAgentId) return `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.noChildAgent"))}</div>`;
    const messages = childMessages(childAgentId);
    if (!messages.length) {
      return `<div class="background-task-empty">${escapeHtml(childBusy.has(childAgentId) ? t("backgroundTasks.loading") : t("backgroundTasks.noConversation"))}</div>`;
    }
    const runId = text(task.childRunId);
    const callsByMessage = childToolCallsByMessage(childAgentId);
    const knownMessageIds = new Set(messages.map((message) => text(message?.id)).filter(Boolean));
    const bubbles = messages.map((message) => {
      const role = text(message?.role) === "user" ? "user" : "assistant";
      // contentText is what the messages endpoint returns. This read used to be
      // `message.text || message.content`, and neither field has ever existed on
      // that payload, so every bubble was dropped as empty below and the pane
      // rendered blank for a task that had run for twenty minutes.
      const body = text(message?.contentText || message?.text || message?.content);
      const reasoning = text(message?.reasoningText);
      const calls = callsByMessage.get(text(message?.id)) || [];
      // A turn with no answer text is still worth showing when it reasoned or
      // called a tool: that is the part of a subagent's work you cannot otherwise
      // see, and dropping the whole bubble is what hid it.
      if (!body && !reasoning && !calls.length) return "";
      return `<article class="background-task-bubble role-${role}">
        <header><span>${escapeHtml(role === "user" ? t("backgroundTasks.roleUser") : t("backgroundTasks.roleAgent"))}</span><time>${escapeHtml(message?.createdAt ? formatTimestamp(message.createdAt) : "")}</time></header>
        ${renderChildActivityHTML(childAgentId, `msg:${text(message?.id)}`, calls, persistedReasoningSteps(message, calls), runId)}
        ${body ? renderChildBodyHTML(body) : ""}
      </article>`;
    }).join("");
    // Calls whose owning turn is not in the loaded window close the transcript on
    // their own, the way the main thread's run outcome card does, so a run whose
    // first turns have scrolled out of the window still accounts for its work.
    const { unowned } = groupToolActivityByMessage(childToolCallsFlat(childAgentId), knownMessageIds);
    const trailing = renderChildActivityHTML(childAgentId, "run", unowned, [], runId);
    return `<div class="background-task-conversation">${bubbles}${trailing}</div>`;
  }

  // Same activity stack as the main transcript: one "活動" disclosure per turn,
  // reasoning steps filed against the tool that ended them, rows that open their
  // own detail. Compact because this pane is a column, not a page.
  function renderChildActivityHTML(childAgentId, scope, calls, reasoningSteps, runId) {
    const stackKey = `child:${text(childAgentId)}:${scope}`;
    return renderToolActivityStackHTML(calls, {
      compact: true,
      runId,
      stackKey,
      reasoningSteps,
      selectedToolUseId: childToolSelection.get(stackKey) || "",
    });
  }

  // Markdown when the main renderer was injected, the bounded plain-text body
  // otherwise. A subagent writes the same lists, tables and fenced code the main
  // agent does, and rendering it as one escaped paragraph was the largest visible
  // difference between this pane and the conversation it mirrors.
  function renderChildBodyHTML(body) {
    if (typeof renderMarkdown !== "function") return renderChildBubbleBodyHTML(body);
    return `<div class="message-content background-task-bubble-body">${renderMarkdown(body)}</div>`;
  }

  // How much of a message body the pane shows before folding the rest away. A briefing
  // to a subagent, or its report back, routinely quotes file excerpts and grep hits,
  // and one such message ran to hundreds of numbered lines: the panel became a wall of
  // source with no indication of which file any of it came from, and the status and
  // error above it were pushed out of view.
  //
  // This is the fallback path. When the main transcript's markdown renderer is
  // injected the body goes through that instead, and the clamping there is the
  // pane's own scrolling rather than a disclosure.
  const childBubbleBodyLineLimit = 12;
  const childBubbleBodyCharLimit = 900;

  // Nothing is discarded: past the limit the head stays visible and the remainder goes
  // into a disclosure, so the shape of the conversation survives while the whole text
  // is still one click away.
  function renderChildBubbleBodyHTML(body) {
    const lines = body.split("\n");
    const tooManyLines = lines.length > childBubbleBodyLineLimit;
    const tooLong = body.length > childBubbleBodyCharLimit;
    if (!tooManyLines && !tooLong) return `<p>${escapeHtml(body)}</p>`;
    // Whichever limit bites first decides the cut, so a single enormous line is
    // bounded too rather than only a tall one.
    const headByLines = tooManyLines ? lines.slice(0, childBubbleBodyLineLimit).join("\n") : body;
    const head = headByLines.length > childBubbleBodyCharLimit
      ? headByLines.slice(0, childBubbleBodyCharLimit)
      : headByLines;
    const rest = body.slice(head.length).replace(/^\n/, "");
    if (!rest) return `<p>${escapeHtml(body)}</p>`;
    const hiddenLines = rest.split("\n").length;
    return `<p>${escapeHtml(head)}</p>
      <details class="background-task-bubble-more">
        <summary>${escapeHtml(t("backgroundTasks.moreLines", { count: String(hiddenLines) }))}</summary>
        <p>${escapeHtml(rest)}</p>
      </details>`;
  }

  // A stack key is "child:<agentId>:<scope>", and the detail lookup needs the
  // agent back out of it: the clicked row only knows which stack it belongs to.
  function childAgentIdFromStackKey(stackKey) {
    const parts = text(stackKey).split(":");
    return parts.length >= 3 && parts[0] === "child" ? parts.slice(1, -1).join(":") : "";
  }

  // Fills the clicked row's own detail slot and clears the rest, matching the
  // main transcript. Rendering into the row rather than a shared slot at the
  // bottom is what keeps the detail next to the tool it describes.
  function renderChildActivitySelection(stack, toolUseId) {
    if (!stack) return;
    const selected = text(toolUseId);
    const childAgentId = childAgentIdFromStackKey(stack.dataset?.toolActivityStackKey);
    stack.querySelectorAll?.("[data-tool-activity-select]").forEach((button) => {
      const active = text(button.dataset?.toolActivitySelect) === selected;
      button.setAttribute?.("aria-expanded", active ? "true" : "false");
      button.closest?.(".tool-activity-step")?.classList?.toggle?.("selected", active);
    });
    stack.querySelectorAll?.("[data-tool-activity-inline-detail]").forEach((slot) => {
      if (text(slot.dataset?.toolActivityInlineDetail) !== selected) {
        slot.innerHTML = "";
        return;
      }
      const record = childToolCallsFlat(childAgentId).find((item) => normalizeToolActivity(item).toolUseId === selected);
      slot.innerHTML = record ? renderToolActivityCardHTML(record, { detailsExpanded: true, inlineDetail: true }) : "";
    });
  }

  // Returns true once it has handled the click, so the panel's own routing does
  // not also run for a press inside an activity stack.
  function handleChildActivityClick(event) {
    const button = event.target?.closest?.("[data-tool-activity-select]");
    if (!button) return false;
    const stack = button.closest?.("[data-tool-activity-stack]");
    const stackKey = text(stack?.dataset?.toolActivityStackKey);
    if (!stack || !stackKey) return false;
    const next = nextToolActivitySelection(childToolSelection.get(stackKey) || "", text(button.dataset?.toolActivitySelect));
    if (next) childToolSelection.set(stackKey, next);
    else childToolSelection.delete(stackKey);
    renderChildActivitySelection(stack, next);
    return true;
  }

  // Mirrors the main composer's controls so a subagent can be retargeted without
  // opening it as the foreground conversation.
  function renderChildControlsHTML(task) {
    const childAgentId = text(task.childAgentId);
    if (!childAgentId) return "";
    const agent = childAgents.get(childAgentId) || {};
    const model = text(agent.model);
    const effort = text(agent.reasoningEffort) || "auto";
    const permission = text(agent.permissionMode) || "default";
    const working = childIsWorking(childAgentId);
    // The model list comes from the configured providers, so this offers the same
    // choices as the main composer instead of asking the user to type an id.
    const options = availableModels();
    const known = options.some((option) => option.value === model);
    return `<div class="background-task-child-controls">
      <label class="background-task-child-field">
        <span>${escapeHtml(t("chat.model"))}</span>
        ${options.length
          ? `<select data-background-child-model="${escapeAttr(childAgentId)}">
              ${!known && model ? `<option value="${escapeAttr(model)}" selected>${escapeHtml(model)}</option>` : ""}
              ${options.map((option) => `<option value="${escapeAttr(option.value)}" ${option.value === model ? "selected" : ""}>${escapeHtml(option.label)}</option>`).join("")}
            </select>`
          : `<input type="text" data-background-child-model="${escapeAttr(childAgentId)}" value="${escapeAttr(model)}" spellcheck="false" />`}
      </label>
      <label class="background-task-child-field compact">
        <span>${escapeHtml(t("chat.reasoningEffort"))}</span>
        <select data-background-child-effort="${escapeAttr(childAgentId)}">
          ${effortOptions.map((option) => `<option value="${escapeAttr(option)}" ${option === effort ? "selected" : ""}>${escapeHtml(option === "auto" ? t("modelProvider.automatic") : option)}</option>`).join("")}
        </select>
      </label>
      <label class="background-task-child-field compact">
        <span>${escapeHtml(t("chat.permissionMode"))}</span>
        <select data-background-child-permission="${escapeAttr(childAgentId)}">
          ${permissionOptions.map(([value, key]) => `<option value="${escapeAttr(value)}" ${value === permission ? "selected" : ""}>${escapeHtml(t(key))}</option>`).join("")}
        </select>
      </label>
    </div>
    <form class="background-task-child-composer" data-background-child-form="${escapeAttr(childAgentId)}">
      <input type="text" name="message" placeholder="${escapeAttr(t("chat.messagePlaceholder"))}" autocomplete="off" />
      <button type="submit" class="ghost-btn mini">${escapeHtml(t("chat.send"))}</button>
    </form>
    ${working ? `<div class="background-task-child-working" role="status"><span class="background-task-child-working-dot" aria-hidden="true"></span>${escapeHtml(t("backgroundTasks.childWorking"))}</div>` : ""}`;
  }

  function renderSelectedTask(task) {
    if (!task) return `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.selectTask"))}</div>`;
    const output = taskOutputText(task.id);
    const canCancel = cancellableStatuses.has(task.status);
    const isTerminal = terminalStatuses.has(task.status);
    const hasChild = Boolean(text(task.childAgentId));
    // An agent task reads as a conversation, full stop. The conversation/output
    // tabs made the pane look like a debugging surface and the "output" side was
    // empty for agent tasks anyway, since their result is the transcript. Shell
    // tasks have no child agent and no transcript, so those still show raw output
    // -- that is the only thing they produce.
    const view = hasChild ? "conversation" : "output";
    // No title block here: the active tab above already names the task and shows
    // its status, and repeating both cost a third of the pane.
    return `<section class="background-task-detail">
      <div class="background-task-meta"><span class="background-task-state status-${escapeAttr(task.status)}">${escapeHtml(taskStatusLabel(task.status))}</span><span>${escapeHtml(task.createdAt ? formatTimestamp(task.createdAt) : "—")}</span><span>${escapeHtml(task.durationMs == null ? "—" : formatDuration(task.durationMs))}</span></div>
      ${task.error || task.errorCode ? `<div class="background-task-error">${escapeHtml([task.errorCode ? t("backgroundTasks.errorCode", { code: task.errorCode }) : "", task.error ? t("backgroundTasks.errorMessage", { message: task.error }) : ""].filter(Boolean).join(" · "))}</div>` : ""}
      ${view === "conversation"
        ? renderChildConversationHTML(task)
        : `<pre class="background-task-output">${escapeHtml(output || t("backgroundTasks.noOutput"))}</pre>`}
      ${task.truncated && view === "output" ? `<div class="background-task-truncated">${escapeHtml(t("backgroundTasks.truncated"))}</div>` : ""}
      ${view === "conversation" ? renderChildControlsHTML(task) : ""}
      <div class="background-task-actions">
        ${task.outputHasMore && view === "output" ? `<button type="button" class="ghost-btn mini" data-background-output-more="${escapeAttr(task.id)}">${escapeHtml(t("backgroundTasks.loadMore"))}</button>` : ""}
      </div>
    </section>`;
  }

  function render() {
    const button = host("backgroundTasksBtn");
    const badge = host("backgroundTasksBadge");
    const headerButton = host("headerTaskSummaryBtn");
    const headerText = host("headerCurrentTaskText");
    const headerQueue = host("headerTaskQueueBadge");
    const headerDot = host("headerTaskStatusDot");
    const tray = host("backgroundTaskTray");
    const summary = taskSummary();
    const activeCount = summary.activeCount;
    const currentStatus = summary.current?.status || "idle";
    const currentTone = foregroundActivity
      ? foregroundActivity.tone || "running"
      : runningStatuses.has(currentStatus) ? "running" : queuedStatuses.has(currentStatus) ? "queued" : "idle";
    const hasCurrentActivity = Boolean(foregroundActivity || summary.current);
    // A lifecycle event carries identifiers and state but no title, and the title
    // only arrives with the follow-up hydration request. Falling through to the
    // idle label in that window told the user nothing was running while the dot
    // beside it was already animating, so fall back to the counts instead: never
    // claim idle while a task is active.
    // Prefer a name over a count wherever one exists: the foreground activity's
    // own words, then the running task's title, and only then the counts.
    const currentText = foregroundActivity?.text
      || summary.current?.title
      || (hasCurrentActivity
        ? t("backgroundTasks.headerTitle", { queued: summary.queuedCount, running: summary.runningCount })
        : t("backgroundTasks.headerIdle"));

    if (button) {
      button.disabled = !agentId;
      button.setAttribute("aria-expanded", trayOpen ? "true" : "false");
      button.title = t("backgroundTasks.title");
      button.setAttribute("aria-label", t("backgroundTasks.title"));
      button.classList.toggle("active", trayOpen);
    }
    if (headerButton) {
      const headerLabel = foregroundActivity?.text || t("backgroundTasks.headerTitle", { queued: summary.queuedCount, running: summary.runningCount });
      headerButton.disabled = !agentId;
      headerButton.setAttribute("aria-expanded", trayOpen ? "true" : "false");
      headerButton.setAttribute("aria-busy", foregroundActivity ? "true" : "false");
      headerButton.setAttribute("aria-label", headerLabel);
      headerButton.title = headerLabel;
      headerButton.classList.toggle("active", trayOpen);
      headerButton.classList.toggle("has-task", hasCurrentActivity);
      headerButton.classList.toggle("has-foreground-activity", Boolean(foregroundActivity));
    }
    if (headerText) setTextIfChanged(headerText, currentText);
    if (headerQueue) {
      setTextIfChanged(headerQueue, t("backgroundTasks.queueCount", { count: summary.queuedCount }));
      headerQueue.classList.toggle("hidden", summary.queuedCount <= 0);
    }
    if (headerDot) headerDot.className = `header-task-status-dot ${currentTone}`;
    if (badge) {
      setTextIfChanged(badge, String(activeCount || order.length));
      badge.classList.toggle("hidden", !activeCount && !order.length);
    }
    if (!tray) return;
    tray.classList.toggle("hidden", !trayOpen || !agentId);
    if (!trayOpen || !agentId) return;
    const tasks = orderedTasks().slice(0, 12);
    setHTMLIfChanged(tray, `<header class="utility-panel-head background-task-tray-head">
        <div class="background-task-panel-title"><span class="background-task-panel-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><rect x="4" y="5" width="16" height="14" rx="2.5"></rect><path d="M8 9h8M8 13h5M8 17h3"></path></svg></span><div><strong>${escapeHtml(t("backgroundTasks.title"))}</strong><span>${escapeHtml(t("backgroundTasks.summary", { active: activeCount, total: order.length }))}</span></div></div>
        <button type="button" class="icon-btn" data-background-close aria-label="${escapeAttr(t("backgroundTasks.close"))}">×</button>
      </header>
      <div class="background-task-panel-body">
        ${error ? `<div class="background-task-error">${escapeHtml(error)}</div>` : ""}
        <div class="background-task-tray-grid">
          ${renderTabStripHTML()}
          <div class="background-task-tray-pane">
            ${selected
              ? renderSelectedTask(tasksById.get(selected))
              : `<div class="background-task-overview">${loading && !tasks.length
                ? `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.loading"))}</div>`
                : tasks.length
                  ? tasks.map(renderOverviewRowHTML).join("")
                  : `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.empty"))}</div>`}</div>`}
          </div>
        </div>
      </div>`);
  }

  function closeTray(reason = "tray-close") {
    if (!setTrayOpen(false, reason)) return false;
    emit(reason);
    return true;
  }

  function toggleTray() {
    const nextOpen = !trayOpen;
    // Opens on the overview rather than guessing a task: the strip is now built
    // from what the user opened, and preselecting would put a tab there that they
    // never asked for.
    setTrayOpen(nextOpen, "tray-toggle");
    emit("tray-toggle");
    if (nextOpen && agentId && !order.length) loadAgent(agentId).catch(onError);
  }

  function bind() {
    if (bound) return;
    bound = true;
    host("backgroundTasksBtn")?.addEventListener("click", toggleTray);
    host("headerTaskSummaryBtn")?.addEventListener("click", toggleTray);
    host("backgroundTaskTray")?.addEventListener("click", (event) => {
      if (handleChildActivityClick(event)) return;
      const target = event.target?.closest?.("[data-background-task],[data-background-close],[data-background-output-more],[data-background-cancel],[data-background-agent],[data-background-run],[data-background-overview],[data-background-tab-close]");
      if (!target) return;
      if (target.hasAttribute("data-background-close")) {
        closeTray();
      } else if (target.hasAttribute("data-background-overview")) {
        selected = "";
        emit("overview-selected");
      } else if (target.dataset.backgroundTabClose) {
        closeTab(target.dataset.backgroundTabClose);
      } else if (target.dataset.backgroundTask) {
        // Opens inside this panel only. Navigating the main conversation as well
        // hijacked the window: the user was reading one thread, and a glance at a
        // subagent replaced it and lost their place.
        selectTask(target.dataset.backgroundTask).catch(onError);
      } else if (target.dataset.backgroundOutputMore) loadOutput(target.dataset.backgroundOutputMore).catch(onError);
      else if (target.dataset.backgroundCancel) cancel(target.dataset.backgroundCancel).catch(onError);
      else if (target.dataset.backgroundAgent) onNavigateAgent?.(target.dataset.backgroundAgent);
      else if (target.dataset.backgroundRun) onNavigateRun?.(target.dataset.backgroundRunAgent, target.dataset.backgroundRun);
    });
    // Settings commit on change rather than behind a save button: this panel is
    // for steering a task mid-flight, and a second click would only add delay.
    host("backgroundTaskTray")?.addEventListener("change", (event) => {
      const node = event.target;
      if (!node?.dataset) return;
      if (node.dataset.backgroundChildEffort) {
        updateChildAgent(node.dataset.backgroundChildEffort, "reasoning-effort", { reasoningEffort: node.value }).catch(onError);
      } else if (node.dataset.backgroundChildPermission) {
        updateChildAgent(node.dataset.backgroundChildPermission, "permission-mode", { permissionMode: node.value }).catch(onError);
      } else if (node.dataset.backgroundChildModel) {
        const model = text(node.value);
        if (model) updateChildAgent(node.dataset.backgroundChildModel, "model", { model }).catch(onError);
      }
    });
    host("backgroundTaskTray")?.addEventListener("submit", (event) => {
      const form = event.target?.closest?.("[data-background-child-form]");
      if (!form) return;
      event.preventDefault();
      const input = form.elements?.message;
      const message = text(input?.value);
      if (!message) return;
      if (input) input.value = "";
      sendToChildAgent(form.dataset.backgroundChildForm, message).catch(onError);
    });
    render();
  }

  function subscribe(listener) {
    if (typeof listener !== "function") throw new TypeError("background task subscriber must be a function");
    listeners.add(listener);
    return () => listeners.delete(listener);
  }

  function getTaskSnapshot(id) {
    return readonlySnapshot(publicTaskSnapshot(tasksById.get(text(id))));
  }

  function getTaskByParentTool(parentRunId, parentToolUseId) {
    const key = associationKey(parentRunId, parentToolUseId);
    const id = key ? tasksByParentTool.get(key) : "";
    return id ? getTaskSnapshot(id) : null;
  }

  function renderContinuationStatusHTML() {
    const mode = continuation.mode === "safe" ? t("backgroundTasks.continuation.safe") : t("backgroundTasks.continuation.off");
    const waitingTask = continuation.waitingTaskId ? tasksById.get(continuation.waitingTaskId) : null;
    const items = [
      [t("backgroundTasks.continuation.mode"), mode],
      [t("backgroundTasks.continuation.count"), continuation.count ?? 0],
      [t("backgroundTasks.continuation.turnBudget"), ratioText(continuation.turnsUsed, continuation.maxTotalTurns)],
      [t("backgroundTasks.continuation.segmentTurns"), continuation.segmentTurns ?? "—"],
      [t("backgroundTasks.continuation.tokenBudget"), ratioText(continuation.tokensUsed, continuation.tokenBudget)],
      [t("backgroundTasks.continuation.timeBudget"), ratioText(continuation.elapsedMs == null ? null : formatDuration(continuation.elapsedMs), durationBudgetText(continuation.durationBudgetMs))],
      [t("backgroundTasks.continuation.waitingTask"), waitingTask?.title || continuation.waitingTaskId || "—"],
      [t("backgroundTasks.continuation.lastStop"), continuation.lastStop || "—"],
      [t("backgroundTasks.continuation.reason"), continuation.reason || "—"],
    ];
    return `<section class="conversation-continuation-card"><header><strong>${escapeHtml(t("backgroundTasks.continuation.title"))}</strong><span class="continuation-mode mode-${escapeAttr(continuation.mode || "off")}">${escapeHtml(mode)}</span></header><div class="conversation-continuation-grid">${items.map(([label, value]) => `<div><span>${escapeHtml(label)}</span><strong>${escapeHtml(value)}</strong></div>`).join("")}</div></section>`;
  }

  return {
    applySnapshot,
    bind,
    cancel,
    closeTab,
    closeTray,
    showOverview: () => {
      selected = "";
      emit("overview-selected");
    },
    getContinuation: () => readonlySnapshot(clonePublicValue(continuation)),
    getSummary: () => readonlySnapshot(publicSummarySnapshot()),
    getTask: getTaskSnapshot,
    getTaskByParentTool,
    handleEvent,
    loadAgent,
    loadOutput,
    loadTask,
    render,
    renderContinuationStatusHTML,
    selectTask,
    setAgent,
    setForegroundActivity,
    subscribe,
    wait,
    loadChildConversation,
    updateChildAgentSetting: updateChildAgent,
    sendChildAgentMessage: sendToChildAgent,
    // Exposed so the control markup can be asserted without a DOM: it renders
    // from the injected model list and the translated permission keys.
    renderChildControlsHTMLForTest: (task, agent = {}) => {
      const id = text(task?.childAgentId);
      if (id) childAgents.set(id, agent);
      return renderChildControlsHTML(task || {});
    },
    // Returns the rendered conversation, not the stored messages. The bug this
    // guards was a field name: the data was always there and only the read was
    // wrong, so a test that inspects the payload passes either way. Only the
    // rendered output shows whether the text actually reached the pane.
    renderChildConversationHTMLForTest: (task) => renderChildConversationHTML(task || {}),
    // Lets a test isolate one trigger for following a child from the others.
    stopChildPollingForTest: stopAllChildPolls,
    state: () => ({
      agentId,
      selected,
      openTabs: openTabIds(),
      order: [...order],
      tasksById: Object.fromEntries([...tasksById].map(([id, task]) => [id, { ...task }])),
      outputs: Object.fromEntries([...outputs].map(([id, chunks]) => [id, chunks.map((chunk) => ({ ...chunk }))])),
      outputCursors: Object.fromEntries(outputCursors),
      cancelBusy: [...cancelBusy],
      waitBusy: [...waitBusy],
      childConversations: Object.fromEntries([...childConversations].map(([id, list]) => [id, list.map((entry) => ({ ...entry }))])),
      // Which subagents the panel is currently following, so a test can tell a
      // finished poll from one that never started.
      childPolling: [...childPolls.keys()],
      continuation: { ...continuation },
      foregroundActivity: foregroundActivity ? { ...foregroundActivity } : null,
      trayOpen,
      loading,
      error,
    }),
  };
}
