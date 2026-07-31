package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"autoto/internal/db"
	"autoto/internal/providers"
)

const supportedAnthropicVersion = "2023-06-01"

type anthropicErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorEnvelope struct {
	Type  string             `json:"type"`
	Error anthropicErrorBody `json:"error"`
}

func writeAnthropicError(w http.ResponseWriter, status int, errorType, message string) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(errorType) == "" {
		errorType = "api_error"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(anthropicErrorEnvelope{Type: "error", Error: anthropicErrorBody{Type: errorType, Message: message}})
}

func writeAnthropicProblem(w http.ResponseWriter, problem *apiProblem) {
	if problem == nil {
		problem = internalProblem()
	}
	errorType := "invalid_request_error"
	switch {
	case problem.Status == http.StatusUnauthorized:
		errorType = "authentication_error"
	case problem.Status == http.StatusForbidden:
		errorType = "permission_error"
	case problem.Status == http.StatusNotFound:
		errorType = "not_found_error"
	case problem.Status == http.StatusRequestEntityTooLarge:
		errorType = "request_too_large"
	case problem.Status == http.StatusTooManyRequests:
		errorType = "rate_limit_error"
	case problem.Status >= http.StatusInternalServerError:
		errorType = "api_error"
	}
	writeAnthropicError(w, problem.Status, errorType, problem.Message)
}

func validateAnthropicVersion(w http.ResponseWriter, r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("anthropic-version")) != supportedAnthropicVersion {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "anthropic-version must be 2023-06-01.")
		return false
	}
	return true
}

type anthropicMessagesRequest struct {
	Model         string                  `json:"model"`
	MaxTokens     *int64                  `json:"max_tokens"`
	System        json.RawMessage         `json:"system"`
	Messages      []anthropicInputMessage `json:"messages"`
	Tools         []anthropicInputTool    `json:"tools"`
	Stream        bool                    `json:"stream"`
	Metadata      json.RawMessage         `json:"metadata"`
	StopSequences json.RawMessage         `json:"stop_sequences"`
	Temperature   json.RawMessage         `json:"temperature"`
	TopP          json.RawMessage         `json:"top_p"`
	TopK          json.RawMessage         `json:"top_k"`
	Thinking      json.RawMessage         `json:"thinking"`
	OutputConfig  json.RawMessage         `json:"output_config"`
	ToolChoice    json.RawMessage         `json:"tool_choice"`
}

type anthropicInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicInputTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	Type        string          `json:"type,omitempty"`
}

type convertedAnthropicRequest struct {
	ProviderRequest          providers.GenerateRequest
	HasImages                bool
	HasNativeReasoningBlocks bool
}

type anthropicMessageResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Model        string                 `json:"model"`
	Content      []anthropicOutputBlock `json:"content"`
	StopReason   *string                `json:"stop_reason"`
	StopSequence any                    `json:"stop_sequence"`
	Usage        anthropicUsage         `json:"usage"`
}

type anthropicOutputBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

func (s *Service) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateAnthropicRequest(w, r)
	if !ok || !validateAnthropicVersion(w, r) {
		return
	}
	lease, err := s.limits.acquireIngress(key)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "Concurrency limit exceeded.")
		return
	}
	defer lease.Release()

	var request anthropicMessagesRequest
	if problem := decodeJSONBody(w, r, s.maxRequestBytes, &request); problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	converted, problem := convertAnthropicRequest(request, true)
	if problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	resolved, problem := s.prepareProviderRequest(r.Context(), key, request.Model, &converted.ProviderRequest, converted.HasImages, lease, generationParameterNames{
		Tools:           "tools",
		Images:          "messages",
		ReasoningEffort: "thinking",
		ServiceTier:     "service_tier",
		MaxOutputTokens: "max_tokens",
	})
	if problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	if problem := validateAnthropicNativeReasoningBlocks(resolved, converted); problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	if request.Stream {
		s.streamAnthropicMessage(w, r, key, resolved, converted)
		return
	}
	s.completeAnthropicMessage(w, r, key, resolved, converted)
}

