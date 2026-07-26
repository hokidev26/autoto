import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatTimestamp as formatRegionalTimestamp } from "./formatters.mjs";
import { t } from "./messages-automation.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

export const automationLimits = Object.freeze({
  schedules: 100,
  scheduleRuns: 20,
  deliveries: 60,
  connections: 40,
  pairings: 80,
  devices: 200,
  deviceActions: 60,
  auditEvents: 50,
  activity: 40,
});

export const schedulePresets = Object.freeze(["@every 15m", "@hourly", "@daily"]);
export const legacyIMDraftKeys = Object.freeze(["autoto.imGateway", "codeharbor.imGateway"]);
const ENV_REFERENCE_PATTERN = /^env:[A-Za-z_][A-Za-z0-9_]*$/;
const SAFE_PERMISSION_MODES = new Set(["readOnly", "acceptEdits"]);
const ENVIRONMENT_MODES = new Set(["workline", "standalone"]);
const NARRATOR_MODES = new Set(["reuse", "new"]);
const SCHEDULE_ENUM_KEYS = Object.freeze({
  permissionMode: Object.freeze({ readOnly: "automation.schedule.permissionModes.readOnly", acceptEdits: "automation.schedule.permissionModes.acceptEdits" }),
  environmentMode: Object.freeze({ workline: "automation.schedule.environmentModes.workline", standalone: "automation.schedule.environmentModes.standalone" }),
  narratorMode: Object.freeze({ reuse: "automation.schedule.narratorModes.reuse", new: "automation.schedule.narratorModes.new" }),
});
const HOME_ASSISTANT_KINDS = new Set(["home_assistant", "home-assistant", "homeassistant", "ha"]);

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function boundedText(value, limit = 240) {
  return String(value ?? "").trim().slice(0, limit);
}

export function formatScheduleEnumValue(group, value) {
  let normalized = "";
  try {
    normalized = boundedText(value, 80);
  } catch {
    normalized = "";
  }
  const key = SCHEDULE_ENUM_KEYS[group]?.[normalized];
  if (key) return t(key);
  return t("automation.schedule.unknownValue", { value: normalized || t("automation.defaults.unknown") });
}

function scheduleEnumSource(value, field, fallback) {
  try {
    const candidate = objectValue(value)[field];
    return candidate === undefined || candidate === null || candidate === "" ? fallback : candidate;
  } catch {
    return fallback;
  }
}

function boundedNumber(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : fallback;
}

function boundedList(payload, keys, limit) {
  if (Array.isArray(payload)) return payload.slice(0, limit);
  const source = objectValue(payload);
  for (const key of keys) {
    if (Array.isArray(source[key])) return source[key].slice(0, limit);
  }
  if (source.data && source.data !== payload) return boundedList(source.data, keys, limit);
  return [];
}

function booleanValue(value, fallback = false) {
  if (value === undefined || value === null) return fallback;
  if (typeof value === "string") {
    if (["false", "0", "off", "disabled"].includes(value.toLowerCase())) return false;
    if (["true", "1", "on", "enabled"].includes(value.toLowerCase())) return true;
  }
  return Boolean(value);
}

function normalizeKind(value) {
  const kind = boundedText(value, 48).toLowerCase();
  if (HOME_ASSISTANT_KINDS.has(kind)) return "home-assistant";
  return kind;
}

function normalizeRisk(value) {
  const risk = boundedText(value, 24).toLowerCase();
  if (["critical", "high", "medium", "low", "blocked"].includes(risk)) return risk;
  if (risk === "danger") return "high";
  if (["warning", "warn"].includes(risk)) return "medium";
  return "low";
}

function normalizeStatus(value, fallback = "unknown") {
  return boundedText(value || fallback, 48).toLowerCase().replace(/\s+/g, "_");
}

function firstText(...values) {
  for (const value of values) {
    const text = boundedText(value, 800);
    if (text) return text;
  }
  return "";
}

function compactJSON(value) {
  if (value == null || value === "") return "";
  if (typeof value === "string") return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

export function redactSensitiveText(value) {
  return String(value ?? "")
    .replace(/\b(Bearer\s+)[A-Za-z0-9._~+\/-]+/gi, "$1[REDACTED]")
    .replace(/\b\d{5,}:[A-Za-z0-9_-]{20,}\b/g, "[REDACTED]")
    .replace(/\b(token|secret|password|api[-_ ]?key)(\s*[:=]\s*)([^\s,;]+)/gi, "$1$2[REDACTED]")
    .slice(0, 1200);
}

export function normalizeEnvReference(value, { required = true, label = t("automation.defaults.credential") } = {}) {
  const reference = boundedText(value, 160);
  if (!reference && !required) return "";
  if (!ENV_REFERENCE_PATTERN.test(reference)) {
    throw new Error(t("automation.validation.envReference", { label }));
  }
  return reference;
}

export function normalizeSchedule(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.scheduleId, 160),
    name: boundedText(source.name ?? source.title, 120),
    expression: firstText(source.expression, source.cronExpr, source.cron, source.schedule),
    timezone: boundedText(source.timezone, 128) || "UTC",
    agentId: boundedText(source.agentId ?? source.projectId ?? source.workspaceId, 160),
    prompt: boundedText(source.prompt ?? source.task ?? source.message, 8000),
    permissionMode: SAFE_PERMISSION_MODES.has(source.permissionMode) ? source.permissionMode : "readOnly",
    environmentMode: ENVIRONMENT_MODES.has(source.environmentMode) ? source.environmentMode : "workline",
    narratorMode: NARRATOR_MODES.has(source.narratorMode) ? source.narratorMode : "reuse",
    enabled: booleanValue(source.enabled, normalizeStatus(source.status) === "enabled"),
    status: normalizeStatus(source.status, source.enabled === false ? "disabled" : "ready"),
    nextRunAt: boundedText(source.nextRunAt ?? source.nextAt, 80),
    lastRunAt: boundedText(source.lastRunAt ?? source.lastTriggeredAt, 80),
    lastRunId: boundedText(source.lastRunId, 160),
    lastOutcome: normalizeStatus(source.lastOutcome, ""),
    lastError: redactSensitiveText(source.lastError ?? source.error),
  };
}

export function normalizeScheduleRun(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.runId, 160),
    triggerType: normalizeStatus(source.triggerType ?? source.source, "manual"),
    durationMs: boundedNumber(source.durationMs ?? source.duration, 0),
    status: normalizeStatus(source.status, "unknown"),
    createdAt: boundedText(source.startedAt ?? source.createdAt, 80),
  };
}

export function normalizeDelivery(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.deliveryId, 160),
    channel: boundedText(source.channel ?? source.sinkType ?? source.connectionKind ?? source.kind, 80),
    event: boundedText(source.event ?? source.eventType ?? source.kind, 120),
    status: normalizeStatus(source.status, source.deliveredAt ? "delivered" : "pending"),
    attempts: boundedNumber(source.attempts ?? source.attemptCount, 0),
    error: redactSensitiveText(source.error ?? source.lastError),
    createdAt: boundedText(source.createdAt ?? source.queuedAt, 80),
    deliveredAt: boundedText(source.deliveredAt ?? source.sentAt, 80),
  };
}

export function normalizeConnection(value = {}) {
  const source = objectValue(value);
  const config = objectValue(source.config);
  const secretConfigured = objectValue(source.secretConfigured);
  const kind = normalizeKind(source.kind ?? source.type ?? source.provider);
  const enabled = booleanValue(source.enabled, true);
  return {
    id: boundedText(source.id ?? source.connectionId, 160),
    kind,
    name: boundedText(source.name ?? source.label ?? (kind === "telegram" ? "Telegram" : "Home Assistant"), 160),
    enabled,
    status: normalizeStatus(source.status, enabled ? "enabled" : "disabled"),
    endpoint: boundedText(source.endpoint ?? source.baseUrl ?? source.url ?? config.baseUrl ?? config.url, 400),
    credentialConfigured: booleanValue(source.credentialConfigured, kind === "telegram" ? Boolean(secretConfigured.botToken) : Boolean(secretConfigured.accessToken)),
    lastTestedAt: boundedText(source.lastTestedAt ?? source.testedAt, 80),
    lastError: redactSensitiveText(source.lastError ?? source.error),
  };
}

export function normalizePairing(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.pairingId, 160),
    connectionId: boundedText(source.connectionId, 160),
    agentId: boundedText(source.agentId, 160),
    channelUser: boundedText(source.channelUser ?? source.username ?? source.chatId ?? source.externalId, 180),
    status: normalizeStatus(source.status, source.revokedAt ? "revoked" : "active"),
    pairedAt: boundedText(source.pairedAt ?? source.createdAt, 80),
    revokedAt: boundedText(source.revokedAt, 80),
  };
}

