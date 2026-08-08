import test from "node:test";
import assert from "node:assert/strict";

import { createSystemSettingsController } from "./system-settings.mjs";

function runtimeSummary(nonRetryableErrorPatterns) {
  return {
    memory: { allocBytes: 1024, sysBytes: 2048, gcCycles: 3 },
    go: { goroutines: 7 },
    agent: {
      maxTurns: 200,
      firstTokenTimeoutMs: 60000,
      maxTransientRetries: 10,
      continuation: { mode: "safe", segmentTurns: 40, maxContinuations: 8, maxTotalTurns: 200, maxRunDurationMs: 3600000, maxRunTokens: 2000000 },
      nonRetryableErrorPatterns,
    },
    generatedAt: "2026-07-29T10:00:00Z",
  };
}

test("stored permanent-error patterns each render a row with a remove control", () => {
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary(["insufficient balance", "model not found"]) },
  });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /class="settings-info-card settings-card settings-card-content retry-policy-card"/);
  assert.match(markup, /data-retry-policy-list/);
  assert.match(markup, /insufficient balance/);
  assert.match(markup, /model not found/);
  assert.equal((markup.match(/data-retry-policy-remove="/g) || []).length, 2);
  assert.match(markup, /id="addRetryPolicyPatternBtn"/);
  assert.match(markup, /id="saveRetryPolicyBtn"/);
});

test("an empty list says so rather than rendering an empty box", () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary([]) } });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /retry-policy-empty/);
  assert.doesNotMatch(markup, /data-retry-policy-remove="/);
});

test("patterns are escaped, so a stored pattern cannot inject markup", () => {
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary(["<img src=x onerror=boom>"]) },
  });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /&lt;img src=x onerror=boom&gt;/);
  assert.doesNotMatch(markup, /<img src=x/);
});

test("patterns from the server are normalized before display, so the list shown is the list stored", () => {
  const controller = createSystemSettingsController({
    // Mixed case, padded whitespace, a duplicate, and one below the minimum
    // length. Server-side normalization does the same, so showing the raw values
    // would promise a list the backend would not keep.
    state: { runtimeSummary: runtimeSummary(["  Insufficient   Balance ", "insufficient balance", "ab"]) },
  });
  const markup = controller.renderRuntimeSettingsContent();

  assert.equal((markup.match(/data-retry-policy-remove="/g) || []).length, 1);
  assert.match(markup, /insufficient balance/);
  assert.doesNotMatch(markup, /Insufficient   Balance/);
});
