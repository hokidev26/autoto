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

// Compute CSS specificity: (id_count * 100) + (class_attr_pseudo_count * 10) + (element_count * 1).
// :has(), :is(), :not() take the specificity of their most specific argument (highest wins).
// :where() has 0 specificity.
// :not() is equivalent to :is() in newer specs (uses argument specificity).
function computeSpecificity(selector) {
  let ids = 0;
  let classes = 0;
  let elements = 0;

  // Remove leading/trailing whitespace
  selector = selector.trim();

  // Track nesting depth to handle :has(), :is(), :not(), :where() correctly
  let i = 0;
  let buffer = "";

  while (i < selector.length) {
    const char = selector[i];

    // Handle pseudo-functions like :has(), :is(), :where(), :not()
    if (char === ":" && i + 1 < selector.length && selector[i + 1] !== ":") {
      // Found a pseudo-selector
      const pseudoStart = i;
      i++; // skip the ':'

      // Read the pseudo-name
      let pseudoName = "";
      while (i < selector.length && /[a-z-]/.test(selector[i])) {
        pseudoName += selector[i];
        i++;
      }

      // Check if it's a pseudo-function (followed by '(')
      if (i < selector.length && selector[i] === "(") {
        if (pseudoName === "where") {
          // :where() has 0 specificity, skip its content
          let depth = 1;
          i++; // skip '('
          while (i < selector.length && depth > 0) {
            if (selector[i] === "(") depth++;
            else if (selector[i] === ")") depth--;
            i++;
          }
        } else if (pseudoName === "has" || pseudoName === "is" || pseudoName === "not") {
          // :has(), :is(), :not() take specificity of their most specific argument
          let depth = 1;
          i++; // skip '('
          let argContent = "";
          while (i < selector.length && depth > 0) {
            if (selector[i] === "(") depth++;
            else if (selector[i] === ")") depth--;
            else if (depth === 1) argContent += selector[i];
            i++;
          }
          // Split by comma (multiple selectors in :is/:not/:has)
          const args = argContent.split(",");
          let maxArgSpec = 0;
          for (const arg of args) {
            maxArgSpec = Math.max(maxArgSpec, computeSpecificity(arg.trim()));
          }
          classes += Math.floor(maxArgSpec / 10) % 10;
          ids += Math.floor(maxArgSpec / 100);
          if (maxArgSpec % 10 > 0) classes += (maxArgSpec % 10);
        } else {
          // Other pseudo-functions like :not-selector (very rare), :nth-child, etc.
          let depth = 1;
          i++; // skip '('
          while (i < selector.length && depth > 0) {
            if (selector[i] === "(") depth++;
            else if (selector[i] === ")") depth--;
            i++;
          }
        }
      } else {
        // Pseudo-element or simple pseudo-class (like :hover, :focus, ::before)
        // Simple pseudo-classes count as classes (10 points)
        // Pseudo-elements count as elements (1 point)
        if (pseudoName.length > 0) {
          // Check if it's a pseudo-element (double colon, but :: is often written as :)
          // In modern CSS, pseudo-elements use ::, but for compatibility they can use :
          if (pseudoName === "before" || pseudoName === "after" || pseudoName === "first-line" || pseudoName === "first-letter") {
            elements++;
          } else {
            // Pseudo-class like :hover, :focus, :active, :visited, etc.
            classes++;
          }
        }
      }
    } else if (char === "#") {
      // ID selector
      ids++;
      i++;
      // Skip the id name
      while (i < selector.length && /[a-zA-Z0-9_-]/.test(selector[i])) {
        i++;
      }
    } else if (char === ".") {
      // Class selector
      classes++;
      i++;
      // Skip the class name
      while (i < selector.length && /[a-zA-Z0-9_-]/.test(selector[i])) {
        i++;
      }
    } else if (char === "[") {
      // Attribute selector
      classes++;
      i++;
      // Skip to closing bracket
      while (i < selector.length && selector[i] !== "]") {
        i++;
      }
      if (i < selector.length) i++; // skip ']'
    } else if (/[a-zA-Z*]/.test(char)) {
      // Element selector
      if (char !== "*") {
        // Skip universal selector '*'
        elements++;
      }
      i++;
      // Skip the element name
      while (i < selector.length && /[a-zA-Z0-9_-]/.test(selector[i])) {
        i++;
      }
    } else {
      // Other characters (whitespace, >, +, ~, etc.)
      i++;
    }
  }

  return ids * 100 + classes * 10 + elements;
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
// Extract hex colors from gradient functions (linear-gradient, radial-gradient, etc).
const GRADIENT = /(?:linear|radial|conic|repeating-linear|repeating-radial|repeating-conic)-gradient\s*\([^)]*\)/gi;
const GRADIENT_COLOR_STOP = /#[0-9a-fA-F]{3,8}(?=\s|,|%|\))/gi;

