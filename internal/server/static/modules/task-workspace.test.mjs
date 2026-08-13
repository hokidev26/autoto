import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { readStylesSource } from "./styles-source-helper.mjs";
import {
  createTaskWorkspaceController,
  flattenTaskWorkspace,
  normalizeTaskWorkspace,
  renderTaskWorkspace,
} from "./task-workspace.mjs";

function workspaceFixture() {
  return {
    projects: [
      {
        id: "project-1",
        name: "Alpha",
        gitPath: "/workspace/alpha",
        counts: { todo: 1, doing: 1, blocked: 0, done: 0, total: 2 },
        agents: [
          {
            id: "agent-1",
            title: "Planner",
            status: "running",
            model: "model-a",
            counts: { todo: 1, doing: 1, blocked: 0, done: 0, total: 2 },
            tasks: [
              { id: "task-1", text: "Plan release", status: "todo", protected: true, revision: 3, position: 0, sourceType: "spec" },
              { id: "task-2", text: "Check tests", status: "doing", revision: 1, position: 1 },
            ],
            systemPrompt: "must not render",
          },
          {
            id: "agent-2",
            title: "Builder",
            status: "idle",
            counts: { total: 0 },
            tasks: [],
          },
        ],
      },
      {
        id: "project-2",
        name: "Beta",
        counts: { done: 1, total: 1 },
        agents: [
          {
            id: "agent-3",
            title: "Reviewer",
            tasks: [{ id: "task-3", text: "Review release", status: "done", revision: 1 }],
          },
        ],
      },
    ],
    summary: { projectCount: 2, agentCount: 3, todo: 1, doing: 1, blocked: 0, done: 1, total: 3 },
    contextSummary: "must not render",
  };
}

function mockElement() {
  return {
    innerHTML: "",
    classList: { toggle() {} },
    querySelector() { return null; },
    querySelectorAll() { return []; },
  };
}

test("normalizes projects, empty agents, tasks, and summary counts", () => {
  const workspace = normalizeTaskWorkspace(workspaceFixture());
  assert.equal(workspace.projects.length, 2);
  assert.equal(workspace.projects[0].agents.length, 2);
  assert.deepEqual(workspace.projects[0].agents[1].tasks, []);
  assert.equal(workspace.summary.projectCount, 2);
  assert.equal(workspace.summary.agentCount, 3);
  assert.equal(workspace.summary.total, 3);
  assert.equal(flattenTaskWorkspace(workspace).length, 3);
});

test("renders dispatch, project, agent load, and task inspector without sensitive fields", () => {
  const workspace = normalizeTaskWorkspace(workspaceFixture());
  const dispatch = renderTaskWorkspace({ workspace, scope: "dispatch", loaded: true, filterStatus: "active" });
  assert.match(dispatch, /跨项目任务调度/);
  assert.match(dispatch, /Plan release/);
  assert.doesNotMatch(dispatch, /Review release/);
  assert.doesNotMatch(dispatch, /must not render/);

  const project = renderTaskWorkspace({ workspace, scope: "project", projectId: "project-1", loaded: true, selectedTaskKey: "agent-1::task-1" });
  assert.match(project, /Planner/);
  assert.match(project, /Builder/);
  assert.match(project, /任务详情/);
  assert.match(project, /重新分配/);
});

test("controller loads workspace and creates tasks through existing agent task API", async () => {
  const calls = [];
  const controller = createTaskWorkspaceController({
    host: mockElement(),
    kanbanHost: mockElement(),
    scopeHost: mockElement(),
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/api/task-workspace") return workspaceFixture();
      return {};
    },
  });

  assert.equal(await controller.load(), true);
  assert.equal(controller.getState().workspace.summary.agentCount, 3);
  assert.equal(await controller.createTask({ agentId: "agent-2", text: "Implement API", status: "doing", protected: false }), true);
  assert.equal(calls.some((call) => call.path === "/api/agents/agent-2/spec/tasks" && call.options.method === "POST"), true);
  const createCall = calls.find((call) => call.path === "/api/agents/agent-2/spec/tasks");
  assert.deepEqual(JSON.parse(createCall.options.body), { text: "Implement API", status: "doing", protected: false });
});

