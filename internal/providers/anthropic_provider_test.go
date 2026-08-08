package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"

	"autoto/internal/anthropicauth"
	"autoto/internal/config"
)

func TestAnthropicGatewayRequiresSharingAndGrantsConfiguredOrManagedAccounts(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	profile, err := store.Create(anthropicauth.CreateRequest{AuthType: anthropicauth.AuthTypeProfile, Profile: "work", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	apiKey := createAnthropicAPIKeyAccount(t, store, "sk-ant-api-key", 2, false)
	oauth := createAnthropicOAuthAccount(t, store, "oauth-access", "", time.Now().Add(time.Hour), 3, false)
	provider := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir, APIKey: "configured-key"})

	candidates, err := provider.accountCandidates(context.Background(), GenerateRequest{Scenario: CallScenarioGateway})
	if err == nil || len(candidates) != 0 {
		t.Fatalf("Gateway credentials must require explicit sharing: candidates=%+v err=%v", candidates, err)
	}

	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{
		provider.Name(): {profile.Credential.ID, apiKey.Credential.ID, oauth.Credential.ID, configuredCredentialID},
	}})
	candidates, err = provider.accountCandidates(context.Background(), GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.id)
	}
	if strings.Join(ids, ",") != strings.Join([]string{apiKey.Credential.ID, oauth.Credential.ID, configuredCredentialID}, ",") {
		t.Fatalf("Gateway grants were not applied or profile was included: %v", ids)
	}

	internal, err := provider.accountCandidates(context.Background(), GenerateRequest{Scenario: CallScenarioInternal})
	if err != nil || len(internal) != 4 {
		t.Fatalf("internal candidates changed: candidates=%+v err=%v", internal, err)
	}
}

func TestAnthropicScenarioConfigurationAndDynamicAvailability(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	if _, err := store.Create(anthropicauth.CreateRequest{AuthType: anthropicauth.AuthTypeProfile, Profile: "work", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	provider := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir})
	if !provider.Configured() || !ConfiguredForScenario(provider, false, CallScenarioInternal) {
		t.Fatal("profile credential should remain configured for internal calls")
	}
	if provider.ConfiguredForScenario(CallScenarioGateway) || ConfiguredForScenario(provider, true, CallScenarioGateway) {
		t.Fatal("profile-only Anthropic provider must not be configured for Gateway calls")
	}

	apiKey := createAnthropicAPIKeyAccount(t, store, "sk-ant-gateway", 2, false)
	if provider.ConfiguredForScenario(CallScenarioGateway) || AvailableForScenario(context.Background(), provider, true, ScenarioAvailability{Scenario: CallScenarioGateway}) {
		t.Fatal("managed Anthropic API keys require explicit sharing and a grant")
	}
	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{provider.Name(): {apiKey.Credential.ID}}})
	if !AvailableForScenario(context.Background(), provider, false, ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("granted managed Anthropic API key was not dynamically available")
	}

	configured := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir, APIKey: "configured-gateway"})
	if !configured.ConfiguredForScenario(CallScenarioGateway) || !ConfiguredForScenario(configured, false, CallScenarioGateway) {
		t.Fatal("ordinary configured Anthropic API key should remain statically configured")
	}
	if AvailableForScenario(context.Background(), configured, true, ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("configured Anthropic API key must require a stable account grant")
	}
	configured.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{configured.Name(): {configuredCredentialID}}})
	if !AvailableForScenario(context.Background(), configured, false, ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("granted configured Anthropic API key was not dynamically available")
	}
}

func TestAnthropicGatewayDispatchesConfiguredOrGrantedManagedCredential(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	managed := createAnthropicAPIKeyAccount(t, store, "managed-key", 1, false)
	oauth := createAnthropicOAuthAccount(t, store, "oauth-access", "", time.Now().Add(time.Hour), 2, false)
	type requestAuth struct {
		apiKey           string
		authorization    string
		beta             string
		userAgent        string
		app              string
		stainlessLang    string
		stainlessRuntime string
		system           []string
	}
	var headers []requestAuth
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Anthropic request: %v", err)
		}
		system := make([]string, 0, len(body.System))
		for _, block := range body.System {
			system = append(system, block.Text)
		}
		headers = append(headers, requestAuth{
			apiKey:           r.Header.Get("X-Api-Key"),
			authorization:    r.Header.Get("Authorization"),
			beta:             r.Header.Get("Anthropic-Beta"),
			userAgent:        r.Header.Get("User-Agent"),
			app:              r.Header.Get("X-App"),
			stainlessLang:    r.Header.Get("X-Stainless-Lang"),
			stainlessRuntime: r.Header.Get("X-Stainless-Runtime"),
			system:           system,
		})
		writeAnthropicSuccessStream(w, "ok")
	}))
	defer server.Close()

	provider := NewAnthropicProvider(config.ProviderConfig{
		Name:                "anthropic",
		BaseURL:             server.URL,
		CredentialStorePath: storeDir,
		APIKey:              "configured-key",
		Model:               "claude-test",
	})
	generate := func(req GenerateRequest) *DispatchInfo {
		t.Helper()
		req.Messages = []Message{{Role: "user", Content: "hello"}}
		req.SystemPrompt = "Autoto system prompt"
		events, err := provider.Generate(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		var dispatch *DispatchInfo
		for event := range events {
			if event.Type == "error" {
				t.Fatalf("unexpected Gateway error: %s", event.Text)
			}
			if event.Dispatch != nil {
				dispatch = event.Dispatch
			}
		}
		return dispatch
	}

	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{provider.Name(): {configuredCredentialID}}})
	dispatch := generate(GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if dispatch == nil || dispatch.CredentialID != configuredCredentialID {
		t.Fatalf("granted configured Gateway credential must use its stable ID: %+v", dispatch)
	}
	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{provider.Name(): {managed.Credential.ID}}})
	dispatch = generate(GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if dispatch == nil || dispatch.CredentialID != managed.Credential.ID {
		t.Fatalf("granted managed account was not attributed: %+v", dispatch)
	}
	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{provider.Name(): {oauth.Credential.ID}}})
	dispatch = generate(GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true})
	if dispatch == nil || dispatch.CredentialID != oauth.Credential.ID {
		t.Fatalf("granted OAuth account was not attributed: %+v", dispatch)
	}
	if len(headers) != 3 || headers[0].apiKey != "configured-key" || headers[1].apiKey != "managed-key" || headers[2].apiKey != "" || headers[2].authorization != "Bearer oauth-access" {
		t.Fatalf("unexpected Gateway credential selection: %+v", headers)
	}
	for index := 0; index < 2; index++ {
		if len(headers[index].system) != 1 || headers[index].system[0] != "Autoto system prompt" {
			t.Fatalf("API-key request %d was unexpectedly disguised as Claude Code: %+v", index, headers[index])
		}
		if headers[index].app != "" {
			t.Fatalf("API-key request %d unexpectedly included X-App: %+v", index, headers[index])
		}
	}
	oauthRequest := headers[2]
	if oauthRequest.beta != anthropicauth.OAuthMessagesBetaHeader() || oauthRequest.userAgent != anthropicauth.ClaudeCodeUserAgent || oauthRequest.app != anthropicauth.ClaudeCodeAppHeader || oauthRequest.stainlessLang != "js" || oauthRequest.stainlessRuntime != "node" {
		t.Fatalf("OAuth request did not use Claude Code headers: %+v", oauthRequest)
	}
	if len(oauthRequest.system) != 2 || oauthRequest.system[0] != anthropicauth.ClaudeCodeIdentity || oauthRequest.system[1] != "Autoto system prompt" {
		t.Fatalf("OAuth request did not put Claude Code identity first: %+v", oauthRequest.system)
	}
}

