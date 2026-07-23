import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs?v=global-background-1-theme-v2-1-formal-theme-assets-1";
import { confirm as platformConfirm } from "./platform.mjs";

function themeSourceLabel(theme) {
  return theme.source === "local" ? t("appearance.themeSourceLocal") : t("appearance.themeSourceBundled");
}

function themeCard(theme, active, snapshot) {
  const hasPreview = Boolean(theme.previewUrl);
  const preview = hasPreview
    ? `<img src="${escapeAttr(theme.previewUrl)}" alt="" loading="lazy" decoding="async" />`
    : `<span class="theme-package-preview-fallback ${theme.colorScheme === "dark" ? "dark" : "light"} theme-package-preview-${escapeAttr(theme.id)}" aria-hidden="true"><i></i><b></b><em></em></span>`;
  const deleting = snapshot.deletingThemeID === theme.id;
  return `
    <article class="theme-package-card ${active ? "active" : ""}" data-theme-card="${escapeAttr(theme.id)}">
      <button class="theme-package-select" type="button" data-theme-package="${escapeAttr(theme.id)}" aria-pressed="${active ? "true" : "false"}" ${snapshot.importing || deleting ? "disabled" : ""}>
        <span class="theme-package-preview">${preview}<span class="theme-package-scheme">${escapeHtml(theme.colorScheme === "dark" ? t("appearance.themeDarkLabel") : t("appearance.themeLightLabel"))}</span></span>
        <span class="theme-package-copy">
          <strong>${escapeHtml(theme.name)}</strong>
          <small>${escapeHtml(theme.description || t("appearance.themePackageDescriptionFallback"))}</small>
          <span class="theme-package-meta"><span>${escapeHtml(themeSourceLabel(theme))}</span>${theme.version ? `<span>v${escapeHtml(theme.version)}</span>` : ""}${theme.author ? `<span>${escapeHtml(theme.author)}</span>` : ""}</span>
          <span class="theme-package-capabilities"><span class="theme-capability ${hasPreview ? "supported" : "fallback"}" data-theme-capability="preview">${escapeHtml(hasPreview ? t("appearance.themeCapabilityPreview") : t("appearance.themeCapabilityPreviewFallback"))}</span><span class="theme-capability ${theme.capabilities?.background ? "supported" : "fallback"}" data-theme-capability="background">${escapeHtml(theme.capabilities?.background ? t("appearance.themeCapabilityBackground") : t("appearance.themeCapabilityBackgroundFallback"))}</span><span class="theme-capability ${theme.capabilities?.icons ? "supported" : "fallback"}" data-theme-capability="icons">${escapeHtml(theme.capabilities?.icons ? t("appearance.themeCapabilityIcons") : t("appearance.themeCapabilityIconsFallback"))}</span></span>
        </span>
      </button>
      ${theme.deletable ? `<button class="theme-package-delete" type="button" data-theme-delete="${escapeAttr(theme.id)}" title="${escapeAttr(t("appearance.themeDelete"))}" aria-label="${escapeAttr(t("appearance.themeDelete"))}" ${snapshot.importing || deleting ? "disabled" : ""}>${deleting ? "…" : "×"}</button>` : ""}
    </article>
  `;
}

