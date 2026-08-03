package background

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"autoto/internal/db"
)

var (
	ErrAlreadyStarted  = errors.New("background manager already started")
	ErrClosed          = errors.New("background manager is closed")
	ErrNilExecutor     = errors.New("background executor is nil")
	ErrExecutorExists  = errors.New("background executor already registered")
	ErrUnknownExecutor = errors.New("background executor is not registered")
)

type Executor interface {
	Execute(context.Context, db.BackgroundTask, OutputWriter) (Result, error)
}

type ExecutorFunc func(context.Context, db.BackgroundTask, OutputWriter) (Result, error)

func (fn ExecutorFunc) Execute(ctx context.Context, task db.BackgroundTask, output OutputWriter) (Result, error) {
	return fn(ctx, task, output)
}

type Result struct {
	JSON      json.RawMessage
	ExitCode  *int
	ErrorCode string
}

type OutputWriter interface {
	Write(stream string, chunk []byte) error
	Truncated() bool
}

type TerminalHook func(context.Context, db.BackgroundTask)

type TaskValidator func(context.Context, db.BackgroundTask) error

// TaskEventHook receives safe lifecycle metadata only. Implementations must not
// include task payloads or output bytes in externally visible events.
type TaskEventHook func(context.Context, string, db.BackgroundTask)

type Options struct {
	WorkerCount          int
	PerAgentLimit        int
	AllowNestedSubagents bool
	MaxSubagentDepth     int
	OutputLimitBytes     int64
	OutputChunkBytes     int
	PollInterval         time.Duration
	WorkerInstanceID     string
}

func (options Options) withDefaults() Options {
	if options.WorkerCount <= 0 {
		options.WorkerCount = 8
	} else if options.WorkerCount > 16 {
		options.WorkerCount = 16
	}
	if options.PerAgentLimit <= 0 {
		options.PerAgentLimit = 4
	} else if options.PerAgentLimit > 8 {
		options.PerAgentLimit = 8
	}
	if options.MaxSubagentDepth < 2 {
		options.MaxSubagentDepth = 2
	} else if options.MaxSubagentDepth > 4 {
		options.MaxSubagentDepth = 4
	}
	if options.OutputLimitBytes <= 0 || options.OutputLimitBytes > db.BackgroundTaskDefaultOutputMax {
		options.OutputLimitBytes = db.BackgroundTaskDefaultOutputMax
	}
	if options.OutputChunkBytes <= 0 || options.OutputChunkBytes > db.BackgroundTaskOutputChunkBytes {
		options.OutputChunkBytes = db.BackgroundTaskOutputChunkBytes
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	return options
}
