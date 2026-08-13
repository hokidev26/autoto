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

func decodeErrorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	message, _ := payload["error"].(string)
	return message
}
