package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"autoto/internal/config"
	"autoto/internal/kiroauth"
	"autoto/internal/subscriptionauth"
)

const kiroProductionBaseURL = "https://q.us-east-1.amazonaws.com/generateAssistantResponse"

// kiroModelNormRe matches claude-*-N-M where N and M are single digits.
var kiroModelNormRe = regexp.MustCompile(`^(claude-.+)-(\d)-(\d)$`)

// normalizeKiroModel converts e.g. "claude-sonnet-4-6" → "claude-sonnet-4.6".
func normalizeKiroModel(model string) string {
	m := kiroModelNormRe.FindStringSubmatch(model)
	if m == nil {
		return model
	}
	return m[1] + "." + m[2] + "." + m[3]
}

// KiroProvider implements Provider using the Kiro (Amazon Q) Event Stream API.
type KiroProvider struct {
	cfg       config.ProviderConfig
	accounts  *subscriptionProviderAccounts
	client    *http.Client
	configErr error
	refresh   subscriptionRefreshFunc
}

// NewKiroProvider constructs a production KiroProvider.
func NewKiroProvider(cfg config.ProviderConfig) *KiroProvider {
	cfg = normalizeKiroProviderConfig(cfg)
	apiConfig := cfg
	apiConfig.RequestHeaders = nil
	apiConfig.UserAgent = ""
	client, clientErr := providerHTTPClient(apiConfig, 5*time.Minute)
	refreshClient, refreshClientErr := providerHTTPClient(apiConfig, 30*time.Second)
	p := &KiroProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderKiro),
		client:    client,
		configErr: errors.Join(validateProviderRuntimeConfig(cfg), validateKiroProductionBaseURL(cfg.BaseURL), clientErr, refreshClientErr),
	}
	if refreshClient != nil {
		p.refresh = func(ctx context.Context, credential subscriptionauth.Credential) (subscriptionauth.TokenUpdate, error) {
			// Subject holds the ProfileArn for Kiro credentials.
			region := kiroauth.RegionFromProfileArn(credential.Subject)
			authClient := kiroauth.New(refreshClient)
			tokens, err := authClient.RefreshToken(ctx, credential.RefreshToken, region)
			if err != nil || tokens == nil {
				return subscriptionauth.TokenUpdate{}, err
			}
			return subscriptionauth.TokenUpdate{
				AccessToken:  tokens.AccessToken,
				RefreshToken: tokens.RefreshToken,
				ExpiresAt:    tokens.ExpiresAt,
				Subject:      tokens.ProfileArn, // store ProfileArn in Subject
			}, nil
		}
	}
	return p
}

func newKiroProviderForTest(cfg config.ProviderConfig, client *http.Client, baseURL string) *KiroProvider {
	cfg = normalizeKiroProviderConfig(cfg)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &KiroProvider{
		cfg:       cfg,
		accounts:  newSubscriptionProviderAccounts(cfg, subscriptionauth.ProviderKiro),
		client:    client,
		configErr: validateSubscriptionTestBaseURL(cfg.BaseURL),
	}
}

func normalizeKiroProviderConfig(cfg config.ProviderConfig) config.ProviderConfig {
	if strings.TrimSpace(cfg.Name) == "" {
		cfg.Name = config.ProviderTypeKiro
	}
	if strings.TrimSpace(cfg.Type) == "" {
		cfg.Type = config.ProviderTypeKiro
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = kiroProductionBaseURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = "claude-sonnet-4-6"
	}
	return cfg
}

func validateKiroProductionBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return errors.New("Kiro Base URL only allows the official HTTPS endpoint")
	}
	host := strings.ToLower(parsed.Host)
	// Must be https://q.*.amazonaws.com/generateAssistantResponse
	if !strings.HasPrefix(host, "q.") || !strings.HasSuffix(host, ".amazonaws.com") {
		return errors.New("Kiro Base URL only allows the official HTTPS endpoint")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path != "/generateAssistantResponse" {
		return errors.New("Kiro Base URL only allows the official HTTPS endpoint")
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Kiro Base URL only allows the official HTTPS endpoint")
	}
	return nil
}

func (p *KiroProvider) Name() string {
	if p == nil {
		return config.ProviderTypeKiro
	}
	return p.cfg.Name
}

func (p *KiroProvider) Configured() bool {
	return p != nil && p.configErr == nil && p.accounts.configured()
}

func (p *KiroProvider) ConfiguredForScenario(scenario CallScenario) bool {
	return p != nil && p.configErr == nil && p.accounts.configuredForScenario(scenario)
}

func (p *KiroProvider) AvailableForScenario(ctx context.Context, availability ScenarioAvailability) bool {
	return p != nil && p.configErr == nil && p.accounts.availableForScenario(ctx, availability)
}

func (p *KiroProvider) SetAccountTelemetry(telemetry AccountTelemetry) {
	if p != nil {
		p.accounts.setAccountTelemetry(telemetry)
	}
}

func (p *KiroProvider) SetGatewayAccountPolicy(policy GatewayAccountPolicy) {
	if p != nil {
		p.accounts.setGatewayAccountPolicy(policy)
	}
}

