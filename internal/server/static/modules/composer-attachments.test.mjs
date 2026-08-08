import test from "node:test";
import assert from "node:assert/strict";

// dom.mjs reads the global `document` when `$` is called, so the stub has to be
// installed before the module under test is imported.
function element() {
  const classes = new Set();
  return {
    dataset: {},
    classList: {
      contains: (name) => classes.has(name),
      toggle(name, force) {
        const on = force === undefined ? !classes.has(name) : Boolean(force);
        if (on) classes.add(name);
        else classes.delete(name);
        return on;
      },
    },
  };
}

const elements = { messages: element(), composerInputShell: element() };
globalThis.document = { getElementById: (id) => elements[id] || null };

const { createComposerAttachments } = await import("./composer-attachments.mjs");

function harness() {
  const added = [];
  const attachments = createComposerAttachments({
    state: { pendingAttachments: [] },
    isAttachmentProcessing: () => false,
    attachmentJobIsCurrent: () => true,
    beginAttachmentProcessing: () => null,
    finishAttachmentProcessing: () => {},
    invalidateAttachmentProcessing: () => {},
    attachmentKind: () => "file",
    clipboardFiles: () => [],
    prepareVideoAttachment: async () => null,
    showToast: () => {},
    syncMessageComposerBusy: () => {},
  });
  return { attachments, added };
}

function fileDrag(files = []) {
  let prevented = false;
  return {
    dataTransfer: { types: ["Files"], files, dropEffect: "" },
    preventDefault() { prevented = true; },
    get prevented() { return prevented; },
  };
}

function textDrag() {
  let prevented = false;
  return {
    dataTransfer: { types: ["text/plain"], files: [] },
    preventDefault() { prevented = true; },
    get prevented() { return prevented; },
  };
}

test("dragging files over the transcript marks it as a drop target", () => {
  const { attachments } = harness();
  const host = elements.messages;
  attachments.setConversationDragging(false);

  const enter = fileDrag();
  attachments.handleConversationDragEnter(enter);
  assert.equal(enter.prevented, true, "the browser default has to be cancelled for a drop to fire");
  assert.equal(host.classList.contains("dropping-files"), true);
  // The hint is localized in JS and read back by CSS, so it must be populated.
  assert.ok(String(host.dataset.dropHint || "").length > 0);

  const over = fileDrag();
  attachments.handleConversationDragOver(over);
  assert.equal(over.dataTransfer.dropEffect, "copy", "a copy cursor, not the default move");
  assert.equal(host.classList.contains("dropping-files"), true);

  attachments.setConversationDragging(false);
});

// The transcript is full of nested cards and every boundary between them fires
// dragleave, so a single leave must not clear a drag that is still inside.
test("nested dragleave does not clear the drop target until the drag really left", () => {
  const { attachments } = harness();
  const host = elements.messages;
  attachments.setConversationDragging(false);

  attachments.handleConversationDragEnter(fileDrag()); // into the transcript
  attachments.handleConversationDragEnter(fileDrag()); // into a message card
  assert.equal(host.classList.contains("dropping-files"), true);

  attachments.handleConversationDragLeave(fileDrag()); // out of the card only
  assert.equal(host.classList.contains("dropping-files"), true, "still inside the transcript");

  attachments.handleConversationDragLeave(fileDrag()); // out of the transcript
  assert.equal(host.classList.contains("dropping-files"), false);
  assert.equal(host.dataset.dropHint, undefined, "the hint is cleared with the class");
});

test("dropping files on the transcript clears the highlight and consumes the event", () => {
  const { attachments } = harness();
  const host = elements.messages;
  attachments.handleConversationDragEnter(fileDrag());

  const drop = fileDrag([{ name: "note.txt", size: 12, type: "text/plain" }]);
  attachments.handleConversationDrop(drop);
  assert.equal(drop.prevented, true, "otherwise the browser opens the file and discards the workspace");
  assert.equal(host.classList.contains("dropping-files"), false);
});

// The sidebar drags projects and conversations as text/plain. Swallowing those
// would break reordering, so only file drags are intercepted.
test("non-file drags are left alone", () => {
  const { attachments } = harness();
  const host = elements.messages;
  attachments.setConversationDragging(false);

  const over = textDrag();
  attachments.handleConversationDragOver(over);
  assert.equal(over.prevented, false);
  assert.equal(host.classList.contains("dropping-files"), false);

  const stray = textDrag();
  attachments.swallowStrayFileDrop(stray);
  assert.equal(stray.prevented, false, "project reordering still has to reach its own handler");
});

// A file dropped just outside either zone would otherwise be opened by the
// browser, navigating away from unsent work.
test("a stray file drop is swallowed rather than opened by the browser", () => {
  const { attachments } = harness();
  const stray = fileDrag([{ name: "note.txt", size: 12, type: "text/plain" }]);
  attachments.swallowStrayFileDrop(stray);
  assert.equal(stray.prevented, true);
  assert.equal(elements.messages.classList.contains("dropping-files"), false);
});
