package server

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/plugins"
)

// PluginService is the server-facing plugin management surface. Keeping it as
// an interface lets API tests exercise sanitization without starting a plugin.
type PluginService interface {
	Install(context.Context, string) (db.Plugin, error)
	List(context.Context) ([]db.Plugin, error)
	Get(context.Context, string) (db.Plugin, error)
	Enable(context.Context, string) (db.Plugin, error)
	Disable(context.Context, string) (db.Plugin, error)
	Discover(context.Context, string) ([]db.PluginTool, error)
	Update(context.Context, string) (db.Plugin, error)
	Health(context.Context, string) plugins.Health
	HasTool(context.Context, string) (bool, error)
	Uninstall(context.Context, string) error
}

type pluginEnvironmentConfigurer interface {
	ConfiguredEnvironment(context.Context, db.Plugin) (map[string]bool, error)
}

type pluginInstallPayload struct {
	RootPath string `json:"rootPath"`
}

type pluginEnablePayload struct {
	ConfirmExecuteLocalCode bool `json:"confirmExecuteLocalCode"`
}

type pluginEnvironmentResponse struct {
	Key        string `json:"key"`
	Configured bool   `json:"configured"`
}

type pluginResponse struct {
	ID              string                      `json:"id"`
	Slug            string                      `json:"slug"`
	Name            string                      `json:"name"`
	Version         string                      `json:"version"`
	Description     string                      `json:"description,omitempty"`
	ManifestVersion string                      `json:"manifestVersion"`
	RootPath        string                      `json:"rootPath"`
	Environment     []pluginEnvironmentResponse `json:"environment"`
	Enabled         bool                        `json:"enabled"`
	Status          string                      `json:"status"`
	Revision        int64                       `json:"revision"`
	LastCheckedAt   string                      `json:"lastCheckedAt,omitempty"`
	CreatedAt       string                      `json:"createdAt"`
	UpdatedAt       string                      `json:"updatedAt"`
}

type pluginToolsResponse struct {
	PluginID string          `json:"pluginId"`
	Tools    []db.PluginTool `json:"tools"`
	Count    int             `json:"count"`
}

func (s *Server) listPlugins(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	items, err := service.List(r.Context())
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadGateway), err)
		return
	}
	responses := make([]pluginResponse, 0, len(items))
	for _, item := range items {
		response, err := makePluginResponse(r.Context(), service, item)
		if err != nil {
			writeError(w, http.StatusBadGateway, "read plugin environment configuration: "+err.Error())
			return
		}
		responses = append(responses, response)
	}
	writeJSON(w, http.StatusOK, responses)
}

func (s *Server) installPlugin(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	var payload pluginInstallPayload
	if err := decodeJSON(r, &payload); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	payload.RootPath = strings.TrimSpace(payload.RootPath)
	if payload.RootPath == "" {
		writeError(w, http.StatusBadRequest, "rootPath is required")
		return
	}
	plugin, err := service.Install(r.Context(), payload.RootPath)
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadRequest), err)
		return
	}
	response, err := makePluginResponse(r.Context(), service, plugin)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read plugin environment configuration: "+err.Error())
		return
	}
	s.invalidatePluginApprovals("plugin installed")
	if err := s.recordPluginAudit(r.Context(), "plugin.install", plugin, "success", "medium", -1); err != nil {
		writeError(w, http.StatusInternalServerError, "plugin was installed but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) getPlugin(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	plugin, err := service.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadGateway), err)
		return
	}
	response, err := makePluginResponse(r.Context(), service, plugin)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read plugin environment configuration: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) enablePlugin(w http.ResponseWriter, r *http.Request) {
	s.changePluginEnabled(w, r, true)
}

func (s *Server) disablePlugin(w http.ResponseWriter, r *http.Request) {
	s.changePluginEnabled(w, r, false)
}

