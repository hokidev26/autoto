package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

type fakePluginService struct {
	plugins       map[string]db.Plugin
	configured    map[string]map[string]bool
	discoverErr   error
	uninstallPath string
}

func (f *fakePluginService) Install(_ context.Context, rootPath string) (db.Plugin, error) {
	if rootPath == "bad" {
		return db.Plugin{}, errors.New("invalid plugin manifest")
	}
	for _, plugin := range f.plugins {
		if plugin.RootPath == rootPath {
			return db.Plugin{}, db.ErrConflict
		}
	}
	plugin := db.Plugin{
		ID: "plugin-1", Slug: "safe-plugin", Name: "Safe <Plugin>", Version: "1.0.0",
		Description: "local plugin", ManifestVersion: "autoto.dev/v1alpha1", RootPath: rootPath,
		Command: "bin/plugin", Args: []string{"--stdio"}, Env: map[string]string{"MODE": "private-value"},
		SecretRefs: map[string]string{"API_TOKEN": "env:AUTOTO_PLUGIN_TEST_SUPER_SECRET"}, Enabled: false,
		Status: "disabled", Revision: 1, CreatedAt: "2025-01-01T00:00:00Z", UpdatedAt: "2025-01-01T00:00:00Z",
	}
	f.plugins[plugin.ID] = plugin
	f.configured[plugin.ID] = map[string]bool{"MODE": true, "API_TOKEN": true}
	return plugin, nil
}

func (f *fakePluginService) List(_ context.Context) ([]db.Plugin, error) {
	out := make([]db.Plugin, 0, len(f.plugins))
	for _, plugin := range f.plugins {
		out = append(out, plugin)
	}
	return out, nil
}

func (f *fakePluginService) Get(_ context.Context, id string) (db.Plugin, error) {
	plugin, ok := f.plugins[id]
	if !ok {
		return db.Plugin{}, sql.ErrNoRows
	}
	return plugin, nil
}

func (f *fakePluginService) Enable(ctx context.Context, id string) (db.Plugin, error) {
	plugin, err := f.Get(ctx, id)
	if err != nil {
		return db.Plugin{}, err
	}
	if !f.configured[id]["API_TOKEN"] {
		return db.Plugin{}, errors.New("plugin secret is not configured")
	}
	plugin.Enabled, plugin.Status = true, "enabled"
	f.plugins[id] = plugin
	return plugin, nil
}

func (f *fakePluginService) Disable(ctx context.Context, id string) (db.Plugin, error) {
	plugin, err := f.Get(ctx, id)
	if err != nil {
		return db.Plugin{}, err
	}
	plugin.Enabled, plugin.Status = false, "disabled"
	f.plugins[id] = plugin
	return plugin, nil
}

func (f *fakePluginService) Discover(ctx context.Context, id string) ([]db.PluginTool, error) {
	plugin, err := f.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !plugin.Enabled {
		return nil, errors.New("plugin is disabled")
	}
	if f.discoverErr != nil {
		return nil, f.discoverErr
	}
	return []db.PluginTool{{PluginID: id, RemoteName: "echo", ExposedName: "plugin__safe-plugin__echo", InputSchemaJSON: json.RawMessage(`{"type":"object"}`)}}, nil
}

func (f *fakePluginService) HasTool(_ context.Context, name string) (bool, error) {
	plugin, ok := f.plugins["plugin-1"]
	return ok && plugin.Enabled && name == "plugin__safe-plugin__echo", nil
}

func (f *fakePluginService) Uninstall(_ context.Context, id string) error {
	plugin, ok := f.plugins[id]
	if !ok {
		return sql.ErrNoRows
	}
	f.uninstallPath = plugin.RootPath
	delete(f.plugins, id)
	return nil
}

func (f *fakePluginService) ConfiguredEnvironment(_ context.Context, plugin db.Plugin) (map[string]bool, error) {
	return f.configured[plugin.ID], nil
}

func TestPluginRoutesRequireSensitiveLocalToken(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(&fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}})
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/plugins"},
		{http.MethodPost, "/api/plugins/install"},
		{http.MethodGet, "/api/plugins/missing"},
		{http.MethodPost, "/api/plugins/missing/enable"},
		{http.MethodPost, "/api/plugins/missing/disable"},
		{http.MethodPost, "/api/plugins/missing/discover"},
		{http.MethodDelete, "/api/plugins/missing"},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			app.Routes().ServeHTTP(recorder, newTestRequest(route.method, route.path, strings.NewReader(`{"rootPath":"/tmp/plugin"}`)))
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401 without sensitive local token, got %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
	legacyRequest := newTestRequest(http.MethodGet, "/api/plugins", nil)
	legacyRequest.Header.Set(nonCanonicalLocalTokenHeader, app.localToken)
	legacyRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(legacyRecorder, legacyRequest)
	if legacyRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("sensitive plugin routes must reject the legacy local token header, got %d: %s", legacyRecorder.Code, legacyRecorder.Body.String())
	}
}