func (s *Service) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateAnthropicRequest(w, r)
	if !ok || !validateAnthropicVersion(w, r) {
		return
	}
	lease, err := s.limits.acquireIngress(key)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "Concurrency limit exceeded.")
		return
	}
	defer lease.Release()

	var request anthropicMessagesRequest
	if problem := decodeJSONBody(w, r, s.maxRequestBytes, &request); problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	converted, problem := convertAnthropicRequest(request, false)
	if problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	resolved, problem := s.resolveAndValidateProviderRequest(r.Context(), key, request.Model, &converted.ProviderRequest, converted.HasImages, generationParameterNames{
		Tools:           "tools",
		Images:          "messages",
		ReasoningEffort: "thinking",
		ServiceTier:     "service_tier",
	})
	if problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	if problem := validateAnthropicNativeReasoningBlocks(resolved, converted); problem != nil {
		writeAnthropicProblem(w, problem)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]int64{"input_tokens": estimateGatewayInputTokens(converted.ProviderRequest)})
}

func convertAnthropicRequest(request anthropicMessagesRequest, requireMaxTokens bool) (convertedAnthropicRequest, *apiProblem) {
	if strings.TrimSpace(request.Model) == "" {
		return convertedAnthropicRequest{}, invalidParam("model", "A model is required.")
	}
	if requireMaxTokens && request.MaxTokens == nil {
		return convertedAnthropicRequest{}, invalidParam("max_tokens", "max_tokens is required.")
	}
	if request.MaxTokens != nil && (*request.MaxTokens <= 0 || *request.MaxTokens > 1_000_000) {
		return convertedAnthropicRequest{}, invalidParam("max_tokens", "max_tokens must be between 1 and 1000000.")
	}
	// Standard inference parameters (temperature, top_p, top_k, stop_sequences,
	// tool_choice, metadata) are accepted and silently ignored — the gateway
	// routes requests without forwarding sampling knobs to providers directly.
	if len(request.Messages) == 0 || len(request.Messages) > 10000 {
		return convertedAnthropicRequest{}, invalidParam("messages", "messages must contain between 1 and 10000 items.")
	}
	thinkingEffort, thinkingBudget, problem := convertAnthropicThinking(request.Thinking, request.OutputConfig, request.MaxTokens)
	if problem != nil {
		return convertedAnthropicRequest{}, problem
	}

	systemPrompt, problem := convertAnthropicSystem(request.System)
	if problem != nil {
		return convertedAnthropicRequest{}, problem
	}
	tools, problem := convertAnthropicTools(request.Tools)
	if problem != nil {
		return convertedAnthropicRequest{}, problem
	}
	messages := make([]providers.Message, 0, len(request.Messages))
	hasImages := false
	hasNativeReasoningBlocks := false
	for index, message := range request.Messages {
		converted, images, nativeReasoning, problem := convertAnthropicMessage(message, index, request.Model)
		if problem != nil {
			return convertedAnthropicRequest{}, problem
		}
		messages = append(messages, converted)
		hasImages = hasImages || images
		hasNativeReasoningBlocks = hasNativeReasoningBlocks || nativeReasoning
	}
	providerRequest := providers.GenerateRequest{
		SystemPrompt:          systemPrompt,
		Messages:              messages,
		Tools:                 tools,
		ReasoningEffort:       thinkingEffort,
		ReasoningBudgetTokens: thinkingBudget,
	}
	if request.MaxTokens != nil {
		providerRequest.MaxOutputTokens = *request.MaxTokens
	}
	return convertedAnthropicRequest{ProviderRequest: providerRequest, HasImages: hasImages, HasNativeReasoningBlocks: hasNativeReasoningBlocks}, nil
}

