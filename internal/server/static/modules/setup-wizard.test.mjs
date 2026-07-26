import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import setupWizardMessages from "./messages-setup-wizard.mjs";
import {
  discoverSetupModels,
  normalizeSetupStatus,
  setupCanFinish,
  setupEnvironmentReady,
  setupWizardStartupDecision,
  setupWizardVersion,
} from "./setup-wizard.mjs";

test("setup wizard discovers arbitrary providers with usable models", () => {
  const models = discoverSetupModels({
    providers: [
      { name: "custom-gateway", type: "gemini-interactions", configured: true, models: ["gemini-test"], discovered: true },
      { name: "broken", type: "openai-compatible", configured: true, models: ["fallback"], error: "offline", modelsSource: "fallback" },
    ],
  });
  assert.deepEqual(models.map((item) => item.value), ["custom-gateway:gemini-test"]);
});

test("setup wizard remains compatible with the existing catalog shape", () => {
  const models = discoverSetupModels({ providers: [{ name: "relay", type: "openai-compatible", configured: true, models: ["model-a"] }] });
  assert.equal(models[0].value, "relay:model-a");
});

test("setup wizard keeps configured runtime models when remote discovery is temporarily unavailable", () => {
  const models = discoverSetupModels({
    providers: [{
      name: "relay",
      type: "openai-compatible",
      enabled: true,
      configured: true,
      registered: true,
      runtimeAvailable: true,
      available: false,
      discovered: false,
      modelsSource: "fallback",
      models: ["configured-model"],
      error: "model listing timed out",
    }],
  });
  assert.deepEqual(models.map((item) => item.value), ["relay:configured-model"]);
});

test("setup status requires database and built-in runtime capabilities", () => {
  const ready = normalizeSetupStatus({
    loaded: true,
    database: { available: true },
    tools: [
      { id: "shell", available: true, required: true, builtIn: true },
      { id: "search", available: true, required: true, builtIn: true },
      { id: "git", available: false, recommended: true },
      { id: "go", available: false },
      { id: "node", available: false },
    ],
  });
  assert.equal(setupEnvironmentReady(ready), true);

  const databaseUnavailable = normalizeSetupStatus({ ...ready, loaded: true, database: { available: false } });
  assert.equal(setupEnvironmentReady(databaseUnavailable), false);

  const shellUnavailable = normalizeSetupStatus({
    ...ready,
    loaded: true,
    database: { available: true },
    tools: ready.tools.map((tool) => tool.id === "shell" ? { ...tool, available: false } : tool),
  });
  assert.equal(setupEnvironmentReady(shellUnavailable), false);
});

test("setup wizard startup decision stays quiet only for a completed usable model", () => {
  const catalog = { providers: [{ name: "relay", configured: true, models: ["model-a"] }] };
  assert.deepEqual(
    setupWizardStartupDecision({ setupVersion: 0, preferredModel: "", catalog }),
    {
      open: true,
      step: "welcome",
      reason: "first-run",
      models: discoverSetupModels(catalog),
      preferredReady: false,
    },
  );
  assert.equal(setupWizardStartupDecision({
    setupVersion: setupWizardVersion,
    preferredModel: "relay:model-a",
    catalog,
  }).open, false);
  assert.deepEqual(
    setupWizardStartupDecision({ setupVersion: setupWizardVersion, preferredModel: "relay:removed", catalog }),
    {
      open: true,
      step: "model",
      reason: "model-unavailable",
      models: discoverSetupModels(catalog),
      preferredReady: false,
    },
  );
});

test("setup completion requires a ready environment and selected catalog model", () => {
  const catalog = { providers: [{ name: "relay", configured: true, models: ["model-a"] }] };
  const status = normalizeSetupStatus({
    loaded: true,
    databaseReady: true,
    tools: [
      { id: "shell", available: true, required: true, builtIn: true },
      { id: "search", available: true, required: true, builtIn: true },
      { id: "git", available: false, recommended: true },
    ],
  });
  assert.equal(setupCanFinish({ status, catalog, selectedModel: "relay:model-a" }), true);
  assert.equal(setupCanFinish({ status, catalog, selectedModel: "relay:missing" }), false);
});

test("setup wizard copy covers simplified Chinese, traditional Chinese, and English", () => {
  for (const locale of ["zh-CN", "zh-TW", "en"]) {
    const messages = setupWizardMessages[locale]?.setupWizard;
    assert.ok(messages?.title);
    assert.ok(messages?.welcome?.title);
    assert.ok(messages?.environment?.title);
    assert.ok(messages?.model?.required);
    assert.ok(messages?.complete?.title);
    assert.ok(messages?.tools?.database?.title);
  }
});

test("static shell mounts the first-run flow and keeps a manual settings entry", async () => {
  const [html, appMain, styles, settingsStyles] = await Promise.all([
    readFile(new URL("../index.html", import.meta.url), "utf8"),
    readFile(new URL("./app-main.mjs", import.meta.url), "utf8"),
    readFile(new URL("../styles/settings-legacy.css", import.meta.url), "utf8"),
    readFile(new URL("../styles/settings.css", import.meta.url), "utf8"),
  ]);
  const settingsEntry = html.match(/<button id="settingsWizardBtn"[^>]*>/)?.[0] || "";
  assert.ok(settingsEntry);
  assert.doesNotMatch(settingsEntry, /\bhidden\b|tabindex="-1"/);
  for (const id of ["setupWizardProgress", "setupWizardBody", "setupWizardBackBtn", "setupWizardRefreshBtn", "setupWizardNextBtn"]) {
    assert.match(html, new RegExp(`id="${id}"`));
  }
  assert.match(appMain, /loadSetupStatus:\s*\(\{ force = false \} = \{\}\) => api\(force \? "\/api\/setup\/status\?refresh=1" : "\/api\/setup\/status"\)/);
  assert.match(appMain, /const setupStartup = maybeOpenSetupWizard\(\)/);
  assert.match(styles, /\.setup-wizard-tool-list/);
  assert.match(styles, /\.setup-wizard-model\.selected/);
  // The manual entry stays in the markup and on desktop, but narrow screens
  // collapse the sidebar into a sticky header where it would sit above every
  // settings page; the wizard still auto-opens on first run and when the
  // preferred model is unavailable, so nothing becomes unreachable.
  assert.match(settingsStyles, /#settingsModal \.settings-wizard-btn\s*\{[^}]*display:\s*none/);
});
