import test from "node:test";
import assert from "node:assert/strict";

import {
  createSubscriptionAccountsController,
  isSubscriptionAccountProvider,
  normalizeSubscriptionAccountList,
  normalizeSubscriptionLoginStatus,
  normalizeSubscriptionProvider,
  renderSubscriptionAccountManagementTable,
  subscriptionAccountActionRequest,
  subscriptionAccountOverview,
  subscriptionAccountQuotaBudgets,
  subscriptionAccountsListRequest,
  subscriptionAccountStatus,
  subscriptionOAuthLoginRequest,
  subscriptionProviderKind,
  subscriptionProviderKinds,
  trustedSubscriptionAuthURL,
} from "./model-provider-settings.mjs";
import { createModelProviderSettingsController } from "./provider-console.mjs";
import { providerCategory, providerDisplayName, renderProviderModelEditor } from "./model-provider-components.mjs";

function flush() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

function createController(overrides = {}) {
  const state = { settings: { providers: [] }, modelCatalog: { providers: [] }, providerConsole: {} };
  const requests = [];
  const controller = createModelProviderSettingsController({
    state,
    loadModelCatalog: async () => {},
    refreshActiveSettingsPanel() {},
    requestAPI: async (path, options = {}) => {
      requests.push({ path, method: options.method || "GET", body: options.body });
      if (overrides.respond) return overrides.respond(path, options);
      return {};
    },
    copyText() {},
    ...overrides.controller,
  });
  return { state, controller, requests };
}

test("subscription provider predicate keys on type, not on the configured name", () => {
  assert.deepEqual(subscriptionProviderKinds, ["gemini", "grok", "kimi"]);
  for (const type of ["gemini", "grok", "kimi"]) {
    assert.equal(subscriptionProviderKind({ type }), type);
    assert.equal(isSubscriptionAccountProvider({ type }), true);
  }
  // A native provider saved under a different name is still that provider: its
  // Base URL is pinned server-side and its credentials are stored per type, so
  // it must keep the OAuth login and account pages.
  assert.equal(subscriptionProviderKind({ name: "gemini-oauth", type: "gemini", origin: "custom" }), "gemini");
  assert.equal(isSubscriptionAccountProvider({ name: "gemini-oauth", type: "gemini", origin: "custom" }), true);
  assert.equal(providerCategory({ name: "gemini-oauth", type: "gemini", origin: "custom" }), "official");
  // The API-key Gemini Interactions provider is not a subscription provider.
  assert.equal(subscriptionProviderKind({ name: "gemini", type: "gemini-interactions" }), "");
  assert.equal(isSubscriptionAccountProvider({ name: "gemini", type: "gemini-interactions" }), false);
  assert.equal(providerCategory({ name: "gemini", type: "gemini-interactions", origin: "builtin" }), "official");
  assert.equal(subscriptionProviderKind({ type: "openai" }), "");
  assert.equal(providerCategory({ name: "my-proxy", type: "openai-compatible", origin: "custom" }), "custom");
});

test("provider display names distinguish native Antigravity from Gemini Interactions", () => {
  // The native gemini provider talks to Google's Antigravity surface, so it is
  // labelled by platform; the API-key gemini-interactions provider is not.
  assert.equal(providerDisplayName({ name: "gemini-oauth", type: "gemini" }), "Antigravity");
  assert.equal(providerDisplayName({ name: "gemini", type: "gemini-interactions" }), "Gemini Interactions");
});

