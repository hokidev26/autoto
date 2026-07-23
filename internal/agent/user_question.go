package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/tools"
)

const (
	userQuestionTimeout        = 30 * time.Minute
	maxUserOtherAnswerRunes    = 2000
)

type pendingUserQuestion struct {
	AgentID   string
	RunID     string
	ToolUseID string
	Questions []tools.UserQuestionItem
	ExpiresAt time.Time
	Decision  chan tools.UserQuestionResponse
}

// AnswerUserQuestionRequest is the API payload for submitting structured answers.
type AnswerUserQuestionRequest struct {
	Answers []tools.UserQuestionAnswer `json:"answers"`
	Skipped bool                       `json:"skipped,omitempty"`
	Reason  string                     `json:"reason,omitempty"`
}

func (r *Runner) AskUser(ctx context.Context, req tools.UserQuestionRequest) (tools.UserQuestionResponse, error) {
	if r == nil {
		return tools.UserQuestionResponse{}, fmt.Errorf("agent runner is not initialized")
	}
	agentID := strings.TrimSpace(req.AgentID)
	toolUseID := strings.TrimSpace(req.ToolUseID)
	if agentID == "" || toolUseID == "" {
		return tools.UserQuestionResponse{}, fmt.Errorf("agentId and toolUseId are required")
	}
	if len(req.Questions) == 0 {
		return tools.UserQuestionResponse{}, fmt.Errorf("questions are required")
	}
	pending := &pendingUserQuestion{
		AgentID:   agentID,
		RunID:     strings.TrimSpace(req.RunID),
		ToolUseID: toolUseID,
		Questions: append([]tools.UserQuestionItem(nil), req.Questions...),
		ExpiresAt: time.Now().Add(userQuestionTimeout),
		Decision:  make(chan tools.UserQuestionResponse, 1),
	}
	r.addPendingUserQuestion(pending)
	defer r.removePendingUserQuestion(agentID, toolUseID)

	payload := map[string]any{
		"toolUseId":  toolUseID,
		"toolName":   "AskUserQuestion",
		"questions":  req.Questions,
		"expiresAt":  pending.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"runId":      pending.RunID,
		"kind":       "user_question",
	}
	r.publish(Event{Type: "user.question_required", AgentID: agentID, Data: payload})
	r.notify(NotificationEvent{
		Event:     "user_question_required",
		RunID:     pending.RunID,
		AgentID:   agentID,
		Status:    "pending_user_question",
		ToolUseID: toolUseID,
		ToolName:  "AskUserQuestion",
	})

	timer := time.NewTimer(userQuestionTimeout)
	defer timer.Stop()
	select {
	case response := <-pending.Decision:
		return response, nil
	case <-timer.C:
		return tools.UserQuestionResponse{Skipped: true, Reason: "user question timed out"}, nil
	case <-ctx.Done():
		return tools.UserQuestionResponse{}, ctx.Err()
	}
}

func (r *Runner) AnswerUserQuestion(ctx context.Context, agentID, toolUseID string, req AnswerUserQuestionRequest) (bool, error) {
	_ = ctx
	agentID = strings.TrimSpace(agentID)
	toolUseID = strings.TrimSpace(toolUseID)
	if agentID == "" || toolUseID == "" {
		return false, fmt.Errorf("agentId and toolUseId are required")
	}
	r.userQuestionMu.Lock()
	pending := r.userQuestions[approvalKey(agentID, toolUseID)]
	r.userQuestionMu.Unlock()
	if pending == nil {
		return false, nil
	}
	response, err := normalizeUserQuestionAnswer(pending.Questions, req)
	if err != nil {
		return false, err
	}
	select {
	case pending.Decision <- response:
		return true, nil
	default:
		return false, nil
	}
}

func (r *Runner) ListPendingUserQuestions(agentID string) []map[string]any {
	agentID = strings.TrimSpace(agentID)
	r.userQuestionMu.Lock()
	defer r.userQuestionMu.Unlock()
	out := make([]map[string]any, 0)
	for _, pending := range r.userQuestions {
		if pending == nil {
			continue
		}
		if agentID != "" && pending.AgentID != agentID {
			continue
		}
		out = append(out, map[string]any{
			"agentId":   pending.AgentID,
			"runId":     pending.RunID,
			"toolUseId": pending.ToolUseID,
			"toolName":  "AskUserQuestion",
			"questions": pending.Questions,
			"expiresAt": pending.ExpiresAt.UTC().Format(time.RFC3339Nano),
			"kind":      "user_question",
		})
	}
	return out
}

