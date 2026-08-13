package agent

import (
	"log/slog"
	"runtime/debug"
)

// recoverGoroutine is the terminal recover for fire-and-forget goroutines,
// which have no caller to hand an error to: an unrecovered panic in one of
// them takes down the whole process, not just the background job that failed.
// Use via defer, registered before any cleanup defers so those still run
// during unwinding. logArgs carries slog key-value context (agent/run id).
func recoverGoroutine(name string, logArgs ...any) {
	value := recover()
	if value == nil {
		return
	}
	args := append([]any{"goroutine", name, "panic", value, "stack", string(debug.Stack())}, logArgs...)
	slog.Error("background goroutine panicked", args...)
}
