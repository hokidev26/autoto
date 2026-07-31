import {
  automationLimits,
  buildSchedulePayload,
  normalizeSchedule,
  normalizeScheduleRun,
  schedulePresets,
} from "./automation-control.mjs";
import { escapeAttr, escapeHtml } from "./dom.mjs";
import { formatDuration, formatTimestamp as formatRegionalTimestamp } from "./formatters.mjs";
import { currentUILocale } from "./i18n.mjs";
import { t } from "./messages-automation.mjs";
import { confirm as platformConfirm } from "./platform.mjs";

const CONVERSATION_LIMIT = 200;
const MODES = new Set(["view", "create"]);
const WORKSPACE_TEXT = Object.freeze({
  "zh-CN": {
    title: "排程工作区", search: "搜索排程", searchPlaceholder: "名称、任务、表达式或关联对话", newSchedule: "新建排程",
    noResults: "没有匹配的排程。", selectSchedule: "请选择一个排程。", linkedConversation: "关联对话", missingConversation: "未找到关联对话",
    openConversation: "打开关联对话", editSchedule: "编辑排程", createSchedule: "创建排程", save: "保存更改", saving: "保存中…", saved: "排程已更新。", cancel: "取消",
    recentRuns: "最近运行历史", refreshHistory: "刷新历史", loadFailed: "排程加载失败", enabled: "已启用", disabled: "已停用", lastRun: "上次运行", lastOutcome: "上次结果", notAvailable: "—", unknownValue: "未知选项（{value}）",
    formDescription: "排程会在指定时间启动关联 Agent。请先确认任务可由现有工具完成。",
    frequencyHint: "选择预设会同步表达式；@every 15m 表示每 15 分钟运行一次，也可以填写 cron。",
    timezoneHint: "新建排程默认使用此浏览器的 IANA 时区；也可填写 UTC、Asia/Shanghai 等。",
    executionSettings: "运行设置", executionSettingsHint: "这些选项决定写入范围、工作区来源，以及每次运行是否创建新叙述者。",
    permissionReadOnly: "只读（推荐）", permissionAcceptEdits: "允许修改文件",
    environmentWorkline: "关联项目工作区", environmentStandalone: "独立工作区",
    narratorReuse: "复用关联叙述者", narratorNew: "每次运行新建叙述者",
    permissionHint: "允许修改文件不会自动批准命令、MCP 或其他执行型工具。",
    environmentHint: "决定任务使用关联项目还是独立工作区。", narratorHint: "复用模式可能在关联叙述者忙碌时跳过；新建模式每次创建独立子叙述者。",
    promptHint: "请写明可验证的输出，不要假设尚未配置的工具。",
    capabilityTitle: "排程能力范围",
    capabilityAvailable: "内置可用：定时启动 Agent、网页搜索与网页文本读取，以及权限允许的文件和命令操作。",
    capabilityUnavailable: "默认不可用：控制本机浏览器、点击或登录网页、播放音频或视频。",
    capabilityExtension: "只有另行配置浏览器自动化 MCP/插件并授予排程所需的执行权限后，浏览器交互才可能运行；媒体播放仍受自动播放、登录和地区策略限制。",
    capabilityExample: "可行改写：“搜索 YouTube，随机选择一个公开结果，返回歌曲名和链接，不要尝试播放。”",
  },
  "zh-TW": {
    title: "排程工作區", search: "搜尋排程", searchPlaceholder: "名稱、任務、表達式或關聯對話", newSchedule: "新增排程",
    noResults: "沒有符合的排程。", selectSchedule: "請選擇一個排程。", linkedConversation: "關聯對話", missingConversation: "找不到關聯對話",
    openConversation: "開啟關聯對話", editSchedule: "編輯排程", createSchedule: "建立排程", save: "儲存變更", saving: "儲存中…", saved: "排程已更新。", cancel: "取消",
    recentRuns: "最近執行歷史", refreshHistory: "重新整理歷史", loadFailed: "排程載入失敗", enabled: "已啟用", disabled: "已停用", lastRun: "上次執行", lastOutcome: "上次結果", notAvailable: "—", unknownValue: "未知選項（{value}）",
    formDescription: "排程會在指定時間啟動關聯 Agent。請先確認任務可由現有工具完成。",
    frequencyHint: "選擇預設會同步表達式；@every 15m 代表每 15 分鐘執行一次，也可以填寫 cron。",
    timezoneHint: "新排程預設使用此瀏覽器的 IANA 時區；也可填寫 UTC、Asia/Taipei 等。",
    executionSettings: "執行設定", executionSettingsHint: "這些選項決定寫入範圍、工作區來源，以及每次執行是否建立新敘述者。",
    permissionReadOnly: "唯讀（建議）", permissionAcceptEdits: "允許修改檔案",
    environmentWorkline: "關聯專案工作區", environmentStandalone: "獨立工作區",
    narratorReuse: "重用關聯敘述者", narratorNew: "每次執行新建敘述者",
    permissionHint: "允許修改檔案不會自動核准命令、MCP 或其他執行型工具。",
    environmentHint: "決定任務使用關聯專案或獨立工作區。", narratorHint: "重用模式可能在關聯敘述者忙碌時略過；新建模式每次建立獨立子敘述者。",
    promptHint: "請寫明可驗證的輸出，不要假設尚未配置的工具。",
    capabilityTitle: "排程能力範圍",
    capabilityAvailable: "內建可用：定時啟動 Agent、網頁搜尋與網頁文字讀取，以及權限允許的檔案和命令操作。",
    capabilityUnavailable: "預設不可用：控制本機瀏覽器、點擊或登入網頁、播放音訊或影片。",
    capabilityExtension: "只有另行配置瀏覽器自動化 MCP／外掛並授予排程所需的執行權限後，瀏覽器互動才可能執行；媒體播放仍受自動播放、登入和地區政策限制。",
    capabilityExample: "可行改寫：「搜尋 YouTube，隨機選擇一個公開結果，回傳歌曲名稱和連結，不要嘗試播放。」",
  },
  en: {
    title: "Schedule workspace", search: "Search schedules", searchPlaceholder: "Name, task, expression, or linked conversation", newSchedule: "New schedule",
    noResults: "No matching schedules.", selectSchedule: "Select a schedule.", linkedConversation: "Linked conversation", missingConversation: "Linked conversation unavailable",
    openConversation: "Open linked conversation", editSchedule: "Edit schedule", createSchedule: "Create schedule", save: "Save changes", saving: "Saving…", saved: "Schedule updated.", cancel: "Cancel",
    recentRuns: "Recent run history", refreshHistory: "Refresh history", loadFailed: "Failed to load schedules", enabled: "Enabled", disabled: "Disabled", lastRun: "Last run", lastOutcome: "Last result", notAvailable: "—", unknownValue: "Unknown option ({value})",
    formDescription: "A schedule starts the linked Agent at the configured time. Confirm that the current tools can complete the task.",
    frequencyHint: "Choosing a preset updates the expression. @every 15m runs every 15 minutes; cron expressions are also supported.",
    timezoneHint: "New schedules use this browser's IANA timezone by default. You can also enter UTC, America/Los_Angeles, and similar values.",
    executionSettings: "Run settings", executionSettingsHint: "These options control write access, workspace source, and whether each run creates a new narrator.",
    permissionReadOnly: "Read only (recommended)", permissionAcceptEdits: "Allow file edits",
    environmentWorkline: "Linked project workspace", environmentStandalone: "Standalone workspace",
    narratorReuse: "Reuse linked narrator", narratorNew: "Create a narrator for each run",
    permissionHint: "Allowing file edits does not automatically approve commands, MCP, or other executable tools.",
    environmentHint: "Choose whether the task uses the linked project or a standalone workspace.", narratorHint: "Reuse can skip while the linked narrator is busy; new mode creates an independent child narrator for each run.",
    promptHint: "Describe a verifiable result and do not assume tools that have not been configured.",
    capabilityTitle: "Schedule capability boundary",
    capabilityAvailable: "Built in: start an Agent on a timer, search the web, fetch public page text, and use file or command tools allowed by policy.",
    capabilityUnavailable: "Not built in: control the local browser, click or sign in to pages, or play audio or video.",
    capabilityExtension: "Browser interaction is possible only after a browser-automation MCP/plugin is configured and the schedule has the required execution permission; media playback still depends on autoplay, sign-in, and regional policies.",
    capabilityExample: "Workable rewrite: “Search YouTube, choose one public result at random, and return the song title and link without trying to play it.”",
  },
});

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function safeRead(value, key) {
  try {
    return value?.[key];
  } catch {
    return undefined;
  }
}

