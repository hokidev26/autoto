package providers

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"autoto/internal/config"
	"autoto/internal/geminiauth"
	"autoto/internal/subscriptionauth"
)

const (
	geminiCloudCodeProductionBaseURL = "https://cloudcode-pa.googleapis.com"
	geminiCloudCodeMaxResponseBytes  = 16 << 20
)

type GeminiProvider struct {
	cfg          config.ProviderConfig
	accounts     *subscriptionProviderAccounts
	client       *http.Client
	baseURL      string
	configErr    error
	refresh      subscriptionRefreshFunc
	fetchProject func(context.Context, string) (string, error)
}

func NewGeminiProvider(cfg config.ProviderConfig) *GeminiProvider {
	cfg = normalizeGeminiProviderConfig(cfg)
	apiConfig := cfg
	// OAuth/model requests have fixed protocol identity headers. Keep proxy/TLS
	// settings but never forward arbitrary provider headers to Google OAuth.
	apiConfig.RequestHeaders = nil
	apiConfig.UserAgent = ""
	client, clientErr := providerHTTPClient(apiConfig, 5*time.Minute)
	refreshClient, refreshClientErr := providerHTTPClient(apiConfig, 30*time.Second)
	provider := &GeminiProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderGemini),
		client:    client,
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		configErr: errors.Join(validateProviderRuntimeConfig(cfg), validateGeminiProductionBaseURL(cfg.BaseURL), clientErr, refreshClientErr),
	}
	if refreshClient != nil {
		authClient := geminiauth.New(refreshClient)
		provider.refresh = func(ctx context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			tokens, err := authClient.RefreshTokens(ctx, credential.RefreshToken)
			if err != nil || tokens == nil {
				return subscriptionauth.TokenUpdate{}, err
			}
			return subscriptionauth.TokenUpdate{
				AccessToken:   tokens.AccessToken,
				RefreshToken:  tokens.RefreshToken,
				IDToken:       tokens.IDToken,
				TokenType:     tokens.TokenType,
				ExpiresAt:     tokens.ExpiresAt,
				Email:         credential.Email,
				Subject:       credential.Subject,
				Scope:         credential.Scope,
				ProjectID:     credential.ProjectID,
				TokenEndpoint: credential.TokenEndpoint,
			}, nil
		}
		provider.fetchProject = authClient.FetchProjectID
	}
	return provider
}

func newGeminiProviderForTest(cfg config.ProviderConfig, client *http.Client, baseURL string) *GeminiProvider {
	cfg = normalizeGeminiProviderConfig(cfg)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &GeminiProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderGemini),
		client:    client,
		baseURL:   cfg.BaseURL,
		configErr: validateSubscriptionTestBaseURL(cfg.BaseURL),
	}
}

func normalizeGeminiProviderConfig(cfg config.ProviderConfig) config.ProviderConfig {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = config.ProviderTypeGemini
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = config.ProviderTypeGemini
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = geminiCloudCodeProductionBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "gemini-3-flash"
	}
	return cfg
}

func validateGeminiProductionBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "cloudcode-pa.googleapis.com") || strings.TrimRight(parsed.EscapedPath(), "/") != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Gemini Base URL only allows the official HTTPS Cloud Code endpoint")
	}
	return nil
}

func (p *GeminiProvider) Name() string {
	if p == nil {
		return config.ProviderTypeGemini
	}
	return p.cfg.Name
}

func (p *GeminiProvider) Configured() bool {
	return p != nil && p.configErr == nil && p.accounts.configured()
}

func (p *GeminiProvider) ConfiguredForScenario(scenario CallScenario) bool {
	return p != nil && p.configErr == nil && p.accounts.configuredForScenario(scenario)
}

func (p *GeminiProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	return p != nil && p.configErr == nil && p.accounts.availableForScenario(ctx, availability)
}

func (p *GeminiProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p != nil {
		p.accounts.setAccountTelemetry(telemetry)
	}
}

func (p *GeminiProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p != nil {
		p.accounts.setGatewayAccountPolicy(policy)
	}
}

func (p *GeminiProvider) Capabilities() Capabilities {
	return Capabilities{
		Tools:            true,
		Streaming:        true,
		ImageInput:       true,
		ImageGeneration:  true,
		Reasoning:        true,
		ReasoningEffort:  true,
		ReasoningEfforts: []string{"low", "medium", "high"},
	}
}

