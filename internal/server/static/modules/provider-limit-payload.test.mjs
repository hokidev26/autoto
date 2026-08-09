import test from "node:test";
import assert from "node:assert/strict";

const { providerConfigPayload, normalizeProviderModelConfigs } = await import("./model-provider-components.mjs");

// providerConfigPayload builds the models array from draft.modelConfigs alone. A
// draft that carries its model list under `models` -- which is what a saved
// provider's config gives, and what discovery writes -- therefore sends an empty
// array. The server reads an empty array as "keep what is saved", so a limit typed
// into the drawer is dropped on the way out and the old value stays on disk. The
// save reports success, which is why this looks like the setting silently
// refusing to stick.

test("a draft holding its models under `models` still sends them", () => {
  const payload = providerConfigPayload({
    name: "ttapy",
    type: "anthropic",
    baseUrl: "https://relay.example.test",
    model: "claude-haiku-4-5",
    models: ["claude-haiku-4-5", "claude-opus-4-5", "claude-opus-5"],
    modelConfigs: [],
  });
  assert.deepEqual(
    payload.models.map((item) => item.name),
    ["claude-haiku-4-5", "claude-opus-4-5", "claude-opus-5"],
    "an empty models array would be read by the server as 'keep what is saved'",
  );
});

test("a limit set on a model known only through `models` reaches the server", () => {
  const payload = providerConfigPayload({
    name: "ttapy",
    type: "anthropic",
    model: "claude-haiku-4-5",
    models: ["claude-haiku-4-5", "claude-opus-5"],
    modelConfigs: [{ name: "claude-opus-5", contextTokenLimit: 1000000 }],
  });
  const row = payload.models.find((item) => item.name === "claude-opus-5");
  assert.ok(row, "the edited model must be present");
  assert.equal(row.contextTokenLimit, 1000000);
  // The other offered model must not be dropped just because it has no limit,
  // or saving a limit would silently shrink the provider's model list.
  assert.ok(payload.models.some((item) => item.name === "claude-haiku-4-5"));
});

test("the default model is never lost, even when it appears nowhere else", () => {
  const payload = providerConfigPayload({
    name: "ttapy",
    type: "anthropic",
    model: "claude-haiku-4-5",
    models: [],
    modelConfigs: [],
  });
  assert.deepEqual(payload.models.map((item) => item.name), ["claude-haiku-4-5"]);
});

test("a genuinely empty provider still sends an empty list", () => {
  const payload = providerConfigPayload({ name: "blank", type: "anthropic", model: "", models: [], modelConfigs: [] });
  assert.deepEqual(payload.models, []);
});

test("hidden rows are still persisted, since hiding is a view preference", () => {
  const payload = providerConfigPayload({
    name: "ttapy",
    type: "anthropic",
    model: "claude-haiku-4-5",
    models: [],
    modelConfigs: [
      { name: "claude-haiku-4-5" },
      { name: "claude-opus-5", contextTokenLimit: 1000000, hidden: true },
    ],
  });
  const row = payload.models.find((item) => item.name === "claude-opus-5");
  assert.ok(row, "a hidden model keeps its saved limit; hiding only affects the picker");
  assert.equal(row.contextTokenLimit, 1000000);
});

test("normalization itself already reads both keys, which is why the payload should too", () => {
  const rows = normalizeProviderModelConfigs({
    models: ["a", "b"],
    modelConfigs: [{ name: "b", contextTokenLimit: 500 }],
  });
  assert.deepEqual(rows.map((item) => item.name).sort(), ["a", "b"]);
  assert.equal(rows.find((item) => item.name === "b").contextTokenLimit, 500);
});
