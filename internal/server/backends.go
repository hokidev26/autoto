package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const backendHealthTimeout = 5 * time.Second

type backendResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	BaseURL          string `json:"baseUrl"`
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
	Active           bool   `json:"active"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type backendPayload struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
	Active  bool   `json:"active"`
}

type updateBackendPayload struct {
	Name    *string `json:"name"`
	Kind    *string `json:"kind"`
	BaseURL *string `json:"baseUrl"`
	APIKey  *string `json:"apiKey"`
	Active  *bool   `json:"active"`
}

type backendHealthResponse struct {
	BackendID string               `json:"backendId"`
	OK        bool                 `json:"ok"`
	Status    string               `json:"status"`
	CheckedAt string               `json:"checkedAt"`
	LatencyMS int64                `json:"latencyMs"`
	Checks    []backendHealthCheck `json:"checks"`
	Info      map[string]any       `json:"info,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type backendHealthCheck struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	StatusCode int    `json:"statusCode,omitempty"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

func (s *Server) listBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := s.store.ListBackends(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	responses := make([]backendResponse, 0, len(backends))
	for _, backend := range backends {
		responses = append(responses, makeBackendResponse(backend))
	}
	writeJSON(w, http.StatusOK, responses)
}

func (s *Server) getBackend(w http.ResponseWriter, r *http.Request) {
	backend, err := s.store.GetBackend(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, makeBackendResponse(backend))
}

func (s *Server) createBackend(w http.ResponseWriter, r *http.Request) {
	var req backendPayload
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	backend, err := backendFromPayload(req)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateBackend(r.Context(), backend)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, makeBackendResponse(created))
}

func (s *Server) updateBackend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetBackend(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}

	var req updateBackendPayload
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Name != nil {
		existing.Name = strings.TrimSpace(*req.Name)
	}
	if req.Kind != nil {
		existing.Kind = normalizeBackendKind(*req.Kind)
	}
	if req.BaseURL != nil {
		existing.BaseURL = normalizeBackendBaseURL(*req.BaseURL)
	}
	if req.APIKey != nil {
		existing.APIKey = strings.TrimSpace(*req.APIKey)
	}
	if req.Active != nil {
		existing.Active = *req.Active
	}
	if err := validateBackend(existing); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateBackend(r.Context(), existing)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, makeBackendResponse(updated))
}

func (s *Server) activateBackend(w http.ResponseWriter, r *http.Request) {
	backend, err := s.store.ActivateBackend(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, makeBackendResponse(backend))
}

func (s *Server) deleteBackend(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteBackend(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) backendHealth(w http.ResponseWriter, r *http.Request) {
	backend, err := s.store.GetBackend(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, checkBackendHealth(r.Context(), backend))
}

func makeBackendResponse(backend db.Backend) backendResponse {
	return backendResponse{
		ID:               backend.ID,
		Name:             backend.Name,
		Kind:             backend.Kind,
		BaseURL:          backend.BaseURL,
		APIKeyConfigured: backend.APIKey != "",
		Active:           backend.Active,
		CreatedAt:        backend.CreatedAt,
		UpdatedAt:        backend.UpdatedAt,
	}
}

func backendFromPayload(req backendPayload) (db.Backend, error) {
	backend := db.Backend{
		Name:    strings.TrimSpace(req.Name),
		Kind:    normalizeBackendKind(req.Kind),
		BaseURL: normalizeBackendBaseURL(req.BaseURL),
		APIKey:  strings.TrimSpace(req.APIKey),
		Active:  req.Active,
	}
	if backend.Name == "" {
		backend.Name = defaultBackendName(backend.BaseURL)
	}
	if err := validateBackend(backend); err != nil {
		return db.Backend{}, err
	}
	return backend, nil
}

func validateBackend(backend db.Backend) error {
	if strings.TrimSpace(backend.Name) == "" {
		return errors.New("name is required")
	}
	if backend.Kind != "local" && backend.Kind != "cloud" {
		return errors.New("kind must be local or cloud")
	}
	if backend.BaseURL == "" {
		return errors.New("baseUrl is required")
	}
	parsed, err := url.Parse(backend.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("baseUrl must be an absolute http(s) URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("baseUrl must use http or https")
	}
	return nil
}

func normalizeBackendKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "cloud" {
		return "cloud"
	}
	return "local"
}

func normalizeBackendBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(trimmed, "/")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func defaultBackendName(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return "Agent Server"
	}
	return parsed.Host
}

