import test from "node:test";
import assert from "node:assert/strict";

globalThis.window = { AUTOTO_LOCAL_TOKEN: "" };
globalThis.location = { origin: "http://localhost", protocol: "http:", host: "localhost", hostname: "localhost" };
globalThis.document = { getElementById: () => null, documentElement: { lang: "zh-TW" } };

const {
  collectPeerConversations,
  createPeerCollaborationWorkspaceController,
  createPeerTargetId,
  normalizePeerSnapshot,
  parsePeerTargetId,
  peerWorkspaceFetchAllowed,
  renderPeerNavigationHTML,
  renderPeerTranscriptHTML,
} = await import("./peer-collaboration-workspace.mjs");

const pairing = {
  id: "pairing-1",
  localRole: "controller",
  displayName: "Autoto",
  endpointOrigin: "https://autoto.example",
  status: "active",
  peerFingerprint: "b".repeat(64),
};

function snapshotPayload(overrides = {}) {
  return {
    pairingId: "pairing-1",
    projects: [{
      id: "project-1",
      name: "mingpan",
      agents: [{
        id: "agent-1",
        name: "國鋒專用 修改網站",
        model: "google:gemini-3-pro",
        reasoningEffort: "xhigh",
        status: "idle",
        messageCount: 4,
        scopes: ["observe", "send_task", "approve_once"],
        permissionModeCap: "acceptEdits",
        updatedAt: "2026-08-19T03:00:00Z",
      }],
    }],
    ...overrides,
  };
}

test("peer targets never look like local navigation ids", () => {
  const targetId = createPeerTargetId("pairing-1", "agent-1");
  assert.equal(targetId, "peer:pairing-1:agent-1");
  assert.deepEqual(parsePeerTargetId(targetId), { pairingId: "pairing-1", agentId: "agent-1", targetId });
  assert.equal(parsePeerTargetId("p1::w1::a1"), null);
});

test("snapshots only keep agents the host listed", () => {
  const snapshot = normalizePeerSnapshot(snapshotPayload());
  const conversations = collectPeerConversations([{ pairing, snapshot }]);
  assert.equal(conversations.length, 1);
  assert.equal(conversations[0].title, "國鋒專用 修改網站");
  assert.deepEqual(conversations[0].scopes, ["observe", "send_task", "approve_once"]);
});

test("the sidebar lists remote conversations without local navigation ids", () => {
  const html = renderPeerNavigationHTML([{ pairing, snapshot: normalizePeerSnapshot(snapshotPayload()) }], {
    selectedTargetId: createPeerTargetId("pairing-1", "agent-1"),
  });
  assert.match(html, /data-peer-collaboration-nav/);
  assert.match(html, /data-peer-target="peer:pairing-1:agent-1"/);
  assert.match(html, /國鋒專用 修改網站/);
  assert.match(html, /aria-current="true"/);
  assert.doesNotMatch(html, /data-navigation-target/);
  assert.doesNotMatch(html, /data-navigation-kind="conversation"/);
  assert.doesNotMatch(html, /draggable="true"/);
});

test("an empty host grant set explains that nothing is shared yet", () => {
  const html = renderPeerNavigationHTML([{
    pairing,
    snapshot: normalizePeerSnapshot({ pairingId: "pairing-1", projects: [] }),
  }]);
  assert.match(html, /peer-collaboration-nav-empty/);
  assert.doesNotMatch(html, /data-peer-target/);
});

test("an allowed controller with no pairings still shows the sidebar section", () => {
  const html = renderPeerNavigationHTML([], { showWhenEmpty: true });
  assert.match(html, /data-peer-collaboration-nav/);
  assert.match(html, /peer-collaboration-nav-empty/);
  assert.doesNotMatch(html, /data-peer-target/);
  const controller = createPeerCollaborationWorkspaceController({
    request: async () => ({ pairings: [] }),
  });
  assert.match(controller.renderNavigationHTML(), /data-peer-collaboration-nav/);
});

test("the remote transcript escapes host text and hides approvals the grant does not allow", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [{ id: "m1", role: "user", contentText: "<script>alert(1)</script>", createdAt: "2026-08-19T03:00:00Z" }],
        pendingApprovals: [{ approvalId: "appr-1", toolName: "Bash", risk: "exec", reason: "run" }],
      },
    }),
  });
  assert.match(html, /peer-collaboration-transcript-banner/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<script>alert/);
  assert.doesNotMatch(html, /data-peer-decision/);
});

