package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountBackgroundTaskRoutes(r chi.Router) {
	r.Get("/api/background-tasks/{taskId}", s.getBackgroundTask)
	r.Get("/api/background-tasks/{taskId}/output", s.backgroundTaskOutput)
	r.Post("/api/background-tasks/{taskId}/wait", s.waitBackgroundTask)
}
