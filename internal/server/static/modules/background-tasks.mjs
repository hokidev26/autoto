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
  return {
    kind: text(value?.kind) || "activity",
    text: label,
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
    errorCode: text(source.errorCode || source.error_code || fallback.errorCode),
    error: text(source.error || source.errorMessage || source.error_message || (terminalStatuses.has(status) ? source.message : "") || fallback.error),
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
  const hydrationRequests = new Map();
  const listeners = new Set();
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
      errorCode: task.errorCode,
      error: task.error,
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
      setTrayOpen(true, "task-selected");
      emit("selected");
      if (alreadyLoaded) await loadTask(normalized);
      if (!operationIsCurrent(expectedAgentId, expectedGeneration)) return null;
      if (!(outputs.get(normalized) || []).length) await loadOutput(normalized, { afterSequence: 0 });
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
    if (next?.kind === foregroundActivity?.kind && next?.text === foregroundActivity?.text) return true;
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

  function renderTaskRow(task) {
    const active = task.id === selected;
    const duration = task.durationMs === null || task.durationMs === undefined ? "—" : formatDuration(task.durationMs);
    const preview = text(task.error || task.summary?.description || task.summary?.command);
    return `<button class="background-task-row ${active ? "active" : ""}" type="button" data-background-task="${escapeAttr(task.id)}" aria-pressed="${active ? "true" : "false"}">
      <span class="background-task-kind">${escapeHtml(task.kind || t("backgroundTasks.task"))}</span>
      <span class="background-task-main"><strong>${escapeHtml(task.title || task.id)}</strong><small>${escapeHtml(preview || `${taskStatusLabel(task.status)} · ${duration}`)}</small></span>
      <span class="background-task-row-state status-${escapeAttr(task.status)}">${escapeHtml(taskStatusLabel(task.status))}</span>
    </button>`;
  }

  function renderSelectedTask(task) {
    if (!task) return `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.selectTask"))}</div>`;
    const output = taskOutputText(task.id);
    const canCancel = cancellableStatuses.has(task.status);
    const isTerminal = terminalStatuses.has(task.status);
    return `<section class="background-task-detail">
      <header><div><span>${escapeHtml(task.kind)}</span><strong>${escapeHtml(task.title || task.id)}</strong></div><span class="background-task-state status-${escapeAttr(task.status)}">${escapeHtml(taskStatusLabel(task.status))}</span></header>
      <div class="background-task-meta"><span>${escapeHtml(task.createdAt ? formatTimestamp(task.createdAt) : "—")}</span><span>${escapeHtml(task.durationMs == null ? "—" : formatDuration(task.durationMs))}</span></div>
      ${task.error ? `<div class="background-task-error">${escapeHtml(task.error)}</div>` : ""}
      <pre class="background-task-output">${escapeHtml(output || t("backgroundTasks.noOutput"))}</pre>
      ${task.truncated ? `<div class="background-task-truncated">${escapeHtml(t("backgroundTasks.truncated"))}</div>` : ""}
      <div class="background-task-actions">
        ${task.outputHasMore ? `<button type="button" class="ghost-btn mini" data-background-output-more="${escapeAttr(task.id)}">${escapeHtml(t("backgroundTasks.loadMore"))}</button>` : ""}
        ${!isTerminal ? `<button type="button" class="ghost-btn mini" data-background-wait="${escapeAttr(task.id)}" ${waitBusy.has(task.id) ? "disabled" : ""}>${escapeHtml(waitBusy.has(task.id) ? t("backgroundTasks.waiting") : t("backgroundTasks.wait"))}</button>` : ""}
        <button type="button" class="ghost-btn mini danger" data-background-cancel="${escapeAttr(task.id)}" ${canCancel && !cancelBusy.has(task.id) ? "" : "disabled"}>${escapeHtml(cancelBusy.has(task.id) ? t("backgroundTasks.cancelling") : t("backgroundTasks.cancel"))}</button>
        ${task.childAgentId ? `<button type="button" class="ghost-btn mini" data-background-agent="${escapeAttr(task.childAgentId)}">${escapeHtml(t("backgroundTasks.openChildAgent"))}</button>` : ""}
        ${task.childRunId ? `<button type="button" class="ghost-btn mini" data-background-run="${escapeAttr(task.childRunId)}" data-background-run-agent="${escapeAttr(task.childAgentId || task.agentId)}">${escapeHtml(t("backgroundTasks.openChildRun"))}</button>` : ""}
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
      ? "running"
      : runningStatuses.has(currentStatus) ? "running" : queuedStatuses.has(currentStatus) ? "queued" : "idle";
    const currentText = foregroundActivity?.text || summary.current?.title || t("backgroundTasks.headerIdle");
    const hasCurrentActivity = Boolean(foregroundActivity || summary.current);
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
          <div class="background-task-list">${loading && !tasks.length ? `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.loading"))}</div>` : tasks.length ? tasks.map(renderTaskRow).join("") : `<div class="background-task-empty">${escapeHtml(t("backgroundTasks.empty"))}</div>`}</div>
          ${renderSelectedTask(tasksById.get(selected))}
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
    if (nextOpen && !selected) selected = taskSummary().current?.id || order[0] || "";
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
      const target = event.target?.closest?.("[data-background-task],[data-background-close],[data-background-output-more],[data-background-wait],[data-background-cancel],[data-background-agent],[data-background-run]");
      if (!target) return;
      if (target.hasAttribute("data-background-close")) {
        closeTray();
      } else if (target.dataset.backgroundTask) selectTask(target.dataset.backgroundTask).catch(onError);
      else if (target.dataset.backgroundOutputMore) loadOutput(target.dataset.backgroundOutputMore).catch(onError);
      else if (target.dataset.backgroundWait) wait(target.dataset.backgroundWait).catch(onError);
      else if (target.dataset.backgroundCancel) cancel(target.dataset.backgroundCancel).catch(onError);
      else if (target.dataset.backgroundAgent) onNavigateAgent?.(target.dataset.backgroundAgent);
      else if (target.dataset.backgroundRun) onNavigateRun?.(target.dataset.backgroundRunAgent, target.dataset.backgroundRun);
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
    closeTray,
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
    state: () => ({
      agentId,
      selected,
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
