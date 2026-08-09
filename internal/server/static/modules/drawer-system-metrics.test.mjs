import assert from "node:assert/strict";
import test from "node:test";

import { renderCompactSystemMetrics } from "./system-metrics-panel.mjs";

// The home dashboard that carries CPU / memory / network is display:none below
// 768px, so on a phone the drawer cell is the only way to see these numbers.
// These tests cover the compact renderer that fills that cell.
function payload(overrides = {}) {
  return {
    cpu: { available: true, percent: 12 },
    memory: { available: true, percent: 48, usedBytes: 8 * 1024 ** 3, totalBytes: 16 * 1024 ** 3 },
    network: { available: true, rxBytesPerSec: 1024, txBytesPerSec: 512 },
    ...overrides,
  };
}

test("all four metrics reach the drawer", () => {
  const html = renderCompactSystemMetrics(payload());
  for (const metric of ["cpu", "memory", "networkDown", "networkUp"]) {
    assert.match(html, new RegExp(`data-metric="${metric}"`), `${metric} is one of the four the user asked for`);
  }
});

test("tone is shared with the dashboard rather than recomputed", () => {
  // 95% CPU is past the dashboard's danger threshold. If this renderer ever grew
  // its own thresholds the two views could disagree about the same reading.
  const html = renderCompactSystemMetrics(payload({ cpu: { available: true, percent: 95 } }));
  assert.match(html, /data-metric="cpu" data-tone="danger"/);
  const calm = renderCompactSystemMetrics(payload());
  assert.match(calm, /data-metric="cpu" data-tone="ok"/);
});

test("nothing measurable renders nothing at all", () => {
  // An empty string lets the cell collapse instead of showing a box of zeroes.
  assert.equal(renderCompactSystemMetrics({}), "");
  assert.equal(renderCompactSystemMetrics(null), "");
});

test("a partial reading renders only what is available", () => {
  const html = renderCompactSystemMetrics({
    cpu: { available: false },
    memory: { available: true, percent: 30, usedBytes: 1024, totalBytes: 4096 },
    network: { available: false },
  });
  assert.match(html, /data-metric="memory"/);
  assert.doesNotMatch(html, /data-metric="cpu"/);
  assert.doesNotMatch(html, /data-metric="networkDown"/);
});

test("labels come from the caller's catalog and are escaped", () => {
  const html = renderCompactSystemMetrics(payload(), () => '"><img src=x onerror="boom">');
  assert.doesNotMatch(html, /<img src=x/);
  assert.match(html, /&lt;img src=x/);
});
