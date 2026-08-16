package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGlobFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGlob(t *testing.T, cwd, pattern string) []string {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"pattern": pattern})
	result, err := (GlobTool{}).Execute(context.Background(), Call{ID: "g", Name: "Glob", Input: input}, Env{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("glob %q failed: %s", pattern, result.Output)
	}
	if strings.TrimSpace(result.Output) == "No matches found" {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(result.Output), "\n")
	for index := range lines {
		lines[index] = filepath.ToSlash(strings.TrimSpace(lines[index]))
	}
	return lines
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestGlobSupportsRecursivePatterns covers the capability filepath.Glob could
// not express at all: matching across directory depth with `**`.
func TestGlobSupportsRecursivePatterns(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "main.go", "package main")
	writeGlobFixture(t, cwd, "internal/app/run.go", "package app")
	writeGlobFixture(t, cwd, "internal/app/deep/nested/util.go", "package nested")
	writeGlobFixture(t, cwd, "internal/app/notes.md", "notes")
	writeGlobFixture(t, cwd, "node_modules/dep/index.go", "package dep")

	t.Run("recursive across all depths", func(t *testing.T) {
		matches := runGlob(t, cwd, "**/*.go")
		for _, want := range []string{"main.go", "internal/app/run.go", "internal/app/deep/nested/util.go"} {
			if !containsString(matches, want) {
				t.Errorf("expected %q in recursive matches, got %v", want, matches)
			}
		}
		if containsString(matches, "internal/app/notes.md") {
			t.Errorf("markdown must not match *.go, got %v", matches)
		}
	})

	t.Run("heavy directories are skipped", func(t *testing.T) {
		matches := runGlob(t, cwd, "**/*.go")
		for _, match := range matches {
			if strings.HasPrefix(match, "node_modules/") {
				t.Errorf("node_modules must not be walked, got %v", matches)
			}
		}
	})

	t.Run("single level pattern stays single level", func(t *testing.T) {
		matches := runGlob(t, cwd, "*.go")
		if !containsString(matches, "main.go") {
			t.Errorf("expected top-level match, got %v", matches)
		}
		if containsString(matches, "internal/app/run.go") {
			t.Errorf("non-recursive pattern must not match nested files, got %v", matches)
		}
	})

	t.Run("prefixed recursive pattern", func(t *testing.T) {
		matches := runGlob(t, cwd, "internal/**/*.go")
		if !containsString(matches, "internal/app/deep/nested/util.go") {
			t.Errorf("expected deep match under internal, got %v", matches)
		}
		if containsString(matches, "main.go") {
			t.Errorf("pattern rooted at internal must not match main.go, got %v", matches)
		}
	})
}

func TestGlobOmitsSensitivePaths(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "app.go", "package main")
	writeGlobFixture(t, cwd, ".env", "SECRET=1")
	writeGlobFixture(t, cwd, ".git/config", "[core]")

	matches := runGlob(t, cwd, "**/*")
	for _, match := range matches {
		if strings.Contains(match, ".env") || strings.HasPrefix(match, ".git/") {
			t.Fatalf("sensitive path leaked through glob: %v", matches)
		}
	}
	if !containsString(matches, "app.go") {
		t.Fatalf("expected ordinary file to match, got %v", matches)
	}
}

func TestMatchGlobSegments(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "a/b/c.go", true},
		{"*.go", "a.go", true},
		{"*.go", "a/b.go", false},
		{"internal/**/*.go", "internal/a.go", true},
		{"internal/**/*.go", "internal/a/b/c.go", true},
		{"internal/**/*.go", "other/a.go", false},
		{"**", "a/b/c.go", true},
		{"a/*/c.go", "a/b/c.go", true},
		{"a/*/c.go", "a/b/d/c.go", false},
		{"**/test_*.py", "x/y/test_main.py", true},
		{"**/test_*.py", "x/y/main.py", false},
	}
	for _, testCase := range cases {
		got := matchGlobSegments(splitGlobPattern(testCase.pattern), splitGlobPattern(testCase.name))
		if got != testCase.want {
			t.Errorf("matchGlobSegments(%q, %q) = %v, want %v", testCase.pattern, testCase.name, got, testCase.want)
		}
	}
}

// TestReadPagesLargeFilesByLine covers the gap that made large files
// unreachable: without line paging, everything past the byte cap was lost.
func TestReadPagesLargeFilesByLine(t *testing.T) {
	cwd := t.TempDir()
	var builder strings.Builder
	for i := 1; i <= 500; i++ {
		builder.WriteString(fmt.Sprintf("line-%d\n", i))
	}
	writeGlobFixture(t, cwd, "big.txt", builder.String())

	read := func(payload string) Result {
		t.Helper()
		result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "r", Name: "Read", Input: json.RawMessage(payload)}, Env{CWD: cwd})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("read failed: %s", result.Output)
		}
		return result
	}

	t.Run("window returns the requested range", func(t *testing.T) {
		result := read(`{"file_path":"big.txt","offset":100,"line_limit":3}`)
		// The trailing marker tells the model more lines remain, so it knows to
		// page again rather than assuming it reached the end.
		if !strings.Contains(result.Output, "   100|line-100") || !strings.Contains(result.Output, "   102|line-102") {
			t.Fatalf("unexpected window: %q", result.Output)
		}
		if !strings.HasSuffix(result.Output, "...[truncated]") {
			t.Fatalf("expected a more-content marker, got %q", result.Output)
		}
		if result.Meta["startLine"] != 100 || result.Meta["endLine"] != 102 || result.Meta["lineCount"] != 500 {
			t.Fatalf("unexpected line metadata: %+v", result.Meta)
		}
	})

	t.Run("final page is not marked truncated", func(t *testing.T) {
		result := read(`{"file_path":"big.txt","offset":499,"line_limit":10}`)
		if strings.Contains(result.Output, "truncated") {
			t.Fatalf("reaching end of file must not be marked truncated, got %q", result.Output)
		}
		if result.Meta["endLine"] != 500 {
			t.Fatalf("expected the window to end at the last line, got %+v", result.Meta)
		}
	})

	t.Run("offset past end reports the file length", func(t *testing.T) {
		result := read(`{"file_path":"big.txt","offset":9000,"line_limit":5}`)
		if !strings.Contains(result.Output, "500 lines") {
			t.Fatalf("expected a helpful out-of-range message, got %q", result.Output)
		}
	})

	t.Run("default read is a full-file snapshot without a paging window", func(t *testing.T) {
		result := read(`{"file_path":"big.txt"}`)
		if !strings.Contains(fileReadBody(result.Output), "line-1\n") {
			t.Fatalf("default read should start at the top, got %q", result.Output[:min(80, len(result.Output))])
		}
		if _, ok := result.Meta["startLine"]; ok {
			t.Fatalf("default read must not report a line window: %+v", result.Meta)
		}
		if result.Meta["lineCount"] != 500 {
			t.Fatalf("default read must report the full file line count, got %+v", result.Meta)
		}
	})
}

func TestReadLineWindowRespectsWorkspaceBoundary(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, ".env", "SECRET=1")
	result, err := (ReadTool{}).Execute(context.Background(), Call{ID: "r", Name: "Read", Input: json.RawMessage(`{"file_path":".env","offset":1,"line_limit":5}`)}, Env{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || strings.Contains(result.Output, "SECRET") {
		t.Fatalf("line paging must not bypass sensitive-path filtering, got %+v", result)
	}
}
