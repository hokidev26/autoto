package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/providers"
)

func TestSettingsProviderResponseNeverExposesProxyUserinfo(t *testing.T) {
	provider := config.ProviderConfig{
		Name:     "relay",
		Type:     "openai-compatible",
		BaseURL:  "https://relay.example/v1",
		Model:    "relay-model",
		ProxyURL: "http://proxy-user:proxy-pass@127.0.0.1:7890",
	}
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}, nil, nil, nil, providers.NewRegistry())
	response := app.settingsProviderResponse(context.Background(), provider)
	if response.ProxyURL != "http://127.0.0.1:7890" || strings.Contains(response.ProxyURL, "proxy-user") || strings.Contains(response.ProxyURL, "proxy-pass") {
		t.Fatalf("settings response exposed proxy userinfo: %+v", response)
	}
}

func TestProviderHeadersKeepBlankOnlyForMatchingSavedName(t *testing.T) {
	existing := config.ProviderConfig{
		RequestHeaders:       []config.ProviderRequestHeader{{Name: "X-Saved", Value: "saved-secret"}},
		RequestHeadersSource: "runtime",
	}
	matching := []providerRequestHeaderInput{{Name: "X-Saved", KeepExisting: true}}
	headers, source, err := providerHeadersFromRequest(existing, &matching, true)
	if err != nil || source != "runtime" || len(headers) != 1 || headers[0].Value != "saved-secret" {
		t.Fatalf("matching saved header was not preserved: headers=%+v source=%q err=%v", headers, source, err)
	}

	renamed := []providerRequestHeaderInput{{Name: "X-Renamed", KeepExisting: true}}
	if _, _, err := providerHeadersFromRequest(existing, &renamed, true); err == nil {
		t.Fatal("renamed header reused a saved value without a new secret")
	}
	newBlank := []providerRequestHeaderInput{{Name: "X-New"}}
	if err := validateProviderConfigRequest(providerConfigUpdateRequest{RequestHeaders: &newBlank}); err == nil {
		t.Fatal("new header without a value was accepted")
	}
}

func TestUpdateProviderConfigRegistersRuntimeProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer relay-secret" {
			t.Fatalf("missing relay api key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"relay-model"}]}`))
	}))
	defer upstream.Close()

	registry := providers.NewRegistry()
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "openai-compatible",
		Type:    "openai-compatible",
		BaseURL: "http://old.example/v1",
		Model:   "old-model",
	}}}}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	payload := []byte(`{"name":"openai-compatible","type":"openai-compatible","baseUrl":"` + upstream.URL + `/v1","apiKey":"relay-secret","model":"relay-model","models":[{"name":"relay-model","contextTokenLimit":300000}]}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/openai-compatible/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/models", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var models modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Providers) != 1 || len(models.Providers[0].Models) != 1 || models.Providers[0].Models[0] != "relay-model" || models.Providers[0].ModelCapabilities["relay-model"].ContextTokenLimit != 300000 {
		t.Fatalf("unexpected models response: %+v", models)
	}
	runtimeProvider, ok := registry.Get("openai-compatible")
	if !ok || providers.ModelCapabilitiesFor(runtimeProvider, "relay-model").ContextTokenLimit != 300000 {
		t.Fatalf("runtime provider did not receive the updated model capability")
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(written, []byte("relay-secret")) {
		t.Fatal("API key should not be persisted to disk")
	}
}

func TestUpdateProviderConfigRenamesCustomProviderAndMigratesModelReferences(t *testing.T) {
	provider := config.ProviderConfig{
		Name:    "relay",
		Type:    "openai-compatible",
		BaseURL: "http://127.0.0.1:8081/v1",
		APIKey:  "runtime-key",
		Model:   "old-model",
	}
	fallback := config.ProviderConfig{
		Name:           "fallback",
		Type:           "openai-compatible",
		BaseURL:        "http://127.0.0.1:8082/v1",
		APIKeyOptional: true,
		Model:          "fallback-model",
	}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(provider))
	registry.Register(providers.NewOpenAICompatible(fallback))
	if !registry.SetDefaultFromConfig("relay:old-model", []config.ProviderConfig{provider, fallback}) {
		t.Fatal("expected relay to become default")
	}
	app := New(config.Config{
		Agent: config.AgentConfig{
			DefaultModel:       "relay:old-model",
			SummaryModel:       "relay:summary-model",
			ReviewModel:        "relay:review-model",
			SubagentModels:     map[string]string{"explore": "relay:explore-model"},
			SubagentModelPools: map[string][]string{"general": {"relay:pool-model", "fallback:fallback-model"}},
		},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider, fallback}},
	}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	payload := []byte(`{"name":"renamed-relay","type":"openai-compatible","baseUrl":"http://127.0.0.1:8081/v1","model":"new-model"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/relay/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", recorder.Code, recorder.Body.String())
	}

	if _, ok := app.providerConfig("relay"); ok {
		t.Fatal("old provider name remained in config")
	}
	updated, ok := app.providerConfig("renamed-relay")
	if !ok || updated.Model != "new-model" {
		t.Fatalf("renamed provider missing or stale: %+v", updated)
	}
	if _, _, err := registry.Resolve("relay:old-model"); err == nil {
		t.Fatal("old provider name remained resolvable")
	}
	if _, _, err := registry.Resolve("renamed-relay:new-model"); err != nil {
		t.Fatalf("renamed provider is not resolvable: %v", err)
	}

	cfg := app.configSnapshot()
	if cfg.Agent.DefaultModel != "renamed-relay:new-model" || cfg.Agent.SummaryModel != "renamed-relay:summary-model" || cfg.Agent.ReviewModel != "renamed-relay:review-model" {
		t.Fatalf("agent model references were not migrated: %+v", cfg.Agent)
	}
	if cfg.Agent.SubagentModels["explore"] != "renamed-relay:explore-model" || cfg.Agent.SubagentModelPools["general"][0] != "renamed-relay:pool-model" {
		t.Fatalf("subagent model references were not migrated: %+v", cfg.Agent)
	}
}

