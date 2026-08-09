import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The green dot means "the agent said something you have not read". It is cleared by
// renderProjects, which advances the seen mark for the conversation currently open.
//
// The hamburger used to reach that render indirectly: every tap called
// switchPrimaryWorkbench, which re-ran the whole layout including the nav render.
// That call is now skipped when the view is already the conversation list, so the
// drawer has to advance the mark itself. Without it, this sequence leaves a green row
// for the conversation you are staring at: read a conversation with the drawer shut,
// background the app, a reply lands while the navigation refresh is skipping (it
// skips while the page is hidden), come back, open the drawer.
const uiShell = readFileSync(new URL("ui-shell.mjs", import.meta.url), "utf8");
const appMain = readFileSync(new URL("app-main.mjs", import.meta.url), "utf8");

function functionBody(source, declaration) {
  const start = source.indexOf(declaration);
  assert.notEqual(start, -1, `expected to find ${declaration}`);
  // Walk past the parameter list before looking for the body brace. Taking the first
  // brace after the name lands inside a default value such as `seenMap = {}` and
  // reads an empty string as the body.
  let parenDepth = 0;
  let cursor = source.indexOf("(", start);
  for (; cursor < source.length; cursor += 1) {
    if (source[cursor] === "(") parenDepth += 1;
    else if (source[cursor] === ")") {
      parenDepth -= 1;
      if (parenDepth === 0) break;
    }
  }
  const open = source.indexOf("{", cursor);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    if (source[index] === "{") depth += 1;
    else if (source[index] === "}") {
      depth -= 1;
      if (depth === 0) return source.slice(open + 1, index);
    }
  }
  throw new Error(`unbalanced braces reading ${declaration}`);
}

test("opening the drawer renders the list, which is what clears the green dot", () => {
  const body = functionBody(uiShell, "function openMobileSidebar()");
  assert.match(
    body,
    /renderProjects\?\.\(\)/,
    "the drawer must render the conversation list when it opens",
  );
  // Ordering: the class has to be on the body first, because the render reads the
  // drawer state, and the metrics poll starts after there is something to poll for.
  const classAt = body.indexOf('classList.add("mobile-sidebar-open")');
  const renderAt = body.indexOf("renderProjects?.()");
  const metricsAt = body.indexOf("startDrawerMetrics");
  assert.ok(classAt < renderAt, "the drawer is marked open before the list renders");
  assert.ok(renderAt < metricsAt, "and the render happens before the metrics poll starts");
});

test("the seen mark is still advanced by the navigation render itself", () => {
  // If this moves out of renderProjects the drawer fix above becomes a no-op, so it
  // is pinned here rather than only in the seen-map unit tests.
  const renderBody = functionBody(appMain, "function renderProjects()");
  const markAt = renderBody.indexOf("markActiveConversationSeen()");
  const readAt = renderBody.indexOf("seenMap: readSeenMap()");
  assert.ok(markAt !== -1, "renderProjects advances the mark for the open conversation");
  assert.ok(readAt !== -1, "and reads the map back for the row colours");
  assert.ok(
    markAt < readAt,
    "the mark has to be written before the map is read, or the open conversation "
      + "renders one paint behind and stays green until the next render",
  );
});

test("a conversation that is still working is not treated as unread", () => {
  // Guard against the obvious over-correction: marking everything seen on drawer
  // open would make the dot useless. Running conversations have their own colour.
  const seen = readFileSync(new URL("conversation-seen.mjs", import.meta.url), "utf8");
  const unread = functionBody(seen, "export function conversationUnread(");
  assert.match(unread, /status === "running"/);
  assert.match(unread, /return activity > seen/, "unread is still a timestamp comparison");
});
