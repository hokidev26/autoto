import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatDuration, formatMoney, formatNumber, formatTimestamp } from "./formatters.mjs";
import { currentUILocale } from "./i18n.mjs";
import { usageHistoryMessage } from "./messages-usage-history.mjs";

export const usageHistoryBuckets = Object.freeze(["hour", "day", "month"]);
export const usageHistoryViews = Object.freeze(["chart", "records"]);
export const usageHistoryDatePresets = Object.freeze(["today", "last7", "last30", "thisMonth", "lastMonth"]);
export const usageHistoryMetrics = Object.freeze([
  "requests",
  "totalTokens",
  "inputTokens",
  "outputTokens",
  "reasoningTokens",
  "cachedInputTokens",
  "averageTTFTMs",
  "averageDurationMs",
  "totalCostUsd",
  "errors",
]);
export const usageHistoryStackableMetrics = Object.freeze([
  "requests",
  "totalTokens",
  "inputTokens",
  "outputTokens",
  "reasoningTokens",
  "cachedInputTokens",
  "totalCostUsd",
  "errors",
]);

const metricFields = Object.freeze({ requests: "requestCount" });
const summaryFields = Object.freeze([
  "requestCount",
  "inputTokens",
  "outputTokens",
  "totalTokens",
  "reasoningTokens",
  "cachedInputTokens",
  "totalCostUsd",
  "averageTTFTMs",
  "averageDurationMs",
  "errors",
  "successRate",
]);
const itemNumberFields = Object.freeze([
  "inputTokens",
  "outputTokens",
  "totalTokens",
  "reasoningTokens",
  "cachedInputTokens",
  "ttftMs",
  "durationMs",
  "costUsd",
]);
const trendNumberFields = Object.freeze(summaryFields);
const stackedPalette = Object.freeze(["#4f63df", "#22a06b", "#d97706", "#7c3aed", "#c2410c", "#64748b"]);
const csvFields = Object.freeze([
  "createdAt", "agentTitle", "kind", "provider", "model",
  "inputTokens", "outputTokens", "totalTokens", "reasoningTokens", "cachedInputTokens",
  "ttftMs", "durationMs", "costUsd", "status", "errorMessage",
]);

function t(key, params = {}) {
  return usageHistoryMessage(key, params, currentUILocale());
}

export function normalizeUsageHistoryNumber(value, fallback = 0) {
  const number = Number(value);
  return Number.isFinite(number) ? Math.max(0, number) : fallback;
}

export function normalizeUsageHistoryText(value, maximum = 1000) {
  return String(value ?? "").slice(0, maximum);
}

function normalizeDateFilter(value) {
  const text = normalizeUsageHistoryText(value, 10).trim();
  return /^\d{4}-\d{2}-\d{2}$/.test(text) ? text : "";
}

function normalizeChoice(value, choices, fallback) {
  const text = normalizeUsageHistoryText(value, 40).trim();
  return choices.includes(text) ? text : fallback;
}

function normalizeTextArray(value, maximum = 500) {
  if (!Array.isArray(value)) return [];
  const seen = new Set();
  const result = [];
  for (const entry of value) {
    const text = normalizeUsageHistoryText(entry, 300).trim();
    if (!text || seen.has(text)) continue;
    seen.add(text);
    result.push(text);
    if (result.length >= maximum) break;
  }
  return result;
}

export function normalizeUsageHistoryFilters(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  return {
    provider: normalizeUsageHistoryText(source.provider, 256).trim(),
    model: normalizeUsageHistoryText(source.model, 256).trim(),
    kind: normalizeUsageHistoryText(source.kind, 256).trim(),
    from: normalizeDateFilter(source.from),
    to: normalizeDateFilter(source.to),
  };
}

function normalizeMetricObject(value, fields) {
  const source = value && typeof value === "object" ? value : {};
  return Object.fromEntries(fields.map((field) => [field, normalizeUsageHistoryNumber(source[field])]));
}

export function normalizeUsageHistoryTrendItem(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  return {
    bucket: normalizeUsageHistoryText(source.bucket, 200),
    ...normalizeMetricObject(source, trendNumberFields),
  };
}

export function normalizeUsageHistoryStackedItem(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  return {
    bucket: normalizeUsageHistoryText(source.bucket, 200),
    provider: normalizeUsageHistoryText(source.provider, 300),
    ...normalizeMetricObject(source, trendNumberFields),
  };
}

export function normalizeUsageHistoryDistributionItem(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  return {
    provider: normalizeUsageHistoryText(source.provider, 300),
    ...normalizeMetricObject(source, trendNumberFields),
  };
}

export function normalizeUsageHistoryItem(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  const item = {
    id: normalizeUsageHistoryText(source.id, 300),
    createdAt: normalizeUsageHistoryText(source.createdAt, 200),
    agentId: normalizeUsageHistoryText(source.agentId, 300),
    agentTitle: normalizeUsageHistoryText(source.agentTitle, 500),
    runId: normalizeUsageHistoryText(source.runId, 300),
    messageId: normalizeUsageHistoryText(source.messageId, 300),
    kind: normalizeUsageHistoryText(source.kind, 200),
    provider: normalizeUsageHistoryText(source.provider, 300),
    model: normalizeUsageHistoryText(source.model, 500),
    errorMessage: normalizeUsageHistoryText(source.errorMessage, 2000),
    status: normalizeUsageHistoryText(source.status, 100),
  };
  itemNumberFields.forEach((field) => { item[field] = normalizeUsageHistoryNumber(source[field]); });
  return item;
}

export function normalizeUsageHistoryResponse(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  const options = source.options && typeof source.options === "object" ? source.options : {};
  return {
    generatedAt: normalizeUsageHistoryText(source.generatedAt, 200),
    summary: normalizeMetricObject(source.summary, summaryFields),
    trend: (Array.isArray(source.trend) ? source.trend : []).slice(0, 1000).map(normalizeUsageHistoryTrendItem),
    trendTruncated: Boolean(source.trendTruncated),
    stacked: (Array.isArray(source.stacked) ? source.stacked : []).slice(0, 6000).map(normalizeUsageHistoryStackedItem),
    distribution: (Array.isArray(source.distribution) ? source.distribution : []).slice(0, 20).map(normalizeUsageHistoryDistributionItem),
    options: {
      providers: normalizeTextArray(options.providers),
      models: normalizeTextArray(options.models),
      kinds: normalizeTextArray(options.kinds),
    },
    items: (Array.isArray(source.items) ? source.items : []).slice(0, 5000).map(normalizeUsageHistoryItem),
    nextCursor: normalizeUsageHistoryText(source.nextCursor, 2000),
  };
}

