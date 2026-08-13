package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountAuditRoutes(r chi.Router) {
	r.Get("/api/audit/events", s.listAuditEvents)
}
