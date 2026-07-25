package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/subscriptionauth"
)

func TestModelAggregateHandlersPreserveOrderAndRequireCAS(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	handler := modelRuntimeTestHandler(app)

	createdResponse := modelRuntimeRequest(handler, http.MethodPut, "/aggregates/fast", `{"mode":"priority","members":["second:model-b","first:model-a"],"revision":0}`)
	if createdResponse.Code != http.StatusOK {
		t.Fatalf("create aggregate: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	created := decodeModelAggregate(t, createdResponse)
	wantMembers := []string{"second:model-b", "first:model-a"}
	if created.Revision != 1 || !reflect.DeepEqual(created.Members, wantMembers) {
		t.Fatalf("unexpected aggregate: %+v", created)
	}

	stale := modelRuntimeRequest(handler, http.MethodPut, "/aggregates/fast", `{"members":["first:model-a"],"revision":0}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale update conflict, got %d: %s", stale.Code, stale.Body.String())
	}

	updatedResponse := modelRuntimeRequest(handler, http.MethodPut, "/aggregates/fast", `{"members":["first:model-a","second:model-b"],"expectedRevision":1}`)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update aggregate: %d %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	updated := decodeModelAggregate(t, updatedResponse)
	wantMembers = []string{"first:model-a", "second:model-b"}
	if updated.Revision != 2 || !reflect.DeepEqual(updated.Members, wantMembers) {
		t.Fatalf("aggregate order or revision changed: %+v", updated)
	}

	getResponse := modelRuntimeRequest(handler, http.MethodGet, "/aggregates/fast", "")
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get aggregate: %d %s", getResponse.Code, getResponse.Body.String())
	}
	if got := decodeModelAggregate(t, getResponse); !reflect.DeepEqual(got.Members, wantMembers) {
		t.Fatalf("stored member order changed: %+v", got.Members)
	}

	listResponse := modelRuntimeRequest(handler, http.MethodGet, "/aggregates", "")
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list aggregates: %d %s", listResponse.Code, listResponse.Body.String())
	}
	var aggregates []db.ModelAggregate
	if err := json.NewDecoder(listResponse.Body).Decode(&aggregates); err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 1 || !reflect.DeepEqual(aggregates[0].Members, wantMembers) {
		t.Fatalf("unexpected aggregate list: %+v", aggregates)
	}

	staleDelete := modelRuntimeRequest(handler, http.MethodDelete, "/aggregates/fast", `{"revision":1}`)
	if staleDelete.Code != http.StatusConflict {
		t.Fatalf("expected stale delete conflict, got %d: %s", staleDelete.Code, staleDelete.Body.String())
	}
	deleted := modelRuntimeRequest(handler, http.MethodDelete, "/aggregates/fast", `{"revision":2}`)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete aggregate: %d %s", deleted.Code, deleted.Body.String())
	}
}

func TestModelAggregateHandlersRejectMalformedRequests(t *testing.T) {
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	handler := modelRuntimeTestHandler(New(config.Config{}, store, nil, nil))
	cases := []string{
		`{"members":["openai:gpt"],"unknown":true,"revision":0}`,
		`{"members":["openai:gpt"]}`,
		`{"members":null,"revision":0}`,
		`{"members":["aggregate:nested"],"revision":0}`,
		`{"members":["openai:gpt","openai:gpt"],"revision":0}`,
		`{"mode":"round_robin","members":["openai:gpt"],"revision":0}`,
		`{"members":["openai:gpt"],"revision":0,"expectedRevision":0}`,
	}
	for _, body := range cases {
		response := modelRuntimeRequest(handler, http.MethodPut, "/aggregates/fast", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %s, got %d: %s", body, response.Code, response.Body.String())
		}
	}
}

func TestRuntimeModelSettingsAndSettingsResponse(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)
	handler := modelRuntimeTestHandler(app)
	initial, err := store.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	response := modelRuntimeRequest(handler, http.MethodPatch, "/runtime/model-settings", `{"defaultReasoningEffort":"high","subscriptionTier":"education_k12","accountEmail":"student@example.edu","revision":`+jsonInt(initial.Revision)+`}`)
	if response.Code != http.StatusOK {
		t.Fatalf("patch runtime settings: %d %s", response.Code, response.Body.String())
	}
	var updated db.RuntimeSettings
	if err := json.NewDecoder(response.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.DefaultReasoningEffort != "high" || updated.SubscriptionTier != "education_k12" || updated.AccountEmail != "student@example.edu" || updated.Revision != initial.Revision+1 {
		t.Fatalf("unexpected runtime settings: %+v", updated)
	}

	stale := modelRuntimeRequest(handler, http.MethodPatch, "/runtime/model-settings", `{"subscriptionTier":"free","revision":`+jsonInt(initial.Revision)+`}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale settings conflict, got %d: %s", stale.Code, stale.Body.String())
	}
	for _, body := range []string{
		`{"revision":` + jsonInt(updated.Revision) + `}`,
		`{"defaultReasoningEffort":"extreme","revision":` + jsonInt(updated.Revision) + `}`,
		`{"subscriptionTier":"student","revision":` + jsonInt(updated.Revision) + `}`,
		`{"accountEmail":null,"revision":` + jsonInt(updated.Revision) + `}`,
		`{"accountEmail":"student@example.edu","unknown":true,"revision":` + jsonInt(updated.Revision) + `}`,
	} {
		invalid := modelRuntimeRequest(handler, http.MethodPatch, "/runtime/model-settings", body)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid settings request to fail: %s => %d %s", body, invalid.Code, invalid.Body.String())
		}
	}

	settingsResponse := httptest.NewRecorder()
	app.Routes().ServeHTTP(settingsResponse, newTestRequest(http.MethodGet, "/api/settings", nil))
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("settings response: %d %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	var settingsBody struct {
		RuntimeSettings db.RuntimeSettings `json:"runtimeSettings"`
		TierOrder       []string           `json:"tierOrder"`
	}
	if err := json.NewDecoder(settingsResponse.Body).Decode(&settingsBody); err != nil {
		t.Fatal(err)
	}
	wantTierOrder := []string{"free", "plus", "pro", "team", "enterprise", "education_k12"}
	if !reflect.DeepEqual(settingsBody.TierOrder, wantTierOrder) || settingsBody.RuntimeSettings.SubscriptionTier != "education_k12" {
		t.Fatalf("unexpected settings metadata: %+v", settingsBody)
	}
}

func TestAgentModelSettingsPersistDefaultsAndRolePools(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	app := New(config.Config{
		Paths: config.PathsConfig{HomeDir: home},
		Agent: config.AgentConfig{DefaultModel: "openai:old", SummaryModel: "openai:old"},
	}, nil, nil, nil)
	app.SetConfigPath(configPath)
	handler := modelRuntimeTestHandler(app)
	body := `{"defaultModel":"codex:gpt-5.5","summaryModel":"openai:gpt-4.1-mini","subagentModels":{"explore":"codex:gpt-5.4-mini","general":"codex:gpt-5.5"},"subagentModelPools":{"explore":["codex:gpt-5.4-mini","openai:gpt-4.1-mini"],"general":["codex:gpt-5.5"]}}`
	response := modelRuntimeRequest(handler, http.MethodPatch, "/runtime/agent-model-settings", body)
	if response.Code != http.StatusOK {
		t.Fatalf("patch agent model settings: %d %s", response.Code, response.Body.String())
	}
	updated := app.configSnapshot().Agent
	if updated.DefaultModel != "codex:gpt-5.5" || updated.SummaryModel != "openai:gpt-4.1-mini" || updated.SubagentModels["explore"] != "codex:gpt-5.4-mini" || len(updated.SubagentModelPools["explore"]) != 2 {
		t.Fatalf("unexpected agent model settings: %+v", updated)
	}
	persisted, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted.Agent.SubagentModels, updated.SubagentModels) || !reflect.DeepEqual(persisted.Agent.SubagentModelPools, updated.SubagentModelPools) {
		t.Fatalf("agent model settings were not persisted: %+v", persisted.Agent)
	}
	for _, invalidBody := range []string{
		`{"defaultModel":"missing-colon","summaryModel":"openai:gpt","subagentModels":{},"subagentModelPools":{}}`,
		`{"defaultModel":"openai:gpt","summaryModel":"openai:gpt","subagentModels":{"unknown":"openai:gpt"},"subagentModelPools":{}}`,
		`{"defaultModel":"openai:gpt","summaryModel":"openai:gpt","subagentModels":{"plan":"openai:gpt"},"subagentModelPools":{"plan":["codex:gpt"]}}`,
	} {
		invalid := modelRuntimeRequest(handler, http.MethodPatch, "/runtime/agent-model-settings", invalidBody)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("expected invalid agent settings rejection for %s, got %d: %s", invalidBody, invalid.Code, invalid.Body.String())
		}
	}
}

