export const executionNotificationDefaults = Object.freeze({
  storageKey: "autoto.executionNotifications.v1",
  maxEntries: 256,
  maxAgents: 64,
});

function normalizedGeneration(value) {
  if (value === null || value === undefined || value === "") return null;
  const generation = Number(value);
  return Number.isSafeInteger(generation) && generation >= 0 ? generation : null;
}

function normalizedLimit(value, fallback) {
  const limit = Number(value);
  return Number.isSafeInteger(limit) && limit > 0 ? limit : fallback;
}

function firstString(...values) {
  for (const value of values) {
    const text = String(value ?? "").trim();
    if (text) return text;
  }
  return "";
}

export function executionNotificationFamily(value) {
  const eventType = typeof value === "object" ? firstString(value?.type, value?.event, value?.data?.type, value?.data?.event) : "";
  const normalizedEventType = eventType.trim().toLowerCase().replaceAll("-", "_");
  const eventStatus = firstString(value?.status, value?.data?.status, value?.task?.status, value?.backgroundTask?.status).toLowerCase();
  if (normalizedEventType === "task.completed" || (normalizedEventType === "task.status" && ["completed", "complete", "success", "succeeded", "failed", "error", "cancelled", "canceled", "interrupted"].includes(eventStatus))) return "task_terminal";
  if (normalizedEventType === "agent.continuation_blocked") return "continuation_blocked";
  if (normalizedEventType === "agent.budget_exhausted") return "budget_exhausted";
  const candidate = typeof value === "string"
    ? value
    : firstString(
      value?.eventFamily,
      value?.family,
      value?.event,
      value?.kind,
      value?.type,
      value?.status,
      value?.data?.eventFamily,
      value?.data?.event,
      value?.data?.status,
    );
  const token = String(candidate || "").trim().toLowerCase().replaceAll("-", "_");
  if (!token) return "";
  if (["task_terminal", "continuation_blocked", "budget_exhausted"].includes(token)) return token;
  if (token === "approval_required" || token === "pending_approval" || token.endsWith(".approval_required")) return "approval_required";
  if (["done", "completed", "complete", "success", "succeeded"].includes(token) || token.endsWith(".done") || token.endsWith(".completed")) return "completed";
  if (["error", "failed", "failure"].includes(token) || token.endsWith(".error") || token.endsWith(".failed")) return "error";
  if (["interrupted", "cancelled", "canceled", "aborted"].includes(token) || token.endsWith(".interrupted")) return "interrupted";
  if (token === "superseded" || token.endsWith(".superseded")) return "superseded";
  if (token === "truncated") return "truncated";
  return "";
}

function notificationScope(value, family) {
  if (!value || typeof value !== "object") return "";
  const raw = value.raw && typeof value.raw === "object" ? value.raw : value;
  const data = raw.data && typeof raw.data === "object" ? raw.data : {};
  if (family === "task_terminal") {
    const id = firstString(value.taskId, raw.taskId, raw.backgroundTaskId, raw.task?.id, raw.backgroundTask?.id, data.taskId, data.backgroundTaskId, data.task?.id);
    return id ? `task:${id}` : "task";
  }
  if (family === "continuation_blocked") {
    const count = normalizedGeneration(value.continuationCount ?? raw.continuationCount ?? raw.count ?? data.continuationCount ?? data.count);
    const reason = firstString(value.reason, raw.reason, data.reason, data.blockedReason).slice(0, 80);
    return `continuation:${count ?? (reason || "blocked")}`;
  }
  if (family === "budget_exhausted") {
    const budget = firstString(value.budget, raw.budget, raw.budgetKind, data.budget, data.budgetKind, data.reason).slice(0, 80);
    return `budget:${budget || "exhausted"}`;
  }
  return "";
}

export function executionNotificationKey(agentId, executionGeneration, familyOrEvent) {
  const normalizedAgentId = String(agentId || "").trim();
  const generation = normalizedGeneration(executionGeneration);
  const family = executionNotificationFamily(familyOrEvent);
  if (!normalizedAgentId || generation === null || !family) return "";
  const scope = notificationScope(familyOrEvent, family);
  return `${normalizedAgentId}:${generation}:${scope ? `${scope}:` : ""}${family}`;
}

