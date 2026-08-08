import { escapeAttr, escapeHtml, setHTMLIfChanged, setTextIfChanged } from "./dom.mjs";
import { formatDuration, formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs";

const terminalStatuses = new Set(["completed", "complete", "succeeded", "success", "failed", "error", "cancelled", "canceled", "interrupted"]);
const runningStatuses = new Set(["running", "started", "cancel_requested"]);
const queuedStatuses = new Set(["queued", "pending", "waiting", "waiting_approval"]);
const cancellableStatuses = new Set(["queued", "pending", "waiting", "waiting_approval", "running", "started", "cancel_requested"]);
const defaultOutputLimit = 24000;

function text(value) {
  return String(value ?? "").trim();
}

function normalizeForegroundActivity(value) {
  const label = text(value?.text || value?.label);
  if (!label) return null;
  const tone = text(value?.tone);
  return {
    kind: text(value?.kind) || "activity",
    text: label,
    // Only the tones the dot has a style for; anything else falls back to the
    // working blue rather than rendering an unstyled dot.
    tone: tone === "waiting" || tone === "queued" ? tone : "running",
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
  let detailView = "conversation";
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

  // Loaded on demand rather than with the task list: most tasks are never
  // opened, and each one costs a separate conversation fetch.
  async function loadChildConversation(childAgentId, { force = false } = {}) {
    const id = text(childAgentId);
    if (!id || childBusy.has(id)) return;
    if (!force && childConversations.has(id)) return;
    childBusy.add(id);
    try {
      const [messages, agent] = await Promise.all([
        request(`/api/agents/${encodeURIComponent(id)}/messages?limit=40`).catch(() => null),
        request(`/api/agents/${encodeURIComponent(id)}`).catch(() => null),
      ]);
      const list = Array.isArray(messages) ? messages : (Array.isArray(messages?.messages) ? messages.messages : []);
      childConversations.set(id, list);
      if (agent && typeof agent === "object") childAgents.set(id, agent);
      render();
    } finally {
      childBusy.delete(id);
    }
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
      await loadChildConversation(id, { force: true });
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
        body: JSON.stringify({ text: body, mode: "execute", context: "conversation" }),
      });
      await loadChildConversation(id, { force: true });
    } catch (err) {
      onError?.(err);
    }
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
      const childAgentId = text(tasksById.get(normalized)?.childAgentId);
      if (childAgentId) await loadChildConversation(childAgentId);
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
    return `<div class="background-task-conversation">${messages.map((message) => {
      const role = text(message?.role) === "user" ? "user" : "assistant";
      const body = text(message?.text || message?.content);
      if (!body) return "";
      return `<article class="background-task-bubble role-${role}">
        <header><span>${escapeHtml(role === "user" ? t("backgroundTasks.roleUser") : t("backgroundTasks.roleAgent"))}</span><time>${escapeHtml(message?.createdAt ? formatTimestamp(message.createdAt) : "")}</time></header>
        <p>${escapeHtml(body)}</p>
      </article>`;
    }).join("")}</div>`;
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
    </form>`;
  }

  function renderSelectedTask(task) {
    if (!task) return `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.selectTask"))}</div>`;
    const output = taskOutputText(task.id);
    const canCancel = cancellableStatuses.has(task.status);
    const isTerminal = terminalStatuses.has(task.status);
    const hasChild = Boolean(text(task.childAgentId));
    // Conversation is the default view because "what did it say" is the usual
    // question; raw output stays one click away for shell tasks and diagnostics.
    const view = hasChild ? detailView : "output";
    // No title block here: the active tab above already names the task and shows
    // its status, and repeating both cost a third of the pane.
    return `<section class="background-task-detail">
      <div class="background-task-meta"><span class="background-task-state status-${escapeAttr(task.status)}">${escapeHtml(taskStatusLabel(task.status))}</span><span>${escapeHtml(task.createdAt ? formatTimestamp(task.createdAt) : "—")}</span><span>${escapeHtml(task.durationMs == null ? "—" : formatDuration(task.durationMs))}</span></div>
      ${task.error || task.errorCode ? `<div class="background-task-error">${escapeHtml([task.errorCode ? t("backgroundTasks.errorCode", { code: task.errorCode }) : "", task.error ? t("backgroundTasks.errorMessage", { message: task.error }) : ""].filter(Boolean).join(" · "))}</div>` : ""}
      ${hasChild ? `<div class="background-task-view-tabs" role="tablist">
        <button type="button" role="tab" class="${view === "conversation" ? "active" : ""}" aria-selected="${view === "conversation" ? "true" : "false"}" data-background-view="conversation">${escapeHtml(t("backgroundTasks.viewConversation"))}</button>
        <button type="button" role="tab" class="${view === "output" ? "active" : ""}" aria-selected="${view === "output" ? "true" : "false"}" data-background-view="output">${escapeHtml(t("backgroundTasks.viewOutput"))}</button>
      </div>` : ""}
      ${view === "conversation"
        ? renderChildConversationHTML(task)
        : `<pre class="background-task-output">${escapeHtml(output || t("backgroundTasks.noOutput"))}</pre>`}
      ${task.truncated && view === "output" ? `<div class="background-task-truncated">${escapeHtml(t("backgroundTasks.truncated"))}</div>` : ""}
      ${view === "conversation" ? renderChildControlsHTML(task) : ""}
      <div class="background-task-actions">
        ${task.outputHasMore && view === "output" ? `<button type="button" class="ghost-btn mini" data-background-output-more="${escapeAttr(task.id)}">${escapeHtml(t("backgroundTasks.loadMore"))}</button>` : ""}
        ${!isTerminal ? `<button type="button" class="ghost-btn mini" data-background-wait="${escapeAttr(task.id)}" ${waitBusy.has(task.id) ? "disabled" : ""}>${escapeHtml(waitBusy.has(task.id) ? t("backgroundTasks.waiting") : t("backgroundTasks.wait"))}</button>` : ""}
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
      const target = event.target?.closest?.("[data-background-task],[data-background-close],[data-background-output-more],[data-background-wait],[data-background-cancel],[data-background-agent],[data-background-run],[data-background-view],[data-background-overview],[data-background-tab-close]");
      if (!target) return;
      if (target.hasAttribute("data-background-close")) {
        closeTray();
      } else if (target.hasAttribute("data-background-overview")) {
        selected = "";
        emit("overview-selected");
      } else if (target.dataset.backgroundTabClose) {
        closeTab(target.dataset.backgroundTabClose);
      } else if (target.dataset.backgroundView) {
        detailView = target.dataset.backgroundView === "output" ? "output" : "conversation";
        render();
      } else if (target.dataset.backgroundTask) {
        // Opens inside this panel only. Navigating the main conversation as well
        // hijacked the window: the user was reading one thread, and a glance at a
        // subagent replaced it and lost their place.
        selectTask(target.dataset.backgroundTask).catch(onError);
      } else if (target.dataset.backgroundOutputMore) loadOutput(target.dataset.backgroundOutputMore).catch(onError);
      else if (target.dataset.backgroundWait) wait(target.dataset.backgroundWait).catch(onError);
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
      continuation: { ...continuation },
      foregroundActivity: foregroundActivity ? { ...foregroundActivity } : null,
      trayOpen,
      loading,
      error,
    }),
  };
}
