import { escapeAttr, escapeHtml } from "./dom.mjs";
import { t as cr } from "./messages-chat-rendering-extra.mjs";

// Shared by the finished-fence path and the streaming open-fence path so a
// code block does not change shape at the moment its closing ``` arrives.
export function codeBlockHTML(code, lang) {
  return `<div class="code-block"><div class="code-head"><span>${escapeHtml(lang)}</span><button class="copy-code" type="button" data-code="${escapeAttr(code)}">${escapeHtml(cr("code.copy"))}</button></div><pre><code>${highlightCode(code, lang)}</code></pre></div>`;
}

export function renderMarkdown(text) {
  const blocks = [];
  const pattern = /```([^\n`]*)\n([\s\S]*?)```/g;
  let lastIndex = 0;
  let match;
  const source = String(text || "");
  while ((match = pattern.exec(source)) !== null) {
    if (match.index > lastIndex) blocks.push(renderMarkdownText(source.slice(lastIndex, match.index)));
    blocks.push(codeBlockHTML(match[2] || "", (match[1] || "text").trim() || "text"));
    lastIndex = pattern.lastIndex;
  }
  if (lastIndex < source.length) blocks.push(renderMarkdownText(source.slice(lastIndex)));
  return blocks.join("");
}

// Blank lines are kept as block separators rather than filtered away: they are
// the only thing that tells one list from the next, or a heading from the
// paragraph that follows it.
export function renderMarkdownText(text) {
  const lines = String(text || "").replace(/\r\n?/g, "\n").split("\n");
  const html = [];
  // A stack of open lists, deepest last, so indented bullets nest instead of
  // flattening into one long column.
  let lists = [];
  let quote = [];
  // Table accumulator: rows collected until a non-table line closes the block.
  let tableRows = [];

  const closeLists = (toDepth = 0) => {
    while (lists.length > toDepth) {
      const closed = lists.pop();
      const markup = `<${closed.tag}>${closed.items.join("")}</${closed.tag}>`;
      if (lists.length) lists[lists.length - 1].items.push(markup);
      else html.push(markup);
    }
  };
  const closeQuote = () => {
    if (!quote.length) return;
    html.push(`<blockquote>${quote.map((line) => `<p>${renderInlineMarkdown(line)}</p>`).join("")}</blockquote>`);
    quote = [];
  };
  // Flush a collected table: first row is <thead>, rest are <tbody>.
  const closeTable = () => {
    if (!tableRows.length) return;
    const [headerRow, , ...bodyRows] = tableRows; // row[1] is the separator
    const thCells = (headerRow || []).map((cell) => `<th>${renderInlineMarkdown(cell)}</th>`).join("");
    const thead = `<thead><tr>${thCells}</tr></thead>`;
    const tbody = bodyRows.length
      ? `<tbody>${bodyRows.map((row) => `<tr>${row.map((cell) => `<td>${renderInlineMarkdown(cell)}</td>`).join("")}</tr>`).join("")}</tbody>`
      : "";
    html.push(`<div class="md-table-wrap"><table class="md-table">${thead}${tbody}</table></div>`);
    tableRows = [];
  };
  // Split a pipe-delimited row into trimmed cells, ignoring leading/trailing |.
  const parseTableRow = (line) => line.replace(/^\s*\|/, "").replace(/\|\s*$/, "").split("|").map((cell) => cell.trim());
  const isSeparatorRow = (row) => row.every((cell) => /^:?-+:?$/.test(cell));
  const pushItem = (markup) => {
    if (lists.length) lists[lists.length - 1].items.push(markup);
    else html.push(markup);
  };

  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim()) {
      closeLists();
      closeQuote();
      closeTable();
      continue;
    }

    const heading = line.match(/^(#{1,6})\s+(.+)$/);
    if (heading) {
      closeLists();
      closeQuote();
      closeTable();
      const level = heading[1].length;
      html.push(`<h${level}>${renderInlineMarkdown(heading[2].replace(/\s+#+\s*$/, ""))}</h${level}>`);
      continue;
    }

    if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
      closeLists();
      closeQuote();
      closeTable();
      html.push("<hr>");
      continue;
    }

    const quoted = line.match(/^\s{0,3}>\s?(.*)$/);
    if (quoted) {
      closeLists();
      closeTable();
      quote.push(quoted[1]);
      continue;
    }
    closeQuote();

    // Table rows: a line with at least one | is a candidate. Accumulate header +
    // separator + body rows together, then flush when the block ends.
    if (line.includes("|")) {
      const cells = parseTableRow(line);
      if (cells.length >= 1) {
        closeLists();
        if (tableRows.length === 1 && isSeparatorRow(cells)) {
          // Separator row: store it but don't display as a real row.
          tableRows.push(cells);
        } else {
          tableRows.push(cells);
        }
        continue;
      }
    } else if (tableRows.length) {
      // Non-pipe line while a table is open closes it.
      closeTable();
    }

    const bullet = line.match(/^(\s*)[-*+]\s+(.+)$/);
    const ordered = line.match(/^(\s*)\d{1,9}[.)]\s+(.+)$/);
    const item = bullet || ordered;
    if (item) {
      closeTable();
      const tag = bullet ? "ul" : "ol";
      // Two spaces is the shallowest indent editors and models agree on, so it
      // is what decides a nesting level here.
      const depth = Math.floor(item[1].replace(/\t/g, "  ").length / 2) + 1;
      while (lists.length > depth) closeLists(lists.length - 1);
      while (lists.length < depth) lists.push({ tag, items: [] });
      const current = lists[lists.length - 1];
      if (current.tag !== tag) {
        closeLists(lists.length - 1);
        lists.push({ tag, items: [] });
      }
      lists[lists.length - 1].items.push(`<li>${renderInlineMarkdown(item[2])}</li>`);
      continue;
    }

    // A plain line while a list is open is that item's continuation, not a new
    // paragraph beside the list.
    if (lists.length) {
      pushItem(`<p>${renderInlineMarkdown(line.trim())}</p>`);
      continue;
    }
    html.push(`<p>${renderInlineMarkdown(line)}</p>`);
  }
  closeLists();
  closeQuote();
  closeTable();
  return html.join("");
}

