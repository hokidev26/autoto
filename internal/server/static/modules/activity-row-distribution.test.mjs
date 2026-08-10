import assert from "node:assert/strict";
import test from "node:test";

import { createChatRenderingController } from "./chat-rendering.mjs";

// A run makes one assistant turn per tool round, and most of those turns are
// invisible: their only content is a tool_use block, whose text is stripped, so
// they never reach the transcript. Their tool calls arrive unowned and have to be
// adopted by a turn that is on screen.
//
// Adoption used to hand every unowned call of a run to one turn, so a single row
// carried the whole run ("1 步推理 · 9 次工具") while the rows around it showed
// nothing, even though each of those rows is a real round that ran its own tools.
// Each call now goes to the last visible turn saved at or before it happened, so
// the counts describe the round the reader is looking at and stay in order.
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

function render(messages, stateOverrides = {}) {
  const messagesElement = fakeMessagesElement();
  const previousDocument = globalThis.document;
  globalThis.document = { getElementById: (id) => (id === "messages" ? messagesElement : null) };
  const state = {
    agent: { id: "agent-1", cwd: "/work/project", status: "running" },
    navigationSelectionKind: "conversation",
    currentMessages: [],
    messageCopyTexts: [],
    liveToolOutputs: {},
    liveImageGenerations: {},
    pendingToolApprovals: {},
    blockedToolNotices: {},
    pendingUserQuestions: {},
    activeRunSummary: null,
    activeRunSummaryRunId: "",
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
    });
    assert.equal(controller.applyMessageSnapshot(messages, "agent-1"), true);
    return messagesElement.innerHTML;
  } finally {
    globalThis.document = previousDocument;
  }
}

function stacks(html) {
  return [...html.matchAll(/<section class="[^"]*tool-activity-stack[^"]*"([^>]*)>([\s\S]*?)<\/section>/g)].map((match) => ({
    key: (match[1].match(/data-tool-activity-stack-key="([^"]*)"/) || [])[1] || "",
    count: Number((match[1].match(/data-tool-activity-count="(\d+)"/) || [])[1] || 0),
    body: match[2],
  }));
}

// Three visible turns of one run, saved in order. The tool calls belong to the
// invisible rounds between them, so they carry no messageId.
const messages = [
  { id: "u1", role: "user", contentText: "go", createdAt: "2026-08-10T10:00:00Z" },
  { id: "a1", role: "assistant", runId: "run-1", contentText: "First step.", createdAt: "2026-08-10T10:00:10Z" },
  { id: "a2", role: "assistant", runId: "run-1", contentText: "Second step.", createdAt: "2026-08-10T10:00:20Z" },
  { id: "a3", role: "assistant", runId: "run-1", contentText: "Third step.", createdAt: "2026-08-10T10:00:30Z" },
];

function unownedCall(toolUseId, toolName, createdAt) {
  return { agentId: "agent-1", runId: "run-1", messageId: "", toolUseId, toolName, status: "completed", createdAt };
}

test("未歸屬的工具呼叫依時間分配到各自的可見回合", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveToolOutputs: {
      // One after a1, two after a2, one after a3.
      t1: unownedCall("t1", "Read", "2026-08-10T10:00:12Z"),
      t2: unownedCall("t2", "Grep", "2026-08-10T10:00:22Z"),
      t3: unownedCall("t3", "Bash", "2026-08-10T10:00:24Z"),
      t4: unownedCall("t4", "Edit", "2026-08-10T10:00:34Z"),
    },
  });
  const rows = stacks(html);
  const counts = Object.fromEntries(rows.filter((row) => row.key.startsWith("msg:")).map((row) => [row.key, row.count]));
  assert.deepEqual(counts, { "msg:a1": 1, "msg:a2": 2, "msg:a3": 1 }, "每個回合的工具數要對應它自己那段時間");
});

test("一個回合不會獨吞整個 run 的工具", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveToolOutputs: {
      t1: unownedCall("t1", "Read", "2026-08-10T10:00:12Z"),
      t2: unownedCall("t2", "Grep", "2026-08-10T10:00:22Z"),
      t3: unownedCall("t3", "Bash", "2026-08-10T10:00:34Z"),
    },
  });
  const rows = stacks(html).filter((row) => row.key.startsWith("msg:"));
  assert.ok(rows.length >= 2, "工具要分散到多列，而不是集中在一列");
  for (const row of rows) {
    assert.ok(row.count < 3, `${row.key} 不該拿到全部工具，實際 ${row.count}`);
  }
});

test("早於第一個可見回合的工具留在第一列", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveToolOutputs: { t0: unownedCall("t0", "Read", "2026-08-10T10:00:05Z") },
  });
  const first = stacks(html).find((row) => row.key === "msg:a1");
  assert.ok(first, "沒有更早的列可以承接，要落在第一列");
  assert.equal(first.count, 1);
});

test("沒有時間戳記時仍然有歸屬，不會消失", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveToolOutputs: { tx: { agentId: "agent-1", runId: "run-1", messageId: "", toolUseId: "tx", toolName: "Read", status: "completed" } },
  });
  const total = stacks(html)
    .filter((row) => row.key.startsWith("msg:"))
    .reduce((sum, row) => sum + row.count, 0);
  assert.equal(total, 1, "缺少時間戳記的工具仍要出現在某一列");
});

test("已明確標記所有者的工具不受影響", () => {
  const html = render(messages, {
    liveAssistantRunId: "run-1",
    liveToolOutputs: {
      owned: { agentId: "agent-1", runId: "run-1", messageId: "a2", toolUseId: "owned", toolName: "Read", status: "completed", createdAt: "2026-08-10T10:00:34Z" },
    },
  });
  const counts = Object.fromEntries(stacks(html).filter((row) => row.key.startsWith("msg:")).map((row) => [row.key, row.count]));
  assert.deepEqual(counts, { "msg:a2": 1 }, "標記過的工具要留在它自己的回合，即使時間較晚");
});
