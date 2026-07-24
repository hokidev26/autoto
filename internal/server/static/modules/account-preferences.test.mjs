import test from "node:test";
import assert from "node:assert/strict";

import {
  accountPreferenceLegacyKeys,
  accountPreferencesCurrentSetupVersion,
  createAccountPreferencesController,
  normalizeSetupVersion,
} from "./account-preferences.mjs";

class MemoryStorage {
  constructor(entries = []) {
    this.values = new Map(entries);
  }
  getItem(key) { return this.values.has(key) ? this.values.get(key) : null; }
  setItem(key, value) { this.values.set(key, String(value)); }
  removeItem(key) { this.values.delete(key); }
}

class MemoryEvents {
  listeners = new Map();
  addEventListener(name, callback) { this.listeners.set(name, callback); }
  dispatch(name) { return this.listeners.get(name)?.(); }
}

function serverSnapshot(overrides = {}) {
  return {
    scopeKey: "user-a",
    profile: { displayName: "Server", avatarInitials: "SV" },
    preferredModel: "openai:gpt-server",
    modelVisibility: { hiddenModels: {}, showUnconfiguredProviders: false },
    setupVersion: 0,
    revision: 3,
    localStorageImportVersion: 1,
    updatedAt: "2026-07-18T00:00:00Z",
    ...overrides,
  };
}

function requestQueue(items) {
  const calls = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options, body: options.body ? JSON.parse(options.body) : null });
    const item = items.shift();
    if (item instanceof Error) throw item;
    if (typeof item === "function") return item(path, options, calls);
    return structuredClone(item);
  };
  return { request, calls };
}

function httpError(status, message = String(status)) {
  return Object.assign(new Error(message), { status });
}

test("服务端快照优先于未命名 localStorage 旧偏好", async () => {
  const storage = new MemoryStorage([
    ["autoto.profile", JSON.stringify({ displayName: "Legacy" })],
    ["autoto.preferredModel", "openai:gpt-legacy"],
  ]);
  const queue = requestQueue([serverSnapshot()]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });

  await controller.hydrate();

  assert.equal(controller.getProfile().displayName, "Server");
  assert.equal(controller.getPreferredModel(), "openai:gpt-server");
  assert.equal(queue.calls.length, 1);
  assert.equal(storage.getItem("autoto.profile"), JSON.stringify({ displayName: "Legacy" }));
});

test("首次设置版本不会接受未来值", () => {
  assert.equal(normalizeSetupVersion(-1), 0);
  assert.equal(normalizeSetupVersion(accountPreferencesCurrentSetupVersion + 10), accountPreferencesCurrentSetupVersion);
});

test("首次设置完成版本与首选模型一起持久化", async () => {
  const queue = requestQueue([
    serverSnapshot(),
    serverSnapshot({ revision: 4, setupVersion: 1, preferredModel: "relay:model-a" }),
  ]);
  const controller = createAccountPreferencesController({ request: queue.request, storage: new MemoryStorage(), eventTarget: new MemoryEvents() });

  await controller.hydrate();
  await controller.setPreferences({ preferredModel: "relay:model-a", setupVersion: 1 });

  assert.equal(queue.calls[1].path, "/api/preferences");
  assert.equal(queue.calls[1].body.preferredModel, "relay:model-a");
  assert.equal(queue.calls[1].body.setupVersion, 1);
  assert.equal(controller.getPreferredModel(), "relay:model-a");
  assert.equal(controller.getSetupVersion(), 1);
  assert.equal(controller.hasPendingPatch(), false);
});

test("网络失败时只回退当前 scope 的命名缓存", async () => {
  const storage = new MemoryStorage();
  const queue = requestQueue([serverSnapshot(), httpError(503)]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });
  await controller.hydrate();

  await assert.rejects(controller.refresh(), /503/);

  assert.equal(controller.getProfile().displayName, "Server");
  assert.equal(controller.getStatus(), "offline");
  assert.ok([...storage.values.keys()].some((key) => key.includes("accountPreferences.cache.user-a")));
});

