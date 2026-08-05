package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestForkWorklineCreatesGitWorktreeAgentAndAllowsGitStatus(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "initial\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "initial")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits", DefaultStartInPlanMode: true}}, store, nil, nil)

	body := strings.NewReader(`{"title":"Feature Branch","branch":"feature/autoto-test"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+root.ID+"/fork", body)
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response forkWorklineResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Workline.ProjectID != project.ID || response.Workline.ParentWorklineID != root.ID || response.Workline.Branch != "feature/autoto-test" || response.Workline.WorktreePath == "" {
		t.Fatalf("unexpected fork response: %+v", response)
	}
	if response.Agent.WorklineID != response.Workline.ID || response.Agent.CWD != response.Workline.WorktreePath || !response.Agent.PlanMode {
		t.Fatalf("unexpected fork agent: %+v", response.Agent)
	}
	if pathWithin(repo, response.Workline.WorktreePath) {
		t.Fatalf("worktree should not be nested in source repo: %s", response.Workline.WorktreePath)
	}
	if !pathWithin(filepath.Join(filepath.Dir(repo), ".autoto-worktrees", "demo"), response.Workline.WorktreePath) {
		t.Fatalf("expected Autoto worktree directory, got %s", response.Workline.WorktreePath)
	}
	branch := strings.TrimSpace(runGitTestOutput(t, response.Workline.WorktreePath, "branch", "--show-current"))
	if branch != "feature/autoto-test" {
		t.Fatalf("expected fork branch, got %q", branch)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/agents/"+response.Agent.ID+"/git/status", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected fork agent git status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// A fork continues the conversation it branched from, so it has to run on that
// conversation's model. Falling through to the global default silently moved the
// fork onto a different model, and when that default advertises no
// reasoning-effort levels the composer reports the level as unsupported on a
// fork of a conversation where it worked. The project model and the config
// default differ here on purpose: with both set to the same value the assertion
// cannot tell inheritance from the fallback.
func TestForkWorklineInheritsParentConversationModel(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initCommittedGitRepoAt(t, repo, "README.md", "initial\n")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, parentAgent, err := store.CreateProject(ctx, "Demo", "", repo, "codex:gpt-5.6-sol", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	if parentAgent.Model != "codex:gpt-5.6-sol" {
		t.Fatalf("fixture precondition: parent agent should hold the project model, got %q", parentAgent.Model)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:unrelated-default", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)

	fork := forkWorklineForTest(t, app, root.ID, "feature/inherit-model")
	if fork.Agent.Model != "codex:gpt-5.6-sol" {
		t.Fatalf("fork should inherit the parent conversation model, got %q", fork.Agent.Model)
	}
}

// An explicit model in the request still wins, so callers that deliberately
// fork onto a different model are unaffected by the inheritance above.
func TestForkWorklineRequestModelOverridesInheritance(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initCommittedGitRepoAt(t, repo, "README.md", "initial\n")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "codex:gpt-5.6-sol", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:unrelated-default", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+root.ID+"/fork", strings.NewReader(`{"title":"Explicit","branch":"feature/explicit-model","model":"openai:chosen"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response forkWorklineResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Agent.Model != "openai:chosen" {
		t.Fatalf("explicit request model should win, got %q", response.Agent.Model)
	}
}

func TestForkWorklineAutoDetectsSingleVisibleRepository(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	hiddenRepo := filepath.Join(rootDir, ".hidden-cache")
	visibleRepo := filepath.Join(rootDir, "visible-project")
	initCommittedGitRepoAt(t, hiddenRepo, "hidden.txt", "hidden repository\n")
	visibleHead := initCommittedGitRepoAt(t, visibleRepo, "README.md", "visible repository\n")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Container", "", rootDir, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)

	fork := forkWorklineForTest(t, app, root.ID, "feature/visible-repository")
	if fork.ForkPoint != visibleHead {
		t.Fatalf("expected visible repository HEAD %q, got %q", visibleHead, fork.ForkPoint)
	}
}

func TestForkWorklineReportsRepositoryWithoutCommits(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	runGitTestCommand(t, repo, "init", "-b", "main")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Unborn", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+root.ID+"/fork", strings.NewReader(`{"title":"Feature"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response["code"] != "git_no_commits" || response["path"] == "" {
		t.Fatalf("unexpected setup response: %#v", response)
	}
}

