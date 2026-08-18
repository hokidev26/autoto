import assert from "node:assert/strict";
import test from "node:test";

import { t } from "./i18n.mjs";
import { createModelProviderSettingsController, providerConsoleSuccessBannerMs } from "./provider-console.mjs";

function fakeSelect(value = "") {
  const classes = new Set();
  return {
    innerHTML: "",
    value,
    title: "",
    options: [],
    dataset: {},
    classList: {
      toggle(name, on) { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
    setAttribute() {},
    removeAttribute() {},
    querySelectorAll: () => [],
  };
}

function withDocument(elements, run) {
  const previous = globalThis.document;
  globalThis.document = {
    getElementById: (id) => elements[id] ?? null,
    querySelectorAll: () => [],
    querySelector: () => null,
  };
  try {
    return run();
  } finally {
    globalThis.document = previous;
  }
}

function harness(stateOverrides = {}, options = {}) {
  const select = fakeSelect(options.selectValue || "");
  const state = {
    agent: { id: "agent-1", model: "openai:gpt-4.1-mini" },
    settings: {
      providers: [
        { name: "openai", type: "openai", enabled: true, configured: true, models: ["gpt-4.1-mini", "gpt-4.1"] },
        { name: "codex", type: "codex", enabled: true, configured: true, models: ["gpt-5.5"] },
        { name: "cliproxyapi", type: "openai-compatible", profile: "cliproxyapi", enabled: true, configured: true, models: ["relay-1"] },
        { name: "custom-relay", type: "openai-compatible", profile: "", enabled: true, configured: false, models: ["m"] },
      ],
      agent: { defaultModel: "openai:gpt-4.1-mini" },
    },
    modelCatalog: {
      providers: [
        { name: "openai", type: "openai", configured: true, enabled: true, models: ["gpt-4.1-mini", "gpt-4.1"] },
        { name: "codex", type: "codex", configured: true, enabled: true, models: ["gpt-5.5"] },
        { name: "cliproxyapi", type: "openai-compatible", profile: "cliproxyapi", configured: true, enabled: true, models: ["relay-1"] },
        { name: "custom-relay", type: "openai-compatible", profile: "", configured: false, enabled: true, models: ["m"] },
      ],
    },
    providerConsole: {},
    ...stateOverrides,
  };
  const controller = createModelProviderSettingsController({
    state,
    getPreferredModelPreference: () => options.preferred || "",
    setPreferredModelPreference: () => {},
    getModelVisibilityPreference: () => ({ hiddenModels: {}, showUnconfiguredProviders: false }),
    setModelVisibilityPreference: () => {},
    refreshActiveSettingsPanel: () => {},
    requestAPI: async () => ({}),
    updateWorkspaceMetaPills: () => {},
    ...options.controller,
  });
  return { state, select, controller };
}

test("provider labels distinguish official, relay, and CLIProxyAPI entries", () => {
  // The console used to print the raw config name for every card, so Codex OAuth
  // and a blank-profile OpenAI-compatible relay were indistinguishable.
  const { controller } = harness();
  assert.equal(controller.providerLabel({ type: "codex", name: "codex" }), "Codex OAuth");
  assert.equal(controller.providerLabel({ profile: "cliproxyapi", name: "cliproxyapi" }), "CLIProxyAPI");
  assert.equal(controller.providerLabel({ type: "openai-compatible", profile: "", name: "acme" }), t("modelProvider.relay"));
  assert.equal(controller.providerLabel({ type: "openai", name: "openai" }), "openai");
});

test("provider status text reports error ahead of configured, then unconfigured", () => {
  // An error that still claimed "ready" hid the reason the catalog would not load.
  const { controller } = harness();
  assert.equal(controller.providerStatusText({ error: "401", configured: true }), t("modelProvider.needsConfiguration"));
  assert.equal(controller.providerStatusText({ configured: true }), t("modelProvider.ready"));
  assert.equal(controller.providerStatusText({ configured: false }), t("modelProvider.unconfigured"));
});

test("relay protocol lookup falls back to completions for an unknown key", () => {
  // A stale stored protocol used to produce an empty spec and a blank create form.
  const { controller } = harness();
  assert.equal(controller.relayProtocolSpec("anthropic").key, "anthropic");
  assert.equal(controller.relayProtocolSpec("codex").providerName, "cliproxyapi");
  assert.equal(controller.relayProtocolSpec("gemini-interactions").providerType, "gemini-interactions");
  assert.equal(controller.relayProtocolSpec("not-a-protocol").key, "completions");
  assert.equal(controller.relayProtocolSpec("").key, "completions");
});

test("model setup copy names Codex specially and surfaces a provider error", () => {
  // Generic "set OPENAI_API_KEY" advice on a Codex model sent people to the wrong
  // credential, and a provider.error was dropped so the notice looked empty.
  const { controller } = harness();
  assert.match(controller.modelSetupMessage("codex:gpt-5.5"), /codex:gpt-5.5/);
  const errored = harness({
    settings: {
      providers: [{ name: "openai", type: "openai", configured: false, error: "quota exceeded", models: ["gpt-4.1"] }],
    },
    modelCatalog: {
      providers: [{ name: "openai", type: "openai", configured: false, error: "quota exceeded", models: ["gpt-4.1"] }],
    },
  });
  assert.match(errored.controller.modelSetupMessage("openai:gpt-4.1"), /quota exceeded/);
});

test("selected model ignores values that are not in the live selectable catalog", () => {
  // The picker used to keep a stale select.value after that model was hidden or
  // the provider went away, so a later save wrote a model the server rejected.
  const { controller, select } = harness({}, { selectValue: "gone:model", preferred: "also-gone" });
  withDocument({ modelSelect: select }, () => {
    assert.equal(controller.selectedModelValue(), "openai:gpt-4.1-mini");
    assert.equal(controller.currentModelValue(), "openai:gpt-4.1-mini");
  });
});

test("a runtime-unavailable provider is not treated as a configured current model", () => {
  // configured=true with runtimeAvailable=false is a catalog row the process
  // cannot actually call. Treating it as ready left the composer looking armed.
  const { controller } = harness({
    settings: {
      providers: [{
        name: "openai",
        type: "openai",
        enabled: true,
        configured: true,
        runtimeAvailable: false,
        registered: true,
        models: ["gpt-4.1-mini"],
      }],
    },
    modelCatalog: {
      providers: [{
        name: "openai",
        type: "openai",
        enabled: true,
        configured: true,
        runtimeAvailable: false,
        registered: true,
        models: ["gpt-4.1-mini"],
      }],
    },
    agent: { id: "agent-1", model: "openai:gpt-4.1-mini" },
  });
  assert.equal(controller.isCurrentModelConfigured("openai:gpt-4.1-mini"), false);
});

test("send is not blocked while the model catalog is still hydrating", () => {
  const { controller } = harness({
    settings: null,
    modelCatalog: null,
    agent: { id: "agent-1", model: "codex:gpt-5.6-luna" },
  });
  assert.equal(controller.isCurrentModelConfigured("codex:gpt-5.6-luna"), true);
});

test("opening Settings on an account page snaps the console back to the provider list", () => {
  // Resuming the last Gemini/Codex drill-down made the Settings button land on
  // an account screen instead of the list the sidebar says is selected.
  const { controller, state } = harness({
    providerConsole: { view: "gemini", mode: "gemini", type: "gemini", drawer: "provider", modal: "types" },
  });
  controller.resetProviderConsoleToProviderList();
  assert.equal(state.providerConsole.view, "providers");
  assert.equal(state.providerConsole.drawer, "");
  assert.equal(state.providerConsole.modal, "");
  assert.equal(state.providerConsole.mode, "");
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /class="mp-provider-page/);
  assert.doesNotMatch(html, /gemini-account-console/);
});

async function waitUntil(predicate, tries = 40) {
  for (let i = 0; i < tries; i++) {
    if (predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("timed out");
}

function clickable(dataset) {
  return {
    dataset,
    textContent: "",
    disabled: false,
    closest(selector) {
      if (selector === "button, [data-mp-provider-card], [data-mp-backdrop]") return this;
      return null;
    },
    setAttribute() {},
    removeAttribute() {},
  };
}

test("refreshing models, Codex quota, and account enablement does not insert a success banner", async () => {
  const listeners = {};
  const root = {
    addEventListener: (type, handler) => { listeners[type] = handler; },
    removeEventListener: () => {},
  };
  let refreshes = 0;
  const { state, controller } = harness({
    providerAuthFiles: [{ id: "account-1" }],
    providerAuthLoaded: true,
    providerConsole: { view: "providers", result: { message: "stale", tone: "success" }, busy: {} },
  }, {
    controller: {
      loadModelCatalog: async () => {},
      requestAPI: async () => [{ id: "account-1" }],
      refreshActiveSettingsPanel: () => { refreshes += 1; },
    },
  });
  const previous = globalThis.document;
  globalThis.document = {
    getElementById: (id) => (id === "settingsContentBody" ? root : null),
    querySelectorAll: () => [],
    querySelector: () => null,
  };
  try {
    controller.bindProviderSettingsActions();
    listeners.click({
      target: clickable({ mpRefreshModels: "" }),
      preventDefault() {},
      stopPropagation() {},
    });
    await waitUntil(() => refreshes >= 2 && !state.providerConsole.busy?.refresh);
    assert.equal(state.providerConsole.result, null);
    assert.doesNotMatch(controller.renderProviderSettingsContent(), /mp-provider-result|codex-console-result/);

    state.providerConsole.view = "codex";
    state.providerConsole.result = { message: "stale", tone: "success" };
    refreshes = 0;
    listeners.click({
      target: clickable({ codexSync: "account-1" }),
      preventDefault() {},
      stopPropagation() {},
    });
    await waitUntil(() => refreshes >= 2 && !state.codexAccountBusy?.["account-1"]);
    assert.equal(state.providerConsole.result, null);
    assert.doesNotMatch(controller.renderProviderSettingsContent(), /mp-provider-result|codex-console-result/);

    state.providerConsole.result = { message: "stale", tone: "success" };
    refreshes = 0;
    listeners.click({
      target: clickable({ codexToggle: "account-1", disabled: "false" }),
      preventDefault() {},
      stopPropagation() {},
    });
    await waitUntil(() => refreshes >= 2 && !state.codexAccountBusy?.["account-1"]);
    assert.equal(state.providerConsole.result, null);
    assert.doesNotMatch(controller.renderProviderSettingsContent(), /mp-provider-result|codex-console-result/);
  } finally {
    globalThis.document = previous;
  }
});

test("creating an OpenAI-compatible provider opens the create drawer with an empty model list", () => {
  // Seeding the template's example model made "Save and enable" look ready before
  // the user had fetched or typed a real model, and the server then stored junk.
  const { controller, state } = harness();
  controller.openProviderConsoleType("openai-compatible");
  assert.equal(state.providerConsole.drawer, "provider");
  assert.equal(state.providerConsole.mode, "create");
  assert.equal(state.providerConsole.draft.model, "");
  assert.deepEqual(state.providerConsole.draft.modelConfigs, []);
  assert.equal(state.providerConsole.draft.modelsReady, false);
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /data-mp-provider-form/);
  assert.match(html, /mp-provider-create-page/);
});

test("a success provider banner clears itself after a short delay", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { state, controller } = harness();
  controller.setProviderConsoleResult("已儲存並啟用 jyqf。", "success");
  assert.equal(state.providerConsole.result?.tone, "success");
  assert.match(controller.renderProviderSettingsContent(), /mp-provider-result/);
  t.mock.timers.tick(providerConsoleSuccessBannerMs);
  assert.equal(state.providerConsole.result, null);
  assert.doesNotMatch(controller.renderProviderSettingsContent(), /mp-provider-result/);
});
