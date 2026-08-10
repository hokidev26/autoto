import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

import { setHTMLIfChanged } from "./dom.mjs";

// A stand-in for the one DOM behaviour this bug turns on: assigning innerHTML
// replaces the children, so listeners bound to the old nodes die with them,
// while skipping the assignment keeps both the nodes and their listeners.
function createNode(attributes = {}) {
  return {
    dataset: { ...attributes },
    listeners: [],
    addEventListener(type, handler) {
      this.listeners.push({ type, handler });
    },
    click() {
      for (const entry of this.listeners) if (entry.type === "click") entry.handler();
    },
  };
}

function createPanel(nodeSpecs) {
  const panel = {
    children: nodeSpecs.map(createNode),
    set innerHTML(_markup) {
      // A real write recreates the children, dropping every listener with them.
      this.children = nodeSpecs.map(createNode);
    },
    querySelectorAll(selector) {
      const guarded = selector.includes(":not([data-copy-bound])");
      return this.children.filter((node) => (guarded ? !node.dataset.copyBound : true));
    },
  };
  return panel;
}

// The panel re-renders on every background-task change while it is open, and
// setHTMLIfChanged deliberately skips identical markup. Binding without a guard
// stacked one listener per render on the surviving buttons, so a single click on
// the project path copied repeatedly and stacked a toast for each listener.
test("重複繪製後點一次複製只會觸發一次", () => {
  const copies = [];
  const panel = createPanel([{ copyDetail: "C:/work/project" }]);
  const markup = "<div>identical</div>";

  const render = () => {
    setHTMLIfChanged(panel, markup);
    panel.querySelectorAll("[data-copy-detail]:not([data-copy-bound])").forEach((node) => {
      node.dataset.copyBound = "1";
      node.addEventListener("click", () => copies.push(node.dataset.copyDetail));
    });
  };

  render();
  render();
  render();

  const [button] = panel.children;
  assert.equal(button.listeners.length, 1, "同一個按鈕不該累積多個監聽器");
  button.click();
  assert.deepEqual(copies, ["C:/work/project"], "一次點擊只該複製一次");
});

// The guard must not become a permanent block: when the markup really changes the
// container is rewritten, and the fresh buttons still need binding.
test("內容真的改變時新按鈕仍會綁定", () => {
  const copies = [];
  const panel = createPanel([{ copyDetail: "C:/work/project" }]);

  const render = (markup) => {
    setHTMLIfChanged(panel, markup);
    panel.querySelectorAll("[data-copy-detail]:not([data-copy-bound])").forEach((node) => {
      node.dataset.copyBound = "1";
      node.addEventListener("click", () => copies.push(node.dataset.copyDetail));
    });
  };

  render("<div>first</div>");
  render("<div>second</div>");

  const [button] = panel.children;
  assert.equal(button.listeners.length, 1, "重寫後的按鈕要剛好一個監聽器");
  button.click();
  assert.deepEqual(copies, ["C:/work/project"], "重寫後複製仍要可用");
});

// Pin the call site, so the guard cannot be dropped as a harmless simplification.
test("renderConversationDetails 的綁定帶有防重複標記", () => {
  const source = readFileSync(new URL("./app-main.mjs", import.meta.url), "utf8");
  assert.match(source, /\[data-copy-detail\]:not\(\[data-copy-bound\]\)/);
  assert.match(source, /\[data-details-runtime\]:not\(\[data-runtime-bound\]\)/);
});
