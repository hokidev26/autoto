package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

type fakeModelProvider struct {
	name              string
	models            []string
	err               error
	capabilities      providers.Capabilities
	modelCapabilities map[string]providers.ModelCapabilities
	listCalls         *int
}

func (p fakeModelProvider) Name() string { return p.name }

func (p fakeModelProvider) Capabilities() providers.Capabilities { return p.capabilities }

func (p fakeModelProvider) ModelCapabilities(model string) providers.ModelCapabilities {
	return p.modelCapabilities[model]
}

func (p fakeModelProvider) ListModels(ctx context.Context) ([]string, error) {
	if p.listCalls != nil {
		(*p.listCalls)++
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.models, nil
}

func (p fakeModelProvider) Generate(ctx context.Context, req providers.GenerateRequest) (<-chan providers.Event, error) {
	out := make(chan providers.Event)
	close(out)
	return out, nil
}

func TestCreateProjectUsesRequestedModel(t *testing.T) {
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	projectDir := filepath.Join(t.TempDir(), "project")
	app := New(config.Config{
		Paths: config.PathsConfig{DefaultProjectDir: t.TempDir()},
		Agent: config.AgentConfig{DefaultModel: "openai:default", DefaultPermissionMode: "acceptEdits"},
	}, store, nil, nil)

	payload, err := json.Marshal(map[string]string{
		"name":    "Demo",
		"gitPath": projectDir,
		"model":   "cliproxyapi:gpt-dynamic",
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/projects", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Agent db.Agent `json:"agent"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Agent.Model != "cliproxyapi:gpt-dynamic" {
		t.Fatalf("expected requested model, got %q", body.Agent.Model)
	}
}

func TestCreateStandaloneConversationUsesExecutableDefaultModel(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "conversation-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "ready"})
	app := New(config.Config{
		Agent:     config.AgentConfig{DefaultModel: "ready:chat"},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{Name: "ready", Type: "openai-compatible", APIKeyOptional: true}}},
	}, store, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Project  db.Project  `json:"project"`
		Workline db.Workline `json:"workline"`
		Agent    db.Agent    `json:"agent"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Project.FlowMode != db.ProjectFlowModeConversation || created.Project.GitPath != "" || created.Workline.WorktreePath != "" || created.Agent.CWD != "" || created.Agent.PermissionMode != "readOnly" || created.Agent.Model != "ready:chat" || created.Agent.Title != "New conversation" {
		t.Fatalf("unexpected standalone conversation response: %+v", created)
	}

	failed := httptest.NewRecorder()
	badRequest := newTestRequest(http.MethodPost, "/api/conversations", strings.NewReader(`{"title":"Bad","model":"missing:model"}`))
	badRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(failed, badRequest)
	if failed.Code != http.StatusBadRequest {
		t.Fatalf("expected unregistered model rejection, got %d: %s", failed.Code, failed.Body.String())
	}
	var projectCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 {
		t.Fatalf("rejected conversation request created partial rows: project count=%d", projectCount)
	}
}

func TestAgentModelUpdateKeepsOnlyTargetSupportedReasoningEffort(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "reasoning:model-a", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "reasoning", capabilities: providers.Capabilities{ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}}})
	registry.Register(fakeModelProvider{name: "basic", capabilities: providers.Capabilities{}})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "reasoning", Type: "openai-compatible", APIKeyOptional: true},
		{Name: "basic", Type: "openai-compatible", APIKeyOptional: true},
	}}}, store, nil, nil, registry)

	patch := func(path, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	if response := patch("/reasoning-effort", `{"reasoningEffort":"xhigh"}`); response.Code != http.StatusOK {
		t.Fatalf("set xhigh: %d %s", response.Code, response.Body.String())
	}
	preserved := patch("/model", `{"model":"reasoning:model-b"}`)
	if preserved.Code != http.StatusOK {
		t.Fatalf("switch within supporting provider: %d %s", preserved.Code, preserved.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(preserved.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Model != "reasoning:model-b" || updated.ReasoningEffort != "xhigh" {
		t.Fatalf("supported model switch should preserve effort: %+v", updated)
	}

	fallback := patch("/model", `{"model":"basic:model"}`)
	if fallback.Code != http.StatusOK {
		t.Fatalf("switch to non-reasoning model: %d %s", fallback.Code, fallback.Body.String())
	}
	if err := json.NewDecoder(fallback.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.Model != "basic:model" || updated.ReasoningEffort != "auto" {
		t.Fatalf("unsupported model switch must use auto effort: %+v", updated)
	}
	if response := patch("/reasoning-effort", `{"reasoningEffort":"auto","model":"basic:model","entityGeneration":0}`); response.Code != http.StatusConflict {
		t.Fatalf("stale model revision should be rejected: %d %s", response.Code, response.Body.String())
	}
	if response := patch("/reasoning-effort", `{"reasoningEffort":"high"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported effort should be rejected for the current model: %d %s", response.Code, response.Body.String())
	}
}

func TestConcurrentAgentModelAndReasoningPatchesRemainCapabilitySafe(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "reasoning:model", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAgentReasoningEffort(ctx, agent.ID, "high"); err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "reasoning", capabilities: providers.Capabilities{ReasoningEffort: true}})
	registry.Register(fakeModelProvider{name: "basic", capabilities: providers.Capabilities{}})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "reasoning", Type: "openai-compatible", APIKeyOptional: true},
		{Name: "basic", Type: "openai-compatible", APIKeyOptional: true},
	}}}, store, nil, nil, registry)

	start := make(chan struct{})
	var group sync.WaitGroup
	codes := make(chan int, 2)
	for _, patch := range []struct {
		path string
		body string
	}{
		{path: "/model", body: `{"model":"basic:model"}`},
		{path: "/reasoning-effort", body: `{"reasoningEffort":"high"}`},
	} {
		group.Add(1)
		go func(patch struct{ path, body string }) {
			defer group.Done()
			<-start
			recorder := httptest.NewRecorder()
			request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+patch.path, strings.NewReader(patch.body))
			request.Header.Set("Content-Type", "application/json")
			app.Routes().ServeHTTP(recorder, request)
			codes <- recorder.Code
		}(patch)
	}
	close(start)
	group.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK && code != http.StatusBadRequest {
			t.Fatalf("unexpected concurrent mutation status %d", code)
		}
	}
	stored, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "basic:model" || stored.ReasoningEffort != "auto" {
		t.Fatalf("concurrent mutations persisted an unsupported combination: %+v", stored)
	}
}

