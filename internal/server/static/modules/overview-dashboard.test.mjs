import test from "node:test";
import assert from "node:assert/strict";

import {
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
  });

  assert.doesNotMatch(html, /<script>|<img src=x|<svg onload|<textarea autofocus/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /&lt;svg onload=boom&gt;/);
  assert.match(html, /&lt;textarea autofocus onfocus=boom&gt;/);
  assert.match(html, /value="&quot;&gt;&lt;img/);
});

test("normalization keeps capped backend lists even though the launcher no longer renders them", () => {
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

  const html = renderOverviewDashboard(normalized);
  assert.doesNotMatch(html, /Conversation 0|Task 0|Run 0|Schedule 0|data-overview-action="open-/);
});

test("render includes the hero launcher, four summaries, and heatmap without legacy list sections", () => {
  const html = renderOverviewDashboard(overview(), {
    formatDateTime: (value) => `date:${value}`,
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
  });
  assert.equal((html.match(/class="overview-summary-card settings-stat-card"/g) || []).length, 4);
  assert.match(html, /data-overview-launcher/);
  assert.match(html, /下午好，Ray/);
  assert.match(html, /class="overview-launcher-input"/);
  assert.match(html, /data-overview-section="activity"/);
  assert.ok(html.indexOf("overview-launcher-hero") < html.indexOf("overview-summary-grid"));
  assert.ok(html.indexOf('data-overview-section="activity"') < html.indexOf("data-overview-launcher>"));
  assert.match(html, /class="overview-launcher-select-trigger composer-select-trigger"[^>]*data-overview-launcher-select="model"/);
  assert.match(html, /class="composer-select-popover overview-launcher-select-popover composer-model-popover"/);
  assert.match(html, /class="composer-model-group-heading"[^>]*>OpenAI</);
  assert.match(html, /class="composer-select-option composer-model-option"[^>]*aria-selected="true"/);
  for (const action of ["conversation", "tasks", "runs", "schedules"]) {
    assert.match(html, new RegExp(`data-overview-action="${action}"`));
  }
  assert.doesNotMatch(html, /<main\b/i);
  assert.match(html, /id="overviewDashboardTitle"/);
  assert.match(html, /class="overview-live-region sr-only" role="status" aria-live="polite" aria-atomic="true"/);
  assert.doesNotMatch(html, /overview-(?:hero-subtitle|dashboard-header|launcher-suggestions)|data-overview-action="refresh"/);
  assert.doesNotMatch(html, /<small>/);
  assert.doesNotMatch(html, /data-overview-section="(?:continue-working|in-progress|upcoming|pending)"/);
  assert.doesNotMatch(html, /继续工作|正在进行|即将执行|待处理提示/);
  assert.doesNotMatch(html, /data-overview-action="(?:approvals|open-conversation|open-task|open-run|open-schedule)"/);
});

test("render supports optional translation keys", () => {
  const keys = [];
  const html = renderOverviewDashboard(overview(), {
    key: (name) => `home.${name}`,
    translate: (key, params) => {
      keys.push([key, params]);
      return key === "home.title" ? "Custom title" : key;
    },
  });
  assert.match(html, /aria-label="Custom title"/);
  assert.ok(keys.some(([key]) => key === "home.tasks"));
});

test("translation fallback rejects missing keys and non-string translator output", () => {
  const html = renderOverviewDashboard(overview(), {
    translate: (key) => key.endsWith(".title") ? { unsafe: true } : key,
  });
  assert.match(html, /工作概览/);
  assert.doesNotMatch(html, /\[object Object\]/);

  const keyFailure = renderOverviewDashboard(overview(), {
    key: () => { throw new Error("bad key builder"); },
    translate: () => "unreachable",
  });
  assert.match(keyFailure, /工作概览/);
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
  assert.match(host.innerHTML, /class="composer-select-popover overview-launcher-select-popover"/);
  assert.equal(host.keydown("", { key: "Escape" }), true);
  assert.doesNotMatch(host.innerHTML, /class="composer-select-popover overview-launcher-select-popover"/);
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
  });
  host.input(" \n ");
  host.launcherClick("submit");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls, 0);
});

test("delegated summary clicks preserve the four supported navigation actions", () => {
  const host = fakeHost();
  const navigations = [];
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    onNavigate: (action, id) => navigations.push([action, id]),
  });

  for (const action of ["conversation", "tasks", "runs", "schedules"]) host.click(action);
  host.click("open-task", "task-1");

  assert.deepEqual(navigations, [
    ["conversation", ""],
    ["tasks", ""],
    ["runs", ""],
    ["schedules", ""],
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
  assert.doesNotMatch(host.innerHTML, /New response|Old response/);
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
  assert.doesNotMatch(host.innerHTML, /<bad failure>|data-overview-action="refresh"/);

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
  assert.doesNotMatch(host.innerHTML, /old-data/);
  assert.match(host.innerHTML, /refresh failed/);
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
  assert.deepEqual(byDate.get("2026-07-27"), { date: "2026-07-27", count: 0, level: 0, future: true });
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

  const failed = renderOverviewDashboard(overview(), { today: "2026-07-26", activityStatus: "error" });
  assert.match(failed, /使用记录暂时无法加载。/);
  // The heatmap failing must not take the rest of the dashboard down.
  assert.match(failed, /data-overview-launcher/);
  assert.equal((failed.match(/class="overview-summary-card settings-stat-card"/g) || []).length, 4);
  assert.doesNotMatch(failed, /data-overview-state="error"/);

  const empty = renderOverviewDashboard(overview(), { today: "2026-07-26" });
  assert.match(empty, /过去一年还没有使用记录。/);
});
