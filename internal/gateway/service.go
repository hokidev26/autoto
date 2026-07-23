package gateway

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"autoto/internal/db"
	"autoto/internal/providers"
)

const defaultMaxRequestBytes = int64(8 << 20)

type ProviderPolicy func(context.Context, string) bool

type Options struct {
	MaxGlobalConcurrency         int
	MaxRequestBytes              int64
	ProviderAllowed              ProviderPolicy
	AllowSubscriptionCredentials bool
	Now                          func() time.Time
}

type Service struct {
	store                        *db.Store
	providers                    *providers.Registry
	providerAllowed              ProviderPolicy
	allowSubscriptionCredentials bool
	maxRequestBytes              int64
	now                          func() time.Time
	limits                       *requestLimiter
}

func New(store *db.Store, registry *providers.Registry, options Options) (*Service, error) {
	if store == nil {
		return nil, errors.New("gateway store is required")
	}
	if registry == nil {
		return nil, errors.New("gateway provider registry is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = defaultMaxRequestBytes
	}
	return &Service{
		store:                        store,
		providers:                    registry,
		providerAllowed:              options.ProviderAllowed,
		allowSubscriptionCredentials: options.AllowSubscriptionCredentials,
		maxRequestBytes:              options.MaxRequestBytes,
		now:                          options.Now,
		limits:                       newRequestLimiter(options.MaxGlobalConcurrency, options.Now),
	}, nil
}

func (s *Service) Handler() http.Handler {
	return s
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.TrimSpace(r.Header.Get("Origin")) != "" {
		if isAnthropicGatewayPath(r.URL.Path) {
			writeAnthropicError(w, http.StatusForbidden, "permission_error", "Browser-origin requests are not allowed.")
		} else {
			writeAPIError(w, http.StatusForbidden, "browser_origin_forbidden", "Browser-origin requests are not allowed.", "invalid_request_error", "")
		}
		return
	}

	switch r.URL.Path {
	case "/v1/models":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", "invalid_request_error", "")
			return
		}
		s.handleModels(w, r)
	case "/v1/chat/completions":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", "invalid_request_error", "")
			return
		}
		s.handleChatCompletions(w, r)
	case "/v1/responses":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.", "invalid_request_error", "")
			return
		}
		s.handleResponses(w, r)
	case "/v1/messages":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method not allowed.")
			return
		}
		s.handleAnthropicMessages(w, r)
	case "/v1/messages/count_tokens":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method not allowed.")
			return
		}
		s.handleAnthropicCountTokens(w, r)
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "Endpoint not found.", "invalid_request_error", "")
	}
}

func isAnthropicGatewayPath(path string) bool {
	return path == "/v1/messages" || path == "/v1/messages/count_tokens"
}

func (s *Service) authenticateRequest(w http.ResponseWriter, r *http.Request) (db.GatewayKey, bool) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="autoto-gateway"`)
		writeProblem(w, invalidAPIKeyProblem())
		return db.GatewayKey{}, false
	}
	key, problem := s.authenticateToken(r.Context(), token)
	if problem != nil {
		if problem.Status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Bearer realm="autoto-gateway"`)
		}
		if problem.Status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		writeProblem(w, problem)
		return db.GatewayKey{}, false
	}
	return key, true
}

func (s *Service) authenticateAnthropicRequest(w http.ResponseWriter, r *http.Request) (db.GatewayKey, bool) {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	xAPIKey := strings.TrimSpace(r.Header.Get("x-api-key"))
	var token string
	if authorization != "" {
		bearer, ok := bearerToken(authorization)
		if !ok {
			writeAnthropicProblem(w, invalidAPIKeyProblem())
			return db.GatewayKey{}, false
		}
		token = bearer
	}
	if xAPIKey != "" {
		if len(xAPIKey) > 1024 {
			writeAnthropicProblem(w, invalidAPIKeyProblem())
			return db.GatewayKey{}, false
		}
		if token != "" && !constantTimeStringEqual(token, xAPIKey) {
			writeAnthropicProblem(w, invalidAPIKeyProblem())
			return db.GatewayKey{}, false
		}
		token = xAPIKey
	}
	if token == "" {
		writeAnthropicProblem(w, invalidAPIKeyProblem())
		return db.GatewayKey{}, false
	}
	key, problem := s.authenticateToken(r.Context(), token)
	if problem != nil {
		if problem.Status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "60")
		}
		writeAnthropicProblem(w, problem)
		return db.GatewayKey{}, false
	}
	return key, true
}

