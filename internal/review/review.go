// Package review implements the isolated, tool-free reviewer boundary.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"autoto/internal/providers"
)

const (
	defaultTimeout       = 30 * time.Second
	maxResponseBytes     = 64 * 1024
	maxPlanFieldBytes    = 16 * 1024
	maxPlanListItems     = 64
	maxReviewReasonBytes = 8 * 1024
)

// ReviewVerdict is deliberately a closed set. An unavailable reviewer is never
// interpreted as approval.
type ReviewVerdict string

const (
	VerdictPass             ReviewVerdict = "pass"
	VerdictNeedsHuman       ReviewVerdict = "needs_human"
	VerdictBlockRecommended ReviewVerdict = "block_recommended"
	VerdictUnavailable      ReviewVerdict = "unavailable"
)

// Result is a reviewer decision. Only VerdictPass is an affirmative review;
// callers must continue enforcing execution policy independently.
type Result struct {
	Verdict ReviewVerdict `json:"verdict"`
	Reason  string        `json:"reason"`
}

// PlanDraft is the machine-readable output required from a plan-mode run.
// The runner, rather than the model, associates it with a run before storage.
type PlanDraft struct {
	Goal        string   `json:"goal"`
	Assumptions []string `json:"assumptions"`
	Steps       []string `json:"steps"`
	Risks       []string `json:"risks"`
	Tests       []string `json:"tests"`
	Rollback    []string `json:"rollback"`
}

// Request provides a bounded, structured subject to the reviewer.
type Request struct {
	Subject string    `json:"subject"`
	Draft   PlanDraft `json:"draft"`
}

// Config names the dedicated reviewer model and bounds a single review call.
type Config struct {
	Model   string
	Timeout time.Duration
}

// Service invokes only the Provider Registry. It has no tool registry and
// always sends a GenerateRequest with a nil Tools field.
type Service struct {
	registry *providers.Registry
	model    string
	timeout  time.Duration
}

func NewService(registry *providers.Registry, model string) *Service {
	return NewServiceWithConfig(registry, Config{Model: model})
}

func NewServiceWithConfig(registry *providers.Registry, cfg Config) *Service {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Service{registry: registry, model: strings.TrimSpace(cfg.Model), timeout: cfg.Timeout}
}

// ReviewerID is a bounded audit identity for the configured dedicated model.
func (s *Service) ReviewerID() string {
	if s == nil || strings.TrimSpace(s.model) == "" {
		return "system:reviewer-unavailable"
	}
	value := "model:" + strings.TrimSpace(s.model)
	if len(value) > 200 {
		return value[:200]
	}
	return value
}

// Review performs a single, tool-free reviewer request. Provider errors,
// timeouts, protocol violations, and malformed JSON all return unavailable so
// a caller cannot accidentally treat an indeterminate review as a pass.
func (s *Service) Review(ctx context.Context, request Request) (Result, error) {
	if s == nil || s.registry == nil {
		return unavailable("review service is not configured"), errors.New("review service is not configured")
	}
	if strings.TrimSpace(s.model) == "" {
		return unavailable("reviewer model is not configured"), errors.New("reviewer model is not configured")
	}
	provider, model, err := s.registry.Resolve(s.model)
	if err != nil {
		return unavailable("reviewer model is unavailable"), fmt.Errorf("resolve reviewer model: %w", err)
	}
	prompt, err := reviewPrompt(request)
	if err != nil {
		return unavailable("review request is invalid"), err
	}

	reviewCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	events, err := provider.Generate(reviewCtx, providers.GenerateRequest{
		Model:        model,
		SystemPrompt: reviewerSystemPrompt,
		Messages: []providers.Message{{
			Role:    "user",
			Content: prompt,
			Blocks:  []providers.ContentBlock{{Type: "text", Text: prompt}},
		}},
		// Deliberately omit ToolSpecs. A reviewer is never an Agent execution loop.
		Tools: nil,
	})
	if err != nil {
		return unavailable("reviewer request failed"), fmt.Errorf("generate review: %w", err)
	}
	return collectVerdict(reviewCtx, events)
}

