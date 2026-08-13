package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/skills"
)

type skillV2Request struct {
	Name              *string `json:"name"`
	Command           *string `json:"command"`
	Description       *string `json:"description"`
	Prompt            *string `json:"prompt"`
	Source            *string `json:"source"`
	Scope             *string `json:"scope"`
	ProjectID         *string `json:"projectId"`
	WorklineID        *string `json:"worklineId"`
	Enabled           *bool   `json:"enabled"`
	AcknowledgeRisk   bool    `json:"acknowledgeRisk"`
	ExpectedUpdatedAt *string `json:"expectedUpdatedAt"`
}

type skillImportV2Request struct {
	Content         string  `json:"content"`
	Scope           *string `json:"scope"`
	ProjectID       *string `json:"projectId"`
	WorklineID      *string `json:"worklineId"`
	Enabled         *bool   `json:"enabled"`
	AcknowledgeRisk bool    `json:"acknowledgeRisk"`
}

type skillDeleteV2Request struct {
	ExpectedUpdatedAt string `json:"expectedUpdatedAt"`
}

type skillRestoreRequest struct {
	RevisionNo              int64  `json:"revisionNo"`
	ExpectedUpdatedAt       string `json:"expectedUpdatedAt"`
	AcknowledgeRisk         bool   `json:"acknowledgeRisk"`
	AcknowledgedContentHash string `json:"acknowledgedContentHash"`
}

func skillScopeTargetFromRequest(scope, projectID, worklineID *string) db.SkillScopeTarget {
	target := db.SkillScopeTarget{Scope: db.SkillScopeGlobal}
	if scope != nil {
		target.Scope = strings.TrimSpace(*scope)
	}
	if projectID != nil {
		target.ProjectID = strings.TrimSpace(*projectID)
	}
	if worklineID != nil {
		target.WorklineID = strings.TrimSpace(*worklineID)
	}
	return target
}

func skillScopeTargetFromQuery(r *http.Request) db.SkillScopeTarget {
	return db.SkillScopeTarget{
		Scope:      strings.TrimSpace(r.URL.Query().Get("scope")),
		ProjectID:  strings.TrimSpace(r.URL.Query().Get("projectId")),
		WorklineID: strings.TrimSpace(r.URL.Query().Get("worklineId")),
	}
}

func hasSkillScopeTargetQuery(r *http.Request) bool {
	query := r.URL.Query()
	return query.Has("scope") || query.Has("projectId") || query.Has("worklineId")
}

func skillScopeTargetMatchesSkill(target db.SkillScopeTarget, skill db.Skill) bool {
	scope := strings.TrimSpace(target.Scope)
	if scope == "" {
		scope = db.SkillScopeGlobal
	}
	return scope == skill.Scope && strings.TrimSpace(target.ProjectID) == skill.ProjectID && strings.TrimSpace(target.WorklineID) == skill.WorklineID
}

func (s *Server) requireSkillScopeAccess(w http.ResponseWriter, r *http.Request, target db.SkillScopeTarget) bool {
	scope := strings.TrimSpace(target.Scope)
	if scope == "" || scope == db.SkillScopeGlobal {
		return true
	}
	switch scope {
	case db.SkillScopeProject:
		projectID := strings.TrimSpace(target.ProjectID)
		if projectID == "" {
			return true // Let the store return the canonical invalid-scope error.
		}
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: projectID})
	case db.SkillScopeWorkspace:
		worklineID := strings.TrimSpace(target.WorklineID)
		if worklineID == "" {
			return true // Let the store return the canonical invalid-scope error.
		}
		return s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessWorkline, id: worklineID})
	default:
		return true
	}
}

func (s *Server) requireSkillAccess(w http.ResponseWriter, r *http.Request, skill db.Skill) bool {
	return s.requireSkillScopeAccess(w, r, db.SkillScopeTarget{Scope: skill.Scope, ProjectID: skill.ProjectID, WorklineID: skill.WorklineID})
}

