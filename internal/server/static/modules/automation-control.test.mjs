import test from "node:test";
import assert from "node:assert/strict";

import { currentUILocale, setUILocale } from "./i18n.mjs";
import { getRegionalPreferences, setRegionalPreferences } from "./locale-registry.mjs";
import {
  automationLimits,
  buildConnectionPayload,
  buildDeviceActionPayload,
  buildPairingCodePayload,
  buildSchedulePayload,
  createAutomationControlController,
  describeCronExpression,
  formatScheduleEnumValue,
  normalizeConnection,
  normalizeSchedule,
  normalizeScheduleRun,
  readLegacyIMDraft,
  renderAutomationControl,
  requireLocalDoubleConfirmation,
} from "./automation-control.mjs";

class MemoryStorage {
  constructor(entries = []) {
    this.values = new Map(entries);
  }

  getItem(key) {
    return this.values.has(key) ? this.values.get(key) : null;
  }
}

function emptyAPI(path) {
  if (path === "/api/monitoring/snapshot") return {};
  return [];
}

test("automation rendering escapes all dynamic content and bounds rendered collections", () => {
  const malicious = '\"><img src=x onerror="boom">';
  const html = renderAutomationControl({
    loaded: true,
    schedules: Array.from({ length: automationLimits.schedules + 5 }, (_, index) => ({
      id: `${malicious}-${index}`,
      name: `<script>alert(${index})</script>`,
      expression: malicious,
      agentId: malicious,
      prompt: malicious,
      enabled: true,
    })),
    deliveries: [{ id: malicious, event: malicious, channel: malicious, error: `<svg onload=boom>` }],
    connections: [{ id: malicious, kind: "telegram", name: malicious, secretConfigured: { botToken: true } }],
    pairings: [{ id: malicious, agentId: malicious, channelUser: malicious }],
    devices: [{ entityId: malicious, name: malicious, state: malicious }],
    deviceActions: [{ id: malicious, entityId: malicious, domain: malicious, service: malicious, status: "pending" }],
    auditEvents: [{ id: malicious, type: malicious, actor: malicious, summary: malicious }],
    errors: { global: `<script>error</script>` },
  });

  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /<img src=x/);
  assert.doesNotMatch(html, /<svg onload/);
  assert.match(html, /&lt;script&gt;alert\(0\)&lt;\/script&gt;/);
  assert.equal((html.match(/data-schedule-card=/g) || []).length, automationLimits.schedules);
  assert.match(html, /data-automation-section="schedules"/);
  assert.match(html, /settings-page-section/);
  assert.match(html, /settings-stat-grid/);
  assert.match(html, /settings-table/);
});

test("automation timestamps use the shared UTC regional formatter and explicit invalid fallback", () => {
  const previous = getRegionalPreferences();
  try {
    setRegionalPreferences({ locale: "en-US", timezone: "UTC" });
    const html = renderAutomationControl({
      loaded: true,
      schedules: [{
        id: "valid",
        name: "valid",
        expression: "* * * * *",
        timezone: "UTC",
        agentId: "agent",
        prompt: "run",
        enabled: true,
        nextRunAt: "2025-01-02T03:04:05Z",
      }, {
        id: "invalid",
        name: "invalid",
        expression: "* * * * *",
        timezone: "UTC",
        agentId: "agent",
        prompt: "run",
        enabled: true,
        nextRunAt: "not-a-date",
      }],
    });
    assert.match(html, /01\/02\/2025, 03:04:05/);
    assert.match(html, /无效日期/);
  } finally {
    setRegionalPreferences(previous);
  }
});

