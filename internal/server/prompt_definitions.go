package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

type promptDefinitionRequest struct {
	ExpectedRevision *int64                    `json:"expectedRevision,omitempty"`
	Scope            *db.DefinitionScopeTarget `json:"scope,omitempty"`
	Key              string                    `json:"key"`
	DisplayName      string                    `json:"displayName"`
	Summary          string                    `json:"summary,omitempty"`
	Layer            string                    `json:"layer"`
	Content          string                    `json:"content"`
}

func (s *Server) listPromptDefinitions(w http.ResponseWriter, r *http.Request) {
	target, err := definitionScopeFromRequest(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, target) {
		return
	}
	items, err := s.store.ListPromptDefinitions(r.Context(), target)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createPromptDefinition(w http.ResponseWriter, r *http.Request) {
	var request promptDefinitionRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
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
	created, err := s.store.CreatePromptDefinition(r.Context(), db.PromptDefinitionInput{
		Scope: *request.Scope, Key: request.Key, DisplayName: request.DisplayName, Summary: request.Summary, Layer: request.Layer, Content: request.Content,
	})
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getPromptDefinition(w http.ResponseWriter, r *http.Request) {
	value, err := s.store.GetPromptDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(value)) {
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) updatePromptDefinition(w http.ResponseWriter, r *http.Request) {
	var request promptDefinitionRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be positive")
		return
	}
	current, err := s.store.GetPromptDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(current)) {
		return
	}
	input := db.PromptDefinitionInput{Key: request.Key, DisplayName: request.DisplayName, Summary: request.Summary, Layer: request.Layer, Content: request.Content}
	if request.Scope != nil {
		if !s.requireDefinitionScopeAccess(w, r, *request.Scope) {
			return
		}
		input.Scope = *request.Scope
	}
	updated, err := s.store.UpdatePromptDefinitionCAS(r.Context(), chi.URLParam(r, "id"), *request.ExpectedRevision, input)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deletePromptDefinition(w http.ResponseWriter, r *http.Request) {
	var request definitionDeleteRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if request.ExpectedRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision must be positive")
		return
	}
	current, err := s.store.GetPromptDefinition(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(current)) {
		return
	}
	deleted, err := s.store.DeletePromptDefinitionCAS(r.Context(), chi.URLParam(r, "id"), request.ExpectedRevision)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted.PromptDefinitionSummary)
}

func (s *Server) listPromptDefinitionRevisions(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.GetPromptDefinitionIncludingDeleted(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(current)) {
		return
	}
	items, err := s.store.ListPromptDefinitionRevisions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func definitionTargetForPrompt(value db.PromptDefinition) db.DefinitionScopeTarget {
	return db.DefinitionScopeTarget{Scope: value.Scope, ProjectID: value.ProjectID, WorkspaceID: value.WorkspaceID}
}

func (s *Server) restorePromptDefinition(w http.ResponseWriter, r *http.Request) {
	var request definitionRestoreRequest
	if err := decodeProfileDefinitionJSON(w, r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if request.ExpectedRevision < 1 || request.SourceRevision < 1 {
		writeError(w, http.StatusBadRequest, "expectedRevision and sourceRevision must be positive")
		return
	}
	id := chi.URLParam(r, "id")
	current, err := s.store.GetPromptDefinitionIncludingDeleted(r.Context(), id)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(current)) {
		return
	}
	revisions, err := s.store.ListPromptDefinitionRevisions(r.Context(), id)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	sourceFound := false
	for _, revision := range revisions {
		if revision.Revision == request.SourceRevision {
			sourceFound = true
			if !s.requireDefinitionScopeAccess(w, r, definitionTargetForPrompt(revision.PromptDefinition)) {
				return
			}
			break
		}
	}
	if !sourceFound {
		writeError(w, http.StatusNotFound, "definition not found")
		return
	}
	restored, err := s.store.RestorePromptDefinitionCAS(r.Context(), chi.URLParam(r, "id"), request.SourceRevision, request.ExpectedRevision)
	if err != nil {
		s.writeDefinitionStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, restored)
}
