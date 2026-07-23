package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"autoto/internal/appearanceassets"
	"github.com/go-chi/chi/v5"
)

const appearanceBackgroundMultipartOverhead = 1 << 20

type appearanceBackgroundResponse struct {
	Background *appearanceassets.Metadata `json:"background"`
}

func (s *Server) mountAppearanceAssetRoutes(router chi.Router) {
	router.Get("/api/appearance/background", s.getAppearanceBackground)
	router.With(s.sensitiveLocalTokenGuard).Post("/api/appearance/background", s.postAppearanceBackground)
	router.With(s.sensitiveLocalTokenGuard).Delete("/api/appearance/background", s.deleteAppearanceBackground)
	router.Group(func(resources chi.Router) {
		resources.Use(s.themeResourceGuard)
		resources.Get("/appearance/backgrounds/{revision}/{filename}", s.appearanceBackgroundResource)
		resources.Head("/appearance/backgrounds/{revision}/{filename}", s.appearanceBackgroundResource)
	})
}

func (s *Server) getAppearanceBackground(w http.ResponseWriter, _ *http.Request) {
	store, ok := s.requireAppearanceAssetStore(w)
	if !ok {
		return
	}
	background, err := store.Current()
	if errors.Is(err, appearanceassets.ErrNotFound) {
		writeJSON(w, http.StatusOK, appearanceBackgroundResponse{})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("read appearance background: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, appearanceBackgroundResponse{Background: &background})
}

func (s *Server) postAppearanceBackground(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireAppearanceAssetStore(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, appearanceassets.MaxImageBytes+appearanceBackgroundMultipartOverhead)
	if err := r.ParseMultipartForm(appearanceBackgroundMultipartOverhead); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "appearance background is too large")
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse appearance background: %v", err))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "appearance background file is required")
		return
	}
	defer file.Close()
	filename := header.Filename
	if displayName := strings.TrimSpace(r.FormValue("displayName")); displayName != "" {
		filename = displayName
	}
	background, err := store.Import(file, filename)
	if err != nil {
		if errors.Is(err, appearanceassets.ErrInvalid) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, appearanceBackgroundResponse{Background: &background})
}

func (s *Server) deleteAppearanceBackground(w http.ResponseWriter, _ *http.Request) {
	store, ok := s.requireAppearanceAssetStore(w)
	if !ok {
		return
	}
	if err := store.Delete(); err != nil {
		if errors.Is(err, appearanceassets.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("delete appearance background: %v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) appearanceBackgroundResource(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireAppearanceAssetStore(w)
	if !ok {
		return
	}
	filename := strings.TrimPrefix(chi.URLParam(r, "filename"), "/")
	resource, err := store.OpenResource(chi.URLParam(r, "revision"), filename)
	if err != nil {
		if errors.Is(err, appearanceassets.ErrNotFound) {
			writeError(w, http.StatusNotFound, "appearance background not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "appearance background is unavailable")
		return
	}
	defer resource.Close()
	setThemeResourceHeaders(w.Header())
	w.Header().Set("Content-Type", resource.Metadata.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(resource.Metadata.Size, 10))
	http.ServeContent(w, r, resource.Metadata.Filename, resource.ModTime, resource)
}

func (s *Server) requireAppearanceAssetStore(w http.ResponseWriter) (*appearanceassets.Store, bool) {
	if s.appearanceAssets == nil {
		writeError(w, http.StatusServiceUnavailable, "appearance background store is unavailable")
		return nil, false
	}
	return s.appearanceAssets, true
}
