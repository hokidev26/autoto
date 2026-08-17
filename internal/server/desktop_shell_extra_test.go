package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	updatepkg "autoto/internal/update"
)

type stubLifecycleHost struct {
	enabled   bool
	lastURL   string
	openedURL string
}

func (h *stubLifecycleHost) AutostartStatus() (bool, string, string, error) {
	return h.enabled, "test", "", nil
}
func (h *stubLifecycleHost) AutostartEnable() error {
	h.enabled = true
	return nil
}
func (h *stubLifecycleHost) AutostartDisable() error {
	h.enabled = false
	return nil
}
func (h *stubLifecycleHost) NotifyDeepLink(raw string) error {
	h.lastURL = raw
	return nil
}
func (h *stubLifecycleHost) OpenExternalURL(raw string) error {
	h.openedURL = raw
	return nil
}

type stubUpdateHost struct {
	home string
}

func (h stubUpdateHost) StageLocalUpdate(sourcePath, version, sha256 string) (updatepkg.PendingReplace, error) {
	return updatepkg.StageLocalBinary(h.home, sourcePath, version, sha256)
}
func (h stubUpdateHost) PendingUpdate() (updatepkg.PendingReplace, bool, error) {
	return updatepkg.ReadPendingReplace(h.home)
}
func (h stubUpdateHost) ClearPendingUpdate() error {
	return updatepkg.ClearPendingReplace(h.home)
}

func withLocalShellToken(req *http.Request, app *Server) {
	req.RemoteAddr = "127.0.0.1:9"
	req.Host = "127.0.0.1:7788"
	req.Header.Set(localTokenHeader, app.localToken)
}

func TestDesktopAutostartLocalOnly(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	host := &stubLifecycleHost{}
	app.SetShellLifecycleHost(host)

	req := httptest.NewRequest(http.MethodPost, "/api/desktop/autostart", nil)
	withLocalShellToken(req, app)
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !host.enabled {
		t.Fatal("expected enabled")
	}

	// Missing token on loopback must fail (port-forward defense).
	noToken := httptest.NewRequest(http.MethodPost, "/api/desktop/autostart", nil)
	noToken.RemoteAddr = "127.0.0.1:9"
	noToken.Host = "127.0.0.1:7788"
	noTokenRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(noTokenRec, noToken)
	if noTokenRec.Code == http.StatusOK {
		t.Fatal("loopback without token must not control autostart")
	}

	remote := httptest.NewRequest(http.MethodPost, "/api/desktop/autostart", nil)
	remote.RemoteAddr = "203.0.113.9:9"
	remote.Host = "example.com"
	remote.Header.Set(localTokenHeader, app.localToken)
	remoteRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(remoteRec, remote)
	if remoteRec.Code == http.StatusOK {
		t.Fatal("remote must not control autostart")
	}
}

func TestDesktopDeepLinkRejectsNonAutoto(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	host := &stubLifecycleHost{}
	app.SetShellLifecycleHost(host)
	body, _ := json.Marshal(map[string]string{"url": "https://evil.example"})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/deep-link", bytes.NewReader(body))
	withLocalShellToken(req, app)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("expected reject")
	}

	body, _ = json.Marshal(map[string]string{"url": "autoto://agent?id=a1"})
	req = httptest.NewRequest(http.MethodPost, "/api/desktop/deep-link", bytes.NewReader(body))
	withLocalShellToken(req, app)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if host.lastURL != "autoto://agent?id=a1" {
		t.Fatalf("lastURL=%q", host.lastURL)
	}
}