func TestModelsRouteReturnsDynamicProviderModels(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "cliproxyapi", models: []string{"z-model", "a-model", "a-model"}})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        "http://127.0.0.1:8317/v1",
		Model:          "fallback-model",
		APIKeyOptional: true,
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/models", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 1 {
		t.Fatalf("expected one provider, got %+v", body.Providers)
	}
	provider := body.Providers[0]
	if provider.Error != "" {
		t.Fatalf("expected no error, got %q", provider.Error)
	}
	if provider.Profile != config.ProviderProfileCLIProxyAPI || provider.ManagementURL != "http://127.0.0.1:8317/management.html" || provider.Management == nil || !provider.Management.AuthFiles {
		t.Fatalf("unexpected provider management metadata: %+v", provider)
	}
	if provider.Capabilities.Tools || provider.Capabilities.Streaming || provider.Capabilities.ImageInput || provider.Capabilities.ReasoningEffort || len(provider.Capabilities.ReasoningEfforts) != 0 {
		t.Fatalf("unknown provider capabilities must be false, got %+v", provider.Capabilities)
	}
	expected := []string{"a-model", "fallback-model", "z-model"}
	if len(provider.Models) != len(expected) {
		t.Fatalf("expected models %+v, got %+v", expected, provider.Models)
	}
	for i := range expected {
		if provider.Models[i] != expected[i] {
			t.Fatalf("expected models %+v, got %+v", expected, provider.Models)
		}
	}
}

