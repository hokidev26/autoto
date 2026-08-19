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
  parsePeerToolResultMessage,
  peerSnapshotErrorCopy,
  peerTranscriptItems,
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
  assert.doesNotMatch(html, /peer-collaboration-nav-label/);
  assert.doesNotMatch(html, /data-navigation-target/);
  assert.doesNotMatch(html, /data-navigation-kind="conversation"/);
  assert.doesNotMatch(html, /draggable="true"/);
});

test("remote navigation search keeps matching hosts and hides unrelated conversations", () => {
  const html = renderPeerNavigationHTML([{ pairing, snapshot: normalizePeerSnapshot(snapshotPayload()) }], {
    query: "國鋒",
  });
  assert.match(html, /data-peer-target="peer:pairing-1:agent-1"/);
  const missed = renderPeerNavigationHTML([{ pairing, snapshot: normalizePeerSnapshot(snapshotPayload()) }], {
    query: "no-such-host",
  });
  assert.doesNotMatch(missed, /data-peer-target/);
  assert.match(missed, /peer-collaboration-nav-empty/);
});

test("an empty host grant set explains that nothing is shared yet", () => {
  const html = renderPeerNavigationHTML([{
    pairing,
    snapshot: normalizePeerSnapshot({ pairingId: "pairing-1", projects: [] }),
  }]);
  assert.match(html, /peer-collaboration-nav-empty/);
  assert.doesNotMatch(html, /data-peer-target/);
  assert.doesNotMatch(html, /data-peer-snapshot-retry/);
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

test("the remote transcript renders markdown lists and inline code like a local chat", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [{
          id: "m1",
          role: "assistant",
          contentText: [
            "目前狀態：",
            "",
            "- 本地 **main**：`28a29ae`",
            "- 遠端 `origin/main`",
            "",
            "我也保留了可回復資料：",
            "",
            "- `b46451a`",
            "",
            "詳見 [文件](https://example.com/docs) 與 [click](javascript:alert(1))。",
          ].join("\n"),
          createdAt: "2026-08-19T03:00:00Z",
        }],
      },
    }),
  });
  assert.match(html, /<ul>/);
  assert.match(html, /<li>/);
  assert.match(html, /<strong>main<\/strong>/);
  assert.match(html, /<code class="inline-code">28a29ae<\/code>/);
  assert.match(html, /<code class="inline-code">origin\/main<\/code>/);
  assert.match(html, /<code class="inline-code">b46451a<\/code>/);
  assert.match(html, /<a href="https:\/\/example\.com\/docs" target="_blank" rel="noopener noreferrer">文件<\/a>/);
  assert.match(html, /\[click\]\(javascript:alert\(1\)\)/);
  assert.doesNotMatch(html, /href="javascript:/);
  assert.doesNotMatch(html, /-\s+本地/);
  assert.doesNotMatch(html, /`28a29ae`/);
  assert.doesNotMatch(html, /\*\*main\*\*/);
});