func (p *GeminiProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

func (p *GeminiProvider) ListModels(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeGemini, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	staticModels := configuredSubscriptionModels(p.cfg)
	accounts, accountErr := p.accounts.enabledAccounts()
	if accountErr != nil {
		if len(staticModels) > 0 {
			return staticModels, nil
		}
		return nil, accountErr
	}
	models := make([]string, 0, len(staticModels))
	seen := make(map[string]struct{}, len(staticModels))
	var lastErr error
	for _, item := range accounts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prepared, err := p.accounts.prepareCredential(ctx, item, p.refresh)
		if err != nil {
			lastErr = err
			p.accounts.recordAttempt(ctx, item.ID, false, 0, "credential_unavailable", err)
			continue
		}
		prepared, err = p.ensureProjectID(ctx, prepared)
		if err != nil {
			lastErr = err
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "project_unavailable", err)
			continue
		}
		// fetchAvailableModels rather than loadCodeAssist: it returns the model
		// list and each model's remaining quota in the same round-trip, and
		// loadCodeAssist carries no quota at all. "project" is required for
		// correctness, not for a 200 — omitting it still answers, but reports
		// full quota for accounts that are actually exhausted.
		body, _ := json.Marshal(map[string]any{"project": strings.TrimSpace(prepared.ProjectID)})
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1internal:fetchAvailableModels", bytes.NewReader(body))
		if requestErr != nil {
			lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
			continue
		}
		p.applyHeaders(request, prepared.Credential, false)
		response, requestErr := p.client.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "network_error", lastErr)
			continue
		}
		if response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			logGeminiRejection(status, "", prepared.ProjectID, response.Body)
			response.Body.Close()
			lastErr = newSubscriptionHTTPError(p.cfg.Name, status, "models_request_failed")
			p.accounts.recordAttempt(ctx, prepared.ID, false, status, "models_request_failed", lastErr)
			continue
		}
		accountModels, modelQuotas, parseErr := parseGeminiAvailableModels(response.Body)
		response.Body.Close()
		if parseErr != nil {
			lastErr = newSubscriptionNetworkError(p.cfg.Name, parseErr)
			p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, "models_response_invalid", lastErr)
			continue
		}
		p.accounts.recordAttempt(ctx, prepared.ID, true, response.StatusCode, "", nil)
		p.accounts.recordModelQuotas(ctx, prepared.ID, modelQuotas)
		for _, model := range accountModels {
			appendUniqueModel(&models, seen, model)
		}
	}
	for _, model := range staticModels {
		appendUniqueModel(&models, seen, model)
	}
	if len(models) > 0 {
		return models, nil
	}
	if lastErr != nil {
		return nil, sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr)
	}
	return nil, providerUnavailableError(p.cfg.Name, "models are unavailable")
}

func (p *GeminiProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeGemini, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	reasoningEffort, err := normalizeReasoningEffortForCapabilities(req.ReasoningEffort, p.Capabilities(), p.cfg.Name)
	if err != nil {
		return nil, err
	}
	accounts, err := p.accounts.accountsForRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}

	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var lastErr error
		for _, item := range accounts {
			if ctx.Err() != nil {
				return
			}
			prepared, prepareErr := p.accounts.prepareCredential(ctx, item, p.refresh)
			if prepareErr != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = prepareErr
				p.accounts.recordAttempt(ctx, item.ID, false, 0, "credential_unavailable", prepareErr)
				continue
			}
			prepared, prepareErr = p.ensureProjectID(ctx, prepared)
			if prepareErr != nil {
				lastErr = prepareErr
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "project_unavailable", prepareErr)
				continue
			}
			payload := buildGeminiCloudCodePayload(req, model, prepared.ProjectID, reasoningEffort)
			data, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				lastErr = newSubscriptionNetworkError(p.cfg.Name, marshalErr)
				continue
			}
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1internal:streamGenerateContent?alt=sse", bytes.NewReader(data))
			if requestErr != nil {
				lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
				continue
			}
			p.applyHeaders(request, prepared.Credential, true)
			response, requestErr := p.client.Do(request)
			if requestErr != nil {
				if ctx.Err() != nil {
					return
				}
				lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "network_error", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				p.emitFinalError(ctx, out, model, prepared.ID, lastErr)
				return
			}
			if response.StatusCode >= http.StatusMultipleChoices {
				status := response.StatusCode
				logGeminiRejection(status, model, prepared.ProjectID, response.Body)
				response.Body.Close()
				lastErr = newSubscriptionHTTPError(p.cfg.Name, status, "model_request_failed")
				p.accounts.recordAttempt(ctx, prepared.ID, false, status, "model_request_failed", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				p.emitFinalError(ctx, out, model, prepared.ID, lastErr)
				return
			}
			dispatchEmitted := false
			emitDispatch := func() bool {
				if dispatchEmitted {
					return true
				}
				dispatchEmitted = emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, prepared.ID))
				return dispatchEmitted
			}
			outcome := consumeGeminiCloudCodeAttempt(ctx, out, response.Body, emitDispatch, p.cfg.Name)
			response.Body.Close()
			if ctx.Err() != nil {
				return
			}
			if outcome.err != nil {
				lastErr = outcome.err
				p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, outcome.code, outcome.err)
				if shouldTryNextSubscriptionAccount(ctx, outcome.err, outcome.emittedContent) {
					continue
				}
				if !emitDispatch() {
					return
				}
				_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, outcome.err).Error()})
				return
			}
			p.accounts.recordAttempt(ctx, prepared.ID, true, response.StatusCode, "", nil)
			return
		}
		if lastErr == nil {
			lastErr = providerUnavailableError(p.cfg.Name, "no usable subscription account")
		}
		if ctx.Err() == nil {
			_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr).Error()})
		}
	}()
	return out, nil
}