func TestAnthropicOAuthRefreshUsesRequestContext(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	createAnthropicOAuthAccount(t, store, "expired-access", "refresh-token", now.Add(-time.Minute), 1, false)
	provider := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir})
	provider.clock = func() time.Time { return now }
	started := make(chan struct{}, 1)
	provider.oauthRefreshToken = func(ctx context.Context, _ string, _ *http.Client) (anthropicauth.OAuthTokenResponse, error) {
		started <- struct{}{}
		<-ctx.Done()
		return anthropicauth.OAuthTokenResponse{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := provider.accountCandidates(ctx, GenerateRequest{Scenario: CallScenarioInternal})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OAuth refresh did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("refresh did not return request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("OAuth refresh ignored request cancellation")
	}
}

func TestAnthropicOAuthRefreshCoalescesAndExpiredFailureCloses(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	account := createAnthropicOAuthAccount(t, store, "expired-access", "refresh-token", now.Add(-time.Minute), 1, false)
	provider := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir})
	provider.clock = func() time.Time { return now }
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var refreshes atomic.Int32
	provider.oauthRefreshToken = func(context.Context, string, *http.Client) (anthropicauth.OAuthTokenResponse, error) {
		refreshes.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return anthropicauth.OAuthTokenResponse{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresIn: 3600}, nil
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			candidates, err := provider.accountCandidates(context.Background(), GenerateRequest{Scenario: CallScenarioInternal})
			if err == nil && (len(candidates) != 1 || candidates[0].id != account.Credential.ID) {
				err = errors.New("unexpected refreshed candidates")
			}
			results <- err
		}()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("OAuth refresh did not start")
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("concurrent OAuth refreshes were not coalesced: %d", refreshes.Load())
	}
	stored, err := store.GetByID(account.Credential.ID)
	if err != nil || stored.Credential.OAuthAccess != "fresh-access" || stored.Credential.OAuthRefresh != "fresh-refresh" {
		t.Fatalf("refreshed OAuth credential was not persisted: item=%+v err=%v", stored, err)
	}

	stored.Credential.OAuthExpiresAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.UpdateOAuthTokens(account.Credential.ID, "expired-again", "refresh-again", stored.Credential.OAuthExpiresAt); err != nil {
		t.Fatal(err)
	}
	provider.oauthRefreshToken = func(context.Context, string, *http.Client) (anthropicauth.OAuthTokenResponse, error) {
		return anthropicauth.OAuthTokenResponse{}, errors.New("refresh failed")
	}
	if candidates, err := provider.accountCandidates(context.Background(), GenerateRequest{Scenario: CallScenarioInternal}); err == nil || len(candidates) != 0 {
		t.Fatalf("expired OAuth refresh failure must fail closed: candidates=%+v err=%v", candidates, err)
	}
}

func TestAnthropicThinkingConfigUsesAdaptiveAndEnabledModes(t *testing.T) {
	t.Run("adaptive", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeAnthropicSuccessStream(w, "ok")
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-4-6", MaxTokens: 4096})
		provider.thinkingSupport["claude-sonnet-4-6"] = anthropicThinkingSupport{Known: true, Supported: true, Adaptive: true, Enabled: true}
		events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}, ReasoningEffort: "medium"})
		if err != nil {
			t.Fatal(err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
		thinking, _ := body["thinking"].(map[string]any)
		outputConfig, _ := body["output_config"].(map[string]any)
		if thinking["type"] != "adaptive" || outputConfig["effort"] != "medium" {
			t.Fatalf("unexpected adaptive thinking request: %+v", body)
		}
	})

	t.Run("enabled", func(t *testing.T) {
		var body map[string]any
		var beta string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			beta = r.Header.Get("Anthropic-Beta")
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeAnthropicSuccessStream(w, "ok")
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-4-5", MaxTokens: 4096})
		events, err := provider.Generate(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: "user", Content: "hello"}},
			Tools:           []ToolSpec{{Name: "Read", Schema: map[string]any{"type": "object"}}},
			ReasoningEffort: "high",
		})
		if err != nil {
			t.Fatal(err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
		thinking, _ := body["thinking"].(map[string]any)
		if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(3072) {
			t.Fatalf("unexpected enabled thinking request: %+v", body)
		}
		if !strings.Contains(beta, anthropicInterleavedThinkingBeta) {
			t.Fatalf("missing interleaved thinking beta header: %q", beta)
		}
	})
}

func TestAnthropicThinkingConfigValidationAndFallback(t *testing.T) {
	provider := NewAnthropicProvider(config.ProviderConfig{Model: "claude-sonnet-4-5", MaxTokens: 1024})
	params := anthropic.MessageNewParams{Model: anthropic.Model("claude-sonnet-4-5"), MaxTokens: 1024}
	if _, err := provider.applyThinkingConfig(&params, "claude-sonnet-4-5", "low", 0); err == nil || !strings.Contains(err.Error(), "at least 2048") {
		t.Fatalf("expected local max_tokens validation, got %v", err)
	}
	if support := fallbackAnthropicThinkingSupport("claude-sonnet-4-5"); !support.Enabled || support.Adaptive {
		t.Fatalf("unexpected legacy fallback: %+v", support)
	}
	if support := fallbackAnthropicThinkingSupport("claude-opus-5-20260701"); !support.Adaptive {
		t.Fatalf("unexpected adaptive fallback: %+v", support)
	}

	var info anthropic.ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"claude-custom","type":"model","display_name":"custom","created_at":"2026-01-01T00:00:00Z","max_input_tokens":200000,"max_tokens":64000,"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true},"enabled":{"supported":false}}}}}`), &info); err != nil {
		t.Fatal(err)
	}
	provider.rememberAnthropicThinkingSupport([]anthropic.ModelInfo{info})
	if support := provider.anthropicThinkingSupportForModel("claude-custom"); !support.Known || !support.Supported || !support.Adaptive || support.Enabled {
		t.Fatalf("model thinking capability was not cached: %+v", support)
	}
}

