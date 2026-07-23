import test from "node:test";
import assert from "node:assert/strict";

import {
  appearanceBackgroundMaxBytes,
  appearanceBackgroundUploadErrorCodes,
  createAppearanceBackgroundManager,
  createThemeManager,
  normalizeAppearanceBackgroundRecord,
  normalizeThemeCatalog,
  safeAppearanceBackgroundUploadFilename,
  safeAppearanceBackgroundURL,
  setThemePageContext,
} from "./theme-manager.mjs";

class FakeClassList {
  constructor() {
    this.values = new Set();
  }
  toggle(name, enabled) {
    if (enabled) this.values.add(name);
    else this.values.delete(name);
  }
  contains(name) {
    return this.values.has(name);
  }
}

function fakeDocument({ fail = new Set() } = {}) {
  const children = [];
  const body = { dataset: {}, classList: new FakeClassList() };
  const documentRef = {
    body,
    head: {
      appendChild(node) {
        children.push(node);
        queueMicrotask(() => {
          if (fail.has(node.href)) node.onerror?.(new Error("failed"));
          else node.onload?.();
        });
      },
    },
    createElement(tag) {
      assert.equal(tag, "link");
      return {
        dataset: {},
        id: "",
        rel: "",
        href: "",
        remove() {
          const index = children.indexOf(this);
          if (index >= 0) children.splice(index, 1);
        },
      };
    },
    getElementById(id) {
      return children.find((node) => node.id === id) || null;
    },
  };
  return { documentRef, body, children };
}

const catalog = {
  themes: [{
    id: "argentina-spain-final",
    name: "Argentina × Spain Final",
    version: "1.0.0",
    description: "Original Autoto theme",
    author: "Autoto",
    colorScheme: "dark",
    source: "bundled",
    revision: "rev-1",
    stylesheetUrl: "/themes/argentina-spain-final/rev-1/theme.css",
    previewUrl: "/themes/argentina-spain-final/rev-1/preview.png",
    capabilities: { globalBackground: true, homeBackground: true, icons: true },
    deletable: false,
  }],
};

test("theme catalog accepts only scoped same-origin theme resources", () => {
  const themes = normalizeThemeCatalog({ themes: [
    ...catalog.themes,
    { ...catalog.themes[0], id: "other", stylesheetUrl: "https://example.test/theme.css" },
    { ...catalog.themes[0], id: "Bad ID" },
    { ...catalog.themes[0], id: "bad_id", stylesheetUrl: "/themes/bad_id/rev/theme.css" },
    { ...catalog.themes[0], id: "bad--id", stylesheetUrl: "/themes/bad--id/rev/theme.css" },
  ] });
  assert.equal(themes.length, 1);
  assert.equal(themes[0].id, "argentina-spain-final");
  assert.equal(themes[0].source, "bundled");
});

test("theme manager loads a package stylesheet before persisting it", async () => {
  const { documentRef, body } = fakeDocument();
  const manager = createThemeManager({
    api: async () => catalog,
    documentRef,
    windowRef: globalThis,
  });
  let prefs = {
    themeRef: { kind: "preset", id: "light" },
    themePreset: "light",
    theme: "light",
  };
  manager.setPreferenceAdapter({
    currentAppearancePreferences: () => prefs,
    saveAppearancePreferences(next) {
      prefs = next;
    },
  });

  await manager.loadCatalog();
  await manager.activateTheme("argentina-spain-final");

  assert.equal(body.dataset.autotoTheme, "argentina-spain-final");
  assert.equal(body.dataset.themeRevision, "rev-1");
  assert.equal(body.dataset.themeGlobalBackground, "true");
  assert.equal(body.dataset.themeIcons, "true");
  assert.equal(body.classList.contains("theme-dark"), true);
  assert.deepEqual(prefs.themeRef, {
    kind: "package",
    id: "argentina-spain-final",
    revision: "rev-1",
    colorScheme: "dark",
  });
});

test("missing package preference falls back without overwriting the desired reference", async () => {
  const { documentRef, body } = fakeDocument();
  const manager = createThemeManager({ api: async () => catalog, documentRef, windowRef: globalThis });
  const prefs = {
    themeRef: { kind: "package", id: "missing-theme", revision: "old", colorScheme: "dark" },
    themePreset: "light",
    theme: "dark",
  };

  await manager.loadCatalog();
  const applied = await manager.applyPreference(prefs, { notifyMissing: false });

  assert.equal(applied, false);
  assert.equal(body.dataset.autotoTheme, undefined);
  assert.equal(body.dataset.themePreset, "light");
  assert.equal(manager.snapshot().missingThemeID, "missing-theme");
  assert.equal(prefs.themeRef.id, "missing-theme");
});

