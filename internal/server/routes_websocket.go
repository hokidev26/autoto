package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountWebsocketRoutes(r chi.Router) {
	r.Get("/ws/agent", s.agentWS)
	r.Get("/ws/narrator", s.agentWS)
	r.Get("/ws/terminal", s.terminalWS)
}
