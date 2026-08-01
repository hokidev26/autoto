package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/pricing"
	"autoto/internal/providers"
)

type apiProblem struct {
	Status  int
	Code    string
	Message string
	Type    string
	Param   string
}

func invalidParam(param, message string) *apiProblem {
	return &apiProblem{Status: http.StatusBadRequest, Code: "invalid_parameter", Type: "invalid_request_error", Param: param, Message: message}
}

func unsupportedParam(param string) *apiProblem {
	return &apiProblem{Status: http.StatusBadRequest, Code: "unsupported_parameter", Type: "invalid_request_error", Param: param, Message: fmt.Sprintf("The parameter %q is not supported by this Gateway.", param)}
}

func internalProblem() *apiProblem {
	return &apiProblem{Status: http.StatusInternalServerError, Code: "gateway_internal_error", Type: "server_error", Message: "The Gateway could not process the request."}
}

type apiErrorBody struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}

func writeProblem(w http.ResponseWriter, problem *apiProblem) {
	if problem == nil {
		problem = internalProblem()
	}
	writeAPIError(w, problem.Status, problem.Code, problem.Message, problem.Type, problem.Param)
}

func writeAPIError(w http.ResponseWriter, status int, code, message, errorType, param string) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if errorType == "" {
		errorType = "server_error"
	}
	var parameter *string
	if param != "" {
		parameter = &param
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorEnvelope{Error: apiErrorBody{Message: message, Type: errorType, Param: parameter, Code: code}})
}

type chatCompletionRequest struct {
	Model               string             `json:"model"`
	Messages            []chatMessage      `json:"messages"`
	Stream              bool               `json:"stream"`
	StreamOptions       *chatStreamOptions `json:"stream_options"`
	Tools               []chatTool         `json:"tools"`
	ToolChoice          json.RawMessage    `json:"tool_choice"`
	ParallelToolCalls   *bool              `json:"parallel_tool_calls"`
	ReasoningEffort     string             `json:"reasoning_effort"`
	MaxTokens           *int64             `json:"max_tokens"`
	MaxCompletionTokens *int64             `json:"max_completion_tokens"`
	Temperature         *float64           `json:"temperature"`
	TopP                *float64           `json:"top_p"`
	N                   *int               `json:"n"`
	Logprobs            *bool              `json:"logprobs"`
	ResponseFormat      json.RawMessage    `json:"response_format"`
	Stop                json.RawMessage    `json:"stop"`
	Seed                *int64             `json:"seed"`
	PresencePenalty     *float64           `json:"presence_penalty"`
	FrequencyPenalty    *float64           `json:"frequency_penalty"`
	ServiceTier         string             `json:"service_tier"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Name       string          `json:"name"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Arguments   string          `json:"arguments"`
}

type convertedChatRequest struct {
	ProviderRequest providers.GenerateRequest
	IncludeUsage    bool
	HasImages       bool
	// IgnoredParameters names what the caller sent that cannot be forwarded.
	// See IgnoredParametersHeader.
	IgnoredParameters []string
}

type modelListResponse struct {
	Object string          `json:"object"`
	Data   []modelListItem `json:"data"`
}

type modelListItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (s *Service) handleModels(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateRequest(w, r)
	if !ok {
		return
	}
	created := s.now().Unix()
	items := make([]modelListItem, 0)

	// Configured aliases first (support remapping / renaming).
	aliases, err := s.store.ListGatewayModels(r.Context())
	if err != nil {
		writeProblem(w, internalProblem())
		return
	}
	coveredTargets := make(map[string]struct{}, len(aliases))
	for _, model := range aliases {
		if !gatewayKeyAllowsModel(key, model.Alias) {
			continue
		}
		if _, problem := s.resolveStoredModel(r.Context(), model); problem != nil {
			continue
		}
		items = append(items, modelListItem{ID: model.Alias, Object: "model", Created: created, OwnedBy: "autoto"})
		coveredTargets[model.TargetModel] = struct{}{}
	}

	// Native provider models — no alias required.
	// Each permitted provider's own model list is exposed as "provider:model".
	for _, providerName := range s.providers.Names() {
		permitted := s.providerPermitted(r.Context(), providerName)
		slog.Debug("gateway handleModels provider check", "provider", providerName, "permitted", permitted)
		if !permitted {
			continue
		}
		provider, ok := s.providers.Get(providerName)
		if !ok || provider == nil {
			continue
		}
		providerModels, err := provider.ListModels(r.Context())
		slog.Debug("gateway handleModels ListModels", "provider", providerName, "models", providerModels, "err", err)
		if err != nil {
			continue
		}
		for _, m := range providerModels {
			nativeID := providerName + ":" + m
			if _, covered := coveredTargets[nativeID]; covered {
				continue // already exposed via an alias
			}
			if !gatewayKeyAllowsModel(key, nativeID) {
				continue
			}
			items = append(items, modelListItem{ID: nativeID, Object: "model", Created: created, OwnedBy: "autoto"})
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(modelListResponse{Object: "list", Data: items})
}

func (s *Service) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	key, ok := s.authenticateRequest(w, r)
	if !ok {
		return
	}
	lease, err := s.limits.acquireIngress(key)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, "concurrency_limit_exceeded", "Concurrency limit exceeded.", "rate_limit_error", "")
		return
	}
	defer lease.Release()

	request, problem := decodeChatCompletionRequest(w, r, s.maxRequestBytes)
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	converted, problem := convertChatCompletionRequest(request)
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	resolved, problem := s.prepareProviderRequest(r.Context(), key, request.Model, &converted.ProviderRequest, converted.HasImages, lease, generationParameterNames{
		Tools:           "tools",
		Images:          "messages",
		ReasoningEffort: "reasoning_effort",
		ServiceTier:     "service_tier",
		MaxOutputTokens: "max_completion_tokens",
	})
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	if request.Stream {
		s.streamChatCompletion(w, r, key, resolved, converted)
		return
	}
	s.completeChatCompletion(w, r, key, resolved, converted)
}

func decodeChatCompletionRequest(w http.ResponseWriter, r *http.Request, maxBytes int64) (chatCompletionRequest, *apiProblem) {
	var request chatCompletionRequest
	if problem := decodeJSONBody(w, r, maxBytes, &request); problem != nil {
		return chatCompletionRequest{}, problem
	}
	return request, nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) *apiProblem {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRequestBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &apiProblem{Status: http.StatusRequestEntityTooLarge, Code: "request_too_large", Type: "invalid_request_error", Message: "Request body is too large."}
		}
		return invalidParam("body", "Request body must be valid JSON.")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return invalidParam("body", "Request body must contain exactly one JSON object.")
	}
	return nil
}

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func convertChatCompletionRequest(request chatCompletionRequest) (convertedChatRequest, *apiProblem) {
	if strings.TrimSpace(request.Model) == "" {
		return convertedChatRequest{}, invalidParam("model", "A model is required.")
	}
	if len(request.Messages) == 0 {
		return convertedChatRequest{}, invalidParam("messages", "At least one message is required.")
	}
	if len(request.Messages) > 10000 {
		return convertedChatRequest{}, invalidParam("messages", "Too many messages were provided.")
	}
	// Standard OpenAI sampling / output parameters (n, temperature, top_p,
	// presence_penalty, frequency_penalty, logprobs, seed, stop,
	// parallel_tool_calls) are accepted but cannot be forwarded, because
	// providers.GenerateRequest carries no sampling knobs. The request therefore
	// succeeds and names them in IgnoredParametersHeader rather than either
	// failing or dropping them without a word.

	maxOutput, _, problem := requestMaxOutputTokens(request)
	if problem != nil {
		return convertedChatRequest{}, problem
	}
	providerRequest := providers.GenerateRequest{
		ReasoningEffort: strings.TrimSpace(request.ReasoningEffort),
		MaxOutputTokens: maxOutput,
	}
	if strings.ToLower(strings.TrimSpace(request.ServiceTier)) == "priority" {
		providerRequest.FastMode = true
	}
	if problem := validateResponseFormat(request.ResponseFormat); problem != nil {
		return convertedChatRequest{}, problem
	}

	tools, disableTools, problem := convertTools(request.Tools, request.ToolChoice)
	if problem != nil {
		return convertedChatRequest{}, problem
	}
	if !disableTools {
		providerRequest.Tools = tools
	}

	messages := make([]providers.Message, 0, len(request.Messages))
	systemPrompts := make([]string, 0, 2)
	hasImages := false
	for index, message := range request.Messages {
		converted, system, messageHasImages, problem := convertMessage(message)
		if problem != nil {
			if problem.Param == "messages" {
				problem.Param = fmt.Sprintf("messages[%d]", index)
			}
			return convertedChatRequest{}, problem
		}
		if system != "" {
			systemPrompts = append(systemPrompts, system)
		}
		if converted != nil {
			messages = append(messages, *converted)
		}
		hasImages = hasImages || messageHasImages
	}
	if len(messages) == 0 {
		return convertedChatRequest{}, invalidParam("messages", "At least one non-system message is required.")
	}
	providerRequest.Messages = messages
	providerRequest.SystemPrompt = strings.Join(systemPrompts, "\n\n")
	return convertedChatRequest{
		ProviderRequest:   providerRequest,
		IncludeUsage:      request.StreamOptions != nil && request.StreamOptions.IncludeUsage,
		HasImages:         hasImages,
		IgnoredParameters: chatIgnoredParameters(request),
	}, nil
}

