package tools

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBashOutputCollectorKeepsShortOutputIntact(t *testing.T) {
	collector := newBashOutputCollector(nil)
	if _, err := collector.Write([]byte("hello world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, truncated := collector.result()
	if truncated {
		t.Fatalf("short output must not be truncated")
	}
	if text != "hello world" {
		t.Fatalf("unexpected output %q", text)
	}
}

func TestBashOutputCollectorFillsExactlyToLimit(t *testing.T) {
	collector := newBashOutputCollector(nil)
	payload := strings.Repeat("a", bashResultMaxBytes)
	if _, err := collector.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, truncated := collector.result()
	if truncated {
		t.Fatalf("output at exactly the limit must not be truncated")
	}
	if text != payload {
		t.Fatalf("output at the limit was modified: got %d bytes, want %d", len(text), len(payload))
	}
}

func TestBashOutputCollectorKeepsHeadAndTail(t *testing.T) {
	collector := newBashOutputCollector(nil)
	head := strings.Repeat("H", bashResultHeadBytes)
	middle := strings.Repeat("M", 50000)
	tail := strings.Repeat("T", bashResultTailBytes)
	if _, err := collector.Write([]byte(head + middle + tail)); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, truncated := collector.result()
	if !truncated {
		t.Fatalf("oversized output must report truncation")
	}
	if !strings.HasPrefix(text, head) {
		t.Fatalf("head was not retained")
	}
	// The regression this guards: the failing end of a command used to be the
	// part that got thrown away.
	if !strings.HasSuffix(text, tail) {
		t.Fatalf("tail was not retained")
	}
	if strings.Contains(text, "M") {
		t.Fatalf("discarded middle leaked into the result")
	}
	marker := fmt.Sprintf("\n...[%d bytes truncated]...\n", len(middle))
	if !strings.Contains(text, marker) {
		t.Fatalf("missing truncation marker %q", marker)
	}
	if want := len(head) + len(marker) + len(tail); len(text) != want {
		t.Fatalf("result is %d bytes, want %d", len(text), want)
	}
}

func TestBashOutputCollectorTailSurvivesManySmallWrites(t *testing.T) {
	collector := newBashOutputCollector(nil)
	total := 0
	for i := 0; i < 20000; i++ {
		line := fmt.Sprintf("line %d\n", i)
		total += len(line)
		if _, err := collector.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	text, truncated := collector.result()
	if !truncated {
		t.Fatalf("expected truncation after %d bytes", total)
	}
	if !strings.HasPrefix(text, "line 0\n") {
		t.Fatalf("head lost across many writes")
	}
	if !strings.HasSuffix(text, "line 19999\n") {
		t.Fatalf("ring buffer lost the last line: got tail %q", text[max(0, len(text)-40):])
	}
}

func TestBashOutputCollectorDoesNotSplitRunes(t *testing.T) {
	collector := newBashOutputCollector(nil)
	// The leading ASCII byte pushes both the head cut and the tail start off a
	// 3-byte rune boundary, so an unguarded byte-count cut would emit a partial
	// sequence at each join.
	payload := "x" + strings.Repeat("測", 10000)
	if _, err := collector.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, truncated := collector.result()
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if !utf8.ValidString(text) {
		t.Fatalf("truncation produced invalid UTF-8")
	}
	if strings.ContainsRune(text, utf8.RuneError) {
		t.Fatalf("truncation produced a replacement character")
	}
}

func TestBashOutputCollectorPassesBinaryThrough(t *testing.T) {
	collector := newBashOutputCollector(nil)
	// Continuation bytes with no rune start: not a split rune, so the boundary
	// trim must leave them alone instead of silently eating bytes.
	payload := append([]byte(strings.Repeat("a", bashResultHeadBytes-2)), 0x80, 0x80)
	payload = append(payload, strings.Repeat("b", 40000)...)
	payload = append(payload, 0x80, 0x80)
	if _, err := collector.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	text, truncated := collector.result()
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if !strings.HasPrefix(text, strings.Repeat("a", bashResultHeadBytes-2)+"\x80\x80") {
		t.Fatalf("binary head bytes were trimmed")
	}
	if !strings.HasSuffix(text, "\x80\x80") {
		t.Fatalf("binary tail bytes were trimmed")
	}
}
