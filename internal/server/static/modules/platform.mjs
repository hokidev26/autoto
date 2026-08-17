/**
 * Host-platform adapters for browser vs desktop shells.
 *
 * Business modules should call these helpers instead of window.confirm /
 * window.alert. Confirm and alert use the in-app Autoto dialog when the
 * shell wires it; directory and file pickers stay native in the desktop
 * shell. Defaults remain browser-native and testable.
 *
 * Desktop picker path: POST /api/desktop/dialog/* on the same Autoto HTTP
 * origin (loopback only). External http(s) URLs use POST /api/desktop/open-url
 * for the same reason: Wails bindings require the asset origin, not Runtime.URL(),
 * and WKWebView window.open does not launch the system browser.
 */

function defaultConfirm(message) {
  if (typeof globalThis.window?.confirm === "function") {
    return Promise.resolve(Boolean(globalThis.window.confirm(String(message ?? ""))));
  }
  return Promise.resolve(false);
}

function defaultAlert(message) {
  if (typeof globalThis.window?.alert === "function") {
    globalThis.window.alert(String(message ?? ""));
  }
  return Promise.resolve();
}

async function defaultPickDirectory() {
  return { canceled: true, path: "" };
}

async function defaultPickFile() {
  return { canceled: true, path: "" };
}

let confirmImpl = defaultConfirm;
let alertImpl = defaultAlert;
let pickDirectoryImpl = defaultPickDirectory;
let pickFileImpl = defaultPickFile;
let desktopBridgeInstalled = false;

export function isDesktopShell() {
  return Boolean(globalThis.window?.AUTOTO_DESKTOP_SHELL || globalThis.window?.__TAURI__ || globalThis.window?.wails);
}

function localAPIToken() {
  return String(globalThis.window?.AUTOTO_LOCAL_TOKEN || "").trim();
}

async function desktopDialogPOST(path, payload = {}) {
  const headers = {
    "Content-Type": "application/json",
    Accept: "application/json",
  };
  const token = localAPIToken();
  if (token) headers["X-Autoto-Token"] = token;
  const response = await fetch(path, {
    method: "POST",
    credentials: "same-origin",
    headers,
    body: JSON.stringify(payload),
  });
  if (!response.ok) {
    const text = await response.text().catch(() => "");
    throw new Error(text || `desktop dialog HTTP ${response.status}`);
  }
  return response.json();
}

async function desktopPickDirectory(options = {}) {
  const data = await desktopDialogPOST("/api/desktop/dialog/open-directory", {
    title: options.title ? String(options.title) : undefined,
    defaultPath: options.defaultPath ? String(options.defaultPath) : undefined,
  });
  if (data?.canceled) return { canceled: true, path: "" };
  return { canceled: false, path: String(data?.path || "") };
}

async function desktopPickFile(options = {}) {
  const data = await desktopDialogPOST("/api/desktop/dialog/open-file", {
    title: options.title ? String(options.title) : undefined,
    defaultPath: options.defaultPath ? String(options.defaultPath) : undefined,
    filters: Array.isArray(options.filters) ? options.filters : undefined,
  });
  if (data?.canceled) return { canceled: true, path: "" };
  return { canceled: false, path: String(data?.path || "") };
}

/** Wire native file pickers when running inside the Autoto desktop shell. */
export function installDesktopShellDialogs() {
  if (desktopBridgeInstalled || !isDesktopShell()) return false;
  setPlatformDialogs({
    pickDirectory: desktopPickDirectory,
    pickFile: desktopPickFile,
  });
  desktopBridgeInstalled = true;
  return true;
}

export function setPlatformDialogs({ confirm, alert, pickDirectory, pickFile } = {}) {
  if (typeof confirm === "function") confirmImpl = confirm;
  if (typeof alert === "function") alertImpl = alert;
  if (typeof pickDirectory === "function") pickDirectoryImpl = pickDirectory;
  if (typeof pickFile === "function") pickFileImpl = pickFile;
}

export function resetPlatformDialogs() {
  confirmImpl = defaultConfirm;
  alertImpl = defaultAlert;
  pickDirectoryImpl = defaultPickDirectory;
  pickFileImpl = defaultPickFile;
  desktopBridgeInstalled = false;
}

export async function confirm(message, options = {}) {
  return confirmImpl(message, options);
}

export async function alert(message, options = {}) {
  return alertImpl(message, options);
}

/** @returns {Promise<{canceled:boolean, path:string}>} */
export async function pickDirectory(options = {}) {
  if (!desktopBridgeInstalled && isDesktopShell()) {
    installDesktopShellDialogs();
  }
  return pickDirectoryImpl(options);
}

/** @returns {Promise<{canceled:boolean, path:string}>} */
export async function pickFile(options = {}) {
  if (!desktopBridgeInstalled && isDesktopShell()) {
    installDesktopShellDialogs();
  }
  return pickFileImpl(options);
}

/** Open an http(s) URL in the system browser. Desktop shells cannot rely on window.open. */
export async function openExternal(url) {
  const target = String(url || "").trim();
  if (!target) return false;
  if (isDesktopShell()) {
    try {
      await desktopDialogPOST("/api/desktop/open-url", { url: target });
      return true;
    } catch {
      return false;
    }
  }
  try {
    const opened = globalThis.window?.open?.(target, "_blank", "noopener,noreferrer");
    if (opened) opened.opener = null;
    return Boolean(opened);
  } catch {
    return false;
  }
}

/** Convenience for controllers that inject confirmAction. */
export function createConfirmAction(confirmFn = confirm) {
  return async (message, options) => confirmFn(message, options);
}

// Auto-install on module load when the shell marker is already present.
if (typeof globalThis.window !== "undefined" && isDesktopShell()) {
  installDesktopShellDialogs();
}
