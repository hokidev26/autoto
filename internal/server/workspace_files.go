package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"autoto/internal/workspacefs"
)

// A JSON string can expand each content byte to a six-byte Unicode escape.
// The decoded file content is still capped at workspacefs.MaxFileBytes.
const workspaceFileRequestBytes = 6*workspacefs.MaxFileBytes + 64*1024

type workspaceFileWriteRequest struct {
	Path            string `json:"path"`
	Content         string `json:"content"`
	ExpectedModTime string `json:"expectedModTime"`
}

func (s *Server) workspaceTree(w http.ResponseWriter, r *http.Request) {
	fs, ok := s.agentWorkspace(w, r)
	if !ok {
		return
	}
	tree, err := fs.Tree(r.URL.Query().Get("path"))
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

func (s *Server) workspaceFile(w http.ResponseWriter, r *http.Request) {
	fs, ok := s.agentWorkspace(w, r)
	if !ok {
		return
	}
	file, err := fs.ReadFile(r.URL.Query().Get("path"))
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (s *Server) updateWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeAgentWorkspaceLookupError(w, err)
		return
	}
	if err := requireLocalExecutionAgent(agent); err != nil {
		s.writeExecutionGuardError(w, r, err)
		return
	}
	if agent.PermissionMode == "readOnly" {
		writeError(w, http.StatusForbidden, "workspace is read-only")
		return
	}
	fs, err := workspacefs.New(agent.CWD)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, workspaceFileRequestBytes)
	defer r.Body.Close()
	var request workspaceFileWriteRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "workspace file is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid workspace file request")
		return
	}
	result, err := fs.WriteFile(request.Path, []byte(request.Content), request.ExpectedModTime)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) agentWorkspace(w http.ResponseWriter, r *http.Request) (*workspacefs.FS, bool) {
	agent, err := s.store.GetAgent(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeAgentWorkspaceLookupError(w, err)
		return nil, false
	}
	if err := requireLocalExecutionAgent(agent); err != nil {
		s.writeExecutionGuardError(w, r, err)
		return nil, false
	}
	fs, err := workspacefs.New(agent.CWD)
	if err != nil {
		writeWorkspaceError(w, err)
		return nil, false
	}
	return fs, true
}

func writeAgentWorkspaceLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to load agent workspace")
}

func writeWorkspaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspacefs.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "workspace file is too large")
	case errors.Is(err, workspacefs.ErrConflict):
		writeError(w, http.StatusConflict, "workspace file changed since it was read")
	case errors.Is(err, workspacefs.ErrForbidden):
		writeError(w, http.StatusForbidden, "workspace path is not accessible")
	case errors.Is(err, workspacefs.ErrNotFound):
		writeError(w, http.StatusNotFound, "workspace path not found")
	case errors.Is(err, workspacefs.ErrBinary):
		writeError(w, http.StatusBadRequest, "workspace file must be text")
	case errors.Is(err, workspacefs.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid workspace path")
	default:
		writeError(w, http.StatusInternalServerError, "workspace operation failed")
	}
}
