package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/codexauth"
	"autoto/internal/config"
)

func newCodexRefreshTestProvider(upstreamURL, storeDir string) *CodexProvider {
	return NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        upstreamURL,
		Model:                          "gpt-a",
		CredentialStorePath:            storeDir,
		CodexAllowInsecureTestEndpoint: true,
		CodexRefreshURLForTest:         upstreamURL + "/oauth/token",
	})
}

func TestCodexProviderGatewayRequiresExplicitSubscriptionAuthorization(t *testing.T) {
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: codexauth.DefaultBaseURL})
	events, err := provider.Generate(context.Background(), GenerateRequest{Scenario: CallScenarioGateway})
	if events != nil || !errors.Is(err, ErrGatewayOAuthUnsupported) {
		t.Fatalf("expected fail-closed Gateway authorization, events=%v err=%v", events, err)
	}
	if provider.ConfiguredForScenario(CallScenarioGateway) {
		t.Fatal("static Gateway readiness must not authorize Codex accounts")
	}
}

func TestCodexProviderGatewayUsesOnlyGrantedEnabledAccounts(t *testing.T) {
	var requestedAccounts []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedAccounts = append(requestedAccounts, r.Header.Get("ChatGPT-Account-ID"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{
		{Filename: "ungranted.json", Content: []byte(`{"type":"codex","access_token":"ungranted-token","account_id":"ungranted","priority":1}`)},
		{Filename: "granted.json", Content: []byte(`{"type":"codex","access_token":"granted-token","account_id":"granted","priority":10}`)},
		{Filename: "disabled.json", Content: []byte(`{"type":"codex","access_token":"disabled-token","account_id":"disabled","priority":0,"disabled":true}`)},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var grantedID string
	for _, item := range items {
		if item.Credential.AccountID == "granted" {
			grantedID = item.Credential.ID
		}
	}
	if grantedID == "" {
		t.Fatal("granted fixture did not receive a stable ID")
	}

	provider := NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        upstream.URL,
		Model:                          "gpt-test",
		CredentialStorePath:            storeDir,
		CodexAllowInsecureTestEndpoint: true,
	})
	if AvailableForScenario(context.Background(), provider, false, ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("Gateway Codex must fail closed before a grant policy is injected")
	}
	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{"codex": {grantedID}}})
	if !AvailableForScenario(context.Background(), provider, false, ScenarioAvailability{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}) {
		t.Fatal("granted Codex account was not dynamically available")
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Scenario:                     CallScenarioGateway,
		AllowSubscriptionCredentials: true,
		Messages:                     []Message{{Role: "user", Content: "hello"}},
	})
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
	if strings.Join(requestedAccounts, ",") != "granted" {
		t.Fatalf("Gateway used an ungranted or disabled account: %v", requestedAccounts)
	}
	if dispatch == nil || dispatch.CredentialID != grantedID {
		t.Fatalf("Gateway dispatch lost granted credential attribution: %+v", dispatch)
	}

	provider.SetGatewayAccountPolicy(staticGatewayAccountPolicy{ids: map[string][]string{"codex": {}}})
	if events, err := provider.Generate(context.Background(), GenerateRequest{Scenario: CallScenarioGateway, AllowSubscriptionCredentials: true}); err == nil || events != nil {
		t.Fatalf("empty grants must fail closed, events=%v err=%v", events, err)
	}
}

func TestCodexFallbackFastCapabilitiesUseExplicitOfficialCatalogEntries(t *testing.T) {
	provider := NewCodexProvider(config.ProviderConfig{
		Name:    "codex",
		Type:    config.ProviderTypeCodex,
		BaseURL: codexauth.DefaultBaseURL,
		Model:   "gpt-5.5",
	})
	fast := ModelCapabilitiesFor(provider, "gpt-5.5")
	if !fast.FastModeKnown || !fast.FastMode {
		t.Fatalf("expected official gpt-5.5 Fast fallback capability, got %+v", fast)
	}
	if unknown := ModelCapabilitiesFor(provider, "gpt-5.2"); unknown.FastModeKnown || unknown.FastMode {
		t.Fatalf("unexpected inferred Fast capability for unmarked model: %+v", unknown)
	}

	custom := NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        "http://127.0.0.1:7789",
		Model:                          "gpt-5.5",
		CodexAllowInsecureTestEndpoint: true,
	})
	if capability := ModelCapabilitiesFor(custom, "gpt-5.5"); capability.FastModeKnown || capability.FastMode {
		t.Fatalf("custom Codex endpoints must not inherit the official fallback catalog: %+v", capability)
	}
}

