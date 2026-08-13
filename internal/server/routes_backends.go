package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountBackendRoutes(r chi.Router) {
	r.Route("/api/backends", func(r chi.Router) {
		r.Get("/", s.listBackends)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createBackend)
		r.Get("/{id}", s.getBackend)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateBackend)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteBackend)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/activate", s.activateBackend)
		r.Get("/{id}/health", s.backendHealth)
	})
}
