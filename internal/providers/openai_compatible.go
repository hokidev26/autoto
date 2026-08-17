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
	"sort"
	"strings"
	"sync"
	"time"

	"autoto/internal/config"
)

type OpenAICompatible struct {
	cfg                 config.OpenAICompatibleConfig
	client              *http.Client
	configErr           error
	modelCapabilitiesMu sync.RWMutex
	modelCapabilities   map[string]ModelCapabilities
}

var _ ModelCapabilityProvider = (*OpenAICompatible)(nil)

func NewOpenAICompatible(cfg config.OpenAICompatibleConfig) *OpenAICompatible {
	if cfg.Name == "" {
		cfg.Name = "openai-compatible"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	client, clientErr := providerHTTPClient(cfg, 90*time.Second)
	return &OpenAICompatible{cfg: cfg, client: client, configErr: errors.Join(validateProviderRuntimeConfig(cfg), clientErr)}
}

func (p *OpenAICompatible) Name() string { return p.cfg.Name }

func (p *OpenAICompatible) Configured() bool {
	return p != nil && p.configErr == nil && (strings.TrimSpace(p.cfg.APIKey) != "" || p.cfg.APIKeyOptional)
}

func (p *OpenAICompatible) Capabilities() Capabilities {
	// Custom /v1 relays are the usual way to add a GPT or Codex-compatible
	// endpoint. Auto still omits reasoning_effort, so APIs that ignore the
	// field keep working; an explicit level is what lets the composer drive
	// thinking strength after the user adds a provider.
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       p.cfg.ImageInput,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
}

func (p *OpenAICompatible) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	model = strings.TrimSpace(model)
	capabilities := p.catalogModelCapabilities(model)
	configured := configuredModelCapabilities(p.cfg, model)
	if len(capabilities.ReasoningEfforts) == 0 {
		capabilities.ReasoningEfforts = reasoningEffortsForKnownCodexIdentity(model)
	}
	capabilities.ImageGeneration = configured.ImageGeneration
	capabilities.ImageGenerationKnown = configured.ImageGenerationKnown
	capabilities.ContextTokenLimit = configured.ContextTokenLimit
	return capabilities
}

func (p *OpenAICompatible) applyRequestHeaders(req *http.Request) {
	if client := autotoClientHeaderValue(p.cfg); client != "" {
		req.Header.Set("X-Autoto-Client", client)
	}
	if p.cfg.InstallationID != "" {
		req.Header.Set("X-Autoto-Installation-ID", p.cfg.InstallationID)
	}
}

func (p *OpenAICompatible) ListModels(ctx context.Context) ([]string, error) {
	if p.configErr != nil {
		return nil, p.configErr
	}
	if p.cfg.APIKey == "" && !p.cfg.APIKeyOptional {
		return []string{p.cfg.Model}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.cfg.BaseURL, "/")+"/models", nil)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "models request could not be constructed")
	}
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	p.applyRequestHeaders(req)
	res, err := p.client.Do(req)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "models request failed")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return nil, providerUnavailableError(p.cfg.Name, fmt.Sprintf("models request failed (HTTP %d)", res.StatusCode))
	}
	models, capabilities, err := parseOpenAICompatibleModelCatalog(res.Body)
	if err != nil {
		return nil, providerUnavailableError(p.cfg.Name, "models response was invalid")
	}
	p.replaceModelCapabilities(capabilities)
	if len(models) == 0 {
		models = append(models, p.cfg.Model)
	}
	return models, nil
}

func openAICompatibleMessages(req GenerateRequest) []map[string]any {
	messages := make([]map[string]any, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, message := range req.Messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		blocks := normalizeContentBlocks(message)
		if len(blocks) == 0 {
			continue
		}
		var textBlocks []ContentBlock
		var toolUseBlocks []ContentBlock
		var toolResultBlocks []ContentBlock
		for _, block := range blocks {
			switch block.Type {
			case "tool_use":
				toolUseBlocks = append(toolUseBlocks, block)
			case "tool_result":
				toolResultBlocks = append(toolResultBlocks, block)
			default:
				textBlocks = append(textBlocks, block)
			}
		}
		if len(toolUseBlocks) > 0 {
			toolCalls := make([]map[string]any, 0, len(toolUseBlocks))
			for _, block := range toolUseBlocks {
				callID := strings.TrimSpace(block.ToolUseID)
				name := strings.TrimSpace(block.ToolName)
				if callID == "" || name == "" {
					continue
				}
				toolCalls = append(toolCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": openAIToolArgumentsString(block.Input)}})
			}
			if len(toolCalls) > 0 {
				content := strings.TrimSpace(contentBlocksText(textBlocks))
				messages = append(messages, map[string]any{"role": "assistant", "content": content, "tool_calls": toolCalls})
			}
			continue
		}
		if len(toolResultBlocks) > 0 {
			if text := strings.TrimSpace(contentBlocksText(textBlocks)); text != "" {
				messages = append(messages, map[string]any{"role": role, "content": text})
			}
			for _, block := range toolResultBlocks {
				callID := strings.TrimSpace(block.ToolUseID)
				if callID == "" {
					continue
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": openAIToolResultOutput(block)})
			}
			continue
		}
		if !contentBlocksHaveImage(blocks) {
			messages = append(messages, map[string]any{"role": role, "content": contentBlocksText(blocks)})
			continue
		}
		content := make([]map[string]any, 0, len(blocks))
		for _, block := range blocks {
			switch block.Type {
			case "image":
				if len(block.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(block.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(block.Data)}})
			default:
				text := strings.TrimSpace(block.Text)
				if text != "" {
					content = append(content, map[string]any{"type": "text", "text": text})
				}
			}
		}
		if len(content) > 0 {
			messages = append(messages, map[string]any{"role": role, "content": content})
		}
	}
	return messages
}

func normalizeContentBlocks(message Message) []ContentBlock {
	if len(message.Blocks) > 0 {
		return message.Blocks
	}
	content := strings.TrimSpace(message.Content)
	if content == "" {
		return nil
	}
	return []ContentBlock{{Type: "text", Text: content}}
}

func contentBlocksHaveImage(blocks []ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "image" && len(block.Data) > 0 {
			return true
		}
	}
	return false
}