function boundedText(value, limit = 240) {
  try {
    return String(value ?? "").replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, "").trim().slice(0, limit);
  } catch {
    return "";
  }
}

function text(name, params = {}) {
  const catalog = WORKSPACE_TEXT[currentUILocale()] || WORKSPACE_TEXT.en;
  return boundedText(catalog[name] || name, 1000).replace(/\{([A-Za-z0-9_]+)\}/g, (_, key) => boundedText(params[key], 300));
}

export function defaultScheduleTimezone(resolveOptions = () => new Intl.DateTimeFormat().resolvedOptions()) {
  try {
    const options = typeof resolveOptions === "function" ? resolveOptions() : resolveOptions;
    const timezone = boundedText(safeRead(options, "timeZone"), 128);
    return timezone && /^[A-Za-z0-9._+/-]+$/.test(timezone) ? timezone : "UTC";
  } catch {
    return "UTC";
  }
}

function normalizeOneSchedule(value) {
  try {
    return normalizeSchedule(value);
  } catch {
    return normalizeSchedule({});
  }
}

function listResult(value, keys, limit) {
  if (Array.isArray(value)) return value.slice(0, limit);
  const source = objectValue(value);
  for (const key of keys) {
    const candidate = safeRead(source, key);
    if (Array.isArray(candidate)) return candidate.slice(0, limit);
  }
  const data = safeRead(source, "data");
  return data && data !== value ? listResult(data, keys, limit) : [];
}

function safeTimestamp(value, options = {}) {
  const formatter = typeof options.formatTimestamp === "function" ? options.formatTimestamp : formatRegionalTimestamp;
  try {
    return boundedText(formatter(value, {
      emptyFallback: t("automation.timestamp.empty"),
      invalidFallback: t("automation.timestamp.invalid"),
    }), 240);
  } catch {
    return boundedText(value, 80) || t("automation.timestamp.empty");
  }
}

function conversationLabel(conversation) {
  if (!conversation) return "";
  return [conversation.title || conversation.agentId, conversation.projectName].filter(Boolean).join(" · ");
}

function conversationMap(value) {
  return new Map(normalizeScheduleConversations(value).map((item) => [item.agentId, item]));
}

function normalizedSchedules(value) {
  return listResult(value, ["schedules", "items"], automationLimits.schedules).map(normalizeOneSchedule);
}

function errorMessage(error) {
  return boundedText(error?.message || error || t("automation.validation.requestFailed"), 1200);
}

