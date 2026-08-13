package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	agentpkg "autoto/internal/agent"
)

type agentStreamStateResponse struct {
	Protocol            int                      `json:"protocol"`
	Stream              agentpkg.StreamWatermark `json:"stream"`
	ExecutionGeneration int64                    `json:"executionGeneration"`
}

func (s *Server) getAgentStreamState(w http.ResponseWriter, r *http.Request) {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "agent event hub is not initialized")
		return
	}
	agentID := strings.TrimSpace(chiURLParam(r, "id"))
	generation, err := s.store.MaxExecutionGeneration(r.Context(), agentID)
	if err != nil {
		s.writeRequestError(w, r, statusFromError(err), err)
		return
	}
	watermark := s.hub.Watermark(agentID)
	etag := fmt.Sprintf(`W/"%s:%d:%d"`, watermark.StreamSession, watermark.LatestSequence, generation)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, agentStreamStateResponse{
		Protocol:            agentpkg.ProtocolVersion,
		Stream:              watermark,
		ExecutionGeneration: generation,
	})
}

// chiURLParam is a small indirection that keeps stream recovery tests focused.
var chiURLParam = func(r *http.Request, key string) string {
	return chi.URLParam(r, key)
}
