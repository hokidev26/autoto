import assert from "node:assert/strict";
import test from "node:test";

import { createStreamingMarkdown } from "./markdown-stream.mjs";

// A stand-in for the real renderer that keeps the two properties the splitter
// depends on: a fence is opaque, and a blank line closes every open block.
// Marking each call lets a test see how much text was reparsed.
let renderCalls = [];
function renderMarkdown(text) {
  renderCalls.push(text);
  const out = [];
  const pattern = /```([^\n`]*)\n([\s\S]*?)```/g;
  let last = 0;
  let match;
  while ((match = pattern.exec(text)) !== null) {
    if (match.index > last) out.push(renderText(text.slice(last, match.index)));
    out.push(`<pre data-lang="${(match[1] || "text").trim() || "text"}">${match[2]}</pre>`);
    last = pattern.lastIndex;
  }
  if (last < text.length) out.push(renderText(text.slice(last)));
  return out.join("");
}

function renderText(text) {
  const html = [];
  let list = null;
  const closeList = () => {
    if (list) html.push(`<ul>${list.join("")}</ul>`);
    list = null;
  };
  for (const raw of String(text).split("\n")) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim()) {
      closeList();
      continue;
    }
    const item = line.match(/^\s*[-*+]\s+(.+)$/);
    if (item) {
      list = list || [];
      list.push(`<li>${item[1]}</li>`);
      continue;
    }
    closeList();
    html.push(`<p>${line}</p>`);
  }
  closeList();
  return html.join("");
}

function streamed(text, { chunkSize = 1, renderOpenFence = null } = {}) {
  const renderer = createStreamingMarkdown({ renderMarkdown, renderOpenFence });
  let acc = "";
  let last = { stableHTML: "", tailHTML: "" };
  for (let i = 0; i < text.length; i += chunkSize) {
    acc += text.slice(i, i + chunkSize);
    last = renderer.update(acc);
  }
  return { html: `${last.stableHTML}${last.tailHTML}`, renderer };
}

test.beforeEach(() => { renderCalls = []; });

// The whole optimisation rests on this: splitting the text must not change what
// the reader ends up seeing.
for (const [name, source] of [
  ["paragraphs", "first para\n\nsecond para\n\nthird para"],
  ["closed fence between prose", "before\n\n```js\nconst a = 1;\nconst b = 2;\n```\n\nafter"],
  ["consecutive fences", "```js\na\n```\n\n```py\nb\n```\n\ntail"],
  ["list then fence with no blank line", "- one\n- two\n```js\ncode\n```\n\nend"],
  ["fence containing blank lines", "intro\n\n```js\na\n\nb\n```\n\nout"],
  ["fence containing markdown syntax", "```md\n- not a list\n\n# not a heading\n```\n\nreal"],
  ["trailing blank lines", "para\n\n\n\n"],
  ["no blank lines at all", "one line only"],
]) {
  test(`streaming one character at a time matches a single full render: ${name}`, () => {
    assert.equal(streamed(source).html, renderMarkdown(source));
  });

  test(`streaming in uneven chunks matches a single full render: ${name}`, () => {
    for (const chunkSize of [2, 3, 7, 13]) {
      assert.equal(streamed(source, { chunkSize }).html, renderMarkdown(source), `chunkSize ${chunkSize}`);
    }
  });
}

test("an unclosed fence is never banked as settled, so closing it still renders a code block", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  renderer.update("intro\n\n```js\nconst a = 1;");
  // Mid-stream the half-written fence has no closing ```, so the renderer can
  // only read it as prose. What matters is that it is not banked.
  const closed = renderer.update("intro\n\n```js\nconst a = 1;\n```\n\ndone");
  const html = `${closed.stableHTML}${closed.tailHTML}`;
  assert.equal(html, renderMarkdown("intro\n\n```js\nconst a = 1;\n```\n\ndone"));
  assert.match(html, /<pre data-lang="js">const a = 1;\n<\/pre>/);
});