function scheduleFromResult(result, fallback = {}) {
  const source = objectValue(result);
  const candidate = safeRead(source, "schedule") ?? safeRead(source, "item") ?? safeRead(source, "data") ?? source;
  const normalized = normalizeOneSchedule({ ...objectValue(fallback), ...objectValue(candidate) });
  return normalized.id ? normalized : null;
}

function cloneHistory(history) {
  return Object.fromEntries(Object.entries(history).map(([id, entry]) => [id, {
    loaded: Boolean(entry.loaded),
    loading: Boolean(entry.loading),
    error: boundedText(entry.error, 1200),
    runs: (Array.isArray(entry.runs) ? entry.runs : []).slice(0, automationLimits.scheduleRuns).map((run) => ({ ...normalizeScheduleRun(run) })),
  }]));
}

export function normalizeScheduleConversations(value) {
  const input = Array.isArray(value) ? value : listResult(value, ["conversations", "items"], CONVERSATION_LIMIT);
  const result = [];
  const seen = new Set();
  for (let index = 0; index < input.length && result.length < CONVERSATION_LIMIT; index += 1) {
    const source = objectValue(input[index]);
    const agentId = boundedText(safeRead(source, "agentId") ?? safeRead(source, "id"), 160);
    if (!agentId || seen.has(agentId)) continue;
    seen.add(agentId);
    result.push({
      agentId,
      title: boundedText(safeRead(source, "agentTitle") ?? safeRead(source, "title") ?? safeRead(source, "worklineTitle") ?? agentId, 240) || agentId,
      projectName: boundedText(safeRead(source, "projectName") ?? safeRead(source, "projectTitle"), 200),
      targetId: boundedText(safeRead(source, "targetId"), 600),
    });
  }
  return result;
}

export function filterScheduleWorkspaceItems(schedules, conversations, query) {
  const items = normalizedSchedules(schedules);
  const needle = boundedText(query, 300).toLocaleLowerCase();
  if (!needle) return items;
  const byAgent = conversationMap(conversations);
  return items.filter((schedule) => {
    const conversation = byAgent.get(schedule.agentId);
    return [
      schedule.name,
      schedule.prompt,
      schedule.expression,
      schedule.agentId,
      conversation?.title,
      conversation?.projectName,
    ].some((value) => boundedText(value, 8000).toLocaleLowerCase().includes(needle));
  });
}

export function renderScheduleNavigationHTML(state = {}, options = {}) {
  const source = objectValue(state);
  const conversations = normalizeScheduleConversations(options.conversations);
  const byAgent = new Map(conversations.map((item) => [item.agentId, item]));
  const schedules = filterScheduleWorkspaceItems(source.schedules, conversations, source.query);
  const selectedId = boundedText(source.selectedScheduleId, 160);
  const loading = Boolean(source.loading) && !Boolean(source.loaded);
  const error = boundedText(source.error || safeRead(source.errors, "load"), 1200);
  let content = "";
  if (loading) {
    content = `<div class="schedule-workspace-state settings-empty-state settings-skeleton" aria-busy="true">${escapeHtml(t("automation.section.loading"))}</div>`;
  } else if (error && !normalizedSchedules(source.schedules).length) {
    content = `<div class="schedule-workspace-state settings-alert" role="alert"><strong>${escapeHtml(text("loadFailed"))}</strong><span>${escapeHtml(error)}</span></div>`;
  } else if (!schedules.length) {
    content = `<div class="schedule-workspace-state settings-empty-state">${escapeHtml(source.query ? text("noResults") : t("automation.schedule.empty"))}</div>`;
  } else {
    content = schedules.map((schedule) => {
      const linked = byAgent.get(schedule.agentId);
      const active = schedule.id && schedule.id === selectedId;
      const status = schedule.enabled ? text("enabled") : text("disabled");
      return `<button class="schedule-navigation-item settings-data-list-row${active ? " active" : ""}${schedule.enabled ? " enabled" : " disabled"}" type="button" data-schedule-navigation="${escapeAttr(schedule.id)}" aria-pressed="${active ? "true" : "false"}">
        <span class="schedule-navigation-heading"><strong>${escapeHtml(schedule.name || t("automation.defaults.unnamedSchedule"))}</strong><em>${escapeHtml(status)}</em></span>
        <small>${escapeHtml(t("automation.schedule.nextRun"))}: ${escapeHtml(safeTimestamp(schedule.nextRunAt, options))}</small>
        <small>${escapeHtml(text("linkedConversation"))}: ${escapeHtml(conversationLabel(linked) || schedule.agentId || text("missingConversation"))}</small>
      </button>`;
    }).join("");
  }
  return `<div class="schedule-workspace-navigation" aria-label="${escapeAttr(text("title"))}">
    <span class="sr-only">${escapeHtml(text("title"))}</span>
    ${error && normalizedSchedules(source.schedules).length ? `<div class="settings-alert" role="alert">${escapeHtml(error)}</div>` : ""}
    <div class="schedule-navigation-list settings-data-list">${content}</div>
  </div>`;
}

const SCHEDULE_OPTION_TEXT = Object.freeze({
  permissionMode: Object.freeze({ readOnly: "permissionReadOnly", acceptEdits: "permissionAcceptEdits" }),
  environmentMode: Object.freeze({ workline: "environmentWorkline", standalone: "environmentStandalone" }),
  narratorMode: Object.freeze({ reuse: "narratorReuse", new: "narratorNew" }),
});

export function formatScheduleOptionValue(group, value) {
  const normalized = boundedText(value, 80);
  const key = safeRead(safeRead(SCHEDULE_OPTION_TEXT, group), normalized);
  return key ? text(key) : text("unknownValue", { value: normalized || text("notAvailable") });
}

