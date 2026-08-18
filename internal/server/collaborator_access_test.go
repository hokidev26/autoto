package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCollaboratorWorkspaceAllowlist(t *testing.T) {
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/ui/app.js"},
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/auth/me"},
		{http.MethodGet, "/api/preferences"},
		{http.MethodPatch, "/api/preferences"},
		{http.MethodPost, "/api/preferences/import-local"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodGet, "/api/projects"},
		{http.MethodGet, "/api/projects/p1"},
		{http.MethodPost, "/api/projects/p1/conversations"},
		{http.MethodGet, "/api/agents"},
		{http.MethodGet, "/api/agents/a1/messages"},
		{http.MethodPost, "/api/agents/a1/messages"},
		{http.MethodPatch, "/api/agents/a1/model"},
		{http.MethodPatch, "/api/agents/a1/navigation-state"},
		{http.MethodGet, "/api/models"},
		{http.MethodGet, "/api/workflow/preferences"},
		{http.MethodGet, "/api/agents/a1/background-tasks"},
		{http.MethodGet, "/api/background-tasks/t1"},
		{http.MethodGet, "/api/background-tasks/t1/output"},
		{http.MethodPost, "/api/background-tasks/t1/wait"},
		{http.MethodPost, "/api/background-tasks/t1/cancel"},
		{http.MethodGet, "/api/navigation"},
		{http.MethodGet, "/ws/agent"},
		{http.MethodGet, "/api/v2/agents/a1/skills/effective"},
		{http.MethodGet, "/api/agents/a1/git/status"},
	}
	denied := []struct{ method, path string }{
		{http.MethodPost, "/api/projects"},
		{http.MethodPost, "/api/conversations"},
		{http.MethodPatch, "/api/projects/p1/navigation-state"},
		{http.MethodPost, "/api/projects/p1/init-git"},
		{http.MethodDelete, "/api/projects/p1"},
		{http.MethodGet, "/api/settings"},
		{http.MethodPut, "/api/workflow/preferences"},
		{http.MethodGet, "/api/providers"},
		{http.MethodPut, "/api/providers/openai/config"},
		{http.MethodGet, "/api/runtime/summary"},
		{http.MethodGet, "/api/fs/directories"},
		{http.MethodPost, "/api/fs/mkdir"},
		{http.MethodGet, "/api/users/accounts"},
		{http.MethodPost, "/api/users/collaborators"},
		{http.MethodPost, "/api/users/operators"},
		{http.MethodPatch, "/api/users/u1/role"},
		{http.MethodGet, "/api/security/remote-access"},
		{http.MethodGet, "/ws/terminal"},
		{http.MethodPost, "/api/background-tasks"},
		{http.MethodDelete, "/api/background-tasks/t1"},
		{http.MethodPatch, "/api/agents/a1/cwd"},
		{http.MethodGet, "/api/licenses"},
		{http.MethodGet, "/api/mcp/servers"},
	}
	for _, item := range allowed {
		req := httptest.NewRequest(item.method, item.path, nil)
		if !collaboratorWorkspaceAllowed(req) {
			t.Fatalf("expected allow %s %s", item.method, item.path)
		}
	}
	for _, item := range denied {
		req := httptest.NewRequest(item.method, item.path, nil)
		if collaboratorWorkspaceAllowed(req) {
			t.Fatalf("expected deny %s %s", item.method, item.path)
		}
	}
}
