import test from "node:test";
import assert from "node:assert/strict";

import { createSystemSettingsController } from "./system-settings.mjs";
import { readStylesSource } from "./styles-source-helper.mjs";

const stylesURL = new URL("../styles.css", import.meta.url);

// The runtime-resources page is five stacked forms, so reaching the one you want
// meant scrolling past all of them. These cards now fold the same way the Shared
// API page folds its provider eligibility / account pool / access key sections:
// a native <details> whose <summary> is the card's own title.
function runtimeSummary() {
  return {
    memory: { allocBytes: 1024, sysBytes: 2048, gcCycles: 3, heapInuseBytes: 512, stackInuseBytes: 64, nextGcBytes: 4096, totalAllocBytes: 8192 },
    go: { goroutines: 7, cpus: 8 },
    agent: {
      defaultModel: "provider:model",
      maxTurns: 200,
      firstTokenTimeoutMs: 60000,
      maxTransientRetries: 10,
      continuation: { mode: "safe", segmentTurns: 40, maxContinuations: 8, maxTotalTurns: 200, maxRunDurationMs: 3600000, maxRunTokens: 2000000 },
      nonRetryableErrorPatterns: ["insufficient balance"],
    },
    security: { maxPermissionMode: "bypassPermissions" },
    backgroundTasks: { workerCount: 8, perAgentLimit: 4, allowNestedSubagents: false, maxSubagentDepth: 2 },
    generatedAt: "2026-07-29T10:00:00Z",
  };
}

function runtimeMarkup() {
  return createSystemSettingsController({ state: { runtimeSummary: runtimeSummary() } }).renderRuntimeSettingsContent();
}

// The five cards in .usage-detail-grid, identified the way the page itself
// distinguishes them. The two key-value cards carry no modifier class, so they
// are counted rather than matched individually.
const modifierCards = ["background-task-settings-card", "execution-budget-card", "retry-policy-card"];

test("執行資源底下每張卡片都是可收縮的 details", () => {
  const markup = runtimeMarkup();

  // Five cards, five disclosures: the two key-value cards plus the three forms.
  assert.equal((markup.match(/<details class="runtime-resource-details"/g) || []).length, 5);
  assert.match(markup, /usage-detail-grid runtime-resource-stack/);
  assert.equal((markup.match(/class="runtime-resource-body"/g) || []).length, 5);

  for (const card of modifierCards) {
    const section = markup.indexOf(`${card}"`);
    assert.ok(section > -1, `找不到 ${card}`);
    // The disclosure wraps the card's own body, so it has to open inside the section.
    assert.match(markup.slice(section, section + 200), /<details class="runtime-resource-details"/, `${card} 的內容沒有被 details 包住`);
  }
});

test("卡片預設收起，標題留在 summary 上", () => {
  const markup = runtimeMarkup();

  // Collapsed on first paint, exactly like the Shared API sections. `open` would
  // defeat the point of the change.
  assert.doesNotMatch(markup, /<details class="runtime-resource-details" open/);

  // The title moved into the summary rather than being duplicated or dropped: a
  // folded card still has to say which card it is.
  const summaries = [...markup.matchAll(/<summary class="compact-settings-section-summary runtime-resource-summary">([\s\S]*?)<\/summary>/g)];
  assert.equal(summaries.length, 5);
  for (const [, inner] of summaries) {
    assert.match(inner, /class="settings-info-title"/);
  }
});

test("收起狀態下表單仍在 DOM 裡，存檔處理器讀的 id 不會消失", () => {
  const markup = runtimeMarkup();

  // Native disclosure hides the body visually but keeps it parented, which is why
  // no re-binding is needed on expand. These are the ids the save handlers read.
  for (const id of [
    "runtimeBackgroundWorkerCount",
    "runtimeBackgroundPerAgentLimit",
    "runtimeAllowNestedSubagents",
    "runtimeMaxSubagentDepth",
    "runtimeBudgetMode",
    "saveBackgroundTaskSettingsBtn",
    "saveExecutionBudgetBtn",
    "saveRetryPolicyBtn",
    "addRetryPolicyPatternBtn",
    "retryPolicyPatternInput",
  ]) {
    assert.match(markup, new RegExp(`id="${id}"`), `${id} 不在收起的卡片內容裡`);
  }
  assert.match(markup, /data-retry-policy-list/);
  assert.match(markup, /insufficient balance/);
});

test("收縮機制沿用共享 API 那幾張卡的樣式做法", async () => {
  const styles = (await readStylesSource(stylesURL)).replace(/\r\n/g, "\n");

  assert.match(styles, /#settingsContentBody \.runtime-page \.usage-detail-grid\.runtime-resource-stack\s*\{[\s\S]*?grid-template-columns:\s*1fr/);
  // The card's spacing used to live on the <section>, which now has a single
  // child. Without the grid moving onto <details>, the summary and the body would
  // sit flush against each other.
  assert.match(styles, /#settingsContentBody \.runtime-resource-details\s*\{[\s\S]*?display:\s*grid;[\s\S]*?gap:\s*var\(--settings-space-3\)/);
  assert.match(styles, /#settingsContentBody \.runtime-resource-body\s*\{[\s\S]*?display:\s*grid;[\s\S]*?gap:\s*var\(--settings-space-3\)/);
  assert.match(styles, /#settingsContentBody \.retry-policy-add-row\s*\{[\s\S]*?grid-template-columns:\s*minmax\(0, 1fr\) auto/);

  // Same three things the Shared API details do to a summary: drop the native
  // marker, make it look clickable, and let the copy pass clicks through.
  assert.match(styles, /#settingsContentBody \.runtime-resource-details > summary \{[^}]*list-style:\s*none[^}]*cursor:\s*pointer/);
  assert.match(styles, /#settingsContentBody \.runtime-resource-details > summary::-webkit-details-marker \{[^}]*display:\s*none/);
  assert.match(styles, /#settingsContentBody \.runtime-resource-details > summary \.compact-settings-section-copy \{[^}]*pointer-events:\s*none/);

  // The chevron is the shared one, which is written rotated for the open state,
  // so the folded state has to point it back.
  assert.match(styles, /#settingsContentBody \.runtime-resource-details:not\(\[open\]\) \.compact-settings-section-summary::after \{[^}]*transform:\s*rotate\(0deg\)/);
  // The shared rule this depends on: if it stops existing there is no chevron to
  // rotate, and the cards would give no sign they can be opened.
  assert.match(styles, /#settingsContentBody \.compact-settings-section-summary::after \{[^}]*content:\s*"›"/);
});

test("伺服器系統頁不受影響，仍是不可收縮的卡片", () => {
  const markup = createSystemSettingsController({
    state: {
      // The same state key: both pages read state.runtimeSummary and only differ
      // in which parts of it they present.
      runtimeSummary: {
        server: { host: "localhost", port: 16888, configPath: "/tmp/config.json" },
        process: { executable: "autoto", pid: 42 },
        go: { version: "go1.21" },
        paths: [{ label: "config", path: "/tmp/config.json" }],
      },
    },
  }).renderServerSystemSettingsContent();

  // Same card and grid classes, different page. The request covered 執行資源 only,
  // so this one must keep rendering its cards open.
  assert.match(markup, /class="settings-info-card settings-card settings-card-content"/);
  assert.doesNotMatch(markup, /runtime-resource-details/);
});
