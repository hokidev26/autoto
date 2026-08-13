package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountMCPServerRoutes(r chi.Router) {
	r.Route("/api/mcp/servers", func(r chi.Router) {
		r.Get("/", s.listMCPServers)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createMCPServer)
		r.Get("/{id}", s.getMCPServer)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateMCPServer)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteMCPServer)
		r.Get("/{id}/tools", s.listMCPServerTools)
	})
}