test("legacy localStorage IM draft is an explicitly disabled migration hint, never runtime authority", async () => {
  const storage = new MemoryStorage([["autoto.imGateway", JSON.stringify({ enabled: true, channel: "telegram" })]]);
  const legacy = readLegacyIMDraft(storage);
  assert.deepEqual(legacy, {
    present: true,
    key: "autoto.imGateway",
    channel: "telegram",
    authoritative: false,
    status: "disabled",
  });

  const controller = createAutomationControlController({
    storage,
    request: async (path) => emptyAPI(path),
  });
  assert.equal(await controller.load(), true);
  const state = controller.getState();
  assert.equal(state.legacyDraft.present, true);
  assert.equal(state.legacyDraft.authoritative, false);
  assert.equal(state.monitoring.channelCount, 0);
  assert.equal(state.connections.length, 0);

  const html = controller.render();
  assert.match(html, /旧草稿已停用/);
  assert.match(html, /绝不计入运行状态/);
  assert.match(html, /data-authority="server-api"/);
});

test("payload helpers send only supported normalized fields and require env references", () => {
  assert.deepEqual(buildSchedulePayload({
    name: "  nightly  ",
    cron: " 0 2 * * * ",
    agentId: " agent-1 ",
    timezone: " Asia/Shanghai ",
    prompt: " run tests ",
    permissionMode: "bypassPermissions",
    enabled: 1,
    ignored: "nope",
  }), {
    name: "nightly",
    agentId: "agent-1",
    expression: "0 2 * * *",
    timezone: "Asia/Shanghai",
    prompt: "run tests",
    permissionMode: "readOnly",
    environmentMode: "workline",
    narratorMode: "reuse",
    enabled: true,
  });

  assert.deepEqual(buildConnectionPayload({
    kind: "telegram",
    name: " Ops ",
    credentialRef: "env:AUTOTO_TELEGRAM_BOT_TOKEN",
    token: "must-not-send",
  }), {
    kind: "telegram",
    name: "Ops",
    enabled: true,
    endpoint: "",
    settings: {},
    secretRefs: { botToken: "env:AUTOTO_TELEGRAM_BOT_TOKEN" },
  });
  assert.deepEqual(buildConnectionPayload({
    kind: "home-assistant",
    name: " Home ",
    endpoint: "http://homeassistant.local:8123/",
    credentialRef: "env:AUTOTO_HOME_ASSISTANT_TOKEN",
  }), {
    kind: "home-assistant",
    name: "Home",
    enabled: true,
    endpoint: "http://homeassistant.local:8123",
    settings: {},
    secretRefs: { accessToken: "env:AUTOTO_HOME_ASSISTANT_TOKEN" },
  });
  assert.throws(() => buildConnectionPayload({ kind: "telegram", credentialRef: "123456:plaintext-token" }), /禁止输入 token/);
  assert.deepEqual(buildPairingCodePayload({ connectionId: " conn ", agentId: " agent " }), { connectionId: "conn", agentId: "agent" });
  assert.deepEqual(buildDeviceActionPayload({
    connectionId: "ha-1",
    entityId: "light.office",
    service: "turn_on",
    input: '{"brightness":2}',
    source: "im",
  }), {
    connectionId: "ha-1",
    domain: "light",
    service: "turn_on",
    input: { brightness: 2, entity_id: "light.office" },
  });
});

test("dangerous device action requires two local confirmations before POST", async () => {
  const calls = [];
  const confirmations = [];
  const controller = createAutomationControlController({
    request: async (path, options = {}) => {
      calls.push({ path, options });
      return { id: "action-1", status: "pending", risk: "high", expiresAt: "2099-01-01T00:00:00Z" };
    },
    confirmAction: (message) => {
      confirmations.push(message);
      return true;
    },
  });

  assert.equal(await controller.requestDeviceAction({
    connectionId: "ha-1",
    entityId: "switch.heater",
    service: "turn_on",
    input: {},
  }), true);
  assert.equal(confirmations.length, 2);
  assert.match(confirmations[0], /本地 Web UI/);
  assert.match(confirmations[0], /IM 无法触发/);
  assert.match(confirmations[1], /真实设备状态/);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].path, "/api/device-actions");
  assert.deepEqual(JSON.parse(calls[0].options.body), {
    connectionId: "ha-1",
    domain: "switch",
    service: "turn_on",
    input: { entity_id: "switch.heater" },
  });
});