func convertAnthropicThinking(thinkingRaw, outputConfigRaw json.RawMessage, maxTokens *int64) (string, int64, *apiProblem) {
	var outputConfig struct {
		Effort json.RawMessage `json:"effort"`
		Format json.RawMessage `json:"format"`
	}
	outputConfigPresent := rawJSONPresent(outputConfigRaw)
	if outputConfigPresent {
		if err := json.Unmarshal(outputConfigRaw, &outputConfig); err != nil {
			return "", 0, invalidParam("output_config", "output_config must be an object.")
		}
		if rawJSONPresent(outputConfig.Format) {
			return "", 0, unsupportedParam("output_config.format")
		}
	}

	if !rawJSONPresent(thinkingRaw) {
		if outputConfigPresent {
			return "", 0, invalidParam("output_config", "output_config is only supported when thinking.type is adaptive.")
		}
		return "", 0, nil
	}
	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens *int64 `json:"budget_tokens"`
	}
	if err := json.Unmarshal(thinkingRaw, &thinking); err != nil {
		return "", 0, invalidParam("thinking", "thinking must be an object.")
	}
	switch strings.ToLower(strings.TrimSpace(thinking.Type)) {
	case "disabled":
		if thinking.BudgetTokens != nil {
			return "", 0, invalidParam("thinking.budget_tokens", "thinking.budget_tokens is only supported when thinking.type is enabled.")
		}
		if outputConfigPresent {
			return "", 0, invalidParam("output_config", "output_config is only supported when thinking.type is adaptive.")
		}
		return "", 0, nil
	case "enabled":
		if outputConfigPresent {
			return "", 0, invalidParam("output_config", "output_config is only supported when thinking.type is adaptive.")
		}
		if thinking.BudgetTokens == nil {
			return "", 0, invalidParam("thinking.budget_tokens", "thinking.budget_tokens is required when thinking.type is enabled.")
		}
		if *thinking.BudgetTokens < 1024 {
			return "", 0, invalidParam("thinking.budget_tokens", "thinking.budget_tokens must be at least 1024.")
		}
		if maxTokens == nil {
			return "", 0, invalidParam("max_tokens", "max_tokens is required when thinking.type is enabled.")
		}
		if *thinking.BudgetTokens >= *maxTokens {
			return "", 0, invalidParam("thinking.budget_tokens", "thinking.budget_tokens must be less than max_tokens.")
		}
		return anthropicThinkingEffortFromBudget(*thinking.BudgetTokens, *maxTokens), *thinking.BudgetTokens, nil
	case "adaptive":
		if thinking.BudgetTokens != nil {
			return "", 0, invalidParam("thinking.budget_tokens", "thinking.budget_tokens is only supported when thinking.type is enabled.")
		}
		effort := "high"
		if rawJSONPresent(outputConfig.Effort) {
			if err := json.Unmarshal(outputConfig.Effort, &effort); err != nil {
				return "", 0, invalidParam("output_config.effort", "output_config.effort must be low, medium, or high.")
			}
			effort = strings.ToLower(strings.TrimSpace(effort))
			switch effort {
			case "low", "medium", "high":
			default:
				return "", 0, invalidParam("output_config.effort", "output_config.effort must be low, medium, or high.")
			}
		}
		return effort, 0, nil
	default:
		return "", 0, invalidParam("thinking.type", "thinking.type must be enabled, adaptive, or disabled.")
	}
}

func anthropicThinkingEffortFromBudget(budget, maxTokens int64) string {
	switch {
	case budget*3 <= maxTokens:
		return "low"
	case budget*3 <= maxTokens*2:
		return "medium"
	default:
		return "high"
	}
}

func validateAnthropicNativeReasoningBlocks(resolved resolvedModel, converted convertedAnthropicRequest) *apiProblem {
	if converted.HasNativeReasoningBlocks && !providers.CapabilitiesFor(resolved.Provider).NativeReasoningBlocks {
		return invalidParam("messages", "The requested model does not support native reasoning blocks.")
	}
	return nil
}

func convertAnthropicSystem(raw json.RawMessage) (string, *apiProblem) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", invalidParam("system", "system must be a string or array of text blocks.")
		}
		return strings.TrimSpace(text), nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil {
		return "", invalidParam("system", "system must be a string or array of text blocks.")
	}
	texts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		if block.Type != "text" {
			return "", invalidParam(fmt.Sprintf("system[%d].type", index), "Only text system blocks are supported.")
		}
		if strings.TrimSpace(block.Text) != "" {
			texts = append(texts, strings.TrimSpace(block.Text))
		}
	}
	return strings.Join(texts, "\n\n"), nil
}

