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
  let pickerEvents = null;
  let floatingMenu = null;
  const openSections = new Set();

  function roleLabel(role) {
    if (role === "admin") return t("users.roleAdmin");
    if (role === "guest") return t("users.roleGuest");
    return t("users.roleUser");
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
    return t("users.collaboratorHint");
  }

  function renderAccount(account) {
    const keys = Array.isArray(account.keys) ? account.keys : [];
    const memberships = Array.isArray(account.projectIds) ? account.projectIds : [];
    const handle = account.handle || account.username || account.id;
    const self = account.id === state.account?.id;
    const guest = accountIsGuest(account);
    const collaborator = String(account.role || "") === "user";
    const membershipsEnabled = guest || collaborator;
    const keyCount = account.keyCount || keys.length || 0;
    return `
      <article class="settings-card user-admin-account${self ? " is-self" : ""}" data-user-id="${escapeAttr(account.id)}" data-role="${escapeAttr(account.role || "")}">
        <div class="user-admin-account-head settings-card-header">
          <div class="user-admin-identity">
            <span class="user-admin-avatar" aria-hidden="true">${escapeHtml(handleInitials(handle))}</span>
            <div class="user-admin-identity-copy">
              <div class="settings-provider-title settings-card-title">${escapeHtml(handle)}</div>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(accountHint(account, self))}</p>
            </div>
          </div>
          <div class="user-admin-badges">
            <span class="settings-status-pill settings-badge ${roleBadgeClass(account.role)}">${escapeHtml(roleLabel(account.role))}</span>
            ${self ? `<span class="settings-badge accent">${escapeHtml(t("users.you"))}</span>` : ""}
          </div>
        </div>
        <div class="user-admin-meta">
          <span class="user-admin-chip">${escapeHtml(account.passwordSet ? t("users.passwordSet") : t("users.passwordUnset"))}</span>
          ${guest ? `<span class="user-admin-chip">${escapeHtml(t("users.keyCount", { count: keyCount }))}</span>` : ""}
          ${membershipsEnabled ? `<span class="user-admin-chip">${escapeHtml(t("users.projectCount", { count: memberships.length }))}</span>` : ""}
        </div>
        ${membershipsEnabled ? `
          <div class="user-admin-memberships settings-card-content">
            ${projectPicker(memberships, guest ? t("users.projects") : t("users.collaboratorProjects"))}
            ${guest ? `<div class="user-admin-section-label">${escapeHtml(t("users.keys"))}</div>${renderKeys(keys)}` : ""}
          </div>
        ` : ""}
        ${membershipsEnabled || (accountIsAdmin(state.account) && !self) ? `
          <div class="settings-action-row settings-card-footer">
            ${membershipsEnabled ? `<button class="settings-action-btn primary" type="button" data-save-memberships>${escapeHtml(t("users.saveMemberships"))}</button>` : ""}
            ${guest ? `<button class="settings-action-btn subtle" type="button" data-issue-key>${escapeHtml(t("users.issueAnotherKey"))}</button>` : ""}
            ${accountIsAdmin(state.account) && !self ? `<button class="settings-action-btn danger" type="button" data-delete-user>${escapeHtml(t("users.deleteUser"))}</button>` : ""}
          </div>
        ` : ""}
      </article>
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

  function renderFold({ id, title, hint, extra = "", body }) {
    return `
      <details class="settings-provider-section settings-page-section settings-card user-admin-fold" data-user-admin-fold="${escapeAttr(id)}"${openSections.has(id) ? " open" : ""}>
        <summary class="settings-provider-section-head settings-card-header user-admin-fold-summary">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(title)}</div>
            <p class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(hint)}</p>
          </div>
          ${extra}
        </summary>
        ${body}
      </details>
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
          action: `<button id="refreshUserAccountsBtn" class="settings-action-btn subtle" type="button">${escapeHtml(t("users.refresh"))}</button>`,
        })}
        <div class="settings-stat-grid user-admin-stats">
          <div class="settings-stat-card"><strong>${countByRole(accounts, "admin")}</strong><span>${escapeHtml(t("users.statsAdmin"))}</span></div>
          <div class="settings-stat-card"><strong>${countByRole(accounts, "user")}</strong><span>${escapeHtml(t("users.statsUser"))}</span></div>
          <div class="settings-stat-card"><strong>${countByRole(accounts, "guest")}</strong><span>${escapeHtml(t("users.statsGuest"))}</span></div>
        </div>
        ${renderIssuedKey()}
        ${renderFold({
          id: "collaborator",
          title: t("users.createCollaborator"),
          hint: t("users.createCollaboratorHint"),
          body: `
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
          `,
        })}
        ${renderFold({
          id: "guest",
          title: t("users.createGuest"),
          hint: t("users.createGuestHint"),
          body: `
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
          `,
        })}
        ${renderFold({
          id: "accounts",
          title: t("users.accounts"),
          hint: t("users.accountsHint"),
          body: `
            <div class="user-admin-account-list settings-card-content">
              ${accounts.length ? accounts.map(renderAccount).join("") : `<p class="settings-card-description">${escapeHtml(t("users.empty"))}</p>`}
            </div>
          `,
        })}
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

  function bind() {
    pickerEvents?.abort();
    floatingMenu?.remove();
    floatingMenu = null;
    pickerEvents = new AbortController();
    const { signal } = pickerEvents;
    $("refreshUserAccountsBtn")?.addEventListener("click", () => refresh().catch(showError), { signal });
    $("createAdministratorBtn")?.addEventListener("click", () => onCreateAdministrator?.(), { signal });
    $("createCollaboratorForm")?.addEventListener("submit", (event) => createCollaborator(event).catch(showError), { signal });
    $("createGuestForm")?.addEventListener("submit", (event) => createGuest(event).catch(showError), { signal });
    $("copyIssuedAccessKeyBtn")?.addEventListener("click", () => issuedKey && copyText?.(issuedKey), { signal });
    document.querySelectorAll("[data-user-admin-fold]").forEach((node) => {
      node.addEventListener("toggle", () => {
        const id = node.dataset.userAdminFold;
        if (!id) return;
        if (node.open) openSections.add(id);
        else openSections.delete(id);
      }, { signal });
    });
    document.querySelectorAll(".user-admin-account").forEach((root) => {
      const userId = root.dataset.userId;
      const account = (state.userAccounts || []).find((item) => item.id === userId);
      root.querySelector("[data-save-memberships]")?.addEventListener("click", () => saveMemberships(userId, root).catch(showError), { signal });
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
