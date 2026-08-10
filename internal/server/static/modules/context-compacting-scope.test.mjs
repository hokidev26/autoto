import assert from "node:assert/strict";
import test from "node:test";

import { contextCompactingForAgent, resolveComposerActivityStatus } from "./agent-workspace-helpers.mjs";

const translate = (key) => ({
  "chat.activity.compacting": "壓縮中",
  "chat.activity.thinking": "思考中",
}[key] || key);

// A manual compaction has no run behind it, so nothing reaches a terminal state
// to clear a stray flag. An unscoped boolean therefore followed the reader into
// an unrelated conversation and left it claiming to be compacting.
test("壓縮狀態只屬於觸發它的對話", () => {
  const compactingAgent = { contextCompacting: "agent-a", agent: { id: "agent-a" } };
  assert.equal(contextCompactingForAgent(compactingAgent), true);

  const switchedAway = { contextCompacting: "agent-a", agent: { id: "agent-b" } };
  assert.equal(contextCompactingForAgent(switchedAway), false, "另一個對話不該顯示壓縮中");

  assert.deepEqual(
    resolveComposerActivityStatus({ ...switchedAway, agent: { id: "agent-b", status: "running" } }, translate),
    { kind: "thinking", text: "思考中" },
  );
});

// The flag must not survive losing track of which agent is on screen, or it
// reappears the moment any conversation opens.
test("沒有目前對話時不顯示壓縮中", () => {
  assert.equal(contextCompactingForAgent({ contextCompacting: "agent-a", agent: null }), false);
  assert.equal(contextCompactingForAgent({ contextCompacting: "agent-a", agent: { id: "" } }), false);
});

// Callers that only know "something is compacting" keep working, so the scoping
// change cannot silently disable the notice.
test("非字串的真值維持不分對話", () => {
  assert.equal(contextCompactingForAgent({ contextCompacting: true }), true);
  assert.equal(contextCompactingForAgent({ contextCompacting: true, agent: { id: "agent-a" } }), true);
});

test("清除後回到一般狀態", () => {
  assert.equal(contextCompactingForAgent({ contextCompacting: false, agent: { id: "agent-a" } }), false);
  assert.equal(contextCompactingForAgent({}), false);
  assert.equal(
    resolveComposerActivityStatus({ contextCompacting: false, providerRetry: null }, translate),
    null,
  );
});

// Ordering the notice above tool and thinking states is the existing contract and
// must survive the scoping change.
test("壓縮中仍然優先於工具與思考狀態", () => {
  assert.deepEqual(
    resolveComposerActivityStatus({
      agent: { id: "agent-a", status: "running" },
      contextCompacting: "agent-a",
      providerRetry: { attempt: 1, maxAttempts: 3 },
      liveToolOutputs: {
        "tool-1": { toolUseId: "tool-1", toolName: "Read", status: "running", inputJson: { file_path: "a.go" } },
      },
    }, translate),
    { kind: "compacting", text: "壓縮中" },
  );
});
