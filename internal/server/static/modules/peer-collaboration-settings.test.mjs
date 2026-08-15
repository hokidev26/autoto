import test from "node:test";
import assert from "node:assert/strict";

import {
  authorizationPayload,
  collectAgentOptions,
  createPeerCollaborationSettingsController,
  normalizePeerCollaboration,
  normalizePeerScopes,
} from "./peer-collaboration-settings.mjs";

const hostFingerprint = "a".repeat(64);
const peerFingerprint = "b".repeat(64);

const navigation = {
  projects: [{ id: "project-1", name: "Alpha" }],
  conversations: [
    { projectId: "project-1", projectName: "Alpha", agentId: "agent-1", agentTitle: "Build the parser" },
    { projectId: "project-1", projectName: "Alpha", agentId: "agent-1", agentTitle: "duplicate is dropped" },
    { projectId: "project-1", projectName: "Alpha", agentId: "agent-2", agentTitle: "Archived", agentArchivedAt: "2026-08-01T00:00:00Z" },
    { projectId: "", projectName: "", agentId: "agent-3", agentTitle: "No project" },
  ],
};

function statusPayload(overrides = {}) {
  return {
    available: true,
    sharingEnabled: true,
    identity: { protocolVersion: 1, publicKey: "k".repeat(44), fingerprint: hostFingerprint },
    tunnel: { available: true, status: "running", publicUrl: "https://pair.example.com" },
    invitations: [],
    pairings: [],
    ...overrides,
  };
}

function claimedInvitation(overrides = {}) {
  return {
    id: "invitation-1",
    protocolVersion: 1,
    status: "claimed",
    requesterDisplayName: "Studio laptop",
    requesterInstallationId: "install-1",
    requesterFingerprint: peerFingerprint,
    failedAttempts: 0,
    expiresAt: "2026-08-13T09:00:00Z",
    revision: 2,
    createdAt: "2026-08-13T08:00:00Z",
    updatedAt: "2026-08-13T08:10:00Z",
    ...overrides,
  };
}

function hostPairing(overrides = {}) {
  return {
    pairing: {
      id: "pairing-1",
      localRole: "host",
      displayName: "Studio laptop",
      peerInstallationId: "install-1",
      peerPublicKey: "k".repeat(44),
      peerFingerprint,
      status: "active",
      scopes: ["observe", "send_task"],
      credentialRevision: 3,
      grantRevision: 4,
      pairedAt: "2026-08-13T08:15:00Z",
      createdAt: "2026-08-13T08:15:00Z",
      updatedAt: "2026-08-13T08:15:00Z",
      ...overrides,
    },
    grants: [{
      id: "grant-1",
      pairingId: "pairing-1",
      projectId: "project-1",
      agentId: "agent-1",
      scopes: ["observe"],
      permissionModeCap: "readOnly",
      revision: 1,
    }],
  };
}

// Every request the controller makes goes through one recorder so a test can
// assert both the payloads and that a mutation refreshed the status afterwards.
function createHarness({ status = statusPayload(), responses = {} } = {}) {
  const requests = [];
  const state = {};
  const toasts = [];
  let currentStatus = status;
  const controller = createPeerCollaborationSettingsController({
    state,
    request: async (path, options = {}) => {
      requests.push({ path, method: options.method || "GET", body: options.body ? JSON.parse(options.body) : null });
      if (path === "/api/navigation") return navigation;
      if (path === "/api/remote-collaboration/status") return currentStatus;
      const responder = responses[path];
      if (typeof responder === "function") return responder();
      if (responder !== undefined) return responder;
      return {};
    },
    copyText: async () => {},
    showToast: (message) => toasts.push(message),
  });
  return {
    controller,
    requests,
    state,
    toasts,
    setStatus: (next) => { currentStatus = next; },
  };
}

test("normalizes peer scopes into the server's order and drops unknown values", () => {
  assert.deepEqual(normalizePeerScopes(["execute_tools", "observe", "nope", "observe"]), ["observe", "execute_tools"]);
  assert.deepEqual(
    normalizePeerScopes(["execute_tools", "approve_session", "nope", "approve_once"]),
    ["approve_once", "approve_session", "execute_tools"],
  );
  assert.deepEqual(normalizePeerScopes("observe"), []);
  assert.deepEqual(normalizePeerScopes(undefined), []);
});

test("normalizes a collaboration status and falls back for unknown enum values", () => {
  const value = normalizePeerCollaboration({
    available: true,
    invitations: [claimedInvitation({ status: "unexpected" })],
    pairings: [hostPairing({ localRole: "unexpected", status: "unexpected" })],
  });
  assert.equal(value.invitations[0].status, "expired");
  assert.equal(value.pairings[0].localRole, "host");
  assert.equal(value.pairings[0].status, "expired");
  assert.equal(value.sharingEnabled, false);
  assert.deepEqual(normalizePeerCollaboration({}).invitations, []);
});

