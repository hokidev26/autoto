package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
)

func TestRunRoutesExposeSummaryAndPendingApprovals(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddToolCall(ctx, db.ToolCall{AgentID: agent.ID, RunID: run.ID, ToolUseID: "tool-1", ToolName: "Bash", InputJSON: json.RawMessage(`{"command":"printf hi"}`), Status: "pending_approval"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 13; i++ {
		if _, err := store.AddToolCall(ctx, db.ToolCall{AgentID: agent.ID, RunID: run.ID, ToolUseID: fmt.Sprintf("completed-%02d", i), ToolName: "Read", InputJSON: json.RawMessage(fmt.Sprintf(`{"file_path":"secret-%02d.txt"}`, i)), OutputJSON: json.RawMessage(`{"output":"private"}`), Status: "completed"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AddAPIRequest(ctx, db.APIRequest{AgentID: agent.ID, RunID: run.ID, Kind: "model", Provider: "fake", Model: "test", InputTokens: 12, OutputTokens: 3, CostUSD: 0.001}); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)

	recorder := httptest.NewRecorder()
	request := newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs?limit=5", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var runs []db.Run
	if err := json.NewDecoder(recorder.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("unexpected runs response: %+v", runs)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs/"+run.ID, nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var summary db.RunSummary
	if err := json.NewDecoder(recorder.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if summary.Run.ID != run.ID || summary.MessageCount != 1 || summary.ToolCallCount != 14 || summary.PendingApprovals != 1 || summary.APIRequestCount != 1 || summary.InputTokens != 12 || summary.OutputTokens != 3 {
		t.Fatalf("unexpected run summary: %+v", summary)
	}
	if len(summary.ToolCalls) != 12 || strings.Contains(recorder.Body.String(), "inputJson") || strings.Contains(recorder.Body.String(), "secret-") || strings.Contains(recorder.Body.String(), "private") {
		t.Fatalf("run summary must return a bounded lightweight tool projection: %s", recorder.Body.String())
	}
	if len(summary.RecentMessages) != 1 || summary.RecentMessages[0].ContentText != "hello" {
		t.Fatalf("unexpected recent message preview: %+v", summary.RecentMessages)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs/active", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected active summary 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var active db.ActiveRunSummary
	if err := json.NewDecoder(recorder.Body).Decode(&active); err != nil {
		t.Fatal(err)
	}
	if active.Run.ID != run.ID || active.Run.Status != "running" || active.ToolCallCount != 14 || active.PendingApprovals != 1 || len(active.ToolCalls) != 6 || strings.Contains(recorder.Body.String(), "inputJson") {
		t.Fatalf("unexpected lightweight active summary: %+v", active)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs/"+run.ID+"/tool-calls", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected full tool list 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var fullCalls []db.ToolCall
	if err := json.NewDecoder(recorder.Body).Decode(&fullCalls); err != nil {
		t.Fatal(err)
	}
	if len(fullCalls) != 14 || len(fullCalls[0].InputJSON) == 0 {
		t.Fatalf("expected complete tool details endpoint, got %+v", fullCalls)
	}

	recorder = httptest.NewRecorder()
	request = newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/tool-calls/pending", nil)
	app.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var pending []db.ToolCall
	if err := json.NewDecoder(recorder.Body).Decode(&pending); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ToolUseID != "tool-1" || pending[0].RunID != run.ID {
		t.Fatalf("unexpected pending calls response: %+v", pending)
	}
}

func TestRunSummaryRedactsAndUTF8BoundsPublicErrorsWithoutMutatingDB(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "run-summary-errors.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	runError := "token=TOP_SECRET_RUN " + strings.Repeat("界", 2_000)
	checkpointError := "api_key=TOP_SECRET_CHECKPOINT " + strings.Repeat("界", 2_000)
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "error", ErrorMessage: runError, CheckpointError: checkpointError})
	if err != nil {
		t.Fatal(err)
	}
	toolError := "authorization: Bearer TOP_SECRET_TOOL " + strings.Repeat("界", 2_000)
	call, err := store.AddToolCall(ctx, db.ToolCall{AgentID: agent.ID, RunID: run.ID, ToolUseID: "failed-tool", ToolName: "Read", Status: "error", ErrorMessage: toolError})
	if err != nil {
		t.Fatal(err)
	}
	app := New(config.Config{}, store, nil, nil)
	recorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(recorder, newTestRequest(http.MethodGet, "/api/agents/"+agent.ID+"/runs/"+run.ID, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if !json.Valid(recorder.Body.Bytes()) {
		t.Fatalf("run summary response is not valid UTF-8 JSON: %q", recorder.Body.Bytes())
	}
	body := recorder.Body.String()
	for _, secret := range []string{"TOP_SECRET_RUN", "TOP_SECRET_CHECKPOINT", "TOP_SECRET_TOOL"} {
		if strings.Contains(body, secret) {
			t.Fatalf("run summary leaked %q: %s", secret, body)
		}
	}
	var summary db.RunSummary
	if err := json.NewDecoder(recorder.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Run.ErrorMessage) > activityErrorMessageBytes || len(summary.Run.CheckpointError) > activityErrorMessageBytes || len(summary.ToolCalls) != 1 || len(summary.ToolCalls[0].ErrorMessage) > activityErrorMessageBytes {
		t.Fatalf("public run errors were not bounded: %+v", summary)
	}
	storedRun, err := store.GetRun(ctx, agent.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	storedCall, err := store.GetToolCallByUseID(ctx, agent.ID, call.ToolUseID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.ErrorMessage != runError || storedRun.CheckpointError != checkpointError || storedCall.ErrorMessage != toolError {
		t.Fatalf("public projection mutated DB values: run=%+v tool=%+v", storedRun, storedCall)
	}
}

func TestRunToolCallsActivityViewBoundsPayloads(t *testing.T) {
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, _, agent, err := store.CreateProject(ctx, "Demo", "", t.TempDir(), "fake:test", "acceptEdits")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	message, err := store.AddMessage(ctx, db.Message{AgentID: agent.ID, RunID: run.ID, Role: "assistant", ContentText: "activity"})
	if err != nil {
		t.Fatal(err)
	}

	writeContent := strings.Repeat("界", 9_000)
	writeInput, err := json.Marshal(map[string]any{
		"file_path": "large-write.txt",
		"content":   writeContent,
		"mode":      "overwrite",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddToolCall(ctx, db.ToolCall{
		AgentID:           agent.ID,
		RunID:             run.ID,
		MessageID:         message.ID,
		ToolUseID:         "activity-write",
		ToolName:          "Write",
		InputJSON:         writeInput,
		OutputJSON:        json.RawMessage(`{"output":"write complete","meta":{"path":"large-write.txt"}}`),
		Status:            "completed",
		DurationMS:        42,
		ExecutionDeviceID: "local",
		StartedAt:         "2025-01-01T00:00:00Z",
		CompletedAt:       "2025-01-01T00:00:01Z",
	}); err != nil {
		t.Fatal(err)
	}

	editDiff := "diff-start\n" + strings.Repeat("+changed line\n", 1_500)
	editOutput, err := json.Marshal(map[string]any{
		"output": strings.Repeat("tool output ", 2_000),
		"meta": map[string]any{
			"path":         "large-write.txt",
			"replacements": 2,
			"diff":         editDiff,
			"raw":          strings.Repeat("must not be exposed", 2_000),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddToolCall(ctx, db.ToolCall{
		AgentID:    agent.ID,
		RunID:      run.ID,
		MessageID:  message.ID,
		ToolUseID:  "activity-edit",
		ToolName:   "Edit",
		InputJSON:  json.RawMessage(`{"file_path":"large-write.txt","old_string":"old","new_string":"new","replace_all":true}`),
		OutputJSON: editOutput,
		Status:     "completed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddToolCall(ctx, db.ToolCall{
		AgentID:    agent.ID,
		RunID:      run.ID,
		ToolUseID:  "activity-latest",
		ToolName:   "Read",
		InputJSON:  json.RawMessage(`{"file_path":"latest.txt"}`),
		OutputJSON: json.RawMessage(`{"output":"latest"}`),
		Status:     "completed",
	}); err != nil {
		t.Fatal(err)
	}

	app := New(config.Config{}, store, nil, nil)
	path := "/api/agents/" + agent.ID + "/runs/" + run.ID + "/tool-calls"

	fullRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(fullRecorder, newTestRequest(http.MethodGet, path, nil))
	if fullRecorder.Code != http.StatusOK {
		t.Fatalf("expected full endpoint 200, got %d: %s", fullRecorder.Code, fullRecorder.Body.String())
	}
	var full []db.ToolCall
	if err := json.NewDecoder(fullRecorder.Body).Decode(&full); err != nil {
		t.Fatal(err)
	}
	if len(full) != 3 {
		t.Fatalf("expected all complete tool calls, got %d", len(full))
	}
	var fullWrite db.ToolCall
	for _, call := range full {
		if call.ToolUseID == "activity-write" {
			fullWrite = call
			break
		}
	}
	var fullWriteInput map[string]string
	if err := json.Unmarshal(fullWrite.InputJSON, &fullWriteInput); err != nil {
		t.Fatal(err)
	}
	if fullWriteInput["content"] != writeContent {
		t.Fatal("default tool-calls endpoint must retain the complete Write content")
	}

	activityRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(activityRecorder, newTestRequest(http.MethodGet, path+"?view=activity", nil))
	if activityRecorder.Code != http.StatusOK {
		t.Fatalf("expected activity endpoint 200, got %d: %s", activityRecorder.Code, activityRecorder.Body.String())
	}
	if activityRecorder.Body.Len() > 100*1024 {
		t.Fatalf("activity response exceeded its bounded payload budget: %d bytes", activityRecorder.Body.Len())
	}
	type activityCall struct {
		AgentID           string          `json:"agentId"`
		RunID             string          `json:"runId"`
		MessageID         string          `json:"messageId"`
		ToolUseID         string          `json:"toolUseId"`
		ToolName          string          `json:"toolName"`
		InputJSON         json.RawMessage `json:"inputJson"`
		OutputJSON        json.RawMessage `json:"outputJson"`
		Status            string          `json:"status"`
		DurationMS        int64           `json:"durationMs"`
		ExecutionDeviceID string          `json:"executionDeviceId"`
		StartedAt         string          `json:"startedAt"`
		CompletedAt       string          `json:"completedAt"`
		CreatedAt         string          `json:"createdAt"`
		InputTruncated    bool            `json:"inputTruncated"`
		OutputTruncated   bool            `json:"outputTruncated"`
	}
	var activityPage struct {
		ToolCalls  []activityCall `json:"toolCalls"`
		HasMore    bool           `json:"hasMore"`
		NextOffset int            `json:"nextOffset"`
	}
	if err := json.NewDecoder(activityRecorder.Body).Decode(&activityPage); err != nil {
		t.Fatal(err)
	}
	activity := activityPage.ToolCalls
	if len(activity) != 3 {
		t.Fatalf("expected all activity calls, got %d", len(activity))
	}
	byUseID := make(map[string]activityCall, len(activity))
	for _, call := range activity {
		byUseID[call.ToolUseID] = call
	}
	writeCall := byUseID["activity-write"]
	if writeCall.AgentID != agent.ID || writeCall.RunID != run.ID || writeCall.MessageID != message.ID || writeCall.ToolName != "Write" || writeCall.Status != "completed" || writeCall.DurationMS != 42 || writeCall.ExecutionDeviceID != "local" || writeCall.StartedAt == "" || writeCall.CompletedAt == "" || writeCall.CreatedAt == "" {
		t.Fatalf("activity projection dropped required call fields: %+v", writeCall)
	}
	var activityWriteInput map[string]any
	if err := json.Unmarshal(writeCall.InputJSON, &activityWriteInput); err != nil {
		t.Fatalf("activity input must remain valid structured JSON: %v", err)
	}
	if _, exposed := activityWriteInput["content"]; exposed || activityWriteInput["contentBytes"] != float64(len(writeContent)) || !writeCall.InputTruncated {
		t.Fatalf("activity view exposed Write content instead of a length summary: %#v", activityWriteInput)
	}
	if activityWriteInput["file_path"] != "large-write.txt" {
		t.Fatalf("activity input lost high-signal file path: %#v", activityWriteInput)
	}

	editCall := byUseID["activity-edit"]
	var editResult struct {
		Output string         `json:"output"`
		Meta   map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(editCall.OutputJSON, &editResult); err != nil {
		t.Fatalf("activity output must remain valid tools.Result JSON: %v", err)
	}
	diff, ok := editResult.Meta["diff"].(string)
	if !ok || !strings.HasPrefix(diff, "diff-start\n") || len([]byte(diff)) > 64*1024 {
		t.Fatalf("activity output did not retain bounded Edit diff: %#v", editResult.Meta)
	}
	if editResult.Meta["path"] != "large-write.txt" || editResult.Meta["replacements"] != float64(2) || strings.Contains(activityRecorder.Body.String(), "must not be exposed") {
		t.Fatalf("activity output did not retain safe Edit metadata: %#v", editResult.Meta)
	}
	if len([]byte(editResult.Output)) > 12*1024 || !editCall.OutputTruncated {
		t.Fatalf("activity output was not bounded: %d bytes", len(editResult.Output))
	}

	limitRecorder := httptest.NewRecorder()
	app.Routes().ServeHTTP(limitRecorder, newTestRequest(http.MethodGet, path+"?view=activity&limit=1", nil))
	if limitRecorder.Code != http.StatusOK {
		t.Fatalf("expected limited activity endpoint 200, got %d: %s", limitRecorder.Code, limitRecorder.Body.String())
	}
	var limitedPage struct {
		ToolCalls  []activityCall `json:"toolCalls"`
		HasMore    bool           `json:"hasMore"`
		NextOffset int            `json:"nextOffset"`
	}
	if err := json.NewDecoder(limitRecorder.Body).Decode(&limitedPage); err != nil {
		t.Fatal(err)
	}
	if len(limitedPage.ToolCalls) != 1 || limitedPage.ToolCalls[0].ToolUseID != "activity-latest" || !limitedPage.HasMore || limitedPage.NextOffset != 1 {
		t.Fatalf("activity limit must return the most recent calls with a cursor: %+v", limitedPage)
	}
}

func TestActivityPermissionDecisionDerivesStableHistoricalSources(t *testing.T) {
	tests := []struct {
		name     string
		call     db.ToolCall
		decision string
		source   string
	}{
		{name: "read only cap", call: db.ToolCall{Status: "denied", PermissionDecisionReason: "write risk denied by readOnly permission mode"}, decision: "deny", source: "read_only_cap"},
		{name: "policy unavailable", call: db.ToolCall{Status: "pending_approval", PermissionDecisionReason: "tool permission policy unavailable; approval required"}, decision: "ask", source: "policy_unavailable"},
		{name: "workflow unavailable", call: db.ToolCall{Status: "pending_approval", PermissionDecisionReason: "workflow preferences unavailable; approval required"}, decision: "ask", source: "workflow_unavailable"},
		{name: "human approval", call: db.ToolCall{Status: "completed", PermissionDecidedBy: "user-1"}, decision: "allow", source: "human_approval"},
		{name: "generation invalidation", call: db.ToolCall{Status: "denied", PermissionDecisionReason: "tool approval invalidated by permission or policy change"}, decision: "deny", source: "generation_invalidation"},
		{name: "system timeout", call: db.ToolCall{Status: "denied", PermissionDecidedBy: "system", PermissionDecisionReason: "tool approval timed out"}, decision: "deny", source: "system"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, source := activityPermissionDecision(tt.call)
			if decision != tt.decision || source != tt.source {
				t.Fatalf("unexpected historical decision metadata: decision=%q source=%q", decision, source)
			}
		})
	}
}

func TestActivityProjectionAddsToolEventDecisionMetadata(t *testing.T) {
	call := db.ToolCall{
		AgentID:                  "agent-1",
		RunID:                    "run-1",
		ToolUseID:                "bash-1",
		ToolName:                 "Bash",
		InputJSON:                json.RawMessage(`{"command":"git status --token=TOP_SECRET_VALUE"}`),
		Status:                   "completed",
		PermissionDecidedBy:      "policy",
		PermissionDecisionReason: "tool permission rule matched (id=rule-secret, priority=10, decision=allow)",
		PermissionGeneration:     7,
		PolicyGeneration:         9,
	}
	projected := projectActivityToolCall(call)
	if projected.EventVersion != 1 || projected.Decision != "allow" || projected.DecisionSource != "rule" || projected.PermissionDecidedBy != "policy" || projected.PermissionGeneration != 7 || projected.PolicyGeneration != 9 {
		t.Fatalf("activity metadata projection is incomplete: %+v", projected)
	}
	if runtime.GOOS != "windows" {
		if projected.CommandFacts == nil || !projected.CommandFacts.ParseKnown || projected.CommandFacts.Program != "git" {
			t.Fatalf("activity projection missing Bash command facts: %+v", projected.CommandFacts)
		}
	} else if projected.CommandFacts == nil {
		t.Fatal("activity projection missing Bash command facts")
	}
	if strings.Contains(fmt.Sprintf("%+v", projected.CommandFacts), "TOP_SECRET_VALUE") {
		t.Fatalf("command facts leaked Bash arguments: %+v", projected.CommandFacts)
	}
}