// Effort levels are a per-model property: adaptive models forward the effort to
// output_config (so xhigh and max are real there), while models on the manual
// budget path only serve what anthropicThinkingBudget maps.
func TestAnthropicModelReasoningEffortsFollowThinkingSupport(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "claude-opus-5", want: "low,medium,high,xhigh,max"},
		{model: "claude-sonnet-5", want: "low,medium,high,xhigh,max"},
		{model: "claude-opus-4-8", want: "low,medium,high,xhigh,max"},
		{model: "claude-opus-4-7", want: "low,medium,high,xhigh,max"},
		{model: "claude-opus-5-20260701", want: "low,medium,high,xhigh,max"},
		// 4.6 serves adaptive effort but not xhigh.
		{model: "claude-sonnet-4-6", want: "low,medium,high,max"},
		{model: "claude-opus-4-6", want: "low,medium,high,max"},
		{model: "claude-sonnet-4.6", want: "low,medium,high,max"},
		// Manual budget path.
		{model: "claude-sonnet-4-5", want: "low,medium,high"},
		{model: "claude-opus-4-5", want: "low,medium,high"},
		{model: "claude-haiku-4-5", want: "low,medium,high"},
		// Unparseable private alias keeps the conservative default.
		{model: "relay-house-blend", want: "low,medium,high"},
	}
	provider := NewAnthropicProvider(config.ProviderConfig{APIKey: "test-key"})
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			got := strings.Join(provider.ModelCapabilities(test.model).ReasoningEfforts, ",")
			if got != test.want {
				t.Fatalf("ReasoningEfforts(%q) = %q, want %q", test.model, got, test.want)
			}
		})
	}

	t.Run("catalog reporting no thinking advertises no effort", func(t *testing.T) {
		p := NewAnthropicProvider(config.ProviderConfig{APIKey: "test-key"})
		p.thinkingSupport["claude-no-think"] = anthropicThinkingSupport{Known: true, Supported: false}
		if efforts := p.ModelCapabilities("claude-no-think").ReasoningEfforts; len(efforts) != 0 {
			t.Fatalf("expected no efforts, got %v", efforts)
		}
	})

	t.Run("catalog adaptive flag wins over version fallback", func(t *testing.T) {
		// A 4.5 model the catalog reports as adaptive gains the extended levels.
		p := NewAnthropicProvider(config.ProviderConfig{APIKey: "test-key"})
		p.thinkingSupport["claude-sonnet-4-5"] = anthropicThinkingSupport{Known: true, Supported: true, Adaptive: true, Enabled: true}
		if got := strings.Join(p.ModelCapabilities("claude-sonnet-4-5").ReasoningEfforts, ","); got != "low,medium,high,max" {
			t.Fatalf("ReasoningEfforts = %q, want %q", got, "low,medium,high,max")
		}
	})
}

// Generate must gate the effort on the resolved model rather than the provider
// baseline, so opus-5 reaches max while a manual-budget model still refuses it.
func TestAnthropicGenerateGatesReasoningEffortPerModel(t *testing.T) {
	t.Run("opus-5 accepts max and forwards it to output_config", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeAnthropicSuccessStream(w, "ok")
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-opus-5", MaxTokens: 4096})
		events, err := provider.Generate(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: "user", Content: "hello"}},
			ReasoningEffort: "max",
		})
		if err != nil {
			t.Fatalf("max must be accepted for opus-5: %v", err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
		thinking, _ := body["thinking"].(map[string]any)
		outputConfig, _ := body["output_config"].(map[string]any)
		if thinking["type"] != "adaptive" || outputConfig["effort"] != "max" {
			t.Fatalf("effort max did not reach output_config: %+v", body)
		}
	})

	t.Run("xhigh reaches output_config for 4.7+", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeAnthropicSuccessStream(w, "ok")
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-opus-4-7", MaxTokens: 4096})
		events, err := provider.Generate(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: "user", Content: "hello"}},
			ReasoningEffort: "xhigh",
		})
		if err != nil {
			t.Fatalf("xhigh must be accepted for 4.7: %v", err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
		outputConfig, _ := body["output_config"].(map[string]any)
		if outputConfig["effort"] != "xhigh" {
			t.Fatalf("effort xhigh did not reach output_config: %+v", body)
		}
	})

	// Refusals must happen before any request leaves the process.
	refuses := func(t *testing.T, model, effort string) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("unsupported effort %q reached upstream for %s", effort, model)
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: model, MaxTokens: 4096})
		if _, err := provider.Generate(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: "user", Content: "hello"}},
			ReasoningEffort: effort,
		}); !errors.Is(err, ErrReasoningEffortUnsupported) {
			t.Fatalf("expected %q to be refused for %s, got %v", effort, model, err)
		}
	}

	t.Run("4.6 refuses xhigh", func(t *testing.T) { refuses(t, "claude-sonnet-4-6", "xhigh") })
	t.Run("manual budget model refuses xhigh", func(t *testing.T) { refuses(t, "claude-sonnet-4-5", "xhigh") })
	t.Run("manual budget model refuses max", func(t *testing.T) { refuses(t, "claude-sonnet-4-5", "max") })

	t.Run("4.6 still accepts max", func(t *testing.T) {
		var body map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			writeAnthropicSuccessStream(w, "ok")
		}))
		defer server.Close()

		provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-4-6", MaxTokens: 4096})
		events, err := provider.Generate(context.Background(), GenerateRequest{
			Messages:        []Message{{Role: "user", Content: "hello"}},
			ReasoningEffort: "max",
		})
		if err != nil {
			t.Fatalf("max must be accepted for 4.6: %v", err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
		outputConfig, _ := body["output_config"].(map[string]any)
		if outputConfig["effort"] != "max" {
			t.Fatalf("effort max did not reach output_config: %+v", body)
		}
	})
}