func (p *GeminiProvider) ensureProjectID(ctx context.Context, item subscriptionauth.StoredCredential) (subscriptionauth.StoredCredential, error) {
	if strings.TrimSpace(item.ProjectID) != "" {
		return item, nil
	}
	if p.fetchProject == nil {
		return item, providerUnavailableError(p.cfg.Name, "Gemini Cloud Code project is not configured")
	}
	projectID, err := p.fetchProject(ctx, item.AccessToken)
	if err != nil || strings.TrimSpace(projectID) == "" {
		if ctx.Err() != nil {
			return item, ctx.Err()
		}
		return item, providerUnavailableError(p.cfg.Name, "Gemini Cloud Code project could not be resolved")
	}
	update := mergeSubscriptionTokenUpdate(item.Credential, subscriptionauth.TokenUpdate{
		AccessToken:   item.AccessToken,
		RefreshToken:  item.RefreshToken,
		IDToken:       item.IDToken,
		TokenType:     item.TokenType,
		ExpiresAt:     item.ExpiresAt,
		Email:         item.Email,
		Subject:       item.Subject,
		Scope:         item.Scope,
		ProjectID:     strings.TrimSpace(projectID),
		TokenEndpoint: item.TokenEndpoint,
	})
	updated, updateErr := p.accounts.store.UpdateTokens(item.ID, update)
	if updateErr != nil {
		return item, providerUnavailableError(p.cfg.Name, "Gemini Cloud Code project could not be saved")
	}
	return updated, nil
}

func (p *GeminiProvider) emitFinalError(ctx context.Context, out chan<- Event, model, accountID string, err error) {
	if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, accountID)) {
		return
	}
	_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, err).Error()})
}

func (p *GeminiProvider) applyHeaders(request *http.Request, credential subscriptionauth.Credential, streaming bool) {
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(credential.AccessToken))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", geminiauth.RequestUserAgent)
	if streaming {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "*/*")
	}
	request.Close = true
}