test("failed stylesheet keeps the package inactive and applies the base palette", async () => {
  const failingURL = catalog.themes[0].stylesheetUrl;
  const { documentRef, body } = fakeDocument({ fail: new Set([failingURL]) });
  const manager = createThemeManager({ api: async () => catalog, documentRef, windowRef: globalThis });
  await manager.loadCatalog();

  const applied = await manager.applyPreference({
    themeRef: { kind: "package", id: "argentina-spain-final", revision: "rev-1", colorScheme: "dark" },
    themePreset: "light",
  }, { notifyMissing: false });

  assert.equal(applied, false);
  assert.equal(body.dataset.autotoTheme, undefined);
  assert.equal(body.classList.contains("theme-dark"), false);
});

test("switching back to a preset cancels an in-flight package activation", async () => {
  const { documentRef, body, children } = fakeDocument();
  const pending = [];
  documentRef.head.appendChild = (node) => {
    children.push(node);
    pending.push(node);
  };
  const manager = createThemeManager({ api: async () => catalog, documentRef, windowRef: globalThis });
  let prefs = {
    themeRef: { kind: "preset", id: "light" },
    themePreset: "light",
    theme: "light",
  };
  manager.setPreferenceAdapter({
    currentAppearancePreferences: () => prefs,
    saveAppearancePreferences(next) {
      prefs = next;
    },
  });
  await manager.loadCatalog();

  const activation = manager.activateTheme("argentina-spain-final");
  assert.equal(pending.length, 1);
  manager.applyPresetFallback("light");
  pending[0].onload?.();

  assert.equal(await activation, null);
  assert.equal(body.dataset.autotoTheme, undefined);
  assert.equal(body.dataset.themeGlobalBackground, undefined);
  assert.equal(body.dataset.themeIcons, undefined);
  assert.deepEqual(prefs.themeRef, { kind: "preset", id: "light" });
});

test("theme page context is explicit and removable", () => {
  const { documentRef, body } = fakeDocument();
  setThemePageContext("home-empty", documentRef);
  assert.equal(body.dataset.themePage, "home-empty");
  setThemePageContext("", documentRef);
  assert.equal(body.dataset.themePage, undefined);
});

function fakeBackgroundEnvironment({ naturalWidth = 3840, naturalHeight = 2160 } = {}) {
  const styleValues = new Map();
  const body = {
    dataset: {},
    style: { setProperty(name, value) { styleValues.set(name, value); } },
  };
  class FakeImage {
    constructor() {
      this.complete = false;
      this.naturalWidth = 0;
      this.naturalHeight = 0;
    }
    set src(value) {
      this.value = value;
      this.complete = true;
      this.naturalWidth = naturalWidth;
      this.naturalHeight = naturalHeight;
      queueMicrotask(() => this.onload?.());
    }
    decode() { return Promise.resolve(); }
  }
  return {
    body,
    styleValues,
    documentRef: { body },
    windowRef: { Image: FakeImage },
  };
}

const backgroundRevision = "a".repeat(64);
const backgroundURL = `/appearance/backgrounds/${backgroundRevision}/wallpaper.png`;

test("appearance background records accept only revision-scoped image URLs", () => {
  assert.equal(safeAppearanceBackgroundURL(backgroundURL), backgroundURL);
  assert.equal(safeAppearanceBackgroundURL("https://example.test/wallpaper.png"), "");
  assert.equal(safeAppearanceBackgroundURL(`/appearance/backgrounds/${backgroundRevision}/../wallpaper.png`), "");
  assert.deepEqual(normalizeAppearanceBackgroundRecord({
    mode: "custom",
    url: backgroundURL,
    dim: 27,
    positionX: 18,
    positionY: 82,
    width: 5120,
    height: 2880,
  }), {
    mode: "custom",
    url: backgroundURL,
    revision: "",
    filename: "",
    contentType: "",
    size: 0,
    dim: 27,
    positionX: 18,
    positionY: 82,
    width: 5120,
    height: 2880,
  });
});

test("appearance background upload uses an ASCII multipart filename without mutating the selected file", async () => {
  const { documentRef, windowRef } = fakeBackgroundEnvironment();
  const selectedFile = Object.freeze({
    name: "夏日 壁纸 (最终) 🌄.PNG",
    type: "image/png",
    size: 4096,
  });
  const formEntries = [];
  const OriginalFormData = globalThis.FormData;
  globalThis.FormData = class RecordingFormData {
    set(...entry) { formEntries.push(entry); }
  };
  try {
    const manager = createAppearanceBackgroundManager({
      api: async (path, options) => {
        assert.equal(path, "/api/appearance/background");
        assert.equal(options.method, "POST");
        return { background: {
          url: backgroundURL,
          revision: backgroundRevision,
          filename: "background-upload.png",
          contentType: "image/png",
          size: selectedFile.size,
          width: 3840,
          height: 2160,
        } };
      },
      documentRef,
      windowRef,
    });

    const uploaded = await manager.upload(selectedFile);

    assert.equal(safeAppearanceBackgroundUploadFilename(selectedFile), "background-upload.png");
    assert.deepEqual(formEntries, [
      ["file", selectedFile, "background-upload.png"],
      ["displayName", selectedFile.name],
    ]);
    assert.equal(selectedFile.name, "夏日 壁纸 (最终) 🌄.PNG");
    assert.equal(uploaded.filename, selectedFile.name);
    assert.equal(manager.snapshot().background.filename, selectedFile.name);
  } finally {
    globalThis.FormData = OriginalFormData;
  }
});

