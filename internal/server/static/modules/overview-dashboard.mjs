import { objectValue } from "./value-coercion.mjs";
import { escapeHtml as escapeSharedHtml } from "./dom.mjs";

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
  // List rows: each carries the entity id and opens that entity directly,
  // unlike the bare stat actions above which only switch surface.
  "open-conversation",
  "open-schedule",
]);

const LAUNCHER_ACTIONS = new Set([
  "suggestion",
  "submit",
  "choose-directory",
  "toggle-select",
  "select-option",
]);
// Provider-agnostic launcher list: the conversation picker narrows this to the
// levels the chosen model actually serves once an agent exists.
const REASONING_EFFORTS = Object.freeze(["auto", "low", "medium", "high", "xhigh", "max", "ultra"]);
const REASONING_EFFORT_SET = new Set(REASONING_EFFORTS);
// The launcher fields rendered as a custom popover rather than a bare native
// select. Keyed on the same names the popover markup carries.
const LAUNCHER_SELECT_FIELDS = new Set(["model", "reasoningEffort", "projectId"]);

const DEFAULT_TEXT = Object.freeze({
  title: "工作概览",
  subtitle: "查看近期统计与使用情况。",
  refresh: "刷新",
  refreshing: "正在刷新…",
  capturedAt: "更新于 {time}",
  loading: "正在加载首页…",
  loadFailed: "首页加载失败",
  retryHint: "请稍后重试，或使用刷新按钮重新加载。",
  loaded: "首页已更新。",
  conversations: "对话",
  tasks: "任务",
  running: "正在执行",
  schedules: "排程",
  conversationSummary: "最近对话与历史记录",
  taskBreakdown: "待办 {todo} · 进行中 {doing} · 已完成 {done}",
  scheduleBreakdown: "已启用 {enabled} / 共 {total}",
  activeRuns: "活跃运行",
  runningAgents: "运行代理",
  greetingMorning: "早上好，{name}",
  greetingAfternoon: "下午好，{name}",
  greetingEvening: "晚上好，{name}",
  greetingFallback: "今天想做什么？",
  launcherSubtitle: "描述任务或提出问题，Autoto 会立即开始。",
  promptPlaceholder: "描述任务或提出问题",
  project: "项目",
  model: "模型",
  reasoningEffort: "思考强度",
  reasoningAuto: "自动",
  reasoningLow: "低",
  reasoningMedium: "中",
  reasoningHigh: "高",
  chooseDirectory: "选择文件夹",
  noProjects: "暂无项目",
  send: "发送",
  starting: "正在启动…",
  projectRequired: "请先选择一个工作区项目。",
  suggestionWrite: "编写代码",
  suggestionFix: "修复问题",
  suggestionPlan: "规划任务",
  suggestionExplain: "解释代码",
  suggestionWritePrompt: "帮我编写代码来实现这个需求：",
  suggestionFixPrompt: "帮我定位并修复这个问题：",
  suggestionPlanPrompt: "帮我为这个目标制定实施计划：",
  suggestionExplainPrompt: "帮我解释这段代码的工作方式：",
  recentSection: "最近会话",
  recentEmpty: "还没有对话。在上方输入需求，开始第一个。",
  untitledConversation: "未命名会话",
  upcomingSection: "即将执行的排程",
  upcomingEmpty: "目前没有排定的执行。",
  scheduleNextRun: "下次执行 {time}",
  activity: "使用热力图",
  activityRecent: "近 7 天 {week} 次请求 · 近 30 天 {month} 次请求",
  activityTotal: "过去一年共 {count} 次模型请求",
  activityEmpty: "过去一年还没有使用记录。",
  activityUnavailable: "使用记录暂时无法加载。",
  activityDay: "{date}：{count} 次请求",
  activityDayTokens: "{date}：{count} 次请求 · {tokens} tokens",
  activityTotalTokens: "过去一年共 {count} 次模型请求 · {tokens} tokens",
  activityDayEmpty: "{date}：没有使用",
  activityLess: "少",
  activityMore: "多",
  activityMon: "周一",
  activityWed: "周三",
  activityFri: "周五",
  // Consumed by the injected resource-card renderer, which is handed this
  // module's translator so its keys resolve in the same overview.* namespace.
  systemMetrics: "系统资源",
  systemMetricsHint: "每 3 秒更新",
  cpu: "CPU",
  memory: "内存",
  networkDown: "下载",
  networkUp: "上传",
  toneOk: "正常",
  toneWarning: "有点压力",
  toneDanger: "有压力",
});

// A GitHub-style contribution grid: 53 columns of 7 weekdays, the last column
// holding today. Intensity comes from /api/usage/history?bucket=day, whose
// requestCount is the closest thing to "did I actually use the IDE that day".
const HEATMAP_WEEKS = 53;
const HEATMAP_LEVELS = 4;
const DAY_MS = 86_400_000;
// Only Mon/Wed/Fri are labelled; seven labels in that space become unreadable.
const HEATMAP_WEEKDAY_LABEL_KEYS = Object.freeze(["", "activityMon", "", "activityWed", "", "activityFri", ""]);

