package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"autoto/internal/config"
	"autoto/internal/db"
	"autoto/internal/providers"
	"autoto/internal/tools"
)

type dynamicTestTool struct {
	name   string
	schema json.RawMessage
	risk   tools.Risk
}

func (t dynamicTestTool) Name() string        { return t.name }
func (t dynamicTestTool) Description() string { return "dynamic plugin test tool" }
func (t dynamicTestTool) Schema() any         { return t.schema }
func (t dynamicTestTool) Risk(json.RawMessage) tools.Risk {
	return t.risk
}
func (t dynamicTestTool) Execute(context.Context, tools.Call, tools.Env) (tools.Result, error) {
	return tools.Result{Output: "dynamic"}, nil
}

type dynamicBoundaryTool struct {
	mu            sync.Mutex
	name          string
	schema        json.RawMessage
	riskInputs    []json.RawMessage
	executeInputs []json.RawMessage
}

func (tool *dynamicBoundaryTool) Name() string   { return tool.name }
func (*dynamicBoundaryTool) Description() string { return "dynamic schema boundary test tool" }
func (tool *dynamicBoundaryTool) Schema() any    { return append(json.RawMessage(nil), tool.schema...) }
func (tool *dynamicBoundaryTool) Risk(input json.RawMessage) tools.Risk {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.riskInputs = append(tool.riskInputs, append(json.RawMessage(nil), input...))
	return tools.RiskRead
}
func (tool *dynamicBoundaryTool) Execute(_ context.Context, call tools.Call, _ tools.Env) (tools.Result, error) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	tool.executeInputs = append(tool.executeInputs, append(json.RawMessage(nil), call.Input...))
	return tools.Result{Output: "dynamic-boundary"}, nil
}
func (tool *dynamicBoundaryTool) inputs() ([]json.RawMessage, []json.RawMessage) {
	tool.mu.Lock()
	defer tool.mu.Unlock()
	return append([]json.RawMessage(nil), tool.riskInputs...), append([]json.RawMessage(nil), tool.executeInputs...)
}

type dynamicTestSource struct {
	mu        sync.Mutex
	tool      tools.Tool
	listCount int
}

func (s *dynamicTestSource) ListTools(context.Context, tools.ResolutionContext) ([]tools.Tool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCount++
	return []tools.Tool{s.tool}, nil
}

func (s *dynamicTestSource) ResolveTool(_ context.Context, _ tools.ResolutionContext, name string) (tools.Tool, error) {
	if s.tool != nil && s.tool.Name() == name {
		return s.tool, nil
	}
	return nil, errors.New("tool not found")
}

