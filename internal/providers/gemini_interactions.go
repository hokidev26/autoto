package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"autoto/internal/config"
)

const defaultGeminiInteractionsURL = "https://generativelanguage.googleapis.com/v1beta/interactions"

type GeminiInteractions struct {
	cfg       config.ProviderConfig
	client    *http.Client
	configErr error
}

func NewGeminiInteractions(cfg config.ProviderConfig) *GeminiInteractions {
	if cfg.Name == "" {
		cfg.Name = "gemini"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultGeminiInteractionsURL
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-2.5-pro"
	}
	client, clientErr := providerHTTPClient(cfg, 90*time.Second)
	return &GeminiInteractions{cfg: cfg, client: client, configErr: errors.Join(validateProviderRuntimeConfig(cfg), clientErr)}
}

func (p *GeminiInteractions) Name() string { return p.cfg.Name }

func (p *GeminiInteractions) Configured() bool {
	return p != nil && p.configErr == nil && strings.TrimSpace(p.cfg.APIKey) != ""
}

func (p *GeminiInteractions) Capabilities() Capabilities {
	return Capabilities{Tools: true, Streaming: true, ImageInput: true, Reasoning: true, ReasoningEffort: true, ReasoningEfforts: []string{"low", "medium", "high"}}
}

func (p *GeminiInteractions) DefaultContextTokenLimit() int { return 1048576 }

func (p *GeminiInteractions) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

// The Interactions endpoint does not expose a portable model-list resource for
// API-key clients, so configured models remain the reliable fallback.
func (p *GeminiInteractions) ListModels(context.Context) ([]string, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	return fallbackModels(p.cfg.Model), nil
}

func fallbackModels(model string) []string {
	if model == "" {
		return []string{}
	}
	return []string{model}
}

func (p *GeminiInteractions) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, providerUnavailableError(p.cfg.Name, "API key is not configured")
	}
	out := make(chan Event, 16)

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	payload := map[string]any{
		"model":  model,
		"input":  geminiInteractionMessages(req),
		"stream": true,
		"store":  false,
	}
	if prompt := strings.TrimSpace(req.SystemPrompt); prompt != "" {
		payload["system_instruction"] = prompt
	}
	if tools := geminiInteractionTools(req.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
	generationConfig := map[string]any{}
	if effort := normalizeGeminiReasoningEffort(req.ReasoningEffort); effort != "" {
		generationConfig["thinking_level"] = effort
	}
	if req.MaxOutputTokens > 0 {
		generationConfig["max_output_tokens"] = req.MaxOutputTokens
	}
	if len(generationConfig) > 0 {
		payload["generation_config"] = generationConfig
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	endpoint, err := url.Parse(strings.TrimRight(p.cfg.BaseURL, "/"))
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "request URL is invalid")
	}
	query := endpoint.Query()
	if query.Get("alt") == "" {
		query.Set("alt", "sse")
	}
	endpoint.RawQuery = query.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "request could not be constructed")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream, application/json")
	httpReq.Header.Set("Api-Revision", "2026-05-20")
	httpReq.Header.Set("x-goog-api-key", p.cfg.APIKey)

	go func() {
		defer close(out)
		out <- newDispatchEvent(p.cfg.Name, model, configuredCredentialID)
		res, err := p.client.Do(httpReq)
		if err != nil {
			out <- Event{Type: "error", Text: providerRequestFailedText(p.cfg.Name)}
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= http.StatusMultipleChoices {
			out <- Event{Type: "error", Text: providerHTTPFailedText(p.cfg.Name, res.StatusCode)}
			return
		}
		if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
			handleGeminiInteractionsSSE(out, res.Body)
			return
		}
		handleGeminiInteractionsJSON(out, res.Body)
	}()
	return out, nil
}

func normalizeGeminiReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return ""
	}
}

