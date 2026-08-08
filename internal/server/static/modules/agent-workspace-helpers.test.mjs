import test from "node:test";
import assert from "node:assert/strict";
import { createAgentWorkspaceHelpers, resolveComposerActivityStatus, waitingOnBackgroundTasks, withDelegatedActivitySuffix } from "./agent-workspace-helpers.mjs";

function translate(key) {
  return {
    "chat.activity.thinking": "思考中",
    "chat.activity.generating": "正在生成",
    "chat.activity.searching": "正在搜索",
    "chat.activity.reading": "正在读取",
    "chat.activity.editing": "正在编辑",
    "chat.activity.writing": "正在写入",
    "chat.activity.runningCommand": "正在执行命令",
    "chat.activity.genericStep": "正在处理",
    "chat.activity.awaitingApproval": "等待批准",
    "chat.activity.retrying": "重试中",
    "chat.activity.compacting": "压缩中",
  }[key] || key;
}

test("a provider retry reports itself with the attempt count", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      agent: { status: "running" },
      providerRetry: { attempt: 2, maxAttempts: 3 },
    }, translate),
    { kind: "retrying", text: "重试中 2/3" },
  );

  // Missing counters must not render "undefined/undefined".
  assert.deepEqual(
    resolveComposerActivityStatus({ agent: { status: "running" }, providerRetry: {} }, translate),
    { kind: "retrying", text: "重试中" },
  );
});

test("retrying outranks the thinking state it would otherwise be mistaken for", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      agent: { status: "running" },
      liveAssistantActive: true,
      liveAssistantText: "partial",
      providerRetry: { attempt: 1, maxAttempts: 3 },
    }, translate),
    { kind: "retrying", text: "重试中 1/3" },
  );
});

test("compaction outranks retry and tool activity", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      agent: { status: "running" },
      contextCompacting: true,
      providerRetry: { attempt: 1, maxAttempts: 3 },
      liveToolOutputs: {
        "tool-1": { toolUseId: "tool-1", toolName: "Read", status: "running", inputJson: { file_path: "a.go" } },
      },
    }, translate),
    { kind: "compacting", text: "压缩中" },
  );
});

test("a pending approval still outranks compaction, since it blocks on the user", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      contextCompacting: true,
      pendingToolApprovals: { "tool-2": { toolUseId: "tool-2", toolName: "Bash" } },
    }, translate),
    { kind: "approval", text: "等待批准 · Bash" },
  );
});

test("cleared retry and compaction flags fall back to the ordinary states", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      agent: { status: "running" },
      providerRetry: null,
      contextCompacting: false,
    }, translate),
    { kind: "thinking", text: "思考中" },
  );
  assert.equal(
    resolveComposerActivityStatus({ providerRetry: null, contextCompacting: false }, translate),
    null,
  );
});

test("attachment classification recognizes only allowlisted browser video MIME types", () => {
  const helpers = createAgentWorkspaceHelpers({ state: {} });
  assert.equal(helpers.attachmentKind({ name: "clip.mp4", type: "video/mp4" }), "video");
  assert.equal(helpers.attachmentKind({ name: "clip.webm", type: "video/webm" }), "video");
  assert.equal(helpers.attachmentKind({ name: "clip.mov", type: "video/quicktime" }), "binary");
  assert.equal(helpers.attachmentIcon("video"), "VIDEO");
});

