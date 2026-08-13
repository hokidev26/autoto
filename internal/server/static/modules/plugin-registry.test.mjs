import test from "node:test";
import assert from "node:assert/strict";

import { setUILocale } from "./i18n.mjs";
import { buildPluginInstallPayload, pluginEnvironmentStatuses } from "./plugin-registry.mjs";
import { createPluginRegistryUIController } from "./plugin-registry-ui.mjs";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("plugin install payload requires and trims a local root path", () => {
  assert.deepEqual(buildPluginInstallPayload(" /tmp/plugin "), { rootPath: "/tmp/plugin" });
  assert.throws(() => buildPluginInstallPayload("  "), /插件根目录/);
});

test("plugin environment status exposes only declared key and configured state", () => {
  assert.deepEqual(pluginEnvironmentStatuses({
    environment: [{ key: "API_TOKEN", configured: true, ref: "env:SUPER_SECRET", value: "leak-me" }],
  }), [{ key: "API_TOKEN", configured: true }]);
});

test("plugin registry escapes manifest text and never renders secret targets or values", async () => {
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async () => [{
      id: "plugin-1",
      name: "<script>alert(1)</script>",
      description: "<b>unsafe</b>",
      rootPath: "/tmp/<plugin>",
      enabled: false,
      environment: [{ key: "TOKEN", configured: true, ref: "env:HIDDEN_TARGET", value: "hidden-value" }],
    }],
  });
  await controller.loadPlugins();
  const html = controller.renderPluginRegistryPanel({ description: "Plugins <local>" });
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.doesNotMatch(html, /&lt;b&gt;unsafe&lt;\/b&gt;/);
  assert.match(html, /TOKEN/);
  assert.match(html, /已配置/);
  assert.match(html, /data-plugin-discover="plugin-1" disabled>/);
  assert.doesNotMatch(html, /HIDDEN_TARGET|hidden-value/);
  assert.match(html, /不会删除插件源目录/);
  assert.match(html, /settings-card/);
  assert.match(html, /settings-form-grid/);
  assert.match(html, /aria-live="polite"/);
});

test("plugin actions maintain private busy state and refresh after enable", async () => {
  const enableRequest = deferred();
  const calls = [];
  let listCount = 0;
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      calls.push([path, options.method || "GET"]);
      if (path === "/api/plugins") {
        listCount += 1;
        return [{ id: "p1", name: "P1", enabled: listCount > 1 }];
      }
      if (path === "/api/plugins/p1/enable") {
        assert.deepEqual(JSON.parse(options.body), { confirmExecuteLocalCode: true });
        return enableRequest.promise;
      }
      throw new Error(`unexpected ${path}`);
    },
  });
  await controller.loadPlugins();
  const pending = controller.setPluginEnabled("p1", true);
  assert.equal(controller.isPluginActionBusy("p1", "enable"), true);
  assert.match(controller.renderPluginRegistryPanel({ description: "" }), /disabled/);
  enableRequest.resolve({ id: "p1", name: "P1", enabled: true });
  await pending;
  assert.equal(controller.isPluginActionBusy("p1", "enable"), false);
  assert.deepEqual(calls, [
    ["/api/plugins", "GET"],
    ["/api/plugins/p1/enable", "POST"],
    ["/api/plugins", "GET"],
  ]);
});

test("plugin enable requires an explicit local-code warning confirmation", async () => {
  const calls = [];
  let confirmation = "";
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      calls.push([path, options.method || "GET"]);
      if (path === "/api/plugins") return [{ id: "p1", name: "Dangerous Plugin", enabled: false }];
      throw new Error(`unexpected ${path}`);
    },
  });
  await controller.loadPlugins();
  const changed = await controller.setPluginEnabled("p1", true, { confirm(message) { confirmation = message; return false; } });
  assert.equal(changed, false);
  assert.match(confirmation, /本机执行其代码/);
  assert.deepEqual(calls, [["/api/plugins", "GET"]]);
});

test("uninstall sends DELETE after warning that source files remain", async () => {
  setUILocale("en");
  try {
    const calls = [];
    let confirmText = "";
    const controller = createPluginRegistryUIController({
      state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
      api: async (path, options = {}) => {
        calls.push([path, options.method || "GET"]);
        if (path === "/api/plugins") return calls.length === 1 ? [{ id: "p1", name: "Local Plugin", enabled: false }] : [];
        if (path === "/api/plugins/p1" && options.method === "DELETE") return { ok: true };
        throw new Error(`unexpected ${path}`);
      },
    });
    await controller.loadPlugins();
    const removed = await controller.uninstallPlugin("p1", { confirm(message) { confirmText = message; return true; } });
    assert.equal(removed, true);
    assert.match(confirmText, /does not delete the source directory/);
    assert.deepEqual(calls, [
      ["/api/plugins", "GET"],
      ["/api/plugins/p1", "DELETE"],
      ["/api/plugins", "GET"],
    ]);
  } finally {
    setUILocale("zh-CN");
  }
});