// The extended levels normally reach the adaptive path. They land in the budget
// mapper only when catalog metadata reports enabled-but-not-adaptive, so the
// mapper must produce a valid budget instead of failing the request.
func TestAnthropicThinkingBudgetMapsExtendedEfforts(t *testing.T) {
	const maxTokens = 32000
	tests := []struct {
		effort string
		want   int64
	}{
		{effort: "low", want: 8000},
		{effort: "medium", want: 16000},
		{effort: "high", want: 24000},
		{effort: "xhigh", want: 28000},
		// 7/8 of 32000 would be 28000; max asks for the ceiling instead.
		{effort: "max", want: maxTokens - 1024},
	}
	for _, test := range tests {
		t.Run(test.effort, func(t *testing.T) {
			budget, err := anthropicThinkingBudget(maxTokens, test.effort)
			if err != nil {
				t.Fatalf("anthropicThinkingBudget(%q) error: %v", test.effort, err)
			}
			if budget != test.want {
				t.Fatalf("anthropicThinkingBudget(%d, %q) = %d, want %d", maxTokens, test.effort, budget, test.want)
			}
		})
	}

	// The existing max_tokens-1024 ceiling still clamps the extended levels on a
	// small budget, so they never produce an invalid request.
	for _, effort := range []string{"xhigh", "max"} {
		budget, err := anthropicThinkingBudget(4096, effort)
		if err != nil {
			t.Fatalf("anthropicThinkingBudget(4096, %q) error: %v", effort, err)
		}
		if budget != 3072 {
			t.Fatalf("anthropicThinkingBudget(4096, %q) = %d, want 3072", effort, budget)
		}
	}

	if _, err := anthropicThinkingBudget(32000, "ultra"); err == nil {
		t.Fatal("unknown effort must still be rejected")
	}
}

func TestAnthropicProviderStreamsSignedThinkingBlocks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"inspect first"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-1"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":1}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"README.md\"}"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":2}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"cache_read_input_tokens":0,"output_tokens":10,"output_tokens_details":{"thinking_tokens":5}}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-4-5", MaxTokens: 4096})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}, ReasoningEffort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	var reasoning string
	var blocks []ContentBlock
	var indexes []int64
	var calls []ToolCall
	for event := range events {
		switch event.Type {
		case "error":
			t.Fatal(event.Text)
		case "reasoning":
			reasoning += event.Text
			if event.BlockType != ContentBlockTypeThinking || event.BlockIndex != 0 {
				t.Fatalf("reasoning event lost native block metadata: %+v", event)
			}
		case "content_block":
			if event.ContentBlock != nil {
				blocks = append(blocks, *event.ContentBlock)
				indexes = append(indexes, event.BlockIndex)
			}
		case "tool_call":
			if event.ToolCall != nil {
				calls = append(calls, *event.ToolCall)
			}
		}
	}
	if reasoning != "inspect first" || len(blocks) != 3 || strings.Join([]string{blocks[0].Type, blocks[1].Type, blocks[2].Type}, ",") != "thinking,redacted_thinking,tool_use" {
		t.Fatalf("unexpected native Anthropic blocks: reasoning=%q blocks=%+v", reasoning, blocks)
	}
	if strings.Join([]string{fmt.Sprint(indexes[0]), fmt.Sprint(indexes[1]), fmt.Sprint(indexes[2])}, ",") != "0,1,2" {
		t.Fatalf("unexpected block indexes: %v", indexes)
	}
	model, signature, _, ok := AnthropicReasoningContentBlockState(blocks[0])
	if !ok || model != "claude-sonnet-4-5" || signature != "sig-1" || blocks[0].ReasoningText != "inspect first" {
		t.Fatalf("thinking state was not preserved: block=%+v model=%q signature=%q ok=%v", blocks[0], model, signature, ok)
	}
	_, _, data, ok := AnthropicReasoningContentBlockState(blocks[1])
	if !ok || data != "opaque-1" {
		t.Fatalf("redacted thinking state was not preserved: block=%+v data=%q ok=%v", blocks[1], data, ok)
	}
	if len(calls) != 1 || calls[0].ID != "toolu_1" {
		t.Fatalf("tool call was not preserved: %+v", calls)
	}
}

func TestAnthropicMessagesReplaySignedThinkingInOriginalOrder(t *testing.T) {
	thinking := NewAnthropicThinkingContentBlock("claude-sonnet-4-5", "inspect first", "sig-1")
	redacted := NewAnthropicRedactedThinkingContentBlock("claude-sonnet-4-5", "opaque-1")
	messages, _ := anthropicMessages([]Message{
		{Role: "assistant", Blocks: []ContentBlock{thinking, {Type: "text", Text: "checking"}, redacted, {Type: "tool_use", ToolUseID: "tool-1", ToolName: "Read", Input: json.RawMessage(`{"file_path":"README.md"}`)}}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-1", Output: "ok"}}},
	}, "", "claude-sonnet-4-5")
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	positions := []int{strings.Index(text, `"type":"thinking"`), strings.Index(text, `"type":"text"`), strings.Index(text, `"type":"redacted_thinking"`), strings.Index(text, `"type":"tool_use"`), strings.Index(text, `"type":"tool_result"`)}
	for index, position := range positions {
		if position < 0 || index > 0 && position <= positions[index-1] {
			t.Fatalf("Anthropic content block order changed: positions=%v json=%s", positions, text)
		}
	}
	for _, want := range []string{"inspect first", "sig-1", "opaque-1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("signed thinking payload %q missing: %s", want, text)
		}
	}

	switched, _ := anthropicMessages([]Message{{Role: "assistant", Blocks: []ContentBlock{thinking, {Type: "text", Text: "answer"}}}}, "", "claude-sonnet-4-6")
	switchedJSON, err := json.Marshal(switched)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(switchedJSON), `"type":"thinking"`) || !strings.Contains(string(switchedJSON), "answer") {
		t.Fatalf("model switch should strip native thinking and preserve answer text: %s", switchedJSON)
	}
}

func TestAnthropicMessagesPreserveToolBlocks(t *testing.T) {
	messages, _ := anthropicMessages([]Message{
		{Role: "assistant", Blocks: []ContentBlock{{Type: "text", Text: "checking"}, {Type: "tool_use", ToolUseID: "tool-1", ToolName: "Read", Input: json.RawMessage(`{"file_path":"README.md"}`)}}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-1", ToolName: "Read", Output: "ok", IsError: true}}},
	}, "", "claude-sonnet-4-5")
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"tool_use", "tool_result", "tool-1", "Read", "is_error"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected marshaled Anthropic messages to contain %q: %s", want, text)
		}
	}
}

func TestAnthropicMessagesPreserveImageBlocks(t *testing.T) {
	messages, _ := anthropicMessages([]Message{{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "see image"}, {Type: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}, Filename: "a.png"}}}}, "", "claude-sonnet-4-5")
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"image", "base64", "image/png", "AQID"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected marshaled Anthropic image message to contain %q: %s", want, text)
		}
	}
}

