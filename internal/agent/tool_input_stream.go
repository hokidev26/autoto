package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// maxToolInputPreviewBytes bounds how much decoded argument text one tool call
// may stream to the UI. Past this the preview stops quietly; the full content
// still reaches the tool itself untouched.
const maxToolInputPreviewBytes = 256 << 10

// toolInputSnapshotInterval throttles snapshot-mode previews (Bash). Each
// snapshot re-redacts and re-sends the whole accumulated command, so unlike
// append-only deltas it must not fire per token.
const toolInputSnapshotInterval = 400 * time.Millisecond

// toolInputPreviewSpec describes how a tool's arguments stream to the UI.
//
// Exactly one of field or arrayField is set. field streams a top-level string
// value append-only. arrayField streams the itemField string of each element
// in a top-level array, separated by numbered markers. snapshot switches the
// wire format from append-only deltas to replace-the-whole-text events whose
// text has passed RedactToolActivityText -- required for Bash, whose command
// is deliberately kept out of every other event projection because secrets
// can only be redacted reliably from the complete accumulated text.
type toolInputPreviewSpec struct {
	field      string
	arrayField string
	itemField  string
	snapshot   bool
}

func toolInputPreviewSpecFor(toolName string) (toolInputPreviewSpec, bool) {
	switch toolName {
	case "Write":
		return toolInputPreviewSpec{field: "content"}, true
	case "Edit":
		return toolInputPreviewSpec{field: "new_string"}, true
	case "Agent":
		return toolInputPreviewSpec{field: "prompt"}, true
	case "MultiEdit":
		return toolInputPreviewSpec{arrayField: "edits", itemField: "new_string"}, true
	case "Bash":
		return toolInputPreviewSpec{field: "command", snapshot: true}, true
	default:
		return toolInputPreviewSpec{}, false
	}
}

// multiEditPreviewSeparator introduces every hunk after the first, numbered by
// the element's 1-based position among the array's objects. Finalize
// reconstructs the same text, so the format must stay deterministic.
func multiEditPreviewSeparator(ordinal int) string {
	return fmt.Sprintf("\n\n--- edit %d ---\n", ordinal)
}

// toolInputStreamPreview incrementally decodes the raw JSON fragments of a
// tool call's arguments as the model generates them, and extracts the target
// text so the UI can follow the write in real time. It is a single-pass
// tokenizer: every byte is looked at exactly once, and escape sequences
// (including \uXXXX surrogate pairs) may be split across fragments.
type toolInputStreamPreview struct {
	spec toolInputPreviewSpec

	// container / member state
	stack     []byte // open containers, '{' or '['
	expectKey bool   // inside an object, the next string is a key
	lastKey   string // most recent completed key at the top level
	valueKey  string // top-level key whose value is being read, "" otherwise

	// target-array state (MultiEdit)
	inTargetArray bool
	arrayDone     bool
	elemOrdinal   int    // 1-based position among the array's object elements
	lastElemKey   string // most recent completed key inside the current element
	valueElemKey  string // element key whose value is being read

	// string decoding state
	inString    bool
	isKey       bool
	escaped     bool
	inUnicode   bool
	unicodeHex  []byte
	pendingHigh rune // high surrogate waiting for its low half

	// capture targets
	capturing     bool // current string is target text
	captureDone   bool // top-level field already captured (field mode only)
	suppressed    bool // preview budget exhausted; keep parsing, stop emitting
	capturingPath bool
	pathDone      bool
	pathReady     bool

	// snapshot mode state
	lastSnapshot   string
	lastSnapshotAt time.Time

	keyBuf  strings.Builder
	pathBuf strings.Builder
	emitted strings.Builder  // decoded target text accumulated so far
	out     *strings.Builder // per-Feed sink for newly decoded target text
}

// newToolInputStreamPreview returns nil for tools that have no streamable
// argument, so callers can cache the miss.
func newToolInputStreamPreview(toolName string) *toolInputStreamPreview {
	spec, ok := toolInputPreviewSpecFor(toolName)
	if !ok {
		return nil
	}
	return &toolInputStreamPreview{spec: spec}
}

func (p *toolInputStreamPreview) Field() string {
	if p == nil {
		return ""
	}
	if p.spec.field != "" {
		return p.spec.field
	}
	return p.spec.itemField
}

func (p *toolInputStreamPreview) SnapshotMode() bool {
	return p != nil && p.spec.snapshot
}

// Feed consumes the next raw fragment of the argument JSON and returns the
// newly decoded target text, if any. Snapshot-mode callers ignore the return
// value and read SnapshotText instead.
func (p *toolInputStreamPreview) Feed(fragment string) string {
	if p == nil || fragment == "" {
		return ""
	}
	var out strings.Builder
	p.out = &out
	for i := 0; i < len(fragment); i++ {
		p.step(fragment[i])
	}
	p.out = nil
	return out.String()
}

