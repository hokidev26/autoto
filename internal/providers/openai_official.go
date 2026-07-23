package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"autoto/internal/config"
)

type OpenAIOfficial struct {
	cfg       config.ProviderConfig
	client    openai.Client
	configErr error
}

func NewOpenAIOfficial(cfg config.ProviderConfig) *OpenAIOfficial {
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4.1-mini"
	}
	httpClient, clientErr := providerHTTPClient(cfg, 90*time.Second)
	configErr := errors.Join(validateProviderRuntimeConfig(cfg), clientErr)
	opts := make([]option.RequestOption, 0, 5)
	opts = append(opts, option.WithHTTPClient(httpClient))
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if configErr == nil {
		if client := autotoClientHeaderValue(cfg); client != "" {
			opts = append(opts, option.WithHeader("X-Autoto-Client", client))
		}
		if cfg.InstallationID != "" {
			opts = append(opts, option.WithHeader("X-Autoto-Installation-ID", cfg.InstallationID))
		}
	}
	return &OpenAIOfficial{cfg: cfg, client: openai.NewClient(opts...), configErr: configErr}
}

func (p *OpenAIOfficial) Name() string { return p.cfg.Name }

func (p *OpenAIOfficial) Configured() bool {
	return p != nil && p.configErr == nil && strings.TrimSpace(p.cfg.APIKey) != ""
}

func (p *OpenAIOfficial) Capabilities() Capabilities {
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       true,
		ImageGeneration:  true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
}

func (p *OpenAIOfficial) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

func (p *OpenAIOfficial) ListModels(ctx context.Context) ([]string, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	if p.cfg.APIKey == "" {
		return []string{p.cfg.Model}, nil
	}
	page, err := p.client.Models.List(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	if len(models) == 0 && p.cfg.Model != "" {
		models = append(models, p.cfg.Model)
	}
	return models, nil
}

func (p *OpenAIOfficial) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, p.Capabilities(), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey == "" {
		return nil, providerUnavailableError(p.cfg.Name, "API key is not configured")
	}
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		model := req.Model
		if model == "" {
			model = p.cfg.Model
		}
		out <- newDispatchEvent(p.cfg.Name, model, configuredCredentialID)
		params := responses.ResponseNewParams{Model: model}
		if req.MaxOutputTokens > 0 {
			params.MaxOutputTokens = param.NewOpt(req.MaxOutputTokens)
		}
		if reasoningEffort != "" {
			params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(reasoningEffort)}
		}
		if req.SystemPrompt != "" {
			params.Instructions = param.NewOpt(req.SystemPrompt)
		}
		enableImageGeneration := req.EnableImageGeneration && p.Capabilities().ImageGeneration && p.ModelCapabilities(model).ImageGeneration
		if len(req.Tools) > 0 || enableImageGeneration || openAIMessagesRequireStructuredInput(req.Messages) {
			params.Input = responses.ResponseNewParamsInputUnion{OfInputItemList: openAIResponseInput(req.Messages)}
		} else {
			input := renderTranscript(req.Messages)
			if input == "" {
				input = "Continue."
			}
			params.Input = responses.ResponseNewParamsInputUnion{OfString: param.NewOpt(input)}
		}
		if tools := openAIToolParams(req.Tools, enableImageGeneration); len(tools) > 0 {
			params.Tools = tools
		}
		stream := p.client.Responses.NewStreaming(ctx, params)
		defer stream.Close()
		sawDelta := false
		emittedToolCalls := map[string]bool{}
		imageTracker := newImageGenerationTracker()
		for stream.Next() {
			event := stream.Current()
			imageEvents, imageErr := imageTracker.processJSON(event.RawJSON())
			if imageErr != nil {
				out <- Event{Type: "error", Text: imageErr.Error()}
				return
			}
			for _, imageEvent := range imageEvents {
				out <- imageEvent
			}
			switch event.Type {
			case "response.output_text.delta":
				delta := event.AsResponseOutputTextDelta()
				if delta.Delta != "" {
					sawDelta = true
					out <- Event{Type: "text", Text: delta.Delta}
				}
			case "response.output_text.done":
				done := event.AsResponseOutputTextDone()
				if !sawDelta && done.Text != "" {
					sawDelta = true
					out <- Event{Type: "text", Text: done.Text}
				}
			case "response.output_item.done":
				done := event.AsResponseOutputItemDone()
				emitOpenAIFunctionToolCallFromOutputItem(out, done.Item, emittedToolCalls)
			case "response.completed":
				completed := event.AsResponseCompleted()
				if errText := openAIResponseErrorText(completed.Response); errText != "" {
					out <- Event{Type: "error", Text: errText}
					return
				}
				emitOpenAIFunctionToolCallsFromResponse(out, completed.Response, emittedToolCalls)
				emitOpenAIUsage(out, completed.Response)
				out <- Event{Type: "done", Done: true}
				return
			case "response.incomplete":
				incomplete := event.AsResponseIncomplete()
				emitOpenAIFunctionToolCallsFromResponse(out, incomplete.Response, emittedToolCalls)
				emitOpenAIUsage(out, incomplete.Response)
				out <- Event{Type: "done", Done: true, StopReason: openAIIncompleteStopReason(incomplete.Response)}
				return
			case "response.failed":
				failed := event.AsResponseFailed()
				emitOpenAIUsage(out, failed.Response)
				if errText := openAIResponseErrorText(failed.Response); errText != "" {
					out <- Event{Type: "error", Text: errText}
					return
				}
				out <- Event{Type: "error", Text: "openai response failed"}
				return
			case "error":
				errEvent := event.AsError()
				out <- Event{Type: "error", Text: openAIStreamErrorText(errEvent)}
				return
			}
		}
		if err := stream.Err(); err != nil {
			out <- Event{Type: "error", Text: err.Error()}
			return
		}
		out <- Event{Type: "done", Done: true}
	}()
	return out, nil
}

