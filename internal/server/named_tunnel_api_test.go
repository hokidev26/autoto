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

func localNamedTunnelRequest(t *testing.T, app *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, path, strings.NewReader(body))
	request.Host = "localhost:7788"
	request.RemoteAddr = "127.0.0.1:4321"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

// Configuring a named tunnel changes how this machine is reachable from the
// internet, so it must be host-local only. A remote session, including a full
// one, must not be able to point the tunnel at a hostname of its choosing.
func TestNamedTunnelMutationRequiresHostLocalAuthority(t *testing.T) {
	app := remoteAccessTestServer(t)
	body := `{"hostname":"autoto.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`

	for name, mode := range map[string]string{
		"restricted": remoteAccessModeRestricted,
		"full":       remoteAccessModeFull,
	} {
		cookies := loginRemoteAccess(t, app, mode)
		request := newTestRequest(http.MethodPut, "/api/security/named-tunnel/remote-access", strings.NewReader(body))
		request.Host = "remote.example.test"
		markRemoteHTTPS(request)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s remote session changed the named tunnel, got %d: %s", name, recorder.Code, recorder.Body.String())
		}
	}

	if cfg := app.configSnapshot(); cfg.Security.NamedTunnel.Configured() {
		t.Fatal("a rejected remote request must not have stored a named tunnel")
	}
}

// A pasted token must be rejected at the boundary. Accepting one would write
// credential material into config.json, which is what the env: reference exists
// to prevent.
func TestNamedTunnelAPIRejectsPlaintextToken(t *testing.T) {
	app := remoteAccessTestServer(t)
	const token = "eyJhIjoiYiIsInMiOiJjIn0"
	recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"autoto.example.com","tokenRef":"`+token+`"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected a rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("response echoed the token: %s", recorder.Body.String())
	}
	if cfg := app.configSnapshot(); cfg.Security.NamedTunnel.TokenRef != "" {
		t.Fatalf("a plaintext token was stored: %q", cfg.Security.NamedTunnel.TokenRef)
	}
}

func TestNamedTunnelAPIStoresReferenceAndNeverReturnsTokenValue(t *testing.T) {
	const token = "fake-token-value-must-not-appear"
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", token)
	app := remoteAccessTestServer(t)

	recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"Autoto.Example.COM","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected the tunnel to be saved, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), token) {
		t.Fatalf("response disclosed the token value: %s", recorder.Body.String())
	}

	var response namedTunnelResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Hostname != "autoto.example.com" {
		t.Fatalf("hostname was not normalized: %q", response.Hostname)
	}
	if response.TokenRef != "env:AUTOTO_TEST_TUNNEL_TOKEN" {
		t.Fatalf("unexpected token reference: %q", response.TokenRef)
	}
	if !response.Configured || !response.TokenAvailable {
		t.Fatalf("expected a configured tunnel with a resolvable token: %+v", response)
	}

	// The value must not reach disk either.
	persisted := loadPersistedConfig(t, app)
	if persisted.Security.NamedTunnel.TokenRef != "env:AUTOTO_TEST_TUNNEL_TOKEN" {
		t.Fatalf("reference was not persisted: %q", persisted.Security.NamedTunnel.TokenRef)
	}
	raw := readPersistedConfigBytes(t, app)
	if strings.Contains(raw, token) {
		t.Fatal("the token value was written to config.json")
	}
}

