package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountClientIdentityRoutes(r chi.Router) {
	r.Get("/api/client/identity", s.clientIdentity)
	r.With(s.fullRemoteAccessGuard).Post("/api/client/identity/rotate", s.rotateClientIdentity)
}