func openAIToolParams(tools []ToolSpec, enableImageGeneration bool) []responses.ToolUnionParam {
	params := make([]responses.ToolUnionParam, 0, len(tools)+1)
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		function := responses.FunctionToolParam{
			Name:       name,
			Parameters: openAIToolSchema(tool.Schema),
			Strict:     openai.Bool(false),
		}
		if description := strings.TrimSpace(tool.Description); description != "" {
			function.Description = openai.String(description)
		}
		params = append(params, responses.ToolUnionParam{OfFunction: &function})
	}
	if enableImageGeneration {
		params = append(params, responses.ToolUnionParam{OfImageGeneration: &responses.ToolImageGenerationParam{OutputFormat: "png"}})
	}
	return params
}

func openAIToolSchema(schema any) map[string]any {
	if schema == nil {
		return defaultOpenAIToolSchema()
	}
	if typed, ok := schema.(map[string]any); ok && len(typed) > 0 {
		return typed
	}
	if raw, ok := schema.(json.RawMessage); ok && len(raw) > 0 {
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err == nil && len(decoded) > 0 {
			return decoded
		}
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return defaultOpenAIToolSchema()
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || len(decoded) == 0 {
		return defaultOpenAIToolSchema()
	}
	return decoded
}

func defaultOpenAIToolSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func openAIMessagesRequireStructuredInput(messages []Message) bool {
	for _, message := range messages {
		for _, block := range normalizeContentBlocks(message) {
			switch block.Type {
			case "image", "tool_use", "tool_result", "image_generation":
				return true
			}
		}
	}
	return false
}

func openAIResponseInput(messages []Message) responses.ResponseInputParam {
	items := make([]responses.ResponseInputItemUnionParam, 0, len(messages))
	for _, message := range messages {
		role := openAIMessageRole(message.Role)
		blocks := normalizeContentBlocks(message)
		if len(blocks) == 0 {
			continue
		}
		var messageBlocks []ContentBlock
		var toolUseBlocks []ContentBlock
		var toolResultBlocks []ContentBlock
		var imageGenerationBlocks []ContentBlock
		for _, block := range blocks {
			switch block.Type {
			case "tool_use":
				toolUseBlocks = append(toolUseBlocks, block)
			case "tool_result":
				toolResultBlocks = append(toolResultBlocks, block)
			case "image_generation":
				imageGenerationBlocks = append(imageGenerationBlocks, block)
			default:
				messageBlocks = append(messageBlocks, block)
			}
		}
		content := make(responses.ResponseInputMessageContentListParam, 0, len(messageBlocks))
		for _, block := range messageBlocks {
			if block.Type == "image" && len(block.Data) > 0 {
				mimeType := strings.TrimSpace(block.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
				image.OfInputImage.ImageURL = param.NewOpt("data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(block.Data))
				content = append(content, image)
				continue
			}
			if text := strings.TrimSpace(block.Text); text != "" {
				content = append(content, responses.ResponseInputContentParamOfInputText(text))
			}
		}
		if len(content) > 0 {
			items = append(items, responses.ResponseInputItemParamOfMessage(content, role))
		}
		for _, block := range toolUseBlocks {
			callID := strings.TrimSpace(block.ToolUseID)
			name := strings.TrimSpace(block.ToolName)
			if callID == "" || name == "" {
				continue
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCall(openAIToolArgumentsString(block.Input), callID, name))
		}
		for _, block := range toolResultBlocks {
			callID := strings.TrimSpace(block.ToolUseID)
			if callID == "" {
				continue
			}
			items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(callID, openAIToolResultOutput(block)))
		}
		for _, block := range imageGenerationBlocks {
			id := strings.TrimSpace(block.GenerationID)
			if id == "" || len(block.Data) == 0 {
				continue
			}
			status := strings.TrimSpace(block.Status)
			if status == "" {
				status = "completed"
			}
			items = append(items, responses.ResponseInputItemParamOfImageGenerationCall(id, base64.StdEncoding.EncodeToString(block.Data), status))
		}
	}
	if len(items) == 0 {
		items = append(items, responses.ResponseInputItemParamOfMessage("Continue.", responses.EasyInputMessageRoleUser))
	}
	return responses.ResponseInputParam(items)
}

func openAIMessageRole(role string) responses.EasyInputMessageRole {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return responses.EasyInputMessageRoleAssistant
	case "system":
		return responses.EasyInputMessageRoleSystem
	case "developer":
		return responses.EasyInputMessageRoleDeveloper
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func openAIToolArgumentsString(input json.RawMessage) string {
	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return "{}"
	}
	if json.Valid([]byte(trimmed)) {
		return trimmed
	}
	fallback, _ := json.Marshal(map[string]string{"arguments": trimmed})
	return string(fallback)
}

func openAIToolArgumentsRaw(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	fallback, _ := json.Marshal(map[string]string{"arguments": trimmed})
	return json.RawMessage(fallback)
}

func openAIToolResultOutput(block ContentBlock) string {
	output := strings.TrimSpace(block.Output)
	if output == "" {
		output = "(empty output)"
	}
	if block.IsError {
		return "ERROR: " + output
	}
	return output
}

func emitOpenAIFunctionToolCallsFromResponse(out chan<- Event, response responses.Response, emitted map[string]bool) {
	for _, item := range response.Output {
		emitOpenAIFunctionToolCallFromOutputItem(out, item, emitted)
	}
}

func emitOpenAIFunctionToolCallFromOutputItem(out chan<- Event, item responses.ResponseOutputItemUnion, emitted map[string]bool) {
	if item.Type != "function_call" {
		return
	}
	call := item.AsFunctionCall()
	id := strings.TrimSpace(call.CallID)
	if id == "" {
		id = strings.TrimSpace(call.ID)
	}
	name := strings.TrimSpace(call.Name)
	if id == "" || name == "" || emitted[id] {
		return
	}
	emitted[id] = true
	out <- Event{Type: "tool_call", ToolCall: &ToolCall{ID: id, Name: name, Input: openAIToolArgumentsRaw(call.Arguments)}}
}

func emitOpenAIUsage(out chan<- Event, response responses.Response) {
	usage := openAIUsageFromResponse(response)
	if usage == (Usage{}) {
		return
	}
	out <- Event{Type: "usage", Usage: &usage}
}

func openAIUsageFromResponse(response responses.Response) Usage {
	return Usage{
		InputTokens:       response.Usage.InputTokens,
		OutputTokens:      response.Usage.OutputTokens,
		CachedInputTokens: response.Usage.InputTokensDetails.CachedTokens,
		ReasoningTokens:   response.Usage.OutputTokensDetails.ReasoningTokens,
	}
}

func openAIResponseErrorText(response responses.Response) string {
	if strings.TrimSpace(response.Error.Message) == "" {
		return ""
	}
	if strings.TrimSpace(string(response.Error.Code)) != "" {
		return fmt.Sprintf("openai response error (%s): %s", response.Error.Code, response.Error.Message)
	}
	return fmt.Sprintf("openai response error: %s", response.Error.Message)
}

func openAIIncompleteStopReason(response responses.Response) string {
	if strings.TrimSpace(response.IncompleteDetails.Reason) != "" {
		return response.IncompleteDetails.Reason
	}
	if response.Status != "" {
		return string(response.Status)
	}
	return "incomplete"
}

func openAIStreamErrorText(event responses.ResponseErrorEvent) string {
	if strings.TrimSpace(event.Code) != "" {
		return fmt.Sprintf("openai stream error (%s): %s", event.Code, event.Message)
	}
	return fmt.Sprintf("openai stream error: %s", event.Message)
}

func renderTranscript(messages []Message) string {
	var builder strings.Builder
	for _, message := range messages {
		content := strings.TrimSpace(contentBlocksText(normalizeContentBlocks(message)))
		if content == "" {
			continue
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(strings.ToUpper(role[:1]))
		if len(role) > 1 {
			builder.WriteString(strings.ToLower(role[1:]))
		}
		builder.WriteString(": ")
		builder.WriteString(content)
	}
	return builder.String()
}