func TestAgentReasoningAndClientIdentityHandlers(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "openai:gpt", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "openai", capabilities: providers.Capabilities{ReasoningEffort: true}})
	app := New(config.Config{}, store, nil, nil, registry)
	handler := modelRuntimeTestHandler(app)

	reasoningResponse := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/reasoning", `{"reasoningEffort":"medium"}`)
	if reasoningResponse.Code != http.StatusOK {
		t.Fatalf("patch agent reasoning: %d %s", reasoningResponse.Code, reasoningResponse.Body.String())
	}
	var updatedAgent db.Agent
	if err := json.NewDecoder(reasoningResponse.Body).Decode(&updatedAgent); err != nil {
		t.Fatal(err)
	}
	if updatedAgent.ReasoningEffort != "medium" {
		t.Fatalf("unexpected agent reasoning effort: %+v", updatedAgent)
	}
	cleared := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/reasoning", `{"reasoningEffort":""}`)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear agent reasoning: %d %s", cleared.Code, cleared.Body.String())
	}
	invalid := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/reasoning", `{"reasoningEffort":null}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("expected null reasoning rejection, got %d: %s", invalid.Code, invalid.Body.String())
	}

	settings, err := store.GetRuntimeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	email := "owner@example.com"
	settings, err = store.UpdateRuntimeSettings(ctx, db.RuntimeSettingsPatch{AccountEmail: &email, ExpectedRevision: settings.Revision})
	if err != nil {
		t.Fatal(err)
	}
	identityResponse := modelRuntimeRequest(handler, http.MethodGet, "/client/identity", "")
	if identityResponse.Code != http.StatusOK {
		t.Fatalf("get identity: %d %s", identityResponse.Code, identityResponse.Body.String())
	}
	var identity clientIdentityResponse
	if err := json.NewDecoder(identityResponse.Body).Decode(&identity); err != nil {
		t.Fatal(err)
	}
	if identity.InstallationID != settings.InstallationID || identity.ClientVersion != config.Version || identity.Version != config.Version || identity.Authentication || identity.IsAuthenticationCredential {
		t.Fatalf("unexpected client identity: %+v", identity)
	}
	identityJSON := identityResponse.Body.String()
	for _, forbidden := range []string{"owner@example.com", "apiKey", "token", "password", "credential\":"} {
		if strings.Contains(identityJSON, forbidden) {
			t.Fatalf("identity leaked credential metadata %q: %s", forbidden, identityJSON)
		}
	}

	rotatedResponse := modelRuntimeRequest(handler, http.MethodPost, "/client/identity/rotate", `{"revision":`+jsonInt(settings.Revision)+`}`)
	if rotatedResponse.Code != http.StatusOK {
		t.Fatalf("rotate identity: %d %s", rotatedResponse.Code, rotatedResponse.Body.String())
	}
	var rotated clientIdentityResponse
	if err := json.NewDecoder(rotatedResponse.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.InstallationID == identity.InstallationID || rotated.Revision != settings.Revision+1 || rotated.Authentication || rotated.IsAuthenticationCredential {
		t.Fatalf("unexpected rotated identity: %+v", rotated)
	}
	staleRotate := modelRuntimeRequest(handler, http.MethodPost, "/client/identity/rotate", `{"revision":`+jsonInt(settings.Revision)+`}`)
	if staleRotate.Code != http.StatusConflict {
		t.Fatalf("expected stale rotation conflict, got %d: %s", staleRotate.Code, staleRotate.Body.String())
	}
}