export function createUsageHistoryState(value = {}) {
  const source = value && typeof value === "object" ? value : {};
  const normalized = normalizeUsageHistoryResponse(source);
  const hasPayload = Boolean(source.summary || source.generatedAt || source.items || source.trend || source.stacked);
  return {
    filters: normalizeUsageHistoryFilters(source.filters),
    bucket: normalizeChoice(source.bucket, usageHistoryBuckets, "day"),
    metric: normalizeChoice(source.metric, usageHistoryMetrics, "totalTokens"),
    view: normalizeChoice(source.view, usageHistoryViews, "chart"),
    status: normalizeChoice(source.status, ["idle", "loading", "ready", "error", "loadingMore"], hasPayload ? "ready" : "idle"),
    error: normalizeUsageHistoryText(source.error, 2000),
    seq: Math.floor(normalizeUsageHistoryNumber(source.seq)),
    ...normalized,
    selectedTrendIndex: normalizeTrendPointIndex(source.selectedTrendIndex, (normalized.trend || []).length),
  };
}

export function localTimezoneOffsetMinutes(now = new Date()) {
  const offset = -Math.round(Number(now.getTimezoneOffset?.()) || 0);
  return Number.isFinite(offset) ? Math.max(-840, Math.min(840, offset)) : 0;
}

export function formatUsageHistoryLocalDate(date) {
  const value = date instanceof Date ? date : new Date(date);
  if (Number.isNaN(value.getTime())) return "";
  const month = String(value.getMonth() + 1).padStart(2, "0");
  const day = String(value.getDate()).padStart(2, "0");
  return `${value.getFullYear()}-${month}-${day}`;
}

export function usageHistoryDateRangeForPreset(preset, now = new Date()) {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const format = (date) => formatUsageHistoryLocalDate(date);
  switch (preset) {
    case "today":
      return { from: format(today), to: format(today) };
    case "last7": {
      const from = new Date(today);
      from.setDate(from.getDate() - 6);
      return { from: format(from), to: format(today) };
    }
    case "last30": {
      const from = new Date(today);
      from.setDate(from.getDate() - 29);
      return { from: format(from), to: format(today) };
    }
    case "thisMonth":
      return { from: format(new Date(today.getFullYear(), today.getMonth(), 1)), to: format(today) };
    case "lastMonth": {
      const start = new Date(today.getFullYear(), today.getMonth() - 1, 1);
      const end = new Date(today.getFullYear(), today.getMonth(), 0);
      return { from: format(start), to: format(end) };
    }
    default:
      return { from: "", to: "" };
  }
}

export function matchingUsageHistoryDatePreset(filters, now = new Date()) {
  const normalized = normalizeUsageHistoryFilters(filters);
  for (const preset of usageHistoryDatePresets) {
    const range = usageHistoryDateRangeForPreset(preset, now);
    if (range.from === normalized.from && range.to === normalized.to) return preset;
  }
  return "";
}

export function usageHistoryUsesStackedChart(metric) {
  return usageHistoryStackableMetrics.includes(metric);
}

function clampTimezoneOffset(value, fallbackNow = new Date()) {
  if (value == null || value === "") return localTimezoneOffsetMinutes(fallbackNow);
  const number = Number(value);
  if (!Number.isFinite(number)) return localTimezoneOffsetMinutes(fallbackNow);
  return Math.max(-840, Math.min(840, Math.round(number)));
}

export function buildUsageHistoryURL({ filters = {}, bucket = "day", limit = 50, cursor = "", tzOffset } = {}) {
  const normalizedFilters = normalizeUsageHistoryFilters(filters);
  const params = new URLSearchParams();
  ["provider", "model", "kind", "from", "to"].forEach((key) => {
    if (normalizedFilters[key]) params.set(key, normalizedFilters[key]);
  });
  params.set("bucket", normalizeChoice(bucket, usageHistoryBuckets, "day"));
  params.set("tzOffset", String(clampTimezoneOffset(tzOffset)));
  params.set("limit", String(Math.min(50, Math.max(1, Math.floor(normalizeUsageHistoryNumber(limit, 50))))));
  const normalizedCursor = normalizeUsageHistoryText(cursor, 2000);
  if (normalizedCursor) params.set("cursor", normalizedCursor);
  return `/api/usage/history?${params.toString()}`;
}

function itemIdentity(item) {
  if (item.id) return `id:${item.id}`;
  return `fields:${item.createdAt}\u0000${item.agentId}\u0000${item.runId}\u0000${item.messageId}\u0000${item.provider}\u0000${item.model}`;
}

export function appendUsageHistoryItems(current, incoming) {
  const result = [];
  const seen = new Set();
  [...(Array.isArray(current) ? current : []), ...(Array.isArray(incoming) ? incoming : [])]
    .map(normalizeUsageHistoryItem)
    .forEach((item) => {
      const key = itemIdentity(item);
      if (seen.has(key)) return;
      seen.add(key);
      result.push(item);
    });
  return result;
}