func TestUpdateProviderConfigRejectsProviderRenameConflict(t *testing.T) {
	relay := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8081/v1", APIKeyOptional: true, Model: "relay-model"}
	occupied := config.ProviderConfig{Name: "occupied", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8082/v1", APIKeyOptional: true, Model: "occupied-model"}
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{relay, occupied}}}, nil, nil, nil, providers.NewRegistry())

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/relay/config", strings.NewReader(`{"name":"occupied","type":"openai-compatible","baseUrl":"http://127.0.0.1:8081/v1","model":"relay-model"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected rename conflict, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, ok := app.providerConfig("relay"); !ok {
		t.Fatal("rename conflict removed the original provider")
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodPut, "/api/providers/occupied/config", strings.NewReader(`{"name":"occupied","type":"openai-compatible","baseUrl":"http://127.0.0.1:9999/v1","model":"overwritten","createOnly":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected create-only conflict, got %d: %s", recorder.Code, recorder.Body.String())
	}
	stillOccupied, ok := app.providerConfig("occupied")
	if !ok || stillOccupied.BaseURL != occupied.BaseURL || stillOccupied.Model != occupied.Model {
		t.Fatalf("create-only conflict mutated existing provider: %+v", stillOccupied)
	}
}

func TestUpdateProviderConfigPreservesRuntimeAPIKeyWhenBlank(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer runtime-secret" {
			t.Fatalf("expected preserved runtime api key, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	defer upstream.Close()

	registry := providers.NewRegistry()
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "openai-compatible",
		Type:    "openai-compatible",
		BaseURL: upstream.URL + "/v1",
		APIKey:  "runtime-secret",
		Model:   "old-model",
	}}}}, nil, nil, nil, registry)

	payload := []byte(`{"name":"openai-compatible","type":"openai-compatible","baseUrl":"` + upstream.URL + `/v1","model":"new-model"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/openai-compatible/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/models", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestUpdateProviderConfigDoesNotMutateRuntimeWhenSaveFails(t *testing.T) {
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name:    "openai-compatible",
		Type:    "openai-compatible",
		BaseURL: "http://old.example/v1",
		Model:   "old-model",
	}}}}, nil, nil, nil, providers.NewRegistry())
	app.SetConfigPath(t.TempDir())

	payload := []byte(`{"name":"openai-compatible","type":"openai-compatible","baseUrl":"http://127.0.0.1:65534/v1","apiKey":"secret","model":"new-model"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/openai-compatible/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/settings", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Providers []config.ProviderSummary `json:"providers"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Providers) != 1 || body.Providers[0].BaseURL != "http://old.example/v1" || body.Providers[0].Model != "old-model" {
		t.Fatalf("provider config mutated despite save failure: %+v", body.Providers)
	}
}

func TestUpdateProviderConfigCreatesCustomProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openai/v1/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer groq-secret" {
			t.Fatalf("missing custom provider api key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openai/gpt-oss-20b"}]}`))
	}))
	defer upstream.Close()

	registry := providers.NewRegistry()
	app := New(config.Config{Providers: config.ProvidersConfig{}}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	payload := []byte(`{"name":"groq","type":"openai-compatible","baseUrl":"` + upstream.URL + `/openai/v1","apiKey":"groq-secret","model":"openai/gpt-oss-20b"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/groq/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/models", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var models modelsResponse
	if err := json.NewDecoder(recorder.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}
	if len(models.Providers) != 1 {
		t.Fatalf("expected one provider, got %+v", models.Providers)
	}
	provider := models.Providers[0]
	if provider.Name != "groq" || provider.Type != "openai-compatible" || !provider.Configured || provider.Origin != config.ProviderOriginCustom || !provider.Enabled {
		t.Fatalf("unexpected custom provider response: %+v", provider)
	}
	if len(provider.Models) != 1 || provider.Models[0] != "openai/gpt-oss-20b" {
		t.Fatalf("unexpected custom provider models: %+v", provider.Models)
	}

	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(written, []byte("groq-secret")) {
		t.Fatal("custom provider API key should not be persisted to disk")
	}
	if !bytes.Contains(written, []byte("groq")) {
		t.Fatalf("expected custom provider to be persisted, got %s", string(written))
	}
}

