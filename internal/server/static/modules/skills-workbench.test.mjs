import test from "node:test";
import assert from "node:assert/strict";

import { currentUILocale, setUILocale } from "./i18n.mjs";
import { createSkillsContext } from "./skills-context.mjs";
import { createSkillsWorkbenchController, isCurrentRestoreReviewConflict, renderFileSourceCandidate, renderSkillRevisionDrawer, renderSkillScopeBadge, restoreRevisionWithCurrentRiskConfirmation, skillContextLabel, skillScopeShadowHint } from "./skills-workbench.mjs";

test("scope labels, badges, and owner shadow hints describe v2 ownership", () => {
  assert.equal(skillContextLabel({ scope: "global" }), "全局作用域");
  assert.equal(skillContextLabel({ scope: "project", projectId: "p-1" }), "项目作用域 · p-1");
  assert.match(renderSkillScopeBadge({ scope: "workspace" }), /工作线/);
  assert.equal(skillScopeShadowHint({ scope: "global" }, { scope: "workspace", worklineId: "w-1" }), "由全局作用域 owner 生效");
  assert.equal(skillScopeShadowHint({ shadowedBy: "workspace" }, { scope: "global" }), "已被更具体作用域的服务端 Skill 覆盖");
});

test("skills context accepts explicit project and workspace identifiers", () => {
  const state = {
    skillContextScope: "global",
    project: { id: "p-current" },
    workline: { id: "w-current" },
  };
  const warnings = [];
  const context = createSkillsContext({
    state,
    showToast: (message) => warnings.push(message),
    notifyTerminal() {},
    getSkillsPhaseB: () => null,
    getEffectiveSkillContext: () => ({ scope: "global" }),
  });
  assert.deepEqual(context.getSkillContext({ scope: "project", projectId: "p-explicit" }), {
    scope: "project", projectId: "p-explicit", worklineId: "",
  });
  assert.deepEqual(context.setSkillContext({ scope: "workspace", projectId: "p-explicit", worklineId: "w-explicit" }), {
    scope: "workspace", projectId: "p-explicit", worklineId: "w-explicit",
  });
  assert.equal(state.skillContextScope, "workspace");
  assert.deepEqual(warnings, []);
});

