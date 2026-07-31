import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import assert from "node:assert/strict";

const stylesDir = join(dirname(fileURLToPath(import.meta.url)), "..", "styles");
const baseline = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), "theme-tokens.baseline.json"), "utf8"));

// Ordered as styles.css imports them: later files win ties, so any resolver that
// reads the directory listing instead would answer differently from the browser.
function orderedStylesheets() {
  const index = readFileSync(join(stylesDir, "..", "styles.css"), "utf8");
  return [...index.matchAll(/@import\s+url\("styles\/([^"?]+)/g)]
    .map((m) => m[1])
    .map((name) => ({ name, css: readFileSync(join(stylesDir, name), "utf8").replace(/\/\*[\s\S]*?\*\//g, "") }));
}

// Which preset a rule applies to, or null when it is not preset-scoped.
// `theme-dark` is a companion class the dark-scheme presets also carry, so a
// rule naming only that applies to whichever of them is active.
const SCOPING_CONTAINERS = ["#settingsModal", "#settingsContentBody", ".mp-provider-page"];

function presetOf(selector) {
  const explicit = /\[data-theme-preset="([a-z]+)"\]/.exec(selector);
  if (explicit) return explicit[1];
  if (selector.includes(".theme-dark")) return "dark-scheme";
  if (/(^|[\s,])(:root|body)([\s.,:[]|$)/.test(selector)) return "any";
  // The scoped families are declared on their container rather than on body:
  // --settings-* on the settings shell, --mp-* on the provider page. The
  // baseline was read from those same elements, so they have to count here or
  // two thirds of the tokens look undeclared.
  if (SCOPING_CONTAINERS.some((container) => selector.includes(container))) return "any";
  return null;
}

// Declared custom properties per preset, later declarations winning, mirroring
// how the cascade resolves them for a body element carrying that preset.
function declaredTokens(preset) {
  const darkScheme = preset === "dark" || preset === "cyber";
  const resolved = new Map();
  for (const { css } of orderedStylesheets()) {
    for (const rule of css.matchAll(/([^{}]+)\{([^}]*)\}/g)) {
      const [, rawSelector, body] = rule;
      if (rawSelector.includes("@")) continue;
      for (const selector of rawSelector.split(",")) {
        const scope = presetOf(selector.trim());
        if (scope === null) continue;
        if (scope === "dark-scheme" && !darkScheme) continue;
        if (scope !== "any" && scope !== "dark-scheme" && scope !== preset) continue;
        for (const declaration of body.matchAll(/(--[A-Za-z0-9_-]+)\s*:\s*([^;]+)/g)) {
          resolved.set(declaration[1], declaration[2].trim());
        }
      }
    }
  }
  return resolved;
}

// A token whose value is another var() only tells us the alias, not the colour.
// Follow the chain so a consolidation that swaps one alias for another is still
// recognised as equivalent.
function deref(value, table, depth = 0) {
  if (depth > 8) return value;
  const alias = /^var\(\s*(--[A-Za-z0-9_-]+)\s*(?:,\s*([\s\S]+))?\)$/.exec(value.trim());
  if (!alias) return value.trim();
  const [, name, fallback] = alias;
  if (table.has(name)) return deref(table.get(name), table, depth + 1);
  return fallback ? deref(fallback, table, depth + 1) : value.trim();
}

const normalise = (value) => value.replace(/\s+/g, " ").trim().toLowerCase();

// The consolidation ahead moves token definitions between files and collapses
// alias chains. Every one of those edits is supposed to be invisible: the same
// preset must still resolve to the same colour. This compares against values
// captured from a real browser, so a resolver bug here shows up as a mismatch
// rather than as false confidence.
test("each preset resolves its tokens to the captured baseline", () => {
  const drift = [];
  for (const [preset, expected] of Object.entries(baseline.presets)) {
    const table = declaredTokens(preset);
    for (const [token, want] of Object.entries(expected)) {
      if (!table.has(token)) {
        drift.push(`${preset} ${token}: no longer declared anywhere (was ${want})`);
        continue;
      }
      const got = deref(table.get(token), table);
      if (normalise(got) !== normalise(want)) {
        drift.push(`${preset} ${token}: ${got} — baseline says ${want}`);
      }
    }
  }
  assert.deepEqual(
    drift,
    [],
    `${drift.length} token(s) resolve differently from the captured baseline.\n`
      + "If the change was deliberate, recapture the baseline (see below) and say so in the\n"
      + "commit; if it was not, this is the ripple the consolidation was supposed to avoid.\n\n"
      + "Recapture: open the app, and for each preset set body.dataset.themePreset plus the\n"
      + "theme-light/theme-dark classes, then read getComputedStyle for the token list on\n"
      + "body (base and --ws-*) and on #settingsContentBody (--settings-*).\n\n"
      + drift.join("\n"),
  );
});

// A token that references itself resolves to nothing at all: the browser treats
// the cycle as invalid at computed-value time and the property comes back as an
// empty string, so every surface using it silently loses its colour. This is a
// real mistake a bulk literal-to-token replacement makes — the replacement hits
// the token's own definition — and it reads as ordinary drift in the baseline
// test above, which makes it easy to recapture the bug into the baseline
// instead of fixing it. Naming it separately keeps that from happening twice.
test("no token resolves through a cycle or to an undefined reference", () => {
  const broken = [];
  for (const preset of Object.keys(baseline.presets)) {
    const table = declaredTokens(preset);
    for (const [token, value] of table) {
      const seen = new Set([token]);
      let current = value;
      for (;;) {
        const alias = /^var\(\s*(--[A-Za-z0-9_-]+)\s*(?:,\s*([\s\S]+))?\)$/.exec(current.trim());
        if (!alias) break;
        const [, ref, fallback] = alias;
        if (seen.has(ref)) {
          broken.push(`${preset} ${token}: cycles through ${ref} (${value})`);
          break;
        }
        if (!table.has(ref)) {
          if (!fallback) broken.push(`${preset} ${token}: references undefined ${ref} with no fallback`);
          break;
        }
        seen.add(ref);
        current = table.get(ref);
      }
    }
  }
  assert.deepEqual(broken, [], `${broken.length} token(s) do not resolve to a colour:\n${broken.join("\n")}`);
});

// Guards the resolver itself. Without this, a bug that made deref() return the
// raw var() text would make the test above pass by comparing nothing useful.
test("the resolver follows alias chains and fallbacks", () => {
  const table = new Map([
    ["--leaf", "#123456"],
    ["--mid", "var(--leaf)"],
    ["--top", "var(--mid)"],
  ]);
  assert.equal(deref("var(--top)", table), "#123456");
  assert.equal(deref("var(--missing, #abcdef)", table), "#abcdef");
  assert.equal(deref("var(--mid, #000000)", table), "#123456", "a present alias beats its fallback");
  assert.equal(deref("#fedcba", table), "#fedcba");
});

// The baseline is only meaningful if it covers the presets the app offers.
test("the baseline covers every theme preset", () => {
  const presets = readFileSync(join(stylesDir, "..", "modules", "preferences-data.mjs"), "utf8");
  const declared = /appearanceThemePresets\s*=\s*Object\.freeze\(\[([^\]]+)\]/.exec(presets);
  assert.ok(declared, "could not read appearanceThemePresets");
  const names = [...declared[1].matchAll(/"([a-z]+)"/g)].map((m) => m[1]).sort();
  assert.deepEqual(Object.keys(baseline.presets).sort(), names,
    "a preset was added or removed without recapturing the token baseline");
});

// Unused files still get read by the audits and still ship to the browser, so a
// stylesheet nothing imports is dead weight that looks alive.
test("every stylesheet is imported by styles.css", () => {
  const imported = orderedStylesheets().map((sheet) => sheet.name);
  const present = readdirSync(stylesDir).filter((name) => name.endsWith(".css"));
  assert.deepEqual(present.filter((name) => !imported.includes(name)), []);
});
