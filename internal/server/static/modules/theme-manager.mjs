import { appearanceThemeForPreset, normalizeAppearanceThemePreset } from "./preferences-data.mjs?v=global-background-1-theme-v2-1";

const packageThemeIDPattern = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;
const themeStylesheetLinkID = "autotoThemeStylesheet";

function safeThemeURL(value) {
  const path = String(value || "").trim();
  return path.startsWith("/themes/") ? path : "";
}

export function normalizeThemeRecord(value = {}) {
  const id = String(value.id || "").trim().toLowerCase();
  const stylesheetUrl = safeThemeURL(value.stylesheetUrl || value.cssUrl);
  if (id.length > 63 || !packageThemeIDPattern.test(id) || !stylesheetUrl) return null;
  const source = value.source === "local" ? "local" : "bundled";
  const revision = String(value.revision || "").trim().slice(0, 128);
  return {
    id,
    name: String(value.name || id).trim().slice(0, 120) || id,
    version: String(value.version || "").trim().slice(0, 64),
    description: String(value.description || "").trim().slice(0, 500),
    author: String(value.author || "").trim().slice(0, 120),
    colorScheme: value.colorScheme === "dark" ? "dark" : "light",
    source,
    revision,
    stylesheetUrl,
    previewUrl: safeThemeURL(value.previewUrl),
    capabilities: {
      background: value.capabilities?.globalBackground === true || value.capabilities?.homeBackground === true || value.capabilities?.background === true || value.supportsBackground === true,
      globalBackground: value.capabilities?.globalBackground === true,
      homeBackground: value.capabilities?.homeBackground === true,
      icons: value.capabilities?.icons === true || value.supportsIcons === true,
      statusTokens: value.capabilities?.statusTokens === true,
      darkVariant: value.capabilities?.darkVariant === true,
    },
    iconVariables: value.iconVariables && typeof value.iconVariables === "object" ? Object.keys(value.iconVariables).slice(0, 32) : [],
    deletable: source === "local" && value.deletable !== false,
  };
}

// Server mutation responses carry the installed theme plus advisory contrast
// warnings and update semantics. Bound and normalize them so a malformed
// payload cannot inject markup or unbounded text into toasts.
export function normalizeThemeMutationResult(payload = {}) {
  const rawWarnings = Array.isArray(payload?.warnings) ? payload.warnings : [];
  return {
    theme: normalizeThemeRecord(payload?.theme || payload),
    replaced: payload?.replaced === true,
    previousVersion: String(payload?.previousVersion || "").trim().slice(0, 64),
    warnings: rawWarnings.slice(0, 32).map((warning) => ({
      pair: String(warning?.pair || "").trim().slice(0, 80),
      ratio: Number(warning?.ratio) || 0,
      minimum: Number(warning?.minimum) || 0,
    })).filter((warning) => warning.pair),
  };
}

export function normalizeThemeCatalog(payload) {
  const rows = Array.isArray(payload) ? payload : payload?.themes;
  if (!Array.isArray(rows)) return [];
  const seen = new Set();
  return rows.reduce((themes, row) => {
    const theme = normalizeThemeRecord(row);
    if (!theme || seen.has(theme.id)) return themes;
    seen.add(theme.id);
    themes.push(theme);
    return themes;
  }, []);
}

function presetFromPreferences(prefs = {}) {
  return normalizeAppearanceThemePreset(prefs.themePreset)
    || normalizeAppearanceThemePreset(prefs.themeRef?.id)
    || "light";
}

function fallbackThemeTranslation(key, values = {}) {
  const messages = {
    "appearance.themeMissing": `Theme ${values.id || ""} is missing. Autoto is temporarily using the base palette.`,
    "appearance.themeChooseFile": "Choose an Autoto theme package first.",
    "appearance.themeNotFound": `Theme ${values.id || ""} was not found.`,
    "appearance.themeBundledDeleteDenied": "Bundled themes cannot be deleted.",
    "appearance.themeEnvironmentUnsupported": "This environment cannot load theme stylesheets.",
    "appearance.themeLoadTimeout": "Theme stylesheet loading timed out.",
    "appearance.themeRequestFailed": "Theme stylesheet request failed.",
    "appearance.themeLoadFailed": `Theme ${values.name || ""} failed to load: ${values.error || ""}`,
  };
  return messages[key] || key;
}

