import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { createSystemSettingsController } from "./system-settings.mjs";
import systemSettingsMessages from "./messages-system-settings.mjs";

const continuation = {
  mode: "off",
  segmentTurns: 40,
  maxContinuations: 8,
  maxTotalTurns: 200,
  maxRunDurationMs: 3600000,
  maxRunTokens: 2000000,
};

function runtimeSummary(backgroundTasks) {
  return {
    memory: { allocBytes: 1024, sysBytes: 2048, gcCycles: 3 },
    go: { goroutines: 7 },
    agent: { maxTurns: 200, firstTokenTimeoutMs: 60000, maxTransientRetries: 10, continuation },
    backgroundTasks,
    generatedAt: "2026-07-29T10:00:00Z",
  };
}

function fakeInput(value, { checked = false, disabled = false } = {}) {
  return {
    value: String(value),
    checked,
    disabled,
    listeners: new Map(),
    addEventListener(name, handler) { this.listeners.set(name, handler); },
    dispatch(name) { return this.listeners.get(name)?.({ currentTarget: this }); },
  };
}

function fakeButton(textContent = "Save") {
  return {
    dataset: {},
    textContent,
    disabled: false,
    attributes: new Map(),
    listeners: new Map(),
    addEventListener(name, handler) { this.listeners.set(name, handler); },
    setAttribute(name, value) { this.attributes.set(name, String(value)); },
    removeAttribute(name) { this.attributes.delete(name); },
  };
}

async function withElements(elements, run) {
  const previous = globalThis.document;
  globalThis.document = { getElementById: (id) => elements[id] ?? null };
  try {
    return await run();
  } finally {
    globalThis.document = previous;
  }
}

test("background task card groups concurrency apart from the nesting switch and hides depth while off", () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary(undefined) } });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /class="settings-info-card settings-card settings-card-content background-task-settings-card"/);
  assert.match(markup, /class="background-task-concurrency-grid settings-form-grid"/);
  assert.match(markup, /id="runtimeBackgroundWorkerCount"[^>]*min="1"[^>]*max="16"[^>]*value="8"/);
  assert.match(markup, /id="runtimeBackgroundPerAgentLimit"[^>]*min="1"[^>]*max="8"[^>]*value="4"/);
  assert.match(markup, /id="runtimeAllowNestedSubagents" type="checkbox"/);
  assert.doesNotMatch(markup, /id="runtimeAllowNestedSubagents"[^>]*checked/);

  // Nesting off: the group is not lit and the depth/warning block is hidden
  // rather than rendered as a disabled control.
  assert.match(markup, /class="background-task-nested-group" data-background-task-nested-group/);
  assert.match(markup, /data-background-task-nested-detail hidden/);
  assert.match(markup, /id="runtimeMaxSubagentDepth"[^>]*min="2"[^>]*max="4"[^>]*value="2"/);
  assert.doesNotMatch(markup, /id="runtimeMaxSubagentDepth"[^>]*disabled/);

  // The cost warning belongs to the nesting group, so it carries the warning
  // treatment instead of reading as ordinary card copy.
  assert.match(markup, /class="skill-security-note"[^>]*>开启嵌套子代理会增加请求、Token 消耗和编辑冲突，但不会扩大权限。/);
});

test("background task card reads persisted values and reveals depth when nesting is on", () => {
  const controller = createSystemSettingsController({
    state: {
      runtimeSummary: runtimeSummary({
        workerCount: 12,
        perAgentLimit: 7,
        allowNestedSubagents: true,
        maxSubagentDepth: 4,
      }),
    },
  });
  const markup = controller.renderRuntimeSettingsContent();

  assert.match(markup, /id="runtimeBackgroundWorkerCount"[^>]*value="12"/);
  assert.match(markup, /id="runtimeBackgroundPerAgentLimit"[^>]*value="7"/);
  assert.match(markup, /id="runtimeAllowNestedSubagents"[^>]*checked/);
  assert.match(markup, /id="runtimeMaxSubagentDepth"[^>]*value="4"/);
  assert.match(markup, /class="background-task-nested-group is-on"/);
  assert.doesNotMatch(markup, /data-background-task-nested-detail hidden/);
});

test("nested sub-agent switch reveals the depth block in place without a panel re-render", async () => {
  const controller = createSystemSettingsController({ state: { runtimeSummary: runtimeSummary({}) } });
  controller.renderRuntimeSettingsContent();

  const detail = { hidden: true };
  const group = {
    classes: new Set(),
    classList: {
      toggle(name, on) { if (on) group.classes.add(name); else group.classes.delete(name); },
    },
    querySelector: (selector) => (selector === "[data-background-task-nested-detail]" ? detail : null),
  };
  const nestedToggle = fakeInput("", { checked: false });
  nestedToggle.closest = (selector) => (selector === "[data-background-task-nested-group]" ? group : null);

  await withElements({ runtimeAllowNestedSubagents: nestedToggle }, async () => {
    controller.bindRuntimeSettingsActions();

    nestedToggle.checked = true;
    nestedToggle.dispatch("change");
    assert.equal(detail.hidden, false);
    assert.equal(group.classes.has("is-on"), true);

    nestedToggle.checked = false;
    nestedToggle.dispatch("change");
    assert.equal(detail.hidden, true);
    assert.equal(group.classes.has("is-on"), false);
  });
});

