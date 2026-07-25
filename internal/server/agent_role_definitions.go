package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

const profileDefinitionRequestBytes int64 = 96 << 10

type agentRoleDefinitionRequest struct {
	ExpectedRevision *int64                    `json:"expectedRevision,omitempty"`
	Scope            *db.DefinitionScopeTarget `json:"scope,omitempty"`
	Key              string                    `json:"key"`
	DisplayName      string                    `json:"displayName"`
	Summary          string                    `json:"summary,omitempty"`
	Definition       json.RawMessage           `json:"definition"`
}

type definitionDeleteRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
}

type definitionRestoreRequest struct {
	ExpectedRevision int64 `json:"expectedRevision"`
	SourceRevision   int64 `json:"sourceRevision"`
}

func (s *Server) listAgentRoleDefinitions(w http.ResponseWriter, r *http.Request) {
	target, err := definitionScopeFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, target) {
		return
	}
	items, err := s.store.ListAgentRoleDefinitions(r.Context(), target)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAgentRoleDefinition(w http.ResponseWriter, r *http.Request) {
	var request agentRoleDefinitionRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision != nil {
		writeError(w, http.StatusBadRequest, "expectedRevision is not allowed when creating a definition")
		return
	}
	if request.Scope == nil {
		writeError(w, http.StatusBadRequest, "scope is required")
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, *request.Scope) {
		return
	}
	created, err := s.store.CreateAgentRoleDefinition(r.Context(), db.AgentRoleDefinitionInput{
		Scope: *request.Scope, Key: request.Key, DisplayName: request.DisplayName, Summary: request.Summary, DefinitionJSON: request.Definition,
	})
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getAgentRoleDefinition(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.GetAgentRoleDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(value)) {
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) updateAgentRoleDefinition(w http.ResponseWriter, r *http.Request) {
	var request agentRoleDefinitionRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be positive")
		return
	}
	current, err := s.store.GetAgentRoleDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(current)) {
		return
	}
	input := db.AgentRoleDefinitionInput{Key: request.Key, DisplayName: request.DisplayName, Summary: request.Summary, DefinitionJSON: request.Definition}
	if request.Scope != nil {
		if !s.requireDefinitionScopeAccess(w, r, *request.Scope) {
			return
		}
		input.Scope = *request.Scope
	}
	updated, err := s.store.UpdateAgentRoleDefinitionCAS(r.Context(), chi.URLParam(r, "id"), *request.ExpectedRevision, input)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteAgentRoleDefinition(w http.ResponseWriter, r *http.Request) {
	var request definitionDeleteRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be positive")
		return
	}
	current, err := s.store.GetAgentRoleDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(current)) {
		return
	}
	deleted, err := s.store.DeleteAgentRoleDefinitionCAS(r.Context(), chi.URLParam(r, "id"), request.ExpectedRevision)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted.AgentRoleDefinitionSummary)
}

func (s *Server) listAgentRoleDefinitionRevisions(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.GetAgentRoleDefinitionIncludingDeleted(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(current)) {
		return
	}
	items, err := s.store.ListAgentRoleDefinitionRevisions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) restoreAgentRoleDefinition(w http.ResponseWriter, r *http.Request) {
	var request definitionRestoreRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.ExpectedRevision < 1 || request.SourceRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision and sourceRevision must be positive")
		return
	}
	id := chi.URLParam(r, "id")
	current, err := s.store.GetAgentRoleDefinitionIncludingDeleted(r.Context(), id)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(current)) {
		return
	}
	revisions, err := s.store.ListAgentRoleDefinitionRevisions(r.Context(), id)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	sourceFound := false
	for _, revision := range revisions {
		if revision.Revision == request.SourceRevision {
			sourceFound = true
			if !s.requireDefinitionScopeAccess(w, r, definitionTargetForAgentRole(revision.AgentRoleDefinition)) {
				return
			}
			break
		}
	}
	if !sourceFound {
		writeError(w, http.StatusNotFound, "definition not found")
		return
	}
	restored, err := s.store.RestoreAgentRoleDefinitionCAS(r.Context(), chi.URLParam(r, "id"), request.SourceRevision, request.ExpectedRevision)
	if err != nil {
		writeDefinitionStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, restored)
}

