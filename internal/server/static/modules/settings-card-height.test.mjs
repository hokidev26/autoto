import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const stylesURL = new URL("../styles/settings-legacy.css", import.meta.url);
const extrasURL = new URL("../styles/extras.css", import.meta.url);

// Server-system still shows two side-by-side key-value cards. Those cards are
// always open and similar in length, so the two-column grid stays, but each
// card keeps its own height instead of stretching to the taller neighbour.
test("伺服器系統頁的明細卡各自撐自己的高度", async () => {
  const styles = await readFile(stylesURL, "utf8");
  const block = styles.match(/\.usage-detail-grid \{[\s\S]*?\}/);
  assert.ok(block, "找不到 .usage-detail-grid 的樣式");
  assert.match(block[0], /align-items:\s*start/, "不對齊到 start 的話短卡片會被拉高留白");
  assert.match(block[0], /grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/, "仍然是兩欄");
});

test("執行資源的可收縮卡片改成單欄，開合時不會左右高度不齊", async () => {
  const styles = await readFile(extrasURL, "utf8");
  const block = styles.match(/\.runtime-page \.usage-detail-grid\.runtime-resource-stack\s*\{[\s\S]*?\}/);
  assert.ok(block, "找不到執行資源單欄規則");
  assert.match(block[0], /grid-template-columns:\s*1fr/);
});
