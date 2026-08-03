package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/tools"
)

type fakeServerBackgroundTasks struct {
	tasks    map[string]tools.BackgroundTask
	output   tools.BackgroundTaskOutputPage
	canceled string
}

func (f *fakeServerBackgroundTasks) Submit(context.Context, tools.BackgroundTaskRequest) (tools.BackgroundTask, error) {
	return tools.BackgroundTask{}, context.Canceled
}
func (f *fakeServerBackgroundTasks) List(_ context.Context, options tools.BackgroundTaskListOptions) ([]tools.BackgroundTask, error) {
	var out []tools.BackgroundTask
	for _, task := range f.tasks {
		if options.OwnerAgentID != "" && task.OwnerAgentID != options.OwnerAgentID {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}
func (f *fakeServerBackgroundTasks) Get(_ context.Context, ownerAgentID, id string) (tools.BackgroundTask, error) {
	task, ok := f.tasks[id]
	if !ok || ownerAgentID != "" && task.OwnerAgentID != ownerAgentID {
		return tools.BackgroundTask{}, context.Canceled
	}
	return task, nil
}
func (f *fakeServerBackgroundTasks) Output(_ context.Context, ownerAgentID, id string, _ int64, _ int) (tools.BackgroundTaskOutputPage, error) {
	if _, err := f.Get(context.Background(), ownerAgentID, id); err != nil {
		return tools.BackgroundTaskOutputPage{}, err
	}
	return f.output, nil
}
func (f *fakeServerBackgroundTasks) Wait(_ context.Context, ownerAgentID, id string, _ int64) (tools.BackgroundTask, error) {
	return f.Get(context.Background(), ownerAgentID, id)
}
func (f *fakeServerBackgroundTasks) Cancel(_ context.Context, ownerAgentID, id string) (tools.BackgroundTask, error) {
	task, err := f.Get(context.Background(), ownerAgentID, id)
	if err != nil {
		return tools.BackgroundTask{}, err
	}
	f.canceled = id
	task.Status = "cancel_requested"
	f.tasks[id] = task
	return task, nil
}

func TestBackgroundTaskRoutesAreScopedAndBounded(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Tasks", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeServerBackgroundTasks{tasks: map[string]tools.BackgroundTask{
		"task-1": {ID: "task-1", OwnerAgentID: agent.ID, Kind: "shell", Status: "running", Revision: 2, PublicSummary: json.RawMessage(`{"program":"go"}`)},
	}, output: tools.BackgroundTaskOutputPage{TaskID: "task-1", NextSequence: 2, Chunks: []tools.BackgroundTaskOutputChunk{{Sequence: 1, Stream: "stdout", Text: "ok", ByteCount: 2}}}}
	app := New(config.Config{}, store, nil, nil)
	app.SetBackgroundTaskService(service)
	routes := app.Routes()

	response := httptest.NewRecorder()
	routes.ServeHTTP(response, newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/background-tasks?limit=10", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "task-1") {
		t.Fatalf("unexpected list response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	routes.ServeHTTP(response, newTestRequest(http.MethodGet, "/api/background-tasks/task-1/output?afterSequence=0&limitBytes=64", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"text":"ok"`) {
		t.Fatalf("unexpected output response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	routes.ServeHTTP(response, newTestRequest(http.MethodPost, "/api/background-tasks/task-1/wait", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "task-1") {
		t.Fatalf("unexpected empty-body wait response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := newTestRequest(http.MethodPost, "/api/background-tasks/task-1/cancel", nil)
	request.Header.Set(localTokenHeader, app.localToken)
	routes.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.canceled != "task-1" || !strings.Contains(response.Body.String(), "cancel_requested") {
		t.Fatalf("unexpected cancel response: %d %s canceled=%q", response.Code, response.Body.String(), service.canceled)
	}
}

func TestBackgroundTaskSensitiveRoutesRequireCanonicalToken(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Tasks", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	service := &fakeServerBackgroundTasks{tasks: map[string]tools.BackgroundTask{
		"task-1": {ID: "task-1", OwnerAgentID: agent.ID, Kind: "shell", Status: "running", Revision: 1},
	}}
	app := New(config.Config{}, store, nil, nil)
	app.SetBackgroundTaskService(service)

	response := httptest.NewRecorder()
	app.Routes().ServeHTTP(response, newTestRequest(http.MethodPost, "/api/background-tasks/task-1/cancel", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected canonical local token requirement, got %d: %s", response.Code, response.Body.String())
	}
}

func TestBackgroundTaskRequestErrorPreservesStructuredToolFailure(t *testing.T) {
	result := backgroundTaskRequestError(tools.Result{
		Output:  `{"errorCode":"subagent_model_rejected","errorMessage":"requested model is unavailable"}`,
		IsError: true,
		Meta:    map[string]any{"errorCode": "stale_code", "errorMessage": "stale message"},
	})
	if result.ErrorCode != "subagent_model_rejected" || result.ErrorMessage != "requested model is unavailable" || result.Error != result.ErrorMessage {
		t.Fatalf("unexpected structured background task error: %+v", result)
	}
}

func TestBackgroundTaskRequestErrorUsesRedactedPlainToolFailure(t *testing.T) {
	result := backgroundTaskRequestError(tools.Result{Output: "tool call requires approval", IsError: true})
	if result.ErrorCode != "background_task_rejected" || result.ErrorMessage != "tool call requires approval" || result.Error != result.ErrorMessage {
		t.Fatalf("unexpected fallback background task error: %+v", result)
	}
}