func (p *KiroProvider) Capabilities() Capabilities {
	return Capabilities{
		Tools:           true,
		Streaming:       true,
		ImageInput:      false,
		ReasoningEffort: false,
	}
}

func (p *KiroProvider) ModelCapabilities(model string) ModelCapabilities {
	if p == nil {
		return ModelCapabilities{}
	}
	return configuredModelCapabilities(p.cfg, model)
}

func (p *KiroProvider) ListModels(ctx context.Context) ([]string, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeKiro, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	staticModels := configuredSubscriptionModels(p.cfg)
	if len(staticModels) > 0 {
		return staticModels, nil
	}
	return nil, providerUnavailableError(p.cfg.Name, "models are unavailable")
}

// kiroAPIURLForCredential returns the generateAssistantResponse URL for the
// given credential's region (derived from Subject, which stores the ProfileArn).
func kiroAPIURLForCredential(credential subscriptionauth.Credential) string {
	region := kiroauth.RegionFromProfileArn(credential.Subject)
	return "https://q." + region + ".amazonaws.com/generateAssistantResponse"
}

// kiroToolSpec is the Kiro wire representation of a tool.
type kiroToolSpec struct {
	ToolSpec struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		InputSchema struct {
			JSON any `json:"json"`
		} `json:"inputSchema"`
	} `json:"toolSpec"`
}

func kiroTools(tools []ToolSpec) []kiroToolSpec {
	out := make([]kiroToolSpec, 0, len(tools))
	for _, t := range tools {
		var ts kiroToolSpec
		ts.ToolSpec.Name = t.Name
		ts.ToolSpec.Description = t.Description
		ts.ToolSpec.InputSchema.JSON = t.Schema
		out = append(out, ts)
	}
	return out
}

// kiroUserMessage is the Kiro wire format for a user turn.
type kiroUserMessage struct {
	Content  []map[string]string `json:"content"`
	ModelID  string              `json:"modelId"`
	Origin   string              `json:"origin"`
	UserInputMessageContext *kiroUserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

type kiroUserInputMessageContext struct {
	Tools []kiroToolSpec `json:"tools,omitempty"`
}

// kiroAssistantMessage is the Kiro wire format for an assistant turn.
type kiroAssistantMessage struct {
	Content []map[string]string `json:"content"`
}

// kiroHistoryTurn pairs one user and one assistant message.
type kiroHistoryTurn struct {
	UserInputMessage         kiroUserMessage      `json:"userInputMessage"`
	AssistantResponseMessage kiroAssistantMessage `json:"assistantResponseMessage"`
}

// kiroConversationState is the top-level request object.
type kiroConversationState struct {
	ChatTriggerType string            `json:"chatTriggerType"`
	CurrentMessage  struct {
		UserInputMessage kiroUserMessage `json:"userInputMessage"`
	} `json:"currentMessage"`
	History []kiroHistoryTurn `json:"history,omitempty"`
}

// buildKiroRequest converts a GenerateRequest into the Kiro API request body.
func buildKiroRequest(req GenerateRequest, model, profileArn string) ([]byte, error) {
	// Collect messages, injecting system prompt into first user message
	type pair struct {
		user      string
		assistant string
	}
	var pairs []pair
	systemPrepend := strings.TrimSpace(req.SystemPrompt)
	firstUser := true

	for _, msg := range req.Messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		text := messageText(msg)
		switch role {
		case "user":
			userText := text
			if firstUser && systemPrepend != "" {
				userText = systemPrepend + "\n\n" + userText
				firstUser = false
			} else {
				firstUser = false
			}
			pairs = append(pairs, pair{user: userText})
		case "assistant":
			if len(pairs) > 0 && pairs[len(pairs)-1].assistant == "" {
				pairs[len(pairs)-1].assistant = text
			}
		}
	}

	if len(pairs) == 0 {
		// Ensure at least one user turn
		userText := "Continue."
		if systemPrepend != "" {
			userText = systemPrepend + "\n\n" + userText
		}
		pairs = append(pairs, pair{user: userText})
	}

	// Last pair is the current message; remainder form history
	last := pairs[len(pairs)-1]
	history := pairs[:len(pairs)-1]

	makeUserMsg := func(text, modelID string, withTools bool) kiroUserMessage {
		m := kiroUserMessage{
			Content: []map[string]string{{"text": text}},
			ModelID: modelID,
			Origin:  "AI_EDITOR",
		}
		if withTools {
			tools := kiroTools(req.Tools)
			if len(tools) > 0 {
				m.UserInputMessageContext = &kiroUserInputMessageContext{Tools: tools}
			}
		}
		return m
	}

	var historyTurns []kiroHistoryTurn
	for _, p := range history {
		historyTurns = append(historyTurns, kiroHistoryTurn{
			UserInputMessage:         makeUserMsg(p.user, model, false),
			AssistantResponseMessage: kiroAssistantMessage{Content: []map[string]string{{"text": p.assistant}}},
		})
	}

	state := kiroConversationState{
		ChatTriggerType: "MANUAL",
		History:         historyTurns,
	}
	state.CurrentMessage.UserInputMessage = makeUserMsg(last.user, model, true)

	body := map[string]any{
		"conversationState": state,
		"profileArn":        profileArn,
	}
	return json.Marshal(body)
}

