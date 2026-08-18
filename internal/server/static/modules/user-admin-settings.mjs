import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs";
import { api } from "./runtime.mjs";
import { accountIsAdmin, accountIsCollaborator, accountIsGuest, accountIsOperator } from "./account-session.mjs";

export function createUserAdminSettingsController({
  state,
  request = api,
  copyText,
  showError,
  showToast,
  confirmAction,
  onChange,
  onCreateAdministrator,
} = {}) {
  let issuedKey = "";
  let pickerEvents = null;
  let floatingMenu = null;
  let createPanel = "";
  let shouldFocusCreate = false;
  const openAccountIds = new Set();

  function roleLabel(role) {
    if (role === "admin") return t("users.roleAdmin");
    if (role === "guest") return t("users.roleGuest");
    if (role === "collaborator") return t("users.roleCollaborator");
    return t("users.roleOperator");
  }

  function roleBadgeClass(role) {
    if (role === "admin") return "success";
    if (role === "guest") return "warning";
    return "accent";
  }

  function handleInitials(handle) {
    const chars = [...String(handle || "").trim()];
    if (!chars.length) return "?";
    return chars.slice(0, 2).join("").toUpperCase();
  }

  function formatStamp(value) {
    const raw = String(value || "").trim();
    if (!raw) return "";
    const date = new Date(raw);
    if (Number.isNaN(date.getTime())) return raw;
    try {
      return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" }).format(date);
    } catch {
      return raw;
    }
  }

  function countByRole(accounts, role) {
    return accounts.filter((account) => String(account?.role || "") === role).length;
  }

  function projectSummary(selectedIds = []) {
    const count = selectedIds.length;
    if (!count) return t("users.projectsNoneSelected");
    return t("users.projectsSelectedCount", { count });
  }

  function projectPicker(selectedIds = [], label = t("users.projects")) {
    const selected = new Set((selectedIds || []).map(String));
    const projects = Array.isArray(state.projects) ? state.projects : [];
    if (!projects.length) return `<p class="settings-card-description">${escapeHtml(t("users.noProjects"))}</p>`;
    const selectedList = [...selected];
    return `
      <div class="user-admin-project-picker">
        <button type="button" class="user-admin-project-trigger" data-project-picker-toggle aria-expanded="false" aria-haspopup="listbox">
          <span class="user-admin-project-trigger-copy">
            <strong>${escapeHtml(label)}</strong>
            <small data-project-picker-summary>${escapeHtml(projectSummary(selectedList))}</small>
          </span>
          <span class="composer-select-chevron" aria-hidden="true">▾</span>
        </button>
        <div class="user-admin-project-checks" hidden>
          ${projects.map((project) => `
            <label>
              <input type="checkbox" data-guest-project-id="${escapeAttr(project.id)}" data-guest-project-name="${escapeAttr(project.name || project.id)}" ${selected.has(project.id) ? "checked" : ""} />
              <span>${escapeHtml(project.name || project.id)}</span>
            </label>
          `).join("")}
        </div>
      </div>
    `;
  }

  function selectedProjectIds(root = document) {
    return [...root.querySelectorAll("[data-guest-project-id]:checked")].map((node) => node.dataset.guestProjectId);
  }

  function renderKeys(keys) {
    if (!keys.length) return `<p class="settings-card-description">${escapeHtml(t("users.noKeys"))}</p>`;
    return `<ul class="user-admin-keys">${keys.map((key) => {
      const used = formatStamp(key.lastUsedAt);
      const meta = used ? t("users.lastUsed", { time: used }) : t("users.neverUsed");
      return `
        <li class="user-admin-key-row">
          <div class="user-admin-key-copy">
            <strong>${escapeHtml(key.label || t("users.unnamedKey"))}</strong>
            <small>${escapeHtml(meta)}</small>
          </div>
          <button class="settings-action-btn subtle" type="button" data-revoke-key="${escapeAttr(key.id)}">${escapeHtml(t("users.revokeKey"))}</button>
        </li>
      `;
    }).join("")}</ul>`;
  }

  function accountHint(account, self) {
    if (self) return t("users.selfHint");
    if (accountIsGuest(account)) return t("users.guestCardHint");
    if (accountIsAdmin(account)) return t("users.adminCardHint");
    if (accountIsCollaborator(account)) return t("users.collaboratorHint");
    return t("users.operatorHint");
  }

  function roleSelect(account) {
    if (accountIsGuest(account) || account.id === state.account?.id) return "";
    const role = String(account.role || "user");
    return `
      <label class="settings-form-field">${escapeHtml(t("users.changeRole"))}
        <select class="settings-field" data-user-role>
          <option value="admin"${role === "admin" ? " selected" : ""}>${escapeHtml(t("users.roleAdmin"))}</option>
          <option value="user"${role === "user" ? " selected" : ""}>${escapeHtml(t("users.roleOperator"))}</option>
          <option value="collaborator"${role === "collaborator" ? " selected" : ""}>${escapeHtml(t("users.roleCollaborator"))}</option>
        </select>
      </label>
    `;
  }

  function renderAccount(account) {
    const keys = Array.isArray(account.keys) ? account.keys : [];
    const memberships = Array.isArray(account.projectIds) ? account.projectIds : [];
    const handle = account.handle || account.username || account.id;
    const self = account.id === state.account?.id;
    const guest = accountIsGuest(account);
    const membershipsEnabled = guest || accountIsOperator(account) || accountIsCollaborator(account);
    const canDelete = accountIsAdmin(state.account) && !self;
    const keyCount = account.keyCount || keys.length || 0;
    const row = `
      <div class="user-admin-account-row">
        <div class="user-admin-identity">
          <span class="user-admin-avatar" aria-hidden="true">${escapeHtml(handleInitials(handle))}</span>
          <div class="user-admin-identity-copy">
            <div class="user-admin-account-name">${escapeHtml(handle)}</div>
            <p class="settings-card-description" data-settings-help-copy>${escapeHtml(accountHint(account, self))}</p>
            <div class="user-admin-meta">
              <span class="user-admin-chip">${escapeHtml(account.passwordSet ? t("users.passwordSet") : t("users.passwordUnset"))}</span>
              ${guest ? `<span class="user-admin-chip">${escapeHtml(t("users.keyCount", { count: keyCount }))}</span>` : ""}
              ${membershipsEnabled ? `<span class="user-admin-chip">${escapeHtml(t("users.projectCount", { count: memberships.length }))}</span>` : ""}
            </div>
          </div>
        </div>
        <div class="user-admin-account-aside">
          <div class="user-admin-badges">
            <span class="settings-status-pill settings-badge ${roleBadgeClass(account.role)}">${escapeHtml(roleLabel(account.role))}</span>
            ${self ? `<span class="settings-badge accent">${escapeHtml(t("users.you"))}</span>` : ""}
          </div>
          ${membershipsEnabled ? `<span class="user-admin-account-chevron" aria-hidden="true"></span>` : ""}
          ${!membershipsEnabled && canDelete ? `<button class="settings-action-btn danger" type="button" data-delete-user>${escapeHtml(t("users.deleteUser"))}</button>` : ""}
        </div>
      </div>
    `;
    const roleControls = roleSelect(account);
    const body = membershipsEnabled ? `
      <div class="user-admin-account-body">
        <div class="user-admin-memberships">
          ${projectPicker(memberships, guest ? t("users.projects") : t("users.collaboratorProjects"))}
          ${guest ? `<div class="user-admin-section-label">${escapeHtml(t("users.keys"))}</div>${renderKeys(keys)}` : ""}
          ${roleControls}
        </div>
        <div class="settings-action-row settings-card-footer">
          <button class="settings-action-btn primary" type="button" data-save-memberships>${escapeHtml(t("users.saveMemberships"))}</button>
          ${roleControls ? `<button class="settings-action-btn subtle" type="button" data-save-role>${escapeHtml(t("users.saveRole"))}</button>` : ""}
          ${guest ? `<button class="settings-action-btn subtle" type="button" data-issue-key>${escapeHtml(t("users.issueAnotherKey"))}</button>` : ""}
          ${canDelete ? `<button class="settings-action-btn danger" type="button" data-delete-user>${escapeHtml(t("users.deleteUser"))}</button>` : ""}
        </div>
      </div>
    ` : roleControls ? `
      <div class="user-admin-account-body">
        <div class="user-admin-memberships">${roleControls}</div>
        <div class="settings-action-row settings-card-footer">
          <button class="settings-action-btn subtle" type="button" data-save-role>${escapeHtml(t("users.saveRole"))}</button>
          ${canDelete ? `<button class="settings-action-btn danger" type="button" data-delete-user>${escapeHtml(t("users.deleteUser"))}</button>` : ""}
        </div>
      </div>
    ` : "";
    if (!membershipsEnabled && !roleControls) {
      return `
        <article class="user-admin-account${self ? " is-self" : ""}" data-user-id="${escapeAttr(account.id)}" data-role="${escapeAttr(account.role || "")}">
          ${row}
        </article>
      `;
    }
    if (!membershipsEnabled) {
      return `
        <details class="user-admin-account${self ? " is-self" : ""}" data-user-id="${escapeAttr(account.id)}" data-role="${escapeAttr(account.role || "")}"${openAccountIds.has(account.id) ? " open" : ""}>
          <summary class="user-admin-account-summary">${row}</summary>
          ${body}
        </details>
      `;
    }
    return `
      <details class="user-admin-account${self ? " is-self" : ""}" data-user-id="${escapeAttr(account.id)}" data-role="${escapeAttr(account.role || "")}"${openAccountIds.has(account.id) ? " open" : ""}>
        <summary class="user-admin-account-summary">${row}</summary>
        ${body}
      </details>
    `;
  }

  function renderIssuedKey() {
    if (!issuedKey) return "";
    return `
      <section class="user-admin-issued-key" role="status">
        <div class="user-admin-issued-copy">
          <div class="settings-provider-title settings-card-title">${escapeHtml(t("users.issuedKeyTitle"))}</div>
          <p class="settings-card-description">${escapeHtml(t("users.createdKey"))}</p>
          <code class="user-admin-key-value">${escapeHtml(issuedKey)}</code>
        </div>
        <button id="copyIssuedAccessKeyBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("users.copyKey"))}</button>
      </section>
    `;
  }

  function renderCreatePanel() {
    const operatorOpen = createPanel === "operator";
    const collaboratorOpen = createPanel === "collaborator";
    const guestOpen = createPanel === "guest";
    const titleKey = guestOpen ? "users.createGuest" : operatorOpen ? "users.createOperator" : "users.createCollaborator";
    return `
      <section class="settings-provider-section settings-page-section settings-card user-admin-create-panel"${createPanel ? "" : " hidden"}>
        <div class="settings-card-header user-admin-create-head">
          <div class="settings-card-title" data-create-title>${escapeHtml(t(titleKey))}</div>
          <button class="settings-action-btn subtle" type="button" data-close-create>${escapeHtml(t("common.cancel"))}</button>
        </div>
        <div data-create-form="operator"${operatorOpen ? "" : " hidden"}>
          <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("users.createOperatorHint"))}</p>
          <form id="createOperatorForm" class="settings-card-content user-admin-create-form">
            <div class="settings-provider-form-grid settings-form-grid">
              <label class="settings-form-field">${escapeHtml(t("users.operatorHandle"))}
                <input id="operatorHandleInput" class="settings-field" required autocomplete="off" placeholder="${escapeAttr(t("users.handlePlaceholder"))}" />
              </label>
              <label class="settings-form-field">${escapeHtml(t("users.operatorPassword"))}
                <input id="operatorPasswordInput" class="settings-field" type="password" required minlength="8" autocomplete="new-password" placeholder="${escapeAttr(t("users.collaboratorPasswordPlaceholder"))}" />
              </label>
            </div>
            ${projectPicker([], t("users.operatorProjects"))}
            <div class="settings-action-row settings-card-footer">
              <button class="settings-action-btn primary" type="submit">${escapeHtml(t("users.createOperator"))}</button>
            </div>
          </form>
        </div>
        <div data-create-form="collaborator"${collaboratorOpen ? "" : " hidden"}>
          <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("users.createCollaboratorHint"))}</p>
          <form id="createCollaboratorForm" class="settings-card-content user-admin-create-form">
            <div class="settings-provider-form-grid settings-form-grid">
              <label class="settings-form-field">${escapeHtml(t("users.collaboratorHandle"))}
                <input id="collaboratorHandleInput" class="settings-field" required autocomplete="off" placeholder="${escapeAttr(t("users.handlePlaceholder"))}" />
              </label>
              <label class="settings-form-field">${escapeHtml(t("users.collaboratorPassword"))}
                <input id="collaboratorPasswordInput" class="settings-field" type="password" required minlength="8" autocomplete="new-password" placeholder="${escapeAttr(t("users.collaboratorPasswordPlaceholder"))}" />
              </label>
            </div>
            ${projectPicker([], t("users.collaboratorProjects"))}
            <div class="settings-action-row settings-card-footer">
              <button class="settings-action-btn primary" type="submit">${escapeHtml(t("users.createCollaborator"))}</button>
            </div>
          </form>
        </div>
        <div data-create-form="guest"${guestOpen ? "" : " hidden"}>
          <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("users.createGuestHint"))}</p>
          <form id="createGuestForm" class="settings-card-content user-admin-create-form">
            <div class="settings-provider-form-grid settings-form-grid">
              <label class="settings-form-field">${escapeHtml(t("users.guestHandle"))}
                <input id="guestHandleInput" class="settings-field" required autocomplete="off" placeholder="${escapeAttr(t("users.handlePlaceholder"))}" />
              </label>
              <label class="settings-form-field">${escapeHtml(t("users.guestPassword"))}
                <input id="guestPasswordInput" class="settings-field" type="password" autocomplete="new-password" placeholder="${escapeAttr(t("users.passwordPlaceholder"))}" />
              </label>
              <label class="settings-form-field settings-form-span-2">${escapeHtml(t("users.keyLabel"))}
                <input id="guestKeyLabelInput" class="settings-field" placeholder="${escapeAttr(t("users.keyLabelPlaceholder"))}" />
              </label>
              <label class="settings-check-row settings-form-span-2">
                <input id="guestIssueKeyInput" type="checkbox" checked />
                <span><strong>${escapeHtml(t("users.issueKey"))}</strong><small data-settings-help-copy>${escapeHtml(t("users.issueKeyHint"))}</small></span>
              </label>
            </div>
            ${projectPicker([])}
            <div class="settings-action-row settings-card-footer">
              <button class="settings-action-btn primary" type="submit">${escapeHtml(t("users.createGuest"))}</button>
            </div>
          </form>
        </div>
      </section>
    `;
  }

  function renderHero({ title, description, action }) {
    const accounts = Array.isArray(state.userAccounts) ? state.userAccounts : [];
    return `
      <section class="settings-hero-card settings-page-section settings-card user-admin-hero">
        <div class="settings-card-header user-admin-hero-head">
          <div>
            <div class="settings-hero-kicker">${escapeHtml(t("users.kicker"))}</div>
            <div class="settings-hero-title settings-card-title">${escapeHtml(title)}</div>
            <p class="settings-card-description" data-settings-help-copy>${escapeHtml(description)}</p>
          </div>
          <div class="user-admin-hero-tools">
            ${state.authStatus?.hasUsers ? `<span class="settings-status-pill settings-badge accent">${escapeHtml(t("users.accountCount", { count: accounts.length }))}</span>` : ""}
            ${action}
          </div>
        </div>
      </section>
    `;
  }

  function render() {
    const hasUsers = Boolean(state.authStatus?.hasUsers);
    const accounts = Array.isArray(state.userAccounts) ? state.userAccounts : [];
    if (!hasUsers) {
      return `
        <div class="settings-live-page user-admin-page">
          ${renderHero({
            title: t("users.noUsersTitle"),
            description: t("users.bootstrapHint"),
            action: `<button id="createAdministratorBtn" class="settings-action-btn primary" type="button">${escapeHtml(t("users.createAdmin"))}</button>`,
          })}
        </div>
      `;
    }
    return `
      <div class="settings-live-page user-admin-page">
        ${renderHero({
          title: t("users.hasUsersTitle"),
          description: t("users.description"),
          action: `
            <button id="createOperatorBtn" class="settings-action-btn ${createPanel === "operator" ? "primary" : "subtle"}" type="button" aria-pressed="${createPanel === "operator" ? "true" : "false"}">${escapeHtml(t("users.createOperator"))}</button>
            <button id="createCollaboratorBtn" class="settings-action-btn ${createPanel === "collaborator" ? "primary" : "subtle"}" type="button" aria-pressed="${createPanel === "collaborator" ? "true" : "false"}">${escapeHtml(t("users.createCollaborator"))}</button>
            <button id="createGuestBtn" class="settings-action-btn ${createPanel === "guest" ? "primary" : "subtle"}" type="button" aria-pressed="${createPanel === "guest" ? "true" : "false"}">${escapeHtml(t("users.createGuest"))}</button>
            <button id="refreshUserAccountsBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("users.refresh"))}</button>
          `,
        })}
        <div class="settings-stat-grid user-admin-stats">
          <div class="settings-stat-card"><strong>${countByRole(accounts, "admin")}</strong><span>${escapeHtml(t("users.statsAdmin"))}</span></div>
          <div class="settings-stat-card"><strong>${countByRole(accounts, "user")}</strong><span>${escapeHtml(t("users.statsOperator"))}</span></div>
          <div class="settings-stat-card"><strong>${countByRole(accounts, "collaborator")}</strong><span>${escapeHtml(t("users.statsCollaborator"))}</span></div>
          <div class="settings-stat-card"><strong>${countByRole(accounts, "guest")}</strong><span>${escapeHtml(t("users.statsGuest"))}</span></div>
        </div>
        ${renderIssuedKey()}
        ${renderCreatePanel()}
        <section class="settings-provider-section settings-page-section settings-card user-admin-accounts">
          <div class="settings-card-header">
            <div class="settings-card-title">${escapeHtml(t("users.accounts"))}</div>
            <p class="settings-card-description" data-settings-help-copy>${escapeHtml(t("users.accountsHint"))}</p>
          </div>
          <div class="user-admin-account-list">
            ${accounts.length ? accounts.map(renderAccount).join("") : `<p class="settings-card-description">${escapeHtml(t("users.empty"))}</p>`}
          </div>
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
  }

  async function createOperator(event) {
    event.preventDefault();
    const handle = $("operatorHandleInput")?.value?.trim() || "";
    const password = $("operatorPasswordInput")?.value || "";
    const projectIds = selectedProjectIds($("createOperatorForm"));
    const button = event.submitter || event.currentTarget.querySelector("button[type=submit]");
    setButtonBusy(button, true, t("common.loading"));
    try {
      await request("/api/users/operators", {
        method: "POST",
        body: JSON.stringify({ handle, password, projectIds }),
      });
      $("operatorHandleInput").value = "";
      $("operatorPasswordInput").value = "";
      createPanel = "";
      await reload();
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function createCollaborator(event) {
    event.preventDefault();
    const handle = $("collaboratorHandleInput")?.value?.trim() || "";
    const password = $("collaboratorPasswordInput")?.value || "";
    const projectIds = selectedProjectIds($("createCollaboratorForm"));
    const button = event.submitter || event.currentTarget.querySelector("button[type=submit]");
    setButtonBusy(button, true, t("common.loading"));
    try {
      await request("/api/users/collaborators", {
        method: "POST",
        body: JSON.stringify({ handle, password, projectIds }),
      });
      $("collaboratorHandleInput").value = "";
      $("collaboratorPasswordInput").value = "";
      createPanel = "";
      await reload();
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
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
      createPanel = "";
      await reload();
      if (issuedKey) copyText?.(issuedKey);
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function saveRole(userId, root) {
    const button = root.querySelector("[data-save-role]");
    setButtonBusy(button, true, t("common.loading"));
    try {
      const role = root.querySelector("[data-user-role]")?.value || "";
      await request(`/api/users/${encodeURIComponent(userId)}/role`, {
        method: "PATCH",
        body: JSON.stringify({ role }),
      });
      await reload();
      showToast?.(t("users.roleSaved"), "success", { force: true });
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
  }

  async function saveMemberships(userId, root) {
    const button = root.querySelector("[data-save-memberships]");
    setButtonBusy(button, true, t("common.loading"));
    try {
      await request(`/api/users/${encodeURIComponent(userId)}/memberships`, {
        method: "PUT",
        body: JSON.stringify({ projectIds: selectedProjectIds(root) }),
      });
      await reload();
      showToast?.(t("users.membershipsSaved"), "success", { force: true });
    } catch (error) {
      showError?.(error);
    } finally {
      setButtonBusy(button, false);
    }
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

  function setProjectPickerOpen(picker, open) {
    const toggle = picker.querySelector("[data-project-picker-toggle]");
    picker.classList.toggle("is-open", open);
    toggle?.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) toggle?.setAttribute("aria-controls", "userAdminProjectMenu");
    else toggle?.removeAttribute("aria-controls");
  }

  function syncProjectOption(option, input) {
    const selected = Boolean(input?.checked);
    option?.setAttribute("aria-selected", selected ? "true" : "false");
    const check = option?.querySelector(".composer-select-option-check");
    if (check) check.textContent = selected ? "✓" : "";
  }

  function positionProjectMenu(trigger, menu) {
    if (!trigger || !menu) return;
    const rect = trigger.getBoundingClientRect();
    const viewportWidth = globalThis.innerWidth || document.documentElement.clientWidth || 0;
    const viewportHeight = globalThis.innerHeight || document.documentElement.clientHeight || 0;
    const width = Math.min(Math.max(rect.width, 260), Math.max(160, viewportWidth - 16));
    menu.style.left = `${Math.min(Math.max(8, rect.left), Math.max(8, viewportWidth - width - 8))}px`;
    menu.style.width = `${width}px`;
    const spaceBelow = viewportHeight - rect.bottom - 8;
    const spaceAbove = rect.top - 8;
    const openDown = spaceBelow >= 160 || spaceBelow >= spaceAbove;
    const available = (openDown ? spaceBelow : spaceAbove) - 6;
    if (openDown) {
      menu.style.top = `${rect.bottom + 6}px`;
      menu.style.bottom = "auto";
    } else {
      menu.style.top = "auto";
      menu.style.bottom = `${Math.max(8, viewportHeight - rect.top + 6)}px`;
    }
    menu.style.maxHeight = `${Math.max(8, Math.min(420, available))}px`;
  }

  function closeProjectPickers() {
    document.querySelectorAll(".user-admin-project-picker.is-open").forEach((picker) => {
      setProjectPickerOpen(picker, false);
    });
    floatingMenu?.remove();
    floatingMenu = null;
  }

  function openProjectPicker(picker) {
    closeProjectPickers();
    setProjectPickerOpen(picker, true);
    const menu = document.createElement("div");
    menu.id = "userAdminProjectMenu";
    menu.className = "composer-select-popover user-admin-project-menu";
    menu.setAttribute("role", "listbox");
    menu.setAttribute("aria-multiselectable", "true");
    menu.setAttribute("aria-label", picker.querySelector(".user-admin-project-trigger-copy strong")?.textContent || t("users.projects"));
    picker.querySelectorAll("[data-guest-project-id]").forEach((input) => {
      const option = document.createElement("button");
      option.type = "button";
      option.className = "composer-select-option";
      option.setAttribute("role", "option");
      option.dataset.guestProjectOption = input.dataset.guestProjectId || "";
      const label = document.createElement("span");
      label.textContent = input.dataset.guestProjectName || input.dataset.guestProjectId || "";
      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      option.append(label, check);
      syncProjectOption(option, input);
      option.addEventListener("click", (event) => {
        event.preventDefault();
        event.stopPropagation();
        input.checked = !input.checked;
        input.dispatchEvent(new Event("change", { bubbles: true }));
        syncProjectOption(option, input);
      });
      menu.appendChild(option);
    });
    document.body.appendChild(menu);
    floatingMenu = menu;
    positionProjectMenu(picker.querySelector("[data-project-picker-toggle]"), menu);
  }

  function syncProjectPickerSummary(picker) {
    const summary = picker.querySelector("[data-project-picker-summary]");
    if (summary) summary.textContent = projectSummary(selectedProjectIds(picker));
  }

  function setCreatePanel(id) {
    const next = createPanel === id ? "" : id;
    shouldFocusCreate = Boolean(next);
    createPanel = next;
    onChange?.();
  }

  function bind() {
    pickerEvents?.abort();
    floatingMenu?.remove();
    floatingMenu = null;
    pickerEvents = new AbortController();
    const { signal } = pickerEvents;
    $("refreshUserAccountsBtn")?.addEventListener("click", () => refresh().catch(showError), { signal });
    $("createAdministratorBtn")?.addEventListener("click", () => onCreateAdministrator?.(), { signal });
    $("createOperatorBtn")?.addEventListener("click", () => setCreatePanel("operator"), { signal });
    $("createCollaboratorBtn")?.addEventListener("click", () => setCreatePanel("collaborator"), { signal });
    $("createGuestBtn")?.addEventListener("click", () => setCreatePanel("guest"), { signal });
    document.querySelector("[data-close-create]")?.addEventListener("click", () => setCreatePanel(""), { signal });
    $("createOperatorForm")?.addEventListener("submit", (event) => createOperator(event).catch(showError), { signal });
    $("createCollaboratorForm")?.addEventListener("submit", (event) => createCollaborator(event).catch(showError), { signal });
    $("createGuestForm")?.addEventListener("submit", (event) => createGuest(event).catch(showError), { signal });
    $("copyIssuedAccessKeyBtn")?.addEventListener("click", () => issuedKey && copyText?.(issuedKey), { signal });
    document.querySelectorAll("details.user-admin-account").forEach((node) => {
      node.addEventListener("toggle", () => {
        const id = node.dataset.userId;
        if (!id) return;
        if (node.open) openAccountIds.add(id);
        else openAccountIds.delete(id);
      }, { signal });
    });
    if (shouldFocusCreate) {
      shouldFocusCreate = false;
      if (createPanel === "operator") $("operatorHandleInput")?.focus();
      if (createPanel === "collaborator") $("collaboratorHandleInput")?.focus();
      if (createPanel === "guest") $("guestHandleInput")?.focus();
    }
    document.querySelectorAll(".user-admin-account").forEach((root) => {
      const userId = root.dataset.userId;
      const account = (state.userAccounts || []).find((item) => item.id === userId);
      root.querySelector("[data-save-memberships]")?.addEventListener("click", () => saveMemberships(userId, root).catch(showError), { signal });
      root.querySelector("[data-save-role]")?.addEventListener("click", () => saveRole(userId, root).catch(showError), { signal });
      root.querySelector("[data-issue-key]")?.addEventListener("click", () => issueKey(userId).catch(showError), { signal });
      root.querySelector("[data-delete-user]")?.addEventListener("click", () => deleteUser(account).catch(showError), { signal });
      root.querySelectorAll("[data-revoke-key]").forEach((button) => {
        button.addEventListener("click", () => revokeKey(userId, button.dataset.revokeKey).catch(showError), { signal });
      });
    });
    document.querySelectorAll(".user-admin-project-picker").forEach((picker) => {
      picker.querySelector("[data-project-picker-toggle]")?.addEventListener("click", (event) => {
        event.preventDefault();
        event.stopPropagation();
        if (picker.classList.contains("is-open")) closeProjectPickers();
        else openProjectPicker(picker);
      }, { signal });
      picker.querySelectorAll("[data-guest-project-id]").forEach((input) => {
        input.addEventListener("change", () => syncProjectPickerSummary(picker), { signal });
      });
    });
    document.addEventListener("pointerdown", (event) => {
      const node = event.target instanceof Element ? event.target : event.target?.parentElement;
      if (node?.closest?.(".user-admin-project-picker, .user-admin-project-menu")) return;
      closeProjectPickers();
    }, { signal });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeProjectPickers();
    }, { signal });
    const reposition = () => {
      const picker = document.querySelector(".user-admin-project-picker.is-open");
      if (picker && floatingMenu) {
        positionProjectMenu(picker.querySelector("[data-project-picker-toggle]"), floatingMenu);
      }
    };
    window.addEventListener("resize", reposition, { signal });
    document.addEventListener("scroll", reposition, { signal, capture: true });
  }

  return { render, bind, load };
}
