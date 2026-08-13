package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountIntegrationConnectionRoutes(r chi.Router) {
	r.Route("/api/integrations/connections", func(r chi.Router) {
		r.Get("/", s.listIntegrationConnections)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteIntegrationConnection)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/test", s.testIntegrationConnection)
	})
}
