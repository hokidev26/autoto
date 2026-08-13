package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountModelAggregateRoutes(r chi.Router) {
	r.Get("/api/model-aggregates", s.listModelAggregates)
	r.Get("/api/model-aggregates/{name}", s.getModelAggregate)
	r.With(s.fullRemoteAccessGuard).Put("/api/model-aggregates/{name}", s.putModelAggregate)
	r.With(s.fullRemoteAccessGuard).Delete("/api/model-aggregates/{name}", s.deleteModelAggregate)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/model-settings", s.updateRuntimeModelSettings)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/agent-model-settings", s.updateAgentModelSettings)
	r.With(s.fullRemoteAccessGuard).Patch("/api/runtime/context-settings", s.updateRuntimeContextSettings)
}