func convertAnthropicTools(tools []anthropicInputTool) ([]providers.ToolSpec, *apiProblem) {
	if len(tools) > 128 {
		return nil, invalidParam("tools", "Too many tools were provided.")
	}
	converted := make([]providers.ToolSpec, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool.Type != "" && tool.Type != "custom" {
			return nil, invalidParam(fmt.Sprintf("tools[%d].type", index), "Only custom function tools are supported.")
		}
		name := strings.TrimSpace(tool.Name)
		if !toolNamePattern.MatchString(name) {
			return nil, invalidParam(fmt.Sprintf("tools[%d].name", index), "Tool name is invalid.")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, invalidParam(fmt.Sprintf("tools[%d].name", index), "Tool names must be unique.")
		}
		seen[name] = struct{}{}
		schema, _, problem := responseToolSchema(tool.InputSchema, fmt.Sprintf("tools[%d].input_schema", index))
		if problem != nil {
			return nil, problem
		}
		converted = append(converted, providers.ToolSpec{Name: name, Description: strings.TrimSpace(tool.Description), Schema: schema})
	}
	return converted, nil
}

func convertAnthropicMessage(message anthropicInputMessage, index int, model string) (providers.Message, bool, bool, *apiProblem) {
	param := fmt.Sprintf("messages[%d]", index)
	role := strings.ToLower(strings.TrimSpace(message.Role))
	if role != "user" && role != "assistant" {
		return providers.Message{}, false, false, invalidParam(param+".role", "Message role must be user or assistant.")
	}
	trimmed := bytes.TrimSpace(message.Content)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return providers.Message{}, false, false, invalidParam(param+".content", "Message content is required.")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil || text == "" {
			return providers.Message{}, false, false, invalidParam(param+".content", "Message content must be a non-empty string or content block array.")
		}
		return providers.Message{Role: role, Content: text, Blocks: []providers.ContentBlock{{Type: "text", Text: text}}}, false, false, nil
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(trimmed, &rawBlocks); err != nil || len(rawBlocks) == 0 {
		return providers.Message{}, false, false, invalidParam(param+".content", "Message content must be a non-empty string or content block array.")
	}
	blocks := make([]providers.ContentBlock, 0, len(rawBlocks))
	texts := make([]string, 0, len(rawBlocks))
	hasImages := false
	hasNativeReasoning := false
	for blockIndex, rawBlock := range rawBlocks {
		block, text, image, nativeReasoning, problem := convertAnthropicContentBlock(rawBlock, role, model, fmt.Sprintf("%s.content[%d]", param, blockIndex))
		if problem != nil {
			return providers.Message{}, false, false, problem
		}
		blocks = append(blocks, block)
		if text != "" {
			texts = append(texts, text)
		}
		hasImages = hasImages || image
		hasNativeReasoning = hasNativeReasoning || nativeReasoning
	}
	return providers.Message{Role: role, Content: strings.Join(texts, "\n"), Blocks: blocks}, hasImages, hasNativeReasoning, nil
}

