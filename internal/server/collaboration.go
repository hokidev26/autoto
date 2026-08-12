package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	agentpkg "autoto/internal/agent"
	"autoto/internal/db"
)

type putMessageDraftRequest struct {
	ContentText *string `json:"contentText"`
	Text        *string `json:"text"`
	Version     *int64  `json:"version"`
}

func (s *Server) getMessageDraft(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	draft, err := s.store.GetMessageDraft(r.Context(), user.ID, chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "message draft not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (s *Server) putMessageDraft(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	var req putMessageDraftRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Version == nil {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}
	content := req.ContentText
	if content == nil {
		content = req.Text
	}
	if content == nil {
		writeError(w, http.StatusBadRequest, "contentText is required")
		return
	}
	draft, err := s.store.PutMessageDraft(r.Context(), db.MessageDraft{UserID: user.ID, AgentID: chi.URLParam(r, "id"), ContentText: *content}, *req.Version)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (s *Server) deleteMessageDraft(w http.ResponseWriter, r *http.Request) {
	user, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMessageDraft(r.Context(), user.ID, chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type correctionRequest struct {
	Text              string   `json:"text"`
	KeepAttachmentIDs []string `json:"keepAttachmentIds"`
	Context           string   `json:"context,omitempty"`
}

func (s *Server) createCorrection(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	var text, messageContext string
	var keepAttachmentIDs []string
	var attachments []db.Attachment
	var err error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		text, keepAttachmentIDs, attachments, messageContext, err = parseMultipartCorrection(w, r)
	} else {
		var req correctionRequest
		err = decodeJSON(r, &req)
		text, keepAttachmentIDs, messageContext = req.Text, req.KeepAttachmentIDs, req.Context
	}
	if err != nil {
		var uploadErr attachmentUploadError
		if errors.As(err, &uploadErr) {
			writeError(w, uploadErr.Status, uploadErr.Message)
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	agentID := chi.URLParam(r, "id")
	_, runSource, err := s.messageRunBoundary(r.Context(), agentID, messageContext)
	if err != nil {
		writeError(w, statusFromMessageBoundaryError(err), err.Error())
		return
	}
	createdBy := ""
	if user, ok, err := s.currentUser(r); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if ok {
		createdBy = user.ID
	}
	if err := s.enforceRemotePermissionCap(r, agentID); err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	message, err := s.runner.SubmitCorrectionWithSource(r.Context(), agentID, chi.URLParam(r, "messageId"), text, createdBy, keepAttachmentIDs, runSource, attachments...)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, message)
}

// rerunMessage re-runs an existing user message. It carries no body beyond the
// optional context, because a rerun changes nothing about the message -- that
// is the whole point of it not being a correction.
func (s *Server) rerunMessage(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	var req struct {
		Context string `json:"context"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	agentID := chi.URLParam(r, "id")
	_, runSource, err := s.messageRunBoundary(r.Context(), agentID, req.Context)
	if err != nil {
		writeError(w, statusFromMessageBoundaryError(err), err.Error())
		return
	}
	if err := s.enforceRemotePermissionCap(r, agentID); err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	run, err := s.runner.SubmitRerun(r.Context(), agentID, chi.URLParam(r, "messageId"), runSource)
	if err != nil {
		writeError(w, statusFromError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// rollbackConversationToMessage retires every turn after the target message so
// the conversation resumes from that point. Soft like a correction: the retired
// rows stay readable in the transcript but leave the model's view.
func (s *Server) rollbackConversationToMessage(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	superseded, err := s.runner.RollbackConversationToMessage(r.Context(), agentID, chi.URLParam(r, "messageId"))
	if err != nil {
		writeError(w, statusFromMessageOperationError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolledBack": true, "supersededCount": superseded})
}

type forkConversationRequest struct {
	Title string `json:"title"`
}

// forkConversationFromMessage copies the transcript up to the target message
// into a new conversation beside the original (same workline, same directory).
func (s *Server) forkConversationFromMessage(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	var req forkConversationRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	fork, err := s.runner.ForkConversationFromMessage(r.Context(), agentID, chi.URLParam(r, "messageId"), req.Title)
	if err != nil {
		writeError(w, statusFromMessageOperationError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": fork})
}

// deleteConversationMessage removes one message permanently, together with the
// hidden tool-result rows produced by its tool calls.
func (s *Server) deleteConversationMessage(w http.ResponseWriter, r *http.Request) {
	if s.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "agent runner is not initialized")
		return
	}
	agentID := strings.TrimSpace(chi.URLParam(r, "id"))
	if !s.requireAgentAccess(w, r, agentID) {
		return
	}
	deleted, err := s.runner.DeleteConversationMessage(r.Context(), agentID, chi.URLParam(r, "messageId"))
	if err != nil {
		writeError(w, statusFromMessageOperationError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "deletedMessageIds": deleted})
}

func statusFromMessageOperationError(err error) int {
	if errors.Is(err, agentpkg.ErrAgentBusy) {
		return http.StatusConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return statusFromError(err)
}

func parseMultipartCorrection(w http.ResponseWriter, r *http.Request) (string, []string, []db.Attachment, string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageUploadBytes)
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	if err := r.ParseMultipartForm(multipartMemoryBytes); err != nil {
		return "", nil, nil, "", attachmentUploadError{Status: http.StatusBadRequest, Message: fmt.Sprintf("附件上传解析失败：%v", err)}
	}
	var keepAttachmentIDs []string
	if raw := strings.TrimSpace(r.FormValue("keepAttachmentIds")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &keepAttachmentIDs); err != nil {
			return "", nil, nil, "", errors.New("keepAttachmentIds must be a JSON array")
		}
	}
	files := multipartFiles(r.MultipartForm)
	attachments := make([]db.Attachment, 0, len(files))
	var total int64
	for _, header := range files {
		if header == nil {
			continue
		}
		if header.Size > maxAttachmentBytes {
			return "", nil, nil, "", attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: fmt.Sprintf("%s 超过 10 MB 限制", sanitizeAttachmentFilename(header.Filename))}
		}
		total += header.Size
		if total > maxMessageUploadBytes {
			return "", nil, nil, "", attachmentUploadError{Status: http.StatusRequestEntityTooLarge, Message: "单条消息附件总大小超过 25 MB"}
		}
		attachment, err := buildAttachmentFromPart(header)
		if err != nil {
			return "", nil, nil, "", err
		}
		attachments = append(attachments, attachment)
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" && len(keepAttachmentIDs) == 0 && len(attachments) == 0 {
		return "", nil, nil, "", attachmentUploadError{Status: http.StatusBadRequest, Message: "text, files, or keepAttachmentIds is required"}
	}
	return text, keepAttachmentIDs, attachments, strings.TrimSpace(r.FormValue("context")), nil
}