test("send_task and approve_once grants surface the matching controls", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe", "send_task", "approve_once", "approve_session"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [],
        pendingApprovals: [{ approvalId: "appr-1", toolName: "Edit", risk: "write" }],
      },
    }),
  });
  assert.match(html, /data-peer-approval="appr-1"/);
  assert.match(html, /data-peer-decision="allow_once"/);
  assert.match(html, /data-peer-decision="allow_session"/);
  assert.match(html, /data-peer-decision="deny"/);
});

test("restricted remote and guest accounts must not fetch the controller API", () => {
  assert.equal(peerWorkspaceFetchAllowed({ account: { role: "guest" } }), false);
  assert.equal(peerWorkspaceFetchAllowed({ account: { role: "collaborator" } }), false);
  assert.equal(peerWorkspaceFetchAllowed({
    remoteAccess: { session: { remote: true }, capabilities: { maxPermissionMode: "acceptEdits" } },
  }, { hostname: "autoto.example" }), false);
  assert.equal(peerWorkspaceFetchAllowed({ account: { role: "user" } }, { hostname: "localhost" }), true);
});

test("snapshots keep the host model and thinking strength for composer chrome", () => {
  const snapshot = normalizePeerSnapshot(snapshotPayload({
    projects: [{
      id: "project-1",
      name: "mingpan",
      agents: [{
        id: "agent-1",
        name: "國鋒專用 修改網站",
        model: "google:gemini-3-pro",
        reasoningEffort: "xhigh",
        status: "idle",
        messageCount: 4,
        scopes: ["observe", "send_task"],
        permissionModeCap: "acceptEdits",
        updatedAt: "2026-08-19T03:00:00Z",
      }],
    }],
  }));
  const conversations = collectPeerConversations([{ pairing, snapshot }]);
  assert.equal(conversations[0].model, "google:gemini-3-pro");
  assert.equal(conversations[0].reasoningEffort, "xhigh");
  assert.equal(conversations[0].permissionModeCap, "acceptEdits");
});

test("the remote transcript uses the local left-aligned chat flow", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe", "send_task"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [
          { id: "m1", role: "user", contentText: "你好", createdAt: "2026-08-19T03:00:00Z" },
          { id: "m2", role: "assistant", contentText: "收到", createdAt: "2026-08-19T03:01:00Z" },
        ],
      },
    }),
  });
  assert.match(html, /chat-flow-left/);
  assert.doesNotMatch(html, /chat-flow-right/);
  assert.match(html, /message-avatar/);
  assert.match(html, /message-role sr-only">Autoto/);
  assert.match(html, /peer-collaboration-message/);
});

test("the workspace controller loads controller snapshots and sends through the proxy", async () => {
  const requests = [];
  const controller = createPeerCollaborationWorkspaceController({
    request: async (path, options = {}) => {
      requests.push({ path, method: options.method || "GET", body: options.body ? JSON.parse(options.body) : null });
      if (path.endsWith("/status")) {
        return { pairings: [{ pairing, grants: [] }] };
      }
      if (path.includes("/snapshot")) {
        const detailed = /agentId=agent-1/.test(path);
        return snapshotPayload(detailed ? {
          selectedAgent: {
            agentId: "agent-1",
            messages: [{ id: "m1", role: "assistant", contentText: "hello", createdAt: "2026-08-19T03:00:00Z" }],
          },
        } : {});
      }
      if (path.endsWith("/tasks")) return { status: "accepted" };
      return {};
    },
  });
  await controller.refresh({ force: true });
  assert.match(controller.renderNavigationHTML(), /data-peer-target="peer:pairing-1:agent-1"/);
  controller.select("peer:pairing-1:agent-1");
  await controller.refresh({ force: true });
  assert.equal(controller.canSend(), true);
  assert.equal(controller.selectedSummary()?.model, "google:gemini-3-pro");
  assert.equal(controller.selectedSummary()?.reasoningEffort, "xhigh");
  assert.equal(controller.selectedSummary()?.permissionModeCap, "acceptEdits");
  assert.match(controller.renderTranscriptHTML(), /hello/);
  await controller.sendTask("請繼續");
  const send = requests.find((entry) => entry.method === "POST" && entry.path.endsWith("/tasks"));
  assert.equal(send.body.message, "請繼續");
  assert.match(String(send.body.requestId || ""), /[A-Za-z0-9._:-]+/);
});

test("a 403 stops further controller polling instead of retrying", async () => {
  let calls = 0;
  const controller = createPeerCollaborationWorkspaceController({
    request: async () => {
      calls += 1;
      const error = new Error("forbidden");
      error.status = 403;
      throw error;
    },
  });
  await controller.refresh({ force: true });
  await controller.refresh({ force: true });
  assert.equal(calls, 1);
  assert.equal(controller.renderNavigationHTML(), "");
});