function loadState(storage, storageKey, maxEntries, maxAgents) {
  let parsed = null;
  try {
    const raw = storage?.getItem?.(storageKey);
    if (raw) parsed = JSON.parse(raw);
  } catch {}
  const seen = Array.isArray(parsed?.seen)
    ? [...new Set(parsed.seen.filter((key) => typeof key === "string" && key).reverse())].reverse().slice(-maxEntries)
    : [];
  const checkpoints = [];
  const rawCheckpoints = Array.isArray(parsed?.checkpoints)
    ? parsed.checkpoints
    : Object.entries(parsed?.checkpoints || {}).map(([agentId, generation]) => ({ agentId, generation }));
  for (const checkpoint of rawCheckpoints) {
    const agentId = String(checkpoint?.agentId || "").trim();
    const generation = normalizedGeneration(checkpoint?.generation ?? checkpoint?.executionGeneration);
    if (!agentId || generation === null) continue;
    const previous = checkpoints.find((entry) => entry.agentId === agentId);
    if (previous) previous.generation = Math.max(previous.generation, generation);
    else checkpoints.push({ agentId, generation });
  }
  return { seen, checkpoints: checkpoints.slice(-maxAgents) };
}

function snapshotAgentId(snapshot, fallback = "") {
  return firstString(snapshot?.agentId, snapshot?.agent?.id, fallback);
}

function snapshotExecutionGeneration(snapshot) {
  return normalizedGeneration(
    snapshot?.executionGeneration
      ?? snapshot?.latestExecutionGeneration
      ?? snapshot?.checkpoint?.executionGeneration
      ?? snapshot?.agent?.executionGeneration,
  );
}

function notificationRecord(value, fallback = {}) {
  if (!value || typeof value !== "object") return null;
  const run = value.run && typeof value.run === "object" ? value.run : null;
  const data = value.data && typeof value.data === "object" ? value.data : null;
  const agentId = firstString(value.agentId, run?.agentId, data?.agentId, fallback.agentId);
  let executionGeneration = normalizedGeneration(
    value.executionGeneration
      ?? value.generation
      ?? run?.executionGeneration
      ?? data?.executionGeneration
      ?? fallback.executionGeneration,
  );
  const family = executionNotificationFamily(value);
  if (executionGeneration === null && ["task_terminal", "continuation_blocked", "budget_exhausted"].includes(family)) executionGeneration = 0;
  if (!agentId || executionGeneration === null || !family) return null;
  return {
    agentId,
    executionGeneration,
    family,
    runId: firstString(value.runId, value.id, run?.id, data?.runId),
    toolUseId: firstString(value.toolUseId, data?.toolUseId),
    taskId: firstString(value.taskId, value.backgroundTaskId, value.task?.id, value.backgroundTask?.id, data?.taskId, data?.backgroundTaskId, data?.task?.id),
    continuationCount: normalizedGeneration(value.continuationCount ?? value.count ?? data?.continuationCount ?? data?.count),
    reason: firstString(value.reason, data?.reason, data?.blockedReason),
    reasonCode: firstString(value.reasonCode, data?.reasonCode),
    budget: firstString(value.budget, value.budgetKind, data?.budget, data?.budgetKind),
    raw: value,
  };
}

