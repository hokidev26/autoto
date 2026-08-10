import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import { readStylesSource } from "./styles-source-helper.mjs";

const staticRoot = new URL("../", import.meta.url);
const stylesURL = new URL("styles.css", staticRoot);
const indexURL = new URL("index.html", staticRoot);

// The narrow composer rail is the layout the middle column reaches once it is
// squeezed, and it had drifted from the phone toolbar it is meant to read like:
// two indicators reporting one fact on the left, and the reasoning control
// showing a three-bar meter where the phone shows the level as a letter.
const railMarker = "/* Narrow composer icon rail: preserve every control at one fixed size. */";

async function readNarrowRail() {
  const styles = (await readStylesSource(stylesURL)).replace(/\r\n/g, "\n");
  const start = styles.indexOf(railMarker);
  assert.ok(start > -1, "找不到窄版 composer 圖示列的樣式區塊");
  const end = styles.indexOf("/* Flat, single-pass settings layout", start);
  assert.ok(end > start, "找不到窄版區塊的結尾");
  return { styles, rail: styles.slice(start, end) };
}

test("窄版工具列只留一個活動指示，連線圓圈整顆收掉", async () => {
  const { rail } = await readNarrowRail();

  assert.match(rail, /@container composer-shell \(max-width:\s*480px\)/);
  // The whole pill goes, not just its idle form: on desktop the running step is
  // handed to the task summary, so the pill held only the connection text and spun a
  // ring beside the word "connected" while the summary described the real work.
  assert.match(
    rail,
    /body\.white-shell\.theme-light \.composer-status \{\s*display:\s*none;\s*\}/,
    "窄版必須整顆隱藏連線狀態，而不是只隱藏閒置狀態",
  );

  // The earlier attempts: hiding only the idle form, and hiding it only while the
  // task summary happened to have a task. Both left an indicator behind.
  assert.doesNotMatch(rail, /\.composer-status:not\(\.is-busy\)\s*\{\s*display:\s*none;\s*\}/);
  assert.doesNotMatch(rail, /:has\(\.composer-task-summary\.has-task\) \.composer-status:not\(\.is-busy\)/);
  // Nothing in this tier may bring it back.
  assert.doesNotMatch(rail, /\.composer-status[^{}]*\{[^}]*display:\s*(?:inline-)?flex/);
});

test("留下來的是工作摘要那顆，它才是報告執行狀態的控制項", async () => {
  const { rail } = await readNarrowRail();

  // The summary is a real button that opens the task tray, and paintComposerStatus
  // routes the running step into it on desktop. Hiding the connection pill must not
  // also hide it, or the rail would report nothing at all.
  assert.doesNotMatch(rail, /\.composer-task-summary[^{}]*\{[^}]*display:\s*none/);
});

test("窄版的思考強度顯示成字母，跟手機瀏覽一致", async () => {
  const { styles, rail } = await readNarrowRail();

  // The bars go...
  assert.match(
    rail,
    /body\.white-shell\.theme-light \.composer-effort-field \.reasoning-effort-icon\s*\{\s*display:\s*none;\s*\}/,
    "窄版必須跟手機一樣不顯示三條長條圖示",
  );
  // ...and the letter comes back, which needs the shared visually-hidden rule
  // undone for this field only.
  assert.match(rail, /\.composer-effort-field \.composer-select-value\s*\{[^}]*position:\s*static[^}]*clip-path:\s*none/);
  assert.match(rail, /\.composer-effort-field \.composer-select-value::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);

  // The other controls keep hiding their text: this is a change to one field, not
  // the rail's icon-only rule.
  assert.match(rail, /body\.white-shell\.theme-light \.composer-select-value\s*\{[^}]*position:\s*absolute[^}]*clip-path:\s*inset\(50%\)/);

  // The phone layout this now matches, so the two cannot silently diverge again.
  const phoneMarker = "Phone composer toolbar";
  const phoneBlock = styles.slice(styles.indexOf(phoneMarker));
  assert.ok(phoneBlock.length > 0, "找不到手機版 composer 工具列區塊");
  assert.match(phoneBlock, /\.composer-effort-field \.composer-select-value::after\s*\{[^}]*content:\s*attr\(data-mobile-label\)/);
  assert.match(styles, /\[class~="composer-effort-field"\] \[class~="reasoning-effort-icon"\]\s*\{\s*display:\s*none;\s*\}/);
});

test("字母的來源仍然在標記裡，隱藏圖示不會讓等級無從得知", async () => {
  const html = await readFile(indexURL, "utf8");

  // data-mobile-label is what ::after renders, and the <select> stays the
  // accessible control, so hiding the meter costs nothing that was being read.
  assert.match(html, /id="reasoningEffortDisplay"[^>]*data-mobile-label="A"/);
  assert.match(html, /id="reasoningEffortIcon"[^>]*class="composer-select-icon reasoning-effort-icon"/);
});