func checkBackendHealth(ctx context.Context, backend db.Backend) backendHealthResponse {
	started := time.Now()
	response := backendHealthResponse{
		BackendID: backend.ID,
		Status:    "offline",
		CheckedAt: started.UTC().Format(time.RFC3339Nano),
	}
	client := &http.Client{Timeout: backendHealthTimeout}
	root := agentServerRootURL(backend.BaseURL)
	checks := []struct {
		name string
		path string
	}{
		{name: "alive", path: "/alive"},
		{name: "health", path: "/health"},
		{name: "ready", path: "/ready"},
		{name: "server_info", path: "/server_info"},
	}

	var livenessOK bool
	var readyInitializing bool
	var lastErr string
	for _, item := range checks {
		check, info := runBackendHealthCheck(ctx, client, backend, root, item.name, item.path)
		response.Checks = append(response.Checks, check)
		if check.OK && (item.name == "alive" || item.name == "health") {
			livenessOK = true
		}
		if item.name == "ready" && check.StatusCode == http.StatusServiceUnavailable {
			readyInitializing = true
		}
		if item.name == "server_info" && info != nil {
			response.Info = info
		}
		if check.Error != "" {
			lastErr = check.Error
		}
	}
	response.OK = livenessOK && !readyInitializing
	if livenessOK {
		response.Status = "online"
	}
	if readyInitializing {
		response.Status = "initializing"
	}
	if !livenessOK && lastErr != "" {
		response.Error = lastErr
	}
	response.LatencyMS = time.Since(started).Milliseconds()
	return response
}

func runBackendHealthCheck(ctx context.Context, client *http.Client, backend db.Backend, root, name, path string) (backendHealthCheck, map[string]any) {
	endpoint := root + path
	check := backendHealthCheck{Name: name, URL: endpoint}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		check.Error = err.Error()
		return check, nil
	}
	for key, value := range backendAuthHeaders(backend) {
		req.Header.Set(key, value)
	}
	res, err := client.Do(req)
	if err != nil {
		check.Error = err.Error()
		return check, nil
	}
	defer res.Body.Close()
	check.StatusCode = res.StatusCode
	check.OK = res.StatusCode >= 200 && res.StatusCode < 300
	body, _ := io.ReadAll(io.LimitReader(res.Body, 512*1024))
	if !check.OK {
		check.Error = fmt.Sprintf("%d %s", res.StatusCode, http.StatusText(res.StatusCode))
		return check, nil
	}
	if name != "server_info" || len(body) == 0 {
		return check, nil
	}
	var info map[string]any
	if err := json.Unmarshal(body, &info); err != nil {
		return check, nil
	}
	return check, info
}

func backendAuthHeaders(backend db.Backend) map[string]string {
	if backend.APIKey == "" {
		return nil
	}
	if backend.Kind == "cloud" {
		return map[string]string{"Authorization": "Bearer " + backend.APIKey}
	}
	return map[string]string{"X-Session-API-Key": backend.APIKey}
}

func agentServerRootURL(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(baseURL, "/")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := strings.TrimRight(parsed.Path, "/")
	if path == "/api" || strings.HasSuffix(path, "/api") {
		path = strings.TrimSuffix(path, "/api")
	}
	parsed.Path = path
	return strings.TrimRight(parsed.String(), "/")
}
