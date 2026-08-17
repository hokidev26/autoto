import test from "node:test";
import assert from "node:assert/strict";

import {
  attachmentGlyph,
  attachmentPaperclipGlyph,
  classifyAttachmentKind,
} from "./attachment-glyphs.mjs";

test("attachment classification treats Word .doc/.docx and PDF as distinct kinds", () => {
  assert.equal(classifyAttachmentKind({ name: "shot.png", type: "image/png" }), "image");
  assert.equal(classifyAttachmentKind({ name: "clip.mp4", type: "video/mp4" }), "video");
  assert.equal(classifyAttachmentKind({ name: "spec.pdf", type: "application/pdf" }), "pdf");
  assert.equal(classifyAttachmentKind({ name: "brief.docx", type: "" }), "docx");
  assert.equal(classifyAttachmentKind({ name: "letter.doc", type: "application/msword" }), "docx");
  assert.equal(classifyAttachmentKind({ name: "notes.txt", type: "text/plain" }), "text");
  assert.equal(classifyAttachmentKind({ name: "archive.bin", type: "application/octet-stream" }), "binary");
});

test("attachment glyphs are inline SVG keyed by kind, not a generic box", () => {
  assert.match(attachmentGlyph("pdf"), /<svg viewBox="0 0 24 24"/);
  assert.match(attachmentGlyph("docx"), /<svg viewBox="0 0 24 24"/);
  assert.notEqual(attachmentGlyph("pdf"), attachmentGlyph("docx"));
  assert.equal(attachmentGlyph("file"), attachmentGlyph("binary"));
  assert.match(attachmentPaperclipGlyph(), /<svg viewBox="0 0 24 24"/);
  assert.doesNotMatch(attachmentGlyph("pdf"), /▯/);
});
