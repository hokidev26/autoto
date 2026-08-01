import test from "node:test";
import assert from "node:assert/strict";

import { t as chatRenderingExtraText } from "./messages-chat-rendering-extra.mjs";
import {
  chatMessagePresentation,
  createChatRenderingController,
  findToolActivityByIdentity,
  formatTurnUsagePerformance,
  generatedImageURL,
  isAgentToolActivity,
  isTranscriptMessageVisible,
  messageContentBlocks,
  normalizeAgentPlan,
  normalizeAgentTaskActivity,
  normalizeGeneratedImageBlocks,
  normalizeImageGenerationStatusEvent,
  normalizeMessageProfileIdentity,
  normalizeToolActivity,
  normalizeTurnUsage,
  nextToolActivitySelection,
  renderAgentTaskActivityCardHTML,
  renderToolActivityCardHTML,
  reasoningStepTitle,
  renderToolActivityStackHTML,
  renderToolDiffHTML,
  transcriptMessageText,
} from "./chat-rendering.mjs";

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
    scrollHeight: 100,
    scrollTop: 0,
  };
}

function renderSnapshot(messages, stateOverrides = {}, applyOptions = {}, controllerOptions = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById(id) {
      return id === "messages" ? messagesElement : null;
    },
  };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project" },
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
    ...stateOverrides,
  };
  try {
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
      ...controllerOptions,
    });
    assert.equal(controller.applyMessageSnapshot(messages, "agent-1", applyOptions), true);
    return { html: messagesElement.innerHTML, state, controller, messagesElement };
  } finally {
    globalThis.document = previousDocument;
  }
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function createAsyncChatRenderingHarness(apiRequest, stateOverrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = {
    getElementById(id) {
      return id === "messages" ? messagesElement : null;
    },
  };
  const state = {
    agent: { id: "agent-a", cwd: "/work/project" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    messageHasMoreBefore: false,
    messageNextBefore: "",
    messageOlderLoading: false,
    liveToolOutputs: {},
    liveAssistantActive: false,
    liveAssistantText: "",
    liveImageGenerations: {},
    pendingToolApprovals: {},
    activeRunSummary: null,
    activeRunSummaryRunId: "",
    runSummaryLoading: false,
    runSummaryError: "",
    ...stateOverrides,
  };
  const controller = createChatRenderingController({
    state,
    apiRequest,
    attachmentIcon: () => "file",
    attachmentKind: () => "file",
    copyToClipboard: async () => true,
    notifyTerminal: () => {},
    selectedModelValue: () => "",
    shortPath: (value) => value,
    showError: () => {},
    showToast: () => {},
  });
  return {
    controller,
    messagesElement,
    state,
    restore() { globalThis.document = previousDocument; },
  };
}

test("explicit transcript scrolling follows the newest message across mobile layout frames", () => {
  const harness = createAsyncChatRenderingHarness(async () => ({}));
  const previousRAF = globalThis.requestAnimationFrame;
  const previousTimeout = globalThis.setTimeout;
  const frames = [];
  const timers = [];
  globalThis.requestAnimationFrame = (callback) => {
    frames.push(callback);
    return frames.length;
  };
  globalThis.setTimeout = (callback, delay) => {
    timers.push({ callback, delay });
    return timers.length;
  };
  try {
    harness.messagesElement.scrollTop = 0;
    assert.equal(harness.controller.scrollMessagesToBottom(), true);
    assert.equal(harness.messagesElement.scrollTop, harness.messagesElement.scrollHeight);
    assert.equal(frames.length, 1);
    assert.deepEqual(timers.map(({ delay }) => delay), [320]);

    frames.shift()();
    assert.equal(harness.messagesElement.scrollTop, harness.messagesElement.scrollHeight);
    assert.equal(frames.length, 1);
    frames.shift()();
    timers[0].callback();
    assert.equal(harness.messagesElement.scrollTop, harness.messagesElement.scrollHeight);
  } finally {
    globalThis.requestAnimationFrame = previousRAF;
    globalThis.setTimeout = previousTimeout;
    harness.restore();
  }
});

test("chatMessagePresentation keeps user semantics while aligning messages left", () => {
  assert.deepEqual(chatMessagePresentation({ role: "user" }).alignment, "left");
  assert.deepEqual(chatMessagePresentation({ role: "user" }).roleClass, "user");
  assert.deepEqual(chatMessagePresentation({ role: "HUMAN" }).alignment, "left");
  assert.deepEqual(chatMessagePresentation({ role: "HUMAN" }).roleClass, "user");
  for (const role of ["assistant", "tool", "system", "error", "task_report", "legacy", ""]) {
    assert.equal(chatMessagePresentation({ role }).alignment, "left", role);
  }
  const persistedToolResult = chatMessagePresentation({ role: "user", parentToolUseId: "tool-1" });
  assert.equal(persistedToolResult.alignment, "left");
  assert.equal(persistedToolResult.roleClass, "assistant");
  assert.equal(persistedToolResult.normalizedRole, "tool");
});

test("message profile identity normalizes current profile fields with safe fallbacks", () => {
  assert.deepEqual(normalizeMessageProfileIdentity({
    displayName: "  ererer  ",
    workspaceLabel: "Workspace",
    avatarInitials: "xy",
  }), {
    displayName: "ererer",
    avatarInitials: "XY",
    avatarDataUrl: "",
  });
  assert.deepEqual(normalizeMessageProfileIdentity({ displayName: "", workspaceLabel: "Local space", avatarInitials: "" }), {
    displayName: "Local space",
    avatarInitials: "AT",
    avatarDataUrl: "",
  });
  assert.deepEqual(normalizeMessageProfileIdentity(null), {
    displayName: "Autoto User",
    avatarInitials: "AT",
    avatarDataUrl: "",
  });
});

test("chat hydration batches message snapshots until the final forced render", () => {
  const deferred = renderSnapshot([{ role: "assistant", contentText: "ready" }], { chatHydrating: true });
  assert.equal(deferred.html, "");
  assert.equal(deferred.state.currentMessages[0].contentText, "ready");

  const committed = renderSnapshot([{ role: "assistant", contentText: "ready" }], { chatHydrating: true }, { forceRender: true });
  assert.match(committed.html, /ready/);
});

test("message lifecycle rejects a delayed A response after A to B to A navigation", async () => {
  const requests = [];
  const harness = createAsyncChatRenderingHarness((_path, options = {}) => {
    const pending = deferred();
    requests.push({ ...pending, signal: options.signal });
    return pending.promise;
  });
  try {
    const first = harness.controller.loadMessages("agent-a");
    assert.equal(requests.length, 1);

    harness.controller.invalidateMessageLifecycle();
    assert.equal(requests[0].signal?.aborted, true);
    harness.state.agent = { id: "agent-b" };
    harness.controller.invalidateMessageLifecycle();
    harness.state.agent = { id: "agent-a" };

    const second = harness.controller.loadMessages("agent-a");
    assert.equal(requests.length, 2);
    requests[1].resolve({ messages: [{ id: "new-a", role: "assistant", contentText: "new lifecycle" }] });
    assert.equal(await second, true);
    assert.equal(harness.state.currentMessages[0].id, "new-a");

    requests[0].resolve({ messages: [{ id: "old-a", role: "assistant", contentText: "stale lifecycle" }] });
    assert.equal(await first, false);
    assert.equal(harness.state.currentMessages[0].id, "new-a");
    assert.doesNotMatch(harness.messagesElement.innerHTML, /stale lifecycle/);
  } finally {
    harness.restore();
  }
});

test("invalidated older-message requests cannot clear the next lifecycle loading state", async () => {
  const pending = deferred();
  const harness = createAsyncChatRenderingHarness((_path, options = {}) => {
    pending.signal = options.signal;
    return pending.promise;
  }, {
    currentMessages: [{ id: "current", role: "assistant", contentText: "current" }],
    messageHasMoreBefore: true,
    messageNextBefore: "cursor-1",
  });
  try {
    const loading = harness.controller.loadOlderMessages("agent-a");
    assert.equal(harness.state.messageOlderLoading, true);
    harness.controller.invalidateMessageLifecycle();
    assert.equal(pending.signal?.aborted, true);
    harness.state.agent = { id: "agent-b" };
    harness.state.messageOlderLoading = true;
    pending.resolve({ messages: [{ id: "older", role: "user", contentText: "older" }] });
    assert.equal(await loading, false);
    assert.equal(harness.state.messageOlderLoading, true);
    assert.deepEqual(harness.state.currentMessages.map((message) => message.id), ["current"]);
  } finally {
    harness.restore();
  }
});

test("tool activity lookup requires the stable run and tool identity across activity stores", () => {
  const live = { live: { runId: "run-1", toolUseId: "tool-1", toolName: "Agent" } };
  const persisted = [{ run_id: "run-2", tool_use_id: "tool-2", tool_name: "Agent" }];
  assert.equal(findToolActivityByIdentity([live, persisted], "run-1", "tool-1"), live.live);
  assert.equal(findToolActivityByIdentity([live, persisted], "run-2", "tool-2"), persisted[0]);
  assert.equal(findToolActivityByIdentity([live, persisted], "", "tool-1"), null);
  assert.equal(findToolActivityByIdentity([live, persisted], "run-1", "missing"), null);
});

test("message rendering aligns messages left and uses the current profile for user identities", () => {
  const { html, state } = renderSnapshot([
    { role: "user", contentText: "hello", createdAt: "2026-02-03T04:05:06Z" },
    { role: "HUMAN", contentText: "human alias" },
    { role: "assistant", contentText: "reply" },
    { role: "tool", contentText: "legacy result" },
    { role: "assistant", contentText: "streaming", streaming: true },
  ], {
    profile: { displayName: "er<erer", workspaceLabel: "Workspace", avatarInitials: "xy" },
  });

  assert.match(html, /class="message user chat-message chat-flow-item chat-flow-left" data-chat-alignment="left"/);
  assert.equal((html.match(/data-chat-alignment="left" data-message-role=/g) || []).length, 4);
  assert.match(html, /class="message user chat-message chat-flow-item chat-flow-left"[^>]*data-message-role="human"/);
  assert.doesNotMatch(html, /data-message-role="tool"|legacy result/);
  assert.equal((html.match(/data-user-profile-avatar>XY<\/span>/g) || []).length, 2);
  assert.equal((html.match(/data-user-profile-name>er&lt;erer<\/span>/g) || []).length, 2);
  assert.doesNotMatch(html, /er<erer/);
  // The assistant identifies by the product mark, not by a role initial.
  assert.match(html, /class="message-avatar" aria-hidden="true"><svg[^>]*viewBox="0 0 64 64"/);
  assert.doesNotMatch(html, /class="message-avatar" aria-hidden="true">A<\/span>/);
  assert.match(html, /<time class="message-time" datetime="2026-02-03T04:05:06Z" title="[^"]+">[^<]+<\/time>/);
  assert.ok(html.indexOf('class="message-meta"') < html.indexOf('class="message-head-actions"'));
  assert.ok(html.indexOf('class="message-head-actions"') < html.indexOf('class="message-time"'));
  assert.equal((html.match(/data-copy-message=/g) || []).length, 4);
  assert.deepEqual(state.messageCopyTexts, ["hello", "human alias", "reply", "streaming"]);
});

test("internal tool protocol messages stay out of the transcript while activity remains", () => {
  const toolUseOnly = {
    id: "tool-use-only",
    role: "assistant",
    contentText: "Tool requested: Bash (call-1)",
    contentJson: [{ type: "tool_use", toolUseId: "call-1", toolName: "Bash", input: { command: "ls -la" } }],
    turnUsage: { outputTokens: 1, tokensPerSecond: 107703.1, ttftMs: 1600 },
  };
  const toolResult = {
    id: "tool-result",
    role: "user",
    parentToolUseId: "call-1",
    contentText: "Tool Bash (call-1) completed:\n/c/Users/Ray/Desktop/autoto\ntotal 44",
    contentJson: [{ type: "tool_result", toolUseId: "call-1", toolName: "Bash", output: "/c/Users/Ray/Desktop/autoto\ntotal 44" }],
  };
  const mixedAssistant = {
    id: "mixed-assistant",
    role: "assistant",
    contentText: "I will inspect first.\nTool requested: Read (call-2)",
    contentJson: [
      { type: "text", text: "I will inspect first." },
      { type: "tool_use", toolUseId: "call-2", toolName: "Read", input: { file_path: "README.md" } },
    ],
  };
  const legacyToolUseOnly = {
    id: "legacy-tool-use-only",
    role: "assistant",
    contentText: "Tool requested: Read (call-legacy)",
  };

  assert.equal(isTranscriptMessageVisible(toolUseOnly), false);
  assert.equal(isTranscriptMessageVisible(toolResult), false);
  assert.equal(isTranscriptMessageVisible(legacyToolUseOnly), false);
  assert.equal(transcriptMessageText(mixedAssistant), "I will inspect first.");

  const { html, state } = renderSnapshot([
    toolUseOnly,
    toolResult,
    mixedAssistant,
    legacyToolUseOnly,
    { id: "final", role: "assistant", contentText: "Inspection complete." },
  ], {
    liveToolOutputs: {
      "call-1": {
        agentId: "agent-1",
        runId: "run-1",
        toolUseId: "call-1",
        toolName: "Bash",
        status: "completed",
        inputJson: { command: "ls -la" },
        output: "/c/Users/Ray/Desktop/autoto\ntotal 44",
      },
    },
  });

  assert.equal(state.currentMessages.length, 5, "raw context messages remain available for pagination and refresh");
  assert.deepEqual(state.messageCopyTexts, ["I will inspect first.", "Inspection complete."]);
  assert.match(html, /I will inspect first\./);
  assert.match(html, /Inspection complete\./);
  assert.match(html, /data-live-tool-output-stack/);
  assert.match(html, /data-tool-activity-select="call-1"/);
  assert.doesNotMatch(html, /Tool requested:|Tool Bash \(call-1\) completed|total 44|107703\.1|data-message-role="tool"/);
});

test("message rendering uses a normalized profile JPEG when one is available", () => {
  const { html } = renderSnapshot([{ role: "user", contentText: "photo" }], {
    profile: { displayName: "Photo user", avatarInitials: "PU", avatarDataUrl: "data:image/jpeg;base64,AAAA" },
  });
  assert.match(html, /<img class="message-avatar-image" data-user-profile-avatar-image src="data:image\/jpeg;base64,AAAA" alt="" aria-hidden="true" \/>/);
  assert.doesNotMatch(html, />PU<\/span>/);
});

test("profile identity refresh updates existing user nodes without rerendering the transcript", () => {
  const previousDocument = globalThis.document;
  const avatars = [{ textContent: "OLD" }, { textContent: "OLD" }];
  const names = [{ textContent: "Old user" }, { textContent: "Old user" }];
  const messagesElement = {
    innerHTML: "preserved transcript",
    querySelectorAll(selector) {
      if (selector === "[data-user-profile-avatar]") return avatars;
      if (selector === "[data-user-profile-name]") return names;
      return [];
    },
  };
  globalThis.document = {
    getElementById(id) {
      return id === "messages" ? messagesElement : null;
    },
  };
  const state = {
    agent: { id: "agent-1" },
    profile: { displayName: "Next <User>", avatarInitials: "nu" },
  };
  try {
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
    assert.equal(controller.refreshUserMessageIdentity(), true);
    assert.deepEqual(avatars.map((node) => node.textContent), ["NU", "NU"]);
    assert.deepEqual(names.map((node) => node.textContent), ["Next <User>", "Next <User>"]);
    assert.equal(messagesElement.innerHTML, "preserved transcript");
    assert.equal(controller.refreshUserMessageIdentity(), false);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("profile identity refresh replaces existing avatar initials with the saved JPEG", () => {
  const previousDocument = globalThis.document;
  const avatar = { textContent: "OLD", innerHTML: "", querySelector: () => null };
  const name = { textContent: "Old user" };
  const messagesElement = {
    innerHTML: "preserved transcript",
    querySelectorAll(selector) {
      if (selector === "[data-user-profile-avatar]") return [avatar];
      if (selector === "[data-user-profile-name]") return [name];
      return [];
    },
  };
  globalThis.document = { getElementById: (id) => id === "messages" ? messagesElement : null };
  try {
    const state = { agent: { id: "agent-1" }, profile: { displayName: "Photo user", avatarInitials: "PU", avatarDataUrl: "data:image/jpeg;base64,AAAA" } };
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
    assert.equal(controller.refreshUserMessageIdentity(), true);
    assert.match(avatar.innerHTML, /data-user-profile-avatar-image[^>]*data:image\/jpeg;base64,AAAA/);
    assert.equal(name.textContent, "Photo user");
    assert.equal(messagesElement.innerHTML, "preserved transcript");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("message correction editor exposes a neutral editing-state style hook", () => {
  const { html } = renderSnapshot([{
    id: "message-1",
    role: "user",
    contentText: "original text",
  }], {
    editingMessageId: "message-1",
    correctionText: "updated text",
    correctionFiles: [],
  });

  assert.match(html, /class="message user message-editing chat-message/);
  assert.match(html, /class="message-correction-editor"/);
  assert.match(html, />updated text<\/textarea>/);
});

test("task reports, tool output, approvals, and errors expose left-aligned bounded-layout hooks", () => {
  const { html } = renderSnapshot([
    { role: "error", contentText: "request failed" },
  ], {
    liveToolOutputs: {
      tool1: { agentId: "agent-1", toolUseId: "tool1", toolName: "Bash", status: "running", output: "working" },
    },
    pendingToolApprovals: {
      approval1: { agentId: "agent-1", toolUseId: "approval1", toolName: "Bash", command: "pwd", risk: "exec" },
    },
    navigationSelectionKind: "project",
    activeRunSummaryRunId: "run-1",
    activeRunSummary: {
      run: { id: "run-1", source: "manual", status: "error", checkpointState: "none", createdAt: "2026-02-03T00:00:00Z", completedAt: "2026-02-03T00:01:00Z" },
      toolCalls: [],
      recentMessages: [],
    },
    runSummaryError: "<run error>",
  });

  assert.match(html, /data-message-role="error"/);
  assert.match(html, /class="live-tool-output-stack tool-activity-stack chat-flow-stack chat-flow-left" data-chat-alignment="left"/);
  assert.match(html, /data-tool-activity-select="tool1"/);
  assert.doesNotMatch(html, /data-chat-report="tool-activity"/);
  assert.match(html, /tool-activity-summary/);
  assert.match(html, /data-chat-report="tool-approval"/);
  // Project runs keep a compact failure notice, but the removed legacy review
  // card and its raw runSummaryError must not return.
  assert.match(html, /data-chat-report="conversation-run"/);
  assert.match(html, /conversation-run-notice error/);
  assert.doesNotMatch(html, /data-chat-report="run-summary"|data-run-summary-card/);
  assert.doesNotMatch(html, /run error/);
});

test("conversation run failures surface localized provider API failures in an error notice", () => {
  // This used to run through the (now removed) project review card; the same
  // runFailureMessage() localization is exercised here through the surviving
  // conversation-run-notice path so the provider-error copy stays covered.
  const failures = [{
    id: "accounts-exhausted",
    errorMessage: `POST "https://pixelstarrysky.xyz/v1/responses": 403 Forbidden {"code":"server_error","message":"All available accounts exhausted"}`,
    expected: "模型 API 错误 403：上游可用账户已耗尽，请稍后重试或切换模型。",
  }, {
    id: "insufficient-balance",
    errorMessage: "OpenAI API error 403: 账户余额不足，请充值后重试",
    expected: "模型 API 错误 403：账户余额不足，请充值后重试。",
  }];

  for (const failure of failures) {
    const { html } = renderSnapshot([], {
      activeRunSummaryRunId: `run-${failure.id}`,
      activeRunSummary: {
        run: { id: `run-${failure.id}`, source: "conversation", status: "error", errorMessage: failure.errorMessage },
        toolCalls: [],
        recentMessages: [],
      },
    });

    assert.match(html, /data-chat-report="conversation-run"/);
    assert.match(html, /conversation-run-notice error/);
    assert.ok(html.includes(failure.expected));
    assert.doesNotMatch(html, /run-summary-card|run-summary-metrics|run-summary-checkpoint|data-run-summary-copy|data-run-summary-refresh|data-chat-report="project-run-failure"/);
  }
});

test("project run failures render an escaped compact notice without the old review card", () => {
  // Project runs now show a compact failure notice identical to conversation
  // runs. The hostile payload must arrive escaped as entity-encoded text; it
  // must never execute as markup, and the old stats/checkpoint review card
  // must remain absent from the DOM.
  const hostile = `<img src=x onerror=boom>${"failure ".repeat(100)}`;
  const { html } = renderSnapshot([], {
    navigationSelectionKind: "project",
    activeRunSummaryRunId: "run-hostile-error",
    activeRunSummary: {
      run: { id: "run-hostile-error", source: "manual", status: "failed", errorMessage: hostile },
      toolCalls: [],
      recentMessages: [],
    },
  });

  // The notice must be present and properly escaped (no raw script/img tags).
  assert.match(html, /data-chat-report="conversation-run"/);
  assert.match(html, /conversation-run-notice error/);
  assert.doesNotMatch(html, /<img\s[^>]*onerror/i);
  assert.match(html, /&lt;img src=x onerror=boom&gt;/);
  // Legacy review card structures must not appear.
  assert.doesNotMatch(html, /run-summary-failure-message|data-chat-report="project-run-failure"|data-chat-report="run-summary"/);
});

test("project run failures with tool activity still render no chat review card", () => {
  const tool = { agentId: "agent-1", runId: "run-tool-failure", toolUseId: "edit-1", toolName: "Edit", status: "completed" };
  const { html } = renderSnapshot([], {
    navigationSelectionKind: "project",
    activeRunSummaryRunId: "run-tool-failure",
    activeRunSummary: {
      run: { id: "run-tool-failure", source: "manual", status: "error", errorMessage: "provider failed after tool activity", checkpointState: "none" },
      toolCallCount: 1,
      toolCalls: [tool],
      recentMessages: [],
    },
    activeRunToolCallsRunId: "run-tool-failure",
    activeRunToolCalls: [tool],
  });

  assert.doesNotMatch(html, /data-chat-report="run-summary"|data-chat-report="project-run-failure"|run-summary-metrics|data-run-summary-rollback/);
});

test("ordinary completed conversations without tools render no Run review", () => {
  const { html } = renderSnapshot([{ role: "assistant", contentText: "done" }], {
    activeRunSummaryRunId: "run-ordinary-success",
    activeRunSummary: {
      run: { id: "run-ordinary-success", source: "conversation", status: "completed", createdAt: "2026-02-03T00:00:00Z", completedAt: "2026-02-03T00:01:00Z" },
      toolCalls: [],
      recentMessages: [],
    },
  });

  assert.doesNotMatch(html, /data-run-summary-card|data-chat-report="run-summary"|data-chat-report="conversation-run"/);
  assert.doesNotMatch(html, /run-summary-metrics|data-run-summary-copy|data-run-summary-refresh/);
  assert.match(html, />done</);
});

test("ordinary legacy manual runs keep a compact tool activity stack and load-earlier entry", () => {
  const tool = { agentId: "agent-1", runId: "run-manual", toolUseId: "read-manual", toolName: "Read", status: "completed", inputJson: { file_path: "legacy.txt" } };
  const { html } = renderSnapshot([], {
    activeRunSummaryRunId: "run-manual",
    activeRunSummary: {
      run: { id: "run-manual", source: "manual", status: "completed" },
      toolCalls: [tool],
      recentMessages: [],
    },
    activeRunToolCallsRunId: "run-manual",
    activeRunToolCalls: [tool],
    activeRunToolCallsHasMore: true,
    liveToolOutputs: { "read-manual": tool },
  });

  assert.match(html, /data-chat-report="conversation-run"/);
  assert.match(html, /data-conversation-run-tool-activity/);
  assert.match(html, /conversation-tool-activity/);
  assert.match(html, /legacy\.txt/);
  assert.match(html, /data-run-tool-activity-more="run-manual"/);
  assert.equal((html.match(/data-tool-activity-select="read-manual"/g) || []).length, 1, "review de-duplication must leave one visible tool row");
  assert.doesNotMatch(html, /run-summary-metrics|run-summary-checkpoint|data-run-summary-copy|data-run-summary-refresh|data-run-summary-open-git|data-run-summary-rollback/);
});

test("ordinary error and failed runs render escaped, visually bounded, history-recoverable notices", () => {
  for (const status of ["error", "failed"]) {
    const hostile = `<img src=x onerror=boom>${"failure ".repeat(100)}`;
    const { html } = renderSnapshot([], {
      activeRunSummaryRunId: `run-${status}`,
      activeRunSummary: {
        run: { id: `run-${status}`, source: "conversation", status, errorMessage: hostile },
        toolCalls: [],
        recentMessages: [],
      },
    });

    assert.match(html, /data-chat-report="conversation-run"/);
    assert.match(html, /conversation-run-notice error/);
    assert.match(html, /conversation-run-error-message/);
    assert.match(html, /错误已保留在对话历史中/);
    assert.match(html, /&lt;img src=x onerror=boom&gt;/);
    assert.doesNotMatch(html, /<img|data-run-id|run-summary-metrics|data-run-summary-copy|data-run-summary-refresh/);
  }
});

test("ordinary interrupted runs are weakly noted while superseded runs stay hidden", () => {
  const interrupted = renderSnapshot([], {
    activeRunSummaryRunId: "run-interrupted",
    activeRunSummary: { run: { id: "run-interrupted", source: "conversation", status: "interrupted" }, toolCalls: [], recentMessages: [] },
  });
  assert.match(interrupted.html, /conversation-run-notice interrupted/);
  assert.match(interrupted.html, /已有消息和工具记录仍保留/);

  const superseded = renderSnapshot([], {
    activeRunSummaryRunId: "run-superseded",
    activeRunSummary: { run: { id: "run-superseded", source: "conversation", status: "superseded" }, toolCalls: [], recentMessages: [] },
  });
  assert.doesNotMatch(superseded.html, /data-run-summary-card|conversation-run-notice|run-summary-card/);
});

test("project navigation keeps compact tool history without restoring the legacy review card", () => {
  // The stats grid, recent-message preview, and git controls stay in their
  // dedicated project surfaces, while persisted tool activity remains visible
  // after leaving and reopening the conversation.
  const project = renderSnapshot([], {
    navigationSelectionKind: "project",
    activeRunSummaryRunId: "run-project",
    activeRunSummary: {
      run: { id: "run-project", source: "project", status: "completed", checkpointState: "none", createdAt: "2026-02-03T00:00:00Z", completedAt: "2026-02-03T00:01:00Z" },
      toolCallCount: 1,
      toolCalls: [{ runId: "run-project", toolUseId: "project-read", toolName: "Read", status: "completed", inputJSON: JSON.stringify({ file_path: "README.md" }) }],
      recentMessages: [{ role: "assistant", contentText: "Visible summary message" }],
    },
  });
  assert.match(project.html, /data-chat-report="conversation-run"/);
  assert.match(project.html, /data-tool-activity-select="project-read"/);
  assert.doesNotMatch(project.html, /data-chat-report="run-summary"|run-summary-metrics|run-summary-checkpoint|data-run-summary-open-git|data-run-summary-rollback|data-run-summary-copy|data-run-summary-refresh|Visible summary message/);

  const conversationSource = renderSnapshot([{ role: "assistant", contentText: "done" }], {
    navigationSelectionKind: "project",
    activeRunSummaryRunId: "run-conversation-source",
    activeRunSummary: {
      run: { id: "run-conversation-source", source: "conversation", status: "completed" },
      toolCalls: [],
      recentMessages: [],
    },
  });
  assert.doesNotMatch(conversationSource.html, /data-run-summary-card|run-summary-metrics|run-summary-checkpoint|data-run-summary-copy|data-run-summary-refresh/);
  assert.match(conversationSource.html, />done</);
});

test("Run summary API failures render a lightweight notice instead of a project review card", async () => {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => id === "messages" ? messagesElement : null };
  try {
    const state = {
      agent: { id: "agent-1" },
      navigationSelectionKind: "project",
      currentMessages: [{ role: "assistant", contentText: "history remains" }],
      messageCopyTexts: [],
      liveToolOutputs: {},
      pendingToolApprovals: {},
      activeRunSummary: null,
      activeRunSummaryRunId: "",
      runSummaryLoading: false,
      runSummaryError: "",
    };
    const controller = createChatRenderingController({
      state,
      apiRequest: async (url) => {
        if (url.includes("/tool-calls?")) return { toolCalls: [], hasMore: false };
        throw new Error(`<summary error>`);
      },
      attachmentIcon: () => "file", attachmentKind: () => "file", copyToClipboard: async () => true,
      notifyTerminal: () => {}, selectedModelValue: () => "", shortPath: (value) => value, showError: () => {}, showToast: () => {},
    });

    await assert.rejects(controller.loadRunSummary("run-load-failed"), /<summary error>/);
    assert.match(messagesElement.innerHTML, /data-chat-report="run-load-error"/);
    assert.match(messagesElement.innerHTML, /conversation-run-notice load-error/);
    assert.match(messagesElement.innerHTML, /暂时无法加载本轮详情/);
    assert.doesNotMatch(messagesElement.innerHTML, /run-summary-card|run-summary-metrics|run-summary-checkpoint|data-run-summary-copy|data-run-summary-refresh|<summary error>|&lt;summary error&gt;/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("ordinary Run outcome copy is localized in Simplified Chinese, Traditional Chinese, and English", () => {
  assert.equal(chatRenderingExtraText("run.conversationErrorTitle", {}, "zh-CN"), "本轮回复失败");
  assert.equal(chatRenderingExtraText("run.conversationErrorTitle", {}, "zh-TW"), "本輪回覆失敗");
  assert.equal(chatRenderingExtraText("run.conversationErrorTitle", {}, "en"), "This response failed");
  assert.match(chatRenderingExtraText("run.summaryUnavailableHint", {}, "zh-CN"), /对话消息仍可查看/);
  assert.match(chatRenderingExtraText("run.summaryUnavailableHint", {}, "zh-TW"), /對話訊息仍可查看/);
  assert.match(chatRenderingExtraText("run.summaryUnavailableHint", {}, "en"), /Conversation messages are still available/);
  assert.equal(chatRenderingExtraText("run.providerErrorWithStatus", { status: 403, message: "余额不足" }, "zh-CN"), "模型 API 错误 403：余额不足");
  assert.equal(chatRenderingExtraText("run.providerErrorWithStatus", { status: 403, message: "餘額不足" }, "zh-TW"), "模型 API 錯誤 403：餘額不足");
  assert.equal(chatRenderingExtraText("run.providerErrorWithStatus", { status: 403, message: "insufficient balance" }, "en"), "Model API error 403: insufficient balance");
});

test("plan cards render pending review data safely and react to live plan events", () => {
  const rawPlan = {
    id: `plan\" onmouseover=\"boom`,
    goal: `<Ship auth safely>`,
    status: "pending_approval",
    steps: [{ title: "Map <routes>", description: "Check middleware", status: "draft" }],
    risks: ["Do not expose <tokens>"],
    reviewVerdict: "review",
    reviewFindings: ["Check <CSRF> handling"],
    staleReason: "<workspace changed>",
  };
  const normalized = normalizeAgentPlan(rawPlan, "agent-1");
  assert.equal(normalized.agentId, "agent-1");
  assert.equal(normalized.steps[0].detail, "Check middleware");

  const { html, state } = renderSnapshot([], {
    activePlan: rawPlan,
    pendingPlanApproval: rawPlan,
    planActionBusy: {},
  });
  assert.match(html, /data-chat-report="agent-plan"/);
  assert.match(html, /data-plan-action="approve"/);
  assert.match(html, /data-plan-action="cancel"/);
  assert.match(html, /data-plan-action="replan"/);
  assert.doesNotMatch(html, /data-plan-action="execute"/);
  assert.match(html, /&lt;Ship auth safely&gt;/);
  assert.match(html, /Map &lt;routes&gt;/);
  assert.match(html, /Check &lt;CSRF&gt; handling/);
  assert.match(html, /&lt;workspace changed&gt;/);
  assert.doesNotMatch(html, /onmouseover="boom"|<Ship auth safely>/);

  const previousDocument = globalThis.document;
  const messagesElement = fakeMessagesElement();
  globalThis.document = { getElementById(id) { return id === "messages" ? messagesElement : null; } };
  try {
    const liveState = { ...state, chatHydrating: true };
    const controller = createChatRenderingController({
      state: liveState,
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });
    assert.equal(controller.applyPlanEvent({ type: "plan.approved", agentId: "agent-1", data: { plan: { ...rawPlan, status: "approved" } } }), true);
    assert.equal(liveState.activePlan.status, "approved");
    assert.equal(liveState.pendingPlanApproval, null);
    assert.equal(controller.applyPlanEvent({ type: "plan.stale", agentId: "agent-1", text: "changed files", data: { plan: { ...rawPlan, staleReason: "", status: "stale" } } }), true);
    assert.equal(liveState.activePlan.status, "stale");
    assert.equal(liveState.activePlan.staleReason, "changed files");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("plan actions use the Agent plan action contract and update local state", async () => {
  const previousDocument = globalThis.document;
  const previousWindow = globalThis.window;
  const messagesElement = fakeMessagesElement();
  const requests = [];
  globalThis.document = { getElementById(id) { return id === "messages" ? messagesElement : null; } };
  globalThis.window = { setTimeout() { return 0; }, clearTimeout() {} };
  try {
    const state = {
      agent: { id: "agent-1" },
      currentMessages: [],
      activePlan: { id: "plan-1", revision: 3, goal: "Ship it", status: "approved" },
      pendingPlanApproval: null,
      planActionBusy: {},
      chatHydrating: true,
    };
    const controller = createChatRenderingController({
      state,
      apiRequest: async (path, options) => {
        requests.push({ path, options });
        return { plan: { id: "plan-1", goal: "Ship it", status: "executing" } };
      },
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });

    await controller.performPlanAction("plan-1", "execute");
    assert.deepEqual(requests, [{
      path: "/api/agents/agent-1/plans/plan-1/execute",
      options: { method: "POST", body: "{\"revision\":3}" },
    }]);
    assert.equal(state.activePlan.status, "executing");
    assert.deepEqual(state.planActionBusy, {});
  } finally {
    globalThis.document = previousDocument;
    globalThis.window = previousWindow;
  }
});

test("message rendering escapes role, text, and code attributes without breaking markdown or copy", () => {
  const { html } = renderSnapshot([
    {
      role: `user\" onmouseover=\"boom`,
      contentText: `<img src=x onerror=boom>\n\n\`\`\`js\nconst value = \"<unsafe>\";\n\`\`\``,
    },
  ]);

  assert.match(html, /data-chat-alignment="left"/);
  assert.doesNotMatch(html, /<img src=x|onmouseover="boom/);
  assert.match(html, /&lt;img src=x onerror=boom&gt;/);
  assert.match(html, /user&quot; onmouseover=&quot;boom/);
  assert.match(html, /class="code-block"/);
  assert.match(html, /class="copy-code"/);
  assert.match(html, /data-copy-message="0"/);
});

test("generated image blocks parse contentJson, sort by outputIndex, and build only same-origin asset URLs", () => {
  const message = {
    id: "message/1",
    agentId: "agent one",
    role: "assistant",
    contentJson: JSON.stringify({ content: [
      { type: "image_generation", assetId: "asset-2", generationId: "gen-2", status: "completed", mimeType: "image/png", filename: "second.png", width: 1024, height: 768, revisedPrompt: "Second <view>", outputIndex: 2, storageKey: "C:/secret/file.png", data: "data:image/png;base64,AAAA" },
      { type: "text", text: "ignored here" },
      { type: "image_generation", assetId: "asset/0", generationId: "gen-0", status: "completed", mimeType: "image/webp", filename: "first.webp", width: 512, height: 512, revisedPrompt: `First \"view\"`, outputIndex: 0 },
    ] }),
  };
  assert.equal(messageContentBlocks(message).length, 3);
  const blocks = normalizeGeneratedImageBlocks(message);
  assert.deepEqual(blocks.map((block) => block.outputIndex), [0, 2]);
  assert.equal(Object.hasOwn(blocks[0], "storageKey"), false);
  assert.equal(Object.hasOwn(blocks[0], "data"), false);
  assert.equal(generatedImageURL("agent one", "message/1", "asset/0"), "/api/agents/agent%20one/messages/message%2F1/generated-images/asset%2F0");
  assert.equal(generatedImageURL("agent one", "message/1", "asset/0", { download: true }), "/api/agents/agent%20one/messages/message%2F1/generated-images/asset%2F0?download=1");
  assert.equal(generatedImageURL("", "message/1", "asset/0"), "");

  const { html } = renderSnapshot([message]);
  assert.ok(html.indexOf("asset%2F0") < html.indexOf("asset-2"));
  assert.match(html, /class="generated-image-preview"/);
  assert.match(html, /loading="lazy"/);
  assert.match(html, /alt="First &quot;view&quot;"/);
  assert.match(html, /width="512" height="512" style="aspect-ratio: 512 \/ 512"/);
  assert.match(html, /generated-images\/asset%2F0\?download=1/);
  assert.match(html, /download="first.webp"/);
  assert.doesNotMatch(html, /storageKey|C:\/secret|file:\/\/|data:image|base64/i);
  assert.doesNotMatch(html, /<view>/);
  assert.doesNotMatch(renderSnapshot([{ ...message, role: "user" }], { agent: { id: "agent-1" } }).html, /data-generated-image/);
});

test("generated image blocks keep a non-breaking placeholder when the asset identity is missing", () => {
  const { html } = renderSnapshot([{ id: "message-1", role: "assistant", contentJson: [{ type: "image_generation", status: "failed", revisedPrompt: "unsafe <prompt>", outputIndex: 0 }] }]);
  assert.match(html, /generated-image-missing/);
  assert.match(html, /图片资产暂不可用/);
  assert.match(html, /unsafe &lt;prompt&gt;/);
  assert.doesNotMatch(html, /<img class="generated-image-preview"/);
});

test("image_generation.status keeps only lightweight status fields and clears on authoritative message refresh", () => {
  const event = {
    type: "image_generation.status",
    agentId: "agent-a",
    data: {
      requestId: "request-1",
      runId: "run-1",
      generationId: "generation-1",
      status: "partial",
      outputIndex: 1,
      partialIndex: 3,
      base64: "AAAA",
      dataUrl: "data:image/png;base64,AAAA",
    },
  };
  assert.deepEqual(normalizeImageGenerationStatusEvent(event), {
    requestId: "request-1",
    runId: "run-1",
    generationId: "generation-1",
    status: "partial",
    outputIndex: 1,
    partialIndex: 3,
  });
  const harness = createAsyncChatRenderingHarness(async () => ({ messages: [] }));
  try {
    assert.equal(harness.controller.rememberImageGenerationStatus(event), true);
    assert.equal(Object.keys(harness.state.liveImageGenerations).length, 1);
    assert.match(harness.messagesElement.innerHTML, /data-live-image-generation="generation-1"/);
    assert.match(harness.messagesElement.innerHTML, /正在生成图片/);
    assert.doesNotMatch(harness.messagesElement.innerHTML, /<img|AAAA|data:image|base64/i);
    harness.controller.applyMessageSnapshot([], "agent-a", { clearLiveImageGenerations: true });
    assert.deepEqual(harness.state.liveImageGenerations, {});
    assert.doesNotMatch(harness.messagesElement.innerHTML, /data-live-image-generation=/);
  } finally {
    harness.restore();
  }
});

test("model generation keeps the message thread clear before the first text delta", () => {
  const { html } = renderSnapshot([], {
    liveAssistantActive: true,
    liveAssistantRequestId: "request-1",
    liveAssistantRunId: "run-1",
    liveAssistantStartedAt: "2026-03-16T10:00:00Z",
  });

  assert.doesNotMatch(html, /data-live-assistant|等待首 token|live-assistant-status/);
  assert.match(html, /class="empty-conversation-state"/);
});

test("tool activity is labelled with line-art SVG icons classed by what the tool does", () => {
  const calls = [
    { toolUseId: "t1", runId: "run-1", toolName: "Grep", status: "completed" },
    { toolUseId: "t2", runId: "run-1", toolName: "Read", status: "completed" },
    { toolUseId: "t3", runId: "run-1", toolName: "Edit", status: "completed" },
    { toolUseId: "t4", runId: "run-1", toolName: "Write", status: "completed" },
    { toolUseId: "t5", runId: "run-1", toolName: "Bash", status: "completed" },
    { toolUseId: "t6", runId: "run-1", toolName: "Glob", status: "completed" },
    { toolUseId: "t7", runId: "run-1", toolName: "WebFetch", status: "completed" },
    { toolUseId: "t8", runId: "run-1", toolName: "TodoWrite", status: "completed" },
  ];
  const html = renderToolActivityStackHTML(calls, { expanded: true });

  for (const kind of ["search", "read", "edit", "write", "command", "files", "web", "todo"]) {
    assert.match(html, new RegExp(`tool-activity-step-icon tool-activity-icon-${kind}"[^>]*><svg viewBox="0 0 24 24"`), `${kind} row should carry its own glyph`);
  }
  assert.match(html, /stroke="currentColor" stroke-width="1.7"/);
  // The glyphs must reach the DOM as markup, not as escaped text, and the
  // Unicode characters they replaced must be gone.
  assert.doesNotMatch(html, /&lt;svg/);
  assert.doesNotMatch(html, /[⌕◌▤±✎•]/);

  const card = renderToolActivityCardHTML({ toolUseId: "t9", runId: "run-1", toolName: "Bash", status: "completed" });
  assert.match(card, /class="tool-activity-icon tool-activity-icon-command"[^>]*><svg/);
});

test("the assistant's own words close the thread, below the run's tool activity", () => {
  const { html } = renderSnapshot([{ role: "user", contentText: "continue" }], {
    navigationSelectionKind: "conversation",
    liveAssistantActive: true,
    liveAssistantText: "here is what I found",
    liveAssistantRequestId: "request-1",
    liveAssistantRunId: "run-1",
    liveToolOutputs: {
      tool1: { agentId: "agent-1", toolUseId: "tool1", toolName: "Grep", status: "running", output: "searching" },
    },
    pendingToolApprovals: {
      approval1: { agentId: "agent-1", toolUseId: "approval1", toolName: "Bash", command: "pwd", risk: "exec" },
    },
    activeRunSummaryRunId: "run-1",
    activeRunSummary: {
      run: { id: "run-1", source: "conversation", status: "running", checkpointState: "none", createdAt: "2026-03-16T10:00:00Z" },
      toolCalls: [{ agentId: "agent-1", runId: "run-1", toolUseId: "tool2", toolName: "Read", status: "completed" }],
      recentMessages: [],
    },
  });

  const liveTools = html.indexOf("data-live-tool-output-stack");
  const runOutcome = html.indexOf("data-run-outcome-card");
  const liveAssistant = html.indexOf("data-live-assistant");
  const approvals = html.indexOf("data-approval-stack");
  for (const [name, index] of [["live tool output", liveTools], ["run outcome", runOutcome], ["live assistant", liveAssistant], ["approvals", approvals]]) {
    assert.ok(index >= 0, `${name} should render in this snapshot`);
  }
  assert.ok(liveTools < runOutcome, "live tool output stays above the run outcome card");
  assert.ok(runOutcome < liveAssistant, "tool activity stays above the assistant's reply");
  assert.ok(liveAssistant < approvals, "only approval prompts render below the assistant's reply");
});

test("live estimated performance is compact and visibly approximate", () => {
  const { html } = renderSnapshot([], {
    liveAssistantActive: true,
    liveAssistantText: "streaming reply",
    liveAssistantRequestId: `request\" onmouseover=\"boom`,
    liveAssistantRunId: `<run-id>`,
    liveAssistantPerformance: {
      outputTokens: 36,
      ttftMs: 820,
      durationMs: 2300,
      tokensPerSecond: 24.6,
      estimated: true,
    },
  });

  assert.match(html, /≈吞吐量 24\.6 tok\/s \| TTFT 0\.8s/);
  assert.match(html, /message-performance-live is-estimated/);
  assert.ok(html.indexOf("message-performance-live") < html.indexOf("message-content"), "live metrics should render in the card header");
  assert.match(html, /request&quot; onmouseover=&quot;boom/);
  assert.match(html, /data-run-id="&lt;run-id&gt;"/);
  assert.doesNotMatch(html, /live-assistant-status|等待首 token|正在生成/);
  assert.doesNotMatch(html, /onmouseover="boom"|<run-id>/);
});

test("historical assistant messages render precise persisted turn usage without approximation", () => {
  const { html } = renderSnapshot([{
    role: "assistant",
    contentText: "final reply",
    turnUsage: {
      inputTokens: 120,
      outputTokens: 36,
      cachedInputTokens: 20,
      reasoningTokens: 8,
      ttftMs: 820,
      durationMs: 2300,
      tokensPerSecond: 24.6,
      estimated: false,
    },
  }]);

  assert.match(html, /class="message-performance"/);
  assert.match(html, /吞吐量 24\.6 tok\/s \| TTFT 0\.8s/);
  assert.ok(html.indexOf("final reply") < html.indexOf("message-performance"), "persisted metrics should render in the message footer");
  assert.doesNotMatch(html, /≈|is-estimated/);
});

test("turn usage performance labels are localized in all supported UI languages", () => {
  const usage = { tokensPerSecond: 12.34, ttftMs: 780, estimated: true };
  assert.equal(formatTurnUsagePerformance(usage, { locale: "en" }), "≈Throughput 12.3 tok/s | TTFT 0.8s");
  assert.equal(formatTurnUsagePerformance(usage, { locale: "zh-CN" }), "≈吞吐量 12.3 tok/s | TTFT 0.8s");
  assert.equal(formatTurnUsagePerformance(usage, { locale: "zh-TW" }), "≈吞吐量 12.3 tok/s | TTFT 0.8s");
});

test("turn usage normalization drops zero, negative, non-finite, extreme, and injectable values", () => {
  assert.deepEqual(normalizeTurnUsage({
    inputTokens: -1,
    outputTokens: 0,
    cachedInputTokens: Number.NaN,
    reasoningTokens: 1e12,
    ttftMs: Number.POSITIVE_INFINITY,
    durationMs: 999999999999,
    tokensPerSecond: `<img src=x onerror=boom>`,
    estimated: true,
  }), {
    inputTokens: null,
    outputTokens: null,
    cachedInputTokens: null,
    reasoningTokens: null,
    ttftMs: null,
    durationMs: null,
    tokensPerSecond: null,
    estimated: true,
  });
  assert.equal(normalizeTurnUsage({ ttftMs: 0 }).ttftMs, 0);
  assert.equal(formatTurnUsagePerformance({ ttftMs: 0 }, { locale: "en" }), "TTFT 0.0s");
  assert.equal(formatTurnUsagePerformance({ outputTokens: 0, durationMs: -4, tokensPerSecond: Number.NaN }, { locale: "en" }), "");
  const { html } = renderSnapshot([{ role: "assistant", contentText: "safe", turnUsage: { tokensPerSecond: `<svg onload=boom>` } }]);
  // The assistant avatar is itself an inline <svg>, so the injection check has to
  // target the payload and the metrics footer rather than the tag name.
  assert.doesNotMatch(html, /onload=boom|message-performance/);
  assert.doesNotMatch(html, /<svg(?![^>]*viewBox="0 0 64 64")/);
});

test("a run's tool calls file under the assistant turn that emitted them, not in one stack on top", () => {
  const { html } = renderSnapshot([
    { id: "u1", role: "user", contentText: "go" },
    { id: "a1", role: "assistant", contentText: "First I look around.", reasoningText: "Need the layout." },
    { id: "a2", role: "assistant", contentText: "Now I edit it." },
  ], {
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [
      { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t1", toolName: "Grep", status: "completed", createdAt: "2026-03-16T10:00:01Z" },
      { agentId: "agent-1", runId: "run-1", messageId: "a2", toolUseId: "t2", toolName: "Edit", status: "completed", createdAt: "2026-03-16T10:00:02Z" },
    ],
    activeRunSummary: {
      run: { id: "run-1", source: "conversation", status: "completed", triggerMessageId: "u1" },
      toolCallCount: 2,
    },
  });

  // Each turn owns its own stack, and the activity leads its answer on every
  // responsive layout so desktop and mobile have the same reading order.
  assert.match(html, /data-tool-activity-stack-key="msg:a1"/);
  assert.match(html, /data-tool-activity-stack-key="msg:a2"/);
  const a1 = html.indexOf('data-message-id="a1"');
  const a1Stack = html.indexOf('data-message-activity="a1"');
  const a2 = html.indexOf('data-message-id="a2"');
  const a2Stack = html.indexOf('data-message-activity="a2"');
  assert.ok(a1Stack < a1 && a1 < a2Stack, "the first turn's activity leads its answer and stays before the second turn");
  assert.ok(a2Stack < a2, "the second turn's activity leads its answer");

  // a1 reasoned and called one tool; a2 only called one.
  const a1Title = html.slice(a1Stack, a1).match(/tool-activity-summary">([^<]+)</)?.[1] || "";
  const a2Title = html.slice(a2Stack, a2).match(/tool-activity-summary">([^<]+)</)?.[1] || "";
  assert.equal(a1Title, "活动 · 1 步推理 · 1 次工具");
  assert.equal(a2Title, "活动 · 1 次工具");

  // Every call found a home, so the run-level card keeps no duplicate of them.
  const outcome = html.indexOf("data-run-outcome-card");
  if (outcome >= 0) {
    const tail = html.slice(outcome);
    assert.doesNotMatch(tail, /data-tool-activity-select="t1"|data-tool-activity-select="t2"/);
  }
});

// The snapshot stub reports no existing nodes, so it only ever exercises the
// full-paint path. This stub answers the two selectors the incremental path
// uses, which is the only way to reach the in-place repaint that runs on every
// tool event once a transcript is already on screen.
function incrementalMessagesElement(messageIds) {
  const activity = new Map();
  const tail = [];
  const element = {
    classList: { add() {}, remove() {}, contains: () => false },
    scrollHeight: 100,
    scrollTop: 0,
    get innerHTML() {
      return messageIds.map((id) => `<div data-message-id="${id}"></div>${activity.get(id) || ""}`).join("");
    },
    set innerHTML(_value) { throw new Error("the incremental path must not rebuild the transcript wholesale"); },
    // The tail stack legitimately appends to the container: it holds calls whose
    // assistant turn is not persisted yet, so it has no message to anchor on.
    insertAdjacentHTML(_position, html) { tail.push(html); },
    querySelector(selector) {
      const match = /^\[data-message-id="(.*)"\]$/.exec(selector);
      if (match && messageIds.includes(match[1])) {
        const id = match[1];
        return { insertAdjacentHTML: (_position, html) => activity.set(id, html) };
      }
      return null;
    },
    querySelectorAll(selector) {
      if (selector !== "[data-message-activity]") return [];
      return [...activity.keys()].map((id) => ({
        dataset: { messageActivity: id },
        set outerHTML(html) { activity.set(id, html); },
        remove: () => activity.delete(id),
        // The repaint reads the current <details> state to carry a manual
        // collapse across the swap; this stub holds markup, not elements.
        querySelector: () => null,
      }));
    },
  };
  return { element, activity, tail };
}

test("the incremental path repaints per-message stacks in place instead of rebuilding the transcript", () => {
  const previousDocument = globalThis.document;
  const { element, activity, tail } = incrementalMessagesElement(["u1", "a1"]);
  globalThis.document = { getElementById: (id) => id === "messages" ? element : null };
  const state = {
    agent: { id: "agent-1", status: "idle" },
    currentMessages: [
      { id: "u1", role: "user", contentText: "go" },
      { id: "a1", role: "assistant", contentText: "done", reasoningText: "thinking" },
    ],
    liveToolOutputs: {},
    toolActivitySelections: {},
    runSummaryLoading: false,
    runSummaryError: "",
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [
      { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "t1", toolName: "Grep", status: "completed" },
    ],
    activeRunSummary: {
      run: { id: "run-1", source: "conversation", status: "completed", triggerMessageId: "u1" },
      toolCallCount: 1,
    },
  };
  try {
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

    // A tool event is what drives the incremental repaint in production. This
    // one belongs to a newer run than the terminal summary above, so it is not
    // already owned by that summary.
    const toolEvent = { agentId: "agent-1", data: { toolUseId: "t2", toolName: "Read", runId: "run-2" } };

    // First pass: the stack is created and anchored on its own message.
    controller.rememberToolStarted(toolEvent);
    assert.deepEqual([...activity.keys()], ["a1"]);
    assert.match(activity.get("a1"), /data-message-activity="a1"/);
    assert.match(activity.get("a1"), /data-tool-activity-select="t1"/);
    assert.match(activity.get("a1"), /活动 · 1 步推理 · 1 次工具/);
    // t2 has no persisted owner yet, so it belongs to the tail, not to a1.
    assert.doesNotMatch(activity.get("a1"), /data-tool-activity-select="t2"/);
    assert.match(tail.join(""), /data-tool-activity-select="t2"/);

    // Second pass: repaint in place, not duplicate.
    controller.rememberToolStarted(toolEvent);
    assert.deepEqual([...activity.keys()], ["a1"]);
    assert.equal((activity.get("a1").match(/data-message-activity="a1"/g) || []).length, 1);

    // When the run's activity goes away, the stale stack is removed rather than
    // left behind pointing at calls that no longer exist.
    state.activeRunToolCalls = [];
    state.activeRunSummary = null;
    state.activeRunSummaryRunId = "";
    state.currentMessages = [
      { id: "u1", role: "user", contentText: "go" },
      { id: "a1", role: "assistant", contentText: "done" },
    ];
    controller.rememberToolStarted(toolEvent);
    assert.deepEqual([...activity.keys()], []);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("reasoning and ownerless same-run tools share one assistant activity stack", () => {
  const { html } = renderSnapshot([{
    id: "a1",
    role: "assistant",
    runId: "run-1",
    contentText: "The answer is ready.",
    reasoningText: "I checked the current conditions first.",
  }], {
    agent: { id: "agent-1", status: "running" },
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [
      { agentId: "agent-1", runId: "run-1", toolUseId: "t1", toolName: "WebSearch", status: "completed" },
      { agentId: "agent-1", runId: "run-1", toolUseId: "t2", toolName: "Read", status: "completed" },
    ],
  });

  assert.equal((html.match(/data-message-activity="a1"/g) || []).length, 1);
  assert.equal((html.match(/tool-activity-summary/g) || []).length, 1);
  assert.match(html, /活动 · 1 步推理 · 2 次工具/);
  assert.match(html, /data-tool-activity-select="t1"/);
  assert.match(html, /data-tool-activity-select="t2"/);
  assert.equal((html.match(/data-live-tool-output-stack/g) || []).length, 0);
});

test("live tool calls group under their assistant turn once that turn is on screen", () => {
  const persisted = { id: "a1", role: "assistant", contentText: "Looking now." };
  const { html } = renderSnapshot([{ id: "u1", role: "user", contentText: "go" }, persisted], {
    agent: { id: "agent-1", status: "running" },
    liveToolOutputs: {
      owned: { agentId: "agent-1", runId: "run-1", messageId: "a1", toolUseId: "owned", toolName: "Grep", status: "running" },
      pending: { agentId: "agent-1", runId: "run-1", toolUseId: "pending", toolName: "Bash", status: "running" },
    },
  });

  // The call whose turn is already persisted moves under it; the one whose turn
  // is still streaming has no anchor yet and stays in the tail stack. The tail
  // stack anchors after the last user message, so it precedes a1 in document
  // order -- containment is what matters here, not position.
  const ownedStack = html.indexOf('data-message-activity="a1"');
  const tailStack = html.indexOf("data-live-tool-output-stack");
  assert.ok(ownedStack >= 0 && tailStack >= 0, "both surfaces render in this snapshot");
  const owned = html.slice(ownedStack);
  const tail = html.slice(tailStack, ownedStack);
  assert.match(owned, /data-tool-activity-select="owned"/);
  assert.doesNotMatch(owned, /data-tool-activity-select="pending"/);
  assert.match(tail, /data-tool-activity-select="pending"/);
  assert.doesNotMatch(tail, /data-tool-activity-select="owned"/);
  // Exactly one home each: no call is rendered twice across the two surfaces.
  assert.equal((html.match(/data-tool-activity-select="owned"/g) || []).length, 1);
  assert.equal((html.match(/data-tool-activity-select="pending"/g) || []).length, 1);
});

test("a tool call whose owning message is not on screen falls back to the run-level stack", () => {
  const { html } = renderSnapshot([
    { id: "u1", role: "user", contentText: "go" },
    { id: "a1", role: "assistant", contentText: "done" },
  ], {
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [
      { agentId: "agent-1", runId: "run-1", messageId: "gone", toolUseId: "orphan", toolName: "Bash", status: "completed" },
      { agentId: "agent-1", runId: "run-1", toolUseId: "legacy", toolName: "Read", status: "completed" },
    ],
    activeRunSummary: {
      run: { id: "run-1", source: "conversation", status: "completed", triggerMessageId: "u1" },
      toolCallCount: 2,
    },
  });

  // Neither an unknown owner nor a pre-messageId row may be dropped.
  assert.doesNotMatch(html, /data-message-activity/);
  assert.match(html, /data-run-outcome-card/);
  assert.match(html, /data-tool-activity-select="orphan"/);
  assert.match(html, /data-tool-activity-select="legacy"/);
});

test("an earlier run's tool calls anchor on the turn that triggered them, not on the newest run", () => {
  const earlierCall = {
    agentId: "agent-1",
    runId: "run-old",
    // The assistant turn that emitted it carried no words, so it never became a
    // visible message: this is the case that used to lose its history entirely.
    messageId: "silent",
    toolUseId: "t-old",
    toolName: "WebSearch",
    status: "completed",
    createdAt: "2026-03-16T09:00:01Z",
  };
  const { html } = renderSnapshot([
    { id: "u1", role: "user", contentText: "open youtube", runId: "run-old" },
    { id: "a1", role: "assistant", contentText: "Opened it.", runId: "run-old" },
    { id: "u2", role: "user", contentText: "again", runId: "run-1" },
    { id: "a2", role: "assistant", contentText: "Done.", runId: "run-1" },
  ], {
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [],
    historyRunToolCalls: { "run-old": [earlierCall] },
    activeRunSummary: { run: { id: "run-1", source: "conversation", status: "completed", triggerMessageId: "u2" }, toolCallCount: 0 },
  }, {}, {
    apiRequest: async () => ({ toolCalls: [] }),
  });

  assert.match(html, /data-tool-activity-select="t-old"/);
  assert.match(html, /data-tool-activity-stack-key="run:run-old"/);
  const trigger = html.indexOf('data-message-id="u1"');
  const stack = html.indexOf('data-message-activity="u1"');
  const reply = html.indexOf('data-message-id="a1"');
  assert.ok(trigger < stack && stack < reply, "the earlier run's tools sit between its user turn and its reply");
  // The newest run owns the outcome card; an earlier run must not be repeated there.
  const outcome = html.indexOf("data-run-outcome-card");
  if (outcome >= 0) assert.doesNotMatch(html.slice(outcome), /data-tool-activity-select="t-old"/);
});

test("earlier-run tool history is fetched once per run and never for the active run", async () => {
  const requests = [];
  const { state } = renderSnapshot([
    { id: "u1", role: "user", contentText: "go", runId: "run-old" },
    { id: "a1", role: "assistant", contentText: "done", runId: "run-old" },
    { id: "u2", role: "user", contentText: "again", runId: "run-1" },
  ], {
    activeRunSummaryRunId: "run-1",
    activeRunToolCallsRunId: "run-1",
    activeRunToolCalls: [],
  }, {}, {
    apiRequest: async (url) => {
      requests.push(url);
      return { toolCalls: [{ agentId: "agent-1", runId: "run-old", messageId: "silent", toolUseId: "t-old", toolName: "Bash", status: "error" }] };
    },
  });

  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(requests, ["/api/agents/agent-1/runs/run-old/tool-calls?view=activity&limit=40"]);
  assert.equal(state.historyRunToolCalls["run-old"].length, 1);
  assert.equal(state.historyRunToolCalls["run-1"], undefined);
});

// A container the follow-the-tail logic can actually read: fakeMessagesElement
// reports no clientHeight, so isNearBottom can never be true against it.
function tailFollowingMessagesElement(messageIds) {
  const inserted = new Map();
  return {
    classList: { add() {}, remove() {}, contains: () => false },
    clientHeight: 500,
    get scrollHeight() { return 500 + inserted.size * 40; },
    scrollTop: 0,
    innerHTML: "",
    removeAttribute() {},
    dataset: {},
    insertAdjacentHTML(_position, html) { inserted.set(`tail:${inserted.size}`, html); },
    querySelector(selector) {
      const match = /^\[data-message-id="(.*)"\]$/.exec(selector);
      if (!match || !messageIds.includes(match[1])) return null;
      return { insertAdjacentHTML: (_position, html) => inserted.set(match[1], html) };
    },
    querySelectorAll: () => [],
    inserted,
  };
}

test("recovering an earlier run's activity keeps the reader pinned to the newest message", async () => {
  const element = tailFollowingMessagesElement(["u1", "a1", "u2"]);
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? element : null) };
  try {
    const state = {
      agent: { id: "agent-1", status: "idle" },
      currentMessages: [
        { id: "u1", role: "user", contentText: "go", runId: "run-old" },
        { id: "a1", role: "assistant", contentText: "done", runId: "run-old" },
        { id: "u2", role: "user", contentText: "again", runId: "run-1" },
      ],
      messageCopyTexts: [],
      liveToolOutputs: {},
      toolActivitySelections: {},
      activeRunSummaryRunId: "run-1",
      activeRunToolCallsRunId: "run-1",
      activeRunToolCalls: [],
      runSummaryLoading: false,
      runSummaryError: "",
    };
    const controller = createChatRenderingController({
      state,
      apiRequest: async () => ({ toolCalls: [{ agentId: "agent-1", runId: "run-old", messageId: "silent", toolUseId: "t-old", toolName: "Bash", status: "completed" }] }),
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });

    // The reader is sitting at the tail when the earlier run's strip arrives.
    element.scrollTop = element.scrollHeight - element.clientHeight;
    assert.equal(await controller.ensureHistoryRunActivity("agent-1"), true);
    assert.match(element.inserted.get("u1") || "", /data-tool-activity-select="t-old"/);
    assert.equal(element.scrollTop, element.scrollHeight, "the tail must stay in view after the strip is inserted");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("a run leaving the active slot hands its activity to history instead of refetching it", async () => {
  const requests = [];
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => messagesElement };
  try {
    const state = {
      agent: { id: "agent-1" },
      currentMessages: [],
      messageCopyTexts: [],
      liveToolOutputs: {},
      pendingToolApprovals: {},
      activeRunSummaryRunId: "run-old",
      activeRunToolCallsRunId: "run-old",
      activeRunToolCalls: [{ agentId: "agent-1", runId: "run-old", toolUseId: "t-old", toolName: "Bash", status: "completed" }],
    };
    const controller = createChatRenderingController({
      state,
      apiRequest: async (url) => {
        requests.push(url);
        return url.includes("/tool-calls?") ? { toolCalls: [] } : { run: { id: "run-1", status: "completed" } };
      },
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });

    await controller.loadRunSummary("run-1");
    assert.equal(state.historyRunToolCalls["run-old"].length, 1, "the outgoing run keeps the calls it already had");
    // Only the new run is fetched; the outgoing one is already in hand.
    assert.equal(requests.filter((url) => url.includes("run-old")).length, 0);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("tool activity renders a lightweight directory before hydrating one auditable detail", () => {
  const calls = [
    { toolUseId: "grep", toolName: "Grep", status: "completed", inputJson: { path: "src", pattern: "TODO" } },
    { toolUseId: "read", toolName: "Read", status: "completed", inputJson: { file_path: "src/main.mjs", pages: "1-2" } },
    { toolUseId: "edit", toolName: "Edit", status: "completed", inputJson: { file_path: "src/main.mjs", old_string: "old", new_string: "new" } },
    { toolUseId: "write", toolName: "Write", status: "completed", inputJson: { file_path: "notes.txt" } },
    { toolUseId: "glob", toolName: "Glob", status: "completed", inputJson: { path: "src", pattern: "**/*.mjs" } },
    { toolUseId: "bash", toolName: "Bash", status: "running", inputJson: { command: "node --test" }, output: "DETAIL_ONLY_OUTPUT", executionDeviceId: "local" },
  ];
  const html = renderToolActivityStackHTML(calls);

  for (const tool of ["Grep", "Read", "Edit", "Write", "Glob", "Bash"]) assert.match(html, new RegExp(`>${tool}<`));
  for (const className of ["tool-activity-stack", "tool-activity-group", "tool-activity-summary", "tool-activity-steps", "tool-activity-step", "tool-activity-step-button", "status-running", "status-completed"]) {
    assert.match(html, new RegExp(className));
  }
  assert.doesNotMatch(html, /tool-activity-card|tool-activity-details|DETAIL_ONLY_OUTPUT|本(?:机|地)服务/);
  assert.doesNotMatch(html, /tool-activity-protected|可审计摘要|可稽核摘要|Auditable summary/);
  assert.doesNotMatch(html, /思维链已加密|chain of thought encrypted/i);

  const selected = renderToolActivityStackHTML(calls, { selectedToolUseId: "bash" });
  assert.match(selected, /tool-activity-card/);
  assert.match(selected, /<details class="tool-activity-details" open>/);
  assert.match(selected, /DETAIL_ONLY_OUTPUT/);
  assert.match(selected, /本(?:机|地)服务/);
  assert.equal((selected.match(/class="tool-activity-card/g) || []).length, 1);
  // Detail now lives inside the selected row's inline slot, not in the shared bottom slot.
  assert.match(selected, /data-tool-activity-inline-detail="bash"[^>]*>[^<]/);
  assert.doesNotMatch(selected, /data-tool-activity-selected-detail[^>]*>[^<\s]/);
});

test("completed tool activity collapses while active or attention-needed work stays expanded", () => {
  const completedTools = [
    { toolUseId: "grep-done", toolName: "Grep", status: "completed", inputJson: { pattern: "TODO" } },
    { toolUseId: "read-done", toolName: "Read", status: "completed", inputJson: { file_path: "src/main.mjs" } },
  ];
  const completed = renderToolActivityStackHTML(completedTools);
  assert.match(completed, /data-tool-activity-default="collapsed"/);
  assert.match(completed, /<details class="tool-activity-group">/);
  assert.doesNotMatch(completed, /<details class="tool-activity-group" open>/);

  // All non-forced cases are collapsed by default now — the user opens the
  // summary when they want the trail, regardless of run status.
  const running = renderToolActivityStackHTML([
    ...completedTools,
    { toolUseId: "bash-running", toolName: "Bash", status: "running", inputJson: { command: "node --test" } },
  ]);
  assert.match(running, /data-tool-activity-default="collapsed"/);
  assert.doesNotMatch(running, /<details class="tool-activity-group" open>/);

  const liveActive = renderToolActivityStackHTML(completedTools, { live: true, runActive: true });
  assert.doesNotMatch(liveActive, /<details class="tool-activity-group" open>/);
  const liveFinished = renderToolActivityStackHTML(completedTools, { live: true, runActive: false });
  assert.doesNotMatch(liveFinished, /<details class="tool-activity-group" open>/);

  const agentTool = { toolUseId: "agent-task", toolName: "Agent", status: "completed", inputJson: { description: "Inspect auth" } };
  const activeSubagent = renderToolActivityStackHTML([agentTool], {
    resolveBackgroundTask: () => ({ id: "task-running", status: "running" }),
  });
  assert.doesNotMatch(activeSubagent, /<details class="tool-activity-group" open>/);
  const completedSubagent = renderToolActivityStackHTML([agentTool], {
    resolveBackgroundTask: () => ({ id: "task-done", status: "succeeded" }),
  });
  assert.doesNotMatch(completedSubagent, /<details class="tool-activity-group" open>/);
});

test("tool activity selection opens one detail and toggles the same row closed", () => {
  assert.equal(nextToolActivitySelection("", "read-1"), "read-1");
  assert.equal(nextToolActivitySelection("read-1", "bash-1"), "bash-1");
  assert.equal(nextToolActivitySelection("read-1", "read-1"), "");
  assert.equal(nextToolActivitySelection("read-1", ""), "");

  const calls = [
    { toolUseId: "read-1", toolName: "Read", status: "completed", inputJson: { file_path: "a.txt" }, output: "READ_DETAIL" },
    { toolUseId: "bash-1", toolName: "Bash", status: "completed", inputJson: { command: "pwd" }, output: "BASH_DETAIL" },
  ];
  const html = renderToolActivityStackHTML(calls, { selectedToolUseId: "read-1" });
  assert.match(html, /aria-expanded="true"/);
  assert.match(html, /READ_DETAIL/);
  assert.doesNotMatch(html, /BASH_DETAIL/);
  assert.equal((html.match(/class="tool-activity-card/g) || []).length, 1);
});

test("tool activity escapes dangerous data and bounds command and output rendering", () => {
  const hostile = `<img src=x onerror="boom">`;
  const html = renderToolActivityStackHTML([{
    toolUseId: `id\" onmouseover=\"boom`,
    toolName: hostile,
    status: "error",
    inputJson: { command: hostile.repeat(400) },
    output: hostile.repeat(400),
    errorMessage: hostile,
  }]);

  assert.match(html, /&lt;img src=x onerror=&quot;boom&quot;&gt;/);
  assert.doesNotMatch(html, /<img|onmouseover="boom"/);
  assert.match(html, /status-error/);
  assert.ok(html.length < 40_000, "tool data should be bounded");
});

test("tool activity keeps legacy data compatible when safety facts are absent", () => {
  const normalized = normalizeToolActivity({
    toolUseId: "legacy-bash",
    toolName: "Bash",
    status: "completed",
    inputJson: { command: "node --test" },
  });

  assert.equal(normalized.eventVersion, null);
  assert.equal(normalized.decision, "");
  assert.equal(normalized.decisionSource, "");
  assert.equal(normalized.decisionScope, "");
  assert.equal(normalized.ruleId, "");
  assert.equal(normalized.commandFacts, null);
  assert.equal(normalized.permissionDecisionReason, "");
});

test("tool activity warns when dynamic commands cannot be classified reliably", () => {
  const call = {
    toolUseId: "dynamic-bash",
    toolName: "Bash",
    status: "waiting_approval",
    shellSafe: false,
    commandFacts: {
      parseKnown: true,
      program: "dynamic",
      commandCount: 1,
    },
  };
  const normalized = normalizeToolActivity(call);
  const html = renderToolActivityStackHTML([call], { selectedToolUseId: "dynamic-bash" });

  assert.equal(normalized.shellSafe, false);
  assert.equal(normalized.commandFacts.program, "dynamic");
  assert.match(html, /无法可靠分类该动态命令；批准前请核对完整命令与展开后的行为/);
});

test("tool activity renders bounded localized safety facts without command parameters", () => {
  const secret = "super-secret-token";
  const call = {
    toolUseId: "facts-1",
    toolName: "Bash",
    status: "denied",
    eventVersion: 2,
    decision: "deny",
    decisionSource: "rule",
    ruleId: "rule-42",
    decisionScope: "session",
    permissionDecisionReason: "Denied <by policy>",
    commandFacts: {
      parseKnown: true,
      program: "git",
      subcommand: "status",
      commandCount: 2,
      compound: true,
      pipeline: true,
      redirection: true,
      substitution: true,
      background: true,
      effects: ["network-access"],
      dangerous: ["git-reset-hard"],
      sensitive: ["git-force-push"],
      injected: `<img src=x data-secret=${secret}>`,
    },
  };
  const normalized = normalizeToolActivity(call);
  const html = renderToolActivityStackHTML([call], { selectedToolUseId: "facts-1" });

  assert.deepEqual(normalized.commandFacts, {
    parseKnown: true,
    program: "git",
    subcommand: "status",
    commandCount: 2,
    compound: true,
    pipeline: true,
    redirection: true,
    substitution: true,
    background: true,
    effects: ["network-access"],
    dangerous: ["git-reset-hard"],
    sensitive: ["git-force-push"],
  });
  assert.equal(normalized.eventVersion, 2);
  assert.equal(normalized.decisionSource, "rule");
  assert.equal(normalized.decisionScope, "session");
  assert.match(html, /来源：权限规则/);
  assert.match(html, /作用域：本次会话/);
  assert.match(html, /安全判定/);
  for (const label of ["复合", "管道", "重定向", "命令替换", "后台", "程序：git", "白名单子命令：status", "影响：network-access", "危险：git-reset-hard"]) assert.match(html, new RegExp(label));
  assert.match(html, /Denied &lt;by policy&gt;/);
  assert.doesNotMatch(html, new RegExp(secret));
  assert.doesNotMatch(html, /<img/);
});

test("tool activity rejects malformed command facts and bounds fact arrays", () => {
  const secret = "secret-argument-should-not-render";
  const normalized = normalizeToolActivity({
    toolUseId: "facts-invalid",
    toolName: "Bash",
    eventVersion: "2",
    decision: "allow_everything",
    decisionSource: "<unsafe-source>",
    ruleId: `<${secret}>`,
    decisionScope: "all",
    permissionDecidedBy: "policy",
    permissionDecisionReason: `<img src=x>${"x".repeat(1_000)}`,
    commandFacts: {
      parseKnown: "yes",
      program: `git ${secret}`,
      subcommand: secret,
      commandCount: Number.POSITIVE_INFINITY,
      compound: "true",
      pipeline: true,
      effects: ["network-access", ...Array.from({ length: 20 }, () => secret)],
      dangerous: ["git-reset-hard", ...Array.from({ length: 20 }, () => secret)],
    },
  });
  const html = renderToolActivityStackHTML([normalized], { selectedToolUseId: "facts-invalid" });

  assert.equal(normalized.eventVersion, null);
  assert.equal(normalized.decision, "");
  assert.equal(normalized.decisionSource, "policy", "historical permissionDecidedBy remains a localized source fallback");
  assert.equal(normalized.ruleId, "");
  assert.equal(normalized.decisionScope, "");
  assert.equal(normalized.commandFacts.parseKnown, null);
  assert.equal(normalized.commandFacts.program, "");
  assert.equal(normalized.commandFacts.commandCount, null);
  assert.equal(normalized.commandFacts.compound, null);
  assert.deepEqual(normalized.commandFacts.effects, ["network-access"]);
  assert.deepEqual(normalized.commandFacts.dangerous, ["git-reset-hard"]);
  assert.ok(normalized.permissionDecisionReason.length <= 600);
  assert.match(html, /来源：策略/);
  assert.match(html, /&lt;img src=x&gt;/);
  assert.doesNotMatch(html, new RegExp(secret));
  assert.ok(html.length < 20_000, "malformed safety facts remain bounded");
});

test("approval cards reuse normalized safety facts without a second approval state", () => {
  const { html } = renderSnapshot([], {
    pendingToolApprovals: {
      approvalFacts: {
        agentId: "agent-1",
        toolUseId: "approval-facts",
        toolName: "Bash",
        inputJson: { command: "git status" },
        risk: "exec",
        decisionSource: "default_policy",
        decisionScope: "tool_call",
        commandFacts: { parseKnown: true, program: "git", subcommand: "status", commandCount: 1, compound: false, effects: [], dangerous: [] },
      },
    },
  });

  assert.match(html, /data-chat-report="tool-approval"/);
  assert.match(html, /来源：默认策略/);
  assert.match(html, /作用域：本次工具调用/);
  assert.match(html, /单条/);
  assert.match(html, /程序：git/);
});

test("approval cards prominently warn for unclassified dynamic commands", () => {
  const { html } = renderSnapshot([], {
    pendingToolApprovals: {
      dynamicCommand: {
        agentId: "agent-1",
        toolUseId: "dynamic-command",
        toolName: "Bash",
        inputJson: { command: "x=rm; $x -rf tmp" },
        risk: "exec",
        commandFacts: { parseKnown: false, program: "dynamic", commandCount: 1, compound: true, effects: [], dangerous: [] },
      },
    },
  });

  assert.match(html, /语法未知/);
  assert.match(html, /程序：dynamic/);
  assert.match(html, /无法可靠分类该命令/);
});

test("tool activity localizes every backend decision source", () => {
  const expected = new Map([
    ["policy_unavailable", "权限策略不可用"],
    ["workflow_unavailable", "工作流偏好不可用"],
    ["human_approval", "人工审批"],
    ["generation_invalidation", "授权版本失效"],
  ]);
  for (const [decisionSource, label] of expected) {
    const normalized = normalizeToolActivity({ toolUseId: decisionSource, toolName: "Bash", decision: "deny", decisionSource });
    assert.equal(normalized.decisionSource, decisionSource);
    assert.match(renderToolActivityStackHTML([normalized], { selectedToolUseId: decisionSource }), new RegExp(label));
  }
  const liveReason = normalizeToolActivity({ toolUseId: "live-reason", toolName: "Read", decision: "allow", decisionSource: "default_policy", reason: "Allowed <by live policy>" });
  assert.equal(liveReason.permissionDecisionReason, "Allowed <by live policy>");
  assert.match(renderToolActivityStackHTML([liveReason], { selectedToolUseId: "live-reason" }), /Allowed &lt;by live policy&gt;/);
});

test("live omitted approval commands remain fail-closed until detail hydration", async () => {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => id === "messages" ? messagesElement : null };
  const state = { agent: { id: "agent-1", cwd: "/work/project" }, pendingToolApprovals: {}, liveToolOutputs: {}, currentMessages: [], messageCopyTexts: [] };
  let resolveDetail;
  let requestedURL = "";
  const detail = new Promise((resolve) => { resolveDetail = resolve; });
  try {
    const controller = createChatRenderingController({
      state,
      apiRequest: async (url) => { requestedURL = url; return detail; },
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });
    controller.rememberToolApproval({
      agentId: "agent-1",
      data: {
        toolUseId: "approval-hydrate",
        toolName: "Bash",
        risk: "exec",
        commandOmitted: true,
        inputJson: { commandPresent: true },
        commandFacts: { parseKnown: true, program: "git", subcommand: "status", commandCount: 1 },
      },
    });
    assert.match(messagesElement.innerHTML, /正在安全加载完整命令/);
    assert.match(messagesElement.innerHTML, /data-approval-decision="allow_once"[^>]*disabled/);
    assert.equal(state.pendingToolApprovals["approval-hydrate"].commandOmitted, true);

    resolveDetail({ status: "pending_approval", inputJson: { command: "git status --short" } });
    await detail;
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(requestedURL, "/api/agents/agent-1/tool-calls/approval-hydrate");
    assert.equal(state.pendingToolApprovals["approval-hydrate"].command, "git status --short");
    assert.equal(state.pendingToolApprovals["approval-hydrate"].commandOmitted, false);
    assert.match(messagesElement.innerHTML, /git status --short/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("approval detail hydration failure keeps allow actions disabled", async () => {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => id === "messages" ? messagesElement : null };
  const state = { agent: { id: "agent-1", cwd: "/work/project" }, pendingToolApprovals: {}, liveToolOutputs: {}, currentMessages: [], messageCopyTexts: [] };
  try {
    const controller = createChatRenderingController({
      state,
      apiRequest: async () => { throw new Error("offline"); },
      attachmentIcon: () => "file",
      attachmentKind: () => "file",
      copyToClipboard: async () => true,
      notifyTerminal: () => {},
      selectedModelValue: () => "",
      shortPath: (value) => value,
      showError: () => {},
      showToast: () => {},
    });
    controller.rememberToolApproval({ agentId: "agent-1", data: { toolUseId: "approval-failed", toolName: "Bash", risk: "exec", commandOmitted: true, inputJson: { commandPresent: true } } });
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(state.pendingToolApprovals["approval-failed"].commandLoadFailed, true);
    assert.match(messagesElement.innerHTML, /无法加载完整命令/);
    assert.match(messagesElement.innerHTML, /data-approval-decision="allow_session"[^>]*disabled/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Edit activity renders escaped structured and fallback diffs with red-green line hooks", () => {
  const fallback = renderToolDiffHTML({
    toolUseId: "edit-fallback",
    toolName: "Edit",
    inputJson: { old_string: "before <unsafe>", new_string: "after <unsafe>" },
  });
  const structured = renderToolDiffHTML({
    toolUseId: "edit-structured",
    toolName: "Edit",
    outputJson: { output: "Edited file", meta: { diff: "--- a/file\n+++ b/file\n@@ -335,1 +335,1 @@\n-old\n+new" } },
  });

  assert.match(fallback, /tool-diff-line del/);
  assert.match(fallback, /tool-diff-line add/);
  assert.match(fallback, /&lt;unsafe&gt;/);
  assert.doesNotMatch(fallback, /<unsafe>/);
  assert.match(structured, /tool-diff-line meta/);
  assert.match(structured, /tool-diff-line-number[^>]*>335</);
});

test("live tool events retain all tools and preserve streamed Bash output after completion", () => {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => messagesElement };
  const state = { agent: { id: "agent-1" }, liveToolOutputs: {}, pendingToolApprovals: {}, currentMessages: [], messageCopyTexts: [] };
  try {
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
    controller.rememberToolStarted({ agentId: "agent-1", createdAt: "2026-01-01T00:00:00Z", data: { tool_use_id: "read-1", tool_name: "Read", run_id: "run-1", input_json: { file_path: "a.txt" }, execution_device_id: "local" } });
    controller.finishToolOutput({ agentId: "agent-1", data: { toolUseId: "read-1", runId: "run-1", status: "completed", resultPreview: "file contents", resultTruncated: true } });
    controller.rememberToolStarted({ agentId: "agent-1", data: { toolUseId: "bash-1", toolName: "Bash", runId: "run-1", input: { command: "printf ok" } } });
    controller.appendToolOutput({ agentId: "agent-1", text: "first\n", data: { toolUseId: "bash-1", runId: "run-1" } });
    controller.appendToolOutput({ agentId: "agent-1", text: "second", data: { toolUseId: "bash-1", runId: "run-1" } });
    controller.finishToolOutput({ agentId: "agent-1", data: { toolUseId: "bash-1", runId: "run-1", status: "completed", duration_ms: 25, resultPreview: "first\nsecond" } });

    assert.equal(state.liveToolOutputs["read-1"].toolName, "Read");
    assert.equal(state.liveToolOutputs["read-1"].output, "file contents");
    assert.equal(state.liveToolOutputs["read-1"].truncated, true);
    assert.equal(state.liveToolOutputs["bash-1"].output, "first\nsecond");
    assert.equal(state.liveToolOutputs["bash-1"].status, "completed");
    assert.equal(state.liveToolOutputs["bash-1"].durationMs, 25);
    assert.doesNotMatch(messagesElement.innerHTML, /first\nsecond/);
    const selected = renderToolActivityStackHTML(Object.values(state.liveToolOutputs), {
      live: true,
      runActive: false,
      selectedToolUseId: "bash-1",
      stackKey: "live:agent-1:run-1",
      totalCount: state.liveToolOutputTotals["agent-1:run-1"],
    });
    assert.match(selected, /first\nsecond/);
    assert.match(selected, /<details class="tool-activity-details" open>/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("live tool activity retains a bounded recent window while preserving the total count", () => {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => messagesElement };
  const state = { agent: { id: "agent-1" }, chatHydrating: true, liveToolOutputs: {}, pendingToolApprovals: {}, currentMessages: [], messageCopyTexts: [] };
  try {
    const controller = createChatRenderingController({
      state,
      attachmentIcon: () => "file", attachmentKind: () => "file", copyToClipboard: async () => true,
      notifyTerminal: () => {}, selectedModelValue: () => "", shortPath: (value) => value, showError: () => {}, showToast: () => {},
    });
    for (let index = 0; index < 45; index += 1) {
      const toolUseId = `read-${index}`;
      const createdAt = `2026-01-01T00:00:${String(index).padStart(2, "0")}Z`;
      controller.rememberToolStarted({ agentId: "agent-1", createdAt, data: { toolUseId, toolName: "Read", runId: "run-bounded", inputJson: { file_path: `${index}.txt` } } });
      controller.finishToolOutput({ agentId: "agent-1", data: { toolUseId, runId: "run-bounded", status: "completed", resultPreview: `done-${index}` } });
    }

    assert.equal(Object.keys(state.liveToolOutputs).length, 40);
    assert.equal(state.liveToolOutputTotals["agent-1:run-bounded"], 45);
    assert.equal(state.liveToolOutputs["read-0"], undefined);
    assert.equal(state.liveToolOutputs["read-44"].status, "completed");
    const html = renderToolActivityStackHTML(Object.values(state.liveToolOutputs), { totalCount: 45 });
    assert.match(html, /data-tool-activity-count="45"/);
    assert.match(html, /data-tool-activity-visible-count="40"/);
    assert.match(html, /另有 5 条/);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("run review loading keeps the existing outcome notice stable without transient loading labels", () => {
  // renderRunSummaryCardHTML() only special-cases runSummaryLoading when
  // there is no run yet (state.activeRunSummary is null); once a run is
  // loaded, a later in-flight refresh must not blank out or replace it with a
  // transient "loading" label.
  const summary = {
    run: { id: "run-1", source: "conversation", status: "interrupted" },
    toolCalls: [],
    recentMessages: [],
  };
  const existing = renderSnapshot([{ role: "assistant", contentText: "reply" }], {
    activeRunSummary: summary,
    activeRunSummaryRunId: "run-1",
    runSummaryLoading: true,
  });
  assert.match(existing.html, /data-chat-report="conversation-run"/);
  assert.match(existing.html, /conversation-run-notice interrupted/);
  assert.doesNotMatch(existing.html, /正在載入任務回顧|正在重新整理|正在加载任务回顾/);

  const firstLoad = renderSnapshot([{ role: "assistant", contentText: "reply" }], {
    activeRunSummary: null,
    activeRunSummaryRunId: "run-1",
    runSummaryLoading: true,
  });
  assert.doesNotMatch(firstLoad.html, /data-run-summary-card|data-run-outcome-card/);
});

test("run review uses complete tool calls and falls back to summary calls when detail loading fails", async () => {
  const fullCall = { toolUseId: "full-1", toolName: "Read", status: "completed", inputJson: { file_path: "full.txt" }, outputJson: { output: "full output", meta: { path: "full.txt" } }, durationMs: 31 };
  const fallbackCall = { toolUseId: "summary-1", toolName: "Grep", status: "error", inputJson: { path: "src", pattern: "x" }, errorMessage: "failed" };
  const summary = { run: { id: "run-1", status: "completed", createdAt: "2026-01-01T00:00:00Z", completedAt: "2026-01-01T00:01:00Z" }, toolCalls: [fallbackCall], recentMessages: [] };
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: () => messagesElement };
  try {
    const state = { agent: { id: "agent-1" }, liveToolOutputs: {}, pendingToolApprovals: {}, currentMessages: [], messageCopyTexts: [] };
    const controller = createChatRenderingController({
      state,
      apiRequest: async (url) => url.includes("/tool-calls?") ? { toolCalls: [fullCall], hasMore: false } : summary,
      attachmentIcon: () => "file", attachmentKind: () => "file", copyToClipboard: async () => true,
      notifyTerminal: () => {}, selectedModelValue: () => "", shortPath: (value) => value, showError: () => {}, showToast: () => {},
    });
    await controller.loadRunSummary("run-1");
    assert.deepEqual(state.activeRunToolCalls, [fullCall]);
    assert.match(messagesElement.innerHTML, /full.txt/);
    assert.doesNotMatch(messagesElement.innerHTML, /full output|&quot;meta&quot;/);
    const selected = renderToolActivityStackHTML(state.activeRunToolCalls, { selectedToolUseId: "full-1", runId: "run-1", stackKey: "run:run-1" });
    assert.match(selected, /full output/);
    assert.doesNotMatch(selected, /&quot;meta&quot;/);

    const fallbackState = { agent: { id: "agent-1" }, liveToolOutputs: {}, pendingToolApprovals: {}, currentMessages: [], messageCopyTexts: [] };
    const fallbackController = createChatRenderingController({
      state: fallbackState,
      apiRequest: async (url) => {
        if (url.includes("/tool-calls?")) throw new Error("detail unavailable");
        return summary;
      },
      attachmentIcon: () => "file", attachmentKind: () => "file", copyToClipboard: async () => true,
      notifyTerminal: () => {}, selectedModelValue: () => "", shortPath: (value) => value, showError: () => {}, showToast: () => {},
    });
    await fallbackController.loadRunSummary("run-1");
    assert.deepEqual(fallbackState.activeRunToolCalls, [fallbackCall]);
    assert.equal(normalizeToolActivity(fallbackState.activeRunToolCalls[0]).status, "error");
  } finally {
    globalThis.document = previousDocument;
  }
});

test("Agent tool activity recognition is exact after case and whitespace normalization", () => {
  for (const value of [
    { toolName: "Agent" },
    { tool_name: "agent" },
    { name: "  AGENT  " },
    { data: { toolName: "AgEnT" } },
  ]) assert.equal(isAgentToolActivity(value), true);

  for (const value of [
    { toolName: "Subagent" },
    { toolName: "AgentTask" },
    { toolName: "my-agent" },
    { name: "DynamicAgentHelper" },
    { toolName: "" },
  ]) assert.equal(isAgentToolActivity(value), false);
});

test("Agent task normalization keeps dispatch separate from child completion and exposes only safe compact fields", () => {
  const prompt = "secret full prompt that belongs only in audit details";
  const tool = {
    toolUseId: "agent-dispatch",
    runId: "parent-run-dispatch",
    toolName: "Agent",
    status: "completed",
    inputJson: {
      prompt,
      description: "Review the auth boundary",
      subagent_type: "reviewer",
      model: "requested-model",
      acceptance_criteria: ["one", "two"],
    },
  };
  const normalized = normalizeAgentTaskActivity(tool);
  assert.equal(normalized.status, "dispatched");
  assert.equal(normalized.toolDispatched, true);
  assert.equal(normalized.description, "Review the auth boundary");
  assert.equal(normalized.role, "reviewer");
  assert.equal(normalized.requestedModel, "requested-model");
  assert.equal(normalized.acceptanceCount, 2);

  const html = renderAgentTaskActivityCardHTML(tool);
  const compact = html.slice(0, html.indexOf("subagent-task-audit"));
  assert.match(html, /data-subagent-card/);
  assert.match(html, /data-subagent-status="dispatched"/);
  assert.match(html, /data-run-id="parent-run-dispatch"/);
  assert.match(html, /role="status" aria-live="polite"/);
  assert.match(html, /status-running/);
  assert.doesNotMatch(html, /status-completed/);
  assert.doesNotMatch(compact, new RegExp(prompt));
  assert.match(html.slice(html.indexOf("subagent-task-audit")), new RegExp(prompt));

  const automaticModel = renderAgentTaskActivityCardHTML({
    toolUseId: "agent-auto-model",
    toolName: "Agent",
    status: "completed",
    inputJson: { prompt: "audit", description: "Auto model" },
  }, { id: "task-auto-model", status: "succeeded", durationMs: 1500 });
  assert.match(automaticModel, /1\.5 s/);
  assert.match(automaticModel, /自动|自動|Auto|subagent\.modelAuto/i);
});

test("Agent task card expansion follows the background task status only", () => {
  const tool = { toolUseId: "agent-status", toolName: "Agent", status: "completed", inputJson: { prompt: "audit" } };
  for (const status of ["queued", "running", "succeeded"]) {
    const html = renderAgentTaskActivityCardHTML(tool, { id: `task-${status}`, status });
    assert.match(html, /<details class="subagent-task-summary">/);
    assert.doesNotMatch(html, /<details class="subagent-task-summary" open>/);
  }
  for (const status of ["waiting_approval", "failed", "canceled", "interrupted"]) {
    const html = renderAgentTaskActivityCardHTML(tool, { id: `task-${status}`, status });
    assert.match(html, /<details class="subagent-task-summary" open>/);
  }
  const waiting = renderAgentTaskActivityCardHTML(tool, { id: "task-waiting", status: "waiting_approval" });
  assert.doesNotMatch(waiting, /子 Agent 内部审批|子代理内部审批/);
});

test("Agent compact failure notice hides prompt and raw errors while audit details retain tool evidence", () => {
  const prompt = "PROMPT_SECRET_DO_NOT_SHOW_COMPACT";
  const toolError = "RAW_TOOL_ERROR_DO_NOT_SHOW_COMPACT";
  const taskError = "RAW_TASK_ERROR_DO_NOT_SHOW_ANYWHERE";
  const html = renderAgentTaskActivityCardHTML({
    toolUseId: "agent-failed",
    toolName: "Agent",
    status: "completed",
    inputJson: { prompt, description: "Safe description" },
    errorMessage: toolError,
  }, {
    id: "task-failed",
    status: "failed",
    errorCode: "child_run_unavailable",
    errorMessage: taskError,
  });
  const auditIndex = html.indexOf("subagent-task-audit");
  const compact = html.slice(0, auditIndex);
  const audit = html.slice(auditIndex);
  assert.doesNotMatch(compact, new RegExp(prompt));
  assert.doesNotMatch(compact, new RegExp(toolError));
  assert.doesNotMatch(compact, new RegExp(taskError));
  assert.match(compact, /subagent-task-notice/);
  assert.match(audit, new RegExp(prompt));
  assert.match(audit, new RegExp(toolError));
  assert.doesNotMatch(html, new RegExp(taskError));
});

test("Agent task card escapes and bounds every compact text and action identifier", () => {
  const hostile = `<img src=x onerror="boom">`;
  const html = renderAgentTaskActivityCardHTML({
    toolUseId: `tool\" onmouseover=\"boom`,
    toolName: "Agent",
    status: "completed",
    inputJson: { prompt: hostile.repeat(800), description: hostile.repeat(100), model: hostile.repeat(100), subagent_type: hostile.repeat(100) },
  }, {
    id: `task\" data-evil=\"boom`,
    status: "running",
    childAgentId: `<child-agent>`,
    childRunId: `<child-run>`,
  });
  assert.match(html, /&lt;img src=x onerror=&quot;boom&quot;&gt;/);
  assert.match(html, /data-task-id="task&quot; data-evil=&quot;boom"/);
  assert.match(html, /data-child-agent-id="&lt;child-agent&gt;"/);
  assert.doesNotMatch(html, /<img|onmouseover="boom"|data-evil="boom"|<child-agent>|<child-run>/);
  assert.ok(html.length < 30_000, "Agent task rendering must remain bounded");
});

test("Agent task actions use explicit identifiers and omit unavailable navigation", () => {
  const tool = { toolUseId: "agent-actions", toolName: "Agent", status: "completed", agentId: "parent-agent", inputJson: { prompt: "audit" } };
  const running = renderAgentTaskActivityCardHTML(tool, {
    id: "task-1",
    status: "running",
    childAgentId: "child-agent-1",
    childRunId: "child-run-1",
  });
  assert.match(running, /data-subagent-action="view-task" data-task-id="task-1"/);
  assert.match(running, /data-subagent-action="cancel" data-task-id="task-1"/);
  assert.match(running, /data-subagent-action="open-agent" data-child-agent-id="child-agent-1"/);
  assert.match(running, /data-subagent-action="open-run" data-child-run-id="child-run-1" data-child-agent-id="child-agent-1"/);

  const runWithoutAgent = renderAgentTaskActivityCardHTML(tool, { id: "task-run-only", status: "succeeded", childRunId: "child-run-only" });
  assert.doesNotMatch(runWithoutAgent, /data-subagent-action="open-run"/);

  const missing = renderAgentTaskActivityCardHTML(tool, null);
  assert.doesNotMatch(missing, /data-subagent-action=/);
  assert.doesNotMatch(missing, /data-task-id=/);
});

test("Agent task resolution supports waiting, safe fallback, exact generic compatibility, and callbacks", () => {
  const agentTool = { toolUseId: "agent-resolve", toolName: "Agent", runId: "parent-run", status: "completed", inputJson: { prompt: "audit", description: "Delegate" } };
  const waiting = renderToolActivityCardHTML(agentTool, { resolveBackgroundTask: () => null });
  assert.match(waiting, /data-chat-report="subagent-task"/);
  assert.match(waiting, /subagent-task-notice/);
  const idOnly = renderToolActivityCardHTML(agentTool, { backgroundTask: { id: "task-id-only" } });
  assert.match(idOnly, /data-task-id="task-id-only"/);
  assert.match(idOnly, /subagent-task-notice/);

  const embedded = renderToolActivityCardHTML({
    ...agentTool,
    output: JSON.stringify({ taskId: "task-from-tool-result", status: "queued" }),
  }, { resolveBackgroundTask: () => null });
  assert.match(embedded, /data-task-id="task-from-tool-result"/);
  assert.match(embedded, /data-subagent-status="dispatched"/);
  assert.match(embedded, /subagent-task-notice/);
  assert.doesNotMatch(embedded, /data-subagent-action="cancel"/);

  const malformed = renderToolActivityCardHTML(agentTool, { resolveBackgroundTask: () => "invalid" });
  assert.match(malformed, /data-chat-report="tool-activity"/);
  assert.doesNotMatch(malformed, /data-subagent-card/);
  const thrown = renderToolActivityCardHTML(agentTool, { resolveBackgroundTask: () => { throw new Error("resolver failed"); } });
  assert.match(thrown, /data-chat-report="tool-activity"/);

  const readTool = { toolUseId: "read-generic", toolName: "Read", status: "completed", inputJson: { file_path: "a.txt" } };
  assert.equal(
    renderToolActivityCardHTML(readTool),
    renderToolActivityCardHTML(readTool, { backgroundTask: { id: "must-not-apply", status: "succeeded" } }),
  );

  let resolvedTool;
  const stack = renderToolActivityStackHTML([agentTool], {
    selectedToolUseId: "agent-resolve",
    resolveBackgroundTask(tool) {
      resolvedTool = tool;
      return { id: "task-resolved", status: "succeeded", publicSummary: { description: "Resolved", acceptanceCount: 3 } };
    },
  });
  assert.equal(resolvedTool.toolUseId, "agent-resolve");
  assert.equal(resolvedTool.runId, "parent-run");
  assert.match(stack, /data-subagent-card/);
  assert.match(stack, /data-task-id="task-resolved"/);
  assert.match(stack, /status-completed/);
});

test("chat rendering controller forwards resolveBackgroundTask to live Agent stacks", () => {
  let resolvedTool;
  const { html } = renderSnapshot([], {
    liveToolOutputs: {
      "agent-live": { agentId: "agent-1", runId: "parent-run", toolUseId: "agent-live", toolName: "Agent", status: "completed", inputJson: { prompt: "audit", description: "Live delegate" } },
    },
    toolActivitySelections: { "live:agent-1:parent-run": "agent-live" },
  }, {}, {
    resolveBackgroundTask(tool) {
      resolvedTool = tool;
      return { id: "task-live", status: "running", childAgentId: "child-live" };
    },
  });
  assert.equal(resolvedTool.toolUseId, "agent-live");
  assert.match(html, /data-live-tool-output-stack/);
  assert.match(html, /data-subagent-card/);
  assert.match(html, /data-task-id="task-live"/);
});

test("live tool activity follows the Agent lifecycle and collapses after completion", () => {
  const liveToolOutputs = {
    "read-live": { agentId: "agent-1", runId: "run-live", toolUseId: "read-live", toolName: "Read", status: "completed", inputJson: { file_path: "src/main.mjs" } },
  };
  const active = renderSnapshot([], {
    agent: { id: "agent-1", cwd: "/work/project", status: "running" },
    liveToolOutputs,
  });
  // Live stacks are collapsed by default now — same as finished ones.
  assert.match(active.html, /data-tool-activity-default="collapsed"/);
  assert.doesNotMatch(active.html, /<details class="tool-activity-group" open>/);

  const finished = renderSnapshot([], {
    agent: { id: "agent-1", cwd: "/work/project", status: "idle" },
    liveToolOutputs,
  });
  assert.match(finished.html, /data-tool-activity-default="collapsed"/);
  assert.doesNotMatch(finished.html, /<details class="tool-activity-group" open>/);
});

test("AskUserQuestion card renders radio inputs for single-select and checkbox inputs for multi-select", () => {
  const single = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-1": {
        toolUseId: "tool-1",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: "Pick one", multiSelect: false, options: [{ label: "A" }, { label: "B" }] },
        ],
      },
    },
  });
  assert.match(single.html, /data-chat-report="user-question"/);
  assert.match(single.html, /data-user-question-card="tool-1"/);
  assert.match(single.html, /data-user-question-block="pick" data-multi="0"/);
  assert.match(single.html, /type="radio"/);
  assert.doesNotMatch(single.html, /type="checkbox"/);

  const multi = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-2": {
        toolUseId: "tool-2",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: "Pick many", multiSelect: true, options: [{ label: "X" }, { label: "Y" }] },
        ],
      },
    },
  });
  assert.match(multi.html, /data-user-question-block="pick" data-multi="1"/);
  assert.match(multi.html, /type="checkbox"/);
  assert.doesNotMatch(multi.html, /type="radio"/);
});

test("AskUserQuestion options render the label in <strong> and description in <small> only when present", () => {
  const { html } = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-3": {
        toolUseId: "tool-3",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          {
            question: "confirm",
            header: "Confirm action",
            multiSelect: false,
            options: [
              { label: "Yes", description: "Proceed right away" },
              { label: "No" },
            ],
          },
        ],
      },
    },
  });
  assert.match(html, /<strong>Yes<\/strong>/);
  assert.match(html, /<small>Proceed right away<\/small>/);
  assert.match(html, /<strong>No<\/strong>/);
  assert.equal((html.match(/<small>/g) || []).length, 1);
});

test("AskUserQuestion card exposes a bounded free-text \"other\" input per question", () => {
  const { html } = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-4": {
        toolUseId: "tool-4",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: "Pick one", multiSelect: false, options: [{ label: "A" }] },
        ],
      },
    },
  });
  assert.match(html, /class="user-question-other"/);
  assert.match(html, /data-question-other="pick"/);
  assert.match(html, /maxlength="2000"/);
});

test("AskUserQuestion submit and skip buttons carry the pending card's toolUseId", () => {
  const { html } = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-5": {
        toolUseId: "tool-5",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: "Pick one", multiSelect: false, options: [{ label: "A" }] },
        ],
      },
    },
  });
  assert.match(html, /data-user-question-submit="tool-5"/);
  assert.match(html, /data-user-question-skip="tool-5"/);
});

test("AskUserQuestion card escapes hostile header, label, and description text", () => {
  const hostile = `<img src=x onerror=boom>`;
  const { html } = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-6": {
        toolUseId: "tool-6",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: hostile, multiSelect: false, options: [{ label: hostile, description: hostile }] },
        ],
      },
    },
  });
  assert.doesNotMatch(html, /<img src=x onerror=boom>/);
  assert.match(html, /&lt;img src=x onerror=boom&gt;/);
});

test("no user-question card renders when there are no pending questions", () => {
  const { html } = renderSnapshot([{ role: "user", contentText: "hello" }], {});
  assert.doesNotMatch(html, /data-user-question-card/);
  assert.doesNotMatch(html, /data-chat-report="user-question"/);
  assert.doesNotMatch(html, /data-approval-stack/);
});

test("AskUserQuestion card renders an expires meta line only when expiresAt is present", () => {
  const withExpiry = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-7": {
        toolUseId: "tool-7",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        expiresAt: "2026-01-01T12:00:00Z",
        questions: [
          { question: "pick", header: "Pick one", multiSelect: false, options: [{ label: "A" }] },
        ],
      },
    },
  });
  assert.match(withExpiry.html, /到期：/);

  const withoutExpiry = renderSnapshot([], {
    pendingUserQuestions: {
      "tool-8": {
        toolUseId: "tool-8",
        agentId: "agent-1",
        createdAt: "2026-01-01T00:00:00Z",
        questions: [
          { question: "pick", header: "Pick one", multiSelect: false, options: [{ label: "A" }] },
        ],
      },
    },
  });
  assert.doesNotMatch(withoutExpiry.html, /到期：/);
});