test("composer activity prefers pending approval, then tools, then thinking/generating", () => {
  assert.equal(resolveComposerActivityStatus({}, translate), null);

  assert.deepEqual(
    resolveComposerActivityStatus({ agent: { status: "running" } }, translate),
    { kind: "thinking", text: "思考中" },
  );

  assert.deepEqual(
    resolveComposerActivityStatus({ liveAssistantActive: true, liveAssistantText: "" }, translate),
    { kind: "thinking", text: "思考中" },
  );

  assert.deepEqual(
    resolveComposerActivityStatus({ liveAssistantActive: true, liveAssistantText: "hello" }, translate),
    { kind: "generating", text: "正在生成" },
  );

  assert.deepEqual(
    resolveComposerActivityStatus({
      liveAssistantActive: true,
      liveAssistantText: "hello",
      liveToolOutputs: {
        "tool-1": {
          toolUseId: "tool-1",
          toolName: "Read",
          status: "running",
          createdAt: "2026-07-21T00:00:01Z",
          inputJson: { file_path: "/work/project/main.go" },
        },
      },
    }, translate),
    { kind: "tool", text: "正在读取 main.go" },
  );

  assert.deepEqual(
    resolveComposerActivityStatus({
      liveToolOutputs: {
        "tool-1": { toolUseId: "tool-1", toolName: "Read", status: "running", inputJson: { file_path: "a.go" } },
      },
      pendingToolApprovals: {
        "tool-2": { toolUseId: "tool-2", toolName: "Bash" },
      },
    }, translate),
    { kind: "approval", text: "等待批准 · Bash" },
  );
});

test("desktop conversations use the task summary and only mobile keeps the composer fallback", () => {
  const previousDocument = globalThis.document;
  const classes = () => {
    const values = new Set();
    return {
      contains: (name) => values.has(name),
      toggle(name, force) {
        if (force) values.add(name);
        else values.delete(name);
      },
    };
  };
  const wrapper = {
    attributes: {},
    classList: classes(),
    setAttribute(name, value) { this.attributes[name] = String(value); },
  };
  const label = { textContent: "", closest: () => wrapper };
  const dot = { classList: classes() };
  globalThis.document = {
    getElementById(id) {
      if (id === "composerStatusText") return label;
      if (id === "composerStatusDot") return dot;
      return null;
    },
    querySelector: () => wrapper,
  };
  let projectContext = true;
  let mobileViewport = false;
  const routed = [];
  try {
    const helpers = createAgentWorkspaceHelpers({
      state: { agent: { id: "agent-1", status: "running" }, liveToolOutputs: {}, pendingToolApprovals: {} },
      getBackgroundTasks: () => ({ setForegroundActivity: (activity) => routed.push(activity) }),
      projectOperationContextActive: () => projectContext,
      isMobileAppViewport: () => mobileViewport,
    });

    helpers.refreshComposerActivityStatus();
    assert.deepEqual(routed[0], { kind: "thinking", text: "思考中" });
    // The text belongs to the task summary so it is not duplicated here, but the
    // pill must still read as busy: an idle grey dot sitting beside a task
    // summary animating a running step looked like a stalled workspace.
    assert.equal(wrapper.classList.contains("is-busy"), true);
    assert.equal(dot.classList.contains("busy"), true);
    assert.equal(dot.classList.contains("ok"), false);
    assert.notEqual(label.textContent, "思考中");

    mobileViewport = true;
    helpers.refreshComposerActivityStatus();
    assert.equal(routed[1], null);
    assert.equal(label.textContent, "思考中");
    assert.equal(wrapper.classList.contains("is-busy"), true);

    // A conversation outside a project routes too. Requiring the project context
    // left an ordinary chat reporting no running task for a whole turn, which is
    // the one place the user looks to see what the agent is doing.
    mobileViewport = false;
    projectContext = false;
    helpers.refreshComposerActivityStatus();
    assert.deepEqual(routed[2], { kind: "thinking", text: "思考中" });
    assert.notEqual(label.textContent, "思考中");
    assert.equal(wrapper.classList.contains("is-busy"), true);
  } finally {
    globalThis.document = previousDocument;
  }
});

