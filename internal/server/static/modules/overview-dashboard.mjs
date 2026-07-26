const LIST_LIMITS = Object.freeze({
  recentConversations: 8,
  activeTasks: 8,
  activeRuns: 6,
  upcomingSchedules: 8,
});

const ACTIONS = new Set([
  "refresh",
  "conversation",
  "tasks",
  "runs",
  "schedules",
  "approvals",
  "open-conversation",
  "open-task",
  "open-run",
  "open-schedule",
]);

const DEFAULT_TEXT = Object.freeze({
  title: "工作总览",
  subtitle: "快速了解当前进展并继续重要工作。",
  refresh: "刷新",
  refreshing: "正在刷新…",
  capturedAt: "更新于 {time}",
  loading: "正在加载首页总览…",
  loadFailed: "首页总览加载失败",
  retryHint: "请稍后重试，或使用刷新按钮重新加载。",
  loaded: "首页总览已更新。",
  conversations: "对话",
  tasks: "任务",
  running: "正在执行",
  schedules: "排程",
  taskBreakdown: "待办 {todo} · 进行中 {doing} · 已完成 {done}",
  scheduleBreakdown: "已启用 {enabled} / 共 {total}",
  continueWorking: "继续工作",
  continueWorkingHint: "最近更新的对话",
  inProgress: "正在进行",
  inProgressHint: "活跃任务与运行",
  upcoming: "即将执行",
  upcomingHint: "计划中的自动执行",
  pending: "待处理提示",
  pendingHint: "需要关注的项目",
  viewAll: "查看全部",
  viewSection: "查看全部{section}",
  openConversation: "打开对话",
  openTask: "打开任务",
  openRun: "打开运行",
  openSchedule: "打开排程",
  recentEmpty: "暂无最近对话。",
  tasksEmpty: "暂无进行中的任务。",
  runsEmpty: "暂无活跃运行。",
  schedulesEmpty: "暂无即将执行的排程。",
  pendingEmpty: "当前没有待处理提示。",
  untitledConversation: "未命名对话",
  untitledTask: "未命名任务",
  untitledSchedule: "未命名排程",
  unknownAgent: "未知代理",
  unknownProject: "未关联项目",
  unknownStatus: "状态未知",
  unknownTime: "时间未定",
  activeTasks: "活跃任务",
  activeRuns: "活跃运行",
  runningAgents: "运行代理",
  pendingApprovals: "待审批",
  pendingApprovalsCount: "有 {count} 项操作等待审批",
  dueSchedules: "到期排程",
  dueSchedulesCount: "有 {count} 个排程已到执行时间",
  failedSchedules: "失败排程",
  failedSchedulesCount: "有 {count} 个排程最近执行失败",
  startedAt: "开始于 {time}",
  nextRunAt: "下次执行 {time}",
  lastOutcome: "上次结果：{outcome}",
  priority: "优先级：{priority}",
  activity: "使用热力图",
  activityTotal: "过去一年共 {count} 次模型请求",
  activityEmpty: "过去一年还没有使用记录。",
  activityUnavailable: "使用记录暂时无法加载。",
  activityDay: "{date}：{count} 次请求",
  activityDayEmpty: "{date}：没有使用",
  activityLess: "少",
  activityMore: "多",
  activityMon: "周一",
  activityWed: "周三",
  activityFri: "周五",
});

// A GitHub-style contribution grid: 53 columns of 7 weekdays, the last column
// holding today. Intensity comes from /api/usage/history?bucket=day, whose
// requestCount is the closest thing to "did I actually use the IDE that day".
const HEATMAP_WEEKS = 53;
const HEATMAP_LEVELS = 4;
const DAY_MS = 86_400_000;
// Only Mon/Wed/Fri are labelled; seven labels in that space become unreadable.
const HEATMAP_WEEKDAY_LABEL_KEYS = Object.freeze(["", "activityMon", "", "activityWed", "", "activityFri", ""]);

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function boundedText(value, maximum = 200) {
  try {
    return String(value ?? "").replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "").slice(0, maximum).trim();
  } catch {
    return "";
  }
}

function boundedCount(value) {
  try {
    const number = Number(value);
    if (!Number.isFinite(number)) return 0;
    return Math.min(999_999_999, Math.max(0, Math.floor(number)));
  } catch {
    return 0;
  }
}

function escapeHtml(value) {
  return boundedText(value, 4000).replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[character]);
}