// logGeminiRejection records why Cloud Code refused a request. The error
// returned to callers deliberately carries only the numeric status, because
// upstream bodies are untrusted (see providerHTTPFailedText), which left a
// rejected payload with no diagnosable cause at all: a single invalid field made
// every request fail with a bare 400. Google answers with a structured
// google.rpc.BadRequest naming the offending field, so that message is worth
// having locally. It stays at debug level, never reaches an API response, and is
// bounded so a hostile body cannot flood the log.
func logGeminiRejection(status int, model, projectID string, body io.Reader) {
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil || len(raw) == 0 {
		return
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	detail := strings.Join(strings.Fields(string(raw)), " ")
	if json.Unmarshal(raw, &envelope) == nil && strings.TrimSpace(envelope.Error.Message) != "" {
		detail = strings.Join(strings.Fields(envelope.Error.Message), " ")
	}
	if len(detail) > 600 {
		detail = detail[:600]
	}
	slog.Debug("gemini cloud code rejected request", "status", status, "model", model, "project", projectID, "upstreamStatus", envelope.Error.Status, "detail", detail)
}

// buildGeminiCloudCodePayload assembles the Cloud Code request envelope.
//
// Cloud Code is protobuf-backed and rejects any unknown field outright with
// 400 INVALID_ARGUMENT, naming only the first offender. A "stream": true here
// inside the inner request object therefore broke every single model call until
// it was removed; streaming is negotiated by the ?alt=sse query parameter on the
// URL, not by the body. Do not add a field here without confirming the control
// plane accepts it — a stray key does not degrade behaviour, it disables the
// provider completely.
func buildGeminiCloudCodePayload(req GenerateRequest, model, projectID, reasoningEffort string) map[string]any {
	contents, system := geminiCloudCodeContents(req.Messages, req.SystemPrompt)
	request := map[string]any{
		"contents":  contents,
		"sessionId": uuid.NewString(),
	}
	if system != "" {
		request["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": system}}}
	}
	generation := map[string]any{}
	if req.MaxOutputTokens > 0 {
		generation["maxOutputTokens"] = req.MaxOutputTokens
	}
	if reasoningEffort != "" {
		generation["thinkingConfig"] = map[string]any{"thinkingLevel": reasoningEffort, "includeThoughts": true}
	}
	if req.EnableImageGeneration || strings.Contains(strings.ToLower(model), "image") {
		generation["responseModalities"] = []string{"TEXT", "IMAGE"}
	}
	if len(generation) > 0 {
		request["generationConfig"] = generation
	}
	if tools := geminiCloudCodeTools(req.Tools); len(tools) > 0 {
		request["tools"] = []map[string]any{{"functionDeclarations": tools}}
		request["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
	}
	requestType := "agent"
	requestID := "agent-" + uuid.NewString()
	if strings.Contains(strings.ToLower(model), "image") {
		requestType = "image_gen"
		requestID = fmt.Sprintf("image_gen/%d/%s/12", time.Now().UnixMilli(), uuid.NewString())
	}
	return map[string]any{
		"project":            strings.TrimSpace(projectID),
		"request":            request,
		"model":              strings.TrimSpace(model),
		"userAgent":          "antigravity",
		"requestType":        requestType,
		"requestId":          requestID,
		"enabledCreditTypes": []string{"GOOGLE_ONE_AI"},
	}
}

func geminiCloudCodeContents(messages []Message, systemPrompt string) ([]map[string]any, string) {
	systemParts := make([]string, 0, 2)
	if text := strings.TrimSpace(systemPrompt); text != "" {
		systemParts = append(systemParts, text)
	}
	contents := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		blocks := normalizeContentBlocks(message)
		if role == "system" || role == "developer" {
			if text := strings.TrimSpace(contentBlocksText(blocks)); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		}
		parts := make([]map[string]any, 0, len(blocks))
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
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(block.Data)}})
			case "tool_use":
				name := strings.TrimSpace(block.ToolName)
				if name == "" {
					continue
				}
				call := map[string]any{"name": name, "args": geminiToolArguments(block.Input)}
				if id := strings.TrimSpace(block.ToolUseID); id != "" {
					call["id"] = id
				}
				part := map[string]any{"functionCall": call}
				if signature := geminiThoughtSignature(block.ProviderState); signature != "" {
					part["thoughtSignature"] = signature
				}
				parts = append(parts, part)
			case "tool_result":
				name := strings.TrimSpace(block.ToolName)
				if name == "" {
					name = "tool"
				}
				response := geminiCloudCodeToolResult(block.Output, block.IsError)
				functionResponse := map[string]any{"name": name, "response": response}
				if id := strings.TrimSpace(block.ToolUseID); id != "" {
					functionResponse["id"] = id
				}
				parts = append(parts, map[string]any{"functionResponse": functionResponse})
			default:
				if text := strings.TrimSpace(block.Text); text != "" {
					parts = append(parts, map[string]any{"text": text})
				}
			}
		}
		if len(parts) == 0 {
			continue
		}
		upstreamRole := "user"
		if role == "assistant" || role == "model" {
			upstreamRole = "model"
		}
		contents = append(contents, map[string]any{"role": upstreamRole, "parts": parts})
	}
	if len(contents) == 0 {
		contents = append(contents, map[string]any{"role": "user", "parts": []map[string]any{{"text": "Continue."}}})
	}
	return contents, strings.Join(systemParts, "\n\n")
}

func geminiCloudCodeToolResult(output string, isError bool) any {
	output = strings.TrimSpace(output)
	if output == "" {
		output = "(empty output)"
	}
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) == nil && parsed != nil {
		if isError {
			return map[string]any{"error": parsed}
		}
		return parsed
	}
	if isError {
		return map[string]any{"error": output}
	}
	return map[string]any{"output": output}
}

