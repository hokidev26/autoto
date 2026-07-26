package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runGrep(t *testing.T, cwd string, payload string) Result {
	t.Helper()
	result, err := (GrepTool{}).Execute(context.Background(), Call{ID: "g", Name: "Grep", Input: json.RawMessage(payload)}, Env{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func grepFixture(t *testing.T) string {
	t.Helper()
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "alpha.go", "package alpha\n\nfunc Target() {}\n\nfunc Other() {}\n")
	writeGlobFixture(t, cwd, "nested/beta.go", "package beta\n\nline1\nline2\nfunc Target() {}\nline4\nline5\n")
	writeGlobFixture(t, cwd, "notes.md", "Target appears here too\n")
	writeGlobFixture(t, cwd, "node_modules/dep/skip.go", "func Target() {}\n")
	return cwd
}

func TestGrepOutputModes(t *testing.T) {
	cwd := grepFixture(t)

	t.Run("content is the default", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"func Target"}`)
		if result.IsError {
			t.Fatalf("grep failed: %s", result.Output)
		}
		if !strings.Contains(result.Output, "alpha.go:3:func Target() {}") {
			t.Fatalf("expected file:line:text output, got %q", result.Output)
		}
		if result.Meta["outputMode"] != "content" {
			t.Fatalf("expected content mode, got %+v", result.Meta)
		}
	})

	t.Run("files_with_matches lists paths only", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","output_mode":"files_with_matches"}`)
		lines := strings.Split(strings.TrimSpace(result.Output), "\n")
		for _, line := range lines {
			if strings.Contains(line, ":") {
				t.Fatalf("files_with_matches must return bare paths, got %q", line)
			}
		}
		if len(lines) != 3 {
			t.Fatalf("expected three matching files, got %v", lines)
		}
	})

	t.Run("count reports per-file totals", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","output_mode":"count"}`)
		if !strings.Contains(result.Output, "alpha.go:1") {
			t.Fatalf("expected per-file counts, got %q", result.Output)
		}
	})

	t.Run("invalid mode is rejected", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","output_mode":"nonsense"}`)
		if !result.IsError {
			t.Fatalf("expected an error for an unknown output mode, got %q", result.Output)
		}
	})
}

func TestGrepContextLines(t *testing.T) {
	cwd := grepFixture(t)

	t.Run("before and after", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"func Target","path":"nested","before":2,"after":1}`)
		// Context lines use '-' and the match uses ':', matching grep convention.
		for _, want := range []string{"beta.go-3-line1", "beta.go-4-line2", "beta.go:5:func Target() {}", "beta.go-6-line4"} {
			if !strings.Contains(result.Output, want) {
				t.Fatalf("expected %q in output, got %q", want, result.Output)
			}
		}
		if strings.Contains(result.Output, "line5") {
			t.Fatalf("after=1 must not reach line5, got %q", result.Output)
		}
	})

	t.Run("context sets both sides", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"func Target","path":"nested","context":1}`)
		if !strings.Contains(result.Output, "beta.go-4-line2") || !strings.Contains(result.Output, "beta.go-6-line4") {
			t.Fatalf("expected one line of context on each side, got %q", result.Output)
		}
		if strings.Contains(result.Output, "line1") {
			t.Fatalf("context=1 must not reach line1, got %q", result.Output)
		}
	})
}

