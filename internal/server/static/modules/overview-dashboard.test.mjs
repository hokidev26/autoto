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
      const trigger = {
        dataset: { overviewAction: action, overviewId: id },
        closest() { return this; },
      };
      listeners.get("click")?.({ target: trigger, preventDefault() {} });
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

test("render escapes every dynamic source including translator and formatter output", () => {
  const attack = '\"><img src=x onerror="boom">';
  const html = renderOverviewDashboard(overview({
    capturedAt: attack,
    recentConversations: [{ id: attack, title: "<script>alert(1)</script>", projectName: attack, status: attack, updatedAt: attack }],
    activeTasks: [{ id: attack, title: "<svg onload=boom>", priority: attack, agentTitle: attack }],
    activeRuns: [{ id: attack, agentTitle: attack, status: attack }],
    upcomingSchedules: [{ id: attack, name: attack, timezone: attack, lastOutcome: attack }],
  }), {
    translate: (key) => key.endsWith(".title") ? attack : key,
    formatDateTime: () => "<iframe src=bad>",
  });

  assert.doesNotMatch(html, /<script>|<img src=x|<svg onload|<iframe/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /&lt;svg onload=boom&gt;/);
  assert.match(html, /&lt;iframe src=bad&gt;/);
  assert.match(html, /data-overview-id="&quot;&gt;&lt;img/);
});

test("all overview lists are capped before rendering", () => {
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
  assert.equal((html.match(/data-overview-action="open-conversation"/g) || []).length, 8);
  assert.equal((html.match(/data-overview-action="open-task"/g) || []).length, 8);
  assert.doesNotMatch(html, /Conversation 8|Task 8|Run 6|Schedule 8/);
});

test("render includes four summaries, four content sections, all allowed actions, and empty states", () => {
  const html = renderOverviewDashboard(overview(), { formatDateTime: (value) => `date:${value}` });
  assert.equal((html.match(/class="overview-summary-card settings-stat-card"/g) || []).length, 4);
  for (const section of ["continue-working", "in-progress", "upcoming", "pending"]) {
    assert.match(html, new RegExp(`data-overview-section="${section}"`));
  }
  for (const action of ["refresh", "conversation", "tasks", "runs", "schedules", "approvals", "open-conversation", "open-task", "open-run", "open-schedule"]) {
    assert.match(html, new RegExp(`data-overview-action="${action}"`));
  }
  assert.doesNotMatch(html, /<main\b/i);
  assert.match(html, /id="overviewDashboardTitle"/);
  assert.match(html, /class="overview-live-region sr-only" role="status" aria-live="polite" aria-atomic="true"/);
  assert.match(html, /data-overview-action="refresh"[^>]*aria-controls="overviewDashboard"/);
  assert.match(html, /data-overview-id="conversation-1"[^>]*aria-label=/);
  assert.match(html, /data-overview-id="task-1"[^>]*aria-label=/);
  assert.match(html, /data-overview-action="approvals"[^>]*aria-label=/);
  assert.match(html, /待审批/);
  assert.match(html, /到期排程/);
  assert.match(html, /失败排程/);

  const empty = renderOverviewDashboard({ summary: {}, recentConversations: [], activeTasks: [], activeRuns: [], upcomingSchedules: [] });
  assert.match(empty, /暂无最近对话/);
  assert.match(empty, /暂无进行中的任务/);
  assert.match(empty, /暂无活跃运行/);
  assert.match(empty, /暂无即将执行的排程/);
  assert.match(empty, /当前没有待处理提示/);
});

test("rendering escapes hostile action IDs and caps every dashboard list", () => {
  const repeated = Array.from({ length: 20 }, (_, index) => ({
    id: `item-${index}\", onfocus=\"alert(1)`,
    title: `Item ${index}`,
    status: "running",
    href: "https://example.invalid",
  }));
  const html = renderOverviewDashboard(overview({
    recentConversations: repeated,
    activeTasks: repeated,
    activeRuns: repeated,
    upcomingSchedules: repeated,
  }));

  assert.doesNotMatch(html, /onfocus=\"alert/);
  assert.equal((html.match(/data-overview-action=\"open-conversation\"/g) || []).length, 8);
  assert.equal((html.match(/data-overview-action=\"open-task\"/g) || []).length, 8);
  assert.equal((html.match(/data-overview-action=\"open-run\"/g) || []).length, 6);
  assert.equal((html.match(/data-overview-action=\"open-schedule\"/g) || []).length, 8);
});

test("render supports optional translation key and date formatter", () => {
  const keys = [];
  const html = renderOverviewDashboard(overview(), {
    key: (name) => `home.${name}`,
    translate: (key, params) => {
      keys.push([key, params]);
      return key === "home.title" ? "Custom title" : key;
    },
    formatDateTime: () => "Custom time",
  });
  assert.match(html, /Custom title/);
  assert.match(html, /Custom time/);
  assert.ok(keys.some(([key]) => key === "home.tasks"));
});

test("translation fallback rejects missing keys and non-string translator output", () => {
  const html = renderOverviewDashboard(overview(), {
    translate: (key) => key.endsWith(".title") ? { unsafe: true } : key,
  });
  assert.match(html, /工作总览/);
  assert.doesNotMatch(html, /\[object Object\]/);

  const keyFailure = renderOverviewDashboard(overview(), {
    key: () => { throw new Error("bad key builder"); },
    translate: () => "unreachable",
  });
  assert.match(keyFailure, /工作总览/);
});

test("real delegated clicks preserve exact action IDs for every shell route", () => {
  const host = fakeHost();
  const navigations = [];
  createOverviewDashboardController({
    host,
    request: async () => overview(),
    onNavigate: (action, id) => navigations.push([action, id]),
  });

  for (const [action, id] of [
    ["conversation", ""],
    ["tasks", ""],
    ["runs", ""],
    ["schedules", ""],
    ["approvals", ""],
    ["open-conversation", "conversation-1"],
    ["open-task", "task-1"],
    ["open-run", "run-1"],
    ["open-schedule", "schedule-1"],
  ]) host.click(action, id);

  assert.deepEqual(navigations, [
    ["conversation", ""],
    ["tasks", ""],
    ["runs", ""],
    ["schedules", ""],
    ["approvals", ""],
    ["open-conversation", "conversation-1"],
    ["open-task", "task-1"],
    ["open-run", "run-1"],
    ["open-schedule", "schedule-1"],
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
  assert.match(host.innerHTML, /New response/);
  assert.doesNotMatch(host.innerHTML, /Old response/);
  // The heatmap rides along with every load but on its own request.
  assert.equal(activityPaths.length, 2);
  assert.match(activityPaths[0], /^\/api\/usage\/history\?bucket=day&tzOffset=-?\d+&from=\d{4}-\d{2}-\d{2}&to=\d{4}-\d{2}-\d{2}&limit=1$/);
});

test("controller renders safe failure state and refresh retries without routing away", async () => {
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

  host.click("open-task", "task-7");
  assert.deepEqual(navigations.at(-1), ["open-task", "task-7"]);
  host.click("refresh");
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(calls, 2);
  assert.deepEqual(navigations.at(-1), ["open-task", "task-7"]);
  assert.deepEqual(host.focusLog.at(-1), ["refresh", ""]);
  assert.equal(controller.getState().status, "ready");
  assert.match(host.innerHTML, /recovered/);
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
  assert.match(host.innerHTML, /old-data/);
  assert.equal(await controller.load({ force: true, preserveFocus: { action: "refresh", id: "" } }), false);
  const state = controller.getState();
  assert.equal(state.status, "error");
  assert.equal(state.payload.capturedAt, "old-data");
  assert.equal(state.error, "refresh failed");
  assert.match(host.innerHTML, /old-data/);
  assert.match(host.innerHTML, /refresh failed/);
  assert.deepEqual(host.focusLog.at(-1), ["refresh", ""]);
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

  host.click("open-conversation", "conversation-9");
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(errors, [["navigation failed", "open-conversation", "conversation-9"]]);
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
  assert.match(failed, /data-overview-section="continue-working"/);
  assert.doesNotMatch(failed, /data-overview-state="error"/);

  const empty = renderOverviewDashboard(overview(), { today: "2026-07-26" });
  assert.match(empty, /过去一年还没有使用记录。/);
});
