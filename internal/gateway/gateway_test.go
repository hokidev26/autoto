package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

var gatewayTestNow = time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)

type gatewayTestProvider struct {
	name         string
	capabilities providers.Capabilities
	modelCaps    providers.ModelCapabilities
	events       []providers.Event
	generateErr  error
	started      chan struct{}
	release      chan struct{}
	startOnce    sync.Once

	mu             sync.Mutex
	requests       []providers.GenerateRequest
	availabilities []providers.ScenarioAvailability
}

func (p *gatewayTestProvider) Name() string { return p.name }

func (p *gatewayTestProvider) ListModels(context.Context) ([]string, error) {
	return []string{"gpt-4.1-mini"}, nil
}

func (p *gatewayTestProvider) Capabilities() providers.Capabilities {
	return p.capabilities
}

func (p *gatewayTestProvider) ModelCapabilities(string) providers.ModelCapabilities {
	return p.modelCaps
}

func (p *gatewayTestProvider) AvailableForScenario(_ context.Context, availability providers.ScenarioAvailability) bool {
	p.mu.Lock()
	p.availabilities = append(p.availabilities, availability)
	p.mu.Unlock()
	return true
}

func (p *gatewayTestProvider) Generate(ctx context.Context, request providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if p.generateErr != nil {
		return nil, p.generateErr
	}
	out := make(chan providers.Event, len(p.events)+1)
	go func() {
		defer close(out)
		if p.started != nil {
			p.startOnce.Do(func() { close(p.started) })
		}
		if p.release != nil {
			select {
			case <-p.release:
			case <-ctx.Done():
				return
			}
		}
		for _, event := range p.events {
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (p *gatewayTestProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *gatewayTestProvider) lastRequest() providers.GenerateRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return providers.GenerateRequest{}
	}
	return p.requests[len(p.requests)-1]
}

func (p *gatewayTestProvider) lastAvailability() providers.ScenarioAvailability {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.availabilities) == 0 {
		return providers.ScenarioAvailability{}
	}
	return p.availabilities[len(p.availabilities)-1]
}

type gatewayHarness struct {
	store     *db.Store
	service   *Service
	provider  *gatewayTestProvider
	key       db.GatewayKey
	generated GeneratedKey
}

type blockingGatewayBody struct {
	reader  *strings.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingGatewayBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.reader.Read(p)
}

func (b *blockingGatewayBody) Close() error { return nil }

