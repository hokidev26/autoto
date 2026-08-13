package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountNotificationRoutes(r chi.Router) {
	r.Route("/api/notifications", func(r chi.Router) {
		r.Get("/settings", s.getNotificationSettings)
		r.With(s.fullRemoteAccessGuard).Put("/settings", s.updateNotificationSettings)
		r.With(s.fullRemoteAccessGuard).Post("/test", s.testNotification)
		r.Get("/deliveries", s.listNotificationDeliveries)
		r.With(s.fullRemoteAccessGuard).Post("/deliveries/{id}/retry", s.retryNotificationDelivery)
	})
}