func validateSkillRequestContext(r *http.Request, skill db.Skill) error {
	if !hasSkillScopeTargetQuery(r) {
		return nil
	}
	if !skillScopeTargetMatchesSkill(skillScopeTargetFromQuery(r), skill) {
		return db.ErrConflict
	}
	return nil
}

func skillPageParams(r *http.Request) (int, string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return limit, strings.TrimSpace(r.URL.Query().Get("cursor"))
}

func (s *Server) listSkillsV2(w http.ResponseWriter, r *http.Request) {
	target := skillScopeTargetFromQuery(r)
	if !s.requireSkillScopeAccess(w, r, target) {
		return
	}
	limit, cursor := skillPageParams(r)
	page, err := s.store.ListSkillsPage(r.Context(), target, limit, cursor)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) createSkillV2(w http.ResponseWriter, r *http.Request) {
	var req skillV2Request
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	if req.Prompt == nil {
		writeError(w, http.StatusBadRequest, "skill prompt is required")
		return
	}
	target := skillScopeTargetFromRequest(req.Scope, req.ProjectID, req.WorklineID)
	if !s.requireSkillScopeAccess(w, r, target) {
		return
	}
	source := "manual"
	if req.Source != nil {
		source = strings.TrimSpace(*req.Source)
		if source != "manual" && source != "local_migration" {
			writeError(w, http.StatusBadRequest, "source must be manual or local_migration")
			return
		}
	}
	enabled := req.Enabled != nil && *req.Enabled
	parsed, err := skills.Normalize(skills.Skill{Name: valueOrEmpty(req.Name), Command: valueOrEmpty(req.Command), Description: valueOrEmpty(req.Description), Prompt: *req.Prompt})
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	record, err := scannedSkillRecord(parsed, source, enabled, req.AcknowledgeRisk)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	record.Scope, record.ProjectID, record.WorklineID = target.Scope, target.ProjectID, target.WorklineID
	created, err := s.store.CreateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("created", created)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getSkillV2(w http.ResponseWriter, r *http.Request) {
	skill, err := s.store.GetSkill(r.Context(), chi.URLParam(r, "id"))
	if err == nil {
		err = validateSkillRequestContext(r, skill)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, skill) {
		return
	}
	writeJSON(w, http.StatusOK, skill)
}

func (s *Server) updateSkillV2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetSkill(r.Context(), id)
	if err == nil {
		err = validateSkillRequestContext(r, existing)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, existing) {
		return
	}
	var req skillV2Request
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	if req.Source != nil {
		writeError(w, http.StatusBadRequest, "skill source cannot be changed")
		return
	}
	if req.ExpectedUpdatedAt == nil || strings.TrimSpace(*req.ExpectedUpdatedAt) == "" {
		writeError(w, http.StatusBadRequest, "expectedUpdatedAt is required")
		return
	}
	candidate := skills.Skill{Name: existing.Name, Command: existing.Command, Description: existing.Description, Prompt: existing.Prompt}
	if req.Name != nil {
		candidate.Name = *req.Name
	}
	if req.Command != nil {
		candidate.Command = *req.Command
	}
	if req.Description != nil {
		candidate.Description = *req.Description
	}
	if req.Prompt != nil {
		candidate.Prompt = *req.Prompt
	}
	candidate, err = skills.Normalize(candidate)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	record, err := scannedSkillRecord(candidate, existing.Source, enabled, req.AcknowledgeRisk)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	record.ID = existing.ID
	record.Scope, record.ProjectID, record.WorklineID = existing.Scope, existing.ProjectID, existing.WorklineID
	if req.Scope != nil || req.ProjectID != nil || req.WorklineID != nil {
		target := skillScopeTargetFromRequest(req.Scope, req.ProjectID, req.WorklineID)
		if !s.requireSkillScopeAccess(w, r, target) {
			return
		}
		record.Scope, record.ProjectID, record.WorklineID = target.Scope, target.ProjectID, target.WorklineID
	}
	record.UpdatedAt = strings.TrimSpace(*req.ExpectedUpdatedAt)
	updated, err := s.store.UpdateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("updated", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteSkillV2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetSkill(r.Context(), id)
	if err == nil {
		err = validateSkillRequestContext(r, current)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, current) {
		return
	}
	var req skillDeleteV2Request
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	deleted, err := s.store.DeleteSkillCAS(r.Context(), id, req.ExpectedUpdatedAt, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("deleted", deleted)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "skill": deleted})
}

