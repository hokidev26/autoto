package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/providers"
)

type responsesRequest struct {
	Model              string                  `json:"model"`
	Input              json.RawMessage         `json:"input"`
	Instructions       json.RawMessage         `json:"instructions"`
	Tools              []responsesFunctionTool `json:"tools"`
	ToolChoice         json.RawMessage         `json:"tool_choice"`
	ParallelToolCalls  *bool                   `json:"parallel_tool_calls"`
	MaxOutputTokens    *int64                  `json:"max_output_tokens"`
	Reasoning          responsesReasoning      `json:"reasoning"`
	ServiceTier        string                  `json:"service_tier"`
	Stream             bool                    `json:"stream"`
	PreviousResponseID json.RawMessage         `json:"previous_response_id"`
	Background         json.RawMessage         `json:"background"`
	Store              json.RawMessage         `json:"store"`
	Conversation       json.RawMessage         `json:"conversation"`
}

type responsesReasoning struct {
	Effort string `json:"effort"`
}

type responsesFunctionTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type convertedResponsesRequest struct {
	ProviderRequest  providers.GenerateRequest
	HasImages        bool
	Instructions     any
	MaxOutputTokens  *int64
	ReasoningEffort  any
	ServiceTier      string
	ResponseTools    []responsesFunctionTool
	ParallelToolCall bool
}

type responsesResponse struct {
	ID                 string                    `json:"id"`
	Object             string                    `json:"object"`
	CreatedAt          int64                     `json:"created_at"`
	Status             string                    `json:"status"`
	Error              any                       `json:"error"`
	IncompleteDetails  *responsesIncomplete      `json:"incomplete_details"`
	Instructions       any                       `json:"instructions"`
	MaxOutputTokens    *int64                    `json:"max_output_tokens"`
	Model              string                    `json:"model"`
	Output             []responsesOutputItem     `json:"output"`
	ParallelToolCalls  bool                      `json:"parallel_tool_calls"`
	PreviousResponseID any                       `json:"previous_response_id"`
	Reasoning          responsesReasoningSummary `json:"reasoning"`
	ServiceTier        string                    `json:"service_tier"`
	Store              bool                      `json:"store"`
	Tools              []responsesFunctionTool   `json:"tools"`
	Usage              *responsesUsage           `json:"usage"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"`
}

type responsesReasoningSummary struct {
	Effort  any `json:"effort"`
	Summary any `json:"summary"`
}

type responsesOutputItem struct {
	ID        string                   `json:"id"`
	Type      string                   `json:"type"`
	Status    string                   `json:"status"`
	Role      string                   `json:"role,omitempty"`
	Content   []responsesOutputContent `json:"content,omitempty"`
	CallID    string                   `json:"call_id,omitempty"`
	Name      string                   `json:"name,omitempty"`
	Arguments string                   `json:"arguments,omitempty"`
}

type responsesOutputContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
	Logprobs    []any  `json:"logprobs"`
}

type responsesUsage struct {
	InputTokens         int64                       `json:"input_tokens"`
	InputTokensDetails  responsesInputTokenDetails  `json:"input_tokens_details"`
	OutputTokens        int64                       `json:"output_tokens"`
	OutputTokensDetails responsesOutputTokenDetails `json:"output_tokens_details"`
	TotalTokens         int64                       `json:"total_tokens"`
}

type responsesInputTokenDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type responsesOutputTokenDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

func (s *Service) handleResponses(w http.ResponseWriter, r *http.Request) {
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

	var request responsesRequest
	if problem := decodeJSONBody(w, r, s.maxRequestBytes, &request); problem != nil {
		writeProblem(w, problem)
		return
	}
	converted, problem := convertResponsesRequest(request)
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	resolved, problem := s.prepareProviderRequest(r.Context(), key, request.Model, &converted.ProviderRequest, converted.HasImages, lease, generationParameterNames{
		Tools:           "tools",
		Images:          "input",
		ReasoningEffort: "reasoning.effort",
		ServiceTier:     "service_tier",
		MaxOutputTokens: "max_output_tokens",
	})
	if problem != nil {
		writeProblem(w, problem)
		return
	}
	if request.Stream {
		s.streamResponse(w, r, key, resolved, converted)
		return
	}
	s.completeResponse(w, r, key, resolved, converted)
}