func TestProviderConfigUpdatePreservesProfileWithoutNameBasedAPIKeyOverride(t *testing.T) {
	existing := config.ProviderConfig{Name: "local-codex", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, Model: "gpt-5.5"}
	updated, err := providerConfigFromUpdateRequest("local-codex", existing, providerConfigUpdateRequest{
		Name:    "local-codex",
		Type:    "openai-compatible",
		BaseURL: "http://127.0.0.1:8317/v1",
		Model:   "gpt-5.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Profile != config.ProviderProfileCLIProxyAPI {
		t.Fatalf("expected profile preservation, got %+v", updated)
	}
	if !updated.APIKeyOptional {
		t.Fatalf("CLIProxyAPI profile must retain its explicit optional-key requirement, got %+v", updated)
	}
}

func TestProviderConfigUpdateModelsOmittedPreservesAndProvidedReplaces(t *testing.T) {
	existing := config.ProviderConfig{
		Name:      "relay",
		Type:      "openai-compatible",
		BaseURL:   "http://127.0.0.1:8080/v1",
		Model:     "old-default",
		Models:    []config.ProviderModelConfig{{Name: "saved-model", ContextTokenLimit: 111111, ImageGeneration: true}},
		MaxTokens: 777,
	}
	omitted, err := providerConfigFromUpdateRequest("relay", existing, providerConfigUpdateRequest{
		Name: "relay", Type: "openai-compatible", BaseURL: existing.BaseURL, Model: "new-default",
	})
	if err != nil {
		t.Fatal(err)
	}
	if omitted.MaxTokens != existing.MaxTokens {
		t.Fatalf("omitted models update changed MaxTokens: %d", omitted.MaxTokens)
	}
	if len(omitted.Models) != 2 || omitted.Models[0] != existing.Models[0] || omitted.Models[1].Name != "new-default" {
		t.Fatalf("omitted models did not preserve saved models and add the default: %+v", omitted.Models)
	}

	replacement := []config.ProviderModelConfig{{Name: " replacement ", ContextTokenLimit: 222222, ImageGeneration: true}}
	replaced, err := providerConfigFromUpdateRequest("relay", existing, providerConfigUpdateRequest{
		Name: "relay", Type: "openai-compatible", BaseURL: existing.BaseURL, Model: "new-default", Models: &replacement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.MaxTokens != existing.MaxTokens {
		t.Fatalf("provided models update changed MaxTokens: %d", replaced.MaxTokens)
	}
	if len(replaced.Models) != 2 || replaced.Models[0].Name != "replacement" || replaced.Models[0].ContextTokenLimit != 222222 || !replaced.Models[0].ImageGeneration || replaced.Models[1].Name != "new-default" {
		t.Fatalf("provided models did not replace and normalize the catalog: %+v", replaced.Models)
	}
}

func TestProviderConfigRequestValidatesModels(t *testing.T) {
	valid := []config.ProviderModelConfig{{Name: " model-a ", ContextTokenLimit: config.ProviderModelContextTokenLimitMax}}
	if err := validateProviderConfigRequest(providerConfigUpdateRequest{Models: &valid}); err != nil {
		t.Fatalf("valid models rejected: %v", err)
	}
	for _, models := range [][]config.ProviderModelConfig{
		{{Name: "model-a"}, {Name: " model-a "}},
		{{Name: "bad\tmodel"}},
		{{Name: strings.Repeat("m", maxProviderModelBytes+1)}},
		{{Name: "model-a", ContextTokenLimit: -1}},
		{{Name: "model-a", ContextTokenLimit: config.ProviderModelContextTokenLimitMax + 1}},
	} {
		models := models
		if err := validateProviderConfigRequest(providerConfigUpdateRequest{Models: &models}); err == nil {
			t.Fatalf("invalid models accepted: %+v", models)
		}
	}
	tooMany := make([]config.ProviderModelConfig, maxProviderModels+1)
	for i := range tooMany {
		tooMany[i].Name = fmt.Sprintf("model-%d", i)
	}
	if err := validateProviderConfigRequest(providerConfigUpdateRequest{Models: &tooMany}); err == nil {
		t.Fatal("oversized model catalog was accepted")
	}
}

func TestProviderDraftModelsDoNotPersist(t *testing.T) {
	existing := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "saved", Models: []config.ProviderModelConfig{{Name: "saved", ContextTokenLimit: 1000}}}
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{existing}}}, nil, nil, nil, providers.NewRegistry())
	draftModels := []config.ProviderModelConfig{{Name: "draft", ContextTokenLimit: 2000}}
	draft, err := app.providerConfigForDraftTest("relay", providerConfigUpdateRequest{Name: "relay", Type: "openai-compatible", BaseURL: existing.BaseURL, Model: "draft", Models: &draftModels})
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Models) != 1 || draft.Models[0].Name != "draft" || draft.Models[0].ContextTokenLimit != 2000 {
		t.Fatalf("draft models were not applied to temporary config: %+v", draft.Models)
	}
	stored := app.configSnapshot().Providers.Instances[0]
	if stored.Model != existing.Model || len(stored.Models) != 1 || stored.Models[0] != existing.Models[0] {
		t.Fatalf("draft model test mutated saved config: %+v", stored)
	}
}

func TestProviderConfigUpdateRejectsOAuthProxyGatewayEligibility(t *testing.T) {
	enabled := true
	_, err := providerConfigFromUpdateRequest("cliproxyapi", config.ProviderConfig{}, providerConfigUpdateRequest{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        "http://127.0.0.1:8317/v1",
		Model:          "gpt-5.5",
		GatewayEnabled: &enabled,
	})
	if err == nil || !strings.Contains(err.Error(), "OAuth") {
		t.Fatalf("expected OAuth proxy Gateway rejection, got %v", err)
	}
}

func TestUpdateProviderConfigRejectsUnsafeCodexBaseURL(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil, providers.NewRegistry())
	for _, baseURL := range []string{"http://chatgpt.com/backend-api/codex", "https://evil.example/backend-api/codex", "https://chatgpt.com/other"} {
		payload, _ := json.Marshal(providerConfigUpdateRequest{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: baseURL, Model: "gpt-test"})
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPut, "/api/providers/codex/config", bytes.NewReader(payload))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unsafe Codex Base URL %q accepted: %d %s", baseURL, recorder.Code, recorder.Body.String())
		}
	}
}

