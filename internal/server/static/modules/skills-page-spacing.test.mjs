import assert from "node:assert/strict";
import test from "node:test";
import { readStylesGroupSync } from "./styles-source-helper.mjs";

// CSS gap only does something on a grid or flex container. The skills page declared
// a gap while remaining a plain block, so the value was inert: the tab strip sat
// flush against its panel, and the scope managers -- global skills, project skills,
// custom subagents -- stacked their header, list, pager and forms with no
// separation, which read as a single slab of cards.
//
// These assertions are about the pairing, not the exact numbers. A gap that cannot
// apply is the failure mode worth pinning, because nothing about it looks wrong in
// the stylesheet itself.
// Comments are stripped first. A declaration is often preceded by the explanation
// for it, and "*/" is neither a semicolon nor the start of the body, so matching
// against the raw text reported a missing display that was in fact right there.
const css = readStylesGroupSync("settings.css", import.meta.url).replace(/\/\*[\s\S]*?\*\//g, "");

function ruleBody(selector) {
  const index = css.indexOf(selector);
  assert.notEqual(index, -1, `expected the stylesheet to still contain: ${selector}`);
  const open = css.indexOf("{", index);
  const close = css.indexOf("}", open);
  assert.ok(open !== -1 && close !== -1, `malformed rule for ${selector}`);
  return css.slice(open + 1, close);
}

function declaresGapOnALayout(body, label) {
  const hasGap = /(?:^|;)\s*(?:row-)?gap\s*:/.test(body);
  const hasLayout = /(?:^|;)\s*display\s*:\s*(?:grid|flex|inline-grid|inline-flex)\b/.test(body);
  if (!hasGap) return;
  assert.ok(hasLayout, `${label} declares a gap but no grid or flex display, so the gap cannot apply`);
}

test("the skills page can actually space its tab strip from its panel", () => {
  const body = ruleBody("#settingsContentBody .skills-page {");
  assert.match(body, /gap:\s*var\(--skill-page-gap\)/);
  declaresGapOnALayout(body, ".skills-page");
});

test("skill scope managers space the blocks they stack", () => {
  // The manager sections hold their own children directly, so the gap has to live
  // on the section. Nested import and create sections stack the same way, which is
  // why the selector is a descendant one.
  const selector = "#settingsContentBody .skills-page :is(.settings-provider-section, .settings-page-section) {";
  const body = ruleBody(selector);
  declaresGapOnALayout(body, "skills-page provider sections");
  assert.match(body, /display:\s*grid/);
  assert.match(body, /gap:/);
});

test("the subagent scope field stays a single inline row", () => {
  const body = ruleBody("#settingsContentBody .skill-config-scope-field {");
  assert.match(body, /display:\s*flex/);
  assert.match(body, /flex-direction:\s*row/);
});

test("the subagent preview disclosure does not use a two-column details grid", () => {
  const body = ruleBody("#settingsContentBody .skills-page details.skill-role-preview {");
  assert.match(body, /display:\s*flex/);
  assert.match(body, /flex-direction:\s*column/);
  assert.doesNotMatch(body, /grid-template-columns/);
});

test("the hook form grid actually separates its fields", () => {
  const body = ruleBody("#settingsContentBody .skills-page [data-hook-form] .skill-hook-form-grid {");
  assert.match(body, /gap:/);
});

test("the scoped server-skills section does not carry the item-card left accent", () => {
  const cssSource = readStylesGroupSync("settings.css", import.meta.url);
  assert.match(cssSource, /#settingsContentBody \.skills-page \.skills-v2-card\s*\{[\s\S]*?border-left:/);
  assert.doesNotMatch(cssSource, /#settingsContentBody \.skills-page :is\(\.skills-v2-section, \.skills-v2-card\)/);
});

// A page-wide sweep for the same mistake was tried here and removed. Deciding
// whether a gap can apply means resolving the display an element ends up with,
// across stylesheets and through nested at-rules, and a flat regex mis-slices
// @media blocks badly enough that the check passed with the bug reintroduced. A
// guard that cannot fail is worse than none, so the two assertions above stay
// targeted at the containers that were actually broken; theme-contrast.test.mjs
// holds the brace walker if this is ever worth doing properly.
