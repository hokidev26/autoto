import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import messagesEN from "./messages-en.mjs";
import messagesZhCN from "./messages-zh-CN.mjs";
import messagesZhTW from "./messages-zh-TW.mjs";
import {
  createSharedAPISettingsController,
  gatewayAccountRequest,
  gatewayConfigNeedsRemoteConfirmation,
  gatewayConfigRequest,
  gatewayKeyPolicyPayload,
  gatewayKeyRequest,
  gatewayProviderRequest,
  gatewayProviderRestriction,
  isCodexGatewayProvider,
  isLoopbackGatewayHost,
  normalizeGatewayAccounts,
  normalizeGatewayKeys,
  normalizeGatewayRequests,
  normalizeGatewaySettings,
  normalizeGatewayStatus,
  normalizeApiTunnel,
} from "./shared-api-settings.mjs";

function enabledState(overrides = {}) {
  return {
    settings: {
      gateway: { enabled: true, host: "127.0.0.1", port: 7788, allowRemote: false, maxGlobalConcurrency: 8, maxRequestBytes: 1048576 },
      providers: [
        { name: "openai", type: "openai", gatewayEnabled: false },
        { name: "codex", type: "codex", gatewayEnabled: false },
        { name: "cliproxyapi", type: "openai-compatible", profile: "cliproxyapi", gatewayEnabled: false },
      ],
    },
    ...overrides,
  };
}

function responseFor(path, options = {}) {
  if (path === "/api/gateway/status") {
    return {
      status: { desiredEnabled: true, running: true, address: "http://127.0.0.1:7788", allowRemote: false, ephemeralIsolation: true, lastError: "", updatedAt: "2026-07-22T12:00:00Z" },
      gateway: { enabled: true, host: "127.0.0.1", port: 7788, allowRemote: false, maxGlobalConcurrency: 8, maxRequestBytes: 1048576 },
      protocols: ["openai-chat", "openai-responses", "anthropic-messages"],
    };
  }
  if (path === "/api/gateway/accounts") return { accounts: [] };
  if (path === "/api/gateway/keys" && !options.method) return { keys: [] };
  if (path === "/api/gateway/usage") return { items: [], summary: {} };
  if (path === "/api/gateway/requests?limit=50") return { requests: [] };
  if (path === "/api/gateway/tunnel") {
    return { available: true, status: "idle", publicApiBaseUrl: "", activeKeys: 0, gatewayRunning: true };
  }
  return {};
}

const loadPaths = [
  "/api/gateway/status",
  "/api/gateway/accounts",
  "/api/gateway/keys",
  "/api/gateway/usage",
  "/api/gateway/requests?limit=50",
  "/api/gateway/tunnel",
  // Every Gateway resource is preloaded in one batch regardless of whether the
  // section that consumes it renders, so alias data travels with the rest.
  "/api/gateway/models",
];

test("normalizes Gateway runtime, settings, accounts, and safe request records", () => {
  assert.deepEqual(normalizeGatewaySettings({}), {
    enabled: false,
    host: "",
    port: 0,
    allowRemote: false,
    maxGlobalConcurrency: 0,
    maxRequestBytes: 0,
  });
  assert.deepEqual(normalizeGatewayKeys({ keys: [] }), []);
  assert.deepEqual(normalizeGatewayKeys(null), []);

  const status = normalizeGatewayStatus({
    desiredEnabled: true,
    running: true,
    address: "0.0.0.0:7788",
    allowRemote: true,
    ephemeralIsolation: true,
    lastError: "safe error",
    updatedAt: "2026-07-22T12:00:00Z",
    gateway: { enabled: true, host: "0.0.0.0", port: 7788, allowRemote: true, maxGlobalConcurrency: 4, maxRequestBytes: 2048 },
    protocols: ["openai", "anthropic"],
  });
  assert.deepEqual(status.status, {
    desiredEnabled: true,
    running: true,
    address: "0.0.0.0:7788",
    allowRemote: true,
    ephemeralIsolation: true,
    lastError: "safe error",
    updatedAt: "2026-07-22T12:00:00Z",
  });
  assert.equal(status.gateway.allowRemote, true);
  assert.deepEqual(status.protocols, ["openai", "anthropic"]);

  assert.deepEqual(normalizeGatewayAccounts({ accounts: [{ provider: "codex", accountId: "acct-1", label: "Work", authType: "oauth", source: "vault", priority: 2, eligible: true, shared: true, effective: true }] })[0], {
    provider: "codex", accountId: "acct-1", label: "Work", authType: "oauth", source: "vault", priority: 2, disabled: false, eligible: true, shared: true, effective: true, reason: "",
  });
  assert.deepEqual(normalizeGatewayRequests({ requests: [{ requestId: "req-1", timestamp: "2026-07-22T12:00:00Z", gatewayKeyName: "Laptop", gatewayKeyPrefix: "ak_live_1234", protocol: "openai", requestKind: "responses", modelAlias: "chat", actualProvider: "codex", actualModel: "gpt-5", safeAccountId: "acct-safe", accountLabel: "Work", promptTokens: 10, completionTokens: 5, duration: 1200, timeToFirstTokenMs: 300, errorMessage: "" }] })[0], {
    id: "req-1", createdAt: "2026-07-22T12:00:00Z", key: "Laptop", protocol: "openai", kind: "responses", alias: "chat", provider: "codex", model: "gpt-5", accountId: "acct-safe", accountLabel: "Work", inputTokens: 10, outputTokens: 5, totalTokens: 15, durationMs: 1200, ttftMs: 300, error: "",
  });
});