func TestDesktopOpenURLLocalOnly(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	host := &stubLifecycleHost{}
	app.SetShellLifecycleHost(host)

	body, _ := json.Marshal(map[string]string{"url": "https://auth.openai.com/oauth/authorize?client_id=fixture"})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(body))
	withLocalShellToken(req, app)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(host.openedURL, "https://auth.openai.com/oauth/authorize") {
		t.Fatalf("openedURL=%q", host.openedURL)
	}

	fileBody, _ := json.Marshal(map[string]string{"url": "file:///etc/passwd"})
	fileReq := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(fileBody))
	withLocalShellToken(fileReq, app)
	fileReq.Header.Set("Content-Type", "application/json")
	fileRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(fileRec, fileReq)
	if fileRec.Code == http.StatusOK {
		t.Fatal("file URLs must not open")
	}

	jsBody, _ := json.Marshal(map[string]string{"url": "javascript:alert(1)"})
	jsReq := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(jsBody))
	withLocalShellToken(jsReq, app)
	jsReq.Header.Set("Content-Type", "application/json")
	jsRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(jsRec, jsReq)
	if jsRec.Code == http.StatusOK {
		t.Fatal("javascript URLs must not open")
	}

	noToken := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(body))
	noToken.RemoteAddr = "127.0.0.1:9"
	noToken.Host = "127.0.0.1:7788"
	noToken.Header.Set("Content-Type", "application/json")
	noTokenRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(noTokenRec, noToken)
	if noTokenRec.Code == http.StatusOK {
		t.Fatal("loopback without token must not open URLs")
	}

	remote := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(body))
	remote.RemoteAddr = "203.0.113.9:9"
	remote.Host = "example.com"
	remote.Header.Set(localTokenHeader, app.localToken)
	remote.Header.Set("Content-Type", "application/json")
	remoteRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(remoteRec, remote)
	if remoteRec.Code == http.StatusOK {
		t.Fatal("remote must not open URLs")
	}

	missing := New(config.Config{}, nil, nil, nil)
	orphan := httptest.NewRequest(http.MethodPost, "/api/desktop/open-url", bytes.NewReader(body))
	withLocalShellToken(orphan, missing)
	orphan.Header.Set("Content-Type", "application/json")
	orphanRec := httptest.NewRecorder()
	missing.Routes().ServeHTTP(orphanRec, orphan)
	if orphanRec.Code != http.StatusNotFound {
		t.Fatalf("CLI/browser process must 404 open-url, got %d", orphanRec.Code)
	}
}

func TestDesktopUpdateStageLocalOnly(t *testing.T) {
	home := t.TempDir()
	app := New(config.Config{}, nil, nil, nil)
	app.SetShellUpdateHost(stubUpdateHost{home: home})

	src := filepath.Join(t.TempDir(), "bin")
	payload := []byte("payload")
	if err := os.WriteFile(src, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	hexSum := hex.EncodeToString(sum[:])
	bodyPayload, _ := json.Marshal(map[string]string{
		"sourcePath": src,
		"version":    "0.2.0",
		"sha256":     hexSum,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/desktop/update/stage", bytes.NewReader(bodyPayload))
	withLocalShellToken(req, app)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["pending"] != true || body["apply"] != "not_implemented" {
		t.Fatalf("body=%v", body)
	}

	// Missing SHA rejected.
	noSHA, _ := json.Marshal(map[string]string{
		"sourcePath": src,
		"version":    "0.2.1",
	})
	missing := httptest.NewRequest(http.MethodPost, "/api/desktop/update/stage", bytes.NewReader(noSHA))
	withLocalShellToken(missing, app)
	missing.Header.Set("Content-Type", "application/json")
	missingRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(missingRec, missing)
	if missingRec.Code == http.StatusOK {
		t.Fatal("staging without sha256 must fail")
	}

	// Remote cannot stage.
	remote := httptest.NewRequest(http.MethodPost, "/api/desktop/update/stage", bytes.NewReader(bodyPayload))
	remote.RemoteAddr = "198.51.100.2:1"
	remote.Host = "tunnel.example"
	remote.Header.Set(localTokenHeader, app.localToken)
	remote.Header.Set("Content-Type", "application/json")
	remoteRec := httptest.NewRecorder()
	app.Routes().ServeHTTP(remoteRec, remote)
	if remoteRec.Code == http.StatusOK {
		t.Fatal("remote must not stage updates")
	}
}
