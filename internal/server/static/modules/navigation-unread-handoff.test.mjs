import assert from "node:assert/strict";
import test from "node:test";
import { buildNavigationView, renderNavigationHTML } from "./conversation-navigation.mjs";

// The unread mark used to disappear while looking for it. A collapsed project
// row aggregates over every conversation in the group, git branches included,
// so an unread branch turned that row green. Expanding the project used to
// hide the branch inside a separately collapsed fork group that never
// aggregated. Git branches now sit at the first level, so expanding the
// project reveals the unread row itself.
const payload = {
  projects: [{ id: "p1", name: "Bid site", path: "/work/bid", updatedAt: "2026-03-16T09:00:00Z" }],
  conversations: [
    {
      projectId: "p1",
      worklineId: "w1",
      worklineRole: "root",
      worklineBranch: "main",
      agentId: "root-1",
      agentTitle: "When to register",
      agentStatus: "idle",
      lastActivityAt: "2026-03-16T10:00:00Z",
    },
    {
      projectId: "p1",
      worklineId: "w2",
      worklineParentId: "w1",
      worklineBranch: "registration-fix",
      agentId: "fork-1",
      agentTitle: "Registration follow-up",
      agentStatus: "idle",
      lastActivityAt: "2026-03-16T12:00:00Z",
    },
  ],
};

const seenMap = { "root-1": Date.parse("2026-03-16T11:00:00Z") };
const view = () => buildNavigationView(payload, { mode: "all" });

function rowClasses(html, agentId) {
  const at = html.indexOf(`data-navigation-id="${agentId}"`);
  assert.notEqual(at, -1, `row ${agentId} must render`);
  const open = html.lastIndexOf('<div class="', at);
  assert.notEqual(open, -1);
  return html.slice(open, at);
}

function projectRow(html) {
  const at = html.indexOf("navigation-project-row");
  assert.notEqual(at, -1, "the project row must render");
  return html.slice(html.lastIndexOf('<div class="', at), html.indexOf(">", at));
}

test("the fork really is the unread one in this fixture", () => {
  const html = renderNavigationHTML(view(), { seenMap });
  assert.match(rowClasses(html, "fork-1"), /unread/);
  assert.doesNotMatch(rowClasses(html, "root-1"), /unread/, "the root itself has been read");
});

test("a collapsed project row carries the hidden fork's mark", () => {
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(["project:p1"]), seenMap });
  assert.match(projectRow(html), /unread/, "the group is shut, so its row speaks for everything inside");
});

test("expanding the project shows the unread branch itself", () => {
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(), seenMap });
  assert.match(rowClasses(html, "fork-1"), /unread/);
  assert.doesNotMatch(
    rowClasses(html, "root-1"),
    /unread/,
    "a sibling branch does not mark the mainline conversation",
  );
  assert.doesNotMatch(
    projectRow(html),
    /unread/,
    "and the project row lets go, because the tier below now shows it",
  );
});

test("a collapsed workline carries unread from extra conversations underneath it", () => {
  const clustered = {
    projects: payload.projects,
    conversations: [
      ...payload.conversations,
      {
        projectId: "p1",
        worklineId: "w2",
        worklineParentId: "w1",
        worklineBranch: "registration-fix",
        worklineTitle: "Registration follow-up",
        agentId: "fork-chat",
        agentTitle: "Follow-up chat",
        agentStatus: "idle",
        messageCount: 0,
        lastActivityAt: "2026-03-16T13:00:00Z",
      },
    ],
  };
  const clusteredView = buildNavigationView(clustered, { mode: "all" });
  const closed = renderNavigationHTML(clusteredView, {
    collapsedNodes: new Set(["workline:w2"]),
    seenMap: { "root-1": Date.parse("2026-03-16T11:00:00Z"), "fork-1": Date.parse("2026-03-16T13:00:00Z") },
  });
  assert.match(rowClasses(closed, "fork-1"), /unread/, "the branch stands in while its extra chat is folded");
  assert.match(closed, /navigation-workline-forks" hidden/);

  const opened = renderNavigationHTML(clusteredView, {
    collapsedNodes: new Set(),
    seenMap: { "root-1": Date.parse("2026-03-16T11:00:00Z"), "fork-1": Date.parse("2026-03-16T13:00:00Z") },
  });
  assert.match(rowClasses(opened, "fork-chat"), /unread/);
  assert.doesNotMatch(rowClasses(opened, "fork-1"), /unread/);
});

test("a read fork leaves its parent alone", () => {
  const allRead = { "root-1": Date.parse("2026-03-16T11:00:00Z"), "fork-1": Date.parse("2026-03-16T13:00:00Z") };
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(), seenMap: allRead });
  assert.doesNotMatch(rowClasses(html, "root-1"), /unread/);
  assert.doesNotMatch(projectRow(html), /unread/);
});

test("the row being read is never unread on its own behalf", () => {
  const html = renderNavigationHTML(view(), {
    collapsedNodes: new Set(),
    seenMap: {},
    activeAgentId: "root-1",
    activeSelectionKind: "conversation",
  });
  assert.doesNotMatch(rowClasses(html, "root-1"), /unread/);
});

test("being inside the fork does not make its parent report it", () => {
  const html = renderNavigationHTML(view(), {
    collapsedNodes: new Set(),
    seenMap,
    activeAgentId: "fork-1",
    activeSelectionKind: "conversation",
  });
  assert.doesNotMatch(
    rowClasses(html, "root-1"),
    /unread/,
    "the reader is in that fork, so there is nothing to stand in for",
  );
});