test("per-message render memoization is transparent: identical snapshots produce identical HTML", () => {
  const messages = [
    { id: "msg-1", role: "user", contentText: "hello there" },
    { id: "msg-2", role: "assistant", contentText: "**hi**\n\n- a\n- b" },
  ];
  const first = renderSnapshot(messages);
  const second = renderSnapshot(messages);
  assert.equal(first.html, second.html);
  assert.match(second.html, /hello there/);
  assert.match(second.html, /<ul><li>a<\/li><li>b<\/li><\/ul>/);
});

test("per-message render cache invalidates when a message's content changes", () => {
  const harness = createAsyncChatRenderingHarness(() => Promise.reject(new Error("unused")));
  try {
    const messages = [{ id: "msg-1", role: "assistant", contentText: "original content" }];
    assert.equal(harness.controller.applyMessageSnapshot(messages, "agent-a"), true);
    assert.match(harness.messagesElement.innerHTML, /original content/);

    const updated = [{ id: "msg-1", role: "assistant", contentText: "revised content" }];
    assert.equal(harness.controller.applyMessageSnapshot(updated, "agent-a"), true);
    assert.match(harness.messagesElement.innerHTML, /revised content/);
    assert.doesNotMatch(harness.messagesElement.innerHTML, /original content/);
  } finally {
    harness.restore();
  }
});