test("subscription request builders encode provider and id and reject invalid providers", () => {
  assert.equal(subscriptionAccountsListRequest("gemini").path, "/api/providers/auth/gemini/accounts");
  assert.deepEqual(subscriptionOAuthLoginRequest("start", "grok"), { path: "/api/providers/oauth/grok/login/start", options: { method: "POST" } });
  // The server renders the OAuth callback page, so it needs the UI locale.
  assert.deepEqual(
    subscriptionOAuthLoginRequest("start", "gemini", "", "zh-TW"),
    { path: "/api/providers/oauth/gemini/login/start?locale=zh-TW", options: { method: "POST" } },
  );
  assert.equal(subscriptionOAuthLoginRequest("start", "gemini", "", "  ").path, "/api/providers/oauth/gemini/login/start");
  assert.equal(subscriptionOAuthLoginRequest("status", "kimi", "kimi login/1").path, "/api/providers/oauth/kimi/login/kimi%20login%2F1");
  assert.equal(subscriptionOAuthLoginRequest("cancel", "kimi", "abc").options.method, "DELETE");

  const save = subscriptionAccountActionRequest("save", "gemini", "acct/1", { alias: "Work", priority: 3 });
  assert.equal(save.path, "/api/providers/auth/gemini/accounts/acct%2F1");
  assert.deepEqual(JSON.parse(save.options.body), { alias: "Work", priority: 3 });
  assert.equal(subscriptionAccountActionRequest("toggle", "grok", "id", { disabled: true }).options.body, JSON.stringify({ disabled: false }));
  assert.equal(subscriptionAccountActionRequest("sync", "grok", "id").path, "/api/providers/auth/grok/accounts/id/sync");
  assert.equal(subscriptionAccountActionRequest("delete", "kimi", "id").options.method, "DELETE");

  assert.throws(() => subscriptionAccountsListRequest("gemini-interactions"));
  assert.throws(() => subscriptionOAuthLoginRequest("start", "openai"));
  assert.throws(() => subscriptionAccountActionRequest("save", "codex", "id", {}));
  assert.equal(normalizeSubscriptionProvider("Gemini"), "gemini");
  assert.equal(normalizeSubscriptionProvider("gemini-interactions"), "");
});

test("subscription account list normalization accepts wrapper and array shapes", () => {
  assert.deepEqual(normalizeSubscriptionAccountList({ accounts: [{ id: "a" }], count: 1 }), [{ id: "a" }]);
  assert.deepEqual(normalizeSubscriptionAccountList([{ id: "b" }]), [{ id: "b" }]);
  assert.deepEqual(normalizeSubscriptionAccountList(null), []);
});

test("subscription auth URL allowlist is provider specific and HTTPS-only", () => {
  assert.equal(trustedSubscriptionAuthURL("gemini", "https://accounts.google.com/o/oauth2/v2/auth?x=1"), true);
  assert.equal(trustedSubscriptionAuthURL("gemini", "https://accounts.google.com.evil.test/"), false);
  assert.equal(trustedSubscriptionAuthURL("grok", "https://auth.x.ai/activate"), true);
  assert.equal(trustedSubscriptionAuthURL("grok", "https://x.ai/"), true);
  assert.equal(trustedSubscriptionAuthURL("grok", "https://accounts.google.com/"), false);
  assert.equal(trustedSubscriptionAuthURL("kimi", "https://auth.kimi.com/device"), true);
  assert.equal(trustedSubscriptionAuthURL("kimi", "http://auth.kimi.com/device"), false);
  assert.equal(trustedSubscriptionAuthURL("kimi", "https://kimi.com.attacker.test/"), false);
  assert.equal(trustedSubscriptionAuthURL("gemini", "https://user:pass@accounts.google.com/"), false);
});

test("subscription account status and overview classify disabled and expired accounts", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");
  assert.equal(subscriptionAccountStatus({ disabled: true }, { now }).key, "statusDisabled");
  assert.equal(subscriptionAccountStatus({ expires_at: "2025-01-01T00:00:00Z" }, { now }).key, "expired");
  assert.equal(subscriptionAccountStatus({ expires_at: "2025-01-01T00:00:00Z", refresh_token: "rt" }, { now }).key, "available");
  const overview = subscriptionAccountOverview([{ disabled: true }, { expires_at: "2025-01-01T00:00:00Z" }, {}], { now });
  assert.deepEqual(overview, { total: 3, available: 1, rateLimited: 0, disabled: 1, expired: 1 });
});

test("subscription account table carries provider on every action and escapes fields", () => {
  const html = renderSubscriptionAccountManagementTable("grok", [{ id: "acct-1", alias: "<b>x</b>", priority: 5, email: "u@example.test" }]);
  assert.match(html, /data-subscription-provider="grok"/);
  assert.match(html, /data-subscription-sync="acct-1"/);
  assert.match(html, /data-subscription-delete="acct-1"/);
  assert.match(html, /&lt;b&gt;x&lt;\/b&gt;/);
  assert.doesNotMatch(html, /<b>x<\/b>/);
  const empty = renderSubscriptionAccountManagementTable("gemini", [], { emptyText: "no accounts" });
  assert.match(empty, /no accounts/);
});

