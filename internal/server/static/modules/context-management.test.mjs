import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";

import {
  contextRingStyle,
  contextSettingsPayload,
  contextUsagePercentage,
  contextUsageTone,
  createContextManagementController,
  defaultContextSettings,
  normalizeContextSettings,
  normalizeContextStatus,
  validateContextSettings,
} from "./context-management.mjs";

const staticRoot = new URL("../", import.meta.url);
const indexURL = new URL("index.html", staticRoot);
const appURL = new URL("app.js", staticRoot);
const appMainURL = new URL("modules/app-main.mjs", staticRoot);
const uiShellURL = new URL("modules/ui-shell.mjs", staticRoot);
const i18nURL = new URL("modules/i18n.mjs", staticRoot);
const stylesURL = new URL("styles.css", staticRoot);

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function browserStubs() {
  return {
    document: {
      activeElement: null,
      body: { style: {} },
      documentElement: { lang: "en" },
      getElementById() { return null; },
      addEventListener() {},
      removeEventListener() {},
    },
    window: {
      innerWidth: 1200,
      innerHeight: 800,
      matchMedia() { return { matches: false }; },
      addEventListener() {},
      removeEventListener() {},
    },
  };
}

test("context status normalizes token usage, preferences, and standard/large thresholds", () => {
  assert.equal(contextUsagePercentage({ estimatedTokens: 475000, limit: 500000 }), 95);
  assert.equal(contextUsagePercentage({ percentage: 0.99 }), 99);
  assert.equal(contextUsagePercentage({ usagePercent: 1 }), 1);
  assert.equal(contextUsagePercentage({ usagePercent: 100 }), 100);
  assert.equal(contextUsagePercentage({ estimatedTokens: 10, limit: 0 }), null);

  const standard = normalizeContextStatus({
    estimatedTokens: 475000,
    limitTokens: 500000,
    usagePercent: 95,
    pruneEnabled: false,
    estimated: true,
    thresholds: { pruneStartPercent: 95, compactStartPercent: 99, minPrunePercent: 30, maxPrunePercent: 80, keepTurns: 2 },
  }, { agentId: "agent-standard" });
  assert.equal(standard.agentId, "agent-standard");
  assert.equal(standard.known, true);
  assert.equal(standard.percentage, 95);
  assert.equal(standard.windowKind, "standard");
  assert.equal(standard.pruneStart, 95);
  assert.equal(standard.compactStart, 99);
  assert.equal(standard.autoPrune, false);
  assert.equal(contextUsageTone(standard), "warning");

  const large = normalizeContextStatus({
    estimatedTokens: 693000,
    limitTokens: 700000,
    usagePercent: 99,
    windowClass: "large",
    estimated: true,
    thresholds: { pruneStartPercent: 94, compactStartPercent: 99, minPrunePercent: 30, maxPrunePercent: 80, keepTurns: 2 },
  });
  assert.equal(large.windowKind, "large");
  assert.equal(large.percentage, 99);
  assert.equal(large.pruneStart, 94);
  assert.equal(contextUsageTone(large), "danger");

  const directCompact = normalizeContextStatus({
    estimatedTokens: 93,
    limitTokens: 100,
    estimated: true,
    thresholds: { pruneStartPercent: 99, compactStartPercent: 95 },
  });
  assert.equal(contextUsageTone(directCompact), "warning");
});