test("per-message render cache invalidates when the editing state toggles onto a message", () => {
  const harness = createAsyncChatRenderingHarness(() => Promise.reject(new Error("unused")));
  try {
    const messages = [{ id: "msg-1", role: "user", contentText: "original text" }];
    assert.equal(harness.controller.applyMessageSnapshot(messages, "agent-a"), true);
    assert.doesNotMatch(harness.messagesElement.innerHTML, /message-correction-editor/);
    assert.match(harness.messagesElement.innerHTML, /class="message-content"><p>original text<\/p><\/div>/);

    harness.state.editingMessageId = "msg-1";
    harness.state.correctionText = "original text";
    harness.state.correctionFiles = [];
    assert.equal(harness.controller.applyMessageSnapshot(messages, "agent-a"), true);
    assert.match(harness.messagesElement.innerHTML, /class="message-correction-editor"/);
    assert.match(harness.messagesElement.innerHTML, /class="message user message-editing chat-message/);

    harness.state.editingMessageId = "";
    assert.equal(harness.controller.applyMessageSnapshot(messages, "agent-a"), true);
    assert.doesNotMatch(harness.messagesElement.innerHTML, /message-correction-editor/);
    assert.match(harness.messagesElement.innerHTML, /class="message-content"><p>original text<\/p><\/div>/);
  } finally {
    harness.restore();
  }
});

test("per-message render cache invalidates when a message's index changes (older message prepended)", () => {
  const harness = createAsyncChatRenderingHarness(() => Promise.reject(new Error("unused")));
  try {
    const second = { id: "msg-2", role: "assistant", contentText: "second message" };
    assert.equal(harness.controller.applyMessageSnapshot([second], "agent-a"), true);
    assert.match(harness.messagesElement.innerHTML, /data-copy-message="0"/);

    const older = { id: "msg-1", role: "user", contentText: "first message" };
    assert.equal(harness.controller.applyMessageSnapshot([older, second], "agent-a"), true);
    assert.doesNotMatch(harness.messagesElement.innerHTML, /data-copy-message="0"[^]*data-copy-message="0"/);
    const matches = [...harness.messagesElement.innerHTML.matchAll(/data-copy-message="(\d+)"/g)].map((match) => match[1]);
    assert.deepEqual(matches, ["0", "1"]);
  } finally {
    harness.restore();
  }
});

// A correction retires the turns after it. They must stay in the transcript —
// deleting them would make the history unreadable — but must be visibly marked,
// and must not offer a correct button of their own, since correcting an already
// retired message would supersede nothing.
test("superseded messages stay visible, are marked, and lose their correct button", () => {
  const rendered = renderSnapshot([
    { id: "m1", role: "user", contentText: "live question" },
    { id: "m2", role: "user", contentText: "withdrawn question", supersededAt: "2026-07-27T00:00:00Z" },
    { id: "m3", role: "assistant", contentText: "withdrawn answer", supersededAt: "2026-07-27T00:00:00Z" },
  ]);

  // Nothing is dropped from the transcript.
  assert.match(rendered.html, /live question/);
  assert.match(rendered.html, /withdrawn question/);
  assert.match(rendered.html, /withdrawn answer/);

  const supersededMarkers = rendered.html.match(/message-superseded/g) || [];
  assert.equal(supersededMarkers.length, 2, "both retired messages should carry the marker class");

  // Only the live user message keeps an edit affordance.
  const correctButtons = rendered.html.match(/data-correct-message="m\d"/g) || [];
  assert.deepEqual(correctButtons, ['data-correct-message="m1"']);
});

test("reasoning renders above the action it explains, and titles itself from the first sentence", () => {
  assert.equal(reasoningStepTitle("  Planning the requirement inference. Then I will read App.css.  "), "Planning the requirement inference");
  assert.equal(reasoningStepTitle("先看一下現有的樣式。接著再改。"), "先看一下現有的樣式");
  assert.equal(reasoningStepTitle("   "), "");

  const calls = [
    { toolUseId: "t1", runId: "run-1", toolName: "Read", status: "completed" },
    { toolUseId: "t2", runId: "run-1", toolName: "Edit", status: "completed" },
  ];
  const html = renderToolActivityStackHTML(calls, {
    expanded: true,
    reasoningSteps: [
      { id: "r1", runId: "run-1", text: "Checking the current styles first. They live in App.css.", beforeToolUseId: "t2" },
      { id: "r2", runId: "run-1", text: "Wrapping up.", beforeToolUseId: "", open: true },
    ],
  });

  const read = html.indexOf('data-tool-activity-select="t1"');
  const reasoning = html.indexOf('data-reasoning-step="r1"');
  const edit = html.indexOf('data-tool-activity-select="t2"');
  const trailing = html.indexOf('data-reasoning-step="r2"');
  assert.ok(read < reasoning, "a step filed under t2 must not jump above the tool before it");
  assert.ok(reasoning < edit, "the step must render immediately above the tool it explains");
  assert.ok(edit < trailing, "a step naming no tool closes the list");
  // The summary line is the title; the full text stays in the expandable body.
  assert.match(html, /<strong>Checking the current styles first<\/strong>/);
  assert.match(html, /tool-activity-reasoning-body">Checking the current styles first\. They live in App\.css\./);
  assert.match(html, /tool-activity-reasoning-step is-open/);

  // Reasoning alone still earns a stack -- that is the gap before the first tool.
  assert.match(renderToolActivityStackHTML([], { reasoningSteps: [{ id: "r1", text: "Thinking it over." }] }), /data-reasoning-step="r1"/);
  assert.equal(renderToolActivityStackHTML([], {}), "");
});

test("a persisted assistant turn keeps its reasoning on the activity surface, not in the bubble", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "assistant",
    contentText: "I updated App.css.",
    reasoningText: "Checking the current styles first.\nThen widening the gap.",
  }]);

  // Reasoning has one home now: the activity stack for its own turn. The old
  // in-bubble disclosure is gone, so live and persisted views agree.
  assert.doesNotMatch(html, /message-reasoning/);
  assert.match(html, /data-message-activity="m1"/);
  assert.match(html, /tool-activity-reasoning-step/);
  assert.match(html, /<strong>Checking the current styles first<\/strong>/);
  assert.match(html, /tool-activity-reasoning-body">Checking the current styles first\.\nThen widening the gap\./);
  // A turn that only thought must not be titled "... · 0 tool calls".
  assert.match(html, /活动 · 1 步推理</);
  assert.ok(
    html.indexOf('data-message-activity="m1"') < html.indexOf('data-message-id="m1"'),
    "the activity leads the assistant answer on every responsive layout",
  );

  // A turn without reasoning must not grow an empty stack, and a user message
  // must never render one even if the field somehow arrives.
  const { html: bare } = renderSnapshot([{ id: "m2", role: "assistant", contentText: "Done." }]);
  assert.doesNotMatch(bare, /message-reasoning|data-message-activity/);
  const { html: user } = renderSnapshot([{ id: "m3", role: "user", contentText: "go", reasoningText: "should not show" }]);
  assert.doesNotMatch(user, /message-reasoning|should not show|data-message-activity/);
});

