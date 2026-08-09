import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// A collapsed activity row was still three lines tall. The run card put the paging
// note and the load-earlier button beside the <details> rather than inside it, so
// folding the activity away left behind a sentence pointing at the run history and a
// button offering to load the same calls inline -- two answers to a question the
// reader had just closed.
const source = readFileSync(new URL("chat-rendering.mjs", import.meta.url), "utf8");
const workspace = readFileSync(new URL("../styles/workspace.css", import.meta.url), "utf8");

function runOutcomeBody() {
  const at = source.indexOf("function renderConversationRunOutcomeHTML(");
  assert.notEqual(at, -1);
  return source.slice(at, source.indexOf("\n  function renderConversationRunNoticeHTML(", at));
}

test("the paging note counts the run across the transcript, not just this card", () => {
  // Tempting to count only this card's leftovers so the number matches the summary
  // directly above it. That states something false: calls filed under their own
  // assistant message are on screen, so a run whose calls all found a home would be
  // told it is showing none of them.
  const body = runOutcomeBody();
  assert.match(
    body,
    /const omitted = Math\.max\(0, Number\(summary\?\.toolCallCount \|\| 0\) - toolCalls\.length\)/,
    "missing means not fetched at all, not merely absent from this one card",
  );
  assert.match(body, /activity\.recentOnly", \{ visible: toolCalls\.length/);
});

test("the note and the button are folded into the activity disclosure", () => {
  const body = runOutcomeBody();
  // They travel together, inside the group, instead of as loose siblings.
  assert.match(body, /class="conversation-run-activity-paging">\$\{omittedNote\}\$\{loadEarlier\}/);
  assert.match(body, /injectRunActivityPaging\(toolActivity, paging\)/);
  // The old shape interpolated both directly into the section next to the stack.
  assert.doesNotMatch(
    body,
    /\$\{toolActivity\}\s*\n\s*\$\{omittedNote\}\s*\n\s*\$\{loadEarlier\}/,
    "that placement is what kept them visible while the activity was collapsed",
  );
});

test("paging is never silently dropped when the stack shape is unexpected", () => {
  const at = source.indexOf("function injectRunActivityPaging(");
  assert.notEqual(at, -1);
  const fn = source.slice(at, source.indexOf("\n  function renderEarlierRunToolCallsButton(", at));
  assert.match(fn, /lastIndexOf\("<\/details>"\)/, "spliced at the group's closing tag");
  assert.match(
    fn,
    /if \(closeAt === -1\) return `\$\{stackHTML\}\$\{paging\}`/,
    "a load button that stops rendering is worse than one in the wrong place",
  );
});

test("with no activity stack the paging still renders on its own", () => {
  // A run whose calls all landed under their own messages has no leftover stack, but
  // may still have earlier pages worth offering.
  assert.match(runOutcomeBody(), /\$\{toolActivity \? injectRunActivityPaging\(toolActivity, paging\) : paging\}/);
});

test("an empty card is still suppressed entirely", () => {
  assert.match(
    runOutcomeBody(),
    /if \(!toolActivity && !loadEarlier && !notice && !omittedNote\) return ""/,
    "nothing to say means no card, not an empty bordered box",
  );
});

test("the paging block is indented with the rows it pages", () => {
  const css = workspace.replace(/\/\*[\s\S]*?\*\//g, "");
  const at = css.indexOf(".conversation-run-activity-paging {");
  assert.notEqual(at, -1);
  const body = css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
  assert.match(body, /padding-left:\s*18px/, "aligned to the activity rows, not the card edge");
  assert.match(body, /justify-items:\s*start/, "so the button hugs its text instead of stretching");
});