test("appearance background upload keeps extension, MIME, and size restrictions", async () => {
  let requests = 0;
  const manager = createAppearanceBackgroundManager({
    api: async () => { requests += 1; },
    translate: (key) => `localized:${key}`,
  });

  await assert.rejects(
    manager.upload({ name: "背景.gif", type: "image/gif", size: 10 }),
    (error) => error.code === appearanceBackgroundUploadErrorCodes.unsupportedType
      && error.message === "localized:appearance.backgroundUnsupported",
  );
  await assert.rejects(
    manager.upload({ name: "背景.png", type: "image/jpeg", size: 10 }),
    (error) => error.code === appearanceBackgroundUploadErrorCodes.typeMismatch
      && error.message === "localized:appearance.backgroundTypeMismatch",
  );
  await assert.rejects(
    manager.upload({ name: "背景.webp", type: "image/webp", size: appearanceBackgroundMaxBytes + 1 }),
    (error) => error.code === appearanceBackgroundUploadErrorCodes.tooLarge
      && error.message === "localized:appearance.backgroundTooLarge",
  );
  assert.equal(requests, 0);
});

test("appearance background upload replaces backend details with a localized error", async () => {
  const OriginalFormData = globalThis.FormData;
  globalThis.FormData = class RecordingFormData { set() {} };
  try {
    const manager = createAppearanceBackgroundManager({
      api: async () => {
        const error = new Error("invalid appearance background: theme resource background-upload.png content does not match its allowed image extension");
        error.status = 400;
        throw error;
      },
      translate: (key) => ({
        "appearance.backgroundInvalid": "无法读取这张图片。",
        "appearance.backgroundUploadFailed": "背景图片上传失败，请重试。",
      }[key] || key),
    });

    await assert.rejects(
      manager.upload({ name: "背景.png", type: "image/png", size: 10 }),
      (error) => error.code === appearanceBackgroundUploadErrorCodes.invalidImage
        && error.message === "无法读取这张图片。"
        && !error.message.includes("invalid appearance background"),
    );
    assert.equal(manager.snapshot().error, "无法读取这张图片。");
  } finally {
    globalThis.FormData = OriginalFormData;
  }
});

test("appearance background manager loads metadata, preloads original pixels, and applies positioning", async () => {
  const { documentRef, windowRef, body, styleValues } = fakeBackgroundEnvironment();
  const manager = createAppearanceBackgroundManager({
    api: async (path) => {
      assert.equal(path, "/api/appearance/background");
      return { background: {
        url: backgroundURL,
        revision: backgroundRevision,
        filename: "wallpaper.png",
        contentType: "image/png",
        size: 123456,
        width: 5120,
        height: 2880,
      } };
    },
    documentRef,
    windowRef,
  });
  manager.setPreferenceAdapter({
    currentAppearancePreferences: () => ({
      backgroundMode: "custom",
      backgroundUrl: backgroundURL,
      backgroundDim: 24,
      backgroundPositionX: 35,
      backgroundPositionY: 65,
    }),
  });

  const background = await manager.load();
  assert.equal(background.mode, "custom");
  assert.equal(background.url, backgroundURL);
  assert.equal(background.width, 5120);
  assert.equal(background.height, 2880);
  assert.equal(body.dataset.backgroundMode, "custom");
  assert.equal(body.dataset.backgroundReady, "true");
  assert.equal(styleValues.get("--autoto-custom-background-image"), `url(${JSON.stringify(backgroundURL)})`);
  assert.equal(styleValues.get("--autoto-background-dim"), "24%");
  assert.equal(styleValues.get("--autoto-background-position"), "35% 65%");
});

test("removing an appearance background clears stale persisted URLs from manager state", async () => {
  const { documentRef, windowRef, body, styleValues } = fakeBackgroundEnvironment();
  const requests = [];
  const manager = createAppearanceBackgroundManager({
    api: async (path, options = {}) => {
      requests.push([path, options.method || "GET"]);
      return null;
    },
    documentRef,
    windowRef,
  });
  manager.setPreferenceAdapter({
    currentAppearancePreferences: () => ({
      backgroundMode: "custom",
      backgroundUrl: backgroundURL,
      backgroundDim: 30,
      backgroundPositionX: 42,
      backgroundPositionY: 58,
    }),
  });
  await manager.apply({ mode: "custom", url: backgroundURL, dim: 30, positionX: 42, positionY: 58 });

  const removed = await manager.remove();
  assert.deepEqual(requests.at(-1), ["/api/appearance/background", "DELETE"]);
  assert.equal(removed.mode, "theme");
  assert.equal(removed.url, "");
  assert.equal(manager.snapshot().background.url, "");
  assert.equal(body.dataset.backgroundMode, "theme");
  assert.equal(body.dataset.backgroundReady, "false");
  assert.equal(styleValues.get("--autoto-custom-background-image"), "none");
});
