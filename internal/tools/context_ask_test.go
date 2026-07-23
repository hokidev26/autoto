package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type contextAskBackgroundService struct {
	task     BackgroundTask
	getErr   error
	getCalls int
	ownerID  string
	taskID   string
	getFunc  func(context.Context, string, string) (BackgroundTask, error)
}

func (f *contextAskBackgroundService) Submit(context.Context, BackgroundTaskRequest) (BackgroundTask, error) {
	return BackgroundTask{}, errors.New("unexpected Submit call")
}

func (f *contextAskBackgroundService) List(context.Context, BackgroundTaskListOptions) ([]BackgroundTask, error) {
	return nil, errors.New("unexpected List call")
}

func (f *contextAskBackgroundService) Get(ctx context.Context, ownerAgentID, taskID string) (BackgroundTask, error) {
	f.getCalls++
	f.ownerID = ownerAgentID
	f.taskID = taskID
	if f.getFunc != nil {
		return f.getFunc(ctx, ownerAgentID, taskID)
	}
	return f.task, f.getErr
}

func (f *contextAskBackgroundService) Output(context.Context, string, string, int64, int) (BackgroundTaskOutputPage, error) {
	return BackgroundTaskOutputPage{}, errors.New("unexpected Output call")
}

func (f *contextAskBackgroundService) Wait(context.Context, string, string, int64) (BackgroundTask, error) {
	return BackgroundTask{}, errors.New("unexpected Wait call")
}

func (f *contextAskBackgroundService) Cancel(context.Context, string, string) (BackgroundTask, error) {
	return BackgroundTask{}, errors.New("unexpected Cancel call")
}

func contextAskInt(value int) *int {
	return &value
}

type fakeContextAskService struct {
	requests []ContextAskRequest
	response ContextAskResponse
	err      error
	ask      func(context.Context, ContextAskRequest) (ContextAskResponse, error)
}

func (f *fakeContextAskService) AskContext(ctx context.Context, request ContextAskRequest) (ContextAskResponse, error) {
	f.requests = append(f.requests, request)
	if f.ask != nil {
		return f.ask(ctx, request)
	}
	return f.response, f.err
}

func TestContextAskSchemaDescriptionAndRisk(t *testing.T) {
	tool := ContextAskTool{}
	if tool.Name() != "ContextAsk" {
		t.Fatalf("unexpected name %q", tool.Name())
	}
	description := strings.ToLower(tool.Description())
	for _, want := range []string{"read-only", "direct child agent", "does not send any message"} {
		if !strings.Contains(description, want) {
			t.Fatalf("description must contain %q: %q", want, tool.Description())
		}
	}
	if risk := tool.Risk(json.RawMessage(`{"task_id":"task-1","questions":["status?"]}`)); risk != RiskRead {
		t.Fatalf("expected read risk, got %s", risk)
	}

	schemaType := reflect.TypeOf(tool.Schema())
	if schemaType != reflect.TypeOf(contextAskInput{}) {
		t.Fatalf("unexpected schema type %v", schemaType)
	}
	wantFields := map[string]struct {
		jsonName string
		typeOf   reflect.Type
	}{
		"TaskID":          {jsonName: "task_id", typeOf: reflect.TypeOf("")},
		"Questions":       {jsonName: "questions", typeOf: reflect.TypeOf([]string{})},
		"RunID":           {jsonName: "run_id,omitempty", typeOf: reflect.TypeOf("")},
		"IncludeEvidence": {jsonName: "include_evidence,omitempty", typeOf: reflect.TypeOf((*bool)(nil))},
		"MaxChars":        {jsonName: "max_chars,omitempty", typeOf: reflect.TypeOf((*int)(nil))},
	}
	if schemaType.NumField() != len(wantFields) {
		t.Fatalf("unexpected schema field count %d", schemaType.NumField())
	}
	for fieldName, want := range wantFields {
		field, ok := schemaType.FieldByName(fieldName)
		if !ok || field.Type != want.typeOf || field.Tag.Get("json") != want.jsonName {
			t.Fatalf("unexpected schema field %s: field=%+v present=%v", fieldName, field, ok)
		}
	}
}