export function createThemeSettingsController({
  themeManager,
  currentAppearancePreferences,
  setAppearancePreference,
  refreshActiveSettingsPanel,
  showError,
  showToast,
  confirmAction = platformConfirm,
} = {}) {
  function renderThemeLibrarySection() {
    const prefs = currentAppearancePreferences?.() || {};
    const snapshot = themeManager?.snapshot?.() || { status: "idle", themes: [] };
    const activeID = prefs.themeRef?.kind === "package" ? prefs.themeRef.id : "";
    let body = "";
    if (snapshot.status === "loading" || snapshot.status === "idle") {
      body = `<div class="theme-library-state" role="status">${escapeHtml(t("appearance.themeLibraryLoading"))}</div>`;
    } else if (snapshot.status === "error") {
      body = `<div class="theme-library-state error" role="alert">${escapeHtml(snapshot.error || t("appearance.themeLibraryError"))}<button type="button" data-theme-reload>${escapeHtml(t("appearance.themeRetry"))}</button></div>`;
    } else if (!snapshot.themes.length) {
      body = `<div class="theme-library-state">${escapeHtml(t("appearance.themeLibraryEmpty"))}</div>`;
    } else {
      body = `<div class="theme-package-grid">${snapshot.themes.map((theme) => themeCard(theme, activeID === theme.id, snapshot)).join("")}</div>`;
    }
    const missing = snapshot.missingThemeID
      ? `<div class="theme-library-missing" role="alert">${escapeHtml(t("appearance.themeMissing", { id: snapshot.missingThemeID }))}</div>`
      : "";
    return `
      <section class="compact-settings-section theme-library-section">
        <div class="compact-settings-section-copy">
          <h2>${escapeHtml(t("appearance.themeLibraryTitle"))}</h2>
          <p data-settings-help-copy>${escapeHtml(t("appearance.themeLibraryMeta"))}</p>
        </div>
        <div class="compact-settings-section-controls theme-library-controls">
          <div class="theme-library-toolbar">
            <button id="importThemeBtn" class="settings-action-btn primary" type="button" ${snapshot.importing ? "disabled" : ""}>${escapeHtml(snapshot.importing ? t("appearance.themeImporting") : t("appearance.themeImport"))}</button>
            <button id="restoreDefaultThemeBtn" class="settings-action-btn" type="button">${escapeHtml(t("appearance.themeRestoreDefault"))}</button>
            <input id="themePackageInput" class="hidden" type="file" accept=".autoto-theme,.zip,application/zip" />
          </div>
          ${missing}
          ${body}
        </div>
      </section>
    `;
  }

  async function importSelectedTheme(file) {
    if (!file) return;
    try {
      const installed = await themeManager.importTheme(file);
      showToast?.(t("appearance.themeImported"), "success");
      const id = installed?.id || installed?.themeId;
      if (id) await themeManager.activateTheme(id);
    } catch (error) {
      if (error?.status === 409 && await confirmAction(t("appearance.themeReplaceConfirm"))) {
        const installed = await themeManager.importTheme(file, { replace: true });
        showToast?.(t("appearance.themeReplaced"), "success");
        const id = installed?.id || installed?.themeId;
        if (id) await themeManager.activateTheme(id);
      } else {
        throw error;
      }
    } finally {
      if ($("themePackageInput")) $("themePackageInput").value = "";
      refreshActiveSettingsPanel?.();
    }
  }

  function bindThemeLibraryActions() {
    $("importThemeBtn")?.addEventListener("click", () => $("themePackageInput")?.click());
    $("restoreDefaultThemeBtn")?.addEventListener("click", () => setAppearancePreference?.("themePreset", "light"));
    $("themePackageInput")?.addEventListener("change", (event) => {
      importSelectedTheme(event.currentTarget.files?.[0]).catch(showError);
    });
    document.querySelectorAll("[data-theme-package]").forEach((node) => {
      node.addEventListener("click", () => {
        themeManager.activateTheme(node.dataset.themePackage)
          .then(() => refreshActiveSettingsPanel?.())
          .catch(showError);
      });
    });
    document.querySelectorAll("[data-theme-delete]").forEach((node) => {
      node.addEventListener("click", () => {
        const theme = themeManager.findTheme(node.dataset.themeDelete);
        if (!theme) return;
        Promise.resolve(confirmAction(t("appearance.themeDeleteConfirm", { name: theme.name })))
          .then((ok) => {
            if (!ok) return null;
            return themeManager.deleteTheme(theme.id);
          })
          .then((deleted) => {
            if (deleted === null || deleted === undefined) return;
            showToast?.(t("appearance.themeDeleted"), "success");
            refreshActiveSettingsPanel?.();
          })
          .catch(showError);
      });
    });
    document.querySelectorAll("[data-theme-reload]").forEach((node) => {
      node.addEventListener("click", () => themeManager.loadCatalog({ force: true }).then(() => refreshActiveSettingsPanel?.()).catch(showError));
    });
  }

  return {
    bindThemeLibraryActions,
    renderThemeLibrarySection,
  };
}
