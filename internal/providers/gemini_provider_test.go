package providers

import (
	"context"
	"encoding/json"
	"fmt"
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
		// Inline image output on a normal chat model. A model whose name
		// contains "image" would instead take the dedicated image-generation
		// branch, which deliberately drops systemInstruction, tools and
		// enabledCreditTypes — the opposite of what this test asserts.
		EnableImageGeneration: true,
	}, "gemini-3-flash", "project-1", "high")
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, required := range []string{`"project":"project-1"`, `"model":"gemini-3-flash"`, `"enabledCreditTypes":["GOOGLE_ONE_AI"]`, `"systemInstruction"`, `"inlineData"`, `"functionCall"`, `"thoughtSignature":"sig-1"`, `"functionResponse"`, `"parametersJsonSchema"`, `"thinkingLevel":"high"`, `"responseModalities":["TEXT","IMAGE"]`} {
		if !strings.Contains(text, required) {
			t.Fatalf("payload missing %s: %s", required, text)
		}
	}
	// Gemini models must NOT send additionalModelRequestFields
	if strings.Contains(text, "additionalModelRequestFields") {
		t.Fatalf("Gemini model payload must not contain additionalModelRequestFields: %s", text)
	}
}

// Claude reasoning on Cloud Code goes through generationConfig.thinkingConfig,
// same container as Gemini, but keyed by thinkingBudget rather than thinkingLevel.
// Every assertion here was taken from live endpoint responses:
//
//	additionalModelRequestFields (top level)  -> 400 Unknown name
//	additionalModelRequestFields (in request) -> 400 Unknown name
//	outputConfig                              -> 400 Unknown name
//	thinkingConfig.thinkingLevel              -> 200 but no thoughts, tokens unchanged
//	thinkingConfig.thinkingBudget             -> 200 with thoughts present
//	budget == max_tokens                      -> 400 max_tokens must be greater
func TestBuildGeminiCloudCodePayloadClaudeModel(t *testing.T) {
	payload := buildGeminiCloudCodePayload(GenerateRequest{
		SystemPrompt:    "Be concise.",
		Messages:        []Message{{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "hello"}}}},
		MaxOutputTokens: 32000,
	}, "claude-opus-4-6-thinking", "project-1", "high")
	encoded, _ := json.Marshal(payload)
	text := string(encoded)

	if !strings.Contains(text, `"thinkingBudget":8192`) {
		t.Fatalf("Claude payload missing thinkingBudget for high effort: %s", text)
	}
	if !strings.Contains(text, `"includeThoughts":true`) {
		t.Fatalf("Claude payload missing includeThoughts: %s", text)
	}
	// thinkingLevel is a Gemini enum; Claude accepts the key but ignores it, so
	// sending it would silently produce a reasoning-free response.
	if strings.Contains(text, "thinkingLevel") {
		t.Fatalf("Claude payload must not use thinkingLevel: %s", text)
	}
	// The field does not exist in this API at any nesting level.
	if strings.Contains(text, "additionalModelRequestFields") || strings.Contains(text, "outputConfig") {
		t.Fatalf("Claude payload must not contain rejected reasoning fields: %s", text)
	}
	// "-thinking" is the real model id on Cloud Code and echoes back as
	// modelVersion. Stripping it produced 404 NOT_FOUND on every opus call.
	if !strings.Contains(text, `"model":"claude-opus-4-6-thinking"`) {
		t.Fatalf("Claude model id must be sent verbatim: %s", text)
	}

	// No reasoning effort: no thinking fields at all.
	payloadNoReason := buildGeminiCloudCodePayload(GenerateRequest{
		Messages: []Message{{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "hello"}}}},
	}, "claude-opus-4-6-thinking", "project-1", "")
	textNoReason := string(mustMarshalProbe(t, payloadNoReason))
	if strings.Contains(textNoReason, "thinkingConfig") || strings.Contains(textNoReason, "additionalModelRequestFields") {
		t.Fatalf("No-reasoning payload must not contain thinking fields: %s", textNoReason)
	}
}

// Budgets must stay at least 1024 below max_tokens; Anthropic returns 400 when
// budget >= max_tokens, and a request with no thoughts beats a rejected one.
func TestGeminiClaudeThinkingBudgetRespectsMaxTokens(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		effort          string
		maxOutputTokens int64
		want            int64
	}{
		{"high with no max uses default", "high", 0, 8192},
		{"medium with no max uses default", "medium", 0, 4096},
		{"low with no max uses default", "low", 0, 2048},
		{"high clamped to leave headroom", "high", 4096, 3072},
		{"medium clamped to leave headroom", "medium", 3072, 2048},
		// max=2048 clamps to exactly the 1024 minimum, which the endpoint
		// accepts: verified HTTP 200 with thoughts present.
		{"max clamps down to the minimum", "high", 2048, 1024},
		{"max below minimum budget", "high", 1024, 0},
		{"unknown effort sends nothing", "sideways", 32000, 0},
		{"empty effort sends nothing", "", 32000, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got := geminiClaudeThinkingBudget(testCase.effort, testCase.maxOutputTokens)
			if got != testCase.want {
				t.Fatalf("budget for effort=%q max=%d: got %d, want %d",
					testCase.effort, testCase.maxOutputTokens, got, testCase.want)
			}
			if testCase.maxOutputTokens > 0 && got > 0 && got >= testCase.maxOutputTokens {
				t.Fatalf("budget %d must stay below max_tokens %d", got, testCase.maxOutputTokens)
			}
		})
	}
}

