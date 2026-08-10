import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The subagent refresh asks for both forceRender and preserveScroll: rebuild the
// DOM, but leave the viewport where the reader put it. The follow decision tested
// forceRender first, so preserveScroll was silently dropped. That refresh runs on
// every background-task update while an answer streams, which is what pulled the
// conversation upward under a reader who had scrolled back to read something.
test("preserveScroll 的優先權高於 forceRender", () => {
  const source = readFileSync(new URL("./chat-rendering.mjs", import.meta.url), "utf8");
  assert.match(
    source,
    /const shouldFollowTail = !options\.preserveScroll && \(options\.forceRender === true \|\| wasFollowing\);/,
    "preserveScroll 必須先被檢查，否則要求保留位置的呼叫端會被忽略",
  );
  assert.doesNotMatch(
    source,
    /const shouldFollowTail = options\.forceRender === true \|\|/,
    "不可以再把 forceRender 放在 preserveScroll 前面",
  );
});

// Pin the caller's intent too, so the pair cannot drift apart again.
test("子代理刷新仍然同時要求重建與保留捲動位置", () => {
  const source = readFileSync(new URL("./subagent-cards.mjs", import.meta.url), "utf8");
  assert.match(source, /applyMessageSnapshot\(state\.currentMessages, agentId, \{ forceRender: true, preserveScroll: true \}\)/);
});

// The truth table the fix has to satisfy.
test("跟隨結尾的判定涵蓋四種組合", () => {
  const shouldFollowTail = (options, wasFollowing) =>
    !options.preserveScroll && (options.forceRender === true || wasFollowing);

  // Opening a conversation: land on the newest message.
  assert.equal(shouldFollowTail({ forceRender: true }, false), true);
  // Sending: the echo is an explicit request to see the latest.
  assert.equal(shouldFollowTail({ forceRender: true }, true), true);
  // Subagent refresh mid-stream: the reader's position wins.
  assert.equal(shouldFollowTail({ forceRender: true, preserveScroll: true }, false), false);
  assert.equal(shouldFollowTail({ forceRender: true, preserveScroll: true }, true), false);
  // Ordinary rebuild: follow only if the reader was already at the tail.
  assert.equal(shouldFollowTail({}, true), true);
  assert.equal(shouldFollowTail({}, false), false);
});