test("an authorization payload clamps grant scopes to the pairing scopes", () => {
  const payload = authorizationPayload({
    scopes: ["execute_tools", "observe"],
    expiresInHours: 0,
    grants: [
      { projectId: "project-1", agentId: "agent-1", scopes: ["observe", "send_task", "approve_session"], permissionModeCap: "acceptEdits" },
      { projectId: "project-1", agentId: "agent-2", scopes: ["execute_tools"], permissionModeCap: "nonsense" },
      { projectId: "", agentId: "agent-3", scopes: ["observe"], permissionModeCap: "readOnly" },
    ],
  }, { revisionKey: "grantRevision", revision: 4 });

  assert.deepEqual(payload, {
    grantRevision: 4,
    scopes: ["observe", "execute_tools"],
    grants: [
      { projectId: "project-1", agentId: "agent-1", scopes: ["observe"], permissionModeCap: "acceptEdits" },
      { projectId: "project-1", agentId: "agent-2", scopes: ["execute_tools"], permissionModeCap: "readOnly" },
    ],
  });
  assert.equal("expiresAt" in payload, false);
});

test("an authorization payload only carries an expiry when hours are positive", () => {
  const payload = authorizationPayload({ scopes: ["observe"], expiresInHours: 2, grants: [] }, { revisionKey: "revision", revision: 1 });
  const expiresAt = Date.parse(payload.expiresAt);
  assert.ok(Number.isFinite(expiresAt));
  assert.ok(expiresAt > Date.now());
});

test("agent options come from navigation, deduplicated and without archived or project-less rows", () => {
  assert.deepEqual(collectAgentOptions(navigation), [
    { agentId: "agent-1", projectId: "project-1", agentTitle: "Build the parser", projectName: "Alpha" },
  ]);
  assert.deepEqual(collectAgentOptions({}), []);
});

test("creating an invitation asks for a bounded lifetime and keeps the code out of shared state", async () => {
  const harness = createHarness({
    responses: {
      "/api/remote-collaboration/invitations": () => ({
        invitation: claimedInvitation({ status: "open", requesterDisplayName: "", requesterFingerprint: "", revision: 1 }),
        encodedInvitation: "autoto-pair:eyJ2ZXJzaW9uIjoxfQ",
        hostFingerprint,
      }),
    },
  });

  const created = await harness.controller.createInvitation("1800");
  assert.equal(created.encodedInvitation, "autoto-pair:eyJ2ZXJzaW9uIjoxfQ");
  assert.equal(harness.state.peerCollaboration.invitations.length, 0);

  const invite = harness.requests.find((entry) => entry.path === "/api/remote-collaboration/invitations");
  assert.deepEqual(invite, { path: "/api/remote-collaboration/invitations", method: "POST", body: { expiresInSeconds: 1800 } });
  // An out-of-range lifetime would be rejected by the server, so the picker value
  // is snapped back to a supported choice before the request goes out.
  await harness.controller.createInvitation("99999");
  const second = harness.requests.filter((entry) => entry.path === "/api/remote-collaboration/invitations")[1];
  assert.deepEqual(second.body, { expiresInSeconds: 600 });

  const html = harness.controller.render();
  assert.match(html, /autoto-pair:eyJ2ZXJzaW9uIjoxfQ/);
  assert.match(html, /peer-collaboration-qr/);
  assert.match(html, /<svg /);
});

test("approving a claim sends the invitation revision with the granted agents", async () => {
  const harness = createHarness({
    status: statusPayload({ invitations: [claimedInvitation()] }),
    responses: { "/api/remote-collaboration/invitations/invitation-1/approve": () => hostPairing() },
  });
  await harness.controller.load();
  harness.controller.addGrant("approval", "invitation-1", "agent-1");
  await harness.controller.approveInvitation("invitation-1");

  const approve = harness.requests.find((entry) => entry.path.endsWith("/approve"));
  assert.equal(approve.method, "POST");
  assert.equal(approve.body.revision, 2);
  assert.deepEqual(approve.body.scopes, ["observe"]);
  assert.deepEqual(approve.body.grants, [
    { projectId: "project-1", agentId: "agent-1", scopes: ["observe"], permissionModeCap: "readOnly" },
  ]);
});