function selectOptions(values, selected, group = "") {
  return values.map((value) => `<option value="${escapeAttr(value)}"${value === selected ? " selected" : ""}>${escapeHtml(group ? formatScheduleOptionValue(group, value) : boundedText(value, 160))}</option>`).join("");
}

// Presets exist so nobody has to know cron. Showing the raw expression as the
// option label defeated that; the expression still lands in the field below,
// where it stays editable and visible.
function presetLabel(expression) {
  const label = t(`automation.schedule.presetLabels.${expression}`);
  return label && !label.includes("automation.schedule") ? label : expression;
}

function presetOptions(selected) {
  return schedulePresets
    .map((value) => `<option value="${escapeAttr(value)}"${value === selected ? " selected" : ""}>${escapeHtml(presetLabel(value))}</option>`)
    .join("");
}

function conversationOptions(conversations, selectedAgentId) {
  const items = normalizeScheduleConversations(conversations);
  if (selectedAgentId && !items.some((item) => item.agentId === selectedAgentId)) {
    items.unshift({ agentId: selectedAgentId, title: selectedAgentId, projectName: "", targetId: "" });
  }
  return items.map((item) => `<option value="${escapeAttr(item.agentId)}"${item.agentId === selectedAgentId ? " selected" : ""}>${escapeHtml(conversationLabel(item) || item.agentId)}</option>`).join("");
}

function renderScheduleForm(schedule, conversations, { create = false, busy = false } = {}) {
  const item = normalizeOneSchedule(schedule);
  const title = create ? text("createSchedule") : text("editSchedule");
  const submitLabel = busy ? text("saving") : create ? t("automation.buttons.createSchedule") : text("save");
  const helpPrefix = create ? "schedule-create" : "schedule-edit";
  return `<form class="schedule-workspace-form automation-form settings-form-grid" data-schedule-form data-schedule-form-mode="${create ? "create" : "edit"}"${item.id ? ` data-schedule-id="${escapeAttr(item.id)}"` : ""}>
    <div class="settings-card-header span-2"><div><h3>${escapeHtml(title)}</h3><p>${escapeHtml(text("formDescription"))}</p></div></div>
    <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.name"))}</span><input name="name" maxlength="120" value="${escapeAttr(item.name)}" placeholder="${escapeAttr(t("automation.schedule.namePlaceholder"))}" /></label>
    <label><span class="schedule-field-label">${escapeHtml(text("linkedConversation"))}</span><select name="agentId" required>${conversationOptions(conversations, item.agentId)}</select></label>
    <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.preset"))}</span><select name="preset" data-schedule-preset aria-describedby="${helpPrefix}-frequency-help"><option value="">${escapeHtml(t("automation.schedule.custom"))}</option>${presetOptions(schedulePresets.includes(item.expression) ? item.expression : "")}</select></label>
    <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.expression"))}</span><input name="expression" maxlength="256" value="${escapeAttr(item.expression)}" placeholder="${escapeAttr(t("automation.schedule.expressionPlaceholder"))}" aria-describedby="${helpPrefix}-frequency-help" required /><small id="${helpPrefix}-frequency-help" class="schedule-field-help">${escapeHtml(text("frequencyHint"))}</small></label>
    <fieldset class="schedule-execution-options span-2">
      <legend>${escapeHtml(text("executionSettings"))}</legend>
      <p>${escapeHtml(text("executionSettingsHint"))}</p>
      <div class="schedule-execution-grid">
        <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.timezone"))}</span><input name="timezone" maxlength="128" value="${escapeAttr(item.timezone || "UTC")}" placeholder="${escapeAttr(t("automation.schedule.timezonePlaceholder"))}" aria-describedby="${helpPrefix}-timezone-help" required /><small id="${helpPrefix}-timezone-help" class="schedule-field-help">${escapeHtml(text("timezoneHint"))}</small></label>
        <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.permission"))}</span><select name="permissionMode" aria-describedby="${helpPrefix}-permission-help">${selectOptions(["readOnly", "acceptEdits"], item.permissionMode, "permissionMode")}</select><small id="${helpPrefix}-permission-help" class="schedule-field-help">${escapeHtml(text("permissionHint"))}</small></label>
        <label data-schedule-environment${item.narratorMode === "reuse" ? " hidden" : ""}><span class="schedule-field-label">${escapeHtml(t("automation.schedule.environment"))}</span><select name="environmentMode" aria-describedby="${helpPrefix}-environment-help">${selectOptions(["workline", "standalone"], item.environmentMode, "environmentMode")}</select><small id="${helpPrefix}-environment-help" class="schedule-field-help">${escapeHtml(text("environmentHint"))}</small></label>
        <label><span class="schedule-field-label">${escapeHtml(t("automation.schedule.narrator"))}</span><select name="narratorMode" aria-describedby="${helpPrefix}-narrator-help">${selectOptions(["reuse", "new"], item.narratorMode, "narratorMode")}</select><small id="${helpPrefix}-narrator-help" class="schedule-field-help">${escapeHtml(text("narratorHint"))}</small></label>
      </div>
    </fieldset>
    <label class="span-2"><span class="schedule-field-label">${escapeHtml(t("automation.schedule.prompt"))}</span><textarea name="prompt" rows="6" maxlength="8000" placeholder="${escapeAttr(t("automation.schedule.promptPlaceholder"))}" aria-describedby="${helpPrefix}-prompt-help ${helpPrefix}-capability" required>${escapeHtml(item.prompt)}</textarea><small id="${helpPrefix}-prompt-help" class="schedule-field-help">${escapeHtml(text("promptHint"))}</small></label>
    <aside id="${helpPrefix}-capability" class="schedule-capability-note span-2" role="note" data-schedule-capability-boundary="external-browser-tool-required">
      <strong>${escapeHtml(text("capabilityTitle"))}</strong>
      <p class="schedule-capability-supported">${escapeHtml(text("capabilityAvailable"))}</p>
      <p class="schedule-capability-limited">${escapeHtml(text("capabilityUnavailable"))}</p>
      <p>${escapeHtml(text("capabilityExtension"))}</p>
      <p class="schedule-capability-example">${escapeHtml(text("capabilityExample"))}</p>
    </aside>
    <div class="automation-form-actions settings-inline-actions span-2"><button class="automation-btn primary" type="submit"${busy ? " disabled aria-busy=\"true\"" : ""}>${escapeHtml(submitLabel)}</button>${create ? `<button class="automation-btn subtle" type="button" data-schedule-cancel>${escapeHtml(text("cancel"))}</button>` : ""}</div>
  </form>`;
}

