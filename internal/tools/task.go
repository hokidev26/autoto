package tools

import (
	"context"
	"encoding/json"
	"strings"
)

const (
	defaultTaskListLimit   = 20
	maxTaskListLimit       = 100
	defaultTaskOutputBytes = 64 * 1024
	maxTaskOutputBytes     = 64 * 1024
	defaultTaskWaitMS      = int64(30_000)
	maxTaskWaitMS          = int64(30_000)
)

type TaskTool struct{}

type taskInput struct {
	Action        string `json:"action" jsonschema:"enum=list|status|output|wait|cancel" desc:"What to do: list background tasks, read one task's status, read its output, wait for it to finish, or cancel it."`
	TaskID        string `json:"task_id,omitempty" desc:"Target task. Required for status, output, wait, and cancel."`
	Status        string `json:"status,omitempty" desc:"Filter for the list action, matching a task status such as running or completed."`
	Kind          string `json:"kind,omitempty" desc:"Filter for the list action, matching a task kind such as shell or agent."`
	AfterSequence int64  `json:"after_sequence,omitempty" jsonschema:"minimum=0" desc:"For the output action, return only chunks after this sequence number so output can be streamed incrementally."`
	Limit         int    `json:"limit,omitempty" jsonschema:"minimum=1" desc:"Maximum number of tasks the list action returns."`
	LimitBytes    int    `json:"limit_bytes,omitempty" jsonschema:"minimum=1" desc:"Maximum bytes of output to return."`
	TimeoutMS     int64  `json:"timeout_ms,omitempty" jsonschema:"minimum=1" desc:"For the wait action, how long to wait in milliseconds before returning without a result."`
}

func (TaskTool) Name() string { return "Task" }
func (TaskTool) Description() string {
	return "List, inspect, read output from, wait for, or cancel durable background tasks."
}
func (TaskTool) Schema() any { return taskInput{} }
func (TaskTool) Risk(raw json.RawMessage) Risk {
	var input taskInput
	_ = json.Unmarshal(raw, &input)
	if strings.EqualFold(strings.TrimSpace(input.Action), "cancel") {
		return RiskExec
	}
	return RiskRead
}

func (TaskTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	if env.Background == nil {
		return Result{Output: "background task service is unavailable", IsError: true}, nil
	}
	var input taskInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.Status = strings.TrimSpace(input.Status)
	input.Kind = strings.TrimSpace(input.Kind)
	encode := func(value any) Result {
		data, err := json.Marshal(value)
		if err != nil {
			return Result{Output: "background task result could not be encoded", IsError: true}
		}
		return Result{Output: string(data)}
	}
	failed := func(message string) Result { return Result{Output: message, IsError: true} }

	switch input.Action {
	case "list":
		limit := input.Limit
		if limit <= 0 {
			limit = defaultTaskListLimit
		}
		if limit > maxTaskListLimit {
			return failed("limit exceeds maximum"), nil
		}
		tasks, err := env.Background.List(ctx, BackgroundTaskListOptions{OwnerAgentID: env.AgentID, Status: input.Status, Kind: input.Kind, Limit: limit})
		if err != nil {
			return failed("background tasks could not be listed"), nil
		}
		return encode(tasks), nil
	case "status":
		if input.TaskID == "" {
			return failed("task_id is required"), nil
		}
		task, err := env.Background.Get(ctx, env.AgentID, input.TaskID)
		if err != nil {
			return failed("background task was not found"), nil
		}
		return encode(task), nil
	case "output":
		if input.TaskID == "" {
			return failed("task_id is required"), nil
		}
		if input.AfterSequence < 0 {
			return failed("after_sequence must not be negative"), nil
		}
		limitBytes := input.LimitBytes
		if limitBytes <= 0 {
			limitBytes = defaultTaskOutputBytes
		}
		if limitBytes > maxTaskOutputBytes {
			return failed("limit_bytes exceeds maximum"), nil
		}
		page, err := env.Background.Output(ctx, env.AgentID, input.TaskID, input.AfterSequence, limitBytes)
		if err != nil {
			return failed("background task output is unavailable"), nil
		}
		return encode(page), nil
	case "wait":
		if input.TaskID == "" {
			return failed("task_id is required"), nil
		}
		timeoutMS := input.TimeoutMS
		if timeoutMS <= 0 {
			timeoutMS = defaultTaskWaitMS
		}
		if timeoutMS > maxTaskWaitMS {
			return failed("timeout_ms exceeds maximum"), nil
		}
		task, err := env.Background.Wait(ctx, env.AgentID, input.TaskID, timeoutMS)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return failed("background task wait failed"), nil
		}
		return encode(task), nil
	case "cancel":
		if input.TaskID == "" {
			return failed("task_id is required"), nil
		}
		task, err := env.Background.Cancel(ctx, env.AgentID, input.TaskID)
		if err != nil {
			return failed("background task could not be canceled"), nil
		}
		return encode(task), nil
	default:
		return failed("action must be list, status, output, wait, or cancel"), nil
	}
}
