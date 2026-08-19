import assert from "node:assert/strict";
import test from "node:test";
import { readStylesGroup } from "./styles-source-helper.mjs";

import {
  maxUtilityPanelWidth,
  minUtilityPanelWidth,
  normalizeUtilityPanelWidth,
  utilityPanelChatMinWidth,
  utilityPanelMaxAvailable,
} from "./ui-shell.mjs";

// The composer's unreadably narrow tier is a 480px container (icon rail, truncated
// bubbles). Dragging the right-hand panel used to be allowed all the way there;
// the floor now stops above that tier so the middle column stays readable.
const composerNarrowTier = 480;

test("拖曳右側面板不會把中間欄壓進看不到內容的層級", () => {
  // A wide desktop with the sidebar at a typical width.
  const viewportWidth = 1568;
  const railWidth = 76;
  const sidebarWidth = 276;

  const available = utilityPanelMaxAvailable({ viewportWidth, railWidth, sidebarWidth });
  const widest = normalizeUtilityPanelWidth(maxUtilityPanelWidth, undefined, { maxAvailable: available });
  const chatAtWidest = viewportWidth - railWidth - sidebarWidth - widest;

  assert.equal(chatAtWidest, utilityPanelChatMinWidth, "壓到底時剩下的寬度就是設定的底線");
  assert.ok(
    chatAtWidest > composerNarrowTier,
    `中間欄底線必須高於 ${composerNarrowTier}px 的手機樣式層級，實際 ${chatAtWidest}px`,
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
  assert.ok(utilityPanelChatMinWidth > composerNarrowTier, "底線不能低到讓對話無法閱讀");
});

// The docked layout (the middle stop when collapsing the navigation area) is the
// same drag with a different DOM: the conversation list is a child of the rail,
// so the rail's measured width already contains it and the shell's second grid
// column is 0px. Subtracting the list on top of the rail removed about a
// sidebar's worth of travel.
test("二階段（docked）版面也守住中間欄底線", () => {
  const viewportWidth = 1568;
  // The rail carries the stored sidebar width in this layout...
  const railWidth = 296;
  // ...and the list sits inside it, so it measures the rail minus the rail's padding.
  const sidebarWidth = 280;

  const available = utilityPanelMaxAvailable({ viewportWidth, railWidth, sidebarWidth, sidebarInsideRail: true });
  const widest = normalizeUtilityPanelWidth(maxUtilityPanelWidth, undefined, { maxAvailable: available });
  // No sidebar column to subtract: the viewport is the rail, the chat, and the panel.
  const chatAtWidest = viewportWidth - railWidth - widest;

  assert.equal(chatAtWidest, utilityPanelChatMinWidth, "壓到底時剩下的寬度就是設定的底線");
  assert.ok(
    chatAtWidest > composerNarrowTier,
    `docked 版面的中間欄底線必須高於 ${composerNarrowTier}px，實際 ${chatAtWidest}px`,
  );

  // The two-column layout still has a real second column, so there the list must
  // keep being subtracted. Asserted alongside so the fix cannot be "stop
  // subtracting the sidebar" everywhere.
  const twoColumn = utilityPanelMaxAvailable({ viewportWidth, railWidth: 68, sidebarWidth: 296 });
  assert.equal(twoColumn, viewportWidth - 68 - 296 - utilityPanelChatMinWidth);
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
  const styles = await readStylesGroup("workbench.css", import.meta.url);
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
  const styles = await readStylesGroup("workbench.css", import.meta.url);
  const ceilings = [...styles.matchAll(/clamp\(\d+px, calc\(50vw - 186px\), (\d+)px\)/g)].map((match) => Number(match[1]));
  assert.ok(ceilings.length > 0, "找不到面板寬度的 CSS 上限");
  for (const ceiling of new Set(ceilings)) {
    assert.equal(ceiling, maxUtilityPanelWidth, `CSS 上限 ${ceiling}px 必須等於 maxUtilityPanelWidth`);
  }
});
