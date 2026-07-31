import test from "node:test";
import assert from "node:assert/strict";

import { createChatRenderingController } from "./chat-rendering.mjs";

// Reading further back is a scroll now, not a button press. These cover the
// sentinel that drives it -- in particular that it survives the full-container
// rebuild every unrelated render performs, which is what used to leave history
// unreachable after a tool finished.

function fakeSentinel() {
  return { name: "sentinel", hidden: false, dataset: {}, addEventListener() {} };
}

// Mirrors the real container closely enough for the sentinel path: a rebuild
// replaces children, so each render hands back a *new* sentinel node.
function fakeMessagesElement() {
  const classes = new Set(["empty"]);
  const element = {
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      contains: (name) => classes.has(name),
    },
    innerHTML: "",
    sentinels: [],
    querySelector(selector) {
      if (selector === "[data-history-sentinel]" && element.innerHTML.includes("data-history-sentinel")) {
        const sentinel = fakeSentinel();
        element.sentinels.push(sentinel);
        return sentinel;
      }
      if (selector === "[data-load-older-messages]") return { addEventListener() {} };
      return null;
    },
    querySelectorAll: () => [],
    insertAdjacentHTML(_position, html) { element.innerHTML += html; },
    scrollHeight: 4000,
    scrollTop: 2000,
    clientHeight: 800,
  };
  return element;
}

function installFakeIntersectionObserver() {
  const instances = [];
  class FakeIntersectionObserver {
    constructor(callback, options) {
      this.callback = callback;
      this.options = options;
      this.observed = [];
      this.disconnected = false;
      instances.push(this);
    }

    observe(node) { this.observed.push(node); }

    disconnect() { this.disconnected = true; }

    // Drives the callback the way a real scroll would.
    trigger() { this.callback([{ isIntersecting: true }]); }
  }
  const previous = globalThis.IntersectionObserver;
  globalThis.IntersectionObserver = FakeIntersectionObserver;
  return {
    instances,
    // The observer actually watching right now: earlier ones are disconnected.
    active: () => instances.filter((instance) => !instance.disconnected).at(-1) || null,
    restore: () => { globalThis.IntersectionObserver = previous; },
  };
}