const reviewerSystemPrompt = `You are an isolated plan reviewer. You cannot use tools and you must not authorize execution. Review the supplied plan only. Reply with exactly one JSON object and no markdown: {"verdict":"pass|needs_human|block_recommended|unavailable","reason":"concise explanation"}.

You MUST return needs_human when the plan's assumptions, risks, tests, or steps admit the stated goal was not achieved, the evidence is for the wrong target, or the steps only report a failed inspection instead of a viable next action. The reason must be a concrete correction for the next plan.

Ordinary residual risks of a viable plan are not automatic needs_human. pass is only for a viable path to the goal. block_recommended remains for dangerous or unbounded plans.`

func reviewPrompt(request Request) (string, error) {
	request.Subject = strings.TrimSpace(request.Subject)
	if request.Subject == "" {
		return "", errors.New("review subject is required")
	}
	if err := ValidatePlanDraft(request.Draft); err != nil {
		return "", fmt.Errorf("invalid plan draft: %w", err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode review request: %w", err)
	}
	return string(payload), nil
}

func collectVerdict(ctx context.Context, events <-chan providers.Event) (Result, error) {
	var output strings.Builder
	for {
		select {
		case <-ctx.Done():
			return unavailable("reviewer timed out or was canceled"), ctx.Err()
		case event, ok := <-events:
			if !ok {
				return parseVerdict(output.String())
			}
			switch event.Type {
			case "text":
				if output.Len()+len(event.Text) > maxResponseBytes {
					return unavailable("reviewer response exceeded size limit"), errors.New("reviewer response exceeded size limit")
				}
				output.WriteString(event.Text)
			case "tool_call":
				return unavailable("reviewer attempted a tool call"), errors.New("reviewer attempted a tool call")
			case "error":
				return unavailable("reviewer returned an error"), errors.New(strings.TrimSpace(event.Text))
			case "done":
				if strings.TrimSpace(event.StopReason) == "not_configured" {
					return unavailable("reviewer provider is not configured"), errors.New("reviewer provider is not configured")
				}
				return parseVerdict(output.String())
			}
		}
	}
}

// ParseVerdict accepts only the exact reviewer protocol. It refuses unknown
// fields and all values outside the closed ReviewVerdict set.
func ParseVerdict(text string) (Result, error) {
	return parseVerdict(text)
}

func parseVerdict(text string) (Result, error) {
	raw, err := decodeUniqueJSONObject(text)
	if err != nil {
		return unavailable("reviewer returned invalid JSON"), fmt.Errorf("parse reviewer JSON: %w", err)
	}
	if len(raw) != 2 {
		return unavailable("reviewer response has an invalid shape"), errors.New("reviewer response must contain exactly verdict and reason")
	}
	verdictRaw, hasVerdict := raw["verdict"]
	reasonRaw, hasReason := raw["reason"]
	if !hasVerdict || !hasReason {
		return unavailable("reviewer response is missing required fields"), errors.New("reviewer response requires verdict and reason")
	}
	var verdict ReviewVerdict
	var reason string
	if err := json.Unmarshal(verdictRaw, &verdict); err != nil {
		return unavailable("reviewer verdict is invalid"), errors.New("reviewer verdict must be a string")
	}
	if err := json.Unmarshal(reasonRaw, &reason); err != nil {
		return unavailable("reviewer reason is invalid"), errors.New("reviewer reason must be a string")
	}
	reason = strings.TrimSpace(reason)
	if !validVerdict(verdict) {
		return unavailable("reviewer returned an unknown verdict"), fmt.Errorf("unknown review verdict %q", verdict)
	}
	if reason == "" || len(reason) > maxReviewReasonBytes || !utf8.ValidString(reason) {
		return unavailable("reviewer reason is invalid"), errors.New("reviewer reason is required and bounded")
	}
	return Result{Verdict: verdict, Reason: reason}, nil
}

func unavailable(reason string) Result {
	return Result{Verdict: VerdictUnavailable, Reason: reason}
}

func validVerdict(verdict ReviewVerdict) bool {
	switch verdict {
	case VerdictPass, VerdictNeedsHuman, VerdictBlockRecommended, VerdictUnavailable:
		return true
	default:
		return false
	}
}

// ParsePlanDraft enforces the exact six-field plan protocol emitted by plan
// runs. Its strictness prevents prose or partial plans from entering review.
func ParsePlanDraft(text string) (PlanDraft, error) {
	raw, err := decodeUniqueJSONObject(text)
	if err != nil {
		return PlanDraft{}, fmt.Errorf("parse plan JSON: %w", err)
	}
	expected := map[string]bool{
		"goal": true, "assumptions": true, "steps": true, "risks": true, "tests": true, "rollback": true,
	}
	if len(raw) != len(expected) {
		return PlanDraft{}, errors.New("plan draft must contain exactly goal, assumptions, steps, risks, tests, and rollback")
	}
	for key := range raw {
		if !expected[key] {
			return PlanDraft{}, fmt.Errorf("plan draft contains unknown field %q", key)
		}
	}
	var draft PlanDraft
	if err := json.Unmarshal(raw["goal"], &draft.Goal); err != nil {
		return PlanDraft{}, errors.New("plan goal must be a string")
	}
	for _, field := range []struct {
		name string
		dest *[]string
		raw  json.RawMessage
	}{
		{name: "assumptions", dest: &draft.Assumptions, raw: raw["assumptions"]},
		{name: "steps", dest: &draft.Steps, raw: raw["steps"]},
		{name: "risks", dest: &draft.Risks, raw: raw["risks"]},
		{name: "tests", dest: &draft.Tests, raw: raw["tests"]},
		{name: "rollback", dest: &draft.Rollback, raw: raw["rollback"]},
	} {
		if err := json.Unmarshal(field.raw, field.dest); err != nil {
			return PlanDraft{}, fmt.Errorf("plan %s must be an array of strings", field.name)
		}
	}
	if err := ValidatePlanDraft(draft); err != nil {
		return PlanDraft{}, err
	}
	return draft, nil
}

func ValidatePlanDraft(draft PlanDraft) error {
	if value, err := boundedPlanText("goal", draft.Goal, true); err != nil {
		return err
	} else {
		draft.Goal = value
	}
	for _, field := range []struct {
		name  string
		items []string
	}{
		{name: "assumptions", items: draft.Assumptions},
		{name: "steps", items: draft.Steps},
		{name: "risks", items: draft.Risks},
		{name: "tests", items: draft.Tests},
		{name: "rollback", items: draft.Rollback},
	} {
		if field.items == nil || len(field.items) > maxPlanListItems {
			return fmt.Errorf("plan %s must contain at most %d items", field.name, maxPlanListItems)
		}
		for _, item := range field.items {
			if _, err := boundedPlanText(field.name, item, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func boundedPlanText(name, value string, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("plan %s must not be empty", name)
	}
	if len(value) > maxPlanFieldBytes || !utf8.ValidString(value) {
		return "", fmt.Errorf("plan %s is invalid or too large", name)
	}
	return value, nil
}

func decodeUniqueJSONObject(text string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("JSON object is required")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key must be a string")
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate JSON field %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	if token, err := decoder.Token(); err != nil {
		return nil, err
	} else if delimiter, ok := token.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("JSON object did not terminate")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are not allowed")
		}
		return nil, err
	}
	return result, nil
}

// PlanStore is the durable persistence contract consumed by the Runner and
// implemented by db.Store. It keeps review orchestration separate from the
// tool-free reviewer itself.
type PlanStore interface {
	PersistPlanDraft(context.Context, string, PlanDraft) error
	TriggerPlanReview(context.Context, string) error
	PersistPlanReview(context.Context, string, string, Result) error
}
