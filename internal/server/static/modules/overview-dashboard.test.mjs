import test from "node:test";
import assert from "node:assert/strict";

import {
  OVERVIEW_LAUNCHER_VISIBLE,
  buildActivityHeatmap,
  createOverviewDashboardController,
  normalizeOverviewPayload,
  overviewRailTarget,
  renderOverviewDashboard,
  resolveOverviewStartup,
} from "./overview-dashboard.mjs";

function overview(overrides = {}) {
  return {
    capturedAt: "2026-07-19T12:00:00Z",
    summary: {
      conversations: 12,
      runningAgents: 2,
      tasks: { total: 9, todo: 3, doing: 2, done: 4 },
      activeRuns: 2,
      pendingApprovals: 1,
      schedules: { total: 5, enabled: 4, due: 1, failed: 1 },
    },
    recentConversations: [{ id: "conversation-1", title: "Release plan", status: "active", projectId: "project-1", projectName: "Autoto", updatedAt: "2026-07-19T11:00:00Z" }],
    activeTasks: [{ id: "task-1", title: "Finish dashboard", status: "doing", priority: "high", agentId: "agent-1", agentTitle: "Frontend", projectId: "project-1", projectName: "Autoto", updatedAt: "2026-07-19T10:00:00Z" }],
    activeRuns: [{ id: "run-1", agentId: "agent-2", agentTitle: "Tester", status: "running", startedAt: "2026-07-19T09:00:00Z" }],
    upcomingSchedules: [{ id: "schedule-1", name: "Nightly tests", agentId: "agent-2", agentTitle: "Tester", nextRunAt: "2026-07-20T00:00:00Z", timezone: "UTC", lastOutcome: "success" }],
    ...overrides,
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function fakeHost() {
  const listeners = new Map();
  const attributes = new Map();
  const focusLog = [];
  const listenerCounts = new Map();
  let html = "";
  const node = (dataset = {}, value = "") => ({
    dataset,
    value,
    closest(selector) {
      if (selector === "[data-overview-action]") return dataset.overviewAction ? this : null;
      if (selector === "[data-overview-launcher-action]") return dataset.overviewLauncherAction ? this : null;
      if (selector === "[data-overview-launcher-field]") return dataset.overviewLauncherField ? this : null;
      if (selector === "[data-overview-launcher-field=\"draft\"]") return dataset.overviewLauncherField === "draft" ? this : null;
      return null;
    },
  });
  return {
    get innerHTML() { return html; },
    set innerHTML(value) { html = String(value); },
    attributes,
    focusLog,
    listenerCounts,
    addEventListener(type, listener) {
      listeners.set(type, listener);
      listenerCounts.set(type, (listenerCounts.get(type) || 0) + 1);
    },
    contains() { return true; },
    setAttribute(name, value) { attributes.set(name, String(value)); },
    querySelector(selector) {
      const field = selector.match(/^\[data-overview-launcher-field="([^"]+)"\]$/)?.[1];
      if (!field) return null;
      return { disabled: false, focus() { focusLog.push(["field", field]); } };
    },
    querySelectorAll(selector) {
      if (selector !== "[data-overview-action]") return [];
      return [...html.matchAll(/<button\b([^>]*data-overview-action="([^"]+)"[^>]*)>/g)].map((match) => {
        const id = match[1].match(/data-overview-id="([^"]*)"/)?.[1] || "";
        return {
          dataset: { overviewAction: match[2], overviewId: id },
          disabled: /\sdisabled(?:\s|$)/.test(match[1]),
          focus() { focusLog.push([match[2], id]); },
        };
      });
    },
    click(action, id = "") {
      listeners.get("click")?.({ target: node({ overviewAction: action, overviewId: id }), preventDefault() {} });
    },
    launcherClick(action, extra = {}) {
      listeners.get("click")?.({
        target: node({ overviewLauncherAction: action, ...extra }),
        preventDefault() {},
      });
    },
    input(value) {
      listeners.get("input")?.({ target: node({ overviewLauncherField: "draft" }, value) });
    },
    change(field, value) {
      listeners.get("change")?.({ target: node({ overviewLauncherField: field }, value) });
    },
    focusout(target, relatedTarget = null) {
      listeners.get("focusout")?.({ target, relatedTarget });
    },
    keydown(value, { key = "Enter", shiftKey = false, isComposing = false } = {}) {
      let prevented = false;
      listeners.get("keydown")?.({
        target: node({ overviewLauncherField: "draft" }, value),
        key,
        shiftKey,
        isComposing,
        preventDefault() { prevented = true; },
      });
      return prevented;
    },
  };
}

test("shell state helpers keep workbench inside the conversation rail and honor task deep links", () => {
  assert.equal(overviewRailTarget({ overviewActive: true, activeWorkbench: "workbench" }), "home");
  assert.equal(overviewRailTarget({ overviewActive: false, activeWorkbench: "conversation" }), "conversation");
  assert.equal(overviewRailTarget({ overviewActive: false, activeWorkbench: "workbench" }), "conversation");
  assert.equal(overviewRailTarget({ overviewActive: false, activeWorkbench: "schedules" }), "schedules");

  assert.deepEqual(resolveOverviewStartup({ hasConversation: true, hasProject: true }), {
    overviewActive: true,
    workbench: "conversation",
    restoreConversation: true,
    selectFallbackProject: false,
  });
  assert.deepEqual(resolveOverviewStartup({ requestedView: "details", hasConversation: true, hasProject: true }), {
    overviewActive: false,
    workbench: "conversation",
    restoreConversation: true,
    selectFallbackProject: false,
  });
  assert.deepEqual(resolveOverviewStartup({ requestedView: "terminal", hasProject: true }), {
    overviewActive: false,
    workbench: "conversation",
    restoreConversation: false,
    selectFallbackProject: true,
  });
  assert.deepEqual(resolveOverviewStartup({ requestedView: "tasks", hasConversation: true }), {
    overviewActive: false,
    workbench: "workbench",
    restoreConversation: true,
    selectFallbackProject: false,
  });
  assert.deepEqual(resolveOverviewStartup({ requestedView: "schedules", hasConversation: true, hasProject: true }), {
    overviewActive: false,
    workbench: "schedules",
    restoreConversation: true,
    selectFallbackProject: false,
  });
  assert.equal(resolveOverviewStartup({ requestedView: "settings", hasConversation: true }).overviewActive, true);
  assert.deepEqual(resolveOverviewStartup({ mobile: true, hasConversation: true, hasProject: true }), {
    overviewActive: false,
    workbench: "conversation",
    restoreConversation: true,
    selectFallbackProject: false,
  });
  assert.deepEqual(resolveOverviewStartup({ mobile: true, hasProject: true }), {
    overviewActive: false,
    workbench: "conversation",
    restoreConversation: false,
    selectFallbackProject: true,
  });
  assert.equal(resolveOverviewStartup({ mobile: true }).overviewActive, false);
});

