package server

import "github.com/go-chi/chi/v5"

func (s *Server) mountFSRoutes(r chi.Router) {
	r.Route("/api/fs", func(r chi.Router) {
		r.Get("/browse", s.fsBrowse)
		r.Get("/directories", s.fsDirectories)
		r.Post("/native-directory", s.fsNativeDirectory)
		r.Get("/preview", s.fsPreview)
		r.Post("/mkdir", s.fsMkdir)
	})
}
