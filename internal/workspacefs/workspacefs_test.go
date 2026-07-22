package workspacefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTreeReadWriteAndAtomicSave(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "z-dir"))
	mustMkdir(t, filepath.Join(root, "A-dir"))
	mustMkdir(t, filepath.Join(root, "node_modules"))
	mustMkdir(t, filepath.Join(root, ".git"))
	mustWrite(t, filepath.Join(root, "node_modules", "dependency.js"), []byte("ignored"), 0o644)
	mustWrite(t, filepath.Join(root, "b.txt"), []byte("bravo"), 0o644)
	mustWrite(t, filepath.Join(root, "A.txt"), []byte("alpha"), 0o600)
	mustWrite(t, filepath.Join(root, ".env"), []byte("TOKEN=secret"), 0o600)

	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := fs.Tree("")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Path != "" {
		t.Fatalf("expected relative root path, got %q", tree.Path)
	}
	gotNames := make([]string, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		gotNames = append(gotNames, entry.Name)
		if filepath.IsAbs(entry.Path) || strings.Contains(entry.Path, root) {
			t.Fatalf("tree leaked an absolute path: %+v", entry)
		}
	}
	wantNames := []string{"A-dir", "z-dir", "A.txt", "b.txt"}
	if fmt.Sprint(gotNames) != fmt.Sprint(wantNames) {
		t.Fatalf("unexpected sorted tree: got %v want %v", gotNames, wantNames)
	}
	if !tree.Entries[2].Editable || tree.Entries[0].Editable {
		t.Fatalf("unexpected editable flags: %+v", tree.Entries)
	}

	read, err := fs.ReadFile("A.txt")
	if err != nil {
		t.Fatal(err)
	}
	if read.Path != "A.txt" || read.Name != "A.txt" || read.Content != "alpha" || read.ReadOnly || read.Truncated {
		t.Fatalf("unexpected file response: %+v", read)
	}
	beforeInfo, err := os.Stat(filepath.Join(root, "A.txt"))
	if err != nil {
		t.Fatal(err)
	}
	beforeInode := inodeOf(beforeInfo)

	written, err := fs.WriteFile("A.txt", []byte("updated"), read.ModTime)
	if err != nil {
		t.Fatal(err)
	}
	if written.Path != "A.txt" || written.Size != int64(len("updated")) {
		t.Fatalf("unexpected write response: %+v", written)
	}
	content, err := os.ReadFile(filepath.Join(root, "A.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "updated" {
		t.Fatalf("unexpected saved content %q", content)
	}
	afterInfo, err := os.Stat(filepath.Join(root, "A.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && afterInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected original mode 0600, got %o", afterInfo.Mode().Perm())
	}
	if beforeInode != 0 && inodeOf(afterInfo) == beforeInode {
		t.Fatal("expected atomic rename to replace the original inode")
	}
	assertNoWorkspaceTemps(t, root)

	if _, err := fs.WriteFile("new.txt", []byte("new"), ""); err != nil {
		t.Fatal(err)
	}
	newInfo, err := os.Stat(filepath.Join(root, "new.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && newInfo.Mode().Perm() != 0o644 {
		t.Fatalf("expected new file mode 0644, got %o", newInfo.Mode().Perm())
	}
}

func TestTreeLimitsEntriesAndReturnsRelativeChildPaths(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	for i := 0; i < MaxTreeEntries+5; i++ {
		mustWrite(t, filepath.Join(root, "src", fmt.Sprintf("file-%03d.txt", i)), []byte("x"), 0o644)
	}
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := fs.Tree("src")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Path != "src" || len(tree.Entries) != MaxTreeEntries {
		t.Fatalf("unexpected limited tree path=%q entries=%d", tree.Path, len(tree.Entries))
	}
	if tree.Entries[0].Path != "src/file-000.txt" || tree.Entries[len(tree.Entries)-1].Path != "src/file-499.txt" {
		t.Fatalf("unexpected relative sorted range: first=%+v last=%+v", tree.Entries[0], tree.Entries[len(tree.Entries)-1])
	}
}

func TestSensitiveTraversalAndNonCanonicalPathsAreRejected(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, ".env.local"), []byte("secret"), 0o600)
	mustWrite(t, filepath.Join(root, "credentials.json"), []byte("secret"), 0o600)
	mustWrite(t, filepath.Join(root, "private.pem"), []byte("secret"), 0o600)
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env.local", "credentials.json", "private.pem"} {
		if _, err := fs.ReadFile(name); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected sensitive read rejection for %q, got %v", name, err)
		}
		if _, err := fs.WriteFile(name, []byte("changed"), ""); !errors.Is(err, ErrForbidden) {
			t.Fatalf("expected sensitive write rejection for %q, got %v", name, err)
		}
	}
	for _, name := range []string{"../outside", "dir/../file", "./file", "/absolute", "dir//file", `dir\file`} {
		if _, err := fs.ReadFile(name); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("expected invalid path for %q, got %v", name, err)
		}
	}
}