func mustMarshalProbe(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
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
			// This fixture streams a functionCall with finishReason STOP, which is
			// exactly what Cloud Code sends for a tool turn. Asserting "stop" here
			// is what kept the bug green: the runner rejects a plain stop alongside
			// tool calls and aborted the whole run.
			done = event.Done && event.StopReason == "tool_use"
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
	var requestedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:fetchAvailableModels" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&requestedBody)
		// models is a protobuf map, so the model id is the JSON key.
		_ = json.NewEncoder(w).Encode(map[string]any{"models": map[string]any{
			"gemini-live-model": map[string]any{"displayName": "Gemini Live", "quotaInfo": map[string]any{"remainingFraction": 0.5, "resetTime": "2026-08-01T00:00:00Z"}},
		}})
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
	// The project id is what makes the quota figures account-specific; without it
	// Google answers 200 but reports full quota for an exhausted account.
	if requestedBody["project"] != "project-models" {
		t.Fatalf("fetchAvailableModels must carry the project id, got %v", requestedBody)
	}
}

// Model ids reach Cloud Code verbatim. "claude-opus-4-6-thinking" is a real
// model on this endpoint, not a flag: it answers 200 and echoes
// modelVersion=claude-opus-4-6-thinking. An earlier revision treated the suffix
// as a request to enable reasoning and stripped it, which turned a working id
// into one the endpoint does not have — "claude-opus-4-6" answers 404
// NOT_FOUND, as do claude-opus-4-5 and claude-opus-4-1. Reasoning is controlled
// by thinkingBudget, never by the model name.
func TestGeminiProviderSendsModelIDVerbatim(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-6-thinking",
		"claude-sonnet-4-6",
		"gemini-3.1-thinking",
		"gemini-3-flash",
	} {
		payload := buildGeminiCloudCodePayload(GenerateRequest{
			Messages:        []Message{{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "hello"}}}},
			MaxOutputTokens: 32000,
		}, model, "project-1", "high")
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		if !strings.Contains(text, `"model":"`+model+`"`) {
			t.Fatalf("model id must be sent verbatim, want %q, got: %s", model, text)
		}
	}
}

// TestGeminiCloudCodeClaudeToolSchemaField locks the field name that decides
// whether Claude works on Cloud Code at all. "parametersJsonSchema" is dropped
// when the request is translated to Anthropic's tool format, and the backend
// answers "tools.0.custom.input_schema: Field required" (HTTP 400) — which,
// since every agent turn ships tools, broke Claude models outright.
func TestGeminiCloudCodeClaudeToolSchemaField(t *testing.T) {
	specs := []ToolSpec{{Name: "Bash", Description: "Run", Schema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"command": map[string]any{"type": "string"}},
	}}}

	claude := geminiCloudCodeTools(specs, true)
	if len(claude) != 1 {
		t.Fatalf("unexpected declarations: %+v", claude)
	}
	if _, ok := claude[0]["parameters"]; !ok {
		t.Fatalf("Claude declaration must carry parameters: %+v", claude[0])
	}
	if _, ok := claude[0]["parametersJsonSchema"]; ok {
		t.Fatalf("Claude declaration must not carry parametersJsonSchema: %+v", claude[0])
	}

	gemini := geminiCloudCodeTools(specs, false)
	if _, ok := gemini[0]["parametersJsonSchema"]; !ok {
		t.Fatalf("Gemini declaration must keep parametersJsonSchema: %+v", gemini[0])
	}
	if _, ok := gemini[0]["parameters"]; ok {
		t.Fatalf("Gemini declaration must not carry parameters: %+v", gemini[0])
	}

	// The schema itself must be identical either way: only the field differs.
	if fmt.Sprint(claude[0]["parameters"]) != fmt.Sprint(gemini[0]["parametersJsonSchema"]) {
		t.Fatalf("sanitized schema diverged between backends: %+v vs %+v", claude[0], gemini[0])
	}
}

// TestGeminiCloudCodeClaudePayloadUsesParameters covers the wiring, not just the
// helper: the Claude branch is selected from the model name.
func TestGeminiCloudCodeClaudePayloadUsesParameters(t *testing.T) {
	request := GenerateRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
		Tools:    []ToolSpec{{Name: "Bash", Schema: map[string]any{"type": "object"}}},
	}
	for model, wantField := range map[string]string{
		"claude-sonnet-4-6": "parameters",
		"gemini-3-flash":    "parametersJsonSchema",
	} {
		payload := buildGeminiCloudCodePayload(request, model, "project-1", "")
		inner, _ := payload["request"].(map[string]any)
		tools, _ := inner["tools"].([]map[string]any)
		if len(tools) != 1 {
			t.Fatalf("%s: unexpected tools: %+v", model, inner["tools"])
		}
		declarations, _ := tools[0]["functionDeclarations"].([]map[string]any)
		if len(declarations) != 1 {
			t.Fatalf("%s: unexpected declarations: %+v", model, tools[0])
		}
		if _, ok := declarations[0][wantField]; !ok {
			t.Fatalf("%s: declaration missing %q: %+v", model, wantField, declarations[0])
		}
	}
}
