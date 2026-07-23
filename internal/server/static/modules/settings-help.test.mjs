import test from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

import {
  collectSettingsHelpEntries,
  createSettingsHelpController,
  normalizeSettingsHelpEntries,
  normalizeSettingsHelpText,
  renderSettingsHelpContent,
  settingsHelpTitleForNode,
} from "./settings-help.mjs";

function classList(initial = []) {
  const values = new Set(initial);
  return {
    add: (...names) => names.forEach((name) => values.add(name)),
    contains: (name) => values.has(name),
    remove: (...names) => names.forEach((name) => values.delete(name)),
  };
}

function fakeElement({ textContent = "", classes = [] } = {}) {
  const attributes = new Map();
  const listeners = new Map();
  return {
    attributes,
    listeners,
    textContent,
    innerHTML: "",
    classList: classList(classes),
    isConnected: true,
    addEventListener(name, handler) { listeners.set(name, handler); },
    focus() { this.focused = true; },
    getAttribute(name) { return attributes.get(name) || ""; },
    setAttribute(name, value) { attributes.set(name, String(value)); },
  };
}

test("normalizes and deduplicates help entries without losing order", () => {
  assert.equal(normalizeSettingsHelpText("  多行\n  说明  "), "多行 说明");
  assert.deepEqual(normalizeSettingsHelpEntries([
    { title: "选项 A", text: "  第一条说明 " },
    { title: "选项 A", text: "第一条说明" },
    { title: "说明本身", text: "说明本身" },
    { title: "选项 B", text: "第二条说明" },
  ]), [
    { title: "选项 A", text: "第一条说明" },
    { title: "", text: "说明本身" },
    { title: "选项 B", text: "第二条说明" },
  ]);
});

test("infers a help title from the closest preceding label", () => {
  const heading = {
    tagName: "H3",
    textContent: "网络策略",
    contains: () => false,
    matches: () => false,
    previousElementSibling: null,
  };
  const node = {
    textContent: "控制联网搜索的默认行为。",
    previousElementSibling: heading,
    parentElement: null,
    getAttribute: () => "",
    closest: () => null,
  };

  assert.equal(settingsHelpTitleForNode(node), "网络策略");
});

test("collects annotated copy and respects explicit titles", () => {
  const nodes = [
    {
      textContent: "说明一",
      previousElementSibling: null,
      parentElement: null,
      getAttribute: (name) => name === "data-settings-help-title" ? "字段一" : "",
      closest: () => null,
    },
    {
      textContent: "说明一",
      previousElementSibling: null,
      parentElement: null,
      getAttribute: (name) => name === "data-settings-help-title" ? "字段一" : "",
      closest: () => null,
    },
  ];
  const root = { querySelectorAll: () => nodes };

  assert.deepEqual(collectSettingsHelpEntries(root), [{ title: "字段一", text: "说明一" }]);
});

test("renders escaped overview, detailed entries, and an empty state", () => {
  const markup = renderSettingsHelpContent({
    overview: "<总览>",
    entries: [{ title: "字段 & A", text: "使用 <安全> 值" }],
    labels: { overview: "页面概览", empty: "暂无说明" },
  });
  assert.match(markup, /页面概览/);
  assert.match(markup, /&lt;总览&gt;/);
  assert.match(markup, /字段 &amp; A/);
  assert.match(markup, /使用 &lt;安全&gt; 值/);

  assert.match(renderSettingsHelpContent({ labels: { empty: "暂无说明" } }), /暂无说明/);
});

test("controller opens, renders, closes on Escape, and restores focus", async () => {
  const trigger = fakeElement();
  const panel = fakeElement({ classes: ["hidden"] });
  const title = fakeElement();
  const body = fakeElement();
  const closeButton = fakeElement();
  const backdrop = fakeElement({ classes: ["hidden"] });
  const copy = {
    textContent: "详细说明",
    previousElementSibling: null,
    parentElement: null,
    getAttribute: (name) => name === "data-settings-help-title" ? "选项" : "",
    closest: () => null,
  };
  const root = { querySelectorAll: () => [copy] };
  const previousDocument = globalThis.document;
  globalThis.document = { activeElement: trigger };
  try {
    const controller = createSettingsHelpController({
      getRoot: () => root,
      trigger,
      panel,
      title,
      body,
      closeButton,
      backdrop,
      translate: (key) => ({
        "settings.dialogTitle": "设置",
        "settings.pageHelp.overview": "页面概览",
        "settings.pageHelp.empty": "暂无说明",
      })[key] || key,
    });
    controller.bind();
    controller.sync({ key: "network-search", label: "网络搜索", overview: "本页概览" });
    trigger.listeners.get("click")();
    await Promise.resolve();

    assert.equal(controller.isOpen(), true);
    assert.equal(title.textContent, "网络搜索");
    assert.match(body.innerHTML, /本页概览/);
    assert.match(body.innerHTML, /详细说明/);
    assert.equal(trigger.attributes.get("aria-expanded"), "true");
    assert.equal(panel.focused, true);

    let prevented = false;
    let stopped = false;
    assert.equal(controller.handleKeydown({
      key: "Escape",
      preventDefault() { prevented = true; },
      stopPropagation() { stopped = true; },
    }), true);
    assert.equal(prevented, true);
    assert.equal(stopped, true);
    assert.equal(controller.isOpen(), false);
    assert.equal(trigger.attributes.get("aria-expanded"), "false");
    assert.equal(trigger.focused, true);
  } finally {
    globalThis.document = previousDocument;
  }
});

test("registered settings renderers expose help-copy markers without hiding critical notices", async () => {
  // model-provider-settings.mjs is a re-export facade; help markers live in the
  // split domain modules (provider-console / model-routing / anthropic accounts).
  const rendererFiles = [
    "app-main.mjs",
    "automation-control.mjs",
    "local-preferences-settings.mjs",
    "memory-settings.mjs",
    "model-provider-components.mjs",
    "provider-console.mjs",
    "provider-codex-auth.mjs",
    "provider-anthropic-accounts.mjs",
    "model-routing-settings.mjs",
    "plugin-registry-ui.mjs",
    "remote-access-settings.mjs",
    "shared-api-settings.mjs",
    "skills-workbench.mjs",
    "system-settings.mjs",
    "terminal.mjs",
    "usage-history.mjs",
  ];
  const sources = Object.fromEntries(await Promise.all(rendererFiles.map(async (file) => [file, await readFile(new URL(file, import.meta.url), "utf8")])));

  rendererFiles.forEach((file) => assert.match(sources[file], /data-settings-help-copy/, `${file} should expose page help copy`));
  assert.match(sources["remote-access-settings.mjs"], /remote-access-current-password[\s\S]*?<small>\$\{escapeHtml\(rt\("currentPasswordHint"\)\)\}<\/small>/);
  assert.doesNotMatch(sources["remote-access-settings.mjs"], /settings-inline-alert[^>]*data-settings-help-copy/);
  assert.match(sources["provider-anthropic-accounts.mjs"], /anthropic-secret-note[^>]*>\$\{escapeHtml\(mt\("anthropic\.apiKeySafety"\)\)\}/);
  assert.doesNotMatch(sources["provider-anthropic-accounts.mjs"], /anthropic-secret-note[^>]*data-settings-help-copy/);
});
