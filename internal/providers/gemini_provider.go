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
	// geminiCloudCodeMaxContinuations caps how many times a truncated stream is
	// automatically continued. Each continuation appends the partial assistant
	// turn and re-issues the request so the model picks up exactly where it
	// stopped. Two retries cover the vast majority of network-induced truncations
	// without risking infinite loops on pathological responses.
	geminiCloudCodeMaxContinuations = 2
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

func (p *GeminiProvider) DefaultContextTokenLimit() int { return 1048576 }

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
			detail := logGeminiRejection(status, "", prepared.ProjectID, response.Body)
			response.Body.Close()
			code := "models_request_failed"
			if detail != "" && status == http.StatusBadRequest {
				lastErr = fmt.Errorf("%s request failed: HTTP %d (%s)", p.cfg.Name, status, detail)
			} else {
				lastErr = newSubscriptionHTTPError(p.cfg.Name, status, code)
			}
			p.accounts.recordAttempt(ctx, prepared.ID, false, status, code, lastErr)
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
		currentReq := req
		for contAttempt := 0; contAttempt <= geminiCloudCodeMaxContinuations; contAttempt++ {
			truncatedText, done := p.generateCloudCodeAttempts(ctx, out, currentReq, accounts, model, reasoningEffort, contAttempt)
			if done {
				return
			}
			if truncatedText == "" {
				return
			}
			slog.Info("gemini cloud code stream truncated, continuing",
				"provider", p.cfg.Name, "attempt", contAttempt+1, "chars", len(truncatedText))
			msgs := make([]Message, len(currentReq.Messages)+1)
			copy(msgs, currentReq.Messages)
			msgs[len(currentReq.Messages)] = Message{Role: "assistant", Content: truncatedText}
			currentReq.Messages = msgs
		}
	}()
	return out, nil
}

// generateCloudCodeAttempts iterates over the available accounts for a single
// generation attempt. It returns (truncatedText, done):
//   - truncatedText != "" && !done: the SSE stream closed before [DONE]; the
//     caller should append truncatedText as an assistant turn and retry.
//   - done == true: generation finished (success or unrecoverable error emitted).
func (p *GeminiProvider) generateCloudCodeAttempts(ctx context.Context, out chan<- Event, req GenerateRequest, accounts []subscriptionauth.StoredCredential, model, reasoningEffort string, contAttempt int) (string, bool) {
	var lastErr error
	for _, item := range accounts {
		if ctx.Err() != nil {
			return "", true
		}
		prepared, prepareErr := p.accounts.prepareCredential(ctx, item, p.refresh)
		if prepareErr != nil {
			if ctx.Err() != nil {
				return "", true
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
		isImageGen := strings.Contains(strings.ToLower(model), "image")
		var endpoint string
		if isImageGen {
			endpoint = p.baseURL + "/v1internal:generateContent"
		} else {
			endpoint = p.baseURL + "/v1internal:streamGenerateContent?alt=sse"
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if requestErr != nil {
			lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
			continue
		}
		p.applyHeaders(request, prepared.Credential, !isImageGen)
		if strings.HasPrefix(strings.ToLower(model), "claude-") {
			request.Header.Set("anthropic-beta", "claude-code-20250219")
		}
			response, requestErr := p.client.Do(request)
		if requestErr != nil {
			if ctx.Err() != nil {
				return "", true
			}
			lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
			p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "network_error", lastErr)
			if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
				continue
			}
			p.emitFinalError(ctx, out, model, prepared.ID, lastErr)
			return "", true
		}
		if response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			detail := logGeminiRejection(status, model, prepared.ProjectID, response.Body)
			response.Body.Close()
			code := "model_request_failed"
			if detail != "" && status == http.StatusBadRequest {
				lastErr = fmt.Errorf("%s request failed: HTTP %d (%s)", p.cfg.Name, status, detail)
			} else {
				lastErr = newSubscriptionHTTPError(p.cfg.Name, status, code)
			}
			p.accounts.recordAttempt(ctx, prepared.ID, false, status, code, lastErr)
			if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
				continue
			}
			p.emitFinalError(ctx, out, model, prepared.ID, lastErr)
			return "", true
		}
		dispatchEmitted := false
		emitDispatch := func() bool {
			if dispatchEmitted {
				return true
			}
			dispatchEmitted = emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, prepared.ID))
			return dispatchEmitted
		}
		var outcome geminiCloudCodeAttemptOutcome
		if isImageGen {
			outcome = consumeGeminiCloudCodeImageResponse(ctx, out, response.Body, emitDispatch, p.cfg.Name)
		} else {
			outcome = consumeGeminiCloudCodeAttempt(ctx, out, response.Body, emitDispatch, p.cfg.Name)
		}
		response.Body.Close()
		if ctx.Err() != nil {
			return "", true
		}
		if outcome.err != nil {
			// Truncated stream: return partial text so the caller can build a continuation.
			if outcome.truncated && outcome.accumulatedText != "" && contAttempt < geminiCloudCodeMaxContinuations {
				p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, outcome.code, outcome.err)
				return outcome.accumulatedText, false
			}
			lastErr = outcome.err
			p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, outcome.code, outcome.err)
			if shouldTryNextSubscriptionAccount(ctx, outcome.err, outcome.emittedContent) {
				continue
			}
			if !emitDispatch() {
				return "", true
			}
			_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, outcome.err).Error()})
			return "", true
		}
		p.accounts.recordAttempt(ctx, prepared.ID, true, response.StatusCode, "", nil)
		return "", true
	}
	if lastErr == nil {
		lastErr = providerUnavailableError(p.cfg.Name, "no usable subscription account")
	}
	if ctx.Err() == nil {
		_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr).Error()})
	}
	return "", true
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