func geminiCloudCodeTools(specs []ToolSpec) []map[string]any {
	declarations := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		declaration := map[string]any{"name": name, "parametersJsonSchema": sanitizeGeminiSchema(spec.Schema)}
		if description := strings.TrimSpace(spec.Description); description != "" {
			declaration["description"] = description
		}
		declarations = append(declarations, declaration)
	}
	return declarations
}

type geminiCloudCodeAttemptOutcome struct {
	emittedContent bool
	err            error
	code           string
}

func consumeGeminiCloudCodeAttempt(ctx context.Context, out chan<- Event, reader io.Reader, emitDispatch func() bool, provider string) geminiCloudCodeAttemptOutcome {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), geminiCloudCodeMaxResponseBytes)
	outcome := geminiCloudCodeAttemptOutcome{}
	emittedCalls := make(map[string]bool)
	var usage Usage
	var stopReason string
	var thoughtSignature string
	sawDone := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, io.ErrUnexpectedEOF), code: "invalid_stream_event"}
		}
		root := payload
		if nested, ok := payload["response"].(map[string]any); ok {
			root = nested
		}
		if errValue, ok := root["error"].(map[string]any); ok && len(errValue) > 0 {
			return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, errors.New("Cloud Code stream error")), code: "stream_error"}
		}
		candidates, _ := root["candidates"].([]any)
		for _, rawCandidate := range candidates {
			candidate, _ := rawCandidate.(map[string]any)
			if reason, _ := candidate["finishReason"].(string); strings.TrimSpace(reason) != "" {
				stopReason = normalizeGeminiCloudCodeStopReason(reason)
			}
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(map[string]any)
				if signature := geminiCloudCodeString(part, "thoughtSignature", "thought_signature"); signature != "" {
					thoughtSignature = signature
				}
				if thought, _ := part["thought"].(bool); thought {
					continue
				}
				if text := geminiCloudCodeString(part, "text"); text != "" {
					if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: text}) {
						return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
					}
					outcome.emittedContent = true
				}
				if rawCall, ok := part["functionCall"].(map[string]any); ok {
					call := geminiCloudCodeToolCall(rawCall, thoughtSignature)
					if call != nil && !emittedCalls[call.ID] {
						emittedCalls[call.ID] = true
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "tool_call", ToolCall: call}) {
							return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
						}
						outcome.emittedContent = true
					}
				}
				if inline, ok := part["inlineData"].(map[string]any); ok {
					image := geminiCloudCodeImage(inline)
					if image != nil {
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "image_generation", ImageGeneration: image}) {
							return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
						}
						outcome.emittedContent = true
					}
				}
			}
		}
		if parsed, ok := geminiCloudCodeUsage(root); ok {
			usage = parsed
		}
		if stopReason != "" {
			sawDone = true
		}
	}
	if ctx.Err() != nil {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	if err := scanner.Err(); err != nil {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, err), code: "stream_read_error"}
	}
	if !sawDone {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, io.EOF), code: "stream_closed"}
	}
	if !emitDispatch() {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	if usage != (Usage{}) && !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	if stopReason == "" {
		stopReason = "stop"
	}
	if !emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: stopReason}) {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	return outcome
}

func geminiCloudCodeToolCall(raw map[string]any, signature string) *ToolCall {
	name := geminiCloudCodeString(raw, "name")
	if name == "" {
		return nil
	}
	input := geminiRawArguments(raw["args"])
	id := geminiCloudCodeString(raw, "id", "callId", "call_id")
	if id == "" {
		hash := sha256.Sum256(append([]byte(name+"\x00"), input...))
		id = "call_" + hex.EncodeToString(hash[:8])
	}
	state := json.RawMessage(nil)
	if strings.TrimSpace(signature) != "" {
		state, _ = json.Marshal(map[string]any{"thought_signature": strings.TrimSpace(signature)})
	}
	return &ToolCall{ID: id, Name: name, Input: input, ProviderState: state}
}

