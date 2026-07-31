// Incremental Markdown for streaming answers.
//
// A streamed answer used to be re-parsed in full on every chunk: chunk n paid
// for parsing n characters, so a long answer cost O(n^2) before it finished.
// This splits the accumulated text at a boundary past which no later chunk can
// change the meaning of what came before, renders the settled prefix once, and
// re-renders only the volatile tail.
//
// Two boundary kinds are safe, both mirroring how the renderer itself reads
// text:
//   - the end of a closed ``` fence, because a fence's content is opaque; and
//   - a blank line outside any fence, because a blank line closes every open
//     list, quote and table, leaving no block state to carry across the split.
// A split inside an unclosed fence is never safe: the closing ``` has not
// arrived, so the text after it is still undecided.

const FENCE_PATTERN = /```([^\n`]*)\n([\s\S]*?)```/g;
const BLANK_LINE_PATTERN = /\n[ \t]*\n/g;

// Index just past the last complete fence, plus where an unclosed fence opener
// starts after it (-1 when there is none). Mirrors FENCE_PATTERN so "complete"
// means the same thing here as it does in the renderer.
function scanFences(text, from) {
  FENCE_PATTERN.lastIndex = from;
  let lastFenceEnd = from;
  let match;
  while ((match = FENCE_PATTERN.exec(text)) !== null) lastFenceEnd = FENCE_PATTERN.lastIndex;
  return { lastFenceEnd, openFenceAt: text.indexOf("```", lastFenceEnd) };
}

// Splits an unclosed fence into its language and the code received so far.
// The opening line is only a fence once its newline arrives; until then the
// language is still being typed and there is no code to show.
function parseOpenFence(text, openFenceAt) {
  const rest = text.slice(openFenceAt + 3);
  const newline = rest.indexOf("\n");
  if (newline === -1) return null;
  return { lang: rest.slice(0, newline).trim() || "text", code: rest.slice(newline + 1) };
}

// Last blank-line split point within [from, text.length), or -1. The boundary
// sits after the blank line so the settled side ends on it.
function lastBlankLineBoundary(text, from) {
  BLANK_LINE_PATTERN.lastIndex = from;
  let boundary = -1;
  let match;
  while ((match = BLANK_LINE_PATTERN.exec(text)) !== null) {
    boundary = match.index + match[0].length;
    BLANK_LINE_PATTERN.lastIndex = boundary;
  }
  return boundary;
}

// Furthest index that is safe to treat as settled, plus the unclosed fence (if
// any) that begins there. Never returns below `from`, so a banked boundary
// cannot regress.
//
// An opening ``` is itself a block boundary: whatever precedes it is settled
// even though the fence has not closed. Banking up to the opener is what stops
// a long code block from dragging the entire answer before it through a reparse
// on every chunk.
function safeBoundary(text, from) {
  const { lastFenceEnd, openFenceAt } = scanFences(text, from);
  if (openFenceAt !== -1) return { boundary: openFenceAt, openFenceAt };
  const blank = lastBlankLineBoundary(text, lastFenceEnd);
  return { boundary: blank > lastFenceEnd ? blank : lastFenceEnd, openFenceAt: -1 };
}

// `renderOpenFence` is optional. Without it an unclosed fence falls back to the
// plain renderer, which reads the half-written block as ordinary markdown until
// the closing ``` lands. Supplying it shows the code as code while it streams.
export function createStreamingMarkdown({ renderMarkdown, renderOpenFence = null }) {
  if (typeof renderMarkdown !== "function") throw new TypeError("createStreamingMarkdown requires a renderMarkdown function");

  let sourceText = "";
  let boundary = 0;
  let stableHTML = "";

  function reset() {
    sourceText = "";
    boundary = 0;
    stableHTML = "";
  }

  // Streaming only ever appends. Anything else (a retry, an edit, a switch to a
  // different run) invalidates the banked prefix, so recompute from scratch
  // rather than trusting a cache that describes different text.
  function isAppendOf(previous, next) {
    return next.length >= previous.length && next.startsWith(previous);
  }

  function update(fullText) {
    const text = String(fullText ?? "");
    const recomputed = !isAppendOf(sourceText, text);
    if (recomputed) {
      boundary = 0;
      stableHTML = "";
    }
    sourceText = text;

    // Rescanning from the banked boundary rather than from 0 is what keeps the
    // whole stream linear: each character is scanned a bounded number of times.
    const { boundary: nextBoundary, openFenceAt } = safeBoundary(text, boundary);
    // `stableDeltaHTML` is the only part of the settled half that is new, so a
    // caller holding settled DOM can append it instead of rewriting the whole
    // prefix. `reset` and replacements report the full prefix as the delta,
    // since in those cases nothing on screen can be reused.
    let stableDeltaHTML = "";
    if (nextBoundary > boundary) {
      stableDeltaHTML = renderMarkdown(text.slice(boundary, nextBoundary));
      stableHTML += stableDeltaHTML;
      boundary = nextBoundary;
    }
    if (recomputed) stableDeltaHTML = stableHTML;

    const tail = text.slice(boundary);
    const result = { stableHTML, stableDeltaHTML, stableGrew: stableDeltaHTML !== "", recomputed, tailHTML: "" };
    if (!tail) return result;

    // The tail is the unclosed fence itself once the boundary has advanced to
    // its opener, so it is the one case the plain renderer cannot read yet.
    if (openFenceAt !== -1 && openFenceAt === boundary && renderOpenFence) {
      const open = parseOpenFence(text, openFenceAt);
      if (open) {
        result.tailHTML = renderOpenFence(open);
        return result;
      }
    }

    result.tailHTML = renderMarkdown(tail);
    return result;
  }

  return { update, reset };
}
