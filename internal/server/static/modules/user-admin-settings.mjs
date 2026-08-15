import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { api } from "./runtime.mjs";
import { accountIsAdmin, accountIsGuest } from "./account-session.mjs";

export function createUserAdminSettingsController({
  state,
  request = api,
  copyText,
  showToast,
  showError,
  confirmAction,
  onChange,
  onCreateAdministrator,
} = {}) {
  let issuedKey = "";

  function roleLabel(role) {
    if (role === "admin") return t("users.roleAdmin");
    if (role === "guest") return t("users.roleGuest");
    return t("users.roleUser");
  }

  function projectOptions(selectedIds = []) {
    const selected = new Set((selectedIds || []).map(String));
    const projects = Array.isArray(state.projects) ? state.projects : [];
    if (!projects.length) return `<p class="settings-card-description">${escapeHtml(t("users.noProjects"))}</p>`;
    return `<div class="user-admin-projects">${projects.map((project) => `
      <label class="user-admin-project">
        <input type="checkbox" data-guest-project-id="${escapeAttr(project.id)}" ${selected.has(project.id) ? "checked" : ""} />
        <span>${escapeHtml(project.name || project.id)}</span>
      </label>
    `).join("")}</div>`;
  }

  function selectedProjectIds(root = document) {
    return [...root.querySelectorAll("[data-guest-project-id]:checked")].map((node) => node.dataset.guestProjectId);
  }

  function renderAccount(account) {
    const keys = Array.isArray(account.keys) ? account.keys : [];
    const memberships = Array.isArray(account.projectIds) ? account.projectIds : [];
    return `
      <article class="settings-card settings-card-content user-admin-account" data-user-id="${escapeAttr(account.id)}">
        <div class="settings-provider-title settings-card-title">${escapeHtml(account.handle || account.username || account.id)}</div>
        <p class="settings-card-description">${escapeHtml(roleLabel(account.role))} · ${escapeHtml(account.passwordSet ? t("users.passwordSet") : t("users.passwordUnset"))} · ${escapeHtml(t("users.keyCount", { count: account.keyCount || keys.length || 0 }))}</p>
        ${accountIsGuest(account) ? `
          <div class="user-admin-memberships">
            <div class="settings-form-label">${escapeHtml(t("users.projects"))}</div>
            ${projectOptions(memberships)}
            <div class="settings-action-row">
              <button class="settings-action-btn subtle" type="button" data-save-memberships>${escapeHtml(t("users.saveMemberships"))}</button>
              <button class="settings-action-btn subtle" type="button" data-issue-key>${escapeHtml(t("users.issueAnotherKey"))}</button>
            </div>
            ${keys.length ? `<ul class="user-admin-keys">${keys.map((key) => `
              <li>
                <span>${escapeHtml(key.label || key.id)}</span>
                <button class="settings-action-btn subtle" type="button" data-revoke-key="${escapeAttr(key.id)}">${escapeHtml(t("users.revokeKey"))}</button>
              </li>
            `).join("")}</ul>` : ""}
          </div>
        ` : ""}
        ${accountIsAdmin(state.account) && account.id !== state.account?.id ? `
          <div class="settings-action-row">
            <button class="settings-action-btn subtle" type="button" data-delete-user>${escapeHtml(t("users.deleteUser"))}</button>
          </div>
        ` : ""}
      </article>
    `;
  }

  function render() {
    const hasUsers = Boolean(state.authStatus?.hasUsers);
    const accounts = Array.isArray(state.userAccounts) ? state.userAccounts : [];
    const issued = issuedKey ? `
      <section class="settings-card settings-card-content user-admin-issued-key">
        <div class="settings-provider-title settings-card-title">${escapeHtml(t("users.createdKey"))}</div>
        <code class="user-admin-key-value">${escapeHtml(issuedKey)}</code>
        <button id="copyIssuedAccessKeyBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("users.copyKey"))}</button>
      </section>
    ` : "";
    if (!hasUsers) {
      return `
        <div class="settings-live-page">
          <section class="settings-hero-card settings-page-section settings-card">
            <div class="settings-hero-kicker">${escapeHtml(t("users.kicker"))}</div>
            <div class="settings-hero-title settings-card-title">${escapeHtml(t("users.noUsersTitle"))}</div>
            <p class="settings-card-description">${escapeHtml(t("users.bootstrapHint"))}</p>
            <div class="settings-action-row">
              <button id="createAdministratorBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("users.createAdmin"))}</button>
            </div>
          </section>
        </div>
      `;
    }
    return `
      <div class="settings-live-page user-admin-page">
        <section class="settings-hero-card settings-page-section settings-card">
          <div class="settings-hero-kicker">${escapeHtml(t("users.kicker"))}</div>
          <div class="settings-hero-title settings-card-title">${escapeHtml(t("users.hasUsersTitle"))}</div>
          <p class="settings-card-description">${escapeHtml(t("users.description"))}</p>
          <div class="settings-action-row">
            <button id="refreshUserAccountsBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("users.refresh"))}</button>
          </div>
        </section>
        ${issued}
        <section class="settings-card settings-card-content">
          <div class="settings-provider-title settings-card-title">${escapeHtml(t("users.createGuest"))}</div>
          <form id="createGuestForm" class="settings-profile-form">
            <label class="settings-form-field">${escapeHtml(t("users.guestHandle"))}
              <input id="guestHandleInput" class="settings-field" required />
            </label>
            <label class="settings-form-field">${escapeHtml(t("users.guestPassword"))}
              <input id="guestPasswordInput" class="settings-field" type="password" />
            </label>
            <label class="settings-form-field">${escapeHtml(t("users.keyLabel"))}
              <input id="guestKeyLabelInput" class="settings-field" />
            </label>
            <label class="user-admin-project">
              <input id="guestIssueKeyInput" type="checkbox" checked />
              <span>${escapeHtml(t("users.issueKey"))}</span>
            </label>
            <div class="settings-form-label">${escapeHtml(t("users.projects"))}</div>
            ${projectOptions([])}
            <button class="settings-action-btn primary" type="submit">${escapeHtml(t("users.createGuest"))}</button>
          </form>
        </section>
        <section class="settings-page-section">
          <div class="settings-provider-title">${escapeHtml(t("users.accounts"))}</div>
          ${accounts.length ? accounts.map(renderAccount).join("") : `<p class="settings-card-description">${escapeHtml(t("users.empty"))}</p>`}
        </section>
      </div>
    `;
  }

  async function load() {
    if (!accountIsAdmin(state.account)) {
      state.userAccounts = [];
      return;
    }
    state.userAccounts = await request("/api/users/accounts");
  }

  async function reload() {
    await load();
    onChange?.();
  }

  async function refresh() {
    await reload();
    showToast?.(t("users.refresh"), "success");
  }

  async function createGuest(event) {
    event.preventDefault();
    const handle = $("guestHandleInput")?.value?.trim() || "";
    const password = $("guestPasswordInput")?.value || "";
    const keyLabel = $("guestKeyLabelInput")?.value?.trim() || "";
    const issueKey = Boolean($("guestIssueKeyInput")?.checked);
    const projectIds = selectedProjectIds($("createGuestForm"));
    const button = event.submitter || event.currentTarget.querySelector("button[type=submit]");
    setButtonBusy(button, true, t("common.loading"));
    try {
      const created = await request("/api/users/guests", {
        method: "POST",
        body: JSON.stringify({ handle, password, projectIds, keyLabel, issueKey }),
      });
      issuedKey = created?.accessKey || "";
      await reload();
      if (issuedKey) copyText?.(issuedKey);
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function saveMemberships(userId, root) {
    await request(`/api/users/${encodeURIComponent(userId)}/memberships`, {
      method: "PUT",
      body: JSON.stringify({ projectIds: selectedProjectIds(root) }),
    });
    await reload();
  }

  async function issueKey(userId) {
    const created = await request(`/api/users/${encodeURIComponent(userId)}/access-keys`, {
      method: "POST",
      body: JSON.stringify({ label: "" }),
    });
    issuedKey = created?.accessKey || "";
    if (issuedKey) copyText?.(issuedKey);
    await reload();
  }

  async function revokeKey(userId, keyId) {
    await request(`/api/users/${encodeURIComponent(userId)}/access-keys/${encodeURIComponent(keyId)}`, { method: "DELETE" });
    await reload();
  }

  async function deleteUser(account) {
    const ok = await confirmAction?.(t("users.deleteConfirm", { handle: account.handle || account.id }));
    if (!ok) return;
    await request(`/api/users/${encodeURIComponent(account.id)}`, { method: "DELETE" });
    await reload();
  }

  function bind() {
    $("refreshUserAccountsBtn")?.addEventListener("click", () => refresh().catch(showError));
    $("createAdministratorBtn")?.addEventListener("click", () => onCreateAdministrator?.());
    $("createGuestForm")?.addEventListener("submit", (event) => createGuest(event).catch(showError));
    $("copyIssuedAccessKeyBtn")?.addEventListener("click", () => issuedKey && copyText?.(issuedKey));
    document.querySelectorAll(".user-admin-account").forEach((root) => {
      const userId = root.dataset.userId;
      const account = (state.userAccounts || []).find((item) => item.id === userId);
      root.querySelector("[data-save-memberships]")?.addEventListener("click", () => saveMemberships(userId, root).catch(showError));
      root.querySelector("[data-issue-key]")?.addEventListener("click", () => issueKey(userId).catch(showError));
      root.querySelector("[data-delete-user]")?.addEventListener("click", () => deleteUser(account).catch(showError));
      root.querySelectorAll("[data-revoke-key]").forEach((button) => {
        button.addEventListener("click", () => revokeKey(userId, button.dataset.revokeKey).catch(showError));
      });
    });
  }

  return { render, bind, load };
}