func TestAgentFastModeRequiresModelCapabilityAndDisablesOnModelSwitch(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "codex:gpt-fast", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "codex", modelCapabilities: map[string]providers.ModelCapabilities{"gpt-fast": {FastMode: true}}})
	registry.Register(fakeModelProvider{name: "basic"})
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "codex", Type: "openai-compatible", APIKeyOptional: true},
		{Name: "basic", Type: "openai-compatible", APIKeyOptional: true},
	}}}, store, nil, nil, registry)
	handler := modelRuntimeTestHandler(app)

	enabled := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/fast-mode", `{"fastMode":true,"model":"codex:gpt-fast","entityGeneration":1}`)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable Fast mode: %d %s", enabled.Code, enabled.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(enabled.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if !updated.FastMode || updated.EntityGeneration != 2 {
		t.Fatalf("Fast mode did not persist: %+v", updated)
	}

	stale := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/fast-mode", `{"fastMode":false,"model":"codex:gpt-fast","entityGeneration":1}`)
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale Fast update conflict, got %d: %s", stale.Code, stale.Body.String())
	}

	switched := httptest.NewRecorder()
	switchRequest := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/model", strings.NewReader(`{"model":"basic:model"}`))
	switchRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(switched, switchRequest)
	if switched.Code != http.StatusOK {
		t.Fatalf("switch to basic model: %d %s", switched.Code, switched.Body.String())
	}
	if err := json.NewDecoder(switched.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.FastMode {
		t.Fatalf("model switch should disable unsupported Fast mode: %+v", updated)
	}

	unsupported := modelRuntimeRequest(handler, http.MethodPatch, "/agents/"+agent.ID+"/fast-mode", `{"fastMode":true,"model":"basic:model"}`)
	if unsupported.Code != http.StatusBadRequest {
		t.Fatalf("expected unsupported Fast rejection, got %d: %s", unsupported.Code, unsupported.Body.String())
	}
}

