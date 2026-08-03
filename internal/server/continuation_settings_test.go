package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type fakeBackgroundRuntimeController struct {
	settings tools.BackgroundRuntimeSettings
	err      error
}

func (fake *fakeBackgroundRuntimeController) BackgroundRuntimeSettings() tools.BackgroundRuntimeSettings {
	return fake.settings
}

func (fake *fakeBackgroundRuntimeController) UpdateBackgroundRuntimeSettings(settings tools.BackgroundRuntimeSettings) (tools.BackgroundRuntimeSettings, error) {
	if fake.err != nil {
		return tools.BackgroundRuntimeSettings{}, fake.err
	}
	fake.settings = settings
	return settings, nil
}

func TestBackgroundRuntimeSettingsEndpointPersistsAndApplies(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	controller := &fakeBackgroundRuntimeController{settings: tools.BackgroundRuntimeSettings{WorkerCount: 8, PerAgentLimit: 4, MaxSubagentDepth: 2}}
	app := New(cfg, store, nil, nil)
	app.SetConfigPath(configPath)
	app.SetBackgroundRuntimeController(controller)
	body := []byte(`{"workerCount":12,"perAgentLimit":6,"allowNestedSubagents":true,"maxSubagentDepth":3}`)
	request := newTestRequest(http.MethodPatch, "/api/runtime/background-task-settings", bytes.NewReader(body))
	request.Host, request.RemoteAddr = "localhost:7788", "127.0.0.1:1234"
	request.Header.Set(localTokenHeader, app.localToken)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if controller.settings.WorkerCount != 12 || controller.settings.PerAgentLimit != 6 || !controller.settings.AllowNestedSubagents || controller.settings.MaxSubagentDepth != 3 {
		t.Fatalf("runtime settings were not applied: %+v", controller.settings)
	}
	persisted, _, err := config.LoadWithReport(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Background.WorkerCount != 12 || persisted.Background.PerAgentLimit != 6 || !persisted.Background.AllowNestedSubagents || persisted.Background.MaxSubagentDepth != 3 {
		t.Fatalf("background settings were not persisted: %+v", persisted.Background)
	}

	invalid := newTestRequest(http.MethodPatch, "/api/runtime/background-task-settings", bytes.NewReader([]byte(`{"workerCount":17,"perAgentLimit":4,"allowNestedSubagents":false,"maxSubagentDepth":2}`)))
	invalid.Host, invalid.RemoteAddr = "localhost:7788", "127.0.0.1:1234"
	invalid.Header.Set(localTokenHeader, app.localToken)
	invalidResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected strict validation failure, got %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	}

	unauthorized := httptest.NewRecorder()
	app.Routes().ServeHTTP(unauthorized, newTestRequest(http.MethodPatch, "/api/runtime/background-task-settings", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("expected local token requirement, got %d: %s", unauthorized.Code, unauthorized.Body.String())
	}
}

func TestContinuationSettingsEndpointPersistsBeforeApplying(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cfg, err := config.Default()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	runner := agent.NewRunner(store, providers.NewRegistry(), tools.NewRegistry(), agent.NewHub(), cfg.Agent)
	app := New(cfg, store, runner, agent.NewHub())
	app.SetConfigPath(configPath)
	body := []byte(`{"mode":"off","segmentTurns":5,"maxContinuations":0,"maxTotalTurns":5,"maxRunDurationMs":1000,"maxRunTokens":1000}`)
	request := newTestRequest(http.MethodPatch, "/api/runtime/continuation-settings", bytes.NewReader(body))
	request.Host, request.RemoteAddr = "localhost:7788", "127.0.0.1:1234"
	request.Header.Set(localTokenHeader, app.localToken)
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if got := runner.GetContinuationSettings(); got.Mode != "off" || got.MaxTotalTurns != 5 {
		t.Fatalf("runner settings were not applied: %+v", got)
	}
	persisted, _, err := config.LoadWithReport(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Agent.AutoContinuationMode != "off" || persisted.Agent.MaxTotalTurns != 5 {
		t.Fatalf("settings were not persisted: %+v", persisted.Agent)
	}

	invalid := newTestRequest(http.MethodPatch, "/api/runtime/continuation-settings", bytes.NewReader([]byte(`{"mode":"unsafe"}`)))
	invalid.Host, invalid.RemoteAddr = "localhost:7788", "127.0.0.1:1234"
	invalid.Header.Set(localTokenHeader, app.localToken)
	invalidResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("expected strict validation failure, got %d: %s", invalidResponse.Code, invalidResponse.Body.String())
	}
	var settings map[string]any
	settingsResponse := httptest.NewRecorder()
	settingsRequest := newTestRequest(http.MethodGet, "/api/settings", nil)
	settingsRequest.Host, settingsRequest.RemoteAddr = "localhost:7788", "127.0.0.1:1234"
	app.Routes().ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK || json.Unmarshal(settingsResponse.Body.Bytes(), &settings) != nil || settings["continuationSettingsEndpoint"] != "/api/runtime/continuation-settings" {
		t.Fatalf("settings endpoint was not advertised: %d %s", settingsResponse.Code, settingsResponse.Body.String())
	}
}