export function setThemePageContext(value, documentRef = globalThis.document) {
  const body = documentRef?.body;
  if (!body?.dataset) return;
  const context = String(value || "").trim();
  if (context) body.dataset.themePage = context;
  else delete body.dataset.themePage;
}

export class ThemeManager {
  constructor({
    api,
    documentRef = globalThis.document,
    windowRef = globalThis.window,
    showToast,
    translate,
    loadTimeoutMs = 8000,
  } = {}) {
    if (typeof api !== "function") throw new TypeError("ThemeManager requires an api function");
    this.api = api;
    this.document = documentRef;
    this.window = windowRef || globalThis;
    this.showToast = showToast;
    this.translate = typeof translate === "function" ? translate : fallbackThemeTranslation;
    this.loadTimeoutMs = loadTimeoutMs;
    this.listeners = new Set();
    this.preferenceAdapter = null;
    this.catalogPromise = null;
    this.catalogSequence = 0;
    this.stylesheetSequence = 0;
    this.missingNoticeID = "";
    this.darkVariantQuery = null;
    this.darkVariantListener = null;
    this.state = {
      status: "idle",
      themes: [],
      error: "",
      activeThemeID: "",
      activeRevision: "",
      missingThemeID: "",
      importing: false,
      deletingThemeID: "",
    };
  }

  setPreferenceAdapter({ currentAppearancePreferences, saveAppearancePreferences } = {}) {
    this.preferenceAdapter = {
      currentAppearancePreferences,
      saveAppearancePreferences,
    };
  }

  snapshot() {
    return {
      ...this.state,
      themes: this.state.themes.map((theme) => ({ ...theme })),
    };
  }

