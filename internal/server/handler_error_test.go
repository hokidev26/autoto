package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"autoto/internal/config"
)

func TestWriteRequestErrorGatesRemoteDetail(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	leak := "sql: SELECT * FROM agents WHERE path = '/var/lib/autoto/secret.db'"
	cause := errors.New(leak)

	localRec := httptest.NewRecorder()
	localReq := newTestRequest(http.MethodGet, "/api/agents/demo", nil)
	app.writeRequestError(localRec, localReq, http.StatusInternalServerError, cause)
	if got := decodeErrorMessage(t, localRec); got != leak {
		t.Fatalf("loopback request want raw detail %q, got %q", leak, got)
	}

	remoteRec := httptest.NewRecorder()
	remoteReq := newTestRequest(http.MethodGet, "/api/agents/demo", nil)
	remoteReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remoteReq)
	if !app.remoteAccessAuthentication(remoteReq).Remote {
		t.Fatal("test request must be classified as remote")
	}
	app.writeRequestError(remoteRec, remoteReq, http.StatusInternalServerError, cause)
	if remoteRec.Code != http.StatusInternalServerError {
		t.Fatalf("remote status=%d want %d", remoteRec.Code, http.StatusInternalServerError)
	}
	got := decodeErrorMessage(t, remoteRec)
	if got != "internal error" {
		t.Fatalf("remote request want generic message, got %q", got)
	}
	if strings.Contains(remoteRec.Body.String(), leak) || strings.Contains(got, "SELECT") || strings.Contains(got, "secret.db") {
		t.Fatalf("remote session leaked internal detail: %s", remoteRec.Body.String())
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	small := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ok":true}`))
	var smallDst struct {
		OK bool `json:"ok"`
	}
	if err := decodeJSON(small, &smallDst); err != nil || !smallDst.OK {
		t.Fatalf("small JSON body should decode, err=%v dst=%+v", err, smallDst)
	}

	payload := `{"x":"` + strings.Repeat("a", 257<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(payload))
	var dst struct {
		X string `json:"x"`
	}
	if err := decodeJSON(req, &dst); err == nil {
		t.Fatal("expected decodeJSON to reject a 257 KiB JSON body")
	}
}

func TestWriteGitErrorGatesRemotePathDetail(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	cause := gitCommandError{
		Status: http.StatusConflict,
		Msg:    `"git" status --porcelain failed in "C:\\Users\\Ray\\secret-project" (exit 128): not a git repository`,
	}

	localRec := httptest.NewRecorder()
	localReq := newTestRequest(http.MethodGet, "/api/git/status", nil)
	app.writeGitError(localRec, localReq, cause)
	if got := decodeErrorMessage(t, localRec); got != cause.Msg {
		t.Fatalf("loopback git error want raw detail, got %q", got)
	}

	remoteRec := httptest.NewRecorder()
	remoteReq := newTestRequest(http.MethodGet, "/api/git/status", nil)
	remoteReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remoteReq)
	app.writeGitError(remoteRec, remoteReq, cause)
	if remoteRec.Code != http.StatusConflict {
		t.Fatalf("remote status=%d want %d", remoteRec.Code, http.StatusConflict)
	}
	got := decodeErrorMessage(t, remoteRec)
	if got != "conflict" {
		t.Fatalf("remote git error want generic conflict, got %q", got)
	}
	if strings.Contains(remoteRec.Body.String(), "secret-project") || strings.Contains(remoteRec.Body.String(), "porcelain") {
		t.Fatalf("remote session leaked git path or argv: %s", remoteRec.Body.String())
	}
}

func TestWriteAPIErrorGatesRemoteGitDetail(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	cause := gitCommandError{
		Status: http.StatusInternalServerError,
		Msg:    `"git" diff failed in "/var/lib/autoto/work" (exit 1): fatal`,
	}

	remoteRec := httptest.NewRecorder()
	remoteReq := newTestRequest(http.MethodGet, "/api/agents/demo", nil)
	remoteReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remoteReq)
	app.writeAPIError(remoteRec, remoteReq, cause)
	if decodeErrorMessage(t, remoteRec) != "internal error" {
		t.Fatalf("remote API git error want generic message, got %q", decodeErrorMessage(t, remoteRec))
	}
	if strings.Contains(remoteRec.Body.String(), "/var/lib/autoto") {
		t.Fatalf("remote session leaked git path: %s", remoteRec.Body.String())
	}
}

func TestWriteAPIErrorGatesRemoteAPIDetail(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	cause := apiErr(http.StatusInternalServerError, `merge was undone in git but the workline record could not be updated: open C:\Users\Ray\secret-project`)

	localRec := httptest.NewRecorder()
	localReq := newTestRequest(http.MethodPost, "/api/worklines/demo/unmerge", nil)
	app.writeAPIError(localRec, localReq, cause)
	if got := decodeErrorMessage(t, localRec); !strings.Contains(got, "secret-project") {
		t.Fatalf("loopback apiError want raw detail, got %q", got)
	}

	remoteRec := httptest.NewRecorder()
	remoteReq := newTestRequest(http.MethodPost, "/api/worklines/demo/unmerge", nil)
	remoteReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remoteReq)
	app.writeAPIError(remoteRec, remoteReq, cause)
	if decodeErrorMessage(t, remoteRec) != "internal error" {
		t.Fatalf("remote apiError want generic message, got %q", decodeErrorMessage(t, remoteRec))
	}
	if strings.Contains(remoteRec.Body.String(), "secret-project") {
		t.Fatalf("remote session leaked apiError detail: %s", remoteRec.Body.String())
	}
}

func TestWriteNoGitRepoErrorOmitsPathForRemote(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	path := `C:\Users\Ray\secret-project`

	localRec := httptest.NewRecorder()
	localReq := newTestRequest(http.MethodPost, "/api/worklines/demo/fork", nil)
	app.writeNoGitRepoError(localRec, localReq, path)
	if got := decodeErrorMessage(t, localRec); !strings.Contains(got, path) {
		t.Fatalf("loopback git-repo error want path, got %q", got)
	}

	remoteRec := httptest.NewRecorder()
	remoteReq := newTestRequest(http.MethodPost, "/api/worklines/demo/fork", nil)
	remoteReq.Host = "demo.trycloudflare.com"
	markRemoteHTTPS(remoteReq)
	app.writeNoGitRepoError(remoteRec, remoteReq, path)
	if decodeErrorMessage(t, remoteRec) != "invalid request" {
		t.Fatalf("remote git-repo error want generic message, got %q", decodeErrorMessage(t, remoteRec))
	}
	var payload map[string]any
	if err := json.Unmarshal(remoteRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["code"] != "no_git_repo" {
		t.Fatalf("remote git-repo error should keep stable code, got %#v", payload)
	}
	if payload["path"] != nil || strings.Contains(remoteRec.Body.String(), "secret-project") {
		t.Fatalf("remote session leaked git path: %s", remoteRec.Body.String())
	}
}

func decodeErrorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	message, _ := payload["error"].(string)
	return message
}
