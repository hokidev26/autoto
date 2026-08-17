package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
)

func TestOpenAICompatibleImageInputRequiresExplicitOptIn(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  config.ProviderConfig
		want bool
	}{
		{name: "generic default", cfg: config.ProviderConfig{Name: "relay", Type: "openai-compatible"}},
		{name: "CLI proxy default", cfg: config.ProviderConfig{Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI}},
		{name: "explicit opt in", cfg: config.ProviderConfig{Name: "vision-relay", Type: "openai-compatible", ImageInput: true}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CapabilitiesFor(NewOpenAICompatible(test.cfg)).ImageInput; got != test.want {
				t.Fatalf("ImageInput = %v, want %v", got, test.want)
			}
		})
	}
}

func TestOpenAICompatibleRejectsImageBlocksWithoutExplicitOptIn(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{
		Name:           "text-only-relay",
		Type:           "openai-compatible",
		BaseURL:        server.URL,
		Model:          "text-only",
		APIKeyOptional: true,
	})
	if _, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{
		Role:   "user",
		Blocks: []ContentBlock{{Type: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}}},
	}}}); err == nil || !strings.Contains(err.Error(), "does not support image input") {
		t.Fatalf("expected fail-closed image capability error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("unsupported image input reached upstream %d times", requests)
	}
}

func TestOpenAICompatibleListModelsAllowsOptionalAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("expected no authorization header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "gpt-a"}, {"id": "gpt-b"}},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		BaseURL:        server.URL,
		Model:          "gpt-5.5",
		APIKeyOptional: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	models, err := provider.ListModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "gpt-a" || models[1] != "gpt-b" {
		t.Fatalf("unexpected models: %+v", models)
	}
}

func TestOpenAICompatibleSendsImageBlocks(t *testing.T) {
	var requestBody struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "ok"},
			}},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		BaseURL:        server.URL,
		Model:          "gpt-5.5",
		APIKeyOptional: true,
		ImageInput:     true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{
		Role: "user",
		Blocks: []ContentBlock{
			{Type: "text", Text: "看这张图"},
			{Type: "image", MIMEType: "image/png", Data: []byte{1, 2, 3}, Filename: "a.png"},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error: %s", event.Text)
		}
	}
	if len(requestBody.Messages) != 1 || len(requestBody.Messages[0].Content) != 2 {
		t.Fatalf("unexpected messages payload: %+v", requestBody.Messages)
	}
	if requestBody.Messages[0].Content[0].Type != "text" || requestBody.Messages[0].Content[0].Text != "看这张图" {
		t.Fatalf("expected text block, got %+v", requestBody.Messages[0].Content[0])
	}
	imageURL := requestBody.Messages[0].Content[1].ImageURL.URL
	if requestBody.Messages[0].Content[1].Type != "image_url" || !strings.HasPrefix(imageURL, "data:image/png;base64,") {
		t.Fatalf("expected image_url data URL, got %+v", requestBody.Messages[0].Content[1])
	}
}

func TestOpenAICompatibleAllowsOptionalAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("expected no authorization header, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "ok"},
			}},
			"usage": map[string]any{
				"prompt_tokens":             12,
				"completion_tokens":         4,
				"prompt_tokens_details":     map[string]any{"cached_tokens": 3},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 1},
			},
		})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		BaseURL:        server.URL,
		Model:          "gpt-5.5",
		APIKeyOptional: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var usage Usage
	var dispatch *DispatchInfo
	for event := range events {
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
		if event.Type == "usage" && event.Usage != nil {
			usage = *event.Usage
		}
		if event.Type == "text" {
			text += event.Text
		}
	}
	if text != "ok" {
		t.Fatalf("expected ok response, got %q", text)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 4 || usage.CachedInputTokens != 3 || usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if dispatch == nil || dispatch.Provider != "cliproxyapi" || dispatch.Model != "gpt-5.5" || dispatch.CredentialID != configuredCredentialID {
		t.Fatalf("unexpected dispatch attribution: %+v", dispatch)
	}
}