func TestAnthropicProviderStreamsTextUsageAndToolCalls(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "test-key" {
			t.Fatalf("expected Anthropic API key header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: message_start`,
			`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"cache_read_input_tokens":2,"output_tokens":0,"output_tokens_details":{"thinking_tokens":0}}}}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hel"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":0}`,
			``,
			`event: content_block_start`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":"}}`,
			``,
			`event: content_block_delta`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"README.md\"}"}}`,
			``,
			`event: content_block_stop`,
			`data: {"type":"content_block_stop","index":1}`,
			``,
			`event: message_delta`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":10,"cache_read_input_tokens":2,"output_tokens":7,"output_tokens_details":{"thinking_tokens":1}}}`,
			``,
			`event: message_stop`,
			`data: {"type":"message_stop"}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "claude-sonnet-4-5", MaxTokens: 128})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}, Tools: []ToolSpec{{Name: "Read", Description: "Read a file", Schema: map[string]any{"type": "object"}}}, MaxOutputTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var usage Usage
	var toolCalls []ToolCall
	var stopReason string
	for event := range events {
		switch event.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", event.Text)
		case "text":
			text += event.Text
		case "usage":
			if event.Usage != nil {
				usage = *event.Usage
			}
		case "tool_call":
			if event.ToolCall != nil {
				toolCalls = append(toolCalls, *event.ToolCall)
			}
		case "done":
			stopReason = event.StopReason
		}
	}
	if requestBody["stream"] != true || requestBody["max_tokens"] != float64(64) {
		t.Fatalf("expected stream and max output tokens, got %+v", requestBody)
	}
	if text != "hello" {
		t.Fatalf("expected streamed text hello, got %q", text)
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 7 || usage.CachedInputTokens != 2 || usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if len(toolCalls) != 1 || toolCalls[0].ID != "toolu_1" || toolCalls[0].Name != "Read" || string(toolCalls[0].Input) != `{"file_path":"README.md"}` {
		t.Fatalf("unexpected tool calls: %+v", toolCalls)
	}
	if stopReason != "tool_use" {
		t.Fatalf("expected tool_use stop reason, got %q", stopReason)
	}
}

func TestAnthropicProviderWithoutAPIKeyReturnsUnavailableError(t *testing.T) {
	provider := NewAnthropicProvider(config.ProviderConfig{Model: "claude-sonnet-4-5"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err == nil || !errors.Is(err, ErrProviderUnavailable) || !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
		t.Fatalf("expected explicit unavailable error, events=%v err=%v", events, err)
	}
	if events != nil {
		t.Fatal("unconfigured provider must not return a successful event stream")
	}
}

func TestAnthropicPromptCachingMarksLargeRequests(t *testing.T) {
	messages, system := anthropicMessages([]Message{{Role: "user", Content: strings.Repeat("please inspect the repository context. ", 120)}}, strings.Repeat("stable coding agent instructions. ", 120), "claude-sonnet-4-5")
	params := anthropic.MessageNewParams{
		MaxTokens: 128,
		Model:     anthropic.Model("claude-sonnet-4-5"),
		Messages:  messages,
		System:    system,
		Tools: anthropicTools([]ToolSpec{{
			Name:        "Read",
			Description: strings.Repeat("Read a file from the bounded workspace. ", 30),
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
				"required":   []string{"file_path"},
			},
		}}),
	}
	if anthropicPromptCacheFootprint(params) < anthropicPromptCacheMinBytes {
		t.Fatalf("test request should be large enough for prompt caching")
	}
	applyAnthropicPromptCaching(&params)
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if count := strings.Count(text, `"cache_control"`); count < 3 {
		t.Fatalf("expected system, tool, and message cache controls, got %d in %s", count, text)
	}
	for _, want := range []string{`"ttl":"5m"`, `"type":"ephemeral"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %s in cached request: %s", want, text)
		}
	}
}

func TestAnthropicPromptCachingSkipsSmallRequests(t *testing.T) {
	messages, system := anthropicMessages([]Message{{Role: "user", Content: "hello"}}, "short system", "claude-sonnet-4-5")
	params := anthropic.MessageNewParams{MaxTokens: 128, Model: anthropic.Model("claude-sonnet-4-5"), Messages: messages, System: system}
	applyAnthropicPromptCaching(&params)
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"cache_control"`) {
		t.Fatalf("small request should not include cache_control: %s", string(data))
	}
}

func TestAnthropicToolsMarshalSchemaAndDescription(t *testing.T) {
	tools := anthropicTools([]ToolSpec{{
		Name:        "Read",
		Description: "Read a file",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string"},
			},
			"required": []string{"file_path"},
		},
	}})
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}
	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{"Read", "Read a file", "input_schema", "file_path", "required"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected marshaled Anthropic tools to contain %q: %s", want, text)
		}
	}
}

type recordingAnthropicTelemetry struct {
	mu       sync.Mutex
	attempts []ProviderAccountAttempt
	quotas   []ProviderAccountQuotaSnapshot
}

func (r *recordingAnthropicTelemetry) RecordProviderAccountAttempt(_ context.Context, attempt ProviderAccountAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
	return nil
}

func (r *recordingAnthropicTelemetry) UpdateProviderAccountQuota(_ context.Context, provider, accountID string, quota any, fetchedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot, ok := quota.(ProviderAccountQuotaSnapshot)
	if !ok {
		return errors.New("unexpected quota type")
	}
	if snapshot.Provider != provider || snapshot.AccountID != accountID || !snapshot.FetchedAt.Equal(fetchedAt) {
		return errors.New("quota metadata mismatch")
	}
	r.quotas = append(r.quotas, snapshot)
	return nil
}

func createAnthropicAPIKeyAccount(t *testing.T, store *anthropicauth.Store, apiKey string, priority int, disabled bool) anthropicauth.StoredCredential {
	t.Helper()
	item, err := store.Create(anthropicauth.CreateRequest{AuthType: anthropicauth.AuthTypeAPIKey, APIKey: apiKey, Priority: priority, Disabled: disabled})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func createAnthropicOAuthAccount(t *testing.T, store *anthropicauth.Store, access, refresh string, expiresAt time.Time, priority int, disabled bool) anthropicauth.StoredCredential {
	t.Helper()
	expires := ""
	if !expiresAt.IsZero() {
		expires = expiresAt.UTC().Format(time.RFC3339Nano)
	}
	item, err := store.Create(anthropicauth.CreateRequest{
		AuthType:       anthropicauth.AuthTypeOAuth,
		OAuthAccess:    access,
		OAuthRefresh:   refresh,
		OAuthExpiresAt: expires,
		Priority:       priority,
		Disabled:       disabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func writeAnthropicSuccessStream(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(mustJSON(text)) + `}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":3,"output_tokens":1}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n") + "\n\n"))
}

