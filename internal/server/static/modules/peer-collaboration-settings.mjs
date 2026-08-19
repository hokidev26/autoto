import { objectValue } from "./value-coercion.mjs";
import { escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { qrToSvg } from "./qrcode.mjs";

const endpoint = "/api/remote-collaboration";

export const peerScopes = Object.freeze(["observe", "send_task", "approve_once", "approve_session", "execute_tools"]);
export const peerPermissionCaps = Object.freeze(["readOnly", "acceptEdits"]);

const invitationStatuses = Object.freeze(["open", "claimed", "approved", "rejected", "revoked", "expired"]);
const pairingStatuses = Object.freeze(["active", "revoked", "expired"]);
const pairingRoles = Object.freeze(["host", "controller"]);
const invitationTTLChoices = Object.freeze([600, 1800, 3600]);

function textValue(value, fallback = "") {
  const text = String(value ?? "").trim();
  return text || fallback;
}

function formatPeerInstant(value) {
  const text = textValue(value);
  return text ? formatTimestamp(text) : "";
}

function positiveInteger(value) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 1 ? parsed : 0;
}

// Scope order is normalized server-side; matching it here keeps a draft that was
// built by clicking checkboxes comparable with what the server echoes back.
export function normalizePeerScopes(value) {
  const requested = new Set((Array.isArray(value) ? value : []).map((scope) => textValue(scope)));
  return peerScopes.filter((scope) => requested.has(scope));
}

export function normalizePeerGrant(value = {}) {
  const source = objectValue(value);
  const cap = textValue(source.permissionModeCap);
  return {
    id: textValue(source.id),
    pairingId: textValue(source.pairingId),
    projectId: textValue(source.projectId),
    agentId: textValue(source.agentId),
    scopes: normalizePeerScopes(source.scopes),
    permissionModeCap: peerPermissionCaps.includes(cap) ? cap : "readOnly",
    revision: positiveInteger(source.revision),
  };
}

export function normalizePeerInvitation(value = {}) {
  const source = objectValue(value);
  const status = textValue(source.status);
  return {
    id: textValue(source.id),
    status: invitationStatuses.includes(status) ? status : "expired",
    requesterDisplayName: textValue(source.requesterDisplayName),
    requesterInstallationId: textValue(source.requesterInstallationId),
    requesterFingerprint: textValue(source.requesterFingerprint),
    failedAttempts: Number.isSafeInteger(Number(source.failedAttempts)) ? Number(source.failedAttempts) : 0,
    lockedUntil: textValue(source.lockedUntil),
    expiresAt: textValue(source.expiresAt),
    revision: positiveInteger(source.revision),
    createdAt: textValue(source.createdAt),
    updatedAt: textValue(source.updatedAt),
  };
}

// The server wraps a pairing as { pairing, grants } while the normalized form is
// flat, and normalized state is re-read on every render. Accepting both shapes
// keeps a second pass from emptying the record it already produced.
export function normalizePeerPairing(value = {}) {
  const source = objectValue(value);
  const wrapped = source.pairing && typeof source.pairing === "object" && !Array.isArray(source.pairing);
  const pairing = wrapped ? source.pairing : source;
  const role = textValue(pairing.localRole);
  const status = textValue(pairing.status);
  return {
    id: textValue(pairing.id),
    localRole: pairingRoles.includes(role) ? role : "host",
    displayName: textValue(pairing.displayName),
    peerInstallationId: textValue(pairing.peerInstallationId),
    peerFingerprint: textValue(pairing.peerFingerprint),
    endpointOrigin: textValue(pairing.endpointOrigin),
    status: pairingStatuses.includes(status) ? status : "expired",
    scopes: normalizePeerScopes(pairing.scopes),
    credentialRevision: positiveInteger(pairing.credentialRevision),
    grantRevision: positiveInteger(pairing.grantRevision),
    expiresAt: textValue(pairing.expiresAt),
    lastSeenAt: textValue(pairing.lastSeenAt),
    pairedAt: textValue(pairing.pairedAt),
    grants: (Array.isArray(source.grants) ? source.grants : []).map(normalizePeerGrant),
  };
}

export function normalizePeerCollaboration(value = {}) {
  const source = objectValue(value);
  const identity = objectValue(source.identity);
  const tunnel = objectValue(source.tunnel);
  return {
    available: Boolean(source.available),
    sharingEnabled: Boolean(source.sharingEnabled),
    identity: {
      fingerprint: textValue(identity.fingerprint),
      protocolVersion: positiveInteger(identity.protocolVersion),
    },
    tunnel: {
      status: textValue(tunnel.status, "unavailable"),
      publicUrl: textValue(tunnel.publicUrl),
    },
    invitations: (Array.isArray(source.invitations) ? source.invitations : []).map(normalizePeerInvitation),
    pairings: (Array.isArray(source.pairings) ? source.pairings : []).map(normalizePeerPairing),
  };
}

// The approve and authorization endpoints replace the whole grant set, so a
// draft always carries every grant the pairing should keep, not a delta.
export function authorizationPayload(draft, { revisionKey, revision }) {
  const source = objectValue(draft);
  const scopes = normalizePeerScopes(source.scopes);
  const allowed = new Set(scopes);
  const payload = {
    [revisionKey]: revision,
    scopes,
    grants: (Array.isArray(source.grants) ? source.grants : [])
      .filter((grant) => textValue(grant?.agentId) && textValue(grant?.projectId))
      .map((grant) => ({
        projectId: textValue(grant.projectId),
        agentId: textValue(grant.agentId),
        scopes: normalizePeerScopes(grant.scopes).filter((scope) => allowed.has(scope)),
        permissionModeCap: peerPermissionCaps.includes(grant.permissionModeCap) ? grant.permissionModeCap : "readOnly",
      })),
  };
  const hours = Number(source.expiresInHours);
  if (Number.isFinite(hours) && hours > 0) {
    payload.expiresAt = new Date(Date.now() + Math.round(hours * 3600000)).toISOString();
  }
  return payload;
}

