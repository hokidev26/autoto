import test from "node:test";
import assert from "node:assert/strict";
import { createAgentWorkspaceHelpers, resolveComposerActivityStatus } from "./agent-workspace-helpers.mjs";

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
  }[key] || key;
}

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

test("desktop project conversations use the task summary while mobile and standalone views keep the composer fallback", () => {
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
    assert.equal(wrapper.classList.contains("is-busy"), false);
    assert.notEqual(label.textContent, "思考中");

    mobileViewport = true;
    helpers.refreshComposerActivityStatus();
    assert.equal(routed[1], null);
    assert.equal(label.textContent, "思考中");
    assert.equal(wrapper.classList.contains("is-busy"), true);

    mobileViewport = false;
    projectContext = false;
    helpers.refreshComposerActivityStatus();
    assert.equal(routed[2], null);
    assert.equal(label.textContent, "思考中");
    assert.equal(wrapper.classList.contains("is-busy"), true);
  } finally {
    globalThis.document = previousDocument;
  }
});