// Maps an overview card action onto the surface that should handle it. The
// bare "tasks"/"schedules" actions open their surface without a selection,
// while the "open-*" variants carry the entity id, so the two are distinguished
// by usesId rather than by separate handlers. Unknown actions resolve to null
// and are ignored: overview payloads come from the server and must not be able
// to drive navigation the client does not recognise.
const overviewNavigationRoutes = new Map([
  ["conversation", { handler: "rail-conversation", usesId: false }],
  ["tasks", { handler: "task", usesId: false }],
  ["open-task", { handler: "task", usesId: true }],
  ["schedules", { handler: "schedules", usesId: true }],
  ["open-schedule", { handler: "schedules", usesId: true }],
  ["approvals", { handler: "approvals", usesId: false }],
  ["runs", { handler: "runs", usesId: true }],
  ["open-run", { handler: "runs", usesId: true }],
  ["open-conversation", { handler: "conversation", usesId: true }],
]);

export function overviewNavigationRoute(action) {
  return overviewNavigationRoutes.get(String(action || "")) || null;
}

export function overviewRailTarget(value = {}) {
  if (value?.overviewActive === true) return "home";
  if (value?.activeWorkbench === "schedules") return "schedules";
  return "conversation";
}

export function resolveOverviewStartup({ requestedView = "", hasConversation = false, hasProject = false, mobile = false } = {}) {
  const view = boundedText(requestedView, 40).toLowerCase();
  const taskSurface = view === "tasks";
  const scheduleSurface = view === "schedules";
  const explicitConversationSurface = ["conversation", "details", "browser", "terminal"].includes(view);
  const conversationSurface = explicitConversationSurface || (Boolean(mobile) && !taskSurface && !scheduleSurface);
  return {
    overviewActive: !taskSurface && !scheduleSurface && !conversationSurface,
    workbench: scheduleSurface ? "schedules" : taskSurface ? "workbench" : "conversation",
    restoreConversation: Boolean(hasConversation),
    selectFallbackProject: Boolean(conversationSurface && !hasConversation && hasProject),
  };
}

function normalizeConversation(value) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id, 160),
    title: boundedText(source.title, 240),
    status: boundedText(source.status, 48),
    projectId: boundedText(source.projectId, 160),
    projectName: boundedText(source.projectName, 160),
    updatedAt: boundedText(source.updatedAt, 80),
  };
}

function normalizeTask(value) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id, 160),
    title: boundedText(source.title, 240),
    status: boundedText(source.status, 48),
    priority: boundedText(source.priority, 48),
    agentId: boundedText(source.agentId, 160),
    agentTitle: boundedText(source.agentTitle, 160),
    projectId: boundedText(source.projectId, 160),
    projectName: boundedText(source.projectName, 160),
    updatedAt: boundedText(source.updatedAt, 80),
  };
}

function normalizeRun(value) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id, 160),
    agentId: boundedText(source.agentId, 160),
    agentTitle: boundedText(source.agentTitle, 160),
    status: boundedText(source.status, 48),
    startedAt: boundedText(source.startedAt, 80),
  };
}

function normalizeSchedule(value) {
  const source = objectValue(value);
  return {
    id: boundedText(source.id, 160),
    name: boundedText(source.name, 240),
    agentId: boundedText(source.agentId, 160),
    agentTitle: boundedText(source.agentTitle, 160),
    nextRunAt: boundedText(source.nextRunAt, 80),
    timezone: boundedText(source.timezone, 80),
    lastOutcome: boundedText(source.lastOutcome, 48),
  };
}

function boundedList(value, limit, normalize) {
  if (!Array.isArray(value)) return [];
  const result = [];
  for (let index = 0; index < value.length && result.length < limit; index += 1) {
    result.push(normalize(value[index]));
  }
  return result;
}

export function normalizeOverviewPayload(payload) {
  const source = objectValue(payload);
  const summary = objectValue(source.summary);
  const tasks = objectValue(summary.tasks);
  const schedules = objectValue(summary.schedules);
  return {
    capturedAt: boundedText(source.capturedAt, 80),
    summary: {
      conversations: boundedCount(summary.conversations),
      runningAgents: boundedCount(summary.runningAgents),
      tasks: {
        total: boundedCount(tasks.total),
        todo: boundedCount(tasks.todo),
        doing: boundedCount(tasks.doing),
        done: boundedCount(tasks.done),
      },
      activeRuns: boundedCount(summary.activeRuns),
      pendingApprovals: boundedCount(summary.pendingApprovals),
      schedules: {
        total: boundedCount(schedules.total),
        enabled: boundedCount(schedules.enabled),
        due: boundedCount(schedules.due),
        failed: boundedCount(schedules.failed),
      },
    },
    recentConversations: boundedList(source.recentConversations, LIST_LIMITS.recentConversations, normalizeConversation),
    activeTasks: boundedList(source.activeTasks, LIST_LIMITS.activeTasks, normalizeTask),
    activeRuns: boundedList(source.activeRuns, LIST_LIMITS.activeRuns, normalizeRun),
    upcomingSchedules: boundedList(source.upcomingSchedules, LIST_LIMITS.upcomingSchedules, normalizeSchedule),
  };
}

