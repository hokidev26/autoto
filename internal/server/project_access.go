package server

import (
	"net/http"
	"strings"

	"autoto/internal/db"
)

type projectAccessTarget struct {
	kind string
	id   string
}

const (
	projectAccessCollection = "collection"
	projectAccessProject    = "project"
	projectAccessWorkline   = "workline"
	projectAccessAgent      = "agent"
)

// projectAccessGuard leaves a fresh local installation unchanged. Once at least
// one user exists, it requires a valid session for every project-scoped route
// and hides inaccessible resources behind a 404 response.
func (s *Server) projectAccessGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, scoped := projectAccessTargetForRequest(r)
		if !scoped || s.store == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !s.requireProjectResourceAccess(w, r, target) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireProjectResourceAccess applies membership for installed users and the
// restricted remote project's filesystem boundary to a single resource. It is
// also used by handlers whose collection route has no ID in its URL.
func (s *Server) requireProjectResourceAccess(w http.ResponseWriter, r *http.Request, target projectAccessTarget) bool {
	if s.store == nil {
		return true
	}
	if !s.requireRemoteResourceScope(w, r, target) {
		return false
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !hasUsers || target.kind == projectAccessCollection {
		return true
	}
	user, ok, err := s.currentUser(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return false
	}
	var allowed bool
	switch target.kind {
	case projectAccessProject:
		allowed, err = s.store.CanAccessProject(r.Context(), user.ID, target.id)
	case projectAccessWorkline:
		allowed, err = s.store.CanAccessWorkline(r.Context(), user.ID, target.id)
	case projectAccessAgent:
		allowed, err = s.store.CanAccessAgent(r.Context(), user.ID, target.id)
	}
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "resource not found")
		return false
	}
	return true
}

func (s *Server) requireRemoteResourceScope(w http.ResponseWriter, r *http.Request, target projectAccessTarget) bool {
	if s.capabilitiesForRequest(r).FilesystemScope != "project" || target.kind == projectAccessCollection {
		return true
	}
	path := ""
	switch target.kind {
	case projectAccessProject:
		project, err := s.store.GetProject(r.Context(), target.id)
		if err != nil {
			s.writeStoreError(w, r, err)
			return false
		}
		path = project.GitPath
	case projectAccessWorkline:
		workline, err := s.store.GetWorkline(r.Context(), target.id)
		if err != nil {
			s.writeStoreError(w, r, err)
			return false
		}
		path = workline.WorktreePath
		if strings.TrimSpace(path) == "" {
			project, projectErr := s.store.GetProject(r.Context(), workline.ProjectID)
			if projectErr != nil {
				s.writeStoreError(w, r, projectErr)
				return false
			}
			path = project.GitPath
		}
	case projectAccessAgent:
		agent, err := s.store.GetAgent(r.Context(), target.id)
		if err != nil {
			s.writeStoreError(w, r, err)
			return false
		}
		path = agent.CWD
	default:
		return true
	}
	if !s.filesystemPathWithinProjectRoot(path) {
		writeError(w, http.StatusNotFound, "resource not found")
		return false
	}
	return true
}

func projectAccessTargetForRequest(r *http.Request) (projectAccessTarget, bool) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 {
		return projectAccessTarget{}, false
	}
	if parts[0] == "ws" && (parts[1] == "agent" || parts[1] == "narrator" || parts[1] == "terminal") {
		agentID := strings.TrimSpace(r.URL.Query().Get("agentId"))
		if agentID == "" && parts[1] != "terminal" {
			// Keep existing agent/narrator websocket clients working while moving
			// new callers to the explicit agentId query parameter.
			agentID = strings.TrimSpace(r.URL.Query().Get("id"))
		}
		return projectAccessTarget{kind: projectAccessAgent, id: agentID}, true
	}
	if parts[0] != "api" {
		return projectAccessTarget{}, false
	}
	switch parts[1] {
	case "projects":
		if len(parts) == 2 {
			return projectAccessTarget{kind: projectAccessCollection}, true
		}
		return projectAccessTarget{kind: projectAccessProject, id: parts[2]}, true
	case "worklines", "chapters":
		if len(parts) >= 3 {
			return projectAccessTarget{kind: projectAccessWorkline, id: parts[2]}, true
		}
	case "agents", "narrators":
		if len(parts) >= 3 {
			return projectAccessTarget{kind: projectAccessAgent, id: parts[2]}, true
		}
	case "v2":
		if len(parts) >= 4 && (parts[2] == "agents" || parts[2] == "narrators") {
			return projectAccessTarget{kind: projectAccessAgent, id: parts[3]}, true
		}
	}
	return projectAccessTarget{}, false
}

func (s *Server) filterAgentsByMembership(w http.ResponseWriter, r *http.Request, agents []db.Agent) ([]db.Agent, bool) {
	if s.store == nil {
		return agents, true
	}
	hasUsers, err := s.store.HasUsers(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	if !hasUsers {
		return agents, true
	}
	user, ok, err := s.currentUser(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return nil, false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return nil, false
	}
	filtered := make([]db.Agent, 0, len(agents))
	for _, agent := range agents {
		allowed, accessErr := s.store.CanAccessAgent(r.Context(), user.ID, agent.ID)
		if accessErr != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, accessErr)
			return nil, false
		}
		if allowed {
			filtered = append(filtered, agent)
		}
	}
	return filtered, true
}

func (s *Server) requireAgentAccess(w http.ResponseWriter, r *http.Request, agentID string) bool {
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
	user, ok, err := s.currentUser(r)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "login required")
		return false
	}
	allowed, err := s.store.CanAccessAgent(r.Context(), user.ID, agentID)
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return false
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "resource not found")
		return false
	}
	return true
}
