import { readFileSync } from "node:fs";
import { readFile } from "node:fs/promises";

// Test helper. The production stylesheet is split into cascade-ordered modules
// under styles/ that styles.css lists via @import. The server concatenates
// those imports when serving GET /ui/styles.css; tests that assert on the full
// CSS text resolve the same imports here so selector pins keep working.
export async function readStylesSource(stylesUrl) {
  const entry = await readFile(stylesUrl, "utf8");
  const imports = [...entry.matchAll(/@import\s+url\("([^"]+)"\)/g)].map((m) => String(m[1]).replace(/\?.*$/, ""));
  if (imports.length === 0) return entry;
  const parts = await Promise.all(imports.map((rel) => readFile(new URL(rel, stylesUrl), "utf8")));
  return parts.join("");
}

// Former single-file sheets, now consecutive @imports in styles.css. Tests that
// used to read workbench.css / settings.css / providers.css concatenate these
// pieces so selector pins keep working after the split.
export const splitStylesheetGroups = Object.freeze({
  "workbench.css": [
    "workbench-shell.css",
    "workbench-desktop.css",
    "workbench-conversation.css",
    "workbench-composer.css",
  ],
  "settings.css": [
    "settings-system.css",
    "settings-skills.css",
    "settings-themes.css",
  ],
  "providers.css": [
    "providers-console.css",
    "providers-create.css",
    "providers-reference.css",
  ],
});

export function readStylesGroupSync(name, metaUrl) {
  const files = splitStylesheetGroups[name] || [name];
  return files.map((file) => readFileSync(new URL(`../styles/${file}`, metaUrl), "utf8")).join("");
}

export async function readStylesGroup(name, metaUrl) {
  const files = splitStylesheetGroups[name] || [name];
  const parts = await Promise.all(
    files.map((file) => readFile(new URL(`../styles/${file}`, metaUrl), "utf8")),
  );
  return parts.join("");
}