func TestParseCodexModelCatalogMarksFastModeKnownOnlyForExplicitFields(t *testing.T) {
	models, capabilities, err := parseCodexModelCatalog(strings.NewReader(`{"models":[{"slug":"unknown"},{"slug":"standard","additional_speed_tiers":[]},{"slug":"fast","service_tiers":[{"id":"priority"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "fast,standard,unknown" {
		t.Fatalf("unexpected models: %v", models)
	}
	if capability := capabilities["unknown"]; capability.FastModeKnown || capability.FastMode {
		t.Fatalf("missing Fast fields must stay unknown: %+v", capability)
	}
	if capability := capabilities["standard"]; !capability.FastModeKnown || capability.FastMode {
		t.Fatalf("explicit empty speed tiers must be known unsupported: %+v", capability)
	}
	if capability := capabilities["fast"]; !capability.FastModeKnown || !capability.FastMode {
		t.Fatalf("priority tier must be known supported: %+v", capability)
	}
}

func TestCodexProviderListsModelsAndStreamsDirectly(t *testing.T) {
	var responseRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fixture-access" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "account-1" {
			t.Fatalf("unexpected account header: %q", got)
		}
		if got := r.Header.Get("originator"); got != "autoto" {
			t.Fatalf("unexpected originator header: %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "Autoto Codex Transport/1.0" {
			t.Fatalf("unexpected configured user agent: %q", got)
		}
		if got := r.Header.Get("X-Codex-Test"); got != "codex-header-secret" {
			t.Fatalf("unexpected configured request header: %q", got)
		}
		switch r.URL.Path {
		case "/models":
			if got := r.URL.Query().Get("client_version"); got != "1.2.3" {
				t.Fatalf("unexpected client_version query: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-z","additional_speed_tiers":[]},{"slug":"gpt-a","service_tiers":[{"service_tier":"priority","name":"Fast"}]}]}`))
		case "/responses":
			responseRequests++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["model"] != "gpt-a" || body["stream"] != true || body["store"] != false || body["tool_choice"] != "auto" || body["service_tier"] != "priority" {
				t.Fatalf("unexpected Codex request body: %+v", body)
			}
			reasoning, _ := body["reasoning"].(map[string]any)
			if reasoning["effort"] != "xhigh" {
				t.Fatalf("Codex xhigh reasoning effort was not sent: %+v", body)
			}
			metadata, _ := body["client_metadata"].(map[string]any)
			if metadata["x-codex-installation-id"] != "123e4567-e89b-42d3-a456-426614174000" {
				t.Fatalf("missing installation metadata: %+v", metadata)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"input_tokens_details\":{\"cached_tokens\":3},\"output_tokens\":4,\"output_tokens_details\":{\"reasoning_tokens\":2}}}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","access_token":"fixture-access","refresh_token":"rt_fixture","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(config.ProviderConfig{
		Name:                           "codex",
		Type:                           config.ProviderTypeCodex,
		BaseURL:                        upstream.URL,
		Model:                          "gpt-a",
		ClientVersion:                  "1.2.3",
		InstallationID:                 "123e4567-e89b-42d3-a456-426614174000",
		CredentialStorePath:            storeDir,
		UserAgent:                      "Autoto Codex Transport/1.0",
		RequestHeaders:                 []config.ProviderRequestHeader{{Name: "X-Codex-Test", Value: "codex-header-secret"}},
		CodexAllowInsecureTestEndpoint: true,
	})
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "gpt-a,gpt-z" {
		t.Fatalf("unexpected models: %v", models)
	}
	if !ModelCapabilitiesFor(provider, "gpt-a").FastMode || ModelCapabilitiesFor(provider, "gpt-z").FastMode {
		t.Fatalf("unexpected model Fast capabilities: gpt-a=%+v gpt-z=%+v", ModelCapabilitiesFor(provider, "gpt-a"), ModelCapabilitiesFor(provider, "gpt-z"))
	}

	events, err := provider.Generate(context.Background(), GenerateRequest{

		Model:           "gpt-a",
		SystemPrompt:    "be useful",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		Tools:           []ToolSpec{{Name: "lookup", Description: "lookup", Schema: map[string]any{"type": "object"}}},
		ReasoningEffort: "xhigh",
		FastMode:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var toolCall *ToolCall
	var usage *Usage
	var done bool
	var dispatch *DispatchInfo
	for event := range events {
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
		switch event.Type {
		case "text":
			text += event.Text
		case "tool_call":
			toolCall = event.ToolCall
		case "usage":
			usage = event.Usage
		case "done":
			done = event.Done
		case "error":
			t.Fatalf("unexpected stream error: %s", event.Text)
		}
	}
	if responseRequests != 1 || text != "hello" || toolCall == nil || toolCall.ID != "call-1" || toolCall.Name != "lookup" || usage == nil || usage.InputTokens != 10 || usage.CachedInputTokens != 3 || usage.OutputTokens != 4 || usage.ReasoningTokens != 2 || !done {
		t.Fatalf("unexpected streamed result: requests=%d text=%q tool=%+v usage=%+v done=%v", responseRequests, text, toolCall, usage, done)
	}
	if dispatch == nil || dispatch.Provider != "codex" || dispatch.Model != "gpt-a" || dispatch.CredentialID == "" {
		t.Fatalf("unexpected dispatch attribution: %+v", dispatch)
	}
}

func TestCodexProviderRefreshesAndPersistsCredential(t *testing.T) {
	futureToken := testCodexProviderJWT(t, map[string]any{
		"exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "account-1",
			"chatgpt_plan_type":  "plus",
		},
	})
	refreshRequests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshRequests++
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["client_id"] != codexOAuthClientID || body["grant_type"] != "refresh_token" || body["refresh_token"] != "rt_old" {
				t.Fatalf("unexpected refresh body: %+v", body)
			}
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt_new"}`, futureToken)
		case "/responses":
			if got := r.Header.Get("Authorization"); got != "Bearer "+futureToken {
				t.Fatalf("refreshed token was not used: %q", got)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","refresh_token":"rt_old","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := newCodexRefreshTestProvider(upstream.URL, storeDir)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error: %s", event.Text)
		}
	}
	if refreshRequests != 1 {
		t.Fatalf("expected one refresh request, got %d", refreshRequests)
	}
	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Credential.AccessToken != futureToken || items[0].Credential.RefreshToken != "rt_new" || items[0].Credential.PlanType != "plus" || items[0].Credential.Expired == "" {
		t.Fatalf("refreshed credential was not persisted: %+v", items)
	}
}

