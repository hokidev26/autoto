package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/audit"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/plugins"
)

type fakePluginService struct {
	plugins       map[string]db.Plugin
	configured    map[string]map[string]bool
	discoverErr   error
	updateErr     error
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

func (f *fakePluginService) Update(ctx context.Context, id string) (db.Plugin, error) {
	plugin, err := f.Get(ctx, id)
	if err != nil {
		return db.Plugin{}, err
	}
	if f.updateErr != nil {
		return db.Plugin{}, f.updateErr
	}
	plugin.Version = "2.0.0"
	plugin.Enabled, plugin.Status, plugin.LastError = false, "disabled", ""
	plugin.Revision++
	f.plugins[id] = plugin
	return plugin, nil
}

func (f *fakePluginService) Health(ctx context.Context, id string) plugins.Health {
	health := plugins.Health{PluginID: id, CheckedAt: "2025-01-01T00:00:00Z"}
	plugin, err := f.Get(ctx, id)
	if err != nil {
		health.Error = err.Error()
		return health
	}
	if !plugin.Enabled {
		health.Error = "plugin is disabled"
		return health
	}
	health.Healthy, health.ToolCount = true, 1
	return health
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

type recordingAuditRecorder struct {
	failWith error
	events   []audit.Event
}

func (r *recordingAuditRecorder) Record(_ context.Context, event audit.Event) error {
	if r.failWith != nil {
		return r.failWith
	}
	r.events = append(r.events, event)
	return nil
}

func (r *recordingAuditRecorder) byAction(action string) []audit.Event {
	out := make([]audit.Event, 0)
	for _, event := range r.events {
		if event.Action == action {
			out = append(out, event)
		}
	}
	return out
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
		{http.MethodPost, "/api/plugins/missing/update"},
		{http.MethodPost, "/api/plugins/missing/health"},
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

func TestPluginUpdateEndpointLeavesPluginDisabledAndRecordsAudit(t *testing.T) {
	service := &fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}}
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(service)
	recorder := &recordingAuditRecorder{}
	app.SetAuditRecorder(recorder)
	body, _ := json.Marshal(pluginInstallPayload{RootPath: t.TempDir()})
	if installed := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body); installed.Code != http.StatusCreated {
		t.Fatalf("install failed: %d %s", installed.Code, installed.Body.String())
	}
	if enabled := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/enable", []byte(`{"confirmExecuteLocalCode":true}`)); enabled.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", enabled.Code, enabled.Body.String())
	}

	updated := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/update", nil)
	if updated.Code != http.StatusOK {
		t.Fatalf("expected update 200, got %d: %s", updated.Code, updated.Body.String())
	}
	var response pluginResponse
	if err := json.Unmarshal(updated.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "plugin-1" || response.Enabled || response.Status != "disabled" {
		t.Fatalf("updating an enabled plugin must leave it disabled: %+v", response)
	}
	if response.Version != "2.0.0" || response.Revision != 2 || len(response.Environment) != 2 {
		t.Fatalf("update response did not adopt manifest fields: %+v", response)
	}
	events := recorder.byAction("plugin.update")
	if len(events) != 1 || events[0].Outcome != "success" || events[0].Risk != "medium" || events[0].SubjectID != "plugin-1" {
		t.Fatalf("unexpected plugin.update audit events: %+v", events)
	}

	if missing := pluginRequest(t, app, http.MethodPost, "/api/plugins/missing/update", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("expected unknown plugin 404, got %d: %s", missing.Code, missing.Body.String())
	}
	service.updateErr = fmt.Errorf("resolve plugin manifest: %w", &fs.PathError{Op: "open", Path: "autoto.plugin.json", Err: fs.ErrNotExist})
	if gone := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/update", nil); gone.Code != http.StatusNotFound {
		t.Fatalf("expected missing manifest 404, got %d: %s", gone.Code, gone.Body.String())
	}
	service.updateErr = errors.New("decode plugin manifest: invalid character")
	if invalid := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/update", nil); invalid.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid manifest 400, got %d: %s", invalid.Code, invalid.Body.String())
	}
	service.updateErr = db.ErrConflict
	if conflict := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/update", nil); conflict.Code != http.StatusConflict {
		t.Fatalf("expected slug conflict 409, got %d: %s", conflict.Code, conflict.Body.String())
	}
	service.updateErr = nil
	recorder.failWith = errors.New("audit store unavailable")
	if failed := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/update", nil); failed.Code != http.StatusInternalServerError || !strings.Contains(failed.Body.String(), "audit persistence failed") {
		t.Fatalf("expected fail-closed audit 500, got %d: %s", failed.Code, failed.Body.String())
	}
}

func TestPluginHealthEndpointReportsWithoutLaunchingDisabledPlugins(t *testing.T) {
	service := &fakePluginService{plugins: map[string]db.Plugin{}, configured: map[string]map[string]bool{}}
	app := New(config.Config{}, nil, nil, nil)
	app.SetPluginService(service)
	recorder := &recordingAuditRecorder{}
	app.SetAuditRecorder(recorder)
	body, _ := json.Marshal(pluginInstallPayload{RootPath: t.TempDir()})
	if installed := pluginRequest(t, app, http.MethodPost, "/api/plugins/install", body); installed.Code != http.StatusCreated {
		t.Fatalf("install failed: %d %s", installed.Code, installed.Body.String())
	}

	disabled := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/health", nil)
	if disabled.Code != http.StatusOK {
		t.Fatalf("expected disabled health report 200, got %d: %s", disabled.Code, disabled.Body.String())
	}
	var disabledHealth plugins.Health
	if err := json.Unmarshal(disabled.Body.Bytes(), &disabledHealth); err != nil {
		t.Fatal(err)
	}
	if disabledHealth.Healthy || disabledHealth.Error != "plugin is disabled" || disabledHealth.PluginID != "plugin-1" {
		t.Fatalf("unexpected disabled health report: %+v", disabledHealth)
	}
	if events := recorder.byAction("plugin.health"); len(events) != 0 {
		t.Fatalf("disabled plugin health must not be audited as a launch: %+v", events)
	}

	if enabled := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/enable", []byte(`{"confirmExecuteLocalCode":true}`)); enabled.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", enabled.Code, enabled.Body.String())
	}
	healthy := pluginRequest(t, app, http.MethodPost, "/api/plugins/plugin-1/health", nil)
	if healthy.Code != http.StatusOK {
		t.Fatalf("expected health report 200, got %d: %s", healthy.Code, healthy.Body.String())
	}
	var enabledHealth plugins.Health
	if err := json.Unmarshal(healthy.Body.Bytes(), &enabledHealth); err != nil {
		t.Fatal(err)
	}
	if !enabledHealth.Healthy || enabledHealth.ToolCount != 1 || enabledHealth.Error != "" {
		t.Fatalf("unexpected enabled health report: %+v", enabledHealth)
	}
	events := recorder.byAction("plugin.health")
	if len(events) != 1 || events[0].Outcome != "success" || events[0].Risk != "medium" || events[0].SubjectID != "plugin-1" {
		t.Fatalf("unexpected plugin.health audit events: %+v", events)
	}

	if missing := pluginRequest(t, app, http.MethodPost, "/api/plugins/missing/health", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("expected unknown plugin 404, got %d: %s", missing.Code, missing.Body.String())
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