// Auto-start makes the machine reachable from boot, so it must not be armable
// while no access password exists. Otherwise a restart would publish an
// unauthenticated UI.
func TestNamedTunnelAutoStartRequiresAccessPassword(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	app := namedTunnelServerWithoutPassword(t)

	recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"autoto.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN","autoStart":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected auto-start to be refused, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if cfg := app.configSnapshot(); cfg.Security.NamedTunnel.AutoStart {
		t.Fatal("auto-start was enabled without an access password")
	}

	// The same tunnel without auto-start is fine: it only opens when asked.
	recorder = localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"autoto.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected a manual-start tunnel to be accepted, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// The two tunnels front different listeners, so writing one must not disturb the
// other. Sharing a hostname would defeat the reason they are separate: the API
// URL can be handed out without exposing the management UI.
func TestNamedTunnelSectionsAreIndependent(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	app := remoteAccessTestServer(t)

	if recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"ui.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`); recorder.Code != http.StatusOK {
		t.Fatalf("save remote access tunnel: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/shared-api",
		`{"hostname":"api.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`); recorder.Code != http.StatusOK {
		t.Fatalf("save shared API tunnel: %d %s", recorder.Code, recorder.Body.String())
	}

	cfg := app.configSnapshot()
	if cfg.Security.NamedTunnel.Hostname != "ui.example.com" {
		t.Fatalf("remote access hostname was disturbed: %q", cfg.Security.NamedTunnel.Hostname)
	}
	if cfg.Gateway.NamedTunnel.Hostname != "api.example.com" {
		t.Fatalf("shared API hostname was not stored: %q", cfg.Gateway.NamedTunnel.Hostname)
	}

	recorder := localNamedTunnelRequest(t, app, http.MethodGet, "/api/security/named-tunnel", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read named tunnel settings: %d %s", recorder.Code, recorder.Body.String())
	}
	var settings struct {
		RemoteAccess namedTunnelResponse `json:"remoteAccess"`
		SharedAPI    namedTunnelResponse `json:"sharedApi"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if settings.RemoteAccess.Hostname != "ui.example.com" || settings.SharedAPI.Hostname != "api.example.com" {
		t.Fatalf("unexpected settings payload: %+v", settings)
	}
}

// Clearing both fields turns the named tunnel off and returns to the quick
// tunnel, which must stay possible without deleting config by hand.
func TestNamedTunnelCanBeCleared(t *testing.T) {
	t.Setenv("AUTOTO_TEST_TUNNEL_TOKEN", "fake-token-value")
	app := remoteAccessTestServer(t)

	if recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"autoto.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN","autoStart":true}`); recorder.Code != http.StatusOK {
		t.Fatalf("save tunnel: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access",
		`{"hostname":"","tokenRef":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("clear tunnel: %d %s", recorder.Code, recorder.Body.String())
	}
	cfg := app.configSnapshot()
	if cfg.Security.NamedTunnel.Configured() || cfg.Security.NamedTunnel.AutoStart {
		t.Fatalf("tunnel was not cleared: %+v", cfg.Security.NamedTunnel)
	}
}

func TestNamedTunnelAPIRejectsIncompleteConfiguration(t *testing.T) {
	app := remoteAccessTestServer(t)
	for _, body := range []string{
		`{"hostname":"autoto.example.com","tokenRef":""}`,
		`{"hostname":"","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`,
		`{"hostname":"localhost","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`,
		`{"hostname":"https://autoto.example.com","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`,
		`{"hostname":"autoto.example.com:8443","tokenRef":"env:AUTOTO_TEST_TUNNEL_TOKEN"}`,
	} {
		recorder := localNamedTunnelRequest(t, app, http.MethodPut, "/api/security/named-tunnel/remote-access", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s was accepted with %d: %s", body, recorder.Code, recorder.Body.String())
		}
	}
}

func loadPersistedConfig(t *testing.T, app *Server) config.Config {
	t.Helper()
	persisted, err := config.Load(app.configPathSnapshot())
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	return persisted
}

// readPersistedConfigBytes reads config.json as text. Checking the raw file is
// the only way to prove a token value never reached disk: a decoded struct would
// look identical whether the reference or the value was stored.
func readPersistedConfigBytes(t *testing.T, app *Server) string {
	t.Helper()
	data, err := os.ReadFile(app.configPathSnapshot())
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	return string(data)
}

func namedTunnelServerWithoutPassword(t *testing.T) *Server {
	t.Helper()
	cfg := config.Config{}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(cfg, nil, nil, nil)
	app.SetConfigPath(path)
	return app
}