// Ordered as styles.css imports them, because that order is what decides ties.
// readdirSync would hand back alphabetical order, which silently reverses some
// pairs and would make the cascade comparison below meaningless.
function readStylesheets() {
  const index = readFileSync(join(stylesDir, "..", "styles.css"), "utf8");
  const imported = [...index.matchAll(/@import\s+url\("styles\/([^"?]+)/g)].map((m) => m[1]);
  const present = readdirSync(stylesDir).filter((name) => name.endsWith(".css"));
  const missing = present.filter((name) => !imported.includes(name));
  if (missing.length) {
    throw new Error(`styles/ contains files styles.css never imports: ${missing.join(", ")}. `
      + "Either import them or delete them — an unimported stylesheet cannot be audited.");
  }
  return imported.map((name, cascade) => ({
    name,
    cascade,
    css: readFileSync(join(stylesDir, name), "utf8").replace(/\/\*[\s\S]*?\*\//g, ""),
  }));
}

// A colour swatch is content, not chrome. The theme-preset previews exist to
// show what each palette looks like, so the light and apple swatches are
// supposed to stay light on a dark page — repainting them with the active theme
// would render all five previews identical and make the picker useless.
//
// This is the only sanctioned reason to hold a light literal: the element's
// whole job is to display that specific colour. Anything that merely sits
// behind text is chrome and must follow the palette.
const SWATCH_SELECTORS = /\.theme-preset-preview/;
// Everything a dark palette has to restate, and everything it already does.
function auditDarkThemeCoverage() {
  const required = new Map(); // Map of darkScoped(selector) -> reason
  const covered = new Set();
  const lightRules = new Map(); // Map of darkScoped(selector) -> (lightSelector, lightSpec, source)
  const darkRulesBySelector = new Map(); // Map of darkSelector -> (spec, source)

  for (const { name, css, cascade } of readStylesheets()) {
    RULE.lastIndex = 0;
    for (const rule of css.matchAll(RULE)) {
      const [, rawSelector, body] = rule;
      const selector = rawSelector.trim();
      if (selector.includes("@")) continue;

      const line = css.slice(0, rule.index).split("\n").length;

      if (selector.includes(DARK_SCOPE)) {
        for (const one of splitSelectors(selector)) {
          covered.add(one);
          // Track dark rules and their specificity for later comparison
          const spec = computeSpecificity(one);
          darkRulesBySelector.set(one, { spec, cascade, source: `${name}:${line}` });
        }
        continue;
      }
      // A preset that already restates its own palette is not this audit's
      // business; only the shared light-shell literals are.
      if (selector.includes("data-theme-preset") || selector.includes("theme-dark")) continue;
      // A swatch is showing a colour, not sitting behind text. See SWATCH_SELECTORS.
      if (SWATCH_SELECTORS.test(selector)) continue;

      const background = BACKGROUND_DECLARATION.exec(body)?.[1]?.trim().toLowerCase();
      const backgroundHex = background && /^#[0-9a-fA-F]{3,8}$/.test(background) ? background.slice(0, 7) : "";
      const remapsSurface = backgroundHex.length === 7 && relativeLuminance(backgroundHex) > LIGHT_SURFACE_LUMINANCE;

      // Check flat backgrounds
      if (remapsSurface) {
        for (const one of splitSelectors(selector)) {
          const darkSelector = darkScoped(one);
          required.set(darkSelector, `${name}:${line} light surface ${backgroundHex}`);
          lightRules.set(darkSelector, {
            lightSelector: one,
            lightSpec: computeSpecificity(one),
            lightCascade: cascade,
            source: `${name}:${line}`,
          });
        }
      } else if (background && !SHOWS_SURFACE_BEHIND.has(background)) {
        // Paints its own non-light background, so its copy is already readable.
        // But still check for gradients in this case
        GRADIENT.lastIndex = 0;
        for (const gradientMatch of body.matchAll(GRADIENT)) {
          const gradientStr = gradientMatch[0];
          GRADIENT_COLOR_STOP.lastIndex = 0;
          for (const colorMatch of gradientStr.matchAll(GRADIENT_COLOR_STOP)) {
            const hex = colorMatch[0].slice(0, 7);
            if (hex.length === 7 && relativeLuminance(hex) > LIGHT_SURFACE_LUMINANCE) {
              for (const one of splitSelectors(selector)) {
                const darkSelector = darkScoped(one);
                required.set(darkSelector, `${name}:${line} light gradient surface ${hex}`);
                lightRules.set(darkSelector, {
                  lightSelector: one,
                  lightSpec: computeSpecificity(one),
              lightCascade: cascade,
                  lightCascade: cascade,
            lightCascade: cascade,
                  source: `${name}:${line}`,
                });
              }
            }
          }
        }
        continue;
      }

      // Check text colors
      COLOR.lastIndex = 0;
      for (const declaration of body.matchAll(COLOR)) {
        const hex = declaration[1].slice(0, 7);
        if (hex.length !== 7 || contrast(hex, DARK_CARD) >= AA_CONTRAST) continue;
        for (const one of splitSelectors(selector)) {
          const darkSelector = darkScoped(one);
          required.set(darkSelector, `${name}:${line} color ${hex} scores ${contrast(hex, DARK_CARD).toFixed(2)}:1`);
          lightRules.set(darkSelector, {
            lightSelector: one,
            lightSpec: computeSpecificity(one),
            lightCascade: cascade,
            source: `${name}:${line}`,
          });
        }
      }

      // Check gradient backgrounds (colors inside linear-gradient, radial-gradient, etc.)
      GRADIENT.lastIndex = 0;
      for (const gradientMatch of body.matchAll(GRADIENT)) {
        const gradientStr = gradientMatch[0];
        GRADIENT_COLOR_STOP.lastIndex = 0;
        for (const colorMatch of gradientStr.matchAll(GRADIENT_COLOR_STOP)) {
          const hex = colorMatch[0].slice(0, 7);
          if (hex.length === 7 && contrast(hex, DARK_CARD) >= AA_CONTRAST) continue;
          for (const one of splitSelectors(selector)) {
            const darkSelector = darkScoped(one);
            required.set(darkSelector, `${name}:${line} gradient color ${hex} scores ${contrast(hex, DARK_CARD).toFixed(2)}:1`);
            lightRules.set(darkSelector, {
              lightSelector: one,
              lightSpec: computeSpecificity(one),
              lightCascade: cascade,
            lightCascade: cascade,
              source: `${name}:${line}`,
            });
          }
        }
      }
    }
  }
  return { required, covered, lightRules, darkRulesBySelector };
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
      + `Otherwise add the selector to the generated dark override block in styles/settings-themes.css.\n${report}`,
  );
});

