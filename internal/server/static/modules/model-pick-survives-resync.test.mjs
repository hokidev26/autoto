import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { createModelProviderSettingsController } from "./provider-console.mjs";

// Changing the model starts a PATCH and returns immediately, so for as long as
// that save is in flight state.agent.model is still the previous model. Any
// repaint in that window wrote the old value straight back into the picker, which
// read as the composer switching the model back on its own. An interrupt forces
// exactly such a repaint, because it resyncs the live snapshot.
function fakeSelect() {
  const classes = new Set();
  return {
    innerHTML: "",
    value: "",
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

function harness(stateOverrides = {}) {
  const select = fakeSelect();
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById: (id) => (id === "modelSelect" ? select : null),
    querySelectorAll: () => [],
  };
  const state = {
    agent: { id: "agent-1", model: "ttapy:claude-opus-5" },
    settings: {
      providers: [
        { name: "ttapy", type: "openai", enabled: true, configured: true, models: ["claude-opus-5"] },
        { name: "other", type: "openai", enabled: true, configured: true, models: ["fast-1"] },
      ],
    },
    modelCatalog: {
      providers: [
        { name: "ttapy", type: "openai", configured: true, models: ["claude-opus-5"] },
        { name: "other", type: "openai", configured: true, models: ["fast-1"] },
      ],
    },
    ...stateOverrides,
  };
  const controller = createModelProviderSettingsController({
    state,
    getPreferredModelPreference: () => "",
    setPreferredModelPreference: () => {},
    getModelVisibilityPreference: () => ({ hiddenModels: {}, showUnconfiguredProviders: false }),
    setModelVisibilityPreference: () => {},
    updateWorkspaceMetaPills: () => {},
  });
  return { state, select, controller, restore: () => { globalThis.document = previousDocument; } };
}

test("重繪不會把使用者剛選、還沒存完的模型改回舊的", () => {
  const { state, select, controller, restore } = harness();
  try {
    // The user picked another provider's model; the PATCH has not answered yet, so
    // state.agent.model is deliberately still the old one.
    state.agentSavePending = true;
    state.agentSaveSnapshot = { agentId: "agent-1", model: "other:fast-1" };

    controller.renderModelOptions();

    assert.equal(select.value, "other:fast-1", "選單要留在使用者剛選的模型上");
  } finally {
    restore();
  }
});

test("存檔完成後，重繪以伺服器確認的模型為準", () => {
  const { state, select, controller, restore } = harness();
  try {
    // Nothing in flight: the persisted model is the authority again, which is what
    // keeps a repaint from pinning a stale pick forever.
    state.agentSavePending = false;
    state.agentSaving = false;
    state.agentSaveSnapshot = { agentId: "agent-1", model: "other:fast-1" };

    controller.renderModelOptions();

    assert.equal(select.value, "ttapy:claude-opus-5");
  } finally {
    restore();
  }
});

test("換對話後，另一個對話的待存選擇不會套到這個對話", () => {
  const { state, select, controller, restore } = harness();
  try {
    // The save belongs to a conversation the user has already left.
    state.agentSaving = true;
    state.agentSaveSnapshot = { agentId: "agent-2", model: "other:fast-1" };

    controller.renderModelOptions();

    assert.equal(select.value, "ttapy:claude-opus-5", "待存選擇只屬於它自己的對話");
  } finally {
    restore();
  }
});

test("即時快照重繪的路徑上，模型選單就是這一個函式", () => {
  // The revert was observed after an interrupt because that resyncs the snapshot,
  // and this is the call in that path which writes the select. Pinned as source so
  // the fix cannot be bypassed by a second repaint route.
  const appMain = readFileSync(new URL("./app-main-stream.mjs", import.meta.url), "utf8");
  const snapshotFn = appMain.slice(appMain.indexOf("async function applyAgentLiveSnapshot"));
  const body = snapshotFn.slice(0, snapshotFn.indexOf("\n}\n"));
  assert.match(body, /renderModelOptions\(\);/, "快照套用時仍然會重繪模型選單");

  const console = readFileSync(new URL("./provider-console.mjs", import.meta.url), "utf8");
  assert.match(
    console,
    /const currentModel = pendingModelSelection\(\) \|\| currentModelValue\(\);/,
    "重繪必須先看待存的選擇",
  );
});

test("較舊的即時快照不能把剛存好的模型蓋回去", () => {
  const appMain = readFileSync(new URL("./app-main-stream.mjs", import.meta.url), "utf8");
  const snapshotFn = appMain.slice(appMain.indexOf("async function applyAgentLiveSnapshot"));
  const body = snapshotFn.slice(0, snapshotFn.indexOf("\n}\n"));
  assert.match(body, /state\.agentSaveSnapshot\?\.model/, "存檔中的選擇要壓過快照裡的舊模型");
  assert.match(body, /nextGeneration < currentGeneration/, "較舊的 entityGeneration 不得覆寫剛 PATCH 的模型");
});

test("送出前會先記住選單上的模型，避免存檔把舊值寫回之後就照舊供應商跑", () => {
  const composer = readFileSync(new URL("./chat-composer.mjs", import.meta.url), "utf8");
  const at = composer.indexOf("async function syncSelectedModelToAgent");
  assert.notEqual(at, -1);
  const body = composer.slice(at, composer.indexOf("\n  function lastTurnStatuses(", at));
  assert.match(body, /const pickerAtStart = String\(\$\("modelSelect"\)\?\.value/);
  assert.match(body, /await awaitAgentSettingsSaved\(agentId\)/);
  assert.ok(
    body.indexOf("const pickerAtStart") < body.indexOf("await awaitAgentSettingsSaved"),
    "必須在等待存檔之前記住選單，否則被寫回舊模型後送出會沿用舊供應商",
  );
});