test("normalization supplies complete defaults, bounds values, and drops unknown fields", () => {
  const veryLong = "x".repeat(1000);
  const normalized = normalizeOverviewPayload({
    capturedAt: veryLong,
    summary: {
      conversations: -4,
      runningAgents: "3.9",
      tasks: { total: "7", todo: Infinity, doing: Symbol("bad"), done: 2 },
      activeRuns: 2n,
      pendingApprovals: "bad",
      schedules: { total: 1e20, enabled: -1, due: "2", failed: 3 },
      secretToken: "must-not-survive",
    },
    recentConversations: [{ id: veryLong, title: null, status: 42, password: "hidden" }, null],
    activeTasks: "not-an-array",
    activeRuns: [{ id: "run", credential: "hidden" }],
    upcomingSchedules: [{}],
    rawDatabaseDump: "hidden",
  });

  assert.equal(normalized.capturedAt.length, 80);
  assert.equal(normalized.summary.conversations, 0);
  assert.equal(normalized.summary.runningAgents, 3);
  assert.deepEqual(normalized.summary.tasks, { total: 7, todo: 0, doing: 0, done: 2 });
  assert.equal(normalized.summary.activeRuns, 2);
  assert.equal(normalized.summary.schedules.total, 999_999_999);
  assert.equal(normalized.summary.schedules.enabled, 0);
  assert.equal(normalized.recentConversations[0].id.length, 160);
  assert.equal(normalized.recentConversations[0].status, "42");
  assert.deepEqual(normalized.activeTasks, []);
  assert.equal("password" in normalized.recentConversations[0], false);
  assert.equal("credential" in normalized.activeRuns[0], false);
  assert.equal("rawDatabaseDump" in normalized, false);
  assert.deepEqual(normalizeOverviewPayload(null).summary.tasks, { total: 0, todo: 0, doing: 0, done: 0 });
});

