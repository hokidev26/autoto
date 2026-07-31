import test from "node:test";
import assert from "node:assert/strict";

import { createSkillsConfigCenter } from "./skills-config-center.mjs";
import { createSkillsWorkbenchController } from "./skills-workbench.mjs";

function skillsController() {
  const buckets = new Map();
  return {
    ensureContext(context) {
      const key = `${context.scope}:${context.projectId || ""}:${context.worklineId || ""}`;
      if (!buckets.has(key)) buckets.set(key, { status: "ready", items: [], revisions: {}, mutations: {}, snapshotSequence: 1 });
      return buckets.get(key);
    },
    load: async () => true,
  };
}

function tick() { return new Promise((resolve) => setTimeout(resolve, 0)); }

function fakeNode(dataset = {}) {
  const handlers = new Map();
  return { dataset, handlers, addEventListener(type, handler) { handlers.set(type, handler); } };
}

function fakeRoot({ formSelector = "", form = null, nodes = {} } = {}) {
  const handlers = new Map();
  if (form) form.addEventListener = (type, handler) => handlers.set(type, handler);
  return {
    handlers,
    querySelector(selector) { return selector === formSelector ? form : null; },
    querySelectorAll(selector) { return nodes[selector] || []; },
  };
}

test("settings tab router renders every server-backed configuration page", () => {
  const controller = createSkillsWorkbenchController({
    state: { activeSkillTab: "commands", serverSkills: [], serverSkillsStatus: "ready" },
    request: async () => ({ items: [] }),
    skillsPhaseB: skillsController(),
    getProject: () => ({ id: "p-1" }),
    getWorkline: () => ({ id: "w-1" }),
    getAgent: () => ({ id: "a-1" }),
    currentSkillsPreferences: () => ({ commands: [], mcpServers: [], toolPolicy: {} }),
  });
  const expectations = {
    "global-skills": /data-scoped-skills-manager/,
    "project-skills": /data-skill-scope="project"/,
    "optional-tools": /data-optional-tools-panel/,
    subagents: /data-agent-role-panel/,
    "global-prompts": /data-prompt-panel="global_user"/,
    "system-prompts": /data-prompt-panel="system_extension"/,
    hooks: /data-hooks-panel/,
  };
  for (const [tab, pattern] of Object.entries(expectations)) assert.match(controller.renderSkillSettingsContent(tab), pattern, tab);
});

test("project skills and scoped targets fail closed without project or workline", () => {
  const center = createSkillsConfigCenter({
    request: async () => ({ items: [] }),
    skillsController: skillsController(),
    getProject: () => null,
    getWorkline: () => null,
    getAgent: () => null,
  });
  const project = center.renderTab("project-skills");
  assert.match(project, /data-scoped-skills-empty="project"/);
  assert.doesNotMatch(project, /data-skill-scope="global"/);
  assert.match(project, /请先选择项目/);

  const prompts = center.renderTab("global-prompts");
  assert.match(prompts, /data-prompt-panel="global_user"/);
  assert.match(prompts, /不可信用户上下文/);
  assert.match(prompts, /data-safe-empty="agent"/);
});

test("optional tools keep the title and scope control without explanatory copy", () => {
  const center = createSkillsConfigCenter({
    request: async (path) => path.endsWith("/catalog") ? { tools: [] } : path.includes("/rules?") ? { rules: [] } : { tools: [] },
    skillsController: skillsController(),
    getProject: () => ({ id: "p" }),
    getWorkline: () => ({ id: "w" }),
  });
  const html = center.renderTab("optional-tools");
  assert.match(html, /工具可见性与可用性/);
  assert.doesNotMatch(html, /allow \/ ask \/ deny|此页决定工具是否/);
  assert.match(html, /data-config-scope="optional"/);
  assert.doesNotMatch(html, /工具权限规则/);
});

test("role CRUD binding posts scoped definitions and preserves fixed boundary fields", async () => {
  const calls = [];
  const center = createSkillsConfigCenter({
    request: async (path, options = {}) => { calls.push({ path, options, body: options.body ? JSON.parse(options.body) : null }); return path.includes("?") ? { items: [] } : { id: "r-1", revision: 1 }; },
    skillsController: skillsController(),
    getProject: () => ({ id: "p-1" }),
    getWorkline: () => ({ id: "w-1" }),
    getAgent: () => ({ id: "a-1" }),
  });
  assert.match(center.renderTab("subagents"), /data-role-model-routing/);
  await tick();
  const form = { elements: {
    key: { value: "safe-review" }, displayName: { value: "Safe review" }, summary: { value: "Review" },
    baseRole: { value: "reviewer" }, roleExtension: { value: "<append only>" },
    toolAllowlist: { value: "Read\nGrep" }, deniedTools: { value: "Bash" }, readOnly: { checked: true }, disableExec: { checked: true },
  } };
  const root = fakeRoot({ formSelector: "[data-role-form]", form });
  center.bind(root, "subagents");
  root.handlers.get("submit")({ preventDefault() {}, currentTarget: form });
  await tick();
  const create = calls.find((call) => call.options.method === "POST" && call.path === "/api/agent-role-definitions");
  assert.ok(create);
  assert.equal(create.body.scope.scope, "global");
  assert.equal(create.body.definition.baseRole, "reviewer");
  assert.equal(create.body.definition.roleExtension, "<append only>");
  assert.deepEqual(create.body.definition.toolAllowlist, ["Grep", "Read"]);
  assert.equal(Object.hasOwn(create.body.definition, "basePrompt"), false);
});