test("skills tabs expose a single selected tab and keyboard-friendly tab semantics", () => {
  const controller = createSkillsWorkbenchController({
    state: {
      activeSkillTab: "commands",
      serverSkills: [],
      serverSkillsSaving: false,
      serverSkillsStatus: "ready",
    },
    currentSkillsPreferences: () => ({ commands: [], mcpServers: [], toolPolicy: {} }),
  });
  const markup = controller.renderSkillSettingsContent("commands");
  assert.equal((markup.match(/role="tab"/g) || []).length, 11);
  assert.equal((markup.match(/aria-selected="true"/g) || []).length, 1);
  assert.equal((markup.match(/aria-controls="skill-tab-panel-/g) || []).length, 1);
  assert.match(markup, /aria-controls="skill-tab-panel-commands"/);
  assert.match(markup, /role="tabpanel"/);
  assert.match(markup, /settings-toolbar/);
});

test("scope labels, badges, and revision controls follow the active UI locale", () => {
  const previous = currentUILocale();
  try {
    setUILocale("en");
    assert.equal(skillContextLabel({ scope: "global" }), "Global scope");
    assert.equal(skillScopeShadowHint({ scope: "global" }, { scope: "workspace" }), "Owned by Global scope");
    assert.match(renderSkillScopeBadge({ scope: "workspace" }), /Workline/);
    assert.match(renderSkillRevisionDrawer({ drawer: {}, revisions: {} }), /Revision history/);

    setUILocale("zh-TW");
    assert.equal(skillContextLabel({ scope: "project", projectId: "p-1" }), "專案作用域 · p-1");
    assert.match(renderSkillScopeBadge({ scope: "workspace" }), /工作線/);
    assert.match(renderSkillRevisionDrawer({ drawer: {}, revisions: {} }), /修訂記錄/);
  } finally {
    setUILocale(previous);
  }
});

test("revision drawer renders revision actions and selected detail safely", () => {
  const markup = renderSkillRevisionDrawer({
    context: { scope: "project", projectId: "p-1" },
    drawer: { skillId: "s-1", selectedRevision: "2", revisionDetail: { prompt: "reviewed prompt" } },
    revisions: { items: [{ revisionNo: 2, label: "修订 2", createdAt: "2025-01-01" }], status: "ready" },
  });
  assert.match(markup, /data-skill-v2-restore="s-1"/);
  assert.match(markup, /data-skill-v2-revision-id="2"/);
  assert.match(markup, /reviewed prompt/);
  assert.match(markup, /项目作用域/);
  assert.match(markup, /settings-sheet/);
  assert.match(markup, /role="region"/);
  assert.doesNotMatch(markup, /aria-modal="true"/);
});

test("restore displays the structured current scan and retries with its content hash", async () => {
  const attempts = [];
  const contentHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
  const reviewConflict = Object.assign(new Error("review required"), {
    status: 409,
    body: {
      error: "conflict: restored review skill requires acknowledgement",
      code: "skill_restore_review_required",
      scanVerdict: "review",
      scanFindings: [{ code: "network_or_external_url", severity: "review", message: "Contains an external URL." }],
      contentHash,
      scannerVersion: 7,
    },
  });
  let confirmationMessage = "";
  const restored = await restoreRevisionWithCurrentRiskConfirmation(async (options) => {
    attempts.push(options);
    if (!options.acknowledgeRisk) throw reviewConflict;
    return { id: "s-1" };
  }, (message) => {
    confirmationMessage = message;
    return true;
  });
  assert.deepEqual(attempts, [
    { acknowledgeRisk: false, acknowledgedContentHash: "" },
    { acknowledgeRisk: true, acknowledgedContentHash: contentHash },
  ]);
  assert.deepEqual(restored, { id: "s-1" });
  assert.equal(isCurrentRestoreReviewConflict(reviewConflict), true);
  assert.match(confirmationMessage, /扫描器版本：7/);
  assert.match(confirmationMessage, /内容哈希：0123456789abcdef…/);
  assert.match(confirmationMessage, /network_or_external_url: Contains an external URL\./);
});

test("restore never retries stale, non-structured, or missing-hash conflicts", async () => {
  const conflicts = [
    Object.assign(new Error("skill was updated by another client"), {
      status: 409,
      body: { error: "conflict: skill was updated by another client" },
    }),
    Object.assign(new Error("restored review skill requires acknowledgeRisk"), {
      status: 409,
      body: { error: "conflict: restored review skill requires acknowledgeRisk" },
    }),
    Object.assign(new Error("review challenge missing hash"), {
      status: 409,
      body: {
        code: "skill_restore_review_required",
        scanVerdict: "review",
        scanFindings: [{ code: "review", severity: "review", message: "Review." }],
        contentHash: "",
        scannerVersion: 1,
      },
    }),
  ];
  for (const conflict of conflicts) {
    let calls = 0;
    let confirms = 0;
    await assert.rejects(restoreRevisionWithCurrentRiskConfirmation(async (options) => {
      calls += 1;
      assert.deepEqual(options, { acknowledgeRisk: false, acknowledgedContentHash: "" });
      throw conflict;
    }, () => {
      confirms += 1;
      return true;
    }));
    assert.equal(calls, 1);
    assert.equal(confirms, 0);
    assert.equal(isCurrentRestoreReviewConflict(conflict), false);
  }
});

test("cancelling a structured restore review challenge does not retry", async () => {
  const contentHash = "abcdef0123456789";
  const review = Object.assign(new Error("review required"), {
    status: 409,
    body: {
      code: "skill_restore_review_required",
      scanVerdict: "review",
      scanFindings: [{ code: "review", severity: "review", message: "Review this content." }],
      contentHash,
      scannerVersion: 1,
    },
  });
  const attempts = [];
  const result = await restoreRevisionWithCurrentRiskConfirmation(async (options) => {
    attempts.push(options);
    throw review;
  }, () => false);
  assert.equal(result, null);
  assert.deepEqual(attempts, [{ acknowledgeRisk: false, acknowledgedContentHash: "" }]);
});

test("file source cards show safe provenance, hashes, scan, conflict, and diagnostics without prompt or absolute root", () => {
  const markup = renderFileSourceCandidate({
    adapter: { id: "agents" },
    provenance: { rootId: "anonymous-root", adapterId: "agents", relativePath: ".agents/skills/review/SKILL.md" },
    relativePath: ".agents/skills/review/SKILL.md",
    sourceHash: "a".repeat(64),
    sidecarSourceHash: "b".repeat(64),
    conflictStatus: "none",
    skill: { command: "/review", prompt: "C:\\Users\\Secret\\must-not-render", allowedTools: ["Bash"] },
    scan: { verdict: "review", findings: [{ code: "network_or_external_url", severity: "review", message: "Contains an external URL." }] },
    diagnostics: [{ code: "allowed_tools_ignored", message: "Agent Skill allowed-tools metadata is untrusted and was ignored." }],
  }, { index: 3 });
  assert.match(markup, /agents/);
  assert.match(markup, /\.agents\/skills\/review\/SKILL\.md/);
  assert.match(markup, new RegExp("a{64}"));
  assert.match(markup, new RegExp("b{64}"));
  assert.match(markup, /network_or_external_url/);
  assert.match(markup, /allowed_tools_ignored/);
  assert.match(markup, /data-file-skill-source-import="3"/);
  assert.doesNotMatch(markup, /C:\\Users\\Secret/);
  assert.doesNotMatch(markup, /allowedTools/);
});

test("commands workbench renders one file source area with user/project selection and explicit import", () => {
  const candidate = {
    adapter: { id: "agents" },
    provenance: { adapterId: "agents", relativePath: ".agents/skills/one/SKILL.md" },
    relativePath: ".agents/skills/one/SKILL.md",
    sourceHash: "c".repeat(64),
    conflictStatus: "none",
    skill: { command: "/one" },
    scan: { verdict: "safe", findings: [] },
  };
  const sourceState = { candidates: [candidate], conflicts: [], diagnostics: [], truncated: false, status: "ready", error: "", importing: {} };
  const controller = createSkillsWorkbenchController({
    state: {
      activeSkillTab: "commands",
      serverSkills: [],
      serverSkillsSaving: false,
      serverSkillsStatus: "ready",
      skillFileSourceScope: "user",
    },
    currentSkillsPreferences: () => ({ commands: [], mcpServers: [], toolPolicy: {} }),
    getSkillContext: () => ({ scope: "project", projectId: "p-1" }),
    skillsPhaseB: {
      ensureContext: () => ({ status: "ready", items: [], revisions: {}, snapshotSequence: 1 }),
      ensureSources: () => sourceState,
      load: async () => {},
      loadSources: async () => sourceState,
    },
  });
  const markup = controller.renderSkillSettingsContent("commands");
  assert.equal((markup.match(/skills-file-sources-section/g) || []).length, 1);
  assert.match(markup, /id="fileSkillSourceScopeSelect"/);
  assert.match(markup, /value="user"/);
  assert.match(markup, /value="project"/);
  assert.match(markup, /data-file-skill-source-import="0"/);
  assert.match(markup, /allowed-tools/);
  assert.match(markup, /绝不授予工具权限/);
});
