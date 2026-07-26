import test from "node:test";
import assert from "node:assert/strict";

import { automationLimits } from "./automation-control.mjs";
import { currentUILocale, setUILocale } from "./i18n.mjs";
import {
  createScheduleWorkspaceController,
  defaultScheduleTimezone,
  filterScheduleWorkspaceItems,
  formatScheduleOptionValue,
  normalizeScheduleConversations,
  renderScheduleNavigationHTML,
  renderScheduleWorkspace,
} from "./schedule-workspace.mjs";

function schedule(overrides = {}) {
  return {
    id: "schedule-1",
    name: "Nightly tests",
    agentId: "agent-1",
    expression: "@daily",
    timezone: "UTC",
    permissionMode: "readOnly",
    environmentMode: "workline",
    narratorMode: "reuse",
    prompt: "Run the test suite",
    enabled: true,
    nextRunAt: "2026-07-20T00:00:00Z",
    ...overrides,
  };
}

function conversations() {
  return [
    { agentId: "agent-1", agentTitle: "Planner", projectName: "Autoto", targetId: "project::main::agent-1" },
    { agentId: "agent-2", agentTitle: "Verifier", projectName: "Release", targetId: "project::release::agent-2" },
  ];
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function fakeRoot() {
  const listeners = new Map();
  return {
    addEventListener(type, listener) { listeners.set(type, listener); },
    contains() { return true; },
    dispatch(type, dataset, attributes = []) {
      const attributeSet = new Set(attributes);
      const trigger = {
        dataset,
        hasAttribute(name) { return attributeSet.has(name); },
        closest() { return this; },
      };
      listeners.get(type)?.({ target: trigger, preventDefault() {} });
    },
  };
}

test("conversation normalization is bounded, safe, and deduplicates agent ids", () => {
  const throwing = new Proxy({}, { get() { throw new Error("hostile getter"); } });
  const input = [
    { agentId: " agent-1 ", agentTitle: '<script>alert("x")</script>', projectName: 'P"><img src=x>', targetId: "p::w::a" },
    { agentId: "agent-1", agentTitle: "duplicate must be dropped" },
    { id: "agent-2", title: Symbol("safe"), projectTitle: "Project", targetId: 42 },
    throwing,
    null,
    ...Array.from({ length: 240 }, (_, index) => ({ agentId: `extra-${index}`, title: `Extra ${index}` })),
  ];
  const normalized = normalizeScheduleConversations(input);
  assert.equal(normalized.length, 200);
  assert.deepEqual(normalized[0], {
    agentId: "agent-1",
    title: '<script>alert("x")</script>',
    projectName: 'P"><img src=x>',
    targetId: "p::w::a",
  });
  assert.equal(normalized.filter((item) => item.agentId === "agent-1").length, 1);
  assert.equal(normalized[1].agentId, "agent-2");
  assert.equal(normalized[1].title, "Symbol(safe)");
});

test("navigation and detail rendering escape XSS and cap schedule and history lists", () => {
  const attack = '\"><img src=x onerror="boom">';
  const schedules = Array.from({ length: automationLimits.schedules + 5 }, (_, index) => schedule({
    id: `${attack}-${index}`,
    name: `<script>alert(${index})</script>`,
    agentId: index === 0 ? "agent-1" : attack,
    prompt: `<svg onload=boom>${index}`,
    expression: attack,
  }));
  const options = {
    conversations: [{ agentId: "agent-1", agentTitle: attack, projectName: "<iframe src=bad>" }],
    formatTimestamp: () => "<img src=time onerror=bad>",
  };
  const navigation = renderScheduleNavigationHTML({ loaded: true, schedules, selectedScheduleId: schedules[0].id }, options);
  assert.equal((navigation.match(/data-schedule-navigation=/g) || []).length, automationLimits.schedules);
  assert.doesNotMatch(navigation, /<script>|<img src=x|<iframe|<img src=time/);
  assert.match(navigation, /&lt;script&gt;alert\(0\)&lt;\/script&gt;/);
  assert.match(navigation, /data-schedule-navigation="&quot;&gt;&lt;img/);
  assert.match(navigation, /active enabled/);

  const detail = renderScheduleWorkspace({
    loaded: true,
    schedules: [schedules[0]],
    selectedScheduleId: schedules[0].id,
    history: {
      [schedules[0].id]: {
        loaded: true,
        runs: Array.from({ length: automationLimits.scheduleRuns + 4 }, (_, index) => ({
          id: `${attack}-${index}`,
          status: attack,
          triggerType: attack,
          createdAt: attack,
          durationMs: index,
        })),
      },
    },
  }, options);
  assert.equal((detail.match(/data-schedule-run-id=/g) || []).length, automationLimits.scheduleRuns);
  assert.doesNotMatch(detail, /<script>|<svg onload|<img src=x|<iframe|<img src=time/);
  assert.doesNotMatch(detail, /bypassPermissions/);
  assert.match(detail, /data-schedule-workspace-state="detail"/);
});

test("search covers schedule fields and linked conversation title and project", () => {
  const schedules = [
    schedule(),
    schedule({ id: "schedule-2", name: "Weekly release", agentId: "agent-2", expression: "0 2 * * 1", prompt: "Publish artifacts" }),
  ];
  const linked = conversations();
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "nightly").map((item) => item.id), ["schedule-1"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "publish artifacts").map((item) => item.id), ["schedule-2"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "0 2 *").map((item) => item.id), ["schedule-2"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "AGENT-1").map((item) => item.id), ["schedule-1"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "verifier").map((item) => item.id), ["schedule-2"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "release").map((item) => item.id), ["schedule-2"]);
  assert.deepEqual(filterScheduleWorkspaceItems(schedules, linked, "missing"), []);
});