test("render escapes visible launcher context and state", () => {
  const attack = '\"><img src=x onerror="boom">';
  const html = renderOverviewDashboard(overview({ capturedAt: attack }), {
    launcherContext: {
      displayName: attack,
      hour: 9,
      projects: [{ id: attack, name: "<script>alert(1)</script>", path: attack }],
      selectedProjectId: attack,
      models: [{ value: attack, label: "<svg onload=boom>", group: attack }],
      selectedModel: attack,
      selectedEffort: "high",
    },
    launcherState: { draft: "<textarea autofocus onfocus=boom>", error: attack },
    showLauncher: true,
  });

  assert.doesNotMatch(html, /<script>|<img src=x|<svg onload|<textarea autofocus/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /&lt;svg onload=boom&gt;/);
  assert.match(html, /&lt;textarea autofocus onfocus=boom&gt;/);
  assert.match(html, /value="&quot;&gt;&lt;img/);
});

test("home board escapes conversation, task, and schedule titles", () => {
  const html = renderOverviewDashboard(overview({
    recentConversations: [{ id: "c1", title: "<script>x</script>", status: "idle", projectId: "p", projectName: "<b>P</b>", updatedAt: "t" }],
    activeTasks: [{ id: "t1", title: "<img src=x>", status: "doing", priority: "normal", agentId: "a1", agentTitle: "A", projectId: "p", projectName: "P", updatedAt: "t" }],
    upcomingSchedules: [{ id: "s1", name: "<svg onload=boom>", agentId: "a1", agentTitle: "A", nextRunAt: "t", timezone: "UTC", lastOutcome: "ok" }],
  }));
  assert.doesNotMatch(html, /<script>x|<img src=x|<svg onload|<b>P<\/b>/);
  assert.match(html, /&lt;script&gt;x/);
  assert.match(html, /&lt;img src=x&gt;/);
  assert.match(html, /&lt;svg onload=boom&gt;/);
  assert.match(html, /&lt;b&gt;P&lt;\/b&gt;/);
});

test("normalization still caps the payload lists the home board renders", () => {
  const make = (count, prefix, mapper) => Array.from({ length: count }, (_, index) => mapper(index, `${prefix}-${index}`));
  const normalized = normalizeOverviewPayload(overview({
    recentConversations: make(20, "conversation", (index, id) => ({ id, title: `Conversation ${index}` })),
    activeTasks: make(20, "task", (index, id) => ({ id, title: `Task ${index}` })),
    activeRuns: make(20, "run", (index, id) => ({ id, agentTitle: `Run ${index}` })),
    upcomingSchedules: make(20, "schedule", (index, id) => ({ id, name: `Schedule ${index}` })),
  }));
  assert.equal(normalized.recentConversations.length, 8);
  assert.equal(normalized.activeTasks.length, 8);
  assert.equal(normalized.activeRuns.length, 6);
  assert.equal(normalized.upcomingSchedules.length, 8);

  // The home board renders the capped rows and drops the overflow.
  const html = renderOverviewDashboard(normalized);
  assert.match(html, /Conversation 0/);
  assert.match(html, /Schedule 0/);
  assert.match(html, /Task 0/);
  assert.match(html, /Run 0/);
  assert.doesNotMatch(html, /Conversation 8|Schedule 8|Task 8|Run 6/);
  assert.match(html, /data-overview-action="open-conversation"/);
  assert.match(html, /data-overview-action="open-schedule"/);
  assert.match(html, /data-overview-action="open-task"/);
  assert.match(html, /data-overview-action="open-run"/);
  assert.doesNotMatch(html, /data-overview-launcher[=\s>]|class="overview-launcher-card"/);
});

test("homepage parks the composer until a later custom design restores it", () => {
  assert.equal(OVERVIEW_LAUNCHER_VISIBLE, false);
  const hidden = renderOverviewDashboard(overview());
  assert.match(hidden, /overview-page-title/);
  assert.match(hidden, /data-overview-section="recent-conversations"/);
  assert.doesNotMatch(hidden, /data-overview-launcher[=\s>]|class="overview-launcher-card"/);

  const restored = renderOverviewDashboard(overview(), { showLauncher: true });
  assert.match(restored, /data-overview-launcher>/);
  assert.match(restored, /class="overview-launcher-card"/);
});

test("attention chips stay hidden when nothing needs the user", () => {
  const html = renderOverviewDashboard(overview({
    summary: {
      conversations: 1,
      runningAgents: 0,
      tasks: { total: 0, todo: 0, doing: 0, done: 0 },
      activeRuns: 0,
      pendingApprovals: 0,
      schedules: { total: 0, enabled: 0, due: 0, failed: 0 },
    },
  }));
  assert.doesNotMatch(html, /data-overview-section="attention"/);
});

test("upcoming schedules mark failed and skipped last outcomes", () => {
  const failed = renderOverviewDashboard(overview({
    upcomingSchedules: [{ id: "s1", name: "Broken job", agentId: "a1", agentTitle: "Runner", nextRunAt: "t", timezone: "UTC", lastOutcome: "failure" }],
  }));
  assert.match(failed, /data-tone="danger"/);
  assert.match(failed, /上次失败/);
  assert.match(failed, /Broken job/);

  const skipped = renderOverviewDashboard(overview({
    upcomingSchedules: [{ id: "s2", name: "Busy skip", agentId: "a1", agentTitle: "Runner", nextRunAt: "t", timezone: "UTC", lastOutcome: "skipped" }],
  }));
  assert.match(skipped, /data-tone="warning"/);
  assert.match(skipped, /上次跳过/);
});

test("render orders heatmap, resources, the work board, then the parked composer", () => {
  const html = renderOverviewDashboard(overview(), {
    formatDateTime: (value) => `date:${value}`,
    systemMetrics: { cpu: { available: true, percent: 50 } },
    renderSystemMetrics: (model) => `<section data-fake-metrics="${model?.cpu?.percent ?? ""}"></section>`,
    launcherContext: {
      displayName: "Ray",
      hour: 14,
      projects: [{ id: "project-1", name: "Autoto", path: "C:/Autoto" }],
      selectedProjectId: "project-1",
      models: [{ value: "openai:gpt-5", label: "GPT-5", group: "OpenAI" }],
      selectedModel: "openai:gpt-5",
      selectedEffort: "medium",
    },
    launcherOpenSelect: "model",
    showLauncher: true,
  });
  assert.match(html, /data-overview-launcher/);
  assert.match(html, /下午好，Ray/);
  assert.match(html, /class="overview-launcher-card"/);
  assert.match(html, /class="overview-page-header"/);
  assert.match(html, /data-overview-section="attention"/);
  assert.match(html, /data-overview-section="recent-conversations"/);
  assert.match(html, /data-overview-section="in-progress"/);
  assert.match(html, /data-overview-section="upcoming-schedules"/);
  assert.match(html, /data-overview-section="activity"/);
  assert.ok(html.indexOf("overview-page-header") < html.indexOf('data-overview-section="activity"'));
  assert.ok(html.indexOf('data-overview-section="activity"') < html.indexOf("data-fake-metrics"));
  assert.ok(html.indexOf("data-fake-metrics") < html.indexOf('data-overview-section="attention"'));
  assert.ok(html.indexOf('data-overview-section="attention"') < html.indexOf('data-overview-section="recent-conversations"'));
  assert.ok(html.indexOf('data-overview-section="recent-conversations"') < html.indexOf('data-overview-section="in-progress"'));
  assert.ok(html.indexOf('data-overview-section="in-progress"') < html.indexOf('data-overview-section="upcoming-schedules"'));
  assert.ok(html.indexOf('data-overview-section="upcoming-schedules"') < html.indexOf("data-overview-launcher>"));
  assert.doesNotMatch(html, /overview-launcher-suggestions|data-overview-launcher-action="suggestion"/);
  assert.match(html, /data-overview-action="open-conversation"/);
  assert.match(html, /data-overview-action="approvals"/);
  assert.match(html, /Release plan/);
  assert.match(html, /Nightly tests/);
  assert.match(html, /Finish dashboard/);
  // The heatmap card carries no visible title; the caption line and the
  // cells-group aria-label carry the meaning instead.
  assert.match(html, /<h2>/);
  // The toolbar keeps the folder picker and the icon send button inside the card.
  assert.match(html, /data-overview-launcher-action="choose-directory"/);
  assert.match(html, /data-overview-launcher-action="submit"/);
  assert.match(html, /class="overview-launcher-select-trigger composer-select-trigger"[^>]*data-overview-launcher-select="model"/);
  assert.match(html, /class="composer-select-popover overview-launcher-select-popover composer-model-popover"/);
  assert.match(html, /class="composer-model-group-heading"[^>]*>OpenAI</);
  assert.match(html, /class="composer-select-option composer-model-option"[^>]*aria-selected="true"/);
  assert.doesNotMatch(html, /<main\b/i);
  assert.match(html, /id="overviewDashboardTitle"/);
  assert.match(html, /class="overview-live-region sr-only" role="status" aria-live="polite" aria-atomic="true"/);
  assert.doesNotMatch(html, /class="overview-stat"|overview-stats-rows/);
  assert.doesNotMatch(html, /overview-(?:hero-subtitle|dashboard-header)/);
});

test("render supports optional translation keys", () => {
  const keys = [];
  const html = renderOverviewDashboard(overview(), {
    key: (name) => `home.${name}`,
    translate: (key, params) => {
      keys.push([key, params]);
      return key === "home.activity" ? "Custom activity" : key;
    },
  });
  // The activity name survives as the cells-group aria-label now that the
  // card renders no visible title.
  assert.match(html, /aria-label="Custom activity"/);
  assert.ok(keys.some(([key]) => key === "home.activity"));
  assert.doesNotMatch(html, /home\.promptPlaceholder/);

  const launcherKeys = [];
  renderOverviewDashboard(overview(), {
    key: (name) => `home.${name}`,
    translate: (key) => {
      launcherKeys.push(key);
      return key;
    },
    showLauncher: true,
  });
  assert.ok(launcherKeys.includes("home.promptPlaceholder"));
});

test("translation fallback rejects missing keys and non-string translator output", () => {
  const html = renderOverviewDashboard(overview(), {
    translate: (key) => key.endsWith(".activity") ? { unsafe: true } : key,
  });
  assert.match(html, /使用热力图/);
  assert.doesNotMatch(html, /\[object Object\]/);

  const keyFailure = renderOverviewDashboard(overview(), {
    key: () => { throw new Error("bad key builder"); },
    translate: () => "unreachable",
  });
  assert.match(keyFailure, /使用热力图/);
});

test("controller reconciles launcher defaults and returns a safe launcher state copy", () => {
  const host = fakeHost();
  let context = {
    displayName: "Ray",
    hour: 9,
    projects: [{ id: "p1", name: "One" }, { id: "p2", name: "Two" }],
    selectedProjectId: "p2",
    models: [{ value: "m1", label: "One" }, { value: "m2", label: "Two" }],
    selectedModel: "m2",
    selectedEffort: "high",
  };
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => context,
  });

  const initial = controller.getState();
  assert.deepEqual(initial.launcher, {
    draft: "",
    projectId: "p2",
    model: "m2",
    reasoningEffort: "high",
    busy: false,
    error: "",
  });
  initial.launcher.projectId = "mutated";
  assert.equal(controller.getState().launcher.projectId, "p2");

  context = {
    ...context,
    projects: [{ id: "p3", name: "Three" }],
    selectedProjectId: "missing",
    models: [{ value: "m3", label: "Three" }],
    selectedModel: "missing",
  };
  controller.render();
  assert.equal(controller.getState().launcher.projectId, "p3");
  assert.equal(controller.getState().launcher.model, "m3");
});