export function normalizePairingCode(value = {}) {
  const source = objectValue(value);
  return {
    code: boundedText(source.code ?? source.pairingCode, 160),
    connectionId: boundedText(source.connectionId, 160),
    agentId: boundedText(source.agentId, 160),
    expiresAt: boundedText(source.expiresAt ?? source.expiry, 80),
  };
}

export function normalizeDevice(value = {}) {
  const source = objectValue(value);
  const attributes = objectValue(source.attributes);
  const entityId = boundedText(source.entityId ?? source.id ?? source.deviceId, 220);
  return {
    id: entityId,
    entityId,
    name: boundedText(source.name ?? source.friendlyName ?? attributes.friendly_name ?? entityId, 220),
    domain: boundedText(source.domain ?? entityId.split(".")[0], 80),
    state: boundedText(source.state ?? source.value ?? "unknown", 160),
    available: booleanValue(source.available, source.state !== "unavailable"),
    readOnly: booleanValue(source.readOnly, true),
    updatedAt: boundedText(source.updatedAt ?? source.lastChanged ?? source.lastUpdated, 80),
  };
}

export function normalizeDeviceAction(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.actionId, 160),
    connectionId: boundedText(source.connectionId, 160),
    entityId: boundedText(source.entityId ?? source.deviceId, 220),
    domain: boundedText(source.domain, 80),
    service: boundedText(source.service ?? source.action, 160),
    risk: normalizeRisk(source.risk ?? source.riskLevel),
    status: normalizeStatus(source.status, "pending"),
    expiresAt: boundedText(source.expiresAt ?? source.expiry, 80),
    requestedAt: boundedText(source.requestedAt ?? source.createdAt, 80),
    error: redactSensitiveText(source.error ?? source.lastError),
  };
}

export function normalizeAuditEvent(value = {}) {
  const source = objectValue(value);
  const action = boundedText(source.action ?? source.type ?? source.event, 160);
  const category = boundedText(source.category, 80);
  return {
    id: boundedText(source.id ?? source.eventId, 160),
    type: [category, action].filter(Boolean).join(" / "),
    actor: boundedText(source.actor ?? source.source ?? source.principal, 160),
    result: normalizeStatus(source.outcome ?? source.result ?? source.status, "recorded"),
    risk: normalizeRisk(source.risk),
    summary: redactSensitiveText(source.summary ?? source.message ?? source.detail ?? compactJSON(source.details)),
    createdAt: boundedText(source.createdAt ?? source.timestamp ?? source.occurredAt, 80),
  };
}

export function normalizeMonitoringSnapshot(value = {}) {
  const source = objectValue(value);
  const counts = objectValue(source.counts);
  const schedules = boundedList(source, ["schedules"], automationLimits.schedules).map(normalizeSchedule);
  const deliveries = boundedList(source, ["deliveries", "notifications"], automationLimits.deliveries).map(normalizeDelivery);
  const connections = boundedList(source, ["connections", "channels"], automationLimits.connections).map(normalizeConnection);
  const devices = boundedList(source, ["devices", "entities"], automationLimits.devices).map(normalizeDevice);
  const deviceActions = boundedList(source, ["deviceActions", "pendingDeviceActions", "actions"], automationLimits.deviceActions).map(normalizeDeviceAction);
  const runs = objectValue(source.runs);
  const approvals = objectValue(source.approvals);
  const scheduleStats = objectValue(source.schedules);
  const notificationStats = objectValue(source.notifications);
  const channelStats = objectValue(source.channels);
  const deviceStats = objectValue(source.devices);
  return {
    activeRuns: boundedNumber(source.activeRuns ?? counts.activeRuns ?? runs.active ?? runs.running, 0),
    pendingApprovals: boundedNumber(source.pendingApprovals ?? counts.pendingApprovals ?? approvals.pending, 0),
    scheduleCount: boundedNumber(source.scheduleCount ?? counts.schedules ?? scheduleStats.total, schedules.length),
    notificationCount: boundedNumber(source.notificationCount ?? counts.notifications ?? notificationStats.total, deliveries.length),
    channelCount: boundedNumber(source.channelCount ?? counts.channels ?? channelStats.total ?? channelStats.connections, connections.length),
    deviceCount: boundedNumber(source.deviceCount ?? counts.devices ?? deviceStats.total, devices.length),
    capturedAt: boundedText(source.capturedAt ?? source.createdAt, 80),
    deviceActions,
  };
}

export function normalizeAutomationSnapshot(value = {}) {
  const source = objectValue(value);
  return {
    schedules: boundedList(source.schedules ?? source, ["schedules", "items"], automationLimits.schedules).map(normalizeSchedule),
    deliveries: boundedList(source.deliveries ?? source.notifications ?? source, ["deliveries", "notifications", "items"], automationLimits.deliveries).map(normalizeDelivery),
    connections: boundedList(source.connections ?? source, ["connections", "items"], automationLimits.connections).map(normalizeConnection),
    pairings: boundedList(source.pairings ?? source, ["pairings", "items"], automationLimits.pairings).map(normalizePairing),
    devices: boundedList(source.devices ?? source, ["devices", "entities", "items"], automationLimits.devices).map(normalizeDevice),
    auditEvents: boundedList(source.auditEvents ?? source.audit ?? source, ["events", "auditEvents", "items"], automationLimits.auditEvents).map(normalizeAuditEvent),
    monitoring: normalizeMonitoringSnapshot(source.monitoring ?? source.snapshot ?? {}),
  };
}

export function buildSchedulePayload(input = {}) {
  const source = objectValue(input);
  const expression = boundedText(source.expression ?? source.cronExpr ?? source.cron, 256);
  const timezone = boundedText(source.timezone, 128) || "UTC";
  const agentId = boundedText(source.agentId ?? source.projectId, 128);
  const prompt = boundedText(source.prompt, 8000);
  if (!expression) throw new Error(t("automation.validation.scheduleExpressionRequired"));
  if (!agentId) throw new Error(t("automation.validation.agentIdRequired"));
  if (!prompt) throw new Error(t("automation.validation.schedulePromptRequired"));
  const permissionMode = SAFE_PERMISSION_MODES.has(source.permissionMode) ? source.permissionMode : "readOnly";
  const environmentMode = ENVIRONMENT_MODES.has(source.environmentMode) ? source.environmentMode : "workline";
  const narratorMode = NARRATOR_MODES.has(source.narratorMode) ? source.narratorMode : "reuse";
  return {
    name: boundedText(source.name, 120) || t("automation.defaults.unnamedSchedule"),
    agentId,
    expression,
    timezone,
    prompt,
    permissionMode,
    environmentMode,
    narratorMode,
    enabled: source.enabled !== undefined ? Boolean(source.enabled) : true,
  };
}