func convertResponsesRequest(request responsesRequest) (convertedResponsesRequest, *apiProblem) {
	if strings.TrimSpace(request.Model) == "" {
		return convertedResponsesRequest{}, invalidParam("model", "A model is required.")
	}
	for _, unsupported := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "previous_response_id", raw: request.PreviousResponseID},
		{name: "background", raw: request.Background},
		{name: "conversation", raw: request.Conversation},
	} {
		if rawJSONPresent(unsupported.raw) {
			return convertedResponsesRequest{}, unsupportedParam(unsupported.name)
		}
	}
	if request.ParallelToolCalls != nil && !*request.ParallelToolCalls {
		return convertedResponsesRequest{}, unsupportedParam("parallel_tool_calls")
	}
	if request.MaxOutputTokens != nil && (*request.MaxOutputTokens <= 0 || *request.MaxOutputTokens > 1_000_000) {
		return convertedResponsesRequest{}, invalidParam("max_output_tokens", "Maximum output tokens must be between 1 and 1000000.")
	}

	instructions, instructionsPresent, problem := optionalJSONString(request.Instructions, "instructions")
	if problem != nil {
		return convertedResponsesRequest{}, problem
	}
	providerRequest := providers.GenerateRequest{ReasoningEffort: strings.TrimSpace(request.Reasoning.Effort)}
	if request.MaxOutputTokens != nil {
		providerRequest.MaxOutputTokens = *request.MaxOutputTokens
	}
	serviceTier := "default"
	switch strings.ToLower(strings.TrimSpace(request.ServiceTier)) {
	case "", "auto", "default":
	case "priority":
		providerRequest.FastMode = true
		serviceTier = "priority"
	default:
		return convertedResponsesRequest{}, unsupportedParam("service_tier")
	}

	tools, responseTools, disableTools, problem := convertResponsesTools(request.Tools, request.ToolChoice)
	if problem != nil {
		return convertedResponsesRequest{}, problem
	}
	if !disableTools {
		providerRequest.Tools = tools
	}

	messages, systemPrompts, hasImages, problem := convertResponsesInput(request.Input)
	if problem != nil {
		return convertedResponsesRequest{}, problem
	}
	if instructionsPresent && strings.TrimSpace(instructions) != "" {
		systemPrompts = append([]string{strings.TrimSpace(instructions)}, systemPrompts...)
	}
	providerRequest.SystemPrompt = strings.Join(systemPrompts, "\n\n")
	providerRequest.Messages = messages

	var responseInstructions any
	if instructionsPresent {
		responseInstructions = instructions
	}
	var responseEffort any
	if providerRequest.ReasoningEffort != "" {
		responseEffort = providerRequest.ReasoningEffort
	}
	return convertedResponsesRequest{
		ProviderRequest:  providerRequest,
		HasImages:        hasImages,
		Instructions:     responseInstructions,
		MaxOutputTokens:  request.MaxOutputTokens,
		ReasoningEffort:  responseEffort,
		ServiceTier:      serviceTier,
		ResponseTools:    responseTools,
		ParallelToolCall: request.ParallelToolCalls == nil || *request.ParallelToolCalls,
	}, nil
}

func optionalJSONString(raw json.RawMessage, param string) (string, bool, *apiProblem) {
	if !rawJSONPresent(raw) {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, invalidParam(param, fmt.Sprintf("%s must be a string.", param))
	}
	return value, true, nil
}