func TestAgentReasoningEffortAPIPersistsXHigh(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "codex:gpt-5", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	registry := providers.NewRegistry()
	registry.Register(fakeModelProvider{name: "codex", capabilities: providers.Capabilities{ReasoningEfforts: []string{"low", "medium", "high", "xhigh"}}})
	app := New(config.Config{}, store, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/reasoning-effort", strings.NewReader(`{"reasoningEffort":"XHIGH"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("persist xhigh reasoning effort: %d %s", recorder.Code, recorder.Body.String())
	}
	var updated db.Agent
	if err := json.NewDecoder(recorder.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.ReasoningEffort != "xhigh" {
		t.Fatalf("expected normalized xhigh agent response, got %+v", updated)
	}
	stored, err := store.GetAgent(ctx, agent.ID)
	if err != nil || stored.ReasoningEffort != "xhigh" {
		t.Fatalf("xhigh did not round-trip from storage: %+v err=%v", stored, err)
	}

	invalid := httptest.NewRecorder()
	invalidRequest := newTestRequest(http.MethodPatch, "/api/agents/"+agent.ID+"/reasoning-effort", strings.NewReader(`{"reasoningEffort":"extreme"}`))
	invalidRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "xhigh") {
		t.Fatalf("expected xhigh-aware validation error, got %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestRefreshProviderRuntimeIdentityUnregistersDisabledProviders(t *testing.T) {
	disabled := config.ProviderConfig{Name: "disabled-relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65530/v1", APIKeyOptional: true, Model: "model", Disabled: true}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(disabled))
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{disabled}}}, nil, nil, nil, registry)
	app.refreshProviderRuntimeIdentity("123e4567-e89b-42d3-a456-426614174000")
	if _, ok := registry.Get(disabled.Name); ok {
		t.Fatal("runtime identity refresh re-registered disabled provider")
	}
}