// Day arithmetic runs on UTC milliseconds even though the days themselves are
// local. Stepping with Date.UTC keeps every day exactly 86400000ms apart, which
// local-time arithmetic does not guarantee across a daylight-saving boundary.
function parseISODay(value) {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(boundedText(value, 10));
  if (!match) return null;
  const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
  const stamp = Date.UTC(year, month - 1, day);
  if (!Number.isFinite(stamp)) return null;
  const date = new Date(stamp);
  // Date.UTC rolls 2024-02-30 over into March; reject rather than accept a
  // silently different day.
  return date.getUTCMonth() === month - 1 && date.getUTCDate() === day ? stamp : null;
}

function formatISODay(stamp) {
  const date = new Date(stamp);
  const month = String(date.getUTCMonth() + 1).padStart(2, "0");
  const day = String(date.getUTCDate()).padStart(2, "0");
  return `${date.getUTCFullYear()}-${month}-${day}`;
}

export function localTodayISO(now = new Date()) {
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${now.getFullYear()}-${month}-${day}`;
}

// getTimezoneOffset counts minutes *behind* UTC, so UTC+8 reports -480. The
// server modifier wants the conventional sign.
export function localTimezoneOffsetMinutes(now = new Date()) {
  const offset = -Math.round(Number(now.getTimezoneOffset?.()) || 0);
  return Number.isFinite(offset) ? Math.max(-840, Math.min(840, offset)) : 0;
}

function heatmapLevel(count, max) {
  if (count <= 0 || max <= 0) return 0;
  return Math.max(1, Math.min(HEATMAP_LEVELS, Math.ceil((count / max) * HEATMAP_LEVELS)));
}

function shortMonthLabel(stamp) {
  const date = new Date(stamp);
  try {
    return boundedText(new Intl.DateTimeFormat(undefined, { month: "short", timeZone: "UTC" }).format(date), 12);
  } catch {
    return String(date.getUTCMonth() + 1);
  }
}

// Turns the /api/usage/history day trend into the calendar grid. Pure, and
// "today" is injected so the layout can be asserted without freezing the clock.
export function buildActivityHeatmap(trend, { today = localTodayISO(), weeks = HEATMAP_WEEKS } = {}) {
  const counts = new Map();
  const points = Array.isArray(trend) ? trend.slice(0, 4000) : [];
  for (const point of points) {
    const day = boundedText(point?.bucket, 10);
    if (parseISODay(day) === null) continue;
    counts.set(day, (counts.get(day) || 0) + boundedCount(point?.requestCount));
  }

  const columns = Math.max(1, Math.min(53, Math.floor(weeks) || HEATMAP_WEEKS));
  const todayStamp = parseISODay(today) ?? parseISODay(localTodayISO());
  // Pad to the end of today's week so the grid is rectangular and today sits in
  // the final column, then walk back a whole number of weeks.
  const endStamp = todayStamp + (6 - new Date(todayStamp).getUTCDay()) * DAY_MS;
  const startStamp = endStamp - (columns * 7 - 1) * DAY_MS;

  let total = 0;
  let max = 0;
  for (const [day, count] of counts) {
    const stamp = parseISODay(day);
    if (stamp === null || stamp < startStamp || stamp > todayStamp) continue;
    total += count;
    if (count > max) max = count;
  }

  const grid = [];
  let previousMonth = -1;
  for (let column = 0; column < columns; column += 1) {
    const days = [];
    for (let row = 0; row < 7; row += 1) {
      const stamp = startStamp + (column * 7 + row) * DAY_MS;
      const date = formatISODay(stamp);
      const future = stamp > todayStamp;
      const count = future ? 0 : (counts.get(date) || 0);
      days.push({ date, count, level: future ? 0 : heatmapLevel(count, max), future });
    }
    const columnMonth = new Date(startStamp + column * 7 * DAY_MS).getUTCMonth();
    // Label a column only where the month turns over, and never in the first
    // column, whose month name would sit half off the left edge.
    const label = column > 0 && columnMonth !== previousMonth ? shortMonthLabel(startStamp + column * 7 * DAY_MS) : "";
    previousMonth = columnMonth;
    grid.push({ days, monthLabel: label });
  }

  return { weeks: grid, total, max, start: formatISODay(startStamp), end: formatISODay(todayStamp) };
}

function renderActivityHeatmap(model, t, status) {
  const months = model.weeks
    .map((week) => `<span class="overview-heatmap-month">${escapeHtml(week.monthLabel)}</span>`)
    .join("");
  const weekdays = HEATMAP_WEEKDAY_LABEL_KEYS
    .map((key) => `<span class="overview-heatmap-weekday">${key ? escapeHtml(t(key)) : ""}</span>`)
    .join("");
  const cells = model.weeks
    .flatMap((week) => week.days)
    .map((day) => {
      if (day.future) return `<span class="overview-heatmap-cell is-future" aria-hidden="true"></span>`;
      const label = day.count > 0
        ? t("activityDay", { date: day.date, count: day.count })
        : t("activityDayEmpty", { date: day.date });
      return `<span class="overview-heatmap-cell" data-level="${day.level}" role="img" tabindex="-1" title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}"></span>`;
    })
    .join("");
  const legend = Array.from({ length: HEATMAP_LEVELS + 1 }, (_, level) => `<span class="overview-heatmap-cell" data-level="${level}" aria-hidden="true"></span>`).join("");
  const caption = status === "error"
    ? t("activityUnavailable")
    : model.total > 0
      ? t("activityTotal", { count: model.total })
      : t("activityEmpty");

  return `<section class="overview-section overview-activity settings-card" data-overview-section="activity">
    <header class="overview-section-header"><div><h2>${escapeHtml(t("activity"))}</h2><p>${escapeHtml(caption)}</p></div></header>
    <div class="overview-heatmap" style="--overview-heatmap-weeks:${model.weeks.length}">
      <div class="overview-heatmap-months" aria-hidden="true">${months}</div>
      <div class="overview-heatmap-weekdays" aria-hidden="true">${weekdays}</div>
      <div class="overview-heatmap-cells" role="group" aria-label="${escapeHtml(t("activity"))}">${cells}</div>
    </div>
    <footer class="overview-heatmap-legend"><span>${escapeHtml(t("activityLess"))}</span>${legend}<span>${escapeHtml(t("activityMore"))}</span></footer>
  </section>`;
}

