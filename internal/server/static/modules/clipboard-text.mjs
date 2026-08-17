import { escapeHtml } from "./dom.mjs";

// Word, Google Docs, and similar editors prefer the HTML clipboard. A native
// copy of rendered markdown is a stack of <p> tags, and those paste as
// paragraphs with the destination's "space after" — which looks like a blank
// line between every visual row. Emit a single block that uses <br> so a
// copied "12 / 12" stays two lines of normal spacing.
export function clipboardHTML(text) {
  const source = String(text || "").replace(/\r\n/g, "\n").replace(/\r/g, "\n").replace(/\n+$/g, "");
  const body = escapeHtml(source).replace(/\n/g, "<br>");
  return `<html><body><!--StartFragment--><div style="margin:0;padding:0;line-height:normal;mso-line-height-rule:exactly;">${body}</div><!--EndFragment--></body></html>`;
}

export async function copyTextAsNormalLines(text, {
  clipboard = globalThis.navigator?.clipboard,
  ClipboardItemImpl = globalThis.ClipboardItem,
} = {}) {
  const value = String(text || "");
  if (!value) return false;
  const html = clipboardHTML(value);
  try {
    if (clipboard?.write && ClipboardItemImpl) {
      await clipboard.write([
        new ClipboardItemImpl({
          "text/plain": new Blob([value], { type: "text/plain" }),
          "text/html": new Blob([html], { type: "text/html" }),
        }),
      ]);
      return true;
    }
  } catch {}
  try {
    if (clipboard?.writeText) {
      await clipboard.writeText(value);
      return true;
    }
  } catch {}
  return false;
}