test("account quota renders upstream rate-limit budgets and stays pending without a snapshot", () => {
  // Shape mirrors what the server echoes from the Grok proxy's response headers.
  const withQuota = renderSubscriptionAccountManagementTable("grok", [{
    id: "acct-1",
    quota: { requests: { limit: "21", remaining: "20", reset: "" }, tokens: { limit: "1000000", remaining: "999984", reset: "" } },
  }], { translate: (key, params) => `${key}:${JSON.stringify(params || {})}` });
  assert.match(withQuota, /quotaRequests/);
  assert.match(withQuota, /quotaTokens/);
  assert.match(withQuota, /&quot;remaining&quot;:&quot;20&quot;/);
  assert.match(withQuota, /&quot;limit&quot;:&quot;1,000,000&quot;/);

  // No snapshot at all must read as pending, never as zero remaining.
  const pending = renderSubscriptionAccountManagementTable("grok", [{ id: "acct-2" }], {
    translate: (key) => key,
  });
  assert.match(pending, /quotaPending/);
  assert.doesNotMatch(pending, /quotaRequests/);
});

test("account quota ignores buckets whose counts are absent or non-numeric", () => {
  assert.deepEqual(subscriptionAccountQuotaBudgets({}), []);
  assert.deepEqual(subscriptionAccountQuotaBudgets({ quota: { requests: { limit: "", remaining: "" } } }), []);
  assert.deepEqual(subscriptionAccountQuotaBudgets({ quota: { requests: { limit: "unlimited", remaining: "" } } }), []);
  assert.deepEqual(
    subscriptionAccountQuotaBudgets({ quota: { tokens: { limit: "10", remaining: "" } } }),
    [{ labelKey: "quotaTokens", remaining: null, limit: 10, reset: "" }],
  );
});

test("subscription console renders three independent pages without a switcher or stacked lists", () => {
  const { state, controller } = createController();
  state.subscriptionAccounts = {
    gemini: [{ id: "gemini-acct", alias: "Gemini One" }],
    grok: [{ id: "grok-acct", alias: "Grok One" }],
    kimi: [{ id: "kimi-acct", alias: "Kimi One" }],
  };

  state.providerConsole.view = "gemini";
  const geminiHTML = controller.renderProviderSettingsContent();
  assert.match(geminiHTML, /data-subscription-view="gemini"/);
  assert.match(geminiHTML, /Gemini One/);
  assert.doesNotMatch(geminiHTML, /Grok One/);
  assert.doesNotMatch(geminiHTML, /Kimi One/);

  state.providerConsole.view = "grok";
  const grokHTML = controller.renderProviderSettingsContent();
  assert.match(grokHTML, /data-subscription-view="grok"/);
  assert.match(grokHTML, /Grok One/);
  assert.doesNotMatch(grokHTML, /Gemini One/);

  state.providerConsole.view = "kimi";
  const kimiHTML = controller.renderProviderSettingsContent();
  assert.match(kimiHTML, /data-subscription-view="kimi"/);
  assert.match(kimiHTML, /Kimi One/);

  // No provider switcher / tabs / select across the three pages.
  for (const html of [geminiHTML, grokHTML, kimiHTML]) {
    assert.doesNotMatch(html, /data-subscription-switch|subscription-provider-tabs|<select/);
  }
});

test("subscription page exposes an available-models panel wired for re-fetching", () => {
  const { state, controller } = createController();
  // The provider is saved under a non-default name, as a migrated install has it.
  state.settings.providers = [{
    name: "gemini-oauth",
    type: "gemini",
    origin: "custom",
    enabled: true,
    configured: true,
    baseUrl: "https://cloudcode-pa.googleapis.com",
    model: "gemini-3-flash",
    models: [{ name: "gemini-3-flash" }, { name: "claude-sonnet-4-6" }],
  }];
  state.subscriptionAccounts = { gemini: [{ id: "gemini-acct" }], grok: [], kimi: [] };
  state.providerConsole.view = "gemini";

  const html = controller.renderProviderSettingsContent();
  assert.match(html, /subscription-model-panel/);
  // The generic console handler keys off this form wrapper to run discovery.
  assert.match(html, /data-mp-provider-form/);
  assert.match(html, /data-mp-fetch-models/);
  // Discovery must target the provider's real configured name, not the kind.
  assert.match(html, /name="name" value="gemini-oauth"/);
  assert.match(html, /name="type" value="gemini"/);
  // The built-in catalog is listed so the user can see what is usable.
  assert.match(html, /gemini-3-flash/);
});