func TestRefreshProviderRuntimeIdentityPreservesSubscriptionCredentialsAndGatewayPolicy(t *testing.T) {
	home := t.TempDir()
	database, err := db.Open(context.Background(), filepath.Join(home, "runtime-identity-subscription.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	credentialStore := subscriptionauth.NewStore(subscriptionauth.DefaultStoreDir(home, subscriptionauth.ProviderKimi))
	account, err := credentialStore.CreateOAuth(subscriptionauth.CreateRequest{
		Provider: subscriptionauth.ProviderKimi, AccessToken: "kimi-runtime-access", RefreshToken: "kimi-runtime-refresh", DeviceID: "kimi-runtime-device",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetGatewayAccountGrant(context.Background(), subscriptionauth.ProviderKimi, account.ID, true); err != nil {
		t.Fatal(err)
	}
	provider := config.NormalizeProviderConfig(config.ProviderConfig{Name: config.ProviderTypeKimi, Type: config.ProviderTypeKimi, GatewayEnabled: true})
	registry := providers.NewRegistry()
	app := New(config.Config{
		Paths:     config.PathsConfig{HomeDir: home},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}},
	}, database, nil, nil, registry)

	app.refreshProviderRuntimeIdentity("123e4567-e89b-42d3-a456-426614174000")
	adapter, ok := registry.Get(subscriptionauth.ProviderKimi)
	if !ok {
		t.Fatal("runtime identity refresh did not register Kimi Provider")
	}
	if !providers.ConfiguredForScenario(adapter, false, providers.CallScenarioInternal) {
		t.Fatal("runtime identity refresh lost the Kimi credential store")
	}
	if !providers.AvailableForScenario(context.Background(), adapter, false, providers.ScenarioAvailability{
		Scenario: providers.CallScenarioGateway, AllowSubscriptionCredentials: true,
	}) {
		t.Fatal("runtime identity refresh lost the Kimi Gateway account policy")
	}
}

func modelRuntimeTestHandler(app *Server) http.Handler {
	router := chi.NewRouter()
	router.Get("/aggregates", app.listModelAggregates)
	router.Get("/aggregates/{name}", app.getModelAggregate)
	router.Put("/aggregates/{name}", app.putModelAggregate)
	router.Delete("/aggregates/{name}", app.deleteModelAggregate)
	router.Patch("/runtime/model-settings", app.updateRuntimeModelSettings)
	router.Patch("/runtime/agent-model-settings", app.updateAgentModelSettings)
	router.Patch("/agents/{id}/reasoning", app.updateAgentReasoningEffort)
	router.Patch("/agents/{id}/fast-mode", app.updateAgentFastMode)
	router.Get("/client/identity", app.clientIdentity)
	router.Post("/client/identity/rotate", app.rotateClientIdentity)
	return router
}

func modelRuntimeRequest(handler http.Handler, method, target, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := newTestRequest(method, target, bytes.NewBufferString(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeModelAggregate(t *testing.T, response *httptest.ResponseRecorder) db.ModelAggregate {
	t.Helper()
	var aggregate db.ModelAggregate
	if err := json.NewDecoder(response.Body).Decode(&aggregate); err != nil {
		t.Fatal(err)
	}
	return aggregate
}

func jsonInt(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
