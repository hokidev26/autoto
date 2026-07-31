import test from "node:test";
import assert from "node:assert/strict";

import { createSystemSettingsController } from "./system-settings.mjs";

function runtimeSummary(continuation) {
  return {
    memory: { allocBytes: 1024, sysBytes: 2048, gcCycles: 3 },
    go: { goroutines: 7 },
    agent: { maxTurns: 200, firstTokenTimeoutMs: 60000, maxTransientRetries: 10, continuation },
    generatedAt: "2026-07-29T10:00:00Z",
  };
}

// Minimal stand-in for the controls the budget card binds. Only the surface the
// controller actually touches is modelled, so a real DOM is not required.
function fakeInput(value, { checked = false, disabled = false, row = null } = {}) {
  return {
    value: String(value),
    checked,
    disabled,
    listeners: new Map(),
    addEventListener(name, handler) { this.listeners.set(name, handler); },
    dispatch(name) { this.listeners.get(name)?.(); },
    closest: () => row,
    focus() { this.focused = true; },
  };
}

function fakeRow() {
  const classes = new Set(["execution-budget-row"]);
  return {
    classes,
    classList: {
      toggle(name, on) { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
  };
}

function withElements(elements, run) {
  const previous = globalThis.document;
  globalThis.document = { getElementById: (id) => elements[id] ?? null };
  try {
    return run();
  } finally {
    globalThis.document = previous;
  }
}

const limitedContinuation = {
  mode: "safe",
  segmentTurns: 40,
  maxContinuations: 8,
  maxTotalTurns: 200,
  maxRunDurationMs: 3600000,
  maxRunTokens: 2000000,
};

test("execution budget card renders one row per budget with the unlimited switch beside its label", () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary(limitedContinuation) } });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /class="settings-info-card settings-card settings-card-content execution-budget-card"/);
  // The mode select owns its own full-width row instead of sharing the budget grid.
  assert.match(markup, /class="execution-budget-mode"[\s\S]*?id="runtimeBudgetMode"/);
  for (const key of ["maxTotalTurns", "maxRunTokens", "maxRunDurationMs", "maxContinuations"]) {
    assert.match(markup, new RegExp(`data-budget-row="${key}"`));
  }
  // Label and switch share one head row, which is what the old inline-styled
  // markup could not do because .settings-form-field is a grid.
  assert.match(markup, /execution-budget-row-head[\s\S]*?execution-budget-label[\s\S]*?execution-budget-toggle/);
  assert.match(markup, /id="runtimeBudgetDurationMinutes"[\s\S]*?execution-budget-unit">分钟</);
  assert.doesNotMatch(markup, /style="margin-top:12px;gap:8px"/);
  assert.doesNotMatch(markup, /flex-direction:row/);
});

test("unlimited budgets render checked, disabled, and dimmed; limited budgets render their stored value", () => {
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary({ ...limitedContinuation, maxRunTokens: -1 }) },
  });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /class="execution-budget-row is-unlimited" data-budget-row="maxRunTokens"/);
  // A budget that still has a ceiling must not be dimmed.
  assert.match(markup, /class="execution-budget-row" data-budget-row="maxTotalTurns"/);
  assert.match(markup, /id="runtimeBudgetTokensUnlimited" type="checkbox" data-budget-field="maxRunTokens" checked/);
  assert.match(markup, /id="runtimeBudgetTokens"[\s\S]*?disabled/);
  // A limited budget keeps its real number and stays editable.
  assert.match(markup, /id="runtimeBudgetTotalTurns"[^>]*value="200"/);
  assert.doesNotMatch(markup, /id="runtimeBudgetTotalTurns"[^>]*disabled/);
});

test("total-turn minimum follows segmentTurns so the save call cannot be rejected by the server", () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary({ ...limitedContinuation, segmentTurns: 120 }) } });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /id="runtimeBudgetTotalTurns"[\s\S]*?min="120"/);
  // Budgets without that coupling keep their own floor.
  assert.match(markup, /id="runtimeBudgetTokens"[\s\S]*?min="1000"/);
});

test("clearing unlimited seeds a value at or above the effective minimum and un-dims the row", () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary({ ...limitedContinuation, segmentTurns: 300, maxTotalTurns: -1 }) } });
  controller.renderRuntimeSettingsContent();

  const row = fakeRow();
  row.classList.toggle("is-unlimited", true);
  const input = fakeInput("", { disabled: true });
  const toggle = fakeInput("", { checked: true, row });
  const elements = { runtimeBudgetTotalTurns: input, runtimeBudgetTotalTurnsUnlimited: toggle };

  withElements(elements, () => {
    controller.bindRuntimeSettingsActions();
    toggle.checked = false;
    toggle.dispatch("change");
  });

  assert.equal(input.disabled, false);
  assert.equal(Number(input.value) >= 300, true, `seeded value ${input.value} must respect the segmentTurns floor`);
  assert.equal(row.classList.contains("is-unlimited"), false);
});

test("saving sends -1 for unlimited budgets, scaled minutes, and carries segmentTurns forward", async () => {
  const requests = [];
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary({ ...limitedContinuation, segmentTurns: 40 }) },
    loadRuntimeSummary: async () => {},
    showToast: () => {},
  });
  controller.renderRuntimeSettingsContent();

  const saveButton = { dataset: {}, textContent: "save", listeners: new Map(), addEventListener(name, handler) { this.listeners.set(name, handler); }, setAttribute() {}, removeAttribute() {} };
  const elements = {
    runtimeBudgetMode: { value: "safe" },
    runtimeBudgetTotalTurns: fakeInput("500"),
    runtimeBudgetTotalTurnsUnlimited: fakeInput("", { checked: false, row: fakeRow() }),
    runtimeBudgetTokens: fakeInput("2000000", { disabled: true }),
    runtimeBudgetTokensUnlimited: fakeInput("", { checked: true, row: fakeRow() }),
    runtimeBudgetDurationMinutes: fakeInput("90"),
    runtimeBudgetDurationMinutesUnlimited: fakeInput("", { checked: false, row: fakeRow() }),
    runtimeBudgetContinuations: fakeInput("12"),
    runtimeBudgetContinuationsUnlimited: fakeInput("", { checked: false, row: fakeRow() }),
    saveExecutionBudgetBtn: saveButton,
  };

  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    requests.push({ url: String(url), body: JSON.parse(options.body) });
    return { ok: true, status: 200, headers: { get: () => "application/json" }, json: async () => ({ persisted: true }), text: async () => "{}" };
  };
  try {
    await withElements(elements, async () => {
      controller.bindRuntimeSettingsActions();
      await saveButton.listeners.get("click")({ currentTarget: saveButton });
    });
  } finally {
    globalThis.fetch = previousFetch;
  }

  assert.equal(requests.length, 1);
  assert.match(requests[0].url, /\/api\/runtime\/continuation-settings$/);
  assert.deepEqual(requests[0].body, {
    mode: "safe",
    segmentTurns: 40,
    maxTotalTurns: 500,
    maxRunTokens: -1,
    maxRunDurationMs: 5400000,
    maxContinuations: 12,
  });
});
