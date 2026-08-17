import assert from "node:assert/strict";
import test from "node:test";

import { clipboardHTML, copyTextAsNormalLines } from "./clipboard-text.mjs";

test("clipboardHTML turns line breaks into br tags instead of paragraphs", () => {
  const html = clipboardHTML("12\n12");
  assert.match(html, /12<br>12/);
  assert.doesNotMatch(html, /<p[\s>]/i);
  assert.match(html, /line-height:normal/);
  assert.match(html, /mso-line-height-rule:exactly/);
});

test("clipboardHTML escapes markup in copied agent text", () => {
  const html = clipboardHTML("<script>alert(1)</script>\nnext");
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;<br>next/);
});

test("copyTextAsNormalLines writes plain text and HTML together", async () => {
  const written = [];
  class FakeClipboardItem {
    constructor(data) {
      this.data = data;
    }
  }
  const clipboard = {
    write: async (items) => {
      written.push(items);
    },
  };
  assert.equal(await copyTextAsNormalLines("12\n12", { clipboard, ClipboardItemImpl: FakeClipboardItem }), true);
  assert.equal(written.length, 1);
  const payload = written[0][0].data;
  assert.equal(await payload["text/plain"].text(), "12\n12");
  assert.equal(await payload["text/html"].text(), clipboardHTML("12\n12"));
});

test("copyTextAsNormalLines falls back to writeText when ClipboardItem is missing", async () => {
  const calls = [];
  const clipboard = {
    writeText: async (value) => {
      calls.push(value);
    },
  };
  assert.equal(await copyTextAsNormalLines("12\n12", { clipboard, ClipboardItemImpl: undefined }), true);
  assert.deepEqual(calls, ["12\n12"]);
});