// logGeminiRejection records why Cloud Code refused a request and returns the
// detail string so callers can surface it in user-visible error messages.
// The error returned to callers deliberately carries only the numeric status
// for non-400 responses, because upstream bodies are untrusted (see
// providerHTTPFailedText). For 400 INVALID_ARGUMENT responses Google answers
// with a structured google.rpc.BadRequest naming the offending field, so that
// message is worth both logging (warn) and forwarding to the user. The body is
// bounded so a hostile response cannot flood the log.
func logGeminiRejection(status int, model, projectID string, body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil || len(raw) == 0 {
		slog.Warn("gemini cloud code rejected request", "status", status, "model", model, "project", projectID)
		return ""
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
	slog.Warn("gemini cloud code rejected request", "status", status, "model", model, "project", projectID, "upstreamStatus", envelope.Error.Status, "detail", detail)
	return detail
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
//
// Reasoning field routing differs by model family, and both live inside
// generationConfig.thinkingConfig:
//   - Gemini models: thinkingLevel (enum "low"/"medium"/"high")
//   - Claude models: thinkingBudget (token count)
//
// Claude ignores thinkingLevel silently — the request succeeds with no thoughts
// and an unchanged token count — so effort must be converted to a budget. The
// budget must be strictly below max_tokens or Anthropic rejects the call.
//
// An earlier revision sent Claude reasoning as a top-level
// additionalModelRequestFields.output_config.effort. That field does not exist
// in this API: Cloud Code answers 400 "Unknown name" both at the top level and
// inside request, so every Claude call carrying an effort failed outright.
func buildGeminiCloudCodePayload(req GenerateRequest, model, projectID, reasoningEffort string) map[string]any {
	contents, system := geminiCloudCodeContents(req.Messages, req.SystemPrompt)
	isImageGen := strings.Contains(strings.ToLower(strings.TrimSpace(model)), "image")
	isClaude := strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
	request := map[string]any{
		"contents":  contents,
		"sessionId": uuid.NewString(),
	}
	// systemInstruction is not accepted by the image-generation endpoint.
	if !isImageGen && system != "" {
		request["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": system}}}
	}
	generation := map[string]any{}
	if req.MaxOutputTokens > 0 {
		generation["maxOutputTokens"] = req.MaxOutputTokens
	}
	if isImageGen {
		// Image generation uses a separate non-streaming endpoint; the response
		// carries inlineData parts, not text. Thoughts are explicitly disabled —
		// the image endpoint rejects thinkingConfig with includeThoughts:true.
		imageConfig, cleanModel := resolveImageConfig(model, req.ImageOptions)
		model = cleanModel
		generation["imageConfig"] = imageConfig
		generation["thinkingConfig"] = map[string]any{"includeThoughts": false}
		// The upstream serves one image per call regardless, so asking for more
		// only risks a rejection; callers fan out concurrently for n > 1.
		generation["candidateCount"] = 1
		request["safetySettings"] = geminiImageSafetySettings()
	} else {
		if reasoningEffort != "" {
			if isClaude {
				if budget := geminiClaudeThinkingBudget(reasoningEffort, req.MaxOutputTokens); budget > 0 {
					generation["thinkingConfig"] = map[string]any{"thinkingBudget": budget, "includeThoughts": true}
				}
			} else {
				generation["thinkingConfig"] = map[string]any{"thinkingLevel": reasoningEffort, "includeThoughts": true}
			}
		}
		if req.EnableImageGeneration {
			generation["responseModalities"] = []string{"TEXT", "IMAGE"}
		}
	}
	if len(generation) > 0 {
		request["generationConfig"] = generation
	}
	// tools/toolConfig are not accepted by the image-generation endpoint.
	if !isImageGen {
		if tools := geminiCloudCodeTools(req.Tools, isClaude); len(tools) > 0 {
			request["tools"] = []map[string]any{{"functionDeclarations": tools}}
			request["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
		}
	}
	requestType := "agent"
	requestID := "agent-" + uuid.NewString()
	if isImageGen {
		requestType = "image_gen"
		requestID = fmt.Sprintf("image_gen/%d/%s/12", time.Now().UnixMilli(), uuid.NewString())
	}
	outer := map[string]any{
		"project":     strings.TrimSpace(projectID),
		"request":     request,
		"model":       strings.TrimSpace(model),
		"userAgent":   "antigravity",
		"requestType": requestType,
		"requestId":   requestID,
	}
	// enabledCreditTypes is omitted for image generation and Claude requests.
	// For image generation the endpoint rejects the field outright. For Claude
	// models, sending GOOGLE_ONE_AI causes Google to debit the Gemini credit
	// pool instead of the Claude/GPT pool; if Gemini quota is exhausted the
	// call fails even when Claude quota is fully available. Omitting the field
	// (as the reference Antigravity client does) lets Google route the billing
	// automatically based on the model family.
	if !isImageGen && !isClaude {
		outer["enabledCreditTypes"] = []string{"GOOGLE_ONE_AI"}
	}
	return outer
}

// geminiClaudeThinkingBudget converts an effort level into the token budget
// Claude expects on Cloud Code. Ratios mirror the Anthropic provider so the same
// effort means the same share of the output allowance on both paths.
//
// Anthropic requires the budget to be at least 1024 and strictly below
// max_tokens; sending budget == max_tokens returns 400. When the caller sets no
// max_tokens the endpoint applies its own ceiling, which comfortably exceeds
// these defaults, so a budget is still safe to send. A max_tokens too small to
// leave 1024 of headroom yields 0, meaning "omit thinkingConfig" — a request
// without thoughts beats a request the endpoint refuses.
func geminiClaudeThinkingBudget(effort string, maxOutputTokens int64) int64 {
	const minimumBudget = 1024
	var budget int64
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		budget = 2048
	case "medium":
		budget = 4096
	case "high":
		budget = 8192
	default:
		return 0
	}
	if maxOutputTokens <= 0 {
		return budget
	}
	if headroom := maxOutputTokens - minimumBudget; budget > headroom {
		budget = headroom
	}
	if budget < minimumBudget {
		return 0
	}
	return budget
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

// geminiCloudCodeTools renders function declarations for Cloud Code.
//
// The schema field name is not cosmetic. Cloud Code fronts two different
// backends, and only Gemini reads "parametersJsonSchema": for Claude models the
// request is translated to Anthropic's tool format, that field is not carried
// across, and the backend rejects the whole request with
// "tools.0.custom.input_schema: Field required" (HTTP 400). Because every agent
// turn ships tools, that made Claude on Cloud Code fail outright. "parameters"
// survives the translation, verified against the live endpoint with this
// package's own sanitized declarations.
//
// Gemini models keep "parametersJsonSchema", which they are known to accept.
// geminiImageSafetySettings disables the content filters for image generation.
// The default thresholds reject ordinary illustration prompts, and the filter
// verdict arrives as an empty response rather than an error, which is
// indistinguishable from a failed generation. The prompt is the user's own.
func geminiImageSafetySettings() []map[string]any {
	categories := []string{
		"HARM_CATEGORY_HARASSMENT",
		"HARM_CATEGORY_HATE_SPEECH",
		"HARM_CATEGORY_SEXUALLY_EXPLICIT",
		"HARM_CATEGORY_DANGEROUS_CONTENT",
	}
	settings := make([]map[string]any, 0, len(categories))
	for _, category := range categories {
		settings = append(settings, map[string]any{"category": category, "threshold": "OFF"})
	}
	return settings
}

func geminiCloudCodeTools(specs []ToolSpec, isClaude bool) []map[string]any {
	schemaField := "parametersJsonSchema"
	if isClaude {
		schemaField = "parameters"
	}
	declarations := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		declaration := map[string]any{"name": name, schemaField: sanitizeGeminiSchema(spec.Schema)}
		if description := strings.TrimSpace(spec.Description); description != "" {
			declaration["description"] = description
		}
		declarations = append(declarations, declaration)
	}
	return declarations
}

