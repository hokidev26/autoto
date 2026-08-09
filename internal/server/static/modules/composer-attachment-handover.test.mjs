import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

// Sending a message with a file played out in stages. The text cleared, the attachment
// cards stayed in the composer for the whole upload, and the button changed on its own
// schedule -- one action reading as three. The cards lingered because they were only
// cleared after the POST returned, which for a file is exactly the slow part.
//
// Everything now empties in the same synchronous beat the button becomes "sending",
// which means a failed send has to put it all back: text, cards, and previews.
const composer = readFileSync(new URL("chat-composer.mjs", import.meta.url), "utf8");
const attachments = readFileSync(new URL("composer-attachments.mjs", import.meta.url), "utf8");

function sendMessageBody() {
  const at = composer.indexOf("async function sendMessage(event) {");
  assert.notEqual(at, -1);
  return composer.slice(at, composer.indexOf("\n  function updateDraftLimitHint(", at));
}

test("the composer empties before the model sync, not after it", () => {
  const body = sendMessageBody();
  const clearAt = body.indexOf('input.value = "";');
  const detachAt = body.indexOf("staged = attachments.length ? detachPendingAttachments() : [];");
  const syncAt = body.indexOf("await syncSelectedModelToAgent(agentId)");
  assert.ok(clearAt !== -1 && detachAt !== -1 && syncAt !== -1);
  // That sync awaits any pending settings save, so leaving it between the button and
  // the clear is what split one action into stages.
  assert.ok(clearAt < syncAt, "the text goes before the round trip");
  assert.ok(detachAt < syncAt, "and so do the attachment cards");
});

test("the attachments leave the composer in the same beat as the text", () => {
  const body = sendMessageBody();
  const clearAt = body.indexOf('input.value = "";');
  const detachAt = body.indexOf("staged = attachments.length ? detachPendingAttachments() : [];");
  const postAt = body.indexOf("await request(");
  assert.ok(detachAt > clearAt && detachAt < postAt, "taken with the text, before the upload starts");
  // The old shape cleared them after the POST resolved.
  assert.doesNotMatch(
    body.slice(postAt),
    /clearPendingAttachments\(\)/,
    "clearing after the upload is what left the cards on screen for its duration",
  );
});

test("a send takes the files without destroying them", () => {
  // clearPendingAttachments revokes the preview object URLs, which a failed send needs
  // in order to put the cards back with their thumbnails.
  const at = attachments.indexOf("function detachPendingAttachments()");
  assert.notEqual(at, -1, "there is a handover that is not a clear");
  const fn = attachments.slice(at, attachments.indexOf("\n  function restorePendingAttachments(", at));
  assert.doesNotMatch(fn, /releasePendingAttachmentPreviews/, "the previews outlive the detach");
  assert.match(fn, /state\.pendingAttachments = \[\]/);
  assert.match(fn, /renderPendingAttachments\(\)/, "so the cards actually disappear");
  assert.match(fn, /return attachments/, "and the caller now owns them");
});

test("a delivered turn releases the previews it was holding", () => {
  const body = sendMessageBody();
  const acceptedAt = body.indexOf("await onMessageAccepted?.(accepted, agentId)");
  assert.notEqual(acceptedAt, -1);
  assert.match(
    body.slice(acceptedAt),
    /if \(staged\.length\) releasePendingAttachmentPreviews\(staged\)/,
    "the other half of the handover: freed once the files will not be handed back",
  );
});

test("a failed send puts the text and the files back together", () => {
  const body = sendMessageBody();
  const at = body.indexOf("const restoreComposerAfterFailedSend = () =>");
  assert.notEqual(at, -1, "one restore, shared by every path that can fail");
  const fn = body.slice(at, body.indexOf("};", at));
  assert.match(fn, /discardPendingUserMessage/, "the echo goes, since nothing was delivered");
  assert.match(fn, /restorePendingAttachments\(staged\)/, "the cards come back");
  assert.match(fn, /input\.value = text/, "and so does the text");
  // Both failure routes use it: the model sync refusing, and the POST throwing.
  const refusalAt = body.indexOf("if (!(await syncSelectedModelToAgent(agentId)))");
  assert.match(
    body.slice(refusalAt, refusalAt + 400),
    /restoreComposerAfterFailedSend\(\)/,
    "a refused model must not swallow the message",
  );
  assert.match(body.slice(body.indexOf("} catch (err) {")), /restoreComposerAfterFailedSend\(\)/);
});

test("moving to another conversation frees the previews instead of leaking them", () => {
  // The cards cannot go back into a composer the reader has left, so the object URLs
  // would otherwise stay allocated for the rest of the session with nothing to show.
  const body = sendMessageBody();
  const refusalAt = body.indexOf("if (!(await syncSelectedModelToAgent(agentId)))");
  assert.match(
    body.slice(refusalAt, refusalAt + 400),
    /else if \(staged\.length\) releasePendingAttachmentPreviews\(staged\)/,
  );
  const catchAt = body.indexOf("} catch (err) {");
  assert.match(body.slice(catchAt), /if \(staged\.length\) releasePendingAttachmentPreviews\(staged\)/);
});

test("restoring keeps the files ahead of anything staged since", () => {
  const at = attachments.indexOf("function restorePendingAttachments(attachments)");
  assert.notEqual(at, -1);
  const fn = attachments.slice(at, attachments.indexOf("\n  function renderPendingAttachments(", at));
  assert.match(fn, /\.\.\.restored\.filter\(\(item\) => !known\.has\(item\?\.id\)\), \.\.\.current/);
  assert.match(fn, /known\.has/, "and a file staged twice is not duplicated");
});

test("the staged list is declared where both outcomes can settle it", () => {
  const body = sendMessageBody();
  const declAt = body.indexOf("let staged = [];");
  const tryAt = body.indexOf("try {", declAt);
  assert.ok(declAt !== -1 && declAt < tryAt, "declared before the try, or the catch cannot see it");
});