func TestContextAskDefaultsAndTrimsInput(t *testing.T) {
	background := &contextAskBackgroundService{task: BackgroundTask{
		ID: "task-1", ParentRunID: "parent-run-1", Kind: BackgroundTaskKindAgent, ChildAgentID: "child-1", ChildRunID: "child-run-1",
	}}
	service := &fakeContextAskService{response: ContextAskResponse{TaskID: "task-1"}}
	result, err := (ContextAskTool{}).Execute(context.Background(), Call{
		ID: "ask-1", Name: "ContextAsk", Input: json.RawMessage(`{"task_id":"  task-1  ","questions":["  What changed?  "]}`),
	}, Env{AgentID: "parent-1", RunID: "parent-run-1", Background: background, ContextAsk: service})
	if err != nil || result.IsError {
		t.Fatalf("unexpected result=%+v err=%v", result, err)
	}
	if background.getCalls != 1 || background.ownerID != "parent-1" || background.taskID != "task-1" {
		t.Fatalf("owner-scoped lookup was not used: %+v", background)
	}
	if len(service.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(service.requests))
	}
	request := service.requests[0]
	if !request.IncludeEvidence || request.MaxChars != defaultContextAskMaxChars {
		t.Fatalf("unexpected defaults: %+v", request)
	}
	if request.RunID != "child-run-1" || !reflect.DeepEqual(request.Questions, []string{"What changed?"}) {
		t.Fatalf("input was not normalized: %+v", request)
	}
}

func TestContextAskInputBoundaries(t *testing.T) {
	validTaskID := strings.Repeat("界", maxContextAskTaskIDChars)
	validRunID := strings.Repeat("程", maxContextAskRunIDChars)
	validQuestion := strings.Repeat("问", maxContextAskQuestionChars)
	validQuestions := make([]string, maxContextAskQuestions)
	for index := range validQuestions {
		validQuestions[index] = validQuestion
	}
	for _, maxChars := range []int{minContextAskMaxChars, maxContextAskMaxChars} {
		t.Run("valid max_chars "+strconv.Itoa(maxChars), func(t *testing.T) {
			background := &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child", ChildRunID: validRunID}}
			service := &fakeContextAskService{}
			input, err := json.Marshal(contextAskInput{TaskID: validTaskID, RunID: validRunID, Questions: validQuestions, MaxChars: contextAskInt(maxChars)})
			if err != nil {
				t.Fatal(err)
			}
			result, executeErr := (ContextAskTool{}).Execute(context.Background(), Call{Input: input}, Env{AgentID: "parent", RunID: "parent-run", Background: background, ContextAsk: service})
			if executeErr != nil || result.IsError || len(service.requests) != 1 {
				t.Fatalf("valid boundary rejected: result=%+v err=%v requests=%d", result, executeErr, len(service.requests))
			}
		})
	}

	tests := []struct {
		name  string
		input contextAskInput
	}{
		{name: "missing task", input: contextAskInput{TaskID: "   ", Questions: []string{"q"}}},
		{name: "task too long", input: contextAskInput{TaskID: strings.Repeat("界", maxContextAskTaskIDChars+1), Questions: []string{"q"}}},
		{name: "no questions", input: contextAskInput{TaskID: "task"}},
		{name: "too many questions", input: contextAskInput{TaskID: "task", Questions: make([]string, maxContextAskQuestions+1)}},
		{name: "empty trimmed question", input: contextAskInput{TaskID: "task", Questions: []string{" \n\t "}}},
		{name: "question too long", input: contextAskInput{TaskID: "task", Questions: []string{strings.Repeat("问", maxContextAskQuestionChars+1)}}},
		{name: "run too long", input: contextAskInput{TaskID: "task", RunID: strings.Repeat("程", maxContextAskRunIDChars+1), Questions: []string{"q"}}},
		{name: "zero max chars", input: contextAskInput{TaskID: "task", Questions: []string{"q"}, MaxChars: contextAskInt(0)}},
		{name: "max chars below minimum", input: contextAskInput{TaskID: "task", Questions: []string{"q"}, MaxChars: contextAskInt(minContextAskMaxChars - 1)}},
		{name: "max chars above maximum", input: contextAskInput{TaskID: "task", Questions: []string{"q"}, MaxChars: contextAskInt(maxContextAskMaxChars + 1)}},
		{name: "negative max chars", input: contextAskInput{TaskID: "task", Questions: []string{"q"}, MaxChars: contextAskInt(-1)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			background := &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent, ChildAgentID: "child"}}
			service := &fakeContextAskService{}
			input, err := json.Marshal(test.input)
			if err != nil {
				t.Fatal(err)
			}
			result, executeErr := (ContextAskTool{}).Execute(context.Background(), Call{Input: input}, Env{AgentID: "parent", Background: background, ContextAsk: service})
			if executeErr != nil || !result.IsError {
				t.Fatalf("expected validation error: result=%+v err=%v", result, executeErr)
			}
			if background.getCalls != 0 || len(service.requests) != 0 {
				t.Fatalf("invalid input reached services: gets=%d requests=%d", background.getCalls, len(service.requests))
			}
		})
	}
}

