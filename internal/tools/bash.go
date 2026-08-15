package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"autoto/internal/process"
)

type BashTool struct{}

const (
	bashResultMaxBytes = 20000
	bashStreamMaxBytes = 100000
	// A truncated result keeps the first 40% and the last 60% of the output
	// instead of the first 100%. What a command failed with — the non-zero exit
	// summary, the last stack frame, the test that broke — is written last, so a
	// head-only cut discarded exactly the part the model needs and left it
	// re-running the command to see the end.
	bashResultHeadBytes = bashResultMaxBytes * 2 / 5
	bashResultTailBytes = bashResultMaxBytes - bashResultHeadBytes
	bashMaxTimeout      = 30 * time.Minute
	// How long output may keep arriving after the shell itself has exited. Only
	// a process the shell detached can still hold the pipes open at that point,
	// and waiting on one of those is waiting on the wrong thing.
	bashDetachedIOGrace = time.Second
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

func bashInputFrom(raw json.RawMessage) bashInput {
	var parsed bashInput
	_ = json.Unmarshal(raw, &parsed)
	return parsed
}

func (BashTool) Risk(input json.RawMessage) Risk {
	parsed := bashInputFrom(input)
	command := strings.TrimSpace(parsed.Command)
	if analyzeBashCommand(command).warning != "" {
		return RiskDanger
	}
	if parsed.RunInBackground && bashBackgroundEscapeWarning(command) != "" {
		return RiskDanger
	}
	return RiskExec
}

func BashCommand(input json.RawMessage) string {
	return strings.TrimSpace(bashInputFrom(input).Command)
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
	if head := legacyEffectiveCommand(fields); head != "" {
		switch head {
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
		if strings.HasPrefix(head, "mkfs") {
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
	if hasTruncatingRedirect(cmd) {
		return "file-truncate"
	}
	return ""
}

// legacyEffectiveCommand peels a leading chain of shell and exec wrappers so the
// string fallback inspects the command that actually runs rather than the
// launcher. Without it `env rm -rf /`, `timeout 5 rm -rf /`, and `wsl rm -rf /`
// slip past a fields[0]-only check. Privileged wrappers (sudo/doas) are left in
// place because they carry their own danger label and must not be skipped. The
// returned token is stripped of any directory prefix and the `\`-alias bypass.
func legacyEffectiveCommand(fields []string) string {
	wrappers := map[string]struct{}{
		"env": {}, "timeout": {}, "gtimeout": {}, "setsid": {}, "stdbuf": {},
		"nice": {}, "ionice": {}, "chrt": {}, "taskset": {}, "nohup": {},
		"wsl": {}, "sh": {}, "bash": {}, "zsh": {}, "ksh": {}, "dash": {},
	}
	for index := 0; index < len(fields) && index < 16; index++ {
		token := legacyBareCommand(fields[index])
		if token == "" {
			continue
		}
		if _, isWrapper := wrappers[token]; isWrapper {
			continue
		}
		// A leading option or operand belongs to the preceding wrapper -- an
		// option flag, an `env` VAR=value assignment, or a bare duration such as
		// the `5` in `timeout 5 rm` -- so it is not the command itself.
		if strings.HasPrefix(fields[index], "-") || strings.Contains(token, "=") || legacyNumericOperand(token) {
			continue
		}
		return token
	}
	return ""
}

func legacyBareCommand(field string) string {
	field = strings.TrimLeft(field, `\`)
	if sep := strings.LastIndexAny(field, `/\`); sep >= 0 {
		field = field[sep+1:]
	}
	return field
}

// legacyNumericOperand reports whether a token is a bare number or duration
// operand (`5`, `10s`, `1.5`), which some wrappers take before the command.
func legacyNumericOperand(token string) bool {
	trimmed := strings.TrimRight(token, "smhd")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// hasTruncatingRedirect reports whether the raw command text contains a `>`
// redirect that would overwrite something worth keeping. Shapes that destroy
// nothing are excluded, and all are routine on the cmd.exe lines this string-level
// layer exists to catch: `>>` appends, a discard sink such as `2>NUL` or
// `>/dev/null` only silences output, and a path under the OS temp directory holds
// nothing of value.
func hasTruncatingRedirect(cmd string) bool {
	for _, match := range truncatingRedirectPattern.FindAllStringSubmatch(cmd, -1) {
		target := match[3]
		// An empty target means the `>` was followed by `&` or nothing, so no file
		// is named; a target starting with `>` is the append operator.
		if target == "" || strings.HasPrefix(target, ">") {
			continue
		}
		if classifyRedirectTarget(target) > redirectTargetScratch {
			return true
		}
	}
	return false
}

// The trailing group captures the redirection target so the caller can tell a
// real file from an append or a discard sink. The target stops at every shell
// separator, not just `&` and whitespace: leaving `;` or `,` attached turned
// `>NUL;` into the unknown file "NUL;", which no longer matched the discard
// list and hard-blocked a command that destroys nothing.
var truncatingRedirectPattern = regexp.MustCompile(`(^|\s|[;&|])(:\s*)?>\s*([^&|<>;,\s]*)`)

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
	useShellCommandLine(cmd, input.Command)
	// A shell that detaches a child (`start` on Windows, a backgrounded child
	// elsewhere) exits at once while that grandchild keeps the inherited
	// stdout/stderr pipes open. Without a bound, Wait blocks on the pipes rather
	// than on the command, so launching anything long-lived cost the full
	// timeout and was reported as a failure. WaitDelay closes the pipes shortly
	// after the shell itself is gone.
	cmd.WaitDelay = bashDetachedIOGrace
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
	// Wait reports ErrWaitDelay when it had to close the pipes on a process the
	// shell left behind. The shell itself finished, so this is not a failure of
	// the command the model asked for.
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
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
	headBuilder     strings.Builder
	headBytes       int
	tail            tailRing
	totalBytes      int
	streamBytes     int
	streamTruncated bool
	output          func(OutputChunk)
}

func newBashOutputCollector(output func(OutputChunk)) *bashOutputCollector {
	return &bashOutputCollector{output: output, tail: newTailRing(bashResultTailBytes)}
}

// tailRing keeps the last N bytes written to it in a fixed buffer. A slice that
// re-sliced on every write would copy the whole retained window per call, and a
// command that logs line by line calls Write tens of thousands of times.
type tailRing struct {
	buf   []byte
	start int
	size  int
}

func newTailRing(capacity int) tailRing {
	if capacity < 0 {
		capacity = 0
	}
	return tailRing{buf: make([]byte, capacity)}
}

func (t *tailRing) write(p []byte) {
	capacity := len(t.buf)
	if capacity == 0 || len(p) == 0 {
		return
	}
	if len(p) >= capacity {
		copy(t.buf, p[len(p)-capacity:])
		t.start, t.size = 0, capacity
		return
	}
	end := (t.start + t.size) % capacity
	written := copy(t.buf[end:], p)
	if written < len(p) {
		copy(t.buf, p[written:])
	}
	if t.size+len(p) <= capacity {
		t.size += len(p)
		return
	}
	t.size = capacity
	t.start = (end + len(p)) % capacity
}

func (t *tailRing) bytes() []byte {
	if t.size == 0 {
		return nil
	}
	out := make([]byte, 0, t.size)
	if t.start+t.size <= len(t.buf) {
		return append(out, t.buf[t.start:t.start+t.size]...)
	}
	out = append(out, t.buf[t.start:]...)
	return append(out, t.buf[:t.size-(len(t.buf)-t.start)]...)
}

// trimPartialRuneSuffix drops an incomplete UTF-8 sequence left at a byte-count
// cut. Only the final three bytes are inspected: if no rune start is found
// there the output is binary and is passed through untouched rather than
// silently shortened.
func trimPartialRuneSuffix(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-3; i-- {
		if !utf8.RuneStart(b[i]) {
			continue
		}
		if r, size := utf8.DecodeRune(b[i:]); r == utf8.RuneError && size <= 1 {
			return b[:i]
		}
		return b
	}
	return b
}

// trimPartialRunePrefix drops the continuation bytes a byte-count cut can leave
// at the front of the retained tail.
func trimPartialRunePrefix(b []byte) []byte {
	for i := 0; i < len(b) && i < 4; i++ {
		if utf8.RuneStart(b[i]) {
			return b[i:]
		}
	}
	return b
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
	c.totalBytes += n
	rest := p
	if c.headBytes < bashResultHeadBytes {
		room := bashResultHeadBytes - c.headBytes
		if len(rest) <= room {
			c.headBuilder.Write(rest)
			c.headBytes += len(rest)
			rest = nil
		} else {
			c.headBuilder.Write(rest[:room])
			c.headBytes += room
			rest = rest[room:]
		}
	}
	c.tail.write(rest)
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
	head := []byte(c.headBuilder.String())
	tail := c.tail.bytes()
	dropped := c.totalBytes - len(head) - len(tail)
	if dropped <= 0 {
		// Nothing was discarded, so head+tail is the whole output and neither
		// join can fall inside a rune.
		return string(head) + string(tail), false
	}
	marker := fmt.Sprintf("\n...[%d bytes truncated]...\n", dropped)
	return string(trimPartialRuneSuffix(head)) + marker + string(trimPartialRunePrefix(tail)), true
}