  subscribe(listener) {
    if (typeof listener !== "function") return () => {};
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  emit() {
    const snapshot = this.snapshot();
    for (const listener of this.listeners) {
      try {
        listener(snapshot);
      } catch {}
    }
  }

  updateState(patch) {
    this.state = { ...this.state, ...patch };
    this.emit();
  }

  async loadCatalog({ force = false } = {}) {
    if (!force && this.catalogPromise) return this.catalogPromise;
    if (!force && this.state.status === "ready") return this.state.themes;
    const sequence = ++this.catalogSequence;
    this.updateState({ status: "loading", error: "" });
    const request = this.api("/api/themes")
      .then((payload) => {
        const themes = normalizeThemeCatalog(payload);
        if (sequence === this.catalogSequence) {
          this.updateState({ status: "ready", themes, error: "" });
        }
        return themes;
      })
      .catch((error) => {
        if (sequence === this.catalogSequence) {
          this.updateState({ status: "error", error: String(error?.message || error || "Theme catalog failed") });
        }
        throw error;
      })
      .finally(() => {
        if (this.catalogPromise === request) this.catalogPromise = null;
      });
    this.catalogPromise = request;
    return request;
  }

  findTheme(id) {
    const normalized = String(id || "").trim().toLowerCase();
    return this.state.themes.find((theme) => theme.id === normalized) || null;
  }

  async initialize(prefs) {
    try {
      await this.loadCatalog();
    } catch {
      this.applyPresetFallback(presetFromPreferences(prefs));
      return false;
    }
    return this.applyPreference(prefs, { notifyMissing: false });
  }

  async applyPreference(prefs = {}, { notifyMissing = true } = {}) {
    if (prefs.themeRef?.kind !== "package") {
      this.applyPresetFallback(presetFromPreferences(prefs));
      return true;
    }
    if (this.state.status === "idle" || this.state.status === "error") {
      try {
        await this.loadCatalog({ force: this.state.status === "error" });
      } catch {
        this.applyPresetFallback(presetFromPreferences(prefs));
        return false;
      }
    } else if (this.state.status === "loading" && this.catalogPromise) {
      try {
        await this.catalogPromise;
      } catch {
        this.applyPresetFallback(presetFromPreferences(prefs));
        return false;
      }
    }
    const theme = this.findTheme(prefs.themeRef.id);
    if (!theme) {
      const missingThemeID = String(prefs.themeRef.id || "").trim();
      this.applyPresetFallback(presetFromPreferences(prefs), { missingThemeID });
      if (notifyMissing && missingThemeID && this.missingNoticeID !== missingThemeID) {
        this.missingNoticeID = missingThemeID;
        this.showToast?.(this.translate("appearance.themeMissing", { id: missingThemeID }), "warn");
      }
      return false;
    }
    try {
      const applied = await this.loadThemeStylesheet(theme);
      if (!applied) return false;
      this.missingNoticeID = "";
      return true;
    } catch (error) {
      this.applyPresetFallback(presetFromPreferences(prefs), { missingThemeID: theme.id });
      if (notifyMissing) this.showToast?.(this.translate("appearance.themeLoadFailed", { name: theme.name, error: String(error?.message || error) }), "error");
      return false;
    }
  }

  async activateTheme(id) {
    if (this.state.status !== "ready") await this.loadCatalog({ force: this.state.status === "error" });
    const theme = this.findTheme(id);
    if (!theme) throw new Error(this.translate("appearance.themeNotFound", { id }));
    const applied = await this.loadThemeStylesheet(theme);
    if (!applied) return null;
    const current = this.preferenceAdapter?.currentAppearancePreferences?.() || {};
    this.preferenceAdapter?.saveAppearancePreferences?.({
      ...current,
      themeRef: {
        kind: "package",
        id: theme.id,
        revision: theme.revision,
        colorScheme: theme.colorScheme,
      },
      themePreset: theme.colorScheme,
      theme: theme.colorScheme,
    }, { notify: true });
    return theme;
  }

  async importTheme(file, { replace = false } = {}) {
    if (!file) throw new Error(this.translate("appearance.themeChooseFile"));
    const form = new FormData();
    form.set("file", file);
    if (replace) form.set("replace", "true");
    this.updateState({ importing: true, error: "" });
    try {
      const result = await this.api("/api/themes/import", { method: "POST", body: form });
      await this.loadCatalog({ force: true });
      return normalizeThemeMutationResult(result);
    } finally {
      this.updateState({ importing: false });
    }
  }

  // createTheme installs a theme from a bare manifest with no image
  // resources: the editor's save path. Same shape as importTheme's result.
  async createTheme(manifest, { replace = false } = {}) {
    this.updateState({ importing: true, error: "" });
    try {
      const result = await this.api("/api/themes", {
        method: "POST",
        body: JSON.stringify({ manifest, replace: replace === true }),
      });
      await this.loadCatalog({ force: true });
      return normalizeThemeMutationResult(result);
    } finally {
      this.updateState({ importing: false });
    }
  }

  async fetchThemeManifest(id) {
    const normalized = String(id || "").trim().toLowerCase();
    const payload = await this.api(`/api/themes/${encodeURIComponent(normalized)}/manifest`);
    const manifest = payload?.manifest;
    if (!manifest || typeof manifest !== "object") throw new Error(this.translate("appearance.themeNotFound", { id: normalized }));
    return manifest;
  }

  async deleteTheme(id) {
    const theme = this.findTheme(id);
    if (!theme) throw new Error(this.translate("appearance.themeNotFound", { id }));
    if (!theme.deletable) throw new Error(this.translate("appearance.themeBundledDeleteDenied"));
    this.updateState({ deletingThemeID: theme.id, error: "" });
    try {
      await this.api(`/api/themes/${encodeURIComponent(theme.id)}`, { method: "DELETE" });
      const current = this.preferenceAdapter?.currentAppearancePreferences?.() || {};
      if (current.themeRef?.kind === "package" && current.themeRef.id === theme.id) {
        this.preferenceAdapter?.saveAppearancePreferences?.({
          ...current,
          themeRef: { kind: "preset", id: "light" },
          themePreset: "light",
          theme: "light",
        }, { notify: true });
      }
      await this.loadCatalog({ force: true });
    } finally {
      this.updateState({ deletingThemeID: "" });
    }
  }

  systemPrefersDark() {
    try {
      return this.window?.matchMedia?.("(prefers-color-scheme: dark)")?.matches === true;
    } catch {
      return false;
    }
  }

  // A theme with a dark variant palette renders it whenever the system asks
  // for dark; a single-palette theme keeps its declared scheme regardless.
  resolveThemeDarkMode(theme) {
    if (theme?.capabilities?.darkVariant === true) return theme.colorScheme === "dark" || this.systemPrefersDark();
    return theme?.colorScheme === "dark";
  }

  // Follow live system scheme flips while a dark-variant theme is active. The
  // generated stylesheet already contains both palettes, so the switch is just
  // the body class; no request or re-render is needed.
  bindDarkVariantListener() {
    if (this.darkVariantListener) return;
    let query;
    try {
      query = this.window?.matchMedia?.("(prefers-color-scheme: dark)");
    } catch {
      query = null;
    }
    if (!query?.addEventListener) return;
    this.darkVariantQuery = query;
    this.darkVariantListener = () => {
      const active = this.findTheme(this.state.activeThemeID);
      if (!active || active.capabilities?.darkVariant !== true) return;
      this.applyResolvedScheme(active);
    };
    query.addEventListener("change", this.darkVariantListener);
  }

  applyResolvedScheme(theme) {
    const dark = this.resolveThemeDarkMode(theme);
    const body = this.document?.body;
    if (body?.dataset) body.dataset.themePreset = dark ? "dark" : "light";
    body?.classList?.toggle?.("theme-light", true);
    body?.classList?.toggle?.("theme-dark", dark);
  }

  applyPresetFallback(preset = "light", { missingThemeID = "" } = {}) {
    this.stylesheetSequence += 1;
    const normalizedPreset = normalizeAppearanceThemePreset(preset) || "light";
    const body = this.document?.body;
    if (body?.dataset) {
      body.dataset.themePreset = normalizedPreset;
      delete body.dataset.autotoTheme;
      delete body.dataset.themeRevision;
      delete body.dataset.themeSource;
      delete body.dataset.themeGlobalBackground;
      delete body.dataset.themeIcons;
    }
    body?.classList?.toggle?.("theme-light", true);
    body?.classList?.toggle?.("theme-dark", appearanceThemeForPreset(normalizedPreset) === "dark");
    const currentLink = this.document?.getElementById?.(themeStylesheetLinkID);
    currentLink?.remove?.();
    this.updateState({
      activeThemeID: "",
      activeRevision: "",
      missingThemeID,
    });
  }

  async loadThemeStylesheet(theme) {
    if (this.state.activeThemeID === theme.id
      && this.state.activeRevision === theme.revision
      && this.document?.getElementById?.(themeStylesheetLinkID)) {
      return true;
    }
    const sequence = ++this.stylesheetSequence;
    const link = this.document?.createElement?.("link");
    if (!link) throw new Error(this.translate("appearance.themeEnvironmentUnsupported"));
    link.rel = "stylesheet";
    link.href = theme.stylesheetUrl;
    link.dataset.autotoThemeCandidate = theme.id;
    const currentLink = this.document.getElementById?.(themeStylesheetLinkID);
    await new Promise((resolve, reject) => {
      let settled = false;
      const finish = (error) => {
        if (settled) return;
        settled = true;
        this.window?.clearTimeout?.(timeout);
        link.onload = null;
        link.onerror = null;
        if (error) reject(error);
        else resolve();
      };
      const timeout = this.window?.setTimeout?.(
        () => finish(new Error(this.translate("appearance.themeLoadTimeout"))),
        this.loadTimeoutMs,
      );
      link.onload = () => finish();
      link.onerror = () => finish(new Error(this.translate("appearance.themeRequestFailed")));
      this.document.head?.appendChild?.(link);
    }).catch((error) => {
      link.remove?.();
      throw error;
    });
    if (sequence !== this.stylesheetSequence) {
      link.remove?.();
      return false;
    }
    currentLink?.remove?.();
    link.id = themeStylesheetLinkID;
    delete link.dataset.autotoThemeCandidate;
    const body = this.document.body;
    if (body?.dataset) {
      body.dataset.autotoTheme = theme.id;
      body.dataset.themeRevision = theme.revision;
      body.dataset.themeSource = theme.source;
      body.dataset.themeGlobalBackground = theme.capabilities.globalBackground ? "true" : "false";
      body.dataset.themeIcons = theme.capabilities.icons ? "true" : "false";
    }
    this.applyResolvedScheme(theme);
    if (theme.capabilities.darkVariant === true) this.bindDarkVariantListener();
    this.updateState({
      status: "ready",
      activeThemeID: theme.id,
      activeRevision: theme.revision,
      missingThemeID: "",
      error: "",
    });
    return true;
  }
}

export function createThemeManager(options) {
  return new ThemeManager(options);
}

const backgroundURLPattern = /^\/appearance\/backgrounds\/[a-f0-9]{64}\/[A-Za-z0-9][A-Za-z0-9._-]{0,119}$/;
const appearanceBackgroundContentTypes = Object.freeze({
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".webp": "image/webp",
});
export const appearanceBackgroundMaxBytes = 8 * 1024 * 1024;
export const appearanceBackgroundUploadErrorCodes = Object.freeze({
  required: "required",
  unsupportedType: "unsupported-type",
  typeMismatch: "type-mismatch",
  tooLarge: "too-large",
  invalidImage: "invalid-image",
  unauthorized: "unauthorized",
  unavailable: "unavailable",
  failed: "failed",
});

function appearanceBackgroundExtension(filename) {
  const match = String(filename || "").trim().toLowerCase().match(/\.[^.]+$/);
  return match?.[0] || "";
}

function appearanceBackgroundUploadError(code, message, cause) {
  const error = new Error(message);
  error.code = code;
  if (cause) error.cause = cause;
  return error;
}

export function validateAppearanceBackgroundFile(file) {
  if (!file) throw appearanceBackgroundUploadError(appearanceBackgroundUploadErrorCodes.required, "appearance.backgroundFileRequired");
  const originalFilename = String(file.name || "").trim();
  const extension = appearanceBackgroundExtension(originalFilename);
  const expectedContentType = appearanceBackgroundContentTypes[extension];
  if (!expectedContentType) {
    throw appearanceBackgroundUploadError(appearanceBackgroundUploadErrorCodes.unsupportedType, "appearance.backgroundUnsupported");
  }
  const contentType = String(file.type || "").trim().toLowerCase().split(";", 1)[0];
  if (contentType && contentType !== expectedContentType) {
    throw appearanceBackgroundUploadError(appearanceBackgroundUploadErrorCodes.typeMismatch, "appearance.backgroundTypeMismatch");
  }
  const size = Number(file.size);
  if (Number.isFinite(size) && size > appearanceBackgroundMaxBytes) {
    throw appearanceBackgroundUploadError(appearanceBackgroundUploadErrorCodes.tooLarge, "appearance.backgroundTooLarge");
  }
  return {
    originalFilename,
    multipartFilename: `background-upload${extension}`,
    extension,
    contentType: expectedContentType,
  };
}

export function safeAppearanceBackgroundUploadFilename(file) {
  return validateAppearanceBackgroundFile(file).multipartFilename;
}

function fallbackAppearanceBackgroundTranslation(key) {
  const messages = {
    "appearance.backgroundFileRequired": "Choose a background image first.",
    "appearance.backgroundUnsupported": "Choose a PNG, JPG, JPEG, or WebP image.",
    "appearance.backgroundTypeMismatch": "The image type does not match its file extension.",
    "appearance.backgroundTooLarge": "The background image exceeds the 8 MiB limit.",
    "appearance.backgroundInvalid": "This image could not be read. Choose a valid PNG, JPG, JPEG, or WebP image within the supported dimensions.",
    "appearance.backgroundUnauthorized": "You do not have permission to upload a background image.",
    "appearance.backgroundUnavailable": "Background image storage is currently unavailable.",
    "appearance.backgroundUploadFailed": "The background image could not be uploaded. Try again.",
  };
  return messages[key] || key;
}

export function localizeAppearanceBackgroundUploadError(error, translate = fallbackAppearanceBackgroundTranslation) {
  if (Object.values(appearanceBackgroundUploadErrorCodes).includes(error?.code)) {
    const key = error.message?.startsWith?.("appearance.") ? error.message : "appearance.backgroundUploadFailed";
    return appearanceBackgroundUploadError(error.code, translate(key), error.cause);
  }
  const status = Number(error?.status) || 0;
  const detail = String(error?.message || "").toLowerCase();
  let code = appearanceBackgroundUploadErrorCodes.failed;
  let key = "appearance.backgroundUploadFailed";
  if (status === 401 || status === 403) {
    code = appearanceBackgroundUploadErrorCodes.unauthorized;
    key = "appearance.backgroundUnauthorized";
  } else if (status === 413 || detail.includes("too large") || detail.includes("exceeds 8388608 bytes")) {
    code = appearanceBackgroundUploadErrorCodes.tooLarge;
    key = "appearance.backgroundTooLarge";
  } else if (status === 503 || detail.includes("store is unavailable")) {
    code = appearanceBackgroundUploadErrorCodes.unavailable;
    key = "appearance.backgroundUnavailable";
  } else if (status === 400 && detail.includes("file is required")) {
    code = appearanceBackgroundUploadErrorCodes.required;
    key = "appearance.backgroundFileRequired";
  } else if (status === 400 && detail.includes("filename extension")) {
    code = appearanceBackgroundUploadErrorCodes.unsupportedType;
    key = "appearance.backgroundUnsupported";
  } else if (status === 400 || detail.includes("failed to load") || detail.includes("decode failed")) {
    code = appearanceBackgroundUploadErrorCodes.invalidImage;
    key = "appearance.backgroundInvalid";
  }
  return appearanceBackgroundUploadError(code, translate(key), error);
}

export function safeAppearanceBackgroundURL(value) {
  const url = String(value || "").trim();
  return backgroundURLPattern.test(url) ? url : "";
}

export function normalizeAppearanceBackgroundRecord(value = {}) {
  const source = value?.background && typeof value.background === "object" ? value.background : value;
  const requestedMode = String(source?.mode || source?.backgroundMode || "").toLowerCase();
  const position = (candidate, fallback) => {
    const number = Number(candidate);
    return Number.isFinite(number) ? Math.max(0, Math.min(100, Math.round(number))) : fallback;
  };
  return {
    mode: ["theme", "custom", "none"].includes(requestedMode) ? requestedMode : "theme",
    url: safeAppearanceBackgroundURL(source?.url || source?.backgroundUrl || source?.imageUrl),
    revision: String(source?.revision || "").trim().toLowerCase().slice(0, 64),
    filename: String(source?.filename || "").trim().slice(0, 120),
    contentType: String(source?.contentType || "").trim().slice(0, 80),
    size: Math.max(0, Math.round(Number(source?.size) || 0)),
    dim: Math.max(0, Math.min(75, Math.round(Number.isFinite(Number(source?.dim ?? source?.backgroundDim)) ? Number(source?.dim ?? source?.backgroundDim) : 18))),
    positionX: position(source?.positionX ?? source?.backgroundPositionX, 50),
    positionY: position(source?.positionY ?? source?.backgroundPositionY, 50),
    width: Math.max(0, Math.round(Number(source?.width || source?.naturalWidth) || 0)),
    height: Math.max(0, Math.round(Number(source?.height || source?.naturalHeight) || 0)),
  };
}

export class AppearanceBackgroundManager {
  constructor({ api, documentRef = globalThis.document, windowRef = globalThis.window, showToast, translate } = {}) {
    if (typeof api !== "function") throw new TypeError("AppearanceBackgroundManager requires an api function");
    this.api = api;
    this.document = documentRef;
    this.window = windowRef || globalThis;
    this.showToast = showToast;
    this.translate = typeof translate === "function" ? translate : fallbackAppearanceBackgroundTranslation;
    this.listeners = new Set();
    this.sequence = 0;
    this.state = { status: "idle", background: normalizeAppearanceBackgroundRecord({}), error: "" };
    this.preferenceAdapter = null;
  }

