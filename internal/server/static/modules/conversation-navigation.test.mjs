import test from "node:test";
import assert from "node:assert/strict";

import {
  addRecentConversation,
  applyConversationOrder,
  applyProjectOrder,
  buildNavigationView,
  createNavigationRefreshController,
  createNavigationTargetId,
  createRecentConversationSyncController,
  aggregateNavigationAgentStatus,
  conversationDisplayTitle,
  conversationHoverTitle,
  formatCompactRelativeTime,
  navigationAgentStatusClass,
  navigationRefreshDefaults,
  normalizeNavigationPayload,
  normalizeRecentConversations,
  renderNavigationHTML,
  renderRecentConversationsHTML,
  resolveInitialNavigationTarget,
  resolveTopNavigationProjectId,
} from "./conversation-navigation.mjs";
import { localPreferenceBackupKeys, recentConversationsKey } from "./preferences-data.mjs";

// Conversations created before titles were derived from the first message stored
// their working directory in the title column, so the sidebar row showed a path
// on both lines. The stored value is left alone; only the display changes.
test("標題被存成路徑時，側欄顯示資料夾名稱而不是整條路徑", () => {
  assert.equal(conversationDisplayTitle({ agentTitle: "C:\\Users\\dev\\Desktop\\autoto" }), "autoto");
  assert.equal(conversationDisplayTitle({ agentTitle: "C:\\Users\\dev\\Desktop\\火影忍者 蠍" }), "火影忍者 蠍");
  // A drive root has no folder name to fall back to, so the original stands.
  assert.equal(conversationDisplayTitle({ agentTitle: "C:\\" }), "C:\\");
  // POSIX and UNC forms too.
  assert.equal(conversationDisplayTitle({ agentTitle: "/home/ray/projects/autoto" }), "autoto");
  assert.equal(conversationDisplayTitle({ agentTitle: "\\\\server\\share\\work" }), "work");
  // A trailing separator must not yield an empty title.
  assert.equal(conversationDisplayTitle({ agentTitle: "/home/ray/projects/autoto/" }), "autoto");
});

test("formatCompactRelativeTime writes Cursor-style compact ages", () => {
  const now = Date.parse("2026-01-03T12:00:00Z");
  assert.equal(formatCompactRelativeTime("2026-01-03T11:59:30Z", now), "now");
  assert.equal(formatCompactRelativeTime("2026-01-03T11:29:00Z", now), "31m");
  assert.equal(formatCompactRelativeTime("2026-01-03T09:00:00Z", now), "3h");
  assert.equal(formatCompactRelativeTime("2026-01-01T12:00:00Z", now), "2d");
  assert.equal(formatCompactRelativeTime("", now), "");
  assert.equal(formatCompactRelativeTime("not-a-date", now), "");
});

test("真正的標題不會被誤判成路徑", () => {
  // These mention a slash or a colon but are not paths.
  for (const title of [
    "AGENTS.md规则与记忆检索评估",
    "fix a/b test handling",
    "修正 input/output 統計",
    "Fork of main",
    "ratio 16:9 的版面",
  ]) {
    assert.equal(conversationDisplayTitle({ agentTitle: title }), title);
  }
  // Missing or empty stays empty rather than becoming a stray fallback.
  assert.equal(conversationDisplayTitle({}), "");
  assert.equal(conversationDisplayTitle({ agentTitle: "   " }), "");
  assert.equal(conversationDisplayTitle(null), "");
});

test("專案列顯示資料夾名稱，路徑只出現在滑鼠提示", () => {
  const gitPath = "C:\\Users\\Ray\\Desktop\\autoto專案\\autoto";
  const view = buildNavigationView({
    projects: [{ id: "p1", name: gitPath, gitPath }],
    conversations: [],
  }, { mode: "all" });
  const html = renderNavigationHTML(view);
  assert.match(html, /<span class="project-name">autoto<\/span>/);
  assert.ok(html.includes(`title="${gitPath}"`));
  assert.doesNotMatch(html, /<span class="project-name">C:\\Users/);
});

test("對話列滑鼠提示顯示 Git 分支，沒有分支時才顯示標題", () => {
  assert.equal(conversationHoverTitle({ worklineBranch: "autoto/fork-of-main-c39bc123", agentTitle: "讀取對話" }), "autoto/fork-of-main-c39bc123");
  assert.equal(conversationHoverTitle({ agentTitle: "主線" }), "主線");
  const html = renderNavigationHTML(buildNavigationView({
    projects: [{ id: "p1", name: "Alpha", gitPath: "/work/alpha" }],
    conversations: [
      { projectId: "p1", worklineId: "w1", worklineBranch: "autoto/fork-of-main-c39bc123", agentId: "a1", agentTitle: "讀取對話" },
      { projectId: "p1", worklineId: "w2", agentId: "a2", agentTitle: "主線" },
    ],
  }, { mode: "all" }));
  assert.match(html, /navigation-project-row[^>]*title="\/work\/alpha"/);
  assert.match(html, /navigation-conversation-row nested[^>]*title="autoto\/fork-of-main-c39bc123"/);
  assert.match(html, /navigation-conversation-row nested[^>]*title="主線"/);
});

class FakeTimers {
  constructor() {
    this.nextId = 1;
    this.tasks = [];
  }

  setTimeout(callback, delay) {
    const id = this.nextId++;
    this.tasks.push({ id, callback, delay });
    return id;
  }

  clearTimeout(id) {
    this.tasks = this.tasks.filter((task) => task.id !== id);
  }

  async runNext() {
    const task = this.tasks.shift();
    assert.ok(task, "expected a scheduled navigation refresh");
    task.callback();
    for (let index = 0; index < 6; index += 1) await Promise.resolve();
    return task;
  }

  nextDelay() {
    return this.tasks[0]?.delay;
  }
}

class FakeWindow {
  constructor() {
    this.listeners = new Map();
  }

  addEventListener(type, listener) {
    if (!this.listeners.has(type)) this.listeners.set(type, new Set());
    this.listeners.get(type).add(listener);
  }

  removeEventListener(type, listener) {
    this.listeners.get(type)?.delete(listener);
  }

  dispatch(type, event) {
    for (const listener of this.listeners.get(type) || []) listener(event);
  }
}

