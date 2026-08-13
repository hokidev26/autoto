package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"autoto/internal/db"
	"autoto/internal/skills"
)

// JSON can expand a valid skill substantially when its content contains escaped
// control or HTML-significant characters. Keep a bounded transport allowance;
// ParseMarkdown/Normalize still enforce the decoded 128 KiB content limits.
const maxSkillJSONBody = skills.MaxContentBytes*6 + 16*1024

type skillRequest struct {
	Name              *string `json:"name"`
	Command           *string `json:"command"`
	Description       *string `json:"description"`
	Prompt            *string `json:"prompt"`
	Source            *string `json:"source"`
	Enabled           *bool   `json:"enabled"`
	AcknowledgeRisk   bool    `json:"acknowledgeRisk"`
	ExpectedUpdatedAt *string `json:"expectedUpdatedAt"`
}

type skillImportRequest struct {
	Content         string `json:"content"`
	Enabled         *bool  `json:"enabled"`
	AcknowledgeRisk bool   `json:"acknowledgeRisk"`
}

type skillPreviewResponse struct {
	Name         string           `json:"name"`
	Command      string           `json:"command"`
	Description  string           `json:"description"`
	Prompt       string           `json:"prompt"`
	ContentHash  string           `json:"contentHash"`
	ScanVerdict  string           `json:"scanVerdict"`
	ScanFindings []skills.Finding `json:"scanFindings"`
}

type skillStateError struct{ message string }

type skillContentTypeError struct{ message string }

func (e skillStateError) Error() string       { return e.message }
func (e skillContentTypeError) Error() string { return e.message }

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListSkillSummaries(r.Context())
	if err != nil {
		s.writeRequestError(w, r, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	skill, err := s.store.GetSkill(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, skill)
}

func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var req skillRequest
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	if req.Prompt == nil {
		writeError(w, http.StatusBadRequest, "skill prompt is required")
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
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	parsed, err := skills.Normalize(skills.Skill{
		Name:        valueOrEmpty(req.Name),
		Command:     valueOrEmpty(req.Command),
		Description: valueOrEmpty(req.Description),
		Prompt:      *req.Prompt,
	})
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	record, err := scannedSkillRecord(parsed, source, enabled, req.AcknowledgeRisk)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	created, err := s.store.CreateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("created", created)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetSkill(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	var req skillRequest
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
	record.Scope = existing.Scope
	record.ProjectID = existing.ProjectID
	record.WorklineID = existing.WorklineID
	record.UpdatedAt = strings.TrimSpace(*req.ExpectedUpdatedAt)
	updated, err := s.store.UpdateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("updated", updated)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteSkill(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetSkill(r.Context(), id)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	if err := s.store.DeleteSkillAs(r.Context(), id, "api_request"); err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("deleted", existing)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) previewSkillImport(w http.ResponseWriter, r *http.Request) {
	var req skillImportRequest
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	parsed, err := skills.ParseMarkdown(req.Content)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	result := skills.Scan(parsed)
	writeJSON(w, http.StatusOK, makeSkillPreview(parsed, result))
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	var req skillImportRequest
	if err := decodeSkillJSON(w, r, &req); err != nil {
		s.writeRequestError(w, r, statusFromSkillDecodeError(err), err)
		return
	}
	parsed, err := skills.ParseMarkdown(req.Content)
	if err != nil {
		s.writeRequestError(w, r, http.StatusBadRequest, err)
		return
	}
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	record, err := scannedSkillRecord(parsed, "skill_md", enabled, req.AcknowledgeRisk)
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	created, err := s.store.CreateSkillAs(r.Context(), record, "api_request")
	if err != nil {
		s.writeRequestError(w, r, statusFromSkillError(err), err)
		return
	}
	logSkillChange("imported", created)
	writeJSON(w, http.StatusCreated, created)
}

func makeSkillPreview(skill skills.Skill, result skills.ScanResult) skillPreviewResponse {
	return skillPreviewResponse{
		Name: skill.Name, Command: skill.Command, Description: skill.Description, Prompt: skill.Prompt,
		ContentHash: result.Hash, ScanVerdict: result.Verdict, ScanFindings: result.Findings,
	}
}

func scannedSkillRecord(parsed skills.Skill, source string, enabled, acknowledgeRisk bool) (db.Skill, error) {
	result := skills.Scan(parsed)
	findings, err := json.Marshal(result.Findings)
	if err != nil {
		return db.Skill{}, err
	}
	record := db.Skill{
		Name: parsed.Name, Command: parsed.Command, Description: parsed.Description, Prompt: parsed.Prompt,
		Source: source, ContentHash: result.Hash, Enabled: enabled, ScanVerdict: result.Verdict, ScanFindings: findings,
	}
	switch result.Verdict {
	case skills.VerdictBlocked:
		if enabled {
			return db.Skill{}, skillStateError{message: "blocked skills cannot be enabled"}
		}
	case skills.VerdictReview:
		if !enabled {
			break
		}
		if !acknowledgeRisk {
			return db.Skill{}, skillStateError{message: "review skills require acknowledgeRisk: true before enabling"}
		}
		// A confirmation is intentionally minted only for this transition. It is
		// cleared on disable and must not be reused after a later enable request.
		record.RiskAcknowledgedAt = db.Now()
		record.RiskAcknowledgedBy = "api_request"
		record.RiskAcknowledgedHash = result.Hash
	}
	return record, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func decodeSkillJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if contentType == "" || err != nil || !strings.EqualFold(mediaType, "application/json") {
		return skillContentTypeError{message: "Content-Type must be application/json"}
	}
	defer r.Body.Close()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSkillJSONBody))
	if err != nil {
		return err
	}
	if !utf8.Valid(body) {
		return errors.New("request body must be valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain a single JSON value")
		}
		return err
	}
	return nil
}

func statusFromSkillDecodeError(err error) int {
	var contentTypeErr skillContentTypeError
	if errors.As(err, &contentTypeErr) {
		return http.StatusUnsupportedMediaType
	}
	return http.StatusBadRequest
}

func statusFromSkillError(err error) int {
	if db.IsNotFound(err) {
		return http.StatusNotFound
	}
	if db.IsConflict(err) || errors.As(err, new(skillStateError)) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

func logSkillChange(action string, skill db.Skill) {
	hash := skill.ContentHash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	slog.Info("skill "+action,
		"skillId", skill.ID,
		"command", skill.Command,
		"source", skill.Source,
		"enabled", skill.Enabled,
		"scanVerdict", skill.ScanVerdict,
		"findingCount", countSkillFindings(skill.ScanFindings),
		"contentHashPrefix", hash,
	)
}

func countSkillFindings(raw json.RawMessage) int {
	var findings []json.RawMessage
	if json.Unmarshal(raw, &findings) != nil {
		return 0
	}
	return len(findings)
}
