package server

import (
	"net/http"
	"strings"

	"autoto/internal/db"
)

const guestAccessDenied = "guest access is limited to conversations and profile"

func userIsGuest(user db.User) bool {
	return strings.EqualFold(strings.TrimSpace(user.Role), "guest")
}

func userIsAdmin(user db.User) bool {
	return strings.EqualFold(strings.TrimSpace(user.Role), "admin")
}

func (s *Server) guestObserveGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok, err := s.currentUser(r)
		if err != nil {
			s.writeRequestError(w, r, http.StatusInternalServerError, err)
			return
		}
		if !ok || !userIsGuest(user) {
			next.ServeHTTP(w, r)
			return
		}
		if !guestObserveAllowed(r) {
			writeError(w, http.StatusForbidden, guestAccessDenied)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func guestObserveAllowed(r *http.Request) bool {
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
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	switch path {
	case "/api/health", "/api/auth/status", "/api/auth/me", "/api/preferences",
		"/api/navigation", "/api/projects", "/api/themes", "/api/appearance/background",
		"/ws/agent", "/ws/narrator":
		return true
	}
	return guestProjectObservePath(path) || guestAgentObservePath(path)
}

func guestStaticPath(path string) bool {
	return path != "/api" && !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/")
}

func guestProjectObservePath(path string) bool {
	if rest, ok := strings.CutPrefix(path, "/api/projects/"); ok {
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && parts[0] != "" {
			return true
		}
		return len(parts) == 2 && (parts[1] == "worklines" || parts[1] == "chapters")
	}
	for _, prefix := range []string{"/api/worklines/", "/api/chapters/"} {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && parts[0] != "" {
			return true
		}
		if len(parts) == 2 && (parts[1] == "agents" || parts[1] == "narrators") {
			return true
		}
	}
	return false
}

func guestAgentObservePath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/agents/")
	if !ok {
		rest, ok = strings.CutPrefix(path, "/api/narrators/")
	}
	if !ok || rest == "" {
		return false
	}
	parts := strings.Split(rest, "/")
	if parts[0] == "" {
		return false
	}
	switch len(parts) {
	case 1:
		return true
	case 2:
		switch parts[1] {
		case "live-snapshot", "messages", "draft", "queue", "runs", "plans":
			return true
		}
	case 3:
		switch {
		case parts[1] == "runs" && parts[2] == "active":
			return true
		case parts[1] == "runs":
			return true
		case parts[1] == "plans":
			return true
		case parts[1] == "tool-calls" && parts[2] == "pending":
			return true
		case parts[1] == "tool-calls":
			return true
		}
	case 4:
		if parts[1] == "runs" && parts[3] == "tool-calls" {
			return true
		}
	case 5:
		if parts[1] == "messages" && (parts[3] == "attachments" || parts[3] == "generated-images") {
			return true
		}
	}
	return false
}
