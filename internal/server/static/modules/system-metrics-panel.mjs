// Home dashboard resource cards: CPU, memory, and network throughput, each
// coloured green / amber / red by how much pressure it is under.
//
// Kept out of overview-dashboard.mjs to stay inside the per-file size budget,
// and injected into it rather than imported by it, matching that module's
// take-every-dependency-by-injection design.

import { formatBytes, formatNumber } from "./formatters.mjs";

// Thresholds are percentages of capacity for CPU and memory. Both leave a wide
// green band: a build using most of the cores for a few seconds is normal and
// should not paint the dashboard red.
const CPU_WARNING = 70;
const CPU_DANGER = 90;
const MEMORY_WARNING = 75;
const MEMORY_DANGER = 90;
// Network is graded on absolute throughput rather than as a share of link speed.
// Reported link speed is a nominal figure (virtual adapters routinely claim
// 10Gbps), so a percentage of it would sit in the green band no matter what the
// machine was actually doing.
const NETWORK_WARNING_BYTES = 2 * 1024 * 1024;
const NETWORK_DANGER_BYTES = 20 * 1024 * 1024;

const DEFAULT_TEXT = Object.freeze({
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

function boundedNumber(value, maximum = Number.MAX_SAFE_INTEGER) {
  const number = Number(value);
  if (!Number.isFinite(number)) return 0;
  return Math.min(maximum, Math.max(0, number));
}

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  })[character]);
}