func contentBlocksText(blocks []ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, strings.TrimSpace(block.Text))
			continue
		}
		switch block.Type {
		case "image":
			name := strings.TrimSpace(block.Filename)
			if name == "" {
				name = "image"
			}
			parts = append(parts, fmt.Sprintf("[图片附件 %s 已上传；当前 provider adapter 未以视觉格式传递该图片。]", name))
		case "tool_use":
			parts = append(parts, fmt.Sprintf("[Tool requested: %s (%s)]", block.ToolName, block.ToolUseID))
		case "tool_result":
			status := "completed"
			if block.IsError {
				status = "error"
			}
			parts = append(parts, fmt.Sprintf("[Tool result for %s (%s), %s]\n%s", block.ToolName, block.ToolUseID, status, strings.TrimSpace(block.Output)))
		case "image_generation":
			parts = append(parts, "[Generated image from earlier assistant response]")
		}
	}
	return strings.Join(parts, "\n\n")
}

func (p *OpenAICompatible) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	req = applyConfiguredClientIdentity(p.cfg, req)
	if req.EffectiveScenario() == CallScenarioGateway && p.cfg.Profile == config.ProviderProfileCLIProxyAPI {
		return nil, fmt.Errorf("%w: provider profile %q", ErrGatewayOAuthUnsupported, p.cfg.Profile)
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	if !p.cfg.ImageInput {
		for _, message := range req.Messages {
			if contentBlocksHaveImage(normalizeContentBlocks(message)) {
				return nil, fmt.Errorf("provider %q does not support image input", p.cfg.Name)
			}
		}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	// Effort is validated against the model: xhigh/max/ultra exist only on some
	// Codex identities and catalog entries, so the provider-wide low/medium/high
	// list would reject a level the chosen model genuinely serves.
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, CapabilitiesForModel(p.Capabilities(), p.ModelCapabilities(model)), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	if p.cfg.APIKey == "" && !p.cfg.APIKeyOptional {
		return nil, providerUnavailableError(p.cfg.Name, "API key is not configured")
	}
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		messages := openAICompatibleMessages(req)
		payload := map[string]any{
			"model":          model,
			"messages":       messages,
			"stream":         true,
			"stream_options": map[string]bool{"include_usage": true},
		}
		if req.MaxOutputTokens > 0 {
			payload["max_tokens"] = req.MaxOutputTokens
		}
		if reasoningEffort != "" {
			payload["reasoning_effort"] = reasoningEffort
		}
		if tools := openAICompatibleTools(req.Tools); len(tools) > 0 {
			payload["tools"] = tools
		}
		data, err := json.Marshal(payload)
		if err != nil {
			out <- Event{Type: "error", Text: providerRequestFailedText(p.cfg.Name)}
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(data))
		if err != nil {
			out <- Event{Type: "error", Text: providerRequestFailedText(p.cfg.Name)}
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		if p.cfg.APIKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
		}
		p.applyRequestHeaders(httpReq)
		out <- newDispatchEvent(p.cfg.Name, model, configuredCredentialID)
		res, err := p.client.Do(httpReq)
		if err != nil {
			out <- Event{Type: "error", Text: providerRequestFailedText(p.cfg.Name)}
			return
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			out <- Event{Type: "error", Text: providerHTTPFailedText(p.cfg.Name, res.StatusCode)}
			return
		}
		if strings.Contains(strings.ToLower(res.Header.Get("Content-Type")), "text/event-stream") {
			handleOpenAICompatibleStream(out, res.Body)
			return
		}
		handleOpenAICompatibleJSON(out, res.Body)
	}()
	return out, nil
}