test("request builders follow the Shared API backend contract", () => {
  assert.deepEqual(gatewayProviderRequest("custom/name", true), {
    path: "/api/providers/custom%2Fname",
    options: { method: "PATCH", body: JSON.stringify({ gatewayEnabled: true }) },
  });
  assert.deepEqual(gatewayAccountRequest("codex/main", "account/id", true), {
    path: "/api/gateway/accounts/codex%2Fmain/account%2Fid",
    options: { method: "PATCH", body: JSON.stringify({ shared: true }) },
  });
  assert.deepEqual(gatewayConfigRequest({ enabled: true, host: "0.0.0.0", port: "7788", allowRemote: true, maxGlobalConcurrency: "8", maxRequestBytes: "4096" }, true), {
    path: "/api/gateway/config",
    options: { method: "PATCH", body: JSON.stringify({ enabled: true, host: "0.0.0.0", port: 7788, allowRemote: true, maxGlobalConcurrency: 8, maxRequestBytes: 4096, confirmRemoteRisk: true }) },
  });
  assert.equal(isLoopbackGatewayHost("localhost"), true);
  assert.equal(isLoopbackGatewayHost("127.0.0.2"), true);
  assert.equal(isLoopbackGatewayHost("[::1]"), true);
  assert.equal(isLoopbackGatewayHost("0.0.0.0"), false);
  assert.equal(gatewayConfigNeedsRemoteConfirmation({ gateway: { host: "127.0.0.1", allowRemote: false } }, { allowRemote: true }), true);
  assert.equal(gatewayConfigNeedsRemoteConfirmation({ gateway: { host: "127.0.0.1", allowRemote: false } }, { host: "0.0.0.0" }), true);
  assert.equal(gatewayConfigNeedsRemoteConfirmation({ gateway: { host: "127.0.0.1", allowRemote: false } }, { port: 9000 }), false);

  const payload = gatewayKeyPolicyPayload({
    name: " Laptop ",
    enabled: false,
    allowedModels: " chat\ncode,chat ",
    requestsPerMinute: "60.9",
    monthlyTokenLimit: "100000",
    maxConcurrency: "3",
    expiresAt: "2026-08-01T12:00:00Z",
  });
  assert.deepEqual(payload, {
    name: "Laptop",
    enabled: false,
    allowedModels: ["chat", "code"],
    requestsPerMinute: 60,
    monthlyTokenLimit: 100000,
    maxConcurrency: 3,
    expiresAt: "2026-08-01T12:00:00.000Z",
  });
  assert.deepEqual(gatewayKeyRequest("create", {}, payload), {
    path: "/api/gateway/keys",
    options: { method: "POST", cache: "no-store", body: JSON.stringify(payload) },
  });
  assert.deepEqual(gatewayKeyRequest("update", { id: "key/id", updatedAt: "key-v1" }, payload), {
    path: "/api/gateway/keys/key%2Fid",
    options: { method: "PATCH", body: JSON.stringify({ ...payload, expectedUpdatedAt: "key-v1" }) },
  });
  assert.deepEqual(gatewayKeyRequest("rotate", "key-1"), { path: "/api/gateway/keys/key-1/rotate", options: { method: "POST", cache: "no-store" } });
  assert.deepEqual(gatewayKeyRequest("revoke", "key-1"), { path: "/api/gateway/keys/key-1/revoke", options: { method: "POST" } });

});

