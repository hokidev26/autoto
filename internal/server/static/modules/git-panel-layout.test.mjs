import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The Git panel put checkpoint, merge, commit and history in four full-width cards
// above the file list. On a repository with any real number of changes, the files and
// the diff -- the reason the panel is open -- started below the fold, behind three
// panels of controls nobody had asked for yet.
const gitWorkflow = readFileSync(new URL("git-workflow.mjs", import.meta.url), "utf8");
const legacy = readFileSync(new URL("../styles/settings-legacy.css", import.meta.url), "utf8");

function ruleBody(css, selector) {
  const at = css.indexOf(selector);
  assert.notEqual(at, -1, `expected ${selector} to exist`);
  return css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
}

test("the changed files start at the top of the panel, not below the action cards", () => {
  const layoutAt = gitWorkflow.indexOf('<div class="git-layout">');
  const filesAt = gitWorkflow.indexOf('<aside class="git-file-list">');
  const checkpointAt = gitWorkflow.indexOf("renderRunCheckpointSection()");
  const mergeAt = gitWorkflow.indexOf("renderWorklineMergePanel()");
  const commitAt = gitWorkflow.indexOf("renderGitCommitPanel(files, selectedCommitPaths)");
  assert.ok(layoutAt !== -1 && filesAt !== -1);
  assert.ok(layoutAt < checkpointAt, "the layout grid opens before the action cards render");
  assert.ok(filesAt < checkpointAt, "and the file list comes before them");
  for (const [name, at] of [["checkpoint", checkpointAt], ["merge", mergeAt], ["commit", commitAt]]) {
    assert.ok(at > filesAt, `the ${name} card must not sit above the file list any more`);
  }
});

test("the action cards live in the side column", () => {
  const sideAt = gitWorkflow.indexOf('<aside class="git-side-column">');
  assert.notEqual(sideAt, -1, "there is a side column");
  const side = gitWorkflow.slice(sideAt, gitWorkflow.indexOf("</aside>", sideAt));
  for (const call of [
    "renderRunCheckpointSection()",
    "renderWorklineMergePanel()",
    "renderGitCommitPanel(files, selectedCommitPaths)",
    "renderGitLog(log?.commits || [], status, Boolean(log?.truncated))",
  ]) {
    assert.ok(side.includes(call), `${call} belongs in the side column`);
  }
});

test("the side column scrolls on its own so the commit box stays reachable", () => {
  const body = ruleBody(legacy, ".git-side-column {");
  assert.match(body, /overflow-y:\s*auto/, "a long history must not push the commit box away");
  assert.match(body, /min-height:\s*0/, "or the column cannot shrink inside the grid row");
  assert.match(body, /overscroll-behavior:\s*contain/, "and its scroll stays inside itself");
  assert.match(body, /flex-direction:\s*column/);
});

test("the grid still gives the diff the largest share", () => {
  const body = ruleBody(legacy, ".git-layout {");
  const columns = /grid-template-columns:\s*([^;]+);/.exec(body)?.[1] || "";
  // Asserted as one string rather than split on whitespace: minmax(0, 1fr) contains
  // a space of its own, so splitting produces a phantom column.
  assert.equal(
    columns.trim(),
    "240px minmax(0, 1fr) 320px",
    "files at a fixed width, the diff flexible and largest, and an actions column "
      + "wider than the old log column because it now carries a commit message field "
      + "and a button row rather than one line of subject text",
  );
});

test("stacked on a narrow screen, the column does not nest a second scroller", () => {
  const narrow = legacy.slice(legacy.indexOf("@media (max-width: 900px)"));
  const body = ruleBody(narrow, ".git-side-column {");
  assert.match(
    body,
    /overflow-y:\s*visible/,
    "one scroll region inside another means the wrong one moves under a thumb",
  );
});
