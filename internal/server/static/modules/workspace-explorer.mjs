import { $, escapeAttr, escapeHtml } from "./dom.mjs";
import { formatBytes } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { shellExtraT as sx } from "./messages-shell-extra.mjs";

export const PREVIEW_IFRAME_SANDBOX = "allow-downloads allow-forms allow-modals allow-pointer-lock allow-popups allow-popups-to-escape-sandbox allow-scripts";
const PREVIEW_LOG_LINE_LIMIT = 400;
const PREVIEW_LOG_CHAR_LIMIT = 50000;

export function workspaceParentPath(path) {
  const normalized = String(path || "").replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
  if (!normalized || normalized === ".") return "";
  const parts = normalized.split("/").filter(Boolean);
  parts.pop();
  return parts.join("/");
}

export function buildWorkspaceSavePayload(path, content, expectedModTime) {
  return {
    path: String(path || ""),
    content: String(content ?? ""),
    expectedModTime: expectedModTime ?? null,
  };
}

export function renderWorkspaceEntriesHTML(entries, selectedPath = "") {
  const sorted = [...(Array.isArray(entries) ? entries : [])].sort((a, b) => {
    const directoryOrder = Number(Boolean(b?.isDir)) - Number(Boolean(a?.isDir));
    return directoryOrder || String(a?.name || "").localeCompare(String(b?.name || ""));
  });
  if (!sorted.length) return `<div class="workspace-empty-state">${escapeHtml(t("workspace.explorer.emptyDirectory"))}</div>`;
  return sorted.map((entry) => {
    const path = String(entry?.path || "");
    const name = String(entry?.name || path || t("workspace.explorer.unnamed"));
    const isDir = Boolean(entry?.isDir);
    const editable = entry?.editable !== false;
    const size = Number.isFinite(Number(entry?.size)) ? Number(entry.size) : 0;
    const meta = isDir ? t("workspace.explorer.directory") : `${formatBytes(size)}${editable ? "" : ` · ${t("workspace.explorer.readOnly")}`}`;
    return `
      <button class="workspace-entry ${path === selectedPath ? "active" : ""}" type="button" data-workspace-path="${escapeAttr(path)}" data-workspace-dir="${isDir ? "true" : "false"}" title="${escapeAttr(path || name)}">
        <span class="workspace-entry-icon" aria-hidden="true">${isDir ? "▱" : "◇"}</span>
        <span class="workspace-entry-main"><strong>${escapeHtml(name)}</strong><small>${escapeHtml(meta)}</small></span>
        <span class="workspace-entry-arrow" aria-hidden="true">${isDir ? "›" : ""}</span>
      </button>
    `;
  }).join("");
}

export function renderPreviewProfileOptionsHTML(profiles, selectedId = "") {
  const list = Array.isArray(profiles) ? profiles : [];
  if (!list.length) return `<option value="">${escapeHtml(t("workspace.explorer.noProfiles"))}</option>`;
  return list.map((profile) => {
    const id = previewProfileId(profile);
    const label = previewProfileLabel(profile);
    return `<option value="${escapeAttr(id)}" ${id === selectedId ? "selected" : ""}>${escapeHtml(label)}</option>`;
  }).join("");
}

export function renderPreviewFrameHTML(url) {
  const safeURL = String(url || "");
  if (!safeURL) return `<div class="workspace-preview-empty">${escapeHtml(t("workspace.explorer.previewEmpty"))}</div>`;
  return `<iframe class="workspace-preview-iframe" src="${escapeAttr(safeURL)}" sandbox="${escapeAttr(PREVIEW_IFRAME_SANDBOX)}" referrerpolicy="no-referrer" title="${escapeAttr(t("workspace.explorer.previewTitle"))}"></iframe>`;
}

export function buildPreviewURL(status, locationLike = globalThis.location) {
  const source = status || {};
  let candidate = String(source.url || source.previewUrl || source.externalUrl || source.baseUrl || "").trim();
  const port = Number(source.port || source.previewPort || 0);
  if (!candidate && port > 0 && port <= 65535) {
    const protocol = locationLike?.protocol === "https:" ? "https:" : "http:";
    const hostname = String(locationLike?.hostname || "127.0.0.1");
    candidate = `${protocol}//${hostname}:${port}/`;
  }
  if (!candidate) return "";
  try {
    const base = locationLike?.origin || "http://127.0.0.1";
    const parsed = new URL(candidate, base);
    if (!/^https?:$/.test(parsed.protocol)) return "";
    if (locationLike?.origin && parsed.origin === locationLike.origin) return "";
    return parsed.href;
  } catch {
    return "";
  }
}

export function normalizePreviewNavigationURL(value, baseURL = "", locationLike = globalThis.location) {
  const raw = String(value || "").trim();
  if (!raw) return "";
  try {
    const base = baseURL || `${locationLike?.protocol || "http:"}//${locationLike?.hostname || "localhost"}/`;
    const url = new URL(raw, base);
    if (!["http:", "https:"].includes(url.protocol)) return "";
    return url.href;
  } catch {
    return "";
  }
}