func TestOpenAICompatibleAttributesConfiguredCredentialOnHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer configured-key" {
			t.Fatalf("unexpected authorization header: %q", got)
		}
		http.Error(w, "upstream unavailable: configured-key Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.signature https://private.example.test/path", http.StatusBadGateway)
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: server.URL, APIKey: "configured-key", Model: "gpt-test"})
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
	if dispatch == nil || dispatch.Provider != "relay" || dispatch.Model != "gpt-test" || dispatch.CredentialID != configuredCredentialID {
		t.Fatalf("failed request lost credential attribution: %+v", dispatch)
	}
	if !strings.Contains(errorText, "502") {
		t.Fatalf("unexpected error event: %q", errorText)
	}
	for _, secret := range []string{"configured-key", "Bearer", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.signature", "private.example.test", server.URL} {
		if strings.Contains(errorText, secret) {
			t.Fatalf("HTTP error leaked %q in %q", secret, errorText)
		}
	}
}

func TestOpenAICompatibleStreamsTextAndToolCalls(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"hel"}}]}`,
			``,
			`data: {"choices":[{"delta":{"content":"lo"}}]}`,
			``,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"path\":\"README"}}]}}]}`,
			``,
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":".md\"}"}}]}}]}`,
			``,
			`data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":1}}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{Name: "cliproxyapi", Type: "openai-compatible", BaseURL: server.URL, Model: "gpt-5.5", APIKeyOptional: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{
		Messages:        []Message{{Role: "user", Content: "read README"}},
		Tools:           []ToolSpec{{Name: "Read", Description: "Read a file", Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var calls []ToolCall
	var usage Usage
	for event := range events {
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
		}
	}
	if text != "hello" {
		t.Fatalf("expected streamed text hello, got %q", text)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "Read" || string(calls[0].Input) != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool calls: %+v", calls)
	}
	if usage != (Usage{InputTokens: 12, OutputTokens: 4, CachedInputTokens: 3, ReasoningTokens: 1}) {
		t.Fatalf("usage-only final chunk was not parsed: %+v", usage)
	}
	if requestBody["stream"] != true || requestBody["max_tokens"] != float64(64) {
		t.Fatalf("expected stream and max output tokens, got %+v", requestBody)
	}
	streamOptions, ok := requestBody["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("expected stream_options.include_usage=true, got %+v", requestBody["stream_options"])
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected tools payload, got %+v", requestBody["tools"])
	}
}

func TestOpenAICompatibleSerializesToolHistory(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{Name: "cliproxyapi", Type: "openai-compatible", BaseURL: server.URL, Model: "gpt-5.5", APIKeyOptional: true})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{
		Messages: []Message{
			{Role: "assistant", Blocks: []ContentBlock{{Type: "tool_use", ToolUseID: "call_1", ToolName: "Read", Input: json.RawMessage(`{"path":"README.md"}`)}}},
			{Role: "user", Blocks: []ContentBlock{{Type: "tool_result", ToolUseID: "call_1", ToolName: "Read", Output: "file contents"}}},
		},
		Tools: []ToolSpec{{Name: "Read", Schema: map[string]any{"type": "object", "properties": map[string]any{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
	}
	messages, ok := requestBody["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("expected assistant tool call and tool result messages, got %+v", requestBody["messages"])
	}
	assistant, _ := messages[0].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("expected assistant tool call message, got %+v", assistant)
	}
	toolCalls, ok := assistant["tool_calls"].([]any)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("expected one assistant tool call, got %+v", assistant["tool_calls"])
	}
	call, _ := toolCalls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if call["id"] != "call_1" || function["name"] != "Read" || function["arguments"] != `{"path":"README.md"}` {
		t.Fatalf("unexpected assistant tool call payload: %+v", call)
	}
	toolResult, _ := messages[1].(map[string]any)
	if toolResult["role"] != "tool" || toolResult["tool_call_id"] != "call_1" || toolResult["content"] != "file contents" {
		t.Fatalf("unexpected tool result message: %+v", toolResult)
	}
}

func TestOpenAICompatibleCLIProxySendsReasoningAndAutotoIdentity(t *testing.T) {
	const installationID = "123e4567-e89b-42d3-a456-426614174000"
	var requestBody map[string]any
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("X-Autoto-Client"); got != "autoto/1.2.3" {
			t.Fatalf("unexpected Autoto client header %q", got)
		}
		if got := r.Header.Get("X-Autoto-Installation-ID"); got != installationID {
			t.Fatalf("unexpected installation header %q", got)
		}
		if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "codex") || strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "chatgpt") {
			t.Fatalf("client must not impersonate Codex or ChatGPT: %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "gpt-5"}}})
		case "/chat/completions":
			if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewOpenAICompatible(config.ProviderConfig{
		Name:           "cliproxyapi",
		Type:           "openai-compatible",
		Profile:        config.ProviderProfileCLIProxyAPI,
		BaseURL:        server.URL,
		Model:          "gpt-5",
		APIKeyOptional: true,
		ClientVersion:  "1.2.3",
		InstallationID: installationID,
	})
	if !CapabilitiesFor(provider).ReasoningEffort {
		t.Fatal("CLIProxyAPI profile must declare reasoning effort support")
	}
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Messages:        []Message{{Role: "user", Content: "think"}},
		ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
	}
	if len(paths) != 2 || paths[0] != "/models" || paths[1] != "/chat/completions" {
		t.Fatalf("unexpected request paths: %+v", paths)
	}
	if requestBody["reasoning_effort"] != "medium" {
		t.Fatalf("unexpected compatible reasoning payload: %+v", requestBody)
	}
}