func TestUpdateProviderConfigRejectsInvalidProviderName(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil, providers.NewRegistry())

	payload := []byte(`{"name":"bad name","type":"openai-compatible","baseUrl":"http://example.com/v1","model":"model"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPut, "/api/providers/bad%20name/config", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderConfigUpdateAcceptsGeminiInteractions(t *testing.T) {
	provider, err := providerConfigFromUpdateRequest("gemini", config.ProviderConfig{}, providerConfigUpdateRequest{Name: "gemini", Type: "gemini-interactions", APIKey: "runtime-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if provider.BaseURL != "https://generativelanguage.googleapis.com/v1beta/interactions" || provider.Model != "gemini-2.5-pro" {
		t.Fatalf("unexpected Gemini defaults: %+v", provider)
	}
	if _, err := providers.NewProvider(provider); err != nil {
		t.Fatalf("Gemini provider should register: %v", err)
	}
}

func TestPatchProviderConfigDisablesAndReenablesRuntimeProvider(t *testing.T) {
	relay := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8081/v1", APIKey: "lifecycle-fixture-key", Model: "relay-old"}
	fallback := config.ProviderConfig{Name: "fallback", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8082/v1", APIKeyOptional: true, Model: "fallback-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(relay))
	registry.Register(providers.NewOpenAICompatible(fallback))
	if !registry.SetDefaultFromConfig("relay:relay-old", []config.ProviderConfig{relay, fallback}) {
		t.Fatal("expected relay to become default")
	}
	app := New(config.Config{
		Agent:     config.AgentConfig{DefaultModel: "relay:relay-old"},
		Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{relay, fallback}},
	}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	patch := func(body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPatch, "/api/providers/relay", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}

	disabled := patch(`{"enabled":false,"model":"relay-disabled"}`)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable failed: %d %s", disabled.Code, disabled.Body.String())
	}
	var disabledBody providerConfigUpdateResponse
	if err := json.NewDecoder(disabled.Body).Decode(&disabledBody); err != nil {
		t.Fatal(err)
	}
	if disabledBody.Provider.Enabled || disabledBody.Provider.Origin != config.ProviderOriginCustom || disabledBody.Provider.Model != "relay-disabled" {
		t.Fatalf("unexpected disable response: %+v", disabledBody)
	}
	if _, _, err := registry.Resolve("relay:relay-disabled"); err == nil {
		t.Fatal("disabled provider must be removed from runtime resolution")
	}
	defaultProvider, err := registry.Default()
	if err != nil || defaultProvider.Name() != "fallback" {
		t.Fatalf("expected safe default fallback, provider=%v err=%v", defaultProvider, err)
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(persisted, []byte(`"disabled": true`)) || bytes.Contains(persisted, []byte("lifecycle-fixture-key")) {
		t.Fatalf("unexpected persisted lifecycle config: %s", persisted)
	}
	if spoofed := patch(`{"enabled":true,"origin":"builtin"}`); spoofed.Code != http.StatusBadRequest {
		t.Fatalf("client must not control provider origin: %d %s", spoofed.Code, spoofed.Body.String())
	}

	enabled := patch(`{"enabled":true}`)
	if enabled.Code != http.StatusOK {
		t.Fatalf("enable failed: %d %s", enabled.Code, enabled.Body.String())
	}
	if _, _, err := registry.Resolve("relay:relay-disabled"); err != nil {
		t.Fatalf("reenabled provider must resolve: %v", err)
	}
	defaultProvider, err = registry.Default()
	if err != nil || defaultProvider.Name() != "relay" {
		t.Fatalf("expected preferred default to be restored, provider=%v err=%v", defaultProvider, err)
	}
}

func TestPatchProviderGatewayEligibility(t *testing.T) {
	relay := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8081/v1", APIKeyOptional: true, Model: "relay-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(relay))
	if !registry.SetDefaultFromConfig("relay:relay-model", []config.ProviderConfig{relay}) {
		t.Fatal("expected relay default")
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "relay:relay-model"}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{relay}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, "/api/providers/relay", strings.NewReader(`{"gatewayEnabled":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("enable gateway provider: %d %s", recorder.Code, recorder.Body.String())
	}
	var response providerConfigUpdateResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Provider.GatewayEnabled {
		t.Fatalf("gateway eligibility was not persisted: %+v", response.Provider)
	}

	codex := config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "https://chatgpt.com/backend-api/codex", Model: "gpt-test"}
	codexApp := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{codex}}}, nil, nil, nil)
	codexResponse := httptest.NewRecorder()
	codexRequest := newTestRequest(http.MethodPatch, "/api/providers/codex", strings.NewReader(`{"gatewayEnabled":true}`))
	codexRequest.Header.Set("Content-Type", "application/json")
	codexRequest.Header.Set(localTokenHeader, codexApp.localToken)
	codexApp.Routes().ServeHTTP(codexResponse, codexRequest)
	if codexResponse.Code != http.StatusOK {
		t.Fatalf("Codex gateway eligibility should now be accepted: %d %s", codexResponse.Code, codexResponse.Body.String())
	}

	proxy := config.ProviderConfig{Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI, BaseURL: "http://127.0.0.1:8317/v1", Model: "gpt-test", APIKeyOptional: true}
	proxyApp := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{proxy}}}, nil, nil, nil)
	proxyDenied := httptest.NewRecorder()
	proxyRequest := newTestRequest(http.MethodPatch, "/api/providers/cliproxyapi", strings.NewReader(`{"gatewayEnabled":true}`))
	proxyRequest.Header.Set("Content-Type", "application/json")
	proxyRequest.Header.Set(localTokenHeader, proxyApp.localToken)
	proxyApp.Routes().ServeHTTP(proxyDenied, proxyRequest)
	if proxyDenied.Code != http.StatusBadRequest {
		t.Fatalf("OAuth proxy gateway eligibility must be rejected: %d %s", proxyDenied.Code, proxyDenied.Body.String())
	}
}

func TestProviderLifecycleRejectsRemovingOnlyDefault(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		body   string
	}{
		{name: "disable", method: http.MethodPatch, body: `{"enabled":false}`},
		{name: "delete", method: http.MethodDelete},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8081/v1", APIKeyOptional: true, Model: "relay-model"}
			registry := providers.NewRegistry()
			registry.Register(providers.NewOpenAICompatible(provider))
			if !registry.SetDefaultFromConfig("relay:relay-model", []config.ProviderConfig{provider}) {
				t.Fatal("expected relay default")
			}
			app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "relay:relay-model"}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}, nil, nil, nil, registry)
			recorder := httptest.NewRecorder()
			request := newTestRequest(test.method, "/api/providers/relay", strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			request.Header.Set(localTokenHeader, app.localToken)
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
			}
			if _, _, err := registry.Resolve("relay:relay-model"); err != nil {
				t.Fatalf("failed mutation must leave provider resolvable: %v", err)
			}
		})
	}
}