function harness({ hasMoreBefore = true, withObserver = true, onOlderRequest = null } = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const observer = withObserver ? installFakeIntersectionObserver() : { active: () => null, instances: [], restore() {} };

  const requests = [];
  const state = {
    agent: { id: "agent-1", cwd: "/work" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveAssistantActive: false,
    liveAssistantText: "",
    liveAssistantRequestId: "",
    liveAssistantRunId: "",
    liveAssistantStartedAt: "",
    liveAssistantPerformance: null,
    liveImageGenerations: {},
    pendingToolApprovals: {},
    activeRunSummary: null,
    activeRunSummaryRunId: "",
    runSummaryLoading: false,
    runSummaryError: "",
    messageHasMoreBefore: hasMoreBefore,
    messageNextBefore: "cursor-1",
    messageOlderLoading: false,
  };

  const controller = createChatRenderingController({
    state,
    attachmentIcon: () => "file",
    attachmentKind: () => "file",
    apiRequest: async (url) => {
      requests.push(url);
      if (onOlderRequest) return onOlderRequest(url);
      return { messages: [{ id: "older-1", role: "user", contentText: "older" }], hasMoreBefore: false, nextBefore: "" };
    },
    copyToClipboard: async () => true,
    notifyTerminal: () => {},
    selectedModelValue: () => "",
    shortPath: (value) => value,
    showError: () => {},
    showToast: () => {},
  });

  const messages = [{ id: "m1", role: "user", contentText: "hello" }];
  const render = () => controller.applyMessageSnapshot(messages, "agent-1", { preserveScroll: true });
  render();

  return {
    state,
    controller,
    messagesElement,
    observer,
    requests,
    render,
    cleanup: () => {
      observer.restore();
      globalThis.document = previousDocument;
    },
  };
}

test("history renders a scroll sentinel and observes it inside the message container", () => {
  const h = harness();
  try {
    assert.match(h.messagesElement.innerHTML, /data-history-sentinel/);
    const active = h.observer.active();
    assert.ok(active, "expected an observer to be watching");
    assert.equal(active.observed.length, 1);
    assert.equal(active.options.root, h.messagesElement);
    // Loading starts before the sentinel is on screen so the page is usually
    // already there when the user arrives.
    assert.match(String(active.options.rootMargin), /^\d+px 0px 0px 0px$/);
  } finally {
    h.cleanup();
  }
});

// The regression this phase exists to prevent: every render replaces the
// container's children, so an observer left pointing at the previous sentinel
// silently stops firing and history becomes unreachable by scrolling.
test("the sentinel is re-observed after an unrelated rebuild replaces the container", () => {
  const h = harness();
  try {
    const first = h.observer.active();
    const firstNode = first.observed[0];

    h.render();

    const second = h.observer.active();
    assert.ok(second, "expected an observer after the rebuild");
    assert.notEqual(second, first, "expected the stale observer to be replaced");
    assert.equal(first.disconnected, true, "expected the stale observer to be disconnected");
    assert.equal(second.observed.length, 1);
    assert.notEqual(second.observed[0], firstNode, "expected the fresh sentinel node to be observed");
  } finally {
    h.cleanup();
  }
});

test("scrolling the sentinel into view loads the page before it", async () => {
  const h = harness();
  try {
    h.observer.active().trigger();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(h.requests.length, 1);
    assert.match(h.requests[0], /before=cursor-1/);
  } finally {
    h.cleanup();
  }
});

test("one intersection cannot request the same page twice", async () => {
  const h = harness();
  try {
    const active = h.observer.active();
    active.trigger();
    active.trigger();
    await new Promise((resolve) => setTimeout(resolve, 0));
    assert.equal(h.requests.length, 1);
    assert.equal(active.disconnected, true, "expected the observer to stop after firing");
  } finally {
    h.cleanup();
  }
});

test("no sentinel is observed once there is no more history to load", () => {
  const h = harness({ hasMoreBefore: false });
  try {
    assert.doesNotMatch(h.messagesElement.innerHTML, /data-history-sentinel/);
    assert.equal(h.observer.active(), null);
  } finally {
    h.cleanup();
  }
});

test("a load already in flight is not re-observed while it settles", () => {
  const h = harness();
  try {
    h.state.messageOlderLoading = true;
    h.render();
    assert.equal(h.observer.active(), null, "expected no observer while a load is in flight");
  } finally {
    h.cleanup();
  }
});

// Without IntersectionObserver there is nothing to hang scroll-to-load off, so
// the button has to stay reachable rather than being hidden behind a gesture
// that cannot fire.
test("the fallback button stays visible when IntersectionObserver is unavailable", () => {
  const h = harness({ withObserver: false });
  try {
    assert.match(h.messagesElement.innerHTML, /data-load-older-messages(?![^>]*hidden)/);
  } finally {
    h.cleanup();
  }
});

test("the fallback button is hidden while scroll-to-load is doing the work", () => {
  const h = harness();
  try {
    assert.match(h.messagesElement.innerHTML, /data-load-older-messages hidden/);
  } finally {
    h.cleanup();
  }
});

// Retrying on every scroll against a failing endpoint would hammer it, so
// repeated failures hand control back to the user.
test("repeated failures fall back to the manual button instead of retrying on scroll", async () => {
  let calls = 0;
  const h = harness({
    onOlderRequest: () => {
      calls += 1;
      throw new Error("network down");
    },
  });
  try {
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const active = h.observer.active();
      if (!active) break;
      active.trigger();
      await new Promise((resolve) => setTimeout(resolve, 0));
      h.render();
    }
    assert.ok(calls >= 2, `expected the failing endpoint to be retried a bounded number of times, saw ${calls}`);
    assert.equal(h.observer.active(), null, "expected scroll-to-load to stand down after repeated failures");
    assert.match(h.messagesElement.innerHTML, /data-load-older-messages(?![^>]*hidden)/);
  } finally {
    h.cleanup();
  }
});

test("switching conversations restores scroll-to-load after an earlier failure", async () => {
  let failing = true;
  const h = harness({
    onOlderRequest: () => {
      if (failing) throw new Error("network down");
      return { messages: [], hasMoreBefore: false, nextBefore: "" };
    },
  });
  try {
    for (let attempt = 0; attempt < 2; attempt += 1) {
      const active = h.observer.active();
      if (!active) break;
      active.trigger();
      await new Promise((resolve) => setTimeout(resolve, 0));
      h.render();
    }
    assert.equal(h.observer.active(), null);

    failing = false;
    // What app-main calls when leaving a conversation (disconnectAgentTransports).
    h.controller.invalidateMessageLifecycle();
    h.state.messageHasMoreBefore = true;
    h.state.messageNextBefore = "cursor-2";
    h.render();
    assert.ok(h.observer.active(), "expected a fresh conversation to observe again");
  } finally {
    h.cleanup();
  }
});