func TestOpenAICompatibleCLIProxyRejectsGatewayScenario(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI,
		BaseURL: server.URL, Model: "gpt-5", APIKeyOptional: true,
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{Scenario: CallScenarioGateway})
	if events != nil || !errors.Is(err, ErrGatewayOAuthUnsupported) {
		t.Fatalf("expected OAuth proxy Gateway rejection, events=%v err=%v", events, err)
	}
	if requests != 0 {
		t.Fatalf("Gateway request reached OAuth proxy upstream %d times", requests)
	}
}

func TestOpenAICompatibleCLIProxyRejectsXHigh(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI,
		BaseURL: server.URL, Model: "gpt-5", APIKeyOptional: true,
	})
	if got := CapabilitiesFor(provider); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("unexpected CLIProxyAPI reasoning capabilities: %+v", got)
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{ReasoningEffort: "xhigh"})
	if err == nil || !errors.Is(err, ErrReasoningEffortUnsupported) || !strings.Contains(err.Error(), "xhigh") {
		t.Fatalf("expected xhigh to be rejected before dispatch, events=%v err=%v", events, err)
	}
	if requests != 0 {
		t.Fatalf("unsupported xhigh reached upstream %d times", requests)
	}
}

func TestOpenAICompatibleAutoOmitsReasoning(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "cliproxyapi", Type: "openai-compatible", Profile: config.ProviderProfileCLIProxyAPI,
		BaseURL: server.URL, Model: "gpt-5", APIKeyOptional: true,
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "think"}}, ReasoningEffort: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}
	if _, exists := requestBody["reasoning_effort"]; exists {
		t.Fatalf("auto reasoning must be omitted: %+v", requestBody)
	}
}

func TestOpenAICompatibleSendsReasoningWithoutCLIProxyProfile(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: server.URL, APIKeyOptional: true})
	if got := CapabilitiesFor(provider); !got.ReasoningEffort || strings.Join(got.ReasoningEfforts, ",") != "low,medium,high" {
		t.Fatalf("ordinary compatible provider should declare standard reasoning efforts, got %+v", got)
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "think"}}, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
	}
	if requestBody["reasoning_effort"] != "high" {
		t.Fatalf("unexpected compatible reasoning payload: %+v", requestBody)
	}
}

func TestOpenAICompatibleWithoutAPIKeyReturnsUnavailableError(t *testing.T) {
	provider := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", Model: "model"})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err == nil || !errors.Is(err, ErrProviderUnavailable) || !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
		t.Fatalf("expected explicit unavailable error, events=%v err=%v", events, err)
	}
	if events != nil {
		t.Fatal("unconfigured provider must not return a successful event stream")
	}
}

