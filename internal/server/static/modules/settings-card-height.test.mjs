import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const stylesURL = new URL("../styles/settings-legacy.css", import.meta.url);

// The two cards in a runtime-resources row are forms of very different lengths:
// the background-task card ends at its save button while the execution-budget card
// beside it runs several rows longer. The grid's default stretch pulled the short
// one down to match, leaving a bordered box with a third of it empty.
test("執行資源的卡片各自撐自己的高度", async () => {
  const styles = await readFile(stylesURL, "utf8");
  const block = styles.match(/\.usage-detail-grid \{[\s\S]*?\}/);
  assert.ok(block, "找不到 .usage-detail-grid 的樣式");
  assert.match(block[0], /align-items:\s*start/, "不對齊到 start 的話短卡片會被拉高留白");
  assert.match(block[0], /grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/, "仍然是兩欄");
});
