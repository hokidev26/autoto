import test from "node:test";
import assert from "node:assert/strict";

import {
  applyThemeEditorPreview,
  clearThemeEditorPreview,
  defaultThemeEditorDraft,
  draftFromManifest,
  manifestFromDraft,
  normalizeHexColor,
  previewVariablesForDraft,
  slugifyThemeID,
  themeStatusKeys,
  themeTokenKeys,
} from "./theme-editor.mjs";

test("theme IDs derive from display names under the manifest grammar", () => {
  assert.equal(slugifyThemeID("My Ocean Theme"), "my-ocean-theme");
  assert.equal(slugifyThemeID("  --Fancy__Name!!  "), "fancy-name");
  assert.equal(slugifyThemeID("深色主題"), "");
  assert.equal(slugifyThemeID("a".repeat(100)).length, 63);
  assert.equal(slugifyThemeID(""), "");
});

test("hex normalization expands short forms and drops alpha", () => {
  assert.equal(normalizeHexColor("#ABC"), "#aabbcc");
  assert.equal(normalizeHexColor("#abcd"), "#aabbcc");
  assert.equal(normalizeHexColor("#12345678"), "#123456");
  assert.equal(normalizeHexColor("#123456"), "#123456");
  assert.equal(normalizeHexColor("red", "#0f0f0f"), "#0f0f0f");
  assert.equal(normalizeHexColor("url(javascript:alert(1))", "#0f0f0f"), "#0f0f0f");
});

test("drafts build complete v3 manifests and optional blocks stay optional", () => {
  const draft = defaultThemeEditorDraft();
  draft.name = "Studio Light";
  const manifest = manifestFromDraft(draft);
  assert.equal(manifest.schemaVersion, 3);
  assert.equal(manifest.id, "studio-light");
  assert.equal(manifest.name, "Studio Light");
  assert.equal(manifest.version, "1.0.0");
  assert.equal(manifest.colorScheme, "light");
  for (const key of themeTokenKeys) {
    assert.match(manifest.tokens[key], /^#[0-9a-f]{6}$/);
  }
  for (const surface of ["canvas", "sidebar", "card", "input", "terminal", "message"]) {
    assert.ok(manifest.materials[surface]?.kind, `material ${surface} must survive`);
  }
  assert.equal(manifest.statusTokens, undefined);
  assert.equal(manifest.darkTokens, undefined);

  draft.statusEnabled = true;
  draft.darkEnabled = true;
  const enriched = manifestFromDraft(draft);
  for (const key of themeStatusKeys) {
    assert.match(enriched.statusTokens[key], /^#[0-9a-f]{6}$/);
  }
  for (const key of themeTokenKeys) {
    assert.match(enriched.darkTokens[key], /^#[0-9a-f]{6}$/);
  }
});

test("manifest prefill round-trips tokens and re-derives identity", () => {
  const source = manifestFromDraft({
    ...defaultThemeEditorDraft(),
    name: "Round Trip",
    statusEnabled: true,
    darkEnabled: true,
  });
  const draft = draftFromManifest(source);
  assert.equal(draft.name, "Round Trip");
  assert.equal(draft.statusEnabled, true);
  assert.equal(draft.darkEnabled, true);
  for (const key of themeTokenKeys) {
    assert.equal(draft.tokens[key], source.tokens[key]);
    assert.equal(draft.darkTokens[key], source.darkTokens[key]);
  }
  const rebuilt = manifestFromDraft({ ...draft, id: "" });
  assert.equal(rebuilt.id, "round-trip");
  assert.deepEqual(rebuilt.tokens, source.tokens);
  assert.deepEqual(rebuilt.darkTokens, source.darkTokens);
  assert.deepEqual(rebuilt.statusTokens, source.statusTokens);
});

test("preview writes the generator's variable vocabulary and clears fully", () => {
  const draft = defaultThemeEditorDraft();
  draft.tokens.canvas = "#101010";
  draft.statusEnabled = true;
  draft.statusTokens.warning = "#ff9900";
  const variables = previewVariablesForDraft(draft);
  assert.equal(variables["--ws-canvas"], "#101010");
  assert.equal(variables["--autoto-color-canvas"], "#101010");
  assert.equal(variables["--autoto-status-warning"], "#ff9900");
  assert.equal(variables["--ws-text"], draft.tokens.text);

  const set = new Map();
  const body = {
    dataset: {},
    style: {
      setProperty: (name, value) => set.set(name, value),
      removeProperty: (name) => set.delete(name),
    },
  };
  const documentRef = { body };
  assert.equal(applyThemeEditorPreview(draft, documentRef), true);
  assert.equal(set.get("--ws-canvas"), "#101010");
  assert.equal(body.dataset.themeEditorPreview, "true");
  clearThemeEditorPreview(documentRef);
  assert.equal(set.size, 0);
  assert.equal(body.dataset.themeEditorPreview, undefined);
});

test("dark preview paints the dark palette when the variant is enabled", () => {
  const draft = defaultThemeEditorDraft();
  draft.darkEnabled = true;
  draft.darkTokens.canvas = "#050505";
  draft.previewDark = true;
  const variables = previewVariablesForDraft(draft);
  assert.equal(variables["--ws-canvas"], "#050505");
  const withoutVariant = previewVariablesForDraft({ ...draft, darkEnabled: false });
  assert.equal(withoutVariant["--ws-canvas"], draft.tokens.canvas);
});
