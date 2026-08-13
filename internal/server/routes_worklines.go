package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountWorklineRoutes(r chi.Router) {
	r.Get("/api/worklines/{id}", s.getWorkline)
	r.Post("/api/worklines/{id}/fork", s.forkWorkline)
	r.Get("/api/worklines/{id}/merge-check", s.worklineMergeCheck)
	r.Post("/api/worklines/{id}/merge", s.worklineMerge)
	r.Post("/api/worklines/{id}/unmerge", s.worklineUnmerge)
	r.Post("/api/worklines/{id}/cleanup", s.worklineCleanup)
	r.Get("/api/worklines/{id}/agents", s.listWorklineAgents)
	r.Get("/api/chapters/{id}", s.getWorkline)
	r.Post("/api/chapters/{id}/fork", s.forkWorkline)
	r.Get("/api/chapters/{id}/merge-check", s.worklineMergeCheck)
	r.Post("/api/chapters/{id}/merge", s.worklineMerge)
	r.Post("/api/chapters/{id}/unmerge", s.worklineUnmerge)
	r.Post("/api/chapters/{id}/cleanup", s.worklineCleanup)
	r.Get("/api/chapters/{id}/narrators", s.listWorklineAgents)
}