test("new schedules use the browser IANA timezone and fall back safely", () => {
  assert.equal(defaultScheduleTimezone(() => ({ timeZone: "Asia/Taipei" })), "Asia/Taipei");
  assert.equal(defaultScheduleTimezone({ timeZone: "America/Los_Angeles" }), "America/Los_Angeles");
  assert.equal(defaultScheduleTimezone(() => ({ timeZone: '<script>alert("x")</script>' })), "UTC");
  assert.equal(defaultScheduleTimezone(() => { throw new Error("timezone unavailable"); }), "UTC");
  const hostile = new Proxy({}, { get() { throw new Error("hostile getter"); } });
  assert.equal(defaultScheduleTimezone(() => hostile), "UTC");

  const create = renderScheduleWorkspace(
    { loaded: true, mode: "create" },
    { conversations: conversations(), resolveTimezone: () => ({ timeZone: "America/Los_Angeles" }) },
  );
  assert.match(create, /<input name="timezone" maxlength="128" value="America\/Los_Angeles"/);
});

test("workspace renders loading, error, empty, create, and preserves a missing linked agent", () => {
  assert.match(renderScheduleWorkspace({ loading: true }), /data-schedule-workspace-state="loading"/);
  assert.match(renderScheduleWorkspace({ loaded: true, error: "broken" }), /data-schedule-workspace-state="error"/);
  assert.match(renderScheduleWorkspace({ loaded: true, schedules: [] }), /data-schedule-workspace-state="empty"/);

  const create = renderScheduleWorkspace({ loaded: true, mode: "create" }, { conversations: conversations(), activeAgentId: "agent-2" });
  assert.match(create, /data-schedule-workspace-state="create"/);
  assert.match(create, /<option value="agent-2" selected>/);
  assert.match(create, /<option value="readOnly" selected>只读（推荐）<\/option>/);
  assert.match(create, /<option value="acceptEdits">允许修改文件<\/option>/);
  assert.match(create, /<option value="workline" selected>关联项目工作区<\/option>/);
  assert.match(create, /<option value="reuse" selected>复用关联叙述者<\/option>/);
  assert.match(create, /class="schedule-execution-options span-2"/);
  assert.match(create, /data-schedule-capability-boundary="external-browser-tool-required"/);
  assert.match(create, /默认不可用：控制本机浏览器、点击或登录网页、播放音频或视频。/);
  assert.match(create, /浏览器自动化 MCP\/插件/);
  assert.match(create, /搜索 YouTube/);
  assert.match(create, /不要尝试播放/);
  assert.doesNotMatch(create, /bypassPermissions/);

  const missing = renderScheduleWorkspace({ loaded: true, schedules: [schedule({ agentId: "retired-agent" })], selectedScheduleId: "schedule-1" }, { conversations: conversations() });
  assert.match(missing, /<option value="retired-agent" selected>retired-agent<\/option>/);
  assert.match(missing, /data-schedule-open-conversation="retired-agent"/);
});