func geminiErrorMessage(value map[string]any) string {
	if nested, ok := value["error"].(map[string]any); ok {
		if message, _ := nested["message"].(string); strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	message, _ := value["message"].(string)
	return strings.TrimSpace(message)
}

func geminiInteractionMessages(req GenerateRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, geminiInteractionMessageItems(message)...)
	}
	if len(messages) == 0 {
		messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "Continue."}}})
	}
	return messages
}

func geminiInteractionMessageItems(message Message) []map[string]any {
	blocks := normalizeContentBlocks(message)
	items := make([]map[string]any, 0, len(blocks)+1)
	content := make([]map[string]any, 0, len(blocks))
	flushContent := func() {
		if len(content) == 0 {
			return
		}
		items = append(items, map[string]any{"role": geminiInteractionRole(message.Role), "content": content})
		content = nil
	}
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			flushContent()
			if native := geminiProviderStateSteps(block.ProviderState); len(native) > 0 {
				items = append(items, native...)
				continue
			}
			if id, name := strings.TrimSpace(block.ToolUseID), strings.TrimSpace(block.ToolName); id != "" && name != "" {
				items = append(items, map[string]any{"type": "function_call", "id": id, "name": name, "arguments": geminiToolArguments(block.Input)})
			}
		case "tool_result":
			flushContent()
			if id := strings.TrimSpace(block.ToolUseID); id != "" {
				output := strings.TrimSpace(block.Output)
				if output == "" {
					output = "(empty output)"
				}
				items = append(items, map[string]any{"type": "function_result", "call_id": id, "output": output, "is_error": block.IsError})
			}
		default:
			content = append(content, geminiInteractionContent([]ContentBlock{block})...)
		}
	}
	flushContent()
	return items
}

func geminiProviderStateSteps(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var state struct {
		Steps []map[string]any `json:"steps"`
	}
	if json.Unmarshal(raw, &state) != nil || len(state.Steps) == 0 {
		return nil
	}
	return state.Steps
}

func geminiInteractionRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "model":
		return "assistant"
	case "system", "developer":
		return "developer"
	default:
		return "user"
	}
}

func geminiInteractionContent(blocks []ContentBlock) []map[string]any {
	content := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			if id, name := strings.TrimSpace(block.ToolUseID), strings.TrimSpace(block.ToolName); id != "" && name != "" {
				call := map[string]any{"type": "function_call", "id": id, "name": name, "arguments": geminiToolArguments(block.Input)}
				if signature := geminiThoughtSignature(block.ProviderState); signature != "" {
					call["thought_signature"] = signature
				}
				content = append(content, call)
			}
		case "tool_result":
			if id := strings.TrimSpace(block.ToolUseID); id != "" {
				output := strings.TrimSpace(block.Output)
				if output == "" {
					output = "(empty output)"
				}
				content = append(content, map[string]any{"type": "function_result", "call_id": id, "output": output, "is_error": block.IsError})
			}
		case "image":
			if len(block.Data) == 0 {
				continue
			}
			mimeType := strings.TrimSpace(block.MIMEType)
			if mimeType == "" {
				mimeType = "image/png"
			}
			content = append(content, map[string]any{"type": "image", "mime_type": mimeType, "data": base64.StdEncoding.EncodeToString(block.Data)})
		default:
			if text := strings.TrimSpace(block.Text); text != "" {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
		}
	}
	return content
}

func geminiToolArguments(raw json.RawMessage) any {
	if len(raw) == 0 || !json.Valid(raw) {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func geminiThoughtSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var state map[string]any
	if json.Unmarshal(raw, &state) != nil {
		return ""
	}
	for _, key := range []string{"thought_signature", "thoughtSignature"} {
		if signature, _ := state[key].(string); strings.TrimSpace(signature) != "" {
			return strings.TrimSpace(signature)
		}
	}
	if steps, ok := state["steps"].([]any); ok {
		for _, rawStep := range steps {
			step, _ := rawStep.(map[string]any)
			for _, key := range []string{"thought_signature", "thoughtSignature"} {
				if signature, _ := step[key].(string); strings.TrimSpace(signature) != "" {
					return strings.TrimSpace(signature)
				}
			}
		}
	}
	return ""
}

func geminiInteractionTools(specs []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		tool := map[string]any{"type": "function", "name": name, "parameters": sanitizeGeminiSchema(spec.Schema)}
		if description := strings.TrimSpace(spec.Description); description != "" {
			tool["description"] = description
		}
		out = append(out, tool)
	}
	return out
}