test("the remote transcript keeps fenced code copyable without printing fence markers", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [{
          id: "m1",
          role: "assistant",
          contentText: "```js\nconst x = 1;\n```",
          createdAt: "2026-08-19T03:00:00Z",
        }],
      },
    }),
  });
  assert.match(html, /class="code-block"/);
  assert.match(html, /class="copy-code"/);
  assert.match(html, /class="tok-keyword">const<\/span>/);
  assert.doesNotMatch(html, /```js/);
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

test("remote tool dumps fold into the local step stack instead of user bubbles", () => {
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
          { id: "m1", role: "user", contentText: "改網站", createdAt: "2026-08-19T03:00:00Z" },
          { id: "m2", role: "assistant", contentText: "我先看 Git。\nTool requested: Bash (call_1)", createdAt: "2026-08-19T03:01:00Z" },
          {
            id: "m3",
            role: "user",
            parentToolUseId: "call_1",
            contentText: "Tool Bash (call_1) completed:\n<script>alert(1)</script>\ngit log",
            createdAt: "2026-08-19T03:01:05Z",
          },
          { id: "m4", role: "assistant", contentText: "已切到 main。", createdAt: "2026-08-19T03:02:00Z" },
        ],
      },
    }),
  });
  assert.match(html, /data-message-id="m1"/);
  assert.match(html, /data-message-id="m4"/);
  assert.match(html, /已切到 main/);
  assert.match(html, /tool-activity-group/);
  assert.match(html, /tool-activity-summary/);
  assert.match(html, /disclosure-chevron/);
  assert.match(html, /data-tool-activity-select="call_1"/);
  assert.match(html, /1 個步驟|1 个步骤|1 steps/);
  assert.doesNotMatch(html, /data-message-id="m3"/);
  assert.doesNotMatch(html, /peer-collaboration-message[\s\S]*Tool Bash/);
  assert.doesNotMatch(html, /<script>alert/);
  assert.match(html, /我先看 Git/);
  assert.doesNotMatch(html, /Tool requested:/);
});

test("remote transcripts put host reasoning into the step stack instead of dropping it", () => {
  const html = renderPeerTranscriptHTML({
    conversation: {
      hostName: "Autoto",
      title: "國鋒專用",
      scopes: ["observe"],
    },
    snapshot: normalizePeerSnapshot({
      selectedAgent: {
        agentId: "agent-1",
        messages: [
          { id: "m1", role: "user", contentText: "改網站", createdAt: "2026-08-19T03:00:00Z" },
          {
            id: "m2",
            role: "assistant",
            contentText: "",
            reasoningText: "先確認目前分支。\n<script>alert(1)</script>",
            createdAt: "2026-08-19T03:01:00Z",
          },
          {
            id: "m3",
            role: "user",
            parentToolUseId: "call_1",
            contentText: "Tool Bash (call_1) completed:\ngit status",
            createdAt: "2026-08-19T03:01:05Z",
          },
          {
            id: "m4",
            role: "assistant",
            contentText: "已切到 main。",
            reasoningText: "工具結果夠了，可以直接回。",
            createdAt: "2026-08-19T03:02:00Z",
          },
        ],
      },
    }),
  });
  assert.match(html, /tool-activity-reasoning-step/);
  assert.match(html, /tool-activity-icon-thinking/);
  assert.match(html, /先確認目前分支/);
  assert.match(html, /工具結果夠了/);
  assert.match(html, /思考 2 · 1 個步驟|思考 2 · 1 个步骤|Thought 2 · 1 steps/);
  assert.match(html, /已切到 main/);
  assert.doesNotMatch(html, /data-message-id="m2"/);
  assert.doesNotMatch(html, /<script>alert/);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
});

test("older hosts without parentToolUseId still fold Tool completed lines into steps", () => {
  const parsed = parsePeerToolResultMessage({
    id: "m3",
    role: "user",
    contentText: "Tool Bash (call_KCnABMYGlIxEYg1asp1SfWxo) completed:\nHEAD is now at 28a29ae",
  });
  assert.equal(parsed.toolName, "Bash");
  assert.equal(parsed.toolUseId, "call_KCnABMYGlIxEYg1asp1SfWxo");
  assert.equal(parsed.status, "completed");
  assert.match(parsed.output, /HEAD is now at 28a29ae/);
  const items = peerTranscriptItems([
    { id: "m1", role: "user", contentText: "改網站" },
    { id: "m3", role: "user", contentText: "Tool Bash (call_1) completed:\ngit stash" },
    { id: "m4", role: "assistant", contentText: "好了" },
  ]);
  assert.equal(items.length, 3);
  assert.equal(items[1].type, "tools");
  assert.equal(items[1].tools.length, 1);
  assert.equal(items[1].stackKey, "peer:m3");
  assert.deepEqual(items[1].reasoningSteps, []);
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
      if (path.endsWith("/runtime")) {
        return {
          agentId: "agent-1",
          model: "google:gemini-3-flash",
          reasoningEffort: "high",
          permissionMode: "acceptEdits",
          permissionModeCap: "acceptEdits",
        };
      }
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
  await controller.updateRuntime({ model: "google:gemini-3-flash", reasoningEffort: "high", permissionMode: "acceptEdits" });
  const runtime = requests.find((entry) => entry.method === "POST" && entry.path.endsWith("/runtime"));
  assert.deepEqual(runtime.body, { model: "google:gemini-3-flash", reasoningEffort: "high", permissionMode: "acceptEdits" });
  assert.equal(controller.selectedSummary()?.model, "google:gemini-3-flash");
  assert.equal(controller.selectedSummary()?.reasoningEffort, "high");
  assert.equal(controller.selectedSummary()?.permissionMode, "acceptEdits");
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

test("peer snapshot errors map sharing-off, credential, and unreachable cases", () => {
  const sharingOff = new Error("remote collaboration is unavailable");
  sharingOff.status = 503;
  assert.match(peerSnapshotErrorCopy(sharingOff), /重啟後還沒再打開|重启后还没再打开|after a restart/);
  const unauthorized = new Error("peer authentication failed");
  unauthorized.status = 409;
  assert.match(peerSnapshotErrorCopy(unauthorized), /憑證已失效|凭证已失效|credential is no longer valid/);
  const unreachable = new Error("peer protocol validation failed");
  unreachable.status = 502;
  assert.match(peerSnapshotErrorCopy(unreachable), /連不上對方|连不上对方|Could not reach the other Autoto/);
});

test("a host snapshot 409 keeps the pairing visible and does not block later polls", async () => {
  let calls = 0;
  const controller = createPeerCollaborationWorkspaceController({
    request: async (path) => {
      calls += 1;
      if (path.endsWith("/status")) {
        return { pairings: [{ pairing, grants: [] }] };
      }
      const error = new Error("peer authentication failed");
      error.status = 409;
      throw error;
    },
  });
  await controller.refresh({ force: true });
  await controller.refresh({ force: true });
  assert.ok(calls > 1);
  const html = controller.renderNavigationHTML();
  assert.match(html, /data-peer-snapshot-retry="pairing-1"/);
  assert.match(html, /憑證已失效|凭证已失效|credential is no longer valid/);
  assert.doesNotMatch(html, /data-peer-target/);
});

test("a host snapshot 503 keeps the pairing visible and offers retry", async () => {
  const controller = createPeerCollaborationWorkspaceController({
    request: async (path) => {
      if (path.endsWith("/status")) {
        return { pairings: [{ pairing, grants: [] }] };
      }
      const error = new Error("remote collaboration is unavailable");
      error.status = 503;
      throw error;
    },
  });
  await controller.refresh({ force: true });
  const html = controller.renderNavigationHTML();
  assert.match(html, /https:\/\/autoto\.example/);
  assert.match(html, /data-peer-snapshot-retry="pairing-1"/);
  assert.match(html, /重啟後還沒再打開|重启后还没再打开|after a restart/);
  assert.doesNotMatch(html, /data-peer-target/);
});
