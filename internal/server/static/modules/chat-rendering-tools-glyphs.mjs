import { t as cr } from "./messages-chat-rendering-extra.mjs";

// Line-art glyphs in the same idiom as the composer and workbench icons: a
// 24x24 box, no fill, 1.7px round-joined strokes inheriting currentColor. They
// replace the Unicode characters this used to emit (⌕ ◌ ▤ ± ✎ ›_ •), which
// rendered at whatever weight and baseline each platform's fallback font chose
// and read as punctuation rather than as icons.
const toolActivityGlyphPaths = Object.freeze({
  search: `<circle cx="10.5" cy="10.5" r="6.5"></circle><path d="m15.5 15.5 4.5 4.5"></path>`,
  files: `<path d="M8 3.5h5.5L18 8v9a1.5 1.5 0 0 1-1.5 1.5H8A1.5 1.5 0 0 1 6.5 17V5A1.5 1.5 0 0 1 8 3.5Z"></path><path d="M13 3.5V8h5"></path><path d="M4 7.5v13A1.5 1.5 0 0 0 5.5 22h9"></path>`,
  read: `<path d="M5.5 4.5h7L18.5 10v9.5a1.5 1.5 0 0 1-1.5 1.5H5.5A1.5 1.5 0 0 1 4 19.5v-13A1.5 1.5 0 0 1 5.5 4.5Z"></path><path d="M12 4.5V10h6"></path><path d="M7.5 13.5h7M7.5 17h4.5"></path>`,
  edit: `<path d="M12 20.5h8.5"></path><path d="M16.6 4.1a2.1 2.1 0 0 1 3 3L8.4 18.3 4 19.5l1.2-4.4Z"></path>`,
  write: `<path d="M5.5 3.5h7L18.5 9.5V13"></path><path d="M12 3.5V9.5h6"></path><path d="M4 6.5v13A1.5 1.5 0 0 0 5.5 21h5"></path><path d="M17.5 15.5v6M14.5 18.5h6"></path>`,
  command: `<rect x="3.5" y="4.5" width="17" height="15" rx="2.5"></rect><path d="m8 10 2.5 2.5L8 15"></path><path d="M13 15h3.5"></path>`,
  web: `<circle cx="12" cy="12" r="8.5"></circle><path d="M3.5 12h17"></path><path d="M12 3.5c2.4 2.3 3.6 5.2 3.6 8.5S14.4 18.2 12 20.5c-2.4-2.3-3.6-5.2-3.6-8.5S9.6 5.8 12 3.5Z"></path>`,
  task: `<circle cx="6.5" cy="6.5" r="2.5"></circle><circle cx="17.5" cy="6.5" r="2.5"></circle><circle cx="12" cy="18" r="2.5"></circle><path d="M6.5 9v2a2 2 0 0 0 2 2h7a2 2 0 0 0 2-2V9"></path><path d="M12 13v2.5"></path>`,
  todo: `<path d="M4 6.5 6 8.5 9.5 5"></path><path d="M4 17 6 19l3.5-3.5"></path><path d="M13 7h7M13 17.5h7"></path>`,
  thinking: `<path d="M12 3.5a5.5 5.5 0 0 0-3.4 9.8V16a1.5 1.5 0 0 0 1.5 1.5h3.8A1.5 1.5 0 0 0 15.4 16v-2.7A5.5 5.5 0 0 0 12 3.5Z"></path><path d="M10 20.5h4"></path><path d="M12 9v4"></path>`,
  // Deliberately the plainest mark in the set: it is the fallback for tools we
  // do not recognise (MCP servers, plugins), so it upgrades the old "•" bullet
  // rather than implying a capability the tool may not have.
  generic: `<circle cx="12" cy="12" r="8.5"></circle><circle cx="12" cy="12" r="2.3"></circle>`,
  // Subagent status markers. These used to be "↗", "✓" and "!" injected by a
  // ::before in workspace-tasks.css, which cannot share a box with an <svg>.
  dispatch: `<path d="M8 16 16 8"></path><path d="M9.5 8H16v6.5"></path>`,
  done: `<path d="m5.5 12.5 4.5 4.5 8.5-9.5"></path>`,
  alert: `<path d="M12 7v6.5"></path><circle cx="12" cy="17" r="1.1"></circle>`,
});

export function toolActivityGlyph(kind) {
  const paths = toolActivityGlyphPaths[kind] || toolActivityGlyphPaths.generic;
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${paths}</svg>`;
}

// Clipboard copy: a back page plus a front page. The old ⧉ glyph sat on
// whatever fallback font the OS picked and read as two misaligned squares.
export function messageCopyGlyph() {
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="9" y="9" width="10.5" height="12" rx="2"></rect><path d="M15 9V6.75A1.75 1.75 0 0 0 13.25 5H6.75A1.75 1.75 0 0 0 5 6.75v10.5A1.75 1.75 0 0 0 6.75 19H9"></path></svg>`;
}

// The kind doubles as a CSS hook (.tool-activity-icon-<kind>), so the palette in
// workspace-tasks.css can tint reads, writes and commands apart at a glance.
export function toolActivityIconKind(toolName) {
  const name = String(toolName || "").toLowerCase();
  if (name.includes("grep") || name.includes("search")) return name.includes("web") ? "web" : "search";
  if (name.includes("glob")) return "files";
  if (name.includes("fetch") || name.includes("browser") || name.includes("navigate") || name.includes("http")) return "web";
  if (name.includes("todo")) return "todo";
  if (name.includes("task") || name.includes("agent")) return "task";
  if (name.includes("notebook") || name.includes("edit")) return "edit";
  if (name.includes("write") || name.includes("create")) return "write";
  if (name.includes("read") || name.includes("view") || name.includes("cat")) return "read";
  if (name.includes("bash") || name.includes("shell") || name.includes("terminal") || name.includes("powershell") || name.includes("exec")) return "command";
  return "generic";
}

// Maps a raw tool name to a human-readable display title. Keeps the original
// as fallback so unknown tools still surface their actual name.
export function friendlyToolName(toolName) {
  const name = String(toolName || "").toLowerCase().trim();
  if (name === "ls" || name === "listdirectory" || name === "list_directory") return cr("toolNames.listDirectory");
  if (name === "pwd") return cr("toolNames.currentDirectory");
  if (name === "cat") return cr("toolNames.readFile");
  if (name === "mkdir") return cr("toolNames.createDirectory");
  if (name === "cp") return cr("toolNames.copyFile");
  if (name === "mv") return cr("toolNames.moveFile");
  if (name === "rm") return cr("toolNames.deleteFile");
  if (name === "touch") return cr("toolNames.createFile");
  if (name === "find") return cr("toolNames.findFiles");
  if (name === "which") return cr("toolNames.findCommand");
  if (name === "echo") return cr("toolNames.echoText");
  if (name === "curl" || name === "wget") return cr("toolNames.networkRequest");
  return toolName;
}

export function toolActivityIconHTML(toolName, extraClass = "") {
  const kind = toolActivityIconKind(toolName);
  const classes = `${extraClass} tool-activity-icon-${kind}`.trim();
  return { kind, classes, svg: toolActivityGlyph(kind) };
}