func (s *Service) authenticateToken(ctx context.Context, token string) (db.GatewayKey, *apiProblem) {
	key, err := s.store.GetGatewayKeyByTokenHash(ctx, HashToken(token))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.GatewayKey{}, invalidAPIKeyProblem()
		}
		return db.GatewayKey{}, &apiProblem{Status: http.StatusInternalServerError, Code: "gateway_internal_error", Type: "server_error", Message: "Gateway authentication failed."}
	}
	if !key.Enabled || key.RevokedAt != "" {
		return db.GatewayKey{}, invalidAPIKeyProblem()
	}
	if key.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, key.ExpiresAt)
		if parseErr != nil {
			return db.GatewayKey{}, &apiProblem{Status: http.StatusInternalServerError, Code: "gateway_internal_error", Type: "server_error", Message: "Gateway authentication failed."}
		}
		if !s.now().Before(expiresAt) {
			return db.GatewayKey{}, &apiProblem{Status: http.StatusUnauthorized, Code: "expired_api_key", Type: "invalid_request_error", Message: "API key has expired."}
		}
	}
	if err := s.limits.allowRequest(key); err != nil {
		return db.GatewayKey{}, &apiProblem{Status: http.StatusTooManyRequests, Code: "rate_limit_exceeded", Type: "rate_limit_error", Message: "Rate limit exceeded."}
	}
	touched, err := s.store.TouchGatewayKeyLastUsed(ctx, key.ID, s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		if errors.Is(err, db.ErrGatewayKeyRevoked) || errors.Is(err, sql.ErrNoRows) {
			return db.GatewayKey{}, invalidAPIKeyProblem()
		}
		return db.GatewayKey{}, &apiProblem{Status: http.StatusInternalServerError, Code: "gateway_internal_error", Type: "server_error", Message: "Gateway authentication failed."}
	}
	return touched, nil
}

func invalidAPIKeyProblem() *apiProblem {
	return &apiProblem{Status: http.StatusUnauthorized, Code: "invalid_api_key", Type: "invalid_request_error", Message: "Invalid API key."}
}

func constantTimeStringEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 1024 {
		return "", false
	}
	return parts[1], parts[1] != ""
}

type resolvedModel struct {
	Alias    string
	Target   string
	Provider providers.Provider
	Model    string
}

func (s *Service) resolveModel(ctx context.Context, key db.GatewayKey, alias string) (resolvedModel, *apiProblem) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return resolvedModel{}, invalidParam("model", "A model is required.")
	}
	if !gatewayKeyAllowsModel(key, alias) {
		return resolvedModel{}, &apiProblem{Status: http.StatusNotFound, Code: "model_not_found", Type: "invalid_request_error", Message: "The requested model is not available."}
	}
	model, err := s.store.GetGatewayModel(ctx, alias)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedModel{}, &apiProblem{Status: http.StatusNotFound, Code: "model_not_found", Type: "invalid_request_error", Message: "The requested model is not available."}
		}
		return resolvedModel{}, internalProblem()
	}
	return s.resolveStoredModel(ctx, model)
}

func (s *Service) resolveStoredModel(ctx context.Context, model db.GatewayModel) (resolvedModel, *apiProblem) {
	if !model.Enabled {
		return resolvedModel{}, &apiProblem{Status: http.StatusNotFound, Code: "model_not_found", Type: "invalid_request_error", Message: "The requested model is not available."}
	}
	providerName, targetModel := providers.SplitModel(model.TargetModel)
	if providerName == "" || targetModel == "" {
		return resolvedModel{}, &apiProblem{Status: http.StatusServiceUnavailable, Code: "model_unavailable", Type: "server_error", Message: "The requested model is unavailable."}
	}
	if strings.EqualFold(providerName, "aggregate") {
		if providerName != "aggregate" {
			return resolvedModel{}, &apiProblem{Status: http.StatusServiceUnavailable, Code: "model_unavailable", Type: "server_error", Message: "The requested model is unavailable."}
		}
		aggregate, err := s.store.GetModelAggregate(ctx, targetModel)
		if err != nil {
			return resolvedModel{}, &apiProblem{Status: http.StatusServiceUnavailable, Code: "model_unavailable", Type: "server_error", Message: "The requested model is unavailable."}
		}
		definition := providers.AggregateDefinition{Name: aggregate.Name, Mode: aggregate.Mode, Members: append([]string(nil), aggregate.Members...)}
		provider, err := s.providers.ResolveAggregateSnapshot(definition)
		if err != nil {
			return resolvedModel{}, &apiProblem{Status: http.StatusServiceUnavailable, Code: "model_unavailable", Type: "server_error", Message: "The requested model is unavailable."}
		}
		for _, member := range definition.Members {
			memberProvider, memberModel := providers.SplitModel(member)
			if memberProvider == "" || memberModel == "" || strings.EqualFold(memberProvider, "aggregate") || !s.providerPermitted(ctx, memberProvider) {
				return resolvedModel{}, &apiProblem{Status: http.StatusForbidden, Code: "model_not_allowed", Type: "invalid_request_error", Message: "The requested model is not permitted for Gateway use."}
			}
		}
		return resolvedModel{Alias: model.Alias, Target: model.TargetModel, Provider: provider, Model: targetModel}, nil
	}
	if !s.providerPermitted(ctx, providerName) {
		return resolvedModel{}, &apiProblem{Status: http.StatusForbidden, Code: "model_not_allowed", Type: "invalid_request_error", Message: "The requested model is not permitted for Gateway use."}
	}
	provider, resolvedTarget, err := s.providers.Resolve(model.TargetModel)
	if err != nil || provider == nil || strings.TrimSpace(resolvedTarget) == "" {
		return resolvedModel{}, &apiProblem{Status: http.StatusServiceUnavailable, Code: "model_unavailable", Type: "server_error", Message: "The requested model is unavailable."}
	}
	return resolvedModel{Alias: model.Alias, Target: model.TargetModel, Provider: provider, Model: resolvedTarget}, nil
}

