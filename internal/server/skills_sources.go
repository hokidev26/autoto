package server

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"autoto/internal/db"
	"autoto/internal/skills"
	"autoto/internal/skillsources"
)

const skillSourceImportSource = "skill_md"

var skillSourceUserHomeDir = os.UserHomeDir
var discoverSkillSourceFiles = func(root string) (skillsources.Result, error) {
	return skillsources.Discover(root)
}

type skillSourceScopeInfo struct {
	SourceScope string `json:"sourceScope"`
}

type skillSourceDiscoveryResponse struct {
	Scope  skillSourceScopeInfo `json:"scope"`
	Result skillsources.Result  `json:"result"`
}

type skillSourceImportProvenance struct {
	RootID       string `json:"rootId"`
	AdapterID    string `json:"adapterId"`
	RelativePath string `json:"relativePath"`
}

type skillSourceImportTarget struct {
	Scope      string `json:"scope"`
	ProjectID  string `json:"projectId"`
	WorklineID string `json:"worklineId"`
}

type skillSourceImportRequest struct {
	SourceScope       string                      `json:"sourceScope"`
	ProjectID         string                      `json:"projectId"`
	Provenance        skillSourceImportProvenance `json:"provenance"`
	SourceHash        string                      `json:"sourceHash"`
	SidecarSourceHash string                      `json:"sidecarSourceHash"`
	Target            skillSourceImportTarget     `json:"target"`
	Scope             string                      `json:"scope"`
	TargetProjectID   string                      `json:"targetProjectId"`
	WorklineID        string                      `json:"worklineId"`
	Enabled           bool                        `json:"enabled"`
	AcknowledgeRisk   bool                        `json:"acknowledgeRisk"`
}

func (s *Server) localSkillSourceGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !trustedLoopbackPeer(r) || !isLoopbackHost(r.Host) || requestHasRemoteForwardingHeaders(r) || requestHasRemoteAccessCredential(r) {
			writeError(w, http.StatusForbidden, "file skill sources are available only to direct loopback requests")
			return
		}
		if strings.TrimSpace(r.Header.Get(legacyLocalTokenHeader)) != "" || strings.TrimSpace(r.URL.Query().Get(localTokenQuery)) != "" || !constantTimeEqualToken(r.Header.Get(localTokenHeader), s.localToken) {
			writeError(w, http.StatusUnauthorized, "missing or invalid canonical local API token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) listSkillSources(w http.ResponseWriter, r *http.Request) {
	if err := rejectUnknownQuery(r, "sourceScope", "projectId"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sourceScope := strings.TrimSpace(r.URL.Query().Get("sourceScope"))
	projectID := strings.TrimSpace(r.URL.Query().Get("projectId"))
	if sourceScope == "project" && !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: projectID}) {
		return
	}
	result, err := s.discoverSkillSource(r, sourceScope, projectID)
	if err != nil {
		writeSkillSourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, skillSourceDiscoveryResponse{
		Scope:  skillSourceScopeInfo{SourceScope: sourceScope},
		Result: result,
	})
}