func openAICompatibleTools(tools []ToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		function := map[string]any{"name": name, "parameters": openAIToolSchema(tool.Schema)}
		if description := strings.TrimSpace(tool.Description); description != "" {
			function["description"] = description
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	return out
}

type openAICompatibleUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u openAICompatibleUsage) toUsage() Usage {
	usage := Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CachedInputTokens: u.InputTokensDetails.CachedTokens, ReasoningTokens: u.OutputTokensDetails.ReasoningTokens}
	if usage.InputTokens == 0 {
		usage.InputTokens = u.PromptTokens
		usage.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	if usage.OutputTokens == 0 {
		usage.OutputTokens = u.CompletionTokens
		usage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	return usage
}

func handleOpenAICompatibleJSON(out chan<- Event, reader io.Reader) {
	var body struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage openAICompatibleUsage `json:"usage"`
	}
	if err := json.NewDecoder(reader).Decode(&body); err != nil {
		out <- Event{Type: "error", Text: err.Error()}
		return
	}
	usage := body.Usage.toUsage()
	if usage != (Usage{}) {
		out <- Event{Type: "usage", Usage: &usage}
	}
	if len(body.Choices) == 0 {
		out <- Event{Type: "error", Text: "OpenAI-compatible response contained no choices"}
		return
	}
	choice := body.Choices[0]
	message := choice.Message
	if message.Content != "" {
		out <- Event{Type: "text", Text: message.Content}
	}
	for _, call := range message.ToolCalls {
		if strings.TrimSpace(call.Type) != "" && call.Type != "function" {
			continue
		}
		emitOpenAICompatibleToolCall(out, call.ID, call.Function.Name, call.Function.Arguments)
	}
	stopReason := strings.ToLower(strings.TrimSpace(choice.FinishReason))
	if stopReason == "" {
		stopReason = "stop"
	}
	out <- Event{Type: "done", Done: true, StopReason: stopReason}
}

type openAICompatibleStreamToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
	// streamed marks how much of Arguments has already been forwarded as a
	// tool_call_delta. Fragments that arrive before the call's id and name are
	// known accumulate here and are flushed on the first delta after both.
	streamed int
}

func handleOpenAICompatibleStream(out chan<- Event, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	toolCalls := map[int]*openAICompatibleStreamToolCall{}
	var order []int
	var usage Usage
	var stopReason string
	sawDoneMarker := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			sawDoneMarker = true
			break
		}
		var chunk struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage openAICompatibleUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			out <- Event{Type: "error", Text: err.Error()}
			return
		}
		if parsedUsage := chunk.Usage.toUsage(); parsedUsage != (Usage{}) {
			usage = parsedUsage
		}
		for _, choice := range chunk.Choices {
			if reason := strings.ToLower(strings.TrimSpace(choice.FinishReason)); reason != "" {
				stopReason = reason
			}
			if choice.Delta.ReasoningContent != "" {
				out <- Event{Type: "reasoning", Text: choice.Delta.ReasoningContent}
			}
			if choice.Delta.Content != "" {
				out <- Event{Type: "text", Text: choice.Delta.Content}
			}
			for _, delta := range choice.Delta.ToolCalls {
				if strings.TrimSpace(delta.Type) != "" && delta.Type != "function" {
					continue
				}
				call, ok := toolCalls[delta.Index]
				if !ok {
					call = &openAICompatibleStreamToolCall{}
					toolCalls[delta.Index] = call
					order = append(order, delta.Index)
				}
				if strings.TrimSpace(delta.ID) != "" {
					call.ID = delta.ID
				}
				if strings.TrimSpace(delta.Function.Name) != "" {
					call.Name = delta.Function.Name
				}
				if delta.Function.Arguments != "" {
					call.Arguments.WriteString(delta.Function.Arguments)
				}
				// Forward argument fragments live so the UI can preview the call
				// while it is still being composed. The complete tool_call emitted
				// after the stream ends stays authoritative.
				if strings.TrimSpace(call.ID) != "" && strings.TrimSpace(call.Name) != "" {
					if pending := call.Arguments.String(); len(pending) > call.streamed {
						out <- Event{Type: "tool_call_delta", Text: pending[call.streamed:], ToolCall: &ToolCall{ID: strings.TrimSpace(call.ID), Name: strings.TrimSpace(call.Name)}}
						call.streamed = len(pending)
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		out <- Event{Type: "error", Text: err.Error()}
		return
	}
	if usage != (Usage{}) {
		out <- Event{Type: "usage", Usage: &usage}
	}
	if stopReason == "" && !sawDoneMarker {
		out <- Event{Type: "error", Text: "OpenAI-compatible stream closed before a terminal event"}
		return
	}
	if stopReason == "" {
		stopReason = "stop"
	}
	sort.Ints(order)
	for _, index := range order {
		call := toolCalls[index]
		emitOpenAICompatibleToolCall(out, call.ID, call.Name, call.Arguments.String())
	}
	out <- Event{Type: "done", Done: true, StopReason: stopReason}
}

func emitOpenAICompatibleToolCall(out chan<- Event, id, name, arguments string) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return
	}
	out <- Event{Type: "tool_call", ToolCall: &ToolCall{ID: id, Name: name, Input: openAIToolArgumentsRaw(arguments)}}
}