func (s *Server) getEffectivePrompt(w http.ResponseWriter, r *http.Request) {
	agentID, ok := effectiveAgentID(w, r)
	if !ok {
		return
	}
	if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: agentID}) {
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	preview, err := s.runner.EffectivePromptSnapshot(r.Context(), agentID)
	if err != nil {
		writeEffectiveProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) listEffectiveChildRoles(w http.ResponseWriter, r *http.Request) {
	agentID, ok := effectiveAgentID(w, r)
	if !ok {
		return
	}
	if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: agentID}) {
		return
	}
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	preview, err := s.runner.EffectiveChildRoles(r.Context(), agentID)
	if err != nil {
		writeEffectiveProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func effectiveAgentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	query := r.URL.Query()
	if len(query) != 1 || len(query["agentId"]) != 1 {
		writeError(w, http.StatusBadRequest, "agentId query parameter is required")
		return "", false
	}
	agentID := strings.TrimSpace(query.Get("agentId"))
	if agentID == "" || len(agentID) > 128 || agentID != query.Get("agentId") {
		writeError(w, http.StatusBadRequest, "agentId query parameter is invalid")
		return "", false
	}
	return agentID, true
}

func writeEffectiveProfileError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || db.IsNotFound(err) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "effective profile resolution failed")
}

func (s *Server) mountProfileDefinitionRoutes(router chi.Router) {
	router.Get("/api/agent-role-definitions", s.listAgentRoleDefinitions)
	router.With(s.fullRemoteAccessGuard).Post("/api/agent-role-definitions", s.createAgentRoleDefinition)
	router.Get("/api/agent-role-definitions/{id}", s.getAgentRoleDefinition)
	router.With(s.fullRemoteAccessGuard).Put("/api/agent-role-definitions/{id}", s.updateAgentRoleDefinition)
	router.With(s.fullRemoteAccessGuard).Delete("/api/agent-role-definitions/{id}", s.deleteAgentRoleDefinition)
	router.Get("/api/agent-role-definitions/{id}/revisions", s.listAgentRoleDefinitionRevisions)
	router.With(s.fullRemoteAccessGuard).Post("/api/agent-role-definitions/{id}/restore", s.restoreAgentRoleDefinition)

	router.Get("/api/prompt-definitions", s.listPromptDefinitions)
	router.With(s.fullRemoteAccessGuard).Post("/api/prompt-definitions", s.createPromptDefinition)
	router.Get("/api/prompt-definitions/{id}", s.getPromptDefinition)
	router.With(s.fullRemoteAccessGuard).Put("/api/prompt-definitions/{id}", s.updatePromptDefinition)
	router.With(s.fullRemoteAccessGuard).Delete("/api/prompt-definitions/{id}", s.deletePromptDefinition)
	router.Get("/api/prompt-definitions/{id}/revisions", s.listPromptDefinitionRevisions)
	router.With(s.fullRemoteAccessGuard).Post("/api/prompt-definitions/{id}/restore", s.restorePromptDefinition)

	router.Get("/api/effective-prompt", s.getEffectivePrompt)
	router.Get("/api/effective-child-roles", s.listEffectiveChildRoles)
}

func definitionTargetForAgentRole(value db.AgentRoleDefinition) db.DefinitionScopeTarget {
	return db.DefinitionScopeTarget{Scope: value.Scope, ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID}
}

func definitionScopeFromRequest(r *http.Request) (db.DefinitionScopeTarget, error) {
	query := r.URL.Query()
	target := db.DefinitionScopeTarget{Scope: query.Get("scope"), ProjectID: query.Get("projectId"), WorkspaceID: query.Get("workspaceId")}
	if strings.TrimSpace(target.Scope) == "" {
		return db.DefinitionScopeTarget{}, errors.New("scope query parameter is required")
	}
	// Store validation remains authoritative; this rejects ambiguous duplicate
	// query parameters before reaching persistence.
	for _, name := range []string{"scope", "projectId", "workspaceId"} {
		if len(query[name]) > 1 {
			return db.DefinitionScopeTarget{}, fmt.Errorf("%s query parameter must appear once", name)
		}
	}
	return target, nil
}

func decodeProfileDefinitionJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, profileDefinitionRequestBytes))
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			return errors.New("request body exceeds size limit")
		}
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8")
	}
	if err := rejectDuplicateProfileDefinitionJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func writeDefinitionStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "definition not found")
	case db.IsConflict(err):
		writeError(w, http.StatusConflict, "definition revision conflict")
	case isDefinitionValidationError(err):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "definition operation failed")
	}
}

func isDefinitionValidationError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"invalid", " must ", " must be", " is required", " is not allowed", "cannot ", "override", "baseprompt",
		"key ", "key is", "displayname", "summary", "definition ", "definition key", "role definition", "scope ",
		"projectid", "workspaceid", "layer ", "content ", "expected revision", "source and expected",
		"cannot restore", "base role", "tool allowlist", "permission ceiling",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func rejectDuplicateProfileDefinitionJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key must be a string")
				}
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	return walk()
}