test("double confirmation cancellation prevents the dangerous request", async () => {
  let confirmations = 0;
  let requests = 0;
  const controller = createAutomationControlController({
    request: async () => { requests += 1; return {}; },
    confirmAction: () => {
      confirmations += 1;
      return confirmations < 2;
    },
  });

  assert.equal(await controller.requestDeviceAction({
    connectionId: "ha-1",
    entityId: "lock.front_door",
    service: "unlock",
  }), false);
  assert.equal(confirmations, 2);
  assert.equal(requests, 0);
  assert.equal(await requireLocalDoubleConfirmation(() => false), false);
});

test("controller sequence drops stale refresh responses", async () => {
  const scheduleResolvers = [];
  const request = (path) => {
    if (path === "/api/schedules") return new Promise((resolve) => scheduleResolvers.push(resolve));
    return Promise.resolve(emptyAPI(path));
  };
  const controller = createAutomationControlController({ request });

  const older = controller.load();
  const newer = controller.load();
  scheduleResolvers[1]([{ id: "new", name: "new response", expression: "* * * * *", timezone: "UTC", agentId: "a", prompt: "new" }]);
  assert.equal(await newer, true);
  scheduleResolvers[0]([{ id: "old", name: "stale response", expression: "* * * * *", timezone: "UTC", agentId: "a", prompt: "old" }]);
  assert.equal(await older, false);

  assert.deepEqual(controller.getState().schedules.map((item) => item.id), ["new"]);
  assert.equal(controller.getState().loading, false);
});

test("connection normalization and rendering never echo returned secret fields", () => {
  const normalized = normalizeConnection({
    id: "tg-1",
    kind: "telegram",
    name: "Telegram",
    secretConfigured: { botToken: true },
    credentialRef: "env:PRIVATE_RUNTIME_TOKEN",
    token: "123456:abcdefghijklmnopqrstuvwxyzABCDE",
    secret: "server-secret-value",
    config: { botToken: "nested-secret-value" },
  });
  assert.equal(Object.hasOwn(normalized, "token"), false);
  assert.equal(Object.hasOwn(normalized, "secret"), false);
  assert.equal(Object.hasOwn(normalized, "credentialRef"), false);
  assert.equal(normalized.credentialConfigured, true);

  const html = renderAutomationControl({
    loaded: true,
    connections: [{
      ...normalized,
      token: "123456:abcdefghijklmnopqrstuvwxyzABCDE",
      secret: "server-secret-value",
    }],
    auditEvents: [{ type: "connection.test", summary: "token=123456:abcdefghijklmnopqrstuvwxyzABCDE" }],
    errors: { global: "Bearer top-secret-bearer-value" },
    // Force both credential forms to render so the no-echo assertions below
    // exercise real inputs rather than passing vacuously on a collapsed form.
    formOpen: { telegram: true, homeAssistant: true },
  });
  assert.doesNotMatch(html, /abcdefghijklmnopqrstuvwxyzABCDE/);
  assert.doesNotMatch(html, /server-secret-value/);
  assert.doesNotMatch(html, /top-secret-bearer-value/);
  assert.doesNotMatch(html, /env:PRIVATE_RUNTIME_TOKEN/);
  assert.match(html, /引用目标与 secret 均不回显/);
  assert.match(html, /\[REDACTED\]/);
  assert.doesNotMatch(html, /telegramCredentialInput[^>]+value=/);
  assert.doesNotMatch(html, /homeAssistantCredentialInput[^>]+value=/);
});

