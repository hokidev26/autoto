package spill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveWritesFullContentUnderTheConversation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("spilled line\n", 500)
	path, err := store.Save("conversation-1", "Bash", content)
	if err != nil {
		t.Fatal(err)
	}
	if directory := filepath.Dir(path); directory != filepath.Join(store.Root(), "conversation-1") {
		t.Fatalf("spill landed outside the conversation directory: %s", path)
	}
	if base := filepath.Base(path); !strings.HasPrefix(base, "Bash-") || !strings.HasSuffix(base, ".txt") {
		t.Fatalf("unexpected spill file name: %s", base)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != content {
		t.Fatalf("spill file content differs from the tool output: %d stored bytes, %d original", len(stored), len(content))
	}
}

func TestSaveNeverReusesAPath(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{}, 16)
	for range 16 {
		path, err := store.Save("conversation-1", "Bash", "output")
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := seen[path]; exists {
			t.Fatalf("spill reused a path: %s", path)
		}
		seen[path] = struct{}{}
	}
}

func TestSaveRejectsAnUnusableConversationID(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, conversationID := range []string{"", "   ", "../escape", `..\escape`, "with space", "a/b"} {
		if _, err := store.Save(conversationID, "Bash", "output"); !errors.Is(err, ErrInvalidConversation) {
			t.Fatalf("Save(%q) error = %v, want ErrInvalidConversation", conversationID, err)
		}
	}
}

func TestSaveFailsWhenTheConversationDirectoryIsNotADirectory(t *testing.T) {
	home := t.TempDir()
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "conversation-1"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save("conversation-1", "Bash", "output"); err == nil {
		t.Fatal("Save succeeded against a conversation path occupied by a file")
	}
}

func TestPruneRemovesExpiredFilesAndEmptyConversations(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.Save("conversation-stale", "Bash", "old output")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := store.Save("conversation-fresh", "Bash", "new output")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * Retention)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := store.Prune(Retention)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("Prune removed %d files, want 1", removed)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired spill survived pruning: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(stale)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("emptied conversation directory survived pruning: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("live spill was pruned: %v", err)
	}
}

func TestPruneToleratesAMissingRoot(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store.Root()); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Prune(Retention)
	if err != nil || removed != 0 {
		t.Fatalf("Prune() = %d, %v; want 0, nil", removed, err)
	}
}
