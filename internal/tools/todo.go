package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	todoStatusPending    = "pending"
	todoStatusInProgress = "in_progress"
	todoStatusCompleted  = "completed"
)

const (
	maxTodoItems         = 50
	maxTodoContentLength = 500
	maxTodoOutputBytes   = 20000
)

type todoItem struct {
	Content string `json:"content" jsonschema:"minLength=1,maxLength=500" desc:"The step, phrased as a short imperative such as 'Add the LS tool'."`
	Status  string `json:"status" jsonschema:"enum=pending|in_progress|completed" desc:"One of pending, in_progress, or completed. At most one item in the list may be in_progress."`
}

// normalizeTodos trims and validates the list. It rejects rather than repairs a
// second in_progress item: two simultaneously active steps means the caller lost
// track of what it is doing, and silently picking one would hide that.
func normalizeTodos(items []todoItem) ([]todoItem, error) {
	if len(items) == 0 {
		return nil, errors.New("todos must contain at least one item")
	}
	if len(items) > maxTodoItems {
		return nil, fmt.Errorf("todos has %d items, the maximum is %d", len(items), maxTodoItems)
	}
	out := make([]todoItem, 0, len(items))
	inProgress := 0
	for index, item := range items {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("todos[%d] has empty content", index)
		}
		if length := len([]rune(content)); length > maxTodoContentLength {
			return nil, fmt.Errorf("todos[%d] content is %d characters, the maximum is %d", index, length, maxTodoContentLength)
		}
		status := strings.ToLower(strings.TrimSpace(item.Status))
		switch status {
		case todoStatusPending, todoStatusInProgress, todoStatusCompleted:
		default:
			return nil, fmt.Errorf("todos[%d] has status %q, want pending, in_progress, or completed", index, item.Status)
		}
		if status == todoStatusInProgress {
			inProgress++
		}
		out = append(out, todoItem{Content: content, Status: status})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("%d items are in_progress, at most one may be", inProgress)
	}
	return out, nil
}

func todoStatusMarker(status string) string {
	switch status {
	case todoStatusCompleted:
		return "[x]"
	case todoStatusInProgress:
		return "[>]"
	default:
		return "[ ]"
	}
}

func renderTodos(items []todoItem) string {
	counts := make(map[string]int, 3)
	var builder strings.Builder
	for _, item := range items {
		counts[item.Status]++
		builder.WriteString(todoStatusMarker(item.Status))
		builder.WriteByte(' ')
		builder.WriteString(item.Content)
		builder.WriteByte('\n')
	}
	fmt.Fprintf(&builder, "%d todos: %d pending, %d in progress, %d completed",
		len(items), counts[todoStatusPending], counts[todoStatusInProgress], counts[todoStatusCompleted])
	return builder.String()
}

type TodoWriteTool struct{}

type todoWriteInput struct {
	Todos []todoItem `json:"todos" desc:"The complete ordered todo list, which replaces any previous list. At least 1 and at most 50 items, with at most one item in_progress."`
}

func (TodoWriteTool) Name() string { return "TodoWrite" }
func (TodoWriteTool) Description() string {
	return "Record the current plan as an ordered todo list. Send the whole list every time; it replaces the previous one. Nothing is written to disk."
}
func (TodoWriteTool) Schema() any { return todoWriteInput{} }

// Risk is RiskRead: the list is recorded and echoed back, never persisted to the
// filesystem and never executed.
func (TodoWriteTool) Risk(json.RawMessage) Risk { return RiskRead }

func (TodoWriteTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input todoWriteInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	items, err := normalizeTodos(input.Todos)
	if err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	rendered, cut := truncate(renderTodos(items), maxTodoOutputBytes)
	// Meta carries the structured list so the UI can render checkboxes instead of
	// re-parsing the text rendering.
	meta := make([]map[string]any, 0, len(items))
	for _, item := range items {
		meta = append(meta, map[string]any{"content": item.Content, "status": item.Status})
	}
	return Result{Output: rendered, Meta: map[string]any{
		"todos":     meta,
		"count":     len(items),
		"truncated": cut,
	}}, nil
}