// sanitizeGeminiSchema retains the compact OpenAPI-compatible subset accepted
// by Gemini function declarations and recursively removes provider-specific
// schema keywords that commonly cause 400 responses.
func sanitizeGeminiSchema(schema any) map[string]any {
	root := openAIToolSchema(schema)
	cleaned := sanitizeGeminiSchemaNode(root, true)
	if cleaned["type"] != "object" {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return cleaned
}

func sanitizeGeminiSchemaNode(node any, root bool) map[string]any {
	input, ok := node.(map[string]any)
	if !ok {
		return map[string]any{"type": "string"}
	}
	typ, _ := input["type"].(string)
	typ = strings.ToLower(strings.TrimSpace(typ))
	switch typ {
	case "object", "array", "string", "integer", "number", "boolean":
	default:
		if _, ok := input["properties"]; ok {
			typ = "object"
		} else if _, ok := input["items"]; ok {
			typ = "array"
		} else {
			typ = "string"
		}
	}
	out := map[string]any{"type": typ}
	if description, ok := input["description"].(string); ok && strings.TrimSpace(description) != "" {
		out["description"] = strings.TrimSpace(description)
	}
	if nullable, ok := input["nullable"].(bool); ok && nullable {
		out["nullable"] = true
	}
	if format, ok := input["format"].(string); ok && strings.TrimSpace(format) != "" && typ == "string" {
		out["format"] = strings.TrimSpace(format)
	}
	if enum := geminiSchemaEnum(input["enum"]); len(enum) > 0 && typ != "object" && typ != "array" {
		out["enum"] = enum
	}
	switch typ {
	case "object":
		properties := map[string]any{}
		if raw, ok := input["properties"].(map[string]any); ok {
			keys := make([]string, 0, len(raw))
			for key := range raw {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				properties[key] = sanitizeGeminiSchemaNode(raw[key], false)
			}
		}
		out["properties"] = properties
		if required, ok := stringSlice(input["required"]); ok {
			filtered := make([]string, 0, len(required))
			for _, name := range required {
				if _, exists := properties[name]; exists {
					filtered = append(filtered, name)
				}
			}
			if len(filtered) > 0 {
				out["required"] = filtered
			}
		}
	case "array":
		out["items"] = sanitizeGeminiSchemaNode(input["items"], false)
	}
	return out
}

func geminiSchemaEnum(value any) []any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case string, float64, bool:
			out = append(out, item)
		}
	}
	return out
}

func handleGeminiInteractionsJSON(out chan<- Event, reader io.Reader) {
	var payload any
	if err := json.NewDecoder(reader).Decode(&payload); err != nil {
		out <- Event{Type: "error", Text: err.Error()}
		return
	}
	acc := geminiEventAccumulator{emittedCalls: make(map[string]bool), pendingCalls: make(map[string]*geminiPendingCall)}
	acc.emit(out, "", payload)
	if !acc.done {
		out <- Event{Type: "error", Text: "Gemini response ended before a terminal event"}
		return
	}
	out <- Event{Type: "done", Done: true, StopReason: acc.stopReason}
}