func mustJSON(value string) []byte {
	data, _ := json.Marshal(value)
	return data
}

func collectAnthropicEvents(t *testing.T, events <-chan Event) (string, []Event) {
	t.Helper()
	var text string
	var collected []Event
	for event := range events {
		collected = append(collected, event)
		if event.Type == "text" {
			text += event.Text
		}
	}
	return text, collected
}

func TestAnthropicProviderUsesPriorityIDOrderSkipsDisabledAndFallsBackLast(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "environment-secret-must-not-be-used")
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	_ = createAnthropicAPIKeyAccount(t, store, "disabled-key", 1, true)
	lowA := createAnthropicAPIKeyAccount(t, store, "priority-100-a", 100, false)
	lowB := createAnthropicAPIKeyAccount(t, store, "priority-100-b", 100, false)
	high := createAnthropicAPIKeyAccount(t, store, "priority-200", 200, false)
	keysByID := map[string]string{lowA.Credential.ID: lowA.Credential.APIKey, lowB.Credential.ID: lowB.Credential.APIKey}
	ids := []string{lowA.Credential.ID, lowB.Credential.ID}
	sort.Strings(ids)
	expected := []string{keysByID[ids[0]], keysByID[ids[1]], high.Credential.APIKey, "legacy-fallback"}

	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Api-Key")
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		if key != "legacy-fallback" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`))
			return
		}
		writeAnthropicSuccessStream(w, "fallback-ok")
	}))
	defer server.Close()

	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, Model: "claude-test", CredentialStorePath: storeDir, APIKey: "legacy-fallback"})
	if !provider.Configured() {
		t.Fatal("provider with enabled stored accounts should be configured")
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	text, collected := collectAnthropicEvents(t, events)
	if text != "fallback-ok" {
		t.Fatalf("unexpected text %q events=%+v", text, collected)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, ",") != strings.Join(expected, ",") {
		t.Fatalf("unexpected account order: got %v want %v", seen, expected)
	}
	for _, forbidden := range []string{"disabled-key", "environment-secret-must-not-be-used"} {
		if strings.Contains(strings.Join(seen, ","), forbidden) {
			t.Fatalf("forbidden credential was used: %s", forbidden)
		}
	}
}

func TestAnthropicProviderDisabledOnlyIsNotConfigured(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	_ = createAnthropicAPIKeyAccount(t, store, "disabled-only", 10, true)
	provider := NewAnthropicProvider(config.ProviderConfig{CredentialStorePath: storeDir, Model: "claude-test"})
	if provider.Configured() {
		t.Fatal("disabled-only store must not configure provider")
	}
	if events, err := provider.Generate(context.Background(), GenerateRequest{}); err == nil || events != nil || !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("expected unavailable provider, events=%v err=%v", events, err)
	}
}

func TestAnthropicProviderSyncAccountReturnsModelsAndQuotaWithoutMessage(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	account := createAnthropicAPIKeyAccount(t, store, "sync-secret", 10, false)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			t.Fatalf("sync must only list models, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Api-Key") != "sync-secret" {
			t.Fatalf("unexpected credential header %q", r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("anthropic-ratelimit-requests-limit", "100")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "91")
		w.Header().Set("anthropic-ratelimit-requests-reset", "2026-07-16T12:00:00Z")
		w.Header().Set("anthropic-ratelimit-input-tokens-limit", "10000")
		w.Header().Set("anthropic-ratelimit-input-tokens-remaining", "9000")
		w.Header().Set("anthropic-ratelimit-input-tokens-reset", "2026-07-16T12:00:01Z")
		w.Header().Set("anthropic-ratelimit-output-tokens-limit", "2000")
		w.Header().Set("anthropic-ratelimit-output-tokens-remaining", "1800")
		w.Header().Set("anthropic-ratelimit-output-tokens-reset", "2026-07-16T12:00:02Z")
		w.Header().Set("retry-after", "2")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","type":"model","display_name":"Claude A","created_at":"2026-07-01T00:00:00Z"}],"has_more":false,"first_id":"claude-a","last_id":"claude-a"}`))
	}))
	defer server.Close()

	telemetry := &recordingAnthropicTelemetry{}
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	provider.clock = func() time.Time { return time.Date(2026, 7, 16, 11, 0, 0, 0, time.UTC) }
	provider.SetAccountTelemetry(telemetry)
	summary, models, quota, err := provider.SyncAccount(context.Background(), account.Credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.ID != account.Credential.ID || len(models) != 1 || models[0] != "claude-a" {
		t.Fatalf("unexpected sync result summary=%+v models=%v", summary, models)
	}
	if quota.Requests.Limit != "100" || quota.Requests.Remaining != "91" || quota.InputTokens.Limit != "10000" || quota.OutputTokens.Remaining != "1800" || quota.RetryAfter != "2" || quota.FetchedAt.IsZero() {
		t.Fatalf("unexpected quota snapshot: %+v", quota)
	}
	encodedSummary, _ := json.Marshal(summary)
	encodedQuota, _ := json.Marshal(quota)
	if strings.Contains(string(encodedSummary), "sync-secret") || strings.Contains(string(encodedQuota), "sync-secret") {
		t.Fatal("sync response leaked API key")
	}
	if len(paths) != 1 || paths[0] != "/v1/models" {
		t.Fatalf("unexpected sync requests: %v", paths)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 0 || len(telemetry.quotas) != 1 {
		t.Fatalf("sync should record quota only: attempts=%v quotas=%v", telemetry.attempts, telemetry.quotas)
	}
}

func TestAnthropicProviderGenerateRecordsAttemptAndQuotaHeaders(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	account := createAnthropicAPIKeyAccount(t, store, "quota-key", 10, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("anthropic-ratelimit-requests-limit", "50")
		w.Header().Set("anthropic-ratelimit-requests-remaining", "49")
		w.Header().Set("anthropic-ratelimit-input-tokens-limit", "5000")
		w.Header().Set("anthropic-ratelimit-output-tokens-limit", "1000")
		writeAnthropicSuccessStream(w, "ok")
	}))
	defer server.Close()
	telemetry := &recordingAnthropicTelemetry{}
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, collected := collectAnthropicEvents(t, events)
	for _, event := range collected {
		if event.Type == "error" {
			t.Fatalf("unexpected error: %s", event.Text)
		}
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 1 || !telemetry.attempts[0].Success || telemetry.attempts[0].HTTPStatus != http.StatusOK || telemetry.attempts[0].AccountID != account.Credential.ID {
		t.Fatalf("unexpected attempt telemetry: %+v", telemetry.attempts)
	}
	if len(telemetry.quotas) != 1 || telemetry.quotas[0].Requests.Limit != "50" || telemetry.quotas[0].InputTokens.Limit != "5000" || telemetry.quotas[0].OutputTokens.Limit != "1000" {
		t.Fatalf("unexpected quota telemetry: %+v", telemetry.quotas)
	}
}