test("disabled Gateway still loads every preconfiguration resource and renders editable sections", async () => {
  const requests = [];
  const state = { settings: { gateway: { enabled: false, host: "127.0.0.1", port: 7788, allowRemote: false }, providers: [] } };
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      requests.push(path);
      if (path === "/api/gateway/status") return { desiredEnabled: false, running: false, address: "", allowRemote: false, gateway: state.settings.gateway, protocols: [] };
      return responseFor(path, options);
    },
  });

  await controller.load();
  const html = controller.render();

  assert.deepEqual(requests, loadPaths);
  assert.match(html, /Gateway 已停止/);
  assert.match(html, /127\.0\.0\.1:7788/);
  assert.match(html, /data-gateway-config-form/);
  assert.match(html, /shared-api-gateway-section[\s\S]*shared-api-section-head/);
  assert.doesNotMatch(html, /shared-api-gateway-details/);
  assert.match(html, /data-gateway-toggle="true"/);
  assert.match(html, /data-gateway-key-add/);
  assert.doesNotMatch(html, /data-gateway-key-add disabled/);
  assert.doesNotMatch(html, /模型别名|data-gateway-model|Token 安全示例|shared-api-security-note/);
  assert.match(html, /尚未创建访问密钥/);
  assert.match(html, /暂无用量数据/);
  assert.match(html, /暂无最近 Gateway 请求/);
});

test("empty enabled Gateway loads visible settings without alias management", async () => {
  const requests = [];
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      requests.push({ path, options });
      return responseFor(path, options);
    },
  });

  await controller.load();
  const html = controller.render();

  assert.deepEqual(requests.map((item) => item.path), loadPaths);
  assert.match(html, /Gateway 运行中/);
  // The client connection examples section was removed from the page.
  assert.doesNotMatch(html, /shared-api-connections-section/);
  assert.doesNotMatch(html, /模型别名|data-gateway-model|Token 安全示例|shared-api-security-note/);
  assert.match(html, /0 把密钥/);
  assert.match(html, /0 个账号/);
});

test("runtime config uses platform confirmation for remote risk and sends confirmRemoteRisk only after acceptance", async () => {
  const requests = [];
  const confirmations = [];
  const decisions = [false, true];
  const state = enabledState({ settings: { gateway: { enabled: false, host: "127.0.0.1", port: 7788, allowRemote: false, maxGlobalConcurrency: 2, maxRequestBytes: 1024 }, providers: [] } });
  const controller = createSharedAPISettingsController({
    state,
    confirmAction: async (message) => { confirmations.push(message); return decisions.shift(); },
    request: async (path, options = {}) => {
      requests.push({ path, options });
      const body = options.body ? JSON.parse(options.body) : {};
      return {
        status: { desiredEnabled: Boolean(body.enabled), running: Boolean(body.enabled), address: "http://0.0.0.0:9000", allowRemote: Boolean(body.allowRemote), ephemeralIsolation: true, lastError: "", updatedAt: "2026-07-22T12:30:00Z" },
        gateway: { ...state.settings.gateway, ...body },
        protocols: ["openai-responses"],
      };
    },
  });

  assert.equal(await controller.updateGatewayConfig({ host: "0.0.0.0", port: 9000, allowRemote: true, maxGlobalConcurrency: 5, maxRequestBytes: 2048 }), null);
  assert.equal(requests.length, 0, "cancelled confirmation must not send a PATCH");

  await controller.updateGatewayConfig({ host: "0.0.0.0", port: 9000, allowRemote: true, maxGlobalConcurrency: 5, maxRequestBytes: 2048 });
  assert.equal(confirmations.length, 2);
  assert.deepEqual(requests[0], {
    path: "/api/gateway/config",
    options: { method: "PATCH", body: JSON.stringify({ host: "0.0.0.0", port: 9000, allowRemote: true, maxGlobalConcurrency: 5, maxRequestBytes: 2048, confirmRemoteRisk: true }) },
  });
  assert.match(controller.render(), /http:\/\/0\.0\.0\.0:9000/);
  assert.match(controller.render(), /openai-responses/);

  const safeRequests = [];
  const safeController = createSharedAPISettingsController({
    state: enabledState(),
    confirmAction: async () => { throw new Error("safe loopback update must not confirm"); },
    request: async (path, options) => { safeRequests.push({ path, options }); return { gateway: JSON.parse(options.body) }; },
  });
  await safeController.updateGatewayConfig({ host: "127.0.0.1", port: 8899, allowRemote: false, maxGlobalConcurrency: 3, maxRequestBytes: 4096 });
  assert.equal(JSON.parse(safeRequests[0].options.body).confirmRemoteRisk, undefined);
});