test("plugin update confirms manifest re-read, refreshes, and clears stale runtime caches", async () => {
  const calls = [];
  const toasts = [];
  let confirmText = "";
  let listCount = 0;
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      calls.push([path, options.method || "GET"]);
      if (path === "/api/plugins") {
        listCount += 1;
        return [{ id: "p1", name: "Fresh Plugin", enabled: listCount === 1 }];
      }
      if (path === "/api/plugins/p1/health") return { pluginId: "p1", healthy: true, toolCount: 2, checkedAt: "2026-08-13T02:00:00Z" };
      if (path === "/api/plugins/p1/update") return { id: "p1", name: "Fresh Plugin", enabled: false, status: "disabled" };
      throw new Error(`unexpected ${path}`);
    },
    showToast: (message, variant) => toasts.push([message, variant]),
  });
  await controller.loadPlugins();
  await controller.checkPluginHealth("p1");
  assert.equal(controller.registry.health.p1.toolCount, 2);
  const updated = await controller.updatePlugin("p1", { confirm(message) { confirmText = message; return true; } });
  assert.equal(updated.enabled, false);
  assert.match(confirmText, /重新读取 manifest/);
  assert.match(confirmText, /重新启用/);
  assert.equal(controller.registry.health.p1, undefined);
  assert.deepEqual(calls, [
    ["/api/plugins", "GET"],
    ["/api/plugins/p1/health", "POST"],
    ["/api/plugins/p1/update", "POST"],
    ["/api/plugins", "GET"],
  ]);
  assert.deepEqual(toasts.at(-1), ["已更新插件 Fresh Plugin（已停用，请重新启用）。", "success"]);
  const html = controller.renderPluginRegistryPanel({ description: "" });
  assert.match(html, /data-plugin-update="p1" >/);
  assert.match(html, /data-plugin-health="p1" disabled>/);
});

test("plugin update sends nothing when the manifest confirmation is rejected", async () => {
  const calls = [];
  const toasts = [];
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      calls.push([path, options.method || "GET"]);
      if (path === "/api/plugins") return [{ id: "p1", name: "P1", enabled: true }];
      throw new Error(`unexpected ${path}`);
    },
    showToast: (message, variant) => toasts.push([message, variant]),
  });
  await controller.loadPlugins();
  const changed = await controller.updatePlugin("p1", { confirm() { return false; } });
  assert.equal(changed, false);
  assert.deepEqual(calls, [["/api/plugins", "GET"]]);
  assert.deepEqual(toasts, []);
});

test("plugin health check shows a busy label then renders the healthy pill", async () => {
  const healthRequest = deferred();
  const toasts = [];
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      if (path === "/api/plugins") return [{ id: "p1", name: "P1", enabled: true }];
      if (path === "/api/plugins/p1/health" && options.method === "POST") return healthRequest.promise;
      throw new Error(`unexpected ${path}`);
    },
    showToast: (message, variant) => toasts.push([message, variant]),
  });
  await controller.loadPlugins();
  const pending = controller.checkPluginHealth("p1");
  assert.equal(controller.isPluginActionBusy("p1", "health"), true);
  assert.match(controller.renderPluginRegistryPanel({ description: "" }), /检查中/);
  healthRequest.resolve({ pluginId: "p1", healthy: true, toolCount: 3, checkedAt: "2026-08-13T02:00:00Z" });
  const report = await pending;
  assert.equal(report.healthy, true);
  assert.equal(controller.isPluginActionBusy("p1", "health"), false);
  assert.equal(controller.registry.health.p1.toolCount, 3);
  const html = controller.renderPluginRegistryPanel({ description: "" });
  assert.match(html, /settings-badge ok">插件健康 · 工具 3 · 检查时间/);
  assert.deepEqual(toasts, [["插件健康 · 工具 3", "success"]]);
});

test("plugin health check surfaces unhealthy reports with the server error", async () => {
  const toasts = [];
  const controller = createPluginRegistryUIController({
    state: { activeSettingsPanel: "skills", activeSkillTab: "plugins" },
    api: async (path, options = {}) => {
      if (path === "/api/plugins") return [{ id: "p1", name: "P1", enabled: true }];
      if (path === "/api/plugins/p1/health" && options.method === "POST") {
        return { pluginId: "p1", healthy: false, toolCount: 0, checkedAt: "2026-08-13T02:00:00Z", error: "plugin is disabled" };
      }
      throw new Error(`unexpected ${path}`);
    },
    showToast: (message, variant) => toasts.push([message, variant]),
  });
  await controller.loadPlugins();
  const report = await controller.checkPluginHealth("p1");
  assert.equal(report.healthy, false);
  const html = controller.renderPluginRegistryPanel({ description: "" });
  assert.match(html, /settings-badge warn">插件异常：plugin is disabled/);
  assert.deepEqual(toasts, [["插件异常：plugin is disabled", "warn"]]);
});
