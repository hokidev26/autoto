import { $, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { settingsItems } from "./settings-data.mjs";
import { api } from "./runtime.mjs";

export function accountIsGuest(account) {
  return String(account?.role || "") === "guest";
}

export function accountIsAdmin(account) {
  return String(account?.role || "") === "admin";
}

export function visibleSettingsItems(account, hasUsers) {
  return settingsItems.filter((item) => {
    if (accountIsGuest(account)) return item.key === "profile";
    if (item.key === "users") return accountIsAdmin(account) || !hasUsers;
    return true;
  });
}

export function defaultSettingsPanelKey(account, hasUsers = true) {
  if (accountIsGuest(account)) return "profile";
  if (!hasUsers) return "users";
  return "providers";
}

export function createAccountSessionController({
  state,
  request = api,
  showToast,
  onSignedIn,
  onSignedOut,
} = {}) {
  function overlay() {
    return $("accountSessionOverlay");
  }

  function applyGuestShell() {
    const guest = accountIsGuest(state.account);
    document.body?.classList.toggle("guest-observe", guest);
    const banner = $("guestObserveBanner");
    banner?.classList.toggle("hidden", !guest);
    if (banner && guest) banner.textContent = t("accountSession.guestBanner");
  }

  function setMode(mode) {
    const root = overlay();
    if (!root) return;
    root.dataset.mode = mode;
    const useKey = mode === "key";
    $("accountSessionPasswordFields")?.classList.toggle("hidden", useKey);
    $("accountSessionKeyFields")?.classList.toggle("hidden", !useKey);
    $("accountSessionUsePasswordBtn")?.classList.toggle("active", !useKey);
    $("accountSessionUseKeyBtn")?.classList.toggle("active", useKey);
  }

  function showOverlay({ createAdministrator = false } = {}) {
    const root = overlay();
    if (!root) return;
    root.classList.remove("hidden");
    root.setAttribute("aria-hidden", "false");
    $("accountSessionTitle").textContent = createAdministrator ? t("accountSession.createTitle") : t("accountSession.title");
    $("accountSessionSubtitle").textContent = createAdministrator ? t("accountSession.createSubtitle") : t("accountSession.subtitle");
    $("accountSessionSubmitBtn").textContent = createAdministrator ? t("accountSession.createSubmit") : t("accountSession.submit");
    $("accountSessionKeyToggle")?.classList.toggle("hidden", createAdministrator);
    setMode(createAdministrator ? "password" : (overlay()?.dataset.mode || "password"));
    $("accountSessionHandle")?.focus();
  }

  function hideOverlay() {
    const root = overlay();
    if (!root) return;
    root.classList.add("hidden");
    root.setAttribute("aria-hidden", "true");
    const error = $("accountSessionError");
    if (error) {
      error.hidden = true;
      error.textContent = "";
    }
  }

  function showError(message) {
    const error = $("accountSessionError");
    if (!error) {
      showToast?.(message, "error", { force: true });
      return;
    }
    error.hidden = false;
    error.textContent = message;
  }

  async function loadStatus() {
    const status = await request("/api/auth/status");
    state.authStatus = {
      hasUsers: Boolean(status?.hasUsers),
      registrationOpen: Boolean(status?.registrationOpen),
    };
    return state.authStatus;
  }

  async function loadMe() {
    try {
      state.account = await request("/api/auth/me");
      return state.account;
    } catch (error) {
      if (error?.status === 401) {
        state.account = null;
        return null;
      }
      throw error;
    }
  }

  async function ensureSession() {
    const status = await loadStatus();
    if (!status.hasUsers) {
      state.account = null;
      applyGuestShell();
      hideOverlay();
      return { ready: true, needsAccount: false };
    }
    const account = await loadMe();
    applyGuestShell();
    if (account) {
      hideOverlay();
      return { ready: true, needsAccount: false };
    }
    showOverlay({ createAdministrator: false });
    return { ready: false, needsAccount: true };
  }

  async function submit(event) {
    event?.preventDefault?.();
    const createAdministrator = Boolean(state.authStatus && !state.authStatus.hasUsers);
    const usingKey = overlay()?.dataset.mode === "key" && !createAdministrator;
    const handle = $("accountSessionHandle")?.value?.trim() || "";
    const password = $("accountSessionPassword")?.value || "";
    const accessKey = $("accountSessionAccessKey")?.value?.trim() || "";
    if (usingKey) {
      if (!accessKey) {
        showError(t("accountSession.keyRequired"));
        return;
      }
    } else if (!handle || !password) {
      showError(t("accountSession.required"));
      return;
    }
    const button = $("accountSessionSubmitBtn");
    setButtonBusy(button, true, t("common.loading"));
    try {
      if (createAdministrator) {
        await request("/api/auth/register", { method: "POST", body: JSON.stringify({ handle, password }) });
      } else if (usingKey) {
        await request("/api/auth/login", { method: "POST", body: JSON.stringify({ accessKey }) });
      } else {
        await request("/api/auth/login", { method: "POST", body: JSON.stringify({ handle, password }) });
      }
      hideOverlay();
      await onSignedIn?.();
    } catch (error) {
      showError(error?.message || t("accountSession.error"));
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function signOut() {
    try {
      await request("/api/auth/logout", { method: "POST", body: "{}" });
    } catch {}
    state.account = null;
    applyGuestShell();
    await onSignedOut?.();
  }

  function bind() {
    $("accountSessionForm")?.addEventListener("submit", (event) => submit(event).catch((error) => showError(error?.message || t("accountSession.error"))));
    $("accountSessionUsePasswordBtn")?.addEventListener("click", () => setMode("password"));
    $("accountSessionUseKeyBtn")?.addEventListener("click", () => setMode("key"));
  }

  function profileSessionHTML() {
    if (!state.account?.handle) return "";
    const roleKey = accountIsAdmin(state.account) ? "accountSession.roleAdmin" : accountIsGuest(state.account) ? "accountSession.roleGuest" : "accountSession.roleUser";
    return `
      <section class="settings-card settings-card-content profile-session-card">
        <div class="settings-provider-title settings-card-title">${escapeHtml(t("accountSession.signedInAs", { handle: state.account.handle }))}</div>
        <p class="settings-card-description">${escapeHtml(t(roleKey))}</p>
        <div class="settings-action-row">
          <button id="profileSignOutBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("accountSession.signOut"))}</button>
        </div>
      </section>
    `;
  }

  return {
    applyGuestShell,
    bind,
    ensureSession,
    hideOverlay,
    loadMe,
    loadStatus,
    profileSessionHTML,
    showOverlay,
    signOut,
    submit,
  };
}
