import { escapeAttr, escapeHtml, setButtonBusy } from "./dom.mjs";
import { formatCompactRelativeTime, navigationAgentStatusClass, navigationStatusLabel } from "./conversation-navigation.mjs";
import { formatTimestamp } from "./formatters.mjs";
import { t } from "./i18n.mjs";
import { normalizePeerPairing, normalizePeerScopes } from "./peer-collaboration-settings.mjs";

const endpoint = "/api/remote-collaboration";
const listIntervalMs = 15000;

export function createPeerTargetId(pairingId, agentId) {
  const pairing = String(pairingId || "").trim();
  const agent = String(agentId || "").trim();
  if (!pairing || !agent) return "";
  return `peer:${encodeURIComponent(pairing)}:${encodeURIComponent(agent)}`;
}

export function parsePeerTargetId(value) {
  const text = String(value || "").trim();
  const match = /^peer:([^:]+):(.+)$/.exec(text);
  if (!match) return null;
  try {
    const pairingId = decodeURIComponent(match[1]);
    const agentId = decodeURIComponent(match[2]);
    if (!pairingId || !agentId) return null;
    return { pairingId, agentId, targetId: createPeerTargetId(pairingId, agentId) };
  } catch {
    return null;
  }
}

function textValue(value, fallback = "") {
  const text = String(value ?? "").trim();
  return text || fallback;
}

function integerValue(value) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= 0 ? parsed : 0;
}

function hasScope(scopes, scope) {
  return (Array.isArray(scopes) ? scopes : []).includes(scope);
}

export function normalizePeerSnapshot(value = {}, pairingId = "") {
  const source = value && typeof value === "object" ? value : {};
  const projects = (Array.isArray(source.projects) ? source.projects : []).map((project) => {
    const id = textValue(project?.id);
    const agents = (Array.isArray(project?.agents) ? project.agents : []).map((agent) => {
      const agentId = textValue(agent?.id);
      if (!agentId) return null;
      return {
        id: agentId,
        name: textValue(agent?.name, agentId),
        model: textValue(agent?.model),
        reasoningEffort: textValue(agent?.reasoningEffort),
        permissionModeCap: textValue(agent?.permissionModeCap, "readOnly"),
        status: textValue(agent?.status, "idle"),
        planMode: Boolean(agent?.planMode),
        messageCount: integerValue(agent?.messageCount),
        scopes: normalizePeerScopes(agent?.scopes),
        updatedAt: textValue(agent?.updatedAt),
      };
    }).filter(Boolean);
    if (!id && !agents.length) return null;
    return {
      id: id || "project",
      name: textValue(project?.name, id || "project"),
      status: textValue(project?.status),
      agents,
    };
  }).filter(Boolean);
  const selected = source.selectedAgent && typeof source.selectedAgent === "object" ? source.selectedAgent : null;
  return {
    pairingId: textValue(source.pairingId, pairingId),
    generatedAt: textValue(source.generatedAt),
    projects,
    selectedAgent: selected ? {
      agentId: textValue(selected.agentId),
      messages: (Array.isArray(selected.messages) ? selected.messages : []).map((message) => ({
        id: textValue(message?.id),
        role: textValue(message?.role, "assistant").toLowerCase(),
        contentText: String(message?.contentText ?? message?.content ?? ""),
        createdAt: textValue(message?.createdAt),
      })),
      hasMoreMessages: Boolean(selected.hasMoreMessages),
      pendingApprovals: (Array.isArray(selected.pendingApprovals) ? selected.pendingApprovals : []).map((approval) => ({
        approvalId: textValue(approval?.approvalId || approval?.id),
        agentId: textValue(approval?.agentId),
        toolName: textValue(approval?.toolName),
        risk: textValue(approval?.risk),
        reason: textValue(approval?.reason),
        warning: textValue(approval?.warning),
        createdAt: textValue(approval?.createdAt),
      })).filter((approval) => approval.approvalId),
    } : null,
  };
}

