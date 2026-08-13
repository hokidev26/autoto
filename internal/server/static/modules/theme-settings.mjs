import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { confirm as platformConfirm } from "./platform.mjs";
import { createThemeEditorController } from "./theme-editor.mjs";

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
          <span class="theme-package-capabilities"><span class="theme-capability ${hasPreview ? "supported" : "fallback"}" data-theme-capability="preview">${escapeHtml(hasPreview ? t("appearance.themeCapabilityPreview") : t("appearance.themeCapabilityPreviewFallback"))}</span><span class="theme-capability ${theme.capabilities?.background ? "supported" : "fallback"}" data-theme-capability="background">${escapeHtml(theme.capabilities?.background ? t("appearance.themeCapabilityBackground") : t("appearance.themeCapabilityBackgroundFallback"))}</span><span class="theme-capability ${theme.capabilities?.icons ? "supported" : "fallback"}" data-theme-capability="icons">${escapeHtml(theme.capabilities?.icons ? t("appearance.themeCapabilityIcons") : t("appearance.themeCapabilityIconsFallback"))}</span>${theme.capabilities?.darkVariant ? `<span class="theme-capability supported" data-theme-capability="dark-variant">${escapeHtml(t("appearance.themeCapabilityDarkVariant"))}</span>` : ""}</span>
        </span>
      </button>
      <button class="theme-package-export" type="button" data-theme-export="${escapeAttr(theme.id)}" title="${escapeAttr(t("appearance.themeExport"))}" aria-label="${escapeAttr(t("appearance.themeExport"))}" ${snapshot.importing || deleting ? "disabled" : ""}>&#8681;</button>
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
  const themeEditor = createThemeEditorController({
    themeManager,
    t,
    showToast,
    showError,
    refreshActiveSettingsPanel,
    confirmAction,
  });

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
            <button id="createThemeBtn" class="settings-action-btn" type="button" ${snapshot.importing ? "disabled" : ""}>${escapeHtml(t("appearance.themeCreate"))}</button>
            <button id="restoreDefaultThemeBtn" class="settings-action-btn" type="button">${escapeHtml(t("appearance.themeRestoreDefault"))}</button>
            <input id="themePackageInput" class="hidden" type="file" accept=".autoto-theme,.zip,application/zip" />
          </div>
          ${themeEditor.renderSection()}
          ${missing}
          ${body}
        </div>
      </section>
    `;
  }

  // The install toast reports the update semantics the server measured:
  // "updated v1.0.0 → v1.1.0" when a replace changed versions, and any
  // advisory contrast warnings so unreadable palettes surface before use.
  function announceThemeInstall(result, replaced) {
    if (result?.replaced && result.previousVersion && result.theme?.version && result.previousVersion !== result.theme.version) {
      showToast?.(t("appearance.themeUpdated", { from: result.previousVersion, to: result.theme.version }), "success");
    } else {
      showToast?.(t(replaced ? "appearance.themeReplaced" : "appearance.themeImported"), "success");
    }
    if (result?.warnings?.length) {
      showToast?.(t("appearance.themeContrastWarnings", { count: result.warnings.length, pair: result.warnings[0].pair }), "warn");
    }
  }

  // Conflict errors name the installed version, so the replace prompt can say
  // exactly what would be overwritten.
  function replacePrompt(error) {
    const installed = String(error?.message || "").match(/installed version ([A-Za-z0-9._+-]+)/);
    return installed
      ? t("appearance.themeReplaceConfirmVersion", { version: installed[1] })
      : t("appearance.themeReplaceConfirm");
  }

  async function importSelectedTheme(file) {
    if (!file) return;
    try {
      const result = await themeManager.importTheme(file);
      announceThemeInstall(result, false);
      if (result?.theme?.id) await themeManager.activateTheme(result.theme.id);
    } catch (error) {
      if (error?.status === 409 && await confirmAction(replacePrompt(error))) {
        const result = await themeManager.importTheme(file, { replace: true });
        announceThemeInstall(result, true);
        if (result?.theme?.id) await themeManager.activateTheme(result.theme.id);
      } else {
        throw error;
      }
    } finally {
      if ($("themePackageInput")) $("themePackageInput").value = "";
      refreshActiveSettingsPanel?.();
    }
  }

  // Exports are plain same-origin GET downloads; an anchor keeps the browser's
  // native download UX and cookie handling.
  function downloadThemeExport(id) {
    const anchor = document.createElement("a");
    anchor.href = `/api/themes/${encodeURIComponent(id)}/export`;
    anchor.download = "";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  }

  function bindThemeLibraryActions() {
    $("importThemeBtn")?.addEventListener("click", () => $("themePackageInput")?.click());
    $("createThemeBtn")?.addEventListener("click", () => {
      // Start from the active package theme when there is one: remixing an
      // installed look is the most common reason to open the editor.
      const prefs = currentAppearancePreferences?.() || {};
      const activeID = prefs.themeRef?.kind === "package" ? prefs.themeRef.id : "";
      themeEditor.openFromTheme(activeID).catch(() => themeEditor.open());
    });
    $("restoreDefaultThemeBtn")?.addEventListener("click", () => setAppearancePreference?.("themePreset", "light"));
    $("themePackageInput")?.addEventListener("change", (event) => {
      importSelectedTheme(event.currentTarget.files?.[0]).catch(showError);
    });
    document.querySelectorAll("[data-theme-export]").forEach((node) => {
      node.addEventListener("click", () => downloadThemeExport(node.dataset.themeExport));
    });
    themeEditor.bindSection();
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