test("subscription models panel is omitted when the provider is not configured at all", () => {
  const { state, controller } = createController();
  state.settings.providers = [];
  state.subscriptionAccounts = { gemini: [], grok: [], kimi: [] };
  state.providerConsole.view = "gemini";
  const html = controller.renderProviderSettingsContent();
  assert.doesNotMatch(html, /subscription-model-panel/);
});

test("three official subscription cards each open their own dedicated page", () => {
  const { state, controller } = createController();
  state.settings.providers = [
    { name: "gemini", type: "gemini", origin: "builtin", enabled: true, configured: true },
    { name: "grok", type: "grok", origin: "builtin", enabled: true, configured: true },
    { name: "kimi", type: "kimi", origin: "builtin", enabled: true, configured: true },
  ];
  state.providerConsole.view = "providers";
  const listHTML = controller.renderProviderSettingsContent();
  assert.match(listHTML, /data-mp-provider-card="gemini"/);
  assert.match(listHTML, /data-mp-provider-card="grok"/);
  assert.match(listHTML, /data-mp-provider-card="kimi"/);

  controller.openGeminiConsolePage({ name: "gemini", type: "gemini" });
  assert.equal(state.providerConsole.view, "gemini");
  controller.openGrokConsolePage({ name: "grok", type: "grok" });
  assert.equal(state.providerConsole.view, "grok");
  controller.openKimiConsolePage({ name: "kimi", type: "kimi" });
  assert.equal(state.providerConsole.view, "kimi");
});

test("subscription login state stays isolated per provider", () => {
  const { state, controller } = createController();
  controller.renderGeminiConsolePage();
  controller.renderGrokConsolePage();
  const pages = state.providerConsole.subscriptionPages;
  pages.gemini.login.status = "pending";
  pages.gemini.login.userCode = "GEMINI-CODE";
  assert.equal(pages.grok.login.status, "idle");
  assert.equal(pages.kimi.login.status, "idle");
  assert.equal(pages.grok.login.userCode, "");
});

test("subscription login polling stops on terminal status and ignores stale sequences", async () => {
  const statuses = [
    { loginId: "grok-login", provider: "grok", status: "pending", userCode: "CODE", verificationUri: "https://auth.x.ai/activate" },
    { loginId: "grok-login", provider: "grok", status: "completed", account: { id: "grok-acct", alias: "Grok" } },
  ];
  let statusReads = 0;
  let accountLoads = 0;
  const { state, controller } = createController({
    respond(path, options) {
      if (path.endsWith("/login/start")) return statuses[0];
      if (path.includes("/login/")) { const next = statuses[Math.min(statusReads, statuses.length - 1)]; statusReads += 1; return next; }
      if (path.endsWith("/accounts")) { accountLoads += 1; return { accounts: [{ id: "grok-acct", alias: "Grok" }] }; }
      return {};
    },
    controller: { remoteAccess: null },
  });
  controller.renderGrokConsolePage();
  const promise = controller.startSubscriptionLogin("grok");
  // Give the poller time to observe the completed status.
  for (let i = 0; i < 40 && state.providerConsole.subscriptionPages.grok.login.status !== "completed"; i += 1) {
    await new Promise((resolve) => setTimeout(resolve, 30));
  }
  await promise;
  const login = state.providerConsole.subscriptionPages.grok.login;
  assert.equal(login.status, "completed");
  assert.equal(accountLoads >= 1, true);
  // A later poll tick with the old sequence must not resurrect the session.
  const staleSeq = login.seq;
  login.seq = staleSeq + 5;
  await flush();
  assert.equal(state.providerConsole.subscriptionPages.grok.login.status, "completed");
});

