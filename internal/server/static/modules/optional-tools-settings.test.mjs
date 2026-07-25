import test from "node:test";
import assert from "node:assert/strict";

import {
  createOptionalToolsSettingsController,
  groupOptionalTools,
  normalizeOptionalToolsSettings,
} from "./optional-tools-settings.mjs";

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

test("optional tools normalize catalog rules, deleted inheritance, and orphans", () => {
  const items = normalizeOptionalToolsSettings(
    { tools: [{ name: "read", displayName: "Read", domain: "filesystem", description: "Read files" }] },
    { rules: [
      { id: "r-read", toolName: "read", state: "disabled", revision: 3, deletedAt: "2026-07-24T00:00:00Z" },
      { id: "r-old", toolName: "removed__tool", state: "disabled", revision: 2, orphan: true },
    ] },
    { tools: [
      { name: "read", enabled: true, state: "enabled", sourceScope: "project" },
      { name: "removed__tool", enabled: false, state: "disabled", domain: "orphan", orphan: true },
    ] },
  );
  const byName = Object.fromEntries(items.map((item) => [item.name, item]));
  assert.equal(byName.read.selection, "inherit");
  assert.equal(byName.read.ruleRevision, 3, "deleted head revision is retained for CAS restore");
  assert.equal(byName.read.effectiveEnabled, true);
  assert.equal(byName.removed__tool.selection, "disabled");
  assert.equal(byName.removed__tool.orphan, true);
});

test("optional tools group by domain and search directory metadata", () => {
  const items = [
    { name: "read", displayName: "Read", description: "Read files", domain: "filesystem", source: "core" },
    { name: "plugin__search", displayName: "Search API", description: "Remote lookup", domain: "plugins", source: "plugin" },
    { name: "bash", displayName: "Bash", description: "Run command", domain: "execution", source: "core" },
  ];
  assert.deepEqual(groupOptionalTools(items).map((group) => group.domain), ["execution", "filesystem", "plugins"]);
  assert.deepEqual(groupOptionalTools(items, "remote").map((group) => group.tools.map((item) => item.name)), [["plugin__search"]]);
  assert.deepEqual(groupOptionalTools(items, "FILESYSTEM").map((group) => group.tools.map((item) => item.name)), [["read"]]);
});

test("optional tools controller sends inherit/enabled/disabled CAS mutations", async () => {
  const calls = [];
  let loadCount = 0;
  const controller = createOptionalToolsSettingsController({
    target: { scope: "workspace", projectId: "p/1", workspaceId: "w 1" },
    request: async (path, options = {}) => {
      calls.push([path, options]);
      if (path.endsWith("/catalog")) return { tools: [{ name: "read", domain: "filesystem" }] };
      if (path.includes("/rules?") && !options.method) {
        loadCount += 1;
        return { rules: loadCount <= 1
          ? [{ id: "rule/1", toolName: "read", state: "disabled", revision: 4 }]
          : [{ id: "rule/1", toolName: "read", state: "disabled", revision: 5, deletedAt: "2026-07-24T00:00:00Z" }] };
      }
      if (path.includes("/effective?")) return { tools: [{ name: "read", enabled: true, state: "enabled" }] };
      if (path.endsWith("/rules/rule%2F1") && options.method === "DELETE") return { deleted: true };
      if (path.endsWith("/rules") && options.method === "PUT") return { id: "rule/1", revision: 5 };
      throw new Error(`unexpected ${path}`);
    },
  });

  await controller.load();
  assert.match(calls[1][0], /scope=workspace/);
  assert.match(calls[1][0], /projectId=p%2F1/);
  assert.match(calls[1][0], /workspaceId=w\+1/);
  await controller.setRule("read", "inherit");
  const deletion = calls.find(([, options]) => options.method === "DELETE");
  assert.equal(deletion[0], "/api/optional-tools/rules/rule%2F1");
  assert.deepEqual(JSON.parse(deletion[1].body), { expectedRevision: 4 });

  // Reloaded state keeps the deleted logical head so restore remains CAS-safe.
  await controller.setRule("read", "enabled");
  const update = calls.find(([, options]) => options.method === "PUT");
  assert.deepEqual(JSON.parse(update[1].body), {
    toolName: "read",
    scope: "workspace",
    projectId: "p/1",
    workspaceId: "w 1",
    state: "enabled",
    expectedRevision: 5,
  });

  const html = controller.render();
  assert.match(html, /data-tool-domain="filesystem"/);
  assert.match(html, /value="inherit"/);
  assert.match(html, /value="enabled"/);
  assert.match(html, /value="disabled"/);
});

test("optional tools explains remote 403 restrictions", async () => {
  const controller = createOptionalToolsSettingsController({
    request: async () => { throw Object.assign(new Error("403 Forbidden"), { status: 403 }); },
    labels: { forbidden: "当前远程会话没有可选工具管理权限（403）。" },
  });
  assert.equal(await controller.load(), false);
  assert.match(controller.render(), /没有可选工具管理权限/);
});

test("optional tools ignores stale load responses after scope changes", async () => {
  const first = [deferred(), deferred(), deferred()];
  const second = [deferred(), deferred(), deferred()];
  let call = 0;
  const controller = createOptionalToolsSettingsController({
    target: { scope: "global" },
    request: () => {
      const index = call++;
      return (index < 3 ? first[index] : second[index - 3]).promise;
    },
  });

  const oldLoad = controller.load();
  const newLoad = controller.setTarget({ scope: "project", projectId: "p2" });
  second[0].resolve({ tools: [{ name: "new_tool", domain: "core" }] });
  second[1].resolve({ rules: [] });
  second[2].resolve({ tools: [{ name: "new_tool", enabled: true }] });
  assert.equal(await newLoad, true);

  first[0].resolve({ tools: [{ name: "stale_tool", domain: "core" }] });
  first[1].resolve({ rules: [] });
  first[2].resolve({ tools: [{ name: "stale_tool", enabled: false }] });
  assert.equal(await oldLoad, false);
  assert.deepEqual(controller.snapshot().items.map((item) => item.name), ["new_tool"]);
  assert.deepEqual(controller.snapshot().target, { scope: "project", projectId: "p2", workspaceId: "" });
});