func TestCodexProviderRefreshesAndRetriesAfterUnauthorized(t *testing.T) {
	var responseRequests atomic.Int32
	var refreshRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshRequests.Add(1)
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","refresh_token":"rt_fresh","expires_in":3600}`))
		case "/responses":
			responseRequests.Add(1)
			switch r.Header.Get("Authorization") {
			case "Bearer stale-access":
				w.WriteHeader(http.StatusUnauthorized)
			case "Bearer fresh-access":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
			default:
				t.Fatalf("unexpected authorization: %q", r.Header.Get("Authorization"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","access_token":"stale-access","refresh_token":"rt_stale","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := newCodexRefreshTestProvider(upstream.URL, storeDir)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error after refresh: %s", event.Text)
		}
	}
	if responseRequests.Load() != 2 || refreshRequests.Load() != 1 {
		t.Fatalf("unexpected retry counts: responses=%d refresh=%d", responseRequests.Load(), refreshRequests.Load())
	}
	items, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Credential.AccessToken != "fresh-access" || items[0].Credential.RefreshToken != "rt_fresh" {
		t.Fatalf("401 refresh was not persisted: %+v", items)
	}
}

func TestCodexProviderCoalescesConcurrentRefreshes(t *testing.T) {
	futureToken := testCodexProviderJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var refreshRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshRequests.Add(1)
			_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt_new"}`, futureToken)
		case "/responses":
			if r.Header.Get("Authorization") != "Bearer "+futureToken {
				t.Fatalf("unexpected authorization after refresh: %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","refresh_token":"rt_old","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := newCodexRefreshTestProvider(upstream.URL, storeDir)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
			if err != nil {
				errorsCh <- err
				return
			}
			for event := range events {
				if event.Type == "error" {
					errorsCh <- errors.New(event.Text)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if got := refreshRequests.Load(); got != 1 {
		t.Fatalf("expected one coalesced refresh request, got %d", got)
	}
}

func TestCodexProviderCancellationDoesNotWaitForConcurrentRefresh(t *testing.T) {
	refreshStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	var refreshRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			refreshRequests.Add(1)
			select {
			case refreshStarted <- struct{}{}:
			default:
			}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","refresh_token":"rt_fresh","expires_in":3600}`))
		case "/responses":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	defer releaseHandler()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","refresh_token":"rt_old","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountTelemetry{}
	provider := newCodexRefreshTestProvider(upstream.URL, storeDir)
	provider.SetAccountTelemetry(telemetry)
	firstEvents, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "first"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshStarted:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	secondEvents, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "second"}}})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case _, ok := <-secondEvents:
		if ok {
			t.Fatal("canceled refresh waiter emitted an event")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled refresh waiter remained blocked")
	}
	telemetry.mu.Lock()
	if len(telemetry.attempts) != 0 {
		t.Fatalf("canceled refresh waiter recorded telemetry: %+v", telemetry.attempts)
	}
	telemetry.mu.Unlock()

	releaseHandler()
	for event := range firstEvents {
		if event.Type == "error" {
			t.Fatalf("unexpected first request error: %s", event.Text)
		}
	}
	if refreshRequests.Load() != 1 {
		t.Fatalf("unexpected refresh count: %d", refreshRequests.Load())
	}
}

func TestCodexProviderRedactsCredentialFromUpstreamError(t *testing.T) {
	const secret = "fixture-access-sensitive"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"code":"bad_request","message":"token %s rejected"}}`, secret)
	}))
	defer upstream.Close()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "account.json", Content: []byte(`{"type":"codex","access_token":"` + secret + `","account_id":"account-1"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := newCodexRefreshTestProvider(upstream.URL, storeDir)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var errorText string
	for event := range events {
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if errorText == "" || strings.Contains(errorText, secret) || !strings.Contains(errorText, "[redacted]") {
		t.Fatalf("credential was not safely redacted: %q", errorText)
	}
}

type recordingAccountTelemetry struct {
	mu       sync.Mutex
	attempts []ProviderAccountAttempt
}

func (r *recordingAccountTelemetry) RecordProviderAccountAttempt(_ context.Context, attempt ProviderAccountAttempt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempts = append(r.attempts, attempt)
	return nil
}

func TestCodexProviderPriorityFailoverRecordsFinalAccountOutcomesOnly(t *testing.T) {
	var requestOrder []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestOrder = append(requestOrder, r.Header.Get("ChatGPT-Account-ID"))
		if r.URL.Path == "/models" {
			_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-test"}]}`))
			return
		}
		if r.Header.Get("ChatGPT-Account-ID") == "priority-first" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limit_exceeded"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{}}\n\n")
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{
		{Filename: "a.json", Content: []byte(`{"type":"codex","access_token":"second-token","account_id":"priority-second","priority":200}`)},
		{Filename: "z.json", Content: []byte(`{"type":"codex","access_token":"first-token","account_id":"priority-first","priority":10}`)},
	}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountTelemetry{}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	provider.SetAccountTelemetry(telemetry)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(telemetry.attempts) != 0 {
		t.Fatalf("model listing must not count as model telemetry: %+v", telemetry.attempts)
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected final error: %s", event.Text)
		}
	}
	if got := strings.Join(requestOrder, ","); got != "priority-first,priority-first,priority-second" {
		t.Fatalf("unexpected request order (models then generation): %s", got)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 2 || telemetry.attempts[0].Success || telemetry.attempts[0].HTTPStatus != 429 || telemetry.attempts[0].ErrorCode != "rate_limit_exceeded" || !telemetry.attempts[1].Success || telemetry.attempts[1].HTTPStatus != 200 {
		t.Fatalf("unexpected account telemetry: %+v", telemetry.attempts)
	}
}