// FilePath reports the decoded top-level file_path value once, as soon as its
// closing quote has been parsed, so the UI can label the card while the
// content is still streaming.
func (p *toolInputStreamPreview) FilePath() (string, bool) {
	if p == nil || !p.pathReady {
		return "", false
	}
	p.pathReady = false
	return p.pathBuf.String(), true
}

// SnapshotText returns the redacted accumulated text when it changed since the
// last snapshot and the throttle window elapsed (force skips the throttle).
// Redaction must always see the whole text: a secret split across two deltas
// would slip past any per-fragment pass, which is exactly why snapshot mode
// replaces instead of appending.
func (p *toolInputStreamPreview) SnapshotText(now time.Time, force bool) (string, bool) {
	if p == nil || !p.spec.snapshot || p.emitted.Len() == 0 {
		return "", false
	}
	if !force && !p.lastSnapshotAt.IsZero() && now.Sub(p.lastSnapshotAt) < toolInputSnapshotInterval {
		return "", false
	}
	text := RedactToolActivityText(p.emitted.String())
	if text == p.lastSnapshot {
		return "", false
	}
	p.lastSnapshot = text
	p.lastSnapshotAt = now
	return text, true
}

// Finalize reconciles the preview with the complete argument JSON. In delta
// mode it returns whatever suffix was not streamed yet, so providers without
// argument streaming still produce a preview and streamed previews always end
// complete. In snapshot mode it returns the final redacted text when it
// differs from the last snapshot sent.
func (p *toolInputStreamPreview) Finalize(input json.RawMessage) string {
	if p == nil {
		return ""
	}
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	if p.spec.snapshot {
		value, _ := args[p.spec.field].(string)
		if value == "" {
			return ""
		}
		text := RedactToolActivityText(value)
		if len(text) > maxToolInputPreviewBytes {
			text, _ = truncateHubString(text, maxToolInputPreviewBytes)
		}
		if text == p.lastSnapshot {
			return ""
		}
		p.lastSnapshot = text
		return text
	}
	if p.suppressed {
		return ""
	}
	expected := p.finalPreviewText(args)
	if expected == "" {
		return ""
	}
	sent := p.emitted.String()
	if !strings.HasPrefix(expected, sent) {
		return ""
	}
	rest := expected[len(sent):]
	if len(rest) > maxToolInputPreviewBytes {
		rest, _ = truncateHubString(rest, maxToolInputPreviewBytes)
	}
	p.emitted.WriteString(rest)
	return rest
}

// finalPreviewText renders the preview a complete argument set should have
// produced, using the exact format the streaming path emits.
func (p *toolInputStreamPreview) finalPreviewText(args map[string]any) string {
	if p.spec.arrayField == "" {
		value, _ := args[p.spec.field].(string)
		return value
	}
	items, _ := args[p.spec.arrayField].([]any)
	var expected strings.Builder
	ordinal := 0
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ordinal++
		value, isString := object[p.spec.itemField].(string)
		if !isString {
			continue
		}
		if ordinal >= 2 {
			expected.WriteString(multiEditPreviewSeparator(ordinal))
		}
		expected.WriteString(value)
	}
	return expected.String()
}

func (p *toolInputStreamPreview) step(b byte) {
	if p.inString {
		p.stepString(b)
		return
	}
	switch b {
	case ' ', '\t', '\n', '\r':
	case '{', '[':
		if b == '[' && len(p.stack) == 1 && p.valueKey == p.spec.arrayField && p.spec.arrayField != "" && !p.arrayDone {
			p.inTargetArray = true
		}
		if b == '{' && p.inTargetArray && len(p.stack) == 2 {
			p.elemOrdinal++
			p.lastElemKey = ""
			p.valueElemKey = ""
		}
		p.stack = append(p.stack, b)
		p.expectKey = b == '{'
		p.valueKey = ""
		p.valueElemKey = ""
	case '}', ']':
		if len(p.stack) > 0 {
			p.stack = p.stack[:len(p.stack)-1]
		}
		if b == ']' && p.inTargetArray && len(p.stack) == 1 {
			p.inTargetArray = false
			p.arrayDone = true
		}
		p.expectKey = false
		p.valueKey = ""
		p.valueElemKey = ""
	case '"':
		p.inString = true
		p.escaped = false
		if p.top() == '{' && p.expectKey {
			p.isKey = true
			p.keyBuf.Reset()
			return
		}
		p.isKey = false
		if len(p.stack) == 1 {
			if p.spec.field != "" && p.valueKey == p.spec.field && !p.captureDone {
				p.capturing = true
			} else if p.valueKey == "file_path" && !p.pathDone {
				p.capturingPath = true
			}
		} else if p.inTargetArray && len(p.stack) == 3 && p.valueElemKey == p.spec.itemField {
			p.capturing = true
			if p.elemOrdinal >= 2 {
				p.sinkBytes([]byte(multiEditPreviewSeparator(p.elemOrdinal)))
			}
		}
		p.valueKey = ""
		p.valueElemKey = ""
	case ':':
		if p.top() == '{' {
			if len(p.stack) == 1 {
				p.valueKey = p.lastKey
			} else if p.inTargetArray && len(p.stack) == 3 {
				p.valueElemKey = p.lastElemKey
			}
		}
		p.expectKey = false
	case ',':
		if p.top() == '{' {
			p.expectKey = true
		}
		p.valueKey = ""
		p.valueElemKey = ""
	default:
		// scalar value bytes (numbers, true/false/null) consume the member
		p.valueKey = ""
		p.valueElemKey = ""
	}
}

