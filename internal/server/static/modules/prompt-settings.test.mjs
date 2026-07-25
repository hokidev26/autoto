import test from "node:test";
import assert from "node:assert/strict";

import { createPromptSettingsController, normalizePromptDraft, promptLayerPresentation } from "./prompt-settings.mjs";
import { agentProfileMessage } from "./messages-agent-profile-settings.mjs";

function recorder() {
  const calls = [];
  return { calls, request: async (path, options = {}) => { calls.push({ path, options, body: options.body ? JSON.parse(options.body) : null }); return { items: [] }; } };
}

test("prompt 只允许 system_extension 与 global_user", () => {
  assert.equal(normalizePromptDraft({ key: "policy", displayName: "Policy", layer: "system_extension", content: "Bound changes." }).layer, "system_extension");
  assert.throws(() => normalizePromptDraft({ key: "root", displayName: "Root", layer: "platform", content: "Override." }), /layer/);
});

test("global_user 展示为不可信 user 而不是 system", () => {
  assert.deepEqual(promptLayerPresentation("global_user"), {
    role: "user", trust: "untrusted_user", warning: "Rendered as explicit untrusted user context, never system text.",
  });
  assert.equal(promptLayerPresentation("system_extension").role, "system");
  assert.match(agentProfileMessage("globalUserWarning", "zh-CN"), /不可信/);
});

test("prompt controller 分离 scope list、CAS 更新、修订恢复和 effective 预留调用", async () => {
  const record = recorder();
  const controller = createPromptSettingsController({ request: record.request });
  const draft = { key: "context", displayName: "Context", summary: "Team", layer: "global_user", content: "Use team style." };
  await controller.list({ scope: "workspace", projectId: "p", workspaceId: "w" });
  await controller.create({ scope: "global" }, draft);
  await controller.update("prompt/1", 2, { ...draft, content: "Updated." });
  await controller.restore("prompt/1", 3, 1);
  await controller.effective("agent/1");

  assert.match(record.calls[0].path, /scope=workspace/);
  assert.equal(record.calls[1].body.scope.scope, "global");
  assert.equal(record.calls[2].body.expectedRevision, 2);
  assert.deepEqual(record.calls[3].body, { expectedRevision: 3, sourceRevision: 1 });
  assert.match(record.calls[4].path, /agent%2F1/);
});
