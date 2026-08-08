package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": config.Version})
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hasUsers": hasUsers, "registrationOpen": s.configSnapshot().Auth.RegistrationOpen})
}

type settingsProviderHeaderResponse struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type settingsProviderResponse struct {
	Name                    string                           `json:"name"`
	Type                    string                           `json:"type"`
	Profile                 string                           `json:"profile,omitempty"`
	BaseURL                 string                           `json:"baseUrl,omitempty"`
	Model                   string                           `json:"model"`
	Models                  []config.ProviderModelConfig     `json:"models"`
	MaxTokens               int64                            `json:"maxTokens,omitempty"`
	Configured              bool                             `json:"configured"`
	APIKeyConfigured        bool                             `json:"apiKeyConfigured"`
	APIKeyPersisted         bool                             `json:"apiKeyPersisted"`
	APIKeyLastFive          string                           `json:"apiKeyLastFive,omitempty"`
	APIKeySource            string                           `json:"apiKeySource"`
	APIKeyOptional          bool                             `json:"apiKeyOptional,omitempty"`
	GatewayEnabled          bool                             `json:"gatewayEnabled"`
	Enabled                 bool                             `json:"enabled"`
	Origin                  string                           `json:"origin"`
	ProxyURL                string                           `json:"proxyUrl,omitempty"`
	ProxyAuthConfigured     bool                             `json:"proxyAuthConfigured"`
	ProxyAuthPersisted      bool                             `json:"proxyAuthPersisted"`
	ProxyAuthSource         string                           `json:"proxyAuthSource"`
	UserAgent               string                           `json:"userAgent,omitempty"`
	RequestHeaders          []settingsProviderHeaderResponse `json:"requestHeaders,omitempty"`
	RequestHeadersPersisted bool                             `json:"requestHeadersPersisted"`
	RequestHeadersSource    string                           `json:"requestHeadersSource"`
	InsecureSkipTLSVerify   bool                             `json:"insecureSkipTLSVerify"`
	AllowPlaintextHTTP      bool                             `json:"allowPlaintextHTTP"`
	Capabilities            providers.Capabilities           `json:"capabilities"`
	Management              *providerManagementResponse      `json:"management,omitempty"`
}

func (s *Server) settingsProviderResponse(ctx context.Context, provider config.ProviderConfig) settingsProviderResponse {
	safeProvider := config.NormalizeProviderConfig(provider)
	summary := safeProvider.Summary()
	metadata := s.providerSettingsMetadata(summary, safeProvider)
	keyStatus := s.providerAPIKeyStatus(ctx, provider)
	proxyStatus := s.providerProxyAuthStatus(ctx, provider)
	headerStatus := s.providerRequestHeadersStatus(ctx, provider)
	headers := make([]settingsProviderHeaderResponse, 0, len(provider.RequestHeaders))
	for _, header := range provider.RequestHeaders {
		name := strings.TrimSpace(header.Name)
		if name == "" {
			continue
		}
		headers = append(headers, settingsProviderHeaderResponse{Name: name, Configured: headerStatus.Configured && header.Value != ""})
	}
	return settingsProviderResponse{
		Name: summary.Name, Type: summary.Type, Profile: metadata.Profile, BaseURL: summary.BaseURL, Model: summary.Model,
		Models: safeProvider.Models, MaxTokens: summary.MaxTokens, Configured: s.providerConfigured(summary), APIKeyConfigured: keyStatus.Configured,
		APIKeyPersisted: keyStatus.Persisted, APIKeyLastFive: keyStatus.LastFive, APIKeySource: keyStatus.Source,
		APIKeyOptional: summary.APIKeyOptional, GatewayEnabled: summary.GatewayEnabled, Enabled: summary.Enabled,
		Origin: summary.Origin, ProxyURL: safeProvider.ProxyURL, ProxyAuthConfigured: proxyStatus.Configured,
		ProxyAuthPersisted: proxyStatus.Persisted, ProxyAuthSource: proxyStatus.Source, UserAgent: provider.UserAgent,
		RequestHeaders: headers, RequestHeadersPersisted: headerStatus.Persisted, RequestHeadersSource: headerStatus.Source,
		InsecureSkipTLSVerify: provider.InsecureSkipTLSVerify, AllowPlaintextHTTP: provider.AllowPlaintextHTTP,
		Capabilities: metadata.Capabilities, Management: metadata.Management,
	}
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	cfg := s.configSnapshot()
	providerResponses := make([]settingsProviderResponse, 0, len(cfg.Providers.Instances))
	for _, provider := range cfg.Providers.Instances {
		providerResponses = append(providerResponses, s.settingsProviderResponse(r.Context(), provider))
	}
	runtimeSettings, err := s.runtimeSettingsForResponse(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"server":                         cfg.Server,
		"gateway":                        cfg.Gateway,
		"background":                     cfg.Background,
		"paths":                          cfg.Paths,
		"agent":                          cfg.Agent,
		"agentModelSettingsEndpoint":     "/api/runtime/agent-model-settings",
		"continuationSettingsEndpoint":   "/api/runtime/continuation-settings",
		"backgroundTaskSettingsEndpoint": "/api/runtime/background-task-settings",
		"retryPolicySettingsEndpoint":    "/api/runtime/retry-policy-settings",
		"contextSettingsEndpoint":        "/api/runtime/context-settings",
		"contextManagement":              cfg.ContextManagement,
		"providers":                      providerResponses,
		"runtimeSettings":                runtimeSettings,
		"tierOrder":                      subscriptionTierOrderSnapshot(),
		"version":                        config.Version,
	})
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var projects []db.Project
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		projects, err = s.store.ListProjectsForUser(r.Context(), user.ID)
	} else {
		projects, err = s.store.ListProjects(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.filterProjectsForRequest(r, projects))
}