function csvCell(value) {
  const text = String(value ?? "");
  if (/[",\r\n]/.test(text)) return `"${text.replace(/"/g, "\"\"")}"`;
  return text;
}

export function buildUsageHistoryCSV(items) {
  const header = csvFields.join(",");
  const rows = (Array.isArray(items) ? items : []).map((value) => {
    const item = normalizeUsageHistoryItem(value);
    const agent = item.agentTitle || item.agentId;
    return csvFields.map((field) => csvCell(field === "agentTitle" ? agent : item[field])).join(",");
  });
  return [header, ...rows].join("\r\n");
}

function formatPercent(value) {
  const number = normalizeUsageHistoryNumber(value);
  const percent = number <= 1 ? number * 100 : number;
  return `${Math.min(100, percent).toFixed(percent >= 99.95 || Number.isInteger(percent) ? 0 : 1)}%`;
}

function formatMetric(metric, value) {
  const number = normalizeUsageHistoryNumber(value);
  if (metric === "totalCostUsd") return formatMoney(number);
  if (metric === "averageTTFTMs" || metric === "averageDurationMs") return formatDuration(number);
  return formatNumber(number);
}

function metricValue(point, metric) {
  return normalizeUsageHistoryNumber(point?.[metricFields[metric] || metric]);
}

function formatAxisMetric(metric, value) {
  const number = normalizeUsageHistoryNumber(value);
  if (metric === "totalCostUsd") return formatMoney(number);
  if (metric === "averageTTFTMs" || metric === "averageDurationMs") return formatDuration(number);
  return formatNumber(number, { notation: "compact", maximumFractionDigits: 1 });
}

function svgNumber(value) {
  return Number(value).toFixed(2).replace(/\.00$/, "");
}

function providerLabel(provider) {
  const name = normalizeUsageHistoryText(provider, 300).trim();
  return name || t("usageHistory.trend.other");
}

function stackedColor(provider, index) {
  if (!normalizeUsageHistoryText(provider, 300).trim()) return stackedPalette[stackedPalette.length - 1];
  return stackedPalette[index % (stackedPalette.length - 1)];
}

function stackedSeriesOrder(stacked, distribution) {
  const names = [];
  const seen = new Set();
  const push = (provider) => {
    const name = normalizeUsageHistoryText(provider, 300);
    if (seen.has(name)) return;
    seen.add(name);
    names.push(name);
  };
  (Array.isArray(distribution) ? distribution : []).forEach((row) => push(row.provider));
  (Array.isArray(stacked) ? stacked : []).forEach((row) => push(row.provider));
  const named = names.filter((name) => name);
  if (seen.has("")) named.push("");
  return named;
}

function trendPointLabel(point, metric, value) {
  const parts = [`${point.bucket || "—"}: ${formatMetric(metric, value)}`];
  if (metric !== "requests") {
    parts.push(t("usageHistory.trend.pointRequests", { count: formatNumber(metricValue(point, "requests")) }));
  }
  const totalTokens = metricValue(point, "totalTokens");
  if (totalTokens > 0 && metric !== "totalTokens") {
    parts.push(t("usageHistory.trend.pointTokens", { total: formatNumber(totalTokens) }));
  }
  if (totalTokens > 0 && metric !== "inputTokens" && metric !== "outputTokens") {
    parts.push(t("usageHistory.trend.pointTokenSplit", {
      input: formatNumber(metricValue(point, "inputTokens")),
      output: formatNumber(metricValue(point, "outputTokens")),
    }));
  }
  return parts.join(" · ");
}

function stackedBucketRows(stacked, bucket) {
  return (Array.isArray(stacked) ? stacked : []).filter((row) => row.bucket === bucket);
}

function stackedPointLabel(point, stacked, metric, series) {
  const rows = stackedBucketRows(stacked, point.bucket);
  const total = rows.reduce((sum, row) => sum + metricValue(row, metric), 0);
  const parts = [`${point.bucket || "—"}: ${formatMetric(metric, total || metricValue(point, metric))}`];
  const ordered = series.length ? series : stackedSeriesOrder(rows, []);
  ordered.forEach((provider) => {
    const row = rows.find((entry) => entry.provider === provider);
    const value = metricValue(row, metric);
    if (value <= 0) return;
    parts.push(`${providerLabel(provider)} ${formatMetric(metric, value)}`);
  });
  return parts.join(" · ");
}

export function renderUsageTrendSVG(value, metric = "totalTokens", options = {}) {
  const trend = (Array.isArray(value) ? value : []).map(normalizeUsageHistoryTrendItem);
  const selectedMetric = normalizeChoice(metric, usageHistoryMetrics, "totalTokens");
  if (!trend.length) {
    return `<div class="uh-chart-empty">${escapeHtml(t("usageHistory.trend.empty"))}</div>`;
  }

  const width = Math.max(420, normalizeUsageHistoryNumber(options.width, 760));
  const height = Math.max(220, normalizeUsageHistoryNumber(options.height, 280));
  const margin = { top: 20, right: 18, bottom: 42, left: 62 };
  const plotWidth = width - margin.left - margin.right;
  const plotHeight = height - margin.top - margin.bottom;
  const values = trend.map((point) => metricValue(point, selectedMetric));
  const maximum = Math.max(1, ...values);
  const x = (index) => margin.left + (trend.length <= 1 ? plotWidth / 2 : (index / (trend.length - 1)) * plotWidth);
  const y = (number) => margin.top + plotHeight - (number / maximum) * plotHeight;
  const path = trend.map((point, index) => `${index ? "L" : "M"}${svgNumber(x(index))},${svgNumber(y(values[index]))}`).join(" ");
  const grid = Array.from({ length: 5 }, (_, index) => {
    const ratio = index / 4;
    const lineY = margin.top + ratio * plotHeight;
    const labelValue = maximum * (1 - ratio);
    return `<g class="uh-chart-grid"><line x1="${margin.left}" y1="${svgNumber(lineY)}" x2="${svgNumber(width - margin.right)}" y2="${svgNumber(lineY)}"></line><text x="${margin.left - 9}" y="${svgNumber(lineY + 4)}" text-anchor="end">${escapeHtml(formatAxisMetric(selectedMetric, labelValue))}</text></g>`;
  }).join("");
  const labelIndexes = [...new Set([0, Math.floor((trend.length - 1) / 2), trend.length - 1])];
  const xLabels = labelIndexes.map((index) => `<text class="uh-chart-x-label" x="${svgNumber(x(index))}" y="${svgNumber(height - 12)}" text-anchor="${index === 0 ? "start" : (index === trend.length - 1 ? "end" : "middle")}">${escapeHtml(trend[index].bucket || "—")}</text>`).join("");
  const selectedIndex = normalizeTrendPointIndex(options.selectedIndex, trend.length);
  const points = trend.map((point, index) => {
    const label = trendPointLabel(point, selectedMetric, values[index]);
    const active = index === selectedIndex;
    return `<circle class="uh-chart-point${active ? " is-selected" : ""}" cx="${svgNumber(x(index))}" cy="${svgNumber(y(values[index]))}" r="${active ? 6 : 4}"`
      + ` tabindex="0" role="button" aria-label="${escapeAttr(label)}" aria-pressed="${active ? "true" : "false"}"`
      + ` data-usage-trend-point="${escapeAttr(String(index))}" data-usage-trend-label="${escapeAttr(label)}"`
      + `><title>${escapeHtml(label)}</title></circle>`;
  }).join("");
  const ariaLabel = `${t("usageHistory.trend.title")}: ${t(`usageHistory.trend.metrics.${selectedMetric}`)}`;
  return `<svg class="uh-trend-svg" viewBox="0 0 ${svgNumber(width)} ${svgNumber(height)}" role="img" aria-label="${escapeAttr(ariaLabel)}" preserveAspectRatio="none">${grid}<line class="uh-chart-axis" x1="${margin.left}" y1="${margin.top}" x2="${margin.left}" y2="${svgNumber(margin.top + plotHeight)}"></line><line class="uh-chart-axis" x1="${margin.left}" y1="${svgNumber(margin.top + plotHeight)}" x2="${svgNumber(width - margin.right)}" y2="${svgNumber(margin.top + plotHeight)}"></line><path class="uh-chart-line" d="${escapeAttr(path)}"></path>${points}${xLabels}</svg>`;
}

export function renderUsageStackedSVG(trendValue, stackedValue, metric = "totalTokens", options = {}) {
  const trend = (Array.isArray(trendValue) ? trendValue : []).map(normalizeUsageHistoryTrendItem);
  const stacked = (Array.isArray(stackedValue) ? stackedValue : []).map(normalizeUsageHistoryStackedItem);
  const selectedMetric = normalizeChoice(metric, usageHistoryMetrics, "totalTokens");
  if (!trend.length) {
    return `<div class="uh-chart-empty">${escapeHtml(t("usageHistory.trend.empty"))}</div>`;
  }
  const series = stackedSeriesOrder(stacked, options.distribution);
  const width = Math.max(420, normalizeUsageHistoryNumber(options.width, 760));
  const height = Math.max(220, normalizeUsageHistoryNumber(options.height, 280));
  const margin = { top: 20, right: 18, bottom: 42, left: 62 };
  const plotWidth = width - margin.left - margin.right;
  const plotHeight = height - margin.top - margin.bottom;
  const totals = trend.map((point) => stackedBucketRows(stacked, point.bucket).reduce((sum, row) => sum + metricValue(row, selectedMetric), 0));
  const maximum = Math.max(1, ...totals, ...trend.map((point) => metricValue(point, selectedMetric)));
  const slot = plotWidth / Math.max(1, trend.length);
  const barWidth = Math.max(8, Math.min(36, slot * 0.62));
  const x = (index) => margin.left + slot * index + (slot - barWidth) / 2;
  const grid = Array.from({ length: 5 }, (_, index) => {
    const ratio = index / 4;
    const lineY = margin.top + ratio * plotHeight;
    const labelValue = maximum * (1 - ratio);
    return `<g class="uh-chart-grid"><line x1="${margin.left}" y1="${svgNumber(lineY)}" x2="${svgNumber(width - margin.right)}" y2="${svgNumber(lineY)}"></line><text x="${margin.left - 9}" y="${svgNumber(lineY + 4)}" text-anchor="end">${escapeHtml(formatAxisMetric(selectedMetric, labelValue))}</text></g>`;
  }).join("");
  const labelIndexes = [...new Set([0, Math.floor((trend.length - 1) / 2), trend.length - 1])];
  const xLabels = labelIndexes.map((index) => `<text class="uh-chart-x-label" x="${svgNumber(x(index) + barWidth / 2)}" y="${svgNumber(height - 12)}" text-anchor="${index === 0 ? "start" : (index === trend.length - 1 ? "end" : "middle")}">${escapeHtml(trend[index].bucket || "—")}</text>`).join("");
  const selectedIndex = normalizeTrendPointIndex(options.selectedIndex, trend.length);
  const bars = trend.map((point, index) => {
    const rows = stackedBucketRows(stacked, point.bucket);
    const label = stackedPointLabel(point, stacked, selectedMetric, series);
    const active = index === selectedIndex;
    let yOffset = 0;
    const slices = series.map((provider, seriesIndex) => {
      const row = rows.find((entry) => entry.provider === provider);
      const value = metricValue(row, selectedMetric);
      if (value <= 0) return "";
      const sliceHeight = (value / maximum) * plotHeight;
      const y = margin.top + plotHeight - yOffset - sliceHeight;
      yOffset += sliceHeight;
      return `<rect class="uh-stack-bar" x="${svgNumber(x(index))}" y="${svgNumber(y)}" width="${svgNumber(barWidth)}" height="${svgNumber(Math.max(0, sliceHeight))}" fill="${escapeAttr(stackedColor(provider, seriesIndex))}"></rect>`;
    }).join("");
    // The hit target stays full-plot-height so the column is easy to click.
    // Selection/focus drawing hugs the painted bar instead, because an outline
    // on the <g> follows the tall hit rect and reads as a black frame.
    const markerHeight = Math.max(8, yOffset);
    const markerY = margin.top + plotHeight - markerHeight;
    const marker = `<rect class="uh-stack-focus" x="${svgNumber(x(index) - 1.5)}" y="${svgNumber(markerY - 1.5)}" width="${svgNumber(barWidth + 3)}" height="${svgNumber((yOffset > 0 ? yOffset : markerHeight) + 3)}" rx="4"></rect>`;
    return `<g class="uh-stack-bucket${active ? " is-selected" : ""}" tabindex="0" role="button" aria-label="${escapeAttr(label)}" aria-pressed="${active ? "true" : "false"}" data-usage-trend-point="${escapeAttr(String(index))}" data-usage-trend-label="${escapeAttr(label)}"><rect class="uh-stack-hit" x="${svgNumber(x(index))}" y="${margin.top}" width="${svgNumber(barWidth)}" height="${svgNumber(plotHeight)}"></rect>${slices}${marker}<title>${escapeHtml(label)}</title></g>`;
  }).join("");
  const ariaLabel = `${t("usageHistory.trend.title")}: ${t(`usageHistory.trend.metrics.${selectedMetric}`)}`;
  return `<svg class="uh-trend-svg uh-stacked-svg" viewBox="0 0 ${svgNumber(width)} ${svgNumber(height)}" role="img" aria-label="${escapeAttr(ariaLabel)}" preserveAspectRatio="none">${grid}<line class="uh-chart-axis" x1="${margin.left}" y1="${margin.top}" x2="${margin.left}" y2="${svgNumber(margin.top + plotHeight)}"></line><line class="uh-chart-axis" x1="${margin.left}" y1="${svgNumber(margin.top + plotHeight)}" x2="${svgNumber(width - margin.right)}" y2="${svgNumber(margin.top + plotHeight)}"></line>${bars}${xLabels}</svg>`;
}

export function renderUsageHistoryChart(state = {}, options = {}) {
  const usage = createUsageHistoryState(state);
  if (usageHistoryUsesStackedChart(usage.metric) && usage.stacked.length) {
    return renderUsageStackedSVG(usage.trend, usage.stacked, usage.metric, {
      selectedIndex: usage.selectedTrendIndex,
      distribution: usage.distribution,
      width: options.width,
      height: options.height,
    });
  }
  return renderUsageTrendSVG(usage.trend, usage.metric, { selectedIndex: usage.selectedTrendIndex, width: options.width, height: options.height });
}

export function normalizeTrendPointIndex(value, length) {
  const count = Number(length);
  if (!Number.isFinite(count) || count <= 0) return -1;
  const index = Number(value);
  if (!Number.isFinite(index)) return -1;
  const rounded = Math.trunc(index);
  return rounded >= 0 && rounded < count ? rounded : -1;
}

function readoutMarkup(bucket, label) {
  return `<div class="uh-trend-readout-value" data-usage-trend-readout role="status" aria-live="polite">`
    + `<strong>${escapeHtml(bucket || "—")}</strong>`
    + `<span>${escapeHtml(label)}</span>`
    + `<button type="button" class="uh-button uh-trend-readout-clear" data-usage-trend-clear>${escapeHtml(t("usageHistory.trend.readoutClear"))}</button>`
    + `</div>`;
}

export function renderTrendReadout(trend, metric, selectedIndex, stacked = []) {
  const items = (Array.isArray(trend) ? trend : []).map(normalizeUsageHistoryTrendItem);
  const index = normalizeTrendPointIndex(selectedIndex, items.length);
  if (index < 0) {
    return `<p class="uh-trend-readout-hint" data-usage-trend-readout>${escapeHtml(t("usageHistory.trend.readoutHint"))}</p>`;
  }
  const selectedMetric = normalizeChoice(metric, usageHistoryMetrics, "totalTokens");
  const point = items[index];
  const stackedRows = (Array.isArray(stacked) ? stacked : []).map(normalizeUsageHistoryStackedItem);
  const label = stackedRows.length && usageHistoryUsesStackedChart(selectedMetric)
    ? stackedPointLabel(point, stackedRows, selectedMetric, stackedSeriesOrder(stackedRows, []))
    : trendPointLabel(point, selectedMetric, metricValue(point, selectedMetric));
  return readoutMarkup(point.bucket, label);
}

function formatCompactCount(value) {
  return formatNumber(normalizeUsageHistoryNumber(value), { notation: "compact", maximumFractionDigits: 1 });
}

function renderSummaryCard(title, value, subtitle = "") {
  return `<section class="uh-summary-card settings-stat-card"><div class="uh-summary-label">${escapeHtml(title)}</div><strong>${escapeHtml(value)}</strong><small>${escapeHtml(subtitle || "—")}</small></section>`;
}

function renderSummary(summary) {
  const usageCards = [
    renderSummaryCard(t("usageHistory.summary.totalTokens"), formatCompactCount(summary.totalTokens), t("usageHistory.summary.inputOutput", { input: formatNumber(summary.inputTokens), output: formatNumber(summary.outputTokens) })),
    renderSummaryCard(t("usageHistory.summary.requests"), formatCompactCount(summary.requestCount), t("usageHistory.summary.exact", { value: formatNumber(summary.requestCount) })),
    renderSummaryCard(t("usageHistory.summary.totalCost"), formatMoney(summary.totalCostUsd)),
    renderSummaryCard(t("usageHistory.summary.cachedInputTokens"), formatCompactCount(summary.cachedInputTokens), t("usageHistory.summary.exact", { value: formatNumber(summary.cachedInputTokens) })),
  ].join("");
  const performanceCards = [
    renderSummaryCard(t("usageHistory.summary.averageDuration"), formatOptionalDuration(summary.averageDurationMs)),
    renderSummaryCard(t("usageHistory.summary.averageTTFT"), formatOptionalDuration(summary.averageTTFTMs)),
    renderSummaryCard(t("usageHistory.summary.reasoningTokens"), formatCompactCount(summary.reasoningTokens), t("usageHistory.summary.exact", { value: formatNumber(summary.reasoningTokens) })),
    renderSummaryCard(t("usageHistory.summary.errors"), formatCompactCount(summary.errors), t("usageHistory.summary.successRate", { rate: formatPercent(summary.successRate) })),
  ].join("");
  return `<div class="uh-summary-groups">
    <section class="uh-summary-group" aria-label="${escapeAttr(t("usageHistory.summary.usageGroup"))}"><div class="uh-summary-grid settings-stat-grid uh-summary-grid-usage">${usageCards}</div></section>
    <section class="uh-summary-group" aria-label="${escapeAttr(t("usageHistory.summary.performanceGroup"))}"><div class="uh-summary-grid settings-stat-grid uh-summary-grid-perf">${performanceCards}</div></section>
  </div>`;
}

function renderBucketControls(bucket) {
  return `<div class="uh-bucket-switch" role="group" aria-label="${escapeAttr(t("usageHistory.trend.title"))}">${usageHistoryBuckets.map((value) => `<button type="button" data-usage-bucket="${escapeAttr(value)}" class="${value === bucket ? "active" : ""}" aria-pressed="${value === bucket ? "true" : "false"}">${escapeHtml(t(`usageHistory.trend.${value}`))}</button>`).join("")}</div>`;
}

function renderMetricSelect(metric) {
  return `<label class="uh-metric-field settings-form-field" for="usageHistoryMetric"><span>${escapeHtml(t("usageHistory.trend.metricLabel"))}</span><select id="usageHistoryMetric">${usageHistoryMetrics.map((value) => `<option value="${escapeAttr(value)}"${value === metric ? " selected" : ""}>${escapeHtml(t(`usageHistory.trend.metrics.${value}`))}</option>`).join("")}</select></label>`;
}

function renderViewTabs(view) {
  return `<div class="uh-view-tabs" role="tablist" aria-label="${escapeAttr(t("usageHistory.views.label"))}">${usageHistoryViews.map((value) => `<button type="button" role="tab" id="usageHistoryView-${value}" data-usage-view="${escapeAttr(value)}" class="${value === view ? "active" : ""}" aria-selected="${value === view ? "true" : "false"}" aria-controls="${value === "chart" ? "usageHistoryChartPanel" : "usageHistoryRecordsPanel"}">${escapeHtml(t(`usageHistory.views.${value}`))}</button>`).join("")}</div>`;
}

function renderDatePresets(filters, now = new Date()) {
  const active = matchingUsageHistoryDatePreset(filters, now);
  return `<div class="uh-date-presets" role="group" aria-label="${escapeAttr(t("usageHistory.presets.label"))}">${usageHistoryDatePresets.map((value) => `<button type="button" data-usage-preset="${escapeAttr(value)}" class="${value === active ? "active" : ""}" aria-pressed="${value === active ? "true" : "false"}">${escapeHtml(t(`usageHistory.presets.${value}`))}</button>`).join("")}</div>`;
}

function renderOptions(values, selected, emptyLabel) {
  const choices = normalizeTextArray([...(Array.isArray(values) ? values : []), selected]);
  return `<option value="">${escapeHtml(emptyLabel)}</option>${choices.map((value) => `<option value="${escapeAttr(value)}"${value === selected ? " selected" : ""}>${escapeHtml(value)}</option>`).join("")}`;
}

function renderFilters(state) {
  const { filters, options } = state;
  return `<section class="uh-panel uh-filter-panel settings-card" aria-labelledby="usageHistoryFiltersTitle"><div class="uh-section-head settings-card-header"><div><h3 id="usageHistoryFiltersTitle" class="settings-card-title">${escapeHtml(t("usageHistory.filters.title"))}</h3></div></div>${renderDatePresets(filters)}<form id="usageHistoryFilters" class="uh-filter-form settings-form-grid">
    <label class="settings-form-field" for="usageHistoryProvider"><span>${escapeHtml(t("usageHistory.filters.provider"))}</span><select id="usageHistoryProvider">${renderOptions(options.providers, filters.provider, t("usageHistory.filters.allProviders"))}</select></label>
    <label class="settings-form-field" for="usageHistoryModel"><span>${escapeHtml(t("usageHistory.filters.model"))}</span><select id="usageHistoryModel">${renderOptions(options.models, filters.model, t("usageHistory.filters.allModels"))}</select></label>
    <label class="settings-form-field" for="usageHistoryKind"><span>${escapeHtml(t("usageHistory.filters.kind"))}</span><select id="usageHistoryKind">${renderOptions(options.kinds, filters.kind, t("usageHistory.filters.allKinds"))}</select></label>
    <label class="settings-form-field" for="usageHistoryFrom"><span>${escapeHtml(t("usageHistory.filters.from"))}</span><input id="usageHistoryFrom" type="date" value="${escapeAttr(filters.from)}"></label>
    <label class="settings-form-field" for="usageHistoryTo"><span>${escapeHtml(t("usageHistory.filters.to"))}</span><input id="usageHistoryTo" type="date" value="${escapeAttr(filters.to)}"></label>
    <div class="uh-filter-actions settings-inline-actions"><button class="uh-button primary" type="submit">${escapeHtml(t("usageHistory.filters.apply"))}</button><button id="usageHistoryReset" class="uh-button" type="button">${escapeHtml(t("usageHistory.filters.reset"))}</button></div>
  </form></section>`;
}

function statusTone(status) {
  const value = status.toLowerCase();
  if (["success", "completed", "ok"].includes(value)) return "success";
  if (["error", "failed", "failure"].includes(value)) return "error";
  if (["running", "streaming", "pending"].includes(value)) return "pending";
  return "neutral";
}

function statusLabel(status, hasError = false) {
  const value = String(status || "").trim().toLowerCase();
  if (["success", "completed", "ok"].includes(value)) return t("usageHistory.history.statusSuccess");
  if (["error", "failed", "failure"].includes(value) || hasError) return t("usageHistory.history.statusError");
  if (["running", "streaming", "pending"].includes(value)) return t("usageHistory.history.statusPending");
  return status || t("usageHistory.history.statusUnknown");
}

function formatOptionalDuration(value) {
  const number = normalizeUsageHistoryNumber(value);
  return number > 0 ? formatDuration(number) : t("usageHistory.history.unknown");
}

function renderTokenBreakdown(item) {
  const extras = [];
  if (item.reasoningTokens > 0) extras.push(`${t("usageHistory.history.reasoning")} ${formatNumber(item.reasoningTokens)}`);
  if (item.cachedInputTokens > 0) extras.push(`${t("usageHistory.history.cached")} ${formatNumber(item.cachedInputTokens)}`);
  const extra = extras.length
    ? `<span class="uh-token-extra">${escapeHtml(extras.join(" · "))}</span>`
    : "";
  const title = [
    `${t("usageHistory.history.tokens")} ${formatNumber(item.totalTokens)}`,
    `${t("usageHistory.history.input")} ${formatNumber(item.inputTokens)}`,
    `${t("usageHistory.history.output")} ${formatNumber(item.outputTokens)}`,
    `${t("usageHistory.history.reasoning")} ${formatNumber(item.reasoningTokens)}`,
    `${t("usageHistory.history.cached")} ${formatNumber(item.cachedInputTokens)}`,
  ].join(" · ");
  return `<div class="uh-token-metrics" title="${escapeAttr(title)}">`
    + `<span class="uh-token-total">${escapeHtml(formatNumber(item.totalTokens))}</span>`
    + `<span class="uh-token-split">`
    + `<span class="uh-token-in"><span aria-hidden="true">↑</span>${escapeHtml(formatNumber(item.inputTokens))}</span>`
    + `<span class="uh-token-out"><span aria-hidden="true">↓</span>${escapeHtml(formatNumber(item.outputTokens))}</span>`
    + extra
    + `</span></div>`;
}

function renderHistoryRow(value) {
  const item = normalizeUsageHistoryItem(value);
  const agent = item.agentTitle || item.agentId || t("usageHistory.history.unknownAgent");
  const status = statusLabel(item.status, Boolean(item.errorMessage));
  return `<tr data-usage-request="${escapeAttr(item.id)}">
    <td><time datetime="${escapeAttr(item.createdAt)}">${escapeHtml(item.createdAt ? formatTimestamp(item.createdAt) : t("usageHistory.history.unknown"))}</time></td>
    <td><strong title="${escapeAttr(item.agentId)}">${escapeHtml(agent)}</strong></td>
    <td><span class="uh-kind-pill">${escapeHtml(item.kind || t("usageHistory.history.unknown"))}</span></td>
    <td>${escapeHtml(item.provider || t("usageHistory.history.unknown"))}</td>
    <td title="${escapeAttr(item.model)}">${escapeHtml(item.model || t("usageHistory.history.unknown"))}</td>
    <td class="uh-token-cell">${renderTokenBreakdown(item)}</td>
    <td>${escapeHtml(formatOptionalDuration(item.ttftMs))}</td>
    <td>${escapeHtml(formatOptionalDuration(item.durationMs))}</td>
    <td>${escapeHtml(formatMoney(item.costUsd))}</td>
    <td><span class="uh-status ${statusTone(item.status || (item.errorMessage ? "error" : ""))}" title="${escapeAttr(item.errorMessage)}">${escapeHtml(status)}</span>${item.errorMessage ? `<small class="uh-status-error">${escapeHtml(item.errorMessage)}</small>` : ""}</td>
  </tr>`;
}

function renderHistoryTable(state) {
  let body = "";
  if (state.status === "loading" && !state.items.length) {
    body = `<div class="uh-state-card loading settings-empty-state" role="status" aria-live="polite">${escapeHtml(t("usageHistory.history.loading"))}</div>`;
  } else if (state.status === "error" && !state.items.length) {
    body = `<div class="uh-state-card error settings-alert" role="alert">${escapeHtml(t("usageHistory.history.error", { message: state.error }))}</div>`;
  } else if (!state.items.length) {
    body = `<div class="uh-state-card settings-empty-state" role="status">${escapeHtml(t("usageHistory.history.empty"))}</div>`;
  } else {
    body = `<div class="uh-table-scroll settings-h-scroll" tabindex="0" role="region" aria-label="${escapeAttr(t("usageHistory.history.title"))}"><table class="uh-history-table settings-data-list" aria-label="${escapeAttr(t("usageHistory.history.title"))}"><thead><tr>${["time", "agent", "kind", "provider", "model", "tokens", "ttft", "duration", "cost", "status"].map((key) => `<th scope="col">${escapeHtml(t(`usageHistory.history.${key}`))}</th>`).join("")}</tr></thead><tbody>${state.items.map(renderHistoryRow).join("")}</tbody></table></div>`;
  }
  const loadMore = state.nextCursor ? `<div class="uh-load-more settings-inline-actions"><button id="usageHistoryLoadMore" class="uh-button" type="button"${state.status === "loadingMore" ? " disabled aria-busy=\"true\"" : ""}>${escapeHtml(t(state.status === "loadingMore" ? "usageHistory.history.loadingMore" : "usageHistory.history.loadMore"))}</button></div>` : "";
  const exportDisabled = !state.items.length ? " disabled" : "";
  return `<section class="uh-panel uh-history-panel settings-card" aria-labelledby="usageHistoryHistoryTitle"><div class="uh-section-head settings-card-header"><div><h3 id="usageHistoryHistoryTitle" class="settings-card-title">${escapeHtml(t("usageHistory.history.title"))}</h3><p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("usageHistory.history.description"))}</p></div><button id="usageHistoryExport" class="uh-button" type="button"${exportDisabled}>${escapeHtml(t("usageHistory.history.exportCsv"))}</button></div>${body}${loadMore}</section>`;
}

function renderUsageChartLegend(state) {
  if (!usageHistoryUsesStackedChart(state.metric) || !state.stacked.length) return "";
  const series = stackedSeriesOrder(state.stacked, state.distribution);
  if (!series.length) return "";
  return `<ul class="uh-chart-legend">${series.map((provider, index) => `<li><span class="uh-swatch" style="background:${escapeAttr(stackedColor(provider, index))}"></span><span>${escapeHtml(providerLabel(provider))}</span></li>`).join("")}</ul>`;
}

function renderUsageDistribution(state) {
  const rows = Array.isArray(state.distribution) ? state.distribution : [];
  if (!rows.length) return "";
  const metric = usageHistoryUsesStackedChart(state.metric) ? state.metric : "totalTokens";
  const total = rows.reduce((sum, row) => sum + metricValue(row, metric), 0) || 1;
  return `<aside class="uh-distribution" aria-labelledby="usageHistoryDistributionTitle"><h4 id="usageHistoryDistributionTitle">${escapeHtml(t("usageHistory.trend.distributionTitle"))}</h4>${rows.map((row, index) => {
    const value = metricValue(row, metric);
    const percent = Math.max(0, Math.min(100, (value / total) * 100));
    return `<div class="uh-dist-row"><span class="uh-swatch" style="background:${escapeAttr(stackedColor(row.provider, index))}"></span><span class="uh-dist-name" title="${escapeAttr(providerLabel(row.provider))}">${escapeHtml(providerLabel(row.provider))}</span><span class="uh-dist-value">${escapeHtml(formatAxisMetric(metric, value))}</span><div class="uh-dist-bar" aria-hidden="true"><span style="width:${svgNumber(percent)}%"></span></div></div>`;
  }).join("")}</aside>`;
}

function renderChartPanel(state) {
  const hidden = state.view === "records" ? " hidden" : "";
  return `<div id="usageHistoryChartPanel" class="uh-view-chart" role="tabpanel" aria-labelledby="usageHistoryView-chart"${hidden}>
    ${renderSummary(state.summary)}
    <section class="uh-panel uh-trend-panel settings-card" aria-labelledby="usageHistoryTrendTitle"><div class="uh-section-head settings-card-header"><div><h3 id="usageHistoryTrendTitle" class="settings-card-title">${escapeHtml(t("usageHistory.trend.title"))}</h3><p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("usageHistory.trend.stackedHint"))}</p></div><div class="uh-trend-controls settings-toolbar">${renderBucketControls(state.bucket)}${renderMetricSelect(state.metric)}</div></div>${state.trendTruncated ? `<div class="uh-truncated settings-badge">${escapeHtml(t("usageHistory.trend.truncated"))}</div>` : ""}<div class="uh-chart-layout"><div class="uh-chart-main"><div id="usageHistoryTrendChart" class="uh-chart-host settings-h-scroll" tabindex="0" role="region" aria-label="${escapeAttr(t("usageHistory.trend.title"))}">${renderUsageHistoryChart(state)}</div>${renderUsageChartLegend(state)}<div id="usageHistoryTrendReadout" class="uh-trend-readout">${renderTrendReadout(state.trend, state.metric, state.selectedTrendIndex, state.stacked)}</div></div>${renderUsageDistribution(state)}</div></section>
  </div>`;
}

function renderRecordsPanel(state) {
  const hidden = state.view === "chart" ? " hidden" : "";
  return `<div id="usageHistoryRecordsPanel" class="uh-view-records" role="tabpanel" aria-labelledby="usageHistoryView-records"${hidden}>${renderHistoryTable(state)}</div>`;
}

export function renderUsageHistory(value = {}) {
  const state = createUsageHistoryState(value);
  const generatedAt = state.generatedAt ? t("usageHistory.generatedAt", { timestamp: formatTimestamp(state.generatedAt) }) : t("usageHistory.notGenerated");
  const pageError = state.status === "error" && state.error
    ? `<div class="uh-inline-error settings-alert" role="alert">${escapeHtml(t("usageHistory.history.error", { message: state.error }))}</div>`
    : (state.error && state.items.length ? `<div class="uh-inline-error settings-alert" role="alert">${escapeHtml(t("usageHistory.history.error", { message: state.error }))}</div>` : "");
  return `<main class="usage-history-page settings-page settings-page-usage" aria-labelledby="usageHistoryTitle">
    <header class="uh-hero settings-card"><div><div class="uh-kicker">${escapeHtml(t("usageHistory.kicker"))}</div><h2 id="usageHistoryTitle">${escapeHtml(t("usageHistory.title"))}</h2><p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("usageHistory.description"))}</p><small aria-live="polite">${escapeHtml(generatedAt)}</small></div><button id="usageHistoryRefresh" class="uh-button primary" type="button"${state.status === "loading" ? " disabled aria-busy=\"true\"" : ""}>${escapeHtml(t(state.status === "loading" ? "usageHistory.refreshing" : "usageHistory.refresh"))}</button></header>
    ${pageError}
    ${renderFilters(state)}
    ${renderViewTabs(state.view)}
    ${renderChartPanel(state)}
    ${renderRecordsPanel(state)}
  </main>`;
}

function downloadUsageHistoryCSV(csv) {
  const doc = globalThis.document;
  if (!doc?.createElement || typeof globalThis.URL?.createObjectURL !== "function") return;
  const blob = new Blob([`\uFEFF${csv}`], { type: "text/csv;charset=utf-8" });
  const url = globalThis.URL.createObjectURL(blob);
  const link = doc.createElement("a");
  link.href = url;
  link.download = `autoto-usage-${formatUsageHistoryLocalDate(new Date())}.csv`;
  link.rel = "noopener";
  doc.body?.appendChild(link);
  link.click();
  link.remove();
  globalThis.URL.revokeObjectURL(url);
}

export function createUsageHistoryController({ state = {}, request, onChange, onError } = {}) {
  if (typeof request !== "function") throw new TypeError("usage history request must be a function");
  state.usageHistory = createUsageHistoryState(state.usageHistory);
  const current = () => state.usageHistory;
  const changed = () => { if (typeof onChange === "function") onChange(current()); };

  async function load({ append = false, cursor = "" } = {}) {
    const usage = current();
    const pageCursor = append ? normalizeUsageHistoryText(cursor || usage.nextCursor, 2000) : "";
    if (append && !pageCursor) return false;
    const seq = ++usage.seq;
    usage.status = append ? "loadingMore" : "loading";
    usage.error = "";
    changed();
    try {
      const payload = await request(buildUsageHistoryURL({ filters: usage.filters, bucket: usage.bucket, limit: 50, cursor: pageCursor }));
      if (seq !== usage.seq) return false;
      const normalized = normalizeUsageHistoryResponse(payload);
      usage.generatedAt = normalized.generatedAt;
      usage.summary = normalized.summary;
      usage.trend = normalized.trend;
      usage.trendTruncated = normalized.trendTruncated;
      usage.stacked = normalized.stacked;
      usage.distribution = normalized.distribution;
      usage.selectedTrendIndex = normalizeTrendPointIndex(usage.selectedTrendIndex, normalized.trend.length);
      usage.options = normalized.options;
      usage.items = append ? appendUsageHistoryItems(usage.items, normalized.items) : normalized.items;
      usage.nextCursor = normalized.nextCursor;
      usage.status = "ready";
      usage.error = "";
      changed();
      return true;
    } catch (error) {
      if (seq !== usage.seq) return false;
      usage.status = "error";
      usage.error = normalizeUsageHistoryText(error?.message || error, 2000);
      changed();
      if (typeof onError === "function") onError(error);
      return false;
    }
  }

  function redrawTrend() {
    const usage = current();
    usage.selectedTrendIndex = normalizeTrendPointIndex(usage.selectedTrendIndex, (usage.trend || []).length);
    const chart = globalThis.document?.getElementById?.("usageHistoryTrendChart");
    if (chart) chart.innerHTML = renderUsageHistoryChart(usage);
    const readout = globalThis.document?.getElementById?.("usageHistoryTrendReadout");
    if (readout) readout.innerHTML = renderTrendReadout(usage.trend, usage.metric, usage.selectedTrendIndex, usage.stacked);
    bindTrendPointSelection();
  }

  function selectTrendPoint(index) {
    const usage = current();
    const next = normalizeTrendPointIndex(index, (usage.trend || []).length);
    usage.selectedTrendIndex = next === usage.selectedTrendIndex ? -1 : next;
    redrawTrend();
    return usage.selectedTrendIndex;
  }

  function bindTrendPointSelection() {
    const chart = globalThis.document?.getElementById?.("usageHistoryTrendChart");
    chart?.querySelectorAll?.("[data-usage-trend-point]").forEach((node) => {
      node.addEventListener("click", () => { selectTrendPoint(node.dataset?.usageTrendPoint); });
      node.addEventListener("keydown", (event) => {
        if (event?.key !== "Enter" && event?.key !== " " && event?.key !== "Spacebar") return;
        event.preventDefault?.();
        selectTrendPoint(node.dataset?.usageTrendPoint);
      });
    });
    const readout = globalThis.document?.getElementById?.("usageHistoryTrendReadout");
    readout?.querySelector?.("[data-usage-trend-clear]")?.addEventListener("click", () => {
      current().selectedTrendIndex = -1;
      redrawTrend();
    });
  }

  function setMetric(metric) {
    current().metric = normalizeChoice(metric, usageHistoryMetrics, "totalTokens");
    redrawTrend();
    return current().metric;
  }

  function setBucket(bucket) {
    const next = normalizeChoice(bucket, usageHistoryBuckets, "day");
    if (next === current().bucket && current().status !== "idle") return Promise.resolve(false);
    current().bucket = next;
    current().nextCursor = "";
    return load();
  }

  function setView(view) {
    current().view = normalizeChoice(view, usageHistoryViews, "chart");
    changed();
    return current().view;
  }

  function applyFilters(filters) {
    current().filters = normalizeUsageHistoryFilters(filters);
    current().nextCursor = "";
    return load();
  }

  function applyDatePreset(preset, now = new Date()) {
    const range = usageHistoryDateRangeForPreset(preset, now);
    return applyFilters({ ...current().filters, ...range });
  }

  function resetFilters() {
    return applyFilters({});
  }

  function refresh() {
    current().nextCursor = "";
    return load();
  }

  function loadMore() {
    return load({ append: true, cursor: current().nextCursor });
  }

  function exportCSV() {
    const csv = buildUsageHistoryCSV(current().items);
    if (current().items.length) downloadUsageHistoryCSV(csv);
    return csv;
  }

  function bind() {
    $("usageHistoryRefresh")?.addEventListener("click", () => { void refresh(); });
    globalThis.document?.querySelectorAll?.("[data-usage-bucket]").forEach((node) => {
      node.addEventListener("click", () => { void setBucket(node.dataset.usageBucket); });
    });
    globalThis.document?.querySelectorAll?.("[data-usage-view]").forEach((node) => {
      node.addEventListener("click", () => { setView(node.dataset.usageView); });
    });
    globalThis.document?.querySelectorAll?.("[data-usage-preset]").forEach((node) => {
      node.addEventListener("click", () => { void applyDatePreset(node.dataset.usagePreset); });
    });
    $("usageHistoryMetric")?.addEventListener("change", (event) => setMetric(event.target.value));
    bindTrendPointSelection();
    $("usageHistoryFilters")?.addEventListener("submit", (event) => {
      event.preventDefault();
      void applyFilters({
        provider: $("usageHistoryProvider")?.value,
        model: $("usageHistoryModel")?.value,
        kind: $("usageHistoryKind")?.value,
        from: $("usageHistoryFrom")?.value,
        to: $("usageHistoryTo")?.value,
      });
    });
    $("usageHistoryReset")?.addEventListener("click", () => { void resetFilters(); });
    $("usageHistoryLoadMore")?.addEventListener("click", () => { void loadMore(); });
    $("usageHistoryExport")?.addEventListener("click", () => { exportCSV(); });
    if (current().status === "idle") void load();
  }

  return {
    applyDatePreset,
    applyFilters,
    bind,
    exportCSV,
    getState: current,
    load,
    loadMore,
    refresh,
    render: () => renderUsageHistory(current()),
    resetFilters,
    selectTrendPoint,
    setBucket,
    setMetric,
    setView,
  };
}