function replaceParams(template, params) {
  return boundedText(template, 1000).replace(/\{([a-zA-Z0-9_]+)\}/g, (_, name) => boundedText(params?.[name], 300));
}

function createText(options) {
  const translate = typeof options?.translate === "function" ? options.translate : null;
  const keyBuilder = typeof options?.key === "function"
    ? options.key
    : (name) => `${boundedText(options?.key, 80) || "overview"}.${name}`;
  return (name, params = {}) => {
    const fallback = replaceParams(DEFAULT_TEXT[name] || name, params);
    if (!translate) return fallback;
    try {
      const rawKey = keyBuilder(name);
      if (typeof rawKey !== "string") return fallback;
      const translationKey = boundedText(rawKey, 160);
      if (!translationKey) return fallback;
      const rawTranslation = translate(translationKey, params);
      if (typeof rawTranslation !== "string") return fallback;
      const translated = boundedText(rawTranslation, 1000);
      return translated && translated !== translationKey ? replaceParams(translated, params) : fallback;
    } catch {
      return fallback;
    }
  };
}

function createDateFormatter(options) {
  if (typeof options?.formatDateTime === "function") {
    return (value) => {
      if (!value) return "";
      try {
        return boundedText(options.formatDateTime(value), 200);
      } catch {
        return boundedText(value, 80);
      }
    };
  }
  return (value) => {
    if (!value) return "";
    const date = new Date(value);
    if (!Number.isFinite(date.getTime())) return boundedText(value, 80);
    try {
      return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
    } catch {
      return boundedText(value, 80);
    }
  };
}