function snapshotRecords(snapshot, fallbackAgentId, fallbackGeneration) {
  const records = [];
  const collections = [
    snapshot?.executionsSince,
    snapshot?.executionEvents,
    snapshot?.executionNotifications?.events,
    snapshot?.notifications,
    snapshot?.events,
    snapshot?.runsSince,
  ];
  for (const collection of collections) {
    if (!Array.isArray(collection)) continue;
    records.push(...collection);
    break;
  }
  if (snapshot?.latestRun && typeof snapshot.latestRun === "object") records.push(snapshot.latestRun);
  if (Array.isArray(snapshot?.pendingApprovals)) {
    for (const approval of snapshot.pendingApprovals) {
      records.push({
        ...approval,
        eventFamily: "approval_required",
        executionGeneration: approval?.executionGeneration ?? fallbackGeneration,
        agentId: approval?.agentId || fallbackAgentId,
      });
    }
  }
  for (const task of [...(Array.isArray(snapshot?.backgroundTasks) ? snapshot.backgroundTasks : []), ...(Array.isArray(snapshot?.recentBackgroundTasks) ? snapshot.recentBackgroundTasks : [])]) {
    const status = firstString(task?.status, task?.state).toLowerCase();
    if (!["completed", "complete", "success", "succeeded", "failed", "error", "cancelled", "canceled", "interrupted"].includes(status)) continue;
    records.push({
      ...task,
      type: "task.completed",
      taskId: firstString(task?.taskId, task?.id),
      executionGeneration: task?.executionGeneration ?? fallbackGeneration,
      agentId: task?.agentId || fallbackAgentId,
    });
  }
  const continuation = snapshot?.continuation;
  if (continuation && typeof continuation === "object") {
    const status = firstString(continuation.status, continuation.state).toLowerCase();
    if (status === "blocked" || status === "continuation_blocked") records.push({ ...continuation, type: "agent.continuation_blocked", executionGeneration: continuation.executionGeneration ?? fallbackGeneration, agentId: fallbackAgentId });
    if (status === "budget_exhausted" || continuation.budgetExhausted === true) records.push({ ...continuation, type: "agent.budget_exhausted", executionGeneration: continuation.executionGeneration ?? fallbackGeneration, agentId: fallbackAgentId });
  }
  const unique = new Map();
  for (const value of records) {
    const record = notificationRecord(value, { agentId: fallbackAgentId, executionGeneration: fallbackGeneration });
    if (!record) continue;
    const key = executionNotificationKey(record.agentId, record.executionGeneration, record);
    if (key && !unique.has(key)) unique.set(key, record);
  }
  return [...unique.values()];
}

function snapshotIsTruncated(snapshot) {
  return Boolean(
    snapshot?.executionsTruncated
      ?? snapshot?.executionNotifications?.truncated
      ?? snapshot?.notificationsTruncated,
  );
}