test("CLIProxy profiles stay private while Codex and API providers remain shareable", async () => {
  const requests = [];
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      requests.push({ path, options });
      return responseFor(path, options);
    },
  });
  const html = controller.render();

  assert.equal(isCodexGatewayProvider({ name: "codex" }), true);
  assert.equal(isCodexGatewayProvider({ type: "codex" }), true);
  assert.equal(gatewayProviderRestriction({ name: "codex" }), "");
  assert.equal(gatewayProviderRestriction({ profile: "cliproxyapi" }), "oauthProxy");
  assert.match(html, /data-gateway-provider="codex"/);
  assert.doesNotMatch(html, /data-gateway-provider="cliproxyapi"/);
  assert.match(html, /订阅\/OAuth 代理 Profile 不可进入共享池/);
  assert.match(html, /data-gateway-provider="openai"/);

  await controller.toggleProvider("openai", true);
  assert.equal(state.settings.providers[0].gatewayEnabled, true);
  assert.deepEqual(requests.at(-1), {
    path: "/api/providers/openai",
    options: { method: "PATCH", body: JSON.stringify({ gatewayEnabled: true }) },
  });
  await controller.toggleProvider("codex", true);
  assert.equal(state.settings.providers[1].gatewayEnabled, true);
  assert.deepEqual(requests.at(-1), {
    path: "/api/providers/codex",
    options: { method: "PATCH", body: JSON.stringify({ gatewayEnabled: true }) },
  });

  const count = requests.length;
  assert.equal(await controller.toggleProvider("cliproxyapi", true), null);
  assert.equal(requests.length, count);
});

test("account pool groups providers, disables restricted rows, and patches shared/effective state", async () => {
  const requests = [];
  const state = enabledState({
    gatewayAccounts: normalizeGatewayAccounts({ accounts: [
      { provider: "codex", accountId: "acct/main", label: "Work", authType: "oauth", source: "vault", priority: 1, disabled: false, eligible: true, shared: false, effective: false },
      { provider: "codex", accountId: "acct-disabled", label: "Disabled", authType: "oauth", source: "vault", priority: 2, disabled: true, eligible: true, shared: false, effective: false, reason: "Quota disabled" },
      { provider: "anthropic", accountId: "profile-1", label: "CLI profile", authType: "profile", source: "profile", priority: 3, disabled: false, eligible: true, shared: false, effective: false },
      { provider: "openai", accountId: "ineligible", label: "No route", authType: "api-key", source: "vault", priority: 4, disabled: false, eligible: false, shared: false, effective: false, reason: "Provider unavailable" },
    ] }),
  });
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      requests.push({ path, options });
      return { account: { provider: "codex", accountId: "acct/main", label: "Work", authType: "oauth", source: "vault", priority: 1, disabled: false, eligible: true, shared: true, effective: true } };
    },
  });

  const html = controller.render();
  assert.match(html, /Work/);
  assert.match(html, /acct\/main/);
  assert.match(html, /Quota disabled/);
  assert.match(html, /Profile 账号不可共享/);
  assert.match(html, /Provider unavailable/);
  assert.match(html, /data-gateway-account-id="acct\/main"/);
  assert.match(html, /data-gateway-account-id="acct-disabled"[^>]*disabled/);
  assert.match(html, /data-gateway-account-id="profile-1"[^>]*disabled/);

  await controller.toggleAccount("codex", "acct/main", true);
  assert.deepEqual(requests[0], {
    path: "/api/gateway/accounts/codex/acct%2Fmain",
    options: { method: "PATCH", body: JSON.stringify({ shared: true }) },
  });
  assert.equal(state.gatewayAccounts[0].shared, true);
  assert.equal(state.gatewayAccounts[0].effective, true);
  assert.match(controller.render(), /已生效/);

  const count = requests.length;
  assert.equal(await controller.toggleAccount("codex", "acct-disabled", true), null);
  assert.equal(await controller.toggleAccount("anthropic", "profile-1", true), null);
  assert.equal(await controller.toggleAccount("openai", "ineligible", true), null);
  assert.equal(requests.length, count);
});

