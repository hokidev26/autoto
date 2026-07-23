package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"testing"
)

type fakeBackgroundTaskService struct {
	submitted []BackgroundTaskRequest
	tasks     map[string]BackgroundTask
	outputs   map[string]BackgroundTaskOutputPage
	canceled  string
}

func (f *fakeBackgroundTaskService) Submit(_ context.Context, request BackgroundTaskRequest) (BackgroundTask, error) {
	f.submitted = append(f.submitted, request)
	task := BackgroundTask{ID: "task-1", OwnerAgentID: request.OwnerAgentID, ParentRunID: request.ParentRunID, ParentToolUseID: request.ParentToolUseID, Kind: request.Kind, Status: "queued", Revision: 1, ResumeParent: request.ResumeParent}
	if f.tasks == nil {
		f.tasks = make(map[string]BackgroundTask)
	}
	f.tasks[task.ID] = task
	return task, nil
}

func (f *fakeBackgroundTaskService) List(_ context.Context, options BackgroundTaskListOptions) ([]BackgroundTask, error) {
	out := make([]BackgroundTask, 0, len(f.tasks))
	for _, task := range f.tasks {
		if options.OwnerAgentID != "" && task.OwnerAgentID != options.OwnerAgentID {
			continue
		}
		out = append(out, task)
	}
	return out, nil
}

func (f *fakeBackgroundTaskService) Get(_ context.Context, ownerAgentID, id string) (BackgroundTask, error) {
	task := f.tasks[id]
	if task.OwnerAgentID != ownerAgentID {
		return BackgroundTask{}, context.Canceled
	}
	return task, nil
}

func (f *fakeBackgroundTaskService) Output(_ context.Context, ownerAgentID, id string, _ int64, _ int) (BackgroundTaskOutputPage, error) {
	if task := f.tasks[id]; task.OwnerAgentID != ownerAgentID {
		return BackgroundTaskOutputPage{}, context.Canceled
	}
	return f.outputs[id], nil
}

func (f *fakeBackgroundTaskService) Wait(ctx context.Context, ownerAgentID, id string, _ int64) (BackgroundTask, error) {
	return f.Get(ctx, ownerAgentID, id)
}

func (f *fakeBackgroundTaskService) Cancel(_ context.Context, ownerAgentID, id string) (BackgroundTask, error) {
	task := f.tasks[id]
	if task.OwnerAgentID != ownerAgentID {
		return BackgroundTask{}, context.Canceled
	}
	f.canceled = id
	task.Status = "cancel_requested"
	f.tasks[id] = task
	return task, nil
}

func TestBashBackgroundSubmitsDurableTask(t *testing.T) {
	service := &fakeBackgroundTaskService{}
	input := json.RawMessage(`{"command":"printf safe","timeout":5000,"run_in_background":true,"resume_parent":true}`)
	result, err := (BashTool{}).Execute(context.Background(), Call{ID: "bash-1", Name: "Bash", Input: input}, Env{
		AgentID: "agent-1", RunID: "run-1", CWD: t.TempDir(), Background: service,
		PermissionGenerationSnapshot: 2, PolicyGenerationSnapshot: 3, AgentGenerationSnapshot: 4,
	})
	if err != nil || result.IsError {
		t.Fatalf("unexpected result=%+v err=%v", result, err)
	}
	if len(service.submitted) != 1 {
		t.Fatalf("expected one task submission, got %d", len(service.submitted))
	}
	request := service.submitted[0]
	if request.Kind != BackgroundTaskKindShell || request.OwnerAgentID != "agent-1" || request.ParentRunID != "run-1" || request.ParentToolUseID != "bash-1" || !request.ResumeParent {
		t.Fatalf("unexpected request: %+v", request)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil || payload["command"] != "printf safe" {
		t.Fatalf("unexpected payload=%s err=%v", request.Payload, err)
	}
}

func TestBashBackgroundRejectsShellEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("these are POSIX shell escape forms; BashTool executes cmd.exe on Windows")
	}
	for _, command := range []string{"sleep 10 &", "nohup sleep 10", "sleep 10; disown"} {
		input, _ := json.Marshal(bashInput{Command: command, RunInBackground: true})
		if risk := (BashTool{}).Risk(input); risk != RiskDanger {
			t.Fatalf("expected danger risk for %q, got %s", command, risk)
		}
		result, err := (BashTool{}).Execute(context.Background(), Call{ID: "escape", Name: "Bash", Input: input}, Env{Background: &fakeBackgroundTaskService{}})
		if err != nil || !result.IsError {
			t.Fatalf("expected safe rejection for %q, result=%+v err=%v", command, result, err)
		}
	}
}

func TestAgentResumeParentRequiresDurableRun(t *testing.T) {
	service := &fakeBackgroundTaskService{}
	result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-call", Name: "Agent", Input: json.RawMessage(`{"prompt":"inspect","resume_parent":true}`)}, Env{AgentID: "parent", CWD: t.TempDir(), Background: service})
	if err != nil || !result.IsError || len(service.submitted) != 0 {
		t.Fatalf("expected resume_parent rejection without a run, result=%+v err=%v requests=%+v", result, err, service.submitted)
	}
}

func TestAgentAndTaskToolsUseBackgroundService(t *testing.T) {
	service := &fakeBackgroundTaskService{}
	agentInput := json.RawMessage(`{"prompt":"inspect tests","description":"test audit","run_in_background":true}`)
	result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-call", Name: "Agent", Input: agentInput}, Env{AgentID: "parent", RunID: "run", CWD: t.TempDir(), Background: service})
	if err != nil || result.IsError || len(service.submitted) != 1 || service.submitted[0].Kind != BackgroundTaskKindAgent {
		t.Fatalf("unexpected agent task result=%+v err=%v requests=%+v", result, err, service.submitted)
	}
	service.outputs = map[string]BackgroundTaskOutputPage{"task-1": {TaskID: "task-1", NextSequence: 2, Chunks: []BackgroundTaskOutputChunk{{Sequence: 1, Text: "ok"}}}}
	output, err := (TaskTool{}).Execute(context.Background(), Call{ID: "task-output", Name: "Task", Input: json.RawMessage(`{"action":"output","task_id":"task-1"}`)}, Env{AgentID: "parent", Background: service})
	if err != nil || output.IsError || output.Output == "" {
		t.Fatalf("unexpected task output=%+v err=%v", output, err)
	}
	cancel, err := (TaskTool{}).Execute(context.Background(), Call{ID: "task-cancel", Name: "Task", Input: json.RawMessage(`{"action":"cancel","task_id":"task-1"}`)}, Env{AgentID: "parent", Background: service})
	if err != nil || cancel.IsError || service.canceled != "task-1" {
		t.Fatalf("unexpected cancel=%+v err=%v canceled=%q", cancel, err, service.canceled)
	}
	if risk := (TaskTool{}).Risk(json.RawMessage(`{"action":"cancel"}`)); risk != RiskExec {
		t.Fatalf("expected cancel risk exec, got %s", risk)
	}
}