test("context ring uses gray unknown/loading track and threshold tones", () => {
  const unknown = normalizeContextStatus({}, { loading: true });
  assert.equal(contextUsageTone(unknown), "unknown");
  assert.equal(contextRingStyle(unknown), "conic-gradient(#d6d9df 0 100%)");

  const normal = normalizeContextStatus({ estimatedTokens: 50, limit: 100 });
  assert.equal(contextUsageTone(normal), "normal");
  assert.match(contextRingStyle(normal), /^conic-gradient\(#4b5563 0 50%, #d6d9df 50% 100%\)$/);

  const warning = normalizeContextStatus({ estimatedTokens: 95, limit: 100 });
  assert.equal(contextUsageTone(warning), "warning");
  assert.match(contextRingStyle(warning), /#d97706/);

  const danger = normalizeContextStatus({ estimatedTokens: 100, limit: 100 });
  assert.equal(contextUsageTone(danger), "danger");
  assert.match(contextRingStyle(danger), /#dc2626/);
});

test("context settings expose approved defaults and reject invalid pruning semantics", () => {
  assert.deepEqual(normalizeContextSettings({}), defaultContextSettings);
  assert.equal(validateContextSettings(defaultContextSettings).valid, true);
  assert.equal(validateContextSettings({ ...defaultContextSettings, minPrunePercent: 81, maxPrunePercent: 80 }).valid, false);
  assert.equal(validateContextSettings({ ...defaultContextSettings, standardPruneStart: 99, standardCompactStart: 99 }).valid, true);
  assert.equal(validateContextSettings({ ...defaultContextSettings, largePruneStart: 100, largeCompactStart: 99 }).valid, true);
  assert.equal(validateContextSettings({ ...defaultContextSettings, retainTurns: 2.5 }).valid, false);
  assert.equal(validateContextSettings({ ...defaultContextSettings, maxPrunePercent: 101 }).valid, false);
  assert.deepEqual(contextSettingsPayload(defaultContextSettings), {
    compactKeepTurns: 2,
    maxPrunePercent: 80,
    minPrunePercent: 30,
    standard: { pruneStart: 95, compactStart: 99 },
    large: { pruneStart: 95, compactStart: 99 },
  });
});

test("controller ignores stale agent context responses", async () => {
  const pending = [];
  let agent = { id: "agent-a", entityGeneration: 1 };
  const { document, window } = browserStubs();
  const controller = createContextManagementController({
    request(path) {
      const operation = deferred();
      pending.push({ path, operation });
      return operation.promise;
    },
    getAgent: () => agent,
    document,
    window,
  });

  const first = controller.setAgent(agent);
  agent = { id: "agent-b", entityGeneration: 2 };
  const second = controller.setAgent(agent);
  assert.deepEqual(pending.map(({ path }) => path), [
    "/api/agents/agent-a/context",
    "/api/agents/agent-b/context",
  ]);

  pending[1].operation.resolve({ context: { estimatedTokens: 40, limit: 100, canCompact: true } });
  await second;
  pending[0].operation.resolve({ context: { estimatedTokens: 99, limit: 100, canCompact: true } });
  await first;

  assert.equal(controller.getStatus().agentId, "agent-b");
  assert.equal(controller.getStatus().percentage, 40);
});

test("controller uses one API path for preferences, compact, and logical clear confirmation", async () => {
  const requests = [];
  const toasts = [];
  let agent = { id: "agent-1", entityGeneration: 7, status: "idle" };
  const { document, window } = browserStubs();
  const controller = createContextManagementController({
    async request(path, options = {}) {
      requests.push({ path, options, body: options.body ? JSON.parse(options.body) : null });
      if (options.method === "GET") return { context: { estimatedTokens: 95, limitTokens: 100, usagePercent: 95, estimated: true, latestMessageId: "message-3", pruneEnabled: true, canCompact: true, canClear: true, messageCount: 3 } };
      if (path.endsWith("/compact")) return { compacted: true, context: { estimatedTokens: 60, limitTokens: 100, usagePercent: 60, estimated: true, latestMessageId: "message-3", pruneEnabled: false, canCompact: true, canClear: true } };
      if (path.endsWith("/clear")) return { cleared: true, context: { estimatedTokens: 0, limitTokens: 100, usagePercent: 0, estimated: true, pruneEnabled: false, canCompact: false, canClear: false } };
      return {};
    },
    getAgent: () => agent,
    showToast(message, tone) { toasts.push([message, tone]); },
    translate: (key) => key,
    document,
    window,
  });

  await controller.setAgent(agent);
  await controller.setAutoPrune(false);
  await controller.compact();
  assert.equal(await controller.clear(), true, "first clear only requests confirmation");
  await controller.clear();

  assert.deepEqual(requests.map(({ path, options }) => [path, options.method]), [
    ["/api/agents/agent-1/context", "GET"],
    ["/api/agents/agent-1/context/preferences", "PATCH"],
    ["/api/agents/agent-1/context/compact", "POST"],
    ["/api/agents/agent-1/context/clear", "POST"],
  ]);
  assert.deepEqual(requests[1].body, { pruneEnabled: false, entityGeneration: 7 });
  assert.deepEqual(requests[2].body, { entityGeneration: 7, expectedLatestMessageId: "message-3" });
  assert.deepEqual(requests[3].body, { entityGeneration: 7, expectedLatestMessageId: "message-3" });
  assert.equal(controller.getStatus().percentage, 0);
  assert.deepEqual(toasts, [
    ["context.autoPruneDisabled", "success"],
    ["context.compactSuccess", "success"],
    ["context.clearSuccess", "success"],
  ]);
  agent = null;
  await controller.reset(null);
});

test("static shell mounts one shared context ring, accessible overlays, APIs, and cache stamps", async () => {
  const [html, styles, app, appMain, uiShell, i18n, contextModule] = await Promise.all([
    readFile(indexURL, "utf8"),
    readStylesSource(stylesURL),
    readFile(appURL, "utf8"),
    readFile(appMainURL, "utf8"),
    readFile(uiShellURL, "utf8"),
    readFile(i18nURL, "utf8"),
    readFile(new URL("modules/context-management.mjs", staticRoot), "utf8"),
  ]);

  assert.equal((html.match(/id="contextUsageBtn"/g) || []).length, 1);
  assert.ok(html.indexOf('id="composerStatusText"') < html.indexOf('id="contextUsageBtn"'));
  assert.ok(html.indexOf('id="contextUsageBtn"') < html.indexOf('class="composer-controls"'));
  for (const id of [
    "contextUsageRing", "contextUsageOverlay", "contextUsagePanel", "contextAutoPrune", "contextCompactBtn",
    "contextClearBtn", "contextClearConfirmation", "contextThresholdBtn", "contextThresholdModal", "contextThresholdForm",
    "contextRetainTurns", "contextMaxPrunePercent", "contextMinPrunePercent", "contextStandardPruneStart",
    "contextStandardCompactStart", "contextLargePruneStart", "contextLargeCompactStart",
  ]) assert.match(html, new RegExp(`id="${id}"`), id);
  assert.match(html, /只会清空提供给后续模型的逻辑上下文，不会删除聊天记录/);
  assert.doesNotMatch(html, /id="contextLargeWindowTokens"/);
  assert.match(html, /id="contextUsagePanel"[^>]*role="dialog"[^>]*aria-labelledby="contextUsageTitle"/);
  assert.match(html, /id="contextThresholdModal"[^>]*aria-modal="true"[^>]*aria-hidden="true"/);

  assert.match(styles, /\.context-usage-ring[\s\S]*?conic-gradient/);
  assert.match(styles, /\.context-usage-overlay\.is-mobile \.context-management-panel[\s\S]*?border-radius:\s*20px 20px 0 0/);
  assert.match(styles, /\.context-usage-btn\[data-tone="warning"\]/);
  assert.match(styles, /\.context-usage-btn\[data-tone="danger"\]/);

  assert.match(appMain, /createContextManagementController\(\{/);
  assert.match(appMain, /contextManagement\.setAgent\(state\.agent\)/);
  assert.match(appMain, /contextManagement\.reset\(null\)/);
  assert.match(appMain, /event\.type === "context\.updated"/);
  assert.match(appMain, /contextManagement\.applyStatus\(snapshot\.context/);
  assert.match(appMain, /manageContext:\s*\(options\) => contextManagement\.open\(options\)/);
  assert.match(uiShell, /manageContextAction\(\{ focusAction: "compact" \}\)/);
  assert.doesNotMatch(uiShell, /api\/agents\/.*context\/compact/);

  for (const path of [
    "/api/agents/${encodeURIComponent(expectedAgentId)}/context",
    "/api/agents/${encodeURIComponent(expectedAgentId)}/context/preferences",
    "/api/agents/${encodeURIComponent(expectedAgentId)}/context/${path}",
    "/api/runtime/context-settings",
  ]) assert.match(contextModule, new RegExp(path.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(contextModule, /pruneEnabled: Boolean\(enabled\)/);
  assert.match(contextModule, /expectedLatestMessageId: status\.latestMessageId/);
  assert.match(contextModule, /compactKeepTurns: settings\.retainTurns/);

  assert.match(html, /context-ring-3/);
  assert.match(app, /context-ring-3/);
  assert.match(appMain, /context-ring-3/);
  assert.match(i18n, /context-ring-3/);
});