func TestInitProjectGitCreatesVerifiedHeadAndAllowsFork(t *testing.T) {
	ctx := context.Background()
	projectDir := t.TempDir()
	writeGitTestFile(t, projectDir, "README.md", "initial contents\n")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, root, _, err := store.CreateProject(ctx, "Initialize", "", projectDir, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/projects/"+project.ID+"/init-git", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	head := strings.TrimSpace(runGitTestOutput(t, projectDir, "rev-parse", "--verify", "HEAD"))
	if head == "" {
		t.Fatal("expected initialized repository to have HEAD")
	}
	fork := forkWorklineForTest(t, app, root.ID, "feature/after-init")
	if fork.ForkPoint != head {
		t.Fatalf("expected fork point %q, got %q", head, fork.ForkPoint)
	}
}

func initCommittedGitRepoAt(t *testing.T, repo, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "init", "-b", "main")
	runGitTestCommand(t, repo, "config", "user.name", "Autoto Test")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.com")
	writeGitTestFile(t, repo, name, content)
	runGitTestCommand(t, repo, "add", name)
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	return strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
}

func TestDefaultWorklineBranchUsesAutotoPrefix(t *testing.T) {
	if branch := defaultWorklineBranch("Feature Branch"); !strings.HasPrefix(branch, "autoto/") {
		t.Fatalf("expected Autoto branch prefix, got %q", branch)
	}
}

