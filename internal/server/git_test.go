package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	agentpkg "autoto/internal/agent"
	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/gitsnapshot"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type scopedCheckpointProvider struct {
	mu            sync.Mutex
	calls         int
	repo          string
	sideEffectErr error
}

func (p *scopedCheckpointProvider) Name() string { return "openai" }

func (p *scopedCheckpointProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Tools: true, Streaming: true, ImageInput: true}
}

func (p *scopedCheckpointProvider) ListModels(context.Context) ([]string, error) {
	return []string{"test"}, nil
}

func (p *scopedCheckpointProvider) Generate(_ context.Context, _ providers.GenerateRequest) (<-chan providers.Event, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	if call == 1 && p.sideEffectErr == nil {
		p.sideEffectErr = os.WriteFile(filepath.Join(p.repo, "concurrent-user.txt"), []byte("user\n"), 0o644)
	}
	err := p.sideEffectErr
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var events []providers.Event
	if call == 0 {
		events = []providers.Event{{Type: "tool_call", ToolCall: &providers.ToolCall{ID: "owned-write", Name: "Write", Input: json.RawMessage(`{"file_path":"owned.txt","content":"run\n"}`)}}, {Type: "done", Done: true}}
	} else {
		events = []providers.Event{{Type: "text", Text: "done"}, {Type: "done", Done: true}}
	}
	out := make(chan providers.Event, len(events))
	for _, event := range events {
		out <- event
	}
	close(out)
	return out, nil
}

func TestGitStatusRouteReturnsChangedFiles(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	writeGitTestFile(t, repo, "untracked file.txt", "new\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/status", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitStatusResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(body.RepoRoot) != filepath.Clean(repo) || body.Clean || len(body.Files) != 2 {
		t.Fatalf("unexpected status body: %+v", body)
	}
	if !gitStatusHasPath(body.Files, "tracked.txt") || !gitStatusHasPath(body.Files, "untracked file.txt") {
		t.Fatalf("expected tracked and untracked files, got %+v", body.Files)
	}
}

func TestGitDiffRouteReturnsPatchForPath(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/diff?scope=all&path=tracked.txt", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitDiffResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Patch, "diff --git") || !strings.Contains(body.Patch, "+two") {
		t.Fatalf("expected patch to contain diff and new line, got %q", body.Patch)
	}
	if len(body.Files) != 1 || body.Files[0].Path != "tracked.txt" || body.Files[0].Added == 0 {
		t.Fatalf("unexpected diff files: %+v", body.Files)
	}
}

func TestGitDiffRouteHandlesUnbornHead(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "new.txt", "new\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/diff?scope=all&path=new.txt", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitDiffResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Path != "new.txt" || body.Scope != "all" {
		t.Fatalf("unexpected diff body: %+v", body)
	}
}

func TestGitLogRouteReturnsCommitsAndBoundsLimit(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial subject")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/log?limit=9999", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitLogResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Commits) != 1 || body.Commits[0].Subject != "initial subject" || body.Commits[0].ShortHash == "" {
		t.Fatalf("unexpected log body: %+v", body)
	}
}