export function createExecutionNotifications({
  storage = globalThis.sessionStorage,
  notifier = () => {},
  onError,
  storageKey = executionNotificationDefaults.storageKey,
  maxEntries = executionNotificationDefaults.maxEntries,
  maxAgents = executionNotificationDefaults.maxAgents,
} = {}) {
  const seenLimit = normalizedLimit(maxEntries, executionNotificationDefaults.maxEntries);
  const agentLimit = normalizedLimit(maxAgents, executionNotificationDefaults.maxAgents);
  const state = loadState(storage, storageKey, seenLimit, agentLimit);
  const seen = new Set(state.seen);
  let seenOrder = [...state.seen];
  let checkpoints = [...state.checkpoints];

  function persist() {
    try {
      storage?.setItem?.(storageKey, JSON.stringify({ version: 1, seen: seenOrder, checkpoints }));
    } catch {}
  }

  function rememberKey(key) {
    if (!key || seen.has(key)) return false;
    seen.add(key);
    seenOrder.push(key);
    while (seenOrder.length > seenLimit) {
      const oldest = seenOrder.shift();
      seen.delete(oldest);
    }
    return true;
  }

  function updateCheckpoint(agentId, generation) {
    const normalizedAgentId = String(agentId || "").trim();
    const normalized = normalizedGeneration(generation);
    if (!normalizedAgentId || normalized === null) return false;
    const existing = checkpoints.find((entry) => entry.agentId === normalizedAgentId);
    if (existing && existing.generation >= normalized) return false;
    checkpoints = checkpoints.filter((entry) => entry.agentId !== normalizedAgentId);
    checkpoints.push({ agentId: normalizedAgentId, generation: normalized });
    if (checkpoints.length > agentLimit) checkpoints = checkpoints.slice(-agentLimit);
    return true;
  }

  function checkpoint(agentId) {
    const normalizedAgentId = String(agentId || "").trim();
    return checkpoints.find((entry) => entry.agentId === normalizedAgentId)?.generation ?? 0;
  }

  async function notify(payload) {
    try {
      if (typeof notifier === "function") await notifier(payload);
      else if (typeof notifier?.notify === "function") await notifier.notify(payload);
    } catch (error) {
      onError?.(error);
    }
  }

  async function processRecords(records, { source, silent = false, truncated = false, snapshotGeneration = null, agentId = "" } = {}) {
    let changed = false;
    let duplicateCount = 0;
    const fresh = [];
    for (const record of records) {
      changed = updateCheckpoint(record.agentId, record.executionGeneration) || changed;
      const key = executionNotificationKey(record.agentId, record.executionGeneration, record);
      if (!rememberKey(key)) {
        duplicateCount += 1;
        continue;
      }
      changed = true;
      fresh.push({ ...record, key, source });
    }

    const primaryAgentId = firstString(agentId, records[0]?.agentId);
    if (primaryAgentId && snapshotGeneration !== null) {
      changed = updateCheckpoint(primaryAgentId, snapshotGeneration) || changed;
    }

    let summary = null;
    if (truncated && primaryAgentId) {
      const summaryGeneration = normalizedGeneration(snapshotGeneration)
        ?? Math.max(checkpoint(primaryAgentId), ...records.map((record) => record.executionGeneration));
      const key = executionNotificationKey(primaryAgentId, summaryGeneration, "truncated");
      if (rememberKey(key)) {
        changed = true;
        summary = {
          key,
          agentId: primaryAgentId,
          executionGeneration: summaryGeneration,
          family: "truncated",
          source,
          truncated: true,
          recoveredCount: fresh.length,
          duplicateCount,
        };
      }
    }

    if (changed) persist();
    if (!silent) {
      if (truncated) {
        if (summary) await notify(summary);
      } else {
        for (const payload of fresh) await notify(payload);
      }
    }
    return {
      notified: silent ? 0 : (truncated ? Number(Boolean(summary)) : fresh.length),
      fresh: fresh.length,
      duplicates: duplicateCount,
      truncated,
      checkpoint: primaryAgentId ? checkpoint(primaryAgentId) : 0,
    };
  }

  async function live(event, options = {}) {
    const record = notificationRecord(event, {
      agentId: options.agentId,
      executionGeneration: options.executionGeneration,
    });
    if (!record) return { notified: 0, fresh: 0, duplicates: 0, ignored: true };
    return processRecords([record], { source: "live" });
  }

  async function ingestSnapshot(snapshot, { initial = false, agentId = "" } = {}) {
    const resolvedAgentId = snapshotAgentId(snapshot, agentId);
    const generation = snapshotExecutionGeneration(snapshot);
    const records = snapshotRecords(snapshot, resolvedAgentId, generation);
    let changed = false;
    if (resolvedAgentId && generation !== null) changed = updateCheckpoint(resolvedAgentId, generation);
    if (changed) persist();
    const truncated = snapshotIsTruncated(snapshot);
    if (records.length === 0 && !truncated) {
      return { notified: 0, fresh: 0, duplicates: 0, checkpoint: checkpoint(resolvedAgentId), initial };
    }
    return processRecords(records, {
      source: initial ? "initial" : "snapshot",
      silent: initial,
      truncated,
      snapshotGeneration: generation,
      agentId: resolvedAgentId,
    });
  }

  function initial(snapshot, options = {}) {
    return ingestSnapshot(snapshot, { ...options, initial: true });
  }

  function snapshot(snapshotValue, options = {}) {
    return ingestSnapshot(snapshotValue, { ...options, initial: false });
  }

  function clear(agentId = "") {
    const normalizedAgentId = String(agentId || "").trim();
    if (!normalizedAgentId) {
      seen.clear();
      seenOrder = [];
      checkpoints = [];
    } else {
      const prefix = `${normalizedAgentId}:`;
      seenOrder = seenOrder.filter((key) => !key.startsWith(prefix));
      seen.clear();
      seenOrder.forEach((key) => seen.add(key));
      checkpoints = checkpoints.filter((entry) => entry.agentId !== normalizedAgentId);
    }
    persist();
  }

  return {
    initial,
    initialize: initial,
    live,
    handleEvent: live,
    snapshot,
    handleSnapshot: snapshot,
    ingestSnapshot,
    checkpoint,
    getCheckpoint: checkpoint,
    clear,
    state: () => ({ seen: [...seenOrder], checkpoints: checkpoints.map((entry) => ({ ...entry })) }),
  };
}