test("text before an unclosed fence is banked so a long code block stops reparsing it", () => {
  const prose = "para one\n\npara two\n\n";
  const renderer = createStreamingMarkdown({ renderMarkdown });
  let acc = `${prose}\`\`\`js\n`;
  renderer.update(acc);
  renderCalls = [];
  // Every further chunk of the open fence must reparse only the fence, never
  // the settled prose ahead of it.
  for (const line of ["a", "b", "c", "d"]) {
    acc += `${line}\n`;
    renderer.update(acc);
  }
  assert.ok(renderCalls.length > 0);
  for (const call of renderCalls) {
    assert.ok(!call.includes("para one"), `reparsed settled prose: ${JSON.stringify(call)}`);
  }
});

test("settled prefix is parsed once even as the tail keeps changing", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  renderer.update("settled para\n\n");
  renderCalls = [];
  renderer.update("settled para\n\ntail a");
  renderer.update("settled para\n\ntail ab");
  renderer.update("settled para\n\ntail abc");
  for (const call of renderCalls) {
    assert.ok(!call.includes("settled para"), `reparsed settled text: ${JSON.stringify(call)}`);
  }
});

test("stableGrew reports only the updates that moved the boundary", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  assert.equal(renderer.update("no boundary yet").stableGrew, false);
  assert.equal(renderer.update("no boundary yet\n\n").stableGrew, true);
  assert.equal(renderer.update("no boundary yet\n\nmore").stableGrew, false);
});

test("renderOpenFence shows a streaming code block as code before it closes", () => {
  const renderOpenFence = ({ lang, code }) => `<pre data-open data-lang="${lang}">${code}</pre>`;
  const renderer = createStreamingMarkdown({ renderMarkdown, renderOpenFence });
  const mid = renderer.update("intro\n\n```js\nconst a = 1;\n");
  assert.match(mid.tailHTML, /<pre data-open data-lang="js">const a = 1;\n<\/pre>/);
  // The opening line alone is not yet a fence: the language is still arriving.
  const renderer2 = createStreamingMarkdown({ renderMarkdown, renderOpenFence });
  assert.doesNotMatch(renderer2.update("intro\n\n```js").tailHTML, /data-open/);
});

test("open fence handling still converges on the plain render once closed", () => {
  const renderOpenFence = ({ lang, code }) => `<pre data-open data-lang="${lang}">${code}</pre>`;
  const source = "before\n\n```js\nconst a = 1;\nconst b = 2;\n```\n\nafter";
  assert.equal(streamed(source, { renderOpenFence }).html, renderMarkdown(source));
  assert.equal(streamed(source, { chunkSize: 5, renderOpenFence }).html, renderMarkdown(source));
});

test("reset clears banked state so a reused renderer does not leak the old answer", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  renderer.update("first answer\n\nbanked");
  renderer.reset();
  const next = renderer.update("second answer\n\nfresh");
  const html = `${next.stableHTML}${next.tailHTML}`;
  assert.equal(html, renderMarkdown("second answer\n\nfresh"));
  assert.ok(!html.includes("first answer"));
});

// A retry or an edit replaces the text instead of extending it. The banked
// prefix describes text that no longer exists, so it has to be discarded.
test("text that is not an append of the previous text recomputes from scratch", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  renderer.update("original para\n\nmore text");
  const replaced = renderer.update("different para\n\nother text");
  const html = `${replaced.stableHTML}${replaced.tailHTML}`;
  assert.equal(html, renderMarkdown("different para\n\nother text"));
  assert.ok(!html.includes("original para"));
});

test("shrinking text is treated as a replacement rather than trusted as a prefix", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  renderer.update("long answer\n\nwith a tail that goes on");
  const shorter = renderer.update("long answer\n\nshort");
  assert.equal(`${shorter.stableHTML}${shorter.tailHTML}`, renderMarkdown("long answer\n\nshort"));
});

test("empty and blank-only input render nothing without throwing", () => {
  const renderer = createStreamingMarkdown({ renderMarkdown });
  assert.equal(renderer.update("").tailHTML, "");
  assert.equal(renderer.update("").stableHTML, "");
  const blank = createStreamingMarkdown({ renderMarkdown }).update("\n\n");
  assert.equal(`${blank.stableHTML}${blank.tailHTML}`, renderMarkdown("\n\n"));
});

test("a missing renderMarkdown is rejected at construction", () => {
  assert.throws(() => createStreamingMarkdown({}), TypeError);
});
