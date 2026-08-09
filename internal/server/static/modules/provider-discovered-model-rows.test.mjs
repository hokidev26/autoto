import test from "node:test";
import assert from "node:assert/strict";

const { normalizeConsoleProvider, normalizeProviderModelConfigs } = await import("./model-provider-components.mjs");

const normalizeProviderForDraft = normalizeConsoleProvider;

// A saved provider only persists the model rows the user configured, so a relay
// whose limits were never set stores just its default model. The card preview
// falls back to the discovered list and shows all of them, but the editable rows
// came from modelConfigs alone -- seeded with the default model only. The result:
// nine models on the card, one row that can carry a context limit, and no way to
// set a limit on the model actually being run. The value went nowhere and the
// runtime kept falling back to the provider's hardcoded protocol default.

const savedTtapy = {
  name: "ttapy",
  type: "anthropic",
  baseUrl: "https://relay.example.test",
  model: "claude-haiku-4-5",
  // What discovery reports, which is what the card preview already displays.
  models: [
    "claude-haiku-4-5",
    "claude-opus-4-5",
    "claude-opus-4-6",
    "claude-opus-5",
    "claude-sonnet-4-6",
  ],
  // What the config file holds: the default model alone, limit unset.
  modelConfigs: [],
};

test("a provider with no saved rows still gets an editable row per discovered model", () => {
  const draft = normalizeProviderForDraft(savedTtapy);
  const names = draft.modelConfigs.map((item) => item.name);
  assert.deepEqual(names, savedTtapy.models, "every discovered model needs a row that can hold a limit");
});

test("the model being run is reachable, not just the provider default", () => {
  const draft = normalizeProviderForDraft(savedTtapy);
  const row = draft.modelConfigs.find((item) => item.name === "claude-opus-5");
  assert.ok(row, "claude-opus-5 is offered by this provider, so its limit must be settable");
  assert.equal(row.contextTokenLimit, 0, "unset, but present");
});

test("saved rows still win: a configured limit is never replaced by discovery", () => {
  const draft = normalizeProviderForDraft({
    ...savedTtapy,
    modelConfigs: [{ name: "claude-opus-5", contextTokenLimit: 1000000 }],
  });
  const row = draft.modelConfigs.find((item) => item.name === "claude-opus-5");
  assert.equal(row.contextTokenLimit, 1000000);
});

test("a provider with neither saved rows nor discovery keeps the default-model row", () => {
  const draft = normalizeProviderForDraft({
    name: "wechat",
    type: "anthropic",
    model: "claude-haiku-4-5",
    models: [],
    modelConfigs: [],
  });
  assert.deepEqual(draft.modelConfigs.map((item) => item.name), ["claude-haiku-4-5"]);
});

test("a discovered row carries no limit of its own, so normalization leaves it unset", () => {
  const rows = normalizeProviderModelConfigs({ models: savedTtapy.models });
  assert.equal(rows.length, savedTtapy.models.length);
  for (const row of rows) assert.equal(row.contextTokenLimit, 0);
});
