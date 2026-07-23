package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	defaultContextAskMaxChars  = 6000
	minContextAskMaxChars      = 512
	maxContextAskMaxChars      = 12000
	maxContextAskTaskIDChars   = 200
	maxContextAskRunIDChars    = 200
	maxContextAskQuestions     = 8
	maxContextAskQuestionChars = 2000
)

type ContextAskService interface {
	AskContext(context.Context, ContextAskRequest) (ContextAskResponse, error)
}

type ContextAskRequest struct {
	RequesterAgentID string   `json:"requesterAgentId"`
	RequesterRunID   string   `json:"requesterRunId"`
	TaskID           string   `json:"taskId"`
	ChildAgentID     string   `json:"childAgentId"`
	RunID            string   `json:"runId"`
	Questions        []string `json:"questions"`
	IncludeEvidence  bool     `json:"includeEvidence"`
	MaxChars         int      `json:"maxChars"`
}

type ContextAskResponse struct {
	TaskID        string               `json:"taskId"`
	ChildAgentID  string               `json:"childAgentId"`
	RunID         string               `json:"runId"`
	Answers       []ContextAskAnswer   `json:"answers"`
	Evidence      []ContextAskEvidence `json:"evidence"`
	Coverage      string               `json:"coverage"`
	Partial       bool                 `json:"partial"`
	Truncated     bool                 `json:"truncated"`
	PossiblyStale bool                 `json:"possiblyStale"`
	Limitations   []string             `json:"limitations"`
	Cached        bool                 `json:"cached"`
}

type ContextAskAnswer struct {
	Question   string            `json:"question"`
	Answer     string            `json:"answer"`
	Confidence string            `json:"confidence"`
	Claims     []ContextAskClaim `json:"claims"`
}

type ContextAskClaim struct {
	Text        string   `json:"text"`
	EvidenceIDs []string `json:"evidenceIds"`
}

type ContextAskEvidence struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SourceID string `json:"sourceId,omitempty"`
	Trust    string `json:"trust"`
	Derived  bool   `json:"derived"`
	Digest   string `json:"digest"`
	// Excerpt is internal-only evidence supplied to the isolated summary model.
	// It must never be serialized back into the parent model's tool result.
	Excerpt string `json:"-"`
}

type ContextAskTool struct{}

type contextAskInput struct {
	TaskID          string   `json:"task_id"`
	Questions       []string `json:"questions"`
	RunID           string   `json:"run_id,omitempty"`
	IncludeEvidence *bool    `json:"include_evidence,omitempty"`
	MaxChars        *int     `json:"max_chars,omitempty"`
}

func (ContextAskTool) Name() string { return "ContextAsk" }

func (ContextAskTool) Description() string {
	return "Read-only query of the direct child agent created by task_id. The task must belong to this agent and the current parent run; run_id, when provided, must exactly match the task's recorded child run. Questions are answered from that child run's durable context; this tool does not send any message or invoke tools in the target agent. include_evidence returns only untrusted/derived/digest metadata and claim IDs, never raw evidence excerpts."
}

func (ContextAskTool) Schema() any               { return contextAskInput{} }
func (ContextAskTool) Risk(json.RawMessage) Risk { return RiskRead }

func (ContextAskTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.Background == nil {
		return contextAskFailure("background task service is unavailable"), nil
	}
	if env.ContextAsk == nil {
		return contextAskFailure("context ask service is unavailable"), nil
	}

	var input contextAskInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return contextAskFailure(err.Error()), nil
	}
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.RunID = strings.TrimSpace(input.RunID)
	if input.TaskID == "" {
		return contextAskFailure("task_id is required"), nil
	}
	if utf8.RuneCountInString(input.TaskID) > maxContextAskTaskIDChars {
		return contextAskFailure("task_id exceeds maximum length"), nil
	}
	if utf8.RuneCountInString(input.RunID) > maxContextAskRunIDChars {
		return contextAskFailure("run_id exceeds maximum length"), nil
	}
	if len(input.Questions) < 1 || len(input.Questions) > maxContextAskQuestions {
		return contextAskFailure("questions must contain between 1 and 8 items"), nil
	}
	questions := make([]string, len(input.Questions))
	for index, question := range input.Questions {
		question = strings.TrimSpace(question)
		if question == "" {
			return contextAskFailure("questions must not contain empty items"), nil
		}
		if utf8.RuneCountInString(question) > maxContextAskQuestionChars {
			return contextAskFailure("question exceeds maximum length"), nil
		}
		questions[index] = question
	}

	includeEvidence := true
	if input.IncludeEvidence != nil {
		includeEvidence = *input.IncludeEvidence
	}
	maxChars := defaultContextAskMaxChars
	if input.MaxChars != nil {
		maxChars = *input.MaxChars
	}
	if maxChars < minContextAskMaxChars || maxChars > maxContextAskMaxChars {
		return contextAskFailure("max_chars must be between 512 and 12000"), nil
	}

	task, err := env.Background.Get(ctx, env.AgentID, input.TaskID)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return contextAskFailure("context target is unavailable"), nil
	}
	childAgentID := strings.TrimSpace(task.ChildAgentID)
	parentRunID := strings.TrimSpace(task.ParentRunID)
	childRunID := strings.TrimSpace(task.ChildRunID)
	if task.Kind != BackgroundTaskKindAgent || childAgentID == "" || strings.TrimSpace(env.RunID) == "" || parentRunID != strings.TrimSpace(env.RunID) || childRunID == "" {
		return contextAskFailure("context target is unavailable"), nil
	}
	if input.RunID != "" && input.RunID != childRunID {
		return contextAskFailure("context target is unavailable"), nil
	}

	runID := childRunID
	response, err := env.ContextAsk.AskContext(ctx, ContextAskRequest{
		RequesterAgentID: env.AgentID,
		RequesterRunID:   env.RunID,
		TaskID:           input.TaskID,
		ChildAgentID:     childAgentID,
		RunID:            runID,
		Questions:        questions,
		IncludeEvidence:  includeEvidence,
		MaxChars:         maxChars,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, err
		}
		return contextAskFailure("child agent context could not be queried"), nil
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return contextAskFailure("context ask response could not be encoded"), nil
	}
	return Result{Output: string(encoded)}, nil
}

func contextAskFailure(message string) Result {
	return Result{Output: message, IsError: true}
}