func TestDeleteProviderOnlyAllowsCustomProviders(t *testing.T) {
	registry := providers.NewRegistry()
	custom := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8081/v1", APIKey: "delete-fixture-key", Model: "relay-model"}
	registry.Register(providers.NewOpenAICompatible(custom))
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "openai", Type: "openai", Model: "gpt-test"},
		{Name: "anthropic", Type: "anthropic", Model: "claude-test"},
		{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "https://chatgpt.com/backend-api/codex", Model: "gpt-test"},
		{Name: "ollama", Type: "openai-compatible", BaseURL: "http://127.0.0.1:11434/v1", APIKeyOptional: true, Model: "llama3.2"},
		{Name: "openai-compatible", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "gpt-test"},
		custom,
	}}}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	deleteProvider := func(name string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodDelete, "/api/providers/"+name, nil)
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	for _, name := range []string{"openai", "anthropic", "codex", "ollama", "openai-compatible"} {
		if recorder := deleteProvider(name); recorder.Code != http.StatusConflict {
			t.Fatalf("built-in %s delete status=%d body=%s", name, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := deleteProvider("missing"); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing provider delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	deleted := deleteProvider("relay")
	if deleted.Code != http.StatusOK {
		t.Fatalf("custom delete failed: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, _, err := registry.Resolve("relay:relay-model"); err == nil {
		t.Fatal("deleted custom provider must be unregistered")
	}
	if _, ok := app.providerConfig("relay"); ok {
		t.Fatal("deleted custom provider remained in config")
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("relay")) || bytes.Contains(persisted, []byte("delete-fixture-key")) {
		t.Fatalf("deleted provider or key persisted unexpectedly: %s", persisted)
	}
}

func TestSavedProviderTestUsesSavedDisabledConfigAndRedactsFailures(t *testing.T) {
	var savedConfigCalls, bodyConfigCalls, deniedCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/saved/v1/models":
			savedConfigCalls++
			if r.Header.Get("Authorization") != "Bearer provider-fixture-key" {
				t.Fatalf("saved provider key was not used")
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"},{"id":"model-a"}]}`))
		case "/body/v1/models":
			bodyConfigCalls++
			w.WriteHeader(http.StatusInternalServerError)
		case "/denied/v1/models":
			deniedCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Authorization: Bearer provider-fixture-key token=provider-fixture-key"))
		default:
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{
		{Name: "saved", Type: "openai-compatible", BaseURL: upstream.URL + "/saved/v1", APIKey: "provider-fixture-key", Model: "fallback", Disabled: true},
		{Name: "denied", Type: "openai-compatible", BaseURL: upstream.URL + "/denied/v1", APIKey: "provider-fixture-key", Model: "fallback"},
	}}}, nil, nil, nil, providers.NewRegistry())

	testProvider := func(name, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPost, "/api/providers/"+name+"/test", strings.NewReader(body))
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		return recorder
	}
	success := testProvider("saved", "")
	if success.Code != http.StatusOK {
		t.Fatalf("saved provider test failed: %d %s", success.Code, success.Body.String())
	}
	var result providerTestResponse
	if err := json.NewDecoder(success.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || !result.Configured || result.ModelCount != 2 || result.ErrorCode != "" {
		t.Fatalf("unexpected successful test response: %+v", result)
	}
	if strings.Join(result.Models, ",") != "model-a,model-b" {
		t.Fatalf("successful test did not return normalized models: %+v", result.Models)
	}
	if savedConfigCalls != 1 || bodyConfigCalls != 0 {
		t.Fatalf("test must use only saved config, saved=%d body=%d", savedConfigCalls, bodyConfigCalls)
	}
	if _, ok := app.providers.Get("saved"); ok {
		t.Fatal("testing a disabled provider must not permanently register it")
	}
	if saved, ok := app.providerConfig("saved"); !ok || !saved.Disabled {
		t.Fatalf("testing disabled provider changed config: %+v", saved)
	}

	bodyAttempt := testProvider("saved", `{"baseUrl":"`+upstream.URL+`/body/v1","apiKey":"attacker-controlled"}`)
	if bodyAttempt.Code != http.StatusBadRequest || bodyConfigCalls != 0 {
		t.Fatalf("provider test must reject request-controlled endpoints: %d %s calls=%d", bodyAttempt.Code, bodyAttempt.Body.String(), bodyConfigCalls)
	}

	denied := testProvider("denied", "")
	if denied.Code != http.StatusOK {
		t.Fatalf("denied provider test status=%d body=%s", denied.Code, denied.Body.String())
	}
	if err := json.NewDecoder(denied.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Reachable || result.ErrorCode != "authentication_failed" || strings.Contains(denied.Body.String(), "provider-fixture-key") || strings.Contains(denied.Body.String(), "Authorization") {
		t.Fatalf("provider test leaked or misclassified failure: %+v body=%s", result, denied.Body.String())
	}
	if deniedCalls != 1 {
		t.Fatalf("expected one denied upstream call, got %d", deniedCalls)
	}
}

func TestProviderTestsSkipMissingRequiredAPIKey(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		t.Fatalf("missing API Key must not reach upstream: %s", r.URL.String())
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "saved-required", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", Model: "required-model",
	}}}}, nil, nil, nil, providers.NewRegistry())

	assertNotConfigured := func(label string, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", label, recorder.Code, recorder.Body.String())
		}
		var result providerTestResponse
		if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Configured || result.Reachable || result.ModelCount != 0 || result.ErrorCode != "not_configured" {
			t.Fatalf("%s returned an invalid missing-key result: %+v", label, result)
		}
		if result.Message != "需要 API Key，尚未执行连接预检。" || strings.Contains(result.Message, "http") {
			t.Fatalf("%s returned an unsafe missing-key message: %q", label, result.Message)
		}
	}

	saved := httptest.NewRecorder()
	savedRequest := newTestRequest(http.MethodPost, "/api/providers/saved-required/test", nil)
	savedRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(saved, savedRequest)
	assertNotConfigured("saved", saved)

	draft := httptest.NewRecorder()
	draftRequest := newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(`{"name":"draft-required","type":"openai-compatible","baseUrl":"`+upstream.URL+`/v1","model":"required-model"}`))
	draftRequest.Header.Set("Content-Type", "application/json")
	draftRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(draft, draftRequest)
	assertNotConfigured("draft", draft)

	if upstreamCalls != 0 {
		t.Fatalf("missing API Key tests reached upstream %d times", upstreamCalls)
	}
}