// Dispatching a subagent returns a handle and ends the parent run, so every
// local signal reads idle while the work is still going. The task counts are the
// only thing that knows, and the parent being blocked is different information
// from the parent working: amber, not the busy blue.
test("waiting on a child agent is its own state, not idle and not working", () => {
  const waitTranslate = (key, params = {}) => {
    if (key === "chat.activity.waitingSubagent") return `waiting ${params.count}`;
    if (key === "chat.activity.waitingSubagentQueued") return `queued ${params.count}`;
    return translate(key);
  };

  assert.equal(waitingOnBackgroundTasks(null, waitTranslate), null, "no controller means nothing to report");
  assert.equal(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 0, queuedCount: 0 }) }, waitTranslate),
    null,
    "an idle agent with no tasks stays idle",
  );

  assert.deepEqual(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 2, queuedCount: 0 }) }, waitTranslate),
    { kind: "waiting", tone: "waiting", text: "waiting 2" },
  );

  // Queued still counts as waiting: the work is accepted and the parent cannot
  // proceed, which is the same thing to the person watching.
  assert.deepEqual(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 0, queuedCount: 1 }) }, waitTranslate),
    { kind: "waiting", tone: "waiting", text: "queued 1" },
  );
});

// The local activity is the parent's own work and has to win: "editing app.mjs"
// says more than "waiting on a subagent" when both are true.
test("the parent's own activity outranks waiting on a child", () => {
  const local = resolveComposerActivityStatus({
    agent: { status: "running" },
    liveAssistantActive: true,
    liveAssistantText: "writing it out",
  }, translate);
  assert.equal(local.kind, "generating", "the parent's own state is still resolved first");
});

// Local work and delegated work are both true at once, and the bar had room for
// only the first. A turn that dispatched sub-agents and then kept editing
// reported just "editing", so the delegated work vanished from the one place the
// user looks for it.
test("local activity carries the delegated count alongside it", () => {
  const suffixTranslate = (key, params = {}) => {
    if (key === "chat.activity.alsoRunningSubagents") return `also ${params.count} running`;
    return translate(key);
  };
  const local = { kind: "tool", text: "editing app.mjs" };

  assert.deepEqual(
    withDelegatedActivitySuffix(local, { getSummary: () => ({ runningCount: 2, queuedCount: 0 }) }, suffixTranslate),
    { kind: "tool", text: "editing app.mjs · also 2 running" },
  );

  // Nothing delegated: the text must be left exactly as it was, not decorated
  // with a zero.
  assert.deepEqual(
    withDelegatedActivitySuffix(local, { getSummary: () => ({ runningCount: 0, queuedCount: 0 }) }, suffixTranslate),
    local,
  );
  assert.equal(withDelegatedActivitySuffix(null, { getSummary: () => ({ runningCount: 2 }) }, suffixTranslate), null);

  // The waiting state already counts children; suffixing it would say it twice.
  const waiting = { kind: "waiting", tone: "waiting", text: "waiting 2" };
  assert.deepEqual(
    withDelegatedActivitySuffix(waiting, { getSummary: () => ({ runningCount: 2 }) }, suffixTranslate),
    waiting,
  );
});

// "waiting for 1 sub-agent" says nothing the dot did not already say. With a
// single child, its title is the useful thing.
test("a single delegated task is named rather than counted", () => {
  const waitTranslate = (key, params = {}) => {
    if (key === "chat.activity.waitingSubagent") return `waiting ${params.count}`;
    if (key === "chat.activity.waitingSubagentQueued") return `queued ${params.count}`;
    return translate(key);
  };

  assert.equal(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 1, queuedCount: 0, current: { title: "Review the migration" } }) }, waitTranslate).text,
    "Review the migration",
  );
  // Two children have no single name to show, so the count stands.
  assert.equal(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 2, queuedCount: 0, current: { title: "Review the migration" } }) }, waitTranslate).text,
    "waiting 2",
  );
  // One child with no title yet falls back rather than rendering an empty pill.
  assert.equal(
    waitingOnBackgroundTasks({ getSummary: () => ({ runningCount: 1, queuedCount: 0, current: {} }) }, waitTranslate).text,
    "waiting 1",
  );
});
