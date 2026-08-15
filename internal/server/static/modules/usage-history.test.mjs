import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";

import { usageHistoryMessages, usageHistoryMessage } from "./messages-usage-history.mjs";
import {
  appendUsageHistoryItems,
  buildUsageHistoryURL,
  createUsageHistoryController,
  createUsageHistoryState,
  normalizeUsageHistoryResponse,
  normalizeTrendPointIndex,
  renderTrendReadout,
  renderUsageHistory,
  renderUsageTrendSVG,
  usageHistoryMetrics,
} from "./usage-history.mjs";

function response(overrides = {}) {
  return {
    generatedAt: "2026-03-01T00:00:00Z",
    summary: { requestCount: 1, totalTokens: 30, inputTokens: 10, outputTokens: 20, successRate: 1 },
    trend: [{ bucket: "2026-03-01", requestCount: 1, totalTokens: 30 }],
    options: { providers: ["openai"], models: ["gpt-5"], kinds: ["chat"] },
    items: [{ id: "one", createdAt: "2026-03-01T00:00:00Z", provider: "openai", model: "gpt-5", status: "success" }],
    nextCursor: "",
    ...overrides,
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test("usage history URL uses URLSearchParams and preserves supported filters", () => {
  const url = buildUsageHistoryURL({
    filters: { provider: "Open AI & Co", model: "gpt/5?x=1", kind: "chat", from: "2026-01-02", to: "2026-02-03" },
    bucket: "month",
    limit: 99,
    cursor: "next=one&two",
  });
  const parsed = new URL(url, "http://localhost");
  assert.equal(parsed.pathname, "/api/usage/history");
  assert.deepEqual(Object.fromEntries(parsed.searchParams), {
    provider: "Open AI & Co",
    model: "gpt/5?x=1",
    kind: "chat",
    from: "2026-01-02",
    to: "2026-02-03",
    bucket: "month",
    limit: "50",
    cursor: "next=one&two",
  });
  assert.doesNotMatch(url, /Open AI|next=one&two/);
});

test("normalization converts all metrics, arrays, and text to safe bounded values", () => {
  const normalized = normalizeUsageHistoryResponse({
    generatedAt: 123,
    summary: { requestCount: "4", inputTokens: "bad", totalCostUsd: -2, successRate: "0.75" },
    trend: [{ bucket: 9, requestCount: "2", averageTTFTMs: Infinity }],
    options: { providers: ["x", "x", null, ""], models: "bad", kinds: ["chat"] },
    items: [{ id: 8, inputTokens: "5", durationMs: -1, status: null }],
    nextCursor: 44,
  });
  assert.equal(normalized.generatedAt, "123");
  assert.equal(normalized.summary.requestCount, 4);
  assert.equal(normalized.summary.inputTokens, 0);
  assert.equal(normalized.summary.totalCostUsd, 0);
  assert.equal(normalized.summary.successRate, 0.75);
  assert.equal(normalized.trend[0].bucket, "9");
  assert.equal(normalized.trend[0].averageTTFTMs, 0);
  assert.deepEqual(normalized.options.providers, ["x"]);
  assert.deepEqual(normalized.options.models, []);
  assert.equal(normalized.items[0].id, "8");
  assert.equal(normalized.items[0].inputTokens, 5);
  assert.equal(normalized.items[0].durationMs, 0);
  assert.equal(normalized.nextCursor, "44");
});

test("usage history defaults to total tokens", () => {
  assert.equal(createUsageHistoryState().metric, "totalTokens");
  assert.match(renderUsageHistory({ status: "ready" }), /value="totalTokens" selected/);
});

test("usage history exposes shared settings classes and accessible regions", () => {
  const html = renderUsageHistory(response());
  assert.match(html, /<main class="usage-history-page settings-page settings-page-usage" aria-labelledby="usageHistoryTitle">/);
  assert.match(html, /class="uh-summary-grid settings-stat-grid"/);
  assert.match(html, /class="uh-panel uh-filter-panel settings-card" aria-labelledby="usageHistoryFiltersTitle"/);
  assert.match(html, /class="uh-history-table settings-data-list" aria-label=/);
  assert.match(html, /uh-table-scroll settings-h-scroll[^>]*tabindex="0"/);
  assert.match(html, /uh-chart-host settings-h-scroll[^>]*tabindex="0"/);
  assert.match(html, /aria-live="polite"/);
});

test("selected filters remain visible and missing summary timings render as dashes", () => {
  const html = renderUsageHistory({
    status: "ready",
    filters: { provider: "selected-provider", model: "selected-model", kind: "selected-kind" },
    options: { providers: [], models: [], kinds: [] },
    summary: { averageTTFTMs: 0, averageDurationMs: 0 },
  });
  assert.match(html, /value="selected-provider" selected/);
  assert.match(html, /value="selected-model" selected/);
  assert.match(html, /value="selected-kind" selected/);
  assert.equal((html.match(/<strong>—<\/strong>/g) || []).length >= 2, true);
});

test("inline SVG has grid, axes, line, point titles, compact axis labels, and an empty state", () => {
  const svg = renderUsageTrendSVG([
    { bucket: "2026-01-01", requestCount: 2 },
    { bucket: "2026-01-02", requestCount: 5 },
  ], "requests");
  assert.match(svg, /^<svg/);
  assert.match(svg, /uh-chart-grid/);
  assert.match(svg, /uh-chart-axis/);
  assert.match(svg, /<path class="uh-chart-line"/);
  assert.equal((svg.match(/<circle class="uh-chart-point[^"]*"/g) || []).length, 2);
  assert.match(svg, /<title>2026-01-02:/);
  const largeAxis = renderUsageTrendSVG([{ bucket: "large", totalTokens: 1250000 }], "totalTokens").split('<line class="uh-chart-axis"')[0];
  assert.doesNotMatch(largeAxis, />1,250,000</);
  assert.match(renderUsageTrendSVG([], "requests"), /uh-chart-empty/);
});

// Each bucket already carries the whole metric set, so a point can report its
// token usage no matter which metric the chart is drawing.
test("trend points report request count and token detail beyond the drawn metric", () => {
  const point = { bucket: "2026-01-02", requestCount: 5, inputTokens: 1200, outputTokens: 800, totalTokens: 2000 };

  const byRequests = renderUsageTrendSVG([point], "requests");
  // The drawn metric leads and is not repeated as a detail part.
  assert.match(byRequests, /<title>2026-01-02: 5 · 2,000 tokens · 输入 1,200 \/ 输出 800<\/title>/);
  // Focusable points need an accessible name, not just an SVG <title>. They
  // announce as buttons because clicking one pins its reading.
  assert.match(byRequests, /<circle class="uh-chart-point"[^>]*role="button" aria-label="2026-01-02: 5 · 2,000 tokens · 输入 1,200 \/ 输出 800"/);

  // Drawing totalTokens: the total is the leading value, so only the request
  // count and the split are appended.
  assert.match(renderUsageTrendSVG([point], "totalTokens"), /<title>2026-01-02: 2,000 · 5 次请求 · 输入 1,200 \/ 输出 800<\/title>/);
  // Drawing inputTokens: the split would restate the leading value.
  assert.match(renderUsageTrendSVG([point], "inputTokens"), /<title>2026-01-02: 1,200 · 5 次请求 · 2,000 tokens<\/title>/);
  // No token data recorded: nothing is invented.
  assert.match(renderUsageTrendSVG([{ bucket: "2026-01-03", requestCount: 4 }], "requests"), /<title>2026-01-03: 4<\/title>/);
});

// Hover was the only way to read a point, which is unreachable on touch and
// disappears the moment the pointer moves. Clicking now pins the reading.
test("圖上的點可點選，選中的點會標記並在 readout 顯示數值", () => {
  const trend = [
    { bucket: "2026-01-01", requestCount: 2, totalTokens: 100 },
    { bucket: "2026-01-02", requestCount: 5, totalTokens: 300 },
  ];

  // Nothing pinned: no point is marked and the readout invites a click.
  const bare = renderUsageTrendSVG(trend, "requests");
  assert.doesNotMatch(bare, /is-selected/);
  assert.match(bare, /data-usage-trend-point="0"/);
  assert.match(bare, /data-usage-trend-point="1"/);
  assert.match(renderTrendReadout(trend, "requests", -1), /uh-trend-readout-hint/);

  // Pinned: only that point is marked, and it is drawn larger so it reads as
  // selected without relying on colour alone.
  const pinned = renderUsageTrendSVG(trend, "requests", { selectedIndex: 1 });
  assert.equal((pinned.match(/is-selected/g) || []).length, 1);
  assert.match(pinned, /data-usage-trend-point="1"[^>]*$|is-selected[^>]*r="6"|r="6"[^>]*is-selected/);
  assert.match(pinned, /aria-pressed="true"/);

  const readout = renderTrendReadout(trend, "requests", 1);
  assert.match(readout, /2026-01-02/);
  assert.match(readout, /uh-trend-readout-value/);
  // It stays on screen, so it must be announced and be dismissible.
  assert.match(readout, /aria-live="polite"/);
  assert.match(readout, /data-usage-trend-clear/);
});

// A pinned index must never outlive the series it belonged to: switching bucket
// can return fewer buckets than were on screen a moment ago.
test("固定的索引會對照當前資料重新驗證，不會指向不存在的點", () => {
  assert.equal(normalizeTrendPointIndex(1, 2), 1);
  assert.equal(normalizeTrendPointIndex(0, 1), 0);
  // Past the end, negative, and non-numeric all mean "nothing pinned".
  assert.equal(normalizeTrendPointIndex(5, 2), -1);
  assert.equal(normalizeTrendPointIndex(-1, 2), -1);
  assert.equal(normalizeTrendPointIndex("nope", 2), -1);
  assert.equal(normalizeTrendPointIndex(0, 0), -1);
  assert.equal(normalizeTrendPointIndex(1.9, 3), 1);

  // The state constructor validates against the trend it actually received.
  const state = createUsageHistoryState({ ...response(), selectedTrendIndex: 7 });
  assert.equal(state.selectedTrendIndex, -1);
  const valid = createUsageHistoryState({ ...response(), selectedTrendIndex: 0 });
  assert.equal(valid.selectedTrendIndex, 0);
});

test("render escapes malicious server fields in cards, SVG, filters, and table", () => {
  const attack = '\"><img src=x onerror="boom">';
  const html = renderUsageHistory({
    status: "ready",
    generatedAt: attack,
    summary: {},
    metric: "requests",
    trend: [{ bucket: `<script>alert(1)</script>`, requestCount: 1 }],
    options: { providers: [attack], models: [attack], kinds: [attack] },
    filters: { provider: attack, model: attack, kind: attack },
    items: [{ id: attack, createdAt: attack, agentId: attack, agentTitle: attack, kind: attack, provider: attack, model: attack, errorMessage: `<svg onload=boom>`, status: attack }],
  });
  assert.doesNotMatch(html, /<script>|<img src=x|<svg onload/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.match(html, /&lt;img src=x onerror=/);
  assert.match(html, /&lt;svg onload=boom&gt;/);
  assert.doesNotMatch(html, /credential|raw dump/i);
});

test("summary uses compact primary counts with precise subtitles", () => {
  const html = renderUsageHistory({
    status: "ready",
    summary: { requestCount: 1250000, totalTokens: 2500000, inputTokens: 1000000, outputTokens: 1500000, reasoningTokens: 800000, cachedInputTokens: 700000, errors: 12000 },
  });
  assert.match(html, /精确值 1,250,000/);
  assert.match(html, /输入 1,000,000 · 输出 1,500,000/);
  assert.doesNotMatch(html, /<strong>1,250,000<\/strong>/);
  assert.doesNotMatch(html, /<strong>2,500,000<\/strong>/);
});

test("request rows localize known statuses and show missing durations as dashes", () => {
  const html = renderUsageHistory({
    status: "ready",
    items: [
      { id: "ok", status: "completed", ttftMs: 0, durationMs: 0 },
      { id: "bad", status: "failed", ttftMs: 25, durationMs: 1000 },
    ],
  });
  assert.match(html, />成功<\/span>/);
  assert.match(html, />错误<\/span>/);
  assert.match(html, /<td>—<\/td>\s*<td>—<\/td>/);
  assert.match(html, /<td>25 ms<\/td>\s*<td>1\.0 s<\/td>/);
});

test("all ten metrics and required three-language keys are present", () => {
  assert.deepEqual(usageHistoryMetrics, [
    "requests", "totalTokens", "inputTokens", "outputTokens", "reasoningTokens", "cachedInputTokens",
    "averageTTFTMs", "averageDurationMs", "totalCostUsd", "errors",
  ]);
  for (const locale of ["zh-CN", "zh-TW", "en"]) {
    assert.ok(usageHistoryMessages[locale].usageHistory.title);
    assert.ok(usageHistoryMessages[locale].usageHistory.filters.apply);
    assert.ok(usageHistoryMessages[locale].usageHistory.history.loadMore);
    assert.ok(usageHistoryMessages[locale].usageHistory.history.statusSuccess);
    assert.ok(usageHistoryMessages[locale].usageHistory.history.statusError);
    assert.ok(usageHistoryMessages[locale].usageHistory.history.statusUnknown);
    assert.notEqual(usageHistoryMessage("usageHistory.trend.metrics.totalCostUsd", {}, locale), "usageHistory.trend.metrics.totalCostUsd");
  }
});

test("render includes explicit empty and error states", () => {
  assert.match(renderUsageHistory({ status: "ready", items: [], trend: [] }), /当前筛选条件下暂无请求记录/);
  const error = renderUsageHistory({ status: "error", error: `<bad>`, items: [], trend: [] });
  assert.match(error, /请求历史加载失败/);
  assert.match(error, /&lt;bad&gt;/);
  assert.doesNotMatch(error, /<bad>/);
});

test("controller ignores stale responses", async () => {
  const first = deferred();
  const second = deferred();
  const calls = [];
  const state = {};
  const controller = createUsageHistoryController({
    state,
    request: (url) => {
      calls.push(url);
      return calls.length === 1 ? first.promise : second.promise;
    },
  });
  const older = controller.refresh();
  const newer = controller.refresh();
  second.resolve(response({ generatedAt: "new", items: [{ id: "new" }] }));
  await newer;
  first.resolve(response({ generatedAt: "old", items: [{ id: "old" }] }));
  await older;
  assert.equal(state.usageHistory.generatedAt, "new");
  assert.deepEqual(state.usageHistory.items.map((item) => item.id), ["new"]);
  assert.equal(state.usageHistory.status, "ready");
});

test("controller 點同一個點會取消固定，換 bucket 時不會留下失效的選取", async () => {
  const urls = [];
  const state = {};
  const twoPoints = response({
    trend: [
      { bucket: "2026-03-01", requestCount: 1, totalTokens: 30 },
      { bucket: "2026-03-02", requestCount: 4, totalTokens: 90 },
    ],
  });
  const controller = createUsageHistoryController({
    state,
    request: async (url) => { urls.push(url); return urls.length === 1 ? twoPoints : response(); },
  });
  await controller.load();

  assert.equal(controller.selectTrendPoint(1), 1);
  // Clicking the pinned point again releases it rather than re-pinning.
  assert.equal(controller.selectTrendPoint(1), -1);
  assert.equal(controller.selectTrendPoint(0), 0);
  // Out of range is treated as nothing pinned, not clamped onto a real point.
  assert.equal(controller.selectTrendPoint(9), -1);

  // A reload returning a shorter trend must not leave index 1 pinned.
  assert.equal(controller.selectTrendPoint(1), 1);
  await controller.setBucket("hour");
  assert.equal(controller.getState().selectedTrendIndex, -1);
});

test("controller applies filters, changes bucket with fetch, and metric without fetch", async () => {
  const urls = [];
  const state = {};
  const controller = createUsageHistoryController({ state, request: async (url) => { urls.push(url); return response(); } });
  await controller.applyFilters({ provider: "a&b", model: "m/x", kind: "chat", from: "2026-01-01", to: "2026-01-31" });
  let parsed = new URL(urls.at(-1), "http://localhost");
  assert.equal(parsed.searchParams.get("provider"), "a&b");
  assert.equal(parsed.searchParams.get("model"), "m/x");
  assert.equal(parsed.searchParams.get("from"), "2026-01-01");
  const requestCount = urls.length;
  controller.setMetric("totalTokens");
  assert.equal(urls.length, requestCount);
  assert.equal(state.usageHistory.metric, "totalTokens");
  await controller.setBucket("hour");
  parsed = new URL(urls.at(-1), "http://localhost");
  assert.equal(parsed.searchParams.get("bucket"), "hour");
  assert.equal(urls.length, requestCount + 1);
  await controller.resetFilters();
  parsed = new URL(urls.at(-1), "http://localhost");
  assert.equal(parsed.searchParams.has("provider"), false);
});

test("load more appends and deduplicates request rows", async () => {
  const urls = [];
  const pages = [
    response({ items: [{ id: "one", provider: "a" }], nextCursor: "cursor&2" }),
    response({ items: [{ id: "one", provider: "duplicate" }, { id: "two" }], nextCursor: "" }),
  ];
  const state = {};
  const controller = createUsageHistoryController({ state, request: async (url) => { urls.push(url); return pages.shift(); } });
  await controller.refresh();
  await controller.loadMore();
  assert.deepEqual(state.usageHistory.items.map((item) => item.id), ["one", "two"]);
  const parsed = new URL(urls[1], "http://localhost");
  assert.equal(parsed.searchParams.get("cursor"), "cursor&2");
  assert.equal(state.usageHistory.nextCursor, "");
  assert.deepEqual(appendUsageHistoryItems([{ id: "x" }], [{ id: "x" }, { id: "y" }]).map((item) => item.id), ["x", "y"]);
});

test("static integration uses the new usage controller and leaves metric card reusable", async () => {
  const root = new URL("../", import.meta.url);
  const [appMain, systemSettings, styles] = await Promise.all([
    readFile(new URL("modules/app-main.mjs", root), "utf8"),
    readFile(new URL("modules/system-settings.mjs", root), "utf8"),
    readStylesSource(new URL("styles.css", root)),
  ]);
  assert.match(appMain, /createUsageHistoryController/);
  assert.match(appMain, /\["usage", \{ render: usageHistory\.render, bind: usageHistory\.bind \}\]/);
  assert.doesNotMatch(appMain, /loadUsageSummary|usageSummary|usageError|usageSeq/);
  assert.doesNotMatch(systemSettings, /renderUsageSettingsContent|bindUsageSettingsActions|refreshUsageSummaryBtn/);
  assert.match(systemSettings, /function renderUsageMetricCard/);
  const usageMarker = styles.indexOf("/* Usage history request analytics.");
  const providerMarker = styles.lastIndexOf("/* Model provider settings. Scoped after legacy settings overrides by design. */");
  assert.ok(usageMarker >= 0 && usageMarker < providerMarker);
  assert.match(styles.slice(usageMarker, providerMarker), /#settingsContentBody \.usage-history-page/);
  assert.ok(styles.trimEnd().endsWith(styles.slice(providerMarker).trimEnd()));
});
