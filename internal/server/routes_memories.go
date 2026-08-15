package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountMemoryRoutes(r chi.Router) {
	r.Route("/api/memories", func(r chi.Router) {
		r.Get("/", s.listMemories)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createMemory)
		r.Get("/{id}", s.getMemory)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateMemory)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteMemory)
	})
}