func handleGeminiInteractionsSSE(out chan<- Event, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	acc := geminiEventAccumulator{emittedCalls: make(map[string]bool), pendingCalls: make(map[string]*geminiPendingCall)}
	var eventType string
	var dataLines []string
	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if strings.TrimSpace(data) == "[DONE]" {
			return false
		}
		var payload any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			out <- Event{Type: "error", Text: fmt.Sprintf("decode Gemini SSE event: %v", err)}
			return false
		}
		acc.emit(out, eventType, payload)
		return true
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !flush() {
				break
			}
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			eventType = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(value))
		}
	}
	if err := scanner.Err(); err != nil {
		out <- Event{Type: "error", Text: err.Error()}
		return
	}
	_ = flush()
	if !acc.done {
		out <- Event{Type: "error", Text: "Gemini stream closed before a terminal event"}
		return
	}
	out <- Event{Type: "done", Done: true, StopReason: acc.stopReason}
}

type geminiPendingCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
}

type geminiEventAccumulator struct {
	emittedCalls map[string]bool
	pendingCalls map[string]*geminiPendingCall
	thoughtSteps []map[string]any
	stopReason   string
	done         bool
}

func (a *geminiEventAccumulator) emit(out chan<- Event, eventType string, payload any) {
	root := geminiEventRoot(payload)
	for _, text := range geminiEventText(root, eventType) {
		out <- Event{Type: "text", Text: text}
	}
	handledStepCall := a.consumeStepEvent(out, root, eventType)
	if !handledStepCall {
		for _, call := range geminiFunctionCalls(root) {
			if call.ID == "" || call.Name == "" || a.emittedCalls[call.ID] {
				continue
			}
			a.emittedCalls[call.ID] = true
			out <- Event{Type: "tool_call", ToolCall: &call}
		}
	}
	if usage, ok := geminiUsage(root); ok {
		out <- Event{Type: "usage", Usage: &usage}
	}
	if reason := geminiStopReason(root, eventType); reason != "" {
		a.stopReason = reason
		a.done = true
	}
	if message := geminiEventError(root); message != "" {
		out <- Event{Type: "error", Text: message}
	}
}

func (a *geminiEventAccumulator) consumeStepEvent(out chan<- Event, root any, sseEventType string) bool {
	value, ok := root.(map[string]any)
	if !ok {
		return false
	}
	eventType := strings.ToLower(geminiString(value, "event_type", "type", "event"))
	if eventType == "" {
		eventType = strings.ToLower(strings.TrimSpace(sseEventType))
	}
	if !strings.Contains(eventType, "step.") {
		return false
	}
	step, _ := value["step"].(map[string]any)
	stepID := geminiString(value, "step_id", "stepId", "id")
	if stepID == "" {
		stepID = geminiString(step, "id", "step_id", "stepId")
	}
	switch {
	case strings.Contains(eventType, "step.start"):
		kind := strings.ToLower(geminiString(step, "type"))
		if kind == "thought" {
			a.thoughtSteps = append(a.thoughtSteps, cloneGeminiMap(step))
			return true
		}
		if kind == "function_call" {
			id := geminiString(step, "id", "call_id", "callId")
			if id == "" {
				id = stepID
			}
			name := geminiString(step, "name", "function_name", "functionName")
			if id == "" || name == "" {
				return true
			}
			pending := &geminiPendingCall{ID: id, Name: name}
			if raw := geminiArgumentFragment(step["arguments"]); raw != "" {
				pending.Arguments.WriteString(raw)
			}
			a.pendingCalls[id] = pending
			return true
		}
	case strings.Contains(eventType, "step.delta"):
		delta, _ := value["delta"].(map[string]any)
		kind := strings.ToLower(geminiString(delta, "type"))
		if strings.Contains(kind, "function_call") || strings.Contains(kind, "arguments") {
			pending := a.pendingCalls[stepID]
			if pending == nil {
				pending = &geminiPendingCall{ID: stepID, Name: geminiString(delta, "name")}
				a.pendingCalls[stepID] = pending
			}
			pending.Arguments.WriteString(geminiArgumentFragment(firstGeminiValue(delta, "delta", "arguments_delta", "arguments", "text")))
			return true
		}
	case strings.Contains(eventType, "step.stop"):
		kind := strings.ToLower(geminiString(step, "type"))
		if kind == "thought" {
			if len(step) > 0 {
				a.thoughtSteps = append(a.thoughtSteps, cloneGeminiMap(step))
			}
			return true
		}
		pending := a.pendingCalls[stepID]
		if pending == nil && kind == "function_call" {
			id := geminiString(step, "id", "call_id", "callId")
			pending = &geminiPendingCall{ID: id, Name: geminiString(step, "name")}
			pending.Arguments.WriteString(geminiArgumentFragment(step["arguments"]))
		}
		if pending == nil || pending.ID == "" || pending.Name == "" || a.emittedCalls[pending.ID] {
			return true
		}
		input := geminiRawArguments(pending.Arguments.String())
		callStep := map[string]any{"type": "function_call", "id": pending.ID, "name": pending.Name, "arguments": geminiToolArguments(input)}
		steps := append([]map[string]any(nil), a.thoughtSteps...)
		steps = append(steps, callStep)
		state, _ := json.Marshal(map[string]any{"steps": steps})
		call := ToolCall{ID: pending.ID, Name: pending.Name, Input: input, ProviderState: state}
		a.emittedCalls[pending.ID] = true
		delete(a.pendingCalls, pending.ID)
		a.thoughtSteps = nil
		out <- Event{Type: "tool_call", ToolCall: &call}
		return true
	}
	return true
}

