//go:build windows

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func executeBash(t *testing.T, command string, timeout time.Duration) (Result, time.Duration) {
	t.Helper()
	input, err := json.Marshal(bashInput{Command: command, Timeout: int(timeout / time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := (BashTool{}).Execute(context.Background(), Call{ID: "bash-windows", Name: "Bash", Input: input}, Env{CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return result, time.Since(started)
}

// Passing the command as an argv entry lets Go escape it with C-runtime rules,
// which cmd.exe does not speak: every quote arrives with a backslash in front.
// The visible damage was `start "" "<url>"` turning into a request to open a
// file named \\, whose modal error dialog blocked the shell until the timeout.
func TestBashKeepsQuotesTheShellCanRead(t *testing.T) {
	result, _ := executeBash(t, `echo "a b"`, 10*time.Second)
	if result.IsError {
		t.Fatalf("quoted echo failed: %+v", result)
	}
	if strings.Contains(result.Output, `\"`) {
		t.Fatalf("cmd.exe saw C-escaped quotes: %q", result.Output)
	}
	if !strings.Contains(result.Output, `"a b"`) {
		t.Fatalf("expected the quotes to reach cmd.exe intact, got %q", result.Output)
	}
}

// `start` hands the console on to a process it does not wait for, so that
// process keeps the inherited stdout pipe open long after the shell is gone.
// Waiting on the pipe instead of on the command is what made opening a URL cost
// a full timeout and report failure.
func TestBashDoesNotWaitOnAProcessTheShellDetached(t *testing.T) {
	timeout := 30 * time.Second
	result, elapsed := executeBash(t, `start "" /B ping -n 20 127.0.0.1`, timeout)
	if elapsed > timeout/3 {
		t.Fatalf("waited %s on a detached grandchild", elapsed)
	}
	if result.IsError {
		t.Fatalf("the shell itself succeeded, so the result must not be an error: %+v", result)
	}
}