func newGatewayHarness(t *testing.T, keyPolicy db.GatewayKey, provider *gatewayTestProvider, mutateOptions func(*Options)) gatewayHarness {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if provider == nil {
		provider = &gatewayTestProvider{
			name: "backend",
			capabilities: providers.Capabilities{
				Tools: true, Streaming: true, ImageInput: true, Reasoning: true, ReasoningEffort: true,
				ReasoningEfforts: []string{"low", "medium", "high"},
			},
			modelCaps: providers.ModelCapabilities{FastMode: true, FastModeKnown: true},
			events:    []providers.Event{{Type: "done", Done: true, StopReason: "stop"}},
		}
	}
	registry := providers.NewRegistry()
	registry.Register(provider)
	generated, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if keyPolicy.Name == "" {
		keyPolicy.Name = "Test key"
	}
	keyPolicy.KeyPrefix = generated.Prefix
	keyPolicy.TokenHash = generated.Hash
	if keyPolicy.AllowedModels == nil {
		keyPolicy.AllowedModels = []string{"shared"}
	}
	key, err := store.CreateGatewayKey(context.Background(), keyPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGatewayModel(context.Background(), db.GatewayModel{Alias: "shared", TargetModel: provider.Name() + ":gpt-4.1-mini", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	options := Options{
		MaxGlobalConcurrency:         4,
		MaxRequestBytes:              1 << 20,
		AllowSubscriptionCredentials: true,
		Now:                          func() time.Time { return gatewayTestNow },
		ProviderAllowed: func(_ context.Context, name string) bool {
			return name == provider.Name()
		},
	}
	if mutateOptions != nil {
		mutateOptions(&options)
	}
	service, err := New(store, registry, options)
	if err != nil {
		t.Fatal(err)
	}
	return gatewayHarness{store: store, service: service, provider: provider, key: key, generated: generated}
}

func gatewayRequest(t *testing.T, service *Service, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	return response
}

func validCompletionBody(extra string) string {
	body := `{"model":"shared","messages":[{"role":"user","content":"hello"}]`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func TestGenerateKeyProducesHashOnlyPersistenceMaterial(t *testing.T) {
	first, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if first.Token == second.Token || first.Hash == second.Hash {
		t.Fatal("generated gateway keys were not unique")
	}
	if !strings.HasPrefix(first.Token, generatedTokenPrefix) || first.Prefix == "" || !strings.HasPrefix(first.Token, first.Prefix) {
		t.Fatalf("unexpected generated key format: %+v", first)
	}
	if len(first.Hash) != 64 || first.Hash != HashToken(first.Token) || strings.Contains(first.Hash, first.Token) {
		t.Fatalf("unexpected generated key hash: %+v", first)
	}
	// The prefix is always sk- now, matching OpenAI-compatible client expectations.
	if !strings.HasPrefix(first.Token, "sk-") {
		t.Fatalf("generated token must start with sk-, got %q", first.Token[:min(len(first.Token), 8)])
	}
	// KeyPrefix stores the first 16 chars. Verify the prefix change didn't
	// shorten the token enough to trigger the truncation branch (token.go:28).
	// sk- is 3 chars vs autoto_ at 7; 16 chars of key prefix must still fit.
	if len(first.Prefix) < 16 {
		t.Fatalf("KeyPrefix shorter than 16 chars after prefix change: len=%d", len(first.Prefix))
	}
}

// TestOldPrefixTokensRemainValidAfterPrefixChange verifies that tokens issued
// under the old "autoto_" prefix still authenticate, because verification is
// hash-based and the prefix never participates.
func TestOldPrefixTokensRemainValidAfterPrefixChange(t *testing.T) {
	oldToken := "autoto_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	oldHash := HashToken(oldToken)
	oldPrefix := oldToken[:16]
	// Create a key that looks like it was generated under the old prefix.
	// Re-use one harness so the service and store share the same DB.
	harness := newGatewayHarness(t, db.GatewayKey{}, nil, nil)
	_, err := harness.store.CreateGatewayKey(context.Background(), db.GatewayKey{
		Name:          "legacy-key",
		KeyPrefix:     oldPrefix,
		TokenHash:     oldHash,
		Enabled:       true,
		AllowedModels: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// authenticateToken uses hash comparison — old-prefix token must still work.
	// A /v1/models call with the old token exercises the full auth path.
	rec := gatewayRequest(t, harness.service, oldToken, http.MethodGet, "/v1/models", "")
	// 401 would mean hash lookup failed; anything else (200/403/404) means the
	// token passed authentication and the prefix change was backward-compatible.
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("old-prefix token rejected after prefix change: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGatewayModelsRequireBearerAndRejectBrowserOrigin(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
	if _, err := harness.store.UpsertGatewayModel(context.Background(), db.GatewayModel{Alias: "hidden", TargetModel: "backend:gpt-4.1-mini", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	unauthorized := gatewayRequest(t, harness.service, "", http.MethodGet, "/v1/models", "")
	if unauthorized.Code != http.StatusUnauthorized || !strings.Contains(unauthorized.Body.String(), "invalid_api_key") {
		t.Fatalf("unexpected unauthorized response: %d %s", unauthorized.Code, unauthorized.Body.String())
	}
	adminToken := gatewayRequest(t, harness.service, "local-admin-token", http.MethodGet, "/v1/models", "")
	if adminToken.Code != http.StatusUnauthorized || !strings.Contains(adminToken.Body.String(), "invalid_api_key") {
		t.Fatalf("admin-style token crossed the Gateway boundary: %d %s", adminToken.Code, adminToken.Body.String())
	}

	originRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	originRequest.Header.Set("Authorization", "Bearer "+harness.generated.Token)
	originRequest.Header.Set("Origin", "https://example.test")
	originResponse := httptest.NewRecorder()
	harness.service.ServeHTTP(originResponse, originRequest)
	if originResponse.Code != http.StatusForbidden || originResponse.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("browser-origin request was not rejected safely: %d headers=%v", originResponse.Code, originResponse.Header())
	}

	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
	if response.Code != http.StatusOK {
		t.Fatalf("models response: %d %s", response.Code, response.Body.String())
	}
	var catalog modelListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.Object != "list" || len(catalog.Data) != 1 || catalog.Data[0].ID != "shared" {
		t.Fatalf("unexpected model catalog: %+v", catalog)
	}
	if strings.Contains(response.Body.String(), harness.generated.Token) || strings.Contains(response.Body.String(), harness.generated.Hash) || strings.Contains(response.Body.String(), "hidden") {
		t.Fatalf("model catalog leaked private data: %s", response.Body.String())
	}
	stored, err := harness.store.GetGatewayKey(context.Background(), harness.key.ID)
	if err != nil || stored.LastUsedAt != gatewayTestNow.Format(time.RFC3339Nano) {
		t.Fatalf("last-used timestamp was not updated: %+v %v", stored, err)
	}
}

func TestGatewayEmptyAllowedModelsIncludesAllEnabledAliases(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, AllowedModels: []string{}, RequestsPerMinute: 10}, nil, nil)
	if _, err := harness.store.UpsertGatewayModel(context.Background(), db.GatewayModel{Alias: "second", TargetModel: "backend:gpt-4.1-mini", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.store.UpsertGatewayModel(context.Background(), db.GatewayModel{Alias: "disabled", TargetModel: "backend:gpt-4.1-mini", Enabled: false}); err != nil {
		t.Fatal(err)
	}

	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
	if response.Code != http.StatusOK {
		t.Fatalf("models response: %d %s", response.Code, response.Body.String())
	}
	var catalog modelListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Data) != 2 || catalog.Data[0].ID != "second" || catalog.Data[1].ID != "shared" {
		t.Fatalf("empty whitelist did not expose all enabled aliases: %+v", catalog)
	}
}

func TestGatewayNonStreamingCompletionTranslatesAndRecordsAttribution(t *testing.T) {
	provider := &gatewayTestProvider{
		name: "backend",
		capabilities: providers.Capabilities{
			Tools: true, Streaming: true, ImageInput: true, Reasoning: true, ReasoningEffort: true,
			ReasoningEfforts: []string{"low", "medium", "high"},
		},
		modelCaps: providers.ModelCapabilities{FastMode: true, FastModeKnown: true},
		events: []providers.Event{
			{Type: "dispatch", Dispatch: &providers.DispatchInfo{Provider: "actual-backend", Model: "gpt-4.1-mini", CredentialID: "credential-1"}},
			{Type: "text", Text: "hello"},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-1", Name: "lookup", Input: json.RawMessage(`{"query":"x"}`)}},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 10, ReasoningTokens: 5}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20, MonthlyTokenLimit: 10000, MaxConcurrency: 2}, provider, nil)
	body := `{"model":"shared","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","description":"Lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}}],"reasoning_effort":"high","max_completion_tokens":128}`
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", body)
	if response.Code != http.StatusOK {
		t.Fatalf("completion response: %d %s", response.Code, response.Body.String())
	}
	var completion chatCompletionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Object != "chat.completion" || completion.Model != "shared" || len(completion.Choices) != 1 || completion.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("unexpected completion: %+v", completion)
	}
	if completion.Choices[0].Message.Content != "hello" || len(completion.Choices[0].Message.ToolCalls) != 1 || completion.Usage.TotalTokens != 120 {
		t.Fatalf("completion content/usage mismatch: %+v", completion)
	}

	captured := provider.lastRequest()
	if captured.Scenario != providers.CallScenarioGateway || !captured.AllowSubscriptionCredentials || captured.Model != "gpt-4.1-mini" || captured.SystemPrompt != "Be concise" || captured.ReasoningEffort != "high" || captured.MaxOutputTokens != 128 {
		t.Fatalf("provider request boundary mismatch: %+v", captured)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "lookup" {
		t.Fatalf("gateway injected or lost client tools: %+v", captured.Tools)
	}

	var providerName, model, credentialID, gatewayKeyID, rawDump string
	var inputTokens, outputTokens int64
	var cost float64
	err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COALESCE(provider,''), COALESCE(model,''), COALESCE(credential_id,''), COALESCE(gateway_key_id,''), input_tokens, output_tokens, cost_usd, COALESCE(raw_dump_json,'') FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&providerName, &model, &credentialID, &gatewayKeyID, &inputTokens, &outputTokens, &cost, &rawDump)
	if err != nil {
		t.Fatal(err)
	}
	if providerName != "actual-backend" || model != "gpt-4.1-mini" || credentialID != "credential-1" || gatewayKeyID != harness.key.ID || inputTokens != 100 || outputTokens != 20 || cost <= 0 || rawDump != "" {
		t.Fatalf("gateway attribution was not stored safely: provider=%q model=%q credential=%q gateway=%q in=%d out=%d cost=%f raw=%q", providerName, model, credentialID, gatewayKeyID, inputTokens, outputTokens, cost, rawDump)
	}
}

func TestGatewayStreamingCompletionEmitsOpenAIChunksAndUsage(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Streaming: true},
		events: []providers.Event{
			{Type: "dispatch", Dispatch: &providers.DispatchInfo{Provider: "backend", Model: "gpt-4.1-mini"}},
			{Type: "text", Text: "hel"},
			{Type: "text", Text: "lo"},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 7, OutputTokens: 3}},
			{Type: "done", Done: true, StopReason: "max_output_tokens"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20, MaxConcurrency: 1}, provider, nil)
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(`"stream":true,"stream_options":{"include_usage":true}`))
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream response: %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"role":"assistant"`, `"content":"hel"`, `"content":"lo"`, `"finish_reason":"length"`, `"prompt_tokens":7`, "data: [DONE]"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("stream missing %q: %s", expected, body)
		}
	}
	if strings.Count(body, `"usage"`) != 1 {
		t.Fatalf("stream usage should appear only in the final usage chunk: %s", body)
	}
	var requestCount int
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&requestCount); err != nil || requestCount != 1 {
		t.Fatalf("stream request was not recorded: count=%d err=%v", requestCount, err)
	}
}

func TestGatewayRateMonthlyAndConcurrencyLimits(t *testing.T) {
	t.Run("rpm", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 1}, nil, nil)
		first := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
		second := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
		if first.Code != http.StatusOK || second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "rate_limit_exceeded") {
			t.Fatalf("unexpected rpm responses: first=%d second=%d %s", first.Code, second.Code, second.Body.String())
		}
	})

	t.Run("monthly", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, MonthlyTokenLimit: 10}, nil, nil)
		if _, err := harness.store.AddAPIRequest(context.Background(), db.APIRequest{ID: "used", Kind: "gateway", GatewayKeyID: harness.key.ID, InputTokens: 6, OutputTokens: 4, CreatedAt: gatewayTestNow.Format(time.RFC3339Nano)}); err != nil {
			t.Fatal(err)
		}
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "monthly_token_limit_exceeded") || harness.provider.requestCount() != 0 {
			t.Fatalf("monthly limit was not enforced before dispatch: %d %s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		provider := &gatewayTestProvider{
			name:         "backend",
			capabilities: providers.Capabilities{Streaming: true},
			events:       []providers.Event{{Type: "done", Done: true}},
			started:      make(chan struct{}),
			release:      make(chan struct{}),
		}
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, MaxConcurrency: 1}, provider, nil)
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			firstDone <- gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		}()
		select {
		case <-provider.started:
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not reach provider")
		}
		second := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "concurrency_limit_exceeded") {
			close(provider.release)
			t.Fatalf("concurrency limit was not enforced: %d %s", second.Code, second.Body.String())
		}
		close(provider.release)
		select {
		case first := <-firstDone:
			if first.Code != http.StatusOK {
				t.Fatalf("first request failed: %d %s", first.Code, first.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not finish")
		}
	})

	t.Run("global concurrency", func(t *testing.T) {
		provider := &gatewayTestProvider{
			name:         "backend",
			capabilities: providers.Capabilities{Streaming: true},
			events:       []providers.Event{{Type: "done", Done: true}},
			started:      make(chan struct{}),
			release:      make(chan struct{}),
		}
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, MaxConcurrency: 2}, provider, func(options *Options) {
			options.MaxGlobalConcurrency = 1
		})
		secondGenerated, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.store.CreateGatewayKey(context.Background(), db.GatewayKey{Name: "Second", KeyPrefix: secondGenerated.Prefix, TokenHash: secondGenerated.Hash, Enabled: true, AllowedModels: []string{"shared"}, RequestsPerMinute: 10, MaxConcurrency: 2}); err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			firstDone <- gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		}()
		select {
		case <-provider.started:
		case <-time.After(5 * time.Second):
			t.Fatal("first global request did not reach provider")
		}
		second := gatewayRequest(t, harness.service, secondGenerated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		if second.Code != http.StatusTooManyRequests || !strings.Contains(second.Body.String(), "concurrency_limit_exceeded") {
			close(provider.release)
			t.Fatalf("global concurrency limit was not enforced: %d %s", second.Code, second.Body.String())
		}
		close(provider.release)
		select {
		case first := <-firstDone:
			if first.Code != http.StatusOK {
				t.Fatalf("first global request failed: %d %s", first.Code, first.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first global request did not finish")
		}
	})
}

func TestGatewayRejectsUnsupportedAndUnsafeRequestParameters(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		maxBytes   int64
		wantStatus int
	}{
		{name: "remote image", body: `{"model":"shared","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}]}`, wantStatus: http.StatusBadRequest},
		{name: "oversized", body: validCompletionBody(`"user":"` + strings.Repeat("x", 512) + `"`), maxBytes: 128, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, func(options *Options) {
				if test.maxBytes > 0 {
					options.MaxRequestBytes = test.maxBytes
				}
			})
			response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", test.body)
			if response.Code != test.wantStatus || harness.provider.requestCount() != 0 {
				t.Fatalf("unsafe request was not rejected before dispatch: status=%d body=%s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
			}
		})
	}
}

// The sampling knobs cannot be forwarded -- providers.GenerateRequest has no
// field to carry them -- but rejecting them made the endpoint useless, because
// the standard OpenAI clients set temperature on nearly every call. Dropping
// them without a word is the other way to be wrong: a caller asking for
// temperature 0 would be told nothing. So the call succeeds and the response
// names what it dropped.
func TestGatewayReportsUnforwardableSamplingParameters(t *testing.T) {
	t.Run("chat completions", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions",
			validCompletionBody(`"temperature":0.2,"top_p":0.9,"seed":7`))
		if response.Code != http.StatusOK || harness.provider.requestCount() != 1 {
			t.Fatalf("a standard sampling parameter must not fail the call: status=%d body=%s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
		}
		if got := response.Header().Get(IgnoredParametersHeader); got != "temperature, top_p, seed" {
			t.Fatalf("the response must name every dropped parameter, got %q", got)
		}
	})

	t.Run("anthropic messages", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
		response := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/messages",
			`{"model":"shared","max_tokens":64,"temperature":0.2,"top_k":5,"messages":[{"role":"user","content":"hi"}]}`,
			map[string]string{"Authorization": "Bearer " + harness.generated.Token, "anthropic-version": supportedAnthropicVersion})
		if response.Code != http.StatusOK {
			t.Fatalf("a standard sampling parameter must not fail the call: status=%d body=%s", response.Code, response.Body.String())
		}
		if got := response.Header().Get(IgnoredParametersHeader); got != "temperature, top_k" {
			t.Fatalf("the response must name every dropped parameter, got %q", got)
		}
	})

	// Nothing dropped, nothing claimed: a bare request must not carry the header
	// at all, or a client cannot use its presence as a signal.
	t.Run("silent when everything was honoured", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		if response.Code != http.StatusOK {
			t.Fatalf("plain request failed: %d %s", response.Code, response.Body.String())
		}
		if got := response.Header().Get(IgnoredParametersHeader); got != "" {
			t.Fatalf("nothing was dropped, so the header must be absent, got %q", got)
		}
	})
}

