package server

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
)

func (s *Server) mountAutomationToolCatalogRoutes(router chi.Router) {
	router.Get("/api/optional-tools/automation", s.listAutomationToolCatalog)
	router.Post("/api/optional-tools/automation/{id}/install", s.installAutomationToolCatalogItem)
	router.With(s.fullRemoteAccessGuard).Post("/api/optional-tools/automation/{id}/configure", s.configureAutomationToolCatalogItem)
}

func (s *Server) listAutomationToolCatalog(w http.ResponseWriter, r *http.Request) {
	catalog := s.automationToolCatalogSnapshot()
	if catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
		return
	}
	items, err := catalog.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) installAutomationToolCatalogItem(w http.ResponseWriter, r *http.Request) {
	if allowed, message := s.remoteSecurityMutationAllowed(r, ""); !allowed {
		writeError(w, http.StatusForbidden, message)
		return
	}
	catalog := s.automationToolCatalogSnapshot()
	if catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
		return
	}
	item, err := catalog.Install(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, automationToolCatalogErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) configureAutomationToolCatalogItem(w http.ResponseWriter, r *http.Request) {
	catalog := s.automationToolCatalogSnapshot()
	if catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "optional automation tool catalog is unavailable")
		return
	}
	item, err := catalog.Configure(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, automationToolCatalogErrorStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func automationToolCatalogErrorStatus(err error) int {
	if db.IsNotFound(err) {
		return http.StatusNotFound
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unsupported"),
		strings.Contains(message, "not managed"),
		strings.Contains(message, "cannot be configured"),
		strings.Contains(message, "not installed correctly"):
		return http.StatusConflict
	case strings.Contains(message, "is required"):
		return http.StatusFailedDependency
	default:
		return http.StatusInternalServerError
	}
}