type geminiCloudCodeAttemptOutcome struct {
	emittedContent  bool
	err             error
	code            string
	truncated       bool   // stream closed without [DONE] after emitting content
	accumulatedText string // text collected before truncation; used to build continuation
}

func consumeGeminiCloudCodeAttempt(ctx context.Context, out chan<- Event, reader io.Reader, emitDispatch func() bool, provider string) geminiCloudCodeAttemptOutcome {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), geminiCloudCodeMaxResponseBytes)
	outcome := geminiCloudCodeAttemptOutcome{}
	emittedCalls := make(map[string]bool)
	var usage Usage
	var stopReason string
	var thoughtSignature string
	var accumulatedText strings.Builder
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
			msg, _ := errValue["message"].(string)
			upstreamStatus, _ := errValue["status"].(string)
			httpCode, _ := errValue["code"].(float64)
			detail := strings.TrimSpace(msg)
			if upstreamStatus != "" && detail != "" {
				detail = upstreamStatus + ": " + detail
			} else if upstreamStatus != "" {
				detail = upstreamStatus
			}
			if detail == "" {
				detail = "Cloud Code stream error"
			}
			if len(detail) > 300 {
				detail = detail[:300]
			}
			slog.Warn("gemini cloud code stream error", "provider", provider, "httpCode", int(httpCode), "upstreamStatus", upstreamStatus, "message", msg)
			httpStatus := int(httpCode)
			// Cloud Code sometimes returns gRPC status codes (e.g. PERMISSION_DENIED=7,
			// RESOURCE_EXHAUSTED=8) as the numeric "code" field instead of the
			// equivalent HTTP status. Those values fall outside 100-599, so map the
			// upstream gRPC status string to an HTTP equivalent when the numeric code
			// is not a valid HTTP status.
			if httpStatus < 100 || httpStatus > 599 {
				switch upstreamStatus {
				case "PERMISSION_DENIED":
					httpStatus = 403
				case "RESOURCE_EXHAUSTED":
					httpStatus = 429
				case "INVALID_ARGUMENT":
					httpStatus = 400
				case "NOT_FOUND":
					httpStatus = 404
				case "UNAUTHENTICATED":
					httpStatus = 401
				case "UNAVAILABLE":
					httpStatus = 503
				case "INTERNAL":
					httpStatus = 500
				default:
					httpStatus = 0
				}
			}
			if httpStatus >= 100 && httpStatus <= 599 {
				return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionHTTPError(provider, httpStatus, "stream_error"), code: "stream_error"}
			}
			return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: newSubscriptionNetworkError(provider, errors.New(detail)), code: "stream_error"}
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
					// These parts were previously dropped. They carry the model's own
					// summary of what it is doing, which is exactly what the activity
					// list wants; they are still not answer text, so emittedContent
					// stays untouched.
					if text := geminiCloudCodeString(part, "text"); text != "" {
						if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "reasoning", Text: text}) {
							return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
						}
					}
					continue
				}
				if text := geminiCloudCodeString(part, "text"); text != "" {
					if !emitDispatch() || !emitProviderEvent(ctx, out, Event{Type: "text", Text: text}) {
						return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
					}
					accumulatedText.WriteString(text)
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
		acc := accumulatedText.String()
		return geminiCloudCodeAttemptOutcome{
			emittedContent:  outcome.emittedContent,
			err:             newSubscriptionNetworkError(provider, io.EOF),
			code:            "stream_closed",
			truncated:       outcome.emittedContent && acc != "",
			accumulatedText: acc,
		}
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
	// Cloud Code has no tool-call finish reason: a turn that asks for a function
	// still reports STOP, which normalises to "stop". The runner treats a "stop"
	// alongside tool calls as a provider that lost track of its own turn and
	// aborts the whole run ("unsafe tool stop reason"), so the first message that
	// triggered a tool killed the conversation. The emitted calls are the
	// authoritative signal here, so report what actually happened.
	if len(emittedCalls) > 0 && stopReason == "stop" {
		stopReason = "tool_use"
	}
	if !emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: stopReason}) {
		return geminiCloudCodeAttemptOutcome{emittedContent: outcome.emittedContent, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
	}
	return outcome
}