test("401 清理活动 scope 的缓存和 pending", async () => {
  const storage = new MemoryStorage();
  const queue = requestQueue([serverSnapshot(), httpError(503), httpError(401)]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });
  await controller.hydrate();
  await controller.setPreferredModel("openai:offline");
  assert.equal(controller.hasPendingPatch(), true);

  await assert.rejects(controller.refresh(), /401/);

  assert.equal(controller.getSnapshot().scopeKey, "");
  assert.equal(controller.getPreferredModel(), "");
  assert.equal(controller.getStatus(), "unauthorized");
  assert.equal([...storage.values.keys()].some((key) => key.includes("accountPreferences") && key.includes("user-a")), false);
});

test("403 权限降级时清理活动 scope 的缓存和 pending", async () => {
  const storage = new MemoryStorage();
  const queue = requestQueue([serverSnapshot(), httpError(503), httpError(403)]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });
  await controller.hydrate();
  await controller.setPreferredModel("openai:offline");
  assert.equal(controller.hasPendingPatch(), true);

  await assert.rejects(controller.refresh(), /403/);

  assert.equal(controller.getSnapshot().scopeKey, "");
  assert.equal(controller.getProfile().displayName, "");
  assert.equal(controller.getPreferredModel(), "");
  assert.equal(controller.getStatus(), "unauthorized");
  assert.equal([...storage.values.keys()].some((key) => key.includes("accountPreferences") && key.includes("user-a")), false);
});

test("未知 scope 网络失败时绝不读取另一个用户的缓存", async () => {
  const storage = new MemoryStorage([
    ["autoto.accountPreferences.cache.user-a", JSON.stringify(serverSnapshot())],
  ]);
  const queue = requestQueue([httpError(503)]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });

  await controller.hydrate();

  assert.equal(controller.getSnapshot().scopeKey, "");
  assert.equal(controller.getProfile().displayName, "");
  assert.equal(controller.getStatus(), "offline");
});

test("local import 成功后才删除 Autoto 旧 key", async () => {
  const storage = new MemoryStorage([
    ["autoto.profile", JSON.stringify({ displayName: "Imported", avatarInitials: "im" })],
    ["autoto.preferredModel", "openai:gpt-imported"],
    ["autoto.modelVisibility", JSON.stringify({ hiddenModels: { "openai:hidden": true } })],
  ]);
  const imported = serverSnapshot({
    profile: { displayName: "Imported", avatarInitials: "IM" },
    preferredModel: "openai:gpt-imported",
    modelVisibility: { hiddenModels: { "openai:hidden": true }, showUnconfiguredProviders: false },
    revision: 1,
    localStorageImportVersion: 1,
  });
  const queue = requestQueue([serverSnapshot({ revision: 0, localStorageImportVersion: 0 }), imported]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });

  await controller.hydrate();

  assert.equal(queue.calls[1].path, "/api/preferences/import-local");
  assert.equal(queue.calls[1].body.version, 1);
  assert.equal(queue.calls[1].body.profile.displayName, "Imported");
  assert.equal(queue.calls[1].body.preferredModel, "openai:gpt-imported");
  assert.deepEqual(queue.calls[1].body.modelVisibility.hiddenModels, { "openai:hidden": true });
  assert.equal(storage.getItem("autoto.profile"), null);
  assert.equal(storage.getItem("autoto.preferredModel"), null);
  assert.equal(storage.getItem("autoto.modelVisibility"), null);
});

test("local import 失败保留所有旧 key", async () => {
  const storage = new MemoryStorage([
    ["autoto.profile", JSON.stringify({ displayName: "Keep" })],
    ["codeharbor.profile", JSON.stringify({ displayName: "Also keep" })],
  ]);
  const queue = requestQueue([serverSnapshot({ localStorageImportVersion: 0 }), httpError(503)]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });

  await controller.hydrate();

  assert.notEqual(storage.getItem("autoto.profile"), null);
  assert.notEqual(storage.getItem("codeharbor.profile"), null);
});