function actionButton(action, label, {
  id = "",
  className = "overview-link",
  disabled = false,
  ariaLabel = "",
  controls = "",
} = {}) {
  const idAttribute = id ? ` data-overview-id="${escapeHtml(id)}"` : "";
  const ariaLabelAttribute = ariaLabel ? ` aria-label="${escapeHtml(ariaLabel)}"` : "";
  const controlsAttribute = controls ? ` aria-controls="${escapeHtml(controls)}"` : "";
  return `<button type="button" class="${escapeHtml(className)}" data-overview-action="${escapeHtml(action)}"${idAttribute}${ariaLabelAttribute}${controlsAttribute}${disabled ? " disabled aria-busy=\"true\"" : ""}>${escapeHtml(label)}</button>`;
}

function summaryCard(action, title, value, detail) {
  const ariaLabel = [title, value, detail].filter((part) => part !== "").join(". ");
  return `<button type="button" class="overview-summary-card settings-stat-card" data-overview-action="${escapeHtml(action)}" aria-label="${escapeHtml(ariaLabel)}"><span>${escapeHtml(title)}</span><strong>${escapeHtml(value)}</strong><small>${escapeHtml(detail)}</small></button>`;
}

function sectionHeader(title, hint, action, t) {
  const viewAll = action ? actionButton(action, t("viewAll"), { ariaLabel: t("viewSection", { section: title }) }) : "";
  return `<header class="overview-section-header"><div><h2>${escapeHtml(title)}</h2><p>${escapeHtml(hint)}</p></div>${viewAll}</header>`;
}

function emptyState(message) {
  return `<div class="overview-empty-state settings-empty-state">${escapeHtml(message)}</div>`;
}

function itemMeta(parts) {
  const values = parts.filter(Boolean).map((part) => escapeHtml(part));
  return values.length ? `<small>${values.join(" · ")}</small>` : "";
}

function renderConversation(item, t, formatDateTime) {
  const title = item.title || t("untitledConversation");
  const project = item.projectName || item.projectId || t("unknownProject");
  const updated = formatDateTime(item.updatedAt);
  return `<article class="overview-list-item"><div><strong>${escapeHtml(title)}</strong>${itemMeta([project, item.status || t("unknownStatus"), updated])}</div>${actionButton("open-conversation", t("continueWorking"), { id: item.id, disabled: !item.id, ariaLabel: `${t("openConversation")} — ${title}` })}</article>`;
}

function renderTask(item, t, formatDateTime) {
  const title = item.title || t("untitledTask");
  const owner = item.agentTitle || item.agentId;
  const project = item.projectName || item.projectId;
  const updated = formatDateTime(item.updatedAt);
  const priority = item.priority ? t("priority", { priority: item.priority }) : "";
  return `<article class="overview-list-item"><div><strong>${escapeHtml(title)}</strong>${itemMeta([item.status || t("unknownStatus"), priority, owner, project, updated])}</div>${actionButton("open-task", t("openTask"), { id: item.id, disabled: !item.id, ariaLabel: `${t("openTask")} — ${title}` })}</article>`;
}

function renderRun(item, t, formatDateTime) {
  const agent = item.agentTitle || item.agentId || t("unknownAgent");
  const started = formatDateTime(item.startedAt);
  return `<article class="overview-list-item"><div><strong>${escapeHtml(agent)}</strong>${itemMeta([item.status || t("unknownStatus"), started ? t("startedAt", { time: started }) : ""])}</div>${actionButton("open-run", t("openRun"), { id: item.id, disabled: !item.id, ariaLabel: `${t("openRun")} — ${agent}` })}</article>`;
}

function renderSchedule(item, t, formatDateTime) {
  const title = item.name || t("untitledSchedule");
  const agent = item.agentTitle || item.agentId || t("unknownAgent");
  const next = formatDateTime(item.nextRunAt);
  const nextLabel = next ? t("nextRunAt", { time: next }) : t("unknownTime");
  const outcome = item.lastOutcome ? t("lastOutcome", { outcome: item.lastOutcome }) : "";
  return `<article class="overview-list-item"><div><strong>${escapeHtml(title)}</strong>${itemMeta([agent, nextLabel, item.timezone, outcome])}</div>${actionButton("open-schedule", t("openSchedule"), { id: item.id, disabled: !item.id, ariaLabel: `${t("openSchedule")} — ${title}` })}</article>`;
}