test("markdown renders headings, emphasis and nested lists instead of printing their syntax", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "assistant",
    contentText: [
      "以下為台灣今日的天氣概況：",
      "",
      "---",
      "### 全台各區天氣概況",
      "",
      "- **北部地區（台北、新竹等）**",
      "  - **天氣狀態**：多雲為主",
      "  - **氣溫**：約 31℃～35℃",
      "",
      "1. **攜帶雨具**：建議隨身攜帶折疊傘。",
      "2. *防曬* 與補水。",
      "",
      "> 註：可搭配中央氣象署即時訊息。",
      "",
      "詳見 [氣象署](https://www.cwa.gov.tw/) 網站。",
    ].join("\n"),
  }]);

  assert.match(html, /<h3>全台各區天氣概況<\/h3>/);
  assert.match(html, /<hr>/);
  assert.match(html, /<strong>北部地區（台北、新竹等）<\/strong>/);
  assert.match(html, /<strong>天氣狀態<\/strong>：多雲為主/);
  assert.match(html, /<ol>[\s\S]*<strong>攜帶雨具<\/strong>/);
  assert.match(html, /<em>防曬<\/em>/);
  assert.match(html, /<blockquote>/);
  assert.match(html, /<a href="https:\/\/www\.cwa\.gov\.tw\/" target="_blank" rel="noopener noreferrer">氣象署<\/a>/);
  // The indented sub-bullets must nest, not flatten into one long column.
  assert.match(html, /<li>[^<]*<strong>北部地區[\s\S]*?<ul><li><strong>天氣狀態/);
  // None of the source syntax may survive as visible text.
  assert.doesNotMatch(html, /\*\*|###/);
});