// consumeGeminiCloudCodeImageResponse parses a non-streaming generateContent
// response for image-generation models. Google returns a single JSON object
// (not SSE) whose candidates carry inlineData parts with base64-encoded images.
func consumeGeminiCloudCodeImageResponse(ctx context.Context, out chan<- Event, reader io.Reader, emitDispatch func() bool, provider string) geminiCloudCodeAttemptOutcome {
	outcome := geminiCloudCodeAttemptOutcome{}
	raw, readErr := io.ReadAll(io.LimitReader(reader, geminiCloudCodeMaxResponseBytes))
	if readErr != nil {
		return geminiCloudCodeAttemptOutcome{err: newSubscriptionNetworkError(provider, readErr), code: "stream_read_error"}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return geminiCloudCodeAttemptOutcome{err: newSubscriptionNetworkError(provider, io.ErrUnexpectedEOF), code: "invalid_upstream_response"}
	}
	if nested, ok := root["response"].(map[string]any); ok {
		root = nested
	}
	if errValue, ok := root["error"].(map[string]any); ok && len(errValue) > 0 {
		msg, _ := errValue["message"].(string)
		upstreamStatus, _ := errValue["status"].(string)
		httpCode, _ := errValue["code"].(float64)
		detail := strings.TrimSpace(msg)
		if upstreamStatus != "" && detail != "" {
			detail = upstreamStatus + ": " + detail
		} else if upstreamStatus != "" {
			detail = upstreamStatus
		}
		if detail == "" {
			detail = "image generation failed"
		}
		if len(detail) > 300 {
			detail = detail[:300]
		}
		slog.Warn("gemini cloud code image error", "provider", provider, "httpCode", int(httpCode), "upstreamStatus", upstreamStatus, "message", msg)
		httpStatus := int(httpCode)
		if httpStatus < 100 || httpStatus > 599 {
			switch upstreamStatus {
			case "PERMISSION_DENIED":
				httpStatus = 403
			case "RESOURCE_EXHAUSTED":
				httpStatus = 429
			case "INVALID_ARGUMENT":
				httpStatus = 400
			case "NOT_FOUND":
				httpStatus = 404
			case "UNAUTHENTICATED":
				httpStatus = 401
			case "UNAVAILABLE":
				httpStatus = 503
			case "INTERNAL":
				httpStatus = 500
			default:
				httpStatus = 0
			}
		}
		if httpStatus >= 100 && httpStatus <= 599 {
			return geminiCloudCodeAttemptOutcome{err: newSubscriptionHTTPError(provider, httpStatus, "stream_error"), code: "stream_error"}
		}
		return geminiCloudCodeAttemptOutcome{err: newSubscriptionNetworkError(provider, errors.New(detail)), code: "stream_error"}
	}
	candidates, _ := root["candidates"].([]any)
	for _, rawCandidate := range candidates {
		candidate, _ := rawCandidate.(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
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
	if !outcome.emittedContent {
		return geminiCloudCodeAttemptOutcome{err: newSubscriptionNetworkError(provider, errors.New("image generation returned no images")), code: "response_failed"}
	}
	if usage, ok := geminiCloudCodeUsage(root); ok {
		if !emitProviderEvent(ctx, out, Event{Type: "usage", Usage: &usage}) {
			return geminiCloudCodeAttemptOutcome{emittedContent: true, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
		}
	}
	if !emitProviderEvent(ctx, out, Event{Type: "done", Done: true, StopReason: "stop"}) {
		return geminiCloudCodeAttemptOutcome{emittedContent: true, err: ctx.Err(), code: telemetryErrorCode(ctx.Err())}
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

// WarmupTokens proactively refreshes expired/near-expiry OAuth tokens at
// startup so the first real request does not pay a cold-start TLS penalty.
func (p *GeminiProvider) WarmupTokens(ctx context.Context) {
	if p == nil || p.accounts == nil || p.configErr != nil {
		return
	}
	p.accounts.warmupTokens(ctx, p.refresh)
}