function renderHistory(scheduleId, history, options) {
  const entry = objectValue(history);
  if (entry.loading) return `<div class="settings-empty-state settings-skeleton" aria-busy="true">${escapeHtml(t("automation.section.loadingHistory"))}</div>`;
  if (entry.error) return `<div class="settings-alert" role="alert">${escapeHtml(entry.error)}</div>`;
  if (!entry.loaded) return `<div class="settings-empty-state">${escapeHtml(t("automation.section.selectHistory"))}</div>`;
  const runs = (Array.isArray(entry.runs) ? entry.runs : []).slice(0, automationLimits.scheduleRuns).map(normalizeScheduleRun);
  if (!runs.length) return `<div class="settings-empty-state">${escapeHtml(t("automation.section.noHistory"))}</div>`;
  return `<div class="schedule-run-list settings-data-list">${runs.map((run) => `<article class="schedule-run-item settings-data-list-row" data-schedule-run-id="${escapeAttr(run.id)}"><div><strong>${escapeHtml(run.status)}</strong><small>${escapeHtml(run.triggerType)}</small></div><span>${escapeHtml(safeTimestamp(run.createdAt, options))}</span><span>${escapeHtml(formatDuration(run.durationMs))}</span></article>`).join("")}</div>`;
}

export function renderScheduleWorkspace(state = {}, options = {}) {
  const source = objectValue(state);
  const schedules = normalizedSchedules(source.schedules);
  const conversations = normalizeScheduleConversations(options.conversations);
  const mode = MODES.has(source.mode) ? source.mode : "view";
  const selectedId = boundedText(source.selectedScheduleId, 160);
  const selected = schedules.find((item) => item.id === selectedId) || null;
  const loading = Boolean(source.loading) && !Boolean(source.loaded);
  const error = boundedText(source.error || safeRead(source.errors, "load"), 1200);
  const busy = objectValue(source.busy);
  let workspaceState = "detail";
  let content = "";
  if (loading) {
    workspaceState = "loading";
    content = `<div class="schedule-workspace-state settings-empty-state settings-skeleton" aria-busy="true">${escapeHtml(t("automation.section.loading"))}</div>`;
  } else if (error && !schedules.length && mode !== "create") {
    workspaceState = "error";
    content = `<div class="schedule-workspace-state settings-alert" role="alert"><strong>${escapeHtml(text("loadFailed"))}</strong><span>${escapeHtml(error)}</span></div>`;
  } else if (mode === "create") {
    workspaceState = "create";
    const defaultAgentId = boundedText(options.activeAgentId, 160) || conversations[0]?.agentId || "";
    content = renderScheduleForm({ agentId: defaultAgentId, timezone: defaultScheduleTimezone(options.resolveTimezone), permissionMode: "readOnly", environmentMode: "workline", narratorMode: "reuse", enabled: true }, conversations, { create: true, busy: Boolean(busy.save) });
  } else if (!schedules.length) {
    workspaceState = "empty";
    content = `<div class="schedule-workspace-state settings-empty-state"><p>${escapeHtml(t("automation.schedule.empty"))}</p><button class="automation-btn primary" type="button" data-schedule-create>${escapeHtml(text("newSchedule"))}</button></div>`;
  } else if (!selected) {
    workspaceState = "empty";
    content = `<div class="schedule-workspace-state settings-empty-state">${escapeHtml(text("selectSchedule"))}</div>`;
  } else {
    const linked = conversations.find((item) => item.agentId === selected.agentId);
    const itemBusy = Boolean(busy[`schedule:${selected.id}`]);
    const history = safeRead(source.history, selected.id) || {};
    content = `<article class="schedule-workspace-detail">
      <header class="schedule-detail-header settings-card-header"><div><span>${escapeHtml(selected.enabled ? text("enabled") : text("disabled"))}</span><h2>${escapeHtml(selected.name || t("automation.defaults.unnamedSchedule"))}</h2><p>${escapeHtml(text("linkedConversation"))}: ${escapeHtml(conversationLabel(linked) || selected.agentId || text("missingConversation"))}</p></div>
        <div class="automation-actions settings-inline-actions"><button class="automation-btn subtle" type="button" data-schedule-toggle="${escapeAttr(selected.id)}" data-enabled="${selected.enabled ? "true" : "false"}"${itemBusy ? " disabled" : ""}>${escapeHtml(t(selected.enabled ? "automation.buttons.disable" : "automation.buttons.enable"))}</button><button class="automation-btn primary" type="button" data-schedule-run="${escapeAttr(selected.id)}"${itemBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.runNow"))}</button><button class="automation-btn danger destructive" type="button" data-schedule-delete="${escapeAttr(selected.id)}"${itemBusy ? " disabled" : ""}>${escapeHtml(t("automation.buttons.delete"))}</button>${selected.agentId ? `<button class="automation-btn subtle" type="button" data-schedule-open-conversation="${escapeAttr(selected.agentId)}">${escapeHtml(text("openConversation"))}</button>` : ""}</div>
      </header>
      ${selected.lastError ? `<div class="settings-alert" role="alert">${escapeHtml(selected.lastError)}</div>` : ""}
      <dl class="automation-kv"><div><dt>${escapeHtml(t("automation.schedule.nextRun"))}</dt><dd>${escapeHtml(safeTimestamp(selected.nextRunAt, options))}</dd></div><div><dt>${escapeHtml(text("lastRun"))}</dt><dd>${escapeHtml(safeTimestamp(selected.lastRunAt, options))}</dd></div><div><dt>${escapeHtml(text("lastOutcome"))}</dt><dd>${escapeHtml(selected.lastOutcome || text("notAvailable"))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.expression"))}</dt><dd>${escapeHtml(selected.expression)}</dd></div><div><dt>${escapeHtml(t("automation.schedule.timezone"))}</dt><dd>${escapeHtml(selected.timezone)}</dd></div><div><dt>${escapeHtml(t("automation.schedule.permission"))}</dt><dd>${escapeHtml(formatScheduleOptionValue("permissionMode", selected.permissionMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.environment"))}</dt><dd>${escapeHtml(formatScheduleOptionValue("environmentMode", selected.environmentMode))}</dd></div><div><dt>${escapeHtml(t("automation.schedule.narrator"))}</dt><dd>${escapeHtml(formatScheduleOptionValue("narratorMode", selected.narratorMode))}</dd></div></dl>
      ${renderScheduleForm(selected, conversations, { busy: Boolean(busy.save) })}
      <section class="schedule-history settings-card"><header class="settings-card-header"><h3>${escapeHtml(text("recentRuns"))}</h3><button class="automation-btn subtle" type="button" data-schedule-history="${escapeAttr(selected.id)}"${history.loading ? " disabled" : ""}>${escapeHtml(text("refreshHistory"))}</button></header>${renderHistory(selected.id, history, options)}</section>
    </article>`;
  }
  const saveError = boundedText(safeRead(source.errors, "save") || safeRead(source.errors, "action"), 1200);
  return `<section class="schedule-workspace settings-page-section" data-schedule-workspace="${escapeAttr(selectedId)}" data-schedule-workspace-state="${workspaceState}" aria-busy="${Boolean(source.loading) || Boolean(busy.save) ? "true" : "false"}">${saveError ? `<div class="settings-alert" role="alert">${escapeHtml(saveError)}</div>` : ""}${content}</section>`;
}