func TestRunnerSnapshotsDynamicNativeSchemaOncePerRun(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	if _, err := store.AddMessage(ctx, dbMessage(agent.ID, "schema")); err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{turns: [][]providers.Event{{{Type: "text", Text: "done"}, {Type: "done", Done: true}}}}
	runner := newAgentTestRunner(store, provider, config.AgentConfig{MaxTurns: 1})
	schema := json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false,"$defs":{"tag":{"type":"string"}}}`)
	source := &dynamicTestSource{tool: dynamicTestTool{name: "plugin__demo__schema", schema: schema, risk: tools.RiskExec}}
	enableAgentTestTool(t, store, source.tool.Name())
	runner.SetDynamicToolSource(source)
	if err := runner.run(ctx, agent.ID, ""); err != nil {
		t.Fatal(err)
	}
	request := provider.request(0)
	var found *providers.ToolSpec
	for index := range request.Tools {
		if request.Tools[index].Name == "plugin__demo__schema" {
			found = &request.Tools[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("dynamic tool missing from model snapshot: %+v", request.Tools)
	}
	encoded, _ := json.Marshal(found.Schema)
	var gotSchema, wantSchema any
	if err := json.Unmarshal(encoded, &gotSchema); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schema, &wantSchema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Fatalf("native MCP schema was reflected away:\n got %v\nwant %v", gotSchema, wantSchema)
	}
	source.mu.Lock()
	count := source.listCount
	source.mu.Unlock()
	if count != 1 {
		t.Fatalf("dynamic tools must be snapshotted once per run, listed %d times", count)
	}
}

func TestRunnerDynamicPluginToolUsesExecApprovalPolicy(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	source := &dynamicTestSource{tool: dynamicTestTool{name: "plugin__demo__exec", schema: json.RawMessage(`{"type":"object"}`), risk: tools.RiskExec}}
	enableAgentTestTool(t, store, source.tool.Name())
	runner.SetDynamicToolSource(source)
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "plugin-exec", Name: "plugin__demo__exec", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || result.Output != "tool call requires approval in an agent loop" {
		t.Fatalf("plugin RiskExec bypassed approval policy: %+v", result)
	}
}

func TestRunnerDynamicToolNormalizesAdditionalPropertiesSchema(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	tool := &dynamicBoundaryTool{
		name:   "plugin__demo__additional_schema",
		schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":{"type":"integer","minimum":2,"maximum":4}}`),
	}
	enableAgentTestTool(t, store, tool.Name())
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	runner.SetDynamicToolSource(&dynamicTestSource{tool: tool})
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "dynamic-additional", Name: tool.Name(), Input: json.RawMessage(`{"low":"1","exact":"3.0","high":"9"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.Output != "dynamic-boundary" {
		t.Fatalf("unexpected dynamic result: %+v", result)
	}
	want := `{"exact":3,"high":4,"low":2}`
	riskInputs, executeInputs := tool.inputs()
	if len(riskInputs) != 1 || string(riskInputs[0]) != want || len(executeInputs) != 1 || string(executeInputs[0]) != want {
		t.Fatalf("dynamic normalized inputs mismatch: risk=%q execute=%q", riskInputs, executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, "dynamic-additional")
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.InputJSON) != want {
		t.Fatalf("dynamic audit input mismatch: %s", stored.InputJSON)
	}
}

func TestRunnerDynamicToolRejectsClosedWorldAndHostFieldsBeforeRisk(t *testing.T) {
	cases := []struct {
		name   string
		schema json.RawMessage
		input  json.RawMessage
		match  string
	}{
		{
			name:   "closed world",
			schema: json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer"}},"additionalProperties":false}`),
			input:  json.RawMessage(`{"count":"3.0","unknown":1}`),
			match:  "unknown property",
		},
		{
			name:   "host field",
			schema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":true}`),
			input:  json.RawMessage(`{"working_directory":"C:/escape"}`),
			match:  "host field",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
			defer store.Close()
			tool := &dynamicBoundaryTool{name: "plugin__demo__reject_" + strings.ReplaceAll(test.name, " ", "_"), schema: test.schema}
			enableAgentTestTool(t, store, tool.Name())
			runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
			runner.SetDynamicToolSource(&dynamicTestSource{tool: tool})
			callID := "dynamic-reject-" + strings.ReplaceAll(test.name, " ", "-")
			result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: callID, Name: tool.Name(), Input: test.input})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || !strings.Contains(result.Output, test.match) {
				t.Fatalf("expected %q rejection, got %+v", test.match, result)
			}
			riskInputs, executeInputs := tool.inputs()
			if len(riskInputs) != 0 || len(executeInputs) != 0 {
				t.Fatalf("rejected dynamic input reached risk or execute: risk=%q execute=%q", riskInputs, executeInputs)
			}
			stored, err := store.GetToolCallByUseID(ctx, agent.ID, callID)
			if err != nil {
				t.Fatal(err)
			}
			if string(stored.InputJSON) != string(test.input) || stored.Status != "error" {
				t.Fatalf("rejected dynamic input was not audited intact: status=%s input=%q", stored.Status, stored.InputJSON)
			}
		})
	}
}

func TestRunnerDynamicToolInvalidSchemaFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, agent := newAgentTestStore(t, t.TempDir(), "acceptEdits")
	defer store.Close()
	tool := &dynamicBoundaryTool{
		name:   "plugin__demo__invalid_schema",
		schema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number","minimum":10,"maximum":1}},"additionalProperties":false}`),
	}
	enableAgentTestTool(t, store, tool.Name())
	runner := newAgentTestRunner(store, &scriptedProvider{}, config.AgentConfig{})
	runner.SetDynamicToolSource(&dynamicTestSource{tool: tool})
	if _, err := runner.snapshotTools(ctx, tools.ResolutionContext{AgentID: agent.ID, CWD: agent.CWD}); err == nil || !strings.Contains(err.Error(), "invalid schema") {
		t.Fatalf("expected invalid dynamic schema snapshot rejection, got %v", err)
	}
	input := json.RawMessage(`{"value":5}`)
	result, err := runner.ExecuteTool(ctx, agent.ID, tools.Call{ID: "dynamic-invalid-schema", Name: tool.Name(), Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Output, "invalid schema") {
		t.Fatalf("expected invalid schema execution rejection, got %+v", result)
	}
	riskInputs, executeInputs := tool.inputs()
	if len(riskInputs) != 0 || len(executeInputs) != 0 {
		t.Fatalf("invalid schema reached risk or execute: risk=%q execute=%q", riskInputs, executeInputs)
	}
	stored, err := store.GetToolCallByUseID(ctx, agent.ID, "dynamic-invalid-schema")
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.InputJSON) != string(input) || stored.Status != "error" {
		t.Fatalf("invalid schema input was not audited intact: status=%s input=%q", stored.Status, stored.InputJSON)
	}
}

func dbMessage(agentID, text string) db.Message {
	return db.Message{AgentID: agentID, Role: "user", ContentText: text}
}
