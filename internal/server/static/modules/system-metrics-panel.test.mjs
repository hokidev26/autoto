import assert from "node:assert/strict";
import test from "node:test";

import {
  createSystemMetricsPoller,
  formatPercent,
  formatRate,
  metricTone,
  normalizeSystemMetrics,
  renderSystemMetrics,
} from "./system-metrics-panel.mjs";

const MB = 1024 * 1024;

function fullPayload(overrides = {}) {
  return {
    cpu: { available: true, percent: 12 },
    memory: { available: true, percent: 40, usedBytes: 8 * 1024 * MB, totalBytes: 20 * 1024 * MB },
    network: { available: true, rxBytesPerSec: 512 * 1024, txBytesPerSec: 64 * 1024 },
    ...overrides,
  };
}

test("normalization requires an explicit availability flag and bounds every value", () => {
  const normalized = normalizeSystemMetrics({
    cpu: { available: true, percent: 250 },
    memory: { available: "yes", percent: 40 },
    network: { available: true, rxBytesPerSec: -5, txBytesPerSec: Number.NaN },
  });

  // Percentages are clamped rather than trusted.
  assert.equal(normalized.cpu.percent, 100);
  // Only a real boolean true counts: a truthy string is not the server saying
  // the metric was measured.
  assert.equal(normalized.memory.available, false);
  assert.equal(normalized.network.rxBytesPerSec, 0);
  assert.equal(normalized.network.txBytesPerSec, 0);

  // Missing, null, and non-object payloads all normalize to unavailable rather
  // than throwing.
  for (const payload of [undefined, null, "nope", [], {}]) {
    const empty = normalizeSystemMetrics(payload);
    assert.deepEqual(
      [empty.cpu.available, empty.memory.available, empty.network.available],
      [false, false, false],
    );
  }
});

test("tones step from ok through warning to danger at the documented thresholds", () => {
  assert.equal(metricTone("cpu", 69.9), "ok");
  assert.equal(metricTone("cpu", 70), "warning");
  assert.equal(metricTone("cpu", 89.9), "warning");
  assert.equal(metricTone("cpu", 90), "danger");

  // Memory turns amber earlier than CPU: sustained high memory is a worse sign
  // than a brief CPU spike.
  assert.equal(metricTone("memory", 74.9), "ok");
  assert.equal(metricTone("memory", 75), "warning");
  assert.equal(metricTone("memory", 90), "danger");

  // Network is graded on absolute throughput, not a share of link speed.
  assert.equal(metricTone("network", 2 * MB - 1), "ok");
  assert.equal(metricTone("network", 2 * MB), "warning");
  assert.equal(metricTone("network", 20 * MB), "danger");

  assert.equal(metricTone("unknown", 999), "ok");
  assert.equal(metricTone("cpu", Number.NaN), "ok");
});

// Rates and percentages go through the shared regional formatters rather than a
// local implementation, so they follow the user's locale like every other figure
// in the app.
test("rates and percentages use the shared formatters", () => {
  assert.equal(formatRate(0), "0 B/s");
  assert.equal(formatRate(512), "512 B/s");
  assert.equal(formatRate(1536), "1.5 KB/s");
  assert.equal(formatRate(2 * MB), "2 MB/s");
  assert.equal(formatRate(512 * 1024), "512 KB/s");
  // Negative rates are not representable and are floored rather than shown.
  assert.equal(formatRate(-5), "0 B/s");

  assert.equal(formatPercent(0), "0%");
  assert.equal(formatPercent(12.4), "12%");
  // Percentages are clamped to the 0-100 range.
  assert.equal(formatPercent(250), "100%");
  assert.equal(formatPercent(Number.NaN), "0%");
});

