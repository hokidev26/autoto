import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The settings body element carries an id and a legacy class at the same time:
// <div id="settingsContentBody" class="settings-content-body legacy-settings-content-body">
// So the channels/schedules/appliance page is styled by two sheets that disagree
// about what a section is.
//
// workbench.css treats a section as a borderless full-bleed row: no border, no
// radius, transparent background, its own vertical padding, and a bottom divider.
// For that shape `gap: 0` is right, because the divider is the separation.
//
// settings.css restyles those same elements back into bordered, rounded, shadowed
// cards. Its selectors are id-scoped, so they win on specificity -- but only for the
// properties they actually declare. The legacy `gap: 0` on the grids kept applying
// because no id-scoped rule ever claimed them, leaving rounded cards stacked flush
// against each other with no divider: one slab, which is the reported crowding.
//
// These assertions pin the containment, not the exact spacing values. The failure
// mode worth guarding is a grid whose gap is decided by the other sheet.
const settingsCSS = readFileSync(new URL("../styles/settings.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
const workbenchCSS = readFileSync(new URL("../styles/workbench.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

function ruleBody(css, selector) {
  const index = css.indexOf(selector);
  assert.notEqual(index, -1, `expected the stylesheet to still contain: ${selector}`);
  const open = css.indexOf("{", index + selector.length - 1);
  const close = css.indexOf("}", open);
  assert.ok(open !== -1 && close !== -1, `malformed rule for ${selector}`);
  return css.slice(open + 1, close);
}

// An at-rule holds whole rules, so the first "}" closes a child rather than the block.
// Walking the braces is the only way to read one out correctly.
function atRuleBody(css, prelude) {
  const index = css.indexOf(prelude);
  assert.notEqual(index, -1, `expected the stylesheet to still contain: ${prelude}`);
  const open = css.indexOf("{", index + prelude.length - 1);
  assert.notEqual(open, -1, `malformed at-rule for ${prelude}`);
  let depth = 0;
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === "{") depth += 1;
    else if (css[i] === "}") {
      depth -= 1;
      if (depth === 0) return css.slice(open + 1, i);
    }
  }
  assert.fail(`unterminated at-rule for ${prelude}`);
}

function readGap(body) {
  const match = /(?:^|;)\s*gap\s*:\s*([^;]+)/.exec(body);
  return match ? match[1].trim() : null;
}

// A gap only does something on a grid or flex container, and a gap of zero is the
// same as no separation at all. Both are silent in the stylesheet, so both are
// asserted together.
function separatesItsChildren(body, label) {
  assert.match(body, /(?:^|;)\s*display\s*:\s*(?:grid|inline-grid|flex|inline-flex)\b/, `${label} needs an explicit grid or flex display, otherwise its gap cannot apply`);
  const gap = readGap(body);
  assert.ok(gap, `${label} declares no gap, so the legacy sheet decides its spacing`);
  assert.doesNotMatch(gap, /^0(?:[a-z%]*)?$/, `${label} collapses its gap to ${gap}, which stacks bordered cards flush`);
}

test("the automation section grid separates its section cards", () => {
  separatesItsChildren(ruleBody(settingsCSS, "#settingsContentBody .automation-section-grid {"), ".automation-section-grid");
});

test("the automation card grid separates the cards inside a section", () => {
  // Schedules, connections, and device actions all render through this grid, and the
  // `.single` variant has to be listed too: the legacy sheet zeroes both.
  const body = ruleBody(settingsCSS, "#settingsContentBody .automation-card-grid,\n#settingsContentBody .automation-card-grid.single {");
  separatesItsChildren(body, ".automation-card-grid");
});

test("the automation list separates its rows", () => {
  separatesItsChildren(ruleBody(settingsCSS, "#settingsContentBody .automation-list {"), ".automation-list");
});

// Two columns are opt-in above a container width. The base rules must therefore stay
// single-column: if the base ever widens, the narrow panel gets two columns with no
// breakpoint able to take them back, because the legacy sheet's own narrow rules lose
// to id specificity.
test("the single-column base is what the widening rules widen from", () => {
  for (const selector of ["#settingsContentBody .automation-section-grid {", "#settingsContentBody .automation-card-grid,\n#settingsContentBody .automation-card-grid.single {"]) {
    const columns = /(?:^|;)\s*grid-template-columns\s*:\s*([^;]+)/.exec(ruleBody(settingsCSS, selector));
    assert.ok(columns, `${selector} must state its base column count`);
    assert.doesNotMatch(columns[1], /repeat\(\s*[2-9]/, `${selector} starts at more than one column, so narrow panels cannot fall back`);
  }
});

// Container queries need a container. Without container-type the @container blocks below
// never match anything, and the page silently stays single-column forever -- a failure
// that looks like nothing at all.
test("the page and its sections are query containers", () => {
  assert.match(ruleBody(settingsCSS, "#settingsContentBody .automation-control-page {"), /container(?:-type)?\s*:[^;]*inline-size/, "the page must establish a query container for the section grid");
  assert.match(ruleBody(settingsCSS, "#settingsContentBody .automation-section {"), /container(?:-type)?\s*:[^;]*inline-size/, "each section must establish a query container for its card grid");
});

// This is the coupling worth guarding. The legacy sheet pins every section, span-2
// included, to grid-column 1. Adding a second column without lifting that pin leaves the
// wide sections stuck in the left column while the right one sits empty, which is a
// worse layout than the single column it replaced. The two changes only make sense
// together, so they are asserted together, in the same block.
test("the two-column breakpoint also lets wide sections span", () => {
  const body = atRuleBody(settingsCSS, "@container automation-page (min-width: 720px) {");
  assert.match(body, /\.automation-section-grid\s*\{[^}]*grid-template-columns\s*:\s*repeat\(\s*2/, "the breakpoint should be what introduces the second column");
  assert.match(body, /\.automation-section\.span-2\s*\{[^}]*grid-column\s*:\s*span\s*2/, "span-2 sections stay pinned to column 1 by the legacy sheet unless this block lifts it");
});

test("cards only pair up inside a section wide enough for two", () => {
  const body = atRuleBody(settingsCSS, "@container automation-section (min-width: 560px) {");
  assert.match(body, /\.automation-card-grid\s*\{[^}]*grid-template-columns\s*:\s*repeat\(\s*2/);
  // .single is the render side saying "this card holds a form, keep it one-up". Widening
  // it would undo that intent, and it is easy to widen by accident because the base rule
  // groups .single together with the plain grid.
  assert.match(body, /\.automation-card-grid\.single\s*\{[^}]*grid-template-columns\s*:\s*minmax\(\s*0\s*,\s*1fr\s*\)/, ".single must stay one-up when the plain card grid widens");
});

// The point of the rules above is that they exist at id specificity at all. If the
// legacy sheet stops zeroing these grids the overrides become redundant rather than
// wrong, so this asserts the conflict is still real instead of asserting a number.
test("the legacy sheet still zeroes the automation grids it flattens", () => {
  const flattened = [
    "body.white-shell.theme-light .legacy-settings-content-body .automation-section-grid {",
    "body.white-shell.theme-light .legacy-settings-content-body .automation-card-grid,\nbody.white-shell.theme-light .legacy-settings-content-body .automation-card-grid.single {",
    "body.white-shell.theme-light .legacy-settings-content-body .automation-list {",
  ];
  const zeroed = flattened.filter((selector) => readGap(ruleBody(workbenchCSS, selector)) === "0");
  assert.ok(zeroed.length > 0, "the legacy overrides no longer zero these grids; the id-scoped gap rules can be revisited");
});
