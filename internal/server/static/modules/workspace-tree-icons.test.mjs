import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The rows were labelled with U+25B1 WHITE PARALLELOGRAM for a directory and U+25C7
// WHITE DIAMOND for a file. Nothing was broken in the font: a leaning parallelogram
// does not read as a folder and a diamond does not read as a file, so at 19px both
// arrived as small hollow outlines carrying no meaning. The same placeholder glyphs
// were cleared out of the toolbar and the directory browser earlier -- see the
// assertions in white-shell.test.mjs -- and this list was missed.
const source = readFileSync(new URL("workspace-explorer.mjs", import.meta.url), "utf8");
const shell = readFileSync(new URL("../styles/white-shell.css", import.meta.url), "utf8");
const stripComments = (css) => css.replace(/\/\*[\s\S]*?\*\//g, "");

test("no geometric placeholder glyph is used as a tree icon", () => {
  const row = source.slice(source.indexOf("workspace-entry-icon"), source.indexOf("workspace-entry-main"));
  assert.doesNotMatch(row, /[\u25B1\u25C7\u25A1\u25C6\u25B0]/, "these say nothing about a file or a folder");
  assert.match(row, /workspaceDirectoryIcon|workspaceFileIcon/);
});

test("both icons are drawn as inline SVG on the same box as the rest of the UI", () => {
  for (const name of ["workspaceDirectoryIcon", "workspaceFileIcon"]) {
    const at = source.indexOf(`const ${name} =`);
    assert.notEqual(at, -1, `${name} must exist`);
    const value = source.slice(at, source.indexOf("\n", at));
    assert.match(value, /<svg viewBox="0 0 24 24"/, "matching the 24x24 box used elsewhere");
    assert.match(value, /aria-hidden="true"/, "the row already has its name as text");
    assert.match(value, /<path /, "and it actually draws something");
  }
});

test("the icon cell is sized for a drawing, not for a text glyph", () => {
  const css = stripComments(shell);
  const at = css.indexOf(".workspace-entry-icon svg");
  assert.notEqual(at, -1, "the SVG needs explicit dimensions");
  const body = css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
  // Left to font-size an inline SVG falls back to its own default box and tears the
  // 28px icon column open.
  assert.match(body, /width:\s*18px/);
  assert.match(body, /height:\s*18px/);
  assert.match(body, /stroke:\s*currentColor/, "so it follows the row's colour and theme");
  assert.match(body, /fill:\s*none/);

  const cellAt = css.indexOf(".workspace-entry-icon {");
  const cell = css.slice(css.indexOf("{", cellAt) + 1, css.indexOf("}", cellAt));
  assert.doesNotMatch(cell, /font-size/, "font-size did nothing once the glyph became an SVG");
});

test("a directory is distinguishable from a file by more than its outline", () => {
  const css = stripComments(shell);
  const at = css.indexOf('.workspace-entry[data-workspace-dir="true"] .workspace-entry-icon svg');
  assert.notEqual(at, -1, "the folder carries a tint");
  const body = css.slice(css.indexOf("{", at) + 1, css.indexOf("}", at));
  assert.match(body, /fill:\s*color-mix\(/, "a tint derived from the theme colour, not a fixed one");
  // The row already publishes this flag for the click handler, so the styling reuses
  // it rather than adding a second class that could disagree with it.
  assert.match(source, /data-workspace-dir="\$\{isDir \? "true" : "false"\}"/);
});

test("the icon colour comes from the theme so it survives a theme switch", () => {
  const css = stripComments(shell);
  const cellAt = css.indexOf(".workspace-entry-icon {");
  const cell = css.slice(css.indexOf("{", cellAt) + 1, css.indexOf("}", cellAt));
  assert.match(cell, /color:\s*var\(--ws-primary\)/, "not a hard-coded blue");
});