func TestPluginInstallDefaultsDisabledAndSanitizesSecrets(t *testing.T) {
	root := t.TempDir()
	service := &fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}}
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(service)

	body, _ := json.Marshal(pluginInstallPayload{RootPath: root})
	recorder := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	responseBody := append([]byte(nil), recorder.Body.Bytes()...)
	var response pluginResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		t.Fatal(err)
	}
	if response.Enabled || response.Status != "disabled" {
		t.Fatalf("install must remain disabled: %+v", response)
	}
	if len(response.Environment) != 2 || response.Environment[0].Key != "API_TOKEN" || !response.Environment[0].Configured || response.Environment[1].Key != "MODE" || !response.Environment[1].Configured {
		t.Fatalf("unexpected configured environment status: %+v", response.Environment)
	}
	text := string(responseBody)
	for _, secret := range []string{"AUTOTO_PLUGIN_TEST_SUPER_SECRET", "private-value", "env:"} {
		if strings.Contains(text, secret) {
			t.Fatalf("plugin response leaked secret target or value %q: %s", secret, text)
		}
	}
	var public map[string]any
	if err := json.Unmarshal([]byte(text), &public); err != nil {
		t.Fatal(err)
	}
	for _, sensitiveField := range []string{"command", "args"} {
		if _, ok := public[sensitiveField]; ok {
			t.Fatalf("plugin response exposed executable field %q: %s", sensitiveField, text)
		}
	}
}

func TestPluginLifecycleDiscoveryAndUninstallPreserveSourceDirectory(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}}
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(service)
	body, _ := json.Marshal(pluginInstallPayload{RootPath: root})
	installed := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body)
	if installed.Code != http.StatusCreated {
		t.Fatalf("install failed: %d %s", installed.Code, installed.Body.String())
	}

	disabledDiscovery := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/discover", nil)
	if disabledDiscovery.Code != http.StatusConflict {
		t.Fatalf("expected disabled discovery 409, got %d: %s", disabledDiscovery.Code, disabledDiscovery.Body.String())
	}
	missingAcknowledgement := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/enable", []byte(`{}`))
	if missingAcknowledgement.Code != http.StatusBadRequest || !strings.Contains(missingAcknowledgement.Body.String(), "confirmExecuteLocalCode") {
		t.Fatalf("expected explicit execution acknowledgement, got %d: %s", missingAcknowledgement.Code, missingAcknowledgement.Body.String())
	}
	enabled := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/enable", []byte(`{"confirmExecuteLocalCode":true}`))

	if enabled.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", enabled.Code, enabled.Body.String())
	}
	discovery := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/discover", nil)
	if discovery.Code != http.StatusOK || !strings.Contains(discovery.Body.String(), "plugin__safe-plugin__echo") {
		t.Fatalf("unexpected discovery: %d %s", discovery.Code, discovery.Body.String())
	}
	service.discoverErr = errors.New("plugin process handshake failed")
	failedDiscovery := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/discover", nil)
	if failedDiscovery.Code != http.StatusBadGateway {
		t.Fatalf("expected discovery failure 502, got %d: %s", failedDiscovery.Code, failedDiscovery.Body.String())
	}

	uninstalled := pluginRequest(t, app, http.MethodDelete, "/api/plugins/plugin-1", nil)
	if uninstalled.Code != http.StatusOK || !strings.Contains(uninstalled.Body.String(), `"sourceDeleted":false`) {
		t.Fatalf("unexpected uninstall response: %d %s", uninstalled.Code, uninstalled.Body.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("uninstall deleted or changed source directory: %v", err)
	}
	missing := pluginRequest(t, app, http.MethodGet, "/api/plugins/plugin-1", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected removed plugin 404, got %d: %s", missing.Code, missing.Body.String())
	}
}

func TestPluginInstallValidationAndConflictStatuses(t *testing.T) {
	service := &fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}}
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(service)
	bad := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", []byte(`{"rootPath":"bad"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid manifest 400, got %d: %s", bad.Code, bad.Body.String())
	}
	root := t.TempDir()
	body, _ := json.Marshal(pluginInstallPayload{RootPath: root})
	if first := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body); first.Code != http.StatusCreated {
		t.Fatalf("first install failed: %d %s", first.Code, first.Body.String())
	}
	conflict := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected duplicate install 409, got %d: %s", conflict.Code, conflict.Body.String())
	}
}

func pluginRequest(t *testing.T, app *Server, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := newTestRequest(method, path, bytes.NewReader(body))
	request.Header.Set(localTokenHeader, app.localToken)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}