export function createScheduleWorkspaceController({
  request,
  onChange,
  showError,
  showToast,
  confirmAction,
  formatTimestamp,
  now = () => Date.now(),
} = {}) {
  if (typeof request !== "function") throw new TypeError("Schedule workspace request must be a function");
  const confirm = confirmAction || platformConfirm;
  const state = {
    loaded: false,
    loading: false,
    error: "",
    schedules: [],
    selectedScheduleId: "",
    mode: "view",
    query: "",
    busy: {},
    errors: {},
    history: {},
    loadSeq: 0,
  };
  const historyRequests = new Map();
  const boundRoots = new WeakSet();

  function currentTime() {
    try {
      return typeof now === "function" ? now() : Date.now();
    } catch {
      return Date.now();
    }
  }

  function invalidateLoad() {
    state.loadSeq += 1;
    state.loading = false;
  }

  function getState() {
    return {
      loaded: state.loaded,
      loading: state.loading,
      error: state.error,
      schedules: state.schedules.map((item) => ({ ...item })),
      selectedScheduleId: state.selectedScheduleId,
      mode: state.mode,
      query: state.query,
      busy: { ...state.busy },
      errors: { ...state.errors },
      history: cloneHistory(state.history),
      loadSeq: state.loadSeq,
      now: currentTime(),
    };
  }

  function emit() {
    try {
      onChange?.(getState());
    } catch {
      // Rendering callbacks are external; controller state remains authoritative.
    }
  }

  function report(section, error) {
    const message = errorMessage(error);
    state.errors[section] = message;
    if (section === "load") state.error = message;
    try {
      showError?.(error instanceof Error ? error : new Error(message));
    } catch {
      // Error reporters must not break workspace actions.
    }
  }

  function clearError(section) {
    delete state.errors[section];
    if (section === "load") state.error = "";
  }

  function toast(message) {
    try {
      showToast?.(message, "success", { force: true });
    } catch {
      // Toasts are optional presentation details.
    }
  }

  function selectAfterLoad(preferredId = "") {
    if (preferredId && state.schedules.some((item) => item.id === preferredId)) state.selectedScheduleId = preferredId;
    else if (!state.schedules.some((item) => item.id === state.selectedScheduleId)) state.selectedScheduleId = state.schedules[0]?.id || "";
  }

  async function load(options = {}) {
    const seq = ++state.loadSeq;
    state.loading = true;
    clearError("load");
    emit();
    try {
      const result = await request("/api/schedules");
      if (seq !== state.loadSeq) return false;
      state.schedules = normalizedSchedules(result);
      state.loaded = true;
      state.loading = false;
      selectAfterLoad(boundedText(options.preferredId, 160));
      emit();
      if (options.autoHistory !== false && state.mode === "view" && state.selectedScheduleId) void loadHistory(state.selectedScheduleId);
      return true;
    } catch (error) {
      if (seq !== state.loadSeq) return false;
      state.loading = false;
      state.loaded = true;
      report("load", error);
      emit();
      return false;
    }
  }

  async function select(id, options = {}) {
    const scheduleId = boundedText(id, 160);
    if (!state.schedules.some((item) => item.id === scheduleId)) return false;
    state.selectedScheduleId = scheduleId;
    state.mode = "view";
    emit();
    if (options.loadHistory === false) return true;
    const entry = state.history[scheduleId];
    if (entry?.loaded) return true;
    return loadHistory(scheduleId);
  }

  function startCreate() {
    state.mode = "create";
    state.selectedScheduleId = "";
    clearError("save");
    emit();
    return true;
  }

  function setQuery(query) {
    state.query = boundedText(query, 300);
    emit();
    return state.query;
  }

  function upsertSchedule(schedule) {
    state.schedules = [schedule, ...state.schedules.filter((item) => item.id !== schedule.id)].slice(0, automationLimits.schedules);
  }

  async function save(input = {}) {
    if (state.busy.save) return false;
    const editingId = state.mode === "create" ? "" : boundedText(input.id || state.selectedScheduleId, 160);
    const existing = state.schedules.find((item) => item.id === editingId);
    let payload;
    try {
      payload = buildSchedulePayload({ ...input, enabled: editingId ? existing?.enabled ?? true : input.enabled });
    } catch (error) {
      report("save", error);
      emit();
      return false;
    }
    invalidateLoad();
    state.busy.save = true;
    clearError("save");
    emit();
    try {
      const path = editingId ? `/api/schedules/${encodeURIComponent(editingId)}` : "/api/schedules";
      const result = await request(path, { method: editingId ? "PATCH" : "POST", body: JSON.stringify(payload) });
      const returned = scheduleFromResult(result, { ...payload, id: editingId });
      if (returned) {
        upsertSchedule(returned);
        state.selectedScheduleId = returned.id;
      } else {
        await load({ autoHistory: false, preferredId: editingId });
        const match = state.schedules.find((item) => editingId ? item.id === editingId : item.name === payload.name && item.agentId === payload.agentId && item.expression === payload.expression);
        state.selectedScheduleId = match?.id || state.selectedScheduleId;
      }
      state.mode = "view";
      toast(editingId ? text("saved") : t("automation.toast.scheduleCreated"));
      if (state.selectedScheduleId) void loadHistory(state.selectedScheduleId, { force: true });
      return true;
    } catch (error) {
      report("save", error);
      return false;
    } finally {
      delete state.busy.save;
      emit();
    }
  }

  async function toggle(id, enabled) {
    const scheduleId = boundedText(id || state.selectedScheduleId, 160);
    const schedule = state.schedules.find((item) => item.id === scheduleId);
    if (!schedule || state.busy[`schedule:${scheduleId}`]) return false;
    const nextEnabled = enabled === undefined ? !schedule.enabled : Boolean(enabled);
    invalidateLoad();
    state.busy[`schedule:${scheduleId}`] = true;
    clearError("action");
    emit();
    try {
      const result = await request(`/api/schedules/${encodeURIComponent(scheduleId)}`, { method: "PATCH", body: JSON.stringify({ enabled: nextEnabled }) });
      const returned = scheduleFromResult(result, { ...schedule, enabled: nextEnabled, status: nextEnabled ? "enabled" : "disabled" });
      upsertSchedule(returned || normalizeOneSchedule({ ...schedule, enabled: nextEnabled, status: nextEnabled ? "enabled" : "disabled" }));
      state.selectedScheduleId = scheduleId;
      toast(t(nextEnabled ? "automation.toast.scheduleEnabled" : "automation.toast.scheduleDisabled"));
      return true;
    } catch (error) {
      report("action", error);
      return false;
    } finally {
      delete state.busy[`schedule:${scheduleId}`];
      emit();
    }
  }

  async function run(id) {
    const scheduleId = boundedText(id || state.selectedScheduleId, 160);
    if (!scheduleId || state.busy[`schedule:${scheduleId}`]) return false;
    state.busy[`schedule:${scheduleId}`] = true;
    clearError("action");
    emit();
    try {
      await request(`/api/schedules/${encodeURIComponent(scheduleId)}/run`, { method: "POST" });
      toast(t("automation.toast.scheduleRunRequested"));
      void loadHistory(scheduleId, { force: true });
      return true;
    } catch (error) {
      report("action", error);
      return false;
    } finally {
      delete state.busy[`schedule:${scheduleId}`];
      emit();
    }
  }

  async function deleteSchedule(id) {
    const scheduleId = boundedText(id || state.selectedScheduleId, 160);
    if (!scheduleId || state.busy[`schedule:${scheduleId}`]) return false;
    if (!await confirm(t("automation.confirm.deleteSchedule"))) return false;
    invalidateLoad();
    state.busy[`schedule:${scheduleId}`] = true;
    clearError("action");
    emit();
    try {
      await request(`/api/schedules/${encodeURIComponent(scheduleId)}`, { method: "DELETE" });
      state.schedules = state.schedules.filter((item) => item.id !== scheduleId);
      delete state.history[scheduleId];
      historyRequests.delete(scheduleId);
      state.selectedScheduleId = state.schedules[0]?.id || "";
      state.mode = "view";
      toast(t("automation.toast.scheduleDeleted"));
      if (state.selectedScheduleId) void loadHistory(state.selectedScheduleId);
      return true;
    } catch (error) {
      report("action", error);
      return false;
    } finally {
      delete state.busy[`schedule:${scheduleId}`];
      emit();
    }
  }

  function loadHistory(id, options = {}) {
    const scheduleId = boundedText(id || state.selectedScheduleId, 160);
    if (!scheduleId) return Promise.resolve(false);
    if (historyRequests.has(scheduleId)) return historyRequests.get(scheduleId);
    if (state.history[scheduleId]?.loaded && !options.force) return Promise.resolve(true);
    state.history[scheduleId] = { ...(state.history[scheduleId] || {}), loaded: false, loading: true, error: "", runs: state.history[scheduleId]?.runs || [] };
    emit();
    const current = (async () => {
      try {
        const result = await request(`/api/schedules/${encodeURIComponent(scheduleId)}/runs?limit=${automationLimits.scheduleRuns}`);
        state.history[scheduleId] = {
          loaded: true,
          loading: false,
          error: "",
          runs: listResult(result, ["runs", "items"], automationLimits.scheduleRuns).map(normalizeScheduleRun),
        };
        return true;
      } catch (error) {
        state.history[scheduleId] = { loaded: true, loading: false, error: errorMessage(error), runs: [] };
        try {
          showError?.(error);
        } catch {
          // Optional reporter.
        }
        return false;
      } finally {
        historyRequests.delete(scheduleId);
        emit();
      }
    })();
    historyRequests.set(scheduleId, current);
    return current;
  }

  function renderNavigation(options = {}) {
    return renderScheduleNavigationHTML(getState(), { ...options, formatTimestamp: options.formatTimestamp || formatTimestamp });
  }

  function render(options = {}) {
    return renderScheduleWorkspace(getState(), { ...options, formatTimestamp: options.formatTimestamp || formatTimestamp });
  }

  function fieldValue(form, name) {
    try {
      const control = form?.elements?.namedItem?.(name) || form?.querySelector?.(`[name="${name}"]`);
      return control?.value ?? "";
    } catch {
      return "";
    }
  }

  function bind(root, options = {}) {
    if (!root || typeof root.addEventListener !== "function" || boundRoots.has(root)) return false;
    root.addEventListener("click", (event) => {
      const trigger = event?.target?.closest?.("[data-schedule-navigation],[data-schedule-create],[data-schedule-cancel],[data-schedule-toggle],[data-schedule-run],[data-schedule-delete],[data-schedule-history],[data-schedule-open-conversation]");
      if (!trigger || (typeof root.contains === "function" && !root.contains(trigger))) return;
      event.preventDefault?.();
      if (trigger.hasAttribute?.("data-schedule-create") || trigger.dataset?.scheduleCreate !== undefined) startCreate();
      else if (trigger.hasAttribute?.("data-schedule-cancel") || trigger.dataset?.scheduleCancel !== undefined) {
        state.mode = "view";
        state.selectedScheduleId = state.schedules[0]?.id || "";
        emit();
      } else if (trigger.dataset?.scheduleNavigation !== undefined) void select(trigger.dataset.scheduleNavigation);
      else if (trigger.dataset?.scheduleToggle !== undefined) void toggle(trigger.dataset.scheduleToggle, trigger.dataset.enabled !== "true");
      else if (trigger.dataset?.scheduleRun !== undefined) void run(trigger.dataset.scheduleRun);
      else if (trigger.dataset?.scheduleDelete !== undefined) void deleteSchedule(trigger.dataset.scheduleDelete);
      else if (trigger.dataset?.scheduleHistory !== undefined) void loadHistory(trigger.dataset.scheduleHistory, { force: true });
      else if (trigger.dataset?.scheduleOpenConversation !== undefined) {
        try {
          options.onOpenConversation?.(boundedText(trigger.dataset.scheduleOpenConversation, 160));
        } catch (error) {
          try { showError?.(error); } catch { /* Optional reporter. */ }
        }
      }
    });
    root.addEventListener("input", (event) => {
      if (event?.target?.matches?.("[data-schedule-query]")) setQuery(event.target.value);
    });
    root.addEventListener("change", (event) => {
      const target = event?.target;
      if (target?.matches?.("[data-schedule-preset]")) {
        if (!target.value) return;
        const form = target.closest?.("[data-schedule-form]");
        const expression = form?.elements?.namedItem?.("expression") || form?.querySelector?.('[name="expression"]');
        if (expression) expression.value = target.value;
        return;
      }
      // Reusing an existing narrator runs wherever that agent already lives, so
      // the environment is not a choice there -- offering it only invites a
      // combination the dispatcher has to reconcile.
      if (target?.matches?.('[name="narratorMode"]')) {
        const form = target.closest?.("[data-schedule-form]");
        const environment = form?.querySelector?.("[data-schedule-environment]");
        if (environment) environment.hidden = target.value === "reuse";
      }
    });
    root.addEventListener("submit", (event) => {
      const form = event?.target?.matches?.("[data-schedule-form]") ? event.target : event?.target?.closest?.("[data-schedule-form]");
      if (!form) return;
      event.preventDefault?.();
      void save({
        id: boundedText(form.dataset?.scheduleId, 160),
        name: fieldValue(form, "name"),
        agentId: fieldValue(form, "agentId"),
        expression: fieldValue(form, "expression"),
        timezone: fieldValue(form, "timezone"),
        permissionMode: fieldValue(form, "permissionMode"),
        environmentMode: fieldValue(form, "environmentMode"),
        narratorMode: fieldValue(form, "narratorMode"),
        prompt: fieldValue(form, "prompt"),
      });
    });
    boundRoots.add(root);
    return true;
  }

  return {
    bind,
    delete: deleteSchedule,
    getState,
    load,
    loadHistory,
    render,
    renderNavigation,
    run,
    save,
    select,
    setQuery,
    startCreate,
    toggle,
  };
}