func convertAnthropicContentBlock(raw json.RawMessage, role, model, param string) (providers.ContentBlock, string, bool, bool, *apiProblem) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return providers.ContentBlock{}, "", false, false, invalidParam(param, "Content block is invalid.")
	}
	switch strings.ToLower(strings.TrimSpace(header.Type)) {
	case "text":
		var block struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &block); err != nil || block.Text == "" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param+".text", "Text block content is required.")
		}
		return providers.ContentBlock{Type: "text", Text: block.Text}, block.Text, false, false, nil
	case providers.ContentBlockTypeThinking:
		if role != "assistant" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "thinking blocks are allowed only in assistant messages.")
		}
		var block struct {
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}
		if err := json.Unmarshal(raw, &block); err != nil || block.Thinking == "" || block.Signature == "" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "thinking requires non-empty thinking and signature fields.")
		}
		return providers.NewAnthropicThinkingContentBlock(model, block.Thinking, block.Signature), "", false, true, nil
	case providers.ContentBlockTypeRedactedThinking:
		if role != "assistant" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "redacted_thinking blocks are allowed only in assistant messages.")
		}
		var block struct {
			Data string `json:"data"`
		}
		if err := json.Unmarshal(raw, &block); err != nil || block.Data == "" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param+".data", "redacted_thinking data is required.")
		}
		return providers.NewAnthropicRedactedThinkingContentBlock(model, block.Data), "", false, true, nil
	case "image":
		if role != "user" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "Image blocks are allowed only in user messages.")
		}
		var block struct {
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
				URL       string `json:"url"`
			} `json:"source"`
		}
		if err := json.Unmarshal(raw, &block); err != nil || block.Source.Type != "base64" || strings.TrimSpace(block.Source.URL) != "" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param+".source", "Only base64 image sources are supported; remote images are unavailable.")
		}
		mimeType, data, problem := decodeAnthropicBase64Image(block.Source.MediaType, block.Source.Data, param+".source")
		if problem != nil {
			return providers.ContentBlock{}, "", false, false, problem
		}
		return providers.ContentBlock{Type: "image", MIMEType: mimeType, Data: data}, "", true, false, nil
	case "tool_use":
		if role != "assistant" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "tool_use blocks are allowed only in assistant messages.")
		}
		var block struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(raw, &block); err != nil {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "tool_use block is invalid.")
		}
		id := strings.TrimSpace(block.ID)
		name := strings.TrimSpace(block.Name)
		input, problem := anthropicToolInput(block.Input, param+".input")
		if problem != nil {
			return providers.ContentBlock{}, "", false, false, problem
		}
		if id == "" || !toolNamePattern.MatchString(name) {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "tool_use requires an id and valid name.")
		}
		return providers.ContentBlock{Type: "tool_use", ToolUseID: id, ToolName: name, Input: input}, "", false, false, nil
	case "tool_result":
		if role != "user" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "tool_result blocks are allowed only in user messages.")
		}
		var block struct {
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if err := json.Unmarshal(raw, &block); err != nil || strings.TrimSpace(block.ToolUseID) == "" {
			return providers.ContentBlock{}, "", false, false, invalidParam(param, "tool_result requires tool_use_id and content.")
		}
		output, problem := anthropicToolResultText(block.Content, param+".content")
		if problem != nil {
			return providers.ContentBlock{}, "", false, false, problem
		}
		return providers.ContentBlock{Type: "tool_result", ToolUseID: strings.TrimSpace(block.ToolUseID), Output: output, IsError: block.IsError}, output, false, false, nil
	default:
		return providers.ContentBlock{}, "", false, false, invalidParam(param+".type", "Unsupported content block type. Files, documents, audio, and remote media are unavailable.")
	}
}

func decodeAnthropicBase64Image(mediaType, encoded, param string) (string, []byte, *apiProblem) {
	mediaType = strings.TrimSpace(mediaType)
	if !strings.HasPrefix(strings.ToLower(mediaType), "image/") || len(mediaType) > 100 {
		return "", nil, invalidParam(param+".media_type", "Image media type is invalid.")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(data) == 0 || len(data) > 16<<20 {
		return "", nil, invalidParam(param+".data", "Image base64 data is invalid or too large.")
	}
	return mediaType, data, nil
}

func anthropicToolInput(raw json.RawMessage, param string) (json.RawMessage, *apiProblem) {
	if !rawJSONPresent(raw) {
		return json.RawMessage(`{}`), nil
	}
	var input map[string]any
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, invalidParam(param, "Tool input must be a JSON object.")
	}
	normalized, err := json.Marshal(input)
	if err != nil {
		return nil, internalProblem()
	}
	return normalized, nil
}

func anthropicToolResultText(raw json.RawMessage, param string) (string, *apiProblem) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", invalidParam(param, "Tool result content is required.")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", invalidParam(param, "Tool result content is invalid.")
		}
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &blocks); err != nil || len(blocks) == 0 {
		return "", invalidParam(param, "Tool result content must be text or an array of text blocks.")
	}
	texts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		if block.Type != "text" {
			return "", invalidParam(fmt.Sprintf("%s[%d].type", param, index), "Only text tool result content is supported.")
		}
		texts = append(texts, block.Text)
	}
	return strings.Join(texts, "\n"), nil
}

