package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"autoto/internal/db"
	"autoto/internal/providers"
)

func gatewayRequestWithHeaders(t *testing.T, service *Service, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	return response
}

func anthropicGatewayRequest(t *testing.T, service *Service, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return gatewayRequestWithHeaders(t, service, http.MethodPost, path, body, map[string]string{
		"x-api-key":         token,
		"anthropic-version": supportedAnthropicVersion,
	})
}

func validResponsesBody(extra string) string {
	body := `{"model":"shared","input":"hello"`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func validAnthropicBody(extra string) string {
	body := `{"model":"shared","max_tokens":64,"messages":[{"role":"user","content":"hello"}]`
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func TestGatewayResponsesNonStreamingTranslatesTextImagesToolsAndHistory(t *testing.T) {
	provider := &gatewayTestProvider{
		name: "backend",
		capabilities: providers.Capabilities{
			Tools: true, Streaming: true, ImageInput: true, Reasoning: true, ReasoningEffort: true,
			ReasoningEfforts: []string{"low", "medium", "high"},
		},
		modelCaps: providers.ModelCapabilities{FastMode: true, FastModeKnown: true},
		events: []providers.Event{
			{Type: "dispatch", Dispatch: &providers.DispatchInfo{Provider: "actual-backend", Model: "gpt-4.1-mini", CredentialID: "credential-responses"}},
			{Type: "text", Text: "answer"},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-new", Name: "lookup", Input: json.RawMessage(`{"query":"new"}`)}},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 30, OutputTokens: 12, CachedInputTokens: 4, ReasoningTokens: 3}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20, MonthlyTokenLimit: 1000, MaxConcurrency: 2}, provider, nil)
	body := `{
		"model":"shared",
		"instructions":"Be concise.",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"},{"type":"input_image","image_url":"data:image/png;base64,AQID"}]},
			{"type":"function_call","call_id":"call-old","name":"lookup","arguments":"{\"query\":\"old\"}"},
			{"type":"function_call_output","call_id":"call-old","output":"old result"}
		],
		"tools":[{"type":"function","name":"lookup","description":"Lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}],
		"max_output_tokens":64,
		"reasoning":{"effort":"high"},
		"service_tier":"priority"
	}`
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", body)
	if response.Code != http.StatusOK {
		t.Fatalf("responses status=%d body=%s", response.Code, response.Body.String())
	}
	var result responsesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Object != "response" || result.Status != "completed" || result.Model != "shared" || result.Usage == nil || result.Usage.TotalTokens != 42 {
		t.Fatalf("unexpected response object: %+v", result)
	}
	if len(result.Output) != 2 || result.Output[0].Type != "message" || result.Output[0].Content[0].Text != "answer" || result.Output[1].Type != "function_call" || result.Output[1].CallID != "call-new" || result.Output[1].Arguments != `{"query":"new"}` {
		t.Fatalf("unexpected response output: %+v", result.Output)
	}
	captured := provider.lastRequest()
	if captured.Scenario != providers.CallScenarioGateway || !captured.AllowSubscriptionCredentials || captured.Model != "gpt-4.1-mini" || captured.SystemPrompt != "Be concise." || captured.MaxOutputTokens != 64 || captured.ReasoningEffort != "high" || !captured.FastMode {
		t.Fatalf("provider request boundary mismatch: %+v", captured)
	}
	if len(captured.Messages) != 3 || len(captured.Tools) != 1 || captured.Tools[0].Name != "lookup" {
		t.Fatalf("provider history/tools mismatch: %+v", captured)
	}
	if blocks := captured.Messages[0].Blocks; len(blocks) != 2 || blocks[1].Type != "image" || blocks[1].MIMEType != "image/png" || string(blocks[1].Data) != string([]byte{1, 2, 3}) {
		t.Fatalf("response image was not preserved: %+v", blocks)
	}
	if blocks := captured.Messages[1].Blocks; len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].ToolUseID != "call-old" {
		t.Fatalf("function_call history was not preserved: %+v", blocks)
	}
	if blocks := captured.Messages[2].Blocks; len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].Output != "old result" {
		t.Fatalf("function_call_output history was not preserved: %+v", blocks)
	}
	var records int
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("response request accounting mismatch: records=%d err=%v", records, err)
	}
}

func TestGatewayResponsesStreamingEmitsLifecycleTextToolAndIncomplete(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Tools: true, Streaming: true},
		events: []providers.Event{
			{Type: "text", Text: "hel"},
			{Type: "text", Text: "lo"},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "call-1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`)}},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 8, OutputTokens: 4}},
			{Type: "done", Done: true, StopReason: "max_output_tokens"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	body := `{"model":"shared","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"stream":true}`
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", body)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("responses stream status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	stream := response.Body.String()
	for _, expected := range []string{
		"event: response.created", "event: response.in_progress", "event: response.output_item.added",
		"event: response.output_text.delta", `"delta":"hel"`, `"delta":"lo"`,
		"event: response.function_call_arguments.delta", `\"q\":\"x\"`,
		"event: response.output_item.done", "event: response.incomplete", `"reason":"max_output_tokens"`,
	} {
		if !strings.Contains(stream, expected) {
			t.Fatalf("responses stream missing %q: %s", expected, stream)
		}
	}
	if strings.Contains(stream, "data: [DONE]") {
		t.Fatalf("responses stream used chat-completions terminator: %s", stream)
	}
	var records int
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("responses stream was not recorded exactly once: records=%d err=%v", records, err)
	}
}

func TestGatewayResponsesStreamingEmitsCompletedEvent(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Streaming: true},
		events: []providers.Event{
			{Type: "text", Text: "done"},
			{Type: "done", Done: true, StopReason: "stop"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", validResponsesBody(`"stream":true`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: response.completed") || strings.Contains(response.Body.String(), "event: response.incomplete") {
		t.Fatalf("responses completed stream mismatch: %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayResponsesRejectsStatefulHostedAndRemoteInputsBeforeDispatch(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "hosted tool", body: `{"model":"shared","input":"hello","tools":[{"type":"web_search_preview"}]}`},
		{name: "previous response", body: `{"model":"shared","input":"hello","previous_response_id":"resp_old"}`},
		{name: "background false is explicit", body: `{"model":"shared","input":"hello","background":false}`},
		{name: "store false is explicit", body: `{"model":"shared","input":"hello","store":false}`},
		{name: "conversation", body: `{"model":"shared","input":"hello","conversation":"conv_1"}`},
		{name: "remote image", body: `{"model":"shared","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.test/x.png"}]}]}`},
		{name: "file item", body: `{"model":"shared","input":[{"type":"input_file","file_id":"file_1"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, nil)
			response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", test.body)
			if response.Code != http.StatusBadRequest || harness.provider.requestCount() != 0 {
				t.Fatalf("unsafe response input was dispatched: status=%d body=%s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
			}
		})
	}
}

func TestGatewayResponsesBodyLimitAndOriginSecurity(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, func(options *Options) {
		options.MaxRequestBytes = 128
	})
	oversized := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", validResponsesBody(`"input":"`+strings.Repeat("x", 512)+`"`))
	if oversized.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversized.Body.String(), "request_too_large") || harness.provider.requestCount() != 0 {
		t.Fatalf("responses body limit failed: %d %s", oversized.Code, oversized.Body.String())
	}
	origin := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/responses", validResponsesBody(""), map[string]string{
		"Authorization": "Bearer " + harness.generated.Token,
		"Origin":        "https://example.test",
	})
	if origin.Code != http.StatusForbidden || origin.Header().Get("Access-Control-Allow-Origin") != "" || !strings.Contains(origin.Body.String(), "browser_origin_forbidden") {
		t.Fatalf("responses origin guard failed: %d headers=%v body=%s", origin.Code, origin.Header(), origin.Body.String())
	}
}

func TestGatewayAnthropicNonStreamingSupportsAPIKeyImagesToolsAndHistory(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Tools: true, Streaming: true, ImageInput: true},
		events: []providers.Event{
			{Type: "text", Text: "answer"},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "toolu-new", Name: "lookup", Input: json.RawMessage(`{"query":"new"}`)}},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 25, OutputTokens: 9, CachedInputTokens: 2}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20, MonthlyTokenLimit: 1000}, provider, nil)
	body := `{
		"model":"shared",
		"max_tokens":80,
		"system":[{"type":"text","text":"Use tools carefully."}],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"}}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu-old","name":"lookup","input":{"query":"old"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu-old","content":"old result"}]}
		],
		"tools":[{"name":"lookup","description":"Lookup","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}]
	}`
	response := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", body)
	if response.Code != http.StatusOK {
		t.Fatalf("anthropic status=%d body=%s", response.Code, response.Body.String())
	}
	var result anthropicMessageResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	// The backend reported 25 input tokens with 2 of them cached; on the
	// Anthropic wire input_tokens excludes cache reads, so the response must
	// carry 23 + 2 rather than repeat the subset inside the total.
	if result.Type != "message" || result.Role != "assistant" || result.Model != "shared" || result.StopReason == nil || *result.StopReason != "tool_use" || result.Usage.InputTokens != 23 || result.Usage.CacheReadInputTokens != 2 || len(result.Content) != 2 {
		t.Fatalf("unexpected Anthropic response: %+v", result)
	}
	if result.Content[0].Type != "text" || result.Content[0].Text != "answer" || result.Content[1].Type != "tool_use" || result.Content[1].ID != "toolu-new" || string(result.Content[1].Input) != `{"query":"new"}` {
		t.Fatalf("unexpected Anthropic content: %+v", result.Content)
	}
	captured := provider.lastRequest()
	if captured.Scenario != providers.CallScenarioGateway || !captured.AllowSubscriptionCredentials || captured.SystemPrompt != "Use tools carefully." || captured.MaxOutputTokens != 80 || len(captured.Messages) != 3 || len(captured.Tools) != 1 {
		t.Fatalf("Anthropic provider request mismatch: %+v", captured)
	}
	if blocks := captured.Messages[0].Blocks; len(blocks) != 2 || blocks[1].Type != "image" || string(blocks[1].Data) != string([]byte{1, 2, 3}) {
		t.Fatalf("Anthropic image was not preserved: %+v", blocks)
	}
	if blocks := captured.Messages[1].Blocks; len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].ToolUseID != "toolu-old" {
		t.Fatalf("Anthropic tool_use history missing: %+v", blocks)
	}
	if blocks := captured.Messages[2].Blocks; len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].Output != "old result" {
		t.Fatalf("Anthropic tool_result history missing: %+v", blocks)
	}
}

func TestGatewayAnthropicStreamingEmitsNamedEvents(t *testing.T) {
	provider := &gatewayTestProvider{
		name:         "backend",
		capabilities: providers.Capabilities{Tools: true, Streaming: true},
		events: []providers.Event{
			{Type: "text", Text: "hello"},
			{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "toolu-1", Name: "lookup", Input: json.RawMessage(`{"q":"x"}`)}},
			{Type: "usage", Usage: &providers.Usage{InputTokens: 6, OutputTokens: 4}},
			{Type: "done", Done: true, StopReason: "tool_use"},
		},
	}
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
	body := `{"model":"shared","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}],"stream":true}`
	response := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", body)
	if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Anthropic stream status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	stream := response.Body.String()
	for _, expected := range []string{
		"event: message_start", "event: content_block_start", "event: content_block_delta", `"type":"text_delta"`,
		`"type":"input_json_delta"`, `\"q\":\"x\"`, "event: content_block_stop", "event: message_delta",
		`"stop_reason":"tool_use"`, "event: message_stop",
	} {
		if !strings.Contains(stream, expected) {
			t.Fatalf("Anthropic stream missing %q: %s", expected, stream)
		}
	}
	if strings.Contains(stream, "[DONE]") {
		t.Fatalf("Anthropic stream used OpenAI terminator: %s", stream)
	}
	var records int
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("Anthropic stream accounting mismatch: records=%d err=%v", records, err)
	}
}

func TestGatewayAnthropicAuthenticationVersionAndErrorEnvelope(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20}, nil, nil)
	body := validAnthropicBody("")
	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantType   string
	}{
		{name: "missing key", headers: map[string]string{"anthropic-version": supportedAnthropicVersion}, wantStatus: http.StatusUnauthorized, wantType: "authentication_error"},
		{name: "conflicting keys", headers: map[string]string{"Authorization": "Bearer " + harness.generated.Token, "x-api-key": "different", "anthropic-version": supportedAnthropicVersion}, wantStatus: http.StatusUnauthorized, wantType: "authentication_error"},
		{name: "missing version", headers: map[string]string{"x-api-key": harness.generated.Token}, wantStatus: http.StatusBadRequest, wantType: "invalid_request_error"},
		{name: "wrong version", headers: map[string]string{"x-api-key": harness.generated.Token, "anthropic-version": "2024-01-01"}, wantStatus: http.StatusBadRequest, wantType: "invalid_request_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := harness.provider.requestCount()
			response := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/messages", body, test.headers)
			if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"type":"error"`) || !strings.Contains(response.Body.String(), `"type":"`+test.wantType+`"`) || harness.provider.requestCount() != before {
				t.Fatalf("unexpected Anthropic error: status=%d body=%s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
			}
		})
	}

	bearer := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer " + harness.generated.Token,
		"anthropic-version": supportedAnthropicVersion,
	})
	if bearer.Code != http.StatusOK {
		t.Fatalf("Bearer credential failed: %d %s", bearer.Code, bearer.Body.String())
	}
	both := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/messages", body, map[string]string{
		"Authorization":     "Bearer " + harness.generated.Token,
		"x-api-key":         harness.generated.Token,
		"anthropic-version": supportedAnthropicVersion,
	})
	if both.Code != http.StatusOK {
		t.Fatalf("matching Bearer/x-api-key credentials failed: %d %s", both.Code, both.Body.String())
	}
}

func TestGatewayAnthropicBodyLimitOriginAndUnsupportedMediaUseAnthropicErrors(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20}, nil, func(options *Options) {
		options.MaxRequestBytes = 256
	})
	oversized := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", validAnthropicBody(`"metadata":{"value":"`+strings.Repeat("x", 512)+`"}`))
	if oversized.Code != http.StatusRequestEntityTooLarge || !strings.Contains(oversized.Body.String(), `"type":"request_too_large"`) || harness.provider.requestCount() != 0 {
		t.Fatalf("Anthropic body limit error mismatch: %d %s", oversized.Code, oversized.Body.String())
	}
	origin := gatewayRequestWithHeaders(t, harness.service, http.MethodPost, "/v1/messages", validAnthropicBody(""), map[string]string{
		"x-api-key":         harness.generated.Token,
		"anthropic-version": supportedAnthropicVersion,
		"Origin":            "https://example.test",
	})
	if origin.Code != http.StatusForbidden || !strings.Contains(origin.Body.String(), `"type":"permission_error"`) || origin.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("Anthropic origin error mismatch: %d headers=%v body=%s", origin.Code, origin.Header(), origin.Body.String())
	}
	remote := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", `{"model":"shared","max_tokens":64,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/x.png"}}]}]}`)
	if remote.Code != http.StatusBadRequest || !strings.Contains(remote.Body.String(), `"type":"invalid_request_error"`) || harness.provider.requestCount() != 0 {
		t.Fatalf("Anthropic remote image was not rejected: %d %s", remote.Code, remote.Body.String())
	}
}

func TestGatewayAnthropicCountTokensIsLocalUnmeteredAndPermissionChecked(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 20, MonthlyTokenLimit: 10}, nil, nil)
	if _, err := harness.store.AddAPIRequest(context.Background(), db.APIRequest{ID: "exhausted", Kind: "gateway", GatewayKeyID: harness.key.ID, InputTokens: 6, OutputTokens: 4, CreatedAt: gatewayTestNow.Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	body := `{"model":"shared","system":"Count locally.","messages":[{"role":"user","content":[{"type":"text","text":"hello world"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"}}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`
	response := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages/count_tokens", body)
	if response.Code != http.StatusOK {
		t.Fatalf("count_tokens status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		InputTokens int64 `json:"input_tokens"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.InputTokens <= 0 {
		t.Fatalf("invalid local token estimate: %+v err=%v", result, err)
	}
	if harness.provider.requestCount() != 0 {
		t.Fatalf("count_tokens called provider %d times", harness.provider.requestCount())
	}
	var records int
	if err := harness.store.DB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM api_requests WHERE gateway_key_id = ?`, harness.key.ID).Scan(&records); err != nil || records != 1 {
		t.Fatalf("count_tokens consumed accounting quota: records=%d err=%v", records, err)
	}

	disallowed := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages/count_tokens", `{"model":"not-allowed","messages":[{"role":"user","content":"hello"}]}`)
	if disallowed.Code != http.StatusNotFound || !strings.Contains(disallowed.Body.String(), `"type":"not_found_error"`) || harness.provider.requestCount() != 0 {
		t.Fatalf("count_tokens bypassed model permissions: %d %s", disallowed.Code, disallowed.Body.String())
	}
}

func TestGatewayLimitsAreSharedAcrossChatResponsesAndMessages(t *testing.T) {
	t.Run("rpm", func(t *testing.T) {
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 2}, nil, nil)
		responses := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", validResponsesBody(""))
		messages := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", validAnthropicBody(""))
		chat := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/chat/completions", validCompletionBody(""))
		if responses.Code != http.StatusOK || messages.Code != http.StatusOK || chat.Code != http.StatusTooManyRequests || !strings.Contains(chat.Body.String(), "rate_limit_exceeded") {
			t.Fatalf("shared RPM mismatch: responses=%d messages=%d chat=%d %s", responses.Code, messages.Code, chat.Code, chat.Body.String())
		}
	})

	for _, test := range []struct {
		name string
		path string
		body string
		call func(*testing.T, gatewayHarness, string, string) *httptest.ResponseRecorder
	}{
		{name: "responses monthly", path: "/v1/responses", body: `{"model":"shared","input":"hello","max_output_tokens":2}`, call: func(t *testing.T, h gatewayHarness, path, body string) *httptest.ResponseRecorder {
			return gatewayRequest(t, h.service, h.generated.Token, http.MethodPost, path, body)
		}},
		{name: "messages monthly", path: "/v1/messages", body: `{"model":"shared","max_tokens":2,"messages":[{"role":"user","content":"hello"}]}`, call: func(t *testing.T, h gatewayHarness, path, body string) *httptest.ResponseRecorder {
			return anthropicGatewayRequest(t, h.service, h.generated.Token, path, body)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10, MonthlyTokenLimit: 10}, nil, nil)
			if _, err := harness.store.AddAPIRequest(context.Background(), db.APIRequest{ID: "used", Kind: "gateway", GatewayKeyID: harness.key.ID, InputTokens: 6, OutputTokens: 3, CreatedAt: gatewayTestNow.Format(time.RFC3339Nano)}); err != nil {
				t.Fatal(err)
			}
			response := test.call(t, harness, test.path, test.body)
			if response.Code != http.StatusTooManyRequests || harness.provider.requestCount() != 0 {
				t.Fatalf("monthly limit bypassed: %d %s requests=%d", response.Code, response.Body.String(), harness.provider.requestCount())
			}
		})
	}

	t.Run("cross protocol concurrency", func(t *testing.T) {
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
			firstDone <- gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", validResponsesBody(""))
		}()
		select {
		case <-provider.started:
		case <-time.After(5 * time.Second):
			t.Fatal("responses request did not reach provider")
		}
		blocked := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", validAnthropicBody(""))
		if blocked.Code != http.StatusTooManyRequests || !strings.Contains(blocked.Body.String(), `"type":"rate_limit_error"`) {
			close(provider.release)
			t.Fatalf("cross-protocol concurrency bypassed: %d %s", blocked.Code, blocked.Body.String())
		}
		close(provider.release)
		select {
		case first := <-firstDone:
			if first.Code != http.StatusOK {
				t.Fatalf("first responses request failed: %d %s", first.Code, first.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("first responses request did not complete")
		}
	})
}

func TestGatewaySubscriptionCredentialSharingDefaultsToDisabled(t *testing.T) {
	harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, nil, func(options *Options) {
		options.AllowSubscriptionCredentials = false
	})
	response := gatewayRequest(t, harness.service, harness.generated.Token, http.MethodPost, "/v1/responses", validResponsesBody(""))
	if response.Code != http.StatusOK {
		t.Fatalf("default credential sharing request failed: %d %s", response.Code, response.Body.String())
	}
	captured := harness.provider.lastRequest()
	availability := harness.provider.lastAvailability()
	if captured.AllowSubscriptionCredentials || availability.AllowSubscriptionCredentials || captured.Scenario != providers.CallScenarioGateway || availability.EffectiveScenario() != providers.CallScenarioGateway {
		t.Fatalf("subscription credential authorization was enabled implicitly: request=%+v availability=%+v", captured, availability)
	}
}

// TestGatewayAnthropicSurfacesReasoningBlocks covers the halves of the Anthropic
// gateway that the reasoning work left unfinished: the non-streaming path had to
// grow addSnapshot, and the streaming path had been deleted outright while
// handleAnthropicMessages still dispatched to it. Both must present a signed
// thinking block, and neither may duplicate the answer text -- the provider
// emits a content_block snapshot for text too, and that snapshot is the trap.
func TestGatewayAnthropicSurfacesReasoningBlocks(t *testing.T) {
	thinking := providers.NewAnthropicThinkingContentBlock("shared", "Weighing the options.", "sig-1")
	redacted := providers.NewAnthropicRedactedThinkingContentBlock("shared", "ENCRYPTED")
	textSnapshot := providers.ContentBlock{Type: "text", Text: "answer"}
	events := []providers.Event{
		{Type: "reasoning", Text: "Weighing the options.", BlockType: providers.ContentBlockTypeThinking},
		{Type: "content_block", ContentBlock: &thinking, BlockType: thinking.Type},
		{Type: "content_block", ContentBlock: &redacted, BlockType: redacted.Type},
		{Type: "text", Text: "answer"},
		{Type: "content_block", ContentBlock: &textSnapshot, BlockType: "text"},
		{Type: "usage", Usage: &providers.Usage{InputTokens: 9, OutputTokens: 5}},
		{Type: "done", Done: true, StopReason: "end_turn"},
	}
	capabilities := providers.Capabilities{Tools: true, Streaming: true, NativeReasoningBlocks: true}
	body := `{"model":"shared","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`

	t.Run("non-streaming", func(t *testing.T) {
		provider := &gatewayTestProvider{name: "backend", capabilities: capabilities, events: events}
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
		response := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", body)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		var result anthropicMessageResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 3 {
			t.Fatalf("expected thinking, redacted_thinking and one text block, got %+v", result.Content)
		}
		if result.Content[0].Type != providers.ContentBlockTypeThinking || result.Content[0].Thinking != "Weighing the options." || result.Content[0].Signature != "sig-1" {
			t.Fatalf("thinking block not surfaced: %+v", result.Content[0])
		}
		if result.Content[1].Type != providers.ContentBlockTypeRedactedThinking || result.Content[1].Data != "ENCRYPTED" {
			t.Fatalf("redacted thinking block not surfaced: %+v", result.Content[1])
		}
		if result.Content[2].Type != "text" || result.Content[2].Text != "answer" {
			t.Fatalf("answer text was duplicated or lost: %+v", result.Content)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		provider := &gatewayTestProvider{name: "backend", capabilities: capabilities, events: events}
		harness := newGatewayHarness(t, db.GatewayKey{Enabled: true, RequestsPerMinute: 10}, provider, nil)
		response := anthropicGatewayRequest(t, harness.service, harness.generated.Token, "/v1/messages", `{"model":"shared","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
		stream := response.Body.String()
		for _, expected := range []string{
			"event: message_start", `"type":"thinking"`, `"signature":"sig-1"`,
			`"type":"redacted_thinking"`, `"data":"ENCRYPTED"`, `"type":"text_delta"`, "event: message_stop",
		} {
			if !strings.Contains(stream, expected) {
				t.Fatalf("stream missing %q: %s", expected, stream)
			}
		}
		// The provider also emits a content_block snapshot for the text it already
		// streamed. Replaying that would open a second text block carrying the whole
		// answer, so exactly one text block may start and it must be delta-fed.
		if got := strings.Count(stream, `"content_block":{"type":"text"}`); got != 1 {
			t.Fatalf("expected exactly one text block start, got %d: %s", got, stream)
		}
		if got := strings.Count(stream, `"type":"text_delta"`); got != 1 {
			t.Fatalf("expected exactly one text delta, got %d: %s", got, stream)
		}
	})
}