  setPreferenceAdapter(adapter = {}) { this.preferenceAdapter = adapter; }
  snapshot() { return { ...this.state, background: { ...this.state.background } }; }
  subscribe(listener) { if (typeof listener !== "function") return () => {}; this.listeners.add(listener); return () => this.listeners.delete(listener); }
  emit() { const snapshot = this.snapshot(); for (const listener of this.listeners) { try { listener(snapshot); } catch {} } }
  update(patch) { this.state = { ...this.state, ...patch }; this.emit(); }

  currentPreferences(overrides = {}) {
    return normalizeAppearanceBackgroundRecord({
      ...(this.preferenceAdapter?.currentAppearancePreferences?.() || {}),
      ...overrides,
    });
  }

  mergeAsset(asset, preferences = {}) {
    const prefs = this.currentPreferences(preferences);
    const metadata = normalizeAppearanceBackgroundRecord(asset);
    const url = metadata.url || prefs.url;
    return normalizeAppearanceBackgroundRecord({
      ...metadata,
      ...prefs,
      url,
      width: metadata.width || prefs.width,
      height: metadata.height || prefs.height,
      size: metadata.size || prefs.size,
      revision: metadata.revision || prefs.revision,
      filename: metadata.filename || prefs.filename,
      contentType: metadata.contentType || prefs.contentType,
      mode: prefs.mode === "custom" && !url ? "theme" : prefs.mode,
    });
  }

