package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

func TestKimiProviderChatPathDeviceHeadersModelAndRefresh(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	store := subscriptionauth.NewStore(t.TempDir())
	account := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{
		provider: subscriptionauth.ProviderKimi, priority: 10, accessToken: "old-access", refreshToken: "keep-refresh",
		expiresAt: now.Add(time.Minute).Format(time.RFC3339Nano), deviceID: "kimi-device-123",
	})
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected Kimi request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed-access" {
			t.Fatalf("unexpected authorization header %q", got)
		}
		if got := r.Header.Get("X-Msh-Platform"); got != "Autoto" {
			t.Fatalf("unexpected platform header %q", got)
		}
		if got := r.Header.Get("X-Msh-Version"); got != "1.2.3" {
			t.Fatalf("unexpected version header %q", got)
		}
		if got := r.Header.Get("X-Msh-Device-Id"); got != "kimi-device-123" {
			t.Fatalf("unexpected device ID header %q", got)
		}
		if r.Header.Get("X-Msh-Device-Name") == "" || r.Header.Get("X-Msh-Device-Model") == "" {
			t.Fatalf("Kimi device name/model headers are required: %+v", r.Header)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writeKimiTestStream(w)
	}))
	defer server.Close()

	provider := newKimiProviderForTest(config.ProviderConfig{
		Name:                "kimi",
		Type:                config.ProviderTypeKimi,
		Model:               "kimi-k2.x",
		ClientVersion:       "1.2.3",
		CredentialStorePath: store.Dir(),
	}, server.Client(), server.URL)
	provider.accounts.clock = func() time.Time { return now }
	refreshCalls := 0
	provider.refresh = func(_ context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
		refreshCalls++
		if credential.DeviceID != "kimi-device-123" || credential.RefreshToken != "keep-refresh" {
			t.Fatalf("refresh did not use account DeviceID: %+v", subscriptionauth.Summary(subscriptionauth.StoredCredential{Credential: credential}))
		}
		return subscriptionauth.TokenUpdate{
			AccessToken: "refreshed-access",
			ExpiresAt:   now.Add(time.Hour).Format(time.RFC3339Nano),
			DeviceID:    credential.DeviceID,
		}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{
		Model:           "kimi-k2.x",
		SystemPrompt:    "Be concise.",
		ReasoningEffort: "high",
		MaxOutputTokens: 64,
		Messages: []Message{{Role: "user", Blocks: []ContentBlock{
			{Type: "text", Text: "inspect"},
			{Type: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}},
		}}},
		Tools: []ToolSpec{{Name: "Read", Description: "Read a file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch *DispatchInfo
	var text string
	var calls []ToolCall
	var usage Usage
	var done bool
	for event := range events {
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
		switch event.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", event.Text)
		case "text":
			text += event.Text
		case "tool_call":
			if event.ToolCall != nil {
				calls = append(calls, *event.ToolCall)
			}
		case "usage":
			if event.Usage != nil {
				usage = *event.Usage
			}
		case "done":
			done = true
		}
	}
	if refreshCalls != 1 {
		t.Fatalf("expected one refresh, got %d", refreshCalls)
	}
	stored, err := store.GetByID(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "refreshed-access" || stored.RefreshToken != "keep-refresh" || stored.DeviceID != "kimi-device-123" {
		t.Fatalf("unexpected stored refreshed credential: %+v", subscriptionauth.Summary(stored))
	}
	if dispatch == nil || dispatch.CredentialID != account.ID || dispatch.Model != "kimi-k2.x" {
		t.Fatalf("unexpected dispatch attribution: %+v", dispatch)
	}
	if text != "hello" || len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "Read" || string(calls[0].Input) != `{"path":"README.md"}` {
		t.Fatalf("unexpected Kimi output text=%q calls=%+v", text, calls)
	}
	if usage != (Usage{InputTokens: 12, OutputTokens: 4, CachedInputTokens: 3, ReasoningTokens: 1}) || !done {
		t.Fatalf("unexpected usage/done: %+v done=%v", usage, done)
	}
	if requestBody["model"] != "kimi-k2.x" {
		t.Fatalf("Kimi model ID prefix was modified: %+v", requestBody["model"])
	}
	if requestBody["stream"] != true || requestBody["max_tokens"] != float64(64) || requestBody["reasoning_effort"] != "high" {
		t.Fatalf("unexpected Kimi request payload: %+v", requestBody)
	}
	messages, ok := requestBody["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("expected system and user/image messages: %+v", requestBody["messages"])
	}
	if tools, ok := requestBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("unexpected Kimi tools payload: %+v", requestBody["tools"])
	}
}

func TestKimiProviderFailureSwitchesBeforeOutputAndAttributesSecondAccount(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	first := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 10, accessToken: "first", deviceID: "device-first"})
	second := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 20, accessToken: "second", deviceID: "device-second"})
	var attempts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts = append(attempts, r.Header.Get("X-Msh-Device-Id"))
		if r.Header.Get("Authorization") == "Bearer first" {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		writeKimiTestStream(w)
	}))
	defer server.Close()
	provider := newKimiProviderForTest(config.ProviderConfig{Name: "kimi", Model: "kimi-k2.x", ClientVersion: "1.2.3", CredentialStorePath: store.Dir()}, server.Client(), server.URL)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch *DispatchInfo
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error: %s", event.Text)
		}
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
	}
	if strings.Join(attempts, ",") != "device-first,device-second" {
		t.Fatalf("unexpected Kimi account order: %+v", attempts)
	}
	if dispatch == nil || dispatch.CredentialID != second.ID || dispatch.CredentialID == first.ID {
		t.Fatalf("unexpected failover dispatch: %+v", dispatch)
	}
}