test("schedule modes normalize and run history uses the mounted endpoint", async () => {
  assert.deepEqual(normalizeSchedule({ environmentMode: "standalone", narratorMode: "new" }), {
    id: "", name: "", expression: "", timezone: "UTC", agentId: "", prompt: "", permissionMode: "readOnly",
    environmentMode: "standalone", narratorMode: "new", enabled: false, status: "ready", nextRunAt: "", lastRunAt: "",
    lastRunId: "", lastOutcome: "", lastError: "",
  });
  assert.deepEqual(normalizeScheduleRun({ id: "run-1", triggerType: "scheduled", durationMs: 42, status: "completed" }), {
    id: "run-1", triggerType: "scheduled", durationMs: 42, status: "completed", createdAt: "",
  });
  const calls = [];
  const controller = createAutomationControlController({
    request: async (path) => {
      calls.push(path);
      if (path.includes("/runs?")) return [{ id: "run-1", triggerType: "scheduled", durationMs: 42, status: "completed" }];
      return emptyAPI(path);
    },
  });
  assert.equal(await controller.loadScheduleRuns("schedule-1"), true);
  assert.equal(calls[0], `/api/schedules/schedule-1/runs?limit=${automationLimits.scheduleRuns}`);
  assert.match(controller.render(), /scheduled/);
  assert.match(controller.render(), /42 ms/);
});

test("automation renders localized schedule enum options, summaries, and safe unknown fallbacks", () => {
  const previous = currentUILocale();
  try {
    for (const [locale, labels] of [
      ["zh-CN", { title: "受限权限后台任务", readOnly: "只读（推荐）", acceptEdits: "允许修改文件", workline: "关联项目工作区", standalone: "独立工作区", reuse: "复用关联叙述者", newNarrator: "每次运行新建叙述者", unknown: "未知选项（futureMode）", escapedUnknown: "未知选项（future&lt;mode&gt;）" }],
      ["zh-TW", { title: "受限權限背景任務", readOnly: "唯讀（建議）", acceptEdits: "允許修改檔案", workline: "關聯專案工作區", standalone: "獨立工作區", reuse: "重用關聯敘述者", newNarrator: "每次執行新建敘述者", unknown: "未知選項（futureMode）", escapedUnknown: "未知選項（future&lt;mode&gt;）" }],
      ["en", { title: "Restricted background tasks", readOnly: "Read only (recommended)", acceptEdits: "Allow file edits", workline: "Linked project workspace", standalone: "Standalone workspace", reuse: "Reuse linked narrator", newNarrator: "Create a narrator for each run", unknown: "Unknown option (futureMode)", escapedUnknown: "Unknown option (future&lt;mode&gt;)" }],
    ]) {
      setUILocale(locale);
      const html = renderAutomationControl({
        loaded: true,
        schedules: [
          { id: "known", name: "Known", expression: "@daily", timezone: "UTC", agentId: "agent", prompt: "run", permissionMode: "acceptEdits", environmentMode: "standalone", narratorMode: "new", enabled: true },
          { id: "future", name: "Future", expression: "@hourly", timezone: "UTC", agentId: "agent", prompt: "run", permissionMode: "future<mode>", environmentMode: "workline", narratorMode: "reuse", enabled: true },
        ],
        // The schedule section has rows, so its create form only renders when
        // opened explicitly; the option assertions below live in that form.
        formOpen: { schedule: true },
      });
      assert.ok(html.includes(labels.title), locale);
      assert.match(html, /placeholder="@every 15m"/, locale);
      assert.match(html, /env:AUTOTO_TELEGRAM_BOT_TOKEN/, locale);
      assert.match(html, /light\.living_room/, locale);
      assert.ok(html.includes(`<option value="readOnly" selected>${labels.readOnly}</option>`), `${locale}:readOnly`);
      assert.ok(html.includes(`<option value="acceptEdits">${labels.acceptEdits}</option>`), `${locale}:acceptEdits`);
      assert.ok(html.includes(`<option value="workline" selected>${labels.workline}</option>`), `${locale}:workline`);
      assert.ok(html.includes(`<option value="standalone">${labels.standalone}</option>`), `${locale}:standalone`);
      assert.ok(html.includes(`<option value="reuse" selected>${labels.reuse}</option>`), `${locale}:reuse`);
      assert.ok(html.includes(`<option value="new">${labels.newNarrator}</option>`), `${locale}:new`);
      assert.ok(html.includes(`<dd>${labels.acceptEdits}</dd>`), `${locale}:permission summary`);
      assert.ok(html.includes(`<dd>${labels.standalone}</dd>`), `${locale}:environment summary`);
      assert.ok(html.includes(`<dd>${labels.newNarrator}</dd>`), `${locale}:narrator summary`);
      assert.ok(html.includes(`<dd>${labels.escapedUnknown}</dd>`), `${locale}:escaped unknown summary`);
      assert.doesNotMatch(html, />(?:readOnly|acceptEdits|workline|standalone|reuse|new)</, locale);
      assert.doesNotMatch(html, /<dd>(?:readOnly|acceptEdits|workline|standalone|reuse|new)<\/dd>/, locale);
      assert.doesNotMatch(html, /<mode>/, locale);
      assert.equal(formatScheduleEnumValue("permissionMode", "futureMode"), labels.unknown, `${locale}:unknown`);
    }
  } finally {
    setUILocale(previous);
  }
});