const payload = {
  projects: [
    { id: "p1", name: "Alpha", gitPath: "/work/alpha", updatedAt: "2026-01-01T00:00:00Z" },
    { id: "p2", name: "Beta", gitPath: "/work/beta" },
    { id: "invalid" },
  ],
  conversations: [
    {
      projectId: "p1", projectName: "Alpha", projectPath: "/work/alpha", projectUpdatedAt: "2026-01-01T00:00:00Z",
      worklineId: "w1", worklineTitle: "Feature login", worklineRole: "primary", worklineBranch: "feat/login", worklineUpdatedAt: "2026-01-02T00:00:00Z",
      agentId: "a1", agentTitle: "Planner", agentType: "primary", agentStatus: "idle", model: "claude-sonnet", permissionMode: "acceptEdits",
      cwd: "/work/alpha", messageCount: "12", lastActivityAt: "2026-01-03T00:00:00Z",
    },
    {
      projectId: "p1", projectName: "Alpha", projectPath: "/work/alpha",
      worklineId: "w2", worklineTitle: "Docs", worklineBranch: "docs",
      agentId: "a2", agentTitle: "Writer", model: "gpt-5", messageCount: -2,
    },
    {
      projectId: "p2", projectName: "Beta", projectPath: "/work/beta",
      worklineId: "w3", worklineTitle: "Release", agentId: "a3", agentTitle: "Verifier", model: "gemini-pro", messageCount: 4,
    },
    { projectId: "p1", worklineId: "", agentId: "bad" },
  ],
};

test("normalizeNavigationPayload normalizes the backend contract and target ids", () => {
  const normalized = normalizeNavigationPayload(payload);
  assert.equal(normalized.projects.length, 3);
  assert.equal(normalized.projects[2].name, "invalid");
  assert.equal(normalized.conversations.length, 3);
  assert.equal(normalized.conversations[0].agentId, "a1");
  assert.equal(normalized.conversations[0].messageCount, 12);
  assert.equal(normalized.conversations[1].messageCount, 4);
  assert.equal(normalized.conversations[2].messageCount, 0);
  assert.equal(normalized.conversations[0].targetId, createNavigationTargetId({ projectId: "p1", worklineId: "w1", agentId: "a1" }));
});

test("legacy conversation-flow records stay out of navigation and project synthesis", () => {
  const mixed = {
    projects: [
      ...payload.projects,
      { id: "legacy-project", name: "Legacy container", flowMode: "conversation" },
    ],
    conversations: [
      ...payload.conversations,
      {
        context: "conversation",
        projectFlowMode: false,
        agentId: "legacy-1",
        agentTitle: "Independent chat",
        agentStatus: "idle",
        model: "codex:gpt-5.5",
        messageCount: 2,
        lastActivityAt: "2026-01-06T00:00:00Z",
      },
      {
        projectId: "legacy-project",
        projectName: "Legacy container",
        worklineId: "legacy-workline",
        agentId: "legacy-2",
        context: "conversation",
        projectFlowMode: false,
      },
    ],
  };
  const normalized = normalizeNavigationPayload(mixed);
  assert.deepEqual(normalized.conversations.map((item) => item.agentId), ["a1", "a3", "a2"]);
  assert.equal(normalized.projects.some((item) => item.id === "legacy-project"), false);
  assert.equal(normalized.conversations.some((item) => item.agentId.startsWith("legacy")), false);

  for (const mode of ["all", "projects", "conversations"]) {
    const view = buildNavigationView(mixed, { mode });
    assert.equal(view.projects.some((item) => item.id === "legacy-project"), false);
    assert.equal(view.conversations.length, 0);
    assert.equal(view.groups.flatMap((group) => group.conversations).some((item) => item.agentId.startsWith("legacy")), false);
    const html = renderNavigationHTML(view);
    assert.doesNotMatch(html, /legacy-project|legacy-1|legacy-2|data-navigation-context="conversation"/);
  }
});

test("navigation preserves pin and archive state and exposes action triggers", () => {
  const statePayload = {
    projects: [
      { id: "p1", name: "Pinned project", pinned: true },
      { id: "p2", name: "Archived project", archivedAt: "2026-01-04T00:00:00Z" },
      { id: "p3", name: "Independent conversation pin" },
    ],
    conversations: [
      { projectId: "p1", projectName: "Pinned project", projectPinned: true, worklineId: "w1", worklineTitle: "main", agentId: "a1", agentTitle: "Recent", lastActivityAt: "2026-01-03T00:00:00Z" },
      { projectId: "p1", projectName: "Pinned project", projectPinned: true, worklineId: "w2", worklineTitle: "pin", agentId: "a2", agentTitle: "Pinned", agentPinned: true, lastActivityAt: "2026-01-01T00:00:00Z" },
      { projectId: "p2", projectName: "Archived project", projectArchivedAt: "2026-01-04T00:00:00Z", worklineId: "w3", worklineTitle: "old", agentId: "a3", agentTitle: "Archived", agentArchivedAt: "2026-01-04T00:00:00Z", lastActivityAt: "2026-01-05T00:00:00Z" },
      { projectId: "p3", projectName: "Independent conversation pin", worklineId: "w4", worklineTitle: "pin", agentId: "a4", agentTitle: "Pinned elsewhere", agentPinned: true, lastActivityAt: "2025-01-01T00:00:00Z" },
    ],
  };
  const normalized = normalizeNavigationPayload(statePayload);
  assert.equal(normalized.projects[0].pinned, true);
  assert.equal(normalized.projects[1].archivedAt, "2026-01-04T00:00:00Z");
  assert.deepEqual(normalized.conversations.map((item) => item.agentId), ["a2", "a1", "a4", "a3"]);
  assert.equal(normalized.conversations[0].agentPinned, true);
  assert.equal(normalized.conversations[3].agentArchivedAt, "2026-01-04T00:00:00Z");
  assert.deepEqual(buildNavigationView(statePayload, { mode: "conversations" }).conversations, []);

  const html = renderNavigationHTML(buildNavigationView(statePayload, { mode: "all" }));
  assert.match(html, /data-navigation-kind="project" data-navigation-id="p1"/);
  assert.match(html, /data-navigation-kind="conversation" data-navigation-id="a2"/);
  assert.doesNotMatch(html, /data-navigation-menu-trigger/);
  assert.match(html, /navigation-state-badge pinned/);
  assert.match(html, /navigation-state-badge archived/);
});

