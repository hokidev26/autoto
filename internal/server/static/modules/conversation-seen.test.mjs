import assert from "node:assert/strict";
import test from "node:test";

import {
  conversationSeenStorageKey,
  conversationUnread,
  markConversationSeen,
  readSeenMap,
} from "./conversation-seen.mjs";

class MemoryStorage {
  constructor(entries = []) {
    this.map = new Map(entries);
  }
  getItem(key) {
    return this.map.has(key) ? this.map.get(key) : null;
  }
  setItem(key, value) {
    this.map.set(key, String(value));
  }
  removeItem(key) {
    this.map.delete(key);
  }
}

const replied = { agentId: "a1", agentStatus: "idle", lastActivityAt: "2026-01-01T00:00:10.000Z" };

test("一個回覆完但沒看過的對話是未讀", () => {
  assert.equal(conversationUnread(replied, {}), true);
});

test("看過之後就不是未讀", () => {
  const storage = new MemoryStorage();
  markConversationSeen(replied.agentId, replied.lastActivityAt, storage);
  assert.equal(conversationUnread(replied, readSeenMap(storage)), false);
});

test("看過之後又有新回覆會重新變成未讀", () => {
  const storage = new MemoryStorage();
  markConversationSeen(replied.agentId, replied.lastActivityAt, storage);
  const newer = { ...replied, lastActivityAt: "2026-01-01T00:00:20.000Z" };
  assert.equal(conversationUnread(newer, readSeenMap(storage)), true);
});

test("執行中和錯誤狀態不算未讀，它們有自己的顏色", () => {
  for (const status of ["running", "pending", "queued", "error", "failed"]) {
    assert.equal(conversationUnread({ ...replied, agentStatus: status }, {}), false, status);
  }
});

test("已讀標記只前進不後退", () => {
  const storage = new MemoryStorage();
  markConversationSeen("a1", "2026-01-01T00:00:20.000Z", storage);
  assert.equal(markConversationSeen("a1", "2026-01-01T00:00:10.000Z", storage), false);
  assert.equal(readSeenMap(storage).a1, Date.parse("2026-01-01T00:00:20.000Z"));
});

test("壞掉的 localStorage 內容不會讓讀取拋錯", () => {
  const storage = new MemoryStorage([[conversationSeenStorageKey, "{not json"]]);
  assert.deepEqual(readSeenMap(storage), {});
  assert.equal(conversationUnread(replied, readSeenMap(storage)), true);
});

test("沒有活動時間就無法判定未讀，視為已讀避免誤報", () => {
  assert.equal(conversationUnread({ agentId: "a1", agentStatus: "idle" }, {}), false);
});

test("記錄數量有上限，保留最新的", () => {
  const storage = new MemoryStorage();
  for (let i = 0; i < 520; i += 1) {
    markConversationSeen(`agent-${i}`, 1000 + i, storage);
  }
  const seen = readSeenMap(storage);
  assert.equal(Object.keys(seen).length, 500);
  assert.equal(Object.hasOwn(seen, "agent-519"), true);
  assert.equal(Object.hasOwn(seen, "agent-0"), false);
});