  async load(preferences = {}) {
    const sequence = ++this.sequence;
    this.update({ status: "loading", error: "" });
    try {
      const payload = await this.api("/api/appearance/background");
      const background = this.mergeAsset(payload?.background || {}, preferences);
      if (sequence !== this.sequence) return background;
      await this.apply(background, { sequence });
      return background;
    } catch (error) {
      if (sequence === this.sequence) this.update({ status: "error", error: String(error?.message || error || "Background request failed") });
      throw error;
    }
  }

  async upload(file, { mode = "custom", dim, positionX, positionY } = {}) {
    let upload;
    try {
      upload = validateAppearanceBackgroundFile(file);
    } catch (error) {
      throw localizeAppearanceBackgroundUploadError(error, this.translate);
    }
    const sequence = ++this.sequence;
    const form = new FormData();
    form.set("file", file, upload.multipartFilename);
    form.set("displayName", upload.originalFilename);
    this.update({ status: "uploading", error: "" });
    try {
      const payload = await this.api("/api/appearance/background", { method: "POST", body: form });
      const background = normalizeAppearanceBackgroundRecord({
        ...this.mergeAsset(payload?.background || {}, {
          mode: mode === "none" ? "none" : "custom",
          dim,
          positionX,
          positionY,
        }),
        filename: upload.originalFilename,
      });
      if (sequence !== this.sequence) return background;
      await this.apply(background, { sequence });
      return background;
    } catch (error) {
      const localized = localizeAppearanceBackgroundUploadError(error, this.translate);
      if (sequence === this.sequence) this.update({ status: "error", error: localized.message });
      throw localized;
    }
  }