test("launcher project, selects, and suggestions update editable state", () => {
  const host = fakeHost();
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => ({
      projects: [{ id: "p1", name: "One" }, { id: "p2", name: "Two" }],
      selectedProjectId: "p1",
      models: [{ value: "m1", label: "One" }, { value: "m2", label: "Two" }],
      selectedModel: "m1",
      selectedEffort: "auto",
    }),
    translate: (key) => key === "overview.suggestionFixPrompt" ? "修复这个定制问题：" : key,
    showLauncher: true,
  });

  assert.match(host.innerHTML, /data-overview-launcher-field="projectId"/);
  assert.match(host.innerHTML, /data-overview-launcher-action="choose-directory"/);
  assert.doesNotMatch(host.innerHTML, /data-overview-launcher-(?:action="mode"|mode=)/);

  host.change("projectId", "p2");
  host.launcherClick("toggle-select", { overviewLauncherSelect: "model" });
  assert.match(host.innerHTML, /overview-launcher-select-popover composer-model-popover/);
  host.launcherClick("select-option", { overviewLauncherSelect: "model", overviewLauncherValue: "m2" });
  assert.doesNotMatch(host.innerHTML, /overview-launcher-select-popover composer-model-popover/);
  host.launcherClick("toggle-select", { overviewLauncherSelect: "reasoningEffort" });
  // Matched by field rather than by class string: project and effort both render
  // a plain (non-model) popover with an identical class list, so a class-only
  // assertion would no longer identify which menu is open.
  assert.match(host.innerHTML, /data-overview-launcher-popover="reasoningEffort"/);
  assert.equal(host.keydown("", { key: "Escape" }), true);
  assert.doesNotMatch(host.innerHTML, /data-overview-launcher-popover="reasoningEffort"/);
  host.launcherClick("toggle-select", { overviewLauncherSelect: "reasoningEffort" });
  host.launcherClick("select-option", { overviewLauncherSelect: "reasoningEffort", overviewLauncherValue: "high" });
  assert.deepEqual(controller.getState().launcher, {
    draft: "",
    projectId: "p2",
    model: "m2",
    reasoningEffort: "high",
    busy: false,
    error: "",
  });

  host.launcherClick("suggestion", { overviewLauncherSuggestion: "fix" });
  assert.equal(controller.getState().launcher.draft, "修复这个定制问题：");
  assert.deepEqual(host.focusLog.at(-1), ["field", "draft"]);
  assert.match(host.innerHTML, /修复这个定制问题：<\/textarea>/);

});

