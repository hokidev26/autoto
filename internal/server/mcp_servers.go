package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/mcp"
)

const mcpDiscoveryTimeout = 20 * time.Second

type mcpServerResponse struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	CWD       string   `json:"cwd,omitempty"`
	EnvKeys   []string `json:"envKeys,omitempty"`
	Enabled   bool     `json:"enabled"`
	CreatedAt string   `json:"createdAt"`
	UpdatedAt string   `json:"updatedAt"`
}

type mcpServerPayload struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args,omitempty"`
	CWD       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Enabled   *bool             `json:"enabled,omitempty"`
}

type updateMCPServerPayload struct {
	Name      *string            `json:"name"`
	Transport *string            `json:"transport"`
	Command   *string            `json:"command"`
	Args      *[]string          `json:"args"`
	CWD       *string            `json:"cwd"`
	Env       *map[string]string `json:"env"`
	Enabled   *bool              `json:"enabled"`
}

type mcpToolsResponse struct {
	ServerID  string     `json:"serverId"`
	Tools     []mcp.Tool `json:"tools"`
	Count     int        `json:"count"`
	CheckedAt string     `json:"checkedAt"`
}

func (s *Server) listMCPServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListMCPServers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	responses := make([]mcpServerResponse, 0, len(servers))
	for _, server := range servers {
		responses = append(responses, makeMCPServerResponse(server))
	}
	writeJSON(w, http.StatusOK, responses)
}

func (s *Server) getMCPServer(w http.ResponseWriter, r *http.Request) {
	server, err := s.store.GetMCPServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, makeMCPServerResponse(server))
}

func (s *Server) createMCPServer(w http.ResponseWriter, r *http.Request) {
	var req mcpServerPayload
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	server, err := mcpServerFromPayload(req)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	created, err := s.store.CreateMCPServer(r.Context(), server)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, makeMCPServerResponse(created))
}

func (s *Server) updateMCPServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetMCPServer(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	previous := existing
	var req updateMCPServerPayload
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Name != nil {
		existing.Name = strings.TrimSpace(*req.Name)
	}
	if req.Transport != nil {
		existing.Transport = normalizeMCPTransport(*req.Transport)
	}
	if req.Command != nil {
		existing.Command = strings.TrimSpace(*req.Command)
	}
	if req.Args != nil {
		existing.Args = normalizeStringSlice(*req.Args)
	}
	if req.CWD != nil {
		existing.CWD = strings.TrimSpace(*req.CWD)
	}
	if req.Env != nil {
		existing.Env = normalizeMCPEnv(*req.Env)
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if err := validateMCPServer(existing); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	updated, err := s.store.UpdateMCPServer(r.Context(), existing)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	if mcpSessionMustInvalidate(previous, updated) {
		mcp.InvalidateRegisteredServer(updated.ID)
	}
	writeJSON(w, http.StatusOK, makeMCPServerResponse(updated))
}

func (s *Server) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteMCPServer(r.Context(), id); err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	mcp.InvalidateRegisteredServer(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listMCPServerTools(w http.ResponseWriter, r *http.Request) {
	// Tool discovery launches the configured stdio command on this host. Keep
	// local discovery compatible, but require an authenticated full remote
	// session before a remote caller can cause that host-side execution.
	auth := s.remoteAccessAuthentication(r)
	if auth.Remote && (!auth.Authenticated || !auth.Session || auth.Mode != remoteAccessModeFull) {
		writeError(w, http.StatusForbidden, "MCP tool discovery requires a full remote session")
		return
	}
	server, err := s.store.GetMCPServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	if !server.Enabled {
		writeError(w, http.StatusConflict, "mcp server is disabled")
		return
	}
	tools, err := discoverMCPTools(r.Context(), server)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, mcpToolsResponse{ServerID: server.ID, Tools: tools, Count: len(tools), CheckedAt: db.Now()})
}

func makeMCPServerResponse(server db.MCPServer) mcpServerResponse {
	keys := make([]string, 0, len(server.Env))
	for key := range server.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return mcpServerResponse{
		ID: server.ID, Name: server.Name, Transport: server.Transport, Command: server.Command,
		Args: append([]string(nil), server.Args...), CWD: server.CWD, EnvKeys: keys,
		Enabled: server.Enabled, CreatedAt: server.CreatedAt, UpdatedAt: server.UpdatedAt,
	}
}

func mcpServerFromPayload(req mcpServerPayload) (db.MCPServer, error) {
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	server := db.MCPServer{
		Name: strings.TrimSpace(req.Name), Transport: normalizeMCPTransport(req.Transport),
		Command: strings.TrimSpace(req.Command), Args: normalizeStringSlice(req.Args),
		CWD: strings.TrimSpace(req.CWD), Env: normalizeMCPEnv(req.Env), Enabled: enabled,
	}
	if server.Name == "" {
		server.Name = defaultMCPServerName(server.Command)
	}
	if err := validateMCPServer(server); err != nil {
		return db.MCPServer{}, err
	}
	return server, nil
}

func mcpSessionMustInvalidate(before, after db.MCPServer) bool {
	if !after.Enabled || before.Enabled != after.Enabled {
		return true
	}
	return before.Command != after.Command || before.Transport != after.Transport || before.CWD != after.CWD || !slices.Equal(before.Args, after.Args) || !maps.Equal(before.Env, after.Env)
}

func validateMCPServer(server db.MCPServer) error {
	if strings.TrimSpace(server.Name) == "" {
		return errors.New("name is required")
	}
	if server.Transport != "stdio" {
		return errors.New("transport must be stdio")
	}
	if strings.TrimSpace(server.Command) == "" {
		return errors.New("command is required")
	}
	return nil
}

func normalizeMCPTransport(transport string) string {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		return "stdio"
	}
	return transport
}

func normalizeStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func normalizeMCPEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for key, value := range env {
		trimmed := strings.TrimSpace(key)
		if trimmed != "" && !strings.Contains(trimmed, "=") {
			out[trimmed] = value
		}
	}
	return out
}

func defaultMCPServerName(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return "MCP Server"
	}
	parts := strings.Fields(command)
	if len(parts) > 0 {
		command = parts[0]
	}
	base := filepath.Base(command)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "MCP Server"
	}
	return base
}

func discoverMCPTools(ctx context.Context, server db.MCPServer) ([]mcp.Tool, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpDiscoveryTimeout)
	defer cancel()
	client, err := mcp.StartStdio(ctx, mcp.StdioConfig{Command: server.Command, Args: server.Args, CWD: server.CWD, Env: server.Env, Timeout: mcpDiscoveryTimeout})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := client.Initialize(ctx); err != nil {
		return nil, err
	}
	return client.ListTools(ctx)
}