func firstGeminiValue(value map[string]any, keys ...string) any {
	for _, key := range keys {
		if candidate, ok := value[key]; ok {
			return candidate
		}
	}
	return nil
}

func geminiArgumentFragment(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		encoded, _ := json.Marshal(typed)
		return string(encoded)
	}
}

func cloneGeminiMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if json.Unmarshal(encoded, &clone) != nil {
		return map[string]any{}
	}
	return clone
}

func geminiEventRoot(payload any) any {
	root, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	if nested, ok := root["event"].(map[string]any); ok {
		return nested
	}
	if nested, ok := root["data"].(map[string]any); ok {
		copy := make(map[string]any, len(nested)+1)
		for key, value := range nested {
			copy[key] = value
		}
		if _, hasType := copy["type"]; !hasType {
			if typ := geminiString(root, "event_type", "type", "event"); typ != "" {
				copy["type"] = typ
			}
		}
		return copy
	}
	return root
}

func geminiEventText(root any, eventType string) []string {
	texts := make([]string, 0)
	visitGeminiMaps(root, func(value map[string]any) {
		typ := strings.ToLower(geminiString(value, "type"))
		if typ == "" {
			typ = strings.ToLower(eventType)
		}
		if !strings.Contains(typ, "text") && !strings.Contains(typ, "content.delta") && !strings.Contains(typ, "message.delta") {
			return
		}
		if delta, ok := value["delta"].(string); ok && delta != "" {
			texts = append(texts, delta)
			return
		}
		if delta, ok := value["delta"].(map[string]any); ok {
			if text := geminiString(delta, "text", "content"); text != "" {
				texts = append(texts, text)
				return
			}
		}
		if text := geminiString(value, "text"); text != "" {
			texts = append(texts, text)
		}
	})
	return texts
}

