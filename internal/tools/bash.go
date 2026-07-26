package tools

import (
	"context"
	"encoding/json"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"autoto/internal/process"
)

type BashTool struct{}

const (
	bashResultMaxBytes = 20000
	bashStreamMaxBytes = 100000
	bashMaxTimeout     = 30 * time.Minute
)

type bashInput struct {
	Command         string `json:"command" desc:"Shell command to run in the working directory. Uses cmd.exe on Windows and /bin/sh elsewhere. Destructive commands are blocked or require approval."`
	Timeout         int    `json:"timeout,omitempty" jsonschema:"minimum=1,maximum=1800000" desc:"Timeout in milliseconds. Defaults to 120000 (2 minutes); the maximum is 1800000 (30 minutes)."`
	RunInBackground bool   `json:"run_in_background,omitempty" desc:"Run as a managed background task instead of waiting. Do not use shell backgrounding such as trailing & or nohup; those are rejected."`
	ResumeParent    bool   `json:"resume_parent,omitempty" desc:"When run_in_background is set, resume this run automatically once the task finishes."`
}

func (BashTool) Name() string        { return "Bash" }
func (BashTool) Description() string { return "Run a shell command in the agent working directory." }
func (BashTool) Schema() any         { return bashInput{} }
func (BashTool) Risk(input json.RawMessage) Risk {
	command := BashCommand(input)
	if analyzeBashCommand(command).warning != "" {
		return RiskDanger
	}
	var parsed bashInput
	_ = json.Unmarshal(input, &parsed)
	if parsed.RunInBackground && bashBackgroundEscapeWarning(command) != "" {
		return RiskDanger
	}
	return RiskExec
}

func BashCommand(input json.RawMessage) string {
	var parsed bashInput
	_ = json.Unmarshal(input, &parsed)
	return strings.TrimSpace(parsed.Command)
}

func BashDangerWarning(command string) string {
	return analyzeBashCommand(command).warning
}

func legacyBashDangerWarning(command string) string {
	return commandDangerWarning(legacyDangerLabel(command))
}

