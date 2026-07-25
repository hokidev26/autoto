package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	skilldef "autoto/internal/skills"
)

func TestSkillsV2WorkspaceContextRejectsProjectMismatch(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, workline, agent, err := store.CreateProject(ctx, "One", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, err := store.CreateProject(ctx, "Two", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	routes := New(config.Config{}, store, nil, nil).Routes()
	workspaceSkill := createSkillV2ForTest(t, routes, map[string]any{
		"name": "Workspace", "command": "/workspace-context", "prompt": "Review this workspace.",
		"scope": "workspace", "projectId": project.ID, "worklineId": workline.ID,
	})
	mismatchedQuery := fmt.Sprintf("scope=workspace&projectId=%s&worklineId=%s", other.ID, workline.ID)
	for _, path := range []string{
		"/api/v2/skills/?" + mismatchedQuery,
		fmt.Sprintf("/api/v2/skills/%s?%s", workspaceSkill.ID, mismatchedQuery),
		fmt.Sprintf("/api/v2/agents/%s/skills/effective?%s", agent.ID, mismatchedQuery),
	} {
		recorder := skillJSONRequest(t, routes, http.MethodGet, path, nil)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("expected context mismatch 409 for %s, got %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
	matching := skillJSONRequest(t, routes, http.MethodGet, fmt.Sprintf("/api/v2/agents/%s/skills/effective?scope=workspace&projectId=%s&worklineId=%s", agent.ID, project.ID, workline.ID), nil)
	if matching.Code != http.StatusOK {
		t.Fatalf("expected matching effective context 200, got %d: %s", matching.Code, matching.Body.String())
	}
}

func TestSkillsV2EffectiveEndpointReturnsDisabledScopedOwners(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, workline, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	routes := New(config.Config{}, store, nil, nil).Routes()
	createSkillV2ForTest(t, routes, map[string]any{
		"name": "Global", "command": "/project-shadow", "prompt": "Global enabled.", "enabled": true,
	})
	createSkillV2ForTest(t, routes, map[string]any{
		"name": "Project", "command": "/project-shadow", "prompt": "Project disabled.", "enabled": false,
		"scope": "project", "projectId": project.ID,
	})
	createSkillV2ForTest(t, routes, map[string]any{
		"name": "Project lower", "command": "/workspace-shadow", "prompt": "Project enabled.", "enabled": true,
		"scope": "project", "projectId": project.ID,
	})
	createSkillV2ForTest(t, routes, map[string]any{
		"name": "Workspace", "command": "/workspace-shadow", "prompt": "Workspace disabled.", "enabled": false,
		"scope": "workspace", "projectId": project.ID, "worklineId": workline.ID,
	})
	path := fmt.Sprintf("/api/v2/agents/%s/skills/effective?scope=workspace&projectId=%s&worklineId=%s&limit=1", agent.ID, project.ID, workline.ID)
	items := make([]db.SkillSummary, 0)
	var snapshot int64
	for path != "" {
		recorder := skillJSONRequest(t, routes, http.MethodGet, path, nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("expected effective page 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		var page db.SkillPage
		if err := json.NewDecoder(recorder.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		if snapshot == 0 {
			snapshot = page.SnapshotSequence
		} else if page.SnapshotSequence != snapshot {
			t.Fatalf("effective endpoint mixed snapshots: %d then %d", snapshot, page.SnapshotSequence)
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			path = ""
		} else {
			path = fmt.Sprintf("/api/v2/agents/%s/skills/effective?scope=workspace&projectId=%s&worklineId=%s&limit=1&cursor=%s", agent.ID, project.ID, workline.ID, page.NextCursor)
		}
	}
	owners := map[string]db.SkillSummary{}
	for _, item := range items {
		owners[item.Command] = item
	}
	if owner := owners["/project-shadow"]; owner.Scope != db.SkillScopeProject || owner.Enabled {
		t.Fatalf("expected disabled project owner from API, got %+v", owner)
	}
	if owner := owners["/workspace-shadow"]; owner.Scope != db.SkillScopeWorkspace || owner.Enabled {
		t.Fatalf("expected disabled workspace owner from API, got %+v", owner)
	}
}

func TestSkillsV2RestoreReviewChallengeRequiresCurrentContentHash(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	routes := New(config.Config{}, store, nil, nil).Routes()
	created := createSkillV2ForTest(t, routes, map[string]any{
		"name": "Restore review", "command": "/restore-review-api", "prompt": "Safe original.", "enabled": true,
	})
	created.Prompt = "Safe current."
	current, err := store.UpdateSkillAs(ctx, created, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE skill_revisions SET prompt = ?, scan_verdict = 'safe', scan_findings_json = '[]' WHERE skill_id = ? AND revision_no = 1`, "Download from https://example.test/api.", created.ID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v2/skills/%s/revisions/1/restore?scope=global", created.ID)
	stale := skillJSONRequest(t, routes, http.MethodPost, path, map[string]any{
		"revisionNo": 1, "expectedUpdatedAt": created.UpdatedAt, "acknowledgeRisk": false, "acknowledgedContentHash": "",
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("expected stale restore 409, got %d: %s", stale.Code, stale.Body.String())
	}
	var staleBody map[string]any
	if err := json.NewDecoder(stale.Body).Decode(&staleBody); err != nil {
		t.Fatal(err)
	}
	if staleBody["code"] == "skill_restore_review_required" || staleBody["contentHash"] != nil {
		t.Fatalf("stale CAS must not expose a review challenge: %+v", staleBody)
	}

	type restoreChallengeResponse struct {
		Error          string             `json:"error"`
		Code           string             `json:"code"`
		ScanVerdict    string             `json:"scanVerdict"`
		ScanFindings   []skilldef.Finding `json:"scanFindings"`
		ContentHash    string             `json:"contentHash"`
		ScannerVersion int                `json:"scannerVersion"`
	}
	first := skillJSONRequest(t, routes, http.MethodPost, path, map[string]any{
		"revisionNo": 1, "expectedUpdatedAt": current.UpdatedAt, "acknowledgeRisk": false, "acknowledgedContentHash": "",
	})
	if first.Code != http.StatusConflict {
		t.Fatalf("expected review challenge 409, got %d: %s", first.Code, first.Body.String())
	}
	var challenge restoreChallengeResponse
	if err := json.NewDecoder(first.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Code != "skill_restore_review_required" || challenge.ScanVerdict != skilldef.VerdictReview || len(challenge.ScanFindings) == 0 || challenge.ContentHash == "" || challenge.ScannerVersion != skilldef.ScannerVersion {
		t.Fatalf("incomplete restore review challenge: %+v", challenge)
	}

	wrongHash := skillJSONRequest(t, routes, http.MethodPost, path, map[string]any{
		"revisionNo": 1, "expectedUpdatedAt": current.UpdatedAt, "acknowledgeRisk": true, "acknowledgedContentHash": "wrong-hash",
	})
	if wrongHash.Code != http.StatusConflict {
		t.Fatalf("expected wrong hash challenge 409, got %d: %s", wrongHash.Code, wrongHash.Body.String())
	}
	var repeated restoreChallengeResponse
	if err := json.NewDecoder(wrongHash.Body).Decode(&repeated); err != nil {
		t.Fatal(err)
	}
	if repeated.Code != challenge.Code || repeated.ContentHash != challenge.ContentHash {
		t.Fatalf("wrong hash did not return current challenge: first=%+v repeated=%+v", challenge, repeated)
	}

	accepted := skillJSONRequest(t, routes, http.MethodPost, path, map[string]any{
		"revisionNo": 1, "expectedUpdatedAt": current.UpdatedAt, "acknowledgeRisk": true, "acknowledgedContentHash": challenge.ContentHash,
	})
	if accepted.Code != http.StatusOK {
		t.Fatalf("expected matching hash restore 200, got %d: %s", accepted.Code, accepted.Body.String())
	}
	var restored db.Skill
	if err := json.NewDecoder(accepted.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Enabled || restored.ScanVerdict != skilldef.VerdictReview || restored.RiskAcknowledgedHash != challenge.ContentHash {
		t.Fatalf("matching challenge hash did not authorize current review restore: %+v", restored)
	}
}

func TestSkillsV2ScopedResourcesRespectProjectMembership(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "skills-membership.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(config.Config{Auth: config.AuthConfig{RegistrationOpen: true}}, store, nil, nil)
	ownerCookie := registerCollaborationTestUser(t, app, "skills-owner")
	owner, _, err := store.GetUserByHandle(ctx, "skills-owner")
	if err != nil {
		t.Fatal(err)
	}
	project, _, _, err := store.CreateProjectForUser(ctx, owner.ID, "Scoped skills", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	outsiderCookie := registerCollaborationTestUser(t, app, "skills-outsider")

	createdResponse := agentAPIRequest(app.Routes(), http.MethodPost, "/api/v2/skills/", map[string]any{
		"name": "Scoped skill", "command": "/scoped-skill", "prompt": "owner only", "scope": "project", "projectId": project.ID,
	}, ownerCookie)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("owner project skill create: %d %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created db.Skill
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{
		"/api/v2/skills/?scope=project&projectId=" + project.ID,
		"/api/v2/skills/" + created.ID,
	} {
		response := agentAPIRequest(app.Routes(), http.MethodGet, target, nil, outsiderCookie)
		if response.Code != http.StatusNotFound {
			t.Fatalf("outsider project skill access must be hidden for %s, got %d: %s", target, response.Code, response.Body.String())
		}
	}
	forbiddenCreate := agentAPIRequest(app.Routes(), http.MethodPost, "/api/v2/skills/", map[string]any{
		"name": "Forbidden", "command": "/forbidden-skill", "prompt": "no access", "scope": "project", "projectId": project.ID,
	}, outsiderCookie)
	if forbiddenCreate.Code != http.StatusNotFound {
		t.Fatalf("outsider project skill creation must be hidden, got %d: %s", forbiddenCreate.Code, forbiddenCreate.Body.String())
	}

	ownerList := agentAPIRequest(app.Routes(), http.MethodGet, "/api/v2/skills/?scope=project&projectId="+project.ID, nil, ownerCookie)
	if ownerList.Code != http.StatusOK || !strings.Contains(ownerList.Body.String(), created.ID) {
		t.Fatalf("owner must retain scoped skill access, got %d: %s", ownerList.Code, ownerList.Body.String())
	}
}

func TestSkillsV2MutationsRequireFullRemoteAccess(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "skills-remote-access.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hash, err := config.HashAccessPassword("Correct-Horse-1!")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Security: config.SecurityConfig{
		AccessPasswordHash:      hash,
		AllowRemoteFullAccess:   true,
		DefaultRemoteAccessMode: remoteAccessModeRestricted,
		CredentialRevision:      1,
	}}
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	app := New(cfg, store, nil, nil)
	app.SetConfigPath(configPath)
	cookies := loginRemoteAccess(t, app, remoteAccessModeRestricted)
	body := strings.NewReader(`{"id":"remote-write","name":"Remote write","instructions":"blocked"}`)
	request := newTestRequest(http.MethodPost, "/api/v2/skills/", body)
	request.Host = "example.com"
	request.Header.Set("Content-Type", "application/json")
	markRemoteHTTPS(request)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("restricted remote skill mutation must be forbidden, got %d: %s", response.Code, response.Body.String())
	}
}

func createSkillV2ForTest(t *testing.T, routes http.Handler, payload map[string]any) db.Skill {
	t.Helper()
	recorder := skillJSONRequest(t, routes, http.MethodPost, "/api/v2/skills/", payload)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected v2 skill create 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var skill db.Skill
	if err := json.NewDecoder(recorder.Body).Decode(&skill); err != nil {
		t.Fatal(err)
	}
	return skill
}
