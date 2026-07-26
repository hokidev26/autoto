import test from "node:test";
import assert from "node:assert/strict";

import {
  createAutomationToolCatalogController,
  safeOfficialHTTPSURL,
} from "./automation-tool-catalog.mjs";
import skillsMessages, { t } from "./messages-skills.mjs";
import { createSkillsWorkbenchController } from "./skills-workbench.mjs";

function managedItem(overrides = {}) {
  return {
    id: "browser-mcp",
    kind: "mcp",
    installMode: "managed",
    publisher: "Official Publisher",
    license: "MIT",
    purpose: "browserAutomation",
    riskBoundary: "browserSideEffects",
    dataAccess: ["pageContent", "consoleNetwork", "screenshots"],
    safetyDefaults: ["systemChrome", "isolatedContext", "coreCapabilities"],
    packageName: "@official/browser-mcp",
    version: "1.2.3",
    sourceUrl: "https://github.com/official/browser-mcp",
    docsUrl: "https://docs.example.com/browser-mcp",
    installUrl: "https://example.com/browser-mcp",
    platforms: ["windows", "darwin", "linux"],
    supported: true,
    managedPath: "C:/Autoto/tools/browser-mcp",
    installed: false,
    installedVersion: "",
    configured: false,
    mcpServerId: "",
    enabled: false,
    canInstall: true,
    canConfigure: false,
    canEnable: false,
    prerequisites: [{ id: "node", available: true, detail: "Node.js 22" }],
    ...overrides,
  };
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function flattenKeys(value, prefix = "") {
  const keys = [];
  for (const [key, child] of Object.entries(value || {})) {
    const path = prefix ? `${prefix}.${key}` : key;
    if (child && typeof child === "object" && !Array.isArray(child)) keys.push(...flattenKeys(child, path));
    else keys.push(path);
  }
  return keys.sort();
}

test("catalog load is a GET-only operation with no install, configure, enable, discovery, or external side effects", async () => {
  const calls = [];
  const confirmations = [];
  const opened = [];
  let registryChanges = 0;
  const controller = createAutomationToolCatalogController({
    state: {},
    request: async (path, options = {}) => {
      calls.push([path, options]);
      return [managedItem()];
    },
    confirmAction: (message) => { confirmations.push(message); return true; },
    openExternal: (url) => opened.push(url),
    onRegistryChanged: () => { registryChanges += 1; },
  });

  assert.equal(await controller.load(), true);
  assert.deepEqual(calls, [["/api/optional-tools/automation", {}]]);
  assert.deepEqual(confirmations, []);
  assert.deepEqual(opened, []);
  assert.equal(registryChanges, 0);
  assert.equal(controller.snapshot().loaded, true);
});

test("install, configure, and enable confirmation cancellation perform no request", async () => {
  const calls = [];
  const state = {
    automationToolCatalogItems: [managedItem({ canConfigure: true, mcpServerId: "server-1", canEnable: true })],
    automationToolCatalogLoaded: true,
  };
  const controller = createAutomationToolCatalogController({
    state,
    request: async (...args) => { calls.push(args); },
    confirmAction: async () => false,
  });

  assert.equal(await controller.install("browser-mcp"), false);
  assert.equal(await controller.configure("browser-mcp"), false);
  assert.equal(await controller.setEnabled("browser-mcp", true), false);
  assert.deepEqual(calls, []);
});

test("install, configure, enable, and discovery remain four independent operations", async () => {
  const calls = [];
  const confirmations = [];
  const toasts = [];
  let registryChanges = 0;
  let current = managedItem();
  const controller = createAutomationToolCatalogController({
    state: {},
    request: async (path, options = {}) => {
      calls.push([path, options]);
      if (path === "/api/optional-tools/automation") return [clone(current)];
      if (path.endsWith("/install")) {
        current = managedItem({ installed: true, installedVersion: "1.2.3", canInstall: false, canConfigure: true });
        return clone(current);
      }
      if (path.endsWith("/configure")) {
        current = managedItem({ installed: true, installedVersion: "1.2.3", configured: true, mcpServerId: "server/1", enabled: false, canInstall: false, canConfigure: false, canEnable: true });
        return clone(current);
      }
      if (path === "/api/mcp/servers/server%2F1" && options.method === "PATCH") {
        current = { ...current, enabled: JSON.parse(options.body).enabled };
        return { id: "server/1", enabled: current.enabled };
      }
      if (path === "/api/mcp/servers/server%2F1/tools") return { count: 1, tools: [{ name: "browse" }] };
      throw new Error(`unexpected request ${path}`);
    },
    confirmAction: async (message) => { confirmations.push(message); return true; },
    onRegistryChanged: async () => { registryChanges += 1; },
    showToast: (message, tone) => toasts.push([message, tone]),
  });

  await controller.load();
  calls.length = 0;

  assert.equal(await controller.install("browser-mcp"), true);
  assert.deepEqual(calls.map(([path]) => path), [
    "/api/optional-tools/automation/browser-mcp/install",
    "/api/optional-tools/automation",
  ]);
  assert.equal(controller.snapshot().items[0].installed, true);
  assert.equal(controller.snapshot().items[0].configured, false);
  assert.equal(controller.snapshot().items[0].enabled, false);
  assert.equal(registryChanges, 0);
  assert.equal(confirmations.length, 1);
  assert.match(confirmations[0], /@official\/browser-mcp@1\.2\.3/);
  assert.match(confirmations[0], /C:\/Autoto\/tools\/browser-mcp/);
  assert.match(confirmations[0], /不会修改 PATH.*授予 Agent 权限/s);
  assert.equal(calls.some(([path, options]) => path.includes("/configure") || path.includes("/tools") || options.method === "PATCH"), false);

  calls.length = 0;
  assert.equal(await controller.configure("browser-mcp"), true);
  assert.deepEqual(calls.map(([path]) => path), [
    "/api/optional-tools/automation/browser-mcp/configure",
    "/api/optional-tools/automation",
  ]);
  assert.equal(controller.snapshot().items[0].configured, true);
  assert.equal(controller.snapshot().items[0].enabled, false, "configure response remains disabled");
  assert.equal(registryChanges, 1);
  assert.equal(confirmations.length, 2);
  assert.match(confirmations[1], /保持停用.*不会.*授予 Agent 权限/s);
  assert.equal(calls.some(([, options]) => options.method === "PATCH"), false, "configure must not enable");

  calls.length = 0;
  assert.equal(await controller.setEnabled("browser-mcp", true), true);
  assert.equal(confirmations.length, 3, "install, configure, and enable require separate confirmations");
  assert.match(confirmations[2], /浏览器数据.*一次性人工审批/);
  assert.deepEqual(calls.map(([path]) => path), [
    "/api/mcp/servers/server%2F1",
    "/api/optional-tools/automation",
  ]);
  assert.deepEqual(JSON.parse(calls[0][1].body), { enabled: true });
  assert.equal(registryChanges, 2);
  assert.equal(calls.some(([path]) => path.endsWith("/tools")), false, "enable must not discover tools");

  calls.length = 0;
  assert.equal(await controller.discover("browser-mcp"), true);
  assert.deepEqual(calls.map(([path]) => path), ["/api/mcp/servers/server%2F1/tools"]);
  assert.equal(controller.snapshot().discovery["browser-mcp"].tools[0].name, "browse");
  assert.equal(registryChanges, 2, "discovery does not refresh the registry");
  assert.equal(toasts.length, 4);
});

test("external and capability cards expose only a fixed HTTPS official-page action", async () => {
  const requests = [];
  const opened = [];
  const external = managedItem({
    id: "desktop-browser",
    name: "Official Browser App",
    kind: "external",
    installMode: "external",
    packageName: "",
    installUrl: "https://browser.example.com/download",
    managedPath: "",
    canInstall: true,
    canConfigure: true,
    canEnable: true,
  });
  const capability = { ...external, id: "browser-capability", kind: "capability", installMode: "capability" };
  const controller = createAutomationToolCatalogController({
    state: { automationToolCatalogItems: [external, capability], automationToolCatalogLoaded: true },
    request: async (...args) => { requests.push(args); },
    confirmAction: async () => true,
    openExternal: (url) => opened.push(url),
  });

  const markup = controller.render();
  assert.match(markup, /Official Browser App/);
  assert.doesNotMatch(markup, /data-automation-install=/);
  assert.doesNotMatch(markup, /data-automation-configure=/);
  assert.doesNotMatch(markup, /data-automation-enable=/);
  assert.doesNotMatch(markup, /data-automation-discover=/);
  assert.match(markup, /安装应用不等于 Agent 已连接/);
  assert.equal(await controller.install("desktop-browser"), false);
  assert.equal(await controller.configure("desktop-browser"), false);
  assert.equal(await controller.install("browser-capability"), false);
  assert.equal(await controller.configure("browser-capability"), false);
  assert.equal(controller.openOfficial("desktop-browser"), true);
  assert.deepEqual(requests, []);
  assert.deepEqual(opened, ["https://browser.example.com/download"]);
});

test("dynamic catalog content is escaped and dangerous URL schemes never open", () => {
  const opened = [];
  const malicious = managedItem({
    id: `bad"><img src=x onerror=alert(1)>`,
    packageName: `<script>alert("package")</script>`,
    publisher: `<img src=x onerror=alert("publisher")>`,
    managedPath: `C:/<svg onload=alert(1)>`,
    installUrl: "javascript:alert(1)",
    docsUrl: "data:text/html,<script>alert(1)</script>",
    sourceUrl: "http://insecure.example.com/source",
    prerequisites: [{ id: "<b>runtime</b>", available: false, detail: "<script>detail</script>" }],
  });
  const controller = createAutomationToolCatalogController({
    state: { automationToolCatalogItems: [malicious], automationToolCatalogLoaded: true },
    openExternal: (url) => opened.push(url),
  });

  const markup = controller.render();
  assert.doesNotMatch(markup, /<script>|<img src=x|<svg onload|<b>runtime/);
  assert.match(markup, /&lt;script&gt;/);
  assert.match(markup, /&lt;img src=x/);
  assert.match(markup, /&lt;svg onload/);
  assert.match(markup, /用途.*浏览器自动化/);
  assert.match(markup, /可接触的数据.*页面内容与可访问性快照/);
  assert.match(markup, /默认安全配置.*固定使用系统 Chrome/);
  assert.match(markup, /风险边界.*一次性人工审批|风险边界.*可能导航/);
  assert.equal(safeOfficialHTTPSURL(malicious), "");
  assert.equal(controller.openOfficial(malicious.id), false);
  assert.deepEqual(opened, []);
});

test("catalog copy has aligned keys and critical Simplified Chinese, Traditional Chinese, and English wording", () => {
  const expected = flattenKeys(skillsMessages["zh-CN"].skillsWorkbench.automationCatalog);
  assert.deepEqual(flattenKeys(skillsMessages["zh-TW"].skillsWorkbench.automationCatalog), expected);
  assert.deepEqual(flattenKeys(skillsMessages.en.skillsWorkbench.automationCatalog), expected);

  const confirmationData = { name: "Browser", source: "https://source.example", packageReference: "pkg@1.2.3", installUrl: "https://install.example", managedPath: "C:/managed", serverId: "server-1" };
  assert.match(t("skillsWorkbench.automationCatalog.externalBoundary", {}, "zh-CN"), /安装应用不等于 Agent 已连接/);
  assert.match(t("skillsWorkbench.automationCatalog.confirmInstall", confirmationData, "zh-CN"), /精确包版本：pkg@1\.2\.3.*不会修改 PATH.*授予 Agent 权限/s);
  assert.match(t("skillsWorkbench.automationCatalog.confirmConfigure", confirmationData, "zh-CN"), /保持停用.*不会.*授予 Agent 权限/s);
  assert.match(t("skillsWorkbench.automationCatalog.confirmEnable", confirmationData, "zh-CN"), /浏览器数据.*外部副作用.*一次性人工审批/);
  assert.match(t("skillsWorkbench.automationCatalog.externalBoundary", {}, "zh-TW"), /安裝應用程式不等於 Agent 已連線/);
  assert.match(t("skillsWorkbench.automationCatalog.confirmInstall", confirmationData, "zh-TW"), /精確套件版本：pkg@1\.2\.3.*不會修改 PATH.*授予 Agent 權限/s);
  assert.match(t("skillsWorkbench.automationCatalog.confirmConfigure", confirmationData, "zh-TW"), /保持停用.*不會.*授予 Agent 權限/s);
  assert.match(t("skillsWorkbench.automationCatalog.confirmEnable", confirmationData, "zh-TW"), /瀏覽器資料.*外部副作用.*一次性人工審批/);
  assert.match(t("skillsWorkbench.automationCatalog.externalBoundary", {}, "en"), /does not mean the Agent is connected/);
  assert.match(t("skillsWorkbench.automationCatalog.confirmInstall", confirmationData, "en"), /Exact package version: pkg@1\.2\.3.*will not modify PATH.*grant Agent permissions/is);
  assert.match(t("skillsWorkbench.automationCatalog.confirmConfigure", confirmationData, "en"), /remains disabled.*will not.*grant Agent permissions/is);
  assert.match(t("skillsWorkbench.automationCatalog.confirmEnable", confirmationData, "en"), /browser data.*external side effects.*one-time human approval/i);
  assert.match(t("skillsWorkbench.automationCatalog.dataAccess", {}, "zh-CN"), /可接触的数据/);
  assert.match(t("skillsWorkbench.automationCatalog.safetyDefaults", {}, "zh-TW"), /預設安全設定/);
  assert.match(t("skillsWorkbench.automationCatalog.riskBrowserSideEffects", {}, "en"), /navigate.*click.*type/i);
});

test("skills workbench renders and binds the catalog before the real MCP registry", () => {
  let binds = 0;
  const controller = createSkillsWorkbenchController({
    state: {
      activeSkillTab: "mcp-tools",
      serverSkills: [],
      serverSkillsStatus: "ready",
      mcpRegistryServers: [],
    },
    currentSkillsPreferences: () => ({ commands: [], mcpServers: [], toolPolicy: {} }),
    renderAutomationToolCatalog: () => '<section data-test="automation-catalog"></section>',
    renderMCPRegistryList: () => '<div data-test="mcp-registry"></div>',
    bindAutomationToolCatalog: () => { binds += 1; },
    bindMCPRegistryActions: () => {},
    isMCPRegistryActionBusy: () => false,
  });
  const markup = controller.renderSkillSettingsContent("mcp-tools");
  assert.ok(markup.indexOf('data-test="automation-catalog"') < markup.indexOf("后端 MCP registry"));

  const previousDocument = globalThis.document;
  const body = { querySelectorAll: () => [] };
  globalThis.document = { getElementById: (id) => id === "settingsContentBody" ? body : null };
  try {
    controller.bindSkillTabs("mcp-tools");
  } finally {
    globalThis.document = previousDocument;
  }
  assert.equal(binds, 1);
});