export function collectPeerConversations(hosts = []) {
  const conversations = [];
  for (const host of Array.isArray(hosts) ? hosts : []) {
    const pairing = host?.pairing;
    if (!pairing?.id) continue;
    for (const project of host?.snapshot?.projects || []) {
      for (const agent of project.agents || []) {
        conversations.push({
          pairingId: pairing.id,
          hostName: textValue(pairing.displayName, pairing.endpointOrigin || pairing.id),
          endpointOrigin: textValue(pairing.endpointOrigin),
          projectId: project.id,
          projectName: project.name,
          agentId: agent.id,
          title: agent.name,
          model: agent.model,
          reasoningEffort: agent.reasoningEffort,
          status: agent.status,
          scopes: agent.scopes,
          permissionModeCap: agent.permissionModeCap,
          messageCount: agent.messageCount,
          updatedAt: agent.updatedAt,
          targetId: createPeerTargetId(pairing.id, agent.id),
        });
      }
    }
  }
  return conversations;
}

function scopeLabels(scopes) {
  return normalizePeerScopes(scopes).map((scope) => t(`peerCollaboration.scope.${scope}`)).filter(Boolean);
}

function transcriptBody(text) {
  return escapeHtml(text).replace(/\r\n|\r|\n/g, "<br>");
}

export function peerSnapshotErrorCopy(error) {
  const status = Number(error?.status) || 0;
  const message = String(typeof error === "string" ? error : (error?.message || ""));
  if (status === 503 || /unavailable|disabled|sharing is disabled/i.test(message)) {
    return t("peerCollaboration.navSnapshotSharingOff");
  }
  if (status === 401 || status === 403 || /authentication failed|unauthorized|forbidden/i.test(message)) {
    return t("peerCollaboration.navSnapshotUnauthorized");
  }
  if (status === 502 || /protocol validation|peer request failed/i.test(message)) {
    return t("peerCollaboration.navSnapshotUnreachable");
  }
  return t("peerCollaboration.navSnapshotError");
}

function renderPeerNavEmpty(host) {
  if (!host?.error) {
    return `<p class="peer-collaboration-nav-empty">${escapeHtml(t("peerCollaboration.navEmptyGrants"))}</p>`;
  }
  return `<div class="peer-collaboration-nav-empty peer-collaboration-nav-error">
    <p>${escapeHtml(peerSnapshotErrorCopy(host.error))}</p>
    <button type="button" class="peer-collaboration-nav-retry" data-peer-snapshot-retry="${escapeAttr(host.pairing?.id || "")}">${escapeHtml(t("peerCollaboration.navRetry"))}</button>
  </div>`;
}