function renderPending(payload, t) {
  const notices = [];
  if (payload.summary.pendingApprovals) {
    notices.push({ action: "approvals", title: t("pendingApprovals"), detail: t("pendingApprovalsCount", { count: payload.summary.pendingApprovals }), count: payload.summary.pendingApprovals });
  }
  if (payload.summary.schedules.due) {
    notices.push({ action: "schedules", title: t("dueSchedules"), detail: t("dueSchedulesCount", { count: payload.summary.schedules.due }), count: payload.summary.schedules.due });
  }
  if (payload.summary.schedules.failed) {
    notices.push({ action: "schedules", title: t("failedSchedules"), detail: t("failedSchedulesCount", { count: payload.summary.schedules.failed }), count: payload.summary.schedules.failed });
  }
  if (!notices.length) return emptyState(t("pendingEmpty"));
  return notices.map((notice) => `<button type="button" class="overview-pending-item settings-alert" data-overview-action="${escapeHtml(notice.action)}" aria-label="${escapeHtml(`${notice.title}：${notice.detail}`)}"><span><strong>${escapeHtml(notice.title)}</strong><small>${escapeHtml(notice.detail)}</small></span><b>${escapeHtml(notice.count)}</b></button>`).join("");
}

function hasDashboardData(payload) {
  return Boolean(
    payload.capturedAt
    || payload.summary.conversations
    || payload.summary.tasks.total
    || payload.summary.activeRuns
    || payload.summary.schedules.total
    || payload.recentConversations.length
    || payload.activeTasks.length
    || payload.activeRuns.length
    || payload.upcomingSchedules.length
  );
}

export function renderOverviewDashboard(payload, options = {}) {
  const normalized = normalizeOverviewPayload(payload);
  const t = createText(options);
  const formatDateTime = createDateFormatter(options);
  const status = ["idle", "loading", "ready", "error"].includes(options.status) ? options.status : "ready";
  const error = boundedText(options.error, 500);
  const captured = formatDateTime(normalized.capturedAt);
  const loading = status === "loading";
  const fullError = status === "error" && !(options.hasData ?? hasDashboardData(normalized));

  const header = `<header class="overview-dashboard-header settings-card-header"><div><h1 id="overviewDashboardTitle">${escapeHtml(t("title"))}</h1><p>${escapeHtml(t("subtitle"))}</p>${captured ? `<small>${escapeHtml(t("capturedAt", { time: captured }))}</small>` : ""}</div>${actionButton("refresh", t(loading ? "refreshing" : "refresh"), { className: "overview-refresh primary", disabled: loading, controls: "overviewDashboard" })}</header>`;
  const liveStatus = loading ? t("loading") : status === "ready" ? t("loaded") : "";
  const liveRegion = `<p class="overview-live-region sr-only" role="status" aria-live="polite" aria-atomic="true">${escapeHtml(liveStatus)}</p>`;
  if (loading && !(options.hasData ?? hasDashboardData(normalized))) {
    return `<div class="overview-dashboard settings-page" data-overview-state="loading" aria-busy="true">${header}${liveRegion}<div class="overview-dashboard-state settings-empty-state">${escapeHtml(t("loading"))}</div></div>`;
  }
  if (fullError) {
    return `<div class="overview-dashboard settings-page" data-overview-state="error" aria-busy="false">${header}${liveRegion}<div class="overview-dashboard-state settings-alert" role="alert"><strong>${escapeHtml(t("loadFailed"))}</strong><p>${escapeHtml(error || t("retryHint"))}</p></div></div>`;
  }

  const inlineError = status === "error" ? `<div class="overview-inline-error settings-alert" role="alert"><strong>${escapeHtml(t("loadFailed"))}</strong><span>${escapeHtml(error || t("retryHint"))}</span></div>` : "";
  const summaries = `<section class="overview-summary-grid settings-stat-grid" aria-label="${escapeHtml(t("title"))}">
    ${summaryCard("conversation", t("conversations"), normalized.summary.conversations, t("continueWorkingHint"))}
    ${summaryCard("tasks", t("tasks"), normalized.summary.tasks.total, t("taskBreakdown", normalized.summary.tasks))}
    ${summaryCard("runs", t("running"), normalized.summary.activeRuns, `${t("activeRuns")} · ${normalized.summary.runningAgents} ${t("runningAgents")}`)}
    ${summaryCard("schedules", t("schedules"), normalized.summary.schedules.total, t("scheduleBreakdown", normalized.summary.schedules))}
  </section>`;

  const heatmap = renderActivityHeatmap(
    buildActivityHeatmap(options.activityTrend, { today: options.today }),
    t,
    ["idle", "loading", "ready", "error"].includes(options.activityStatus) ? options.activityStatus : "ready",
  );

  const conversations = normalized.recentConversations.length
    ? normalized.recentConversations.map((item) => renderConversation(item, t, formatDateTime)).join("")
    : emptyState(t("recentEmpty"));
  const tasks = normalized.activeTasks.length
    ? normalized.activeTasks.map((item) => renderTask(item, t, formatDateTime)).join("")
    : emptyState(t("tasksEmpty"));
  const runs = normalized.activeRuns.length
    ? normalized.activeRuns.map((item) => renderRun(item, t, formatDateTime)).join("")
    : emptyState(t("runsEmpty"));
  const schedules = normalized.upcomingSchedules.length
    ? normalized.upcomingSchedules.map((item) => renderSchedule(item, t, formatDateTime)).join("")
    : emptyState(t("schedulesEmpty"));

  return `<div class="overview-dashboard settings-page" data-overview-state="${escapeHtml(status)}" aria-busy="${loading ? "true" : "false"}">
    ${header}
    ${liveRegion}
    ${inlineError}
    ${summaries}
    ${heatmap}
    <div class="overview-dashboard-grid">
      <section class="overview-section settings-card" data-overview-section="continue-working">${sectionHeader(t("continueWorking"), t("continueWorkingHint"), "conversation", t)}<div class="overview-list settings-data-list">${conversations}</div></section>
      <section class="overview-section settings-card" data-overview-section="in-progress">${sectionHeader(t("inProgress"), t("inProgressHint"), "tasks", t)}<div class="overview-progress-groups"><div><h3>${escapeHtml(t("activeTasks"))}</h3><div class="overview-list settings-data-list">${tasks}</div></div><div><h3>${escapeHtml(t("activeRuns"))}</h3><div class="overview-list settings-data-list">${runs}</div></div></div></section>
      <section class="overview-section settings-card" data-overview-section="upcoming">${sectionHeader(t("upcoming"), t("upcomingHint"), "schedules", t)}<div class="overview-list settings-data-list">${schedules}</div></section>
      <section class="overview-section settings-card" data-overview-section="pending">${sectionHeader(t("pending"), t("pendingHint"), "", t)}<div class="overview-pending-list">${renderPending(normalized, t)}</div></section>
    </div>
  </div>`;
}