test("navigation status classes remain safe for live agent state styling", () => {
  assert.equal(navigationAgentStatusClass("RUNNING"), "running");
  assert.equal(navigationAgentStatusClass("agent error"), "agent-error");
  assert.equal(navigationAgentStatusClass(`\"><script>`), "script");
  assert.equal(navigationAgentStatusClass(""), "idle");
  assert.equal(aggregateNavigationAgentStatus([]), "");
  assert.equal(aggregateNavigationAgentStatus([{ agentStatus: "idle" }, { agentStatus: "running" }]), "running");
  assert.equal(aggregateNavigationAgentStatus([{ agentStatus: "idle" }, { agentStatus: "error" }]), "error");
  assert.equal(aggregateNavigationAgentStatus([{ agentStatus: "idle" }]), "idle");
});

test("navigation refresh runs immediately on demand and pauses network work while hidden", async () => {
  const timers = new FakeTimers();
  const reasons = [];
  let visible = true;
  const controller = createNavigationRefreshController({
    timers,
    intervalMs: navigationRefreshDefaults.intervalMs,
    shouldRefresh: () => visible,
    refresh: ({ reason }) => reasons.push(reason),
  });

  assert.equal(controller.intervalMs, 2000);
  assert.equal(controller.start(), true);
  assert.equal(timers.nextDelay(), 2000);
  assert.equal(controller.request("agent.started"), true);
  assert.equal(timers.nextDelay(), 0);
  await timers.runNext();
  assert.deepEqual(reasons, ["agent.started"]);
  assert.equal(timers.nextDelay(), 2000);

  visible = false;
  await timers.runNext();
  assert.deepEqual(reasons, ["agent.started"]);
  assert.equal(timers.nextDelay(), 2000);

  visible = true;
  controller.request("visible");
  await timers.runNext();
  assert.deepEqual(reasons, ["agent.started", "visible"]);
  assert.equal(controller.stop(), true);
  assert.equal(timers.tasks.length, 0);
});

