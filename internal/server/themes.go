package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"autoto/internal/themes"
	"github.com/go-chi/chi/v5"
)

const themeImportMultipartOverhead = 1 << 20

type themeListResponse struct {
	Themes []themes.Theme `json:"themes"`
}

type themeMutationResponse struct {
	Theme themes.Theme `json:"theme"`
	// Contrast warnings are advisory: the install already succeeded, but the
	// user learns which token pairs will be hard to read before activating.
	Warnings        []themes.ContrastWarning `json:"warnings,omitempty"`
	Replaced        bool                     `json:"replaced,omitempty"`
	PreviousVersion string                   `json:"previousVersion,omitempty"`
}

type themeManifestResponse struct {
	Manifest themes.Manifest `json:"manifest"`
}

type themeCreateRequest struct {
	Manifest json.RawMessage `json:"manifest"`
	Replace  bool            `json:"replace,omitempty"`
}

func (s *Server) mountThemeRoutes(router chi.Router) {
	router.Get("/api/themes", s.listThemes)
	router.Get("/api/themes/{themeID}/manifest", s.themeManifest)
	router.Get("/api/themes/{themeID}/export", s.exportTheme)
	router.With(s.sensitiveLocalTokenGuard).Post("/api/themes", s.createTheme)
	router.With(s.sensitiveLocalTokenGuard).Post("/api/themes/import", s.importTheme)
	router.With(s.sensitiveLocalTokenGuard).Delete("/api/themes/{themeID}", s.deleteTheme)

	router.Group(func(resources chi.Router) {
		resources.Use(s.themeResourceGuard)
		resources.Get("/themes/{themeID}/{revision}/theme.css", s.themeStylesheet)
		resources.Head("/themes/{themeID}/{revision}/theme.css", s.themeStylesheet)
		resources.Get("/themes/{themeID}/{revision}/*", s.themeResource)
		resources.Head("/themes/{themeID}/{revision}/*", s.themeResource)
	})
}

func (s *Server) listThemes(w http.ResponseWriter, _ *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	items, err := store.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("list themes: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, themeListResponse{Themes: items})
}

func (s *Server) importTheme(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, themes.MaxArchiveBytes+themeImportMultipartOverhead)
	if err := r.ParseMultipartForm(themeImportMultipartOverhead); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "theme archive is too large")
			return
		}
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse theme import: %v", err))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	archive, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "theme archive file is required")
		return
	}
	defer archive.Close()
	replace := false
	if value := strings.TrimSpace(r.FormValue("replace")); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "replace must be true or false")
			return
		}
		replace = parsed
	}
	result, err := store.Import(archive, themes.ImportOptions{Replace: replace})
	if err != nil {
		writeThemeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, themeMutationResponse{
		Theme:           result.Theme,
		Warnings:        themes.AuditContrast(result.Theme.Manifest),
		Replaced:        result.Replaced,
		PreviousVersion: result.PreviousVersion,
	})
}

// createTheme installs a theme from a bare manifest, with no image resources.
// This is the theme editor's save path; the manifest goes through the same
// strict parser and store validation as an imported archive.
func (s *Server) createTheme(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, themes.MaxManifestBytes+themeImportMultipartOverhead)
	var request themeCreateRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("parse theme create request: %v", err))
		return
	}
	manifest, err := themes.ParseManifest(request.Manifest)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := store.InstallManifest(manifest, themes.ImportOptions{Replace: request.Replace})
	if err != nil {
		writeThemeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, themeMutationResponse{
		Theme:           result.Theme,
		Warnings:        themes.AuditContrast(result.Theme.Manifest),
		Replaced:        result.Replaced,
		PreviousVersion: result.PreviousVersion,
	})
}

// themeManifest returns the validated manifest of one installed theme. The
// editor uses it to prefill "start from this theme"; manifests are controlled
// data with no secrets, so it shares the catalog's access level.
func (s *Server) themeManifest(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	theme, err := store.Get(chi.URLParam(r, "themeID"))
	if err != nil {
		writeThemeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, themeManifestResponse{Manifest: theme.Manifest})
}

func (s *Server) exportTheme(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	archive, filename, err := store.ExportArchive(chi.URLParam(r, "themeID"))
	if err != nil {
		writeThemeStoreError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.Itoa(len(archive)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(archive)
}

func (s *Server) deleteTheme(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	if err := store.Delete(chi.URLParam(r, "themeID")); err != nil {
		writeThemeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) themeStylesheet(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	css, err := store.CSSForRevision(chi.URLParam(r, "themeID"), chi.URLParam(r, "revision"))
	if err != nil {
		writeThemeResourceError(w, err)
		return
	}
	setThemeResourceHeaders(w.Header())
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(css)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(css))
	}
}

func (s *Server) themeResource(w http.ResponseWriter, r *http.Request) {
	store, ok := s.requireThemeStore(w)
	if !ok {
		return
	}
	resourcePath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	resource, err := store.OpenResource(chi.URLParam(r, "themeID"), chi.URLParam(r, "revision"), resourcePath)
	if err != nil {
		writeThemeResourceError(w, err)
		return
	}
	defer resource.Close()
	setThemeResourceHeaders(w.Header())
	w.Header().Set("Content-Type", resource.Metadata.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(resource.Metadata.Size, 10))
	http.ServeContent(w, r, resource.Metadata.Path, resource.ModTime, resource)
}

func (s *Server) themeResourceGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isBrowserInitiated(r) && !s.sameOriginRequest(r) {
			writeError(w, http.StatusForbidden, "cross-origin theme resource request denied")
			return
		}
		if s.remoteAccessGateRequired(r) {
			if !s.remoteAccessAuthentication(r).Authenticated {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if s.validHeaderToken(r) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(localTokenCookieName)
		if err == nil && constantTimeEqualToken(cookie.Value, s.localToken) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "local access token required")
	})
}

func (s *Server) requireThemeStore(w http.ResponseWriter) (*themes.Store, bool) {
	if s.themeStore == nil {
		writeError(w, http.StatusServiceUnavailable, "theme store is unavailable")
		return nil, false
	}
	return s.themeStore, true
}

func setThemeResourceHeaders(header http.Header) {
	header.Set("Cache-Control", "private, max-age=31536000, immutable")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("X-Content-Type-Options", "nosniff")
}

func writeThemeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, themes.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, themes.ErrConflict), errors.Is(err, themes.ErrBundledProtected), errors.Is(err, themes.ErrRevisionMismatch):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, themes.ErrInvalidArchive):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeThemeResourceError(w http.ResponseWriter, err error) {
	if errors.Is(err, themes.ErrNotFound) || errors.Is(err, themes.ErrRevisionMismatch) {
		writeError(w, http.StatusNotFound, "theme resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "theme resource is unavailable")
}
