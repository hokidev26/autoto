package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadToolDoesNotRenderBinaryBytes(t *testing.T) {
	cwd := t.TempDir()
	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0x00, 0xff, 0x01}
	if err := os.WriteFile(filepath.Join(cwd, "image.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "binary", Name: "Read", Input: json.RawMessage(`{"file_path":"image.png"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Output, "Binary file not displayed") || strings.Contains(result.Output, string(data)) {
		t.Fatalf("binary data was rendered directly: %q", result.Output)
	}
	if binary, _ := result.Meta["binary"].(bool); !binary || result.Meta["mime"] != "image/png" || result.Meta["size"] != int64(len(data)) {
		t.Fatalf("unexpected binary metadata: %+v", result.Meta)
	}
}

func TestReadToolTreatsInvalidUTF8AsBinary(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "invalid.txt"), []byte{'o', 'k', 0xff, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "invalid", Name: "Read", Input: json.RawMessage(`{"file_path":"invalid.txt"}`)}, Env{CWD: cwd})
	if err != nil || result.IsError || !strings.Contains(result.Output, "Binary file not displayed") {
		t.Fatalf("expected invalid UTF-8 to be omitted as binary: result=%+v err=%v", result, err)
	}
}

func TestReadToolKeepsTruncatedUTF8Valid(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "unicode.txt"), []byte("界界"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "unicode", Name: "Read", Input: json.RawMessage(`{"file_path":"unicode.txt","limit":4}`)}, Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("read failed: result=%+v err=%v", result, err)
	}
	if binary, _ := result.Meta["binary"].(bool); binary || !result.Meta["truncated"].(bool) || !utf8.ValidString(result.Output) || !strings.HasPrefix(result.Output, "界") {
		t.Fatalf("expected valid truncated UTF-8 text: %+v", result)
	}
}