// The project field used to be a bare native <select>, whose browser-drawn
// option panel no stylesheet here can colour; under the dark presets that left
// white text on the platform's white panel. It now renders the same custom
// popover as model and effort, so the click path has to work for it too.
test("launcher project renders a themed popover and selects through it", () => {
  const host = fakeHost();
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => ({
      projects: [{ id: "p1", name: "One" }, { id: "p2", name: "Two" }],
      selectedProjectId: "p1",
      models: [{ value: "m1", label: "One" }],
      selectedModel: "m1",
      selectedEffort: "auto",
    }),
    showLauncher: true,
  });

  // Every field keeps its hidden native select for form semantics.
  assert.match(host.innerHTML, /data-overview-launcher-field="projectId"/);
  assert.match(host.innerHTML, /composer-native-select[^>]*data-overview-launcher-field="projectId"/);
  // Sized like the model pill, not the narrow effort pill.
  assert.match(host.innerHTML, /overview-launcher-custom-select select-pill project-pill/);
  assert.doesNotMatch(host.innerHTML, /data-overview-launcher-popover="projectId"/);

  host.launcherClick("toggle-select", { overviewLauncherSelect: "projectId" });
  assert.match(host.innerHTML, /data-overview-launcher-popover="projectId"/);
  host.launcherClick("select-option", { overviewLauncherSelect: "projectId", overviewLauncherValue: "p2" });
  assert.equal(controller.getState().launcher.projectId, "p2");
  assert.doesNotMatch(host.innerHTML, /data-overview-launcher-popover="projectId"/);

  // A value that is not in the current project list must not be accepted.
  host.launcherClick("select-option", { overviewLauncherSelect: "projectId", overviewLauncherValue: "gone" });
  assert.equal(controller.getState().launcher.projectId, "p2");

  // Each field gets its own DOM id; the old two-way branch handed project the
  // effort field's id.
  assert.match(host.innerHTML, /id="overviewLauncherProject"/);
  assert.match(host.innerHTML, /id="overviewLauncherModel"/);
  assert.match(host.innerHTML, /id="overviewLauncherReasoningEffort"/);
});

test("launcher project popover stays shut when there are no projects", () => {
  const host = fakeHost();
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => ({ projects: [], models: [{ value: "m1", label: "One" }], selectedModel: "m1", selectedEffort: "auto" }),
    showLauncher: true,
  });

  host.launcherClick("toggle-select", { overviewLauncherSelect: "projectId" });
  assert.doesNotMatch(host.innerHTML, /data-overview-launcher-popover="projectId"/);
  // The field still renders, disabled, showing the empty-state label.
  assert.match(host.innerHTML, /data-overview-launcher-field="projectId"[^>]*disabled/);
});

test("Enter submits the launcher payload, Shift+Enter composes, and busy prevents duplicates", async () => {
  const host = fakeHost();
  const launch = deferred();
  const payloads = [];
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => ({
      projects: [{ id: "p1", name: "One" }],
      selectedProjectId: "p1",
      models: [{ value: "m1", label: "One" }],
      selectedModel: "m1",
      selectedEffort: "medium",
    }),
    onLaunch: (payload) => {
      payloads.push(payload);
      return launch.promise;
    },
    showLauncher: true,
  });

  host.input("  build it  ");
  assert.equal(host.keydown("  build it  ", { shiftKey: true }), false);
  assert.equal(host.keydown("  build it  ", { isComposing: true }), false);
  assert.equal(payloads.length, 0);
  assert.equal(host.keydown("  build it  "), true);
  assert.deepEqual(payloads, [{
    text: "build it",
    projectId: "p1",
    model: "m1",
    reasoningEffort: "medium",
  }]);
  assert.equal(controller.getState().launcher.busy, true);
  assert.match(host.innerHTML, /正在启动…/);
  host.keydown("  build it  ");
  host.launcherClick("submit");
  assert.equal(payloads.length, 1);

  launch.resolve();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(controller.getState().launcher.busy, false);
  assert.equal(controller.getState().launcher.draft, "");
});

test("launcher requires a project, preserves drafts, and reports directory errors", async () => {
  const host = fakeHost();
  const errors = [];
  let launchCalls = 0;
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    getLauncherContext: () => ({ models: [{ value: "m1", label: "One" }], selectedModel: "m1" }),
    onLaunch: async () => {
      launchCalls += 1;
      throw new Error("<launch failed>");
    },
    onChooseDirectory: async () => { throw new Error("directory failed"); },
    onError: (error, action) => errors.push([error.message, action]),
    showLauncher: true,
  });

  host.input("keep this draft");
  host.launcherClick("submit");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(launchCalls, 0);
  assert.equal(controller.getState().launcher.error, "请先选择一个工作区项目。");
  assert.match(host.innerHTML, /请先选择一个工作区项目。/);

  assert.equal(controller.getState().launcher.draft, "keep this draft");
  assert.deepEqual(errors, []);

  host.launcherClick("choose-directory");
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(errors[0], ["directory failed", "choose-directory"]);
});

test("empty launcher submissions are ignored", async () => {
  const host = fakeHost();
  let calls = 0;
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    onLaunch: async () => { calls += 1; },
    showLauncher: true,
  });
  host.input(" \n ");
  host.launcherClick("submit");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls, 0);
});

test("delegated clicks dispatch the stat surfaces and the list rows, nothing else", () => {
  const host = fakeHost();
  const navigations = [];
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    onNavigate: (action, id) => navigations.push([action, id]),
  });

  for (const action of ["conversation", "tasks", "runs", "schedules", "approvals"]) host.click(action);
  host.click("open-conversation", "conversation-1");
  host.click("open-schedule", "schedule-1");
  host.click("open-task", "task-1");
  host.click("open-run", "run-1");
  assert.deepEqual(navigations, [
    ["conversation", ""],
    ["tasks", ""],
    ["runs", ""],
    ["schedules", ""],
    ["approvals", ""],
    ["open-conversation", "conversation-1"],
    ["open-schedule", "schedule-1"],
    ["open-task", "task-1"],
    ["open-run", "run-1"],
  ]);
});