// messageText extracts a flat text string from a Message, including tool_result blocks.
func messageText(msg Message) string {
	if len(msg.Blocks) == 0 {
		return msg.Content
	}
	var sb strings.Builder
	if msg.Content != "" {
		sb.WriteString(msg.Content)
	}
	for _, b := range msg.Blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Text)
			}
		case "tool_result":
			if b.Output != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Output)
			}
		}
	}
	return sb.String()
}

// Generate sends a request to the Kiro API and streams events onto the returned channel.
func (p *KiroProvider) Generate(ctx context.Context, req GenerateRequest) (<-chan Event, error) {
	if p == nil {
		return nil, providerUnavailableError(config.ProviderTypeKiro, "provider is not configured")
	}
	if p.configErr != nil {
		return nil, p.configErr
	}
	accounts, err := p.accounts.accountsForRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = p.cfg.Model
	}
	kiroModel := normalizeKiroModel(model)

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
			profileArn := strings.TrimSpace(prepared.Credential.Subject)
			apiURL := kiroAPIURLForCredential(prepared.Credential)

			data, buildErr := buildKiroRequest(req, kiroModel, profileArn)
			if buildErr != nil {
				lastErr = providerUnavailableError(p.cfg.Name, "request payload could not be constructed")
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
				continue
			}

			request, requestErr := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(data))
			if requestErr != nil {
				lastErr = newSubscriptionNetworkError(p.cfg.Name, requestErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, 0, "request_construction_failed", lastErr)
				continue
			}
			p.applyHeaders(request, prepared.Credential)

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
				p.emitKiroFinalError(ctx, out, kiroModel, prepared.ID, lastErr)
				return
			}
			if response.StatusCode >= http.StatusMultipleChoices {
				status := response.StatusCode
				response.Body.Close()
				lastErr = newSubscriptionHTTPError(p.cfg.Name, status, "model_request_failed")
				p.accounts.recordAttempt(ctx, prepared.ID, false, status, "model_request_failed", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				p.emitKiroFinalError(ctx, out, kiroModel, prepared.ID, lastErr)
				return
			}

			dispatchEmitted := false
			emitDispatch := func() bool {
				if dispatchEmitted {
					return true
				}
				dispatchEmitted = emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, kiroModel, prepared.ID))
				return dispatchEmitted
			}

			if !emitDispatch() {
				response.Body.Close()
				return
			}

			streamErr := readKiroEvents(ctx, response.Body, out)
			response.Body.Close()

			if ctx.Err() != nil {
				return
			}
			if streamErr != nil {
				lastErr = newSubscriptionNetworkError(p.cfg.Name, streamErr)
				p.accounts.recordAttempt(ctx, prepared.ID, false, response.StatusCode, "stream_read_error", lastErr)
				if shouldTryNextSubscriptionAccount(ctx, lastErr, false) {
					continue
				}
				_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, lastErr).Error()})
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

func (p *KiroProvider) emitKiroFinalError(ctx context.Context, out chan<- Event, model, accountID string, err error) {
	if !emitProviderEvent(ctx, out, newDispatchEvent(p.cfg.Name, model, accountID)) {
		return
	}
	_ = emitProviderEvent(ctx, out, Event{Type: "error", Text: sanitizeSubscriptionError(ctx, p.cfg.Name, err).Error()})
}

func (p *KiroProvider) applyHeaders(request *http.Request, credential subscriptionauth.Credential) {
	token := strings.TrimSpace(credential.AccessToken)
	request.Header.Set("Authorization", "Bearer "+token)
	// ksk_* tokens are Kiro API keys and require an extra header so the
	// service can distinguish them from short-lived OAuth access tokens.
	if strings.HasPrefix(token, "ksk_") {
		request.Header.Set("tokentype", "API_KEY")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/vnd.amazon.eventstream")
	request.Header.Set("x-amzn-codewhisperer-optout", "true")
	request.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	request.Header.Set("x-amz-user-agent", "aws-sdk-js/1.0.34 KiroIDE-0.1.0")
}

var _ Provider = (*KiroProvider)(nil)
var _ ConfigurationProvider = (*KiroProvider)(nil)
var _ ScenarioConfigurationProvider = (*KiroProvider)(nil)
var _ ScenarioAvailabilityProvider = (*KiroProvider)(nil)
var _ CapabilityProvider = (*KiroProvider)(nil)
var _ ModelCapabilityProvider = (*KiroProvider)(nil)

// WarmupTokens proactively refreshes expired/near-expiry OAuth tokens at
// startup so the first real request does not pay a cold-start TLS penalty.
func (p *KiroProvider) WarmupTokens(ctx context.Context) {
	if p == nil || p.accounts == nil || p.configErr != nil {
		return
	}
	p.accounts.warmupTokens(ctx, p.refresh)
}
