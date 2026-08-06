import { $, escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { t } from "./i18n.mjs?v=remote-control-full-3-remote-full-toggle-3-cloudflared-install-1";
import { applyRemoteAccessFailClosed, fullAccessAllowed, remoteAccessContext } from "./remote-access-capabilities.mjs";
import { qrToSvg } from "./qrcode.mjs";

const endpoint = "/api/security/remote-access";

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function textValue(value, fallback = "") {
  const text = String(value ?? "").trim();
  return text || fallback;
}

export function normalizeRemoteAccess(value = {}) {
  const source = objectValue(value);
  const credential = objectValue(source.credential);
  const policy = objectValue(source.policy);
  const session = objectValue(source.session);
  const capabilities = objectValue(source.capabilities);
  const tunnel = objectValue(source.tunnel);
  const sessionMode = textValue(session.mode, "local");
  const revision = Number(policy.revision);
  return {
    credential: {
      configured: Boolean(credential.configured),
      source: textValue(credential.source, "none"),
    },
    policy: {
      allowFullAccess: Boolean(policy.allowFullAccess),
      defaultMode: Boolean(policy.allowFullAccess) ? "full" : "restricted",
      allowRemoteNativePicker: Boolean(policy.allowRemoteNativePicker),
      revision: Number.isSafeInteger(revision) && revision >= 1 ? revision : 0,
    },
    session: {
      remote: Boolean(session.remote),
      authenticated: Boolean(session.authenticated),
      mode: ["restricted", "full", "local"].includes(sessionMode) ? sessionMode : "restricted",
      expiresAt: textValue(session.expiresAt),
    },
    capabilities: {
      maxPermissionMode: textValue(capabilities.maxPermissionMode, "acceptEdits"),
      terminalAllowed: Boolean(capabilities.terminalAllowed),
      filesystemScope: ["host", "full"].includes(textValue(capabilities.filesystemScope).toLowerCase()) ? "full" : "restricted",
      nativePickerAllowed: Boolean(capabilities.nativePickerAllowed),
      securityAdminAllowed: Boolean(capabilities.securityAdminAllowed),
    },
    tunnel: {
      available: Boolean(tunnel.available),
      installable: !Boolean(tunnel.available) && Boolean(tunnel.installable),
      status: ["idle", "installing", "starting", "running", "stopping", "unavailable", "error"].includes(textValue(tunnel.status)) ? textValue(tunnel.status) : "unavailable",
      publicUrl: textValue(tunnel.publicUrl),
      error: textValue(tunnel.error),
      startedAt: textValue(tunnel.startedAt),
    },
  };
}

function hasAuthoritativeRemoteAccessState(value) {
  const source = objectValue(value);
  const session = objectValue(source.session);
  const capabilities = objectValue(source.capabilities);
  return typeof session.remote === "boolean"
    && typeof session.authenticated === "boolean"
    && typeof session.mode === "string"
    && typeof capabilities.maxPermissionMode === "string"
    && typeof capabilities.terminalAllowed === "boolean"
    && typeof capabilities.filesystemScope === "string"
    && typeof capabilities.nativePickerAllowed === "boolean"
    && typeof capabilities.securityAdminAllowed === "boolean";
}

export function isEnvironmentCredential(source) {
  const value = String(source || "").trim().toLowerCase();
  return value === "environment" || value === "env";
}

export function policyPayload(access, draft, currentPassword = "") {
  const normalizedAccess = normalizeRemoteAccess(access);
  const normalizedDraft = objectValue(draft);
  const allowFullAccess = normalizedDraft.allowFullAccess === undefined
    ? normalizedDraft.defaultMode === "full"
    : Boolean(normalizedDraft.allowFullAccess);
  const payload = {
    allowFullAccess,
    defaultMode: allowFullAccess ? "full" : "restricted",
    allowRemoteNativePicker: Boolean(normalizedDraft.allowRemoteNativePicker),
    revision: normalizedAccess.policy.revision,
  };
  const password = String(currentPassword || "");
  if (password) payload.currentPassword = password;
  return payload;
}

export function passwordPayload(strategy, password = "", currentPassword = "") {
  const normalizedStrategy = strategy === "custom" ? "custom" : "generate";
  const payload = { strategy: normalizedStrategy };
  if (normalizedStrategy === "custom") payload.password = String(password || "");
  const current = String(currentPassword || "");
  if (current) payload.currentPassword = current;
  return payload;
}

export function createRemoteAccessSettingsController({
  state,
  request,
  copyText,
  onChange,
  showError,
  showToast,
}) {
  let generatedPassword = "";
  let remoteSessionRevoked = false;
  let loadSequence = 0;
  let authorizationEpoch = 0;

  const rt = (key, params = {}) => t(`remoteAccess.${key}`, params);

  function access() {
    return normalizeRemoteAccess(state?.remoteAccess || {});
  }

  function requiresCurrentPassword() {
    const session = access().session;
    return Boolean(session.remote && session.authenticated);
  }

  function invalidateRemoteSessionIfNeeded(wasRemote) {
    if (!wasRemote) return;
    const current = access();
    remoteSessionRevoked = true;
    state.remoteAccessFailClosed = true;
    state.remoteAccess = normalizeRemoteAccess({
      ...current,
      session: { ...current.session, authenticated: false, expiresAt: "" },
      capabilities: {
        maxPermissionMode: "acceptEdits",
        terminalAllowed: false,
        filesystemScope: "project",
        nativePickerAllowed: false,
        securityAdminAllowed: false,
      },
    });
  }

  function invalidatePendingLoads({ status } = {}) {
    authorizationEpoch += 1;
    if (Number(status) === 401) remoteSessionRevoked = true;
    return authorizationEpoch;
  }

  async function load() {
    const sequence = ++loadSequence;
    const startedAuthorizationEpoch = authorizationEpoch;
    state.remoteAccessLoading = true;
    state.remoteAccessError = "";
    const remoteBeforeLoad = remoteAccessContext(state);
    const current = () => sequence === loadSequence && startedAuthorizationEpoch === authorizationEpoch;
    try {
      const result = await request(endpoint);
      if (!current()) return state.remoteAccess;
      state.remoteAccess = normalizeRemoteAccess(result);
      if (hasAuthoritativeRemoteAccessState(result)) {
        state.remoteAccessFailClosed = false;
        remoteSessionRevoked = false;
      } else if (remoteBeforeLoad || state.remoteAccess.session.remote) {
        applyRemoteAccessFailClosed(state);
      }
      return state.remoteAccess;
    } catch (err) {
      if (!current()) throw err;
      state.remoteAccessError = err?.message || String(err);
      // A local-token failure is not evidence that localhost became a remote
      // session. Only a context already known to be remote may synthesize the
      // conservative remote capability set.
      if (remoteBeforeLoad) {
        applyRemoteAccessFailClosed(state, { status: err?.status });
        remoteSessionRevoked = err?.status === 401;
      }
      throw err;
    } finally {
      if (sequence === loadSequence) {
        state.remoteAccessLoading = false;
        onChange?.(state.remoteAccess);
      }
    }
  }

  async function savePolicy(draft, currentPassword = "") {
    const current = access();
    const result = await request(`${endpoint}/policy`, {
      method: "PATCH",
      body: JSON.stringify(policyPayload(current, draft, currentPassword)),
    });
    state.remoteAccess = normalizeRemoteAccess({ ...current, policy: result });
    generatedPassword = "";
    invalidateRemoteSessionIfNeeded(current.session.remote);
    showToast?.(rt("policySaved"));
    onChange?.(state.remoteAccess);
    return state.remoteAccess;
  }

  async function updatePassword(strategy, password = "", currentPassword = "") {
    const current = access();
    const result = await request(`${endpoint}/password`, {
      method: "PUT",
      body: JSON.stringify(passwordPayload(strategy, password, currentPassword)),
    });
    generatedPassword = strategy === "generate" ? textValue(result?.generatedPassword) : "";
    state.remoteAccess = normalizeRemoteAccess({
      ...current,
      credential: result?.credential || { configured: true, source: "config" },
      policy: { ...current.policy, revision: result?.revision || current.policy.revision },
    });
    invalidateRemoteSessionIfNeeded(current.session.remote);
    showToast?.(rt(strategy === "generate" ? "passwordGenerated" : "passwordUpdated"));
    onChange?.(state.remoteAccess);
    return { access: state.remoteAccess, generatedPassword };
  }

  async function updateTunnel(method, action = "") {
    const suffix = textValue(action);
    const result = await request(`${endpoint}/tunnel${suffix ? `/${suffix}` : ""}`, { method });
    const current = access();
    state.remoteAccess = normalizeRemoteAccess({ ...current, tunnel: result });
    onChange?.(state.remoteAccess);
    return state.remoteAccess.tunnel;
  }

  async function installTunnel() {
    return updateTunnel("POST", "install");
  }

  async function startTunnel() {
    return updateTunnel("POST");
  }

  async function stopTunnel() {
    return updateTunnel("DELETE");
  }

  function currentPasswordField(id) {
    if (!requiresCurrentPassword() || !access().capabilities.securityAdminAllowed) return "";
    return `<label class="settings-form-field remote-access-current-password">${escapeHtml(rt("currentPassword"))}<input id="${escapeAttr(id)}" class="settings-field" type="password" autocomplete="current-password" required placeholder="${escapeAttr(rt("currentPasswordPlaceholder"))}" /><small>${escapeHtml(rt("currentPasswordHint"))}</small></label>`;
  }

  function capabilityValue(value) {
    return value ? rt("allowed") : rt("blocked");
  }

  // Boolean capabilities carry a tone so allowed/blocked is scannable; the two
  // enum capabilities stay plain text because a colour would imply a judgement
  // the value does not make.
  function renderCapabilityCell(label, value) {
    const boolean = typeof value === "boolean";
    const text = boolean ? capabilityValue(value) : String(value ?? "");
    const badge = boolean ? `<span class="settings-badge ${value ? "ok" : "warn"}">${escapeHtml(text)}</span>` : `<strong>${escapeHtml(text)}</strong>`;
    return `<div class="remote-access-capability-cell"><span>${escapeHtml(label)}</span>${badge}</div>`;
  }

  function sessionModeLabel(mode) {
    if (mode === "full") return rt("full");
    if (mode === "restricted") return rt("restricted");
    return rt("local");
  }

  function fullAccessStatusLabel(allowFullAccess) {
    return rt(allowFullAccess ? "fullEnabledDefault" : "fullDisabled");
  }

  function tunnelStatusLabel(status) {
    const labels = {
      idle: "tunnelIdle",
      installing: "tunnelInstalling",
      starting: "tunnelStarting",
      running: "tunnelRunning",
      stopping: "tunnelStopping",
      unavailable: "tunnelUnavailable",
      error: "tunnelError",
    };
    return rt(labels[status] || labels.unavailable);
  }

  function renderTunnelQr(publicUrl) {
    const url = String(publicUrl || "").trim();
    if (!url) return "";
    let svg = "";
    try {
      svg = qrToSvg(url, { size: 168, margin: 2 });
    } catch {
      return `<p class="settings-card-description">${escapeHtml(rt("tunnelQrUnavailable"))}</p>`;
    }
    return `
          <div class="remote-access-tunnel-phone">
            <div class="remote-access-tunnel-qr" aria-hidden="false">${svg}</div>
            <div class="remote-access-tunnel-phone-copy">
              <strong>${escapeHtml(rt("phoneScanTitle"))}</strong>
              <ol class="remote-access-phone-steps">
                <li>${escapeHtml(rt("phoneStep1"))}</li>
                <li>${escapeHtml(rt("phoneStep2"))}</li>
                <li>${escapeHtml(rt("phoneStep3"))}</li>
              </ol>
              <p class="settings-card-description">${escapeHtml(rt("phoneSafetyHint"))}</p>
            </div>
          </div>`;
  }

  function renderTunnelSection(value, securityAdminAllowed) {
    const tunnel = value.tunnel;
    const active = tunnel.status === "running";
    const busy = tunnel.status === "installing" || tunnel.status === "starting" || tunnel.status === "stopping";
    let actionButton = "";
    let footerHint = rt("tunnelAccessHint");
    if (!tunnel.available) {
      footerHint = securityAdminAllowed
        ? rt(tunnel.installable ? "tunnelInstallHint" : "tunnelInstallUnsupportedHint")
        : rt("tunnelAccessHint");
      if (tunnel.installable) {
        const canInstall = securityAdminAllowed && !busy;
        actionButton = `<button id="installRemoteTunnelBtn" class="settings-action-btn remote-access-tunnel-action primary" type="button" data-remote-tunnel-action="install" data-remote-tunnel-busy-label="${escapeAttr(rt("tunnelInstalling"))}" ${canInstall ? "" : "disabled"}>${escapeHtml(busy ? tunnelStatusLabel(tunnel.status) : rt("installTunnel"))}</button>`;
      }
    } else {
      const canManage = securityAdminAllowed && value.credential.configured;
      const actionLabel = active ? rt("stopTunnel") : rt("startTunnel");
      const actionMethod = active ? "stop" : "start";
      // When no access password is configured the button stays disabled and a
      // clear hint replaces the generic footer copy so the user knows what to do.
      if (!value.credential.configured && !active) {
        footerHint = rt("tunnelNeedCredential");
      }
      actionButton = `<button id="${actionMethod}RemoteTunnelBtn" class="settings-action-btn remote-access-tunnel-action ${active ? "subtle" : "primary"}" type="button" data-remote-tunnel-action="${actionMethod}" data-remote-tunnel-busy-label="${escapeAttr(active ? rt("tunnelStopping") : rt("tunnelStarting"))}" ${canManage && !busy ? "" : "disabled"}>${escapeHtml(busy ? tunnelStatusLabel(tunnel.status) : actionLabel)}</button>`;
    }
    return `
        <section class="settings-provider-section settings-page-section settings-card remote-access-tunnel-card">
          <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(rt("temporaryTunnel"))}</div><div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(rt("temporaryTunnelHint"))}</div></div><span class="settings-status-pill settings-badge ${active ? "ok" : tunnel.status === "error" ? "warn" : ""}">${escapeHtml(tunnelStatusLabel(tunnel.status))}</span></div>
          ${tunnel.error && tunnel.status === "error" ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(tunnel.error)}</div>` : ""}
          ${tunnel.publicUrl ? `<div class="remote-access-tunnel-url"><a href="${escapeAttr(tunnel.publicUrl)}" target="_blank" rel="noopener noreferrer">${escapeHtml(tunnel.publicUrl)}</a><button id="copyRemoteTunnelUrlBtn" class="settings-action-btn subtle" type="button">${escapeHtml(rt("copyTunnelUrl"))}</button></div>${renderTunnelQr(tunnel.publicUrl)}` : `<p class="settings-card-description">${escapeHtml(tunnel.available ? rt("tunnelStopped") : rt("tunnelUnavailableHint"))}</p>`}
          <div class="settings-action-row settings-card-footer"><span class="settings-provider-meta">${escapeHtml(footerHint)}</span>${actionButton}</div>
        </section>`;
  }

  function render() {
    const value = access();
    const environmentCredential = isEnvironmentCredential(value.credential.source);
    const fullAllowed = fullAccessAllowed(state);
    const securityAdminAllowed = value.capabilities.securityAdminAllowed;
    const fullAccessEnabled = value.policy.allowFullAccess;
    const canEditFullPolicy = fullAllowed && securityAdminAllowed;
    const fullStatus = fullAccessStatusLabel(fullAccessEnabled);
    if (!state?.remoteAccess && state?.remoteAccessLoading) {
      return `<div class="settings-empty-card settings-empty-state">${escapeHtml(rt("loading"))}</div>`;
    }
    return `
      <div class="settings-live-page remote-access-page">
        <section class="settings-hero-card settings-page-section settings-card remote-access-hero">
          <div class="settings-card-header">
            <div>
              <div class="settings-hero-kicker">${escapeHtml(rt("session"))}</div>
              <div class="settings-hero-title settings-card-title">${escapeHtml(value.session.remote ? rt("remote") : rt("local"))}</div>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(rt("description"))}</p>
            </div>
            <span class="settings-status-pill settings-badge ${value.session.authenticated ? "ok" : "warn"}">${escapeHtml(value.session.authenticated ? rt("authenticated") : rt("unauthenticated"))}</span>
          </div>
          <div class="settings-action-row settings-card-footer"><button id="refreshRemoteAccessBtn" class="settings-action-btn subtle" type="button">${escapeHtml(rt("refresh"))}</button></div>
        </section>
        ${remoteSessionRevoked ? `<div class="settings-inline-alert settings-alert" role="status"><span>${escapeHtml(rt("sessionRevoked"))}</span> <a class="settings-action-btn subtle" href="/auth/remote-access">${escapeHtml(rt("signInAgain"))}</a></div>` : ""}
        ${state?.remoteAccessError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.remoteAccessError)}</div>` : ""}
        ${!securityAdminAllowed ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("localOnlyNotice"))}</div>` : ""}
        ${renderTunnelSection(value, securityAdminAllowed)}
        <div class="remote-access-summary-grid settings-stat-grid">
          <div class="settings-stat-card"><strong>${escapeHtml(sessionModeLabel(value.session.mode))}</strong><span>${escapeHtml(rt("mode"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(value.credential.configured ? rt("configured") : rt("notConfigured"))}</strong><span>${escapeHtml(rt("credential"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(value.credential.source)}</strong><span>${escapeHtml(rt("source"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(value.session.expiresAt || rt("never"))}</strong><span>${escapeHtml(rt("expiresAt"))}</span></div>
        </div>
        <section class="settings-provider-section settings-page-section settings-card remote-access-policy-card">
          <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(rt("policy"))}</div><div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(rt("policyDescription"))}</div></div><button class="settings-action-btn primary" type="submit" form="remoteAccessPolicyForm" data-remote-policy-submit ${securityAdminAllowed ? "" : "disabled"}>${escapeHtml(rt("savePolicy"))}</button></div>
          <form id="remoteAccessPolicyForm" class="settings-card-content remote-access-policy-form">
            <div class="settings-provider-form-grid settings-form-grid">
              <div class="remote-access-full-access-card${fullAccessEnabled ? " is-on" : ""}" data-remote-full-card>
                <div class="remote-access-full-access-copy"><strong>${escapeHtml(rt("allowFullAccess"))}</strong><small data-settings-help-copy>${escapeHtml(rt("allowFullAccessHint"))}</small><span class="remote-access-full-status ${fullAccessEnabled ? "ok" : "muted"}" data-remote-full-status>${escapeHtml(fullStatus)}</span></div>
                <label class="remote-access-switch" title="${escapeAttr(rt("allowFullAccess"))}"><input id="remoteAccessAllowFullAccess" type="checkbox" role="switch" aria-checked="${fullAccessEnabled ? "true" : "false"}" ${fullAccessEnabled ? "checked" : ""} ${canEditFullPolicy ? "" : "disabled"} /><span class="remote-access-switch-track" aria-hidden="true"></span></label>
              </div>
              <label class="settings-check-row"><input id="remoteAccessNativePicker" type="checkbox" ${value.policy.allowRemoteNativePicker ? "checked" : ""} ${securityAdminAllowed ? "" : "disabled"} /><span><strong>${escapeHtml(rt("nativePicker"))}</strong><small data-settings-help-copy>${escapeHtml(rt("nativePickerHint"))}</small></span></label>
              ${currentPasswordField("remoteAccessPolicyCurrentPassword")}
            </div>
          </form>
        </section>
        <section class="settings-provider-section settings-page-section settings-card">
          <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(rt("credential"))}</div><div class="settings-provider-meta settings-card-description">${escapeHtml(`${rt("source")}: ${value.credential.source}`)}</div></div></div>
          ${environmentCredential ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt(securityAdminAllowed ? "environmentOverrideHint" : "environmentReadonly"))}</div>` : ""}
          <div class="remote-access-password-grid settings-card-content">
            <form id="remoteAccessGeneratePasswordForm" class="remote-access-password-form"><strong>${escapeHtml(rt("generatePassword"))}</strong>${currentPasswordField("remoteAccessGenerateCurrentPassword")}<button class="settings-action-btn subtle" type="submit" data-remote-generate-submit ${securityAdminAllowed ? "" : "disabled"}>${escapeHtml(rt("generatePassword"))}</button></form>
            <form id="remoteAccessCustomPasswordForm" class="remote-access-password-form"><label class="settings-form-field">${escapeHtml(rt("customPassword"))}<input id="remoteAccessCustomPassword" class="settings-field" type="password" autocomplete="new-password" required ${securityAdminAllowed ? "" : "disabled"} placeholder="${escapeAttr(rt("customPasswordPlaceholder"))}" /></label>${currentPasswordField("remoteAccessCustomCurrentPassword")}<button class="settings-action-btn primary" type="submit" data-remote-custom-submit ${securityAdminAllowed ? "" : "disabled"}>${escapeHtml(rt("updatePassword"))}</button></form>
          </div>
          ${generatedPassword ? `<div class="remote-access-generated settings-inline-alert" role="status"><strong>${escapeHtml(rt("generatedPassword"))}</strong><code>${escapeHtml(generatedPassword)}</code><span>${escapeHtml(rt("generatedPasswordHint"))}</span><button id="copyGeneratedRemotePasswordBtn" class="settings-action-btn subtle" type="button">${escapeHtml(rt("copyPassword"))}</button></div>` : ""}
        </section>
        <section class="settings-provider-section settings-page-section settings-card">
          <div class="settings-provider-section-head settings-card-header"><div><div class="settings-provider-title settings-card-title">${escapeHtml(rt("capabilities"))}</div></div></div>
          <div class="remote-access-capability-grid settings-card-content">
            ${renderCapabilityCell(rt("maxPermissionMode"), value.capabilities.maxPermissionMode)}
            ${renderCapabilityCell(rt("filesystemScope"), value.capabilities.filesystemScope)}
            ${renderCapabilityCell(rt("terminalAllowed"), value.capabilities.terminalAllowed)}
            ${renderCapabilityCell(rt("nativePickerAllowed"), value.capabilities.nativePickerAllowed)}
            ${renderCapabilityCell(rt("securityAdminAllowed"), value.capabilities.securityAdminAllowed)}
          </div>
        </section>
      </div>`;
  }

  async function submitPolicy(form) {
    const allowFullAccess = Boolean($("remoteAccessAllowFullAccess")?.checked);
    await savePolicy({
      allowFullAccess,
      allowRemoteNativePicker: Boolean($("remoteAccessNativePicker")?.checked),
    }, $("remoteAccessPolicyCurrentPassword")?.value || "");
  }

  function bind() {
    $("refreshRemoteAccessBtn")?.addEventListener("click", async (event) => {
      setButtonBusy(event.currentTarget, true);
      try {
        await load();
        showToast?.(rt("refreshed"));
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(event.currentTarget, false);
      }
    });
    const tunnelButton = $("installRemoteTunnelBtn") || $("startRemoteTunnelBtn") || $("stopRemoteTunnelBtn");
    tunnelButton?.addEventListener("click", async (event) => {
      const button = event.currentTarget;
      const actionName = button.dataset.remoteTunnelAction;
      const action = { install: installTunnel, start: startTunnel, stop: stopTunnel }[actionName];
      if (!action) return;
      const busyKey = { install: "tunnelInstalling", start: "tunnelStarting", stop: "tunnelStopping" }[actionName];
      const toastKey = { install: "tunnelInstalledToast", start: "tunnelStartedToast", stop: "tunnelStoppedToast" }[actionName];
      const busyLabel = button.dataset.remoteTunnelBusyLabel || rt(busyKey);
      setButtonBusy(button, true, busyLabel);
      try {
        await action();
        showToast?.(rt(toastKey));
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    });
    $("copyRemoteTunnelUrlBtn")?.addEventListener("click", async () => {
      const publicUrl = access().tunnel.publicUrl;
      if (!publicUrl) return;
      try {
        await copyText?.(publicUrl);
        showToast?.(rt("tunnelUrlCopied"));
      } catch (err) {
        showError?.(err);
      }
    });
    $("remoteAccessAllowFullAccess")?.addEventListener("change", (event) => {
      const enabled = Boolean(event.currentTarget.checked);
      event.currentTarget.setAttribute("aria-checked", enabled ? "true" : "false");
      const card = document.querySelector?.("[data-remote-full-card]");
      card?.classList.toggle("is-on", enabled);
      const status = document.querySelector?.("[data-remote-full-status]");
      if (status) {
        status.textContent = fullAccessStatusLabel(enabled);
        status.classList.toggle("ok", enabled);
        status.classList.toggle("muted", !enabled);
      }
    });
    $("remoteAccessPolicyForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      // The submit button lives in the card header, outside this form, and is
      // wired to it with the form attribute. Scope the lookup to the document so
      // the busy state still lands on it.
      const button = document.querySelector?.("[data-remote-policy-submit]");
      setButtonBusy(button, true);
      try {
        await submitPolicy(event.currentTarget);
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    });
    $("remoteAccessGeneratePasswordForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.currentTarget.querySelector("[data-remote-generate-submit]");
      setButtonBusy(button, true);
      try {
        await updatePassword("generate", "", $("remoteAccessGenerateCurrentPassword")?.value || "");
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    });
    $("remoteAccessCustomPasswordForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.currentTarget.querySelector("[data-remote-custom-submit]");
      setButtonBusy(button, true);
      try {
        await updatePassword("custom", $("remoteAccessCustomPassword")?.value || "", $("remoteAccessCustomCurrentPassword")?.value || "");
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    });
    $("copyGeneratedRemotePasswordBtn")?.addEventListener("click", async () => {
      if (!generatedPassword) return;
      try {
        await copyText?.(generatedPassword);
        generatedPassword = "";
        showToast?.(rt("passwordCopied"));
        onChange?.(state.remoteAccess);
      } catch (err) {
        showError?.(err);
      }
    });
  }

  function consumeGeneratedPassword() {
    const value = generatedPassword;
    generatedPassword = "";
    return value;
  }

  function generatedPasswordValue() {
    return generatedPassword;
  }

  return {
    bind,
    consumeGeneratedPassword,
    generatedPasswordValue,
    invalidatePendingLoads,
    installTunnel,
    load,
    render,
    requiresCurrentPassword,
    savePolicy,
    startTunnel,
    stopTunnel,
    updatePassword,
  };
}