test("saving background task settings sends all fields, shows busy state, then refreshes", async () => {
  const requests = [];
  const events = [];
  let refreshes = 0;
  let resolveFetch;
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary({ workerCount: 8, perAgentLimit: 4, allowNestedSubagents: false, maxSubagentDepth: 2 }) },
    loadRuntimeSummary: async () => {
      refreshes += 1;
      events.push("refresh");
    },
    showToast: () => events.push("toast"),
  });
  controller.renderRuntimeSettingsContent();

  const saveButton = fakeButton("Save settings");
  const elements = {
    runtimeBackgroundWorkerCount: fakeInput(12),
    runtimeBackgroundPerAgentLimit: fakeInput(6),
    runtimeAllowNestedSubagents: fakeInput("", { checked: true }),
    runtimeMaxSubagentDepth: fakeInput(4),
    saveBackgroundTaskSettingsBtn: saveButton,
  };
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    requests.push({ url: String(url), method: options.method, body: JSON.parse(options.body) });
    return new Promise((resolve) => {
      resolveFetch = () => resolve({ ok: true, status: 200, text: async () => "{}" });
    });
  };

  try {
    await withElements(elements, async () => {
      controller.bindRuntimeSettingsActions();
      const savePromise = saveButton.listeners.get("click")({ currentTarget: saveButton });
      await Promise.resolve();
      assert.equal(saveButton.disabled, true);
      assert.equal(saveButton.attributes.get("aria-busy"), "true");
      resolveFetch();
      await savePromise;
    });
  } finally {
    globalThis.fetch = previousFetch;
  }

  assert.deepEqual(requests, [{
    url: "/api/runtime/background-task-settings",
    method: "PATCH",
    body: {
      workerCount: 12,
      perAgentLimit: 6,
      allowNestedSubagents: true,
      maxSubagentDepth: 4,
    },
  }]);
  assert.deepEqual(events, ["toast", "refresh"]);
  assert.equal(refreshes, 1);
  assert.equal(saveButton.disabled, false);
  assert.equal(saveButton.attributes.has("aria-busy"), false);
  assert.equal(saveButton.textContent, "Save settings");
});

test("failed background task save reports through showError and restores the button", async () => {
  const errors = [];
  const controller = createSystemSettingsController({
    state: { runtimeSummary: runtimeSummary({}) },
    loadRuntimeSummary: async () => {},
    showError: (error) => errors.push(error),
  });
  controller.renderRuntimeSettingsContent();

  const saveButton = fakeButton("Save settings");
  const elements = {
    runtimeBackgroundWorkerCount: fakeInput(8),
    runtimeBackgroundPerAgentLimit: fakeInput(4),
    runtimeAllowNestedSubagents: fakeInput("", { checked: false }),
    runtimeMaxSubagentDepth: fakeInput(2, { disabled: true }),
    saveBackgroundTaskSettingsBtn: saveButton,
  };
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 500,
    statusText: "Internal Server Error",
    text: async () => JSON.stringify({ error: "settings rejected" }),
  });

  try {
    await withElements(elements, async () => {
      controller.bindRuntimeSettingsActions();
      await saveButton.listeners.get("click")({ currentTarget: saveButton });
    });
  } finally {
    globalThis.fetch = previousFetch;
  }

  assert.equal(errors.length, 1);
  assert.equal(errors[0].message, "settings rejected");
  assert.equal(saveButton.disabled, false);
  assert.equal(saveButton.attributes.has("aria-busy"), false);
  assert.equal(saveButton.textContent, "Save settings");
});

test("background task settings include the required keys in all supported locales", () => {
  const requiredKeys = [
    "title",
    "description",
    "workerCount",
    "perAgentLimit",
    "allowNestedSubagents",
    "allowNestedSubagentsHint",
    "maxSubagentDepth",
    "range",
    "nestedWarning",
    "save",
    "saving",
    "saved",
  ];
  for (const locale of ["zh-CN", "zh-TW", "en"]) {
    const messages = systemSettingsMessages[locale].systemSettings.runtimeResources.backgroundTaskSettings;
    for (const key of requiredKeys) {
      assert.equal(typeof messages[key], "string", `${locale}.${key} should be localized`);
    }
    if (locale !== "zh-CN") {
      assert.notEqual(messages.nestedWarning, systemSettingsMessages["zh-CN"].systemSettings.runtimeResources.backgroundTaskSettings.nestedWarning, `${locale} should not fall back to zh-CN`);
    }
  }

  assert.match(systemSettingsMessages["zh-CN"].systemSettings.runtimeResources.backgroundTaskSettings.nestedWarning, /请求.*Token.*编辑冲突.*权限/);
  assert.match(systemSettingsMessages["zh-TW"].systemSettings.runtimeResources.backgroundTaskSettings.nestedWarning, /請求.*Token.*編輯衝突.*權限/);
  assert.match(systemSettingsMessages.en.systemSettings.runtimeResources.backgroundTaskSettings.nestedWarning, /requests.*token.*edit conflicts.*permissions/i);
});

