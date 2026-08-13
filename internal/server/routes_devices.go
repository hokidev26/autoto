package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountDeviceRoutes(r chi.Router) {
	r.Get("/api/devices", s.listDevices)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions", s.createDeviceAction)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions/{id}/approve", s.approveDeviceAction)
	r.With(s.fullRemoteAccessGuard).Post("/api/device-actions/{id}/deny", s.denyDeviceAction)
}