test("project groups contain every conversation once and preserve recent ordering", () => {
  const duplicatedPayload = {
    ...payload,
    conversations: [payload.conversations[1], ...payload.conversations, payload.conversations[0]],
  };
  const normalized = normalizeNavigationPayload(duplicatedPayload);
  const all = buildNavigationView(duplicatedPayload, { mode: "all" });

  assert.equal(normalized.conversations.length, 3);
  assert.deepEqual(all.groups.find((group) => group.project.id === "p1").conversations.map((item) => item.agentId), ["a1", "a2"]);
  const html = renderNavigationHTML(all, {
    activeProjectId: "p1",
    activeAgentId: "a2",
    now: Date.parse("2026-01-03T00:31:00Z"),
  });
  assert.match(html, /data-navigation-project-group="p1" data-conversation-count="2"/);
  assert.equal((html.match(/data-navigation-target="p1::w1::a1"/g) || []).length, 1);
  assert.equal((html.match(/data-navigation-target="p1::w2::a2"/g) || []).length, 1);
  assert.match(html, /navigation-conversation-row nested active/);
  assert.doesNotMatch(html, /navigation-project-row active/);
  const projectContextHTML = renderNavigationHTML(all, { activeProjectId: "p1", activeAgentId: "a2", activeSelectionKind: "project" });
  assert.match(projectContextHTML, /navigation-project-row active/);
  assert.doesNotMatch(projectContextHTML, /navigation-conversation-row nested active/);
  assert.match(projectContextHTML, /data-navigation-context="project"/);
  // Folder name stays on the group row. Conversation titles live on the nested
  // rows, matching the Cursor-style tree the sidebar is aiming for.
  assert.match(html, /navigation-project-title"><span class="project-name">Alpha<\/span><\/span>/);
  assert.match(html, /<\/span>\s*<button class="navigation-row-fork"[^>]*data-project-fork-trigger/);
  assert.match(html, /navigation-project-row[^>]*title="\/work\/alpha"/);
  assert.match(html, /navigation-project-twist/);
  assert.match(html, /navigation-folder-icon navigation-folder-closed[^>]*>[\s\S]*?M20 20a2 2 0 0 0 2-2V8/);
  assert.match(html, /navigation-folder-open[^>]*>[\s\S]*?m6 14 1\.45-2\.9/);
  // The directory is still available, just not as the headline.
  assert.match(html, /navigation-conversation-meta project-path" title="\/work\/alpha"/);
  assert.match(html, /navigation-conversation-row nested[^>]*title="docs"/);
  assert.match(html, /navigation-conversation-time">31m<\/span>/);
  assert.match(html, /navigation-empty-conversations/);
  assert.doesNotMatch(html, /project-agent-count|AGENT 2/);
  assert.match(html, /navigation-conversation-row nested[^\"]*status-idle/);
  assert.match(html, /data-agent-status="idle"/);
  // The status in the meta line is localized ("空闲"), not the raw backend enum.
  assert.match(html, /navigation-conversation-meta" title="feat\/login · claude-sonnet · 空闲 ·/);
  assert.doesNotMatch(html, /navigation-conversation-meta" title="[^"]*· idle ·/);
  assert.doesNotMatch(html, /navigation-breadcrumb/);
});

test("project mode and default project selection share message-activity ordering", () => {
  const activityPayload = {
    projects: [
      { id: "project-stale", name: "Stale project", updatedAt: "2026-07-30T00:00:00Z" },
      { id: "project-recent", name: "Recent project", updatedAt: "2026-01-01T00:00:00Z" },
      { id: "project-archived", name: "Archived project", archivedAt: "2026-07-31T00:00:00Z", updatedAt: "2026-07-31T00:00:00Z" },
    ],
    conversations: [
      { projectId: "project-stale", worklineId: "work-stale", agentId: "agent-stale", agentTitle: "Stale", lastActivityAt: "2026-07-30T00:00:00Z" },
      { projectId: "project-recent", worklineId: "work-recent", agentId: "agent-recent", agentTitle: "Recent", lastActivityAt: "2026-08-01T00:00:00Z" },
      { projectId: "project-archived", projectArchivedAt: "2026-07-31T00:00:00Z", worklineId: "work-archived", agentId: "agent-archived", agentTitle: "Archived", lastActivityAt: "2026-08-02T00:00:00Z" },
    ],
  };
  const view = buildNavigationView(activityPayload, { mode: "projects" });

  assert.deepEqual(view.projects.map((project) => project.id), ["project-recent", "project-stale", "project-archived"]);
  assert.equal(resolveTopNavigationProjectId(activityPayload), "project-recent");
  assert.deepEqual(applyProjectOrder(view.projects, ["project-stale", "project-recent"]).map((project) => project.id), ["project-stale", "project-recent", "project-archived"]);
  assert.equal(resolveTopNavigationProjectId(activityPayload, { projectOrder: ["project-stale", "project-recent"] }), "project-stale");
  assert.match(renderNavigationHTML(view), /data-project-id="project-recent"[\s\S]*data-project-id="project-stale"/);
  assert.equal(resolveTopNavigationProjectId({}), "");

  const pinnedPayload = structuredClone(activityPayload);
  pinnedPayload.projects[0].pinned = true;
  assert.equal(resolveTopNavigationProjectId(pinnedPayload), "project-stale");
});

test("buildNavigationView supports grouped and flat project modes", () => {
  const all = buildNavigationView(payload, { mode: "all" });
  assert.equal(all.groups.length, 3);
  assert.deepEqual(all.groups[0].conversations.map((item) => item.agentId), ["a1", "a2"]);
  assert.equal(all.projects.length, 3);
  assert.deepEqual(all.conversations, []);

  const projects = buildNavigationView(payload, { mode: "projects" });
  assert.deepEqual(projects.projects.map((item) => item.id), ["p1", "p2", "invalid"]);
  assert.equal(projects.groups.length, 3);
  assert.deepEqual(projects.groups[0].conversations.map((item) => item.agentId), ["a1", "a2"]);
  const projectsHTML = renderNavigationHTML(projects, { activeProjectId: "p1" });
  assert.equal((projectsHTML.match(/class="navigation-conversation-row navigation-project-row/g) || []).length, 3);
  assert.match(projectsHTML, /navigation-project-row active/);
  // Flat rows have no nested conversation under them, so the row itself has to
  // carry the conversation's name. Naming it after the project instead repeats
  // the directory that the meta line below already shows. No conversation is
  // open here, so the most recent one names the row.
  assert.match(projectsHTML, /navigation-project-title"><span class="project-name">Planner<\/span>/);
  assert.match(projectsHTML, /navigation-conversation-meta project-path" title="\/work\/alpha">\/work\/alpha<\/span>/);
  // The open conversation wins over recency when it belongs to this project.
  const openInFlatHTML = renderNavigationHTML(projects, { activeProjectId: "p1", activeAgentId: "a2" });
  assert.match(openInFlatHTML, /navigation-project-title"><span class="project-name">Writer<\/span>/);
  // A project with no conversations keeps falling back to its own name.
  assert.match(projectsHTML, /<span class="project-name">Beta<\/span>|<span class="project-name">Verifier<\/span>/);
  assert.doesNotMatch(projectsHTML, /data-navigation-project-group|data-project-conversations|data-navigation-target|project-agent-count/);

  const legacyMode = buildNavigationView(payload, { mode: "conversations" });
  assert.deepEqual(legacyMode.conversations, []);
  assert.deepEqual(legacyMode.projects.map((item) => item.id), ["p1", "p2", "invalid"]);
});

// Subagents are dispatched by a parent turn, several at a time, and they reuse
// the parent's workline -- so in the sidebar they could only ever be plain
// sibling rows, which is what made it look like every task opened a branch.
// They have one home instead: the background-task panel.
test("buildNavigationView keeps subagents out of the sidebar without hiding them from the projection", () => {
  const withSubagents = {
    projects: [{ id: "p1", name: "Alpha", gitPath: "/work/alpha", updatedAt: "2026-01-01T00:00:00Z" }],
    conversations: [
      {
        projectId: "p1", projectName: "Alpha", projectPath: "/work/alpha",
        worklineId: "w1", worklineTitle: "Feature", worklineRole: "root",
        agentId: "parent-1", agentTitle: "Planner", agentType: "primary", agentStatus: "idle", messageCount: 5,
      },
      {
        // Same workline as its parent: this is exactly the shape that produced a
        // second flat row rather than anything the tree could nest.
        projectId: "p1", projectName: "Alpha", projectPath: "/work/alpha",
        worklineId: "w1", worklineTitle: "Feature", worklineRole: "root",
        agentId: "child-1", agentTitle: "Review agent", agentType: "subagent", agentStatus: "running", messageCount: 3,
      },
      {
        // No agentType at all must never be treated as a subagent, or an older
        // payload would silently empty the sidebar.
        projectId: "p1", projectName: "Alpha", projectPath: "/work/alpha",
        worklineId: "w2", worklineTitle: "Docs", agentId: "plain-1", agentTitle: "Writer", messageCount: 1,
      },
    ],
  };

  const view = buildNavigationView(withSubagents, { mode: "all" });
  assert.deepEqual(view.groups[0].conversations.map((item) => item.agentId), ["parent-1", "plain-1"]);
  assert.equal(view.totalConversationCount, 2);

  // Everything downstream reads group.conversations, so the count, the status
  // aggregate and the disclosure all have to follow the filter.
  const html = renderNavigationHTML(view, { activeProjectId: "p1", activeAgentId: "child-1" });
  assert.doesNotMatch(html, /Review agent/);
  assert.match(html, /data-conversation-count="2"/);

  // The filter is a rendering decision only. navigateToAgent resolves a child's
  // targetId out of the unfiltered projection, and the background panel's "open
  // subagent" button depends on that lookup succeeding.
  const projection = normalizeNavigationPayload(withSubagents);
  const child = projection.conversations.find((item) => item.agentId === "child-1");
  assert.ok(child, "the subagent must stay in the projection the panel resolves against");
  assert.equal(child.targetId, createNavigationTargetId({ projectId: "p1", worklineId: "w1", agentId: "child-1" }));

  // Opening it must also stay open: nothing reconciles the active selection
  // against the rendered set, so an activeAgentId absent from groups simply
  // highlights no row instead of ejecting the reader.
  assert.doesNotMatch(html, /navigation-conversation-row[^"]*active/);
});

test("task navigation stays project-level and exposes aggregate counts", () => {
  const taskHTML = renderNavigationHTML(buildNavigationView(payload, { mode: "projects" }), {
    taskContext: true,
    taskCounts: { p1: { todo: 2, doing: 1, blocked: 1 }, p2: { blocked: 0 } },
  });
  const taskEmptyHTML = renderNavigationHTML(buildNavigationView({}, { mode: "projects" }), { taskContext: true });
  const conversationEmptyHTML = renderNavigationHTML(buildNavigationView({}, { mode: "projects" }));

  assert.match(taskHTML, /data-navigation-context="tasks"/);
  assert.match(taskHTML, /navigation-project-row task-context/);
  assert.match(taskHTML, /project-task-counts/);
  assert.match(taskHTML, /<span>4<\/span><span class="blocked">1<\/span>/);
  assert.doesNotMatch(taskHTML, /data-navigation-target|navigation-project-conversations|12 (?:条消息|條訊息|messages)/);
  assert.match(taskEmptyHTML, /data-task-project-boundary="true"/);
  assert.match(taskEmptyHTML, /data-primary-workbench-target="conversation"/);
  assert.doesNotMatch(taskEmptyHTML, /data-open-directory-shortcut/);
  assert.match(conversationEmptyHTML, /data-open-directory-shortcut="new"/);
});

test("navigation search matches project path, workline, agent title, and model", () => {
  assert.deepEqual(buildNavigationView(payload, { mode: "projects", query: "/work/beta" }).projects.map((item) => item.id), ["p2"]);

  const feature = buildNavigationView(payload, { mode: "all", query: "Feature login" });
  assert.deepEqual(feature.groups.flatMap((group) => group.conversations).map((item) => item.agentId), ["a1"]);
  const writer = buildNavigationView(payload, { mode: "all", query: "Writer" });
  assert.deepEqual(writer.groups.flatMap((group) => group.conversations).map((item) => item.agentId), ["a2"]);
  const gemini = buildNavigationView(payload, { mode: "all", query: "gemini-pro" });
  assert.deepEqual(gemini.groups.flatMap((group) => group.conversations).map((item) => item.agentId), ["a3"]);

  const grouped = buildNavigationView(payload, { mode: "all", query: "Docs" });
  assert.equal(grouped.groups.length, 1);
  assert.deepEqual(grouped.groups[0].conversations.map((item) => item.agentId), ["a2"]);
});

test("recent conversations deduplicate newest targets and truncate to eight", () => {
  const targets = Array.from({ length: 10 }, (_, index) => ({ projectId: `p${index}`, worklineId: `w${index}`, agentId: `a${index}` }));
  let recent = targets.reduceRight((items, target, index) => addRecentConversation(items, target, `2026-01-${String(index + 1).padStart(2, "0")}T00:00:00Z`), []);
  assert.equal(recent.length, 8);

  recent = addRecentConversation(recent, targets[3], "2026-02-01T00:00:00Z");
  assert.equal(recent.length, 8);
  assert.equal(recent[0].targetId, createNavigationTargetId(targets[3]));
  assert.equal(recent.filter((item) => item.targetId === recent[0].targetId).length, 1);
  assert.deepEqual(normalizeRecentConversations([recent[0], recent[0], { targetId: "bad" }, { targetId: "::::legacy-agent" }]), [recent[0]]);
  assert.deepEqual(addRecentConversation(recent, { projectId: "", worklineId: "", agentId: "legacy-agent" }), recent);
});

test("recent conversation sync accepts only valid canonical storage events", () => {
  const window = new FakeWindow();
  const storage = {};
  const updates = [];
  const controller = createRecentConversationSyncController({
    key: recentConversationsKey,
    window,
    storage,
    onChange: (recent, detail) => updates.push({ recent, detail }),
  });
  assert.equal(controller.isStarted(), true);

  window.dispatch("storage", { key: "other-key", newValue: "[]", storageArea: storage });
  window.dispatch("storage", { key: recentConversationsKey, newValue: "{", storageArea: storage });
  window.dispatch("storage", { key: recentConversationsKey, newValue: JSON.stringify({ targetId: "p1::w1::a1" }), storageArea: storage });
  window.dispatch("storage", { key: recentConversationsKey, newValue: "[]", storageArea: {} });
  assert.equal(updates.length, 0);

  window.dispatch("storage", {
    key: recentConversationsKey,
    storageArea: storage,
    newValue: JSON.stringify([
      { targetId: "p1::w1::a1", openedAt: "2026-07-20T10:00:00Z" },
      { targetId: "p1::w1::a1", openedAt: "2026-07-19T10:00:00Z" },
      { targetId: "invalid" },
    ]),
  });
  assert.equal(updates.length, 1);
  assert.deepEqual(updates[0].recent, [{ targetId: "p1::w1::a1", openedAt: "2026-07-20T10:00:00Z" }]);
  assert.deepEqual(updates[0].detail, { reason: "storage", key: recentConversationsKey });

  window.dispatch("storage", { key: recentConversationsKey, newValue: null, storageArea: storage });
  assert.deepEqual(updates[1].recent, []);
  assert.equal(controller.stop(), true);
  window.dispatch("storage", { key: recentConversationsKey, newValue: "[]", storageArea: storage });
  assert.equal(updates.length, 2);
});

test("initial navigation restores the newest valid recent conversation before backend fallback", () => {
  const conversations = normalizeNavigationPayload(payload).conversations;
  const targetA = conversations.find((conversation) => conversation.agentId === "a1").targetId;
  const targetB = conversations.find((conversation) => conversation.agentId === "a3").targetId;

  assert.equal(resolveInitialNavigationTarget([
    { targetId: "missing::workline::agent", openedAt: "2026-03-02T00:00:00Z" },
    { targetId: targetB, openedAt: "2026-03-01T00:00:00Z" },
    { targetId: targetA, openedAt: "2026-02-01T00:00:00Z" },
  ], conversations), targetB);
  assert.equal(resolveInitialNavigationTarget([], conversations), conversations[0].targetId);
  assert.equal(resolveInitialNavigationTarget([], []), "");
});

test("global recent conversations do not duplicate project-grouped conversations", () => {
  const normalized = normalizeNavigationPayload(payload);
  const recent = normalized.conversations.map((conversation) => ({
    targetId: conversation.targetId,
    openedAt: "2026-02-01T00:00:00Z",
  }));
  const html = renderRecentConversationsHTML(recent, normalized.conversations, "a1");

  assert.doesNotMatch(html, /recent-conversation-item/);
  assert.match(html, /data-recent-conversations-deduplicated="true"/);
  assert.match(html, /data-deduplicated-count="3"/);
});

test("recent conversations are registered in local preference backups", () => {
  assert.ok(localPreferenceBackupKeys.some((entry) => entry.key === recentConversationsKey && entry.type === "json"));
});

test("navigation rendering escapes all dynamic text and attributes", () => {
  const malicious = '"><img src=x onerror="boom">';
  const maliciousPayload = {
    projects: [{ id: malicious, name: `<script>alert("project")</script>`, gitPath: malicious }],
    conversations: [{
      projectId: malicious, projectName: malicious, projectPath: malicious,
      worklineId: "work", worklineTitle: `<b>workline</b>`,
      agentId: "agent", agentTitle: `<img src=x onerror="agent">`, model: `<svg onload="model">`, messageCount: 1,
    }],
  };
  const normalized = normalizeNavigationPayload(maliciousPayload);
  const html = renderNavigationHTML(buildNavigationView(maliciousPayload, { mode: "all" }), {
    activeProjectId: malicious,
    activeAgentId: "agent",
  });
  const recentHtml = renderRecentConversationsHTML([
    { targetId: normalized.conversations[0].targetId, openedAt: "2026-01-01T00:00:00Z" },
  ], normalized.conversations);

  assert.doesNotMatch(`${html}${recentHtml}`, /<script>|<img src=x|<svg onload|<b>/);
  // The row is named after its conversation, so that title is what needs
  // escaping on the normal path.
  assert.match(html, /&lt;img src=x onerror=&quot;agent&quot;&gt;/);
  // A project with no conversations still falls back to its own name, so that
  // path has to stay escaped too.
  const emptyProjectHTML = renderNavigationHTML(
    buildNavigationView({ projects: maliciousPayload.projects, conversations: [] }, { mode: "all" }),
    { activeProjectId: malicious },
  );
  assert.doesNotMatch(emptyProjectHTML, /<script>|<img src=x|<svg onload|<b>/);
  assert.match(emptyProjectHTML, /&lt;script&gt;alert\(&quot;project&quot;\)&lt;\/script&gt;/);
  assert.doesNotMatch(recentHtml, /recent-conversation-item|<img/);
  assert.match(recentHtml, /data-recent-conversations-deduplicated="true"/);
});

test("each project row carries its own fork trigger, distinct from the header create button", () => {
  const payload = {
    projects: [{ id: "p1", name: "autoto", gitPath: "/work/autoto" }],
    conversations: [{
      projectId: "p1", projectName: "autoto", worklineId: "w1", worklineTitle: "main",
      agentId: "a1", agentTitle: "main", messageCount: 2,
    }],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), { activeProjectId: "p1" });

  // The fork trigger is scoped to the project it sits on, so a click cannot be
  // mistaken for the header's create-project action.
  assert.match(html, /data-project-fork-trigger data-project-id-fork="p1"/);
  assert.equal(html.match(/data-project-fork-trigger/g).length, 1);
  // The folder "+" is a trailing grid cell, so it sits on the far right of a
  // wide sidebar instead of overlapping the folder name.
  assert.match(html, /class="project-name">autoto<\/span><\/span>[\s\S]*?<button class="navigation-row-fork"[^>]*data-project-fork-trigger/);
  assert.doesNotMatch(html, /class="project-name">autoto<\/span><button class="navigation-row-fork"/);
  assert.match(html, /class="navigation-title-text">main<\/span><button class="navigation-row-fork"/);
  // Pin and archive live on the right-click menu, so neither row carries "…".
  assert.doesNotMatch(html, /data-navigation-menu-trigger/);
  // Conversation rows are not git-forkable: a git branch is per project.
  const conversationRow = html.slice(html.indexOf("data-navigation-target"));
  assert.doesNotMatch(conversationRow, /data-project-fork-trigger/);
  // They can add another conversation on the same workline.
  assert.match(html, /data-workline-conversation-trigger data-workline-id-conversation="w1"/);
});

test("the task sidebar hides the fork trigger so its rows stay selection-only", () => {
  const payload = { projects: [{ id: "p1", name: "autoto", gitPath: "/work/autoto" }], conversations: [] };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "projects" }), {
    activeProjectId: "p1",
    taskContext: true,
    taskCounts: { p1: { todo: 1 } },
  });

  assert.doesNotMatch(html, /data-project-fork-trigger/);
});

test("project and conversation rows are both draggable within their own ordering scopes", () => {
  const payload = {
    projects: [{ id: "p1", name: "autoto", gitPath: "/work" }],
    conversations: [{ projectId: "p1", worklineId: "w1", worklineTitle: "main", agentId: "a1", agentTitle: "main", messageCount: 1 }],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), { activeAgentId: "a1" });
  assert.match(html, /navigation-project-row[^>]*draggable="true"/);
  assert.match(html, /navigation-conversation-row nested[^>]*draggable="true"/);
  assert.match(html, /data-navigation-id="a1"[^>]*data-conversation-order-scope="p1"/);
});

test("未讀對話帶 unread 標記，正在看的那個不帶", () => {
  const payload = {
    projects: [{ id: "p1", name: "Project" }],
    conversations: [
      { projectId: "p1", worklineId: "w1", agentId: "unread-agent", agentTitle: "Unread", agentStatus: "idle", lastActivityAt: "2026-08-01T00:00:10Z" },
      { projectId: "p1", worklineId: "w2", agentId: "read-agent", agentTitle: "Read", agentStatus: "idle", lastActivityAt: "2026-08-01T00:00:10Z" },
      { projectId: "p1", worklineId: "w3", agentId: "active-agent", agentTitle: "Active", agentStatus: "idle", lastActivityAt: "2026-08-01T00:00:10Z" },
    ],
  };
  const view = buildNavigationView(payload, { mode: "all" });
  const html = renderNavigationHTML(view, {
    activeAgentId: "active-agent",
    seenMap: { "read-agent": Date.parse("2026-08-01T00:00:10Z") },
  });

  assert.match(html, /data-navigation-id="unread-agent"[^>]*data-agent-unread="true"/);
  assert.doesNotMatch(html, /data-navigation-id="read-agent"[^>]*data-agent-unread="true"/);
  // The row being read right now must never render as unread.
  assert.doesNotMatch(html, /data-navigation-id="active-agent"[^>]*data-agent-unread="true"/);
});

test("拖曳順序只在置頂／一般／封存層內生效，不會把置頂項目擠下來", () => {
  const projects = [
    { id: "pinned", pinned: true },
    { id: "normal-a" },
    { id: "normal-b" },
    { id: "archived", archivedAt: "2026-01-01T00:00:00Z" },
  ];
  // A stored order that names the pinned project last, and the archived one
  // first, must not be able to reorder across the tiers.
  const ordered = applyProjectOrder(projects, ["archived", "normal-b", "normal-a", "pinned"]);
  assert.deepEqual(ordered.map((project) => project.id), ["pinned", "normal-b", "normal-a", "archived"]);

  const conversations = [
    { agentId: "pinned", agentPinned: true },
    { agentId: "c1" },
    { agentId: "c2" },
  ];
  assert.deepEqual(
    applyConversationOrder(conversations, ["c2", "c1", "pinned"]).map((c) => c.agentId),
    ["pinned", "c2", "c1"],
  );
});

test("不在拖曳記錄裡的項目保留原本的時間排序，不會被打亂", () => {
  const projects = [{ id: "newest" }, { id: "middle" }, { id: "oldest" }];
  // Only "oldest" was ever dragged. The other two must keep their incoming
  // recency order rather than being shuffled among themselves.
  const ordered = applyProjectOrder(projects, ["oldest"]);
  assert.deepEqual(ordered.map((project) => project.id), ["oldest", "newest", "middle"]);
});

test("applyConversationOrder reorders conversations and puts unknowns at the end", () => {
  const convs = [
    { agentId: "a1" }, { agentId: "a2" }, { agentId: "a3" },
  ];
  const reordered = applyConversationOrder(convs, ["a3", "a1", "a2"]);
  assert.deepEqual(reordered.map((c) => c.agentId), ["a3", "a1", "a2"]);

  const withUnknown = applyConversationOrder(convs, ["a2", "zz"]);
  assert.equal(withUnknown[0].agentId, "a2");
  assert.deepEqual(new Set(withUnknown.map((c) => c.agentId)), new Set(["a1", "a2", "a3"]));
});

test("有新活動的對話浮到拖曳順序之上，讀過之後回到手動位置", () => {
  const seenMap = { done: 200, fresh: 100 };
  const convs = [
    // Unread: activity 150 is newer than the seen mark at 100.
    { agentId: "fresh", agentStatus: "idle", lastActivityAt: new Date(150).toISOString() },
    { agentId: "running", agentStatus: "running" },
    // Read: activity 150 is older than the seen mark at 200.
    { agentId: "done", agentStatus: "idle", lastActivityAt: new Date(150).toISOString() },
    { agentId: "quiet", agentStatus: "idle" },
  ];
  const order = ["quiet", "done", "running", "fresh"];

  // Fresh rows float in their incoming (recency) order; the read and quiet
  // rows below them still follow the drag order.
  assert.deepEqual(
    applyConversationOrder(convs, order, seenMap).map((c) => c.agentId),
    ["fresh", "running", "quiet", "done"],
  );

  // Callers without a seenMap keep the pure drag-order behaviour.
  assert.deepEqual(
    applyConversationOrder(convs, order).map((c) => c.agentId),
    ["quiet", "done", "running", "fresh"],
  );

  // Freshness never lifts a normal row above the pinned tier.
  const withPinned = [{ agentId: "pin", agentPinned: true, agentStatus: "idle" }, ...convs];
  assert.equal(applyConversationOrder(withPinned, order, seenMap)[0].agentId, "pin");
});

test("有新活動的專案群組浮到拖曳順序之上", () => {
  const payload = {
    projects: [
      { id: "p-quiet", name: "quiet", gitPath: "/quiet" },
      { id: "p-fresh", name: "fresh", gitPath: "/fresh" },
    ],
    conversations: [
      { projectId: "p-quiet", worklineId: "w1", worklineTitle: "main", agentId: "q1", agentTitle: "Q", agentStatus: "idle" },
      { projectId: "p-fresh", worklineId: "w2", worklineTitle: "main", agentId: "f1", agentTitle: "F", agentStatus: "running" },
    ],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), {
    projectOrder: ["p-quiet", "p-fresh"],
    seenMap: {},
  });
  assert.ok(
    html.indexOf('data-navigation-project-group="p-fresh"') < html.indexOf('data-navigation-project-group="p-quiet"'),
    "the group with a running agent must float above the dragged-first group",
  );
});

test("renderNavigationHTML applies conversationOrders when provided", () => {
  const payload = {
    projects: [{ id: "p1", name: "autoto", gitPath: "/work" }],
    conversations: [
      { projectId: "p1", worklineId: "w-b", worklineTitle: "B", agentId: "b1", agentTitle: "B" },
      { projectId: "p1", worklineId: "w-a", worklineTitle: "A", agentId: "a1", agentTitle: "A" },
    ],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), {
    conversationOrders: { p1: ["b1", "a1"] },
  });
  assert.ok(html.indexOf("a1") > html.indexOf("b1"), "b1 should come before a1 with the given order");
});

test("legacy projectless records cannot create a navigation order scope", () => {
  const payload = {
    projects: [],
    conversations: [
      { context: "conversation", agentId: "a1", agentTitle: "A" },
      { context: "conversation", agentId: "b1", agentTitle: "B" },
    ],
  };
  for (const mode of ["all", "projects", "conversations"]) {
    const html = renderNavigationHTML(buildNavigationView(payload, { mode }), {
      conversationOrders: { "__legacy__": ["b1", "a1"] },
    });
    assert.doesNotMatch(html, /data-navigation-id="(?:a1|b1)"/);
    assert.doesNotMatch(html, /__legacy__/);
  }
});

// Forks had no coverage at all, which is how they came to render in only one of
// the three sidebar widths without a test noticing.
const forkPayload = {
  projects: [{ id: "p1", name: "Alpha", gitPath: "/work/alpha" }],
  conversations: [
    {
      projectId: "p1", projectPath: "/work/alpha",
      worklineId: "w-root", worklineRole: "root", agentId: "a-root", agentTitle: "Mainline", messageCount: 3,
    },
    {
      projectId: "p1", projectPath: "/work/alpha",
      worklineId: "w-fork", worklineParentId: "w-root", worklineBranch: "feat/x", worklineTitle: "Fork of main",
      agentId: "a-fork", agentTitle: "Branch work", messageCount: 1,
    },
  ],
};

test("a forked workline renders as a first-level sibling under the project", () => {
  const html = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), { activeProjectId: "p1" });
  assert.match(html, /fork-conversation/);
  const convs = html.indexOf('data-project-conversations="p1"');
  const parent = html.indexOf('data-navigation-id="a-root"');
  const fork = html.indexOf('data-navigation-id="a-fork"');
  assert.ok(convs >= 0 && parent > convs && fork > convs, "both rows sit directly under the project");
  assert.doesNotMatch(html, /navigation-workline-forks/);
});

test("disclosure triangles appear only where there is something to disclose", () => {
  const html = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), { activeProjectId: "p1" });
  // The project has conversations, so it gets a triangle. Git branches are
  // first-level rows and do not hide under the parent conversation.
  assert.match(html, /data-navigation-disclosure="project:p1"/);
  assert.match(html, /navigation-disclosure[^>]*draggable="false"/);
  assert.match(html, /navigation-project-twist[\s\S]*?data-navigation-disclosure="project:p1"[\s\S]*?navigation-folder-icon/);
  assert.doesNotMatch(html, /data-navigation-disclosure="fork-open:/);
  assert.doesNotMatch(html, /data-navigation-disclosure="workline:/);

  // Extra conversations on the same workline do get a triangle on that branch.
  const clustered = {
    projects: forkPayload.projects,
    conversations: [
      ...forkPayload.conversations,
      {
        projectId: "p1", projectPath: "/work/alpha",
        worklineId: "w-fork", worklineParentId: "w-root", worklineBranch: "feat/x", worklineTitle: "Fork of main",
        agentId: "a-fork-chat", agentTitle: "Follow-up", messageCount: 0,
      },
    ],
  };
  const clusteredHTML = renderNavigationHTML(buildNavigationView(clustered, { mode: "all" }), { activeProjectId: "p1" });
  assert.match(clusteredHTML, /data-navigation-disclosure="workline:w-fork"/);
  assert.match(clusteredHTML, /navigation-workline-forks/);
  const followUpAt = clusteredHTML.indexOf('data-navigation-id="a-fork-chat"');
  assert.ok(followUpAt > clusteredHTML.indexOf("navigation-workline-forks"));
  const followUpRow = clusteredHTML.slice(clusteredHTML.lastIndexOf('<div class="', followUpAt), followUpAt);
  assert.doesNotMatch(followUpRow, /fork-conversation/);
  assert.match(clusteredHTML, /data-workline-conversation-trigger data-workline-id-conversation="w-fork"/);

  // A project with no conversations has nothing to disclose either.
  const bare = renderNavigationHTML(
    buildNavigationView({ projects: forkPayload.projects, conversations: [] }, { mode: "all" }),
    { activeProjectId: "p1" },
  );
  assert.doesNotMatch(bare, /data-navigation-disclosure/);
  assert.match(bare, /navigation-folder-icon/);
  assert.match(bare, /navigation-empty-conversations/);
});

test("collapsed nodes hide their children and report it to assistive tech", () => {
  const open = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), { activeProjectId: "p1" });
  assert.match(open, /data-navigation-disclosure="project:p1"[^>]*aria-expanded="true"/);
  assert.doesNotMatch(open, /data-project-conversations="p1" hidden/);

  const collapsedProject = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), {
    activeProjectId: "p1",
    collapsedNodes: new Set(["project:p1"]),
  });
  assert.match(collapsedProject, /data-navigation-disclosure="project:p1"[^>]*aria-expanded="false"/);
  assert.match(collapsedProject, /data-project-conversations="p1" hidden/);

  const clustered = {
    projects: forkPayload.projects,
    conversations: [
      ...forkPayload.conversations,
      {
        projectId: "p1", projectPath: "/work/alpha",
        worklineId: "w-fork", worklineParentId: "w-root", worklineBranch: "feat/x", worklineTitle: "Fork of main",
        agentId: "a-fork-chat", agentTitle: "Follow-up", messageCount: 0,
      },
    ],
  };
  const worklineOpen = renderNavigationHTML(buildNavigationView(clustered, { mode: "all" }), { activeProjectId: "p1" });
  assert.match(worklineOpen, /data-navigation-disclosure="workline:w-fork"[^>]*aria-expanded="true"/);
  assert.doesNotMatch(worklineOpen, /navigation-workline-forks" hidden/);

  const worklineCollapsed = renderNavigationHTML(buildNavigationView(clustered, { mode: "all" }), {
    activeProjectId: "p1",
    collapsedNodes: ["workline:w-fork"],
  });
  assert.match(worklineCollapsed, /data-navigation-disclosure="workline:w-fork"[^>]*aria-expanded="false"/);
  assert.match(worklineCollapsed, /navigation-workline-forks" hidden/);
  assert.doesNotMatch(worklineCollapsed, /data-project-conversations="p1" hidden/);
});

test("a git branch stays visible at the first level even while its own chats are closed", () => {
  const html = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), {
    activeProjectId: "p1",
    activeAgentId: "a-fork",
  });
  assert.match(html, /data-navigation-id="a-fork"/);
  assert.doesNotMatch(html, /navigation-workline-forks" hidden/);
});

test("the tree renders at every sidebar width, not only when collapsed", () => {
  // mode is chosen by the shell, so the guard here is that "all" is the only
  // mode that can show a fork and the shell must therefore use it everywhere.
  const all = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "all" }), { activeProjectId: "p1" });
  assert.match(all, /data-navigation-id="a-fork"/);
  const flat = renderNavigationHTML(buildNavigationView(forkPayload, { mode: "projects" }), { activeProjectId: "p1" });
  assert.doesNotMatch(flat, /data-navigation-id="a-fork"/);
});