test("create and rotate expose plaintext tokens only in the controller one-time area", async () => {
  const requests = [];
  const copied = [];
  const state = enabledState();
  let sequence = 0;
  const controller = createSharedAPISettingsController({
    state,
    copyText: async (value) => { copied.push(value); return true; },
    confirmAction: () => true,
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (path === "/api/gateway/keys" && options.method === "POST") {
        return { key: { id: "key-1", name: "Laptop", keyPrefix: "sk_live_abc", enabled: true, allowedModels: ["chat"] }, token: "secret-create-once" };
      }
      if (path.endsWith("/rotate")) {
        sequence += 1;
        return { key: { id: "key-1", name: "Laptop", keyPrefix: `sk_live_rot${sequence}`, enabled: true, allowedModels: ["chat"] }, token: "secret-rotate-once" };
      }
      return responseFor(path, options);
    },
  });

  await controller.createKey({ name: "Laptop", enabled: true, allowedModels: ["chat"] });
  assert.equal(controller.oneTimeTokenValue(), "secret-create-once");
  assert.equal(state.token, undefined);
  assert.equal(state.gatewayToken, undefined);
  assert.equal(JSON.stringify(state).includes("secret-create-once"), false);
  const createdHTML = controller.render();
  assert.match(createdHTML, />secret-create-once<\/code>/);
  assert.doesNotMatch(createdHTML, /data-[^=]+="[^"]*secret-create-once/);
  assert.doesNotMatch(createdHTML, /shared-api-connections-section/);
  await controller.copyOneTimeToken();
  assert.deepEqual(copied, ["secret-create-once"]);

  controller.dismissToken();
  assert.equal(controller.oneTimeTokenValue(), "");
  assert.doesNotMatch(controller.render(), /secret-create-once/);

  await controller.rotateKey("key-1");
  assert.equal(controller.oneTimeTokenValue(), "secret-rotate-once");
  assert.match(controller.render(), /secret-rotate-once/);
  assert.equal(controller.consumeOneTimeToken(), "secret-rotate-once");
  assert.equal(controller.oneTimeTokenValue(), "");
  assert.doesNotMatch(controller.render(), /secret-rotate-once/);

  const source = await readFile(new URL("./shared-api-settings.mjs", import.meta.url), "utf8");
  assert.doesNotMatch(source, /localStorage/);
});

test("key lifecycle covers edit, pause, rotate, and revoke", async () => {
  const requests = [];
  const state = enabledState({ gatewayKeys: [{ id: "key-1", name: "Laptop", keyPrefix: "sk_lap", enabled: true, allowedModels: ["chat"], updatedAt: "key-v1", usage: {} }] });
  const controller = createSharedAPISettingsController({
    state,
    confirmAction: () => true,
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (options.method === "PATCH") {
        const body = JSON.parse(options.body);
        return { key: { ...state.gatewayKeys[0], ...body } };
      }
      if (path.endsWith("/rotate")) return { key: { ...state.gatewayKeys[0], keyPrefix: "sk_new" }, token: "rotated-once" };
      if (path.endsWith("/revoke")) return { key: { ...state.gatewayKeys[0], enabled: false, revokedAt: "2026-07-17T12:00:00Z" } };
      return {};
    },
  });

  await controller.updateKey("key-1", { name: "Team", enabled: true, allowedModels: "chat,code", requestsPerMinute: 20, monthlyTokenLimit: 5000, maxConcurrency: 2 });
  assert.deepEqual(JSON.parse(requests.at(-1).options.body), {
    name: "Team", enabled: true, allowedModels: ["chat", "code"], requestsPerMinute: 20, monthlyTokenLimit: 5000, maxConcurrency: 2, expiresAt: "", expectedUpdatedAt: "key-v1",
  });
  await controller.toggleKey("key-1");
  assert.deepEqual(JSON.parse(requests.at(-1).options.body), { enabled: false, expectedUpdatedAt: "key-v1" });
  await controller.rotateKey("key-1");
  assert.equal(requests.at(-1).path, "/api/gateway/keys/key-1/rotate");
  await controller.revokeKey("key-1");
  assert.equal(requests.at(-1).path, "/api/gateway/keys/key-1/revoke");
  assert.match(controller.render(), /已撤销/);
  assert.doesNotMatch(controller.render(), /data-gateway-key-rotate="key-1"/);
});

