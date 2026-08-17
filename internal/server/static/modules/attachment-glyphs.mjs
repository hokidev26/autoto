// Shared file-type glyphs for composer chips, the correction editor, and sent
// attachment cards. The previous composer chips used a single "▯" box for every
// kind, so PDF / Word / text were indistinguishable. These are the same 24x24
// stroke idiom as the transcript tool icons, tinted by `data-kind` in CSS.

const textNamePattern = /\.(txt|md|markdown|json|jsonl|csv|tsv|log|xml|ya?ml|toml|ini|env|go|js|jsx|ts|tsx|css|html?|py|rb|rs|java|c|h|cpp|hpp|cs|php|sh|zsh|bash|sql|swift|kt|kts|dart|vue|svelte)$/i;

function svg(paths) {
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${paths}</svg>`;
}

const fileSheet = `<path d="M7 3.5h6.5L18 8v12.5H7A1.5 1.5 0 0 1 5.5 19V5A1.5 1.5 0 0 1 7 3.5Z"></path><path d="M13.5 3.5V8H18"></path>`;

const attachmentGlyphPaths = Object.freeze({
  image: `<rect x="4" y="6" width="16" height="12" rx="2"></rect><circle cx="9" cy="9.5" r="1.1"></circle><path d="m8 14.5 2.6-2.6 2.4 2.4L16 12l4 4"></path>`,
  video: `<rect x="3.5" y="6" width="17" height="12" rx="2"></rect><path d="m10 9.5 6 2.5-6 2.5Z"></path>`,
  pdf: `${fileSheet}<path d="M8.5 12.5h7M8.5 15.5h4.5"></path>`,
  docx: `${fileSheet}<path d="M8.5 12h7M8.5 15.5h5"></path>`,
  text: `${fileSheet}<path d="M8.5 11.5h7M8.5 14.5h7M8.5 17.5h4"></path>`,
  binary: fileSheet,
});

export function classifyAttachmentKind(file) {
  const type = String(file?.type || "").toLowerCase();
  const name = String(file?.name || "").toLowerCase();
  if (type.startsWith("image/")) return "image";
  if (type === "video/mp4" || type === "video/webm") return "video";
  if (type === "application/pdf" || name.endsWith(".pdf")) return "pdf";
  if (
    name.endsWith(".docx")
    || name.endsWith(".doc")
    || type === "application/msword"
    || type.includes("wordprocessingml")
  ) return "docx";
  if (type.startsWith("text/") || textNamePattern.test(name)) return "text";
  return "binary";
}

export function attachmentGlyph(kind) {
  const key = kind === "file" ? "binary" : kind;
  return svg(attachmentGlyphPaths[key] || attachmentGlyphPaths.binary);
}

export function attachmentPaperclipGlyph() {
  return svg(`<path d="m8.5 12.5 6.8-6.8a3 3 0 0 1 4.2 4.2l-8.6 8.6a5 5 0 0 1-7.1-7.1l8-8"></path><path d="m6.7 13.2 7.6-7.6"></path>`);
}