func requestMaxOutputTokens(request chatCompletionRequest) (int64, bool, *apiProblem) {
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil && *request.MaxTokens != *request.MaxCompletionTokens {
		return 0, false, invalidParam("max_completion_tokens", "max_tokens and max_completion_tokens must match when both are provided.")
	}
	value := int64(0)
	explicit := false
	if request.MaxCompletionTokens != nil {
		value, explicit = *request.MaxCompletionTokens, true
	} else if request.MaxTokens != nil {
		value, explicit = *request.MaxTokens, true
	}
	if explicit && (value <= 0 || value > 1_000_000) {
		return 0, false, invalidParam("max_completion_tokens", "Maximum output tokens must be between 1 and 1000000.")
	}
	return value, explicit, nil
}

func validateResponseFormat(raw json.RawMessage) *apiProblem {
	if !rawJSONPresent(raw) {
		return nil
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return invalidParam("response_format", "response_format must be a valid object.")
	}
	if format.Type == "" || format.Type == "text" {
		return nil
	}
	return unsupportedParam("response_format")
}

func convertTools(tools []chatTool, rawChoice json.RawMessage) ([]providers.ToolSpec, bool, *apiProblem) {
	disable := false
	if rawJSONPresent(rawChoice) {
		var choice string
		if err := json.Unmarshal(rawChoice, &choice); err != nil {
			return nil, false, unsupportedParam("tool_choice")
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "auto":
		case "none":
			disable = true
		default:
			return nil, false, unsupportedParam("tool_choice")
		}
	}
	if len(tools) > 128 {
		return nil, false, invalidParam("tools", "Too many tools were provided.")
	}
	converted := make([]providers.ToolSpec, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool.Type != "function" || !toolNamePattern.MatchString(tool.Function.Name) {
			return nil, false, invalidParam(fmt.Sprintf("tools[%d]", index), "Only valid function tools are supported.")
		}
		if _, duplicate := seen[tool.Function.Name]; duplicate {
			return nil, false, invalidParam(fmt.Sprintf("tools[%d]", index), "Tool names must be unique.")
		}
		seen[tool.Function.Name] = struct{}{}
		var schema any = map[string]any{"type": "object", "properties": map[string]any{}}
		if rawJSONPresent(tool.Function.Parameters) {
			if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
				return nil, false, invalidParam(fmt.Sprintf("tools[%d].function.parameters", index), "Tool parameters must be valid JSON Schema.")
			}
			if _, ok := schema.(map[string]any); !ok {
				return nil, false, invalidParam(fmt.Sprintf("tools[%d].function.parameters", index), "Tool parameters must be a JSON object.")
			}
		}
		converted = append(converted, providers.ToolSpec{Name: tool.Function.Name, Description: strings.TrimSpace(tool.Function.Description), Schema: schema})
	}
	return converted, disable, nil
}