test("recent requests render safe routing, account, token, timing, and escaped error fields", async () => {
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      if (path === "/api/gateway/requests?limit=50") {
        return { requests: [{
          id: "req-1", createdAt: "2026-07-22T12:00:00Z", keyName: "Laptop", protocol: "openai", kind: "responses", alias: "public-chat", provider: "codex", model: "gpt-5", accountId: "acct-safe", accountLabel: "Work", inputTokens: 12, outputTokens: 8, totalTokens: 20, durationMs: 1500, ttftMs: 250, errorMessage: '<img src=x onerror="steal()">',
        }] };
      }
      return responseFor(path, options);
    },
  });

  await controller.load();
  const html = controller.render();
  assert.match(html, /Laptop/);
  assert.match(html, /openai \/ responses/);
  assert.match(html, /public-chat → codex:gpt-5/);
  assert.match(html, /Work · acct-safe/);
  assert.match(html, /输入 12 · 输出 8 · 总计 20/);
  assert.match(html, /1\.5 s · TTFT 250 ms/);
  assert.match(html, /&lt;img src=x onerror=&quot;steal\(\)&quot;&gt;/);
  assert.doesNotMatch(html, /<img src=x/);
});

test("one-time token copy only succeeds when the clipboard reports true", async () => {
  const toasts = [];
  let rejectCopy = false;
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    copyText: async () => {
      if (rejectCopy) throw new Error("clipboard unavailable");
      return false;
    },
    showToast: (message) => toasts.push(message),
    request: async (path, options = {}) => {
      if (path === "/api/gateway/keys" && options.method === "POST") {
        return { key: { id: "key-1", name: "Laptop", enabled: true }, token: "secret-copy-once" };
      }
      return responseFor(path, options);
    },
  });

  await controller.createKey({ name: "Laptop" });
  toasts.length = 0;
  assert.equal(await controller.copyOneTimeToken(), false);
  assert.equal(controller.oneTimeTokenValue(), "secret-copy-once");
  assert.deepEqual(toasts, ["复制 Token 失败，请在关闭前手动复制。"]);

  rejectCopy = true;
  assert.equal(await controller.copyOneTimeToken(), false);
  assert.equal(controller.oneTimeTokenValue(), "secret-copy-once");
  assert.deepEqual(toasts, [
    "复制 Token 失败，请在关闭前手动复制。",
    "复制 Token 失败，请在关闭前手动复制。",
  ]);
});

test("PATCH conflicts refresh latest Shared API data without applying stale edits", async () => {
  const requests = [];
  const toasts = [];
  const state = enabledState({
    gatewayKeys: [{ id: "key-1", name: "Laptop", enabled: true, allowedModels: ["public/chat"], updatedAt: "key-v1", usage: {} }],
  });
  const conflict = () => Object.assign(new Error("conflict"), { status: 409 });
  const controller = createSharedAPISettingsController({
    state,
    showToast: (message) => toasts.push(message),
    request: async (path, options = {}) => {
      requests.push({ path, options });
      if (options.method === "PATCH") throw conflict();
      if (path === "/api/gateway/keys") return { keys: [{ id: "key-1", name: "Server key", enabled: false, allowedModels: ["public/chat"], updatedAt: "key-v2" }] };
      return responseFor(path, options);
    },
  });

  await assert.rejects(controller.updateKey("key-1", { name: "Stale key", enabled: true }), /服务器/);
  assert.deepEqual(JSON.parse(requests[0].options.body), {
    name: "Stale key", enabled: true, allowedModels: [], requestsPerMinute: 0, monthlyTokenLimit: 0, maxConcurrency: 0, expiresAt: "", expectedUpdatedAt: "key-v1",
  });
  assert.equal(state.gatewayKeys[0].name, "Server key");
  assert.match(state.gatewayAPIError, /服务器/);
  assert.deepEqual(toasts, []);
});

test("newer Shared API loads prevent older responses from overwriting state", async () => {
  const pending = [];
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: (path) => new Promise((resolve) => pending.push({ path, resolve })),
  });

  const stale = controller.load();
  const fresh = controller.load();
  // Derived from loadPaths so adding a Gateway resource cannot silently
  // desynchronise the two halves of this race.
  assert.equal(pending.length, loadPaths.length * 2);
  pending.slice(0, loadPaths.length).forEach(({ path, resolve }) => resolve(responseFor(path)));
  assert.equal(await stale, false);
  pending.slice(loadPaths.length).forEach(({ path, resolve }) => {
    if (path === "/api/gateway/status") resolve({ desiredEnabled: false, running: false, address: "127.0.0.1:9000", gateway: { enabled: false, host: "127.0.0.1", port: 9000 }, protocols: ["anthropic"] });
    else if (path === "/api/gateway/accounts") resolve({ accounts: [{ provider: "codex", accountId: "new-account", eligible: true }] });
    else if (path === "/api/gateway/keys") resolve({ keys: [{ id: "new", name: "Latest", enabled: true }] });
    else if (path === "/api/gateway/usage") resolve({ items: [], summary: { requests: 2 } });
    else if (path === "/api/gateway/tunnel") resolve({ available: true, status: "idle", activeKeys: 0, gatewayRunning: true });
    else resolve({ requests: [{ id: "latest-request", createdAt: "2026-07-22T12:00:00Z" }] });
  });
  assert.equal(await fresh, true);
  assert.equal(state.gatewayStatus.running, false);
  assert.equal(state.gatewayAccounts[0].accountId, "new-account");
  assert.equal(state.gatewayKeys[0].id, "new");
  assert.equal(state.gatewayUsage.summary.requests, 2);
  assert.equal(state.gatewayRequests[0].id, "latest-request");
});

