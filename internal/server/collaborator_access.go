package server

import (
	"net/http"
	"strings"
)

const collaboratorAccessDenied = "collaborator access is limited to granted projects and personal settings"

func (s *Server) collaboratorHostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok, err := s.currentUser(r)
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		if !ok || !userIsCollaborator(user) {
			next.ServeHTTP(w, r)
			return
		}
		if !collaboratorWorkspaceAllowed(r) {
			writeError(w, http.StatusForbidden, collaboratorAccessDenied)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func collaboratorWorkspaceAllowed(r *http.Request) bool {
	method := r.Method
	if method == http.MethodOptions {
		return true
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}
	if guestStaticPath(path) {
		return method == http.MethodGet || method == http.MethodHead
	}
	if method == http.MethodPost && path == "/api/auth/logout" {
		return true
	}
	if method == http.MethodPatch && path == "/api/preferences" {
		return true
	}
	if method == http.MethodPost && path == "/api/preferences/import-local" {
		return true
	}
	if method == http.MethodGet || method == http.MethodHead {
		switch path {
		case "/api/health", "/api/auth/status", "/api/auth/me", "/api/preferences",
			"/api/navigation", "/api/projects", "/api/themes", "/api/appearance/background",
			"/api/models", "/api/overview", "/api/system/metrics", "/api/users",
			"/api/workflow/preferences",
			"/ws/agent", "/ws/narrator":
			return true
		}
		if strings.HasPrefix(path, "/api/themes/") || strings.HasPrefix(path, "/api/appearance/") {
			return true
		}
		if guestProjectObservePath(path) || guestAgentObservePath(path) {
			return true
		}
		if collaboratorAgentCollectionPath(path) {
			return true
		}
	}
	if collaboratorProjectWriteAllowed(method, path) {
		return true
	}
	if collaboratorAgentWriteAllowed(method, path) {
		return true
	}
	if collaboratorBackgroundTaskAllowed(method, path) {
		return true
	}
	return false
}

func collaboratorBackgroundTaskAllowed(method, path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/background-tasks/")
	if !ok || rest == "" {
		return false
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return false
	}
	switch len(parts) {
	case 1:
		return method == http.MethodGet || method == http.MethodHead
	case 2:
		switch parts[1] {
		case "output":
			return method == http.MethodGet || method == http.MethodHead
		case "wait", "cancel":
			return method == http.MethodPost
		}
	}
	return false
}

func collaboratorAgentCollectionPath(path string) bool {
	switch path {
	case "/api/agents", "/api/narrators", "/api/v2/agents":
		return true
	}
	return false
}

func collaboratorProjectWriteAllowed(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if rest, ok := strings.CutPrefix(path, "/api/projects/"); ok {
		parts := strings.Split(rest, "/")
		return len(parts) == 2 && parts[0] != "" && parts[1] == "conversations"
	}
	if rest, ok := strings.CutPrefix(path, "/api/worklines/"); ok {
		parts := strings.Split(rest, "/")
		return len(parts) == 2 && parts[0] != "" && parts[1] == "conversations"
	}
	return false
}

func collaboratorAgentWriteAllowed(method, path string) bool {
	if method == http.MethodConnect || method == http.MethodTrace {
		return false
	}
	rest := ""
	switch {
	case strings.HasPrefix(path, "/api/agents/"):
		rest = strings.TrimPrefix(path, "/api/agents/")
	case strings.HasPrefix(path, "/api/narrators/"):
		rest = strings.TrimPrefix(path, "/api/narrators/")
	case strings.HasPrefix(path, "/api/v2/agents/"):
		rest = strings.TrimPrefix(path, "/api/v2/agents/")
	default:
		return false
	}
	if rest == "" {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[1] == "cwd" {
		return false
	}
	return true
}