func (s *Service) completeAnthropicMessage(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedAnthropicRequest) {
	messageID := newGatewayPublicID("msg_")
	recorder := newGatewayExecutionRecorder(s, key, resolved)
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		_ = recorder.record(gatewayFailureUpstreamStart)
		writeAnthropicProviderStartError(w, err)
		return
	}
	if events == nil {
		_ = recorder.record(gatewayFailureProviderNoEventFeed)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "The upstream model request failed.")
		return
	}

	var output anthropicOutputAccumulator
	for {
		select {
		case <-r.Context().Done():
			_ = recorder.record(gatewayFailureRequestCanceled)
			return
		case event, ok := <-events:
			if !ok {
				_ = recorder.record(gatewayFailureUpstreamEnded)
				writeAnthropicError(w, http.StatusBadGateway, "api_error", "The upstream model request failed.")
				return
			}
			recorder.capture(event)
			switch event.Type {
			case "text":
				if event.Text != "" {
					recorder.markOutput()
					output.addText(event.Text)
				}
			case "tool_call":
				if event.ToolCall != nil {
					recorder.markOutput()
					output.addTool(*event.ToolCall)
				}
			case "content_block":
				if event.ContentBlock != nil {
					recorder.markOutput()
					output.addSnapshot(event.BlockIndex, *event.ContentBlock)
				}
			case "error":
				_ = recorder.record(gatewayFailureUpstreamEvent)
				writeAnthropicError(w, http.StatusBadGateway, "api_error", "The upstream model request failed.")
				return
			case "done":
				if err := recorder.record(""); err != nil {
					writeAnthropicProblem(w, internalProblem())
					return
				}
				stopReason := anthropicStopReason(recorder.execution.StopReason, output.hasTools)
				response := anthropicMessageResponse{
					ID: messageID, Type: "message", Role: "assistant", Model: resolved.Alias,
					Content: output.blocksOrEmpty(), StopReason: &stopReason, StopSequence: nil, Usage: anthropicUsageValue(recorder.execution.Usage),
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(response)
				return
			}
		}
	}
}