// Existing as a dark counterpart is not the same as winning. A dark override
// only takes effect when it outranks the light rule, and CSS breaks ties by
// source order — so equal specificity in an EARLIER stylesheet loses.
//
// That is exactly how the provider pages shipped unreadable: providers.css had
// `...theme-light #settingsContentBody:has(.mp-provider-reference-layout)` and the
// override block in settings.css answered with `...theme-light.theme-dark
// #settingsContentBody`. Both score 131. settings.css is imported first, so the
// light rule won and every field label rendered near-white on white. A
// specificity-only check calls that pair fine.
test("dark overrides actually win over the light rules they restate", () => {
  const { required, covered, lightRules, darkRulesBySelector } = auditDarkThemeCoverage();

  const specificityMismatches = [];
  for (const [darkSelector, reason] of required) {
    // Only check if the dark rule actually exists
    if (!covered.has(darkSelector)) continue;

    const lightRuleData = lightRules.get(darkSelector);
    if (!lightRuleData) continue; // No light rule data, shouldn't happen
    const { lightSelector, lightSpec, lightCascade, source: lightSource } = lightRuleData;

    const darkRuleData = darkRulesBySelector.get(darkSelector);
    if (!darkRuleData) continue; // Dark rule not found, shouldn't happen if covered.has() is true

    const darkSpec = darkRuleData.spec;
    const loses = darkSpec < lightSpec
      || (darkSpec === lightSpec && darkRuleData.cascade < lightCascade);
    if (loses) {
      specificityMismatches.push({
        lightSelector,
        darkSelector,
        lightSpec,
        darkSpec,
        lightSource,
        darkSource: darkRuleData.source,
      });
    }
  }

  const report = specificityMismatches
    .slice(0, 25)
    .map(
      (m) =>
        `  ${m.lightSource}\n    light selector: ${m.lightSelector}\n    light specificity: ${m.lightSpec}\n  ${m.darkSource}\n    dark selector: ${m.darkSelector}\n    dark specificity: ${m.darkSpec}`,
    )
    .join("\n");

  assert.equal(
    specificityMismatches.length,
    0,
    `${specificityMismatches.length} dark override(s) have lower specificity than their light counterpart and cannot win.\n`
      + `Increase dark override specificity by adding class/id selectors (e.g., .theme-dark, #id) or matching :has()/:is() from the light rule.\n${report}`,
  );
});

