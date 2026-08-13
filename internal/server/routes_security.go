package server

import "github.com/go-chi/chi/v5"

// mountSecurityRoutes registers the remote-access settings, the temporary and
// named Cloudflare tunnel controls, and the remote-access password endpoint.
// Each mutating handler enforces its own remote-security policy, so the group
// needs no shared middleware.
func (s *Server) mountSecurityRoutes(r chi.Router) {
	r.Get("/api/security/remote-access", s.getRemoteAccessSettings)
	r.Get("/api/security/remote-access/tunnel", s.getTemporaryTunnel)
	r.Post(temporaryTunnelInstallPath, s.installTemporaryTunnel)
	r.Post("/api/security/remote-access/tunnel", s.startTemporaryTunnel)
	r.Delete("/api/security/remote-access/tunnel", s.stopTemporaryTunnel)
	r.Patch("/api/security/remote-access/policy", s.updateRemoteAccessPolicy)
	r.Get("/api/security/named-tunnel", s.getNamedTunnelSettings)
	r.Put("/api/security/named-tunnel/remote-access", s.updateRemoteAccessNamedTunnel)
	r.Put("/api/security/named-tunnel/shared-api", s.updateSharedAPINamedTunnel)
	r.Put("/api/security/remote-access/password", s.updateRemoteAccessPassword)
}