func TestAnthropicProviderDoesNotReplayAfterOutput(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	_ = createAnthropicAPIKeyAccount(t, store, "first-key", 10, false)
	_ = createAnthropicAPIKeyAccount(t, store, "second-key", 20, false)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-Api-Key") == "second-key" {
			writeAnthropicSuccessStream(w, "replayed")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-test\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try another\"}}\n\n"))
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	text, collected := collectAnthropicEvents(t, events)
	if text != "partial" || requests.Load() != 1 {
		t.Fatalf("stream was replayed after output: text=%q requests=%d events=%+v", text, requests.Load(), collected)
	}
	if len(collected) == 0 || collected[len(collected)-1].Type != "error" {
		t.Fatalf("expected terminal error event: %+v", collected)
	}
}

func TestAnthropicProviderContextCancellationDoesNotFailOver(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	_ = createAnthropicAPIKeyAccount(t, store, "cancel-first", 10, false)
	_ = createAnthropicAPIKeyAccount(t, store, "cancel-second", 20, false)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	ctx, cancel := context.WithCancel(context.Background())
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	_, _ = collectAnthropicEvents(t, events)
	close(release)
	if requests.Load() != 1 {
		t.Fatalf("canceled request failed over to another account: requests=%d", requests.Load())
	}
}

func TestAnthropicProviderSuppressesDuplicateConfiguredAPIKey(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	secret := "duplicate-configured-key"
	account := createAnthropicAPIKeyAccount(t, store, secret, 10, false)
	var requests atomic.Int32
	telemetry := &recordingAnthropicTelemetry{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`))
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, APIKey: secret, Model: "claude-test"})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, collected := collectAnthropicEvents(t, events)
	if requests.Load() != 1 {
		t.Fatalf("duplicate configured API key was retried as a second account: requests=%d", requests.Load())
	}
	if len(collected) != 1 || collected[0].Type != "error" {
		t.Fatalf("unexpected terminal events: %+v", collected)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 1 || telemetry.attempts[0].AccountID != account.Credential.ID {
		t.Fatalf("duplicate fallback changed telemetry identity: %+v", telemetry.attempts)
	}
}

func TestAnthropicProviderListModelsUsesAllAccountsAndDeduplicatesWithoutGenerationTelemetry(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	_ = createAnthropicAPIKeyAccount(t, store, "models-first", 10, false)
	_ = createAnthropicAPIKeyAccount(t, store, "models-second", 20, false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") == "models-first" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","type":"model"},{"id":"claude-shared","type":"model"}],"has_more":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-shared","type":"model"},{"id":"claude-b","type":"model"}],"has_more":false}`))
	}))
	defer server.Close()
	telemetry := &recordingAnthropicTelemetry{}
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	provider.SetAccountTelemetry(telemetry)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "claude-a,claude-shared,claude-b" {
		t.Fatalf("unexpected deduplicated models: %v", models)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 0 || len(telemetry.quotas) != 0 {
		t.Fatalf("ListModels recorded generation telemetry: attempts=%v quotas=%v", telemetry.attempts, telemetry.quotas)
	}
}

func TestAnthropicProviderListModelsFallsBackToSameOriginOpenAIModels(t *testing.T) {
	var openAICatalogRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/anthropic/v1/models":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"not found"}}`))
		case "/v1/models":
			openAICatalogRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("fallback request missing Bearer authorization: %q", r.Header.Get("Authorization"))
			}
			if r.Header.Get("X-Api-Key") != "test-key" {
				t.Errorf("fallback request missing x-api-key: %q", r.Header.Get("X-Api-Key"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat","type":"model"},{"id":"deepseek-reasoner","type":"model"}]}`))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL + "/anthropic", APIKey: "test-key", Model: "deepseek-chat"})

	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "deepseek-chat,deepseek-reasoner" {
		t.Fatalf("unexpected fallback models: %v", models)
	}
	if openAICatalogRequests.Load() != 1 {
		t.Fatalf("expected exactly one same-origin catalog request, got %d", openAICatalogRequests.Load())
	}
}

func TestAnthropicProviderListModelsDoesNotFallbackWithoutKey(t *testing.T) {
	var openAICatalogRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/anthropic/v1/models":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"not_found_error","message":"not found"}}`))
		case "/v1/models", "/models":
			openAICatalogRequests.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL + "/anthropic", Model: "deepseek-chat"})

	models, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatalf("expected the 404 catalog error without a key, got models: %v", models)
	}
	if openAICatalogRequests.Load() != 0 {
		t.Fatalf("fallback must not run without a key: %d same-origin requests", openAICatalogRequests.Load())
	}
}

func TestAnthropicProviderErrorsAreRedactedAndNonRetryableErrorsStop(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	secret := "sk-ant-secret-never-leak"
	selected := createAnthropicAPIKeyAccount(t, store, secret, 10, false)
	_ = createAnthropicAPIKeyAccount(t, store, "unused-second-key", 20, false)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad key ` + secret + `"}}`))
	}))
	defer server.Close()
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, collected := collectAnthropicEvents(t, events)
	if requests.Load() != 1 {
		t.Fatalf("non-retryable error rotated accounts: requests=%d", requests.Load())
	}
	if len(collected) != 2 || collected[0].Dispatch == nil || collected[0].Dispatch.Provider != provider.Name() || collected[0].Dispatch.Model != "claude-test" || collected[0].Dispatch.CredentialID != selected.Credential.ID || collected[1].Type != "error" || !strings.Contains(collected[1].Text, "HTTP 400") {
		t.Fatalf("unexpected attributed error events: %+v", collected)
	}
	if strings.Contains(collected[1].Text, secret) || strings.Contains(collected[1].Text, "bad key") {
		t.Fatalf("error leaked upstream secret/body: %q", collected[1].Text)
	}
}