func (s *Server) importSkillV2(w http.ResponseWriter, r *http.Request) {
	var req skillImportV2Request
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	target := skillScopeTargetFromRequest(req.Scope, req.ProjectID, req.WorklineID)
	if !s.requireSkillScopeAccess(w, r, target) {
		return
	}
	parsed, err := skills.ParseMarkdown(req.Content)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := req.Enabled != nil && *req.Enabled
	record, err := scannedSkillRecord(parsed, "skill_md", enabled, req.AcknowledgeRisk)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	record.Scope, record.ProjectID, record.WorklineID = target.Scope, target.ProjectID, target.WorklineID
	created, err := s.store.CreateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("imported", created)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listSkillRevisionsV2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	skill, err := s.store.GetSkillIncludingDeleted(r.Context(), id)
	if err == nil {
		err = validateSkillRequestContext(r, skill)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, skill) {
		return
	}
	limit, cursor := skillPageParams(r)
	page, err := s.store.ListSkillRevisionsPage(r.Context(), id, limit, cursor)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) getSkillRevisionV2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetSkillIncludingDeleted(r.Context(), id)
	if err == nil {
		err = validateSkillRequestContext(r, current)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, current) {
		return
	}
	revisionNo, err := strconv.ParseInt(chi.URLParam(r, "revisionNo"), 10, 64)
	if err != nil || revisionNo < 1 {
		writeError(w, http.StatusBadRequest, "invalid revision number")
		return
	}
	revision, err := s.store.GetSkillRevision(r.Context(), id, revisionNo)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, revision)
}

func (s *Server) restoreSkillV2(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	current, err := s.store.GetSkillIncludingDeleted(r.Context(), id)
	if err == nil {
		err = validateSkillRequestContext(r, current)
	}
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if !s.requireSkillAccess(w, r, current) {
		return
	}
	var req skillRestoreRequest
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	if pathRevision := strings.TrimSpace(chi.URLParam(r, "revisionNo")); pathRevision != "" {
		parsed, err := strconv.ParseInt(pathRevision, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid revision number")
			return
		}
		req.RevisionNo = parsed
	}
	if req.RevisionNo < 1 || strings.TrimSpace(req.ExpectedUpdatedAt) == "" {
		writeError(w, http.StatusBadRequest, "revisionNo and expectedUpdatedAt are required")
		return
	}
	restored, err := s.store.RestoreSkillAs(r.Context(), id, req.RevisionNo, req.ExpectedUpdatedAt, req.AcknowledgeRisk, req.AcknowledgedContentHash, "api_request")
	if err != nil {
		var challenge *db.SkillRestoreReviewRequiredError
		if errors.As(err, &challenge) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":          err.Error(),
				"code":           "skill_restore_review_required",
				"scanVerdict":    challenge.ScanVerdict,
				"scanFindings":   challenge.ScanFindings,
				"contentHash":    challenge.ContentHash,
				"scannerVersion": challenge.ScannerVersion,
			})
			return
		}
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("restored", restored)
	writeJSON(w, http.StatusOK, restored)
}

func (s *Server) listEffectiveSkillsV2(w http.ResponseWriter, r *http.Request) {
	agentID := chi.URLParam(r, "id")
	if hasSkillScopeTargetQuery(r) {
		if err := s.store.ValidateEffectiveSkillContext(r.Context(), agentID, skillScopeTargetFromQuery(r)); err != nil {
			s.writeRequestError(w, r, statusFromSkillError(err), err)
			return
		}
	}
	limit, cursor := skillPageParams(r)
	page, err := s.store.ListEffectiveSkillsPage(r.Context(), agentID, limit, cursor)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func marshalSkillJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