  async remove() {
    const sequence = ++this.sequence;
    await this.api("/api/appearance/background", { method: "DELETE" });
    const background = normalizeAppearanceBackgroundRecord({
      mode: "theme",
      url: "",
      dim: this.state.background.dim,
      positionX: this.state.background.positionX,
      positionY: this.state.background.positionY,
    });
    if (sequence !== this.sequence) return background;
    await this.apply(background, { sequence });
    return background;
  }

  async saveOptions(next = {}) {
    const background = normalizeAppearanceBackgroundRecord({ ...this.state.background, ...next });
    await this.apply(background);
    return background;
  }

  createPreloadImage() {
    if (typeof this.window?.Image === "function") return new this.window.Image();
    return this.document?.createElement?.("img") || null;
  }

  async preload(url) {
    const image = this.createPreloadImage();
    if (!image) throw new Error("Image preloading is unavailable");
    image.decoding = "async";
    image.alt = "";
    await new Promise((resolve, reject) => {
      let settled = false;
      const finish = (error) => {
        if (settled) return;
        settled = true;
        image.onload = null;
        image.onerror = null;
        if (error) reject(error);
        else resolve();
      };
      image.onload = () => {
        const decoded = typeof image.decode === "function" ? image.decode() : Promise.resolve();
        Promise.resolve(decoded).then(() => finish()).catch((error) => finish(error || new Error("Background image decode failed")));
      };
      image.onerror = () => finish(new Error("Background image failed to load"));
      image.src = url;
      if (image.complete) {
        if (Number(image.naturalWidth) > 0) image.onload();
        else image.onerror();
      }
    });
    return image;
  }