test("cron preview estimates presets and pinned five-field crons, and refuses the rest", () => {
  const now = Date.parse("2026-08-12T10:15:30Z");
  assert.deepEqual(describeCronExpression("@every 30m", now), { valid: true, nextAt: new Date(now + 30 * 60000).toISOString() });
  const hourly = describeCronExpression("@hourly", now);
  assert.equal(hourly.valid, true);
  assert.equal(new Date(hourly.nextAt).getMinutes(), 0);
  assert.ok(Date.parse(hourly.nextAt) > now);
  assert.ok(Date.parse(hourly.nextAt) - now <= 3600000);
  const daily = describeCronExpression("@daily", now);
  assert.equal(new Date(daily.nextAt).getHours(), 0);
  assert.ok(Date.parse(daily.nextAt) > now);
  const weekly = describeCronExpression("@weekly", now);
  assert.equal(new Date(weekly.nextAt).getDay(), 0);
  // The five-field estimate runs in the browser's local clock, so the
  // assertions read the result back with local getters.
  const monday = describeCronExpression("0 9 * * 1", now);
  assert.equal(monday.valid, true);
  assert.equal(new Date(monday.nextAt).getDay(), 1);
  assert.equal(new Date(monday.nextAt).getHours(), 9);
  assert.equal(new Date(monday.nextAt).getMinutes(), 0);
  const nightly = describeCronExpression("0 22 * * *", now);
  assert.equal(nightly.valid, true);
  assert.equal(new Date(nightly.nextAt).getHours(), 22);
  for (const unsupported of ["", "not-cron", "* * * * *", "*/5 * * * *", "0 9 * * mon", "@every 0m", "61 9 * * *"]) {
    assert.equal(describeCronExpression(unsupported, now).valid, false, unsupported);
  }
});

test("feature pillars summarize configured areas and expose CTAs into the sections", () => {
  const configured = renderAutomationControl({
    loaded: true,
    connections: [{ id: "tg-1", kind: "telegram", name: "TG", secretConfigured: { botToken: true } }],
    pairings: [{ id: "p-1", connectionId: "tg-1", agentId: "agent-1", status: "active" }],
    schedules: [{ id: "s-1", name: "Nightly", expression: "@daily", agentId: "agent-1", prompt: "run", enabled: true }],
  });
  assert.equal((configured.match(/data-feature-card=/g) || []).length, 3);
  // Configured areas offer a plain jump; the unconfigured one also opens its form.
  assert.match(configured, /data-feature-cta="telegram" data-feature-target="connections">/);
  assert.match(configured, /data-feature-cta="schedule" data-feature-target="schedules">/);
  assert.match(configured, /data-feature-cta="homeAssistant" data-feature-target="home-assistant" data-feature-open="true">/);
  assert.match(configured, /data-automation-section="connections"/);
  assert.match(configured, /data-automation-section="home-assistant"/);
  assert.match(configured, /data-metric-jump="device-actions"/);
  // Every metric card is a jump button now.
  assert.equal((configured.match(/data-metric-jump=/g) || []).length >= 6, true);
});