func TestGrepFiltersAndFlags(t *testing.T) {
	cwd := grepFixture(t)

	t.Run("glob restricts the search", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","glob":"**/*.md","output_mode":"files_with_matches"}`)
		if strings.Contains(result.Output, ".go") || !strings.Contains(result.Output, "notes.md") {
			t.Fatalf("glob filter did not apply, got %q", result.Output)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		sensitive := runGrep(t, cwd, `{"pattern":"TARGET","output_mode":"count"}`)
		if !strings.Contains(sensitive.Output, "No matches") {
			t.Fatalf("expected no case-sensitive matches, got %q", sensitive.Output)
		}
		insensitive := runGrep(t, cwd, `{"pattern":"TARGET","case_insensitive":true,"output_mode":"count"}`)
		if strings.Contains(insensitive.Output, "No matches") {
			t.Fatalf("expected case-insensitive matches, got %q", insensitive.Output)
		}
	})

	t.Run("heavy directories are skipped", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","output_mode":"files_with_matches"}`)
		if strings.Contains(result.Output, "node_modules") {
			t.Fatalf("node_modules must not be searched, got %q", result.Output)
		}
	})

	t.Run("sensitive files are omitted", func(t *testing.T) {
		writeGlobFixture(t, cwd, ".env", "Target=secret\n")
		result := runGrep(t, cwd, `{"pattern":"Target","output_mode":"files_with_matches"}`)
		if strings.Contains(result.Output, ".env") {
			t.Fatalf("sensitive file leaked through grep, got %q", result.Output)
		}
	})

	t.Run("head limit truncates", func(t *testing.T) {
		result := runGrep(t, cwd, `{"pattern":"Target","head_limit":1}`)
		if truncated, _ := result.Meta["truncated"].(bool); !truncated {
			t.Fatalf("expected truncation to be reported, got %+v", result.Meta)
		}
	})
}

func runMultiEdit(t *testing.T, cwd, payload string) Result {
	t.Helper()
	result, err := (MultiEditTool{}).Execute(context.Background(), Call{ID: "m", Name: "MultiEdit", Input: json.RawMessage(payload)}, Env{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestMultiEditAppliesEveryHunk(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "app.go", "package app\n\nconst A = 1\nconst B = 2\nconst C = 3\n")

	result := runMultiEdit(t, cwd, `{"file_path":"app.go","edits":[
		{"old_string":"const A = 1","new_string":"const A = 10"},
		{"old_string":"const B = 2","new_string":"const B = 20"},
		{"old_string":"const C = 3","new_string":"const C = 30"}
	]}`)
	if result.IsError {
		t.Fatalf("multiedit failed: %s", result.Output)
	}
	data, err := os.ReadFile(filepath.Join(cwd, "app.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"const A = 10", "const B = 20", "const C = 30"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected %q in the file, got %q", want, string(data))
		}
	}
	if result.Meta["edits"] != 3 || result.Meta["replacements"] != 3 {
		t.Fatalf("unexpected counts: %+v", result.Meta)
	}
	if diff, _ := result.Meta["diff"].(string); !strings.Contains(diff, "+const A = 10") {
		t.Fatalf("expected a unified diff, got %q", diff)
	}
}

// TestMultiEditIsAtomic is the property that makes this worth having over
// repeated Edit calls: a failure anywhere leaves the file untouched, so the
// model never has to work out which hunks already landed.
func TestMultiEditIsAtomic(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name:    "later hunk missing",
			payload: `{"file_path":"app.go","edits":[{"old_string":"const A = 1","new_string":"const A = 10"},{"old_string":"const MISSING = 9","new_string":"x"}]}`,
			wantErr: "edits[1]: old_string not found",
		},
		{
			name:    "later hunk ambiguous",
			payload: `{"file_path":"app.go","edits":[{"old_string":"const A = 1","new_string":"const A = 10"},{"old_string":"dup","new_string":"x"}]}`,
			wantErr: "edits[1]: old_string is not unique; found 2 occurrences",
		},
		{
			name:    "no-op hunk",
			payload: `{"file_path":"app.go","edits":[{"old_string":"const A = 1","new_string":"const A = 1"}]}`,
			wantErr: "edits[0]: old_string and new_string must differ",
		},
	}
	const original = "package app\n\nconst A = 1\nconst B = 2\n// dup\n// dup\n"
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cwd := t.TempDir()
			writeGlobFixture(t, cwd, "app.go", original)

			result := runMultiEdit(t, cwd, testCase.payload)

			if !result.IsError || !strings.Contains(result.Output, testCase.wantErr) {
				t.Fatalf("want error %q, got isError=%v output=%q", testCase.wantErr, result.IsError, result.Output)
			}
			data, err := os.ReadFile(filepath.Join(cwd, "app.go"))
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != original {
				t.Fatalf("file must be untouched after a failed multiedit, got %q", string(data))
			}
		})
	}
}

// TestMultiEditHunksSeePriorResults documents the ordering contract: each hunk
// operates on the text the previous hunk produced.
func TestMultiEditHunksSeePriorResults(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "app.go", "alpha\n")

	result := runMultiEdit(t, cwd, `{"file_path":"app.go","edits":[
		{"old_string":"alpha","new_string":"beta"},
		{"old_string":"beta","new_string":"gamma"}
	]}`)
	if result.IsError {
		t.Fatalf("multiedit failed: %s", result.Output)
	}
	data, _ := os.ReadFile(filepath.Join(cwd, "app.go"))
	if strings.TrimSpace(string(data)) != "gamma" {
		t.Fatalf("expected sequential application, got %q", string(data))
	}
}

func TestMultiEditReplaceAllAndValidation(t *testing.T) {
	cwd := t.TempDir()
	writeGlobFixture(t, cwd, "app.go", "x\nx\nx\n")

	result := runMultiEdit(t, cwd, `{"file_path":"app.go","edits":[{"old_string":"x","new_string":"y","replace_all":true}]}`)
	if result.IsError {
		t.Fatalf("multiedit failed: %s", result.Output)
	}
	if result.Meta["replacements"] != 3 {
		t.Fatalf("expected three replacements, got %+v", result.Meta)
	}

	if got := runMultiEdit(t, cwd, `{"file_path":"app.go","edits":[]}`); !got.IsError {
		t.Fatal("empty edits must be rejected")
	}
	if got := runMultiEdit(t, cwd, `{"file_path":".env","edits":[{"old_string":"a","new_string":"b"}]}`); !got.IsError {
		t.Fatal("sensitive paths must be rejected")
	}
}
