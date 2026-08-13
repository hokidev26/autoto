package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountSkillRoutes(r chi.Router) {
	r.Route("/api/skills", func(r chi.Router) {
		r.Get("/", s.listSkills)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createSkill)
		r.With(s.fullRemoteAccessGuard).Post("/import/preview", s.previewSkillImport)
		r.With(s.fullRemoteAccessGuard).Post("/import", s.importSkill)
		r.Get("/{id}", s.getSkill)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateSkill)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteSkill)
	})
}
