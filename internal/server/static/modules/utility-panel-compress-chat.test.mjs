import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  maxUtilityPanelWidth,
  minUtilityPanelWidth,
  normalizeUtilityPanelWidth,
  utilityPanelChatMinWidth,
  utilityPanelMaxAvailable,
} from "./ui-shell.mjs";

const stylesURL = new URL("../styles/workbench.css", import.meta.url);

// The composer's narrowest tier is a 480px container, and that tier is what makes
// the middle column look like the phone layout. Dragging the sidebar wider could
// reach it; dragging the utility panel wider could not, because the panel stopped
// at a flat 900px ceiling and held back a 420px chat floor. On a wide screen the
// ceiling was hit while the chat was still far wider than 480, so the compact
// layout looked reachable only from the left.
const composerNarrowTier = 480;

test("拖曳右側面板能把中間欄壓進手機樣式的層級", () => {
  // A wide desktop with the sidebar at a typical width.
  const viewportWidth = 1568;
  const railWidth = 76;
  const sidebarWidth = 276;

  const available = utilityPanelMaxAvailable({ viewportWidth, railWidth, sidebarWidth });
  const widest = normalizeUtilityPanelWidth(maxUtilityPanelWidth, undefined, { maxAvailable: available });
  const chatAtWidest = viewportWidth - railWidth - sidebarWidth - widest;

  // The container that drives the tiers is .composer-wrap, which is the chat
  // column minus its padding, so the column has to land clearly under the tier
  // rather than just touching it. The previous 420 floor left no margin at all.
  assert.ok(
    chatAtWidest <= composerNarrowTier - 100,
    `中間欄要能壓到 ${composerNarrowTier - 100}px 以下才有餘裕套用手機樣式，實際 ${chatAtWidest}px`,
  );
});

test("中間欄仍然保有底線，不會被壓到消失", () => {
  const viewportWidth = 1568;
  const railWidth = 76;
  const sidebarWidth = 276;
  const available = utilityPanelMaxAvailable({ viewportWidth, railWidth, sidebarWidth });
  const widest = normalizeUtilityPanelWidth(maxUtilityPanelWidth, undefined, { maxAvailable: available });
  const chatAtWidest = viewportWidth - railWidth - sidebarWidth - widest;

  assert.equal(chatAtWidest, utilityPanelChatMinWidth, "壓到底時剩下的寬度就是設定的底線");
  assert.ok(utilityPanelChatMinWidth >= 320, "底線不能低到讓對話無法閱讀");
});

test("窄螢幕上讓路給對話，面板不會溢出視窗", () => {
  const available = utilityPanelMaxAvailable({ viewportWidth: 1280, railWidth: 76, sidebarWidth: 296 });
  const widest = normalizeUtilityPanelWidth(maxUtilityPanelWidth, undefined, { maxAvailable: available });
  assert.equal(widest, available, "可用空間比上限小時，以可用空間為準");
  assert.ok(widest < maxUtilityPanelWidth);
  assert.ok(widest >= minUtilityPanelWidth);
});

// The JS clamp and the CSS grid have to agree on the floor, or whichever is higher
// silently becomes the real limit and the drag stops somewhere unexplained.
test("CSS 格線的底線與 JS 的底線一致", async () => {
  const styles = await readFile(stylesURL, "utf8");
  // Only the app-shell columns describe the chat column. Toolbars and card grids
  // use minmax() for their own reasons and must not be swept in.
  const floors = [...styles.matchAll(/grid-template-columns:\s*76px var\(--session-sidebar-width\) minmax\((\d+)px, 1fr\)/g)]
    .map((match) => Number(match[1]));
  assert.ok(floors.length > 0, "找不到中間欄的格線底線");
  for (const floor of new Set(floors)) {
    assert.equal(floor, utilityPanelChatMinWidth, `格線底線 ${floor}px 必須等於 utilityPanelChatMinWidth`);
  }
});

test("面板寬度的 CSS 上限與 JS 上限一致", async () => {
  const styles = await readFile(stylesURL, "utf8");
  const ceilings = [...styles.matchAll(/clamp\(\d+px, calc\(50vw - 186px\), (\d+)px\)/g)].map((match) => Number(match[1]));
  assert.ok(ceilings.length > 0, "找不到面板寬度的 CSS 上限");
  for (const ceiling of new Set(ceilings)) {
    assert.equal(ceiling, maxUtilityPanelWidth, `CSS 上限 ${ceiling}px 必須等於 maxUtilityPanelWidth`);
  }
});
