package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fileReadBody(output string) string {
	_, body, ok := strings.Cut(output, "\n")
	if !ok {
		return ""
	}
	return body
}

func TestFileSnapshotHashIsStableAndCoversTheWholeFile(t *testing.T) {
	cwd := t.TempDir()
	content := []byte("hello snapshot\n")
	if err := os.WriteFile(filepath.Join(cwd, "note.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	want := hex.EncodeToString(sum[:])[:contentHashHexLen]

	first, err := (ReadTool{}).Execute(context.Background(), Call{ID: "r1", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}, Env{CWD: cwd})
	if err != nil || first.IsError {
		t.Fatalf("read failed: %+v err=%v", first, err)
	}
	second, err := (ReadTool{}).Execute(context.Background(), Call{ID: "r2", Name: "Read", Input: json.RawMessage(`{"file_path":"note.txt"}`)}, Env{CWD: cwd})
	if err != nil || second.IsError {
		t.Fatalf("second read failed: %+v err=%v", second, err)
	}
	if first.Meta["contentHash"] != want || second.Meta["contentHash"] != want {
		t.Fatalf("hash was not stable: first=%v second=%v want=%s", first.Meta["contentHash"], second.Meta["contentHash"], want)
	}
	if !strings.HasPrefix(first.Output, "file note.txt hash="+want+" lines=1\n") {
		t.Fatalf("expected model-visible header, got %q", first.Output)
	}
}

func TestTruncatedReadUsesFullFileHashAndOutline(t *testing.T) {
	cwd := t.TempDir()
	var builder strings.Builder
	builder.WriteString(strings.Repeat("padding line\n", 9000))
	builder.WriteString("func UniqueTail() {}\n")
	builder.WriteString("# Heading Near End\n")
	content := []byte(builder.String())
	if len(content) <= maxReadBytes {
		t.Fatalf("fixture must exceed the read byte cap, got %d", len(content))
	}
	if err := os.WriteFile(filepath.Join(cwd, "wide.go"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	full := hashBytes(content)
	prefix := hashBytes(content[:maxReadBytes])
	if prefix.hash == full.hash {
		t.Fatal("prefix hash accidentally matched the full-file hash")
	}

	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "trunc", Name: "Read", Input: json.RawMessage(`{"file_path":"wide.go"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError || result.Meta["truncated"] != true {
		t.Fatalf("expected truncated read, result=%+v err=%v", result, err)
	}
	if result.Meta["contentHash"] != full.hash {
		t.Fatalf("truncated read hashed the window instead of the file: got %v want %s", result.Meta["contentHash"], full.hash)
	}
	if result.Meta["lineCount"] != full.lines {
		t.Fatalf("lineCount=%v want %d", result.Meta["lineCount"], full.lines)
	}
	if !strings.Contains(result.Output, "     1|padding line") {
		t.Fatalf("truncated window must include line numbers, got %q", result.Output[:min(120, len(result.Output))])
	}
	if !strings.Contains(result.Output, "func UniqueTail() {}") || !strings.Contains(result.Output, "# Heading Near End") {
		t.Fatalf("outline missing late declarations: %q", result.Output[len(result.Output)-400:])
	}
	if !strings.Contains(result.Output, "outline:") {
		t.Fatalf("expected an outline section, got %q", result.Output[len(result.Output)-200:])
	}
}

func TestFileOutlineIsBounded(t *testing.T) {
	cwd := t.TempDir()
	var builder strings.Builder
	builder.WriteString(strings.Repeat("padding line\n", 8000))
	for i := 0; i < maxFileOutlineEntries+20; i++ {
		builder.WriteString("func Extra")
		builder.WriteString(strings.Repeat("X", i%3+1))
		builder.WriteString("() {}\n")
	}
	if err := os.WriteFile(filepath.Join(cwd, "many.go"), []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "outline", Name: "Read", Input: json.RawMessage(`{"file_path":"many.go"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: %+v err=%v", result, err)
	}
	if strings.Count(result.Output, "func Extra") > maxFileOutlineEntries {
		t.Fatalf("outline exceeded the entry cap:\n%s", result.Output[max(0, len(result.Output)-1500):])
	}
	if !strings.Contains(result.Output, "...[outline truncated]") {
		t.Fatal("expected the outline cap marker")
	}
}

func TestEditRefusesStaleExpectedHashAndOmittingHashStillEdits(t *testing.T) {
	cwd := t.TempDir()
	path := filepath.Join(cwd, "hello.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := (ReadTool{}).Execute(context.Background(), Call{ID: "r", Name: "Read", Input: json.RawMessage(`{"file_path":"hello.txt"}`)}, Env{CWD: cwd})
	if err != nil || read.IsError {
		t.Fatalf("read failed: %+v err=%v", read, err)
	}
	liveHash, _ := read.Meta["contentHash"].(string)
	if liveHash == "" {
		t.Fatalf("missing contentHash: %+v", read.Meta)
	}

	stale, _ := json.Marshal(map[string]string{
		"file_path": "hello.txt", "old_string": "world", "new_string": "agent", "expected_hash": "aaaaaaaaaaaaaaaa",
	})
	denied, err := (EditTool{}).Execute(context.Background(), Call{ID: "stale", Name: "Edit", Input: stale}, Env{CWD: cwd})
	if err != nil || !denied.IsError || !strings.Contains(denied.Output, liveHash) {
		t.Fatalf("expected stale hash refusal, result=%+v err=%v", denied, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello world" {
		t.Fatalf("stale edit wrote to disk: %q", data)
	}

	okInput, _ := json.Marshal(map[string]string{
		"file_path": "hello.txt", "old_string": "world", "new_string": "agent", "expected_hash": liveHash,
	})
	ok, err := (EditTool{}).Execute(context.Background(), Call{ID: "ok", Name: "Edit", Input: okInput}, Env{CWD: cwd})
	if err != nil || ok.IsError {
		t.Fatalf("matching hash edit failed: %+v err=%v", ok, err)
	}
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	omitted, _ := json.Marshal(map[string]string{"file_path": "hello.txt", "old_string": "world", "new_string": "agent"})
	plain, err := (EditTool{}).Execute(context.Background(), Call{ID: "omit", Name: "Edit", Input: omitted}, Env{CWD: cwd})
	if err != nil || plain.IsError {
		t.Fatalf("omitted hash must still edit: %+v err=%v", plain, err)
	}
	if !strings.Contains(plain.Output, "hash=") {
		t.Fatalf("successful edit must keep a model-visible hash header: %q", plain.Output)
	}
}

func TestMultiEditRefusesStaleExpectedHash(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "app.go", "package app\n")
	input, _ := json.Marshal(map[string]any{
		"file_path":     "app.go",
		"expected_hash": "bbbbbbbbbbbbbbbb",
		"edits":         []map[string]string{{"old_string": "package app", "new_string": "package changed"}},
	})
	result, err := (MultiEditTool{}).Execute(context.Background(), Call{ID: "stale", Name: "MultiEdit", Input: input}, Env{CWD: cwd})
	if err != nil || !result.IsError || !strings.Contains(result.Output, "Re-read") {
		t.Fatalf("expected stale hash refusal, result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "package app\n" {
		t.Fatalf("stale multiedit wrote to disk: %q", data)
	}
}