test("API failures are retained as an escaped panel alert", async () => {
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: async () => { throw new Error('<script>alert("gateway")</script>'); },
  });

  await assert.rejects(controller.load(), /gateway/);
  assert.equal(state.gatewayDataLoaded, true, "a failed initial load must not trigger an automatic retry loop");
  const html = controller.render();
  assert.match(html, /role="alert"/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});

test("Shared API translation keys stay aligned across all three locales", () => {
  const keys = (value, prefix = "") => Object.entries(value || {}).flatMap(([key, item]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    return item && typeof item === "object" && !Array.isArray(item) ? keys(item, path) : [path];
  }).sort();
  assert.deepEqual(keys(messagesZhTW.sharedAPI), keys(messagesZhCN.sharedAPI));
  assert.deepEqual(keys(messagesEN.sharedAPI), keys(messagesZhCN.sharedAPI));
});

test("API tunnel snapshot normalizes status, url, and key count defensively", () => {
  assert.deepEqual(normalizeApiTunnel({}), {
    available: false,
    installable: false,
    status: "unavailable",
    publicUrl: "",
    activeKeys: 0,
    gatewayRunning: false,
    error: "",
  });
  // An unknown status must fall back rather than reach the DOM verbatim.
  assert.equal(normalizeApiTunnel({ status: "<script>" }).status, "unavailable");
  // The backend field is publicApiBaseUrl; publicUrl is accepted as a fallback.
  assert.equal(normalizeApiTunnel({ publicApiBaseUrl: "https://x.test/v1" }).publicUrl, "https://x.test/v1");
  assert.equal(normalizeApiTunnel({ publicUrl: "https://y.test/v1" }).publicUrl, "https://y.test/v1");
  // installable only means something when cloudflared is missing.
  assert.equal(normalizeApiTunnel({ available: true, installable: true }).installable, false);
  assert.equal(normalizeApiTunnel({ available: false, installable: true }).installable, true);
});

test("API tunnel warns about missing keys and blocks starting while the gateway is down", async () => {
  const noKeysRunning = enabledState();
  const controller = createSharedAPISettingsController({
    state: noKeysRunning,
    request: async (path, options = {}) => {
      if (path === "/api/gateway/tunnel") {
        return { available: true, status: "running", publicApiBaseUrl: "https://demo.trycloudflare.com/v1", activeKeys: 0, gatewayRunning: true };
      }
      return responseFor(path, options);
    },
  });
  await controller.load();
  const runningHTML = controller.render();
  // A public URL with no usable key is a real misconfiguration worth surfacing.
  assert.match(runningHTML, /没有可用的 sk- 密钥/);
  assert.match(runningHTML, /https:\/\/demo\.trycloudflare\.com\/v1/);
  assert.match(runningHTML, /data-api-tunnel-stop/);
  assert.doesNotMatch(runningHTML, /data-api-tunnel-start/);

  const gatewayDown = enabledState();
  const downController = createSharedAPISettingsController({
    state: gatewayDown,
    request: async (path, options = {}) => {
      if (path === "/api/gateway/tunnel") {
        return { available: true, status: "idle", activeKeys: 2, gatewayRunning: false };
      }
      return responseFor(path, options);
    },
  });
  await downController.load();
  const downHTML = downController.render();
  assert.match(downHTML, /Gateway 未运行/);
  // The tunnel points at the gateway port, so starting it first is required.
  assert.match(downHTML, /data-api-tunnel-start[^>]*disabled/);
});