test("normalizeSubscriptionLoginStatus keeps device and browser fields without tokens", () => {
  const status = normalizeSubscriptionLoginStatus({
    loginId: "l1", provider: "kimi", status: "pending", userCode: "K-CODE",
    verificationUri: "https://auth.kimi.com/device", authUrl: "https://auth.kimi.com/device?x=1",
    account: { id: "a", access_token: "should-not-be-read" },
  });
  assert.equal(status.loginId, "l1");
  assert.equal(status.provider, "kimi");
  assert.equal(status.userCode, "K-CODE");
  assert.equal(status.verificationUri, "https://auth.kimi.com/device");
  assert.equal(Object.hasOwn(status, "access_token"), false);
});

test("the models panel eye hides a model from the composer picker, not via a draft", () => {
  // Reproduces the reported symptom: clicking the eye reported "keep at least
  // one visible model" and nothing was hidden, because the handler read an
  // unrelated console draft instead of the visibility preference.
  let prefs = { hiddenModels: {}, showUnconfiguredProviders: false };
  let panelRefreshes = 0;
  const { state, controller } = createController({
    controller: {
      getModelVisibilityPreference: () => prefs,
      setModelVisibilityPreference: (next) => { prefs = next; },
      refreshActiveSettingsPanel: () => { panelRefreshes += 1; },
    },
  });
  state.settings.providers = [{
    name: "grok", type: "grok", origin: "builtin", enabled: true, configured: true,
    baseUrl: "https://cli-chat-proxy.grok.com/v1", model: "grok-4.5",
    models: [{ name: "grok-4.5" }, { name: "grok-3-mini" }],
  }];
  state.subscriptionAccounts = { gemini: [], grok: [{ id: "grok-acct" }], kimi: [] };
  state.providerConsole.view = "grok";

  // The click handler is bound to #settingsContentBody, so capture it there.
  const listeners = {};
  const root = {
    addEventListener: (type, handler) => { listeners[type] = handler; },
    removeEventListener: () => {},
  };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "settingsContentBody" ? root : null) };
  try {
    controller.bindProviderSettingsActions();
    assert.ok(listeners.click, "the console must bind a click handler");

    // The handler resolves the button via event.target.closest(...), so the fake
    // element has to answer both that lookup and the subscription-form lookup.
    const form = { elements: { name: { value: "grok" } } };
    const target = {
      dataset: { mpModelVisibility: "grok-4.5", hidden: "false" },
      closest: (selector) => {
        if (selector === "[data-subscription-provider-config]") return form;
        if (selector.includes("button")) return target;
        return null;
      },
    };
    const click = () => listeners.click({ target, preventDefault() {}, stopPropagation() {} });
    click();

    assert.equal(prefs.hiddenModels["grok:grok-4.5"], true, "the model should be hidden from the picker");
    assert.ok(panelRefreshes > 0, "the settings panel must re-render so the row greys out");

    // Hiding the last visible model of an official provider is allowed.
    target.dataset.mpModelVisibility = "grok-3-mini";
    click();
    assert.equal(prefs.hiddenModels["grok:grok-3-mini"], true);

    // Clicking again unhides it.
    target.dataset.hidden = "true";
    click();
    assert.equal(prefs.hiddenModels["grok:grok-3-mini"], undefined);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("only allowEmpty pages let the last visible model's eye be clicked", () => {
  const draft = { name: "grok", type: "grok", modelConfigs: [
    { name: "grok-4.5", hidden: false },
    { name: "grok-3-mini", hidden: true },
  ] };
  // Default: the final eye is disabled so a provider is never left with nothing.
  const guarded = renderProviderModelEditor(draft, false, true);
  assert.match(guarded, /data-mp-model-visibility="grok-4\.5"[^>]*disabled/);
  // The subscription pages opt out: visibility there is only a display filter,
  // so hiding every model is allowed and the button must stay clickable.
  const open = renderProviderModelEditor(draft, false, true, { allowEmpty: true });
  assert.doesNotMatch(open, /data-mp-model-visibility="grok-4\.5"[^>]*disabled/);
  // An already-hidden model is always clickable so it can be shown again.
  assert.doesNotMatch(guarded, /data-mp-model-visibility="grok-3-mini"[^>]*disabled/);
});