test("controller requests /api/overview, deduplicates ordinary loads, and discards stale forced responses", async () => {
  const first = deferred();
  const second = deferred();
  const paths = [];
  const activityPaths = [];
  const host = fakeHost();
  const controller = createOverviewDashboardController({
    host,
    request: (path) => {
      if (path.startsWith("/api/usage/history")) {
        activityPaths.push(path);
        return Promise.resolve({ trend: [] });
      }
      paths.push(path);
      return paths.length === 1 ? first.promise : second.promise;
    },
  });

  const oldLoad = controller.load();
  const duplicate = controller.load();
  assert.equal(oldLoad, duplicate);
  assert.deepEqual(paths, ["/api/overview"]);
  assert.match(host.innerHTML, /data-overview-state="loading"/);

  const forced = controller.load({ force: true });
  assert.deepEqual(paths, ["/api/overview", "/api/overview"]);
  second.resolve(overview({ capturedAt: "new", recentConversations: [{ id: "new", title: "New response" }] }));
  assert.equal(await forced, true);
  first.resolve(overview({ capturedAt: "old", recentConversations: [{ id: "old", title: "Old response" }] }));
  assert.equal(await oldLoad, false);

  const state = controller.getState();
  assert.equal(state.status, "ready");
  assert.equal(state.payload.capturedAt, "new");
  assert.equal(state.payload.recentConversations[0].id, "new");
  // The heatmap rides along with every load but on its own request.
  assert.equal(activityPaths.length, 2);
  assert.match(activityPaths[0], /^\/api\/usage\/history\?bucket=day&tzOffset=-?\d+&from=\d{4}-\d{2}-\d{2}&to=\d{4}-\d{2}-\d{2}&limit=1$/);
});

test("controller renders safe failure state and retries without routing away", async () => {
  const host = fakeHost();
  const navigations = [];
  let calls = 0;
  const controller = createOverviewDashboardController({
    host,
    request: async (path) => {
      if (path.startsWith("/api/usage/history")) return { trend: [] };
      calls += 1;
      assert.equal(path, "/api/overview");
      if (calls === 1) throw new Error("<bad failure>");
      return overview({ capturedAt: "recovered" });
    },
    onNavigate: (action, id) => navigations.push([action, id]),
  });

  assert.equal(await controller.load(), false);
  assert.equal(controller.getState().status, "error");
  assert.match(host.innerHTML, /data-overview-state="error"/);
  assert.match(host.innerHTML, /&lt;bad failure&gt;/);
  assert.doesNotMatch(host.innerHTML, /<bad failure>/);
  assert.match(host.innerHTML, /data-overview-action="refresh"/);

  host.click("tasks");
  assert.deepEqual(navigations.at(-1), ["tasks", ""]);
  assert.equal(await controller.load(), true);
  assert.equal(calls, 2);
  assert.deepEqual(navigations.at(-1), ["tasks", ""]);
  assert.equal(controller.getState().status, "ready");
  assert.equal(controller.getState().payload.capturedAt, "recovered");
});

test("forced refresh errors retain old payload and expose a non-destructive inline error", async () => {
  const host = fakeHost();
  let calls = 0;
  const controller = createOverviewDashboardController({
    host,
    request: async (path) => {
      if (path.startsWith("/api/usage/history")) return { trend: [] };
      calls += 1;
      if (calls === 1) return overview({ capturedAt: "old-data" });
      throw new Error("refresh failed");
    },
  });

  assert.equal(await controller.load(), true);
  assert.equal(await controller.load({ force: true }), false);
  const state = controller.getState();
  assert.equal(state.status, "error");
  assert.equal(state.payload.capturedAt, "old-data");
  assert.equal(state.error, "refresh failed");
  assert.match(host.innerHTML, /old-data/);
  assert.match(host.innerHTML, /refresh failed/);
  assert.match(host.innerHTML, /overview-inline-error/);
  assert.match(host.innerHTML, /data-overview-section="recent-conversations"/);
  assert.equal(host.listenerCounts.get("click"), 1);
});

test("rejected async navigation is reported without an unhandled rejection", async () => {
  const host = fakeHost();
  const errors = [];
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    onNavigate: async () => { throw new Error("navigation failed"); },
    onError: (error, action, id) => errors.push([error.message, action, id]),
  });

  host.click("conversation");
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(errors, [["navigation failed", "conversation", ""]]);
});

// 2026-07-26 is a Sunday, so it opens its own week and the final column holds
// only that one past day; the other six are future padding.
test("activity heatmap lays out whole weeks ending on today", () => {
  const model = buildActivityHeatmap([], { today: "2026-07-26" });

  assert.equal(model.weeks.length, 53);
  assert.equal(model.end, "2026-07-26");
  assert.equal(model.start, "2025-07-27");
  assert.equal(model.weeks.flatMap((week) => week.days).length, 53 * 7);
  assert.equal(model.weeks[0].days[0].date, "2025-07-27");
  assert.equal(model.weeks.at(-1).days[0].date, "2026-07-26");
  assert.equal(model.weeks.at(-1).days[0].future, false);
  assert.deepEqual(model.weeks.at(-1).days.slice(1).map((day) => day.future), [true, true, true, true, true, true]);
  // The first column is never labelled; every other month change is.
  assert.equal(model.weeks[0].monthLabel, "");
  assert.equal(model.weeks.filter((week) => week.monthLabel).length, 12);
});

