package server

import (
	"net/http"
	"strings"

	"autoto/internal/db"
	"autoto/internal/hooks"
)

func (s *Server) requireInstalledUserAccess(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.store == nil {
		return true
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access check failed")
		return false
	}
	if !hasUsers {
		return true
	}
	if _, ok, err := s.currentUser(r); err != nil {
		writeError(w, http.StatusInternalServerError, "access check failed")
		return false
	} else if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return false
	}
	return true
}

func (s *Server) requireDefinitionScopeAccess(w http.ResponseWriter, r *http.Request, target db.DefinitionScopeTarget) bool {
	target.Scope = strings.TrimSpace(strings.ToLower(target.Scope))
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.WorkspaceID = strings.TrimSpace(target.WorkspaceID)
	switch target.Scope {
	case db.DefinitionScopeGlobal:
		if target.ProjectID != "" || target.WorkspaceID != "" {
			writeError(w, http.StatusBadRequest, "invalid definition scope")
			return false
		}
		return s.requireInstalledUserAccess(w, r)
	case db.DefinitionScopeProject:
		if target.ProjectID == "" || target.WorkspaceID != "" {
			writeError(w, http.StatusBadRequest, "invalid definition scope")
			return false
		}
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: target.ProjectID})
	case db.DefinitionScopeWorkspace:
		if target.ProjectID == "" || target.WorkspaceID == "" {
			writeError(w, http.StatusBadRequest, "invalid definition scope")
			return false
		}
		if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: target.ProjectID}) {
			return false
		}
		workline, err := s.store.GetWorkline(r.Context(), target.WorkspaceID)
		if err != nil || workline.ProjectID != target.ProjectID {
			writeError(w, http.StatusNotFound, "resource not found")
			return false
		}
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessWorkline, id: target.WorkspaceID})
	default:
		writeError(w, http.StatusBadRequest, "invalid definition scope")
		return false
	}
}

func (s *Server) requireToolAvailabilityTargetAccess(w http.ResponseWriter, r *http.Request, target db.ToolAvailabilityTarget) bool {
	return s.requireDefinitionScopeAccess(w, r, db.DefinitionScopeTarget{
		Scope: target.Scope, ProjectID: target.ProjectID, WorkspaceID: target.WorkspaceID,
	})
}

func (s *Server) requireToolAvailabilityRuleAccess(w http.ResponseWriter, r *http.Request, rule db.ToolAvailabilityRule) bool {
	return s.requireToolAvailabilityTargetAccess(w, r, db.ToolAvailabilityTarget{
		Scope: rule.Scope, ProjectID: rule.ProjectID, WorkspaceID: rule.WorkspaceID,
	})
}

func (s *Server) requireLifecycleHookScopeAccess(w http.ResponseWriter, r *http.Request, scope hooks.Scope) bool {
	switch scope.Kind {
	case hooks.ScopeGlobal:
		return s.requireInstalledUserAccess(w, r)
	case hooks.ScopeProject:
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: scope.ID})
	case hooks.ScopeAgent:
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessAgent, id: scope.ID})
	default:
		writeError(w, http.StatusNotFound, "resource not found")
		return false
	}
}

func (s *Server) filterAccessibleLifecycleHooks(w http.ResponseWriter, r *http.Request, items []hooks.Hook) ([]hooks.Hook, bool) {
	if s == nil || s.store == nil {
		return items, true
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access check failed")
		return nil, false
	}
	if !hasUsers {
		return items, true
	}
	user, ok, err := s.currentUser(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "access check failed")
		return nil, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return nil, false
	}
	filtered := make([]hooks.Hook, 0, len(items))
	for _, item := range items {
		allowed := false
		switch item.Scope.Kind {
		case hooks.ScopeGlobal:
			allowed = true
		case hooks.ScopeProject:
			allowed, err = s.store.CanAccessProject(r.Context(), user.ID, item.Scope.ID)
		case hooks.ScopeAgent:
			allowed, err = s.store.CanAccessAgent(r.Context(), user.ID, item.Scope.ID)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "access check failed")
			return nil, false
		}
		if allowed {
			filtered = append(filtered, item)
		}
	}
	return filtered, true
}