func TestKimiProviderListModelsContinuesAndMergesStaticModels(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 10, accessToken: "bad", deviceID: "device-bad"})
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 20, accessToken: "good", deviceID: "device-good"})
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected Kimi models path %s", r.URL.Path)
		}
		calls++
		if r.Header.Get("Authorization") == "Bearer bad" {
			http.Error(w, "temporary", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "kimi-dynamic"}, {"id": "kimi-static"}}})
	}))
	defer server.Close()
	provider := newKimiProviderForTest(config.ProviderConfig{
		Name:                "kimi",
		ClientVersion:       "1.2.3",
		CredentialStorePath: store.Dir(),
		Model:               "kimi-default",
		Models:              []config.ProviderModelConfig{{Name: "kimi-static"}, {Name: "kimi-extra"}},
	}, server.Client(), server.URL)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "kimi-dynamic,kimi-static,kimi-extra,kimi-default"
	if strings.Join(models, ",") != want || calls != 2 {
		t.Fatalf("unexpected Kimi models=%+v calls=%d", models, calls)
	}
}

func TestKimiProviderErrorsAreSanitized(t *testing.T) {
	const token = "kimi-secret-token"
	store := subscriptionauth.NewStore(t.TempDir())
	item := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderKimi, priority: 10, accessToken: token, deviceID: "device"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Bearer "+token+" https://private.example.test/path", http.StatusBadRequest)
	}))
	defer server.Close()
	provider := newKimiProviderForTest(config.ProviderConfig{Name: "kimi", CredentialStorePath: store.Dir(), Model: "kimi-k2.x", ClientVersion: "1.2.3"}, server.Client(), server.URL)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var dispatch *DispatchInfo
	var errorText string
	for event := range events {
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if dispatch == nil || dispatch.CredentialID != item.ID {
		t.Fatalf("failed request lost dispatch attribution: %+v", dispatch)
	}
	if !strings.Contains(errorText, "400") {
		t.Fatalf("unexpected sanitized error %q", errorText)
	}
	for _, secret := range []string{token, "Bearer", "private.example.test", server.URL} {
		if strings.Contains(errorText, secret) {
			t.Fatalf("Kimi error leaked %q in %q", secret, errorText)
		}
	}
}

func TestKimiProviderCapabilitiesAndProductionEndpointRestriction(t *testing.T) {
	provider := NewKimiProvider(config.ProviderConfig{BaseURL: "https://api.moonshot.cn/v1"})
	if provider.configErr == nil {
		t.Fatal("production Kimi provider must reject non-coding Base URLs")
	}
	capabilities := canonicalCapabilities((&KimiProvider{}).Capabilities())
	if !capabilities.Tools || !capabilities.Streaming || !capabilities.ImageInput || strings.Join(capabilities.ReasoningEfforts, ",") != "low,high" {
		t.Fatalf("unexpected Kimi capabilities: %+v", capabilities)
	}
	if capabilities.SupportsReasoningEffort("medium") {
		t.Fatal("Kimi provider must expose only low/high reasoning effort")
	}
}

func writeKimiTestStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"README"}}]}}]}`,
		``,
		`data: {"choices":[{"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":0,"function":{"arguments":".md\"}"}}]}}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":1}}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n") + "\n\n"))
}