// TestAnthropicMessagesMergeParallelToolResults is the regression guard for the
// bug this merge exists to prevent: a turn with parallel tool calls is stored as
// one user message per tool result, and replaying that verbatim is rejected with
// "Invalid message sequence: tool_use and tool_result blocks must be correctly
// paired and ordered" — permanently, because every later request replays it.
func TestAnthropicMessagesMergeParallelToolResults(t *testing.T) {
	messages, _ := anthropicMessages([]Message{
		{Role: "user", Content: "list the files"},
		{Role: "assistant", Blocks: []ContentBlock{
			{Type: "tool_use", ToolUseID: "tool-a", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			{Type: "tool_use", ToolUseID: "tool-b", ToolName: "Glob", Input: json.RawMessage(`{"pattern":"*.go"}`)},
		}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-a", Output: "out-a"}}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-b", Output: "out-b"}}},
	}, "", "claude-sonnet-4-5")

	if len(messages) != 3 {
		t.Fatalf("parallel tool results must collapse into one user message, got %d messages", len(messages))
	}
	if messages[0].Role != anthropic.MessageParamRoleUser || messages[1].Role != anthropic.MessageParamRoleAssistant || messages[2].Role != anthropic.MessageParamRoleUser {
		t.Fatalf("unexpected roles: %v %v %v", messages[0].Role, messages[1].Role, messages[2].Role)
	}
	results := messages[2].Content
	if len(results) != 2 || results[0].OfToolResult == nil || results[1].OfToolResult == nil {
		t.Fatalf("expected both tool_result blocks in the trailing user message: %+v", results)
	}
	// Order must survive the merge: a tool_result is matched to its tool_use by
	// id, but reordering would still change what the model reads first.
	if results[0].OfToolResult.ToolUseID != "tool-a" || results[1].OfToolResult.ToolUseID != "tool-b" {
		t.Fatalf("tool_result order changed: %s then %s", results[0].OfToolResult.ToolUseID, results[1].OfToolResult.ToolUseID)
	}
}

// Merging runs on the emitted slice, so user messages that were not adjacent in
// the input still merge once the entries between them are hoisted or dropped.
func TestAnthropicMessagesMergeAcrossVanishedMessages(t *testing.T) {
	foreignThinking := NewAnthropicThinkingContentBlock("claude-opus-4-6", "other model", "sig-other")
	messages, system := anthropicMessages([]Message{
		{Role: "assistant", Blocks: []ContentBlock{
			{Type: "tool_use", ToolUseID: "tool-a", ToolName: "Bash", Input: json.RawMessage(`{}`)},
			{Type: "tool_use", ToolUseID: "tool-b", ToolName: "Glob", Input: json.RawMessage(`{}`)},
		}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-a", Output: "out-a"}}},
		// Hoisted into system, so it must not split the tool_result run.
		{Role: "system", Content: "sidecar note"},
		// Dropped entirely: its only block is thinking signed by another model.
		{Role: "assistant", Blocks: []ContentBlock{foreignThinking}},
		{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "tool-b", Output: "out-b"}}},
	}, "", "claude-sonnet-4-5")

	if len(system) != 1 || system[0].Text != "sidecar note" {
		t.Fatalf("system message was not hoisted: %+v", system)
	}
	if len(messages) != 2 {
		t.Fatalf("expected assistant + one merged user message, got %d", len(messages))
	}
	results := messages[1].Content
	if len(results) != 2 || results[0].OfToolResult == nil || results[1].OfToolResult == nil {
		t.Fatalf("tool_result blocks were split across messages: %+v", results)
	}
}

// The merge must not flatten an ordinary conversation into one turn.
func TestAnthropicMessagesPreserveAlternation(t *testing.T) {
	messages, _ := anthropicMessages([]Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer one"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "answer two"},
	}, "", "claude-sonnet-4-5")

	wantRoles := []anthropic.MessageParamRole{
		anthropic.MessageParamRoleUser,
		anthropic.MessageParamRoleAssistant,
		anthropic.MessageParamRoleUser,
		anthropic.MessageParamRoleAssistant,
	}
	if len(messages) != len(wantRoles) {
		t.Fatalf("alternating turns must not merge, got %d messages", len(messages))
	}
	for index, want := range wantRoles {
		if messages[index].Role != want {
			t.Fatalf("message %d role = %v, want %v", index, messages[index].Role, want)
		}
	}
}

// A relay that reports no error type used to surface as "HTTP 400 (<nil>)",
// which named no cause and stored a meaningless "nil" telemetry code. The status
// text now fills in, the rejection is classified in Autoto's own words, and no
// upstream byte is forwarded.
func TestAnthropicProviderClassifiesRejectionWithoutForwardingBody(t *testing.T) {
	storeDir := t.TempDir()
	store := anthropicauth.NewStore(storeDir)
	account := createAnthropicAPIKeyAccount(t, store, "sk-ant-classify-key", 10, false)
	upstream := "Invalid message sequence: tool_use and tool_result blocks must be correctly paired and ordered. (request id: abc123)"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"<nil>","message":` + strconv.Quote(upstream) + `}}`))
	}))
	defer server.Close()
	telemetry := &recordingAnthropicTelemetry{}
	provider := NewAnthropicProvider(config.ProviderConfig{BaseURL: server.URL, CredentialStorePath: storeDir, Model: "claude-test"})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, collected := collectAnthropicEvents(t, events)

	var errorText string
	for _, event := range collected {
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if errorText == "" {
		t.Fatalf("expected an error event: %+v", collected)
	}
	if !strings.Contains(errorText, "HTTP 400") || !strings.Contains(errorText, "Bad Request") {
		t.Fatalf("status was not reported: %q", errorText)
	}
	if !strings.Contains(errorText, "工具调用与工具结果配对不合法") {
		t.Fatalf("rejection was not classified: %q", errorText)
	}
	if strings.Contains(errorText, "<nil>") || strings.Contains(errorText, "(nil)") {
		t.Fatalf("uninformative error type reached the operator: %q", errorText)
	}
	// The classification is local: none of the upstream body may be echoed.
	for _, forbidden := range []string{"Invalid message sequence", "request id", "abc123"} {
		if strings.Contains(errorText, forbidden) {
			t.Fatalf("upstream body fragment %q leaked: %q", forbidden, errorText)
		}
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 1 {
		t.Fatalf("expected exactly one recorded attempt: %+v", telemetry.attempts)
	}
	attempt := telemetry.attempts[0]
	if attempt.Success || attempt.HTTPStatus != http.StatusBadRequest || attempt.AccountID != account.Credential.ID {
		t.Fatalf("unexpected attempt telemetry: %+v", attempt)
	}
	if attempt.ErrorCode != "http_400" {
		t.Fatalf("telemetry error code = %q, want http_400", attempt.ErrorCode)
	}
}
