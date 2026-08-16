import test from "node:test";
import assert from "node:assert/strict";

import {
  GIT_LOG_LIMIT,
  UNCOMMITTED_HASH,
  gitTimelineLaneColor,
  layoutGitTimeline,
  renderGitTimeline,
  renderGitTimelineLanes,
  timelineCommits,
} from "./git-timeline.mjs";

test("a linear history stays on one lane", () => {
  const layout = layoutGitTimeline([
    { hash: "c3", parents: ["c2"] },
    { hash: "c2", parents: ["c1"] },
    { hash: "c1", parents: [] },
  ]);
  assert.equal(layout.laneCount, 1);
  assert.deepEqual(layout.rows.map((row) => row.lane), [0, 0, 0]);
});

test("a merge commit opens a second lane for the other parent", () => {
  const layout = layoutGitTimeline([
    { hash: "cM", parents: ["cA", "cB"] },
    { hash: "cA", parents: ["c0"] },
    { hash: "cB", parents: ["c0"] },
    { hash: "c0", parents: [] },
  ]);
  assert.equal(layout.laneCount, 2);
  const byHash = Object.fromEntries(layout.rows.map((row) => [row.hash, row]));
  assert.equal(byHash.cM.lane, 0);
  assert.equal(byHash.cA.lane, 0);
  assert.equal(byHash.cB.lane, 1);
  assert.equal(byHash.c0.lane, 0);
  assert.ok(byHash.cM.links.some((link) => link.kind === "merge" && link.to === 1));
  assert.ok(byHash.cB.links.some((link) => link.from === 1 && link.to === 0));
});

test("parents outside the loaded window do not grow endless lanes", () => {
  const layout = layoutGitTimeline([
    { hash: "c2", parents: ["missing"] },
    { hash: "c1", parents: ["also-missing"] },
  ]);
  assert.equal(layout.laneCount, 1);
  assert.equal(layout.rows[0].links[0].kind, "stub");
});

test("dirty workspaces prepend a hollow uncommitted row onto HEAD", () => {
  const items = timelineCommits(
    [{ hash: "abc", shortHash: "abc", subject: "done", parents: [] }],
    { dirty: true, head: "abc" },
  );
  assert.equal(items[0].hash, UNCOMMITTED_HASH);
  assert.deepEqual(items[0].parents, ["abc"]);
  assert.equal(items[1].hash, "abc");
  assert.equal(timelineCommits([{ hash: "abc" }], { dirty: false, head: "abc" }).length, 1);
});

test("lane colors cycle and the SVG keeps user text out of markup", () => {
  assert.equal(gitTimelineLaneColor(0), gitTimelineLaneColor(8));
  const svg = renderGitTimelineLanes({
    lane: 0,
    through: [0],
    links: [],
    uncommitted: true,
  }, 1);
  assert.match(svg, /<circle /);
  assert.match(svg, /fill="none"/);
  assert.doesNotMatch(svg, /<script/);
});

test("the timeline HTML escapes subjects and paints refs", () => {
  const html = renderGitTimeline([
    {
      hash: "aaaaaaaa",
      shortHash: "aaaaaaa",
      subject: "<script>alert(1)</script>",
      authorName: "Ada",
      date: "2026-08-13T00:00:00Z",
      parents: [],
      refs: [{ kind: "head", name: "HEAD" }, { kind: "branch", name: "main" }],
    },
  ], {
    t: (key) => key,
    formatTimestamp: () => "when",
    openHash: "aaaaaaaa",
  });
  assert.match(html, /git-timeline/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>alert/);
  assert.match(html, /git-ref head/);
  assert.match(html, /git-ref branch/);
  assert.match(html, /Ada/);
  assert.equal(GIT_LOG_LIMIT, 80);
});