func (s *Service) providerPermitted(ctx context.Context, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || s.providerAllowed == nil || !s.providerAllowed(ctx, name) {
		return false
	}
	provider, ok := s.providers.Get(name)
	if !ok || provider == nil {
		return false
	}
	return providers.AvailableForScenario(ctx, provider, true, providers.ScenarioAvailability{
		Scenario:                     providers.CallScenarioGateway,
		AllowSubscriptionCredentials: s.allowSubscriptionCredentials,
	})
}

func gatewayKeyAllowsModel(key db.GatewayKey, alias string) bool {
	if len(key.AllowedModels) == 0 {
		return true
	}
	for _, allowed := range key.AllowedModels {
		if allowed == alias {
			return true
		}
	}
	return false
}

type generationParameterNames struct {
	Tools           string
	Images          string
	ReasoningEffort string
	ServiceTier     string
	MaxOutputTokens string
}

func (s *Service) resolveAndValidateProviderRequest(ctx context.Context, key db.GatewayKey, alias string, request *providers.GenerateRequest, hasImages bool, parameters generationParameterNames) (resolvedModel, *apiProblem) {
	if request == nil {
		return resolvedModel{}, internalProblem()
	}
	resolved, problem := s.resolveModel(ctx, key, alias)
	if problem != nil {
		return resolvedModel{}, problem
	}
	capabilities := providers.CapabilitiesFor(resolved.Provider)
	if len(request.Tools) > 0 && !capabilities.Tools {
		return resolvedModel{}, invalidParam(parameters.Tools, "The requested model does not support function tools.")
	}
	if hasImages && !capabilities.ImageInput {
		return resolvedModel{}, invalidParam(parameters.Images, "The requested model does not support image input.")
	}
	if !capabilities.SupportsReasoningEffort(request.ReasoningEffort) {
		return resolvedModel{}, invalidParam(parameters.ReasoningEffort, "The requested reasoning effort is not supported by this model.")
	}
	if request.FastMode && !providers.ModelCapabilitiesFor(resolved.Provider, resolved.Model).FastMode {
		return resolvedModel{}, invalidParam(parameters.ServiceTier, "Priority service is not supported by this model.")
	}
	request.Model = resolved.Model
	request.Scenario = providers.CallScenarioGateway
	request.AllowSubscriptionCredentials = s.allowSubscriptionCredentials
	return resolved, nil
}

func (s *Service) prepareProviderRequest(ctx context.Context, key db.GatewayKey, alias string, request *providers.GenerateRequest, hasImages bool, lease *ingressLease, parameters generationParameterNames) (resolvedModel, *apiProblem) {
	resolved, problem := s.resolveAndValidateProviderRequest(ctx, key, alias, request, hasImages, parameters)
	if problem != nil {
		return resolvedModel{}, problem
	}
	monthlyTokens := int64(0)
	reservation := int64(0)
	if key.MonthlyTokenLimit > 0 {
		usage, err := s.store.GetGatewayKeyMonthlyUsage(ctx, key.ID, s.now())
		if err != nil {
			return resolvedModel{}, internalProblem()
		}
		monthlyTokens = usage.TotalTokens
		remaining := key.MonthlyTokenLimit - monthlyTokens
		if remaining <= 0 {
			return resolvedModel{}, &apiProblem{Status: http.StatusTooManyRequests, Code: "monthly_token_limit_exceeded", Type: "rate_limit_error", Message: "Monthly token limit exceeded."}
		}
		requested := request.MaxOutputTokens
		if requested <= 0 {
			requested = defaultOutputReservation
			if requested > remaining {
				requested = remaining
			}
		} else if requested > remaining {
			return resolvedModel{}, &apiProblem{Status: http.StatusTooManyRequests, Code: "monthly_token_limit_exceeded", Type: "rate_limit_error", Param: parameters.MaxOutputTokens, Message: "The requested maximum output exceeds the remaining monthly token allowance."}
		}
		request.MaxOutputTokens = requested
		reservation = requested
	}
	if lease == nil {
		return resolvedModel{}, internalProblem()
	}
	if err := lease.Reserve(key.MonthlyTokenLimit, monthlyTokens, reservation); err != nil {
		return resolvedModel{}, &apiProblem{Status: http.StatusTooManyRequests, Code: "monthly_token_limit_exceeded", Type: "rate_limit_error", Message: "Monthly token limit exceeded."}
	}
	return resolved, nil
}