test("markdown emphasis leaves code, arithmetic and hostile links alone", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "assistant",
    contentText: [
      "Use `a ** b` and `**not bold**` verbatim.",
      "",
      "Math: 3 * 4 * 5 stays plain, and snake_case_name too.",
      "",
      "[click](javascript:alert(1)) and [ok](https://example.com/a?b=1&c=2)",
      "",
      "Digits 12 34 must survive restoration.",
    ].join("\n"),
  }]);

  assert.match(html, /<code class="inline-code">a \*\* b<\/code>/);
  assert.match(html, /<code class="inline-code">\*\*not bold\*\*<\/code>/);
  assert.match(html, /3 \* 4 \* 5 stays plain/);
  assert.match(html, /snake_case_name/);
  assert.doesNotMatch(html, /<em>case<\/em>/);
  // A rejected scheme must leave the source text visible rather than link it.
  assert.doesNotMatch(html, /href="javascript/);
  assert.match(html, /\[click\]\(javascript:alert\(1\)\)/);
  assert.match(html, /<a href="https:\/\/example\.com\/a\?b=1&amp;c=2"/);
  assert.match(html, /Digits 12 34 must survive restoration\./);
});

// The 401 broken-image bug shipped because no test looked at what the src
// actually was. These assertions pin the contract: protected asset paths travel
// in data attributes, and no markup may point a browser-initiated load at /api/.
test("image attachments render a real preview whose bytes are fetched with the API token", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "user",
    contentText: "look",
    attachments: [{ id: "att-1", filename: "shot.png", mimeType: "image/png", sizeBytes: 164675, kind: "image" }],
  }], { agent: { id: "agent-1" } }, {}, { attachmentKind: () => "image" });

  assert.match(html, /class="attachment-image-card"/);
  assert.match(html, /data-protected-image="\/api\/agents\/agent-1\/messages\/m1\/attachments\/att-1"/);
  assert.match(html, /data-attachment-lightbox="\/api\/agents\/agent-1\/messages\/m1\/attachments\/att-1"/);
  assert.match(html, /shot\.png/);
  // A src or href pointing at the guarded API is the bug itself.
  assert.doesNotMatch(html, /src="\/api\//);
  assert.doesNotMatch(html, /href="\/api\//);
  assert.doesNotMatch(html, /loading="lazy"[^>]*data-protected-image/);
});

