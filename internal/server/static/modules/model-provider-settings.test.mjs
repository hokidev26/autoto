import test from "node:test";
import assert from "node:assert/strict";
import { readStylesGroup } from "./styles-source-helper.mjs";

import messagesEN from "./messages-en.mjs";
import messagesZhCN from "./messages-zh-CN.mjs";
import messagesZhTW from "./messages-zh-TW.mjs";
import {
  agentModelSettingsPayload,
  agentModelRoles,
  anthropicAccountActionRequest,
  anthropicAccountCreateRequest,
  anthropicAccountOverview,
  anthropicAccountStatus,
  anthropicAccountsListRequest,
  anthropicProfileLoginCommand,
  automaticProviderNameUpdate,
  codexAccountActionRequest,
  codexAccountBatchRequest,
  codexAccountExportFilename,
  codexAccountOverview,
  codexBrowserLoginRequest,
  codexAccountStatus,
  codexDeleteResultWarning,
  codexImportBatchRequest,
  codexMutationRefreshWarning,
  consumeAnthropicAccountCreateRequest,
  createModelProviderSettingsController,
  isAgentModelReference,
  isProviderConsoleInteractiveTarget,
  modelAggregateActionRequest,
  modelAggregateMembers,
  normalizeAgentModelSettings,
  normalizeAnthropicAccountList,
  normalizeCodexBatchResult,
  normalizeCodexBrowserLoginStatus,
  normalizeCodexImportBatchResult,
  normalizeCodexSelectedIds,
  normalizeModelAggregateList,
  normalizeCodexAccountList,
  markProviderModelsStale,
  providerConnectionFingerprint,
  providerConsoleDraftFromForm,
  providerNameFromBaseURL,
  providerModelDiscovery,
  providerPreflightResult,
  providerSensitiveDraftAccessAllowed,
  restoreProviderConsoleFocus,
  selectProviderConsoleFieldOnFocus,
  shouldOpenProviderCardFromKeyboard,
  syncProviderConsoleDraft,
  trapProviderConsoleFocus,
  trustedCodexBrowserAuthURL,
  validateCodexImportJSON,
  validateProviderNameValue,
  renderAnthropicAccountManagementTable,
  renderCodexAccountManagementTable,
  runtimeReasoningSettingsRequest,
  subscriptionProviderKind,
} from "./model-provider-settings.mjs";
import {
  createProviderDraft,
  filterConsoleProviders,
  isAnthropicAccountProvider,
  isBuiltinProvider,
  isProviderDeletable,
  mergeProviderModelDiscovery,
  modelProvidersForUIUnion,
  normalizeConsoleProvider,
  normalizeProviderModelConfigs,
  providerConfigPayload,
  providerModelDraftUsable,
  providerSupportsImageGeneration,
  providerVisibilityPreferencesForDraft,
  removeProviderVisibilityPreferences,
  setProviderModelHidden,
  setProviderModelHiddenAll,
  providerConsoleRequest,
  providerConsoleStats,
  providerTestPayload,
  providerMessageTestPayload,
  renderProviderConsolePage,
  renderProviderModelEditor,
} from "./model-provider-components.mjs";
import { reasoningEffortValuesForModel } from "./chat-composer.mjs";
import { setUILocale } from "./i18n.mjs";
import { getRegionalPreferences, setRegionalPreferences } from "./locale-registry.mjs";

const labels = {
  noCodexCredentials: "No accounts",
  accountName: "Name",
  accountId: "Account ID",
  priority: "Priority",
  status: "Status",
  successFailure: "Success / failure",
  usage: "Usage",
  lastUsed: "Last used",
  actions: "Actions",
  selectAccount: "Select account",
  selectAccountNamed: "Select {account}",
  selectedAccounts: "{count} selected",
  selectAllAccounts: "Select all accounts",
  clearSelection: "Clear selection",
  batchActions: "Batch actions",
  batchSync: "Sync selected",
  batchEnable: "Enable selected",
  batchDisable: "Disable selected",
  batchPriority: "Set priority",
  batchDelete: "Delete selected",
  apply: "Apply",
  primaryQuota: "Primary",
  secondaryQuota: "Secondary",
  remainingPercent: "{percent}% remaining",
  resetsIn: "Resets in {time}",
  noQuota: "No quota",
  credits: "Credits",
  creditsBalance: "Balance {balance}",
  creditsUnlimited: "Unlimited",
  creditsUnavailable: "Unavailable",
  usageTotal: "All time",
  usageLast5Hours: "Last 5 hours",
  usageLast7Days: "Last 7 days",
  usageRequests: "requests",
  usageTokens: "tokens",
  usageNoLocalData: "No local records",
  usageLocalOnly: "Local records only",
  recordedCost: "Recorded local cost",
  recordedCostHint: "Based on local request records; not a provider billing statement.",
  never: "Never",
  rateLimited: "Rate limited",
  exhausted: "Quota exhausted",
  disabled: "Disabled",
  available: "Available",
  expired: "Expired",
  expiresAt: "Expires {time}",
  expiredAt: "Expired {time}",
  quotaUpdatedAt: "Quota synced {time}",
  accountActions: "Actions for {account}",
  save: "Save",
  edit: "Edit",
  cancel: "Cancel",
  sync: "Sync",
  enable: "Enable",
  disable: "Disable",
  delete: "Delete",
  editAccount: "Edit account",
  saveAccount: "Save account",
  cancelEdit: "Cancel edit",
  syncAccount: "Sync account",
  enableAccount: "Enable account",
  disableAccount: "Disable account",
  deleteAccount: "Delete account",
  exportAccountJSON: "Export account JSON",
  exportAccount: "Export JSON",
  exportAccountConfirm: "Download the full Codex credential?",
  exporting: "Exporting",
  accountExported: "The Codex account JSON was downloaded.",
  accountExportFailed: "Unable to create the account JSON download.",
  unknown: "unknown",
  accountMutationRefreshFailed: "Mutation succeeded; refresh failed: {message}",
  accountDeletePartial: "Credential deleted; statistics cleanup incomplete.",
  unconfigured: "Not configured",
  "anthropic.noAccounts": "No Anthropic accounts",
  "anthropic.accountsTitle": "Anthropic accounts",
  "anthropic.authType": "Authentication",
  "anthropic.apiKeyAuth": "API Key",
  "anthropic.profileAuth": "Official profile",
  "anthropic.modelCount": "{count} models",
  "anthropic.noQuotaData": "No quota data available",
  "anthropic.existingConfigSource": "Existing configuration / environment variable",
  "anthropic.readOnly": "Read only",
  "anthropic.quotaRequests": "Requests",
  "anthropic.quotaInputTokens": "Input tokens",
  "anthropic.quotaOutputTokens": "Output tokens",
  "anthropic.quotaRetryAfter": "Retry after {time}",
  "anthropic.quotaFetchedAt": "Fetched {time}",
  "anthropic.quotaRemaining": "Remaining: {count}",
  "anthropic.quotaLimit": "Limit: {count}",
  "anthropic.quotaUsed": "Used: {percent}%",
  "anthropic.quotaResetAt": "Resets {time}",
};
const translate = (key, params = {}) => String(labels[key] || key).replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? ""));

function render(accounts, now = Date.parse("2026-01-01T00:00:00Z"), options = {}) {
  return renderCodexAccountManagementTable(accounts, { translate, now, ...options });
}