// A metric counts as present only when the server said so. "Unavailable" and
// "measured as zero" are different facts: the first hides the card, the second
// draws an idle bar.
function normalizeMetric(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

export function normalizeSystemMetrics(payload) {
  const source = normalizeMetric(payload);
  const cpu = normalizeMetric(source.cpu);
  const memory = normalizeMetric(source.memory);
  const network = normalizeMetric(source.network);
  return {
    cpu: {
      available: cpu.available === true,
      percent: boundedNumber(cpu.percent, 100),
    },
    memory: {
      available: memory.available === true,
      percent: boundedNumber(memory.percent, 100),
      usedBytes: boundedNumber(memory.usedBytes),
      totalBytes: boundedNumber(memory.totalBytes),
    },
    network: {
      available: network.available === true,
      rxBytesPerSec: boundedNumber(network.rxBytesPerSec),
      txBytesPerSec: boundedNumber(network.txBytesPerSec),
    },
  };
}

export function metricTone(kind, value) {
  const number = boundedNumber(value);
  if (kind === "cpu") return number >= CPU_DANGER ? "danger" : number >= CPU_WARNING ? "warning" : "ok";
  if (kind === "memory") return number >= MEMORY_DANGER ? "danger" : number >= MEMORY_WARNING ? "warning" : "ok";
  if (kind === "network") return number >= NETWORK_DANGER_BYTES ? "danger" : number >= NETWORK_WARNING_BYTES ? "warning" : "ok";
  return "ok";
}

// Byte and number formatting go through the shared regional formatters so these
// cards follow the user's locale and grouping preferences like every other
// figure in the app.
export function formatRate(value) {
  return `${formatBytes(boundedNumber(value))}/s`;
}

export function formatPercent(value) {
  return `${formatNumber(Math.round(boundedNumber(value, 100)), { maximumFractionDigits: 0 })}%`;
}

// The bar length is the metric's share of capacity. Network has no meaningful
// capacity, so its bar is scaled against the danger threshold and clamped, which
// makes it a pressure indicator rather than a false percentage.
function networkFill(bytesPerSec) {
  return Math.min(100, (boundedNumber(bytesPerSec) / NETWORK_DANGER_BYTES) * 100);
}

function metricCard({ kind, label, tone, value, detail, fill, t }) {
  const toneLabel = t(`tone${tone[0].toUpperCase()}${tone.slice(1)}`);
  // The tone word is rendered, not just the colour: colour alone would carry the
  // whole signal for anyone who cannot distinguish these hues.
  return `<div class="overview-metric-card" data-overview-metric="${escapeHtml(kind)}" data-tone="${escapeHtml(tone)}" role="group" aria-label="${escapeHtml(`${label} ${value} ${toneLabel}`)}">
    <span class="overview-metric-label">${escapeHtml(label)}</span>
    <strong class="overview-metric-value">${escapeHtml(value)}</strong>
    <div class="overview-metric-bar" role="presentation"><span style="width:${fill.toFixed(1)}%"></span></div>
    <small class="overview-metric-detail">${escapeHtml(detail ? `${toneLabel} · ${detail}` : toneLabel)}</small>
  </div>`;
}

export function renderSystemMetrics(payload, t = (key) => DEFAULT_TEXT[key] ?? key) {
  const model = normalizeSystemMetrics(payload);
  const cards = [];

  if (model.cpu.available) {
    cards.push(metricCard({
      kind: "cpu",
      label: t("cpu"),
      tone: metricTone("cpu", model.cpu.percent),
      value: formatPercent(model.cpu.percent),
      detail: "",
      fill: model.cpu.percent,
      t,
    }));
  }
  if (model.memory.available) {
    cards.push(metricCard({
      kind: "memory",
      label: t("memory"),
      tone: metricTone("memory", model.memory.percent),
      value: formatPercent(model.memory.percent),
      detail: model.memory.totalBytes > 0 ? `${formatBytes(model.memory.usedBytes)} / ${formatBytes(model.memory.totalBytes)}` : "",
      fill: model.memory.percent,
      t,
    }));
  }
  if (model.network.available) {
    cards.push(metricCard({
      kind: "networkDown",
      label: t("networkDown"),
      tone: metricTone("network", model.network.rxBytesPerSec),
      value: formatRate(model.network.rxBytesPerSec),
      detail: "",
      fill: networkFill(model.network.rxBytesPerSec),
      t,
    }));
    cards.push(metricCard({
      kind: "networkUp",
      label: t("networkUp"),
      tone: metricTone("network", model.network.txBytesPerSec),
      value: formatRate(model.network.txBytesPerSec),
      detail: "",
      fill: networkFill(model.network.txBytesPerSec),
      t,
    }));
  }

  // Nothing measurable on this platform (or no reading yet): render nothing
  // rather than an empty shell with zeroed bars.
  if (!cards.length) return "";

  return `<section class="overview-section overview-metrics settings-card" data-overview-section="system-metrics">
    <header class="overview-section-header"><div><h2>${escapeHtml(t("systemMetrics"))}</h2><p>${escapeHtml(t("systemMetricsHint"))}</p></div></header>
    <div class="overview-metrics-grid">${cards.join("")}</div>
  </section>`;
}

const DEFAULT_INTERVAL_MS = 3_000;
// After this many consecutive failures the poller gives up. Resource cards are
// supplementary; a server that is not answering should not be asked every three
// seconds forever.
const MAX_CONSECUTIVE_FAILURES = 3;

export function createSystemMetricsPoller({
  request,
  onUpdate,
  intervalMs = DEFAULT_INTERVAL_MS,
  documentRef,
  setTimeoutFn,
  clearTimeoutFn,
} = {}) {
  if (typeof request !== "function") throw new TypeError("system metrics request must be a function");

  const interval = Math.max(1_000, Number(intervalMs) || DEFAULT_INTERVAL_MS);
  const doc = documentRef === undefined ? globalThis.document : documentRef;
  const schedule = setTimeoutFn || globalThis.setTimeout?.bind(globalThis);
  const unschedule = clearTimeoutFn || globalThis.clearTimeout?.bind(globalThis);

  let running = false;
  let timer = null;
  let failures = 0;
  let inFlight = false;
  let visibilityListener = null;

  function emit(value) {
    try {
      onUpdate?.(value);
    } catch {
      // The consumer's render must not be able to kill the poll loop.
    }
  }

  function queue() {
    if (!running || !schedule) return;
    if (timer !== null) unschedule?.(timer);
    timer = schedule(() => {
      timer = null;
      void tick();
    }, interval);
  }

  async function tick() {
    if (!running || inFlight) return false;
    // A hidden tab keeps polling pointlessly and, in a packaged desktop shell,
    // keeps the machine awake. Skip the request but keep the loop alive so it
    // resumes without a restart.
    if (doc?.hidden) {
      queue();
      return false;
    }
    inFlight = true;
    try {
      const payload = await request("/api/system/metrics");
      if (!running) return false;
      failures = 0;
      emit(normalizeSystemMetrics(payload));
      return true;
    } catch {
      if (!running) return false;
      failures += 1;
      if (failures >= MAX_CONSECUTIVE_FAILURES) {
        // Hand back an all-unavailable model so the section disappears instead
        // of freezing on the last good reading.
        stop();
        emit(normalizeSystemMetrics({}));
        return false;
      }
      return false;
    } finally {
      inFlight = false;
      queue();
    }
  }

  function start() {
    if (running) return false;
    running = true;
    failures = 0;
    if (doc?.addEventListener && !visibilityListener) {
      visibilityListener = () => {
        if (running && !doc.hidden) void tick();
      };
      doc.addEventListener("visibilitychange", visibilityListener);
    }
    void tick();
    return true;
  }

  function stop() {
    if (!running) return false;
    running = false;
    if (timer !== null) {
      unschedule?.(timer);
      timer = null;
    }
    if (visibilityListener && doc?.removeEventListener) {
      doc.removeEventListener("visibilitychange", visibilityListener);
      visibilityListener = null;
    }
    return true;
  }

  return { start, stop, refresh: tick, isRunning: () => running };
}