func TestProviderTestsDoNotMisreportOfficialOpenAIWithoutAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		t.Fatalf("unconfigured official OpenAI provider must not reach upstream: %s", r.URL.String())
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "custom-openai", Type: "openai", BaseURL: upstream.URL + "/v1", Model: "gpt-test", APIKeyOptional: true,
	}}}}, nil, nil, nil, providers.NewRegistry())

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/custom-openai/test", nil)
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result providerTestResponse
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Configured || result.Reachable || result.ModelCount != 0 || result.ErrorCode != "not_configured" {
		t.Fatalf("official OpenAI provider was incorrectly reported usable without a key: %+v", result)
	}
	if upstreamCalls != 0 {
		t.Fatalf("unconfigured official OpenAI test reached upstream %d times", upstreamCalls)
	}
}

func TestProviderTestsAllowOptionalAPIKey(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("optional-key test unexpectedly sent authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"optional-model"}]}`))
	}))
	defer upstream.Close()

	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "saved-optional", Type: "openai-compatible", BaseURL: upstream.URL + "/v1", Model: "optional-model", APIKeyOptional: true,
	}}}}, nil, nil, nil, providers.NewRegistry())

	assertTested := func(label string, recorder *httptest.ResponseRecorder) {
		t.Helper()
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", label, recorder.Code, recorder.Body.String())
		}
		var result providerTestResponse
		if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if !result.Configured || !result.Reachable || result.ModelCount != 1 || result.ErrorCode != "" {
			t.Fatalf("%s did not run the optional-key test: %+v", label, result)
		}
	}

	saved := httptest.NewRecorder()
	savedRequest := newTestRequest(http.MethodPost, "/api/providers/saved-optional/test", nil)
	savedRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(saved, savedRequest)
	assertTested("saved", saved)

	draft := httptest.NewRecorder()
	draftRequest := newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(`{"name":"draft-optional","type":"openai-compatible","baseUrl":"`+upstream.URL+`/v1","model":"optional-model","apiKeyOptional":true}`))
	draftRequest.Header.Set("Content-Type", "application/json")
	draftRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(draft, draftRequest)
	assertTested("draft", draft)

	if upstreamCalls != 2 {
		t.Fatalf("optional API Key tests should reach upstream twice, got %d", upstreamCalls)
	}
}

func TestProviderNetworkSettingsApplyToDiscoveryMessageTestAndRuntime(t *testing.T) {
	const (
		proxyUser   = "proxy-user"
		proxyPass   = "proxy-pass"
		tenantValue = "tenant-secret"
		userAgent   = "Autoto Provider Integration/1.0"
	)
	var modelsCalls, messageCalls int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Proxy-Authorization"); got != "Basic "+base64.StdEncoding.EncodeToString([]byte(proxyUser+":"+proxyPass)) {
			t.Fatalf("unexpected proxy authorization %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Fatalf("unexpected user agent %q", got)
		}
		if got := r.Header.Get("X-Tenant"); got != tenantValue {
			t.Fatalf("unexpected tenant header %q", got)
		}
		switch r.URL.Path {
		case "/v1/models":
			modelsCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
		case "/v1/chat/completions":
			messageCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
		default:
			t.Fatalf("unexpected proxied path %s", r.URL.Path)
		}
	}))
	defer proxy.Close()

	provider := config.ProviderConfig{
		Name:          "relay",
		Type:          "openai-compatible",
		BaseURL:       "http://127.0.0.1:65535/v1",
		APIKey:        "runtime-key",
		Model:         "model-a",
		ProxyURL:      proxy.URL,
		ProxyUsername: proxyUser,
		ProxyPassword: proxyPass,
		UserAgent:     userAgent,
		RequestHeaders: []config.ProviderRequestHeader{{
			Name:  "X-Tenant",
			Value: tenantValue,
		}},
	}
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{provider}}}, nil, nil, nil, providers.NewRegistry())

	discoveryBody := `{"name":"relay","type":"openai-compatible","baseUrl":"http://127.0.0.1:65535/v1","apiKey":"draft-key","model":"model-a","proxyUrl":"` + proxy.URL[:len("http://")] + proxyUser + `:` + proxyPass + `@` + strings.TrimPrefix(proxy.URL, "http://") + `","userAgent":"` + userAgent + `","requestHeaders":[{"name":"X-Tenant","value":"` + tenantValue + `"}]}`
	discovery := httptest.NewRecorder()
	discoveryRequest := newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(discoveryBody))
	discoveryRequest.Header.Set("Content-Type", "application/json")
	discoveryRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(discovery, discoveryRequest)
	if discovery.Code != http.StatusOK {
		t.Fatalf("discovery failed: %d %s", discovery.Code, discovery.Body.String())
	}
	var discoveryResult providerTestResponse
	if err := json.NewDecoder(discovery.Body).Decode(&discoveryResult); err != nil {
		t.Fatal(err)
	}
	if !discoveryResult.Reachable || len(discoveryResult.Models) != 1 || discoveryResult.Models[0] != "model-a" {
		t.Fatalf("unexpected discovery result: %+v", discoveryResult)
	}

	messageBody := strings.TrimSuffix(discoveryBody, "}") + `,"prompt":"reply"}`
	message := httptest.NewRecorder()
	messageRequest := newTestRequest(http.MethodPost, "/api/providers/test-message", strings.NewReader(messageBody))
	messageRequest.Header.Set("Content-Type", "application/json")
	messageRequest.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(message, messageRequest)
	if message.Code != http.StatusOK {
		t.Fatalf("message test failed: %d %s", message.Code, message.Body.String())
	}
	var messageResult providerMessageTestResponse
	if err := json.NewDecoder(message.Body).Decode(&messageResult); err != nil {
		t.Fatal(err)
	}
	if !messageResult.Success || messageResult.Output != "ok" {
		t.Fatalf("unexpected message result: %+v", messageResult)
	}

	adapter, err := app.newRuntimeProvider(provider)
	if err != nil {
		t.Fatal(err)
	}
	events, err := adapter.Generate(context.Background(), providers.GenerateRequest{Model: "model-a", Messages: []providers.Message{{Role: "user", Content: "runtime"}}})
	if err != nil {
		t.Fatal(err)
	}
	var runtimeDone, runtimeError bool
	for event := range events {
		if event.Type == "done" {
			runtimeDone = true
		}
		if event.Type == "error" {
			runtimeError = true
		}
	}
	if !runtimeDone || runtimeError {
		t.Fatalf("runtime request did not complete: done=%v error=%v", runtimeDone, runtimeError)
	}
	if modelsCalls != 1 || messageCalls != 2 {
		t.Fatalf("network settings were not shared across all paths: models=%d messages=%d", modelsCalls, messageCalls)
	}
	for _, body := range []string{discovery.Body.String(), message.Body.String()} {
		for _, secret := range []string{proxyUser, proxyPass, tenantValue, "draft-key"} {
			if strings.Contains(body, secret) {
				t.Fatalf("provider response leaked %q: %s", secret, body)
			}
		}
	}
}

