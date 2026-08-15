package server

import "github.com/go-chi/chi/v5"

// mountAuthRoutes registers local account authentication, the user directory,
// and per-account preference endpoints. Extracted from Routes() so the router
// setup reads as a table of domains rather than one long block.
func (s *Server) mountAuthRoutes(r chi.Router) {
	r.Get("/api/auth/status", s.authStatus)
	r.Post("/api/auth/register", s.register)
	r.Post("/api/auth/login", s.login)
	r.Post("/api/auth/logout", s.logout)
	r.Get("/api/auth/me", s.me)
	r.Get("/api/users", s.listUsers)
	r.Get("/api/users/accounts", s.listUserAccounts)
	r.Post("/api/users/guests", s.createGuestAccount)
	r.Post("/api/users/{id}/access-keys", s.issueGuestAccessKey)
	r.Delete("/api/users/{id}/access-keys/{keyId}", s.revokeGuestAccessKey)
	r.Put("/api/users/{id}/memberships", s.replaceGuestMemberships)
	r.Delete("/api/users/{id}", s.deleteUserAccount)
	r.Get("/api/preferences", s.getAccountPreferences)
	r.Patch("/api/preferences", s.patchAccountPreferences)
	r.Post("/api/preferences/import-local", s.importLocalAccountPreferences)
}
