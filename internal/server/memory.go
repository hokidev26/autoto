package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

type createMemoryRequest struct {
	Content  string   `json:"content"`
	Keywords []string `json:"keywords"`
	Pinned   bool     `json:"pinned"`
	// AgentID scopes the memory to one conversation. Empty keeps it global.
	AgentID string `json:"agentId"`
}

type updateMemoryRequest struct {
	Content  *string   `json:"content"`
	Keywords *[]string `json:"keywords"`
	Pinned   *bool     `json:"pinned"`
	Archived *bool     `json:"archived"`
}

func (s *Server) listMemories(w http.ResponseWriter, r *http.Request) {
	includeArchived := false
	if r.URL.Query().Has("includeArchived") {
		var err error
		includeArchived, err = strconv.ParseBool(r.URL.Query().Get("includeArchived"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "includeArchived must be a boolean")
			return
		}
	}
	scope := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope")))
	agentID := strings.TrimSpace(r.URL.Query().Get("agentId"))
	// Reading one conversation's memories is reading that conversation, so it
	// goes through the same access check as the conversation itself.
	if scope == db.MemoryScopeAgent && !s.requireAgentAccess(w, r, agentID) {
		return
	}
	memories, err := s.store.ListMemories(r.Context(), db.MemoryListOptions{
		Query:           r.URL.Query().Get("q"),
		IncludeArchived: includeArchived,
		Scope:           scope,
		AgentID:         agentID,
	})
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	memories, ok := s.filterMemoriesForRequest(w, r, memories)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, memories)
}

func (s *Server) createMemory(w http.ResponseWriter, r *http.Request) {
	var req createMemoryRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	agentID := strings.TrimSpace(req.AgentID)
	if agentID != "" && !s.requireAgentAccess(w, r, agentID) {
		return
	}
	if agentID == "" && !s.requireGlobalMemoryAccess(w, r) {
		return
	}
	created, err := s.store.CreateMemory(r.Context(), db.Memory{
		AgentID:  agentID,
		Content:  req.Content,
		Keywords: req.Keywords,
		Pinned:   req.Pinned,
	})
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getMemory(w http.ResponseWriter, r *http.Request) {
	memory, err := s.store.GetMemory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	if !s.requireMemoryAccess(w, r, memory) {
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (s *Server) updateMemory(w http.ResponseWriter, r *http.Request) {
	memory, err := s.store.GetMemory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	if !s.requireMemoryAccess(w, r, memory) {
		return
	}
	var req updateMemoryRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if req.Content != nil {
		memory.Content = *req.Content
	}
	if req.Keywords != nil {
		memory.Keywords = *req.Keywords
	}
	if req.Pinned != nil {
		memory.Pinned = *req.Pinned
	}
	if req.Archived != nil {
		if *req.Archived {
			memory.ArchivedAt = db.Now()
		} else {
			memory.ArchivedAt = ""
		}
	}
	updated, err := s.store.UpdateMemory(r.Context(), memory)
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteMemory(w http.ResponseWriter, r *http.Request) {
	memory, err := s.store.GetMemory(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	if !s.requireMemoryAccess(w, r, memory) {
		return
	}
	if err := s.store.DeleteMemory(r.Context(), memory.ID); err != nil {
		s.writeRequestError(w, r, statusFromMemoryError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) requireMemoryAccess(w http.ResponseWriter, r *http.Request, memory db.Memory) bool {
	if agentID := strings.TrimSpace(memory.AgentID); agentID != "" {
		return s.requireAgentAccess(w, r, agentID)
	}
	return s.requireGlobalMemoryAccess(w, r)
}

func (s *Server) requireGlobalMemoryAccess(w http.ResponseWriter, r *http.Request) bool {
	if s.store == nil {
		return true
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !hasUsers {
		return true
	}
	_, ok := s.requireUser(w, r)
	return ok
}

func (s *Server) filterMemoriesForRequest(w http.ResponseWriter, r *http.Request, memories []db.Memory) ([]db.Memory, bool) {
	if s.store == nil {
		return memories, true
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	if !hasUsers {
		return memories, true
	}
	user, ok := s.requireUser(w, r)
	if !ok {
		return nil, false
	}
	filtered := make([]db.Memory, 0, len(memories))
	for _, memory := range memories {
		if strings.TrimSpace(memory.AgentID) == "" {
			filtered = append(filtered, memory)
			continue
		}
		allowed, accessErr := s.store.CanAccessAgent(r.Context(), user.ID, memory.AgentID)
		if accessErr != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, accessErr)
			return nil, false
		}
		if allowed {
			filtered = append(filtered, memory)
		}
	}
	return filtered, true
}

func statusFromMemoryError(err error) int {
	status := statusFromError(err)
	if status != http.StatusInternalServerError {
		return status
	}
	message := err.Error()
	if strings.HasPrefix(message, "memory content") ||
		strings.HasPrefix(message, "invalid memory content") ||
		strings.HasPrefix(message, "memory keyword") ||
		strings.HasPrefix(message, "invalid memory keyword") ||
		strings.HasPrefix(message, "memory keywords") ||
		strings.HasPrefix(message, "memory id") ||
		strings.HasPrefix(message, "invalid memory id") ||
		strings.HasPrefix(message, "invalid memory agent id") ||
		strings.HasPrefix(message, "memory scope") ||
		strings.HasPrefix(message, "invalid memory scope") ||
		strings.HasPrefix(message, "invalid memory archived_at") {
		return http.StatusBadRequest
	}
	return status
}