test("紧凑配置卡片保留安全边界、信任提示与 Hook 历史控件", async () => {
  const malicious = `<img src=x onerror="boom">`;
  const historyButton = fakeNode({ hookHistory: "h-1" });
  const center = createSkillsConfigCenter({
    request: async (path) => {
      if (path.startsWith("/api/agent-role-definitions?")) return { items: [{ id: "r-1", key: "safe-review", displayName: malicious, summary: "普通角色摘要", revision: 1 }] };
      if (path.startsWith("/api/prompt-definitions?")) return { items: [
        { id: "p-user", key: "global-user", displayName: "Global user", summary: "普通提示摘要", layer: "global_user", revision: 1 },
        { id: "p-system", key: "system-extension", displayName: "System extension", summary: "普通系统摘要", layer: "system_extension", revision: 2 },
      ] };
      if (path === "/api/lifecycle-hooks/") return { hooks: [{ id: "h-1", name: malicious, description: "普通钩子描述", event: "tool.after", scope: { kind: "global" }, mode: "async", failurePolicy: "continue", action: { kind: "shell", shell: { executable: "audit-helper" }, secretConfigured: { TOKEN: true } }, revision: 2 }] };
      if (path.startsWith("/api/lifecycle-hooks/h-1/history?")) return { history: [{ execution: { id: "e-1", hookId: "h-1", status: "failed", error: "token=actual-secret" } }] };
      return { items: [] };
    },
    skillsController: skillsController(),
    getAgent: () => null,
  });

  center.renderTab("subagents");
  await tick();
  const roles = center.renderTab("subagents");
  assert.match(roles, /skill-config-card/);
  assert.match(roles, /skill-card-main/);
  assert.match(roles, /skill-card-meta/);
  assert.match(roles, /skill-card-actions/);
  assert.match(roles, /固定边界：不可覆盖 · 只能追加 · 权限只能收窄/);
  assert.match(roles, /data-role-model-routing/);
  assert.doesNotMatch(roles, /普通角色摘要/);
  assert.doesNotMatch(roles, /<img src=x/);
  assert.match(roles, /&lt;img src=x/);

  center.renderTab("global-prompts");
  await tick();
  const globalPrompts = center.renderTab("global-prompts");
  assert.match(globalPrompts, /不可信用户上下文，绝不作为 system/);
  assert.match(globalPrompts, /data-prompt-trust="untrusted_user"/);
  assert.match(globalPrompts, /skill-config-badge">user/);
  assert.match(globalPrompts, /skill-config-badge">untrusted/);
  assert.doesNotMatch(globalPrompts, /普通提示摘要/);

  const systemPrompts = center.renderTab("system-prompts");
  assert.match(systemPrompts, /受信任 system 扩展：只能追加，不可覆盖边界/);
  assert.match(systemPrompts, /data-prompt-trust="trusted_extension"/);
  assert.match(systemPrompts, /skill-config-badge">trusted/);
  assert.doesNotMatch(systemPrompts, /普通系统摘要/);

  center.renderTab("hooks");
  const hookRoot = fakeRoot({ nodes: { "[data-hook-history]": [historyButton] } });
  center.bind(hookRoot, "hooks");
  await tick();
  await historyButton.handlers.get("click")();
  const hooks = center.renderTab("hooks");
  assert.match(hooks, /data-hook-form/);
  assert.match(hooks, /data-hook-test="h-1"/);
  assert.match(hooks, /data-hook-history="h-1"/);
  assert.match(hooks, /data-hook-edit="h-1"/);
  assert.match(hooks, /data-hook-delete="h-1"/);
  assert.match(hooks, /已配置密钥 1/);
  assert.equal((hooks.match(/Secret 仅支持 env: 引用/g) || []).length, 1);
  assert.equal((hooks.match(/工具审批与审计网关/g) || []).length, 1);
  assert.match(hooks, /skill-history-row/);
  assert.match(hooks, /failed/);
  assert.match(hooks, /data-hook-cancel="e-1"/);
  assert.match(hooks, /data-hook-retry="e-1"/);
  assert.doesNotMatch(hooks, /普通钩子描述|env:PRIVATE|TOKEN|actual-secret/);
  assert.doesNotMatch(hooks, /<img src=x/);
});