// streamAnthropicMessage is the SSE path. It was lost when the non-streaming
// handler was rewritten around anthropicOutputAccumulator -- handleAnthropicMessages
// still dispatched to it, so the package stopped compiling. Restored here with
// the reasoning blocks the accumulator gained, so a streaming client sees the
// same thinking blocks a non-streaming one does.
func (s *Service) streamAnthropicMessage(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedAnthropicRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicProblem(w, internalProblem())
		return
	}
	messageID := newGatewayPublicID("msg_")
	recorder := newGatewayExecutionRecorder(s, key, resolved)
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		_ = recorder.record(gatewayFailureUpstreamStart)
		writeAnthropicProviderStartError(w, err)
		return
	}
	if events == nil {
		_ = recorder.record(gatewayFailureProviderNoEventFeed)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "The upstream model request failed.")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	startMessage := anthropicMessageResponse{ID: messageID, Type: "message", Role: "assistant", Model: resolved.Alias, Content: []anthropicOutputBlock{}, StopReason: nil, StopSequence: nil, Usage: anthropicUsage{}}
	if err := writeNamedSSEJSON(w, flusher, "message_start", map[string]any{"type": "message_start", "message": startMessage}); err != nil {
		_ = recorder.record(gatewayFailureClientDisconnected)
		return
	}

	blockIndex := 0
	textActive := false
	hasToolCalls := false
	// Closes the open text block, if any, so the next block starts clean. Shared
	// by the tool_call and content_block cases, which both interrupt a text run.
	closeTextBlock := func() bool {
		if !textActive {
			return true
		}
		if err := writeNamedSSEJSON(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex}); err != nil {
			_ = recorder.record(gatewayFailureClientDisconnected)
			return false
		}
		blockIndex++
		textActive = false
		return true
	}
	for {
		select {
		case <-r.Context().Done():
			_ = recorder.record(gatewayFailureRequestCanceled)
			return
		case event, ok := <-events:
			if !ok {
				_ = writeAnthropicStreamError(w, flusher, "api_error", "The upstream model request failed.")
				_ = recorder.record(gatewayFailureUpstreamEnded)
				return
			}
			recorder.capture(event)
			switch event.Type {
			case "text":
				if event.Text == "" {
					continue
				}
				recorder.markOutput()
				if !textActive {
					if err := writeNamedSSEJSON(w, flusher, "content_block_start", map[string]any{"type": "content_block_start", "index": blockIndex, "content_block": anthropicOutputBlock{Type: "text", Text: ""}}); err != nil {
						_ = recorder.record(gatewayFailureClientDisconnected)
						return
					}
					textActive = true
				}
				if err := writeNamedSSEJSON(w, flusher, "content_block_delta", map[string]any{"type": "content_block_delta", "index": blockIndex, "delta": map[string]any{"type": "text_delta", "text": event.Text}}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
			case "content_block":
				// Only the reasoning blocks are emitted here: text and tool_use also
				// arrive as snapshots, but their deltas were already streamed above and
				// re-sending them would duplicate the answer.
				if event.ContentBlock == nil {
					continue
				}
				block := anthropicReasoningOutputBlock(*event.ContentBlock)
				if block == nil {
					continue
				}
				recorder.markOutput()
				if !closeTextBlock() {
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "content_block_start", map[string]any{"type": "content_block_start", "index": blockIndex, "content_block": *block}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				blockIndex++
			case "tool_call":
				if event.ToolCall == nil {
					continue
				}
				recorder.markOutput()
				hasToolCalls = true
				if !closeTextBlock() {
					return
				}
				call := sanitizedProviderToolCall(*event.ToolCall)
				toolBlock := anthropicOutputBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: json.RawMessage(`{}`)}
				if err := writeNamedSSEJSON(w, flusher, "content_block_start", map[string]any{"type": "content_block_start", "index": blockIndex, "content_block": toolBlock}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "content_block_delta", map[string]any{"type": "content_block_delta", "index": blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": call.Arguments}}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				blockIndex++
			case "error":
				_ = writeAnthropicStreamError(w, flusher, "api_error", "The upstream model request failed.")
				_ = recorder.record(gatewayFailureUpstreamEvent)
				return
			case "done":
				if err := recorder.record(""); err != nil {
					_ = writeAnthropicStreamError(w, flusher, "api_error", "The Gateway could not process the request.")
					return
				}
				if textActive {
					if err := writeNamedSSEJSON(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": blockIndex}); err != nil {
						return
					}
				}
				stopReason := anthropicStopReason(recorder.execution.StopReason, hasToolCalls)
				if err := writeNamedSSEJSON(w, flusher, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int64{"output_tokens": recorder.execution.Usage.OutputTokens}}); err != nil {
					return
				}
				_ = writeNamedSSEJSON(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
				return
			}
		}
	}
}

// anthropicReasoningOutputBlock renders a provider reasoning block in the wire
// shape, or nil for any block that is not one. Both the streaming and
// non-streaming paths go through it so they cannot drift apart.
func anthropicReasoningOutputBlock(block providers.ContentBlock) *anthropicOutputBlock {
	_, signature, data, ok := providers.AnthropicReasoningContentBlockState(block)
	if !ok {
		return nil
	}
	switch block.Type {
	case providers.ContentBlockTypeThinking:
		if signature == "" {
			return nil
		}
		return &anthropicOutputBlock{Type: block.Type, Thinking: block.ReasoningText, Signature: signature}
	case providers.ContentBlockTypeRedactedThinking:
		if data == "" {
			return nil
		}
		return &anthropicOutputBlock{Type: block.Type, Data: data}
	}
	return nil
}

func writeAnthropicProviderStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, providers.ErrGatewayOAuthUnsupported) {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", "The requested model is not permitted for Gateway use.")
		return
	}
	writeAnthropicError(w, http.StatusBadGateway, "api_error", "The upstream model request failed.")
}

func writeAnthropicStreamError(w http.ResponseWriter, flusher http.Flusher, errorType, message string) error {
	return writeNamedSSEJSON(w, flusher, "error", anthropicErrorEnvelope{Type: "error", Error: anthropicErrorBody{Type: errorType, Message: message}})
}

