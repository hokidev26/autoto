import test from "node:test";
import assert from "node:assert/strict";

import { createAgentRoleSettingsController, normalizeAgentRoleDraft, normalizeDefinitionScope } from "./agent-role-settings.mjs";

function requestRecorder() {
  const calls = [];
  return { calls, request: async (path, options = {}) => { calls.push({ path, options, body: options.body ? JSON.parse(options.body) : null }); return { ok: true }; } };
}

const draft = {
  key: "safe-review", displayName: "Safe review", summary: "Review only", baseRole: "reviewer",
  roleExtension: "Focus on validation.", toolAllowlist: ["Read", "Grep"], deniedTools: ["Bash"],
};

test("角色草案只能选择固定基础角色且不接受基础提示词覆盖", () => {
  assert.equal(normalizeAgentRoleDraft(draft).baseRole, "reviewer");
  assert.throws(() => normalizeAgentRoleDraft({ ...draft, baseRole: "custom-root" }), /canonical/);
  assert.throws(() => normalizeAgentRoleDraft({ ...draft, toolAllowlist: ["Read", "read"] }), /duplicate/);
  assert.throws(() => normalizeAgentRoleDraft({ ...draft, toolAllowlist: ["Bash"], deniedTools: ["bash"] }), /both allowed and denied/);
});

test("global project workspace scope 严格规范化", () => {
  assert.deepEqual(normalizeDefinitionScope({ scope: "global" }), { scope: "global" });
  assert.deepEqual(normalizeDefinitionScope({ scope: "project", projectId: "p" }), { scope: "project", projectId: "p" });
  assert.deepEqual(normalizeDefinitionScope({ scope: "workspace", projectId: "p", workspaceId: "w" }), { scope: "workspace", projectId: "p", workspaceId: "w" });
  assert.throws(() => normalizeDefinitionScope({ scope: "workspace", projectId: "p" }), /valid/);
});

test("角色 controller 发送版本化正文并使用 CAS 与恢复端点", async () => {
  const recorder = requestRecorder();
  const controller = createAgentRoleSettingsController({ request: recorder.request });
  await controller.create({ scope: "project", projectId: "p" }, draft);
  await controller.update("role/1", 3, { ...draft, toolAllowlist: ["Read"] });
  await controller.restore("role/1", 4, 2);
  await controller.effective("agent 1");

  assert.equal(recorder.calls[0].body.definition.version, 1);
  assert.equal(Object.hasOwn(recorder.calls[0].body.definition, "basePrompt"), false);
  assert.equal(recorder.calls[1].body.expectedRevision, 3);
  assert.equal(recorder.calls[2].body.sourceRevision, 2);
  assert.match(recorder.calls[3].path, /agent%201/);
});
