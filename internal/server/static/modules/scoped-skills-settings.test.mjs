import test from "node:test";
import assert from "node:assert/strict";

import {
  bindScopedSkillsManager,
  effectiveOwnerRows,
  renderEffectiveCommandsView,
  renderScopedSkillsManager,
} from "./scoped-skills-settings.mjs";

function bucket(overrides = {}) {
  return {
    context: { scope: "global", projectId: "", worklineId: "" },
    items: [],
    nextCursor: "",
    snapshotSequence: 1,
    status: "ready",
    error: "",
    revisions: {},
    mutations: {},
    importPreview: { status: "idle", error: "", requestSequence: 0, content: "", value: null },
    drafts: { create: {}, edits: {}, importContent: "" },
    drawer: null,
    ...overrides,
  };
}

class FakeNode {
  constructor({ dataset = {}, value = "", selectors = {} } = {}) {
    this.dataset = dataset;
    this.value = value;
    this.selectors = selectors;
    this.listeners = new Map();
  }

  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) || [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type, listener) {
    this.listeners.set(type, (this.listeners.get(type) || []).filter((item) => item !== listener));
  }

  querySelector(selector) {
    return (this.selectors[selector] || [])[0] || null;
  }

  querySelectorAll(selector) {
    return this.selectors[selector] || [];
  }

  dispatch(type, event = {}) {
    return Promise.all((this.listeners.get(type) || []).map((listener) => listener({ target: this, preventDefault() {}, ...event })));
  }
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

test("scoped manager renders explicit contexts, empty project state, escaped scans, and revision UI", () => {
  const projectBucket = bucket({
    context: { scope: "project", projectId: "p-1", worklineId: "" },
    items: [{
      id: "s-1",
      scope: "project",
      projectId: "p-1",
      command: "/review<script>",
      description: "desc & detail",
      prompt: "prompt <unsafe>",
      detailLoaded: true,
      enabled: false,
      scanVerdict: "review",
      scanFindings: [{ code: "network", message: "URL <external>" }],
      updatedAt: "v1",
    }],
    drawer: { skillId: "s-1", selectedRevision: "1", revisionDetail: { prompt: "old <prompt>" } },
    revisions: { "s-1": { status: "ready", items: [{ revisionNo: 1, createdAt: "earlier" }], nextCursor: "" } },
  });
  const controller = { ensureContext: () => projectBucket };
  const markup = renderScopedSkillsManager({ controller, context: { scope: "project", projectId: "p-1" } });
  assert.match(markup, /项目作用域 · p-1/);
  assert.match(markup, /data-scoped-skill-edit="s-1"/);
  assert.match(markup, /data-scoped-skill-toggle="s-1"/);
  assert.match(markup, /data-scoped-skill-delete="s-1"/);
  assert.match(markup, /data-scoped-revision-restore="1"/);
  assert.match(markup, /network/);
  assert.match(markup, /&lt;external&gt;/);
  assert.doesNotMatch(markup, /<script>/);
  assert.doesNotMatch(markup, /prompt <unsafe>/);
  assert.doesNotMatch(markup, /desc &amp; detail|desc &amp;/);

  const empty = renderScopedSkillsManager({ controller, context: { scope: "project", projectId: "" } });
  assert.match(empty, /data-scoped-skills-empty="project"/);
  assert.match(empty, /请先选择项目/);
  assert.doesNotMatch(empty, /data-scoped-skill-create-form/);
});

test("commands helper unwraps effective owners as read-only rows without mutation controls", () => {
  const response = {
    items: [
      { owner: { id: "workspace-owner", command: "/review", description: "workspace", scope: "workspace", enabled: false, scanVerdict: "safe" } },
      { effectiveOwner: { id: "global-owner", command: "/summary", scope: "global", enabled: true, scanVerdict: "review" } },
    ],
  };
  assert.deepEqual(effectiveOwnerRows(response), [
    { id: "workspace-owner", command: "/review", description: "workspace", scope: "workspace", enabled: false, scanVerdict: "safe", readOnly: true },
    { id: "global-owner", command: "/summary", description: "", scope: "global", enabled: true, scanVerdict: "review", readOnly: true },
  ]);
  const markup = renderEffectiveCommandsView(response);
  assert.match(markup, /data-effective-commands-readonly/);
  assert.match(markup, /只读 owner/);
  assert.doesNotMatch(markup, /data-scoped-skill-toggle/);
  assert.doesNotMatch(markup, /data-scoped-skill-delete/);
  assert.doesNotMatch(markup, /无描述|noDescription/);
});

test("binder preserves an edited draft when CAS update conflicts", async () => {
  const state = bucket({
    items: [{ id: "s-1", command: "/old", name: "Old", description: "old", prompt: "old prompt", detailLoaded: true, updatedAt: "v1", scanVerdict: "safe" }],
    editingSkillId: "s-1",
    drafts: { create: {}, edits: { "s-1": { name: "Old", command: "/old", description: "old", prompt: "old prompt" } }, importContent: "" },
  });
  const commandField = new FakeNode({ dataset: { scopedEditField: "command" }, value: "/draft" });
  const form = new FakeNode({
    dataset: { scopedSkillEditForm: "s-1" },
    selectors: { "[data-scoped-edit-field]": [commandField] },
  });
  const root = new FakeNode({ selectors: { "[data-scoped-skill-edit-form]": [form] } });
  const conflict = Object.assign(new Error("skill was updated by another client"), { status: 409 });
  let reported = null;
  let updatePayload = null;
  const controller = {
    ensureContext: () => state,
    updateSkill: async (_id, payload, context) => {
      updatePayload = { payload, context };
      throw conflict;
    },
  };
  const unbind = bindScopedSkillsManager(root, {
    controller,
    context: { scope: "project", projectId: "p-1" },
    onError: (error) => { reported = error; },
  });
  await commandField.dispatch("input");
  await form.dispatch("submit");
  await flush();
  assert.equal(updatePayload.payload.command, "/draft");
  assert.equal(updatePayload.payload.expectedUpdatedAt, "v1");
  assert.deepEqual(updatePayload.context, { scope: "project", projectId: "p-1", worklineId: "" });
  assert.equal(state.drafts.edits["s-1"].command, "/draft");
  assert.equal(reported, conflict);
  unbind();
  assert.equal(commandField.listeners.get("input").length, 0);
});

test("binder submits create drafts to the fixed workspace context and clears only after success", async () => {
  const state = bucket({ context: { scope: "workspace", projectId: "p-1", worklineId: "w-1" } });
  const command = new FakeNode({ dataset: { scopedCreateField: "command" }, value: "/created" });
  const prompt = new FakeNode({ dataset: { scopedCreateField: "prompt" }, value: "created prompt" });
  const form = new FakeNode();
  const root = new FakeNode({
    selectors: {
      "[data-scoped-skill-create-form]": [form],
      "[data-scoped-create-field]": [command, prompt],
    },
  });
  let request = null;
  const controller = {
    ensureContext: () => state,
    createSkill: async (payload, context) => {
      request = { payload, context };
      return { id: "created" };
    },
  };
  bindScopedSkillsManager(root, { controller, context: state.context });
  await command.dispatch("input");
  await prompt.dispatch("input");
  await form.dispatch("submit");
  await flush();
  assert.equal(request.payload.command, "/created");
  assert.equal(request.payload.prompt, "created prompt");
  assert.equal(request.payload.enabled, false);
  assert.deepEqual(request.context, state.context);
  assert.deepEqual(state.drafts.create, {});
});
