import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

// The light shell writes literal colors (#0f172a, #ffffff, …) instead of
// var(--ws-*) in hundreds of rules. A dark palette can re-point the variables
// but cannot reach those literals, so a white card with near-black copy stays
// white and near-black on a near-black canvas -- which is how Runtime resources
// and About became unreadable in the dark and cyber themes.
//
// The fix is a generated override block scoped to .theme-dark. This test is what
// keeps it honest: it re-runs the audit that produced the block and fails when a
// newly hard-coded color is not covered. Fixing a failure means either using a
// var(--ws-*) in the offending rule (preferred) or regenerating the block.
const stylesDir = join(dirname(fileURLToPath(import.meta.url)), "..", "styles");

// Both dark palettes carry .theme-dark, so one scope covers the plain dark theme
// and cyber alike. The audit scores against the lighter of the two dark cards so
// it is the stricter of the two.
const DARK_SCOPE = "body.white-shell.theme-light.theme-dark";
const DARK_CARD = "#171827";
const AA_CONTRAST = 4.5;
const LIGHT_SURFACE_LUMINANCE = 0.5;

function channels(hex) {
  let value = hex.replace("#", "");
  if (value.length === 3) value = [...value].map((c) => c + c).join("");
  return [0, 2, 4].map((i) => Number.parseInt(value.slice(i, i + 2), 16));
}