  async apply(background = this.state.background, { sequence = ++this.sequence } = {}) {
    const normalized = normalizeAppearanceBackgroundRecord(background);
    let image = null;
    if (normalized.mode === "custom" && normalized.url) image = await this.preload(normalized.url);
    if (sequence !== this.sequence) return false;
    const applied = normalizeAppearanceBackgroundRecord({
      ...normalized,
      width: normalized.width || Number(image?.naturalWidth) || 0,
      height: normalized.height || Number(image?.naturalHeight) || 0,
    });
    const body = this.document?.body;
    if (body?.dataset) {
      body.dataset.backgroundMode = applied.mode;
      body.dataset.backgroundReady = applied.mode === "custom" && applied.url ? "true" : "false";
    }
    body?.style?.setProperty?.("--autoto-custom-background-image", applied.mode === "custom" && applied.url ? `url(${JSON.stringify(applied.url)})` : "none");
    body?.style?.setProperty?.("--autoto-background-dim", `${applied.dim}%`);
    body?.style?.setProperty?.("--autoto-background-position", `${applied.positionX}% ${applied.positionY}%`);
    this.update({ status: "ready", background: applied, error: "" });
    return true;
  }
}

export function createAppearanceBackgroundManager(options) {
  return new AppearanceBackgroundManager(options);
}
