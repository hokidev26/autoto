package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxUserQuestions          = 4
	maxUserQuestionOptions    = 4
	minUserQuestionOptions    = 2
	maxUserQuestionKeyChars   = 80
	maxUserQuestionTextChars  = 500
	maxUserOptionLabelChars   = 80
	maxUserOptionDescChars    = 400
	maxUserOtherAnswerChars   = 2000
)

// UserQuestionService blocks the tool loop until the human answers structured questions.
type UserQuestionService interface {
	AskUser(context.Context, UserQuestionRequest) (UserQuestionResponse, error)
}

type UserQuestionRequest struct {
	AgentID   string              `json:"agentId"`
	RunID     string              `json:"runId"`
	ToolUseID string              `json:"toolUseId"`
	Questions []UserQuestionItem  `json:"questions"`
}

type UserQuestionItem struct {
	Question    string               `json:"question"`
	Header      string               `json:"header"`
	Options     []UserQuestionOption `json:"options"`
	MultiSelect bool                 `json:"multiSelect,omitempty"`
}

type UserQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type UserQuestionResponse struct {
	Answers []UserQuestionAnswer `json:"answers"`
	Skipped bool                 `json:"skipped,omitempty"`
	Reason  string               `json:"reason,omitempty"`
}

type UserQuestionAnswer struct {
	Question       string   `json:"question"`
	SelectedLabels []string `json:"selectedLabels"`
	OtherText      string   `json:"otherText,omitempty"`
}

type AskUserQuestionTool struct{}

type askUserQuestionInput struct {
	Questions []askUserQuestionItemInput `json:"questions"`
}

type askUserQuestionItemInput struct {
	Question    string                     `json:"question"`
	Header      string                     `json:"header"`
	Options     []askUserQuestionOptionIn  `json:"options"`
	MultiSelect bool                       `json:"multiSelect,omitempty"`
}

type askUserQuestionOptionIn struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func (AskUserQuestionTool) Name() string { return "AskUserQuestion" }

func (AskUserQuestionTool) Description() string {
	return "Ask the human operator one to four structured multiple-choice questions and wait for their answers before continuing. Use this when a product decision, preference, or clarification is required from the user. Host identity fields such as agentId/runId/cwd must not be supplied; they are injected by the runtime."
}

func (AskUserQuestionTool) Schema() any               { return askUserQuestionInput{} }
func (AskUserQuestionTool) Risk(json.RawMessage) Risk { return RiskRead }

func (AskUserQuestionTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.UserQuestion == nil {
		return Result{Output: "user question service is unavailable", IsError: true}, nil
	}
	var input askUserQuestionInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: "invalid AskUserQuestion input: " + err.Error(), IsError: true}, nil
	}
	questions, err := normalizeUserQuestions(input.Questions)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	response, err := env.UserQuestion.AskUser(ctx, UserQuestionRequest{
		AgentID:   env.AgentID,
		RunID:     env.RunID,
		ToolUseID: call.ID,
		Questions: questions,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{Output: "user question failed: " + err.Error(), IsError: true}, nil
	}
	if response.Skipped {
		reason := strings.TrimSpace(response.Reason)
		if reason == "" {
			reason = "user skipped the question"
		}
		return Result{Output: reason, IsError: true}, nil
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return Result{Output: "user question response could not be encoded", IsError: true}, nil
	}
	return Result{Output: string(encoded)}, nil
}

func normalizeUserQuestions(items []askUserQuestionItemInput) ([]UserQuestionItem, error) {
	if len(items) < 1 || len(items) > maxUserQuestions {
		return nil, fmt.Errorf("questions must contain between 1 and %d items", maxUserQuestions)
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]UserQuestionItem, 0, len(items))
	for _, item := range items {
		question := strings.TrimSpace(item.Question)
		header := strings.TrimSpace(item.Header)
		if question == "" {
			return nil, fmt.Errorf("each question requires a non-empty question key")
		}
		if header == "" {
			return nil, fmt.Errorf("each question requires a non-empty header")
		}
		if utf8.RuneCountInString(question) > maxUserQuestionKeyChars {
			return nil, fmt.Errorf("question key exceeds maximum length")
		}
		if utf8.RuneCountInString(header) > maxUserQuestionTextChars {
			return nil, fmt.Errorf("question header exceeds maximum length")
		}
		key := strings.ToLower(question)
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("duplicate question key %q", question)
		}
		seen[key] = struct{}{}
		if len(item.Options) < minUserQuestionOptions || len(item.Options) > maxUserQuestionOptions {
			return nil, fmt.Errorf("each question requires between %d and %d options", minUserQuestionOptions, maxUserQuestionOptions)
		}
		options := make([]UserQuestionOption, 0, len(item.Options))
		optionSeen := make(map[string]struct{}, len(item.Options))
		for _, option := range item.Options {
			label := strings.TrimSpace(option.Label)
			description := strings.TrimSpace(option.Description)
			if label == "" {
				return nil, fmt.Errorf("option labels must not be empty")
			}
			if utf8.RuneCountInString(label) > maxUserOptionLabelChars {
				return nil, fmt.Errorf("option label exceeds maximum length")
			}
			if utf8.RuneCountInString(description) > maxUserOptionDescChars {
				return nil, fmt.Errorf("option description exceeds maximum length")
			}
			ol := strings.ToLower(label)
			if _, dup := optionSeen[ol]; dup {
				return nil, fmt.Errorf("duplicate option label %q", label)
			}
			optionSeen[ol] = struct{}{}
			options = append(options, UserQuestionOption{Label: label, Description: description})
		}
		out = append(out, UserQuestionItem{
			Question:    question,
			Header:      header,
			Options:     options,
			MultiSelect: item.MultiSelect,
		})
	}
	return out, nil
}