func convertResponsesTools(tools []responsesFunctionTool, rawChoice json.RawMessage) ([]providers.ToolSpec, []responsesFunctionTool, bool, *apiProblem) {
	disable := false
	if rawJSONPresent(rawChoice) {
		var choice string
		if err := json.Unmarshal(rawChoice, &choice); err != nil {
			return nil, nil, false, unsupportedParam("tool_choice")
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "auto":
		case "none":
			disable = true
		default:
			return nil, nil, false, unsupportedParam("tool_choice")
		}
	}
	if len(tools) > 128 {
		return nil, nil, false, invalidParam("tools", "Too many tools were provided.")
	}
	converted := make([]providers.ToolSpec, 0, len(tools))
	sanitized := make([]responsesFunctionTool, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for index, tool := range tools {
		if tool.Type != "function" {
			return nil, nil, false, invalidParam(fmt.Sprintf("tools[%d]", index), "Only function tools are supported; hosted tools are unavailable.")
		}
		name := strings.TrimSpace(tool.Name)
		if !toolNamePattern.MatchString(name) {
			return nil, nil, false, invalidParam(fmt.Sprintf("tools[%d].name", index), "Tool name is invalid.")
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, false, invalidParam(fmt.Sprintf("tools[%d].name", index), "Tool names must be unique.")
		}
		seen[name] = struct{}{}
		schema, normalized, problem := responseToolSchema(tool.Parameters, fmt.Sprintf("tools[%d].parameters", index))
		if problem != nil {
			return nil, nil, false, problem
		}
		converted = append(converted, providers.ToolSpec{Name: name, Description: strings.TrimSpace(tool.Description), Schema: schema})
		tool.Name = name
		tool.Description = strings.TrimSpace(tool.Description)
		tool.Parameters = normalized
		sanitized = append(sanitized, tool)
	}
	return converted, sanitized, disable, nil
}

func responseToolSchema(raw json.RawMessage, param string) (any, json.RawMessage, *apiProblem) {
	if !rawJSONPresent(raw) {
		raw = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, invalidParam(param, "Tool parameters must be valid JSON Schema.")
	}
	if _, ok := schema.(map[string]any); !ok {
		return nil, nil, invalidParam(param, "Tool parameters must be a JSON object.")
	}
	normalized, err := json.Marshal(schema)
	if err != nil {
		return nil, nil, internalProblem()
	}
	return schema, normalized, nil
}

func convertResponsesInput(raw json.RawMessage) ([]providers.Message, []string, bool, *apiProblem) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil, false, invalidParam("input", "Input is required.")
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil || text == "" {
			return nil, nil, false, invalidParam("input", "Input must be a non-empty string or an array of input items.")
		}
		block := providers.ContentBlock{Type: "text", Text: text}
		return []providers.Message{{Role: "user", Content: text, Blocks: []providers.ContentBlock{block}}}, nil, false, nil
	}

	var items []json.RawMessage
	if trimmed[0] == '{' {
		items = []json.RawMessage{append(json.RawMessage(nil), trimmed...)}
	} else if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, nil, false, invalidParam("input", "Input must be a string, input item, or array of input items.")
	}
	if len(items) == 0 || len(items) > 10000 {
		return nil, nil, false, invalidParam("input", "Input must contain between 1 and 10000 items.")
	}

	messages := make([]providers.Message, 0, len(items))
	systemPrompts := make([]string, 0, 2)
	hasImages := false
	for index, rawItem := range items {
		var item struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Text      string          `json:"text"`
			ImageURL  string          `json:"image_url"`
			FileID    string          `json:"file_id"`
			ID        string          `json:"id"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return nil, nil, false, invalidParam(fmt.Sprintf("input[%d]", index), "Input item is invalid.")
		}
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		if itemType == "" && strings.TrimSpace(item.Role) != "" {
			itemType = "message"
		}
		switch itemType {
		case "message":
			message, system, image, problem := convertResponsesMessage(item.Role, item.Content, fmt.Sprintf("input[%d]", index))
			if problem != nil {
				return nil, nil, false, problem
			}
			if system != "" {
				systemPrompts = append(systemPrompts, system)
			}
			if message != nil {
				messages = append(messages, *message)
			}
			hasImages = hasImages || image
		case "function_call":
			callID := strings.TrimSpace(item.CallID)
			if callID == "" {
				callID = strings.TrimSpace(item.ID)
			}
			name := strings.TrimSpace(item.Name)
			arguments, problem := responseFunctionArguments(item.Arguments, fmt.Sprintf("input[%d].arguments", index))
			if problem != nil {
				return nil, nil, false, problem
			}
			if callID == "" || !toolNamePattern.MatchString(name) {
				return nil, nil, false, invalidParam(fmt.Sprintf("input[%d]", index), "Function call history requires call_id and a valid name.")
			}
			messages = append(messages, providers.Message{Role: "assistant", Blocks: []providers.ContentBlock{{Type: "tool_use", ToolUseID: callID, ToolName: name, Input: json.RawMessage(arguments)}}})
		case "function_call_output":
			callID := strings.TrimSpace(item.CallID)
			output, problem := responseFunctionOutput(item.Output, fmt.Sprintf("input[%d].output", index))
			if problem != nil {
				return nil, nil, false, problem
			}
			if callID == "" {
				return nil, nil, false, invalidParam(fmt.Sprintf("input[%d].call_id", index), "Function call output requires call_id.")
			}
			messages = append(messages, providers.Message{Role: "tool", Content: output, Blocks: []providers.ContentBlock{{Type: "tool_result", ToolUseID: callID, Output: output}}})
		case "input_text":
			if item.Text == "" {
				return nil, nil, false, invalidParam(fmt.Sprintf("input[%d].text", index), "Input text is required.")
			}
			messages = append(messages, providers.Message{Role: "user", Content: item.Text, Blocks: []providers.ContentBlock{{Type: "text", Text: item.Text}}})
		case "input_image":
			if strings.TrimSpace(item.FileID) != "" {
				return nil, nil, false, unsupportedParam(fmt.Sprintf("input[%d].file_id", index))
			}
			mimeType, data, problem := decodeImageDataURL(strings.TrimSpace(item.ImageURL))
			if problem != nil {
				problem.Param = fmt.Sprintf("input[%d].image_url", index)
				return nil, nil, false, problem
			}
			messages = append(messages, providers.Message{Role: "user", Blocks: []providers.ContentBlock{{Type: "image", MIMEType: mimeType, Data: data}}})
			hasImages = true
		default:
			return nil, nil, false, invalidParam(fmt.Sprintf("input[%d].type", index), "Unsupported input item type. Files, audio, and hosted items are unavailable.")
		}
	}
	if len(messages) == 0 {
		return nil, nil, false, invalidParam("input", "At least one non-system input item is required.")
	}
	return messages, systemPrompts, hasImages, nil
}

func convertResponsesMessage(role string, rawContent json.RawMessage, param string) (*providers.Message, string, bool, *apiProblem) {
	role = strings.ToLower(strings.TrimSpace(role))
	blocks, text, hasImages, problem := convertResponsesContent(rawContent, param+".content")
	if problem != nil {
		return nil, "", false, problem
	}
	switch role {
	case "system", "developer":
		if hasImages {
			return nil, "", false, invalidParam(param+".content", "System input may contain text only.")
		}
		if strings.TrimSpace(text) == "" {
			return nil, "", false, invalidParam(param+".content", "System input text is required.")
		}
		return nil, strings.TrimSpace(text), false, nil
	case "user", "assistant":
		if len(blocks) == 0 {
			return nil, "", false, invalidParam(param+".content", "Message content is required.")
		}
		return &providers.Message{Role: role, Content: text, Blocks: blocks}, "", hasImages, nil
	default:
		return nil, "", false, invalidParam(param+".role", "Unsupported message role.")
	}
}

func convertResponsesContent(raw json.RawMessage, param string) ([]providers.ContentBlock, string, bool, *apiProblem) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, "", false, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, "", false, invalidParam(param, "Message content is invalid.")
		}
		if text == "" {
			return nil, "", false, nil
		}
		return []providers.ContentBlock{{Type: "text", Text: text}}, text, false, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, "", false, invalidParam(param, "Message content must be a string or array.")
	}
	blocks := make([]providers.ContentBlock, 0, len(parts))
	texts := make([]string, 0, len(parts))
	hasImages := false
	for index, rawPart := range parts {
		var part struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
			FileID   string `json:"file_id"`
		}
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return nil, "", false, invalidParam(fmt.Sprintf("%s[%d]", param, index), "Content part is invalid.")
		}
		switch strings.ToLower(strings.TrimSpace(part.Type)) {
		case "text", "input_text", "output_text":
			if part.Text != "" {
				blocks = append(blocks, providers.ContentBlock{Type: "text", Text: part.Text})
				texts = append(texts, part.Text)
			}
		case "input_image":
			if strings.TrimSpace(part.FileID) != "" {
				return nil, "", false, unsupportedParam(fmt.Sprintf("%s[%d].file_id", param, index))
			}
			mimeType, data, problem := decodeImageDataURL(strings.TrimSpace(part.ImageURL))
			if problem != nil {
				problem.Param = fmt.Sprintf("%s[%d].image_url", param, index)
				return nil, "", false, problem
			}
			blocks = append(blocks, providers.ContentBlock{Type: "image", MIMEType: mimeType, Data: data})
			hasImages = true
		default:
			return nil, "", false, invalidParam(fmt.Sprintf("%s[%d].type", param, index), "Unsupported content type. Remote images, files, and audio are unavailable.")
		}
	}
	return blocks, strings.Join(texts, "\n"), hasImages, nil
}

func responseFunctionArguments(raw json.RawMessage, param string) (string, *apiProblem) {
	if !rawJSONPresent(raw) {
		return "", invalidParam(param, "Function call arguments are required.")
	}
	var arguments string
	if err := json.Unmarshal(raw, &arguments); err != nil || !json.Valid([]byte(arguments)) {
		return "", invalidParam(param, "Function call arguments must be a JSON string containing valid JSON.")
	}
	return arguments, nil
}

func responseFunctionOutput(raw json.RawMessage, param string) (string, *apiProblem) {
	if !rawJSONPresent(raw) {
		return "", invalidParam(param, "Function call output is required.")
	}
	var output string
	if err := json.Unmarshal(raw, &output); err != nil {
		return "", invalidParam(param, "Function call output must be a string.")
	}
	return output, nil
}

type gatewayExecutionRecorder struct {
	service   *Service
	key       db.GatewayKey
	resolved  resolvedModel
	execution completionExecution
	recorded  bool
	recordErr error
}

func newGatewayExecutionRecorder(service *Service, key db.GatewayKey, resolved resolvedModel) *gatewayExecutionRecorder {
	return &gatewayExecutionRecorder{service: service, key: key, resolved: resolved, execution: completionExecution{StartedAt: time.Now()}}
}

func (r *gatewayExecutionRecorder) capture(event providers.Event) {
	captureExecutionEvent(&r.execution, event)
}

func (r *gatewayExecutionRecorder) markOutput() {
	r.execution.markOutput()
}

func (r *gatewayExecutionRecorder) record(category string) error {
	if r.recorded {
		return r.recordErr
	}
	if category != "" {
		r.execution.ErrorMessage = category
	}
	r.recorded = true
	r.recordErr = r.service.recordGatewayRequest(r.key, r.resolved, r.execution)
	return r.recordErr
}

func (s *Service) completeResponse(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedResponsesRequest) {
	responseID := newResponseID()
	created := s.now().Unix()
	recorder := newGatewayExecutionRecorder(s, key, resolved)
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		_ = recorder.record(gatewayFailureUpstreamStart)
		writeProviderStartError(w, err)
		return
	}
	if events == nil {
		_ = recorder.record(gatewayFailureProviderNoEventFeed)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
		return
	}

	var text strings.Builder
	toolCalls := make([]providers.ToolCall, 0)
	for {
		select {
		case <-r.Context().Done():
			_ = recorder.record(gatewayFailureRequestCanceled)
			return
		case event, ok := <-events:
			if !ok {
				_ = recorder.record(gatewayFailureUpstreamEnded)
				writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
				return
			}
			recorder.capture(event)
			switch event.Type {
			case "text":
				if event.Text != "" {
					recorder.markOutput()
					text.WriteString(event.Text)
				}
			case "tool_call":
				if event.ToolCall != nil {
					recorder.markOutput()
					toolCalls = append(toolCalls, *event.ToolCall)
				}
			case "error":
				_ = recorder.record(gatewayFailureUpstreamEvent)
				writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
				return
			case "done":
				if err := recorder.record(""); err != nil {
					writeProblem(w, internalProblem())
					return
				}
				status, incomplete := responsesCompletionStatus(recorder.execution.StopReason)
				output := buildResponsesOutput(text.String(), toolCalls)
				response := buildResponsesResponse(responseID, created, resolved.Alias, status, incomplete, output, recorder.execution.Usage, converted)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				_ = json.NewEncoder(w).Encode(response)
				return
			}
		}
	}
}

func (s *Service) streamResponse(w http.ResponseWriter, r *http.Request, key db.GatewayKey, resolved resolvedModel, converted convertedResponsesRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeProblem(w, internalProblem())
		return
	}
	responseID := newResponseID()
	created := s.now().Unix()
	recorder := newGatewayExecutionRecorder(s, key, resolved)
	events, err := resolved.Provider.Generate(r.Context(), converted.ProviderRequest)
	if err != nil {
		_ = recorder.record(gatewayFailureUpstreamStart)
		writeProviderStartError(w, err)
		return
	}
	if events == nil {
		_ = recorder.record(gatewayFailureProviderNoEventFeed)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "The upstream model request failed.", "server_error", "")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	initial := buildResponsesResponse(responseID, created, resolved.Alias, "in_progress", nil, []responsesOutputItem{}, providers.Usage{}, converted)
	initial.Usage = nil
	if err := writeNamedSSEJSON(w, flusher, "response.created", map[string]any{"type": "response.created", "response": initial}); err != nil {
		_ = recorder.record(gatewayFailureClientDisconnected)
		return
	}
	if err := writeNamedSSEJSON(w, flusher, "response.in_progress", map[string]any{"type": "response.in_progress", "response": initial}); err != nil {
		_ = recorder.record(gatewayFailureClientDisconnected)
		return
	}

	output := make([]responsesOutputItem, 0)
	var text strings.Builder
	textOutputIndex := -1
	textItemID := ""
	for {
		select {
		case <-r.Context().Done():
			_ = recorder.record(gatewayFailureRequestCanceled)
			return
		case event, ok := <-events:
			if !ok {
				_ = writeResponsesStreamError(w, flusher, "upstream_error", "The upstream model request failed.")
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
				if textOutputIndex < 0 {
					textOutputIndex = len(output)
					textItemID = newGatewayPublicID("msg_")
					item := responsesOutputItem{ID: textItemID, Type: "message", Status: "in_progress", Role: "assistant", Content: []responsesOutputContent{}}
					output = append(output, item)
					if err := writeNamedSSEJSON(w, flusher, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": textOutputIndex, "item": item}); err != nil {
						_ = recorder.record(gatewayFailureClientDisconnected)
						return
					}
					part := responsesOutputContent{Type: "output_text", Text: "", Annotations: []any{}, Logprobs: []any{}}
					if err := writeNamedSSEJSON(w, flusher, "response.content_part.added", map[string]any{"type": "response.content_part.added", "item_id": textItemID, "output_index": textOutputIndex, "content_index": 0, "part": part}); err != nil {
						_ = recorder.record(gatewayFailureClientDisconnected)
						return
					}
				}
				text.WriteString(event.Text)
				if err := writeNamedSSEJSON(w, flusher, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": textItemID, "output_index": textOutputIndex, "content_index": 0, "delta": event.Text, "logprobs": []any{}}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
			case "tool_call":
				if event.ToolCall == nil {
					continue
				}
				recorder.markOutput()
				call := sanitizedProviderToolCall(*event.ToolCall)
				outputIndex := len(output)
				item := responsesOutputItem{ID: newGatewayPublicID("fc_"), Type: "function_call", Status: "in_progress", CallID: call.ID, Name: call.Name, Arguments: ""}
				if err := writeNamedSSEJSON(w, flusher, "response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": outputIndex, "item": item}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "item_id": item.ID, "output_index": outputIndex, "delta": call.Arguments}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				if err := writeNamedSSEJSON(w, flusher, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "item_id": item.ID, "output_index": outputIndex, "arguments": call.Arguments}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
				item.Status = "completed"
				item.Arguments = call.Arguments
				output = append(output, item)
				if err := writeNamedSSEJSON(w, flusher, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": outputIndex, "item": item}); err != nil {
					_ = recorder.record(gatewayFailureClientDisconnected)
					return
				}
			case "error":
				_ = writeResponsesStreamError(w, flusher, "upstream_error", "The upstream model request failed.")
				_ = recorder.record(gatewayFailureUpstreamEvent)
				return
			case "done":
				if err := recorder.record(""); err != nil {
					_ = writeResponsesStreamError(w, flusher, "gateway_internal_error", "The Gateway could not process the request.")
					return
				}
				if textOutputIndex >= 0 {
					content := responsesOutputContent{Type: "output_text", Text: text.String(), Annotations: []any{}, Logprobs: []any{}}
					output[textOutputIndex].Status = "completed"
					output[textOutputIndex].Content = []responsesOutputContent{content}
					if err := writeNamedSSEJSON(w, flusher, "response.output_text.done", map[string]any{"type": "response.output_text.done", "item_id": textItemID, "output_index": textOutputIndex, "content_index": 0, "text": text.String(), "logprobs": []any{}}); err != nil {
						return
					}
					if err := writeNamedSSEJSON(w, flusher, "response.content_part.done", map[string]any{"type": "response.content_part.done", "item_id": textItemID, "output_index": textOutputIndex, "content_index": 0, "part": content}); err != nil {
						return
					}
					if err := writeNamedSSEJSON(w, flusher, "response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": textOutputIndex, "item": output[textOutputIndex]}); err != nil {
						return
					}
				}
				status, incomplete := responsesCompletionStatus(recorder.execution.StopReason)
				response := buildResponsesResponse(responseID, created, resolved.Alias, status, incomplete, output, recorder.execution.Usage, converted)
				eventName := "response.completed"
				if status == "incomplete" {
					eventName = "response.incomplete"
				}
				_ = writeNamedSSEJSON(w, flusher, eventName, map[string]any{"type": eventName, "response": response})
				return
			}
		}
	}
}

func buildResponsesResponse(id string, created int64, model, status string, incomplete *responsesIncomplete, output []responsesOutputItem, usage providers.Usage, converted convertedResponsesRequest) responsesResponse {
	if output == nil {
		output = []responsesOutputItem{}
	}
	return responsesResponse{
		ID:                 id,
		Object:             "response",
		CreatedAt:          created,
		Status:             status,
		Error:              nil,
		IncompleteDetails:  incomplete,
		Instructions:       converted.Instructions,
		MaxOutputTokens:    converted.MaxOutputTokens,
		Model:              model,
		Output:             output,
		ParallelToolCalls:  converted.ParallelToolCall,
		PreviousResponseID: nil,
		Reasoning:          responsesReasoningSummary{Effort: converted.ReasoningEffort, Summary: nil},
		ServiceTier:        converted.ServiceTier,
		Store:              false,
		Tools:              converted.ResponseTools,
		Usage:              responsesUsageValue(usage),
	}
}

func buildResponsesOutput(text string, calls []providers.ToolCall) []responsesOutputItem {
	output := make([]responsesOutputItem, 0, 1+len(calls))
	if text != "" {
		output = append(output, responsesOutputItem{
			ID:     newGatewayPublicID("msg_"),
			Type:   "message",
			Status: "completed",
			Role:   "assistant",
			Content: []responsesOutputContent{{
				Type: "output_text", Text: text, Annotations: []any{}, Logprobs: []any{},
			}},
		})
	}
	for _, providerCall := range calls {
		call := sanitizedProviderToolCall(providerCall)
		output = append(output, responsesOutputItem{ID: newGatewayPublicID("fc_"), Type: "function_call", Status: "completed", CallID: call.ID, Name: call.Name, Arguments: call.Arguments})
	}
	return output
}

type sanitizedToolCall struct {
	ID        string
	Name      string
	Arguments string
}

func sanitizedProviderToolCall(call providers.ToolCall) sanitizedToolCall {
	id := strings.TrimSpace(call.ID)
	if id == "" {
		id = newGatewayPublicID("call_")
	}
	name := strings.TrimSpace(call.Name)
	arguments := strings.TrimSpace(string(call.Input))
	if arguments == "" || !json.Valid([]byte(arguments)) {
		arguments = "{}"
	}
	return sanitizedToolCall{ID: id, Name: name, Arguments: arguments}
}

func responsesCompletionStatus(stopReason string) (string, *responsesIncomplete) {
	switch strings.ToLower(strings.TrimSpace(stopReason)) {
	case "max_tokens", "max_output_tokens", "length", "incomplete":
		return "incomplete", &responsesIncomplete{Reason: "max_output_tokens"}
	default:
		return "completed", nil
	}
}

func responsesUsageValue(usage providers.Usage) *responsesUsage {
	return &responsesUsage{
		InputTokens:         usage.InputTokens,
		InputTokensDetails:  responsesInputTokenDetails{CachedTokens: usage.CachedInputTokens},
		OutputTokens:        usage.OutputTokens,
		OutputTokensDetails: responsesOutputTokenDetails{ReasoningTokens: usage.ReasoningTokens},
		TotalTokens:         usage.InputTokens + usage.OutputTokens,
	}
}

func newResponseID() string {
	return newGatewayPublicID("resp_")
}

func newGatewayPublicID(prefix string) string {
	return prefix + strings.ReplaceAll(db.NewID(), "-", "")
}

func writeNamedSSEJSON(w http.ResponseWriter, flusher http.Flusher, eventName string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, encoded); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeResponsesStreamError(w http.ResponseWriter, flusher http.Flusher, code, message string) error {
	return writeNamedSSEJSON(w, flusher, "error", map[string]any{"type": "error", "code": code, "message": message})
}
