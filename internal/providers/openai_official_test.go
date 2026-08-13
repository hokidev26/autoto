package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoto/internal/config"
)

func TestOpenAIOfficialStreamsTextAndUsage(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("expected bearer auth header, got %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","item_id":"item_1","output_index":0,"content_index":0,"delta":"hel","logprobs":[],"sequence_number":1}`,
			``,
			`event: response.output_text.delta`,
			`data: {"type":"response.output_text.delta","item_id":"item_1","output_index":0,"content_index":0,"delta":"lo","logprobs":[],"sequence_number":2}`,
			``,
			`event: response.output_text.done`,
			`data: {"type":"response.output_text.done","item_id":"item_1","output_index":0,"content_index":0,"text":"hello","logprobs":[],"sequence_number":3}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-4.1-mini","status":"completed","error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":1},"total_tokens":16}}}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-4.1-mini"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{SystemPrompt: "Be concise.", Messages: []Message{{Role: "user", Content: "hello"}}, MaxOutputTokens: 64})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var usage Usage
	var done bool
	var dispatch *DispatchInfo
	for event := range events {
		if event.Dispatch != nil {
			dispatch = event.Dispatch
		}
		switch event.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", event.Text)
		case "text":
			text += event.Text
		case "usage":
			if event.Usage != nil {
				usage = *event.Usage
			}
		case "done":
			done = true
		}
	}
	if requestBody["stream"] != true || requestBody["max_output_tokens"] != float64(64) {
		t.Fatalf("expected stream and max output tokens, got %+v", requestBody)
	}
	input, _ := requestBody["input"].(string)
	if !strings.Contains(input, "User: hello") || requestBody["instructions"] != "Be concise." {
		t.Fatalf("unexpected request body: %+v", requestBody)
	}
	if text != "hello" {
		t.Fatalf("expected only delta text hello without done duplication, got %q", text)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 4 || usage.CachedInputTokens != 3 || usage.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if !done {
		t.Fatal("expected done event")
	}
	if dispatch == nil || dispatch.Provider != "openai" || dispatch.Model != "gpt-4.1-mini" || dispatch.CredentialID != configuredCredentialID {
		t.Fatalf("unexpected dispatch attribution: %+v", dispatch)
	}
}

// prompt_cache_key routes consecutive turns of one conversation to the same
// OpenAI cache shard. Run requests carry the agent ID as SessionKey; one-shot
// internal calls without a key must not send the field at all.
func TestOpenAIOfficialForwardsPromptCacheKey(t *testing.T) {
	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestBodies = append(requestBodies, body)
		writeOpenAICompletedStream(w)
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-4.1-mini"})
	generate := func(sessionKey string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}, SessionKey: sessionKey})
		if err != nil {
			t.Fatal(err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatalf("unexpected error event: %s", event.Text)
			}
		}
	}
	generate("agent-1")
	generate("")

	if len(requestBodies) != 2 {
		t.Fatalf("expected two captured requests, got %d", len(requestBodies))
	}
	if requestBodies[0]["prompt_cache_key"] != "agent-1" {
		t.Fatalf("expected prompt_cache_key agent-1, got %+v", requestBodies[0])
	}
	if _, present := requestBodies[1]["prompt_cache_key"]; present {
		t.Fatalf("keyless request must not send prompt_cache_key: %+v", requestBodies[1])
	}
}

func TestOpenAIOfficialStreamsDoneTextWhenNoDelta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: response.output_text.done`,
			`data: {"type":"response.output_text.done","item_id":"item_1","output_index":0,"content_index":0,"text":"fallback","logprobs":[],"sequence_number":1}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-4.1-mini","status":"completed","error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-4.1-mini"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
		if event.Type == "text" {
			text += event.Text
		}
	}
	if text != "fallback" {
		t.Fatalf("expected done text fallback when no deltas arrived, got %q", text)
	}
}