type createProjectRequest struct {
	Name                 string `json:"name"`
	Description          string `json:"description"`
	GitPath              string `json:"gitPath"`
	Model                string `json:"model"`
	ForceNewConversation bool   `json:"forceNewConversation"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

type createProjectConversationRequest struct {
	Title          string `json:"title"`
	Name           string `json:"name"`
	Model          string `json:"model"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type projectConversationResult struct {
	Project  db.Project
	Workline db.Workline
	Agent    db.Agent
}

type navigationStatePatchRequest struct {
	Pinned   *bool `json:"pinned"`
	Archived *bool `json:"archived"`
}

func validNavigationStatePatch(req navigationStatePatchRequest) bool {
	return req.Pinned != nil || req.Archived != nil
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := s.configSnapshot()
	gitPath := cleanProjectPath(strings.TrimSpace(req.GitPath))
	if gitPath == "" {
		gitPath = filepath.Join(cfg.Paths.DefaultProjectDir, slugify(req.Name))
	}
	resolvedGitPath, err := s.resolveCWDForRequest(r, gitPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	gitPath = resolvedGitPath
	if err := os.MkdirAll(gitPath, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	userID := ""
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		userID = user.ID
	}

	create := func() (projectConversationResult, error) {
		if req.ForceNewConversation {
			var projects []db.Project
			if hasUsers {
				projects, err = s.store.ListProjectsForUserWithOptions(r.Context(), userID, true)
			} else {
				projects, err = s.store.ListProjectsWithOptions(r.Context(), true)
			}
			if err != nil {
				return projectConversationResult{}, err
			}
			for _, existing := range projects {
				if existing.Status != "active" || existing.ArchivedAt != "" || existing.FlowMode == db.ProjectFlowModeConversation || !sameFilesystemProjectPath(existing.GitPath, gitPath) {
					continue
				}
				project, workline, agent, createErr := s.store.CreateProjectConversation(r.Context(), existing.ID, req.Name, model, permissionMode)
				return projectConversationResult{Project: project, Workline: workline, Agent: agent}, createErr
			}
		}
		var project db.Project
		var workline db.Workline
		var agent db.Agent
		if hasUsers {
			project, workline, agent, err = s.store.CreateProjectForUser(r.Context(), userID, req.Name, req.Description, gitPath, model, permissionMode)
		} else {
			project, workline, agent, err = s.store.CreateProject(r.Context(), req.Name, req.Description, gitPath, model, permissionMode)
		}
		return projectConversationResult{Project: project, Workline: workline, Agent: agent}, err
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}
	cacheKey := ""
	if req.ForceNewConversation && key != "" {
		cacheKey = "directory" + "\x00" + userID + "\x00" + filesystemProjectPathKey(gitPath) + "\x00" + key
	}

	var result projectConversationResult
	if req.ForceNewConversation {
		s.projectConversationMu.Lock()
		defer s.projectConversationMu.Unlock()
		if s.projectConversationKeys == nil {
			s.projectConversationKeys = make(map[string]projectConversationResult)
		}
		if cacheKey != "" {
			if cached, ok := s.projectConversationKeys[cacheKey]; ok {
				writeJSON(w, http.StatusOK, map[string]any{"project": cached.Project, "workline": cached.Workline, "agent": cached.Agent})
				return
			}
		}
		result, err = create()
	} else {
		result, err = create()
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cfg.Agent.DefaultStartInPlanMode {
		result.Agent, err = s.updatePersistedAgentPlanMode(r.Context(), result.Agent.ID, true)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "project was created but its default plan mode could not be applied")
			return
		}
	}
	if req.ForceNewConversation && cacheKey != "" {
		s.projectConversationKeys[cacheKey] = result
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": result.Project, "workline": result.Workline, "agent": result.Agent})
}

func filesystemProjectPathKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = filepath.Clean(value)
	if absolute, err := filepath.Abs(value); err == nil {
		value = absolute
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(value)
	}
	return value
}

func sameFilesystemProjectPath(left, right string) bool {
	leftKey := filesystemProjectPathKey(left)
	rightKey := filesystemProjectPathKey(right)
	return leftKey != "" && leftKey == rightKey
}

func (s *Server) createProjectConversation(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(chi.URLParam(r, "id"))
	var req createProjectConversationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		req.Title = strings.TrimSpace(req.Name)
	}
	cfg := s.configSnapshot()
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = cfg.Agent.DefaultModel
	}
	permissionMode := s.safeDefaultPermissionModeForRequest(r, cfg.Agent.DefaultPermissionMode)
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	userID := ""
	if hasUsers {
		user, ok := s.requireUser(w, r)
		if !ok {
			return
		}
		userID = user.ID
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = strings.TrimSpace(req.IdempotencyKey)
	}
	if len(key) > 200 {
		writeError(w, http.StatusBadRequest, "idempotency key is too long")
		return
	}
	cacheKey := ""
	if key != "" {
		cacheKey = userID + "\x00" + projectID + "\x00" + key
	}

	create := func() (projectConversationResult, error) {
		project, workline, agent, createErr := s.store.CreateProjectConversation(r.Context(), projectID, req.Title, model, permissionMode)
		if createErr != nil {
			return projectConversationResult{}, createErr
		}
		if cfg.Agent.DefaultStartInPlanMode {
			agent, createErr = s.updatePersistedAgentPlanMode(r.Context(), agent.ID, true)
			if createErr != nil {
				return projectConversationResult{}, createErr
			}
		}
		return projectConversationResult{Project: project, Workline: workline, Agent: agent}, nil
	}

	var result projectConversationResult
	if cacheKey != "" {
		s.projectConversationMu.Lock()
		defer s.projectConversationMu.Unlock()
		if s.projectConversationKeys == nil {
			s.projectConversationKeys = make(map[string]projectConversationResult)
		}
		if cached, ok := s.projectConversationKeys[cacheKey]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"project": cached.Project, "workline": cached.Workline, "agent": cached.Agent})
			return
		}
		result, err = create()
		if err == nil {
			s.projectConversationKeys[cacheKey] = result
		}
	} else {
		result, err = create()
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"project": result.Project, "workline": result.Workline, "agent": result.Agent})
}