export function renderPeerNavigationHTML(hosts = [], { selectedTargetId = "", now = Date.now(), showWhenEmpty = false, query = "" } = {}) {
  const items = Array.isArray(hosts) ? hosts.filter((host) => host?.pairing?.id) : [];
  const needle = String(query || "").trim().toLowerCase();
  if (!items.length) {
    if (!showWhenEmpty) return "";
    return `<div class="peer-collaboration-nav" data-peer-collaboration-nav>
    <p class="peer-collaboration-nav-empty">${escapeHtml(t("peerCollaboration.navNoPairings"))}</p>
  </div>`;
  }
  const selected = textValue(selectedTargetId);
  const sections = items.map((host) => {
      const pairing = host.pairing;
      const hostName = textValue(pairing.displayName, pairing.endpointOrigin || pairing.id);
      const origin = textValue(pairing.endpointOrigin);
      const hostMatches = !needle || `${hostName} ${origin}`.toLowerCase().includes(needle);
      let conversations = collectPeerConversations([host]);
      if (needle && !hostMatches) {
        conversations = conversations.filter((conversation) => (
          `${conversation.title} ${conversation.projectName}`.toLowerCase().includes(needle)
        ));
        if (!conversations.length && !host?.error) return "";
      }
      const rows = conversations.length
        ? conversations.map((conversation) => {
          const active = conversation.targetId === selected;
          const statusClass = navigationAgentStatusClass(conversation.status);
          const meta = [
            conversation.projectName,
            navigationStatusLabel(conversation.status),
            conversation.messageCount ? t("workspace.navigation.messageCount", { count: conversation.messageCount }) : "",
          ].filter(Boolean).join(" · ");
          const relativeTime = formatCompactRelativeTime(conversation.updatedAt, now);
          return `
            <div class="navigation-conversation-row nested peer-collaboration-nav-row ${active ? "active " : ""}status-${escapeAttr(statusClass)}" role="button" tabindex="0"${active ? ` aria-current="true"` : ""} title="${escapeAttr(conversation.title)}" data-peer-target="${escapeAttr(conversation.targetId)}">
              <span class="navigation-agent-icon" aria-hidden="true"><svg viewBox="0 0 20 20"><path d="M10 3.2a6.8 6.8 0 1 0 .01 0Z"></path><path d="M4.4 10h11.2M10 3.5c1.8 1.8 2.7 4 2.7 6.5S11.8 14.7 10 16.5M10 3.5C8.2 5.3 7.3 7.5 7.3 10s.9 4.7 2.7 6.5"></path></svg></span>
              <span class="navigation-conversation-main">
                <span class="navigation-conversation-title"><span class="navigation-title-text">${escapeHtml(conversation.title)}</span></span>
                <span class="navigation-conversation-meta" title="${escapeAttr(meta)}">${escapeHtml(meta)}</span>
              </span>
              ${relativeTime ? `<span class="navigation-conversation-trailing"><span class="navigation-conversation-time">${escapeHtml(relativeTime)}</span></span>` : ""}
            </div>`;
        }).join("")
        : renderPeerNavEmpty(host);
      return `
        <section class="navigation-project-group peer-collaboration-nav-host" data-peer-host="${escapeAttr(pairing.id)}">
          <div class="navigation-conversation-row navigation-project-row peer-collaboration-nav-host-row">
            <span class="navigation-agent-icon navigation-folder-icon" aria-hidden="true"><svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"></circle><path d="M3.8 12h16.4M12 3.5c2.2 2.3 3.3 5.1 3.3 8.5S14.2 18.2 12 20.5M12 3.5C9.8 5.8 8.7 8.6 8.7 12s1.1 6.2 3.3 8.5"></path></svg></span>
            <span class="navigation-conversation-main">
              <span class="navigation-conversation-title"><span class="project-name">${escapeHtml(hostName)}</span></span>
              <span class="navigation-conversation-meta">${escapeHtml(origin || t("peerCollaboration.navHostMeta"))}</span>
            </span>
          </div>
          <div class="navigation-project-conversations">${rows}</div>
        </section>`;
  }).filter(Boolean).join("");
  if (!sections && needle) {
    return `<div class="peer-collaboration-nav" data-peer-collaboration-nav>
    <p class="peer-collaboration-nav-empty">${escapeHtml(t("peerCollaboration.navNoPairings"))}</p>
  </div>`;
  }
  return `<div class="peer-collaboration-nav" data-peer-collaboration-nav>
    ${sections}
  </div>`;
}

function findConversation(hosts, pairingId, agentId) {
  return collectPeerConversations(hosts).find((item) => item.pairingId === pairingId && item.agentId === agentId) || null;
}