func (p *toolInputStreamPreview) stepString(b byte) {
	if p.inUnicode {
		p.stepUnicode(b)
		return
	}
	if p.escaped {
		p.escaped = false
		switch b {
		case '"', '\\', '/':
			p.flushHigh()
			p.sinkByte(b)
		case 'b':
			p.flushHigh()
			p.sinkByte('\b')
		case 'f':
			p.flushHigh()
			p.sinkByte('\f')
		case 'n':
			p.flushHigh()
			p.sinkByte('\n')
		case 'r':
			p.flushHigh()
			p.sinkByte('\r')
		case 't':
			p.flushHigh()
			p.sinkByte('\t')
		case 'u':
			p.inUnicode = true
			p.unicodeHex = p.unicodeHex[:0]
		default:
			// invalid escape in malformed JSON; keep the stream readable
			p.flushHigh()
			p.sinkRune(utf8.RuneError)
		}
		return
	}
	switch b {
	case '\\':
		p.escaped = true
	case '"':
		p.flushHigh()
		p.endString()
	default:
		p.flushHigh()
		p.sinkByte(b)
	}
}

func (p *toolInputStreamPreview) stepUnicode(b byte) {
	isHex := (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
	if !isHex {
		// malformed escape; emit a replacement and reprocess the byte normally
		p.inUnicode = false
		p.flushHigh()
		p.sinkRune(utf8.RuneError)
		p.stepString(b)
		return
	}
	p.unicodeHex = append(p.unicodeHex, b)
	if len(p.unicodeHex) < 4 {
		return
	}
	p.inUnicode = false
	value, err := strconv.ParseUint(string(p.unicodeHex), 16, 32)
	if err != nil {
		p.flushHigh()
		p.sinkRune(utf8.RuneError)
		return
	}
	r := rune(value)
	if !utf16.IsSurrogate(r) {
		p.flushHigh()
		p.sinkRune(r)
		return
	}
	if p.pendingHigh != 0 {
		if combined := utf16.DecodeRune(p.pendingHigh, r); combined != utf8.RuneError {
			p.pendingHigh = 0
			p.sinkRune(combined)
			return
		}
		p.flushHigh()
	}
	if r >= 0xD800 && r <= 0xDBFF {
		p.pendingHigh = r
		return
	}
	p.sinkRune(utf8.RuneError)
}

func (p *toolInputStreamPreview) flushHigh() {
	if p.pendingHigh == 0 {
		return
	}
	p.pendingHigh = 0
	p.sinkRune(utf8.RuneError)
}

func (p *toolInputStreamPreview) endString() {
	p.inString = false
	if p.isKey {
		p.isKey = false
		if len(p.stack) == 1 {
			p.lastKey = p.keyBuf.String()
		} else if p.inTargetArray && len(p.stack) == 3 {
			p.lastElemKey = p.keyBuf.String()
		}
		return
	}
	if p.capturing {
		p.capturing = false
		if p.spec.arrayField == "" {
			p.captureDone = true
		}
	}
	if p.capturingPath {
		p.capturingPath = false
		p.pathDone = true
		p.pathReady = true
	}
}

func (p *toolInputStreamPreview) sinkByte(b byte) {
	p.sinkBytes([]byte{b})
}

func (p *toolInputStreamPreview) sinkRune(r rune) {
	var buf [utf8.UTFMax]byte
	p.sinkBytes(buf[:utf8.EncodeRune(buf[:], r)])
}

func (p *toolInputStreamPreview) sinkBytes(data []byte) {
	switch {
	case p.isKey:
		if p.keyBuf.Len() < 256 {
			p.keyBuf.Write(data)
		}
	case p.capturing:
		if p.suppressed {
			return
		}
		p.emitted.Write(data)
		if p.out != nil {
			p.out.Write(data)
		}
		if p.emitted.Len() > maxToolInputPreviewBytes {
			p.suppressed = true
		}
	case p.capturingPath:
		if p.pathBuf.Len() < 2048 {
			p.pathBuf.Write(data)
		}
	}
}

func (p *toolInputStreamPreview) top() byte {
	if len(p.stack) == 0 {
		return 0
	}
	return p.stack[len(p.stack)-1]
}