func TestOpenAIOfficialEmitsFunctionToolCall(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: response.output_item.done`,
			`data: {"type":"response.output_item.done","output_index":0,"sequence_number":1,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"Read","arguments":"{\"path\":\"README.md\"}"}}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-4.1-mini","status":"completed","error":null,"incomplete_details":null,"output":[{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"Read","arguments":"{\"path\":\"README.md\"}"}],"usage":{"input_tokens":5,"output_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":7}}}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-4.1-mini"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := provider.Generate(ctx, GenerateRequest{
		Messages: []Message{{Role: "user", Content: "read README"}},
		Tools: []ToolSpec{{
			Name:        "Read",
			Description: "Read a file",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []any{"path"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var calls []ToolCall
	var done bool
	for event := range events {
		switch event.Type {
		case "error":
			t.Fatalf("unexpected error event: %s", event.Text)
		case "tool_call":
			if event.ToolCall != nil {
				calls = append(calls, *event.ToolCall)
			}
		case "done":
			done = true
		}
	}
	if !done {
		t.Fatal("expected done event")
	}
	if len(calls) != 1 {
		t.Fatalf("expected one tool call, got %+v", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "Read" || string(calls[0].Input) != `{"path":"README.md"}` {
		t.Fatalf("unexpected tool call: %+v", calls[0])
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool in request, got %+v", requestBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "Read" || tool["description"] != "Read a file" {
		t.Fatalf("unexpected tool payload: %+v", tool)
	}
	if _, ok := requestBody["input"].([]any); !ok {
		t.Fatalf("expected structured input list when tools are present, got %+v", requestBody["input"])
	}
}

func TestOpenAIOfficialSerializesToolHistory(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: response.completed`,
			`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-4.1-mini","status":"completed","error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-4.1-mini"})
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
	input, ok := requestBody["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("expected function call and output input items, got %+v", requestBody["input"])
	}
	functionCall, _ := input[0].(map[string]any)
	if functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionCall["name"] != "Read" || functionCall["arguments"] != `{"path":"README.md"}` {
		t.Fatalf("unexpected function call history item: %+v", functionCall)
	}
	functionOutput, _ := input[1].(map[string]any)
	if functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "call_1" || functionOutput["output"] != "file contents" {
		t.Fatalf("unexpected function output history item: %+v", functionOutput)
	}
}

func TestOpenAIOfficialSendsReasoningAndAutotoIdentity(t *testing.T) {
	const installationID = "123e4567-e89b-42d3-a456-426614174000"
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Autoto-Client"); got != "autoto/1.2.3" {
			t.Fatalf("unexpected Autoto client header %q", got)
		}
		if got := r.Header.Get("X-Autoto-Installation-ID"); got != installationID {
			t.Fatalf("unexpected installation header %q", got)
		}
		if strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "codex") || strings.Contains(strings.ToLower(r.Header.Get("User-Agent")), "chatgpt") {
			t.Fatalf("client must not impersonate Codex or ChatGPT: %q", r.Header.Get("User-Agent"))
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		writeOpenAICompletedStream(w)
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{
		BaseURL:        server.URL,
		APIKey:         "test-key",
		Model:          "gpt-5",
		ClientVersion:  "1.2.3",
		InstallationID: installationID,
	})
	if !CapabilitiesFor(provider).ReasoningEffort {
		t.Fatal("official OpenAI provider must declare reasoning effort support")
	}
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Messages:        []Message{{Role: "user", Content: "think"}},
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range events {
		if event.Type == "error" {
			t.Fatalf("unexpected error event: %s", event.Text)
		}
	}
	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok || reasoning["effort"] != "high" {
		t.Fatalf("unexpected official reasoning payload: %+v", requestBody["reasoning"])
	}
}

func TestOpenAIOfficialAutoOmitsReasoning(t *testing.T) {
	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestBodies = append(requestBodies, body)
		writeOpenAICompletedStream(w)
	}))
	defer server.Close()
	provider := NewOpenAIOfficial(config.ProviderConfig{BaseURL: server.URL, APIKey: "test-key", Model: "gpt-5"})
	for _, effort := range []string{"", "auto"} {
		events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "think"}}, ReasoningEffort: effort})
		if err != nil {
			t.Fatal(err)
		}
		for range events {
		}
	}
	if len(requestBodies) != 2 {
		t.Fatalf("expected two requests, got %d", len(requestBodies))
	}
	for _, body := range requestBodies {
		if _, exists := body["reasoning"]; exists {
			t.Fatalf("auto reasoning must be omitted: %+v", body)
		}
	}
}