func geminiCloudCodeImage(raw map[string]any) *ImageGeneration {
	dataText := geminiCloudCodeString(raw, "data")
	if dataText == "" {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(dataText)
	if err != nil || len(data) == 0 {
		return nil
	}
	mime := geminiCloudCodeString(raw, "mimeType", "mime_type")
	return &ImageGeneration{GenerationID: "gemini_" + uuid.NewString(), Status: "completed", Data: data, MIME: mime}
}

func geminiCloudCodeUsage(root map[string]any) (Usage, bool) {
	metadata, _ := root["usageMetadata"].(map[string]any)
	if len(metadata) == 0 {
		metadata, _ = root["cpaUsageMetadata"].(map[string]any)
	}
	if len(metadata) == 0 {
		return Usage{}, false
	}
	usage := Usage{
		InputTokens:       geminiCloudCodeInt(metadata, "promptTokenCount"),
		OutputTokens:      geminiCloudCodeInt(metadata, "candidatesTokenCount"),
		CachedInputTokens: geminiCloudCodeInt(metadata, "cachedContentTokenCount"),
		ReasoningTokens:   geminiCloudCodeInt(metadata, "thoughtsTokenCount"),
	}
	return usage, usage != (Usage{})
}

func normalizeGeminiCloudCodeStopReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "", "STOP", "FINISH_REASON_UNSPECIFIED":
		return "stop"
	case "MAX_TOKENS", "MAX_OUTPUT_TOKENS":
		return "max_output_tokens"
	default:
		return strings.ToLower(strings.TrimSpace(reason))
	}
}

func geminiCloudCodeString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func geminiCloudCodeInt(value map[string]any, key string) int64 {
	switch number := value[key].(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case json.Number:
		parsed, _ := number.Int64()
		return parsed
	default:
		return 0
	}
}

// parseGeminiAvailableModels reads a fetchAvailableModels response. "models" is a
// protobuf map, so the model id is the JSON *key* — the tree-walking parser used
// for loadCodeAssist only inspects string values and would return nothing here.
//
// The quota presence rules are load-bearing. remaining_fraction has implicit
// proto3 presence (no Has/Clear accessor, unlike reset_time), so the wire format
// omits it entirely at 0.0: a model with quotaInfo but no remainingFraction is
// EXHAUSTED, not full. A model with no quotaInfo at all reports nothing, which is
// a different thing and is left out of the snapshot rather than reported as 0.
func parseGeminiAvailableModels(reader io.Reader) ([]string, []AccountModelQuotaSnapshot, error) {
	data, err := io.ReadAll(io.LimitReader(reader, geminiCloudCodeMaxResponseBytes+1))
	if err != nil || len(data) > geminiCloudCodeMaxResponseBytes {
		return nil, nil, errors.New("Gemini model response is invalid")
	}
	var payload struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			Disabled    bool   `json:"disabled"`
			QuotaInfo   *struct {
				RemainingFraction *float64 `json:"remainingFraction"`
				ResetTime         string   `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, err
	}

	models := make([]string, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	quotas := make([]AccountModelQuotaSnapshot, 0, len(payload.Models))
	for id, details := range payload.Models {
		model := strings.TrimSpace(id)
		if !looksLikeGeminiCloudCodeModel(model) {
			continue
		}
		appendUniqueModel(&models, seen, model)
		if details.QuotaInfo == nil {
			continue
		}
		fraction := 0.0
		if details.QuotaInfo.RemainingFraction != nil {
			fraction = *details.QuotaInfo.RemainingFraction
		}
		quotas = append(quotas, AccountModelQuotaSnapshot{
			Model:            model,
			DisplayName:      boundedGeminiQuotaText(details.DisplayName),
			RemainingPercent: int(math.Round(clampPercent(fraction * 100))),
			Reset:            boundedGeminiQuotaText(details.QuotaInfo.ResetTime),
			Disabled:         details.Disabled,
		})
	}
	sort.Strings(models)
	sort.Slice(quotas, func(i, j int) bool { return quotas[i].Model < quotas[j].Model })
	return models, quotas, nil
}

// boundedGeminiQuotaText keeps upstream-controlled display strings from reaching
// storage unbounded or carrying control characters.
func boundedGeminiQuotaText(value string) string {
	trimmed := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if len(trimmed) > 120 {
		return trimmed[:120]
	}
	return trimmed
}

func looksLikeGeminiCloudCodeModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "claude-") || strings.HasPrefix(model, "gpt-oss-")
}

var _ Provider = (*GeminiProvider)(nil)
var _ ConfigurationProvider = (*GeminiProvider)(nil)
var _ ScenarioConfigurationProvider = (*GeminiProvider)(nil)
var _ ScenarioAvailabilityProvider = (*GeminiProvider)(nil)
var _ CapabilityProvider = (*GeminiProvider)(nil)
var _ ModelCapabilityProvider = (*GeminiProvider)(nil)
