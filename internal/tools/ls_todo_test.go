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

func runLS(t *testing.T, cwd, payload string) Result {
	t.Helper()
	result, err := (LSTool{}).Execute(context.Background(), Call{ID: "ls", Name: "LS", Input: json.RawMessage(payload)}, Env{CWD: cwd})
	if err != nil {
		t.Fatalf("LS returned an infrastructure error: %v", err)
	}
	return result
}

func lsLines(t *testing.T, result Result) []string {
	t.Helper()
	if result.IsError {
		t.Fatalf("LS failed: %s", result.Output)
	}
	if strings.TrimSpace(result.Output) == "No entries found" {
		return nil
	}
	return strings.Split(strings.TrimSpace(result.Output), "\n")
}

func lsFixture(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "main.go", "package main")
	writeGlobFixture(t, cwd, "README.md", "# readme")
	writeGlobFixture(t, cwd, "internal/app/run.go", "package app")
	writeGlobFixture(t, cwd, "internal/app/deep/util.go", "package deep")
	writeGlobFixture(t, cwd, "node_modules/dep/index.js", "module.exports={}")
	writeGlobFixture(t, cwd, ".env", "SECRET=1")
	writeGlobFixture(t, cwd, ".git/config", "[core]")
	return cwd
}

func TestLSListsEntries(t *testing.T) {
	cwd := lsFixture(t)

	t.Run("default depth lists only immediate children", func(t *testing.T) {
		lines := lsLines(t, runLS(t, cwd, `{}`))
		want := []string{"file README.md 8", "dir  internal/", "file main.go 12"}
		if strings.Join(lines, "|") != strings.Join(want, "|") {
			t.Fatalf("unexpected listing: %v", lines)
		}
	})

	t.Run("depth descends the requested number of levels", func(t *testing.T) {
		lines := lsLines(t, runLS(t, cwd, `{"depth":2}`))
		if !containsString(lines, "dir  internal/app/") {
			t.Fatalf("expected the second level, got %v", lines)
		}
		for _, line := range lines {
			if strings.Contains(line, "internal/app/run.go") {
				t.Fatalf("depth 2 must not reach third-level files: %v", lines)
			}
		}
		lines = lsLines(t, runLS(t, cwd, `{"depth":3}`))
		if !containsString(lines, "file internal/app/run.go 11") {
			t.Fatalf("expected the third level, got %v", lines)
		}
	})

	t.Run("path scopes the listing", func(t *testing.T) {
		lines := lsLines(t, runLS(t, cwd, `{"path":"internal/app","depth":2}`))
		if !containsString(lines, "file run.go 11") || !containsString(lines, "dir  deep/") {
			t.Fatalf("expected paths relative to the requested root, got %v", lines)
		}
	})

	t.Run("heavy and sensitive entries are omitted", func(t *testing.T) {
		lines := lsLines(t, runLS(t, cwd, `{"depth":10}`))
		for _, line := range lines {
			for _, banned := range []string{"node_modules", ".env", ".git"} {
				if strings.Contains(line, banned) {
					t.Fatalf("%q must never be listed, got %v", banned, lines)
				}
			}
		}
	})

	t.Run("empty directory reports no entries", func(t *testing.T) {
		empty := t.TempDir()
		result := runLS(t, empty, `{}`)
		if result.IsError || result.Output != "No entries found" {
			t.Fatalf("unexpected empty-directory result: %+v", result)
		}
		if result.Meta["count"] != 0 {
			t.Fatalf("expected a zero count, got %+v", result.Meta)
		}
	})
}

func TestLSRejectsBadInput(t *testing.T) {
	cwd := lsFixture(t)
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"escapes the working directory", `{"path":"../.."}`, "escapes working directory"},
		{"sensitive path", `{"path":".git"}`, "sensitive path"},
		{"file instead of directory", `{"path":"main.go"}`, "not a directory"},
		{"missing directory", `{"path":"nope"}`, "nope"},
		{"unknown field", `{"paths":"."}`, "unknown field"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := runLS(t, cwd, testCase.payload)
			if !result.IsError {
				t.Fatalf("expected a user-facing error, got %+v", result)
			}
			if !strings.Contains(result.Output, testCase.want) {
				t.Fatalf("expected output containing %q, got %q", testCase.want, result.Output)
			}
		})
	}
}

// TestLSBoundsEntryCount checks the cap actually engages, because an unbounded
// listing of a generated tree would blow the model's context window.
func TestLSBoundsEntryCount(t *testing.T) {
	cwd := t.TempDir()
	for index := 0; index < lsMaxEntries+25; index++ {
		writeGlobFixture(t, cwd, fmt.Sprintf("many/f%04d.txt", index), "x")
	}
	result := runLS(t, cwd, `{"path":"many"}`)
	if result.IsError {
		t.Fatalf("LS failed: %s", result.Output)
	}
	if count, _ := result.Meta["count"].(int); count > lsMaxEntries {
		t.Fatalf("entry count %d exceeds the cap %d", count, lsMaxEntries)
	}
	if truncated, _ := result.Meta["truncated"].(bool); !truncated {
		t.Fatalf("a capped listing must report truncation: %+v", result.Meta)
	}
	if !strings.HasSuffix(result.Output, "...[truncated]") {
		t.Fatalf("a capped listing must carry the truncation marker")
	}
}

