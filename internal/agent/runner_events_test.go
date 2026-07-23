package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type lifecycleTestTool struct {
	output string
}

func (tool lifecycleTestTool) Name() string { return "LifecycleTest" }

func (tool lifecycleTestTool) Description() string { return "Returns a controlled test result." }

func (tool lifecycleTestTool) Schema() any { return map[string]any{"type": "object"} }

func (tool lifecycleTestTool) Risk(json.RawMessage) tools.Risk {
	return tools.RiskRead
}

func (tool lifecycleTestTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{Output: tool.output}, nil
}

func TestToolLifecycleEventsIncludeStructuredInputAndBoundedPreview(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	run, err := store.CreateRun(ctx, db.Run{AgentID: agent.ID, Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	toolRegistry := tools.NewRegistry()
	toolRegistry.Register(lifecycleTestTool{output: strings.Repeat("界", maxToolResultPreviewBytes)})
	runner := NewRunner(store, providers.NewRegistry(), toolRegistry, NewHub(), config.AgentConfig{})
	subscription := runner.hub.Subscribe(ctx, agent.ID)
	input := json.RawMessage(`{"target":"文档.txt","options":{"recursive":true}}`)

	result, err := runner.executeToolForLoop(ctx, agent.ID, run.ID, tools.Call{ID: "lifecycle-1", Name: "LifecycleTest", Input: input}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("expected successful tool result, got %+v", result)
	}

	var started, finished Event
	deadline := time.After(time.Second)
	for finished.Type == "" {
		select {
		case event := <-subscription:
			switch event.Type {
			case "tool.started":
				started = event
			case "tool.finished":
				finished = event
			}
		case <-deadline:
			t.Fatal("timed out waiting for tool lifecycle events")
		}
	}
	if started.Type == "" {
		t.Fatal("missing tool.started event")
	}
	for name, event := range map[string]Event{"started": started, "finished": finished} {
		if got, _ := event.Data["toolUseId"].(string); got != "lifecycle-1" {
			t.Fatalf("%s event missing tool use id: %+v", name, event.Data)
		}
		if got, _ := event.Data["toolName"].(string); got != "LifecycleTest" {
			t.Fatalf("%s event missing tool name: %+v", name, event.Data)
		}
		if got, _ := event.Data["executionDeviceId"].(string); got != "local" {
			t.Fatalf("%s event has execution device %q, want local", name, got)
		}
		if got, _ := event.Data["runId"].(string); got != run.ID {
			t.Fatalf("%s event has run id %q, want %q", name, got, run.ID)
		}
		raw, ok := event.Data["inputJson"].(json.RawMessage)
		if !ok || !json.Valid(raw) {
			t.Fatalf("%s event inputJson is not structured JSON: %#v", name, event.Data["inputJson"])
		}
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) != 0 {
			t.Fatalf("%s event should omit unapproved custom input fields: input=%s err=%v", name, raw, err)
		}
		if marked, _ := event.Data["inputTruncated"].(bool); !marked {
			t.Fatalf("%s event should mark projected custom input: %+v", name, event.Data)
		}
	}
	wire, err := json.Marshal(started)
	if err != nil {
		t.Fatal(err)
	}
	var wirePayload struct {
		Data struct {
			Input json.RawMessage `json:"inputJson"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wire, &wirePayload); err != nil || !json.Valid(wirePayload.Data.Input) {
		t.Fatalf("tool.started inputJson did not remain structured on the wire: event=%s err=%v", wire, err)
	}
	if got, _ := finished.Data["status"].(string); got != "completed" {
		t.Fatalf("tool.finished status = %q, want completed", got)
	}
	if duration, ok := finished.Data["durationMs"].(int64); !ok || duration < 0 {
		t.Fatalf("tool.finished durationMs is invalid: %#v", finished.Data["durationMs"])
	}
	preview, ok := finished.Data["resultPreview"].(string)
	if !ok || !utf8.ValidString(preview) || len(preview) > maxToolResultPreviewBytes {
		t.Fatalf("tool.finished preview is not bounded UTF-8: bytes=%d valid=%v", len(preview), utf8.ValidString(preview))
	}
	if truncated, _ := finished.Data["resultTruncated"].(bool); !truncated {
		t.Fatalf("tool.finished should mark truncated preview: %+v", finished.Data)
	}
}

func TestToolEventInputJSONBoundsLargeStructuredValues(t *testing.T) {
	input, err := json.Marshal(map[string]any{
		"file_path": "large.txt",
		"content":   strings.Repeat("界", maxToolEventInputBytes),
		"options":   map[string]any{"recursive": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, truncated := toolEventInputJSON(input)
	if !truncated || len(bounded) > maxToolEventInputBytes || !json.Valid(bounded) || !utf8.Valid(bounded) {
		t.Fatalf("expected bounded structured input: bytes=%d truncated=%v validJSON=%v validUTF8=%v", len(bounded), truncated, json.Valid(bounded), utf8.Valid(bounded))
	}
	var decoded map[string]any
	if err := json.Unmarshal(bounded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["file_path"] != "large.txt" {
		t.Fatalf("high-signal file path was not preserved: %s", bounded)
	}
	if _, exposed := decoded["content"]; exposed || decoded["contentBytes"] != float64(len(strings.Repeat("界", maxToolEventInputBytes))) {
		t.Fatalf("content should be replaced with a byte-length summary: %s", bounded)
	}
	data := toolStartedEventData(tools.Call{ID: "large-input", Name: "Write", Input: input}, tools.RiskWrite, "", "run-1")
	if marked, _ := data["inputTruncated"].(bool); !marked {
		t.Fatalf("tool event should mark bounded input: %+v", data)
	}
}

func TestToolEventMetaV1KeepsLegacyFieldsAndOmitsBashArguments(t *testing.T) {
	secret := "TOP_SECRET_VALUE"
	call := tools.Call{ID: "bash-meta", Name: "Bash", Input: json.RawMessage(`{"command":"git status --token=TOP_SECRET_VALUE $(printf TOP_SECRET_VALUE)"}`)}
	data := NewToolEventMetaBuilder(call, tools.RiskExec, "", "run-1").Decision(toolPermissionAsk, decisionSourceDefaultPolicy, "", "").ToEventData()
	if data["eventVersion"] != toolEventVersion || data["toolUseId"] != call.ID || data["toolName"] != call.Name || data["runId"] != "run-1" || data["decision"] != toolPermissionAsk || data["decisionSource"] != decisionSourceDefaultPolicy {
		t.Fatalf("missing v1 or legacy fields: %+v", data)
	}
	input, ok := data["inputJson"].(json.RawMessage)
	if !ok || strings.Contains(string(input), secret) || strings.Contains(string(input), "command\"") {
		t.Fatalf("Bash event input must omit raw command arguments: %s", input)
	}
	facts, ok := data["commandFacts"].(tools.CommandFacts)
	if !ok || strings.Contains(fmt.Sprintf("%+v", facts), secret) {
		t.Fatalf("expected argument-free command facts, got %+v", data["commandFacts"])
	}
	if runtime.GOOS == "windows" {
		if facts.ParseKnown {
			t.Fatalf("cmd.exe command facts must remain unknown, got %+v", facts)
		}
	} else if !facts.ParseKnown || facts.Program != "git" {
		t.Fatalf("expected parsed git command facts, got %+v", facts)
	}
}

func TestRedactToolActivityTextCoversGenericTokenAssignments(t *testing.T) {
	for _, input := range []string{"token=TOP_SECRET_TOKEN", `token: "TOP_SECRET_JSON_TOKEN"`} {
		redacted := RedactToolActivityText(input)
		if strings.Contains(redacted, "TOP_SECRET") || !strings.Contains(redacted, "[redacted]") {
			t.Fatalf("generic token assignment was not redacted: %q", redacted)
		}
	}
}

func TestToolEventDecisionReasonIsRedactedAndBounded(t *testing.T) {
	reason := strings.Repeat("x", maxToolEventInputStringBytes+128) + " token=TOP_SECRET_REASON"
	data := NewToolEventMetaBuilder(tools.Call{ID: "reason-1", Name: "Read", Input: json.RawMessage(`{"file_path":"README.md"}`)}, tools.RiskRead, "local", "run-1").
		Decision(toolPermissionAllow, decisionSourceDefaultPolicy, "", "").
		DecisionReason(reason).
		ToEventData()
	projected, _ := data["reason"].(string)
	if len(projected) > maxToolEventInputStringBytes || strings.Contains(projected, "TOP_SECRET_REASON") {
		t.Fatalf("decision reason must be bounded and redacted: len=%d reason=%q", len(projected), projected)
	}
}

func TestApprovalEventOmitsBashCommandAndMarksDetailHydration(t *testing.T) {
	secret := "TOP_SECRET_APPROVAL_VALUE"
	call := tools.Call{ID: "bash-approval", Name: "Bash", Input: json.RawMessage(`{"command":"git status --token=TOP_SECRET_APPROVAL_VALUE"}`)}
	data := approvalEventDataWithResolution(db.Agent{ID: "agent-1", CWD: "/workspace"}, call, tools.RiskExec, "review", "approval required", time.Now().Add(time.Minute), 2, 3, toolPermissionResolution{Decision: toolPermissionAsk, Source: decisionSourceDefaultPolicy})
	if data["command"] != "" || data["commandOmitted"] != true {
		t.Fatalf("approval event must require authenticated detail hydration: %+v", data)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("approval broadcast leaked Bash arguments: %s", encoded)
	}
}