test("controller creates and updates normalized payloads at the expected endpoints", async () => {
  const calls = [];
  const controller = createScheduleWorkspaceController({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/api/schedules" && !options.method) return [schedule()];
      if (path === "/api/schedules" && options.method === "POST") return { schedule: schedule({ id: "created", name: "Created", agentId: "agent-2", expression: "@hourly", prompt: "Ship" }) };
      if (path === "/api/schedules/schedule-1" && options.method === "PATCH") return { schedule: schedule({ name: "Updated", permissionMode: "acceptEdits" }) };
      if (path.includes("/runs?")) return [];
      throw new Error(`unexpected request ${path}`);
    },
  });

  assert.equal(await controller.load({ autoHistory: false }), true);
  controller.startCreate();
  assert.equal(await controller.save({
    name: " Created ", agentId: "agent-2", expression: " @hourly ", timezone: " UTC ", prompt: " Ship ",
    permissionMode: "bypassPermissions", environmentMode: "standalone", narratorMode: "new",
  }), true);
  const createCall = calls.find((call) => call.options.method === "POST" && call.path === "/api/schedules");
  assert.deepEqual(JSON.parse(createCall.options.body), {
    name: "Created",
    agentId: "agent-2",
    expression: "@hourly",
    timezone: "UTC",
    prompt: "Ship",
    permissionMode: "readOnly",
    environmentMode: "standalone",
    narratorMode: "new",
    enabled: true,
  });
  assert.equal(controller.getState().selectedScheduleId, "created");

  await controller.select("schedule-1", { loadHistory: false });
  assert.equal(await controller.save({
    name: "Updated", agentId: "agent-1", expression: "@daily", timezone: "UTC", prompt: "Run updated tests",
    permissionMode: "acceptEdits", environmentMode: "workline", narratorMode: "reuse",
  }), true);
  const updateCall = calls.find((call) => call.path === "/api/schedules/schedule-1" && call.options.method === "PATCH");
  assert.deepEqual(JSON.parse(updateCall.options.body), {
    name: "Updated",
    agentId: "agent-1",
    expression: "@daily",
    timezone: "UTC",
    prompt: "Run updated tests",
    permissionMode: "acceptEdits",
    environmentMode: "workline",
    narratorMode: "reuse",
    enabled: true,
  });
  assert.equal(controller.getState().selectedScheduleId, "schedule-1");
});

test("toggle, run, delete, and history use mounted endpoints and deduplicate history requests", async () => {
  const calls = [];
  const historyDeferred = deferred();
  let confirms = 0;
  const controller = createScheduleWorkspaceController({
    confirmAction: () => { confirms += 1; return true; },
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/api/schedules") return [schedule(), schedule({ id: "schedule-2", agentId: "agent-2" })];
      if (path === "/api/schedules/schedule-1/runs?limit=20") return historyDeferred.promise;
      if (path === "/api/schedules/schedule-1" && options.method === "PATCH") return {};
      if (path === "/api/schedules/schedule-1/run" && options.method === "POST") return {};
      if (path === "/api/schedules/schedule-1" && options.method === "DELETE") return {};
      if (path === "/api/schedules/schedule-2/runs?limit=20") return [];
      throw new Error(`unexpected request ${path}`);
    },
  });

  await controller.load({ autoHistory: false });
  const firstHistory = controller.loadHistory("schedule-1");
  const duplicateHistory = controller.loadHistory("schedule-1");
  assert.equal(firstHistory, duplicateHistory);
  assert.equal(calls.filter((call) => call.path === "/api/schedules/schedule-1/runs?limit=20").length, 1);
  historyDeferred.resolve({ runs: [{ id: "run-1", status: "succeeded", triggerType: "manual", durationMs: 12 }] });
  assert.equal(await firstHistory, true);
  assert.equal(controller.getState().history["schedule-1"].runs[0].id, "run-1");

  assert.equal(await controller.toggle("schedule-1", false), true);
  assert.deepEqual(JSON.parse(calls.find((call) => call.path === "/api/schedules/schedule-1" && call.options.method === "PATCH").options.body), { enabled: false });
  assert.equal(await controller.run("schedule-1"), true);
  assert.ok(calls.some((call) => call.path === "/api/schedules/schedule-1/run" && call.options.method === "POST"));
  assert.equal(await controller.delete("schedule-1"), true);
  assert.equal(confirms, 1);
  assert.ok(calls.some((call) => call.path === "/api/schedules/schedule-1" && call.options.method === "DELETE"));
  assert.equal(controller.getState().selectedScheduleId, "schedule-2");
});

test("load sequence discards stale responses", async () => {
  const oldResponse = deferred();
  const newResponse = deferred();
  let calls = 0;
  const controller = createScheduleWorkspaceController({
    request: (path) => {
      assert.equal(path, "/api/schedules");
      calls += 1;
      return calls === 1 ? oldResponse.promise : newResponse.promise;
    },
  });
  const oldLoad = controller.load({ autoHistory: false });
  const newLoad = controller.load({ autoHistory: false });
  newResponse.resolve([schedule({ id: "new", name: "New response" })]);
  assert.equal(await newLoad, true);
  oldResponse.resolve([schedule({ id: "old", name: "Old response" })]);
  assert.equal(await oldLoad, false);
  assert.deepEqual(controller.getState().schedules.map((item) => item.id), ["new"]);
  assert.equal(controller.getState().loading, false);
});

