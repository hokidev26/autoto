package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/preview"
)

type startPreviewRequest struct {
	ProfileID string `json:"profileId"`
	Port      *int   `json:"port,omitempty"`
}

func (s *Server) detectPreview(w http.ResponseWriter, r *http.Request) {
	manager, workspace, ok := s.previewRequestContext(w, r, true)
	if !ok {
		return
	}
	profiles, err := manager.Detect(workspace)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, preview.ErrManagerClosed) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, "preview detection failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

func (s *Server) startPreview(w http.ResponseWriter, r *http.Request) {
	manager, workspace, ok := s.previewRequestContext(w, r, true)
	if !ok {
		return
	}
	var request startPreviewRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	if request.ProfileID == "" {
		writeError(w, http.StatusBadRequest, "profileId is required")
		return
	}

	status, err := manager.StartPreview(r.Context(), chi.URLParam(r, "id"), workspace, preview.StartOptions{
		ProfileID: request.ProfileID,
		Port:      request.Port,
	})
	if err == nil {
		writeJSON(w, http.StatusOK, status)
		return
	}
	switch {
	case errors.Is(err, preview.ErrStaleProfile):
		writeJSON(w, http.StatusConflict, status)
	case errors.Is(err, preview.ErrInvalidPort):
		writeJSON(w, http.StatusBadRequest, status)
	case errors.Is(err, preview.ErrManagerClosed):
		writeJSON(w, http.StatusServiceUnavailable, status)
	default:
		writeJSON(w, http.StatusInternalServerError, status)
	}
}

func (s *Server) stopPreview(w http.ResponseWriter, r *http.Request) {
	manager, _, ok := s.previewRequestContext(w, r, false)
	if !ok {
		return
	}
	status, err := manager.StopPreview(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, status)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) previewStatus(w http.ResponseWriter, r *http.Request) {
	manager, _, ok := s.previewRequestContext(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, manager.Status(chi.URLParam(r, "id")))
}

func (s *Server) previewLogs(w http.ResponseWriter, r *http.Request) {
	manager, _, ok := s.previewRequestContext(w, r, false)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, manager.Logs(chi.URLParam(r, "id")))
}

func (s *Server) previewRequestContext(w http.ResponseWriter, r *http.Request, requireWorkspace bool) (*preview.Manager, string, bool) {
	if s.previewManager == nil {
		writeError(w, http.StatusServiceUnavailable, "preview service is not initialized")
		return nil, "", false
	}
	if s.store == nil {
		writeError(w, http.StatusServiceUnavailable, "agent store is not initialized")
		return nil, "", false
	}
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || db.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "agent not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load agent")
		}
		return nil, "", false
	}
	if err := requireLocalExecutionAgent(agent); err != nil {
		s.writeExecutionGuardError(w, r, err)
		return nil, "", false
	}
	if requireWorkspace && strings.TrimSpace(agent.CWD) == "" {
		writeError(w, http.StatusConflict, "agent workspace is not configured")
		return nil, "", false
	}
	return s.previewManager, agent.CWD, true
}