func TestProviderMessageTestSendsPromptWithoutMutatingDraftProvider(t *testing.T) {
	var requestBody struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/draft/v1/chat/completions" {
			t.Fatalf("unexpected message test path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer draft-secret" {
			t.Fatalf("unexpected message test authorization %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"测试回复正常"}}],"usage":{"prompt_tokens":7,"completion_tokens":3}}`))
	}))
	defer upstream.Close()

	existing := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: upstream.URL + "/saved/v1", APIKey: "runtime-secret", Model: "saved-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(existing))
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{existing}}}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + upstream.URL + `/draft/v1","apiKey":"draft-secret","model":"draft-model","prompt":"请回复测试结果"}`
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/test-message", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("message test failed: %d %s", recorder.Code, recorder.Body.String())
	}
	var result providerMessageTestResponse
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Model != "draft-model" || result.Output != "测试回复正常" || result.Usage == nil || result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 3 {
		t.Fatalf("unexpected message test response: %+v", result)
	}
	if requestBody.Model != "draft-model" || len(requestBody.Messages) != 1 || requestBody.Messages[0].Role != "user" || requestBody.Messages[0].Content != "请回复测试结果" {
		t.Fatalf("unexpected upstream message test request: %+v", requestBody)
	}
	if strings.Contains(recorder.Body.String(), "draft-secret") || strings.Contains(recorder.Body.String(), "Authorization") {
		t.Fatalf("message test leaked credentials: %s", recorder.Body.String())
	}
	stored, ok := app.providerConfig("relay")
	if !ok || stored.BaseURL != existing.BaseURL || stored.Model != existing.Model || stored.APIKey != existing.APIKey {
		t.Fatalf("message test mutated saved provider: %+v", stored)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "relay" {
		t.Fatalf("message test mutated registry: %v", names)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("message test must not persist config, stat error=%v", err)
	}
}

func TestProviderMessageTestRejectsMissingAPIKeyWithoutUpstreamCall(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		t.Fatalf("missing key message test reached upstream: %s", r.URL.Path)
	}))
	defer upstream.Close()
	app := New(config.Config{}, nil, nil, nil, providers.NewRegistry())
	payload := `{"name":"draft-required","type":"openai-compatible","baseUrl":"` + upstream.URL + `/v1","model":"draft-model","prompt":"test"}`
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/test-message", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("missing key message test status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var result providerMessageTestResponse
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Success || result.ErrorCode != "not_configured" || result.Output != "" {
		t.Fatalf("unexpected missing key result: %+v", result)
	}
	if upstreamCalls != 0 {
		t.Fatalf("missing key message test reached upstream %d times", upstreamCalls)
	}
}

func TestProviderDraftTestDoesNotReuseExistingRuntimeKeyAcrossBinding(t *testing.T) {
	var savedBindingCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/saved/v1/models" {
			t.Fatalf("draft reached changed binding %s", r.URL.Path)
		}
		savedBindingCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer runtime-only-key" {
			t.Fatalf("same-binding draft did not reuse in-memory key: %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"draft-model"}]}`))
	}))
	defer upstream.Close()

	existing := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: upstream.URL + "/saved/v1", APIKey: "runtime-only-key", Model: "saved-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(existing))
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{existing}}}, nil, nil, nil, registry)
	configPath := filepath.Join(t.TempDir(), "config.json")
	app.SetConfigPath(configPath)

	payload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + upstream.URL + `/draft/v1","model":"draft-model"}`
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("draft test failed: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "runtime-only-key") || strings.Contains(recorder.Body.String(), "Authorization") {
		t.Fatalf("draft test leaked a credential: %s", recorder.Body.String())
	}
	var result providerTestResponse
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Configured || result.Reachable || result.ErrorCode != "not_configured" {
		t.Fatalf("changed-binding draft unexpectedly reused an existing runtime key: %+v", result)
	}
	if savedBindingCalls != 0 {
		t.Fatalf("changed-binding draft reached upstream %d times", savedBindingCalls)
	}

	sameBindingPayload := `{"name":"relay","type":"openai-compatible","baseUrl":"` + upstream.URL + `/saved/v1","model":"draft-model"}`
	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(sameBindingPayload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("same-binding draft test failed: %d %s", recorder.Code, recorder.Body.String())
	}
	result = providerTestResponse{}
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if !result.Configured || !result.Reachable || result.ErrorCode != "" || savedBindingCalls != 1 {
		t.Fatalf("same-binding draft did not reuse the existing runtime key: result=%+v calls=%d", result, savedBindingCalls)
	}
	stored, ok := app.providerConfig("relay")
	if !ok || stored.BaseURL != existing.BaseURL || stored.Model != existing.Model || stored.APIKey != "runtime-only-key" {
		t.Fatalf("draft altered saved runtime configuration: %+v", stored)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "relay" {
		t.Fatalf("draft altered registry: %v", names)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("draft test must not persist config, stat error=%v", err)
	}

	for _, body := range []string{
		`{"name":"relay","type":"openai-compatible","baseUrl":"http://169.254.169.254/v1","apiKey":"draft-secret","model":"draft-model"}`,
		`{"name":"relay","type":"openai-compatible","baseUrl":"http://user:password@127.0.0.1:8080/v1","apiKey":"draft-secret","model":"draft-model"}`,
		`{"name":"relay","type":"openai-compatible","baseUrl":"http://8.8.8.8/v1","apiKey":"draft-secret","model":"draft-model"}`,
		`{"name":"relay","type":"openai-compatible","baseUrl":"` + upstream.URL + `/draft/v1","apiKey":"draft-secret","model":"draft-model","unknown":true}`,
	} {
		recorder = httptest.NewRecorder()
		request = newTestRequest(http.MethodPost, "/api/providers/test", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("unsafe draft accepted: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "draft-secret") || strings.Contains(recorder.Body.String(), "password") {
			t.Fatalf("draft validation leaked secret: %s", recorder.Body.String())
		}
	}
	if savedBindingCalls != 1 {
		t.Fatalf("rejected drafts reached upstream; saved-binding calls=%d", savedBindingCalls)
	}
}

func TestSavedProviderTestRejectsChunkedBody(t *testing.T) {
	app := New(config.Config{Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{{
		Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65534/v1", APIKey: "fixture-key", Model: "model",
	}}}}, nil, nil, nil, providers.NewRegistry())
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/providers/relay/test", strings.NewReader(`{}`))
	request.ContentLength = -1
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("chunked saved test body accepted: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderLifecycleRequiresARealConfiguredFallback(t *testing.T) {
	primary := config.ProviderConfig{Name: "primary", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65531/v1", APIKey: "primary-key", Model: "primary-model"}
	unconfigured := config.ProviderConfig{Name: "unconfigured", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65532/v1", Model: "fallback-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(primary))
	registry.Register(providers.NewOpenAICompatible(unconfigured))
	if !registry.SetDefaultFromConfig("primary:primary-model", []config.ProviderConfig{primary, unconfigured}) {
		t.Fatal("expected configured primary default")
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "primary:primary-model"}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{primary, unconfigured}}}, nil, nil, nil, registry)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPatch, "/api/providers/primary", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(localTokenHeader, app.localToken)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("unconfigured fallback must be rejected: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, _, err := registry.Resolve("primary:primary-model"); err != nil {
		t.Fatalf("failed mutation removed primary runtime adapter: %v", err)
	}
}

