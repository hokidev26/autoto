package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountMonitoringRoutes(r chi.Router) {
	r.Get("/api/monitoring/snapshot", s.monitoringSnapshot)
}
