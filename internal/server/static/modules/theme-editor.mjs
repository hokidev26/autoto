// In-app theme editor: builds schema v3 manifests from form state, previews
// them live by writing the same CSS variables the server generator emits, and
// installs them through the manifest-only create endpoint. No user CSS ever
// leaves this module -- only structured token data.
import { escapeAttr, escapeHtml } from "./dom.mjs";

export const themeTokenKeys = Object.freeze([
  "canvas", "sidebar", "card", "input", "text", "muted",
  "border", "primary", "secondary", "danger", "terminal", "message",
]);

export const themeStatusKeys = Object.freeze(["success", "warning", "attention", "info"]);

// Mirrors the alias emission in internal/themes/css.go so the live preview
// paints exactly the surfaces the installed stylesheet will paint.
const previewVariableMap = Object.freeze({
  canvas: ["--autoto-color-canvas", "--ws-bg", "--ws-canvas"],
  sidebar: ["--autoto-color-sidebar", "--ws-sidebar"],
  card: ["--autoto-color-card", "--ws-surface", "--ws-card"],
  input: ["--autoto-color-input", "--ws-surface-muted", "--ws-input"],
  text: ["--autoto-color-text", "--ws-text"],
  muted: ["--autoto-color-muted", "--ws-muted"],
  border: ["--autoto-color-border", "--ws-border"],
  primary: ["--autoto-color-primary", "--ws-primary"],
  secondary: ["--autoto-color-secondary", "--ws-primary-strong", "--autoto-theme-secondary"],
  danger: ["--autoto-color-danger", "--ws-danger", "--autoto-theme-danger"],
  terminal: ["--autoto-color-terminal", "--ws-terminal", "--autoto-theme-terminal"],
  message: ["--autoto-color-message", "--ws-message-user", "--autoto-theme-message-user"],
});

const statusVariableMap = Object.freeze({
  success: ["--autoto-status-success"],
  warning: ["--autoto-status-warning"],
  attention: ["--autoto-status-attention"],
  info: ["--autoto-status-info"],
});

const defaultLightTokens = Object.freeze({
  canvas: "#f5f7fb", sidebar: "#eef2f9", card: "#ffffff", input: "#f1f5fb",
  text: "#1f2937", muted: "#5b6474", border: "#d7deea", primary: "#3b82f6",
  secondary: "#8b5cf6", danger: "#dc2626", terminal: "#101623", message: "#e8f0fe",
});

const defaultDarkTokens = Object.freeze({
  canvas: "#0b0f1a", sidebar: "#101726", card: "#141c2e", input: "#1b2438",
  text: "#eaf1fa", muted: "#9db1c8", border: "#33415c", primary: "#8fb8e8",
  secondary: "#c4b5fd", danger: "#f87171", terminal: "#05070c", message: "#182338",
});

const defaultMaterials = Object.freeze({
  canvas: { kind: "solid", opacity: 1, blur: 0, radius: 0, shadow: "none" },
  sidebar: { kind: "translucent", opacity: 0.96, blur: 10, radius: 0, shadow: "soft" },
  card: { kind: "translucent", opacity: 0.96, blur: 8, radius: 16, shadow: "soft" },
  input: { kind: "solid", opacity: 1, blur: 0, radius: 12, shadow: "none" },
  terminal: { kind: "solid", opacity: 1, blur: 0, radius: 12, shadow: "medium" },
  message: { kind: "translucent", opacity: 0.94, blur: 8, radius: 16, shadow: "soft" },
});