func TestProviderConfigAllowsOrdinaryOptionalKeyToBeCleared(t *testing.T) {
	existing := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:8080/v1", Model: "model", APIKeyOptional: true}
	updated, err := providerConfigFromUpdateRequest("relay", existing, providerConfigUpdateRequest{Name: "relay", Type: "openai-compatible", BaseURL: existing.BaseURL, Model: existing.Model, APIKeyOptional: false})
	if err != nil {
		t.Fatal(err)
	}
	if updated.APIKeyOptional {
		t.Fatalf("ordinary provider must allow true-to-false APIKeyOptional transition: %+v", updated)
	}
}

func TestConcurrentProviderPUTsKeepAllMutations(t *testing.T) {
	app := New(config.Config{}, nil, nil, nil, providers.NewRegistry())
	app.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	const count = 16
	start := make(chan struct{})
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			name := fmt.Sprintf("relay-%02d", index)
			payload := fmt.Sprintf(`{"name":%q,"type":"openai-compatible","baseUrl":"http://127.0.0.1:%d/v1","apiKey":"key-%d","model":"model-%d"}`, name, 64000+index, index, index)
			recorder := httptest.NewRecorder()
			request := newTestRequest(http.MethodPut, "/api/providers/"+name+"/config", strings.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(localTokenHeader, app.localToken)
			app.Routes().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				errs <- fmt.Errorf("%s status=%d body=%s", name, recorder.Code, recorder.Body.String())
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	cfg := app.configSnapshot()
	if len(cfg.Providers.Instances) != count {
		t.Fatalf("concurrent updates lost providers: got %d want %d", len(cfg.Providers.Instances), count)
	}
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("relay-%02d", i)
		if _, ok := app.providerConfig(name); !ok {
			t.Fatalf("missing concurrently written provider %q", name)
		}
	}
}

func TestProviderMutationLockPreventsStalePutFromRevivingDelete(t *testing.T) {
	existing := config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65520/v1", APIKey: "fixture-key", Model: "old-model"}
	fallback := config.ProviderConfig{Name: "fallback", Type: "openai-compatible", BaseURL: "http://127.0.0.1:65521/v1", APIKeyOptional: true, Model: "fallback-model"}
	registry := providers.NewRegistry()
	registry.Register(providers.NewOpenAICompatible(existing))
	registry.Register(providers.NewOpenAICompatible(fallback))
	if !registry.SetDefaultFromConfig("fallback:fallback-model", []config.ProviderConfig{existing, fallback}) {
		t.Fatal("expected configured fallback default")
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "fallback:fallback-model"}, Providers: config.ProvidersConfig{Instances: []config.ProviderConfig{existing, fallback}}}, nil, nil, nil, registry)
	app.SetConfigPath(filepath.Join(t.TempDir(), "config.json"))
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	app.providerMutationHook = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}

	putDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodPut, "/api/providers/relay/config", strings.NewReader(`{"name":"relay","type":"openai-compatible","baseUrl":"http://127.0.0.1:65520/v1","model":"new-model"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		putDone <- recorder
	}()
	<-entered

	deleteStarted := make(chan struct{})
	deleteDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(deleteStarted)
		recorder := httptest.NewRecorder()
		request := newTestRequest(http.MethodDelete, "/api/providers/relay", nil)
		request.Header.Set(localTokenHeader, app.localToken)
		app.Routes().ServeHTTP(recorder, request)
		deleteDone <- recorder
	}()
	<-deleteStarted
	select {
	case response := <-deleteDone:
		t.Fatalf("delete bypassed provider mutation lock before PUT committed: %d %s", response.Code, response.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	putResponse := <-putDone
	if putResponse.Code != http.StatusOK {
		t.Fatalf("PUT failed: %d %s", putResponse.Code, putResponse.Body.String())
	}
	deleteResponse := <-deleteDone
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("DELETE failed: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	if _, ok := app.providerConfig("relay"); ok {
		t.Fatal("stale PUT revived provider after serialized DELETE")
	}
	if _, ok := registry.Get("relay"); ok {
		t.Fatal("deleted provider remained registered")
	}
}