type anthropicOutputAccumulator struct {
	blocks    []anthropicOutputBlock
	textIndex int
	hasText   bool
	hasTools  bool
}

func (a *anthropicOutputAccumulator) addText(text string) {
	if text == "" {
		return
	}
	if !a.hasText {
		a.blocks = append(a.blocks, anthropicOutputBlock{Type: "text", Text: text})
		a.textIndex = len(a.blocks) - 1
		a.hasText = true
		return
	}
	a.blocks[a.textIndex].Text += text
}

func (a *anthropicOutputAccumulator) addTool(call providers.ToolCall) {
	sanitized := sanitizedProviderToolCall(call)
	a.blocks = append(a.blocks, anthropicOutputBlock{Type: "tool_use", ID: sanitized.ID, Name: sanitized.Name, Input: json.RawMessage(sanitized.Arguments)})
	a.hasText = false
	a.hasTools = true
}

// addSnapshot records a completed provider content block. Text and tool_use
// arrive here as well, but they were already assembled from the delta stream by
// addText/addTool, so taking them again would emit the answer twice; only the
// reasoning blocks carry information the deltas do not.
func (a *anthropicOutputAccumulator) addSnapshot(index int64, block providers.ContentBlock) {
	_ = index
	_, signature, data, ok := providers.AnthropicReasoningContentBlockState(block)
	if !ok {
		return
	}
	switch block.Type {
	case providers.ContentBlockTypeThinking:
		if signature == "" {
			return
		}
		a.blocks = append(a.blocks, anthropicOutputBlock{Type: block.Type, Thinking: block.ReasoningText, Signature: signature})
	case providers.ContentBlockTypeRedactedThinking:
		if data == "" {
			return
		}
		a.blocks = append(a.blocks, anthropicOutputBlock{Type: block.Type, Data: data})
	default:
		return
	}
	// A reasoning block closes any open text run, so the next text delta starts
	// its own block rather than being appended after the thinking.
	a.hasText = false
}

func (a *anthropicOutputAccumulator) blocksOrEmpty() []anthropicOutputBlock {
	if a.blocks == nil {
		return []anthropicOutputBlock{}
	}
	return a.blocks
}

func anthropicStopReason(stopReason string, hasTools bool) string {
	if hasTools {
		return "tool_use"
	}
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens", "max_output_tokens", "length", "incomplete":
		return "max_tokens"
	case "stop_sequence":
		return "stop_sequence"
	case "tool_use", "tool_calls":
		return "tool_use"
	default:
		return "end_turn"
	}
}

func anthropicUsageValue(usage providers.Usage) anthropicUsage {
	return anthropicUsage{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CacheReadInputTokens: usage.CachedInputTokens}
}

func estimateGatewayInputTokens(request providers.GenerateRequest) int64 {
	total := estimateGatewayTextTokens(request.SystemPrompt) + 3
	if len(request.Tools) > 0 {
		if data, err := json.Marshal(request.Tools); err == nil {
			total += estimateGatewayTextTokens(string(data)) + int64(len(request.Tools))*8
		}
	}
	for _, message := range request.Messages {
		total += 4 + estimateGatewayTextTokens(message.Role)
		if len(message.Blocks) == 0 {
			total += estimateGatewayTextTokens(message.Content)
		}
		for _, block := range message.Blocks {
			total += estimateGatewayTextTokens(block.Type) + estimateGatewayTextTokens(block.Text) + estimateGatewayTextTokens(block.Output) + estimateGatewayTextTokens(block.ToolName) + estimateGatewayTextTokens(block.ToolUseID)
			if len(block.Input) > 0 {
				total += estimateGatewayTextTokens(string(block.Input))
			}
			if block.Type == "image" && len(block.Data) > 0 {
				total += 1024 + int64((len(block.Data)+511)/512)
			}
		}
	}
	if total < 1 {
		return 1
	}
	return total
}

func estimateGatewayTextTokens(text string) int64 {
	asciiRunes := int64(0)
	nonASCII := int64(0)
	for _, value := range text {
		if value <= 0x7f {
			asciiRunes++
		} else {
			nonASCII++
		}
	}
	return (asciiRunes+3)/4 + nonASCII
}
