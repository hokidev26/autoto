package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/workspacefs"
)

func TestWorkspaceFilesTreeReadAndWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("before\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, agentID := newWorkspaceServer(t, root, "acceptEdits")

	treeRecorder := serveWorkspaceRequest(t, app, http.MethodGet, "/api/agents/"+agentID+"/workspace/tree", nil)
	if treeRecorder.Code != http.StatusOK {
		t.Fatalf("tree status=%d body=%s", treeRecorder.Code, treeRecorder.Body.String())
	}
	var tree workspacefs.Tree
	decodeWorkspaceResponse(t, treeRecorder, &tree)
	if tree.Path != "" || len(tree.Entries) != 2 {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	if tree.Entries[0].Name != "src" || tree.Entries[0].Path != "src" || !tree.Entries[0].IsDir {
		t.Fatalf("expected directory first with relative path: %+v", tree.Entries)
	}
	if tree.Entries[1].Name != "README.md" || tree.Entries[1].Path != "README.md" || !tree.Entries[1].Editable {
		t.Fatalf("expected editable file after directory: %+v", tree.Entries)
	}
	if strings.Contains(treeRecorder.Body.String(), root) || strings.Contains(treeRecorder.Body.String(), "node_modules") || strings.Contains(treeRecorder.Body.String(), ".env") {
		t.Fatalf("tree leaked root or filtered entries: %s", treeRecorder.Body.String())
	}

	readRecorder := serveWorkspaceRequest(t, app, http.MethodGet, "/api/agents/"+agentID+"/workspace/file?path=README.md", nil)
	if readRecorder.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%s", readRecorder.Code, readRecorder.Body.String())
	}
	var read workspacefs.File
	decodeWorkspaceResponse(t, readRecorder, &read)
	if read.Path != "README.md" || read.Content != "before\n" || read.ModTime == "" {
		t.Fatalf("unexpected read response: %+v", read)
	}
	if strings.Contains(readRecorder.Body.String(), root) {
		t.Fatalf("read response leaked workspace root: %s", readRecorder.Body.String())
	}

	body := workspaceFileWriteRequest{Path: "README.md", Content: "after\n", ExpectedModTime: read.ModTime}
	writeRecorder := serveWorkspaceJSON(t, app, http.MethodPut, "/api/agents/"+agentID+"/workspace/file", body)
	if writeRecorder.Code != http.StatusOK {
		t.Fatalf("write status=%d body=%s", writeRecorder.Code, writeRecorder.Body.String())
	}
	var written workspacefs.WriteResult
	decodeWorkspaceResponse(t, writeRecorder, &written)
	if written.Path != "README.md" || written.Size != int64(len("after\n")) || written.ModTime == "" {
		t.Fatalf("unexpected write response: %+v", written)
	}
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after\n" {
		t.Fatalf("unexpected saved content %q", data)
	}
	info, err := os.Stat(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("expected preserved mode 0640, got %o", info.Mode().Perm())
	}
}

func TestWorkspaceFilesErrorStatusMapping(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", workspacefs.MaxFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "locked.txt"), []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, agentID := newWorkspaceServer(t, root, "acceptEdits")

	tests := []struct {
		name   string
		method string
		path   string
		body   any
		status int
	}{
		{name: "traversal", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=" + url.QueryEscape("../outside"), status: http.StatusBadRequest},
		{name: "non canonical", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=" + url.QueryEscape("dir/../locked.txt"), status: http.StatusBadRequest},
		{name: "sensitive read", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=.env", status: http.StatusForbidden},
		{name: "missing", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=missing.txt", status: http.StatusNotFound},
		{name: "binary read", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=binary.dat", status: http.StatusBadRequest},
		{name: "oversized read", method: http.MethodGet, path: "/api/agents/" + agentID + "/workspace/file?path=large.txt", status: http.StatusRequestEntityTooLarge},
		{name: "sensitive write", method: http.MethodPut, path: "/api/agents/" + agentID + "/workspace/file", body: workspaceFileWriteRequest{Path: ".env", Content: "changed"}, status: http.StatusForbidden},
		{name: "binary write", method: http.MethodPut, path: "/api/agents/" + agentID + "/workspace/file", body: workspaceFileWriteRequest{Path: "new.dat", Content: "a\x00b"}, status: http.StatusBadRequest},
		{name: "oversized write", method: http.MethodPut, path: "/api/agents/" + agentID + "/workspace/file", body: workspaceFileWriteRequest{Path: "large-new.txt", Content: strings.Repeat("x", workspacefs.MaxFileBytes+1)}, status: http.StatusRequestEntityTooLarge},
		{name: "stale write", method: http.MethodPut, path: "/api/agents/" + agentID + "/workspace/file", body: workspaceFileWriteRequest{Path: "locked.txt", Content: "changed", ExpectedModTime: "2000-01-01T00:00:00Z"}, status: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var recorder *httptest.ResponseRecorder
			if test.body != nil {
				recorder = serveWorkspaceJSON(t, app, test.method, test.path, test.body)
			} else {
				recorder = serveWorkspaceRequest(t, app, test.method, test.path, nil)
			}
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), root) {
				t.Fatalf("error response leaked workspace root: %s", recorder.Body.String())
			}
		})
	}
}

func TestWorkspaceFileWriteHonorsReadOnlyPermission(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filename, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	app, agentID := newWorkspaceServer(t, root, "readOnly")
	recorder := serveWorkspaceJSON(t, app, http.MethodPut, "/api/agents/"+agentID+"/workspace/file", workspaceFileWriteRequest{Path: "file.txt", Content: "after"})
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before" {
		t.Fatalf("read-only write changed content: %q", data)
	}
}

func TestWorkspaceRoutesRemainBehindLocalRequestGuard(t *testing.T) {
	root := t.TempDir()
	app, agentID := newWorkspaceServer(t, root, "acceptEdits")
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agentID+"/workspace/tree", nil)
	request.Host = "localhost:7788"
	request.Header.Set("Origin", "http://localhost:7788")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected local request guard rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkspaceInvalidRootDoesNotLeakAgentCWD(t *testing.T) {
	root := t.TempDir()
	app, agentID := newWorkspaceServer(t, root, "acceptEdits")
	missing := filepath.Join(root, "private", "missing-workspace")
	if _, err := app.store.UpdateAgentCWD(context.Background(), agentID, missing); err != nil {
		t.Fatal(err)
	}
	recorder := serveWorkspaceRequest(t, app, http.MethodGet, "/api/agents/"+agentID+"/workspace/tree", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), missing) || strings.Contains(recorder.Body.String(), root) {
		t.Fatalf("response leaked agent cwd: %s", recorder.Body.String())
	}
}

func newWorkspaceServer(t *testing.T, root, permissionMode string) (*Server, string) {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, _, agent, err := store.CreateProject(context.Background(), "Workspace", "", root, "openai:test", permissionMode)
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)
	return app, agent.ID
}

func serveWorkspaceJSON(t *testing.T, app *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return serveWorkspaceRequest(t, app, method, target, bytes.NewReader(data))
}

func serveWorkspaceRequest(t *testing.T, app *Server, method, target string, body *bytes.Reader) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == nil {
		request = newTestRequest(method, target, nil)
	} else {
		request = newTestRequest(method, target, body)
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

func decodeWorkspaceResponse(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
}
