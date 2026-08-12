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

func setupMergedForkForUnmerge(t *testing.T, branch string) (*Server, *db.Store, string, db.Workline, forkWorklineResponse, string) {
	t.Helper()
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "README.md", "base\n")
	runGitTestCommand(t, repo, "add", "README.md")
	runGitTestCommand(t, repo, "commit", "-m", "base")
	preMergeHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))

	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, root, _, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{Agent: config.AgentConfig{DefaultModel: "openai:test", DefaultPermissionMode: "acceptEdits"}}, store, nil, nil)
	fork := forkWorklineForTest(t, app, root.ID, branch)
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
	return app, store, repo, root, fork, preMergeHead
}

func postWorklineUnmerge(t *testing.T, app *Server, worklineID, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/worklines/"+worklineID+"/unmerge", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(recorder, request)
	return recorder
}

// While the merge commit is still the target's tip, undoing it is a pure
// rewind: the target returns to its recorded pre-merge head and the fork goes
// back to active with its branch and worktree untouched, ready to continue.
func TestWorklineUnmergeResetsWhenMergeIsTargetHead(t *testing.T) {
	ctx := context.Background()
	app, store, repo, _, fork, preMergeHead := setupMergedForkForUnmerge(t, "feature/unmerge-reset")

	recorder := postWorklineUnmerge(t, app, fork.Workline.ID, `{"confirm":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineUnmergeResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Strategy != "reset" || response.NewTargetHead != preMergeHead {
		t.Fatalf("expected a reset back to %s, got %+v", preMergeHead, response)
	}
	if head := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD")); head != preMergeHead {
		t.Fatalf("expected target head %s, got %s", preMergeHead, head)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected the merged file to leave the target, stat err=%v", err)
	}
	stored, err := store.GetWorkline(ctx, fork.Workline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "active" || stored.MergeCommitSHA != "" || stored.MergedIntoWorklineID != "" || stored.PreMergeTargetSHA != "" {
		t.Fatalf("expected the fork to return to active with cleared merge bookkeeping, got %+v", stored)
	}
	// The branch and worktree survive so the fork can be fixed and re-merged.
	if _, err := os.Stat(filepath.Join(fork.Workline.WorktreePath, "feature.txt")); err != nil {
		t.Fatalf("fork worktree must survive an unmerge, stat err=%v", err)
	}
	if branches := strings.TrimSpace(runGitTestOutput(t, repo, "branch", "--list", "feature/unmerge-reset")); branches == "" {
		t.Fatal("fork branch must survive an unmerge")
	}
}

// Once the target has commits on top of the merge, rewinding would destroy
// them, so the undo becomes a revert commit: the merged content disappears but
// the later work stays.
func TestWorklineUnmergeRevertsWhenTargetMovedOn(t *testing.T) {
	ctx := context.Background()
	app, store, repo, _, fork, preMergeHead := setupMergedForkForUnmerge(t, "feature/unmerge-revert")
	writeGitTestFile(t, repo, "later.txt", "after the merge\n")
	runGitTestCommand(t, repo, "add", "later.txt")
	runGitTestCommand(t, repo, "commit", "-m", "later work")

	recorder := postWorklineUnmerge(t, app, fork.Workline.ID, `{"confirm":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response worklineUnmergeResponse
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Strategy != "revert" || response.NewTargetHead == preMergeHead || response.NewTargetHead == "" {
		t.Fatalf("expected a revert commit on top of the target, got %+v", response)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected the merged file to be reverted away, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "later.txt")); err != nil {
		t.Fatalf("commits made after the merge must survive, stat err=%v", err)
	}
	stored, err := store.GetWorkline(ctx, fork.Workline.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "active" || stored.MergeCommitSHA != "" {
		t.Fatalf("expected the fork to return to active, got %+v", stored)
	}
}

func TestWorklineUnmergeRequiresConfirmation(t *testing.T) {
	app, _, repo, _, fork, _ := setupMergedForkForUnmerge(t, "feature/unmerge-confirm")

	recorder := postWorklineUnmerge(t, app, fork.Workline.ID, `{}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("unconfirmed unmerge must not touch the target, stat err=%v", err)
	}
}

// After cleanup the fork's branch and worktree are gone, so the workline
// cannot return to active work and a reset would strand the merged commits
// without any named ref. The endpoint refuses instead of guessing.
func TestWorklineUnmergeRejectsCleanedWorkline(t *testing.T) {
	app, _, repo, _, fork, _ := setupMergedForkForUnmerge(t, "feature/unmerge-cleaned")

	cleanupRecorder := httptest.NewRecorder()
	cleanupRequest := newTestRequest(http.MethodPost, "/api/worklines/"+fork.Workline.ID+"/cleanup", strings.NewReader(`{"confirm":true}`))
	cleanupRequest.Header.Set("Content-Type", "application/json")
	app.Routes().ServeHTTP(cleanupRecorder, cleanupRequest)
	if cleanupRecorder.Code != http.StatusOK {
		t.Fatalf("expected cleanup 200, got %d: %s", cleanupRecorder.Code, cleanupRecorder.Body.String())
	}

	recorder := postWorklineUnmerge(t, app, fork.Workline.ID, `{"confirm":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("rejected unmerge must not touch the target, stat err=%v", err)
	}
}

// A dirty target worktree would be clobbered by both reset and revert, so the
// unmerge refuses until the target is clean, mirroring the merge's contract.
func TestWorklineUnmergeRejectsDirtyTarget(t *testing.T) {
	app, _, repo, _, fork, _ := setupMergedForkForUnmerge(t, "feature/unmerge-dirty")
	writeGitTestFile(t, repo, "wip.txt", "uncommitted work\n")

	recorder := postWorklineUnmerge(t, app, fork.Workline.ID, `{"confirm":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("rejected unmerge must not touch the target, stat err=%v", err)
	}
	if content, err := os.ReadFile(filepath.Join(repo, "wip.txt")); err != nil || !strings.Contains(string(content), "uncommitted work") {
		t.Fatalf("uncommitted target work must survive, err=%v content=%q", err, string(content))
	}
}