func (s *Server) changePluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	if enabled {
		var payload pluginEnablePayload
		if err := decodeJSON(r, &payload); err != nil {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
			return
		}
		if !payload.ConfirmExecuteLocalCode {
			writeError(w, http.StatusBadRequest, "confirmExecuteLocalCode must be true to enable a local code plugin")
			return
		}
	}
	var (
		plugin db.Plugin
		err    error
	)
	if enabled {
		plugin, err = service.Enable(r.Context(), chi.URLParam(r, "id"))
	} else {
		plugin, err = service.Disable(r.Context(), chi.URLParam(r, "id"))
	}
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadGateway), err)
		return
	}
	response, err := makePluginResponse(r.Context(), service, plugin)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read plugin environment configuration: "+err.Error())
		return
	}
	action := "disabled"
	if enabled {
		action = "enabled"
	}
	s.invalidatePluginApprovals("plugin " + action)
	risk := "medium"
	if enabled {
		risk = "high"
	}
	if err := s.recordPluginAudit(r.Context(), "plugin."+action, plugin, "success", risk, -1); err != nil {
		writeError(w, http.StatusInternalServerError, "plugin was "+action+" but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) discoverPlugin(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	tools, err := service.Discover(r.Context(), id)
	if err != nil {
		status := pluginStatusFromError(err, http.StatusBadGateway)
		if status == http.StatusInternalServerError || status == http.StatusBadRequest {
			status = http.StatusBadGateway
		}
		s.writeRequestError(w, r, status, err)
		return
	}
	plugin, err := service.Get(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusInternalServerError), err)
		return
	}
	s.invalidatePluginApprovals("plugin tools discovered")
	if err := s.recordPluginAudit(r.Context(), "plugin.discover", plugin, "success", "high", len(tools)); err != nil {
		writeError(w, http.StatusInternalServerError, "plugin tools were discovered but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, pluginToolsResponse{PluginID: id, Tools: tools, Count: len(tools)})
}

// updatePlugin re-reads the manifest from the plugin's stored root path and
// adopts it. The plugin is always left disabled — updating an enabled plugin
// is allowed but disables it, and even an unchanged manifest stays disabled —
// because the manifest may change the code being executed, so the user must
// re-confirm by re-enabling.
func (s *Server) updatePlugin(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	plugin, err := service.Update(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadRequest), err)
		return
	}
	response, err := makePluginResponse(r.Context(), service, plugin)
	if err != nil {
		writeError(w, http.StatusBadGateway, "read plugin environment configuration: "+err.Error())
		return
	}
	s.invalidatePluginApprovals("plugin updated")
	if err := s.recordPluginAudit(r.Context(), "plugin.update", plugin, "success", "medium", -1); err != nil {
		writeError(w, http.StatusInternalServerError, "plugin was updated but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// pluginHealth reports plugin health. Health is a report, so an unhealthy
// plugin still responds 200; only an unknown plugin id is an HTTP error. A
// launch is only attempted (and therefore only audited) for enabled plugins;
// disabled plugins never execute.
func (s *Server) pluginHealth(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	plugin, err := service.Get(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusBadGateway), err)
		return
	}
	health := service.Health(r.Context(), id)
	if plugin.Enabled {
		outcome := "failure"
		toolCount := -1
		if health.Healthy {
			outcome = "success"
			toolCount = health.ToolCount
		}
		if err := s.recordPluginAudit(r.Context(), "plugin.health", plugin, outcome, "medium", toolCount); err != nil {
			writeError(w, http.StatusInternalServerError, "plugin health was checked but audit persistence failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, health)
}

func (s *Server) uninstallPlugin(w http.ResponseWriter, r *http.Request) {
	service, ok := s.pluginService(w)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	plugin, err := service.Get(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusConflict), err)
		return
	}
	if err := service.Uninstall(r.Context(), id); err != nil {
		s.writeRequestError(w, r, pluginStatusFromError(err, http.StatusConflict), err)
		return
	}
	s.invalidatePluginApprovals("plugin uninstalled")
	if err := s.recordPluginAudit(r.Context(), "plugin.uninstall", plugin, "success", "medium", -1); err != nil {
		writeError(w, http.StatusInternalServerError, "plugin was uninstalled but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sourceDeleted": false})
}

func (s *Server) pluginService(w http.ResponseWriter) (PluginService, bool) {
	if s.plugins == nil {
		writeError(w, http.StatusBadGateway, "plugin service is unavailable")
		return nil, false
	}
	return s.plugins, true
}

func makePluginResponse(ctx context.Context, service PluginService, plugin db.Plugin) (pluginResponse, error) {
	configured := make(map[string]bool, len(plugin.Env)+len(plugin.SecretRefs))
	for key := range plugin.Env {
		configured[key] = true
	}
	if source, ok := service.(pluginEnvironmentConfigurer); ok {
		resolved, err := source.ConfiguredEnvironment(ctx, plugin)
		if err != nil {
			return pluginResponse{}, err
		}
		for key, value := range resolved {
			configured[key] = value
		}
	} else {
		for key := range plugin.SecretRefs {
			configured[key] = false
		}
	}
	keys := make([]string, 0, len(configured))
	for key := range configured {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]pluginEnvironmentResponse, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, pluginEnvironmentResponse{Key: key, Configured: configured[key]})
	}
	return pluginResponse{
		ID: plugin.ID, Slug: plugin.Slug, Name: plugin.Name, Version: plugin.Version,
		Description: plugin.Description, ManifestVersion: plugin.ManifestVersion, RootPath: plugin.RootPath,
		Environment: environment,
		Enabled:     plugin.Enabled, Status: plugin.Status, Revision: plugin.Revision,
		LastCheckedAt: plugin.LastCheckedAt,
		CreatedAt:     plugin.CreatedAt, UpdatedAt: plugin.UpdatedAt,
	}, nil
}

func pluginStatusFromError(err error, fallback int) int {
	if err == nil {
		return http.StatusOK
	}
	// fs.ErrNotExist covers a missing plugin manifest or root directory on all
	// platforms; the Windows error text does not match the substrings below.
	if db.IsNotFound(err) || errors.Is(err, http.ErrMissingFile) || errors.Is(err, fs.ErrNotExist) {
		return http.StatusNotFound
	}
	if db.IsConflict(err) {
		return http.StatusConflict
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not found") || strings.Contains(message, "does not exist") || strings.Contains(message, "no such file") {
		return http.StatusNotFound
	}
	if strings.Contains(message, "already exists") || strings.Contains(message, "conflict") || strings.Contains(message, "disabled") || strings.Contains(message, "not enabled") || strings.Contains(message, "not configured") {
		return http.StatusConflict
	}
	return fallback
}

func (s *Server) recordPluginAudit(ctx context.Context, action string, plugin db.Plugin, outcome, risk string, toolCount int) error {
	details := map[string]any{
		"slug":    plugin.Slug,
		"version": plugin.Version,
		"enabled": plugin.Enabled,
		"status":  plugin.Status,
	}
	if toolCount >= 0 {
		details["toolCount"] = toolCount
	}
	return s.recordAudit(ctx, audit.Event{
		Category: "plugin", Action: action, Actor: "local-api",
		SubjectType: "plugin", SubjectID: plugin.ID,
		Outcome: outcome, Risk: risk, Details: details,
	})
}

func (s *Server) invalidatePluginApprovals(reason string) {
	if s.runner != nil {
		s.runner.InvalidatePolicyApprovals(reason)
	}
}