export function renderPeerTranscriptHTML({ conversation, snapshot, error = "" } = {}) {
  const hostName = conversation?.hostName || "";
  const title = conversation?.title || conversation?.agentId || "";
  const scopes = scopeLabels(conversation?.scopes);
  const canSend = hasScope(conversation?.scopes, "send_task");
  const canApproveOnce = hasScope(conversation?.scopes, "approve_once");
  const canApproveSession = hasScope(conversation?.scopes, "approve_session");
  const selected = snapshot?.selectedAgent;
  const messages = selected?.messages || [];
  const approvals = selected?.pendingApprovals || [];
  const banner = t(canSend ? "peerCollaboration.remoteBanner" : "peerCollaboration.remoteBannerObserve", {
    host: hostName || title,
  });
  const messageHTML = messages.length
    ? messages.map((message) => {
      const user = message.role === "user";
      const alignment = "left";
      const roleClass = user ? "user" : "assistant";
      const userLabel = t("peerCollaboration.remoteUser");
      const time = message.createdAt
        ? `<time class="message-time" datetime="${escapeAttr(message.createdAt)}">${escapeHtml(formatTimestamp(message.createdAt, { timeOnly: true }))}</time>`
        : "";
      const sender = user
        ? `<div class="message-meta"><span class="message-avatar" aria-hidden="true">${escapeHtml((userLabel || "?").slice(0, 1))}</span><div class="message-role">${escapeHtml(userLabel)}</div></div>`
        : `<div class="message-role sr-only">Autoto</div>`;
      return `
        <div class="message ${roleClass} chat-message chat-flow-item chat-flow-${alignment} peer-collaboration-message" data-chat-alignment="${alignment}" data-message-role="${escapeAttr(message.role)}" data-message-id="${escapeAttr(message.id)}">
          <div class="message-head">
            ${sender}
            ${time}
          </div>
          <div class="message-content">${transcriptBody(message.contentText)}</div>
        </div>`;
    }).join("")
    : `<div class="empty-workspace-card peer-collaboration-transcript-empty"><div class="empty-workspace-title">${escapeHtml(t("peerCollaboration.remoteEmpty"))}</div></div>`;
  const approvalHTML = approvals.length
    ? `<div class="peer-collaboration-approvals">${approvals.map((approval) => `
        <div class="peer-collaboration-approval" data-peer-approval-id="${escapeAttr(approval.approvalId)}">
          <strong>${escapeHtml(t("peerCollaboration.approvalTitle", { tool: approval.toolName || "tool" }))}</strong>
          ${approval.risk ? `<span class="peer-collaboration-approval-risk">${escapeHtml(approval.risk)}</span>` : ""}
          ${approval.reason ? `<p>${escapeHtml(approval.reason)}</p>` : ""}
          ${approval.warning ? `<p class="peer-collaboration-approval-warning">${escapeHtml(approval.warning)}</p>` : ""}
          ${canApproveOnce ? `<div class="peer-collaboration-approval-actions">
            <button class="settings-action-btn primary" type="button" data-peer-approval="${escapeAttr(approval.approvalId)}" data-peer-decision="allow_once">${escapeHtml(t("peerCollaboration.approvalAllowOnce"))}</button>
            ${canApproveSession ? `<button class="settings-action-btn subtle" type="button" data-peer-approval="${escapeAttr(approval.approvalId)}" data-peer-decision="allow_session">${escapeHtml(t("peerCollaboration.approvalAllowSession"))}</button>` : ""}
            <button class="settings-action-btn subtle" type="button" data-peer-approval="${escapeAttr(approval.approvalId)}" data-peer-decision="deny">${escapeHtml(t("peerCollaboration.approvalDeny"))}</button>
          </div>` : ""}
        </div>`).join("")}</div>`
    : "";
  return `
    <div class="peer-collaboration-transcript-banner" role="status">${escapeHtml(banner)}${scopes.length ? ` · ${escapeHtml(scopes.join(" · "))}` : ""}</div>
    ${error ? `<div class="settings-inline-alert settings-alert" role="alert">${escapeHtml(error)}</div>` : ""}
    ${messageHTML}
    ${selected?.hasMoreMessages ? `<p class="peer-collaboration-transcript-more">${escapeHtml(t("peerCollaboration.hasMoreMessages"))}</p>` : ""}
    ${approvalHTML}`;
}

function snapshotURL(pairingId, { agentId = "", messageLimit, runLimit } = {}) {
  const params = new URLSearchParams();
  if (agentId) params.set("agentId", agentId);
  params.set("messageLimit", String(messageLimit ?? (agentId ? 80 : 1)));
  params.set("runLimit", String(runLimit ?? (agentId ? 20 : 1)));
  return `${endpoint}/peers/${encodeURIComponent(pairingId)}/snapshot?${params.toString()}`;
}

function newRequestId() {
  if (typeof globalThis.crypto?.randomUUID === "function") return globalThis.crypto.randomUUID();
  return `peer-${Date.now()}-${Math.floor(Math.random() * 1e9)}`;
}

export function peerWorkspaceFetchAllowed(state = {}, locationLike = globalThis.location) {
  const role = String(state?.account?.role || "").trim();
  if (role === "guest" || role === "collaborator") return false;
  if (state?.remoteAccessFailClosed) return false;
  const hostname = String(locationLike?.hostname || "").trim().toLowerCase();
  const loopback = hostname === "localhost" || hostname === "127.0.0.1" || hostname === "::1" || hostname === "[::1]";
  const sessionRemote = state?.remoteAccess?.session?.remote === true;
  const capabilities = state?.remoteAccess?.capabilities || {};
  if (sessionRemote || (!loopback && hostname)) {
    return String(capabilities.maxPermissionMode || "") === "bypassPermissions"
      || String(capabilities.maxPermissionMode || "") === "full";
  }
  return true;
}

