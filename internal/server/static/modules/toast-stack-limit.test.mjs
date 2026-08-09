import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// The toast stack had no ceiling. A batch of background tasks finishing together
// produced a card each, and on a phone -- where the stack sits above the composer and
// grows upward -- that column covered the conversation it was reporting on.
//
// These are source assertions rather than DOM ones: showToast reads the live shell
// through $() and the notification preference helpers, so standing a whole app-main
// up here would test the harness more than the behaviour. The trimming rules are
// small and worth pinning exactly.
const appMain = readFileSync(new URL("app-main.mjs", import.meta.url), "utf8");

function functionBody(name) {
  const start = appMain.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `expected app-main.mjs to still define ${name}`);
  // The body brace is the one after the parameter list closes. Taking the first brace
  // instead lands inside a default value like `options = {}` and reads the wrong span.
  let parenDepth = 0;
  let cursor = appMain.indexOf("(", start);
  for (; cursor < appMain.length; cursor += 1) {
    if (appMain[cursor] === "(") parenDepth += 1;
    else if (appMain[cursor] === ")") {
      parenDepth -= 1;
      if (parenDepth === 0) break;
    }
  }
  const open = appMain.indexOf("{", cursor);
  let depth = 0;
  for (let index = open; index < appMain.length; index += 1) {
    if (appMain[index] === "{") depth += 1;
    else if (appMain[index] === "}") {
      depth -= 1;
      if (depth === 0) return appMain.slice(open + 1, index);
    }
  }
  throw new Error(`unbalanced braces reading ${name}`);
}

test("the toast stack has a ceiling and showToast applies it", () => {
  assert.match(appMain, /const maxVisibleToasts = \d+;/, "the ceiling is a named constant");
  const limit = Number(/const maxVisibleToasts = (\d+);/.exec(appMain)?.[1]);
  assert.ok(limit >= 1 && limit <= 5, `a ceiling of ${limit} is not a plausible number of simultaneous toasts`);
  // Trimming has to happen on the way in. Doing it after the append would let the
  // stack reach limit + 1 for a frame, which is visible as a jump on a phone.
  const body = functionBody("showToast");
  const trimAt = body.indexOf("trimToastStack(stack)");
  const appendAt = body.indexOf("stack.appendChild(node)");
  assert.ok(trimAt !== -1, "showToast must trim the stack");
  assert.ok(appendAt !== -1, "showToast must still append the new toast");
  assert.ok(trimAt < appendAt, "the stack is trimmed before the new toast is appended");
});

test("trimming never discards a toast waiting to be dismissed", () => {
  // Persistent toasts are errors held until the reader closes them. Dropping one to
  // make room would lose a failure nobody ever saw, which is worse than a tall stack.
  const body = functionBody("trimToastStack");
  assert.match(body, /:not\(\.toast-persistent\)/, "persistent toasts are excluded from trimming");
  assert.match(body, /:not\(\.leaving\)/, "and so are toasts already animating out, which would double-count");
});

test("a finished background task is named by something a person can read", () => {
  // The fallback used to be the task id: a line of hex that names nothing and wrapped
  // onto a second line. The conversation it belongs to answers the actual question.
  const body = functionBody("backgroundTaskNoticeLabel");
  assert.match(body, /raw\.title \|\| data\.title/, "an explicit task title still wins");
  assert.match(body, /conversationTitleForNotice\(notice\)/, "then the conversation it belongs to");
  assert.match(body, /t\("backgroundTasks\.task"\)/, "and a generic word as the last resort");
  assert.doesNotMatch(body, /taskId/, "the raw id must not be a label any more");

  // And the caller has to go through it rather than keeping its own fallback chain.
  const notice = functionBody("executionNoticeMessage");
  assert.match(notice, /backgroundTaskNoticeLabel\(notice, raw, data\)/);
  assert.doesNotMatch(
    notice.slice(0, notice.indexOf("continuation_blocked")),
    /notice\.taskId/,
    "the task_terminal branch must not fall back to the id again",
  );
});