func convertMessage(message chatMessage) (*providers.Message, string, bool, *apiProblem) {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	blocks, text, hasImages, problem := convertMessageContent(message.Content)
	if problem != nil {
		return nil, "", false, problem
	}
	switch role {
	case "system", "developer":
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" || hasImages {
			return nil, "", false, invalidParam("messages", "System messages may contain text only.")
		}
		return nil, strings.TrimSpace(text), false, nil
	case "user":
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" {
			return nil, "", false, invalidParam("messages", "User messages cannot contain tool call metadata.")
		}
		if len(blocks) == 0 {
			return nil, "", false, invalidParam("messages", "User message content is required.")
		}
		return &providers.Message{Role: "user", Content: text, Blocks: blocks}, "", hasImages, nil
	case "assistant":
		if message.ToolCallID != "" {
			return nil, "", false, invalidParam("messages", "Assistant messages cannot contain tool_call_id.")
		}
		for index, call := range message.ToolCalls {
			if call.Type != "" && call.Type != "function" {
				return nil, "", false, invalidParam("messages", "Only function tool calls are supported.")
			}
			if strings.TrimSpace(call.ID) == "" || !toolNamePattern.MatchString(call.Function.Name) {
				return nil, "", false, invalidParam("messages", fmt.Sprintf("Assistant tool call %d is invalid.", index))
			}
			arguments := strings.TrimSpace(call.Function.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return nil, "", false, invalidParam("messages", fmt.Sprintf("Assistant tool call %d arguments must be valid JSON.", index))
			}
			blocks = append(blocks, providers.ContentBlock{Type: "tool_use", ToolUseID: strings.TrimSpace(call.ID), ToolName: call.Function.Name, Input: json.RawMessage(arguments)})
		}
		if len(blocks) == 0 {
			return nil, "", false, invalidParam("messages", "Assistant message content or tool calls are required.")
		}
		return &providers.Message{Role: "assistant", Content: text, Blocks: blocks}, "", hasImages, nil
	case "tool":
		if len(message.ToolCalls) > 0 || strings.TrimSpace(message.ToolCallID) == "" || hasImages {
			return nil, "", false, invalidParam("messages", "Tool messages require tool_call_id and text content.")
		}
		output := strings.TrimSpace(text)
		if output == "" {
			return nil, "", false, invalidParam("messages", "Tool message content is required.")
		}
		name := strings.TrimSpace(message.Name)
		return &providers.Message{Role: "tool", Content: output, Blocks: []providers.ContentBlock{{Type: "tool_result", ToolUseID: strings.TrimSpace(message.ToolCallID), ToolName: name, Output: output}}}, "", false, nil
	default:
		return nil, "", false, invalidParam("messages", "Unsupported message role.")
	}
}

