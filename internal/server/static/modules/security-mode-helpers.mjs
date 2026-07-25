import { $ } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { appMainT as am } from "./messages-app-main-extra.mjs";
import { fullAccessAllowed, remoteAccessContext, terminalAccessAllowed } from "./remote-access-capabilities.mjs";
import { api } from "./runtime.mjs";

// Derives the current connection/security summary from runtime + remote
// access state, keeps the security badge and terminal-availability UI in
// sync with it, and handles the remote-access logout action.
export function createSecurityModeHelpers({
  state,
  showToast,
  connectionMobileLabel,
  updatePermissionModeDisplay,
  projectOperationContextActive,
  updateWorkspaceMetaPills,
  closeSidebarSettingsMenu,
  enforceTerminalAccessPolicy,
  renderTerminalButtonState,
  updateRuntimeStatusButton,
}) {
  function currentSecuritySummary() {
    const runtimeSecurity = state.runtimeSummary?.security || null;
    const access = state.remoteAccess || null;
    if (!runtimeSecurity && !access) return null;
    return {
      ...(runtimeSecurity || {}),
      currentRequestRemote: access?.session?.remote ?? runtimeSecurity?.currentRequestRemote,
      remoteAccessRequired: access?.session?.remote ?? runtimeSecurity?.remoteAccessRequired,
      bypassPermissionsAllowed: fullAccessAllowed(state),
      remoteTerminalAllowed: terminalAccessAllowed(state),
      maxPermissionMode: access?.capabilities?.maxPermissionMode || runtimeSecurity?.maxPermissionMode,
      accessPasswordConfigured: access?.credential?.configured ?? runtimeSecurity?.accessPasswordConfigured,
      capabilities: access?.capabilities || runtimeSecurity?.capabilities,
    };
  }

  function remoteSecurityHardeningActive() {
    const security = currentSecuritySummary();
    const remoteSession = Boolean(state.remoteAccess?.session?.remote);
    return Boolean(state.remoteAccessFailClosed || remoteSession || security?.remoteAccessRequired || security?.exposed || security?.currentRequestRemote || security?.bypassPermissionsAllowed === false);
  }

  function connectionModeSummary() {
    const remote = remoteAccessContext(state);
    if (!remote) {
      return { remote: false, restricted: false, label: am("localConnection"), title: am("localConnectionTitle"), tone: "ok" };
    }
    const restricted = !fullAccessAllowed(state);
    const mode = restricted ? am("tunnelRestrictedConnection") : am("tunnelFullConnection");
    return {
      remote: true,
      restricted,
      label: mode,
      title: am("tunnelConnectionTitle", { mode }),
      tone: restricted ? "warn" : "ok",
    };
  }

  function bypassDisabledBySecurity() {
    return !fullAccessAllowed(state);
  }

  function effectivePermissionForDisplay(value) {
    if (value === "bypassPermissions" && bypassDisabledBySecurity()) return "acceptEdits";
    return value;
  }

  function enforcePermissionSelectCap() {
    const select = $("permissionMode");
    if (!select) return;
    const disabled = bypassDisabledBySecurity();
    const option = Array.from(select.options).find((item) => item.value === "bypassPermissions");
    if (option) {
      option.disabled = disabled;
      option.textContent = disabled ? `${t("chat.permission.allowAll")} (${t("workspace.terminal.remoteDisabled")})` : t("chat.permission.allowAll");
    }
    if (disabled && select.value === "bypassPermissions") {
      select.value = "acceptEdits";
    }
    updatePermissionModeDisplay();
  }

  async function logoutRemoteAccess() {
    closeSidebarSettingsMenu();
    if (!remoteSecurityHardeningActive()) {
      showToast(t("workspace.main.localMvpNoLogout"), "info");
      return;
    }
    await api("/auth/remote-access/logout", { method: "POST" });
    showToast(t("workspace.main.remoteLoggedOut"), "success", { force: true });
    location.reload();
  }

  function updateSecurityModeUI() {
    const security = currentSecuritySummary();
    const terminalLocked = !terminalAccessAllowed(state);
    if (terminalLocked) enforceTerminalAccessPolicy();
    const connection = connectionModeSummary();
    const badge = $("securityModeBadge");
    if (badge) {
      badge.textContent = connection.label;
      badge.title = [connection.title, security?.message].filter(Boolean).join(" · ");
      (badge.dataset ||= {}).mobileLabel = connectionMobileLabel(connection);
      badge.classList.toggle("warn", connection.tone === "warn");
      badge.classList.toggle("ok", connection.tone === "ok");
      badge.classList.toggle("tunnel", connection.remote);
      badge.dataset.connectionMode = connection.remote ? (connection.restricted ? "tunnel-restricted" : "tunnel-full") : "local";
    }
    const terminalUnavailable = terminalLocked || !projectOperationContextActive();
    [$("toggleTerminalBtn"), $("workbenchTerminalBtn"), $("expandTerminalBtn"), $("reconnectTerminalBtn")].forEach((button) => {
      if (!button) return;
      if (!button.dataset.defaultTitle) button.dataset.defaultTitle = button.title || "";
      button.disabled = terminalUnavailable;
      button.title = terminalLocked ? am("remoteTerminalDisabledTitle") : button.dataset.defaultTitle;
    });
    enforcePermissionSelectCap();
    updateWorkspaceMetaPills();
    renderTerminalButtonState();
    updateRuntimeStatusButton();
  }

  return {
    currentSecuritySummary,
    remoteSecurityHardeningActive,
    connectionModeSummary,
    bypassDisabledBySecurity,
    effectivePermissionForDisplay,
    enforcePermissionSelectCap,
    logoutRemoteAccess,
    updateSecurityModeUI,
  };
}
