import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// A hard-blocked call used to be filed under pendingToolApprovals, so the card
// appeared and then vanished when tool.finished cleared it. All that survived was
// a toast, and the reason was gone before it could be read. These tests pin the
// two properties that made the notice unreadable: it must stay after the call
// finishes, and it must not count as a turn still waiting for a decision.
function fakeMessagesElement() {
  const classes = new Set(["empty"]);
  return {
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      contains: (name) => classes.has(name),
    },
    innerHTML: "",
    querySelector: () => null,
    querySelectorAll: () => [],
    insertAdjacentHTML(_position, html) { this.innerHTML += html; },
    addEventListener() {},
    scrollHeight: 100,
    clientHeight: 0,
    scrollTop: 0,
  };
}

function harness(stateOverrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveImageGenerations: {},
    pendingToolApprovals: {},
    blockedToolNotices: {},
    pendingUserQuestions: {},
    ...stateOverrides,
  };
  const controller = createChatRenderingController({
    state,
    attachmentIcon: () => "file",
    attachmentKind: () => "file",
    copyToClipboard: async () => true,
    notifyTerminal: () => {},
    selectedModelValue: () => "",
    shortPath: (value) => value,
    showError: () => {},
    showToast: () => {},
  });
  return { state, controller, messagesElement, restore: () => { globalThis.document = previousDocument; } };
}

const dangerEvent = {
  agentId: "agent-1",
  createdAt: "2026-08-09T06:00:00.000Z",
  data: {
    toolUseId: "blocked-1",
    toolName: "Bash",
    risk: "danger",
    warning: "This truncates an existing file to zero length.",
  },
};

test("a danger block is recorded as a notice, not as a pending approval", () => {
  const { state, controller, restore } = harness();
  try {
    controller.rememberToolApproval(dangerEvent);
    // The distinction is the whole fix: agentTurnInFlight() treats any pending
    // approval as a live turn, so filing a decided block there locks the composer
    // on a decision nobody is able to make.
    assert.deepEqual(state.pendingToolApprovals, {});
    assert.equal(state.blockedToolNotices["blocked-1"].warning, "This truncates an existing file to zero length.");
  } finally {
    restore();
  }
});

test("the notice survives tool.finished clearing the approval", () => {
  const { state, controller, restore } = harness();
  try {
    controller.rememberToolApproval(dangerEvent);
    controller.clearToolApproval("blocked-1");
    assert.ok(state.blockedToolNotices["blocked-1"], "the reason must outlive the call that was refused");
  } finally {
    restore();
  }
});

test("the notice renders as a single dismissible error row carrying the reason", () => {
  const { controller, messagesElement, restore } = harness({
    blockedToolNotices: {
      "blocked-1": {
        toolUseId: "blocked-1",
        agentId: "agent-1",
        toolName: "Bash",
        warning: "This truncates an existing file to zero length.",
        createdAt: "2026-08-09T06:00:00.000Z",
      },
    },
  });
  try {
    assert.equal(controller.applyMessageSnapshot([], "agent-1", {}), true);
    const html = messagesElement.innerHTML;
    // Same component as the run failure row, so the two read as one idea.
    assert.match(html, /class="conversation-run-notice error"/);
    assert.match(html, /data-blocked-tool-notice="blocked-1"/);
    assert.match(html, /This truncates an existing file to zero length\./);
    assert.match(html, /data-blocked-notice-dismiss="blocked-1"/);
    // Retry is deliberately absent: a hard block is reached again every time.
    assert.doesNotMatch(html, /data-run-retry/);
  } finally {
    restore();
  }
});

test("dismissing forgets the notice so a later re-render cannot restore it", () => {
  const { state, controller, restore } = harness();
  try {
    controller.rememberToolApproval(dangerEvent);
    controller.dismissBlockedToolNotice("blocked-1");
    assert.deepEqual(state.blockedToolNotices, {});
  } finally {
    restore();
  }
});

test("a non-danger approval still becomes a pending approval", () => {
  const { state, controller, restore } = harness();
  try {
    controller.rememberToolApproval({
      agentId: "agent-1",
      data: { toolUseId: "exec-1", toolName: "Bash", risk: "exec", warning: "needs review" },
    });
    assert.equal(state.pendingToolApprovals["exec-1"].toolUseId, "exec-1");
    assert.deepEqual(state.blockedToolNotices, {});
  } finally {
    restore();
  }
});