func geminiFunctionCalls(root any) []ToolCall {
	calls := make([]ToolCall, 0)
	visitGeminiMaps(root, func(value map[string]any) {
		candidate := value
		if nested, ok := value["function_call"].(map[string]any); ok {
			candidate = nested
		} else if nested, ok := value["functionCall"].(map[string]any); ok {
			candidate = nested
		}
		typ := strings.ToLower(geminiString(candidate, "type"))
		if typ != "function_call" && typ != "functioncall" && typ != "function-call" && geminiString(candidate, "name") == "" {
			return
		}
		id := geminiString(candidate, "id", "call_id", "callId")
		name := geminiString(candidate, "name", "function_name", "functionName")
		if id == "" || name == "" {
			return
		}
		input := geminiRawArguments(candidate["arguments"])
		if len(input) == 0 {
			input = geminiRawArguments(candidate["args"])
		}
		if len(input) == 0 {
			input = geminiRawArguments(candidate["input"])
		}
		state := json.RawMessage(nil)
		if signature := geminiString(candidate, "thought_signature", "thoughtSignature"); signature != "" {
			steps := []map[string]any{
				{"type": "thought", "thought_signature": signature},
				{"type": "function_call", "id": id, "name": name, "arguments": geminiToolArguments(input)},
			}
			state, _ = json.Marshal(map[string]any{"thought_signature": signature, "steps": steps})
		}
		calls = append(calls, ToolCall{ID: id, Name: name, Input: input, ProviderState: state})
	})
	return calls
}

func geminiRawArguments(value any) json.RawMessage {
	if value == nil {
		return json.RawMessage(`{}`)
	}
	if text, ok := value.(string); ok {
		if json.Valid([]byte(text)) {
			return json.RawMessage(text)
		}
		encoded, _ := json.Marshal(map[string]string{"arguments": text})
		return encoded
	}
	encoded, err := json.Marshal(value)
	if err != nil || !json.Valid(encoded) {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func geminiUsage(root any) (Usage, bool) {
	var result Usage
	found := false
	visitGeminiMaps(root, func(value map[string]any) {
		if _, ok := value["usage"]; ok {
			return
		}
		input, hasInput := geminiInt(value, "input_tokens", "inputTokens", "prompt_token_count", "promptTokenCount")
		output, hasOutput := geminiInt(value, "output_tokens", "outputTokens", "candidates_token_count", "candidatesTokenCount")
		cached, hasCached := geminiInt(value, "cached_input_tokens", "cachedInputTokens", "cached_content_token_count", "cachedContentTokenCount")
		reasoning, hasReasoning := geminiInt(value, "reasoning_tokens", "reasoningTokens", "thoughts_token_count", "thoughtsTokenCount")
		if !hasInput && !hasOutput && !hasCached && !hasReasoning {
			return
		}
		result = Usage{InputTokens: input, OutputTokens: output, CachedInputTokens: cached, ReasoningTokens: reasoning}
		found = true
	})
	return result, found
}

func geminiStopReason(root any, eventType string) string {
	rootMap, _ := root.(map[string]any)
	terminal := geminiOverallCompletionEvent(eventType)
	if !terminal && rootMap != nil {
		terminal = geminiOverallCompletionEvent(geminiString(rootMap, "type", "event", "event_type"))
	}
	if !terminal {
		return ""
	}
	reason := ""
	if rootMap != nil {
		reason = strings.ToLower(strings.TrimSpace(geminiString(rootMap, "stop_reason", "stopReason", "finish_reason", "finishReason")))
	}
	switch reason {
	case "", "stop", "completed", "complete":
		return "completed"
	case "max_tokens", "max_output_tokens", "length":
		return "max_output_tokens"
	default:
		return reason
	}
}

func geminiOverallCompletionEvent(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "completed", "interaction.completed", "interaction.finished", "response.completed", "response.finished":
		return true
	default:
		return false
	}
}

func geminiEventError(root any) string {
	var message string
	visitGeminiMaps(root, func(value map[string]any) {
		if message != "" {
			return
		}
		typ := strings.ToLower(geminiString(value, "type"))
		if typ == "error" || strings.HasSuffix(typ, ".error") {
			message = geminiErrorMessage(value)
		}
	})
	return message
}

func visitGeminiMaps(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			visitGeminiMaps(child, visit)
		}
	case []any:
		for _, child := range typed {
			visitGeminiMaps(child, visit)
		}
	}
}

func geminiString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func geminiInt(value map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		switch number := value[key].(type) {
		case float64:
			return int64(number), true
		case int64:
			return number, true
		case json.Number:
			if parsed, err := number.Int64(); err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}