func convertMessageContent(raw json.RawMessage) ([]providers.ContentBlock, string, bool, *apiProblem) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, "", false, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, "", false, invalidParam("messages", "Message content is invalid.")
		}
		if text == "" {
			return nil, "", false, nil
		}
		return []providers.ContentBlock{{Type: "text", Text: text}}, text, false, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, "", false, invalidParam("messages", "Message content must be a string or an array of content parts.")
	}
	blocks := make([]providers.ContentBlock, 0, len(parts))
	texts := make([]string, 0, len(parts))
	hasImages := false
	for _, rawPart := range parts {
		var part struct {
			Type     string          `json:"type"`
			Text     string          `json:"text"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return nil, "", false, invalidParam("messages", "Message content part is invalid.")
		}
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				blocks = append(blocks, providers.ContentBlock{Type: "text", Text: part.Text})
				texts = append(texts, part.Text)
			}
		case "image_url", "input_image":
			urlValue, problem := imageURLValue(part.ImageURL)
			if problem != nil {
				return nil, "", false, problem
			}
			mimeType, data, problem := decodeImageDataURL(urlValue)
			if problem != nil {
				return nil, "", false, problem
			}
			blocks = append(blocks, providers.ContentBlock{Type: "image", MIMEType: mimeType, Data: data})
			hasImages = true
		default:
			return nil, "", false, invalidParam("messages", "Unsupported message content part type.")
		}
	}
	return blocks, strings.Join(texts, "\n"), hasImages, nil
}

func imageURLValue(raw json.RawMessage) (string, *apiProblem) {
	if !rawJSONPresent(raw) {
		return "", invalidParam("messages", "Image content requires image_url.")
	}
	var direct string
	if err := json.Unmarshal(raw, &direct); err == nil {
		return strings.TrimSpace(direct), nil
	}
	var object struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", invalidParam("messages", "image_url is invalid.")
	}
	return strings.TrimSpace(object.URL), nil
}

func decodeImageDataURL(value string) (string, []byte, *apiProblem) {
	if !strings.HasPrefix(value, "data:") {
		return "", nil, invalidParam("messages", "Only base64 data URLs are accepted for image input.")
	}
	header, encoded, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", nil, invalidParam("messages", "Image data URL must use base64 encoding.")
	}
	mimeType := strings.TrimSpace(header[:len(header)-len(";base64")])
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") || len(mimeType) > 100 {
		return "", nil, invalidParam("messages", "Image data URL has an invalid media type.")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > 16<<20 {
		return "", nil, invalidParam("messages", "Image data URL is invalid or too large.")
	}
	return mimeType, data, nil
}

func rawJSONPresent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

const (
	gatewayFailureUpstreamStart       = "upstream_start_failed"
	gatewayFailureUpstreamEvent       = "upstream_event_failed"
	gatewayFailureUpstreamEnded       = "upstream_stream_ended"
	gatewayFailureRequestCanceled     = "request_canceled"
	gatewayFailureClientDisconnected  = "client_disconnected"
	gatewayFailureProviderNoEventFeed = "provider_no_event_stream"
	gatewayFailureUnknown             = "gateway_failed"
	gatewayAccountingTimeout          = 5 * time.Second
)

type completionExecution struct {
	StartedAt     time.Time
	FirstOutputAt time.Time
	Usage         providers.Usage
	Dispatch      providers.DispatchInfo
	StopReason    string
	ErrorMessage  string
}

func (execution *completionExecution) markOutput() {
	if execution.FirstOutputAt.IsZero() {
		execution.FirstOutputAt = time.Now()
	}
}

func (s *Service) completeChatCompletion(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedChatRequest) {
	completionID := newCompletionID()
	created := s.now().Unix()
	execution := completionExecution{StartedAt: time.Now()}
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		execution.ErrorMessage = gatewayFailureUpstreamStart
		s.recordGatewayRequest(key, resolved, execution)
		writeProviderStartError(w, err)
		return
	}
	if events == nil {
		execution.ErrorMessage = gatewayFailureProviderNoEventFeed
		s.recordGatewayRequest(key, resolved, execution)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
		return
	}

	var text strings.Builder
	toolCalls := make([]chatToolCallResponse, 0)
	completed := false
	for !completed {
		select {
		case <-r.Context().Done():
			execution.ErrorMessage = gatewayFailureRequestCanceled
			s.recordGatewayRequest(key, resolved, execution)
			return
		case event, ok := <-events:
			if !ok {
				execution.ErrorMessage = gatewayFailureUpstreamEnded
				s.recordGatewayRequest(key, resolved, execution)
				writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
				return
			}
			captureExecutionEvent(&execution, event)
			switch event.Type {
			case "text":
				if event.Text != "" {
					execution.markOutput()
					text.WriteString(event.Text)
				}
			case "tool_call":
				if event.ToolCall != nil {
					execution.markOutput()
					toolCalls = append(toolCalls, responseToolCall(*event.ToolCall))
				}
			case "error":
				execution.ErrorMessage = gatewayFailureUpstreamEvent
				s.recordGatewayRequest(key, resolved, execution)
				writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
				return
			case "done":
				completed = true
			}
		}
	}
	if err := s.recordGatewayRequest(key, resolved, execution); err != nil {
		writeProblem(w, internalProblem())
		return
	}
	finishReason := mapFinishReason(execution.StopReason, len(toolCalls) > 0)
	content := any(text.String())
	if text.Len() == 0 && len(toolCalls) > 0 {
		content = nil
	}
	response := chatCompletionResponse{
		ID: completionID, Object: "chat.completion", Created: created, Model: resolved.Alias,
		Choices: []chatCompletionChoice{{Index: 0, Message: chatCompletionMessage{Role: "assistant", Content: content, ToolCalls: toolCalls}, FinishReason: finishReason}},
		Usage:   openAIUsage(execution.Usage),
	}
	setIgnoredParameters(w, converted.IgnoredParameters)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Service) streamChatCompletion(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedChatRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, internalProblem())
		return
	}
	completionID := newCompletionID()
	created := s.now().Unix()
	execution := completionExecution{StartedAt: time.Now()}
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		execution.ErrorMessage = gatewayFailureUpstreamStart
		s.recordGatewayRequest(key, resolved, execution)
		writeProviderStartError(w, err)
		return
	}
	if events == nil {
		execution.ErrorMessage = gatewayFailureProviderNoEventFeed
		s.recordGatewayRequest(key, resolved, execution)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
		return
	}

	setIgnoredParameters(w, converted.IgnoredParameters)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := writeSSEJSON(w, flusher, chatCompletionChunk{
		ID: completionID, Object: "chat.completion.chunk", Created: created, Model: resolved.Alias,
		Choices: []chatChunkChoice{{Index: 0, Delta: chatChunkDelta{Role: "assistant"}}},
	}); err != nil {
		execution.ErrorMessage = gatewayFailureClientDisconnected
		s.recordGatewayRequest(key, resolved, execution)
		return
	}

	toolIndex := 0
	for {
		select {
		case <-r.Context().Done():
			execution.ErrorMessage = gatewayFailureRequestCanceled
			s.recordGatewayRequest(key, resolved, execution)
			return
		case event, ok := <-events:
			if !ok {
				execution.ErrorMessage = gatewayFailureUpstreamEnded
				_ = writeSSEJSON(w, flusher, apiErrorEnvelope{Error: apiErrorBody{Message: "The upstream model request failed.", Type: "server_error", Code: "upstream_error"}})
				s.recordGatewayRequest(key, resolved, execution)
				return
			}
			captureExecutionEvent(&execution, event)
			switch event.Type {
			case "text":
				if event.Text == "" {
					continue
				}
				execution.markOutput()
				chunk := chatCompletionChunk{ID: completionID, Object: "chat.completion.chunk", Created: created, Model: resolved.Alias, Choices: []chatChunkChoice{{Index: 0, Delta: chatChunkDelta{Content: event.Text}}}}
				if err := writeSSEJSON(w, flusher, chunk); err != nil {
					execution.ErrorMessage = gatewayFailureClientDisconnected
					s.recordGatewayRequest(key, resolved, execution)
					return
				}
			case "tool_call":
				if event.ToolCall == nil {
					continue
				}
				execution.markOutput()
				call := responseToolCall(*event.ToolCall)
				chunk := chatCompletionChunk{ID: completionID, Object: "chat.completion.chunk", Created: created, Model: resolved.Alias, Choices: []chatChunkChoice{{Index: 0, Delta: chatChunkDelta{ToolCalls: []chatStreamToolCall{{Index: toolIndex, ID: call.ID, Type: "function", Function: call.Function}}}}}}
				toolIndex++
				if err := writeSSEJSON(w, flusher, chunk); err != nil {
					execution.ErrorMessage = gatewayFailureClientDisconnected
					s.recordGatewayRequest(key, resolved, execution)
					return
				}
			case "error":
				execution.ErrorMessage = gatewayFailureUpstreamEvent
				_ = writeSSEJSON(w, flusher, apiErrorEnvelope{Error: apiErrorBody{Message: "The upstream model request failed.", Type: "server_error", Code: "upstream_error"}})
				s.recordGatewayRequest(key, resolved, execution)
				return
			case "done":
				if err := s.recordGatewayRequest(key, resolved, execution); err != nil {
					_ = writeSSEJSON(w, flusher, apiErrorEnvelope{Error: apiErrorBody{Message: "The Gateway could not process the request.", Type: "server_error", Code: "gateway_internal_error"}})
					return
				}
				finishReason := mapFinishReason(execution.StopReason, toolIndex > 0)
				chunk := chatCompletionChunk{ID: completionID, Object: "chat.completion.chunk", Created: created, Model: resolved.Alias, Choices: []chatChunkChoice{{Index: 0, Delta: chatChunkDelta{}, FinishReason: &finishReason}}}
				if err := writeSSEJSON(w, flusher, chunk); err != nil {
					return
				}
				if converted.IncludeUsage {
					usage := openAIUsage(execution.Usage)
					usageChunk := chatCompletionChunk{ID: completionID, Object: "chat.completion.chunk", Created: created, Model: resolved.Alias, Choices: []chatChunkChoice{}, Usage: &usage}
					if err := writeSSEJSON(w, flusher, usageChunk); err != nil {
						return
					}
				}
				_ = writeSSEDone(w, flusher)
				return
			}
		}
	}
}

func writeProviderStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, providers.ErrGatewayOAuthUnsupported) {
		writeAPIError(w, http.StatusForbidden, "model_not_allowed", "The requested model is not permitted for Gateway use.", "invalid_request_error", "model")
		return
	}
	writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
}

func captureExecutionEvent(execution *completionExecution, event providers.Event) {
	if event.Dispatch != nil {
		execution.Dispatch = *event.Dispatch
	}
	if event.Usage != nil {
		execution.Usage = *event.Usage
	}
	if event.StopReason != "" {
		execution.StopReason = event.StopReason
	}
}

func (s *Service) recordGatewayRequest(key db.GatewayKey, resolved resolvedModel, execution completionExecution) error {
	providerName := resolved.Provider.Name()
	model := resolved.Model
	credentialID := ""
	if strings.TrimSpace(execution.Dispatch.Provider) != "" {
		providerName = strings.TrimSpace(execution.Dispatch.Provider)
	}
	if strings.TrimSpace(execution.Dispatch.Model) != "" {
		model = strings.TrimSpace(execution.Dispatch.Model)
	}
	credentialID = strings.TrimSpace(execution.Dispatch.CredentialID)
	durationMS := time.Since(execution.StartedAt).Milliseconds()
	if durationMS < 1 {
		durationMS = 1
	}
	ttftMS := int64(0)
	if !execution.FirstOutputAt.IsZero() {
		ttftMS = execution.FirstOutputAt.Sub(execution.StartedAt).Milliseconds()
		if ttftMS < 0 {
			ttftMS = 0
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayAccountingTimeout)
	defer cancel()
	_, err := s.store.AddAPIRequest(ctx, db.APIRequest{
		Kind:              "gateway",
		Provider:          providerName,
		CredentialID:      credentialID,
		GatewayKeyID:      key.ID,
		Model:             model,
		InputTokens:       execution.Usage.InputTokens,
		OutputTokens:      execution.Usage.OutputTokens,
		CachedInputTokens: execution.Usage.CachedInputTokens,
		ReasoningTokens:   execution.Usage.ReasoningTokens,
		TTFTMS:            ttftMS,
		DurationMS:        durationMS,
		CostUSD: pricing.EstimateUsageCostUSD(providerName, model, pricing.Usage{
			InputTokens:       execution.Usage.InputTokens,
			OutputTokens:      execution.Usage.OutputTokens,
			CachedInputTokens: execution.Usage.CachedInputTokens,
			ReasoningTokens:   execution.Usage.ReasoningTokens,
		}),
		ErrorMessage: gatewayFailureCategory(execution.ErrorMessage),
		StopReason:   execution.StopReason,
		CreatedAt:    s.now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		slog.Warn("record gateway api request failed", "gatewayKeyId", key.ID, "category", "persistence_failed")
	}
	return err
}

func gatewayFailureCategory(value string) string {
	switch value {
	case "", gatewayFailureUpstreamStart, gatewayFailureUpstreamEvent, gatewayFailureUpstreamEnded, gatewayFailureRequestCanceled, gatewayFailureClientDisconnected, gatewayFailureProviderNoEventFeed:
		return value
	default:
		return gatewayFailureUnknown
	}
}

func newCompletionID() string {
	return "chatcmpl-" + strings.ReplaceAll(db.NewID(), "-", "")
}

type chatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   chatUsage              `json:"usage"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Role      string                 `json:"role"`
	Content   any                    `json:"content"`
	ToolCalls []chatToolCallResponse `json:"tool_calls,omitempty"`
}

type chatToolCallResponse struct {
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function chatToolCallFunctionBody `json:"function"`
}

type chatToolCallFunctionBody struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatUsage struct {
	PromptTokens            int64                   `json:"prompt_tokens"`
	CompletionTokens        int64                   `json:"completion_tokens"`
	TotalTokens             int64                   `json:"total_tokens"`
	PromptTokensDetails     *promptTokenDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *completionTokenDetails `json:"completion_tokens_details,omitempty"`
}

type promptTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type completionTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

func openAIUsage(usage providers.Usage) chatUsage {
	result := chatUsage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.InputTokens + usage.OutputTokens}
	if usage.CachedInputTokens != 0 {
		result.PromptTokensDetails = &promptTokenDetails{CachedTokens: usage.CachedInputTokens}
	}
	if usage.ReasoningTokens != 0 {
		result.CompletionTokensDetails = &completionTokenDetails{ReasoningTokens: usage.ReasoningTokens}
	}
	return result
}

