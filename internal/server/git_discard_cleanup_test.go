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

// Discard is the general escape hatch when a run checkpoint is unavailable:
// the selection is restored to HEAD in index and worktree, and files HEAD does
// not know about are deleted. Everything outside the selection must survive.
func TestGitDiscardRouteRestoresSelectionToHead(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "modified.txt", "base\n")
	writeGitTestFile(t, repo, "staged.txt", "base\n")
	writeGitTestFile(t, repo, "kept.txt", "base\n")
	runGitTestCommand(t, repo, "add", "modified.txt", "staged.txt", "kept.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")

	writeGitTestFile(t, repo, "modified.txt", "base\nchanged\n")
	writeGitTestFile(t, repo, "staged.txt", "base\nstaged change\n")
	runGitTestCommand(t, repo, "add", "staged.txt")
	writeGitTestFile(t, repo, "added.txt", "brand new staged\n")
	runGitTestCommand(t, repo, "add", "added.txt")
	writeGitTestFile(t, repo, "untracked.txt", "brand new\n")
	writeGitTestFile(t, repo, "kept.txt", "base\nmust stay\n")

	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/discard", strings.NewReader(`{"confirm":true,"paths":["modified.txt","staged.txt","added.txt","untracked.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response gitDiscardResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !containsString(response.RestoredPaths, "modified.txt") || !containsString(response.RestoredPaths, "staged.txt") {
		t.Fatalf("expected tracked files in restored list, got %+v", response)
	}
	if !containsString(response.DeletedPaths, "added.txt") || !containsString(response.DeletedPaths, "untracked.txt") {
		t.Fatalf("expected new files in deleted list, got %+v", response)
	}
	for _, want := range []struct {
		path    string
		content string
	}{{"modified.txt", "base\n"}, {"staged.txt", "base\n"}, {"kept.txt", "base\nmust stay\n"}} {
		content, err := os.ReadFile(filepath.Join(repo, want.path))
		if err != nil {
			t.Fatal(err)
		}
		if normalizedGitTestContent(content) != want.content {
			t.Fatalf("expected %s to contain %q, got %q", want.path, want.content, string(content))
		}
	}
	for _, gone := range []string{"added.txt", "untracked.txt"} {
		if _, err := os.Stat(filepath.Join(repo, gone)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted, stat err=%v", gone, err)
		}
	}
	if response.Status == nil || response.Status.Clean || len(response.Status.Files) != 1 || response.Status.Files[0].Path != "kept.txt" {
		t.Fatalf("expected only the unselected change to remain, got %+v", response.Status)
	}
}

func TestGitDiscardRouteRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "base\nchanged\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/discard", strings.NewReader(`{"paths":["tracked.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if normalizedGitTestContent(content) != "base\nchanged\n" {
		t.Fatalf("unconfirmed discard must not touch files, got %q", string(content))
	}
}

// A stale client selection must fail loudly rather than silently discarding
// nothing, mirroring the commit route's contract.
func TestGitDiscardRouteRejectsSelectionWithoutChanges(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/discard", strings.NewReader(`{"confirm":true,"paths":["tracked.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

// A successful rollback rewrites the workspace underneath the conversation, so
// the transcript has to record it: without the durable notice, the next model
// turn reasons from file states the rollback just erased.
func TestRollbackRunRecordsConversationNotice(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	recordRunGitCheckpoint(t, ctx, store, run.ID, repo, baseHead)
	writeGitTestFile(t, repo, "tracked.txt", "run change\n")
	writeGitTestFile(t, repo, "owned/new.txt", "created by run\n")
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	messages, err := store.ListMessages(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var notice *db.Message
	for index := range messages {
		if messages[index].Role == "system" {
			notice = &messages[index]
		}
	}
	if notice == nil {
		t.Fatalf("expected a durable system notice after rollback, got %d messages", len(messages))
	}
	if notice.RunID != run.ID || !strings.Contains(notice.ContentText, "tracked.txt") || !strings.Contains(notice.ContentText, "owned/new.txt") || !strings.Contains(notice.ContentText, baseHead[:8]) {
		t.Fatalf("unexpected rollback notice: %+v", notice)
	}
}

// Merging only flips database status; cleanup is the explicit second step that
// removes the fork's worktree directory and branch and clears the recorded
// paths so nothing keeps resolving git operations against them.
func TestWorklineCleanupRemovesWorktreeAndBranchAfterMerge(t *testing.T) {
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
	fork := forkWorklineForTest(t, app, root.ID, "feature/cleanup-after-merge")
	writeGitTestFile(t, fork.Workline.WorktreePath, "feature.txt", "feature\n")
	runGitTestCommand(t, fork.Workline.WorktreePath, "add", "feature.txt")
	runGitTestCommand(t, fork.Workline.WorktreePath, "commit", "-m", "feature")

	mergeRecorder := httptest.NewRecorder()
	mergeRequest := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/merge", strings.NewReader(`{"targetWorklineId":"`+root.ID+`"}`))
	mergeRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(mergeRecorder, mergeRequest)
	if mergeRecorder.Code != http.StatusOK {
		t.Fatalf("expected merge 200, got %d: %s", mergeRecorder.Code, mergeRecorder.Body.String())
	}

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/cleanup", strings.NewReader(`{"confirm":true}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected cleanup 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineCleanupResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.RemovedWorktree || !response.DeletedBranch || len(response.Warnings) != 0 {
		t.Fatalf("unexpected cleanup response: %+v", response)
	}
	if _, err := os.Stat(fork.Workline.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree directory to be removed, stat err=%v", err)
	}
	if branches := strings.TrimSpace(runGitTestOutput(t, repo, "branch", "--list", "feature/cleanup-after-merge")); branches != "" {
		t.Fatalf("expected fork branch to be deleted, got %q", branches)
	}
	stored, err := store.GetWorkline(ctx, fork.Workline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.WorktreePath != "" || stored.Status != "merged" {
		t.Fatalf("expected cleaned workline record, got %+v", stored)
	}
	agent, err := store.GetAgent(ctx, fork.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.CWD != "" {
		t.Fatalf("expected fork agent cwd to be cleared, got %q", agent.CWD)
	}

	repeated := httptest.NewRecorder()
	repeatedRequest := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/cleanup", strings.NewReader(`{"confirm":true}`))
	repeatedRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(repeated, repeatedRequest)
	if repeated.Code != http.StatusConflict {
		t.Fatalf("expected repeated cleanup 409, got %d: %s", repeated.Code, repeated.Body.String())
	}
}

func TestWorklineCleanupRejectsUnmergedWorkline(t *testing.T) {
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
	fork := forkWorklineForTest(t, app, root.ID, "feature/still-active")

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/cleanup", strings.NewReader(`{"confirm":true}`))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(fork.Workline.WorktreePath); err != nil {
		t.Fatalf("unmerged workline's worktree must survive, stat err=%v", err)
	}
}

// Deleting an archived project removes its database rows; the fork worktrees
// and branches it created have to go with it or they leak forever.
func TestDeleteArchivedProjectCleansForkWorktreesAndBranches(t *testing.T) {
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
	project, root, _, err := store.CreateProject(ctx, "Doomed", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, "feature/doomed-with-project")

	archived := true
	if _, err := store.UpdateProjectNavigationState(ctx, project.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodDelete, "/api/projects/"+project.ID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(fork.Workline.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected fork worktree to be removed with the project, stat err=%v", err)
	}
	if branches := strings.TrimSpace(runGitTestOutput(t, repo, "branch", "--list", "feature/doomed-with-project")); branches != "" {
		t.Fatalf("expected fork branch to be deleted with the project, got %q", branches)
	}
	// The main repository itself is user data and must never be deleted.
	if _, err := os.Stat(filepath.Join(repo, "README.md")); err != nil {
		t.Fatalf("project repository must survive deletion, stat err=%v", err)
	}
}

// Deleting the last archived conversation of a fork workline retires the
// workline itself: worktree, branch, and finally the now-empty row.
func TestDeleteArchivedAgentCleansEmptyForkWorkline(t *testing.T) {
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
	fork := forkWorklineForTest(t, app, root.ID, "feature/doomed-conversation")

	archived := true
	if _, err := store.UpdateAgentNavigationState(ctx, fork.Agent.ID, nil, &archived); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodDelete, "/api/agents/"+fork.Agent.ID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(fork.Workline.WorktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected fork worktree to be removed with its last conversation, stat err=%v", err)
	}
	if branches := strings.TrimSpace(runGitTestOutput(t, repo, "branch", "--list", "feature/doomed-conversation")); branches != "" {
		t.Fatalf("expected fork branch to be deleted, got %q", branches)
	}
	if _, err := store.GetWorkline(ctx, fork.Workline.ID); err == nil {
		t.Fatal("expected the empty fork workline row to be deleted")
	}
	// The mainline workline and repository stay.
	if _, err := store.GetWorkline(ctx, root.ID); err != nil {
		t.Fatalf("root workline must survive, err=%v", err)
	}
}