test("legacy CodeHarbor key 可导入且成功后统一删除", async () => {
  const storage = new MemoryStorage([
    ["codeharbor.profile", JSON.stringify({ displayName: "CodeHarbor" })],
    ["codeharbor.preferredModel", "anthropic:legacy"],
    ["codeharbor.modelVisibility", JSON.stringify({ showUnconfiguredProviders: true })],
  ]);
  const queue = requestQueue([
    serverSnapshot({ localStorageImportVersion: 0 }),
    serverSnapshot({ profile: { displayName: "CodeHarbor" }, preferredModel: "anthropic:legacy", localStorageImportVersion: 1 }),
  ]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });

  await controller.hydrate();

  assert.equal(queue.calls[1].body.profile.displayName, "CodeHarbor");
  assert.equal(queue.calls[1].body.preferredModel, "anthropic:legacy");
  assert.equal(queue.calls[1].body.modelVisibility.showUnconfiguredProviders, true);
  Object.values(accountPreferenceLegacyKeys).flat().forEach((key) => assert.equal(storage.getItem(key), null));
});

test("CAS 409 后重新 GET 并只重放一次", async () => {
  const queue = requestQueue([
    serverSnapshot({ revision: 3 }),
    httpError(409),
    serverSnapshot({ revision: 4, preferredModel: "openai:other-tab" }),
    serverSnapshot({ revision: 5, preferredModel: "openai:mine" }),
  ]);
  const controller = createAccountPreferencesController({ request: queue.request, storage: new MemoryStorage(), eventTarget: new MemoryEvents() });
  await controller.hydrate();

  await controller.setPreferredModel("openai:mine");

  assert.equal(controller.getPreferredModel(), "openai:mine");
  assert.equal(controller.getSnapshot().revision, 5);
  assert.deepEqual(queue.calls.map((call) => call.path), [
    "/api/preferences", "/api/preferences", "/api/preferences", "/api/preferences",
  ]);
  assert.equal(queue.calls[1].body.expectedRevision, 3);
  assert.equal(queue.calls[3].body.expectedRevision, 4);
});

test("scope 切换不会把上一用户的 pending 重放到新用户", async () => {
  const storage = new MemoryStorage();
  const queue = requestQueue([
    serverSnapshot({ scopeKey: "user:a", revision: 1 }),
    httpError(503),
    serverSnapshot({ scopeKey: "user:b", revision: 2, preferredModel: "openai:b" }),
    serverSnapshot({
      scopeKey: "user:b",
      revision: 3,
      profile: { displayName: "User B", avatarInitials: "UB" },
      preferredModel: "openai:b",
    }),
  ]);
  const controller = createAccountPreferencesController({ request: queue.request, storage, eventTarget: new MemoryEvents() });
  await controller.hydrate();
  await controller.setPreferredModel("openai:a-pending");
  assert.equal(controller.hasPendingPatch(), true);

  await controller.refresh();

  assert.equal(controller.getSnapshot().scopeKey, "user:b");
  assert.equal(controller.getPreferredModel(), "openai:b");
  assert.equal(controller.hasPendingPatch(), false);
  assert.ok([...storage.values.keys()].some((key) => key.includes("accountPreferences.pending.user%3Aa")));

  await controller.setProfile({ displayName: "User B", avatarInitials: "ub" });
  assert.equal(queue.calls[3].body.expectedRevision, 2);
  assert.equal(Object.prototype.hasOwnProperty.call(queue.calls[3].body, "preferredModel"), false);
  assert.equal(controller.getProfile().displayName, "User B");
});

test("离线乐观写入 pending 并在 online 重试", async () => {
  const events = new MemoryEvents();
  const queue = requestQueue([
    serverSnapshot({ revision: 1 }),
    httpError(503),
    serverSnapshot({ revision: 2, preferredModel: "openai:replayed" }),
  ]);
  const controller = createAccountPreferencesController({ request: queue.request, storage: new MemoryStorage(), eventTarget: events });
  await controller.hydrate();

  await controller.setPreferredModel("openai:replayed");
  assert.equal(controller.getPreferredModel(), "openai:replayed");
  assert.equal(controller.hasPendingPatch(), true);

  await events.dispatch("online");
  assert.equal(controller.hasPendingPatch(), false);
  assert.equal(controller.getStatus(), "synced");
});