export function createPeerCollaborationWorkspaceController({
  request,
  isAllowed = () => true,
  onChange,
  now = () => Date.now(),
} = {}) {
  let loadSequence = 0;
  let hosts = [];
  let selected = null;
  let selectedSnapshot = null;
  let selectedError = "";
  let lastListAt = 0;
  let blocked = false;
  let sending = false;
  let transcriptBound = false;
  const runtimeOverrides = new Map();

  function selectedConversation() {
    if (!selected) return null;
    return findConversation(hosts, selected.pairingId, selected.agentId);
  }

  function selectedSummary() {
    const conversation = selectedConversation();
    if (!conversation) return null;
    const override = runtimeOverrides.get(conversation.targetId) || {};
    return {
      title: conversation.title,
      hostName: conversation.hostName,
      scopes: conversation.scopes,
      scopeLabels: scopeLabels(conversation.scopes).join(" · "),
      model: override.model || conversation.model,
      reasoningEffort: override.reasoningEffort || conversation.reasoningEffort,
      permissionMode: override.permissionMode || conversation.permissionModeCap,
      permissionModeCap: conversation.permissionModeCap,
    };
  }

  async function refreshList(sequence) {
    const status = await request(`${endpoint}/status`);
    if (sequence !== loadSequence) return;
    const pairings = (Array.isArray(status?.pairings) ? status.pairings : [])
      .map(normalizePeerPairing)
      .filter((item) => item.localRole === "controller" && item.status === "active");
    const nextHosts = await Promise.all(pairings.map(async (pairing) => {
      try {
        const snapshot = normalizePeerSnapshot(await request(snapshotURL(pairing.id)), pairing.id);
        return { pairing, snapshot, error: "" };
      } catch (error) {
        return { pairing, snapshot: normalizePeerSnapshot({}, pairing.id), error };
      }
    }));
    if (sequence !== loadSequence) return;
    hosts = nextHosts;
    lastListAt = now();
  }

  async function refreshSelected(sequence) {
    if (!selected) return;
    const snapshot = normalizePeerSnapshot(
      await request(snapshotURL(selected.pairingId, { agentId: selected.agentId })),
      selected.pairingId,
    );
    if (sequence !== loadSequence) return;
    selectedSnapshot = snapshot;
    selectedError = "";
    hosts = hosts.map((host) => host.pairing.id === selected.pairingId ? { ...host, snapshot, error: "" } : host);
  }

  async function refresh({ force = false } = {}) {
    if (blocked || !isAllowed()) {
      if (hosts.length) {
        hosts = [];
        onChange?.();
      }
      return hosts;
    }
    const sequence = ++loadSequence;
    const listDue = force || !lastListAt || now() - lastListAt >= listIntervalMs;
    try {
      if (listDue) await refreshList(sequence);
      if (sequence !== loadSequence) return hosts;
      if (selected) await refreshSelected(sequence);
    } catch (error) {
      if (sequence !== loadSequence) return hosts;
      if (error?.status === 401 || error?.status === 403) {
        blocked = true;
        hosts = [];
        selected = null;
        selectedSnapshot = null;
      } else if (selected) {
        selectedError = error?.message || String(error);
      }
    } finally {
      if (sequence === loadSequence) onChange?.();
    }
    return hosts;
  }

  function select(target) {
    const parsed = typeof target === "string" ? parsePeerTargetId(target) : target;
    if (!parsed?.pairingId || !parsed?.agentId) return null;
    selected = { pairingId: parsed.pairingId, agentId: parsed.agentId, targetId: createPeerTargetId(parsed.pairingId, parsed.agentId) };
    selectedError = "";
    return selected;
  }

  function clearSelection() {
    selected = null;
    selectedSnapshot = null;
    selectedError = "";
  }

  async function updateRuntime(fields = {}) {
    const conversation = selectedConversation();
    if (!conversation || !hasScope(conversation.scopes, "send_task")) {
      throw new Error(t("peerCollaboration.observeOnlyToast"));
    }
    const model = String(fields.model || "").trim();
    const reasoningEffort = String(fields.reasoningEffort || "").trim();
    const permissionMode = String(fields.permissionMode || "").trim();
    if (!model && !reasoningEffort && !permissionMode) return selectedSummary();
    const result = await request(`${endpoint}/peers/${encodeURIComponent(conversation.pairingId)}/agents/${encodeURIComponent(conversation.agentId)}/runtime`, {
      method: "POST",
      body: JSON.stringify({
        ...(model ? { model } : {}),
        ...(reasoningEffort ? { reasoningEffort } : {}),
        ...(permissionMode ? { permissionMode } : {}),
      }),
    });
    const previous = runtimeOverrides.get(conversation.targetId) || {};
    runtimeOverrides.set(conversation.targetId, {
      model: textValue(result.model, previous.model || conversation.model),
      reasoningEffort: textValue(result.reasoningEffort, previous.reasoningEffort || conversation.reasoningEffort),
      permissionMode: textValue(result.permissionMode, previous.permissionMode || conversation.permissionModeCap),
    });
    conversation.model = runtimeOverrides.get(conversation.targetId).model;
    conversation.reasoningEffort = runtimeOverrides.get(conversation.targetId).reasoningEffort;
    onChange?.();
    return selectedSummary();
  }

  async function sendTask(message) {
    const conversation = selectedConversation();
    if (!conversation || !hasScope(conversation.scopes, "send_task")) {
      throw new Error(t("peerCollaboration.observeOnlyToast"));
    }
    const text = String(message || "").trim();
    if (!text) return null;
    sending = true;
    try {
      const result = await request(`${endpoint}/peers/${encodeURIComponent(conversation.pairingId)}/agents/${encodeURIComponent(conversation.agentId)}/tasks`, {
        method: "POST",
        body: JSON.stringify({ message: text, requestId: newRequestId() }),
      });
      await refresh({ force: true });
      return result;
    } finally {
      sending = false;
    }
  }

  async function resolveApproval(approvalId, decision) {
    const conversation = selectedConversation();
    if (!conversation) throw new Error(t("peerCollaboration.pairingMissing"));
    sending = true;
    try {
      const result = await request(`${endpoint}/peers/${encodeURIComponent(conversation.pairingId)}/agents/${encodeURIComponent(conversation.agentId)}/approvals/${encodeURIComponent(approvalId)}`, {
        method: "POST",
        body: JSON.stringify({ decision }),
      });
      await refresh({ force: true });
      return result;
    } finally {
      sending = false;
    }
  }

  function bindTranscript(root, { onApprove } = {}) {
    if (!root || transcriptBound) return;
    transcriptBound = true;
    root.addEventListener("click", async (event) => {
      const button = event.target?.closest?.("[data-peer-approval][data-peer-decision]");
      if (!button || !root.contains(button)) return;
      event.preventDefault();
      setButtonBusy(button, true, t("peerCollaboration.working"));
      try {
        await onApprove?.(button.dataset.peerApproval, button.dataset.peerDecision);
      } finally {
        setButtonBusy(button, false);
      }
    });
  }

  return {
    refresh,
    select,
    clearSelection,
    sendTask,
    updateRuntime,
    resolveApproval,
    bindTranscript,
    isSelected: () => Boolean(selected?.targetId),
    selectedTargetId: () => selected?.targetId || "",
    selectedSummary,
    selectedConversation,
    selectedSnapshot: () => selectedSnapshot,
    selectedError: () => selectedError,
    canSend: () => hasScope(selectedConversation()?.scopes, "send_task"),
    isSending: () => sending,
    selectedIsLive: () => ["running", "pending", "queued", "waiting"].includes(navigationAgentStatusClass(selectedConversation()?.status)),
    hosts: () => hosts,
    renderNavigationHTML: (options = {}) => {
      if (blocked || !isAllowed()) return "";
      return renderPeerNavigationHTML(hosts, {
        selectedTargetId: selected?.targetId || "",
        showWhenEmpty: true,
        ...options,
      });
    },
    renderTranscriptHTML: () => renderPeerTranscriptHTML({
      conversation: selectedConversation(),
      snapshot: selectedSnapshot,
      error: selectedError,
    }),
  };
}
