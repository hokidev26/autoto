package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/audit"
	"autoto/internal/db"
	"autoto/internal/devices"
	"autoto/internal/integrations"
	"autoto/internal/secrets"
)

const telegramOfficialEndpoint = "https://api.telegram.org"

type integrationConnectionRequest struct {
	Kind       string            `json:"kind"`
	Name       string            `json:"name"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Endpoint   string            `json:"endpoint,omitempty"`
	Settings   json.RawMessage   `json:"settings,omitempty"`
	SecretRefs map[string]string `json:"secretRefs"`
}

type integrationConnectionPatch struct {
	Name       *string            `json:"name,omitempty"`
	Enabled    *bool              `json:"enabled,omitempty"`
	Endpoint   *string            `json:"endpoint,omitempty"`
	Settings   *json.RawMessage   `json:"settings,omitempty"`
	SecretRefs *map[string]string `json:"secretRefs,omitempty"`
}

func (s *Server) connectionService() *integrations.ConnectionService {
	if s.connections != nil {
		return s.connections
	}
	return integrations.NewConnectionService(s.store, secrets.EnvResolver{})
}

func (s *Server) listIntegrationConnections(w http.ResponseWriter, r *http.Request) {
	items, err := s.connectionService().ListPublic(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) createIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	var req integrationConnectionRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	connection := db.IntegrationConnection{Kind: req.Kind, Name: req.Name, Enabled: enabled, Endpoint: req.Endpoint, SettingsJSON: req.Settings, SecretRefs: req.SecretRefs}
	if err := validateIntegrationConnection(&connection); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateIntegrationConnection(r.Context(), connection)
	if err != nil {
		if db.IsConflict(err) {
			s.writeRequestError(w, r, http.StatusConflict, err)
		} else {
			s.writeRequestError(w, r, http.StatusBadRequest, err)
		}
		return
	}
	view, err := s.connectionService().GetPublic(r.Context(), created.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "integration", Action: "connection.create", Actor: "local-api", SubjectType: "integration_connection", SubjectID: created.ID, Outcome: "success", Risk: "medium", Details: map[string]any{"kind": created.Kind, "enabled": created.Enabled}}); err != nil {
		writeError(w, http.StatusInternalServerError, "connection was created but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (s *Server) updateIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetIntegrationConnection(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	var req integrationConnectionPatch
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Name != nil {
		current.Name = *req.Name
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.Endpoint != nil {
		current.Endpoint = *req.Endpoint
	}
	if req.Settings != nil {
		current.SettingsJSON = append(json.RawMessage(nil), (*req.Settings)...)
	}
	if req.SecretRefs != nil {
		current.SecretRefs = cloneStringMap(*req.SecretRefs)
	}
	if err := validateIntegrationConnection(&current); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateIntegrationConnection(r.Context(), current)
	if err != nil {
		if db.IsConflict(err) {
			s.writeRequestError(w, r, http.StatusConflict, err)
		} else {
			s.writeStoreError(w, r, err)
		}
		return
	}
	view, err := s.connectionService().GetPublic(r.Context(), updated.ID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "integration", Action: "connection.update", Actor: "local-api", SubjectType: "integration_connection", SubjectID: updated.ID, Outcome: "success", Risk: "medium", Details: map[string]any{"kind": updated.Kind, "enabled": updated.Enabled}}); err != nil {
		writeError(w, http.StatusInternalServerError, "connection was updated but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) deleteIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetIntegrationConnection(r.Context(), id)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.store.DeleteIntegrationConnection(r.Context(), id); err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := s.recordAudit(r.Context(), audit.Event{Category: "integration", Action: "connection.delete", Actor: "local-api", SubjectType: "integration_connection", SubjectID: current.ID, Outcome: "success", Risk: "medium", Details: map[string]any{"kind": current.Kind}}); err != nil {
		writeError(w, http.StatusInternalServerError, "connection was deleted but audit persistence failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) testIntegrationConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	resolved, err := s.connectionService().Resolve(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, "integration connection test failed")
		return
	}
	if !resolved.Enabled {
		writeError(w, http.StatusConflict, "integration connection is disabled")
		return
	}
	client := s.integrationHTTPClient()
	switch resolved.Kind {
	case "telegram":
		err = testTelegramConnection(r.Context(), resolved, client)
	case devices.HomeAssistantKind:
		var adapter devices.Adapter
		adapter, err = devices.NewAdapter(resolved, client)
		if err == nil {
			_, err = adapter.ListDevices(r.Context())
		}
	default:
		err = errors.New("unsupported integration kind")
	}
	outcome := "success"
	status := http.StatusOK
	if err != nil {
		outcome = "failure"
		status = http.StatusBadGateway
	}
	_ = s.recordAudit(context.WithoutCancel(r.Context()), audit.Event{Category: "integration", Action: "connection.test", Actor: "local-api", SubjectType: "integration_connection", SubjectID: id, Outcome: outcome, Risk: "low", Details: map[string]any{"kind": resolved.Kind}})
	if err != nil {
		writeError(w, status, "integration connection test failed")
		return
	}
	writeJSON(w, status, map[string]any{"ok": true, "testedAt": db.Now()})
}

func validateIntegrationConnection(connection *db.IntegrationConnection) error {
	if connection == nil {
		return errors.New("connection is required")
	}
	connection.Kind = strings.TrimSpace(connection.Kind)
	connection.Name = strings.TrimSpace(connection.Name)
	connection.Endpoint = strings.TrimSpace(connection.Endpoint)
	if connection.Name == "" {
		return errors.New("name is required")
	}
	if len(connection.SettingsJSON) == 0 {
		connection.SettingsJSON = json.RawMessage(`{}`)
	}
	if !isJSONObject(connection.SettingsJSON) {
		return errors.New("settings must be a JSON object")
	}
	switch connection.Kind {
	case "telegram":
		if connection.Endpoint != "" && connection.Endpoint != telegramOfficialEndpoint {
			return errors.New("telegram endpoint is fixed to the official API")
		}
		connection.Endpoint = telegramOfficialEndpoint
		if err := requireSecretRefs(connection.SecretRefs, "botToken"); err != nil {
			return err
		}
	case devices.HomeAssistantKind:
		if connection.Endpoint == "" {
			return errors.New("home-assistant endpoint is required")
		}
		if err := validateHomeAssistantEndpoint(connection.Endpoint); err != nil {
			return err
		}
		if err := requireSecretRefs(connection.SecretRefs, "accessToken"); err != nil {
			return err
		}
		_, err := devices.NewClient(integrations.ResolvedConnection{Kind: devices.HomeAssistantKind, Endpoint: connection.Endpoint, Secrets: map[string]string{"accessToken": "validation"}}, &http.Client{})
		if err != nil {
			return errors.New("invalid home-assistant endpoint")
		}
	default:
		return errors.New("kind must be telegram or home-assistant")
	}
	return nil
}

func validateHomeAssistantEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return errors.New("invalid home-assistant endpoint")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return nil
	}
	return errors.New("home-assistant endpoint must use a local or private-network host")
}

func requireSecretRefs(refs map[string]string, required string) error {
	if len(refs) != 1 {
		return errors.New("exactly one secret reference is required")
	}
	value, ok := refs[required]
	if !ok {
		return errors.New(required + " secret reference is required")
	}
	if _, err := secrets.ParseRef(value); err != nil {
		return errors.New("secret references must use env:VARIABLE_NAME")
	}
	return nil
}

func isJSONObject(raw json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func (s *Server) integrationHTTPClient() *http.Client {
	base := s.integrationClient
	if base == nil {
		base = &http.Client{}
	}
	client := *base
	client.Timeout = 5 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func testTelegramConnection(ctx context.Context, connection integrations.ResolvedConnection, client *http.Client) error {
	if connection.Endpoint != telegramOfficialEndpoint {
		return errors.New("invalid telegram endpoint")
	}
	token := connection.Secrets["botToken"]
	if token == "" || token != strings.TrimSpace(token) || len(token) > 512 || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid telegram credential")
	}
	endpoint := telegramOfficialEndpoint + "/bot" + url.PathEscape(token) + "/getMe"
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("telegram request failed")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("telegram request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("telegram request failed")
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(body, &result) != nil || !result.OK {
		return errors.New("telegram request failed")
	}
	return nil
}