test("rejecting and revoking an invitation carry the status the server expects", async () => {
  const harness = createHarness({ status: statusPayload({ invitations: [claimedInvitation()] }) });
  await harness.controller.load();

  await harness.controller.transitionInvitation("invitation-1", "reject");
  await harness.controller.transitionInvitation("invitation-1", "revoke");

  const transitions = harness.requests.filter((entry) => entry.path.match(/\/(reject|revoke)$/));
  assert.deepEqual(transitions.map((entry) => [entry.path, entry.method, entry.body]), [
    ["/api/remote-collaboration/invitations/invitation-1/reject", "POST", { status: "claimed", revision: 2 }],
    ["/api/remote-collaboration/invitations/invitation-1/revoke", "POST", { status: "claimed", revision: 2 }],
  ]);
});

test("editing authorization replaces the whole grant set against the grant revision", async () => {
  const harness = createHarness({
    status: statusPayload({ pairings: [hostPairing()] }),
    responses: { "/api/remote-collaboration/pairings/pairing-1/authorization": () => hostPairing() },
  });
  await harness.controller.load();
  await harness.controller.saveAuthorization("pairing-1");

  const save = harness.requests.find((entry) => entry.path.endsWith("/authorization"));
  assert.equal(save.method, "PUT");
  assert.equal(save.body.grantRevision, 4);
  assert.deepEqual(save.body.scopes, ["observe", "send_task"]);
  assert.deepEqual(save.body.grants, [
    { projectId: "project-1", agentId: "agent-1", scopes: ["observe"], permissionModeCap: "readOnly" },
  ]);
});

test("revoking a pairing sends the credential revision that invalidates peer tokens", async () => {
  const harness = createHarness({ status: statusPayload({ pairings: [hostPairing()] }) });
  await harness.controller.load();
  await harness.controller.revokePairing("pairing-1");

  const revoke = harness.requests.find((entry) => entry.path.endsWith("/pairings/pairing-1/revoke"));
  assert.deepEqual(revoke.body, { status: "active", credentialRevision: 3 });
});

test("redeeming an invitation tracks the pending claim until it is polled", async () => {
  const harness = createHarness({
    responses: {
      "/api/remote-collaboration/connect": () => ({
        claim: { protocolVersion: 1, invitationId: "invitation-9", status: "claimed", revision: 2 },
        hostFingerprint,
        origin: "https://pair.example.com",
      }),
      "/api/remote-collaboration/claims/invitation-9/poll": () => ({ invitationId: "invitation-9", status: "claimed", revision: 2 }),
    },
  });

  const claimId = await harness.controller.connectPeer(" autoto-pair:abc ", "");
  assert.equal(claimId, "invitation-9");
  const connect = harness.requests.find((entry) => entry.path.endsWith("/connect"));
  assert.deepEqual(connect.body, { invitation: "autoto-pair:abc", displayName: "Autoto" });

  const pending = harness.controller.render();
  assert.match(pending, /peer-collaboration-claim/);
  assert.match(pending, /pair\.example\.com/);

  assert.equal(await harness.controller.pollClaim("invitation-9"), "claimed");
  await assert.rejects(harness.controller.connectPeer("", "name"), /邀请码/);
});

test("an approved claim clears the pending card and reloads the pairing list", async () => {
  const harness = createHarness({
    responses: {
      "/api/remote-collaboration/connect": () => ({
        claim: { invitationId: "invitation-9", status: "claimed" },
        hostFingerprint,
        origin: "https://pair.example.com",
      }),
      "/api/remote-collaboration/claims/invitation-9/poll": () => ({ invitationId: "invitation-9", status: "approved", pairingId: "pairing-9" }),
    },
  });
  await harness.controller.connectPeer("autoto-pair:abc", "Desk");
  harness.setStatus(statusPayload({ pairings: [hostPairing({ id: "pairing-9", localRole: "controller", endpointOrigin: "https://pair.example.com" })] }));

  assert.equal(await harness.controller.pollClaim("invitation-9"), "approved");
  const html = harness.controller.render();
  assert.doesNotMatch(html, /peer-collaboration-claim/);
  assert.match(html, /本机为控制端/);
});

test("invitations cannot be created without sharing and an HTTPS tunnel", async () => {
  const offline = createHarness({
    status: statusPayload({ sharingEnabled: false, tunnel: { available: false, status: "unavailable", publicUrl: "" } }),
  });
  await offline.controller.load();
  const html = offline.controller.render();
  assert.match(html, /data-peer-invite-submit[^>]*disabled/);
  assert.match(html, /尚无 HTTPS 通道/);

  const ready = createHarness({ status: statusPayload({ invitations: [claimedInvitation()] }) });
  await ready.controller.load();
  const readyHTML = ready.controller.render();
  assert.doesNotMatch(readyHTML, /data-peer-invite-submit[^>]*disabled/);
  // A claimed invitation must show the requester fingerprint before approval.
  assert.match(readyHTML, new RegExp(peerFingerprint.slice(0, 12)));
  assert.match(readyHTML, /data-peer-action="open-approve"/);
});

