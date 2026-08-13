package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountV2AgentRoutes(r chi.Router) {
	r.Get("/api/v2/agents/{id}/live-snapshot", s.getAgentLiveSnapshot)
	r.Get("/api/v2/agents/{id}/stream-state", s.getAgentStreamState)
	r.Get("/api/v2/agents/{id}/skills/effective", s.listEffectiveSkillsV2)
}