test("controller updates status and reassigns tasks within the selected project", async () => {
  const calls = [];
  let confirmations = 0;
  const controller = createTaskWorkspaceController({
    host: mockElement(),
    kanbanHost: mockElement(),
    scopeHost: mockElement(),
    confirmAction: async () => { confirmations += 1; return true; },
    request: async (path, options = {}) => {
      calls.push({ path, options });
      if (path === "/api/task-workspace") return workspaceFixture();
      return {};
    },
  });

  await controller.load();
  assert.equal(controller.selectTask("agent-1", "task-1"), true);
  assert.equal(await controller.updateSelectedTask("blocked"), true);
  const updateCall = calls.find((call) => call.path === "/api/agents/agent-1/spec/tasks/task-1" && call.options.method === "PATCH");
  assert.deepEqual(JSON.parse(updateCall.options.body), {
    text: "Plan release",
    status: "blocked",
    protected: true,
    expectedRevision: 3,
    acknowledgeProtected: true,
  });
  assert.equal(confirmations, 2);

  assert.equal(controller.selectTask("agent-1", "task-1"), true);
  assert.equal(await controller.reassignSelectedTask("agent-2"), true);
  const assignCall = calls.find((call) => call.path === "/api/agents/agent-1/spec/tasks/task-1/assign");
  assert.deepEqual(JSON.parse(assignCall.options.body), {
    targetAgentId: "agent-2",
    expectedRevision: 3,
    acknowledgeProtected: true,
  });
  assert.equal(confirmations, 4);
});

test("controller rejects project and agent scopes without matching context", async () => {
  const controller = createTaskWorkspaceController({
    host: mockElement(),
    kanbanHost: mockElement(),
    scopeHost: mockElement(),
    request: async () => workspaceFixture(),
  });
  await controller.load();
  assert.equal(controller.setScope("project"), false);
  assert.equal(controller.setScope("agent"), false);
  controller.setContext({ projectId: "project-1", agentId: "agent-1" });
  assert.equal(controller.setScope("project"), true);
  assert.equal(controller.setScope("agent"), true);
});

test("static shell mounts the three-level task workspace in the main workbench", async () => {
  const [indexHTML, appMain, styles, appEntry] = await Promise.all([
    readFile(new URL("../index.html", import.meta.url), "utf8"),
    readFile(new URL("./app-main.mjs", import.meta.url), "utf8"),
    readStylesSource(new URL("../styles.css", import.meta.url)),
    readFile(new URL("../app.js", import.meta.url), "utf8"),
  ]);
  const workbenchMarkup = indexHTML.slice(indexHTML.indexOf('id="workbenchPanel"'), indexHTML.indexOf('id="terminalPanel"'));
  const ids = [...indexHTML.matchAll(/\bid="([^"]+)"/g)].map((match) => match[1]);

  assert.match(workbenchMarkup, /id="taskWorkspaceScopes"/);
  assert.match(workbenchMarkup, /id="taskWorkspaceOverview"/);
  assert.match(workbenchMarkup, /id="projectKanbanBody" class="project-kanban-body hidden"/);
  assert.doesNotMatch(indexHTML, /employeeOverviewModal|employeeOverviewBody/);
  assert.equal(new Set(ids).size, ids.length);
  assert.match(appMain, /createTaskWorkspaceController/);
  assert.match(appMain, /taskWorkspace\.load/);
  assert.match(styles, /\.task-workspace-scopes/);
  assert.match(styles, /\.task-workspace-inspector/);
  const workspaceViewRule = styles.match(/\.task-workspace-view\s*\{([^}]*)\}/)?.[1] || "";
  assert.match(workspaceViewRule, /width:\s*100%/);
  assert.match(workspaceViewRule, /margin:\s*0/);
  assert.doesNotMatch(workspaceViewRule, /1280px|auto/);
});