test("turning sharing off reports the revocation and keeps state in step", async () => {
  const harness = createHarness({
    responses: { "/api/remote-collaboration/sharing": () => ({ sharingEnabled: false }) },
  });
  await harness.controller.load();
  assert.equal(await harness.controller.setSharing(false), false);
  const sharing = harness.requests.find((entry) => entry.path.endsWith("/sharing"));
  assert.deepEqual([sharing.method, sharing.body], ["PUT", { enabled: false }]);
  assert.equal(harness.state.peerCollaboration.sharingEnabled, false);
  assert.ok(harness.toasts.some((message) => message.includes("撤销")));
});

test("a forbidden status read explains that pairing is managed on localhost", async () => {
  const state = {};
  const controller = createPeerCollaborationSettingsController({
    state,
    request: async () => {
      const error = new Error("403 Forbidden");
      error.status = 403;
      throw error;
    },
  });
  await assert.rejects(controller.load(), /403/);
  assert.match(state.peerCollaborationError, /localhost/);
});

test("peer collaboration cards share the settings-network panel container", async () => {
  const { readFile } = await import("node:fs/promises");
  const css = await readFile(new URL("../styles/workspace-tasks.css", import.meta.url), "utf8");
  assert.match(css, /#settingsContentBody \.peer-collaboration-page/);
  assert.match(css, /container:\s*settings-network-page\s*\/\s*inline-size/);
  assert.match(css, /@container settings-network-page \(max-width: 759px\)[\s\S]*?\.peer-collaboration-qr/);
  assert.match(css, /#settingsContentBody \.peer-collaboration-page \.settings-hero-card \{[\s\S]*?flex-direction:\s*column/);
  assert.match(css, /#settingsContentBody \.peer-collaboration-stack \{/);
  assert.match(css, /#settingsContentBody \.peer-collaboration-invite-form \{[\s\S]*?grid-template-columns:\s*minmax\(0, 200px\) auto/);
  assert.match(css, /#settingsContentBody \.peer-collaboration-connect-form \{/);
  assert.match(css, /#settingsContentBody \.peer-collaboration-connect-actions \{[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\) auto/);
});

test("the pairing hero is a switch plus labeled identity rows, not nested stat cards", async () => {
  const harness = createHarness();
  await harness.controller.load();
  const html = harness.controller.render();
  assert.match(html, /peer-collaboration-hero/);
  assert.match(html, /peer-collaboration-stack/);
  assert.match(html, /peer-collaboration-sharing-row is-on/);
  assert.match(html, /id="peerCollaborationSharing"[^>]*role="switch"/);
  assert.match(html, /peer-collaboration-meta/);
  assert.match(html, /peer-collaboration-invite-form/);
  assert.doesNotMatch(html, /peer-collaboration-facts/);
  const hero = html.slice(html.indexOf("peer-collaboration-hero"), html.indexOf("peerCollaborationInviteForm"));
  assert.doesNotMatch(hero, /settings-check-row/);
  assert.doesNotMatch(hero, /settings-stat-card/);
  const refreshAt = hero.indexOf("data-peer-action=\"refresh\"");
  const sharingAt = hero.indexOf("peerCollaborationSharing");
  const copyAt = hero.indexOf("data-peer-action=\"copy-fingerprint\"");
  assert.ok(refreshAt > -1 && refreshAt < sharingAt, "refresh belongs in the header, not the fingerprint row");
  assert.ok(sharingAt > -1 && sharingAt < copyAt, "the sharing switch sits above the fingerprint");
});

test("the connect card stacks the invitation code above the name and submit row", async () => {
  const harness = createHarness();
  await harness.controller.load();
  const html = harness.controller.render();
  assert.match(html, /peer-collaboration-connect-form/);
  assert.match(html, /peer-collaboration-connect-code/);
  assert.match(html, /peer-collaboration-connect-actions/);
  const connect = html.slice(html.indexOf("peerCollaborationConnectForm"), html.indexOf("peerCollaborationConnectForm") + 1200);
  assert.doesNotMatch(connect, /settings-form-grid/);
  const codeAt = connect.indexOf("peerCollaborationConnectCode");
  const nameAt = connect.indexOf("peerCollaborationConnectName");
  const submitAt = connect.indexOf("data-peer-connect-submit");
  assert.ok(codeAt > -1 && nameAt > codeAt, "the invitation code sits above the display name");
  assert.ok(submitAt > nameAt, "submit stays beside the display name, not in a third grid hole");
});