// The manifest ID grammar is strict lowercase kebab (server-validated); the
// slug is derived from the display name so most users never think about it.
export function slugifyThemeID(value) {
  return String(value || "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/-+$/, "");
}

// Color inputs only speak #rrggbb; manifests may carry #rgb/#rgba/#rrggbbaa.
// Alpha is dropped for editing -- the editor writes opaque palettes.
export function normalizeHexColor(value, fallback = "#000000") {
  const raw = String(value || "").trim().toLowerCase();
  if (/^#[0-9a-f]{6}([0-9a-f]{2})?$/.test(raw)) return raw.slice(0, 7);
  if (/^#[0-9a-f]{3,4}$/.test(raw)) {
    return `#${raw[1]}${raw[1]}${raw[2]}${raw[2]}${raw[3]}${raw[3]}`;
  }
  return fallback;
}

export function defaultThemeEditorDraft() {
  return {
    id: "",
    name: "",
    version: "1.0.0",
    description: "",
    author: "",
    colorScheme: "light",
    tokens: { ...defaultLightTokens },
    materials: JSON.parse(JSON.stringify(defaultMaterials)),
    statusEnabled: false,
    statusTokens: { success: "#16a34a", warning: "#d97706", attention: "#dc2626", info: "#3b82f6" },
    darkEnabled: false,
    darkTokens: { ...defaultDarkTokens },
  };
}

// Prefills the editor from an installed theme's manifest ("start from...").
// Backgrounds, icons and previews are image resources the manifest-only save
// path cannot carry, so they are intentionally not copied into the draft.
export function draftFromManifest(manifest = {}) {
  const draft = defaultThemeEditorDraft();
  draft.name = String(manifest.name || "").slice(0, 120);
  draft.version = String(manifest.version || "1.0.0").slice(0, 64);
  draft.description = String(manifest.description || "").slice(0, 500);
  draft.author = String(manifest.author || "").slice(0, 120);
  draft.colorScheme = manifest.colorScheme === "dark" ? "dark" : "light";
  const base = draft.colorScheme === "dark" ? defaultDarkTokens : defaultLightTokens;
  for (const key of themeTokenKeys) {
    draft.tokens[key] = normalizeHexColor(manifest.tokens?.[key], base[key]);
  }
  if (manifest.materials && typeof manifest.materials === "object") {
    for (const surface of Object.keys(defaultMaterials)) {
      const material = manifest.materials[surface];
      if (material && typeof material === "object") draft.materials[surface] = { ...material };
    }
  }
  if (manifest.statusTokens && typeof manifest.statusTokens === "object") {
    draft.statusEnabled = true;
    for (const key of themeStatusKeys) {
      if (manifest.statusTokens[key]) draft.statusTokens[key] = normalizeHexColor(manifest.statusTokens[key], draft.statusTokens[key]);
    }
  }
  if (manifest.darkTokens && typeof manifest.darkTokens === "object") {
    draft.darkEnabled = true;
    for (const key of themeTokenKeys) {
      draft.darkTokens[key] = normalizeHexColor(manifest.darkTokens[key], defaultDarkTokens[key]);
    }
  }
  return draft;
}

export function manifestFromDraft(draft = {}) {
  const name = String(draft.name || "").trim().slice(0, 120);
  const id = slugifyThemeID(draft.id || name);
  const manifest = {
    schemaVersion: 3,
    id,
    name: name || id,
    version: String(draft.version || "1.0.0").trim() || "1.0.0",
    description: String(draft.description || "").trim().slice(0, 500),
    author: String(draft.author || "").trim().slice(0, 120),
    colorScheme: draft.colorScheme === "dark" ? "dark" : "light",
    tokens: {},
    materials: JSON.parse(JSON.stringify(draft.materials || defaultMaterials)),
  };
  const base = manifest.colorScheme === "dark" ? defaultDarkTokens : defaultLightTokens;
  for (const key of themeTokenKeys) {
    manifest.tokens[key] = normalizeHexColor(draft.tokens?.[key], base[key]);
  }
  if (draft.statusEnabled) {
    manifest.statusTokens = {};
    for (const key of themeStatusKeys) {
      manifest.statusTokens[key] = normalizeHexColor(draft.statusTokens?.[key], "#000000");
    }
  }
  if (draft.darkEnabled) {
    manifest.darkTokens = {};
    for (const key of themeTokenKeys) {
      manifest.darkTokens[key] = normalizeHexColor(draft.darkTokens?.[key], defaultDarkTokens[key]);
    }
  }
  return manifest;
}

// Inline body styles beat every stylesheet, which is exactly what a preview
// wants: no waiting for an install, no interference from the active theme.
export function previewVariablesForDraft(draft = {}) {
  const variables = {};
  const dark = draft.darkEnabled && draft.previewDark === true;
  const palette = dark ? draft.darkTokens : draft.tokens;
  const base = dark ? defaultDarkTokens : defaultLightTokens;
  for (const key of themeTokenKeys) {
    const value = normalizeHexColor(palette?.[key], base[key]);
    for (const variable of previewVariableMap[key]) variables[variable] = value;
  }
  if (draft.statusEnabled) {
    for (const key of themeStatusKeys) {
      const value = normalizeHexColor(draft.statusTokens?.[key], "#000000");
      for (const variable of statusVariableMap[key]) variables[variable] = value;
    }
  }
  return variables;
}

export function applyThemeEditorPreview(draft, documentRef = globalThis.document) {
  const body = documentRef?.body;
  if (!body?.style?.setProperty) return false;
  clearThemeEditorPreview(documentRef);
  const variables = previewVariablesForDraft(draft);
  for (const [variable, value] of Object.entries(variables)) {
    body.style.setProperty(variable, value);
  }
  if (body.dataset) body.dataset.themeEditorPreview = "true";
  return true;
}

export function clearThemeEditorPreview(documentRef = globalThis.document) {
  const body = documentRef?.body;
  if (!body?.style?.removeProperty) return;
  const allVariables = new Set();
  for (const list of Object.values(previewVariableMap)) for (const variable of list) allVariables.add(variable);
  for (const list of Object.values(statusVariableMap)) for (const variable of list) allVariables.add(variable);
  for (const variable of allVariables) body.style.removeProperty(variable);
  if (body.dataset) delete body.dataset.themeEditorPreview;
}

function colorField(scope, key, label, value) {
  return `
    <label class="theme-editor-color">
      <input type="color" data-theme-editor-color="${escapeAttr(scope)}:${escapeAttr(key)}" value="${escapeAttr(normalizeHexColor(value))}" />
      <span>${escapeHtml(label)}</span>
    </label>
  `;
}

export function createThemeEditorController({
  themeManager,
  t,
  showToast,
  showError,
  refreshActiveSettingsPanel,
  confirmAction,
  documentRef = globalThis.document,
} = {}) {
  const state = {
    open: false,
    saving: false,
    previewing: false,
    draft: defaultThemeEditorDraft(),
  };

  function translate(key, values) {
    return typeof t === "function" ? t(key, values) : key;
  }

  function tokenLabel(key) {
    return translate(`appearance.themeToken.${key}`);
  }

  function open(draft = defaultThemeEditorDraft()) {
    state.open = true;
    state.draft = draft;
    refreshActiveSettingsPanel?.();
  }

  function close() {
    state.open = false;
    stopPreview();
    refreshActiveSettingsPanel?.();
  }

  function stopPreview() {
    if (!state.previewing) return;
    state.previewing = false;
    clearThemeEditorPreview(documentRef);
  }

  function syncPreview() {
    if (state.previewing) applyThemeEditorPreview(state.draft, documentRef);
  }

  function renderSection() {
    if (!state.open) return "";
    const draft = state.draft;
    const paletteFields = themeTokenKeys.map((key) => colorField("tokens", key, tokenLabel(key), draft.tokens[key])).join("");
    const statusFields = themeStatusKeys.map((key) => colorField("status", key, translate(`appearance.themeStatus.${key}`), draft.statusTokens[key])).join("");
    const darkFields = themeTokenKeys.map((key) => colorField("dark", key, tokenLabel(key), draft.darkTokens[key])).join("");
    return `
      <div class="theme-editor" data-theme-editor>
        <div class="theme-editor-head">
          <h3>${escapeHtml(translate("appearance.themeEditorTitle"))}</h3>
          <p data-settings-help-copy>${escapeHtml(translate("appearance.themeEditorMeta"))}</p>
        </div>
        <div class="theme-editor-meta-grid">
          <label>${escapeHtml(translate("appearance.themeEditorName"))}<input type="text" id="themeEditorName" maxlength="120" value="${escapeAttr(draft.name)}" /></label>
          <label>${escapeHtml(translate("appearance.themeEditorID"))}<input type="text" id="themeEditorID" maxlength="63" placeholder="${escapeAttr(slugifyThemeID(draft.name) || "my-theme")}" value="${escapeAttr(draft.id)}" /></label>
          <label>${escapeHtml(translate("appearance.themeEditorVersion"))}<input type="text" id="themeEditorVersion" maxlength="64" value="${escapeAttr(draft.version)}" /></label>
          <label>${escapeHtml(translate("appearance.themeEditorAuthor"))}<input type="text" id="themeEditorAuthor" maxlength="120" value="${escapeAttr(draft.author)}" /></label>
          <label class="theme-editor-wide">${escapeHtml(translate("appearance.themeEditorDescription"))}<input type="text" id="themeEditorDescription" maxlength="500" value="${escapeAttr(draft.description)}" /></label>
          <label>${escapeHtml(translate("appearance.themeEditorScheme"))}
            <select id="themeEditorScheme">
              <option value="light" ${draft.colorScheme === "light" ? "selected" : ""}>${escapeHtml(translate("appearance.themeLightLabel"))}</option>
              <option value="dark" ${draft.colorScheme === "dark" ? "selected" : ""}>${escapeHtml(translate("appearance.themeDarkLabel"))}</option>
            </select>
          </label>
        </div>
        <h4>${escapeHtml(translate("appearance.themeEditorPalette"))}</h4>
        <div class="theme-editor-color-grid">${paletteFields}</div>
        <label class="theme-editor-toggle"><input type="checkbox" id="themeEditorStatusEnabled" ${draft.statusEnabled ? "checked" : ""} /> ${escapeHtml(translate("appearance.themeEditorStatusToggle"))}</label>
        <div class="theme-editor-color-grid ${draft.statusEnabled ? "" : "hidden"}" id="themeEditorStatusGrid">${statusFields}</div>
        <label class="theme-editor-toggle"><input type="checkbox" id="themeEditorDarkEnabled" ${draft.darkEnabled ? "checked" : ""} /> ${escapeHtml(translate("appearance.themeEditorDarkToggle"))}</label>
        <div class="theme-editor-color-grid ${draft.darkEnabled ? "" : "hidden"}" id="themeEditorDarkGrid">${darkFields}</div>
        <div class="theme-editor-actions">
          <button id="themeEditorPreviewBtn" class="settings-action-btn" type="button">${escapeHtml(translate(state.previewing ? "appearance.themeEditorPreviewStop" : "appearance.themeEditorPreview"))}</button>
          <button id="themeEditorSaveBtn" class="settings-action-btn primary" type="button" ${state.saving ? "disabled" : ""}>${escapeHtml(translate(state.saving ? "appearance.themeEditorSaving" : "appearance.themeEditorSave"))}</button>
          <button id="themeEditorCloseBtn" class="settings-action-btn" type="button">${escapeHtml(translate("appearance.themeEditorClose"))}</button>
        </div>
      </div>
    `;
  }

  async function save() {
    const manifest = manifestFromDraft(state.draft);
    if (!manifest.id) {
      showToast?.(translate("appearance.themeEditorNameRequired"), "warn");
      return;
    }
    state.saving = true;
    refreshActiveSettingsPanel?.();
    try {
      let result;
      try {
        result = await themeManager.createTheme(manifest);
      } catch (error) {
        if (error?.status !== 409) throw error;
        const replace = await Promise.resolve(confirmAction?.(translate("appearance.themeReplaceConfirm")));
        if (!replace) return;
        result = await themeManager.createTheme(manifest, { replace: true });
      }
      stopPreview();
      if (result?.replaced && result.previousVersion && result.theme?.version && result.previousVersion !== result.theme.version) {
        showToast?.(translate("appearance.themeUpdated", { from: result.previousVersion, to: result.theme.version }), "success");
      } else {
        showToast?.(translate("appearance.themeEditorSaved"), "success");
      }
      if (result?.warnings?.length) {
        showToast?.(translate("appearance.themeContrastWarnings", { count: result.warnings.length, pair: result.warnings[0].pair }), "warn");
      }
      state.open = false;
      if (result?.theme?.id) await themeManager.activateTheme(result.theme.id);
    } finally {
      state.saving = false;
      refreshActiveSettingsPanel?.();
    }
  }

  function bindColorInputs() {
    documentRef?.querySelectorAll?.("[data-theme-editor-color]")?.forEach((node) => {
      node.addEventListener("input", () => {
        const [scope, key] = String(node.dataset.themeEditorColor || "").split(":");
        if (scope === "tokens" && themeTokenKeys.includes(key)) state.draft.tokens[key] = node.value;
        else if (scope === "dark" && themeTokenKeys.includes(key)) state.draft.darkTokens[key] = node.value;
        else if (scope === "status" && themeStatusKeys.includes(key)) state.draft.statusTokens[key] = node.value;
        syncPreview();
      });
    });
  }

  function bindField(id, apply) {
    const node = documentRef?.getElementById?.(id);
    node?.addEventListener("input", () => {
      apply(node);
      syncPreview();
    });
    return node;
  }

  function bindSection() {
    if (!state.open) return;
    bindField("themeEditorName", (node) => { state.draft.name = node.value; });
    bindField("themeEditorID", (node) => { state.draft.id = node.value; });
    bindField("themeEditorVersion", (node) => { state.draft.version = node.value; });
    bindField("themeEditorAuthor", (node) => { state.draft.author = node.value; });
    bindField("themeEditorDescription", (node) => { state.draft.description = node.value; });
    documentRef?.getElementById?.("themeEditorScheme")?.addEventListener("change", (event) => {
      state.draft.colorScheme = event.currentTarget.value === "dark" ? "dark" : "light";
      syncPreview();
    });
    documentRef?.getElementById?.("themeEditorStatusEnabled")?.addEventListener("change", (event) => {
      state.draft.statusEnabled = event.currentTarget.checked === true;
      documentRef?.getElementById?.("themeEditorStatusGrid")?.classList?.toggle?.("hidden", !state.draft.statusEnabled);
      syncPreview();
    });
    documentRef?.getElementById?.("themeEditorDarkEnabled")?.addEventListener("change", (event) => {
      state.draft.darkEnabled = event.currentTarget.checked === true;
      documentRef?.getElementById?.("themeEditorDarkGrid")?.classList?.toggle?.("hidden", !state.draft.darkEnabled);
      syncPreview();
    });
    bindColorInputs();
    documentRef?.getElementById?.("themeEditorPreviewBtn")?.addEventListener("click", () => {
      if (state.previewing) {
        stopPreview();
      } else {
        state.previewing = true;
        applyThemeEditorPreview(state.draft, documentRef);
      }
      refreshActiveSettingsPanel?.();
    });
    documentRef?.getElementById?.("themeEditorSaveBtn")?.addEventListener("click", () => {
      save().catch((error) => showError?.(error));
    });
    documentRef?.getElementById?.("themeEditorCloseBtn")?.addEventListener("click", () => close());
  }

  async function openFromTheme(themeID) {
    if (!themeID) {
      open();
      return;
    }
    const manifest = await themeManager.fetchThemeManifest(themeID);
    const draft = draftFromManifest(manifest);
    // A copy needs its own identity so saving never silently overwrites the
    // source theme; the user can still type the original ID to update it.
    draft.name = draft.name ? `${draft.name} Copy` : "";
    draft.id = "";
    open(draft);
  }

  return {
    get isOpen() { return state.open; },
    get draft() { return state.draft; },
    open,
    openFromTheme,
    close,
    renderSection,
    bindSection,
  };
}