// TestLSDoesNotFollowSymlinksOutOfWorkspace guards the boundary a directory
// listing would otherwise be an easy way to cross.
func TestLSDoesNotFollowSymlinksOutOfWorkspace(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "inside.txt", "ok")
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		// Creating symlinks on Windows needs Developer Mode or elevation.
		t.Skipf("symlinks unavailable: %v", err)
	}
	lines := lsLines(t, runLS(t, cwd, `{"depth":5}`))
	for _, line := range lines {
		if strings.Contains(line, "secret.txt") {
			t.Fatalf("listing escaped the workspace through a symlink: %v", lines)
		}
	}
	if !containsString(lines, "file inside.txt 2") {
		t.Fatalf("expected ordinary entries to survive, got %v", lines)
	}
}

func runTodoWrite(t *testing.T, payload string) Result {
	t.Helper()
	result, err := (TodoWriteTool{}).Execute(context.Background(), Call{ID: "t", Name: "TodoWrite", Input: json.RawMessage(payload)}, Env{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("TodoWrite returned an infrastructure error: %v", err)
	}
	return result
}

func TestTodoWriteRendersAndNormalizes(t *testing.T) {
	result := runTodoWrite(t, `{"todos":[{"content":"  Read the code  ","status":"completed"},{"content":"Write the tool","status":"IN_PROGRESS"},{"content":"Add tests","status":"pending"}]}`)
	if result.IsError {
		t.Fatalf("TodoWrite failed: %s", result.Output)
	}
	wantLines := []string{"[x] Read the code", "[>] Write the tool", "[ ] Add tests", "3 todos: 1 pending, 1 in progress, 1 completed"}
	if got := strings.Split(result.Output, "\n"); strings.Join(got, "|") != strings.Join(wantLines, "|") {
		t.Fatalf("unexpected rendering: %q", result.Output)
	}
	todos, ok := result.Meta["todos"].([]map[string]any)
	if !ok || len(todos) != 3 {
		t.Fatalf("expected three normalized todos in meta, got %+v", result.Meta["todos"])
	}
	if todos[0]["content"] != "Read the code" {
		t.Fatalf("content must be trimmed, got %q", todos[0]["content"])
	}
	if todos[1]["status"] != todoStatusInProgress {
		t.Fatalf("status must be lowercased, got %q", todos[1]["status"])
	}
}

func TestTodoWriteRejectsInvalidLists(t *testing.T) {
	longContent := strings.Repeat("a", maxTodoContentLength+1)
	oversized := make([]string, 0, maxTodoItems+1)
	for index := 0; index <= maxTodoItems; index++ {
		oversized = append(oversized, `{"content":"step","status":"pending"}`)
	}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"empty list", `{"todos":[]}`, "at least one item"},
		{"missing field", `{}`, "at least one item"},
		{"two in progress", `{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"in_progress"}]}`, "at most one"},
		{"blank content", `{"todos":[{"content":"   ","status":"pending"}]}`, "empty content"},
		{"content too long", `{"todos":[{"content":"` + longContent + `","status":"pending"}]}`, "maximum is 500"},
		{"unknown status", `{"todos":[{"content":"a","status":"done"}]}`, "want pending, in_progress, or completed"},
		{"too many items", `{"todos":[` + strings.Join(oversized, ",") + `]}`, "the maximum is 50"},
		{"unknown field", `{"todos":[{"content":"a","status":"pending","id":1}]}`, "unknown field"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := runTodoWrite(t, testCase.payload)
			if !result.IsError {
				t.Fatalf("expected a user-facing error, got %+v", result)
			}
			if !strings.Contains(result.Output, testCase.want) {
				t.Fatalf("expected output containing %q, got %q", testCase.want, result.Output)
			}
			if result.Meta["todos"] != nil {
				t.Fatalf("a rejected list must not be reported as recorded: %+v", result.Meta)
			}
		})
	}
}

// TestTodoWriteAllowsExactlyOneInProgress pins the boundary of the in_progress
// rule: one is the point of the tool, zero is legal for a finished list.
func TestTodoWriteAllowsExactlyOneInProgress(t *testing.T) {
	for _, payload := range []string{
		`{"todos":[{"content":"a","status":"in_progress"},{"content":"b","status":"pending"}]}`,
		`{"todos":[{"content":"a","status":"completed"},{"content":"b","status":"completed"}]}`,
	} {
		if result := runTodoWrite(t, payload); result.IsError {
			t.Fatalf("payload %s must be accepted, got %q", payload, result.Output)
		}
	}
}

func TestTodoWriteStoresNothingOnDisk(t *testing.T) {
	cwd := t.TempDir()
	result, err := (TodoWriteTool{}).Execute(context.Background(),
		Call{ID: "t", Name: "TodoWrite", Input: json.RawMessage(`{"todos":[{"content":"a","status":"pending"}]}`)},
		Env{CWD: cwd})
	if err != nil || result.IsError {
		t.Fatalf("TodoWrite failed: %v %+v", err, result)
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("TodoWrite must not touch the filesystem, found %d entries", len(entries))
	}
}
