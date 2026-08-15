package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountProjectRoutes(r chi.Router) {
	r.Route("/api/projects", func(r chi.Router) {
		r.Get("/", s.listProjects)
		r.Post("/", s.createProject)
		r.Patch("/{id}/navigation-state", s.patchProjectNavigationState)
		r.With(s.fullRemoteAccessGuard).Delete("/{id}", s.deleteArchivedProject)
		r.Get("/{id}", s.getProject)
		r.Post("/{id}/conversations", s.createProjectConversation)
		r.Get("/{id}/worklines", s.listProjectWorklines)
		r.Get("/{id}/chapters", s.listProjectWorklines)
		r.With(s.fullRemoteAccessGuard).Post("/{id}/init-git", s.initProjectGit)
	})
}