func (s *Server) importSkillSource(w http.ResponseWriter, r *http.Request) {
	var req skillSourceImportRequest
	if err := decodeSkillJSON(w, r, &req); err != nil {
		writeError(w, statusFromSkillDecodeError(err), err.Error())
		return
	}
	req.SourceScope = strings.TrimSpace(req.SourceScope)
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	sourceProjectID := ""
	if req.SourceScope == "project" {
		sourceProjectID = req.ProjectID
		if !s.requireProjectResourceAccess(w, r, projectAccessTarget{kind: projectAccessProject, id: sourceProjectID}) {
			return
		}
	}
	target := skillSourceTargetFromRequest(req)
	if target.Scope == "" {
		target.Scope = db.SkillScopeGlobal
	}
	if !s.requireSkillScopeAccess(w, r, target) {
		return
	}
	if err := validateSkillSourceImportIdentity(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.discoverSkillSource(r, req.SourceScope, sourceProjectID)
	if err != nil {
		writeSkillSourceError(w, err)
		return
	}
	candidate, matched := matchSkillSourceCandidate(result, req)
	if !matched {
		writeError(w, http.StatusConflict, "skill source changed; refresh file sources before importing")
		return
	}
	if candidate.ConflictStatus != skillsources.ConflictNone {
		writeError(w, http.StatusConflict, "skill source has an unresolved command conflict")
		return
	}
	if candidate.Scan.Verdict == skills.VerdictBlocked {
		writeError(w, http.StatusConflict, "blocked file skills cannot be imported")
		return
	}
	record, err := scannedSkillRecord(candidate.Skill, skillSourceImportSource, req.Enabled, req.AcknowledgeRisk)
	if err != nil {
		writeError(w, statusFromSkillError(err), err.Error())
		return
	}
	if record.ContentHash != candidate.Hash || record.ScanVerdict != candidate.Scan.Verdict {
		writeError(w, http.StatusConflict, "skill source scan changed; refresh file sources before importing")
		return
	}
	record.Scope, record.ProjectID, record.WorklineID = target.Scope, target.ProjectID, target.WorklineID
	created, err := s.store.CreateSkillAs(r.Context(), record, "file_skill_source")
	if err != nil {
		writeError(w, statusFromSkillError(err), err.Error())
		return
	}
	logSkillChange("imported from file source", created)
	writeJSON(w, http.StatusCreated, created)
}

func skillSourceTargetFromRequest(req skillSourceImportRequest) db.SkillScopeTarget {
	target := db.SkillScopeTarget{
		Scope:      strings.TrimSpace(req.Target.Scope),
		ProjectID:  strings.TrimSpace(req.Target.ProjectID),
		WorklineID: strings.TrimSpace(req.Target.WorklineID),
	}
	if target.Scope == "" {
		target.Scope = strings.TrimSpace(req.Scope)
	}
	if target.Scope == "" {
		target.Scope = db.SkillScopeGlobal
	}
	if target.ProjectID == "" {
		target.ProjectID = strings.TrimSpace(req.TargetProjectID)
	}
	if target.ProjectID == "" && (target.Scope == db.SkillScopeProject || target.Scope == db.SkillScopeWorkspace) {
		target.ProjectID = strings.TrimSpace(req.ProjectID)
	}
	if target.WorklineID == "" {
		target.WorklineID = strings.TrimSpace(req.WorklineID)
	}
	return target
}

func validateSkillSourceImportIdentity(req skillSourceImportRequest) error {
	if req.SourceScope != "user" && req.SourceScope != "project" {
		return errors.New("sourceScope must be user or project")
	}
	if req.SourceScope == "project" && req.ProjectID == "" {
		return errors.New("projectId is required for project file sources")
	}
	if strings.TrimSpace(req.Provenance.RootID) == "" || strings.TrimSpace(req.Provenance.AdapterID) == "" || strings.TrimSpace(req.Provenance.RelativePath) == "" {
		return errors.New("complete file source provenance is required")
	}
	if req.Provenance.RootID != strings.TrimSpace(req.Provenance.RootID) || req.Provenance.AdapterID != strings.TrimSpace(req.Provenance.AdapterID) || req.Provenance.RelativePath != strings.TrimSpace(req.Provenance.RelativePath) {
		return errors.New("file source provenance must not contain surrounding whitespace")
	}
	if !validSkillSourceHash(req.SourceHash) || req.SidecarSourceHash != "" && !validSkillSourceHash(req.SidecarSourceHash) {
		return errors.New("file source hashes must be lowercase SHA-256 values")
	}
	return nil
}

func validSkillSourceHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func matchSkillSourceCandidate(result skillsources.Result, req skillSourceImportRequest) (skillsources.Candidate, bool) {
	for _, candidate := range result.Candidates {
		if candidate.Provenance.RootID != req.Provenance.RootID || candidate.Provenance.AdapterID != req.Provenance.AdapterID || candidate.Provenance.RelativePath != req.Provenance.RelativePath {
			continue
		}
		if candidate.SourceHash != req.SourceHash || candidate.SidecarSourceHash != req.SidecarSourceHash {
			return skillsources.Candidate{}, false
		}
		return candidate, true
	}
	return skillsources.Candidate{}, false
}

func (s *Server) discoverSkillSource(r *http.Request, sourceScope, projectID string) (skillsources.Result, error) {
	root, err := s.skillSourceRoot(r, sourceScope, projectID)
	if err != nil {
		return skillsources.Result{}, err
	}
	result, err := discoverSkillSourceFiles(root)
	if err != nil {
		return skillsources.Result{}, &skillSourceError{status: http.StatusBadRequest, message: "skill source root could not be read safely"}
	}
	return result, nil
}

func (s *Server) skillSourceRoot(r *http.Request, sourceScope, projectID string) (string, error) {
	var root string
	switch sourceScope {
	case "user":
		if strings.TrimSpace(projectID) != "" {
			return "", &skillSourceError{status: http.StatusBadRequest, message: "projectId is not allowed for user file sources"}
		}
		home, err := skillSourceUserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", &skillSourceError{status: http.StatusServiceUnavailable, message: "user skill source root is unavailable"}
		}
		root = home
	case "project":
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			return "", &skillSourceError{status: http.StatusBadRequest, message: "projectId is required for project file sources"}
		}
		if s.store == nil {
			return "", &skillSourceError{status: http.StatusServiceUnavailable, message: "project skill source root is unavailable"}
		}
		project, err := s.store.GetProject(r.Context(), projectID)
		if db.IsNotFound(err) {
			return "", &skillSourceError{status: http.StatusNotFound, message: "project not found"}
		}
		if err != nil {
			return "", &skillSourceError{status: http.StatusInternalServerError, message: "project skill source root is unavailable"}
		}
		root = project.GitPath
		if strings.TrimSpace(root) == "" {
			return "", &skillSourceError{status: http.StatusConflict, message: "project skill source root is not configured"}
		}
	default:
		return "", &skillSourceError{status: http.StatusBadRequest, message: "sourceScope must be user or project"}
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", &skillSourceError{status: http.StatusNotFound, message: "skill source root does not exist"}
	}
	if err != nil {
		return "", &skillSourceError{status: http.StatusBadRequest, message: "skill source root could not be inspected safely"}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", &skillSourceError{status: http.StatusBadRequest, message: "skill source root must be a real directory"}
	}
	return root, nil
}

type skillSourceError struct {
	status  int
	message string
}

func (err *skillSourceError) Error() string { return err.message }

func writeSkillSourceError(w http.ResponseWriter, err error) {
	var sourceErr *skillSourceError
	if errors.As(err, &sourceErr) {
		writeError(w, sourceErr.status, sourceErr.message)
		return
	}
	writeError(w, http.StatusBadRequest, "skill source request failed")
}