function boundedText(value, maximum = 200) {
  try {
    return String(value ?? "").replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "").slice(0, maximum).trim();
  } catch {
    return "";
  }
}

function boundedInput(value, maximum = 8_000) {
  try {
    return String(value ?? "").replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "").slice(0, maximum);
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

// Token totals over a year run far past the request-count ceiling, so they get
// their own bound instead of being silently clamped to 999,999,999.
function boundedTokens(value) {
  try {
    const number = Number(value);
    if (!Number.isFinite(number)) return 0;
    return Math.min(Number.MAX_SAFE_INTEGER, Math.max(0, Math.floor(number)));
  } catch {
    return 0;
  }
}

// The server derives totalTokens as input + output (reasoning and cached are
// reported separately and deliberately excluded), so the fallback matches that
// definition rather than inventing a different total.
function trendPointTokens(point) {
  const total = boundedTokens(point?.totalTokens);
  if (total > 0) return total;
  return boundedTokens(point?.inputTokens) + boundedTokens(point?.outputTokens);
}

// Thousands separators, with a plain fallback because this module is used in
// tests and environments where Intl may be unavailable.
function formatCount(value) {
  const number = boundedTokens(value);
  try {
    return new Intl.NumberFormat().format(number);
  } catch {
    return String(number);
  }
}

// The length cap is this module's own concern -- dashboard cards must not be
// stretched by one enormous field -- so it stays here and the escaping itself
// defers to the shared implementation.
function escapeHtml(value) {
  return escapeSharedHtml(boundedText(value, 4000));
}

// Injected renderers return markup, so it is length-bounded rather than escaped.
// The bound stops a malfunctioning renderer from producing an unbounded string.
function boundedHtml(value, maximum = 20_000) {
  return typeof value === "string" ? value.slice(0, maximum) : "";
}

function escapeInputHtml(value) {
  return boundedInput(value).replace(/[&<>"']/g, (character) => ({
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
    const previous = counts.get(day) || { count: 0, tokens: 0 };
    counts.set(day, {
      count: previous.count + boundedCount(point?.requestCount),
      tokens: previous.tokens + trendPointTokens(point),
    });
  }

  const columns = Math.max(1, Math.min(53, Math.floor(weeks) || HEATMAP_WEEKS));
  const todayStamp = parseISODay(today) ?? parseISODay(localTodayISO());
  // Pad to the end of today's week so the grid is rectangular and today sits in
  // the final column, then walk back a whole number of weeks.
  const endStamp = todayStamp + (6 - new Date(todayStamp).getUTCDay()) * DAY_MS;
  const startStamp = endStamp - (columns * 7 - 1) * DAY_MS;

  let total = 0;
  let totalTokens = 0;
  let max = 0;
  // Rolling 7- and 30-day request sums, both ending today, for the header's
  // recent-usage line. Windowed here because the caller only has the grid.
  let recentWeek = 0;
  let recentMonth = 0;
  for (const [day, entry] of counts) {
    const stamp = parseISODay(day);
    if (stamp === null || stamp < startStamp || stamp > todayStamp) continue;
    total += entry.count;
    totalTokens += entry.tokens;
    if (stamp > todayStamp - 7 * DAY_MS) recentWeek += entry.count;
    if (stamp > todayStamp - 30 * DAY_MS) recentMonth += entry.count;
    if (entry.count > max) max = entry.count;
  }

  const grid = [];
  let previousMonth = -1;
  for (let column = 0; column < columns; column += 1) {
    const days = [];
    for (let row = 0; row < 7; row += 1) {
      const stamp = startStamp + (column * 7 + row) * DAY_MS;
      const date = formatISODay(stamp);
      const future = stamp > todayStamp;
      const entry = future ? null : counts.get(date);
      const count = entry?.count || 0;
      const tokens = entry?.tokens || 0;
      // The colour scale still reads request count: tokens are additional detail
      // in the tooltip, not a change to what the grid's intensity means.
      days.push({ date, count, tokens, level: future ? 0 : heatmapLevel(count, max), future });
    }
    const columnMonth = new Date(startStamp + column * 7 * DAY_MS).getUTCMonth();
    // Label a column only where the month turns over, and never in the first
    // column, whose month name would sit half off the left edge.
    const label = column > 0 && columnMonth !== previousMonth ? shortMonthLabel(startStamp + column * 7 * DAY_MS) : "";
    previousMonth = columnMonth;
    grid.push({ days, monthLabel: label });
  }

  return { weeks: grid, total, totalTokens, max, recentWeek, recentMonth, start: formatISODay(startStamp), end: formatISODay(todayStamp) };
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
      // A day with requests but no recorded tokens keeps the shorter wording;
      // "0 tokens" would read as a fact rather than as missing data.
      const label = day.count > 0
        ? day.tokens > 0
          ? t("activityDayTokens", { date: day.date, count: day.count, tokens: formatCount(day.tokens) })
          : t("activityDay", { date: day.date, count: day.count })
        : t("activityDayEmpty", { date: day.date });
      return `<span class="overview-heatmap-cell" data-level="${day.level}" role="img" tabindex="-1" title="${escapeHtml(label)}" aria-label="${escapeHtml(label)}"></span>`;
    })
    .join("");
  const legend = Array.from({ length: HEATMAP_LEVELS + 1 }, (_, level) => `<span class="overview-heatmap-cell" data-level="${level}" aria-hidden="true"></span>`).join("");
  const caption = status === "error"
    ? t("activityUnavailable")
    : model.total > 0
      ? model.totalTokens > 0
        ? t("activityTotalTokens", { count: model.total, tokens: formatCount(model.totalTokens) })
        : t("activityTotal", { count: model.total })
      : t("activityEmpty");
  // The year total alone hides whether the recent pace changed; the rolling
  // windows answer that at a glance. Omitted while empty or failed, where the
  // caption already says everything there is to say.
  const recent = status !== "error" && model.total > 0
    ? `<p class="overview-heatmap-recent">${escapeHtml(t("activityRecent", { week: model.recentWeek, month: model.recentMonth }))}</p>`
    : "";

  return `<section class="overview-section overview-activity settings-card" data-overview-section="activity">
    <header class="overview-section-header"><div><h2>${escapeHtml(t("activity"))}</h2><p>${escapeHtml(caption)}</p>${recent}</div></header>
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

function normalizeLauncherContext(value = {}) {
  const source = objectValue(value);
  const projects = boundedList(source.projects, 200, (project) => {
    const item = objectValue(project);
    return {
      id: boundedText(item.id, 160),
      name: boundedText(item.name, 240),
      path: boundedText(item.path, 1000),
    };
  }).filter((project) => project.id);
  const models = boundedList(source.models, 200, (model) => {
    const item = objectValue(model);
    return {
      value: boundedText(item.value, 240),
      label: boundedText(item.label, 240),
      group: boundedText(item.group, 160),
    };
  }).filter((model) => model.value);
  let hourNumber = Number.NaN;
  try {
    hourNumber = Number(source.hour);
  } catch {
    hourNumber = Number.NaN;
  }
  const hour = Number.isFinite(hourNumber)
    ? Math.max(0, Math.min(23, Math.floor(hourNumber)))
    : new Date().getHours();
  return {
    displayName: boundedText(source.displayName, 160),
    projects,
    selectedProjectId: boundedText(source.selectedProjectId, 160),
    models,
    selectedModel: boundedText(source.selectedModel, 240),
    selectedEffort: REASONING_EFFORT_SET.has(boundedText(source.selectedEffort, 20).toLowerCase())
      ? boundedText(source.selectedEffort, 20).toLowerCase()
      : "auto",
    hour,
  };
}

function reconcileLauncherState(value = {}, context = normalizeLauncherContext()) {
  const source = objectValue(value);
  const projectIds = new Set(context.projects.map((project) => project.id));
  const modelValues = new Set(context.models.map((model) => model.value));
  const requestedProject = boundedText(source.projectId, 160);
  const requestedModel = boundedText(source.model, 240);
  const requestedEffort = boundedText(source.reasoningEffort, 20).toLowerCase();
  const defaultProject = projectIds.has(context.selectedProjectId) ? context.selectedProjectId : (context.projects[0]?.id || "");
  const defaultModel = modelValues.has(context.selectedModel) ? context.selectedModel : (context.models[0]?.value || "");
  return {
    draft: boundedInput(source.draft),
    projectId: projectIds.has(requestedProject) ? requestedProject : defaultProject,
    model: modelValues.has(requestedModel) ? requestedModel : defaultModel,
    reasoningEffort: REASONING_EFFORT_SET.has(requestedEffort) ? requestedEffort : context.selectedEffort,
    busy: source.busy === true,
    error: boundedText(source.error, 500),
  };
}

function launcherGreeting(context, t) {
  if (!context.displayName) return t("greetingFallback");
  const key = context.hour >= 5 && context.hour < 12
    ? "greetingMorning"
    : context.hour >= 12 && context.hour < 18
      ? "greetingAfternoon"
      : "greetingEvening";
  return t(key, { name: context.displayName });
}

function renderModelOptions(models, selected) {
  const groups = new Map();
  for (const model of models) {
    const key = model.group || "";
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(model);
  }
  return [...groups].map(([group, entries]) => {
    const options = entries.map((model) => `<option value="${escapeHtml(model.value)}"${model.value === selected ? " selected" : ""}>${escapeHtml(model.label || model.value)}</option>`).join("");
    return group ? `<optgroup label="${escapeHtml(group)}">${options}</optgroup>` : options;
  }).join("");
}

function renderLauncherSelectOption(name, value, label, selected, { model = false } = {}) {
  const optionLabel = escapeHtml(label || value);
  const copy = model
    ? `<span class="composer-model-option-copy"><span class="composer-model-option-name">${optionLabel}</span></span>`
    : `<span>${optionLabel}</span>`;
  return `<button type="button" class="composer-select-option${model ? " composer-model-option" : ""}" role="option" aria-selected="${selected ? "true" : "false"}" data-overview-launcher-action="select-option" data-overview-launcher-select="${escapeHtml(name)}" data-overview-launcher-value="${escapeHtml(value)}">${copy}<span class="composer-select-option-check" aria-hidden="true">${selected ? "✓" : ""}</span></button>`;
}

// One DOM id and one pill width per field. Derived from a table rather than a
// ternary because a third field (projectId) made the two-way branch silently
// hand out a duplicate id and the wrong pill width.
const LAUNCHER_SELECT_IDS = Object.freeze({
  model: "overviewLauncherModel",
  reasoningEffort: "overviewLauncherReasoningEffort",
  projectId: "overviewLauncherProject",
});
const LAUNCHER_SELECT_PILLS = Object.freeze({
  model: "model-pill",
  reasoningEffort: "effort-pill",
  projectId: "project-pill",
});

// Suggestion chips under the composer, Claude-style: icon plus a short verb.
// The names key into applySuggestion, which swaps the draft for the prompt.
const LAUNCHER_SUGGESTIONS = Object.freeze([
  { name: "write", labelKey: "suggestionWrite", icon: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17 3a2.83 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z"></path></svg>` },
  { name: "fix", labelKey: "suggestionFix", icon: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z"></path></svg>` },
  { name: "plan", labelKey: "suggestionPlan", icon: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"></path></svg>` },
  { name: "explain", labelKey: "suggestionExplain", icon: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 19.5A2.5 2.5 0 0 1 6.5 17H20"></path><path d="M6.5 2H20v20H6.5A2.5 2.5 0 0 1 4 19.5v-15A2.5 2.5 0 0 1 6.5 2z"></path></svg>` },
]);

function renderLauncherSelect({ name, label, options, selected, open = false, model = false, disabled = false }) {
  const id = LAUNCHER_SELECT_IDS[name] || `overviewLauncher${name}`;
  const pill = LAUNCHER_SELECT_PILLS[name] || "effort-pill";
  const selectedOption = options.find((option) => option.value === selected) || options[0] || { value: "", label: "" };
  const nativeOptions = model
    ? renderModelOptions(options, selected)
    : options.map((option) => `<option value="${escapeHtml(option.value)}"${option.value === selected ? " selected" : ""}>${escapeHtml(option.label || option.value)}</option>`).join("");
  let menuOptions = "";
  if (model) {
    const groups = new Map();
    for (const option of options) {
      const group = option.group || "";
      if (!groups.has(group)) groups.set(group, []);
      groups.get(group).push(option);
    }
    menuOptions = [...groups].map(([group, entries], index) => {
      const heading = `<div class="composer-model-group-heading${index > 0 ? " composer-model-group-start" : ""}" role="presentation">${escapeHtml(group || label)}</div>`;
      return `${heading}${entries.map((option) => renderLauncherSelectOption(name, option.value, option.label, option.value === selected, { model: true })).join("")}`;
    }).join("");
  } else {
    menuOptions = options.map((option) => renderLauncherSelectOption(name, option.value, option.label, option.value === selected)).join("");
  }
  const menu = open ? `<div class="composer-select-popover overview-launcher-select-popover${model ? " composer-model-popover" : ""}" data-overview-launcher-popover="${escapeHtml(name)}" role="listbox" aria-label="${escapeHtml(label)}">
    <div class="composer-select-popover-title">${escapeHtml(label)}</div>${menuOptions}
  </div>` : "";
  return `<div class="overview-launcher-field overview-launcher-custom-field">
    <label class="overview-launcher-label sr-only" for="${id}">${escapeHtml(label)}</label>
    <div class="overview-launcher-custom-select select-pill ${escapeHtml(pill)}">
      <select id="${id}" class="overview-launcher-select composer-native-select" data-overview-launcher-field="${escapeHtml(name)}"${disabled ? " disabled" : ""}>${nativeOptions}</select>
      <button type="button" class="overview-launcher-select-trigger composer-select-trigger" data-overview-launcher-action="toggle-select" data-overview-launcher-select="${escapeHtml(name)}" aria-haspopup="listbox" aria-expanded="${open ? "true" : "false"}" aria-label="${escapeHtml(`${label}：${selectedOption.label || selectedOption.value}`)}"${disabled ? " disabled" : ""}><span class="overview-launcher-select-value composer-select-value">${escapeHtml(selectedOption.label || selectedOption.value)}</span><span class="composer-select-chevron" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="m6 9 6 6 6-6"></path></svg></span></button>
      ${menu}
    </div>
  </div>`;
}

// The launcher field starts one row tall like the composer's, so it has to grow
// with the draft or a long prompt would be typed through a 40px slot. Same
// clamp rule as resizeMessageInputElement: rest at the CSS minimum while empty
// (measuring an empty textarea has been seen to report the maximum), grow to the
// maximum, then scroll internally. Implemented here rather than imported because
// this module takes every dependency by injection and pulling in the composer
// would give the home page the whole chat module graph for one measurement.
export function resizeLauncherInput(input, computedStyle) {
  // Sizing is presentation only, so anything that cannot be measured or styled
  // is left alone rather than throwing: this runs inside render(), and a failure
  // here would take the whole dashboard down with it.
  if (!input || !input.style) return { height: 0, scrollable: false };
  const style = computedStyle || globalThis.getComputedStyle?.(input);
  const pixels = (value, fallback) => {
    const parsed = Number.parseFloat(String(value || ""));
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
  };
  const minHeight = pixels(style?.minHeight, 40);
  const maxHeight = pixels(style?.maxHeight, 132);
  input.style.height = "auto";
  const empty = typeof input.value === "string" && input.value.length === 0;
  const content = empty ? minHeight : Math.max(minHeight, Number(input.scrollHeight) || 0);
  const height = Math.min(content, Math.max(minHeight, maxHeight));
  const scrollable = content > maxHeight;
  input.style.height = `${height}px`;
  input.style.overflowY = scrollable ? "auto" : "hidden";
  return { height, scrollable };
}

function renderLauncher(contextValue, stateValue, t, openSelect = "") {
  const context = normalizeLauncherContext(contextValue);
  const state = reconcileLauncherState(stateValue, context);
  // A native <select> was unreadable here: its dropdown panel is drawn by the
  // browser, no stylesheet in this app targets <option>, and the dark presets
  // set --ws-text near-white, so the inherited option colour landed white on the
  // platform's white panel. The custom popover the model and effort fields
  // already use is themed like the rest of the app, so project now shares it.
  const projectOptions = context.projects.length
    ? context.projects.map((project) => ({ value: project.id, label: project.name || project.path || project.id }))
    : [{ value: "", label: t("noProjects") }];
  const effortOptions = REASONING_EFFORTS.map((effort) => ({
    value: effort,
    label: t(`reasoning${effort[0].toUpperCase()}${effort.slice(1)}`),
  }));
  const launcherError = state.error ? `<p class="overview-launcher-error" role="alert">${escapeHtml(state.error)}</p>` : "";
  const hero = `<section class="overview-hero-root overview-launcher-hero">
    <div class="overview-hero-copy"><div class="overview-hero-heading"><span class="overview-hero-mark" aria-hidden="true"><svg viewBox="0 0 32 32"><circle cx="16" cy="16" r="12.5"></circle><path d="M10.5 17.5c1.6 2 3.4 3 5.5 3s3.9-1 5.5-3"></path><path d="M11.5 12.5h.01M20.5 12.5h.01"></path></svg></span><h1 class="overview-hero-title" id="overviewDashboardTitle">${escapeHtml(launcherGreeting(context, t))}</h1></div></div>
  </section>`;
  const suggestions = LAUNCHER_SUGGESTIONS
    .map((chip) => `<button type="button" class="overview-launcher-suggestion" data-overview-launcher-action="suggestion" data-overview-launcher-suggestion="${chip.name}"${state.busy ? " disabled" : ""}>${chip.icon}<span>${escapeHtml(t(chip.labelKey))}</span></button>`)
    .join("");
  // One rounded card holding the whole composer: the textarea on top and one
  // toolbar row along the card's bottom edge -- workspace controls on the left,
  // model, effort, and the round send button on the right. The suggestion chips
  // sit under the card as ready-made openers.
  const composer = `<section class="overview-launcher-root" data-overview-launcher>
    <div class="overview-launcher-form" data-overview-launcher-form>
      <div class="overview-launcher-card">
        <textarea class="overview-launcher-input" data-overview-launcher-field="draft" rows="1" maxlength="8000" aria-label="${escapeHtml(t("promptPlaceholder"))}" placeholder="${escapeHtml(t("promptPlaceholder"))}"${state.busy ? " disabled" : ""}>${escapeInputHtml(state.draft)}</textarea>
        <div class="overview-launcher-toolbar">
          <div class="overview-launcher-toolbar-group">
            ${renderLauncherSelect({
              name: "projectId",
              label: t("project"),
              options: projectOptions,
              selected: state.projectId,
              open: openSelect === "projectId",
              disabled: !context.projects.length,
            })}
            <button type="button" class="overview-launcher-directory" data-overview-launcher-action="choose-directory" title="${escapeHtml(t("chooseDirectory"))}" aria-label="${escapeHtml(t("chooseDirectory"))}"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13c0 1.1.9 2 2 2Z"></path></svg></button>
          </div>
          <div class="overview-launcher-toolbar-group">
            ${renderLauncherSelect({ name: "model", label: t("model"), options: context.models, selected: state.model, open: openSelect === "model", model: true, disabled: !context.models.length })}
            ${renderLauncherSelect({ name: "reasoningEffort", label: t("reasoningEffort"), options: effortOptions, selected: state.reasoningEffort, open: openSelect === "reasoningEffort" })}
            <button type="button" class="overview-launcher-send" data-overview-launcher-action="submit" aria-label="${escapeHtml(t(state.busy ? "starting" : "send"))}"${state.busy ? " disabled aria-busy=\"true\"" : ""}><span class="overview-launcher-send-label sr-only">${escapeHtml(t(state.busy ? "starting" : "send"))}</span><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 19V5"></path><path d="m5 12 7-7 7 7"></path></svg></button>
          </div>
        </div>
      </div>
      ${launcherError}
      <div class="overview-launcher-suggestions">${suggestions}</div>
    </div>
  </section>`;
  return { hero, composer };
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
  const status = ["idle", "loading", "ready", "error"].includes(options.status) ? options.status : "ready";
  const error = boundedText(options.error, 500);
  const loading = status === "loading";
  const fullError = status === "error" && !(options.hasData ?? hasDashboardData(normalized));

  const launcher = renderLauncher(options.launcherContext, options.launcherState, t, options.launcherOpenSelect);
  const liveStatus = loading ? t("loading") : status === "ready" ? t("loaded") : "";
  const liveRegion = `<p class="overview-live-region sr-only" role="status" aria-live="polite" aria-atomic="true">${escapeHtml(liveStatus)}</p>`;
  // The composer stays available in the degraded states too: typing a request
  // is the page's primary action and must not depend on the dashboard data
  // having loaded.
  if (loading && !(options.hasData ?? hasDashboardData(normalized))) {
    return `<div class="overview-dashboard settings-page" data-overview-state="loading" aria-busy="true"><div class="overview-home-center">${launcher.hero}${liveRegion}${launcher.composer}</div><div class="overview-dashboard-state settings-empty-state">${escapeHtml(t("loading"))}</div></div>`;
  }
  if (fullError) {
    return `<div class="overview-dashboard settings-page" data-overview-state="error" aria-busy="false"><div class="overview-home-center">${launcher.hero}${liveRegion}${launcher.composer}</div><div class="overview-dashboard-state settings-alert" role="alert"><strong>${escapeHtml(t("loadFailed"))}</strong><p>${escapeHtml(error || t("retryHint"))}</p></div></div>`;
  }

  const inlineError = status === "error" ? `<div class="overview-inline-error settings-alert" role="alert"><strong>${escapeHtml(t("loadFailed"))}</strong><span>${escapeHtml(error || t("retryHint"))}</span></div>` : "";
  const heatmap = renderActivityHeatmap(
    buildActivityHeatmap(options.activityTrend, { today: options.today }),
    t,
    ["idle", "loading", "ready", "error"].includes(options.activityStatus) ? options.activityStatus : "ready",
  );

  // Injected rather than imported: this module takes every dependency by
  // injection, and a host that does not pass a renderer simply gets no resource
  // cards. The renderer returns "" when nothing is measurable.
  let systemMetrics = "";
  if (typeof options.renderSystemMetrics === "function") {
    try {
      systemMetrics = boundedHtml(options.renderSystemMetrics(options.systemMetrics, t));
    } catch {
      // Resource cards are supplementary; a failure here must not take the
      // dashboard down.
      systemMetrics = "";
    }
  }

  // Claude-style home: greeting and composer form one centred group at the top
  // of the page, the heatmap follows as the only data section, and the injected
  // resource cards (when any) close the page.
  return `<div class="overview-dashboard settings-page" data-overview-state="${escapeHtml(status)}" aria-busy="${loading ? "true" : "false"}">
    <div class="overview-home-center">
      ${launcher.hero}
      ${liveRegion}
      ${inlineError}
      ${launcher.composer}
    </div>
    ${heatmap}
    ${systemMetrics}
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

export function createOverviewDashboardController({
  request,
  host,
  translate,
  formatDateTime,
  onNavigate,
  onError,
  today: todayOption,
  getLauncherContext,
  onLaunch,
  onChooseDirectory,
  renderSystemMetrics,
  createSystemMetricsPoller,
} = {}) {
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
    launcherContext: normalizeLauncherContext(),
    launcher: {
      draft: "",
      projectId: "",
      model: "",
      reasoningEffort: "",
      busy: false,
      error: "",
    },
    launcherOpenSelect: "",
    systemMetrics: null,
  };
  const t = createText({ translate });
  let inFlight = null;
  let pendingFocus = null;
  const boundHosts = new WeakSet();

  // The poller owns its own cadence, independent of the dashboard's load cycle:
  // resource utilisation is only meaningful when sampled repeatedly, while the
  // rest of the dashboard is a snapshot fetched on navigation.
  let metricsPoller = null;
  if (typeof createSystemMetricsPoller === "function") {
    try {
      metricsPoller = createSystemMetricsPoller({
        request,
        onUpdate: (value) => {
          state.systemMetrics = value;
          render();
        },
      });
    } catch (error) {
      reportExternalError(error, "system-metrics");
      metricsPoller = null;
    }
  }

  function refreshLauncherContext() {
    let context = {};
    if (typeof getLauncherContext === "function") {
      try {
        context = getLauncherContext() || {};
      } catch (error) {
        reportExternalError(error, "launcher-context");
      }
    }
    state.launcherContext = normalizeLauncherContext(context);
    state.launcher = reconcileLauncherState(state.launcher, state.launcherContext);
    return state.launcherContext;
  }

  function getState() {
    return {
      status: state.status,
      error: state.error,
      payload: normalizeOverviewPayload(state.payload),
      hasData: state.hasData,
      activityStatus: state.activityStatus,
      activityTrend: state.activityTrend.slice(),
      launcher: { ...state.launcher },
      systemMetrics: state.systemMetrics ? { ...state.systemMetrics } : null,
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

  function reportExternalError(error, action = "", id = "") {
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
      if (result && typeof result.catch === "function") result.catch((error) => reportExternalError(error, action, routeId));
    } catch (error) {
      reportExternalError(error, action, id || "");
    }
    return true;
  }

  function applySuggestion(name) {
    const promptKeys = {
      write: "suggestionWritePrompt",
      fix: "suggestionFixPrompt",
      plan: "suggestionPlanPrompt",
      explain: "suggestionExplainPrompt",
    };
    const key = promptKeys[name];
    if (!key) return false;
    state.launcher.draft = boundedInput(t(key));
    state.launcher.error = "";
    pendingFocus = { launcherField: "draft" };
    render();
    return true;
  }

  async function submitLauncher() {
    if (state.launcher.busy) return false;
    refreshLauncherContext();
    const text = state.launcher.draft.trim();
    if (!text) return false;
    if (!state.launcher.projectId) {
      state.launcher.error = t("projectRequired");
      render();
      return false;
    }
    const payload = {
      text,
      projectId: state.launcher.projectId,
      model: state.launcher.model,
      reasoningEffort: state.launcher.reasoningEffort,
    };
    state.launcher.busy = true;
    state.launcher.error = "";
    render();
    try {
      await onLaunch?.({ ...payload });
      state.launcher.draft = "";
      return true;
    } catch (error) {
      state.launcher.error = boundedText(error?.message || error, 500) || t("retryHint");
      reportExternalError(error, "launch");
      return false;
    } finally {
      state.launcher.busy = false;
      render();
    }
  }

  async function chooseDirectory() {
    try {
      await onChooseDirectory?.();
      render();
      return true;
    } catch (error) {
      reportExternalError(error, "choose-directory");
      return false;
    }
  }

  function setLauncherSelect(name, value) {
    refreshLauncherContext();
    if (name === "model" && state.launcherContext.models.some((model) => model.value === value)) state.launcher.model = value;
    else if (name === "reasoningEffort" && REASONING_EFFORT_SET.has(value)) state.launcher.reasoningEffort = value;
    // Same membership check the change handler applies, so a stale popover from
    // before a project list refresh cannot select a project that is now gone.
    else if (name === "projectId" && state.launcherContext.projects.some((project) => project.id === value)) state.launcher.projectId = value;
    else return false;
    state.launcherOpenSelect = "";
    state.launcher.error = "";
    render();
    return true;
  }

  function toggleLauncherSelect(name) {
    if (!LAUNCHER_SELECT_FIELDS.has(name)) return false;
    // An empty list would open a popover with nothing to pick.
    if (name === "model" && !state.launcherContext.models.length) return false;
    if (name === "projectId" && !state.launcherContext.projects.length) return false;
    state.launcherOpenSelect = state.launcherOpenSelect === name ? "" : name;
    render();
    return true;
  }

  function handleLauncherAction(trigger) {
    const action = boundedText(trigger?.dataset?.overviewLauncherAction || trigger?.getAttribute?.("data-overview-launcher-action"), 40);
    if (!LAUNCHER_ACTIONS.has(action)) return false;
    if (action === "suggestion") return applySuggestion(boundedText(trigger?.dataset?.overviewLauncherSuggestion || trigger?.getAttribute?.("data-overview-launcher-suggestion"), 20));
    if (action === "toggle-select") return toggleLauncherSelect(boundedText(trigger?.dataset?.overviewLauncherSelect || trigger?.getAttribute?.("data-overview-launcher-select"), 30));
    if (action === "select-option") return setLauncherSelect(
      boundedText(trigger?.dataset?.overviewLauncherSelect || trigger?.getAttribute?.("data-overview-launcher-select"), 30),
      boundedText(trigger?.dataset?.overviewLauncherValue || trigger?.getAttribute?.("data-overview-launcher-value"), 240),
    );
    if (action === "submit") {
      void submitLauncher();
      return true;
    }
    if (action === "choose-directory") {
      void chooseDirectory();
      return true;
    }
    return false;
  }

  function bind(target = resolveHost(host)) {
    if (!target || typeof target.addEventListener !== "function" || boundHosts.has(target)) return false;
    target.addEventListener("click", (event) => {
      const launcherTrigger = event?.target?.closest?.("[data-overview-launcher-action]");
      if (launcherTrigger && (typeof target.contains !== "function" || target.contains(launcherTrigger))) {
        event.preventDefault?.();
        handleLauncherAction(launcherTrigger);
        return;
      }
      const trigger = event?.target?.closest?.("[data-overview-action]");
      if (!trigger || (typeof target.contains === "function" && !target.contains(trigger))) {
        if (state.launcherOpenSelect) {
          state.launcherOpenSelect = "";
          render();
        }
        return;
      }
      const action = boundedText(trigger.dataset?.overviewAction || trigger.getAttribute?.("data-overview-action"), 40);
      const id = boundedText(trigger.dataset?.overviewId || trigger.getAttribute?.("data-overview-id"), 160);
      if (!ACTIONS.has(action)) return;
      state.launcherOpenSelect = "";
      event.preventDefault?.();
      handleAction(action, id);
    });
    target.addEventListener("input", (event) => {
      const field = event?.target?.closest?.("[data-overview-launcher-field]");
      if (!field || (typeof target.contains === "function" && !target.contains(field))) return;
      const name = boundedText(field.dataset?.overviewLauncherField || field.getAttribute?.("data-overview-launcher-field"), 30);
      if (name !== "draft") return;
      state.launcher.draft = boundedInput(field.value);
      // Typing does not re-render (that would move the caret), so the field is
      // resized in place on every keystroke.
      resizeLauncherInput(field);
    });
    target.addEventListener("change", (event) => {
      const field = event?.target?.closest?.("[data-overview-launcher-field]");
      if (!field || (typeof target.contains === "function" && !target.contains(field))) return;
      const name = boundedText(field.dataset?.overviewLauncherField || field.getAttribute?.("data-overview-launcher-field"), 30);
      const value = boundedText(field.value, 240);
      if (name === "projectId" && state.launcherContext.projects.some((project) => project.id === value)) state.launcher.projectId = value;
      else if (name === "model" && state.launcherContext.models.some((model) => model.value === value)) state.launcher.model = value;
      else if (name === "reasoningEffort" && REASONING_EFFORT_SET.has(value)) state.launcher.reasoningEffort = value;
      else return;
      state.launcherOpenSelect = "";
      state.launcher.error = "";
      render();
    });
    target.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && state.launcherOpenSelect) {
        state.launcherOpenSelect = "";
        event.preventDefault?.();
        render();
        return;
      }
      const field = event?.target?.closest?.("[data-overview-launcher-field=\"draft\"]");
      if (!field || (typeof target.contains === "function" && !target.contains(field))) return;
      if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
      event.preventDefault?.();
      state.launcher.draft = boundedInput(field.value);
      void submitLauncher();
    });
    boundHosts.add(target);
    return true;
  }

  function render() {
    refreshLauncherContext();
    const html = renderOverviewDashboard(state.payload, {
      translate,
      formatDateTime,
      status: state.status,
      error: state.error,
      hasData: state.hasData,
      activityTrend: state.activityTrend,
      activityStatus: state.activityStatus,
      today: today(),
      launcherContext: state.launcherContext,
      launcherState: state.launcher,
      launcherOpenSelect: state.launcherOpenSelect,
      renderSystemMetrics,
      systemMetrics: state.systemMetrics,
    });
    const target = resolveHost(host);
    if (target && "innerHTML" in target) {
      target.innerHTML = html;
      target.setAttribute?.("aria-busy", state.status === "loading" ? "true" : "false");
      bind(target);
      // innerHTML replaces the textarea, so the inline height from the last
      // keystroke is gone: a multi-line draft would snap back to one row on any
      // re-render (model change, load finishing) until the next keystroke.
      resizeLauncherInput(target.querySelector?.("[data-overview-launcher-field=\"draft\"]"));
      if (pendingFocus) {
        const focusTarget = pendingFocus.launcherField
          ? target.querySelector?.(`[data-overview-launcher-field="${pendingFocus.launcherField}"]`)
          : actionElement(target, pendingFocus.action, pendingFocus.id);
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

  // Polling only while the home page is on screen: away from it the cards are
  // not visible, so the requests would be pure waste.
  function start() {
    return Boolean(metricsPoller?.start());
  }

  function stop() {
    return Boolean(metricsPoller?.stop());
  }

  bind();
  render();

  return {
    bind,
    getState,
    load,
    loadActivity,
    render,
    start,
    stop,
  };
}
