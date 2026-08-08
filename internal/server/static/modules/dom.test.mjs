import test from "node:test";
import assert from "node:assert/strict";
import { escapeAttr, escapeHtml } from "./dom.mjs";

// Four modules used to carry private copies of these functions with three
// different character sets. They now all import these, so the escaped set is
// pinned here: widening or narrowing it silently is what would turn a formatting
// tweak into an XSS hole, or into unreadable output.
test("escapeHtml neutralizes tag and entity syntax in text content", () => {
  assert.equal(escapeHtml('<script>alert("x")</script>'), "&lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;");
  assert.equal(escapeHtml("a & b"), "a &amp; b");
  // Ampersand first, so an escape is never double-escaped into a literal entity.
  assert.equal(escapeHtml("&lt;"), "&amp;lt;");
  assert.equal(escapeHtml(null), "");
  assert.equal(escapeHtml(undefined), "");
  assert.equal(escapeHtml(0), "0");
});

// Text content is full of quotes and backticks: shell commands, prose, markdown
// inline code. Escaping them there makes output unreadable without making it
// safer, and the markdown renderer depends on backticks surviving.
test("escapeHtml leaves single quotes and backticks alone in text content", () => {
  assert.equal(escapeHtml("ant auth login --profile 'work'"), "ant auth login --profile 'work'");
  assert.equal(escapeHtml("use `a ** b` verbatim"), "use `a ** b` verbatim");
});

// An attribute value is a different context: a quote there can close the
// attribute and open another one.
test("escapeAttr additionally neutralizes both quote styles and the backtick", () => {
  assert.equal(escapeAttr("it's"), "it&#39;s");
  assert.equal(escapeAttr("a`b"), "a&#96;b");
  assert.equal(escapeAttr(`" onload=x`), "&quot; onload=x");
  // Everything escapeHtml covers still has to be covered.
  assert.equal(escapeAttr("<&>"), "&lt;&amp;&gt;");
});

// The dangerous characters for breaking out of an attribute must all be gone,
// whichever delimiter the markup happens to use.
test("escapeAttr output cannot terminate an attribute value", () => {
  const hostile = `x" onerror="alert(1)" '` + "`";
  const escaped = escapeAttr(hostile);
  for (const ch of ['"', "'", "`", "<", ">"]) {
    assert.ok(!escaped.includes(ch), `escapeAttr left ${ch} unescaped: ${escaped}`);
  }
});
