package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountSkillV2Routes(r chi.Router) {
	r.Route("/api/v2/skills", func(r chi.Router) {
		r.Get("/", s.listSkillsV2)
		r.With(s.localSkillSourceGuard).Get("/sources", s.listSkillSources)
		r.With(s.localSkillSourceGuard).Post("/sources/import", s.importSkillSource)
		r.With(s.fullRemoteAccessGuard).Post("/", s.createSkillV2)
		r.With(s.fullRemoteAccessGuard).Post("/import/preview", s.previewSkillImport)
		r.With(s.fullRemoteAccessGuard).Post("/import", s.importSkillV2)
		r.Get("/{id}", s.getSkillV2)
		r.With(s.fullRemoteAccessGuard).Patch("/{id}", s.updateSkillV2)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteSkillV2)
		r.Get("/{id}/revisions", s.listSkillRevisionsV2)
		r.Get("/{id}/revisions/{revisionNo}", s.getSkillRevisionV2)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/restore", s.restoreSkillV2)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/revisions/{revisionNo}/restore", s.restoreSkillV2)
	})
}
