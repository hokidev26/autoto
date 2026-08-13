import { objectValue } from "./value-coercion.mjs";
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

export const schedulePresets = Object.freeze(["@every 15m", "@every 30m", "@hourly", "@daily", "@weekly", "0 9 * * 1", "0 22 * * *"]);
export const legacyIMDraftKeys = Object.freeze(["autoto.imGateway"]);
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

export function normalizeAgentOption(value = {}) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id ?? source.agentId, 160),
    title: boundedText(source.title ?? source.name, 160),
  };
}

// Client-side preview only: the server stays authoritative for scheduling. The
// preview covers the preset grammar (@every/@hourly/@daily/@weekly) and plain
// numeric five-field cron; anything fancier reports valid: false and the form
// says the server will do the math. The estimate uses the browser clock, which
// is why the label says "estimated" -- the schedule's own timezone is applied
// server-side.
export function describeCronExpression(expression, now = Date.now()) {
  const text = boundedText(expression, 256);
  if (!text) return { valid: false };
  const from = new Date(Number(now));
  if (Number.isNaN(from.getTime())) return { valid: false };
  const every = text.match(/^@every\s+(\d+)(s|m|h)$/i);
  if (every) {
    const amount = Number(every[1]);
    if (!amount) return { valid: false };
    const unit = { s: 1000, m: 60000, h: 3600000 }[every[2].toLowerCase()];
    return { valid: true, nextAt: new Date(from.getTime() + amount * unit).toISOString() };
  }
  if (/^@hourly$/i.test(text)) {
    const next = new Date(from);
    next.setMinutes(0, 0, 0);
    next.setHours(next.getHours() + 1);
    return { valid: true, nextAt: next.toISOString() };
  }
  if (/^@(daily|midnight)$/i.test(text)) {
    const next = new Date(from);
    next.setHours(0, 0, 0, 0);
    next.setDate(next.getDate() + 1);
    return { valid: true, nextAt: next.toISOString() };
  }
  if (/^@weekly$/i.test(text)) {
    const next = new Date(from);
    next.setHours(0, 0, 0, 0);
    do {
      next.setDate(next.getDate() + 1);
    } while (next.getDay() !== 0);
    return { valid: true, nextAt: next.toISOString() };
  }
  const fields = text.split(/\s+/);
  if (fields.length !== 5) return { valid: false };
  const numericField = (field, min, max) => {
    if (field === "*") return null;
    if (!/^\d+$/.test(field)) return undefined;
    const value = Number(field);
    return value >= min && value <= max ? value : undefined;
  };
  const minute = numericField(fields[0], 0, 59);
  const hour = numericField(fields[1], 0, 23);
  const dayOfMonth = numericField(fields[2], 1, 31);
  const month = numericField(fields[3], 1, 12);
  const dayOfWeek = numericField(fields[4], 0, 7);
  // Wildcard minutes or hours mean sub-daily cadence with offsets this preview
  // does not model, so only fully pinned times are estimated.
  if (minute === null || minute === undefined || hour === null || hour === undefined) return { valid: false };
  if (dayOfMonth === undefined || month === undefined || dayOfWeek === undefined) return { valid: false };
  const candidate = new Date(from);
  candidate.setHours(hour, minute, 0, 0);
  for (let step = 0; step <= 366; step += 1) {
    if (candidate.getTime() > from.getTime()
      && (dayOfMonth === null || candidate.getDate() === dayOfMonth)
      && (month === null || candidate.getMonth() + 1 === month)
      && (dayOfWeek === null || candidate.getDay() === (dayOfWeek === 7 ? 0 : dayOfWeek))) {
      return { valid: true, nextAt: candidate.toISOString() };
    }
    candidate.setDate(candidate.getDate() + 1);
  }
  return { valid: false };
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

// Metrics double as navigation: each one names the section that explains its
// number, and clicking scrolls there.
function renderMetric(label, value, tone = "", target = "") {
  return `<button class="automation-metric settings-stat-card ${escapeAttr(tone)}" type="button" data-metric-jump="${escapeAttr(target)}"><strong>${escapeHtml(String(boundedNumber(value)))}</strong><span>${escapeHtml(label)}</span></button>`;
}

function presetLabel(preset) {
  const key = `automation.schedule.presetLabels.${preset}`;
  const label = t(key);
  return label === key ? preset : label;
}

// Sections keep their data list on screen and tuck the create form behind this
// toggle. An empty section auto-opens its form instead, so first-run users are
// not shown a bare empty state with the way in hidden.
function renderFormToggle(key, open, openLabelKey) {
  return `<button class="automation-btn subtle automation-form-toggle" type="button" data-form-toggle="${escapeAttr(key)}" data-form-open="${open ? "true" : "false"}" aria-expanded="${open ? "true" : "false"}">${escapeHtml(t(open ? "automation.buttons.closeForm" : openLabelKey))}</button>`;
}

function renderPillarCard({ key, target, configured, title, description, summary, ctaLabel }) {
  return `
    <article class="automation-pillar settings-card" data-feature-card="${escapeAttr(key)}">
      <div class="automation-pillar-head">
        <h3>${escapeHtml(title)}</h3>
        <span class="automation-status settings-badge ${configured ? "ok" : "muted"}">${escapeHtml(t(configured ? "automation.pillars.configured" : "automation.pillars.notConfigured"))}</span>
      </div>
      <p>${escapeHtml(configured ? summary : description)}</p>
      <button class="automation-btn ${configured ? "subtle" : "primary"}" type="button" data-feature-cta="${escapeAttr(key)}" data-feature-target="${escapeAttr(target)}"${configured ? "" : ` data-feature-open="true"`}>${escapeHtml(configured ? t("automation.pillars.manage") : ctaLabel)}</button>
    </article>`;
}

// The three pillars of this page, as entry cards: each shows whether it is set
// up, a one-line summary once it is, and the way in when it is not.
function renderFeaturePillars({ telegramConnections, pairings, schedules, homeAssistantConnections, deviceCount }) {
  const activePairings = pairings.filter((item) => item.status !== "revoked").length;
  const enabledSchedules = schedules.filter((item) => item.enabled).length;
  return `
    <div class="automation-pillars" data-automation-pillars>
      ${renderPillarCard({
        key: "telegram",
        target: "connections",
        configured: telegramConnections.length > 0,
        title: t("automation.pillars.telegram.title"),
        description: t("automation.pillars.telegram.description"),
        summary: t("automation.pillars.telegram.summary", { count: telegramConnections.length, paired: activePairings }),
        ctaLabel: t("automation.pillars.telegram.cta"),
      })}
      ${renderPillarCard({
        key: "schedule",
        target: "schedules",
        configured: schedules.length > 0,
        title: t("automation.pillars.schedules.title"),
        description: t("automation.pillars.schedules.description"),
        summary: t("automation.pillars.schedules.summary", { count: schedules.length, enabled: enabledSchedules }),
        ctaLabel: t("automation.pillars.schedules.cta"),
      })}
      ${renderPillarCard({
        key: "homeAssistant",
        target: "home-assistant",
        configured: homeAssistantConnections.length > 0,
        title: t("automation.pillars.home.title"),
        description: t("automation.pillars.home.description"),
        summary: t("automation.pillars.home.summary", { count: homeAssistantConnections.length, devices: deviceCount }),
        ctaLabel: t("automation.pillars.home.cta"),
      })}
    </div>`;
}

// Pending device actions are the one thing on this page that expires while the
// reader scrolls, so they are pinned above everything else. The cards reuse the
// standard renderer, and their approve/deny buttons share the section bindings.
function renderPendingApprovals(deviceActions, busy, now) {
  const pending = deviceActions.filter((item) => ["pending", "pending_approval"].includes(item.status) && !isDeviceActionExpired(item, now));
  if (!pending.length) return "";
  return `
    <section class="automation-pending settings-card" data-automation-pending role="region" aria-label="${escapeAttr(t("automation.pending.title"))}">
      <div class="automation-section-head settings-card-header">
        <div><span>${escapeHtml(t("automation.deviceActions.kicker"))}</span><h3>${escapeHtml(t("automation.pending.title", { count: pending.length }))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.pending.description"))}</p></div>
        <button class="automation-btn subtle" type="button" data-metric-jump="device-actions">${escapeHtml(t("automation.pending.jump"))}</button>
      </div>
      <div class="automation-card-grid settings-data-list">${pending.slice(0, 5).map((item) => renderDeviceAction(item, busy, now)).join("")}</div>
    </section>`;
}

// Agent IDs are opaque; nobody should have to copy one from another screen.
// With the agent list loaded the field becomes a picker; the plain input stays
// as the fallback so the form still works when that list is unavailable.
function renderAgentField(id, agents, selectLabelKey) {
  if (!agents.length) return `<input id="${escapeAttr(id)}" maxlength="128" placeholder="agent-id" required />`;
  return `<select id="${escapeAttr(id)}" required><option value="">${escapeHtml(t(selectLabelKey))}</option>${agents.map((agent) => `<option value="${escapeAttr(agent.id)}">${escapeHtml(agent.title ? `${agent.title} · ${agent.id.slice(0, 8)}` : agent.id)}</option>`).join("")}</select>`;
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
      <dl class="automation-kv"><div><dt>${escapeHtml(t("automation.schedule.agentId"))}</dt><dd>${escapeHtml(schedule.agentId || t("automation.timestamp.empty"))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.permission"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("permissionMode", permissionMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.environment"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("environmentMode", environmentMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.narrator"))}</dt><dd>${escapeHtml(formatScheduleEnumValue("narratorMode", narratorMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.nextRun"))}</dt><dd>${escapeHtml(formatTimestamp(schedule.nextRunAt))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.lastRun"))}</dt><dd>${escapeHtml(schedule.lastRunAt ? `${statusLabel(schedule.lastOutcome || "unknown")} · ${formatTimestamp(schedule.lastRunAt)}` : t("automation.schedule.neverRan"))}</dd></div></dl>
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

function renderDeviceRow(value, connectionId = "") {
  const device = normalizeDevice(value);
  return `
    <article class="automation-device-row settings-data-list-row${connectionId ? " has-action" : ""}" data-entity-row data-entity-search="${escapeAttr(`${device.entityId} ${device.name}`.toLowerCase())}">
      <div><strong>${escapeHtml(device.name || device.entityId)}</strong><small class="mono">${escapeHtml(device.entityId)}</small></div>
      <span>${escapeHtml(device.state)}</span>
      <span class="automation-readonly">${escapeHtml(t("automation.homeAssistant.readonly"))}</span>
      ${connectionId ? `<button class="automation-btn subtle" type="button" data-entity-action="${escapeAttr(device.entityId)}">${escapeHtml(t("automation.homeAssistant.requestAction"))}</button>` : ""}
    </article>`;
}

// Entities arrive as one flat list; grouped by domain they read as rooms of a
// house rather than a dump. The group heads collapse out of view when a search
// filters all their rows away.
function renderDeviceGroups(devices, connectionId = "") {
  const groups = new Map();
  for (const device of devices.map(normalizeDevice)) {
    const domain = device.domain || "other";
    if (!groups.has(domain)) groups.set(domain, []);
    groups.get(domain).push(device);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([domain, items]) => `
      <div class="automation-device-group" data-device-group>
        <div class="automation-device-group-head"><strong>${escapeHtml(domain)}</strong><span>${escapeHtml(String(items.length))}</span></div>
        ${items.map((item) => renderDeviceRow(item, connectionId)).join("")}
      </div>`).join("");
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
  const agents = (Array.isArray(source.agents) ? source.agents : []).slice(0, 200).map(normalizeAgentOption).filter((item) => item.id);
  const deviceFilter = boundedText(source.deviceFilter, 120);
  const deviceActionPrefill = objectValue(source.deviceActionPrefill);
  const formOpen = objectValue(source.formOpen);
  // Explicit user intent wins; otherwise a section with nothing in it opens its
  // form, because an empty list plus a hidden form is a dead end.
  const formVisible = (key, autoOpen) => (Object.hasOwn(formOpen, key) ? booleanValue(formOpen[key], false) : Boolean(autoOpen && loaded && !loading));
  const showForm = {
    schedule: formVisible("schedule", !schedules.length),
    telegram: formVisible("telegram", !telegramConnections.length),
    homeAssistant: formVisible("homeAssistant", !homeAssistantConnections.length),
    pairing: formVisible("pairing", !pairings.length),
    deviceAction: formVisible("deviceAction", !deviceActions.length),
  };
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

      ${renderPendingApprovals(deviceActions, busy, boundedNumber(source.now, Date.now()) || Date.now())}

      ${renderFeaturePillars({
        telegramConnections,
        pairings,
        schedules,
        homeAssistantConnections,
        deviceCount: monitoring.deviceCount || devices.length,
      })}

      <section class="automation-metrics settings-stat-grid" aria-label="${escapeAttr(t("automation.metrics.ariaLabel"))}">
        ${renderMetric(t("automation.metrics.activeRuns"), monitoring.activeRuns, monitoring.activeRuns ? "active" : "", "schedules")}
        ${renderMetric(t("automation.metrics.pendingApprovals"), monitoring.pendingApprovals, monitoring.pendingApprovals ? "warn" : "", "device-actions")}
        ${renderMetric(t("automation.metrics.schedules"), monitoring.scheduleCount || schedules.length, "", "schedules")}
        ${renderMetric(t("automation.metrics.notifications"), monitoring.notificationCount || deliveries.length, "", "deliveries")}
        ${renderMetric(t("automation.metrics.channels"), monitoring.channelCount || connections.length, "", "connections")}
        ${renderMetric(t("automation.metrics.devices"), monitoring.deviceCount || devices.length, "", "home-assistant")}
      </section>

      <div class="automation-section-grid">
        <section class="automation-section settings-card settings-page-section span-2" data-automation-section="schedules">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.schedule.kicker"))}</span><h3>${escapeHtml(t("automation.schedule.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.schedule.description"))}</p></div>${scheduleViewMode === "history" ? "" : renderFormToggle("schedule", showForm.schedule, "automation.buttons.newSchedule")}${errors.monitoring ? `<small class="error">${escapeHtml(redactSensitiveText(errors.monitoring))}</small>` : ""}</div>
          ${scheduleViewMode === "history" ? renderScheduleHistoryMode(selectedScheduleHistoryId, selectedSchedule, scheduleRunHistory[selectedScheduleHistoryId]) : `
          ${showForm.schedule ? `<form id="createScheduleForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.schedule.name"))}<input id="scheduleNameInput" maxlength="120" placeholder="${escapeAttr(t("automation.schedule.namePlaceholder"))}" required /></label>
            <label>${escapeHtml(t("automation.schedule.preset"))}<select id="schedulePresetInput"><option value="">${escapeHtml(t("automation.schedule.custom"))}</option>${schedulePresets.map((preset) => `<option value="${escapeAttr(preset)}">${escapeHtml(presetLabel(preset))}</option>`).join("")}</select></label>
            <label>${escapeHtml(t("automation.schedule.expression"))}<input id="scheduleExpressionInput" maxlength="256" placeholder="@every 15m" required /><small id="scheduleCronPreview" class="automation-cron-preview" aria-live="polite"></small></label>
            <label>${escapeHtml(t("automation.schedule.agentId"))}${renderAgentField("scheduleAgentInput", agents, "automation.schedule.selectAgent")}</label>
            <label>${escapeHtml(t("automation.schedule.timezone"))}<input id="scheduleTimezoneInput" maxlength="128" value="UTC" placeholder="Asia/Shanghai" required /></label>
            <label>${escapeHtml(t("automation.schedule.permission"))}<select id="schedulePermissionInput">${scheduleEnumOptions("permissionMode", ["readOnly", "acceptEdits"], "readOnly")}</select></label>
            <label>${escapeHtml(t("automation.schedule.environment"))}<select id="scheduleEnvironmentInput">${scheduleEnumOptions("environmentMode", ["workline", "standalone"], "workline")}</select></label>
            <label>${escapeHtml(t("automation.schedule.narrator"))}<select id="scheduleNarratorInput">${scheduleEnumOptions("narratorMode", ["reuse", "new"], "reuse")}</select></label>
            <label class="span-2">${escapeHtml(t("automation.schedule.prompt"))}<textarea id="schedulePromptInput" rows="3" maxlength="8000" placeholder="${escapeAttr(t("automation.schedule.promptPlaceholder"))}" required></textarea></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn primary" type="submit"${busy["schedule:create"] ? " disabled" : ""}>${escapeHtml(t(busy["schedule:create"] ? "automation.buttons.creating" : "automation.buttons.createSchedule"))}</button></div>
          </form>` : ""}
          <div class="automation-card-grid settings-data-list">${scheduleState || schedules.map((item, index) => renderScheduleCard(scheduleSources[index], busy, scheduleRunHistory[item.id])).join("")}</div>`}
        </section>

        <!-- The Telegram flow reads left to right: connect, pair, then watch
             deliveries. All three are usually short lists, so alone in a
             two-column grid each left half a row of dead space; as a trio they
             share one full-width row and their lists scroll past a cap instead
             of stretching the row. -->
        <div class="automation-trio" data-automation-trio>
        <section class="automation-section settings-card settings-page-section" data-automation-section="connections">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.connections.kicker"))}</span><h3>${escapeHtml(t("automation.connections.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.connections.description"))}</p></div>${renderFormToggle("telegram", showForm.telegram, "automation.buttons.newTelegram")}</div>
          ${showForm.telegram ? `<form id="createTelegramConnectionForm" class="automation-form settings-form-grid compact">
            <label>${escapeHtml(t("automation.connections.name"))}<input id="telegramNameInput" maxlength="160" placeholder="${escapeAttr(t("automation.connections.namePlaceholder"))}" /></label>
            <label>${escapeHtml(t("automation.connections.credential"))}<input id="telegramCredentialInput" maxlength="160" placeholder="env:AUTOTO_TELEGRAM_BOT_TOKEN" autocomplete="off" required /></label>
            <div class="automation-form-actions settings-inline-actions"><button class="automation-btn primary" type="submit"${busy["connection:create:telegram"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createTelegram"))}</button></div>
          </form>` : ""}
          <div class="automation-card-grid single">${connectionState || telegramConnections.map((item) => renderConnectionCard(item, busy)).join("") || `<div class="automation-empty">${escapeHtml(t("automation.connections.emptyTelegram"))}</div>`}</div>
        </section>

        <section class="automation-section settings-card settings-page-section" data-automation-section="pairing">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.pairing.kicker"))}</span><h3>${escapeHtml(t("automation.pairing.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.pairing.description"))}</p></div>${renderFormToggle("pairing", showForm.pairing, "automation.buttons.newPairingCode")}</div>
          ${showForm.pairing ? `<form id="createPairingCodeForm" class="automation-form settings-form-grid compact">
            <label>${escapeHtml(t("automation.pairing.connection"))}<select id="pairingConnectionInput" required><option value="">${escapeHtml(t("automation.pairing.selectConnection"))}</option>${connectionOptions(connections, "telegram")}</select></label>
            <label>${escapeHtml(t("automation.pairing.agentId"))}${renderAgentField("pairingAgentInput", agents, "automation.pairing.selectAgent")}</label>
            <div class="automation-form-actions settings-inline-actions"><button class="automation-btn primary" type="submit"${busy["pairing:create"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createPairingCode"))}</button></div>
          </form>` : ""}
          ${pairingCode.code ? `<div class="automation-pairing-code"><span>${escapeHtml(t("automation.pairing.code"))}</span><strong>${escapeHtml(pairingCode.code)}</strong><small>${escapeHtml(t("automation.pairing.expiresAt", { timestamp: formatTimestamp(pairingCode.expiresAt) }))}</small></div>` : ""}
          <div class="automation-list settings-data-list settings-table">${pairingState || pairings.map((item) => renderPairingCard(item, busy)).join("")}</div>
        </section>

        <section class="automation-section settings-card settings-page-section" data-automation-section="deliveries">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.deliveries.kicker"))}</span><h3>${escapeHtml(t("automation.deliveries.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.deliveries.description"))}</p></div></div>
          <div class="automation-list settings-data-list settings-table">${deliveryState || deliveries.map((item) => renderDeliveryRow(item, busy)).join("")}</div>
        </section>
        </div>

        <section class="automation-section settings-card settings-page-section span-2" data-automation-section="home-assistant">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.homeAssistant.kicker"))}</span><h3>${escapeHtml(t("automation.homeAssistant.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.homeAssistant.description"))}</p></div>${renderFormToggle("homeAssistant", showForm.homeAssistant, "automation.buttons.newHomeAssistant")}</div>
          ${showForm.homeAssistant ? `<form id="createHomeAssistantConnectionForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.homeAssistant.name"))}<input id="homeAssistantNameInput" maxlength="160" placeholder="Home Assistant" /></label>
            <label>${escapeHtml(t("automation.homeAssistant.url"))}<input id="homeAssistantUrlInput" maxlength="400" placeholder="http://homeassistant.local:8123" required /></label>
            <label class="span-2">${escapeHtml(t("automation.homeAssistant.credential"))}<input id="homeAssistantCredentialInput" maxlength="160" placeholder="env:AUTOTO_HOME_ASSISTANT_TOKEN" autocomplete="off" required /></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn primary" type="submit"${busy["connection:create:home-assistant"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.createHomeAssistant"))}</button></div>
          </form>` : ""}
          <div class="automation-card-grid settings-data-list">${homeAssistantConnections.length ? homeAssistantConnections.map((item) => renderConnectionCard(item, busy)).join("") : `<div class="automation-empty">${escapeHtml(t("automation.homeAssistant.empty"))}</div>`}</div>
          <div class="automation-device-toolbar"><label>${escapeHtml(t("automation.homeAssistant.viewConnection"))}<select id="deviceConnectionSelect"><option value="">${escapeHtml(t("automation.homeAssistant.selectConnection"))}</option>${connectionOptions(connections, "home-assistant", selectedConnectionId)}</select></label><label class="automation-device-search">${escapeHtml(t("automation.homeAssistant.search"))}<input id="deviceSearchInput" maxlength="120" value="${escapeAttr(deviceFilter)}" placeholder="${escapeAttr(t("automation.homeAssistant.searchPlaceholder"))}" /></label><span>${escapeHtml(t("automation.homeAssistant.devicesLimit", { count: automationLimits.devices }))}</span></div>
          <div class="automation-device-list settings-data-list settings-table">${deviceState || renderDeviceGroups(devices, selectedConnectionId)}</div>
        </section>

        <section class="automation-section span-2 danger-section" data-automation-section="device-actions">
          <div class="automation-section-head settings-card-header"><div><span>${escapeHtml(t("automation.deviceActions.kicker"))}</span><h3>${escapeHtml(t("automation.deviceActions.title"))}</h3><p data-settings-help-copy>${escapeHtml(t("automation.deviceActions.description"))}</p></div>${renderFormToggle("deviceAction", showForm.deviceAction, "automation.buttons.newDeviceAction")}<span class="automation-danger-badge settings-badge destructive">${escapeHtml(t("automation.deviceActions.riskBoundary"))}</span></div>
          ${showForm.deviceAction ? `<form id="createDeviceActionForm" class="automation-form settings-form-grid">
            <label>${escapeHtml(t("automation.deviceActions.connection"))}<select id="deviceActionConnectionInput" required><option value="">${escapeHtml(t("automation.deviceActions.selectConnection"))}</option>${connectionOptions(connections, "home-assistant", boundedText(deviceActionPrefill.connectionId, 160) || selectedConnectionId)}</select></label>
            <label>${escapeHtml(t("automation.deviceActions.entityId"))}<input id="deviceActionEntityInput" maxlength="255" placeholder="light.living_room" value="${escapeAttr(boundedText(deviceActionPrefill.entityId, 220))}" required /></label>
            <label>${escapeHtml(t("automation.deviceActions.service"))}<input id="deviceActionServiceInput" maxlength="64" placeholder="turn_off" required /></label>
            <label>${escapeHtml(t("automation.deviceActions.input"))}<input id="deviceActionInput" maxlength="1200" placeholder='{"brightness": 0}' /></label>
            <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn danger destructive" type="submit"${busy["device-action:create"] ? " disabled" : ""}>${escapeHtml(t("automation.buttons.requestLocal"))}</button></div>
          </form>` : ""}
          <div class="automation-card-grid settings-data-list">${actionState || deviceActions.map((item) => renderDeviceAction(item, busy, source.now)).join("")}</div>
        </section>

        <section class="automation-section settings-card settings-page-section span-2" data-automation-section="audit">
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
    agents: [],
    // Explicit open/closed choices per create form; a key that was never
    // touched falls back to the render's auto-open-when-empty rule.
    formOpen: {},
    deviceFilter: "",
    deviceActionPrefill: null,
    // Section to scroll to after the next render, set by feature-card CTAs and
    // entity action shortcuts whose target only exists in the new DOM.
    pendingFocus: "",
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
      agents: state.agents.map((item) => ({ ...item })),
      formOpen: { ...state.formOpen },
      deviceFilter: state.deviceFilter,
      deviceActionPrefill: state.deviceActionPrefill ? { ...state.deviceActionPrefill } : null,
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
      // Feeds the agent pickers in the schedule and pairing forms. Failure is
      // tolerable: the forms fall back to a plain agent-id input.
      ["agents", "/api/agents?limit=200"],
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
      if (section === "agents") state.agents = collectionResult(result.value, ["agents", "items"], 200, normalizeAgentOption).filter((item) => item.id);
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
      onResult: () => { state.formOpen.schedule = false; },
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
      onResult: () => { state.formOpen[payload.kind === "telegram" ? "telegram" : "homeAssistant"] = false; },
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
      onResult: (result) => {
        state.latestPairingCode = normalizePairingCode({ ...objectValue(result), ...payload });
        state.formOpen.pairing = false;
      },
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
        state.formOpen.deviceAction = false;
        state.deviceActionPrefill = null;
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
    // Targets are the fixed section slugs this module renders itself, so the
    // selector needs no escaping.
    const scrollToSection = (target) => {
      if (!/^[a-z-]+$/.test(String(target || ""))) return;
      root.querySelector(`[data-automation-section="${target}"]`)?.scrollIntoView?.({ behavior: "smooth", block: "start" });
    };
    byId("refreshAutomationControlBtn")?.addEventListener("click", () => load());
    root.querySelectorAll("[data-form-toggle]").forEach((button) => button.addEventListener("click", () => {
      state.formOpen[button.dataset.formToggle] = button.dataset.formOpen !== "true";
      emit();
    }));
    root.querySelectorAll("[data-metric-jump]").forEach((button) => button.addEventListener("click", () => scrollToSection(button.dataset.metricJump)));
    root.querySelectorAll("[data-feature-cta]").forEach((button) => button.addEventListener("click", () => {
      const target = button.dataset.featureTarget || "";
      if (button.dataset.featureOpen === "true") {
        state.formOpen[button.dataset.featureCta] = true;
        state.pendingFocus = target;
        emit();
        return;
      }
      scrollToSection(target);
    }));
    const cronPreview = byId("scheduleCronPreview");
    const updateCronPreview = () => {
      const expression = String(byId("scheduleExpressionInput")?.value || "").trim();
      if (!cronPreview) return;
      if (!expression) {
        cronPreview.textContent = "";
        return;
      }
      const described = describeCronExpression(expression, now());
      cronPreview.textContent = described.valid
        ? t("automation.schedule.preview", { timestamp: formatTimestamp(described.nextAt) })
        : t("automation.schedule.previewUnknown");
    };
    byId("scheduleExpressionInput")?.addEventListener("input", updateCronPreview);
    byId("schedulePresetInput")?.addEventListener("change", (event) => {
      if (event.currentTarget.value && byId("scheduleExpressionInput")) byId("scheduleExpressionInput").value = event.currentTarget.value;
      updateCronPreview();
    });
    updateCronPreview();
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
    // The entity search filters in place: it touches only hidden flags on the
    // rendered rows and never emits, so typing does not trigger a re-render
    // that would steal focus. The query is kept on state solely so a re-render
    // triggered by something else can restore the filter.
    const deviceSearch = byId("deviceSearchInput");
    const applyDeviceFilter = () => {
      const query = String(deviceSearch?.value || "").trim().toLowerCase();
      state.deviceFilter = query;
      root.querySelectorAll("[data-entity-row]").forEach((row) => {
        row.hidden = Boolean(query) && !String(row.dataset.entitySearch || "").includes(query);
      });
      root.querySelectorAll("[data-device-group]").forEach((group) => {
        group.hidden = !group.querySelector("[data-entity-row]:not([hidden])");
      });
    };
    deviceSearch?.addEventListener("input", applyDeviceFilter);
    if (state.deviceFilter) applyDeviceFilter();
    root.querySelectorAll("[data-entity-action]").forEach((button) => button.addEventListener("click", () => {
      state.deviceActionPrefill = { entityId: button.dataset.entityAction, connectionId: state.selectedConnectionId };
      state.formOpen.deviceAction = true;
      state.pendingFocus = "device-actions";
      emit();
    }));
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
    // A CTA that opened a form re-rendered the page before its target section
    // existed; this bind runs against the new DOM, so honor the deferred scroll
    // here. The prefill is consumed the same way: it is baked into the markup
    // just rendered, and clearing it keeps later re-renders from stomping on
    // whatever the user edits in the form.
    if (state.pendingFocus) {
      scrollToSection(state.pendingFocus);
      state.pendingFocus = "";
    }
    state.deviceActionPrefill = null;
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