test("non-image attachments keep the compact file card and download through the token", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "user",
    contentText: "doc",
    attachments: [{ id: "att-2", filename: "spec.pdf", mimeType: "application/pdf", sizeBytes: 2048, kind: "pdf" }],
  }], { agent: { id: "agent-1" } }, {}, { attachmentKind: () => "pdf" });

  assert.match(html, /class="attachment-card"/);
  assert.match(html, /data-attachment-download="\/api\/agents\/agent-1\/messages\/m1\/attachments\/att-2"/);
  assert.doesNotMatch(html, /attachment-image-card/);
  assert.doesNotMatch(html, /href="\/api\//);
});

test("generated images open in the viewer instead of an unauthenticated new tab", () => {
  const { html } = renderSnapshot([{
    id: "m1",
    role: "assistant",
    contentJson: [{ type: "image_generation", assetId: "asset-1", generationId: "gen-1", status: "completed", mimeType: "image/png", filename: "out.png", width: 512, height: 512, outputIndex: 0 }],
  }]);

  assert.match(html, /data-protected-image="[^"]*generated-images\/asset-1"/);
  assert.match(html, /data-image-lightbox="[^"]*generated-images\/asset-1"/);
  assert.match(html, /data-protected-download="[^"]*generated-images\/asset-1\?download=1"/);
  assert.doesNotMatch(html, /src="\/api\//);
  assert.doesNotMatch(html, /target="_blank"/);
});

test("one tool call renders one row even when two surfaces claim it", () => {
  // Reported as "活動" appearing twice for a single turn: one bare row of tools
  // and one row of the same tools beside that turn's reasoning. A call arrives
  // from the run summary, the retained history map and the live stream, and
  // when two copies land on different surfaces the turn draws both. The
  // toolUseId is the identity, so it is what de-duplication keys on.
  const calls = ["t1", "t2", "t3"].map((toolUseId) => ({
    agentId: "agent-1", runId: "run-1", toolUseId, toolName: "Bash", status: "completed",
  }));

  const { html } = renderSnapshot([
    { id: "u1", role: "user", runId: "run-1", contentText: "go" },
    {
      id: "a1",
      role: "assistant",
      runId: "run-1",
      contentText: "Done.",
      reasoningText: "Worked out which commands to run.",
    },
  ], {
    agent: { id: "agent-1", status: "idle" },
    // The same three calls owned by the assistant turn and, simultaneously,
    // present as history for that run without an owning message.
    activeRunSummaryRunId: "run-2",
    activeRunToolCallsRunId: "run-2",
    activeRunToolCalls: calls.map((call) => ({ ...call, messageId: "a1" })),
    historyRunToolCalls: { "run-1": calls.map((call) => ({ ...call })) },
  });

  for (const toolUseId of ["t1", "t2", "t3"]) {
    const seen = (html.match(new RegExp(`data-tool-activity-select="${toolUseId}"`, "g")) || []).length;
    assert.equal(seen, 1, `${toolUseId} must render exactly once, saw ${seen}`);
  }
  // And the turn keeps a single activity row rather than one per surface.
  const summaries = (html.match(/tool-activity-summary/g) || []).length;
  assert.equal(summaries, 1, `expected one activity row, saw ${summaries}`);
});
