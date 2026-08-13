package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountNetworkDiagnosticRoutes(r chi.Router) {
	r.Get("/api/network/diagnostics", s.getNetworkDiagnostics)
	r.Post("/api/network/diagnostics/probe", s.runNetworkDiagnostic)
}