func TestOpenAIOfficialWithoutAPIKeyReturnsUnavailableError(t *testing.T) {
	provider := NewOpenAIOfficial(config.ProviderConfig{Model: "gpt-4.1-mini"})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "hello"}}})
	if err == nil || !errors.Is(err, ErrProviderUnavailable) || !strings.Contains(strings.ToLower(err.Error()), "unavailable") {
		t.Fatalf("expected explicit unavailable error, events=%v err=%v", events, err)
	}
	if events != nil {
		t.Fatal("unconfigured provider must not return a successful event stream")
	}
}

func TestOpenAIOfficialImageGenerationRequiresExplicitConfiguredEnablement(t *testing.T) {
	inputPNG := testImageGenerationPNG(t, 2, 2)
	messages := []Message{{Role: "user", Blocks: []ContentBlock{{Type: "text", Text: "draw"}, {Type: "image", MIMEType: "image/png", Data: inputPNG}}}}
	var requestBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requestBodies = append(requestBodies, body)
		writeOpenAICompletedStream(w)
	}))
	defer server.Close()

	configured := NewOpenAIOfficial(config.ProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-enabled",
		Models: []config.ProviderModelConfig{{Name: "gpt-image-enabled", ImageGeneration: true}},
	})
	for _, enabled := range []bool{true, false} {
		events, err := configured.Generate(context.Background(), GenerateRequest{
			Messages:              messages,
			Tools:                 []ToolSpec{{Name: "Read"}},
			EnableImageGeneration: enabled,
		})
		if err != nil {
			t.Fatal(err)
		}
		for event := range events {
			if event.Type == "error" {
				t.Fatal(event.Text)
			}
		}
	}
	unconfigured := NewOpenAIOfficial(config.ProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-disabled",
		Models: []config.ProviderModelConfig{{Name: "gpt-image-disabled"}},
	})
	events, err := unconfigured.Generate(context.Background(), GenerateRequest{
		Messages:              messages,
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

	if len(requestBodies) != 3 {
		t.Fatalf("expected three requests, got %d", len(requestBodies))
	}
	for index, wantImage := range []bool{true, false, false} {
		tools, _ := requestBodies[index]["tools"].([]any)
		functionCount := 0
		imageCount := 0
		for _, raw := range tools {
			tool, _ := raw.(map[string]any)
			switch tool["type"] {
			case "function":
				functionCount++
			case "image_generation":
				imageCount++
				if tool["output_format"] != "png" {
					t.Fatalf("unexpected image output format: %+v", tool)
				}
			}
		}
		if functionCount != 1 || (imageCount == 1) != wantImage {
			t.Fatalf("request %d tools do not match enablement: %+v", index, tools)
		}
		inputItems, _ := requestBodies[index]["input"].([]any)
		sawInputImage := false
		for _, rawInput := range inputItems {
			inputItem, _ := rawInput.(map[string]any)
			content, _ := inputItem["content"].([]any)
			for _, rawContent := range content {
				contentItem, _ := rawContent.(map[string]any)
				if contentItem["type"] == "input_image" {
					sawInputImage = true
				}
			}
		}
		if !sawInputImage {
			t.Fatalf("request %d lost image input while toggling hosted generation: %+v", index, requestBodies[index]["input"])
		}
	}
}

func TestOpenAIOfficialStreamsImageGenerationAndRebuildsHistory(t *testing.T) {
	pngData := testImageGenerationPNG(t, 4, 5)
	encoded := base64.StdEncoding.EncodeToString(pngData)
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			`event: response.output_item.added`,
			`data: {"type":"response.output_item.added","output_index":0,"sequence_number":1,"item":{"type":"image_generation_call","id":"ig_new","status":"in_progress","revised_prompt":"a blue lighthouse"}}`,
			``,
			`event: response.image_generation_call.partial_image`,
			`data: {"type":"response.image_generation_call.partial_image","item_id":"ig_new","output_index":0,"partial_image_index":0,"partial_image_b64":"aGVsbG8=","sequence_number":2}`,
			``,
			`event: response.output_item.done`,
			`data: {"type":"response.output_item.done","output_index":0,"sequence_number":3,"item":{"type":"image_generation_call","id":"ig_new","status":"completed","result":"` + encoded + `"}}`,
			``,
			`event: response.completed`,
			`data: {"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-image-enabled","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"image_generation_call","id":"ig_new","status":"completed","result":"` + encoded + `"}],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}}`,
			``,
		}, "\n") + "\n\n"))
	}))
	defer server.Close()

	provider := NewOpenAIOfficial(config.ProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-enabled",
		Models: []config.ProviderModelConfig{{Name: "gpt-image-enabled", ImageGeneration: true}},
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{
		Messages: []Message{
			{Role: "assistant", Blocks: []ContentBlock{{Type: "image_generation", GenerationID: "ig_history", Status: "completed", MIMEType: "image/png", Data: pngData}}},
			{Role: "user", Content: "make it blue"},
		},
		EnableImageGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var final *ImageGeneration
	var finalCount int
	for event := range events {
		if event.Type == "error" {
			t.Fatal(event.Text)
		}
		if event.ToolCall != nil {
			t.Fatalf("image generation call became a local tool call: %+v", event.ToolCall)
		}
		if event.Type == "image_generation" && event.ImageGeneration != nil && len(event.ImageGeneration.Data) > 0 {
			final = event.ImageGeneration
			finalCount++
		}
	}
	if finalCount != 1 || final == nil || final.GenerationID != "ig_new" || final.RevisedPrompt != "a blue lighthouse" || final.MIME != "image/png" || final.Width != 4 || final.Height != 5 || !bytes.Equal(final.Data, pngData) {
		t.Fatalf("unexpected final image event: count=%d event=%+v", finalCount, final)
	}
	input, _ := requestBody["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected image generation history and user message, got %+v", requestBody["input"])
	}
	history, _ := input[0].(map[string]any)
	if history["type"] != "image_generation_call" || history["id"] != "ig_history" || history["status"] != "completed" || history["result"] != encoded {
		t.Fatalf("unexpected image generation history item: %+v", history)
	}
}

func TestOpenAIOfficialPreservesUpstreamForbiddenMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Image generation is not enabled for this project","type":"invalid_request_error","code":"image_generation_not_enabled"}}`))
	}))
	defer server.Close()
	provider := NewOpenAIOfficial(config.ProviderConfig{
		BaseURL: server.URL, APIKey: "test-key", Model: "gpt-image-enabled",
		Models: []config.ProviderModelConfig{{Name: "gpt-image-enabled", ImageGeneration: true}},
	})
	events, err := provider.Generate(context.Background(), GenerateRequest{Messages: []Message{{Role: "user", Content: "draw"}}, EnableImageGeneration: true})
	if err != nil {
		t.Fatal(err)
	}
	var errorText string
	for event := range events {
		if event.Type == "error" {
			errorText = event.Text
		}
	}
	if !strings.Contains(errorText, "Image generation is not enabled for this project") || !strings.Contains(errorText, "403") {
		t.Fatalf("upstream 403 detail was not preserved: %q", errorText)
	}
}

func writeOpenAICompletedStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = w.Write([]byte("event: response.completed\n" +
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"model":"gpt-5","status":"completed","error":null,"incomplete_details":null,"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0},"total_tokens":2}}}` + "\n\n"))
}
