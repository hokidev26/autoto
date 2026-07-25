package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"autoto/internal/config"
	"autoto/internal/subscriptionauth"
)

func createGeminiProviderTestAccount(t *testing.T, store *subscriptionauth.Store, access, subject, project string, priority int, expiresAt time.Time) subscriptionauth.StoredCredential {
	t.Helper()
	item, err := store.CreateOAuth(subscriptionauth.CreateRequest{
		Provider:      subscriptionauth.ProviderGemini,
		Priority:      priority,
		AccessToken:   access,
		RefreshToken:  "refresh-" + access,
		TokenType:     "Bearer",
		ExpiresAt:     expiresAt.UTC().Format(time.RFC3339Nano),
		Email:         subject + "@example.test",
		Subject:       subject,
		Scope:         "openid cloud-platform",
		ProjectID:     project,
		TokenEndpoint: "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func collectGeminiProviderEvents(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var result []Event
	for event := range events {
		result = append(result, event)
	}
	return result
}

func TestGeminiProviderRejectsNonOfficialProductionBaseURL(t *testing.T) {
	cfg := config.ProviderConfig{Name: "gemini", Type: config.ProviderTypeGemini, BaseURL: "https://example.com", CredentialStorePath: t.TempDir()}
	provider := NewGeminiProvider(cfg)
	if provider.Configured() || provider.configErr == nil || !strings.Contains(provider.configErr.Error(), "official HTTPS Cloud Code") {
		t.Fatalf("unexpected production validation result: configured=%v err=%v", provider.Configured(), provider.configErr)
	}
	if err := validateGeminiProductionBaseURL(geminiCloudCodeProductionBaseURL); err != nil {
		t.Fatal(err)
	}
}

func TestBuildGeminiCloudCodePayload(t *testing.T) {
	payload := buildGeminiCloudCodePayload(GenerateRequest{
		SystemPrompt: "Be concise.",
		Messages: []Message{
			{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "hello"}, {Type: "image", MIMEType: "image/png", Data: []byte("image")}}},
			{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", ToolUseID: "call-1", ToolName: "lookup", Input: json.RawMessage(`{"q":"x"}`), ProviderState: json.RawMessage(`{"thought_signature":"sig-1"}`)}}},
			{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "call-1", ToolName: "lookup", Output: `{"ok":true}`}}},
		},
		Tools:           []ToolSpec{{Name: "lookup", Description: "Lookup", Schema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}},
		MaxOutputTokens: 128,
	}, "gemini-3.1-flash-image", "project-1", "high")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"project":"project-1"`, `"model":"gemini-3.1-flash-image"`, `"enabledCreditTypes":["GOOGLE_ONE_AI"]`, `"systemInstruction"`, `"inlineData"`, `"functionCall"`, `"thoughtSignature":"sig-1"`, `"functionResponse"`, `"parametersJsonSchema"`, `"thinkingLevel":"high"`, `"responseModalities":["TEXT","IMAGE"]`} {
		if !strings.Contains(text, required) {
			t.Fatalf("payload missing %s: %s", required, text)
		}
	}
}

func TestGeminiProviderFailoverDispatchAndStreaming(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemini")
	store := subscriptionauth.NewStore(dir)
	now := time.Now()
	first := createGeminiProviderTestAccount(t, store, "token-first", "first", "project-first", 1, now.Add(time.Hour))
	second := createGeminiProviderTestAccount(t, store, "token-second", "second", "project-second", 2, now.Add(time.Hour))
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Fatalf("unexpected request target: %s", r.URL.String())
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("missing Cloud Code headers: %v", r.Header)
		}
		switch r.Header.Get("Authorization") {
		case "Bearer token-first":
			calls.Add(1)
			w.WriteHeader(http.StatusTooManyRequests)
			return
		case "Bearer token-second":
			calls.Add(1)
		default:
			t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["project"] != "project-second" || body["model"] != "gemini-3-flash" {
			t.Fatalf("unexpected Cloud Code body: %v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"},{\"thoughtSignature\":\"sig-live\",\"functionCall\":{\"id\":\"call-live\",\"name\":\"lookup\",\"args\":{\"q\":\"x\"}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"cachedContentTokenCount\":1,\"thoughtsTokenCount\":4}}}\n\n"))
	}))
	defer server.Close()

	provider := newGeminiProviderForTest(config.ProviderConfig{Name: "gemini", Type: config.ProviderTypeGemini, Model: "gemini-3-flash", Models: []config.ProviderModelConfig{{Name: "gemini-3-flash", ContextTokenLimit: 1048576}}, CredentialStorePath: dir}, server.Client(), server.URL)
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := collectGeminiProviderEvents(t, events)
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
	var dispatch *DispatchInfo
	var text string
	var tool *ToolCall
	var usage *Usage
	var done bool
	for _, event := range got {
		switch event.Type {
		case "dispatch":
			dispatch = event.Dispatch
		case "text":
			text += event.Text
		case "tool_call":
			tool = event.ToolCall
		case "usage":
			usage = event.Usage
		case "done":
			done = event.Done && event.StopReason == "stop"
		}
	}
	if dispatch == nil || dispatch.CredentialID != second.ID || dispatch.CredentialID == first.ID {
		t.Fatalf("unexpected dispatch: %+v", dispatch)
	}
	if text != "hello" || tool == nil || tool.ID != "call-live" || tool.Name != "lookup" || !strings.Contains(string(tool.ProviderState), "sig-live") {
		t.Fatalf("unexpected content events: text=%q tool=%+v", text, tool)
	}
	if usage == nil || usage.InputTokens != 3 || usage.OutputTokens != 2 || usage.CachedInputTokens != 1 || usage.ReasoningTokens != 4 || !done {
		t.Fatalf("unexpected terminal events: usage=%+v done=%v events=%+v", usage, done, got)
	}
}

func TestGeminiProviderRefreshPreservesRefreshToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemini")
	store := subscriptionauth.NewStore(dir)
	account := createGeminiProviderTestAccount(t, store, "expired-access", "refresh", "project-refresh", 1, time.Now().Add(-time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer refreshed-access" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()
	provider := newGeminiProviderForTest(config.ProviderConfig{Name: "gemini", Type: config.ProviderTypeGemini, Model: "gemini-3-flash", CredentialStorePath: dir}, server.Client(), server.URL)
	provider.refresh = func(context.Context, subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
		return subscriptionauth.TokenUpdate{AccessToken: "refreshed-access", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}, nil
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	updated, err := store.GetByID(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "refreshed-access" || updated.RefreshToken != account.RefreshToken || updated.ProjectID != account.ProjectID {
		t.Fatalf("refresh persistence mismatch: %+v", updated.Credential)
	}
}

func TestGeminiProviderResolvesAndPersistsProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemini")
	store := subscriptionauth.NewStore(dir)
	account := createGeminiProviderTestAccount(t, store, "access-project", "project", "", 1, time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()
	provider := newGeminiProviderForTest(config.ProviderConfig{Name: "gemini", Type: config.ProviderTypeGemini, Model: "gemini-3-flash", CredentialStorePath: dir}, server.Client(), server.URL)
	provider.fetchProject = func(context.Context, string) (string, error) { return "project-resolved", nil }
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	updated, err := store.GetByID(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ProjectID != "project-resolved" {
		t.Fatalf("project ID was not persisted: %+v", updated.Credential)
	}
}

func TestGeminiProviderListModelsMergesLiveAndStatic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemini")
	store := subscriptionauth.NewStore(dir)
	_ = createGeminiProviderTestAccount(t, store, "access-models", "models", "project-models", 1, time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:loadCodeAssist" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"availableModels": []any{map[string]any{"id": "gemini-live-model"}}})
	}))
	defer server.Close()
	provider := newGeminiProviderForTest(config.ProviderConfig{Name: "gemini", Type: config.ProviderTypeGemini, Model: "gemini-static", Models: []config.ProviderModelConfig{{Name: "gemini-static", ContextTokenLimit: 1000}}, CredentialStorePath: dir}, server.Client(), server.URL)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(models, ",")
	if !strings.Contains(joined, "gemini-live-model") || !strings.Contains(joined, "gemini-static") {
		t.Fatalf("live/static models were not merged: %v", models)
	}
}