func TestCodexProviderSyncAccountUsesUsageEndpointWithoutUnnecessaryRefresh(t *testing.T) {
	var refreshRequests atomic.Int32
	var usageRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			usageRequests.Add(1)
			if r.Header.Get("Authorization") != "Bearer quota-access" || r.Header.Get("ChatGPT-Account-ID") != "quota-account" {
				t.Fatalf("unexpected quota headers: auth=%q account=%q", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-ID"))
			}
			_, _ = w.Write([]byte(`{
				"plan_type":"plus",
				"rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_after_seconds":90},"secondaryWindow":{"usedPercent":"60","windowSeconds":604800}},
				"additionalRateLimits":[{"name":"gpt-test","rateLimit":{"primaryWindow":{"usedPercent":10}}}],
				"credits":{"hasCredits":true,"balance":"12.5"},
				"rateLimitReachedType":"secondary"
			}`))
		case "/oauth/token":
			refreshRequests.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "quota.json", Content: []byte(`{"type":"codex","access_token":"quota-access","refresh_token":"rt_quota","account_id":"quota-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ListAccounts()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("account setup failed: accounts=%+v err=%v", accounts, err)
	}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL + "/backend-api/codex", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	account, quota, err := provider.SyncAccount(context.Background(), accounts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if account.PlanType != "plus" || quota.PlanType != "plus" || quota.PrimaryWindow == nil || quota.PrimaryWindow.UsedPercent != 25 || quota.SecondaryWindow == nil || quota.SecondaryWindow.UsedPercent != 60 || len(quota.AdditionalRateLimits) != 1 || quota.Credits == nil || quota.Credits.Balance != 12.5 || quota.RateLimitReachedType != "secondary" {
		t.Fatalf("unexpected normalized quota: account=%+v quota=%+v", account, quota)
	}
	if usageRequests.Load() != 1 || refreshRequests.Load() != 0 {
		t.Fatalf("unexpected sync requests: usage=%d refresh=%d", usageRequests.Load(), refreshRequests.Load())
	}
}

func TestParseCodexQuotaGracefullyHandlesMissingFields(t *testing.T) {
	quota, err := parseCodexQuota(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":"not-a-number"}},"credits":{}}`), time.Unix(123, 0))
	if err != nil {
		t.Fatal(err)
	}
	if quota.PrimaryWindow == nil || quota.PrimaryWindow.UsedPercent != 0 || quota.SecondaryWindow != nil || quota.FetchedAt == "" {
		t.Fatalf("unexpected legacy quota fallback: %+v", quota)
	}
}