function relativeLuminance(hex) {
  const [r, g, b] = channels(hex).map((c) => {
    const channel = c / 255;
    return channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [relativeLuminance(a), relativeLuminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// Selector lists carry commas inside :is()/:not(), so splitting has to respect
// nesting or half a selector ends up as its own entry.
function splitSelectors(list) {
  const parts = [];
  let depth = 0;
  let buffer = "";
  for (const char of list) {
    if (char === "(") depth += 1;
    else if (char === ")") depth -= 1;
    if (char === "," && depth === 0) {
      parts.push(buffer);
      buffer = "";
    } else {
      buffer += char;
    }
  }
  parts.push(buffer);
  return parts.map((part) => part.replace(/\s+/g, " ").trim()).filter(Boolean);
}

// A selector already anchored on the light shell needs that anchor replaced
// rather than prefixed: "SCOPE body.white-shell…" can never match anything.
function darkScoped(selector) {
  for (const prefix of ["body.white-shell.theme-light", "body.theme-light", "body.white-shell", "body"]) {
    if (selector === prefix) return DARK_SCOPE;
    if (selector.startsWith(`${prefix} `)) return `${DARK_SCOPE} ${selector.slice(prefix.length + 1)}`;
    if (/^[:.[]/.test(selector.slice(prefix.length)) && selector.startsWith(prefix)) {
      return DARK_SCOPE + selector.slice(prefix.length);
    }
  }
  return `${DARK_SCOPE} ${selector}`;
}

const RULE = /([^{}]+)\{([^{}]*)\}/g;
const COLOR = /(?<![-\w])color:\s*(#[0-9a-fA-F]{3,8})/gi;
const BACKGROUND_DECLARATION = /(?<![-\w])background(?:-color)?:\s*([^;}]+)/i;
// `transparent` and `none` are not a painted background: the element shows the
// surface behind it, so its copy has to be judged against that surface. Missing
// this is what left the composer popovers unreadable after the first pass.
const SHOWS_SURFACE_BEHIND = new Set(["transparent", "none"]);

function readStylesheets() {
  return readdirSync(stylesDir)
    .filter((name) => name.endsWith(".css"))
    .map((name) => ({ name, css: readFileSync(join(stylesDir, name), "utf8").replace(/\/\*[\s\S]*?\*\//g, "") }));
}

// Everything a dark palette has to restate, and everything it already does.
function auditDarkThemeCoverage() {
  const required = new Map();
  const covered = new Set();
  for (const { name, css } of readStylesheets()) {
    RULE.lastIndex = 0;
    for (const rule of css.matchAll(RULE)) {
      const [, rawSelector, body] = rule;
      const selector = rawSelector.trim();
      if (selector.includes("@")) continue;
      if (selector.includes(DARK_SCOPE)) {
        for (const one of splitSelectors(selector)) covered.add(one);
        continue;
      }
      // A preset that already restates its own palette is not this audit's
      // business; only the shared light-shell literals are.
      if (selector.includes("data-theme-preset") || selector.includes("theme-dark")) continue;

      const line = css.slice(0, rule.index).split("\n").length;
      const background = BACKGROUND_DECLARATION.exec(body)?.[1]?.trim().toLowerCase();
      const backgroundHex = background && /^#[0-9a-fA-F]{3,8}$/.test(background) ? background.slice(0, 7) : "";
      const remapsSurface = backgroundHex.length === 7 && relativeLuminance(backgroundHex) > LIGHT_SURFACE_LUMINANCE;
      if (remapsSurface) {
        for (const one of splitSelectors(selector)) {
          required.set(darkScoped(one), `${name}:${line} light surface ${backgroundHex}`);
        }
      } else if (background && !SHOWS_SURFACE_BEHIND.has(background)) {
        // Paints its own non-light background, so its copy is already readable.
        continue;
      }

      COLOR.lastIndex = 0;
      for (const declaration of body.matchAll(COLOR)) {
        const hex = declaration[1].slice(0, 7);
        if (hex.length !== 7 || contrast(hex, DARK_CARD) >= AA_CONTRAST) continue;
        for (const one of splitSelectors(selector)) {
          required.set(darkScoped(one), `${name}:${line} color ${hex} scores ${contrast(hex, DARK_CARD).toFixed(2)}:1`);
        }
      }
    }
  }
  return { required, covered };
}

test("every hard-coded light-shell color is restated for the dark themes", () => {
  const { required, covered } = auditDarkThemeCoverage();
  assert.ok(required.size > 0, "the audit found no literals at all, which means it stopped parsing the stylesheets");

  const uncovered = [...required].filter(([selector]) => !covered.has(selector));
  const report = uncovered
    .slice(0, 25)
    .map(([selector, why]) => `  ${why}\n    needs: ${selector}`)
    .join("\n");
  assert.equal(
    uncovered.length,
    0,
    `${uncovered.length} rule(s) hard-code a color the dark themes cannot reach.\n`
      + `Prefer changing the rule to use var(--ws-text) / var(--ws-muted) / var(--ws-card).\n`
      + `Otherwise add the selector to the generated dark override block in styles/settings.css.\n${report}`,
  );
});

test("the dark override block resolves through theme variables, not new literals", () => {
  const settings = readFileSync(join(stylesDir, "settings.css"), "utf8");
  const start = settings.indexOf("Dark schemes: surfaces and copy the light shell hard-codes");
  assert.ok(start > 0, "the generated dark override block is missing from settings.css");
  // Slice to the block's own end marker. Guessing at "the next preset rule"
  // swept in the theme presets that follow, so a preset painting dark text on
  // its own light button read as a violation of a block it is not part of.
  const end = settings.indexOf("/* ===== end dark override block ===== */", start);
  assert.ok(end > start, "the dark override block is missing its end marker");
  const block = settings.slice(start, end);

  // Surfaces and primary copy must follow the active palette. Only the semantic
  // accents (error/success/info) are allowed to be literal, because no --ws-*
  // variable carries them.
  const literals = [...block.matchAll(/(?<![-\w])(?:background|color|border-color):\s*(#[0-9a-fA-F]{3,8})/gi)].map((m) => m[1]);
  const allowed = new Set(["#ff9a9a", "#7ee2a0", "#7fdcff"]);
  const unexpected = [...new Set(literals)].filter((hex) => !allowed.has(hex.toLowerCase()));
  assert.deepEqual(unexpected, [], "the dark override block should resolve through var(--ws-*), not fresh literals");

  for (const hex of allowed) {
    assert.ok(contrast(hex, DARK_CARD) >= AA_CONTRAST, `${hex} must clear AA against the dark card`);
  }
});