test("pending device actions pin an approval banner unless they are expired", () => {
  const base = {
    loaded: true,
    deviceActions: [{ id: "act-1", entityId: "light.desk", domain: "light", service: "turn_on", status: "pending", expiresAt: "2099-01-01T00:00:00Z" }],
    now: Date.parse("2026-01-01T00:00:00Z"),
  };
  const pinned = renderAutomationControl(base);
  assert.match(pinned, /data-automation-pending/);
  const expired = renderAutomationControl({
    ...base,
    deviceActions: [{ ...base.deviceActions[0], expiresAt: "2020-01-01T00:00:00Z" }],
  });
  assert.doesNotMatch(expired, /data-automation-pending/);
  const approvedOnly = renderAutomationControl({
    ...base,
    deviceActions: [{ ...base.deviceActions[0], status: "approved" }],
  });
  assert.doesNotMatch(approvedOnly, /data-automation-pending/);
});

test("create forms collapse behind toggles once a section has data and auto-open when empty", () => {
  const populated = renderAutomationControl({
    loaded: true,
    schedules: [{ id: "s-1", name: "Nightly", expression: "@daily", agentId: "agent-1", prompt: "run", enabled: true }],
    connections: [
      { id: "tg-1", kind: "telegram", name: "TG", secretConfigured: { botToken: true } },
      { id: "ha-1", kind: "home-assistant", name: "Home", endpoint: "http://ha.local:8123", secretConfigured: { accessToken: true } },
    ],
    pairings: [{ id: "p-1", connectionId: "tg-1", agentId: "agent-1", status: "active" }],
    deviceActions: [{ id: "act-1", entityId: "light.desk", domain: "light", service: "turn_on", status: "approved" }],
  });
  for (const formId of ["createScheduleForm", "createTelegramConnectionForm", "createHomeAssistantConnectionForm", "createPairingCodeForm", "createDeviceActionForm"]) {
    assert.doesNotMatch(populated, new RegExp(formId), formId);
  }
  assert.match(populated, /data-form-toggle="schedule" data-form-open="false"/);

  const empty = renderAutomationControl({ loaded: true });
  for (const formId of ["createScheduleForm", "createTelegramConnectionForm", "createHomeAssistantConnectionForm", "createPairingCodeForm", "createDeviceActionForm"]) {
    assert.match(empty, new RegExp(formId), formId);
  }

  const explicitlyOpened = renderAutomationControl({
    loaded: true,
    schedules: [{ id: "s-1", name: "Nightly", expression: "@daily", agentId: "agent-1", prompt: "run", enabled: true }],
    formOpen: { schedule: true },
  });
  assert.match(explicitlyOpened, /createScheduleForm/);
  assert.match(explicitlyOpened, /data-form-toggle="schedule" data-form-open="true"/);

  const explicitlyClosed = renderAutomationControl({ loaded: true, formOpen: { schedule: false } });
  assert.doesNotMatch(explicitlyClosed, /createScheduleForm/);
});

test("agent pickers use the loaded agent directory and fall back to plain inputs", () => {
  const withAgents = renderAutomationControl({
    loaded: true,
    agents: [{ id: "agent-12345678", title: "Nightly bot" }, { id: "agent-plain" }],
    formOpen: { schedule: true, pairing: true },
  });
  assert.match(withAgents, /<select id="scheduleAgentInput" required>/);
  assert.match(withAgents, /<select id="pairingAgentInput" required>/);
  assert.match(withAgents, /<option value="agent-12345678">Nightly bot · agent-12<\/option>/);
  assert.match(withAgents, /<option value="agent-plain">agent-plain<\/option>/);

  const withoutAgents = renderAutomationControl({ loaded: true, formOpen: { schedule: true, pairing: true } });
  assert.match(withoutAgents, /<input id="scheduleAgentInput"/);
  assert.match(withoutAgents, /<input id="pairingAgentInput"/);
});

