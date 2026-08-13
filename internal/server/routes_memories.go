package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountMemoryRoutes(r chi.Router) {
	r.Route("/api/memories", func(r chi.Router) {
		r.Get("/", s.listMemories)
		r.Post("/", s.createMemory)
		r.Get("/{id}", s.getMemory)
		r.Patch("/{id}", s.updateMemory)
		r.Delete("/{id}", s.deleteMemory)
	})
}
