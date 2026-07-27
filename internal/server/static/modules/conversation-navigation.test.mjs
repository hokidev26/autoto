import test from "node:test";
import assert from "node:assert/strict";

import {
  addRecentConversation,
  applyConversationOrder,
  buildNavigationView,
  createNavigationRefreshController,
  createNavigationTargetId,
  createRecentConversationSyncController,
  navigationAgentStatusClass,
  navigationRefreshDefaults,
  normalizeNavigationPayload,
  normalizeRecentConversations,
  renderNavigationHTML,
  renderRecentConversationsHTML,
  resolveInitialNavigationTarget,
} from "./conversation-navigation.mjs";
import { localPreferenceBackupKeys, recentConversationsKey } from "./preferences-data.mjs";

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

test("standalone conversations stay out of project synthesis and project groups", () => {
  const mixed = {
    projects: payload.projects,
    conversations: [
      ...payload.conversations,
      {
        context: "conversation",
        projectFlowMode: false,
        agentId: "standalone-1",
        agentTitle: "Independent chat",
        agentStatus: "idle",
        model: "codex:gpt-5.5",
        messageCount: 2,
        lastActivityAt: "2026-01-06T00:00:00Z",
      },
      {
        projectId: "hidden-project",
        projectName: "Must stay hidden",
        projectPath: "/hidden/project",
        worklineId: "hidden-workline",
        worklineTitle: "Hidden",
        agentId: "standalone-2",
        agentTitle: "Backend-marked standalone",
        context: "conversation",
        projectFlowMode: false,
      },
    ],
  };
  const normalized = normalizeNavigationPayload(mixed);
  assert.deepEqual(normalized.conversations.filter((item) => item.standalone).map((item) => item.agentId), ["standalone-1", "standalone-2"]);
  assert.equal(normalized.projects.some((item) => item.id === "hidden-project"), false);
  assert.deepEqual(normalized.conversations.find((item) => item.agentId === "standalone-1").targetId, "::::standalone-1");

  const conversations = buildNavigationView(mixed, { mode: "conversations" });
  assert.deepEqual(conversations.conversations.map((item) => item.agentId), ["standalone-1", "standalone-2"]);
  assert.deepEqual(conversations.groups, []);
  const projects = buildNavigationView(mixed, { mode: "projects" });
  assert.equal(projects.projects.some((item) => item.id === "hidden-project"), false);
  assert.equal(projects.groups.flatMap((group) => group.conversations).some((item) => item.standalone), false);

  const all = buildNavigationView(mixed, { mode: "all" });
  const html = renderNavigationHTML(all);
  assert.equal((html.match(/data-navigation-target="::::standalone-1"/g) || []).length, 1);
  assert.equal((html.match(/data-navigation-target="hidden-project::hidden-workline::standalone-2"/g) || []).length, 1);
  assert.equal((html.match(/data-navigation-target="p1::w1::a1"/g) || []).length, 1);
  assert.match(html, /data-standalone-conversation="true"/);
  assert.match(html, /data-navigation-context="conversation"/);
  assert.match(html, /data-navigation-context="project"/);
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
  assert.match(html, /data-navigation-menu-trigger/);
  assert.match(html, /navigation-state-badge pinned/);
  assert.match(html, /navigation-state-badge archived/);
});