export function createPeerCollaborationSettingsController({
  state,
  request,
  copyText,
  onChange,
  showError,
  showToast,
  confirmAction,
}) {
  // Agents come from the navigation projection because a grant needs the
  // project a conversation belongs to, and the agent record alone does not
  // carry it.
  let agentOptions = [];
  let createdInvitation = null;
  let loadSequence = 0;
  const pendingClaims = new Map();
  const approvalDrafts = new Map();
  const authorizationDrafts = new Map();
  let openApproval = "";
  let openAuthorization = "";
  let pickerEvents = null;
  let floatingMenu = null;

  const rt = (key, params = {}) => t(`peerCollaboration.${key}`, params);

  function status() {
    return normalizePeerCollaboration(state?.peerCollaboration || {});
  }

  function invitation(id) {
    return status().invitations.find((item) => item.id === id) || null;
  }

  function pairing(id) {
    return status().pairings.find((item) => item.id === id) || null;
  }

  function agentLabel(agentId) {
    const option = agentOptions.find((item) => item.agentId === agentId);
    if (!option) return agentId;
    return option.projectName ? `${option.projectName} / ${option.agentTitle}` : option.agentTitle;
  }

  function approvalDraft(id) {
    if (!approvalDrafts.has(id)) {
      approvalDrafts.set(id, { scopes: ["observe"], expiresInHours: 24, grants: [], permissionModeCap: "readOnly" });
    }
    return approvalDrafts.get(id);
  }

  function authorizationDraft(id) {
    if (!authorizationDrafts.has(id)) {
      const current = pairing(id);
      const grants = (current?.grants || []).map((grant) => ({
        projectId: grant.projectId,
        agentId: grant.agentId,
        scopes: [...grant.scopes],
        permissionModeCap: grant.permissionModeCap,
      }));
      authorizationDrafts.set(id, {
        scopes: current ? [...current.scopes] : ["observe"],
        expiresInHours: 0,
        permissionModeCap: grants.find((grant) => peerPermissionCaps.includes(grant.permissionModeCap))?.permissionModeCap || "readOnly",
        grants,
      });
    }
    return authorizationDrafts.get(id);
  }

  function draftFor(kind, id) {
    return kind === "approval" ? approvalDraft(id) : authorizationDraft(id);
  }

  async function load() {
    const sequence = ++loadSequence;
    state.peerCollaborationLoading = true;
    state.peerCollaborationError = "";
    try {
      const [statusResult, navigationResult] = await Promise.allSettled([
        request(endpoint + "/status"),
        request("/api/navigation"),
      ]);
      if (sequence !== loadSequence) return state.peerCollaboration;
      if (statusResult.status === "rejected") throw statusResult.reason;
      state.peerCollaboration = normalizePeerCollaboration(statusResult.value);
      if (navigationResult.status === "fulfilled") {
        agentOptions = collectAgentOptions(navigationResult.value);
      }
      return state.peerCollaboration;
    } catch (err) {
      if (sequence !== loadSequence) throw err;
      state.peerCollaborationError = err?.status === 403 ? rt("localOnlyNotice") : (err?.message || String(err));
      throw err;
    } finally {
      if (sequence === loadSequence) {
        state.peerCollaborationLoading = false;
        onChange?.(state.peerCollaboration);
      }
    }
  }

  async function setSharing(enabled) {
    const result = await request(endpoint + "/sharing", { method: "PUT", body: JSON.stringify({ enabled: Boolean(enabled) }) });
    state.peerCollaboration = normalizePeerCollaboration({ ...status(), sharingEnabled: Boolean(objectValue(result).sharingEnabled) });
    showToast?.(rt(enabled ? "sharingEnabledToast" : "sharingDisabledToast"));
    onChange?.(state.peerCollaboration);
    return state.peerCollaboration.sharingEnabled;
  }

  async function createInvitation(expiresInSeconds) {
    const ttl = invitationTTLChoices.includes(Number(expiresInSeconds)) ? Number(expiresInSeconds) : invitationTTLChoices[0];
    const result = objectValue(await request(endpoint + "/invitations", { method: "POST", body: JSON.stringify({ expiresInSeconds: ttl }) }));
    const invited = normalizePeerInvitation(result.invitation);
    createdInvitation = {
      id: invited.id,
      encodedInvitation: textValue(result.encodedInvitation),
      hostFingerprint: textValue(result.hostFingerprint),
      expiresAt: invited.expiresAt,
    };
    showToast?.(rt("invitationCreatedToast"));
    await load();
    return createdInvitation;
  }

  async function approveInvitation(id) {
    const current = invitation(id);
    if (!current) throw new Error(rt("invitationMissing"));
    const result = await request(`${endpoint}/invitations/${encodeURIComponent(id)}/approve`, {
      method: "POST",
      body: JSON.stringify(authorizationPayload(approvalDraft(id), { revisionKey: "revision", revision: current.revision })),
    });
    approvalDrafts.delete(id);
    if (openApproval === id) openApproval = "";
    showToast?.(rt("pairingApprovedToast"));
    await load();
    return normalizePeerPairing(result);
  }

  async function transitionInvitation(id, action) {
    const current = invitation(id);
    if (!current) throw new Error(rt("invitationMissing"));
    await request(`${endpoint}/invitations/${encodeURIComponent(id)}/${action}`, {
      method: "POST",
      body: JSON.stringify({ status: current.status, revision: current.revision }),
    });
    approvalDrafts.delete(id);
    if (openApproval === id) openApproval = "";
    showToast?.(rt(action === "reject" ? "invitationRejectedToast" : "invitationRevokedToast"));
    await load();
  }

  async function deleteInvitation(id) {
    const current = invitation(id);
    if (!current) throw new Error(rt("invitationMissing"));
    await request(`${endpoint}/invitations/${encodeURIComponent(id)}/delete`, {
      method: "POST",
      body: JSON.stringify({ status: current.status, revision: current.revision }),
    });
    approvalDrafts.delete(id);
    if (openApproval === id) openApproval = "";
    showToast?.(rt("invitationDeletedToast"));
    await load();
  }

  async function saveAuthorization(id) {
    const current = pairing(id);
    if (!current) throw new Error(rt("pairingMissing"));
    const result = await request(`${endpoint}/pairings/${encodeURIComponent(id)}/authorization`, {
      method: "PUT",
      body: JSON.stringify(authorizationPayload(authorizationDraft(id), { revisionKey: "grantRevision", revision: current.grantRevision })),
    });
    authorizationDrafts.delete(id);
    if (openAuthorization === id) openAuthorization = "";
    showToast?.(rt("authorizationSavedToast"));
    await load();
    return normalizePeerPairing(result);
  }

  async function revokePairing(id) {
    const current = pairing(id);
    if (!current) throw new Error(rt("pairingMissing"));
    await request(`${endpoint}/pairings/${encodeURIComponent(id)}/revoke`, {
      method: "POST",
      body: JSON.stringify({ status: current.status, credentialRevision: current.credentialRevision }),
    });
    authorizationDrafts.delete(id);
    if (openAuthorization === id) openAuthorization = "";
    showToast?.(rt("pairingRevokedToast"));
    await load();
  }

  async function deletePairing(id) {
    const current = pairing(id);
    if (!current) throw new Error(rt("pairingMissing"));
    await request(`${endpoint}/pairings/${encodeURIComponent(id)}/delete`, {
      method: "POST",
      body: JSON.stringify({ status: current.status, credentialRevision: current.credentialRevision }),
    });
    authorizationDrafts.delete(id);
    if (openAuthorization === id) openAuthorization = "";
    showToast?.(rt("pairingDeletedToast"));
    await load();
  }

  async function connectPeer(invitationCode, displayName) {
    const code = textValue(invitationCode);
    if (!code) throw new Error(rt("connectCodeRequired"));
    let result;
    try {
      result = objectValue(await request(endpoint + "/connect", {
        method: "POST",
        body: JSON.stringify({ invitation: code, displayName: textValue(displayName, "Autoto") }),
      }));
    } catch (err) {
      if (Number(err?.status) === 409) throw new Error(rt("connectConflict"));
      throw err;
    }
    const claim = objectValue(result.claim);
    const claimId = textValue(claim.invitationId);
    const claimStatus = textValue(claim.status, "claimed");
    if (claimId) {
      pendingClaims.set(claimId, {
        origin: textValue(result.origin),
        hostFingerprint: textValue(result.hostFingerprint),
        status: claimStatus,
      });
    }
    if (claimStatus === "approved" && claimId) {
      await pollClaim(claimId);
      return claimId;
    }
    showToast?.(rt("claimSentToast"));
    onChange?.(state.peerCollaboration);
    return claimId;
  }

  async function pollClaim(id) {
    const result = objectValue(await request(`${endpoint}/claims/${encodeURIComponent(id)}/poll`, { method: "POST" }));
    const claimStatus = textValue(result.status, "claimed");
    const pending = pendingClaims.get(id);
    if (pending) pendingClaims.set(id, { ...pending, status: claimStatus });
    if (claimStatus === "approved") {
      pendingClaims.delete(id);
      showToast?.(rt("claimApprovedToast"));
      await load();
      return claimStatus;
    }
    if (["rejected", "revoked", "expired"].includes(claimStatus)) {
      pendingClaims.delete(id);
      showToast?.(rt("claimClosedToast"));
    }
    onChange?.(state.peerCollaboration);
    return claimStatus;
  }

  function scopeLabel(scope) {
    return rt(`scope.${scope}`);
  }

  function renderScopeChecks(kind, id, selected, available = peerScopes) {
    const chosen = new Set(selected);
    return available.map((scope) => `
      <label class="settings-check-row peer-collaboration-scope">
        <input type="checkbox" data-peer-scope="${escapeAttr(scope)}" data-peer-draft-kind="${escapeAttr(kind)}" data-peer-draft-id="${escapeAttr(id)}" ${chosen.has(scope) ? "checked" : ""} />
        <span><strong>${escapeHtml(scopeLabel(scope))}</strong><small>${escapeHtml(rt(`scopeHint.${scope}`))}</small></span>
      </label>`).join("");
  }

  function renderProjectGrantPicker(kind, id, draft) {
    const projects = collectProjectGrantOptions(agentOptions);
    if (!agentOptions.length) {
      return `<p class="settings-card-description">${escapeHtml(rt("agentsUnavailable"))}</p>`;
    }
    if (!projects.length) {
      return `<p class="settings-card-description">${escapeHtml(rt("grantsEmpty"))}</p>`;
    }
    const granted = new Set(draft.grants.map((grant) => grant.agentId));
    const selectedCount = projects.filter((project) => project.agents.some((agent) => granted.has(agent.agentId))).length;
    const cap = peerPermissionCaps.includes(draft.permissionModeCap) ? draft.permissionModeCap : "readOnly";
    return `
      <div class="user-admin-project-picker peer-collaboration-project-picker" data-peer-project-picker>
        <button type="button" class="user-admin-project-trigger" data-peer-project-picker-toggle aria-expanded="false" aria-haspopup="listbox">
          <span class="user-admin-project-trigger-copy">
            <strong>${escapeHtml(rt("grants"))}</strong>
            <small data-peer-project-picker-summary>${escapeHtml(rt("projectGrantSummary", { count: selectedCount }))}</small>
          </span>
          <span class="composer-select-chevron" aria-hidden="true">▾</span>
        </button>
        <div class="user-admin-project-checks" hidden>
          ${projects.map((project) => {
            const selected = project.agents.some((agent) => granted.has(agent.agentId));
            return `
              <label>
                <input type="checkbox" data-peer-project-grant="${escapeAttr(project.projectId)}" data-peer-project-name="${escapeAttr(project.projectName)}" data-peer-draft-kind="${escapeAttr(kind)}" data-peer-draft-id="${escapeAttr(id)}" ${selected ? "checked" : ""} />
                <span>${escapeHtml(project.projectName)}</span>
              </label>`;
          }).join("")}
        </div>
      </div>
      <label class="settings-form-field">${escapeHtml(rt("permissionModeCap"))}
        <select class="settings-field" data-peer-grant-cap-all="1" data-peer-draft-kind="${escapeAttr(kind)}" data-peer-draft-id="${escapeAttr(id)}">
          ${peerPermissionCaps.map((value) => `<option value="${escapeAttr(value)}" ${cap === value ? "selected" : ""}>${escapeHtml(rt(`cap.${value}`))}</option>`).join("")}
        </select>
      </label>`;
  }

  function renderAuthorizationEditor(kind, id, draft, { submitAction, submitLabel }) {
    return `
      <div class="peer-collaboration-editor settings-card-content" data-peer-editor="${escapeAttr(kind)}-${escapeAttr(id)}">
        <div class="peer-collaboration-editor-block">
          <strong>${escapeHtml(rt("pairingScopes"))}</strong>
          <p class="settings-card-description">${escapeHtml(rt("pairingScopesHint"))}</p>
          <div class="peer-collaboration-scope-grid">${renderScopeChecks(kind, id, draft.scopes)}</div>
        </div>
        <label class="settings-form-field">${escapeHtml(rt("expiresInHours"))}
          <input class="settings-field" type="number" min="0" step="1" value="${escapeAttr(String(draft.expiresInHours || 0))}" data-peer-expiry="1" data-peer-draft-kind="${escapeAttr(kind)}" data-peer-draft-id="${escapeAttr(id)}" />
          <small>${escapeHtml(rt("expiresInHoursHint"))}</small>
        </label>
        <div class="peer-collaboration-editor-block">
          <strong>${escapeHtml(rt("grants"))}</strong>
          <p class="settings-card-description">${escapeHtml(rt("grantsHint"))}</p>
          ${renderProjectGrantPicker(kind, id, draft)}
        </div>
        <div class="settings-action-row settings-card-footer">
          <button class="settings-action-btn subtle" type="button" data-peer-action="${escapeAttr(kind === "approval" ? "close-approve" : "close-auth")}" data-peer-id="${escapeAttr(id)}">${escapeHtml(rt("cancel"))}</button>
          <button class="settings-action-btn primary" type="button" data-peer-action="${escapeAttr(submitAction)}" data-peer-id="${escapeAttr(id)}" ${draft.scopes.length ? "" : "disabled"}>${escapeHtml(submitLabel)}</button>
        </div>
      </div>`;
  }

  function renderCreatedInvitation() {
    if (!createdInvitation?.encodedInvitation) return "";
    let qr = "";
    try {
      qr = `<div class="peer-collaboration-qr">${qrToSvg(createdInvitation.encodedInvitation, { size: 200 })}</div>`;
    } catch {
      qr = `<p class="settings-card-description">${escapeHtml(rt("qrUnavailable"))}</p>`;
    }
    return `
      <div class="peer-collaboration-created peer-collaboration-item" role="status">
        <strong>${escapeHtml(rt("invitationReady"))}</strong>
        <p class="settings-card-description">${escapeHtml(rt("invitationReadyHint", { expiresAt: formatPeerInstant(createdInvitation.expiresAt) || createdInvitation.expiresAt || "" }))}</p>
        ${qr}
        <code class="peer-collaboration-code">${escapeHtml(createdInvitation.encodedInvitation)}</code>
        <div class="settings-action-row">
          <span class="settings-provider-meta">${escapeHtml(rt("hostFingerprint"))}: ${escapeHtml(createdInvitation.hostFingerprint)}</span>
          <button class="settings-action-btn subtle" type="button" data-peer-action="copy-invitation">${escapeHtml(rt("copyInvitation"))}</button>
        </div>
      </div>`;
  }

  function renderInvitation(item) {
    const actionable = item.status === "open" || item.status === "claimed";
    const approvable = item.status === "claimed";
    const inactive = item.status === "rejected" || item.status === "revoked" || item.status === "expired";
    const editing = openApproval === item.id;
    return `
      <div class="peer-collaboration-invitation peer-collaboration-item" data-peer-invitation="${escapeAttr(item.id)}">
        <div class="settings-provider-section-head settings-card-header">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(item.requesterDisplayName || rt("awaitingRequester"))}</div>
            <div class="settings-provider-meta settings-card-description">${escapeHtml(rt("invitationMeta", {
              fingerprint: item.requesterFingerprint ? item.requesterFingerprint.slice(0, 12) : "—",
              expiresAt: formatPeerInstant(item.expiresAt) || item.expiresAt || "—",
            }))}</div>
          </div>
          <div class="peer-collaboration-item-tools">
            <span class="settings-status-pill settings-badge ${item.status === "approved" ? "ok" : actionable ? "" : "warn"}">${escapeHtml(rt(`invitationStatus.${item.status}`))}</span>
            ${inactive ? `<button class="settings-action-btn danger" type="button" data-peer-action="delete-invitation" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("deleteInvitation"))}</button>` : ""}
          </div>
        </div>
        ${item.failedAttempts ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("failedAttempts", { count: String(item.failedAttempts) }))}</div>` : ""}
        ${item.lockedUntil ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("lockedUntil", { lockedUntil: item.lockedUntil }))}</div>` : ""}
        ${editing ? renderAuthorizationEditor("approval", item.id, approvalDraft(item.id), { submitAction: "approve", submitLabel: rt("approvePairing") }) : ""}
        <div class="settings-action-row settings-card-footer">
          <span class="settings-provider-meta">${escapeHtml(approvable ? rt("claimedHint") : inactive ? rt("inactiveInvitationHint") : rt("openHint"))}</span>
          ${approvable && !editing ? `<button class="settings-action-btn primary" type="button" data-peer-action="open-approve" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("reviewClaim"))}</button>` : ""}
          ${approvable ? `<button class="settings-action-btn subtle" type="button" data-peer-action="reject" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("rejectClaim"))}</button>` : ""}
          ${actionable ? `<button class="settings-action-btn subtle" type="button" data-peer-action="revoke-invitation" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("revokeInvitation"))}</button>` : ""}
        </div>
      </div>`;
  }

  function renderPairing(item) {
    const editing = openAuthorization === item.id;
    const active = item.status === "active";
    const grants = item.localRole === "controller"
      ? `<p class="settings-card-description">${escapeHtml(rt("controllerGrantsHint"))}</p>
         <div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("controllerReadHint"))}</div>`
      : item.grants.length
        ? `<ul class="peer-collaboration-grant-list">${item.grants.map((grant) => `<li>${escapeHtml(agentLabel(grant.agentId))} — ${escapeHtml(rt(`cap.${grant.permissionModeCap}`))}${grant.scopes.length ? ` · ${escapeHtml(grant.scopes.map(scopeLabel).join(", "))}` : ""}</li>`).join("")}</ul>`
        : `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("pairingNeedsGrants"))}</div>`;
    return `
      <div class="peer-collaboration-pairing peer-collaboration-item" data-peer-pairing="${escapeAttr(item.id)}">
        <div class="settings-provider-section-head settings-card-header">
          <div>
            <div class="settings-provider-title settings-card-title">${escapeHtml(item.displayName || item.id)}</div>
            <div class="settings-provider-meta settings-card-description">${escapeHtml(rt("pairingMeta", {
              role: rt(`role.${item.localRole}`),
              fingerprint: item.peerFingerprint ? item.peerFingerprint.slice(0, 12) : "—",
              lastSeenAt: formatPeerInstant(item.lastSeenAt) || rt("neverSeen"),
            }))}</div>
          </div>
          <div class="peer-collaboration-item-tools">
            <span class="settings-status-pill settings-badge ${active ? "ok" : "warn"}">${escapeHtml(rt(`pairingStatus.${item.status}`))}</span>
            ${active ? "" : `<button class="settings-action-btn danger" type="button" data-peer-action="delete-pairing" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("deletePairing"))}</button>`}
          </div>
        </div>
        <div class="settings-stat-grid peer-collaboration-pairing-stats">
          <div class="settings-stat-card"><strong>${escapeHtml(item.scopes.length ? item.scopes.map(scopeLabel).join(", ") : rt("noScopes"))}</strong><span>${escapeHtml(rt("pairingScopes"))}</span></div>
          <div class="settings-stat-card"><strong>${escapeHtml(formatPeerInstant(item.expiresAt) || rt("noExpiry"))}</strong><span>${escapeHtml(rt("expiresAt"))}</span></div>
          ${item.endpointOrigin ? `<div class="settings-stat-card"><strong>${escapeHtml(item.endpointOrigin)}</strong><span>${escapeHtml(rt("endpointOrigin"))}</span></div>` : ""}
        </div>
        ${grants}
        ${editing ? renderAuthorizationEditor("authorization", item.id, authorizationDraft(item.id), { submitAction: "save-auth", submitLabel: rt("saveAuthorization") }) : ""}
        <div class="peer-collaboration-item-foot">
          <span class="peer-collaboration-item-hint">${escapeHtml(active ? (item.localRole === "host" ? rt("hostPairingHint") : rt("controllerPairingHint")) : rt("inactivePairingHint"))}</span>
          ${active && item.localRole === "host" && !editing ? `<button class="settings-action-btn primary" type="button" data-peer-action="open-auth" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("editAuthorization"))}</button>` : ""}
          ${active ? `<button class="settings-action-btn subtle" type="button" data-peer-action="revoke-pairing" data-peer-id="${escapeAttr(item.id)}">${escapeHtml(rt("revokePairing"))}</button>` : ""}
        </div>
      </div>`;
  }

  function renderPendingClaims() {
    if (!pendingClaims.size) return "";
    return `<div class="peer-collaboration-stack-list">${Array.from(pendingClaims.entries()).map(([id, claim]) => `
      <div class="peer-collaboration-claim peer-collaboration-item" role="status">
        <strong>${escapeHtml(rt("claimPending"))}</strong>
        <p class="settings-card-description">${escapeHtml(rt("claimPendingHint", {
          origin: claim.origin,
          fingerprint: claim.hostFingerprint ? claim.hostFingerprint.slice(0, 12) : "—",
          status: rt(`invitationStatus.${invitationStatuses.includes(claim.status) ? claim.status : "claimed"}`),
        }))}</p>
        <div class="settings-action-row">
          <button class="settings-action-btn primary" type="button" data-peer-action="poll-claim" data-peer-id="${escapeAttr(id)}">${escapeHtml(rt("pollClaim"))}</button>
        </div>
      </div>`).join("")}</div>`;
  }

  function render() {
    if (!state?.peerCollaboration && state?.peerCollaborationLoading) {
      return `<div class="settings-empty-card settings-empty-state">${escapeHtml(rt("loading"))}</div>`;
    }
    const value = status();
    const tunnelReady = value.tunnel.status === "running" && Boolean(value.tunnel.publicUrl);
    const invitations = value.invitations.filter((item) => item.status !== "approved");
    const hostPairings = value.pairings.filter((item) => item.localRole === "host");
    const controllerPairings = value.pairings.filter((item) => item.localRole === "controller");
    const sharingOffBlocks = !value.sharingEnabled && hostPairings.some((item) => item.status === "active");
    return `
      <div class="settings-live-page peer-collaboration-page" id="peerCollaborationPage">
        <section class="settings-hero-card settings-page-section settings-card peer-collaboration-hero">
          <div class="settings-card-header peer-collaboration-hero-head">
            <div>
              <div class="settings-hero-title settings-card-title">${escapeHtml(rt("title"))}</div>
              <p class="settings-card-description" data-settings-help-copy>${escapeHtml(rt("description"))}</p>
            </div>
            <div class="peer-collaboration-hero-tools">
              <span class="settings-status-pill settings-badge ${value.sharingEnabled ? "ok" : "warn"}">${escapeHtml(rt(value.sharingEnabled ? "sharingOn" : "sharingOff"))}</span>
              <button class="settings-action-btn subtle" type="button" data-peer-action="refresh">${escapeHtml(rt("refresh"))}</button>
            </div>
          </div>
          <div class="peer-collaboration-stack">
            <div class="peer-collaboration-sharing-row${value.sharingEnabled ? " is-on" : ""}" data-peer-sharing-card>
              <div class="peer-collaboration-sharing-copy">
                <strong>${escapeHtml(rt("enableSharing"))}</strong>
                <small data-settings-help-copy>${escapeHtml(rt("enableSharingHint"))}</small>
              </div>
              <label class="remote-access-switch" title="${escapeAttr(rt("enableSharing"))}">
                <input id="peerCollaborationSharing" type="checkbox" role="switch" aria-checked="${value.sharingEnabled ? "true" : "false"}" ${value.sharingEnabled ? "checked" : ""} ${value.available ? "" : "disabled"} />
                <span class="remote-access-switch-track" aria-hidden="true"></span>
              </label>
            </div>
            ${sharingOffBlocks ? `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("sharingOffBlocksPeers"))}</div>` : ""}
            <dl class="peer-collaboration-meta">
              <div class="peer-collaboration-meta-row">
                <dt>${escapeHtml(rt("identityFingerprint"))}</dt>
                <dd>
                  <code class="peer-collaboration-fingerprint">${escapeHtml(value.identity.fingerprint || "—")}</code>
                  <button class="settings-action-btn subtle" type="button" data-peer-action="copy-fingerprint"${value.identity.fingerprint ? "" : " disabled"}>${escapeHtml(rt("copyFingerprint"))}</button>
                </dd>
              </div>
              <div class="peer-collaboration-meta-row">
                <dt>${escapeHtml(rt("invitationOrigin"))}</dt>
                <dd class="${tunnelReady ? "" : "is-missing"}">${tunnelReady ? `<code>${escapeHtml(value.tunnel.publicUrl)}</code>` : escapeHtml(rt("tunnelMissing"))}</dd>
              </div>
            </dl>
          </div>
        </section>
        ${state?.peerCollaborationError ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(state.peerCollaborationError)}</div>` : ""}
        ${value.available ? "" : `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("unavailableNotice"))}</div>`}
        <section class="settings-provider-section settings-page-section settings-card" data-peer-lane="host">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <p class="peer-collaboration-lane-kicker">${escapeHtml(rt("hostLaneKicker"))}</p>
              <div class="settings-provider-title settings-card-title">${escapeHtml(rt("inviteTitle"))}</div>
              <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(rt("inviteDescription"))}</div>
            </div>
          </div>
          ${tunnelReady ? "" : `<div class="settings-inline-alert settings-alert" role="status">${escapeHtml(rt("tunnelRequired"))}</div>`}
          <form id="peerCollaborationInviteForm" class="settings-card-content peer-collaboration-invite-form">
            <label class="settings-form-field">${escapeHtml(rt("invitationLifetime"))}
              <select id="peerCollaborationInviteTTL" class="settings-field">
                ${invitationTTLChoices.map((seconds) => `<option value="${seconds}">${escapeHtml(rt(`ttl.${seconds}`))}</option>`).join("")}
              </select>
            </label>
            <button class="settings-action-btn primary" type="submit" data-peer-invite-submit ${value.sharingEnabled && tunnelReady ? "" : "disabled"}>${escapeHtml(rt("createInvitation"))}</button>
          </form>
          ${renderCreatedInvitation()}
        </section>
        <section class="settings-provider-section settings-page-section settings-card" data-peer-lane="host">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <div class="settings-provider-title settings-card-title">${escapeHtml(rt("invitationsTitle"))}</div>
              <div class="settings-provider-meta settings-card-description">${escapeHtml(rt("invitationsDescription"))}</div>
            </div>
          </div>
          ${invitations.length ? `<div class="peer-collaboration-stack-list">${invitations.map(renderInvitation).join("")}</div>` : `<p class="settings-card-description peer-collaboration-empty">${escapeHtml(rt("invitationsEmpty"))}</p>`}
        </section>
        <section class="settings-provider-section settings-page-section settings-card" data-peer-lane="host">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <div class="settings-provider-title settings-card-title">${escapeHtml(rt("hostPairingsTitle"))}</div>
              <div class="settings-provider-meta settings-card-description">${escapeHtml(rt("hostPairingsDescription"))}</div>
            </div>
          </div>
          ${hostPairings.length ? `<div class="peer-collaboration-stack-list">${hostPairings.map(renderPairing).join("")}</div>` : `<p class="settings-card-description peer-collaboration-empty">${escapeHtml(rt("hostPairingsEmpty"))}</p>`}
        </section>
        <section class="settings-provider-section settings-page-section settings-card" data-peer-lane="controller">
          <div class="settings-provider-section-head settings-card-header">
            <div>
              <p class="peer-collaboration-lane-kicker">${escapeHtml(rt("controllerLaneKicker"))}</p>
              <div class="settings-provider-title settings-card-title">${escapeHtml(rt("connectTitle"))}</div>
              <div class="settings-provider-meta settings-card-description" data-settings-help-copy>${escapeHtml(rt("connectDescription"))}</div>
            </div>
          </div>
          <form id="peerCollaborationConnectForm" class="settings-card-content peer-collaboration-connect-form">
            <label class="settings-form-field peer-collaboration-connect-code">${escapeHtml(rt("invitationCode"))}
              <input id="peerCollaborationConnectCode" class="settings-field" type="text" autocomplete="off" spellcheck="false" placeholder="${escapeAttr(rt("invitationCodePlaceholder"))}" />
            </label>
            <div class="peer-collaboration-connect-actions">
              <label class="settings-form-field">${escapeHtml(rt("controllerDisplayName"))}
                <input id="peerCollaborationConnectName" class="settings-field" type="text" autocomplete="off" placeholder="${escapeAttr(rt("controllerDisplayNamePlaceholder"))}" />
              </label>
              <button class="settings-action-btn primary" type="submit" data-peer-connect-submit ${value.available ? "" : "disabled"}>${escapeHtml(rt("connect"))}</button>
            </div>
          </form>
          ${renderPendingClaims()}
          ${controllerPairings.length ? `<div class="peer-collaboration-stack-list">${controllerPairings.map(renderPairing).join("")}</div>` : `<p class="settings-card-description peer-collaboration-empty">${escapeHtml(rt("controllerPairingsEmpty"))}</p>`}
        </section>
      </div>`;
  }

  function updateDraftScope(kind, id, scope, checked) {
    const draft = draftFor(kind, id);
    const scopes = new Set(draft.scopes);
    if (checked) scopes.add(scope);
    else scopes.delete(scope);
    draft.scopes = normalizePeerScopes(Array.from(scopes));
    const allowed = new Set(draft.scopes);
    draft.grants = draft.grants.map((grant) => ({ ...grant, scopes: grant.scopes.filter((value) => allowed.has(value)) }));
    syncEditorSubmit(kind, id);
  }

  function updateGrantScope(kind, id, index, scope, checked) {
    const draft = draftFor(kind, id);
    const grant = draft.grants[index];
    if (!grant) return;
    const scopes = new Set(grant.scopes);
    if (checked) scopes.add(scope);
    else scopes.delete(scope);
    grant.scopes = normalizePeerScopes(Array.from(scopes));
  }

  function addGrant(kind, id, agentId) {
    const option = agentOptions.find((item) => item.agentId === agentId);
    if (!option) return;
    const draft = draftFor(kind, id);
    if (draft.grants.some((grant) => grant.agentId === agentId)) return;
    const cap = peerPermissionCaps.includes(draft.permissionModeCap) ? draft.permissionModeCap : "readOnly";
    draft.grants.push({
      projectId: option.projectId,
      agentId: option.agentId,
      scopes: normalizePeerScopes(draft.scopes),
      permissionModeCap: cap,
    });
  }

  function setProjectGranted(kind, id, projectId, checked) {
    const draft = draftFor(kind, id);
    const agents = agentOptions.filter((item) => item.projectId === projectId);
    if (!agents.length) return;
    if (checked) {
      const cap = peerPermissionCaps.includes(draft.permissionModeCap) ? draft.permissionModeCap : "readOnly";
      const scopes = normalizePeerScopes(draft.scopes);
      agents.forEach((option) => {
        if (draft.grants.some((grant) => grant.agentId === option.agentId)) return;
        draft.grants.push({
          projectId: option.projectId,
          agentId: option.agentId,
          scopes: [...scopes],
          permissionModeCap: cap,
        });
      });
    } else {
      draft.grants = draft.grants.filter((grant) => grant.projectId !== projectId);
    }
  }

  function setDraftPermissionCap(kind, id, value) {
    const draft = draftFor(kind, id);
    const cap = peerPermissionCaps.includes(value) ? value : "readOnly";
    draft.permissionModeCap = cap;
    draft.grants = draft.grants.map((grant) => ({ ...grant, permissionModeCap: cap }));
  }

  function removeGrant(kind, id, index) {
    const draft = draftFor(kind, id);
    if (index < 0 || index >= draft.grants.length) return;
    draft.grants.splice(index, 1);
  }

  function syncEditorSubmit(kind, id) {
    const action = kind === "approval" ? "approve" : "save-auth";
    const editor = document.querySelector?.(`[data-peer-editor="${kind}-${id}"]`);
    const button = editor?.querySelector?.(`[data-peer-action="${action}"]`);
    if (button) button.disabled = !draftFor(kind, id).scopes.length;
  }

  function syncProjectGrantSummary(picker) {
    const summary = picker?.querySelector?.("[data-peer-project-picker-summary]");
    if (!summary) return;
    summary.textContent = rt("projectGrantSummary", { count: picker.querySelectorAll("[data-peer-project-grant]:checked").length });
  }

  function closeProjectPickers() {
    document.querySelectorAll(".peer-collaboration-project-picker.is-open").forEach((picker) => {
      picker.classList.remove("is-open");
      picker.querySelector("[data-peer-project-picker-toggle]")?.setAttribute("aria-expanded", "false");
    });
    floatingMenu?.remove();
    floatingMenu = null;
  }

  function openProjectPicker(picker) {
    closeProjectPickers();
    const toggle = picker.querySelector("[data-peer-project-picker-toggle]");
    picker.classList.add("is-open");
    toggle?.setAttribute("aria-expanded", "true");
    const menu = document.createElement("div");
    menu.id = "peerCollaborationProjectMenu";
    menu.className = "composer-select-popover user-admin-project-menu";
    menu.setAttribute("role", "listbox");
    menu.setAttribute("aria-multiselectable", "true");
    picker.querySelectorAll("[data-peer-project-grant]").forEach((input) => {
      const option = document.createElement("button");
      option.type = "button";
      option.className = "composer-select-option";
      option.setAttribute("role", "option");
      option.setAttribute("aria-selected", input.checked ? "true" : "false");
      const label = document.createElement("span");
      label.textContent = input.dataset.peerProjectName || input.dataset.peerProjectGrant || "";
      const check = document.createElement("span");
      check.className = "composer-select-option-check";
      check.setAttribute("aria-hidden", "true");
      check.textContent = input.checked ? "✓" : "";
      option.append(label, check);
      option.addEventListener("click", (event) => {
        event.preventDefault();
        event.stopPropagation();
        input.checked = !input.checked;
        input.dispatchEvent(new Event("change", { bubbles: true }));
        option.setAttribute("aria-selected", input.checked ? "true" : "false");
        check.textContent = input.checked ? "✓" : "";
      });
      menu.appendChild(option);
    });
    document.body.appendChild(menu);
    floatingMenu = menu;
    const rect = toggle?.getBoundingClientRect?.();
    if (!rect) return;
    const viewportWidth = globalThis.innerWidth || document.documentElement.clientWidth || 0;
    const width = Math.min(Math.max(rect.width, 260), Math.max(160, viewportWidth - 16));
    menu.style.left = `${Math.min(Math.max(8, rect.left), Math.max(8, viewportWidth - width - 8))}px`;
    menu.style.width = `${width}px`;
    menu.style.top = `${rect.bottom + 6}px`;
  }

  async function runAction(button, action, id) {
    switch (action) {
      case "refresh":
        await load();
        showToast?.(rt("refreshed"));
        return;
      case "copy-fingerprint": {
        const fingerprint = status().identity.fingerprint;
        if (!fingerprint) return;
        await copyText?.(fingerprint);
        showToast?.(rt("fingerprintCopied"));
        return;
      }
      case "copy-invitation": {
        if (!createdInvitation?.encodedInvitation) return;
        await copyText?.(createdInvitation.encodedInvitation);
        showToast?.(rt("invitationCopied"));
        return;
      }
      case "open-approve":
        openApproval = id;
        onChange?.(state.peerCollaboration);
        return;
      case "close-approve":
        openApproval = "";
        onChange?.(state.peerCollaboration);
        return;
      case "approve":
        await approveInvitation(id);
        return;
      case "reject":
        await transitionInvitation(id, "reject");
        return;
      case "revoke-invitation":
        await transitionInvitation(id, "revoke");
        return;
      case "delete-invitation": {
        const confirmed = confirmAction ? await confirmAction(rt("deleteInvitationConfirm")) : true;
        if (!confirmed) return;
        await deleteInvitation(id);
        return;
      }
      case "open-auth":
        openAuthorization = id;
        onChange?.(state.peerCollaboration);
        return;
      case "close-auth":
        openAuthorization = "";
        onChange?.(state.peerCollaboration);
        return;
      case "save-auth":
        await saveAuthorization(id);
        return;
      case "revoke-pairing": {
        const confirmed = confirmAction ? await confirmAction(rt("revokePairingConfirm")) : true;
        if (!confirmed) return;
        await revokePairing(id);
        return;
      }
      case "delete-pairing": {
        const confirmed = confirmAction ? await confirmAction(rt("deletePairingConfirm")) : true;
        if (!confirmed) return;
        await deletePairing(id);
        return;
      }
      case "poll-claim":
        await pollClaim(id);
        return;
      case "remove-grant":
        removeGrant(button.dataset.peerDraftKind, button.dataset.peerDraftId, Number(button.dataset.peerGrantIndex));
        return;
      default:
        return;
    }
  }

  function bind() {
    pickerEvents?.abort();
    closeProjectPickers();
    pickerEvents = new AbortController();
    const { signal } = pickerEvents;
    const root = document.getElementById("peerCollaborationPage");
    if (!root) {
      // The loading placeholder has no controls, so the first paint still has to
      // start the fetch that replaces it.
      if (!state?.peerCollaboration && !state?.peerCollaborationLoading) void load().catch(() => {});
      return;
    }
    if (!state?.peerCollaboration && !state?.peerCollaborationLoading) void load().catch(() => {});
    root.addEventListener("click", async (event) => {
      const toggle = event.target?.closest?.("[data-peer-project-picker-toggle]");
      if (toggle && root.contains(toggle)) {
        event.preventDefault();
        const picker = toggle.closest("[data-peer-project-picker]");
        if (!picker) return;
        if (picker.classList.contains("is-open")) closeProjectPickers();
        else openProjectPicker(picker);
        return;
      }
      const button = event.target?.closest?.("[data-peer-action]");
      if (!button || !root.contains(button)) return;
      const action = button.dataset.peerAction;
      const id = button.dataset.peerId || "";
      setButtonBusy(button, true, rt("working"));
      try {
        await runAction(button, action, id);
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    }, { signal });
    root.addEventListener("change", (event) => {
      const target = event.target;
      if (!target?.dataset) return;
      const kind = target.dataset.peerDraftKind;
      const id = target.dataset.peerDraftId;
      if (target.id === "peerCollaborationSharing") return;
      if (kind && id && target.dataset.peerScope) {
        updateDraftScope(kind, id, target.dataset.peerScope, Boolean(target.checked));
        return;
      }
      if (kind && id && target.dataset.peerGrantScope) {
        updateGrantScope(kind, id, Number(target.dataset.peerGrantIndex), target.dataset.peerGrantScope, Boolean(target.checked));
        return;
      }
      if (kind && id && target.dataset.peerProjectGrant) {
        setProjectGranted(kind, id, target.dataset.peerProjectGrant, Boolean(target.checked));
        syncProjectGrantSummary(target.closest("[data-peer-project-picker]"));
        return;
      }
      if (kind && id && target.dataset.peerGrantCapAll) {
        setDraftPermissionCap(kind, id, target.value);
        return;
      }
      if (kind && id && target.dataset.peerGrantCap) {
        const draft = draftFor(kind, id);
        const grant = draft.grants[Number(target.dataset.peerGrantCap)];
        if (grant) grant.permissionModeCap = peerPermissionCaps.includes(target.value) ? target.value : "readOnly";
        return;
      }
      if (kind && id && target.dataset.peerExpiry) {
        const draft = draftFor(kind, id);
        const hours = Number(target.value);
        draft.expiresInHours = Number.isFinite(hours) && hours > 0 ? hours : 0;
        return;
      }
      if (kind && id && target.dataset.peerAddGrant) {
        const agentId = String(target.value || "");
        target.value = "";
        if (agentId) addGrant(kind, id, agentId);
      }
    }, { signal });
    document.addEventListener("pointerdown", (event) => {
      const node = event.target instanceof Element ? event.target : event.target?.parentElement;
      if (node?.closest?.(".peer-collaboration-project-picker, .user-admin-project-menu")) return;
      closeProjectPickers();
    }, { signal });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape") closeProjectPickers();
    }, { signal });
    document.getElementById("peerCollaborationSharing")?.addEventListener("change", async (event) => {
      const input = event.currentTarget;
      const enabled = Boolean(input.checked);
      const card = input.closest("[data-peer-sharing-card]");
      input.setAttribute("aria-checked", enabled ? "true" : "false");
      card?.classList.toggle("is-on", enabled);
      input.disabled = true;
      try {
        await setSharing(enabled);
      } catch (err) {
        input.checked = !enabled;
        input.setAttribute("aria-checked", !enabled ? "true" : "false");
        card?.classList.toggle("is-on", !enabled);
        showError?.(err);
      } finally {
        input.disabled = false;
      }
    }, { signal });
    document.getElementById("peerCollaborationInviteForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.currentTarget.querySelector("[data-peer-invite-submit]");
      setButtonBusy(button, true, rt("working"));
      try {
        await createInvitation(document.getElementById("peerCollaborationInviteTTL")?.value);
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    }, { signal });
    document.getElementById("peerCollaborationConnectForm")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = event.currentTarget.querySelector("[data-peer-connect-submit]");
      setButtonBusy(button, true, rt("working"));
      try {
        await connectPeer(
          document.getElementById("peerCollaborationConnectCode")?.value,
          document.getElementById("peerCollaborationConnectName")?.value,
        );
      } catch (err) {
        showError?.(err);
      } finally {
        setButtonBusy(button, false);
      }
    }, { signal });
  }

  return {
    addGrant,
    setProjectGranted,
    approveInvitation,
    bind,
    connectPeer,
    createInvitation,
    load,
    pollClaim,
    render,
    reviewInvitation(id) {
      openApproval = String(id || "");
      onChange?.(state.peerCollaboration);
    },
    revokePairing,
    deletePairing,
    deleteInvitation,
    saveAuthorization,
    setSharing,
    transitionInvitation,
  };
}

// Grants are keyed by project and agent together, and the navigation projection
// is the only response that pairs them.
export function collectAgentOptions(navigation = {}) {
  const source = objectValue(navigation);
  const conversations = Array.isArray(source.conversations) ? source.conversations : [];
  const seen = new Set();
  const options = [];
  conversations.forEach((entry) => {
    const value = objectValue(entry);
    const agentId = textValue(value.agentId);
    const projectId = textValue(value.projectId);
    if (!agentId || !projectId || seen.has(agentId)) return;
    if (textValue(value.agentArchivedAt)) return;
    seen.add(agentId);
    options.push({
      agentId,
      projectId,
      agentTitle: textValue(value.agentTitle, agentId),
      projectName: textValue(value.projectName),
    });
  });
  return options.slice(0, 500);
}

export function collectProjectGrantOptions(agents = []) {
  const map = new Map();
  (Array.isArray(agents) ? agents : []).forEach((option) => {
    const projectId = textValue(option?.projectId);
    const agentId = textValue(option?.agentId);
    if (!projectId || !agentId) return;
    if (!map.has(projectId)) {
      map.set(projectId, {
        projectId,
        projectName: textValue(option.projectName, projectId),
        agents: [],
      });
    }
    map.get(projectId).agents.push(option);
  });
  return [...map.values()];
}
