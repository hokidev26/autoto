package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountWorkflowRoutes(r chi.Router) {
	r.Route("/api/workflow", func(r chi.Router) {
		r.Get("/preferences", s.getWorkflowPreferences)
		r.With(s.fullRemoteAccessGuard).Put("/preferences", s.updateWorkflowPreferences)
		r.Get("/tool-permissions", s.listToolPermissionRules)
		r.With(s.fullRemoteAccessGuard).Post("/tool-permissions", s.createToolPermissionRule)
		r.With(s.fullRemoteAccessGuard).Patch("/tool-permissions/{id}", s.updateToolPermissionRule)
		r.With(s.fullRemoteAccessGuard).Delete("/tool-permissions/{id}", s.deleteToolPermissionRule)
	})
}