test("starting the API tunnel requires explicit confirmation of public exposure", async () => {
  const confirmations = [];
  const requests = [];
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    confirmAction: async (message) => { confirmations.push(message); return false; },
    request: async (path, options = {}) => {
      requests.push({ path, method: options.method });
      if (path === "/api/gateway/tunnel") {
        return { available: true, status: "idle", activeKeys: 1, gatewayRunning: true };
      }
      return responseFor(path, options);
    },
  });
  await controller.load();
  requests.length = 0;

  // Declining must not open the tunnel: this URL is not password protected.
  assert.equal(await controller.startApiTunnel(), null);
  assert.equal(requests.length, 0, "declined confirmation must not POST");
  assert.equal(confirmations.length, 1);
  assert.match(confirmations[0], /sk- 密钥/);

  const accepting = createSharedAPISettingsController({
    state: enabledState(),
    confirmAction: async () => true,
    request: async (path, options = {}) => {
      requests.push({ path, method: options.method });
      if (path === "/api/gateway/tunnel") {
        return { available: true, status: "running", publicApiBaseUrl: "https://ok.trycloudflare.com/v1", activeKeys: 1, gatewayRunning: true };
      }
      return responseFor(path, options);
    },
  });
  await accepting.startApiTunnel();
  assert.deepEqual(requests.at(-1), { path: "/api/gateway/tunnel", method: "POST" });
});

// The two tests above only pin when alias management must stay hidden. Without
// this one, a gate that hid it in every state would still pass them.
test("alias management appears once the Gateway is running with an account to route to", async () => {
  const state = enabledState();
  const controller = createSharedAPISettingsController({
    state,
    request: async (path, options = {}) => {
      if (path === "/api/gateway/accounts") return { accounts: [{ provider: "codex", accountId: "acct-1", eligible: true }] };
      if (path === "/api/gateway/models") return { models: [{ alias: "fast", targetModel: "codex:gpt-5", enabled: true }] };
      return responseFor(path, options);
    },
  });

  await controller.load();
  const html = controller.render();
  assert.match(html, /模型别名/);
  assert.match(html, /data-gateway-model-add/);
  assert.match(html, /fast/);
  assert.match(html, /codex:gpt-5/);
});

test("Gateway, public tunnel, and model aliases stack full-width instead of pinning titles in a side column", async () => {
  const styles = (await readFile(new URL("../styles/extras.css", import.meta.url), "utf8")).replace(/\r\n/g, "\n");
  assert.match(styles, /#settingsContentBody \.shared-api-requests-details\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0,\s*1fr\)/);
  assert.doesNotMatch(styles, /#settingsContentBody \.shared-api-gateway-details \{[^}]*minmax\(150px,\s*210px\)/);
  assert.match(styles, /#settingsContentBody \.compact-settings-section\.shared-api-tunnel-section,[\s\S]*?\.shared-api-gateway-section,[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\)/);
  assert.match(styles, /#settingsContentBody \.shared-api-section-head\s*\{/);
  assert.match(styles, /#settingsContentBody \.shared-api-page \.compact-settings-section-copy h2 \{[^}]*word-break:\s*keep-all/);

  const state = enabledState({
    gatewayStatus: { running: true, desiredEnabled: true, address: "127.0.0.1:51532" },
    gatewayAccounts: [{ provider: "codex", accountId: "acct-1", eligible: true }],
    apiTunnel: { available: true, status: "idle", activeKeys: 1, gatewayRunning: true },
  });
  const html = createSharedAPISettingsController({ state }).render();
  const gateway = html.match(/<section class="compact-settings-section shared-api-gateway-section">[\s\S]*?<\/section>/)?.[0] || "";
  assert.match(gateway, /shared-api-section-head[\s\S]*class="settings-badge/);
  assert.doesNotMatch(gateway, /shared-api-status-row|<details/);
  assert.match(html, /shared-api-tunnel-section[\s\S]*shared-api-section-head[\s\S]*data-api-tunnel-start/);
  assert.match(html, /shared-api-models-section[\s\S]*shared-api-section-head[\s\S]*data-gateway-model-add/);
  assert.match(html, /shared-api-providers-section[\s\S]*shared-api-section-head/);
  assert.match(html, /shared-api-accounts-section[\s\S]*shared-api-section-head/);
  assert.match(html, /shared-api-keys-section[\s\S]*shared-api-section-head/);
  assert.equal((html.match(/<details/g) || []).length, 1);
  assert.match(html, /<details class="shared-api-requests-details">/);
  assert.doesNotMatch(html, /<details class="shared-api-requests-details" open/);
  assert.doesNotMatch(html, /shared-api-(gateway|providers|accounts|keys)-details/);
});