export function boundPreviewLogs(payload) {
  let text = "";
  if (typeof payload === "string") text = payload;
  else if (Array.isArray(payload)) text = payload.map(previewLogLine).join("\n");
  else if (Array.isArray(payload?.logs)) text = payload.logs.map(previewLogLine).join("\n");
  else if (Array.isArray(payload?.lines)) text = payload.lines.map(previewLogLine).join("\n");
  else if (typeof payload?.logs === "string") text = payload.logs;
  else if (typeof payload?.content === "string") text = payload.content;
  const lines = String(text || "").split(/\r?\n/).slice(-PREVIEW_LOG_LINE_LIMIT);
  return lines.join("\n").slice(-PREVIEW_LOG_CHAR_LIMIT);
}

export function createWorkspaceExplorerController({
  state = {},
  request,
  getElementById = $,
  locationLike = globalThis.location,
  openWindow = (...args) => globalThis.window?.open?.(...args),
  setTimeoutFn = (...args) => globalThis.setTimeout(...args),
  clearTimeoutFn = (...args) => globalThis.clearTimeout(...args),
  showError = () => {},
  showToast = () => {},
  onPreviewOpen = () => {},
  onPreviewClose = () => {},
  onScreenshot = () => {},
  pollIntervalMs = 2000,
} = {}) {
  initializeWorkspaceState(state);

  const element = (id) => getElementById?.(id) || null;
  const currentAgentId = () => String(state.workspaceAgentId || "");
  const liveAgentId = () => String(state.agent?.id || "");

  function isCurrent(seqKey, seq, agentId) {
    return state[seqKey] === seq && currentAgentId() === agentId;
  }

  function invalidateRequests() {
    state.workspaceTreeSeq += 1;
    state.workspaceFileSeq += 1;
    state.workspaceSaveSeq += 1;
    state.workspacePreviewSeq += 1;
  }

  function stopPreviewPolling() {
    if (state.workspacePreviewTimer) clearTimeoutFn(state.workspacePreviewTimer);
    state.workspacePreviewTimer = null;
  }

  function schedulePreviewPolling() {
    stopPreviewPolling();
    if (!state.workspaceOpen || state.workspaceTab !== "preview" || !currentAgentId()) return;
    state.workspacePreviewTimer = setTimeoutFn(() => {
      state.workspacePreviewTimer = null;
      refreshPreview({ polling: true }).catch(() => {});
    }, pollIntervalMs);
    state.workspacePreviewTimer?.unref?.();
  }

  function resetWorkspaceView() {
    state.workspaceTab = "files";
    state.workspacePath = "";
    state.workspaceEntries = [];
    state.workspaceTreeLoading = false;
    state.workspaceTreeError = "";
    state.workspaceFile = null;
    state.workspaceFilePath = "";
    state.workspaceFileContent = "";
    state.workspaceOriginalContent = "";
    state.workspaceFileLoading = false;
    state.workspaceSaving = false;
    state.workspaceFileStatus = t("workspace.explorer.selectFile");
    state.workspaceFileStatusError = false;
    state.workspaceProfiles = [];
    state.workspaceSelectedProfileId = "";
    state.workspacePreviewStatus = null;
    state.workspacePreviewLogs = "";
    state.workspacePreviewLoading = false;
    state.workspacePreviewBusy = false;
    state.workspacePreviewError = "";
    state.workspacePreviewURL = "";
    state.workspacePreviewHistory = [];
    state.workspacePreviewHistoryIndex = -1;
    state.workspacePreviewViewport = "adaptive";
    state.workspacePreviewAutoStart = false;
  }

  function setAgent(agent) {
    const nextId = String(agent?.id || "");
    if (nextId === currentAgentId()) {
      renderWorkspaceButtonState();
      return false;
    }
    const wasPreview = state.workspaceOpen && state.workspaceTab === "preview";
    invalidateRequests();
    stopPreviewPolling();
    state.workspaceOpen = false;
    element("workspaceModal")?.classList.add("hidden");
    element("workspaceModal")?.classList.remove("workspace-preview-dock-mode");
    if (wasPreview) onPreviewClose();
    state.workspaceAgentId = nextId;
    resetWorkspaceView();
    renderWorkspaceButtonState();
    renderWorkspace();
    return true;
  }

  function renderWorkspaceButtonState() {
    const filesButton = element("workspaceExplorerBtn");
    const previewButton = element("workspacePreviewBtn");
    const enabled = Boolean(liveAgentId());
    const filesOpen = enabled && state.workspaceOpen && state.workspaceTab === "files";
    const previewOpen = enabled && state.workspaceOpen && state.workspaceTab === "preview";
    const running = previewRunning(state.workspacePreviewStatus || {});

    if (filesButton) {
      filesButton.disabled = !enabled;
      filesButton.classList.toggle("active", filesOpen);
      filesButton.setAttribute("aria-expanded", filesOpen ? "true" : "false");
      filesButton.title = enabled ? t("workspace.explorer.openFiles") : t("workspace.explorer.selectAgent");
    }
    if (previewButton) {
      previewButton.disabled = !enabled;
      previewButton.classList.toggle("active", previewOpen);
      previewButton.classList.toggle("preview-running", running);
      previewButton.setAttribute("aria-expanded", previewOpen ? "true" : "false");
      previewButton.title = !enabled ? t("workspace.explorer.selectAgent") : running ? t("workspace.explorer.previewRunning") : t("workspace.explorer.openPreview");
    }
    const indicator = element("workspacePreviewIndicator");
    if (indicator) {
      indicator.classList.toggle("running", running);
      indicator.classList.toggle("busy", !running && (state.workspacePreviewLoading || state.workspacePreviewBusy));
      indicator.classList.toggle("error", Boolean(state.workspacePreviewError));
      indicator.classList.toggle("idle", !running && !state.workspacePreviewLoading && !state.workspacePreviewBusy && !state.workspacePreviewError);
    }
  }

  function openWorkspace(tab = "files") {
    if (!liveAgentId()) return false;
    if (currentAgentId() !== liveAgentId()) setAgent(state.agent);
    state.workspaceTab = tab === "preview" ? "preview" : "files";
    state.workspaceOpen = true;
    const modal = element("workspaceModal");
    modal?.classList.remove("hidden");
    modal?.classList.toggle("workspace-preview-dock-mode", state.workspaceTab === "preview");
    if (state.workspaceTab === "preview") onPreviewOpen();
    renderWorkspaceButtonState();
    renderWorkspace();
    if (state.workspaceTab === "preview") loadPreview().catch(showError);
    else loadTree(state.workspacePath || "").catch(showError);
    return true;
  }

  function closeWorkspace() {
    const wasPreview = state.workspaceOpen && state.workspaceTab === "preview";
    state.workspaceOpen = false;
    invalidateRequests();
    stopPreviewPolling();
    state.workspaceTreeLoading = false;
    state.workspaceFileLoading = false;
    state.workspaceSaving = false;
    state.workspacePreviewLoading = false;
    state.workspacePreviewBusy = false;
    const modal = element("workspaceModal");
    modal?.classList.add("hidden");
    modal?.classList.remove("workspace-preview-dock-mode");
    if (wasPreview) onPreviewClose();
    renderWorkspaceButtonState();
  }

  async function loadTree(path = "") {
    const agentId = currentAgentId();
    if (!agentId) return null;
    const requestedPath = normalizeRelativePath(path);
    const seq = ++state.workspaceTreeSeq;
    state.workspaceTreeLoading = true;
    state.workspaceTreeError = "";
    renderTree();
    try {
      const query = new URLSearchParams({ path: requestedPath });
      const payload = await request(`/api/agents/${encodeURIComponent(agentId)}/workspace/tree?${query.toString()}`);
      if (!isCurrent("workspaceTreeSeq", seq, agentId)) return null;
      state.workspacePath = normalizeRelativePath(payload?.path ?? requestedPath);
      state.workspaceEntries = Array.isArray(payload?.entries) ? payload.entries : [];
      state.workspaceTreeError = "";
      return payload;
    } catch (error) {
      if (!isCurrent("workspaceTreeSeq", seq, agentId)) return null;
      state.workspaceEntries = [];
      state.workspaceTreeError = error?.message || String(error);
      throw error;
    } finally {
      if (isCurrent("workspaceTreeSeq", seq, agentId)) {
        state.workspaceTreeLoading = false;
        renderTree();
      }
    }
  }

  async function loadFile(path) {
    const agentId = currentAgentId();
    const requestedPath = normalizeRelativePath(path);
    if (!agentId || !requestedPath) return null;
    const seq = ++state.workspaceFileSeq;
    state.workspaceFileLoading = true;
    state.workspaceFileStatus = t("workspace.explorer.loadingFile");
    state.workspaceFileStatusError = false;
    renderEditor();
    try {
      const query = new URLSearchParams({ path: requestedPath });
      const payload = await request(`/api/agents/${encodeURIComponent(agentId)}/workspace/file?${query.toString()}`);
      if (!isCurrent("workspaceFileSeq", seq, agentId)) return null;
      state.workspaceFile = { ...payload, path: normalizeRelativePath(payload?.path ?? requestedPath) };
      state.workspaceFilePath = state.workspaceFile.path;
      state.workspaceFileContent = String(payload?.content ?? "");
      state.workspaceOriginalContent = state.workspaceFileContent;
      state.workspaceFileStatus = workspaceFileStatusText(state.workspaceFile);
      state.workspaceFileStatusError = false;
      renderTree();
      return payload;
    } catch (error) {
      if (!isCurrent("workspaceFileSeq", seq, agentId)) return null;
      state.workspaceFile = null;
      state.workspaceFilePath = requestedPath;
      state.workspaceFileContent = "";
      state.workspaceOriginalContent = "";
      state.workspaceFileStatus = t("workspace.explorer.loadFailed", { message: error?.message || String(error) });
      state.workspaceFileStatusError = true;
      throw error;
    } finally {
      if (isCurrent("workspaceFileSeq", seq, agentId)) {
        state.workspaceFileLoading = false;
        renderEditor();
      }
    }
  }

  async function saveFile() {
    const agentId = currentAgentId();
    const file = state.workspaceFile;
    const editor = element("workspaceEditor");
    if (editor) state.workspaceFileContent = String(editor.value ?? "");
    if (!agentId || !file?.path) return false;
    if (file.readOnly || file.truncated) {
      state.workspaceFileStatus = file.truncated ? t("workspace.explorer.truncated") : t("workspace.explorer.fileReadOnly");
      state.workspaceFileStatusError = true;
      renderEditorControls();
      showToast(state.workspaceFileStatus, "warn", { force: true });
      return false;
    }
    const seq = ++state.workspaceSaveSeq;
    const path = normalizeRelativePath(file.path);
    const payload = buildWorkspaceSavePayload(path, state.workspaceFileContent, file.modTime);
    state.workspaceSaving = true;
    state.workspaceFileStatus = t("workspace.explorer.saving");
    state.workspaceFileStatusError = false;
    renderEditorControls();
    try {
      const result = await request(`/api/agents/${encodeURIComponent(agentId)}/workspace/file?${new URLSearchParams({ path }).toString()}`, {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      if (!isCurrent("workspaceSaveSeq", seq, agentId)) return false;
      state.workspaceFile = { ...file, ...result, path, content: state.workspaceFileContent };
      state.workspaceOriginalContent = state.workspaceFileContent;
      state.workspaceFileStatus = t("workspace.explorer.saved");
      state.workspaceFileStatusError = false;
      showToast(t("workspace.explorer.fileSaved"), "success", { force: true });
      return true;
    } catch (error) {
      if (!isCurrent("workspaceSaveSeq", seq, agentId)) return false;
      if (Number(error?.status) === 409) {
        state.workspaceFileStatus = t("workspace.explorer.fileChanged");
        state.workspaceFileStatusError = true;
        showToast(state.workspaceFileStatus, "error", { force: true });
        return false;
      }
      state.workspaceFileStatus = sx("workspace.saveFailed", { message: error?.message || String(error) });
      state.workspaceFileStatusError = true;
      throw error;
    } finally {
      if (isCurrent("workspaceSaveSeq", seq, agentId)) {
        state.workspaceSaving = false;
        renderEditorControls();
      }
    }
  }

  function selectTab(tab) {
    const next = tab === "preview" ? "preview" : "files";
    if (state.workspaceTab === next) {
      renderWorkspaceButtonState();
      return;
    }
    const previous = state.workspaceTab;
    state.workspaceTab = next;
    state.workspacePreviewSeq += 1;
    stopPreviewPolling();
    element("workspaceModal")?.classList.toggle("workspace-preview-dock-mode", next === "preview");
    if (state.workspaceOpen && previous !== "preview" && next === "preview") onPreviewOpen();
    if (state.workspaceOpen && previous === "preview" && next !== "preview") onPreviewClose();
    renderWorkspaceButtonState();
    renderWorkspace();
    if (next === "preview") loadPreview().catch(showError);
  }

  async function loadPreview() {
    const agentId = currentAgentId();
    if (!agentId) return null;
    const seq = ++state.workspacePreviewSeq;
    state.workspacePreviewLoading = true;
    state.workspacePreviewError = "";
    renderPreview();
    try {
      const base = `/api/agents/${encodeURIComponent(agentId)}/preview`;
      const detected = await request(`${base}/detect`);
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return null;
      state.workspaceProfiles = Array.isArray(detected?.profiles) ? detected.profiles : [];
      const availableIds = state.workspaceProfiles.map(previewProfileId);
      if (!availableIds.includes(state.workspaceSelectedProfileId)) state.workspaceSelectedProfileId = availableIds[0] || "";
      const [status, logs] = await Promise.all([request(`${base}/status`), request(`${base}/logs`)]);
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return null;
      applyPreviewPayload(status, logs);
      if (state.workspacePreviewAutoStart && !previewRunning(status) && state.workspaceSelectedProfileId) {
        setTimeoutFn(() => startPreview().catch(showError), 0);
      }
      return { detected, status, logs };
    } catch (error) {
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return null;
      state.workspacePreviewError = error?.message || String(error);
      throw error;
    } finally {
      if (isCurrent("workspacePreviewSeq", seq, agentId)) {
        state.workspacePreviewLoading = false;
        renderPreview();
        schedulePreviewPolling();
      }
    }
  }

  async function refreshPreview({ polling = false } = {}) {
    const agentId = currentAgentId();
    if (!agentId || !state.workspaceOpen || state.workspaceTab !== "preview") return null;
    const seq = ++state.workspacePreviewSeq;
    if (!polling) state.workspacePreviewLoading = true;
    try {
      const base = `/api/agents/${encodeURIComponent(agentId)}/preview`;
      const [status, logs] = await Promise.all([request(`${base}/status`), request(`${base}/logs`)]);
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return null;
      applyPreviewPayload(status, logs);
      state.workspacePreviewError = "";
      return { status, logs };
    } catch (error) {
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return null;
      state.workspacePreviewError = error?.message || String(error);
      if (!polling) throw error;
      return null;
    } finally {
      if (isCurrent("workspacePreviewSeq", seq, agentId)) {
        state.workspacePreviewLoading = false;
        renderPreview();
        schedulePreviewPolling();
      }
    }
  }

  async function startPreview() {
    const agentId = currentAgentId();
    const profileId = String(element("workspacePreviewProfile")?.value || state.workspaceSelectedProfileId || "");
    if (!agentId || !profileId || state.workspacePreviewBusy) return false;
    const port = parsePreviewPort(element("workspacePreviewPort")?.value);
    const seq = ++state.workspacePreviewSeq;
    state.workspacePreviewBusy = true;
    state.workspacePreviewError = "";
    renderPreview();
    try {
      const body = { profileId };
      if (port) body.port = port;
      const status = await request(`/api/agents/${encodeURIComponent(agentId)}/preview/start`, { method: "POST", body: JSON.stringify(body) });
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return false;
      state.workspacePreviewStatus = status || {};
      showToast(t("workspace.explorer.previewStarted"), "success", { force: true });
      await refreshPreview();
      return true;
    } catch (error) {
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return false;
      state.workspacePreviewError = error?.message || String(error);
      throw error;
    } finally {
      if (currentAgentId() === agentId) {
        state.workspacePreviewBusy = false;
        renderPreview();
      }
    }
  }

  async function stopPreview() {
    const agentId = currentAgentId();
    if (!agentId || state.workspacePreviewBusy) return false;
    const seq = ++state.workspacePreviewSeq;
    state.workspacePreviewBusy = true;
    state.workspacePreviewError = "";
    renderPreview();
    try {
      const status = await request(`/api/agents/${encodeURIComponent(agentId)}/preview/stop`, { method: "POST" });
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return false;
      state.workspacePreviewStatus = status || { running: false };
      showToast(t("workspace.explorer.previewStopped"), "success", { force: true });
      await refreshPreview();
      return true;
    } catch (error) {
      if (!isCurrent("workspacePreviewSeq", seq, agentId)) return false;
      state.workspacePreviewError = error?.message || String(error);
      throw error;
    } finally {
      if (currentAgentId() === agentId) {
        state.workspacePreviewBusy = false;
        renderPreview();
      }
    }
  }

  function applyPreviewPayload(status, logs) {
    state.workspacePreviewStatus = status || {};
    state.workspacePreviewLogs = boundPreviewLogs(logs);
    const statusProfileId = String(status?.profileId || "");
    if (statusProfileId) state.workspaceSelectedProfileId = statusProfileId;
    const homeURL = buildPreviewURL(status, locationLike);
    if (homeURL && !state.workspacePreviewURL) {
      state.workspacePreviewURL = homeURL;
      state.workspacePreviewHistory = [homeURL];
      state.workspacePreviewHistoryIndex = 0;
    }
  }

  function normalizedBrowserURL(value) {
    const base = state.workspacePreviewURL || buildPreviewURL(state.workspacePreviewStatus, locationLike);
    return normalizePreviewNavigationURL(value, base, locationLike);
  }

  function navigatePreview(value, { replace = false } = {}) {
    const url = normalizedBrowserURL(value);
    if (!url) {
      showToast(t("workspace.explorer.invalidAddress"), "warn", { force: true });
      return false;
    }
    const history = Array.isArray(state.workspacePreviewHistory) ? [...state.workspacePreviewHistory] : [];
    let index = Number(state.workspacePreviewHistoryIndex ?? -1);
    if (replace && index >= 0) history[index] = url;
    else {
      history.splice(index + 1);
      history.push(url);
      index = history.length - 1;
    }
    state.workspacePreviewURL = url;
    state.workspacePreviewHistory = history.slice(-40);
    state.workspacePreviewHistoryIndex = Math.min(index, state.workspacePreviewHistory.length - 1);
    renderPreview();
    return true;
  }

  function navigatePreviewHistory(delta) {
    const history = state.workspacePreviewHistory || [];
    const next = Number(state.workspacePreviewHistoryIndex || 0) + delta;
    if (next < 0 || next >= history.length) return false;
    state.workspacePreviewHistoryIndex = next;
    state.workspacePreviewURL = history[next];
    renderPreview();
    return true;
  }

  function reloadPreviewFrame() {
    const frameHost = element("workspacePreviewFrameHost");
    if (frameHost?.dataset) frameHost.dataset.previewUrl = "";
    renderPreview();
  }

  function setPreviewViewport(viewport) {
    state.workspacePreviewViewport = ["desktop", "tablet"].includes(viewport) ? viewport : "adaptive";
    renderPreview();
  }

  function openPreviewExternal() {
    const url = state.workspacePreviewURL || buildPreviewURL(state.workspacePreviewStatus, locationLike);
    if (!url) {
      showToast(t("workspace.explorer.previewUnavailable"), "warn", { force: true });
      return false;
    }
    openWindow(url, "_blank", "noopener,noreferrer");
    return true;
  }

  function renderWorkspace() {
    const filesPanel = element("workspaceFilesPanel");
    const previewPanel = element("workspacePreviewPanel");
    filesPanel?.classList.toggle("hidden", state.workspaceTab !== "files");
    previewPanel?.classList.toggle("hidden", state.workspaceTab !== "preview");
    ["files", "preview"].forEach((tab) => {
      const button = element(tab === "files" ? "workspaceFilesTab" : "workspacePreviewTab");
      const active = state.workspaceTab === tab;
      button?.classList.toggle("active", active);
      button?.setAttribute("aria-selected", active ? "true" : "false");
    });
    const subtitle = element("workspaceModalSubtitle");
    if (subtitle) subtitle.textContent = state.agent?.cwd || state.project?.gitPath || t("workspace.explorer.agentDirectory");
    renderTree();
    renderEditor();
    renderPreview();
  }

  function renderTree() {
    const path = element("workspaceTreePath");
    if (path) path.textContent = state.workspacePath || "/";
    const parent = element("workspaceParentBtn");
    if (parent) parent.disabled = !state.workspacePath || state.workspaceTreeLoading;
    const refresh = element("workspaceRefreshTreeBtn");
    if (refresh) refresh.disabled = state.workspaceTreeLoading;
    const tree = element("workspaceTree");
    if (!tree) return;
    if (state.workspaceTreeLoading) tree.innerHTML = `<div class="workspace-loading-state">${escapeHtml(t("workspace.explorer.loadingDirectory"))}</div>`;
    else if (state.workspaceTreeError) tree.innerHTML = `<div class="workspace-error-state">${escapeHtml(state.workspaceTreeError)}</div>`;
    else tree.innerHTML = renderWorkspaceEntriesHTML(state.workspaceEntries, state.workspaceFilePath);
  }

  function renderEditor() {
    const editor = element("workspaceEditor");
    if (editor && editor.value !== state.workspaceFileContent) editor.value = state.workspaceFileContent;
    renderEditorControls();
  }

  function renderEditorControls() {
    const file = state.workspaceFile;
    const editor = element("workspaceEditor");
    const readOnly = !file || Boolean(file.readOnly || file.truncated || state.workspaceFileLoading);
    if (editor) {
      editor.readOnly = readOnly;
      editor.disabled = state.workspaceFileLoading;
      editor.placeholder = state.workspaceFileLoading ? t("workspace.explorer.editorLoading") : t("workspace.explorer.editorHint");
    }
    const path = element("workspaceEditorPath");
    if (path) path.textContent = state.workspaceFilePath || t("workspace.explorer.noFile");
    const status = element("workspaceEditorStatus");
    if (status) {
      status.textContent = state.workspaceFileStatus || "";
      status.classList.toggle("error", Boolean(state.workspaceFileStatusError));
    }
    const reload = element("workspaceReloadFileBtn");
    if (reload) reload.disabled = !file?.path || state.workspaceFileLoading || state.workspaceSaving;
    const save = element("workspaceSaveFileBtn");
    if (save) save.disabled = !file?.path || readOnly || state.workspaceSaving || state.workspaceFileContent === state.workspaceOriginalContent;
  }

  function renderPreview() {
    const profile = element("workspacePreviewProfile");
    if (profile) {
      profile.innerHTML = renderPreviewProfileOptionsHTML(state.workspaceProfiles, state.workspaceSelectedProfileId);
      profile.value = state.workspaceSelectedProfileId || "";
      profile.disabled = state.workspacePreviewLoading || state.workspacePreviewBusy || !state.workspaceProfiles.length;
    }
    const status = state.workspacePreviewStatus || {};
    const running = previewRunning(status);
    const statusNode = element("workspacePreviewStatus");
    if (statusNode) {
      statusNode.textContent = previewStatusText(status, state.workspacePreviewLoading);
      statusNode.classList.toggle("running", running);
    }
    const errorNode = element("workspacePreviewError");
    if (errorNode) {
      errorNode.textContent = state.workspacePreviewError || "";
      errorNode.classList.toggle("hidden", !state.workspacePreviewError);
    }
    const detect = element("workspaceDetectPreviewBtn");
    if (detect) detect.disabled = state.workspacePreviewLoading || state.workspacePreviewBusy;
    const start = element("workspaceStartPreviewBtn");
    if (start) start.disabled = state.workspacePreviewBusy || state.workspacePreviewLoading || running || !state.workspaceSelectedProfileId;
    const stop = element("workspaceStopPreviewBtn");
    if (stop) stop.disabled = state.workspacePreviewBusy || !running;
    const homeURL = buildPreviewURL(status, locationLike);
    const url = state.workspacePreviewURL || homeURL;
    const external = element("workspaceOpenPreviewBtn");
    if (external) external.disabled = !url;
    const address = element("workspacePreviewAddress");
    if (address && address.value !== url) address.value = url || "";
    const history = state.workspacePreviewHistory || [];
    const historyIndex = Number(state.workspacePreviewHistoryIndex ?? -1);
    const back = element("workspacePreviewBackBtn");
    const forward = element("workspacePreviewForwardBtn");
    if (back) back.disabled = historyIndex <= 0;
    if (forward) forward.disabled = historyIndex < 0 || historyIndex >= history.length - 1;
    const autoStart = element("workspacePreviewAutoStart");
    if (autoStart) autoStart.checked = Boolean(state.workspacePreviewAutoStart);
    globalThis.document?.querySelectorAll?.("[data-preview-viewport]").forEach((button) => {
      button.classList.toggle("active", button.dataset.previewViewport === state.workspacePreviewViewport);
    });
    const frameHost = element("workspacePreviewFrameHost");
    if (frameHost) {
      frameHost.classList.remove("viewport-adaptive", "viewport-desktop", "viewport-tablet");
      frameHost.classList.add(`viewport-${state.workspacePreviewViewport || "adaptive"}`);
      if (String(frameHost.dataset?.previewUrl || "") !== url) {
        frameHost.innerHTML = renderPreviewFrameHTML(url);
        if (frameHost.dataset) frameHost.dataset.previewUrl = url;
      }
    }
    const logs = element("workspacePreviewLogs");
    if (logs) logs.textContent = state.workspacePreviewLogs || t("workspace.explorer.noLogs");
    renderWorkspaceButtonState();
  }

  // The preview toolbar markup ships without a screenshot button; insert it
  // next to the existing reload button so it lives in the same toolbar
  // without requiring a static-HTML change. Additive and idempotent.
  function ensureScreenshotButton() {
    if (element("workspacePreviewScreenshotBtn")) return;
    const reloadBtn = element("workspacePreviewReloadBtn");
    const doc = globalThis.document;
    if (!reloadBtn?.parentNode || !doc?.createElement) return;
    const button = doc.createElement("button");
    button.id = "workspacePreviewScreenshotBtn";
    button.type = "button";
    button.className = "workspace-browser-icon";
    button.textContent = "✂";
    button.setAttribute("data-i18n-title", "workspace.explorer.screenshot");
    button.setAttribute("data-i18n-aria-label", "workspace.explorer.screenshot");
    button.title = t("workspace.explorer.screenshot");
    button.setAttribute("aria-label", t("workspace.explorer.screenshot"));
    reloadBtn.insertAdjacentElement("afterend", button);
  }

  function bind() {
    ensureScreenshotButton();
    element("workspacePreviewScreenshotBtn")?.addEventListener("click", () => onScreenshot());
    element("workspaceExplorerBtn")?.addEventListener("click", () => openWorkspace("files"));
    element("workspacePreviewBtn")?.addEventListener("click", () => openWorkspace("preview"));
    element("closeWorkspaceModalBtn")?.addEventListener("click", closeWorkspace);
    element("workspaceModal")?.addEventListener("click", (event) => {
      if (event.target?.id === "workspaceModal") closeWorkspace();
    });
    element("workspaceFilesTab")?.addEventListener("click", () => selectTab("files"));
    element("workspacePreviewTab")?.addEventListener("click", () => selectTab("preview"));
    element("workspaceParentBtn")?.addEventListener("click", () => loadTree(workspaceParentPath(state.workspacePath)).catch(showError));
    element("workspaceRefreshTreeBtn")?.addEventListener("click", () => loadTree(state.workspacePath).catch(showError));
    element("workspaceTree")?.addEventListener("click", (event) => {
      const target = event.target?.closest?.("[data-workspace-path]");
      if (!target) return;
      const path = target.dataset.workspacePath || "";
      if (target.dataset.workspaceDir === "true") loadTree(path).catch(showError);
      else loadFile(path).catch(showError);
    });
    element("workspaceEditor")?.addEventListener("input", (event) => {
      state.workspaceFileContent = String(event.target?.value ?? "");
      state.workspaceFileStatus = state.workspaceFileContent === state.workspaceOriginalContent ? workspaceFileStatusText(state.workspaceFile) : t("workspace.explorer.unsaved");
      state.workspaceFileStatusError = false;
      renderEditorControls();
    });
    element("workspaceSaveFileBtn")?.addEventListener("click", () => saveFile().catch(showError));
    element("workspaceReloadFileBtn")?.addEventListener("click", () => loadFile(state.workspaceFilePath).catch(showError));
    element("workspacePreviewProfile")?.addEventListener("change", (event) => {
      state.workspaceSelectedProfileId = String(event.target?.value || "");
      renderPreview();
    });
    element("workspaceDetectPreviewBtn")?.addEventListener("click", () => loadPreview().catch(showError));
    element("workspaceStartPreviewBtn")?.addEventListener("click", () => startPreview().catch(showError));
    element("workspaceStopPreviewBtn")?.addEventListener("click", () => stopPreview().catch(showError));
    element("workspaceOpenPreviewBtn")?.addEventListener("click", openPreviewExternal);
    element("workspacePreviewNavigateForm")?.addEventListener("submit", (event) => {
      event.preventDefault();
      navigatePreview(element("workspacePreviewAddress")?.value || "");
    });
    element("workspacePreviewBackBtn")?.addEventListener("click", () => navigatePreviewHistory(-1));
    element("workspacePreviewForwardBtn")?.addEventListener("click", () => navigatePreviewHistory(1));
    element("workspacePreviewReloadBtn")?.addEventListener("click", reloadPreviewFrame);
    element("workspacePreviewHomeBtn")?.addEventListener("click", () => {
      const home = buildPreviewURL(state.workspacePreviewStatus, locationLike);
      if (home) navigatePreview(home);
    });
    element("workspacePreviewAutoStart")?.addEventListener("change", (event) => {
      state.workspacePreviewAutoStart = Boolean(event.target?.checked);
      if (state.workspacePreviewAutoStart && !previewRunning(state.workspacePreviewStatus || {}) && state.workspaceSelectedProfileId) startPreview().catch(showError);
    });
    globalThis.document?.querySelectorAll?.("[data-preview-viewport]").forEach((button) => {
      button.addEventListener("click", () => {
        setPreviewViewport(button.dataset.previewViewport);
        button.closest("details")?.removeAttribute("open");
      });
    });
    globalThis.document?.addEventListener?.("keydown", (event) => {
      if (event.key === "Escape" && state.workspaceOpen) {
        closeWorkspace();
        event.preventDefault();
      }
    });
    setAgent(state.agent);
    renderWorkspaceButtonState();
  }

  return {
    bind,
    closeWorkspace,
    loadFile,
    loadPreview,
    loadTree,
    openPreviewExternal,
    openWorkspace,
    refreshPreview,
    renderWorkspace,
    renderWorkspaceButtonState,
    saveFile,
    selectTab,
    setAgent,
    startPreview,
    stopPreview,
  };
}

function initializeWorkspaceState(state) {
  const defaults = {
    workspaceAgentId: "",
    workspaceOpen: false,
    workspaceTab: "files",
    workspacePath: "",
    workspaceEntries: [],
    workspaceTreeLoading: false,
    workspaceTreeError: "",
    workspaceFile: null,
    workspaceFilePath: "",
    workspaceFileContent: "",
    workspaceOriginalContent: "",
    workspaceFileLoading: false,
    workspaceSaving: false,
    workspaceFileStatus: t("workspace.explorer.selectFile"),
    workspaceFileStatusError: false,
    workspaceProfiles: [],
    workspaceSelectedProfileId: "",
    workspacePreviewStatus: null,
    workspacePreviewLogs: "",
    workspacePreviewLoading: false,
    workspacePreviewBusy: false,
    workspacePreviewError: "",
    workspacePreviewURL: "",
    workspacePreviewHistory: [],
    workspacePreviewHistoryIndex: -1,
    workspacePreviewViewport: "adaptive",
    workspacePreviewAutoStart: false,
    workspacePreviewTimer: null,
    workspaceTreeSeq: 0,
    workspaceFileSeq: 0,
    workspaceSaveSeq: 0,
    workspacePreviewSeq: 0,
  };
  Object.entries(defaults).forEach(([key, value]) => {
    if (state[key] === undefined) state[key] = value;
  });
}

function normalizeRelativePath(path) {
  return String(path || "").replace(/\\/g, "/").replace(/^\/+|\/+$/g, "").replace(/\/{2,}/g, "/");
}

function workspaceFileStatusText(file) {
  if (!file) return t("workspace.explorer.selectFile");
  if (file.truncated) return t("workspace.explorer.truncated");
  if (file.readOnly) return t("workspace.explorer.fileReadOnly");
  return t("workspace.explorer.saved");
}

function previewProfileId(profile) {
  return String(profile?.id || profile?.profileId || profile?.name || "");
}

function previewProfileLabel(profile) {
  const id = previewProfileId(profile);
  return String(profile?.label || profile?.title || profile?.name || id || t("workspace.explorer.unnamed"));
}

function previewRunning(status) {
  const value = String(status?.status || status?.state || "").toLowerCase();
  return status?.running === true || ["running", "started", "ready"].includes(value);
}

function previewStatusText(status, loading) {
  if (loading && !status) return t("workspace.explorer.detecting");
  if (!status || !Object.keys(status).length) return t("workspace.explorer.notStarted");
  if (previewRunning(status)) {
    const port = status.port || status.previewPort;
    return port ? t("workspace.explorer.runningPort", { port }) : t("workspace.chat.running");
  }
  return String(status.message || status.status || status.state || t("workspace.explorer.notStarted"));
}

function parsePreviewPort(value) {
  const text = String(value || "").trim();
  if (!text) return 0;
  const port = Number(text);
  return Number.isInteger(port) && port > 0 && port <= 65535 ? port : 0;
}

function previewLogLine(line) {
  if (typeof line === "string") return line;
  if (line && typeof line === "object") return String(line.text || line.message || line.line || JSON.stringify(line));
  return String(line ?? "");
}
