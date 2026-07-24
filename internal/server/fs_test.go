package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
)

func TestFSOperationsRejectSymlinkEscape(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	// Exposed so that local HTTP requests stay project-scoped and the symlink
	// escape protection applies (a non-exposed local server may browse the host).
	app := New(config.Config{Paths: config.PathsConfig{DefaultProjectDir: project}, Security: config.SecurityConfig{Exposed: true}}, nil, nil, nil)
	for _, path := range []string{"link/secret.txt", "link/new-directory"} {
		if _, err := app.resolveFSPath(path); err == nil || !strings.Contains(err.Error(), "escapes default project directory") {
			t.Fatalf("expected symlink escape rejection for %q, got %v", path, err)
		}
	}

	browse := httptest.NewRecorder()
	app.fsBrowse(browse, newTestRequest(http.MethodGet, "/api/fs/browse?path=.", nil))
	if browse.Code != http.StatusOK || strings.Contains(browse.Body.String(), "secret.txt") {
		t.Fatalf("browse must hide external symlink target, code=%d body=%s", browse.Code, browse.Body.String())
	}

	mkdir := httptest.NewRecorder()
	app.fsMkdir(mkdir, newTestRequest(http.MethodPost, "/api/fs/mkdir", strings.NewReader(`{"path":"link/new-directory"}`)))
	if mkdir.Code != http.StatusBadRequest {
		t.Fatalf("expected mkdir symlink rejection, got %d: %s", mkdir.Code, mkdir.Body.String())
	}
	if _, err := os.Stat(filepath.Join(outside, "new-directory")); !os.IsNotExist(err) {
		t.Fatalf("mkdir escaped through symlink, stat err=%v", err)
	}
}

func TestFSPreviewBoundsLargeFiles(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "large.txt"), []byte(strings.Repeat("x", 256*1024+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Paths: config.PathsConfig{DefaultProjectDir: project}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	app.fsPreview(recorder, newTestRequest(http.MethodGet, "/api/fs/preview?path=large.txt", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Truncated || len(body.Text) != 256*1024 {
		t.Fatalf("expected bounded preview, truncated=%v length=%d", body.Truncated, len(body.Text))
	}
}

func TestFSDirectoriesRestrictsRemoteRootEnumeration(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	app := New(config.Config{Paths: config.PathsConfig{DefaultProjectDir: project}, Security: config.SecurityConfig{Exposed: true}}, nil, nil, nil)
	recorder := httptest.NewRecorder()
	app.fsDirectories(recorder, newTestRequest(http.MethodGet, "/api/fs/directories?path="+outside, nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected remote root restriction, got %d: %s", recorder.Code, recorder.Body.String())
	}
}