func TestOpenAICompatibleStreamFailsClosedWithoutTerminalEvent(t *testing.T) {
	out := make(chan Event, 4)
	handleOpenAICompatibleStream(out, strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	close(out)
	var sawError, sawDone bool
	for event := range out {
		sawError = sawError || event.Type == "error"
		sawDone = sawDone || event.Type == "done"
	}
	if !sawError || sawDone {
		t.Fatalf("unterminated stream was not fail-closed: error=%v done=%v", sawError, sawDone)
	}
}

func TestOpenAICompatiblePropagatesLengthFinishReason(t *testing.T) {
	out := make(chan Event, 4)
	handleOpenAICompatibleJSON(out, strings.NewReader(`{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`))
	close(out)
	var done Event
	for event := range out {
		if event.Type == "done" {
			done = event
		}
	}
	if !done.Done || done.StopReason != "length" {
		t.Fatalf("length finish reason was not propagated: %+v", done)
	}
}

func TestOpenAICompatibleNeverForwardsHostedImageGenerationTool(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", BaseURL: server.URL, Model: "model", APIKeyOptional: true})
	if CapabilitiesFor(provider).ImageGeneration {
		t.Fatal("OpenAI-compatible provider must not advertise hosted image generation")
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Messages:              []Message{{Role: "user", Content: "draw"}},
		Tools:                 []ToolSpec{{Name: "Read"}},
		EnableImageGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatal(event.Text)
		}
	}
	tools, _ := requestBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("compatible provider changed local tools: %+v", requestBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	function, _ := tool["function"].(map[string]any)
	if tool["type"] != "function" || function["name"] != "Read" {
		t.Fatalf("hosted image generation was disguised as a function tool: %+v", tool)
	}
}

func TestParseOpenAICompatibleModelCatalog(t *testing.T) {
	models, capabilities, err := parseOpenAICompatibleModelCatalog(strings.NewReader(`{
		"data": [
			{"id": "gpt-a"},
			{"id": "custom-thinker", "supported_reasoning_efforts": ["low", "ultra"]},
			{"id": "gpt-5.6-luna", "supported_reasoning_levels": [{"effort": "low"}, {"effort": "xhigh"}]}
		],
		"models": [
			{"slug": "gpt-b"},
			{"slug": "gpt-a"}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 || models[0] != "gpt-a" || models[1] != "custom-thinker" || models[2] != "gpt-5.6-luna" || models[3] != "gpt-b" {
		t.Fatalf("unexpected catalog order: %+v", models)
	}
	if efforts := strings.Join(capabilities["custom-thinker"].ReasoningEfforts, ","); efforts != "low,ultra" {
		t.Fatalf("string effort array was not parsed: %q", efforts)
	}
	if efforts := strings.Join(capabilities["gpt-5.6-luna"].ReasoningEfforts, ","); efforts != "low,xhigh" {
		t.Fatalf("Codex effort objects were not parsed: %q", efforts)
	}
	if _, exists := capabilities["gpt-a"]; exists {
		t.Fatalf("models without effort lists must not invent capabilities: %+v", capabilities)
	}
}

func TestOpenAICompatibleKnownCodexIdentityExposesExtendedEfforts(t *testing.T) {
	provider := NewOpenAICompatible(config.ProviderConfig{Name: "relay", Type: "openai-compatible", Model: "gpt-5.6-luna", APIKeyOptional: true})
	tests := []struct {
		model, want string
	}{
		{model: "gpt-5.6-luna", want: "low,medium,high,xhigh,max"},
		{model: "openai/gpt-5.6-terra", want: "low,medium,high,xhigh,max,ultra"},
		{model: "gpt-5.6-sol", want: "low,medium,high,xhigh,max"},
		{model: "gpt-5.5", want: "low,medium,high,xhigh"},
		{model: "gpt-5", want: ""},
		{model: "llama-3", want: ""},
	}
	for _, test := range tests {
		got := strings.Join(provider.ModelCapabilities(test.model).ReasoningEfforts, ",")
		if got != test.want {
			t.Fatalf("ModelCapabilities(%q).ReasoningEfforts = %q, want %q", test.model, got, test.want)
		}
	}
}

func TestOpenAICompatibleKnownCodexIdentityAcceptsExtendedEfforts(t *testing.T) {
	tests := []struct {
		model, effort string
	}{
		{model: "gpt-5.6-luna", effort: "max"},
		{model: "gpt-5.6-terra", effort: "ultra"},
		{model: "gpt-5.5", effort: "xhigh"},
	}
	for _, test := range tests {
		t.Run(test.model+"/"+test.effort, func(t *testing.T) {
			var requestBody map[string]any
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/chat/completions" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Fatal(err)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
			}))
			defer server.Close()
			provider := NewOpenAICompatible(config.ProviderConfig{
				Name: "relay", Type: "openai-compatible", BaseURL: server.URL, Model: test.model, APIKeyOptional: true,
			})
			events, err := provider.Generate(context.Background(), GenerateRequest{
				Messages:        []Message{{Role: "user", Content: "think"}},
				ReasoningEffort: test.effort,
			})
			if err != nil {
				t.Fatal(err)
			}
			for event := range events {
				if event.Type == "error" {
					t.Fatalf("unexpected error event: %s", event.Text)
				}
			}
			if requests != 1 {
				t.Fatalf("expected one completion request, got %d", requests)
			}
			if requestBody["reasoning_effort"] != test.effort {
				t.Fatalf("unexpected reasoning payload: %+v", requestBody)
			}
		})
	}
}

func TestOpenAICompatibleKnownCodexIdentityRejectsAboveCeiling(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "relay", Type: "openai-compatible", BaseURL: server.URL, Model: "gpt-5.6-luna", APIKeyOptional: true,
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{ReasoningEffort: "ultra"})
	if err == nil || !errors.Is(err, ErrReasoningEffortUnsupported) || !strings.Contains(err.Error(), "ultra") {
		t.Fatalf("expected luna to reject ultra, events=%v err=%v", events, err)
	}
	if requests != 0 {
		t.Fatalf("unsupported ultra reached upstream %d times", requests)
	}
}

func TestOpenAICompatibleCatalogReasoningEffortsWinOverKnownIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id": "gpt-5.6-luna",
					"supported_reasoning_levels": []map[string]string{
						{"effort": "low"},
						{"effort": "xhigh"},
					},
				}},
			})
		case "/chat/completions":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["reasoning_effort"] != "xhigh" {
				t.Fatalf("unexpected reasoning payload: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "relay", Type: "openai-compatible", BaseURL: server.URL, Model: "gpt-5.6-luna", APIKeyOptional: true,
	})
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if efforts := strings.Join(provider.ModelCapabilities("gpt-5.6-luna").ReasoningEfforts, ","); efforts != "low,xhigh" {
		t.Fatalf("catalog levels were overwritten: %q", efforts)
	}
	if events, err := provider.Generate(context.Background(), GenerateRequest{ReasoningEffort: "max"}); err == nil || !errors.Is(err, ErrReasoningEffortUnsupported) {
		t.Fatalf("explicit catalog without max must reject max, events=%v err=%v", events, err)
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Messages:        []Message{{Role: "user", Content: "think"}},
		ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
	}
}

func TestOpenAICompatibleCatalogAdvertisesUnknownModelUltra(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{{
				"slug":                       "custom-thinker",
				"supported_reasoning_levels": []map[string]string{{"effort": "low"}, {"effort": "ultra"}},
			}},
		})
	}))
	defer server.Close()
	provider := NewOpenAICompatible(config.ProviderConfig{
		Name: "relay", Type: "openai-compatible", BaseURL: server.URL, Model: "fallback", APIKeyOptional: true,
	})
	models, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "custom-thinker" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if efforts := strings.Join(provider.ModelCapabilities("custom-thinker").ReasoningEfforts, ","); efforts != "low,ultra" {
		t.Fatalf("catalog ultra was not exposed: %q", efforts)
	}
}