func responseToolCall(call providers.ToolCall) chatToolCallResponse {
	arguments := strings.TrimSpace(string(call.Input))
	if arguments == "" || !json.Valid([]byte(arguments)) {
		arguments = "{}"
	}
	return chatToolCallResponse{ID: call.ID, Type: "function", Function: chatToolCallFunctionBody{Name: call.Name, Arguments: arguments}}
}

func mapFinishReason(stopReason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens", "max_output_tokens", "length", "incomplete":
		return "length"
	case "content_filter", "safety":
		return "content_filter"
	default:
		return "stop"
	}
}

type chatCompletionChunk struct {
	ID      string            `json:"id"`
	Object  string            `json:"object"`
	Created int64             `json:"created"`
	Model   string            `json:"model"`
	Choices []chatChunkChoice `json:"choices"`
	Usage   *chatUsage        `json:"usage,omitempty"`
}

type chatChunkChoice struct {
	Index        int            `json:"index"`
	Delta        chatChunkDelta `json:"delta"`
	FinishReason *string        `json:"finish_reason"`
}

type chatChunkDelta struct {
	Role      string               `json:"role,omitempty"`
	Content   string               `json:"content,omitempty"`
	ToolCalls []chatStreamToolCall `json:"tool_calls,omitempty"`
}

type chatStreamToolCall struct {
	Index    int                      `json:"index"`
	ID       string                   `json:"id"`
	Type     string                   `json:"type"`
	Function chatToolCallFunctionBody `json:"function"`
}

func writeSSEJSON(w http.ResponseWriter, flusher http.Flusher, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEDone(w http.ResponseWriter, flusher http.Flusher) error {
	if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