func (r *Runner) addPendingUserQuestion(pending *pendingUserQuestion) {
	r.userQuestionMu.Lock()
	defer r.userQuestionMu.Unlock()
	if r.userQuestions == nil {
		r.userQuestions = make(map[string]*pendingUserQuestion)
	}
	r.userQuestions[approvalKey(pending.AgentID, pending.ToolUseID)] = pending
}

func (r *Runner) removePendingUserQuestion(agentID, toolUseID string) {
	r.userQuestionMu.Lock()
	defer r.userQuestionMu.Unlock()
	delete(r.userQuestions, approvalKey(agentID, toolUseID))
}

func (r *Runner) cancelPendingUserQuestions(agentID, reason string) int {
	if strings.TrimSpace(reason) == "" {
		reason = "user question canceled"
	}
	r.userQuestionMu.Lock()
	pending := make([]*pendingUserQuestion, 0)
	for _, item := range r.userQuestions {
		if agentID == "" || item.AgentID == agentID {
			pending = append(pending, item)
		}
	}
	r.userQuestionMu.Unlock()
	for _, item := range pending {
		select {
		case item.Decision <- tools.UserQuestionResponse{Skipped: true, Reason: reason}:
		default:
		}
	}
	return len(pending)
}

func normalizeUserQuestionAnswer(questions []tools.UserQuestionItem, req AnswerUserQuestionRequest) (tools.UserQuestionResponse, error) {
	if req.Skipped {
		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			reason = "user skipped the question"
		}
		return tools.UserQuestionResponse{Skipped: true, Reason: reason}, nil
	}
	if len(req.Answers) != len(questions) {
		return tools.UserQuestionResponse{}, fmt.Errorf("answers must cover every question (%d expected, got %d)", len(questions), len(req.Answers))
	}
	byKey := make(map[string]tools.UserQuestionItem, len(questions))
	for _, question := range questions {
		byKey[strings.ToLower(question.Question)] = question
	}
	seen := make(map[string]struct{}, len(req.Answers))
	out := make([]tools.UserQuestionAnswer, 0, len(req.Answers))
	for _, answer := range req.Answers {
		key := strings.TrimSpace(answer.Question)
		if key == "" {
			return tools.UserQuestionResponse{}, fmt.Errorf("each answer requires a question key")
		}
		lk := strings.ToLower(key)
		question, ok := byKey[lk]
		if !ok {
			return tools.UserQuestionResponse{}, fmt.Errorf("unknown question key %q", key)
		}
		if _, dup := seen[lk]; dup {
			return tools.UserQuestionResponse{}, fmt.Errorf("duplicate answer for question %q", key)
		}
		seen[lk] = struct{}{}
		labels := make([]string, 0, len(answer.SelectedLabels))
		labelSet := make(map[string]struct{}, len(question.Options))
		for _, option := range question.Options {
			labelSet[strings.ToLower(option.Label)] = struct{}{}
		}
		for _, label := range answer.SelectedLabels {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			if _, allowed := labelSet[strings.ToLower(label)]; !allowed {
				return tools.UserQuestionResponse{}, fmt.Errorf("selected label %q is not a valid option for question %q", label, key)
			}
			labels = append(labels, label)
		}
		other := strings.TrimSpace(answer.OtherText)
		if utf8.RuneCountInString(other) > maxUserOtherAnswerRunes {
			return tools.UserQuestionResponse{}, fmt.Errorf("otherText exceeds maximum length")
		}
		if len(labels) == 0 && other == "" {
			return tools.UserQuestionResponse{}, fmt.Errorf("answer for question %q requires a selection or otherText", key)
		}
		if !question.MultiSelect && len(labels) > 1 {
			return tools.UserQuestionResponse{}, fmt.Errorf("question %q does not allow multiSelect", key)
		}
		out = append(out, tools.UserQuestionAnswer{
			Question:       question.Question,
			SelectedLabels: labels,
			OtherText:      other,
		})
	}
	if len(seen) != len(questions) {
		return tools.UserQuestionResponse{}, fmt.Errorf("answers must cover every question")
	}
	return tools.UserQuestionResponse{Answers: out}, nil
}