func (s *Server) createConversation(w http.ResponseWriter, r *http.Request) {
	// Keep the old route during the compatibility window, but make the removed
	// product boundary explicit. In particular, do not decode, validate, or
	// touch the store: stale clients must not be able to create a hidden project
	// container by accident.
	writeJSON(w, http.StatusGone, map[string]any{
		"error": "standalone conversations have been removed; create or choose a project instead",
		"code":  "standalone_conversation_removed",
	})
}

func (s *Server) patchProjectNavigationState(w http.ResponseWriter, r *http.Request) {
	var req navigationStatePatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validNavigationStatePatch(req) {
		writeError(w, http.StatusBadRequest, "navigation state patch must include pinned or archived")
		return
	}
	project, err := s.store.UpdateProjectNavigationState(r.Context(), chi.URLParam(r, "id"), req.Pinned, req.Archived)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (s *Server) patchAgentNavigationState(w http.ResponseWriter, r *http.Request) {
	var req navigationStatePatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !validNavigationStatePatch(req) {
		writeError(w, http.StatusBadRequest, "navigation state patch must include pinned or archived")
		return
	}
	agent, err := s.store.UpdateAgentNavigationState(r.Context(), chi.URLParam(r, "id"), req.Pinned, req.Archived)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "agent not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agent)
}

// writeArchiveDeleteError maps the archive-deletion guards onto HTTP codes so
// the UI can tell "you must archive first" apart from "it is still running".
func writeArchiveDeleteError(w http.ResponseWriter, kind string, err error) {
	switch {
	case db.IsNotFound(err):
		writeError(w, http.StatusNotFound, kind+" not found")
	case db.IsNotArchived(err):
		writeError(w, http.StatusConflict, err.Error())
	case db.HasActiveRun(err):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) deleteArchivedProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteArchivedProject(r.Context(), id); err != nil {
		writeArchiveDeleteError(w, "project", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (s *Server) deleteArchivedAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// The durable runs check lives in the store; this catches a live in-memory
	// loop that has not yet written a run row.
	if s.runner != nil && s.runner.IsAgentRunning(id) {
		writeError(w, http.StatusConflict, "conversation is still running: interrupt it before deleting")
		return
	}
	if err := s.store.DeleteArchivedAgent(r.Context(), id); err != nil {
		writeArchiveDeleteError(w, "conversation", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, project)
}

var slugCleanup = regexp.MustCompile(`[^a-z0-9_-]+`)

func cleanProjectPath(path string) string {
	if strings.HasPrefix(path, "Users"+string(filepath.Separator)) {
		return string(filepath.Separator) + path
	}
	return path
}

func slugify(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugCleanup.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "project"
	}
	return slug
}

func (s *Server) listProjectWorklines(w http.ResponseWriter, r *http.Request) {
	worklines, err := s.store.ListWorklinesByProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.filterWorklinesForRequest(r, worklines))
}

func (s *Server) getWorkline(w http.ResponseWriter, r *http.Request) {
	workline, err := s.store.GetWorkline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "workline not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, workline)
}

func (s *Server) listWorklineAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgentsByWorkline(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.filterAgentsForRequest(r, agents))
}
