//go:build desktop && windows

package desktop

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeLegacyProfile(t *testing.T, appData, exeName, contents string, modTime time.Time) string {
	t.Helper()
	dir := filepath.Join(appData, exeName, "EBWebView", "Default", "Local Storage", "leveldb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "000005.ldb"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	// LOCK/CURRENT are database bookkeeping, not records: copying them would
	// import another profile's lock state, so the migration must leave them behind.
	for _, name := range []string{"LOCK", "CURRENT", "MANIFEST-000001"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bookkeeping"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filepath.Join(appData, exeName), modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The bug this covers: WebView2 derives its data directory from the executable
// filename, so shipping autoto-desktop-<build>.exe handed every new build an
// empty localStorage and with it a sidebar where every finished conversation
// looked unread again.
func TestStableWebviewUserDataPathIsIndependentOfExecutableName(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	path := stableWebviewUserDataPath(quietLogger())

	want := filepath.Join(appData, "Autoto", "WebView")
	if path != want {
		t.Fatalf("profile path = %q, want %q", path, want)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("profile directory not created: %v", err)
	}
}

func TestStableWebviewUserDataPathMigratesNewestSeenState(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)

	older := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	writeLegacyProfile(t, appData, "autoto-desktop-integration-old.exe", `{"autoto.conversationSeen":"old"}`, older)
	writeLegacyProfile(t, appData, "autoto-desktop-integration-new.exe", `{"autoto.conversationSeen":"new"}`, newer)
	// A profile that never stored a seen map is not a migration source: picking it
	// because it happened to be newest would import an empty read state.
	writeLegacyProfile(t, appData, "autoto-desktop-integration-empty.exe", `{"autoto.somethingElse":1}`, time.Now())

	path := stableWebviewUserDataPath(quietLogger())

	migrated := filepath.Join(path, "Default", "Local Storage", "leveldb", "000005.ldb")
	data, err := os.ReadFile(migrated)
	if err != nil {
		t.Fatalf("seen state was not migrated: %v", err)
	}
	if string(data) != `{"autoto.conversationSeen":"new"}` {
		t.Fatalf("migrated the wrong profile: %s", data)
	}
	for _, name := range []string{"LOCK", "CURRENT", "MANIFEST-000001"} {
		if _, err := os.Stat(filepath.Join(path, "Default", "Local Storage", "leveldb", name)); !os.IsNotExist(err) {
			t.Fatalf("%s must not be copied from another profile (err=%v)", name, err)
		}
	}
}

// The source profile belongs to a build the user may still run, so the migration
// reads it and leaves it exactly as it was.
func TestStableWebviewUserDataPathLeavesLegacyProfileIntact(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	legacy := writeLegacyProfile(t, appData, "autoto-desktop-integration-old.exe", `{"autoto.conversationSeen":"old"}`, time.Now())

	stableWebviewUserDataPath(quietLogger())

	data, err := os.ReadFile(filepath.Join(legacy, "000005.ldb"))
	if err != nil || string(data) != `{"autoto.conversationSeen":"old"}` {
		t.Fatalf("legacy profile was modified: %s (err=%v)", data, err)
	}
}

// Once the stable profile has its own records, the old one is history: copying
// into a live LevelDB would overwrite newer marks with older ones.
func TestStableWebviewUserDataPathDoesNotOverwriteExistingProfile(t *testing.T) {
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	writeLegacyProfile(t, appData, "autoto-desktop-integration-old.exe", `{"autoto.conversationSeen":"old"}`, time.Now())

	target := filepath.Join(appData, "Autoto", "WebView", "Default", "Local Storage", "leveldb")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "000005.ldb"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}

	stableWebviewUserDataPath(quietLogger())

	data, err := os.ReadFile(filepath.Join(target, "000005.ldb"))
	if err != nil || string(data) != "current" {
		t.Fatalf("existing profile was overwritten: %s (err=%v)", data, err)
	}
}

// Without APPDATA there is no per-user location to name, and an empty string
// leaves Wails' own default in place rather than pointing WebView2 at a path it
// would reject with a modal error box.
func TestStableWebviewUserDataPathFallsBackWhenAppDataMissing(t *testing.T) {
	t.Setenv("APPDATA", "")

	if path := stableWebviewUserDataPath(quietLogger()); path != "" {
		t.Fatalf("expected empty fallback, got %q", path)
	}
}
