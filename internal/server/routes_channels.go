package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountChannelRoutes(r chi.Router) {
	r.With(s.fullRemoteAccessGuard).Post("/api/channels/pairing-codes", s.createChannelPairingCode)
	r.Get("/api/channels/pairings", s.listChannelPairings)
	r.With(s.fullRemoteAccessGuard).Post("/api/channels/pairings/{id}/revoke", s.revokeChannelPairing)
}