func TestCodexEndpointValidationAndRedirectPolicy(t *testing.T) {
	for _, endpoint := range []string{
		"https://chatgpt.com/backend-api/codex",
		"https://chat.openai.com/backend-api/codex/",
	} {
		if err := ValidateCodexProviderConfig(config.ProviderConfig{BaseURL: endpoint}); err != nil {
			t.Fatalf("official endpoint rejected: %s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"http://chatgpt.com/backend-api/codex",
		"https://evil.example/backend-api/codex",
		"https://chatgpt.com/other",
		"https://chatgpt.com:444/backend-api/codex",
		"https://chatgpt.com/backend-api/codex?target=other",
	} {
		if err := ValidateCodexProviderConfig(config.ProviderConfig{BaseURL: endpoint}); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if err := ValidateCodexProviderConfig(config.ProviderConfig{BaseURL: "http://127.0.0.1:1234/prefix"}); err == nil {
		t.Fatal("loopback endpoint was accepted without the explicit test option")
	}
	if err := ValidateCodexProviderConfig(config.ProviderConfig{BaseURL: "http://127.0.0.1:1234/prefix", CodexAllowInsecureTestEndpoint: true}); err != nil {
		t.Fatalf("loopback test endpoint rejected: %v", err)
	}
	if err := ValidateCodexProviderConfig(config.ProviderConfig{BaseURL: "http://example.test/prefix", CodexAllowInsecureTestEndpoint: true}); err == nil {
		t.Fatal("non-loopback test endpoint accepted")
	}

	via := httptest.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/responses", nil)
	downgrade := httptest.NewRequest(http.MethodGet, "http://chatgpt.com/backend-api/codex/responses", nil)
	if err := codexRedirectPolicy(downgrade, []*http.Request{via}); err == nil {
		t.Fatal("HTTPS downgrade redirect was accepted")
	}
	crossOrigin := httptest.NewRequest(http.MethodGet, "https://chat.openai.com/backend-api/codex/responses", nil)
	if err := codexRedirectPolicy(crossOrigin, []*http.Request{via}); err == nil {
		t.Fatal("cross-origin redirect was accepted")
	}
	sameOrigin := httptest.NewRequest(http.MethodGet, "https://chatgpt.com/backend-api/codex/other", nil)
	if err := codexRedirectPolicy(sameOrigin, []*http.Request{via}); err != nil {
		t.Fatalf("same-origin HTTPS redirect was rejected: %v", err)
	}
	if err := codexRefreshRedirectPolicy(downgrade, []*http.Request{via}); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("OAuth refresh HTTPS downgrade was not rejected: %v", err)
	}
}

func TestCodexRefreshEndpointAllowsOnlyExplicitLoopbackTestInjection(t *testing.T) {
	production := NewCodexProvider(config.ProviderConfig{
		Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "https://chatgpt.com/backend-api/codex", CredentialStorePath: t.TempDir(),
	})
	if production.endpointErr != nil || production.refreshEndpoint != codexOAuthRefreshURL {
		t.Fatalf("production refresh endpoint was not fixed: endpoint=%q err=%v", production.refreshEndpoint, production.endpointErr)
	}

	for _, cfg := range []config.ProviderConfig{
		{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "https://chatgpt.com/backend-api/codex", CredentialStorePath: t.TempDir(),
			CodexRefreshURLForTest: "http://127.0.0.1:9999/oauth/token",
		},
		{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "http://127.0.0.1:9999", CredentialStorePath: t.TempDir(), CodexAllowInsecureTestEndpoint: true,
			CodexRefreshURLForTest: "https://example.test/oauth/token",
		},
		{
			Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "http://127.0.0.1:9999", CredentialStorePath: t.TempDir(), CodexAllowInsecureTestEndpoint: true,
			CodexRefreshURLForTest: "http://127.0.0.1:9999/oauth/token?next=external",
		},
	} {
		if err := ValidateCodexProviderConfig(cfg); err == nil {
			t.Fatalf("unsafe refresh test endpoint was accepted: %+v", cfg)
		}
	}

	injected := NewCodexProvider(config.ProviderConfig{
		Name: "codex", Type: config.ProviderTypeCodex, BaseURL: "http://127.0.0.1:9999", CredentialStorePath: t.TempDir(), CodexAllowInsecureTestEndpoint: true,
		CodexRefreshURLForTest: "http://127.0.0.1:9999/oauth/token",
	})
	if injected.endpointErr != nil || injected.refreshEndpoint != "http://127.0.0.1:9999/oauth/token" {
		t.Fatalf("explicit loopback refresh injection was rejected: endpoint=%q err=%v", injected.refreshEndpoint, injected.endpointErr)
	}
}

func TestCodexProviderRefreshDoesNotFollowCrossOriginRedirect(t *testing.T) {
	const refreshToken = "rt_refresh_redirect_fixture"
	received := make(chan struct {
		body string
	}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		received <- struct{ body string }{string(data)}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "redirect.json", Content: []byte(`{"type":"codex","refresh_token":"` + refreshToken + `","account_id":"redirect-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := newCodexRefreshTestProvider(source.URL, storeDir)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var errorText string
	for event := range events {
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if errorText == "" || strings.Contains(errorText, refreshToken) {
		t.Fatalf("refresh failure was missing or leaked the refresh token: %q", errorText)
	}
	select {
	case <-received:
		t.Fatal("cross-origin refresh redirect reached the target")
	default:
	}
}

func TestCodexProviderBlocksCrossOriginRedirectWithoutForwardingCredentials(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("ChatGPT-Account-ID") != "" {
			t.Fatalf("credential headers followed cross-origin redirect")
		}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "redirect.json", Content: []byte(`{"type":"codex","access_token":"redirect-secret","account_id":"redirect-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: source.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect reached target %d times", targetRequests.Load())
	}
}

func TestCodexProviderRedactsJWTFromErrorCode(t *testing.T) {
	secretJWT := testCodexProviderJWT(t, map[string]any{"sub": "sensitive"})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, `{"error":{"code":%q,"message":"rejected"}}`, secretJWT)
	}))
	defer upstream.Close()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	content, _ := json.Marshal(map[string]any{"type": "codex", "access_token": secretJWT, "account_id": "jwt-account"})
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "jwt.json", Content: content}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var errorText string
	for event := range events {
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if errorText == "" || strings.Contains(errorText, secretJWT) || !strings.Contains(errorText, "redacted") {
		t.Fatalf("JWT leaked through upstream error code: %q", errorText)
	}
}