test("activity heatmap scales levels against the busiest day and ignores days outside the window", () => {
  const model = buildActivityHeatmap([
    { bucket: "2026-07-26", requestCount: 100 },
    { bucket: "2026-07-25", requestCount: 75 },
    { bucket: "2026-07-24", requestCount: 50 },
    { bucket: "2026-07-23", requestCount: 25 },
    { bucket: "2026-07-22", requestCount: 0 },
    // Before the window opens and after today: both must be excluded.
    { bucket: "2025-07-26", requestCount: 999 },
    { bucket: "2026-07-27", requestCount: 999 },
    { bucket: "not-a-date", requestCount: 999 },
    { bucket: "2026-02-30", requestCount: 999 },
  ], { today: "2026-07-26" });

  const byDate = new Map(model.weeks.flatMap((week) => week.days).map((day) => [day.date, day]));
  assert.equal(model.total, 250);
  assert.equal(model.max, 100);
  assert.deepEqual([
    byDate.get("2026-07-26").level,
    byDate.get("2026-07-25").level,
    byDate.get("2026-07-24").level,
    byDate.get("2026-07-23").level,
    byDate.get("2026-07-22").level,
  ], [4, 3, 2, 1, 0]);
  // A day with any activity is never level 0, however small its share.
  assert.equal(buildActivityHeatmap([
    { bucket: "2026-07-26", requestCount: 1000 },
    { bucket: "2026-07-25", requestCount: 1 },
  ], { today: "2026-07-26" }).weeks.at(-2).days.at(-1).level, 1);
  // Before the window the day is absent entirely; after today it is present as
  // future padding but its count is dropped rather than drawn.
  assert.equal(byDate.get("2025-07-26"), undefined);
  assert.deepEqual(byDate.get("2026-07-27"), { date: "2026-07-27", count: 0, tokens: 0, level: 0, future: true });
});

test("activity heatmap accumulates tokens per day alongside request counts", () => {
  const model = buildActivityHeatmap([
    { bucket: "2026-07-26", requestCount: 4, inputTokens: 100, outputTokens: 50, totalTokens: 150 },
    // Same day arriving as a second bucket: both counts and tokens accumulate.
    { bucket: "2026-07-26", requestCount: 1, inputTokens: 10, outputTokens: 5, totalTokens: 15 },
    // No totalTokens field: falls back to input + output, matching how the
    // server derives it.
    { bucket: "2026-07-25", requestCount: 2, inputTokens: 30, outputTokens: 20 },
    // Requests recorded but no token data at all.
    { bucket: "2026-07-24", requestCount: 3 },
    // Outside the window: excluded from the totals.
    { bucket: "2026-07-27", requestCount: 9, totalTokens: 9999 },
  ], { today: "2026-07-26" });

  const byDate = new Map(model.weeks.flatMap((week) => week.days).map((day) => [day.date, day]));
  assert.equal(byDate.get("2026-07-26").tokens, 165);
  assert.equal(byDate.get("2026-07-25").tokens, 50);
  assert.equal(byDate.get("2026-07-24").tokens, 0);
  assert.equal(model.total, 10);
  assert.equal(model.totalTokens, 215);
  assert.equal(model.recentWeek, 10);
  assert.equal(model.recentMonth, 10);
  // reasoning and cached tokens are reported separately by the server and are
  // deliberately not folded into the total.
  assert.equal(buildActivityHeatmap([
    { bucket: "2026-07-26", requestCount: 1, inputTokens: 10, outputTokens: 5, reasoningTokens: 400, cachedInputTokens: 900 },
  ], { today: "2026-07-26" }).totalTokens, 15);
});

test("heatmap recent windows cover the trailing 7 and 30 days ending today", () => {
  const model = buildActivityHeatmap([
    { bucket: "2026-07-26", requestCount: 3 },
    // Six days back: the last day inside the 7-day window.
    { bucket: "2026-07-20", requestCount: 2 },
    // Seven days back: outside the week, inside the month.
    { bucket: "2026-07-19", requestCount: 7 },
    // Twenty-nine days back: the last day inside the 30-day window.
    { bucket: "2026-06-27", requestCount: 11 },
    // Thirty days back: outside both windows but inside the year total.
    { bucket: "2026-06-26", requestCount: 13 },
  ], { today: "2026-07-26" });

  assert.equal(model.recentWeek, 5);
  assert.equal(model.recentMonth, 23);
  assert.equal(model.total, 36);
});

test("heatmap tooltips report tokens when present and omit them when zero", () => {
  const withTokens = renderOverviewDashboard(overview(), {
    today: "2026-07-26",
    activityTrend: [{ bucket: "2026-07-26", requestCount: 7, inputTokens: 1500, outputTokens: 900, totalTokens: 2400 }],
  });
  assert.match(withTokens, /2026-07-26：7 次请求 · 2,400 tokens/);
  assert.match(withTokens, /过去一年共 7 次模型请求 · 2,400 tokens/);

  // A day with requests but no token data keeps the shorter wording rather than
  // asserting "0 tokens".
  const withoutTokens = renderOverviewDashboard(overview(), {
    today: "2026-07-26",
    activityTrend: [{ bucket: "2026-07-26", requestCount: 7 }],
  });
  assert.match(withoutTokens, /2026-07-26：7 次请求"/);
  assert.doesNotMatch(withoutTokens, /0 tokens/);
});

// The resource cards are injected rather than imported, so the dashboard has to
// place them, tolerate their absence, and survive a renderer that throws.
test("system metrics section is injected after the heatmap, before the work board", () => {
  const html = renderOverviewDashboard(overview(), {
    today: "2026-07-26",
    systemMetrics: { cpu: { available: true, percent: 50 } },
    renderSystemMetrics: (model) => `<section data-fake-metrics="${model?.cpu?.percent ?? ""}"></section>`,
  });

  assert.match(html, /data-fake-metrics="50"/);
  assert.equal(html.indexOf("data-fake-metrics") > html.indexOf('data-overview-section="activity"'), true);
  assert.equal(html.indexOf("data-fake-metrics") < html.indexOf('data-overview-section="recent-conversations"'), true);

  // No renderer supplied: the dashboard renders exactly as before.
  assert.doesNotMatch(renderOverviewDashboard(overview(), { today: "2026-07-26" }), /data-fake-metrics/);
  // A renderer returning nothing measurable contributes no markup.
  assert.doesNotMatch(renderOverviewDashboard(overview(), { today: "2026-07-26", renderSystemMetrics: () => "" }), /overview-metrics/);

  // A throwing renderer must not take the dashboard down.
  const survived = renderOverviewDashboard(overview(), {
    today: "2026-07-26",
    renderSystemMetrics: () => { throw new Error("metrics blew up"); },
  });
  assert.match(survived, /data-overview-section="activity"/);
  assert.match(survived, /overview-page-title/);
});

test("controller drives the injected metrics poller and re-renders on updates", () => {
  const host = fakeHost();
  let emit = null;
  const log = [];
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    renderSystemMetrics: (model) => (model?.cpu?.available ? `<section data-fake-metrics="${model.cpu.percent}"></section>` : ""),
    createSystemMetricsPoller: ({ onUpdate }) => {
      emit = onUpdate;
      return {
        start() { log.push("start"); return true; },
        stop() { log.push("stop"); return true; },
      };
    },
  });

  assert.doesNotMatch(host.innerHTML, /data-fake-metrics/);
  controller.start();
  controller.stop();
  assert.deepEqual(log, ["start", "stop"]);

  // A reading arriving from the poller re-renders the dashboard in place.
  emit({ cpu: { available: true, percent: 77 } });
  assert.match(host.innerHTML, /data-fake-metrics="77"/);
  assert.equal(controller.getState().systemMetrics.cpu.percent, 77);
});