func TestSymlinkEscapesAreRejectedForFileAndParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside.txt")
	mustWrite(t, outsideFile, []byte("outside"), 0o644)
	if err := os.Symlink(outsideFile, filepath.Join(root, "file-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "parent-link")); err != nil {
		t.Fatal(err)
	}
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("file-link"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected file symlink escape rejection, got %v", err)
	}
	if _, err := fs.WriteFile("file-link", []byte("changed"), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected file symlink write rejection, got %v", err)
	}
	if _, err := fs.WriteFile("parent-link/new.txt", []byte("changed"), ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected parent symlink escape rejection, got %v", err)
	}
	if _, err := fs.Tree("parent-link"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected tree symlink escape rejection, got %v", err)
	}
	content, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside" {
		t.Fatalf("outside file was changed: %q", content)
	}
}

func TestBinaryPreviewLimitAndMaximumSize(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "binary.dat"), []byte{'a', 0, 'b'}, 0o644)
	previewData := []byte(strings.Repeat("p", PreviewBytes+17))
	mustWrite(t, filepath.Join(root, "preview.txt"), previewData, 0o644)
	mustWrite(t, filepath.Join(root, "large.txt"), []byte(strings.Repeat("l", MaxFileBytes+1)), 0o644)
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile("binary.dat"); !errors.Is(err, ErrBinary) {
		t.Fatalf("expected binary rejection, got %v", err)
	}
	if _, err := fs.WriteFile("new.dat", []byte{'a', 0, 'b'}, ""); !errors.Is(err, ErrBinary) {
		t.Fatalf("expected binary write rejection, got %v", err)
	}
	preview, err := fs.ReadFile("preview.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Truncated || !preview.ReadOnly || preview.Editable || len(preview.Content) != PreviewBytes || preview.Size != int64(len(previewData)) {
		t.Fatalf("unexpected preview response: size=%d content=%d readOnly=%v truncated=%v editable=%v", preview.Size, len(preview.Content), preview.ReadOnly, preview.Truncated, preview.Editable)
	}
	if _, err := fs.ReadFile("large.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected oversized read rejection, got %v", err)
	}
	if _, err := fs.WriteFile("too-large.txt", []byte(strings.Repeat("x", MaxFileBytes+1)), ""); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected oversized write rejection, got %v", err)
	}
}

func TestOptimisticLockRejectsStaleModTime(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "locked.txt")
	mustWrite(t, filename, []byte("before"), 0o644)
	fs, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	read, err := fs.ReadFile("locked.txt")
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filename, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.WriteFile("locked.txt", []byte("stale"), read.ModTime); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected optimistic lock conflict, got %v", err)
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "before" {
		t.Fatalf("stale write changed file: %q", content)
	}
}

func TestNewRequiresExistingDirectoryRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := New(filepath.Join(root, "missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing root error, got %v", err)
	}
	file := filepath.Join(root, "file")
	mustWrite(t, file, []byte("x"), 0o644)
	if _, err := New(file); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected non-directory root error, got %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func assertNoWorkspaceTemps(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".autoto-workspace-") {
			t.Fatalf("temporary file was not cleaned up: %s", entry.Name())
		}
	}
}

func inodeOf(info os.FileInfo) uint64 {
	sys := info.Sys()
	if sys == nil {
		return 0
	}
	value := reflect.ValueOf(sys)
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0
	}
	field := value.FieldByName("Ino")
	if !field.IsValid() || !field.CanUint() {
		return 0
	}
	return field.Uint()
}
