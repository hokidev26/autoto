import { objectValue } from "./value-coercion.mjs";

function restrictedCapabilities() {
  return {
    maxPermissionMode: "acceptEdits",
    terminalAllowed: false,
    filesystemScope: "restricted",
    nativePickerAllowed: false,
    securityAdminAllowed: false,
  };
}

export function remoteAccessCapabilities(state = {}) {
  if (state?.remoteAccessFailClosed) return restrictedCapabilities();
  const direct = objectValue(state?.remoteAccess?.capabilities);
  if (Object.keys(direct).length > 0) return direct;
  return objectValue(state?.runtimeSummary?.security?.capabilities);
}

export function remoteAccessSession(state = {}) {
  return objectValue(state?.remoteAccess?.session);
}

export function isFullPermissionMode(mode) {
  const normalized = String(mode || "").trim();
  return normalized === "bypassPermissions" || normalized === "full";
}

export function loopbackLocation(locationLike = globalThis.location) {
  const hostname = String(locationLike?.hostname || "").trim().toLowerCase();
  return hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1" || hostname === "[::1]";
}

export function remoteAccessContext(state = {}, locationLike = globalThis.location) {
  if (state?.remoteAccessFailClosed) return true;
  const locationKnown = String(locationLike?.hostname || "").trim() !== "";
  const locationIsRemote = locationKnown && !loopbackLocation(locationLike);
  const session = remoteAccessSession(state);
  if (session.remote === true) return true;
  if (session.remote === false && !locationIsRemote) return false;
  const security = objectValue(state?.runtimeSummary?.security);
  if (security.currentRequestRemote === true || security.remoteAccessRequired === true) return true;
  if ((security.currentRequestRemote === false || security.remoteAccessRequired === false) && !locationIsRemote) return false;
  // A stale local-shaped payload must never override a visibly non-loopback page.
  return locationIsRemote;
}

export function applyRemoteAccessFailClosed(state = {}, { status } = {}) {
  const current = objectValue(state.remoteAccess);
  const session = objectValue(current.session);
  const authenticated = Number(status) === 401 ? false : Boolean(session.authenticated);
  const capabilities = restrictedCapabilities();
  state.remoteAccessFailClosed = true;
  state.remoteAccess = {
    ...current,
    credential: objectValue(current.credential),
    policy: objectValue(current.policy),
    session: {
      ...session,
      remote: true,
      authenticated,
      mode: "restricted",
      expiresAt: authenticated ? String(session.expiresAt || "") : "",
    },
    capabilities,
  };

  const runtime = objectValue(state.runtimeSummary);
  const security = objectValue(runtime.security);
  if (Object.keys(runtime).length > 0 || Object.keys(security).length > 0) {
    state.runtimeSummary = {
      ...runtime,
      security: {
        ...security,
        currentRequestRemote: true,
        remoteAccessRequired: true,
        bypassPermissionsAllowed: false,
        remoteTerminalAllowed: false,
        maxPermissionMode: "acceptEdits",
        mode: authenticated ? "remote-restricted" : "remote-unauthenticated",
        capabilities,
      },
    };
  }
  return state.remoteAccess;
}

export function fullAccessAllowed(state = {}, locationLike = globalThis.location) {
  const capabilities = remoteAccessCapabilities(state);
  if (typeof capabilities.maxPermissionMode === "string") return isFullPermissionMode(capabilities.maxPermissionMode);
  if (remoteAccessContext(state, locationLike)) return false;

  const security = objectValue(state?.runtimeSummary?.security);
  if (typeof security.bypassPermissionsAllowed === "boolean") return security.bypassPermissionsAllowed;
  return loopbackLocation(locationLike);
}

export function terminalAccessAllowed(state = {}, locationLike = globalThis.location) {
  const capabilities = remoteAccessCapabilities(state);
  if (typeof capabilities.terminalAllowed === "boolean") return capabilities.terminalAllowed;
  if (remoteAccessContext(state, locationLike)) return false;

  const security = objectValue(state?.runtimeSummary?.security);
  if (typeof security.remoteTerminalAllowed === "boolean") return security.remoteTerminalAllowed;
  if (security.remoteAccessRequired === false) return true;
  return loopbackLocation(locationLike);
}

export function filesystemScope(state = {}, locationLike = globalThis.location) {
  const scope = String(remoteAccessCapabilities(state).filesystemScope || "").trim().toLowerCase();
  if (scope === "project" || scope === "restricted") return "restricted";
  if (scope === "host" || scope === "full") return "full";
  if (remoteAccessContext(state, locationLike)) return "restricted";

  const security = objectValue(state?.runtimeSummary?.security);
  if (typeof security.bypassPermissionsAllowed === "boolean") return security.bypassPermissionsAllowed ? "full" : "restricted";
  if (security.remoteAccessRequired === false) return "full";
  return loopbackLocation(locationLike) ? "full" : "restricted";
}

export function directoryBrowserScope(state = {}, locationLike = globalThis.location) {
  return filesystemScope(state, locationLike) === "restricted" ? "default-projects" : "host";
}

export function nativeDirectoryPickerAllowedFor(state = {}, locationLike = globalThis.location, platformLike = globalThis.navigator?.userAgentData?.platform || globalThis.navigator?.platform || "") {
  const capabilities = remoteAccessCapabilities(state);
  const security = objectValue(state?.runtimeSummary?.security);
  const capabilityAllowed = typeof capabilities.nativePickerAllowed === "boolean"
    ? capabilities.nativePickerAllowed
    : Boolean(security.nativePickerAllowed ?? security.nativeDirectoryPickerAllowed);
  if (!capabilityAllowed || state?.remoteAccessFailClosed || !loopbackLocation(locationLike)) return false;
  // Desktop shell (Wails) provides cross-platform native pickers; browser path
  // remains macOS-only via AppleScript fallback on the server.
  if (globalThis.window?.AUTOTO_DESKTOP_SHELL) return true;
  return /mac/i.test(String(platformLike || ""));
}
