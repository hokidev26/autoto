package gitsnapshot

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorktreeFingerprintIncludesPermissionsAndContent(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "large.txt")
	contents := strings.Repeat("abcdef", 256*1024)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := WorktreeFingerprint(repo, "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	afterMode := before
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
		afterMode, err = WorktreeFingerprint(repo, "large.txt")
		if err != nil {
			t.Fatal(err)
		}
		if before == afterMode {
			t.Fatal("expected chmod to change fingerprint")
		}
	}
	if err := os.WriteFile(path, []byte(contents+"changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	afterContent, err := WorktreeFingerprint(repo, "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if afterMode == afterContent {
		t.Fatal("expected content change to change fingerprint")
	}
}

func TestWorktreeFingerprintHonorsContextAndByteBudgets(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "first.txt"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("5678"), 0o644); err != nil {
		t.Fatal(err)
	}
	budget := &FingerprintBudget{MaxFileBytes: 4, MaxTotalBytes: 6}
	if _, err := WorktreeFingerprintWithBudget(context.Background(), repo, "first.txt", budget); err != nil {
		t.Fatal(err)
	}
	if _, err := WorktreeFingerprintWithBudget(context.Background(), repo, "second.txt", budget); err == nil || !strings.Contains(err.Error(), "total byte budget") {
		t.Fatalf("expected aggregate byte limit, got %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WorktreeFingerprintWithBudget(cancelled, repo, "first.txt", nil); err == nil {
		t.Fatal("expected canceled context to stop fingerprinting")
	}
}

func TestParsePorcelainV1NoRenames(t *testing.T) {
	entries, err := ParsePorcelainV1NoRenames(" M tracked.txt\x00?? new.txt\x00")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "tracked.txt" || entries[0].IndexStatus != " " || entries[0].WorktreeStatus != "M" || !entries[1].Untracked {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}
