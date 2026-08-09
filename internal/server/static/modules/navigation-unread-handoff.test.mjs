import assert from "node:assert/strict";
import test from "node:test";
import { buildNavigationView, renderNavigationHTML } from "./conversation-navigation.mjs";

// The unread mark used to disappear while looking for it. A collapsed project row
// aggregates over every conversation in the group, forks included, so an unread fork
// turned that row green. Expanding the project stopped the aggregation -- correctly,
// since the rows are visible now -- but the unread fork was still hidden inside its
// own separately collapsed fork group, and that row never aggregated. So the mark
// existed only while the group was shut, and expanding it to find the new reply was
// the action that hid it.
//
// Forks rest closed, so this is the ordinary state of a forked conversation, not an
// unusual arrangement the reader has to set up first.
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
      // A fork is recognised by branch plus parent, not by parent alone.
      worklineBranch: "registration-fix",
      agentId: "fork-1",
      agentTitle: "Registration follow-up",
      agentStatus: "idle",
      // Newer than the seen mark below, so this fork is the unread one.
      lastActivityAt: "2026-03-16T12:00:00Z",
    },
  ],
};

// root-1 has been read; fork-1 has not.
const seenMap = { "root-1": Date.parse("2026-03-16T11:00:00Z") };

// "all" is the grouped mode, the one that nests conversation rows under a project row
// and so the only one where this handoff exists. "projects" renders flat rows with no
// children to hide anything.
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
  // Guards the premise: if the fixture stopped producing an unread fork the rest of
  // these assertions would pass by saying nothing.
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(["fork-open:root-1"]), seenMap });
  assert.match(rowClasses(html, "fork-1"), /unread/);
  assert.doesNotMatch(rowClasses(html, "root-1"), /unread/, "the root itself has been read");
});

test("a collapsed project row carries the hidden fork's mark", () => {
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(["project:p1"]), seenMap });
  assert.match(projectRow(html), /unread/, "the group is shut, so its row speaks for everything inside");
});

test("expanding the project hands the mark to the row that still hides the fork", () => {
  // Project open, forks closed. This is the case that used to lose the mark.
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(), seenMap });
  assert.match(
    rowClasses(html, "root-1"),
    /unread/,
    "the workline root stands in for the fork folded underneath it",
  );
  assert.doesNotMatch(
    projectRow(html),
    /unread/,
    "and the project row lets go, because the tier below now shows it",
  );
});

test("opening the forks moves the mark onto the fork itself", () => {
  const html = renderNavigationHTML(view(), { collapsedNodes: new Set(["fork-open:root-1"]), seenMap });
  assert.match(rowClasses(html, "fork-1"), /unread/);
  assert.doesNotMatch(
    rowClasses(html, "root-1"),
    /unread/,
    "the stand-in steps back so the mark is not shown twice",
  );
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
  // Its own activity is unseen here, but it is the conversation on screen.
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
