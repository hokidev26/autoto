import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// A briefing to a subagent, or its report back, routinely quotes file excerpts and grep
// hits. One such message ran to hundreds of numbered lines and the panel became a wall
// of source: the status, the duration and the error that explained why the task failed
// were all pushed out of view above it.
//
// This clamp is the fallback the pane uses when it has no markdown renderer to hand the
// body to, so it has to keep holding a body of any size on its own.
const source = readFileSync(new URL("background-tasks.mjs", import.meta.url), "utf8");
const messages = readFileSync(new URL("messages-background-tasks.mjs", import.meta.url), "utf8");
const css = readFileSync(new URL("../styles/workspace-tasks.css", import.meta.url), "utf8");

// The renderer is a closure over the panel's state, so the body helper is lifted out and
// evaluated on its own. That keeps this a test of the clamping rule rather than of the
// panel's wiring.
function loadBodyRenderer() {
  const lineLimit = /const childBubbleBodyLineLimit = (\d+);/.exec(source);
  const charLimit = /const childBubbleBodyCharLimit = (\d+);/.exec(source);
  assert.ok(lineLimit && charLimit, "both limits must stay named constants");
  const startAt = source.indexOf("function renderChildBubbleBodyHTML(body) {");
  assert.notEqual(startAt, -1);
  const endAt = source.indexOf("\n  // A stack key is", startAt);
  assert.notEqual(endAt, -1);
  const factory = new Function(
    "escapeHtml",
    "t",
    `const childBubbleBodyLineLimit = ${lineLimit[1]};`
      + `const childBubbleBodyCharLimit = ${charLimit[1]};`
      + source.slice(startAt, endAt)
      + "return renderChildBubbleBodyHTML;",
  );
  return {
    render: factory((value) => String(value), (key, vars) => `${key}:${vars?.count ?? ""}`),
    lineLimit: Number(lineLimit[1]),
    charLimit: Number(charLimit[1]),
  };
}

test("a short answer is left exactly as it was", () => {
  const { render } = loadBodyRenderer();
  const html = render("Found it in three files.");
  assert.equal(html, "<p>Found it in three files.</p>", "no disclosure for something already readable");
});

test("a body at the line limit is not folded", () => {
  const { render, lineLimit } = loadBodyRenderer();
  const body = Array.from({ length: lineLimit }, (_, i) => `line ${i + 1}`).join("\n");
  assert.doesNotMatch(render(body), /<details/, "the limit is a ceiling, not a trigger");
});

test("hundreds of quoted lines fold into a disclosure", () => {
  const { render, lineLimit } = loadBodyRenderer();
  const body = Array.from({ length: 400 }, (_, i) => `${700 + i}|   some source line`).join("\n");
  const html = render(body);
  assert.match(html, /<details class="background-task-bubble-more">/);
  // The head is still shown, so the bubble keeps its shape in the conversation.
  assert.match(html, /^<p>700\| {3}some source line/);
  const shownLines = html.slice(0, html.indexOf("</p>")).split("\n").length;
  assert.equal(shownLines, lineLimit, "exactly the head, no more");
  assert.match(html, /backgroundTasks\.moreLines:388/, "and the summary counts what is folded");
});

test("nothing is discarded, only folded", () => {
  const { render } = loadBodyRenderer();
  const body = Array.from({ length: 60 }, (_, i) => `row ${i}`).join("\n");
  const html = render(body);
  for (const needle of ["row 0", "row 30", "row 59"]) {
    assert.ok(html.includes(needle), `${needle} must still be present somewhere in the markup`);
  }
});

test("one enormous single line is bounded too", () => {
  // A grep hit can arrive as a single line longer than the whole panel. Clamping only
  // by line count would let that through untouched.
  const { render, charLimit } = loadBodyRenderer();
  const body = "x".repeat(charLimit * 3);
  const html = render(body);
  assert.match(html, /<details/, "the character limit is what catches this");
  const head = html.slice(3, html.indexOf("</p>"));
  assert.equal(head.length, charLimit);
});

test("the summary string exists in every locale", () => {
  // A missing key renders as the raw key in the panel.
  assert.equal(
    (messages.match(/moreLines:/g) || []).length,
    3,
    "zh-Hans, zh-Hant and en all define it",
  );
  assert.match(messages, /moreLines: "展开其余 \{count\} 行"/);
  assert.match(messages, /moreLines: "展開其餘 \{count\} 行"/);
  assert.match(messages, /moreLines: "Show \{count\} more lines"/);
});

test("the folded tail scrolls instead of growing the pane", () => {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const at = stripped.indexOf(".background-task-bubble-more[open] > p");
  assert.notEqual(at, -1);
  const body = stripped.slice(stripped.indexOf("{", at) + 1, stripped.indexOf("}", at));
  assert.match(body, /max-height:\s*40vh/, "expanding must not push the error notice off screen again");
  assert.match(body, /overflow:\s*auto/);
});

test("the clamp is the fallback, not a branch nobody reaches", () => {
  // The pane renders a subagent's answer with the main transcript's markdown pipeline
  // whenever it is handed one, so this clamp only runs for a caller without it. Asserting
  // the branch keeps the fallback wired: dropping it silently would leave the tests above
  // exercising code the panel never calls.
  assert.match(source, /function renderChildBodyHTML\(body\) \{[\s\S]*?renderChildBubbleBodyHTML\(body\)/);
  assert.match(source, /typeof renderMarkdown !== "function"/);
});