test("render emits one inline reading per available metric with tone, value, and bar", () => {
  const html = renderSystemMetrics(fullPayload());

  assert.match(html, /data-overview-section="system-metrics"/);
  // A single slim strip rather than a grid of cards.
  assert.match(html, /class="overview-metrics-strip"/);
  // CPU, memory, download, upload.
  assert.equal((html.match(/class="overview-metric-inline"/g) || []).length, 4);
  assert.match(html, /data-overview-metric="cpu" data-tone="ok"/);
  assert.match(html, /data-overview-metric="memory" data-tone="ok"/);
  assert.match(html, /data-overview-metric="networkDown"/);
  assert.match(html, /data-overview-metric="networkUp"/);
  assert.match(html, />12%</);
  // 512 is past the 100 mark in its unit, so the decimal is dropped.
  assert.match(html, />512 KB\/s</);
  // Memory keeps the absolute figures in the tooltip and accessible name.
  assert.match(html, /8 GB \/ 20 GB/);
  // The bar length is the metric's share of capacity.
  assert.match(html, /style="width:40\.0%"/);
  // At rest the strip does not repeat "normal" four times in a row.
  assert.doesNotMatch(html, /class="overview-metric-tone"/);
});

test("render escalates tone and reports the level as text, not colour alone", () => {
  const stressed = renderSystemMetrics(fullPayload({
    cpu: { available: true, percent: 95 },
    memory: { available: true, percent: 80, usedBytes: 16 * 1024 * MB, totalBytes: 20 * 1024 * MB },
    network: { available: true, rxBytesPerSec: 30 * MB, txBytesPerSec: 5 * MB },
  }));

  assert.match(stressed, /data-overview-metric="cpu" data-tone="danger"/);
  assert.match(stressed, /data-overview-metric="memory" data-tone="warning"/);
  assert.match(stressed, /data-overview-metric="networkDown" data-tone="danger"/);
  assert.match(stressed, /data-overview-metric="networkUp" data-tone="warning"/);
  // Anyone who cannot distinguish the hues still gets the state in words,
  // rendered inline once a metric is under pressure.
  assert.match(stressed, /class="overview-metric-tone">有压力/);
  assert.match(stressed, /class="overview-metric-tone">有点压力/);
  // Network has no capacity ceiling, so its bar is clamped rather than
  // reporting a percentage it cannot know.
  assert.match(stressed, /style="width:100\.0%"/);
});

test("render omits unavailable metrics and the whole section when nothing is measurable", () => {
  const memoryOnly = renderSystemMetrics({
    cpu: { available: false, percent: 0 },
    memory: { available: true, percent: 30, usedBytes: 3, totalBytes: 10 },
    network: { available: false },
  });
  assert.equal((memoryOnly.match(/class="overview-metric-inline"/g) || []).length, 1);
  assert.match(memoryOnly, /data-overview-metric="memory"/);
  assert.doesNotMatch(memoryOnly, /data-overview-metric="cpu"/);

  // A platform with no collector, or the very first sample of a process, must
  // render nothing rather than an empty shell of zeroed bars.
  assert.equal(renderSystemMetrics({}), "");
  assert.equal(renderSystemMetrics(null), "");
});

test("render escapes translator output", () => {
  const html = renderSystemMetrics(fullPayload(), () => '"><img src=x onerror="boom">');
  assert.doesNotMatch(html, /<img src=x/);
  assert.match(html, /&quot;&gt;&lt;img/);
});

// The poller is driven through injected timers and document so the cadence,
// visibility handling, and failure behaviour can be asserted without waiting.
function fakeEnvironment() {
  const listeners = new Map();
  const timers = new Map();
  let nextTimer = 1;
  return {
    doc: {
      hidden: false,
      addEventListener(type, handler) { listeners.set(type, handler); },
      removeEventListener(type) { listeners.delete(type); },
    },
    listeners,
    timers,
    setTimeoutFn(callback) {
      const id = nextTimer++;
      timers.set(id, callback);
      return id;
    },
    clearTimeoutFn(id) { timers.delete(id); },
    // Runs whatever is currently queued, exactly once.
    async advance() {
      const pending = [...timers.entries()];
      timers.clear();
      for (const [, callback] of pending) callback();
      await Promise.resolve();
      await Promise.resolve();
    },
  };
}

test("poller requires a request function", () => {
  assert.throws(() => createSystemMetricsPoller({}), TypeError);
});

