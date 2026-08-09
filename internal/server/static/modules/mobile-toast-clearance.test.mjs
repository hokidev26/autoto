import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// These notices used to sit at the bottom edge, where the composer is also fixed on a
// phone, at a higher z-index: a finished background task landed on the message box and
// the send button and had to be waited out. Reserving the composer's height fixed that
// but left the notice over the transcript tail, which is the other thing being read.
//
// They now hang under the header tool row instead, so the conflict is gone rather than
// negotiated: nothing is pinned there, on either a phone or a desktop.
const read = (name) => readFileSync(new URL(`../styles/${name}`, import.meta.url), "utf8");
const stripComments = (css) => css.replace(/\/\*[\s\S]*?\*\//g, "");

function ruleBody(css, selector) {
  const at = css.indexOf(selector);
  assert.notEqual(at, -1, `expected ${selector} to exist`);
  return css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
}

// The declaration order across files is fixed by styles.css, and the toast lives in
// workspace.css while the mobile override lives in extras.css. Equal specificity
// means the later file wins, so the override is only effective while that order
// holds.
test("the mobile toast override is declared after the base toast rule", () => {
  const order = stripComments(readFileSync(new URL("../styles.css", import.meta.url), "utf8"));
  const workspaceAt = order.indexOf("styles/workspace.css");
  const extrasAt = order.indexOf("styles/extras.css");
  assert.ok(workspaceAt !== -1 && extrasAt !== -1, "both stylesheets must still be imported");
  assert.ok(
    workspaceAt < extrasAt,
    "extras.css must stay after workspace.css or the phone override loses to the base toast geometry",
  );
});

test("notices hang under the header row rather than rising from the bottom edge", () => {
  const workspace = stripComments(read("workspace.css"));
  const body = ruleBody(workspace, ".toast-stack");
  assert.match(body, /position:\s*fixed/);
  assert.match(
    body,
    /top:\s*var\(--header-tool-row-bottom/,
    "anchored to the published header height, not a number copied from it",
  );
  assert.doesNotMatch(body, /bottom:\s*\d/, "the bottom anchor is what put it on the composer");
});

test("the header offset is a real declared property, not a fallback nobody sets", () => {
  // A var() with a plausible fallback silently ships the fallback when the property
  // does not exist, so the whole stack would sit at a guessed offset forever.
  const shell = stripComments(read("white-shell.css"));
  assert.match(
    shell,
    /body\.white-shell\.theme-light \{[^}]*--header-tool-row-bottom:\s*64px/,
    "declared on the body: the stack is position:fixed and outside .chat-header, and a "
      + "custom property only reaches descendants",
  );
  // And it has to agree with the height it stands for.
  assert.match(shell, /body\.white-shell\.theme-light \.chat-header \{[^}]*height:\s*64px/);
  // The narrow tier shortens the header, so the offset follows it.
  const narrow = shell.slice(shell.indexOf("@media (max-width: 767px)"));
  assert.match(narrow, /--header-tool-row-bottom:\s*56px/);
  assert.match(narrow, /\.chat-header \{[^}]*height:\s*56px/);
});

test("the phone stack spans the width under the top bar and reserves nothing below", () => {
  const extras = stripComments(read("extras.css"));
  const mobileBlock = extras.slice(extras.indexOf("@media (max-width: 767px)"));
  const body = ruleBody(mobileBlock, ".toast-stack");
  assert.match(body, /top:\s*calc\(\s*var\(--mobile-topbar-height/, "under the phone's own top bar");
  assert.match(body, /env\(safe-area-inset-top\)/, "clearing the notch rather than the home indicator");
  assert.doesNotMatch(
    body,
    /--mobile-composer-reserve/,
    "the composer clearance is obsolete: the notice is nowhere near the composer now",
  );
});

test("no viewport width anchors the stack to a header that is not there", () => {
  // The desktop offset is the header's height, and the phone tier hides that header
  // and re-anchors to the mobile top bar. Those two breakpoints have to be the same
  // number: if the hide happened at a width the re-anchor did not cover, notices in
  // that band would hang at the offset of an element that is display:none.
  const settings = stripComments(read("settings.css"));
  const hiddenAt = settings.indexOf(".chat-header { display: none; }");
  assert.notEqual(hiddenAt, -1, "the phone tier still hides the desktop chat header");
  const enclosing = [...settings.slice(0, hiddenAt).matchAll(/@media\s*\(([^)]*)\)/g)].pop();
  assert.equal(
    enclosing?.[1].replace(/\s/g, ""),
    "max-width:767px",
    "the header is hidden at the same breakpoint the toast override uses",
  );

  const extras = stripComments(read("extras.css"));
  const overrideAt = extras.indexOf(".toast-stack {");
  const overrideMedia = [...extras.slice(0, overrideAt).matchAll(/@media\s*\(([^)]*)\)/g)].pop();
  assert.equal(overrideMedia?.[1].replace(/\s/g, ""), "max-width:767px");

  // And the base rule must stay unwrapped, so it covers every width above that.
  const workspace = stripComments(read("workspace.css"));
  const baseAt = workspace.indexOf(".toast-stack {");
  const before = workspace.slice(0, baseAt);
  const unclosed = (before.match(/\{/g) || []).length - (before.match(/\}/g) || []).length;
  assert.equal(unclosed, 0, "the base toast rule is top level, not inside an at-rule");
});

test("the enter and leave animations move toward the row the notice came from", () => {
  const workspace = stripComments(read("workspace.css"));
  const leaving = ruleBody(workspace, ".toast.leaving");
  assert.match(leaving, /translateY\(-6px\)/, "leaves upward, not down into the transcript");
  const keyframes = workspace.slice(workspace.indexOf("@keyframes toast-in"));
  assert.match(keyframes.slice(0, 160), /translateY\(-8px\)/);
});

test("a long unbroken label cannot decide the height of the toast", () => {
  // Declared once in the base rule rather than per breakpoint: the ceiling is the
  // same on a phone and on a desktop, and a toast that grows past it stops being a
  // passing notice.
  const workspace = stripComments(read("workspace.css"));
  const body = ruleBody(workspace, ".toast span");
  assert.match(body, /overflow-wrap:\s*anywhere/, "a hex id has no break opportunities of its own");
  assert.match(body, /-webkit-line-clamp:\s*2/, "and the message is capped at two lines");
  assert.match(body, /overflow:\s*hidden/, "which only clips with an overflow to clip against");
});