export function buildConnectionPayload(input = {}) {
  const source = objectValue(input);
  const kind = normalizeKind(source.kind);
  if (!new Set(["telegram", "home-assistant"]).has(kind)) throw new Error(t("automation.validation.unsupportedConnection"));
  const credentialRef = normalizeEnvReference(source.credentialRef, {
    label: kind === "telegram" ? t("automation.connections.credential") : t("automation.homeAssistant.credential"),
  });
  const payload = {
    kind,
    name: boundedText(source.name, 160) || (kind === "telegram" ? t("automation.defaults.telegram") : t("automation.defaults.homeAssistant")),
    enabled: true,
    endpoint: "",
    settings: {},
    secretRefs: kind === "telegram" ? { botToken: credentialRef } : { accessToken: credentialRef },
  };
  if (kind === "home-assistant") {
    payload.endpoint = boundedText(source.endpoint ?? source.baseUrl, 400).replace(/\/+$/, "");
    if (!/^https?:\/\//i.test(payload.endpoint)) throw new Error(t("automation.validation.homeAssistantUrl"));
  }
  return payload;
}

export function buildPairingCodePayload(input = {}) {
  const source = objectValue(input);
  const connectionId = boundedText(source.connectionId, 160);
  const agentId = boundedText(source.agentId, 160);
  if (!connectionId || !agentId) throw new Error(t("automation.validation.pairingCodeRequired"));
  return { connectionId, agentId };
}

export function parseDeviceParameters(value) {
  if (value == null || value === "") return {};
  if (typeof value === "object" && !Array.isArray(value)) return { ...value };
  let parsed;
  try {
    parsed = JSON.parse(String(value));
  } catch {
    throw new Error(t("automation.validation.deviceParametersJson"));
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(t("automation.validation.deviceParametersObject"));
  return parsed;
}

export function buildDeviceActionPayload(input = {}) {
  const source = objectValue(input);
  const connectionId = boundedText(source.connectionId, 128);
  const entityId = boundedText(source.entityId ?? source.deviceId, 255);
  const domain = boundedText(source.domain ?? entityId.split(".")[0], 64);
  const service = boundedText(source.service ?? source.action, 64);
  if (!connectionId || !entityId || !domain || !service) throw new Error(t("automation.validation.deviceActionRequired"));
  const inputPayload = parseDeviceParameters(source.input ?? source.parameters);
  return {
    connectionId,
    domain,
    service,
    input: { ...inputPayload, entity_id: entityId },
  };
}

export function readLegacyIMDraft(storage = globalThis.localStorage) {
  for (const key of legacyIMDraftKeys) {
    try {
      const raw = storage?.getItem?.(key);
      if (raw == null) continue;
      let channel = "";
      try {
        channel = boundedText(JSON.parse(raw)?.channel, 80);
      } catch {}
      return { present: true, key, channel, authoritative: false, status: "disabled" };
    } catch {}
  }
  return { present: false, key: "", channel: "", authoritative: false, status: "disabled" };
}

export function isDeviceActionExpired(action, now = Date.now()) {
  const expiresAt = Date.parse(normalizeDeviceAction(action).expiresAt);
  return Number.isFinite(expiresAt) && expiresAt <= Number(now);
}

export async function requireLocalDoubleConfirmation(confirmAction, messages = []) {
  const confirm = typeof confirmAction === "function" ? confirmAction : () => false;
  const [first, second] = messages;
  if (!await confirm(first || t("automation.confirm.dangerousFirst"))) return false;
  if (!await confirm(second || t("automation.confirm.dangerousSecond"))) return false;
  return true;
}

function statusLabel(status) {
  const normalized = normalizeStatus(status);
  return t(`automation.status.${normalized}`) === `automation.status.${normalized}` ? (status || t("automation.defaults.unknown")) : t(`automation.status.${normalized}`);
}

function statusTone(status) {
  if (["active", "approved", "connected", "delivered", "enabled", "healthy", "ready", "succeeded", "success"].includes(status)) return "ok";
  if (["failed", "denied", "expired", "revoked", "dead"].includes(status)) return "danger";
  if (["pending", "pending_approval", "queued", "inflight", "retry_wait", "running", "executing"].includes(status)) return "warn";
  return "muted";
}

function kindLabel(kind) {
  return ({ telegram: t("automation.defaults.telegram"), "home-assistant": t("automation.defaults.homeAssistant") })[kind] || kind || t("automation.defaults.unknownChannel");
}

function formatTimestamp(value) {
  return formatRegionalTimestamp(boundedText(value, 80), {
    emptyFallback: t("automation.timestamp.empty"),
    invalidFallback: t("automation.timestamp.invalid"),
  });
}

function renderStatusPill(status) {
  const normalized = normalizeStatus(status);
  return `<span class="automation-status settings-badge ${statusTone(normalized)}">${escapeHtml(statusLabel(normalized))}</span>`;
}

function renderSectionState({ loading = false, error = "", empty = false, emptyText = t("automation.section.noData") } = {}) {
  if (loading) return `<div class="automation-empty settings-empty-state settings-skeleton" aria-busy="true" aria-live="polite">${escapeHtml(t("automation.section.loading"))}</div>`;
  if (error) return `<div class="automation-inline-error settings-alert" role="alert" aria-live="assertive">${escapeHtml(redactSensitiveText(error))}</div>`;
  if (empty) return `<div class="automation-empty settings-empty-state" aria-live="polite">${escapeHtml(emptyText)}</div>`;
  return "";
}

function renderMetric(label, value, tone = "") {
  return `<div class="automation-metric settings-stat-card ${escapeAttr(tone)}"><strong>${escapeHtml(String(boundedNumber(value)))}</strong><span>${escapeHtml(label)}</span></div>`;
}

function renderScheduleHistory(value = {}) {
  const history = objectValue(value);
  if (history.loading) return `<div class="automation-empty settings-empty-state settings-skeleton" aria-busy="true" aria-live="polite">${escapeHtml(t("automation.section.loadingHistory"))}</div>`;
  if (history.error) return `<div class="automation-inline-error">${escapeHtml(redactSensitiveText(history.error))}</div>`;
  if (!history.loaded) return "";
  const runs = (Array.isArray(history.runs) ? history.runs : []).slice(0, automationLimits.scheduleRuns).map(normalizeScheduleRun);
  if (!runs.length) return `<div class="automation-empty settings-empty-state" aria-live="polite">${escapeHtml(t("automation.section.noHistory"))}</div>`;
  return `<div class="automation-run-history">${runs.map((run) => `
    <div class="automation-run-row">
      <span>${escapeHtml(run.triggerType || t("automation.defaults.manual"))}</span>
      <strong>${escapeHtml(statusLabel(run.status))}</strong>
      <span>${escapeHtml(t("automation.schedule.runMs", { duration: run.durationMs }))}</span>
    </div>`).join("")}</div>`;
}

function renderScheduleHistoryMode(scheduleId, schedule, history) {
  const title = schedule?.name || scheduleId || t("automation.defaults.schedule");
  return `<div class="automation-history-mode" data-schedule-history-mode="${escapeAttr(scheduleId)}">
    <div class="automation-history-head">
      <div><span>${escapeHtml(t("automation.schedule.history"))}</span><h4>${escapeHtml(title)}</h4><small>${escapeHtml(scheduleId || t("automation.defaults.unknownSchedule"))}</small></div>
      <button class="automation-btn subtle" type="button" data-schedule-history-close>${escapeHtml(t("automation.buttons.returnSchedules"))}</button>
    </div>
    ${renderScheduleHistory(history) || `<div class="automation-empty">${escapeHtml(t("automation.section.selectHistory"))}</div>`}
  </div>`;
}

function renderScheduleCard(value, busy = {}, history = {}) {
  const schedule = normalizeSchedule(value);
  const permissionMode = scheduleEnumSource(value, "permissionMode", schedule.permissionMode);
  const environmentMode = scheduleEnumSource(value, "environmentMode", schedule.environmentMode);
  const narratorMode = scheduleEnumSource(value, "narratorMode", schedule.narratorMode);
  const actionBusy = Boolean(busy[`schedule:${schedule.id}`]);
  const historyBusy = Boolean(busy[`schedule-runs:${schedule.id}`]);
  const disabled = actionBusy ? " disabled" : "";
  return `
    <article class="automation-card settings-card settings-data-list-row" data-schedule-card="${escapeAttr(schedule.id)}" aria-busy="${actionBusy || historyBusy ? "true" : "false"}">
      <div class="automation-card-head settings-card-header">
        <div><strong>${escapeHtml(schedule.name || schedule.id || t("automation.defaults.unnamedSchedule"))}</strong><small>${escapeHtml(`${schedule.expression || t("automation.defaults.cronUnconfigured")} · ${schedule.timezone}`)}</small></div>
        ${renderStatusPill(schedule.enabled ? "enabled" : "disabled")}
      </div>
      <p class="automation-card-copy">${escapeHtml(schedule.prompt || t("automation.defaults.taskMissing"))}</p>
      <dl class="automation-kv"><div><dt>${escapeHtml(t("automation.schedule.agentId"))}</dt><dd>${escapeHtml(schedule.agentId || t("automation.timestamp.empty"))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.permission"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("permissionMode", permissionMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.environment"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("environmentMode", environmentMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.narrator"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("narratorMode", narratorMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.nextRun"))}</dt><dd>${escapeHtml(formatTimestamp(schedule.nextRunAt))}</dd></div></dl>
      ${schedule.lastError ? `<div class="automation-inline-error">${escapeHtml(schedule.lastError)}</div>` : ""}
      <div class="automation-actions settings-inline-actions">
        <button class="automation-btn subtle" type="button" data-schedule-history="${escapeAttr(schedule.id)}"${historyBusy ? " disabled" : ""}>${escapeHtml(t(historyBusy ? "automation.buttons.loading" : "automation.buttons.history"))}</button>
        <button class="automation-btn subtle" type="button" data-schedule-toggle="${escapeAttr(schedule.id)}" data-enabled="${schedule.enabled ? "true" : "false"}"${disabled}>${escapeHtml(t(schedule.enabled ? "automation.buttons.disable" : "automation.buttons.enable"))}</button>
        <button class="automation-btn primary" type="button" data-schedule-run="${escapeAttr(schedule.id)}"${disabled}>${escapeHtml(t("automation.buttons.runNow"))}</button>
        <button class="automation-btn danger destructive" type="button" data-schedule-delete="${escapeAttr(schedule.id)}"${disabled}>${escapeHtml(t("automation.buttons.delete"))}</button>
      </div>
      ${renderScheduleHistory(history)}
    </article>`;
}

function renderConnectionCard(value, busy = {}) {
  const connection = normalizeConnection(value);
  const actionBusy = Boolean(busy[`connection:${connection.id}`]);
  const disabled = actionBusy ? " disabled" : "";
  const credential = t(connection.credentialConfigured ? "automation.connections.credentialConfigured" : "automation.connections.credentialMissing");
  return `
    <article class="automation-card settings-card settings-data-list-row" data-connection-card="${escapeAttr(connection.id)}" aria-busy="${actionBusy ? "true" : "false"}">
      <div class="automation-card-head settings-card-header">
        <div><strong>${escapeHtml(connection.name || kindLabel(connection.kind))}</strong><small>${escapeHtml(kindLabel(connection.kind))}</small></div>
        ${renderStatusPill(connection.enabled ? connection.status : "disabled")}
      </div>
      ${connection.endpoint ? `<p class="automation-card-copy mono">${escapeHtml(connection.endpoint)}</p>` : ""}
      <p class="automation-secret-note">${escapeHtml(credential)}</p>
      ${connection.lastError ? `<div class="automation-inline-error">${escapeHtml(connection.lastError)}</div>` : ""}
      <div class="automation-actions settings-inline-actions">
        <button class="automation-btn subtle" type="button" data-connection-toggle="${escapeAttr(connection.id)}" data-enabled="${connection.enabled ? "true" : "false"}"${disabled}>${escapeHtml(t(connection.enabled ? "automation.buttons.disable" : "automation.buttons.enable"))}</button>
        <button class="automation-btn subtle" type="button" data-connection-test="${escapeAttr(connection.id)}"${disabled}>${escapeHtml(t("automation.buttons.testConnection"))}</button>
        <button class="automation-btn danger destructive" type="button" data-connection-delete="${escapeAttr(connection.id)}"${disabled}>${escapeHtml(t("automation.buttons.delete"))}</button>
      </div>
    </article>`;
}

function renderPairingCard(value, busy = {}) {
  const pairing = normalizePairing(value);
  const actionBusy = Boolean(busy[`pairing:${pairing.id}`]);
  return `
    <article class="automation-row-card settings-card settings-data-list-row" aria-busy="${actionBusy ? "true" : "false"}">
      <div><strong>${escapeHtml(pairing.channelUser || pairing.agentId || pairing.id || t("automation.defaults.unknownPairing"))}</strong><small>${escapeHtml(t("automation.pairing.agentAt", { agentId: pairing.agentId || t("automation.timestamp.empty"), timestamp: formatTimestamp(pairing.pairedAt) }))}</small></div>
      <div class="automation-actions settings-inline-actions compact">${renderStatusPill(pairing.status)}${pairing.status !== "revoked" ? `<button class="automation-btn danger destructive" type="button" data-pairing-revoke="${escapeAttr(pairing.id)}"${actionBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.revoke"))}</button>` : ""}</div>
    </article>`;
}

function renderDeliveryRow(value, busy = {}) {
  const delivery = normalizeDelivery(value);
  const retryable = ["failed", "error", "dead", "dead_letter"].includes(delivery.status);
  const actionBusy = Boolean(busy[`delivery:${delivery.id}`]);
  return `
    <article class="automation-row-card settings-card settings-data-list-row" aria-busy="${actionBusy ? "true" : "false"}">
      <div><strong>${escapeHtml(delivery.event || t("automation.defaults.notification"))}</strong><small>${escapeHtml(delivery.channel || t("automation.defaults.unknownChannel"))} · ${escapeHtml(t("automation.deliveries.attempts", { count: delivery.attempts }))} · ${escapeHtml(formatTimestamp(delivery.deliveredAt || delivery.createdAt))}</small>${delivery.error ? `<em>${escapeHtml(delivery.error)}</em>` : ""}</div>
      <div class="automation-actions settings-inline-actions compact">${renderStatusPill(delivery.status)}${retryable ? `<button class="automation-btn subtle" type="button" data-delivery-retry="${escapeAttr(delivery.id)}"${actionBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.retry"))}</button>` : ""}</div>
    </article>`;
}

function renderDeviceRow(value) {
  const device = normalizeDevice(value);
  return `
    <article class="automation-device-row settings-data-list-row">
      <div><strong>${escapeHtml(device.name || device.entityId)}</strong><small class="mono">${escapeHtml(device.entityId)}</small></div>
      <span>${escapeHtml(device.state)}</span>
      <span class="automation-readonly">${escapeHtml(t("automation.homeAssistant.readonly"))}</span>
    </article>`;
}

function renderDeviceAction(value, busy = {}, now = Date.now()) {
  const action = normalizeDeviceAction(value);
  const expired = isDeviceActionExpired(action, now);
  const pending = ["pending", "pending_approval"].includes(action.status) && !expired;
  const status = expired && ["pending", "pending_approval", "approved"].includes(action.status) ? "expired" : action.status;
  const actionBusy = Boolean(busy[`device-action:${action.id}`]);
  const riskLabel = t(`automation.risk.${action.risk}`) === `automation.risk.${action.risk}` ? action.risk : t(`automation.risk.${action.risk}`);
  return `
    <article class="automation-card settings-card settings-data-list-row ${["critical", "high", "blocked"].includes(action.risk) ? "danger-zone destructive" : ""}" aria-busy="${actionBusy ? "true" : "false"}">
      <div class="automation-card-head settings-card-header"><div><strong>${escapeHtml([action.domain, action.service].filter(Boolean).join(".") || t("automation.defaults.deviceAction"))}</strong><small class="mono">${escapeHtml(action.entityId || t("automation.defaults.unknownEntity"))}</small></div>${renderStatusPill(status)}</div>
      <dl class="automation-kv"><div><dt>${escapeHtml(t("automation.deviceActions.risk"))}</dt><dd>${escapeHtml(riskLabel)}</dd></div><div><dt>${escapeHtml(t("automation.deviceActions.expires"))}</dt><dd>${escapeHtml(formatTimestamp(action.expiresAt))}</dd></div></dl>
      ${action.error ? `<div class="automation-inline-error settings-alert" role="alert" aria-live="assertive">${escapeHtml(action.error)}</div>` : ""}
      ${pending ? `<div class="automation-actions settings-inline-actions"><button class="automation-btn danger destructive" type="button" data-device-action-approve="${escapeAttr(action.id)}"${actionBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.approveLocal"))}</button><button class="automation-btn subtle" type="button" data-device-action-deny="${escapeAttr(action.id)}"${actionBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.deny"))}</button></div>` : ""}
    </article>`;
}

function renderAuditRow(value) {
  const event = normalizeAuditEvent(value);
  return `
    <article class="automation-audit-row settings-data-list-row">
      <time>${escapeHtml(formatTimestamp(event.createdAt))}</time>
      <div><strong>${escapeHtml(event.type || t("automation.defaults.event"))}</strong><small>${escapeHtml(event.actor || t("automation.defaults.system"))}${event.summary ? ` · ${escapeHtml(event.summary)}` : ""}</small></div>
      ${renderStatusPill(event.result)}
    </article>`;
}

function renderActivityRow(value) {
  const source = objectValue(value);
  return `<li><time>${escapeHtml(formatTimestamp(source.at))}</time><span>${escapeHtml(redactSensitiveText(source.message))}</span></li>`;
}

function scheduleEnumOptions(group, values, selected = "") {
  return values.map((value) => `<option value="${escapeAttr(value)}"${value === selected ? " selected" : ""}>${escapeHtml(formatScheduleEnumValue(group, value))}</option>`).join("");
}

function connectionOptions(connections, kind, selected = "") {
  return connections.filter((item) => item.kind === kind).map((item) => `<option value="${escapeAttr(item.id)}" ${item.id === selected ? "selected" : ""}>${escapeHtml(item.name || item.id)}</option>`).join("");
}

export function renderAutomationControl(value = {}) {
  const source = objectValue(value);
  const scheduleSources = (Array.isArray(source.schedules) ? source.schedules : []).slice(0, automationLimits.schedules);
  const schedules = scheduleSources.map(normalizeSchedule);
  const deliveries = (Array.isArray(source.deliveries) ? source.deliveries : []).slice(0, automationLimits.deliveries).map(normalizeDelivery);
  const connections = (Array.isArray(source.connections) ? source.connections : []).slice(0, automationLimits.connections).map(normalizeConnection);
  const pairings = (Array.isArray(source.pairings) ? source.pairings : []).slice(0, automationLimits.pairings).map(normalizePairing);
  const devices = (Array.isArray(source.devices) ? source.devices : []).slice(0, automationLimits.devices).map(normalizeDevice);
  const deviceActions = (Array.isArray(source.deviceActions) ? source.deviceActions : []).slice(0, automationLimits.deviceActions).map(normalizeDeviceAction);
  const auditEvents = (Array.isArray(source.auditEvents) ? source.auditEvents : []).slice(0, automationLimits.auditEvents).map(normalizeAuditEvent);
  const activity = (Array.isArray(source.activity) ? source.activity : []).slice(-automationLimits.activity);
  const monitoring = normalizeMonitoringSnapshot(source.monitoring);
  const errors = objectValue(source.errors);
  const busy = objectValue(source.busy);
  const scheduleRunHistory = objectValue(source.scheduleRunHistory);
  const scheduleViewMode = source.scheduleViewMode === "history" ? "history" : "list";
  const selectedScheduleHistoryId = boundedText(source.selectedScheduleHistoryId, 160);
  const selectedSchedule = schedules.find((item) => item.id === selectedScheduleHistoryId);
  const loading = Boolean(source.loading);
  const loaded = Boolean(source.loaded);
  const selectedConnectionId = boundedText(source.selectedConnectionId, 160);
  const legacyDraft = objectValue(source.legacyDraft);
  const pairingCode = normalizePairingCode(source.latestPairingCode);
  const telegramConnections = connections.filter((item) => item.kind === "telegram");
  const homeAssistantConnections = connections.filter((item) => item.kind === "home-assistant");
  const scheduleState = renderSectionState({ loading: loading && !loaded, error: errors.schedules, empty: !schedules.length, emptyText: t("automation.schedule.empty") });
  const deliveryState = renderSectionState({ loading: loading && !loaded, error: errors.deliveries, empty: !deliveries.length, emptyText: t("automation.deliveries.empty") });
  const connectionState = renderSectionState({ loading: loading && !loaded, error: errors.connections, empty: !connections.length, emptyText: t("automation.connections.empty") });
  const pairingState = renderSectionState({ loading: loading && !loaded, error: errors.pairings, empty: !pairings.length, emptyText: t("automation.pairing.empty") });
  const deviceState = renderSectionState({ loading: Boolean(source.devicesLoading), error: errors.devices, empty: !devices.length, emptyText: t(homeAssistantConnections.length ? "automation.homeAssistant.noDevices" : "automation.homeAssistant.createConnectionFirst") });
  const actionState = renderSectionState({ error: errors.deviceActions, empty: !deviceActions.length, emptyText: t("automation.deviceActions.empty") });
  const auditState = renderSectionState({ loading: loading && !loaded, error: errors.auditEvents, empty: !auditEvents.length, emptyText: t("automation.audit.empty") });

  return `
    <div id="automationControlPage" class="automation-control-page settings-page-section" data-authority="server-api" aria-live="polite" aria-busy="${loading || Boolean(source.devicesLoading) ? "true" : "false"}">
      <section class="automation-hero settings-card settings-card-header">
        <div>
          <div class="settings-hero-kicker">${escapeHtml(t("automation.hero.kicker"))}</div>
          <h2>${escapeHtml(t("automation.hero.title"))}</h2>
          <p data-settings-help-copy>${escapeHtml(t("automation.hero.description"))}</p>
        </div>
        <div class="automation-actions settings-inline-actions">
          <button id="refreshAutomationControlBtn" class="automation-btn primary" type="button"${loading ? " disabled" : ""}>${escapeHtml(t(loading ? "automation.buttons.refreshing" : "automation.buttons.refreshAll"))}</button>
        </div>
      </section>
      <div class="automation-safety settings-alert" role="note"><strong>${escapeHtml(t("automation.hero.safetyTitle"))}</strong><span>${escapeHtml(t("automation.hero.safetyDescription"))}</span></div>
      ${legacyDraft.present ? `<div class="automation-migration settings-alert" role="status" aria-live="polite"><strong>${escapeHtml(t("automation.hero.migrationTitle"))}</strong><span>${escapeHtml(t("automation.hero.migration", { key: legacyDraft.key || "localStorage", channel: legacyDraft.channel ? t("automation.hero.migrationChannel", { channel: legacyDraft.channel }) : "" }))}</span></div>` : ""}
      ${errors.global ? `<div class="automation-inline-error settings-alert" role="alert" aria-live="assertive">${escapeHtml(redactSensitiveText(errors.global))}</div>` : ""}

      <section class="automation-metrics settings-stat-grid" aria-label="${escapeAttr(t("automation.metrics.ariaLabel"))}">
        ${renderMetric(t("automation.metrics.activeRuns"), monitoring.activeRuns, monitoring.activeRuns ? "active" : "")}
        ${renderMetric(t("automation.metrics.pendingApprovals"), monitoring.pendingApprovals, monitoring.pendingApprovals ? "warn" : "")}
        ${renderMetric(t("automation.metrics.schedules"), monitoring.scheduleCount || schedules.length)}
        ${renderMetric(t("automation.metrics.notifications"), monitoring.notificationCount || deliveries.length)}
        ${renderMetric(t("automation.metrics.channels"), monitoring.channelCount || connections.length)}
        ${renderMetric(t("automation.metrics.devices"), monitoring.deviceCount || devices.length)}
      </section>

      <div class="automation-section-grid">
        <section class="automation-section settings-card settings-page-section span-2" data-automation-section="schedules">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.schedule.kicker"))}</span><h3>${escapeHtml(t("automation.schedule.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.schedule.description"))}</p></div>${errors.monitoring ? `<small class="error">${escapeHtml(redactSensitiveText(errors.monitoring))}</small>` : ""}</div>
          ${scheduleViewMode === "history" ? renderScheduleHistoryMode(selectedScheduleHistoryId, selectedSchedule, scheduleRunHistory[selectedScheduleHistoryId]) : `
          <form id="createScheduleForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.schedule.name"))}<input id="scheduleNameInput" maxlength="120" placeholder="${escapeAttr(t("automation.schedule.namePlaceholder"))}" required /></label>
            <label>${escapeHtml(t("automation.schedule.preset"))}<select id="schedulePresetInput"><option value="">${escapeHtml(t("automation.schedule.custom"))}</option>${schedulePresets.map((preset) => `<option value="${escapeAttr(preset)}">${escapeHtml(preset)}</option>`).join("")}</select></label>
            <label>${escapeHtml(t("automation.schedule.expression"))}<input id="scheduleExpressionInput" maxlength="256" placeholder="@every 15m" required /></label>
            <label>${escapeHtml(t("automation.schedule.agentId"))}<input id="scheduleAgentInput" maxlength="128" placeholder="agent-id" required /></label>
            <label>${escapeHtml(t("automation.schedule.timezone"))}<input id="scheduleTimezoneInput" maxlength="128" value="UTC" placeholder="Asia/Shanghai" required /></label>
            <label>${escapeHtml(t("automation.schedule.permission"))}<select id="schedulePermissionInput">${scheduleEnumOptions("permissionMode", ["readOnly", "acceptEdits"], "readOnly")}</select></label>
            <label>${escapeHtml(t("automation.schedule.environment"))}<select id="scheduleEnvironmentInput">${scheduleEnumOptions("environmentMode", ["workline", "standalone"], "workline")}</select></label>
            <label>${escapeHtml(t("automation.schedule.narrator"))}<select id="scheduleNarratorInput">${scheduleEnumOptions("narratorMode", ["reuse", "new"], "reuse")}</select></label>
            <label class="span-2">${escapeHtml(t("automation.schedule.prompt"))}<textarea id="schedulePromptInput" rows="3" maxlength="8000" placeholder="${escapeAttr(t("automation.schedule.promptPlaceholder"))}" required></textarea></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn primary" type="submit"${busy["schedule:create"] ? " disabled" : ""}>${escapeHtml(t(busy["schedule:create"] ? "automation.buttons.creating" : "automation.buttons.createSchedule"))}</button></div>
          </form>
          <div class="automation-card-grid settings-data-list">${scheduleState || schedules.map((item, index) => renderScheduleCard(scheduleSources[index], busy, scheduleRunHistory[item.id])).join("")}</div>`}
        </section>

        <section class="automation-section settings-card settings-page-section">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.deliveries.kicker"))}</span><h3>${escapeHtml(t("automation.deliveries.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.deliveries.description"))}</p></div></div>
          <div class="automation-list settings-data-list settings-table">${deliveryState || deliveries.map((item) => renderDeliveryRow(item, busy)).join("")}</div>
        </section>

        <section class="automation-section settings-card settings-page-section">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.connections.kicker"))}</span><h3>${escapeHtml(t("automation.connections.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.connections.description"))}</p></div></div>
          <form id="createTelegramConnectionForm" class="automation-form settings-form-grid compact">
            <label>${escapeHtml(t("automation.connections.name"))}<input id="telegramNameInput" maxlength="160" placeholder="${escapeAttr(t("automation.connections.namePlaceholder"))}" /></label>
            <label>${escapeHtml(t("automation.connections.credential"))}<input id="telegramCredentialInput" maxlength="160" placeholder="env:AUTOTO_TELEGRAM_BOT_TOKEN" autocomplete="off" required /></label>
            <div class="automation-form-actions settings-inline-actions"><button class="automation-btn primary" type="submit"${busy["connection:create:telegram"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createTelegram"))}</button></div>
          </form>
          <div class="automation-card-grid single">${connectionState || telegramConnections.map((item) => renderConnectionCard(item, busy)).join("") || `<div class="automation-empty">${escapeHtml(t("automation.connections.emptyTelegram"))}</div>`}</div>
        </section>

        <section class="automation-section settings-card settings-page-section">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.pairing.kicker"))}</span><h3>${escapeHtml(t("automation.pairing.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.pairing.description"))}</p></div></div>
          <form id="createPairingCodeForm" class="automation-form settings-form-grid compact">
            <label>${escapeHtml(t("automation.pairing.connection"))}<select id="pairingConnectionInput" required><option value="">${escapeHtml(t("automation.pairing.selectConnection"))}</option>${connectionOptions(connections, "telegram")}</select></label>
            <label>${escapeHtml(t("automation.pairing.agentId"))}<input id="pairingAgentInput" maxlength="160" placeholder="agent-id" required /></label>
            <div class="automation-form-actions settings-inline-actions"><button class="automation-btn primary" type="submit"${busy["pairing:create"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createPairingCode"))}</button></div>
          </form>
          ${pairingCode.code ? `<div class="automation-pairing-code"><span>${escapeHtml(t("automation.pairing.code"))}</span><strong>${escapeHtml(pairingCode.code)}</strong><small>${escapeHtml(t("automation.pairing.expiresAt", { timestamp: formatTimestamp(pairingCode.expiresAt) }))}</small></div>` : ""}
          <div class="automation-list settings-data-list settings-table">${pairingState || pairings.map((item) => renderPairingCard(item, busy)).join("")}</div>
        </section>

        <section class="automation-section settings-card settings-page-section span-2">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.homeAssistant.kicker"))}</span><h3>${escapeHtml(t("automation.homeAssistant.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.homeAssistant.description"))}</p></div></div>
          <form id="createHomeAssistantConnectionForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.homeAssistant.name"))}<input id="homeAssistantNameInput" maxlength="160" placeholder="Home Assistant" /></label>
            <label>${escapeHtml(t("automation.homeAssistant.url"))}<input id="homeAssistantUrlInput" maxlength="400" placeholder="http://homeassistant.local:8123" required /></label>
            <label class="span-2">${escapeHtml(t("automation.homeAssistant.credential"))}<input id="homeAssistantCredentialInput" maxlength="160" placeholder="env:AUTOTO_HOME_ASSISTANT_TOKEN" autocomplete="off" required /></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn primary" type="submit"${busy["connection:create:home-assistant"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createHomeAssistant"))}</button></div>
          </form>
          <div class="automation-card-grid settings-data-list">${homeAssistantConnections.length ? homeAssistantConnections.map((item) => renderConnectionCard(item, busy)).join("") : `<div class="automation-empty">${escapeHtml(t("automation.homeAssistant.empty"))}</div>`}</div>
          <div class="automation-device-toolbar"><label>${escapeHtml(t("automation.homeAssistant.viewConnection"))}<select id="deviceConnectionSelect"><option value="">${escapeHtml(t("automation.homeAssistant.selectConnection"))}</option>${connectionOptions(connections, "home-assistant", selectedConnectionId)}</select></label><span>${escapeHtml(t("automation.homeAssistant.devicesLimit", { count: automationLimits.devices }))}</span></div>
          <div class="automation-device-list settings-data-list settings-table">${deviceState || devices.map(renderDeviceRow).join("")}</div>
        </section>

        <section class="automation-section span-2 danger-section">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.deviceActions.kicker"))}</span><h3>${escapeHtml(t("automation.deviceActions.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.deviceActions.description"))}</p></div><span class="automation-danger-badge settings-badge destructive">${escapeHtml(t("automation.deviceActions.riskBoundary"))}</span></div>
          <form id="createDeviceActionForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.deviceActions.connection"))}<select id="deviceActionConnectionInput" required><option value="">${escapeHtml(t("automation.deviceActions.selectConnection"))}</option>${connectionOptions(connections, "home-assistant", selectedConnectionId)}</select></label>
            <label>${escapeHtml(t("automation.deviceActions.entityId"))}<input id="deviceActionEntityInput" maxlength="255" placeholder="light.living_room" required /></label>
            <label>${escapeHtml(t("automation.deviceActions.service"))}<input id="deviceActionServiceInput" maxlength="64" placeholder="turn_off" required /></label>
            <label>${escapeHtml(t("automation.deviceActions.input"))}<input id="deviceActionInput" maxlength="1200" placeholder='{"brightness": 0}' /></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn danger destructive" type="submit"${busy["device-action:create"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.requestLocal"))}</button></div>
          </form>
          <div class="automation-card-grid settings-data-list">${actionState || deviceActions.map((item) => renderDeviceAction(item, busy, source.now)).join("")}</div>
        </section>

        <section class="automation-section settings-card settings-page-section span-2">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.audit.kicker"))}</span><h3>${escapeHtml(t("automation.audit.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.audit.description"))}</p></div></div>
          <div class="automation-audit-list settings-data-list settings-table">${auditState || auditEvents.map(renderAuditRow).join("")}</div>
          ${activity.length ? `<details class="automation-activity"><summary>${escapeHtml(t("automation.audit.activity", { count: activity.length }))}</summary><ol>${activity.map(renderActivityRow).join("")}</ol></details>` : ""}
        </section>
      </div>
    </div>`;
}

export function createAutomationControlController({
  request,
  onChange,
  showError,
  showToast,
  storage = globalThis.localStorage,
  confirmAction,
  now = () => Date.now(),
} = {}) {
  if (typeof request !== "function") throw new TypeError("Automation control request must be a function");
  const confirm = confirmAction || platformConfirm;
  const state = {
    loaded: false,
    loading: false,
    devicesLoading: false,
    schedules: [],
    scheduleRunHistory: {},
    scheduleViewMode: "list",
    selectedScheduleHistoryId: "",
    deliveries: [],
    connections: [],
    pairings: [],
    devices: [],
    deviceActions: [],
    auditEvents: [],
    monitoring: normalizeMonitoringSnapshot({}),
    errors: {},
    busy: {},
    selectedConnectionId: "",
    latestPairingCode: null,
    legacyDraft: readLegacyIMDraft(storage),
    activity: [],
    loadSeq: 0,
    deviceSeq: 0,
  };

  function getState() {
    return {
      loaded: state.loaded,
      loading: state.loading,
      devicesLoading: state.devicesLoading,
      schedules: state.schedules.map((item) => ({ ...item })),
      scheduleRunHistory: Object.fromEntries(Object.entries(state.scheduleRunHistory).map(([id, history]) => [id, { ...history, runs: (history.runs || []).map((item) => ({ ...item })) }])),
      scheduleViewMode: state.scheduleViewMode,
      selectedScheduleHistoryId: state.selectedScheduleHistoryId,
      deliveries: state.deliveries.map((item) => ({ ...item })),
      connections: state.connections.map((item) => ({ ...item })),
      pairings: state.pairings.map((item) => ({ ...item })),
      devices: state.devices.map((item) => ({ ...item })),
      deviceActions: state.deviceActions.map((item) => ({ ...item })),
      auditEvents: state.auditEvents.map((item) => ({ ...item })),
      monitoring: { ...state.monitoring, deviceActions: state.monitoring.deviceActions.map((item) => ({ ...item })) },
      errors: { ...state.errors },
      busy: { ...state.busy },
      selectedConnectionId: state.selectedConnectionId,
      latestPairingCode: state.latestPairingCode ? { ...state.latestPairingCode } : null,
      legacyDraft: { ...state.legacyDraft },
      activity: state.activity.map((item) => ({ ...item })),
      now: now(),
      loadSeq: state.loadSeq,
      deviceSeq: state.deviceSeq,
    };
  }

  function emit() {
    onChange?.(getState());
  }

  function addActivity(message) {
    state.activity.push({ at: new Date(now()).toISOString(), message: redactSensitiveText(message) });
    if (state.activity.length > automationLimits.activity) state.activity.splice(0, state.activity.length - automationLimits.activity);
  }

  function setError(section, error, { notify = true } = {}) {
    const message = redactSensitiveText(error?.message || error || t("automation.validation.requestFailed"));
    state.errors[section] = message;
    addActivity(t("automation.activity.error", { section, message }));
    if (notify) showError?.(new Error(message));
  }

  function clearError(section) {
    if (state.errors[section]) delete state.errors[section];
  }

  function collectionResult(result, keys, limit, normalize) {
    return boundedList(result, keys, limit).map(normalize);
  }

  async function load() {
    const seq = ++state.loadSeq;
    state.deviceSeq += 1;
    state.loading = true;
    state.errors = {};
    emit();
    const requests = [
      ["schedules", "/api/schedules"],
      ["deliveries", "/api/notifications/deliveries"],
      ["connections", "/api/integrations/connections"],
      ["pairings", "/api/channels/pairings"],
      ["monitoring", "/api/monitoring/snapshot"],
      ["auditEvents", "/api/audit/events?limit=50"],
    ];
    const results = await Promise.allSettled(requests.map(([, path]) => request(path)));
    if (seq !== state.loadSeq) return false;
    results.forEach((result, index) => {
      const [section] = requests[index];
      if (result.status === "rejected") {
        setError(section, result.reason, { notify: false });
        return;
      }
      clearError(section);
      if (section === "schedules") state.schedules = collectionResult(result.value, ["schedules", "items"], automationLimits.schedules, normalizeSchedule);
      if (section === "deliveries") state.deliveries = collectionResult(result.value, ["deliveries", "notifications", "items"], automationLimits.deliveries, normalizeDelivery);
      if (section === "connections") state.connections = collectionResult(result.value, ["connections", "items"], automationLimits.connections, normalizeConnection);
      if (section === "pairings") state.pairings = collectionResult(result.value, ["pairings", "items"], automationLimits.pairings, normalizePairing);
      if (section === "monitoring") {
        state.monitoring = normalizeMonitoringSnapshot(result.value);
        state.deviceActions = state.monitoring.deviceActions;
      }
      if (section === "auditEvents") state.auditEvents = collectionResult(result.value, ["events", "auditEvents", "items"], automationLimits.auditEvents, normalizeAuditEvent);
    });
    const homeAssistantConnections = state.connections.filter((item) => item.kind === "home-assistant");
    if (!homeAssistantConnections.some((item) => item.id === state.selectedConnectionId)) {
      state.selectedConnectionId = homeAssistantConnections[0]?.id || "";
    }
    if (state.selectedConnectionId) {
      state.devicesLoading = true;
      emit();
      try {
        const devicesResult = await request(`/api/devices?connectionId=${encodeURIComponent(state.selectedConnectionId)}`);
        if (seq !== state.loadSeq) return false;
        state.devices = collectionResult(devicesResult, ["devices", "entities", "items"], automationLimits.devices, normalizeDevice);
        clearError("devices");
      } catch (error) {
        if (seq !== state.loadSeq) return false;
        state.devices = [];
        setError("devices", error, { notify: false });
      } finally {
        if (seq === state.loadSeq) state.devicesLoading = false;
      }
    } else {
      state.devices = [];
      state.devicesLoading = false;
    }
    if (seq !== state.loadSeq) return false;
    state.loaded = true;
    state.loading = false;
    addActivity(t("automation.activity.refreshed"));
    emit();
    return true;
  }

  async function loadDevices(connectionId) {
    const id = boundedText(connectionId, 160);
    state.selectedConnectionId = id;
    const seq = ++state.deviceSeq;
    state.devicesLoading = true;
    clearError("devices");
    emit();
    if (!id) {
      state.devices = [];
      state.devicesLoading = false;
      emit();
      return true;
    }
    try {
      const result = await request(`/api/devices?connectionId=${encodeURIComponent(id)}`);
      if (seq !== state.deviceSeq) return false;
      state.devices = collectionResult(result, ["devices", "entities", "items"], automationLimits.devices, normalizeDevice);
      return true;
    } catch (error) {
      if (seq !== state.deviceSeq) return false;
      state.devices = [];
      setError("devices", error);
      return false;
    } finally {
      if (seq === state.deviceSeq) {
        state.devicesLoading = false;
        emit();
      }
    }
  }

  async function mutate({ key, section, path, options, success, reload = true, onResult }) {
    if (state.busy[key]) return false;
    state.loadSeq += 1;
    state.busy[key] = true;
    clearError(section);
    emit();
    try {
      const result = await request(path, options);
      onResult?.(result);
      addActivity(success);
      showToast?.(success, "success", { force: true });
      if (reload) await load();
      return true;
    } catch (error) {
      setError(section, error);
      return false;
    } finally {
      delete state.busy[key];
      emit();
    }
  }

  function closeScheduleHistory() {
    state.scheduleViewMode = "list";
    state.selectedScheduleHistoryId = "";
    emit();
    return true;
  }

  async function loadScheduleRuns(id) {
    const scheduleId = boundedText(id, 160);
    if (!scheduleId || state.busy[`schedule-runs:${scheduleId}`]) return false;
    state.scheduleViewMode = "history";
    state.selectedScheduleHistoryId = scheduleId;
    state.busy[`schedule-runs:${scheduleId}`] = true;
    state.scheduleRunHistory[scheduleId] = { ...(state.scheduleRunHistory[scheduleId] || {}), loading: true, error: "" };
    emit();
    try {
      const result = await request(`/api/schedules/${encodeURIComponent(scheduleId)}/runs?limit=${automationLimits.scheduleRuns}`);
      state.scheduleRunHistory[scheduleId] = {
        loaded: true,
        loading: false,
        error: "",
        runs: boundedList(result, ["runs", "items"], automationLimits.scheduleRuns).map(normalizeScheduleRun),
      };
      return true;
    } catch (error) {
      state.scheduleRunHistory[scheduleId] = { loaded: true, loading: false, error: redactSensitiveText(error?.message || error), runs: [] };
      showError?.(error);
      return false;
    } finally {
      delete state.busy[`schedule-runs:${scheduleId}`];
      emit();
    }
  }

  async function createSchedule(input) {
    let payload;
    try {
      payload = buildSchedulePayload(input);
    } catch (error) {
      setError("schedules", error);
      emit();
      return false;
    }
    return mutate({
      key: "schedule:create",
      section: "schedules",
      path: "/api/schedules",
      options: { method: "POST", body: JSON.stringify(payload) },
      success: t("automation.toast.scheduleCreated"),
    });
  }

  function toggleSchedule(id, enabled) {
    const scheduleId = boundedText(id, 160);
    return mutate({
      key: `schedule:${scheduleId}`,
      section: "schedules",
      path: `/api/schedules/${encodeURIComponent(scheduleId)}`,
      options: { method: "PATCH", body: JSON.stringify({ enabled: Boolean(enabled) }) },
      success: t(enabled ? "automation.toast.scheduleEnabled" : "automation.toast.scheduleDisabled"),
    });
  }

  function runSchedule(id) {
    const scheduleId = boundedText(id, 160);
    return mutate({
      key: `schedule:${scheduleId}`,
      section: "schedules",
      path: `/api/schedules/${encodeURIComponent(scheduleId)}/run`,
      options: { method: "POST" },
      success: t("automation.toast.scheduleRunRequested"),
    });
  }

  async function deleteSchedule(id) {
    const scheduleId = boundedText(id, 160);
    if (!await confirm(t("automation.confirm.deleteSchedule"))) return false;
    return mutate({
      key: `schedule:${scheduleId}`,
      section: "schedules",
      path: `/api/schedules/${encodeURIComponent(scheduleId)}`,
      options: { method: "DELETE" },
      success: t("automation.toast.scheduleDeleted"),
    });
  }

  function retryDelivery(id) {
    const deliveryId = boundedText(id, 160);
    return mutate({
      key: `delivery:${deliveryId}`,
      section: "deliveries",
      path: `/api/notifications/deliveries/${encodeURIComponent(deliveryId)}/retry`,
      options: { method: "POST" },
      success: t("automation.toast.deliveryRetry"),
    });
  }

  async function createConnection(input) {
    let payload;
    try {
      payload = buildConnectionPayload(input);
    } catch (error) {
      setError("connections", error);
      emit();
      return false;
    }
    return mutate({
      key: `connection:create:${payload.kind}`,
      section: "connections",
      path: "/api/integrations/connections",
      options: { method: "POST", body: JSON.stringify(payload) },
      success: t("automation.toast.connectionCreated", { kind: kindLabel(payload.kind) }),
    });
  }

  function toggleConnection(id, enabled) {
    const connectionId = boundedText(id, 160);
    return mutate({
      key: `connection:${connectionId}`,
      section: "connections",
      path: `/api/integrations/connections/${encodeURIComponent(connectionId)}`,
      options: { method: "PATCH", body: JSON.stringify({ enabled: Boolean(enabled) }) },
      success: t(enabled ? "automation.toast.connectionEnabled" : "automation.toast.connectionDisabled"),
    });
  }

  function testConnection(id) {
    const connectionId = boundedText(id, 160);
    return mutate({
      key: `connection:${connectionId}`,
      section: "connections",
      path: `/api/integrations/connections/${encodeURIComponent(connectionId)}/test`,
      options: { method: "POST" },
      success: t("automation.toast.connectionTested"),
    });
  }

  async function deleteConnection(id) {
    const connectionId = boundedText(id, 160);
    if (!await confirm(t("automation.confirm.deleteConnection"))) return false;
    return mutate({
      key: `connection:${connectionId}`,
      section: "connections",
      path: `/api/integrations/connections/${encodeURIComponent(connectionId)}`,
      options: { method: "DELETE" },
      success: t("automation.toast.connectionDeleted"),
    });
  }

  async function createPairingCode(input) {
    let payload;
    try {
      payload = buildPairingCodePayload(input);
    } catch (error) {
      setError("pairings", error);
      emit();
      return false;
    }
    return mutate({
      key: "pairing:create",
      section: "pairings",
      path: "/api/channels/pairing-codes",
      options: { method: "POST", body: JSON.stringify(payload) },
      success: t("automation.toast.pairingCodeCreated"),
      onResult: (result) => { state.latestPairingCode = normalizePairingCode({ ...objectValue(result), ...payload }); },
    });
  }

  async function revokePairing(id) {
    const pairingId = boundedText(id, 160);
    if (!await confirm(t("automation.confirm.revokePairing"))) return false;
    return mutate({
      key: `pairing:${pairingId}`,
      section: "pairings",
      path: `/api/channels/pairings/${encodeURIComponent(pairingId)}/revoke`,
      options: { method: "POST" },
      success: t("automation.toast.pairingRevoked"),
    });
  }

  async function requestDeviceAction(input) {
    let payload;
    try {
      payload = buildDeviceActionPayload(input);
    } catch (error) {
      setError("deviceActions", error);
      emit();
      return false;
    }
    const confirmed = await requireLocalDoubleConfirmation(confirm, [
      t("automation.confirm.requestDeviceActionFirst", { entityId: payload.input.entity_id, action: `${payload.domain}.${payload.service}` }),
      t("automation.confirm.requestDeviceActionSecond"),
    ]);
    if (!confirmed) return false;
    return mutate({
      key: "device-action:create",
      section: "deviceActions",
      path: "/api/device-actions",
      options: { method: "POST", body: JSON.stringify(payload) },
      success: t("automation.toast.deviceActionRequested"),
      reload: false,
      onResult: (result) => {
        const normalized = normalizeDeviceAction({ entityId: payload.input.entity_id, ...payload, ...objectValue(result) });
        state.deviceActions = [normalized, ...state.deviceActions.filter((item) => item.id !== normalized.id)].slice(0, automationLimits.deviceActions);
      },
    });
  }

  async function approveDeviceAction(id) {
    const actionId = boundedText(id, 160);
    const action = state.deviceActions.find((item) => item.id === actionId);
    if (!action) return false;
    if (isDeviceActionExpired(action, now())) {
      setError("deviceActions", new Error(t("automation.validation.actionExpired")));
      emit();
      return false;
    }
    const confirmed = await requireLocalDoubleConfirmation(confirm, [
      t("automation.confirm.approveDeviceActionFirst", { entityId: action.entityId, action: `${action.domain}.${action.service}` }),
      t("automation.confirm.approveDeviceActionSecond", { risk: t(`automation.risk.${action.risk}`), expiresAt: action.expiresAt || t("automation.defaults.unknown") }),
    ]);
    if (!confirmed) return false;
    return mutate({
      key: `device-action:${actionId}`,
      section: "deviceActions",
      path: `/api/device-actions/${encodeURIComponent(actionId)}/approve`,
      options: { method: "POST" },
      success: t("automation.toast.deviceActionApproved"),
    });
  }

  async function denyDeviceAction(id) {
    const actionId = boundedText(id, 160);
    if (!await confirm(t("automation.confirm.denyDeviceAction"))) return false;
    return mutate({
      key: `device-action:${actionId}`,
      section: "deviceActions",
      path: `/api/device-actions/${encodeURIComponent(actionId)}/deny`,
      options: { method: "POST" },
      success: t("automation.toast.deviceActionDenied"),
    });
  }

  function bind() {
    if (typeof document === "undefined") return;
    const root = document.getElementById("automationControlPage");
    if (!root) return;
    const byId = (id) => root.querySelector(`#${id}`);
    byId("refreshAutomationControlBtn")?.addEventListener("click", () => load());
    byId("schedulePresetInput")?.addEventListener("change", (event) => {
      if (event.currentTarget.value && byId("scheduleExpressionInput")) byId("scheduleExpressionInput").value = event.currentTarget.value;
    });
    byId("createScheduleForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createSchedule({
        name: byId("scheduleNameInput")?.value,
        expression: byId("scheduleExpressionInput")?.value,
        agentId: byId("scheduleAgentInput")?.value,
        timezone: byId("scheduleTimezoneInput")?.value,
        permissionMode: byId("schedulePermissionInput")?.value,
        environmentMode: byId("scheduleEnvironmentInput")?.value,
        narratorMode: byId("scheduleNarratorInput")?.value,
        prompt: byId("schedulePromptInput")?.value,
      });
    });
    byId("createTelegramConnectionForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createConnection({ kind: "telegram", name: byId("telegramNameInput")?.value, credentialRef: byId("telegramCredentialInput")?.value });
    });
    byId("createHomeAssistantConnectionForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createConnection({ kind: "home-assistant", name: byId("homeAssistantNameInput")?.value, endpoint: byId("homeAssistantUrlInput")?.value, credentialRef: byId("homeAssistantCredentialInput")?.value });
    });
    byId("createPairingCodeForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      createPairingCode({ connectionId: byId("pairingConnectionInput")?.value, agentId: byId("pairingAgentInput")?.value });
    });
    byId("deviceConnectionSelect")?.addEventListener("change", (event) => loadDevices(event.currentTarget.value));
    byId("createDeviceActionForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      requestDeviceAction({
        connectionId: byId("deviceActionConnectionInput")?.value,
        entityId: byId("deviceActionEntityInput")?.value,
        service: byId("deviceActionServiceInput")?.value,
        input: byId("deviceActionInput")?.value,
      });
    });
    root.querySelectorAll("[data-schedule-history]").forEach((button) => button.addEventListener("click", () => loadScheduleRuns(button.dataset.scheduleHistory)));
    root.querySelector("[data-schedule-history-close]")?.addEventListener("click", closeScheduleHistory);
    root.querySelectorAll("[data-schedule-toggle]").forEach((button) => button.addEventListener("click", () => toggleSchedule(button.dataset.scheduleToggle, button.dataset.enabled !== "true")));
    root.querySelectorAll("[data-schedule-run]").forEach((button) => button.addEventListener("click", () => runSchedule(button.dataset.scheduleRun)));
    root.querySelectorAll("[data-schedule-delete]").forEach((button) => button.addEventListener("click", () => deleteSchedule(button.dataset.scheduleDelete)));
    root.querySelectorAll("[data-delivery-retry]").forEach((button) => button.addEventListener("click", () => retryDelivery(button.dataset.deliveryRetry)));
    root.querySelectorAll("[data-connection-toggle]").forEach((button) => button.addEventListener("click", () => toggleConnection(button.dataset.connectionToggle, button.dataset.enabled !== "true")));
    root.querySelectorAll("[data-connection-test]").forEach((button) => button.addEventListener("click", () => testConnection(button.dataset.connectionTest)));
    root.querySelectorAll("[data-connection-delete]").forEach((button) => button.addEventListener("click", () => deleteConnection(button.dataset.connectionDelete)));
    root.querySelectorAll("[data-pairing-revoke]").forEach((button) => button.addEventListener("click", () => revokePairing(button.dataset.pairingRevoke)));
    root.querySelectorAll("[data-device-action-approve]").forEach((button) => button.addEventListener("click", () => approveDeviceAction(button.dataset.deviceActionApprove)));
    root.querySelectorAll("[data-device-action-deny]").forEach((button) => button.addEventListener("click", () => denyDeviceAction(button.dataset.deviceActionDeny)));
    if (!state.loaded && !state.loading) load();
  }

  return {
    approveDeviceAction,
    bind,
    closeScheduleHistory,
    createConnection,
    createPairingCode,
    createSchedule,
    deleteConnection,
    deleteSchedule,
    denyDeviceAction,
    getState,
    load,
    loadDevices,
    loadScheduleRuns,
    render: () => renderAutomationControl(getState()),
    requestDeviceAction,
    retryDelivery,
    revokePairing,
    runSchedule,
    testConnection,
    toggleConnection,
    toggleSchedule,
  };
}
