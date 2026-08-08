import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { recentDirectoriesKey } from "./preferences-data.mjs";
import { api } from "./runtime.mjs";
import { t } from "./i18n.mjs";
import { directoryBrowserScope, nativeDirectoryPickerAllowedFor } from "./remote-access-capabilities.mjs";

export function normalizeRecentDirectories(value = []) {
  return (Array.isArray(value) ? value : [])
    .map((item) => String(item || "").trim())
    .filter(Boolean)
    .slice(0, 8);
}

export function canonicalLocalPath(path) {
  const value = String(path || "").trim();
  if (!value) return "";
  if (value.startsWith("/")) return value;
  if (/^Users\//.test(value)) return `/${value}`;
  return value;
}

export function shortPath(path) {
  const value = canonicalLocalPath(path);
  if (!value) return t("workspace.directory.folderNotSet");
  const home = "/Users/aaa";
  if (value === home) return "~";
  if (value.startsWith(home + "/")) return `~/${value.slice(home.length + 1)}`;
  return value;
}

export function projectPathLabel(path) {
  const value = canonicalLocalPath(path);
  return value || t("workspace.directory.pathNotSet");
}

export function normalizePath(path) {
  return canonicalLocalPath(path).replace(/\/+$/, "") || "/";
}

export function basename(path) {
  return String(path || "").replace(/\/$/, "").split("/").filter(Boolean).pop() || "";
}

export function nativeDirectoryPickerAllowed(locationLike = globalThis.location, { state = {}, platformLike } = {}) {
  return nativeDirectoryPickerAllowedFor(state, locationLike, platformLike);
}

export function directoryScopeForBrowser(state = {}) {
  return directoryBrowserScope(state);
}

const directoryFolderIcon = `
  <svg class="directory-folder-svg" viewBox="0 0 24 20" aria-hidden="true" focusable="false">
    <path class="directory-folder-fill" d="M2.75 5.25A2.25 2.25 0 0 1 5 3h4.2l1.8 2h8A2.25 2.25 0 0 1 21.25 7.25v7.5A2.25 2.25 0 0 1 19 17H5a2.25 2.25 0 0 1-2.25-2.25Z"></path>
    <path class="directory-folder-stroke" d="M2.75 7h18.5M2.75 5.25A2.25 2.25 0 0 1 5 3h4.2l1.8 2h8A2.25 2.25 0 0 1 21.25 7.25v7.5A2.25 2.25 0 0 1 19 17H5a2.25 2.25 0 0 1-2.25-2.25Z"></path>
  </svg>`;

const directoryShortcutIcons = Object.freeze({
  Home: `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m3.5 10.5 8.5-7 8.5 7"></path><path d="M5.75 9.5v10h12.5v-10M9.25 19.5v-6h5.5v6"></path></svg>`,
  Desktop: `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><rect x="3.5" y="4.5" width="17" height="12" rx="1.8"></rect><path d="M8 20h8M12 16.5V20"></path></svg>`,
  Downloads: `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M12 3.5v11"></path><path d="m7.5 10.5 4.5 4.5 4.5-4.5"></path><path d="M4 18.5v1.25A1.75 1.75 0 0 0 5.75 21.5h12.5A1.75 1.75 0 0 0 20 19.75V18.5"></path></svg>`,
  Documents: `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M6 3.5h8l4 4v13H6z"></path><path d="M14 3.5v4h4M9 12h6M9 16h6"></path></svg>`,
  Projects: `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M3.5 7.25A2.25 2.25 0 0 1 5.75 5h4l1.8 2h6.7a2.25 2.25 0 0 1 2.25 2.25v7.5A2.25 2.25 0 0 1 18.25 19H5.75a2.25 2.25 0 0 1-2.25-2.25Z"></path><path d="M3.75 9h16.5"></path></svg>`,
});

const directoryFavoriteIcon = `<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="m12 3.75 2.55 5.16 5.7.83-4.12 4.02.97 5.68L12 16.76l-5.1 2.68.97-5.68-4.12-4.02 5.7-.83Z"></path></svg>`;

export function createDirectoryBrowserController({
  state,
  createProjectFromDirectory,
  elementVisible,
  notifyTerminal,
  showError,
  showToast,
} = {}) {
  function directoryElementVisible(id) {
    return typeof elementVisible === "function" ? elementVisible(id) : Boolean($(id) && !$(id).classList.contains("hidden"));
  }

  function reportError(err) {
    if (typeof showError === "function") showError(err);
  }

  async function openDirectoryChooser(path = "", { trigger = null, preferNative = true } = {}) {
    const defaultPath = String(path || "").trim();
    if (preferNative && nativeDirectoryPickerAllowed(globalThis.location, { state })) {
      try {
        const picked = await selectNativeDirectory(defaultPath, { trigger });
        if (picked?.canceled) return;
        if (picked?.path) {
          await createProjectFromDirectory(picked.path, { button: trigger });
          return;
        }
      } catch (err) {
        notifyTerminal?.(`[warn] ${t("workspace.directory.nativeUnavailable")} ${err.message || err}\n`);
        showToast?.(t("workspace.directory.nativeUnavailable"), "warn", { force: true });
      }
    }
    await openDirectoryModal(defaultPath);
  }

  async function selectNativeDirectory(path = "", { trigger = null } = {}) {
    if (state.nativeDirectorySelecting) return { canceled: true };
    state.nativeDirectorySelecting = true;
    const label = trigger?.textContent || "";
    setButtonBusy(trigger, true, "…");
    showToast?.(t("workspace.directory.openingNative"), "info", { force: true });
    try {
      const params = new URLSearchParams({ scope: directoryScopeForBrowser(state) });
      if (path) params.set("path", path);
      const data = await api(`/api/fs/native-directory?${params.toString()}`, { method: "POST", body: JSON.stringify({}) });
      if (data.canceled) {
        showToast?.(t("workspace.directory.selectionCanceled"), "info", { force: true });
        return { canceled: true };
      }
      if (data.path) {
        showToast?.(t("workspace.directory.selected", { path: shortPath(data.path) }), "success", { force: true });
        return data;
      }
      return { canceled: true };
    } finally {
      state.nativeDirectorySelecting = false;
      setButtonBusy(trigger, false, label || "…");
    }
  }

  async function openDirectoryModal(path = "") {
    $("folderModal").classList.remove("hidden");
    hideNewFolderInline();
    renderRecentModalDirectories();
    setDirectoryStatus(t("workspace.directory.loading"), "busy");
    await browseDirectories(path);
  }

  function closeDirectoryModal() {
    state.directoryBrowseSeq++;
    setDirectoryBrowserBusy(false);
    hideNewFolderInline();
    setDirectoryStatus(t("workspace.directory.chooseHint"), "");
    $("folderModal").classList.add("hidden");
  }

  function setDirectoryBrowserBusy(busy) {
    ["folderModal", "directoryList", "manualDirectoryPath", "goDirectoryBtn"].forEach((id) => {
      const el = $(id);
      if (!el) return;
      if (busy) el.setAttribute("aria-busy", "true");
      else el.removeAttribute("aria-busy");
    });
  }

  function setDirectoryStatus(message, variant = "") {
    const el = $("directoryStatus");
    if (!el) return;
    el.textContent = message || t("workspace.directory.chooseHint");
    el.classList.toggle("busy", variant === "busy");
    el.classList.toggle("error", variant === "error");
    el.classList.toggle("success", variant === "success");
  }

  function updateDirectoryPathDisplay(path) {
    const value = String(path || "").trim();
    const name = basename(value) || value || t("workspace.directory.currentPath");
    if ($("directoryPath")) $("directoryPath").textContent = value || t("common.loading");
    if ($("manualDirectoryPath")) $("manualDirectoryPath").value = value;
    if ($("folderLocationName")) $("folderLocationName").textContent = name;
    if ($("folderLocationPill")) $("folderLocationPill").title = value || t("workspace.directory.currentPath");
  }

  async function browseDirectories(path = "") {
    hideNewFolderInline();
    const seq = ++state.directoryBrowseSeq;
    setDirectoryBrowserBusy(true);
    try {
      const params = new URLSearchParams({ scope: directoryScopeForBrowser(state) });
      if (path) params.set("path", path);
      const data = await api(`/api/fs/directories?${params.toString()}`);
      if (seq !== state.directoryBrowseSeq || !directoryElementVisible("folderModal")) return;
      state.directoryPath = data.path;
      state.directoryParent = data.parent || "";
      state.directoryShortcuts = data.shortcuts || [];
      updateDirectoryPathDisplay(data.path);
    setDirectoryStatus(t("workspace.directory.chooseHint"), "");
      renderDirectoryShortcuts(state.directoryShortcuts);
      renderDirectoryList(data);
    } catch (err) {
      if (seq === state.directoryBrowseSeq && directoryElementVisible("folderModal")) {
        setDirectoryStatus(t("workspace.explorer.loadFailed", { message: err.message || err }), "error");
        throw err;
      }
    } finally {
      if (seq === state.directoryBrowseSeq) setDirectoryBrowserBusy(false);
    }
  }

  function renderDirectoryShortcuts(shortcuts) {
    const regular = (Array.isArray(shortcuts) ? shortcuts : []).filter((shortcut) => shortcut.name !== "Root");
    const renderShortcut = (shortcut) => {
      const active = normalizePath(shortcut.path) === normalizePath(state.directoryPath);
      const label = shortcutLabel(shortcut.name);
      const icon = directoryShortcutIcons[shortcut.name] || directoryShortcutIcons.Projects;
      return `
      <button class="folder-shortcut ${active ? "active" : ""}" type="button" data-path="${escapeAttr(shortcut.path)}">
        <span class="folder-shortcut-icon">${icon}</span>
        <span>${escapeHtml(label)}</span>
      </button>
    `;
    };
    $("directoryShortcuts").innerHTML = regular.map(renderShortcut).join("");
    $("directoryShortcuts").querySelectorAll("[data-path]").forEach((node) => {
      node.addEventListener("click", () => browseDirectories(node.dataset.path).catch(reportError));
    });
  }

  function shortcutLabel(name) {
    return {
      Home: t("workspace.directory.home"),
      Desktop: t("workspace.directory.desktop"),
      Downloads: t("workspace.directory.downloads"),
      Documents: t("workspace.directory.documents"),
      Projects: t("workspace.directory.projects"),
    }[name] || name;
  }

  function getRecentDirectories() {
    try {
      return normalizeRecentDirectories(JSON.parse(localStorage.getItem(recentDirectoriesKey) || "[]"));
    } catch {
      return [];
    }
  }

  function rememberDirectory(path) {
    const normalized = canonicalLocalPath(path);
    if (!normalized) return;
    const next = [normalized, ...getRecentDirectories().filter((item) => normalizePath(item) !== normalizePath(normalized))].slice(0, 8);
    localStorage.setItem(recentDirectoriesKey, JSON.stringify(next));
    renderRecentModalDirectories();
  }

  function renderRecentModalDirectories() {
    const el = $("recentModalDirectories");
    if (!el) return;
    const recent = getRecentDirectories();
    el.innerHTML = recent.length ? recent.map((rawPath) => {
      const path = canonicalLocalPath(rawPath);
      return `
      <button class="folder-shortcut" type="button" data-path="${escapeAttr(path)}" title="${escapeAttr(path)}">
        <span class="folder-shortcut-icon">${directoryFavoriteIcon}</span>
        <span>${escapeHtml(basename(path) || path)}</span>
      </button>
    `;
    }).join("") : `<div class="folder-empty-note">${escapeHtml(t("workspace.directory.noFavorites"))}</div>`;
    el.querySelectorAll("[data-path]").forEach((node) => {
      node.addEventListener("click", () => browseDirectories(node.dataset.path).catch(reportError));
    });
  }

  function renderDirectoryList(data) {
    const rows = [];
    if (data.parent) {
      rows.push(`
      <button class="directory-row parent-row" type="button" data-path="${escapeAttr(data.parent)}">
        <span class="directory-row-main"><span class="directory-icon">↰</span><span>${escapeHtml(t("workspace.directory.parent"))}</span></span>
        <span class="directory-meta">..</span>
      </button>
    `);
    }
    rows.push(...(data.entries || []).map((entry) => `
    <button class="directory-row" type="button" data-path="${escapeAttr(entry.path)}">
      <span class="directory-row-main"><span class="directory-icon" aria-hidden="true">${directoryFolderIcon}</span><span>${escapeHtml(entry.name)}</span></span>
      <span class="directory-meta">${escapeHtml(t("workspace.directory.folder"))}</span>
    </button>
  `));
    $("directoryList").innerHTML = rows.join("") || `<div class="folder-empty-state">${escapeHtml(t("workspace.directory.empty"))}</div>`;
    $("directoryList").querySelectorAll("[data-path]").forEach((node) => {
      node.addEventListener("click", () => browseDirectories(node.dataset.path).catch(reportError));
    });
  }

  function homeShortcutPath() {
    const home = state.directoryShortcuts.find((shortcut) => shortcut.name === "Home");
    return home?.path || "";
  }

  async function browseHomeDirectory() {
    await browseDirectories(homeShortcutPath());
  }

  async function browseParentDirectory() {
    if (!state.directoryParent) return;
    await browseDirectories(state.directoryParent);
  }

  async function refreshDirectory() {
    await browseDirectories(state.directoryPath);
  }

  function showNewFolderInline() {
    const row = $("newFolderInline");
    const input = $("newFolderNameInput");
    const trigger = $("newFolderBtn");
    row.classList.remove("hidden");
    trigger?.classList.add("active");
    trigger?.setAttribute("aria-expanded", "true");
    input.value = "";
    window.setTimeout(() => input.focus(), 0);
  }

  function hideNewFolderInline() {
    const row = $("newFolderInline");
    const trigger = $("newFolderBtn");
    if (!row) return;
    row.classList.add("hidden");
    trigger?.classList.remove("active");
    trigger?.setAttribute("aria-expanded", "false");
    if ($("newFolderNameInput")) $("newFolderNameInput").value = "";
  }

  async function createFolderInCurrentDirectory() {
    const input = $("newFolderNameInput");
    const button = $("confirmNewFolderBtn");
    if (button?.disabled) return;
    const trimmed = input?.value.trim() || "";
    if (!trimmed) {
      showToast?.(t("workspace.directory.nameRequired"), "warn");
      input?.focus();
      return;
    }
    if (trimmed === "." || trimmed === "..") {
      showToast?.(t("workspace.directory.nameDots"), "warn");
      input?.focus();
      return;
    }
    if (trimmed.length > 255) {
      showToast?.(t("workspace.directory.nameTooLong"), "warn");
      input?.focus();
      return;
    }
    if (/[\\/\0]/.test(trimmed)) {
      throw new Error(t("workspace.directory.nameInvalid"));
    }
    const base = normalizePath(state.directoryPath);
    const path = base === "/" ? `/${trimmed}` : `${base}/${trimmed}`;
    const previousLabel = button?.textContent || t("folder.create");
    if (button) {
      button.textContent = button.classList.contains("composer-send-btn") ? "…" : t("workspace.chat.sending");
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
    }
    if (input) input.disabled = true;
    try {
      await api("/api/fs/mkdir", { method: "POST", body: JSON.stringify({ path }) });
      hideNewFolderInline();
      showToast?.(t("workspace.directory.created"), "success");
      await browseDirectories(path);
    } finally {
      if (button) {
        button.textContent = previousLabel;
        button.disabled = false;
        button.removeAttribute("aria-busy");
      }
      if (input) input.disabled = false;
    }
  }

  function favoriteCurrentDirectory() {
    if (!state.directoryPath) return;
    rememberDirectory(state.directoryPath);
  }

  return {
    browseDirectories,
    browseHomeDirectory,
    browseParentDirectory,
    closeDirectoryModal,
    createFolderInCurrentDirectory,
    favoriteCurrentDirectory,
    hideNewFolderInline,
    openDirectoryChooser,
    refreshDirectory,
    rememberDirectory,
    renderRecentModalDirectories,
    selectNativeDirectory,
    setDirectoryStatus,
    showNewFolderInline,
    updateDirectoryPathDisplay,
  };
}
