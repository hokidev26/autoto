package server

import (
	"net/http"
	"strings"

	"autoto/internal/config"
)

type contextSettingsRequest struct {
	CompactKeepTurns *int                                  `json:"compactKeepTurns"`
	MaxPrunePercent  *int                                  `json:"maxPrunePercent"`
	MinPrunePercent  *int                                  `json:"minPrunePercent"`
	Standard         *config.ContextManagementWindowConfig `json:"standard"`
	Large            *config.ContextManagementWindowConfig `json:"large"`
}

func (s *Server) updateRuntimeContextSettings(w http.ResponseWriter, r *http.Request) {
	var request contextSettingsRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	if request.CompactKeepTurns == nil && request.MaxPrunePercent == nil && request.MinPrunePercent == nil && request.Standard == nil && request.Large == nil {
		writeError(w, http.StatusBadRequest, "at least one context setting is required")
		return
	}
	s.configMutationMu.Lock()
	defer s.configMutationMu.Unlock()
	s.cfgMu.RLock()
	updated := s.cfg
	path := s.configPath
	s.cfgMu.RUnlock()
	settings := updated.ContextManagement.Normalized()
	if request.CompactKeepTurns != nil {
		settings.CompactKeepTurns = *request.CompactKeepTurns
	}
	if request.MaxPrunePercent != nil {
		settings.MaxPrunePercent = *request.MaxPrunePercent
	}
	if request.MinPrunePercent != nil {
		settings.MinPrunePercent = *request.MinPrunePercent
	}
	if request.Standard != nil {
		settings.Standard = *request.Standard
	}
	if request.Large != nil {
		settings.Large = *request.Large
	}
	if err := settings.Validate(); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	updated.ContextManagement = settings
	path = effectiveConfigPath(updated, path)
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusInternalServerError, "context settings could not be persisted")
		return
	}
	if err := config.Save(path, updated); err != nil {
		writeError(w, http.StatusInternalServerError, "context settings could not be persisted")
		return
	}
	s.cfgMu.Lock()
	s.cfg = updated
	s.cfgMu.Unlock()
	if s.runner != nil {
		s.runner.SetContextManagementConfig(settings)
	}
	writeJSON(w, http.StatusOK, map[string]any{"contextManagement": settings, "persisted": true})
}