test("schedule cards report the last run outcome or say the schedule never ran", () => {
  const html = renderAutomationControl({
    loaded: true,
    schedules: [
      { id: "ran", name: "Ran", expression: "@daily", agentId: "a", prompt: "p", enabled: true, lastRunAt: "2026-01-02T03:04:05Z", lastOutcome: "succeeded" },
      { id: "fresh", name: "Fresh", expression: "@daily", agentId: "a", prompt: "p", enabled: true },
    ],
  });
  assert.match(html, /上次运行/);
  assert.match(html, /成功 · /);
  assert.match(html, /从未运行/);
});

test("entities group by domain with search metadata and per-row action shortcuts", () => {
  const html = renderAutomationControl({
    loaded: true,
    connections: [{ id: "ha-1", kind: "home-assistant", name: "Home", endpoint: "http://ha.local:8123", secretConfigured: { accessToken: true } }],
    selectedConnectionId: "ha-1",
    devices: [
      { entityId: "light.desk", name: "Desk lamp", state: "on" },
      { entityId: "switch.heater", name: "Heater", state: "off" },
      { entityId: "light.hall", name: "Hall", state: "off" },
    ],
    deviceFilter: "desk",
  });
  assert.equal((html.match(/automation-device-group-head/g) || []).length, 2);
  assert.match(html, /data-entity-search="light\.desk desk lamp"/);
  assert.equal((html.match(/data-entity-action=/g) || []).length, 3);
  assert.match(html, /id="deviceSearchInput" maxlength="120" value="desk"/);
  // Without a selected connection there is nothing to prefill, so no shortcut.
  const unselected = renderAutomationControl({
    loaded: true,
    devices: [{ entityId: "light.desk", name: "Desk lamp", state: "on" }],
  });
  assert.doesNotMatch(unselected, /data-entity-action=/);
});

test("controller loads the agent directory for the pickers and tolerates its failure", async () => {
  const controller = createAutomationControlController({
    request: async (path) => {
      if (path === "/api/agents?limit=200") return [{ id: "agent-1", title: "Alpha" }, { id: "", title: "dropped: no id" }];
      return emptyAPI(path);
    },
  });
  assert.equal(await controller.load(), true);
  assert.deepEqual(controller.getState().agents, [{ id: "agent-1", title: "Alpha" }]);
  assert.match(controller.render(), /<select id="scheduleAgentInput" required>/);

  const failing = createAutomationControlController({
    request: async (path) => {
      if (path.startsWith("/api/agents")) throw new Error("agents endpoint down");
      return emptyAPI(path);
    },
  });
  assert.equal(await failing.load(), true);
  assert.deepEqual(failing.getState().agents, []);
  assert.match(failing.render(), /<input id="scheduleAgentInput"/);
});

test("the Telegram trio shares one row: connections, pairing, then deliveries", () => {
  const html = renderAutomationControl({ loaded: true });
  const trio = html.match(/<div class="automation-trio" data-automation-trio>([\s\S]*?)<\/div>\s*<section class="automation-section settings-card settings-page-section span-2" data-automation-section="home-assistant"/);
  assert.ok(trio, "trio wrapper renders before the home-assistant section");
  const order = ["connections", "pairing", "deliveries"].map((slug) => trio[1].indexOf(`data-automation-section="${slug}"`));
  assert.ok(order.every((index) => index >= 0), "all three sections live inside the trio");
  assert.deepEqual([...order].sort((a, b) => a - b), order, "sections follow the connect, pair, deliver flow");
});

test("a successful create collapses the section form again", async () => {
  const controller = createAutomationControlController({ request: async (path) => emptyAPI(path) });
  assert.equal(await controller.createSchedule({ name: "n", expression: "@daily", agentId: "agent-1", prompt: "p" }), true);
  assert.equal(controller.getState().formOpen.schedule, false);
  assert.equal(await controller.createConnection({ kind: "telegram", credentialRef: "env:AUTOTO_TELEGRAM_BOT_TOKEN" }), true);
  assert.equal(controller.getState().formOpen.telegram, false);
});
