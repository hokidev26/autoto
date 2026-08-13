package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable: %v (%s)", args, err, out)
		}
	}
}

// A project pointed at a directory that merely contains a repository used to be
// accepted in silence. Git resolves repositories by walking upward, never
// downward, so that project can never anchor a continuation safety snapshot: a
// long task stops partway with an internal message, days after the directory was
// chosen. The report is what lets the UI say so at creation time.
func TestProjectWorkspaceGitReportNamesTheRepositoryInsideTheChosenPath(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "autoto")
	initTestGitRepo(t, repo)

	report := (&Server{}).projectWorkspaceGitReport(context.Background(), parent)

	if report["isGitRepository"] != false {
		t.Fatalf("the parent directory is not a repository: %+v", report)
	}
	discovered, _ := report["discoveredRoot"].(string)
	if discovered == "" {
		t.Fatalf("the repository inside the chosen path must be named: %+v", report)
	}
	if !sameFilesystemProjectPath(discovered, repo) {
		t.Fatalf("discoveredRoot = %q, want %q", discovered, repo)
	}
}

func TestProjectWorkspaceGitReportAcceptsARepositoryRoot(t *testing.T) {
	repo := t.TempDir()
	initTestGitRepo(t, repo)

	report := (&Server{}).projectWorkspaceGitReport(context.Background(), repo)

	if report["isGitRepository"] != true {
		t.Fatalf("a repository root must be reported as one: %+v", report)
	}
	if _, ok := report["discoveredRoot"]; ok {
		t.Fatalf("a repository root needs no discovery hint: %+v", report)
	}
}

// A plain directory with no repository anywhere is a legitimate project: init-git
// exists for exactly that. It must be reported, not rejected, and without a
// misleading hint.
func TestProjectWorkspaceGitReportAllowsAPlainDirectory(t *testing.T) {
	plain := t.TempDir()

	report := (&Server{}).projectWorkspaceGitReport(context.Background(), plain)

	if report["isGitRepository"] != false {
		t.Fatalf("a plain directory is not a repository: %+v", report)
	}
	if _, ok := report["discoveredRoot"]; ok {
		t.Fatalf("no hint should be offered when nothing was found: %+v", report)
	}
	if report["path"] != plain {
		t.Fatalf("path = %v, want %q", report["path"], plain)
	}
}

// Two sibling repositories are ambiguous, so no single root may be asserted.
func TestProjectWorkspaceGitReportDoesNotGuessBetweenSiblingRepositories(t *testing.T) {
	parent := t.TempDir()
	initTestGitRepo(t, filepath.Join(parent, "one"))
	initTestGitRepo(t, filepath.Join(parent, "two"))

	report := (&Server{}).projectWorkspaceGitReport(context.Background(), parent)

	if _, ok := report["discoveredRoot"]; ok {
		t.Fatalf("an ambiguous choice must not be presented as the answer: %+v", report)
	}
	roots, _ := report["discoveredRoots"].([]string)
	if len(roots) != 2 {
		t.Fatalf("both candidates should be listed, got %+v", report)
	}
}
