package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountScheduleRoutes(r chi.Router) {
	r.Route("/api/schedules", func(r chi.Router) {
		r.Use(s.loginIfUsersExistGuard)
		r.Get("/", s.listSchedules)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createSchedule)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateSchedule)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteSchedule)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/run", s.runSchedule)
	})
}
