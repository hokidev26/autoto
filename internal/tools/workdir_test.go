package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveWorkdirWithinKeepsNestedDirectoriesInsideParent(t *testing.T) {
	parent := t.TempDir()
	nested := filepath.Join(parent, "repo", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveWorkdirWithin(parent, nested)
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(resolved, nested) {
		t.Fatalf("resolved nested workdir=%q, want %q", resolved, nested)
	}
	inherited, err := ResolveWorkdirWithin(parent, "")
	if err != nil {
		t.Fatal(err)
	}
	if !sameResolvedPath(inherited, parent) {
		t.Fatalf("inherited workdir=%q, want %q", inherited, parent)
	}
}

func TestResolveWorkdirWithinRejectsTraversalAndPrefixCollisions(t *testing.T) {
	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	prefixCollision := parent + "-sibling"
	if err := os.MkdirAll(prefixCollision, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, requested := range map[string]string{
		"dotdot":           filepath.Join(parent, "..", filepath.Base(outside)),
		"outside":          outside,
		"prefix collision": prefixCollision,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveWorkdirWithin(parent, requested); err == nil {
				t.Fatalf("workdir %q was accepted", requested)
			}
		})
	}
	if _, err := ResolveWorkdirWithin(parent, "child/../child"); err == nil {
		t.Fatal("workdir containing .. was accepted")
	}
}

func TestResolveWorkdirWithinRejectsSymlinkEscapeWhenSupported(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(parent, "link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := ResolveWorkdirWithin(parent, link); err == nil || !strings.Contains(strings.ToLower(err.Error()), "inside") {
		t.Fatalf("symlink escape was not rejected: %v", err)
	}
}

func sameResolvedPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}