test("poller fetches on start, reschedules, and emits normalized models", async () => {
  const environment = fakeEnvironment();
  const paths = [];
  const updates = [];
  const poller = createSystemMetricsPoller({
    request: async (path) => {
      paths.push(path);
      return fullPayload();
    },
    onUpdate: (value) => updates.push(value),
    documentRef: environment.doc,
    setTimeoutFn: environment.setTimeoutFn,
    clearTimeoutFn: environment.clearTimeoutFn,
  });

  assert.equal(poller.start(), true);
  // start() is not idempotent-by-accident: a second call must not double the
  // request rate.
  assert.equal(poller.start(), false);
  await environment.advance();

  assert.deepEqual(paths, ["/api/system/metrics"]);
  assert.equal(updates.length, 1);
  // The consumer receives a normalized model, not the raw payload.
  assert.equal(updates[0].cpu.percent, 12);
  assert.equal(updates[0].memory.available, true);

  await environment.advance();
  assert.equal(paths.length, 2);

  assert.equal(poller.stop(), true);
  assert.equal(poller.stop(), false);
  assert.equal(poller.isRunning(), false);
  await environment.advance();
  // Nothing more is requested once stopped.
  assert.equal(paths.length, 2);
});

test("poller skips requests while the document is hidden but resumes on visibility", async () => {
  const environment = fakeEnvironment();
  let calls = 0;
  const poller = createSystemMetricsPoller({
    request: async () => { calls += 1; return fullPayload(); },
    documentRef: environment.doc,
    setTimeoutFn: environment.setTimeoutFn,
    clearTimeoutFn: environment.clearTimeoutFn,
  });

  poller.start();
  await environment.advance();
  assert.equal(calls, 1);

  environment.doc.hidden = true;
  await environment.advance();
  // A hidden tab polling on a timer is wasted work and, in a packaged shell,
  // keeps the machine awake.
  assert.equal(calls, 1);
  // The loop stays alive rather than needing a restart.
  assert.equal(poller.isRunning(), true);

  environment.doc.hidden = false;
  await environment.listeners.get("visibilitychange")?.();
  await Promise.resolve();
  assert.equal(calls, 2);

  poller.stop();
  // The listener is released so a stopped poller cannot be revived by a tab
  // switch.
  assert.equal(environment.listeners.has("visibilitychange"), false);
});

test("poller gives up after repeated failures and clears the cards", async () => {
  const environment = fakeEnvironment();
  const updates = [];
  let calls = 0;
  const poller = createSystemMetricsPoller({
    request: async () => { calls += 1; throw new Error("offline"); },
    onUpdate: (value) => updates.push(value),
    documentRef: environment.doc,
    setTimeoutFn: environment.setTimeoutFn,
    clearTimeoutFn: environment.clearTimeoutFn,
  });

  poller.start();
  await environment.advance();
  // A single failure keeps the last good reading on screen and retries.
  assert.equal(updates.length, 0);
  assert.equal(poller.isRunning(), true);

  await environment.advance();
  await environment.advance();

  assert.equal(calls, 3);
  assert.equal(poller.isRunning(), false);
  // The final emit is an all-unavailable model, so the section disappears
  // instead of freezing on a stale reading.
  assert.equal(updates.length, 1);
  assert.equal(updates.at(-1).cpu.available, false);
  assert.equal(renderSystemMetrics(updates.at(-1)), "");
});

test("poller survives a throwing consumer", async () => {
  const environment = fakeEnvironment();
  let calls = 0;
  const poller = createSystemMetricsPoller({
    request: async () => { calls += 1; return fullPayload(); },
    onUpdate: () => { throw new Error("render blew up"); },
    documentRef: environment.doc,
    setTimeoutFn: environment.setTimeoutFn,
    clearTimeoutFn: environment.clearTimeoutFn,
  });

  poller.start();
  await environment.advance();
  await environment.advance();

  // A broken renderer must not kill the poll loop.
  assert.equal(calls, 2);
  assert.equal(poller.isRunning(), true);
  poller.stop();
});