// Only http(s) and mailto survive. The link text arrives already escaped, but
// a scheme like javascript: contains nothing escapeHtml touches, so the scheme
// has to be checked explicitly or a link becomes a script.
export function safeMarkdownLinkHref(url) {
  const value = String(url || "").trim().replace(/&amp;/g, "&");
  if (!/^(?:https?:\/\/|mailto:)/i.test(value)) return "";
  if (/[\s<>"'`\\]/.test(value)) return "";
  return escapeAttr(value);
}

export function renderInlineMarkdown(text) {
  const held = [];
  // Code spans are lifted out before any emphasis runs so `**` inside a code
  // span stays literal, which is exactly why someone wrapped it in backticks.
  let out = escapeHtml(text).replace(/`([^`]+)`/g, (_, code) => {
    held.push(`<code class="inline-code">${code}</code>`);
    return `${held.length - 1}`;
  });
  out = out
    .replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, (whole, label, url) => {
      const href = safeMarkdownLinkHref(url);
      return href ? `<a href="${href}" target="_blank" rel="noopener noreferrer">${label}</a>` : whole;
    })
    .replace(/\*\*\*(?=\S)([\s\S]*?\S)\*\*\*/g, "<strong><em>$1</em></strong>")
    .replace(/\*\*(?=\S)([\s\S]*?\S)\*\*/g, "<strong>$1</strong>")
    .replace(/__(?=\S)([\s\S]*?\S)__/g, "<strong>$1</strong>")
    .replace(/~~(?=\S)([\s\S]*?\S)~~/g, "<del>$1</del>")
    // Single-character emphasis is matched last and only when the delimiter
    // hugs the text, so multiplication and snake_case survive intact.
    .replace(/(^|[^*\w])\*(?=\S)([^*\n]*?\S)\*(?![*\w])/g, "$1<em>$2</em>")
    .replace(/(^|[^_\w])_(?=\S)([^_\n]*?\S)_(?![_\w])/g, "$1<em>$2</em>");
  return out.replace(/(\d+)/g, (_, index) => held[Number(index)] ?? "");
}

export function highlightCode(code, lang) {
  const tokens = [];
  const hold = (html) => {
    const key = `\uE000TOK${tokens.length}\uE001`;
    tokens.push(html);
    return key;
  };
  let html = escapeHtml(code);
  html = html.replace(/("[^"\n]*"|'[^'\n]*')/g, (value) => hold(`<span class="tok-string">${value}</span>`));
  html = html.replace(/(\/\/.*|#.*)$/gm, (value) => hold(`<span class="tok-comment">${value}</span>`));
  const keywordSet = "const|let|var|function|return|if|else|for|while|switch|case|break|class|type|struct|func|package|import|from|export|async|await|try|catch|defer|go|select|range";
  html = html.replace(new RegExp(`\\b(${keywordSet})\\b`, "g"), '<span class="tok-keyword">$1</span>');
  return html.replace(/\uE000TOK(\d+)\uE001/g, (_, index) => tokens[Number(index)] || "");
}