// Re-rendering replaces the composer textarea, which drops focus and kills an
// in-progress IME composition. While the draft field is focused, background
// updates must hold their repaint and flush it when the field blurs.
test("background updates defer their repaint while the draft field is focused", () => {
  const host = fakeHost();
  let emit = null;
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    renderSystemMetrics: (model) => (model?.cpu?.available ? `<section data-fake-metrics="${model.cpu.percent}"></section>` : ""),
    createSystemMetricsPoller: ({ onUpdate }) => {
      emit = onUpdate;
      return { start: () => true, stop: () => true };
    },
    showLauncher: true,
  });

  const draftNode = {
    closest(selector) {
      return selector === "[data-overview-launcher-field=\"draft\"]" ? this : null;
    },
  };
  const originalDocument = globalThis.document;
  globalThis.document = { activeElement: draftNode };
  try {
    // A reading arriving mid-typing updates state but leaves the DOM alone.
    emit({ cpu: { available: true, percent: 42 } });
    assert.equal(controller.getState().systemMetrics.cpu.percent, 42);
    assert.doesNotMatch(host.innerHTML, /data-fake-metrics/);

    // Focus moving to another launcher control still must not repaint: the
    // control under the pointer would be replaced and the click swallowed.
    globalThis.document = { activeElement: null };
    const launcherButton = { closest: (selector) => (selector === "[data-overview-launcher]" ? {} : null) };
    host.focusout(draftNode, launcherButton);
    assert.doesNotMatch(host.innerHTML, /data-fake-metrics/);

    // Leaving the composer entirely flushes the deferred repaint.
    host.focusout(draftNode, null);
    assert.match(host.innerHTML, /data-fake-metrics="42"/);

    // Nothing pending: a later blur does not repaint again (innerHTML stable).
    const settled = host.innerHTML;
    host.focusout(draftNode, null);
    assert.equal(host.innerHTML, settled);
  } finally {
    globalThis.document = originalDocument;
  }
});

test("controller works when no metrics poller is injected", () => {
  const host = fakeHost();
  const controller = createOverviewDashboardController({ host, request: async () => overview() });

  // start/stop stay callable so callers need no capability checks.
  assert.equal(controller.start(), false);
  assert.equal(controller.stop(), false);
  assert.equal(controller.getState().systemMetrics, null);
  assert.match(host.innerHTML, /overview-page-title/);
});

// A poller factory that throws must not prevent the dashboard from existing.
test("controller survives a failing metrics poller factory", () => {
  const host = fakeHost();
  const errors = [];
  const controller = createOverviewDashboardController({
    host,
    request: async () => overview(),
    createSystemMetricsPoller: () => { throw new Error("no poller"); },
    onError: (error, action) => errors.push([error.message, action]),
  });

  assert.deepEqual(errors, [["no poller", "system-metrics"]]);
  assert.equal(controller.start(), false);
  assert.match(host.innerHTML, /overview-page-title/);
});

test("activity heatmap renders escaped tooltips and a legend, and survives a failed load", () => {
  const html = renderOverviewDashboard(overview(), {
    today: "2026-07-26",
    activityTrend: [{ bucket: "2026-07-26", requestCount: 7 }],
  });

  assert.match(html, /data-overview-section="activity"/);
  assert.match(html, /2026-07-26：7 次请求/);
  assert.match(html, /--overview-heatmap-weeks:53/);
  // 53 weeks x 7 days of grid, plus the 5 legend swatches.
  assert.equal(html.match(/class="overview-heatmap-cell"|class="overview-heatmap-cell is-future"/g).length, 53 * 7 + 5);
  assert.match(html, /过去一年共 7 次模型请求/);
  // The header also reports the recent pace alongside the year total.
  assert.match(html, /近 7 天 7 次请求 · 近 30 天 7 次请求/);

  const failed = renderOverviewDashboard(overview(), { today: "2026-07-26", activityStatus: "error" });
  assert.match(failed, /使用记录暂时无法加载。/);
  // The failed load renders no recent-usage line alongside the error caption.
  assert.doesNotMatch(failed, /overview-heatmap-recent/);
  // The heatmap failing must not take the rest of the dashboard down.
  assert.match(failed, /overview-page-title/);
  assert.doesNotMatch(failed, /data-overview-state="error"/);

  const empty = renderOverviewDashboard(overview(), { today: "2026-07-26" });
  assert.match(empty, /过去一年还没有使用记录。/);
});