test("navigation status classes remain safe for live agent state styling", () => {
  assert.equal(navigationAgentStatusClass("RUNNING"), "running");
  assert.equal(navigationAgentStatusClass("agent error"), "agent-error");
  assert.equal(navigationAgentStatusClass(`\"><script>`), "script");
  assert.equal(navigationAgentStatusClass(""), "idle");
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
  const html = renderNavigationHTML(all, { activeProjectId: "p1", activeAgentId: "a2" });
  assert.match(html, /data-navigation-project-group="p1" data-conversation-count="2"/);
  assert.equal((html.match(/data-navigation-target="p1::w1::a1"/g) || []).length, 1);
  assert.equal((html.match(/data-navigation-target="p1::w2::a2"/g) || []).length, 1);
  assert.match(html, /navigation-conversation-row nested active/);
  assert.doesNotMatch(html, /navigation-project-row active/);
  const projectContextHTML = renderNavigationHTML(all, { activeProjectId: "p1", activeAgentId: "a2", activeSelectionKind: "project" });
  assert.match(projectContextHTML, /navigation-project-row active/);
  assert.doesNotMatch(projectContextHTML, /navigation-conversation-row nested active/);
  assert.match(projectContextHTML, /data-navigation-context="project"/);
  assert.match(html, /navigation-project-title"><span class="project-kind-badge">PROJECT<\/span><span class="project-name">Alpha<\/span><\/span>/);
  assert.match(html, /navigation-project-row[^>]*title="Alpha"/);
  assert.match(html, /navigation-conversation-row nested[^>]*title="Writer"/);
  assert.doesNotMatch(html, /project-agent-count|AGENT 2/);
  assert.match(html, /navigation-conversation-row nested[^\"]*status-idle/);
  assert.match(html, /data-agent-status="idle"/);
  assert.match(html, /navigation-conversation-meta" title="feat\/login · claude-sonnet · idle ·/);
  assert.doesNotMatch(html, /navigation-breadcrumb/);
});

test("buildNavigationView supports all, projects, and conversations modes", () => {
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
  assert.match(projectsHTML, /navigation-project-title"><span class="project-kind-badge">PROJECT<\/span><span class="project-name">Alpha<\/span>/);
  assert.match(projectsHTML, /navigation-conversation-meta project-path" title="\/work\/alpha">\/work\/alpha<\/span>/);
  assert.doesNotMatch(projectsHTML, /data-navigation-project-group|data-project-conversations|data-navigation-target|project-agent-count/);

  const conversations = buildNavigationView(payload, { mode: "conversations" });
  assert.deepEqual(conversations.conversations, []);
  assert.deepEqual(conversations.projects, []);
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
  assert.deepEqual(normalizeRecentConversations([recent[0], recent[0], { targetId: "bad" }]), [recent[0]]);
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
  assert.match(html, /class="project-kind-badge">PROJECT<\/span>/);
  assert.match(html, /&lt;script&gt;alert\(&quot;project&quot;\)&lt;\/script&gt;/);
  assert.match(html, /&lt;img src=x onerror=&quot;agent&quot;&gt;/);
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
  // Project rows removed the "…" button; right-click already provides the same actions.
  assert.doesNotMatch(html, /data-navigation-menu-trigger data-navigation-kind="project"/);
  // Conversation rows still have the "…" button.
  assert.match(html, /data-navigation-menu-trigger data-navigation-kind="conversation"/);
  // Conversation rows are not forkable: a fork is per project.
  const conversationRow = html.slice(html.indexOf("data-navigation-target"));
  assert.doesNotMatch(conversationRow, /data-project-fork-trigger/);
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

test("conversation rows are draggable, project rows are not", () => {
  const payload = {
    projects: [{ id: "p1", name: "autoto", gitPath: "/work" }],
    conversations: [{ projectId: "p1", worklineId: "w1", worklineTitle: "main", agentId: "a1", agentTitle: "main", messageCount: 1 }],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), { activeAgentId: "a1" });
  // Conversation rows must be draggable
  assert.match(html, /navigation-conversation-row[^>]*draggable="true"/);
  // Project rows must NOT be draggable (reordering projects is not implemented)
  assert.doesNotMatch(html, /data-project-id[^>]*draggable="true"/);
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

test("renderNavigationHTML applies conversationOrders when provided", () => {
  const payload = {
    projects: [{ id: "p1", name: "autoto", gitPath: "/work" }],
    conversations: [
      { projectId: "p1", worklineId: "w1", worklineTitle: "main", agentId: "b1", agentTitle: "B" },
      { projectId: "p1", worklineId: "w1", worklineTitle: "main", agentId: "a1", agentTitle: "A" },
    ],
  };
  const html = renderNavigationHTML(buildNavigationView(payload, { mode: "all" }), {
    conversationOrders: { p1: ["b1", "a1"] },
  });
  // b1 must appear before a1 in the HTML
  assert.ok(html.indexOf("a1") > html.indexOf("b1"), "b1 should come before a1 with the given order");
});