func TestContextAskRequiresServices(t *testing.T) {
	validInput := json.RawMessage(`{"task_id":"task","questions":["q"]}`)
	service := &fakeContextAskService{}
	result, err := (ContextAskTool{}).Execute(context.Background(), Call{Input: validInput}, Env{ContextAsk: service})
	if err != nil || !result.IsError || result.Output != "background task service is unavailable" {
		t.Fatalf("expected background service error, result=%+v err=%v", result, err)
	}
	background := &contextAskBackgroundService{}
	result, err = (ContextAskTool{}).Execute(context.Background(), Call{Input: validInput}, Env{Background: background})
	if err != nil || !result.IsError || result.Output != "context ask service is unavailable" {
		t.Fatalf("expected context service error, result=%+v err=%v", result, err)
	}
	if background.getCalls != 0 {
		t.Fatalf("missing context service must fail before lookup, got %d calls", background.getCalls)
	}
}

func TestContextAskRejectsInvalidTargetsWithoutEnumeration(t *testing.T) {
	ownerFailure := errors.New("belongs to another owner")
	tests := []struct {
		name       string
		background *contextAskBackgroundService
	}{
		{name: "owner lookup failure", background: &contextAskBackgroundService{getErr: ownerFailure}},
		{name: "shell task", background: &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindShell}}},
		{name: "agent without child", background: &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeContextAskService{}
			result, err := (ContextAskTool{}).Execute(context.Background(), Call{Input: json.RawMessage(`{"task_id":"target","questions":["q"]}`)}, Env{
				AgentID: "parent", Background: test.background, ContextAsk: service,
			})
			if err != nil || !result.IsError || result.Output != "context target is unavailable" {
				t.Fatalf("expected generic target error, result=%+v err=%v", result, err)
			}
			if test.background.ownerID != "parent" || len(service.requests) != 0 {
				t.Fatalf("target validation bypassed owner isolation: background=%+v requests=%d", test.background, len(service.requests))
			}
		})
	}
}

func TestContextAskRequesterFieldsCannotBeForged(t *testing.T) {
	background := &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "real-parent-run", ChildAgentID: "real-child", ChildRunID: "stored-run"}}
	service := &fakeContextAskService{}
	input := json.RawMessage(`{
		"task_id":"task-1",
		"questions":["q"],
		"run_id":"stored-run",
		"include_evidence":false,
		"max_chars":7000,
		"requester_agent_id":"forged-agent",
		"requester_run_id":"forged-run",
		"requesterAgentId":"also-forged",
		"requesterRunId":"also-forged"
	}`)
	result, err := (ContextAskTool{}).Execute(context.Background(), Call{Input: input}, Env{
		AgentID: "real-parent", RunID: "real-parent-run", Background: background, ContextAsk: service,
	})
	// Closed-world decode rejects forged requester_* fields before any service call.
	if err != nil || !result.IsError || len(service.requests) != 0 {
		t.Fatalf("expected forged requester fields to be rejected: result=%+v err=%v requests=%d", result, err, len(service.requests))
	}
	if !strings.Contains(result.Output, "unknown field") && !strings.Contains(result.Output, "requester") {
		t.Fatalf("expected closed-world rejection message, got %q", result.Output)
	}
}

