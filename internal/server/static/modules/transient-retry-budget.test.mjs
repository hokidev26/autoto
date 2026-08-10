import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

const settingsURL = new URL("./system-settings.mjs", import.meta.url);
const stylesURL = new URL("../styles/settings-legacy.css", import.meta.url);
const messagesURL = new URL("./messages-system-settings.mjs", import.meta.url);

// The retry ceiling joins the execution-budget table rather than being hand-rolled
// beside it, so it inherits the unlimited toggle, the remembered draft, and the -1
// wire form the budgets already use.
test("重試次數是預算表格的一員，預設 10、範圍 0 到 64", async () => {
  const source = await readFile(settingsURL, "utf8");
  const row = source.match(/\{ key: "maxTransientRetries",[^}]*\}/);
  assert.ok(row, "重試次數必須登記在 executionBudgetFields");
  assert.match(row[0], /id: "runtimeBudgetTransientRetries"/);
  assert.match(row[0], /labelKey: "transientRetriesBudget"/);
  assert.match(row[0], /fallback: 10\b/, "預設應為 10 次");
  assert.match(row[0], /min: 0\b/);
  assert.match(row[0], /max: 64\b/);
  // Retries live on the agent config, not inside continuation, so the row has to
  // say where to read its value from.
  assert.match(row[0], /source: "agent"/);
});

test("卡片會把 agent 傳進去，否則這一列讀不到值", async () => {
  const source = await readFile(settingsURL, "utf8");
  assert.match(source, /renderExecutionBudgetCard\(agent\.continuation \|\| \{\}, agent\)/);
  assert.match(source, /function renderExecutionBudgetCard\(continuation, agent = \{\}\)/);
  assert.match(
    source,
    /field\.source === "agent" \? agent\?\.\[field\.key\] : continuation\[field\.key\]/,
    "來源是 agent 的欄位要從 agent 讀，其餘仍從 continuation 讀",
  );
});

test("三個語言都有重試次數的標籤", async () => {
  const messages = await readFile(messagesURL, "utf8");
  const labels = [...messages.matchAll(/transientRetriesBudget:\s*"([^"]+)"/g)].map((match) => match[1]);
  assert.equal(labels.length, 3, "zh-CN、zh-TW、en 各要一份");
  for (const label of labels) {
    assert.ok(label.trim().length > 0, "標籤不能是空字串");
  }
  assert.equal(new Set(labels).size >= 2, true, "至少中英文要不同，才知道有真的翻譯");
});