test("bind delegates creation, selection, and linked conversation actions", async () => {
  const root = fakeRoot();
  const opened = [];
  const controller = createScheduleWorkspaceController({ request: async () => [] });
  assert.equal(controller.bind(root, { onOpenConversation: (agentId) => opened.push(agentId) }), true);
  assert.equal(controller.bind(root), false);

  root.dispatch("click", { scheduleCreate: "" }, ["data-schedule-create"]);
  assert.equal(controller.getState().mode, "create");
  root.dispatch("input", {}, []);
  root.dispatch("click", { scheduleOpenConversation: "agent-1" });
  assert.deepEqual(opened, ["agent-1"]);
});

test("workspace copy and schedule enum values render in Simplified Chinese, Traditional Chinese, and English", () => {
  const previous = currentUILocale();
  try {
    for (const [locale, labels] of [
      ["zh-CN", { title: "排程工作区", create: "创建排程", capability: "排程能力范围", browserLimit: "默认不可用：控制本机浏览器", readOnly: "只读（推荐）", acceptEdits: "允许修改文件", workline: "关联项目工作区", standalone: "独立工作区", reuse: "复用关联叙述者", newNarrator: "每次运行新建叙述者", unknown: "未知选项（futureMode）" }],
      ["zh-TW", { title: "排程工作區", create: "建立排程", capability: "排程能力範圍", browserLimit: "預設不可用：控制本機瀏覽器", readOnly: "唯讀（建議）", acceptEdits: "允許修改檔案", workline: "關聯專案工作區", standalone: "獨立工作區", reuse: "重用關聯敘述者", newNarrator: "每次執行新建敘述者", unknown: "未知選項（futureMode）" }],
      ["en", { title: "Schedule workspace", create: "Create schedule", capability: "Schedule capability boundary", browserLimit: "Not built in: control the local browser", readOnly: "Read only (recommended)", acceptEdits: "Allow file edits", workline: "Linked project workspace", standalone: "Standalone workspace", reuse: "Reuse linked narrator", newNarrator: "Create a narrator for each run", unknown: "Unknown option (futureMode)" }],
    ]) {
      setUILocale(locale);
      const navigation = renderScheduleNavigationHTML({ loaded: true, schedules: [schedule()] }, { conversations: conversations() });
      const create = renderScheduleWorkspace({ loaded: true, mode: "create" }, { conversations: conversations() });
      const detail = renderScheduleWorkspace({ loaded: true, schedules: [schedule({ permissionMode: "acceptEdits", environmentMode: "standalone", narratorMode: "new" })], selectedScheduleId: "schedule-1" }, { conversations: conversations() });
      assert.ok(navigation.includes(labels.title), locale);
      assert.ok(create.includes(labels.create), locale);
      assert.ok(create.includes(labels.capability), locale);
      assert.ok(create.includes(labels.browserLimit), locale);
      assert.match(create, /@every 15m/, locale);
      assert.ok(create.includes(`<option value="readOnly" selected>${labels.readOnly}</option>`), `${locale}:readOnly`);
      assert.ok(create.includes(`<option value="acceptEdits">${labels.acceptEdits}</option>`), `${locale}:acceptEdits`);
      assert.ok(create.includes(`<option value="workline" selected>${labels.workline}</option>`), `${locale}:workline`);
      assert.ok(create.includes(`<option value="standalone">${labels.standalone}</option>`), `${locale}:standalone`);
      assert.ok(create.includes(`<option value="reuse" selected>${labels.reuse}</option>`), `${locale}:reuse`);
      assert.ok(create.includes(`<option value="new">${labels.newNarrator}</option>`), `${locale}:new`);
      assert.ok(detail.includes(`<dd>${labels.acceptEdits}</dd>`), `${locale}:permission summary`);
      assert.ok(detail.includes(`<dd>${labels.standalone}</dd>`), `${locale}:environment summary`);
      assert.ok(detail.includes(`<dd>${labels.newNarrator}</dd>`), `${locale}:narrator summary`);
      assert.doesNotMatch(create, />(?:readOnly|acceptEdits|workline|standalone|reuse|new)</, locale);
      assert.doesNotMatch(detail, /<dd>(?:readOnly|acceptEdits|workline|standalone|reuse|new)<\/dd>/, locale);
      assert.equal(formatScheduleOptionValue("permissionMode", "futureMode"), labels.unknown, `${locale}:unknown`);
    }
  } finally {
    setUILocale(previous);
  }
});