func TestLegacyAgentWorklineRoutesAliasCanonicalHandlers(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	project, workline, agent, err := store.CreateProject(ctx, "Legacy routes", "", t.TempDir(), "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)
	for _, path := range []string{
		"/api/projects/" + project.ID + "/chapters",
		"/api/chapters/" + workline.ID,
		"/api/chapters/" + workline.ID + "/narrators",
		"/api/narrators/" + agent.ID,
	} {
		recorder := httptest.NewRecorder()
		app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("legacy alias %s returned %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestWorklineMergeMergesCleanSourceIntoTarget(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "feature/merge-success")
	writeGitTestFile(t, fork.Workline.WorktreePath, "feature.txt", "merged feature\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "feature.txt")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "feature change")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/merge", strings.NewReader(`{"targetWorklineId":"`+root.ID+`","message":"Merge feature workline"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineMergeResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.Merged || response.MergeCommit == "" || response.Workline.Status != "merged" || response.Workline.MergedIntoWorklineID != root.ID {
		t.Fatalf("unexpected merge response: %+v", response)
	}
	if got := strings.TrimSpace(runGitTestOutput(t, repo, "show", "HEAD:feature.txt")); got != "merged feature" {
		t.Fatalf("expected merged feature file in target, got %q", got)
	}
	stored, err := store.GetWorkline(ctx, fork.Workline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "merged" || stored.MergeCommitSHA != response.MergeCommit || stored.PreMergeTargetSHA == "" {
		t.Fatalf("expected merge metadata persisted, got %+v", stored)
	}
}

func TestWorklineMergeRejectsConflictsAndAborts(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "conflict/merge")
	writeGitTestFile(t, repo, "README.md", "target change\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "target change")
	writeGitTestFile(t, fork.Workline.WorktreePath, "README.md", "source change\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "README.md")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "source change")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/merge", strings.NewReader(`{"targetWorklineId":"`+root.ID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineMergeResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Merged || !containsString(response.Conflicts, "README.md") {
		t.Fatalf("expected conflict response, got %+v", response)
	}
	if status := strings.TrimSpace(runGitTestOutput(t, repo, "status", "--porcelain")); status != "" {
		t.Fatalf("expected merge abort to leave target clean, got %q", status)
	}
	stored, err := store.GetWorkline(ctx, fork.Workline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status == "merged" || stored.MergeCommitSHA != "" {
		t.Fatalf("conflicted merge should not update workline metadata: %+v", stored)
	}
}

func TestWorklineMergeCheckReportsConflicts(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "conflict/source")

	writeGitTestFile(t, repo, "README.md", "target change\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "target change")
	writeGitTestFile(t, fork.Workline.WorktreePath, "README.md", "source change\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "README.md")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "source change")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/worklines/"+fork.Workline.ID+"/merge-check?targetWorklineId="+root.ID, nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineMergeCheckResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.CanMerge {
		t.Fatalf("expected conflict response, got %+v", response)
	}
	if !containsString(response.Conflicts, "README.md") {
		t.Fatalf("expected README.md conflict, got %+v", response)
	}
}

func forkWorklineForTest(t *testing.T, app *Server, worklineID, branch string) forkWorklineResponse {
	t.Helper()
	body := strings.NewReader(`{"title":"` + branch + `","branch":"` + branch + `"}`)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+worklineID+"/fork", body)
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected fork 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response forkWorklineResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	return response
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// The merge-check response is what the review panel renders, so it has to carry
// the size and direction of the change, not just a can/cannot verdict. A bare
// boolean gives the user nothing to decide with.
func TestWorklineMergeCheckReportsReviewSignals(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "feature/review-signals")

	// Two commits on the source touching two files, one commit on the target:
	// ahead=2, behind=1, and only the source's own files must be counted.
	writeGitTestFile(t, fork.Workline.WorktreePath, "one.txt", "one\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "one.txt")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "source one")
	writeGitTestFile(t, fork.Workline.WorktreePath, "two.txt", "two\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "two.txt")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "source two")
	writeGitTestFile(t, repo, "target-only.txt", "target\n")
	runGitTestCommand(t, repo, "add", "target-only.txt")
	runGitTestCommand(t, repo, "commit", "-m", "target only")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/worklines/"+fork.Workline.ID+"/merge-check?targetWorklineId="+root.ID, nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineMergeCheckResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.CanMerge || len(response.Conflicts) != 0 {
		t.Fatalf("expected a clean merge verdict, got %+v", response)
	}
	if response.Ahead != 2 || response.Behind != 1 {
		t.Fatalf("expected ahead=2 behind=1, got ahead=%d behind=%d", response.Ahead, response.Behind)
	}
	if response.ChangedCount != 2 {
		t.Fatalf("expected 2 changed files, got %d (%v)", response.ChangedCount, response.ChangedFiles)
	}
	// Diffing against the merge base, not the target head, is what keeps the
	// target's own commit out of the source's changed-file list.
	if containsString(response.ChangedFiles, "target-only.txt") {
		t.Fatalf("target-only work must not be attributed to the source: %v", response.ChangedFiles)
	}
	if !containsString(response.ChangedFiles, "one.txt") || !containsString(response.ChangedFiles, "two.txt") {
		t.Fatalf("expected both source files, got %v", response.ChangedFiles)
	}
	if response.AlreadyMerged || response.SourceDirty || response.TargetDirty {
		t.Fatalf("expected committed, unmerged worktrees, got %+v", response)
	}
}

// Uncommitted work is the most common reason a merge that "checks out" then
// fails, so the check has to surface it before the user commits to merging.
func TestWorklineMergeCheckFlagsDirtyWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "feature/dirty")
	writeGitTestFile(t, fork.Workline.WorktreePath, "wip.txt", "uncommitted\n")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/worklines/"+fork.Workline.ID+"/merge-check?targetWorklineId="+root.ID, nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineMergeCheckResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.SourceDirty {
		t.Fatalf("expected the uncommitted source worktree to be reported: %+v", response)
	}
	// Nothing committed means nothing to bring over, even though the worktree
	// has edits sitting in it.
	if !response.AlreadyMerged || response.ChangedCount != 0 {
		t.Fatalf("expected no committed work to merge, got %+v", response)
	}
}