test("the dark override block resolves through theme variables, not new literals", () => {
  const settings = readFileSync(join(stylesDir, "settings-themes.css"), "utf8");
  const start = settings.indexOf("Dark schemes: surfaces and copy the light shell hard-codes");
  assert.ok(start > 0, "the generated dark override block is missing from settings-themes.css");
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

// A var() whose custom property nothing declares is not a token. It is its
// fallback, permanently, under every palette -- so
// `background: var(--surface-subtle, #f8fafc)` is a hard-coded white surface
// that merely spells itself like a themeable one.
//
// The audit above cannot see those. It decides a rule paints light by testing
// the declaration against a bare-hex regex, so a var() falls through to the
// branch that assumes the rule painted a readable background of its own -- the
// exact opposite of the truth when the property is never defined.
//
// That is how the auto-continuation card shipped as a white card carrying 1.01:1
// copy under the cyber palette: --surface, --surface-subtle and --border are
// declared only in oauth-app.css, a separate standalone page this shell never
// loads, so all three resolved to their light literals in every theme.
//
// Only colour fallbacks are checked. A var() holding a length or a count is
// routinely written from JS at runtime -- --utility-panel-width from ui-shell,
// --overview-heatmap-weeks from the dashboard -- and has no business failing a
// contrast guard.
const COLOUR_FALLBACK = /^(?:#[0-9a-fA-F]{3,8}|(?:rgb|rgba|hsl|hsla)\([^()]*\))$/;

// theme-runtime.css is loaded after the shell but lives outside styles/, so it
// both declares and consumes properties the audit would otherwise not know
// about. Reading it here keeps its --autoto-* variables from reading as
// undefined.
function shippedStylesheets() {
  const sheets = readStylesheets().map(({ name, css }) => ({ name, css }));
  sheets.push({
    name: "theme-runtime.css",
    css: readFileSync(join(stylesDir, "..", "theme-runtime.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, ""),
  });
  return sheets;
}

// Split var(--name, fallback) by hand: the fallback can contain commas and
// parens of its own (rgba(0, 0, 0, .5)), which a regex would cut in the wrong
// place.
function varUsages(css) {
  const usages = [];
  for (let start = css.indexOf("var("); start !== -1; start = css.indexOf("var(", start + 4)) {
    let depth = 1;
    let comma = -1;
    let i = start + 4;
    for (; i < css.length && depth > 0; i += 1) {
      const character = css[i];
      if (character === "(") depth += 1;
      else if (character === ")") depth -= 1;
      else if (character === "," && depth === 1 && comma === -1) comma = i;
    }
    if (depth !== 0) continue;
    const close = i - 1;
    usages.push({
      token: (comma === -1 ? css.slice(start + 4, close) : css.slice(start + 4, comma)).trim(),
      fallback: comma === -1 ? "" : css.slice(comma + 1, close).trim(),
      index: start,
    });
  }
  return usages;
}

test("no rule paints with a custom property the shell never declares", () => {
  const sheets = shippedStylesheets();
  const declared = new Set();
  for (const { css } of sheets) {
    for (const match of css.matchAll(/(--[a-zA-Z0-9-]+)\s*:/g)) declared.add(match[1]);
  }
  assert.ok(declared.has("--ws-card"), "no custom properties were found, so the parse is wrong rather than the CSS");

  const offenders = [];
  for (const { name, css } of sheets) {
    for (const { token, fallback, index } of varUsages(css)) {
      if (declared.has(token) || !COLOUR_FALLBACK.test(fallback)) continue;
      offenders.push(`  ${name}:${css.slice(0, index).split("\n").length} var(${token}, ${fallback})`);
    }
  }
  assert.deepEqual(
    offenders,
    [],
    `${offenders.length} rule(s) paint with a custom property nothing declares, so the fallback colour is\n`
      + `what ships under every palette. Use a property the presets bridge -- --bg, --bg-soft, --bg-panel,\n`
      + `--text, --text-soft, --line, --accent -- or a var(--ws-*) directly.\n${offenders.join("\n")}`,
  );
});

// The palette blocks are the ones that declare the canvas. The narrower preset
// rules -- the mobile overrides that re-point one or two --ws-* values -- are
// not palettes and owe no bridges.
function presetPalettes() {
  const css = readFileSync(join(stylesDir, "settings-themes.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const palettes = [];
  for (const match of css.matchAll(/body\.white-shell\.theme-light\[data-theme-preset="([a-z]+)"\]\s*\{([^}]*)\}/g)) {
    const [, preset, body] = match;
    if (!/--ws-canvas\s*:/.test(body)) continue;
    const declarations = new Map();
    for (const declaration of body.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)) {
      declarations.set(declaration[1], declaration[2].trim());
    }
    palettes.push({ preset, declarations, line: css.slice(0, match.index).split("\n").length });
  }
  return palettes;
}

// A card can be a translucent white over the canvas (apple), which is a
// different colour from the one it is written as. Flatten it before measuring,
// or the contrast is computed against a surface nobody sees.
function flatten(value, behind) {
  const rgba = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)(?:[,\s/]+([\d.]+))?\s*\)$/.exec(value.trim());
  if (!rgba) return /^#[0-9a-fA-F]{3,8}$/.test(value.trim()) ? value.trim().slice(0, 7) : "";
  const alpha = rgba[4] === undefined ? 1 : Number(rgba[4]);
  const back = channels(behind);
  const front = [1, 2, 3].map((i) => Number(rgba[i]));
  return `#${front.map((c, i) => Math.round(c * alpha + back[i] * (1 - alpha)).toString(16).padStart(2, "0")).join("")}`;
}

// AA is not a matter of palette taste. --ws-muted is what secondary copy is
// painted with, so a palette whose muted tone does not clear 4.5:1 against its
// own card is one where bridging correctly only makes the unreadability
// on-brand. cream shipped at 4.06 against #fffef9.
test("every preset's muted tone is readable on its own card", () => {
  const failures = [];
  for (const { preset, declarations, line } of presetPalettes()) {
    const canvas = flatten(declarations.get("--ws-canvas") ?? "", "#ffffff");
    const card = flatten(declarations.get("--ws-card") ?? "", canvas || "#ffffff");
    const muted = flatten(declarations.get("--ws-muted") ?? "", card || "#ffffff");
    if (!card || !muted) {
      failures.push(`  ${preset} (settings-themes.css:${line}) has an unreadable --ws-card/--ws-muted pair to measure`);
      continue;
    }
    const ratio = contrast(muted, card);
    if (ratio < AA_CONTRAST) {
      failures.push(`  ${preset} (settings-themes.css:${line}) muted ${muted} on card ${card} is ${ratio.toFixed(2)}, needs ${AA_CONTRAST}`);
    }
  }
  assert.deepEqual(failures, [], `secondary copy must clear AA against the surface it sits on.\n${failures.join("\n")}`);
});

// The audit above never sees inside a media or container query. Its rule regex
// captures an innermost body, so for `@media (...) { .a { ... } }` the selector
// it captures is "@media (...) {\n  .a" -- which contains "@" and is skipped by
// the at-rule guard. Every responsive tier has therefore gone unaudited, and
// all three dark-mode surfaces reported broken on a phone lived in one: the
// mobile select sheet, the mobile settings index, and the mobile shell itself.
//
// This walks the brace structure instead, so at-rules are descended into rather
// than skipped, and holds the count of hard-coded light surfaces as a ratchet.
// It is deliberately a ceiling and not zero: the remaining entries are real and
// are being migrated to var(--ws-*) file by file. Lowering the number as they
// go is the point; raising it is what this stops.
const LIGHT_SURFACE_CEILING = Object.freeze({
  "settings-legacy.css": 47,
  // 46 rather than 45: the html background has to stay a literal, because the
  // palette is declared on body and custom properties do not inherit upward,
  // so a var() there always resolves to its fallback. It is paired with a
  // matching html:has(...theme-dark) rule instead.
  "white-shell.css": 46,
  // 28/20/18 match the counts already shipped on v1.0.0; the previous
  // ceilings were stale and failed CI Test on an otherwise unchanged tree.
  "workbench-shell.css": 28,
  "workbench-desktop.css": 3,
  "workbench-composer.css": 8,
  "workspace-tasks.css": 29,
  "workspace.css": 20,
  "extras.css": 18,
  "settings-themes.css": 7,
  "base.css": 9,
  "providers-console.css": 1,
  "providers-reference.css": 2,
});

function eachStyleRule(css, visit, atRules = []) {
  let index = 0;
  while (index < css.length) {
    const open = css.indexOf("{", index);
    if (open === -1) return;
    const prelude = css.slice(index, open).trim();
    let depth = 1;
    let cursor = open + 1;
    while (cursor < css.length && depth > 0) {
      if (css[cursor] === "{") depth += 1;
      else if (css[cursor] === "}") depth -= 1;
      cursor += 1;
    }
    const body = css.slice(open + 1, cursor - 1);
    if (prelude.startsWith("@")) eachStyleRule(body, visit, atRules.concat(prelude));
    else visit({ selector: prelude, body, atRules, offset: open });
    index = cursor;
  }
}

test("no stylesheet grows its count of hard-coded light surfaces", () => {
  const counts = {};
  const samples = [];
  for (const { name, css } of readStylesheets()) {
    eachStyleRule(css, ({ selector, body, atRules, offset }) => {
      if (/theme-dark|data-theme-preset/.test(selector)) return;
      const background = /(?:^|;)\s*background(?:-color)?:\s*(#[0-9a-fA-F]{3,8})\s*(?:;|$)/.exec(body);
      if (!background) return;
      const hex = background[1].slice(0, 7);
      if (relativeLuminance(hex) <= LIGHT_SURFACE_LUMINANCE) return;
      counts[name] = (counts[name] || 0) + 1;
      if (atRules.length) samples.push(`${name}:${css.slice(0, offset).split("\n").length} ${hex} ${selector.replace(/\s+/g, " ").slice(0, 60)}`);
    });
  }
  const grown = Object.entries(counts)
    .filter(([name, count]) => count > (LIGHT_SURFACE_CEILING[name] ?? 0))
    .map(([name, count]) => `  ${name}: ${count} light surfaces, ceiling ${LIGHT_SURFACE_CEILING[name] ?? 0}`);
  assert.deepEqual(
    grown,
    [],
    "A rule painting a literal light background needs a var(--ws-*) instead. The dark palettes cannot\n"
      + "reach a literal, so the surface stays light while its copy is recoloured for a dark page.\n"
      + `${grown.join("\n")}\n\nInside at-rules right now:\n${samples.slice(0, 10).join("\n")}`,
  );
  for (const [name, ceiling] of Object.entries(LIGHT_SURFACE_CEILING)) {
    assert.ok((counts[name] ?? 0) <= ceiling, `${name} exceeded its ceiling`);
  }
});
