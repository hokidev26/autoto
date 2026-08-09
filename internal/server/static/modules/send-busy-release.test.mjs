import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// Sending held the composer busy through two round trips: the POST, and the full
// transcript reload after it. The reload is not part of the send -- the echo is
// already on screen and the WebSocket refresh reconciles it -- but the busy flag
// disables the textarea, so on a phone over a remote tunnel you could not start
// typing the next message for a second or more, and the button still read "sending"
// for a turn the server had already accepted.
const composer = readFileSync(new URL("chat-composer.mjs", import.meta.url), "utf8");

function functionBody(source, name) {
  const start = source.indexOf(`async function ${name}(`);
  assert.notEqual(start, -1, `expected ${name} to still exist`);
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
  throw new Error(`unbalanced braces reading ${name}`);
}

const sendMessage = functionBody(composer, "sendMessage");

test("the composer is released once the server has the turn, not after the reload", () => {
  const acceptedAt = sendMessage.indexOf("await onMessageAccepted?.(accepted, agentId)");
  const releaseAt = sendMessage.indexOf("setMessageSendingFor(agentId, false);");
  const reloadAt = sendMessage.indexOf("await loadMessages(agentId)");
  assert.ok(acceptedAt !== -1 && releaseAt !== -1 && reloadAt !== -1);
  assert.ok(acceptedAt < releaseAt, "the POST has to be accepted first");
  assert.ok(
    releaseAt < reloadAt,
    "the release must come before the transcript reload, which is what made the wait long",
  );
});

test("the reload still runs, and still cannot report itself as a failed send", () => {
  // The message is delivered by this point. A failed reload must not restore the
  // draft or pull the echo, which would invite sending the same thing twice.
  const reloadAt = sendMessage.indexOf("await loadMessages(agentId)");
  const tail = sendMessage.slice(reloadAt);
  assert.match(tail, /catch \(reloadError\)/, "the reload keeps its own catch");
  assert.match(tail, /notifyTerminal/, "and reports to the terminal rather than throwing");
  assert.match(
    sendMessage,
    /scheduleMessageRefresh\(1200, agentId, \{ skipWhileActive: true \}\)/,
    "the later refresh stays as the backstop that reconciles the echo",
  );
});

test("the error path still releases the composer", () => {
  // The early release is on the success path only, so the finally block is still
  // the one thing standing between a failed POST and a permanently dead composer.
  const finallyAt = sendMessage.lastIndexOf("} finally {");
  assert.notEqual(finallyAt, -1);
  assert.match(
    sendMessage.slice(finallyAt),
    /setMessageSendingFor\(agentId, false\);/,
    "the finally release must stay",
  );
});

test("a double send is prevented by the run being active, not by the busy flag", () => {
  // This is what makes the early release safe: onMessageAccepted marks the agent
  // running, so a second Enter parks the text instead of posting it again.
  assert.match(
    sendMessage,
    /const autoQueue = agentTurnInFlight\(\) && !isGoalCommandDraft\(goalCommand\)/,
    "the auto-queue path is what catches a second send",
  );
  const appMain = readFileSync(new URL("app-main.mjs", import.meta.url), "utf8");
  const acceptedStart = appMain.indexOf("onMessageAccepted: async (result, agentId) => {");
  assert.notEqual(acceptedStart, -1);
  const acceptedBody = appMain.slice(acceptedStart, acceptedStart + 600);
  assert.match(
    acceptedBody,
    /state\.agent = \{ \.\.\.state\.agent, status: "running" \}/,
    "and it is true because the accept handler marks the run active",
  );
});

test("settling the transcript does not fight a user who is already typing", () => {
  const finallyBlock = sendMessage.slice(sendMessage.lastIndexOf("} finally {"));
  assert.match(
    finallyBlock,
    /if \(!input\?\.value\?\.trim\?\.\(\)\) scrollMessagesToBottom/,
    "the composer is usable during the reload now, so an unconditional scroll would "
      + "yank the transcript from someone mid-sentence",
  );
  assert.match(finallyBlock, /input\.focus\?\.\(\{ preventScroll: true \}\)/, "the caret is still restored");
});