func TestGitCommitRouteCommitsSelectedPaths(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	writeGitTestFile(t, repo, "other.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt", "other.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	writeGitTestFile(t, repo, "other.txt", "base\nlocal\n")
	writeGitTestFile(t, repo, "new.txt", "new\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"commit selected paths","paths":["tracked.txt","new.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitCommitResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Commit.Subject != "commit selected paths" || body.Commit.ShortHash == "" {
		t.Fatalf("unexpected commit body: %+v", body)
	}
	status := runGitTestOutput(t, repo, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, "other.txt") {
		t.Fatalf("expected unselected file to remain dirty, got %q", status)
	}
	if strings.Contains(status, "tracked.txt") || strings.Contains(status, "new.txt") {
		t.Fatalf("expected selected files to be committed, got %q", status)
	}
}

func TestGitCommitRouteRejectsEmptyMessage(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"   ","paths":["tracked.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitCommitRouteRejectsEmptyPaths(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"commit","paths":[]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitCommitRouteRejectsEscapingPath(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"commit","paths":["../secret.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitCommitRouteRejectsSensitivePath(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, ".env", "TOKEN=secret\n")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"commit env","paths":[".env"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
	status := runGitTestOutput(t, repo, "status", "--porcelain=v1", "--untracked-files=all")
	if !strings.Contains(status, ".env") {
		t.Fatalf("expected sensitive file to remain untracked, got %q", status)
	}
}

func TestGitCommitRouteRejectsStagedOutsideSelection(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	writeGitTestFile(t, repo, "other.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt", "other.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	writeGitTestFile(t, repo, "other.txt", "base\nstaged\n")
	runGitTestCommand(t, repo, "add", "other.txt")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/git/commit", strings.NewReader(`{"message":"commit selected","paths":["tracked.txt"]}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	cached := runGitTestOutput(t, repo, "diff", "--cached", "--name-only")
	if !strings.Contains(cached, "other.txt") {
		t.Fatalf("expected pre-staged file to remain staged, got %q", cached)
	}
}

func TestRollbackRunRouteRestoresOnlyRecordedRunChanges(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	writeGitTestFile(t, repo, "removed.txt", "keep\n")
	runGitTestCommand(t, repo, "add", "tracked.txt", "removed.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	recordRunGitCheckpoint(t, ctx, store, run.ID, repo, baseHead)

	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	if err := os.Remove(filepath.Join(repo, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	writeGitTestFile(t, repo, "nested/agent-new.txt", "agent\n")
	runGitTestCommand(t, repo, "add", "tracked.txt", "removed.txt")
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)
	writeGitTestFile(t, repo, "nested/user-new.txt", "user\n")

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body gitRollbackResponse
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RunID != run.ID || body.BaseHead != baseHead || body.Status == nil || body.Status.Clean || len(body.Status.Files) != 1 || !gitStatusHasPath(body.Status.Files, "nested/user-new.txt") {
		t.Fatalf("unexpected rollback body: %+v", body)
	}
	for _, want := range []struct {
		path    string
		content string
	}{{"tracked.txt", "one\n"}, {"removed.txt", "keep\n"}, {"nested/user-new.txt", "user\n"}} {
		content, err := os.ReadFile(filepath.Join(repo, want.path))
		if err != nil {
			t.Fatal(err)
		}
		if normalizeGitTestContent(string(content)) != want.content {
			t.Fatalf("expected %s to contain %q, got %q", want.path, want.content, string(content))
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "nested/agent-new.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected only run-created untracked file to be removed, stat err=%v", err)
	}
}

func TestRollbackRunPreviewAndIdempotence(t *testing.T) {
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
	previewRecorder := httptest.NewRecorder()
	previewRequest := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", nil)
	app.Routes().ServeHTTP(previewRecorder, previewRequest)
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("expected preview 200, got %d: %s", previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview gitRollbackPreviewResponse
	if err := json.NewDecoder(previewRecorder.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if !preview.Available || preview.RestoreCount != 1 || preview.DeleteCount != 1 || len(preview.RestorePaths) != 1 || preview.RestorePaths[0] != "tracked.txt" || len(preview.DeletePaths) != 1 || preview.DeletePaths[0] != "owned/new.txt" {
		t.Fatalf("unexpected rollback preview: %+v", preview)
	}

	rollbackRecorder := httptest.NewRecorder()
	rollbackRequest := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(rollbackRecorder, rollbackRequest)
	if rollbackRecorder.Code != http.StatusOK {
		t.Fatalf("expected rollback 200, got %d: %s", rollbackRecorder.Code, rollbackRecorder.Body.String())
	}
	updated, err := store.GetRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CheckpointState != db.RunCheckpointRolledBack || updated.RolledBackAt == "" {
		t.Fatalf("expected durable rolled back state, got %+v", updated)
	}

	repeatedRecorder := httptest.NewRecorder()
	repeatedRequest := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(repeatedRecorder, repeatedRequest)
	if repeatedRecorder.Code != http.StatusConflict || !strings.Contains(repeatedRecorder.Body.String(), "already rolled back") {
		t.Fatalf("expected repeated rollback conflict, got %d: %s", repeatedRecorder.Code, repeatedRecorder.Body.String())
	}
}

func TestRollbackRunRouteConcurrentPostClaimsOnce(t *testing.T) {
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
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)
	app := New(config.Config{}, store, nil, nil)
	start := make(chan struct{})
	results := make(chan int, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			recorder := httptest.NewRecorder()
			request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
			app.Routes().ServeHTTP(recorder, request)
			results <- recorder.Code
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for status := range results {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected concurrent rollback status %d", status)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected one rollback claim winner and one conflict, successes=%d conflicts=%d", successes, conflicts)
	}
	updated, err := store.GetRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CheckpointState != db.RunCheckpointRolledBack {
		t.Fatalf("expected single successful rollback state, got %+v", updated)
	}
	content, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil || normalizeGitTestContent(string(content)) != "base\n" {
		t.Fatalf("expected exactly one rollback restore, content=%q err=%v", string(content), err)
	}
}

func TestGitRollbackPreviewTruncatesPaths(t *testing.T) {
	paths := make([]string, gitRollbackPreviewMaxPaths+1)
	for i := range paths {
		paths[i] = "file-" + strconv.Itoa(i)
	}
	preview := gitRollbackPreview("/repo", "run", gitRollbackPlan{available: true, restorePaths: paths})
	if !preview.Truncated || preview.RestoreCount != len(paths) || len(preview.RestorePaths) != gitRollbackPreviewMaxPaths {
		t.Fatalf("expected bounded preview paths, got %+v", preview)
	}
}

func TestRollbackRunRoutePreservesUserFileCreatedOutsideToolWindow(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "base\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, Role: "user", ContentText: "create owned file"}); err != nil {
		t.Fatal(err)
	}
	provider := &scopedCheckpointProvider{repo: repo}
	providerRegistry := providers.NewRegistry()
	providerRegistry.Register(provider)
	toolRegistry := tools.NewRegistry()
	tools.RegisterCore(toolRegistry)
	runner := agentpkg.NewRunner(store, providerRegistry, toolRegistry, agentpkg.NewHub(), config.AgentConfig{MaxTurns: 3})
	runner.Run(ctx, agent.ID)
	if provider.sideEffectErr != nil {
		t.Fatal(provider.sideEffectErr)
	}
	runs, err := store.ListRuns(ctx, agent.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].GitSnapshotAt == "" {
		t.Fatalf("expected completed run snapshot, got %+v", runs)
	}
	changes, err := store.ListRunGitChanges(ctx, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Path != "owned.txt" {
		t.Fatalf("expected only tool-owned path in checkpoint, got %+v", changes)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+runs[0].ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "owned.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected owned tool file to be removed, stat err=%v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "concurrent-user.txt"))
	if err != nil || normalizeGitTestContent(string(content)) != "user\n" {
		t.Fatalf("expected concurrent user file preserved, content=%q err=%v", string(content), err)
	}
}

func TestRollbackRunRouteRestoresRenameRecordedWithoutOrigPath(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "old.txt", "base\n")
	runGitTestCommand(t, repo, "add", "old.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	recordRunGitCheckpoint(t, ctx, store, run.ID, repo, baseHead)
	runGitTestCommand(t, repo, "mv", "old.txt", "renamed.txt")
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)
	changes, err := store.ListRunGitChanges(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].OrigPath != "" || changes[1].OrigPath != "" {
		t.Fatalf("expected no-renames snapshot to persist separate paths, got %+v", changes)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(repo, "old.txt"))
	if err != nil || normalizeGitTestContent(string(content)) != "base\n" {
		t.Fatalf("expected original path restored, content=%q err=%v", string(content), err)
	}
	if _, err := os.Stat(filepath.Join(repo, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected renamed path removed, stat err=%v", err)
	}
}

func TestRollbackRunRouteRejectsModeChangeAfterCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve git mode changes reliably")
	}
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
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)
	if err := os.Chmod(filepath.Join(repo, "tracked.txt"), 0o755); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "contents or mode changed") {
		t.Fatalf("expected mode conflict, got %d: %s", recorder.Code, recorder.Body.String())
	}
	info, err := os.Stat(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("rollback changed later file mode: %#o", info.Mode().Perm())
	}
}

func TestRollbackRunRouteRejectsRunPathModifiedAfterCompletion(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
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
	writeGitTestFile(t, repo, "tracked.txt", "one\nrun change\n")
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)
	writeGitTestFile(t, repo, "tracked.txt", "one\nuser follow-up\n")

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "changed after the run completed") {
		t.Fatalf("expected conflict explanation, got %s", recorder.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(repo, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if normalizeGitTestContent(string(content)) != "one\nuser follow-up\n" {
		t.Fatalf("rollback overwrote later user change: %q", string(content))
	}
}

func TestRollbackRunRouteRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed", BaseHead: baseHead, EndHead: baseHead})
	if err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":false}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRollbackRunRouteRejectsMissingCheckpoint(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRollbackRunRouteRejectsMissingScopedSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
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

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "still tracking") {
		t.Fatalf("expected tracking checkpoint conflict, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRollbackRunRouteRejectsCurrentHeadMismatch(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "second")
	currentHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	recordRunGitCheckpoint(t, ctx, store, run.ID, repo, baseHead)
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, baseHead)

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if head := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD")); head != currentHead {
		t.Fatalf("expected HEAD to remain at %s, got %s", currentHead, head)
	}
}

func TestRollbackRunRouteRejectsCommitChangingRun(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	baseHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	writeGitTestFile(t, repo, "tracked.txt", "one\ntwo\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "second")
	endHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	recordRunGitCheckpoint(t, ctx, store, run.ID, repo, baseHead)
	recordRunGitSnapshot(t, ctx, store, run.ID, repo, endHead)

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/rollback", strings.NewReader(`{"confirm":true}`))
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
	currentHead := strings.TrimSpace(runGitTestOutput(t, repo, "rev-parse", "HEAD"))
	if currentHead != endHead {
		t.Fatalf("expected HEAD to remain at %s, got %s", endHead, currentHead)
	}
}

func TestGitStatusRouteRejectsRepoOutsideProjectBoundary(t *testing.T) {
	ctx := context.Background()
	projectRoot := t.TempDir()
	outsideRepo := newGitTestRepo(t)
	writeGitTestFile(t, outsideRepo, "tracked.txt", "one\n")
	runGitTestCommand(t, outsideRepo, "add", "tracked.txt")
	runGitTestCommand(t, outsideRepo, "commit", "-m", "outside")
	store, agent := newGitRouteStore(t, ctx, projectRoot)
	defer store.Close()
	if _, err := store.UpdateAgentCWD(ctx, agent.ID, outsideRepo); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/status", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitStatusRouteAllowsRepoUnderDefaultProjectDir(t *testing.T) {
	ctx := context.Background()
	defaultRoot := t.TempDir()
	repo := filepath.Join(defaultRoot, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitTestCommand(t, repo, "init", "-b", "main")
	runGitTestCommand(t, repo, "config", "user.name", "Autoto Test")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.com")
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "inside")
	projectRoot := t.TempDir()
	store, agent := newGitRouteStore(t, ctx, projectRoot)
	defer store.Close()
	if _, err := store.UpdateAgentCWD(ctx, agent.ID, repo); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{Paths: config.PathsConfig{DefaultProjectDir: defaultRoot}}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/status", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitStatusRouteRejectsNonGitRepo(t *testing.T) {
	ctx := context.Background()
	store, agent := newGitRouteStore(t, ctx, t.TempDir())
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/status", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGitDiffRouteRejectsEscapingPath(t *testing.T) {
	ctx := context.Background()
	repo := newGitTestRepo(t)
	writeGitTestFile(t, repo, "tracked.txt", "one\n")
	runGitTestCommand(t, repo, "add", "tracked.txt")
	runGitTestCommand(t, repo, "commit", "-m", "initial")
	store, agent := newGitRouteStore(t, ctx, repo)
	defer store.Close()

	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/git/diff?path=../secret.txt", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func newGitRouteStore(t *testing.T, ctx context.Context, repo string) (*db.Store, db.Agent) {
	t.Helper()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", repo, "openai:test", "acceptEdits")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, agent
}

func newGitTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	resolved, err := filepath.EvalSymlinks(repo)
	if err == nil {
		repo = resolved
	}
	runGitTestCommand(t, repo, "init", "-b", "main")
	runGitTestCommand(t, repo, "config", "user.name", "Autoto Test")
	runGitTestCommand(t, repo, "config", "user.email", "test@example.com")
	return repo
}

func writeGitTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGitTestCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}

func runGitTestOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func recordRunGitCheckpoint(t *testing.T, ctx context.Context, store *db.Store, runID, repo, baseHead string) {
	t.Helper()
	if err := store.BeginRunGitCheckpoint(ctx, runID, baseHead, repo); err != nil {
		t.Fatal(err)
	}
}

func recordRunGitSnapshot(t *testing.T, ctx context.Context, store *db.Store, runID, repo, endHead string) {
	t.Helper()
	statusOut := runGitTestOutput(t, repo, "status", "--porcelain=v1", "-z", "--no-renames", "--untracked-files=all")
	files := parseGitPorcelainStatus(statusOut)
	changes := make([]db.RunGitChange, 0, len(files))
	for _, file := range files {
		indexFingerprint, err := gitRunIndexFingerprint(ctx, repo, file.Path)
		if err != nil {
			t.Fatal(err)
		}
		worktreeFingerprint, err := gitsnapshot.WorktreeFingerprint(repo, file.Path)
		if err != nil {
			t.Fatal(err)
		}
		changes = append(changes, db.RunGitChange{RunID: runID, Path: file.Path, OrigPath: file.OrigPath, IndexStatus: file.Index, WorktreeStatus: file.Worktree, Untracked: file.Untracked, IndexFingerprint: indexFingerprint, WorktreeFingerprint: worktreeFingerprint})
	}
	if err := store.MarkRunGitCheckpointCapturing(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceRunGitCheckpointChanges(ctx, runID, changes); err != nil {
		t.Fatal(err)
	}
	ready, err := store.FinalizeRunGitCheckpoint(ctx, runID, endHead)
	if err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("expected checkpoint tracking state to finalize")
	}
}

func normalizeGitTestContent(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func gitStatusHasPath(files []gitStatusFile, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}
