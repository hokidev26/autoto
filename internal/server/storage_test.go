package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestStorageSummaryRouteReturnsConfiguredPathStats(t *testing.T) {
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(filepath.Join(projectDir, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(homeDir, "config.json")
	databasePath := filepath.Join(homeDir, "autoto.db")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"server":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(databasePath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "demo", "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := db.Open(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Paths: config.PathsConfig{HomeDir: homeDir, DatabasePath: databasePath, DefaultProjectDir: projectDir}}, store, nil, nil)
	app.SetConfigPath(configPath)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/storage/summary", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body storageSummaryResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.TotalKnownBytes <= 0 || len(body.Entries) != 4 {
		t.Fatalf("unexpected storage summary: %+v", body)
	}
	database := storageEntryByKey(body.Entries, "database")
	if !database.Exists || database.FileCount != 2 || database.SizeBytes != 5 {
		t.Fatalf("unexpected database entry: %+v", database)
	}
	projects := storageEntryByKey(body.Entries, "projects")
	if !projects.Exists || !projects.IsDir || projects.FileCount != 1 || projects.DirectoryCount < 2 {
		t.Fatalf("unexpected projects entry: %+v", projects)
	}
	configEntry := storageEntryByKey(body.Entries, "config")
	if !configEntry.Exists || configEntry.FileCount != 1 {
		t.Fatalf("unexpected config entry: %+v", configEntry)
	}
}

func TestBuildStorageSummaryTruncatesLargeDirectoryScan(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(projectDir, db.NewID()+".txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	summary := buildStorageSummary(context.Background(), config.Config{Paths: config.PathsConfig{DefaultProjectDir: projectDir}}, "", 3)
	projects := storageEntryByKey(summary.Entries, "projects")
	if !projects.Truncated {
		t.Fatalf("expected truncated projects scan, got %+v", projects)
	}
	if projects.EntriesScanned > 3 {
		t.Fatalf("expected bounded scan, got %+v", projects)
	}
}

func storageEntryByKey(entries []storageEntry, key string) storageEntry {
	for _, entry := range entries {
		if entry.Key == key {
			return entry
		}
	}
	return storageEntry{}
}

// TestBuildStorageSummaryDeduplicatesNestedPaths verifies that bytes counted
// inside HomeDir are not double-counted when the database or config file lives
// inside that same directory.
func TestBuildStorageSummaryDeduplicatesNestedPaths(t *testing.T) {
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Database and config are nested inside homeDir.
	databasePath := filepath.Join(homeDir, "autoto.db")
	configPath := filepath.Join(homeDir, "config.json")
	if err := os.WriteFile(databasePath, []byte("db-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Paths: config.PathsConfig{
		HomeDir:      homeDir,
		DatabasePath: databasePath,
	}}
	summary := buildStorageSummary(context.Background(), cfg, configPath, storageScanLimit)

	homeEntry := storageEntryByKey(summary.Entries, "home")
	dbEntry := storageEntryByKey(summary.Entries, "database")
	cfgEntry := storageEntryByKey(summary.Entries, "config")

	// Individual entries must still report their own sizes.
	if !homeEntry.Exists || homeEntry.SizeBytes <= 0 {
		t.Fatalf("home entry: %+v", homeEntry)
	}
	if !dbEntry.Exists || dbEntry.SizeBytes <= 0 {
		t.Fatalf("database entry: %+v", dbEntry)
	}
	if !cfgEntry.Exists || cfgEntry.SizeBytes <= 0 {
		t.Fatalf("config entry: %+v", cfgEntry)
	}

	// Total must not exceed homeDir bytes (db and config are inside it).
	if summary.TotalKnownBytes > homeEntry.SizeBytes {
		t.Fatalf("TotalKnownBytes %d exceeds homeDir %d (nested paths double-counted)",
			summary.TotalKnownBytes, homeEntry.SizeBytes)
	}
}

// TestBuildStorageSummaryNonNestedPathsAccumulate verifies that entries with
// completely separate roots all contribute to the total.
func TestBuildStorageSummaryNonNestedPathsAccumulate(t *testing.T) {
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	projectDir := filepath.Join(root, "projects")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, "a.txt"), []byte("home"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "b.txt"), []byte("proj"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Paths: config.PathsConfig{
		HomeDir:           homeDir,
		DefaultProjectDir: projectDir,
	}}
	summary := buildStorageSummary(context.Background(), cfg, "", storageScanLimit)

	homeEntry := storageEntryByKey(summary.Entries, "home")
	projEntry := storageEntryByKey(summary.Entries, "projects")
	if homeEntry.SizeBytes <= 0 || projEntry.SizeBytes <= 0 {
		t.Fatalf("expected both entries to have bytes: home=%+v projects=%+v", homeEntry, projEntry)
	}
	if summary.TotalKnownBytes < homeEntry.SizeBytes+projEntry.SizeBytes {
		t.Fatalf("TotalKnownBytes %d less than home+projects %d",
			summary.TotalKnownBytes, homeEntry.SizeBytes+projEntry.SizeBytes)
	}
}

// TestIsNestedPath covers the helper used for deduplication.
func TestIsNestedPath(t *testing.T) {
	sep := string(filepath.Separator)
	parent := filepath.Join("a", "b")
	cases := []struct {
		child string
		want  bool
	}{
		{parent, true},
		{parent + sep + "c", true},
		{parent + sep + "c" + sep + "d", true},
		{filepath.Join("a", "bc"), false}, // shares prefix but not a child
		{filepath.Join("a"), false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isNestedPath(tc.child, parent); got != tc.want {
			t.Errorf("isNestedPath(%q, %q) = %v, want %v", tc.child, parent, got, tc.want)
		}
	}
}

// TestBuildStorageSummaryWithCacheReturnsCachedResult confirms that a second
// call with the same paths within TTL reuses the previous result without
// touching the disk (GeneratedAt stays the same).
func TestBuildStorageSummaryWithCacheReturnsCachedResult(t *testing.T) {
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := db.Open(context.Background(), filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cfg := config.Config{Paths: config.PathsConfig{HomeDir: homeDir}}
	app := New(cfg, store, nil, nil)

	first := app.buildStorageSummaryWithCache(context.Background(), cfg, "", storageScanLimit)
	second := app.buildStorageSummaryWithCache(context.Background(), cfg, "", storageScanLimit)

	if first.GeneratedAt != second.GeneratedAt {
		t.Fatalf("cache miss: first=%s second=%s", first.GeneratedAt, second.GeneratedAt)
	}
}