func TestModelsRouteDoesNotMarkUnconfiguredFallbackAsDiscovered(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "relay", models: []string{"fallback-model"}})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "fallback-model",
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	provider := body.Providers[0]
	if provider.Configured || provider.Discovered || provider.Available || provider.ModelsSource != "configured-default" {
		t.Fatalf("unconfigured fallback was reported as discovered: %+v", provider)
	}
	if len(provider.Models) != 1 || provider.Models[0] != "fallback-model" {
		t.Fatalf("fallback model should remain visible as a configured default: %+v", provider.Models)
	}
}

func TestModelsRouteKeepsDisabledProviderVisibleWithoutListingUpstreamModels(t *testing.T) {
	calls := 0
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "relay", models: []string{"remote-model"}, listCalls: &calls})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "fallback-model", Models: []config.ProviderModelConfig{{Name: "saved-model", ContextTokenLimit: 123000}}, Disabled: true,
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var models modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Providers) != 1 {
		t.Fatalf("unexpected providers: %+v", models.Providers)
	}
	provider := models.Providers[0]
	if provider.Enabled || provider.Origin != config.ProviderOriginCustom || provider.Error != "" || strings.Join(provider.Models, ",") != "fallback-model,saved-model" {
		t.Fatalf("unexpected disabled provider response: %+v", provider)
	}
	if provider.ModelCapabilities["saved-model"].ContextTokenLimit != 123000 {
		t.Fatalf("disabled provider lost saved model capabilities: %+v", provider.ModelCapabilities)
	}
	if calls != 0 {
		t.Fatalf("disabled provider must not list upstream models, got %d calls", calls)
	}

	recorder = httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/settings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var settings struct {
		Providers []settingsProviderResponse `json:"providers"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Providers) != 1 || settings.Providers[0].Enabled || settings.Providers[0].Origin != config.ProviderOriginCustom {
		t.Fatalf("settings did not expose disabled custom provider: %+v", settings.Providers)
	}
}

func TestModelsRouteMarksRuntimeRegistrationSeparatelyFromSettingsMetadata(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "ready", models: []string{"chat"}})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "ready", Type: "openai-compatible", Model: "chat", APIKeyOptional: true},
		{Name: "metadata-only", Type: "openai-compatible", Model: "saved", APIKeyOptional: true},
	}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]modelProviderResponse, len(body.Providers))
	for _, provider := range body.Providers {
		byName[provider.Name] = provider
	}
	if ready := byName["ready"]; !ready.Registered || !ready.RuntimeAvailable || !ready.Configured {
		t.Fatalf("registered configured provider was not runtime available: %+v", ready)
	}
	if metadata := byName["metadata-only"]; metadata.Registered || metadata.RuntimeAvailable || !metadata.Configured || len(metadata.Models) != 1 || metadata.Models[0] != "saved" {
		t.Fatalf("unregistered settings metadata was not retained safely: %+v", metadata)
	}
}

func TestAgentModelUpdateRejectsUnregisteredAndUnconfiguredProviders(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "model-validation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "ready:chat", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "ready"})
	registry.Register(fakeModelProvider{name: "unconfigured"})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "ready", Type: "openai-compatible", APIKeyOptional: true},
		{Name: "unconfigured", Type: "openai-compatible"},
		{Name: "metadata-only", Type: "openai-compatible", APIKeyOptional: true},
	}}}, store, nil, nil, registry)

	for _, model := range []string{"metadata-only:saved", "unconfigured:chat"} {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/model", strings.NewReader(`{"model":"`+model+`"}`))
		request.Header.Set("Content-Type", "application/json")
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("expected %s to be rejected, got %d: %s", model, recorder.Code, recorder.Body.String())
		}
	}
	stored, err := store.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "ready:chat" {
		t.Fatalf("rejected model update mutated the agent: %+v", stored)
	}
}

func TestModelsRouteExposesCanonicalReasoningEfforts(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{
		name:         "legacy",
		models:       []string{"reasoning-model"},
		capabilities: providers.Capabilities{ReasoningEffort: true},
	})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "legacy", Type: "openai-compatible", Model: "reasoning-model", APIKeyOptional: true,
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 1 {
		t.Fatalf("unexpected model catalog: %+v", body)
	}
	got := body.Providers[0].Capabilities
	if !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("model catalog did not expose canonical reasoning efforts: %+v", got)
	}
}

func TestModelsRouteExposesXHighForCodexCapability(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{
		name:              "codex",
		models:            []string{"gpt-5", "gpt-5-mini"},
		modelCapabilities: map[string]providers.ModelCapabilities{"gpt-5": {FastMode: true, FastModeKnown: true}},
		capabilities: providers.Capabilities{
			ReasoningEffort:  true,
			ReasoningEfforts: []string{"xhigh", "high", "medium", "low"},
		},
	})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "codex", Type: config.ProviderTypeCodex, Model: "gpt-5", Models: []config.ProviderModelConfig{{Name: "gpt-5", ContextTokenLimit: 400000, ImageGeneration: true}},
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	got := body.Providers[0].Capabilities
	if !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high,xhigh" {
		t.Fatalf("model catalog did not expose canonical Codex xhigh capability: %+v", got)
	}
	modelCapabilities := body.Providers[0].ModelCapabilities
	if !modelCapabilities["gpt-5"].FastMode || !modelCapabilities["gpt-5"].ImageGeneration || modelCapabilities["gpt-5"].ContextTokenLimit != 400000 {
		t.Fatalf("model catalog did not merge per-model Fast, image generation, and context capabilities: %+v", modelCapabilities)
	}
	if _, exists := modelCapabilities["gpt-5-mini"]; exists {
		t.Fatalf("unsupported model should not expose Fast capability: %+v", modelCapabilities)
	}
}

func TestModelsRouteFallsBackWhenProviderModelListFails(t *testing.T) {
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{
		name: "cliproxyapi",
		err:  errors.New("connection refused"),
		modelCapabilities: map[string]providers.ModelCapabilities{
			"fallback-model": {FastMode: true, FastModeKnown: true},
		},
	})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        "http://127.0.0.1:8317/v1",
		Model:          "fallback-model",
		Models:         []config.ProviderModelConfig{{Name: "saved-model", ContextTokenLimit: 250000}},
		APIKeyOptional: true,
	}}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/models", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	provider := body.Providers[0]
	if strings.Join(provider.Models, ",") != "fallback-model,saved-model" {
		t.Fatalf("expected saved and default models after remote failure, got %+v", provider.Models)
	}
	if provider.Error == "" {
		t.Fatal("expected provider error message")
	}
	if capability, exists := provider.ModelCapabilities["fallback-model"]; !exists || !capability.FastMode {
		t.Fatalf("fallback model must retain its explicit Fast capability: %+v", provider.ModelCapabilities)
	}
	if capability, exists := provider.ModelCapabilities["saved-model"]; !exists || capability.ContextTokenLimit != 250000 {
		t.Fatalf("saved model must retain its configured context limit: %+v", provider.ModelCapabilities)
	}

	settingsRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(settingsRecorder, newTestRequest(http.MethodGet, "/api/settings", nil))
	if settingsRecorder.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", settingsRecorder.Code, settingsRecorder.Body.String())
	}
	var settings struct {
		Providers []settingsProviderResponse `json:"providers"`
	}
	if err := json.NewDecoder(settingsRecorder.Body).Decode(&settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Providers) != 1 || len(settings.Providers[0].Models) != 2 || settings.Providers[0].Models[0].Name != "saved-model" || settings.Providers[0].Models[0].ContextTokenLimit != 250000 || settings.Providers[0].Models[1].Name != "fallback-model" {
		t.Fatalf("settings did not return normalized model configuration: %+v", settings.Providers)
	}
}

func TestFriendlyModelListErrorUsesAutotoBranding(t *testing.T) {
	message := friendlyModelListError(config.ProviderSummary{Profile: config.ProviderProfileCLIProxyAPI}, errors.New("401 unauthorized"))
	if !strings.Contains(message, "后重启 Autoto。") {
		t.Fatalf("expected Autoto-branded error message, got %q", message)
	}
}
