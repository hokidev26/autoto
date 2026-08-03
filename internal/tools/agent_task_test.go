package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentAcceptsSupportedSubagentTypes(t *testing.T) {
	roles := map[string]string{
		"executor": "executor", "explorer": "explorer", "reviewer": "reviewer", "tester": "tester",
		"general": "general", "general-purpose": "general", "explore": "explorer", "plan": "plan", "search": "search", "background": "general",
	}
	for role, expectedRole := range roles {
		t.Run(role, func(t *testing.T) {
			service := &fakeBackgroundTaskService{}
			input, err := json.Marshal(map[string]any{
				"prompt":        "inspect the repository",
				"subagent_type": role,
			})
			if err != nil {
				t.Fatal(err)
			}

			result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-role", Name: "Agent", Input: input}, Env{Background: service})
			if err != nil || result.IsError || len(service.submitted) != 1 {
				t.Fatalf("expected role %q to be accepted, result=%+v err=%v requests=%d", role, result, err, len(service.submitted))
			}
			var payload agentTaskPayload
			if err := json.Unmarshal(service.submitted[0].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SubagentType != expectedRole {
				t.Fatalf("expected payload role %q, got %q", expectedRole, payload.SubagentType)
			}
		})
	}
}

func TestAgentAcceptanceCriteriaAreBoundedAndPrivate(t *testing.T) {
	service := &fakeBackgroundTaskService{}
	input, err := json.Marshal(map[string]any{
		"prompt":              "TOP_SECRET_PROMPT",
		"description":         "review task",
		"subagent_type":       " Reviewer ",
		"acceptance_criteria": []string{"  findings are actionable  ", "tests pass"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-criteria", Name: "Agent", Input: input}, Env{Background: service})
	if err != nil || result.IsError || len(service.submitted) != 1 {
		t.Fatalf("unexpected result=%+v err=%v requests=%d", result, err, len(service.submitted))
	}
	request := service.submitted[0]

	var payload agentTaskPayload
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	wantCriteria := []string{"findings are actionable", "tests pass"}
	if payload.Prompt != "TOP_SECRET_PROMPT" || payload.SubagentType != "reviewer" || !equalStrings(payload.AcceptanceCriteria, wantCriteria) {
		t.Fatalf("unexpected private payload: %+v", payload)
	}

	publicSummary := string(request.PublicSummary)
	if strings.Contains(publicSummary, "TOP_SECRET_PROMPT") || strings.Contains(publicSummary, "findings are actionable") || strings.Contains(publicSummary, "tests pass") {
		t.Fatalf("public summary leaked private task content: %s", request.PublicSummary)
	}
	var summary map[string]json.RawMessage
	if err := json.Unmarshal(request.PublicSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if _, ok := summary["prompt"]; ok {
		t.Fatalf("public summary contains prompt field: %s", request.PublicSummary)
	}
	if _, ok := summary["acceptanceCriteria"]; ok {
		t.Fatalf("public summary contains acceptance criteria: %s", request.PublicSummary)
	}
	var acceptanceCount int
	if err := json.Unmarshal(summary["acceptanceCount"], &acceptanceCount); err != nil {
		t.Fatal(err)
	}
	if acceptanceCount != len(wantCriteria) {
		t.Fatalf("unexpected public acceptance count: %d", acceptanceCount)
	}
}

func TestAgentRejectsInvalidRoleAndAcceptanceCriteria(t *testing.T) {
	tooMany := make([]string, maxAcceptanceCriteriaItems+1)
	for index := range tooMany {
		tooMany[index] = "criterion"
	}
	tests := []struct {
		name       string
		input      map[string]any
		wantOutput string
	}{
		{
			name:       "invalid role",
			input:      map[string]any{"prompt": "inspect", "subagent_type": "bad role!"},
			wantOutput: "invalid subagent_type",
		},
		{
			name:       "blank criterion",
			input:      map[string]any{"prompt": "inspect", "acceptance_criteria": []string{"valid", " \n\t "}},
			wantOutput: "must not be blank",
		},
		{
			name:       "too many criteria",
			input:      map[string]any{"prompt": "inspect", "acceptance_criteria": tooMany},
			wantOutput: "item limit",
		},
		{
			name:       "oversized criterion",
			input:      map[string]any{"prompt": "inspect", "acceptance_criteria": []string{strings.Repeat("x", maxAcceptanceCriterionBytes+1)}},
			wantOutput: "item exceeds size limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeBackgroundTaskService{}
			input, err := json.Marshal(tt.input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-invalid", Name: "Agent", Input: input}, Env{Background: service})
			if err != nil || !result.IsError || !strings.Contains(result.Output, tt.wantOutput) || len(service.submitted) != 0 {
				t.Fatalf("expected fail-closed rejection, result=%+v err=%v requests=%d", result, err, len(service.submitted))
			}
		})
	}
}

func TestAgentAcceptsSyntacticallyValidScopedPresetKey(t *testing.T) {
	service := &fakeBackgroundTaskService{}
	input := json.RawMessage(`{"prompt":"inspect","subagent_type":"review.safe"}`)
	result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-preset", Name: "Agent", Input: input}, Env{Background: service})
	if err != nil || result.IsError || len(service.submitted) != 1 {
		t.Fatalf("expected scoped preset key to reach background resolution, result=%+v err=%v requests=%d", result, err, len(service.submitted))
	}
	var payload agentTaskPayload
	if err := json.Unmarshal(service.submitted[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SubagentType != "review.safe" {
		t.Fatalf("preset key = %q, want review.safe", payload.SubagentType)
	}
}

func TestAgentAcceptanceCriteriaMaximumAndLegacyInputRemainCompatible(t *testing.T) {
	criteria := make([]string, maxAcceptanceCriteriaItems)
	for index := range criteria {
		criteria[index] = strings.Repeat("x", maxAcceptanceCriterionBytes)
	}
	service := &fakeBackgroundTaskService{}
	input, err := json.Marshal(map[string]any{"prompt": "bounded", "acceptance_criteria": criteria})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-max", Name: "Agent", Input: input}, Env{Background: service})
	if err != nil || result.IsError || len(service.submitted) != 1 {
		t.Fatalf("expected maximum bounded criteria to be accepted, result=%+v err=%v requests=%d", result, err, len(service.submitted))
	}

	legacyService := &fakeBackgroundTaskService{}
	legacyResult, err := (AgentTool{}).Execute(context.Background(), Call{ID: "agent-legacy", Name: "Agent", Input: json.RawMessage(`{"prompt":"legacy call"}`)}, Env{Background: legacyService})
	if err != nil || legacyResult.IsError || len(legacyService.submitted) != 1 {
		t.Fatalf("expected legacy input to remain compatible, result=%+v err=%v requests=%d", legacyResult, err, len(legacyService.submitted))
	}
	var payloadFields map[string]json.RawMessage
	if err := json.Unmarshal(legacyService.submitted[0].Payload, &payloadFields); err != nil {
		t.Fatal(err)
	}
	if _, ok := payloadFields["acceptanceCriteria"]; ok {
		t.Fatalf("legacy payload unexpectedly contains acceptance criteria: %s", legacyService.submitted[0].Payload)
	}
	var summaryFields map[string]json.RawMessage
	if err := json.Unmarshal(legacyService.submitted[0].PublicSummary, &summaryFields); err != nil {
		t.Fatal(err)
	}
	if _, ok := summaryFields["prompt"]; ok {
		t.Fatalf("legacy public summary contains prompt: %s", legacyService.submitted[0].PublicSummary)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
