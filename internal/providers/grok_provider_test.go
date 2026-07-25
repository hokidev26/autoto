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

func TestGrokProviderResponsesHeadersFailoverAndDispatch(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	first := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "first-token"})
	second := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 20, accessToken: "second-token"})
	var requestBody map[string]any
	var attempts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Fatalf("unexpected Grok request %s %s", r.Method, r.URL.Path)
		}
		authorization := r.Header.Get("Authorization")
		attempts = append(attempts, authorization)
		if authorization == "Bearer first-token" {
			http.Error(w, "do not expose first-token or https://private.example.test", http.StatusTooManyRequests)
			return
		}
		if authorization != "Bearer second-token" {
			t.Fatalf("unexpected authorization header %q", authorization)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("unexpected Accept header %q", got)
		}
		if got := r.Header.Get("X-XAI-Token-Auth"); got != "xai-grok-cli" {
			t.Fatalf("unexpected token auth header %q", got)
		}
		if got := r.Header.Get("x-grok-client-version"); got != grokClientVersion {
			t.Fatalf("unexpected Grok client version %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "xai-grok-workspace/"+grokClientVersion {
			t.Fatalf("unexpected User-Agent %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writeGrokTestStream(w)
	}))
	defer server.Close()

	provider := newGrokProviderForTest(config.ProviderConfig{
		Name:                "grok",
		Type:                config.ProviderTypeGrok,
		Model:               "grok-4.5",
		CredentialStorePath: store.Dir(),
	}, server.Client(), server.URL)
	telemetry := &subscriptionRecordingTelemetry{}
	provider.SetAccountTelemetry(telemetry)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{
		SystemPrompt:    "Be concise.",
		ReasoningEffort: "high",
		MaxOutputTokens: 128,
		Messages: []Message{
			{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "inspect"}, {Type: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}}}},
			{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", ToolUseID: "old-call", ToolName: "Read", Input: json.RawMessage(`{"path":"old"}`)}}},
			{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "old-call", Output: "old result"}}},
		},
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
	if len(attempts) != 2 || attempts[0] != "Bearer first-token" || attempts[1] != "Bearer second-token" {
		t.Fatalf("unexpected account attempt order: %+v", attempts)
	}
	if dispatch == nil || dispatch.CredentialID != second.ID || dispatch.CredentialID == first.ID || dispatch.Model != "grok-4.5" {
		t.Fatalf("unexpected dispatch attribution: %+v", dispatch)
	}
	if text != "hello" || len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "Read" || string(calls[0].Input) != `{"path":"README.md"}` {
		t.Fatalf("unexpected Grok output text=%q calls=%+v", text, calls)
	}
	if usage != (Usage{InputTokens: 12, OutputTokens: 4, CachedInputTokens: 3, ReasoningTokens: 1}) || !done {
		t.Fatalf("unexpected usage/done: %+v done=%v", usage, done)
	}
	if requestBody["model"] != "grok-4.5" || requestBody["stream"] != true || requestBody["instructions"] != "Be concise." || requestBody["max_output_tokens"] != float64(128) {
		t.Fatalf("unexpected Responses payload: %+v", requestBody)
	}
	reasoning, _ := requestBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("unexpected reasoning payload: %+v", requestBody["reasoning"])
	}
	if _, ok := requestBody["input"].([]any); !ok {
		t.Fatalf("images and tool history must use structured Responses input: %+v", requestBody["input"])
	}
	if tools, ok := requestBody["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("unexpected tools payload: %+v", requestBody["tools"])
	}
	attemptRecords := telemetry.snapshot()
	if len(attemptRecords) != 2 || attemptRecords[0].Success || !attemptRecords[1].Success || attemptRecords[0].AccountID != first.ID || attemptRecords[1].AccountID != second.ID {
		t.Fatalf("unexpected account telemetry: %+v", attemptRecords)
	}
}

func TestGrokProviderListModelsContinuesAndMergesStaticModels(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "bad-token"})
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 20, accessToken: "good-token"})
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected models path %s", r.URL.Path)
		}
		calls++
		if r.Header.Get("Authorization") == "Bearer bad-token" {
			http.Error(w, "unsupported by this account", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "grok-dynamic"}, {"id": "grok-static"}}})
	}))
	defer server.Close()
	provider := newGrokProviderForTest(config.ProviderConfig{
		Name:                "grok",
		CredentialStorePath: store.Dir(),
		Model:               "grok-default",
		Models:              []config.ProviderModelConfig{{Name: "grok-static"}, {Name: "grok-extra"}},
	}, server.Client(), server.URL)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"grok-dynamic", "grok-static", "grok-extra", "grok-default"}
	if strings.Join(models, ",") != strings.Join(want, ",") || calls != 2 {
		t.Fatalf("unexpected merged models=%+v calls=%d", models, calls)
	}
}

func TestGrokProviderListModelsFallsBackToStaticCatalog(t *testing.T) {
	store := subscriptionauth.NewStore(t.TempDir())
	createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: "token"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "proxy has no models route", http.StatusNotFound)
	}))
	defer server.Close()
	provider := newGrokProviderForTest(config.ProviderConfig{
		CredentialStorePath: store.Dir(),
		Model:               "grok-default",
		Models:              []config.ProviderModelConfig{{Name: "grok-static"}},
	}, server.Client(), server.URL)
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "grok-static,grok-default" {
		t.Fatalf("unexpected static fallback: %+v", models)
	}
}

func TestGrokProviderErrorsAreSanitized(t *testing.T) {
	const token = "grok-secret-token"
	store := subscriptionauth.NewStore(t.TempDir())
	item := createSubscriptionTestAccount(t, store, subscriptionTestAccountParams{provider: subscriptionauth.ProviderGrok, priority: 10, accessToken: token})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Bearer "+token+" https://private.example.test/path", http.StatusBadRequest)
	}))
	defer server.Close()
	provider := newGrokProviderForTest(config.ProviderConfig{Name: "grok", CredentialStorePath: store.Dir(), Model: "grok-4.5"}, server.Client(), server.URL)
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
			t.Fatalf("Grok error leaked %q in %q", secret, errorText)
		}
	}
}

func TestGrokProviderCapabilitiesAndProductionEndpointRestriction(t *testing.T) {
	provider := NewGrokProvider(config.ProviderConfig{BaseURL: "https://api.x.ai/v1"})
	if provider.configErr == nil {
		t.Fatal("production Grok provider must reject non-CLI-proxy Base URLs")
	}
	capabilities := canonicalCapabilities((&GrokProvider{}).Capabilities())
	if !capabilities.Tools || !capabilities.Streaming || !capabilities.ImageInput || strings.Join(capabilities.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("unexpected Grok capabilities: %+v", capabilities)
	}
}

func writeGrokTestStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte(strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"path\":\"README.md\"}"}}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"output":[{"type":"function_call","call_id":"call_1","name":"Read","arguments":"{\"path\":\"README.md\"}"}],"usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}}`,
		``,
	}, "\n") + "\n\n"))
}