func TestSanitizeCodexErrorCodeRedactsEmbeddedSecrets(t *testing.T) {
	credential := codexauth.Credential{AccessToken: "fixture-access"}
	for _, code := range []string{
		"upstream:sk-sensitive-value",
		"refresh:rt_sensitive_value",
		"token:fixture-access",
		testCodexProviderJWT(t, map[string]any{"sub": "sensitive"}),
	} {
		if got := sanitizeCodexErrorCode(code, credential); got != "redacted" {
			t.Fatalf("sensitive error code was not redacted: code=%q got=%q", code, got)
		}
	}
	for _, code := range []string{"rate_limit_exceeded", "support_ticket_required"} {
		if got := sanitizeCodexErrorCode(code, credential); got != code {
			t.Fatalf("safe upstream code changed unexpectedly: code=%q got=%q", code, got)
		}
	}
}

func TestCodexProviderCancellationStopsFailoverWithoutTelemetryFailure(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	var secondRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ChatGPT-Account-ID") == "cancel-second" {
			secondRequests.Add(1)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer releaseHandler()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{
		{Filename: "first.json", Content: []byte(`{"type":"codex","access_token":"first","account_id":"cancel-first","priority":1}`)},
		{Filename: "second.json", Content: []byte(`{"type":"codex","access_token":"second","account_id":"cancel-second","priority":2}`)},
	}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountTelemetry{}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	provider.SetAccountTelemetry(telemetry)
	ctx, cancel := context.WithCancel(context.Background())
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Codex request did not start")
	}
	cancel()
	select {
	case _, ok := <-events:
		for ok {
			select {
			case _, ok = <-events:
			case <-time.After(time.Second):
				t.Fatal("Codex event stream did not close after cancellation")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Codex event stream did not react to cancellation")
	}
	releaseHandler()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if secondRequests.Load() != 0 || len(telemetry.attempts) != 0 {
		t.Fatalf("cancellation polluted failover telemetry: second=%d attempts=%+v", secondRequests.Load(), telemetry.attempts)
	}
}

func TestCodexProviderDeadlineStopsFailoverWithoutTelemetryFailure(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	var secondRequests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("ChatGPT-Account-ID") == "deadline-second" {
			secondRequests.Add(1)
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer releaseHandler()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{
		{Filename: "first.json", Content: []byte(`{"type":"codex","access_token":"first","account_id":"deadline-first","priority":1}`)},
		{Filename: "second.json", Content: []byte(`{"type":"codex","access_token":"second","account_id":"deadline-second","priority":2}`)},
	}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountTelemetry{}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	provider.SetAccountTelemetry(telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first Codex request did not start")
	}
	select {
	case _, ok := <-events:
		for ok {
			select {
			case _, ok = <-events:
			case <-time.After(time.Second):
				t.Fatal("Codex event stream did not close after deadline")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("Codex event stream did not react to deadline")
	}
	releaseHandler()
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if secondRequests.Load() != 0 || len(telemetry.attempts) != 0 {
		t.Fatalf("deadline polluted failover telemetry: second=%d attempts=%+v", secondRequests.Load(), telemetry.attempts)
	}
}

func TestCodexIncompleteResponseCountsAsSuccessfulAttempt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n")
	}))
	defer upstream.Close()
	storeDir := filepath.Join(t.TempDir(), "codex")
	store := codexauth.NewStore(storeDir)
	if _, err := store.Import([]codexauth.ImportDocument{{Filename: "incomplete.json", Content: []byte(`{"type":"codex","access_token":"incomplete","account_id":"incomplete-account"}`)}}); err != nil {
		t.Fatal(err)
	}
	telemetry := &recordingAccountTelemetry{}
	provider := NewCodexProvider(config.ProviderConfig{Name: "codex", Type: config.ProviderTypeCodex, BaseURL: upstream.URL, Model: "gpt-test", CredentialStorePath: storeDir, CodexAllowInsecureTestEndpoint: true})
	provider.SetAccountTelemetry(telemetry)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var done Event
	for event := range events {
		if event.Type == "done" {
			done = event
		}
	}
	if !done.Done || done.StopReason != "max_output_tokens" {
		t.Fatalf("incomplete response did not finish cleanly: %+v", done)
	}
	telemetry.mu.Lock()
	defer telemetry.mu.Unlock()
	if len(telemetry.attempts) != 1 || !telemetry.attempts[0].Success || telemetry.attempts[0].HTTPStatus != http.StatusOK || telemetry.attempts[0].ErrorCode != "" {
		t.Fatalf("incomplete response was not recorded as a valid attempt: %+v", telemetry.attempts)
	}
}

func TestParseCodexQuotaRejectsTrailingData(t *testing.T) {
	if _, err := parseCodexQuota(strings.NewReader(`{"plan_type":"plus"} {"extra":true}`), time.Now()); err == nil {
		t.Fatal("quota parser accepted trailing JSON data")
	}
}

func TestCodexUsageURLPreservesPrefixOrExplicitTestEndpoint(t *testing.T) {
	provider := NewCodexProvider(config.ProviderConfig{BaseURL: "http://127.0.0.1:1234/prefix/backend-api/codex", CodexAllowInsecureTestEndpoint: true})
	if got := provider.usageURL(); got != "http://127.0.0.1:1234/prefix/backend-api/wham/usage" {
		t.Fatalf("unexpected derived usage URL: %s", got)
	}
	provider = NewCodexProvider(config.ProviderConfig{BaseURL: "http://127.0.0.1:1234/prefix/codex", CodexUsageURL: "http://127.0.0.1:1234/mock/usage", CodexAllowInsecureTestEndpoint: true})
	if got := provider.usageURL(); got != "http://127.0.0.1:1234/mock/usage" {
		t.Fatalf("explicit test usage URL was not preserved: %s", got)
	}
}

func testCodexProviderJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
}