function resolveHost(host) {
  if (typeof host === "function") {
    try {
      return host() || null;
    } catch {
      return null;
    }
  }
  if (typeof host === "string") return globalThis.document?.querySelector?.(host) || null;
  return host && typeof host === "object" ? host : null;
}

function actionElement(target, action, id = "") {
  const candidates = target?.querySelectorAll?.("[data-overview-action]") || [];
  return [...candidates].find((node) => {
    const nodeAction = boundedText(node.dataset?.overviewAction || node.getAttribute?.("data-overview-action"), 40);
    const nodeId = boundedText(node.dataset?.overviewId || node.getAttribute?.("data-overview-id"), 160);
    return nodeAction === action && nodeId === id;
  }) || null;
}

function focusWithoutScroll(element) {
  if (!element || element.disabled || typeof element.focus !== "function") return false;
  try {
    element.focus({ preventScroll: true });
  } catch {
    try {
      element.focus();
    } catch {
      return false;
    }
  }
  return true;
}

export function createOverviewDashboardController({ request, host, translate, formatDateTime, onNavigate, onError, today: todayOption } = {}) {
  if (typeof request !== "function") throw new TypeError("overview dashboard request must be a function");

  // Injectable so heatmap layout can be asserted against a fixed calendar.
  const today = () => {
    try {
      const resolved = typeof todayOption === "function" ? todayOption() : todayOption;
      return boundedText(resolved, 10) || localTodayISO();
    } catch {
      return localTodayISO();
    }
  };

  const state = {
    status: "idle",
    error: "",
    payload: normalizeOverviewPayload({}),
    hasData: false,
    sequence: 0,
    activityTrend: [],
    activityStatus: "idle",
    activitySequence: 0,
  };
  let inFlight = null;
  let pendingFocus = null;
  const boundHosts = new WeakSet();

  function getState() {
    return {
      status: state.status,
      error: state.error,
      payload: normalizeOverviewPayload(state.payload),
      hasData: state.hasData,
      activityStatus: state.activityStatus,
      activityTrend: state.activityTrend.slice(),
    };
  }

  // The heatmap window is widened by a day at each end: the server filters
  // created_at in UTC but buckets in local time, so the outermost local days are
  // only partly covered by an exactly-sized UTC range. buildActivityHeatmap
  // discards whatever falls outside the grid.
  function activityRequestPath(today) {
    const todayStamp = parseISODay(today) ?? parseISODay(localTodayISO());
    const from = formatISODay(todayStamp - (HEATMAP_WEEKS * 7) * DAY_MS);
    const to = formatISODay(todayStamp + DAY_MS);
    const query = new URLSearchParams({
      bucket: "day",
      tzOffset: String(localTimezoneOffsetMinutes()),
      from,
      to,
      // The paginated item list is not used here; 1 is the smallest accepted.
      limit: "1",
    });
    return `/api/usage/history?${query.toString()}`;
  }

  function loadActivity() {
    const sequence = ++state.activitySequence;
    state.activityStatus = "loading";
    return (async () => {
      try {
        const response = await request(activityRequestPath(today()));
        if (sequence !== state.activitySequence) return false;
        state.activityTrend = Array.isArray(response?.trend) ? response.trend : [];
        state.activityStatus = "ready";
      } catch {
        if (sequence !== state.activitySequence) return false;
        // A missing heatmap must not take the rest of the dashboard down.
        state.activityTrend = [];
        state.activityStatus = "error";
      }
      render();
      return state.activityStatus === "ready";
    })();
  }

  function reportNavigationError(error, action = "", id = "") {
    try {
      onError?.(error, action, id);
    } catch {
      // Error reporters are external; the dashboard must remain usable.
    }
  }

  function handleAction(action, id) {
    if (!ACTIONS.has(action)) return false;
    if (action === "refresh") {
      pendingFocus = { action, id: id || "" };
      void load({ force: true });
      return true;
    }
    try {
      const routeId = id || "";
      const result = onNavigate?.(action, routeId);
      if (result && typeof result.catch === "function") result.catch((error) => reportNavigationError(error, action, routeId));
    } catch (error) {
      reportNavigationError(error, action, id || "");
    }
    return true;
  }

  function bind(target = resolveHost(host)) {
    if (!target || typeof target.addEventListener !== "function" || boundHosts.has(target)) return false;
    target.addEventListener("click", (event) => {
      const trigger = event?.target?.closest?.("[data-overview-action]");
      if (!trigger || (typeof target.contains === "function" && !target.contains(trigger))) return;
      const action = boundedText(trigger.dataset?.overviewAction || trigger.getAttribute?.("data-overview-action"), 40);
      const id = boundedText(trigger.dataset?.overviewId || trigger.getAttribute?.("data-overview-id"), 160);
      if (!ACTIONS.has(action)) return;
      event.preventDefault?.();
      handleAction(action, id);
    });
    boundHosts.add(target);
    return true;
  }

  function render() {
    const html = renderOverviewDashboard(state.payload, {
      translate,
      formatDateTime,
      status: state.status,
      error: state.error,
      hasData: state.hasData,
      activityTrend: state.activityTrend,
      activityStatus: state.activityStatus,
      today: today(),
    });
    const target = resolveHost(host);
    if (target && "innerHTML" in target) {
      target.innerHTML = html;
      target.setAttribute?.("aria-busy", state.status === "loading" ? "true" : "false");
      bind(target);
      if (pendingFocus) {
        const focusTarget = actionElement(target, pendingFocus.action, pendingFocus.id);
        if (focusWithoutScroll(focusTarget)) pendingFocus = null;
      }
    }
    return html;
  }

  function load(options = {}) {
    const force = options === true || Boolean(options?.force);
    if (inFlight && !force) return inFlight;
    if (options && typeof options === "object" && options.preserveFocus) {
      pendingFocus = {
        action: boundedText(options.preserveFocus.action, 40),
        id: boundedText(options.preserveFocus.id, 160),
      };
    }

    const sequence = ++state.sequence;
    state.status = "loading";
    state.error = "";
    render();
    // Deliberately not awaited: the heatmap is supplementary and re-renders on
    // its own so a slow usage query never delays the rest of the dashboard.
    void loadActivity();

    const current = (async () => {
      try {
        const payload = await request("/api/overview");
        if (sequence !== state.sequence) return false;
        state.payload = normalizeOverviewPayload(payload);
        state.hasData = true;
        state.status = "ready";
        state.error = "";
        render();
        return true;
      } catch (error) {
        if (sequence !== state.sequence) return false;
        state.status = "error";
        state.error = boundedText(error?.message || error, 500) || "Request failed";
        render();
        return false;
      } finally {
        if (sequence === state.sequence) inFlight = null;
      }
    })();
    inFlight = current;
    return current;
  }

  bind();
  render();

  return {
    bind,
    getState,
    load,
    loadActivity,
    render,
  };
}
