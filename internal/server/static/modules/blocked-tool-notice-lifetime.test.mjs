import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// These notices are one-time: they explain the turn that hit the refusal. Kept
// past it, they wait for the next stop and land as if they explained that one.
// That is what put three unrelated Bash refusals above the composer at once,
// including on a manual stop, which the reader chose and needs no reason for.
//
// The dismiss bug had the same shape from the other side: clearing the entry
// without re-rendering left the row on screen, so the close button did nothing
// visible while the existing test still passed on state alone.

// renderApprovalCards finds the existing stack with querySelector and either
// replaces it through outerHTML or removes it outright. A stub that always
// returns null never reaches the removal branch, which is the branch that has to
// run for a dismissed row to leave the screen, so the stub has to honour both.
function fakeMessagesElement() {
  const classes = new Set(["empty"]);
  const el = {
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      contains: (name) => classes.has(name),
    },
    innerHTML: "",
    querySelector(selector) {
      if (selector !== "[data-approval-stack]") return null;
      if (!this.innerHTML.includes("data-approval-stack")) return null;
      const host = this;
      return {
        remove() { host.innerHTML = ""; },
        set outerHTML(next) { host.innerHTML = next; },
        get outerHTML() { return host.innerHTML; },
      };
    },
    querySelectorAll: () => [],
    insertAdjacentHTML(_position, html) { this.innerHTML += html; },
    addEventListener() {},
    scrollHeight: 100,
    clientHeight: 0,
    scrollTop: 0,
  };
  return el;
}

function harness(stateOverrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project", status: "idle" },
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

function blocked(id, warning, agentId = "agent-1") {
  return { agentId, data: { toolUseId: id, toolName: "Bash", risk: "danger", warning } };
}

// Counts rows actually in the transcript element. Nothing here calls render:
// the point is whether the controller rendered on its own, which is precisely
// what asserting on state alone could not see.
function renderedNoticeCount(messagesElement) {
  return (messagesElement.innerHTML.match(/data-blocked-tool-notice=/g) || []).length;
}

test("dismissing a notice removes its row, not just its state entry", () => {
  const { state, controller, messagesElement, restore } = harness();
  try {
    controller.rememberToolApproval(blocked("blocked-1", "recursive delete"));
    assert.equal(renderedNoticeCount(messagesElement), 1);
    controller.dismissBlockedToolNotice("blocked-1");
    assert.deepEqual(state.blockedToolNotices, {});
    assert.equal(
      renderedNoticeCount(messagesElement),
      0,
      "dismiss cleared state but left the row rendered",
    );
  } finally {
    restore();
  }
});

test("refusals from earlier turns do not survive into a later stop", () => {
  const { state, controller, messagesElement, restore } = harness();
  try {
    controller.rememberToolApproval(blocked("blocked-1", "recursive delete"));
    controller.rememberToolApproval(blocked("blocked-2", "pipe to interpreter"));
    controller.rememberToolApproval(blocked("blocked-3", "truncate to zero"));
    assert.equal(renderedNoticeCount(messagesElement), 3);
    controller.clearBlockedToolNotices("agent-1");
    assert.deepEqual(state.blockedToolNotices, {});
    assert.equal(renderedNoticeCount(messagesElement), 0);
  } finally {
    restore();
  }
});

test("clearing one agent leaves another agent's refusals alone", () => {
  const { state, controller, restore } = harness();
  try {
    controller.rememberToolApproval(blocked("blocked-1", "recursive delete"));
    controller.rememberToolApproval(blocked("blocked-2", "other agent", "agent-2"));
    controller.clearBlockedToolNotices("agent-1");
    assert.deepEqual(Object.keys(state.blockedToolNotices), ["blocked-2"]);
  } finally {
    restore();
  }
});

test("clearing when there is nothing to clear leaves state untouched", () => {
  const { state, controller, restore } = harness();
  try {
    controller.clearBlockedToolNotices("agent-1");
    assert.deepEqual(state.blockedToolNotices, {});
  } finally {
    restore();
  }
});

test("a refusal stays hidden while the agent is still working", () => {
  const { state, controller, messagesElement, restore } = harness();
  try {
    state.agent = { ...state.agent, status: "running" };
    controller.rememberToolApproval(blocked("blocked-1", "recursive delete"));
    assert.equal(renderedNoticeCount(messagesElement), 0);
    state.agent = { ...state.agent, status: "idle" };
    assert.match(controller.renderApprovalCardsHTML(), /data-blocked-tool-notice="blocked-1"/);
  } finally {
    restore();
  }
});