func TestGatewayRejectsRevokedDisabledAndExpiredKeys(t *testing.T) {
	t.Run("revoked", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
		if _, err := harness.store.RevokeGatewayKey(context.Background(), harness.key.ID); err != nil {
			t.Fatal(err)
		}
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("revoked key response: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("disabled", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: false, RequestsPerMinute: 10}, nil, nil)
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("disabled key response: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("expired", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, ExpiresAt: gatewayTestNow.Add(-time.Minute).Format(time.RFC3339Nano)}, nil, nil)
		response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodGet, "/v1/models", "")
		if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "expired_api_key") {
			t.Fatalf("expired key response: %d %s", response.Code, response.Body.String())
		}
	})
}

func TestGatewayRejectsAggregateContainingDisallowedProvider(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "aggregate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	allowed := &gatewayTestProvider{name: "allowed", capabilities: providers.Capabilities{Streaming: true}, events: []providers.Event{{Type: "done", Done: true}}}
	blocked := &gatewayTestProvider{name: "blocked", capabilities: providers.Capabilities{Streaming: true}, events: []providers.Event{{Type: "done", Done: true}}}
	registry := providers.NewRegistry()
	registry.Register(allowed)
	registry.Register(blocked)
	registry.SetAggregateSource(providers.AggregateSourceFunc(func(ctx context.Context, name string) (providers.AggregateDefinition, error) {
		aggregate, err := store.GetModelAggregate(ctx, name)
		if err != nil {
			return providers.AggregateDefinition{}, err
		}
		return providers.AggregateDefinition{Name: aggregate.Name, Mode: aggregate.Mode, Members: aggregate.Members}, nil
	}))
	if _, err := store.UpsertModelAggregate(ctx, db.ModelAggregate{Name: "mixed", Mode: "priority", Members: []string{"allowed:gpt-4.1-mini", "blocked:gpt-4.1-mini"}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGatewayModel(ctx, db.GatewayModel{Alias: "mixed", TargetModel: "aggregate:mixed", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateGatewayKey(ctx, db.GatewayKey{Name: "aggregate", KeyPrefix: generated.Prefix, TokenHash: generated.Hash, Enabled: true, AllowedModels: []string{"mixed"}, RequestsPerMinute: 10})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(store, registry, Options{AllowSubscriptionCredentials: true, Now: func() time.Time { return gatewayTestNow }, ProviderAllowed: func(_ context.Context, name string) bool { return name == "allowed" }})
	if err != nil {
		t.Fatal(err)
	}
	response := gatewayRequest(t, service, generated.Token, http.MethodPost, "/v1/chat/completions", `{"model":"mixed","messages":[{"role":"user","content":"hello"}]}`)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "model_not_allowed") || allowed.requestCount() != 0 || blocked.requestCount() != 0 {
		t.Fatalf("unsafe aggregate was not rejected: status=%d body=%s allowed=%d blocked=%d key=%s", response.Code, response.Body.String(), allowed.requestCount(), blocked.requestCount(), key.ID)
	}
}

func TestGatewayAggregateDispatchUsesAuthorizedSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "aggregate-snapshot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	allowed := &gatewayTestProvider{name: "allowed", capabilities: providers.Capabilities{Streaming: true}, events: []providers.Event{{Type: "text", Text: "ok"}, {Type: "done", Done: true, StopReason: "stop"}}}
	blocked := &gatewayTestProvider{name: "blocked", capabilities: providers.Capabilities{Streaming: true}, events: []providers.Event{{Type: "done", Done: true, StopReason: "stop"}}}
	registry := providers.NewRegistry()
	registry.Register(allowed)
	registry.Register(blocked)
	sourceCalls := 0
	registry.SetAggregateSource(providers.AggregateSourceFunc(func(context.Context, string) (providers.AggregateDefinition, error) {
		sourceCalls++
		return providers.AggregateDefinition{Name: "safe", Mode: "priority", Members: []string{"blocked:gpt-4.1-mini"}}, nil
	}))
	if _, err := store.UpsertModelAggregate(ctx, db.ModelAggregate{Name: "safe", Mode: "priority", Members: []string{"allowed:gpt-4.1-mini"}}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGatewayModel(ctx, db.GatewayModel{Alias: "safe", TargetModel: "aggregate:safe", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGatewayKey(ctx, db.GatewayKey{Name: "snapshot", KeyPrefix: generated.Prefix, TokenHash: generated.Hash, Enabled: true, AllowedModels: []string{"safe"}, RequestsPerMinute: 10}); err != nil {
		t.Fatal(err)
	}
	service, err := New(store, registry, Options{AllowSubscriptionCredentials: true, Now: func() time.Time { return gatewayTestNow }, ProviderAllowed: func(_ context.Context, name string) bool { return name == "allowed" }})
	if err != nil {
		t.Fatal(err)
	}
	response := gatewayRequest(t, service, generated.Token, http.MethodPost, "/v1/chat/completions", `{"model":"safe","messages":[{"role":"user","content":"hello"}]}`)
	if response.Code != http.StatusOK || allowed.requestCount() != 1 || blocked.requestCount() != 0 || sourceCalls != 0 {
		t.Fatalf("aggregate dispatch did not use the authorized snapshot: status=%d body=%s allowed=%d blocked=%d sourceCalls=%d", response.Code, response.Body.String(), allowed.requestCount(), blocked.requestCount(), sourceCalls)
	}
}

func TestGatewayHidesUnconfiguredGeminiProvider(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "gemini-unconfigured.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := providers.NewGeminiInteractions(config.ProviderConfig{Name: "gemini", Type: "gemini-interactions", Model: "gemini-test"})
	if provider.Configured() {
		t.Fatal("Gemini without an API key must not be configured")
	}
	registry := providers.NewRegistry()
	registry.Register(provider)
	if _, err := store.UpsertGatewayModel(ctx, db.GatewayModel{Alias: "gemini", TargetModel: "gemini:gemini-test", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGatewayKey(ctx, db.GatewayKey{Name: "gemini", KeyPrefix: generated.Prefix, TokenHash: generated.Hash, Enabled: true, AllowedModels: []string{"gemini"}, RequestsPerMinute: 10}); err != nil {
		t.Fatal(err)
	}
	service, err := New(store, registry, Options{AllowSubscriptionCredentials: true, Now: func() time.Time { return gatewayTestNow }, ProviderAllowed: func(_ context.Context, name string) bool { return name == "gemini" }})
	if err != nil {
		t.Fatal(err)
	}

	models := gatewayRequest(t, service, generated.Token, http.MethodGet, "/v1/models", "")
	if models.Code != http.StatusOK || strings.Contains(models.Body.String(), "gemini") {
		t.Fatalf("unconfigured Gemini model was exposed: %d %s", models.Code, models.Body.String())
	}
	completion := gatewayRequest(t, service, generated.Token, http.MethodPost, "/v1/chat/completions", `{"model":"gemini","messages":[{"role":"user","content":"hello"}]}`)
	if completion.Code != http.StatusForbidden || !strings.Contains(completion.Body.String(), "model_not_allowed") {
		t.Fatalf("unconfigured Gemini request was not rejected: %d %s", completion.Code, completion.Body.String())
	}
}

func TestGatewayRejectsAnthropicProfileOnlyProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	credentialStore := anthropicauth.NewStore(filepath.Join(root, "anthropic"))
	if _, err := credentialStore.Create(anthropicauth.CreateRequest{AuthType: anthropicauth.AuthTypeProfile, Profile: "work", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(ctx, filepath.Join(root, "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := providers.NewAnthropicProvider(config.ProviderConfig{Name: "anthropic", Type: "anthropic", CredentialStorePath: filepath.Join(root, "anthropic"), Model: "claude-sonnet-4-5"})
	if !provider.Configured() || provider.ConfiguredForScenario(providers.CallScenarioGateway) {
		t.Fatalf("unexpected Anthropic scenario readiness: internal=%v gateway=%v", provider.Configured(), provider.ConfiguredForScenario(providers.CallScenarioGateway))
	}
	registry := providers.NewRegistry()
	registry.Register(provider)
	if _, err := store.UpsertGatewayModel(ctx, db.GatewayModel{Alias: "claude", TargetModel: "anthropic:claude-sonnet-4-5", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateGatewayKey(ctx, db.GatewayKey{Name: "profile-only", KeyPrefix: generated.Prefix, TokenHash: generated.Hash, Enabled: true, AllowedModels: []string{"claude"}, RequestsPerMinute: 10}); err != nil {
		t.Fatal(err)
	}
	service, err := New(store, registry, Options{AllowSubscriptionCredentials: true, Now: func() time.Time { return gatewayTestNow }, ProviderAllowed: func(_ context.Context, name string) bool { return name == "anthropic" }})
	if err != nil {
		t.Fatal(err)
	}

	models := gatewayRequest(t, service, generated.Token, http.MethodGet, "/v1/models", "")
	if models.Code != http.StatusOK || strings.Contains(models.Body.String(), "claude") {
		t.Fatalf("profile-only Anthropic model was exposed: %d %s", models.Code, models.Body.String())
	}
	completion := gatewayRequest(t, service, generated.Token, http.MethodPost, "/v1/chat/completions", `{"model":"claude","messages":[{"role":"user","content":"hello"}]}`)
	if completion.Code != http.StatusForbidden || !strings.Contains(completion.Body.String(), "model_not_allowed") {
		t.Fatalf("profile-only Anthropic request was not rejected: %d %s", completion.Code, completion.Body.String())
	}
}

func TestConvertChatCompletionRequestPreservesImagesAndToolHistory(t *testing.T) {
	var request chatCompletionRequest
	body := `{
		"model":"shared",
		"messages":[
			{"role":"system","content":"Use the client tools only."},
			{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AQID"}}]},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call-1","name":"lookup","content":"result"}
		],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	converted, problem := convertChatCompletionRequest(request)
	if problem != nil {
		t.Fatalf("conversion failed: %+v", problem)
	}
	if converted.ProviderRequest.SystemPrompt != "Use the client tools only." || !converted.HasImages || len(converted.ProviderRequest.Messages) != 3 || len(converted.ProviderRequest.Tools) != 1 {
		t.Fatalf("unexpected converted request: %+v", converted)
	}
	userBlocks := converted.ProviderRequest.Messages[0].Blocks
	if len(userBlocks) != 2 || userBlocks[0].Type != "text" || userBlocks[1].Type != "image" || userBlocks[1].MIMEType != "image/png" || string(userBlocks[1].Data) != string([]byte{1, 2, 3}) {
		t.Fatalf("image content was not preserved: %+v", userBlocks)
	}
	assistantBlocks := converted.ProviderRequest.Messages[1].Blocks
	if len(assistantBlocks) != 1 || assistantBlocks[0].Type != "tool_use" || assistantBlocks[0].ToolUseID != "call-1" || assistantBlocks[0].ToolName != "lookup" || string(assistantBlocks[0].Input) != `{"query":"x"}` {
		t.Fatalf("assistant tool history was not preserved: %+v", assistantBlocks)
	}
	toolBlocks := converted.ProviderRequest.Messages[2].Blocks
	if len(toolBlocks) != 1 || toolBlocks[0].Type != "tool_result" || toolBlocks[0].ToolUseID != "call-1" || toolBlocks[0].Output != "result" {
		t.Fatalf("tool result history was not preserved: %+v", toolBlocks)
	}
}

func TestGatewayProviderStartErrorsAreSanitized(t *testing.T) {
	provider := &gatewayTestProvider{name: "backend", capabilities: providers.Capabilities{Streaming: true}, generateErr: errors.New("upstream secret sk-test must not leak")}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "sk-test") || !strings.Contains(response.Body.String(), "upstream_error") {
		t.Fatalf("provider error was not sanitized: %d %s", response.Code, response.Body.String())
	}
	var stored string
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COALESCE(error_message,'') FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&stored); err != nil || stored != gatewayFailureUpstreamStart || strings.Contains(stored, "sk-test") {
		t.Fatalf("stored error category was not sanitized: %q %v", stored, err)
	}
}

func TestGatewayProviderStreamErrorsAreSanitized(t *testing.T) {
	provider := &gatewayTestProvider{
		name: "backend", capabilities: providers.Capabilities{Streaming: true},
		events: []providers.Event{{Type: "error", Text: "upstream secret sk-stream must not leak"}},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "sk-stream") || !strings.Contains(response.Body.String(), "upstream_error") {
		t.Fatalf("stream error was not sanitized: %d %s", response.Code, response.Body.String())
	}
	var stored string
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COALESCE(error_message,'') FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&stored); err != nil || stored != gatewayFailureUpstreamEvent || strings.Contains(stored, "sk-stream") {
		t.Fatalf("stored stream error category was not sanitized: %q %v", stored, err)
	}
}

func TestGatewayZeroLimitsAreUnlimitedAndNegativeValuesFailClosed(t *testing.T) {
	limiter := newRequestLimiter(3, func() time.Time { return gatewayTestNow })
	unlimited := db.GatewayKey{ID: "unlimited", RequestsPerMinute: 0, MaxConcurrency: 0}
	for range 100 {
		if err := limiter.allowRequest(unlimited); err != nil {
			t.Fatalf("zero RPM must be unlimited: %v", err)
		}
	}
	first, err := limiter.acquireIngress(unlimited)
	if err != nil {
		t.Fatal(err)
	}
	second, err := limiter.acquireIngress(unlimited)
	if err != nil {
		t.Fatalf("zero max concurrency must be unlimited: %v", err)
	}
	first.Release()
	second.Release()

	if err := limiter.allowRequest(db.GatewayKey{ID: "negative-rpm", RequestsPerMinute: -1}); !errors.Is(err, errGatewayRateLimit) {
		t.Fatalf("negative RPM must fail closed: %v", err)
	}
	if _, err := limiter.acquireIngress(db.GatewayKey{ID: "negative-concurrency", MaxConcurrency: -1}); !errors.Is(err, errGatewayConcurrency) {
		t.Fatalf("negative max concurrency must fail closed: %v", err)
	}
	monthlyLease, err := limiter.acquireIngress(db.GatewayKey{ID: "negative-monthly"})
	if err != nil {
		t.Fatal(err)
	}
	if err := monthlyLease.Reserve(-1, 0, 1); !errors.Is(err, errGatewayMonthly) {
		t.Fatalf("negative monthly limit must fail closed: %v", err)
	}
	if err := monthlyLease.Reserve(10, 0, -1); !errors.Is(err, errGatewayMonthly) {
		t.Fatalf("negative monthly reservation must fail closed: %v", err)
	}
	monthlyLease.Release()
}

func TestGatewayIngressLeaseCoversSlowBodiesAndReleasesAfterEarlyReturns(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, MaxConcurrency: 1}, nil, func(options *Options) {
		options.MaxGlobalConcurrency = 1
	})
	body := &blockingGatewayBody{
		reader:  strings.NewReader(validCompletionBody("")),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", body)
	request.Header.Set("Authorization", "Bearer "+harness.generated.Token)
	request.Header.Set("Content-Type", "application/json")
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		harness.service.ServeHTTP(response, request)
		firstDone <- response
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("slow body was not read")
	}

	blocked := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
	if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Body.String(), "concurrency_limit_exceeded") {
		close(body.release)
		t.Fatalf("slow body did not hold ingress capacity: %d %s", blocked.Code, blocked.Body.String())
	}
	close(body.release)
	select {
	case first := <-firstDone:
		if first.Code != http.StatusOK {
			t.Fatalf("slow request failed: %d %s", first.Code, first.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("slow request did not finish")
	}

	invalid := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", `{`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid request response: %d %s", invalid.Code, invalid.Body.String())
	}
	valid := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
	if valid.Code != http.StatusOK {
		t.Fatalf("lease was not released after early return: %d %s", valid.Code, valid.Body.String())
	}
}

func TestGatewayAccountingFailurePreventsNormalCompletion(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Streaming: true},
		events:       []providers.Event{{Type: "done", Done: true}},
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completed <- gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach provider")
	}
	if err := harness.store.Close(); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case response := <-completed:
		if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), `"object":"chat.completion"`) {
			t.Fatalf("accounting failure looked successful: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not complete after accounting failure")
	}
}

func TestGatewayStreamingAccountingFailureOmitsNormalTerminator(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Streaming: true},
		events: []providers.Event{
			{Type: "text", Text: "partial"},
			{Type: "done", Done: true, StopReason: "stop"},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completed <- gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(`"stream":true,"stream_options":{"include_usage":true}`))
	}()
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming request did not reach provider")
	}
	if err := harness.store.Close(); err != nil {
		t.Fatal(err)
	}
	close(provider.release)
	select {
	case response := <-completed:
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "gateway_internal_error") || strings.Contains(body, "[DONE]") || strings.Contains(body, `"finish_reason":"stop"`) {
			t.Fatalf("streaming accounting failure looked complete: %d %s", response.Code, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streaming request did not complete after accounting failure")
	}
}