// legacyDangerLabel is the string-matching fallback retained as defense in depth
// for malformed, non-POSIX, and otherwise unclassified input. It returns the
// danger label rather than prose so callers can merge it into CommandFacts.
func legacyDangerLabel(command string) string {
	cmd := strings.TrimSpace(strings.ToLower(command))
	if cmd == "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) > 0 {
		switch strings.TrimLeft(fields[0], `\`) {
		case "rm", "rmdir", "unlink":
			return "file-delete"
		case "sudo", "doas":
			return "privilege-escalation"
		case "dd":
			return "disk-write"
		case "shred":
			return "file-destroy"
		case "wipefs", "blkdiscard", "sgdisk":
			return "disk-partition"
		}
		if strings.HasPrefix(fields[0], "mkfs") {
			return "disk-format"
		}
	}
	if strings.Contains(cmd, " shred ") || strings.HasPrefix(cmd, "shred ") {
		return "file-destroy"
	}
	if strings.Contains(cmd, "find ") && strings.Contains(cmd, " -delete") {
		return "find-delete"
	}
	if strings.HasPrefix(cmd, "git clean") && strings.Contains(cmd, "-f") {
		return "git-clean"
	}
	if strings.HasPrefix(cmd, "git reset") && strings.Contains(cmd, "--hard") {
		return "git-reset-hard"
	}
	if legacyNetworkFetchPattern.MatchString(cmd) && shellPipesToInterpreter(cmd) {
		return "network-pipe-shell"
	}
	if strings.Contains(cmd, "chmod") && strings.Contains(cmd, "-r") && strings.Contains(cmd, "777") {
		return "permission-weaken"
	}
	if strings.Contains(cmd, " /dev/null") && strings.HasPrefix(cmd, "mv ") {
		return "file-delete"
	}
	if truncatingRedirectPattern.MatchString(cmd) {
		return "file-truncate"
	}
	return ""
}

var truncatingRedirectPattern = regexp.MustCompile(`(^|\s|[;&|])(:\s*)?>\s*[^&\s]`)

var legacyNetworkFetchPattern = regexp.MustCompile(`(^|[\s;&|(])(curl|wget|fetch|aria2c)(\s|$)`)

// shellPipesToInterpreterPattern covers every interpreter that executes code
// read from stdin, not only shells: piping a download into python or node is
// the same remote-code-execution shape as piping it into sh.
var shellPipesToInterpreterPattern = regexp.MustCompile(`\|\s*(sh|bash|zsh|dash|ksh|python3?|perl|ruby|node|deno|bun|php|lua)(\s|$)`)

func shellPipesToInterpreter(cmd string) bool {
	return shellPipesToInterpreterPattern.MatchString(cmd)
}

var backgroundEscapeCommandPattern = regexp.MustCompile(`(?i)(^|[[:space:];&|()])(nohup|disown)([[:space:];&|()]|$)`)

func bashBackgroundEscapeWarning(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if strings.HasSuffix(command, "&") && !strings.HasSuffix(command, "&&") {
		return "Background tasks must be managed by Autoto; do not add shell '&' backgrounding."
	}
	facts := AnalyzeBashCommand(command)
	if facts.Background {
		return "Background tasks must be managed by Autoto; do not add shell '&' backgrounding."
	}
	if backgroundEscapeCommandPattern.MatchString(command) {
		return "Background tasks cannot use nohup or disown to escape Autoto cancellation and lifecycle management."
	}
	return ""
}

func (BashTool) Execute(ctx context.Context, call Call, env Env) (Result, error) {
	var input bashInput
	if err := StrictDecode(call.Input, &input); err != nil {
		return Result{Output: err.Error(), IsError: true}, nil
	}
	if strings.TrimSpace(input.Command) == "" {
		return Result{Output: "command is required", IsError: true}, nil
	}
	if input.Timeout > int(bashMaxTimeout/time.Millisecond) {
		return Result{Output: "timeout exceeds the 30 minute maximum", IsError: true}, nil
	}
	timeout := time.Duration(input.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if input.RunInBackground {
		if warning := bashBackgroundEscapeWarning(input.Command); warning != "" {
			return Result{Output: warning, IsError: true}, nil
		}
		if env.Background == nil {
			return Result{Output: "background task service is unavailable", IsError: true}, nil
		}
		if input.ResumeParent && strings.TrimSpace(env.RunID) == "" {
			return Result{Output: "resume_parent requires a durable parent run", IsError: true}, nil
		}
		payload, err := json.Marshal(map[string]any{
			"command":   input.Command,
			"timeoutMs": timeout.Milliseconds(),
			"cwd":       env.CWD,
		})
		if err != nil {
			return Result{}, err
		}
		publicSummary, _ := json.Marshal(AnalyzeBashCommand(input.Command))
		task, err := env.Background.Submit(ctx, BackgroundTaskRequest{
			Kind:                         BackgroundTaskKindShell,
			OwnerAgentID:                 env.AgentID,
			ParentRunID:                  env.RunID,
			ParentToolUseID:              call.ID,
			CWD:                          env.CWD,
			Payload:                      payload,
			PublicSummary:                publicSummary,
			ResumeParent:                 input.ResumeParent,
			PermissionModeCap:            env.PermissionModeCap,
			PermissionGenerationSnapshot: env.PermissionGenerationSnapshot,
			PolicyGenerationSnapshot:     env.PolicyGenerationSnapshot,
			AgentGenerationSnapshot:      env.AgentGenerationSnapshot,
			ToolCatalogDigest:            env.ToolCatalogDigest,
			WorkspaceFingerprint:         env.WorkspaceFingerprint,
		})
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{Output: "background shell task could not be created", IsError: true}, nil
		}
		encoded, _ := json.Marshal(task)
		return Result{Output: string(encoded), Meta: map[string]any{"backgroundTaskId": task.ID, "background": true}}, nil
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	shell := "/bin/sh"
	args := []string{"-c", input.Command}
	if runtime.GOOS == "windows" {
		shell = "cmd"
		args = []string{"/C", input.Command}
	}
	// Use plain Command (not CommandContext) so process.Group owns tree kill on
	// timeout/cancel. CommandContext only signals the direct child.
	cmd := exec.Command(shell, args...)
	if env.CWD != "" {
		cmd.Dir = env.CWD
	}
	collector := newBashOutputCollector(env.Output)
	cmd.Stdout = collector
	cmd.Stderr = collector
	group := process.Prepare(cmd)
	if err := cmd.Start(); err != nil {
		_ = group.Close()
		return Result{Output: err.Error(), IsError: true, Meta: map[string]any{"truncated": false}}, nil
	}
	if err := group.Started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = group.Close()
		return Result{Output: err.Error(), IsError: true, Meta: map[string]any{"truncated": false}}, nil
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
		_ = group.Close()
	case <-cmdCtx.Done():
		err = group.Terminate(cmd, done, 2*time.Second)
		_ = group.Close()
	}
	text, cut := collector.result()
	result := Result{Output: text, Meta: map[string]any{"truncated": cut}}
	if cmdCtx.Err() != nil {
		result.IsError = true
		result.Output += "\ncommand timed out"
		if env.Output != nil {
			env.Output(OutputChunk{Text: "\ncommand timed out\n", Stream: "combined"})
		}
		return result, nil
	}
	if err != nil {
		result.IsError = true
		if text == "" {
			result.Output = err.Error()
		}
	}
	return result, nil
}

type bashOutputCollector struct {
	mu              sync.Mutex
	resultBuilder   strings.Builder
	resultBytes     int
	resultTruncated bool
	streamBytes     int
	streamTruncated bool
	output          func(OutputChunk)
}

func newBashOutputCollector(output func(OutputChunk)) *bashOutputCollector {
	return &bashOutputCollector{output: output}
}

func (c *bashOutputCollector) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n := len(p)
	text := string(p)
	emitText := ""
	emitTruncationNotice := false
	c.mu.Lock()
	if c.resultBytes < bashResultMaxBytes {
		remaining := bashResultMaxBytes - c.resultBytes
		if n <= remaining {
			c.resultBuilder.WriteString(text)
			c.resultBytes += n
		} else {
			c.resultBuilder.WriteString(string(p[:remaining]))
			c.resultBytes += remaining
			c.resultTruncated = true
		}
	} else {
		c.resultTruncated = true
	}
	if c.output != nil && !c.streamTruncated {
		remaining := bashStreamMaxBytes - c.streamBytes
		if remaining > 0 {
			if n <= remaining {
				emitText = text
				c.streamBytes += n
			} else {
				emitText = string(p[:remaining])
				c.streamBytes += remaining
				c.streamTruncated = true
				emitTruncationNotice = true
			}
		} else {
			c.streamTruncated = true
			emitTruncationNotice = true
		}
	}
	c.mu.Unlock()
	if c.output != nil {
		if emitText != "" {
			c.output(OutputChunk{Text: emitText, Stream: "combined"})
		}
		if emitTruncationNotice {
			c.output(OutputChunk{Text: "\n...[stream truncated]\n", Stream: "combined", Truncated: true})
		}
	}
	return n, nil
}

func (c *bashOutputCollector) result() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	text := c.resultBuilder.String()
	if !c.resultTruncated {
		return text, false
	}
	return text + "\n...[truncated]", true
}