test("Codex account table renders complete account data and escapes HTML", (t) => {
  // Pin the regional number locale so compact token counts render
  // deterministically regardless of the host environment's default locale.
  const previousRegional = getRegionalPreferences();
  setRegionalPreferences({ locale: "en-US" });
  t.after(() => setRegionalPreferences(previousRegional));
  const html = render([{
    id: "codex_fixture",
    alias: `Primary <script>alert(1)</script>`,
    email: "user@example.test",
    account_id: "workspace-1",
    priority: 7,
    plan_type: "team<unsafe>",
    disabled: false,
    stats: { success_count: 12, failure_count: 3, last_use_at: "2025-12-31T23:00:00Z", last_error_code: `bad<script>` },
    usage: {
      total: { requestCount: 828, inputTokens: 90000000, outputTokens: 27000000, totalTokens: 117000000, costUsd: 145.19 },
      last5Hours: { requestCount: 3, inputTokens: 1000, outputTokens: 200, totalTokens: 1200, costUsd: 0.12 },
      last7Days: { requestCount: 20, inputTokens: 5000, outputTokens: 1000, totalTokens: 6000, costUsd: 1.5 },
    },
    quota: { plan_type: "team", primary_window: { used_percent: 25, limit_window_seconds: 18000, reset_after_seconds: 3600 } },
  }]);
  assert.match(html, /codex-account-table/);
  assert.match(html, /settings-card-content/);
  assert.match(html, /scope="col"/);
  assert.match(html, /settings-inline-actions/);
  assert.match(html, /settings-badge/);
  assert.match(html, /workspace-1/);
  assert.match(html, />12<\/span> \/ <span[^>]*>3</);
  assert.match(html, /aria-valuenow="25"/);
  assert.match(html, />25%<\/strong>/);
  assert.doesNotMatch(html, /828 requests|(?:117M|1\.2億|1\.2亿) tokens|\$145\.19/);
  assert.match(html, /3 requests/);
  assert.match(html, /1\.2K tokens/);
  assert.match(html, /\$0\.1200/);
  assert.match(html, /20 requests/);
  assert.match(html, /6K tokens/);
  assert.match(html, /\$1\.50/);
  assert.match(html, /codex-usage-window-badge is-5h">5h/);
  assert.match(html, /codex-usage-window-badge is-7d">7d/);
  assert.match(html, /Resets in 1h 0m/);
  assert.match(html, /data-codex-select="codex_fixture"/);
  assert.match(html, /data-codex-batch-sync/);
  assert.match(html, /data-codex-edit="codex_fixture"/);
  assert.match(html, /data-codex-sync="codex_fixture"/);
  assert.match(html, /data-codex-export="codex_fixture"/);
  assert.match(html, /data-codex-toggle="codex_fixture"/);
  assert.match(html, /data-codex-delete="codex_fixture"/);
  assert.match(html, /codex-account-action-divider/);
  assert.doesNotMatch(html, /data-codex-save="codex_fixture"/);
  assert.doesNotMatch(html, /bad&lt;script&gt;|Expires |Expired 2025/);
  assert.doesNotMatch(html, /Credits|Balance |Quota synced/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /&lt;script&gt;/);

  const editHtml = render([{ id: "codex_fixture", alias: "Primary", priority: 7 }], undefined, {
    editing: { id: "codex_fixture", alias: "Edited", priority: 9 },
  });
  assert.match(editHtml, /data-codex-edit-alias="codex_fixture"/);
  assert.match(editHtml, /value="Edited"/);
  assert.match(editHtml, /data-codex-edit-priority="codex_fixture"/);
  assert.match(editHtml, /value="9"/);
  assert.match(editHtml, /data-codex-save="codex_fixture"/);
  assert.match(editHtml, /data-codex-edit-cancel="codex_fixture"/);
});

test("Codex account export filenames stay safe and end in JSON", () => {
  assert.equal(codexAccountExportFilename({ name: "primary.json" }, "codex_1"), "primary.json");
  assert.equal(codexAccountExportFilename({ name: `../secret\\backup` }, "codex_1"), "-secret-backup.json");
  assert.equal(codexAccountExportFilename({}, "codex_1"), "codex-codex_1.json");
});

test("Codex account normalization supports legacy response shapes", () => {
  const legacy = [{ name: "legacy.json", account_id: "legacy" }];
  assert.deepEqual(normalizeCodexAccountList(legacy), legacy);
  assert.deepEqual(normalizeCodexAccountList({ files: legacy }), legacy);
  assert.deepEqual(normalizeCodexAccountList({ accounts: legacy }), legacy);
  assert.deepEqual(normalizeCodexAccountList(null), []);
  const html = render(legacy);
  assert.match(html, /legacy\.json/);
  assert.match(html, /codex-priority-value">100/);
  assert.match(html, /data-codex-edit="legacy\.json"/);
  assert.match(html, /No quota/);
});

test("Codex quota rendering supports no quota and dual windows", () => {
  assert.match(render([{ id: "none", priority: 100 }]), /No quota/);
  const html = render([{
    id: "dual",
    priority: 100,
    quota: {
      primary_window: { used_percent: 20, limit_window_seconds: 18000, reset_at: "2026-01-01T02:30:00Z" },
      secondary_window: { used_percent: 65.5, limit_window_seconds: 604800, reset_after_seconds: 90061 },
    },
  }]);
  assert.match(html, /aria-valuenow="20"/);
  assert.match(html, />20%<\/strong>/);
  assert.match(html, /aria-valuenow="65\.5"/);
  assert.match(html, />65\.5%<\/strong>/);
  assert.match(html, /is-5h">5h/);
  assert.match(html, /Resets in 2h 30m/);
  assert.match(html, /is-7d">7d/);
  assert.match(html, /Resets in 1d 1h/);
});

test("Codex quota rendering supports camelCase windows and invalid numeric values", () => {
  const html = render([{
    id: "credits",
    accountID: "workspace<&>",
    priority: "invalid",
    stats: { successCount: "invalid", failureCount: 2, lastStatusCode: "status<script>" },
    quota: {
      planType: "pro<&>",
      primaryWindow: { usedPercent: 100, windowSeconds: 3600, resetAfterSeconds: 60 },
      credits: { hasCredits: true, balance: "12.50" },
    },
  }]);
  assert.match(html, /Rate limited/);
  assert.match(html, /aria-valuenow="100"/);
  assert.match(html, />100%<\/strong>/);
  assert.match(html, /is-5h">5h/);
  assert.match(html, /Resets in 1m/);
  assert.doesNotMatch(html, /codex-credits-summary|Credits|Balance \$12\.50/);
  assert.match(html, /codex-priority-value">100/);
  assert.doesNotMatch(html, /NaN/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /workspace&lt;&amp;&gt;/);
  assert.match(html, /pro&lt;&amp;&gt;/);
});

test("Codex quota hides cumulative usage and upstream credit explanations", () => {
  const html = render([{
    id: "unlimited",
    quota: { credits: { unlimited: true } },
    usage: { total: { requestCount: 2, totalTokens: 12, costUsd: 0.01 } },
  }]);
  assert.match(html, /No quota/);
  assert.doesNotMatch(html, /codex-usage-total-summary|All time|codex-credits-summary|Credits|Unlimited/);
});

test("Codex account status distinguishes disabled, limited, exhausted, expired, and available", () => {
  const now = Date.parse("2026-01-01T00:00:00Z");
  assert.match(render([{ id: "disabled", disabled: true }], now), />Disabled<\/span>/);
  assert.match(render([{ id: "limited", quota: { rate_limit_reached_type: "primary" } }], now), />Rate limited<\/span>/);
  assert.match(render([{ id: "exhausted", stats: { last_error_code: "quota_unauthorized", last_attempt_at: "2026-01-01T00:00:00Z" } }], now), />Quota exhausted<\/span>/);
  assert.match(render([{ id: "expired", expires_at: "2025-12-01T00:00:00Z", refreshable: false }], now), />Expired<\/span>/);
  assert.match(render([{ id: "ready" }], now), />Available<\/span>/);
  assert.equal(codexAccountStatus({ expires_at: "2025-12-01T00:00:00Z", refreshable: true }, { now }).key, "available");
  assert.equal(codexAccountStatus({ stats: { last_error_code: "oauth_unauthorized", last_attempt_at: "2026-01-01T00:00:00Z" } }, { now }).key, "available");
  assert.equal(codexAccountStatus({
    stats: { last_error_code: "quota_unauthorized", last_attempt_at: "2025-12-31T23:00:00Z", quota_fetched_at: "2026-01-01T00:00:00Z" },
    quota: { primary_window: { used_percent: 25 } },
  }, { now }).key, "available");
  assert.deepEqual(codexAccountOverview([
    { id: "ready" },
    { id: "limited", quota: { rate_limit_reached_type: "primary" } },
    { id: "exhausted", stats: { last_error_code: "quota_unauthorized", last_attempt_at: "2026-01-01T00:00:00Z" } },
    { id: "disabled", disabled: true },
    { id: "expired", expires_at: "2025-12-01T00:00:00Z" },
  ], { now }), { total: 5, available: 1, rateLimited: 1, exhausted: 1, disabled: 1, expired: 1 });
});

test("Codex mutation success is reported separately from a refresh failure", () => {
  assert.equal(codexMutationRefreshWarning(true, "ignored", translate), "");
  assert.equal(codexMutationRefreshWarning(false, "offline", translate), "Mutation succeeded; refresh failed: offline");
});

test("Codex partial delete results produce a dedicated warning", () => {
  assert.equal(codexDeleteResultWarning({ status: "ok", stats_deleted: true }, translate), "");
  assert.equal(codexDeleteResultWarning({ status: "partial" }, translate), "Credential deleted; statistics cleanup incomplete.");
  assert.equal(codexDeleteResultWarning({ status: "ok", stats_deleted: false }, translate), "Credential deleted; statistics cleanup incomplete.");
});

test("Codex account action requests cover save, sync, export, toggle, and delete", () => {
  const save = codexAccountActionRequest("save", "id/with space", { alias: "Main", priority: 9 });
  assert.equal(save.path, "/api/providers/oauth/codex/accounts/id%2Fwith%20space");
  assert.equal(save.options.method, "PATCH");
  assert.deepEqual(JSON.parse(save.options.body), { alias: "Main", priority: 9 });

  const sync = codexAccountActionRequest("sync", "codex_1");
  assert.deepEqual(sync, { path: "/api/providers/oauth/codex/accounts/codex_1/refresh", options: { method: "POST" } });
  const exported = codexAccountActionRequest("export", "id/with space");
  assert.equal(exported.path, "/api/providers/oauth/codex/accounts/id%2Fwith%20space/export");
  assert.deepEqual(exported.options, { method: "GET", headers: { "X-Autoto-Confirm": "export-codex-account" } });
  const toggle = codexAccountActionRequest("toggle", "codex_1", { disabled: true });
  assert.deepEqual(JSON.parse(toggle.options.body), { disabled: false });
  const remove = codexAccountActionRequest("delete", "codex_1");
  assert.equal(remove.options.method, "DELETE");
});

test("Codex batch requests normalize IDs and preserve failed selections", () => {
  const request = codexAccountBatchRequest("set_priority", [" codex_1 ", "codex_2", "codex_1", ""], { priority: "42" });
  assert.equal(request.path, "/api/providers/oauth/codex/accounts/batch");
  assert.equal(request.options.method, "POST");
  assert.deepEqual(JSON.parse(request.options.body), { ids: ["codex_1", "codex_2"], operation: "set_priority", priority: 42 });
  assert.deepEqual(JSON.parse(codexAccountBatchRequest("sync", ["codex_1"]).options.body), { ids: ["codex_1"], operation: "sync" });

  assert.deepEqual(normalizeCodexSelectedIds(["codex_1", "missing", "codex_1"], [{ id: "codex_1" }]), ["codex_1"]);
  assert.deepEqual(normalizeCodexBatchResult({
    total: 2,
    success: 1,
    failed: 1,
    results: [
      { id: "codex_1", success: true },
      { id: "codex_2", success: false, warning: "retry cleanup", retryable: true },
    ],
  }, ["codex_1", "codex_2"]), {
    total: 2,
    success: 1,
    failed: 1,
    failedIds: ["codex_2"],
    warnings: ["retry cleanup"],
    results: [
      { id: "codex_1", success: true, error: "", warning: "", retryable: false },
      { id: "codex_2", success: false, error: "", warning: "retry cleanup", retryable: true },
    ],
  });
});

test("Codex JSON file validation rejects wrong types and malformed payloads", () => {
  assert.deepEqual(validateCodexImportJSON("account.txt", "{}"), { valid: false, filename: "account.txt", code: "type" });
  assert.deepEqual(validateCodexImportJSON("empty.json", "  "), { valid: false, filename: "empty.json", code: "empty" });
  assert.deepEqual(validateCodexImportJSON("broken.json", "{"), { valid: false, filename: "broken.json", code: "parse" });
  assert.deepEqual(validateCodexImportJSON("scalar.json", "1"), { valid: false, filename: "scalar.json", code: "shape" });
  assert.deepEqual(validateCodexImportJSON("account.json", "{\"access_token\":\"secret\"}"), {
    valid: true,
    filename: "account.json",
    value: { access_token: "secret" },
  });
});

test("Codex batch import normalization keeps per-file success, skip, and failure details", () => {
  const request = codexImportBatchRequest([{ name: " one.json ", content: "{}" }, { filename: "two.json", content: "[]" }]);
  assert.equal(request.path, "/api/providers/oauth/codex/import/batch");
  assert.equal(request.options.method, "POST");
  assert.deepEqual(JSON.parse(request.options.body), { files: [{ filename: "one.json", content: "{}" }, { filename: "two.json", content: "[]" }] });

  assert.deepEqual(normalizeCodexImportBatchResult({
    status: "partial",
    total: 3,
    success: 1,
    skipped: 1,
    failed: 1,
    results: [
      { filename: "one.json", status: "success", format: "codex", imported: 2, skipped: 1, files: ["one-a.json", "one-b.json"] },
      { filename: "duplicate.json", status: "skipped", skipped: 2 },
      { filename: "broken.json", status: "failed", error: "JSON invalid" },
    ],
  }), {
    status: "partial",
    total: 3,
    successFiles: 1,
    skippedFiles: 1,
    failedFiles: 1,
    imported: 2,
    skipped: 3,
    results: [
      { filename: "one.json", status: "success", format: "codex", imported: 2, skipped: 1, files: ["one-a.json", "one-b.json"], error: "" },
      { filename: "duplicate.json", status: "skipped", format: "", imported: 0, skipped: 2, files: [], error: "" },
      { filename: "broken.json", status: "failed", format: "", imported: 0, skipped: 0, files: [], error: "JSON invalid" },
    ],
  });
});

test("Codex browser login requests and authorization URLs stay fail-closed", () => {
  assert.deepEqual(codexBrowserLoginRequest("start"), { path: "/api/providers/oauth/codex/login/start", options: { method: "POST" } });
  assert.deepEqual(codexBrowserLoginRequest("status", "id/with space"), { path: "/api/providers/oauth/codex/login/id%2Fwith%20space", options: {} });
  assert.deepEqual(codexBrowserLoginRequest("cancel", "codex-login-1"), { path: "/api/providers/oauth/codex/login/codex-login-1", options: { method: "DELETE" } });
  assert.throws(() => codexBrowserLoginRequest("status", ""));
  assert.throws(() => codexBrowserLoginRequest("unknown", "id"));

  const official = "https://auth.openai.com/oauth/authorize?client_id=fixture&state=opaque";
  assert.equal(trustedCodexBrowserAuthURL(official), true);
  for (const unsafe of [
    "http://auth.openai.com/oauth/authorize",
    "https://auth.openai.com.evil.test/oauth/authorize",
    "https://user:pass@auth.openai.com/oauth/authorize",
    "https://auth.openai.com/oauth/token",
    "javascript:alert(1)",
  ]) assert.equal(trustedCodexBrowserAuthURL(unsafe), false, unsafe);

  assert.deepEqual(normalizeCodexBrowserLoginStatus({ login_id: " login-1 ", status: "CANCELED", auth_url: official, expires_at: "2026-07-17T12:00:00Z", error: "authorization denied", account: { email: "a@example.test" } }), {
    loginId: "login-1",
    status: "cancelled",
    authUrl: official,
    expiresAt: "2026-07-17T12:00:00Z",
    message: "authorization denied",
    account: { email: "a@example.test" },
  });
});

test("Anthropic account requests follow the independent account API contract", () => {
  assert.deepEqual(anthropicAccountsListRequest(), { path: "/api/providers/auth/anthropic/accounts", options: {} });
  assert.equal(anthropicProfileLoginCommand(" work "), "ant auth login --profile work");
  assert.equal(anthropicProfileLoginCommand(""), "ant auth login --profile <name>");
  assert.equal(anthropicProfileLoginCommand("team'; rm -rf /"), `ant auth login --profile 'team'"'"'; rm -rf /'`);

  const profile = anthropicAccountCreateRequest({ authType: "profile", profile: " work ", alias: " Team ", priority: "7" });
  assert.equal(profile.path, "/api/providers/auth/anthropic/accounts");
  assert.equal(profile.options.method, "POST");
  assert.deepEqual(JSON.parse(profile.options.body), { authType: "profile", profile: "work", alias: "Team", priority: 7 });

  const key = anthropicAccountCreateRequest({ authType: "api_key", apiKey: " sk-ant-secret ", priority: 11 });
  assert.deepEqual(JSON.parse(key.options.body), { authType: "api_key", apiKey: "sk-ant-secret", priority: 11 });
  const form = { elements: { authType: { value: "api_key" }, apiKey: { value: "sk-ant-once" }, alias: { value: "Main" }, priority: { value: "9" } } };
  const consumed = consumeAnthropicAccountCreateRequest(form);
  assert.equal(form.elements.apiKey.value, "", "submitted secrets are removed from the live form immediately");
  assert.deepEqual(JSON.parse(consumed.options.body), { authType: "api_key", apiKey: "sk-ant-once", alias: "Main", priority: 9 });

  const save = anthropicAccountActionRequest("save", "id/with space", { alias: "Primary", priority: 3 });
  assert.equal(save.path, "/api/providers/auth/anthropic/accounts/id%2Fwith%20space");
  assert.equal(save.options.method, "PATCH");
  assert.deepEqual(JSON.parse(save.options.body), { alias: "Primary", priority: 3 });
  assert.deepEqual(anthropicAccountActionRequest("sync", "anthropic_1"), { path: "/api/providers/auth/anthropic/accounts/anthropic_1/sync", options: { method: "POST" } });
  assert.deepEqual(JSON.parse(anthropicAccountActionRequest("toggle", "anthropic_1", { disabled: false }).options.body), { disabled: true });
  assert.equal(anthropicAccountActionRequest("delete", "anthropic_1").options.method, "DELETE");
});

test("Anthropic account table normalizes responses, escapes HTML, and exposes account interactions", () => {
  const accounts = [{
    id: `anthropic"><script>alert(1)</script>`,
    alias: `Main <img src=x onerror=alert(1)>`,
    priority: 4,
    auth_type: "profile",
    profile: `work<&>`,
    source: `cli<script>`,
    configured: true,
    models: ["claude-sonnet", "claude-opus"],
    stats: { success_count: 8, failure_count: 2, last_use_at: "2026-07-15T12:00:00Z" },
  }];
  assert.deepEqual(normalizeAnthropicAccountList({ accounts }), accounts);
  assert.deepEqual(normalizeAnthropicAccountList({ items: accounts }), accounts);
  assert.deepEqual(normalizeAnthropicAccountList(null), []);
  const html = renderAnthropicAccountManagementTable(accounts, { translate });
  assert.match(html, /anthropic-account-table/);
  assert.match(html, /Official profile/);
  assert.match(html, />8<\/span> \/ <span[^>]*>2</);
  assert.match(html, /2 models/);
  assert.match(html, /No quota data available/);
  assert.match(html, /data-anthropic-edit=/);
  assert.match(html, /data-anthropic-sync=/);
  assert.match(html, /data-anthropic-toggle=/);
  assert.match(html, /data-anthropic-delete=/);
  assert.doesNotMatch(html, /<script>|<img /);
  assert.match(html, /&lt;img/);

  const edit = renderAnthropicAccountManagementTable([{ id: "a1", alias: "Main", priority: 4 }], { translate, editing: { id: "a1", alias: "Edited", priority: 6 } });
  assert.match(edit, /data-anthropic-edit-alias="a1"/);
  assert.match(edit, /value="Edited"/);
  assert.match(edit, /data-anthropic-edit-priority="a1"/);
  assert.match(edit, /data-anthropic-save="a1"/);
  assert.match(edit, /data-anthropic-edit-cancel="a1"/);

  const fallback = renderAnthropicAccountManagementTable([{
    id: "configured-fallback",
    alias: "Configured fallback",
    configured: true,
    managed: false,
    auth_type: "api_key",
    source: "server-env",
  }], { translate, editing: { id: "configured-fallback", alias: "Must not edit", priority: 1 } });
  assert.match(fallback, /Existing configuration \/ environment variable/);
  assert.match(fallback, /Read only/);
  assert.doesNotMatch(fallback, /server-env|Must not edit/);
  assert.doesNotMatch(fallback, /data-anthropic-(?:edit|save|sync|toggle|delete)/);
});

test("Anthropic nested rate limits render requests first with token and retry metadata", () => {
  const html = renderAnthropicAccountManagementTable([{
    id: "limited",
    configured: true,
    rate_limit: {
      requests: { limit: 100, remaining: 0, reset: "2026-07-16T13:00:00Z" },
      input_tokens: { limit: 10000, remaining: 7500 },
      output_tokens: { limit: 5000, remaining: 4000 },
      retry_after: 30,
      fetched_at: "2026-07-16T12:00:00Z",
    },
  }], { translate });
  assert.match(html, />Rate limited<\/span>/);
  assert.match(html, />Requests<\/strong>/);
  assert.match(html, /Remaining: 0/);
  assert.match(html, /Limit: 100/);
  assert.match(html, />Input tokens<\/strong>/);
  assert.match(html, />Output tokens<\/strong>/);
  assert.match(html, /Retry after 30s/);
  assert.match(html, /Fetched/);
  assert.doesNotMatch(html, /No quota data available/);
});

test("Anthropic status and overview distinguish disabled, limited, unconfigured, and available accounts", () => {
  assert.equal(anthropicAccountStatus({ disabled: true }).key, "disabled");
  assert.equal(anthropicAccountStatus({ rate_limit: { remaining: 50, requests: { remaining: 0 } } }).key, "rateLimited");
  assert.equal(anthropicAccountStatus({ rate_limit: { limited: true, requests: { remaining: 2 } } }).key, "available");
  assert.equal(anthropicAccountStatus({ configured: false }).key, "unconfigured");
  assert.equal(anthropicAccountStatus({ configured: true }).key, "available");
  assert.deepEqual(anthropicAccountOverview([
    { configured: true },
    { rate_limit: { limited: true } },
    { disabled: true },
  ]), { total: 3, available: 1, rateLimited: 1, disabled: 1 });
});

test("modelProvider translation keys stay aligned across all three locales", () => {
  const keys = (value, prefix = "") => Object.entries(value || {}).flatMap(([key, item]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    return item && typeof item === "object" && !Array.isArray(item) ? keys(item, path) : [path];
  }).sort();
  const expected = keys(messagesZhCN.modelProvider);
  assert.deepEqual(keys(messagesZhTW.modelProvider), expected);
  assert.deepEqual(keys(messagesEN.modelProvider), expected);
});

test("缺少必填 API Key 的预检使用三语言警告，不显示成功", () => {
  const expected = {
    "zh-CN": "需要 API Key，尚未执行连接预检。",
    "zh-TW": "需要 API Key，尚未執行連線預檢。",
    en: "An API Key is required; connection preflight was not run.",
  };
  const catalogs = { "zh-CN": messagesZhCN, "zh-TW": messagesZhTW, en: messagesEN };
  for (const [locale, message] of Object.entries(expected)) {
    const result = providerPreflightResult(
      { configured: false, reachable: false, errorCode: "not_configured" },
      (key, params) => {
        const [scope, name] = key.split(".");
        const template = catalogs[locale].modelProvider.console[scope]?.[name] || key;
        return template.replace(/\{(\w+)\}/g, (_, field) => String(params?.[field] ?? ""));
      },
    );
    assert.deepEqual(result, { message, tone: "warning", terminalLevel: "warn" }, locale);
    assert.notEqual(result.tone, "success", locale);
  }

  const success = providerPreflightResult(
    { configured: true, reachable: true },
    (key) => key,
  );
  assert.equal(success.tone, "success");
});

test("供应商敏感草稿只允许 localhost 或完整远程会话", () => {
  assert.equal(providerSensitiveDraftAccessAllowed({}, { hostname: "localhost" }), true);
  assert.equal(providerSensitiveDraftAccessAllowed({
    remoteAccess: {
      session: { remote: true, authenticated: true, mode: "restricted" },
      capabilities: { maxPermissionMode: "acceptEdits" },
    },
  }, { hostname: "remote.example.test" }), false);
  assert.equal(providerSensitiveDraftAccessAllowed({
    remoteAccess: {
      session: { remote: true, authenticated: true, mode: "full" },
      capabilities: { maxPermissionMode: "bypassPermissions" },
    },
  }, { hostname: "remote.example.test" }), true);
});

test("Gemini/Grok/Kimi 各自作为独立官方卡片进入独立管理页且互不堆叠", () => {
  const state = {
    settings: { providers: [
      { name: "gemini", type: "gemini", origin: "builtin", enabled: true, configured: true },
      { name: "grok", type: "grok", origin: "builtin", enabled: true, configured: true },
      { name: "kimi", type: "kimi", origin: "builtin", enabled: true, configured: true },
      { name: "gemini-relay", type: "gemini-interactions", origin: "custom", enabled: true, configured: true },
    ] },
    modelCatalog: { providers: [] },
    providerConsole: {},
    subscriptionAccounts: {
      gemini: [{ id: "gemini-acct", alias: "Gemini Only" }],
      grok: [{ id: "grok-acct", alias: "Grok Only" }],
      kimi: [{ id: "kimi-acct", alias: "Kimi Only" }],
    },
  };
  const controller = createModelProviderSettingsController({ state, loadModelCatalog: async () => {}, requestAPI: async () => ({}) });

  state.providerConsole.view = "providers";
  const listHTML = controller.renderProviderSettingsContent();
  for (const provider of ["gemini", "grok", "kimi"]) {
    assert.match(listHTML, new RegExp(`data-mp-provider-card="${provider}"`));
  }

  controller.openGeminiConsolePage({ name: "gemini", type: "gemini" });
  assert.equal(state.providerConsole.view, "gemini");
  const geminiHTML = controller.renderProviderSettingsContent();
  assert.match(geminiHTML, /Gemini Only/);
  assert.doesNotMatch(geminiHTML, /Grok Only|Kimi Only/);

  controller.openKimiConsolePage({ name: "kimi", type: "kimi" });
  assert.equal(state.providerConsole.view, "kimi");
  const kimiHTML = controller.renderProviderSettingsContent();
  assert.match(kimiHTML, /Kimi Only/);
  assert.doesNotMatch(kimiHTML, /Gemini Only|Grok Only/);
  // gemini-interactions is a separate custom provider, never the Gemini account page.
  assert.equal(subscriptionProviderKind({ name: "gemini-relay", type: "gemini-interactions", origin: "custom" }), "");
});

test("官方账号概览优先加载 Codex 与 Antigravity，并在模型刷新后重读快照", async () => {
  const state = {
    settings: { providers: [
      { name: "codex", type: "codex", origin: "builtin", enabled: true, configured: true },
      { name: "gemini", type: "gemini", origin: "builtin", enabled: true, configured: true },
      { name: "anthropic", type: "anthropic", origin: "builtin", enabled: true, configured: true },
      { name: "grok", type: "grok", origin: "builtin", enabled: true, configured: true },
      { name: "kimi", type: "kimi", origin: "builtin", enabled: true, configured: true },
      { name: "kiro", type: "kiro", origin: "builtin", enabled: true, configured: true },
    ] },
    modelCatalog: { providers: [] },
    providerConsole: { view: "providers" },
    providerAuthFiles: null,
    providerAuthSeq: 0,
    providerAuthError: "",
    providerAuthLoading: false,
  };
  const requests = [];
  const events = [];
  const controller = createModelProviderSettingsController({
    state,
    refreshActiveSettingsPanel() {},
    loadModelCatalog: async () => { events.push("catalog"); },
    requestAPI: async (path) => {
      requests.push(path);
      return { accounts: [] };
    },
  });

  await controller.loadOfficialProviderAccountOverview({ defer: false });
  assert.deepEqual(requests, [
    "/api/providers/oauth/codex/accounts",
    "/api/providers/auth/gemini/accounts",
    "/api/providers/auth/anthropic/accounts",
    "/api/providers/auth/grok/accounts",
    "/api/providers/auth/kimi/accounts",
    "/api/providers/auth/kiro/accounts",
  ]);
  await controller.loadOfficialProviderAccountOverview({ defer: false });
  assert.equal(requests.length, 6, "loaded account sources must not be requested twice");

  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => null };
  try {
    await controller.refreshModelCatalog();
  } finally {
    globalThis.document = previousDocument;
  }
  assert.deepEqual(events, ["catalog"]);
  assert.equal(requests.length, 12);
  assert.deepEqual(new Set(requests.slice(6)), new Set([
    "/api/providers/oauth/codex/accounts",
    "/api/providers/auth/anthropic/accounts",
    "/api/providers/auth/gemini/accounts",
    "/api/providers/auth/grok/accounts",
    "/api/providers/auth/kimi/accounts",
    "/api/providers/auth/kiro/accounts",
  ]));
});

test("受限远程会话不会发送供应商草稿或 API Key", async () => {
  let requests = 0;
  const state = {
    settings: { providers: [] },
    modelCatalog: { providers: [] },
    remoteAccess: {
      session: { remote: true, authenticated: true, mode: "restricted" },
      capabilities: { maxPermissionMode: "acceptEdits" },
    },
    providerConsole: {
      view: "providers", drawer: "provider", mode: "create", type: "openai", providerName: "openai-test", busy: {},
      draft: { ...createProviderDraft("openai"), name: "openai-test", apiKey: "sk-local-only", apiKeyDraft: true },
    },
  };
  const form = {
    elements: {
      name: { value: "openai-test", setCustomValidity() {}, setAttribute() {} },
      type: { value: "openai" }, baseUrl: { value: "" }, apiKey: { value: "sk-local-only" },
      model: { value: "" }, maxTokens: { value: "0" }, apiKeyOptional: { checked: false }, insecureSkipTLSVerify: { checked: false },
    },
    querySelector() { return null; },
    querySelectorAll() { return []; },
  };
  const controller = createModelProviderSettingsController({
    state,
    requestAPI: async () => { requests += 1; return {}; },
  });
  assert.equal(await controller.discoverConsoleProviderModels(form), false);
  assert.equal(requests, 0);
  assert.equal(state.providerConsole.draft.apiKey, "sk-local-only");
  assert.match(state.providerConsole.result.message, /受限远程会话/);
});

test("模型发现去重、合并 Token 能力并保留已有编辑", () => {
  const discovered = providerModelDiscovery({
    models: [" model-b ", "model-a", "model-b", ""],
    modelCapabilities: { "model-a": { contextTokenLimit: 128000 } },
  }, "missing", [{ name: "model-b", contextTokenLimit: 64000, hidden: true }]);
  assert.deepEqual(discovered.models, ["model-b", "model-a", "missing"]);
  assert.equal(discovered.selectedModel, "missing");
  assert.deepEqual(discovered.modelConfigs, [
    { name: "model-b", contextTokenLimit: 64000, imageGeneration: false, hidden: true, manual: false },
    { name: "model-a", contextTokenLimit: 128000, imageGeneration: false, hidden: false, manual: false },
    { name: "missing", contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: true },
  ]);
  assert.equal(providerModelDiscovery({ models: ["model-b", "model-a"] }, "model-a").selectedModel, "model-a");
});

test("图片生成模型开关默认关闭并在重新发现时保留已有值", () => {
  const normalized = normalizeProviderModelConfigs({
    modelConfigs: [{ name: "image-model", imageGeneration: true }, { name: "new-model" }],
  });
  assert.equal(normalized[0].imageGeneration, true);
  assert.equal(normalized[1].imageGeneration, false);
  const merged = mergeProviderModelDiscovery(normalized, { models: ["new-model", "image-model", "fresh-model"] }, "image-model");
  assert.deepEqual(merged.map((item) => [item.name, item.imageGeneration]), [
    ["new-model", false],
    ["image-model", true],
    ["fresh-model", false],
  ]);
});

// 保存会整体替换 models 数组，所以发现结果里少了某个模型时不能丢掉它的行：
// 上游列表失败会退化成只有默认模型，那一次保存就会把已配置的上限抹掉。
test("重新发现时保留已配置上限的模型，即使上游不再列出它", () => {
  const configured = normalizeProviderModelConfigs({
    modelConfigs: [
      { name: "claude-opus-5", contextTokenLimit: 1000000 },
      { name: "claude-haiku-4-5" },
      { name: "image-model", imageGeneration: true },
    ],
  });
  // 上游只回默认模型（例如 502 后的 fallback 列表）。
  const merged = mergeProviderModelDiscovery(configured, { models: ["claude-haiku-4-5"] }, "claude-haiku-4-5");
  const names = merged.map((item) => item.name);
  assert.ok(names.includes("claude-opus-5"), "带上限的模型必须保留");
  assert.equal(merged.find((item) => item.name === "claude-opus-5").contextTokenLimit, 1000000);
  assert.ok(names.includes("image-model"), "开启图片生成的模型也是用户配置");
  // 没有任何用户配置的行可以随发现结果消失。
  assert.ok(!names.includes("unconfigured-model"));
});

test("未设定的行不会把发现到的真实上限覆盖成 0", () => {
  const unset = normalizeProviderModelConfigs({ modelConfigs: [{ name: "claude-opus-5" }] });
  assert.equal(unset[0].contextTokenLimit, 0);
  const merged = mergeProviderModelDiscovery(
    unset,
    { models: ["claude-opus-5"], modelCapabilities: { "claude-opus-5": { contextTokenLimit: 1000000 } } },
    "claude-opus-5",
  );
  assert.equal(merged[0].contextTokenLimit, 1000000);
});

test("已显式设定的上限优先于发现结果", () => {
  const explicit = normalizeProviderModelConfigs({ modelConfigs: [{ name: "m", contextTokenLimit: 250000 }] });
  const merged = mergeProviderModelDiscovery(
    explicit,
    { models: ["m"], modelCapabilities: { m: { contextTokenLimit: 128000 } } },
    "m",
  );
  assert.equal(merged[0].contextTokenLimit, 250000);
});

test("图片生成能力只控制模型开关且 OpenAI/Codex 兼容缺省能力", () => {
  assert.equal(providerSupportsImageGeneration({ type: "openai" }), true);
  assert.equal(providerSupportsImageGeneration({ type: "codex" }), true);
  assert.equal(providerSupportsImageGeneration({ type: "openai", capabilities: { imageGeneration: false } }), false);
  assert.equal(providerSupportsImageGeneration({ type: "anthropic", capabilities: { imageGeneration: true } }), true);

  const supported = renderProviderConsolePage({ providers: [], consoleState: { drawer: "provider", mode: "edit", type: "openai", draft: { name: "openai", type: "openai", model: "gpt-image", modelConfigs: [{ name: "gpt-image", imageGeneration: true }], modelsReady: true, capabilities: { imageGeneration: true } } } });
  assert.match(supported, /data-mp-model-image-generation="gpt-image" checked/);
  assert.doesNotMatch(supported, /data-mp-model-image-generation="gpt-image"[^>]*disabled/);

  const unsupported = renderProviderConsolePage({ providers: [], consoleState: { drawer: "provider", mode: "edit", type: "anthropic", draft: { name: "anthropic", type: "anthropic", model: "claude", modelConfigs: [{ name: "claude", imageGeneration: true }], modelsReady: true, capabilities: { imageGeneration: false } } } });
  assert.match(unsupported, /data-mp-model-image-generation="claude" checked disabled/);
  assert.match(unsupported, /协议不支持/);
  assert.doesNotMatch(unsupported, /data-mp-save-provider disabled/);
});

test("旧 Provider 数据合成 modelConfigs 并兼容对象模型与 capabilities", () => {
  const normalized = normalizeConsoleProvider({
    name: "legacy",
    model: "model-b",
    models: ["model-a", { name: "model-b", contextTokenLimit: 64000 }, "model-a"],
    modelCapabilities: { "model-a": { contextTokenLimit: 128000 } },
  });
  assert.deepEqual(normalized.models, ["model-a", "model-b"]);
  assert.deepEqual(normalized.modelConfigs, [
    { name: "model-a", contextTokenLimit: 128000, imageGeneration: false, hidden: false, manual: false },
    { name: "model-b", contextTokenLimit: 64000, imageGeneration: false, hidden: false, manual: false },
  ]);
  const draft = createProviderDraft("openai-compatible", normalized);
  assert.equal(draft.modelsReady, true);
  assert.equal(draft.modelsStale, false);
});

test("连接关键字段变化只标记 stale，模型编辑与名称前缀变化不标记", () => {
  const ready = {
    name: "relay",
    type: "openai-compatible",
    baseUrl: "https://one.example/v1",
    apiKey: "draft-secret",
    apiKeyOptional: false,
    proxyUrl: "",
    userAgent: "",
    requestHeaders: [],
    insecureSkipTLSVerify: false,
    model: "model-a",
    modelConfigs: [{ name: "model-a", contextTokenLimit: 128000, hidden: false }],
    modelsReady: true,
    modelsStale: false,
  };
  const renamed = markProviderModelsStale(ready, { ...ready, name: "renamed", model: "model-a", maxTokens: 99 });
  assert.equal(renamed.modelsStale, false);
  const changed = markProviderModelsStale(ready, { ...ready, baseUrl: "https://two.example/v1" });
  assert.equal(changed.modelsStale, true);
  assert.deepEqual(changed.modelConfigs, ready.modelConfigs);
  assert.notEqual(providerConnectionFingerprint(ready), providerConnectionFingerprint(changed));
  assert.equal(providerModelDraftUsable(changed), false);
});

test("隐藏只影响选单显示，全部隐藏时仍保留内部默认模型并允许保存", () => {
  const configs = [
    { name: "a", contextTokenLimit: 100, hidden: false },
    { name: "b", contextTokenLimit: 200, hidden: false },
  ];
  const hiddenDefault = setProviderModelHidden(configs, "a", true, "a", { allowEmpty: true });
  assert.equal(hiddenDefault.changed, true);
  assert.equal(hiddenDefault.defaultModel, "a");
  assert.equal(hiddenDefault.modelConfigs[0].hidden, true);
  const allHidden = setProviderModelHidden(hiddenDefault.modelConfigs, "b", true, "a", { allowEmpty: true });
  assert.equal(allHidden.changed, true);
  assert.equal(allHidden.modelConfigs.every((item) => item.hidden), true);
  assert.equal(providerModelDraftUsable({ model: "a", modelConfigs: allHidden.modelConfigs, modelsReady: true, modelsStale: false }), true);
  assert.equal(providerModelDraftUsable({ model: "missing", modelConfigs: allHidden.modelConfigs, modelsReady: true, modelsStale: false }), true);
});

test("allowEmpty lets an official provider hide every model; custom providers keep one", () => {
  const configs = [
    { name: "a", contextTokenLimit: 100, hidden: false },
    { name: "b", contextTokenLimit: 200, hidden: true },
  ];
  // Custom provider: the last visible model cannot be hidden.
  const blocked = setProviderModelHidden(configs, "a", true, "a");
  assert.equal(blocked.changed, false);
  assert.equal(blocked.modelConfigs[0].hidden, false);

  // Official provider: hiding the last one is allowed, and the default survives
  // because hiding only removes the entry from the picker.
  const allowed = setProviderModelHidden(configs, "a", true, "a", { allowEmpty: true });
  assert.equal(allowed.changed, true);
  assert.equal(allowed.modelConfigs[0].hidden, true);
  assert.equal(allowed.defaultModel, "a");

  // The same distinction applies to the hide-all toggle.
  const keptOne = setProviderModelHiddenAll(configs, true, "a");
  assert.equal(keptOne.modelConfigs.filter((m) => !m.hidden).length, 1);
  const hidAll = setProviderModelHiddenAll(configs, true, "a", { allowEmpty: true });
  assert.equal(hidAll.changed, true);
  assert.equal(hidAll.modelConfigs.every((m) => m.hidden), true);
  assert.equal(hidAll.defaultModel, "a");
});

test("批量可见性开关：隐藏全部保留一个默认，显示全部恢复", () => {
  const configs = [
    { name: "a", contextTokenLimit: 100, hidden: false },
    { name: "b", contextTokenLimit: 200, hidden: false },
    { name: "c", contextTokenLimit: 300, hidden: false },
  ];
  const hidden = setProviderModelHiddenAll(configs, true, "b");
  assert.equal(hidden.changed, true);
  assert.equal(hidden.defaultModel, "b");
  assert.deepEqual(hidden.modelConfigs.map((m) => `${m.name}:${m.hidden ? 1 : 0}`), ["a:1", "b:0", "c:1"]);
  const shown = setProviderModelHiddenAll(hidden.modelConfigs, false, "b");
  assert.equal(shown.changed, true);
  assert.equal(shown.modelConfigs.every((m) => !m.hidden), true);
  // With no configured default the first entry is the one kept visible, and the
  // (empty) default is still not rewritten.
  const noDefault = setProviderModelHiddenAll(configs, true, "");
  assert.equal(noDefault.defaultModel, "");
  assert.equal(noDefault.modelConfigs.filter((m) => !m.hidden).length, 1);
  assert.equal(noDefault.modelConfigs[0].hidden, false);
});

test("可见性偏好重命名和删除只处理当前 Provider 前缀", () => {
  const preferences = {
    hiddenModels: { "old:a": true, "old:orphan": true, "other:z": true },
    showUnconfiguredProviders: true,
  };
  const migrated = providerVisibilityPreferencesForDraft(preferences, "old", "new", [
    { name: "a", hidden: false },
    { name: "b", hidden: true },
  ]);
  assert.deepEqual(migrated.hiddenModels, { "other:z": true, "new:b": true });
  assert.equal(migrated.showUnconfiguredProviders, true);
  assert.deepEqual(removeProviderVisibilityPreferences(migrated, "new").hiddenModels, { "other:z": true });
});

test("模型 payload 仅发送 name/contextTokenLimit/imageGeneration 且不泄露 UI 元数据", () => {
  const payload = providerConfigPayload({
    name: "relay",
    type: "openai-compatible",
    model: "a",
    modelConfigs: [{ name: "a", contextTokenLimit: 128000, imageGeneration: true, hidden: true, manual: true }],
    modelsReady: true,
    modelsStale: false,
  });
  assert.deepEqual(payload.models, [{ name: "a", contextTokenLimit: 128000, imageGeneration: true }]);
  for (const privateField of ["hidden", "manual", "modelsReady", "modelsStale"]) assert.equal(JSON.stringify(payload).includes(privateField), false);
});

test("扁平供应商页面允许全部隐藏模型并保留 ready/stale 保存约束", () => {
  const base = {
    name: "relay",
    type: "openai-compatible",
    baseUrl: "https://relay.example/v1",
    model: "a",
    modelConfigs: [{ name: "a", contextTokenLimit: 128000, hidden: true }],
    models: ["a"],
    modelsReady: true,
    modelsStale: false,
    origin: "custom",
  };
  const ready = renderProviderConsolePage({ providers: [], consoleState: { drawer: "provider", mode: "create", type: base.type, dirty: true, draft: base } });
  assert.match(ready, /mp-provider-reference-layout/);
  assert.match(ready, /mp-provider-reference-header/);
  assert.match(ready, /data-mp-provider-prefix-preview/);
  assert.match(ready, /data-mp-toggle-api-key/);
  assert.match(ready, /mp-provider-header-add-bar/);
  assert.match(ready, /mp-provider-reference-protocol/);
  assert.doesNotMatch(ready, /mp-provider-steps|mp-provider-create-section mp-provider-create-section-linear/);
  assert.match(ready, /mp-provider-model-config-row is-hidden[^>]*data-mp-model-config="a"/);
  assert.doesNotMatch(ready, /data-mp-model-visibility="a"[^>]*disabled/);
  assert.match(ready, /type="hidden" name="model" value="a"/);
  assert.match(ready, /data-mp-save-provider  >/);
  assert.doesNotMatch(ready, /data-mp-save-provider disabled/);
  assert.doesNotMatch(ready, /name="maxTokens"|data-mp-model-example|mp-provider-reference-final/);
  const stale = renderProviderConsolePage({ providers: [], consoleState: { drawer: "provider", mode: "create", type: base.type, dirty: true, draft: { ...base, modelsStale: true } } });
  assert.match(stale, /data-models-stale="true"/);
  assert.match(stale, /data-mp-test-provider disabled/);
  assert.match(stale, /data-mp-save-provider disabled/);
  const restricted = renderProviderConsolePage({ providers: [], consoleState: { drawer: "provider", mode: "create", type: base.type, dirty: true, sensitiveAccessAllowed: false, draft: base } });
  assert.match(restricted, /受限远程会话/);
  assert.match(restricted, /data-mp-fetch-models disabled/);
  assert.match(restricted, /data-mp-test-provider disabled/);
  assert.match(restricted, /data-mp-save-provider disabled/);
});

test("控制器最终保存按 config、enable、可见性偏好、刷新顺序执行", async () => {
  const order = [];
  const state = {
    settings: { providers: [] },
    modelCatalog: { providers: [] },
    providerConsole: {
      view: "providers",
      drawer: "provider",
      mode: "edit",
      type: "openai-compatible",
      providerName: "old-relay",
      busy: {},
      dirty: true,
      draft: {
        name: "new-relay",
        type: "openai-compatible",
        baseUrl: "https://relay.example/v1",
        apiKey: "draft-secret",
        apiKeyDraft: true,
        apiKeyOptional: true,
        model: "a",
        models: ["a", "b"],
        modelConfigs: [
          { name: "a", contextTokenLimit: 128000, hidden: true },
          { name: "b", contextTokenLimit: 64000, hidden: true },
        ],
        modelsReady: true,
        modelsStale: false,
        origin: "custom",
      },
    },
  };
  const form = {
    elements: {
      name: { value: "new-relay", setCustomValidity() {}, setAttribute() {} },
      type: { value: "openai-compatible" },
      baseUrl: { value: "https://relay.example/v1" },
      apiKey: { value: "draft-secret" },
      apiKeyOptional: { checked: true },
      model: { value: "a" },
      maxTokens: { value: "0" },
      insecureSkipTLSVerify: { checked: false },
    },
    querySelector() { return null; },
    querySelectorAll() { return []; },
  };
  const controller = createModelProviderSettingsController({
    state,
    requestAPI: async (path, options = {}) => {
      order.push(options.method === "PUT" ? "config" : options.method === "PATCH" ? "enable" : path);
      return {};
    },
    getModelVisibilityPreference: () => ({ hiddenModels: { "old-relay:b": true, "other:z": true }, showUnconfiguredProviders: false }),
    setModelVisibilityPreference: async (value) => {
      order.push("visibility");
      assert.deepEqual(value.hiddenModels, { "other:z": true, "new-relay:a": true, "new-relay:b": true });
    },
    loadSettings: async () => { order.push("settings"); },
    loadModelCatalog: async () => { order.push("catalog"); },
  });
  await controller.saveConsoleProvider(form);
  assert.deepEqual(order, ["config", "enable", "visibility", "settings", "catalog"]);
  assert.equal(state.providerConsole.mode, "edit");
  assert.equal(state.providerConsole.draft.apiKey, "draft-secret");
  assert.equal(state.providerConsole.draft.apiKeyDraft, true);
  controller.discardProviderConsoleDraft();
  assert.equal(state.providerConsole.draft, null);
});

test("model catalog preserves reasoning effort capabilities for composer controls", () => {
  const controller = createModelProviderSettingsController({
    state: {
      modelCatalog: {
        providers: [{
          name: "openai",
          type: "openai",
          models: ["gpt-5"],
          configured: true,
          capabilities: { reasoningEffortValues: ["low", "medium", "high", "xhigh"] },
          modelCapabilities: { "gpt-5": { fastMode: true } },
        }],
      },
      settings: { providers: [] },
      agent: { model: "openai:gpt-5" },
    },
  });

  assert.deepEqual(controller.currentProviderConfig("openai:gpt-5").capabilities, {
    tools: false,
    streaming: false,
    imageInput: false,
    reasoningEffort: true,
    reasoningEfforts: ["low", "medium", "high", "xhigh"],
  });
  assert.deepEqual(controller.currentProviderConfig("openai:gpt-5").modelCapabilities, {
    "gpt-5": { fastMode: true },
  });
});

test("model catalog passes through object-shaped Provider reasoning capabilities", () => {
  const controller = createModelProviderSettingsController({
    state: {
      modelCatalog: {
        providers: [{
          name: "provider",
          type: "openai-compatible",
          models: ["reasoning-model"],
          configured: true,
          capabilities: { reasoningEffort: { supportedValues: ["medium", "xhigh"] } },
        }],
      },
      settings: { providers: [] },
    },
  });

  assert.deepEqual(controller.currentProviderConfig("provider:reasoning-model").capabilities, {
    tools: false,
    streaming: false,
    imageInput: false,
    reasoningEffort: true,
    reasoningEfforts: ["medium", "xhigh"],
  });
});

test("模型上下文上限未设定时输入框留空并提示回退值，不伪造已保存的数字", () => {
  const html = renderProviderModelEditor({
    modelConfigs: [{ name: "unset-model" }, { name: "set-model", contextTokenLimit: 272000 }],
    capabilities: {},
    modelsReady: true,
  }, false, true);
  const inputs = html.match(/<input[^>]*data-mp-model-token="[^"]*"[^>]*>/g) || [];
  assert.equal(inputs.length, 2);

  const unset = inputs.find((item) => item.includes('data-mp-model-token="unset-model"'));
  // 旧行为把 272000 直接写进 value，看起来像已保存，实际后端存的是 0 并回退到 120000。
  assert.match(unset, /value=""/);
  assert.doesNotMatch(unset, /value="272000"/);
  assert.match(unset, /placeholder="[^"]*120000[^"]*"|placeholder="未设定[^"]*"/);

  const set = inputs.find((item) => item.includes('data-mp-model-token="set-model"'));
  assert.match(set, /value="272000"/);
});

test("合并供应商时空 capabilities 不覆盖已知能力，思考强度档位得以保留", () => {
  const capabilities = {
    tools: true,
    streaming: true,
    imageInput: true,
    reasoningEffort: true,
    reasoningEfforts: ["low", "medium", "high"],
  };
  const settings = [{ name: "myrelay", type: "anthropic", model: "claude-sonnet-4-5", configured: true, enabled: true, capabilities }];

  // 模型目录始终序列化 capabilities 字段，空对象曾经会覆盖设置侧的正确能力。
  for (const catalogCapabilities of [{}, undefined]) {
    const providers = modelProvidersForUIUnion(settings, [
      { name: "myrelay", type: "anthropic", configured: true, models: ["claude-sonnet-4-5"], capabilities: catalogCapabilities },
    ]);
    const provider = providers.find((item) => item.name === "myrelay");
    assert.deepEqual(provider.capabilities.reasoningEfforts, ["low", "medium", "high"]);
    assert.equal(provider.capabilities.reasoningEffort, true);
    assert.deepEqual(
      reasoningEffortValuesForModel(provider, "myrelay:claude-sonnet-4-5"),
      ["auto", "low", "medium", "high"],
    );
  }

  // 目录侧有能力时以目录为准，因为它反映运行时实例。
  const narrowed = modelProvidersForUIUnion(settings, [
    { name: "myrelay", type: "anthropic", configured: true, capabilities: { reasoningEffort: true, reasoningEfforts: ["low", "high"] } },
  ]);
  assert.deepEqual(narrowed.find((item) => item.name === "myrelay").capabilities.reasoningEfforts, ["low", "high"]);

  // 两侧都没有能力时保持空对象，不得凭空捏造档位。
  const empty = modelProvidersForUIUnion(
    [{ name: "plain", type: "openai-compatible", model: "m1", configured: true }],
    [{ name: "plain", type: "openai-compatible", configured: true, capabilities: {} }],
  );
  assert.deepEqual(empty.find((item) => item.name === "plain").capabilities, {});
});

test("供应商控制台总览合并数据，并在卡片上提供启停与自建供应商删除入口", () => {
  const providers = modelProvidersForUIUnion(
    [
      { name: "openai", type: "openai", enabled: true, origin: "builtin", model: "gpt-4.1-mini", configured: true },
      { name: "offline-gateway", type: "openai-compatible", enabled: false, origin: "custom", baseUrl: "https://gateway.example/v1", model: "offline-1", configured: true },
    ],
    [{ name: "openai", type: "openai", configured: true, models: ["gpt-4.1-mini", "gpt-5"] }],
  );
  assert.equal(providers.length, 2);
  assert.equal(providers.find((provider) => provider.name === "offline-gateway").enabled, false);
  assert.deepEqual(providerConsoleStats(providers), { total: 2, enabled: 1, models: 3, attention: 0 });
  const controller = createModelProviderSettingsController({
    state: {
      settings: { providers: [{ name: "offline-gateway", type: "openai-compatible", enabled: false, origin: "custom", model: "offline-1" }] },
      modelCatalog: { providers: [{ name: "openai", type: "openai", configured: true, models: ["gpt-5"] }] },
    },
  });
  assert.equal(controller.currentProviderConfig("offline-gateway:offline-1").enabled, false);

  const html = renderProviderConsolePage({ providers, consoleState: {} });
  assert.match(html, /mp-provider-page/);
  assert.match(html, /总数/);
  assert.match(html, /已启用/);
  assert.match(html, /可用模型/);
  assert.match(html, /需处理/);
  assert.match(html, /data-mp-provider-card="offline-gateway"/);
  assert.match(html, /mp-provider-card settings-card is-disabled is-custom/);
  assert.match(html, /data-disabled="true"/);
  assert.match(html, /data-mp-category-section="custom"[\s\S]*data-mp-provider-card="offline-gateway"/);
  const builtinCard = html.match(/<article[^>]*data-mp-provider-card="openai"[\s\S]*?<\/article>/)?.[0] || "";
  const customCard = html.match(/<article[^>]*data-mp-provider-card="offline-gateway"[\s\S]*?<\/article>/)?.[0] || "";
  assert.match(builtinCard, /data-mp-provider-toggle="openai"/);
  assert.match(builtinCard, /role="switch" aria-checked="true"/);
  assert.doesNotMatch(builtinCard, /data-mp-delete-provider/);
  assert.match(customCard, /data-mp-provider-toggle="offline-gateway"/);
  assert.match(customCard, /role="switch" aria-checked="false"/);
  assert.match(customCard, /data-mp-provider-open="offline-gateway"/);
  assert.match(customCard, /data-mp-delete-provider="offline-gateway"/);
  assert.match(customCard, /编辑供应商/);
  for (const className of ["settings-page-section", "settings-card", "settings-card-header", "settings-card-content", "settings-toolbar", "settings-stat-grid", "settings-stat-card", "settings-form-field", "settings-inline-actions", "settings-badge"]) {
    assert.match(html, new RegExp(className));
  }
});

test("官方账号平台卡片用单一额度摘要替代模型铺排", () => {
  const providers = modelProvidersForUIUnion([
    { name: "gemini", type: "gemini", enabled: true, origin: "builtin", configured: true, model: "gemini-3-flash" },
    { name: "openai", type: "openai", enabled: true, origin: "builtin", configured: true, model: "gpt-5" },
    { name: "relay", type: "openai-compatible", enabled: true, origin: "custom", configured: true, model: "relay-a" },
  ], [
    { name: "gemini", type: "gemini", configured: true, models: ["gemini-3-flash", "gemini-3.1-pro", "claude-opus"] },
    { name: "openai", type: "openai", configured: true, models: ["gpt-5", "gpt-4.1"] },
    { name: "relay", type: "openai-compatible", configured: true, models: ["relay-a", "relay-b"] },
  ]);
  const html = renderProviderConsolePage({
    providers,
    accountSummaries: {
      gemini: {
        provider: "gemini", state: "ready", loaded: true, total: 2, available: 2,
        percent: 12, tone: "warning", model: "gemini-3.1-pro", accountLabel: "Work",
        resetAt: "2026-07-28T13:00:00Z", resetAfterSeconds: 0, updatedAt: "", hasQuota: true,
      },
    },
    consoleState: {},
  });
  const geminiCard = html.match(/<article[^>]*data-mp-provider-card="gemini"[\s\S]*?<\/article>/)?.[0] || "";
  const openaiCard = html.match(/<article[^>]*data-mp-provider-card="openai"[\s\S]*?<\/article>/)?.[0] || "";
  const relayCard = html.match(/<article[^>]*data-mp-provider-card="relay"[\s\S]*?<\/article>/)?.[0] || "";
  assert.match(geminiCard, /data-mp-provider-quota="gemini"/);
  assert.match(geminiCard, /role="progressbar"[^>]*aria-valuenow="12"/);
  assert.match(geminiCard, /gemini-3\.1-pro/);
  assert.doesNotMatch(geminiCard, /gemini-3-flash|claude-opus|mp-provider-model-preview/);
  assert.match(openaiCard, /mp-provider-model-preview/);
  assert.match(openaiCard, /gpt-5/);
  assert.match(relayCard, /mp-provider-model-preview/);
});

test("已获取模型在 Provider 页面可见并进入全局模型选择器", () => {
  const providers = modelProvidersForUIUnion(
    [],
    [{
      name: "relay",
      type: "openai-compatible",
      baseUrl: "https://relay.example/v1",
      defaultModel: "terra-a",
      models: ["terra-a", "terra-b"],
      modelsSource: "remote",
      discovered: true,
      available: false,
      runtimeAvailable: true,
      registered: true,
      configured: false,
      enabled: true,
      origin: "custom",
    }],
  );
  const html = renderProviderConsolePage({
    providers,
    consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: providers[0] },
  });
  assert.match(html, /mp-provider-create-page mp-provider-create-form/);
  assert.match(html, /mp-provider-edit-page/);
  assert.match(html, /data-mp-model-workspace/);
  assert.match(html, /data-mp-model-config="terra-a"/);
  assert.match(html, /data-mp-model-config="terra-b"/);
  assert.match(html, /type="hidden" name="model" value="terra-a"/);
  assert.match(html, /data-mp-model-token="terra-a"/);
  assert.doesNotMatch(html, /data-mp-provider-card="relay"|mp-provider-drawer-backdrop/);

  const controller = createModelProviderSettingsController({
    state: {
      settings: { providers: [] },
      modelCatalog: { providers },
      agent: { model: "" },
    },
  });
  const options = controller.renderAgentModelOptions("");
  assert.match(options, /relay:terra-a/);
  assert.match(options, /relay:terra-b/);
  assert.match(options, /未配置|unconfigured/i);
});

test("未发现的未配置默认模型不会进入全局模型选择器", () => {
  const controller = createModelProviderSettingsController({
    state: {
      settings: { providers: [] },
      modelCatalog: {
        providers: [{
          name: "relay",
          type: "openai-compatible",
          defaultModel: "fallback-model",
          models: ["fallback-model"],
          modelsSource: "configured-default",
          discovered: false,
          configured: false,
          enabled: true,
        }],
      },
      agent: { model: "" },
    },
  });
  const options = controller.renderAgentModelOptions("");
  assert.doesNotMatch(options, /relay:fallback-model/);
});

test("运行时不可用或未注册的 Provider 只保留当前模型占位，不进入首选与新聊天选择", () => {
  const previousDocument = globalThis.document;
  const modelSelect = { value: "offline:stale-model" };
  globalThis.document = { getElementById(id) { return id === "modelSelect" ? modelSelect : null; } };
  try {
    const controller = createModelProviderSettingsController({
      state: {
        settings: { providers: [] },
        modelCatalog: {
          providers: [
            { name: "offline", type: "openai-compatible", models: ["stale-model"], configured: true, enabled: true, runtimeAvailable: false, registered: false },
            { name: "live", type: "openai", models: ["ready-model"], configured: true, enabled: true, runtimeAvailable: true, registered: true },
          ],
        },
        agent: { id: "agent-1", model: "offline:stale-model" },
      },
      getPreferredModelPreference: () => "offline:stale-model",
    });

    const options = controller.renderAgentModelOptions("offline:stale-model");
    assert.match(options, /value="offline:stale-model" disabled/);
    assert.match(options, /当前不可用|currently unavailable/i);
    assert.doesNotMatch(options, /<optgroup[^>]*>[^]*offline:stale-model/);
    assert.match(options, /live:ready-model/);
    assert.equal(controller.selectedModelValue(), "live:ready-model");
    assert.equal(controller.isCurrentModelConfigured("offline:stale-model"), false);
    assert.equal(controller.isCurrentModelConfigured("live:ready-model"), true);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("模型目录尚未加载时不会读取空 Provider 的运行时状态", () => {
  const previousDocument = globalThis.document;
  const toggles = [];
  const modelSelect = {
    value: "",
    innerHTML: "",
    title: "",
    classList: {
      toggle(name, enabled) {
        toggles.push([name, enabled]);
      },
    },
  };
  globalThis.document = { getElementById(id) { return id === "modelSelect" ? modelSelect : null; } };
  try {
    const controller = createModelProviderSettingsController({
      state: {
        settings: { providers: [] },
        modelCatalog: null,
        agent: { model: "" },
      },
    });

    assert.doesNotThrow(() => controller.renderModelOptions());
    assert.deepEqual(toggles, [["model-unconfigured", true]]);
    assert.equal(modelSelect.title, controller.modelSetupMessage(""));
  } finally {
    globalThis.document = previousDocument;
  }
});

test("供应商控制台仅保留官方与自定义分类，并移除 relay 独立入口", () => {
  const providers = modelProvidersForUIUnion([
    { name: "codex", type: "codex", enabled: true, origin: "builtin", model: "gpt-5.5" },
    { name: "groq", type: "openai-compatible", enabled: true, origin: "custom", baseUrl: "https://api.groq.example/v1", model: "llama" },
    { name: "acme", type: "anthropic", enabled: false, origin: "custom", baseUrl: "https://acme.example", model: "claude" },
  ], []);
  assert.deepEqual(filterConsoleProviders(providers, { category: "official" }).map((provider) => provider.name), ["codex"]);
  assert.deepEqual(filterConsoleProviders(providers, { category: "custom" }).map((provider) => provider.name), ["groq", "acme"]);
  assert.deepEqual(filterConsoleProviders(providers, { category: "compatible" }).map((provider) => provider.name), []);
  assert.deepEqual(filterConsoleProviders(providers, { search: "acme" }).map((provider) => provider.name), ["acme"]);
  assert.deepEqual(filterConsoleProviders(providers, { search: "groq.example" }).map((provider) => provider.name), ["groq"]);

  const html = renderProviderConsolePage({ providers, consoleState: { category: "compatible" } });
  assert.match(html, /mp-category-tabs/);
  assert.match(html, /role="group"/);
  assert.match(html, /data-mp-category="all"/);
  assert.match(html, /data-mp-category="official"/);
  assert.match(html, /data-mp-category="custom"/);
  assert.doesNotMatch(html, /data-mp-category="compatible"|compatibleAdvanced|兼容与高级|data-mp-open-relay/);
  assert.doesNotMatch(html, /role="tab"|aria-controls="mp-provider-panel-/);
  assert.match(html, /mp-provider-section/);
});

test("供应商控制台移除类型选择弹窗并让普通编辑使用全页配置", () => {
  const providers = modelProvidersForUIUnion([{ name: "groq", type: "openai-compatible", origin: "custom", enabled: true, model: "llama", models: ["llama"], baseUrl: "https://api.groq.example/v1" }], []);
  const list = renderProviderConsolePage({ providers, consoleState: { modal: "types" } });
  assert.match(list, /data-mp-open-types/);
  assert.doesNotMatch(list, /mp-provider-type-modal|mp-provider-type-grid|data-mp-select-type/);

  const editPage = renderProviderConsolePage({
    providers,
    consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: providers[0] },
  });
  assert.match(editPage, /mp-provider-page mp-provider-create-page mp-provider-create-form/);
  assert.match(editPage, /mp-provider-edit-page/);
  assert.match(editPage, /编辑供应商/);
  assert.match(editPage, /name="name"[^>]*value="groq"[^>]*required/);
  assert.doesNotMatch(editPage, /name="name"[^>]*readonly/);
  assert.match(editPage, /name="baseUrl"[^>]*type="url"/);
  assert.doesNotMatch(editPage, /name="baseUrl"[^>]*readonly/);
  assert.match(editPage, /data-mp-fetch-models/);
  assert.match(editPage, /data-mp-model-config="llama"/);
  assert.match(editPage, /type="hidden" name="model" value="llama"/);
  assert.match(editPage, /data-mp-test-provider/);
  assert.match(editPage, /发送测试/);
  assert.match(editPage, /data-mp-save-provider/);
  assert.match(editPage, /data-mp-delete-provider="groq"/);
  assert.match(editPage, /aria-describedby="mp-provider-create-description"/);
  assert.match(editPage, /aria-busy="false"/);
  assert.match(editPage, /留空会保留已保存的 Key/);
  assert.doesNotMatch(editPage, /mp-provider-drawer-backdrop|mp-provider-type-modal|data-mp-provider-card="groq"/);

  const testPage = renderProviderConsolePage({
    providers,
    consoleState: {
      drawer: "provider",
      mode: "edit",
      type: "openai-compatible",
      draft: providers[0],
      testOpen: true,
      test: { prompt: "reply", result: { success: true, tone: "success", output: "ok", message: "测试成功" } },
    },
  });
  assert.match(testPage, /mp-provider-test-dialog/);
  assert.match(testPage, /data-mp-test-prompt/);
  assert.match(testPage, /data-mp-send-test/);
  assert.match(testPage, /<pre>ok<\/pre>/);
});

test("新增供应商入口默认直接建立 OpenAI Responses 全页草稿", () => {
  const state = { settings: { providers: [] }, modelCatalog: { providers: [] } };
  let refreshes = 0;
  const controller = createModelProviderSettingsController({
    state,
    refreshActiveSettingsPanel() { refreshes += 1; },
  });
  controller.openProviderConsoleType();
  assert.equal(state.providerConsole.modal, "");
  assert.equal(state.providerConsole.drawer, "provider");
  assert.equal(state.providerConsole.mode, "create");
  assert.equal(state.providerConsole.type, "openai");
  assert.equal(state.providerConsole.providerName, "openai");
  assert.equal(state.providerConsole.draft.name, "openai");
  assert.equal(state.providerConsole.draft.baseUrl, "");
  assert.equal(state.providerConsole.draft.model, "");
  assert.deepEqual(state.providerConsole.draft.modelConfigs, []);
  assert.equal(state.providerConsole.draft.modelsReady, false);
  assert.equal(refreshes, 1);
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /mp-provider-create-page/);
  assert.match(html, /<h1[^>]*>OpenAI<\/h1><span class="mp-provider-reference-type">OpenAI \/ Responses<\/span>/);
  assert.match(html, /name="name" value=""[^>]*placeholder="openai"/);
  assert.match(html, /name="baseUrl"[^>]*value=""[^>]*placeholder="https:\/\/api\.openai\.com\/v1"/);
  assert.match(html, /name="type" value="openai" checked/);
  assert.doesNotMatch(html, /mp-provider-type-modal|data-mp-select-type/);
});

test("供应商控制台重绘保持状态对象身份，异步模型发现结果不会丢失", () => {
  const retainedTestState = { prompt: "", result: null };
  const state = {
    settings: { providers: [] },
    modelCatalog: { providers: [] },
    providerConsole: {
      drawer: "provider",
      mode: "create",
      type: "openai-compatible",
      providerName: "zzz",
      dirty: true,
      busy: {},
      test: retainedTestState,
      draft: {
        ...createProviderDraft("openai-compatible"),
        name: "zzz",
        baseUrl: "https://relay.example/v1",
        model: "your-model",
        apiKeyOptional: true,
      },
    },
  };
  const retainedConsoleState = state.providerConsole;
  const controller = createModelProviderSettingsController({ state });

  controller.renderProviderSettingsContent();
  assert.equal(state.providerConsole, retainedConsoleState);
  assert.equal(state.providerConsole.test, retainedTestState);

  const discoveredModels = ["codex-auto-review", "model-b", "model-c", "model-d", "model-e", "model-f", "model-g"];
  retainedConsoleState.draft = {
    ...retainedConsoleState.draft,
    models: discoveredModels,
    model: discoveredModels[0],
  };
  const html = controller.renderProviderSettingsContent();

  assert.equal(state.providerConsole, retainedConsoleState);
  assert.match(html, /type="hidden" name="model" value="codex-auto-review"/);
  assert.match(html, /data-mp-model-config="codex-auto-review"/);
  assert.match(html, /data-mp-model-config="model-g"/);
  assert.doesNotMatch(html, /data-mp-model-example|name="maxTokens"|mp-provider-reference-final/);
});

test("新增普通供应商使用独立全宽扁平配置页", () => {
  const providers = modelProvidersForUIUnion([{ name: "existing", type: "openai-compatible", origin: "custom", enabled: true, model: "old-model", baseUrl: "https://old.example/v1" }], []);
  const html = renderProviderConsolePage({
    providers,
    consoleState: {
      drawer: "provider",
      mode: "create",
      type: "openai-compatible",
      draft: {
        name: "new-gateway",
        type: "openai-compatible",
        apiKey: "sk-local-draft",
        baseUrl: "https://new.example/v1",
        model: "model-a",
        models: ["model-a", "model-b"],
        maxTokens: 4096,
        apiKeyOptional: false,
      },
    },
  });
  assert.match(html, /mp-provider-page mp-provider-create-page mp-provider-create-form/);
  assert.match(html, /data-mp-provider-form/);
  assert.match(html, /data-mp-close-drawer/);
  assert.match(html, /返回供应商列表/);
  assert.match(html, /连接与凭据/);
  assert.match(html, /mp-provider-create-protocol-options/);
  assert.match(html, /type="radio" name="type" value="openai-compatible" checked/);
  assert.match(html, /name="apiKeyOptional" type="checkbox"/);
  assert.match(html, /data-mp-test-provider/);
  assert.match(html, /data-mp-fetch-models/);
  assert.match(html, /data-mp-save-provider/);
  assert.match(html, /放弃更改/);
  assert.match(html, /data-mp-model-config="model-a"/);
  assert.match(html, /data-mp-model-config="model-b"/);
  assert.match(html, /data-mp-model-token="model-b"/);
  assert.match(html, /type="hidden" name="model" value="model-a"/);
  assert.match(html, /mp-provider-reference-layout/);
  assert.match(html, /data-mp-toggle-api-key/);
  assert.match(html, /mp-provider-header-add-bar/);
  assert.doesNotMatch(html, /name="maxTokens"|data-mp-model-example|mp-provider-reference-final/);
  const referenceOrder = [
    'name="name"',
    "data-mp-provider-prefix-preview",
    'name="apiKey"',
    'name="baseUrl"',
    'name="proxyUrl"',
    'name="userAgent"',
    "data-mp-add-request-header",
    "mp-provider-reference-protocol",
    "data-mp-model-workspace",
    "data-mp-test-provider",
  ].map((marker) => html.indexOf(marker));
  assert.ok(referenceOrder.every((index) => index >= 0));
  assert.deepEqual(referenceOrder, [...referenceOrder].sort((a, b) => a - b));
  assert.doesNotMatch(html, /data-mp-provider-card="existing"/);
  assert.doesNotMatch(html, /mp-provider-steps|mp-provider-create-section-linear|mp-provider-drawer-backdrop|mp-provider-type-modal/);
});

test("Provider API Key 元数据安全规范化、掩码渲染和草稿隔离", () => {
  const input = {
    name: "safe-provider",
    type: "openai-compatible",
    apiKey: "sk-full-secret-must-not-escape",
    apiKeyConfigured: true,
    apiKeyPersisted: true,
    apiKeyLastFive: `abc<&\"`,
    apiKeySource: "stored",
    model: "model-a",
  };
  const normalized = normalizeConsoleProvider(input);
  assert.equal(Object.hasOwn(normalized, "apiKey"), false);
  assert.deepEqual({
    apiKeyConfigured: normalized.apiKeyConfigured,
    apiKeyPersisted: normalized.apiKeyPersisted,
    apiKeyLastFive: normalized.apiKeyLastFive,
    apiKeySource: normalized.apiKeySource,
  }, { apiKeyConfigured: true, apiKeyPersisted: true, apiKeyLastFive: "bc<&\"", apiKeySource: "stored" });
  const union = modelProvidersForUIUnion([input], [{ ...input, models: ["model-a"] }]);
  assert.equal(Object.hasOwn(union[0], "apiKey"), false);
  assert.deepEqual({ apiKeyConfigured: union[0].apiKeyConfigured, apiKeyPersisted: union[0].apiKeyPersisted, apiKeyLastFive: union[0].apiKeyLastFive, apiKeySource: union[0].apiKeySource }, { apiKeyConfigured: true, apiKeyPersisted: true, apiKeyLastFive: "bc<&\"", apiKeySource: "stored" });
  const draft = createProviderDraft("openai-compatible", { ...union[0], apiKey: input.apiKey });
  assert.equal(draft.apiKey, "", "saved provider metadata never seeds a complete API key");
  assert.deepEqual({ apiKeyConfigured: draft.apiKeyConfigured, apiKeyPersisted: draft.apiKeyPersisted, apiKeyLastFive: draft.apiKeyLastFive, apiKeySource: draft.apiKeySource }, { apiKeyConfigured: true, apiKeyPersisted: true, apiKeyLastFive: "bc<&\"", apiKeySource: "stored" });

  const html = renderProviderConsolePage({
    providers: union,
    consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: { ...union[0], apiKeyDraft: true, apiKey: input.apiKey } },
  });
  assert.match(html, /已加密保存，末 5 位：bc&lt;&amp;&quot;/);
  assert.match(html, /name="apiKey" type="password" value="sk-full-secret-must-not-escape"/);
  assert.match(html, /name="clearApiKey" type="checkbox"/);

  const payload = providerConfigPayload({ ...union[0], apiKey: "", model: "model-a" });
  assert.equal(payload.apiKey, "");
  assert.equal(Object.hasOwn(payload, "clearApiKey"), false);
  const clearPayload = providerConfigPayload({ ...union[0], apiKey: "sk-should-never-coexist", clearApiKey: true });
  assert.deepEqual({ apiKey: clearPayload.apiKey, clearApiKey: clearPayload.clearApiKey }, { apiKey: "", clearApiKey: true });
});

test("供应商控制台配置 payload 保留空 API Key 并规范请求与网络字段", () => {
  const draft = createProviderDraft("openai-compatible");
  const payload = providerConfigPayload({
    ...draft,
    name: "acme-gateway",
    baseUrl: " https://api.acme.example/v1 ",
    apiKey: "",
    model: " model-a ",
    modelConfigs: [{ name: " model-a ", contextTokenLimit: "128000", hidden: true }],
    maxTokens: "4096.8",
    apiKeyOptional: true,
    proxyUrl: " http://proxy-user:proxy-pass@127.0.0.1:7890 ",
    userAgent: " Acme Client/1.0 ",
    requestHeaders: [
      { name: " X-Tenant ", value: "tenant-secret" },
      { name: "Authorization", value: "", keepExisting: true },
      { name: "", value: "" },
    ],
    insecureSkipTLSVerify: true,
  });
  assert.deepEqual(payload, {
    name: "acme-gateway",
    type: "openai-compatible",
    profile: "",
    baseUrl: "https://api.acme.example/v1",
    apiKey: "",
    model: "model-a",
    models: [{ name: "model-a", contextTokenLimit: 128000, imageGeneration: false }],
    maxTokens: 4096,
    apiKeyOptional: true,
    proxyUrl: "http://proxy-user:proxy-pass@127.0.0.1:7890",
    userAgent: "Acme Client/1.0",
    requestHeaders: [
      { name: "X-Tenant", value: "tenant-secret", keepExisting: false },
      { name: "Authorization", value: "", keepExisting: true },
    ],
    insecureSkipTLSVerify: true,
    allowPlaintextHTTP: false,
  });
});

test("Provider 名称即时校验覆盖必填、格式、长度和创建冲突", () => {
  assert.equal(validateProviderNameValue("").code, "required");
  assert.equal(validateProviderNameValue("-bad").code, "invalid");
  assert.equal(validateProviderNameValue("a".repeat(65)).code, "too_long");
  assert.equal(validateProviderNameValue("relay", { existingNames: ["relay"], mode: "create" }).code, "conflict");
  assert.equal(validateProviderNameValue("relay", { existingNames: ["relay"], mode: "edit", originalName: "relay" }).valid, true);
});

test("Base URL 自动提取主域名作为新增 Provider 名称且不覆盖手动名称", () => {
  assert.equal(providerNameFromBaseURL("https://qionggeme.com/dashboard/v1"), "qionggeme");
  assert.equal(providerNameFromBaseURL("https://api.futureapi.com/v1"), "futureapi");
  assert.equal(providerNameFromBaseURL("https://api.example.co.uk/v1"), "example");
  assert.equal(providerNameFromBaseURL("http://127.0.0.1:11434/v1"), "");
  assert.equal(providerNameFromBaseURL("not-a-url"), "");

  assert.deepEqual(automaticProviderNameUpdate("https://qionggeme.com/dashboard/v1"), {
    name: "qionggeme",
    suggestion: "qionggeme",
    changed: true,
  });
  assert.deepEqual(automaticProviderNameUpdate("https://futureapi.com/v1", "qionggeme", "qionggeme"), {
    name: "futureapi",
    suggestion: "futureapi",
    changed: true,
  });
  assert.deepEqual(automaticProviderNameUpdate("https://futureapi.com/v1", "manual-name", "qionggeme"), {
    name: "manual-name",
    suggestion: "",
    changed: false,
  });
  assert.deepEqual(automaticProviderNameUpdate("https://futureapi.com/v1", "saved-name", "", "edit"), {
    name: "saved-name",
    suggestion: "",
    changed: false,
  });
});

test("Provider 草稿请求携带创建保护或原始名称", () => {
  const createPayload = providerConfigPayload({ name: "new-relay", type: "openai-compatible", baseUrl: "https://example.test/v1", model: "m", createOnly: true });
  assert.equal(createPayload.createOnly, true);
  const editPayload = providerTestPayload({ name: "renamed", type: "openai-compatible", baseUrl: "https://example.test/v1", model: "m", originalName: "relay" });
  assert.equal(editPayload.originalName, "relay");
});

test("供应商控制台 toggle、草稿预检、delete 与 config 请求遵守后端契约", () => {
  const provider = { name: "custom/name space", defaultModel: "model-a", enabled: true, configured: true, origin: "custom" };
  const toggle = providerConsoleRequest("toggle", provider, { enabled: true });
  assert.equal(toggle.path, "/api/providers/custom%2Fname%20space");
  assert.equal(toggle.options.method, "PATCH");
  assert.deepEqual(JSON.parse(toggle.options.body), { enabled: true, model: "model-a" });
  const toggleWithoutModel = providerConsoleRequest("toggle", { name: "empty-provider" }, { enabled: false });
  assert.deepEqual(JSON.parse(toggleWithoutModel.options.body), { enabled: false });

  const draft = {
    name: provider.name,
    type: "openai-compatible",
    profile: "cliproxyapi",
    baseUrl: " https://example.test/v1 ",
    apiKey: "",
    model: " model-a ",
    modelConfigs: [{ name: "model-a", contextTokenLimit: 32000, hidden: true }],
    maxTokens: "4096.2",
    apiKeyOptional: true,
    proxyUrl: "http://proxy-user:proxy-pass@127.0.0.1:7890",
    userAgent: "Autoto Test Client",
    requestHeaders: [{ name: "X-Test", value: "request-secret" }],
    insecureSkipTLSVerify: true,
    enabled: false,
    origin: "builtin",
    configured: false,
    error: "never send this",
  };
  assert.deepEqual(providerTestPayload(draft), {
    name: provider.name,
    type: "openai-compatible",
    profile: "cliproxyapi",
    baseUrl: "https://example.test/v1",
    apiKey: "",
    model: "model-a",
    models: [{ name: "model-a", contextTokenLimit: 32000, imageGeneration: false }],
    maxTokens: 4096,
    apiKeyOptional: true,
    proxyUrl: "http://proxy-user:proxy-pass@127.0.0.1:7890",
    userAgent: "Autoto Test Client",
    requestHeaders: [{ name: "X-Test", value: "request-secret", keepExisting: false }],
    insecureSkipTLSVerify: true,
    allowPlaintextHTTP: false,
  });
  const testConnection = providerConsoleRequest("test", provider, draft);
  assert.equal(testConnection.path, "/api/providers/test");
  assert.equal(testConnection.options.method, "POST");
  assert.deepEqual(JSON.parse(testConnection.options.body), providerTestPayload(draft));
  assert.equal(JSON.parse(testConnection.options.body).apiKey, "", "empty key keeps existing-key semantics");
  assert.equal(Object.hasOwn(JSON.parse(testConnection.options.body), "origin"), false);
  assert.equal(Object.hasOwn(JSON.parse(testConnection.options.body), "enabled"), false);

  const messagePayload = providerMessageTestPayload(draft, provider, "reply with ok");
  assert.equal(messagePayload.prompt, "reply with ok");
  assert.equal(Object.hasOwn(messagePayload, "origin"), false);
  const messageTest = providerConsoleRequest("message-test", provider, { ...draft, prompt: "reply with ok" });
  assert.equal(messageTest.path, "/api/providers/test-message");
  assert.equal(messageTest.options.method, "POST");
  assert.deepEqual(JSON.parse(messageTest.options.body), messagePayload);

  const remove = providerConsoleRequest("delete", provider);
  assert.deepEqual(remove, { path: "/api/providers/custom%2Fname%20space", options: { method: "DELETE" } });

  const config = providerConsoleRequest("config", provider, { name: provider.name, type: "openai-compatible", baseUrl: "https://example.test/v1", apiKey: "", model: "model-a" });
  assert.equal(config.path, "/api/providers/custom%2Fname%20space/config");
  assert.equal(config.options.method, "PUT");
  assert.equal(JSON.parse(config.options.body).apiKey, "");

  const renameConfig = providerConsoleRequest("config", { name: "old-name" }, { name: "new-name", pathName: "old-name", type: "openai-compatible", baseUrl: "https://example.test/v1", model: "model-a" });
  assert.equal(renameConfig.path, "/api/providers/old-name/config");
  assert.equal(JSON.parse(renameConfig.options.body).name, "new-name");
  const renameToggle = providerConsoleRequest("toggle", { name: "new-name" }, { name: "new-name", enabled: true, model: "model-a" });
  assert.equal(renameToggle.path, "/api/providers/new-name");
});

test("供应商表单草稿实时同步并在后台重绘时保持 dirty 内容", () => {
  const headerRow = {
    dataset: { originalName: "X-Saved", keepExisting: "true", configured: "true" },
    querySelector(selector) {
      if (selector === "[data-mp-request-header-name]") return { value: "X-Saved" };
      if (selector === "[data-mp-request-header-value]") return { value: "" };
      return null;
    },
  };
  const form = {
    elements: {
      name: { value: "acme" },
      type: { value: "openai-compatible" },
      baseUrl: { value: "https://api.acme.example/v1" },
      apiKey: { value: "draft-key" },
      proxyUrl: { value: "http://proxy-user:proxy-pass@127.0.0.1:7890" },
      clearProxyAuth: { checked: false },
      userAgent: { value: "Acme Client" },
      insecureSkipTLSVerify: { checked: true },
      model: { value: "acme-model" },
      maxTokens: { value: "8192" },
      apiKeyOptional: { checked: true },
    },
    querySelectorAll(selector) {
      return selector === "[data-mp-request-header-row]" ? [headerRow] : [];
    },
  };
  const consoleState = { type: "openai", draft: { origin: "custom", enabled: false }, dirty: false };
  assert.deepEqual(providerConsoleDraftFromForm(consoleState.draft, form, consoleState.type), {
    origin: "custom",
    enabled: false,
    name: "acme",
    type: "openai-compatible",
    profile: "",
    baseUrl: "https://api.acme.example/v1",
    apiKey: "draft-key",
    apiKeyDraft: true,
    clearApiKey: false,
    proxyUrl: "http://proxy-user:proxy-pass@127.0.0.1:7890",
    proxyUrlDraft: true,
    clearProxyAuth: false,
    userAgent: "Acme Client",
    userAgentDraft: true,
    requestHeaders: [{ name: "X-Saved", value: "", keepExisting: true, configured: true }],
    requestHeadersDraft: true,
    insecureSkipTLSVerify: true,
    allowPlaintextHTTP: false,
    model: "acme-model",
    maxTokens: 8192,
    apiKeyOptional: true,
  });
  syncProviderConsoleDraft(consoleState, form);
  assert.equal(consoleState.dirty, true);
  assert.equal(consoleState.draft.apiKey, "draft-key");
  assert.equal(consoleState.draft.apiKeyOptional, true);

  const state = {
    settings: { providers: [{ name: "acme", type: "openai-compatible", origin: "custom", model: "saved-model", baseUrl: "https://old.example/v1" }] },
    modelCatalog: { providers: [{ name: "acme", type: "openai-compatible", models: ["saved-model"] }] },
    providerConsole: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: { ...consoleState.draft }, dirty: true },
  };
  const controller = createModelProviderSettingsController({ state });
  const firstRender = controller.renderProviderSettingsContent();
  state.settings.providers[0].model = "refreshed-model";
  state.modelCatalog.providers[0].models = ["refreshed-model"];
  const refreshedRender = controller.renderProviderSettingsContent();
  assert.match(firstRender, /value="acme-model"/);
  assert.match(refreshedRender, /value="acme-model"/);
  assert.match(refreshedRender, /name="apiKey" type="password" value="draft-key"/);
  assert.equal(state.providerConsole.dirty, true);
  assert.equal(state.providerConsole.draft.apiKey, "draft-key");
});

test("页面移除 Max tokens 输入后仍保留草稿中的现有值", () => {
  const form = {
    elements: {
      name: { value: "relay" }, type: { value: "openai-compatible" }, baseUrl: { value: "https://relay.example/v1" },
      apiKey: { value: "" }, model: { value: "a" }, apiKeyOptional: { checked: false }, insecureSkipTLSVerify: { checked: false },
    },
    querySelectorAll() { return []; },
  };
  const draft = providerConsoleDraftFromForm({
    name: "relay", type: "openai-compatible", baseUrl: "https://relay.example/v1", model: "a", maxTokens: 4096,
  }, form);
  assert.equal(draft.maxTokens, 4096);
});

test("安全重绘后的空 API Key 输入不会清除仅存在于内存的草稿 secret", () => {
  const form = {
    elements: {
      name: { value: "relay" }, type: { value: "openai-compatible" }, baseUrl: { value: "https://relay.example/v1" },
      apiKey: { value: "" }, model: { value: "a" }, maxTokens: { value: "0" }, apiKeyOptional: { checked: false },
    },
    querySelectorAll() { return []; },
  };
  const draft = providerConsoleDraftFromForm({
    name: "relay", type: "openai-compatible", apiKey: "draft-secret", apiKeyDraft: true,
    model: "a", modelConfigs: [{ name: "a" }], modelsReady: true,
  }, form);
  assert.equal(draft.apiKey, "draft-secret");
});

test("模型选择器更新 draft 后会在重绘中保持内部默认模型", () => {
  const form = {
    elements: {
      name: { value: "relay" },
      type: { value: "openai-compatible" },
      baseUrl: { value: "https://relay.example/v1" },
      apiKey: { value: "" },
      model: { value: "model-b" },
      maxTokens: { value: "0" },
      apiKeyOptional: { checked: true },
    },
  };
  const consoleState = { type: "openai-compatible", draft: { name: "relay", models: ["model-a", "model-b"], model: "model-a" } };
  syncProviderConsoleDraft(consoleState, form);
  assert.equal(consoleState.draft.model, "model-b");
  const html = renderProviderConsolePage({
    providers: [{ name: "relay", type: "openai-compatible", model: "model-b", models: ["model-a", "model-b"], baseUrl: "https://relay.example/v1", configured: true, enabled: true, origin: "custom" }],
    consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: consoleState.draft },
  });
  assert.match(html, /type="hidden" name="model" value="model-b"/);
  assert.doesNotMatch(html, /data-mp-model-example|mp-provider-reference-final/);
});

test("模型可见性偏好仍影响模型选择器但设置页不再显示可见性区块", () => {
  const previousStorage = globalThis.localStorage;
  delete globalThis.localStorage;
  try {
    const controller = createModelProviderSettingsController({
      state: {
        settings: { providers: [{ name: "openai", type: "openai", model: "gpt-5", configured: true, enabled: true }] },
        modelCatalog: { providers: [{ name: "openai", type: "openai", configured: true, enabled: true, models: ["gpt-5", "gpt-4.1-mini"] }] },
        agent: { model: "openai:gpt-5" },
      },
      getModelVisibilityPreference: () => ({ hiddenModels: { "openai:gpt-4.1-mini": true }, showUnconfiguredProviders: false }),
    });
    const html = controller.renderModelSettingsContent();
    assert.doesNotMatch(html, /data-toggle-model-visibility|data-model-visibility-state|agent-model-catalog-details|显示这个模型/);
    assert.doesNotMatch(controller.renderAgentModelOptions(""), /openai:gpt-4\.1-mini/);
    assert.match(controller.renderAgentModelOptions(""), /openai:gpt-5/);
  } finally {
    if (previousStorage === undefined) delete globalThis.localStorage;
    else globalThis.localStorage = previousStorage;
  }
});

test("首选模型通过注入 getter 和乐观 setter 且不依赖全局 localStorage", () => {
  const previousStorage = globalThis.localStorage;
  delete globalThis.localStorage;
  const saved = [];
  try {
    const controller = createModelProviderSettingsController({
      state: {},
      getPreferredModelPreference: () => saved.at(-1) || "openai:gpt-server",
      setPreferredModelPreference: (value) => saved.push(value),
    });
    assert.equal(controller.getPreferredModel(), "openai:gpt-server");
    assert.equal(controller.setPreferredModel("anthropic:claude"), "anthropic:claude");
    assert.equal(saved.at(-1), "anthropic:claude");
    assert.equal(controller.getPreferredModel(), "anthropic:claude");
  } finally {
    if (previousStorage === undefined) delete globalThis.localStorage;
    else globalThis.localStorage = previousStorage;
  }
});

test("Agent 模型设置规范化角色路由并生成后端 payload", () => {
  assert.deepEqual(agentModelRoles, ["explore", "plan", "general", "search"]);
  assert.equal(isAgentModelReference("codex:gpt-5.5"), true);
  assert.equal(isAgentModelReference("missing-colon"), false);
  assert.equal(isAgentModelReference("openai:"), false);
  const normalized = normalizeAgentModelSettings({
    defaultModel: " codex:gpt-5.5 ",
    summaryModel: "",
    defaultReasoningEffort: "HIGH",
    subagentModels: { explore: " openai:gpt-4.1-mini ", unknown: "ignored:model" },
    subagentModelPools: { explore: ["codex:gpt-5.5", "codex:gpt-5.5", " openai:gpt-4.1-mini "], unknown: ["ignored:model"] },
  });
  assert.deepEqual(normalized, {
    defaultModel: "codex:gpt-5.5",
    summaryModel: "codex:gpt-5.5",
    // Blank means "follow the active conversation model". Unlike summaryModel it
    // must NOT inherit defaultModel, because the reviewer is selected per agent.
    safetyModel: "",
    defaultReasoningEffort: "high",
    subagentModels: { explore: "openai:gpt-4.1-mini" },
    subagentModelPools: { explore: ["codex:gpt-5.5", "openai:gpt-4.1-mini"] },
  });
  assert.deepEqual(agentModelSettingsPayload({
    ...normalized,
    subagentModels: { ...normalized.subagentModels, plan: "anthropic:claude-sonnet" },
    subagentModelPools: { ...normalized.subagentModelPools, plan: ["codex:gpt-5.5"] },
  }), {
    defaultModel: "codex:gpt-5.5",
    summaryModel: "codex:gpt-5.5",
    safetyModel: "",
    subagentModels: { explore: "openai:gpt-4.1-mini", plan: "anthropic:claude-sonnet" },
    subagentModelPools: {
      explore: ["codex:gpt-5.5", "openai:gpt-4.1-mini"],
      plan: ["anthropic:claude-sonnet", "codex:gpt-5.5"],
    },
  });
});

test("安全审查模型可独立设定且留空时沿用摘要模型", () => {
  const inherited = normalizeAgentModelSettings({ defaultModel: "codex:gpt-5.5", summaryModel: "openai:gpt-4.1-mini" });
  assert.equal(inherited.safetyModel, "");
  assert.equal(agentModelSettingsPayload(inherited).safetyModel, "");

  const overridden = normalizeAgentModelSettings({
    defaultModel: "codex:gpt-5.5",
    summaryModel: "openai:gpt-4.1-mini",
    safetyModel: " anthropic:claude-opus-5 ",
  });
  assert.equal(overridden.safetyModel, "anthropic:claude-opus-5");
  assert.equal(agentModelSettingsPayload(overridden).safetyModel, "anthropic:claude-opus-5");
});

test("模型聚合与默认推理强度请求遵循后端契约", () => {
  assert.deepEqual(modelAggregateMembers(" openai:gpt-5\n\ncodex:gpt-5.5 \n"), ["openai:gpt-5", "codex:gpt-5.5"]);
  assert.deepEqual(normalizeModelAggregateList({ aggregates: [{ name: "primary", mode: "priority", members: ["openai:gpt-5"], revision: 3 }] }), [{
    id: "",
    name: "primary",
    mode: "priority",
    members: ["openai:gpt-5"],
    revision: 3,
    updatedAt: "",
  }]);
  const save = modelAggregateActionRequest("save", {}, { name: "primary/fallback", members: "openai:gpt-5\ncodex:gpt-5.5", revision: 0 });
  assert.equal(save.path, "/api/model-aggregates/primary%2Ffallback");
  assert.equal(save.options.method, "PUT");
  assert.deepEqual(JSON.parse(save.options.body), { mode: "priority", members: ["openai:gpt-5", "codex:gpt-5.5"], revision: 0 });
  const remove = modelAggregateActionRequest("delete", { name: "primary", revision: 4 });
  assert.deepEqual(JSON.parse(remove.options.body), { revision: 4 });
  const reasoning = runtimeReasoningSettingsRequest("HIGH", { revision: 7 });
  assert.equal(reasoning.path, "/api/runtime/model-settings");
  assert.equal(reasoning.options.method, "PATCH");
  assert.deepEqual(JSON.parse(reasoning.options.body), { defaultReasoningEffort: "high", revision: 7 });
});

test("模型设置页面仅保留扁平路由表单并移除聚合管理与模型可见性区块", () => {
  const controller = createModelProviderSettingsController({
    state: {
      settings: {
        agent: {
          defaultModel: "openai:gpt-5",
          summaryModel: "openai:gpt-4.1-mini",
          subagentModels: { explore: "openai:gpt-4.1-mini" },
          subagentModelPools: { explore: ["openai:gpt-4.1-mini", "openai:gpt-5"] },
        },
        runtimeSettings: { defaultReasoningEffort: "high", revision: 3 },
        providers: [{ name: "openai", type: "openai", origin: "builtin", enabled: true, configured: true, model: "gpt-5" }],
      },
      modelCatalog: { providers: [{ name: "openai", type: "openai", configured: true, models: ["gpt-5", "gpt-4.1-mini"] }] },
      modelAggregatesLoaded: true,
      modelAggregates: [{ name: "primary", mode: "priority", members: ["openai:gpt-5", "openai:gpt-4.1-mini"], revision: 2 }],
      agent: { model: "openai:gpt-5" },
    },
  });
  const html = controller.renderModelSettingsContent();
  for (const className of ["compact-settings-page", "compact-settings-header", "compact-settings-section", "compact-settings-form", "compact-settings-grid", "compact-settings-footer", "settings-inline-actions", "settings-badge"]) {
    assert.match(html, new RegExp(className));
  }
  assert.doesNotMatch(html, /settings-stat-grid|settings-stat-card/);
  assert.match(html, /id="settingsRefreshModelsBtn"/);
  assert.match(html, /id="settingsOpenLoginBtn"[^>]*>供应商设置</);
  assert.doesNotMatch(html, /凭证\s*\/\s*中转站/);
  assert.match(html, /id="agentModelSettingsForm"/);
  assert.match(html, /name="defaultModel" required/);
  assert.match(html, /name="summaryModel" required/);
  assert.match(html, /name="defaultReasoningEffort"[^>]*>[\s\S]*?<option value="high" selected>/);
  // The runtime supports Codex-specific levels too; they must use translated
  // labels instead of leaking the modelProvider.* key into the native select.
  assert.match(html, /<option value="xhigh"[^>]*>超高<\/option>/);
  assert.match(html, /<option value="max"[^>]*>极高<\/option>/);
  assert.match(html, /<option value="ultra"[^>]*>极限<\/option>/);
  assert.doesNotMatch(html, />modelProvider\.(?:xhigh|max|ultra)<\/option>/);
  assert.match(html, /name="subagentModel_explore"/);
  assert.match(html, /<details class="compact-multi-select agent-model-pool-details" data-agent-model-pool-details="explore">/);
  assert.match(html, /data-agent-model-pool-summary="explore"/);
  assert.match(html, /data-agent-model-pool-option="explore"/);
  assert.match(html, /<option value="aggregate:primary"/);
  assert.match(html, /aggregate:primary/);
  assert.match(html, /id="saveAgentModelSettingsBtn"/);
  assert.doesNotMatch(html, /data-model-aggregate-add|model-aggregate-section|model-aggregate-title/);
  assert.doesNotMatch(html, /agent-model-catalog-details|agent-model-catalog-provider|agent-model-catalog-item|data-apply-model|data-toggle-model-visibility/);
  assert.doesNotMatch(html, /settingsShowConfiguredModelsBtn|settingsClearPreferredModelBtn|当前对话与模型可见性|已加载 2 个模型/);
});

test("供应商卡片键盘操作排除交互元素", () => {
  const card = { contains: (node) => node === button || node === input || node === select || node === switchControl };
  const button = { closest: () => button };
  const input = { closest: () => input };
  const select = { closest: () => select };
  const switchControl = { closest: () => switchControl };
  const plain = { closest: () => null };
  assert.equal(isProviderConsoleInteractiveTarget(button, card), true);
  assert.equal(isProviderConsoleInteractiveTarget(input, card), true);
  assert.equal(isProviderConsoleInteractiveTarget(select, card), true);
  assert.equal(isProviderConsoleInteractiveTarget(switchControl, card), true);
  assert.equal(shouldOpenProviderCardFromKeyboard({ key: "Enter", target: button }, card), false);
  assert.equal(shouldOpenProviderCardFromKeyboard({ key: " ", target: input }, card), false);
  assert.equal(shouldOpenProviderCardFromKeyboard({ key: "Enter", target: plain }, card), true);
  assert.equal(shouldOpenProviderCardFromKeyboard({ key: "Escape", target: plain }, card), false);
});

test("供应商配置的通用聚焦辅助仅对显式标记字段生效", () => {
  const field = {
    value: "https://api.example.com/v1",
    type: "url",
    disabled: false,
    readOnly: false,
    dataset: { selectOnFocus: "true" },
    selected: 0,
    removeAttribute(name) {
      if (name === "data-select-on-focus") delete this.dataset.selectOnFocus;
    },
    select() { this.selected += 1; },
  };
  assert.equal(selectProviderConsoleFieldOnFocus(field), true);
  assert.equal(field.selected, 1);
  assert.equal(field.dataset.selectOnFocus, undefined);
  assert.equal(selectProviderConsoleFieldOnFocus(field), false);
  assert.equal(field.selected, 1);

  const empty = {
    value: "",
    type: "text",
    disabled: false,
    readOnly: false,
    dataset: { selectOnFocus: "true" },
    removeAttribute(name) {
      if (name === "data-select-on-focus") delete this.dataset.selectOnFocus;
    },
    select() { throw new Error("empty fields should not be selected"); },
  };
  assert.equal(selectProviderConsoleFieldOnFocus(empty), false);
  assert.equal(empty.dataset.selectOnFocus, undefined);

  const password = {
    value: "secret",
    type: "password",
    disabled: false,
    readOnly: false,
    dataset: { selectOnFocus: "true" },
    removeAttribute(name) {
      if (name === "data-select-on-focus") delete this.dataset.selectOnFocus;
    },
    select() { throw new Error("password fields should keep normal editing"); },
  };
  assert.equal(selectProviderConsoleFieldOnFocus(password), false);
});

test("供应商新增表单使用 placeholder 示例且名称和 Base URL 不自动全选", () => {
  const html = renderProviderConsolePage({
    providers: [],
    consoleState: { drawer: "provider", mode: "create", type: "openai-compatible", draft: createProviderDraft("openai-compatible") },
  });
  assert.match(html, /name="name" value=""[^>]*placeholder="custom-openai"/);
  assert.match(html, /name="baseUrl"[^>]*value=""[^>]*placeholder="https:\/\/api\.example\.com\/v1"/);
  assert.doesNotMatch(html, /name="name"[^>]*data-select-on-focus="true"/);
  assert.doesNotMatch(html, /name="baseUrl"[^>]*data-select-on-focus="true"/);
  assert.doesNotMatch(html, /name="model"[^>]*data-select-on-focus="true"/);
  assert.match(html, /data-mp-model-workspace/);
  assert.doesNotMatch(html, /name="maxTokens"|data-mp-model-example|mp-provider-reference-final/);
});

test("供应商弹层焦点环与触发元素恢复", () => {
  const first = { disabled: false, tabIndex: 0, getAttribute: () => null, focused: 0, focus() { this.focused += 1; } };
  const last = { disabled: false, tabIndex: 0, getAttribute: () => null, focused: 0, focus() { this.focused += 1; } };
  const layer = { querySelectorAll: () => [first, last], focused: 0, focus() { this.focused += 1; } };
  const forward = { key: "Tab", target: last, shiftKey: false, prevented: false, preventDefault() { this.prevented = true; } };
  assert.equal(trapProviderConsoleFocus(forward, layer), true);
  assert.equal(forward.prevented, true);
  assert.equal(first.focused, 1);
  const backward = { key: "Tab", target: first, shiftKey: true, prevented: false, preventDefault() { this.prevented = true; } };
  assert.equal(trapProviderConsoleFocus(backward, layer), true);
  assert.equal(backward.prevented, true);
  assert.equal(last.focused, 1);
  const emptyLayer = { querySelectorAll: () => [], focused: 0, focus() { this.focused += 1; } };
  assert.equal(trapProviderConsoleFocus({ key: "Tab", target: emptyLayer, preventDefault() {} }, emptyLayer), true);
  assert.equal(emptyLayer.focused, 1);
  const trigger = { focused: 0, focus() { this.focused += 1; } };
  restoreProviderConsoleFocus(trigger);
  assert.equal(trigger.focused, 1);
});

test("英文与繁中控制台不会回退为简中硬编码", () => {
  const providers = modelProvidersForUIUnion([{ name: "gateway", type: "openai-compatible", origin: "custom", enabled: true, model: "model-a", baseUrl: "https://gateway.example/v1" }], []);
  const root = { title: "", documentElement: { lang: "", dataset: {} }, querySelectorAll() { return []; } };
  setUILocale("en", root);
  const english = renderProviderConsolePage({ providers, consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: providers[0] } });
  const englishCreate = renderProviderConsolePage({ providers, consoleState: { drawer: "provider", mode: "create", type: "openai-compatible", draft: createProviderDraft("openai-compatible") } });
  assert.match(english, /Edit provider|Back to providers|Connection and credential/);
  assert.match(englishCreate, /Back to providers|Connection and credential|Discard changes/);
  assert.doesNotMatch(`${english}${englishCreate}`, /供应商|连接|配置|当前|保存|删除/);
  setUILocale("zh-TW", root);
  const traditional = renderProviderConsolePage({ providers, consoleState: { modal: "types" } });
  const traditionalCreate = renderProviderConsolePage({ providers, consoleState: { drawer: "provider", mode: "create", type: "openai-compatible", draft: createProviderDraft("openai-compatible") } });
  assert.match(traditional, /模型供應商|新增供應商/);
  assert.doesNotMatch(traditional, /選擇連線類型|mp-provider-type-modal/);
  assert.match(traditionalCreate, /返回供應商清單|連線與憑據|放棄變更/);
  assert.doesNotMatch(`${traditional}${traditionalCreate}`, /供应商|连接|配置|当前|保存|删除/);
  setUILocale("zh-CN", root);
});

test("缺失 origin 时保守保护删除，明确 custom 才可删除", () => {
  for (const name of ["openai-compatible", "cliproxyapi", "ollama"]) {
    assert.equal(isBuiltinProvider({ name }), true, name);
  }
  assert.equal(isBuiltinProvider({ name: "unlabeled-provider" }), true);
  assert.equal(isProviderDeletable({ name: "unlabeled-provider" }), false);
  assert.equal(isProviderDeletable({ name: "openai-compatible", origin: "custom" }), true);
  const protectedHtml = renderProviderConsolePage({
    providers: modelProvidersForUIUnion([{ name: "unlabeled-provider", type: "openai-compatible", enabled: false, model: "model-a" }], []),
    consoleState: { drawer: "provider", mode: "edit", type: "openai-compatible", draft: { name: "unlabeled-provider", type: "openai-compatible", model: "model-a", origin: "unknown" } },
  });
  assert.doesNotMatch(protectedHtml, /data-mp-delete-provider/);
});

test("Codex 控制台使用独立账号页并让凭证导入区常开", () => {
  const state = {
    settings: { providers: [{ name: "codex", type: "codex", origin: "builtin", enabled: true, model: "gpt-5.5" }] },
    modelCatalog: { providers: [{ name: "codex", type: "codex", configured: true, models: ["gpt-5.5", "gpt-5.4"] }] },
    providerAuthFiles: [{ id: "account-1", alias: "Main", quota: { primary_window: { used_percent: 10, limit_window_seconds: 3600 } } }],
    providerAuthLoading: false,
    codexAccountBusy: {},
    providerConsole: {
      view: "codex",
      mode: "codex",
      type: "codex",
      draft: createProviderDraft("codex"),
      codexImportOpen: false,
      codexImportDraft: `</textarea><script>alert(1)</script>`,
    },
  };
  const controller = createModelProviderSettingsController({ state });
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /codex-account-console/);
  assert.match(html, /data-mp-close-codex-page/);
  assert.match(html, /codex-browser-login-panel/);
  assert.match(html, /data-mp-codex-browser-login/);
  assert.match(html, /浏览器登录/);
  assert.match(html, /手动导入 JSON \/ Token/);
  assert.match(html, /codexAuthImportText/);
  assert.match(html, /data-mp-codex-import/);
  assert.match(html, /data-codex-import-files/);
  assert.match(html, /codex-import-dropzone/);
  assert.match(html, /codex-account-table/);
  assert.match(html, /data-codex-select="account-1"/);
  assert.match(html, /data-codex-batch-sync/);
  assert.match(html, /data-codex-export="account-1"/);
  assert.match(html, /codex-model-panel/);
  assert.match(html, /Codex 模型/);
  assert.match(html, /data-mp-provider-form data-codex-provider-config="codex"/);
  assert.match(html, /data-mp-model-config="gpt-5\.5"/);
  assert.match(html, /data-mp-model-config="gpt-5\.4"/);
  assert.match(html, /data-mp-refresh-models/);
  assert.doesNotMatch(html, /data-mp-fetch-models/);
  assert.doesNotMatch(html, /codex-credits-summary|Credits|Unlimited/);
  assert.equal((html.match(/id="codexCredentialImportSection"/g) || []).length, 1);
  assert.doesNotMatch(html, /data-mp-codex-toggle-import/);
  assert.doesNotMatch(html, /mp-provider-drawer/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /&lt;\/textarea&gt;&lt;script&gt;/);

  state.providerConsole.codexBrowserLogin = {
    seq: 1,
    loginId: "login-1",
    status: "pending",
    authUrl: "https://auth.openai.com/oauth/authorize?state=opaque",
    expiresAt: "2026-07-17T21:00:00Z",
    popupBlocked: true,
  };
  const pending = controller.renderProviderSettingsContent();
  assert.match(pending, /等待浏览器授权/);
  assert.match(pending, /data-mp-codex-browser-open/);
  assert.match(pending, /data-mp-codex-browser-cancel/);
  assert.match(pending, /登录窗口可能被浏览器拦截/);
  assert.doesNotMatch(pending, /data-mp-codex-browser-login/);

  state.runtimeSummary = { security: { currentRequestRemote: true, remoteAccessRequired: true } };
  state.providerConsole.codexBrowserLogin = { seq: 2, status: "idle" };
  const remote = controller.renderProviderSettingsContent();
  assert.match(remote, /浏览器登录只能在运行 Autoto 的本机完成/);
  assert.match(remote, /data-mp-codex-browser-login disabled/);
});

test("Codex 模型面板的眼睛直接更新模型选择器可见性", () => {
  let prefs = { hiddenModels: {}, showUnconfiguredProviders: false };
  let panelRefreshes = 0;
  const state = {
    settings: { providers: [{ name: "codex", type: "codex", origin: "builtin", enabled: true, configured: true, model: "gpt-5.5" }] },
    modelCatalog: { providers: [{ name: "codex", type: "codex", configured: true, models: ["gpt-5.5", "gpt-5.4"] }] },
    providerAuthFiles: [{ id: "account-1" }],
    providerAuthLoading: false,
    providerConsole: { view: "codex", mode: "codex", type: "codex", providerName: "codex", draft: createProviderDraft("codex") },
  };
  const controller = createModelProviderSettingsController({
    state,
    getModelVisibilityPreference: () => prefs,
    setModelVisibilityPreference: (next) => { prefs = next; },
    refreshActiveSettingsPanel: () => { panelRefreshes += 1; },
  });
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
    const form = { elements: { name: { value: "codex" } } };
    const target = {
      dataset: { mpModelVisibility: "gpt-5.5", hidden: "false" },
      closest: (selector) => {
        if (selector === "[data-subscription-provider-config]") return null;
        if (selector === "[data-codex-provider-config]") return form;
        if (selector.includes("button")) return target;
        return null;
      },
    };
    listeners.click({ target, preventDefault() {}, stopPropagation() {} });
    assert.equal(prefs.hiddenModels["codex:gpt-5.5"], true);
    assert.ok(panelRefreshes > 0, "the Codex model panel must re-render after visibility changes");
    assert.match(controller.renderProviderSettingsContent(), /mp-provider-model-config-row is-hidden[^>]*data-mp-model-config="gpt-5\.5"/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("已保存供应商编辑页的眼睛立即持久化到共享可见性偏好", () => {
  let prefs = { hiddenModels: { "relay:stale": true }, showUnconfiguredProviders: false };
  const draft = {
    name: "relay",
    type: "openai-compatible",
    baseUrl: "https://relay.example/v1",
    model: "a",
    models: ["a", "b"],
    modelConfigs: [
      { name: "a", contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: false },
      { name: "b", contextTokenLimit: 0, imageGeneration: false, hidden: false, manual: false },
    ],
    modelsReady: true,
    modelsStale: false,
  };
  const state = {
    settings: { providers: [{ name: "relay", type: "openai-compatible", enabled: true, configured: true, model: "a" }] },
    modelCatalog: { providers: [{ name: "relay", type: "openai-compatible", configured: true, models: ["a", "b"] }] },
    providerConsole: { view: "providers", drawer: "provider", mode: "edit", type: "openai-compatible", providerName: "relay", draft, dirty: false },
  };
  let prefWrites = 0;
  const controller = createModelProviderSettingsController({
    state,
    getModelVisibilityPreference: () => prefs,
    setModelVisibilityPreference: (next) => { prefs = next; prefWrites += 1; },
    refreshActiveSettingsPanel: () => {},
  });
  const listeners = {};
  const root = { addEventListener: (type, handler) => { listeners[type] = handler; }, removeEventListener: () => {} };
  const makeTarget = (dataset) => {
    const target = {
      dataset,
      closest: (selector) => {
        if (selector === "[data-subscription-provider-config]") return null;
        if (selector === "[data-codex-provider-config]") return null;
        if (selector.includes("button")) return target;
        return null;
      },
    };
    return target;
  };
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "settingsContentBody" ? root : null) };
  try {
    controller.bindProviderSettingsActions();
    assert.ok(listeners.click, "the console must bind a click handler");

    // Hiding a model on an already-saved provider must survive closing the
    // drawer without saving: the shared preference is rewritten immediately
    // and stale keys under the provider prefix are cleaned like a save would.
    listeners.click({ target: makeTarget({ mpModelVisibility: "b", hidden: "false" }), preventDefault() {}, stopPropagation() {} });
    assert.equal(prefs.hiddenModels["relay:b"], true);
    assert.equal(prefs.hiddenModels["relay:stale"], undefined);
    assert.equal(state.providerConsole.dirty, false, "an instantly-applied eye must not force a save");
    assert.equal(state.providerConsole.draft.modelConfigs.find((item) => item.name === "b")?.hidden, true);
    assert.ok(prefWrites > 0, "the shared preference setter must be called");

    // The hide-all control persists the same way.
    listeners.click({ target: makeTarget({ mpModelVisibilityAll: "", allVisible: "true" }), preventDefault() {}, stopPropagation() {} });
    assert.equal(prefs.hiddenModels["relay:a"], true);
    assert.equal(prefs.hiddenModels["relay:b"], true);
    assert.equal(state.providerConsole.dirty, false);

    // A provider still being created has no saved name yet: the eye stays a
    // draft change (dirty, persisted on save) and the preference is untouched.
    const before = JSON.stringify(prefs);
    state.providerConsole = {
      view: "providers", drawer: "provider", mode: "create", type: "openai-compatible", providerName: "",
      draft: { ...draft, name: "brand-new", modelConfigs: draft.modelConfigs.map((item) => ({ ...item, hidden: false })) },
      dirty: false,
    };
    listeners.click({ target: makeTarget({ mpModelVisibility: "a", hidden: "false" }), preventDefault() {}, stopPropagation() {} });
    assert.equal(JSON.stringify(prefs), before, "creating a provider must not write shared preferences");
    assert.equal(state.providerConsole.dirty, true);
    assert.equal(state.providerConsole.draft.modelConfigs.find((item) => item.name === "a")?.hidden, true);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Anthropic 内置卡片使用独立账号页并保留模型配置", () => {
  assert.equal(isAnthropicAccountProvider({ name: "anthropic", type: "anthropic", origin: "builtin" }), true);
  assert.equal(isAnthropicAccountProvider({ name: "custom-anthropic", type: "anthropic", origin: "custom" }), false);
  assert.equal(isAnthropicAccountProvider({ name: "legacy-custom", type: "anthropic", origin: "unknown" }), false);
  const state = {
    settings: { providers: [{ name: "anthropic", type: "anthropic", origin: "builtin", enabled: true, configured: true, model: "claude-sonnet-4-5", baseUrl: "", maxTokens: 4096 }] },
    modelCatalog: { providers: [{ name: "anthropic", type: "anthropic", configured: true, models: ["claude-sonnet-4-5", "claude-opus-4"] }] },
    anthropicAccounts: { accounts: [{
      id: "anthropic-1",
      alias: `Team <script>alert(1)</script>`,
      authType: "api_key",
      priority: 5,
      configured: true,
      stats: { successCount: 3, failureCount: 1 },
    }] },
    anthropicAccountsLoading: false,
    anthropicAccountBusy: { "anthropic-1": true },
    providerConsole: {
      view: "anthropic",
      mode: "anthropic",
      type: "anthropic",
      providerName: "anthropic",
      anthropicAddMode: "profile",
      anthropicProfile: `work</code><script>alert(2)</script>`,
      anthropicAlias: `Alias"><img src=x>`,
      anthropicPriority: 8,
      anthropicApiKey: "sk-ant-must-not-render",
      draft: createProviderDraft("anthropic", { name: "anthropic", type: "anthropic", model: "claude-sonnet-4-5", models: ["claude-sonnet-4-5"], configured: true }),
    },
  };
  const controller = createModelProviderSettingsController({ state });
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /anthropic-account-console/);
  assert.match(html, /data-mp-close-anthropic-page/);
  assert.match(html, /data-anthropic-account-form/);
  assert.match(html, /ant auth login --profile 'work&lt;\/code&gt;&lt;script&gt;/);
  assert.match(html, /data-anthropic-copy-command=/);
  assert.match(html, /anthropic-account-table/);
  assert.match(html, /data-anthropic-provider-config/);
  assert.match(html, /name="model"/);
  // The Anthropic account page talks to the official endpoint over OAuth, so
  // it exposes no Base URL or Max tokens field; the stored values survive
  // because the draft keeps whatever the form does not carry.
  assert.doesNotMatch(html, /name="baseUrl"/);
  assert.doesNotMatch(html, /name="maxTokens"/);
  assert.doesNotMatch(html, /name="apiKeyOptional" checked/);
  assert.match(html, /data-mp-fetch-models/);
  assert.match(html, /data-mp-refresh-models/);
  assert.doesNotMatch(html, /mp-provider-drawer/);
  assert.doesNotMatch(html, /<script>|<img /);
  assert.doesNotMatch(html, /sk-ant-must-not-render/);

  state.providerConsole.anthropicAddMode = "api_key";
  const apiKeyMode = controller.renderProviderSettingsContent();
  assert.match(apiKeyMode, /name="apiKey" type="password" value=""/);
  assert.doesNotMatch(apiKeyMode, /sk-ant-must-not-render/);
});

test("旧 relay 控制台状态回退到供应商列表且不再渲染独立入口", () => {
  const state = {
    settings: { providers: [{ name: "openai-compatible", type: "openai-compatible", origin: "builtin", enabled: true, model: "relay-model", baseUrl: "https://relay.example/v1" }] },
    modelCatalog: { providers: [] },
    providerConsole: { drawer: "relay", mode: "relay", type: "relay", draft: null },
  };
  const controller = createModelProviderSettingsController({ state });
  const html = controller.renderProviderSettingsContent();
  assert.match(html, /data-mp-provider-card="openai-compatible"/);
  assert.match(html, /data-mp-provider-open="openai-compatible"/);
  assert.doesNotMatch(html, /relayBaseUrl|relayCustomModel|data-relay-protocol|data-mp-relay-save|data-mp-open-relay|mp-provider-drawer/);
});

test("供应商控制台转义服务端 Provider 文本和错误", () => {
  const html = renderProviderConsolePage({
    providers: modelProvidersForUIUnion([{
      name: `bad"><script>alert(1)</script>`,
      type: "openai-compatible",
      origin: "custom",
      enabled: true,
      baseUrl: `https://x.example/<script>`,
      model: `<img src=x onerror=alert(1)>`,
      error: `<script>server error</script>`,
    }], []),
    consoleState: {},
  });
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /<img /);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /&quot;&gt;&lt;script&gt;/);
});

test("allowPlaintextHTTP 开关渲染、警告横幅和 payload 包含标记", () => {
  setUILocale("zh-TW");
  try {
    const baseState = { drawer: "provider", mode: "create", type: "openai-compatible", dirty: true };

    // Default: switch rendered, warning absent, payload flag false.
    const defaultDraft = { name: "relay", type: "openai-compatible", baseUrl: "http://154.36.172.121:3000/v1", apiKey: "sk-test", model: "claude-opus-5", allowPlaintextHTTP: false };
    const defaultHtml = renderProviderConsolePage({ providers: [], consoleState: { ...baseState, draft: defaultDraft } });
    assert.match(defaultHtml, /name="allowPlaintextHTTP"/, "the toggle must appear in the form");
    assert.doesNotMatch(defaultHtml, /plaintextWarning|明文 HTTP.*API Key|API Key.*明文/, "warning must be absent when not enabled");

    // Enabled: warning banner must appear.
    const enabledDraft = { ...defaultDraft, allowPlaintextHTTP: true };
    const enabledHtml = renderProviderConsolePage({ providers: [], consoleState: { ...baseState, draft: enabledDraft } });
    assert.match(enabledHtml, /name="allowPlaintextHTTP"[^>]*checked/, "checked when opted-in");
    assert.match(enabledHtml, /mp-provider-security-warning/, "warning banner must appear when enabled");

    // Payload contains the flag.
    const defaultPayload = providerConfigPayload(defaultDraft);
    assert.equal(defaultPayload.allowPlaintextHTTP, false, "false must be included in payload so the server can clear a prior opt-in");
    const enabledPayload = providerConfigPayload(enabledDraft);
    assert.equal(enabledPayload.allowPlaintextHTTP, true, "true must be included when opted-in");
  } finally {
    setUILocale("zh-CN");
  }
});

// 手机上「可用模型」每个模型占三行，九个模型就是一屏滚不完；工具栏在窄屏被
// 拉成一列，让显示/隐藏全部的眼睛图标单独占满一行，看起来像个来源不明的控件。
// providers.css 有多个 767px 区块，按第一个出现位置切片会把桌面规则也算进
// 「移动端」，所以这里按大括号配对精确取出区块内容。
function splitCSSByBreakpoint(css, query) {
  let inside = "";
  let outside = "";
  let cursor = 0;
  for (;;) {
    const start = css.indexOf(query, cursor);
    if (start < 0) { outside += css.slice(cursor); break; }
    outside += css.slice(cursor, start);
    const open = css.indexOf("{", start);
    if (open < 0) { outside += css.slice(start); break; }
    let depth = 0;
    let end = open;
    for (let i = open; i < css.length; i++) {
      if (css[i] === "{") depth++;
      else if (css[i] === "}") { depth--; if (depth === 0) { end = i; break; } }
    }
    inside += css.slice(open + 1, end);
    cursor = end + 1;
  }
  return { inside, outside };
}

test("窄屏下模型列表压缩为两行且工具栏保持一行", async () => {
  const css = await readStylesGroup("providers.css", import.meta.url);
  const { inside: mobile, outside: desktop } = splitCSSByBreakpoint(css, "@media (max-width: 767px)");
  assert.ok(mobile.length > 0, "the 767px breakpoint must exist");
  assert.ok(desktop.includes(".mp-provider-model-limit"), "the desktop rules must be outside the breakpoint");

  // Toolbar stays horizontal so the eye toggle sits beside the refresh action.
  assert.match(mobile, /\.mp-provider-model-toolbar\s*\{[^}]*flex-direction:\s*row/);
  assert.match(mobile, /\.mp-provider-model-visibility-all\s*\{[^}]*flex:\s*0 0 auto/);

  // Limit label and field share one line.
  assert.match(mobile, /\.mp-provider-model-limit\s*\{[^}]*justify-content:\s*space-between/);

  // An unsupported image-generation toggle is not worth a row on a phone.
  assert.match(mobile, /\.mp-provider-model-image-generation\.is-unsupported\s*\{[^}]*display:\s*none/);

  // Desktop keeps both: the stacked label and the unsupported notice.
  assert.match(desktop, /\.mp-provider-model-limit\s*\{[^}]*display:\s*grid/);
  assert.doesNotMatch(desktop, /\.mp-provider-model-image-generation\.is-unsupported\s*\{[^}]*display:\s*none/);
});