func TestContextAskBindsParentAndChildRuns(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		envRun string
		task   BackgroundTask
	}{
		{name: "blank current parent run", input: `{"task_id":"task","questions":["q"]}`, task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child", ChildRunID: "child-run"}},
		{name: "different parent run", input: `{"task_id":"task","questions":["q"]}`, envRun: "other-parent-run", task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child", ChildRunID: "child-run"}},
		{name: "different explicit child run", input: `{"task_id":"task","run_id":"other-child-run","questions":["q"]}`, envRun: "parent-run", task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child", ChildRunID: "child-run"}},
		{name: "missing recorded child run", input: `{"task_id":"task","questions":["q"]}`, envRun: "parent-run", task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			background := &contextAskBackgroundService{task: test.task}
			service := &fakeContextAskService{}
			result, err := (ContextAskTool{}).Execute(context.Background(), Call{Input: json.RawMessage(test.input)}, Env{
				AgentID: "parent", RunID: test.envRun, Background: background, ContextAsk: service,
			})
			if err != nil || !result.IsError || result.Output != "context target is unavailable" {
				t.Fatalf("expected generic run-binding failure, result=%+v err=%v", result, err)
			}
			if len(service.requests) != 0 {
				t.Fatalf("run-binding failure reached context service: %+v", service.requests)
			}
		})
	}
}

func TestContextAskReturnsSuccessJSON(t *testing.T) {
	want := ContextAskResponse{
		TaskID: "task-1", ChildAgentID: "child-1", RunID: "child-run-1",
		Answers: []ContextAskAnswer{{
			Question: "What changed?", Answer: "The parser changed.", Confidence: "high",
			Claims: []ContextAskClaim{{Text: "Parser validation was tightened.", EvidenceIDs: []string{"ev-1"}}},
		}},
		Evidence:      []ContextAskEvidence{{ID: "ev-1", Kind: "file", SourceID: "parser.go", Trust: "untrusted_data", Derived: false, Digest: "sha256:abc", Excerpt: "validate(input)"}},
		Coverage:      "complete",
		Partial:       false,
		Truncated:     true,
		PossiblyStale: true,
		Limitations:   []string{"working tree changed"},
		Cached:        true,
	}
	background := &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child-1", ChildRunID: "child-run-1"}}
	service := &fakeContextAskService{response: want}
	result, err := (ContextAskTool{}).Execute(context.Background(), Call{Input: json.RawMessage(`{"task_id":"task-1","questions":["What changed?"]}`)}, Env{
		AgentID: "parent", RunID: "parent-run", Background: background, ContextAsk: service,
	})
	if err != nil || result.IsError {
		t.Fatalf("unexpected result=%+v err=%v", result, err)
	}
	var got ContextAskResponse
	if err := json.Unmarshal([]byte(result.Output), &got); err != nil {
		t.Fatalf("output is not response JSON: %v; output=%q", err, result.Output)
	}
	safeWant := want
	safeWant.Evidence = append([]ContextAskEvidence(nil), want.Evidence...)
	safeWant.Evidence[0].Excerpt = ""
	if !reflect.DeepEqual(got, safeWant) {
		t.Fatalf("unexpected response:\n got: %+v\nwant: %+v", got, safeWant)
	}
	if strings.Contains(result.Output, "validate(input)") || strings.Contains(result.Output, `"excerpt"`) {
		t.Fatalf("raw evidence excerpt leaked to parent model: %s", result.Output)
	}
	for _, key := range []string{`"taskId"`, `"childAgentId"`, `"possiblyStale"`, `"evidenceIds"`, `"sourceId"`, `"trust"`, `"derived"`, `"digest"`} {
		if !strings.Contains(result.Output, key) {
			t.Fatalf("expected output to contain %s: %s", key, result.Output)
		}
	}
}

func TestContextAskPropagatesCancellation(t *testing.T) {
	t.Run("background lookup", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		background := &contextAskBackgroundService{getFunc: func(ctx context.Context, _, _ string) (BackgroundTask, error) {
			return BackgroundTask{}, ctx.Err()
		}}
		service := &fakeContextAskService{}
		result, err := (ContextAskTool{}).Execute(ctx, Call{Input: json.RawMessage(`{"task_id":"task","questions":["q"]}`)}, Env{
			AgentID: "parent", Background: background, ContextAsk: service,
		})
		if !errors.Is(err, context.Canceled) || result.Output != "" || result.IsError || result.Meta != nil || len(service.requests) != 0 {
			t.Fatalf("cancellation not propagated: result=%+v err=%v requests=%d", result, err, len(service.requests))
		}
	})

	t.Run("context service", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		background := &contextAskBackgroundService{task: BackgroundTask{Kind: BackgroundTaskKindAgent, ParentRunID: "parent-run", ChildAgentID: "child", ChildRunID: "child-run"}}
		service := &fakeContextAskService{ask: func(ctx context.Context, _ ContextAskRequest) (ContextAskResponse, error) {
			cancel()
			return ContextAskResponse{}, ctx.Err()
		}}
		result, err := (ContextAskTool{}).Execute(ctx, Call{Input: json.RawMessage(`{"task_id":"task","questions":["q"]}`)}, Env{
			AgentID: "parent", RunID: "parent-run", Background: background, ContextAsk: service,
		})
		if !errors.Is(err, context.Canceled) || result.Output != "" || result.IsError || result.Meta != nil || len(service.requests) != 1 {
			t.Fatalf("cancellation not propagated: result=%+v err=%v requests=%d", result, err, len(service.requests))
		}
	})
}
