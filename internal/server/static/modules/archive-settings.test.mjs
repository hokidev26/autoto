import test from "node:test";
import assert from "node:assert/strict";

import { setUILocale } from "./i18n.mjs?v=settings-flat-1-codex-browser-login-1-shared-api-1-apple-theme-1-settings-help-1-task-workspace-1-navigation-state-2-archive-1";
import { createArchiveSettingsController, normalizeArchivePayload } from "./archive-settings.mjs";

test("normalizeArchivePayload keeps archived project and conversation navigation fields", () => {
  assert.deepEqual(normalizeArchivePayload({
    projects: [{ id: "p-1", name: " Project ", gitPath: "/Users/test/project", archivedAt: "2026-07-18T00:00:00Z", pinned: 1 }],
    conversations: [{
      projectId: "p-1",
      projectName: "Project",
      projectPath: "/Users/test/project",
      worklineId: "w-1",
      worklineTitle: "Main",
      agentId: "a-1",
      agentTitle: " Conversation ",
      agentArchivedAt: "2026-07-18T00:00:00Z",
      projectArchivedAt: "2026-07-17T00:00:00Z",
      agentPinned: 1,
    }],
  }), {
    projects: [{ id: "p-1", name: "Project", gitPath: "/Users/test/project", archivedAt: "2026-07-18T00:00:00Z", pinned: true }],
    conversations: [{
      projectId: "p-1",
      projectName: "Project",
      projectPath: "/Users/test/project",
      worklineId: "w-1",
      worklineTitle: "Main",
      agentId: "a-1",
      agentTitle: "Conversation",
      agentArchivedAt: "2026-07-18T00:00:00Z",
      projectArchivedAt: "2026-07-17T00:00:00Z",
      agentPinned: true,
    }],
  });
});

test("archive settings loads archived records and restores agents", async () => {
  const calls = [];
  const refreshes = [];
  const request = async (path, options = {}) => {
    calls.push({ path, options });
    if (options.method === "PATCH") return {};
    return {
      projects: [{ id: "p-1", name: "Project", gitPath: "/tmp/project", archivedAt: "2026-07-18T00:00:00Z" }],
      conversations: [{ projectId: "p-1", projectName: "Project", worklineTitle: "Main", agentId: "a-1", agentTitle: "Chat", agentArchivedAt: "2026-07-18T00:00:00Z" }],
    };
  };
  const controller = createArchiveSettingsController({ request, refresh: () => refreshes.push(true) });

  await controller.load();
  setUILocale("en");
  try {
    const html = controller.render();
    assert.match(html, /Archived projects/);
    assert.match(html, /Archived conversations/);
    assert.match(html, /Project/);
    assert.match(html, /Chat/);
  } finally {
    setUILocale("zh-CN");
  }

  const button = {
    textContent: "恢复",
    disabled: false,
    dataset: {},
    setAttribute(name, value) { this[name] = value; },
    removeAttribute(name) { delete this[name]; },
  };
  await controller.restore("conversation", "a/1", button);
  assert.equal(calls[1].path, "/api/agents/a%2F1/navigation-state");
  assert.deepEqual(JSON.parse(calls[1].options.body), { archived: false });
  assert.ok(refreshes.length >= 2);
});

test("failed archive load does not auto-retry through refresh→render", async () => {
  let loads = 0;
  const errors = [];
  const controller = createArchiveSettingsController({
    request: async () => {
      loads += 1;
      const error = new Error("missing or invalid local API token");
      error.status = 401;
      throw error;
    },
    refresh: () => {
      // Mimic settings panel: every refresh re-renders the live page.
      controller.render();
    },
    showError: (error) => errors.push(error?.message || String(error)),
  });

  const first = controller.render();
  assert.match(first, /加载|Loading|loading/i);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(loads, 1);
  assert.deepEqual(errors, ["missing or invalid local API token"]);
  const failed = controller.render();
  assert.match(failed, /missing or invalid local API token/);
  assert.match(failed, /archiveRefreshBtn|刷新|Refresh/i);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(loads, 1, "failed load must not re-enter via render()");
});
